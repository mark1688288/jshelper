package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// ErrDevelopmentWizard is returned when setup is asked to copy or prompt
// outside production.
var ErrDevelopmentWizard = errors.New("docs-cli: setup refused: development reads .env.example and does not copy or prompt")

// ErrDesktopClientRequired means bootstrap finished and this call must stop
// before asking for a client id. The text has no docs-cli: prefix.
var ErrDesktopClientRequired = errors.New("建立 Desktop app「Docs CLI Desktop」後再執行 ./docs-cli setup")

const (
	projectReadyKey    = "DOCS_CLI_PROJECT_READY"
	bootstrapFailedKey = "DOCS_CLI_BOOTSTRAP_FAILED"

	projectNotReady              = "docs-cli: 專案尚未標成就緒。若 GCP_PROJECT_ID 已被佔用或是保留字，請改 .env 的 GCP_PROJECT_ID，再執行 ./docs-cli setup。"
	invalidClientBeforeBootstrap = "docs-cli: invalid GOOGLE_OAUTH_CLIENT_ID\ndocs-cli: 清掉 .env 的 GOOGLE_OAUTH_CLIENT_ID，或改成這個專案的 Desktop client id，再執行 ./docs-cli setup。"
	staleMarkerCleared           = "已清掉 client id、secret 與專案就緒標記：GCP_PROJECT_ID 與標記不一致，Desktop client 留在舊專案。"
)

// wizardStderr receives the stale-marker notice. Bootstrap output uses the
// same writer so Console URLs stay on the process stderr stream.
var wizardStderr io.Writer = os.Stderr

// copyEnvData, chmodEnv, and beforeEnvRename are replaced by tests.
var (
	copyEnvData = func(dst io.Writer, src io.Reader) error {
		_, err := io.Copy(dst, src)
		return err
	}
	chmodEnv        = os.Chmod
	beforeEnvRename = func(string) error { return nil }
)

var plainEnvValue = regexp.MustCompile(`^[A-Za-z0-9_./:@+-]+$`)

var phase1Keys = []string{
	"GCP_PROJECT_ID",
	"GCP_PROJECT_NAME",
	"GOOGLE_OAUTH_SUPPORT_EMAIL",
	"GOOGLE_OAUTH_TEST_USER",
	"GOOGLE_DRIVE_FOLDER_ID",
}

var phase2Keys = []string{
	"GOOGLE_OAUTH_CLIENT_ID",
	"GOOGLE_OAUTH_CLIENT_SECRET",
}

// EnsureProductionEnv copies .env.example to .env when needed and asks for
// wizard fields that are still invalid. It does not run in development.
func EnsureProductionEnv(root string, environ []string, tty TTY) (*Config, error) {
	mode, err := ResolveMode(environ)
	if err != nil {
		return nil, err
	}
	if mode != modeProduction {
		return nil, ErrDevelopmentWizard
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("docs-cli: root: %w", err)
	}
	created, err := copyExample(absRoot)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(absRoot, ".env")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("docs-cli: read %s: %w", path, err)
	}
	vals, err := parseEnv(path, data)
	if err != nil {
		return nil, err
	}
	if key := fixedKeyProblem(absRoot, vals); key != "" {
		return nil, fmt.Errorf("docs-cli: invalid %s in .env; this program will not replace an existing .env", key)
	}
	content := string(data)
	content, err = clearStaleProjectMarker(absRoot, content, vals)
	if err != nil {
		return nil, err
	}
	keys := phase1KeysToAsk(vals, created)
	if len(keys) > 0 && !terminalReady(tty) {
		return nil, ErrNeedTerminal
	}
	for _, key := range keys {
		prev := vals[key]
		value, err := askWizard(tty, key, prev)
		if err != nil {
			return nil, err
		}
		content = upsertEnvKey(content, key, value)
		if key == "GCP_PROJECT_ID" && value != prev {
			content = clearClientAndMarker(content)
			vals["GOOGLE_OAUTH_CLIENT_ID"] = ""
			vals["GOOGLE_OAUTH_CLIENT_SECRET"] = ""
			vals[projectReadyKey] = ""
		}
		if err := writeEnvAtomic(absRoot, content); err != nil {
			return nil, err
		}
		vals[key] = value
	}
	content, vals, err = readEnvContent(path)
	if err != nil {
		return nil, err
	}
	if !phase1Ready(vals) {
		return nil, fmt.Errorf("docs-cli: invalid %s", firstBadPhase1(vals))
	}
	if !markerBound(vals) {
		if id := vals["GOOGLE_OAUTH_CLIENT_ID"]; id != "" && !clientIDPattern.MatchString(id) {
			return nil, errors.New(invalidClientBeforeBootstrap)
		}
		if err := runProjectBootstrap(absRoot, environ, noticeWriter()); err != nil {
			wrapped := ensureBootstrapFailed(err)
			if werr := writeBootstrapFailure(absRoot, path); werr != nil {
				return nil, fmt.Errorf("%s\n%w\n%v", projectNotReady, wrapped, werr)
			}
			return nil, fmt.Errorf("%s\n%w", projectNotReady, wrapped)
		}
		if err := writeBootstrapSuccess(absRoot, path); err != nil {
			return nil, err
		}
		return nil, ErrDesktopClientRequired
	}
	keys = phase2KeysToAsk(vals)
	if len(keys) > 0 && !terminalReady(tty) {
		return nil, ErrNeedTerminal
	}
	for _, key := range keys {
		value, err := askWizard(tty, key, vals[key])
		if err != nil {
			return nil, err
		}
		content = upsertEnvKey(content, key, value)
		if err := writeEnvAtomic(absRoot, content); err != nil {
			return nil, err
		}
		vals[key] = value
		if key == "GOOGLE_OAUTH_CLIENT_SECRET" {
			writeStatus(tty, "已寫入 GOOGLE_OAUTH_CLIENT_SECRET")
		}
	}
	_, vals, err = readEnvContent(path)
	if err != nil {
		return nil, err
	}
	cfg, err := validate(absRoot, path, mode, vals)
	if err != nil {
		return nil, err
	}
	if cfg.DriveFolderID == "" {
		return nil, errFolderRequired
	}
	if cfg.ClientID == "" {
		return nil, errors.New("docs-cli: invalid GOOGLE_OAUTH_CLIENT_ID")
	}
	if strings.TrimSpace(cfg.ClientSecret) == "" {
		return nil, errors.New("docs-cli: invalid GOOGLE_OAUTH_CLIENT_SECRET")
	}
	return cfg, nil
}

