package jobad

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ledongthuc/pdf"
)

func TestCheckDraftRequirementsEmpty(t *testing.T) {
	docs := []Document{
		{},
		{Requirements: []string{}},
		{Requirements: []string{"", "   "}},
		{Requirements: []string{"person@example.com"}},
		{Requirements: nil, Trailer: []string{"still not a requirement"}},
	}
	for _, doc := range docs {
		err := CheckDraftRequirements(doc)
		if !errors.Is(err, ErrRequirementsEmpty) || err.Error() != "docs-cli: requirements is empty" {
			t.Fatalf("doc %#v err=%v", doc.Requirements, err)
		}
	}
}

func TestDraftPromptShape(t *testing.T) {
	doc := Document{
		Header:       Header{TitleBlock: "System Analyst"},
		Duties:       nil,
		Requirements: []string{"Know Go", "React (For Frontend)"},
	}
	got, err := DraftPrompt(doc, "Dear team\nada@example.com", "CV-SKILL-TOKEN")
	if err != nil {
		t.Fatal(err)
	}
	want := "Title:\nSystem Analyst\n\nCompany:\n(none)\n\nDuties:\n(none)\n\nRequirements:\n- Know Go\n- React (For Frontend)\n\nCV:\nCV-SKILL-TOKEN\n\nTemplate:\nDear team\nada@example.com\n"
	if got != want {
		t.Fatalf("prompt =\n%s\nwant =\n%s", got, want)
	}
	order := []string{"Title:\n", "Company:\n", "Duties:\n", "Requirements:\n", "CV:\n", "Template:\n"}
	at := 0
	for _, label := range order {
		i := strings.Index(got, label)
		if i < at {
			t.Fatalf("label %q at %d, previous %d", label, i, at)
		}
		at = i + len(label)
	}
	if strings.Contains(got, "Benefits:") {
		t.Fatalf("prompt contains benefits label\n%s", got)
	}

	withDuties := doc
	withDuties.Duties = []string{"Build services"}
	withDuties.Requirements = []string{"Know Go"}
	got, err = DraftPrompt(withDuties, "ada@example.com", "CV-SKILL-TOKEN")
	if err != nil {
		t.Fatal(err)
	}
	want = "Title:\nSystem Analyst\n\nCompany:\n(none)\n\nDuties:\n- Build services\n\nRequirements:\n- Know Go\n\nCV:\nCV-SKILL-TOKEN\n\nTemplate:\nada@example.com\n"
	if got != want {
		t.Fatalf("duties prompt =\n%s\nwant =\n%s", got, want)
	}
}

