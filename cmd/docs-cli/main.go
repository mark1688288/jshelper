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
	"strings"

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
  create [--title TITLE] [--text TEXT] [--folder FOLDER]
  auth
  setup
  parse --html PATH
  gemini-key

create:
  --title TITLE    document title; omitted means DOCS_DEFAULT_TITLE
  --text TEXT      document body; if omitted, read stdin when it is not a terminal;
                   if omitted and stdin is a terminal, the body is empty
  --folder FOLDER  Drive folder id or folder URL for this run; overrides GOOGLE_DRIVE_FOLDER_ID

parse:
  --html PATH      saved JobsDB HTML file

gemini-key stores the Gemini API key in the keyring. The key is not a flag.

Run from the repository root, the directory that contains .env.example.
The release binary defaults to production. APP_ENV in the environment overrides it.
`

const (
	errTitleEmpty  = "docs-cli: title must not be empty"
	errBodyTooBig  = "docs-cli: body exceeds 1 MiB (1048576 bytes)"
	errNeedFolder  = "docs-cli: GOOGLE_DRIVE_FOLDER_ID is required"
	errKeyringWarn = "docs-cli: warning: could not update keyring"

	errHTMLTooLarge      = "docs-cli: html exceeds 2 MiB"
	errJobAdDetails      = "docs-cli: jobAdDetails not found"
	errGeminiKeyMissing  = "docs-cli: gemini api key is not in the keyring"
	errGeminiKeyTerminal = "docs-cli: gemini api key must be entered in a terminal"
	errGeminiStored      = "docs-cli: stored gemini api key in keyring"
	errGeminiRejected    = "docs-cli: gemini mapping rejected"

	// geminiKeyPrompt is the ReadSecret prompt for gemini-key and for parse
	// when the keyring item is missing. The typed key is not echoed.
	geminiKeyPrompt = "Gemini API key（輸入時不顯示）:"

	// maxHTMLBytes is 2 MiB. parse refuses a larger file before it builds a document.
	maxHTMLBytes = 2 << 20 // 2097152
)

var errPageTooLarge = errors.New(errHTMLTooLarge)

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
	// GeminiClient is the HTTP client for section mapping.
	// Nil uses the default client inside MapSections. Tests pass a client that does not call Gemini.
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
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprint(d.Stderr, usageText)
		return 2
	}
	titleSet, textSet, folderSet := false, false, false
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "title":
			titleSet = true
		case "text":
			textSet = true
		case "folder":
			folderSet = true
		}
	})

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
	if err := d.writeCredentials(cfg); err != nil {
		fmt.Fprintln(d.Stderr, err.Error())
		return 1
	}
	fmt.Fprintln(d.Stderr, "docs-cli: wrote credentials.json")
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

func runParse(args []string, d deps) int {
	fs := newFlagSet("parse", d.Stderr)
	htmlFlag := fs.String("html", "", "saved JobsDB HTML file")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprint(d.Stderr, usageText)
		return 2
	}
	htmlSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "html" {
			htmlSet = true
		}
	})
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
		fmt.Fprintln(d.Stderr, errGeminiRejected)
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

func writeDocument(w io.Writer, doc jobad.Document) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
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
