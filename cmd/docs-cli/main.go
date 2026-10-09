package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/zalando/go-keyring"
	"golang.org/x/oauth2"
	"golang.org/x/term"

	"github.com/mark1688288/jshelper/internal/config"
	"github.com/mark1688288/jshelper/internal/docs"
	"github.com/mark1688288/jshelper/internal/jobad"
	"github.com/mark1688288/jshelper/internal/oauth"
)

// Help exits 2. This program does not follow the GNU convention of exit 0.
const usageText = `usage: docs-cli <command> [flags]

commands:
  create [--title TITLE] [--text TEXT] [--folder FOLDER] [--import PATH]
  auth
  setup
  parse --html PATH [--autoparse]
  parse --txt PATH
  parse --batch
  gemini-key
  analyze --json PATH
  analyze --batch
  draft --json PATH --template PATH --cv PATH [--pdf NAME]
  upload [--folder FOLDER] PATH [PATH ...]

create:
  --title TITLE    document title; omitted means DOCS_DEFAULT_TITLE
  --text TEXT      document body; if omitted, read stdin when it is not a terminal;
                   if omitted and stdin is a terminal, the body is empty
  --folder FOLDER  Drive folder id or folder URL for this run; overrides GOOGLE_DRIVE_FOLDER_ID
  --import PATH    PDF to convert into the Google Doc; omit to use --text or stdin

parse:
  --html PATH      saved job HTML file
  --txt PATH       job details text file
  --autoparse      send the saved HTML to Gemini
  --batch          parse every .txt file in ./batch/jobads

gemini-key stores the Gemini API key in the keyring. The key is not a flag.

analyze:
  --json PATH      section JSON from parse
  --batch          analyze every .json file in ./batch/jobads/parsed

draft:
  --json PATH      section JSON from parse
  --template PATH  cover-letter template PDF
  --cv PATH        CV or resume PDF
  --pdf NAME       PDF file name in drafts; omitted means a timestamped .txt file

upload:
  PATH             one or more .json files
  --folder FOLDER  Drive folder id or folder URL for this run; overrides GOOGLE_DRIVE_FOLDER_ID

Run from the repository root, the directory that contains .env.example.
The release binary defaults to production. APP_ENV in the environment overrides it.
`

const (
	errTitleEmpty  = "docs-cli: title must not be empty"
	errBodyTooBig  = "docs-cli: body exceeds 1 MiB (1048576 bytes)"
	errNeedFolder  = "docs-cli: GOOGLE_DRIVE_FOLDER_ID is required"
	errKeyringWarn = "docs-cli: warning: could not update keyring"

	errHTMLTooLarge      = "docs-cli: html exceeds 2 MiB"
	errTxtTooLarge       = "docs-cli: txt exceeds 2 MiB"
	errNoBatchTxt        = "docs-cli: no txt files: batch/jobads"
	errNoBatchJSON       = "docs-cli: no json files: batch/jobads/parsed"
	errParsedDirMissing  = "docs-cli: parsed directory is missing: batch/jobads/parsed"
	errJSONTooLarge      = "docs-cli: json exceeds 1 MiB"
	errJobAdDetails      = "docs-cli: jobAdDetails not found"
	errGeminiKeyMissing  = "docs-cli: gemini api key is not in the keyring"
	errGeminiKeyTerminal = "docs-cli: gemini api key must be entered in a terminal"
	errGeminiStored      = "docs-cli: stored gemini api key in keyring"

	// geminiKeyPrompt is the ReadSecret prompt for gemini-key and for parse
	// when the keyring item is missing. The typed key is not echoed.
	geminiKeyPrompt = "Gemini API key（輸入時不顯示）:"

	// maxHTMLBytes is 2 MiB. parse refuses a larger file before it builds a document.
	maxHTMLBytes = 2 << 20 // 2097152
	// maxJSONBytes is 1 MiB. analyze refuses a larger file before it calls fm.
	maxJSONBytes = 1 << 20 // 1048576
)

var errPageTooLarge = errors.New(errHTMLTooLarge)
var errJSONTooBig = errors.New(errJSONTooLarge)

func main() {
	os.Exit(run(os.Args[1:], realDeps()))
}

type deps struct {
	Stdin               io.Reader
	Stdout              io.Writer
	Stderr              io.Writer
	Environ             []string
	Root                string
	TTY                 config.TTY
	OpenTTY             func() (config.TTY, error)
	StdinIsTerminal     bool
	Load                func(root string, environ []string) (*config.Config, error)
	EnsureProductionEnv func(root string, environ []string, tty config.TTY) (*config.Config, error)
	RequireClient       func(*config.Config) error
	WriteCredentials    func(*config.Config) error
	Authorize           func(context.Context, *config.Config, oauth.Options) (*oauth2.Token, error)
	CreateDocument      func(ctx context.Context, title, body, folderID string, insertIndex int64) (string, error)
	ImportPDF           func(ctx context.Context, title, folderID string, pdf []byte) (string, error)
	UploadJSON          func(ctx context.Context, folderID string, files []docs.JSONFile) ([]string, error)
	// GeminiClient is the HTTP client for section mapping, autoparse, and analyze
	// when fm fails. Nil uses the default client. Tests pass a client that does not call Gemini.
	GeminiClient *http.Client
}

func realDeps() deps {
	return deps{
		Stdin:           os.Stdin,
		Stdout:          os.Stdout,
		Stderr:          os.Stderr,
		Environ:         os.Environ(),
		StdinIsTerminal: term.IsTerminal(int(os.Stdin.Fd())),
		OpenTTY:         config.OpenTTY,
	}
}

func run(args []string, d deps) int {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprint(d.Stderr, usageText)
		return 2
	}
	switch args[0] {
	case "create":
		return runCreate(args[1:], d)
	case "auth":
		return runAuth(args[1:], d)
	case "setup":
		return runSetup(args[1:], d)
	case "parse":
		return runParse(args[1:], d)
	case "gemini-key":
		return runGeminiKey(args[1:], d)
	case "analyze":
		return runAnalyze(args[1:], d)
	case "draft":
		return runDraft(args[1:], d)
	case "upload":
		return runUpload(args[1:], d)
	default:
		fmt.Fprint(d.Stderr, usageText)
		return 2
	}
}

