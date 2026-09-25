package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode"
)

// Load reads root/.env.example in development and root/.env in production.
// The file is chosen before it is parsed. Load does not copy, prompt, or write
// credentials. A missing production file returns an error containing "missing .env".
func Load(root string, environ []string) (*Config, error) {
	mode, err := ResolveMode(environ)
	if err != nil {
		return nil, err
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("docs-cli: root: %w", err)
	}
	name := ".env.example"
	tighten := false
	if mode == modeProduction {
		name = ".env"
		tighten = true
	}
	path := filepath.Join(absRoot, name)
	if err := prepareEnvFile(path, tighten); err != nil {
		if os.IsNotExist(err) {
			if mode == modeProduction {
				return nil, errors.New("docs-cli: missing .env")
			}
			return nil, fmt.Errorf("docs-cli: missing .env.example in %s", absRoot)
		}
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("docs-cli: read %s: %w", path, err)
	}
	vals, err := parseEnv(path, data)
	if err != nil {
		return nil, err
	}
	return validate(absRoot, path, mode, vals)
}

func prepareEnvFile(path string, tighten bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("docs-cli: refusing symlink %s", path)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("docs-cli: %s is not a regular file", filepath.Base(path))
	}
	if tighten && runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		if err := os.Chmod(path, 0o600); err != nil {
			return fmt.Errorf("docs-cli: chmod %s: %w", path, err)
		}
	}
	return nil
}

// parseEnv follows bootstrap load_env. A line without '=' is an error.
// Only one trailing CR is removed.
func parseEnv(path string, data []byte) (map[string]string, error) {
	vals := make(map[string]string)
	for _, line := range splitLines(string(data)) {
		line = trimLeadingSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("docs-cli: invalid env line in %s: missing =", path)
		}
		if !envKeyPattern.MatchString(key) {
			return nil, fmt.Errorf("docs-cli: invalid env key in %s: %s", path, key)
		}
		vals[key] = unquoteEnv(val)
	}
	return vals, nil
}

func splitLines(data string) []string {
	var lines []string
	for data != "" {
		var line string
		if i := strings.IndexByte(data, '\n'); i >= 0 {
			line = data[:i]
			data = data[i+1:]
		} else {
			line = data
			data = ""
		}
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		lines = append(lines, line)
	}
	return lines
}

func trimLeadingSpace(s string) string {
	i := 0
	for i < len(s) {
		switch s[i] {
		case ' ', '\t', '\v', '\f':
			i++
		default:
			return s[i:]
		}
	}
	return ""
}

func unquoteEnv(val string) string {
	if len(val) >= 2 {
		last := val[len(val)-1]
		if (val[0] == '"' && last == '"') || (val[0] == '\'' && last == '\'') {
			return val[1 : len(val)-1]
		}
	}
	return val
}

