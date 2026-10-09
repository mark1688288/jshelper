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
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestMain(m *testing.M) {
	keyring.MockInit()
	SetFMExecForTest(func([]string) ([]byte, []byte, int) {
		return nil, []byte("fm exec not stubbed"), 1
	})
	code := m.Run()
	SetFMExecForTest(nil)
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
	if join(got.Benefits) != "" || join(got.Duties) != "Medical insurance" || join(got.Requirements) != "Degree holder" || join(got.Trailer) != "" {
		t.Fatalf("sections = %#v", got)
	}
	if got.Benefits == nil || got.Trailer == nil {
		t.Fatal("accepted empty section is nil")
	}
	assertGeminiRequest(t, cap, key, blocks, fallback.Header)
}

func TestMapSectionsRejectsInexactReply(t *testing.T) {
	const key = "gemini-test-key"
	blocks := []Block{
		{Heading: "What we offer", Text: "Medical insurance"},
		{Heading: "Job Requirement", Text: "Degree holder"},
	}
	fallback := phraseDoc()
	cases := []struct {
		name     string
		status   int
		response string
		want     string
		hidden   []string
	}{
		{
			name:     "rewrite",
			status:   http.StatusOK,
			response: geminiEnvelope(t, sectionMapping{Benefits: []string{"Medical insurance coverage"}, Requirements: []string{"Degree holder"}}),
			want: "docs-cli: gemini mapping rejected: partition\n" +
				"docs-cli: gemini mapping rejected: sent=2 reply=2 missing=1 extra=1\n" +
				"docs-cli: gemini mapping rejected: missing: Medical insurance\n" +
				"docs-cli: gemini mapping rejected: extra: Medical insurance coverage",
		},
		{
			name:     "extra",
			status:   http.StatusOK,
			response: geminiEnvelope(t, sectionMapping{Benefits: []string{"Medical insurance"}, Requirements: []string{"Degree holder"}, Trailer: []string{"Invented line"}}),
			want: "docs-cli: gemini mapping rejected: partition\n" +
				"docs-cli: gemini mapping rejected: sent=2 reply=3 missing=0 extra=1\n" +
				"docs-cli: gemini mapping rejected: extra: Invented line",
		},
		{
			name:     "missing",
			status:   http.StatusOK,
			response: geminiEnvelope(t, sectionMapping{Benefits: []string{"Medical insurance"}}),
			want: "docs-cli: gemini mapping rejected: partition\n" +
				"docs-cli: gemini mapping rejected: sent=2 reply=1 missing=1 extra=0\n" +
				"docs-cli: gemini mapping rejected: missing: Degree holder",
		},
		{
			name:     "status",
			status:   http.StatusBadGateway,
			response: `{"error":"UPSTREAM-BODY ` + key + `"}`,
			want:     "docs-cli: gemini mapping rejected: status 502",
			hidden:   []string{"UPSTREAM-BODY", key},
		},
		{
			name:     "prose",
			status:   http.StatusOK,
			response: `{"candidates":[{"content":{"parts":[{"text":"not json ` + key + `"}]}}]}`,
			want:     errGeminiResponse.Error(),
			hidden:   []string{key, "not json"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cleanKeyring(t)
			if err := StoreGeminiKey(key); err != nil {
				t.Fatal(err)
			}
			client, _ := testGeminiClient(t, tc.status, tc.response)
			got, err := MapSections(context.Background(), fallback, blocks, client)
			if err == nil || err.Error() != tc.want || err.Error() == "docs-cli: gemini mapping rejected" {
				t.Fatalf("err = %v", err)
			}
			for _, secret := range tc.hidden {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("stderr leaked %q in %q", secret, err.Error())
				}
			}
			assertPhraseKept(t, got, fallback)
		})
	}
}

