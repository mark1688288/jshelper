package jobad

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestMain(m *testing.M) {
	keyring.MockInit()
	code := m.Run()
	keyring.MockRestore()
	os.Exit(code)
}

func cleanKeyring(t *testing.T) {
	t.Helper()
	keyring.MockInit()
}

func TestStoreAndLoadGeminiKey(t *testing.T) {
	cleanKeyring(t)
	if _, err := LoadGeminiKey(); !errors.Is(err, keyring.ErrNotFound) {
		t.Fatalf("missing key err = %v", err)
	}
	if err := StoreGeminiKey("gemini-test-key"); err != nil {
		t.Fatal(err)
	}
	got, err := LoadGeminiKey()
	if err != nil || got != "gemini-test-key" {
		t.Fatalf("key = %q err %v", got, err)
	}
	raw, err := keyring.Get(keyringService, geminiAccount)
	if err != nil || raw != `{"api_key":"gemini-test-key"}` {
		t.Fatalf("stored %q err %v", raw, err)
	}
}

func TestLoadGeminiKeyUnavailable(t *testing.T) {
	const secret = "security: boom-from-keychain"
	keyring.MockInitWithError(errors.New(secret))
	t.Cleanup(func() { keyring.MockInit() })
	_, err := LoadGeminiKey()
	if !errors.Is(err, errKeyringUnavailable) || err.Error() != "docs-cli: keyring unavailable" {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "security") {
		t.Fatalf("system text leaked: %v", err)
	}
	err = StoreGeminiKey("gemini-test-key")
	if !errors.Is(err, errKeyringUnavailable) || strings.Contains(err.Error(), "gemini-test-key") {
		t.Fatalf("store err = %v", err)
	}
}

func TestLoadGeminiKeyInvalid(t *testing.T) {
	cleanKeyring(t)
	for _, raw := range []string{
		"not-json",
		`{"api_key":""}`,
		`{"api_key":"k","extra":1}`,
		`{"api_key":"k"}{"api_key":"two"}`,
	} {
		if err := keyring.Set(keyringService, geminiAccount, raw); err != nil {
			t.Fatal(err)
		}
		_, err := LoadGeminiKey()
		if !errors.Is(err, errGeminiKeyInvalid) || err.Error() != "docs-cli: keyring item is invalid; run docs-cli gemini-key" {
			t.Fatalf("raw %s err %v", raw, err)
		}
	}
}

func TestStoreGeminiKeyEmptyDoesNotOverwrite(t *testing.T) {
	cleanKeyring(t)
	if err := StoreGeminiKey("kept-key"); err != nil {
		t.Fatal(err)
	}
	if err := StoreGeminiKey(""); err != nil {
		t.Fatal(err)
	}
	got, err := LoadGeminiKey()
	if err != nil || got != "kept-key" {
		t.Fatalf("key = %q err %v", got, err)
	}
}

func TestStoreGeminiKeyTooBig(t *testing.T) {
	cleanKeyring(t)
	overhead := len(`{"api_key":""}`)
	fit := strings.Repeat("a", maxKeyringBytes-overhead)
	raw, err := json.Marshal(geminiKey{APIKey: fit})
	if err != nil || len(raw) != maxKeyringBytes {
		t.Fatalf("payload len %d err %v", len(raw), err)
	}
	if err := StoreGeminiKey(fit); err != nil {
		t.Fatal(err)
	}
	too := fit + "b"
	err = StoreGeminiKey(too)
	if !errors.Is(err, errKeyringItemTooBig) || err.Error() != "docs-cli: keyring item exceeds the 4096 byte command limit" {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), too) {
		t.Fatalf("key leaked: %v", err)
	}
	got, err := LoadGeminiKey()
	if err != nil || got != fit {
		t.Fatalf("oversize overwrote key %q err %v", got, err)
	}
}

func TestStoreGeminiKeyLeavesRefreshToken(t *testing.T) {
	cleanKeyring(t)
	const refresh = `{"refresh_token":"refresh-token-value-1","scope":"https://www.googleapis.com/auth/drive"}`
	if err := keyring.Set(keyringService, "refresh-token", refresh); err != nil {
		t.Fatal(err)
	}
	if err := StoreGeminiKey("gemini-test-key"); err != nil {
		t.Fatal(err)
	}
	got, err := keyring.Get(keyringService, "refresh-token")
	if err != nil || got != refresh {
		t.Fatalf("refresh-token = %q err %v", got, err)
	}
	key, err := LoadGeminiKey()
	if err != nil || key != "gemini-test-key" {
		t.Fatalf("gemini key = %q err %v", key, err)
	}
}

