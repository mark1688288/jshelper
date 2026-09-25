package oauth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/mark1688288/jshelper/internal/config"
)

const driveScope = "https://www.googleapis.com/auth/drive"

var (
	errInvalidScope = errors.New("docs-cli: invalid_scope; add https://www.googleapis.com/auth/drive under Google Auth platform → Data Access, then run docs-cli auth")
	errRedirectURI  = errors.New("docs-cli: redirect_uri_mismatch; the OAuth client must be a Desktop installed client, not a Web or IAP client")
	errScopeGrant   = errors.New("docs-cli: token response did not grant https://www.googleapis.com/auth/drive")
	errNoRefresh    = errors.New("docs-cli: error: token response did not include a refresh token")
)

// Options controls one authorization.
// Force starts a browser consent even when a keyring item already exists.
// HTTPClient replaces the client used for the token endpoint. Nil uses http.DefaultClient.
type Options struct {
	Force       bool
	OpenBrowser func(rawURL string) error
	Stderr      io.Writer
	HTTPClient  *http.Client
}

// Authorize returns an access token for cfg.
// The refresh token is stored in the OS keyring. The access token stays in the returned value.
func Authorize(ctx context.Context, cfg *config.Config, opt Options) (*oauth2.Token, error) {
	if cfg == nil {
		return nil, errors.New("docs-cli: error: missing config")
	}
	stderr := opt.Stderr
	if stderr == nil {
		stderr = io.Discard
	}
	if tok, err := cachedToken(cfg, opt.Force, stderr); tok != nil || err != nil {
		return tok, err
	}
	return interactive(ctx, cfg, opt, stderr)
}

func interactive(ctx context.Context, cfg *config.Config, opt Options, stderr io.Writer) (*oauth2.Token, error) {
	state, err := randomState()
	if err != nil {
		return nil, err
	}
	verifier := oauth2.GenerateVerifier()
	loop, err := listenLoopback(state)
	if err != nil {
		return nil, err
	}
	defer loop.shutdown()

	wait, cancel := context.WithTimeout(ctx, cfg.OAuthTimeout)
	defer cancel()
	if opt.HTTPClient != nil {
		wait = context.WithValue(wait, oauth2.HTTPClient, opt.HTTPClient)
	}

	redirect := loop.redirectURL()
	conf := oauthConfig(cfg, redirect)
	authURL := conf.AuthCodeURL(state,
		oauth2.S256ChallengeOption(verifier),
		oauth2.SetAuthURLParam("access_type", cfg.AccessType),
		oauth2.SetAuthURLParam("prompt", cfg.Prompt),
	)
	fmt.Fprintf(stderr, "docs-cli: listening 127.0.0.1:%d\n", loop.port())
	fmt.Fprintf(stderr, "docs-cli: open this URL if the browser does not:\n%s\n", authURL)

	opener := opt.OpenBrowser
	if opener == nil {
		opener = openBrowser
	}
	if err := opener(authURL); err != nil {
		fmt.Fprintf(stderr, "docs-cli: error: %s\n", mask(err.Error(), cfg.ClientSecret, verifier))
	}

	var got callback
	select {
	case got = <-loop.done:
		loop.shutdown()
	case <-wait.Done():
		loop.shutdown()
		if errors.Is(wait.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("docs-cli: oauth timed out after %s", formatTimeout(cfg.OAuthTimeout))
		}
		return nil, fmt.Errorf("docs-cli: error: %s", mask(wait.Err().Error(), cfg.ClientSecret, verifier))
	}
	if got.err != nil {
		return nil, got.err
	}

	tok, err := conf.Exchange(wait, got.code, oauth2.VerifierOption(verifier))
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("docs-cli: oauth timed out after %s", formatTimeout(cfg.OAuthTimeout))
		}
		return nil, exchangeError(err, cfg.ClientSecret, verifier, got.code)
	}
	if !scopeGranted(tok) {
		return nil, errScopeGrant
	}
	if tok.RefreshToken == "" {
		return nil, errNoRefresh
	}
	if err := storeItem(cfg.Scope, tok.RefreshToken); err != nil {
		return nil, err
	}
	fmt.Fprintln(stderr, "docs-cli: stored refresh token in keyring")
	if err := deleteLegacyIfRefresh(cfg); err != nil {
		return nil, err
	}
	return tok, nil
}

func oauthConfig(cfg *config.Config, redirect string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		Endpoint: oauth2.Endpoint{
			AuthURL:   cfg.AuthURI,
			TokenURL:  cfg.TokenURI,
			AuthStyle: oauth2.AuthStyleInParams,
		},
		RedirectURL: redirect,
		Scopes:      []string{cfg.Scope},
	}
}

func scopeGranted(tok *oauth2.Token) bool {
	if tok == nil {
		return false
	}
	raw, _ := tok.Extra("scope").(string)
	for _, part := range strings.Fields(raw) {
		if part == driveScope {
			return true
		}
	}
	return false
}

func exchangeError(err error, secrets ...string) error {
	var retrieve *oauth2.RetrieveError
	text := err.Error()
	if errors.As(err, &retrieve) {
		text = retrieve.ErrorCode + " " + retrieve.ErrorDescription + " " + retrieve.Error()
	}
	if proto := protocolError(text); proto != nil {
		return proto
	}
	return fmt.Errorf("docs-cli: error: %s", mask(err.Error(), secrets...))
}

func protocolError(text string) error {
	if strings.Contains(text, "invalid_scope") || strings.Contains(text, "request is invalid") {
		return errInvalidScope
	}
	if strings.Contains(text, "redirect_uri_mismatch") {
		return errRedirectURI
	}
	return nil
}

func openBrowser(rawURL string) error {
	var bin string
	switch runtime.GOOS {
	case "darwin":
		bin = "open"
	case "linux":
		bin = "xdg-open"
	default:
		return errors.New("cannot open a browser on this OS")
	}
	cmd := exec.Command(bin, rawURL)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

func formatTimeout(d time.Duration) string {
	if d > 0 && d%time.Hour == 0 {
		return fmt.Sprintf("%dh", int64(d/time.Hour))
	}
	if d > 0 && d%time.Minute == 0 {
		return fmt.Sprintf("%dm", int64(d/time.Minute))
	}
	return d.String()
}

func mask(msg string, secrets ...string) string {
	for _, secret := range secrets {
		if len(secret) < 8 {
			continue
		}
		msg = strings.ReplaceAll(msg, secret, "***")
	}
	return msg
}
