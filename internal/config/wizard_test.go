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
	original := []byte(filledEnv() + "# marker-keep\n")
	path := filepath.Join(dir, ".env")
	writeMode(t, path, string(original), 0o644)
	cfg, err := EnsureProductionEnv(dir, productionEnviron(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("content changed:\n%s", got)
	}
	if cfg.DefaultTitle != "Untitled" || cfg.DriveFolderID != sampleFolderID {
		t.Fatalf("title=%q folder=%q", cfg.DefaultTitle, cfg.DriveFolderID)
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
	tty := successTTY(sampleFolderID)
	cfg, err := EnsureProductionEnv(dir, productionEnviron(t), tty)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProjectID != "docs-cli-dev" {
		t.Fatalf("project=%s", cfg.ProjectID)
	}
	got, readErr := os.ReadFile(filepath.Join(dir, ".env"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(got), "# wizard-marker") {
		t.Fatalf("marker missing:\n%s", got)
	}
}

func TestChmodFailureThenRetry(t *testing.T) {
	dir := t.TempDir()
	writeMode(t, filepath.Join(dir, ".env.example"), validEnv(), 0o644)
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
	if _, err := EnsureProductionEnv(dir, productionEnviron(t), successTTY(sampleFolderID)); err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".env")); statErr != nil {
		t.Fatal(statErr)
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
	_, err := EnsureProductionEnv(dir, productionEnviron(t), successTTY(sampleFolderID))
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
	_, err := EnsureProductionEnv(dir, productionEnviron(t), successTTY(sampleFolderID))
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
	assertNoTemp(t, dir)
}

func TestWizardSuccess(t *testing.T) {
	dir := t.TempDir()
	writeMode(t, filepath.Join(dir, ".env.example"), validEnv()+"# wizard-marker\n", 0o644)
	folderURL := "https://drive.google.com/drive/folders/" + sampleFolderID + "?usp=sharing"
	tty := successTTY(folderURL)
	cfg, err := EnsureProductionEnv(dir, productionEnviron(t), tty)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProjectID != "docs-cli-dev" || cfg.ProjectName != "Docs CLI" {
		t.Fatalf("project=%s name=%q", cfg.ProjectID, cfg.ProjectName)
	}
	if cfg.DriveFolderID != sampleFolderID || cfg.ClientID != "abc.apps.googleusercontent.com" || cfg.ClientSecret != wizardSecret {
		t.Fatalf("folder=%q id=%q secret=%q", cfg.DriveFolderID, cfg.ClientID, cfg.ClientSecret)
	}
	line := modeLine(cfg.Mode, ".env")
	if line != "docs-cli: mode=production file=.env" || strings.Count(line, "docs-cli:") != 1 {
		t.Fatalf("mode line %q", line)
	}
	transcript := tty.buf.String()
	if strings.Contains(transcript, wizardSecret) {
		t.Fatalf("secret leaked:\n%s", transcript)
	}
	if !strings.Contains(transcript, "已寫入 GOOGLE_OAUTH_CLIENT_SECRET") {
		t.Fatalf("missing secret acknowledgement:\n%s", transcript)
	}
	if strings.Contains(transcript, "GCP_BILLING_ACCOUNT") || strings.Contains(transcript, "OAUTH_TIMEOUT") || strings.Contains(transcript, "GOOGLE_OAUTH_SCOPE") {
		t.Fatalf("asked a fixed key:\n%s", transcript)
	}
	if strings.Contains(transcript, "GCP_FOLDER_ID:") || strings.Contains(transcript, "GCP_FOLDER_ID [") {
		t.Fatalf("asked GCP_FOLDER_ID:\n%s", transcript)
	}
	got, err := os.ReadFile(filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	if !strings.Contains(text, "# wizard-marker") {
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

	again := &scriptTTY{}
	cfg, err = EnsureProductionEnv(dir, productionEnviron(t), again)
	if err != nil {
		t.Fatal(err)
	}
	if again.reads != 0 {
		t.Fatalf("second run read %d prompts:\n%s", again.reads, again.buf.String())
	}
	if strings.Contains(again.buf.String(), wizardSecret) {
		t.Fatal("second transcript contains secret")
	}
	if cfg.ClientSecret != wizardSecret {
		t.Fatalf("secret=%q", cfg.ClientSecret)
	}
}

func TestWizardRejectsClientIDThenAccepts(t *testing.T) {
	dir := t.TempDir()
	writeMode(t, filepath.Join(dir, ".env.example"), validEnv(), 0o644)
	tty := successTTY(sampleFolderID)
	tty.lines = []string{"", "", "dev@example.com", "dev@example.com", "not-a-client", "abc.apps.googleusercontent.com", sampleFolderID}
	cfg, err := EnsureProductionEnv(dir, productionEnviron(t), tty)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ClientID != "abc.apps.googleusercontent.com" {
		t.Fatalf("id=%q", cfg.ClientID)
	}
	text := tty.buf.String()
	if !strings.Contains(text, "無效的值：") || !strings.Contains(text, "client id 必須以 .apps.googleusercontent.com 結尾") {
		t.Fatalf("transcript:\n%s", text)
	}
}

func TestWizardRejectsProjectName(t *testing.T) {
	dir := t.TempDir()
	writeMode(t, filepath.Join(dir, ".env.example"), validEnv(), 0o644)
	tty := &scriptTTY{lines: []string{"", "A", "Docs!CLI", "Doc's CLI"}}
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
	if !strings.Contains(tty.buf.String(), "project name 必須是 4 到 30 個字元且只含英數、空白與連字號") {
		t.Fatalf("transcript:\n%s", tty.buf.String())
	}
}

func TestWizardRejectsPlaceholderEmail(t *testing.T) {
	dir := t.TempDir()
	body := replaceKey(validEnv(), "GOOGLE_OAUTH_SUPPORT_EMAIL", "you@gmail.com")
	body = replaceKey(body, "GOOGLE_OAUTH_TEST_USER", "you@gmail.com")
	writeMode(t, filepath.Join(dir, ".env.example"), body, 0o644)
	tty := &scriptTTY{lines: []string{"", "", "", "", ""}}
	_, err := EnsureProductionEnv(dir, productionEnviron(t), tty)
	if err == nil || !strings.Contains(err.Error(), "GOOGLE_OAUTH_SUPPORT_EMAIL") {
		t.Fatalf("err=%v", err)
	}
	if !strings.Contains(tty.buf.String(), "不可使用佔位 you@gmail.com") {
		t.Fatalf("transcript:\n%s", tty.buf.String())
	}
}

func TestWizardRejectsEmailWithoutAt(t *testing.T) {
	dir := t.TempDir()
	writeMode(t, filepath.Join(dir, ".env.example"), validEnv(), 0o644)
	tty := &scriptTTY{lines: []string{"", "", "nope", "nope", "nope"}}
	_, err := EnsureProductionEnv(dir, productionEnviron(t), tty)
	if err == nil || !strings.Contains(err.Error(), "GOOGLE_OAUTH_SUPPORT_EMAIL") {
		t.Fatalf("err=%v", err)
	}
	if !strings.Contains(tty.buf.String(), "email 必須包含 @") {
		t.Fatalf("transcript:\n%s", tty.buf.String())
	}
}

func TestWizardRejectsEmptyFolder(t *testing.T) {
	dir := t.TempDir()
	writeMode(t, filepath.Join(dir, ".env.example"), validEnv(), 0o644)
	tty := successTTY("")
	tty.lines = []string{"", "", "dev@example.com", "dev@example.com", "abc.apps.googleusercontent.com", "", "", ""}
	_, err := EnsureProductionEnv(dir, productionEnviron(t), tty)
	if err == nil || !strings.Contains(err.Error(), "GOOGLE_DRIVE_FOLDER_ID") {
		t.Fatalf("err=%v", err)
	}
	if !strings.Contains(tty.buf.String(), "Drive 資料夾不可空白") || strings.Contains(tty.buf.String(), wizardSecret) {
		t.Fatalf("transcript:\n%s", tty.buf.String())
	}
}

func TestWizardRejectsDocsURL(t *testing.T) {
	dir := t.TempDir()
	writeMode(t, filepath.Join(dir, ".env.example"), validEnv(), 0o644)
	docsURL := "https://docs.google.com/document/d/" + sampleFolderID + "/edit"
	tty := successTTY(docsURL)
	tty.lines = []string{"", "", "dev@example.com", "dev@example.com", "abc.apps.googleusercontent.com", docsURL, docsURL, docsURL}
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
	if strings.Contains(tty.buf.String(), wizardSecret) {
		t.Fatal("secret leaked")
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

func successTTY(folder string) *scriptTTY {
	return &scriptTTY{
		lines: []string{
			"",
			"",
			"dev@example.com",
			"dev@example.com",
			"abc.apps.googleusercontent.com",
			folder,
		},
		secrets: []string{wizardSecret},
	}
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