func TestMapSectionsPartitionSamples(t *testing.T) {
	const key = "gemini-test-key"
	fallback := phraseDoc()
	long := strings.Repeat("測", maxRejectRunes+1)
	spaced := "  spaced \n\t line  "
	blocks := []Block{
		{Text: "Keep one"},
		{Text: "Keep two"},
		{Text: "Keep three"},
		{Text: "Keep four"},
		{Text: "Keep five"},
	}
	mapped := sectionMapping{
		Benefits: []string{spaced, long},
		Duties:   []string{"Fifth extra", "Sixth extra"},
	}
	cleanKeyring(t)
	if err := StoreGeminiKey(key); err != nil {
		t.Fatal(err)
	}
	client, _ := testGeminiClient(t, http.StatusOK, geminiEnvelope(t, mapped))
	_, err := MapSections(context.Background(), fallback, blocks, client)
	if err == nil {
		t.Fatal("expected partition rejection")
	}
	want := "docs-cli: gemini mapping rejected: partition\n" +
		"docs-cli: gemini mapping rejected: sent=5 reply=4 missing=5 extra=4\n" +
		"docs-cli: gemini mapping rejected: missing: Keep one\n" +
		"docs-cli: gemini mapping rejected: missing: Keep two\n" +
		"docs-cli: gemini mapping rejected: missing: Keep three\n" +
		"docs-cli: gemini mapping rejected: extra: spaced line\n" +
		"docs-cli: gemini mapping rejected: extra: " + strings.Repeat("測", maxRejectRunes) + "\n" +
		"docs-cli: gemini mapping rejected: extra: Fifth extra"
	if err.Error() != want {
		t.Fatalf("err = %q", err.Error())
	}
	if strings.Contains(err.Error(), "Keep four") || strings.Contains(err.Error(), "Keep five") || strings.Contains(err.Error(), "Sixth extra") || strings.Contains(err.Error(), "...") {
		t.Fatalf("sample leaked or ellipsis added: %q", err.Error())
	}
	if strings.Count(err.Error(), "測") != maxRejectRunes {
		t.Fatalf("rune clip = %d", strings.Count(err.Error(), "測"))
	}

	t.Run("hides lines that contain the api key", func(t *testing.T) {
		cleanKeyring(t)
		if err := StoreGeminiKey(key); err != nil {
			t.Fatal(err)
		}
		keyBlocks := []Block{{Text: key}, {Text: "Visible missing"}}
		reply := sectionMapping{Benefits: []string{key + " tail", "Other extra"}}
		client, _ := testGeminiClient(t, http.StatusOK, geminiEnvelope(t, reply))
		_, err := MapSections(context.Background(), fallback, keyBlocks, client)
		want := "docs-cli: gemini mapping rejected: partition\n" +
			"docs-cli: gemini mapping rejected: sent=2 reply=2 missing=2 extra=2\n" +
			"docs-cli: gemini mapping rejected: missing: Visible missing\n" +
			"docs-cli: gemini mapping rejected: extra: Other extra"
		if err == nil || err.Error() != want {
			t.Fatalf("err = %v", err)
		}
		if strings.Contains(err.Error(), key) {
			t.Fatalf("api key leaked: %q", err.Error())
		}
	})

	t.Run("duplicate delta", func(t *testing.T) {
		cleanKeyring(t)
		if err := StoreGeminiKey(key); err != nil {
			t.Fatal(err)
		}
		dup := []Block{{Text: "Medical insurance"}, {Text: "Medical insurance"}}
		reply := sectionMapping{Benefits: []string{"Medical insurance"}}
		client, _ := testGeminiClient(t, http.StatusOK, geminiEnvelope(t, reply))
		_, err := MapSections(context.Background(), fallback, dup, client)
		want := "docs-cli: gemini mapping rejected: partition\n" +
			"docs-cli: gemini mapping rejected: sent=2 reply=1 missing=1 extra=0\n" +
			"docs-cli: gemini mapping rejected: missing: Medical insurance"
		if err == nil || err.Error() != want {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestMapSectionsRejectReasons(t *testing.T) {
	const key = "gemini-test-key"
	const leak = "UPSTREAM-ERR dial tcp secret"
	fallback := phraseDoc()
	blocks := []Block{{Text: "Medical insurance"}}

	t.Run("timeout", func(t *testing.T) {
		cleanKeyring(t)
		if err := StoreGeminiKey(key); err != nil {
			t.Fatal(err)
		}
		client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, &netTimeout{msg: leak}
		})}
		_, err := MapSections(context.Background(), fallback, blocks, client)
		if !errors.Is(err, errGeminiTimeout) || err.Error() != errGeminiTimeout.Error() {
			t.Fatalf("err = %v", err)
		}
		if strings.Contains(err.Error(), leak) || strings.Contains(err.Error(), "dial") {
			t.Fatalf("transport text leaked: %v", err)
		}
	})

	t.Run("deadline", func(t *testing.T) {
		cleanKeyring(t)
		if err := StoreGeminiKey(key); err != nil {
			t.Fatal(err)
		}
		client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, context.DeadlineExceeded
		})}
		_, err := MapSections(context.Background(), fallback, blocks, client)
		if !errors.Is(err, errGeminiTimeout) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("http", func(t *testing.T) {
		cleanKeyring(t)
		if err := StoreGeminiKey(key); err != nil {
			t.Fatal(err)
		}
		client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New(leak)
		})}
		_, err := MapSections(context.Background(), fallback, blocks, client)
		if !errors.Is(err, errGeminiHTTP) || strings.Contains(err.Error(), leak) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("response too long", func(t *testing.T) {
		cleanKeyring(t)
		if err := StoreGeminiKey(key); err != nil {
			t.Fatal(err)
		}
		body := "BODY-SECRET-" + key + strings.Repeat("x", maxGeminiResponse)
		client, _ := testGeminiClient(t, http.StatusOK, body)
		_, err := MapSections(context.Background(), fallback, blocks, client)
		if !errors.Is(err, errGeminiResponse) || strings.Contains(err.Error(), "BODY-SECRET") || strings.Contains(err.Error(), key) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("no candidates", func(t *testing.T) {
		cleanKeyring(t)
		if err := StoreGeminiKey(key); err != nil {
			t.Fatal(err)
		}
		client, _ := testGeminiClient(t, http.StatusOK, `{"candidates":[]}`)
		_, err := MapSections(context.Background(), fallback, blocks, client)
		if !errors.Is(err, errGeminiResponse) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("status 500", func(t *testing.T) {
		cleanKeyring(t)
		if err := StoreGeminiKey(key); err != nil {
			t.Fatal(err)
		}
		client, _ := testGeminiClient(t, http.StatusInternalServerError, "BODY-SECRET-"+key)
		_, err := MapSections(context.Background(), fallback, blocks, client)
		if err == nil || err.Error() != "docs-cli: gemini mapping rejected: status 500" {
			t.Fatalf("err = %v", err)
		}
		if strings.Contains(err.Error(), "BODY-SECRET") || strings.Contains(err.Error(), key) {
			t.Fatalf("body leaked: %v", err)
		}
	})
}

func TestMapSectionsStatusErrorMessage(t *testing.T) {
	const key = "gemini-test-key"
	const demand = "This model is currently experiencing high demand. Spikes in demand are usually temporary. Please try again later."
	fallback := phraseDoc()
	blocks := []Block{{Text: "Medical insurance"}}

	t.Run("503 high demand", func(t *testing.T) {
		cleanKeyring(t)
		if err := StoreGeminiKey(key); err != nil {
			t.Fatal(err)
		}
		body := `{"error":{"code":503,"message":"` + demand + `","status":"UNAVAILABLE","details":["DETAIL-SECRET ` + key + `"]}}`
		client, _ := testGeminiClient(t, http.StatusServiceUnavailable, body)
		_, err := MapSections(context.Background(), fallback, blocks, client)
		want := "docs-cli: gemini mapping rejected: status 503\n" +
			"docs-cli: gemini mapping rejected: UNAVAILABLE: " + demand
		if err == nil || err.Error() != want {
			t.Fatalf("err = %q", err)
		}
		if strings.Contains(err.Error(), "DETAIL-SECRET") || strings.Contains(err.Error(), key) {
			t.Fatalf("detail leaked: %q", err.Error())
		}
	})

	t.Run("message hides api key", func(t *testing.T) {
		cleanKeyring(t)
		if err := StoreGeminiKey(key); err != nil {
			t.Fatal(err)
		}
		body := `{"error":{"status":"UNAVAILABLE","message":"see ` + key + `"}}`
		client, _ := testGeminiClient(t, http.StatusServiceUnavailable, body)
		_, err := MapSections(context.Background(), fallback, blocks, client)
		if err == nil || err.Error() != "docs-cli: gemini mapping rejected: status 503" || strings.Contains(err.Error(), key) {
			t.Fatalf("err = %q", err)
		}
	})

	t.Run("non string fields stay hidden", func(t *testing.T) {
		cleanKeyring(t)
		if err := StoreGeminiKey(key); err != nil {
			t.Fatal(err)
		}
		body := `{"error":{"status":503,"message":{"text":"SECRET-DETAIL"},"details":["BODY-SECRET"]}}`
		client, _ := testGeminiClient(t, http.StatusInternalServerError, body)
		_, err := MapSections(context.Background(), fallback, blocks, client)
		if err == nil || err.Error() != "docs-cli: gemini mapping rejected: status 500" {
			t.Fatalf("err = %q", err)
		}
		if strings.Contains(err.Error(), "SECRET-DETAIL") || strings.Contains(err.Error(), "BODY-SECRET") {
			t.Fatalf("raw field leaked: %q", err.Error())
		}
	})

	t.Run("whitespace and clip", func(t *testing.T) {
		cleanKeyring(t)
		if err := StoreGeminiKey(key); err != nil {
			t.Fatal(err)
		}
		long := strings.Repeat("測", 300)
		body := `{"error":{"status":"  S  ","message":"` + long + `"}}`
		client, _ := testGeminiClient(t, http.StatusBadGateway, body)
		_, err := MapSections(context.Background(), fallback, blocks, client)
		suffix := "S: " + long
		suffix = string([]rune(suffix)[:maxStatusDetailRunes])
		want := "docs-cli: gemini mapping rejected: status 502\n" +
			"docs-cli: gemini mapping rejected: " + suffix
		if err == nil || err.Error() != want || strings.Contains(err.Error(), "...") {
			t.Fatalf("err = %q", err)
		}
		if strings.Count(err.Error(), "測") != maxStatusDetailRunes-len("S: ") {
			t.Fatalf("clip = %d", strings.Count(err.Error(), "測"))
		}
	})
}

type netTimeout struct{ msg string }

func (e *netTimeout) Error() string   { return e.msg }
func (e *netTimeout) Timeout() bool   { return true }
func (e *netTimeout) Temporary() bool { return true }

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
	if got.Header != fallback.Header || join(got.Benefits) != join(fallback.Benefits) {
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
	if !errors.Is(err, errGeminiHTTP) || err.Error() != errGeminiHTTP.Error() {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "canceled") || strings.Contains(err.Error(), "context") {
		t.Fatalf("transport text leaked: %v", err)
	}
	if join(got.Benefits) != join(fallback.Benefits) || got.Header != fallback.Header {
		t.Fatalf("phrase result replaced: %#v", got)
	}
}

func TestCollectBlocks(t *testing.T) {
	const warning = "Applications submitted without expected salary in resume may be excluded from consideration."
	page := `<span data-automation="job-detail-title">UNIQUE-TITLE</span>
<span data-automation="advertiser-name">COMPANY-TOKEN<svg><title>Verified</title></svg></span>
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
	if strings.Contains(blob, "UNIQUE-TITLE") || strings.Contains(blob, "COMPANY-TOKEN") || strings.Contains(blob, "Verified") || strings.Contains(blob, warning) || strings.Contains(blob, "example.com") {
		t.Fatalf("collected header, warning, or email: %s", blob)
	}

	plain, err := CollectBlocks([]byte(`<p>no ad</p>`))
	if err != nil {
		t.Fatal(err)
	}
	if len(plain) != 1 || plain[0].Text != "no ad" {
		t.Fatalf("plain blocks = %#v", plain)
	}

	_, err = CollectBlocks([]byte(`<div>no ad</div>`))
	if !errors.Is(err, ErrJobAdDetailsNotFound) || err.Error() != errGeminiBlocks.Error() {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "jobad:") {
		t.Fatalf("cause text leaked: %v", err)
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
	if len(blocks) != len(doc.Benefits)+len(doc.Duties)+len(doc.Requirements)+len(doc.Trailer) {
		t.Fatalf("blocks %d phrase items %d+%d+%d+%d", len(blocks), len(doc.Benefits), len(doc.Duties), len(doc.Requirements), len(doc.Trailer))
	}
	counts := map[string]int{}
	for _, block := range blocks {
		counts[block.Text]++
		if strings.Contains(block.Text, doc.Header.TitleBlock) || strings.Contains(block.Heading, doc.Header.TitleBlock) || strings.Contains(block.Text, doc.Header.Company) || strings.Contains(block.Heading, doc.Header.Company) || strings.Contains(block.Text, "Verified") || strings.Contains(block.Text, "View all jobs") {
			t.Fatalf("header sent in block %#v", block)
		}
	}
	for _, item := range append(append(append(doc.Benefits, doc.Duties...), doc.Requirements...), doc.Trailer...) {
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

func TestCollectBlocksOtherSource(t *testing.T) {
	page := `<title>System Analyst at CTgoodjobs</title>
<script>loginId</script>
<script type="application/ld+json">` + jsonLD(t, map[string]any{
		"@type": "JobPosting",
		"title": "System Analyst",
		"hiringOrganization": map[string]any{
			"name": "Acme Ltd",
		},
		"description": `<p><strong>Key Responsibilities</strong></p><ul><li>Build the API</li></ul><p><strong>Requirements</strong></p><ul><li>Know Go</li></ul><p>Apply on the site</p>`,
	}) + `</script>`
	blocks, err := CollectBlocks([]byte(page))
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, len(blocks))
	for i, block := range blocks {
		got[i] = block.Heading + " | " + block.Text
	}
	want := []string{
		"Key Responsibilities | Build the API",
		"Requirements | Know Go",
		"Requirements | Apply on the site",
	}
	if join(got) != join(want) {
		t.Fatalf("blocks = %#v", got)
	}
	blob := join(got)
	for _, leak := range []string{"CTgoodjobs", "loginId", "Acme Ltd", "<script", "System Analyst"} {
		if strings.Contains(blob, leak) {
			t.Fatalf("block text contains %s: %s", leak, blob)
		}
	}

	body := `<title>Site Name</title>
<nav><p>NAV-P</p><ul><li>NAV-LI</li></ul></nav>
<header><p>HEAD-P</p></header>
<footer><li>FOOT-LI</li></footer>
<div role="navigation"><p>ROLE-P</p></div>
<p><strong>Key Responsibilities</strong></p>
<ul><li>Ship the work</li></ul>`
	blocks, err = CollectBlocks([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 || blocks[0].Text != "Ship the work" {
		t.Fatalf("body blocks = %#v", blocks)
	}
	blob = blocks[0].Heading + blocks[0].Text
	for _, leak := range []string{"NAV-P", "NAV-LI", "HEAD-P", "FOOT-LI", "ROLE-P", "Site Name"} {
		if strings.Contains(blob, leak) {
			t.Fatalf("chrome in blocks: %s", blob)
		}
	}

	dom := `<script type="application/ld+json">` + jsonLD(t, map[string]any{
		"@type":       "JobPosting",
		"title":       "From JSON",
		"description": "<ul><li>FROM-JSON</li></ul>",
	}) + `</script>
<div data-automation="jobAdDetails">
<p><strong>Job Description</strong></p>
<ul><li>FROM-DOM</li></ul>
</div>`
	blocks, err = CollectBlocks([]byte(dom))
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 || blocks[0].Text != "FROM-DOM" {
		t.Fatalf("jobsdb blocks = %#v", blocks)
	}
}

func jsonLD(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func phraseDoc() Document {
	return Document{
		Source: "asset/SA_jobs.html",
		Header: Header{
			TitleBlock:     "UNIQUE-TITLE",
			Company:        "COMPANY-TOKEN",
			Location:       "Kwun Tong District",
			WorkType:       "Full time",
			Classification: "Business/Systems Analysts",
		},
		Benefits:     []string{"Medical insurance"},
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

func assertGeminiRequest(t *testing.T, cap *captured, key string, blocks []Block, header Header) {
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
	if strings.Contains(cap.body, header.TitleBlock) || strings.Contains(cap.body, header.Company) || strings.Contains(cap.body, "Kwun Tong") {
		t.Fatalf("header text was sent: %s", cap.body)
	}
	var req struct {
		Contents []struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"contents"`
		GenerationConfig struct {
			ResponseSchema struct {
				Properties map[string]json.RawMessage `json:"properties"`
				Required   []string                   `json:"required"`
			} `json:"response_schema"`
		} `json:"generationConfig"`
	}
	if err := json.Unmarshal([]byte(cap.body), &req); err != nil {
		t.Fatal(err)
	}
	if len(req.Contents) != 1 || len(req.Contents[0].Parts) != 1 || !strings.HasPrefix(req.Contents[0].Parts[0].Text, "Split these job-ad blocks into benefits, duties, requirements, and trailer.\n") {
		t.Fatalf("prompt: %s", cap.body)
	}
	wantRequired := []string{"benefits", "duties", "requirements", "trailer"}
	if !slices.Equal(req.GenerationConfig.ResponseSchema.Required, wantRequired) {
		t.Fatalf("schema required: %#v", req.GenerationConfig.ResponseSchema.Required)
	}
	for _, key := range wantRequired {
		if _, ok := req.GenerationConfig.ResponseSchema.Properties[key]; !ok {
			t.Fatalf("schema missing %s: %s", key, cap.body)
		}
	}
	if _, ok := req.GenerationConfig.ResponseSchema.Properties["offer"]; ok {
		t.Fatalf("schema still has offer: %s", cap.body)
	}
	if _, ok := req.GenerationConfig.ResponseSchema.Properties["company"]; ok {
		t.Fatalf("schema has company: %s", cap.body)
	}
	for _, block := range blocks {
		if !strings.Contains(cap.body, block.Text) || !strings.Contains(cap.body, block.Heading) {
			t.Fatalf("body missing block %#v", block)
		}
	}
}

func assertPhraseKept(t *testing.T, got, fallback Document) {
	t.Helper()
	if join(got.Benefits) != join(fallback.Benefits) || join(got.Duties) != join(fallback.Duties) || join(got.Requirements) != join(fallback.Requirements) || join(got.Trailer) != join(fallback.Trailer) {
		t.Fatalf("phrase result replaced: %#v", got)
	}
	if got.Header != fallback.Header || got.Source != fallback.Source {
		t.Fatalf("header or source changed: %#v", got)
	}
}

func TestAnalyzeGeminiFallback(t *testing.T) {
	const key = "gemini-analysis-key"
	const model = `{"must_have":["Know Go"],"advantage":[],"min_years":null,"degree":null,"languages":[],"tech":[{"name":"Go","level":"must","applies_to":null}],"soft_skills":[]}`
	doc := sampleAnalysisDoc()
	doc.Header.Company = "COMPANY-TOKEN"
	prompt, err := PromptText(doc)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("available", func(t *testing.T) {
		cleanKeyring(t)
		if err := StoreGeminiKey(key); err != nil {
			t.Fatal(err)
		}
		calls := 0
		SetFMExecForTest(func(argv []string) ([]byte, []byte, int) {
			calls++
			return nil, []byte("not licensed\n"), 1
		})
		t.Cleanup(restoreFMStub)
		client, cap := testGeminiClient(t, http.StatusOK, geminiTextEnvelope(t, model))
		got, err := Analyze(doc, client)
		if err != nil {
			t.Fatal(err)
		}
		if calls != 1 {
			t.Fatalf("fm calls %d", calls)
		}
		if len(got.MustHave) != 1 || got.MustHave[0] != "Know Go" || got.MinYears != nil || got.Degree != nil {
			t.Fatalf("analysis %#v", got)
		}
		if len(got.Tech) != 1 || got.Tech[0].Name != "Go" || got.Tech[0].Level != levelMust || got.Tech[0].AppliesTo != nil {
			t.Fatalf("tech %#v", got.Tech)
		}
		assertAnalysisRequest(t, cap, key, prompt)
	})

	t.Run("respond", func(t *testing.T) {
		cleanKeyring(t)
		if err := StoreGeminiKey(key); err != nil {
			t.Fatal(err)
		}
		calls := 0
		SetFMExecForTest(func(argv []string) ([]byte, []byte, int) {
			calls++
			if calls == 1 {
				return nil, nil, 0
			}
			return []byte(model), []byte("guardrail rejected\n"), 1
		})
		t.Cleanup(restoreFMStub)
		client, cap := testGeminiClient(t, http.StatusOK, geminiTextEnvelope(t, model))
		got, err := Analyze(doc, client)
		if err != nil || len(got.MustHave) != 1 || got.MustHave[0] != "Know Go" {
			t.Fatalf("got %#v err %v", got, err)
		}
		if calls != 2 {
			t.Fatalf("fm calls %d", calls)
		}
		assertAnalysisRequest(t, cap, key, prompt)
	})
}

func TestAnalyzeFMSuccessSkipsGemini(t *testing.T) {
	cleanKeyring(t)
	keyring.MockInitWithError(errors.New("do-not-read-gemini"))
	t.Cleanup(func() { keyring.MockInit() })
	hits := 0
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		hits++
		return nil, errors.New("gemini called")
	})}
	model := []byte(`{"must_have":["FROM-FM"],"advantage":[],"min_years":null,"degree":null,"languages":[],"tech":[],"soft_skills":[]}`)
	calls := 0
	SetFMExecForTest(func([]string) ([]byte, []byte, int) {
		calls++
		if calls == 1 {
			return nil, nil, 0
		}
		return model, nil, 0
	})
	t.Cleanup(restoreFMStub)
	got, err := Analyze(sampleAnalysisDoc(), client)
	if err != nil || len(got.MustHave) != 1 || got.MustHave[0] != "FROM-FM" || hits != 0 || calls != 2 {
		t.Fatalf("got %#v err %v hits %d calls %d", got, err, hits, calls)
	}
}

