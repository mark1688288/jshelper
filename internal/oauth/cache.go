package oauth

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zalando/go-keyring"
	"golang.org/x/oauth2"

	"github.com/mark1688288/jshelper/internal/config"
)

const (
	keyringService = "docs-cli"
	keyringUser    = "refresh-token"
)

var (
	errKeyringUnavailable = errors.New("docs-cli: keyring unavailable")
	errKeyringInvalid     = errors.New("docs-cli: keyring item is invalid; run docs-cli auth")
	errKeyringScope       = errors.New("docs-cli: keyring scope does not match; run docs-cli auth")
	errKeyringTooBig      = errors.New("docs-cli: keyring item exceeds the 4096 byte command limit")
	errTokenFileScope     = errors.New("docs-cli: token.json scope does not match; run docs-cli auth")
	errTokenFileDelete    = errors.New("docs-cli: refresh token is in the keyring; delete token.json")
)

type storedItem struct {
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
}

type legacyFile struct {
	Scope        string `json:"scope"`
	RefreshToken string `json:"refresh_token"`
	Token        struct {
		RefreshToken string `json:"refresh_token"`
		AccessToken  string `json:"access_token"`
	} `json:"token"`
}

// cachedToken returns a token that can be used without a browser.
// A nil token and a nil error means the caller must start the loopback flow.
// Force still checks that the keyring can be read. Any error other than
// ErrNotFound refuses the browser; the item itself does not.
func cachedToken(cfg *config.Config, force bool, stderr io.Writer) (*oauth2.Token, error) {
	raw, err := keyring.Get(keyringService, keyringUser)
	if err != nil {
		if !errors.Is(err, keyring.ErrNotFound) {
			return nil, errKeyringUnavailable
		}
		if force {
			return nil, nil
		}
		return migrateLegacy(cfg, stderr)
	}
	if force {
		return nil, nil
	}
	item, ok := parseStoredItem(raw)
	if !ok {
		return nil, errKeyringInvalid
	}
	if item.Scope != cfg.Scope {
		return nil, errKeyringScope
	}
	if err := deleteLegacyIfRefresh(cfg); err != nil {
		return nil, err
	}
	return memoryToken(item.RefreshToken), nil
}

func parseStoredItem(raw string) (storedItem, bool) {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	var item storedItem
	if err := dec.Decode(&item); err != nil {
		return storedItem{}, false
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return storedItem{}, false
	}
	if item.RefreshToken == "" || item.Scope == "" {
		return storedItem{}, false
	}
	return item, true
}

func migrateLegacy(cfg *config.Config, stderr io.Writer) (*oauth2.Token, error) {
	leg, found, err := readLegacy(cfg)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	refresh := legacyRefresh(leg)
	if refresh == "" {
		return nil, nil
	}
	if strings.TrimSpace(leg.Scope) == "" || leg.Scope != cfg.Scope {
		return nil, errTokenFileScope
	}
	// Only the old wrapper's nested refresh token is migrated.
	if leg.Token.RefreshToken == "" {
		return nil, nil
	}
	if err := storeItem(cfg.Scope, leg.Token.RefreshToken); err != nil {
		return nil, err
	}
	fmt.Fprintln(stderr, "docs-cli: stored refresh token in keyring")
	if err := deleteLegacyIfRefresh(cfg); err != nil {
		return nil, err
	}
	return memoryToken(leg.Token.RefreshToken), nil
}

func legacyRefresh(leg legacyFile) string {
	if leg.Token.RefreshToken != "" {
		return leg.Token.RefreshToken
	}
	return leg.RefreshToken
}

func memoryToken(refresh string) *oauth2.Token {
	// A zero Expiry makes oauth2 reuse this token and never refresh it.
	return &oauth2.Token{
		RefreshToken: refresh,
		TokenType:    "Bearer",
		Expiry:       time.Unix(0, 0),
	}
}

func storeItem(scope, refresh string) error {
	if refresh == "" {
		return errors.New("docs-cli: error: empty refresh token")
	}
	payload, err := json.Marshal(storedItem{RefreshToken: refresh, Scope: scope})
	if err != nil {
		return fmt.Errorf("docs-cli: error: %s", mask(err.Error(), refresh))
	}
	if err := keyring.Set(keyringService, keyringUser, string(payload)); err != nil {
		return keyringWriteError(err, refresh, scope)
	}
	return nil
}

func keyringWriteError(err error, secrets ...string) error {
	if errors.Is(err, keyring.ErrSetDataTooBig) || strings.Contains(err.Error(), "4096") {
		return errKeyringTooBig
	}
	msg := err.Error()
	lower := strings.ToLower(msg)
	if strings.Contains(lower, "security") || strings.Contains(msg, "/usr/bin/security") {
		return errors.New("docs-cli: error: keyring set failed")
	}
	return fmt.Errorf("docs-cli: error: %s", mask(msg, secrets...))
}

func tokenPath(cfg *config.Config) string {
	return filepath.Join(cfg.Root, "token.json")
}

func readLegacy(cfg *config.Config) (legacyFile, bool, error) {
	path := tokenPath(cfg)
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return legacyFile{}, false, nil
		}
		return legacyFile{}, false, fmt.Errorf("docs-cli: error: %s", mask(err.Error()))
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return legacyFile{}, false, errors.New("docs-cli: error: refusing symlink token.json")
	}
	if !info.Mode().IsRegular() {
		return legacyFile{}, true, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return legacyFile{}, false, fmt.Errorf("docs-cli: error: %s", mask(err.Error()))
	}
	var leg legacyFile
	if err := json.Unmarshal(data, &leg); err != nil {
		return legacyFile{}, true, nil
	}
	return leg, true, nil
}

func deleteLegacyIfRefresh(cfg *config.Config) error {
	path := tokenPath(cfg)
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("docs-cli: error: %s", mask(err.Error()))
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errTokenFileDelete
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("docs-cli: error: %s", mask(err.Error()))
	}
	var leg legacyFile
	if err := json.Unmarshal(data, &leg); err != nil || legacyRefresh(leg) == "" {
		return nil
	}
	if err := os.Remove(path); err != nil {
		return errTokenFileDelete
	}
	return nil
}
