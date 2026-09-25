package config

import (
	"encoding/json"
	"fmt"
	"path/filepath"
)

type installedFile struct {
	Installed installedClient `json:"installed"`
}

type installedClient struct {
	ClientID     string   `json:"client_id"`
	ProjectID    string   `json:"project_id"`
	AuthURI      string   `json:"auth_uri"`
	TokenURI     string   `json:"token_uri"`
	CertURL      string   `json:"auth_provider_x509_cert_url"`
	ClientSecret string   `json:"client_secret"`
	RedirectURIs []string `json:"redirect_uris"`
}

// RequireClient reports a missing client id or secret.
// The filename is the base name of the file Load or the wizard actually read.
func RequireClient(cfg *Config) error {
	name := ".env"
	if cfg != nil && cfg.FilePath != "" {
		name = filepath.Base(cfg.FilePath)
	}
	if cfg == nil || cfg.ClientID == "" {
		return fmt.Errorf("docs-cli: missing GOOGLE_OAUTH_CLIENT_ID in %s", name)
	}
	if cfg.ClientSecret == "" {
		return fmt.Errorf("docs-cli: missing GOOGLE_OAUTH_CLIENT_SECRET in %s", name)
	}
	return nil
}

// WriteCredentials writes an installed-app credentials file derived from cfg.
// An existing regular file is replaced. A symlink is refused. Empty client
// id or secret writes nothing.
func WriteCredentials(cfg *Config) error {
	if err := RequireClient(cfg); err != nil {
		return err
	}
	doc := installedFile{Installed: installedClient{
		ClientID:     cfg.ClientID,
		ProjectID:    cfg.ProjectID,
		AuthURI:      cfg.AuthURI,
		TokenURI:     cfg.TokenURI,
		CertURL:      cfg.CertURL,
		ClientSecret: cfg.ClientSecret,
		RedirectURIs: []string{cfg.RedirectURI},
	}}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("docs-cli: credentials: %w", err)
	}
	data = append(data, '\n')
	return writeAtomic(filepath.Dir(cfg.CredentialsPath), cfg.CredentialsPath, "credentials.json.*.tmp", data)
}