func modeLine(mode, file string) string {
	return "docs-cli: mode=" + mode + " file=" + file
}

func phase1KeysToAsk(vals map[string]string, created bool) []string {
	failed := vals[bootstrapFailedKey] == "1"
	bound := markerBound(vals)
	var keys []string
	for _, key := range phase1Keys {
		force := false
		switch key {
		case "GCP_PROJECT_ID":
			force = created || (failed && !bound)
		case "GCP_PROJECT_NAME":
			force = created
		}
		if force || !wizardValueOK(key, vals[key]) {
			keys = append(keys, key)
		}
	}
	return keys
}

func phase2KeysToAsk(vals map[string]string) []string {
	var keys []string
	for _, key := range phase2Keys {
		if !wizardValueOK(key, vals[key]) {
			keys = append(keys, key)
		}
	}
	return keys
}

func markerBound(vals map[string]string) bool {
	id := vals["GCP_PROJECT_ID"]
	return id != "" && vals[projectReadyKey] == id
}

func phase1Ready(vals map[string]string) bool {
	for _, key := range phase1Keys {
		if !wizardValueOK(key, vals[key]) {
			return false
		}
	}
	return true
}

func firstBadPhase1(vals map[string]string) string {
	for _, key := range phase1Keys {
		if !wizardValueOK(key, vals[key]) {
			return key
		}
	}
	return "GCP_PROJECT_ID"
}

func terminalReady(tty TTY) bool {
	return tty != nil && tty.IsTerminal()
}

func noticeWriter() io.Writer {
	if wizardStderr == nil {
		return os.Stderr
	}
	return wizardStderr
}

func readEnvContent(path string) (string, map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", nil, fmt.Errorf("docs-cli: read %s: %w", path, err)
	}
	vals, err := parseEnv(path, data)
	if err != nil {
		return "", nil, err
	}
	return string(data), vals, nil
}

func clearClientAndMarker(content string) string {
	content = upsertEnvKey(content, "GOOGLE_OAUTH_CLIENT_ID", "")
	content = upsertEnvKey(content, "GOOGLE_OAUTH_CLIENT_SECRET", "")
	return upsertEnvKey(content, projectReadyKey, "")
}

func clearStaleProjectMarker(root, content string, vals map[string]string) (string, error) {
	ready := vals[projectReadyKey]
	if ready == "" || ready == vals["GCP_PROJECT_ID"] {
		return content, nil
	}
	content = clearClientAndMarker(content)
	if err := writeEnvAtomic(root, content); err != nil {
		return "", err
	}
	vals["GOOGLE_OAUTH_CLIENT_ID"] = ""
	vals["GOOGLE_OAUTH_CLIENT_SECRET"] = ""
	vals[projectReadyKey] = ""
	fmt.Fprintln(noticeWriter(), staleMarkerCleared)
	return content, nil
}

