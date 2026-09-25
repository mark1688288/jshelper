package main

import (
	"flag"
	"fmt"
	"io"
	"os"
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
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

func realDeps() deps {
	return deps{
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
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
	case "auth", "setup":
		return runBare(args[0], args[1:], d)
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

func runBare(name string, args []string, d deps) int {
	fs := newFlagSet(name, d.Stderr)
	return parseOrUnimplemented(fs, args, d)
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
