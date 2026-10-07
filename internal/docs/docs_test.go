package docs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"
	docsapi "google.golang.org/api/docs/v1"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"

	"github.com/mark1688288/jshelper/internal/config"
)

const (
	testFolderID = "1AbC_def-234"
	testDocID    = "doc9876543210"
)

func TestRPCTimeoutIsOneMinute(t *testing.T) {
	if rpcTimeout != 60*time.Second {
		t.Fatalf("rpcTimeout=%s", rpcTimeout)
	}
}

func TestEditURL(t *testing.T) {
	if got, want := EditURL(testDocID), "https://docs.google.com/document/d/"+testDocID+"/edit"; got != want {
		t.Fatalf("url %q", got)
	}
}

func TestFileURL(t *testing.T) {
	if got, want := FileURL(testDocID), "https://drive.google.com/file/d/"+testDocID+"/view"; got != want {
		t.Fatalf("url %q", got)
	}
}

func TestCreateDocumentRejectsBeforeHTTP(t *testing.T) {
	rec := &recorder{}
	driveSvc, docsSvc := newServices(t, rec)
	cases := []struct {
		name   string
		body   string
		index  int64
		folder string
		want   string
	}{
		{name: "index 0", body: "hello", index: 0, folder: testFolderID, want: "DOCS_INSERT_INDEX must be 1"},
		{name: "index 2", body: "hello", index: 2, folder: testFolderID, want: "DOCS_INSERT_INDEX must be 1"},
		{name: "empty folder", body: "hello", index: 1, folder: "", want: "GOOGLE_DRIVE_FOLDER_ID is required"},
		{name: "oversize", body: strings.Repeat("a", MaxBodyBytes+1), index: 1, folder: testFolderID, want: "body exceeds 1 MiB (1048576 bytes)"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			rec.reset()
			_, err := CreateDocument(context.Background(), driveSvc, docsSvc, "Demo", tt.body, tt.folder, tt.index)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err %v", err)
			}
			if len(rec.snapshot()) != 0 {
				t.Fatalf("requests %d", len(rec.snapshot()))
			}
		})
	}
}

func TestInsertIndexZeroDoesNotUseForceSendFields(t *testing.T) {
	src, err := os.ReadFile("docs.go")
	if err != nil {
		t.Fatal(err)
	}
	if bytesContains(src, "ForceSendFields") {
		t.Fatal("ForceSendFields is present")
	}
}

func TestFolderURLUsesOnlyTheID(t *testing.T) {
	raw := "https://drive.google.com/drive/folders/" + testFolderID + "?usp=sharing"
	id, err := config.NormalizeFolderID(raw)
	if err != nil {
		t.Fatal(err)
	}
	if id != testFolderID {
		t.Fatalf("id %q", id)
	}
	rec := &recorder{getBody: folderJSON(testFolderID, DriveFolderMime, false), createBody: fileJSON(testDocID, testFolderID)}
	driveSvc, docsSvc := newServices(t, rec)
	got, err := CreateDocument(context.Background(), driveSvc, docsSvc, "Demo", "", id, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got != testDocID {
		t.Fatalf("id %q", got)
	}
	calls := rec.snapshot()
	if len(calls) != 2 {
		t.Fatalf("calls %+v", calls)
	}
	get := calls[0]
	if get.method != http.MethodGet || get.path != "/files/"+testFolderID {
		t.Fatalf("get %s %s", get.method, get.path)
	}
	if strings.Contains(get.path, "https") || strings.Contains(get.path, "folders") || strings.Contains(get.path, "usp=sharing") {
		t.Fatalf("path %s", get.path)
	}
	if strings.Contains(get.rawQuery, "https") || strings.Contains(get.rawQuery, "folders") || strings.Contains(get.rawQuery, "usp=sharing") {
		t.Fatalf("query %s", get.rawQuery)
	}
	if strings.Contains(get.host, "googleapis.com") {
		t.Fatalf("host %s", get.host)
	}
}

func TestFolderDeniedDoesNotCreate(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			rec := &recorder{getStatus: status, getBody: `{"error":{"code":` + fmt.Sprint(status) + `,"message":"nope"}}`}
			driveSvc, docsSvc := newServices(t, rec)
			_, err := CreateDocument(context.Background(), driveSvc, docsSvc, "Demo", "hello", testFolderID, 1)
			want := "docs-cli: folder not found or not accessible: " + testFolderID
			if err == nil || err.Error() != want {
				t.Fatalf("err %v", err)
			}
			if creates := rec.count(http.MethodPost, "/files"); creates != 0 {
				t.Fatalf("creates %d", creates)
			}
			assertNoDocumentsCreate(t, rec.snapshot())
		})
	}
}