func writeBootstrapSuccess(root, path string) error {
	content, vals, err := readEnvContent(path)
	if err != nil {
		return err
	}
	content = upsertEnvKey(content, projectReadyKey, vals["GCP_PROJECT_ID"])
	content = upsertEnvKey(content, bootstrapFailedKey, "")
	return writeEnvAtomic(root, content)
}

func writeBootstrapFailure(root, path string) error {
	content, _, err := readEnvContent(path)
	if err != nil {
		return err
	}
	content = upsertEnvKey(content, bootstrapFailedKey, "1")
	return writeEnvAtomic(root, content)
}

func ensureBootstrapFailed(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "docs-cli: bootstrap failed:") {
		return err
	}
	return fmt.Errorf("docs-cli: bootstrap failed: %w", err)
}

func wizardValueOK(key, value string) bool {
	_, _, _, ok := acceptWizard(key, value, "")
	return ok
}

func askWizard(tty TTY, key, current string) (string, error) {
	for attempt := 1; attempt <= 3; attempt++ {
		prompt := promptFor(key, current)
		var (
			input string
			err   error
		)
		if key == "GOOGLE_OAUTH_CLIENT_SECRET" {
			input, err = tty.ReadSecret(prompt)
		} else {
			input, err = tty.ReadLine(prompt)
		}
		if err != nil {
			return "", err
		}
		value, reason, fatal, ok := acceptWizard(key, input, current)
		if ok {
			return value, nil
		}
		writeStatus(tty, "無效的值："+reason)
		if attempt == 3 {
			if fatal != nil {
				return "", fatal
			}
			return "", fmt.Errorf("docs-cli: invalid %s", key)
		}
	}
	return "", fmt.Errorf("docs-cli: invalid %s", key)
}

func acceptWizard(key, input, current string) (value, reason string, fatal error, ok bool) {
	if strings.TrimSpace(input) == "" {
		value = current
	} else {
		value = strings.TrimSpace(input)
	}
	if reason, fatal, bad := forbiddenValue(key, value); bad {
		return "", reason, fatal, false
	}
	switch key {
	case "GCP_PROJECT_ID":
		if projectIDPattern.MatchString(value) {
			return value, "", nil, true
		}
		return "", "project id 格式不符", nil, false
	case "GCP_PROJECT_NAME":
		if projectNamePattern.MatchString(value) {
			return value, "", nil, true
		}
		return "", "project name 必須是 4 到 30 個字元且只含英數、空白與連字號", nil, false
	case "GOOGLE_OAUTH_SUPPORT_EMAIL", "GOOGLE_OAUTH_TEST_USER":
		if value == placeholderEmail {
			return "", "不可使用佔位 you@gmail.com", nil, false
		}
		if validateEmail(key, value) != nil {
			return "", "email 必須包含 @", nil, false
		}
		return value, "", nil, true
	case "GOOGLE_OAUTH_CLIENT_ID":
		if clientIDPattern.MatchString(value) {
			return value, "", nil, true
		}
		return "", "client id 必須以 .apps.googleusercontent.com 結尾", nil, false
	case "GOOGLE_OAUTH_CLIENT_SECRET":
		if value == "" {
			return "", "secret 不可空白", nil, false
		}
		return value, "", nil, true
	case "GOOGLE_DRIVE_FOLDER_ID":
		id, err := NormalizeFolderID(value)
		if err != nil {
			if errors.Is(err, errFolderRequired) {
				return "", "Drive 資料夾不可空白", nil, false
			}
			return "", "那不是 Drive 資料夾網址或 id", errFolderFormat, false
		}
		return id, "", nil, true
	default:
		return "", "project id 格式不符", nil, false
	}
}

func forbiddenValue(key, value string) (string, error, bool) {
	if !strings.ContainsAny(value, "\"'\n\r") && !strings.ContainsRune(value, 0) {
		return "", nil, false
	}
	switch key {
	case "GCP_PROJECT_ID":
		return "project id 格式不符", nil, true
	case "GCP_PROJECT_NAME":
		return "project name 必須是 4 到 30 個字元且只含英數、空白與連字號", nil, true
	case "GOOGLE_OAUTH_SUPPORT_EMAIL", "GOOGLE_OAUTH_TEST_USER":
		return "email 必須包含 @", nil, true
	case "GOOGLE_OAUTH_CLIENT_ID":
		return "client id 必須以 .apps.googleusercontent.com 結尾", nil, true
	case "GOOGLE_DRIVE_FOLDER_ID":
		return "那不是 Drive 資料夾網址或 id", errFolderFormat, true
	default:
		return "不可包含引號、換行或 NUL", nil, true
	}
}

