package jobad

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/ledongthuc/pdf"
)

const (
	// maxTemplateBytes is the only input cap for a template PDF.
	// GetPlainText inflates compressed streams before a reader limit can apply.
	maxTemplateBytes = 1048576
	// kernArgMax is this Mac's kern.argmax. The slack is reserved for apple[].
	kernArgMax = 1048576
	fmArgSlack = 131072
	fmPtrBytes = 8
)

// ErrTemplateTooLarge is returned when a template file is over 1 MiB.
// The file is not parsed.
var ErrTemplateTooLarge = errors.New("docs-cli: template exceeds 1 MiB")

// ErrTemplateNoText is returned when a template is not a regular file,
// cannot be read, or has no text after whitespace is collapsed.
var ErrTemplateNoText = errors.New("docs-cli: template has no text")

// ErrFMArgLimit is returned when the respond argv would not fit in kern.argmax.
// fm is not called.
var ErrFMArgLimit = errors.New("docs-cli: fm text exceeds the argument limit")

// ErrDraftSchema is returned when stdout is not one letter object.
var ErrDraftSchema = errors.New("docs-cli: fm output is not the draft schema")

// DraftInstructions is the fm --instructions argument for a cover letter.
// The template is a pattern. Title, duties, and requirements are this job.
const DraftInstructions = `The template is a pattern for structure and voice only, not text to paste. Keep the template's language, salutation, closing, paragraph shape, and the applicant's name and contact lines. Rewrite the body for this job's Duties and Requirements. Do not invent an employer, date, degree, or years of experience that appears in neither the template nor the JSON. Do not address this job with an employer from the template's previous role. Title is this job's title. The letter field is the whole letter and nothing else.`

// LetterSchema is the fm --schema argument accepted on 2026-10-02.
// It is the compact byte string, not the pretty-printed object dialect.
const LetterSchema = `{"x-order":["letter"],"title":"Letter","additionalProperties":false,"required":["letter"],"properties":{"letter":{"type":"string"}},"type":"object"}`

// draftEnviron is the environment counted toward the argv limit.
// The child inherits the process environment, so the default is os.Environ.
var draftEnviron = os.Environ

// ExtractTemplateText reads the plain text of a template PDF.
// Stat errors are returned unchanged. The text keeps its newlines.
func ExtractTemplateText(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.Size() > maxTemplateBytes {
		return "", ErrTemplateTooLarge
	}
	if !info.Mode().IsRegular() {
		return "", ErrTemplateNoText
	}
	f, err := os.Open(path)
	if err != nil {
		return "", ErrTemplateNoText
	}
	defer f.Close()
	return readTemplate(f, info.Size())
}

func readTemplate(f io.ReaderAt, size int64) (text string, err error) {
	defer func() {
		if recover() != nil {
			text = ""
			err = ErrTemplateNoText
		}
	}()
	reader, err := pdf.NewReader(f, size)
	if err != nil {
		return "", ErrTemplateNoText
	}
	plain, err := reader.GetPlainText()
	if err != nil {
		return "", ErrTemplateNoText
	}
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(plain); err != nil {
		return "", ErrTemplateNoText
	}
	raw := buf.String()
	if len(strings.Fields(raw)) == 0 {
		return "", ErrTemplateNoText
	}
	return raw, nil
}

// CheckDraftRequirements reports whether any requirement remains after email removal.
// It does not open a PDF or call fm.
func CheckDraftRequirements(doc Document) error {
	if len(promptLines(doc.Requirements)) == 0 {
		return ErrRequirementsEmpty
	}
	return nil
}

// DraftPrompt builds the fm --text value for a cover letter.
// templateText is copied unchanged. Job emails are removed.
// An empty requirements list returns ErrRequirementsEmpty.
func DraftPrompt(doc Document, templateText string) (string, error) {
	if err := CheckDraftRequirements(doc); err != nil {
		return "", err
	}
	var b strings.Builder
	writeBlock(&b, "Title:", redact(doc.Header.TitleBlock))
	writeBlock(&b, "Duties:", listBody(promptLines(doc.Duties)))
	writeBlock(&b, "Requirements:", listBody(promptLines(doc.Requirements)))
	writeBlock(&b, "Template:", templateText)
	return b.String(), nil
}

// Draft calls fm available, then fm respond, and returns the letter.
// It does not create a directory or write a file.
// An empty requirements list returns ErrRequirementsEmpty and does not call fm.
func Draft(doc Document, templateText string) (string, error) {
	text, err := DraftPrompt(doc, templateText)
	if err != nil {
		return "", err
	}
	argv := respondArgv(text)
	if fmArgExceeds(argv, draftEnviron()) {
		return "", ErrFMArgLimit
	}
	if _, err := callFM([]string{fmPath, "available"}); err != nil {
		return "", err
	}
	stdout, err := callFM(argv)
	if err != nil {
		return "", err
	}
	return ParseLetter(stdout)
}

func respondArgv(text string) []string {
	return []string{
		fmPath,
		"respond",
		"--no-stream",
		"--use-case", "general",
		"--instructions", DraftInstructions,
		"--schema", LetterSchema,
		"--text", text,
	}
}

func fmStringCharge(groups ...[]string) int {
	n := 0
	for _, group := range groups {
		for _, s := range group {
			n += len(s) + 1 + fmPtrBytes
		}
	}
	return n
}

func fmArgCharge(argv, env []string) int {
	return fmStringCharge(argv, env) + fmPtrBytes + fmPtrBytes
}

func fmArgExceeds(argv, env []string) bool {
	return fmArgCharge(argv, env)+fmArgSlack >= kernArgMax
}

// ParseLetter checks fm stdout for one non-empty letter field.
// The letter is returned unchanged, including a trailing blank line.
func ParseLetter(stdout []byte) (string, error) {
	stdout = bytes.TrimSpace(stdout)
	if len(stdout) == 0 {
		return "", ErrDraftSchema
	}
	dec := json.NewDecoder(bytes.NewReader(stdout))
	dec.DisallowUnknownFields()
	var parsed struct {
		Letter string `json:"letter"`
	}
	if err := dec.Decode(&parsed); err != nil {
		return "", ErrDraftSchema
	}
	var trail json.RawMessage
	if err := dec.Decode(&trail); !errors.Is(err, io.EOF) {
		return "", ErrDraftSchema
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(stdout, &fields); err != nil || len(fields) != 1 {
		return "", ErrDraftSchema
	}
	raw, ok := fields["letter"]
	if !ok || isNull(raw) {
		return "", ErrDraftSchema
	}
	if parsed.Letter == "" {
		return "", ErrDraftSchema
	}
	return parsed.Letter, nil
}
