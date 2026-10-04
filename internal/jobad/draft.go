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
// One prompt drafts the whole letter. The bytes match design PR 23.
const DraftInstructions = `The template is a pattern for structure and voice only, not text to paste. Write the whole letter for this job. The template's line breaks are PDF extraction artifacts, not the letter's layout. Do not copy them. Do not put each word on its own line, and do not write a line that is only a space. Write a normal cover letter: a salutation on one line, then body paragraphs of complete sentences, with several sentences in each paragraph and one blank line between paragraphs, then a closing on one line. Keep the template's language. Keep an applicant name or contact line only when the template gives that line as real text. When a real contact line is kept, put that line on its own line. A square-bracket placeholder is a blank, not text to copy. Do not leave any square-bracket placeholder in the letter. Fill the letter from Title, Company, Duties, and Requirements. Title is the role being applied for, not the applicant's current job. Company is this job's employer. When Company is not (none), use that name as the employer. Do not take the employer from a parenthetical in Title, and do not use an employer from the template's previous role. When Company is (none), do not invent an employer and do not write (none) into the letter. Rewrite the body for this job's Duties and Requirements. Do not invent a recipient, job board, compliment, current job title, degree, date, or years of experience that appears in neither the template nor the JSON. Do not turn a required number of years into the applicant's years. When a blank has no fact in the template or the JSON, omit that blank. The letter field is the whole letter and nothing else.`

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
// Order is Title, Company, Duties, Requirements, Template.
// Template text is reflowed and is not redacted. Job emails are removed.
// An empty company prints (none). An empty requirements list returns ErrRequirementsEmpty.
func DraftPrompt(doc Document, templateText string) (string, error) {
	if err := CheckDraftRequirements(doc); err != nil {
		return "", err
	}
	company := redact(doc.Header.Company)
	if company == "" {
		company = "(none)"
	}
	var b strings.Builder
	writeBlock(&b, "Title:", redact(doc.Header.TitleBlock))
	writeBlock(&b, "Company:", company)
	writeBlock(&b, "Duties:", listBody(promptLines(doc.Duties)))
	writeBlock(&b, "Requirements:", listBody(promptLines(doc.Requirements)))
	writeBlock(&b, "Template:", reflowTemplate(templateText))
	return b.String(), nil
}

type templateLine int

const (
	templateSpacer templateLine = iota
	templateFragment
	templateProse
)

func classifyTemplateLine(line string) templateLine {
	if strings.TrimSpace(line) == "" {
		return templateSpacer
	}
	if len(strings.Fields(line)) == 1 {
		return templateFragment
	}
	return templateProse
}

func collapseSpace(line string) string {
	return strings.Join(strings.Fields(line), " ")
}

// reflowTemplate joins a run of one-word lines into one line.
// Blank lines inside that run are not paragraph breaks.
// Blank lines outside a run become one blank line.
// Leading and trailing blank lines are dropped. There is no trailing newline.
// The text is not redacted.
func reflowTemplate(raw string) string {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.ReplaceAll(raw, "\r", "\n")
	lines := strings.Split(raw, "\n")

	var out []string
	emitted := false
	blankPending := false
	emit := func(line string) {
		if emitted && blankPending {
			out = append(out, "")
		}
		out = append(out, line)
		emitted = true
		blankPending = false
	}

	for i := 0; i < len(lines); {
		switch classifyTemplateLine(lines[i]) {
		case templateSpacer:
			if emitted {
				blankPending = true
			}
			i++
		case templateFragment:
			words := []string{collapseSpace(lines[i])}
			j := i + 1
			for {
				k := j
				for k < len(lines) && classifyTemplateLine(lines[k]) == templateSpacer {
					k++
				}
				if k < len(lines) && classifyTemplateLine(lines[k]) == templateFragment {
					words = append(words, collapseSpace(lines[k]))
					j = k + 1
					continue
				}
				break
			}
			emit(strings.Join(words, " "))
			i = j
		default:
			emit(collapseSpace(lines[i]))
			i++
		}
	}
	return strings.Join(out, "\n")
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
