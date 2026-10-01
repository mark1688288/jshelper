package jobad

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/zalando/go-keyring"
	"golang.org/x/net/html"
)

const (
	keyringService = "docs-cli"
	geminiAccount  = "gemini-api-key"

	// geminiModel is the stable Gemini Flash id in the Gemini API models guide.
	geminiModel = "gemini-3.8-flash"
	geminiURL   = "https://generativelanguage.googleapis.com/v1beta/models/" + geminiModel + ":generateContent"

	geminiTimeout     = 60 * time.Second
	maxKeyringBytes   = 4096
	maxGeminiResponse = 1 << 20
)

var (
	errKeyringUnavailable    = errors.New("docs-cli: keyring unavailable")
	errGeminiKeyInvalid      = errors.New("docs-cli: keyring item is invalid; run docs-cli gemini-key")
	errKeyringItemTooBig     = errors.New("docs-cli: keyring item exceeds the 4096 byte command limit")
	errGeminiMappingRejected = errors.New("docs-cli: gemini mapping rejected")
)

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
	Offer        []string `json:"offer"`
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
// Header fields are not collected.
func CollectBlocks(page []byte) ([]Block, error) {
	root, err := html.Parse(strings.NewReader(string(page)))
	if err != nil {
		return nil, err
	}
	body := findAutomation(root, "jobAdDetails")
	if body == nil {
		return nil, ErrJobAdDetailsNotFound
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

// MapSections asks Gemini once to partition blocks into the four section arrays.
// The key is sent only in the x-goog-api-key header. Header and Source of
// fallback stay as given. A missing key, keyring failure, HTTP failure, or a
// reply that is not an exact partition of the block texts returns fallback.
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
	if err != nil || !partitionOK(blocks, mapped) {
		return fallback, errGeminiMappingRejected
	}
	fallback.Offer = cloneStrings(mapped.Offer)
	fallback.Duties = cloneStrings(mapped.Duties)
	fallback.Requirements = cloneStrings(mapped.Requirements)
	fallback.Trailer = cloneStrings(mapped.Trailer)
	return fallback, nil
}

func requestMapping(ctx context.Context, client *http.Client, key string, blocks []Block) (sectionMapping, error) {
	payload, err := geminiRequestBody(mappingPrompt(blocks))
	if err != nil {
		return sectionMapping{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, geminiURL, bytes.NewReader(payload))
	if err != nil {
		return sectionMapping{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", key)
	resp, err := client.Do(req)
	if err != nil {
		return sectionMapping{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxGeminiResponse+1))
	if err != nil || resp.StatusCode != http.StatusOK || len(body) > maxGeminiResponse {
		return sectionMapping{}, errors.New("rejected")
	}
	return parseMapping(body)
}

func geminiRequestBody(prompt string) ([]byte, error) {
	schema := json.RawMessage(`{
		"type": "OBJECT",
		"properties": {
			"offer": {"type": "ARRAY", "items": {"type": "STRING"}},
			"duties": {"type": "ARRAY", "items": {"type": "STRING"}},
			"requirements": {"type": "ARRAY", "items": {"type": "STRING"}},
			"trailer": {"type": "ARRAY", "items": {"type": "STRING"}}
		},
		"required": ["offer", "duties", "requirements", "trailer"]
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
	b.WriteString("Split these job-ad blocks into offer, duties, requirements, and trailer.\n")
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
		return sectionMapping{}, errors.New("rejected")
	}
	var text strings.Builder
	for _, part := range env.Candidates[0].Content.Parts {
		text.WriteString(part.Text)
	}
	dec := json.NewDecoder(strings.NewReader(text.String()))
	dec.DisallowUnknownFields()
	var mapped sectionMapping
	if err := dec.Decode(&mapped); err != nil {
		return sectionMapping{}, err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return sectionMapping{}, err
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
	for _, items := range [][]string{mapped.Offer, mapped.Duties, mapped.Requirements, mapped.Trailer} {
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

func cloneStrings(in []string) []string {
	if len(in) == 0 {
		return []string{}
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}