func TestFolderMimeAndTrashStopCreate(t *testing.T) {
	rec := &recorder{getBody: folderJSON(testFolderID, GoogleDocMime, false)}
	driveSvc, docsSvc := newServices(t, rec)
	_, err := CreateDocument(context.Background(), driveSvc, docsSvc, "Demo", "hello", testFolderID, 1)
	want := "docs-cli: GOOGLE_DRIVE_FOLDER_ID is not a folder: " + testFolderID
	if err == nil || err.Error() != want {
		t.Fatalf("err %v", err)
	}
	if rec.count(http.MethodPost, "/files") != 0 {
		t.Fatal("created a file for a non-folder")
	}

	rec.reset()
	rec.getBody = folderJSON(testFolderID, DriveFolderMime, true)
	_, err = CreateDocument(context.Background(), driveSvc, docsSvc, "Demo", "hello", testFolderID, 1)
	want = "docs-cli: folder is in the trash: " + testFolderID
	if err == nil || err.Error() != want {
		t.Fatalf("err %v", err)
	}
	if rec.count(http.MethodPost, "/files") != 0 {
		t.Fatal("created a file in a trashed folder")
	}
}

func TestCreateDocumentParentsAndInsert(t *testing.T) {
	rec := &recorder{
		getBody:    folderJSON(testFolderID, DriveFolderMime, false),
		createBody: fileJSON(testDocID, testFolderID),
		updateBody: `{}`,
	}
	driveSvc, docsSvc := newServices(t, rec)
	const body = "hello"
	id, err := CreateDocument(context.Background(), driveSvc, docsSvc, "Demo", body, testFolderID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if id != testDocID {
		t.Fatalf("id %q", id)
	}
	calls := rec.snapshot()
	assertNoDocumentsCreate(t, calls)
	var creates, updates int
	for _, call := range calls {
		if call.host != "" && strings.Contains(call.host, "googleapis.com") {
			t.Fatalf("host %s", call.host)
		}
		if deadlineBad(call.remain) {
			t.Fatalf("deadline remain %s for %s", call.remain, call.path)
		}
		switch {
		case call.method == http.MethodGet && call.path == "/files/"+testFolderID:
			if call.query("supportsAllDrives") != "true" || call.query("fields") != "id,mimeType,name,trashed" {
				t.Fatalf("get query %s", call.rawQuery)
			}
		case call.method == http.MethodPost && call.path == "/files":
			creates++
			if call.query("supportsAllDrives") != "true" || call.query("fields") != "id,parents" {
				t.Fatalf("create query %s", call.rawQuery)
			}
			assertCreateBody(t, call.body, "Demo", testFolderID)
		case call.method == http.MethodPost && strings.Contains(call.path, ":batchUpdate"):
			updates++
			assertInsert(t, call.body, body)
		default:
			t.Fatalf("unexpected %s %s", call.method, call.path)
		}
	}
	if creates != 1 || updates != 1 {
		t.Fatalf("creates %d updates %d", creates, updates)
	}
}

func TestEmptyBodySkipsBatchUpdate(t *testing.T) {
	rec := &recorder{
		getBody:    folderJSON(testFolderID, DriveFolderMime, false),
		createBody: fileJSON(testDocID, testFolderID),
	}
	driveSvc, docsSvc := newServices(t, rec)
	id, err := CreateDocument(context.Background(), driveSvc, docsSvc, "Demo", "", testFolderID, 1)
	if err != nil || id != testDocID {
		t.Fatalf("id %q err %v", id, err)
	}
	for _, call := range rec.snapshot() {
		if strings.Contains(call.path, "batchUpdate") {
			t.Fatalf("batchUpdate %s", call.path)
		}
	}
	assertNoDocumentsCreate(t, rec.snapshot())
	if rec.count(http.MethodPost, "/files") != 1 {
		t.Fatal("expected one files.create")
	}
}

func TestParentMismatchSkipsInsert(t *testing.T) {
	rec := &recorder{
		getBody:    folderJSON(testFolderID, DriveFolderMime, false),
		createBody: fileJSON(testDocID, "otherFolder1"),
	}
	driveSvc, docsSvc := newServices(t, rec)
	_, err := CreateDocument(context.Background(), driveSvc, docsSvc, "Demo", "hello", testFolderID, 1)
	want := "docs-cli: document parent is not the requested folder: " + testFolderID
	if err == nil || err.Error() != want {
		t.Fatalf("err %v", err)
	}
	for _, call := range rec.snapshot() {
		if strings.Contains(call.path, "batchUpdate") {
			t.Fatal("insertText was called")
		}
	}
	if rec.count(http.MethodPost, "/files") != 1 {
		t.Fatal("files.create count")
	}
}

func TestInsertFailureKeepsDocument(t *testing.T) {
	rec := &recorder{
		getStatus:    http.StatusOK,
		getBody:      folderJSON(testFolderID, DriveFolderMime, false),
		createBody:   fileJSON(testDocID, testFolderID),
		updateStatus: http.StatusInternalServerError,
		updateBody:   `{"error":{"code":500,"message":"insert failed"}}`,
	}
	driveSvc, docsSvc := newServices(t, rec)
	id, err := CreateDocument(context.Background(), driveSvc, docsSvc, "Demo", "hello", testFolderID, 1)
	want := "docs-cli: insertText failed; document was created: " + testDocID
	if id != testDocID || err == nil || err.Error() != want {
		t.Fatalf("id %q err %v", id, err)
	}
	if rec.count(http.MethodPost, "/files") != 1 {
		t.Fatal("files.create was retried")
	}
}

func TestBodyCapAllowsOneMiBAndStopsAbove(t *testing.T) {
	rec := &recorder{
		getBody:    folderJSON(testFolderID, DriveFolderMime, false),
		createBody: fileJSON(testDocID, testFolderID),
		updateBody: `{}`,
	}
	driveSvc, docsSvc := newServices(t, rec)
	// 1048576 bytes is inside the product cap, so files.create is attempted.
	// This does not mean Docs accepts that many characters.
	body := strings.Repeat("a", MaxBodyBytes)
	if _, err := CreateDocument(context.Background(), driveSvc, docsSvc, "Demo", body, testFolderID, 1); err != nil {
		t.Fatal(err)
	}
	if rec.count(http.MethodPost, "/files") != 1 {
		t.Fatal("expected files.create at the product cap")
	}

	rec.reset()
	rec.getBody = folderJSON(testFolderID, DriveFolderMime, false)
	_, err := CreateDocument(context.Background(), driveSvc, docsSvc, "Demo", strings.Repeat("b", MaxBodyBytes+1), testFolderID, 1)
	if err == nil || !strings.Contains(err.Error(), "1048576") {
		t.Fatalf("err %v", err)
	}
	if len(rec.snapshot()) != 0 {
		t.Fatal("oversized body issued HTTP")
	}
}

func TestGetFailureHidesAuthorization(t *testing.T) {
	rec := &recorder{
		getStatus: http.StatusInternalServerError,
		getBody:   `{"error":{"code":500,"message":"backend down"},"secret":"Bearer super-secret-value"}`,
	}
	driveSvc, docsSvc := newServices(t, rec)
	_, err := CreateDocument(context.Background(), driveSvc, docsSvc, "Demo", "hello", testFolderID, 1)
	if err == nil {
		t.Fatal("expected error")
	}
	text := err.Error()
	if strings.Contains(text, "Bearer") || strings.Contains(text, "super-secret-value") || strings.Contains(text, "Authorization") {
		t.Fatalf("leaked %s", text)
	}
	if !strings.Contains(text, "docs-cli: error:") || !strings.Contains(text, "backend down") {
		t.Fatalf("err %s", text)
	}
	if rec.count(http.MethodPost, "/files") != 0 {
		t.Fatal("created after get failed")
	}
}

func TestUploadJSONTwoFiles(t *testing.T) {
	const (
		id1 = "file1111111111"
		id2 = "file2222222222"
	)
	one := []byte("{\n  \"k\": \"ONE\"\n}\n")
	two := []byte("[1, 2, 3]")
	rec := &recorder{
		getBody: folderJSON(testFolderID, DriveFolderMime, false),
		uploads: []uploadReply{
			{body: fileJSON(id1, testFolderID)},
			{body: fileJSON(id2, testFolderID)},
		},
	}
	driveSvc, _ := newServices(t, rec)
	ids, err := UploadJSON(context.Background(), driveSvc, testFolderID, []JSONFile{
		{Name: "1.json", Body: one},
		{Name: "2.json", Body: two},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != id1 || ids[1] != id2 {
		t.Fatalf("ids %#v", ids)
	}
	var gets, creates int
	for _, call := range rec.snapshot() {
		if deadlineBad(call.remain) {
			t.Fatalf("deadline remain %s for %s", call.remain, call.path)
		}
		switch {
		case call.method == http.MethodGet && call.path == "/files/"+testFolderID:
			gets++
			if call.query("supportsAllDrives") != "true" || call.query("fields") != "id,mimeType,name,trashed" {
				t.Fatalf("get query %s", call.rawQuery)
			}
		case call.method == http.MethodPost && call.path == "/upload/drive/v3/files":
			creates++
			if call.query("uploadType") != "multipart" || call.query("supportsAllDrives") != "true" || call.query("fields") != "id,parents" {
				t.Fatalf("upload query %s", call.rawQuery)
			}
			payload := one
			name := "1.json"
			if creates == 2 {
				payload = two
				name = "2.json"
			}
			assertMultipartUpload(t, call, name, testFolderID, payload)
		default:
			t.Fatalf("unexpected %s %s", call.method, call.path)
		}
	}
	if gets != 1 || creates != 2 {
		t.Fatalf("gets %d creates %d", gets, creates)
	}
}

func TestUploadFolderDeniedDoesNotCreate(t *testing.T) {
	rec := &recorder{getStatus: http.StatusNotFound, getBody: `{"error":{"code":404,"message":"nope"}}`}
	driveSvc, _ := newServices(t, rec)
	ids, err := UploadJSON(context.Background(), driveSvc, testFolderID, []JSONFile{{Name: "1.json", Body: []byte("{}")}})
	if err == nil || !strings.Contains(err.Error(), "folder not found or not accessible: "+testFolderID) {
		t.Fatalf("ids %#v err %v", ids, err)
	}
	if len(ids) != 0 || rec.count(http.MethodPost, "/upload/drive/v3/files") != 0 {
		t.Fatalf("ids %#v calls %d", ids, rec.count(http.MethodPost, "/upload/drive/v3/files"))
	}
}

func TestUploadSecondCreateKeepsFirstID(t *testing.T) {
	const id1 = "file1111111111"
	rec := &recorder{
		getBody: folderJSON(testFolderID, DriveFolderMime, false),
		uploads: []uploadReply{
			{body: fileJSON(id1, testFolderID)},
			{status: http.StatusInternalServerError, body: `{"error":{"code":500,"message":"second failed"}}`},
		},
	}
	driveSvc, _ := newServices(t, rec)
	ids, err := UploadJSON(context.Background(), driveSvc, testFolderID, []JSONFile{
		{Name: "1.json", Body: []byte(`{"n":1}`)},
		{Name: "2.json", Body: []byte(`{"n":2}`)},
	})
	if err == nil || !strings.Contains(err.Error(), "docs-cli: error:") || !strings.Contains(err.Error(), "second failed") {
		t.Fatalf("ids %#v err %v", ids, err)
	}
	if len(ids) != 1 || ids[0] != id1 {
		t.Fatalf("ids %#v", ids)
	}
	if rec.count(http.MethodPost, "/upload/drive/v3/files") != 2 {
		t.Fatalf("creates %d", rec.count(http.MethodPost, "/upload/drive/v3/files"))
	}
}

func TestUploadEmptyFolderBeforeHTTP(t *testing.T) {
	rec := &recorder{}
	driveSvc, _ := newServices(t, rec)
	ids, err := UploadJSON(context.Background(), driveSvc, "", []JSONFile{{Name: "1.json", Body: []byte("{}")}})
	if err == nil || !strings.Contains(err.Error(), "GOOGLE_DRIVE_FOLDER_ID is required") {
		t.Fatalf("ids %#v err %v", ids, err)
	}
	if len(ids) != 0 || len(rec.snapshot()) != 0 {
		t.Fatalf("ids %#v calls %d", ids, len(rec.snapshot()))
	}
}

func TestUploadParentMismatch(t *testing.T) {
	rec := &recorder{
		getBody: folderJSON(testFolderID, DriveFolderMime, false),
		uploads: []uploadReply{{body: fileJSON(testDocID, "otherFolder1")}},
	}
	driveSvc, _ := newServices(t, rec)
	ids, err := UploadJSON(context.Background(), driveSvc, testFolderID, []JSONFile{{Name: "1.json", Body: []byte("{}")}})
	want := "docs-cli: file parent is not the requested folder: " + testFolderID
	if err == nil || err.Error() != want || len(ids) != 0 {
		t.Fatalf("ids %#v err %v", ids, err)
	}
}

func assertMultipartUpload(t *testing.T, call apiCall, name, folderID string, payload []byte) {
	t.Helper()
	mediaType, params, err := mime.ParseMediaType(call.contentType)
	if err != nil || mediaType != "multipart/related" || params["boundary"] == "" {
		t.Fatalf("content type %q err %v", call.contentType, err)
	}
	parts := multipart.NewReader(bytes.NewReader(call.body), params["boundary"])
	metaPart, err := parts.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	metaRaw, err := io.ReadAll(metaPart)
	if err != nil {
		t.Fatal(err)
	}
	var meta struct {
		Name     string   `json:"name"`
		MimeType string   `json:"mimeType"`
		Parents  []string `json:"parents"`
	}
	if err := json.Unmarshal(metaRaw, &meta); err != nil {
		t.Fatalf("meta %s: %v", metaRaw, err)
	}
	if meta.Name != name || meta.MimeType != JSONMime || len(meta.Parents) != 1 || meta.Parents[0] != folderID {
		t.Fatalf("meta %s", metaRaw)
	}
	if meta.MimeType == GoogleDocMime || bytesContains(metaRaw, GoogleDocMime) {
		t.Fatalf("meta %s", metaRaw)
	}
	mediaPart, err := parts.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	if got := mediaPart.Header.Get("Content-Type"); got != JSONMime {
		t.Fatalf("media type %q", got)
	}
	media, err := io.ReadAll(mediaPart)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(media, payload) {
		t.Fatalf("media %q", media)
	}
	if _, err := parts.NextPart(); err != io.EOF {
		t.Fatalf("extra part %v", err)
	}
}

type apiCall struct {
	method      string
	path        string
	rawQuery    string
	host        string
	contentType string
	body        []byte
	remain      time.Duration
	queryFn     func(string) string
}

func (c apiCall) query(key string) string {
	if c.queryFn == nil {
		return ""
	}
	return c.queryFn(key)
}

type recorder struct {
	mu           sync.Mutex
	calls        []apiCall
	deadlines    map[string]time.Duration
	getStatus    int
	getBody      string
	createStatus int
	createBody   string
	updateStatus int
	updateBody   string
	uploads      []uploadReply
	uploadN      int
}

type uploadReply struct {
	status int
	body   string
}

func (r *recorder) noteDeadline(path string, remain time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.deadlines == nil {
		r.deadlines = map[string]time.Duration{}
	}
	r.deadlines[path] = remain
}

func (r *recorder) deadlineFor(path string) time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.deadlines[path]
}

func (r *recorder) reset() {
	r.mu.Lock()
	r.calls = nil
	r.uploadN = 0
	r.mu.Unlock()
}

func (r *recorder) takeUpload() (int, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.uploads) == 0 {
		body := r.createBody
		if body == "" {
			body = `{}`
		}
		return statusOr(r.createStatus, http.StatusOK), body
	}
	i := r.uploadN
	if i >= len(r.uploads) {
		i = len(r.uploads) - 1
	} else {
		r.uploadN++
	}
	rep := r.uploads[i]
	body := rep.body
	if body == "" {
		body = `{}`
	}
	return statusOr(rep.status, http.StatusOK), body
}

func (r *recorder) snapshot() []apiCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]apiCall, len(r.calls))
	copy(out, r.calls)
	return out
}

