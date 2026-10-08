package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

const (
	// Page geometry is in hundredths of a point so the baselines stay exact.
	pdfPageWidth  = 59528 // 595.28 pt, A4
	pdfPageHeight = 84189 // 841.89 pt, A4
	pdfMargin     = 7200  // 72 pt
	pdfFontSize   = 1100  // 11 pt
	pdfLeading    = 1600  // 16 pt between wrapped pieces of one source line
	// pdfBreak is the gap between source lines. Google Docs PDF import joins
	// baselines 16 or 18 points apart and starts a paragraph at 20, so a
	// subject line, a salutation, and the following paragraph stay apart.
	// Wrapped pieces stay on pdfLeading and remain one paragraph.
	pdfBreak = 2000 // 20 pt

	// pdfMaxWidth is the text width in thousandths of a point: (595.28-144)*1000.
	pdfMaxWidth = 595280 - 2*72000
)

// errPDFNotEncodable is a letter that WinAnsi cannot store.
// The CLI prints it as stderr and does not write a file.
var errPDFNotEncodable = errors.New("docs-cli: pdf text is not encodable")

// pdfExistsError is an output name that is already in drafts.
// O_EXCL uses the same text so a race does not overwrite the file.
type pdfExistsError struct{ name string }

func (e *pdfExistsError) Error() string {
	return "docs-cli: pdf already exists: " + e.name
}

