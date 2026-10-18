package config

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const sampleFolderID = "1AbC_def-234"

func TestResolveMode(t *testing.T) {
	t.Setenv("APP_ENV", "production")

	mode, err := ResolveMode(nil)
	if err != nil || mode != "development" {
		t.Fatalf("nil environ: mode=%q err=%v", mode, err)
	}
	mode, err = ResolveMode([]string{})
	if err != nil || mode != "development" {
		t.Fatalf("empty environ: mode=%q err=%v", mode, err)
	}
	mode, err = ResolveMode([]string{"APP_ENV="})
	if err != nil || mode != "development" {
		t.Fatalf("empty APP_ENV: mode=%q err=%v", mode, err)
	}
	mode, err = ResolveMode([]string{"OTHER=production"})
	if err != nil || mode != "development" {
		t.Fatalf("unrelated entry: mode=%q err=%v", mode, err)
	}
	mode, err = ResolveMode([]string{"APP_ENV=development", "APP_ENV=production"})
	if err != nil || mode != "production" {
		t.Fatalf("last wins: mode=%q err=%v", mode, err)
	}
	mode, err = ResolveMode([]string{"APP_ENV=development"})
	if err != nil || mode != "development" {
		t.Fatalf("development: mode=%q err=%v", mode, err)
	}
	if _, err = ResolveMode([]string{"APP_ENV=staging"}); err == nil || !strings.Contains(err.Error(), "APP_ENV must be development or production") {
		t.Fatalf("staging: %v", err)
	}

	original := defaultAppEnv
	t.Cleanup(func() { defaultAppEnv = original })
	defaultAppEnv = "production"
	mode, err = ResolveMode(nil)
	if err != nil || mode != "production" {
		t.Fatalf("default production: mode=%q err=%v", mode, err)
	}
	mode, err = ResolveMode([]string{"APP_ENV=development"})
	if err != nil || mode != "development" {
		t.Fatalf("process overrides default: mode=%q err=%v", mode, err)
	}
	defaultAppEnv = "staging"
	if _, err = ResolveMode(nil); err == nil {
		t.Fatal("invalid defaultAppEnv succeeded")
	}
	mode, err = ResolveMode([]string{"APP_ENV=production"})
	if err != nil || mode != "production" {
		t.Fatalf("process wins over bad default: mode=%q err=%v", mode, err)
	}
}

