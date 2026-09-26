package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
	"golang.org/x/oauth2"

	"github.com/mark1688288/jshelper/internal/config"
)

const (
	testClientID     = "abc.apps.googleusercontent.com"
	testSecret       = "client-secret-value"
	testRefresh      = "refresh-token-value-1"
	testAccess       = "access-token-value-1"
	testCode         = "auth-code-value"
	drivePlusProfile = "https://www.googleapis.com/auth/drive https://www.googleapis.com/auth/userinfo.email"
)

func TestMain(m *testing.M) {
	keyring.MockInit()
	code := m.Run()
	keyring.MockRestore()
	os.Exit(code)
}

func withMock(t *testing.T) {
	t.Helper()
	keyring.MockInit()
	t.Cleanup(keyring.MockRestore)
}

type tokenServer struct {
	mu     sync.Mutex
	hits   int
	form   url.Values
	status int
	body   string
	echo   bool
	srv    *httptest.Server
}

func newTokenServer(t *testing.T, status int, body string) *tokenServer {
	t.Helper()
	ts := &tokenServer{status: status, body: body}
	ts.srv = httptest.NewServer(http.HandlerFunc(ts.serve))
	t.Cleanup(ts.srv.Close)
	return ts
}

func (s *tokenServer) serve(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad", http.StatusBadRequest)
		return
	}
	body := s.body
	if s.echo {
		body = body + " " + r.PostForm.Get("code_verifier") + " " + r.PostForm.Get("client_secret") + " " + r.PostForm.Get("code")
	}
	s.mu.Lock()
	s.hits++
	s.form = r.PostForm
	status := s.status
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func (s *tokenServer) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits
}

func (s *tokenServer) post() url.Values {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.form
}

func testConfig(t *testing.T, tokenURL string) *config.Config {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	return &config.Config{
		Root:         dir,
		ClientID:     testClientID,
		ClientSecret: testSecret,
		AuthURI:      "https://accounts.google.com/o/oauth2/auth",
		TokenURI:     tokenURL,
		Scope:        driveScope,
		AccessType:   "offline",
		Prompt:       "consent",
		PKCEMethod:   "S256",
		OAuthTimeout: 5 * time.Second,
		RedirectURI:  "http://127.0.0.1",
	}
}

func tokenBody(scope, refresh, access string) string {
	return `{"access_token":"` + access + `","refresh_token":"` + refresh + `","token_type":"Bearer","expires_in":3600,"scope":"` + scope + `"}`
}

