package jobad

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestTechDistribution(t *testing.T) {
	t.Run("places the 95148940 shape", func(t *testing.T) {
		analysis := Analysis{
			MustHave:   []string{"NOT-IN-CHART"},
			Advantage:  []string{"ALSO-NOT"},
			Languages:  []string{"English"},
			SoftSkills: []string{"Communication"},
			Tech: []Tech{
				{Name: "Node.js", Level: levelMust, AppliesTo: techPtr("Full Stack")},
				{Name: "React", Level: levelMust, AppliesTo: techPtr("Full Stack")},
				{Name: "Typescript", Level: levelMust, AppliesTo: techPtr("Full Stack")},
				{Name: "RESTful API", Level: levelMust, AppliesTo: techPtr("Backend")},
				{Name: "RDBMS", Level: levelMust, AppliesTo: techPtr("Database")},
				{Name: "NoSQL", Level: levelMust, AppliesTo: techPtr("Database")},
				{Name: "Linux", Level: levelAdvantage, AppliesTo: techPtr("Infrastructure")},
				{Name: "CI/CD", Level: levelAdvantage, AppliesTo: techPtr("DevOps")},
				{Name: "Jenkins", Level: levelAdvantage, AppliesTo: techPtr("DevOps")},
				{Name: "JMeter", Level: levelAdvantage, AppliesTo: techPtr("Testing")},
				{Name: "AWS Kiro", Level: levelAdvantage, AppliesTo: techPtr("Cloud")},
			},
		}
		got, err := TechDistribution("95148940", analysis)
		if err != nil {
			t.Fatal(err)
		}
		again, err := TechDistribution("95148940", analysis)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, again) {
			t.Fatal("distribution bytes changed")
		}
		md := string(got)
		if !strings.HasPrefix(md, "```mermaid\n"+distInit+"\nquadrantChart\n") || !strings.HasSuffix(md, "\n") {
			t.Fatalf("envelope %q", md[:80])
		}
		if strings.Contains(md, "# ") || strings.Contains(md, "<svg") || strings.Contains(md, "quadrantChart\nquadrantChart") {
			t.Fatalf("shape %s", md)
		}
		for _, banned := range []string{"NOT-IN-CHART", "ALSO-NOT", "English", "Communication", "&amp;", "bar"} {
			if strings.Contains(md, banned) {
				t.Fatalf("markdown contains %q", banned)
			}
		}
		if !strings.Contains(md, "    title 95148940\n") {
			t.Fatalf("title %s", md)
		}
		for _, want := range []string{
			`x-axis "must 0" --> "must 3"`,
			`y-axis "advantage 0" --> "advantage 3"`,
			`quadrant-1 "must 2–3 · advantage 2–3"`,
			`quadrant-2 "must 0–1 · advantage 2–3"`,
			`quadrant-3 "must 0–1 · advantage 0–1"`,
			`quadrant-4 "must 2–3 · advantage 0–1"`,
		} {
			if !strings.Contains(md, want) {
				t.Fatalf("missing %s\n%s", want, md)
			}
		}
		points := []string{
			`"Full Stack": [0.875, 0.125] color: #3A3A3A, stroke-color: #3A3A3A, stroke-width: 1px`,
			`"Backend": [0.375, 0.125] color: #3A3A3A, stroke-color: #3A3A3A, stroke-width: 1px`,
			`"Database": [0.625, 0.125] color: #3A3A3A, stroke-color: #3A3A3A, stroke-width: 1px`,
			`"Infrastructure": [0.125, 0.450] color: #F2F2F2, stroke-color: #3A3A3A, stroke-width: 2px`,
			`"DevOps": [0.125, 0.625] color: #F2F2F2, stroke-color: #3A3A3A, stroke-width: 2px`,
			`"Testing": [0.125, 0.375] color: #F2F2F2, stroke-color: #3A3A3A, stroke-width: 2px`,
			`"Cloud": [0.125, 0.300] color: #F2F2F2, stroke-color: #3A3A3A, stroke-width: 2px`,
		}
		prev := -1
		for _, want := range points {
			at := strings.Index(md, want)
			if at < 0 || at < prev {
				t.Fatalf("point order %s\n%s", want, md)
			}
			prev = at
		}
		if !strings.Contains(md, "```\n\n| applies_to | must | advantage | technical |\n| --- | --- | --- | --- |\n| Full Stack | 3 | 0 | Node.js / React / Typescript |") {
			t.Fatalf("table %s", md)
		}
	})

	t.Run("fills a point that has both levels", func(t *testing.T) {
		got, err := TechDistribution("core", Analysis{Tech: []Tech{
			{Name: "Go", Level: levelMust, AppliesTo: techPtr("Core")},
			{Name: "Rust", Level: levelAdvantage, AppliesTo: techPtr("Core")},
		}})
		if err != nil {
			t.Fatal(err)
		}
		md := string(got)
		for _, want := range []string{
			`"Core": [0.750, 0.750] color: #737373, stroke-color: #3A3A3A, stroke-width: 1px`,
			`quadrant-1 "must 1 · advantage 1"`,
			`quadrant-2 "must 0 · advantage 1"`,
			`quadrant-3 "must 0 · advantage 0"`,
			`quadrant-4 "must 1 · advantage 0"`,
			`x-axis "must 0" --> "must 1"`,
			"| Core | 1 | 1 | Go / Rust |",
		} {
			if !strings.Contains(md, want) {
				t.Fatalf("missing %s\n%s", want, md)
			}
		}
	})

	t.Run("splits five points into two columns", func(t *testing.T) {
		names := []string{"One", "Two", "Three", "Four", "Five"}
		tech := make([]Tech, len(names))
		for i, name := range names {
			tech[i] = Tech{Name: name, Level: levelAdvantage, AppliesTo: techPtr(name)}
		}
		got, err := TechDistribution("five", Analysis{Tech: tech})
		if err != nil {
			t.Fatal(err)
		}
		md := string(got)
		first := `"One": [0.138, 0.919] color: #F2F2F2, stroke-color: #3A3A3A, stroke-width: 2px`
		fifth := `"Five": [0.363, 0.919] color: #F2F2F2, stroke-color: #3A3A3A, stroke-width: 2px`
		if strings.Index(md, first) < 0 || strings.Index(md, first) > strings.Index(md, fifth) {
			t.Fatalf("columns %s", md)
		}
		if strings.Count(md, "color: #F2F2F2") != 5 {
			t.Fatalf("styles %s", md)
		}
	})

	t.Run("empty tech is none", func(t *testing.T) {
		got, err := TechDistribution("empty", Analysis{})
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "(none)\n" || bytes.Contains(got, []byte("quadrantChart")) {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("all blank names are none", func(t *testing.T) {
		got, err := TechDistribution("blank", Analysis{Tech: []Tech{{Name: " \t ", Level: levelMust}}})
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "(none)\n" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("keeps ampersand", func(t *testing.T) {
		got, err := TechDistribution("amp", Analysis{Tech: []Tech{
			{Name: "A & B", Level: levelMust, AppliesTo: techPtr("X<Y")},
		}})
		if err != nil {
			t.Fatal(err)
		}
		md := string(got)
		if !strings.Contains(md, `"X<Y": [`) || !strings.Contains(md, "| X<Y | 1 | 0 | A & B |") || strings.Contains(md, "&amp;") || strings.Contains(md, "&lt;") {
			t.Fatalf("markdown %s", md)
		}
	})

	t.Run("replaces quotes in the label", func(t *testing.T) {
		got, err := TechDistribution(`Job "A"`, Analysis{Tech: []Tech{
			{Name: `Say "Hi"`, Level: levelMust, AppliesTo: nil},
		}})
		if err != nil {
			t.Fatal(err)
		}
		md := string(got)
		if !strings.Contains(md, "    title Job A\n") || !strings.Contains(md, `"Say 'Hi'": [`) || !strings.Contains(md, `| Say 'Hi' | 1 | 0 | Say "Hi" |`) {
			t.Fatalf("markdown %s", md)
		}
		if strings.Contains(md, `Job "A"`) {
			t.Fatalf("title quotes left %s", md)
		}
	})

	t.Run("keeps null applies_to apart", func(t *testing.T) {
		got, err := TechDistribution("solo", Analysis{Tech: []Tech{
			{Name: "Go", Level: levelMust, AppliesTo: nil},
			{Name: "Rust", Level: levelAdvantage, AppliesTo: nil},
		}})
		if err != nil {
			t.Fatal(err)
		}
		md := string(got)
		if strings.Contains(md, "Go / Rust") || !strings.Contains(md, `"Go": [0.750, 0.250]`) || !strings.Contains(md, `"Rust": [0.250, 0.750]`) {
			t.Fatalf("markdown %s", md)
		}
	})

	t.Run("blank applies_to is its own point", func(t *testing.T) {
		got, err := TechDistribution("blank-to", Analysis{Tech: []Tech{
			{Name: "Go", Level: levelMust, AppliesTo: techPtr("  ")},
		}})
		if err != nil {
			t.Fatal(err)
		}
		md := string(got)
		if !strings.Contains(md, `"Go": [`) || strings.Contains(md, " / ") {
			t.Fatalf("markdown %s", md)
		}
	})

	t.Run("keeps case apart", func(t *testing.T) {
		got, err := TechDistribution("case", Analysis{Tech: []Tech{
			{Name: "Java", Level: levelMust, AppliesTo: techPtr("Backend")},
			{Name: "Go", Level: levelMust, AppliesTo: techPtr("backend")},
		}})
		if err != nil {
			t.Fatal(err)
		}
		md := string(got)
		if !strings.Contains(md, `"Backend": [`) || !strings.Contains(md, `"backend": [`) {
			t.Fatalf("markdown %s", md)
		}
	})

	t.Run("bumps an odd band", func(t *testing.T) {
		got, err := TechDistribution("odd", Analysis{Tech: []Tech{
			{Name: "A", Level: levelMust, AppliesTo: techPtr("Lang")},
			{Name: "B", Level: levelMust, AppliesTo: techPtr("Lang")},
		}})
		if err != nil {
			t.Fatal(err)
		}
		md := string(got)
		if !strings.Contains(md, `x-axis "must 0" --> "must 3"`) || !strings.Contains(md, `"Lang": [0.625, 0.125]`) || !strings.Contains(md, `quadrant-1 "must 2–3 · advantage 2–3"`) {
			t.Fatalf("markdown %s", md)
		}
	})

	t.Run("skips a blank name", func(t *testing.T) {
		got, err := TechDistribution("skip", Analysis{Tech: []Tech{
			{Name: "  ", Level: levelMust, AppliesTo: techPtr("Backend")},
			{Name: "\n", Level: "expert", AppliesTo: techPtr("Backend")},
			{Name: "Go", Level: levelMust, AppliesTo: nil},
		}})
		if err != nil {
			t.Fatal(err)
		}
		md := string(got)
		if strings.Contains(md, "Backend") || strings.Contains(md, "expert") || !strings.Contains(md, `"Go": [`) {
			t.Fatalf("markdown %s", md)
		}
	})

	t.Run("keeps chinese", func(t *testing.T) {
		got, err := TechDistribution("zh", Analysis{Tech: []Tech{
			{Name: "前端", Level: levelAdvantage, AppliesTo: techPtr("介面")},
		}})
		if err != nil {
			t.Fatal(err)
		}
		md := string(got)
		if !strings.Contains(md, `"介面": [0.250, 0.750]`) || !strings.Contains(md, "| 介面 | 0 | 1 | 前端 |") {
			t.Fatalf("markdown %s", md)
		}
	})

	t.Run("rejects a bad level", func(t *testing.T) {
		got, err := TechDistribution("bad", Analysis{Tech: []Tech{{Name: "Go", Level: "expert", AppliesTo: techPtr("Backend")}}})
		if !errors.Is(err, ErrTechDistributionInvalid) || got != nil || err.Error() != "docs-cli: tech distribution is invalid" {
			t.Fatalf("got %q err %v", got, err)
		}
	})
}
