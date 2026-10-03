package jobad

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestPromptLabels(t *testing.T) {
	doc := Document{
		Header:       Header{TitleBlock: "System Analyst"},
		Benefits:     []string{"Medical"},
		Duties:       nil,
		Requirements: []string{"Know Go", "React (For Frontend)"},
	}
	got, err := PromptText(doc)
	if err != nil {
		t.Fatal(err)
	}
	want := "Title:\nSystem Analyst\n\nBenefits:\n- Medical\n\nDuties:\n(none)\n\nRequirements:\n- Know Go\n- React (For Frontend)\n"
	if got != want {
		t.Fatalf("prompt =\n%s\nwant =\n%s", got, want)
	}
	order := []string{"Title:\n", "Benefits:\n", "Duties:\n", "Requirements:\n"}
	at := 0
	for _, label := range order {
		i := strings.Index(got, label)
		if i < at {
			t.Fatalf("label %q at %d, previous %d", label, i, at)
		}
		at = i + len(label)
	}

	emptyBenefits := doc
	emptyBenefits.Benefits = []string{}
	emptyBenefits.Duties = []string{"Build services"}
	emptyBenefits.Requirements = []string{"Know Go"}
	got, err = PromptText(emptyBenefits)
	if err != nil {
		t.Fatal(err)
	}
	want = "Title:\nSystem Analyst\n\nBenefits:\n(none)\n\nDuties:\n- Build services\n\nRequirements:\n- Know Go\n"
	if got != want {
		t.Fatalf("empty benefits =\n%s\nwant =\n%s", got, want)
	}
}

func TestPromptOmitsTrailer(t *testing.T) {
	doc := Document{
		Source: "source-token-9f3a.html",
		Header: Header{
			TitleBlock:     "Analyst person@example.com",
			Location:       "LOCATION-TOKEN",
			WorkType:       "WORKTYPE-TOKEN",
			Classification: "CLASS-TOKEN",
		},
		Benefits:     []string{"Medical person@example.com cover"},
		Duties:       []string{"Build services"},
		Requirements: []string{"Know Go person@example.com"},
		Trailer:      []string{"TRAILER-SENTINEL", "hidden@example.com"},
	}
	got, err := PromptText(doc)
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{
		"source-token-9f3a.html",
		"TRAILER-SENTINEL",
		"person@example.com",
		"hidden@example.com",
		"LOCATION-TOKEN",
		"WORKTYPE-TOKEN",
		"CLASS-TOKEN",
		doc.Source,
	} {
		if strings.Contains(got, banned) {
			t.Fatalf("prompt contains %q\n%s", banned, got)
		}
	}
	for _, keep := range []string{"Analyst", "Medical cover", "Build services", "Know Go"} {
		if !strings.Contains(got, keep) {
			t.Fatalf("prompt missing %q\n%s", keep, got)
		}
	}
}

func TestPromptEmptyRequirements(t *testing.T) {
	docs := []Document{
		{},
		{Requirements: []string{}},
		{Requirements: []string{"", "   "}},
		{Requirements: []string{"person@example.com"}},
		{Requirements: nil, Trailer: []string{"still not a requirement"}},
	}
	for _, doc := range docs {
		got, err := PromptText(doc)
		if got != "" || !errors.Is(err, ErrRequirementsEmpty) || err.Error() != "docs-cli: requirements is empty" {
			t.Fatalf("doc %#v text=%q err=%v", doc.Requirements, got, err)
		}
	}
}

