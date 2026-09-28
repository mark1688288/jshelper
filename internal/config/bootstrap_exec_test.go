package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectBootstrapCommand(t *testing.T) {
	root := t.TempDir()
	parents := [][]string{
		{"PATH=/usr/bin"},
		{"PATH=/usr/bin", "APP_ENV="},
		{"PATH=/usr/bin", "APP_ENV=development"},
	}
	for _, parent := range parents {
		cmd := projectBootstrapCmd(root, parent)
		if len(cmd.Args) != 2 || cmd.Args[0] != "bash" || cmd.Args[1] != "config/bootstrap.sh" {
			t.Fatalf("args=%q", cmd.Args)
		}
		joined := strings.Join(cmd.Args, " ")
		if strings.Contains(joined, "--check") || strings.Contains(joined, "--open") {
			t.Fatalf("args=%q", cmd.Args)
		}
		if cmd.Dir != root {
			t.Fatalf("dir=%s", cmd.Dir)
		}
		if !containsEntry(cmd.Env, "PATH=/usr/bin") || countPrefix(cmd.Env, "APP_ENV=") != 1 || !containsEntry(cmd.Env, "APP_ENV=production") {
			t.Fatalf("parent %q env %q", parent, cmd.Env)
		}
	}
}

func TestExecProjectBootstrapStreamsAndSetsAppEnv(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nif [ \"${APP_ENV}\" != \"production\" ]; then\n  exit 2\nfi\nprintf '%s\\n' stdout-marker\nprintf '%s\\n' stderr-marker >&2\n"
	if err := os.WriteFile(filepath.Join(root, "config", "bootstrap.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := execProjectBootstrap(root, []string{"PATH=/usr/bin", "APP_ENV=development"}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "stdout-marker") || !strings.Contains(out.String(), "stderr-marker") {
		t.Fatalf("output %q", out.String())
	}
}

func containsEntry(env []string, want string) bool {
	for _, entry := range env {
		if entry == want {
			return true
		}
	}
	return false
}

func countPrefix(env []string, prefix string) int {
	n := 0
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			n++
		}
	}
	return n
}