func promptFor(key, current string) string {
	switch key {
	case "GCP_PROJECT_ID":
		return "GCP_PROJECT_ID [" + current + "]（直接按 Enter 保留；改掉會清掉 client id、secret 與專案就緒標記，Desktop client 留在舊專案）:"
	case "GCP_PROJECT_NAME":
		return "GCP_PROJECT_NAME [" + current + "]（直接按 Enter 保留預設）:"
	case "GOOGLE_OAUTH_SUPPORT_EMAIL":
		return "GOOGLE_OAUTH_SUPPORT_EMAIL（不可使用 you@gmail.com）:"
	case "GOOGLE_OAUTH_TEST_USER":
		return "GOOGLE_OAUTH_TEST_USER（不可使用 you@gmail.com）:"
	case "GOOGLE_OAUTH_CLIENT_ID":
		return "GOOGLE_OAUTH_CLIENT_ID:"
	case "GOOGLE_OAUTH_CLIENT_SECRET":
		return "GOOGLE_OAUTH_CLIENT_SECRET（輸入時不顯示）:"
	case "GOOGLE_DRIVE_FOLDER_ID":
		return "GOOGLE_DRIVE_FOLDER_ID（Drive 資料夾 id 或資料夾網址，不可留空；不是 GCP_FOLDER_ID）:"
	default:
		return key + ":"
	}
}

func writeStatus(tty TTY, line string) {
	if tty == nil {
		return
	}
	if w, ok := tty.(interface{ WriteStatus(string) error }); ok {
		_ = w.WriteStatus(line)
	}
}

func copyExample(root string) (bool, error) {
	srcPath := filepath.Join(root, ".env.example")
	dstPath := filepath.Join(root, ".env")
	srcInfo, err := os.Lstat(srcPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, fmt.Errorf("docs-cli: missing .env.example in %s", root)
		}
		return false, err
	}
	if srcInfo.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("docs-cli: refusing symlink %s", srcPath)
	}
	if !srcInfo.Mode().IsRegular() {
		return false, fmt.Errorf("docs-cli: .env.example is not a regular file")
	}
	dstInfo, err := os.Lstat(dstPath)
	if err == nil {
		if dstInfo.Mode()&os.ModeSymlink != 0 {
			return false, fmt.Errorf("docs-cli: refusing symlink %s", dstPath)
		}
		if !dstInfo.Mode().IsRegular() {
			return false, fmt.Errorf("docs-cli: .env is not a regular file")
		}
		if runtime.GOOS != "windows" && dstInfo.Mode().Perm() != 0o600 {
			if err := chmodEnv(dstPath, 0o600); err != nil {
				return false, fmt.Errorf("docs-cli: chmod %s: %w", dstPath, err)
			}
		}
		return false, nil
	}
	if !os.IsNotExist(err) {
		return false, err
	}
	src, err := os.Open(srcPath)
	if err != nil {
		return false, err
	}
	defer src.Close()
	dst, err := os.OpenFile(dstPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return false, fmt.Errorf("docs-cli: .env already exists")
		}
		return false, err
	}
	copyErr := copyEnvData(dst, src)
	closeErr := dst.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(dstPath)
		if copyErr != nil {
			return false, copyErr
		}
		return false, closeErr
	}
	if err := chmodEnv(dstPath, 0o600); err != nil {
		_ = os.Remove(dstPath)
		return false, err
	}
	return true, nil
}

func writeEnvAtomic(root, content string) error {
	return writeAtomic(root, filepath.Join(root, ".env"), ".env.*.tmp", []byte(content))
}