func TestAnalyzeGeminiKeepsFMError(t *testing.T) {
	const fmText = "docs-cli: fm: not licensed"
	refuse := func(hits *int) *http.Client {
		return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			*hits++
			return nil, errors.New("gemini called")
		})}
	}
	failFM := func(t *testing.T) {
		t.Helper()
		SetFMExecForTest(func([]string) ([]byte, []byte, int) {
			return nil, []byte("not licensed\n"), 1
		})
		t.Cleanup(restoreFMStub)
	}

	t.Run("missing", func(t *testing.T) {
		cleanKeyring(t)
		hits := 0
		failFM(t)
		_, err := Analyze(sampleAnalysisDoc(), refuse(&hits))
		if err == nil || err.Error() != fmText || hits != 0 {
			t.Fatalf("err %v hits %d", err, hits)
		}
	})

	t.Run("invalid", func(t *testing.T) {
		cleanKeyring(t)
		if err := keyring.Set(keyringService, geminiAccount, `{"api_key":""}`); err != nil {
			t.Fatal(err)
		}
		hits := 0
		failFM(t)
		_, err := Analyze(sampleAnalysisDoc(), refuse(&hits))
		if err == nil || err.Error() != fmText || hits != 0 || strings.Contains(err.Error(), "invalid") {
			t.Fatalf("err %v hits %d", err, hits)
		}
	})

	t.Run("unavailable", func(t *testing.T) {
		const secret = "keyring-boom-secret"
		keyring.MockInitWithError(errors.New(secret))
		t.Cleanup(func() { keyring.MockInit() })
		hits := 0
		failFM(t)
		_, err := Analyze(sampleAnalysisDoc(), refuse(&hits))
		if err == nil || err.Error() != fmText || hits != 0 || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "keyring") {
			t.Fatalf("err %v hits %d", err, hits)
		}
	})
}

