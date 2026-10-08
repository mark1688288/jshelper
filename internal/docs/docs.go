// Package docs creates one Google Doc in a Drive folder, imports a PDF as a Google Doc, and uploads JSON files there.
package docs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"golang.org/x/oauth2"
	docsapi "google.golang.org/api/docs/v1"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

const MaxBodyBytes = 1 << 20 // 1048576

const (
	GoogleDocMime   = "application/vnd.google-apps.document"
	DriveFolderMime = "application/vnd.google-apps.folder"
	JSONMime        = "application/json"
	PDFMime         = "application/pdf"
)

// rpcTimeout is the deadline for one Drive or Docs RPC.
// Each call gets its own timeout. This is not an environment key.
const rpcTimeout = 60 * time.Second

var (
	errIndex        = errors.New("docs-cli: error: DOCS_INSERT_INDEX must be 1")
	errBodyTooLarge = errors.New("docs-cli: body exceeds 1 MiB (1048576 bytes)")
	errFolderNeeded = errors.New("docs-cli: GOOGLE_DRIVE_FOLDER_ID is required")
)

// EditURL is the browser URL for a document id.
func EditURL(documentID string) string {
	return "https://docs.google.com/document/d/" + documentID + "/edit"
}

// FileURL is the browser URL for an uploaded Drive file id.
func FileURL(fileID string) string {
	return "https://drive.google.com/file/d/" + fileID + "/view"
}

// JSONFile is one JSON upload. Name is the Drive file name. Body is the original bytes.
type JSONFile struct {
	Name string
	Body []byte
}

// NewDocsService builds a Docs client. Extra options are for tests.
// Scope is not added here; the token source already carries it.
func NewDocsService(ctx context.Context, src oauth2.TokenSource, opts ...option.ClientOption) (*docsapi.Service, error) {
	all := append([]option.ClientOption{option.WithTokenSource(src)}, opts...)
	return docsapi.NewService(ctx, all...)
}

// NewDriveService builds a Drive client. Extra options are for tests.
// Scope is not added here; the token source already carries it.
func NewDriveService(ctx context.Context, src oauth2.TokenSource, opts ...option.ClientOption) (*drive.Service, error) {
	all := append([]option.ClientOption{option.WithTokenSource(src)}, opts...)
	return drive.NewService(ctx, all...)
}

// CreateDocument creates a Google Doc in folderID and, when body is non-empty,
// inserts that text at index 1. folderID is already a normalized id.
// The returned file id is the document id. A failed insert leaves the file in place.
func CreateDocument(ctx context.Context, driveSvc *drive.Service, docsSvc *docsapi.Service, title, body, folderID string, insertIndex int64) (string, error) {
	if insertIndex != 1 {
		return "", errIndex
	}
	if len(body) > MaxBodyBytes {
		return "", errBodyTooLarge
	}
	if folderID == "" {
		return "", errFolderNeeded
	}
	if driveSvc == nil || (body != "" && docsSvc == nil) {
		return "", errors.New("docs-cli: error: missing Google service")
	}

	if err := requireFolder(ctx, driveSvc, folderID); err != nil {
		return "", err
	}

	createCtx, cancelCreate := rpcContext(ctx)
	created, err := driveSvc.Files.Create(&drive.File{
		Name:     title,
		MimeType: GoogleDocMime,
		Parents:  []string{folderID},
	}).SupportsAllDrives(true).Fields("id,parents").Context(createCtx).Do()
	cancelCreate()
	if err != nil {
		return "", fmt.Errorf("docs-cli: error: %s", publicErr(err))
	}
	if created == nil || created.Id == "" {
		return "", errors.New("docs-cli: error: files.create returned an empty id")
	}
	if !hasParent(created.Parents, folderID) {
		return "", fmt.Errorf("docs-cli: document parent is not the requested folder: %s", folderID)
	}
	if body == "" {
		return created.Id, nil
	}

	updateCtx, cancelUpdate := rpcContext(ctx)
	_, err = docsSvc.Documents.BatchUpdate(created.Id, &docsapi.BatchUpdateDocumentRequest{
		Requests: []*docsapi.Request{{
			InsertText: &docsapi.InsertTextRequest{
				Text:     body,
				Location: &docsapi.Location{Index: insertIndex},
			},
		}},
	}).Context(updateCtx).Do()
	cancelUpdate()
	if err != nil {
		return created.Id, fmt.Errorf("docs-cli: insertText failed; document was created: %s", created.Id)
	}
	return created.Id, nil
}