func TestLayoutGlyphs(t *testing.T) {
	g := func(s string, x, y, w float64) pdf.Text {
		return pdf.Text{S: s, X: x, Y: y, W: w, FontSize: 12}
	}
	cases := []struct {
		name string
		in   []pdf.Text
		want string
	}{
		{
			name: "next word fits",
			in: []pdf.Text{
				g("edge", 0, 300, 100),
				g("short", 0, 280, 40),
				g("next", 0, 260, 30),
			},
			want: "edge\nshort\nnext",
		},
		{
			name: "narrow gap joins",
			in: []pdf.Text{
				g("edge", 0, 300, 100),
				g("short", 0, 280, 60),
				g("next", 0, 260, 50),
			},
			want: "edge\nshort next",
		},
		{
			name: "require gap joins",
			in: []pdf.Text{
				g("edge", 0, 300, 200),
				g("require", 0, 280, 158),
				g("additional", 0, 260, 47),
			},
			want: "edge\nrequire additional",
		},
		{
			name: "task gap stays",
			in: []pdf.Text{
				g("edge", 0, 300, 100),
				g("task].", 0, 280, 46),
				g("I", 0, 260, 3),
			},
			want: "edge\ntask].\nI",
		},
		{
			name: "blank lines are one paragraph break",
			in: []pdf.Text{
				g(" ", 0, 400, 4),
				g("Dear", 0, 360, 40),
				g(" ", 0, 340, 4),
				g(" ", 0, 320, 4),
				g("Body", 0, 280, 30),
				g(" ", 0, 260, 4),
			},
			want: "Dear\n\nBody",
		},
		{
			name: "trailing space trimmed",
			in: []pdf.Text{
				g("Hello", 0, 100, 40),
				g(" ", 40, 100, 4),
			},
			want: "Hello",
		},
		{
			name: "join trims the trailing space",
			in: []pdf.Text{
				g("edge", 0, 300, 100),
				g("short", 0, 280, 60),
				g(" ", 60, 280, 4),
				g("next", 0, 260, 50),
			},
			want: "edge\nshort next",
		},
		{
			name: "wide gap stays",
			in: []pdf.Text{
				g("edge", 0, 300, 200),
				g("head", 0, 280, 100),
				g("next", 0, 260, 150),
			},
			want: "edge\nhead\nnext",
		},
		{
			name: "right edge does not swallow",
			in: []pdf.Text{
				g("edge", 0, 300, 100),
				g("next", 0, 280, 20),
			},
			want: "edge\nnext",
		},
		{
			name: "top to bottom then left to right",
			in: []pdf.Text{
				g("b", 10, 100, 5),
				g("low", 0, 50, 10),
				g("a", 0, 100, 5),
			},
			want: "ab\nlow",
		},
		{
			name: "within one point is one line",
			in: []pdf.Text{
				g("b", 10, 99.5, 5),
				g("a", 0, 100, 5),
			},
			want: "ab",
		},
		{
			name: "one point is a new line",
			in: []pdf.Text{
				g("a", 0, 100, 20),
				g("b", 0, 99, 10),
			},
			want: "a\nb",
		},
		{
			name: "drop empty and newline glyphs",
			in: []pdf.Text{
				g("\n", 0, 100, 0),
				{S: "", X: 5, Y: 100, W: 5, FontSize: 12},
				g("b", 10, 100, 5),
				g("a", 0, 100, 5),
			},
			want: "ab",
		},
		{
			name: "x gap beats width",
			in: []pdf.Text{
				g("edge", 0, 200, 30),
				{S: "A", X: 0, Y: 180, W: 100, FontSize: 12},
				{S: "B", X: 8, Y: 180, W: 0, FontSize: 0},
				g("next", 0, 160, 20),
			},
			want: "edge\nAB next",
		},
		{
			name: "font size is the advance",
			in: []pdf.Text{
				g("edge", 0, 200, 30),
				{S: "Z", X: 0, Y: 180, W: 0, FontSize: 12},
				g("next", 0, 160, 20),
			},
			want: "edge\nZ next",
		},
	}
	for _, c := range cases {
		got := layoutPage(c.in)
		if got != c.want || strings.HasSuffix(got, "\n") {
			t.Fatalf("%s =\n%q\nwant =\n%q", c.name, got, c.want)
		}
	}

	got := layoutPages([][]pdf.Text{
		{g("one", 0, 100, 20)},
		nil,
		{g(" ", 0, 100, 4)},
		{g("two", 0, 80, 20)},
	})
	if got != "one\n\ntwo" || strings.HasSuffix(got, "\n") {
		t.Fatalf("pages =\n%q", got)
	}
}

func TestLayoutLetter(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"trailing blank line", "hi\n\n", "hi\n\n"},
		{"trim trailing spaces", "Dear team, \n\nI write. \n", "Dear team,\n\nI write.\n"},
		{"drop placeholder line", "Yours sincerely, \n[Your name]\n", "Yours sincerely,\n"},
		{"collapse blank lines", "a\n\n\nb", "a\n\nb"},
		{"placeholder inside a sentence", "See [job board] today.\n", "See [job board] today.\n"},
		{"empty brackets stay", "[]\n", "[]\n"},
		{"placeholder between lines", "a\n[job board]\nb", "a\nb"},
		{"trim a trailing tab", "Dear team,\t\nI write.", "Dear team,\nI write."},
		{"crlf", "a\r\n\r\n\r\nb", "a\n\nb"},
		{"keep a leading space", "  hi", "  hi"},
	}
	for _, c := range cases {
		got, err := layoutLetter(c.in)
		if err != nil || got != c.want {
			t.Fatalf("%s = %q err %v\nwant %q", c.name, got, err, c.want)
		}
	}
	for _, in := range []string{"[Your name]", "[job board]\n", " \n\t", "[Your name]\n\n"} {
		got, err := layoutLetter(in)
		if got != "" || !errors.Is(err, ErrDraftSchema) || err.Error() != "docs-cli: fm output is not the draft schema" {
			t.Fatalf("in %q got %q err %v", in, got, err)
		}
	}
}

