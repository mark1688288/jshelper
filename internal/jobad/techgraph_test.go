package jobad

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestTechDiagram(t *testing.T) {
	t.Run("groups the 95132688 shape", func(t *testing.T) {
		analysis := Analysis{
			MustHave:   []string{"NOT-IN-DIAGRAM"},
			Advantage:  []string{"ALSO-NOT"},
			Languages:  []string{"English"},
			SoftSkills: []string{"Communication"},
			Tech: []Tech{
				{Name: "Java", Level: levelMust, AppliesTo: techPtr("Backend")},
				{Name: "Spring Boot", Level: levelMust, AppliesTo: techPtr("Backend")},
				{Name: "RESTful APIs", Level: levelMust, AppliesTo: techPtr("Full Stack")},
				{Name: "SQL Server", Level: levelMust, AppliesTo: techPtr("Database")},
				{Name: "Azure Cloud services", Level: levelMust, AppliesTo: techPtr("Infrastructure")},
				{Name: "Salesforce", Level: levelAdvantage, AppliesTo: techPtr("Integration")},
				{Name: "AI-assisted software development tools", Level: levelAdvantage, AppliesTo: techPtr("Development")},
			},
		}
		got, err := TechDiagram(analysis)
		if err != nil {
			t.Fatal(err)
		}
		again, err := TechDiagram(analysis)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, again) {
			t.Fatal("diagram bytes changed")
		}
		svg := string(got)
		if !strings.HasPrefix(svg, "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n") || !strings.HasSuffix(svg, "</svg>\n") {
			t.Fatalf("svg envelope %q", svg[:80])
		}
		if !strings.Contains(svg, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 `) || !strings.Contains(svg, "<title>tech</title>") {
			t.Fatalf("svg header %s", svg)
		}
		for _, want := range []string{
			"Java / Spring Boot",
			">Backend</text>",
			`class="must"`,
			"Salesforce",
			">Integration</text>",
			`class="advantage"`,
			"RESTful APIs",
			"Full Stack",
			"SQL Server",
			"Database",
			"Azure Cloud services",
			"Infrastructure",
			"AI-assisted software development tools",
			"Development",
			`stroke="#111111"`,
			`stroke="#9a3412"`,
			`stroke-dasharray="6 4"`,
			`marker-end="url(#arrow)"`,
		} {
			if !strings.Contains(svg, want) {
				t.Fatalf("missing %q\n%s", want, svg)
			}
		}
		for _, banned := range []string{"NOT-IN-DIAGRAM", "ALSO-NOT", "English", "Communication", "<script", "foreignObject"} {
			if strings.Contains(svg, banned) {
				t.Fatalf("svg contains %q", banned)
			}
		}
		if strings.Count(svg, `class="link"`) != 6 {
			t.Fatalf("links %d\n%s", strings.Count(svg, `class="link"`), svg)
		}
		row := techGroup(t, svg, "row-1")
		if !strings.Contains(row, "Java / Spring Boot") || !strings.Contains(row, ">Backend</text>") || !strings.Contains(row, `class="must"`) {
			t.Fatalf("row-1 %s", row)
		}
	})

	t.Run("standalone null applies_to", func(t *testing.T) {
		got, err := TechDiagram(Analysis{Tech: []Tech{{Name: "Go", Level: levelMust, AppliesTo: nil}}})
		if err != nil {
			t.Fatal(err)
		}
		svg := string(got)
		row := techGroup(t, svg, "row-1")
		if !strings.Contains(row, `class="standalone"`) || !strings.Contains(row, ">Go</text>") || !strings.Contains(row, ">must</text>") {
			t.Fatalf("row %s", row)
		}
		if strings.Contains(row, "<line") || strings.Contains(svg, "<line") || strings.Contains(svg, `class="link"`) {
			t.Fatalf("standalone has a line\n%s", svg)
		}
	})

	t.Run("blank applies_to is standalone", func(t *testing.T) {
		got, err := TechDiagram(Analysis{Tech: []Tech{{Name: "Go", Level: levelAdvantage, AppliesTo: techPtr("  ")}}})
		if err != nil {
			t.Fatal(err)
		}
		svg := string(got)
		row := techGroup(t, svg, "row-1")
		if !strings.Contains(row, `class="standalone"`) || !strings.Contains(row, ">advantage</text>") || strings.Contains(row, "<line") {
			t.Fatalf("row %s", row)
		}
	})

	t.Run("empty tech", func(t *testing.T) {
		for _, tech := range [][]Tech{nil, {}} {
			got, err := TechDiagram(Analysis{Tech: tech, MustHave: []string{"NOT-IN-DIAGRAM"}})
			if err != nil {
				t.Fatal(err)
			}
			svg := string(got)
			if !strings.Contains(svg, `<text class="empty">(none)</text>`) {
				t.Fatalf("svg %s", svg)
			}
			if strings.Contains(svg, `class="link"`) || strings.Contains(svg, `class="standalone"`) || strings.Contains(svg, "<line") || strings.Contains(svg, "NOT-IN-DIAGRAM") {
				t.Fatalf("svg %s", svg)
			}
		}
	})

	t.Run("escapes xml", func(t *testing.T) {
		got, err := TechDiagram(Analysis{Tech: []Tech{{Name: "A & B", Level: levelMust, AppliesTo: techPtr("X<Y")}}})
		if err != nil {
			t.Fatal(err)
		}
		svg := string(got)
		if !strings.Contains(svg, "A &amp; B") || strings.Contains(svg, "A & B") {
			t.Fatalf("svg %s", svg)
		}
		if !strings.Contains(svg, "X&lt;Y") || strings.Contains(svg, "X<Y") {
			t.Fatalf("svg %s", svg)
		}
	})

	t.Run("shared target different levels", func(t *testing.T) {
		got, err := TechDiagram(Analysis{Tech: []Tech{
			{Name: "Java", Level: levelMust, AppliesTo: techPtr("Backend")},
			{Name: "Go", Level: levelAdvantage, AppliesTo: techPtr("Backend")},
		}})
		if err != nil {
			t.Fatal(err)
		}
		svg := string(got)
		if strings.Count(svg, `class="link"`) != 2 {
			t.Fatalf("links\n%s", svg)
		}
		if strings.Count(svg, ">Backend</text>") != 1 {
			t.Fatalf("targets\n%s", svg)
		}
		if strings.Count(svg, `class="must"`) != 1 || strings.Count(svg, `class="advantage"`) != 1 {
			t.Fatalf("levels\n%s", svg)
		}
	})

	t.Run("case sensitive targets", func(t *testing.T) {
		got, err := TechDiagram(Analysis{Tech: []Tech{
			{Name: "Java", Level: levelMust, AppliesTo: techPtr("Backend")},
			{Name: "Go", Level: levelAdvantage, AppliesTo: techPtr("backend")},
		}})
		if err != nil {
			t.Fatal(err)
		}
		svg := string(got)
		if strings.Count(svg, ">Backend</text>") != 1 || strings.Count(svg, ">backend</text>") != 1 {
			t.Fatalf("svg %s", svg)
		}
	})

	t.Run("merges a later item into the first group", func(t *testing.T) {
		got, err := TechDiagram(Analysis{Tech: []Tech{
			{Name: "Java", Level: levelMust, AppliesTo: techPtr("Backend")},
			{Name: "SQL Server", Level: levelMust, AppliesTo: techPtr("Database")},
			{Name: "Spring Boot", Level: levelMust, AppliesTo: techPtr("Backend")},
		}})
		if err != nil {
			t.Fatal(err)
		}
		svg := string(got)
		row1 := techGroup(t, svg, "row-1")
		row2 := techGroup(t, svg, "row-2")
		if !strings.Contains(row1, "Java / Spring Boot") || !strings.Contains(row1, ">Backend</text>") {
			t.Fatalf("row-1 %s", row1)
		}
		if !strings.Contains(row2, "SQL Server") || strings.Contains(row2, "Spring Boot") {
			t.Fatalf("row-2 %s", row2)
		}
		if strings.Count(svg, "Java / Spring Boot") != 1 {
			t.Fatalf("svg %s", svg)
		}
	})

	t.Run("keeps a standalone in place", func(t *testing.T) {
		got, err := TechDiagram(Analysis{Tech: []Tech{
			{Name: "Java", Level: levelMust, AppliesTo: techPtr("Backend")},
			{Name: "Go", Level: levelMust, AppliesTo: nil},
			{Name: "Spring Boot", Level: levelMust, AppliesTo: techPtr("Backend")},
		}})
		if err != nil {
			t.Fatal(err)
		}
		svg := string(got)
		row1 := techGroup(t, svg, "row-1")
		row2 := techGroup(t, svg, "row-2")
		if !strings.Contains(row1, `class="link"`) || !strings.Contains(row1, "Java / Spring Boot") {
			t.Fatalf("row-1 %s", row1)
		}
		if !strings.Contains(row2, `class="standalone"`) || !strings.Contains(row2, ">Go</text>") || strings.Contains(row2, "<line") {
			t.Fatalf("row-2 %s", row2)
		}
	})

	t.Run("skips a blank name", func(t *testing.T) {
		got, err := TechDiagram(Analysis{Tech: []Tech{
			{Name: "  ", Level: levelMust, AppliesTo: techPtr("Backend")},
			{Name: "\n", Level: "expert", AppliesTo: techPtr("Backend")},
			{Name: "Go", Level: levelMust, AppliesTo: nil},
		}})
		if err != nil {
			t.Fatal(err)
		}
		svg := string(got)
		if strings.Contains(svg, "Backend") || strings.Contains(svg, "expert") || !strings.Contains(svg, ">Go</text>") {
			t.Fatalf("svg %s", svg)
		}
	})

	t.Run("all blank names are none", func(t *testing.T) {
		got, err := TechDiagram(Analysis{Tech: []Tech{{Name: " \t ", Level: levelMust}}})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(got), `<text class="empty">(none)</text>`) {
			t.Fatalf("svg %s", got)
		}
	})

	t.Run("collapses whitespace and drops control runes", func(t *testing.T) {
		got, err := TechDiagram(Analysis{Tech: []Tech{
			{Name: "Java \n  Spring", Level: levelMust, AppliesTo: techPtr("  Backend  ")},
			{Name: "A\x01B", Level: levelAdvantage, AppliesTo: techPtr("C\x01D")},
		}})
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(got, []byte{0x01}) {
			t.Fatalf("control rune kept %s", got)
		}
		svg := string(got)
		if !strings.Contains(svg, "Java Spring") || !strings.Contains(svg, ">Backend</text>") || !strings.Contains(svg, ">AB</text>") || !strings.Contains(svg, ">CD</text>") {
			t.Fatalf("svg %s", svg)
		}
	})

	t.Run("rejects a bad level", func(t *testing.T) {
		got, err := TechDiagram(Analysis{Tech: []Tech{{Name: "Go", Level: "expert", AppliesTo: techPtr("Backend")}}})
		if !errors.Is(err, ErrTechDiagramInvalid) || got != nil || err.Error() != "docs-cli: tech diagram is invalid" {
			t.Fatalf("got %q err %v", got, err)
		}
	})
}

func techPtr(s string) *string {
	return &s
}

func techGroup(t *testing.T, svg, id string) string {
	t.Helper()
	start := strings.Index(svg, `<g id="`+id+`"`)
	if start < 0 {
		t.Fatalf("missing %s\n%s", id, svg)
	}
	rest := svg[start:]
	end := strings.Index(rest, "</g>")
	if end < 0 {
		t.Fatalf("unclosed %s\n%s", id, svg)
	}
	return rest[:end]
}