// validPDFName reports whether name is a basename that ends in lowercase .pdf.
// The caller prints the raw flag string. This function does not trim it.
func validPDFName(name string) bool {
	if name == "" || name == "." || name == ".." || len(name) > 255 {
		return false
	}
	if strings.ContainsAny(name, `/\`) || filepath.Base(name) != name {
		return false
	}
	if !strings.HasSuffix(name, ".pdf") {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// writeDraftPDF writes drafts/name as one uncompressed PDF 1.4 file.
// A letter that is not WinAnsi returns errPDFNotEncodable and creates nothing.
// An existing path, including one created between the earlier Lstat and O_EXCL,
// returns pdfExistsError and leaves the old bytes alone.
func writeDraftPDF(dir, name, letter string) (string, error) {
	body, err := buildLetterPDF(letter)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return "", &pdfExistsError{name: name}
	}
	if err != nil {
		return "", err
	}
	_, werr := f.Write(body)
	cerr := f.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(path)
		if werr != nil {
			return "", werr
		}
		return "", cerr
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return abs, nil
}

// pdfLine is one visual row. gapBefore is the drop from the previous
// baseline, in hundredths of a point. The first row on a page ignores it.
type pdfLine struct {
	text      []byte
	gapBefore int
}

// buildLetterPDF lays the letter out on A4 in Helvetica 11 pt.
// A trailing newline does not add a blank line. \r\n and a lone \r become \n.
// A source newline drops pdfBreak. A wrap inside one source line drops pdfLeading.
func buildLetterPDF(letter string) ([]byte, error) {
	var lines []pdfLine
	for _, part := range splitLetterLines(letter) {
		encoded, err := encodeWinAnsi(part)
		if err != nil {
			return nil, err
		}
		wrapped := wrapWinAnsi(encoded)
		for j, piece := range wrapped {
			gap := 0
			if len(lines) > 0 {
				if j == 0 {
					gap = pdfBreak
				} else {
					gap = pdfLeading
				}
			}
			lines = append(lines, pdfLine{text: piece, gapBefore: gap})
		}
	}
	pages := paginateLetter(lines)
	if len(pages) == 0 {
		pages = [][]pdfLine{nil}
	}
	return renderLetterPDF(pages), nil
}

func splitLetterLines(letter string) []string {
	letter = strings.ReplaceAll(letter, "\r\n", "\n")
	letter = strings.ReplaceAll(letter, "\r", "\n")
	if letter == "" {
		return nil
	}
	parts := strings.Split(letter, "\n")
	if strings.HasSuffix(letter, "\n") {
		parts = parts[:len(parts)-1]
	}
	return parts
}

// encodeWinAnsi accepts printable ASCII, Latin-1 characters that WinAnsi
// keeps at the same byte, and the extra punctuation WinAnsi has for quotes,
// dashes, the bullet, the ellipsis, and nbsp. Filled and open circles used
// as template bullets map to that bullet and the middle dot. Ligatures
// expand to ASCII letters.
func encodeWinAnsi(s string) ([]byte, error) {
	s = normalizePDFText(s)
	out := make([]byte, 0, len(s))
	for _, r := range s {
		b, ok := winAnsiByte(r)
		if !ok {
			return nil, errPDFNotEncodable
		}
		out = append(out, b)
	}
	return out, nil
}

// normalizePDFText rewrites glyphs the cover-letter template and CV use
// into characters WinAnsi can store. Chinese is left unchanged so it still
// fails as not encodable.
func normalizePDFText(s string) string {
	return strings.NewReplacer(
		"\uFB00", "ff",
		"\uFB01", "fi",
		"\uFB02", "fl",
		"\uFB03", "ffi",
		"\uFB04", "ffl",
		"\u2010", "-",
		"\u2011", "-",
		"\u2012", "\u2013",
		"\u2015", "\u2014",
		"\u2212", "-",
		"\uFEFF", "",
		"\u200B", "",
		"\u200C", "",
		"\u200D", "",
	).Replace(s)
}

func winAnsiByte(r rune) (byte, bool) {
	switch r {
	case '\u2018':
		return 0x91, true // ‘
	case '\u2019':
		return 0x92, true // ’
	case '\u201C':
		return 0x93, true // “
	case '\u201D':
		return 0x94, true // ”
	case '\u2013':
		return 0x96, true // –
	case '\u2014':
		return 0x97, true // —
	case '\u2022', '\u25CF', '\u25E6', '\u2023', '\u2043', '\u2219':
		return 0x95, true // • ● ◦ and other filled bullets
	case '\u25CB':
		return 0xB7, true // ○ as a middle dot
	case '\u2026':
		return 0x85, true // …
	}
	if r >= 0x20 && r <= 0x7E {
		return byte(r), true
	}
	if r >= 0xA0 && r <= 0xFF {
		return byte(r), true
	}
	return 0, false
}

// wrapWinAnsi breaks a line that is wider than the text block.
// It prefers the last space whose prefix fits, and otherwise splits on bytes.
// A single byte that is wider than the block still stays on its own line.
func wrapWinAnsi(line []byte) [][]byte {
	if len(line) == 0 {
		return [][]byte{nil}
	}
	var out [][]byte
	for len(line) > 0 {
		if helveticaWidth(line) <= pdfMaxWidth {
			out = append(out, line)
			break
		}
		cut := -1
		for i, c := range line {
			if c != ' ' || i == 0 {
				continue
			}
			if helveticaWidth(line[:i]) <= pdfMaxWidth {
				cut = i
				continue
			}
			break
		}
		if cut > 0 {
			out = append(out, append([]byte(nil), line[:cut]...))
			line = line[cut+1:]
			continue
		}
		n := 1
		for n < len(line) && helveticaWidth(line[:n+1]) <= pdfMaxWidth {
			n++
		}
		out = append(out, append([]byte(nil), line[:n]...))
		line = line[n:]
	}
	return out
}

// helveticaWidth is the line width in thousandths of a point at 11 pt.
func helveticaWidth(line []byte) int {
	w := 0
	for _, c := range line {
		w += helveticaAFM[c] * (pdfFontSize / 100)
	}
	return w
}

// paginateLetter starts a new page when the next baseline would fall below
// the bottom margin. The first baseline is 841.89-72-11. A line that opens
// a page keeps that baseline and does not apply gapBefore.
func paginateLetter(lines []pdfLine) [][]pdfLine {
	if len(lines) == 0 {
		return nil
	}
	first := pdfPageHeight - pdfMargin - pdfFontSize
	var pages [][]pdfLine
	var page []pdfLine
	y := first
	for _, line := range lines {
		gap := line.gapBefore
		if len(page) == 0 {
			gap = 0
		}
		next := y - gap
		if len(page) > 0 && next < pdfMargin {
			pages = append(pages, page)
			page = nil
			next = first
		}
		page = append(page, line)
		y = next
	}
	if len(page) > 0 {
		pages = append(pages, page)
	}
	return pages
}

func pageStream(lines []pdfLine) string {
	var b strings.Builder
	b.WriteString("BT\n")
	fmt.Fprintf(&b, "/F1 %s Tf\n", formatPt(pdfFontSize))
	fmt.Fprintf(&b, "%s %s Td\n", formatPt(pdfMargin), formatPt(pdfPageHeight-pdfMargin-pdfFontSize))
	for i, line := range lines {
		if i > 0 && line.gapBefore > 0 {
			fmt.Fprintf(&b, "0 -%s Td\n", formatPt(line.gapBefore))
		}
		if len(line.text) == 0 {
			continue
		}
		b.WriteString(pdfString(line.text))
		b.WriteString(" Tj\n")
	}
	b.WriteString("ET\n")
	return b.String()
}

func pdfString(raw []byte) string {
	var b strings.Builder
	b.Grow(len(raw) + 2)
	b.WriteByte('(')
	for _, c := range raw {
		switch c {
		case '(', ')', '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte(')')
	return b.String()
}

func formatPt(hundredths int) string {
	if hundredths < 0 {
		return "-" + formatPt(-hundredths)
	}
	if hundredths%100 == 0 {
		return fmt.Sprintf("%d", hundredths/100)
	}
	return fmt.Sprintf("%d.%02d", hundredths/100, hundredths%100)
}

func renderLetterPDF(pages [][]pdfLine) []byte {
	streams := make([]string, len(pages))
	for i, lines := range pages {
		streams[i] = pageStream(lines)
	}
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
		kids[i] = fmt.Sprintf("%d 0 R", 4+2*i)
	}
	media := fmt.Sprintf("[0 0 %s %s]", formatPt(pdfPageWidth), formatPt(pdfPageHeight))
	writeObj("<< /Type /Catalog /Pages 2 0 R >>")
	writeObj(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), nPages))
	writeObj("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>")
	for i, stream := range streams {
		contentNum := 5 + 2*i
		writeObj(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox %s /Contents %d 0 R /Resources << /Font << /F1 3 0 R >> >> >>", media, contentNum))
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

// helveticaAFM is the Adobe Helvetica advance width of each byte, in
// thousandths of an em. Bytes 32–255 follow WinAnsiEncoding. Bytes 0–31
// use the missing-glyph width. A glyph at 11 pt is width*11/1000 points.
var helveticaAFM = [256]int{
	278, 278, 278, 278, 278, 278, 278, 278, 278, 278, 278, 278, 278, 278, 278, 278,
	278, 278, 278, 278, 278, 278, 278, 278, 278, 278, 278, 278, 278, 278, 278, 278,
	278, 278, 355, 556, 556, 889, 667, 191, 333, 333, 389, 584, 278, 333, 278, 278,
	556, 556, 556, 556, 556, 556, 556, 556, 556, 556, 278, 278, 584, 584, 584, 556,
	1015, 667, 667, 722, 722, 667, 611, 778, 722, 278, 500, 667, 556, 833, 722, 778,
	667, 778, 722, 667, 611, 722, 667, 944, 667, 667, 611, 278, 278, 278, 469, 556,
	333, 556, 556, 500, 556, 556, 278, 556, 556, 222, 222, 500, 222, 833, 556, 556,
	556, 556, 333, 500, 278, 556, 500, 722, 500, 500, 500, 334, 260, 334, 584, 350,
	556, 350, 222, 556, 333, 1000, 556, 556, 333, 1000, 667, 333, 1000, 350, 611, 350,
	350, 222, 222, 333, 333, 350, 556, 1000, 333, 1000, 500, 333, 944, 350, 500, 667,
	278, 333, 556, 556, 556, 556, 260, 556, 333, 737, 370, 556, 584, 333, 737, 333,
	400, 584, 333, 333, 333, 556, 537, 278, 333, 333, 365, 556, 834, 834, 834, 611,
	667, 667, 667, 667, 667, 667, 1000, 722, 667, 667, 667, 667, 278, 278, 278, 278,
	722, 722, 778, 778, 778, 778, 778, 584, 778, 722, 722, 722, 722, 667, 667, 611,
	556, 556, 556, 556, 556, 556, 889, 500, 556, 556, 556, 556, 278, 278, 278, 278,
	556, 556, 556, 556, 556, 556, 556, 584, 611, 556, 556, 556, 556, 500, 556, 500,
}
