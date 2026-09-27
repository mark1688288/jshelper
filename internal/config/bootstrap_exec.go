package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

var runProjectBootstrap = execProjectBootstrap

// SetProjectBootstrapForTest replaces the project-create step.
// nil restores execProjectBootstrap. Tests must not pass nil if that
// would run the real script; they install their own fake instead.
func SetProjectBootstrapForTest(fn func(root string, environ []string, stderr io.Writer) error) {
	if fn == nil {
		runProjectBootstrap = execProjectBootstrap
		return
	}
	runProjectBootstrap = fn
}

func projectBootstrapCmd(root string, environ []string) *exec.Cmd {
	cmd := exec.Command("bash", "config/bootstrap.sh")
	cmd.Dir = root
	cmd.Env = bootstrapEnviron(environ)
	return cmd
}

func execProjectBootstrap(root string, environ []string, stderr io.Writer) error {
	if stderr == nil {
		stderr = os.Stderr
	}
	cmd := projectBootstrapCmd(root, environ)
	cmd.Stdout = stderr
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return fmt.Errorf("docs-cli: bootstrap failed: exit status %d", exitErr.ExitCode())
		}
		return fmt.Errorf("docs-cli: bootstrap failed: %w", err)
	}
	return nil
}

func bootstrapEnviron(parent []string) []string {
	out := make([]string, 0, len(parent)+1)
	for _, entry := range parent {
		if strings.HasPrefix(entry, "APP_ENV=") {
			continue
		}
		out = append(out, entry)
	}
	return append(out, "APP_ENV=production")
}