func runCreate(args []string, d deps) int {
	fs := newFlagSet("create", d.Stderr)
	titleFlag := fs.String("title", "", "document title; omitted means DOCS_DEFAULT_TITLE")
	textFlag := fs.String("text", "", "document body; if omitted, read stdin when it is not a terminal")
	folderFlag := fs.String("folder", "", "Drive folder id or folder URL for this run; overrides GOOGLE_DRIVE_FOLDER_ID")
	importFlag := fs.String("import", "", "PDF to convert into the Google Doc; omit to use --text or stdin")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprint(d.Stderr, usageText)
		return 2
	}
	titleSet, textSet, folderSet, importSet := false, false, false, false
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "title":
			titleSet = true
		case "text":
			textSet = true
		case "folder":
			folderSet = true
		case "import":
			importSet = true
		}
	})
	if importSet {
		return runCreateImport(d, titleSet, textSet, folderSet, *titleFlag, *folderFlag, *importFlag)
	}

	body, code := readCreateBody(d, textSet, *textFlag)
	if code != 0 {
		return code
	}
	var title string
	if titleSet {
		title = strings.TrimSpace(*titleFlag)
		if title == "" {
			fmt.Fprintln(d.Stderr, errTitleEmpty)
			return 1
		}
	}
	var folderFromFlag string
	if folderSet {
		id, err := config.NormalizeFolderID(*folderFlag)
		if err != nil {
			fmt.Fprintln(d.Stderr, err.Error())
			return 1
		}
		folderFromFlag = id
	}

	root, err := commandRoot(d)
	if err != nil {
		fmt.Fprintf(d.Stderr, "docs-cli: error: %s\n", err.Error())
		return 1
	}
	cfg, ok := loadConfigured(d, root)
	if !ok {
		return 1
	}
	folderID := folderFromFlag
	if !folderSet {
		if cfg.DriveFolderID == "" {
			fmt.Fprintln(d.Stderr, errNeedFolder)
			return 1
		}
		folderID = cfg.DriveFolderID
	}
	if !titleSet {
		title = cfg.DefaultTitle
	}
	if err := d.requireClient(cfg); err != nil {
		fmt.Fprintln(d.Stderr, err.Error())
		return 1
	}
	tok, err := d.authorize(context.Background(), cfg, oauth.Options{Force: false, Stderr: d.Stderr})
	if err != nil {
		fmt.Fprintln(d.Stderr, err.Error())
		return 1
	}
	fmt.Fprintf(d.Stderr, "docs-cli: body_bytes=%d\n", len(body))
	id, warn, err := d.createDocument(context.Background(), cfg, tok, title, body, folderID)
	if err != nil {
		fmt.Fprintln(d.Stderr, err.Error())
		if warn {
			fmt.Fprintln(d.Stderr, errKeyringWarn)
		}
		return 1
	}
	if warn {
		fmt.Fprintln(d.Stderr, errKeyringWarn)
	}
	fmt.Fprintf(d.Stderr, "docs-cli: created document %s\n", id)
	fmt.Fprintf(d.Stdout, "%s\n%s\n", id, docs.EditURL(id))
	return 0
}

func runCreateImport(d deps, titleSet, textSet, folderSet bool, titleFlag, folderFlag, path string) int {
	if path == "" || textSet {
		fmt.Fprint(d.Stderr, usageText)
		return 2
	}
	if !strings.EqualFold(filepath.Ext(path), ".pdf") {
		fmt.Fprintf(d.Stderr, "docs-cli: not a pdf file: %s\n", path)
		return 1
	}
	var title string
	if titleSet {
		title = strings.TrimSpace(titleFlag)
		if title == "" {
			fmt.Fprintln(d.Stderr, errTitleEmpty)
			return 1
		}
	}
	var folderFromFlag string
	if folderSet {
		id, err := config.NormalizeFolderID(folderFlag)
		if err != nil {
			fmt.Fprintln(d.Stderr, err.Error())
			return 1
		}
		folderFromFlag = id
	}
	pdf, err := readImportPDF(path)
	if err != nil {
		fmt.Fprintln(d.Stderr, err.Error())
		return 1
	}

	root, err := commandRoot(d)
	if err != nil {
		fmt.Fprintf(d.Stderr, "docs-cli: error: %s\n", err.Error())
		return 1
	}
	cfg, ok := loadConfigured(d, root)
	if !ok {
		return 1
	}
	folderID := folderFromFlag
	if !folderSet {
		if cfg.DriveFolderID == "" {
			fmt.Fprintln(d.Stderr, errNeedFolder)
			return 1
		}
		folderID = cfg.DriveFolderID
	}
	if !titleSet {
		title = cfg.DefaultTitle
	}
	if err := d.requireClient(cfg); err != nil {
		fmt.Fprintln(d.Stderr, err.Error())
		return 1
	}
	tok, err := d.authorize(context.Background(), cfg, oauth.Options{Force: false, Stderr: d.Stderr})
	if err != nil {
		fmt.Fprintln(d.Stderr, err.Error())
		return 1
	}
	id, warn, err := d.importPDF(context.Background(), cfg, tok, title, folderID, pdf)
	if err != nil {
		fmt.Fprintln(d.Stderr, err.Error())
		if warn {
			fmt.Fprintln(d.Stderr, errKeyringWarn)
		}
		return 1
	}
	if warn {
		fmt.Fprintln(d.Stderr, errKeyringWarn)
	}
	fmt.Fprintf(d.Stderr, "docs-cli: imported %s bytes=%d\n", path, len(pdf))
	fmt.Fprintf(d.Stderr, "docs-cli: created document %s\n", id)
	fmt.Fprintf(d.Stdout, "%s\n%s\n", id, docs.EditURL(id))
	return 0
}

func readImportPDF(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("docs-cli: error: %s", err.Error())
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("docs-cli: not a regular file: %s", path)
	}
	if info.Size() > docs.MaxBodyBytes {
		return nil, fmt.Errorf("docs-cli: pdf exceeds 1 MiB: %s", path)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("docs-cli: error: %s", err.Error())
	}
	if len(body) > docs.MaxBodyBytes {
		return nil, fmt.Errorf("docs-cli: pdf exceeds 1 MiB: %s", path)
	}
	if !bytes.HasPrefix(body, []byte("%PDF-")) {
		return nil, fmt.Errorf("docs-cli: pdf is not valid: %s", path)
	}
	return body, nil
}

func runUpload(args []string, d deps) int {
	fs := newFlagSet("upload", d.Stderr)
	folderFlag := fs.String("folder", "", "Drive folder id or folder URL for this run; overrides GOOGLE_DRIVE_FOLDER_ID")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() == 0 {
		fmt.Fprint(d.Stderr, usageText)
		return 2
	}
	paths := fs.Args()
	for _, path := range paths {
		if !strings.EqualFold(filepath.Ext(path), ".json") {
			fmt.Fprintf(d.Stderr, "docs-cli: not a json file: %s\n", path)
			return 1
		}
	}
	folderSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "folder" {
			folderSet = true
		}
	})
	var folderFromFlag string
	if folderSet {
		id, err := config.NormalizeFolderID(*folderFlag)
		if err != nil {
			fmt.Fprintln(d.Stderr, err.Error())
			return 1
		}
		folderFromFlag = id
	}
	files := make([]docs.JSONFile, len(paths))
	for i, path := range paths {
		body, err := readUploadJSON(path)
		if err != nil {
			fmt.Fprintln(d.Stderr, err.Error())
			return 1
		}
		files[i] = docs.JSONFile{Name: filepath.Base(path), Body: body}
	}

	root, err := commandRoot(d)
	if err != nil {
		fmt.Fprintf(d.Stderr, "docs-cli: error: %s\n", err.Error())
		return 1
	}
	cfg, ok := loadConfigured(d, root)
	if !ok {
		return 1
	}
	folderID := folderFromFlag
	if !folderSet {
		if cfg.DriveFolderID == "" {
			fmt.Fprintln(d.Stderr, errNeedFolder)
			return 1
		}
		folderID = cfg.DriveFolderID
	}
	if err := d.requireClient(cfg); err != nil {
		fmt.Fprintln(d.Stderr, err.Error())
		return 1
	}
	tok, err := d.authorize(context.Background(), cfg, oauth.Options{Force: false, Stderr: d.Stderr})
	if err != nil {
		fmt.Fprintln(d.Stderr, err.Error())
		return 1
	}
	ids, warn, err := d.uploadJSON(context.Background(), cfg, tok, folderID, files)
	for i, id := range ids {
		fmt.Fprintf(d.Stderr, "docs-cli: uploaded %s bytes=%d\n", paths[i], len(files[i].Body))
		fmt.Fprintf(d.Stdout, "%s\n%s\n%s\n", paths[i], id, docs.FileURL(id))
	}
	if err != nil {
		fmt.Fprintln(d.Stderr, err.Error())
		if warn {
			fmt.Fprintln(d.Stderr, errKeyringWarn)
		}
		return 1
	}
	if warn {
		fmt.Fprintln(d.Stderr, errKeyringWarn)
	}
	return 0
}