func TestDraftAppliesLayout(t *testing.T) {
	doc := Document{Requirements: []string{"Know Go"}}
	calls := 0
	SetFMExecForTest(func([]string) ([]byte, []byte, int) {
		calls++
		if calls == 1 {
			return []byte("ok"), nil, 0
		}
		return []byte("{\"letter\":\"Yours sincerely, \\n[Your name]\\n\"}\n"), nil, 0
	})
	t.Cleanup(restoreFMStub)

	got, err := Draft(doc, "ada@example.com", "CV-SKILL-TOKEN")
	if err != nil || got != "Yours sincerely,\n" {
		t.Fatalf("got %q err %v", got, err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d", calls)
	}

	calls = 0
	SetFMExecForTest(func([]string) ([]byte, []byte, int) {
		calls++
		if calls == 1 {
			return []byte("ok"), nil, 0
		}
		return []byte(`{"letter":"[Your name]"}`), nil, 0
	})
	got, err = Draft(doc, "ada@example.com", "CV-SKILL-TOKEN")
	if got != "" || !errors.Is(err, ErrDraftSchema) || err.Error() != "docs-cli: fm output is not the draft schema" {
		t.Fatalf("blank got %q err %v", got, err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d", calls)
	}
}

func TestDraftPromptOmitsUnusedFields(t *testing.T) {
	doc := Document{
		Source: "source-token-9f3a.html",
		Header: Header{
			TitleBlock:     "System Analyst",
			Company:        "COMPANY-TOKEN",
			Location:       "LOCATION-TOKEN",
			WorkType:       "WORKTYPE-TOKEN",
			Classification: "CLASS-TOKEN",
		},
		Benefits:     []string{"BENEFITS-TOKEN"},
		Duties:       []string{"Build services"},
		Requirements: []string{"Know Go"},
		Trailer:      []string{"TRAILER-SENTINEL"},
	}
	got, err := DraftPrompt(doc, "ada@example.com", "CV-SKILL-TOKEN")
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{
		"source-token-9f3a.html",
		"LOCATION-TOKEN",
		"WORKTYPE-TOKEN",
		"CLASS-TOKEN",
		"BENEFITS-TOKEN",
		"TRAILER-SENTINEL",
		"Benefits:",
		"Offer:",
	} {
		if strings.Contains(got, banned) {
			t.Fatalf("prompt contains %q\n%s", banned, got)
		}
	}
	for _, keep := range []string{"Title:\nSystem Analyst", "Company:\nCOMPANY-TOKEN", "Duties:\n- Build services", "Requirements:\n- Know Go", "CV:\nCV-SKILL-TOKEN", "Template:\nada@example.com"} {
		if !strings.Contains(got, keep) {
			t.Fatalf("prompt missing %q\n%s", keep, got)
		}
	}
}

func TestDraftPromptRedactsJobNotTemplate(t *testing.T) {
	doc := Document{
		Header: Header{
			TitleBlock: "Analyst person@example.com",
			Company:    "Acme person@example.com Ltd",
		},
		Duties:       []string{"Mail person@example.com daily"},
		Requirements: []string{"person@example.com", "Know Go"},
	}
	cv := "Led the ERP in C#.\nada@example.com"
	got, err := DraftPrompt(doc, "Dear team,\n", cv)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "person@example.com") {
		t.Fatalf("prompt contains job email\n%s", got)
	}
	for _, keep := range []string{
		"Analyst",
		"Mail daily",
		"Know Go",
		"Company:\nAcme Ltd",
		"CV:\n" + cv + "\n",
		"Template:\nDear team,\n",
	} {
		if !strings.Contains(got, keep) {
			t.Fatalf("prompt missing %q\n%s", keep, got)
		}
	}
	if strings.Index(got, "CV:\n") > strings.Index(got, "Template:\n") {
		t.Fatalf("CV is not before Template\n%s", got)
	}

	emailOnly := doc
	emailOnly.Header.Company = "person@example.com"
	got, err = DraftPrompt(emailOnly, "Dear team,\n", cv)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "person@example.com") {
		t.Fatalf("prompt contains job email\n%s", got)
	}
	if !strings.Contains(got, "Company:\n(none)\n") {
		t.Fatalf("prompt =\n%s", got)
	}
}

