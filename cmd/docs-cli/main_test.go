package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ledongthuc/pdf"
	"github.com/zalando/go-keyring"
	"golang.org/x/oauth2"

	"github.com/mark1688288/jshelper/internal/config"
	"github.com/mark1688288/jshelper/internal/docs"
	"github.com/mark1688288/jshelper/internal/jobad"
	"github.com/mark1688288/jshelper/internal/oauth"
)

func TestMain(m *testing.M) {
	config.SetProjectBootstrapForTest(func(string, []string, io.Writer) error {
		return errors.New("docs-cli: bootstrap not stubbed")
	})
	keyring.MockInit()
	jobad.SetFMExecForTest(func([]string) ([]byte, []byte, int) {
		return nil, []byte("fm exec not stubbed"), 1
	})
	dir, err := os.MkdirTemp("", "docs-cli-draft-root-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "docs-cli: draft root: %v\n", err)
		os.Exit(1)
	}
	testDraftRoot = dir
	SetDraftRootForTest(testDraftRoot)
	code := m.Run()
	SetDraftRootForTest("")
	jobad.SetFMExecForTest(nil)
	keyring.MockRestore()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

var testDraftRoot string

func restoreDraftRoot() {
	SetDraftRootForTest(testDraftRoot)
}

func TestRun(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	dir := t.TempDir()
	t.Chdir(dir)

	tests := []struct {
		name   string
		args   []string
		code   int
		stderr string
		exact  bool
	}{
		{name: "no command", args: nil, code: 2, stderr: usageText, exact: true},
		{name: "long help", args: []string{"--help"}, code: 2, stderr: usageText, exact: true},
		{name: "short help", args: []string{"-h"}, code: 2, stderr: usageText, exact: true},
		{name: "create help", args: []string{"create", "--help"}, code: 2, stderr: usageText, exact: true},
		{name: "create short help", args: []string{"create", "-h"}, code: 2, stderr: usageText, exact: true},
		{name: "auth help", args: []string{"auth", "--help"}, code: 2, stderr: usageText, exact: true},
		{name: "setup help", args: []string{"setup", "-h"}, code: 2, stderr: usageText, exact: true},
		{name: "unknown command", args: []string{"frob"}, code: 2, stderr: "usage: docs-cli"},
		{name: "unknown top-level flag", args: []string{"--bogus"}, code: 2, stderr: "usage: docs-cli"},
		{name: "unknown create flag", args: []string{"create", "--bogus"}, code: 2, stderr: "usage: docs-cli"},
		{name: "missing title", args: []string{"create", "--title"}, code: 2, stderr: "usage: docs-cli"},
		{name: "missing text", args: []string{"create", "--text"}, code: 2, stderr: "usage: docs-cli"},
		{name: "missing folder", args: []string{"create", "--folder"}, code: 2, stderr: "usage: docs-cli"},
		{name: "missing import", args: []string{"create", "--import"}, code: 2, stderr: "usage: docs-cli"},
		{name: "extra create arg", args: []string{"create", "extra"}, code: 2, stderr: "usage: docs-cli"},
		{name: "extra arg after flags", args: []string{"create", "--title", "Demo", "extra"}, code: 2, stderr: "usage: docs-cli"},
		{name: "extra auth arg", args: []string{"auth", "extra"}, code: 2, stderr: "usage: docs-cli"},
		{name: "extra setup arg", args: []string{"setup", "extra"}, code: 2, stderr: "usage: docs-cli"},
		{name: "auth rejects title", args: []string{"auth", "--title", "Demo"}, code: 2, stderr: "usage: docs-cli"},
		{name: "auth rejects text", args: []string{"auth", "--text", "hello"}, code: 2, stderr: "usage: docs-cli"},
		{name: "auth rejects folder", args: []string{"auth", "--folder", "abc"}, code: 2, stderr: "usage: docs-cli"},
		{name: "setup rejects title", args: []string{"setup", "--title", "Demo"}, code: 2, stderr: "usage: docs-cli"},
		{name: "setup rejects folder", args: []string{"setup", "--folder", "abc"}, code: 2, stderr: "usage: docs-cli"},
		{name: "create", args: []string{"create"}, code: 1, stderr: "missing .env.example"},
		{name: "auth", args: []string{"auth"}, code: 1, stderr: "missing .env.example"},
		{name: "setup", args: []string{"setup"}, code: 1, stderr: "docs-cli: setup refused: development reads .env.example and does not copy or prompt"},
		{
			name:   "create with flags",
			args:   []string{"create", "--title", "Demo", "--text", "hello", "--folder", "abc"},
			code:   1,
			stderr: "not a Drive folder URL or id",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(tt.args, deps{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr})
			if code != tt.code {
				t.Fatalf("exit %d, want %d; stderr=%q", code, tt.code, stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
			got := stderr.String()
			if tt.exact {
				if got != tt.stderr {
					t.Fatalf("stderr = %q, want %q", got, tt.stderr)
				}
			} else if !strings.Contains(got, tt.stderr) {
				t.Fatalf("stderr = %q, want substring %q", got, tt.stderr)
			}
			if tt.code == 2 && !strings.Contains(got, "usage: docs-cli") {
				t.Fatalf("stderr = %q, want usage", got)
			}
			if strings.Contains(got, "listening") {
				t.Fatalf("stderr = %q, listened", got)
			}
			if _, err := os.Stat(filepath.Join(dir, ".env")); !os.IsNotExist(err) {
				t.Fatalf(".env created or stat failed: %v", err)
			}
		})
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := make([]string, len(entries))
		for i, entry := range entries {
			names[i] = entry.Name()
		}
		t.Fatalf("temp dir has files: %s", strings.Join(names, ", "))
	}
}

func TestSubcommandsLeaveEnvUntouched(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	original := []byte("APP_ENV=development\nSECRET=keep\n")
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	commands := [][]string{
		{"create"},
		{"auth"},
		{"setup"},
		{"create", "--title", "Demo", "--text", "hello", "--folder", "abc"},
	}
	for _, args := range commands {
		var stdout, stderr bytes.Buffer
		code := run(args, deps{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr})
		want := "missing .env.example"
		switch {
		case args[0] == "setup":
			want = "setup refused"
		case args[0] == "auth":
			want = "missing .env.example"
		case len(args) > 1:
			want = "not a Drive folder URL or id"
		}
		if code != 1 || !strings.Contains(stderr.String(), want) {
			t.Fatalf("%v: exit %d stderr %q", args, code, stderr.String())
		}
		if stdout.Len() != 0 {
			t.Fatalf("%v: stdout %q", args, stdout.String())
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, original) {
			t.Fatalf("%v: .env changed to %q", args, got)
		}
	}
}

func TestCreateBadFolderDoesNotCopyInProduction(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env.example"), []byte(exampleEnv()), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"create", "--title", "Demo", "--text", "hello", "--folder", "abc"}, deps{
		Stdin:   strings.NewReader(""),
		Stdout:  &stdout,
		Stderr:  &stderr,
		Environ: []string{"APP_ENV=production"},
		Root:    dir,
	})
	if code != 1 || !strings.Contains(stderr.String(), "not a Drive folder URL or id") {
		t.Fatalf("exit %d stderr %q", code, stderr.String())
	}
	if strings.Contains(stderr.String(), "listening") || stdout.Len() != 0 {
		t.Fatalf("stdout %q stderr %s", stdout.String(), stderr.String())
	}
	if _, err := os.Stat(filepath.Join(dir, ".env")); !os.IsNotExist(err) {
		t.Fatalf(".env: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "credentials.json")); !os.IsNotExist(err) {
		t.Fatalf("credentials: %v", err)
	}
}

func TestSetupProduction(t *testing.T) {
	t.Setenv("APP_ENV", "staging")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env.example"), []byte(exampleEnv()), 0o644); err != nil {
		t.Fatal(err)
	}
	var bootstrapCalls int
	config.SetProjectBootstrapForTest(func(string, []string, io.Writer) error {
		bootstrapCalls++
		return nil
	})
	t.Cleanup(func() {
		config.SetProjectBootstrapForTest(func(string, []string, io.Writer) error {
			return errors.New("docs-cli: bootstrap not stubbed")
		})
	})
	tty := &scriptTTY{
		lines: []string{
			"",
			"",
			"dev@example.com",
			"dev@example.com",
			"https://drive.google.com/drive/folders/" + folderID + "?usp=sharing",
		},
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"setup"}, deps{
		Stdin:   strings.NewReader(""),
		Stdout:  &stdout,
		Stderr:  &stderr,
		Environ: []string{"APP_ENV=production"},
		Root:    dir,
		TTY:     tty,
	})
	if code != 0 {
		t.Fatalf("exit %d stderr %s", code, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout %q", stdout.String())
	}
	text := stderr.String()
	if !strings.Contains(text, "Create the Desktop app \"Docs CLI Desktop\", then run ./docs-cli setup") || !strings.Contains(text, "docs-cli: copied .env.example to .env") {
		t.Fatalf("stderr %s", text)
	}
	if strings.Contains(text, "docs-cli: wrote credentials.json") || strings.Contains(text, "docs-cli: mode=") || strings.Contains(text, secretValue) {
		t.Fatalf("stderr %s", text)
	}
	if bootstrapCalls != 1 {
		t.Fatalf("bootstrap calls=%d", bootstrapCalls)
	}
	second := &scriptTTY{lines: []string{"abc.apps.googleusercontent.com"}, secrets: []string{secretValue}}
	stdout.Reset()
	stderr.Reset()
	code = run([]string{"setup"}, deps{
		Stdin:   strings.NewReader(""),
		Stdout:  &stdout,
		Stderr:  &stderr,
		Environ: []string{"APP_ENV=production"},
		Root:    dir,
		TTY:     second,
	})
	if code != 0 {
		t.Fatalf("second exit %d stderr %s", code, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout %q", stdout.String())
	}
	text = stderr.String()
	if !strings.Contains(text, "docs-cli: wrote credentials.json") || !strings.Contains(text, "docs-cli: mode=production file=.env") {
		t.Fatalf("stderr %s", text)
	}
	if strings.Contains(text, "Create the Desktop app") || strings.Contains(text, secretValue) || strings.Contains(second.buf.String(), secretValue) {
		t.Fatalf("stderr %s\ntty %s", text, second.buf.String())
	}
	if bootstrapCalls != 1 {
		t.Fatalf("bootstrap calls=%d", bootstrapCalls)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(dir, "credentials.json"))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("credentials perm=%o", info.Mode().Perm())
		}
		info, err = os.Stat(filepath.Join(dir, ".env"))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf(".env perm=%o", info.Mode().Perm())
		}
	}
}

func TestSetupPauseExitsZero(t *testing.T) {
	var creds, authz int
	var stdout, stderr bytes.Buffer
	code := run([]string{"setup"}, deps{
		Stdin:   strings.NewReader(""),
		Stdout:  &stdout,
		Stderr:  &stderr,
		Environ: []string{"APP_ENV=production"},
		Root:    t.TempDir(),
		EnsureProductionEnv: func(string, []string, config.TTY) (*config.Config, error) {
			return nil, config.ErrDesktopClientRequired
		},
		WriteCredentials: func(*config.Config) error {
			creds++
			return nil
		},
		Authorize: func(context.Context, *config.Config, oauth.Options) (*oauth2.Token, error) {
			authz++
			return nil, nil
		},
	})
	if code != 0 || stdout.Len() != 0 || creds != 0 || authz != 0 {
		t.Fatalf("exit %d creds %d auth %d stdout %q stderr %s", code, creds, authz, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), config.ErrDesktopClientRequired.Error()) {
		t.Fatalf("stderr %s", stderr.String())
	}
}

func TestAuthPauseDoesNotListen(t *testing.T) {
	assertPauseSkipsWork(t, []string{"auth"})
}

func TestCreatePauseDoesNotListen(t *testing.T) {
	assertPauseSkipsWork(t, []string{"create", "--title", "Demo", "--text", "hello"})
}

func assertPauseSkipsWork(t *testing.T, args []string) {
	t.Helper()
	var authz, created int
	var stdout, stderr bytes.Buffer
	code := run(args, deps{
		Stdin:   strings.NewReader(""),
		Stdout:  &stdout,
		Stderr:  &stderr,
		Environ: []string{"APP_ENV=production"},
		Root:    t.TempDir(),
		EnsureProductionEnv: func(string, []string, config.TTY) (*config.Config, error) {
			return nil, config.ErrDesktopClientRequired
		},
		Authorize: func(context.Context, *config.Config, oauth.Options) (*oauth2.Token, error) {
			authz++
			return nil, nil
		},
		CreateDocument: func(context.Context, string, string, string, int64) (string, error) {
			created++
			return "", nil
		},
	})
	if code != 1 || authz != 0 || created != 0 || strings.Contains(stderr.String(), "listening") {
		t.Fatalf("%v exit %d auth %d create %d stderr %s", args, code, authz, created, stderr.String())
	}
}

const folderID = "1AbC_def-234"
const secretValue = "super-secret-value"
const sentinelCredentials = "keep-these-credentials-bytes\n"

func exampleEnv() string {
	return `APP_ENV=development
GCP_PROJECT_ID=docs-cli-dev
GCP_PROJECT_NAME=Docs CLI
GCP_BILLING_ACCOUNT=
GCP_ORGANIZATION_ID=
GCP_FOLDER_ID=
GOOGLE_DOCS_API=docs.googleapis.com
GOOGLE_DRIVE_API=drive.googleapis.com
GOOGLE_OAUTH_APP_NAME=Docs CLI
GOOGLE_OAUTH_SUPPORT_EMAIL=you@gmail.com
GOOGLE_OAUTH_USER_TYPE=external
GOOGLE_OAUTH_PUBLISHING_STATUS=testing
GOOGLE_OAUTH_TEST_USER=you@gmail.com
GOOGLE_OAUTH_CLIENT_NAME=Docs CLI Desktop
GOOGLE_OAUTH_CLIENT_TYPE=installed
GOOGLE_OAUTH_CLIENT_ID=
GOOGLE_OAUTH_CLIENT_SECRET=
GOOGLE_OAUTH_AUTH_URI=https://accounts.google.com/o/oauth2/auth
GOOGLE_OAUTH_TOKEN_URI=https://oauth2.googleapis.com/token
GOOGLE_OAUTH_CERT_URL=https://www.googleapis.com/oauth2/v1/certs
GOOGLE_OAUTH_REDIRECT_URI=http://127.0.0.1
GOOGLE_OAUTH_SCOPE=https://www.googleapis.com/auth/drive
GOOGLE_OAUTH_ACCESS_TYPE=offline
GOOGLE_OAUTH_PROMPT=consent
GOOGLE_OAUTH_PKCE_METHOD=S256
OAUTH_TIMEOUT=3m
GOOGLE_CREDENTIALS_PATH=credentials.json
DOCS_INSERT_INDEX=1
DOCS_DEFAULT_TITLE=Untitled
GOOGLE_DRIVE_FOLDER_ID=
`
}

type scriptTTY struct {
	lines   []string
	secrets []string
	buf     bytes.Buffer
}

func (s *scriptTTY) IsTerminal() bool { return true }

func (s *scriptTTY) ReadLine(prompt string) (string, error) {
	s.buf.WriteString(prompt)
	s.buf.WriteByte('\n')
	if len(s.lines) == 0 {
		return "", fmt.Errorf("unexpected ReadLine %q", prompt)
	}
	value := s.lines[0]
	s.lines = s.lines[1:]
	return value, nil
}

func (s *scriptTTY) ReadSecret(prompt string) (string, error) {
	s.buf.WriteString(prompt)
	s.buf.WriteByte('\n')
	if len(s.secrets) == 0 {
		return "", fmt.Errorf("unexpected ReadSecret %q", prompt)
	}
	value := s.secrets[0]
	s.secrets = s.secrets[1:]
	return value, nil
}

