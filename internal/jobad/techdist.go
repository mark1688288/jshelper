package jobad

import (
	"bytes"
	"encoding/xml"
	"errors"
	"strconv"
	"strings"
)

// ErrTechDistributionInvalid is returned when a drawn tech level is not must or advantage.
// The CLI prints this text and writes neither diagram.
var ErrTechDistributionInvalid = errors.New("docs-cli: tech distribution is invalid")

const (
	distPad       = 24
	distGap       = 16
	distFont      = 16
	distLine      = 22
	distCellMin   = 96
	distCellPad   = 16
	distCircleGap = 12
	distTextPad   = 12
	distEllipseIn = 6
)

// TechDistribution draws analysis.Tech as a UTF-8 SVG.
// The horizontal axis is must and the vertical axis is advantage.
// SVG y grows downward, so tick 0 is the bottom of the plot.
// It does not write a file and does not call fm or Gemini.
// The same Analysis always produces the same bytes.
func TechDistribution(analysis Analysis) ([]byte, error) {
	circles, err := techCircles(analysis.Tech)
	if err != nil {
		return nil, err
	}
	return renderTechDistribution(circles), nil
}

type distCircle struct {
	title string
	names []string
	must  int
	adv   int
}

type distBubble struct {
	title  string
	names  string
	titleW int
	namesW int
	rx     int
	ry     int
}

type distCellKey struct {
	x int
	y int
}

func techCircles(items []Tech) ([]distCircle, error) {
	var circles []distCircle
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
			circles = append(circles, distCircle{
				title: name,
				names: []string{name},
				must:  must,
				adv:   adv,
			})
			continue
		}
		if at, ok := index[applies]; ok {
			circles[at].names = append(circles[at].names, name)
			circles[at].must += must
			circles[at].adv += adv
			continue
		}
		index[applies] = len(circles)
		circles = append(circles, distCircle{
			title: applies,
			names: []string{name},
			must:  must,
			adv:   adv,
		})
	}
	return circles, nil
}

// distCell is the band between tick count-1 and tick count.
// A count of 0 shares the cell between 0 and 1.
func distCell(count int) int {
	if count < 1 {
		return 1
	}
	return count
}

func renderTechDistribution(circles []distCircle) []byte {
	n := 2
	for _, circle := range circles {
		if circle.must > n {
			n = circle.must
		}
		if circle.adv > n {
			n = circle.adv
		}
	}

	var order []distCellKey
	groups := map[distCellKey][]distBubble{}
	for _, circle := range circles {
		key := distCellKey{x: distCell(circle.must), y: distCell(circle.adv)}
		bubble := measureBubble(circle)
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], bubble)
	}

	colW := make([]int, n+1)
	rowH := make([]int, n+1)
	for i := 1; i <= n; i++ {
		colW[i] = distCellMin
		rowH[i] = distCellMin
	}
	for key, bubbles := range groups {
		maxW := 0
		totalH := 0
		for i, bubble := range bubbles {
			if i > 0 {
				totalH += distCircleGap
			}
			totalH += 2 * bubble.ry
			if w := 2 * bubble.rx; w > maxW {
				maxW = w
			}
		}
		if need := maxW + 2*distCellPad; need > colW[key.x] {
			colW[key.x] = need
		}
		if need := totalH + 2*distCellPad; need > rowH[key.y] {
			rowH[key.y] = need
		}
	}

	plotW := 0
	plotH := 0
	for i := 1; i <= n; i++ {
		plotW += colW[i]
		plotH += rowH[i]
	}
	tickW := textPx(strconv.Itoa(n))
	advW := textPx("advantage")
	originX := distPad + advW + distGap + tickW + distGap
	originY := distPad + plotH
	xs := make([]int, n+1)
	ys := make([]int, n+1)
	xs[0] = originX
	ys[0] = originY
	for i := 1; i <= n; i++ {
		xs[i] = xs[i-1] + colW[i]
		ys[i] = ys[i-1] - rowH[i]
	}
	xTickY := originY + distGap + distFont
	mustY := xTickY + distGap + distFont
	width := xs[n] + distPad
	height := mustY + distPad

	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	b.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 `)
	b.WriteString(strconv.Itoa(width))
	b.WriteString(` `)
	b.WriteString(strconv.Itoa(height))
	b.WriteString("\">\n")
	b.WriteString("<title>tech distribution</title>\n")

	for x := 1; x <= n; x++ {
		for y := 1; y <= n; y++ {
			writeDistRect(&b, x, y, xs[x-1], ys[y], colW[x], rowH[y])
		}
	}
	writeDistLine(&b, xs[0], ys[0], xs[n], ys[0])
	writeDistLine(&b, xs[0], ys[0], xs[0], ys[n])
	for i := 0; i <= n; i++ {
		label := strconv.Itoa(i)
		writeDistLabel(&b, xs[i]-textPx(label)/2, xTickY, "tick", label)
		writeDistLabel(&b, xs[0]-distGap-textPx(label), ys[i]+4, "tick", label)
	}
	writeDistLabel(&b, originX+plotW/2-textPx("must")/2, mustY, "axis-label", "must")
	writeDistLabel(&b, distPad, distPad+plotH/2+4, "axis-label", "advantage")

	if len(circles) == 0 {
		writeDistLabel(&b, originX+plotW/2-textPx("(none)")/2, distPad+plotH/2, "empty", "(none)")
		b.WriteString("</svg>\n")
		return []byte(b.String())
	}

	for _, key := range order {
		b.WriteString(`<g class="at" id="cell-`)
		b.WriteString(strconv.Itoa(key.x))
		b.WriteString("-")
		b.WriteString(strconv.Itoa(key.y))
		b.WriteString("\">\n")
		left := xs[key.x-1]
		top := ys[key.y]
		cx := left + colW[key.x]/2
		cursor := top + distCellPad
		bubbles := groups[key]
		for i, bubble := range bubbles {
			if i > 0 {
				cursor += distCircleGap
			}
			cy := cursor + bubble.ry
			writeDistCircle(&b, cx, cy, bubble)
			cursor += 2 * bubble.ry
		}
		b.WriteString("</g>\n")
	}
	b.WriteString("</svg>\n")
	return []byte(b.String())
}