func TestAnalyzeGeminiRejects(t *testing.T) {
	const key = "gemini-analysis-key"
	const model = `{"must_have":["Know Go"],"advantage":[],"min_years":null,"degree":null,"languages":[],"tech":[{"name":"Go","level":"must","applies_to":null}],"soft_skills":[]}`
	store := func(t *testing.T) {
		t.Helper()
		cleanKeyring(t)
		if err := StoreGeminiKey(key); err != nil {
			t.Fatal(err)
		}
		SetFMExecForTest(func([]string) ([]byte, []byte, int) {
			return nil, []byte("not licensed\n"), 1
		})
		t.Cleanup(restoreFMStub)
	}

	t.Run("level", func(t *testing.T) {
		store(t)
		bad := strings.Replace(model, `"level":"must"`, `"level":"expert"`, 1)
		client, _ := testGeminiClient(t, http.StatusOK, geminiTextEnvelope(t, bad))
		_, err := Analyze(sampleAnalysisDoc(), client)
		if err == nil || err.Error() != errGeminiAnalysisResponse.Error() || strings.Contains(err.Error(), "expert") || strings.Contains(err.Error(), "fm output") || strings.Contains(err.Error(), "not licensed") {
			t.Fatalf("err %v", err)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		store(t)
		client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, &netTimeout{msg: "dial tcp secret"}
		})}
		_, err := Analyze(sampleAnalysisDoc(), client)
		if !errors.Is(err, errGeminiAnalysisTimeout) || strings.Contains(err.Error(), "dial") || strings.Contains(err.Error(), "not licensed") {
			t.Fatalf("err %v", err)
		}
	})

	t.Run("http", func(t *testing.T) {
		store(t)
		client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("dial tcp secret")
		})}
		_, err := Analyze(sampleAnalysisDoc(), client)
		if !errors.Is(err, errGeminiAnalysisHTTP) || strings.Contains(err.Error(), "dial") || strings.Contains(err.Error(), "not licensed") {
			t.Fatalf("err %v", err)
		}
	})

	t.Run("status", func(t *testing.T) {
		store(t)
		const detail = "This model is currently experiencing high demand. Spikes in demand are usually temporary. Please try again later."
		body := `{"error":{"status":"UNAVAILABLE","message":"` + detail + `","details":["hide-me ` + key + `"]}}`
		client, _ := testGeminiClient(t, http.StatusServiceUnavailable, body)
		_, err := Analyze(sampleAnalysisDoc(), client)
		want := "docs-cli: gemini analysis rejected: status 503\n" +
			"docs-cli: gemini analysis rejected: UNAVAILABLE: " + detail
		if err == nil || err.Error() != want || strings.Contains(err.Error(), "hide-me") || strings.Contains(err.Error(), key) || strings.Contains(err.Error(), "not licensed") || strings.Contains(err.Error(), "partition") || strings.Contains(err.Error(), "blocks") {
			t.Fatalf("err %q", err)
		}
	})
}

