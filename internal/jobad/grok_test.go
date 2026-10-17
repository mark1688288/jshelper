package jobad

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestStoreAndLoadGrokKey(t *testing.T) {
	cleanKeyring(t)
	if err := keyring.Set(keyringService, "refresh-token", `{"refresh_token":"keep"}`); err != nil {
		t.Fatal(err)
	}
	if err := StoreGeminiKey("gemini-stays"); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadGrokKey(); !errors.Is(err, keyring.ErrNotFound) {
		t.Fatalf("missing key err = %v", err)
	}
	if err := StoreGrokKey(""); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadGrokKey(); !errors.Is(err, keyring.ErrNotFound) {
		t.Fatalf("empty store created an item: %v", err)
	}
	if err := StoreGrokKey("grok-test-key"); err != nil {
		t.Fatal(err)
	}
	if err := StoreGrokKey(""); err != nil {
		t.Fatal(err)
	}
	got, err := LoadGrokKey()
	if err != nil || got != "grok-test-key" {
		t.Fatalf("key = %q err %v", got, err)
	}
	raw, err := keyring.Get(keyringService, grokAccount)
	if err != nil || raw != `{"api_key":"grok-test-key"}` {
		t.Fatalf("stored %q err %v", raw, err)
	}
	gemini, err := LoadGeminiKey()
	if err != nil || gemini != "gemini-stays" {
		t.Fatalf("gemini key %q err %v", gemini, err)
	}
	refresh, err := keyring.Get(keyringService, "refresh-token")
	if err != nil || refresh != `{"refresh_token":"keep"}` {
		t.Fatalf("refresh %q err %v", refresh, err)
	}
}

func TestLoadGrokKeyInvalid(t *testing.T) {
	cleanKeyring(t)
	for _, raw := range []string{
		"not-json",
		`{"api_key":""}`,
		`{"api_key":"k","extra":1}`,
		`{"api_key":"k"}{"api_key":"two"}`,
	} {
		if err := keyring.Set(keyringService, grokAccount, raw); err != nil {
			t.Fatal(err)
		}
		_, err := LoadGrokKey()
		if !errors.Is(err, errGrokKeyInvalid) || err.Error() != "docs-cli: keyring item is invalid; run docs-cli grok-key" {
			t.Fatalf("raw %s err %v", raw, err)
		}
	}
}

func TestStoreGrokKeyTooBig(t *testing.T) {
	cleanKeyring(t)
	overhead := len(`{"api_key":""}`)
	fit := strings.Repeat("a", maxKeyringBytes-overhead)
	if err := StoreGrokKey(fit); err != nil {
		t.Fatal(err)
	}
	too := fit + "b"
	err := StoreGrokKey(too)
	if !errors.Is(err, errKeyringItemTooBig) || err.Error() != "docs-cli: keyring item exceeds the 4096 byte command limit" || strings.Contains(err.Error(), too) {
		t.Fatalf("err = %v", err)
	}
	got, err := LoadGrokKey()
	if err != nil || got != fit {
		t.Fatalf("oversize overwrote key err %v", err)
	}
}

