package jobad

import (
	"bytes"
	"encoding/xml"
	"errors"
	"strconv"
	"strings"
	"unicode"
)

// ErrTechDiagramInvalid is returned when a drawn tech level is not must or advantage.
// The CLI prints this text and does not write the diagram.
var ErrTechDiagramInvalid = errors.New("docs-cli: tech diagram is invalid")

const (
	diagramPad   = 24
	diagramFont  = 16
	diagramChar  = 8
	diagramRow   = 56
	diagramArrow = 80
	diagramGap   = 16
)

// TechDiagram draws analysis.Tech as a UTF-8 SVG.
// It does not write a file and does not call fm or Gemini.
// The same Analysis always produces the same bytes.
func TechDiagram(analysis Analysis) ([]byte, error) {
	rows, err := techRows(analysis.Tech)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return emptyTechDiagram(), nil
	}
	return renderTechRows(rows), nil
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

// diagramText is the label drawn on the SVG.
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

func textPx(s string) int {
	return len([]rune(s)) * diagramChar
}

func emptyTechDiagram() []byte {
	label := "(none)"
	w := diagramPad + textPx(label) + diagramPad
	h := diagramPad + diagramRow + diagramPad
	var b strings.Builder
	writeTechHeader(&b, w, h)
	b.WriteString(`<g transform="translate(`)
	b.WriteString(strconv.Itoa(diagramPad))
	b.WriteString(` `)
	b.WriteString(strconv.Itoa(diagramPad + diagramFont))
	b.WriteString(")\">\n")
	b.WriteString("<text class=\"empty\">(none)</text>\n")
	b.WriteString("</g>\n")
	b.WriteString("</svg>\n")
	return []byte(b.String())
}

func renderTechRows(rows []techRow) []byte {
	maxLinkName := 0
	maxApplies := 0
	maxLeft := 0
	anyLink := false
	for _, row := range rows {
		label := strings.Join(row.names, " / ")
		if row.standalone {
			if w := textPx(label); w > maxLeft {
				maxLeft = w
			}
			if w := textPx(row.level); w > maxLeft {
				maxLeft = w
			}
			continue
		}
		anyLink = true
		if w := textPx(label); w > maxLinkName {
			maxLinkName = w
		}
		if w := textPx(row.applies); w > maxApplies {
			maxApplies = w
		}
	}
	width := diagramPad + maxLeft + diagramPad
	appliesX := 0
	if anyLink {
		appliesX = diagramPad + maxLinkName + diagramGap + diagramArrow + diagramGap
		if right := appliesX + maxApplies; right+diagramPad > width {
			width = right + diagramPad
		}
	}
	height := diagramPad + len(rows)*diagramRow + diagramPad

	var b strings.Builder
	writeTechHeader(&b, width, height)
	if anyLink {
		b.WriteString("<defs>\n")
		b.WriteString(`<marker id="arrow" viewBox="0 0 8 8" markerWidth="8" markerHeight="8" refX="8" refY="4" orient="auto">` + "\n")
		b.WriteString(`<polygon points="0 0, 8 4, 0 8" fill="#111111"/>` + "\n")
		b.WriteString("</marker>\n")
		b.WriteString("</defs>\n")
	}
	type anchor struct {
		x2 int
		y  int
	}
	anchors := map[string]anchor{}
	for i, row := range rows {
		label := strings.Join(row.names, " / ")
		nameY := diagramPad + diagramFont + i*diagramRow
		b.WriteString(`<g id="row-`)
		b.WriteString(strconv.Itoa(i + 1))
		b.WriteString(`" class="`)
		if row.standalone {
			b.WriteString("standalone")
		} else {
			b.WriteString("link")
		}
		b.WriteString("\">\n")
		writeTechText(&b, "name", diagramPad, nameY, label)
		if row.standalone {
			writeTechText(&b, "level", diagramPad, nameY+22, row.level)
			b.WriteString("</g>\n")
			continue
		}
		x1 := diagramPad + textPx(label) + diagramGap
		y1 := nameY - 4
		x2 := appliesX - diagramGap
		y2 := y1
		drawApplies := false
		if at, ok := anchors[row.applies]; ok {
			x2 = at.x2
			y2 = at.y
		} else {
			anchors[row.applies] = anchor{x2: x2, y: y2}
			drawApplies = true
		}
		writeTechLine(&b, row.level, x1, y1, x2, y2)
		midX := (x1 + x2) / 2
		midY := (y1 + y2) / 2
		levelX := midX - textPx(row.level)/2
		if levelX < 0 {
			levelX = 0
		}
		writeTechText(&b, "level", levelX, midY-8, row.level)
		if drawApplies {
			writeTechText(&b, "applies", appliesX, nameY, row.applies)
		}
		b.WriteString("</g>\n")
	}
	b.WriteString("</svg>\n")
	return []byte(b.String())
}

func writeTechHeader(b *strings.Builder, w, h int) {
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	b.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 `)
	b.WriteString(strconv.Itoa(w))
	b.WriteString(` `)
	b.WriteString(strconv.Itoa(h))
	b.WriteString(`" font-family="sans-serif" font-size="16" fill="#111111">` + "\n")
	b.WriteString("<title>tech</title>\n")
}

func writeTechText(b *strings.Builder, class string, x, y int, text string) {
	b.WriteString(`<text class="`)
	b.WriteString(class)
	b.WriteString(`" x="`)
	b.WriteString(strconv.Itoa(x))
	b.WriteString(`" y="`)
	b.WriteString(strconv.Itoa(y))
	b.WriteString(`">`)
	var esc bytes.Buffer
	_ = xml.EscapeText(&esc, []byte(text))
	b.Write(esc.Bytes())
	b.WriteString("</text>\n")
}

func writeTechLine(b *strings.Builder, level string, x1, y1, x2, y2 int) {
	b.WriteString(`<line class="`)
	b.WriteString(level)
	b.WriteString(`" x1="`)
	b.WriteString(strconv.Itoa(x1))
	b.WriteString(`" y1="`)
	b.WriteString(strconv.Itoa(y1))
	b.WriteString(`" x2="`)
	b.WriteString(strconv.Itoa(x2))
	b.WriteString(`" y2="`)
	b.WriteString(strconv.Itoa(y2))
	b.WriteString(`" stroke="`)
	if level == levelAdvantage {
		b.WriteString(`#9a3412" stroke-dasharray="6 4"`)
	} else {
		b.WriteString(`#111111"`)
	}
	b.WriteString(` stroke-width="2" marker-end="url(#arrow)"/>` + "\n")
}
