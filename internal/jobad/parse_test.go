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

func TestParseJSONLD(t *testing.T) {
	const work = `[ "TEMPORARY", "CONTRACTOR" ]`
	page := `<title>System Analyst at CTgoodjobs</title>
<meta property="og:title" content="OG System Analyst">
<script>loginId</script>
<script type="application/ld+json">{not json</script>
<script type="application/ld+json">` + mustJSON(t, map[string]any{
		"@context": "https://schema.org",
		"@type":    "JobPosting",
		"title":    "System Analyst",
		"hiringOrganization": map[string]any{
			"@type": "Organization",
			"name":  "Acme person@example.com Ltd",
			"logo":  "https://example.test/logo.gif",
		},
		"jobLocation": []any{
			map[string]any{
				"@type": "Place",
				"address": map[string]any{
					"addressLocality": "Wan Chai",
					"streetAddress":   "1 Harbour Road",
					"addressRegion":   "Hong Kong",
					"postalCode":      "999077",
				},
				"geo": map[string]any{"latitude": 22.2776, "longitude": 114.1729},
			},
		},
		"employmentType":       work,
		"occupationalCategory": "Analysts",
		"baseSalary":           map[string]any{"currency": "HKD"},
		"url":                  "https://example.test/job",
		"description": `<p><strong>Benefits</strong></p><ul><li>Medical cover</li></ul>` +
			`<p><strong>Key Responsibilities</strong></p><ul><li>Build the API</li></ul>` +
			`<p><strong>Requirements</strong></p><ul><li>Know Go</li><li>Write to person@example.com today</li></ul>` +
			`<p>Apply on the site</p>`,
	}) + `</script>
<script type="application/ld+json">` + mustJSON(t, map[string]any{
		"@type": "JobPosting",
		"title": "Second posting",
	}) + `</script>`
	doc := parseHTML(t, page)
	if doc.Source != "" {
		t.Fatalf("source = %q", doc.Source)
	}
	if doc.Header.TitleBlock != "System Analyst" {
		t.Fatalf("title_block = %q", doc.Header.TitleBlock)
	}
	if doc.Header.Company != "Acme Ltd" {
		t.Fatalf("company = %q", doc.Header.Company)
	}
	if doc.Header.Location != "Wan Chai" {
		t.Fatalf("location = %q", doc.Header.Location)
	}
	if doc.Header.WorkType != work {
		t.Fatalf("work_type = %q", doc.Header.WorkType)
	}
	if doc.Header.Classification != "Analysts" {
		t.Fatalf("classification = %q", doc.Header.Classification)
	}
	if len(doc.Benefits) != 0 {
		t.Fatalf("benefits = %#v", doc.Benefits)
	}
	if got, want := strings.Join(doc.Duties, "|"), "Build the API"; got != want {
		t.Fatalf("duties = %q", got)
	}
	if got, want := strings.Join(doc.Requirements, "|"), "Know Go|Write to today"; got != want {
		t.Fatalf("requirements = %q", got)
	}
	if got, want := strings.Join(doc.Trailer, "|"), "Medical cover|Apply on the site"; got != want {
		t.Fatalf("trailer = %q", got)
	}
	raw := string(marshal(t, doc))
	for _, leak := range []string{"CTgoodjobs", "OG System Analyst", "Second posting", "person@example.com", "loginId", "999077", "22.2776", "logo.gif", "HKD", "1 Harbour Road", "example.test"} {
		if strings.Contains(raw, leak) {
			t.Fatalf("json contains %s:\n%s", leak, raw)
		}
	}
}

func TestParseJSONLDFallbacks(t *testing.T) {
	page := `<script type="application/ld+json">` + mustJSON(t, map[string]any{
		"@context": "https://schema.org",
		"@graph": []any{
			map[string]any{"@type": "Organization", "name": "Not a job"},
			map[string]any{
				"@type": "JobPosting",
				"title": "From Graph",
				"jobLocation": map[string]any{
					"address": map[string]any{
						"streetAddress": "1 Road",
						"addressRegion": "Hong Kong",
						"postalCode":    "999077",
					},
				},
				"employmentType":       []any{"TEMPORARY", "CONTRACTOR"},
				"occupationalCategory": "ICT",
			},
			map[string]any{"@type": "JobPosting", "title": "Later title"},
		},
	}) + `</script>`
	doc := parseHTML(t, page)
	if doc.Header.TitleBlock != "From Graph" || doc.Header.Location != "1 Road" || doc.Header.WorkType != "TEMPORARY, CONTRACTOR" || doc.Header.Classification != "ICT" {
		t.Fatalf("header = %+v", doc.Header)
	}
	if !sectionsEmpty(doc) {
		t.Fatalf("lists = %#v %#v %#v %#v", doc.Benefits, doc.Duties, doc.Requirements, doc.Trailer)
	}

	region := `<script type="application/ld+json">` + mustJSON(t, map[string]any{
		"@type": []any{"JobPosting"},
		"title": "Region role",
		"jobLocation": []any{
			map[string]any{"address": map[string]any{"addressLocality": "  ", "streetAddress": "", "addressRegion": "Kowloon"}},
		},
		"occupationalCategory": []any{"Not a string"},
	}) + `</script>`
	doc = parseHTML(t, region)
	if doc.Header.TitleBlock != "Region role" || doc.Header.Location != "Kowloon" || doc.Header.Classification != "" || doc.Header.WorkType != "" {
		t.Fatalf("header = %+v", doc.Header)
	}
}