func validate(root, path, mode string, vals map[string]string) (*Config, error) {
	projectID, err := nonEmpty(vals, "GCP_PROJECT_ID")
	if err != nil {
		return nil, err
	}
	if !projectIDPattern.MatchString(projectID) {
		return nil, errors.New("docs-cli: invalid GCP_PROJECT_ID")
	}
	projectName, err := nonEmpty(vals, "GCP_PROJECT_NAME")
	if err != nil {
		return nil, err
	}
	if !projectNamePattern.MatchString(projectName) {
		return nil, errors.New("docs-cli: invalid GCP_PROJECT_NAME")
	}
	docsAPI, err := nonEmpty(vals, "GOOGLE_DOCS_API")
	if err != nil {
		return nil, err
	}
	if docsAPI != docsAPIHost {
		return nil, errors.New("docs-cli: invalid GOOGLE_DOCS_API")
	}
	driveAPI, err := nonEmpty(vals, "GOOGLE_DRIVE_API")
	if err != nil {
		return nil, err
	}
	if driveAPI != driveAPIHost {
		return nil, errors.New("docs-cli: invalid GOOGLE_DRIVE_API")
	}
	appName, err := nonEmpty(vals, "GOOGLE_OAUTH_APP_NAME")
	if err != nil {
		return nil, err
	}
	supportEmail, err := nonEmpty(vals, "GOOGLE_OAUTH_SUPPORT_EMAIL")
	if err != nil {
		return nil, err
	}
	if err := validateEmail("GOOGLE_OAUTH_SUPPORT_EMAIL", supportEmail); err != nil {
		return nil, err
	}
	userType, err := nonEmpty(vals, "GOOGLE_OAUTH_USER_TYPE")
	if err != nil {
		return nil, err
	}
	if userType != userTypeExternal {
		return nil, errors.New("docs-cli: invalid GOOGLE_OAUTH_USER_TYPE")
	}
	publishing, err := nonEmpty(vals, "GOOGLE_OAUTH_PUBLISHING_STATUS")
	if err != nil {
		return nil, err
	}
	if publishing != publishingTesting {
		return nil, errors.New("docs-cli: invalid GOOGLE_OAUTH_PUBLISHING_STATUS")
	}
	testUser, err := nonEmpty(vals, "GOOGLE_OAUTH_TEST_USER")
	if err != nil {
		return nil, err
	}
	if err := validateEmail("GOOGLE_OAUTH_TEST_USER", testUser); err != nil {
		return nil, err
	}
	clientName, err := nonEmpty(vals, "GOOGLE_OAUTH_CLIENT_NAME")
	if err != nil {
		return nil, err
	}
	clientType, err := nonEmpty(vals, "GOOGLE_OAUTH_CLIENT_TYPE")
	if err != nil {
		return nil, err
	}
	if clientType != clientTypeInstalled {
		return nil, errors.New("docs-cli: invalid GOOGLE_OAUTH_CLIENT_TYPE")
	}
	authURI, err := nonEmpty(vals, "GOOGLE_OAUTH_AUTH_URI")
	if err != nil {
		return nil, err
	}
	if authURI != authURIValue {
		return nil, errors.New("docs-cli: invalid GOOGLE_OAUTH_AUTH_URI")
	}
	tokenURI, err := nonEmpty(vals, "GOOGLE_OAUTH_TOKEN_URI")
	if err != nil {
		return nil, err
	}
	if tokenURI != tokenURIValue {
		return nil, errors.New("docs-cli: invalid GOOGLE_OAUTH_TOKEN_URI")
	}
	certURL, err := nonEmpty(vals, "GOOGLE_OAUTH_CERT_URL")
	if err != nil {
		return nil, err
	}
	if certURL != certURLValue {
		return nil, errors.New("docs-cli: invalid GOOGLE_OAUTH_CERT_URL")
	}
	redirectURI, err := nonEmpty(vals, "GOOGLE_OAUTH_REDIRECT_URI")
	if err != nil {
		return nil, err
	}
	if redirectURI != redirectURIValue {
		return nil, errors.New("docs-cli: invalid GOOGLE_OAUTH_REDIRECT_URI")
	}
	scope, err := nonEmpty(vals, "GOOGLE_OAUTH_SCOPE")
	if err != nil {
		return nil, err
	}
	if strings.Contains(scope, "readonly") || scope != driveScope {
		return nil, errors.New("docs-cli: invalid GOOGLE_OAUTH_SCOPE")
	}
	accessType, err := nonEmpty(vals, "GOOGLE_OAUTH_ACCESS_TYPE")
	if err != nil {
		return nil, err
	}
	if accessType != accessTypeOffline {
		return nil, errors.New("docs-cli: invalid GOOGLE_OAUTH_ACCESS_TYPE")
	}
	prompt, err := nonEmpty(vals, "GOOGLE_OAUTH_PROMPT")
	if err != nil {
		return nil, err
	}
	if prompt != promptConsent {
		return nil, errors.New("docs-cli: invalid GOOGLE_OAUTH_PROMPT")
	}
	pkce, err := nonEmpty(vals, "GOOGLE_OAUTH_PKCE_METHOD")
	if err != nil {
		return nil, err
	}
	if pkce != pkceS256 {
		return nil, errors.New("docs-cli: invalid GOOGLE_OAUTH_PKCE_METHOD")
	}
	timeoutRaw, err := nonEmpty(vals, "OAUTH_TIMEOUT")
	if err != nil {
		return nil, err
	}
	timeout, err := time.ParseDuration(timeoutRaw)
	if err != nil || timeout <= 0 || timeout > 15*time.Minute {
		return nil, errors.New("docs-cli: invalid OAUTH_TIMEOUT")
	}
	credRaw, err := nonEmpty(vals, "GOOGLE_CREDENTIALS_PATH")
	if err != nil {
		return nil, err
	}
	credPath, err := credentialsAbs(root, credRaw)
	if err != nil {
		return nil, err
	}
	indexRaw, err := nonEmpty(vals, "DOCS_INSERT_INDEX")
	if err != nil {
		return nil, err
	}
	if indexRaw != "1" {
		return nil, errors.New("docs-cli: invalid DOCS_INSERT_INDEX")
	}
	title, err := nonEmpty(vals, "DOCS_DEFAULT_TITLE")
	if err != nil {
		return nil, err
	}
	if vals["GCP_ORGANIZATION_ID"] != "" && vals["GCP_FOLDER_ID"] != "" {
		return nil, errors.New("docs-cli: set only one of GCP_ORGANIZATION_ID or GCP_FOLDER_ID")
	}
	clientID := vals["GOOGLE_OAUTH_CLIENT_ID"]
	if clientID != "" && !clientIDPattern.MatchString(clientID) {
		return nil, errors.New("docs-cli: invalid GOOGLE_OAUTH_CLIENT_ID")
	}
	var driveFolder string
	if strings.TrimSpace(vals["GOOGLE_DRIVE_FOLDER_ID"]) != "" {
		driveFolder, err = NormalizeFolderID(vals["GOOGLE_DRIVE_FOLDER_ID"])
		if err != nil {
			return nil, err
		}
	}
	return &Config{
		Mode:             mode,
		Root:             root,
		FilePath:         path,
		DriveFolderID:    driveFolder,
		ProjectID:        projectID,
		ProjectName:      projectName,
		BillingAccount:   vals["GCP_BILLING_ACCOUNT"],
		OrganizationID:   vals["GCP_ORGANIZATION_ID"],
		GCPFolderID:      vals["GCP_FOLDER_ID"],
		DocsAPI:          docsAPI,
		DriveAPI:         driveAPI,
		AppName:          appName,
		SupportEmail:     supportEmail,
		UserType:         userType,
		PublishingStatus: publishing,
		TestUser:         testUser,
		ClientName:       clientName,
		ClientType:       clientType,
		ClientID:         clientID,
		ClientSecret:     vals["GOOGLE_OAUTH_CLIENT_SECRET"],
		AuthURI:          authURI,
		TokenURI:         tokenURI,
		CertURL:          certURL,
		RedirectURI:      redirectURI,
		Scope:            scope,
		AccessType:       accessType,
		Prompt:           prompt,
		PKCEMethod:       pkce,
		OAuthTimeout:     timeout,
		CredentialsPath:  credPath,
		InsertIndex:      1,
		DefaultTitle:     title,
	}, nil
}

