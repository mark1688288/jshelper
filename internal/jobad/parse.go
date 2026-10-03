// Package jobad extracts a saved JobsDB job page into section JSON.
package jobad

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
)

// ErrJobAdDetailsNotFound is returned when the page has no jobAdDetails node.
var ErrJobAdDetailsNotFound = errors.New("jobad: jobAdDetails not found")

// Document is the extraction JSON. Source stays empty; the CLI fills it.
// A legacy "offer" key is not read. Missing benefits decodes as an empty slice.
type Document struct {
	Source       string   `json:"source"`
	Header       Header   `json:"header"`
	Benefits     []string `json:"benefits"`
	Duties       []string `json:"duties"`
	Requirements []string `json:"requirements"`
	Trailer      []string `json:"trailer"`
}

// Header is the visible text of the job-detail automation nodes.
type Header struct {
	TitleBlock     string `json:"title_block"`
	Location       string `json:"location"`
	WorkType       string `json:"work_type"`
	Classification string `json:"classification"`
}

const (
	sectionBenefits     = "benefits"
	sectionDuties       = "duties"
	sectionRequirements = "requirements"
	sectionTrailer      = "trailer"
	// sectionGap is walk state only. Items collected there are stored in trailer.
	sectionGap = "gap"
	// maxHeadingRunes is the longest normalized paragraph that can still be a heading.
	// Block collection uses the same ceiling. The salary warning is longer and is not a heading.
	maxHeadingRunes = 80
	// maxGapRunes is the longest normalized unclassified strong that opens a gap.
	// A strong of 41 runes through maxHeadingRunes that matches no phrase stays in the current section.
	maxGapRunes = 40
)

