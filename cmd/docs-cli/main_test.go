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
	if !strings.Contains(text, "建立 Desktop app「Docs CLI Desktop」後再執行 ./docs-cli setup") || !strings.Contains(text, "docs-cli: copied .env.example to .env") {
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
	if strings.Contains(text, "建立 Desktop app") || strings.Contains(text, secretValue) || strings.Contains(second.buf.String(), secretValue) {
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
	base.Authorize = func(_ context.Context, _ *config.Config, opt oauth.Options) (*oauth2.Token, error) {
		calls.auth++
		if opt.Force {
			t.Fatal("Force")
		}
		return &oauth2.Token{AccessToken: "access-token-value"}, nil
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
	if calls.load != 0 || calls.ensure != 0 || calls.create != 1 || calls.auth != 1 {
		t.Fatalf("calls %+v", calls)
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
	if gotFolder != folderID || gotTitle != "Untitled" || gotBody != "hello" {
		t.Fatalf("title %q body %q folder %q", gotTitle, gotBody, gotFolder)
	}
	if calls.ensure != 0 || calls.create != 1 {
		t.Fatalf("calls %+v", calls)
	}
	if _, err := os.Stat(filepath.Join(dir, ".env")); !os.IsNotExist(err) {
		t.Fatalf(".env: %v", err)
	}
	credPath := filepath.Join(dir, "credentials.json")
	if runtime.GOOS != "windows" {
		info, err := os.Stat(credPath)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("perm %o", info.Mode().Perm())
		}
	}
	cred, err := os.ReadFile(credPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(cred, []byte("refresh")) || bytes.Contains(cred, []byte(raw)) {
		t.Fatalf("credentials %s", cred)
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
	if !strings.Contains(usageText, "\n  parse --html PATH\n  gemini-key\n") {
		t.Fatalf("usage commands:\n%s", usageText)
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
		if strings.Contains(cap.body, "jobAdDetails") || strings.Contains(cap.body, "Analyst Example") {
			t.Fatalf("body sent header or html: %s", cap.body)
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
		if !strings.Contains(cap.url, "generativelanguage.googleapis.com") || !strings.Contains(cap.url, "gemini-3.1-flash-lite") || strings.Contains(cap.url, "aiplatform.googleapis.com") {
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
		if strings.Contains(cap.body, "CHROME-NOTICE") || strings.Contains(cap.body, apiKey) || strings.Contains(cap.body, "initialHistory") || strings.Contains(cap.body, "<p>") || strings.Contains(cap.url, apiKey) {
			t.Fatalf("request url %s body %s", cap.url, cap.body)
		}
		if !strings.Contains(cap.body, "Buy supplies") || !strings.Contains(cap.body, "Know stores") || !strings.Contains(cap.body, "Excel helps") {
			t.Fatalf("body %s", cap.body)
		}
		assertNoSecret(t, stdout.String(), stderr.String(), apiKey)
	})
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
	if !strings.Contains(usageText, "\n  gemini-key\n  analyze --json PATH\n") {
		t.Fatalf("usage commands:\n%s", usageText)
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
		path := writeJobJSON(t, doc)
		var calls [][]string
		scriptedFM(t, &calls,
			fmStep{stdout: "not-a-letter", stderr: "available noise"},
			fmStep{stdout: "{\"letter\":\"hi\\n\\n\"}\n", stderr: "respond noise"},
		)
		stdout, stderr, code := runDraftArgs(t, root, path, template)
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
		path := writeJobJSON(t, jobad.Document{Requirements: []string{"Know Go"}})
		var calls [][]string
		scriptedFM(t, &calls, fmStep{}, fmStep{stdout: `{"letter":"Hi there"}`})
		stdout, stderr, code := runDraftArgs(t, root, path, template)
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
		path := writeJobJSON(t, jobad.Document{Requirements: []string{"Know Go"}})
		var calls [][]string
		scriptedFM(t, &calls, fmStep{}, fmStep{stdout: "{\"letter\":\"hi\\n\\n\"}\n"})
		stdout, stderr, code := runDraftArgs(t, root, path, template)
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
	var calls [][]string
	scriptedFM(t, &calls, fmStep{}, fmStep{stdout: `{"letter":"Hi there"}`})
	stdout, stderr, code := runDraftArgs(t, "", path, template)
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
		path := writeJobJSON(t, doc)
		stdout, stderr, code := runDraftArgs(t, root, path, template)
		if code != 1 || stdout != "" || stderr != "docs-cli: requirements is empty\n" {
			t.Fatalf("doc %#v exit %d stdout %q stderr %q", doc.Requirements, code, stdout, stderr)
		}
		if strings.Contains(stderr, "template-not-opened.pdf") {
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
	stdout, stderr, code := runDraftArgs(t, root, jsonPath, template)
	if code != 1 || stdout != "" || !strings.Contains(stderr, "docs-cli: error:") || !strings.Contains(stderr, jsonPath) {
		t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	if strings.Contains(stderr, template) || strings.Contains(stderr, "template has no text") {
		t.Fatalf("stderr %q", stderr)
	}
	assertDraftsEmpty(t, root)
}

func TestDraftInvalidJSONBeforeTemplate(t *testing.T) {
	rejectFM(t)
	root := t.TempDir()
	jsonPath := writeFile(t, "bad.json", []byte("{"))
	template := filepath.Join(t.TempDir(), "template-not-opened.pdf")
	stdout, stderr, code := runDraftArgs(t, root, jsonPath, template)
	if code != 1 || stdout != "" || !strings.Contains(stderr, "docs-cli: error:") || !strings.Contains(stderr, jsonPath) {
		t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	if strings.Contains(stderr, template) {
		t.Fatalf("stderr %q", stderr)
	}
	assertDraftsEmpty(t, root)
}

func TestDraftTemplateTooLarge(t *testing.T) {
	rejectFM(t)
	root := t.TempDir()
	path := writeJobJSON(t, jobad.Document{Requirements: []string{"Know Go"}})
	template := writeFile(t, "over.bin", bytes.Repeat([]byte{'x'}, maxJSONBytes+1))
	stdout, stderr, code := runDraftArgs(t, root, path, template)
	if code != 1 || stdout != "" || stderr != "docs-cli: template exceeds 1 MiB\n" {
		t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	assertDraftsEmpty(t, root)
}

func TestDraftTemplateNoText(t *testing.T) {
	rejectFM(t)
	root := t.TempDir()
	path := writeJobJSON(t, jobad.Document{Requirements: []string{"Know Go"}})
	template := writeFile(t, "note.txt", []byte("not a pdf"))
	stdout, stderr, code := runDraftArgs(t, root, path, template)
	if code != 1 || stdout != "" || stderr != "docs-cli: template has no text\n" {
		t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	assertDraftsEmpty(t, root)
}

func TestDraftTemplateNotRegular(t *testing.T) {
	rejectFM(t)
	root := t.TempDir()
	path := writeJobJSON(t, jobad.Document{Requirements: []string{"Know Go"}})
	template := t.TempDir()
	stdout, stderr, code := runDraftArgs(t, root, path, template)
	if code != 1 || stdout != "" || stderr != "docs-cli: template has no text\n" {
		t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	assertDraftsEmpty(t, root)
}

func TestDraftTemplateMissing(t *testing.T) {
	rejectFM(t)
	root := t.TempDir()
	path := writeJobJSON(t, jobad.Document{Requirements: []string{"Know Go"}})
	template := filepath.Join(t.TempDir(), "missing.pdf")
	stdout, stderr, code := runDraftArgs(t, root, path, template)
	if code != 1 || stdout != "" || !strings.Contains(stderr, "docs-cli: error:") || !strings.Contains(stderr, template) {
		t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	if strings.Contains(stderr, "template has no text") {
		t.Fatalf("stderr %q", stderr)
	}
	assertDraftsEmpty(t, root)
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
	stdout, stderr, code := runDraftArgs(t, root, path, coverMinPath(t))
	if code != 1 || stdout != "" || stderr != "docs-cli: fm text exceeds the argument limit\n" {
		t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	assertDraftsEmpty(t, root)
}

func TestDraftFMAvailableFails(t *testing.T) {
	path := writeJobJSON(t, jobad.Document{Requirements: []string{"Know Go"}})
	template := writeDraftEmailPDF(t)

	t.Run("available", func(t *testing.T) {
		root := t.TempDir()
		var calls [][]string
		scriptedFM(t, &calls, fmStep{stdout: `{"letter":"hi"}`, stderr: "not licensed\n", code: 1})
		stdout, stderr, code := runDraftArgs(t, root, path, template)
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
		stdout, stderr, code := runDraftArgs(t, root, path, template)
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
	var calls [][]string
	scriptedFM(t, &calls, fmStep{}, fmStep{stdout: "The letter is ready.\n"})
	stdout, stderr, code := runDraftArgs(t, root, path, template)
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
	if !strings.Contains(usageText, "\n  analyze --json PATH\n  draft --json PATH --template PATH\n") {
		t.Fatalf("usage commands:\n%s", usageText)
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
	stdout, stderr, code := runDraftArgs(t, root, jsonPath, template)
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
	stdout, stderr, code := runDraftArgs(t, root, path, template)
	if code != 1 || stdout != "" || stderr != errJSONTooLarge+"\n" {
		t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	if strings.Contains(stderr, template) {
		t.Fatalf("stderr %q", stderr)
	}
	assertDraftsEmpty(t, root)
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
		{name: "empty json", args: []string{"draft", "--json", "", "--template", "letter.pdf"}},
		{name: "empty template", args: []string{"draft", "--json", "job.json", "--template", ""}},
		{name: "json only", args: []string{"draft", "--json", "job.json"}},
		{name: "template only", args: []string{"draft", "--template", "letter.pdf"}},
		{name: "unknown flag", args: []string{"draft", "--bogus"}},
		{name: "out flag", args: []string{"draft", "--out", "letter.txt"}},
		{name: "extra arg", args: []string{"draft", "--json", "job.json", "--template", "letter.pdf", "extra"}},
		{name: "help", args: []string{"draft", "--help"}, exact: true},
		{name: "short help", args: []string{"draft", "-h"}, exact: true},
	}
}

func runDraftArgs(t *testing.T, root, jsonPath, templatePath string) (string, string, int) {
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
	code := run([]string{"draft", "--json", jsonPath, "--template", templatePath}, d)
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
