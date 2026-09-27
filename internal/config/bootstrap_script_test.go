package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

const (
	scriptProjectID    = "docs-cli-dev"
	scriptProjectName  = "Docs CLI"
	scriptClientID     = "abc.apps.googleusercontent.com"
	scriptClientSecret = "test-secret"
	scriptProdProject  = "docs-cli-prod"
)

func TestBootstrapScriptCheckSkipsGcloud(t *testing.T) {
	f := newBootstrapFixture(t)
	script := f.read(t, filepath.Join("config", "bootstrap.sh"))
	if strings.Contains(script, "GOOGLE_TOKEN_PATH") || strings.Contains(script, "GOOGLE_TOKEN_FILE_MODE") {
		t.Fatal("bootstrap.sh names a token file variable")
	}
	if !strings.Contains(script, `DATA_ACCESS_URL="https://console.cloud.google.com/auth/scopes?project=${GCP_PROJECT_ID}"`) {
		t.Fatal("DATA_ACCESS_URL missing")
	}

	stdout, stderr, code := f.run(t, []string{"--check"}, []string{"APP_ENV=development"})
	if code != 0 || stderr != "" {
		scriptFatal(t, stdout, stderr, fmt.Sprintf("check exit %d", code))
	}
	assertInOrder(t, stdout,
		"file=.env.example",
		"oauth_client=missing",
		"next: branding  "+consoleURL("branding", scriptProjectID),
		"next: audience  "+consoleURL("audience", scriptProjectID),
		"next: data-access  "+consoleURL("scopes", scriptProjectID),
		"next: clients   "+consoleURL("clients", scriptProjectID),
		"insert_index=1 scope=https://www.googleapis.com/auth/drive",
		"apis=docs.googleapis.com,drive.googleapis.com",
	)
	if strings.Contains(stdout, "config/.env.example") {
		scriptFatal(t, stdout, stderr, "check printed config/.env.example")
	}
	f.assertGcloudNotRun(t)
	assertNoBootstrapMarkers(t, f.path(".env.example"))

	marked := f.read(t, ".env.example")
	if !strings.HasSuffix(marked, "\n") {
		marked += "\n"
	}
	f.write(t, ".env.example", marked+"DOCS_CLI_PROJECT_READY="+scriptProjectID+"\n", 0o644)
	stdout, stderr, code = f.run(t, []string{"--check"}, []string{"APP_ENV=development"})
	if code != 0 || stderr != "" || !strings.Contains(stdout, "oauth_client=missing") || !strings.Contains(stdout, "next: data-access  ") {
		scriptFatal(t, stdout, stderr, fmt.Sprintf("check with marker exit %d", code))
	}
	f.assertGcloudNotRun(t)

	present := replaceKey(f.read(t, ".env.example"), "GOOGLE_OAUTH_CLIENT_ID", scriptClientID)
	present = replaceKey(present, "GOOGLE_OAUTH_CLIENT_SECRET", scriptClientSecret)
	f.write(t, ".env.example", present, 0o644)
	stdout, stderr, code = f.run(t, []string{"--check"}, []string{"APP_ENV=development"})
	if code != 0 || stderr != "" || !strings.Contains(stdout, "oauth_client=present") || strings.Contains(stdout, "data-access") {
		scriptFatal(t, stdout, stderr, fmt.Sprintf("present client check exit %d", code))
	}
	f.assertGcloudNotRun(t)

	bad := replaceKey(present, "GOOGLE_OAUTH_CLIENT_ID", "not-a-client")
	f.write(t, ".env.example", bad, 0o644)
	stdout, stderr, code = f.run(t, []string{"--check"}, []string{"APP_ENV=development"})
	if code == 0 || !strings.Contains(stderr, "GOOGLE_OAUTH_CLIENT_ID does not look like a Google OAuth client id") || strings.Contains(stdout, "next:") {
		scriptFatal(t, stdout, stderr, fmt.Sprintf("bad client id exit %d", code))
	}
	f.assertGcloudNotRun(t)
}

