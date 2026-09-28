package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const wizardSecret = "super-secret-value"

func TestMain(m *testing.M) {
	SetProjectBootstrapForTest(func(string, []string, io.Writer) error {
		return errors.New("docs-cli: bootstrap not stubbed")
	})
	os.Exit(m.Run())
}

func useBootstrap(t *testing.T, fn func(root string, environ []string, stderr io.Writer) error) *int {
	t.Helper()
	var n int
	SetProjectBootstrapForTest(func(root string, environ []string, stderr io.Writer) error {
		n++
		if fn == nil {
			return nil
		}
		return fn(root, environ, stderr)
	})
	t.Cleanup(func() {
		SetProjectBootstrapForTest(func(string, []string, io.Writer) error {
			return errors.New("docs-cli: bootstrap not stubbed")
		})
	})
	return &n
}

func TestEnsureProductionEnvDevelopment(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	dir := t.TempDir()
	writeMode(t, filepath.Join(dir, ".env.example"), validEnv(), 0o644)
	_, err := EnsureProductionEnv(dir, nil, &scriptTTY{})
	if !errors.Is(err, ErrDevelopmentWizard) {
		t.Fatalf("err=%v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".env")); !os.IsNotExist(statErr) {
		t.Fatalf(".env created: %v", statErr)
	}
	_, err = EnsureProductionEnv(dir, []string{"APP_ENV=development"}, &scriptTTY{})
	if !errors.Is(err, ErrDevelopmentWizard) {
		t.Fatalf("explicit development: %v", err)
	}
}

func TestEnsureProductionEnvMissingExample(t *testing.T) {
	dir := t.TempDir()
	_, err := EnsureProductionEnv(dir, productionEnviron(t), nil)
	if err == nil || !strings.Contains(err.Error(), "missing .env.example") {
		t.Fatalf("err=%v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".env")); !os.IsNotExist(statErr) {
		t.Fatalf(".env created: %v", statErr)
	}
}

func TestCopyDoesNotOverwrite(t *testing.T) {
	dir := t.TempDir()
	writeMode(t, filepath.Join(dir, ".env.example"), validEnv(), 0o644)
	original := filledEnv() + "# marker-keep\n"
	path := filepath.Join(dir, ".env")
	writeMode(t, path, original, 0o644)
	calls := useBootstrap(t, nil)
	_, err := EnsureProductionEnv(dir, productionEnviron(t), nil)
	if !errors.Is(err, ErrDesktopClientRequired) {
		t.Fatalf("err=%v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	if !strings.Contains(text, "# marker-keep") || !strings.Contains(text, "GOOGLE_OAUTH_CLIENT_ID=abc.apps.googleusercontent.com") || !strings.Contains(text, "GOOGLE_OAUTH_CLIENT_SECRET="+wizardSecret) {
		t.Fatalf("original fields missing:\n%s", text)
	}
	if !strings.Contains(text, "DOCS_CLI_PROJECT_READY=docs-cli-dev") {
		t.Fatalf("marker missing:\n%s", text)
	}
	cfg, err := EnsureProductionEnv(dir, productionEnviron(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	if *calls != 1 {
		t.Fatalf("bootstrap calls=%d", *calls)
	}
	if cfg.DefaultTitle != "Untitled" || cfg.DriveFolderID != sampleFolderID || cfg.ClientSecret != wizardSecret {
		t.Fatalf("title=%q folder=%q secret=%q", cfg.DefaultTitle, cfg.DriveFolderID, cfg.ClientSecret)
	}
	if runtime.GOOS != "windows" && filePerm(t, path) != 0o600 {
		t.Fatalf("perm=%o", filePerm(t, path))
	}
}

func TestExistingGarbageEnvIsKept(t *testing.T) {
	dir := t.TempDir()
	writeMode(t, filepath.Join(dir, ".env.example"), validEnv(), 0o644)
	path := filepath.Join(dir, ".env")
	writeMode(t, path, "keep-me\n", 0o600)
	_, err := EnsureProductionEnv(dir, productionEnviron(t), &scriptTTY{})
	if err == nil {
		t.Fatal("expected parse error")
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "keep-me\n" {
		t.Fatalf("content=%q", got)
	}
}

func TestEmptyEnvIsNotReplaced(t *testing.T) {
	dir := t.TempDir()
	writeMode(t, filepath.Join(dir, ".env.example"), validEnv(), 0o644)
	path := filepath.Join(dir, ".env")
	writeMode(t, path, "", 0o600)
	_, err := EnsureProductionEnv(dir, productionEnviron(t), &scriptTTY{})
	if err == nil || !strings.Contains(err.Error(), "will not replace an existing .env") {
		t.Fatalf("err=%v", err)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(got) != 0 {
		t.Fatalf("content=%q", got)
	}
}

func TestFixedKeyWillNotReplace(t *testing.T) {
	dir := t.TempDir()
	body := replaceKey(validEnv(), "GOOGLE_OAUTH_SCOPE", "https://www.googleapis.com/auth/documents")
	writeMode(t, filepath.Join(dir, ".env.example"), body, 0o644)
	tty := &scriptTTY{}
	_, err := EnsureProductionEnv(dir, productionEnviron(t), tty)
	if err == nil || !strings.Contains(err.Error(), "GOOGLE_OAUTH_SCOPE") || !strings.Contains(err.Error(), "will not replace an existing .env") {
		t.Fatalf("err=%v", err)
	}
	if tty.reads != 0 {
		t.Fatalf("reads=%d transcript=%s", tty.reads, tty.buf.String())
	}
	got, readErr := os.ReadFile(filepath.Join(dir, ".env"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(got), "https://www.googleapis.com/auth/documents") {
		t.Fatalf("scope was replaced:\n%s", got)
	}
}

func TestCopyFailureThenRetry(t *testing.T) {
	dir := t.TempDir()
	example := validEnv() + "# wizard-marker\n"
	writeMode(t, filepath.Join(dir, ".env.example"), example, 0o644)
	orig := copyEnvData
	t.Cleanup(func() { copyEnvData = orig })
	copyEnvData = func(dst io.Writer, src io.Reader) error {
		_, _ = io.WriteString(dst, "PARTIAL")
		return errors.New("copy failed")
	}
	_, err := EnsureProductionEnv(dir, productionEnviron(t), nil)
	if err == nil {
		t.Fatal("expected copy error")
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".env")); !os.IsNotExist(statErr) {
		t.Fatalf("partial .env remains: %v", statErr)
	}
	copyEnvData = orig
	calls := useBootstrap(t, nil)
	_, err = EnsureProductionEnv(dir, productionEnviron(t), phase1TTY(sampleFolderID))
	if !errors.Is(err, ErrDesktopClientRequired) {
		t.Fatalf("err=%v", err)
	}
	if *calls != 1 {
		t.Fatalf("bootstrap calls=%d", *calls)
	}
	got, readErr := os.ReadFile(filepath.Join(dir, ".env"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	text := string(got)
	if !strings.Contains(text, "# wizard-marker") || !strings.Contains(text, "DOCS_CLI_PROJECT_READY=docs-cli-dev") {
		t.Fatalf("marker missing:\n%s", text)
	}
}

func TestChmodFailureThenRetry(t *testing.T) {
	dir := t.TempDir()
	writeMode(t, filepath.Join(dir, ".env.example"), validEnv()+"# chmod-marker\n", 0o644)
	orig := chmodEnv
	t.Cleanup(func() { chmodEnv = orig })
	chmodEnv = func(string, os.FileMode) error { return errors.New("chmod failed") }
	_, err := EnsureProductionEnv(dir, productionEnviron(t), nil)
	if err == nil {
		t.Fatal("expected chmod error")
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".env")); !os.IsNotExist(statErr) {
		t.Fatalf(".env remains: %v", statErr)
	}
	chmodEnv = orig
	calls := useBootstrap(t, nil)
	_, err = EnsureProductionEnv(dir, productionEnviron(t), phase1TTY(sampleFolderID))
	if !errors.Is(err, ErrDesktopClientRequired) {
		t.Fatalf("err=%v", err)
	}
	if *calls != 1 {
		t.Fatalf("bootstrap calls=%d", *calls)
	}
	got, readErr := os.ReadFile(filepath.Join(dir, ".env"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	text := string(got)
	if !strings.Contains(text, "# chmod-marker") || !strings.Contains(text, "DOCS_CLI_PROJECT_READY=docs-cli-dev") {
		t.Fatalf("marker missing:\n%s", text)
	}
}

func TestEnvSymlinkRejected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	writeMode(t, target, "target-bytes", 0o644)
	writeMode(t, filepath.Join(dir, ".env.example"), validEnv(), 0o644)
	if err := os.Symlink(target, filepath.Join(dir, ".env")); err != nil {
		t.Fatal(err)
	}
	calls := useBootstrap(t, func(string, []string, io.Writer) error {
		t.Errorf("bootstrap called")
		return errors.New("docs-cli: bootstrap not stubbed")
	})
	_, err := EnsureProductionEnv(dir, productionEnviron(t), phase1TTY(sampleFolderID))
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("err=%v", err)
	}
	info, err := os.Lstat(filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink replaced")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "target-bytes" {
		t.Fatalf("target=%q", got)
	}
	if *calls != 0 {
		t.Fatalf("bootstrap calls=%d", *calls)
	}
}

func TestRenameRefusesSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	writeMode(t, target, "target-bytes", 0o644)
	writeMode(t, filepath.Join(dir, ".env.example"), validEnv()+"# wizard-marker\n", 0o644)
	orig := beforeEnvRename
	t.Cleanup(func() { beforeEnvRename = orig })
	swapped := false
	beforeEnvRename = func(dest string) error {
		if swapped {
			return nil
		}
		swapped = true
		if err := os.Remove(dest); err != nil {
			return err
		}
		return os.Symlink(target, dest)
	}
	calls := useBootstrap(t, func(string, []string, io.Writer) error {
		t.Errorf("bootstrap called")
		return errors.New("docs-cli: bootstrap not stubbed")
	})
	_, err := EnsureProductionEnv(dir, productionEnviron(t), phase1TTY(sampleFolderID))
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("err=%v", err)
	}
	info, err := os.Lstat(filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal(".env is not a symlink")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "target-bytes" {
		t.Fatalf("target=%q", got)
	}
	if *calls != 0 {
		t.Fatalf("bootstrap calls=%d", *calls)
	}
	assertNoTemp(t, dir)
}

func TestWizardSuccess(t *testing.T) {
	dir := t.TempDir()
	writeMode(t, filepath.Join(dir, ".env.example"), validEnv()+"# wizard-marker\n", 0o644)
	folderURL := "https://drive.google.com/drive/folders/" + sampleFolderID + "?usp=sharing"
	tty := phase1TTY(folderURL)
	calls := useBootstrap(t, nil)
	_, err := EnsureProductionEnv(dir, productionEnviron(t), tty)
	if !errors.Is(err, ErrDesktopClientRequired) {
		t.Fatalf("err=%v", err)
	}
	if *calls != 1 {
		t.Fatalf("bootstrap calls=%d", *calls)
	}
	if tty.secrets != nil || strings.Contains(tty.buf.String(), "GOOGLE_OAUTH_CLIENT_ID:") || strings.Contains(tty.buf.String(), "GOOGLE_OAUTH_CLIENT_SECRET") {
		t.Fatalf("phase 1 asked for a client:\n%s", tty.buf.String())
	}
	if strings.Contains(tty.buf.String(), "GCP_BILLING_ACCOUNT") || strings.Contains(tty.buf.String(), "OAUTH_TIMEOUT") || strings.Contains(tty.buf.String(), "GOOGLE_OAUTH_SCOPE") {
		t.Fatalf("asked a fixed key:\n%s", tty.buf.String())
	}
	got, err := os.ReadFile(filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	if !strings.Contains(text, "# wizard-marker") || !strings.Contains(text, "DOCS_CLI_PROJECT_READY=docs-cli-dev") {
		t.Fatalf("marker missing:\n%s", text)
	}
	if !strings.Contains(text, "GOOGLE_DRIVE_FOLDER_ID="+sampleFolderID) || strings.Contains(text, "usp=sharing") || strings.Contains(text, "drive.google.com") {
		t.Fatalf("folder line:\n%s", text)
	}
	if !strings.Contains(text, `GCP_PROJECT_NAME="Docs CLI"`) {
		t.Fatalf("project name line:\n%s", text)
	}
	if runtime.GOOS != "windows" && filePerm(t, filepath.Join(dir, ".env")) != 0o600 {
		t.Fatalf("perm=%o", filePerm(t, filepath.Join(dir, ".env")))
	}

	phase2 := phase2TTY("abc.apps.googleusercontent.com", wizardSecret)
	cfg, err := EnsureProductionEnv(dir, productionEnviron(t), phase2)
	if err != nil {
		t.Fatal(err)
	}
	if *calls != 1 {
		t.Fatalf("bootstrap calls=%d", *calls)
	}
	if cfg.ProjectID != "docs-cli-dev" || cfg.ProjectName != "Docs CLI" || cfg.DriveFolderID != sampleFolderID || cfg.ClientID != "abc.apps.googleusercontent.com" || cfg.ClientSecret != wizardSecret {
		t.Fatalf("cfg project=%s name=%q folder=%q id=%q", cfg.ProjectID, cfg.ProjectName, cfg.DriveFolderID, cfg.ClientID)
	}
	line := modeLine(cfg.Mode, ".env")
	if line != "docs-cli: mode=production file=.env" || strings.Count(line, "docs-cli:") != 1 {
		t.Fatalf("mode line %q", line)
	}
	transcript := phase2.buf.String()
	if strings.Contains(transcript, wizardSecret) || !strings.Contains(transcript, "已寫入 GOOGLE_OAUTH_CLIENT_SECRET") {
		t.Fatalf("secret transcript:\n%s", transcript)
	}
	again := &scriptTTY{}
	cfg, err = EnsureProductionEnv(dir, productionEnviron(t), again)
	if err != nil {
		t.Fatal(err)
	}
	if again.reads != 0 || *calls != 1 || cfg.ClientSecret != wizardSecret {
		t.Fatalf("third reads=%d calls=%d secret=%q\n%s", again.reads, *calls, cfg.ClientSecret, again.buf.String())
	}
}

func TestWizardRejectsClientIDThenAccepts(t *testing.T) {
	t.Run("no marker", func(t *testing.T) {
		dir := t.TempDir()
		writeMode(t, filepath.Join(dir, ".env.example"), validEnv(), 0o644)
		tty := phase1TTY(sampleFolderID)
		calls := useBootstrap(t, nil)
		_, err := EnsureProductionEnv(dir, productionEnviron(t), tty)
		if !errors.Is(err, ErrDesktopClientRequired) {
			t.Fatalf("err=%v", err)
		}
		if *calls != 1 || strings.Contains(tty.buf.String(), "client id 必須以 .apps.googleusercontent.com 結尾") {
			t.Fatalf("calls=%d transcript:\n%s", *calls, tty.buf.String())
		}
	})
	t.Run("marker bound", func(t *testing.T) {
		dir := t.TempDir()
		body := replaceKey(phase1Env(), projectReadyKey, "docs-cli-dev")
		writeExistingEnv(t, dir, body)
		tty := &scriptTTY{lines: []string{"not-a-client", "abc.apps.googleusercontent.com"}, secrets: []string{wizardSecret}}
		calls := useBootstrap(t, func(string, []string, io.Writer) error {
			t.Errorf("bootstrap called")
			return errors.New("docs-cli: bootstrap not stubbed")
		})
		cfg, err := EnsureProductionEnv(dir, productionEnviron(t), tty)
		if err != nil {
			t.Fatal(err)
		}
		if *calls != 0 || cfg.ClientID != "abc.apps.googleusercontent.com" {
			t.Fatalf("calls=%d id=%q", *calls, cfg.ClientID)
		}
		text := tty.buf.String()
		if !strings.Contains(text, "無效的值：") || !strings.Contains(text, "client id 必須以 .apps.googleusercontent.com 結尾") {
			t.Fatalf("transcript:\n%s", text)
		}
	})
}

func TestWizardRejectsProjectName(t *testing.T) {
	dir := t.TempDir()
	writeMode(t, filepath.Join(dir, ".env.example"), validEnv(), 0o644)
	tty := &scriptTTY{lines: []string{"", "A", "Docs!CLI", "Doc's CLI"}}
	calls := useBootstrap(t, func(string, []string, io.Writer) error {
		t.Errorf("bootstrap called")
		return errors.New("docs-cli: bootstrap not stubbed")
	})
	_, err := EnsureProductionEnv(dir, productionEnviron(t), tty)
	if err == nil || !strings.Contains(err.Error(), "GCP_PROJECT_NAME") {
		t.Fatalf("err=%v", err)
	}
	got, readErr := os.ReadFile(filepath.Join(dir, ".env"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	text := string(got)
	if strings.Contains(text, "GCP_PROJECT_NAME=A") || strings.Contains(text, "Docs!CLI") || strings.Contains(text, "Doc's CLI") {
		t.Fatalf("rejected name was written:\n%s", text)
	}
	if !strings.Contains(text, "GCP_PROJECT_NAME=Docs CLI") && !strings.Contains(text, `GCP_PROJECT_NAME="Docs CLI"`) {
		t.Fatalf("original name missing:\n%s", text)
	}
	if !strings.Contains(tty.buf.String(), "project name 必須是 4 到 30 個字元且只含英數、空白與連字號") || strings.Contains(tty.buf.String(), "GOOGLE_OAUTH_CLIENT_ID:") {
		t.Fatalf("transcript:\n%s", tty.buf.String())
	}
	if *calls != 0 {
		t.Fatalf("bootstrap calls=%d", *calls)
	}
}

func TestWizardRejectsPlaceholderEmail(t *testing.T) {
	dir := t.TempDir()
	body := replaceKey(validEnv(), "GOOGLE_OAUTH_SUPPORT_EMAIL", "you@gmail.com")
	body = replaceKey(body, "GOOGLE_OAUTH_TEST_USER", "you@gmail.com")
	writeMode(t, filepath.Join(dir, ".env.example"), body, 0o644)
	tty := &scriptTTY{lines: []string{"", "", "", "", ""}}
	calls := useBootstrap(t, func(string, []string, io.Writer) error {
		t.Errorf("bootstrap called")
		return errors.New("docs-cli: bootstrap not stubbed")
	})
	_, err := EnsureProductionEnv(dir, productionEnviron(t), tty)
	if err == nil || !strings.Contains(err.Error(), "GOOGLE_OAUTH_SUPPORT_EMAIL") {
		t.Fatalf("err=%v", err)
	}
	prompts := promptLines(tty.buf.String())
	if len(prompts) < 3 || !strings.Contains(prompts[2], "GOOGLE_OAUTH_SUPPORT_EMAIL") || !strings.Contains(tty.buf.String(), "不可使用佔位 you@gmail.com") || strings.Contains(tty.buf.String(), "GOOGLE_OAUTH_CLIENT_ID:") {
		t.Fatalf("transcript:\n%s", tty.buf.String())
	}
	if *calls != 0 {
		t.Fatalf("bootstrap calls=%d", *calls)
	}
}

func TestWizardRejectsEmailWithoutAt(t *testing.T) {
	dir := t.TempDir()
	body := replaceKey(validEnv(), "GOOGLE_OAUTH_SUPPORT_EMAIL", "not-an-email")
	writeMode(t, filepath.Join(dir, ".env.example"), body, 0o644)
	tty := &scriptTTY{lines: []string{"", "", "nope", "nope", "nope"}}
	calls := useBootstrap(t, func(string, []string, io.Writer) error {
		t.Errorf("bootstrap called")
		return errors.New("docs-cli: bootstrap not stubbed")
	})
	_, err := EnsureProductionEnv(dir, productionEnviron(t), tty)
	if err == nil || !strings.Contains(err.Error(), "GOOGLE_OAUTH_SUPPORT_EMAIL") {
		t.Fatalf("err=%v", err)
	}
	prompts := promptLines(tty.buf.String())
	if len(prompts) < 3 || !strings.Contains(prompts[2], "GOOGLE_OAUTH_SUPPORT_EMAIL") || !strings.Contains(tty.buf.String(), "email 必須包含 @") || strings.Contains(tty.buf.String(), "GOOGLE_OAUTH_CLIENT_ID:") {
		t.Fatalf("transcript:\n%s", tty.buf.String())
	}
	if *calls != 0 {
		t.Fatalf("bootstrap calls=%d", *calls)
	}
}

func TestWizardRejectsEmptyFolder(t *testing.T) {
	dir := t.TempDir()
	writeMode(t, filepath.Join(dir, ".env.example"), validEnv(), 0o644)
	tty := &scriptTTY{lines: []string{"", "", "", "", ""}}
	calls := useBootstrap(t, func(string, []string, io.Writer) error {
		t.Errorf("bootstrap called")
		return errors.New("docs-cli: bootstrap not stubbed")
	})
	_, err := EnsureProductionEnv(dir, productionEnviron(t), tty)
	if err == nil || !strings.Contains(err.Error(), "GOOGLE_DRIVE_FOLDER_ID") {
		t.Fatalf("err=%v", err)
	}
	if !strings.Contains(tty.buf.String(), "Drive 資料夾不可空白") || strings.Contains(tty.buf.String(), "GOOGLE_OAUTH_CLIENT_ID:") {
		t.Fatalf("transcript:\n%s", tty.buf.String())
	}
	if *calls != 0 {
		t.Fatalf("bootstrap calls=%d", *calls)
	}
}

func TestWizardRejectsDocsURL(t *testing.T) {
	dir := t.TempDir()
	writeMode(t, filepath.Join(dir, ".env.example"), validEnv(), 0o644)
	docsURL := "https://docs.google.com/document/d/" + sampleFolderID + "/edit"
	tty := &scriptTTY{lines: []string{"", "", docsURL, docsURL, docsURL}}
	calls := useBootstrap(t, func(string, []string, io.Writer) error {
		t.Errorf("bootstrap called")
		return errors.New("docs-cli: bootstrap not stubbed")
	})
	_, err := EnsureProductionEnv(dir, productionEnviron(t), tty)
	if err == nil || err.Error() != errFolderFormat.Error() {
		t.Fatalf("err=%v", err)
	}
	got, readErr := os.ReadFile(filepath.Join(dir, ".env"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if strings.Contains(string(got), "docs.google.com") {
		t.Fatalf("docs url stored:\n%s", got)
	}
	if strings.Contains(tty.buf.String(), "GOOGLE_OAUTH_CLIENT_ID:") {
		t.Fatalf("transcript:\n%s", tty.buf.String())
	}
	if *calls != 0 {
		t.Fatalf("bootstrap calls=%d", *calls)
	}
}

func TestWriteCredentials(t *testing.T) {
	dir := t.TempDir()
	cfg := sampleConfig(dir)
	if err := WriteCredentials(cfg); err != nil {
		t.Fatal(err)
	}
	path := cfg.CredentialsPath
	if runtime.GOOS != "windows" && filePerm(t, path) != 0o600 {
		t.Fatalf("perm=%o", filePerm(t, path))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(data, []byte("\n")) {
		t.Fatal("missing trailing newline")
	}
	var doc installedFile
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Installed.ClientID != cfg.ClientID || doc.Installed.ClientSecret != cfg.ClientSecret || doc.Installed.ProjectID != cfg.ProjectID {
		t.Fatalf("doc=%+v", doc.Installed)
	}
	if len(doc.Installed.RedirectURIs) != 1 || doc.Installed.RedirectURIs[0] != redirectURIValue {
		t.Fatalf("redirect=%v", doc.Installed.RedirectURIs)
	}
	cfg.ClientSecret = "rotated-secret-value"
	if err := WriteCredentials(cfg); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte("rotated-secret-value")) || bytes.Contains(data, []byte(wizardSecret)) {
		t.Fatalf("overwrite failed:\n%s", data)
	}

	cfg.ClientID = ""
	other := filepath.Join(dir, "nope.json")
	cfg.CredentialsPath = other
	if err := WriteCredentials(cfg); err == nil {
		t.Fatal("expected missing client")
	}
	if _, statErr := os.Stat(other); !os.IsNotExist(statErr) {
		t.Fatalf("credentials created: %v", statErr)
	}
}

func TestWriteCredentialsRejectsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	writeMode(t, target, "target-bytes", 0o644)
	cfg := sampleConfig(dir)
	if err := os.Symlink(target, cfg.CredentialsPath); err != nil {
		t.Fatal(err)
	}
	if err := WriteCredentials(cfg); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("err=%v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "target-bytes" {
		t.Fatalf("target=%q", got)
	}
	info, err := os.Lstat(cfg.CredentialsPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink replaced")
	}
	assertNoTemp(t, dir)
}

func TestRequireClientFilename(t *testing.T) {
	err := RequireClient(&Config{FilePath: "/tmp/project/.env.example"})
	if err == nil || err.Error() != "docs-cli: missing GOOGLE_OAUTH_CLIENT_ID in .env.example" {
		t.Fatalf("err=%v", err)
	}
	err = RequireClient(&Config{FilePath: "/tmp/project/.env", ClientID: "abc.apps.googleusercontent.com"})
	if err == nil || err.Error() != "docs-cli: missing GOOGLE_OAUTH_CLIENT_SECRET in .env" {
		t.Fatalf("err=%v", err)
	}
}

func TestMarkEnvAsksOnlyFolderThenPauses(t *testing.T) {
	dir := t.TempDir()
	writeExistingEnv(t, dir, validEnv())
	folderURL := "https://drive.google.com/drive/folders/" + sampleFolderID + "?usp=sharing"
	tty := &scriptTTY{lines: []string{folderURL}}
	var gotEnv []string
	calls := useBootstrap(t, func(_ string, environ []string, _ io.Writer) error {
		gotEnv = append([]string(nil), environ...)
		return nil
	})
	_, err := EnsureProductionEnv(dir, productionEnviron(t), tty)
	if !errors.Is(err, ErrDesktopClientRequired) {
		t.Fatalf("err=%v", err)
	}
	if *calls != 1 || !strings.Contains(strings.Join(gotEnv, "\n"), "APP_ENV=production") {
		t.Fatalf("calls=%d env=%q", *calls, gotEnv)
	}
	text := tty.buf.String()
	if !strings.Contains(text, "GOOGLE_DRIVE_FOLDER_ID") || strings.Contains(text, "GOOGLE_OAUTH_CLIENT_ID") {
		t.Fatalf("transcript:\n%s", text)
	}
	body, err := os.ReadFile(filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	file := string(body)
	if !strings.Contains(file, "DOCS_CLI_PROJECT_READY=docs-cli-dev") || !strings.Contains(file, "GOOGLE_DRIVE_FOLDER_ID="+sampleFolderID) {
		t.Fatalf("env:\n%s", file)
	}
	if got := envAssignment(file, "GOOGLE_OAUTH_CLIENT_ID"); got != "" {
		t.Fatalf("client id %q", got)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "credentials.json")); !os.IsNotExist(statErr) {
		t.Fatalf("credentials: %v", statErr)
	}
}

func TestPhase2AsksClientIDOnly(t *testing.T) {
	dir := t.TempDir()
	body := replaceKey(phase1Env(), projectReadyKey, "docs-cli-dev")
	writeExistingEnv(t, dir, body)
	tty := phase2TTY("abc.apps.googleusercontent.com", wizardSecret)
	calls := useBootstrap(t, func(string, []string, io.Writer) error {
		t.Errorf("bootstrap called")
		return errors.New("docs-cli: bootstrap not stubbed")
	})
	cfg, err := EnsureProductionEnv(dir, productionEnviron(t), tty)
	if err != nil {
		t.Fatal(err)
	}
	if *calls != 0 || !clientIDPattern.MatchString(cfg.ClientID) {
		t.Fatalf("calls=%d id=%q", *calls, cfg.ClientID)
	}
	text := tty.buf.String()
	if !strings.Contains(text, "已寫入 GOOGLE_OAUTH_CLIENT_SECRET") || strings.Contains(text, wizardSecret) {
		t.Fatalf("transcript:\n%s", text)
	}
	if strings.Contains(text, "GCP_PROJECT_ID") || strings.Contains(text, "GOOGLE_DRIVE_FOLDER_ID") {
		t.Fatalf("asked phase 1:\n%s", text)
	}
	again := &scriptTTY{}
	if _, err := EnsureProductionEnv(dir, productionEnviron(t), again); err != nil {
		t.Fatal(err)
	}
	if again.reads != 0 || *calls != 0 {
		t.Fatalf("third reads=%d calls=%d", again.reads, *calls)
	}
}

func TestProjectIDPromptClearsClientAndMarker(t *testing.T) {
	dir := t.TempDir()
	body := phase1Env()
	body = replaceKey(body, "GOOGLE_OAUTH_CLIENT_ID", "abc.apps.googleusercontent.com")
	body = replaceKey(body, "GOOGLE_OAUTH_CLIENT_SECRET", wizardSecret)
	body = replaceKey(body, projectReadyKey, "docs-cli-dev")
	writeMode(t, filepath.Join(dir, ".env.example"), body, 0o644)
	stderr := captureWizardStderr(t)
	var snapshot string
	orig := beforeEnvRename
	t.Cleanup(func() { beforeEnvRename = orig })
	beforeEnvRename = func(dest string) error {
		if snapshot != "" {
			return nil
		}
		entries, err := os.ReadDir(filepath.Dir(dest))
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if !strings.Contains(entry.Name(), ".tmp") {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(filepath.Dir(dest), entry.Name()))
			if err != nil {
				return err
			}
			if strings.Contains(string(raw), "GCP_PROJECT_ID=docs-cli-new") {
				snapshot = string(raw)
			}
		}
		return nil
	}
	tty := &scriptTTY{lines: []string{"docs-cli-new", ""}}
	calls := useBootstrap(t, nil)
	_, err := EnsureProductionEnv(dir, productionEnviron(t), tty)
	if !errors.Is(err, ErrDesktopClientRequired) {
		t.Fatalf("err=%v", err)
	}
	if *calls != 1 {
		t.Fatalf("bootstrap calls=%d", *calls)
	}
	if snapshot == "" {
		t.Fatal("did not capture the project id write")
	}
	vals, err := parseEnv("snapshot", []byte(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	if vals["GCP_PROJECT_ID"] != "docs-cli-new" || vals["GOOGLE_OAUTH_CLIENT_ID"] != "" || vals["GOOGLE_OAUTH_CLIENT_SECRET"] != "" || vals[projectReadyKey] != "" {
		t.Fatalf("snapshot vals=%v\n%s", vals, snapshot)
	}
	full := "已清掉 client id、secret 與專案就緒標記：GCP_PROJECT_ID 與標記不一致"
	if strings.Contains(tty.buf.String(), full) || strings.Contains(stderr.String(), full) {
		t.Fatalf("status line leaked\ntty:\n%s\nstderr:\n%s", tty.buf.String(), stderr.String())
	}
	if !strings.Contains(tty.buf.String(), "清掉 client id、secret 與專案就緒標記") {
		t.Fatalf("prompt:\n%s", tty.buf.String())
	}
}

func TestHandEditedProjectIDClearsClientBeforeBootstrap(t *testing.T) {
	dir := t.TempDir()
	body := phase1Env()
	body = replaceKey(body, "GCP_PROJECT_ID", "docs-cli-new")
	body = replaceKey(body, "GOOGLE_OAUTH_CLIENT_ID", "abc.apps.googleusercontent.com")
	body = replaceKey(body, "GOOGLE_OAUTH_CLIENT_SECRET", wizardSecret)
	body = replaceKey(body, projectReadyKey, "docs-cli-dev")
	writeExistingEnv(t, dir, body)
	stderr := captureWizardStderr(t)
	tty := &scriptTTY{}
	calls := useBootstrap(t, func(_ string, environ []string, _ io.Writer) error {
		if !strings.Contains(strings.Join(environ, "\n"), "APP_ENV=production") {
			t.Errorf("environ %q", environ)
		}
		return nil
	})
	_, err := EnsureProductionEnv(dir, productionEnviron(t), tty)
	if !errors.Is(err, ErrDesktopClientRequired) {
		t.Fatalf("err=%v", err)
	}
	if *calls != 1 || tty.reads != 0 || strings.Contains(tty.buf.String(), "GOOGLE_OAUTH_CLIENT_ID:") {
		t.Fatalf("calls=%d reads=%d\n%s", *calls, tty.reads, tty.buf.String())
	}
	if !strings.Contains(stderr.String(), staleMarkerCleared) {
		t.Fatalf("stderr %q", stderr.String())
	}
	file, err := os.ReadFile(filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	vals, err := parseEnv("env", file)
	if err != nil {
		t.Fatal(err)
	}
	if vals[projectReadyKey] != "docs-cli-new" || vals["GOOGLE_OAUTH_CLIENT_ID"] != "" {
		t.Fatalf("vals ready=%q client=%q", vals[projectReadyKey], vals["GOOGLE_OAUTH_CLIENT_ID"])
	}
}

func TestBootstrapFailureDoesNotAskClientID(t *testing.T) {
	dir := t.TempDir()
	writeExistingEnv(t, dir, phase1Env())
	tty := &scriptTTY{}
	calls := useBootstrap(t, func(string, []string, io.Writer) error {
		return errors.New("gcloud is not installed; project setup was not run")
	})
	_, err := EnsureProductionEnv(dir, productionEnviron(t), tty)
	if err == nil || errors.Is(err, ErrDesktopClientRequired) {
		t.Fatalf("err=%v", err)
	}
	if !strings.Contains(err.Error(), "專案尚未標成就緒") || !strings.Contains(err.Error(), "docs-cli: bootstrap failed:") {
		t.Fatalf("err=%v", err)
	}
	if *calls != 1 || tty.reads != 0 {
		t.Fatalf("calls=%d reads=%d", *calls, tty.reads)
	}
	file, err := os.ReadFile(filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	vals, err := parseEnv("env", file)
	if err != nil {
		t.Fatal(err)
	}
	if vals[projectReadyKey] != "" || vals[bootstrapFailedKey] != "1" {
		t.Fatalf("ready=%q failed=%q\n%s", vals[projectReadyKey], vals[bootstrapFailedKey], file)
	}
}

func TestBootstrapFailureReasksProjectID(t *testing.T) {
	dir := t.TempDir()
	body := replaceKey(phase1Env(), bootstrapFailedKey, "1")
	writeExistingEnv(t, dir, body)
	tty := &scriptTTY{lines: []string{""}}
	calls := useBootstrap(t, nil)
	_, err := EnsureProductionEnv(dir, productionEnviron(t), tty)
	if !errors.Is(err, ErrDesktopClientRequired) {
		t.Fatalf("err=%v", err)
	}
	if *calls != 1 {
		t.Fatalf("bootstrap calls=%d", *calls)
	}
	prompts := promptLines(tty.buf.String())
	if len(prompts) != 1 || !strings.Contains(prompts[0], "GCP_PROJECT_ID") || strings.Contains(tty.buf.String(), "GOOGLE_OAUTH_CLIENT_ID:") {
		t.Fatalf("transcript:\n%s", tty.buf.String())
	}
	file, err := os.ReadFile(filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	vals, err := parseEnv("env", file)
	if err != nil {
		t.Fatal(err)
	}
	if vals[projectReadyKey] != "docs-cli-dev" || vals[bootstrapFailedKey] != "" {
		t.Fatalf("ready=%q failed=%q", vals[projectReadyKey], vals[bootstrapFailedKey])
	}
}

func TestInvalidClientIDWithoutMarkerDoesNotExecBootstrap(t *testing.T) {
	for _, id := range []string{"not-a-client", "   "} {
		t.Run(id, func(t *testing.T) {
			dir := t.TempDir()
			body := replaceKey(phase1Env(), "GOOGLE_OAUTH_CLIENT_ID", id)
			body = replaceKey(body, "GOOGLE_OAUTH_CLIENT_SECRET", wizardSecret)
			writeExistingEnv(t, dir, body)
			calls := useBootstrap(t, func(string, []string, io.Writer) error {
				t.Errorf("bootstrap called")
				return errors.New("docs-cli: bootstrap not stubbed")
			})
			_, err := EnsureProductionEnv(dir, productionEnviron(t), nil)
			if err == nil || errors.Is(err, ErrDesktopClientRequired) {
				t.Fatalf("err=%v", err)
			}
			if !strings.Contains(err.Error(), "docs-cli: invalid GOOGLE_OAUTH_CLIENT_ID") || !strings.Contains(err.Error(), "清掉 .env 的 GOOGLE_OAUTH_CLIENT_ID") {
				t.Fatalf("err=%v", err)
			}
			if *calls != 0 {
				t.Fatalf("bootstrap calls=%d", *calls)
			}
			file, readErr := os.ReadFile(filepath.Join(dir, ".env"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			vals, parseErr := parseEnv("env", file)
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			if vals[projectReadyKey] != "" || vals[bootstrapFailedKey] == "1" {
				t.Fatalf("ready=%q failed=%q", vals[projectReadyKey], vals[bootstrapFailedKey])
			}
		})
	}
	t.Run("legal id still bootstraps", func(t *testing.T) {
		dir := t.TempDir()
		body := replaceKey(phase1Env(), "GOOGLE_OAUTH_CLIENT_ID", "abc.apps.googleusercontent.com")
		body = replaceKey(body, "GOOGLE_OAUTH_CLIENT_SECRET", wizardSecret)
		writeExistingEnv(t, dir, body)
		calls := useBootstrap(t, nil)
		_, err := EnsureProductionEnv(dir, productionEnviron(t), nil)
		if !errors.Is(err, ErrDesktopClientRequired) {
			t.Fatalf("err=%v", err)
		}
		if *calls != 1 {
			t.Fatalf("bootstrap calls=%d", *calls)
		}
		file, readErr := os.ReadFile(filepath.Join(dir, ".env"))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if envAssignment(string(file), "GOOGLE_OAUTH_CLIENT_ID") != "abc.apps.googleusercontent.com" {
			t.Fatalf("client cleared:\n%s", file)
		}
	})
}

func TestBootstrapNotCalledWhenMarkerMatches(t *testing.T) {
	dir := t.TempDir()
	body := replaceKey(filledEnv(), projectReadyKey, "docs-cli-dev")
	writeExistingEnv(t, dir, body)
	calls := useBootstrap(t, func(string, []string, io.Writer) error {
		t.Errorf("bootstrap called")
		return errors.New("docs-cli: bootstrap not stubbed")
	})
	cfg, err := EnsureProductionEnv(dir, productionEnviron(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	if *calls != 0 || cfg.ClientID != "abc.apps.googleusercontent.com" || cfg.ClientSecret != wizardSecret {
		t.Fatalf("calls=%d id=%q", *calls, cfg.ClientID)
	}
}

type scriptTTY struct {
	lines   []string
	secrets []string
	buf     bytes.Buffer
	reads   int
}

func (s *scriptTTY) IsTerminal() bool { return true }

func (s *scriptTTY) ReadLine(prompt string) (string, error) {
	s.reads++
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
	s.reads++
	s.buf.WriteString(prompt)
	s.buf.WriteByte('\n')
	if len(s.secrets) == 0 {
		return "", fmt.Errorf("unexpected ReadSecret %q", prompt)
	}
	value := s.secrets[0]
	s.secrets = s.secrets[1:]
	return value, nil
}

func (s *scriptTTY) WriteStatus(line string) error {
	s.buf.WriteString(line)
	s.buf.WriteByte('\n')
	return nil
}

func phase1TTY(folder string) *scriptTTY {
	return &scriptTTY{lines: []string{"", "", folder}}
}

func phase2TTY(clientID, secret string) *scriptTTY {
	return &scriptTTY{lines: []string{clientID}, secrets: []string{secret}}
}

func promptLines(transcript string) []string {
	var out []string
	for _, line := range strings.Split(transcript, "\n") {
		if strings.HasPrefix(line, "GCP_") || strings.HasPrefix(line, "GOOGLE_") {
			out = append(out, line)
		}
	}
	return out
}

func phase1Env() string {
	return replaceKey(validEnv(), "GOOGLE_DRIVE_FOLDER_ID", sampleFolderID)
}

func envAssignment(content, key string) string {
	vals, err := parseEnv("env", []byte(content))
	if err != nil {
		return ""
	}
	return vals[key]
}

func writeExistingEnv(t *testing.T, dir, body string) {
	t.Helper()
	writeMode(t, filepath.Join(dir, ".env.example"), validEnv(), 0o644)
	writeMode(t, filepath.Join(dir, ".env"), body, 0o600)
}

func captureWizardStderr(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := wizardStderr
	wizardStderr = &buf
	t.Cleanup(func() { wizardStderr = prev })
	return &buf
}

func filledEnv() string {
	s := validEnv()
	s = replaceKey(s, "GOOGLE_OAUTH_SUPPORT_EMAIL", "dev@example.com")
	s = replaceKey(s, "GOOGLE_OAUTH_TEST_USER", "dev@example.com")
	s = replaceKey(s, "GOOGLE_OAUTH_CLIENT_ID", "abc.apps.googleusercontent.com")
	s = replaceKey(s, "GOOGLE_OAUTH_CLIENT_SECRET", wizardSecret)
	s = replaceKey(s, "GOOGLE_DRIVE_FOLDER_ID", sampleFolderID)
	return s
}

func sampleConfig(dir string) *Config {
	return &Config{
		FilePath:        filepath.Join(dir, ".env"),
		ProjectID:       "docs-cli-dev",
		ClientID:        "abc.apps.googleusercontent.com",
		ClientSecret:    wizardSecret,
		AuthURI:         authURIValue,
		TokenURI:        tokenURIValue,
		CertURL:         certURLValue,
		RedirectURI:     redirectURIValue,
		CredentialsPath: filepath.Join(dir, "credentials.json"),
	}
}

func productionEnviron(t *testing.T) []string {
	t.Helper()
	t.Setenv("APP_ENV", "staging")
	return []string{"APP_ENV=production"}
}

func assertNoTemp(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp") {
			t.Fatalf("temp file remains: %s", entry.Name())
		}
	}
}
