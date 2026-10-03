package jobad

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestParseJobsDBSections(t *testing.T) {
	doc := parseFixture(t)

	if doc.Source != "" {
		t.Fatalf("source = %q, want empty", doc.Source)
	}
	if doc.Header.TitleBlock != "System Analyst (Java/Web Frontend/.Net Core)" {
		t.Fatalf("title_block = %q", doc.Header.TitleBlock)
	}
	if doc.Header.Company != "Nexify Limited" {
		t.Fatalf("company = %q", doc.Header.Company)
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
	if len(doc.Benefits) != 6 || len(doc.Duties) != 7 || len(doc.Requirements) != 10 || len(doc.Trailer) != 2 {
		t.Fatalf("lens benefits=%d duties=%d requirements=%d trailer=%d", len(doc.Benefits), len(doc.Duties), len(doc.Requirements), len(doc.Trailer))
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
	for _, key := range []string{`"benefits"`, `"duties"`, `"requirements"`, `"trailer"`} {
		if !bytes.Contains(raw, []byte(key+`: []`)) {
			t.Fatalf("empty document JSON missing %s: []\n%s", key, raw)
		}
	}
	if bytes.Contains(raw, []byte("null")) {
		t.Fatalf("empty document encoded null:\n%s", raw)
	}
	if bytes.Contains(raw, []byte(`"offer"`)) {
		t.Fatalf("empty document still has offer:\n%s", raw)
	}
	order := []string{`"source"`, `"header"`, `"benefits"`, `"duties"`, `"requirements"`, `"trailer"`}
	at := 0
	for _, key := range order {
		i := bytes.Index(raw, []byte(key))
		if i < at {
			t.Fatalf("key %s out of order:\n%s", key, raw)
		}
		at = i + len(key)
	}
	headerOrder := []string{`"title_block"`, `"company"`, `"location"`, `"work_type"`, `"classification"`}
	at = 0
	for _, key := range headerOrder {
		i := bytes.Index(raw, []byte(key))
		if i < at {
			t.Fatalf("header key %s out of order:\n%s", key, raw)
		}
		at = i + len(key)
	}
	if !bytes.Contains(raw, []byte(`"company": ""`)) {
		t.Fatalf("empty company omitted:\n%s", raw)
	}
	if !strings.Contains(strings.Join(doc.Trailer, "\n"), "fringe benefits") {
		t.Fatalf("fringe benefits left trailer: %#v", doc.Trailer)
	}
	var legacy Document
	if err := json.Unmarshal([]byte(`{"offer":["Medical cover"]}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.Benefits == nil || len(legacy.Benefits) != 0 {
		t.Fatalf("offer filled benefits: %#v", legacy.Benefits)
	}
	if again := marshal(t, legacy); !bytes.Contains(again, []byte(`"benefits": []`)) || bytes.Contains(again, []byte("Medical cover")) {
		t.Fatalf("legacy benefits JSON:\n%s", again)
	}
	if err := json.Unmarshal([]byte(`{"benefits":["Kept"],"offer":["Old"]}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if strings.Join(legacy.Benefits, "|") != "Kept" {
		t.Fatalf("offer alias changed benefits: %#v", legacy.Benefits)
	}
}

func TestParseSkipsScriptSVGAttributesAndSiblingIcons(t *testing.T) {
	doc := parseFixture(t)
	blob := string(marshal(t, doc))
	for _, leak := range []string{"SHOULD-NOT-LEAK", "loginId", "person@example.com", "mailto:", "Salary", "Industry", "Verified", "View all jobs"} {
		if strings.Contains(blob, leak) {
			t.Fatalf("JSON contains %q\n%s", leak, blob)
		}
	}
	if doc.Header.WorkType != "Full time" {
		t.Fatalf("work_type = %q", doc.Header.WorkType)
	}
}

func TestParseCompanyFromAdvertiserName(t *testing.T) {
	doc := parseHTML(t, `<span data-automation="job-detail-title">Role</span>
<span data-automation="advertiser-name">Nexify Limited<span><svg><title>Verified</title></svg></span></span>
<a data-automation="job-details-header-more-jobs">View all jobs</a>
<span data-automation="advertiser-name">Other Co</span>
<div data-automation="company-profile"><h4>Short Name</h4></div>
<script>window.SK_DL = {"companyName":"SK Name"};</script>
<div data-automation="jobAdDetails"></div>`)
	if doc.Header.TitleBlock != "Role" {
		t.Fatalf("title_block = %q", doc.Header.TitleBlock)
	}
	if doc.Header.Company != "Nexify Limited" {
		t.Fatalf("company = %q", doc.Header.Company)
	}
	blob := string(marshal(t, doc))
	for _, leak := range []string{"Verified", "View all jobs", "Other Co", "Short Name", "SK Name", "companyName"} {
		if strings.Contains(blob, leak) {
			t.Fatalf("JSON contains %q\n%s", leak, blob)
		}
	}
}

func TestParseCompanyMissingIsEmpty(t *testing.T) {
	doc := parseHTML(t, `<span data-automation="job-detail-title">Role</span><div data-automation="jobAdDetails"></div>`)
	if doc.Header.TitleBlock != "Role" || doc.Header.Company != "" {
		t.Fatalf("header = %#v", doc.Header)
	}
	if !bytes.Contains(marshal(t, doc), []byte(`"company": ""`)) {
		t.Fatal("missing company was omitted")
	}
}

func TestParseCompanyRedactsEmail(t *testing.T) {
	doc := parseHTML(t, `<span data-automation="advertiser-name">Acme person@example.com Ltd ••••@example.com</span><div data-automation="jobAdDetails"></div>`)
	if doc.Header.Company != "Acme Ltd" {
		t.Fatalf("company = %q", doc.Header.Company)
	}
	if strings.Contains(string(marshal(t, doc)), "example.com") {
		t.Fatalf("email left in JSON: %s", marshal(t, doc))
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
	joined := strings.Join(append(append(append(doc.Benefits, doc.Duties...), doc.Requirements...), doc.Trailer...), "\n")
	if strings.Contains(joined, warning) {
		t.Fatalf("salary warning in sections: %s", joined)
	}
}

func TestParseFriendlyHeadings(t *testing.T) {
	doc := parseHTML(t, `<div data-automation="jobAdDetails">
		<p><strong>You'll be a good fit if you have/are:</strong></p>
		<ul><li>Have shipped Go services</li></ul>
		<p><strong>You'll be a good fit if you are:</strong></p>
		<ul><li>Based in Hong Kong</li></ul>
		<p><strong>Job Requirements</strong></p>
		<ul><li>Degree in Computer Science</li></ul>
		<p>What you'll do:</p>
		<ul><li>Design the API</li></ul>
	</div>`)
	if got, want := strings.Join(doc.Requirements, "|"), "Have shipped Go services|Based in Hong Kong|Degree in Computer Science"; got != want {
		t.Fatalf("requirements = %q", got)
	}
	if got, want := strings.Join(doc.Duties, "|"), "Design the API"; got != want {
		t.Fatalf("duties = %q", got)
	}
	if len(doc.Benefits) != 0 || len(doc.Trailer) != 0 {
		t.Fatalf("benefits = %#v trailer = %#v", doc.Benefits, doc.Trailer)
	}
}

func TestParseKeyResponsibilitiesAreDuties(t *testing.T) {
	doc := parseHTML(t, `<div data-automation="jobAdDetails">
		<p><strong>Key Responsibilities</strong></p>
		<ul><li>Own the service</li></ul>
		<p><strong>Key Responsibilities:</strong></p>
		<ul><li>Review pull requests</li></ul>
		<p><strong>Job Responsibilities</strong></p>
		<ul><li>Write the runbook</li></ul>
		<p><strong>Roles and Responsibilities</strong></p>
		<ul><li>Pair with product</li></ul>
		<p><strong>Team culture:</strong></p>
		<ul><li>We share context openly</li></ul>
	</div>`)
	if got, want := strings.Join(doc.Duties, "|"), "Own the service|Review pull requests|Write the runbook|Pair with product"; got != want {
		t.Fatalf("duties = %q", got)
	}
	if got, want := strings.Join(doc.Trailer, "|"), "We share context openly"; got != want {
		t.Fatalf("trailer = %q", got)
	}
	if len(doc.Benefits) != 0 || len(doc.Requirements) != 0 {
		t.Fatalf("benefits = %#v requirements = %#v", doc.Benefits, doc.Requirements)
	}
}

func TestParseResponsibilitiesLookalikesStayInTrailer(t *testing.T) {
	doc := parseHTML(t, `<div data-automation="jobAdDetails">
		<p><strong>Job Description</strong></p>
		<ul><li>Write design notes</li></ul>
		<p><strong>irresponsibilities</strong></p>
		<ul><li>Stay in trailer</li></ul>
		<p><strong>responsibility</strong></p>
		<ul><li>Singular stays in trailer</li></ul>
		<p><strong>主要職責</strong></p>
		<ul><li>Chinese prefix stays in trailer</li></ul>
	</div>`)
	if got, want := strings.Join(doc.Duties, "|"), "Write design notes"; got != want {
		t.Fatalf("duties = %q", got)
	}
	if got, want := strings.Join(doc.Trailer, "|"), "Stay in trailer|Singular stays in trailer|Chinese prefix stays in trailer"; got != want {
		t.Fatalf("trailer = %q", got)
	}
	if len(doc.Requirements) != 0 {
		t.Fatalf("requirements = %#v", doc.Requirements)
	}
}

func TestParseResponsibilitiesBeforeRequirementsPhrase(t *testing.T) {
	doc := parseHTML(t, `<div data-automation="jobAdDetails">
		<p><strong>Key responsibilities if you have shipped</strong></p>
		<ul><li>Own the service</li></ul>
	</div>`)
	if got, want := strings.Join(doc.Duties, "|"), "Own the service"; got != want {
		t.Fatalf("duties = %q", got)
	}
	if len(doc.Requirements) != 0 || len(doc.Trailer) != 0 {
		t.Fatalf("requirements = %#v trailer = %#v", doc.Requirements, doc.Trailer)
	}
}

func TestParseShortHeadingCutsToTrailer(t *testing.T) {
	doc := parseHTML(t, `<div data-automation="jobAdDetails">
		<p><strong>Job Description</strong></p>
		<ul><li>Write design notes</li></ul>
		<p><strong>Team culture:</strong></p>
		<ul><li>We share context openly</li></ul>
	</div>`)
	if got, want := strings.Join(doc.Duties, "|"), "Write design notes"; got != want {
		t.Fatalf("duties = %q", got)
	}
	if got, want := strings.Join(doc.Trailer, "|"), "We share context openly"; got != want {
		t.Fatalf("trailer = %q", got)
	}
	if len(doc.Requirements) != 0 {
		t.Fatalf("requirements = %#v", doc.Requirements)
	}
}

func TestParseLongSalaryWarningIsNotAHeading(t *testing.T) {
	const warning = "Applications submitted without expected salary in resume may be excluded from consideration."
	doc := parseHTML(t, `<div data-automation="jobAdDetails">
		<p><strong>Job Description</strong></p>
		<ul><li>Write design notes</li></ul>
		<p><strong>`+warning+`</strong></p>
		<ul><li>Five years of relevant experience</li></ul>
	</div>`)
	if got, want := strings.Join(doc.Duties, "|"), "Write design notes|Five years of relevant experience"; got != want {
		t.Fatalf("duties = %q", got)
	}
	if len(doc.Requirements) != 0 {
		t.Fatalf("requirements = %#v", doc.Requirements)
	}
	blob := strings.Join(append(append(append(doc.Benefits, doc.Duties...), doc.Requirements...), doc.Trailer...), "\n")
	if strings.Contains(blob, warning) {
		t.Fatalf("salary warning was kept: %s", blob)
	}
}

func TestParseUnclassifiedStrongGapCeiling(t *testing.T) {
	forty := strings.Repeat("a", 40)
	fortyOne := strings.Repeat("b", 41)
	if utf8.RuneCountInString(normalizeHeading(forty)) != 40 || utf8.RuneCountInString(normalizeHeading(fortyOne)) != 41 {
		t.Fatal("boundary fixture rune count drifted")
	}

	cut := parseHTML(t, `<div data-automation="jobAdDetails">
		<p><strong>Job Description</strong></p>
		<ul><li>Write design notes</li></ul>
		<p><strong>`+forty+`</strong></p>
		<ul><li>Cut to trailer</li></ul>
	</div>`)
	if got, want := strings.Join(cut.Duties, "|"), "Write design notes"; got != want {
		t.Fatalf("duties = %q", got)
	}
	if got, want := strings.Join(cut.Trailer, "|"), "Cut to trailer"; got != want {
		t.Fatalf("trailer = %q", got)
	}
	if len(cut.Requirements) != 0 {
		t.Fatalf("requirements = %#v", cut.Requirements)
	}

	stay := parseHTML(t, `<div data-automation="jobAdDetails">
		<p><strong>Job Description</strong></p>
		<ul><li>Write design notes</li></ul>
		<p><strong>`+fortyOne+`</strong></p>
		<ul><li>Stay in duties</li></ul>
	</div>`)
	if got, want := strings.Join(stay.Duties, "|"), "Write design notes|Stay in duties"; got != want {
		t.Fatalf("duties = %q", got)
	}
	if len(stay.Trailer) != 0 || len(stay.Requirements) != 0 {
		t.Fatalf("trailer = %#v requirements = %#v", stay.Trailer, stay.Requirements)
	}
	if strings.Contains(strings.Join(stay.Duties, "\n"), fortyOne) {
		t.Fatalf("41-rune label was kept: %s", strings.Join(stay.Duties, "\n"))
	}
}

func TestParseLongSubsectionLabelStaysInDuties(t *testing.T) {
	const mission = "The AI Enablement Lead will own three interconnected missions"
	if utf8.RuneCountInString(normalizeHeading(mission)) != 61 {
		t.Fatalf("mission runes = %d", utf8.RuneCountInString(normalizeHeading(mission)))
	}
	label := "1. " + strings.Repeat("n", 45)
	if n := utf8.RuneCountInString(normalizeHeading(label)); n < 46 || n > 52 {
		t.Fatalf("numbered label runes = %d", n)
	}
	doc := parseHTML(t, `<div data-automation="jobAdDetails">
		<p><strong>Key Responsibilities</strong></p>
		<p><strong>`+mission+`</strong></p>
		<ul><li>Embed with the CEO</li></ul>
		<p><strong>`+label+`</strong></p>
		<ul><li>Set the operating rhythm</li></ul>
		<p><strong>Team culture:</strong></p>
		<ul><li>We share context openly</li></ul>
	</div>`)
	if got, want := strings.Join(doc.Duties, "|"), "Embed with the CEO|Set the operating rhythm"; got != want {
		t.Fatalf("duties = %q", got)
	}
	if got, want := strings.Join(doc.Trailer, "|"), "We share context openly"; got != want {
		t.Fatalf("trailer = %q", got)
	}
	if len(doc.Benefits) != 0 || len(doc.Requirements) != 0 {
		t.Fatalf("benefits = %#v requirements = %#v", doc.Benefits, doc.Requirements)
	}
	blob := strings.Join(append(doc.Duties, doc.Trailer...), "\n")
	if strings.Contains(blob, mission) || strings.Contains(blob, label) {
		t.Fatalf("subsection label was kept: %s", blob)
	}
}

func TestParseResponsibilitiesHeadingOverGapCeiling(t *testing.T) {
	heading := "Key responsibilities " + strings.Repeat("z", 25)
	if n := utf8.RuneCountInString(normalizeHeading(heading)); n < 41 || n > 80 {
		t.Fatalf("heading runes = %d", n)
	}
	doc := parseHTML(t, `<div data-automation="jobAdDetails">
		<p><strong>`+heading+`</strong></p>
		<ul><li>Own the service</li></ul>
	</div>`)
	if got, want := strings.Join(doc.Duties, "|"), "Own the service"; got != want {
		t.Fatalf("duties = %q", got)
	}
	if len(doc.Trailer) != 0 || len(doc.Requirements) != 0 {
		t.Fatalf("trailer = %#v requirements = %#v", doc.Trailer, doc.Requirements)
	}
}

func TestParseWholeParagraphHeadingKeepsEightyRunes(t *testing.T) {
	atEighty := "looking for " + strings.Repeat("x", 68)
	overEighty := "looking for " + strings.Repeat("y", 69)
	if utf8.RuneCountInString(normalizeHeading(atEighty)) != 80 {
		t.Fatalf("80-rune paragraph = %d", utf8.RuneCountInString(normalizeHeading(atEighty)))
	}
	if utf8.RuneCountInString(normalizeHeading(overEighty)) != 81 {
		t.Fatalf("81-rune paragraph = %d", utf8.RuneCountInString(normalizeHeading(overEighty)))
	}
	doc := parseHTML(t, `<div data-automation="jobAdDetails">
		<p><strong>Job Description</strong></p>
		<ul><li>Write design notes</li></ul>
		<p>`+atEighty+`</p>
		<ul><li>Degree in Computer Science</li></ul>
		<p><strong>Job Description</strong></p>
		<ul><li>Back to duties</li></ul>
		<p>`+overEighty+`</p>
		<ul><li>Still a duty</li></ul>
	</div>`)
	if got, want := strings.Join(doc.Requirements, "|"), "Degree in Computer Science"; got != want {
		t.Fatalf("requirements = %q", got)
	}
	if got, want := strings.Join(doc.Duties, "|"), "Write design notes|Back to duties|Still a duty"; got != want {
		t.Fatalf("duties = %q", got)
	}
	blob := strings.Join(append(append(doc.Duties, doc.Requirements...), doc.Trailer...), "\n")
	if strings.Contains(blob, overEighty) {
		t.Fatalf("over-80 paragraph was kept: %s", blob)
	}
}

func TestParseShortGapLabelsStillCutToTrailer(t *testing.T) {
	doc := parseHTML(t, `<div data-automation="jobAdDetails">
		<p><strong>Job Description</strong></p>
		<ul><li>Write design notes</li></ul>
		<p><strong>Benefits</strong></p>
		<ul><li>Medical cover</li></ul>
		<p><strong>Why this role</strong></p>
		<ul><li>Own the domain</li></ul>
		<p><strong>This role is not</strong></p>
		<ul><li>A support rota</li></ul>
		<p><strong>Who we are</strong></p>
		<ul><li>A small team</li></ul>
	</div>`)
	if got, want := strings.Join(doc.Duties, "|"), "Write design notes"; got != want {
		t.Fatalf("duties = %q", got)
	}
	if got, want := strings.Join(doc.Trailer, "|"), "Medical cover|Own the domain|A support rota|A small team"; got != want {
		t.Fatalf("trailer = %q", got)
	}
	if len(doc.Benefits) != 0 || len(doc.Requirements) != 0 {
		t.Fatalf("benefits = %#v requirements = %#v", doc.Benefits, doc.Requirements)
	}
}

func TestParseAboutTheRoleParagraphIsNotADuty(t *testing.T) {
	const para = "You will partner with product on the roadmap."
	doc := parseHTML(t, `<div data-automation="jobAdDetails">
		<p><strong>About the Role</strong></p>
		<p>`+para+`</p>
		<ul><li>Own the service</li></ul>
	</div>`)
	if got, want := strings.Join(doc.Duties, "|"), "Own the service"; got != want {
		t.Fatalf("duties = %q", got)
	}
	blob := strings.Join(append(append(append(doc.Benefits, doc.Duties...), doc.Requirements...), doc.Trailer...), "\n")
	if strings.Contains(blob, para) {
		t.Fatalf("bare paragraph was kept: %s", blob)
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

func parseHTML(t *testing.T, page string) Document {
	t.Helper()
	doc, err := Parse([]byte(page))
	if err != nil {
		t.Fatal(err)
	}
	return doc
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