func nonEmpty(vals map[string]string, key string) (string, error) {
	value, ok := vals[key]
	if !ok || value == "" {
		return "", fmt.Errorf("docs-cli: missing %s", key)
	}
	return value, nil
}

func validateEmail(key, value string) error {
	if strings.ContainsFunc(value, unicode.IsSpace) || !strings.Contains(value, "@") || strings.HasPrefix(value, "@") || strings.HasSuffix(value, "@") || value == placeholderEmail {
		return fmt.Errorf("docs-cli: invalid %s", key)
	}
	return nil
}

func credentialsAbs(root, raw string) (string, error) {
	if raw == "" {
		return "", errors.New("docs-cli: missing GOOGLE_CREDENTIALS_PATH")
	}
	if filepath.IsAbs(raw) {
		return "", errors.New("docs-cli: invalid GOOGLE_CREDENTIALS_PATH")
	}
	for _, part := range strings.Split(filepath.ToSlash(raw), "/") {
		if part == ".." {
			return "", errors.New("docs-cli: invalid GOOGLE_CREDENTIALS_PATH")
		}
	}
	cleaned := filepath.Clean(raw)
	if filepath.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", errors.New("docs-cli: invalid GOOGLE_CREDENTIALS_PATH")
	}
	full := filepath.Join(root, cleaned)
	rel, err := filepath.Rel(root, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("docs-cli: invalid GOOGLE_CREDENTIALS_PATH")
	}
	return full, nil
}
