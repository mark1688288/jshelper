package jobad

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/zalando/go-keyring"
	"golang.org/x/net/html"
)

const (
	keyringService = "docs-cli"
	geminiAccount  = "gemini-api-key"

	// geminiModel is the Gemini API model id used when GEMINI_MODEL is missing
	// or empty in the production .env file.
	geminiModel    = "gemini-3.8-flash"
	geminiModelKey = "GEMINI_MODEL"
	// geminiThinkingLevel is generationConfig.thinkingConfig.thinkingLevel on
	// every generateContent call. HIGH is the Gemini API enum
	// genai.ThinkingLevelHigh. Thoughts are not requested.
	geminiThinkingLevel = "HIGH"
	geminiURL           = "https://generativelanguage.googleapis.com/v1beta/models/" + geminiModel + ":generateContent"

	geminiTimeout        = 300 * time.Second
	maxKeyringBytes      = 4096
	maxGeminiResponse    = 1 << 20
	maxRejectRunes       = 120
	maxGeminiErrorBody   = 8192
	maxStatusDetailRunes = 240
)

var (
	errKeyringUnavailable = errors.New("docs-cli: keyring unavailable")
	errGeminiKeyInvalid   = errors.New("docs-cli: keyring item is invalid; run docs-cli gemini-key")
	errKeyringItemTooBig  = errors.New("docs-cli: keyring item exceeds the 4096 byte command limit")
	errGeminiTimeout      = errors.New("docs-cli: gemini mapping rejected: timeout")
	errGeminiHTTP         = errors.New("docs-cli: gemini mapping rejected: http")
	errGeminiResponse     = errors.New("docs-cli: gemini mapping rejected: response")
	errGeminiBlocks       = errors.New("docs-cli: gemini mapping rejected: blocks")

	errGeminiKeyMissing       = errors.New("docs-cli: gemini api key is not in the keyring")
	errGeminiAnalysisTimeout  = errors.New("docs-cli: gemini analysis rejected: timeout")
	errGeminiAnalysisHTTP     = errors.New("docs-cli: gemini analysis rejected: http")
	errGeminiAnalysisResponse = errors.New("docs-cli: gemini analysis rejected: response")

	errGeminiAutoparseTimeout  = errors.New("docs-cli: gemini autoparse rejected: timeout")
	errGeminiAutoparseHTTP     = errors.New("docs-cli: gemini autoparse rejected: http")
	errGeminiAutoparseResponse = errors.New("docs-cli: gemini autoparse rejected: response")
)

// geminiEnvRoot is the directory whose .env supplies GEMINI_MODEL.
// The CLI sets it to the working directory. Empty skips the file.
var geminiEnvRoot string

// SetGeminiEnvRoot selects the directory of the production .env file.
// An empty directory uses geminiModel and does not open a file.
func SetGeminiEnvRoot(dir string) {
	geminiEnvRoot = dir
}

func geminiEndpoint() string {
	model := activeGeminiModel()
	if model == geminiModel {
		return geminiURL
	}
	return "https://generativelanguage.googleapis.com/v1beta/models/" + url.PathEscape(model) + ":generateContent"
}

// activeGeminiModel reads GEMINI_MODEL from <root>/.env.
// A missing file, a missing key, or a blank value uses geminiModel.
// .env.example is not read.
func activeGeminiModel() string {
	if geminiEnvRoot == "" {
		return geminiModel
	}
	value, ok := geminiModelValue(filepath.Join(geminiEnvRoot, ".env"))
	value = strings.TrimSpace(value)
	if !ok || value == "" {
		return geminiModel
	}
	return value
}

func geminiModelValue(path string) (string, bool) {
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
		if !ok || key != geminiModelKey {
			continue
		}
		found = true
		value = unquoteEnvValue(val)
	}
	return value, found
}

