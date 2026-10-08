package main

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/ledongthuc/pdf"
)

func TestLetterPDFSample(t *testing.T) {
	body, err := buildLetterPDF("Dear team,\n\nI write to apply.\n")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(body, []byte("%PDF-1.4\n")) {
		t.Fatalf("header %q", body[:16])
	}
	if !bytes.Contains(body, []byte("/BaseFont /Helvetica")) || !bytes.Contains(body, []byte("/Encoding /WinAnsiEncoding")) {
		t.Fatal("font is not Helvetica WinAnsi")
	}
	if !bytes.Contains(body, []byte("/MediaBox [0 0 595.28 841.89]")) {
		t.Fatal("media box")
	}
	want := []byte("(Dear team,) Tj\nT*\nT*\n(I write to apply.) Tj\n")
	if !bytes.Contains(body, want) {
		t.Fatalf("stream missing %q", want)
	}
	if pdfPageCount(t, body) != 1 {
		t.Fatal("page count")
	}
	text := pdfPageText(t, body, 1)
	if !strings.Contains(text, "Dear team,") || !strings.Contains(text, "I write to apply.") {
		t.Fatalf("text %q", text)
	}
	r := openLetterPDF(t, body)
	glyph := r.Page(1).Content().Text[0]
	if glyph.X != 72 || glyph.Y < 758.88 || glyph.Y > 758.90 || glyph.FontSize != 11 {
		t.Fatalf("glyph %+v", glyph)
	}
}

func TestLetterPDFEscapesParentheses(t *testing.T) {
	body, err := buildLetterPDF("See (Go) and a\\b.\n")
	if err != nil {
		t.Fatal(err)
	}
	want := []byte(`(See \(Go\) and a\\b.) Tj`)
	if !bytes.Contains(body, want) {
		t.Fatalf("stream missing %q\n%s", want, body)
	}
	text := pdfPageText(t, body, 1)
	if !strings.Contains(text, "See (Go) and a\\b.") {
		t.Fatalf("text %q", text)
	}
}

func TestLetterPDFPages(t *testing.T) {
	one, err := buildLetterPDF(strings.Repeat("Hi.\n", 43))
	if err != nil {
		t.Fatal(err)
	}
	if pdfPageCount(t, one) != 1 {
		t.Fatal("43 lines")
	}
	two, err := buildLetterPDF(strings.Repeat("Hi.\n", 44))
	if err != nil {
		t.Fatal(err)
	}
	if pdfPageCount(t, two) != 2 {
		t.Fatal("44 lines")
	}
	fifty, err := buildLetterPDF(strings.Repeat("Hi.\n", 50))
	if err != nil {
		t.Fatal(err)
	}
	if pdfPageCount(t, fifty) != 2 {
		t.Fatal("50 lines")
	}
}

func TestLetterPDFNewlines(t *testing.T) {
	cr, err := buildLetterPDF("a\r\nb\rc\n")
	if err != nil {
		t.Fatal(err)
	}
	lf, err := buildLetterPDF("a\nb\nc\n")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(cr, lf) {
		t.Fatal("crlf differs from lf")
	}
	trailing, err := buildLetterPDF("Hi\n")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(trailing, []byte("T*")) || !bytes.Contains(trailing, []byte("(Hi) Tj\n")) {
		t.Fatal("trailing newline became a blank line")
	}
	blank, err := buildLetterPDF("Hi\n\n")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(blank, []byte("T*")) != 1 {
		t.Fatalf("blank lines %d", bytes.Count(blank, []byte("T*")))
	}
}

func TestLetterPDFTemplateBullets(t *testing.T) {
	body, err := buildLetterPDF("● role — owner’s plan\n○ next\nﬁle\n")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte{0x95}) || !bytes.Contains(body, []byte{0x97}) || !bytes.Contains(body, []byte{0x92}) {
		t.Fatal("bullet, em dash, or apostrophe missing")
	}
	if !bytes.Contains(body, []byte{0xB7}) {
		t.Fatal("open circle missing")
	}
	text := pdfPageText(t, body, 1)
	if !strings.Contains(text, "file") || strings.Contains(text, "ﬁ") {
		t.Fatalf("ligature %q", text)
	}
}

func TestLetterPDFNotEncodable(t *testing.T) {
	for _, letter := range []string{"你好\n", "Hello 你好", "Hi 😀", "€10\n"} {
		body, err := buildLetterPDF(letter)
		if !errors.Is(err, errPDFNotEncodable) || body != nil {
			t.Fatalf("%q err %v body %d", letter, err, len(body))
		}
	}
}

