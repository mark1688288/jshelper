package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const releaseLdflags = "-X github.com/mark1688288/jshelper/internal/config.defaultAppEnv=production"

func TestReleaseBinaryDefaultsToProduction(t *testing.T) {
	root := moduleRoot(t)
	bin := filepath.Join(t.TempDir(), "docs-cli")
	build := exec.Command("go", "build", "-o", bin, "-ldflags", releaseLdflags, "./cmd/docs-cli")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	work := t.TempDir()
	stdout, stderr, err := execSetup(t, bin, work, environWithout("APP_ENV"))
	if err == nil {
		t.Fatal("setup with an unset APP_ENV exited 0")
	}
	if stdout != "" {
		t.Fatalf("stdout = %q", stdout)
	}
	if !strings.Contains(stderr, "missing .env.example") {
		t.Fatalf("stderr = %q, want missing .env.example", stderr)
	}
	if strings.Contains(stderr, "does not copy or prompt") {
		t.Fatalf("production stderr refused the wizard: %q", stderr)
	}
	assertNoEnvFile(t, work)

	stdout, stderr, err = execSetup(t, bin, work, append(environWithout("APP_ENV"), "APP_ENV=development"))
	if err == nil {
		t.Fatal("setup with APP_ENV=development exited 0")
	}
	if stdout != "" {
		t.Fatalf("stdout = %q", stdout)
	}
	if !strings.Contains(stderr, "does not copy or prompt") {
		t.Fatalf("stderr = %q, want development refusal", stderr)
	}
	assertNoEnvFile(t, work)
}

func execSetup(t *testing.T, bin, dir string, env []string) (string, string, error) {
	t.Helper()
	cmd := exec.Command(bin, "setup")
	cmd.Dir = dir
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func assertNoEnvFile(t *testing.T, dir string) {
	t.Helper()
	_, err := os.Lstat(filepath.Join(dir, ".env"))
	if !os.IsNotExist(err) {
		t.Fatalf(".env in %s: %v", dir, err)
	}
}

func environWithout(key string) []string {
	prefix := key + "="
	src := os.Environ()
	out := make([]string, 0, len(src))
	for _, entry := range src {
		if strings.HasPrefix(entry, prefix) {
			continue
		}
		out = append(out, entry)
	}
	return out
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("module root %s: %v", root, err)
	}
	return root
}
