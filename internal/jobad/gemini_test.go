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
