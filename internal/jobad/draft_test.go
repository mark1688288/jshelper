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
	got, err := DraftPrompt(doc, "Dear team\nada@example.com")
	if err != nil {
		t.Fatal(err)
	}
	want := "Title:\nSystem Analyst\n\nCompany:\n(none)\n\nDuties:\n(none)\n\nRequirements:\n- Know Go\n- React (For Frontend)\n\nTemplate:\nDear team\nada@example.com\n"
	if got != want {
		t.Fatalf("prompt =\n%s\nwant =\n%s", got, want)
	}
	order := []string{"Title:\n", "Company:\n", "Duties:\n", "Requirements:\n", "Template:\n"}
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
	got, err = DraftPrompt(withDuties, "ada@example.com")
	if err != nil {
		t.Fatal(err)
	}
	want = "Title:\nSystem Analyst\n\nCompany:\n(none)\n\nDuties:\n- Build services\n\nRequirements:\n- Know Go\n\nTemplate:\nada@example.com\n"
	if got != want {
		t.Fatalf("duties prompt =\n%s\nwant =\n%s", got, want)
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
	got, err := DraftPrompt(doc, "ada@example.com")
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
	for _, keep := range []string{"Title:\nSystem Analyst", "Company:\nCOMPANY-TOKEN", "Duties:\n- Build services", "Requirements:\n- Know Go", "Template:\nada@example.com"} {
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
	got, err := DraftPrompt(doc, "ada@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "person@example.com") {
		t.Fatalf("prompt contains job email\n%s", got)
	}
	for _, keep := range []string{"Analyst", "Mail daily", "Know Go", "ada@example.com", "Company:\nAcme Ltd"} {
		if !strings.Contains(got, keep) {
			t.Fatalf("prompt missing %q\n%s", keep, got)
		}
	}

	emailOnly := doc
	emailOnly.Header.Company = "person@example.com"
	got, err = DraftPrompt(emailOnly, "ada@example.com")
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
	if !strings.Contains(got, "DRAFT-TEMPLATE-SENTINEL") {
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
		got, err := Draft(doc, "ada@example.com")
		if got != "" || !errors.Is(err, ErrRequirementsEmpty) {
			t.Fatalf("doc %#v got %q err %v", doc.Requirements, got, err)
		}
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
	text, err := DraftPrompt(doc, "ada@example.com")
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

	got, err := Draft(doc, "ada@example.com")
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
	const wantInstructions = `The template is a pattern for structure and voice only, not text to paste. Write the whole letter for this job. Keep the template's language, salutation shape, closing, and paragraph shape. Keep an applicant name or contact line only when the template gives that line as real text. A square-bracket placeholder is a blank, not text to copy. Do not leave any square-bracket placeholder in the letter. Fill the letter from Title, Company, Duties, and Requirements. Title is the role being applied for, not the applicant's current job. Company is this job's employer. When Company is not (none), use that name as the employer. Do not take the employer from a parenthetical in Title, and do not use an employer from the template's previous role. When Company is (none), do not invent an employer and do not write (none) into the letter. Rewrite the body for this job's Duties and Requirements. Do not invent a recipient, job board, compliment, current job title, degree, date, or years of experience that appears in neither the template nor the JSON. Do not turn a required number of years into the applicant's years. When a blank has no fact in the template or the JSON, omit that blank. The letter field is the whole letter and nothing else.`
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
}

func TestDraftAvailableFailureDoesNotRespond(t *testing.T) {
	var calls [][]string
	SetFMExecForTest(func(argv []string) ([]byte, []byte, int) {
		calls = append(calls, append([]string(nil), argv...))
		return []byte("ignored"), []byte("not licensed\n"), 1
	})
	t.Cleanup(restoreFMStub)

	got, err := Draft(Document{Requirements: []string{"Know Go"}}, "ada@example.com")
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
	}, "ada@example.com")
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
	got, err := Draft(doc, "ada@example.com")
	if got != "" || !errors.Is(err, ErrFMArgLimit) || err.Error() != "docs-cli: fm text exceeds the argument limit" {
		t.Fatalf("env got %q err %v", got, err)
	}

	draftEnviron = func() []string { return nil }
	got, err = Draft(doc, strings.Repeat("Q", 917488))
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