func readUploadJSON(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("docs-cli: error: %s", err.Error())
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("docs-cli: not a regular file: %s", path)
	}
	if info.Size() > maxJSONBytes {
		return nil, fmt.Errorf("docs-cli: json exceeds 1 MiB: %s", path)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("docs-cli: error: %s", err.Error())
	}
	if len(body) > maxJSONBytes {
		return nil, fmt.Errorf("docs-cli: json exceeds 1 MiB: %s", path)
	}
	if !json.Valid(body) {
		return nil, fmt.Errorf("docs-cli: json is not valid: %s", path)
	}
	return body, nil
}

func readCreateBody(d deps, textSet bool, text string) (string, int) {
	if textSet {
		if len(text) > docs.MaxBodyBytes {
			fmt.Fprintln(d.Stderr, errBodyTooBig)
			return "", 1
		}
		return text, 0
	}
	if d.StdinIsTerminal {
		return "", 0
	}
	if d.Stdin == nil {
		fmt.Fprintln(d.Stderr, "docs-cli: error: stdin is nil")
		return "", 1
	}
	buf, err := io.ReadAll(io.LimitReader(d.Stdin, int64(docs.MaxBodyBytes)+1))
	if err != nil {
		fmt.Fprintf(d.Stderr, "docs-cli: error: %s\n", err.Error())
		return "", 1
	}
	if len(buf) > docs.MaxBodyBytes {
		fmt.Fprintln(d.Stderr, errBodyTooBig)
		return "", 1
	}
	return string(buf), 0
}

func runAuth(args []string, d deps) int {
	fs := newFlagSet("auth", d.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprint(d.Stderr, usageText)
		return 2
	}
	root, err := commandRoot(d)
	if err != nil {
		fmt.Fprintf(d.Stderr, "docs-cli: error: %s\n", err.Error())
		return 1
	}
	cfg, ok := loadConfigured(d, root)
	if !ok {
		return 1
	}
	if err := d.requireClient(cfg); err != nil {
		fmt.Fprintln(d.Stderr, err.Error())
		return 1
	}
	if err := d.writeCredentials(cfg); err != nil {
		fmt.Fprintln(d.Stderr, err.Error())
		return 1
	}
	fmt.Fprintln(d.Stderr, "docs-cli: wrote credentials.json")
	if _, err := d.authorize(context.Background(), cfg, oauth.Options{Force: true, Stderr: d.Stderr}); err != nil {
		fmt.Fprintln(d.Stderr, err.Error())
		return 1
	}
	return 0
}

func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, usageText)
	}
	return fs
}

func runSetup(args []string, d deps) int {
	fs := newFlagSet("setup", d.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprint(d.Stderr, usageText)
		return 2
	}
	root, err := commandRoot(d)
	if err != nil {
		fmt.Fprintf(d.Stderr, "docs-cli: error: %s\n", err.Error())
		return 1
	}
	tty, closeTTY := openCommandTTY(d)
	defer closeTTY()
	existed := envExisted(root)
	cfg, err := d.ensureProduction(root, d.Environ, tty)
	noteEnvCopy(d, root, existed)
	if errors.Is(err, config.ErrDesktopClientRequired) {
		fmt.Fprintln(d.Stderr, err.Error())
		return 0
	}
	if err != nil {
		fmt.Fprintln(d.Stderr, err.Error())
		return 1
	}
	fmt.Fprintf(d.Stderr, "docs-cli: mode=%s file=%s\n", cfg.Mode, filepath.Base(cfg.FilePath))
	if err := d.requireClient(cfg); err != nil {
		fmt.Fprintln(d.Stderr, err.Error())
		return 1
	}
	if err := d.writeCredentials(cfg); err != nil {
		fmt.Fprintln(d.Stderr, err.Error())
		return 1
	}
	fmt.Fprintln(d.Stderr, "docs-cli: wrote credentials.json")
	return 0
}

func loadConfigured(d deps, root string) (*config.Config, bool) {
	mode, err := config.ResolveMode(d.Environ)
	if err != nil {
		fmt.Fprintln(d.Stderr, err.Error())
		return nil, false
	}
	existed := envExisted(root)
	var cfg *config.Config
	if mode == "production" {
		tty, closeTTY := openCommandTTY(d)
		defer closeTTY()
		cfg, err = d.ensureProduction(root, d.Environ, tty)
		noteEnvCopy(d, root, existed)
	} else {
		cfg, err = d.loadEnv(root, d.Environ)
	}
	if err != nil {
		fmt.Fprintln(d.Stderr, err.Error())
		return nil, false
	}
	fmt.Fprintf(d.Stderr, "docs-cli: mode=%s file=%s\n", cfg.Mode, filepath.Base(cfg.FilePath))
	return cfg, true
}

func envExisted(root string) bool {
	_, err := os.Lstat(filepath.Join(root, ".env"))
	return err == nil
}

func noteEnvCopy(d deps, root string, existed bool) {
	if !existed && envExisted(root) {
		fmt.Fprintln(d.Stderr, "docs-cli: copied .env.example to .env")
	}
}

func (d deps) loadEnv(root string, environ []string) (*config.Config, error) {
	if d.Load != nil {
		return d.Load(root, environ)
	}
	return config.Load(root, environ)
}

func (d deps) ensureProduction(root string, environ []string, tty config.TTY) (*config.Config, error) {
	if d.EnsureProductionEnv != nil {
		return d.EnsureProductionEnv(root, environ, tty)
	}
	return config.EnsureProductionEnv(root, environ, tty)
}

func (d deps) requireClient(cfg *config.Config) error {
	if d.RequireClient != nil {
		return d.RequireClient(cfg)
	}
	return config.RequireClient(cfg)
}

func (d deps) writeCredentials(cfg *config.Config) error {
	if d.WriteCredentials != nil {
		return d.WriteCredentials(cfg)
	}
	return config.WriteCredentials(cfg)
}

func (d deps) authorize(ctx context.Context, cfg *config.Config, opt oauth.Options) (*oauth2.Token, error) {
	if d.Authorize != nil {
		return d.Authorize(ctx, cfg, opt)
	}
	return oauth.Authorize(ctx, cfg, opt)
}

func (d deps) createDocument(ctx context.Context, cfg *config.Config, tok *oauth2.Token, title, body, folderID string) (string, bool, error) {
	if d.CreateDocument != nil {
		id, err := d.CreateDocument(ctx, title, body, folderID, cfg.InsertIndex)
		return id, false, err
	}
	if tok == nil {
		return "", false, errors.New("docs-cli: error: missing token")
	}
	src := oauth.TokenSource(ctx, cfg, tok)
	if _, err := src.Token(); err != nil {
		return "", false, oauth.UserTokenError(err, cfg.ClientSecret, tok.RefreshToken, tok.AccessToken)
	}
	driveSvc, err := docs.NewDriveService(ctx, src)
	if err != nil {
		return "", false, fmt.Errorf("docs-cli: error: %s", err.Error())
	}
	docsSvc, err := docs.NewDocsService(ctx, src)
	if err != nil {
		return "", false, fmt.Errorf("docs-cli: error: %s", err.Error())
	}
	id, err := docs.CreateDocument(ctx, driveSvc, docsSvc, title, body, folderID, cfg.InsertIndex)
	warn := false
	if rotErr := oauth.SyncRefreshToken(cfg, src); rotErr != nil {
		warn = true
	}
	return id, warn, err
}