func TestAuthDevelopmentEmptyClientDoesNotListen(t *testing.T) {
	dir := t.TempDir()
	env := strings.ReplaceAll(exampleEnv(), "you@gmail.com", "dev@example.com")
	if err := os.WriteFile(filepath.Join(dir, ".env.example"), []byte(env), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"auth"}, deps{
		Stdin:   strings.NewReader(""),
		Stdout:  &stdout,
		Stderr:  &stderr,
		Environ: []string{"APP_ENV=development"},
		Root:    dir,
		Authorize: func(context.Context, *config.Config, oauth.Options) (*oauth2.Token, error) {
			t.Fatal("Authorize was called")
			return nil, nil
		},
	})
	if code != 1 {
		t.Fatalf("exit %d stderr %s", code, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout %q", stdout.String())
	}
	text := stderr.String()
	if !strings.Contains(text, "docs-cli: missing GOOGLE_OAUTH_CLIENT_ID in .env.example") {
		t.Fatalf("stderr %s", text)
	}
	if strings.Contains(text, "listening") || strings.Contains(text, "not implemented") {
		t.Fatalf("stderr %s", text)
	}
	if _, err := os.Stat(filepath.Join(dir, ".env")); !os.IsNotExist(err) {
		t.Fatalf(".env: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "credentials.json")); !os.IsNotExist(err) {
		t.Fatalf("credentials: %v", err)
	}
}

func TestAuthProductionCopiesEnv(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env.example"), []byte(exampleEnv()), 0o644); err != nil {
		t.Fatal(err)
	}
	var bootstrapCalls int
	config.SetProjectBootstrapForTest(func(string, []string, io.Writer) error {
		bootstrapCalls++
		t.Errorf("bootstrap called")
		return errors.New("docs-cli: bootstrap not stubbed")
	})
	t.Cleanup(func() {
		config.SetProjectBootstrapForTest(func(string, []string, io.Writer) error {
			return errors.New("docs-cli: bootstrap not stubbed")
		})
	})
	var stdout, stderr bytes.Buffer
	code := run([]string{"auth"}, deps{
		Stdin:   strings.NewReader(""),
		Stdout:  &stdout,
		Stderr:  &stderr,
		Environ: []string{"APP_ENV=production"},
		Root:    dir,
	})
	if code != 1 {
		t.Fatalf("exit %d stderr %s", code, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout %q", stdout.String())
	}
	text := stderr.String()
	if !strings.Contains(text, "docs-cli: copied .env.example to .env") {
		t.Fatalf("stderr %s", text)
	}
	if !strings.Contains(text, "docs-cli: production env is incomplete; run docs-cli setup in a terminal") {
		t.Fatalf("stderr %s", text)
	}
	if bootstrapCalls != 0 {
		t.Fatalf("bootstrap calls=%d", bootstrapCalls)
	}
	if strings.Contains(text, "listening") || strings.Contains(text, "docs-cli: docs-cli:") {
		t.Fatalf("stderr %s", text)
	}
	if _, err := os.Stat(filepath.Join(dir, "credentials.json")); !os.IsNotExist(err) {
		t.Fatalf("credentials: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf(".env perm=%o", info.Mode().Perm())
	}
}

func TestAuthDevelopmentCallsAuthorize(t *testing.T) {
	dir := t.TempDir()
	env := strings.ReplaceAll(exampleEnv(), "you@gmail.com", "dev@example.com")
	env = strings.ReplaceAll(env, "GOOGLE_OAUTH_CLIENT_ID=\n", "GOOGLE_OAUTH_CLIENT_ID=abc.apps.googleusercontent.com\n")
	env = strings.ReplaceAll(env, "GOOGLE_OAUTH_CLIENT_SECRET=\n", "GOOGLE_OAUTH_CLIENT_SECRET="+secretValue+"\n")
	if err := os.WriteFile(filepath.Join(dir, ".env.example"), []byte(env), 0o644); err != nil {
		t.Fatal(err)
	}
	var calls int
	var stdout, stderr bytes.Buffer
	code := run([]string{"auth"}, deps{
		Stdin:   strings.NewReader(""),
		Stdout:  &stdout,
		Stderr:  &stderr,
		Environ: []string{"APP_ENV=development"},
		Root:    dir,
		Authorize: func(_ context.Context, cfg *config.Config, opt oauth.Options) (*oauth2.Token, error) {
			calls++
			if !opt.Force {
				t.Fatal("Force is false")
			}
			if cfg.ClientID != "abc.apps.googleusercontent.com" || cfg.ClientSecret != secretValue {
				t.Fatalf("client id=%q", cfg.ClientID)
			}
			if cfg.DriveFolderID != "" {
				t.Fatalf("folder %q", cfg.DriveFolderID)
			}
			fmt.Fprintln(opt.Stderr, "docs-cli: stored refresh token in keyring")
			return &oauth2.Token{AccessToken: "access-token-value"}, nil
		},
	})
	if code != 0 || calls != 1 {
		t.Fatalf("exit %d calls %d stderr %s", code, calls, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout %q", stdout.String())
	}
	text := stderr.String()
	if strings.Contains(text, secretValue) || strings.Contains(text, "listening") || strings.Contains(text, "docs-cli: docs-cli:") {
		t.Fatalf("stderr %s", text)
	}
	if !strings.Contains(text, "docs-cli: mode=development file=.env.example") || !strings.Contains(text, "docs-cli: wrote credentials.json") {
		t.Fatalf("stderr %s", text)
	}
	if _, err := os.Stat(filepath.Join(dir, ".env")); !os.IsNotExist(err) {
		t.Fatalf(".env: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "token.json")); !os.IsNotExist(err) {
		t.Fatalf("token.json: %v", err)
	}
	credPath := filepath.Join(dir, "credentials.json")
	cred, err := os.ReadFile(credPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(cred, []byte("refresh")) {
		t.Fatalf("credentials hold a refresh token: %s", cred)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(credPath)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("credentials perm=%o", info.Mode().Perm())
		}
	}
}

func (s *scriptTTY) WriteStatus(line string) error {
	s.buf.WriteString(line)
	s.buf.WriteByte('\n')
	return nil
}

var _ config.TTY = (*scriptTTY)(nil)

type failReader struct {
	t *testing.T
}

func (r failReader) Read([]byte) (int, error) {
	r.t.Helper()
	r.t.Fatal("stdin was read")
	return 0, io.EOF
}

type callCount struct {
	load, ensure, require, write, auth, create int
}

func (c *callCount) hooks(cfg *config.Config, docID string, docErr error, capture func(title, body, folder string, index int64)) deps {
	return deps{
		Load: func(string, []string) (*config.Config, error) {
			c.load++
			return cfg, nil
		},
		EnsureProductionEnv: func(string, []string, config.TTY) (*config.Config, error) {
			c.ensure++
			return cfg, nil
		},
		RequireClient: func(*config.Config) error {
			c.require++
			return nil
		},
		WriteCredentials: func(*config.Config) error {
			c.write++
			return nil
		},
		Authorize: func(_ context.Context, _ *config.Config, opt oauth.Options) (*oauth2.Token, error) {
			c.auth++
			if opt.Force {
				return nil, fmt.Errorf("docs-cli: error: create set Force")
			}
			return &oauth2.Token{AccessToken: "access-token-value"}, nil
		},
		CreateDocument: func(_ context.Context, title, body, folder string, index int64) (string, error) {
			c.create++
			if capture != nil {
				capture(title, body, folder, index)
			}
			return docID, docErr
		},
	}
}

func TestCreateRejectsBeforeEnv(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env.example"), []byte(exampleEnv()), 0o644); err != nil {
		t.Fatal(err)
	}
	over := strings.Repeat("a", docs.MaxBodyBytes+1)
	tests := []struct {
		name string
		args []string
		in   string
		term bool
		want string
	}{
		{name: "big text", args: []string{"create", "--text", over}, want: "body exceeds 1 MiB"},
		{name: "big stdin", args: []string{"create"}, in: over, want: "body exceeds 1 MiB"},
		{name: "empty title", args: []string{"create", "--title", ""}, want: "title must not be empty"},
		{name: "blank title", args: []string{"create", "--title", "   "}, want: "title must not be empty"},
		{name: "bad folder", args: []string{"create", "--folder", "abc"}, want: "not a Drive folder URL or id"},
		{name: "docs url", args: []string{"create", "--folder", "https://docs.google.com/document/d/" + folderID + "/edit"}, want: "not a Drive folder URL or id"},
		{name: "empty folder flag", args: []string{"create", "--folder", ""}, want: "GOOGLE_DRIVE_FOLDER_ID is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls callCount
			base := calls.hooks(&config.Config{DriveFolderID: folderID, DefaultTitle: "Untitled", InsertIndex: 1, FilePath: filepath.Join(dir, ".env")}, "doc", nil, nil)
			var stdout, stderr bytes.Buffer
			base.Stdin = strings.NewReader(tt.in)
			base.Stdout = &stdout
			base.Stderr = &stderr
			base.Environ = []string{"APP_ENV=production"}
			base.Root = dir
			base.StdinIsTerminal = tt.term
			code := run(tt.args, base)
			if code != 1 || !strings.Contains(stderr.String(), tt.want) {
				t.Fatalf("exit %d stderr %q", code, stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout %q", stdout.String())
			}
			if calls.load != 0 || calls.ensure != 0 || calls.require != 0 || calls.write != 0 || calls.auth != 0 || calls.create != 0 {
				t.Fatalf("calls %+v", calls)
			}
			if _, err := os.Stat(filepath.Join(dir, ".env")); !os.IsNotExist(err) {
				t.Fatalf(".env: %v", err)
			}
		})
	}
}

func TestCreateEmptyFolderSkipsClient(t *testing.T) {
	dir := t.TempDir()
	env := strings.ReplaceAll(exampleEnv(), "you@gmail.com", "dev@example.com")
	env = strings.ReplaceAll(env, "GOOGLE_OAUTH_CLIENT_ID=\n", "GOOGLE_OAUTH_CLIENT_ID=abc.apps.googleusercontent.com\n")
	env = strings.ReplaceAll(env, "GOOGLE_OAUTH_CLIENT_SECRET=\n", "GOOGLE_OAUTH_CLIENT_SECRET="+secretValue+"\n")
	if err := os.WriteFile(filepath.Join(dir, ".env.example"), []byte(env), 0o644); err != nil {
		t.Fatal(err)
	}
	var calls callCount
	base := calls.hooks(nil, "doc", nil, nil)
	var stdout, stderr bytes.Buffer
	base.Stdin = strings.NewReader("ignored")
	base.Stdout = &stdout
	base.Stderr = &stderr
	base.Environ = []string{"APP_ENV=development"}
	base.Root = dir
	base.Load = nil
	base.RequireClient = func(*config.Config) error {
		calls.require++
		return nil
	}
	base.WriteCredentials = func(*config.Config) error {
		calls.write++
		return nil
	}
	base.Authorize = func(context.Context, *config.Config, oauth.Options) (*oauth2.Token, error) {
		calls.auth++
		return nil, nil
	}
	base.CreateDocument = func(context.Context, string, string, string, int64) (string, error) {
		calls.create++
		return "doc", nil
	}
	code := run([]string{"create", "--title", "Demo", "--text", "hello"}, base)
	if code != 1 || !strings.Contains(stderr.String(), "docs-cli: GOOGLE_DRIVE_FOLDER_ID is required") {
		t.Fatalf("exit %d stderr %s", code, stderr.String())
	}
	if stdout.Len() != 0 || calls.require != 0 || calls.write != 0 || calls.auth != 0 || calls.create != 0 {
		t.Fatalf("stdout %q calls %+v", stdout.String(), calls)
	}
	if _, err := os.Stat(filepath.Join(dir, "credentials.json")); !os.IsNotExist(err) {
		t.Fatalf("credentials: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".env")); !os.IsNotExist(err) {
		t.Fatalf(".env: %v", err)
	}
}

func TestCreateFlagFolderOverridesEnv(t *testing.T) {
	dir := t.TempDir()
	envFolder := "https://drive.google.com/drive/folders/" + folderID + "?usp=sharing"
	flagFolder := "ZZ_folder_99"
	original := readyEnv(envFolder)
	path := filepath.Join(dir, ".env.example")
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	var gotTitle, gotBody, gotFolder string
	var gotIndex int64
	var calls callCount
	base := calls.hooks(nil, "doc1234567890", nil, func(title, body, folder string, index int64) {
		gotTitle, gotBody, gotFolder, gotIndex = title, body, folder, index
	})
	base.Load = nil
	base.WriteCredentials = func(*config.Config) error {
		calls.write++
		return os.WriteFile(filepath.Join(dir, "credentials.json"), []byte("rewritten\n"), 0o600)
	}
	base.Authorize = func(_ context.Context, _ *config.Config, opt oauth.Options) (*oauth2.Token, error) {
		calls.auth++
		if opt.Force {
			t.Fatal("Force")
		}
		return &oauth2.Token{AccessToken: "access-token-value"}, nil
	}
	credPath := filepath.Join(dir, "credentials.json")
	if err := os.WriteFile(credPath, []byte(sentinelCredentials), 0o640); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	base.Stdin = failReader{t: t}
	base.Stdout = &stdout
	base.Stderr = &stderr
	base.Environ = []string{"APP_ENV=development"}
	base.Root = dir
	code := run([]string{"create", "--text", " hello ", "--folder", "  " + flagFolder + "  "}, base)
	if code != 0 {
		t.Fatalf("exit %d stderr %s", code, stderr.String())
	}
	if gotFolder != flagFolder || gotTitle != "Untitled" || gotBody != " hello " || gotIndex != 1 {
		t.Fatalf("title %q body %q folder %q index %d", gotTitle, gotBody, gotFolder, gotIndex)
	}
	if calls.load != 0 || calls.ensure != 0 || calls.create != 1 || calls.auth != 1 || calls.write != 0 {
		t.Fatalf("calls %+v", calls)
	}
	if strings.Contains(stderr.String(), "docs-cli: wrote credentials.json") {
		t.Fatalf("stderr %s", stderr.String())
	}
	gotCred, err := os.ReadFile(credPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotCred) != sentinelCredentials {
		t.Fatalf("credentials rewritten: %s", gotCred)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(credPath)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o640 {
			t.Fatalf("credentials perm=%o", info.Mode().Perm())
		}
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Fatalf("env rewritten:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(dir, ".env")); !os.IsNotExist(err) {
		t.Fatalf(".env: %v", err)
	}
}

func TestCreateTextWinsAndTerminalIsEmpty(t *testing.T) {
	cfg := &config.Config{
		Mode:          "development",
		FilePath:      "/tmp/.env.example",
		DriveFolderID: folderID,
		DefaultTitle:  "From Env",
		InsertIndex:   1,
		ClientID:      "abc.apps.googleusercontent.com",
		ClientSecret:  secretValue,
	}
	t.Run("flag wins", func(t *testing.T) {
		var got string
		var calls callCount
		base := calls.hooks(cfg, "doc1234567890", nil, func(_, body, _ string, _ int64) { got = body })
		var stdout, stderr bytes.Buffer
		base.Stdin = failReader{t: t}
		base.Stdout = &stdout
		base.Stderr = &stderr
		base.Environ = []string{"APP_ENV=development"}
		base.Root = t.TempDir()
		code := run([]string{"create", "--title", "  Demo  ", "--text", "from-flag"}, base)
		if code != 0 || got != "from-flag" {
			t.Fatalf("exit %d body %q stderr %s", code, got, stderr.String())
		}
		if !strings.Contains(stderr.String(), "docs-cli: created document doc1234567890") || !strings.Contains(stderr.String(), "docs-cli: body_bytes=9") {
			t.Fatalf("stderr %s", stderr.String())
		}
	})
	t.Run("terminal empty", func(t *testing.T) {
		var gotTitle, gotBody string
		var calls callCount
		base := calls.hooks(cfg, "doc1234567890", nil, func(title, body, _ string, _ int64) {
			gotTitle, gotBody = title, body
		})
		var stdout, stderr bytes.Buffer
		base.Stdin = failReader{t: t}
		base.StdinIsTerminal = true
		base.Stdout = &stdout
		base.Stderr = &stderr
		base.Environ = []string{"APP_ENV=development"}
		base.Root = t.TempDir()
		code := run([]string{"create", "--title", "Demo"}, base)
		if code != 0 || gotBody != "" || gotTitle != "Demo" {
			t.Fatalf("exit %d title %q body %q stderr %s", code, gotTitle, gotBody, stderr.String())
		}
		if !strings.Contains(stderr.String(), "docs-cli: body_bytes=0") {
			t.Fatalf("stderr %s", stderr.String())
		}
	})
	t.Run("empty text skips stdin", func(t *testing.T) {
		var got string
		var calls callCount
		base := calls.hooks(cfg, "doc1234567890", nil, func(_, body, _ string, _ int64) { got = body })
		var stdout, stderr bytes.Buffer
		base.Stdin = strings.NewReader("from-stdin")
		base.Stdout = &stdout
		base.Stderr = &stderr
		base.Environ = []string{"APP_ENV=development"}
		base.Root = t.TempDir()
		code := run([]string{"create", "--title", "Demo", "--text", ""}, base)
		if code != 0 || got != "" {
			t.Fatalf("exit %d body %q stderr %s", code, got, stderr.String())
		}
	})
	t.Run("newline stdin", func(t *testing.T) {
		var got string
		var calls callCount
		base := calls.hooks(cfg, "doc1234567890", nil, func(_, body, _ string, _ int64) { got = body })
		var stdout, stderr bytes.Buffer
		base.Stdin = strings.NewReader("\n")
		base.Stdout = &stdout
		base.Stderr = &stderr
		base.Environ = []string{"APP_ENV=development"}
		base.Root = t.TempDir()
		code := run([]string{"create", "--title", "Demo"}, base)
		if code != 0 || got != "\n" {
			t.Fatalf("exit %d body %q", code, got)
		}
	})
}

func TestCreateSuccessStdout(t *testing.T) {
	dir := t.TempDir()
	raw := "https://drive.google.com/drive/folders/" + folderID + "?usp=sharing"
	original := readyEnv(raw)
	if err := os.WriteFile(filepath.Join(dir, ".env.example"), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	const docID = "doc1234567890"
	var gotFolder, gotTitle, gotBody string
	var calls callCount
	base := calls.hooks(nil, docID, nil, func(title, body, folder string, _ int64) {
		gotTitle, gotBody, gotFolder = title, body, folder
	})
	base.ImportPDF = func(context.Context, string, string, []byte) (string, error) {
		t.Fatal("ImportPDF")
		return "", nil
	}
	base.Load = nil
	base.WriteCredentials = nil
	base.RequireClient = nil
	base.Authorize = func(_ context.Context, cfg *config.Config, opt oauth.Options) (*oauth2.Token, error) {
		calls.auth++
		if opt.Force {
			t.Fatal("Force")
		}
		if cfg.DriveFolderID != folderID {
			t.Fatalf("loaded folder %q", cfg.DriveFolderID)
		}
		return &oauth2.Token{AccessToken: "access-token-value"}, nil
	}
	var stdout, stderr bytes.Buffer
	base.Stdin = strings.NewReader("from-stdin")
	base.Stdout = &stdout
	base.Stderr = &stderr
	base.Environ = []string{"APP_ENV=development"}
	base.Root = dir
	code := run([]string{"create", "--text", "hello"}, base)
	if code != 0 {
		t.Fatalf("exit %d stderr %s", code, stderr.String())
	}
	wantOut := docID + "\n" + docs.EditURL(docID) + "\n"
	if stdout.String() != wantOut {
		t.Fatalf("stdout %q", stdout.String())
	}
	if strings.Contains(stdout.String(), "docs-cli:") {
		t.Fatalf("stdout %q", stdout.String())
	}
	text := stderr.String()
	if strings.Contains(text, "docs-cli: docs-cli:") || strings.Contains(text, secretValue) {
		t.Fatalf("stderr %s", text)
	}
	if !strings.Contains(text, "docs-cli: mode=development file=.env.example") || !strings.Contains(text, "docs-cli: created document "+docID) || !strings.Contains(text, "docs-cli: body_bytes=5") {
		t.Fatalf("stderr %s", text)
	}
	if strings.Contains(text, "docs-cli: wrote credentials.json") {
		t.Fatalf("stderr %s", text)
	}
	if gotFolder != folderID || gotTitle != "Untitled" || gotBody != "hello" {
		t.Fatalf("title %q body %q folder %q", gotTitle, gotBody, gotFolder)
	}
	if calls.ensure != 0 || calls.create != 1 {
		t.Fatalf("calls %+v", calls)
	}
	if _, err := os.Stat(filepath.Join(dir, ".env")); !os.IsNotExist(err) {
		t.Fatalf(".env: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "credentials.json")); !os.IsNotExist(err) {
		t.Fatalf("credentials: %v", err)
	}
}

func TestCreateAndUploadIgnoreCredentialsSymlink(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "ok.pdf"), []byte("%PDF-1.4\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "1.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "cred-target")
	if err := os.WriteFile(target, []byte("untouched\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "credentials.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Mode:            "development",
		FilePath:        "/tmp/.env.example",
		DriveFolderID:   folderID,
		DefaultTitle:    "Untitled",
		InsertIndex:     1,
		ClientID:        "abc.apps.googleusercontent.com",
		ClientSecret:    secretValue,
		CredentialsPath: link,
	}
	cases := [][]string{
		{"create", "--title", "Demo", "--text", "hello"},
		{"create", "--import", "ok.pdf"},
		{"upload", "1.json"},
	}
	for _, args := range cases {
		var calls callCount
		base := calls.hooks(cfg, "doc1234567890", nil, nil)
		base.WriteCredentials = nil
		base.ImportPDF = func(context.Context, string, string, []byte) (string, error) {
			return "doc1234567890", nil
		}
		base.UploadJSON = func(context.Context, string, []docs.JSONFile) ([]string, error) {
			return []string{"id-1"}, nil
		}
		var stdout, stderr bytes.Buffer
		base.Stdout = &stdout
		base.Stderr = &stderr
		base.Environ = []string{"APP_ENV=development"}
		base.Root = dir
		code := run(args, base)
		if code != 0 || strings.Contains(stderr.String(), "symlink") || strings.Contains(stderr.String(), "docs-cli: wrote credentials.json") {
			t.Fatalf("args %v exit %d stderr %s", args, code, stderr.String())
		}
	}
	info, err := os.Lstat(link)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("link: %v mode %v", err, info)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "untouched\n" {
		t.Fatalf("target %q err %v", got, err)
	}
}

func TestCreateInsertFailureIsNotSuccess(t *testing.T) {
	cfg := &config.Config{
		Mode:          "development",
		FilePath:      "/tmp/.env.example",
		DriveFolderID: folderID,
		DefaultTitle:  "Untitled",
		InsertIndex:   1,
	}
	const docID = "doc1234567890"
	var calls callCount
	base := calls.hooks(cfg, docID, fmt.Errorf("docs-cli: insertText failed; document was created: %s", docID), nil)
	var stdout, stderr bytes.Buffer
	base.Stdin = strings.NewReader("")
	base.Stdout = &stdout
	base.Stderr = &stderr
	base.Environ = []string{"APP_ENV=development"}
	base.Root = t.TempDir()
	code := run([]string{"create", "--title", "Demo", "--text", "hello"}, base)
	if code != 1 {
		t.Fatalf("exit %d", code)
	}
	if stdout.Len() != 0 || strings.Contains(stdout.String(), docs.EditURL(docID)) {
		t.Fatalf("stdout %q", stdout.String())
	}
	text := stderr.String()
	if !strings.Contains(text, "docs-cli: insertText failed; document was created: "+docID) || strings.Contains(text, docs.EditURL(docID)) || strings.Contains(text, "docs-cli: created document") {
		t.Fatalf("stderr %s", text)
	}
}

func TestCreateProductionUsesWizard(t *testing.T) {
	cfg := &config.Config{
		Mode:          "production",
		FilePath:      "/tmp/.env",
		DriveFolderID: folderID,
		DefaultTitle:  "Untitled",
		InsertIndex:   1,
	}
	var calls callCount
	base := calls.hooks(cfg, "doc1234567890", nil, nil)
	var stdout, stderr bytes.Buffer
	base.Stdin = strings.NewReader("")
	base.Stdout = &stdout
	base.Stderr = &stderr
	base.Environ = []string{"APP_ENV=production"}
	base.Root = t.TempDir()
	code := run([]string{"create", "--title", "Demo", "--text", "hello"}, base)
	if code != 0 || calls.ensure != 1 || calls.load != 0 || calls.create != 1 {
		t.Fatalf("exit %d calls %+v stderr %s", code, calls, stderr.String())
	}
	if !strings.Contains(stderr.String(), "docs-cli: mode=production file=.env") {
		t.Fatalf("stderr %s", stderr.String())
	}
}

func TestCreateExactCapReachesCreate(t *testing.T) {
	cfg := &config.Config{
		Mode:          "development",
		FilePath:      "/tmp/.env.example",
		DriveFolderID: folderID,
		DefaultTitle:  "Untitled",
		InsertIndex:   1,
	}
	body := strings.Repeat("a", docs.MaxBodyBytes)
	var got int
	var calls callCount
	base := calls.hooks(cfg, "doc1234567890", nil, func(_, text, _ string, _ int64) { got = len(text) })
	var stdout, stderr bytes.Buffer
	base.Stdin = strings.NewReader("")
	base.Stdout = &stdout
	base.Stderr = &stderr
	base.Environ = []string{"APP_ENV=development"}
	base.Root = t.TempDir()
	code := run([]string{"create", "--title", "Demo", "--text", body}, base)
	if code != 0 || got != docs.MaxBodyBytes || calls.create != 1 {
		t.Fatalf("exit %d len %d calls %+v stderr %s", code, got, calls, stderr.String())
	}
}

func TestCreateImportUsage(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "empty import", args: []string{"create", "--import", ""}},
		{name: "empty import and text", args: []string{"create", "--import", "", "--text", "hello"}},
		{name: "import and text", args: []string{"create", "--import", "missing.pdf", "--text", "hello"}},
		{name: "import and empty text", args: []string{"create", "--import", "missing.pdf", "--text", ""}},
		{name: "extra arg", args: []string{"create", "--import", "missing.pdf", "extra"}},
		{name: "txt extra arg", args: []string{"create", "--import", "notes.txt", "extra"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls callCount
			base := calls.hooks(&config.Config{DriveFolderID: folderID, DefaultTitle: "Untitled", InsertIndex: 1}, "doc", nil, nil)
			base.CreateDocument = func(context.Context, string, string, string, int64) (string, error) {
				t.Fatal("CreateDocument")
				return "", nil
			}
			base.ImportPDF = func(context.Context, string, string, []byte) (string, error) {
				t.Fatal("ImportPDF")
				return "", nil
			}
			var stdout, stderr bytes.Buffer
			base.Stdin = failReader{t: t}
			base.Stdout = &stdout
			base.Stderr = &stderr
			base.Environ = []string{"APP_ENV=development"}
			base.Root = t.TempDir()
			code := run(tt.args, base)
			if code != 2 || stdout.Len() != 0 || stderr.String() != usageText {
				t.Fatalf("exit %d stdout %q stderr %q", code, stdout.String(), stderr.String())
			}
			if calls.load != 0 || calls.ensure != 0 || calls.require != 0 || calls.write != 0 || calls.auth != 0 || calls.create != 0 {
				t.Fatalf("calls %+v", calls)
			}
		})
	}
	if !strings.Contains(usageText, "\n  create [--title TITLE] [--text TEXT] [--folder FOLDER] [--import PATH]\n") {
		t.Fatalf("usage commands:\n%s", usageText)
	}
	if !strings.Contains(usageText, "\n  --import PATH    PDF to convert into the Google Doc; omit to use --text or stdin\n") {
		t.Fatalf("usage import flag:\n%s", usageText)
	}
}

func TestCreateImportRejectsBeforeAuth(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "bad.pdf"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "empty.pdf"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lower.pdf"), []byte("%pdf-1.4\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "big.pdf"), bytes.Repeat([]byte("a"), docs.MaxBodyBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "dir.pdf"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "realdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "realdir"), filepath.Join(dir, "dirlink.pdf")); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		args   []string
		want   string
		substr bool
	}{
		{name: "txt", args: []string{"create", "--import", "notes.txt"}, want: "docs-cli: not a pdf file: notes.txt"},
		{name: "bak", args: []string{"create", "--import", "letter.pdf.bak"}, want: "docs-cli: not a pdf file: letter.pdf.bak"},
		{name: "no extension", args: []string{"create", "--import", "readme"}, want: "docs-cli: not a pdf file: readme"},
		{name: "txt before title", args: []string{"create", "--import", "notes.txt", "--title", ""}, want: "docs-cli: not a pdf file: notes.txt"},
		{name: "spaced txt", args: []string{"create", "--import", " notes.txt"}, want: "docs-cli: not a pdf file:  notes.txt"},
		{name: "empty title", args: []string{"create", "--import", "missing.pdf", "--title", "   "}, want: "docs-cli: title must not be empty"},
		{name: "bad folder", args: []string{"create", "--import", "missing.pdf", "--folder", "abc"}, want: "docs-cli: GOOGLE_DRIVE_FOLDER_ID is not a Drive folder URL or id"},
		{name: "too big", args: []string{"create", "--import", "big.pdf"}, want: "docs-cli: pdf exceeds 1 MiB: big.pdf"},
		{name: "directory", args: []string{"create", "--import", "dir.pdf"}, want: "docs-cli: not a regular file: dir.pdf"},
		{name: "directory symlink", args: []string{"create", "--import", "dirlink.pdf"}, want: "docs-cli: not a regular file: dirlink.pdf"},
		{name: "not pdf", args: []string{"create", "--import", "bad.pdf"}, want: "docs-cli: pdf is not valid: bad.pdf"},
		{name: "empty file", args: []string{"create", "--import", "empty.pdf"}, want: "docs-cli: pdf is not valid: empty.pdf"},
		{name: "lower header", args: []string{"create", "--import", "lower.pdf"}, want: "docs-cli: pdf is not valid: lower.pdf"},
		{name: "missing", args: []string{"create", "--import", "missing.pdf"}, want: "docs-cli: error: stat missing.pdf: no such file or directory"},
		{name: "untrimmed path", args: []string{"create", "--import", " missing.pdf"}, want: "docs-cli: error: stat  missing.pdf: no such file or directory", substr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls callCount
			base := calls.hooks(&config.Config{DriveFolderID: folderID, DefaultTitle: "Untitled", InsertIndex: 1}, "", nil, nil)
			base.CreateDocument = func(context.Context, string, string, string, int64) (string, error) {
				t.Fatal("CreateDocument")
				return "", nil
			}
			base.ImportPDF = func(context.Context, string, string, []byte) (string, error) {
				t.Fatal("ImportPDF")
				return "", nil
			}
			var stdout, stderr bytes.Buffer
			base.Stdin = failReader{t: t}
			base.Stdout = &stdout
			base.Stderr = &stderr
			base.Environ = []string{"APP_ENV=production"}
			base.Root = dir
			code := run(tt.args, base)
			got := stderr.String()
			match := got == tt.want+"\n"
			if tt.substr {
				match = strings.Contains(got, tt.want)
			}
			if code != 1 || !match || stdout.Len() != 0 {
				t.Fatalf("exit %d stdout %q stderr %q", code, stdout.String(), got)
			}
			if calls.load != 0 || calls.ensure != 0 || calls.require != 0 || calls.write != 0 || calls.auth != 0 || calls.create != 0 {
				t.Fatalf("calls %+v", calls)
			}
			if _, err := os.Stat(filepath.Join(dir, "credentials.json")); !os.IsNotExist(err) {
				t.Fatalf("credentials: %v", err)
			}
		})
	}
}

func TestCreateImportSuccess(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	const (
		docID   = "doc1234567890"
		relPath = "customname.pdf"
		payload = "%PDF-1.4\nSENTINEL-PDF\n"
	)
	if err := os.WriteFile(relPath, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	keyring.MockInit()
	if err := keyring.Set("docs-cli", "refresh-token", testRefreshItem); err != nil {
		t.Fatal(err)
	}
	const geminiItem = `{"api_key":"gemini-key-value"}`
	if err := keyring.Set("docs-cli", "gemini-api-key", geminiItem); err != nil {
		t.Fatal(err)
	}
	rejectFM(t)
	var calls callCount
	cfg := &config.Config{
		Mode:          "development",
		FilePath:      filepath.Join(dir, ".env.example"),
		DriveFolderID: folderID,
		DefaultTitle:  "From Env",
		InsertIndex:   1,
		ClientID:      "abc.apps.googleusercontent.com",
		ClientSecret:  secretValue,
	}
	base := calls.hooks(cfg, docID, nil, nil)
	base.CreateDocument = func(context.Context, string, string, string, int64) (string, error) {
		t.Fatal("CreateDocument")
		return "", nil
	}
	var gotTitle, gotFolder string
	var got []byte
	base.ImportPDF = func(_ context.Context, title, folder string, pdf []byte) (string, error) {
		gotTitle, gotFolder = title, folder
		got = append([]byte(nil), pdf...)
		return docID, nil
	}
	credPath := filepath.Join(dir, "credentials.json")
	if err := os.WriteFile(credPath, []byte(sentinelCredentials), 0o640); err != nil {
		t.Fatal(err)
	}
	base.WriteCredentials = func(*config.Config) error {
		calls.write++
		return os.WriteFile(credPath, []byte("rewritten\n"), 0o600)
	}
	var stdout, stderr bytes.Buffer
	base.Stdin = failReader{t: t}
	base.Stdout = &stdout
	base.Stderr = &stderr
	base.Environ = []string{"APP_ENV=development"}
	base.Root = dir
	code := run([]string{"create", "--import", relPath}, base)
	if code != 0 {
		t.Fatalf("exit %d stderr %s", code, stderr.String())
	}
	wantOut := docID + "\n" + docs.EditURL(docID) + "\n"
	if stdout.String() != wantOut {
		t.Fatalf("stdout %q", stdout.String())
	}
	text := stderr.String()
	imported := "docs-cli: imported " + relPath + " bytes=" + fmt.Sprint(len(payload))
	importedAt := strings.Index(text, imported)
	createdAt := strings.Index(text, "docs-cli: created document "+docID)
	if strings.Contains(text, "docs-cli: wrote credentials.json") || importedAt < 0 || createdAt < importedAt {
		t.Fatalf("stderr %s", text)
	}
	if strings.Contains(text, "body_bytes") || strings.Contains(text, "SENTINEL-PDF") || strings.Contains(stdout.String(), "SENTINEL-PDF") {
		t.Fatalf("stdout %q stderr %s", stdout.String(), text)
	}
	assertNoSecret(t, stdout.String(), text, secretValue)
	assertRefreshUntouched(t)
	gotKey, err := keyring.Get("docs-cli", "gemini-api-key")
	if err != nil || gotKey != geminiItem {
		t.Fatalf("gemini key %q err %v", gotKey, err)
	}
	if gotTitle != "From Env" || gotFolder != folderID || string(got) != payload {
		t.Fatalf("title %q folder %q pdf %q", gotTitle, gotFolder, got)
	}
	if calls.load != 1 || calls.ensure != 0 || calls.auth != 1 || calls.create != 0 || calls.require != 1 || calls.write != 0 {
		t.Fatalf("calls %+v", calls)
	}
	gotCred, err := os.ReadFile(credPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotCred) != sentinelCredentials {
		t.Fatalf("credentials rewritten: %s", gotCred)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(credPath)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o640 {
			t.Fatalf("credentials perm=%o", info.Mode().Perm())
		}
	}
	disk, err := os.ReadFile(relPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(disk) != payload {
		t.Fatalf("file changed %q", disk)
	}
}

func TestCreateImportTitleFolderAndPDFCase(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	const docID = "doc1234567890"
	payload := []byte("%PDF-1.4\ncase\n")
	if err := os.WriteFile(filepath.Join(dir, "Cover.PDF"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	flagFolder := "ZZ_folder_99"
	var calls callCount
	cfg := &config.Config{
		Mode:          "development",
		FilePath:      "/tmp/.env.example",
		DriveFolderID: folderID,
		DefaultTitle:  "From Env",
		InsertIndex:   1,
	}
	base := calls.hooks(cfg, docID, nil, nil)
	base.CreateDocument = func(context.Context, string, string, string, int64) (string, error) {
		t.Fatal("CreateDocument")
		return "", nil
	}
	var gotTitle, gotFolder string
	var got []byte
	base.ImportPDF = func(_ context.Context, title, folder string, pdf []byte) (string, error) {
		gotTitle, gotFolder = title, folder
		got = append([]byte(nil), pdf...)
		return docID, nil
	}
	var stdout, stderr bytes.Buffer
	base.Stdin = failReader{t: t}
	base.Stdout = &stdout
	base.Stderr = &stderr
	base.Environ = []string{"APP_ENV=development"}
	base.Root = dir
	code := run([]string{"create", "--title", "  MY Cover Letter  ", "--folder", "  " + flagFolder + "  ", "--import", "Cover.PDF"}, base)
	if code != 0 {
		t.Fatalf("exit %d stderr %s", code, stderr.String())
	}
	if gotTitle != "MY Cover Letter" || gotFolder != flagFolder || !bytes.Equal(got, payload) {
		t.Fatalf("title %q folder %q pdf %q", gotTitle, gotFolder, got)
	}
	if stdout.String() != docID+"\n"+docs.EditURL(docID)+"\n" || calls.create != 0 {
		t.Fatalf("stdout %q calls %+v", stdout.String(), calls)
	}
	if !strings.Contains(stderr.String(), "docs-cli: imported Cover.PDF bytes="+fmt.Sprint(len(payload))) {
		t.Fatalf("stderr %s", stderr.String())
	}
}

func TestCreateImportFollowsFileSymlink(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	const docID = "doc1234567890"
	payload := []byte("%PDF-1.4\nlink\n")
	target := filepath.Join(dir, "target.pdf")
	if err := os.WriteFile(target, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "link.pdf")); err != nil {
		t.Fatal(err)
	}
	var calls callCount
	cfg := &config.Config{Mode: "development", FilePath: "/tmp/.env.example", DriveFolderID: folderID, DefaultTitle: "Untitled", InsertIndex: 1}
	base := calls.hooks(cfg, docID, nil, nil)
	var got []byte
	base.ImportPDF = func(_ context.Context, title, folder string, pdf []byte) (string, error) {
		if title != "Untitled" || folder != folderID {
			t.Fatalf("title %q folder %q", title, folder)
		}
		got = append([]byte(nil), pdf...)
		return docID, nil
	}
	var stdout, stderr bytes.Buffer
	base.Stdin = failReader{t: t}
	base.Stdout = &stdout
	base.Stderr = &stderr
	base.Environ = []string{"APP_ENV=development"}
	base.Root = dir
	code := run([]string{"create", "--import", "link.pdf"}, base)
	if code != 0 || !bytes.Equal(got, payload) {
		t.Fatalf("exit %d pdf %q stderr %s", code, got, stderr.String())
	}
	if _, err := os.Lstat(target); err != nil {
		t.Fatal(err)
	}
}

func TestCreateImportExactCap(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	body := append([]byte("%PDF-"), bytes.Repeat([]byte("a"), docs.MaxBodyBytes-len("%PDF-"))...)
	if len(body) != docs.MaxBodyBytes {
		t.Fatalf("len %d", len(body))
	}
	if err := os.WriteFile(filepath.Join(dir, "cap.pdf"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	var calls callCount
	cfg := &config.Config{Mode: "development", FilePath: "/tmp/.env.example", DriveFolderID: folderID, DefaultTitle: "Untitled", InsertIndex: 1}
	base := calls.hooks(cfg, "doc1234567890", nil, nil)
	var got int
	base.ImportPDF = func(_ context.Context, _, _ string, pdf []byte) (string, error) {
		got = len(pdf)
		return "doc1234567890", nil
	}
	var stdout, stderr bytes.Buffer
	base.Stdout = &stdout
	base.Stderr = &stderr
	base.Environ = []string{"APP_ENV=development"}
	base.Root = dir
	code := run([]string{"create", "--import", "cap.pdf"}, base)
	if code != 0 || got != docs.MaxBodyBytes || calls.create != 0 {
		t.Fatalf("exit %d len %d calls %+v stderr %s", code, got, calls, stderr.String())
	}
}

func TestCreateImportFailureIsNotSuccess(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "bad-result.pdf"), []byte("%PDF-1.4\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	const docID = "doc1234567890"
	var calls callCount
	cfg := &config.Config{Mode: "development", FilePath: "/tmp/.env.example", DriveFolderID: folderID, DefaultTitle: "Untitled", InsertIndex: 1}
	base := calls.hooks(cfg, docID, nil, nil)
	base.CreateDocument = func(context.Context, string, string, string, int64) (string, error) {
		t.Fatal("CreateDocument")
		return "", nil
	}
	base.ImportPDF = func(context.Context, string, string, []byte) (string, error) {
		return docID, fmt.Errorf("docs-cli: import did not create a Google Doc: %s", docID)
	}
	var stdout, stderr bytes.Buffer
	base.Stdout = &stdout
	base.Stderr = &stderr
	base.Environ = []string{"APP_ENV=development"}
	base.Root = dir
	code := run([]string{"create", "--import", "bad-result.pdf"}, base)
	if code != 1 || stdout.Len() != 0 {
		t.Fatalf("exit %d stdout %q", code, stdout.String())
	}
	text := stderr.String()
	if !strings.Contains(text, "docs-cli: import did not create a Google Doc: "+docID) || strings.Contains(text, "docs-cli: created document") || strings.Contains(text, "docs-cli: imported ") || strings.Contains(text, docs.EditURL(docID)) {
		t.Fatalf("stderr %s", text)
	}
}

func TestCreateImportEmptyFolderSkipsClient(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "ok.pdf"), []byte("%PDF-1.4\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := strings.ReplaceAll(exampleEnv(), "you@gmail.com", "dev@example.com")
	env = strings.ReplaceAll(env, "GOOGLE_OAUTH_CLIENT_ID=\n", "GOOGLE_OAUTH_CLIENT_ID=abc.apps.googleusercontent.com\n")
	env = strings.ReplaceAll(env, "GOOGLE_OAUTH_CLIENT_SECRET=\n", "GOOGLE_OAUTH_CLIENT_SECRET="+secretValue+"\n")
	if err := os.WriteFile(filepath.Join(dir, ".env.example"), []byte(env), 0o644); err != nil {
		t.Fatal(err)
	}
	var calls callCount
	base := calls.hooks(nil, "", nil, nil)
	base.Load = nil
	base.RequireClient = func(*config.Config) error {
		calls.require++
		return nil
	}
	base.WriteCredentials = func(*config.Config) error {
		calls.write++
		return nil
	}
	base.Authorize = func(context.Context, *config.Config, oauth.Options) (*oauth2.Token, error) {
		calls.auth++
		return nil, nil
	}
	base.ImportPDF = func(context.Context, string, string, []byte) (string, error) {
		t.Fatal("ImportPDF")
		return "", nil
	}
	var stdout, stderr bytes.Buffer
	base.Stdout = &stdout
	base.Stderr = &stderr
	base.Environ = []string{"APP_ENV=development"}
	base.Root = dir
	code := run([]string{"create", "--import", "ok.pdf"}, base)
	if code != 1 || !strings.Contains(stderr.String(), "docs-cli: GOOGLE_DRIVE_FOLDER_ID is required") {
		t.Fatalf("exit %d stderr %s", code, stderr.String())
	}
	if stdout.Len() != 0 || calls.require != 0 || calls.write != 0 || calls.auth != 0 || calls.create != 0 {
		t.Fatalf("stdout %q calls %+v", stdout.String(), calls)
	}
	if _, err := os.Stat(filepath.Join(dir, "credentials.json")); !os.IsNotExist(err) {
		t.Fatalf("credentials: %v", err)
	}
}

func TestCreateImportProductionUsesWizard(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "ok.pdf"), []byte("%PDF-1.4\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Mode:          "production",
		FilePath:      "/tmp/.env",
		DriveFolderID: folderID,
		DefaultTitle:  "Untitled",
		InsertIndex:   1,
	}
	var calls callCount
	base := calls.hooks(cfg, "doc1234567890", nil, nil)
	base.ImportPDF = func(context.Context, string, string, []byte) (string, error) {
		return "doc1234567890", nil
	}
	var stdout, stderr bytes.Buffer
	base.Stdout = &stdout
	base.Stderr = &stderr
	base.Environ = []string{"APP_ENV=production"}
	base.Root = dir
	code := run([]string{"create", "--title", "Demo", "--import", "ok.pdf"}, base)
	if code != 0 || calls.ensure != 1 || calls.load != 0 || calls.create != 0 {
		t.Fatalf("exit %d calls %+v stderr %s", code, calls, stderr.String())
	}
	if !strings.Contains(stderr.String(), "docs-cli: mode=production file=.env") || !strings.Contains(stderr.String(), "docs-cli: imported ok.pdf bytes=9") {
		t.Fatalf("stderr %s", stderr.String())
	}
}

func readyEnv(folder string) string {
	env := strings.ReplaceAll(exampleEnv(), "you@gmail.com", "dev@example.com")
	env = strings.ReplaceAll(env, "GOOGLE_OAUTH_CLIENT_ID=\n", "GOOGLE_OAUTH_CLIENT_ID=abc.apps.googleusercontent.com\n")
	env = strings.ReplaceAll(env, "GOOGLE_OAUTH_CLIENT_SECRET=\n", "GOOGLE_OAUTH_CLIENT_SECRET="+secretValue+"\n")
	if folder != "" {
		env = strings.ReplaceAll(env, "GOOGLE_DRIVE_FOLDER_ID=\n", "GOOGLE_DRIVE_FOLDER_ID="+folder+"\n")
	}
	return env
}

const (
	sampleJobHTML = `<!DOCTYPE html><html><body>
<div data-automation="job-detail-title">Analyst Example</div>
<div data-automation="job-detail-location">Kwun Tong</div>
<div data-automation="job-detail-work-type">Full time</div>
<div data-automation="job-detail-classifications">ICT &amp; Data</div>
<div data-automation="jobAdDetails">
<p><strong>What do we offer?</strong></p>
<ul><li>Medical insurance</li></ul>
<p><strong>Job Description</strong></p>
<ul><li>Build tools</li></ul>
<p><strong>Job Requirement</strong></p>
<ul><li>Go experience</li></ul>
<p>Apply by form</p>
</div>
</body></html>`
	testRefreshItem = `{"refresh_token":"refresh-token-value-1","scope":"https://www.googleapis.com/auth/drive"}`
)

func TestParseCommandWritesJSON(t *testing.T) {
	keyring.MockInit()
	path := jobsdbMinPath(t)
	page, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := runParseArgs(t, []string{"parse", "--html", path}, nil)
	if code != 0 {
		t.Fatalf("exit %d stderr %s", code, stderr)
	}
	if stdout != phraseJSON(t, page, path) {
		t.Fatalf("stdout:\n%s", stdout)
	}
	if stderr != errGeminiKeyMissing+"\n" {
		t.Fatalf("stderr %q", stderr)
	}
	if strings.Contains(stdout, `\u0026`) || !strings.Contains(stdout, "Information & Communication Technology") {
		t.Fatalf("classification encoding:\n%s", stdout)
	}
	for _, leak := range []string{"loginId", "person@example.com", "Salary"} {
		if strings.Contains(stdout, leak) {
			t.Fatalf("stdout contains %s", leak)
		}
	}
	if strings.Contains(stderr, "mode=") {
		t.Fatalf("stderr %q", stderr)
	}
}

func TestParseMissingNode(t *testing.T) {
	keyring.MockInit()
	path := writeFile(t, "page.html", []byte(`<div data-automation="job-detail-title">Role</div>`))
	stdout, stderr, code := runParseArgs(t, []string{"parse", "--html", path}, nil)
	if code != 1 || stderr != errJobAdDetails+"\n" || stdout != "" {
		t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
	}

	missing := filepath.Join(t.TempDir(), "missing.html")
	stdout, stderr, code = runParseArgs(t, []string{"parse", "--html", missing}, nil)
	if code != 1 || stdout != "" || !strings.Contains(stderr, "docs-cli: error:") || strings.Contains(stderr, errJobAdDetails) {
		t.Fatalf("missing file: exit %d stdout %q stderr %q", code, stdout, stderr)
	}
}

func TestParseHTMLTooLarge(t *testing.T) {
	keyring.MockInit()
	over := writeFile(t, "over.html", bytes.Repeat([]byte{'x'}, maxHTMLBytes+1))
	stdout, stderr, code := runParseArgs(t, []string{"parse", "--html", over}, nil)
	if code != 1 || stderr != errHTMLTooLarge+"\n" || stdout != "" {
		t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
	}

	exact := writeFile(t, "exact.html", bytes.Repeat([]byte{'x'}, maxHTMLBytes))
	stdout, stderr, code = runParseArgs(t, []string{"parse", "--html", exact}, nil)
	if code != 1 || stderr != errJobAdDetails+"\n" || stdout != "" {
		t.Fatalf("exact size: exit %d stdout %q stderr %q", code, stdout, stderr)
	}
}

func TestParseUsage(t *testing.T) {
	const leaked = "super-secret-gemini-key"
	tests := []struct {
		name  string
		args  []string
		exact bool
	}{
		{name: "no flag", args: []string{"parse"}},
		{name: "autoparse without html", args: []string{"parse", "--autoparse"}},
		{name: "autoparse extra value", args: []string{"parse", "--html", "page.html", "--autoparse", "yes"}},
		{name: "html and txt", args: []string{"parse", "--html", "page.html", "--txt", "job.txt"}},
		{name: "txt and autoparse", args: []string{"parse", "--txt", "job.txt", "--autoparse"}},
		{name: "txt missing value", args: []string{"parse", "--txt"}},
		{name: "txt empty", args: []string{"parse", "--txt", ""}},
		{name: "missing value", args: []string{"parse", "--html"}},
		{name: "unknown flag", args: []string{"parse", "--bogus"}},
		{name: "extra arg", args: []string{"parse", "--html", "page.html", "extra"}},
		{name: "title flag", args: []string{"parse", "--html", "page.html", "--title", "Demo"}},
		{name: "help", args: []string{"parse", "--help"}, exact: true},
		{name: "short help", args: []string{"parse", "-h"}, exact: true},
		{name: "gemini extra", args: []string{"gemini-key", "extra"}},
		{name: "gemini key flag", args: []string{"gemini-key", "--key", leaked}},
		{name: "gemini help", args: []string{"gemini-key", "--help"}, exact: true},
		{name: "gemini html", args: []string{"gemini-key", "--html", "page.html"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keyring.MockInit()
			var stdout, stderr bytes.Buffer
			code := run(tt.args, deps{
				Stdin:  strings.NewReader(leaked),
				Stdout: &stdout,
				Stderr: &stderr,
			})
			if code != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "usage: docs-cli") {
				t.Fatalf("exit %d stdout %q stderr %q", code, stdout.String(), stderr.String())
			}
			if tt.exact && stderr.String() != usageText {
				t.Fatalf("stderr %q", stderr.String())
			}
			if strings.Contains(stderr.String(), leaked) {
				t.Fatalf("stderr leaked the key %q", stderr.String())
			}
			if _, err := keyring.Get("docs-cli", "gemini-api-key"); !errors.Is(err, keyring.ErrNotFound) {
				t.Fatalf("keyring err %v", err)
			}
		})
	}
	if !strings.Contains(usageText, "\n  parse --html PATH [--autoparse]\n  parse --txt PATH\n  parse --batch\n  gemini-key\n") {
		t.Fatalf("usage commands:\n%s", usageText)
	}
	if !strings.Contains(usageText, "\n  --batch          parse every .txt file in ./batch/jobads\n") {
		t.Fatalf("usage batch flag:\n%s", usageText)
	}
}

func TestParseWorksWithoutEnv(t *testing.T) {
	keyring.MockInit()
	dir := t.TempDir()
	t.Chdir(dir)
	path := jobsdbMinPath(t)
	page, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	d := deps{
		Stdin:        strings.NewReader(""),
		Stdout:       &stdout,
		Stderr:       &stderr,
		Root:         dir,
		Environ:      []string{"APP_ENV=production"},
		GeminiClient: blockGemini(t),
	}
	forbidEnv(t, &d)
	code := run([]string{"parse", "--html", path}, d)
	if code != 0 {
		t.Fatalf("exit %d stderr %s", code, stderr.String())
	}
	if stdout.String() != phraseJSON(t, page, path) {
		t.Fatal("json mismatch")
	}
	if stderr.String() != errGeminiKeyMissing+"\n" {
		t.Fatalf("stderr %q", stderr.String())
	}
	if strings.Contains(stderr.String(), "mode=") || strings.Contains(stderr.String(), ".env") {
		t.Fatalf("stderr %q", stderr.String())
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("temp dir has %d entries", len(entries))
	}
}

func TestParseUsesGeminiModelFromEnv(t *testing.T) {
	const apiKey = "model-env-gemini-key"
	const secret = "env-client-secret-value"
	page := []byte(sampleJobHTML)
	path := writeFile(t, "job.html", page)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env.example"), []byte("GEMINI_MODEL=from-example\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	envPath := filepath.Join(dir, ".env")

	requestURL := func(t *testing.T) string {
		t.Helper()
		keyring.MockInit()
		if err := jobad.StoreGeminiKey(apiKey); err != nil {
			t.Fatal(err)
		}
		cap := &geminiCapture{}
		var stdout, stderr bytes.Buffer
		code := run([]string{"parse", "--html", path}, deps{
			Stdin:        strings.NewReader(""),
			Stdout:       &stdout,
			Stderr:       &stderr,
			Root:         dir,
			GeminiClient: mappingClient(t, http.StatusOK, geminiReply(t, []string{"Medical insurance"}, []string{"Build tools"}, []string{"Go experience"}, []string{"Apply by form"}), cap),
		})
		if cap.url == "" {
			t.Fatalf("exit %d stderr %q", code, stderr.String())
		}
		if strings.Contains(cap.url, apiKey) || strings.Contains(cap.url, secret) || strings.Contains(cap.body, secret) || strings.Contains(stdout.String(), secret) || strings.Contains(stderr.String(), secret) {
			t.Fatalf("secret leaked url %s", cap.url)
		}
		return cap.url
	}

	t.Run("production value", func(t *testing.T) {
		body := "GEMINI_MODEL=gemini-test-model\nGOOGLE_OAUTH_CLIENT_SECRET=" + secret + "\n"
		if err := os.WriteFile(envPath, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		got := requestURL(t)
		if !strings.Contains(got, "/models/gemini-test-model:generateContent") || strings.Contains(got, "gemini-3.8-flash") || strings.Contains(got, "from-example") {
			t.Fatalf("url %s", got)
		}
	})

	t.Run("empty value", func(t *testing.T) {
		if err := os.WriteFile(envPath, []byte("GEMINI_MODEL=\nGEMINI_MODEL=   \n"), 0o600); err != nil {
			t.Fatal(err)
		}
		got := requestURL(t)
		if !strings.Contains(got, "/models/gemini-3.8-flash:generateContent") || strings.Contains(got, "from-example") {
			t.Fatalf("url %s", got)
		}
	})

	t.Run("missing file ignores example", func(t *testing.T) {
		if err := os.Remove(envPath); err != nil {
			t.Fatal(err)
		}
		got := requestURL(t)
		if !strings.Contains(got, "/models/gemini-3.8-flash:generateContent") || strings.Contains(got, "from-example") {
			t.Fatalf("url %s", got)
		}
	})
}

func TestGeminiKeyStoresInKeyring(t *testing.T) {
	const apiKey = "tty-gemini-key-value"
	const stdinKey = "stdin-secret-should-not-store"

	t.Run("stores", func(t *testing.T) {
		keyring.MockInit()
		if err := keyring.Set("docs-cli", "refresh-token", testRefreshItem); err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		envPath := filepath.Join(dir, ".env")
		if err := os.WriteFile(envPath, []byte("KEEP=1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		tty := &fakeTTY{terminal: true, secrets: []string{apiKey}}
		var stdout, stderr bytes.Buffer
		d := deps{
			Stdin:   strings.NewReader(stdinKey),
			Stdout:  &stdout,
			Stderr:  &stderr,
			Root:    dir,
			TTY:     tty,
			Environ: []string{"APP_ENV=production"},
		}
		forbidEnv(t, &d)
		code := run([]string{"gemini-key"}, d)
		if code != 0 || stdout.Len() != 0 || stderr.String() != errGeminiStored+"\n" {
			t.Fatalf("exit %d stdout %q stderr %q", code, stdout.String(), stderr.String())
		}
		assertNoSecret(t, stdout.String(), stderr.String(), apiKey)
		assertNoSecret(t, stdout.String(), stderr.String(), stdinKey)
		if len(tty.prompts) != 1 || tty.prompts[0] != geminiKeyPrompt {
			t.Fatalf("prompts %q", tty.prompts)
		}
		raw, err := keyring.Get("docs-cli", "gemini-api-key")
		if err != nil || raw != `{"api_key":"`+apiKey+`"}` {
			t.Fatalf("stored %q err %v", raw, err)
		}
		assertRefreshUntouched(t)
		got, err := os.ReadFile(envPath)
		if err != nil || string(got) != "KEEP=1\n" {
			t.Fatalf(".env %q err %v", got, err)
		}
	})

	t.Run("needs terminal", func(t *testing.T) {
		keyring.MockInit()
		tty := &fakeTTY{terminal: false, secrets: []string{apiKey}}
		var stdout, stderr bytes.Buffer
		code := run([]string{"gemini-key"}, deps{Stdin: strings.NewReader(apiKey), Stdout: &stdout, Stderr: &stderr, TTY: tty})
		if code != 1 || stdout.Len() != 0 || stderr.String() != errGeminiKeyTerminal+"\n" {
			t.Fatalf("exit %d stdout %q stderr %q", code, stdout.String(), stderr.String())
		}
		if len(tty.prompts) != 0 {
			t.Fatalf("prompted %q", tty.prompts)
		}
		assertNoSecret(t, stdout.String(), stderr.String(), apiKey)
		if _, err := keyring.Get("docs-cli", "gemini-api-key"); !errors.Is(err, keyring.ErrNotFound) {
			t.Fatalf("keyring err %v", err)
		}

		stdout.Reset()
		stderr.Reset()
		code = run([]string{"gemini-key"}, deps{Stdin: strings.NewReader(apiKey), Stdout: &stdout, Stderr: &stderr})
		if code != 1 || stderr.String() != errGeminiKeyTerminal+"\n" || stdout.Len() != 0 {
			t.Fatalf("nil tty exit %d stdout %q stderr %q", code, stdout.String(), stderr.String())
		}
	})

	t.Run("empty does not overwrite", func(t *testing.T) {
		keyring.MockInit()
		if err := jobad.StoreGeminiKey("kept-key"); err != nil {
			t.Fatal(err)
		}
		tty := &fakeTTY{terminal: true, secrets: []string{""}}
		var stdout, stderr bytes.Buffer
		code := run([]string{"gemini-key"}, deps{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr, TTY: tty})
		if code != 0 || stdout.Len() != 0 || stderr.Len() != 0 {
			t.Fatalf("exit %d stdout %q stderr %q", code, stdout.String(), stderr.String())
		}
		got, err := jobad.LoadGeminiKey()
		if err != nil || got != "kept-key" {
			t.Fatalf("key %q err %v", got, err)
		}
	})
}

func TestParseFallsBackWithoutKey(t *testing.T) {
	page := []byte(sampleJobHTML)
	path := writeFile(t, "job.html", page)

	t.Run("no tty", func(t *testing.T) {
		keyring.MockInit()
		tty := &fakeTTY{terminal: false, secrets: []string{"tty-gemini-key-value"}}
		stdout, stderr, code := runParseArgs(t, []string{"parse", "--html", path}, tty)
		if code != 0 || stdout != phraseJSON(t, page, path) || stderr != errGeminiKeyMissing+"\n" {
			t.Fatalf("exit %d stdout %s stderr %q", code, stdout, stderr)
		}
		if len(tty.prompts) != 0 {
			t.Fatalf("prompted %q", tty.prompts)
		}
		if strings.Contains(stderr, "secret not found in keyring") {
			t.Fatalf("stderr %q", stderr)
		}
	})

	t.Run("invalid item", func(t *testing.T) {
		keyring.MockInit()
		if err := keyring.Set("docs-cli", "gemini-api-key", "not-json"); err != nil {
			t.Fatal(err)
		}
		tty := &fakeTTY{terminal: true, secrets: []string{"tty-gemini-key-value"}}
		stdout, stderr, code := runParseArgs(t, []string{"parse", "--html", path}, tty)
		if code != 0 || stdout != phraseJSON(t, page, path) {
			t.Fatalf("exit %d stdout %s stderr %q", code, stdout, stderr)
		}
		if stderr != "docs-cli: keyring item is invalid; run docs-cli gemini-key\n" {
			t.Fatalf("stderr %q", stderr)
		}
		if len(tty.prompts) != 0 {
			t.Fatalf("prompted %q", tty.prompts)
		}
	})

	t.Run("keyring unavailable", func(t *testing.T) {
		const boom = "security: boom-from-keychain"
		keyring.MockInitWithError(errors.New(boom))
		t.Cleanup(func() { keyring.MockInit() })
		stdout, stderr, code := runParseArgs(t, []string{"parse", "--html", path}, nil)
		if code != 0 || stdout != phraseJSON(t, page, path) || stderr != "docs-cli: keyring unavailable\n" {
			t.Fatalf("exit %d stdout %s stderr %q", code, stdout, stderr)
		}
		if strings.Contains(stderr, boom) || strings.Contains(stderr, "security") || strings.Contains(stdout, boom) {
			t.Fatalf("system text leaked stdout %q stderr %q", stdout, stderr)
		}
	})
}

func TestParseDoesNotTouchRefreshToken(t *testing.T) {
	keyring.MockInit()
	if err := keyring.Set("docs-cli", "refresh-token", testRefreshItem); err != nil {
		t.Fatal(err)
	}
	path := jobsdbMinPath(t)
	stdout, stderr, code := runParseArgs(t, []string{"parse", "--html", path}, nil)
	if code != 0 {
		t.Fatalf("parse exit %d stderr %s", code, stderr)
	}
	assertRefreshUntouched(t)
	assertNoSecret(t, stdout, stderr, "refresh-token-value-1")

	const apiKey = "tty-gemini-key-value"
	tty := &fakeTTY{terminal: true, secrets: []string{apiKey}}
	var out, errBuf bytes.Buffer
	code = run([]string{"gemini-key"}, deps{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errBuf, TTY: tty})
	if code != 0 {
		t.Fatalf("gemini-key exit %d stderr %s", code, errBuf.String())
	}
	assertRefreshUntouched(t)
	assertNoSecret(t, out.String(), errBuf.String(), apiKey)
	assertNoSecret(t, stdout, stderr, apiKey)
}

func TestParsePromptsThenMaps(t *testing.T) {
	const apiKey = "tty-gemini-key-value"
	page := []byte(sampleJobHTML)
	path := writeFile(t, "job.html", page)
	phrase := phraseJSON(t, page, path)

	t.Run("prompt then mapped json", func(t *testing.T) {
		keyring.MockInit()
		if err := keyring.Set("docs-cli", "refresh-token", testRefreshItem); err != nil {
			t.Fatal(err)
		}
		reply := geminiReply(t, nil, []string{"Medical insurance", "Build tools"}, []string{"Go experience"}, []string{"Apply by form"})
		cap := &geminiCapture{}
		tty := &fakeTTY{terminal: true, secrets: []string{apiKey}}
		var stdout, stderr bytes.Buffer
		d := deps{
			Stdin:        strings.NewReader("stdin-secret-should-not-store"),
			Stdout:       &stdout,
			Stderr:       &stderr,
			TTY:          tty,
			GeminiClient: mappingClient(t, http.StatusOK, reply, cap),
		}
		forbidEnv(t, &d)
		code := run([]string{"parse", "--html", path}, d)
		if code != 0 {
			t.Fatalf("exit %d stderr %s", code, stderr.String())
		}
		if stderr.String() != errGeminiStored+"\n" {
			t.Fatalf("stderr %q", stderr.String())
		}
		if len(tty.prompts) != 1 || tty.prompts[0] != geminiKeyPrompt {
			t.Fatalf("prompts %q", tty.prompts)
		}
		assertNoSecret(t, stdout.String(), stderr.String(), apiKey)
		assertNoSecret(t, stdout.String(), stderr.String(), "stdin-secret-should-not-store")
		if stdout.String() == phrase {
			t.Fatal("stdout stayed on the phrase document")
		}
		var got jobad.Document
		if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.Source != path || strings.Join(got.Duties, "|") != "Medical insurance|Build tools" || len(got.Benefits) != 0 {
			t.Fatalf("doc %+v", got)
		}
		if strings.Contains(stdout.String(), `\u0026`) || got.Header.Classification != "ICT & Data" {
			t.Fatalf("classification %q json %s", got.Header.Classification, stdout.String())
		}
		if cap.header.Get("x-goog-api-key") != apiKey {
			t.Fatalf("header %q", cap.header.Get("x-goog-api-key"))
		}
		if strings.Contains(cap.url, apiKey) || strings.Contains(cap.body, apiKey) {
			t.Fatal("api key appeared in the url or body")
		}
		if !strings.Contains(cap.url, "generativelanguage.googleapis.com") || strings.Contains(cap.url, "aiplatform.googleapis.com") {
			t.Fatalf("url %s", cap.url)
		}
		user := geminiUserText(t, cap.body)
		if strings.Contains(user, "jobAdDetails") || strings.Contains(user, "Analyst Example") || !strings.HasPrefix(user, "Blocks:\n") {
			t.Fatalf("body sent header or html: %s", user)
		}
		if !strings.Contains(cap.body, "Medical insurance") {
			t.Fatalf("body %s", cap.body)
		}
		assertRefreshUntouched(t)
		raw, err := keyring.Get("docs-cli", "gemini-api-key")
		if err != nil || raw != `{"api_key":"`+apiKey+`"}` {
			t.Fatalf("stored %q err %v", raw, err)
		}
	})

	t.Run("existing key does not prompt", func(t *testing.T) {
		keyring.MockInit()
		if err := jobad.StoreGeminiKey(apiKey); err != nil {
			t.Fatal(err)
		}
		reply := geminiReply(t, nil, []string{"Medical insurance", "Build tools"}, []string{"Go experience"}, []string{"Apply by form"})
		tty := &fakeTTY{terminal: true, secrets: []string{"other-key"}}
		var stdout, stderr bytes.Buffer
		code := run([]string{"parse", "--html", path}, deps{
			Stdin:        strings.NewReader(""),
			Stdout:       &stdout,
			Stderr:       &stderr,
			TTY:          tty,
			GeminiClient: mappingClient(t, http.StatusOK, reply, &geminiCapture{}),
		})
		if code != 0 || stderr.Len() != 0 || len(tty.prompts) != 0 {
			t.Fatalf("exit %d stderr %q prompts %q", code, stderr.String(), tty.prompts)
		}
		assertNoSecret(t, stdout.String(), stderr.String(), apiKey)
		var got jobad.Document
		if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if len(got.Benefits) != 0 || strings.Join(got.Duties, "|") != "Medical insurance|Build tools" {
			t.Fatalf("doc %+v", got)
		}
	})

	t.Run("mapping rejected", func(t *testing.T) {
		keyring.MockInit()
		if err := jobad.StoreGeminiKey(apiKey); err != nil {
			t.Fatal(err)
		}
		reply := geminiReply(t, []string{"Medical insurance", "invented"}, nil, nil, nil)
		var stdout, stderr bytes.Buffer
		code := run([]string{"parse", "--html", path}, deps{
			Stdin:        strings.NewReader(""),
			Stdout:       &stdout,
			Stderr:       &stderr,
			GeminiClient: mappingClient(t, http.StatusOK, reply, &geminiCapture{}),
		})
		want := "docs-cli: gemini mapping rejected: partition\n" +
			"docs-cli: gemini mapping rejected: sent=4 reply=2 missing=3 extra=1\n" +
			"docs-cli: gemini mapping rejected: missing: Build tools\n" +
			"docs-cli: gemini mapping rejected: missing: Go experience\n" +
			"docs-cli: gemini mapping rejected: missing: Apply by form\n" +
			"docs-cli: gemini mapping rejected: extra: invented\n"
		if code != 0 || stdout.String() != phrase || stderr.String() != want {
			t.Fatalf("exit %d stdout %s stderr %q", code, stdout.String(), stderr.String())
		}
		if strings.Contains(stderr.String(), "docs-cli: docs-cli:") || stderr.String() == "docs-cli: gemini mapping rejected\n" {
			t.Fatalf("stderr %q", stderr.String())
		}
		assertNoSecret(t, stdout.String(), stderr.String(), apiKey)
	})

	t.Run("status body stays off stderr", func(t *testing.T) {
		keyring.MockInit()
		if err := jobad.StoreGeminiKey(apiKey); err != nil {
			t.Fatal(err)
		}
		const upstream = "UPSTREAM-BODY"
		var stdout, stderr bytes.Buffer
		code := run([]string{"parse", "--html", path}, deps{
			Stdin:        strings.NewReader(""),
			Stdout:       &stdout,
			Stderr:       &stderr,
			GeminiClient: mappingClient(t, http.StatusInternalServerError, upstream+" "+apiKey, &geminiCapture{}),
		})
		if code != 0 || stdout.String() != phrase || stderr.String() != "docs-cli: gemini mapping rejected: status 500\n" {
			t.Fatalf("exit %d stdout %s stderr %q", code, stdout.String(), stderr.String())
		}
		if strings.Contains(stderr.String(), upstream) || strings.Contains(stderr.String(), "docs-cli: docs-cli:") {
			t.Fatalf("stderr %q", stderr.String())
		}
		assertNoSecret(t, stdout.String(), stderr.String(), apiKey)
	})

	t.Run("status 503 prints error message", func(t *testing.T) {
		keyring.MockInit()
		if err := jobad.StoreGeminiKey(apiKey); err != nil {
			t.Fatal(err)
		}
		const demand = "This model is currently experiencing high demand. Spikes in demand are usually temporary. Please try again later."
		body := `{"error":{"code":503,"message":"` + demand + `","status":"UNAVAILABLE","details":["DETAIL ` + apiKey + `"]}}`
		var stdout, stderr bytes.Buffer
		code := run([]string{"parse", "--html", path}, deps{
			Stdin:        strings.NewReader(""),
			Stdout:       &stdout,
			Stderr:       &stderr,
			GeminiClient: mappingClient(t, http.StatusServiceUnavailable, body, &geminiCapture{}),
		})
		want := "docs-cli: gemini mapping rejected: status 503\n" +
			"docs-cli: gemini mapping rejected: UNAVAILABLE: " + demand + "\n"
		if code != 0 || stdout.String() != phrase || stderr.String() != want {
			t.Fatalf("exit %d stdout %s stderr %q", code, stdout.String(), stderr.String())
		}
		if strings.Contains(stderr.String(), "DETAIL") || strings.Contains(stderr.String(), "docs-cli: docs-cli:") {
			t.Fatalf("stderr %q", stderr.String())
		}
		assertNoSecret(t, stdout.String(), stderr.String(), apiKey)
	})

	t.Run("mapping hides api key line", func(t *testing.T) {
		keyring.MockInit()
		if err := jobad.StoreGeminiKey(apiKey); err != nil {
			t.Fatal(err)
		}
		reply := geminiReply(t, []string{apiKey}, nil, nil, nil)
		var stdout, stderr bytes.Buffer
		code := run([]string{"parse", "--html", path}, deps{
			Stdin:        strings.NewReader(""),
			Stdout:       &stdout,
			Stderr:       &stderr,
			GeminiClient: mappingClient(t, http.StatusOK, reply, &geminiCapture{}),
		})
		want := "docs-cli: gemini mapping rejected: partition\n" +
			"docs-cli: gemini mapping rejected: sent=4 reply=1 missing=4 extra=1\n" +
			"docs-cli: gemini mapping rejected: missing: Medical insurance\n" +
			"docs-cli: gemini mapping rejected: missing: Build tools\n" +
			"docs-cli: gemini mapping rejected: missing: Go experience\n"
		if code != 0 || stdout.String() != phrase || stderr.String() != want {
			t.Fatalf("exit %d stdout %s stderr %q", code, stdout.String(), stderr.String())
		}
		assertNoSecret(t, stdout.String(), stderr.String(), apiKey)
	})
}

func TestParseOtherSource(t *testing.T) {
	const apiKey = "other-source-gemini-key"
	const work = `[ "TEMPORARY", "CONTRACTOR" ]`
	posting, err := json.Marshal(map[string]any{
		"@type": "JobPosting",
		"title": "System Analyst",
		"hiringOrganization": map[string]any{
			"name": "Acme Ltd",
		},
		"jobLocation":    []any{map[string]any{"address": map[string]any{"addressLocality": "Wan Chai"}}},
		"employmentType": work,
		"description": `<p><strong>Benefits</strong></p><ul><li>Medical cover</li></ul>` +
			`<p><strong>Key Responsibilities</strong></p><ul><li>Build the API</li></ul>` +
			`<p><strong>Requirements</strong></p><ul><li>Know Go</li></ul>` +
			`<p>Apply on the site</p>`,
	})
	if err != nil {
		t.Fatal(err)
	}
	page := []byte(`<title>System Analyst at CTgoodjobs</title><script>loginId</script><script type="application/ld+json">` + string(posting) + `</script>`)
	path := writeFile(t, "other.html", page)
	phrase := phraseJSON(t, page, path)

	t.Run("no key keeps phrase json", func(t *testing.T) {
		keyring.MockInit()
		stdout, stderr, code := runParseArgs(t, []string{"parse", "--html", path}, nil)
		if code != 0 || stdout != phrase || stderr != errGeminiKeyMissing+"\n" {
			t.Fatalf("exit %d stdout %s stderr %q", code, stdout, stderr)
		}
		if strings.Contains(stdout, "CTgoodjobs") || strings.Contains(stdout, "loginId") {
			t.Fatalf("stdout %s", stdout)
		}
	})

	t.Run("exact partition replaces lists", func(t *testing.T) {
		keyring.MockInit()
		if err := jobad.StoreGeminiKey(apiKey); err != nil {
			t.Fatal(err)
		}
		reply := geminiReply(t, []string{"Medical cover"}, []string{"Build the API"}, []string{"Know Go"}, []string{"Apply on the site"})
		cap := &geminiCapture{}
		var stdout, stderr bytes.Buffer
		code := run([]string{"parse", "--html", path}, deps{
			Stdin:        strings.NewReader(""),
			Stdout:       &stdout,
			Stderr:       &stderr,
			GeminiClient: mappingClient(t, http.StatusOK, reply, cap),
		})
		if code != 0 || stderr.Len() != 0 {
			t.Fatalf("exit %d stderr %q", code, stderr.String())
		}
		var got jobad.Document
		if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.Source != path || got.Header.TitleBlock != "System Analyst" || got.Header.Company != "Acme Ltd" || got.Header.Location != "Wan Chai" || got.Header.WorkType != work {
			t.Fatalf("header %+v source %q", got.Header, got.Source)
		}
		if strings.Join(got.Benefits, "|") != "Medical cover" || strings.Join(got.Duties, "|") != "Build the API" || strings.Join(got.Requirements, "|") != "Know Go" || strings.Join(got.Trailer, "|") != "Apply on the site" {
			t.Fatalf("lists %+v", got)
		}
		if strings.Contains(cap.body, "<script") || strings.Contains(cap.body, apiKey) || strings.Contains(cap.body, "loginId") || strings.Contains(cap.body, "Acme Ltd") || strings.Contains(cap.body, "Wan Chai") || strings.Contains(cap.body, "TEMPORARY") || strings.Contains(cap.url, apiKey) {
			t.Fatalf("request url %s body %s", cap.url, cap.body)
		}
		if !strings.Contains(cap.url, "generativelanguage.googleapis.com") || !strings.Contains(cap.url, "gemini-3.8-flash") || strings.Contains(cap.url, "aiplatform.googleapis.com") {
			t.Fatalf("url %s", cap.url)
		}
		assertNoSecret(t, stdout.String(), stderr.String(), apiKey)
	})

	t.Run("rewritten sentence keeps phrase json", func(t *testing.T) {
		keyring.MockInit()
		if err := jobad.StoreGeminiKey(apiKey); err != nil {
			t.Fatal(err)
		}
		reply := geminiReply(t, []string{"Great medical cover"}, nil, nil, nil)
		var stdout, stderr bytes.Buffer
		code := run([]string{"parse", "--html", path}, deps{
			Stdin:        strings.NewReader(""),
			Stdout:       &stdout,
			Stderr:       &stderr,
			GeminiClient: mappingClient(t, http.StatusOK, reply, &geminiCapture{}),
		})
		want := "docs-cli: gemini mapping rejected: partition\n" +
			"docs-cli: gemini mapping rejected: sent=4 reply=1 missing=4 extra=1\n" +
			"docs-cli: gemini mapping rejected: missing: Medical cover\n" +
			"docs-cli: gemini mapping rejected: missing: Build the API\n" +
			"docs-cli: gemini mapping rejected: missing: Know Go\n" +
			"docs-cli: gemini mapping rejected: extra: Great medical cover\n"
		if code != 0 || stdout.String() != phrase || stderr.String() != want {
			t.Fatalf("exit %d stdout %s stderr %q", code, stdout.String(), stderr.String())
		}
		assertNoSecret(t, stdout.String(), stderr.String(), apiKey)
	})

	t.Run("header only does not call gemini", func(t *testing.T) {
		keyring.MockInit()
		if err := jobad.StoreGeminiKey(apiKey); err != nil {
			t.Fatal(err)
		}
		only, err := json.Marshal(map[string]any{"@type": "JobPosting", "title": "Only Title"})
		if err != nil {
			t.Fatal(err)
		}
		onlyPage := []byte(`<title>CTgoodjobs</title><script type="application/ld+json">` + string(only) + `</script>`)
		onlyPath := writeFile(t, "header.html", onlyPage)
		stdout, stderr, code := runParseArgs(t, []string{"parse", "--html", onlyPath}, nil)
		if code != 0 || stderr != "" {
			t.Fatalf("exit %d stderr %q stdout %s", code, stderr, stdout)
		}
		var got jobad.Document
		if err := json.Unmarshal([]byte(stdout), &got); err != nil {
			t.Fatal(err)
		}
		if got.Source != onlyPath || got.Header.TitleBlock != "Only Title" || len(got.Benefits) != 0 || len(got.Duties) != 0 || len(got.Requirements) != 0 || len(got.Trailer) != 0 {
			t.Fatalf("doc %+v", got)
		}
		if strings.Contains(stdout, "CTgoodjobs") || !strings.Contains(stdout, `"benefits": []`) {
			t.Fatalf("stdout %s", stdout)
		}
	})
}

func TestParseTaleoHistory(t *testing.T) {
	const apiKey = "taleo-history-gemini-key"
	page := []byte(`<title>Careers at Hospital Authority</title>
<nav><p>CHROME-NOTICE</p></nav>
<input type="hidden" name="initialHistory" value="ftlx0!|!100%!|!!*!<p><strong>Post:</strong> Supplies Supervisor II</p><p><strong>Key Responsibilities\:</strong></p><p>Buy supplies</p><p><strong>Entry Requirements:</strong></p><p>Know stores</p><p><strong>OR</strong></p><p>Second path</p><p><strong>Preferable attributes:</strong></p><p>Excel helps</p>">`)
	path := writeFile(t, "taleo.html", page)
	phrase := phraseJSON(t, page, path)

	t.Run("no key keeps taleo phrase json", func(t *testing.T) {
		keyring.MockInit()
		stdout, stderr, code := runParseArgs(t, []string{"parse", "--html", path}, nil)
		if code != 0 || stdout != phrase || stderr != errGeminiKeyMissing+"\n" {
			t.Fatalf("exit %d stdout %s stderr %q", code, stdout, stderr)
		}
		if strings.Contains(stdout, "CHROME-NOTICE") || strings.Contains(stdout, "Careers at Hospital Authority") || !strings.Contains(stdout, "Know stores") {
			t.Fatalf("stdout %s", stdout)
		}
	})

	t.Run("gemini partitions taleo blocks", func(t *testing.T) {
		keyring.MockInit()
		if err := jobad.StoreGeminiKey(apiKey); err != nil {
			t.Fatal(err)
		}
		reply := geminiReply(t, nil, []string{"Buy supplies"}, []string{"Know stores", "OR", "Second path"}, []string{"Post: Supplies Supervisor II", "Excel helps"})
		cap := &geminiCapture{}
		var stdout, stderr bytes.Buffer
		code := run([]string{"parse", "--html", path}, deps{
			Stdin:        strings.NewReader(""),
			Stdout:       &stdout,
			Stderr:       &stderr,
			GeminiClient: mappingClient(t, http.StatusOK, reply, cap),
		})
		if code != 0 || stderr.Len() != 0 {
			t.Fatalf("exit %d stderr %q stdout %s", code, stderr.String(), stdout.String())
		}
		var got jobad.Document
		if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.Source != path || got.Header.TitleBlock != "Supplies Supervisor II" {
			t.Fatalf("header %+v source %q", got.Header, got.Source)
		}
		if strings.Join(got.Duties, "|") != "Buy supplies" || strings.Join(got.Requirements, "|") != "Know stores|OR|Second path" || strings.Join(got.Trailer, "|") != "Post: Supplies Supervisor II|Excel helps" {
			t.Fatalf("lists %+v", got)
		}
		user := geminiUserText(t, cap.body)
		if strings.Contains(user, "CHROME-NOTICE") || strings.Contains(cap.body, apiKey) || strings.Contains(user, "initialHistory") || strings.Contains(user, "<p>") || strings.Contains(cap.url, apiKey) || !strings.HasPrefix(user, "Blocks:\n") {
			t.Fatalf("request url %s body %s", cap.url, cap.body)
		}
		if !strings.Contains(cap.body, "Buy supplies") || !strings.Contains(cap.body, "Know stores") || !strings.Contains(cap.body, "Excel helps") {
			t.Fatalf("body %s", cap.body)
		}
		assertNoSecret(t, stdout.String(), stderr.String(), apiKey)
	})
}

func TestParseAutoparse(t *testing.T) {
	const apiKey = "autoparse-gemini-key"
	const email = "person@example.com"
	page := []byte(`<html><script>AUTOPARSE-HTML-SENTINEL</script><p>Write to ` + email + ` today</p><p>Preferable attributes: Excel helps</p></html>`)
	var fmCalls [][]string
	scriptedFM(t, &fmCalls)

	t.Run("without flag does not send html", func(t *testing.T) {
		keyring.MockInit()
		if err := keyring.Set("docs-cli", "refresh-token", testRefreshItem); err != nil {
			t.Fatal(err)
		}
		if err := jobad.StoreGeminiKey(apiKey); err != nil {
			t.Fatal(err)
		}
		path := writeFile(t, "job.html", page)
		cap := &geminiCapture{}
		reply := geminiReply(t, nil, nil, nil, []string{"Write to today", "Preferable attributes: Excel helps"})
		var stdout, stderr bytes.Buffer
		d := deps{
			Stdin:        strings.NewReader(""),
			Stdout:       &stdout,
			Stderr:       &stderr,
			GeminiClient: mappingClient(t, http.StatusOK, reply, cap),
		}
		forbidEnv(t, &d)
		code := run([]string{"parse", "--html", path}, d)
		if code != 0 || stderr.Len() != 0 {
			t.Fatalf("exit %d stderr %q stdout %s", code, stderr.String(), stdout.String())
		}
		for _, banned := range []string{"AUTOPARSE-HTML-SENTINEL", "<script", "<p>", email, apiKey} {
			if strings.Contains(cap.body, banned) {
				t.Fatalf("body contains %q\n%s", banned, cap.body)
			}
		}
		if cap.header.Get("x-goog-api-key") != apiKey || strings.Contains(cap.url, apiKey) {
			t.Fatalf("url %s header %q", cap.url, cap.header.Get("x-goog-api-key"))
		}
		assertRefreshUntouched(t)
		assertNoSecret(t, stdout.String(), stderr.String(), apiKey)
	})

	t.Run("writes reencoded json", func(t *testing.T) {
		keyring.MockInit()
		if err := keyring.Set("docs-cli", "refresh-token", testRefreshItem); err != nil {
			t.Fatal(err)
		}
		if err := jobad.StoreGeminiKey(apiKey); err != nil {
			t.Fatal(err)
		}
		path := writeFile(t, "job.html", page)
		model, err := json.Marshal(map[string]any{
			"source": "LEAK-SOURCE",
			"header": map[string]any{
				"title_block":    "Supplies Supervisor",
				"company":        "Acme " + email + " Ltd",
				"location":       "",
				"work_type":      "",
				"classification": "",
			},
			"benefits":     []string{"Gratuity"},
			"duties":       []string{"Buy supplies"},
			"requirements": []string{"Preferable attributes: Excel helps", email},
			"trailer":      []string{"Deadline Friday"},
		})
		if err != nil {
			t.Fatal(err)
		}
		cap := &geminiCapture{}
		var stdout, stderr bytes.Buffer
		d := deps{
			Stdin:        strings.NewReader(""),
			Stdout:       &stdout,
			Stderr:       &stderr,
			GeminiClient: mappingClient(t, http.StatusOK, analysisEnvelope(t, string(model)), cap),
		}
		forbidEnv(t, &d)
		code := run([]string{"parse", "--html", path, "--autoparse"}, d)
		if code != 0 || stderr.Len() != 0 {
			t.Fatalf("exit %d stderr %q stdout %s", code, stderr.String(), stdout.String())
		}
		want := jobad.Document{
			Source: path,
			Header: jobad.Header{
				TitleBlock: "Supplies Supervisor",
				Company:    "Acme Ltd",
			},
			Benefits:     []string{"Gratuity"},
			Duties:       []string{"Buy supplies"},
			Requirements: []string{"Preferable attributes: Excel helps"},
			Trailer:      []string{"Deadline Friday"},
		}
		var buf bytes.Buffer
		if err := writeDocument(&buf, want); err != nil {
			t.Fatal(err)
		}
		if stdout.String() != buf.String() {
			t.Fatalf("stdout:\n%s\nwant:\n%s", stdout.String(), buf.String())
		}
		if strings.Contains(stdout.String(), "LEAK-SOURCE") || strings.Contains(stdout.String(), email) || strings.Contains(stdout.String(), `\u0026`) {
			t.Fatalf("stdout %s", stdout.String())
		}
		kept, err := os.ReadFile(path)
		if err != nil || string(kept) != string(page) {
			t.Fatalf("file changed %q err %v", kept, err)
		}
		assertAutoparseRequest(t, cap, apiKey, string(page))
		assertRefreshUntouched(t)
		assertNoSecret(t, stdout.String(), stderr.String(), apiKey)
	})

	t.Run("no key non tty", func(t *testing.T) {
		keyring.MockInit()
		path := writeFile(t, "job.html", page)
		tty := &fakeTTY{terminal: false, secrets: []string{apiKey}}
		stdout, stderr, code := runParseArgs(t, []string{"parse", "--autoparse", "--html", path}, tty)
		if code != 1 || stdout != "" || stderr != errGeminiKeyMissing+"\n" {
			t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
		}
		if len(tty.prompts) != 0 {
			t.Fatalf("prompted %q", tty.prompts)
		}
	})

	t.Run("not json", func(t *testing.T) {
		keyring.MockInit()
		if err := jobad.StoreGeminiKey(apiKey); err != nil {
			t.Fatal(err)
		}
		path := writeFile(t, "job.html", page)
		var stdout, stderr bytes.Buffer
		code := run([]string{"parse", "--html", path, "--autoparse"}, deps{
			Stdin:        strings.NewReader(""),
			Stdout:       &stdout,
			Stderr:       &stderr,
			GeminiClient: mappingClient(t, http.StatusOK, analysisEnvelope(t, "hello"), &geminiCapture{}),
		})
		if code != 1 || stdout.Len() != 0 || stderr.String() != "docs-cli: gemini autoparse rejected: response\n" {
			t.Fatalf("exit %d stdout %q stderr %q", code, stdout.String(), stderr.String())
		}
		assertNoSecret(t, stdout.String(), stderr.String(), apiKey)
	})

	t.Run("status 400", func(t *testing.T) {
		keyring.MockInit()
		if err := jobad.StoreGeminiKey(apiKey); err != nil {
			t.Fatal(err)
		}
		path := writeFile(t, "job.html", page)
		const msg = "Request contains an invalid argument."
		body := `{"error":{"code":400,"message":"` + msg + `","status":"INVALID_ARGUMENT","details":["` + apiKey + `"]}}`
		var stdout, stderr bytes.Buffer
		code := run([]string{"parse", "--html", path, "--autoparse"}, deps{
			Stdin:        strings.NewReader(""),
			Stdout:       &stdout,
			Stderr:       &stderr,
			GeminiClient: mappingClient(t, http.StatusBadRequest, body, &geminiCapture{}),
		})
		want := "docs-cli: gemini autoparse rejected: status 400\n" +
			"docs-cli: gemini autoparse rejected: INVALID_ARGUMENT: " + msg + "\n"
		if code != 1 || stdout.Len() != 0 || stderr.String() != want {
			t.Fatalf("exit %d stdout %q stderr %q", code, stdout.String(), stderr.String())
		}
		assertNoSecret(t, stdout.String(), stderr.String(), apiKey)
	})

	t.Run("invalid key", func(t *testing.T) {
		keyring.MockInit()
		if err := keyring.Set("docs-cli", "gemini-api-key", "not-json"); err != nil {
			t.Fatal(err)
		}
		path := writeFile(t, "job.html", page)
		stdout, stderr, code := runParseArgs(t, []string{"parse", "--html", path, "--autoparse"}, nil)
		if code != 1 || stdout != "" || stderr != "docs-cli: keyring item is invalid; run docs-cli gemini-key\n" {
			t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
		}
	})

	t.Run("keyring unavailable", func(t *testing.T) {
		const boom = "security: boom-from-keychain"
		keyring.MockInitWithError(errors.New(boom))
		t.Cleanup(func() { keyring.MockInit() })
		path := writeFile(t, "job.html", page)
		stdout, stderr, code := runParseArgs(t, []string{"parse", "--html", path, "--autoparse"}, nil)
		if code != 1 || stdout != "" || stderr != "docs-cli: keyring unavailable\n" {
			t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
		}
		if strings.Contains(stderr, boom) || strings.Contains(stdout, boom) {
			t.Fatalf("system text leaked stdout %q stderr %q", stdout, stderr)
		}
	})

	t.Run("html too large", func(t *testing.T) {
		keyring.MockInit()
		if err := jobad.StoreGeminiKey(apiKey); err != nil {
			t.Fatal(err)
		}
		over := writeFile(t, "over.html", bytes.Repeat([]byte{'x'}, maxHTMLBytes+1))
		stdout, stderr, code := runParseArgs(t, []string{"parse", "--autoparse", "--html", over}, nil)
		if code != 1 || stdout != "" || stderr != errHTMLTooLarge+"\n" {
			t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
		}
	})

	t.Run("prompt then autoparse", func(t *testing.T) {
		keyring.MockInit()
		if err := keyring.Set("docs-cli", "refresh-token", testRefreshItem); err != nil {
			t.Fatal(err)
		}
		path := writeFile(t, "job.html", page)
		model, err := json.Marshal(map[string]any{
			"source": "",
			"header": map[string]any{
				"title_block":    "Supplies Supervisor",
				"company":        "",
				"location":       "",
				"work_type":      "",
				"classification": "",
			},
			"benefits":     []string{},
			"duties":       []string{},
			"requirements": []string{"Preferable attributes: Excel helps"},
			"trailer":      []string{},
		})
		if err != nil {
			t.Fatal(err)
		}
		tty := &fakeTTY{terminal: true, secrets: []string{apiKey}}
		var stdout, stderr bytes.Buffer
		d := deps{
			Stdin:        strings.NewReader("stdin-secret-should-not-store"),
			Stdout:       &stdout,
			Stderr:       &stderr,
			TTY:          tty,
			GeminiClient: mappingClient(t, http.StatusOK, analysisEnvelope(t, string(model)), &geminiCapture{}),
		}
		forbidEnv(t, &d)
		code := run([]string{"parse", "--autoparse", "--html", path}, d)
		if code != 0 || stderr.String() != errGeminiStored+"\n" {
			t.Fatalf("exit %d stderr %q stdout %s", code, stderr.String(), stdout.String())
		}
		if len(tty.prompts) != 1 || tty.prompts[0] != geminiKeyPrompt {
			t.Fatalf("prompts %q", tty.prompts)
		}
		var got jobad.Document
		if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.Source != path || strings.Join(got.Requirements, "|") != "Preferable attributes: Excel helps" {
			t.Fatalf("doc %+v", got)
		}
		assertNoSecret(t, stdout.String(), stderr.String(), apiKey)
		assertNoSecret(t, stdout.String(), stderr.String(), "stdin-secret-should-not-store")
		assertRefreshUntouched(t)
	})

	t.Run("skips local parse", func(t *testing.T) {
		keyring.MockInit()
		if err := jobad.StoreGeminiKey(apiKey); err != nil {
			t.Fatal(err)
		}
		only := []byte(`<script>AUTOPARSE-HTML-SENTINEL</script>`)
		path := writeFile(t, "only.html", only)
		plain, plainErr, plainCode := runParseArgs(t, []string{"parse", "--html", path}, nil)
		if plainCode != 1 || plain != "" || plainErr != errJobAdDetails+"\n" {
			t.Fatalf("plain exit %d stdout %q stderr %q", plainCode, plain, plainErr)
		}
		model, err := json.Marshal(map[string]any{
			"source": "ignored",
			"header": map[string]any{
				"title_block":    "",
				"company":        "",
				"location":       "",
				"work_type":      "",
				"classification": "",
			},
			"benefits":     []any{},
			"duties":       []any{},
			"requirements": []string{"Preferable attributes: Excel helps"},
			"trailer":      []any{},
		})
		if err != nil {
			t.Fatal(err)
		}
		cap := &geminiCapture{}
		var stdout, stderr bytes.Buffer
		code := run([]string{"parse", "--html", path, "--autoparse"}, deps{
			Stdin:        strings.NewReader(""),
			Stdout:       &stdout,
			Stderr:       &stderr,
			GeminiClient: mappingClient(t, http.StatusOK, analysisEnvelope(t, string(model)), cap),
		})
		if code != 0 || stderr.Len() != 0 {
			t.Fatalf("exit %d stderr %q stdout %s", code, stderr.String(), stdout.String())
		}
		var got jobad.Document
		if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.Source != path || strings.Join(got.Requirements, "|") != "Preferable attributes: Excel helps" {
			t.Fatalf("doc %+v", got)
		}
		if !strings.Contains(cap.body, "<script>AUTOPARSE-HTML-SENTINEL</script>") || strings.Contains(cap.body, apiKey) {
			t.Fatalf("body %s", cap.body)
		}
		assertNoSecret(t, stdout.String(), stderr.String(), apiKey)
	})

	t.Run("txt writes reencoded json", func(t *testing.T) {
		keyring.MockInit()
		if err := keyring.Set("docs-cli", "refresh-token", testRefreshItem); err != nil {
			t.Fatal(err)
		}
		if err := jobad.StoreGeminiKey(apiKey); err != nil {
			t.Fatal(err)
		}
		details := []byte("JOB-DETAILS-SENTINEL\nKey Responsibilities\nBuy supplies\nWrite to " + email + " today\n")
		path := writeFile(t, "job.txt", details)
		model, err := json.Marshal(map[string]any{
			"source": "LEAK-SOURCE",
			"header": map[string]any{
				"title_block":    "Supplies Supervisor",
				"company":        "",
				"location":       "",
				"work_type":      "",
				"classification": "",
			},
			"benefits":     []string{},
			"duties":       []string{"Buy supplies"},
			"requirements": []string{"Know stores"},
			"trailer":      []string{},
		})
		if err != nil {
			t.Fatal(err)
		}
		cap := &geminiCapture{}
		var stdout, stderr bytes.Buffer
		d := deps{
			Stdin:        strings.NewReader(""),
			Stdout:       &stdout,
			Stderr:       &stderr,
			GeminiClient: mappingClient(t, http.StatusOK, analysisEnvelope(t, string(model)), cap),
		}
		forbidEnv(t, &d)
		code := run([]string{"parse", "--txt", path}, d)
		if code != 0 || stderr.Len() != 0 {
			t.Fatalf("exit %d stderr %q stdout %s", code, stderr.String(), stdout.String())
		}
		want := jobad.Document{
			Source:       path,
			Header:       jobad.Header{TitleBlock: "Supplies Supervisor"},
			Benefits:     []string{},
			Duties:       []string{"Buy supplies"},
			Requirements: []string{"Know stores"},
			Trailer:      []string{},
		}
		var buf bytes.Buffer
		if err := writeDocument(&buf, want); err != nil {
			t.Fatal(err)
		}
		if stdout.String() != buf.String() {
			t.Fatalf("stdout:\n%s\nwant:\n%s", stdout.String(), buf.String())
		}
		if strings.Contains(stdout.String(), "LEAK-SOURCE") || strings.Contains(stdout.String(), email) {
			t.Fatalf("stdout %s", stdout.String())
		}
		kept, err := os.ReadFile(path)
		if err != nil || string(kept) != string(details) {
			t.Fatalf("file changed %q err %v", kept, err)
		}
		assertAutoparseRequest(t, cap, apiKey, string(details))
		if strings.Contains(cap.body, "<html") || strings.Contains(cap.body, "<p>") || !strings.Contains(cap.body, "JOB-DETAILS-SENTINEL") {
			t.Fatalf("body %s", cap.body)
		}
		assertRefreshUntouched(t)
		assertNoSecret(t, stdout.String(), stderr.String(), apiKey)
	})

	t.Run("txt ignores extension", func(t *testing.T) {
		keyring.MockInit()
		if err := jobad.StoreGeminiKey(apiKey); err != nil {
			t.Fatal(err)
		}
		details := []byte("JOB-DETAILS-SENTINEL\nBuy supplies\n")
		path := writeFile(t, "job.dat", details)
		model, err := json.Marshal(map[string]any{
			"source": "",
			"header": map[string]any{
				"title_block":    "",
				"company":        "",
				"location":       "",
				"work_type":      "",
				"classification": "",
			},
			"benefits":     []string{},
			"duties":       []string{"Buy supplies"},
			"requirements": []string{"Know stores"},
			"trailer":      []string{},
		})
		if err != nil {
			t.Fatal(err)
		}
		cap := &geminiCapture{}
		var stdout, stderr bytes.Buffer
		code := run([]string{"parse", "--txt", path}, deps{
			Stdin:        strings.NewReader(""),
			Stdout:       &stdout,
			Stderr:       &stderr,
			GeminiClient: mappingClient(t, http.StatusOK, analysisEnvelope(t, string(model)), cap),
		})
		if code != 0 || stderr.Len() != 0 {
			t.Fatalf("exit %d stderr %q stdout %s", code, stderr.String(), stdout.String())
		}
		var got jobad.Document
		if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.Source != path || strings.Join(got.Duties, "|") != "Buy supplies" {
			t.Fatalf("doc %+v", got)
		}
		assertAutoparseRequest(t, cap, apiKey, string(details))
		if strings.Contains(cap.body, "<html") {
			t.Fatalf("body %s", cap.body)
		}
		assertNoSecret(t, stdout.String(), stderr.String(), apiKey)
	})

	t.Run("html txt does not autoparse", func(t *testing.T) {
		for _, name := range []string{"job.txt", "job.TXT"} {
			t.Run(name, func(t *testing.T) {
				keyring.MockInit()
				if err := jobad.StoreGeminiKey(apiKey); err != nil {
					t.Fatal(err)
				}
				path := writeFile(t, name, []byte("JOB-DETAILS-SENTINEL\nBuy supplies\n"))
				stdout, stderr, code := runParseArgs(t, []string{"parse", "--html", path}, nil)
				if code != 1 || stdout != "" || stderr != errJobAdDetails+"\n" {
					t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
				}
			})
		}
	})

	t.Run("txt combined flags do not read", func(t *testing.T) {
		keyring.MockInit()
		if err := jobad.StoreGeminiKey(apiKey); err != nil {
			t.Fatal(err)
		}
		html := writeFile(t, "job.html", page)
		missing := filepath.Join(t.TempDir(), "missing.txt")
		cases := [][]string{
			{"parse", "--html", html, "--txt", missing},
			{"parse", "--txt", missing, "--autoparse"},
		}
		for _, args := range cases {
			var stdout, stderr bytes.Buffer
			code := run(args, deps{
				Stdin:        strings.NewReader(""),
				Stdout:       &stdout,
				Stderr:       &stderr,
				GeminiClient: blockGemini(t),
			})
			if code != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "usage: docs-cli") {
				t.Fatalf("args %v exit %d stdout %q stderr %q", args, code, stdout.String(), stderr.String())
			}
		}
	})

	t.Run("txt no key non tty", func(t *testing.T) {
		keyring.MockInit()
		path := writeFile(t, "job.txt", []byte("JOB-DETAILS-SENTINEL\n"))
		var stdout, stderr bytes.Buffer
		code := run([]string{"parse", "--txt", path}, deps{
			Stdin:        strings.NewReader(""),
			Stdout:       &stdout,
			Stderr:       &stderr,
			GeminiClient: blockGemini(t),
		})
		if code != 1 || stdout.Len() != 0 || stderr.String() != errGeminiKeyMissing+"\n" {
			t.Fatalf("exit %d stdout %q stderr %q", code, stdout.String(), stderr.String())
		}
		if strings.Contains(stderr.String(), errJobAdDetails) {
			t.Fatalf("stderr %q", stderr.String())
		}
	})

	t.Run("txt too large", func(t *testing.T) {
		keyring.MockInit()
		over := writeFile(t, "over.txt", bytes.Repeat([]byte{'x'}, maxHTMLBytes+1))
		var stdout, stderr bytes.Buffer
		code := run([]string{"parse", "--txt", over}, deps{
			Stdin:        strings.NewReader(""),
			Stdout:       &stdout,
			Stderr:       &stderr,
			GeminiClient: blockGemini(t),
		})
		if code != 1 || stdout.Len() != 0 || stderr.String() != errTxtTooLarge+"\n" {
			t.Fatalf("exit %d stdout %q stderr %q", code, stdout.String(), stderr.String())
		}
	})

	t.Run("txt missing file", func(t *testing.T) {
		keyring.MockInit()
		missing := filepath.Join(t.TempDir(), "missing.txt")
		stdout, stderr, code := runParseArgs(t, []string{"parse", "--txt", missing}, nil)
		if code != 1 || stdout != "" || !strings.Contains(stderr, "docs-cli: error:") || strings.Contains(stderr, errJobAdDetails) {
			t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
		}
	})

	if len(fmCalls) != 0 {
		t.Fatalf("fm calls %d", len(fmCalls))
	}
}

func geminiUserText(t *testing.T, body string) string {
	t.Helper()
	var req struct {
		Contents []struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"contents"`
	}
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatal(err)
	}
	if len(req.Contents) != 1 || len(req.Contents[0].Parts) != 1 {
		t.Fatalf("contents %#v", req.Contents)
	}
	return req.Contents[0].Parts[0].Text
}

func assertAutoparseRequest(t *testing.T, cap *geminiCapture, apiKey, page string) {
	t.Helper()
	redacted := strings.ReplaceAll(page, "person@example.com", "")
	if cap.header.Get("x-goog-api-key") != apiKey {
		t.Fatalf("header %q", cap.header.Get("x-goog-api-key"))
	}
	if strings.Contains(cap.url, apiKey) || strings.Contains(cap.body, apiKey) || strings.Contains(cap.body, "person@example.com") {
		t.Fatalf("url %s body %s", cap.url, cap.body)
	}
	if !strings.Contains(cap.url, "generativelanguage.googleapis.com") || !strings.Contains(cap.url, "gemini-3.8-flash") || strings.Contains(cap.url, "aiplatform.googleapis.com") {
		t.Fatalf("url %s", cap.url)
	}
	var req struct {
		SystemInstruction struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"systemInstruction"`
		Contents []struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"contents"`
		GenerationConfig struct {
			Mime           string          `json:"response_mime_type"`
			Schema         json.RawMessage `json:"response_schema"`
			ThinkingConfig struct {
				Level string `json:"thinkingLevel"`
			} `json:"thinkingConfig"`
		} `json:"generationConfig"`
	}
	if err := json.Unmarshal([]byte(cap.body), &req); err != nil {
		t.Fatal(err)
	}
	if len(req.SystemInstruction.Parts) != 1 || req.SystemInstruction.Parts[0].Text != jobad.GeminiParseInstructions {
		t.Fatalf("instructions %#v", req.SystemInstruction.Parts)
	}
	if len(req.Contents) != 1 || len(req.Contents[0].Parts) != 1 || req.Contents[0].Parts[0].Text != "Page:\n"+redacted {
		t.Fatalf("user text %q", req.Contents)
	}
	if strings.Contains(page, "AUTOPARSE-HTML-SENTINEL") && !strings.Contains(req.Contents[0].Parts[0].Text, "<script>AUTOPARSE-HTML-SENTINEL</script>") {
		t.Fatalf("user text %s", req.Contents[0].Parts[0].Text)
	}
	if req.GenerationConfig.Mime != "application/json" || bytes.Contains(req.GenerationConfig.Schema, []byte(`"additionalProperties"`)) {
		t.Fatalf("schema %s", req.GenerationConfig.Schema)
	}
	if req.GenerationConfig.ThinkingConfig.Level != "HIGH" {
		t.Fatalf("thinking level %q", req.GenerationConfig.ThinkingConfig.Level)
	}
	var schema struct {
		Required   []string                   `json:"required"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(req.GenerationConfig.Schema, &schema); err != nil {
		t.Fatal(err)
	}
	want := []string{"source", "header", "benefits", "duties", "requirements", "trailer"}
	if !slices.Equal(schema.Required, want) {
		t.Fatalf("required %#v", schema.Required)
	}
	for _, name := range want {
		if _, ok := schema.Properties[name]; !ok {
			t.Fatalf("schema missing %s", name)
		}
	}
}

func runBatch(t *testing.T, dir string, args []string, d deps) (string, string, int) {
	t.Helper()
	t.Chdir(dir)
	var stdout, stderr bytes.Buffer
	d.Stdout = &stdout
	d.Stderr = &stderr
	if d.Stdin == nil {
		d.Stdin = strings.NewReader("")
	}
	forbidEnv(t, &d)
	code := run(args, d)
	return stdout.String(), stderr.String(), code
}

func writeAt(t *testing.T, path string, body []byte) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertPerm(t *testing.T, path string, perm os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	if info.Mode().Perm() != perm {
		t.Fatalf("%s perm %o want %o", path, info.Mode().Perm(), perm)
	}
}

type geminiStep struct {
	status int
	body   string
}

func scriptedGemini(t *testing.T, caps *[]geminiCapture, steps ...geminiStep) *http.Client {
	t.Helper()
	n := 0
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		*caps = append(*caps, geminiCapture{
			url:    r.URL.String(),
			header: r.Header.Clone(),
			body:   string(body),
		})
		if n >= len(steps) {
			t.Errorf("extra gemini call")
			return nil, errors.New("extra gemini call")
		}
		step := steps[n]
		n++
		return &http.Response{
			StatusCode: step.status,
			Status:     fmt.Sprintf("%d %s", step.status, http.StatusText(step.status)),
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(step.body)),
			Request:    r,
		}, nil
	})}
}

func autoparseDocEnvelope(t *testing.T, title, duty string) string {
	t.Helper()
	model, err := json.Marshal(map[string]any{
		"source": "LEAK-SOURCE",
		"header": map[string]any{
			"title_block":    title,
			"company":        "",
			"location":       "",
			"work_type":      "",
			"classification": "",
		},
		"benefits":     []string{},
		"duties":       []string{duty},
		"requirements": []string{"Know stores"},
		"trailer":      []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return analysisEnvelope(t, string(model))
}

func runParseArgs(t *testing.T, args []string, tty config.TTY) (string, string, int) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	d := deps{
		Stdin:        strings.NewReader(""),
		Stdout:       &stdout,
		Stderr:       &stderr,
		TTY:          tty,
		GeminiClient: blockGemini(t),
	}
	forbidEnv(t, &d)
	code := run(args, d)
	return stdout.String(), stderr.String(), code
}

func TestParseBatch(t *testing.T) {
	const apiKey = "batch-gemini-key"
	const email = "person@example.com"
	var fmCalls [][]string
	scriptedFM(t, &fmCalls)

	t.Run("writes parsed json and moves txt", func(t *testing.T) {
		keyring.MockInit()
		if err := keyring.Set("docs-cli", "refresh-token", testRefreshItem); err != nil {
			t.Fatal(err)
		}
		if err := jobad.StoreGeminiKey(apiKey); err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		jobads := filepath.Join(dir, "batch", "jobads")
		if err := os.MkdirAll(jobads, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(dir, "batch"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(jobads, 0o755); err != nil {
			t.Fatal(err)
		}
		role := []byte("ROLE-JOB\nLead stores\n")
		aBody := []byte("A-JOB\nWrite to " + email + " today\n")
		bBody := []byte("B-JOB\nBuy supplies\n")
		keep := []byte("KEEP-HTML")
		skip := []byte("SKIP-TXT")
		notes := []byte("NOTES")
		writeAt(t, filepath.Join(jobads, "b.txt"), bBody)
		writeAt(t, filepath.Join(jobads, "Role.TXT"), role)
		writeAt(t, filepath.Join(jobads, "a.txt"), aBody)
		writeAt(t, filepath.Join(jobads, "keep.html"), keep)
		writeAt(t, filepath.Join(jobads, "nested", "skip.txt"), skip)
		writeAt(t, filepath.Join(jobads, "notes.md"), notes)

		names := []string{"Role.TXT", "a.txt", "b.txt"}
		titles := []string{"Role", "Alpha", "Beta"}
		duties := []string{"Lead stores", "Write today", "Buy supplies"}
		bodies := [][]byte{role, aBody, bBody}
		var steps []geminiStep
		for i := range names {
			steps = append(steps, geminiStep{status: http.StatusOK, body: autoparseDocEnvelope(t, titles[i], duties[i])})
		}
		var caps []geminiCapture
		stdout, stderr, code := runBatch(t, dir, []string{"parse", "--batch"}, deps{
			GeminiClient: scriptedGemini(t, &caps, steps...),
		})
		if code != 0 || stderr != "" {
			t.Fatalf("exit %d stderr %q stdout %q", code, stderr, stdout)
		}
		var lines []string
		for i, name := range names {
			stem := strings.TrimSuffix(name, filepath.Ext(name))
			jsonName := stem + ".json"
			jsonPath := filepath.Join(dir, "batch", "jobads", "parsed", jsonName)
			abs, err := filepath.Abs(jsonPath)
			if err != nil {
				t.Fatal(err)
			}
			lines = append(lines, abs)
			want := jobad.Document{
				Source:       filepath.Join("batch", "jobads", name),
				Header:       jobad.Header{TitleBlock: titles[i]},
				Benefits:     []string{},
				Duties:       []string{duties[i]},
				Requirements: []string{"Know stores"},
				Trailer:      []string{},
			}
			var buf bytes.Buffer
			if err := writeIndentedJSON(&buf, want); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(jsonPath)
			if err != nil || string(got) != buf.String() {
				t.Fatalf("%s json %q want %q err %v", name, got, buf.String(), err)
			}
			if strings.Contains(string(got), "LEAK-SOURCE") || strings.Contains(string(got), email) || strings.Contains(string(got), apiKey) {
				t.Fatalf("json %s", got)
			}
			info, err := os.Stat(jsonPath)
			if err != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("%s perm %v err %v", jsonName, info, err)
			}
			moved := filepath.Join(dir, "batch", "jobads", "parsed", name)
			kept, err := os.ReadFile(moved)
			if err != nil || string(kept) != string(bodies[i]) {
				t.Fatalf("moved %s %q err %v", name, kept, err)
			}
			if _, err := os.Lstat(filepath.Join(jobads, name)); !os.IsNotExist(err) {
				t.Fatalf("%s still in jobads: %v", name, err)
			}
		}
		if stdout != strings.Join(lines, "\n")+"\n" {
			t.Fatalf("stdout %q", stdout)
		}
		if strings.Contains(stdout, "benefits") || strings.Contains(stdout, "Know stores") {
			t.Fatalf("stdout printed json %q", stdout)
		}
		assertPerm(t, filepath.Join(dir, "batch"), 0o755)
		assertPerm(t, jobads, 0o755)
		assertPerm(t, filepath.Join(dir, "batch", "jobads", "parsed"), 0o700)
		if _, err := os.Lstat(filepath.Join(dir, "batch", "jobads", "analyzed")); !os.IsNotExist(err) {
			t.Fatalf("parse --batch created analyzed: %v", err)
		}
		if got, err := os.ReadFile(filepath.Join(jobads, "keep.html")); err != nil || string(got) != string(keep) {
			t.Fatalf("keep.html %q err %v", got, err)
		}
		if got, err := os.ReadFile(filepath.Join(jobads, "nested", "skip.txt")); err != nil || string(got) != string(skip) {
			t.Fatalf("skip.txt %q err %v", got, err)
		}
		if got, err := os.ReadFile(filepath.Join(jobads, "notes.md")); err != nil || string(got) != string(notes) {
			t.Fatalf("notes %q err %v", got, err)
		}
		if _, err := os.Lstat(filepath.Join(dir, "batch", "jobads", "parsed", "skip.txt")); !os.IsNotExist(err) {
			t.Fatalf("nested txt was parsed: %v", err)
		}
		if _, err := os.Lstat(filepath.Join(testDraftRoot, "batch")); !os.IsNotExist(err) {
			t.Fatalf("batch under draft root: %v", err)
		}
		if len(caps) != len(names) {
			t.Fatalf("gemini calls %d", len(caps))
		}
		for i := range names {
			assertAutoparseRequest(t, &caps[i], apiKey, string(bodies[i]))
			if !strings.Contains(caps[i].body, []string{"ROLE-JOB", "A-JOB", "B-JOB"}[i]) {
				t.Fatalf("call %d body %s", i, caps[i].body)
			}
		}
		assertRefreshUntouched(t)
		assertNoSecret(t, stdout, stderr, apiKey)
	})

	t.Run("no txt", func(t *testing.T) {
		keyring.MockInit()
		dir := t.TempDir()
		tty := &fakeTTY{terminal: true, secrets: []string{apiKey}}
		stdout, stderr, code := runBatch(t, dir, []string{"parse", "--batch"}, deps{
			TTY:          tty,
			GeminiClient: blockGemini(t),
		})
		if code != 1 || stdout != "" || stderr != errNoBatchTxt+"\n" {
			t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
		}
		if len(tty.prompts) != 0 {
			t.Fatalf("prompted %q", tty.prompts)
		}
		assertPerm(t, filepath.Join(dir, "batch"), 0o700)
		assertPerm(t, filepath.Join(dir, "batch", "jobads"), 0o700)
		assertPerm(t, filepath.Join(dir, "batch", "jobads", "parsed"), 0o700)
		if _, err := keyring.Get("docs-cli", "gemini-api-key"); !errors.Is(err, keyring.ErrNotFound) {
			t.Fatalf("keyring err %v", err)
		}
	})

	t.Run("non txt stays", func(t *testing.T) {
		keyring.MockInit()
		dir := t.TempDir()
		jobads := filepath.Join(dir, "batch", "jobads")
		keep := []byte("KEEP-HTML")
		skip := []byte("SKIP-TXT")
		writeAt(t, filepath.Join(jobads, "keep.html"), keep)
		writeAt(t, filepath.Join(jobads, "nested", "skip.txt"), skip)
		stdout, stderr, code := runBatch(t, dir, []string{"parse", "--batch"}, deps{
			GeminiClient: blockGemini(t),
		})
		if code != 1 || stdout != "" || stderr != errNoBatchTxt+"\n" {
			t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
		}
		if got, err := os.ReadFile(filepath.Join(jobads, "keep.html")); err != nil || string(got) != string(keep) {
			t.Fatalf("keep %q err %v", got, err)
		}
		if got, err := os.ReadFile(filepath.Join(jobads, "nested", "skip.txt")); err != nil || string(got) != string(skip) {
			t.Fatalf("skip %q err %v", got, err)
		}
	})

	t.Run("usage does not create batch", func(t *testing.T) {
		keyring.MockInit()
		cases := [][]string{
			{"parse", "--batch", "--txt", "job.txt"},
			{"parse", "--batch", "--html", "page.html"},
			{"parse", "--batch", "--autoparse"},
			{"parse", "--batch", "extra"},
			{"parse", "--batch", "--html", "page.html", "extra"},
			{"parse", "--batch", "--html", "page.html", "--autoparse"},
			{"parse", "--batch", "--help"},
			{"parse", "--batch=false"},
			{"parse", "--batch=false", "--txt", "job.txt"},
		}
		for _, args := range cases {
			dir := t.TempDir()
			stdout, stderr, code := runBatch(t, dir, args, deps{GeminiClient: blockGemini(t)})
			if code != 2 || stdout != "" || !strings.Contains(stderr, "usage: docs-cli") {
				t.Fatalf("args %v exit %d stdout %q stderr %q", args, code, stdout, stderr)
			}
			if _, err := os.Lstat(filepath.Join(dir, "batch")); !os.IsNotExist(err) {
				t.Fatalf("args %v created batch: %v", args, err)
			}
		}
	})

	t.Run("too large before gemini", func(t *testing.T) {
		keyring.MockInit()
		if err := jobad.StoreGeminiKey(apiKey); err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		jobads := filepath.Join(dir, "batch", "jobads")
		small := []byte("A-JOB\n")
		writeAt(t, filepath.Join(jobads, "a.txt"), small)
		writeAt(t, filepath.Join(jobads, "big.txt"), bytes.Repeat([]byte{'x'}, maxHTMLBytes+1))
		tty := &fakeTTY{terminal: true, secrets: []string{"other-key"}}
		stdout, stderr, code := runBatch(t, dir, []string{"parse", "--batch"}, deps{
			TTY:          tty,
			GeminiClient: blockGemini(t),
		})
		want := "docs-cli: txt exceeds 2 MiB: " + filepath.Join("batch", "jobads", "big.txt") + "\n"
		if code != 1 || stdout != "" || stderr != want {
			t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
		}
		if len(tty.prompts) != 0 {
			t.Fatalf("prompted %q", tty.prompts)
		}
		if got, err := os.ReadFile(filepath.Join(jobads, "a.txt")); err != nil || string(got) != string(small) {
			t.Fatalf("a.txt moved %q err %v", got, err)
		}
	})

	t.Run("exact cap", func(t *testing.T) {
		keyring.MockInit()
		if err := jobad.StoreGeminiKey(apiKey); err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		jobads := filepath.Join(dir, "batch", "jobads")
		body := bytes.Repeat([]byte{'Q'}, maxHTMLBytes)
		writeAt(t, filepath.Join(jobads, "exact.txt"), body)
		var caps []geminiCapture
		stdout, stderr, code := runBatch(t, dir, []string{"parse", "--batch"}, deps{
			GeminiClient: scriptedGemini(t, &caps, geminiStep{status: http.StatusOK, body: autoparseDocEnvelope(t, "Exact", "Know stores")}),
		})
		if code != 0 || stderr != "" {
			t.Fatalf("exit %d stderr %q stdout %q", code, stderr, stdout)
		}
		if len(caps) != 1 {
			t.Fatalf("gemini calls %d", len(caps))
		}
		moved, err := os.ReadFile(filepath.Join(dir, "batch", "jobads", "parsed", "exact.txt"))
		if err != nil || !bytes.Equal(moved, body) {
			t.Fatalf("moved %d err %v", len(moved), err)
		}
		jsonPath := filepath.Join(dir, "batch", "jobads", "parsed", "exact.json")
		abs, err := filepath.Abs(jsonPath)
		if err != nil {
			t.Fatal(err)
		}
		if stdout != abs+"\n" {
			t.Fatalf("stdout %q", stdout)
		}
	})

	t.Run("symlink", func(t *testing.T) {
		keyring.MockInit()
		if err := jobad.StoreGeminiKey(apiKey); err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		jobads := filepath.Join(dir, "batch", "jobads")
		small := []byte("A-JOB\n")
		writeAt(t, filepath.Join(jobads, "a.txt"), small)
		target := writeAt(t, filepath.Join(dir, "outside.txt"), []byte("OUT\n"))
		if err := os.Symlink(target, filepath.Join(jobads, "link.txt")); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(jobads, "dir.txt"), 0o700); err != nil {
			t.Fatal(err)
		}
		stdout, stderr, code := runBatch(t, dir, []string{"parse", "--batch"}, deps{
			GeminiClient: blockGemini(t),
		})
		want := "docs-cli: not a regular file: " + filepath.Join("batch", "jobads", "dir.txt") + "\n"
		if code != 1 || stdout != "" || stderr != want {
			t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
		}
		if got, err := os.ReadFile(filepath.Join(jobads, "a.txt")); err != nil || string(got) != string(small) {
			t.Fatalf("a.txt %q err %v", got, err)
		}
	})

	t.Run("destination exists", func(t *testing.T) {
		keyring.MockInit()
		if err := jobad.StoreGeminiKey(apiKey); err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		jobads := filepath.Join(dir, "batch", "jobads")
		parsed := filepath.Join(jobads, "parsed")
		writeAt(t, filepath.Join(jobads, "a.txt"), []byte("A-JOB\n"))
		writeAt(t, filepath.Join(jobads, "b.txt"), []byte("B-JOB\n"))
		writeAt(t, filepath.Join(jobads, "c.txt"), []byte("C-JOB\n"))
		writeAt(t, filepath.Join(jobads, "d.txt"), []byte("D-JOB\n"))
		writeAt(t, filepath.Join(parsed, "a.json"), []byte("KEEP-JSON"))
		writeAt(t, filepath.Join(parsed, "b.txt"), []byte("OLD-TXT"))
		writeAt(t, filepath.Join(parsed, "c.json"), []byte("KEEP-C"))
		writeAt(t, filepath.Join(parsed, "c.txt"), []byte("OLD-C"))
		if err := os.Symlink(filepath.Join(dir, "missing"), filepath.Join(parsed, "d.json")); err != nil {
			t.Fatal(err)
		}
		stdout, stderr, code := runBatch(t, dir, []string{"parse", "--batch"}, deps{
			GeminiClient: blockGemini(t),
		})
		want := "docs-cli: parsed file already exists: " + filepath.Join("batch", "jobads", "parsed", "a.json") + "\n"
		if code != 1 || stdout != "" || stderr != want {
			t.Fatalf("both exit %d stdout %q stderr %q", code, stdout, stderr)
		}
		if got, err := os.ReadFile(filepath.Join(jobads, "a.txt")); err != nil || string(got) != "A-JOB\n" {
			t.Fatalf("a.txt %q err %v", got, err)
		}
		if got, err := os.ReadFile(filepath.Join(parsed, "a.json")); err != nil || string(got) != "KEEP-JSON" {
			t.Fatalf("a.json %q err %v", got, err)
		}

		if err := os.Remove(filepath.Join(jobads, "a.txt")); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(parsed, "a.json")); err != nil {
			t.Fatal(err)
		}
		stdout, stderr, code = runBatch(t, dir, []string{"parse", "--batch"}, deps{
			GeminiClient: blockGemini(t),
		})
		want = "docs-cli: parsed file already exists: " + filepath.Join("batch", "jobads", "parsed", "b.txt") + "\n"
		if code != 1 || stdout != "" || stderr != want {
			t.Fatalf("txt exit %d stdout %q stderr %q", code, stdout, stderr)
		}

		if err := os.Remove(filepath.Join(jobads, "b.txt")); err != nil {
			t.Fatal(err)
		}
		stdout, stderr, code = runBatch(t, dir, []string{"parse", "--batch"}, deps{
			GeminiClient: blockGemini(t),
		})
		want = "docs-cli: parsed file already exists: " + filepath.Join("batch", "jobads", "parsed", "c.json") + "\n"
		if code != 1 || stdout != "" || stderr != want {
			t.Fatalf("json first exit %d stdout %q stderr %q", code, stdout, stderr)
		}

		if err := os.Remove(filepath.Join(jobads, "c.txt")); err != nil {
			t.Fatal(err)
		}
		stdout, stderr, code = runBatch(t, dir, []string{"parse", "--batch"}, deps{
			GeminiClient: blockGemini(t),
		})
		want = "docs-cli: parsed file already exists: " + filepath.Join("batch", "jobads", "parsed", "d.json") + "\n"
		if code != 1 || stdout != "" || stderr != want {
			t.Fatalf("symlink exit %d stdout %q stderr %q", code, stdout, stderr)
		}
		if _, err := os.Lstat(filepath.Join(jobads, "d.txt")); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("stops after rejection", func(t *testing.T) {
		keyring.MockInit()
		if err := keyring.Set("docs-cli", "refresh-token", testRefreshItem); err != nil {
			t.Fatal(err)
		}
		if err := jobad.StoreGeminiKey(apiKey); err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		jobads := filepath.Join(dir, "batch", "jobads")
		aBody := []byte("A-JOB\nWrite to " + email + " today\n")
		bBody := []byte("B-JOB\nBuy supplies\n")
		cBody := []byte("C-JOB\nLater\n")
		writeAt(t, filepath.Join(jobads, "c.txt"), cBody)
		writeAt(t, filepath.Join(jobads, "a.txt"), aBody)
		writeAt(t, filepath.Join(jobads, "b.txt"), bBody)
		var caps []geminiCapture
		stdout, stderr, code := runBatch(t, dir, []string{"parse", "--batch"}, deps{
			GeminiClient: scriptedGemini(t, &caps,
				geminiStep{status: http.StatusOK, body: autoparseDocEnvelope(t, "Alpha", "Write today")},
				geminiStep{status: http.StatusInternalServerError, body: "nope"},
			),
		})
		jsonPath := filepath.Join(dir, "batch", "jobads", "parsed", "a.json")
		abs, err := filepath.Abs(jsonPath)
		if err != nil {
			t.Fatal(err)
		}
		if code != 1 || stdout != abs+"\n" || stderr != "docs-cli: gemini autoparse rejected: status 500\n" {
			t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
		}
		if len(caps) != 2 {
			t.Fatalf("gemini calls %d", len(caps))
		}
		assertAutoparseRequest(t, &caps[0], apiKey, string(aBody))
		if strings.Contains(caps[0].body, "B-JOB") || strings.Contains(caps[0].body, "C-JOB") || strings.Contains(caps[1].body, "C-JOB") {
			t.Fatalf("bodies %q %q", caps[0].body, caps[1].body)
		}
		moved, err := os.ReadFile(filepath.Join(dir, "batch", "jobads", "parsed", "a.txt"))
		if err != nil || string(moved) != string(aBody) {
			t.Fatalf("moved %q err %v", moved, err)
		}
		if got, err := os.ReadFile(filepath.Join(jobads, "b.txt")); err != nil || string(got) != string(bBody) {
			t.Fatalf("b.txt %q err %v", got, err)
		}
		if got, err := os.ReadFile(filepath.Join(jobads, "c.txt")); err != nil || string(got) != string(cBody) {
			t.Fatalf("c.txt %q err %v", got, err)
		}
		if _, err := os.Lstat(filepath.Join(dir, "batch", "jobads", "parsed", "b.json")); !os.IsNotExist(err) {
			t.Fatalf("b.json: %v", err)
		}
		assertRefreshUntouched(t)
		assertNoSecret(t, stdout, stderr, apiKey)
	})

	t.Run("no key non tty", func(t *testing.T) {
		keyring.MockInit()
		dir := t.TempDir()
		jobads := filepath.Join(dir, "batch", "jobads")
		body := []byte("A-JOB\n")
		writeAt(t, filepath.Join(jobads, "a.txt"), body)
		tty := &fakeTTY{terminal: false, secrets: []string{apiKey}}
		stdout, stderr, code := runBatch(t, dir, []string{"parse", "--batch"}, deps{
			TTY:          tty,
			GeminiClient: blockGemini(t),
		})
		if code != 1 || stdout != "" || stderr != errGeminiKeyMissing+"\n" {
			t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
		}
		if len(tty.prompts) != 0 {
			t.Fatalf("prompted %q", tty.prompts)
		}
		if got, err := os.ReadFile(filepath.Join(jobads, "a.txt")); err != nil || string(got) != string(body) {
			t.Fatalf("a.txt %q err %v", got, err)
		}
		entries, err := os.ReadDir(filepath.Join(jobads, "parsed"))
		if err != nil || len(entries) != 0 {
			t.Fatalf("parsed %#v err %v", entries, err)
		}
	})

	t.Run("asks once", func(t *testing.T) {
		keyring.MockInit()
		if err := keyring.Set("docs-cli", "refresh-token", testRefreshItem); err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		jobads := filepath.Join(dir, "batch", "jobads")
		writeAt(t, filepath.Join(jobads, "b.txt"), []byte("B-JOB\n"))
		writeAt(t, filepath.Join(jobads, "a.txt"), []byte("A-JOB\n"))
		tty := &fakeTTY{terminal: true, secrets: []string{apiKey}}
		var caps []geminiCapture
		stdout, stderr, code := runBatch(t, dir, []string{"parse", "--batch"}, deps{
			TTY: tty,
			GeminiClient: scriptedGemini(t, &caps,
				geminiStep{status: http.StatusOK, body: autoparseDocEnvelope(t, "Alpha", "Write today")},
				geminiStep{status: http.StatusOK, body: autoparseDocEnvelope(t, "Beta", "Buy supplies")},
			),
		})
		if code != 0 || stderr != errGeminiStored+"\n" {
			t.Fatalf("exit %d stderr %q stdout %q", code, stderr, stdout)
		}
		if len(tty.prompts) != 1 || tty.prompts[0] != geminiKeyPrompt {
			t.Fatalf("prompts %q", tty.prompts)
		}
		if len(caps) != 2 {
			t.Fatalf("gemini calls %d", len(caps))
		}
		if _, err := os.Lstat(filepath.Join(jobads, "a.txt")); !os.IsNotExist(err) {
			t.Fatalf("a.txt: %v", err)
		}
		if _, err := os.Lstat(filepath.Join(jobads, "b.txt")); !os.IsNotExist(err) {
			t.Fatalf("b.txt: %v", err)
		}
		raw, err := keyring.Get("docs-cli", "gemini-api-key")
		if err != nil || raw != `{"api_key":"`+apiKey+`"}` {
			t.Fatalf("stored %q err %v", raw, err)
		}
		assertRefreshUntouched(t)
		assertNoSecret(t, stdout, stderr, apiKey)
	})

	t.Run("txt still writes stdout", func(t *testing.T) {
		keyring.MockInit()
		if err := keyring.Set("docs-cli", "refresh-token", testRefreshItem); err != nil {
			t.Fatal(err)
		}
		if err := jobad.StoreGeminiKey(apiKey); err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		body := []byte("JOB-DETAILS-SENTINEL\nBuy supplies\n")
		writeAt(t, filepath.Join(dir, "job.txt"), body)
		var caps []geminiCapture
		stdout, stderr, code := runBatch(t, dir, []string{"parse", "--txt", "job.txt"}, deps{
			GeminiClient: scriptedGemini(t, &caps, geminiStep{status: http.StatusOK, body: autoparseDocEnvelope(t, "Supplies Supervisor", "Buy supplies")}),
		})
		if code != 0 || stderr != "" {
			t.Fatalf("exit %d stderr %q stdout %s", code, stderr, stdout)
		}
		want := jobad.Document{
			Source:       "job.txt",
			Header:       jobad.Header{TitleBlock: "Supplies Supervisor"},
			Benefits:     []string{},
			Duties:       []string{"Buy supplies"},
			Requirements: []string{"Know stores"},
			Trailer:      []string{},
		}
		var buf bytes.Buffer
		if err := writeIndentedJSON(&buf, want); err != nil {
			t.Fatal(err)
		}
		if stdout != buf.String() {
			t.Fatalf("stdout %s", stdout)
		}
		kept, err := os.ReadFile(filepath.Join(dir, "job.txt"))
		if err != nil || string(kept) != string(body) {
			t.Fatalf("file %q err %v", kept, err)
		}
		if _, err := os.Lstat(filepath.Join(dir, "batch")); !os.IsNotExist(err) {
			t.Fatalf("batch created: %v", err)
		}
		if len(caps) != 1 {
			t.Fatalf("gemini calls %d", len(caps))
		}
		assertRefreshUntouched(t)
		assertNoSecret(t, stdout, stderr, apiKey)
	})

	t.Run("batch is a file", func(t *testing.T) {
		keyring.MockInit()
		dir := t.TempDir()
		body := []byte("not-a-dir")
		writeAt(t, filepath.Join(dir, "batch"), body)
		stdout, stderr, code := runBatch(t, dir, []string{"parse", "--batch"}, deps{
			GeminiClient: blockGemini(t),
		})
		if code != 1 || stdout != "" || !strings.HasPrefix(stderr, "docs-cli: error: ") {
			t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
		}
		got, err := os.ReadFile(filepath.Join(dir, "batch"))
		if err != nil || string(got) != string(body) {
			t.Fatalf("batch %q err %v", got, err)
		}
	})

	t.Run("parsed is a file", func(t *testing.T) {
		keyring.MockInit()
		dir := t.TempDir()
		jobads := filepath.Join(dir, "batch", "jobads")
		body := []byte("A-JOB\n")
		marker := []byte("parsed-file")
		writeAt(t, filepath.Join(jobads, "a.txt"), body)
		writeAt(t, filepath.Join(jobads, "parsed"), marker)
		stdout, stderr, code := runBatch(t, dir, []string{"parse", "--batch"}, deps{
			GeminiClient: blockGemini(t),
		})
		if code != 1 || stdout != "" || !strings.HasPrefix(stderr, "docs-cli: error: ") {
			t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
		}
		if got, err := os.ReadFile(filepath.Join(jobads, "a.txt")); err != nil || string(got) != string(body) {
			t.Fatalf("a.txt %q err %v", got, err)
		}
		if got, err := os.ReadFile(filepath.Join(jobads, "parsed")); err != nil || string(got) != string(marker) {
			t.Fatalf("parsed %q err %v", got, err)
		}
	})

	t.Run("parsed not writable", func(t *testing.T) {
		keyring.MockInit()
		if err := jobad.StoreGeminiKey(apiKey); err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		jobads := filepath.Join(dir, "batch", "jobads")
		parsed := filepath.Join(jobads, "parsed")
		body := []byte("A-JOB\n")
		writeAt(t, filepath.Join(jobads, "a.txt"), body)
		if err := os.MkdirAll(parsed, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(parsed, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = os.Chmod(parsed, 0o700)
		})
		var caps []geminiCapture
		stdout, stderr, code := runBatch(t, dir, []string{"parse", "--batch"}, deps{
			GeminiClient: scriptedGemini(t, &caps, geminiStep{status: http.StatusOK, body: autoparseDocEnvelope(t, "Alpha", "Write today")}),
		})
		if code != 1 || stdout != "" || !strings.HasPrefix(stderr, "docs-cli: error: ") {
			t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
		}
		if len(caps) != 1 {
			t.Fatalf("gemini calls %d", len(caps))
		}
		if got, err := os.ReadFile(filepath.Join(jobads, "a.txt")); err != nil || string(got) != string(body) {
			t.Fatalf("a.txt %q err %v", got, err)
		}
		entries, err := os.ReadDir(parsed)
		if err != nil || len(entries) != 0 {
			t.Fatalf("parsed %#v err %v", entries, err)
		}
	})

	if len(fmCalls) != 0 {
		t.Fatalf("fm calls %d", len(fmCalls))
	}
}

func TestAnalyzeCommandFakeFM(t *testing.T) {
	keyring.MockInit()
	const apiKey = "analyze-must-not-read-key"
	if err := keyring.Set("docs-cli", "refresh-token", testRefreshItem); err != nil {
		t.Fatal(err)
	}
	if err := jobad.StoreGeminiKey(apiKey); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	t.Chdir(dir)
	doc := jobad.Document{
		Source: "source-token-9f3a.html",
		Header: jobad.Header{
			TitleBlock:     "System Analyst",
			Location:       "LOCATION-TOKEN",
			WorkType:       "WORKTYPE-TOKEN",
			Classification: "CLASS-TOKEN",
		},
		Benefits:     []string{"Medical person@example.com"},
		Duties:       []string{},
		Requirements: []string{"Know Go", "React (For Frontend)"},
		Trailer:      []string{"TRAILER-SENTINEL"},
	}
	path := writeJobJSON(t, doc)
	text, err := jobad.PromptText(doc)
	if err != nil {
		t.Fatal(err)
	}
	model := `{"must_have":["Know Go"],"advantage":[],"min_years":null,"degree":null,"languages":[],"tech":[{"name":"R&D","level":"must","applies_to":"For Frontend"}],"soft_skills":[]}`
	var calls [][]string
	scriptedFM(t, &calls,
		fmStep{stdout: "not-analysis", stderr: "available noise"},
		fmStep{stdout: model + "\n", stderr: "respond noise"},
	)
	var stdout, stderr bytes.Buffer
	d := deps{
		Stdin:        strings.NewReader(apiKey),
		Stdout:       &stdout,
		Stderr:       &stderr,
		Root:         dir,
		Environ:      []string{"APP_ENV=production"},
		GeminiClient: blockGemini(t),
	}
	forbidEnv(t, &d)
	code := run([]string{"analyze", "--json", path}, d)
	if code != 0 {
		t.Fatalf("exit %d stderr %s", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr %q", stderr.String())
	}
	const want = `{
  "must_have": [
    "Know Go"
  ],
  "advantage": [],
  "min_years": null,
  "degree": null,
  "languages": [],
  "tech": [
    {
      "name": "R&D",
      "level": "must",
      "applies_to": "For Frontend"
    }
  ],
  "soft_skills": []
}
`
	if stdout.String() != want {
		t.Fatalf("stdout =\n%s\nwant =\n%s", stdout.String(), want)
	}
	if strings.Contains(stdout.String(), `\u0026`) || !strings.Contains(stdout.String(), "R&D") {
		t.Fatalf("stdout %s", stdout.String())
	}
	if len(calls) != 2 || !slices.Equal(calls[0], []string{"/usr/bin/fm", "available"}) {
		t.Fatalf("available %#v", calls)
	}
	wantArgs := []string{
		"/usr/bin/fm", "respond", "--no-stream", "--use-case", "general",
		"--instructions", jobad.AnalysisInstructions,
		"--schema", jobad.AnalysisSchema,
		"--text", text,
	}
	if !slices.Equal(calls[1], wantArgs) {
		t.Fatalf("respond %#v\nwant %#v", calls[1], wantArgs)
	}
	for _, arg := range calls[1] {
		for _, banned := range []string{apiKey, "TRAILER-SENTINEL", "source-token-9f3a.html", "person@example.com", "LOCATION-TOKEN", "WORKTYPE-TOKEN", "CLASS-TOKEN"} {
			if strings.Contains(arg, banned) {
				t.Fatalf("argv contains %q\n%s", banned, arg)
			}
		}
	}
	assertNoSecret(t, stdout.String(), stderr.String(), apiKey)
	assertRefreshUntouched(t)
	raw, err := keyring.Get("docs-cli", "gemini-api-key")
	if err != nil || raw != `{"api_key":"`+apiKey+`"}` {
		t.Fatalf("gemini key %q err %v", raw, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("temp dir has %d entries", len(entries))
	}
}

func TestAnalyzeSkipsFMWhenRequirementsEmpty(t *testing.T) {
	keyring.MockInit()
	if err := jobad.StoreGeminiKey("analyze-gemini-key"); err != nil {
		t.Fatal(err)
	}
	calls := 0
	jobad.SetFMExecForTest(func(argv []string) ([]byte, []byte, int) {
		calls++
		t.Errorf("fm called %q", argv)
		return nil, []byte("fm called"), 1
	})
	t.Cleanup(restoreFMStub)
	docs := []jobad.Document{
		{Requirements: []string{}, Benefits: []string{"Medical"}, Trailer: []string{"TRAILER-SENTINEL"}},
		{Requirements: []string{"person@example.com"}},
		{},
	}
	for _, doc := range docs {
		path := writeJobJSON(t, doc)
		stdout, stderr, code := runAnalyzeArgs(t, path)
		if code != 1 || stdout != "" || stderr != "docs-cli: requirements is empty\n" {
			t.Fatalf("doc %#v exit %d stdout %q stderr %q", doc.Requirements, code, stdout, stderr)
		}
	}
	if calls != 0 {
		t.Fatalf("calls %d", calls)
	}
}

func TestAnalyzeFMAvailableFails(t *testing.T) {
	keyring.MockInit()
	path := writeJobJSON(t, jobad.Document{Requirements: []string{"Know Go"}})

	t.Run("available", func(t *testing.T) {
		var calls [][]string
		scriptedFM(t, &calls, fmStep{stdout: `{"must_have":[]}`, stderr: "not licensed\n", code: 1})
		stdout, stderr, code := runAnalyzeArgs(t, path)
		if code != 1 || stdout != "" || stderr != "docs-cli: fm: not licensed\n" {
			t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
		}
		if len(calls) != 1 || !slices.Equal(calls[0], []string{"/usr/bin/fm", "available"}) {
			t.Fatalf("calls %#v", calls)
		}
	})

	t.Run("respond", func(t *testing.T) {
		var calls [][]string
		scriptedFM(t, &calls,
			fmStep{},
			fmStep{stdout: `{"must_have":["Go"]}`, stderr: "guardrail rejected\n", code: 1},
		)
		stdout, stderr, code := runAnalyzeArgs(t, path)
		if code != 1 || stdout != "" || stderr != "docs-cli: fm: guardrail rejected\n" {
			t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
		}
		if len(calls) != 2 || calls[1][1] != "respond" {
			t.Fatalf("calls %#v", calls)
		}
	})
}

func TestAnalyzeRejectsModelProse(t *testing.T) {
	keyring.MockInit()
	if err := jobad.StoreGeminiKey("analyze-gemini-key"); err != nil {
		t.Fatal(err)
	}
	path := writeJobJSON(t, jobad.Document{Requirements: []string{"Know Go"}})
	var calls [][]string
	scriptedFM(t, &calls,
		fmStep{},
		fmStep{stdout: "The candidate needs Go.\n"},
	)
	stdout, stderr, code := runAnalyzeArgs(t, path)
	if code != 1 || stdout != "" || stderr != "docs-cli: fm output is not the analysis schema\n" {
		t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	if len(calls) != 2 || calls[1][1] != "respond" {
		t.Fatalf("calls %#v", calls)
	}
}

func TestAnalyzeUsesGeminiWhenFMFails(t *testing.T) {
	const apiKey = "analyze-gemini-key"
	const company = "COMPANY-TOKEN"
	model := `{"must_have":["Know Go"],"advantage":[],"min_years":null,"degree":null,"languages":[],"tech":[{"name":"R&D","level":"must","applies_to":"For Frontend"}],"soft_skills":[]}`
	const want = `{
  "must_have": [
    "Know Go"
  ],
  "advantage": [],
  "min_years": null,
  "degree": null,
  "languages": [],
  "tech": [
    {
      "name": "R&D",
      "level": "must",
      "applies_to": "For Frontend"
    }
  ],
  "soft_skills": []
}
`
	doc := jobad.Document{
		Source: "source-token-9f3a.html",
		Header: jobad.Header{
			TitleBlock:     "System Analyst",
			Company:        company,
			Location:       "LOCATION-TOKEN",
			WorkType:       "WORKTYPE-TOKEN",
			Classification: "CLASS-TOKEN",
		},
		Benefits:     []string{"Medical person@example.com"},
		Requirements: []string{"Know Go"},
		Trailer:      []string{"TRAILER-SENTINEL"},
	}

	runCase := func(t *testing.T, steps ...fmStep) {
		t.Helper()
		keyring.MockInit()
		if err := keyring.Set("docs-cli", "refresh-token", testRefreshItem); err != nil {
			t.Fatal(err)
		}
		if err := jobad.StoreGeminiKey(apiKey); err != nil {
			t.Fatal(err)
		}
		path := writeJobJSON(t, doc)
		var calls [][]string
		scriptedFM(t, &calls, steps...)
		hits := 0
		cap := &geminiCapture{}
		tty := &fakeTTY{terminal: true, secrets: []string{apiKey}}
		var stdout, stderr bytes.Buffer
		d := deps{
			Stdin:        strings.NewReader(apiKey + "\n"),
			Stdout:       &stdout,
			Stderr:       &stderr,
			Root:         t.TempDir(),
			Environ:      []string{"APP_ENV=production"},
			TTY:          tty,
			GeminiClient: countingGemini(t, http.StatusOK, analysisEnvelope(t, model), &hits, cap),
		}
		forbidEnv(t, &d)
		code := run([]string{"analyze", "--json", path}, d)
		if code != 0 || stderr.Len() != 0 || stdout.String() != want {
			t.Fatalf("exit %d stderr %q stdout\n%s", code, stderr.String(), stdout.String())
		}
		if hits != 1 {
			t.Fatalf("gemini hits %d", hits)
		}
		if len(tty.prompts) != 0 {
			t.Fatalf("prompted %q", tty.prompts)
		}
		for _, banned := range []string{apiKey, company, "LOCATION-TOKEN", "WORKTYPE-TOKEN", "CLASS-TOKEN", "TRAILER-SENTINEL", "source-token-9f3a.html", "person@example.com"} {
			if strings.Contains(cap.body, banned) || strings.Contains(cap.url, banned) {
				t.Fatalf("request contains %q\n%s", banned, cap.body)
			}
		}
		if cap.header.Get("x-goog-api-key") != apiKey {
			t.Fatalf("header %q", cap.header.Get("x-goog-api-key"))
		}
		assertNoSecret(t, stdout.String(), stderr.String(), apiKey)
		assertRefreshUntouched(t)
		if len(calls) != len(steps) {
			t.Fatalf("fm calls %#v", calls)
		}
	}

	t.Run("available", func(t *testing.T) {
		runCase(t, fmStep{stderr: "not licensed\n", code: 1})
	})
	t.Run("respond", func(t *testing.T) {
		runCase(t, fmStep{}, fmStep{stderr: "guardrail rejected\n", code: 1})
	})
}

func TestAnalyzeFMSuccessSkipsGemini(t *testing.T) {
	keyring.MockInit()
	const apiKey = "analyze-gemini-key"
	if err := keyring.Set("docs-cli", "refresh-token", testRefreshItem); err != nil {
		t.Fatal(err)
	}
	if err := jobad.StoreGeminiKey(apiKey); err != nil {
		t.Fatal(err)
	}
	path := writeJobJSON(t, jobad.Document{Requirements: []string{"Know Go"}})
	fmModel := `{"must_have":["FROM-FM"],"advantage":[],"min_years":null,"degree":null,"languages":[],"tech":[],"soft_skills":[]}`
	geminiModel := `{"must_have":["FROM-GEMINI"],"advantage":[],"min_years":null,"degree":null,"languages":[],"tech":[],"soft_skills":[]}`
	var calls [][]string
	scriptedFM(t, &calls, fmStep{}, fmStep{stdout: fmModel + "\n"})
	hits := 0
	stdout, stderr, code := runAnalyzeClient(t, path, countingGemini(t, http.StatusOK, analysisEnvelope(t, geminiModel), &hits, &geminiCapture{}))
	if code != 0 || stderr != "" || hits != 0 || !strings.Contains(stdout, "FROM-FM") || strings.Contains(stdout, "FROM-GEMINI") {
		t.Fatalf("exit %d hits %d stdout %s stderr %q", code, hits, stdout, stderr)
	}
	if len(calls) != 2 {
		t.Fatalf("calls %#v", calls)
	}
	assertRefreshUntouched(t)
}

func TestAnalyzeGeminiRejectsBadLevel(t *testing.T) {
	keyring.MockInit()
	const apiKey = "analyze-gemini-key"
	if err := jobad.StoreGeminiKey(apiKey); err != nil {
		t.Fatal(err)
	}
	path := writeJobJSON(t, jobad.Document{
		Header:       jobad.Header{Company: "COMPANY-TOKEN"},
		Requirements: []string{"Know Go"},
	})
	var calls [][]string
	scriptedFM(t, &calls, fmStep{stderr: "not licensed\n", code: 1})
	bad := `{"must_have":[],"advantage":[],"min_years":null,"degree":null,"languages":[],"tech":[{"name":"Go","level":"expert","applies_to":null}],"soft_skills":[]}`
	stdout, stderr, code := runAnalyzeClient(t, path, mappingClient(t, http.StatusOK, analysisEnvelope(t, bad), &geminiCapture{}))
	if code != 1 || stdout != "" || stderr != "docs-cli: gemini analysis rejected: response\n" {
		t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	if strings.Contains(stderr, "docs-cli: fm:") || strings.Contains(stderr, apiKey) || strings.Contains(stderr, "expert") || strings.Contains(stderr, "COMPANY-TOKEN") {
		t.Fatalf("stderr %q", stderr)
	}
	if len(calls) != 1 {
		t.Fatalf("calls %#v", calls)
	}
}

func TestAnalyzeNoKeyDoesNotCallGemini(t *testing.T) {
	keyring.MockInit()
	path := writeJobJSON(t, jobad.Document{Requirements: []string{"Know Go"}})
	var calls [][]string
	scriptedFM(t, &calls, fmStep{stderr: "not licensed\n", code: 1})
	hits := 0
	tty := &fakeTTY{terminal: true, secrets: []string{"should-not-ask"}}
	var stdout, stderr bytes.Buffer
	d := deps{
		Stdin:        strings.NewReader("should-not-ask\n"),
		Stdout:       &stdout,
		Stderr:       &stderr,
		Root:         t.TempDir(),
		Environ:      []string{"APP_ENV=production"},
		TTY:          tty,
		GeminiClient: countingGemini(t, http.StatusOK, analysisEnvelope(t, `{"must_have":["Know Go"],"advantage":[],"min_years":null,"degree":null,"languages":[],"tech":[],"soft_skills":[]}`), &hits, &geminiCapture{}),
	}
	forbidEnv(t, &d)
	code := run([]string{"analyze", "--json", path}, d)
	if code != 1 || stdout.Len() != 0 || stderr.String() != "docs-cli: fm: not licensed\n" || hits != 0 {
		t.Fatalf("exit %d hits %d stdout %q stderr %q", code, hits, stdout.String(), stderr.String())
	}
	if len(tty.prompts) != 0 {
		t.Fatalf("prompted %q", tty.prompts)
	}
	if _, err := keyring.Get("docs-cli", "gemini-api-key"); !errors.Is(err, keyring.ErrNotFound) {
		t.Fatalf("key stored, err %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("calls %#v", calls)
	}
}

func TestAnalyzeWithGeminiFlag(t *testing.T) {
	const apiKey = "analyze-gemini-key"
	const company = "COMPANY-TOKEN"
	model := `{"must_have":["Know Go"],"advantage":[],"min_years":null,"degree":null,"languages":[],"tech":[{"name":"R&D","level":"must","applies_to":"For Frontend"}],"soft_skills":[]}`
	doc := jobad.Document{
		Source: "source-token-9f3a.html",
		Header: jobad.Header{
			TitleBlock:     "System Analyst",
			Company:        company,
			Location:       "LOCATION-TOKEN",
			WorkType:       "WORKTYPE-TOKEN",
			Classification: "CLASS-TOKEN",
		},
		Benefits:     []string{"Medical person@example.com"},
		Requirements: []string{"Know Go"},
		Trailer:      []string{"TRAILER-SENTINEL"},
	}
	want := string(analysisFileBytes(t, model))

	t.Run("writes analysis without fm", func(t *testing.T) {
		keyring.MockInit()
		if err := keyring.Set("docs-cli", "refresh-token", testRefreshItem); err != nil {
			t.Fatal(err)
		}
		if err := jobad.StoreGeminiKey(apiKey); err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		body := jobFileBytes(t, doc)
		path := writeAt(t, filepath.Join(dir, "jd.json"), body)
		var calls [][]string
		scriptedFM(t, &calls, fmStep{stdout: `{"must_have":["FROM-FM"],"advantage":[],"min_years":null,"degree":null,"languages":[],"tech":[],"soft_skills":[]}`})
		hits := 0
		cap := &geminiCapture{}
		tty := &fakeTTY{terminal: true, secrets: []string{apiKey}}
		stdout, stderr, code := runBatch(t, dir, []string{"analyze", "--json", path, "--gemini"}, deps{
			TTY:          tty,
			GeminiClient: countingGemini(t, http.StatusOK, analysisEnvelope(t, model), &hits, cap),
		})
		if code != 0 || stderr != "" || stdout != want {
			t.Fatalf("exit %d stderr %q stdout\n%s", code, stderr, stdout)
		}
		if hits != 1 || len(calls) != 0 || len(tty.prompts) != 0 {
			t.Fatalf("hits %d calls %#v prompts %q", hits, calls, tty.prompts)
		}
		if strings.Contains(stdout, "FROM-FM") || strings.Contains(stdout, "jd-tech.md") || strings.Contains(stdout, `\u0026`) {
			t.Fatalf("stdout %s", stdout)
		}
		for _, banned := range []string{apiKey, company, "LOCATION-TOKEN", "WORKTYPE-TOKEN", "CLASS-TOKEN", "TRAILER-SENTINEL", "source-token-9f3a.html", "person@example.com"} {
			if strings.Contains(cap.body, banned) || strings.Contains(cap.url, banned) {
				t.Fatalf("request contains %q\n%s", banned, cap.body)
			}
		}
		if cap.header.Get("x-goog-api-key") != apiKey || cap.header.Get("Authorization") != "" {
			t.Fatalf("header %#v", cap.header)
		}
		diagram, err := os.ReadFile(filepath.Join(dir, "jd-tech.md"))
		if err != nil || !bytes.Contains(diagram, []byte("flowchart LR")) || !bytes.Contains(diagram, []byte("R&D")) {
			t.Fatalf("diagram %q err %v", diagram, err)
		}
		assertPerm(t, filepath.Join(dir, "jd-tech.md"), 0o600)
		assertPerm(t, filepath.Join(dir, "jd-tech-dist.md"), 0o600)
		assertAbsent(t, filepath.Join(dir, "jd-tech.svg"))
		assertAbsent(t, filepath.Join(dir, "batch"))
		assertFileBytes(t, path, body)
		assertRefreshUntouched(t)
		assertNoSecret(t, stdout, stderr, apiKey)
	})

	t.Run("false still uses fm", func(t *testing.T) {
		keyring.MockInit()
		if err := jobad.StoreGeminiKey(apiKey); err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		path := writeAt(t, filepath.Join(dir, "jd.json"), jobFileBytes(t, doc))
		fmModel := `{"must_have":["FROM-FM"],"advantage":[],"min_years":null,"degree":null,"languages":[],"tech":[],"soft_skills":[]}`
		var calls [][]string
		scriptedFM(t, &calls, fmStep{}, fmStep{stdout: fmModel + "\n"})
		hits := 0
		stdout, stderr, code := runBatch(t, dir, []string{"analyze", "--gemini=false", "--json", path}, deps{
			GeminiClient: countingGemini(t, http.StatusOK, analysisEnvelope(t, model), &hits, &geminiCapture{}),
		})
		if code != 0 || stderr != "" || hits != 0 || len(calls) != 2 || !strings.Contains(stdout, "FROM-FM") || strings.Contains(stdout, "Know Go") {
			t.Fatalf("exit %d hits %d calls %d stdout %s stderr %q", code, hits, len(calls), stdout, stderr)
		}
	})

	t.Run("missing key", func(t *testing.T) {
		keyring.MockInit()
		dir := t.TempDir()
		path := writeAt(t, filepath.Join(dir, "jd.json"), jobFileBytes(t, doc))
		var calls [][]string
		scriptedFM(t, &calls, fmStep{stdout: model})
		hits := 0
		tty := &fakeTTY{terminal: true, secrets: []string{"should-not-ask"}}
		stdout, stderr, code := runBatch(t, dir, []string{"analyze", "--gemini", "--json", path}, deps{
			Stdin:        strings.NewReader("should-not-ask\n"),
			TTY:          tty,
			GeminiClient: countingGemini(t, http.StatusOK, analysisEnvelope(t, model), &hits, &geminiCapture{}),
		})
		if code != 1 || stdout != "" || stderr != "docs-cli: gemini api key is not in the keyring\n" || hits != 0 || len(calls) != 0 || len(tty.prompts) != 0 {
			t.Fatalf("exit %d hits %d calls %#v prompts %q stdout %q stderr %q", code, hits, calls, tty.prompts, stdout, stderr)
		}
		if _, err := keyring.Get("docs-cli", "gemini-api-key"); !errors.Is(err, keyring.ErrNotFound) {
			t.Fatalf("key stored, err %v", err)
		}
		assertAbsent(t, filepath.Join(dir, "jd-tech.md"))
		assertAbsent(t, filepath.Join(dir, "jd-tech-dist.md"))
	})

	t.Run("invalid key", func(t *testing.T) {
		keyring.MockInit()
		if err := keyring.Set("docs-cli", "gemini-api-key", `{"api_key":""}`); err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		path := writeAt(t, filepath.Join(dir, "jd.json"), jobFileBytes(t, doc))
		var calls [][]string
		scriptedFM(t, &calls)
		hits := 0
		stdout, stderr, code := runBatch(t, dir, []string{"analyze", "--json", path, "--gemini"}, deps{
			GeminiClient: countingGemini(t, http.StatusOK, analysisEnvelope(t, model), &hits, &geminiCapture{}),
		})
		if code != 1 || stdout != "" || stderr != "docs-cli: keyring item is invalid; run docs-cli gemini-key\n" || hits != 0 || len(calls) != 0 {
			t.Fatalf("exit %d hits %d calls %#v stdout %q stderr %q", code, hits, calls, stdout, stderr)
		}
	})

	t.Run("keyring unavailable", func(t *testing.T) {
		const secret = "keyring-boom-secret"
		keyring.MockInitWithError(errors.New(secret))
		t.Cleanup(func() { keyring.MockInit() })
		dir := t.TempDir()
		path := writeAt(t, filepath.Join(dir, "jd.json"), jobFileBytes(t, doc))
		var calls [][]string
		scriptedFM(t, &calls)
		hits := 0
		stdout, stderr, code := runBatch(t, dir, []string{"analyze", "--json", path, "--gemini"}, deps{
			GeminiClient: countingGemini(t, http.StatusOK, analysisEnvelope(t, model), &hits, &geminiCapture{}),
		})
		if code != 1 || stdout != "" || stderr != "docs-cli: keyring unavailable\n" || hits != 0 || len(calls) != 0 || strings.Contains(stderr, secret) || strings.Contains(stderr, "docs-cli: fm:") {
			t.Fatalf("exit %d hits %d calls %#v stdout %q stderr %q", code, hits, calls, stdout, stderr)
		}
	})

	t.Run("empty requirements", func(t *testing.T) {
		keyring.MockInitWithError(errors.New("do-not-read-gemini"))
		t.Cleanup(func() { keyring.MockInit() })
		dir := t.TempDir()
		path := writeAt(t, filepath.Join(dir, "jd.json"), jobFileBytes(t, jobad.Document{Requirements: []string{"person@example.com"}}))
		var calls [][]string
		scriptedFM(t, &calls)
		hits := 0
		stdout, stderr, code := runBatch(t, dir, []string{"analyze", "--gemini", "--json", path}, deps{
			GeminiClient: countingGemini(t, http.StatusOK, analysisEnvelope(t, model), &hits, &geminiCapture{}),
		})
		if code != 1 || stdout != "" || stderr != "docs-cli: requirements is empty\n" || hits != 0 || len(calls) != 0 || strings.Contains(stderr, "keyring") {
			t.Fatalf("exit %d hits %d calls %#v stdout %q stderr %q", code, hits, calls, stdout, stderr)
		}
	})

	t.Run("bad level", func(t *testing.T) {
		keyring.MockInit()
		if err := jobad.StoreGeminiKey(apiKey); err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		path := writeAt(t, filepath.Join(dir, "jd.json"), jobFileBytes(t, doc))
		var calls [][]string
		scriptedFM(t, &calls, fmStep{stdout: model})
		bad := `{"must_have":[],"advantage":[],"min_years":null,"degree":null,"languages":[],"tech":[{"name":"Go","level":"expert","applies_to":null}],"soft_skills":[]}`
		stdout, stderr, code := runBatch(t, dir, []string{"analyze", "--json", path, "--gemini"}, deps{
			GeminiClient: mappingClient(t, http.StatusOK, analysisEnvelope(t, bad), &geminiCapture{}),
		})
		if code != 1 || stdout != "" || stderr != "docs-cli: gemini analysis rejected: response\n" || len(calls) != 0 || strings.Contains(stderr, "docs-cli: fm:") || strings.Contains(stderr, "expert") {
			t.Fatalf("exit %d calls %#v stdout %q stderr %q", code, calls, stdout, stderr)
		}
		assertAbsent(t, filepath.Join(dir, "jd-tech.md"))
	})

	t.Run("status 503", func(t *testing.T) {
		keyring.MockInit()
		if err := jobad.StoreGeminiKey(apiKey); err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		path := writeAt(t, filepath.Join(dir, "jd.json"), jobFileBytes(t, doc))
		var calls [][]string
		scriptedFM(t, &calls, fmStep{stdout: model})
		const detail = "This model is currently experiencing high demand. Spikes in demand are usually temporary. Please try again later."
		body := `{"error":{"status":"UNAVAILABLE","message":"` + detail + `"}}`
		stdout, stderr, code := runBatch(t, dir, []string{"analyze", "--gemini", "--json", path}, deps{
			GeminiClient: mappingClient(t, http.StatusServiceUnavailable, body, &geminiCapture{}),
		})
		wantErr := "docs-cli: gemini analysis rejected: status 503\n" +
			"docs-cli: gemini analysis rejected: UNAVAILABLE: " + detail + "\n"
		if code != 1 || stdout != "" || stderr != wantErr || len(calls) != 0 || strings.Contains(stderr, "docs-cli: fm:") || strings.Contains(stderr, apiKey) {
			t.Fatalf("exit %d calls %#v stdout %q stderr %q", code, calls, stdout, stderr)
		}
		assertAbsent(t, filepath.Join(dir, "jd-tech.md"))
		assertAbsent(t, filepath.Join(dir, "jd-tech-dist.md"))
	})

	t.Run("existing diagram skips http", func(t *testing.T) {
		keyring.MockInit()
		if err := jobad.StoreGeminiKey(apiKey); err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		body := jobFileBytes(t, doc)
		path := writeAt(t, filepath.Join(dir, "jd.json"), body)
		diagramPath := filepath.Join(dir, "jd-tech.md")
		marker := []byte("already-md")
		writeAt(t, diagramPath, marker)
		var calls [][]string
		scriptedFM(t, &calls, fmStep{stdout: model})
		hits := 0
		stdout, stderr, code := runBatch(t, dir, []string{"analyze", "--json", path, "--gemini"}, deps{
			GeminiClient: countingGemini(t, http.StatusOK, analysisEnvelope(t, model), &hits, &geminiCapture{}),
		})
		wantErr := "docs-cli: tech diagram already exists: " + diagramPath + "\n"
		if code != 1 || stdout != "" || stderr != wantErr || hits != 0 || len(calls) != 0 {
			t.Fatalf("exit %d hits %d calls %#v stdout %q stderr %q", code, hits, calls, stdout, stderr)
		}
		assertFileBytes(t, diagramPath, marker)
		assertFileBytes(t, path, body)
		assertAbsent(t, filepath.Join(dir, "jd-tech-dist.md"))
	})

	t.Run("batch", func(t *testing.T) {
		keyring.MockInit()
		if err := keyring.Set("docs-cli", "refresh-token", testRefreshItem); err != nil {
			t.Fatal(err)
		}
		if err := jobad.StoreGeminiKey(apiKey); err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		parsed := makeParsed(t, dir)
		analyzed := filepath.Join(dir, "batch", "jobads", "analyzed")
		aBody := jobFileBytes(t, batchJob("Know A"))
		bBody := jobFileBytes(t, batchJob("Know B"))
		writeAt(t, filepath.Join(parsed, "a.json"), aBody)
		writeAt(t, filepath.Join(parsed, "b.json"), bBody)
		var calls [][]string
		scriptedFM(t, &calls, fmStep{stdout: batchModel("FROM-FM")})
		hits := 0
		var caps []geminiCapture
		stdout, stderr, code := runBatch(t, dir, []string{"analyze", "--batch", "--gemini"}, deps{
			GeminiClient: sequenceGemini(t, &hits, &caps, []geminiHTTPStep{
				{status: http.StatusOK, body: analysisEnvelope(t, batchModel("Know A"))},
				{status: http.StatusOK, body: analysisEnvelope(t, batchModel("Know B"))},
			}),
		})
		aAbs, err := filepath.Abs(filepath.Join(analyzed, "a-analysis.json"))
		if err != nil {
			t.Fatal(err)
		}
		aMD, err := filepath.Abs(filepath.Join(analyzed, "a-tech.md"))
		if err != nil {
			t.Fatal(err)
		}
		aDist, err := filepath.Abs(filepath.Join(analyzed, "a-tech-dist.md"))
		if err != nil {
			t.Fatal(err)
		}
		bAbs, err := filepath.Abs(filepath.Join(analyzed, "b-analysis.json"))
		if err != nil {
			t.Fatal(err)
		}
		bMD, err := filepath.Abs(filepath.Join(analyzed, "b-tech.md"))
		if err != nil {
			t.Fatal(err)
		}
		bDist, err := filepath.Abs(filepath.Join(analyzed, "b-tech-dist.md"))
		if err != nil {
			t.Fatal(err)
		}
		if code != 0 || stderr != "" || stdout != aAbs+"\n"+aMD+"\n"+aDist+"\n"+bAbs+"\n"+bMD+"\n"+bDist+"\n" {
			t.Fatalf("exit %d stderr %q stdout %q", code, stderr, stdout)
		}
		if hits != 2 || len(calls) != 0 || len(caps) != 2 {
			t.Fatalf("hits %d calls %#v caps %d", hits, calls, len(caps))
		}
		for _, cap := range caps {
			if strings.Contains(cap.body, company) || strings.Contains(cap.url, apiKey) || strings.Contains(cap.body, apiKey) || cap.header.Get("x-goog-api-key") != apiKey {
				t.Fatalf("request url %s header %q body %s", cap.url, cap.header.Get("x-goog-api-key"), cap.body)
			}
		}
		assertFileBytes(t, filepath.Join(analyzed, "a-analysis.json"), analysisFileBytes(t, batchModel("Know A")))
		assertFileBytes(t, filepath.Join(analyzed, "b-analysis.json"), analysisFileBytes(t, batchModel("Know B")))
		assertFileBytes(t, filepath.Join(analyzed, "a.json"), aBody)
		assertFileBytes(t, filepath.Join(analyzed, "b.json"), bBody)
		assertPerm(t, filepath.Join(analyzed, "a-tech.md"), 0o600)
		assertPerm(t, filepath.Join(analyzed, "b-tech-dist.md"), 0o600)
		assertAbsent(t, filepath.Join(parsed, "a.json"))
		assertAbsent(t, filepath.Join(parsed, "b.json"))
		assertRefreshUntouched(t)
	})

	t.Run("batch second failure", func(t *testing.T) {
		keyring.MockInit()
		if err := jobad.StoreGeminiKey(apiKey); err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		parsed := makeParsed(t, dir)
		analyzed := filepath.Join(dir, "batch", "jobads", "analyzed")
		aBody := jobFileBytes(t, batchJob("Know A"))
		bBody := jobFileBytes(t, batchJob("Know B"))
		writeAt(t, filepath.Join(parsed, "a.json"), aBody)
		writeAt(t, filepath.Join(parsed, "b.json"), bBody)
		var calls [][]string
		scriptedFM(t, &calls, fmStep{stdout: batchModel("FROM-FM")})
		hits := 0
		const detail = "This model is currently experiencing high demand. Spikes in demand are usually temporary. Please try again later."
		stdout, stderr, code := runBatch(t, dir, []string{"analyze", "--gemini", "--batch"}, deps{
			GeminiClient: sequenceGemini(t, &hits, nil, []geminiHTTPStep{
				{status: http.StatusOK, body: analysisEnvelope(t, batchModel("Know A"))},
				{status: http.StatusServiceUnavailable, body: `{"error":{"status":"UNAVAILABLE","message":"` + detail + `"}}`},
			}),
		})
		aAbs, err := filepath.Abs(filepath.Join(analyzed, "a-analysis.json"))
		if err != nil {
			t.Fatal(err)
		}
		aMD, err := filepath.Abs(filepath.Join(analyzed, "a-tech.md"))
		if err != nil {
			t.Fatal(err)
		}
		aDist, err := filepath.Abs(filepath.Join(analyzed, "a-tech-dist.md"))
		if err != nil {
			t.Fatal(err)
		}
		wantErr := "docs-cli: gemini analysis rejected: status 503\n" +
			"docs-cli: gemini analysis rejected: UNAVAILABLE: " + detail + "\n"
		if code != 1 || stdout != aAbs+"\n"+aMD+"\n"+aDist+"\n" || stderr != wantErr || hits != 2 || len(calls) != 0 || strings.Contains(stderr, "docs-cli: fm:") {
			t.Fatalf("exit %d hits %d calls %#v stdout %q stderr %q", code, hits, calls, stdout, stderr)
		}
		assertFileBytes(t, filepath.Join(analyzed, "a-analysis.json"), analysisFileBytes(t, batchModel("Know A")))
		assertFileBytes(t, filepath.Join(analyzed, "a.json"), aBody)
		assertFileBytes(t, filepath.Join(parsed, "b.json"), bBody)
		assertAbsent(t, filepath.Join(parsed, "a.json"))
		assertAbsent(t, filepath.Join(analyzed, "b-analysis.json"))
		assertAbsent(t, filepath.Join(analyzed, "b-tech.md"))
		assertAbsent(t, filepath.Join(analyzed, "b-tech-dist.md"))
		assertAbsent(t, filepath.Join(analyzed, "b.json"))
	})

	t.Run("batch missing key", func(t *testing.T) {
		keyring.MockInit()
		dir := t.TempDir()
		parsed := makeParsed(t, dir)
		aBody := jobFileBytes(t, batchJob("Know A"))
		bBody := jobFileBytes(t, batchJob("Know B"))
		writeAt(t, filepath.Join(parsed, "a.json"), aBody)
		writeAt(t, filepath.Join(parsed, "b.json"), bBody)
		var calls [][]string
		scriptedFM(t, &calls)
		hits := 0
		tty := &fakeTTY{terminal: true, secrets: []string{"should-not-ask"}}
		stdout, stderr, code := runBatch(t, dir, []string{"analyze", "--batch", "--gemini"}, deps{
			TTY:          tty,
			GeminiClient: countingGemini(t, http.StatusOK, analysisEnvelope(t, batchModel("Know A")), &hits, &geminiCapture{}),
		})
		if code != 1 || stdout != "" || stderr != "docs-cli: gemini api key is not in the keyring\n" || hits != 0 || len(calls) != 0 || len(tty.prompts) != 0 {
			t.Fatalf("exit %d hits %d calls %#v prompts %q stdout %q stderr %q", code, hits, calls, tty.prompts, stdout, stderr)
		}
		assertFileBytes(t, filepath.Join(parsed, "a.json"), aBody)
		assertFileBytes(t, filepath.Join(parsed, "b.json"), bBody)
		assertAbsent(t, filepath.Join(dir, "batch", "jobads", "analyzed", "a-analysis.json"))
		assertAbsent(t, filepath.Join(dir, "batch", "jobads", "analyzed", "a.json"))
	})

	t.Run("batch existing diagram skips http", func(t *testing.T) {
		keyring.MockInit()
		if err := jobad.StoreGeminiKey(apiKey); err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		parsed := makeParsed(t, dir)
		analyzed := filepath.Join(dir, "batch", "jobads", "analyzed")
		if err := os.Mkdir(analyzed, 0o700); err != nil {
			t.Fatal(err)
		}
		body := jobFileBytes(t, batchJob("Know A"))
		writeAt(t, filepath.Join(parsed, "a.json"), body)
		marker := []byte("already-md")
		writeAt(t, filepath.Join(analyzed, "a-tech.md"), marker)
		var calls [][]string
		scriptedFM(t, &calls)
		hits := 0
		stdout, stderr, code := runBatch(t, dir, []string{"analyze", "--batch", "--gemini"}, deps{
			GeminiClient: countingGemini(t, http.StatusOK, analysisEnvelope(t, batchModel("Know A")), &hits, &geminiCapture{}),
		})
		wantErr := "docs-cli: analyzed file already exists: " + filepath.Join("batch", "jobads", "analyzed", "a-tech.md") + "\n"
		if code != 1 || stdout != "" || stderr != wantErr || hits != 0 || len(calls) != 0 {
			t.Fatalf("exit %d hits %d calls %#v stdout %q stderr %q", code, hits, calls, stdout, stderr)
		}
		assertFileBytes(t, filepath.Join(analyzed, "a-tech.md"), marker)
		assertFileBytes(t, filepath.Join(parsed, "a.json"), body)
		assertAbsent(t, filepath.Join(analyzed, "a-analysis.json"))
	})
}

type geminiHTTPStep struct {
	status int
	body   string
}

func sequenceGemini(t *testing.T, hits *int, caps *[]geminiCapture, steps []geminiHTTPStep) *http.Client {
	t.Helper()
	n := 0
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		*hits++
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		if caps != nil {
			*caps = append(*caps, geminiCapture{url: r.URL.String(), header: r.Header.Clone(), body: string(raw)})
		}
		if n >= len(steps) {
			t.Errorf("extra gemini call")
			return nil, errors.New("extra gemini call")
		}
		step := steps[n]
		n++
		return &http.Response{
			StatusCode: step.status,
			Body:       io.NopCloser(strings.NewReader(step.body)),
			Header:     make(http.Header),
			Request:    r,
		}, nil
	})}
}

func TestAnalyzeUsage(t *testing.T) {
	const leaked = "super-secret-gemini-key"
	jobad.SetFMExecForTest(func(argv []string) ([]byte, []byte, int) {
		t.Errorf("fm called %q", argv)
		return nil, []byte("fm called"), 1
	})
	t.Cleanup(restoreFMStub)
	tests := []struct {
		name  string
		args  []string
		exact bool
	}{
		{name: "no flag", args: []string{"analyze"}},
		{name: "gemini only", args: []string{"analyze", "--gemini"}},
		{name: "missing value", args: []string{"analyze", "--json"}},
		{name: "empty value", args: []string{"analyze", "--json", ""}},
		{name: "unknown flag", args: []string{"analyze", "--bogus"}},
		{name: "html flag", args: []string{"analyze", "--html", "page.html"}},
		{name: "extra arg", args: []string{"analyze", "--json", "job.json", "extra"}},
		{name: "help", args: []string{"analyze", "--help"}, exact: true},
		{name: "short help", args: []string{"analyze", "-h"}, exact: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keyring.MockInit()
			var stdout, stderr bytes.Buffer
			code := run(tt.args, deps{
				Stdin:  strings.NewReader(leaked),
				Stdout: &stdout,
				Stderr: &stderr,
			})
			if code != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "usage: docs-cli") {
				t.Fatalf("exit %d stdout %q stderr %q", code, stdout.String(), stderr.String())
			}
			if tt.exact && stderr.String() != usageText {
				t.Fatalf("stderr %q", stderr.String())
			}
			if strings.Contains(stdout.String()+stderr.String(), leaked) {
				t.Fatal("leaked the key")
			}
			if _, err := keyring.Get("docs-cli", "gemini-api-key"); !errors.Is(err, keyring.ErrNotFound) {
				t.Fatalf("keyring err %v", err)
			}
		})
	}
	if !strings.Contains(usageText, "\n  gemini-key\n  analyze --json PATH [--gemini]\n  analyze --batch [--gemini]\n") {
		t.Fatalf("usage commands:\n%s", usageText)
	}
	if !strings.Contains(usageText, "\n  --batch          analyze every .json file in ./batch/jobads/parsed\n  --gemini         skip the on-device model and call Gemini\n") {
		t.Fatalf("usage batch flag:\n%s", usageText)
	}
}

func TestAnalyzeJSONTooLarge(t *testing.T) {
	jobad.SetFMExecForTest(func(argv []string) ([]byte, []byte, int) {
		t.Errorf("fm called %q", argv)
		return nil, []byte("fm called"), 1
	})
	t.Cleanup(restoreFMStub)

	t.Run("over", func(t *testing.T) {
		path := writeFile(t, "over.json", bytes.Repeat([]byte{'x'}, maxJSONBytes+1))
		stdout, stderr, code := runAnalyzeArgs(t, path)
		if code != 1 || stdout != "" || stderr != errJSONTooLarge+"\n" {
			t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
		}
	})

	t.Run("exact", func(t *testing.T) {
		body := []byte(`{"requirements":[]}`)
		padded := append(body, bytes.Repeat([]byte{' '}, maxJSONBytes-len(body))...)
		path := writeFile(t, "exact.json", padded)
		stdout, stderr, code := runAnalyzeArgs(t, path)
		if code != 1 || stdout != "" || stderr != "docs-cli: requirements is empty\n" {
			t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
		}
	})

	t.Run("missing", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "missing.json")
		stdout, stderr, code := runAnalyzeArgs(t, path)
		if code != 1 || stdout != "" || !strings.Contains(stderr, "docs-cli: error:") || strings.Contains(stderr, errJSONTooLarge) {
			t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
		}
	})

	t.Run("invalid", func(t *testing.T) {
		path := writeFile(t, "bad.json", []byte(`{"requirements":`))
		stdout, stderr, code := runAnalyzeArgs(t, path)
		if code != 1 || stdout != "" || !strings.Contains(stderr, "docs-cli: error:") || strings.Contains(stderr, errJSONTooLarge) {
			t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
		}
	})
}

func batchJob(req string) jobad.Document {
	return jobad.Document{
		Source:       "source-token-9f3a.html",
		Header:       jobad.Header{TitleBlock: "System Analyst", Company: "COMPANY-TOKEN"},
		Requirements: []string{req},
		Trailer:      []string{"TRAILER-SENTINEL"},
	}
}

func batchModel(must string) string {
	return `{"must_have":["` + must + `"],"advantage":[],"min_years":null,"degree":null,"languages":[],"tech":[],"soft_skills":[]}`
}

func analysisFileBytes(t *testing.T, model string) []byte {
	t.Helper()
	parsed, err := jobad.ParseAnalysis([]byte(model))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := writeIndentedJSON(&buf, parsed); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func jobFileBytes(t *testing.T, doc jobad.Document) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := writeIndentedJSON(&buf, doc); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func makeParsed(t *testing.T, dir string) string {
	t.Helper()
	parsed := filepath.Join(dir, "batch", "jobads", "parsed")
	if err := os.MkdirAll(parsed, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(dir, "batch"),
		filepath.Join(dir, "batch", "jobads"),
		parsed,
	} {
		if err := os.Chmod(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return parsed
}

func assertAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("%s exists: %v", path, err)
	}
}

func assertFileBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("%s %q err %v", path, got, err)
	}
}

func TestAnalyzeBatch(t *testing.T) {
	const apiKey = "batch-analyze-key"

	t.Run("writes analysis and moves json", func(t *testing.T) {
		keyring.MockInit()
		if err := keyring.Set("docs-cli", "refresh-token", testRefreshItem); err != nil {
			t.Fatal(err)
		}
		if err := jobad.StoreGeminiKey(apiKey); err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		parsed := makeParsed(t, dir)
		analyzed := filepath.Join(dir, "batch", "jobads", "analyzed")
		roleBody := jobFileBytes(t, batchJob("Know Role"))
		aBody := jobFileBytes(t, batchJob("Know A"))
		keep := []byte("KEEP-TXT")
		skip := []byte("SKIP-JSON")
		notes := []byte("NOTES")
		writeAt(t, filepath.Join(parsed, "a.json"), aBody)
		writeAt(t, filepath.Join(parsed, "Role.JSON"), roleBody)
		writeAt(t, filepath.Join(parsed, "keep.txt"), keep)
		writeAt(t, filepath.Join(parsed, "nested", "skip.json"), skip)
		writeAt(t, filepath.Join(parsed, "notes.md"), notes)

		roleModel := batchModel("Know Role")
		aModel := batchModel("Know A")
		var calls [][]string
		scriptedFM(t, &calls,
			fmStep{}, fmStep{stdout: roleModel + "\n"},
			fmStep{}, fmStep{stdout: aModel + "\n"},
		)
		tty := &fakeTTY{terminal: true, secrets: []string{"should-not-ask"}}
		stdout, stderr, code := runBatch(t, dir, []string{"analyze", "--batch"}, deps{
			TTY:          tty,
			GeminiClient: blockGemini(t),
		})
		if code != 0 || stderr != "" {
			t.Fatalf("exit %d stderr %q stdout %q", code, stderr, stdout)
		}
		roleAbs, err := filepath.Abs(filepath.Join(analyzed, "Role-analysis.json"))
		if err != nil {
			t.Fatal(err)
		}
		roleSVG, err := filepath.Abs(filepath.Join(analyzed, "Role-tech.md"))
		if err != nil {
			t.Fatal(err)
		}
		roleDist, err := filepath.Abs(filepath.Join(analyzed, "Role-tech-dist.md"))
		if err != nil {
			t.Fatal(err)
		}
		aAbs, err := filepath.Abs(filepath.Join(analyzed, "a-analysis.json"))
		if err != nil {
			t.Fatal(err)
		}
		aSVG, err := filepath.Abs(filepath.Join(analyzed, "a-tech.md"))
		if err != nil {
			t.Fatal(err)
		}
		aDist, err := filepath.Abs(filepath.Join(analyzed, "a-tech-dist.md"))
		if err != nil {
			t.Fatal(err)
		}
		if stdout != roleAbs+"\n"+roleSVG+"\n"+roleDist+"\n"+aAbs+"\n"+aSVG+"\n"+aDist+"\n" {
			t.Fatalf("stdout %q", stdout)
		}
		if strings.Contains(stdout, "must_have") || strings.Contains(stdout, "COMPANY-TOKEN") {
			t.Fatalf("stdout printed json %q", stdout)
		}
		assertFileBytes(t, filepath.Join(analyzed, "Role-analysis.json"), analysisFileBytes(t, roleModel))
		assertFileBytes(t, filepath.Join(analyzed, "a-analysis.json"), analysisFileBytes(t, aModel))
		assertFileBytes(t, filepath.Join(analyzed, "Role.JSON"), roleBody)
		assertFileBytes(t, filepath.Join(analyzed, "a.json"), aBody)
		assertPerm(t, filepath.Join(analyzed, "Role-analysis.json"), 0o600)
		assertPerm(t, filepath.Join(analyzed, "a-analysis.json"), 0o600)
		assertPerm(t, filepath.Join(analyzed, "Role-tech.md"), 0o600)
		assertPerm(t, filepath.Join(analyzed, "a-tech.md"), 0o600)
		assertPerm(t, filepath.Join(analyzed, "Role-tech-dist.md"), 0o600)
		assertPerm(t, filepath.Join(analyzed, "a-tech-dist.md"), 0o600)
		for _, path := range []string{
			filepath.Join(analyzed, "Role-tech.md"),
			filepath.Join(analyzed, "a-tech.md"),
		} {
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != "(none)\n" || bytes.Contains(got, []byte("flowchart")) || bytes.Contains(got, []byte("COMPANY-TOKEN")) || bytes.Contains(got, []byte(apiKey)) {
				t.Fatalf("%s %s", path, got)
			}
		}
		assertAbsent(t, filepath.Join(analyzed, "Role-tech.svg"))
		assertAbsent(t, filepath.Join(analyzed, "a-tech.svg"))
		for _, path := range []string{
			filepath.Join(analyzed, "Role-tech-dist.md"),
			filepath.Join(analyzed, "a-tech-dist.md"),
		} {
			got, err := os.ReadFile(path)
			if err != nil || string(got) != "(none)\n" || bytes.Contains(got, []byte("quadrantChart")) || bytes.Contains(got, []byte(apiKey)) {
				t.Fatalf("%s %q err %v", path, got, err)
			}
		}
		assertAbsent(t, filepath.Join(analyzed, "Role-tech-dist.svg"))
		assertAbsent(t, filepath.Join(analyzed, "a-tech-dist.svg"))
		assertAbsent(t, filepath.Join(parsed, "Role.JSON"))
		assertAbsent(t, filepath.Join(parsed, "a.json"))
		assertFileBytes(t, filepath.Join(parsed, "keep.txt"), keep)
		assertFileBytes(t, filepath.Join(parsed, "notes.md"), notes)
		assertFileBytes(t, filepath.Join(parsed, "nested", "skip.json"), skip)
		assertAbsent(t, filepath.Join(analyzed, "keep.txt"))
		assertAbsent(t, filepath.Join(analyzed, "skip.json"))
		assertPerm(t, analyzed, 0o700)
		assertPerm(t, filepath.Join(dir, "batch"), 0o755)
		assertPerm(t, filepath.Join(dir, "batch", "jobads"), 0o755)
		assertPerm(t, parsed, 0o755)
		if len(calls) != 4 || calls[0][1] != "available" || calls[1][1] != "respond" {
			t.Fatalf("calls %#v", calls)
		}
		if len(tty.prompts) != 0 {
			t.Fatalf("prompted %q", tty.prompts)
		}
		for _, path := range []string{
			filepath.Join(analyzed, "Role-analysis.json"),
			filepath.Join(analyzed, "a-analysis.json"),
		} {
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(got, []byte("COMPANY-TOKEN")) || bytes.Contains(got, []byte("TRAILER-SENTINEL")) || bytes.Contains(got, []byte(apiKey)) {
				t.Fatalf("%s contains a field that stays out of analysis %s", path, got)
			}
		}
		assertRefreshUntouched(t)
		assertNoSecret(t, stdout, stderr, apiKey)
	})

	t.Run("no json still creates analyzed", func(t *testing.T) {
		keyring.MockInit()
		dir := t.TempDir()
		parsed := makeParsed(t, dir)
		keep := []byte("KEEP-TXT")
		writeAt(t, filepath.Join(parsed, "keep.txt"), keep)
		var calls [][]string
		scriptedFM(t, &calls)
		stdout, stderr, code := runBatch(t, dir, []string{"analyze", "--batch"}, deps{
			GeminiClient: blockGemini(t),
		})
		if code != 1 || stdout != "" || stderr != errNoBatchJSON+"\n" {
			t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
		}
		if len(calls) != 0 {
			t.Fatalf("calls %#v", calls)
		}
		analyzed := filepath.Join(dir, "batch", "jobads", "analyzed")
		assertPerm(t, analyzed, 0o700)
		assertFileBytes(t, filepath.Join(parsed, "keep.txt"), keep)
		assertPerm(t, filepath.Join(dir, "batch"), 0o755)
		assertPerm(t, filepath.Join(dir, "batch", "jobads"), 0o755)
		assertPerm(t, parsed, 0o755)
	})

	t.Run("parsed missing", func(t *testing.T) {
		keyring.MockInit()
		dir := t.TempDir()
		jobads := filepath.Join(dir, "batch", "jobads")
		if err := os.MkdirAll(jobads, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(dir, "batch"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(jobads, 0o755); err != nil {
			t.Fatal(err)
		}
		var calls [][]string
		scriptedFM(t, &calls)
		stdout, stderr, code := runBatch(t, dir, []string{"analyze", "--batch"}, deps{
			GeminiClient: blockGemini(t),
		})
		if code != 1 || stdout != "" || stderr != errParsedDirMissing+"\n" {
			t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
		}
		if len(calls) != 0 {
			t.Fatalf("calls %#v", calls)
		}
		assertAbsent(t, filepath.Join(jobads, "parsed"))
		assertAbsent(t, filepath.Join(jobads, "analyzed"))
		assertPerm(t, jobads, 0o755)
	})

	t.Run("parsed is a file", func(t *testing.T) {
		keyring.MockInit()
		dir := t.TempDir()
		jobads := filepath.Join(dir, "batch", "jobads")
		marker := []byte("parsed-file")
		writeAt(t, filepath.Join(jobads, "parsed"), marker)
		var calls [][]string
		scriptedFM(t, &calls)
		stdout, stderr, code := runBatch(t, dir, []string{"analyze", "--batch"}, deps{
			GeminiClient: blockGemini(t),
		})
		want := "docs-cli: not a directory: " + filepath.Join("batch", "jobads", "parsed") + "\n"
		if code != 1 || stdout != "" || stderr != want {
			t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
		}
		assertFileBytes(t, filepath.Join(jobads, "parsed"), marker)
		assertAbsent(t, filepath.Join(jobads, "analyzed"))
		if len(calls) != 0 {
			t.Fatalf("calls %#v", calls)
		}
	})

	t.Run("parsed is a symlink", func(t *testing.T) {
		keyring.MockInit()
		dir := t.TempDir()
		jobads := filepath.Join(dir, "batch", "jobads")
		if err := os.MkdirAll(jobads, 0o755); err != nil {
			t.Fatal(err)
		}
		real := filepath.Join(dir, "real")
		body := jobFileBytes(t, batchJob("Know A"))
		writeAt(t, filepath.Join(real, "a.json"), body)
		if err := os.Symlink(real, filepath.Join(jobads, "parsed")); err != nil {
			t.Fatal(err)
		}
		var calls [][]string
		scriptedFM(t, &calls)
		stdout, stderr, code := runBatch(t, dir, []string{"analyze", "--batch"}, deps{
			GeminiClient: blockGemini(t),
		})
		want := "docs-cli: not a directory: " + filepath.Join("batch", "jobads", "parsed") + "\n"
		if code != 1 || stdout != "" || stderr != want {
			t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
		}
		assertFileBytes(t, filepath.Join(real, "a.json"), body)
		assertAbsent(t, filepath.Join(jobads, "analyzed"))
		if len(calls) != 0 {
			t.Fatalf("calls %#v", calls)
		}
	})

	t.Run("analyzed is a file", func(t *testing.T) {
		keyring.MockInit()
		dir := t.TempDir()
		parsed := makeParsed(t, dir)
		body := jobFileBytes(t, batchJob("Know A"))
		writeAt(t, filepath.Join(parsed, "a.json"), body)
		marker := []byte("analyzed-file")
		analyzed := filepath.Join(dir, "batch", "jobads", "analyzed")
		writeAt(t, analyzed, marker)
		var calls [][]string
		scriptedFM(t, &calls)
		stdout, stderr, code := runBatch(t, dir, []string{"analyze", "--batch"}, deps{
			GeminiClient: blockGemini(t),
		})
		want := "docs-cli: not a directory: " + filepath.Join("batch", "jobads", "analyzed") + "\n"
		if code != 1 || stdout != "" || stderr != want {
			t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
		}
		assertFileBytes(t, analyzed, marker)
		assertFileBytes(t, filepath.Join(parsed, "a.json"), body)
		if len(calls) != 0 {
			t.Fatalf("calls %#v", calls)
		}
	})

	t.Run("analyzed is a symlink", func(t *testing.T) {
		keyring.MockInit()
		dir := t.TempDir()
		parsed := makeParsed(t, dir)
		body := jobFileBytes(t, batchJob("Know A"))
		writeAt(t, filepath.Join(parsed, "a.json"), body)
		marker := []byte("symlink-target")
		target := writeAt(t, filepath.Join(dir, "marker"), marker)
		analyzed := filepath.Join(dir, "batch", "jobads", "analyzed")
		if err := os.Symlink(target, analyzed); err != nil {
			t.Fatal(err)
		}
		var calls [][]string
		scriptedFM(t, &calls)
		stdout, stderr, code := runBatch(t, dir, []string{"analyze", "--batch"}, deps{
			GeminiClient: blockGemini(t),
		})
		want := "docs-cli: not a directory: " + filepath.Join("batch", "jobads", "analyzed") + "\n"
		if code != 1 || stdout != "" || stderr != want {
			t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
		}
		got, err := os.Readlink(analyzed)
		if err != nil || got != target {
			t.Fatalf("symlink %q err %v", got, err)
		}
		assertFileBytes(t, target, marker)
		assertFileBytes(t, filepath.Join(parsed, "a.json"), body)
		if len(calls) != 0 {
			t.Fatalf("calls %#v", calls)
		}
	})

	t.Run("existing analyzed mode stays", func(t *testing.T) {
		keyring.MockInit()
		dir := t.TempDir()
		parsed := makeParsed(t, dir)
		analyzed := filepath.Join(dir, "batch", "jobads", "analyzed")
		if err := os.Mkdir(analyzed, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(analyzed, 0o755); err != nil {
			t.Fatal(err)
		}
		body := jobFileBytes(t, batchJob("Know A"))
		writeAt(t, filepath.Join(parsed, "a.json"), body)
		model := batchModel("Know A")
		var calls [][]string
		scriptedFM(t, &calls, fmStep{}, fmStep{stdout: model + "\n"})
		stdout, stderr, code := runBatch(t, dir, []string{"analyze", "--batch"}, deps{
			GeminiClient: blockGemini(t),
		})
		if code != 0 || stderr != "" || stdout == "" {
			t.Fatalf("exit %d stderr %q stdout %q", code, stderr, stdout)
		}
		assertPerm(t, analyzed, 0o755)
		assertPerm(t, parsed, 0o755)
		assertPerm(t, filepath.Join(dir, "batch"), 0o755)
		assertFileBytes(t, filepath.Join(analyzed, "a.json"), body)
		assertAbsent(t, filepath.Join(parsed, "a.json"))
	})

	t.Run("usage does not create analyzed", func(t *testing.T) {
		keyring.MockInit()
		var calls [][]string
		scriptedFM(t, &calls)
		cases := [][]string{
			{"analyze", "--batch", "--json", "job.json"},
			{"analyze", "--json", "job.json", "--batch"},
			{"analyze", "--batch", "--json", "job.json", "--gemini"},
			{"analyze", "--json", "job.json", "--batch", "--gemini"},
			{"analyze", "--gemini"},
			{"analyze", "--batch", "extra"},
			{"analyze", "--batch", "--json", "job.json", "extra"},
			{"analyze", "--batch=false"},
			{"analyze", "--batch=false", "--json", "job.json"},
			{"analyze", "--batch", "--help"},
			{"analyze", "--batch", "-h"},
		}
		for _, args := range cases {
			dir := t.TempDir()
			parsed := makeParsed(t, dir)
			body := jobFileBytes(t, batchJob("Know A"))
			writeAt(t, filepath.Join(parsed, "a.json"), body)
			stdout, stderr, code := runBatch(t, dir, args, deps{
				GeminiClient: blockGemini(t),
			})
			if code != 2 || stdout != "" || !strings.Contains(stderr, "usage: docs-cli") {
				t.Fatalf("args %v exit %d stdout %q stderr %q", args, code, stdout, stderr)
			}
			assertAbsent(t, filepath.Join(dir, "batch", "jobads", "analyzed"))
			assertFileBytes(t, filepath.Join(parsed, "a.json"), body)
		}
		if len(calls) != 0 {
			t.Fatalf("calls %#v", calls)
		}
	})

	t.Run("too big and symlink before fm", func(t *testing.T) {
		keyring.MockInit()
		dir := t.TempDir()
		parsed := makeParsed(t, dir)
		aBody := jobFileBytes(t, batchJob("Know A"))
		writeAt(t, filepath.Join(parsed, "a.json"), aBody)
		big := bytes.Repeat([]byte{'x'}, maxJSONBytes+1)
		writeAt(t, filepath.Join(parsed, "b.json"), big)
		var calls [][]string
		scriptedFM(t, &calls, fmStep{}, fmStep{stdout: batchModel("Know A") + "\n"})
		stdout, stderr, code := runBatch(t, dir, []string{"analyze", "--batch"}, deps{
			GeminiClient: blockGemini(t),
		})
		want := "docs-cli: json exceeds 1 MiB: " + filepath.Join("batch", "jobads", "parsed", "b.json") + "\n"
		if code != 1 || stdout != "" || stderr != want {
			t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
		}
		if len(calls) != 0 {
			t.Fatalf("calls %#v", calls)
		}
		assertFileBytes(t, filepath.Join(parsed, "a.json"), aBody)
		assertFileBytes(t, filepath.Join(parsed, "b.json"), big)
		assertAbsent(t, filepath.Join(dir, "batch", "jobads", "analyzed", "a-analysis.json"))
		assertPerm(t, filepath.Join(dir, "batch", "jobads", "analyzed"), 0o700)

		dir = t.TempDir()
		parsed = makeParsed(t, dir)
		target := writeAt(t, filepath.Join(dir, "real.json"), aBody)
		if err := os.Symlink(target, filepath.Join(parsed, "link.json")); err != nil {
			t.Fatal(err)
		}
		stdout, stderr, code = runBatch(t, dir, []string{"analyze", "--batch"}, deps{
			GeminiClient: blockGemini(t),
		})
		want = "docs-cli: not a regular file: " + filepath.Join("batch", "jobads", "parsed", "link.json") + "\n"
		if code != 1 || stdout != "" || stderr != want {
			t.Fatalf("symlink exit %d stdout %q stderr %q", code, stdout, stderr)
		}
		assertFileBytes(t, target, aBody)
		if len(calls) != 0 {
			t.Fatalf("calls %#v", calls)
		}
	})

	t.Run("exact size is accepted", func(t *testing.T) {
		keyring.MockInit()
		dir := t.TempDir()
		parsed := makeParsed(t, dir)
		body := []byte(`{"requirements":[]}`)
		padded := append(body, bytes.Repeat([]byte{' '}, maxJSONBytes-len(body))...)
		if len(padded) != maxJSONBytes {
			t.Fatalf("len %d", len(padded))
		}
		writeAt(t, filepath.Join(parsed, "exact.json"), padded)
		var calls [][]string
		scriptedFM(t, &calls)
		stdout, stderr, code := runBatch(t, dir, []string{"analyze", "--batch"}, deps{
			GeminiClient: blockGemini(t),
		})
		if code != 1 || stdout != "" || stderr != "docs-cli: requirements is empty\n" {
			t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
		}
		if len(calls) != 0 {
			t.Fatalf("calls %#v", calls)
		}
		assertFileBytes(t, filepath.Join(parsed, "exact.json"), padded)
		assertAbsent(t, filepath.Join(dir, "batch", "jobads", "analyzed", "exact-analysis.json"))
	})

	t.Run("destination already exists", func(t *testing.T) {
		keyring.MockInit()
		dir := t.TempDir()
		parsed := makeParsed(t, dir)
		analyzed := filepath.Join(dir, "batch", "jobads", "analyzed")
		if err := os.Mkdir(analyzed, 0o700); err != nil {
			t.Fatal(err)
		}
		body := jobFileBytes(t, batchJob("Know A"))
		writeAt(t, filepath.Join(parsed, "a.json"), body)
		marker := []byte("already")
		writeAt(t, filepath.Join(analyzed, "a-analysis.json"), marker)
		var calls [][]string
		scriptedFM(t, &calls, fmStep{}, fmStep{stdout: batchModel("Know A") + "\n"})
		stdout, stderr, code := runBatch(t, dir, []string{"analyze", "--batch"}, deps{
			GeminiClient: blockGemini(t),
		})
		want := "docs-cli: analyzed file already exists: " + filepath.Join("batch", "jobads", "analyzed", "a-analysis.json") + "\n"
		if code != 1 || stdout != "" || stderr != want {
			t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
		}
		assertFileBytes(t, filepath.Join(analyzed, "a-analysis.json"), marker)
		assertFileBytes(t, filepath.Join(parsed, "a.json"), body)
		if len(calls) != 0 {
			t.Fatalf("calls %#v", calls)
		}

		dir = t.TempDir()
		parsed = makeParsed(t, dir)
		analyzed = filepath.Join(dir, "batch", "jobads", "analyzed")
		if err := os.Mkdir(analyzed, 0o700); err != nil {
			t.Fatal(err)
		}
		writeAt(t, filepath.Join(parsed, "a.json"), body)
		moved := []byte("moved-name")
		writeAt(t, filepath.Join(analyzed, "a.json"), moved)
		stdout, stderr, code = runBatch(t, dir, []string{"analyze", "--batch"}, deps{
			GeminiClient: blockGemini(t),
		})
		want = "docs-cli: analyzed file already exists: " + filepath.Join("batch", "jobads", "analyzed", "a.json") + "\n"
		if code != 1 || stdout != "" || stderr != want {
			t.Fatalf("move dest exit %d stdout %q stderr %q", code, stdout, stderr)
		}
		assertFileBytes(t, filepath.Join(analyzed, "a.json"), moved)
		assertFileBytes(t, filepath.Join(parsed, "a.json"), body)
		assertAbsent(t, filepath.Join(analyzed, "a-analysis.json"))

		dir = t.TempDir()
		parsed = makeParsed(t, dir)
		analyzed = filepath.Join(dir, "batch", "jobads", "analyzed")
		if err := os.Mkdir(analyzed, 0o700); err != nil {
			t.Fatal(err)
		}
		writeAt(t, filepath.Join(parsed, "a.json"), body)
		writeAt(t, filepath.Join(analyzed, "a-analysis.json"), marker)
		writeAt(t, filepath.Join(analyzed, "a.json"), moved)
		stdout, stderr, code = runBatch(t, dir, []string{"analyze", "--batch"}, deps{
			GeminiClient: blockGemini(t),
		})
		want = "docs-cli: analyzed file already exists: " + filepath.Join("batch", "jobads", "analyzed", "a-analysis.json") + "\n"
		if code != 1 || stdout != "" || stderr != want {
			t.Fatalf("both dests exit %d stdout %q stderr %q", code, stdout, stderr)
		}
		if len(calls) != 0 {
			t.Fatalf("calls %#v", calls)
		}

		dir = t.TempDir()
		parsed = makeParsed(t, dir)
		analyzed = filepath.Join(dir, "batch", "jobads", "analyzed")
		if err := os.Mkdir(analyzed, 0o700); err != nil {
			t.Fatal(err)
		}
		writeAt(t, filepath.Join(parsed, "a.json"), body)
		marker = []byte("already-svg")
		writeAt(t, filepath.Join(analyzed, "a-tech.md"), marker)
		stdout, stderr, code = runBatch(t, dir, []string{"analyze", "--batch"}, deps{
			GeminiClient: blockGemini(t),
		})
		want = "docs-cli: analyzed file already exists: " + filepath.Join("batch", "jobads", "analyzed", "a-tech.md") + "\n"
		if code != 1 || stdout != "" || stderr != want {
			t.Fatalf("svg dest exit %d stdout %q stderr %q", code, stdout, stderr)
		}
		assertFileBytes(t, filepath.Join(analyzed, "a-tech.md"), marker)
		assertFileBytes(t, filepath.Join(parsed, "a.json"), body)
		assertAbsent(t, filepath.Join(analyzed, "a-analysis.json"))
		assertAbsent(t, filepath.Join(analyzed, "a.json"))
		if len(calls) != 0 {
			t.Fatalf("calls %#v", calls)
		}
	})

	t.Run("dot json name", func(t *testing.T) {
		keyring.MockInit()
		dir := t.TempDir()
		parsed := makeParsed(t, dir)
		body := []byte(`{"requirements":["Know A"]}`)
		writeAt(t, filepath.Join(parsed, ".json"), body)
		var calls [][]string
		scriptedFM(t, &calls)
		stdout, stderr, code := runBatch(t, dir, []string{"analyze", "--batch"}, deps{
			GeminiClient: blockGemini(t),
		})
		want := "docs-cli: not a json file: " + filepath.Join("batch", "jobads", "parsed", ".json") + "\n"
		if code != 1 || stdout != "" || stderr != want {
			t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
		}
		assertFileBytes(t, filepath.Join(parsed, ".json"), body)
		if len(calls) != 0 {
			t.Fatalf("calls %#v", calls)
		}
	})

	t.Run("invalid json stops", func(t *testing.T) {
		keyring.MockInit()
		dir := t.TempDir()
		parsed := makeParsed(t, dir)
		bad := []byte(`{`)
		good := jobFileBytes(t, batchJob("Know B"))
		writeAt(t, filepath.Join(parsed, "a.json"), bad)
		writeAt(t, filepath.Join(parsed, "b.json"), good)
		var calls [][]string
		scriptedFM(t, &calls, fmStep{}, fmStep{stdout: batchModel("Know B") + "\n"})
		stdout, stderr, code := runBatch(t, dir, []string{"analyze", "--batch"}, deps{
			GeminiClient: blockGemini(t),
		})
		if code != 1 || stdout != "" || !strings.HasPrefix(stderr, "docs-cli: error: ") || strings.Contains(stderr, errJSONTooLarge) {
			t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
		}
		assertFileBytes(t, filepath.Join(parsed, "a.json"), bad)
		assertFileBytes(t, filepath.Join(parsed, "b.json"), good)
		assertAbsent(t, filepath.Join(dir, "batch", "jobads", "analyzed", "a-analysis.json"))
		assertAbsent(t, filepath.Join(dir, "batch", "jobads", "analyzed", "b-analysis.json"))
		if len(calls) != 0 {
			t.Fatalf("calls %#v", calls)
		}
	})

	t.Run("second file fm failure keeps the first", func(t *testing.T) {
		keyring.MockInit()
		if err := keyring.Set("docs-cli", "refresh-token", testRefreshItem); err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		parsed := makeParsed(t, dir)
		analyzed := filepath.Join(dir, "batch", "jobads", "analyzed")
		aBody := jobFileBytes(t, batchJob("Know A"))
		bBody := jobFileBytes(t, batchJob("Know B"))
		writeAt(t, filepath.Join(parsed, "a.json"), aBody)
		writeAt(t, filepath.Join(parsed, "b.json"), bBody)
		model := batchModel("Know A")
		var calls [][]string
		scriptedFM(t, &calls,
			fmStep{}, fmStep{stdout: model + "\n"},
			fmStep{stderr: "not licensed\n", code: 1},
		)
		hits := 0
		stdout, stderr, code := runBatch(t, dir, []string{"analyze", "--batch"}, deps{
			GeminiClient: countingGemini(t, http.StatusOK, analysisEnvelope(t, batchModel("FROM-GEMINI")), &hits, &geminiCapture{}),
		})
		aAbs, err := filepath.Abs(filepath.Join(analyzed, "a-analysis.json"))
		if err != nil {
			t.Fatal(err)
		}
		aSVG, err := filepath.Abs(filepath.Join(analyzed, "a-tech.md"))
		if err != nil {
			t.Fatal(err)
		}
		aDist, err := filepath.Abs(filepath.Join(analyzed, "a-tech-dist.md"))
		if err != nil {
			t.Fatal(err)
		}
		if code != 1 || stdout != aAbs+"\n"+aSVG+"\n"+aDist+"\n" || stderr != "docs-cli: fm: not licensed\n" || hits != 0 {
			t.Fatalf("exit %d hits %d stdout %q stderr %q", code, hits, stdout, stderr)
		}
		assertFileBytes(t, filepath.Join(analyzed, "a-analysis.json"), analysisFileBytes(t, model))
		assertFileBytes(t, filepath.Join(analyzed, "a.json"), aBody)
		diagramBody, err := os.ReadFile(filepath.Join(analyzed, "a-tech.md"))
		if err != nil || string(diagramBody) != "(none)\n" || bytes.Contains(diagramBody, []byte("flowchart")) {
			t.Fatalf("diagram %q err %v", diagramBody, err)
		}
		assertPerm(t, filepath.Join(analyzed, "a-tech.md"), 0o600)
		assertAbsent(t, filepath.Join(analyzed, "a-tech.svg"))
		distBody, err := os.ReadFile(filepath.Join(analyzed, "a-tech-dist.md"))
		if err != nil || string(distBody) != "(none)\n" || bytes.Contains(distBody, []byte("quadrantChart")) {
			t.Fatalf("dist %q err %v", distBody, err)
		}
		assertPerm(t, filepath.Join(analyzed, "a-tech-dist.md"), 0o600)
		assertFileBytes(t, filepath.Join(parsed, "b.json"), bBody)
		assertAbsent(t, filepath.Join(parsed, "a.json"))
		assertAbsent(t, filepath.Join(analyzed, "b-analysis.json"))
		assertAbsent(t, filepath.Join(analyzed, "b-tech.md"))
		assertAbsent(t, filepath.Join(analyzed, "b-tech.svg"))
		assertAbsent(t, filepath.Join(analyzed, "b-tech-dist.md"))
		assertAbsent(t, filepath.Join(analyzed, "b.json"))
		if len(calls) != 3 {
			t.Fatalf("calls %#v", calls)
		}
		assertRefreshUntouched(t)
		if _, err := keyring.Get("docs-cli", "gemini-api-key"); !errors.Is(err, keyring.ErrNotFound) {
			t.Fatalf("key stored, err %v", err)
		}
	})

	t.Run("empty requirements stops before fm", func(t *testing.T) {
		keyring.MockInit()
		if err := jobad.StoreGeminiKey(apiKey); err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		parsed := makeParsed(t, dir)
		aBody := jobFileBytes(t, jobad.Document{Requirements: []string{}})
		bBody := jobFileBytes(t, batchJob("Know B"))
		writeAt(t, filepath.Join(parsed, "a.json"), aBody)
		writeAt(t, filepath.Join(parsed, "b.json"), bBody)
		var calls [][]string
		scriptedFM(t, &calls, fmStep{}, fmStep{stdout: batchModel("Know B") + "\n"})
		stdout, stderr, code := runBatch(t, dir, []string{"analyze", "--batch"}, deps{
			GeminiClient: blockGemini(t),
		})
		if code != 1 || stdout != "" || stderr != "docs-cli: requirements is empty\n" {
			t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
		}
		assertFileBytes(t, filepath.Join(parsed, "a.json"), aBody)
		assertFileBytes(t, filepath.Join(parsed, "b.json"), bBody)
		assertAbsent(t, filepath.Join(dir, "batch", "jobads", "analyzed", "a-analysis.json"))
		assertAbsent(t, filepath.Join(dir, "batch", "jobads", "analyzed", "b-analysis.json"))
		if len(calls) != 0 {
			t.Fatalf("calls %#v", calls)
		}
	})

	t.Run("single analyze does not use the batch folders", func(t *testing.T) {
		keyring.MockInit()
		if err := keyring.Set("docs-cli", "refresh-token", testRefreshItem); err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		body := jobFileBytes(t, batchJob("Know Go"))
		path := writeAt(t, filepath.Join(dir, "job.json"), body)
		model := batchModel("Know Go")
		var calls [][]string
		scriptedFM(t, &calls, fmStep{}, fmStep{stdout: model + "\n"})
		stdout, stderr, code := runBatch(t, dir, []string{"analyze", "--json", path}, deps{
			GeminiClient: blockGemini(t),
		})
		if code != 0 || stderr != "" || stdout != string(analysisFileBytes(t, model)) {
			t.Fatalf("exit %d stderr %q stdout %q", code, stderr, stdout)
		}
		assertAbsent(t, filepath.Join(dir, "batch"))
		assertFileBytes(t, path, body)
		diagramPath := filepath.Join(dir, "job-tech.md")
		diagramBody, err := os.ReadFile(diagramPath)
		if err != nil || string(diagramBody) != "(none)\n" || bytes.Contains(diagramBody, []byte("flowchart")) {
			t.Fatalf("diagram %q err %v", diagramBody, err)
		}
		assertPerm(t, diagramPath, 0o600)
		assertAbsent(t, filepath.Join(dir, "job-tech.svg"))
		distBody, err := os.ReadFile(filepath.Join(dir, "job-tech-dist.md"))
		if err != nil || string(distBody) != "(none)\n" {
			t.Fatalf("dist %q err %v", distBody, err)
		}
		if strings.Contains(stdout, "job-tech.md") {
			t.Fatalf("stdout printed the diagram path %q", stdout)
		}
		if len(calls) != 2 {
			t.Fatalf("calls %#v", calls)
		}
		assertRefreshUntouched(t)
	})
}

func TestAnalyzeTechDiagram(t *testing.T) {
	model := `{"must_have":["Know Go"],"advantage":[],"min_years":null,"degree":null,"languages":[],"tech":[{"name":"Java","level":"must","applies_to":"Backend"},{"name":"Spring Boot","level":"must","applies_to":"Backend"},{"name":"Salesforce","level":"advantage","applies_to":"Integration"}],"soft_skills":[]}`
	doc := batchJob("Know Go")

	t.Run("writes markdown beside json", func(t *testing.T) {
		keyring.MockInit()
		dir := t.TempDir()
		body := jobFileBytes(t, doc)
		path := writeAt(t, filepath.Join(dir, "jd.json"), body)
		var calls [][]string
		scriptedFM(t, &calls, fmStep{}, fmStep{stdout: model + "\n"})
		stdout, stderr, code := runBatch(t, dir, []string{"analyze", "--json", path}, deps{
			GeminiClient: blockGemini(t),
		})
		if code != 0 || stderr != "" || stdout != string(analysisFileBytes(t, model)) {
			t.Fatalf("exit %d stderr %q stdout %q", code, stderr, stdout)
		}
		if strings.Contains(stdout, "jd-tech.md") || strings.Contains(stdout, "Java / Spring Boot") {
			t.Fatalf("stdout %q", stdout)
		}
		diagramPath := filepath.Join(dir, "jd-tech.md")
		diagram, err := os.ReadFile(diagramPath)
		if err != nil {
			t.Fatal(err)
		}
		assertPerm(t, diagramPath, 0o600)
		assertPerm(t, filepath.Join(dir, "jd-tech-dist.md"), 0o600)
		for _, want := range []string{
			"flowchart LR",
			`n1["Java / Spring Boot"]:::must`,
			`n2["Backend"]:::target`,
			`n1 -->|must| n2`,
			`n3["Salesforce"]:::advantage`,
			`n4["Integration"]:::target`,
			`n3 -.->|advantage| n4`,
			"linkStyle 0 stroke:#3A3A3A,stroke-width:2px",
			"linkStyle 1 stroke:#3A3A3A,stroke-width:2px",
		} {
			if !bytes.Contains(diagram, []byte(want)) {
				t.Fatalf("missing %s\n%s", want, diagram)
			}
		}
		for _, banned := range []string{"<svg", "#111111", "#9a3412"} {
			if bytes.Contains(diagram, []byte(banned)) {
				t.Fatalf("diagram contains %s\n%s", banned, diagram)
			}
		}
		assertAbsent(t, filepath.Join(dir, "jd-tech.svg"))
		assertFileBytes(t, path, body)
		assertAbsent(t, filepath.Join(dir, "batch"))
		if len(calls) != 2 {
			t.Fatalf("calls %#v", calls)
		}
	})

	t.Run("existing file skips fm", func(t *testing.T) {
		keyring.MockInit()
		dir := t.TempDir()
		body := jobFileBytes(t, doc)
		path := writeAt(t, filepath.Join(dir, "jd.json"), body)
		svgPath := filepath.Join(dir, "jd-tech.md")
		marker := []byte("already-svg")
		writeAt(t, svgPath, marker)
		var calls [][]string
		scriptedFM(t, &calls, fmStep{}, fmStep{stdout: model + "\n"})
		stdout, stderr, code := runBatch(t, dir, []string{"analyze", "--json", path}, deps{
			GeminiClient: blockGemini(t),
		})
		want := "docs-cli: tech diagram already exists: " + svgPath + "\n"
		if code != 1 || stdout != "" || stderr != want {
			t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
		}
		assertFileBytes(t, svgPath, marker)
		assertFileBytes(t, path, body)
		assertAbsent(t, filepath.Join(dir, "jd-tech-dist.md"))
		if len(calls) != 0 {
			t.Fatalf("calls %#v", calls)
		}
	})

	t.Run("existing symlink skips fm", func(t *testing.T) {
		keyring.MockInit()
		dir := t.TempDir()
		body := jobFileBytes(t, doc)
		path := writeAt(t, filepath.Join(dir, "jd.json"), body)
		marker := []byte("symlink-svg")
		target := writeAt(t, filepath.Join(dir, "marker.svg"), marker)
		svgPath := filepath.Join(dir, "jd-tech.md")
		if err := os.Symlink(target, svgPath); err != nil {
			t.Fatal(err)
		}
		var calls [][]string
		scriptedFM(t, &calls, fmStep{}, fmStep{stdout: model + "\n"})
		stdout, stderr, code := runBatch(t, dir, []string{"analyze", "--json", path}, deps{
			GeminiClient: blockGemini(t),
		})
		want := "docs-cli: tech diagram already exists: " + svgPath + "\n"
		if code != 1 || stdout != "" || stderr != want {
			t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
		}
		got, err := os.Readlink(svgPath)
		if err != nil || got != target {
			t.Fatalf("symlink %q err %v", got, err)
		}
		assertFileBytes(t, target, marker)
		assertFileBytes(t, path, body)
		if len(calls) != 0 {
			t.Fatalf("calls %#v", calls)
		}
	})

	t.Run("existing directory skips fm", func(t *testing.T) {
		keyring.MockInit()
		dir := t.TempDir()
		body := jobFileBytes(t, doc)
		path := writeAt(t, filepath.Join(dir, "jd.json"), body)
		svgPath := filepath.Join(dir, "jd-tech.md")
		if err := os.Mkdir(svgPath, 0o700); err != nil {
			t.Fatal(err)
		}
		var calls [][]string
		scriptedFM(t, &calls, fmStep{}, fmStep{stdout: model + "\n"})
		stdout, stderr, code := runBatch(t, dir, []string{"analyze", "--json", path}, deps{
			GeminiClient: blockGemini(t),
		})
		want := "docs-cli: tech diagram already exists: " + svgPath + "\n"
		if code != 1 || stdout != "" || stderr != want {
			t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
		}
		info, err := os.Lstat(svgPath)
		if err != nil || !info.IsDir() {
			t.Fatalf("diagram path %v err %v", info, err)
		}
		assertFileBytes(t, path, body)
		if len(calls) != 0 {
			t.Fatalf("calls %#v", calls)
		}
	})

	t.Run("fm failure writes no svg", func(t *testing.T) {
		keyring.MockInit()
		dir := t.TempDir()
		body := jobFileBytes(t, doc)
		path := writeAt(t, filepath.Join(dir, "jd.json"), body)
		var calls [][]string
		scriptedFM(t, &calls, fmStep{stderr: "not licensed\n", code: 1})
		stdout, stderr, code := runBatch(t, dir, []string{"analyze", "--json", path}, deps{
			GeminiClient: blockGemini(t),
		})
		if code != 1 || stdout != "" || stderr != "docs-cli: fm: not licensed\n" {
			t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
		}
		assertAbsent(t, filepath.Join(dir, "jd-tech.md"))
		assertAbsent(t, filepath.Join(dir, "jd-tech.svg"))
		assertAbsent(t, filepath.Join(dir, "jd-tech-dist.md"))
		assertFileBytes(t, path, body)
		if len(calls) != 1 {
			t.Fatalf("calls %#v", calls)
		}
	})

	t.Run("old svg does not block markdown", func(t *testing.T) {
		keyring.MockInit()
		dir := t.TempDir()
		body := jobFileBytes(t, doc)
		path := writeAt(t, filepath.Join(dir, "jd.json"), body)
		old := filepath.Join(dir, "jd-tech.svg")
		marker := []byte("<svg>old</svg>")
		writeAt(t, old, marker)
		var calls [][]string
		scriptedFM(t, &calls, fmStep{}, fmStep{stdout: model + "\n"})
		stdout, stderr, code := runBatch(t, dir, []string{"analyze", "--json", path}, deps{
			GeminiClient: blockGemini(t),
		})
		if code != 0 || stderr != "" || stdout != string(analysisFileBytes(t, model)) {
			t.Fatalf("exit %d stderr %q stdout %q", code, stderr, stdout)
		}
		assertFileBytes(t, old, marker)
		diagram, err := os.ReadFile(filepath.Join(dir, "jd-tech.md"))
		if err != nil || !bytes.Contains(diagram, []byte("flowchart LR")) || !bytes.Contains(diagram, []byte(`n1 -->|must| n2`)) {
			t.Fatalf("diagram %q err %v", diagram, err)
		}
		assertPerm(t, filepath.Join(dir, "jd-tech.md"), 0o600)
		assertPerm(t, filepath.Join(dir, "jd-tech-dist.md"), 0o600)
		if len(calls) != 2 {
			t.Fatalf("calls %#v", calls)
		}
	})

	t.Run("dot json name", func(t *testing.T) {
		keyring.MockInit()
		dir := t.TempDir()
		body := []byte(`{"requirements":["Know Go"]}`)
		path := writeAt(t, filepath.Join(dir, ".json"), body)
		var calls [][]string
		scriptedFM(t, &calls, fmStep{}, fmStep{stdout: model + "\n"})
		stdout, stderr, code := runBatch(t, dir, []string{"analyze", "--json", path}, deps{
			GeminiClient: blockGemini(t),
		})
		want := "docs-cli: not a json file: " + path + "\n"
		if code != 1 || stdout != "" || stderr != want {
			t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
		}
		assertFileBytes(t, path, body)
		assertAbsent(t, filepath.Join(dir, "-tech.md"))
		assertAbsent(t, filepath.Join(dir, "-tech-dist.md"))
		if len(calls) != 0 {
			t.Fatalf("calls %#v", calls)
		}
	})
}

func TestAnalyzeTechDistribution(t *testing.T) {
	model := `{"must_have":["Know Go"],"advantage":[],"min_years":null,"degree":null,"languages":[],"tech":[{"name":"RESTful services","level":"must","applies_to":"APIs"},{"name":"GraphQL","level":"advantage","applies_to":"APIs"},{"name":"WebSockets","level":"advantage","applies_to":"APIs"}],"soft_skills":[]}`
	doc := batchJob("Know Go")

	t.Run("writes the diagram and the markdown", func(t *testing.T) {
		keyring.MockInit()
		dir := t.TempDir()
		body := jobFileBytes(t, doc)
		path := writeAt(t, filepath.Join(dir, "jd.json"), body)
		var calls [][]string
		scriptedFM(t, &calls, fmStep{}, fmStep{stdout: model + "\n"})
		stdout, stderr, code := runBatch(t, dir, []string{"analyze", "--json", path}, deps{
			GeminiClient: blockGemini(t),
		})
		if code != 0 || stderr != "" || stdout != string(analysisFileBytes(t, model)) {
			t.Fatalf("exit %d stderr %q stdout %q", code, stderr, stdout)
		}
		if strings.Contains(stdout, "jd-tech") {
			t.Fatalf("stdout printed a diagram path %q", stdout)
		}
		for _, name := range []string{"jd-tech.md", "jd-tech-dist.md"} {
			assertPerm(t, filepath.Join(dir, name), 0o600)
		}
		assertAbsent(t, filepath.Join(dir, "jd-tech.svg"))
		dist, err := os.ReadFile(filepath.Join(dir, "jd-tech-dist.md"))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{
			"quadrantChart",
			"    title jd\n",
			`"APIs": [0.375, 0.625] color: #737373, stroke-color: #3A3A3A, stroke-width: 1px`,
			"| APIs | 1 | 2 | RESTful services / GraphQL / WebSockets |",
		} {
			if !bytes.Contains(dist, []byte(want)) {
				t.Fatalf("missing %s\n%s", want, dist)
			}
		}
		assertAbsent(t, filepath.Join(dir, "jd-tech-dist.svg"))
		assertFileBytes(t, path, body)
		assertAbsent(t, filepath.Join(dir, "batch"))
		if len(calls) != 2 {
			t.Fatalf("calls %#v", calls)
		}
	})

	t.Run("old svg distribution does not block markdown", func(t *testing.T) {
		keyring.MockInit()
		dir := t.TempDir()
		body := jobFileBytes(t, doc)
		path := writeAt(t, filepath.Join(dir, "jd.json"), body)
		old := filepath.Join(dir, "jd-tech-dist.svg")
		marker := []byte("<svg>old</svg>")
		writeAt(t, old, marker)
		var calls [][]string
		scriptedFM(t, &calls, fmStep{}, fmStep{stdout: model + "\n"})
		stdout, stderr, code := runBatch(t, dir, []string{"analyze", "--json", path}, deps{
			GeminiClient: blockGemini(t),
		})
		if code != 0 || stderr != "" || stdout != string(analysisFileBytes(t, model)) {
			t.Fatalf("exit %d stderr %q stdout %q", code, stderr, stdout)
		}
		assertFileBytes(t, old, marker)
		assertPerm(t, filepath.Join(dir, "jd-tech-dist.md"), 0o600)
		dist, err := os.ReadFile(filepath.Join(dir, "jd-tech-dist.md"))
		if err != nil || !bytes.Contains(dist, []byte("quadrantChart")) {
			t.Fatalf("dist %q err %v", dist, err)
		}
		if len(calls) != 2 {
			t.Fatalf("calls %#v", calls)
		}
	})

	t.Run("existing distribution skips fm", func(t *testing.T) {
		keyring.MockInit()
		dir := t.TempDir()
		body := jobFileBytes(t, doc)
		path := writeAt(t, filepath.Join(dir, "jd.json"), body)
		distPath := filepath.Join(dir, "jd-tech-dist.md")
		marker := []byte("already-dist")
		writeAt(t, distPath, marker)
		var calls [][]string
		scriptedFM(t, &calls, fmStep{}, fmStep{stdout: model + "\n"})
		stdout, stderr, code := runBatch(t, dir, []string{"analyze", "--json", path}, deps{
			GeminiClient: blockGemini(t),
		})
		want := "docs-cli: tech distribution already exists: " + distPath + "\n"
		if code != 1 || stdout != "" || stderr != want {
			t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
		}
		assertFileBytes(t, distPath, marker)
		assertFileBytes(t, path, body)
		assertAbsent(t, filepath.Join(dir, "jd-tech.md"))
		if len(calls) != 0 {
			t.Fatalf("calls %#v", calls)
		}
	})

	t.Run("existing distribution symlink skips fm", func(t *testing.T) {
		keyring.MockInit()
		dir := t.TempDir()
		body := jobFileBytes(t, doc)
		path := writeAt(t, filepath.Join(dir, "jd.json"), body)
		marker := []byte("symlink-dist")
		target := writeAt(t, filepath.Join(dir, "marker.svg"), marker)
		distPath := filepath.Join(dir, "jd-tech-dist.md")
		if err := os.Symlink(target, distPath); err != nil {
			t.Fatal(err)
		}
		var calls [][]string
		scriptedFM(t, &calls, fmStep{}, fmStep{stdout: model + "\n"})
		stdout, stderr, code := runBatch(t, dir, []string{"analyze", "--json", path}, deps{
			GeminiClient: blockGemini(t),
		})
		want := "docs-cli: tech distribution already exists: " + distPath + "\n"
		if code != 1 || stdout != "" || stderr != want {
			t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
		}
		got, err := os.Readlink(distPath)
		if err != nil || got != target {
			t.Fatalf("symlink %q err %v", got, err)
		}
		assertFileBytes(t, target, marker)
		assertAbsent(t, filepath.Join(dir, "jd-tech.md"))
		if len(calls) != 0 {
			t.Fatalf("calls %#v", calls)
		}
	})

	t.Run("existing distribution directory skips fm", func(t *testing.T) {
		keyring.MockInit()
		dir := t.TempDir()
		body := jobFileBytes(t, doc)
		path := writeAt(t, filepath.Join(dir, "jd.json"), body)
		distPath := filepath.Join(dir, "jd-tech-dist.md")
		if err := os.Mkdir(distPath, 0o700); err != nil {
			t.Fatal(err)
		}
		var calls [][]string
		scriptedFM(t, &calls, fmStep{}, fmStep{stdout: model + "\n"})
		stdout, stderr, code := runBatch(t, dir, []string{"analyze", "--json", path}, deps{
			GeminiClient: blockGemini(t),
		})
		want := "docs-cli: tech distribution already exists: " + distPath + "\n"
		if code != 1 || stdout != "" || stderr != want {
			t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
		}
		info, err := os.Lstat(distPath)
		if err != nil || !info.IsDir() {
			t.Fatalf("distribution path %v err %v", info, err)
		}
		assertAbsent(t, filepath.Join(dir, "jd-tech.md"))
		if len(calls) != 0 {
			t.Fatalf("calls %#v", calls)
		}
	})

	t.Run("distribution write failure removes the diagram", func(t *testing.T) {
		keyring.MockInit()
		dir := t.TempDir()
		body := jobFileBytes(t, doc)
		path := writeAt(t, filepath.Join(dir, "jd.json"), body)
		techDistWrite = func(string, []byte) error { return errors.New("disk full") }
		t.Cleanup(func() { techDistWrite = nil })
		var calls [][]string
		scriptedFM(t, &calls, fmStep{}, fmStep{stdout: model + "\n"})
		stdout, stderr, code := runBatch(t, dir, []string{"analyze", "--json", path}, deps{
			GeminiClient: blockGemini(t),
		})
		if code != 1 || stdout != "" || stderr != "docs-cli: error: disk full\n" {
			t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
		}
		assertAbsent(t, filepath.Join(dir, "jd-tech.md"))
		assertAbsent(t, filepath.Join(dir, "jd-tech-dist.md"))
		assertFileBytes(t, path, body)
		if len(calls) != 2 {
			t.Fatalf("calls %#v", calls)
		}
	})

	t.Run("batch writes three paths", func(t *testing.T) {
		keyring.MockInit()
		dir := t.TempDir()
		parsed := makeParsed(t, dir)
		analyzed := filepath.Join(dir, "batch", "jobads", "analyzed")
		body := jobFileBytes(t, doc)
		writeAt(t, filepath.Join(parsed, "95169560.json"), body)
		var calls [][]string
		scriptedFM(t, &calls, fmStep{}, fmStep{stdout: model + "\n"})
		stdout, stderr, code := runBatch(t, dir, []string{"analyze", "--batch"}, deps{
			GeminiClient: blockGemini(t),
		})
		analysisAbs, err := filepath.Abs(filepath.Join(analyzed, "95169560-analysis.json"))
		if err != nil {
			t.Fatal(err)
		}
		svgAbs, err := filepath.Abs(filepath.Join(analyzed, "95169560-tech.md"))
		if err != nil {
			t.Fatal(err)
		}
		distAbs, err := filepath.Abs(filepath.Join(analyzed, "95169560-tech-dist.md"))
		if err != nil {
			t.Fatal(err)
		}
		if code != 0 || stderr != "" || stdout != analysisAbs+"\n"+svgAbs+"\n"+distAbs+"\n" {
			t.Fatalf("exit %d stderr %q stdout %q", code, stderr, stdout)
		}
		if strings.Contains(stdout, "must_have") {
			t.Fatalf("stdout printed json %q", stdout)
		}
		assertPerm(t, filepath.Join(analyzed, "95169560-tech-dist.md"), 0o600)
		dist, err := os.ReadFile(filepath.Join(analyzed, "95169560-tech-dist.md"))
		if err != nil || !bytes.Contains(dist, []byte("    title 95169560\n")) || !bytes.Contains(dist, []byte("quadrantChart")) {
			t.Fatalf("dist %q err %v", dist, err)
		}
		assertAbsent(t, filepath.Join(analyzed, "95169560-tech-dist.svg"))
		assertFileBytes(t, filepath.Join(analyzed, "95169560.json"), body)
		assertAbsent(t, filepath.Join(parsed, "95169560.json"))
	})

	t.Run("existing batch distribution skips fm", func(t *testing.T) {
		keyring.MockInit()
		dir := t.TempDir()
		parsed := makeParsed(t, dir)
		analyzed := filepath.Join(dir, "batch", "jobads", "analyzed")
		if err := os.Mkdir(analyzed, 0o700); err != nil {
			t.Fatal(err)
		}
		body := jobFileBytes(t, doc)
		writeAt(t, filepath.Join(parsed, "a.json"), body)
		marker := []byte("already-dist")
		writeAt(t, filepath.Join(analyzed, "a-tech-dist.md"), marker)
		var calls [][]string
		scriptedFM(t, &calls, fmStep{}, fmStep{stdout: model + "\n"})
		stdout, stderr, code := runBatch(t, dir, []string{"analyze", "--batch"}, deps{
			GeminiClient: blockGemini(t),
		})
		want := "docs-cli: analyzed file already exists: " + filepath.Join("batch", "jobads", "analyzed", "a-tech-dist.md") + "\n"
		if code != 1 || stdout != "" || stderr != want {
			t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
		}
		assertFileBytes(t, filepath.Join(analyzed, "a-tech-dist.md"), marker)
		assertFileBytes(t, filepath.Join(parsed, "a.json"), body)
		assertAbsent(t, filepath.Join(analyzed, "a-analysis.json"))
		assertAbsent(t, filepath.Join(analyzed, "a-tech.md"))
		if len(calls) != 0 {
			t.Fatalf("calls %#v", calls)
		}
	})

	t.Run("batch distribution write failure removes the new files", func(t *testing.T) {
		keyring.MockInit()
		dir := t.TempDir()
		parsed := makeParsed(t, dir)
		analyzed := filepath.Join(dir, "batch", "jobads", "analyzed")
		body := jobFileBytes(t, doc)
		writeAt(t, filepath.Join(parsed, "a.json"), body)
		techDistWrite = func(string, []byte) error { return errors.New("disk full") }
		t.Cleanup(func() { techDistWrite = nil })
		var calls [][]string
		scriptedFM(t, &calls, fmStep{}, fmStep{stdout: model + "\n"})
		stdout, stderr, code := runBatch(t, dir, []string{"analyze", "--batch"}, deps{
			GeminiClient: blockGemini(t),
		})
		if code != 1 || stdout != "" || stderr != "docs-cli: error: disk full\n" {
			t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
		}
		assertFileBytes(t, filepath.Join(parsed, "a.json"), body)
		assertAbsent(t, filepath.Join(analyzed, "a-analysis.json"))
		assertAbsent(t, filepath.Join(analyzed, "a-tech.md"))
		assertAbsent(t, filepath.Join(analyzed, "a-tech-dist.md"))
		assertAbsent(t, filepath.Join(analyzed, "a.json"))
		if len(calls) != 2 {
			t.Fatalf("calls %#v", calls)
		}
	})

	t.Run("old batch svg does not block markdown", func(t *testing.T) {
		keyring.MockInit()
		dir := t.TempDir()
		parsed := makeParsed(t, dir)
		analyzed := filepath.Join(dir, "batch", "jobads", "analyzed")
		if err := os.Mkdir(analyzed, 0o700); err != nil {
			t.Fatal(err)
		}
		body := jobFileBytes(t, doc)
		writeAt(t, filepath.Join(parsed, "a.json"), body)
		old := filepath.Join(analyzed, "a-tech.svg")
		marker := []byte("<svg>old</svg>")
		writeAt(t, old, marker)
		var calls [][]string
		scriptedFM(t, &calls, fmStep{}, fmStep{stdout: model + "\n"})
		stdout, stderr, code := runBatch(t, dir, []string{"analyze", "--batch"}, deps{
			GeminiClient: blockGemini(t),
		})
		analysisAbs, err := filepath.Abs(filepath.Join(analyzed, "a-analysis.json"))
		if err != nil {
			t.Fatal(err)
		}
		diagramAbs, err := filepath.Abs(filepath.Join(analyzed, "a-tech.md"))
		if err != nil {
			t.Fatal(err)
		}
		distAbs, err := filepath.Abs(filepath.Join(analyzed, "a-tech-dist.md"))
		if err != nil {
			t.Fatal(err)
		}
		if code != 0 || stderr != "" || stdout != analysisAbs+"\n"+diagramAbs+"\n"+distAbs+"\n" {
			t.Fatalf("exit %d stderr %q stdout %q", code, stderr, stdout)
		}
		assertFileBytes(t, old, marker)
		diagram, err := os.ReadFile(filepath.Join(analyzed, "a-tech.md"))
		if err != nil || !bytes.Contains(diagram, []byte("flowchart LR")) {
			t.Fatalf("diagram %q err %v", diagram, err)
		}
		if len(calls) != 2 {
			t.Fatalf("calls %#v", calls)
		}
	})

	t.Run("diagram write failure removes the analysis", func(t *testing.T) {
		keyring.MockInit()
		dir := t.TempDir()
		parsed := makeParsed(t, dir)
		analyzed := filepath.Join(dir, "batch", "jobads", "analyzed")
		body := jobFileBytes(t, doc)
		writeAt(t, filepath.Join(parsed, "a.json"), body)
		techDiagramWrite = func(string, []byte) error { return errors.New("disk full") }
		t.Cleanup(func() { techDiagramWrite = nil })
		var calls [][]string
		scriptedFM(t, &calls, fmStep{}, fmStep{stdout: model + "\n"})
		stdout, stderr, code := runBatch(t, dir, []string{"analyze", "--batch"}, deps{
			GeminiClient: blockGemini(t),
		})
		if code != 1 || stdout != "" || stderr != "docs-cli: error: disk full\n" {
			t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
		}
		assertFileBytes(t, filepath.Join(parsed, "a.json"), body)
		assertAbsent(t, filepath.Join(analyzed, "a-analysis.json"))
		assertAbsent(t, filepath.Join(analyzed, "a-tech.md"))
		assertAbsent(t, filepath.Join(analyzed, "a-tech.svg"))
		assertAbsent(t, filepath.Join(analyzed, "a-tech-dist.md"))
		assertAbsent(t, filepath.Join(analyzed, "a.json"))
		if len(calls) != 2 {
			t.Fatalf("calls %#v", calls)
		}
	})
}

func TestDraftCommandFakeFM(t *testing.T) {
	keyring.MockInit()
	const apiKey = "draft-must-not-read-key"
	if err := keyring.Set("docs-cli", "refresh-token", testRefreshItem); err != nil {
		t.Fatal(err)
	}
	if err := jobad.StoreGeminiKey(apiKey); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	t.Chdir(work)

	doc := jobad.Document{
		Source: "source-token-9f3a.html",
		Header: jobad.Header{
			TitleBlock:     "System Analyst",
			Company:        "COMPANY-TOKEN",
			Location:       "LOCATION-TOKEN",
			WorkType:       "WORKTYPE-TOKEN",
			Classification: "CLASS-TOKEN",
		},
		Benefits:     []string{"BENEFITS-TOKEN"},
		Duties:       []string{},
		Requirements: []string{"person@example.com", "Know Go"},
		Trailer:      []string{"TRAILER-SENTINEL"},
	}

	t.Run("keeps trailing newlines", func(t *testing.T) {
		root := t.TempDir()
		entries, err := os.ReadDir(root)
		if err != nil || len(entries) != 0 {
			t.Fatalf("program dir %v err %v", entries, err)
		}
		template := writeDraftEmailPDF(t)
		cv := writeDraftCVPDF(t)
		path := writeJobJSON(t, doc)
		var calls [][]string
		scriptedFM(t, &calls,
			fmStep{stdout: "not-a-letter", stderr: "available noise"},
			fmStep{stdout: "{\"letter\":\"hi\\n\\n\"}\n", stderr: "respond noise"},
		)
		stdout, stderr, code := runDraftArgs(t, root, path, template, cv)
		if code != 0 || stderr != "" {
			t.Fatalf("exit %d stderr %q stdout %q", code, stderr, stdout)
		}
		gotPath := strings.TrimSuffix(stdout, "\n")
		if stdout != gotPath+"\n" || !filepath.IsAbs(gotPath) {
			t.Fatalf("stdout %q", stdout)
		}
		if strings.Contains(stdout, "hi\n\n") || strings.Contains(stdout, "{") {
			t.Fatalf("stdout contains the letter %q", stdout)
		}
		drafts := filepath.Join(root, "drafts")
		if filepath.Dir(gotPath) != drafts {
			t.Fatalf("path %s drafts %s", gotPath, drafts)
		}
		info, err := os.Stat(drafts)
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
			t.Fatalf("drafts mode %v err %v", info, err)
		}
		body, err := os.ReadFile(gotPath)
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != "hi\n\n" {
			t.Fatalf("letter %q", body)
		}
		fileInfo, err := os.Stat(gotPath)
		if err != nil || fileInfo.Mode().Perm() != 0o600 {
			t.Fatalf("file mode %v err %v", fileInfo, err)
		}
		names, err := os.ReadDir(drafts)
		if err != nil || len(names) != 1 || names[0].Name() != filepath.Base(gotPath) {
			t.Fatalf("drafts %#v err %v", names, err)
		}
		if ok, _ := regexp.MatchString(`^\d{8}T\d{6}\.\d{3}\.txt$`, names[0].Name()); !ok {
			t.Fatalf("name %s", names[0].Name())
		}
		if len(calls) != 2 || !slices.Equal(calls[0], []string{"/usr/bin/fm", "available"}) {
			t.Fatalf("calls %#v", calls)
		}
		prefix := []string{
			"/usr/bin/fm", "respond", "--no-stream", "--use-case", "general",
			"--instructions", jobad.DraftInstructions,
			"--schema", jobad.LetterSchema,
			"--text",
		}
		if len(calls[1]) != len(prefix)+1 || !slices.Equal(calls[1][:len(prefix)], prefix) {
			t.Fatalf("respond %#v", calls[1])
		}
		text := calls[1][len(calls[1])-1]
		if !strings.Contains(text, "ada@example.com") || !strings.Contains(text, "Know Go") {
			t.Fatalf("text %q", text)
		}
		if !strings.Contains(text, "Duties:\n(none)") || !strings.Contains(text, "Company:\nCOMPANY-TOKEN") {
			t.Fatalf("text %q", text)
		}
		if !strings.Contains(text, "CV:\nCV-SKILL-TOKEN ada@example.com\n") || strings.Index(text, "CV:\n") > strings.Index(text, "Template:\n") {
			t.Fatalf("text %q", text)
		}
		for _, banned := range []string{
			apiKey, "person@example.com", "TRAILER-SENTINEL", "BENEFITS-TOKEN",
			"LOCATION-TOKEN", "WORKTYPE-TOKEN", "CLASS-TOKEN", doc.Source,
			jobad.AnalysisSchema,
		} {
			if strings.Contains(text, banned) {
				t.Fatalf("text contains %q", banned)
			}
		}
		assertNoSecret(t, stdout, stderr, apiKey)
		assertRefreshUntouched(t)
		raw, err := keyring.Get("docs-cli", "gemini-api-key")
		if err != nil || raw != `{"api_key":"`+apiKey+`"}` {
			t.Fatalf("gemini key %q err %v", raw, err)
		}
		workEntries, err := os.ReadDir(work)
		if err != nil || len(workEntries) != 0 {
			t.Fatalf("workdir %#v err %v", workEntries, err)
		}
	})

	t.Run("adds a newline", func(t *testing.T) {
		root := t.TempDir()
		template := writeDraftEmailPDF(t)
		cv := writeDraftCVPDF(t)
		path := writeJobJSON(t, jobad.Document{Requirements: []string{"Know Go"}})
		var calls [][]string
		scriptedFM(t, &calls, fmStep{}, fmStep{stdout: `{"letter":"Hi there"}`})
		stdout, stderr, code := runDraftArgs(t, root, path, template, cv)
		if code != 0 || stderr != "" {
			t.Fatalf("exit %d stderr %q", code, stderr)
		}
		gotPath := strings.TrimSuffix(stdout, "\n")
		body, err := os.ReadFile(gotPath)
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != "Hi there\n" || strings.Contains(stdout, "Hi there") {
			t.Fatalf("body %q stdout %q", body, stdout)
		}
	})

	t.Run("existing name", func(t *testing.T) {
		root := t.TempDir()
		fixed := time.Date(2026, 10, 3, 15, 4, 5, 123_000_000, time.Local)
		prev := draftClock
		draftClock = func() time.Time { return fixed }
		t.Cleanup(func() { draftClock = prev })
		stamp := fixed.Local().Format("20060102T150405.000")
		drafts := filepath.Join(root, "drafts")
		if err := os.MkdirAll(drafts, 0o700); err != nil {
			t.Fatal(err)
		}
		kept := filepath.Join(drafts, stamp+".txt")
		if err := os.WriteFile(kept, []byte("kept\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		template := writeDraftEmailPDF(t)
		cv := writeDraftCVPDF(t)
		path := writeJobJSON(t, jobad.Document{Requirements: []string{"Know Go"}})
		var calls [][]string
		scriptedFM(t, &calls, fmStep{}, fmStep{stdout: "{\"letter\":\"hi\\n\\n\"}\n"})
		stdout, stderr, code := runDraftArgs(t, root, path, template, cv)
		if code != 0 || stderr != "" {
			t.Fatalf("exit %d stderr %q", code, stderr)
		}
		gotPath := strings.TrimSuffix(stdout, "\n")
		if filepath.Base(gotPath) != stamp+"-2.txt" {
			t.Fatalf("name %s", gotPath)
		}
		body, err := os.ReadFile(gotPath)
		if err != nil || string(body) != "hi\n\n" {
			t.Fatalf("body %q err %v", body, err)
		}
		old, err := os.ReadFile(kept)
		if err != nil || string(old) != "kept\n" {
			t.Fatalf("kept %q err %v", old, err)
		}
	})
}

func TestDraftUsesWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	SetDraftRootForTest("")
	t.Cleanup(restoreDraftRoot)
	path := writeJobJSON(t, jobad.Document{Requirements: []string{"Know Go"}})
	template := writeDraftEmailPDF(t)
	cv := writeDraftCVPDF(t)
	var calls [][]string
	scriptedFM(t, &calls, fmStep{}, fmStep{stdout: `{"letter":"Hi there"}`})
	stdout, stderr, code := runDraftArgs(t, "", path, template, cv)
	if code != 0 || stderr != "" {
		t.Fatalf("exit %d stderr %q stdout %q", code, stderr, stdout)
	}
	gotPath := strings.TrimSuffix(stdout, "\n")
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	drafts := filepath.Join(wd, "drafts")
	if filepath.Dir(gotPath) != drafts {
		t.Fatalf("path %s want %s", gotPath, drafts)
	}
	body, err := os.ReadFile(gotPath)
	if err != nil || string(body) != "Hi there\n" {
		t.Fatalf("body %q err %v", body, err)
	}
}

func TestDraftEmptyRequirementsBeforeTemplate(t *testing.T) {
	rejectFM(t)
	docs := []jobad.Document{
		{},
		{Requirements: []string{}, Duties: []string{"Build services"}},
		{Requirements: []string{"person@example.com"}},
	}
	for _, doc := range docs {
		root := t.TempDir()
		template := filepath.Join(t.TempDir(), "template-not-opened.pdf")
		cv := filepath.Join(t.TempDir(), "cv-not-opened.pdf")
		path := writeJobJSON(t, doc)
		stdout, stderr, code := runDraftArgs(t, root, path, template, cv)
		if code != 1 || stdout != "" || stderr != "docs-cli: requirements is empty\n" {
			t.Fatalf("doc %#v exit %d stdout %q stderr %q", doc.Requirements, code, stdout, stderr)
		}
		if strings.Contains(stderr, "template-not-opened.pdf") || strings.Contains(stderr, "cv-not-opened.pdf") {
			t.Fatalf("stderr %q", stderr)
		}
		assertDraftsEmpty(t, root)
	}
}

func TestDraftMissingJSONBeforeTemplate(t *testing.T) {
	rejectFM(t)
	root := t.TempDir()
	jsonPath := filepath.Join(t.TempDir(), "missing.json")
	template := filepath.Join(t.TempDir(), "template-not-opened.pdf")
	cv := filepath.Join(t.TempDir(), "cv-not-opened.pdf")
	stdout, stderr, code := runDraftArgs(t, root, jsonPath, template, cv)
	if code != 1 || stdout != "" || !strings.Contains(stderr, "docs-cli: error:") || !strings.Contains(stderr, jsonPath) {
		t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	if strings.Contains(stderr, template) || strings.Contains(stderr, cv) || strings.Contains(stderr, "template has no text") {
		t.Fatalf("stderr %q", stderr)
	}
	assertDraftsEmpty(t, root)
}

func TestDraftInvalidJSONBeforeTemplate(t *testing.T) {
	rejectFM(t)
	root := t.TempDir()
	jsonPath := writeFile(t, "bad.json", []byte("{"))
	template := filepath.Join(t.TempDir(), "template-not-opened.pdf")
	cv := filepath.Join(t.TempDir(), "cv-not-opened.pdf")
	stdout, stderr, code := runDraftArgs(t, root, jsonPath, template, cv)
	if code != 1 || stdout != "" || !strings.Contains(stderr, "docs-cli: error:") || !strings.Contains(stderr, jsonPath) {
		t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	if strings.Contains(stderr, template) || strings.Contains(stderr, cv) {
		t.Fatalf("stderr %q", stderr)
	}
	assertDraftsEmpty(t, root)
}

func TestDraftTemplateTooLarge(t *testing.T) {
	rejectFM(t)
	root := t.TempDir()
	path := writeJobJSON(t, jobad.Document{Requirements: []string{"Know Go"}})
	template := writeFile(t, "over.bin", bytes.Repeat([]byte{'x'}, maxJSONBytes+1))
	cv := filepath.Join(t.TempDir(), "cv-not-opened.pdf")
	stdout, stderr, code := runDraftArgs(t, root, path, template, cv)
	if code != 1 || stdout != "" || stderr != "docs-cli: template exceeds 1 MiB\n" || strings.Contains(stderr, cv) {
		t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	assertDraftsEmpty(t, root)
}

func TestDraftTemplateNoText(t *testing.T) {
	rejectFM(t)
	root := t.TempDir()
	path := writeJobJSON(t, jobad.Document{Requirements: []string{"Know Go"}})
	template := writeFile(t, "note.txt", []byte("not a pdf"))
	cv := filepath.Join(t.TempDir(), "cv-not-opened.pdf")
	stdout, stderr, code := runDraftArgs(t, root, path, template, cv)
	if code != 1 || stdout != "" || stderr != "docs-cli: template has no text\n" || strings.Contains(stderr, cv) {
		t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	assertDraftsEmpty(t, root)
}

func TestDraftTemplateNotRegular(t *testing.T) {
	rejectFM(t)
	root := t.TempDir()
	path := writeJobJSON(t, jobad.Document{Requirements: []string{"Know Go"}})
	template := t.TempDir()
	cv := filepath.Join(t.TempDir(), "cv-not-opened.pdf")
	stdout, stderr, code := runDraftArgs(t, root, path, template, cv)
	if code != 1 || stdout != "" || stderr != "docs-cli: template has no text\n" || strings.Contains(stderr, cv) {
		t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	assertDraftsEmpty(t, root)
}

func TestDraftTemplateMissing(t *testing.T) {
	rejectFM(t)
	root := t.TempDir()
	path := writeJobJSON(t, jobad.Document{Requirements: []string{"Know Go"}})
	template := filepath.Join(t.TempDir(), "missing.pdf")
	cv := filepath.Join(t.TempDir(), "cv-not-opened.pdf")
	stdout, stderr, code := runDraftArgs(t, root, path, template, cv)
	if code != 1 || stdout != "" || !strings.Contains(stderr, "docs-cli: error:") || !strings.Contains(stderr, template) {
		t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	if strings.Contains(stderr, "template has no text") || strings.Contains(stderr, cv) {
		t.Fatalf("stderr %q", stderr)
	}
	assertDraftsEmpty(t, root)
}

func TestDraftCVTooLarge(t *testing.T) {
	rejectFM(t)
	root := t.TempDir()
	path := writeJobJSON(t, jobad.Document{Requirements: []string{"Know Go"}})
	template := writeDraftEmailPDF(t)
	cv := writeFile(t, "cv-over.bin", bytes.Repeat([]byte{'x'}, maxJSONBytes+1))
	stdout, stderr, code := runDraftArgs(t, root, path, template, cv)
	if code != 1 || stdout != "" || stderr != "docs-cli: cv exceeds 1 MiB\n" {
		t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	assertDraftsEmpty(t, root)
}

func TestDraftCVNoText(t *testing.T) {
	rejectFM(t)
	root := t.TempDir()
	path := writeJobJSON(t, jobad.Document{Requirements: []string{"Know Go"}})
	template := writeDraftEmailPDF(t)
	cv := writeFile(t, "note.txt", []byte("not a pdf"))
	stdout, stderr, code := runDraftArgs(t, root, path, template, cv)
	if code != 1 || stdout != "" || stderr != "docs-cli: cv has no text\n" {
		t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	assertDraftsEmpty(t, root)
}

func TestDraftCVNotRegular(t *testing.T) {
	rejectFM(t)
	root := t.TempDir()
	path := writeJobJSON(t, jobad.Document{Requirements: []string{"Know Go"}})
	template := writeDraftEmailPDF(t)
	cv := t.TempDir()
	stdout, stderr, code := runDraftArgs(t, root, path, template, cv)
	if code != 1 || stdout != "" || stderr != "docs-cli: cv has no text\n" {
		t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	assertDraftsEmpty(t, root)
}

func TestDraftCVMissing(t *testing.T) {
	rejectFM(t)
	root := t.TempDir()
	path := writeJobJSON(t, jobad.Document{Requirements: []string{"Know Go"}})
	template := writeDraftEmailPDF(t)
	cv := filepath.Join(t.TempDir(), "missing-cv.pdf")
	stdout, stderr, code := runDraftArgs(t, root, path, template, cv)
	if code != 1 || stdout != "" || !strings.Contains(stderr, "docs-cli: error:") || !strings.Contains(stderr, cv) {
		t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	if strings.Contains(stderr, "cv has no text") {
		t.Fatalf("stderr %q", stderr)
	}
	assertDraftsEmpty(t, root)
}

func TestDraftSamePath(t *testing.T) {
	root := t.TempDir()
	path := writeJobJSON(t, jobad.Document{Requirements: []string{"Know Go"}})
	pdf := writeDraftEmailPDF(t)
	var calls [][]string
	scriptedFM(t, &calls, fmStep{}, fmStep{stdout: `{"letter":"Hi there"}`})
	stdout, stderr, code := runDraftArgs(t, root, path, pdf, pdf)
	if code != 0 || stderr != "" {
		t.Fatalf("exit %d stderr %q stdout %q", code, stderr, stdout)
	}
	if len(calls) != 2 {
		t.Fatalf("calls %#v", calls)
	}
	text := calls[1][len(calls[1])-1]
	if strings.Count(text, "ada@example.com") != 2 {
		t.Fatalf("text %q", text)
	}
}

func TestDraftArgLimit(t *testing.T) {
	rejectFM(t)
	root := t.TempDir()
	doc := jobad.Document{Requirements: []string{strings.Repeat("Q", 917000), "Know Go"}}
	path := writeJobJSON(t, doc)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() >= int64(maxJSONBytes) || info.Size() <= 917000 {
		t.Fatalf("json size %d", info.Size())
	}
	stdout, stderr, code := runDraftArgs(t, root, path, coverMinPath(t), coverMinPath(t))
	if code != 1 || stdout != "" || stderr != "docs-cli: fm text exceeds the argument limit\n" {
		t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	assertDraftsEmpty(t, root)
}

func TestDraftFMAvailableFails(t *testing.T) {
	path := writeJobJSON(t, jobad.Document{Requirements: []string{"Know Go"}})
	template := writeDraftEmailPDF(t)
	cv := writeDraftCVPDF(t)

	t.Run("available", func(t *testing.T) {
		root := t.TempDir()
		var calls [][]string
		scriptedFM(t, &calls, fmStep{stdout: `{"letter":"hi"}`, stderr: "not licensed\n", code: 1})
		stdout, stderr, code := runDraftArgs(t, root, path, template, cv)
		if code != 1 || stdout != "" || stderr != "docs-cli: fm: not licensed\n" {
			t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
		}
		if len(calls) != 1 || !slices.Equal(calls[0], []string{"/usr/bin/fm", "available"}) {
			t.Fatalf("calls %#v", calls)
		}
		assertDraftsEmpty(t, root)
	})

	t.Run("respond", func(t *testing.T) {
		root := t.TempDir()
		var calls [][]string
		scriptedFM(t, &calls,
			fmStep{},
			fmStep{stdout: `{"letter":"hi"}`, stderr: "guardrail rejected\n", code: 1},
		)
		stdout, stderr, code := runDraftArgs(t, root, path, template, cv)
		if code != 1 || stdout != "" || stderr != "docs-cli: fm: guardrail rejected\n" {
			t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
		}
		if len(calls) != 2 || calls[1][1] != "respond" {
			t.Fatalf("calls %#v", calls)
		}
		assertDraftsEmpty(t, root)
	})
}

func TestDraftRejectsModelProse(t *testing.T) {
	root := t.TempDir()
	path := writeJobJSON(t, jobad.Document{Requirements: []string{"Know Go"}})
	template := writeDraftEmailPDF(t)
	cv := writeDraftCVPDF(t)
	var calls [][]string
	scriptedFM(t, &calls, fmStep{}, fmStep{stdout: "The letter is ready.\n"})
	stdout, stderr, code := runDraftArgs(t, root, path, template, cv)
	if code != 1 || stdout != "" || stderr != "docs-cli: fm output is not the draft schema\n" {
		t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	if len(calls) != 2 || calls[1][1] != "respond" {
		t.Fatalf("calls %#v", calls)
	}
	if strings.Contains(stdout+stderr, "The letter is ready.") {
		t.Fatalf("forwarded model text stdout %q stderr %q", stdout, stderr)
	}
	assertDraftsEmpty(t, root)
}

func TestDraftUsage(t *testing.T) {
	const leaked = "super-secret-gemini-key"
	rejectFM(t)
	for _, tt := range draftUsageCases() {
		t.Run(tt.name, func(t *testing.T) {
			keyring.MockInit()
			root := t.TempDir()
			SetDraftRootForTest(root)
			t.Cleanup(restoreDraftRoot)
			var stdout, stderr bytes.Buffer
			code := run(tt.args, deps{
				Stdin:  strings.NewReader(leaked),
				Stdout: &stdout,
				Stderr: &stderr,
			})
			if code != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "usage: docs-cli") {
				t.Fatalf("exit %d stdout %q stderr %q", code, stdout.String(), stderr.String())
			}
			if tt.exact && stderr.String() != usageText {
				t.Fatalf("stderr %q", stderr.String())
			}
			if strings.Contains(stdout.String()+stderr.String(), leaked) {
				t.Fatal("leaked the key")
			}
			if _, err := keyring.Get("docs-cli", "gemini-api-key"); !errors.Is(err, keyring.ErrNotFound) {
				t.Fatalf("keyring err %v", err)
			}
		})
	}
	if !strings.Contains(usageText, "\n  analyze --json PATH [--gemini]\n  analyze --batch [--gemini]\n  draft --json PATH --template PATH --cv PATH [--pdf NAME]\n") {
		t.Fatalf("usage commands:\n%s", usageText)
	}
	if !strings.Contains(usageText, "\n  --template PATH  cover-letter template PDF\n  --cv PATH        CV or resume PDF\n  --pdf NAME       PDF file name in drafts; omitted means a timestamped .txt file\n") {
		t.Fatalf("usage draft flags:\n%s", usageText)
	}
}

func TestDraftUsageDoesNotCreateDrafts(t *testing.T) {
	rejectFM(t)
	root := t.TempDir()
	SetDraftRootForTest(root)
	t.Cleanup(restoreDraftRoot)
	for _, tt := range draftUsageCases() {
		var stdout, stderr bytes.Buffer
		code := run(tt.args, deps{
			Stdin:  strings.NewReader(""),
			Stdout: &stdout,
			Stderr: &stderr,
		})
		if code != 2 || stdout.Len() != 0 {
			t.Fatalf("%s exit %d stdout %q stderr %q", tt.name, code, stdout.String(), stderr.String())
		}
		if _, err := os.Stat(filepath.Join(root, "drafts")); !os.IsNotExist(err) {
			t.Fatalf("%s created drafts: %v", tt.name, err)
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("program dir %#v err %v", entries, err)
	}
}

func TestDraftRejectsDraftsFile(t *testing.T) {
	rejectFM(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "drafts"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	jsonPath := filepath.Join(root, "missing.json")
	template := filepath.Join(root, "missing.pdf")
	cv := filepath.Join(root, "missing-cv.pdf")
	stdout, stderr, code := runDraftArgs(t, root, jsonPath, template, cv)
	if code != 1 || stdout != "" || stderr != "docs-cli: drafts is not a directory\n" {
		t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	info, err := os.Stat(filepath.Join(root, "drafts"))
	if err != nil || info.IsDir() {
		t.Fatalf("drafts %v err %v", info, err)
	}
}

func TestDraftJSONTooLarge(t *testing.T) {
	rejectFM(t)
	root := t.TempDir()
	path := writeFile(t, "over.json", bytes.Repeat([]byte{'x'}, maxJSONBytes+1))
	template := filepath.Join(t.TempDir(), "template-not-opened.pdf")
	cv := filepath.Join(t.TempDir(), "cv-not-opened.pdf")
	stdout, stderr, code := runDraftArgs(t, root, path, template, cv)
	if code != 1 || stdout != "" || stderr != errJSONTooLarge+"\n" {
		t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	if strings.Contains(stderr, template) || strings.Contains(stderr, cv) {
		t.Fatalf("stderr %q", stderr)
	}
	assertDraftsEmpty(t, root)
}

func TestDraftPDF(t *testing.T) {
	doc := jobad.Document{Requirements: []string{"Know Go"}}

	t.Run("writes only the pdf", func(t *testing.T) {
		root := t.TempDir()
		path := writeJobJSON(t, doc)
		template := writeDraftEmailPDF(t)
		cv := writeDraftCVPDF(t)
		var calls [][]string
		scriptedFM(t, &calls, fmStep{}, fmStep{stdout: `{"letter":"Dear team,\n\nI write to apply.\n"}`})
		stdout, stderr, code := runDraftPDFArgs(t, root, path, template, cv, "customname.pdf")
		if code != 0 || stderr != "" {
			t.Fatalf("exit %d stderr %q stdout %q", code, stderr, stdout)
		}
		gotPath := strings.TrimSuffix(stdout, "\n")
		if stdout != gotPath+"\n" || !filepath.IsAbs(gotPath) || strings.Contains(stdout, "Dear team") {
			t.Fatalf("stdout %q", stdout)
		}
		drafts := filepath.Join(root, "drafts")
		if filepath.Dir(gotPath) != drafts || filepath.Base(gotPath) != "customname.pdf" {
			t.Fatalf("path %s", gotPath)
		}
		info, err := os.Stat(gotPath)
		if err != nil || info.Mode().Perm() != 0o600 || info.IsDir() {
			t.Fatalf("mode %v err %v", info, err)
		}
		names, err := os.ReadDir(drafts)
		if err != nil || len(names) != 1 || names[0].Name() != "customname.pdf" {
			t.Fatalf("drafts %#v err %v", names, err)
		}
		body, err := os.ReadFile(gotPath)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(body, []byte("(Dear team,) Tj\n0 -20 Td\n0 -20 Td\n(I write to apply.) Tj\n")) {
			t.Fatal("content stream")
		}
		if !bytes.Contains(buildParenthesisPDF(t), []byte(`\(`)) {
			t.Fatal("parenthesis was not escaped")
		}
		r, err := pdf.NewReader(bytes.NewReader(body), int64(len(body)))
		if err != nil || r.NumPage() != 1 {
			t.Fatalf("pages %v err %v", r, err)
		}
		if len(calls) != 2 || calls[1][1] != "respond" {
			t.Fatalf("calls %#v", calls)
		}
		prefix := []string{
			"/usr/bin/fm", "respond", "--no-stream", "--use-case", "general",
			"--instructions", jobad.DraftInstructions,
			"--schema", jobad.LetterSchema,
		}
		if len(calls[1]) < len(prefix) || !slices.Equal(calls[1][:len(prefix)], prefix) {
			t.Fatalf("respond %#v", calls[1])
		}
	})

	t.Run("space in name", func(t *testing.T) {
		root := t.TempDir()
		path := writeJobJSON(t, doc)
		template := writeDraftEmailPDF(t)
		cv := writeDraftCVPDF(t)
		var calls [][]string
		scriptedFM(t, &calls, fmStep{}, fmStep{stdout: `{"letter":"Hi there"}`})
		stdout, stderr, code := runDraftPDFArgs(t, root, path, template, cv, "My Letter.pdf")
		if code != 0 || stderr != "" || filepath.Base(strings.TrimSuffix(stdout, "\n")) != "My Letter.pdf" {
			t.Fatalf("exit %d stderr %q stdout %q", code, stderr, stdout)
		}
		names, err := os.ReadDir(filepath.Join(root, "drafts"))
		if err != nil || len(names) != 1 || names[0].Name() != "My Letter.pdf" {
			t.Fatalf("drafts %#v err %v", names, err)
		}
	})

	t.Run("rejects names before drafts", func(t *testing.T) {
		rejectFM(t)
		for _, name := range []string{"../x.pdf", "/tmp/x.pdf", "customname.txt", "Custom.PDF"} {
			root := t.TempDir()
			path := filepath.Join(t.TempDir(), "missing.json")
			template := filepath.Join(t.TempDir(), "missing-template.pdf")
			cv := filepath.Join(t.TempDir(), "missing-cv.pdf")
			stdout, stderr, code := runDraftPDFArgs(t, root, path, template, cv, name)
			if code != 1 || stdout != "" || stderr != "docs-cli: not a pdf file: "+name+"\n" {
				t.Fatalf("%q exit %d stdout %q stderr %q", name, code, stdout, stderr)
			}
			if _, err := os.Stat(filepath.Join(root, "drafts")); !os.IsNotExist(err) {
				t.Fatalf("%q created drafts: %v", name, err)
			}
		}
	})

	t.Run("existing file", func(t *testing.T) {
		rejectFM(t)
		root := t.TempDir()
		drafts := filepath.Join(root, "drafts")
		if err := os.MkdirAll(drafts, 0o700); err != nil {
			t.Fatal(err)
		}
		kept := []byte("KEEP-BYTES")
		dest := filepath.Join(drafts, "customname.pdf")
		if err := os.WriteFile(dest, kept, 0o600); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), "missing.json")
		template := filepath.Join(t.TempDir(), "missing-template.pdf")
		cv := filepath.Join(t.TempDir(), "missing-cv.pdf")
		stdout, stderr, code := runDraftPDFArgs(t, root, path, template, cv, "customname.pdf")
		if code != 1 || stdout != "" || stderr != "docs-cli: pdf already exists: customname.pdf\n" {
			t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
		}
		again, err := os.ReadFile(dest)
		if err != nil || !bytes.Equal(again, kept) {
			t.Fatalf("bytes %q err %v", again, err)
		}
	})

	t.Run("existing symlink", func(t *testing.T) {
		rejectFM(t)
		root := t.TempDir()
		drafts := filepath.Join(root, "drafts")
		if err := os.MkdirAll(drafts, 0o700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(t.TempDir(), "real.pdf")
		kept := []byte("KEEP")
		if err := os.WriteFile(target, kept, 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(drafts, "customname.pdf")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), "missing.json")
		template := filepath.Join(t.TempDir(), "missing-template.pdf")
		cv := filepath.Join(t.TempDir(), "missing-cv.pdf")
		stdout, stderr, code := runDraftPDFArgs(t, root, path, template, cv, "customname.pdf")
		if code != 1 || stdout != "" || stderr != "docs-cli: pdf already exists: customname.pdf\n" {
			t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
		}
		info, err := os.Lstat(link)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("link %v err %v", info, err)
		}
		again, err := os.ReadFile(target)
		if err != nil || !bytes.Equal(again, kept) {
			t.Fatalf("target %q err %v", again, err)
		}
	})

	t.Run("broken symlink", func(t *testing.T) {
		rejectFM(t)
		root := t.TempDir()
		drafts := filepath.Join(root, "drafts")
		if err := os.MkdirAll(drafts, 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(drafts, "customname.pdf")
		if err := os.Symlink(filepath.Join(t.TempDir(), "gone.pdf"), link); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), "missing.json")
		template := filepath.Join(t.TempDir(), "missing-template.pdf")
		cv := filepath.Join(t.TempDir(), "missing-cv.pdf")
		stdout, stderr, code := runDraftPDFArgs(t, root, path, template, cv, "customname.pdf")
		if code != 1 || stdout != "" || stderr != "docs-cli: pdf already exists: customname.pdf\n" {
			t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
		}
		info, err := os.Lstat(link)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("link %v err %v", info, err)
		}
	})

	t.Run("not encodable", func(t *testing.T) {
		root := t.TempDir()
		path := writeJobJSON(t, doc)
		template := writeDraftEmailPDF(t)
		cv := writeDraftCVPDF(t)
		var calls [][]string
		scriptedFM(t, &calls, fmStep{}, fmStep{stdout: `{"letter":"你好"}`})
		stdout, stderr, code := runDraftPDFArgs(t, root, path, template, cv, "customname.pdf")
		if code != 1 || stdout != "" || stderr != "docs-cli: pdf text is not encodable\n" {
			t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
		}
		if len(calls) != 2 {
			t.Fatalf("calls %#v", calls)
		}
		assertDraftsEmpty(t, root)
	})

	t.Run("empty requirements", func(t *testing.T) {
		rejectFM(t)
		root := t.TempDir()
		path := writeJobJSON(t, jobad.Document{})
		template := filepath.Join(t.TempDir(), "template-not-opened.pdf")
		cv := filepath.Join(t.TempDir(), "cv-not-opened.pdf")
		stdout, stderr, code := runDraftPDFArgs(t, root, path, template, cv, "customname.pdf")
		if code != 1 || stdout != "" || stderr != "docs-cli: requirements is empty\n" {
			t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
		}
		if strings.Contains(stderr, "template-not-opened.pdf") || strings.Contains(stderr, "cv-not-opened.pdf") {
			t.Fatalf("stderr %q", stderr)
		}
		assertDraftsEmpty(t, root)
		if _, err := os.Stat(filepath.Join(root, "drafts", "customname.pdf")); !os.IsNotExist(err) {
			t.Fatal(err)
		}
	})
}

func buildParenthesisPDF(t *testing.T) []byte {
	t.Helper()
	body, err := buildLetterPDF("See (Go).\n")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte(`\(Go\)`)) {
		t.Fatalf("stream %s", body)
	}
	return body
}

func draftUsageCases() []struct {
	name  string
	args  []string
	exact bool
} {
	return []struct {
		name  string
		args  []string
		exact bool
	}{
		{name: "no flag", args: []string{"draft"}},
		{name: "missing json", args: []string{"draft", "--json"}},
		{name: "missing template", args: []string{"draft", "--template"}},
		{name: "missing cv", args: []string{"draft", "--cv"}},
		{name: "empty json", args: []string{"draft", "--json", "", "--template", "letter.pdf", "--cv", "cv.pdf"}},
		{name: "empty template", args: []string{"draft", "--json", "job.json", "--template", "", "--cv", "cv.pdf"}},
		{name: "empty cv", args: []string{"draft", "--json", "job.json", "--template", "letter.pdf", "--cv", ""}},
		{name: "json only", args: []string{"draft", "--json", "job.json"}},
		{name: "template only", args: []string{"draft", "--template", "letter.pdf"}},
		{name: "json and template", args: []string{"draft", "--json", "job.json", "--template", "letter.pdf"}},
		{name: "unknown flag", args: []string{"draft", "--bogus"}},
		{name: "gemini flag", args: []string{"draft", "--json", "job.json", "--template", "letter.pdf", "--cv", "cv.pdf", "--gemini"}},
		{name: "out flag", args: []string{"draft", "--out", "letter.txt"}},
		{name: "extra arg", args: []string{"draft", "--json", "job.json", "--template", "letter.pdf", "--cv", "cv.pdf", "extra"}},
		{name: "missing pdf", args: []string{"draft", "--json", "job.json", "--template", "letter.pdf", "--cv", "cv.pdf", "--pdf"}},
		{name: "empty pdf", args: []string{"draft", "--json", "job.json", "--template", "letter.pdf", "--cv", "cv.pdf", "--pdf", ""}, exact: true},
		{name: "help", args: []string{"draft", "--help"}, exact: true},
		{name: "short help", args: []string{"draft", "-h"}, exact: true},
	}
}

func runDraftArgs(t *testing.T, root, jsonPath, templatePath, cvPath string) (string, string, int) {
	t.Helper()
	return runDraftCommand(t, root, []string{"draft", "--json", jsonPath, "--template", templatePath, "--cv", cvPath})
}

func runDraftPDFArgs(t *testing.T, root, jsonPath, templatePath, cvPath, pdfName string) (string, string, int) {
	t.Helper()
	return runDraftCommand(t, root, []string{"draft", "--json", jsonPath, "--template", templatePath, "--cv", cvPath, "--pdf", pdfName})
}

func runDraftCommand(t *testing.T, root string, args []string) (string, string, int) {
	t.Helper()
	SetDraftRootForTest(root)
	t.Cleanup(restoreDraftRoot)
	var stdout, stderr bytes.Buffer
	d := deps{
		Stdin:        strings.NewReader(""),
		Stdout:       &stdout,
		Stderr:       &stderr,
		Root:         t.TempDir(),
		Environ:      []string{"APP_ENV=production"},
		GeminiClient: blockGemini(t),
	}
	forbidEnv(t, &d)
	code := run(args, d)
	return stdout.String(), stderr.String(), code
}

func rejectFM(t *testing.T) {
	t.Helper()
	jobad.SetFMExecForTest(func(argv []string) ([]byte, []byte, int) {
		t.Errorf("fm called %q", argv)
		return nil, []byte("fm called"), 1
	})
	t.Cleanup(restoreFMStub)
}

func assertDraftsEmpty(t *testing.T, root string) {
	t.Helper()
	drafts := filepath.Join(root, "drafts")
	entries, err := os.ReadDir(drafts)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("drafts has %d entries", len(entries))
	}
}

func writeDraftEmailPDF(t *testing.T) string {
	t.Helper()
	body := draftTextPDF([]string{"ada@example.com"})
	text := string(body)
	if strings.Count(text, "Tj") != 1 || !strings.Contains(text, "(ada@example.com) Tj") {
		t.Fatalf("pdf Tj operands = %q", text)
	}
	return writeFile(t, "ada.pdf", body)
}

func writeDraftCVPDF(t *testing.T) string {
	t.Helper()
	return writeFile(t, "cv.pdf", draftTextPDF([]string{"CV-SKILL-TOKEN ada@example.com"}))
}

func coverMinPath(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller")
	}
	path := filepath.Join(filepath.Dir(file), "..", "..", "internal", "jobad", "testdata", "cover_min.pdf")
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	return path
}

func draftTextPDF(pages []string) []byte {
	streams := make([]string, len(pages))
	for i, text := range pages {
		if text != "" {
			streams[i] = "BT (" + text + ") Tj ET"
		}
	}
	return draftRawPDF(streams)
}

func draftRawPDF(streams []string) []byte {
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offsets := []int{0}
	writeObj := func(body string) {
		offsets = append(offsets, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", len(offsets)-1, body)
	}
	nPages := len(streams)
	kids := make([]string, nPages)
	for i := 0; i < nPages; i++ {
		kids[i] = fmt.Sprintf("%d 0 R", 3+2*i)
	}
	writeObj("<< /Type /Catalog /Pages 2 0 R >>")
	writeObj(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), nPages))
	for i, stream := range streams {
		contentNum := 4 + 2*i
		writeObj(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 144] /Contents %d 0 R >>", contentNum))
		writeObj(fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream))
	}
	xrefAt := b.Len()
	n := len(offsets)
	fmt.Fprintf(&b, "xref\n0 %d\n", n)
	fmt.Fprintf(&b, "0000000000 65535 f \n")
	for i := 1; i < n; i++ {
		fmt.Fprintf(&b, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\n", n)
	fmt.Fprintf(&b, "startxref\n%d\n%%%%EOF\n", xrefAt)
	return b.Bytes()
}

func runAnalyzeArgs(t *testing.T, path string) (string, string, int) {
	t.Helper()
	return runAnalyzeClient(t, path, blockGemini(t))
}

func runAnalyzeClient(t *testing.T, path string, client *http.Client) (string, string, int) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	d := deps{
		Stdin:        strings.NewReader(""),
		Stdout:       &stdout,
		Stderr:       &stderr,
		Root:         t.TempDir(),
		Environ:      []string{"APP_ENV=production"},
		GeminiClient: client,
	}
	forbidEnv(t, &d)
	code := run([]string{"analyze", "--json", path}, d)
	return stdout.String(), stderr.String(), code
}

func countingGemini(t *testing.T, status int, response string, hits *int, cap *geminiCapture) *http.Client {
	t.Helper()
	inner := mappingClient(t, status, response, cap)
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		*hits++
		return inner.Transport.RoundTrip(r)
	})}
}

func analysisEnvelope(t *testing.T, object string) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"candidates": []any{
			map[string]any{
				"content": map[string]any{
					"parts": []any{map[string]any{"text": object}},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

type fmStep struct {
	stdout string
	stderr string
	code   int
}

func scriptedFM(t *testing.T, calls *[][]string, steps ...fmStep) {
	t.Helper()
	n := 0
	jobad.SetFMExecForTest(func(argv []string) ([]byte, []byte, int) {
		*calls = append(*calls, append([]string(nil), argv...))
		if n >= len(steps) {
			t.Errorf("extra fm call %q", argv)
			return nil, []byte("extra fm call"), 1
		}
		step := steps[n]
		n++
		return []byte(step.stdout), []byte(step.stderr), step.code
	})
	t.Cleanup(restoreFMStub)
}

func restoreFMStub() {
	jobad.SetFMExecForTest(func([]string) ([]byte, []byte, int) {
		return nil, []byte("fm exec not stubbed"), 1
	})
}

func writeJobJSON(t *testing.T, doc jobad.Document) string {
	t.Helper()
	var buf bytes.Buffer
	if err := writeIndentedJSON(&buf, doc); err != nil {
		t.Fatal(err)
	}
	return writeFile(t, "job.json", buf.Bytes())
}

func forbidEnv(t *testing.T, d *deps) {
	t.Helper()
	d.Load = func(string, []string) (*config.Config, error) {
		t.Errorf("Load called")
		return nil, errors.New("Load called")
	}
	d.EnsureProductionEnv = func(string, []string, config.TTY) (*config.Config, error) {
		t.Errorf("EnsureProductionEnv called")
		return nil, errors.New("EnsureProductionEnv called")
	}
}

func blockGemini(t *testing.T) *http.Client {
	t.Helper()
	return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Errorf("gemini http called")
		return nil, errors.New("gemini http called")
	})}
}

func phraseJSON(t *testing.T, page []byte, source string) string {
	t.Helper()
	doc, err := jobad.Parse(page)
	if err != nil {
		t.Fatal(err)
	}
	doc.Source = source
	var buf bytes.Buffer
	if err := writeDocument(&buf, doc); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func jobsdbMinPath(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller")
	}
	path := filepath.Join(filepath.Dir(file), "..", "..", "internal", "jobad", "testdata", "jobsdb_min.html")
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeFile(t *testing.T, name string, body []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertRefreshUntouched(t *testing.T) {
	t.Helper()
	got, err := keyring.Get("docs-cli", "refresh-token")
	if err != nil || got != testRefreshItem {
		t.Fatalf("refresh-token = %q err %v", got, err)
	}
}

func assertNoSecret(t *testing.T, stdout, stderr, secret string) {
	t.Helper()
	if secret != "" && (strings.Contains(stdout, secret) || strings.Contains(stderr, secret)) {
		t.Fatalf("secret leaked stdout=%q stderr=%q", stdout, stderr)
	}
}

func TestUploadUsage(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		exact bool
	}{
		{name: "no path", args: []string{"upload"}},
		{name: "help", args: []string{"upload", "--help"}, exact: true},
		{name: "short help", args: []string{"upload", "-h"}, exact: true},
		{name: "unknown flag", args: []string{"upload", "--bogus", "a.json"}},
		{name: "title flag", args: []string{"upload", "--title", "Demo", "a.json"}},
		{name: "html flag", args: []string{"upload", "--html", "a.json"}},
		{name: "json flag", args: []string{"upload", "--json", "a.json"}},
		{name: "folder missing value", args: []string{"upload", "--folder"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls callCount
			base := calls.hooks(&config.Config{DriveFolderID: folderID}, "", nil, nil)
			base.UploadJSON = func(context.Context, string, []docs.JSONFile) ([]string, error) {
				t.Fatal("upload called")
				return nil, nil
			}
			var stdout, stderr bytes.Buffer
			base.Stdout = &stdout
			base.Stderr = &stderr
			base.Environ = []string{"APP_ENV=development"}
			base.Root = t.TempDir()
			code := run(tt.args, base)
			if code != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "usage: docs-cli") {
				t.Fatalf("exit %d stdout %q stderr %q", code, stdout.String(), stderr.String())
			}
			if tt.exact && stderr.String() != usageText {
				t.Fatalf("stderr %q", stderr.String())
			}
			if calls.load != 0 || calls.ensure != 0 || calls.require != 0 || calls.write != 0 || calls.auth != 0 || calls.create != 0 {
				t.Fatalf("calls %+v", calls)
			}
		})
	}
	if !strings.Contains(usageText, "\n  draft --json PATH --template PATH --cv PATH [--pdf NAME]\n  upload [--folder FOLDER] PATH [PATH ...]\n") {
		t.Fatalf("usage commands:\n%s", usageText)
	}
}

func TestUploadRejectsBeforeAuth(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "bad.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "big.json"), bytes.Repeat([]byte("a"), docs.MaxBodyBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "dir.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ok.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "txt", args: []string{"upload", "missing.txt"}, want: "docs-cli: not a json file: missing.txt"},
		{name: "txt after json", args: []string{"upload", "missing.json", "notes.txt"}, want: "docs-cli: not a json file: notes.txt"},
		{name: "no extension", args: []string{"upload", "readme"}, want: "docs-cli: not a json file: readme"},
		{name: "bak", args: []string{"upload", "job.json.bak"}, want: "docs-cli: not a json file: job.json.bak"},
		{name: "invalid", args: []string{"upload", "bad.json"}, want: "docs-cli: json is not valid: bad.json"},
		{name: "too big", args: []string{"upload", "big.json"}, want: "docs-cli: json exceeds 1 MiB: big.json"},
		{name: "directory", args: []string{"upload", "dir.json"}, want: "docs-cli: not a regular file: dir.json"},
		{name: "bad folder", args: []string{"upload", "--folder", "abc", "missing.json"}, want: "docs-cli: GOOGLE_DRIVE_FOLDER_ID is not a Drive folder URL or id"},
		{name: "empty folder flag", args: []string{"upload", "--folder", "", "ok.json"}, want: "docs-cli: GOOGLE_DRIVE_FOLDER_ID is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls callCount
			base := calls.hooks(&config.Config{DriveFolderID: folderID}, "", nil, nil)
			base.UploadJSON = func(context.Context, string, []docs.JSONFile) ([]string, error) {
				t.Fatal("upload called")
				return nil, nil
			}
			var stdout, stderr bytes.Buffer
			base.Stdout = &stdout
			base.Stderr = &stderr
			base.Environ = []string{"APP_ENV=production"}
			base.Root = dir
			code := run(tt.args, base)
			if code != 1 || stderr.String() != tt.want+"\n" || stdout.Len() != 0 {
				t.Fatalf("exit %d stdout %q stderr %q", code, stdout.String(), stderr.String())
			}
			if calls.load != 0 || calls.ensure != 0 || calls.require != 0 || calls.write != 0 || calls.auth != 0 || calls.create != 0 {
				t.Fatalf("calls %+v", calls)
			}
			if strings.Contains(stderr.String(), "no such file") {
				t.Fatalf("read a file: %s", stderr.String())
			}
		})
	}
}

func TestUploadWritesBothFiles(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	one := []byte("{\n  \"k\": \"SENTINEL-ONE\"\n}\n")
	two := []byte("[2]")
	if err := os.Mkdir(filepath.Join(dir, "tmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tmp", "1.json"), one, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tmp", "2.json"), two, 0o600); err != nil {
		t.Fatal(err)
	}
	keyring.MockInit()
	if err := keyring.Set("docs-cli", "refresh-token", testRefreshItem); err != nil {
		t.Fatal(err)
	}
	const geminiItem = `{"api_key":"gemini-key-value"}`
	if err := keyring.Set("docs-cli", "gemini-api-key", geminiItem); err != nil {
		t.Fatal(err)
	}
	rejectFM(t)
	var calls callCount
	var gotFolder string
	var got []docs.JSONFile
	cfg := &config.Config{
		Mode:          "development",
		FilePath:      filepath.Join(dir, ".env.example"),
		DriveFolderID: folderID,
		ClientID:      "abc.apps.googleusercontent.com",
		ClientSecret:  secretValue,
	}
	base := calls.hooks(cfg, "", nil, nil)
	base.UploadJSON = func(_ context.Context, folder string, files []docs.JSONFile) ([]string, error) {
		gotFolder = folder
		got = append([]docs.JSONFile(nil), files...)
		return []string{"id-one", "id-two"}, nil
	}
	credPath := filepath.Join(dir, "credentials.json")
	if err := os.WriteFile(credPath, []byte(sentinelCredentials), 0o640); err != nil {
		t.Fatal(err)
	}
	base.WriteCredentials = func(*config.Config) error {
		calls.write++
		return os.WriteFile(credPath, []byte("rewritten\n"), 0o600)
	}
	var stdout, stderr bytes.Buffer
	base.Stdout = &stdout
	base.Stderr = &stderr
	base.Environ = []string{"APP_ENV=development"}
	base.Root = dir
	args := []string{"upload", "./tmp/1.json", "./tmp/2.json"}
	code := run(args, base)
	if code != 0 {
		t.Fatalf("exit %d stderr %s", code, stderr.String())
	}
	wantOut := "./tmp/1.json\nid-one\n" + docs.FileURL("id-one") + "\n" +
		"./tmp/2.json\nid-two\n" + docs.FileURL("id-two") + "\n"
	if stdout.String() != wantOut {
		t.Fatalf("stdout %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "docs-cli: uploaded ./tmp/1.json bytes="+fmt.Sprint(len(one))) ||
		!strings.Contains(stderr.String(), "docs-cli: uploaded ./tmp/2.json bytes="+fmt.Sprint(len(two))) ||
		strings.Contains(stderr.String(), "docs-cli: wrote credentials.json") {
		t.Fatalf("stderr %s", stderr.String())
	}
	if strings.Contains(stdout.String(), "SENTINEL-ONE") || strings.Contains(stderr.String(), "SENTINEL-ONE") || strings.Contains(stderr.String(), geminiItem) {
		t.Fatalf("leaked stdout %q stderr %q", stdout.String(), stderr.String())
	}
	assertNoSecret(t, stdout.String(), stderr.String(), secretValue)
	assertRefreshUntouched(t)
	gotKey, err := keyring.Get("docs-cli", "gemini-api-key")
	if err != nil || gotKey != geminiItem {
		t.Fatalf("gemini key %q err %v", gotKey, err)
	}
	if gotFolder != folderID || len(got) != 2 || got[0].Name != "1.json" || got[1].Name != "2.json" ||
		!bytes.Equal(got[0].Body, one) || !bytes.Equal(got[1].Body, two) {
		t.Fatalf("folder %q files %#v", gotFolder, got)
	}
	if calls.load != 1 || calls.ensure != 0 || calls.auth != 1 || calls.create != 0 || calls.require != 1 || calls.write != 0 {
		t.Fatalf("calls %+v", calls)
	}
	gotCred, err := os.ReadFile(credPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotCred) != sentinelCredentials {
		t.Fatalf("credentials rewritten: %s", gotCred)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(credPath)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o640 {
			t.Fatalf("credentials perm=%o", info.Mode().Perm())
		}
	}
	disk, err := os.ReadFile(filepath.Join(dir, "tmp", "1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(disk, one) {
		t.Fatalf("file changed %q", disk)
	}
}

func TestUploadAcceptsOneFileAndJSONLiterals(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	cases := []struct {
		name string
		body string
	}{
		{name: "object", body: "{}"},
		{name: "array", body: "[]"},
		{name: "null", body: "null"},
		{name: "number", body: "1"},
		{name: "string", body: `"a"`},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			path := tt.name + ".json"
			if err := os.WriteFile(path, []byte(tt.body), 0o600); err != nil {
				t.Fatal(err)
			}
			var calls callCount
			cfg := &config.Config{Mode: "development", FilePath: "/tmp/.env.example", DriveFolderID: folderID}
			base := calls.hooks(cfg, "", nil, nil)
			var got docs.JSONFile
			base.UploadJSON = func(_ context.Context, folder string, files []docs.JSONFile) ([]string, error) {
				if folder != folderID || len(files) != 1 {
					t.Fatalf("folder %q files %d", folder, len(files))
				}
				got = files[0]
				return []string{"id-1"}, nil
			}
			var stdout, stderr bytes.Buffer
			base.Stdout = &stdout
			base.Stderr = &stderr
			base.Environ = []string{"APP_ENV=development"}
			base.Root = dir
			code := run([]string{"upload", path}, base)
			if code != 0 {
				t.Fatalf("exit %d stderr %s", code, stderr.String())
			}
			if got.Name != path || string(got.Body) != tt.body {
				t.Fatalf("file %#v", got)
			}
			want := path + "\nid-1\n" + docs.FileURL("id-1") + "\n"
			if stdout.String() != want {
				t.Fatalf("stdout %q", stdout.String())
			}
		})
	}
}

func TestUploadKeepsJSONExtensionCase(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	body := []byte(`{"ok":true}`)
	if err := os.WriteFile(filepath.Join(dir, "Note.JSON"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	var calls callCount
	cfg := &config.Config{Mode: "development", FilePath: "/tmp/.env.example", DriveFolderID: folderID}
	base := calls.hooks(cfg, "", nil, nil)
	var name string
	base.UploadJSON = func(_ context.Context, _ string, files []docs.JSONFile) ([]string, error) {
		name = files[0].Name
		return []string{"id-json"}, nil
	}
	var stdout, stderr bytes.Buffer
	base.Stdout = &stdout
	base.Stderr = &stderr
	base.Environ = []string{"APP_ENV=development"}
	base.Root = dir
	code := run([]string{"upload", "Note.JSON"}, base)
	if code != 0 || name != "Note.JSON" {
		t.Fatalf("exit %d name %q stderr %s", code, name, stderr.String())
	}
}

func TestUploadPartialKeepsStdout(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "1.json"), []byte(`{"n":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "2.json"), []byte(`{"n":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var calls callCount
	cfg := &config.Config{Mode: "development", FilePath: "/tmp/.env.example", DriveFolderID: folderID}
	base := calls.hooks(cfg, "", nil, nil)
	base.UploadJSON = func(context.Context, string, []docs.JSONFile) ([]string, error) {
		return []string{"id-one"}, errors.New("docs-cli: error: second failed")
	}
	var stdout, stderr bytes.Buffer
	base.Stdout = &stdout
	base.Stderr = &stderr
	base.Environ = []string{"APP_ENV=development"}
	base.Root = dir
	code := run([]string{"upload", "1.json", "2.json"}, base)
	if code != 1 {
		t.Fatalf("exit %d stderr %s", code, stderr.String())
	}
	want := "1.json\nid-one\n" + docs.FileURL("id-one") + "\n"
	if stdout.String() != want {
		t.Fatalf("stdout %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "docs-cli: uploaded 1.json bytes=7") || !strings.Contains(stderr.String(), "docs-cli: error: second failed") {
		t.Fatalf("stderr %s", stderr.String())
	}
	if strings.Contains(stderr.String(), "docs-cli: uploaded 2.json") {
		t.Fatalf("stderr %s", stderr.String())
	}
}

func TestUploadFolderFlagOverridesEnv(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "1.json"), []byte(`{"n":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	flagFolder := "ZZ_folder_99"
	original := readyEnv("https://drive.google.com/drive/folders/" + folderID)
	path := filepath.Join(dir, ".env.example")
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	var calls callCount
	base := calls.hooks(nil, "", nil, nil)
	base.Load = nil
	var got string
	base.UploadJSON = func(_ context.Context, folder string, _ []docs.JSONFile) ([]string, error) {
		got = folder
		return []string{"id-1"}, nil
	}
	base.Authorize = func(_ context.Context, _ *config.Config, opt oauth.Options) (*oauth2.Token, error) {
		calls.auth++
		if opt.Force {
			t.Fatal("Force")
		}
		return &oauth2.Token{AccessToken: "access-token-value"}, nil
	}
	var stdout, stderr bytes.Buffer
	base.Stdout = &stdout
	base.Stderr = &stderr
	base.Environ = []string{"APP_ENV=development"}
	base.Root = dir
	code := run([]string{"upload", "--folder", "  " + flagFolder + "  ", "1.json"}, base)
	if code != 0 || got != flagFolder {
		t.Fatalf("exit %d folder %q stderr %s", code, got, stderr.String())
	}
	file, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(file) != original {
		t.Fatalf("env rewritten:\n%s", file)
	}
}

func TestUploadEmptyFolderSkipsClient(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "1.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	env := strings.ReplaceAll(exampleEnv(), "you@gmail.com", "dev@example.com")
	env = strings.ReplaceAll(env, "GOOGLE_OAUTH_CLIENT_ID=\n", "GOOGLE_OAUTH_CLIENT_ID=abc.apps.googleusercontent.com\n")
	env = strings.ReplaceAll(env, "GOOGLE_OAUTH_CLIENT_SECRET=\n", "GOOGLE_OAUTH_CLIENT_SECRET="+secretValue+"\n")
	if err := os.WriteFile(filepath.Join(dir, ".env.example"), []byte(env), 0o644); err != nil {
		t.Fatal(err)
	}
	var calls callCount
	base := calls.hooks(nil, "", nil, nil)
	base.Load = nil
	base.RequireClient = func(*config.Config) error {
		calls.require++
		return nil
	}
	base.WriteCredentials = func(*config.Config) error {
		calls.write++
		return nil
	}
	base.Authorize = func(context.Context, *config.Config, oauth.Options) (*oauth2.Token, error) {
		calls.auth++
		return nil, nil
	}
	base.UploadJSON = func(context.Context, string, []docs.JSONFile) ([]string, error) {
		t.Fatal("upload called")
		return nil, nil
	}
	var stdout, stderr bytes.Buffer
	base.Stdout = &stdout
	base.Stderr = &stderr
	base.Environ = []string{"APP_ENV=development"}
	base.Root = dir
	code := run([]string{"upload", "1.json"}, base)
	if code != 1 || !strings.Contains(stderr.String(), "docs-cli: GOOGLE_DRIVE_FOLDER_ID is required") {
		t.Fatalf("exit %d stderr %s", code, stderr.String())
	}
	if stdout.Len() != 0 || calls.require != 0 || calls.write != 0 || calls.auth != 0 {
		t.Fatalf("stdout %q calls %+v", stdout.String(), calls)
	}
}

func TestUploadSameBaseName(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.MkdirAll(filepath.Join(dir, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a", "1.json"), []byte(`{"side":"a"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b", "1.json"), []byte(`{"side":"b"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var calls callCount
	cfg := &config.Config{Mode: "development", FilePath: "/tmp/.env.example", DriveFolderID: folderID}
	base := calls.hooks(cfg, "", nil, nil)
	var names []string
	base.UploadJSON = func(_ context.Context, _ string, files []docs.JSONFile) ([]string, error) {
		for _, file := range files {
			names = append(names, file.Name)
		}
		return []string{"id-a", "id-b"}, nil
	}
	var stdout, stderr bytes.Buffer
	base.Stdout = &stdout
	base.Stderr = &stderr
	base.Environ = []string{"APP_ENV=development"}
	base.Root = dir
	code := run([]string{"upload", "a/1.json", "b/1.json"}, base)
	if code != 0 {
		t.Fatalf("exit %d stderr %s", code, stderr.String())
	}
	if len(names) != 2 || names[0] != "1.json" || names[1] != "1.json" {
		t.Fatalf("names %#v", names)
	}
	if !strings.Contains(stdout.String(), "a/1.json\nid-a\n") || !strings.Contains(stdout.String(), "b/1.json\nid-b\n") {
		t.Fatalf("stdout %q", stdout.String())
	}
}

type fakeTTY struct {
	terminal bool
	secrets  []string
	prompts  []string
}

func (f *fakeTTY) IsTerminal() bool { return f.terminal }

func (f *fakeTTY) ReadLine(string) (string, error) {
	return "", errors.New("ReadLine")
}

func (f *fakeTTY) ReadSecret(prompt string) (string, error) {
	f.prompts = append(f.prompts, prompt)
	if len(f.secrets) == 0 {
		return "", errors.New("no secret")
	}
	secret := f.secrets[0]
	f.secrets = f.secrets[1:]
	return secret, nil
}

type geminiCapture struct {
	url    string
	header http.Header
	body   string
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func mappingClient(t *testing.T, status int, response string, cap *geminiCapture) *http.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, response)
	}))
	t.Cleanup(srv.Close)
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		cap.url = r.URL.String()
		cap.header = r.Header.Clone()
		cap.body = string(body)
		out, err := http.NewRequestWithContext(r.Context(), r.Method, srv.URL, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		out.Header = r.Header.Clone()
		return http.DefaultTransport.RoundTrip(out)
	})}
}

func geminiReply(t *testing.T, benefits, duties, requirements, trailer []string) string {
	t.Helper()
	inner, err := json.Marshal(map[string][]string{
		"benefits":     emptyStrings(benefits),
		"duties":       emptyStrings(duties),
		"requirements": emptyStrings(requirements),
		"trailer":      emptyStrings(trailer),
	})
	if err != nil {
		t.Fatal(err)
	}
	outer := map[string]any{
		"candidates": []any{
			map[string]any{
				"content": map[string]any{
					"parts": []any{map[string]any{"text": string(inner)}},
				},
			},
		},
	}
	raw, err := json.Marshal(outer)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func emptyStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}
