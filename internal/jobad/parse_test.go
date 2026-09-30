package jobad

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestParseJobsDBSections(t *testing.T) {
	doc := parseFixture(t)

	if doc.Source != "" {
		t.Fatalf("source = %q, want empty", doc.Source)
	}
	if doc.Header.TitleBlock != "System Analyst (Java/Web Frontend/.Net Core) Nexify Limited" {
		t.Fatalf("title_block = %q", doc.Header.TitleBlock)
	}
	if doc.Header.Location != "Kwun Tong District" {
		t.Fatalf("location = %q", doc.Header.Location)
	}
	if doc.Header.WorkType != "Full time" {
		t.Fatalf("work_type = %q", doc.Header.WorkType)
	}
	if !strings.Contains(doc.Header.Classification, "&") {
		t.Fatalf("classification = %q, want decoded &", doc.Header.Classification)
	}
	if doc.Header.Classification != "Business/Systems Analysts (Information & Communication Technology)" {
		t.Fatalf("classification = %q", doc.Header.Classification)
	}
	if len(doc.Offer) != 6 || len(doc.Duties) != 7 || len(doc.Requirements) != 10 || len(doc.Trailer) != 2 {
		t.Fatalf("lens offer=%d duties=%d requirements=%d trailer=%d", len(doc.Offer), len(doc.Duties), len(doc.Requirements), len(doc.Trailer))
	}
	if !strings.Contains(strings.Join(doc.Requirements, "\n"), "(For Frontend)") {
		t.Fatal("requirements missing (For Frontend)")
	}
	if !strings.Contains(strings.Join(doc.Requirements, "\n"), "will be an advantage") {
		t.Fatal("requirements missing advantage line")
	}
	if !strings.Contains(strings.Join(doc.Requirements, "\n"), "Fluency in Cantonese and good written English is required") {
		t.Fatal("requirements missing strong-wrapped fluency line")
	}

	empty, err := Parse([]byte(`<div data-automation="jobAdDetails"></div>`))
	if err != nil {
		t.Fatal(err)
	}
	raw := marshal(t, empty)
	for _, key := range []string{`"offer"`, `"duties"`, `"requirements"`, `"trailer"`} {
		if !bytes.Contains(raw, []byte(key+`: []`)) {
			t.Fatalf("empty document JSON missing %s: []\n%s", key, raw)
		}
	}
	if bytes.Contains(raw, []byte("null")) {
		t.Fatalf("empty document encoded null:\n%s", raw)
	}
}

func TestParseSkipsScriptSVGAttributesAndSiblingIcons(t *testing.T) {
	doc := parseFixture(t)
	blob := string(marshal(t, doc))
	for _, leak := range []string{"SHOULD-NOT-LEAK", "loginId", "person@example.com", "mailto:", "Salary", "Industry"} {
		if strings.Contains(blob, leak) {
			t.Fatalf("JSON contains %q\n%s", leak, blob)
		}
	}
	if doc.Header.WorkType != "Full time" {
		t.Fatalf("work_type = %q", doc.Header.WorkType)
	}
}

func TestParseRedactsEmails(t *testing.T) {
	doc := parseFixture(t)
	blob := string(marshal(t, doc))
	if strings.Contains(blob, "example.com") {
		t.Fatalf("email host left in JSON:\n%s", blob)
	}
	if len(doc.Trailer) != 2 {
		t.Fatalf("trailer len = %d", len(doc.Trailer))
	}
	if strings.Contains(doc.Trailer[1], "@") {
		t.Fatalf("trailer email not removed: %q", doc.Trailer[1])
	}
	if !strings.Contains(doc.Trailer[1], "email to") {
		t.Fatalf("trailer lost surrounding text: %q", doc.Trailer[1])
	}

	onlyMail, err := Parse([]byte(`<div data-automation="jobAdDetails"><p><strong>Job Requirement</strong></p><ul><li>person@example.com</li></ul><p>Write to ••••@example.com today</p></div>`))
	if err != nil {
		t.Fatal(err)
	}
	if len(onlyMail.Requirements) != 0 {
		t.Fatalf("email-only requirement kept: %#v", onlyMail.Requirements)
	}
	if len(onlyMail.Trailer) != 1 || onlyMail.Trailer[0] != "Write to today" {
		t.Fatalf("masked email trailer = %#v", onlyMail.Trailer)
	}
}

func TestParseOmitsSalaryWarning(t *testing.T) {
	doc := parseFixture(t)
	blob := string(marshal(t, doc))
	const warning = "Applications submitted without expected salary"
	if strings.Contains(blob, warning) {
		t.Fatalf("salary warning was kept:\n%s", blob)
	}
	joined := strings.Join(append(append(append(doc.Offer, doc.Duties...), doc.Requirements...), doc.Trailer...), "\n")
	if strings.Contains(joined, warning) {
		t.Fatalf("salary warning in sections: %s", joined)
	}
}

func TestParseMissingJobAdDetails(t *testing.T) {
	_, err := Parse([]byte(`<span data-automation="job-detail-title">Title</span>`))
	if !errors.Is(err, ErrJobAdDetailsNotFound) {
		t.Fatalf("err = %v", err)
	}
	if err.Error() != "jobad: jobAdDetails not found" {
		t.Fatalf("err text = %q", err.Error())
	}
}

func parseFixture(t *testing.T) Document {
	t.Helper()
	page, err := os.ReadFile("testdata/jobsdb_min.html")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := Parse(page)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func marshal(t *testing.T, doc Document) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
