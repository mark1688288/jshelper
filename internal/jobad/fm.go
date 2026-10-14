package jobad

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os/exec"
	"strings"

	"github.com/zalando/go-keyring"
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
// When either fm command exits non-zero and a Gemini API key is already stored,
// Gemini writes the Analysis from GeminiJD. A successful fm run does not read that key.
// A missing, unreadable, or invalid key keeps the fm error and does not call Gemini.
// An empty requirements list returns ErrRequirementsEmpty and does not run either model.
// client nil uses the default Gemini HTTP client. Tests pass a client that does not call Gemini.
func Analyze(doc Document, client *http.Client) (Analysis, error) {
	text, err := PromptText(doc)
	if err != nil {
		return Analysis{}, err
	}
	if _, err := callFM([]string{fmPath, "available"}); err != nil {
		return analyzeWithGemini(doc, client, err)
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
		return analyzeWithGemini(doc, client, err)
	}
	return ParseAnalysis(stdout)
}

// AnalyzeGemini asks Gemini for an Analysis and does not call fm.
// The user message is GeminiJD. The Gemini API key must already be stored.
// A missing key is errGeminiKeyMissing. AnalyzeGemini does not prompt and
// does not write the keyring. An empty requirements list returns
// ErrRequirementsEmpty and does not read the key. A Gemini failure is not
// retried with fm. client nil uses the default Gemini HTTP client.
// Tests pass a client that does not call Gemini.
func AnalyzeGemini(doc Document, client *http.Client) (Analysis, error) {
	text, err := GeminiJD(doc)
	if err != nil {
		return Analysis{}, err
	}
	key, err := LoadGeminiKey()
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return Analysis{}, errGeminiKeyMissing
		}
		return Analysis{}, err
	}
	return requestAnalysis(context.Background(), client, key, text)
}

func analyzeWithGemini(doc Document, client *http.Client, fmErr error) (Analysis, error) {
	text, err := GeminiJD(doc)
	if err != nil {
		return Analysis{}, err
	}
	key, err := LoadGeminiKey()
	if err != nil {
		return Analysis{}, fmErr
	}
	return requestAnalysis(context.Background(), client, key, text)
}

func callFM(argv []string) ([]byte, error) {
	stdout, stderr, code := fmExec(argv)
	if code != 0 {
		return nil, newFMError(stderr)
	}
	return stdout, nil
}