func writeAtomic(dir, dest, pattern string, data []byte) error {
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return err
	}
	tmp := f.Name()
	remove := true
	defer func() {
		if remove {
			_ = os.Remove(tmp)
		}
	}()
	if err := chmodEnv(tmp, 0o600); err != nil {
		_ = f.Close()
		return fmt.Errorf("docs-cli: chmod %s: %w", tmp, err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if filepath.Base(dest) == ".env" {
		if err := beforeEnvRename(dest); err != nil {
			return err
		}
	}
	info, err := os.Lstat(dest)
	if err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("docs-cli: refusing symlink %s", dest)
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(tmp, dest); err != nil {
		return err
	}
	remove = false
	if err := chmodEnv(dest, 0o600); err != nil {
		return fmt.Errorf("docs-cli: chmod %s: %w", dest, err)
	}
	return nil
}

func upsertEnvKey(content, key, value string) string {
	assignment := key + "=" + formatAssignedValue(value)
	if content == "" {
		return assignment + "\n"
	}
	endedNL := strings.HasSuffix(content, "\n")
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	found := false
	out := make([]string, 0, len(lines)+1)
	for _, line := range lines {
		logical := strings.TrimSuffix(line, "\r")
		trimmed := trimLeadingSpace(logical)
		parsed := strings.TrimPrefix(trimmed, "export ")
		name, _, ok := strings.Cut(parsed, "=")
		if ok && trimmed != "" && !strings.HasPrefix(trimmed, "#") && name == key {
			if !found {
				out = append(out, assignment)
				found = true
			}
			continue
		}
		out = append(out, line)
	}
	if !found {
		out = append(out, assignment)
	}
	result := strings.Join(out, "\n")
	if endedNL || !found {
		result += "\n"
	}
	return result
}

func formatAssignedValue(value string) string {
	if plainEnvValue.MatchString(value) {
		return value
	}
	return `"` + value + `"`
}

func fixedKeyProblem(root string, vals map[string]string) string {
	if vals["GOOGLE_DOCS_API"] != docsAPIHost {
		return "GOOGLE_DOCS_API"
	}
	if vals["GOOGLE_DRIVE_API"] != driveAPIHost {
		return "GOOGLE_DRIVE_API"
	}
	if vals["GOOGLE_OAUTH_APP_NAME"] == "" {
		return "GOOGLE_OAUTH_APP_NAME"
	}
	if vals["GOOGLE_OAUTH_USER_TYPE"] != userTypeExternal {
		return "GOOGLE_OAUTH_USER_TYPE"
	}
	if vals["GOOGLE_OAUTH_PUBLISHING_STATUS"] != publishingTesting {
		return "GOOGLE_OAUTH_PUBLISHING_STATUS"
	}
	if vals["GOOGLE_OAUTH_CLIENT_NAME"] == "" {
		return "GOOGLE_OAUTH_CLIENT_NAME"
	}
	if vals["GOOGLE_OAUTH_CLIENT_TYPE"] != clientTypeInstalled {
		return "GOOGLE_OAUTH_CLIENT_TYPE"
	}
	if vals["GOOGLE_OAUTH_AUTH_URI"] != authURIValue {
		return "GOOGLE_OAUTH_AUTH_URI"
	}
	if vals["GOOGLE_OAUTH_TOKEN_URI"] != tokenURIValue {
		return "GOOGLE_OAUTH_TOKEN_URI"
	}
	if vals["GOOGLE_OAUTH_CERT_URL"] != certURLValue {
		return "GOOGLE_OAUTH_CERT_URL"
	}
	if vals["GOOGLE_OAUTH_REDIRECT_URI"] != redirectURIValue {
		return "GOOGLE_OAUTH_REDIRECT_URI"
	}
	scope := vals["GOOGLE_OAUTH_SCOPE"]
	if scope == "" || strings.Contains(scope, "readonly") || scope != driveScope {
		return "GOOGLE_OAUTH_SCOPE"
	}
	if vals["GOOGLE_OAUTH_ACCESS_TYPE"] != accessTypeOffline {
		return "GOOGLE_OAUTH_ACCESS_TYPE"
	}
	if vals["GOOGLE_OAUTH_PROMPT"] != promptConsent {
		return "GOOGLE_OAUTH_PROMPT"
	}
	if vals["GOOGLE_OAUTH_PKCE_METHOD"] != pkceS256 {
		return "GOOGLE_OAUTH_PKCE_METHOD"
	}
	timeout, err := time.ParseDuration(vals["OAUTH_TIMEOUT"])
	if err != nil || timeout <= 0 || timeout > 15*time.Minute {
		return "OAUTH_TIMEOUT"
	}
	if _, err := credentialsAbs(root, vals["GOOGLE_CREDENTIALS_PATH"]); err != nil {
		return "GOOGLE_CREDENTIALS_PATH"
	}
	if vals["DOCS_INSERT_INDEX"] != "1" {
		return "DOCS_INSERT_INDEX"
	}
	if vals["DOCS_DEFAULT_TITLE"] == "" {
		return "DOCS_DEFAULT_TITLE"
	}
	if vals["GCP_ORGANIZATION_ID"] != "" && vals["GCP_FOLDER_ID"] != "" {
		return "GCP_ORGANIZATION_ID"
	}
	return ""
}