func TestParseJSONLDHeaderWithoutBlocks(t *testing.T) {
	page := `<title>CTgoodjobs</title><script type="application/ld+json">` + mustJSON(t, map[string]any{
		"@type":       "JobPosting",
		"title":       "Only Title",
		"description": "<p><strong>Key Responsibilities</strong></p>",
	}) + `</script>`
	doc := parseHTML(t, page)
	if doc.Header.TitleBlock != "Only Title" || doc.Header.Company != "" {
		t.Fatalf("header = %+v", doc.Header)
	}
	if !sectionsEmpty(doc) {
		t.Fatalf("lists = %#v %#v %#v %#v", doc.Benefits, doc.Duties, doc.Requirements, doc.Trailer)
	}
	if _, err := CollectBlocks([]byte(page)); err != nil {
		t.Fatal(err)
	}
	blocks, err := CollectBlocks([]byte(page))
	if err != nil || len(blocks) != 0 {
		t.Fatalf("blocks = %#v err %v", blocks, err)
	}
}

func TestParseJobAdDetailsIgnoresJSONLD(t *testing.T) {
	page := `<script type="application/ld+json">` + mustJSON(t, map[string]any{
		"@type": "JobPosting",
		"title": "From JSON",
		"hiringOrganization": map[string]any{
			"name": "JSON Co",
		},
		"description": "<p><strong>Key Responsibilities</strong></p><ul><li>FROM-JSON</li></ul>",
	}) + `</script>
<span data-automation="job-detail-title">From DOM</span>
<span data-automation="advertiser-name">DOM Co</span>
<div data-automation="jobAdDetails">
<p><strong>Job Description</strong></p>
<ul><li>FROM-DOM</li></ul>
</div>`
	doc := parseHTML(t, page)
	if doc.Header.TitleBlock != "From DOM" || doc.Header.Company != "DOM Co" {
		t.Fatalf("header = %+v", doc.Header)
	}
	if strings.Join(doc.Duties, "|") != "FROM-DOM" {
		t.Fatalf("duties = %#v", doc.Duties)
	}
	raw := string(marshal(t, doc))
	if strings.Contains(raw, "From JSON") || strings.Contains(raw, "JSON Co") || strings.Contains(raw, "FROM-JSON") {
		t.Fatalf("json-ld leaked:\n%s", raw)
	}
}

func TestParseBodySkipsChrome(t *testing.T) {
	page := `<title>Site Name Jobs</title>
<meta property="og:title" content="OG Site">
<nav><p>NAV-P</p><ul><li>NAV-LI</li></ul></nav>
<header><p>HEAD-P</p></header>
<footer><li>FOOT-LI</li></footer>
<div role="navigation"><p>ROLE-P</p><ul><li>ROLE-LI</li></ul></div>
<p><strong>Key Responsibilities</strong></p>
<ul><li>Ship the work</li></ul>
<p><strong>Requirements</strong></p>
<ul><li>Know Go</li></ul>
<p>After requirements</p>`
	doc := parseHTML(t, page)
	if doc.Header != (Header{}) {
		t.Fatalf("header = %+v", doc.Header)
	}
	if strings.Join(doc.Duties, "|") != "Ship the work" {
		t.Fatalf("duties = %#v", doc.Duties)
	}
	if strings.Join(doc.Requirements, "|") != "Know Go" {
		t.Fatalf("requirements = %#v", doc.Requirements)
	}
	if strings.Join(doc.Trailer, "|") != "After requirements" {
		t.Fatalf("trailer = %#v", doc.Trailer)
	}
	if len(doc.Benefits) != 0 {
		t.Fatalf("benefits = %#v", doc.Benefits)
	}
	raw := string(marshal(t, doc))
	for _, leak := range []string{"Site Name", "OG Site", "NAV-P", "NAV-LI", "HEAD-P", "FOOT-LI", "ROLE-P", "ROLE-LI"} {
		if strings.Contains(raw, leak) {
			t.Fatalf("json contains %s:\n%s", leak, raw)
		}
	}
}

