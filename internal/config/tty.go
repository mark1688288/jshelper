package config

import (
	"errors"
	"io"
	"os"
	"runtime"
	"strings"

	"golang.org/x/term"
)

// TTY reads wizard answers from the terminal, not from stdin.
type TTY interface {
	IsTerminal() bool
	ReadLine(prompt string) (string, error)
	ReadSecret(prompt string) (string, error)
}

// ErrNeedTerminal is returned when production setup must ask questions
// and no terminal is available.
var ErrNeedTerminal = errors.New("docs-cli: production env is incomplete; run docs-cli setup in a terminal")

// OpenTTY opens /dev/tty for wizard prompts.
// Windows and a missing terminal return ErrNeedTerminal.
func OpenTTY() (TTY, error) {
	if runtime.GOOS == "windows" {
		return nil, ErrNeedTerminal
	}
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, ErrNeedTerminal
	}
	return &devTTY{f: f}, nil
}

type devTTY struct {
	f *os.File
}

func (t *devTTY) IsTerminal() bool {
	return term.IsTerminal(int(t.f.Fd()))
}

func (t *devTTY) ReadLine(prompt string) (string, error) {
	if _, err := io.WriteString(t.f, prompt); err != nil {
		return "", err
	}
	return readTTYLine(t.f)
}

func (t *devTTY) ReadSecret(prompt string) (string, error) {
	if _, err := io.WriteString(t.f, prompt); err != nil {
		return "", err
	}
	pw, err := term.ReadPassword(int(t.f.Fd()))
	_, _ = io.WriteString(t.f, "\n")
	if err != nil {
		return "", err
	}
	return string(pw), nil
}

func (t *devTTY) WriteStatus(line string) error {
	_, err := io.WriteString(t.f, line+"\n")
	return err
}

func (t *devTTY) Close() error {
	return t.f.Close()
}

func readTTYLine(f *os.File) (string, error) {
	var buf []byte
	one := make([]byte, 1)
	for {
		n, err := f.Read(one)
		if n == 1 {
			if one[0] == '\n' {
				return strings.TrimRight(string(buf), "\r"), nil
			}
			buf = append(buf, one[0])
			continue
		}
		if err != nil {
			if len(buf) > 0 && errors.Is(err, io.EOF) {
				return strings.TrimRight(string(buf), "\r"), nil
			}
			return "", err
		}
	}
}
