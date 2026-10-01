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
	"runtime"
	"strings"
	"testing"

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
	code := m.Run()
	keyring.MockRestore()
	os.Exit(code)
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
		if got.Source != path || strings.Join(got.Duties, "|") != "Medical insurance|Build tools" || len(got.Offer) != 0 {
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
		if len(got.Offer) != 0 || strings.Join(got.Duties, "|") != "Medical insurance|Build tools" {
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
		if code != 0 || stdout.String() != phrase || stderr.String() != errGeminiRejected+"\n" {
			t.Fatalf("exit %d stdout %s stderr %q", code, stdout.String(), stderr.String())
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

func geminiReply(t *testing.T, offer, duties, requirements, trailer []string) string {
	t.Helper()
	inner, err := json.Marshal(map[string][]string{
		"offer":        emptyStrings(offer),
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
