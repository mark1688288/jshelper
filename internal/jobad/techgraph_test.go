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
		want := flowchartFile(
			`    n1["Java / Spring Boot"]:::must`,
			`    n2["Backend"]:::target`,
			`    n1 -->|must| n2`,
			`    n3["RESTful APIs"]:::must`,
			`    n4["Full Stack"]:::target`,
			`    n3 -->|must| n4`,
			`    n5["SQL Server"]:::must`,
			`    n6["Database"]:::target`,
			`    n5 -->|must| n6`,
			`    n7["Azure Cloud services"]:::must`,
			`    n8["Infrastructure"]:::target`,
			`    n7 -->|must| n8`,
			`    n9["Salesforce"]:::advantage`,
			`    n10["Integration"]:::target`,
			`    n9 -.->|advantage| n10`,
			`    n11["AI-assisted software development tools"]:::advantage`,
			`    n12["Development"]:::target`,
			`    n11 -.->|advantage| n12`,
			`    linkStyle 0 stroke:#3A3A3A,stroke-width:2px`,
			`    linkStyle 1 stroke:#3A3A3A,stroke-width:2px`,
			`    linkStyle 2 stroke:#3A3A3A,stroke-width:2px`,
			`    linkStyle 3 stroke:#3A3A3A,stroke-width:2px`,
			`    linkStyle 4 stroke:#3A3A3A,stroke-width:2px`,
			`    linkStyle 5 stroke:#3A3A3A,stroke-width:2px`,
		)
		if string(got) != want {
			t.Fatalf("diagram\n%s", got)
		}
		for _, banned := range []string{"NOT-IN-DIAGRAM", "ALSO-NOT", "English", "Communication", "<svg", "#111111", "#9a3412", "<script"} {
			if strings.Contains(string(got), banned) {
				t.Fatalf("diagram contains %q", banned)
			}
		}
	})

	t.Run("shared target and standalone nodes", func(t *testing.T) {
		got, err := TechDiagram(Analysis{Tech: []Tech{
			{Name: "Java", Level: levelMust, AppliesTo: techPtr("Backend")},
			{Name: "Kotlin", Level: levelAdvantage, AppliesTo: techPtr("Backend")},
			{Name: "Go", Level: levelMust, AppliesTo: nil},
			{Name: "Rust", Level: levelAdvantage, AppliesTo: techPtr("  ")},
		}})
		if err != nil {
			t.Fatal(err)
		}
		want := flowchartFile(
			`    n1["Java"]:::must`,
			`    n2["Backend"]:::target`,
			`    n1 -->|must| n2`,
			`    n3["Kotlin"]:::advantage`,
			`    n3 -.->|advantage| n2`,
			`    n4["Go<br/>must"]:::must`,
			`    n5["Rust<br/>advantage"]:::advantage`,
			`    linkStyle 0 stroke:#3A3A3A,stroke-width:2px`,
			`    linkStyle 1 stroke:#3A3A3A,stroke-width:2px`,
		)
		if string(got) != want {
			t.Fatalf("diagram\n%s", got)
		}
		if strings.Count(string(got), `["Backend"]`) != 1 {
			t.Fatalf("targets\n%s", got)
		}
	})

	t.Run("standalone null applies_to", func(t *testing.T) {
		got, err := TechDiagram(Analysis{Tech: []Tech{{Name: "Go", Level: levelMust, AppliesTo: nil}}})
		if err != nil {
			t.Fatal(err)
		}
		md := string(got)
		if !strings.Contains(md, `    n1["Go<br/>must"]:::must`) || strings.Contains(md, "-->") || strings.Contains(md, "linkStyle") || strings.Contains(md, ":::target") {
			t.Fatalf("diagram\n%s", md)
		}
	})

	t.Run("blank applies_to is standalone", func(t *testing.T) {
		got, err := TechDiagram(Analysis{Tech: []Tech{{Name: "Go", Level: levelAdvantage, AppliesTo: techPtr("  ")}}})
		if err != nil {
			t.Fatal(err)
		}
		md := string(got)
		if !strings.Contains(md, `    n1["Go<br/>advantage"]:::advantage`) || strings.Contains(md, "-.->") || strings.Contains(md, "linkStyle") {
			t.Fatalf("diagram\n%s", md)
		}
	})

	t.Run("empty tech", func(t *testing.T) {
		for _, tech := range [][]Tech{nil, {}} {
			got, err := TechDiagram(Analysis{Tech: tech, MustHave: []string{"NOT-IN-DIAGRAM"}})
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != "(none)\n" || bytes.Contains(got, []byte("flowchart")) || bytes.Contains(got, []byte("NOT-IN-DIAGRAM")) {
				t.Fatalf("diagram %q", got)
			}
		}
	})

	t.Run("keeps ampersand and rewrites label characters", func(t *testing.T) {
		got, err := TechDiagram(Analysis{Tech: []Tech{{Name: `A & B "q" [x]`, Level: levelMust, AppliesTo: techPtr("X<Y>Z]")}}})
		if err != nil {
			t.Fatal(err)
		}
		md := string(got)
		if !strings.Contains(md, `n1["A & B 'q' (x)"]:::must`) || strings.Contains(md, "A &amp; B") || strings.Contains(md, `&quot;`) {
			t.Fatalf("diagram\n%s", md)
		}
		if !strings.Contains(md, `n2["X(Y)Z)"]:::target`) || strings.Contains(md, "X<Y") || strings.Contains(md, "X&lt;Y") {
			t.Fatalf("diagram\n%s", md)
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
		md := string(got)
		if strings.Count(md, ":::must") != 1 || strings.Count(md, ":::advantage") != 1 || strings.Count(md, `["Backend"]`) != 1 {
			t.Fatalf("diagram\n%s", md)
		}
		if !strings.Contains(md, `n1 -->|must| n2`) || !strings.Contains(md, `n3 -.->|advantage| n2`) {
			t.Fatalf("diagram\n%s", md)
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
		md := string(got)
		if strings.Count(md, `["Backend"]`) != 1 || strings.Count(md, `["backend"]`) != 1 {
			t.Fatalf("diagram\n%s", md)
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
		md := string(got)
		java := strings.Index(md, `n1["Java / Spring Boot"]:::must`)
		sql := strings.Index(md, `n3["SQL Server"]:::must`)
		if java < 0 || sql < java || strings.Count(md, "Java / Spring Boot") != 1 || strings.Contains(md[sql:], "Spring Boot") {
			t.Fatalf("diagram\n%s", md)
		}
		if !strings.Contains(md, `n1 -->|must| n2`) || !strings.Contains(md, `["Backend"]`) {
			t.Fatalf("diagram\n%s", md)
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
		md := string(got)
		link := strings.Index(md, `n1 -->|must| n2`)
		alone := strings.Index(md, `n3["Go<br/>must"]:::must`)
		if link < 0 || alone < link || strings.Contains(md, "Go -->|") || strings.Contains(md, "linkStyle 1") {
			t.Fatalf("diagram\n%s", md)
		}
		if !strings.Contains(md, "Java / Spring Boot") {
			t.Fatalf("diagram\n%s", md)
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
		md := string(got)
		if strings.Contains(md, "Backend") || strings.Contains(md, "expert") || !strings.Contains(md, `n1["Go<br/>must"]:::must`) {
			t.Fatalf("diagram\n%s", md)
		}
	})

	t.Run("all blank names are none", func(t *testing.T) {
		got, err := TechDiagram(Analysis{Tech: []Tech{{Name: " \t ", Level: levelMust}}})
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "(none)\n" || bytes.Contains(got, []byte("flowchart")) {
			t.Fatalf("diagram %q", got)
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
		md := string(got)
		if !strings.Contains(md, `n1["Java Spring"]:::must`) || !strings.Contains(md, `n2["Backend"]:::target`) || !strings.Contains(md, `n3["AB"]:::advantage`) || !strings.Contains(md, `n4["CD"]:::target`) {
			t.Fatalf("diagram\n%s", md)
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

func flowchartFile(lines ...string) string {
	var b strings.Builder
	b.WriteString("```mermaid\n")
	b.WriteString(flowchartInit)
	b.WriteString("\nflowchart LR\n")
	b.WriteString("    classDef must fill:#3A3A3A,stroke:#3A3A3A,color:#F2F2F2,stroke-width:1px\n")
	b.WriteString("    classDef advantage fill:#F2F2F2,stroke:#3A3A3A,color:#262626,stroke-width:2px\n")
	b.WriteString("    classDef target fill:#D5D5D5,stroke:#6E6E6E,color:#262626,stroke-width:1px\n")
	for _, line := range lines {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	b.WriteString("```\n")
	return b.String()
}