func unquoteEnvValue(val string) string {
	if len(val) >= 2 {
		last := val[len(val)-1]
		if (val[0] == '"' && last == '"') || (val[0] == '\'' && last == '\'') {
			return val[1 : len(val)-1]
		}
	}
	return val
}

// geminiCall selects the stderr prefix for one Gemini request.
type geminiCall int

const (
	geminiMapping geminiCall = iota
	geminiAnalysis
	geminiAutoparse
)

func (c geminiCall) prefix() string {
	switch c {
	case geminiAnalysis:
		return "docs-cli: gemini analysis rejected: "
	case geminiAutoparse:
		return "docs-cli: gemini autoparse rejected: "
	default:
		return "docs-cli: gemini mapping rejected: "
	}
}

func (c geminiCall) timeoutErr() error {
	switch c {
	case geminiAnalysis:
		return errGeminiAnalysisTimeout
	case geminiAutoparse:
		return errGeminiAutoparseTimeout
	default:
		return errGeminiTimeout
	}
}

func (c geminiCall) httpErr() error {
	switch c {
	case geminiAnalysis:
		return errGeminiAnalysisHTTP
	case geminiAutoparse:
		return errGeminiAutoparseHTTP
	default:
		return errGeminiHTTP
	}
}

func (c geminiCall) responseErr() error {
	switch c {
	case geminiAnalysis:
		return errGeminiAnalysisResponse
	case geminiAutoparse:
		return errGeminiAutoparseResponse
	default:
		return errGeminiResponse
	}
}

// analysisGeminiSchema is the Gemini response schema for Analysis.
// It is not the fm --schema dialect. Gemini rejects additionalProperties,
// so that field is omitted. ParseAnalysis still rejects unknown fields
// and a tech.level other than must or advantage.
const analysisGeminiSchema = `{"type":"OBJECT","properties":{"must_have":{"type":"ARRAY","items":{"type":"STRING"}},"advantage":{"type":"ARRAY","items":{"type":"STRING"}},"min_years":{"type":"INTEGER","nullable":true},"degree":{"type":"STRING","nullable":true},"languages":{"type":"ARRAY","items":{"type":"STRING"}},"tech":{"type":"ARRAY","items":{"type":"OBJECT","properties":{"name":{"type":"STRING"},"level":{"type":"STRING","enum":["must","advantage"]},"applies_to":{"type":"STRING","nullable":true}},"required":["name","level","applies_to"]}},"soft_skills":{"type":"ARRAY","items":{"type":"STRING"}}},"required":["must_have","advantage","min_years","degree","languages","tech","soft_skills"]}`

// AutoparseInstructions is the systemInstruction for parse --autoparse.
// The bytes match the design. The user part is the saved HTML, not these sentences.
const AutoparseInstructions = `Read the saved job advertisement HTML and extract it into docs-cli section JSON.
Put pay, allowances, and gratuity in benefits.
Put responsibilities in duties.
Put entry requirements and preferable attributes in requirements.
Put deadlines, remarks, and other advertisement lines that are none of those three in trailer.
Leave out site navigation, sign-in text, and search chrome.
Fill header title_block, company, location, work_type, and classification from the advertisement. Use an empty string when the page does not state that field. Do not use a portal title that names the website instead of the job.
Copy the advertisement wording. Do not invent employers, dates, or requirements.
The source field may be empty. The program replaces it.`

// autoparseGeminiSchema is the Gemini response schema for a section document.
// Gemini rejects additionalProperties, so that field is omitted.
// decodeAutoparse still rejects unknown fields and missing keys.
const autoparseGeminiSchema = `{"type":"OBJECT","properties":{"source":{"type":"STRING"},"header":{"type":"OBJECT","properties":{"title_block":{"type":"STRING"},"company":{"type":"STRING"},"location":{"type":"STRING"},"work_type":{"type":"STRING"},"classification":{"type":"STRING"}},"required":["title_block","company","location","work_type","classification"]},"benefits":{"type":"ARRAY","items":{"type":"STRING"}},"duties":{"type":"ARRAY","items":{"type":"STRING"}},"requirements":{"type":"ARRAY","items":{"type":"STRING"}},"trailer":{"type":"ARRAY","items":{"type":"STRING"}}},"required":["source","header","benefits","duties","requirements","trailer"]}`