func (d deps) importPDF(ctx context.Context, cfg *config.Config, tok *oauth2.Token, title, folderID string, pdf []byte) (string, bool, error) {
	if d.ImportPDF != nil {
		id, err := d.ImportPDF(ctx, title, folderID, pdf)
		return id, false, err
	}
	if tok == nil {
		return "", false, errors.New("docs-cli: error: missing token")
	}
	src := oauth.TokenSource(ctx, cfg, tok)
	if _, err := src.Token(); err != nil {
		return "", false, oauth.UserTokenError(err, cfg.ClientSecret, tok.RefreshToken, tok.AccessToken)
	}
	driveSvc, err := docs.NewDriveService(ctx, src)
	if err != nil {
		return "", false, fmt.Errorf("docs-cli: error: %s", err.Error())
	}
	id, err := docs.ImportPDF(ctx, driveSvc, title, folderID, pdf)
	warn := false
	if rotErr := oauth.SyncRefreshToken(cfg, src); rotErr != nil {
		warn = true
	}
	return id, warn, err
}

func (d deps) uploadJSON(ctx context.Context, cfg *config.Config, tok *oauth2.Token, folderID string, files []docs.JSONFile) ([]string, bool, error) {
	if d.UploadJSON != nil {
		ids, err := d.UploadJSON(ctx, folderID, files)
		return ids, false, err
	}
	if tok == nil {
		return nil, false, errors.New("docs-cli: error: missing token")
	}
	src := oauth.TokenSource(ctx, cfg, tok)
	if _, err := src.Token(); err != nil {
		return nil, false, oauth.UserTokenError(err, cfg.ClientSecret, tok.RefreshToken, tok.AccessToken)
	}
	driveSvc, err := docs.NewDriveService(ctx, src)
	if err != nil {
		return nil, false, fmt.Errorf("docs-cli: error: %s", err.Error())
	}
	ids, err := docs.UploadJSON(ctx, driveSvc, folderID, files)
	warn := false
	if rotErr := oauth.SyncRefreshToken(cfg, src); rotErr != nil {
		warn = true
	}
	return ids, warn, err
}

func runParse(args []string, d deps) int {
	fs := newFlagSet("parse", d.Stderr)
	htmlFlag := fs.String("html", "", "saved job HTML file")
	txtFlag := fs.String("txt", "", "job details text file")
	auto := fs.Bool("autoparse", false, "send the saved HTML to Gemini")
	batch := fs.Bool("batch", false, "parse every .txt file in ./batch/jobads")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprint(d.Stderr, usageText)
		return 2
	}
	htmlSet, txtSet, autoSet, batchSet := false, false, false, false
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "html":
			htmlSet = true
		case "txt":
			txtSet = true
		case "autoparse":
			autoSet = true
		case "batch":
			batchSet = true
		}
	})
	// --batch cannot be combined with --html, --txt, or --autoparse.
	if batchSet && (htmlSet || txtSet || autoSet) {
		fmt.Fprint(d.Stderr, usageText)
		return 2
	}
	if batchSet && *batch {
		return runParseBatch(d)
	}
	// --txt always uses autoparse. It cannot be combined with --html or --autoparse.
	if txtSet && (htmlSet || autoSet || *txtFlag == "") {
		fmt.Fprint(d.Stderr, usageText)
		return 2
	}
	if txtSet {
		page, err := readHTMLFile(*txtFlag)
		if errors.Is(err, errPageTooLarge) {
			fmt.Fprintln(d.Stderr, errTxtTooLarge)
			return 1
		}
		if err != nil {
			fmt.Fprintf(d.Stderr, "docs-cli: error: %s\n", err.Error())
			return 1
		}
		return runAutoparse(d, *txtFlag, page)
	}
	if !htmlSet || *htmlFlag == "" {
		fmt.Fprint(d.Stderr, usageText)
		return 2
	}

	page, err := readHTMLFile(*htmlFlag)
	if errors.Is(err, errPageTooLarge) {
		fmt.Fprintln(d.Stderr, errHTMLTooLarge)
		return 1
	}
	if err != nil {
		fmt.Fprintf(d.Stderr, "docs-cli: error: %s\n", err.Error())
		return 1
	}
	if *auto {
		return runAutoparse(d, *htmlFlag, page)
	}
	doc, err := jobad.Parse(page)
	if errors.Is(err, jobad.ErrJobAdDetailsNotFound) {
		fmt.Fprintln(d.Stderr, errJobAdDetails)
		return 1
	}
	if err != nil {
		fmt.Fprintf(d.Stderr, "docs-cli: error: %s\n", err.Error())
		return 1
	}
	doc.Source = *htmlFlag
	doc = applyGemini(context.Background(), d, page, doc)
	if err := writeDocument(d.Stdout, doc); err != nil {
		fmt.Fprintf(d.Stderr, "docs-cli: error: %s\n", err.Error())
		return 1
	}
	return 0
}

func runParseBatch(d deps) int {
	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(d.Stderr, "docs-cli: error: %s\n", err.Error())
		return 1
	}
	jobads := filepath.Join(wd, "batch", "jobads")
	parsed := filepath.Join(jobads, "parsed")
	if err := ensureBatchDirs(wd); err != nil {
		fmt.Fprintf(d.Stderr, "docs-cli: error: %s\n", err.Error())
		return 1
	}
	entries, err := os.ReadDir(jobads)
	if err != nil {
		fmt.Fprintf(d.Stderr, "docs-cli: error: %s\n", err.Error())
		return 1
	}
	var names []string
	for _, ent := range entries {
		if strings.EqualFold(filepath.Ext(ent.Name()), ".txt") {
			names = append(names, ent.Name())
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		fmt.Fprintln(d.Stderr, errNoBatchTxt)
		return 1
	}

	type batchFile struct {
		name    string
		src     string
		rel     string
		jsonDst string
		txtDst  string
		txtRel  string
	}
	files := make([]batchFile, 0, len(names))
	for _, name := range names {
		rel := filepath.Join("batch", "jobads", name)
		src := filepath.Join(jobads, name)
		info, err := os.Lstat(src)
		if err != nil {
			fmt.Fprintf(d.Stderr, "docs-cli: error: %s\n", err.Error())
			return 1
		}
		if !info.Mode().IsRegular() {
			fmt.Fprintf(d.Stderr, "docs-cli: not a regular file: %s\n", rel)
			return 1
		}
		if info.Size() > maxHTMLBytes {
			fmt.Fprintf(d.Stderr, "docs-cli: txt exceeds 2 MiB: %s\n", rel)
			return 1
		}
		ext := filepath.Ext(name)
		stem := strings.TrimSuffix(name, ext)
		jsonName := stem + ".json"
		jsonRel := filepath.Join("batch", "jobads", "parsed", jsonName)
		txtRel := filepath.Join("batch", "jobads", "parsed", name)
		jsonDst := filepath.Join(parsed, jsonName)
		txtDst := filepath.Join(parsed, name)
		if code := rejectParsedDest(d, jsonDst, jsonRel); code != 0 {
			return code
		}
		if code := rejectParsedDest(d, txtDst, txtRel); code != 0 {
			return code
		}
		files = append(files, batchFile{
			name:    name,
			src:     src,
			rel:     rel,
			jsonDst: jsonDst,
			txtDst:  txtDst,
			txtRel:  txtRel,
		})
	}

	if code := prepareAutoparseKey(d); code != 0 {
		return code
	}
	for _, file := range files {
		page, err := readHTMLFile(file.src)
		if errors.Is(err, errPageTooLarge) {
			fmt.Fprintf(d.Stderr, "docs-cli: txt exceeds 2 MiB: %s\n", file.rel)
			return 1
		}
		if err != nil {
			fmt.Fprintf(d.Stderr, "docs-cli: error: %s\n", err.Error())
			return 1
		}
		doc, err := jobad.Autoparse(context.Background(), page, d.GeminiClient)
		if err != nil {
			if errors.Is(err, keyring.ErrNotFound) {
				fmt.Fprintln(d.Stderr, errGeminiKeyMissing)
			} else {
				fmt.Fprintln(d.Stderr, err.Error())
			}
			return 1
		}
		doc.Source = filepath.Join("batch", "jobads", file.name)
		abs, err := filepath.Abs(file.jsonDst)
		if err != nil {
			fmt.Fprintf(d.Stderr, "docs-cli: error: %s\n", err.Error())
			return 1
		}
		if err := writeExclusiveJSON(file.jsonDst, doc); err != nil {
			fmt.Fprintf(d.Stderr, "docs-cli: error: %s\n", err.Error())
			return 1
		}
		if code := rejectParsedDest(d, file.txtDst, file.txtRel); code != 0 {
			_ = os.Remove(file.jsonDst)
			return code
		}
		if err := os.Rename(file.src, file.txtDst); err != nil {
			_ = os.Remove(file.jsonDst)
			fmt.Fprintf(d.Stderr, "docs-cli: error: %s\n", err.Error())
			return 1
		}
		fmt.Fprintln(d.Stdout, abs)
	}
	return 0
}

