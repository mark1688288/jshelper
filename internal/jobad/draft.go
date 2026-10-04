package jobad

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/ledongthuc/pdf"
)

const (
	// maxTemplateBytes is the only input cap for a template PDF.
	// The PDF library inflates compressed streams before a reader limit can apply.
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
// One prompt drafts the whole letter. The bytes match design PR 24.
const DraftInstructions = `The template is a pattern for structure and voice only, not text to paste. Write the whole letter for this job. The template's blank line is a paragraph break. Keep it. A single newline is a line break. Keep it. Do not join two blocks that the template separates with a blank line. Do not insert a blank line where the template has only a single newline. Do not put each word on its own line. Do not put a trailing space on any line. Do not write a line that is only a space. Keep the template's language. Keep an applicant name or contact line only when the template gives that line as real text. When a real contact line is kept, put that line on its own line. A square-bracket placeholder is a blank, not text to copy. Do not leave any square-bracket placeholder in the letter. Fill the letter from Title, Company, Duties, and Requirements. Title is the role being applied for, not the applicant's current job. Company is this job's employer. When Company is not (none), use that name as the employer. Do not take the employer from a parenthetical in Title, and do not use an employer from the template's previous role. When Company is (none), do not invent an employer and do not write (none) into the letter. Rewrite the body for this job's Duties and Requirements. Do not invent a recipient, job board, compliment, current job title, degree, date, or years of experience that appears in neither the template nor the JSON. Do not turn a required number of years into the applicant's years. When a blank has no fact in the template or the JSON, omit that blank. The letter field is the whole letter and nothing else.`

// wrapGapMax is the widest leftover that still counts as a wrapped line.
// A leftover of zero belongs to the line that sets the page's right edge.
const wrapGapMax = 72

// LetterSchema is the fm --schema argument accepted on 2026-10-02.
// It is the compact byte string, not the pretty-printed object dialect.
const LetterSchema = `{"x-order":["letter"],"title":"Letter","additionalProperties":false,"required":["letter"],"properties":{"letter":{"type":"string"}},"type":"object"}`

// draftEnviron is the environment counted toward the argv limit.
// The child inherits the process environment, so the default is os.Environ.
var draftEnviron = os.Environ

// ExtractTemplateText reads a template PDF as visual lines.
// Stat errors are returned unchanged. The text has no trailing newline.
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
	pages := make([][]pdf.Text, 0, reader.NumPage())
	for i := 1; i <= reader.NumPage(); i++ {
		pages = append(pages, reader.Page(i).Content().Text)
	}
	raw := layoutPages(pages)
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
// Template text is used as extracted and is not redacted. Job emails are removed.
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
	writeBlock(&b, "Template:", templateText)
	return b.String(), nil
}

type visualLine struct {
	text      string
	end       float64
	wordWidth float64
	blank     bool
}

// layoutPages lays each page out, drops empty pages, and separates pages
// with one blank line. The result has no trailing newline.
func layoutPages(pages [][]pdf.Text) string {
	var out []string
	for _, page := range pages {
		laid := layoutPage(page)
		if laid == "" {
			continue
		}
		out = append(out, laid)
	}
	return strings.Join(out, "\n\n")
}

func layoutPage(texts []pdf.Text) string {
	lines := visualLines(texts)
	for len(lines) > 0 && lines[0].blank {
		lines = lines[1:]
	}
	for len(lines) > 0 && lines[len(lines)-1].blank {
		lines = lines[:len(lines)-1]
	}
	compact := lines[:0]
	for _, ln := range lines {
		if ln.blank && len(compact) > 0 && compact[len(compact)-1].blank {
			continue
		}
		compact = append(compact, ln)
	}
	lines = compact
	if len(lines) == 0 {
		return ""
	}
	right := 0.0
	for _, ln := range lines {
		if !ln.blank && ln.end > right {
			right = ln.end
		}
	}
	var b strings.Builder
	for i, ln := range lines {
		if i == 0 {
			b.WriteString(ln.text)
			continue
		}
		prev := lines[i-1]
		if prev.blank || ln.blank || !lineJoins(right-prev.end, ln.wordWidth) {
			b.WriteByte('\n')
			b.WriteString(ln.text)
			continue
		}
		b.WriteByte(' ')
		b.WriteString(ln.text)
	}
	return b.String()
}

