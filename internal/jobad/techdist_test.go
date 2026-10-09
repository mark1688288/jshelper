package jobad

import (
	"bytes"
	"errors"
	"strconv"
	"strings"
	"testing"
)

func TestTechDistribution(t *testing.T) {
	t.Run("places the 95169560 shape", func(t *testing.T) {
		analysis := Analysis{
			MustHave:   []string{"NOT-IN-CHART"},
			Advantage:  []string{"ALSO-NOT"},
			Languages:  []string{"English"},
			SoftSkills: []string{"Communication"},
			Tech: []Tech{
				{Name: "Java or .NET", Level: levelMust, AppliesTo: techPtr("Backend")},
				{Name: "RESTful services", Level: levelMust, AppliesTo: techPtr("APIs")},
				{Name: "Angular, React, or similar modern frameworks", Level: levelMust, AppliesTo: techPtr("Frontend")},
				{Name: "GraphQL", Level: levelAdvantage, AppliesTo: techPtr("APIs")},
				{Name: "SQL (MSSQL, MySQL, or PostgreSQL)", Level: levelMust, AppliesTo: techPtr("Database")},
				{Name: "WebSockets", Level: levelAdvantage, AppliesTo: techPtr("APIs")},
				{Name: "Docker", Level: levelMust, AppliesTo: techPtr("Containerization")},
			},
		}
		got, err := TechDistribution(analysis)
		if err != nil {
			t.Fatal(err)
		}
		again, err := TechDistribution(analysis)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, again) {
			t.Fatal("distribution bytes changed")
		}
		svg := string(got)
		if !strings.HasPrefix(svg, "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n") || !strings.HasSuffix(svg, "</svg>\n") {
			t.Fatalf("svg envelope %q", svg[:80])
		}
		if !strings.Contains(svg, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 `) || !strings.Contains(svg, "<title>tech distribution</title>") {
			t.Fatalf("svg header %s", svg)
		}
		if strings.Count(svg, `class="grid-cell"`) != 4 {
			t.Fatalf("grid cells %d\n%s", strings.Count(svg, `class="grid-cell"`), svg)
		}
		for _, id := range []string{"grid-1-1", "grid-1-2", "grid-2-1", "grid-2-2"} {
			if !strings.Contains(svg, `id="`+id+`"`) {
				t.Fatalf("missing %s\n%s", id, svg)
			}
		}
		if strings.Count(svg, `id="grid-`) != 4 {
			t.Fatalf("extra grid ids\n%s", svg)
		}
		for _, banned := range []string{`id="cell-1-0"`, `id="cell-0-1"`, "<script", "foreignObject", "NOT-IN-CHART", "ALSO-NOT", "English", "Communication"} {
			if strings.Contains(svg, banned) {
				t.Fatalf("svg contains %q", banned)
			}
		}
		if strings.Count(svg, `<line class="axis"`) != 2 {
			t.Fatalf("axes %d\n%s", strings.Count(svg, `<line class="axis"`), svg)
		}
		for _, label := range []string{"must", "advantage"} {
			if !strings.Contains(svg, `<text class="axis-label">`+label+`</text>`) {
				t.Fatalf("missing axis label %s\n%s", label, svg)
			}
		}
		for _, tick := range []string{"0", "1", "2"} {
			if strings.Count(svg, `<text class="tick">`+tick+`</text>`) != 2 {
				t.Fatalf("tick %s count %d\n%s", tick, strings.Count(svg, `<text class="tick">`+tick+`</text>`), svg)
			}
		}
		apis := distGroup(t, svg, "cell-1-2")
		for _, want := range []string{
			`<text class="title">APIs</text>`,
			`<text class="names">RESTful services / GraphQL / WebSockets</text>`,
			`<ellipse class="circle"`,
		} {
			if !strings.Contains(apis, want) {
				t.Fatalf("cell-1-2 missing %s\n%s", want, apis)
			}
		}
		if strings.Count(apis, "<ellipse") != 1 {
			t.Fatalf("cell-1-2 ellipses %d\n%s", strings.Count(apis, "<ellipse"), apis)
		}
		low := distGroup(t, svg, "cell-1-1")
		titles := []string{">Backend</text>", ">Frontend</text>", ">Database</text>", ">Containerization</text>"}
		names := []string{
			">Java or .NET</text>",
			">Angular, React, or similar modern frameworks</text>",
			">SQL (MSSQL, MySQL, or PostgreSQL)</text>",
			">Docker</text>",
		}
		prev := -1
		for _, want := range append(titles, names...) {
			at := strings.Index(low, want)
			if at < 0 {
				t.Fatalf("cell-1-1 missing %s\n%s", want, low)
			}
			if strings.HasPrefix(want, ">Backend") || strings.HasPrefix(want, ">Frontend") || strings.HasPrefix(want, ">Database") || strings.HasPrefix(want, ">Containerization") {
				if at < prev {
					t.Fatalf("title order %s\n%s", want, low)
				}
				prev = at
			}
		}
		if strings.Count(low, "<ellipse") != 4 {
			t.Fatalf("cell-1-1 ellipses %d\n%s", strings.Count(low, "<ellipse"), low)
		}
		cys := ellipseCYs(t, low)
		if len(cys) != 4 {
			t.Fatalf("cys %v", cys)
		}
		for i := 1; i < len(cys); i++ {
			if cys[i] <= cys[i-1] {
				t.Fatalf("circles overlap or are not top-first %v", cys)
			}
		}
	})

	t.Run("keeps null applies_to as two circles", func(t *testing.T) {
		got, err := TechDistribution(Analysis{Tech: []Tech{
			{Name: "Go", Level: levelMust, AppliesTo: nil},
			{Name: "Rust", Level: levelAdvantage, AppliesTo: nil},
		}})
		if err != nil {
			t.Fatal(err)
		}
		svg := string(got)
		cell := distGroup(t, svg, "cell-1-1")
		if strings.Count(cell, `<g class="circle">`) != 2 {
			t.Fatalf("circles %d\n%s", strings.Count(cell, `<g class="circle">`), cell)
		}
		if !strings.Contains(cell, `<text class="title">Go</text>`) || !strings.Contains(cell, `<text class="title">Rust</text>`) {
			t.Fatalf("titles %s", cell)
		}
		if !strings.Contains(cell, `<text class="names">Go</text>`) || !strings.Contains(cell, `<text class="names">Rust</text>`) {
			t.Fatalf("names %s", cell)
		}
		if strings.Contains(svg, " / ") || strings.Contains(svg, `id="cell-1-2"`) || strings.Contains(svg, `id="cell-1-0"`) {
			t.Fatalf("svg %s", svg)
		}
	})

	t.Run("blank applies_to is its own circle", func(t *testing.T) {
		got, err := TechDistribution(Analysis{Tech: []Tech{
			{Name: "Go", Level: levelMust, AppliesTo: techPtr("  ")},
		}})
		if err != nil {
			t.Fatal(err)
		}
		cell := distGroup(t, string(got), "cell-1-1")
		if !strings.Contains(cell, `<text class="title">Go</text>`) || strings.Contains(cell, " / ") {
			t.Fatalf("cell %s", cell)
		}
	})

	t.Run("empty tech is none", func(t *testing.T) {
		got, err := TechDistribution(Analysis{})
		if err != nil {
			t.Fatal(err)
		}
		svg := string(got)
		if !strings.Contains(svg, `<text class="empty">(none)</text>`) || strings.Contains(svg, `class="circle"`) {
			t.Fatalf("svg %s", svg)
		}
		if strings.Count(svg, `class="grid-cell"`) != 4 {
			t.Fatalf("grid %d\n%s", strings.Count(svg, `class="grid-cell"`), svg)
		}
		for _, tick := range []string{"0", "1", "2"} {
			if strings.Count(svg, `<text class="tick">`+tick+`</text>`) != 2 {
				t.Fatalf("tick %s\n%s", tick, svg)
			}
		}
		for _, label := range []string{"must", "advantage"} {
			if !strings.Contains(svg, `<text class="axis-label">`+label+`</text>`) {
				t.Fatalf("label %s\n%s", label, svg)
			}
		}
	})

	t.Run("all blank names are none", func(t *testing.T) {
		got, err := TechDistribution(Analysis{Tech: []Tech{{Name: " \t ", Level: levelMust}}})
		if err != nil {
			t.Fatal(err)
		}
		svg := string(got)
		if !strings.Contains(svg, `<text class="empty">(none)</text>`) || strings.Contains(svg, `class="circle"`) {
			t.Fatalf("svg %s", svg)
		}
	})

	t.Run("escapes names", func(t *testing.T) {
		got, err := TechDistribution(Analysis{Tech: []Tech{
			{Name: "A & B", Level: levelMust, AppliesTo: techPtr("X<Y")},
		}})
		if err != nil {
			t.Fatal(err)
		}
		svg := string(got)
		if !strings.Contains(svg, "A &amp; B") || !strings.Contains(svg, "X&lt;Y") || strings.Contains(svg, "A & B") || strings.Contains(svg, "X<Y") {
			t.Fatalf("svg %s", svg)
		}
	})

	t.Run("keeps case apart", func(t *testing.T) {
		got, err := TechDistribution(Analysis{Tech: []Tech{
			{Name: "Java", Level: levelMust, AppliesTo: techPtr("Backend")},
			{Name: "Go", Level: levelMust, AppliesTo: techPtr("backend")},
		}})
		if err != nil {
			t.Fatal(err)
		}
		cell := distGroup(t, string(got), "cell-1-1")
		if strings.Count(cell, `<g class="circle">`) != 2 || !strings.Contains(cell, `<text class="title">Backend</text>`) || !strings.Contains(cell, `<text class="title">backend</text>`) {
			t.Fatalf("cell %s", cell)
		}
	})

	t.Run("extends the axes past 2", func(t *testing.T) {
		got, err := TechDistribution(Analysis{Tech: []Tech{
			{Name: "A", Level: levelMust, AppliesTo: techPtr("Lang")},
			{Name: "B", Level: levelMust, AppliesTo: techPtr("Lang")},
			{Name: "C", Level: levelMust, AppliesTo: techPtr("Lang")},
		}})
		if err != nil {
			t.Fatal(err)
		}
		svg := string(got)
		if strings.Count(svg, `class="grid-cell"`) != 9 {
			t.Fatalf("grid %d\n%s", strings.Count(svg, `class="grid-cell"`), svg)
		}
		if strings.Count(svg, `<text class="tick">3</text>`) != 2 || strings.Contains(svg, `id="cell-3-0"`) {
			t.Fatalf("svg %s", svg)
		}
		cell := distGroup(t, svg, "cell-3-1")
		if !strings.Contains(cell, `<text class="title">Lang</text>`) || !strings.Contains(cell, `<text class="names">A / B / C</text>`) {
			t.Fatalf("cell %s", cell)
		}
	})

	t.Run("skips a blank name", func(t *testing.T) {
		got, err := TechDistribution(Analysis{Tech: []Tech{
			{Name: "  ", Level: levelMust, AppliesTo: techPtr("Backend")},
			{Name: "\n", Level: "expert", AppliesTo: techPtr("Backend")},
			{Name: "Go", Level: levelMust, AppliesTo: nil},
		}})
		if err != nil {
			t.Fatal(err)
		}
		svg := string(got)
		if strings.Contains(svg, "Backend") || strings.Contains(svg, "expert") || !strings.Contains(svg, `<text class="title">Go</text>`) {
			t.Fatalf("svg %s", svg)
		}
	})

	t.Run("keeps chinese", func(t *testing.T) {
		got, err := TechDistribution(Analysis{Tech: []Tech{
			{Name: "前端", Level: levelAdvantage, AppliesTo: techPtr("介面")},
		}})
		if err != nil {
			t.Fatal(err)
		}
		svg := string(got)
		if !strings.Contains(svg, `<text class="title">介面</text>`) || !strings.Contains(svg, `<text class="names">前端</text>`) {
			t.Fatalf("svg %s", svg)
		}
	})

	t.Run("rejects a bad level", func(t *testing.T) {
		got, err := TechDistribution(Analysis{Tech: []Tech{{Name: "Go", Level: "expert", AppliesTo: techPtr("Backend")}}})
		if !errors.Is(err, ErrTechDistributionInvalid) || got != nil || err.Error() != "docs-cli: tech distribution is invalid" {
			t.Fatalf("got %q err %v", got, err)
		}
	})
}