// ensureBatchDirs creates batch, batch/jobads, and batch/jobads/parsed
// at mode 0700 when they are missing. An existing directory keeps its mode.
func ensureBatchDirs(wd string) error {
	rels := []string{
		"batch",
		filepath.Join("batch", "jobads"),
		filepath.Join("batch", "jobads", "parsed"),
	}
	var created []string
	for _, rel := range rels {
		path := filepath.Join(wd, rel)
		info, err := os.Lstat(path)
		if err == nil {
			if !info.IsDir() {
				break
			}
			continue
		}
		if !os.IsNotExist(err) {
			return err
		}
		created = append(created, path)
	}
	if err := os.MkdirAll(filepath.Join(wd, "batch", "jobads", "parsed"), 0o700); err != nil {
		return err
	}
	for _, path := range created {
		if err := os.Chmod(path, 0o700); err != nil {
			return err
		}
	}
	return nil
}

func rejectParsedDest(d deps, path, rel string) int {
	_, err := os.Lstat(path)
	if err == nil {
		fmt.Fprintf(d.Stderr, "docs-cli: parsed file already exists: %s\n", rel)
		return 1
	}
	if !os.IsNotExist(err) {
		fmt.Fprintf(d.Stderr, "docs-cli: error: %s\n", err.Error())
		return 1
	}
	return 0
}

func writeExclusiveJSON(path string, v any) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	werr := writeIndentedJSON(f, v)
	cerr := f.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(path)
		if werr != nil {
			return werr
		}
		return cerr
	}
	return nil
}

func runAutoparse(d deps, source string, page []byte) int {
	if code := prepareAutoparseKey(d); code != 0 {
		return code
	}
	doc, err := jobad.Autoparse(context.Background(), page, d.GeminiClient)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			fmt.Fprintln(d.Stderr, errGeminiKeyMissing)
		} else {
			fmt.Fprintln(d.Stderr, err.Error())
		}
		return 1
	}
	doc.Source = source
	if err := writeDocument(d.Stdout, doc); err != nil {
		fmt.Fprintf(d.Stderr, "docs-cli: error: %s\n", err.Error())
		return 1
	}
	return 0
}

func prepareAutoparseKey(d deps) int {
	_, err := jobad.LoadGeminiKey()
	switch {
	case errors.Is(err, keyring.ErrNotFound):
		tty, closeTTY := terminalOrNil(d)
		defer closeTTY()
		if tty == nil {
			fmt.Fprintln(d.Stderr, errGeminiKeyMissing)
			return 1
		}
		secret, rerr := tty.ReadSecret(geminiKeyPrompt)
		if rerr != nil {
			fmt.Fprintf(d.Stderr, "docs-cli: error: %s\n", rerr.Error())
			return 1
		}
		if secret == "" {
			fmt.Fprintln(d.Stderr, errGeminiKeyMissing)
			return 1
		}
		if serr := jobad.StoreGeminiKey(secret); serr != nil {
			fmt.Fprintln(d.Stderr, serr.Error())
			return 1
		}
		fmt.Fprintln(d.Stderr, errGeminiStored)
		return 0
	case err != nil:
		fmt.Fprintln(d.Stderr, err.Error())
		return 1
	default:
		return 0
	}
}

func readHTMLFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf, err := io.ReadAll(io.LimitReader(f, int64(maxHTMLBytes)+1))
	if err != nil {
		return nil, err
	}
	if len(buf) > maxHTMLBytes {
		return nil, errPageTooLarge
	}
	return buf, nil
}

func applyGemini(ctx context.Context, d deps, page []byte, doc jobad.Document) jobad.Document {
	_, err := jobad.LoadGeminiKey()
	switch {
	case errors.Is(err, keyring.ErrNotFound):
		tty, closeTTY := terminalOrNil(d)
		defer closeTTY()
		if tty == nil {
			fmt.Fprintln(d.Stderr, errGeminiKeyMissing)
			return doc
		}
		secret, rerr := tty.ReadSecret(geminiKeyPrompt)
		if rerr != nil {
			fmt.Fprintf(d.Stderr, "docs-cli: error: %s\n", rerr.Error())
			return doc
		}
		if secret == "" {
			fmt.Fprintln(d.Stderr, errGeminiKeyMissing)
			return doc
		}
		if serr := jobad.StoreGeminiKey(secret); serr != nil {
			fmt.Fprintln(d.Stderr, serr.Error())
			return doc
		}
		fmt.Fprintln(d.Stderr, errGeminiStored)
	case err != nil:
		fmt.Fprintln(d.Stderr, err.Error())
		return doc
	}

	blocks, err := jobad.CollectBlocks(page)
	if err != nil {
		fmt.Fprintln(d.Stderr, err.Error())
		return doc
	}
	if len(blocks) == 0 {
		return doc
	}
	mapped, err := d.mapSections(ctx, doc, blocks)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			fmt.Fprintln(d.Stderr, errGeminiKeyMissing)
		} else {
			fmt.Fprintln(d.Stderr, err.Error())
		}
		return doc
	}
	return mapped
}

