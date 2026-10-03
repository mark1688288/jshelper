package jobad

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestAnalyzeRunsAvailableThenRespond(t *testing.T) {
	doc := sampleAnalysisDoc()
	text, err := PromptText(doc)
	if err != nil {
		t.Fatal(err)
	}
	model := []byte(`{"must_have":["Know Go"],"advantage":[],"min_years":null,"degree":null,"languages":[],"tech":[{"name":"R&D","level":"must","applies_to":"For Frontend"}],"soft_skills":[]}`)
	var calls [][]string
	SetFMExecForTest(func(argv []string) ([]byte, []byte, int) {
		calls = append(calls, append([]string(nil), argv...))
		if len(calls) == 1 {
			return []byte("not analysis"), []byte("available noise"), 0
		}
		return append(append([]byte(nil), model...), '\n'), []byte("respond noise"), 0
	})
	t.Cleanup(restoreFMStub)

	got, err := Analyze(doc)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 {
		t.Fatalf("calls = %d", len(calls))
	}
	if !slices.Equal(calls[0], []string{fmPath, "available"}) {
		t.Fatalf("available = %#v", calls[0])
	}
	want := []string{
		fmPath, "respond", "--no-stream", "--use-case", "general",
		"--instructions", AnalysisInstructions,
		"--schema", AnalysisSchema,
		"--text", text,
	}
	if !slices.Equal(calls[1], want) {
		t.Fatalf("respond = %#v", calls[1])
	}
	for _, arg := range calls[1] {
		for _, banned := range []string{"TRAILER-SENTINEL", doc.Source, "person@example.com", "LOCATION-TOKEN"} {
			if strings.Contains(arg, banned) {
				t.Fatalf("argv contains %q\n%s", banned, arg)
			}
		}
	}
	if len(got.MustHave) != 1 || got.MustHave[0] != "Know Go" {
		t.Fatalf("must_have = %#v", got.MustHave)
	}
	if len(got.Tech) != 1 || got.Tech[0].Name != "R&D" || got.Tech[0].AppliesTo == nil || *got.Tech[0].AppliesTo != "For Frontend" {
		t.Fatalf("tech = %#v", got.Tech)
	}
}

func TestAnalyzeSkipsRespondWhenAvailableFails(t *testing.T) {
	var calls [][]string
	SetFMExecForTest(func(argv []string) ([]byte, []byte, int) {
		calls = append(calls, append([]string(nil), argv...))
		return []byte("ignored"), []byte("not licensed\n"), 1
	})
	t.Cleanup(restoreFMStub)

	got, err := Analyze(sampleAnalysisDoc())
	if err == nil || err.Error() != "docs-cli: fm: not licensed" {
		t.Fatalf("got %#v err %v", got, err)
	}
	if len(calls) != 1 || !slices.Equal(calls[0], []string{fmPath, "available"}) {
		t.Fatalf("calls = %#v", calls)
	}
}

func TestAnalyzeRespondFailure(t *testing.T) {
	var calls int
	SetFMExecForTest(func(argv []string) ([]byte, []byte, int) {
		calls++
		if calls == 1 {
			return nil, nil, 0
		}
		return []byte(`{"must_have":[]}`), []byte("line1\nguardrail rejected\n"), 3
	})
	t.Cleanup(restoreFMStub)

	_, err := Analyze(sampleAnalysisDoc())
	if err == nil || err.Error() != "docs-cli: fm: line1\nguardrail rejected" {
		t.Fatalf("err %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d", calls)
	}
}

func TestAnalyzeEmptyRequirementsDoesNotExec(t *testing.T) {
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
		got, err := Analyze(doc)
		if got.MustHave != nil || !errors.Is(err, ErrRequirementsEmpty) {
			t.Fatalf("doc %#v got %#v err %v", doc.Requirements, got, err)
		}
	}
	if calls != 0 {
		t.Fatalf("calls = %d", calls)
	}
}

func TestAnalyzeRejectsFMProse(t *testing.T) {
	calls := 0
	SetFMExecForTest(func([]string) ([]byte, []byte, int) {
		calls++
		if calls == 1 {
			return nil, nil, 0
		}
		return []byte("The candidate needs Go."), nil, 0
	})
	t.Cleanup(restoreFMStub)

	got, err := Analyze(sampleAnalysisDoc())
	if !errors.Is(err, ErrAnalysisSchema) || got.MustHave != nil {
		t.Fatalf("got %#v err %v", got, err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d", calls)
	}
}

func TestAnalyzeDefaultStubDoesNotRunFM(t *testing.T) {
	_, err := Analyze(sampleAnalysisDoc())
	if err == nil || err.Error() != "docs-cli: fm: fm exec not stubbed" {
		t.Fatal(err)
	}
}

func TestSetFMExecForTestNilRestoresRunner(t *testing.T) {
	SetFMExecForTest(func([]string) ([]byte, []byte, int) {
		t.Fatal("replacement ran")
		return nil, nil, 1
	})
	defer restoreFMStub()
	SetFMExecForTest(nil)
	if reflect.ValueOf(fmExec).Pointer() != reflect.ValueOf(runFMProcess).Pointer() {
		t.Fatal("nil did not restore /usr/bin/fm")
	}
}

func sampleAnalysisDoc() Document {
	return Document{
		Source: "source-token-9f3a.html",
		Header: Header{
			TitleBlock:     "System Analyst",
			Location:       "LOCATION-TOKEN",
			WorkType:       "WORKTYPE-TOKEN",
			Classification: "CLASS-TOKEN",
		},
		Benefits:     []string{"Medical person@example.com"},
		Duties:       []string{},
		Requirements: []string{"Know Go", "React (For Frontend)"},
		Trailer:      []string{"TRAILER-SENTINEL"},
	}
}

func restoreFMStub() {
	SetFMExecForTest(func([]string) ([]byte, []byte, int) {
		return nil, []byte("fm exec not stubbed"), 1
	})
}