func TestNormalizeFolderID(t *testing.T) {
	sharing := "https://drive.google.com/drive/folders/" + sampleFolderID + "?usp=sharing"
	userURL := "https://drive.google.com/drive/u/0/folders/" + sampleFolderID + "?usp=sharing"
	tests := []struct {
		name string
		raw  string
		want string
		err  string
	}{
		{name: "id", raw: sampleFolderID, want: sampleFolderID},
		{name: "padded id", raw: "  " + sampleFolderID + "  ", want: sampleFolderID},
		{name: "folders url", raw: sharing, want: sampleFolderID},
		{name: "user url", raw: userURL, want: sampleFolderID},
		{name: "trailing slash", raw: "https://drive.google.com/drive/folders/" + sampleFolderID + "/", want: sampleFolderID},
		{name: "empty", raw: "", err: "docs-cli: GOOGLE_DRIVE_FOLDER_ID is required"},
		{name: "spaces", raw: "   ", err: "docs-cli: GOOGLE_DRIVE_FOLDER_ID is required"},
		{name: "short", raw: "short", err: "docs-cli: GOOGLE_DRIVE_FOLDER_ID is not a Drive folder URL or id"},
		{name: "docs url", raw: "https://docs.google.com/document/d/" + sampleFolderID + "/edit", err: "docs-cli: GOOGLE_DRIVE_FOLDER_ID is not a Drive folder URL or id"},
		{name: "http", raw: "http://drive.google.com/drive/folders/" + sampleFolderID, err: "docs-cli: GOOGLE_DRIVE_FOLDER_ID is not a Drive folder URL or id"},
		{name: "bad user", raw: "https://drive.google.com/drive/u/x/folders/" + sampleFolderID, err: "docs-cli: GOOGLE_DRIVE_FOLDER_ID is not a Drive folder URL or id"},
		{name: "short in url", raw: "https://drive.google.com/drive/folders/short", err: "docs-cli: GOOGLE_DRIVE_FOLDER_ID is not a Drive folder URL or id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeFolderID(tt.raw)
			if tt.err != "" {
				if err == nil || err.Error() != tt.err {
					t.Fatalf("err=%v, want %s", err, tt.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("id=%q, want %q", got, tt.want)
			}
		})
	}
}

func TestLoadValidExample(t *testing.T) {
	t.Setenv("APP_ENV", "staging")
	dir := t.TempDir()
	path := filepath.Join(dir, ".env.example")
	writeMode(t, path, validEnv(), 0o644)
	before := filePerm(t, path)

	cfg, err := Load(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if filePerm(t, path) != before {
		t.Fatalf("example mode changed from %o to %o", before, filePerm(t, path))
	}
	if _, err := os.Stat(filepath.Join(dir, ".env")); !os.IsNotExist(err) {
		t.Fatalf(".env created or stat failed: %v", err)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != "development" || cfg.Root != abs || cfg.FilePath != path {
		t.Fatalf("mode=%s root=%s file=%s", cfg.Mode, cfg.Root, cfg.FilePath)
	}
	if cfg.DriveFolderID != "" || cfg.GCPFolderID != "" || cfg.OrganizationID != "" || cfg.BillingAccount != "" {
		t.Fatalf("optional ids = drive %q gcp %q org %q billing %q", cfg.DriveFolderID, cfg.GCPFolderID, cfg.OrganizationID, cfg.BillingAccount)
	}
	if cfg.ProjectID != "docs-cli-dev" || cfg.ProjectName != "Docs CLI" || cfg.DefaultTitle != "Untitled" {
		t.Fatalf("project=%s name=%q title=%q", cfg.ProjectID, cfg.ProjectName, cfg.DefaultTitle)
	}
	if cfg.InsertIndex != 1 || cfg.OAuthTimeout != 3*time.Minute || cfg.Scope != driveScope {
		t.Fatalf("index=%d timeout=%s scope=%s", cfg.InsertIndex, cfg.OAuthTimeout, cfg.Scope)
	}
	if cfg.ClientID != "" || cfg.ClientSecret != "" {
		t.Fatalf("client id=%q secret=%q", cfg.ClientID, cfg.ClientSecret)
	}
	if cfg.CredentialsPath != filepath.Join(abs, "credentials.json") {
		t.Fatalf("credentials path %s", cfg.CredentialsPath)
	}
	if cfg.DocsAPI != docsAPIHost || cfg.DriveAPI != driveAPIHost || cfg.RedirectURI != redirectURIValue {
		t.Fatalf("apis %s %s redirect %s", cfg.DocsAPI, cfg.DriveAPI, cfg.RedirectURI)
	}
	if cfg.SupportEmail != "dev@example.com" || cfg.TestUser != "dev@example.com" {
		t.Fatalf("emails %s %s", cfg.SupportEmail, cfg.TestUser)
	}
	if cfg.UserType != "external" || cfg.PublishingStatus != "testing" || cfg.ClientType != "installed" || cfg.PKCEMethod != "S256" || cfg.AccessType != "offline" || cfg.Prompt != "consent" {
		t.Fatalf("oauth flags %+v", cfg)
	}
}

func TestLoadParser(t *testing.T) {
	t.Setenv("APP_ENV", "staging")
	tests := []struct {
		name    string
		extra   string
		title   string
		wantErr string
	}{
		{name: "export", extra: "export DOCS_DEFAULT_TITLE=FromExport\n", title: "FromExport"},
		{name: "quotes", extra: "DOCS_DEFAULT_TITLE=\"Quoted Title\"\n", title: "Quoted Title"},
		{name: "single quotes", extra: "DOCS_DEFAULT_TITLE='Single'\n", title: "Single"},
		{name: "duplicate", extra: "DOCS_DEFAULT_TITLE=First\nDOCS_DEFAULT_TITLE=Second\n", title: "Second"},
		{name: "leading space", extra: "   DOCS_DEFAULT_TITLE=Spaced\n", title: "Spaced"},
		{name: "comment", extra: "# DOCS_DEFAULT_TITLE=Ignored\n", title: "Untitled"},
		{name: "hash in value", extra: "DOCS_DEFAULT_TITLE=Title # keep\n", title: "Title # keep"},
		{name: "trailing space", extra: "DOCS_DEFAULT_TITLE=Untitled \n", title: "Untitled "},
		{name: "no escape", extra: "DOCS_DEFAULT_TITLE=\"A\\\"B\"\n", title: "A\\\"B"},
		{name: "missing equals", extra: "FOO\n", wantErr: "missing ="},
		{name: "bad key", extra: "1BAD=value\n", wantErr: "invalid env key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeMode(t, filepath.Join(dir, ".env.example"), validEnv()+tt.extra, 0o644)
			cfg, err := Load(dir, nil)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err=%v, want %s", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.DefaultTitle != tt.title {
				t.Fatalf("title=%q, want %q", cfg.DefaultTitle, tt.title)
			}
		})
	}
}

func TestLoadCarriageReturn(t *testing.T) {
	t.Setenv("APP_ENV", "staging")
	t.Run("dos newline", func(t *testing.T) {
		dir := t.TempDir()
		body := strings.ReplaceAll(validEnv(), "\n", "\r\n")
		writeMode(t, filepath.Join(dir, ".env.example"), body, 0o644)
		cfg, err := Load(dir, nil)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.DefaultTitle != "Untitled" || strings.Contains(cfg.DefaultTitle, "\r") {
			t.Fatalf("title=%q", cfg.DefaultTitle)
		}
	})
	t.Run("one trailing CR", func(t *testing.T) {
		dir := t.TempDir()
		body := strings.Replace(validEnv(), "DOCS_DEFAULT_TITLE=Untitled\n", "DOCS_DEFAULT_TITLE=Untitled\r\r\n", 1)
		writeMode(t, filepath.Join(dir, ".env.example"), body, 0o644)
		cfg, err := Load(dir, nil)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.DefaultTitle != "Untitled\r" {
			t.Fatalf("title=%q", cfg.DefaultTitle)
		}
	})
	t.Run("CR in the middle", func(t *testing.T) {
		dir := t.TempDir()
		body := strings.Replace(validEnv(), "DOCS_DEFAULT_TITLE=Untitled\n", "DOCS_DEFAULT_TITLE=Untit\rled\n", 1)
		writeMode(t, filepath.Join(dir, ".env.example"), body, 0o644)
		cfg, err := Load(dir, nil)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.DefaultTitle != "Untit\rled" {
			t.Fatalf("title=%q", cfg.DefaultTitle)
		}
	})
}

func TestLoadValidation(t *testing.T) {
	t.Setenv("APP_ENV", "staging")
	tests := []struct {
		name   string
		mutate func(string) string
		want   string
	}{
		{name: "missing drive api", mutate: func(s string) string { return deleteKey(s, "GOOGLE_DRIVE_API") }, want: "missing GOOGLE_DRIVE_API"},
		{name: "insert 0", mutate: func(s string) string { return replaceKey(s, "DOCS_INSERT_INDEX", "0") }, want: "invalid DOCS_INSERT_INDEX"},
		{name: "insert 2", mutate: func(s string) string { return replaceKey(s, "DOCS_INSERT_INDEX", "2") }, want: "invalid DOCS_INSERT_INDEX"},
		{name: "readonly scope", mutate: func(s string) string {
			return replaceKey(s, "GOOGLE_OAUTH_SCOPE", "https://www.googleapis.com/auth/drive.readonly")
		}, want: "invalid GOOGLE_OAUTH_SCOPE"},
		{name: "documents scope", mutate: func(s string) string {
			return replaceKey(s, "GOOGLE_OAUTH_SCOPE", "https://www.googleapis.com/auth/documents")
		}, want: "invalid GOOGLE_OAUTH_SCOPE"},
		{name: "drive.file scope", mutate: func(s string) string {
			return replaceKey(s, "GOOGLE_OAUTH_SCOPE", "https://www.googleapis.com/auth/drive.file")
		}, want: "invalid GOOGLE_OAUTH_SCOPE"},
		{name: "two scopes", mutate: func(s string) string {
			return replaceKey(s, "GOOGLE_OAUTH_SCOPE", "https://www.googleapis.com/auth/drive https://www.googleapis.com/auth/documents")
		}, want: "invalid GOOGLE_OAUTH_SCOPE"},
		{name: "user type", mutate: func(s string) string { return replaceKey(s, "GOOGLE_OAUTH_USER_TYPE", "internal") }, want: "invalid GOOGLE_OAUTH_USER_TYPE"},
		{name: "publishing", mutate: func(s string) string { return replaceKey(s, "GOOGLE_OAUTH_PUBLISHING_STATUS", "production") }, want: "invalid GOOGLE_OAUTH_PUBLISHING_STATUS"},
		{name: "both org and folder", mutate: func(s string) string {
			return replaceKey(replaceKey(s, "GCP_ORGANIZATION_ID", "123"), "GCP_FOLDER_ID", "456")
		}, want: "GCP_ORGANIZATION_ID"},
		{name: "bad client id", mutate: func(s string) string { return replaceKey(s, "GOOGLE_OAUTH_CLIENT_ID", "not-a-client") }, want: "invalid GOOGLE_OAUTH_CLIENT_ID"},
		{name: "timeout 0", mutate: func(s string) string { return replaceKey(s, "OAUTH_TIMEOUT", "0") }, want: "invalid OAUTH_TIMEOUT"},
		{name: "timeout 16m", mutate: func(s string) string { return replaceKey(s, "OAUTH_TIMEOUT", "16m") }, want: "invalid OAUTH_TIMEOUT"},
		{name: "timeout 2h", mutate: func(s string) string { return replaceKey(s, "OAUTH_TIMEOUT", "2h") }, want: "invalid OAUTH_TIMEOUT"},
		{name: "absolute path", mutate: func(s string) string { return replaceKey(s, "GOOGLE_CREDENTIALS_PATH", "/tmp/credentials.json") }, want: "invalid GOOGLE_CREDENTIALS_PATH"},
		{name: "dotdot path", mutate: func(s string) string { return replaceKey(s, "GOOGLE_CREDENTIALS_PATH", "../credentials.json") }, want: "invalid GOOGLE_CREDENTIALS_PATH"},
		{name: "nested dotdot", mutate: func(s string) string { return replaceKey(s, "GOOGLE_CREDENTIALS_PATH", "foo/../../credentials.json") }, want: "invalid GOOGLE_CREDENTIALS_PATH"},
		{name: "placeholder support", mutate: func(s string) string { return replaceKey(s, "GOOGLE_OAUTH_SUPPORT_EMAIL", "you@gmail.com") }, want: "invalid GOOGLE_OAUTH_SUPPORT_EMAIL"},
		{name: "placeholder test user", mutate: func(s string) string { return replaceKey(s, "GOOGLE_OAUTH_TEST_USER", "you@gmail.com") }, want: "invalid GOOGLE_OAUTH_TEST_USER"},
		{name: "email without at", mutate: func(s string) string { return replaceKey(s, "GOOGLE_OAUTH_SUPPORT_EMAIL", "userexample.com") }, want: "invalid GOOGLE_OAUTH_SUPPORT_EMAIL"},
		{name: "bad project id", mutate: func(s string) string { return replaceKey(s, "GCP_PROJECT_ID", "DOCS") }, want: "invalid GCP_PROJECT_ID"},
		{name: "plain pkce", mutate: func(s string) string { return replaceKey(s, "GOOGLE_OAUTH_PKCE_METHOD", "plain") }, want: "invalid GOOGLE_OAUTH_PKCE_METHOD"},
		{name: "redirect slash", mutate: func(s string) string { return replaceKey(s, "GOOGLE_OAUTH_REDIRECT_URI", "http://127.0.0.1/") }, want: "invalid GOOGLE_OAUTH_REDIRECT_URI"},
		{name: "docs url folder", mutate: func(s string) string {
			return replaceKey(s, "GOOGLE_DRIVE_FOLDER_ID", "https://docs.google.com/document/d/"+sampleFolderID+"/edit")
		}, want: "docs-cli: GOOGLE_DRIVE_FOLDER_ID is not a Drive folder URL or id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeMode(t, filepath.Join(dir, ".env.example"), tt.mutate(validEnv()), 0o644)
			_, err := Load(dir, nil)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err=%v, want substring %s", err, tt.want)
			}
		})
	}
}