func runGeminiKey(args []string, d deps) int {
	fs := newFlagSet("gemini-key", d.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprint(d.Stderr, usageText)
		return 2
	}
	tty, closeTTY := terminalOrNil(d)
	defer closeTTY()
	if tty == nil {
		fmt.Fprintln(d.Stderr, errGeminiKeyTerminal)
		return 1
	}
	secret, err := tty.ReadSecret(geminiKeyPrompt)
	if err != nil {
		fmt.Fprintf(d.Stderr, "docs-cli: error: %s\n", err.Error())
		return 1
	}
	if secret == "" {
		return 0
	}
	if err := jobad.StoreGeminiKey(secret); err != nil {
		fmt.Fprintln(d.Stderr, err.Error())
		return 1
	}
	fmt.Fprintln(d.Stderr, errGeminiStored)
	return 0
}

func runAnalyze(args []string, d deps) int {
	fs := newFlagSet("analyze", d.Stderr)
	jsonFlag := fs.String("json", "", "section JSON from parse")
	batch := fs.Bool("batch", false, "analyze every .json file in ./batch/jobads/parsed")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprint(d.Stderr, usageText)
		return 2
	}
	jsonSet, batchSet := false, false
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "json":
			jsonSet = true
		case "batch":
			batchSet = true
		}
	})
	// --batch cannot be combined with --json. --batch=false is not the batch path.
	if batchSet && jsonSet {
		fmt.Fprint(d.Stderr, usageText)
		return 2
	}
	if batchSet && *batch {
		return runAnalyzeBatch(d)
	}
	if !jsonSet || *jsonFlag == "" {
		fmt.Fprint(d.Stderr, usageText)
		return 2
	}

	raw, err := readLimitedFile(*jsonFlag, maxJSONBytes)
	if errors.Is(err, errJSONTooBig) {
		fmt.Fprintln(d.Stderr, errJSONTooLarge)
		return 1
	}
	if err != nil {
		fmt.Fprintf(d.Stderr, "docs-cli: error: %s\n", err.Error())
		return 1
	}
	var doc jobad.Document
	if err := json.Unmarshal(raw, &doc); err != nil {
		fmt.Fprintf(d.Stderr, "docs-cli: error: %s\n", err.Error())
		return 1
	}
	base := filepath.Base(*jsonFlag)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	if stem == "" {
		fmt.Fprintf(d.Stderr, "docs-cli: not a json file: %s\n", *jsonFlag)
		return 1
	}
	diagramPath := filepath.Join(filepath.Dir(*jsonFlag), stem+"-tech.md")
	distPath := filepath.Join(filepath.Dir(*jsonFlag), stem+"-tech-dist.md")
	if code := rejectTechDiagram(d, diagramPath); code != 0 {
		return code
	}
	if code := rejectTechDistribution(d, distPath); code != 0 {
		return code
	}
	analysis, err := jobad.Analyze(doc, d.GeminiClient)
	if err != nil {
		fmt.Fprintln(d.Stderr, err.Error())
		return 1
	}
	diagram, err := jobad.TechDiagram(analysis)
	if err != nil {
		fmt.Fprintln(d.Stderr, err.Error())
		return 1
	}
	dist, err := jobad.TechDistribution(stem, analysis)
	if err != nil {
		fmt.Fprintln(d.Stderr, err.Error())
		return 1
	}
	if err := writeTechDiagram(diagramPath, diagram); err != nil {
		if errors.Is(err, os.ErrExist) {
			fmt.Fprintf(d.Stderr, "docs-cli: tech diagram already exists: %s\n", diagramPath)
			return 1
		}
		fmt.Fprintf(d.Stderr, "docs-cli: error: %s\n", err.Error())
		return 1
	}
	if err := writeTechDistribution(distPath, dist); err != nil {
		_ = os.Remove(diagramPath)
		if errors.Is(err, os.ErrExist) {
			fmt.Fprintf(d.Stderr, "docs-cli: tech distribution already exists: %s\n", distPath)
			return 1
		}
		fmt.Fprintf(d.Stderr, "docs-cli: error: %s\n", err.Error())
		return 1
	}
	if err := writeIndentedJSON(d.Stdout, analysis); err != nil {
		fmt.Fprintf(d.Stderr, "docs-cli: error: %s\n", err.Error())
		return 1
	}
	return 0
}