func distGroup(t *testing.T, svg, id string) string {
	t.Helper()
	needle := `id="` + id + `"`
	at := strings.Index(svg, needle)
	if at < 0 {
		t.Fatalf("missing %s\n%s", id, svg)
	}
	start := strings.LastIndex(svg[:at], "<g ")
	if start < 0 {
		t.Fatalf("no group for %s", id)
	}
	depth := 0
	i := start
	for i < len(svg) {
		nextOpen := strings.Index(svg[i:], "<g")
		nextClose := strings.Index(svg[i:], "</g>")
		if nextClose < 0 {
			t.Fatalf("unclosed %s", id)
		}
		if nextOpen >= 0 && nextOpen < nextClose {
			depth++
			i += nextOpen + 2
			continue
		}
		depth--
		i += nextClose + len("</g>")
		if depth == 0 {
			return svg[start:i]
		}
	}
	t.Fatalf("unclosed %s", id)
	return ""
}

func ellipseCYs(t *testing.T, group string) []int {
	t.Helper()
	var ys []int
	rest := group
	for {
		i := strings.Index(rest, `cy="`)
		if i < 0 {
			break
		}
		rest = rest[i+len(`cy="`):]
		j := strings.Index(rest, `"`)
		if j < 0 {
			t.Fatalf("unclosed cy %s", group)
		}
		n, err := strconv.Atoi(rest[:j])
		if err != nil {
			t.Fatal(err)
		}
		ys = append(ys, n)
		rest = rest[j:]
	}
	return ys
}
