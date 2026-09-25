package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
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
		{name: "auth", args: []string{"auth"}, code: 1, stderr: "not implemented"},
		{name: "setup", args: []string{"setup"}, code: 1, stderr: "not implemented"},
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
		if code != 1 || !strings.Contains(stderr.String(), "not implemented") {
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
