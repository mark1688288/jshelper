# jshelper

Private repository. Module path: `github.com/mark1688288/jshelper`.

```bash
go env -w GOPRIVATE=github.com/mark1688288/jshelper
git config url.git@github.com:.insteadOf https://github.com/
```

Do not commit `.env`, `credentials.json`, `token.json`, `/design`, an OAuth client secret, or a personal `GOOGLE_DRIVE_FOLDER_ID`.

`docs-cli` creates one Google Doc in a Drive folder you already have. There is one setup path. Decide `GCP_PROJECT_ID` before you create the OAuth client. That id is the Cloud project that owns the client and the enabled APIs. It is separate from the Drive folder. `GCP_FOLDER_ID` is an optional Cloud Resource Manager folder. `GOOGLE_DRIVE_FOLDER_ID` is the Drive folder, and `create` requires it.

`upload` copies one or more `.json` files into that same folder. Each file is stored as `application/json`.

With --import PATH, create converts that PDF into the Google Doc and puts it in the same folder. The document title is --title, or DOCS_DEFAULT_TITLE when --title is omitted.

The consent screen authorizes this app to see, edit, create, and delete all of your Google Drive files. The program only reads the one folder you name. `create` adds one document there. `upload` adds the JSON files you name.

## Prerequisites

- macOS 27 or later
- Apple silicon, M1 or later
- Go 1.27.1
- Google Cloud CLI (`gcloud`), logged in
- A Google account for the support email and the Testing test user
- An existing Google Drive folder; `create` and `upload` need `GOOGLE_DRIVE_FOLDER_ID`
- macOS Keychain for the refresh token; the first `auth` may prompt
- `fm` (`/usr/bin/fm`), with Apple Intelligence turned on

The full name of `fm` is the Apple Foundation Models command-line interface. It is preinstalled at `/usr/bin/fm` with macOS 27. Apple Foundation Models is the on-device language model behind Apple Intelligence, on a Mac with M1 or later.

## Quick Start

From the repository root, the directory that contains `.env.example`. Do not export `APP_ENV`.

```bash
go build -o docs-cli -ldflags "-X github.com/mark1688288/jshelper/internal/config.defaultAppEnv=production" ./cmd/docs-cli
```

### Run wizard to setup

```bash
./docs-cli setup
```

The first round copies `.env` when it is missing, asks `GCP_PROJECT_ID`, `GCP_PROJECT_NAME`, `GOOGLE_OAUTH_SUPPORT_EMAIL`, `GOOGLE_OAUTH_TEST_USER`, and `GOOGLE_DRIVE_FOLDER_ID`, creates the Cloud project when it is missing, and enables the Docs and Drive APIs.

In that same project, finish these Console pages by hand. Create a Desktop client. Leave Web and IAP clients unused.

Following urls will provided:

```bash
- Branding: `https://console.cloud.google.com/auth/branding?project=<GCP_PROJECT_ID>`
- Audience: External, Testing, and your test user. `https://console.cloud.google.com/auth/audience?project=<GCP_PROJECT_ID>`
- Data Access: add only `https://www.googleapis.com/auth/drive`. `https://console.cloud.google.com/auth/scopes?project=<GCP_PROJECT_ID>`
- Clients: Desktop app named `Docs CLI Desktop`. `https://console.cloud.google.com/auth/clients?project=<GCP_PROJECT_ID>`
```

> [!NOTE]
> Stay in Testing unless you are in a Google Workspace and can complete Google verification for this app.

```bash
./docs-cli setup
./docs-cli auth
```

The second `setup` asks for the client id and secret. `auth` stores the refresh token in the keyring. Sign in as the listed test user. The consent screen stays in Testing; an unlisted account gets `403: access_denied`.

Test connection of Google Drive by creating a Google Docs:

```bash
./docs-cli create --title "Demo" --text "hello"
```

## Security

The OAuth consent screen stays in Testing. Google expires that refresh token after about 7 days. When `create` fails with `invalid_grant`, run:

```bash
./docs-cli auth
```

The program does not open a browser on its own. A successful `auth` overwrites the stored token.

`docs-cli` stores the refresh token with [zalando/go-keyring](https://github.com/zalando/go-keyring) as Keychain item `docs-cli` / `refresh-token`. The payload is JSON with `refresh_token` and `scope`. Access tokens stay in memory. The refresh token is not a file in the project directory, so it is not committed.

That keyring item is the user grant. `credentials.json` is a different file: the Google installed Desktop client (`client_id`, `client_secret`, token URLs), derived from `.env`. Token exchange and refresh read the client id and secret from the env file. `setup` and `auth` rewrite `credentials.json` (mode `0600`) as that installed-app copy. `create` and `upload` do not write that file. It is gitignored. The client secret stays in `.env` and `credentials.json`; it does not go in the keyring.

On macOS, go-keyring writes to Keychain. Keychain encrypts the item at rest and unlocks it with your login. The first `auth` may ask for permission. The application does not add its own encryption.

## Build

`go run` and `go test` stay in development. They read `.env.example` and do not copy it or ask questions.

```bash
go run ./cmd/docs-cli create --title "Demo" --text "hello"
```

```bash
go run ./cmd/docs-cli create --title "MY Cover Letter" --import ./drafts/customname.pdf
```

The release binary defaults to production when it is built with this link flag. Run it from the repository root, the directory that contains `.env.example`. A non-empty `APP_ENV` in the process environment overrides the compiled default.

```bash
go build -o docs-cli -ldflags "-X github.com/mark1688288/jshelper/internal/config.defaultAppEnv=production" ./cmd/docs-cli
```

## Parse

`parse` reads a saved job HTML file and writes section JSON to stdout. A JobsDB page still uses its job-detail fields. Another page uses the first schema.org JobPosting, or the page's paragraphs when that object is absent. When that page has no requirements, and a hidden `initialHistory` value holds the job HTML, `parse` uses that HTML. A `Post:` line fills the title. A heading that contains the word requirements fills the requirements list. It does not use the refresh token. When parse calls Gemini, the model id is `GEMINI_MODEL` from `.env` in the working directory. A missing or empty value uses the built-in model id. The API key is not read from `.env`.

```bash
docs-cli parse --html PATH [--autoparse]
docs-cli parse --txt PATH
docs-cli parse --batch
```

`gemini-key` stores a Gemini API key in the keyring item `docs-cli` / `gemini-api-key`. The key is typed at the terminal and is not echoed. It is not a flag, not an environment variable, and it is not written to `.env` or `credentials.json`. `create`, `auth`, and `setup` do not read or write that item.

```bash
docs-cli gemini-key
```

On a terminal, `parse` asks for the key when that item is missing, then stores it. Without a key, `parse` keeps the local heading classification and does not call Gemini. When a key is stored, `parse` sends only the extracted plain text, with email addresses removed. It does not send the HTML page. `parse --autoparse` sends that saved HTML to Gemini and writes the same section JSON. Email addresses are removed before the page is sent. Without `--autoparse`, `parse` does not send the HTML page. `parse --txt PATH` reads a text file of the job details and sends that text to Gemini. Email addresses are removed before the text is sent. It does not need `--autoparse`. `parse --batch` reads every .txt file in ./batch/jobads, one or more, and sends each file to Gemini. It writes the section JSON in ./batch/jobads/parsed and moves that .txt file there. It creates ./batch, ./batch/jobads, and ./batch/jobads/parsed when they are missing. `go test` does not call Gemini.

### Json Schema

```json
{
  "source": { "type": "string" },
  "header": {
    "title_block": { "type": "string" },
    "company": { "type": "string" },
    "location": { "type": "string" },
    "work_type": { "type": "string" },
    "classification": { "type": "string" }
  },
  "benefits": ["type": "array", "items": { "type": "string" } ],
  "duties": ["type": "array", "items": { "type": "string" } ],
  "requirements": ["type": "array", "items": { "type": "string" } ],
  "trailer": ["type": "array", "items": { "type": "string" } ]
}
```

## Analyze

`analyze` reads that section JSON and writes the requirement JSON to stdout. It supports one file, `analyze --json PATH`, or a batch, `analyze --batch`. By default it calls Apple `fm`. If `fm` is unavailable and a Gemini API key is already stored, it falls back to Gemini. `--gemini` bypasses the `fm` call.

`analyze --batch` reads every `.json` file in `./batch/jobads/parsed`, moves that file into `./batch/jobads/analyzed`, and writes the analysis files there. `parsed` must already exist. `analyzed` is created when it is missing.

```bash
docs-cli analyze --json PATH [--gemini]
docs-cli analyze --batch [--gemini]
```

> [!TIPS] 
> Saves file to speicified location by 
> ```bash
> ./docs-cli analyze --json PATH > PATH
> ```

#### Folders structure of batch mode:

```bash
./batch/jobads/parsed/
./batch/jobads/analyzed/name.json
./batch/jobads/analyzed/name-analysis.json
./batch/jobads/analyzed/name-tech.md
./batch/jobads/analyzed/name-tech-dist.md
```

An empty requirements list exits 1 with `docs-cli: requirements is empty` and does not run `fm` or Gemini.

### Json Schema

```json
{
  "type": "object",
  "properties": {
    "must_have": { "type": "array", "items": { "type": "string" } },
    "advantage": { "type": "array", "items": { "type": "string" } },
    "min_years": { "type": ["integer", "null"] },
    "degree": { "type": ["string", "null"] },
    "languages": { "type": "array", "items": { "type": "string" } },
    "tech": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "name": { "type": "string" },
          "level": { "type": "string" },
          "applies_to": { "type": ["string", "null"] }
        },
        "required": ["name", "level", "applies_to"]
      }
    },
    "soft_skills": { "type": "array", "items": { "type": "string" } }
  },
  "required": ["must_have", "advantage", "min_years", "degree", "languages", "tech", "soft_skills"]
}
```

## Draft

`draft` reads a cover-letter template PDF and the section JSON from `parse`, then writes a letter with the on-device model. The template is a pattern for structure and voice. It also reads the CV or resume PDF named by `--cv`. The on-device model selects the skills, projects, and achievements in that CV that match the job, and drafts the letter from those facts. The letter covers that experience, interest in the employer and fit for the role, and a polite close with a call to action. The CV is not sent to Gemini. The on-device model drafts the whole letter from the job JSON and the template's structure and voice without copying square-bracket placeholders, and the program does not fill those placeholders afterward. The letter keeps the template's breaks: a blank line stays a blank line, and a single newline stays a single newline. The program trims trailing spaces and drops a line that is only a square-bracket placeholder. It needs macOS, Apple Intelligence, and `/usr/bin/fm`. A privileged user must already have run `sudo fm license` on that Mac. The letter is saved in a `drafts` directory in the working directory. Run from the repository root and that directory is `./drafts`. That directory is created if it does not exist. On success, stdout is the absolute path of the letter file. It does not write a Google Doc. Without --pdf it writes a timestamped .txt file in drafts. With --pdf NAME it writes that file name in drafts as a PDF instead, and NAME must end in .pdf. `go test` does not run `/usr/bin/fm` and does not read a real cover-letter template or a real CV.

```bash
docs-cli draft --json PATH --template PATH --cv PATH
docs-cli draft --json PATH --template PATH --cv PATH --pdf NAME.pdf
```

## Google Docs

```bash
docs-cli create -title "MY Cover Letter" --import ./drafts/cover_letter.pdf
```

## Google Drive

```bash
docs-cli upload parsed.json analysis.json
```


## License

Copyright (C) 2026 mark1688288.

This program is free software under the GNU General Public License version 3 only. See [LICENSE](LICENSE).

If you distribute this program, or a modified version of it, including inside a commercial product, you must license that work under GPL-3.0 and make the corresponding source available. Using it yourself, without distributing it, does not require you to publish your changes.

## Credits

Refresh tokens are stored with [zalando/go-keyring](https://github.com/zalando/go-keyring). Copyright (c) 2016 Zalando SE.

Template and CV PDFs are read with [ledongthuc/pdf](https://github.com/ledongthuc/pdf). Copyright (c) 2009 The Go Authors.

Job-page HTML is parsed with [golang.org/x/net/html](https://pkg.go.dev/golang.org/x/net/html). Copyright 2009 The Go Authors.

The OAuth loopback uses [golang.org/x/oauth2](https://pkg.go.dev/golang.org/x/oauth2). Copyright 2009 The Go Authors.

Terminal checks and hidden secret input use [golang.org/x/term](https://pkg.go.dev/golang.org/x/term). Copyright 2009 The Go Authors.

Google Docs and Drive clients come from [google.golang.org/api](https://pkg.go.dev/google.golang.org/api). Copyright (c) 2011 Google Inc.
