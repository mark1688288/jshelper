package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/mark1688288/jshelper/internal/config"
	"github.com/mark1688288/jshelper/internal/oauth"
)

// Help exits 2. This program does not follow the GNU convention of exit 0.
const usageText = `usage: docs-cli <command> [flags]

commands:
  create [--title TITLE] [--text TEXT] [--folder FOLDER]
  auth
  setup

create:
  --title TITLE    document title; omitted means DOCS_DEFAULT_TITLE
  --text TEXT      document body; if omitted, read stdin when it is not a terminal;
                   if omitted and stdin is a terminal, the body is empty
  --folder FOLDER  Drive folder id or folder URL for this run; overrides GOOGLE_DRIVE_FOLDER_ID

Run from the repository root, the directory that contains .env.example.
The release binary defaults to production. APP_ENV in the environment overrides it.
`

func main() {
	os.Exit(run(os.Args[1:], realDeps()))
}

type deps struct {
	Stdin     io.Reader
	Stdout    io.Writer
	Stderr    io.Writer
	Environ   []string
	Root      string
	TTY       config.TTY
	OpenTTY   func() (config.TTY, error)
	Authorize func(context.Context, *config.Config, oauth.Options) error
}

func realDeps() deps {
	return deps{
		Stdin:   os.Stdin,
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		Environ: os.Environ(),
		OpenTTY: config.OpenTTY,
	}
}

func run(args []string, d deps) int {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprint(d.Stderr, usageText)
		return 2
	}
	switch args[0] {
	case "create":
		return runCreate(args[1:], d)
	case "auth":
		return runAuth(args[1:], d)
	case "setup":
		return runSetup(args[1:], d)
	default:
		fmt.Fprint(d.Stderr, usageText)
		return 2
	}
}

func runCreate(args []string, d deps) int {
	fs := newFlagSet("create", d.Stderr)
	fs.String("title", "", "document title; omitted means DOCS_DEFAULT_TITLE")
	fs.String("text", "", "document body; if omitted, read stdin when it is not a terminal")
	fs.String("folder", "", "Drive folder id or folder URL for this run; overrides GOOGLE_DRIVE_FOLDER_ID")
	return parseOrUnimplemented(fs, args, d)
}

func runAuth(args []string, d deps) int {
	fs := newFlagSet("auth", d.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprint(d.Stderr, usageText)
		return 2
	}
	root, err := commandRoot(d)
	if err != nil {
		fmt.Fprintf(d.Stderr, "docs-cli: error: %s\n", err.Error())
		return 1
	}
	mode, err := config.ResolveMode(d.Environ)
	if err != nil {
		fmt.Fprintln(d.Stderr, err.Error())
		return 1
	}
	envPath := filepath.Join(root, ".env")
	_, statErr := os.Lstat(envPath)
	existed := statErr == nil
	var cfg *config.Config
	if mode == "production" {
		tty, closeTTY := openCommandTTY(d)
		defer closeTTY()
		cfg, err = config.EnsureProductionEnv(root, d.Environ, tty)
		if _, afterErr := os.Lstat(envPath); !existed && afterErr == nil {
			fmt.Fprintln(d.Stderr, "docs-cli: copied .env.example to .env")
		}
	} else {
		cfg, err = config.Load(root, d.Environ)
	}
	if err != nil {
		fmt.Fprintln(d.Stderr, err.Error())
		return 1
	}
	fmt.Fprintf(d.Stderr, "docs-cli: mode=%s file=%s\n", cfg.Mode, filepath.Base(cfg.FilePath))
	if err := config.RequireClient(cfg); err != nil {
		fmt.Fprintln(d.Stderr, err.Error())
		return 1
	}
	if err := config.WriteCredentials(cfg); err != nil {
		fmt.Fprintln(d.Stderr, err.Error())
		return 1
	}
	fmt.Fprintln(d.Stderr, "docs-cli: wrote credentials.json")
	authorize := d.Authorize
	if authorize == nil {
		authorize = func(ctx context.Context, cfg *config.Config, opt oauth.Options) error {
			_, err := oauth.Authorize(ctx, cfg, opt)
			return err
		}
	}
	if err := authorize(context.Background(), cfg, oauth.Options{Force: true, Stderr: d.Stderr}); err != nil {
		fmt.Fprintln(d.Stderr, err.Error())
		return 1
	}
	return 0
}

func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, usageText)
	}
	return fs
}

func parseOrUnimplemented(fs *flag.FlagSet, args []string, d deps) int {
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprint(d.Stderr, usageText)
		return 2
	}
	// Return before any env write, listener, or Google call.
	fmt.Fprintln(d.Stderr, "docs-cli: not implemented")
	return 1
}

func runSetup(args []string, d deps) int {
	fs := newFlagSet("setup", d.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprint(d.Stderr, usageText)
		return 2
	}
	root, err := commandRoot(d)
	if err != nil {
		fmt.Fprintf(d.Stderr, "docs-cli: error: %s\n", err.Error())
		return 1
	}
	tty, closeTTY := openCommandTTY(d)
	defer closeTTY()
	envPath := filepath.Join(root, ".env")
	_, statErr := os.Lstat(envPath)
	existed := statErr == nil
	cfg, err := config.EnsureProductionEnv(root, d.Environ, tty)
	if _, afterErr := os.Lstat(envPath); !existed && afterErr == nil {
		fmt.Fprintln(d.Stderr, "docs-cli: copied .env.example to .env")
	}
	if err != nil {
		fmt.Fprintln(d.Stderr, err.Error())
		return 1
	}
	fmt.Fprintf(d.Stderr, "docs-cli: mode=%s file=%s\n", cfg.Mode, filepath.Base(cfg.FilePath))
	if err := config.RequireClient(cfg); err != nil {
		fmt.Fprintln(d.Stderr, err.Error())
		return 1
	}
	if err := config.WriteCredentials(cfg); err != nil {
		fmt.Fprintln(d.Stderr, err.Error())
		return 1
	}
	fmt.Fprintln(d.Stderr, "docs-cli: wrote credentials.json")
	return 0
}

func commandRoot(d deps) (string, error) {
	if d.Root != "" {
		return d.Root, nil
	}
	return os.Getwd()
}

func openCommandTTY(d deps) (config.TTY, func()) {
	if d.TTY != nil {
		return d.TTY, func() {}
	}
	if d.OpenTTY == nil {
		return nil, func() {}
	}
	opened, err := d.OpenTTY()
	if err != nil {
		return nil, func() {}
	}
	return opened, func() {
		if c, ok := opened.(io.Closer); ok {
			_ = c.Close()
		}
	}
}