func TestExtractCoverMin(t *testing.T) {
	got, err := ExtractTemplateText("testdata/cover_min.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if got != "DRAFT-TEMPLATE-SENTINEL" {
		t.Fatalf("text = %q", got)
	}
	if strings.Contains(got, "ada@example.com") {
		t.Fatalf("fixture contains a contact email %q", got)
	}
}

func TestExtractEmptyText(t *testing.T) {
	dir := t.TempDir()
	blank := filepath.Join(dir, "blank.pdf")
	if err := os.WriteFile(blank, textPDF([]string{"", "", ""}), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ExtractTemplateText(blank)
	if !errors.Is(err, ErrTemplateNoText) || err.Error() != "docs-cli: template has no text" {
		t.Fatalf("blank err = %v", err)
	}
	spaces := filepath.Join(dir, "spaces.pdf")
	if err := os.WriteFile(spaces, textPDF([]string{"   "}), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = ExtractTemplateText(spaces)
	if !errors.Is(err, ErrTemplateNoText) {
		t.Fatalf("spaces err = %v", err)
	}
}

func TestExtractTooLarge(t *testing.T) {
	dir := t.TempDir()
	over := filepath.Join(dir, "over.bin")
	if err := os.WriteFile(over, bytes.Repeat([]byte("x"), maxTemplateBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ExtractTemplateText(over)
	if !errors.Is(err, ErrTemplateTooLarge) || err.Error() != "docs-cli: template exceeds 1 MiB" {
		t.Fatalf("over err = %v", err)
	}
	exact := filepath.Join(dir, "exact.bin")
	if err := os.WriteFile(exact, bytes.Repeat([]byte("x"), maxTemplateBytes), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = ExtractTemplateText(exact)
	if !errors.Is(err, ErrTemplateNoText) {
		t.Fatalf("exact err = %v", err)
	}
}

func TestExtractUnreadable(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(bad, []byte("not a pdf"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ExtractTemplateText(bad)
	if !errors.Is(err, ErrTemplateNoText) || err.Error() != "docs-cli: template has no text" {
		t.Fatalf("not a pdf err = %v", err)
	}
	_, err = ExtractTemplateText(dir)
	if !errors.Is(err, ErrTemplateNoText) {
		t.Fatalf("dir err = %v", err)
	}
	missing := filepath.Join(dir, "missing.pdf")
	_, err = ExtractTemplateText(missing)
	if err == nil || errors.Is(err, ErrTemplateNoText) || !os.IsNotExist(err) {
		t.Fatalf("missing err = %v", err)
	}
	panicPDF := filepath.Join(dir, "panic.pdf")
	if err := os.WriteFile(panicPDF, rawPDF([]string{"BT Tj ET"}), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = ExtractTemplateText(panicPDF)
	if !errors.Is(err, ErrTemplateNoText) || err.Error() != "docs-cli: template has no text" {
		t.Fatalf("panic err = %v", err)
	}
}

func TestDraftSkipsFMWhenRequirementsEmpty(t *testing.T) {
	calls := 0
	SetFMExecForTest(func([]string) ([]byte, []byte, int) {
		calls++
		return nil, []byte("should not run"), 1
	})
	t.Cleanup(restoreFMStub)

	docs := []Document{
		{},
		{Requirements: []string{}},
		{Requirements: []string{"person@example.com"}, Trailer: []string{"TRAILER-SENTINEL"}},
	}
	for _, doc := range docs {
		got, err := Draft(doc, "ada@example.com", "")
		if got != "" || !errors.Is(err, ErrRequirementsEmpty) {
			t.Fatalf("doc %#v got %q err %v", doc.Requirements, got, err)
		}
	}
	if calls != 0 {
		t.Fatalf("calls = %d", calls)
	}
}

func TestDraftSkipsFMWhenCVEmpty(t *testing.T) {
	calls := 0
	SetFMExecForTest(func([]string) ([]byte, []byte, int) {
		calls++
		return nil, []byte("should not run"), 1
	})
	t.Cleanup(restoreFMStub)

	doc := Document{Requirements: []string{"Know Go"}}
	for _, cv := range []string{"", "   ", "\n\t"} {
		got, err := Draft(doc, "ada@example.com", cv)
		if got != "" || !errors.Is(err, ErrCVNoText) || err.Error() != "docs-cli: cv has no text" {
			t.Fatalf("cv %q got %q err %v", cv, got, err)
		}
		got, err = DraftPrompt(doc, "ada@example.com", cv)
		if got != "" || !errors.Is(err, ErrCVNoText) {
			t.Fatalf("prompt cv %q got %q err %v", cv, got, err)
		}
	}
	got, err := Draft(Document{}, "ada@example.com", "")
	if got != "" || !errors.Is(err, ErrRequirementsEmpty) {
		t.Fatalf("empty requirements got %q err %v", got, err)
	}
	if calls != 0 {
		t.Fatalf("calls = %d", calls)
	}
}

func TestDraftAvailableThenRespond(t *testing.T) {
	doc := Document{
		Source: "source-token-9f3a.html",
		Header: Header{
			TitleBlock:     "System Analyst",
			Company:        "COMPANY-TOKEN",
			Location:       "LOCATION-TOKEN",
			WorkType:       "WORKTYPE-TOKEN",
			Classification: "CLASS-TOKEN",
		},
		Benefits:     []string{"BENEFITS-TOKEN person@example.com"},
		Duties:       []string{"Build services"},
		Requirements: []string{"Know Go"},
		Trailer:      []string{"TRAILER-SENTINEL"},
	}
	text, err := DraftPrompt(doc, "ada@example.com", "CV-SKILL-TOKEN")
	if err != nil {
		t.Fatal(err)
	}
	model := []byte("{\"letter\":\"hi\\n\\n\"}\n")
	var calls [][]string
	SetFMExecForTest(func(argv []string) ([]byte, []byte, int) {
		calls = append(calls, append([]string(nil), argv...))
		if len(calls) == 1 {
			return []byte("not a letter"), []byte("available noise"), 0
		}
		return append([]byte(nil), model...), []byte("respond noise"), 0
	})
	t.Cleanup(restoreFMStub)

	got, err := Draft(doc, "ada@example.com", "CV-SKILL-TOKEN")
	if err != nil {
		t.Fatal(err)
	}
	if got != "hi\n\n" {
		t.Fatalf("letter = %q", got)
	}
	if len(calls) != 2 {
		t.Fatalf("calls = %d", len(calls))
	}
	if !slices.Equal(calls[0], []string{fmPath, "available"}) {
		t.Fatalf("available = %#v", calls[0])
	}
	want := []string{
		fmPath, "respond", "--no-stream", "--use-case", "general",
		"--instructions", DraftInstructions,
		"--schema", LetterSchema,
		"--text", text,
	}
	if !slices.Equal(calls[1], want) {
		t.Fatalf("respond = %#v", calls[1])
	}
	const wantSchema = `{"x-order":["letter"],"title":"Letter","additionalProperties":false,"required":["letter"],"properties":{"letter":{"type":"string"}},"type":"object"}`
	if LetterSchema != wantSchema || LetterSchema == AnalysisSchema {
		t.Fatalf("schema = %q", LetterSchema)
	}
	const wantInstructions = `The template is a pattern for structure and voice only, not text to paste. Write the whole letter for this job. The template's blank line is a paragraph break. Keep it. A single newline is a line break. Keep it. Do not join two blocks that the template separates with a blank line. Do not insert a blank line where the template has only a single newline. Do not put each word on its own line. Do not put a trailing space on any line. Do not write a line that is only a space. Keep the template's language. A square-bracket placeholder is a blank, not text to copy. Do not leave any square-bracket placeholder in the letter. Fill the letter from Title, Company, Duties, Requirements, and the CV. Fill a name, email, phone, or address blank only from the CV, and only when the CV states that fact as real text. Put each kept contact line on its own line. When the CV does not state that fact, omit the line. Do not copy the CV's expected salary, previous salary, or available date into the letter. Do not copy a CV section heading into the letter. Read the CV and choose only the skills, projects, and achievements that match this job's Duties and Requirements. Do not paste the CV. Do not claim a skill, employer, project, metric, degree, or year that the CV does not state. Title is the role being applied for, not the applicant's current job. Use a current job title only when the CV states it. Company is this job's employer. When Company is not (none), use that name as the employer. Do not take the employer from a parenthetical in Title, from the template's previous role, or from an employer named in the CV. When Company is (none), do not invent an employer and do not write (none) into the letter. Keep the template's paragraph order. In those paragraphs, cover three things: the chosen experience and skills from the CV, tied to this job; interest in the employer and fit for the role, using only Company, Duties, Requirements, and those CV facts; and a polite close with a call to action in the template's closing shape. Do not add a paragraph the template does not separate, and do not drop a body paragraph the template separates with a blank line. Do not invent a recipient, job board, compliment, award, date, or meeting time that appears in neither the template, the CV, nor the JSON. Do not turn a required number of years into the applicant's years. When a blank has no fact in the template, the CV, or the JSON, omit that blank. The letter field is the whole letter and nothing else.`
	if DraftInstructions != wantInstructions || strings.Contains(DraftInstructions, "\n") {
		t.Fatalf("instructions = %q", DraftInstructions)
	}
	if !strings.Contains(calls[1][len(calls[1])-1], "Company:\nCOMPANY-TOKEN") {
		t.Fatal("company was omitted")
	}
	for _, arg := range calls[1] {
		for _, banned := range []string{
			"TRAILER-SENTINEL",
			"BENEFITS-TOKEN",
			"LOCATION-TOKEN",
			"WORKTYPE-TOKEN",
			"CLASS-TOKEN",
			doc.Source,
			"person@example.com",
		} {
			if strings.Contains(arg, banned) {
				t.Fatalf("argv contains %q\n%s", banned, arg)
			}
		}
	}
	if !strings.Contains(calls[1][len(calls[1])-1], "ada@example.com") {
		t.Fatal("template email was removed")
	}
	if !strings.Contains(calls[1][len(calls[1])-1], "CV:\nCV-SKILL-TOKEN\n") {
		t.Fatal("cv text was omitted")
	}
}

func TestDraftAvailableFailureDoesNotRespond(t *testing.T) {
	var calls [][]string
	SetFMExecForTest(func(argv []string) ([]byte, []byte, int) {
		calls = append(calls, append([]string(nil), argv...))
		return []byte("ignored"), []byte("not licensed\n"), 1
	})
	t.Cleanup(restoreFMStub)

	got, err := Draft(Document{Requirements: []string{"Know Go"}}, "ada@example.com", "CV-SKILL-TOKEN")
	if got != "" || err == nil || err.Error() != "docs-cli: fm: not licensed" {
		t.Fatalf("got %q err %v", got, err)
	}
	if len(calls) != 1 || !slices.Equal(calls[0], []string{fmPath, "available"}) {
		t.Fatalf("calls = %#v", calls)
	}
}

func TestRespondArgvFits(t *testing.T) {
	fit := []string{strings.Repeat("A", 917478)}
	if got := fmStringCharge(fit); got != 917487 {
		t.Fatalf("string charge = %d", got)
	}
	if got := fmArgCharge(fit, nil); got != 917503 {
		t.Fatalf("full charge = %d", got)
	}
	if fmArgExceeds(fit, nil) {
		t.Fatal("917503 should fit")
	}
	over := []string{strings.Repeat("A", 917479)}
	if got := fmArgCharge(over, nil); got != 917504 {
		t.Fatalf("full charge = %d", got)
	}
	if !fmArgExceeds(over, nil) {
		t.Fatal("917504 should not fit")
	}
	if !fmArgExceeds([]string{"fm"}, []string{strings.Repeat("E", kernArgMax)}) {
		t.Fatal("1048576-byte env should not fit")
	}
	text, err := DraftPrompt(Document{
		Header:       Header{TitleBlock: "System Analyst"},
		Requirements: []string{"Know Go"},
	}, "ada@example.com", "CV-SKILL-TOKEN")
	if err != nil {
		t.Fatal(err)
	}
	if fmArgExceeds(respondArgv(text), nil) {
		t.Fatal("short respond argv should fit")
	}
}

func TestDraftArgLimitSkipsFM(t *testing.T) {
	calls := 0
	SetFMExecForTest(func([]string) ([]byte, []byte, int) {
		calls++
		return nil, []byte("should not run"), 1
	})
	t.Cleanup(restoreFMStub)
	prev := draftEnviron
	t.Cleanup(func() { draftEnviron = prev })

	doc := Document{Requirements: []string{"Know Go"}}
	draftEnviron = func() []string { return []string{strings.Repeat("E", kernArgMax)} }
	got, err := Draft(doc, "ada@example.com", "CV-SKILL-TOKEN")
	if got != "" || !errors.Is(err, ErrFMArgLimit) || err.Error() != "docs-cli: fm text exceeds the argument limit" {
		t.Fatalf("env got %q err %v", got, err)
	}

	draftEnviron = func() []string { return nil }
	got, err = Draft(doc, strings.Repeat("Q", 917488), "CV-SKILL-TOKEN")
	if got != "" || !errors.Is(err, ErrFMArgLimit) {
		t.Fatalf("text got %q err %v", got, err)
	}
	if calls != 0 {
		t.Fatalf("calls = %d", calls)
	}
}

func TestDraftSourceDoesNotCallGemini(t *testing.T) {
	src, err := os.ReadFile("draft.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{
		"MapSections",
		"LoadGeminiKey",
		"generativelanguage",
		"net/http",
	} {
		if strings.Contains(string(src), banned) {
			t.Fatalf("draft.go contains %q", banned)
		}
	}
}

func TestParseLetter(t *testing.T) {
	got, err := ParseLetter([]byte("{\"letter\":\"hi\\n\\n\"}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got != "hi\n\n" {
		t.Fatalf("letter = %q", got)
	}
	got, err = ParseLetter([]byte("  {\"letter\":\"hi\"}"))
	if err != nil || got != "hi" {
		t.Fatalf("letter = %q err %v", got, err)
	}
}

func TestParseLetterRejects(t *testing.T) {
	bad := [][]byte{
		nil,
		[]byte("   "),
		[]byte("The letter is ready."),
		[]byte(`{"letter":""}`),
		[]byte(`{}`),
		[]byte(`{"letter":null}`),
		[]byte(`{"letter":"hi","extra":true}`),
		[]byte(`{"letter":"hi"}{"letter":"no"}`),
		[]byte(`{"letter":"hi"`),
	}
	for _, stdout := range bad {
		got, err := ParseLetter(stdout)
		if got != "" || !errors.Is(err, ErrDraftSchema) || err.Error() != "docs-cli: fm output is not the draft schema" {
			t.Fatalf("stdout %q got %q err %v", stdout, got, err)
		}
	}
}

func textPDF(pages []string) []byte {
	streams := make([]string, len(pages))
	for i, text := range pages {
		if text != "" {
			streams[i] = "BT (" + text + ") Tj ET"
		}
	}
	return rawPDF(streams)
}

func rawPDF(streams []string) []byte {
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offsets := []int{0}
	writeObj := func(body string) {
		offsets = append(offsets, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", len(offsets)-1, body)
	}
	nPages := len(streams)
	kids := make([]string, nPages)
	for i := 0; i < nPages; i++ {
		kids[i] = fmt.Sprintf("%d 0 R", 3+2*i)
	}
	writeObj("<< /Type /Catalog /Pages 2 0 R >>")
	writeObj(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), nPages))
	for i, stream := range streams {
		contentNum := 4 + 2*i
		writeObj(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 144] /Contents %d 0 R >>", contentNum))
		writeObj(fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream))
	}
	xrefAt := b.Len()
	n := len(offsets)
	fmt.Fprintf(&b, "xref\n0 %d\n", n)
	fmt.Fprintf(&b, "0000000000 65535 f \n")
	for i := 1; i < n; i++ {
		fmt.Fprintf(&b, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\n", n)
	fmt.Fprintf(&b, "startxref\n%d\n%%%%EOF\n", xrefAt)
	return b.Bytes()
}