func TestBootstrapScriptReadsDotEnvOnlyWhenAppEnvProduction(t *testing.T) {
	f := newBootstrapFixture(t)
	prod := replaceKey(f.read(t, ".env.example"), "GCP_PROJECT_ID", scriptProdProject)
	f.write(t, ".env", prod, 0o600)

	stdout, stderr, code := f.run(t, []string{"--check"}, nil)
	if code != 0 || stderr != "" {
		scriptFatal(t, stdout, stderr, fmt.Sprintf("unset APP_ENV exit %d", code))
	}
	if !strings.Contains(stdout, "file=.env.example project="+scriptProjectID) || strings.Contains(stdout, scriptProdProject) {
		scriptFatal(t, stdout, stderr, "unset APP_ENV read the wrong file")
	}
	f.assertGcloudNotRun(t)

	stdout, stderr, code = f.run(t, []string{"--check"}, []string{"APP_ENV=production"})
	if code != 0 || stderr != "" {
		scriptFatal(t, stdout, stderr, fmt.Sprintf("production check exit %d", code))
	}
	if !strings.Contains(stdout, "file=.env project="+scriptProdProject) || strings.Contains(stdout, scriptProjectID) {
		scriptFatal(t, stdout, stderr, "production check read the wrong file")
	}
	f.assertGcloudNotRun(t)
}

func TestBootstrapScriptProjectCreateFakeGcloud(t *testing.T) {
	t.Run("describe missing", func(t *testing.T) {
		f := newBootstrapFixture(t)
		before := f.read(t, ".env.example")
		f.write(t, ".env", before, 0o600)
		stdout, stderr, code := f.run(t, nil, []string{"APP_ENV=production", "GCLOUD_DESCRIBE_STATUS=1"})
		if code != 0 || stderr != "" {
			scriptFatal(t, stdout, stderr, fmt.Sprintf("create exit %d", code))
		}
		assertInOrder(t, stdout,
			consoleURL("branding", scriptProjectID),
			consoleURL("audience", scriptProjectID),
			"add only https://www.googleapis.com/auth/drive",
			consoleURL("scopes", scriptProjectID),
			consoleURL("clients", scriptProjectID),
			"run ./docs-cli setup again",
			"The browser consent flow does not create the client id.",
		)
		if strings.Contains(stdout, "rerun this script") {
			scriptFatal(t, stdout, stderr, "production step 5 still says rerun this script")
		}
		log := f.mustGcloudLog(t)
		if !strings.Contains(log, "projects create "+scriptProjectID) || !strings.Contains(log, "--name="+scriptProjectName) {
			t.Fatalf("create log missing project create:\n%s", log)
		}
		if !strings.Contains(log, "services enable docs.googleapis.com") || !strings.Contains(log, "services enable drive.googleapis.com") {
			t.Fatalf("create log missing API enable:\n%s", log)
		}
		if strings.Contains(log, "folders") || strings.Contains(log, "--folder=") {
			t.Fatalf("log created a folder:\n%s", log)
		}
		if f.read(t, ".env") != before {
			t.Fatal(".env changed")
		}
		assertNoBootstrapMarkers(t, f.path(".env"))
		assertNoCredentials(t, f.root)
	})

	t.Run("describe exists", func(t *testing.T) {
		f := newBootstrapFixture(t)
		f.write(t, ".env", f.read(t, ".env.example"), 0o600)
		stdout, stderr, code := f.run(t, nil, []string{"APP_ENV=production", "GCLOUD_DESCRIBE_STATUS=0"})
		if code != 0 || stderr != "" || !strings.Contains(stdout, "run ./docs-cli setup again") {
			scriptFatal(t, stdout, stderr, fmt.Sprintf("existing project exit %d", code))
		}
		log := f.mustGcloudLog(t)
		if strings.Contains(log, "projects create") {
			t.Fatalf("created a project that already exists:\n%s", log)
		}
		if !strings.Contains(log, "services enable docs.googleapis.com") || !strings.Contains(log, "services enable drive.googleapis.com") {
			t.Fatalf("existing project log missing API enable:\n%s", log)
		}
		assertNoCredentials(t, f.root)
	})

	t.Run("development reruns script", func(t *testing.T) {
		f := newBootstrapFixture(t)
		stdout, stderr, code := f.run(t, nil, []string{"APP_ENV=development", "GCLOUD_DESCRIBE_STATUS=0"})
		if code != 0 || stderr != "" {
			scriptFatal(t, stdout, stderr, fmt.Sprintf("development exit %d", code))
		}
		if !strings.Contains(stdout, "rerun this script") || strings.Contains(stdout, "run ./docs-cli setup again") {
			scriptFatal(t, stdout, stderr, "development step 5")
		}
		if !strings.Contains(stdout, "The browser consent flow does not create the client id.") {
			scriptFatal(t, stdout, stderr, "missing consent sentence")
		}
		assertNoBootstrapMarkers(t, f.path(".env.example"))
	})
}