// runAnalyzeBatch analyzes each one-level .json file in ./batch/jobads/parsed.
// parsed must already exist. analyzed is created at mode 0700 when it is missing.
// An existing analyzed directory keeps its mode. A file or symlink is not replaced.
func runAnalyzeBatch(d deps) int {
	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(d.Stderr, "docs-cli: error: %s\n", err.Error())
		return 1
	}
	parsedRel := filepath.Join("batch", "jobads", "parsed")
	analyzedRel := filepath.Join("batch", "jobads", "analyzed")
	parsed := filepath.Join(wd, parsedRel)
	analyzed := filepath.Join(wd, analyzedRel)

	info, err := os.Lstat(parsed)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintln(d.Stderr, errParsedDirMissing)
			return 1
		}
		fmt.Fprintf(d.Stderr, "docs-cli: error: %s\n", err.Error())
		return 1
	}
	if !info.IsDir() {
		fmt.Fprintf(d.Stderr, "docs-cli: not a directory: %s\n", parsedRel)
		return 1
	}

	ainfo, err := os.Lstat(analyzed)
	switch {
	case os.IsNotExist(err):
		// Only this directory. Do not create batch, jobads, or parsed.
		if err := os.Mkdir(analyzed, 0o700); err != nil {
			fmt.Fprintf(d.Stderr, "docs-cli: error: %s\n", err.Error())
			return 1
		}
		if err := os.Chmod(analyzed, 0o700); err != nil {
			fmt.Fprintf(d.Stderr, "docs-cli: error: %s\n", err.Error())
			return 1
		}
	case err != nil:
		fmt.Fprintf(d.Stderr, "docs-cli: error: %s\n", err.Error())
		return 1
	case !ainfo.IsDir():
		fmt.Fprintf(d.Stderr, "docs-cli: not a directory: %s\n", analyzedRel)
		return 1
	}

	entries, err := os.ReadDir(parsed)
	if err != nil {
		fmt.Fprintf(d.Stderr, "docs-cli: error: %s\n", err.Error())
		return 1
	}
	var names []string
	for _, ent := range entries {
		if strings.EqualFold(filepath.Ext(ent.Name()), ".json") {
			names = append(names, ent.Name())
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		fmt.Fprintln(d.Stderr, errNoBatchJSON)
		return 1
	}

	type analyzeFile struct {
		name        string
		src         string
		rel         string
		analysisDst string
		jsonDst     string
		jsonRel     string
		diagramDst  string
		diagramRel  string
		distDst     string
		distRel     string
	}
	files := make([]analyzeFile, 0, len(names))
	for _, name := range names {
		rel := filepath.Join(parsedRel, name)
		src := filepath.Join(parsed, name)
		info, err := os.Lstat(src)
		if err != nil {
			fmt.Fprintf(d.Stderr, "docs-cli: error: %s\n", err.Error())
			return 1
		}
		if !info.Mode().IsRegular() {
			fmt.Fprintf(d.Stderr, "docs-cli: not a regular file: %s\n", rel)
			return 1
		}
		if info.Size() > maxJSONBytes {
			fmt.Fprintf(d.Stderr, "docs-cli: json exceeds 1 MiB: %s\n", rel)
			return 1
		}
		ext := filepath.Ext(name)
		stem := strings.TrimSuffix(name, ext)
		if stem == "" {
			fmt.Fprintf(d.Stderr, "docs-cli: not a json file: %s\n", rel)
			return 1
		}
		analysisName := stem + "-analysis.json"
		analysisRel := filepath.Join(analyzedRel, analysisName)
		jsonRel := filepath.Join(analyzedRel, name)
		diagramName := stem + "-tech.md"
		diagramRel := filepath.Join(analyzedRel, diagramName)
		distName := stem + "-tech-dist.md"
		distRel := filepath.Join(analyzedRel, distName)
		analysisDst := filepath.Join(analyzed, analysisName)
		jsonDst := filepath.Join(analyzed, name)
		diagramDst := filepath.Join(analyzed, diagramName)
		distDst := filepath.Join(analyzed, distName)
		if code := rejectAnalyzedDest(d, analysisDst, analysisRel); code != 0 {
			return code
		}
		if code := rejectAnalyzedDest(d, jsonDst, jsonRel); code != 0 {
			return code
		}
		if code := rejectAnalyzedDest(d, diagramDst, diagramRel); code != 0 {
			return code
		}
		if code := rejectAnalyzedDest(d, distDst, distRel); code != 0 {
			return code
		}
		files = append(files, analyzeFile{
			name:        name,
			src:         src,
			rel:         rel,
			analysisDst: analysisDst,
			jsonDst:     jsonDst,
			jsonRel:     jsonRel,
			diagramDst:  diagramDst,
			diagramRel:  diagramRel,
			distDst:     distDst,
			distRel:     distRel,
		})
	}

	for _, file := range files {
		raw, err := readLimitedFile(file.src, maxJSONBytes)
		if errors.Is(err, errJSONTooBig) {
			fmt.Fprintf(d.Stderr, "docs-cli: json exceeds 1 MiB: %s\n", file.rel)
			return 1
		}
		if err != nil {
			fmt.Fprintf(d.Stderr, "docs-cli: error: %s\n", err.Error())
			return 1
		}
		var doc jobad.Document
		if err := json.Unmarshal(raw, &doc); err != nil {
			fmt.Fprintf(d.Stderr, "docs-cli: error: %s\n", err.Error())
			return 1
		}
		analysis, err := jobad.Analyze(doc, d.GeminiClient)
		if err != nil {
			fmt.Fprintln(d.Stderr, err.Error())
			return 1
		}
		diagram, err := jobad.TechDiagram(analysis)
		if err != nil {
			fmt.Fprintln(d.Stderr, err.Error())
			return 1
		}
		stem := strings.TrimSuffix(file.name, filepath.Ext(file.name))
		dist, err := jobad.TechDistribution(stem, analysis)
		if err != nil {
			fmt.Fprintln(d.Stderr, err.Error())
			return 1
		}
		abs, err := filepath.Abs(file.analysisDst)
		if err != nil {
			fmt.Fprintf(d.Stderr, "docs-cli: error: %s\n", err.Error())
			return 1
		}
		diagramAbs, err := filepath.Abs(file.diagramDst)
		if err != nil {
			fmt.Fprintf(d.Stderr, "docs-cli: error: %s\n", err.Error())
			return 1
		}
		distAbs, err := filepath.Abs(file.distDst)
		if err != nil {
			fmt.Fprintf(d.Stderr, "docs-cli: error: %s\n", err.Error())
			return 1
		}
		if err := writeExclusiveJSON(file.analysisDst, analysis); err != nil {
			fmt.Fprintf(d.Stderr, "docs-cli: error: %s\n", err.Error())
			return 1
		}
		if err := writeTechDiagram(file.diagramDst, diagram); err != nil {
			_ = os.Remove(file.analysisDst)
			if errors.Is(err, os.ErrExist) {
				fmt.Fprintf(d.Stderr, "docs-cli: analyzed file already exists: %s\n", file.diagramRel)
				return 1
			}
			fmt.Fprintf(d.Stderr, "docs-cli: error: %s\n", err.Error())
			return 1
		}
		if err := writeTechDistribution(file.distDst, dist); err != nil {
			_ = os.Remove(file.analysisDst)
			_ = os.Remove(file.diagramDst)
			if errors.Is(err, os.ErrExist) {
				fmt.Fprintf(d.Stderr, "docs-cli: analyzed file already exists: %s\n", file.distRel)
				return 1
			}
			fmt.Fprintf(d.Stderr, "docs-cli: error: %s\n", err.Error())
			return 1
		}
		if code := rejectAnalyzedDest(d, file.jsonDst, file.jsonRel); code != 0 {
			_ = os.Remove(file.analysisDst)
			_ = os.Remove(file.diagramDst)
			_ = os.Remove(file.distDst)
			return code
		}
		if err := os.Rename(file.src, file.jsonDst); err != nil {
			_ = os.Remove(file.analysisDst)
			_ = os.Remove(file.diagramDst)
			_ = os.Remove(file.distDst)
			fmt.Fprintf(d.Stderr, "docs-cli: error: %s\n", err.Error())
			return 1
		}
		fmt.Fprintln(d.Stdout, abs)
		fmt.Fprintln(d.Stdout, diagramAbs)
		fmt.Fprintln(d.Stdout, distAbs)
	}
	return 0
}

func rejectAnalyzedDest(d deps, path, rel string) int {
	_, err := os.Lstat(path)
	if err == nil {
		fmt.Fprintf(d.Stderr, "docs-cli: analyzed file already exists: %s\n", rel)
		return 1
	}
	if !os.IsNotExist(err) {
		fmt.Fprintf(d.Stderr, "docs-cli: error: %s\n", err.Error())
		return 1
	}
	return 0
}

func rejectTechDiagram(d deps, path string) int {
	_, err := os.Lstat(path)
	if err == nil {
		fmt.Fprintf(d.Stderr, "docs-cli: tech diagram already exists: %s\n", path)
		return 1
	}
	if !os.IsNotExist(err) {
		fmt.Fprintf(d.Stderr, "docs-cli: error: %s\n", err.Error())
		return 1
	}
	return 0
}

func rejectTechDistribution(d deps, path string) int {
	_, err := os.Lstat(path)
	if err == nil {
		fmt.Fprintf(d.Stderr, "docs-cli: tech distribution already exists: %s\n", path)
		return 1
	}
	if !os.IsNotExist(err) {
		fmt.Fprintf(d.Stderr, "docs-cli: error: %s\n", err.Error())
		return 1
	}
	return 0
}

// techDiagramWrite overrides the relationship-diagram file write in tests.
// Nil uses writeExclusiveFile. Tests restore nil.
var techDiagramWrite func(path string, body []byte) error

func writeTechDiagram(path string, body []byte) error {
	if techDiagramWrite != nil {
		return techDiagramWrite(path, body)
	}
	return writeExclusiveFile(path, body)
}

// techDistWrite overrides the distribution file write in tests.
// Nil uses writeExclusiveFile. Tests restore nil.
var techDistWrite func(path string, body []byte) error

func writeTechDistribution(path string, body []byte) error {
	if techDistWrite != nil {
		return techDistWrite(path, body)
	}
	return writeExclusiveFile(path, body)
}

func writeExclusiveFile(path string, body []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(body)
	cerr := f.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(path)
		if werr != nil {
			return werr
		}
		return cerr
	}
	return nil
}

// errDraftsNotDirectory is returned when ./drafts exists and is not a directory.
var errDraftsNotDirectory = errors.New("docs-cli: drafts is not a directory")

// draftRootForTest is the working directory injected by tests.
// Empty uses os.Getwd, so drafts is ./drafts.
var draftRootForTest string

// draftClock is the local clock used for draft file names.
var draftClock = time.Now