func writeMode(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

type flowResult struct {
	tok     *oauth2.Token
	authURL string
	stderr  string
	err     error
	port    int
}

func runFlow(t *testing.T, cfg *config.Config, client *http.Client, force bool, edit func(q url.Values), openErr error) flowResult {
	t.Helper()
	var stderr strings.Builder
	var authURL string
	var port int
	var wg sync.WaitGroup
	browser := &http.Client{Transport: &http.Transport{Proxy: nil}}
	opt := Options{
		Force:      force,
		Stderr:     &stderr,
		HTTPClient: client,
		OpenBrowser: func(raw string) error {
			authURL = raw
			u, err := url.Parse(raw)
			if err != nil {
				t.Errorf("auth url: %v", err)
				return err
			}
			redir, err := url.Parse(u.Query().Get("redirect_uri"))
			if err != nil {
				t.Errorf("redirect: %v", err)
				return err
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				fav := *redir
				fav.Path = "/favicon.ico"
				fav.RawQuery = ""
				resp, err := getRetry(browser, fav.String())
				if err == nil {
					resp.Body.Close()
					if resp.StatusCode != http.StatusNotFound {
						t.Errorf("favicon status %d", resp.StatusCode)
					}
				}
				q := url.Values{}
				q.Set("state", u.Query().Get("state"))
				q.Set("code", testCode)
				if edit != nil {
					edit(q)
				}
				cb := *redir
				cb.Path = "/"
				cb.RawQuery = q.Encode()
				resp, err = getRetry(browser, cb.String())
				if err != nil {
					t.Errorf("callback: %v", err)
					return
				}
				defer resp.Body.Close()
				body, _ := io.ReadAll(resp.Body)
				page := string(body)
				if strings.Contains(page, testCode) || strings.Contains(page, u.Query().Get("state")) {
					t.Errorf("callback page echoed query: %q", body)
				}
				if edit == nil && !strings.Contains(page, successHTML) {
					t.Errorf("success page %q", body)
				}
			}()
			return openErr
		},
	}
	tok, err := Authorize(context.Background(), cfg, opt)
	wg.Wait()
	text := stderr.String()
	if i := strings.Index(text, "listening 127.0.0.1:"); i >= 0 {
		line := text[i:]
		if nl := strings.IndexByte(line, '\n'); nl >= 0 {
			line = line[:nl]
		}
		_, _ = fmt.Sscanf(line, "listening 127.0.0.1:%d", &port)
	}
	return flowResult{tok: tok, authURL: authURL, stderr: text, err: err, port: port}
}

func getRetry(client *http.Client, raw string) (*http.Response, error) {
	var last error
	for i := 0; i < 50; i++ {
		resp, err := client.Get(raw)
		if err == nil {
			return resp, nil
		}
		last = err
		time.Sleep(10 * time.Millisecond)
	}
	return nil, last
}

func TestAuthorizeInteractive(t *testing.T) {
	withMock(t)
	ts := newTokenServer(t, http.StatusOK, tokenBody(drivePlusProfile, testRefresh, testAccess))
	cfg := testConfig(t, ts.srv.URL)
	writeMode(t, filepath.Join(cfg.Root, ".env"), "SECRET="+testSecret+"\n")
	writeMode(t, filepath.Join(cfg.Root, "credentials.json"), `{"installed":{"client_secret":"`+testSecret+`"}}`+"\n")

	got := runFlow(t, cfg, ts.srv.Client(), false, nil, nil)
	if got.err != nil {
		t.Fatalf("authorize: %v\nstderr:\n%s", got.err, got.stderr)
	}
	if ts.count() != 1 {
		t.Fatalf("token hits %d", ts.count())
	}
	assertAuthURL(t, got.authURL, cfg)
	post := ts.post()
	if post.Get("grant_type") != "authorization_code" {
		t.Fatalf("grant_type %q", post.Get("grant_type"))
	}
	if post.Get("client_secret") != testSecret || post.Get("client_id") != testClientID {
		t.Fatalf("client params id %q secret %q", post.Get("client_id"), post.Get("client_secret"))
	}
	verifier := post.Get("code_verifier")
	if verifier == "" || strings.Contains(got.authURL, verifier) || strings.Contains(got.authURL, "code_verifier=") {
		t.Fatalf("verifier leaked or missing: url %q verifier %q", got.authURL, verifier)
	}
	redir, err := url.Parse(post.Get("redirect_uri"))
	if err != nil || redir.Hostname() != "127.0.0.1" || redir.Port() == "" {
		t.Fatalf("redirect_uri %q", post.Get("redirect_uri"))
	}
	if post.Get("redirect_uri") != redirectFromAuth(t, got.authURL) {
		t.Fatalf("post redirect %q auth %q", post.Get("redirect_uri"), got.authURL)
	}
	if strings.Contains(got.stderr, testSecret) || strings.Contains(got.stderr, testCode) || strings.Contains(got.stderr, testRefresh) || strings.Contains(got.stderr, testAccess) || strings.Contains(got.stderr, verifier) {
		t.Fatalf("stderr leaked:\n%s", got.stderr)
	}
	if !strings.Contains(got.stderr, "docs-cli: stored refresh token in keyring") {
		t.Fatalf("stderr %s", got.stderr)
	}
	assertStored(t, testRefresh, cfg.Scope)
	if got.tok.AccessToken != testAccess || got.tok.RefreshToken != testRefresh {
		t.Fatalf("token access %q refresh %q", got.tok.AccessToken, got.tok.RefreshToken)
	}
	assertPortClosed(t, got.port)
	assertDirClean(t, cfg.Root, testRefresh)

	var stderr strings.Builder
	opened := false
	tok, err := Authorize(context.Background(), cfg, Options{
		Force:      false,
		Stderr:     &stderr,
		HTTPClient: ts.srv.Client(),
		OpenBrowser: func(string) error {
			opened = true
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if opened || strings.Contains(stderr.String(), "listening") {
		t.Fatalf("second call listened: opened %v stderr %s", opened, stderr.String())
	}
	if ts.count() != 1 {
		t.Fatalf("token hits %d", ts.count())
	}
	if tok.RefreshToken != testRefresh || tok.AccessToken != "" {
		t.Fatalf("cached token %+v", tok)
	}
}

func TestAuthorizeStatesDiffer(t *testing.T) {
	withMock(t)
	ts := newTokenServer(t, http.StatusOK, tokenBody(driveScope, testRefresh, testAccess))
	cfg := testConfig(t, ts.srv.URL)
	first := runFlow(t, cfg, ts.srv.Client(), true, nil, nil)
	second := runFlow(t, cfg, ts.srv.Client(), true, nil, nil)
	if first.err != nil || second.err != nil {
		t.Fatalf("first %v second %v", first.err, second.err)
	}
	s1 := mustState(t, first.authURL)
	s2 := mustState(t, second.authURL)
	if s1 == s2 {
		t.Fatalf("states match %s", s1)
	}
}

func TestAuthorizeStateMismatch(t *testing.T) {
	withMock(t)
	ts := newTokenServer(t, http.StatusOK, tokenBody(driveScope, testRefresh, testAccess))
	cfg := testConfig(t, ts.srv.URL)
	got := runFlow(t, cfg, ts.srv.Client(), false, func(q url.Values) {
		q.Set("state", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	}, nil)
	if got.err == nil || got.err.Error() != "docs-cli: error: oauth state mismatch" {
		t.Fatalf("err %v", got.err)
	}
	if ts.count() != 0 {
		t.Fatalf("token hits %d", ts.count())
	}
	if strings.Contains(got.stderr, testCode) {
		t.Fatalf("stderr contains code:\n%s", got.stderr)
	}
	assertPortClosed(t, got.port)
	if _, err := keyring.Get(keyringService, keyringUser); !errors.Is(err, keyring.ErrNotFound) {
		t.Fatalf("keyring err %v", err)
	}
}

func TestAuthorizeRejectsMissingDriveScope(t *testing.T) {
	withMock(t)
	ts := newTokenServer(t, http.StatusOK, tokenBody("https://www.googleapis.com/auth/userinfo.email", testRefresh, testAccess))
	cfg := testConfig(t, ts.srv.URL)
	got := runFlow(t, cfg, ts.srv.Client(), false, nil, nil)
	if !errors.Is(got.err, errScopeGrant) {
		t.Fatalf("err %v", got.err)
	}
	if _, err := keyring.Get(keyringService, keyringUser); !errors.Is(err, keyring.ErrNotFound) {
		t.Fatalf("keyring was written: %v", err)
	}
}

func TestAuthorizeEmptyRefreshItem(t *testing.T) {
	withMock(t)
	if err := keyring.Set(keyringService, keyringUser, `{"refresh_token":"","scope":"https://www.googleapis.com/auth/drive"}`); err != nil {
		t.Fatal(err)
	}
	ts := newTokenServer(t, http.StatusOK, tokenBody(driveScope, testRefresh, testAccess))
	cfg := testConfig(t, ts.srv.URL)
	opened := false
	_, err := Authorize(context.Background(), cfg, Options{
		Force:      false,
		Stderr:     io.Discard,
		HTTPClient: ts.srv.Client(),
		OpenBrowser: func(string) error {
			opened = true
			return nil
		},
	})
	if !errors.Is(err, errKeyringInvalid) {
		t.Fatalf("err %v", err)
	}
	if opened || ts.count() != 0 {
		t.Fatalf("opened %v hits %d", opened, ts.count())
	}

	legacyPath := filepath.Join(cfg.Root, "token.json")
	writeMode(t, legacyPath, `{"token":{"refresh_token":"legacy-refresh-token"},"scope":"https://www.googleapis.com/auth/drive.file"}`)
	got := runFlow(t, cfg, ts.srv.Client(), true, nil, nil)
	if got.err != nil {
		t.Fatalf("auth: %v\n%s", got.err, got.stderr)
	}
	if _, statErr := os.Stat(legacyPath); !os.IsNotExist(statErr) {
		t.Fatalf("token.json remains: %v", statErr)
	}
	assertStored(t, testRefresh, cfg.Scope)
	if ts.count() != 1 {
		t.Fatalf("hits %d", ts.count())
	}
}

func TestAuthorizeKeyringUnavailable(t *testing.T) {
	withMock(t)
	secret := "security-tool-secret"
	keyring.MockInitWithError(errors.New("security: " + secret))
	ts := newTokenServer(t, http.StatusOK, tokenBody(driveScope, testRefresh, testAccess))
	cfg := testConfig(t, ts.srv.URL)
	var stderr strings.Builder
	opened := false
	_, err := Authorize(context.Background(), cfg, Options{
		Force:  true,
		Stderr: &stderr,
		OpenBrowser: func(string) error {
			opened = true
			return nil
		},
	})
	if !errors.Is(err, errKeyringUnavailable) || err.Error() != errKeyringUnavailable.Error() {
		t.Fatalf("err %v", err)
	}
	if opened || strings.Contains(stderr.String(), "listening") || strings.Contains(stderr.String(), secret) || strings.Contains(stderr.String(), "security") {
		t.Fatalf("opened %v stderr %s", opened, stderr.String())
	}
	if ts.count() != 0 {
		t.Fatalf("hits %d", ts.count())
	}
}

func TestAuthorizeTimeout(t *testing.T) {
	withMock(t)
	ts := newTokenServer(t, http.StatusOK, tokenBody(driveScope, testRefresh, testAccess))
	cfg := testConfig(t, ts.srv.URL)
	cfg.OAuthTimeout = 200 * time.Millisecond
	var stderr strings.Builder
	_, err := Authorize(context.Background(), cfg, Options{
		Force:      true,
		Stderr:     &stderr,
		HTTPClient: ts.srv.Client(),
		OpenBrowser: func(string) error {
			return nil
		},
	})
	if err == nil || err.Error() != "docs-cli: oauth timed out after 200ms" {
		t.Fatalf("err %v", err)
	}
	if _, getErr := keyring.Get(keyringService, keyringUser); !errors.Is(getErr, keyring.ErrNotFound) {
		t.Fatalf("keyring %v", getErr)
	}
	if ts.count() != 0 {
		t.Fatalf("hits %d", ts.count())
	}
	port := 0
	text := stderr.String()
	if _, scanErr := fmt.Sscanf(text[strings.Index(text, "listening "):], "listening 127.0.0.1:%d\n", &port); scanErr != nil || port == 0 {
		t.Fatalf("port from %q: %v", text, scanErr)
	}
	assertPortClosed(t, port)
}

func TestAuthorizeMigratesLegacyFile(t *testing.T) {
	withMock(t)
	ts := newTokenServer(t, http.StatusOK, tokenBody(driveScope, testRefresh, testAccess))
	cfg := testConfig(t, ts.srv.URL)
	legacy := `{"scope":"https://www.googleapis.com/auth/drive","token":{"access_token":"` + testAccess + `","refresh_token":"legacy-refresh-token"}}`
	writeMode(t, filepath.Join(cfg.Root, "token.json"), legacy)
	opened := false
	tok, err := Authorize(context.Background(), cfg, Options{
		Force:  false,
		Stderr: io.Discard,
		OpenBrowser: func(string) error {
			opened = true
			return nil
		},
		HTTPClient: ts.srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if opened || ts.count() != 0 {
		t.Fatalf("opened %v hits %d", opened, ts.count())
	}
	if tok.RefreshToken != "legacy-refresh-token" || tok.AccessToken != "" {
		t.Fatalf("token %+v", tok)
	}
	assertStored(t, "legacy-refresh-token", cfg.Scope)
	if _, err := os.Stat(filepath.Join(cfg.Root, "token.json")); !os.IsNotExist(err) {
		t.Fatalf("token.json still present: %v", err)
	}
}

func TestAuthorizeTokenFileScopeMismatch(t *testing.T) {
	withMock(t)
	ts := newTokenServer(t, http.StatusOK, tokenBody(driveScope, testRefresh, testAccess))
	cfg := testConfig(t, ts.srv.URL)
	path := filepath.Join(cfg.Root, "token.json")
	writeMode(t, path, `{"scope":"https://www.googleapis.com/auth/drive.file","token":{"refresh_token":"legacy-refresh-token"}}`)
	opened := false
	_, err := Authorize(context.Background(), cfg, Options{
		Force: false,
		OpenBrowser: func(string) error {
			opened = true
			return nil
		},
		HTTPClient: ts.srv.Client(),
	})
	if !errors.Is(err, errTokenFileScope) {
		t.Fatalf("err %v", err)
	}
	if opened || ts.count() != 0 {
		t.Fatalf("opened %v hits %d", opened, ts.count())
	}
	if _, getErr := keyring.Get(keyringService, keyringUser); !errors.Is(getErr, keyring.ErrNotFound) {
		t.Fatal(getErr)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatal(statErr)
	}
}

func TestAuthorizeKeyringScopeMismatchKeepsFile(t *testing.T) {
	withMock(t)
	raw := `{"refresh_token":"old-refresh-token","scope":"https://www.googleapis.com/auth/drive.file"}`
	if err := keyring.Set(keyringService, keyringUser, raw); err != nil {
		t.Fatal(err)
	}
	ts := newTokenServer(t, http.StatusOK, tokenBody(driveScope, testRefresh, testAccess))
	cfg := testConfig(t, ts.srv.URL)
	path := filepath.Join(cfg.Root, "token.json")
	writeMode(t, path, `{"scope":"https://www.googleapis.com/auth/drive","token":{"refresh_token":"legacy-refresh-token"}}`)
	opened := false
	_, err := Authorize(context.Background(), cfg, Options{
		Force: false,
		OpenBrowser: func(string) error {
			opened = true
			return nil
		},
	})
	if !errors.Is(err, errKeyringScope) {
		t.Fatalf("err %v", err)
	}
	if opened || ts.count() != 0 {
		t.Fatalf("opened %v hits %d", opened, ts.count())
	}
	got, getErr := keyring.Get(keyringService, keyringUser)
	if getErr != nil || got != raw {
		t.Fatalf("keyring %q err %v", got, getErr)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatal(statErr)
	}
}

func TestAuthorizeUsesKeyringAndDeletesLegacy(t *testing.T) {
	withMock(t)
	if err := keyring.Set(keyringService, keyringUser, `{"refresh_token":"keyring-refresh-token","scope":"https://www.googleapis.com/auth/drive"}`); err != nil {
		t.Fatal(err)
	}
	ts := newTokenServer(t, http.StatusOK, tokenBody(driveScope, testRefresh, testAccess))
	cfg := testConfig(t, ts.srv.URL)
	writeMode(t, filepath.Join(cfg.Root, "token.json"), `{"refresh_token":"legacy-refresh-token","scope":"https://www.googleapis.com/auth/drive"}`)
	tok, err := Authorize(context.Background(), cfg, Options{Force: false, Stderr: io.Discard, HTTPClient: ts.srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if tok.RefreshToken != "keyring-refresh-token" {
		t.Fatalf("refresh %q", tok.RefreshToken)
	}
	if ts.count() != 0 {
		t.Fatalf("hits %d", ts.count())
	}
	if _, statErr := os.Stat(filepath.Join(cfg.Root, "token.json")); !os.IsNotExist(statErr) {
		t.Fatal(statErr)
	}
	assertStored(t, "keyring-refresh-token", driveScope)
}

func TestAuthorizeDoesNotReplaceRefreshWithEmpty(t *testing.T) {
	withMock(t)
	original := `{"refresh_token":"keyring-refresh-token","scope":"https://www.googleapis.com/auth/drive"}`
	if err := keyring.Set(keyringService, keyringUser, original); err != nil {
		t.Fatal(err)
	}
	ts := newTokenServer(t, http.StatusOK, tokenBody(driveScope, "", testAccess))
	cfg := testConfig(t, ts.srv.URL)
	path := filepath.Join(cfg.Root, "token.json")
	writeMode(t, path, `{"token":{"refresh_token":"legacy-refresh-token"},"scope":"https://www.googleapis.com/auth/drive"}`)
	got := runFlow(t, cfg, ts.srv.Client(), true, nil, nil)
	if !errors.Is(got.err, errNoRefresh) {
		t.Fatalf("err %v", got.err)
	}
	stored, err := keyring.Get(keyringService, keyringUser)
	if err != nil || stored != original {
		t.Fatalf("stored %q err %v", stored, err)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatal(statErr)
	}
}

func TestAuthorizeProtocolErrors(t *testing.T) {
	withMock(t)
	tests := []struct {
		name string
		body string
		want error
	}{
		{name: "invalid scope", body: `{"error":"invalid_scope","error_description":"nope"}`, want: errInvalidScope},
		{name: "request invalid", body: `{"error":"invalid_request","error_description":"the request is invalid"}`, want: errInvalidScope},
		{name: "redirect", body: `{"error":"redirect_uri_mismatch","error_description":"mismatch"}`, want: errRedirectURI},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keyring.MockInit()
			ts := newTokenServer(t, http.StatusBadRequest, tt.body)
			cfg := testConfig(t, ts.srv.URL)
			got := runFlow(t, cfg, ts.srv.Client(), true, nil, nil)
			if !errors.Is(got.err, tt.want) {
				t.Fatalf("err %v", got.err)
			}
			if tt.want == errRedirectURI && strings.Contains(got.err.Error(), "Data Access") {
				t.Fatalf("redirect message mentions Data Access: %s", got.err)
			}
			if tt.want == errInvalidScope && strings.Contains(got.err.Error(), "redirect_uri_mismatch") {
				t.Fatalf("scope message mentions redirect: %s", got.err)
			}
			if _, err := keyring.Get(keyringService, keyringUser); !errors.Is(err, keyring.ErrNotFound) {
				t.Fatal(err)
			}
		})
	}
}

func TestAuthorizeMasksTokenError(t *testing.T) {
	withMock(t)
	ts := newTokenServer(t, http.StatusInternalServerError, "leaked")
	ts.echo = true
	cfg := testConfig(t, ts.srv.URL)
	got := runFlow(t, cfg, ts.srv.Client(), false, nil, nil)
	if got.err == nil {
		t.Fatal("expected error")
	}
	text := got.err.Error() + "\n" + got.stderr
	post := ts.post()
	verifier := post.Get("code_verifier")
	for _, secret := range []string{testSecret, testCode, verifier} {
		if secret != "" && strings.Contains(text, secret) {
			t.Fatalf("leaked %q in %s", secret, text)
		}
	}
}

func TestAuthorizeCallbackError(t *testing.T) {
	withMock(t)
	ts := newTokenServer(t, http.StatusOK, tokenBody(driveScope, testRefresh, testAccess))
	cfg := testConfig(t, ts.srv.URL)
	got := runFlow(t, cfg, ts.srv.Client(), false, func(q url.Values) {
		q.Del("code")
		q.Set("error", "access_denied")
		q.Set("error_description", "user said no "+testSecret)
	}, nil)
	if got.err == nil || got.err.Error() != "docs-cli: error: access_denied" {
		t.Fatalf("err %v", got.err)
	}
	if strings.Contains(got.err.Error(), testSecret) || strings.Contains(got.stderr, testSecret) || ts.count() != 0 {
		t.Fatalf("err %v hits %d stderr %s", got.err, ts.count(), got.stderr)
	}
	assertPortClosed(t, got.port)
}

func TestFormatTimeout(t *testing.T) {
	if got := formatTimeout(3 * time.Minute); got != "3m" {
		t.Fatalf("3m rendered as %q", got)
	}
	if got := formatTimeout(15 * time.Minute); got != "15m" {
		t.Fatalf("15m rendered as %q", got)
	}
	if got := formatTimeout(200 * time.Millisecond); got != "200ms" {
		t.Fatalf("200ms rendered as %q", got)
	}
}

func TestParseStoredItemRejectsExtraFields(t *testing.T) {
	raw := `{"refresh_token":"refresh-token-value-1","scope":"https://www.googleapis.com/auth/drive","access_token":"access-token-value-1"}`
	if _, ok := parseStoredItem(raw); ok {
		t.Fatal("accepted an access token field")
	}
}

func TestKeyringWriteTooBig(t *testing.T) {
	err := keyringWriteError(keyring.ErrSetDataTooBig, testRefresh)
	if !errors.Is(err, errKeyringTooBig) {
		t.Fatalf("err %v", err)
	}
	if strings.Contains(err.Error(), testRefresh) {
		t.Fatalf("err leaked refresh: %v", err)
	}
}

func TestOAuthSourcesDoNotImportMathRand(t *testing.T) {
	matches, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range matches {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "math/rand") {
			t.Fatalf("%s imports math/rand", name)
		}
	}
}

func assertAuthURL(t *testing.T, raw string, cfg *config.Config) {
	t.Helper()
	if !strings.Contains(raw, "code_challenge_method=S256") {
		t.Fatalf("url %s", raw)
	}
	if strings.Contains(raw, cfg.ClientSecret) || strings.Contains(raw, "client_secret=") || strings.Contains(raw, "/o/oauth2/v2/auth") || strings.Contains(raw, "approval_prompt=") {
		t.Fatalf("url %s", raw)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme+"://"+u.Host+u.Path != cfg.AuthURI {
		t.Fatalf("auth uri %s", raw)
	}
	q := u.Query()
	if q.Get("response_type") != "code" || q.Get("access_type") != "offline" || q.Get("prompt") != "consent" || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		t.Fatalf("query %v", q)
	}
	if q.Get("client_id") != cfg.ClientID || q.Get("scope") != cfg.Scope || q.Get("code_verifier") != "" {
		t.Fatalf("query %v", q)
	}
	redir, err := url.Parse(q.Get("redirect_uri"))
	if err != nil || redir.Hostname() != "127.0.0.1" || redir.Port() == "" || strings.Contains(q.Get("redirect_uri"), "localhost") {
		t.Fatalf("redirect %q", q.Get("redirect_uri"))
	}
	mustState(t, raw)
}

func mustState(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	state := u.Query().Get("state")
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(state) {
		t.Fatalf("state %q", state)
	}
	return state
}

func redirectFromAuth(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query().Get("redirect_uri")
}

func assertStored(t *testing.T, refresh, scope string) {
	t.Helper()
	raw, err := keyring.Get(keyringService, keyringUser)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 2 || fields["refresh_token"] != refresh || fields["scope"] != scope {
		t.Fatalf("item %s", raw)
	}
	if strings.Contains(raw, "access_token") || strings.Contains(raw, testAccess) || strings.Contains(raw, testSecret) {
		t.Fatalf("item %s", raw)
	}
}

func assertPortClosed(t *testing.T, port int) {
	t.Helper()
	if port == 0 {
		t.Fatal("missing port")
	}
	conn, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 200*time.Millisecond)
	if err == nil {
		conn.Close()
		t.Fatalf("port %d still open", port)
	}
}

func assertDirClean(t *testing.T, root, refresh string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if d.Name() == "token.json" {
			t.Fatalf("token.json at %s", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(data), refresh) {
			t.Fatalf("%s contains refresh token", path)
		}
		if runtime.GOOS != "windows" && (d.Name() == ".env" || d.Name() == "credentials.json") {
			info, err := d.Info()
			if err != nil {
				return err
			}
			if info.Mode().Perm() != 0o600 {
				t.Fatalf("%s perm %o", path, info.Mode().Perm())
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSyncRefreshTokenPreservesOmittedRefresh(t *testing.T) {
	withMock(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"`+testAccess+`","token_type":"Bearer","expires_in":3600}`)
	}))
	t.Cleanup(srv.Close)
	cfg := testConfig(t, srv.URL)
	if err := keyring.Set(keyringService, keyringUser, `{"refresh_token":"`+testRefresh+`","scope":"`+driveScope+`"}`); err != nil {
		t.Fatal(err)
	}
	src := TokenSource(context.Background(), cfg, &oauth2.Token{
		AccessToken:  "expired-access-token",
		RefreshToken: testRefresh,
		Expiry:       time.Now().Add(-time.Hour),
	})
	if err := SyncRefreshToken(cfg, src); err != nil {
		t.Fatal(err)
	}
	assertStored(t, testRefresh, driveScope)
}

func TestSyncRefreshTokenStoresRotatedRefresh(t *testing.T) {
	withMock(t)
	const rotated = "refresh-token-value-2"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"`+testAccess+`","refresh_token":"`+rotated+`","token_type":"Bearer","expires_in":3600}`)
	}))
	t.Cleanup(srv.Close)
	cfg := testConfig(t, srv.URL)
	if err := keyring.Set(keyringService, keyringUser, `{"refresh_token":"`+testRefresh+`","scope":"`+driveScope+`"}`); err != nil {
		t.Fatal(err)
	}
	src := TokenSource(context.Background(), cfg, &oauth2.Token{
		AccessToken:  "expired-access-token",
		RefreshToken: testRefresh,
		Expiry:       time.Now().Add(-time.Hour),
	})
	if err := SyncRefreshToken(cfg, src); err != nil {
		t.Fatal(err)
	}
	assertStored(t, rotated, driveScope)
}

func TestSyncRefreshTokenIgnoresEmptyRefresh(t *testing.T) {
	withMock(t)
	cfg := testConfig(t, "https://oauth2.googleapis.com/token")
	if err := keyring.Set(keyringService, keyringUser, `{"refresh_token":"`+testRefresh+`","scope":"`+driveScope+`"}`); err != nil {
		t.Fatal(err)
	}
	src := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: testAccess})
	if err := SyncRefreshToken(cfg, src); err != nil {
		t.Fatal(err)
	}
	assertStored(t, testRefresh, driveScope)
}

func TestUserTokenErrorInvalidGrant(t *testing.T) {
	err := UserTokenError(&oauth2.RetrieveError{ErrorCode: "invalid_grant", ErrorDescription: "expired"}, testRefresh)
	want := "docs-cli: refresh failed; run docs-cli auth (a Testing-mode refresh token expires after 7 days; invalid_grant has other causes)"
	if err == nil || err.Error() != want {
		t.Fatalf("err %v", err)
	}
	if strings.Contains(err.Error(), testRefresh) {
		t.Fatalf("leaked %v", err)
	}
}