// rejectError is a stderr line that may wrap a cause without including the cause text.
type rejectError struct {
	msg   string
	cause error
}

func (e *rejectError) Error() string { return e.msg }

func (e *rejectError) Unwrap() error { return e.cause }

type geminiKey struct {
	APIKey string `json:"api_key"`
}

// Block is one li or non-heading paragraph from the same scope as Parse.
// Heading is the preceding heading, with emails removed. Text is the block.
type Block struct {
	Heading string
	Text    string
}

type sectionMapping struct {
	Benefits     []string `json:"benefits"`
	Duties       []string `json:"duties"`
	Requirements []string `json:"requirements"`
	Trailer      []string `json:"trailer"`
}

// StoreGeminiKey writes {"api_key"} to docs-cli / gemini-api-key.
// An empty key does not call Set and does not delete an existing item.
func StoreGeminiKey(key string) error {
	if key == "" {
		return nil
	}
	payload, err := json.Marshal(geminiKey{APIKey: key})
	if err != nil {
		return errKeyringUnavailable
	}
	if len(payload) > maxKeyringBytes {
		return errKeyringItemTooBig
	}
	if err := keyring.Set(keyringService, geminiAccount, string(payload)); err != nil {
		if errors.Is(err, keyring.ErrSetDataTooBig) || strings.Contains(err.Error(), "4096") {
			return errKeyringItemTooBig
		}
		return errKeyringUnavailable
	}
	return nil
}

// LoadGeminiKey reads the Gemini API key.
// keyring.ErrNotFound means the item is absent. Other keyring errors are
// errKeyringUnavailable and do not include the system text.
func LoadGeminiKey() (string, error) {
	raw, err := keyring.Get(keyringService, geminiAccount)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return "", err
		}
		return "", errKeyringUnavailable
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	var item geminiKey
	if err := dec.Decode(&item); err != nil {
		return "", errGeminiKeyInvalid
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return "", errGeminiKeyInvalid
	}
	if item.APIKey == "" {
		return "", errGeminiKeyInvalid
	}
	return item.APIKey, nil
}

// CollectBlocks lists li items and non-heading paragraphs in the same scope
// as Parse. A jobAdDetails page collects only that element. Otherwise it
// collects the JSON-LD description, the Taleo initialHistory job HTML when
// the visible page has no requirements, or the body paragraphs outside nav,
// header, footer, and role=navigation. Emails are removed. Header fields
// are not collected. A page Parse rejects returns the blocks stderr line.
func CollectBlocks(page []byte) ([]Block, error) {
	root, err := html.Parse(strings.NewReader(string(page)))
	if err != nil {
		return nil, blockReject(err)
	}
	if _, err := parseRoot(root); err != nil {
		return nil, blockReject(err)
	}
	scope, err := scopeFor(root)
	if err != nil || scope.node == nil {
		return []Block{}, nil
	}
	var blocks []Block
	if scope.taleo {
		walkBlocksTaleo(scope.node, "", &blocks)
		return blocks, nil
	}
	walkBlocks(scope.node, "", &blocks, scope.skipChrome)
	return blocks, nil
}