// SetDraftRootForTest injects the directory whose child is drafts.
// An empty dir restores the working directory. Tests restore the previous value.
func SetDraftRootForTest(dir string) {
	draftRootForTest = dir
}

func runDraft(args []string, d deps) int {
	fs := newFlagSet("draft", d.Stderr)
	jsonFlag := fs.String("json", "", "section JSON from parse")
	templateFlag := fs.String("template", "", "cover-letter template PDF")
	cvFlag := fs.String("cv", "", "CV or resume PDF")
	pdfFlag := fs.String("pdf", "", "PDF file name in drafts; omitted means a timestamped .txt file")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprint(d.Stderr, usageText)
		return 2
	}
	jsonSet, templateSet, cvSet, pdfSet := false, false, false, false
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "json":
			jsonSet = true
		case "template":
			templateSet = true
		case "cv":
			cvSet = true
		case "pdf":
			pdfSet = true
		}
	})
	if !jsonSet || *jsonFlag == "" || !templateSet || *templateFlag == "" || !cvSet || *cvFlag == "" || (pdfSet && *pdfFlag == "") {
		fmt.Fprint(d.Stderr, usageText)
		return 2
	}
	if pdfSet && !validPDFName(*pdfFlag) {
		fmt.Fprintf(d.Stderr, "docs-cli: not a pdf file: %s\n", *pdfFlag)
		return 1
	}

	// A valid draft checks ./drafts in the working directory before it reads JSON.
	dir, err := ensureDraftsDir()
	if errors.Is(err, errDraftsNotDirectory) {
		fmt.Fprintln(d.Stderr, err.Error())
		return 1
	}
	if err != nil {
		fmt.Fprintf(d.Stderr, "docs-cli: error: %s\n", err.Error())
		return 1
	}
	if pdfSet {
		_, err := os.Lstat(filepath.Join(dir, *pdfFlag))
		if err == nil {
			fmt.Fprintf(d.Stderr, "docs-cli: pdf already exists: %s\n", *pdfFlag)
			return 1
		}
		if !os.IsNotExist(err) {
			fmt.Fprintf(d.Stderr, "docs-cli: error: %s\n", err.Error())
			return 1
		}
	}

	raw, err := readLimitedFile(*jsonFlag, maxJSONBytes)
	if errors.Is(err, errJSONTooBig) {
		fmt.Fprintln(d.Stderr, errJSONTooLarge)
		return 1
	}
	if err != nil {
		fmt.Fprintf(d.Stderr, "docs-cli: error: %s\n", err.Error())
		return 1
	}
	var doc jobad.Document
	if err := json.Unmarshal(raw, &doc); err != nil {
		fmt.Fprintf(d.Stderr, "docs-cli: error: %s: %s\n", *jsonFlag, err.Error())
		return 1
	}
	if err := jobad.CheckDraftRequirements(doc); err != nil {
		fmt.Fprintln(d.Stderr, err.Error())
		return 1
	}
	templateText, err := jobad.ExtractTemplateText(*templateFlag)
	if err != nil {
		if errors.Is(err, jobad.ErrTemplateTooLarge) || errors.Is(err, jobad.ErrTemplateNoText) {
			fmt.Fprintln(d.Stderr, err.Error())
			return 1
		}
		fmt.Fprintf(d.Stderr, "docs-cli: error: %s\n", err.Error())
		return 1
	}
	cvText, err := jobad.ExtractTemplateText(*cvFlag)
	if err != nil {
		if errors.Is(err, jobad.ErrTemplateTooLarge) {
			fmt.Fprintln(d.Stderr, jobad.ErrCVTooLarge.Error())
			return 1
		}
		if errors.Is(err, jobad.ErrTemplateNoText) {
			fmt.Fprintln(d.Stderr, jobad.ErrCVNoText.Error())
			return 1
		}
		fmt.Fprintf(d.Stderr, "docs-cli: error: %s\n", err.Error())
		return 1
	}
	letter, err := jobad.Draft(doc, templateText, cvText)
	if err != nil {
		fmt.Fprintln(d.Stderr, err.Error())
		return 1
	}
	var path string
	if pdfSet {
		path, err = writeDraftPDF(dir, *pdfFlag, letter)
	} else {
		path, err = writeDraftLetter(dir, letter)
	}
	if err != nil {
		var exists *pdfExistsError
		if errors.Is(err, errPDFNotEncodable) || errors.As(err, &exists) {
			fmt.Fprintln(d.Stderr, err.Error())
			return 1
		}
		fmt.Fprintf(d.Stderr, "docs-cli: error: %s\n", err.Error())
		return 1
	}
	fmt.Fprintln(d.Stdout, path)
	return 0
}

func draftParentDir() (string, error) {
	if draftRootForTest != "" {
		return draftRootForTest, nil
	}
	return os.Getwd()
}

func ensureDraftsDir() (string, error) {
	root, err := draftParentDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, "drafts")
	info, err := os.Stat(dir)
	if err == nil {
		if !info.IsDir() {
			return "", errDraftsNotDirectory
		}
		return dir, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// writeDraftLetter stores the letter in the working directory's drafts folder.
// The name is the local timestamp. An existing name uses -2 through -100 with O_EXCL.
// A letter that already ends in a newline is stored unchanged.
func writeDraftLetter(dir, letter string) (string, error) {
	if !strings.HasSuffix(letter, "\n") {
		letter += "\n"
	}
	stamp := draftClock().Local().Format("20060102T150405.000")
	var lastErr error
	for n := 1; n <= 100; n++ {
		name := stamp + ".txt"
		if n > 1 {
			name = fmt.Sprintf("%s-%d.txt", stamp, n)
		}
		path := filepath.Join(dir, name)
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			lastErr = err
			continue
		}
		if err != nil {
			return "", err
		}
		_, werr := io.WriteString(f, letter)
		cerr := f.Close()
		if werr != nil || cerr != nil {
			_ = os.Remove(path)
			if werr != nil {
				return "", werr
			}
			return "", cerr
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			_ = os.Remove(path)
			return "", err
		}
		return abs, nil
	}
	if lastErr == nil {
		lastErr = errors.New("no free draft name")
	}
	return "", lastErr
}

func readLimitedFile(path string, limit int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(buf) > limit {
		return nil, errJSONTooBig
	}
	return buf, nil
}

func writeDocument(w io.Writer, doc jobad.Document) error {
	return writeIndentedJSON(w, doc)
}

func writeIndentedJSON(w io.Writer, v any) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return err
	}
	_, err := w.Write(buf.Bytes())
	return err
}

func (d deps) mapSections(ctx context.Context, doc jobad.Document, blocks []jobad.Block) (jobad.Document, error) {
	return jobad.MapSections(ctx, doc, blocks, d.GeminiClient)
}

func terminalOrNil(d deps) (config.TTY, func()) {
	tty, closeTTY := openCommandTTY(d)
	if tty == nil || !tty.IsTerminal() {
		closeTTY()
		return nil, func() {}
	}
	return tty, closeTTY
}

func commandRoot(d deps) (string, error) {
	if d.Root != "" {
		return d.Root, nil
	}
	return os.Getwd()
}

func openCommandTTY(d deps) (config.TTY, func()) {
	if d.TTY != nil {
		return d.TTY, func() {}
	}
	if d.OpenTTY == nil {
		return nil, func() {}
	}
	opened, err := d.OpenTTY()
	if err != nil {
		return nil, func() {}
	}
	return opened, func() {
		if c, ok := opened.(io.Closer); ok {
			_ = c.Close()
		}
	}
}
