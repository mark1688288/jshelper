package jobad

import (
	"errors"
	"strconv"
	"strings"
)

// ErrTechDistributionInvalid is returned when a drawn tech level is not must or advantage.
// The CLI prints this text and writes neither the relationship diagram nor this chart.
var ErrTechDistributionInvalid = errors.New("docs-cli: tech distribution is invalid")

// distInit is the first line inside every non-empty distribution fence.
// Width and height stay fixed, so every file uses the same init.
const distInit = `%%{init: {"theme": "base", "quadrantChart": {"chartWidth": 860, "chartHeight": 860, "pointLabelFontSize": 14, "pointRadius": 6, "pointTextPadding": 10, "quadrantLabelFontSize": 14}, "themeVariables": {"quadrant1Fill": "#E8E8E8", "quadrant2Fill": "#E8E8E8", "quadrant3Fill": "#D5D5D5", "quadrant4Fill": "#D5D5D5", "quadrant1TextFill": "#262626", "quadrant2TextFill": "#262626", "quadrant3TextFill": "#262626", "quadrant4TextFill": "#262626", "quadrantPointTextFill": "#262626", "quadrantXAxisTextFill": "#262626", "quadrantYAxisTextFill": "#262626", "quadrantTitleFill": "#262626", "quadrantInternalBorderStrokeFill": "#6E6E6E", "quadrantExternalBorderStrokeFill": "#3A3A3A"}}}%%`

// TechDistribution writes analysis.Tech as UTF-8 Markdown.
// The file is a Mermaid quadrantChart plus a name table.
// stem is the chart title. It does not write a file and does not call fm or Gemini.
// The same stem and Analysis always produce the same bytes.
func TechDistribution(stem string, analysis Analysis) ([]byte, error) {
	points, err := distPoints(analysis.Tech)
	if err != nil {
		return nil, err
	}
	if len(points) == 0 {
		return []byte("(none)\n"), nil
	}
	return renderDistMarkdown(stem, points), nil
}

type distPoint struct {
	label string
	names []string
	must  int
	adv   int
}

type distCell struct {
	m int
	a int
}

func distPoints(items []Tech) ([]distPoint, error) {
	var points []distPoint
	index := map[string]int{}
	for _, item := range items {
		name := diagramText(item.Name)
		if name == "" {
			continue
		}
		if item.Level != levelMust && item.Level != levelAdvantage {
			return nil, ErrTechDistributionInvalid
		}
		applies := ""
		if item.AppliesTo != nil {
			applies = diagramText(*item.AppliesTo)
		}
		must, adv := 0, 1
		if item.Level == levelMust {
			must, adv = 1, 0
		}
		if applies == "" {
			points = append(points, distPoint{
				label: name,
				names: []string{name},
				must:  must,
				adv:   adv,
			})
			continue
		}
		if at, ok := index[applies]; ok {
			points[at].names = append(points[at].names, name)
			points[at].must += must
			points[at].adv += adv
			continue
		}
		index[applies] = len(points)
		points = append(points, distPoint{
			label: applies,
			names: []string{name},
			must:  must,
			adv:   adv,
		})
	}
	return points, nil
}

// distBand is one more than the largest count, at least 2, and even.
// An even band puts 0.5 on the boundary between two integer bands.
func distBand(points []distPoint) int {
	maxCount := 0
	for _, point := range points {
		if point.must > maxCount {
			maxCount = point.must
		}
		if point.adv > maxCount {
			maxCount = point.adv
		}
	}
	b := maxCount + 1
	if b < 2 {
		b = 2
	}
	if b%2 == 1 {
		b++
	}
	return b
}