func TestAnalyzeGrok(t *testing.T) {
	const key = "grok-library-key"
	const company = "COMPANY-TOKEN"
	doc := Document{
		Source: "source-token-9f3a.html",
		Header: Header{
			TitleBlock:     "System Analyst",
			Company:        company,
			Location:       "LOCATION-TOKEN",
			WorkType:       "WORKTYPE-TOKEN",
			Classification: "CLASS-TOKEN",
		},
		Benefits:     []string{"Medical person@example.com"},
		Requirements: []string{"Know Go", "React (For Frontend)"},
		Trailer:      []string{"TRAILER-SENTINEL"},
	}
	model := `{"must_have":["Know Go"],"advantage":[],"min_years":null,"degree":null,"languages":[],"tech":[{"name":"Go","level":"must","applies_to":"For Frontend"}],"soft_skills":[]}`
	prompt, err := GeminiJD(doc)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("completed", func(t *testing.T) {
		cleanKeyring(t)
		if err := StoreGrokKey(key); err != nil {
			t.Fatal(err)
		}
		calls := grokFMCalls(t)
		client, cap := grokClient(t, http.StatusOK, grokEnvelope(t, model, "REASONING-SECRET"))
		got, err := AnalyzeGrok(doc, client)
		if err != nil || len(got.MustHave) != 1 || got.MustHave[0] != "Know Go" || *calls != 0 {
			t.Fatalf("got %#v err %v calls %d", got, err, *calls)
		}
		if strings.Contains(got.MustHave[0], "REASONING-SECRET") {
			t.Fatal("kept reasoning text")
		}
		assertGrokRequest(t, cap, key, prompt)
	})

	t.Run("reasoning skipped", func(t *testing.T) {
		cleanKeyring(t)
		if err := StoreGrokKey(key); err != nil {
			t.Fatal(err)
		}
		body := `{
			"status":"completed",
			"output":[
				{"type":"reasoning","encrypted_content":"REASONING-SECRET","content":[{"type":"output_text","text":"not-json"}]},
				{"type":"message","content":[{"type":"output_text","text":` + jsonString(t, model) + `}]}
			]
		}`
		client, _ := grokClient(t, http.StatusOK, body)
		got, err := AnalyzeGrok(doc, client)
		if err != nil || len(got.MustHave) != 1 || got.MustHave[0] != "Know Go" || strings.Contains(errString(err), "REASONING-SECRET") {
			t.Fatalf("got %#v err %v", got, err)
		}
	})

	t.Run("missing key", func(t *testing.T) {
		cleanKeyring(t)
		hits := 0
		client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			hits++
			return nil, errors.New("grok called")
		})}
		_, err := AnalyzeGrok(doc, client)
		if !errors.Is(err, errGrokKeyMissing) || err.Error() != "docs-cli: grok api key is not in the keyring" || hits != 0 {
			t.Fatalf("err %v hits %d", err, hits)
		}
	})

	t.Run("empty requirements", func(t *testing.T) {
		keyring.MockInitWithError(errors.New("do-not-read-grok"))
		t.Cleanup(func() { keyring.MockInit() })
		hits := 0
		client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			hits++
			return nil, errors.New("grok called")
		})}
		for _, empty := range []Document{{}, {Requirements: []string{}}, {Requirements: []string{"person@example.com"}}} {
			got, err := AnalyzeGrok(empty, client)
			if got.MustHave != nil || !errors.Is(err, ErrRequirementsEmpty) || strings.Contains(err.Error(), "keyring") {
				t.Fatalf("doc %#v got %#v err %v", empty.Requirements, got, err)
			}
		}
		if hits != 0 {
			t.Fatalf("hits %d", hits)
		}
	})

	t.Run("bad level", func(t *testing.T) {
		cleanKeyring(t)
		if err := StoreGrokKey(key); err != nil {
			t.Fatal(err)
		}
		bad := `{"must_have":[],"advantage":[],"min_years":null,"degree":null,"languages":[],"tech":[{"name":"Go","level":"expert","applies_to":null}],"soft_skills":[]}`
		client, _ := grokClient(t, http.StatusOK, grokEnvelope(t, bad, ""))
		_, err := AnalyzeGrok(doc, client)
		if err == nil || err.Error() != errGrokAnalysisResponse.Error() || strings.Contains(err.Error(), "expert") || strings.Contains(err.Error(), "fm:") {
			t.Fatalf("err %v", err)
		}
	})

	t.Run("status 401", func(t *testing.T) {
		cleanKeyring(t)
		if err := StoreGrokKey(key); err != nil {
			t.Fatal(err)
		}
		body := `{"error":{"type":"auth_error","message":"bad credentials","code":"` + key + `"}}`
		client, _ := grokClient(t, http.StatusUnauthorized, body)
		_, err := AnalyzeGrok(doc, client)
		want := "docs-cli: grok analysis rejected: status 401\n" +
			"docs-cli: grok analysis rejected: auth_error: bad credentials"
		if err == nil || err.Error() != want || strings.Contains(err.Error(), key) || strings.Contains(err.Error(), "docs-cli: fm:") {
			t.Fatalf("err %q", err)
		}
	})

	t.Run("key in message", func(t *testing.T) {
		cleanKeyring(t)
		if err := StoreGrokKey(key); err != nil {
			t.Fatal(err)
		}
		body := `{"error":{"message":"rejected ` + key + `"}}`
		client, _ := grokClient(t, http.StatusUnauthorized, body)
		_, err := AnalyzeGrok(doc, client)
		if err == nil || err.Error() != "docs-cli: grok analysis rejected: status 401" || strings.Contains(err.Error(), key) {
			t.Fatalf("err %q", err)
		}
	})

	t.Run("message only", func(t *testing.T) {
		cleanKeyring(t)
		if err := StoreGrokKey(key); err != nil {
			t.Fatal(err)
		}
		body := `{"error":{"message":"bad   key\nplease","status":"HIDDEN"}}`
		client, _ := grokClient(t, http.StatusBadRequest, body)
		_, err := AnalyzeGrok(doc, client)
		want := "docs-cli: grok analysis rejected: status 400\n" +
			"docs-cli: grok analysis rejected: bad key please"
		if err == nil || err.Error() != want || strings.Contains(err.Error(), "HIDDEN") {
			t.Fatalf("err %q", err)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		cleanKeyring(t)
		if err := StoreGrokKey(key); err != nil {
			t.Fatal(err)
		}
		const leak = "UPSTREAM-ERR dial tcp secret"
		client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, &netTimeout{msg: leak}
		})}
		_, err := AnalyzeGrok(doc, client)
		if !errors.Is(err, errGrokAnalysisTimeout) || strings.Contains(err.Error(), leak) {
			t.Fatalf("err %v", err)
		}
	})

	t.Run("http", func(t *testing.T) {
		cleanKeyring(t)
		if err := StoreGrokKey(key); err != nil {
			t.Fatal(err)
		}
		const leak = "UPSTREAM-ERR dial tcp secret"
		client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New(leak)
		})}
		_, err := AnalyzeGrok(doc, client)
		if !errors.Is(err, errGrokAnalysisHTTP) || strings.Contains(err.Error(), leak) {
			t.Fatalf("err %v", err)
		}
	})

	t.Run("not completed", func(t *testing.T) {
		cleanKeyring(t)
		if err := StoreGrokKey(key); err != nil {
			t.Fatal(err)
		}
		body := strings.Replace(grokEnvelope(t, model, ""), `"status":"completed"`, `"status":"incomplete"`, 1)
		client, _ := grokClient(t, http.StatusOK, body)
		_, err := AnalyzeGrok(doc, client)
		if err == nil || err.Error() != errGrokAnalysisResponse.Error() || strings.Contains(err.Error(), "Know Go") {
			t.Fatalf("err %v", err)
		}
	})
}