func (r *recorder) count(method, path string) int {
	n := 0
	for _, call := range r.snapshot() {
		if call.method == method && call.path == path {
			n++
		}
	}
	return n
}

func (r *recorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	q := req.URL.Query()
	call := apiCall{
		method:      req.Method,
		path:        req.URL.Path,
		rawQuery:    req.URL.RawQuery,
		host:        req.Host,
		contentType: req.Header.Get("Content-Type"),
		body:        body,
		remain:      r.deadlineFor(req.URL.Path),
		queryFn:     q.Get,
	}
	r.mu.Lock()
	r.calls = append(r.calls, call)
	r.mu.Unlock()

	status, payload := http.StatusOK, `{}`
	switch {
	case req.Method == http.MethodGet && strings.HasPrefix(req.URL.Path, "/files/"):
		status, payload = statusOr(r.getStatus, http.StatusOK), r.getBody
	case req.Method == http.MethodPost && req.URL.Path == "/files":
		status, payload = statusOr(r.createStatus, http.StatusOK), r.createBody
	case req.Method == http.MethodPost && req.URL.Path == "/upload/drive/v3/files":
		status, payload = r.takeUpload()
	case req.Method == http.MethodPost && strings.Contains(req.URL.Path, ":batchUpdate"):
		status, payload = statusOr(r.updateStatus, http.StatusOK), r.updateBody
	default:
		status, payload = http.StatusInternalServerError, `{"error":{"code":500,"message":"unexpected"}}`
	}
	if payload == "" {
		payload = `{}`
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, payload)
}