func renderDistMarkdown(stem string, points []distPoint) []byte {
	b := distBand(points)
	title := strings.ReplaceAll(diagramText(stem), `"`, "")
	left := countSpan(0, b/2-1)
	right := countSpan(b/2, b-1)
	var buf strings.Builder
	buf.WriteString("```mermaid\n")
	buf.WriteString(distInit)
	buf.WriteString("\nquadrantChart\n")
	buf.WriteString("    title ")
	buf.WriteString(title)
	buf.WriteByte('\n')
	buf.WriteString("    x-axis \"must 0\" --> \"must ")
	buf.WriteString(strconv.Itoa(b - 1))
	buf.WriteString("\"\n")
	buf.WriteString("    y-axis \"advantage 0\" --> \"advantage ")
	buf.WriteString(strconv.Itoa(b - 1))
	buf.WriteString("\"\n")
	writeQuadrant(&buf, 1, right, right)
	writeQuadrant(&buf, 2, left, right)
	writeQuadrant(&buf, 3, left, left)
	writeQuadrant(&buf, 4, right, left)
	for _, line := range pointLines(points, b) {
		buf.WriteString("    ")
		buf.WriteString(line)
		buf.WriteByte('\n')
	}
	buf.WriteString("```\n\n")
	buf.WriteString("| applies_to | must | advantage | technical |\n")
	buf.WriteString("| --- | --- | --- | --- |\n")
	for _, point := range points {
		buf.WriteString("| ")
		buf.WriteString(distLabel(point.label))
		buf.WriteString(" | ")
		buf.WriteString(strconv.Itoa(point.must))
		buf.WriteString(" | ")
		buf.WriteString(strconv.Itoa(point.adv))
		buf.WriteString(" | ")
		buf.WriteString(strings.Join(point.names, " / "))
		buf.WriteString(" |\n")
	}
	return []byte(buf.String())
}

func writeQuadrant(buf *strings.Builder, n int, must, adv string) {
	buf.WriteString("    quadrant-")
	buf.WriteString(strconv.Itoa(n))
	buf.WriteString(" \"must ")
	buf.WriteString(must)
	buf.WriteString(" · advantage ")
	buf.WriteString(adv)
	buf.WriteString("\"\n")
}

func countSpan(lo, hi int) string {
	if lo == hi {
		return strconv.Itoa(lo)
	}
	return strconv.Itoa(lo) + "–" + strconv.Itoa(hi)
}

func distLabel(s string) string {
	return strings.ReplaceAll(s, `"`, "'")
}

func pointLines(points []distPoint, b int) []string {
	counts := map[distCell]int{}
	for _, point := range points {
		counts[distCell{m: point.must, a: point.adv}]++
	}
	seen := map[distCell]int{}
	lines := make([]string, len(points))
	for n, point := range points {
		key := distCell{m: point.must, a: point.adv}
		i := seen[key]
		seen[key] = i + 1
		x, y := pointCoord(point.must, point.adv, i, counts[key], b)
		lines[n] = strconv.Quote(distLabel(point.label)) + ": [" + x + ", " + y + "] " + pointStyle(point.must, point.adv)
	}
	return lines
}

func pointStyle(must, adv int) string {
	switch {
	case adv == 0:
		return "color: #3A3A3A, stroke-color: #3A3A3A, stroke-width: 1px"
	case must == 0:
		return "color: #F2F2F2, stroke-color: #3A3A3A, stroke-width: 2px"
	default:
		return "color: #737373, stroke-color: #3A3A3A, stroke-width: 1px"
	}
}

// pointCoord places point i of k inside cell (m, a).
// k is 1 to 4 uses the cell center. More than 4 uses columns of 4.
func pointCoord(m, a, i, k, b int) (string, string) {
	if k <= 4 {
		xNum := int64(2*m + 1)
		xDen := int64(2 * b)
		yNum := int64(20*k*a + 19*k - 9*(2*i+1))
		yDen := int64(20 * k * b)
		return formatCoord(xNum, xDen), formatCoord(yNum, yDen)
	}
	cols := (k + 3) / 4
	c := i / 4
	r := i % 4
	xNum := int64(20*cols*m + cols + 9*(2*c+1))
	xDen := int64(20 * cols * b)
	yNum := int64(80*a + 76 - 9*(2*r+1))
	yDen := int64(80 * b)
	return formatCoord(xNum, xDen), formatCoord(yNum, yDen)
}

// formatCoord prints a positive ratio to three decimal places.
// The ratio is rounded half away from zero in decimal first.
// FormatFloat of the nearest float64 to 0.3625 prints 0.362, and the chart spec locks 0.363.
func formatCoord(num, den int64) string {
	q := (num * 1000) / den
	rem := (num * 1000) % den
	if rem*2 >= den {
		q++
	}
	return strconv.FormatFloat(float64(q)/1000, 'f', 3, 64)
}