func TestGrokModelFromProductionEnv(t *testing.T) {
	t.Cleanup(func() { SetGeminiEnvRoot("") })
	dir := t.TempDir()
	const secret = "client-secret-do-not-send"
	if err := os.WriteFile(filepath.Join(dir, ".env.example"), []byte("GROK_MODEL=from-example\nGEMINI_MODEL=from-example\nGOOGLE_OAUTH_CLIENT_SECRET="+secret+"\n"), 0o644); err != nil {
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
	writeEnv("GROK_MODEL=from-cwd\n")
	SetGeminiEnvRoot("")
	if activeGrokModel() != grokModel {
		t.Fatal("unset root read .env")
	}

	SetGeminiEnvRoot(dir)
	if err := os.Remove(envPath); err != nil {
		t.Fatal(err)
	}
	if activeGrokModel() != grokModel {
		t.Fatal("missing .env")
	}
	if err := os.WriteFile(filepath.Join(dir, "secret.env"), []byte("GROK_MODEL=from-link\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("secret.env", envPath); err != nil {
		t.Fatal(err)
	}
	if activeGrokModel() != grokModel {
		t.Fatal("followed .env symlink")
	}
	if err := os.Remove(envPath); err != nil {
		t.Fatal(err)
	}

	for _, body := range []string{
		"GOOGLE_OAUTH_CLIENT_SECRET=" + secret + "\n",
		"GROK_MODEL=\n",
		"GROK_MODEL=   \n",
		"GROK_MODEL=\"\"\n",
		"# GROK_MODEL=from-comment\n",
		"GEMINI_MODEL=gemini-test-model\n",
	} {
		writeEnv(body)
		if got := activeGrokModel(); got != grokModel {
			t.Fatalf("body %q model %q", body, got)
		}
	}

	writeEnv("GEMINI_MODEL=gemini-test-model\n")
	if activeGeminiModel() != "gemini-test-model" || geminiEndpoint() == geminiURL {
		t.Fatal("gemini model was ignored")
	}
	if activeGrokModel() != grokModel {
		t.Fatal("grok read GEMINI_MODEL")
	}

	writeEnv("GROK_MODEL=grok-only\n")
	if activeGeminiModel() != geminiModel || geminiEndpoint() != geminiURL {
		t.Fatalf("gemini read GROK_MODEL: %s", geminiEndpoint())
	}

	writeEnv("GROK_MODEL=first\nexport GROK_MODEL=\"grok-test-model\"\r\nGEMINI_MODEL=gemini-test-model\nGOOGLE_OAUTH_CLIENT_SECRET=" + secret + "\n")
	if activeGrokModel() != "grok-test-model" || activeGeminiModel() != "gemini-test-model" {
		t.Fatalf("grok %q gemini %q", activeGrokModel(), activeGeminiModel())
	}

	cleanKeyring(t)
	const key = "grok-test-key"
	if err := StoreGrokKey(key); err != nil {
		t.Fatal(err)
	}
	doc := Document{Requirements: []string{"Know Go"}}
	model := `{"must_have":["Know Go"],"advantage":[],"min_years":null,"degree":null,"languages":[],"tech":[],"soft_skills":[]}`
	client, cap := grokClient(t, http.StatusOK, grokEnvelope(t, model, ""))
	if _, err := AnalyzeGrok(doc, client); err != nil {
		t.Fatal(err)
	}
	if cap.url != grokURL || strings.Contains(cap.url, "grok-test-model") || strings.Contains(cap.url, "gemini-test-model") || strings.Contains(cap.url, secret) || strings.Contains(cap.body, secret) {
		t.Fatalf("url %s", cap.url)
	}
	var req struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal([]byte(cap.body), &req); err != nil {
		t.Fatal(err)
	}
	if req.Model != "grok-test-model" {
		t.Fatalf("model %q", req.Model)
	}
}

func grokFMCalls(t *testing.T) *int {
	t.Helper()
	calls := 0
	SetFMExecForTest(func([]string) ([]byte, []byte, int) {
		calls++
		return nil, []byte("fm"), 1
	})
	t.Cleanup(func() {
		SetFMExecForTest(func([]string) ([]byte, []byte, int) {
			return nil, []byte("fm exec not stubbed"), 1
		})
	})
	return &calls
}

func grokEnvelope(t *testing.T, object, reasoning string) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"id":     "resp-test",
		"status": "completed",
		"output": []any{
			map[string]any{
				"type":              "reasoning",
				"encrypted_content": reasoning,
			},
			map[string]any{
				"type": "message",
				"role": "assistant",
				"content": []any{
					map[string]any{"type": "output_text", "text": object},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func jsonString(t *testing.T, s string) string {
	t.Helper()
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func grokClient(t *testing.T, status int, response string) (*http.Client, *captured) {
	t.Helper()
	cap := &captured{}
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		cap.url = r.URL.String()
		cap.header = r.Header.Clone()
		cap.body = string(body)
		return &http.Response{
			StatusCode: status,
			Body:       io.NopCloser(strings.NewReader(response)),
			Header:     make(http.Header),
			Request:    r,
		}, nil
	})}
	return client, cap
}

func assertGrokRequest(t *testing.T, cap *captured, key, prompt string) {
	t.Helper()
	if cap.url != grokURL || strings.Contains(cap.url, key) || strings.Contains(cap.body, key) {
		t.Fatalf("url %s", cap.url)
	}
	if cap.header.Get("Authorization") != "Bearer "+key || cap.header.Get("x-goog-api-key") != "" {
		t.Fatalf("header %#v", cap.header)
	}
	if cap.header.Get("Content-Type") != "application/json" {
		t.Fatalf("content-type %q", cap.header.Get("Content-Type"))
	}
	for _, banned := range []string{"COMPANY-TOKEN", "LOCATION-TOKEN", "WORKTYPE-TOKEN", "CLASS-TOKEN", "TRAILER-SENTINEL", "source-token-9f3a.html", "person@example.com", `"company"`} {
		if strings.Contains(cap.body, banned) {
			t.Fatalf("body contains %q\n%s", banned, cap.body)
		}
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(cap.body), &top); err != nil {
		t.Fatal(err)
	}
	for name := range top {
		switch name {
		case "model", "store", "reasoning", "input", "text":
		default:
			t.Fatalf("extra field %s", name)
		}
	}
	var req struct {
		Model     string `json:"model"`
		Store     bool   `json:"store"`
		Reasoning struct {
			Effort string `json:"effort"`
		} `json:"reasoning"`
		Input []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"input"`
		Text struct {
			Format struct {
				Type   string          `json:"type"`
				Name   string          `json:"name"`
				Strict bool            `json:"strict"`
				Schema json.RawMessage `json:"schema"`
			} `json:"format"`
		} `json:"text"`
	}
	if err := json.Unmarshal([]byte(cap.body), &req); err != nil {
		t.Fatal(err)
	}
	if req.Model != grokModel || req.Store || req.Reasoning.Effort != "high" {
		t.Fatalf("model %q store %v effort %q", req.Model, req.Store, req.Reasoning.Effort)
	}
	if !strings.Contains(cap.body, `"store":false`) {
		t.Fatalf("store missing\n%s", cap.body)
	}
	if len(req.Input) != 2 || req.Input[0].Role != "system" || req.Input[0].Content != GeminiAnalysisInstructions || req.Input[1].Role != "user" || req.Input[1].Content != prompt {
		t.Fatalf("input roles %#v", req.Input)
	}
	if req.Text.Format.Type != "json_schema" || req.Text.Format.Name != "analysis" || !req.Text.Format.Strict {
		t.Fatalf("format %#v", req.Text.Format)
	}
	schema := req.Text.Format.Schema
	if bytesContains(schema, `"additionalProperties"`) || !bytesContains(schema, `"enum":["must","advantage"]`) {
		t.Fatalf("schema %s", schema)
	}
}

func bytesContains(b []byte, s string) bool {
	return strings.Contains(string(b), s)
}