func TestBootstrapScriptEnableFailure(t *testing.T) {
	f := newBootstrapFixture(t)
	before := f.read(t, ".env.example")
	f.write(t, ".env", before, 0o600)
	stdout, stderr, code := f.run(t, nil, []string{
		"APP_ENV=production",
		"GCLOUD_DESCRIBE_STATUS=0",
		"GCLOUD_ENABLE_DOCS_STATUS=1",
	})
	if code == 0 {
		scriptFatal(t, stdout, stderr, "enable failure exited 0")
	}
	docsLibrary := "https://console.cloud.google.com/apis/library/docs.googleapis.com?project=" + scriptProjectID
	driveLibrary := "https://console.cloud.google.com/apis/library/drive.googleapis.com?project=" + scriptProjectID
	if !strings.Contains(stderr, docsLibrary) || !strings.Contains(stderr, driveLibrary) {
		scriptFatal(t, stdout, stderr, "enable failure stderr missing library URLs")
	}
	if f.read(t, ".env") != before {
		t.Fatal("script rewrote .env after enable failure")
	}
	assertNoBootstrapMarkers(t, f.path(".env"))
	log := f.mustGcloudLog(t)
	if !strings.Contains(log, "services enable docs.googleapis.com") || strings.Contains(log, "services enable drive.googleapis.com") {
		t.Fatalf("enable failure log:\n%s", log)
	}
}

func TestBootstrapScriptProductionSkipsPython(t *testing.T) {
	f := newBootstrapFixture(t)
	body := replaceKey(f.read(t, ".env.example"), "GOOGLE_OAUTH_CLIENT_ID", scriptClientID)
	body = replaceKey(body, "GOOGLE_OAUTH_CLIENT_SECRET", scriptClientSecret)
	f.write(t, ".env.example", body, 0o644)
	f.write(t, ".env", body, 0o600)

	stdout, stderr, code := f.run(t, nil, []string{"APP_ENV=production", "GCLOUD_DESCRIBE_STATUS=0"})
	if code != 0 {
		scriptFatal(t, stdout, stderr, fmt.Sprintf("production exit %d", code))
	}
	if strings.Contains(stderr, "python3 is required") {
		scriptFatal(t, stdout, stderr, "production required python3")
	}
	if !strings.Contains(stdout, "project ready. production credentials.json is written by docs-cli, not this script.") {
		scriptFatal(t, stdout, stderr, "missing production skip message")
	}
	log := f.mustGcloudLog(t)
	if !strings.Contains(log, "projects describe "+scriptProjectID) ||
		!strings.Contains(log, "services enable docs.googleapis.com") ||
		!strings.Contains(log, "services enable drive.googleapis.com") {
		t.Fatalf("production skip did not enable APIs:\n%s", log)
	}
	assertNoCredentials(t, f.root)
	assertNoBootstrapMarkers(t, f.path(".env"))
	if f.read(t, ".env") != body {
		t.Fatal("production run rewrote .env")
	}

	stdout, stderr, code = f.run(t, nil, []string{"APP_ENV=development", "GCLOUD_DESCRIBE_STATUS=0"})
	if code == 0 || !strings.Contains(stderr, "python3 is required to write credentials.json") {
		scriptFatal(t, stdout, stderr, fmt.Sprintf("development exit %d", code))
	}
	assertNoCredentials(t, f.root)
	assertNoBootstrapMarkers(t, f.path(".env.example"))
}

type scriptFixture struct {
	root    string
	binDir  string
	logPath string
}

