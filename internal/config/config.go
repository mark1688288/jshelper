// Package config loads docs-cli environment files.
package config

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// defaultAppEnv is the mode used when process APP_ENV is unset.
// Release build:
//
//	-X github.com/mark1688288/jshelper/internal/config.defaultAppEnv=production
//
// Must remain a string var. -X cannot override a const.
var defaultAppEnv = "development"

// Config is the validated environment for one run.
type Config struct {
	// Mode is ResolveMode's result, not APP_ENV from the file.
	Mode     string
	Root     string
	FilePath string
	// DriveFolderID is empty or a normalized Drive folder id. It is never a URL.
	DriveFolderID string

	ProjectID      string
	ProjectName    string
	BillingAccount string
	OrganizationID string
	// GCPFolderID is the Cloud Resource Manager folder, not a Drive folder.
	GCPFolderID string

	DocsAPI  string
	DriveAPI string

	AppName          string
	SupportEmail     string
	UserType         string
	PublishingStatus string
	TestUser         string

	ClientName   string
	ClientType   string
	ClientID     string
	ClientSecret string
	AuthURI      string
	TokenURI     string
	CertURL      string
	RedirectURI  string
	Scope        string
	AccessType   string
	Prompt       string
	PKCEMethod   string
	OAuthTimeout time.Duration

	CredentialsPath string
	InsertIndex     int64
	DefaultTitle    string
}

const (
	modeDevelopment = "development"
	modeProduction  = "production"

	driveScope          = "https://www.googleapis.com/auth/drive"
	docsAPIHost         = "docs.googleapis.com"
	driveAPIHost        = "drive.googleapis.com"
	authURIValue        = "https://accounts.google.com/o/oauth2/auth"
	tokenURIValue       = "https://oauth2.googleapis.com/token"
	certURLValue        = "https://www.googleapis.com/oauth2/v1/certs"
	redirectURIValue    = "http://127.0.0.1"
	accessTypeOffline   = "offline"
	promptConsent       = "consent"
	pkceS256            = "S256"
	clientTypeInstalled = "installed"
	userTypeExternal    = "external"
	publishingTesting   = "testing"
	placeholderEmail    = "you@gmail.com"
)

var (
	errFolderRequired = errors.New("docs-cli: GOOGLE_DRIVE_FOLDER_ID is required")
	errFolderFormat   = errors.New("docs-cli: GOOGLE_DRIVE_FOLDER_ID is not a Drive folder URL or id")
	errMode           = errors.New("docs-cli: APP_ENV must be development or production")

	projectIDPattern   = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)
	projectNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 -]{2,28}[A-Za-z0-9]$`)
	clientIDPattern    = regexp.MustCompile(`^[A-Za-z0-9._-]+\.apps\.googleusercontent\.com$`)
	folderIDPattern    = regexp.MustCompile(`^[A-Za-z0-9_-]{10,128}$`)
	envKeyPattern      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// ResolveMode returns "development" or "production".
// A non-empty APP_ENV in environ wins over defaultAppEnv.
// environ is KEY=VALUE entries and is not read from the process environment.
func ResolveMode(environ []string) (string, error) {
	if value, ok := environLookup(environ, "APP_ENV"); ok && value != "" {
		if value != modeDevelopment && value != modeProduction {
			return "", errMode
		}
		return value, nil
	}
	switch defaultAppEnv {
	case modeDevelopment, modeProduction:
		return defaultAppEnv, nil
	default:
		return "", errMode
	}
}

func environLookup(environ []string, key string) (string, bool) {
	prefix := key + "="
	value := ""
	found := false
	for _, entry := range environ {
		if strings.HasPrefix(entry, prefix) {
			value = strings.TrimPrefix(entry, prefix)
			found = true
		}
	}
	return value, found
}

// NormalizeFolderID trims raw and returns a Drive folder id.
// An empty string is an error. Only https Drive folder URLs are accepted.
func NormalizeFolderID(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errFolderRequired
	}
	if folderIDPattern.MatchString(raw) {
		return raw, nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || !strings.EqualFold(u.Scheme, "https") {
		return "", errFolderFormat
	}
	if u.User != nil || u.Port() != "" || !strings.EqualFold(u.Hostname(), "drive.google.com") {
		return "", errFolderFormat
	}
	id, ok := driveFolderID(u.Path)
	if !ok {
		return "", errFolderFormat
	}
	return id, nil
}

func driveFolderID(urlPath string) (string, bool) {
	if len(urlPath) > 1 && strings.HasSuffix(urlPath, "/") {
		urlPath = strings.TrimSuffix(urlPath, "/")
	}
	const plain = "/drive/folders/"
	if rest, ok := strings.CutPrefix(urlPath, plain); ok {
		if folderIDPattern.MatchString(rest) {
			return rest, true
		}
		return "", false
	}
	const userPrefix = "/drive/u/"
	rest, ok := strings.CutPrefix(urlPath, userPrefix)
	if !ok {
		return "", false
	}
	n, after, ok := strings.Cut(rest, "/")
	if !ok || !digitsOnly(n) || !strings.HasPrefix(after, "folders/") {
		return "", false
	}
	id := strings.TrimPrefix(after, "folders/")
	if folderIDPattern.MatchString(id) {
		return id, true
	}
	return "", false
}

func digitsOnly(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
