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

	// geminiModel is the Gemini API model id for section mapping.
	geminiModel = "gemini-3.1-flash-lite"
	geminiURL   = "https://generativelanguage.googleapis.com/v1beta/models/" + geminiModel + ":generateContent"

	geminiTimeout        = 60 * time.Second
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
)

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

// Block is one li or non-heading paragraph from jobAdDetails.
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

// CollectBlocks lists li items and non-heading paragraphs in jobAdDetails.
// Each block keeps the heading text that preceded it. Emails are removed.
// Header fields are not collected. A failure's Error text is the blocks
// stderr line and does not include the parser or sentinel text.
func CollectBlocks(page []byte) ([]Block, error) {
	root, err := html.Parse(strings.NewReader(string(page)))
	if err != nil {
		return nil, blockReject(err)
	}
	body := findAutomation(root, "jobAdDetails")
	if body == nil {
		return nil, blockReject(ErrJobAdDetailsNotFound)
	}
	var blocks []Block
	walkBlocks(body, "", &blocks)
	return blocks, nil
}

func walkBlocks(n *html.Node, heading string, blocks *[]Block) string {
	if n.Type == html.ElementNode {
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
			return walkBlockP(n, heading, blocks)
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		heading = walkBlocks(c, heading, blocks)
	}
	return heading
}

func walkBlockP(n *html.Node, heading string, blocks *[]Block) string {
	raw := textOf(n)
	norm := normalizeHeading(raw)
	if utf8.RuneCountInString(norm) <= maxHeadingRunes {
		if _, ok := classify(norm); ok {
			return raw
		}
	}
	if containsHeading(n) {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			heading = walkBlocks(c, heading, blocks)
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
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, geminiURL, bytes.NewReader(payload))
	if err != nil {
		return sectionMapping{}, errGeminiResponse
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", key)
	resp, err := client.Do(req)
	if err != nil {
		return sectionMapping{}, geminiTransportError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		errBody, readErr := io.ReadAll(io.LimitReader(resp.Body, maxGeminiErrorBody))
		if readErr != nil {
			errBody = nil
		}
		return sectionMapping{}, geminiStatusError(resp.StatusCode, errBody, key)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxGeminiResponse+1))
	if err != nil || len(body) > maxGeminiResponse {
		return sectionMapping{}, errGeminiResponse
	}
	mapped, err := parseMapping(body)
	if err != nil {
		return sectionMapping{}, errGeminiResponse
	}
	return mapped, nil
}

func geminiTransportError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return errGeminiTimeout
	}
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		return errGeminiTimeout
	}
	return errGeminiHTTP
}

func geminiStatusError(code int, body []byte, key string) error {
	first := "docs-cli: gemini mapping rejected: status " + strconv.Itoa(code)
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
	second := "docs-cli: gemini mapping rejected: " + suffix
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
		"generationConfig": map[string]any{
			"response_mime_type": "application/json",
			"response_schema":    schema,
		},
	}
	return json.Marshal(body)
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
	var env struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(raw, &env); err != nil || len(env.Candidates) == 0 {
		return sectionMapping{}, errGeminiResponse
	}
	var text strings.Builder
	for _, part := range env.Candidates[0].Content.Parts {
		text.WriteString(part.Text)
	}
	dec := json.NewDecoder(strings.NewReader(text.String()))
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