func TestAnalysisAcceptsNullYears(t *testing.T) {
	if strings.Contains(AnalysisSchema, "\n") || !json.Valid([]byte(AnalysisSchema)) {
		t.Fatalf("schema = %q", AnalysisSchema)
	}
	var schema map[string]any
	if err := json.Unmarshal([]byte(AnalysisSchema), &schema); err != nil {
		t.Fatal(err)
	}
	required, _ := schema["required"].([]any)
	if len(required) != 7 {
		t.Fatalf("schema required = %#v", schema["required"])
	}
	for _, phrase := range []string{
		"Benefits and Duties are for comparison only",
		"Requirements",
		"must_have",
		"advantage",
		"applies_to",
		"For Frontend",
		"will be an advantage",
		"familiar with",
		"min_years",
		`tech.level must be "must" or "advantage"`,
	} {
		if !strings.Contains(AnalysisInstructions, phrase) {
			t.Fatalf("instructions missing %q", phrase)
		}
	}
	if strings.Contains(AnalysisInstructions, "\n") {
		t.Fatal("instructions contain a newline")
	}

	stdout := []byte(`{
	  "must_have": ["Go"],
	  "advantage": ["AWS"],
	  "min_years": null,
	  "degree": null,
	  "languages": ["English"],
	  "tech": [
	    {"name": "React", "level": "must", "applies_to": "For Frontend"},
	    {"name": "SQL", "level": "advantage", "applies_to": null}
	  ],
	  "soft_skills": ["communication"]
	}`)
	got, err := ParseAnalysis(stdout)
	if err != nil {
		t.Fatal(err)
	}
	if got.MinYears != nil || got.Degree != nil {
		t.Fatalf("null years/degree = %#v %#v", got.MinYears, got.Degree)
	}
	if len(got.MustHave) != 1 || got.MustHave[0] != "Go" {
		t.Fatalf("must_have = %#v", got.MustHave)
	}
	if len(got.Advantage) != 1 || got.Advantage[0] != "AWS" {
		t.Fatalf("advantage = %#v", got.Advantage)
	}
	if len(got.Languages) != 1 || got.Languages[0] != "English" {
		t.Fatalf("languages = %#v", got.Languages)
	}
	if len(got.SoftSkills) != 1 || got.SoftSkills[0] != "communication" {
		t.Fatalf("soft_skills = %#v", got.SoftSkills)
	}
	if len(got.Tech) != 2 {
		t.Fatalf("tech = %#v", got.Tech)
	}
	if got.Tech[0].Name != "React" || got.Tech[0].Level != "must" || got.Tech[0].AppliesTo == nil || *got.Tech[0].AppliesTo != "For Frontend" {
		t.Fatalf("tech[0] = %#v", got.Tech[0])
	}
	if got.Tech[1].Name != "SQL" || got.Tech[1].Level != "advantage" || got.Tech[1].AppliesTo != nil {
		t.Fatalf("tech[1] = %#v", got.Tech[1])
	}

	years := 5
	degree := "Bachelor"
	numbered := []byte(`{"must_have":[],"advantage":[],"min_years":5,"degree":"Bachelor","languages":[],"tech":[],"soft_skills":[]}`)
	got, err = ParseAnalysis(numbered)
	if err != nil {
		t.Fatal(err)
	}
	if got.MinYears == nil || *got.MinYears != years || got.Degree == nil || *got.Degree != degree {
		t.Fatalf("numbered = %#v %#v", got.MinYears, got.Degree)
	}
	if got.MustHave == nil || got.Tech == nil {
		t.Fatal("empty arrays decoded as nil")
	}
}

func TestAnalysisRejectsBadLevel(t *testing.T) {
	base := `{
	  "must_have": ["Go"],
	  "advantage": [],
	  "min_years": null,
	  "degree": null,
	  "languages": [],
	  "tech": [{"name": "React", "level": "must", "applies_to": null}],
	  "soft_skills": []
	}`
	cases := []string{
		strings.Replace(base, `"level": "must"`, `"level": "required"`, 1),
		strings.Replace(base, `"level": "must"`, `"level": "Must"`, 1),
		strings.Replace(base, `"level": "must"`, `"level": ""`, 1),
		strings.Replace(base, `"name": "React", "level": "must", "applies_to": null`, `"name": "React", "applies_to": null`, 1),
		strings.Replace(base, `"name": "React", "level": "must", "applies_to": null`, `"name": "React", "level": "must"`, 1),
		strings.Replace(base, `"applies_to": null`, `"applies_to": null, "note": "x"`, 1),
		strings.Replace(base, `"tech": [{"name": "React", "level": "must", "applies_to": null}]`, `"tech": [null]`, 1),
	}
	for _, stdout := range cases {
		got, err := ParseAnalysis([]byte(stdout))
		if err == nil || !errors.Is(err, ErrAnalysisSchema) || err.Error() != "docs-cli: fm output is not the analysis schema" {
			t.Fatalf("stdout %s\ngot %#v err %v", stdout, got, err)
		}
	}
}

func TestAnalysisRejectsProse(t *testing.T) {
	cases := []string{
		"The candidate needs Go.",
		"",
		"   ",
		"{",
		"null",
		"[]",
		`{"must_have":[]}`,
		`{"must_have":[],"advantage":[],"min_years":null,"degree":null,"languages":[],"tech":[],"soft_skills":[],"notes":"x"}`,
		`{"must_have":null,"advantage":[],"min_years":null,"degree":null,"languages":[],"tech":[],"soft_skills":[]}`,
		`{"must_have":[],"advantage":[],"min_years":null,"degree":null,"languages":[],"tech":[],"soft_skills":[]}{"ok":true}`,
		`{"must_have":["Go",1],"advantage":[],"min_years":null,"degree":null,"languages":[],"tech":[],"soft_skills":[]}`,
		`{"must_have":[],"advantage":[],"min_years":1.5,"degree":null,"languages":[],"tech":[],"soft_skills":[]}`,
	}
	for _, stdout := range cases {
		got, err := ParseAnalysis([]byte(stdout))
		if err == nil || !errors.Is(err, ErrAnalysisSchema) {
			t.Fatalf("stdout %q got %#v err %v", stdout, got, err)
		}
	}
}