// Local part is either an ordinary mailbox or a mask of bullets, stars, or dots.
var emailRE = regexp.MustCompile(`(?i)(?:[A-Za-z0-9._%+\-]+|[•*.]{2,})@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)

// UnmarshalJSON ignores a legacy offer key. Missing benefits becomes [].
func (d *Document) UnmarshalJSON(data []byte) error {
	type plain Document
	var raw plain
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if raw.Benefits == nil {
		raw.Benefits = []string{}
	}
	*d = Document(raw)
	return nil
}

// Parse builds a Document from a JobsDB HTML page.
// Lists are empty slices, never nil. Source is left empty.
func Parse(page []byte) (Document, error) {
	root, err := html.Parse(strings.NewReader(string(page)))
	if err != nil {
		return Document{}, err
	}
	doc := Document{
		Benefits:     []string{},
		Duties:       []string{},
		Requirements: []string{},
		Trailer:      []string{},
	}
	doc.Header = Header{
		TitleBlock:     textOf(findAutomation(root, "job-detail-title")),
		Location:       textOf(findAutomation(root, "job-detail-location")),
		WorkType:       textOf(findAutomation(root, "job-detail-work-type")),
		Classification: textOf(findAutomation(root, "job-detail-classifications")),
	}
	redactHeader(&doc.Header)
	body := findAutomation(root, "jobAdDetails")
	if body == nil {
		return Document{}, ErrJobAdDetailsNotFound
	}
	walk(body, "", &doc)
	return doc, nil
}

func redactHeader(h *Header) {
	h.TitleBlock = redact(h.TitleBlock)
	h.Location = redact(h.Location)
	h.WorkType = redact(h.WorkType)
	h.Classification = redact(h.Classification)
}

func walk(n *html.Node, section string, doc *Document) string {
	if n.Type == html.ElementNode {
		switch n.Data {
		case "script", "style", "svg":
			return section
		case "strong":
			// A strong inside an li never reaches here; li returns first.
			if next, ok := sectionStrong(textOf(n)); ok {
				return next
			}
			return section
		case "li":
			switch section {
			case sectionBenefits, sectionDuties, sectionRequirements:
				add(doc, section, textOf(n))
			case sectionGap:
				add(doc, sectionTrailer, textOf(n))
			}
			return section
		case "p":
			return walkP(n, section, doc)
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		section = walk(c, section, doc)
	}
	return section
}

func walkP(n *html.Node, section string, doc *Document) string {
	norm := normalizeHeading(textOf(n))
	if utf8.RuneCountInString(norm) <= maxHeadingRunes {
		if kind, ok := classify(norm); ok {
			return kind
		}
	}
	if containsSectionStrong(n) {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			section = walk(c, section, doc)
		}
		return section
	}
	if section == sectionRequirements || section == sectionGap {
		add(doc, sectionTrailer, textOf(n))
		return section
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		section = walk(c, section, doc)
	}
	return section
}

// sectionStrong classifies a strong for Parse, or opens a gap when the
// normalized text is 1 to maxGapRunes and matches no phrase.
func sectionStrong(text string) (string, bool) {
	return strongWithin(text, maxGapRunes)
}

// strongSection classifies a strong for block collection, or opens a gap when
// the normalized text is 1 to maxHeadingRunes and matches no phrase.
func strongSection(text string) (string, bool) {
	return strongWithin(text, maxHeadingRunes)
}

func strongWithin(text string, gapRunes int) (string, bool) {
	norm := normalizeHeading(text)
	if kind, ok := classify(norm); ok {
		return kind, true
	}
	if n := utf8.RuneCountInString(norm); n >= 1 && n <= gapRunes {
		return sectionGap, true
	}
	return "", false
}

func containsSectionStrong(n *html.Node) bool {
	return containsStrongWithin(n, maxGapRunes)
}

func containsHeading(n *html.Node) bool {
	return containsStrongWithin(n, maxHeadingRunes)
}

func containsStrongWithin(n *html.Node, gapRunes int) bool {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode {
			continue
		}
		switch c.Data {
		case "script", "style", "svg", "li":
			continue
		case "strong":
			if _, ok := strongWithin(textOf(c), gapRunes); ok {
				return true
			}
		}
		if containsStrongWithin(c, gapRunes) {
			return true
		}
	}
	return false
}

// classify expects normalizeHeading output. Benefits exact, then duties, then requirements.
// Duties is an exact phrase, or any strings.Fields token equal to responsibilities.
// That token is a whole word, so irresponsibilities and responsibility do not match.
// It is checked before requirements phrases, so both in one heading stay duties.
func classify(norm string) (string, bool) {
	switch norm {
	case "what do we offer", "what we offer", "what you'll get", "what you will get":
		return sectionBenefits, true
	case "job description", "what you'll do", "what you will do", "responsibilities",
		"your responsibilities", "about the role", "the role", "工作內容", "職責":
		return sectionDuties, true
	}
	for _, word := range strings.Fields(norm) {
		if word == "responsibilities" {
			return sectionDuties, true
		}
	}
	switch norm {
	case "requirement", "requirements", "job requirement", "job requirements",
		"qualifications", "qualification", "who you are", "about you":
		return sectionRequirements, true
	}
	for _, phrase := range []string{
		"good fit",
		"if you have",
		"if you are",
		"you'll need",
		"you will need",
		"what you'll bring",
		"what you will bring",
		"looking for",
		"職位要求",
		"入職要求",
	} {
		if strings.Contains(norm, phrase) {
			return sectionRequirements, true
		}
	}
	return "", false
}

// normalizeHeading collapses whitespace, drops one trailing colon or question mark, then lowercases.
func normalizeHeading(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return ""
	}
	rs := []rune(s)
	switch rs[len(rs)-1] {
	case ':', '：', '?', '？':
		s = strings.TrimSpace(string(rs[:len(rs)-1]))
	}
	return strings.ToLower(s)
}

func add(doc *Document, section, text string) {
	text = redact(text)
	if text == "" {
		return
	}
	switch section {
	case sectionBenefits:
		doc.Benefits = append(doc.Benefits, text)
	case sectionDuties:
		doc.Duties = append(doc.Duties, text)
	case sectionRequirements:
		doc.Requirements = append(doc.Requirements, text)
	case sectionTrailer:
		doc.Trailer = append(doc.Trailer, text)
	}
}

func findAutomation(n *html.Node, name string) *html.Node {
	if n == nil {
		return nil
	}
	if n.Type == html.ElementNode && attr(n, "data-automation") == name {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if found := findAutomation(c, name); found != nil {
			return found
		}
	}
	return nil
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// textOf is the visible text of n and its descendants.
// Attribute values are ignored. script, style, and svg subtrees are skipped.
func textOf(n *html.Node) string {
	if n == nil {
		return ""
	}
	var b strings.Builder
	collectText(&b, n)
	return strings.Join(strings.Fields(b.String()), " ")
}

func collectText(b *strings.Builder, n *html.Node) {
	if n.Type == html.ElementNode {
		switch n.Data {
		case "script", "style", "svg":
			return
		}
	}
	if n.Type == html.TextNode {
		b.WriteString(n.Data)
		b.WriteByte(' ')
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		collectText(b, c)
	}
}

func redact(s string) string {
	s = emailRE.ReplaceAllString(s, "")
	return strings.Join(strings.Fields(s), " ")
}