func lineJoins(remaining, wordWidth float64) bool {
	return remaining > 0 && remaining < wrapGapMax && wordWidth > remaining
}

func visualLines(texts []pdf.Text) []visualLine {
	glyphs := make([]pdf.Text, 0, len(texts))
	for _, g := range texts {
		if g.S == "" || g.S == "\n" {
			continue
		}
		glyphs = append(glyphs, g)
	}
	sort.SliceStable(glyphs, func(i, j int) bool {
		if glyphs[i].Y != glyphs[j].Y {
			return glyphs[i].Y > glyphs[j].Y
		}
		return glyphs[i].X < glyphs[j].X
	})
	var groups [][]pdf.Text
	for _, g := range glyphs {
		if len(groups) == 0 || groups[len(groups)-1][0].Y-g.Y >= 1 {
			groups = append(groups, []pdf.Text{g})
			continue
		}
		groups[len(groups)-1] = append(groups[len(groups)-1], g)
	}
	lines := make([]visualLine, 0, len(groups))
	for _, group := range groups {
		sort.SliceStable(group, func(i, j int) bool {
			return group[i].X < group[j].X
		})
		lines = append(lines, measureLine(group))
	}
	return lines
}

func measureLine(glyphs []pdf.Text) visualLine {
	adv := glyphAdvances(glyphs)
	var text strings.Builder
	end := 0.0
	word := 0.0
	inWord := true
	nonSpace := false
	for i, g := range glyphs {
		text.WriteString(g.S)
		if isSpaceGlyph(g.S) {
			inWord = false
			continue
		}
		nonSpace = true
		end = g.X + adv[i]
		if inWord {
			word += adv[i]
		}
	}
	trimmed := strings.TrimRight(text.String(), " \t")
	return visualLine{
		text:      trimmed,
		end:       end,
		wordWidth: word,
		blank:     !nonSpace || trimmed == "",
	}
}

func glyphAdvances(glyphs []pdf.Text) []float64 {
	adv := make([]float64, len(glyphs))
	for i, g := range glyphs {
		if i+1 < len(glyphs) && glyphs[i+1].X-g.X > 0 {
			adv[i] = glyphs[i+1].X - g.X
			continue
		}
		if g.W > 0 {
			adv[i] = g.W
			continue
		}
		if i > 0 && adv[i-1] > 0 {
			adv[i] = adv[i-1]
			continue
		}
		if g.FontSize > 0 {
			adv[i] = g.FontSize
			continue
		}
	}
	return adv
}

func isSpaceGlyph(s string) bool {
	if s == "" {
		return true
	}
	for _, r := range s {
		if r != ' ' && r != '\t' {
			return false
		}
	}
	return true
}

// layoutLetter trims trailing spaces, drops a line that is only one
// square-bracket placeholder, and collapses three or more newlines to one
// blank line. It does not join lines or add a trailing newline.
// A letter that is empty after that is ErrDraftSchema.
func layoutLetter(letter string) (string, error) {
	letter = strings.ReplaceAll(letter, "\r\n", "\n")
	letter = strings.ReplaceAll(letter, "\r", "\n")
	parts := strings.Split(letter, "\n")
	kept := make([]string, 0, len(parts))
	for _, line := range parts {
		line = strings.TrimRight(line, " \t")
		if isPlaceholderLine(line) {
			continue
		}
		kept = append(kept, line)
	}
	out := strings.Join(kept, "\n")
	for strings.Contains(out, "\n\n\n") {
		out = strings.ReplaceAll(out, "\n\n\n", "\n\n")
	}
	if strings.TrimSpace(out) == "" {
		return "", ErrDraftSchema
	}
	return out, nil
}

func isPlaceholderLine(line string) bool {
	if len(line) < 3 || line[0] != '[' || line[len(line)-1] != ']' {
		return false
	}
	return !strings.ContainsAny(line[1:len(line)-1], "[]")
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
	letter, err := ParseLetter(stdout)
	if err != nil {
		return "", err
	}
	return layoutLetter(letter)
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