func TestLoadProjectName(t *testing.T) {
	t.Setenv("APP_ENV", "staging")
	tests := []struct {
		name  string
		value string
		ok    bool
	}{
		{name: "words", value: "Docs CLI", ok: true},
		{name: "min", value: "Abcd", ok: true},
		{name: "max", value: strings.Repeat("A", 30), ok: true},
		{name: "too short", value: "A", ok: false},
		{name: "three", value: "Abc", ok: false},
		{name: "too long", value: strings.Repeat("A", 31), ok: false},
		{name: "quote", value: "Doc's CLI", ok: false},
		{name: "bang", value: "Docs!CLI", ok: false},
		{name: "cjk", value: "文件名稱", ok: false},
		{name: "dollar", value: "Docs$", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeMode(t, filepath.Join(dir, ".env.example"), replaceKey(validEnv(), "GCP_PROJECT_NAME", tt.value), 0o644)
			cfg, err := Load(dir, nil)
			if tt.ok {
				if err != nil {
					t.Fatal(err)
				}
				if cfg.ProjectName != tt.value {
					t.Fatalf("name=%q", cfg.ProjectName)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "GCP_PROJECT_NAME") {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestLoadFolderID(t *testing.T) {
	t.Setenv("APP_ENV", "staging")
	sharing := "https://drive.google.com/drive/folders/" + sampleFolderID + "?usp=sharing"
	dir := t.TempDir()
	writeMode(t, filepath.Join(dir, ".env.example"), replaceKey(validEnv(), "GOOGLE_DRIVE_FOLDER_ID", sharing), 0o644)
	cfg, err := Load(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DriveFolderID != sampleFolderID {
		t.Fatalf("DriveFolderID=%q", cfg.DriveFolderID)
	}

	dir = t.TempDir()
	writeMode(t, filepath.Join(dir, ".env.example"), replaceKey(validEnv(), "GOOGLE_DRIVE_FOLDER_ID", "   "), 0o644)
	cfg, err = Load(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DriveFolderID != "" {
		t.Fatalf("whitespace folder=%q", cfg.DriveFolderID)
	}

	dir = t.TempDir()
	writeMode(t, filepath.Join(dir, ".env.example"), deleteKey(validEnv(), "GOOGLE_DRIVE_FOLDER_ID"), 0o644)
	cfg, err = Load(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DriveFolderID != "" {
		t.Fatalf("missing folder=%q", cfg.DriveFolderID)
	}
}

func TestLoadAllowsEmptyGCPFolderID(t *testing.T) {
	t.Setenv("APP_ENV", "staging")
	dir := t.TempDir()
	body := replaceKey(validEnv(), "GCP_FOLDER_ID", "folders/123")
	body = replaceKey(body, "GCP_ORGANIZATION_ID", "")
	writeMode(t, filepath.Join(dir, ".env.example"), body, 0o644)
	cfg, err := Load(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GCPFolderID != "folders/123" || cfg.OrganizationID != "" {
		t.Fatalf("gcp=%q org=%q", cfg.GCPFolderID, cfg.OrganizationID)
	}

	dir = t.TempDir()
	writeMode(t, filepath.Join(dir, ".env.example"), validEnv(), 0o644)
	cfg, err = Load(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GCPFolderID != "" {
		t.Fatalf("empty gcp folder = %q", cfg.GCPFolderID)
	}
}

func TestLoadSelectsFileFromMode(t *testing.T) {
	t.Setenv("APP_ENV", "staging")
	dir := t.TempDir()
	example := replaceKey(validEnv(), "APP_ENV", "production")
	example = replaceKey(example, "DOCS_DEFAULT_TITLE", "FromExample")
	dotenv := replaceKey(validEnv(), "DOCS_DEFAULT_TITLE", "FromDotEnv")
	writeMode(t, filepath.Join(dir, ".env.example"), example, 0o644)
	writeMode(t, filepath.Join(dir, ".env"), dotenv, 0o600)

	cfg, err := Load(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != "development" || cfg.DefaultTitle != "FromExample" {
		t.Fatalf("development read mode=%s title=%q", cfg.Mode, cfg.DefaultTitle)
	}

	cfg, err = Load(dir, []string{"APP_ENV=production"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != "production" || cfg.DefaultTitle != "FromDotEnv" || cfg.FilePath != filepath.Join(dir, ".env") {
		t.Fatalf("production read mode=%s title=%q file=%s", cfg.Mode, cfg.DefaultTitle, cfg.FilePath)
	}
}

func TestLoadDevelopmentIgnoresDotEnv(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	dir := t.TempDir()
	writeMode(t, filepath.Join(dir, ".env.example"), validEnv(), 0o644)
	writeMode(t, filepath.Join(dir, ".env"), "this is not valid env\n", 0o600)
	cfg, err := Load(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != "development" || cfg.DefaultTitle != "Untitled" {
		t.Fatalf("mode=%s title=%q", cfg.Mode, cfg.DefaultTitle)
	}
}

func TestLoadProductionMissingEnv(t *testing.T) {
	t.Setenv("APP_ENV", "staging")
	dir := t.TempDir()
	writeMode(t, filepath.Join(dir, ".env.example"), validEnv(), 0o644)
	_, err := Load(dir, []string{"APP_ENV=production"})
	if err == nil || err.Error() != "docs-cli: missing .env" {
		t.Fatalf("err=%v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".env")); !os.IsNotExist(statErr) {
		t.Fatalf(".env created or stat failed: %v", statErr)
	}
}

func TestLoadTightensPerm(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix file mode")
	}
	t.Setenv("APP_ENV", "staging")
	for _, mode := range []os.FileMode{0o644, 0o640, 0o066} {
		t.Run(mode.String(), func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, ".env")
			body := validEnv()
			writeMode(t, path, body, mode)
			cfg, err := Load(dir, []string{"APP_ENV=production"})
			if err != nil {
				t.Fatal(err)
			}
			if cfg.ClientID != "" {
				t.Fatalf("client id %q", cfg.ClientID)
			}
			if got := filePerm(t, path); got != 0o600 {
				t.Fatalf("perm=%o, want 600", got)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, []byte(body)) {
				t.Fatal("content changed")
			}
		})
	}

	t.Run("invalid content still tightened", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, ".env")
		writeMode(t, path, replaceKey(validEnv(), "DOCS_INSERT_INDEX", "0"), 0o644)
		_, err := Load(dir, []string{"APP_ENV=production"})
		if err == nil {
			t.Fatal("expected invalid insert index")
		}
		if got := filePerm(t, path); got != 0o600 {
			t.Fatalf("perm=%o, want 600", got)
		}
	})

	t.Run("example stays loose", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, ".env.example")
		writeMode(t, path, validEnv(), 0o666)
		if _, err := Load(dir, nil); err != nil {
			t.Fatal(err)
		}
		if got := filePerm(t, path); got != 0o666 {
			t.Fatalf("example perm=%o, want 666", got)
		}
	})
}

func TestLoadRejectsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink mode")
	}
	t.Setenv("APP_ENV", "staging")
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	writeMode(t, target, validEnv(), 0o644)
	link := filepath.Join(dir, ".env")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	_, err := Load(dir, []string{"APP_ENV=production"})
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("err=%v", err)
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink was replaced")
	}
	if got := filePerm(t, target); got != 0o644 {
		t.Fatalf("target perm=%o, want 644", got)
	}
}

func TestLoadIgnoresTokenFileKeys(t *testing.T) {
	t.Setenv("APP_ENV", "staging")
	dir := t.TempDir()
	body := validEnv() + "GOOGLE_TOKEN_PATH=token.json\nGOOGLE_TOKEN_FILE_MODE=0777\nFOO=bar\n"
	writeMode(t, filepath.Join(dir, ".env.example"), body, 0o644)
	cfg, err := Load(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultTitle != "Untitled" || cfg.Scope != driveScope {
		t.Fatalf("title=%q scope=%s", cfg.DefaultTitle, cfg.Scope)
	}
}

func TestLoadCredentialsSubdir(t *testing.T) {
	t.Setenv("APP_ENV", "staging")
	dir := t.TempDir()
	writeMode(t, filepath.Join(dir, ".env.example"), replaceKey(validEnv(), "GOOGLE_CREDENTIALS_PATH", "subdir/credentials.json"), 0o644)
	cfg, err := Load(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(abs, "subdir", "credentials.json")
	if cfg.CredentialsPath != want {
		t.Fatalf("path=%s, want %s", cfg.CredentialsPath, want)
	}
}

func TestLoadTimeoutBoundary(t *testing.T) {
	t.Setenv("APP_ENV", "staging")
	dir := t.TempDir()
	writeMode(t, filepath.Join(dir, ".env.example"), replaceKey(validEnv(), "OAUTH_TIMEOUT", "15m"), 0o644)
	cfg, err := Load(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OAuthTimeout != 15*time.Minute {
		t.Fatalf("timeout=%s", cfg.OAuthTimeout)
	}
}

func TestLoadClientID(t *testing.T) {
	t.Setenv("APP_ENV", "staging")
	dir := t.TempDir()
	body := replaceKey(validEnv(), "GOOGLE_OAUTH_CLIENT_ID", "abc.apps.googleusercontent.com")
	body = replaceKey(body, "GOOGLE_OAUTH_CLIENT_SECRET", "secret-value")
	writeMode(t, filepath.Join(dir, ".env"), body, 0o600)
	cfg, err := Load(dir, []string{"APP_ENV=production"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ClientID != "abc.apps.googleusercontent.com" || cfg.ClientSecret != "secret-value" {
		t.Fatalf("id=%q secret=%q", cfg.ClientID, cfg.ClientSecret)
	}
}

func validEnv() string {
	return `APP_ENV=development
GCP_PROJECT_ID=docs-cli-dev
GCP_PROJECT_NAME=Docs CLI
GCP_BILLING_ACCOUNT=
GCP_ORGANIZATION_ID=
GCP_FOLDER_ID=
GOOGLE_DOCS_API=docs.googleapis.com
GOOGLE_DRIVE_API=drive.googleapis.com
GOOGLE_OAUTH_APP_NAME=Docs CLI
GOOGLE_OAUTH_SUPPORT_EMAIL=dev@example.com
GOOGLE_OAUTH_USER_TYPE=external
GOOGLE_OAUTH_PUBLISHING_STATUS=testing
GOOGLE_OAUTH_TEST_USER=dev@example.com
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

func replaceKey(content, key, value string) string {
	prefix := key + "="
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, prefix) {
			lines[i] = prefix + value
			return strings.Join(lines, "\n")
		}
	}
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return content + prefix + value + "\n"
}

func deleteKey(content, key string) string {
	prefix := key + "="
	var lines []string
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, prefix) {
			continue
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func writeMode(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func TestLoadIgnoresProjectReadyMarker(t *testing.T) {
	t.Setenv("APP_ENV", "staging")
	dir := t.TempDir()
	writeMode(t, filepath.Join(dir, ".env.example"), validEnv(), 0o644)
	if _, err := Load(dir, nil); err != nil {
		t.Fatal(err)
	}
	for _, extra := range []string{
		"DOCS_CLI_PROJECT_READY=other-project\n",
		"DOCS_CLI_PROJECT_READY=\n",
		"DOCS_CLI_BOOTSTRAP_FAILED=1\n",
	} {
		writeMode(t, filepath.Join(dir, ".env.example"), validEnv()+extra, 0o644)
		cfg, err := Load(dir, nil)
		if err != nil {
			t.Fatalf("extra %q: %v", extra, err)
		}
		if cfg.ProjectID != "docs-cli-dev" {
			t.Fatalf("project=%s", cfg.ProjectID)
		}
	}
}

func TestCommittedExampleStaysBlank(t *testing.T) {
	body, err := os.ReadFile(filepath.Join(repoRoot(t), ".env.example"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if strings.Contains(text, "DOCS_CLI_PROJECT_READY") || strings.Contains(text, "DOCS_CLI_BOOTSTRAP_FAILED") {
		t.Fatalf("example contains a wizard marker:\n%s", text)
	}
	vals, err := parseEnv(".env.example", body)
	if err != nil {
		t.Fatal(err)
	}
	model, modelOK := vals["GEMINI_MODEL"]
	grok, grokOK := vals["GROK_MODEL"]
	if vals["GOOGLE_OAUTH_CLIENT_ID"] != "" || vals["GOOGLE_OAUTH_CLIENT_SECRET"] != "" || vals["GOOGLE_DRIVE_FOLDER_ID"] != "" || !modelOK || model != "" || !grokOK || grok != "" {
		t.Fatalf("client=%q secret=%q folder=%q model=%q present=%v grok=%q grokPresent=%v", vals["GOOGLE_OAUTH_CLIENT_ID"], vals["GOOGLE_OAUTH_CLIENT_SECRET"], vals["GOOGLE_DRIVE_FOLDER_ID"], model, modelOK, grok, grokOK)
	}
}

func filePerm(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}