func TestAnalyzeGeminiDirect(t *testing.T) {
	const key = "gemini-analysis-key"
	const model = `{"must_have":["Know Go"],"advantage":[],"min_years":null,"degree":null,"languages":[],"tech":[{"name":"Go","level":"must","applies_to":null}],"soft_skills":[]}`
	doc := sampleAnalysisDoc()
	doc.Header.Company = "COMPANY-TOKEN"
	prompt, err := PromptText(doc)
	if err != nil {
		t.Fatal(err)
	}
	succeedFM := func(t *testing.T) *int {
		t.Helper()
		calls := 0
		SetFMExecForTest(func([]string) ([]byte, []byte, int) {
			calls++
			return []byte(model), nil, 0
		})
		t.Cleanup(restoreFMStub)
		return &calls
	}

	t.Run("success", func(t *testing.T) {
		cleanKeyring(t)
		if err := StoreGeminiKey(key); err != nil {
			t.Fatal(err)
		}
		calls := succeedFM(t)
		client, cap := testGeminiClient(t, http.StatusOK, geminiTextEnvelope(t, model))
		got, err := AnalyzeGemini(doc, client)
		if err != nil {
			t.Fatal(err)
		}
		if *calls != 0 {
			t.Fatalf("fm calls %d", *calls)
		}
		if len(got.MustHave) != 1 || got.MustHave[0] != "Know Go" {
			t.Fatalf("analysis %#v", got)
		}
		assertAnalysisRequest(t, cap, key, prompt)
	})

	t.Run("missing key", func(t *testing.T) {
		cleanKeyring(t)
		calls := succeedFM(t)
		hits := 0
		client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			hits++
			return nil, errors.New("gemini called")
		})}
		_, err := AnalyzeGemini(doc, client)
		if err == nil || err.Error() != errGeminiKeyMissing.Error() || hits != 0 || *calls != 0 {
			t.Fatalf("err %v hits %d calls %d", err, hits, *calls)
		}
	})

	t.Run("invalid key", func(t *testing.T) {
		cleanKeyring(t)
		if err := keyring.Set(keyringService, geminiAccount, `{"api_key":""}`); err != nil {
			t.Fatal(err)
		}
		calls := succeedFM(t)
		hits := 0
		client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			hits++
			return nil, errors.New("gemini called")
		})}
		_, err := AnalyzeGemini(doc, client)
		if err == nil || err.Error() != errGeminiKeyInvalid.Error() || hits != 0 || *calls != 0 {
			t.Fatalf("err %v hits %d calls %d", err, hits, *calls)
		}
	})

	t.Run("keyring unavailable", func(t *testing.T) {
		const secret = "keyring-boom-secret"
		keyring.MockInitWithError(errors.New(secret))
		t.Cleanup(func() { keyring.MockInit() })
		calls := succeedFM(t)
		hits := 0
		client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			hits++
			return nil, errors.New("gemini called")
		})}
		_, err := AnalyzeGemini(doc, client)
		if err == nil || err.Error() != errKeyringUnavailable.Error() || hits != 0 || *calls != 0 || strings.Contains(err.Error(), secret) {
			t.Fatalf("err %v hits %d calls %d", err, hits, *calls)
		}
	})

	t.Run("empty requirements", func(t *testing.T) {
		const secret = "do-not-read-gemini"
		keyring.MockInitWithError(errors.New(secret))
		t.Cleanup(func() { keyring.MockInit() })
		calls := succeedFM(t)
		hits := 0
		client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			hits++
			return nil, errors.New("gemini called")
		})}
		for _, empty := range []Document{{}, {Requirements: []string{}}, {Requirements: []string{"person@example.com"}}} {
			got, err := AnalyzeGemini(empty, client)
			if got.MustHave != nil || !errors.Is(err, ErrRequirementsEmpty) || strings.Contains(err.Error(), "keyring") {
				t.Fatalf("doc %#v got %#v err %v", empty.Requirements, got, err)
			}
		}
		if hits != 0 || *calls != 0 {
			t.Fatalf("hits %d calls %d", hits, *calls)
		}
	})

	t.Run("bad level", func(t *testing.T) {
		cleanKeyring(t)
		if err := StoreGeminiKey(key); err != nil {
			t.Fatal(err)
		}
		calls := succeedFM(t)
		bad := strings.Replace(model, `"level":"must"`, `"level":"expert"`, 1)
		client, _ := testGeminiClient(t, http.StatusOK, geminiTextEnvelope(t, bad))
		_, err := AnalyzeGemini(doc, client)
		if err == nil || err.Error() != errGeminiAnalysisResponse.Error() || *calls != 0 || strings.Contains(err.Error(), "fm:") {
			t.Fatalf("err %v calls %d", err, *calls)
		}
	})

	t.Run("status", func(t *testing.T) {
		cleanKeyring(t)
		if err := StoreGeminiKey(key); err != nil {
			t.Fatal(err)
		}
		calls := succeedFM(t)
		const detail = "This model is currently experiencing high demand. Spikes in demand are usually temporary. Please try again later."
		body := `{"error":{"status":"UNAVAILABLE","message":"` + detail + `"}}`
		client, _ := testGeminiClient(t, http.StatusServiceUnavailable, body)
		_, err := AnalyzeGemini(doc, client)
		want := "docs-cli: gemini analysis rejected: status 503\n" +
			"docs-cli: gemini analysis rejected: UNAVAILABLE: " + detail
		if err == nil || err.Error() != want || *calls != 0 || strings.Contains(err.Error(), "docs-cli: fm:") || strings.Contains(err.Error(), key) {
			t.Fatalf("err %q calls %d", err, *calls)
		}
	})
}