func walkBlocksTaleo(n *html.Node, heading string, blocks *[]Block) string {
	if n.Type == html.ElementNode {
		switch n.Data {
		case "script", "style", "svg":
			return heading
		case "li":
			appendBlock(blocks, heading, textOf(n))
			return heading
		case "p":
			return walkBlockTaleoP(n, heading, blocks)
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		heading = walkBlocksTaleo(c, heading, blocks)
	}
	return heading
}

func walkBlockTaleoP(n *html.Node, heading string, blocks *[]Block) string {
	raw := textOf(n)
	norm := normalizeHeading(raw)
	if utf8.RuneCountInString(norm) <= maxHeadingRunes {
		if _, ok := classify(norm); ok {
			return raw
		}
		if taleoRequirementHeading(norm) {
			return raw
		}
	}
	if _, ok := taleoGapLabel(n, norm); ok {
		if taleoLabelHasBody(raw) {
			appendBlock(blocks, heading, raw)
		}
		return raw
	}
	appendBlock(blocks, heading, raw)
	return heading
}

func walkBlocks(n *html.Node, heading string, blocks *[]Block, skipChrome bool) string {
	if n.Type == html.ElementNode {
		if skipChrome && chrome(n) {
			return heading
		}
		switch n.Data {
		case "script", "style", "svg":
			return heading
		case "li":
			appendBlock(blocks, heading, textOf(n))
			return heading
		case "strong":
			if _, ok := strongSection(textOf(n)); ok {
				return textOf(n)
			}
			return heading
		case "p":
			return walkBlockP(n, heading, blocks, skipChrome)
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		heading = walkBlocks(c, heading, blocks, skipChrome)
	}
	return heading
}

func walkBlockP(n *html.Node, heading string, blocks *[]Block, skipChrome bool) string {
	raw := textOf(n)
	norm := normalizeHeading(raw)
	if utf8.RuneCountInString(norm) <= maxHeadingRunes {
		if _, ok := classify(norm); ok {
			return raw
		}
	}
	if containsHeading(n) {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			heading = walkBlocks(c, heading, blocks, skipChrome)
		}
		return heading
	}
	appendBlock(blocks, heading, raw)
	return heading
}

func appendBlock(blocks *[]Block, heading, text string) {
	text = redact(text)
	if text == "" {
		return
	}
	*blocks = append(*blocks, Block{Heading: redact(heading), Text: text})
}

func blockReject(cause error) error {
	return &rejectError{msg: errGeminiBlocks.Error(), cause: cause}
}

// MapSections asks Gemini once to partition blocks into the four section arrays.
// The key is sent only in the x-goog-api-key header. Header and Source of
// fallback stay as given. A missing key or keyring failure returns that error.
// A transport failure, bad response, or inexact partition returns fallback and
// an error whose Error text is the stderr reason. The text does not include
// the HTTP body, the transport error, or the API key.
func MapSections(ctx context.Context, fallback Document, blocks []Block, client *http.Client) (Document, error) {
	key, err := LoadGeminiKey()
	if err != nil {
		return fallback, err
	}
	if client == nil {
		client = &http.Client{Timeout: geminiTimeout}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, geminiTimeout)
	defer cancel()

	mapped, err := requestMapping(ctx, client, key, blocks)
	if err != nil {
		return fallback, err
	}
	if !partitionOK(blocks, mapped) {
		return fallback, partitionRejectError(key, blocks, mapped)
	}
	fallback.Benefits = cloneStrings(mapped.Benefits)
	fallback.Duties = cloneStrings(mapped.Duties)
	fallback.Requirements = cloneStrings(mapped.Requirements)
	fallback.Trailer = cloneStrings(mapped.Trailer)
	return fallback, nil
}

func requestMapping(ctx context.Context, client *http.Client, key string, blocks []Block) (sectionMapping, error) {
	payload, err := geminiRequestBody(mappingPrompt(blocks))
	if err != nil {
		return sectionMapping{}, errGeminiResponse
	}
	body, err := postGemini(ctx, client, key, payload, geminiMapping)
	if err != nil {
		return sectionMapping{}, err
	}
	mapped, err := parseMapping(body)
	if err != nil {
		return sectionMapping{}, errGeminiResponse
	}
	return mapped, nil
}

// requestAnalysis asks Gemini once for an Analysis object.
// The key is sent only in the x-goog-api-key header.
// prompt is GeminiJD. The instruction bytes are GeminiAnalysisInstructions.
// A transport failure or a body that is not Analysis returns an analysis
// rejection. The text does not include the HTTP body, the transport error,
// or the API key. There is no partition reason and no blocks reason.
func requestAnalysis(ctx context.Context, client *http.Client, key, prompt string) (Analysis, error) {
	if client == nil {
		client = &http.Client{Timeout: geminiTimeout}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, geminiTimeout)
	defer cancel()
	payload, err := analysisRequestBody(prompt)
	if err != nil {
		return Analysis{}, errGeminiAnalysisResponse
	}
	raw, err := postGemini(ctx, client, key, payload, geminiAnalysis)
	if err != nil {
		return Analysis{}, err
	}
	text, err := geminiCandidateText(raw)
	if err != nil {
		return Analysis{}, errGeminiAnalysisResponse
	}
	parsed, err := ParseAnalysis([]byte(text))
	if err != nil {
		return Analysis{}, errGeminiAnalysisResponse
	}
	return parsed, nil
}

func postGemini(ctx context.Context, client *http.Client, key string, payload []byte, call geminiCall) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, geminiEndpoint(), bytes.NewReader(payload))
	if err != nil {
		return nil, call.responseErr()
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", key)
	resp, err := client.Do(req)
	if err != nil {
		return nil, geminiTransportError(err, call)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		errBody, readErr := io.ReadAll(io.LimitReader(resp.Body, maxGeminiErrorBody))
		if readErr != nil {
			errBody = nil
		}
		return nil, geminiStatusError(resp.StatusCode, errBody, key, call)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxGeminiResponse+1))
	if err != nil || len(body) > maxGeminiResponse {
		return nil, call.responseErr()
	}
	return body, nil
}