func newServices(t *testing.T, h http.Handler) (*drive.Service, *docsapi.Service) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	ctx := context.Background()
	src := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "test-access-token"})
	client := srv.Client()
	if rec, ok := h.(*recorder); ok {
		base := client.Transport
		if base == nil {
			base = http.DefaultTransport
		}
		client.Transport = deadlineTransport{base: base, rec: rec}
	}
	opts := []option.ClientOption{
		option.WithHTTPClient(client),
		option.WithEndpoint(srv.URL),
	}
	driveSvc, err := NewDriveService(ctx, src, opts...)
	if err != nil {
		t.Fatal(err)
	}
	docsSvc, err := NewDocsService(ctx, src, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return driveSvc, docsSvc
}

func statusOr(status, fallback int) int {
	if status == 0 {
		return fallback
	}
	return status
}

func folderJSON(id, mime string, trashed bool) string {
	return fmt.Sprintf(`{"id":%q,"mimeType":%q,"name":"Folder","trashed":%t}`, id, mime, trashed)
}

func fileJSON(id, parent string) string {
	return fmt.Sprintf(`{"id":%q,"parents":[%q]}`, id, parent)
}

func assertCreateBody(t *testing.T, body []byte, title, folderID string) {
	t.Helper()
	var got struct {
		Name     string   `json:"name"`
		MimeType string   `json:"mimeType"`
		Parents  []string `json:"parents"`
		DriveID  string   `json:"driveId"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("body %s: %v", body, err)
	}
	if got.Name != title || got.MimeType != GoogleDocMime || len(got.Parents) != 1 || got.Parents[0] != folderID {
		t.Fatalf("create body %s", body)
	}
	if got.DriveID != "" || bytesContains(body, "documents.create") {
		t.Fatalf("create body %s", body)
	}
}

func assertInsert(t *testing.T, body []byte, text string) {
	t.Helper()
	var got struct {
		Requests []struct {
			InsertText *struct {
				Text     string `json:"text"`
				Location struct {
					Index int64 `json:"index"`
				} `json:"location"`
			} `json:"insertText"`
		} `json:"requests"`
		WriteControl any `json:"writeControl"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("insert %s: %v", body, err)
	}
	if got.WriteControl != nil || len(got.Requests) != 1 || got.Requests[0].InsertText == nil {
		t.Fatalf("requests %s", body)
	}
	ins := got.Requests[0].InsertText
	if ins.Text != text || ins.Location.Index != 1 {
		t.Fatalf("insert %s", body)
	}
	if bytesContains(body, "endOfSegmentLocation") || bytesContains(body, "ForceSendFields") {
		t.Fatalf("insert %s", body)
	}
}

func assertNoDocumentsCreate(t *testing.T, calls []apiCall) {
	t.Helper()
	for _, call := range calls {
		if call.method == http.MethodPost && (call.path == "/v1/documents" || strings.HasSuffix(call.path, "/documents")) {
			t.Fatalf("documents.create %s", call.path)
		}
	}
}

type deadlineTransport struct {
	base http.RoundTripper
	rec  *recorder
}

func (t deadlineTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	remain := time.Duration(-1)
	if deadline, ok := req.Context().Deadline(); ok {
		remain = time.Until(deadline)
	}
	t.rec.noteDeadline(req.URL.Path, remain)
	return t.base.RoundTrip(req)
}

func deadlineBad(remain time.Duration) bool {
	if remain <= 0 {
		return true
	}
	return remain > rpcTimeout+2*time.Second
}

func bytesContains(b []byte, sub string) bool {
	return strings.Contains(string(b), sub)
}