func TestMapSectionsAcceptsExactPartition(t *testing.T) {
	cleanKeyring(t)
	const key = "gemini-test-key"
	if err := StoreGeminiKey(key); err != nil {
		t.Fatal(err)
	}
	blocks := []Block{
		{Heading: "What we offer", Text: "Medical insurance"},
		{Heading: "Job Requirement", Text: "Degree holder"},
	}
	fallback := phraseDoc()
	client, cap := testGeminiClient(t, http.StatusOK, geminiEnvelope(t, sectionMapping{
		Duties:       []string{"Medical insurance"},
		Requirements: []string{"Degree holder"},
	}))
	got, err := MapSections(context.Background(), fallback, blocks, client)
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != fallback.Source || got.Header != fallback.Header {
		t.Fatalf("header or source changed: %#v", got)
	}
	if join(got.Offer) != "" || join(got.Duties) != "Medical insurance" || join(got.Requirements) != "Degree holder" || join(got.Trailer) != "" {
		t.Fatalf("sections = %#v", got)
	}
	if got.Offer == nil || got.Trailer == nil {
		t.Fatal("accepted empty section is nil")
	}
	assertGeminiRequest(t, cap, key, blocks, fallback.Header.TitleBlock)
}

func TestMapSectionsRejectsInexactReply(t *testing.T) {
	blocks := []Block{
		{Heading: "What we offer", Text: "Medical insurance"},
		{Heading: "Job Requirement", Text: "Degree holder"},
	}
	fallback := phraseDoc()
	cases := []struct {
		name     string
		status   int
		response string
	}{
		{"rewrite", http.StatusOK, geminiEnvelope(t, sectionMapping{Offer: []string{"Medical insurance coverage"}, Requirements: []string{"Degree holder"}})},
		{"extra", http.StatusOK, geminiEnvelope(t, sectionMapping{Offer: []string{"Medical insurance"}, Requirements: []string{"Degree holder"}, Trailer: []string{"Invented line"}})},
		{"missing", http.StatusOK, geminiEnvelope(t, sectionMapping{Offer: []string{"Medical insurance"}})},
		{"http", http.StatusBadGateway, `{"candidates":[]}`},
		{"prose", http.StatusOK, `{"candidates":[{"content":{"parts":[{"text":"not json"}]}}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cleanKeyring(t)
			if err := StoreGeminiKey("gemini-test-key"); err != nil {
				t.Fatal(err)
			}
			client, _ := testGeminiClient(t, tc.status, tc.response)
			got, err := MapSections(context.Background(), fallback, blocks, client)
			if !errors.Is(err, errGeminiMappingRejected) || err.Error() != "docs-cli: gemini mapping rejected" {
				t.Fatalf("err = %v", err)
			}
			if join(got.Offer) != join(fallback.Offer) || join(got.Duties) != join(fallback.Duties) || join(got.Requirements) != join(fallback.Requirements) || join(got.Trailer) != join(fallback.Trailer) {
				t.Fatalf("phrase result replaced: %#v", got)
			}
			if got.Header != fallback.Header || got.Source != fallback.Source {
				t.Fatalf("header or source changed: %#v", got)
			}
		})
	}
}

func TestMapSectionsNoKeySkipsHTTP(t *testing.T) {
	cleanKeyring(t)
	hits := 0
	client, _ := testGeminiClient(t, http.StatusOK, geminiEnvelope(t, sectionMapping{}))
	client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		hits++
		return nil, errors.New("should not be called")
	})
	fallback := phraseDoc()
	got, err := MapSections(context.Background(), fallback, nil, client)
	if !errors.Is(err, keyring.ErrNotFound) || hits != 0 {
		t.Fatalf("err %v hits %d", err, hits)
	}
	if got.Header != fallback.Header || join(got.Offer) != join(fallback.Offer) {
		t.Fatalf("fallback changed: %#v", got)
	}
}

func TestMapSectionsCancelledContext(t *testing.T) {
	cleanKeyring(t)
	if err := StoreGeminiKey("gemini-test-key"); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, r.Context().Err()
	})}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fallback := phraseDoc()
	got, err := MapSections(ctx, fallback, []Block{{Text: "Medical insurance"}}, client)
	if !errors.Is(err, errGeminiMappingRejected) {
		t.Fatalf("err = %v", err)
	}
	if join(got.Offer) != join(fallback.Offer) || got.Header != fallback.Header {
		t.Fatalf("phrase result replaced: %#v", got)
	}
}

func TestCollectBlocks(t *testing.T) {
	const warning = "Applications submitted without expected salary in resume may be excluded from consideration."
	page := `<span data-automation="job-detail-title">UNIQUE-TITLE</span>
<div data-automation="jobAdDetails">
  <p><strong>What you'll do:</strong></p>
  <ul><li>Design the API</li></ul>
  <p><strong>Team culture:</strong></p>
  <ul><li>Share context</li></ul>
  <p>Write to person@example.com today</p>
  <p><strong>` + warning + `</strong><strong>Job Description</strong></p>
  <ul><li>Write notes</li></ul>
</div>`
	blocks, err := CollectBlocks([]byte(page))
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, len(blocks))
	for i, block := range blocks {
		got[i] = block.Heading + " | " + block.Text
	}
	want := []string{
		"What you'll do: | Design the API",
		"Team culture: | Share context",
		"Team culture: | Write to today",
		"Job Description | Write notes",
	}
	if join(got) != join(want) {
		t.Fatalf("blocks = %#v", got)
	}
	blob := join(got)
	if strings.Contains(blob, "UNIQUE-TITLE") || strings.Contains(blob, warning) || strings.Contains(blob, "example.com") {
		t.Fatalf("collected header, warning, or email: %s", blob)
	}

	_, err = CollectBlocks([]byte(`<p>no ad</p>`))
	if !errors.Is(err, ErrJobAdDetailsNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestCollectBlocksMatchPhraseItems(t *testing.T) {
	page, err := os.ReadFile("testdata/jobsdb_min.html")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := Parse(page)
	if err != nil {
		t.Fatal(err)
	}
	blocks, err := CollectBlocks(page)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != len(doc.Offer)+len(doc.Duties)+len(doc.Requirements)+len(doc.Trailer) {
		t.Fatalf("blocks %d phrase items %d+%d+%d+%d", len(blocks), len(doc.Offer), len(doc.Duties), len(doc.Requirements), len(doc.Trailer))
	}
	counts := map[string]int{}
	for _, block := range blocks {
		counts[block.Text]++
		if strings.Contains(block.Text, doc.Header.TitleBlock) || strings.Contains(block.Heading, doc.Header.TitleBlock) {
			t.Fatalf("header sent in block %#v", block)
		}
	}
	for _, item := range append(append(append(doc.Offer, doc.Duties...), doc.Requirements...), doc.Trailer...) {
		counts[item]--
		if counts[item] < 0 {
			t.Fatalf("phrase item missing from blocks: %q", item)
		}
	}
	blob := join(blocksText(blocks))
	if strings.Contains(blob, "Applications submitted without expected salary") || strings.Contains(blob, "example.com") {
		t.Fatalf("warning or email collected: %s", blob)
	}
}

func phraseDoc() Document {
	return Document{
		Source: "asset/SA_jobs.html",
		Header: Header{
			TitleBlock:     "UNIQUE-TITLE",
			Location:       "Kwun Tong District",
			WorkType:       "Full time",
			Classification: "Business/Systems Analysts",
		},
		Offer:        []string{"Medical insurance"},
		Duties:       []string{},
		Requirements: []string{"Degree holder"},
		Trailer:      []string{},
	}
}

func geminiEnvelope(t *testing.T, mapped sectionMapping) string {
	t.Helper()
	inner, err := json.Marshal(mapped)
	if err != nil {
		t.Fatal(err)
	}
	outer := map[string]any{
		"candidates": []any{
			map[string]any{
				"content": map[string]any{
					"parts": []any{map[string]any{"text": string(inner)}},
				},
			},
		},
	}
	raw, err := json.Marshal(outer)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

type captured struct {
	url    string
	header http.Header
	body   string
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func testGeminiClient(t *testing.T, status int, response string) (*http.Client, *captured) {
	t.Helper()
	cap := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, response)
	}))
	t.Cleanup(srv.Close)
	dest, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				return nil, err
			}
			cap.url = r.URL.String()
			cap.header = r.Header.Clone()
			cap.body = string(body)
			out, err := http.NewRequestWithContext(r.Context(), r.Method, dest.String(), bytes.NewReader(body))
			if err != nil {
				return nil, err
			}
			out.Header = r.Header.Clone()
			return http.DefaultTransport.RoundTrip(out)
		}),
	}
	return client, cap
}

func assertGeminiRequest(t *testing.T, cap *captured, key string, blocks []Block, title string) {
	t.Helper()
	u, err := url.Parse(cap.url)
	if err != nil {
		t.Fatal(err)
	}
	if u.String() != geminiURL || u.RawQuery != "" || u.Host != "generativelanguage.googleapis.com" {
		t.Fatalf("url = %s", cap.url)
	}
	if !strings.Contains(u.Path, geminiModel) {
		t.Fatalf("path = %s", u.Path)
	}
	if strings.Contains(cap.url, key) || strings.Contains(cap.body, key) {
		t.Fatal("api key appeared in the url or body")
	}
	if cap.header.Get("x-goog-api-key") != key {
		t.Fatalf("x-goog-api-key = %q", cap.header.Get("x-goog-api-key"))
	}
	for name, values := range cap.header {
		if name == "X-Goog-Api-Key" {
			continue
		}
		if strings.Contains(strings.Join(values, " "), key) {
			t.Fatalf("key in header %s", name)
		}
	}
	if strings.Contains(cap.body, title) || strings.Contains(cap.body, "Kwun Tong") {
		t.Fatalf("header text was sent: %s", cap.body)
	}
	for _, block := range blocks {
		if !strings.Contains(cap.body, block.Text) || !strings.Contains(cap.body, block.Heading) {
			t.Fatalf("body missing block %#v", block)
		}
	}
}

func join(items []string) string {
	return strings.Join(items, "|")
}

func blocksText(blocks []Block) []string {
	out := make([]string, len(blocks))
	for i, block := range blocks {
		out[i] = block.Heading + " " + block.Text
	}
	return out
}
