package jobad

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/zalando/go-keyring"
)

const (
	grokAccount = "grok-api-key"

	// grokModel is the xAI model id used when GROK_MODEL is missing or empty
	// in the production .env file.
	grokModel    = "grok-4.7"
	grokModelKey = "GROK_MODEL"
	grokURL      = "https://api.x.ai/v1/responses"

	grokRejectPrefix = "docs-cli: grok analysis rejected: "
)

var (
	errGrokKeyInvalid = errors.New("docs-cli: keyring item is invalid; run docs-cli grok-key")
	errGrokKeyMissing = errors.New("docs-cli: grok api key is not in the keyring")

	errGrokAnalysisTimeout  = errors.New(grokRejectPrefix + "timeout")
	errGrokAnalysisHTTP     = errors.New(grokRejectPrefix + "http")
	errGrokAnalysisResponse = errors.New(grokRejectPrefix + "response")
)

// grokAnalysisSchema is the Responses API JSON Schema for Analysis.
// It is not analysisGeminiSchema. xAI does not accept OBJECT, ARRAY, or
// nullable. additionalProperties is omitted and stays at its default, false.
// ParseAnalysis still rejects unknown fields and a tech.level other than
// must or advantage.
const grokAnalysisSchema = `{"type":"object","properties":{"must_have":{"type":"array","items":{"type":"string"}},"advantage":{"type":"array","items":{"type":"string"}},"min_years":{"type":["integer","null"]},"degree":{"type":["string","null"]},"languages":{"type":"array","items":{"type":"string"}},"tech":{"type":"array","items":{"type":"object","properties":{"name":{"type":"string"},"level":{"type":"string","enum":["must","advantage"]},"applies_to":{"type":["string","null"]}},"required":["name","level","applies_to"]}},"soft_skills":{"type":"array","items":{"type":"string"}}},"required":["must_have","advantage","min_years","degree","languages","tech","soft_skills"]}`

type grokKey struct {
	APIKey string `json:"api_key"`
}

// StoreGrokKey writes {"api_key"} to docs-cli / grok-api-key.
// An empty key does not call Set and does not delete an existing item.
// It does not read or write gemini-api-key or refresh-token.
func StoreGrokKey(key string) error {
	if key == "" {
		return nil
	}
	payload, err := json.Marshal(grokKey{APIKey: key})
	if err != nil {
		return errKeyringUnavailable
	}
	if len(payload) > maxKeyringBytes {
		return errKeyringItemTooBig
	}
	if err := keyring.Set(keyringService, grokAccount, string(payload)); err != nil {
		if errors.Is(err, keyring.ErrSetDataTooBig) || strings.Contains(err.Error(), "4096") {
			return errKeyringItemTooBig
		}
		return errKeyringUnavailable
	}
	return nil
}

// LoadGrokKey reads the Grok API key.
// keyring.ErrNotFound means the item is absent. Other keyring errors are
// errKeyringUnavailable and do not include the system text.
func LoadGrokKey() (string, error) {
	raw, err := keyring.Get(keyringService, grokAccount)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return "", err
		}
		return "", errKeyringUnavailable
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	var item grokKey
	if err := dec.Decode(&item); err != nil {
		return "", errGrokKeyInvalid
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return "", errGrokKeyInvalid
	}
	if item.APIKey == "" {
		return "", errGrokKeyInvalid
	}
	return item.APIKey, nil
}

// AnalyzeGrok asks Grok for an Analysis and does not call fm or Gemini.
// The user message is GeminiJD. The system text is GeminiAnalysisInstructions.
// The Grok API key must already be stored. A missing key is errGrokKeyMissing.
// AnalyzeGrok does not prompt and does not write the keyring. An empty
// requirements list returns ErrRequirementsEmpty and does not read the key.
// A Grok failure is not retried and is not sent to fm or Gemini.
// client nil uses the default HTTP client. Tests pass a client that does not
// call api.x.ai.
func AnalyzeGrok(doc Document, client *http.Client) (Analysis, error) {
	text, err := GeminiJD(doc)
	if err != nil {
		return Analysis{}, err
	}
	key, err := LoadGrokKey()
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return Analysis{}, errGrokKeyMissing
		}
		return Analysis{}, err
	}
	return requestGrokAnalysis(client, key, text)
}

// activeGrokModel reads GROK_MODEL from the directory selected by
// SetGeminiEnvRoot. A missing file, a symlink, a missing key, or a blank
// value uses grokModel. .env.example is not read. GEMINI_MODEL is not read.
// The value is not escaped for a URL.
func activeGrokModel() string {
	if geminiEnvRoot == "" {
		return grokModel
	}
	value, ok := grokModelValue(filepath.Join(geminiEnvRoot, ".env"))
	value = strings.TrimSpace(value)
	if !ok || value == "" {
		return grokModel
	}
	return value
}

