package jobad

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

const (
	levelMust      = "must"
	levelAdvantage = "advantage"
)

// ErrRequirementsEmpty is returned when PromptText has no requirements left
// to send. The CLI prints this text and does not call fm.
var ErrRequirementsEmpty = errors.New("docs-cli: requirements is empty")

// ErrAnalysisSchema is returned when stdout is not an Analysis object.
// The CLI prints this text and does not treat the model text as success.
var ErrAnalysisSchema = errors.New("docs-cli: fm output is not the analysis schema")

// AnalysisInstructions is the fm --instructions argument.
// Benefits and duties are context only. Requirements are the hiring conditions.
const AnalysisInstructions = `Treat only the Requirements section as hiring conditions. Benefits and Duties are for comparison only and must not be written into must_have or advantage. Put a parenthetical condition, such as "For Frontend", in that technology item's applies_to. Put "will be an advantage" and "familiar with" in advantage. Put degree, minimum years of experience, languages, and soft skills in their own fields. When no years of experience are stated, min_years is null. Each tech.level must be "must" or "advantage".`

// AnalysisSchema is the fm --schema argument: the workflow schema as one JSON value.
const AnalysisSchema = `{"type":"object","properties":{"must_have":{"type":"array","items":{"type":"string"}},"advantage":{"type":"array","items":{"type":"string"}},"min_years":{"type":["integer","null"]},"degree":{"type":["string","null"]},"languages":{"type":"array","items":{"type":"string"}},"tech":{"type":"array","items":{"type":"object","properties":{"name":{"type":"string"},"level":{"type":"string"},"applies_to":{"type":["string","null"]}},"required":["name","level","applies_to"]}},"soft_skills":{"type":"array","items":{"type":"string"}}},"required":["must_have","advantage","min_years","degree","languages","tech","soft_skills"]}`

// Analysis is the on-device model's structured result.
// MinYears, Degree, and Tech.AppliesTo are pointers so JSON null is kept.
type Analysis struct {
	MustHave   []string `json:"must_have"`
	Advantage  []string `json:"advantage"`
	MinYears   *int     `json:"min_years"`
	Degree     *string  `json:"degree"`
	Languages  []string `json:"languages"`
	Tech       []Tech   `json:"tech"`
	SoftSkills []string `json:"soft_skills"`
}

// Tech is one technology row. Level is "must" or "advantage".
type Tech struct {
	Name      string  `json:"name"`
	Level     string  `json:"level"`
	AppliesTo *string `json:"applies_to"`
}

var analysisKeys = []string{
	"must_have",
	"advantage",
	"min_years",
	"degree",
	"languages",
	"tech",
	"soft_skills",
}

// PromptText builds the fm --text value from the title and three lists.
// It does not exec fm. An empty requirements list returns ErrRequirementsEmpty.
// Trailer, source, and email addresses are omitted.
func PromptText(doc Document) (string, error) {
	requirements := promptLines(doc.Requirements)
	if len(requirements) == 0 {
		return "", ErrRequirementsEmpty
	}
	var b strings.Builder
	writeBlock(&b, "Title:", redact(doc.Header.TitleBlock))
	writeBlock(&b, "Benefits:", listBody(promptLines(doc.Benefits)))
	writeBlock(&b, "Duties:", listBody(promptLines(doc.Duties)))
	writeBlock(&b, "Requirements:", listBody(requirements))
	return b.String(), nil
}

func promptLines(items []string) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = redact(item)
		if item == "" {
			continue
		}
		out = append(out, item)
	}
	return out
}

func listBody(items []string) string {
	if len(items) == 0 {
		return "(none)"
	}
	var b strings.Builder
	for i, item := range items {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("- ")
		b.WriteString(item)
	}
	return b.String()
}

func writeBlock(b *strings.Builder, label, body string) {
	if b.Len() > 0 {
		b.WriteByte('\n')
	}
	b.WriteString(label)
	b.WriteByte('\n')
	b.WriteString(body)
	b.WriteByte('\n')
}

// ParseAnalysis checks fm stdout against Analysis.
// It uses DisallowUnknownFields and does not exec fm.
// JSON null is legal for min_years, degree, and applies_to.
func ParseAnalysis(stdout []byte) (Analysis, error) {
	stdout = bytes.TrimSpace(stdout)
	if len(stdout) == 0 {
		return Analysis{}, ErrAnalysisSchema
	}
	dec := json.NewDecoder(bytes.NewReader(stdout))
	dec.DisallowUnknownFields()
	var parsed Analysis
	if err := dec.Decode(&parsed); err != nil {
		return Analysis{}, ErrAnalysisSchema
	}
	var trail json.RawMessage
	if err := dec.Decode(&trail); !errors.Is(err, io.EOF) {
		return Analysis{}, ErrAnalysisSchema
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(stdout, &fields); err != nil || len(fields) != len(analysisKeys) {
		return Analysis{}, ErrAnalysisSchema
	}
	for _, key := range analysisKeys {
		raw, ok := fields[key]
		if !ok {
			return Analysis{}, ErrAnalysisSchema
		}
		if key != "min_years" && key != "degree" && isNull(raw) {
			return Analysis{}, ErrAnalysisSchema
		}
	}
	if err := checkTech(fields["tech"], parsed.Tech); err != nil {
		return Analysis{}, ErrAnalysisSchema
	}
	parsed.MustHave = nonNilStrings(parsed.MustHave)
	parsed.Advantage = nonNilStrings(parsed.Advantage)
	parsed.Languages = nonNilStrings(parsed.Languages)
	parsed.SoftSkills = nonNilStrings(parsed.SoftSkills)
	if parsed.Tech == nil {
		parsed.Tech = []Tech{}
	}
	return parsed, nil
}

func checkTech(raw json.RawMessage, items []Tech) error {
	if isNull(raw) {
		return ErrAnalysisSchema
	}
	var elems []json.RawMessage
	if err := json.Unmarshal(raw, &elems); err != nil || elems == nil || len(elems) != len(items) {
		return ErrAnalysisSchema
	}
	for i, elem := range elems {
		if isNull(elem) {
			return ErrAnalysisSchema
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(elem, &fields); err != nil || len(fields) != 3 {
			return ErrAnalysisSchema
		}
		if _, ok := fields["name"]; !ok {
			return ErrAnalysisSchema
		}
		if _, ok := fields["level"]; !ok {
			return ErrAnalysisSchema
		}
		if _, ok := fields["applies_to"]; !ok {
			return ErrAnalysisSchema
		}
		switch items[i].Level {
		case levelMust, levelAdvantage:
		default:
			return ErrAnalysisSchema
		}
	}
	return nil
}

func isNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func nonNilStrings(items []string) []string {
	if items == nil {
		return []string{}
	}
	return items
}