// ImportPDF converts pdf into one Google Doc named title in folderID.
// The media is the original PDF. A same title creates another document.
func ImportPDF(ctx context.Context, driveSvc *drive.Service, title, folderID string, pdf []byte) (string, error) {
	if folderID == "" {
		return "", errFolderNeeded
	}
	if driveSvc == nil {
		return "", errors.New("docs-cli: error: missing Google service")
	}
	if err := requireFolder(ctx, driveSvc, folderID); err != nil {
		return "", err
	}
	createCtx, cancelCreate := rpcContext(ctx)
	created, err := driveSvc.Files.Create(&drive.File{
		Name:     title,
		MimeType: GoogleDocMime,
		Parents:  []string{folderID},
	}).Media(bytes.NewReader(pdf), googleapi.ContentType(PDFMime)).
		SupportsAllDrives(true).
		Fields("id,parents,mimeType").
		Context(createCtx).Do()
	cancelCreate()
	if err != nil {
		return "", fmt.Errorf("docs-cli: error: %s", publicErr(err))
	}
	if created == nil || created.Id == "" {
		return "", errors.New("docs-cli: error: files.create returned an empty id")
	}
	if created.MimeType != GoogleDocMime {
		return "", fmt.Errorf("docs-cli: import did not create a Google Doc: %s", created.Id)
	}
	if !hasParent(created.Parents, folderID) {
		return "", fmt.Errorf("docs-cli: document parent is not the requested folder: %s", folderID)
	}
	return created.Id, nil
}

// UploadJSON checks folderID once, then creates one application/json file per item.
// A later failure returns the ids already created. Those files stay in the folder.
func UploadJSON(ctx context.Context, driveSvc *drive.Service, folderID string, files []JSONFile) ([]string, error) {
	if folderID == "" {
		return nil, errFolderNeeded
	}
	if driveSvc == nil {
		return nil, errors.New("docs-cli: error: missing Google service")
	}
	if err := requireFolder(ctx, driveSvc, folderID); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(files))
	for _, file := range files {
		id, err := createJSONFile(ctx, driveSvc, folderID, file)
		if err != nil {
			return ids, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func requireFolder(ctx context.Context, driveSvc *drive.Service, folderID string) error {
	getCtx, cancelGet := rpcContext(ctx)
	got, err := driveSvc.Files.Get(folderID).
		SupportsAllDrives(true).
		Fields("id,mimeType,name,trashed").
		Context(getCtx).Do()
	cancelGet()
	if err != nil {
		if code := statusCode(err); code == http.StatusNotFound || code == http.StatusForbidden {
			return fmt.Errorf("docs-cli: folder not found or not accessible: %s", folderID)
		}
		return fmt.Errorf("docs-cli: error: %s", publicErr(err))
	}
	if got == nil {
		return errors.New("docs-cli: error: files.get returned no file")
	}
	if got.MimeType != DriveFolderMime {
		return fmt.Errorf("docs-cli: GOOGLE_DRIVE_FOLDER_ID is not a folder: %s", folderID)
	}
	if got.Trashed {
		return fmt.Errorf("docs-cli: folder is in the trash: %s", folderID)
	}
	return nil
}

func createJSONFile(ctx context.Context, driveSvc *drive.Service, folderID string, file JSONFile) (string, error) {
	createCtx, cancelCreate := rpcContext(ctx)
	created, err := driveSvc.Files.Create(&drive.File{
		Name:     file.Name,
		MimeType: JSONMime,
		Parents:  []string{folderID},
	}).Media(bytes.NewReader(file.Body), googleapi.ContentType(JSONMime)).
		SupportsAllDrives(true).
		Fields("id,parents").
		Context(createCtx).Do()
	cancelCreate()
	if err != nil {
		return "", fmt.Errorf("docs-cli: error: %s", publicErr(err))
	}
	if created == nil || created.Id == "" {
		return "", errors.New("docs-cli: error: files.create returned an empty id")
	}
	if !hasParent(created.Parents, folderID) {
		return "", fmt.Errorf("docs-cli: file parent is not the requested folder: %s", folderID)
	}
	return created.Id, nil
}

func rpcContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, rpcTimeout)
}

func hasParent(parents []string, folderID string) bool {
	for _, parent := range parents {
		if parent == folderID {
			return true
		}
	}
	return false
}

func statusCode(err error) int {
	var gerr *googleapi.Error
	if errors.As(err, &gerr) {
		return gerr.Code
	}
	return 0
}

func publicErr(err error) string {
	var gerr *googleapi.Error
	if errors.As(err, &gerr) {
		if gerr.Message != "" {
			return fmt.Sprintf("googleapi: Error %d: %s", gerr.Code, gerr.Message)
		}
		return fmt.Sprintf("googleapi: Error %d", gerr.Code)
	}
	msg := err.Error()
	if strings.Contains(strings.ToLower(msg), "authorization") || strings.Contains(msg, "Bearer ") {
		return "request failed"
	}
	return msg
}