func geminiTransportError(err error, call geminiCall) error {
	if geminiTimedOut(err) {
		return call.timeoutErr()
	}
	return call.httpErr()
}

func geminiTimedOut(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var timeout net.Error
	return errors.As(err, &timeout) && timeout.Timeout()
}

func geminiStatusError(code int, body []byte, key string, call geminiCall) error {
	prefix := call.prefix()
	first := prefix + "status " + strconv.Itoa(code)
	suffix, ok := geminiErrorSuffix(body)
	if !ok {
		return errors.New(first)
	}
	if key != "" && strings.Contains(suffix, key) {
		return errors.New(first)
	}
	if utf8.RuneCountInString(suffix) > maxStatusDetailRunes {
		suffix = string([]rune(suffix)[:maxStatusDetailRunes])
	}
	second := prefix + suffix
	if key != "" && strings.Contains(second, key) {
		return errors.New(first)
	}
	return errors.New(first + "\n" + second)
}

// geminiErrorSuffix reads only the string fields error.status and error.message.
func geminiErrorSuffix(body []byte) (string, bool) {
	if len(body) == 0 {
		return "", false
	}
	var env struct {
		Error struct {
			Status  json.RawMessage `json:"status"`
			Message json.RawMessage `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return "", false
	}
	status, statusOK := rawJSONString(env.Error.Status)
	message, messageOK := rawJSONString(env.Error.Message)
	status = strings.Join(strings.Fields(status), " ")
	message = strings.Join(strings.Fields(message), " ")
	switch {
	case statusOK && status != "" && messageOK && message != "":
		return status + ": " + message, true
	case messageOK && message != "":
		return message, true
	case statusOK && status != "":
		return status, true
	default:
		return "", false
	}
}

func rawJSONString(raw json.RawMessage) (string, bool) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

func analysisRequestBody(prompt string) ([]byte, error) {
	body := map[string]any{
		"systemInstruction": map[string]any{
			"parts": []any{map[string]any{"text": GeminiAnalysisInstructions}},
		},
		"contents": []any{
			map[string]any{
				"role":  "user",
				"parts": []any{map[string]any{"text": prompt}},
			},
		},
		"generationConfig": geminiGenerationConfig(json.RawMessage(analysisGeminiSchema)),
	}
	return json.Marshal(body)
}

func geminiRequestBody(prompt string) ([]byte, error) {
	schema := json.RawMessage(`{
		"type": "OBJECT",
		"properties": {
			"benefits": {"type": "ARRAY", "items": {"type": "STRING"}},
			"duties": {"type": "ARRAY", "items": {"type": "STRING"}},
			"requirements": {"type": "ARRAY", "items": {"type": "STRING"}},
			"trailer": {"type": "ARRAY", "items": {"type": "STRING"}}
		},
		"required": ["benefits", "duties", "requirements", "trailer"]
	}`)
	body := map[string]any{
		"contents": []any{
			map[string]any{
				"role":  "user",
				"parts": []any{map[string]any{"text": prompt}},
			},
		},
		"generationConfig": geminiGenerationConfig(schema),
	}
	return json.Marshal(body)
}

// geminiGenerationConfig is the generationConfig for one generateContent call.
func geminiGenerationConfig(schema json.RawMessage) map[string]any {
	return map[string]any{
		"response_mime_type": "application/json",
		"response_schema":    schema,
		"thinkingConfig": map[string]any{
			"thinkingLevel": geminiThinkingLevel,
		},
	}
}

func mappingPrompt(blocks []Block) string {
	var b strings.Builder
	b.WriteString("Split these job-ad blocks into benefits, duties, requirements, and trailer.\n")
	b.WriteString("Copy each block text exactly once into one array. Do not add, drop, merge, or rewrite a block.\n")
	b.WriteString("Headings are context only and are not blocks.\n\n")
	for _, block := range blocks {
		b.WriteString("Heading: ")
		b.WriteString(block.Heading)
		b.WriteByte('\n')
		b.WriteString("Block: ")
		b.WriteString(block.Text)
		b.WriteString("\n\n")
	}
	return b.String()
}

func parseMapping(raw []byte) (sectionMapping, error) {
	text, err := geminiCandidateText(raw)
	if err != nil {
		return sectionMapping{}, err
	}
	dec := json.NewDecoder(strings.NewReader(text))
	dec.DisallowUnknownFields()
	var mapped sectionMapping
	if err := dec.Decode(&mapped); err != nil {
		return sectionMapping{}, errGeminiResponse
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return sectionMapping{}, errGeminiResponse
	}
	return mapped, nil
}

func geminiCandidateText(raw []byte) (string, error) {
	var env struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text    string `json:"text"`
					Thought bool   `json:"thought"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(raw, &env); err != nil || len(env.Candidates) == 0 {
		return "", errGeminiResponse
	}
	var text strings.Builder
	for _, part := range env.Candidates[0].Content.Parts {
		if part.Thought {
			continue
		}
		text.WriteString(part.Text)
	}
	return text.String(), nil
}

func partitionOK(blocks []Block, mapped sectionMapping) bool {
	want := make(map[string]int, len(blocks))
	for _, block := range blocks {
		want[block.Text]++
	}
	got := map[string]int{}
	n := 0
	for _, items := range [][]string{mapped.Benefits, mapped.Duties, mapped.Requirements, mapped.Trailer} {
		for _, item := range items {
			got[item]++
			n++
		}
	}
	if n != len(blocks) {
		return false
	}
	for text, count := range want {
		if got[text] != count {
			return false
		}
	}
	return true
}

func partitionRejectError(key string, blocks []Block, mapped sectionMapping) error {
	sentCount := make(map[string]int, len(blocks))
	for _, block := range blocks {
		sentCount[block.Text]++
	}
	replyCount := map[string]int{}
	replyN := 0
	var replyOrder []string
	for _, items := range [][]string{mapped.Benefits, mapped.Duties, mapped.Requirements, mapped.Trailer} {
		for _, item := range items {
			replyCount[item]++
			replyOrder = append(replyOrder, item)
			replyN++
		}
	}
	missingN := 0
	for text, n := range sentCount {
		if got := replyCount[text]; n > got {
			missingN += n - got
		}
	}
	extraN := 0
	for text, n := range replyCount {
		if got := sentCount[text]; n > got {
			extraN += n - got
		}
	}
	var b strings.Builder
	b.WriteString("docs-cli: gemini mapping rejected: partition\n")
	fmt.Fprintf(&b, "docs-cli: gemini mapping rejected: sent=%d reply=%d missing=%d extra=%d", len(blocks), replyN, missingN, extraN)
	sentOrder := make([]string, len(blocks))
	for i, block := range blocks {
		sentOrder[i] = block.Text
	}
	appendRejectSamples(&b, "missing", surplusTexts(sentOrder, replyCount), key)
	appendRejectSamples(&b, "extra", surplusTexts(replyOrder, sentCount), key)
	return errors.New(b.String())
}

// surplusTexts lists texts in order whose occurrence was not covered by have.
func surplusTexts(order []string, have map[string]int) []string {
	left := make(map[string]int, len(have))
	for text, n := range have {
		left[text] = n
	}
	var out []string
	for _, text := range order {
		if left[text] > 0 {
			left[text]--
			continue
		}
		out = append(out, text)
	}
	return out
}

func appendRejectSamples(b *strings.Builder, kind string, texts []string, key string) {
	printed := 0
	for _, text := range texts {
		collapsed := strings.Join(strings.Fields(text), " ")
		shown := collapsed
		if utf8.RuneCountInString(shown) > maxRejectRunes {
			shown = string([]rune(shown)[:maxRejectRunes])
		}
		if key != "" && (strings.Contains(collapsed, key) || strings.Contains(shown, key)) {
			continue
		}
		b.WriteByte('\n')
		b.WriteString("docs-cli: gemini mapping rejected: ")
		b.WriteString(kind)
		b.WriteString(": ")
		b.WriteString(shown)
		printed++
		if printed == 3 {
			return
		}
	}
}

func cloneStrings(in []string) []string {
	if len(in) == 0 {
		return []string{}
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}

// Autoparse asks Gemini once to extract section JSON from saved HTML.
// Emails are removed from the page text before it is sent. Markup stays.
// The key is sent only in the x-goog-api-key header. Source is left empty.
// A missing key or keyring failure returns that error and does not call Gemini.
// There is no phrase-classification fallback. The error text does not include
// the HTTP body, the transport error, or the API key.
func Autoparse(ctx context.Context, page []byte, client *http.Client) (Document, error) {
	key, err := LoadGeminiKey()
	if err != nil {
		return Document{}, err
	}
	if client == nil {
		client = &http.Client{Timeout: geminiTimeout}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, geminiTimeout)
	defer cancel()
	payload, err := autoparseRequestBody(emailRE.ReplaceAllString(string(page), ""))
	if err != nil {
		return Document{}, errGeminiAutoparseResponse
	}
	raw, err := postGemini(ctx, client, key, payload, geminiAutoparse)
	if err != nil {
		return Document{}, err
	}
	text, err := geminiCandidateText(raw)
	if err != nil {
		return Document{}, errGeminiAutoparseResponse
	}
	return decodeAutoparse(text)
}

func autoparseRequestBody(page string) ([]byte, error) {
	body := map[string]any{
		"systemInstruction": map[string]any{
			"parts": []any{map[string]any{"text": AutoparseInstructions}},
		},
		"contents": []any{
			map[string]any{
				"role":  "user",
				"parts": []any{map[string]any{"text": page}},
			},
		},
		"generationConfig": geminiGenerationConfig(json.RawMessage(autoparseGeminiSchema)),
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(body); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func decodeAutoparse(text string) (Document, error) {
	dec := json.NewDecoder(strings.NewReader(text))
	dec.DisallowUnknownFields()
	var raw struct {
		Source       json.RawMessage `json:"source"`
		Header       json.RawMessage `json:"header"`
		Benefits     json.RawMessage `json:"benefits"`
		Duties       json.RawMessage `json:"duties"`
		Requirements json.RawMessage `json:"requirements"`
		Trailer      json.RawMessage `json:"trailer"`
	}
	if err := dec.Decode(&raw); err != nil {
		return Document{}, errGeminiAutoparseResponse
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return Document{}, errGeminiAutoparseResponse
	}
	if _, err := decodeRequiredString(raw.Source); err != nil {
		return Document{}, errGeminiAutoparseResponse
	}
	header, err := decodeAutoparseHeader(raw.Header)
	if err != nil {
		return Document{}, errGeminiAutoparseResponse
	}
	benefits, err := decodeStringList(raw.Benefits)
	if err != nil {
		return Document{}, errGeminiAutoparseResponse
	}
	duties, err := decodeStringList(raw.Duties)
	if err != nil {
		return Document{}, errGeminiAutoparseResponse
	}
	requirements, err := decodeStringList(raw.Requirements)
	if err != nil {
		return Document{}, errGeminiAutoparseResponse
	}
	trailer, err := decodeStringList(raw.Trailer)
	if err != nil {
		return Document{}, errGeminiAutoparseResponse
	}
	redactHeader(&header)
	return Document{
		Header:       header,
		Benefits:     redactList(benefits),
		Duties:       redactList(duties),
		Requirements: redactList(requirements),
		Trailer:      redactList(trailer),
	}, nil
}

func decodeAutoparseHeader(raw json.RawMessage) (Header, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return Header{}, errGeminiAutoparseResponse
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var fields struct {
		TitleBlock     json.RawMessage `json:"title_block"`
		Company        json.RawMessage `json:"company"`
		Location       json.RawMessage `json:"location"`
		WorkType       json.RawMessage `json:"work_type"`
		Classification json.RawMessage `json:"classification"`
	}
	if err := dec.Decode(&fields); err != nil {
		return Header{}, errGeminiAutoparseResponse
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return Header{}, errGeminiAutoparseResponse
	}
	title, err := decodeRequiredString(fields.TitleBlock)
	if err != nil {
		return Header{}, err
	}
	company, err := decodeRequiredString(fields.Company)
	if err != nil {
		return Header{}, err
	}
	location, err := decodeRequiredString(fields.Location)
	if err != nil {
		return Header{}, err
	}
	work, err := decodeRequiredString(fields.WorkType)
	if err != nil {
		return Header{}, err
	}
	class, err := decodeRequiredString(fields.Classification)
	if err != nil {
		return Header{}, err
	}
	return Header{
		TitleBlock:     title,
		Company:        company,
		Location:       location,
		WorkType:       work,
		Classification: class,
	}, nil
}

func decodeRequiredString(raw json.RawMessage) (string, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return "", errGeminiAutoparseResponse
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	var s string
	if err := dec.Decode(&s); err != nil {
		return "", errGeminiAutoparseResponse
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return "", errGeminiAutoparseResponse
	}
	return s, nil
}

func decodeStringList(raw json.RawMessage) ([]string, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, errGeminiAutoparseResponse
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return []string{}, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var items []string
	if err := dec.Decode(&items); err != nil {
		return nil, errGeminiAutoparseResponse
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errGeminiAutoparseResponse
	}
	if items == nil {
		items = []string{}
	}
	return items, nil
}

func redactList(items []string) []string {
	out := []string{}
	for _, item := range items {
		item = redact(item)
		if item == "" {
			continue
		}
		out = append(out, item)
	}
	return out
}