func TestLetterPDFWinAnsiPunctuation(t *testing.T) {
	body, err := buildLetterPDF("‘quote’ “q” –—•…\u00a0café\n")
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range []byte{0x91, 0x92, 0x93, 0x94, 0x96, 0x97, 0x95, 0x85, 0xA0, 0xE9} {
		if !bytes.Contains(body, []byte{b}) {
			t.Fatalf("missing byte %02x", b)
		}
	}
	if pdfPageCount(t, body) != 1 {
		t.Fatal("page count")
	}
}

func TestLetterPDFWrap(t *testing.T) {
	wide, err := buildLetterPDF(strings.Repeat("W", 44) + "\n")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(wide, []byte(" Tj\n")) != 2 {
		t.Fatalf("character split count %d", bytes.Count(wide, []byte(" Tj\n")))
	}
	if !bytes.Contains(wide, []byte("("+strings.Repeat("W", 43)+") Tj\n")) || !bytes.Contains(wide, []byte("(W) Tj\n")) {
		t.Fatal("character split")
	}
	spaced := strings.Repeat("W", 43) + " tail\n"
	body, err := buildLetterPDF(spaced)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte("(" + strings.Repeat("W", 43) + ") Tj\nT*\n(tail) Tj\n")
	if !bytes.Contains(body, want) {
		t.Fatalf("space split missing %q", want)
	}
}

func TestValidPDFName(t *testing.T) {
	ok := []string{"customname.pdf", "My Letter.pdf", ".pdf", strings.Repeat("a", 251) + ".pdf"}
	for _, name := range ok {
		if !validPDFName(name) {
			t.Fatalf("want ok %q", name)
		}
	}
	bad := []string{
		"", ".", "..", "customname.txt", "Custom.PDF", "customname.Pdf",
		"../x.pdf", "/tmp/x.pdf", `a\b.pdf`, "customname.pdf ", " customname.pdf\n",
		strings.Repeat("a", 252) + ".pdf", "foo.pdf\x00",
	}
	for _, name := range bad {
		if validPDFName(name) {
			t.Fatalf("want reject %q", name)
		}
	}
}

func TestWriteDraftPDF(t *testing.T) {
	dir := t.TempDir()
	path, err := writeDraftPDF(dir, "customname.pdf", "Hi\n")
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v err %v", info, err)
	}
	kept, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = writeDraftPDF(dir, "customname.pdf", "Other\n")
	var exists *pdfExistsError
	if !errors.As(err, &exists) || exists.Error() != "docs-cli: pdf already exists: customname.pdf" {
		t.Fatalf("err %v", err)
	}
	again, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(again, kept) {
		t.Fatal("overwrote the pdf")
	}
	missing := t.TempDir()
	if _, err := writeDraftPDF(missing, "customname.pdf", "你好\n"); !errors.Is(err, errPDFNotEncodable) {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(missing)
	if err != nil || len(entries) != 0 {
		t.Fatalf("entries %#v err %v", entries, err)
	}
}

func TestHelveticaWidths(t *testing.T) {
	if helveticaAFM[' '] != 278 || helveticaAFM['A'] != 667 || helveticaAFM['a'] != 556 || helveticaAFM['i'] != 222 {
		t.Fatalf("ascii widths space %d A %d a %d i %d", helveticaAFM[' '], helveticaAFM['A'], helveticaAFM['a'], helveticaAFM['i'])
	}
	if helveticaAFM[0x95] != 350 || helveticaAFM[0x97] != 1000 || helveticaAFM[0xA0] != 278 {
		t.Fatalf("winansi bullet %d em %d nbsp %d", helveticaAFM[0x95], helveticaAFM[0x97], helveticaAFM[0xA0])
	}
	// 43 W fit. 44 do not. W is 944 units.
	if got := helveticaWidth(bytes.Repeat([]byte{'W'}, 43)); got > pdfMaxWidth {
		t.Fatalf("43 W = %d max %d", got, pdfMaxWidth)
	}
	if got := helveticaWidth(bytes.Repeat([]byte{'W'}, 44)); got <= pdfMaxWidth {
		t.Fatalf("44 W = %d max %d", got, pdfMaxWidth)
	}
}

func openLetterPDF(t *testing.T, body []byte) *pdf.Reader {
	t.Helper()
	r, err := pdf.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func pdfPageCount(t *testing.T, body []byte) int {
	t.Helper()
	return openLetterPDF(t, body).NumPage()
}

func pdfPageText(t *testing.T, body []byte, page int) string {
	t.Helper()
	var b strings.Builder
	for _, glyph := range openLetterPDF(t, body).Page(page).Content().Text {
		b.WriteString(glyph.S)
	}
	return b.String()
}