func geminiTextEnvelope(t *testing.T, text string) string {
	t.Helper()
	outer := map[string]any{
		"candidates": []any{
			map[string]any{
				"content": map[string]any{
					"parts": []any{map[string]any{"text": text}},
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

func assertAnalysisRequest(t *testing.T, cap *captured, key, prompt string) {
	t.Helper()
	u, err := url.Parse(cap.url)
	if err != nil {
		t.Fatal(err)
	}
	if u.String() != geminiURL || u.RawQuery != "" || !strings.Contains(u.Path, geminiModel) {
		t.Fatalf("url %s", cap.url)
	}
	if strings.Contains(cap.url, key) || strings.Contains(cap.body, key) {
		t.Fatal("api key appeared in the url or body")
	}
	if cap.header.Get("x-goog-api-key") != key {
		t.Fatalf("x-goog-api-key %q", cap.header.Get("x-goog-api-key"))
	}
	for _, banned := range []string{"COMPANY-TOKEN", "LOCATION-TOKEN", "WORKTYPE-TOKEN", "CLASS-TOKEN", "TRAILER-SENTINEL", "source-token", "person@example.com", AnalysisSchema} {
		if strings.Contains(cap.body, banned) {
			t.Fatalf("body contains %q\n%s", banned, cap.body)
		}
	}
	var req struct {
		SystemInstruction struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"systemInstruction"`
		Contents []struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"contents"`
		GenerationConfig struct {
			Mime   string          `json:"response_mime_type"`
			Schema json.RawMessage `json:"response_schema"`
		} `json:"generationConfig"`
	}
	if err := json.Unmarshal([]byte(cap.body), &req); err != nil {
		t.Fatal(err)
	}
	if len(req.SystemInstruction.Parts) != 1 || req.SystemInstruction.Parts[0].Text != AnalysisInstructions {
		t.Fatalf("instructions %#v", req.SystemInstruction.Parts)
	}
	if len(req.Contents) != 1 || len(req.Contents[0].Parts) != 1 || req.Contents[0].Parts[0].Text != prompt {
		t.Fatalf("prompt =\n%s", req.Contents)
	}
	if req.GenerationConfig.Mime != "application/json" {
		t.Fatalf("mime %s", req.GenerationConfig.Mime)
	}
	schema := req.GenerationConfig.Schema
	if bytes.Contains(schema, []byte(`"additionalProperties"`)) || !bytes.Contains(schema, []byte(`"enum":["must","advantage"]`)) {
		t.Fatalf("schema %s", schema)
	}
	if strings.Count(string(schema), `"nullable":true`) != 3 {
		t.Fatalf("nullable %s", schema)
	}
	var parsed struct {
		Required   []string                   `json:"required"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(schema, &parsed); err != nil {
		t.Fatal(err)
	}
	want := []string{"must_have", "advantage", "min_years", "degree", "languages", "tech", "soft_skills"}
	if !slices.Equal(parsed.Required, want) {
		t.Fatalf("required %#v", parsed.Required)
	}
	for _, name := range want {
		if _, ok := parsed.Properties[name]; !ok {
			t.Fatalf("schema missing %s", name)
		}
	}
	if _, ok := parsed.Properties["company"]; ok {
		t.Fatal("schema has company")
	}
}

func TestAutoparse(t *testing.T) {
	const key = "autoparse-library-key"
	const email = "person@example.com"
	const wantInstructions = `Read the saved job advertisement HTML and extract it into docs-cli section JSON.
Put pay, allowances, and gratuity in benefits.
Put responsibilities in duties.
Put entry requirements and preferable attributes in requirements.
Put deadlines, remarks, and other advertisement lines that are none of those three in trailer.
Leave out site navigation, sign-in text, and search chrome.
Fill header title_block, company, location, work_type, and classification from the advertisement. Use an empty string when the page does not state that field. Do not use a portal title that names the website instead of the job.
Copy the advertisement wording. Do not invent employers, dates, or requirements.
The source field may be empty. The program replaces it.`
	if AutoparseInstructions != wantInstructions || strings.Contains(autoparseGeminiSchema, "additionalProperties") {
		t.Fatalf("instructions or schema changed")
	}
	page := []byte(`<script>AUTOPARSE-HTML-SENTINEL</script><p>` + email + `</p>`)
	model := `{
		"source": "model-source",
		"header": {"title_block": "Role", "company": "Acme ` + email + ` Ltd", "location": "", "work_type": "", "classification": ""},
		"benefits": null,
		"duties": ["Buy supplies"],
		"requirements": ["Preferable attributes: Excel helps", "` + email + `"],
		"trailer": []
	}`
	fmCalls := 0
	SetFMExecForTest(func([]string) ([]byte, []byte, int) {
		fmCalls++
		return nil, []byte("fm"), 1
	})
	t.Cleanup(func() {
		SetFMExecForTest(func([]string) ([]byte, []byte, int) {
			return nil, []byte("fm exec not stubbed"), 1
		})
	})

	t.Run("extracts and redacts", func(t *testing.T) {
		cleanKeyring(t)
		if err := StoreGeminiKey(key); err != nil {
			t.Fatal(err)
		}
		client, cap := testGeminiClient(t, http.StatusOK, geminiTextEnvelope(t, model))
		got, err := Autoparse(context.Background(), page, client)
		if err != nil {
			t.Fatal(err)
		}
		if got.Source != "" || got.Header.TitleBlock != "Role" || got.Header.Company != "Acme Ltd" {
			t.Fatalf("doc %#v", got)
		}
		if join(got.Benefits) != "" || join(got.Duties) != "Buy supplies" || join(got.Requirements) != "Preferable attributes: Excel helps" || join(got.Trailer) != "" {
			t.Fatalf("lists %#v", got)
		}
		if got.Benefits == nil || got.Trailer == nil {
			t.Fatal("nil list")
		}
		if fmCalls != 0 {
			t.Fatalf("fm calls %d", fmCalls)
		}
		if strings.Contains(cap.body, email) || strings.Contains(cap.body, key) || strings.Contains(cap.url, key) {
			t.Fatalf("request url %s body %s", cap.url, cap.body)
		}
		if cap.header.Get("x-goog-api-key") != key {
			t.Fatalf("header %q", cap.header.Get("x-goog-api-key"))
		}
		if !strings.Contains(cap.body, "<script>AUTOPARSE-HTML-SENTINEL</script>") {
			t.Fatalf("body %s", cap.body)
		}
		var req struct {
			SystemInstruction struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"systemInstruction"`
			Contents []struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"contents"`
		}
		if err := json.Unmarshal([]byte(cap.body), &req); err != nil {
			t.Fatal(err)
		}
		if len(req.SystemInstruction.Parts) != 1 || req.SystemInstruction.Parts[0].Text != wantInstructions {
			t.Fatalf("instructions %#v", req.SystemInstruction.Parts)
		}
		if len(req.Contents) != 1 || len(req.Contents[0].Parts) != 1 || req.Contents[0].Parts[0].Text != emailRE.ReplaceAllString(string(page), "") {
			t.Fatalf("user text %q", req.Contents)
		}
		if strings.Contains(cap.body, "additionalProperties") || strings.Contains(cap.url, "aiplatform.googleapis.com") {
			t.Fatalf("url %s body %s", cap.url, cap.body)
		}
	})

	t.Run("missing key", func(t *testing.T) {
		cleanKeyring(t)
		hits := 0
		client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			hits++
			return nil, errors.New("called")
		})}
		_, err := Autoparse(context.Background(), page, client)
		if !errors.Is(err, keyring.ErrNotFound) || hits != 0 {
			t.Fatalf("err %v hits %d", err, hits)
		}
	})

	t.Run("not json", func(t *testing.T) {
		cleanKeyring(t)
		if err := StoreGeminiKey(key); err != nil {
			t.Fatal(err)
		}
		client, _ := testGeminiClient(t, http.StatusOK, geminiTextEnvelope(t, "hello"))
		_, err := Autoparse(context.Background(), page, client)
		if !errors.Is(err, errGeminiAutoparseResponse) || strings.Contains(err.Error(), key) {
			t.Fatalf("err %v", err)
		}
	})

	t.Run("missing field", func(t *testing.T) {
		cleanKeyring(t)
		if err := StoreGeminiKey(key); err != nil {
			t.Fatal(err)
		}
		bad := strings.Replace(model, `"title_block": "Role", `, "", 1)
		client, _ := testGeminiClient(t, http.StatusOK, geminiTextEnvelope(t, bad))
		_, err := Autoparse(context.Background(), page, client)
		if !errors.Is(err, errGeminiAutoparseResponse) {
			t.Fatalf("err %v", err)
		}
	})

	t.Run("extra field", func(t *testing.T) {
		cleanKeyring(t)
		if err := StoreGeminiKey(key); err != nil {
			t.Fatal(err)
		}
		bad := strings.Replace(model, `"source": "model-source"`, `"source": "model-source", "offer": []`, 1)
		client, _ := testGeminiClient(t, http.StatusOK, geminiTextEnvelope(t, bad))
		_, err := Autoparse(context.Background(), page, client)
		if !errors.Is(err, errGeminiAutoparseResponse) {
			t.Fatalf("err %v", err)
		}
	})

	t.Run("status 400", func(t *testing.T) {
		cleanKeyring(t)
		if err := StoreGeminiKey(key); err != nil {
			t.Fatal(err)
		}
		const msg = "Request contains an invalid argument."
		body := `{"error":{"status":"INVALID_ARGUMENT","message":"` + msg + `","details":["` + key + `"]}}`
		client, _ := testGeminiClient(t, http.StatusBadRequest, body)
		_, err := Autoparse(context.Background(), page, client)
		want := "docs-cli: gemini autoparse rejected: status 400\n" +
			"docs-cli: gemini autoparse rejected: INVALID_ARGUMENT: " + msg
		if err == nil || err.Error() != want || strings.Contains(err.Error(), key) || strings.Contains(err.Error(), "partition") || strings.Contains(err.Error(), "blocks") {
			t.Fatalf("err %q", err)
		}
	})
}

func TestGeminiModelFromProductionEnv(t *testing.T) {
	t.Cleanup(func() { SetGeminiEnvRoot("") })
	dir := t.TempDir()
	const secret = "client-secret-do-not-send"
	if err := os.WriteFile(filepath.Join(dir, ".env.example"), []byte("GEMINI_MODEL=from-example\nGOOGLE_OAUTH_CLIENT_SECRET="+secret+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	envPath := filepath.Join(dir, ".env")
	writeEnv := func(body string) {
		t.Helper()
		if err := os.WriteFile(envPath, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	t.Chdir(dir)
	writeEnv("GEMINI_MODEL=from-cwd\n")
	SetGeminiEnvRoot("")
	if geminiEndpoint() != geminiURL || activeGeminiModel() != geminiModel {
		t.Fatal("unset root read .env")
	}

	SetGeminiEnvRoot(dir)
	if err := os.Remove(envPath); err != nil {
		t.Fatal(err)
	}
	if activeGeminiModel() != geminiModel || geminiEndpoint() != geminiURL {
		t.Fatal("missing .env")
	}
	if err := os.WriteFile(filepath.Join(dir, "secret.env"), []byte("GEMINI_MODEL=from-link\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("secret.env", envPath); err != nil {
		t.Fatal(err)
	}
	if activeGeminiModel() != geminiModel {
		t.Fatal("followed .env symlink")
	}
	if err := os.Remove(envPath); err != nil {
		t.Fatal(err)
	}

	for _, body := range []string{
		"GOOGLE_OAUTH_CLIENT_SECRET=" + secret + "\n",
		"GEMINI_MODEL=\n",
		"GEMINI_MODEL=   \n",
		"GEMINI_MODEL=\"\"\n",
		"# GEMINI_MODEL=from-comment\n",
	} {
		writeEnv(body)
		if got := activeGeminiModel(); got != geminiModel || geminiEndpoint() != geminiURL {
			t.Fatalf("body %q model %q", body, got)
		}
	}

	writeEnv("GEMINI_MODEL=first\nexport GEMINI_MODEL=\"gemini-test-model\"\nGOOGLE_OAUTH_CLIENT_SECRET=" + secret + "\n")
	if activeGeminiModel() != "gemini-test-model" {
		t.Fatalf("model %q", activeGeminiModel())
	}
	endpoint := geminiEndpoint()
	if endpoint != "https://generativelanguage.googleapis.com/v1beta/models/gemini-test-model:generateContent" || strings.Contains(endpoint, secret) || strings.Contains(endpoint, "from-example") {
		t.Fatalf("endpoint %s", endpoint)
	}

	cleanKeyring(t)
	const key = "gemini-test-key"
	if err := StoreGeminiKey(key); err != nil {
		t.Fatal(err)
	}
	blocks := []Block{{Heading: "What we offer", Text: "Medical insurance"}}
	client, cap := testGeminiClient(t, http.StatusOK, geminiEnvelope(t, sectionMapping{Benefits: []string{"Medical insurance"}}))
	if _, err := MapSections(context.Background(), phraseDoc(), blocks, client); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(cap.url)
	if err != nil {
		t.Fatal(err)
	}
	if u.String() != endpoint || u.RawQuery != "" || u.Host != "generativelanguage.googleapis.com" || strings.Contains(cap.url, key) || strings.Contains(cap.url, secret) || strings.Contains(cap.body, secret) {
		t.Fatalf("url %s", cap.url)
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
