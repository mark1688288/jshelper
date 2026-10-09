package jobad

import (
	"errors"
	"strconv"
	"strings"
	"unicode"
)

// ErrTechDiagramInvalid is returned when a drawn tech level is not must or advantage.
// The CLI prints this text and does not write the diagram.
var ErrTechDiagramInvalid = errors.New("docs-cli: tech diagram is invalid")

// flowchartInit is the first line inside every non-empty relationship fence.
const flowchartInit = `%%{init: {"theme": "base", "flowchart": {"htmlLabels": true}, "themeVariables": {"background": "#E8E8E8", "mainBkg": "#E8E8E8", "fontFamily": "sans-serif", "fontSize": "16px", "primaryColor": "#E8E8E8", "primaryTextColor": "#262626", "primaryBorderColor": "#6E6E6E", "lineColor": "#3A3A3A", "textColor": "#262626", "edgeLabelBackground": "#E8E8E8"}}}%%`

// TechDiagram writes analysis.Tech as UTF-8 Markdown.
// The file is a Mermaid flowchart. It does not write a file and does not call fm or Gemini.
// The same Analysis always produces the same bytes.
func TechDiagram(analysis Analysis) ([]byte, error) {
	rows, err := techRows(analysis.Tech)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return []byte("(none)\n"), nil
	}
	return renderFlowchart(rows), nil
}

type techRow struct {
	names      []string
	level      string
	applies    string
	standalone bool
}

type techGroupKey struct {
	applies string
	level   string
}

func techRows(items []Tech) ([]techRow, error) {
	var rows []techRow
	index := map[techGroupKey]int{}
	for _, item := range items {
		name := diagramText(item.Name)
		if name == "" {
			continue
		}
		if item.Level != levelMust && item.Level != levelAdvantage {
			return nil, ErrTechDiagramInvalid
		}
		applies := ""
		if item.AppliesTo != nil {
			applies = diagramText(*item.AppliesTo)
		}
		if applies == "" {
			rows = append(rows, techRow{
				names:      []string{name},
				level:      item.Level,
				standalone: true,
			})
			continue
		}
		key := techGroupKey{applies: applies, level: item.Level}
		if at, ok := index[key]; ok {
			rows[at].names = append(rows[at].names, name)
			continue
		}
		index[key] = len(rows)
		rows = append(rows, techRow{
			names:   []string{name},
			level:   item.Level,
			applies: applies,
		})
	}
	return rows, nil
}

// diagramText is the label drawn on a diagram.
// Whitespace collapses to one space. Other control runes are dropped.
func diagramText(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 && !unicode.IsSpace(r) {
			continue
		}
		b.WriteRune(r)
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// flowchartLabel rewrites characters that would break a Mermaid node label.
// The rewrite is only for this diagram. "<br/>" is inserted after it.
func flowchartLabel(s string) string {
	return strings.NewReplacer(
		`"`, `'`,
		`[`, `(`,
		`]`, `)`,
		`<`, `(`,
		`>`, `)`,
	).Replace(s)
}

func renderFlowchart(rows []techRow) []byte {
	var b strings.Builder
	b.WriteString("```mermaid\n")
	b.WriteString(flowchartInit)
	b.WriteString("\nflowchart LR\n")
	b.WriteString("    classDef must fill:#3A3A3A,stroke:#3A3A3A,color:#F2F2F2,stroke-width:1px\n")
	b.WriteString("    classDef advantage fill:#F2F2F2,stroke:#3A3A3A,color:#262626,stroke-width:2px\n")
	b.WriteString("    classDef target fill:#D5D5D5,stroke:#6E6E6E,color:#262626,stroke-width:1px\n")

	next := 1
	targets := map[string]string{}
	links := 0
	id := func() string {
		s := "n" + strconv.Itoa(next)
		next++
		return s
	}
	for _, row := range rows {
		label := flowchartLabel(strings.Join(row.names, " / "))
		if row.standalone {
			src := id()
			b.WriteString("    ")
			b.WriteString(src)
			b.WriteString(`["`)
			b.WriteString(label)
			b.WriteString("<br/>")
			b.WriteString(row.level)
			b.WriteString(`"]:::`)
			b.WriteString(row.level)
			b.WriteByte('\n')
			continue
		}
		src := id()
		b.WriteString("    ")
		b.WriteString(src)
		b.WriteString(`["`)
		b.WriteString(label)
		b.WriteString(`"]:::`)
		b.WriteString(row.level)
		b.WriteByte('\n')
		tgt, ok := targets[row.applies]
		if !ok {
			tgt = id()
			targets[row.applies] = tgt
			b.WriteString("    ")
			b.WriteString(tgt)
			b.WriteString(`["`)
			b.WriteString(flowchartLabel(row.applies))
			b.WriteString(`"]:::target`)
			b.WriteByte('\n')
		}
		b.WriteString("    ")
		b.WriteString(src)
		if row.level == levelAdvantage {
			b.WriteString(" -.->|advantage| ")
		} else {
			b.WriteString(" -->|must| ")
		}
		b.WriteString(tgt)
		b.WriteByte('\n')
		links++
	}
	for i := 0; i < links; i++ {
		b.WriteString("    linkStyle ")
		b.WriteString(strconv.Itoa(i))
		b.WriteString(" stroke:#3A3A3A,stroke-width:2px\n")
	}
	b.WriteString("```\n")
	return []byte(b.String())
}
