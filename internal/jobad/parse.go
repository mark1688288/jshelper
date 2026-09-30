// Package jobad extracts a saved JobsDB job page into section JSON.
package jobad

import (
	"errors"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

// ErrJobAdDetailsNotFound is returned when the page has no jobAdDetails node.
var ErrJobAdDetailsNotFound = errors.New("jobad: jobAdDetails not found")

// Document is the extraction JSON. Source stays empty; the CLI fills it.
type Document struct {
	Source       string   `json:"source"`
	Header       Header   `json:"header"`
	Offer        []string `json:"offer"`
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
	sectionOffer        = "offer"
	sectionDuties       = "duties"
	sectionRequirements = "requirements"
	sectionTrailer      = "trailer"
)

// Known headings switch section. Other strong text, including the salary
// warning, is not a heading and is not copied into a list.
var headings = map[string]string{
	"What do we offer?": sectionOffer,
	"Job Description":   sectionDuties,
	"Job Requirement":   sectionRequirements,
}

// Local part is either an ordinary mailbox or a mask of bullets, stars, or dots.
var emailRE = regexp.MustCompile(`(?i)(?:[A-Za-z0-9._%+\-]+|[•*.]{2,})@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)

// Parse builds a Document from a JobsDB HTML page.
// Lists are empty slices, never nil. Source is left empty.
func Parse(page []byte) (Document, error) {
	root, err := html.Parse(strings.NewReader(string(page)))
	if err != nil {
		return Document{}, err
	}
	doc := Document{
		Offer:        []string{},
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
			if next, ok := headings[textOf(n)]; ok {
				return next
			}
		case "li":
			if section == sectionOffer || section == sectionDuties || section == sectionRequirements {
				add(doc, section, textOf(n))
				return section
			}
		case "p":
			if section == sectionRequirements {
				if hasHeading(n) {
					for c := n.FirstChild; c != nil; c = c.NextSibling {
						section = walk(c, section, doc)
					}
					return section
				}
				add(doc, sectionTrailer, textOf(n))
				return section
			}
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		section = walk(c, section, doc)
	}
	return section
}

func add(doc *Document, section, text string) {
	text = redact(text)
	if text == "" {
		return
	}
	switch section {
	case sectionOffer:
		doc.Offer = append(doc.Offer, text)
	case sectionDuties:
		doc.Duties = append(doc.Duties, text)
	case sectionRequirements:
		doc.Requirements = append(doc.Requirements, text)
	case sectionTrailer:
		doc.Trailer = append(doc.Trailer, text)
	}
}

func hasHeading(n *html.Node) bool {
	if n.Type == html.ElementNode {
		switch n.Data {
		case "script", "style", "svg":
			return false
		case "strong":
			if _, ok := headings[textOf(n)]; ok {
				return true
			}
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if hasHeading(c) {
			return true
		}
	}
	return false
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