func TestParsePlainParagraph(t *testing.T) {
	doc := parseHTML(t, `<title>Site Name</title><p>no ad</p>`)
	if doc.Header != (Header{}) || strings.Join(doc.Trailer, "|") != "no ad" {
		t.Fatalf("doc = %+v", doc)
	}
	if len(doc.Benefits) != 0 || len(doc.Duties) != 0 || len(doc.Requirements) != 0 {
		t.Fatalf("lists = %#v %#v %#v", doc.Benefits, doc.Duties, doc.Requirements)
	}
	_, err := Parse([]byte(`<div>no ad</div>`))
	if !errors.Is(err, ErrJobAdDetailsNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestParseTaleoInitialHistory(t *testing.T) {
	page := `<title>Careers at Hospital Authority</title>
<meta property="og:title" content="OG Supplies Supervisor">
<nav><p>CHROME-NOTICE</p><ul><li>CHROME-LI</li></ul></nav>
<input type="hidden" name="initialHistory" id="initialHistory" value="ftlx0!|!100%!|!!*!<p><strong>Application Deadline: 16 October 2026</strong></p><p><strong>Post:</strong> Supplies Supervisor II</p><p><strong>Key Responsibilities\:</strong></p><p>Buy supplies</p><p><strong>Entry Requirements:</strong></p><p>Know stores person@example.com</p><p><strong>OR</strong></p><p>Second path</p><p><strong>Preferable attributes:</strong></p><p>Excel helps</p>">`
	doc := parseHTML(t, page)
	if doc.Header.TitleBlock != "Supplies Supervisor II" || doc.Header.Company != "" || doc.Header.Location != "" {
		t.Fatalf("header = %+v", doc.Header)
	}
	if strings.Join(doc.Duties, "|") != "Buy supplies" {
		t.Fatalf("duties = %#v", doc.Duties)
	}
	if strings.Join(doc.Requirements, "|") != "Know stores|OR|Second path" {
		t.Fatalf("requirements = %#v", doc.Requirements)
	}
	if strings.Join(doc.Trailer, "|") != "Application Deadline: 16 October 2026|Post: Supplies Supervisor II|Excel helps" {
		t.Fatalf("trailer = %#v", doc.Trailer)
	}
	if len(doc.Benefits) != 0 {
		t.Fatalf("benefits = %#v", doc.Benefits)
	}
	raw := string(marshal(t, doc))
	for _, leak := range []string{"CHROME-NOTICE", "CHROME-LI", "Careers at Hospital Authority", "OG Supplies Supervisor", "person@example.com", "initialHistory", "Key Responsibilities"} {
		if strings.Contains(raw, leak) {
			t.Fatalf("json contains %s:\n%s", leak, raw)
		}
	}

	blocks, err := CollectBlocks([]byte(page))
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, len(blocks))
	for i, block := range blocks {
		got[i] = block.Heading + " | " + block.Text
	}
	want := []string{
		" | Application Deadline: 16 October 2026",
		"Application Deadline: 16 October 2026 | Post: Supplies Supervisor II",
		"Key Responsibilities: | Buy supplies",
		"Entry Requirements: | Know stores",
		"Entry Requirements: | OR",
		"Entry Requirements: | Second path",
		"Preferable attributes: | Excel helps",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("blocks = %#v", got)
	}

	kept := `<p><strong>Requirements</strong></p><ul><li>Know Go</li></ul>
<input type="hidden" name="initialHistory" value="!*!<p>SECRET-HISTORY</p>">`
	doc = parseHTML(t, kept)
	if strings.Join(doc.Requirements, "|") != "Know Go" {
		t.Fatalf("requirements = %#v", doc.Requirements)
	}
	if strings.Contains(string(marshal(t, doc)), "SECRET-HISTORY") {
		t.Fatalf("history replaced a page that already had requirements:\n%s", marshal(t, doc))
	}

	jobsdb := `<input type="hidden" name="initialHistory" value="!*!<p>SECRET-HISTORY</p>">
<div data-automation="jobAdDetails"></div>`
	doc = parseHTML(t, jobsdb)
	if !sectionsEmpty(doc) || strings.Contains(string(marshal(t, doc)), "SECRET-HISTORY") {
		t.Fatalf("jobsdb doc = %+v", doc)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
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
