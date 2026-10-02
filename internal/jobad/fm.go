package jobad

import (
	"bytes"
	"errors"
	"os/exec"
	"strings"
)

// fmPath is the on-device Foundation Models CLI.
// Analyze does not start `fm serve` and does not shell out.
const fmPath = "/usr/bin/fm"

// FMExec runs one fm invocation. argv[0] is the executable.
// A test replacement must not start a process.
type FMExec func(argv []string) (stdout []byte, stderr []byte, code int)

var fmExec FMExec = runFMProcess

// SetFMExecForTest replaces fm execution.
// nil restores /usr/bin/fm. Tests install a fake that does not start a process.
func SetFMExecForTest(fn FMExec) {
	if fn == nil {
		fmExec = runFMProcess
		return
	}
	fmExec = fn
}

func runFMProcess(argv []string) (stdout []byte, stderr []byte, code int) {
	if len(argv) == 0 {
		return nil, []byte("missing argv"), 1
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	if err == nil {
		return outBuf.Bytes(), errBuf.Bytes(), 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return outBuf.Bytes(), errBuf.Bytes(), exitErr.ExitCode()
	}
	msg := errBuf.String()
	if msg != "" && !strings.HasSuffix(msg, "\n") {
		msg += "\n"
	}
	msg += err.Error()
	return outBuf.Bytes(), []byte(msg), 1
}

type fmError struct {
	text string
}

func (e *fmError) Error() string {
	return "docs-cli: fm: " + e.text
}

func newFMError(stderr []byte) error {
	return &fmError{text: strings.TrimRight(string(stderr), "\n")}
}

// Analyze runs `fm available`, then `fm respond`, and parses stdout as Analysis.
// It does not read files, the environment, or the Gemini API key.
// An empty requirements list returns ErrRequirementsEmpty and does not run fm.
func Analyze(doc Document) (Analysis, error) {
	text, err := PromptText(doc)
	if err != nil {
		return Analysis{}, err
	}
	if _, err := callFM([]string{fmPath, "available"}); err != nil {
		return Analysis{}, err
	}
	stdout, err := callFM([]string{
		fmPath,
		"respond",
		"--no-stream",
		"--use-case", "general",
		"--instructions", AnalysisInstructions,
		"--schema", AnalysisSchema,
		"--text", text,
	})
	if err != nil {
		return Analysis{}, err
	}
	return ParseAnalysis(stdout)
}

func callFM(argv []string) ([]byte, error) {
	stdout, stderr, code := fmExec(argv)
	if code != 0 {
		return nil, newFMError(stderr)
	}
	return stdout, nil
}
