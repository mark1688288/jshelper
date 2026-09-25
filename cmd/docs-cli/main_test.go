package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mark1688288/jshelper/internal/config"
	"github.com/mark1688288/jshelper/internal/oauth"
)

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
		{name: "create", args: []string{"create"}, code: 1, stderr: "not implemented"},
		{name: "auth", args: []string{"auth"}, code: 1, stderr: "missing .env.example"},
		{name: "setup", args: []string{"setup"}, code: 1, stderr: "docs-cli: setup refused: development reads .env.example and does not copy or prompt"},
		{
			name:   "create with flags",
			args:   []string{"create", "--title", "Demo", "--text", "hello", "--folder", "abc"},
			code:   1,
			stderr: "not implemented",
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
		want := "not implemented"
		switch args[0] {
		case "setup":
			want = "setup refused"
		case "auth":
			want = "missing .env.example"
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

func TestCreateAndAuthDoNotCopyInProduction(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, ".env.example"), []byte(exampleEnv()), 0o644); err != nil {
		t.Fatal(err)
	}
	commands := [][]string{
		{"create"},
		{"create", "--title", "Demo", "--text", "hello", "--folder", "abc"},
	}
	for _, args := range commands {
		var stdout, stderr bytes.Buffer
		code := run(args, deps{
			Stdin:   strings.NewReader(""),
			Stdout:  &stdout,
			Stderr:  &stderr,
			Environ: []string{"APP_ENV=production"},
			Root:    dir,
		})
		if code != 1 || !strings.Contains(stderr.String(), "not implemented") {
			t.Fatalf("%v: exit %d stderr %q", args, code, stderr.String())
		}
		if strings.Contains(stderr.String(), "listening") {
			t.Fatalf("%v: listened: %s", args, stderr.String())
		}
		if stdout.Len() != 0 {
			t.Fatalf("%v: stdout %q", args, stdout.String())
		}
		if _, err := os.Stat(filepath.Join(dir, ".env")); !os.IsNotExist(err) {
			t.Fatalf("%v: .env created: %v", args, err)
		}
		if _, err := os.Stat(filepath.Join(dir, "credentials.json")); !os.IsNotExist(err) {
			t.Fatalf("%v: credentials created: %v", args, err)
		}
	}
}

func TestSetupProduction(t *testing.T) {
	t.Setenv("APP_ENV", "staging")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env.example"), []byte(exampleEnv()), 0o644); err != nil {
		t.Fatal(err)
	}
	tty := &scriptTTY{
		lines: []string{
			"",
			"",
			"dev@example.com",
			"dev@example.com",
			"abc.apps.googleusercontent.com",
			"https://drive.google.com/drive/folders/" + folderID + "?usp=sharing",
		},
		secrets: []string{secretValue},
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
	if !strings.Contains(text, "docs-cli: mode=production file=.env\n") {
		t.Fatalf("stderr %s", text)
	}
	if strings.Contains(text, "docs-cli: docs-cli:") {
		t.Fatalf("doubled prefix:\n%s", text)
	}
	if !strings.Contains(text, "docs-cli: copied .env.example to .env") || !strings.Contains(text, "docs-cli: wrote credentials.json") {
		t.Fatalf("stderr %s", text)
	}
	if strings.Contains(text, secretValue) || strings.Contains(tty.buf.String(), secretValue) {
		t.Fatal("secret leaked")
	}
	envText, err := os.ReadFile(filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(envText, []byte("GOOGLE_DRIVE_FOLDER_ID="+folderID)) || bytes.Contains(envText, []byte("usp=sharing")) {
		t.Fatalf("env:\n%s", envText)
	}
	credPath := filepath.Join(dir, "credentials.json")
	if runtime.GOOS != "windows" {
		info, err := os.Stat(credPath)
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
		Authorize: func(context.Context, *config.Config, oauth.Options) error {
			t.Fatal("Authorize was called")
			return nil
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
		Authorize: func(_ context.Context, cfg *config.Config, opt oauth.Options) error {
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
			return nil
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