func newBootstrapFixture(t *testing.T) scriptFixture {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	repo := repoRoot(t)
	script, err := os.ReadFile(filepath.Join(repo, "config", "bootstrap.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config", "bootstrap.sh"), script, 0o755); err != nil {
		t.Fatal(err)
	}
	example, err := os.ReadFile(filepath.Join(repo, ".env.example"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env.example"), example, 0o644); err != nil {
		t.Fatal(err)
	}

	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "gcloud.log")
	// PATH is only this directory, so the real gcloud and python3 are absent.
	fake := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> %s
if [ "$1" = "projects" ] && [ "$2" = "describe" ]; then
  exit "${GCLOUD_DESCRIBE_STATUS:-0}"
fi
if [ "$1" = "services" ] && [ "$2" = "enable" ] && [ "$3" = "docs.googleapis.com" ]; then
  exit "${GCLOUD_ENABLE_DOCS_STATUS:-0}"
fi
exit 0
`, strconv.Quote(logPath))
	if err := os.WriteFile(filepath.Join(binDir, "gcloud"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"/usr/bin/dirname", "/usr/bin/basename", "/bin/mkdir"} {
		if err := os.Symlink(tool, filepath.Join(binDir, filepath.Base(tool))); err != nil {
			t.Fatal(err)
		}
	}
	return scriptFixture{root: root, binDir: binDir, logPath: logPath}
}

func (f scriptFixture) path(name string) string {
	return filepath.Join(f.root, name)
}

func (f scriptFixture) read(t *testing.T, name string) string {
	t.Helper()
	body, err := os.ReadFile(f.path(name))
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func (f scriptFixture) write(t *testing.T, name, content string, mode os.FileMode) {
	t.Helper()
	writeMode(t, f.path(name), content, mode)
}

func (f scriptFixture) run(t *testing.T, args, env []string) (string, string, int) {
	t.Helper()
	cmdArgs := append([]string{f.path(filepath.Join("config", "bootstrap.sh"))}, args...)
	cmd := exec.Command("/bin/bash", cmdArgs...)
	cmd.Dir = f.root
	cmd.Env = append([]string{"PATH=" + f.binDir}, env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return stdout.String(), stderr.String(), 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return stdout.String(), stderr.String(), exitErr.ExitCode()
	}
	t.Fatalf("bootstrap: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	return "", "", -1
}

func (f scriptFixture) assertGcloudNotRun(t *testing.T) {
	t.Helper()
	body, err := os.ReadFile(f.logPath)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(bytes.TrimSpace(body)) != 0 {
		t.Fatalf("gcloud was called:\n%s", body)
	}
}

func (f scriptFixture) mustGcloudLog(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(f.logPath)
	if err != nil {
		t.Fatalf("gcloud log: %v", err)
	}
	return string(body)
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(file)
	for {
		body, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err == nil && bytes.Contains(body, []byte("module github.com/mark1688288/jshelper")) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("repo root not found")
		}
		dir = parent
	}
}

func consoleURL(page, project string) string {
	return "https://console.cloud.google.com/auth/" + page + "?project=" + project
}

func assertInOrder(t *testing.T, text string, parts ...string) {
	t.Helper()
	last := -1
	for _, part := range parts {
		i := strings.Index(text, part)
		if i < 0 || i <= last {
			t.Fatalf("missing or out of order %q in:\n%s", part, text)
		}
		last = i
	}
}

func assertNoBootstrapMarkers(t *testing.T, path string) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if strings.Contains(text, "DOCS_CLI_PROJECT_READY") || strings.Contains(text, "DOCS_CLI_BOOTSTRAP_FAILED") {
		t.Fatalf("%s contains a bootstrap marker:\n%s", path, text)
	}
}

func assertNoCredentials(t *testing.T, root string) {
	t.Helper()
	_, err := os.Stat(filepath.Join(root, "credentials.json"))
	if !os.IsNotExist(err) {
		t.Fatalf("credentials.json exists or stat failed: %v", err)
	}
}

func scriptFatal(t *testing.T, stdout, stderr, msg string) {
	t.Helper()
	t.Fatalf("%s\nstdout:\n%s\nstderr:\n%s", msg, stdout, stderr)
}