func measureBubble(circle distCircle) distBubble {
	names := strings.Join(circle.names, " / ")
	titleW := textPx(circle.title)
	namesW := textPx(names)
	textW := titleW
	if namesW > textW {
		textW = namesW
	}
	rx := textW/2 + distTextPad
	ry := distLine/2 + distFont + distEllipseIn
	if rx < ry {
		rx = ry
	}
	return distBubble{
		title:  circle.title,
		names:  names,
		titleW: titleW,
		namesW: namesW,
		rx:     rx,
		ry:     ry,
	}
}

func writeDistRect(b *strings.Builder, x, y, left, top, w, h int) {
	b.WriteString(`<rect class="grid-cell" id="grid-`)
	b.WriteString(strconv.Itoa(x))
	b.WriteString("-")
	b.WriteString(strconv.Itoa(y))
	b.WriteString(`" x="`)
	b.WriteString(strconv.Itoa(left))
	b.WriteString(`" y="`)
	b.WriteString(strconv.Itoa(top))
	b.WriteString(`" width="`)
	b.WriteString(strconv.Itoa(w))
	b.WriteString(`" height="`)
	b.WriteString(strconv.Itoa(h))
	b.WriteString(`" fill="#ffffff" stroke="#d6d3d1"/>` + "\n")
}

func writeDistLine(b *strings.Builder, x1, y1, x2, y2 int) {
	b.WriteString(`<line class="axis" x1="`)
	b.WriteString(strconv.Itoa(x1))
	b.WriteString(`" y1="`)
	b.WriteString(strconv.Itoa(y1))
	b.WriteString(`" x2="`)
	b.WriteString(strconv.Itoa(x2))
	b.WriteString(`" y2="`)
	b.WriteString(strconv.Itoa(y2))
	b.WriteString(`" stroke="#111111"/>` + "\n")
}

func writeDistCircle(b *strings.Builder, cx, cy int, bubble distBubble) {
	b.WriteString("<g class=\"circle\">\n")
	b.WriteString(`<ellipse class="circle" cx="`)
	b.WriteString(strconv.Itoa(cx))
	b.WriteString(`" cy="`)
	b.WriteString(strconv.Itoa(cy))
	b.WriteString(`" rx="`)
	b.WriteString(strconv.Itoa(bubble.rx))
	b.WriteString(`" ry="`)
	b.WriteString(strconv.Itoa(bubble.ry))
	b.WriteString(`" stroke="#111111" fill="#ffffff"/>` + "\n")
	writeDistLabel(b, cx-bubble.titleW/2, cy-distLine/2, "title", bubble.title)
	writeDistLabel(b, cx-bubble.namesW/2, cy+distLine/2, "names", bubble.names)
	b.WriteString("</g>\n")
}

func writeDistLabel(b *strings.Builder, x, y int, class, text string) {
	b.WriteString(`<g transform="translate(`)
	b.WriteString(strconv.Itoa(x))
	b.WriteString(` `)
	b.WriteString(strconv.Itoa(y))
	b.WriteString(")\">\n")
	b.WriteString(`<text class="`)
	b.WriteString(class)
	b.WriteString(`">`)
	var esc bytes.Buffer
	_ = xml.EscapeText(&esc, []byte(text))
	b.Write(esc.Bytes())
	b.WriteString("</text>\n")
	b.WriteString("</g>\n")
}