func grokModelValue(path string) (string, bool) {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	found := false
	value := ""
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasSuffix(line, "\r") {
			line = strings.TrimSuffix(line, "\r")
		}
		line = strings.TrimLeft(line, " \t\v\f")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, val, ok := strings.Cut(line, "=")
		if !ok || key != grokModelKey {
			continue
		}
		found = true
		value = unquoteEnvValue(val)
	}
	return value, found
}

func requestGrokAnalysis(client *http.Client, key, prompt string) (Analysis, error) {
	if client == nil {
		client = &http.Client{Timeout: geminiTimeout}
	}
	ctx, cancel := context.WithTimeout(context.Background(), geminiTimeout)
	defer cancel()
	payload, err := grokRequestBody(prompt)
	if err != nil {
		return Analysis{}, errGrokAnalysisResponse
	}
	raw, err := postGrok(ctx, client, key, payload)
	if err != nil {
		return Analysis{}, err
	}
	text, err := grokOutputText(raw)
	if err != nil {
		return Analysis{}, err
	}
	parsed, err := ParseAnalysis([]byte(text))
	if err != nil {
		return Analysis{}, errGrokAnalysisResponse
	}
	return parsed, nil
}

func grokRequestBody(prompt string) ([]byte, error) {
	body := struct {
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
	}{
		Model: activeGrokModel(),
		Store: false,
	}
	body.Reasoning.Effort = "high"
	body.Input = []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}{
		{Role: "system", Content: GeminiAnalysisInstructions},
		{Role: "user", Content: prompt},
	}
	body.Text.Format.Type = "json_schema"
	body.Text.Format.Name = "analysis"
	body.Text.Format.Strict = true
	body.Text.Format.Schema = json.RawMessage(grokAnalysisSchema)
	return json.Marshal(body)
}

func postGrok(ctx context.Context, client *http.Client, key string, payload []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, grokURL, bytes.NewReader(payload))
	if err != nil {
		return nil, errGrokAnalysisResponse
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := client.Do(req)
	if err != nil {
		if geminiTimedOut(err) {
			return nil, errGrokAnalysisTimeout
		}
		return nil, errGrokAnalysisHTTP
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		errBody, readErr := io.ReadAll(io.LimitReader(resp.Body, maxGeminiErrorBody))
		if readErr != nil {
			errBody = nil
		}
		return nil, grokStatusError(resp.StatusCode, errBody, key)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxGeminiResponse+1))
	if err != nil || len(body) > maxGeminiResponse {
		return nil, errGrokAnalysisResponse
	}
	return body, nil
}

func grokStatusError(code int, body []byte, key string) error {
	first := grokRejectPrefix + "status " + strconv.Itoa(code)
	suffix, ok := grokErrorSuffix(body)
	if !ok {
		return errors.New(first)
	}
	if key != "" && strings.Contains(suffix, key) {
		return errors.New(first)
	}
	if utf8.RuneCountInString(suffix) > maxStatusDetailRunes {
		suffix = string([]rune(suffix)[:maxStatusDetailRunes])
	}
	second := grokRejectPrefix + suffix
	if key != "" && strings.Contains(second, key) {
		return errors.New(first)
	}
	return errors.New(first + "\n" + second)
}

// grokErrorSuffix reads only the string fields error.type and error.message.
func grokErrorSuffix(body []byte) (string, bool) {
	if len(body) == 0 {
		return "", false
	}
	var env struct {
		Error struct {
			Type    json.RawMessage `json:"type"`
			Message json.RawMessage `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return "", false
	}
	kind, kindOK := rawJSONString(env.Error.Type)
	message, messageOK := rawJSONString(env.Error.Message)
	kind = strings.Join(strings.Fields(kind), " ")
	message = strings.Join(strings.Fields(message), " ")
	switch {
	case kindOK && kind != "" && messageOK && message != "":
		return kind + ": " + message, true
	case messageOK && message != "":
		return message, true
	default:
		return "", false
	}
}

// grokOutputText returns the single output_text from a completed response.
// Reasoning items are ignored. Their encrypted_content is not kept.
func grokOutputText(raw []byte) (string, error) {
	var env struct {
		Status string `json:"status"`
		Output []struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(raw, &env); err != nil || env.Status != "completed" {
		return "", errGrokAnalysisResponse
	}
	var text string
	messages := 0
	for _, item := range env.Output {
		if item.Type == "reasoning" {
			continue
		}
		if item.Type != "message" {
			return "", errGrokAnalysisResponse
		}
		messages++
		texts := 0
		for _, part := range item.Content {
			if part.Type != "output_text" {
				return "", errGrokAnalysisResponse
			}
			texts++
			text = part.Text
		}
		if texts != 1 || text == "" {
			return "", errGrokAnalysisResponse
		}
	}
	if messages != 1 {
		return "", errGrokAnalysisResponse
	}
	return text, nil
}
