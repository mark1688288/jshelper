# jshelper

Private repository. Module path: `github.com/mark1688288/jshelper`.

```bash
go env -w GOPRIVATE=github.com/mark1688288/jshelper
git config url.git@github.com:.insteadOf https://github.com/
```

Do not commit `.env`, `credentials.json`, `token.json`, `/design`, an OAuth client secret, or a personal `GOOGLE_DRIVE_FOLDER_ID`.

`docs-cli` creates one Google Doc in a Drive folder you already have. There is one setup path. Decide `GCP_PROJECT_ID` before you create the OAuth client. That id is the Cloud project that owns the client and the enabled APIs. It is separate from the Drive folder. `GCP_FOLDER_ID` is an optional Cloud Resource Manager folder. `GOOGLE_DRIVE_FOLDER_ID` is the Drive folder, and `create` requires it.

`upload` copies one or more `.json` files into that same folder. Each file is stored as `application/json`.

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

```bash
./docs-cli setup
```

The first round copies `.env` when it is missing, asks `GCP_PROJECT_ID`, `GCP_PROJECT_NAME`, `GOOGLE_OAUTH_SUPPORT_EMAIL`, `GOOGLE_OAUTH_TEST_USER`, and `GOOGLE_DRIVE_FOLDER_ID`, creates the Cloud project when it is missing, and enables the Docs and Drive APIs.

In that same project, finish these Console pages by hand. Create a Desktop client. Leave Web and IAP clients unused.

- Branding: `https://console.cloud.google.com/auth/branding?project=<GCP_PROJECT_ID>`
- Audience: External, Testing, and your test user. `https://console.cloud.google.com/auth/audience?project=<GCP_PROJECT_ID>`
- Data Access: add only `https://www.googleapis.com/auth/drive`. `https://console.cloud.google.com/auth/scopes?project=<GCP_PROJECT_ID>`
- Clients: Desktop app named `Docs CLI Desktop`. `https://console.cloud.google.com/auth/clients?project=<GCP_PROJECT_ID>`

> [!NOTE]
> Stay in Testing unless you are in a Google Workspace and can complete Google verification for this app.

```bash
./docs-cli setup
./docs-cli auth
```

The second `setup` asks for the client id and secret. `auth` stores the refresh token in the keyring. Sign in as the listed test user. The consent screen stays in Testing; an unlisted account gets `403: access_denied`.

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

That keyring item is the user grant. `credentials.json` is a different file: the Google installed Desktop client (`client_id`, `client_secret`, token URLs), derived from `.env`. Token exchange and refresh read the client id and secret from the env file. `setup`, `auth`, and `create` rewrite `credentials.json` (mode `0600`) as that installed-app copy. It is gitignored. The client secret stays in `.env` and `credentials.json`; it does not go in the keyring.

On macOS, go-keyring writes to Keychain. Keychain encrypts the item at rest and unlocks it with your login. The first `auth` may ask for permission. The application does not add its own encryption.

## Build

`go run` and `go test` stay in development. They read `.env.example` and do not copy it or ask questions.

```bash
go run ./cmd/docs-cli create --title "Demo" --text "hello"
```

The release binary defaults to production when it is built with this link flag. Run it from the repository root, the directory that contains `.env.example`. A non-empty `APP_ENV` in the process environment overrides the compiled default.

```bash
go build -o docs-cli -ldflags "-X github.com/mark1688288/jshelper/internal/config.defaultAppEnv=production" ./cmd/docs-cli
```

## Setup

There is one Console order. The client id exists only after a Desktop client is created in that Cloud project. Browser consent does not create the client id.

1. Decide `GCP_PROJECT_ID`. That id is the Cloud project that owns the OAuth client and the enabled APIs. It is separate from the Drive folder. `GCP_FOLDER_ID` is an optional Cloud Resource Manager folder. `GOOGLE_DRIVE_FOLDER_ID` is the Drive folder, and `create` and `upload` require it.

2. Create the Cloud project when it is missing and enable `docs.googleapis.com` and `drive.googleapis.com`. You need a logged-in `gcloud`. Bootstrap does not create a Drive folder and does not move an OAuth client.

   Development, from the repository root:

   ```bash
   APP_ENV=development bash config/bootstrap.sh --check
   bash config/bootstrap.sh
   ```

   `bash config/bootstrap.sh` reads `.env.example`. An empty OAuth client is expected: the script prints the Console URLs and exits 0.

   Production uses the release binary. From the repository root, run `./docs-cli setup`. If `.env` is missing, it copies `.env.example` to `.env` (mode `0600`) and does not replace an `.env` that already exists. The first round asks only `GCP_PROJECT_ID`, `GCP_PROJECT_NAME`, `GOOGLE_OAUTH_SUPPORT_EMAIL`, `GOOGLE_OAUTH_TEST_USER`, and `GOOGLE_DRIVE_FOLDER_ID`. Press Enter on the project id and name to keep the current value. A new project id clears the client id, the client secret, and the project-ready marker; the Desktop client stays in the old project. After those values are valid, setup runs `APP_ENV=production bash config/bootstrap.sh`, writes `DOCS_CLI_PROJECT_READY` equal to `GCP_PROJECT_ID`, and exits 0. That round does not ask for a client id or secret and does not open a browser.

3. In that same project, finish these Console steps by hand. Create a Desktop client. Leave Web and IAP clients unused.

   - Branding: `https://console.cloud.google.com/auth/branding?project=<GCP_PROJECT_ID>`
   - Audience: External, Testing, and your test user. `https://console.cloud.google.com/auth/audience?project=<GCP_PROJECT_ID>`
   - Data Access: add only `https://www.googleapis.com/auth/drive`. `https://console.cloud.google.com/auth/scopes?project=<GCP_PROJECT_ID>`
   - Clients: Desktop app named `Docs CLI Desktop`. `https://console.cloud.google.com/auth/clients?project=<GCP_PROJECT_ID>`

   Testing can grant this restricted scope to the listed test user. This version does not publish the consent screen, so CASA is not part of setup. The consent screen authorizes this app to see, edit, create, and delete all of your Google Drive files. The program only reads the one folder you name. `create` adds one document there. `upload` adds the JSON files you name.

4. Put the client id and secret into the env file only after that Desktop client exists. Do not commit the secret or the folder id. `Load` rejects the placeholder email `you@gmail.com`.

   Development reads `.env.example` only. Put the client id, client secret, both email addresses, and the Drive folder id there, then:

   ```bash
   go run ./cmd/docs-cli auth
   go run ./cmd/docs-cli create --title "Demo" --text "hello" --folder <folder id or folder URL>
   go run ./cmd/docs-cli upload ./tmp/1.json ./tmp/2.json
   ```

   Production runs `./docs-cli setup` again. `DOCS_CLI_PROJECT_READY` already matches the project id, so this round asks only for the client id and secret, writes `credentials.json` (mode `0600`), and does not listen on a port.

5. `docs-cli auth` performs loopback consent and stores the refresh token. It does not create the client id. After a scope change, run `docs-cli auth` again. A successful `auth` overwrites the keyring item `docs-cli` / `refresh-token`. The refresh token is not a file in the project directory. The client secret stays in `.env` and `credentials.json`. The first time on macOS, Keychain may ask for permission. Refusing that prompt exits with `docs-cli: keyring unavailable`.

   ```bash
   ./docs-cli auth
   ./docs-cli create --title "Demo" --text "hello"
   ```

Production `./docs-cli create` and `./docs-cli auth` use the same state machine as `setup`. When `DOCS_CLI_PROJECT_READY` is not the current `GCP_PROJECT_ID` and the first-round values are already valid, those commands run bootstrap and then exit 1 with the Desktop app instruction. They do not listen. The client id and secret are asked only when `DOCS_CLI_PROJECT_READY` equals the current `GCP_PROJECT_ID`. The round that writes the marker does not start a listener. Use setup, the Console steps, setup again, then auth.

## Job ads

`parse` reads a saved job HTML file and writes section JSON to stdout. A JobsDB page still uses its job-detail fields. Another page uses the first schema.org JobPosting, or the page's paragraphs when that object is absent. When that page has no requirements, and a hidden `initialHistory` value holds the job HTML, `parse` uses that HTML. A `Post:` line fills the title. A heading that contains the word requirements fills the requirements list. It does not read `.env` and it does not use the refresh token.

```bash
docs-cli parse --html PATH [--autoparse]
docs-cli parse --txt PATH
```

`gemini-key` stores a Gemini API key in the keyring item `docs-cli` / `gemini-api-key`. The key is typed at the terminal and is not echoed. It is not a flag, not an environment variable, and it is not written to `.env` or `credentials.json`. `create`, `auth`, and `setup` do not read or write that item.

```bash
docs-cli gemini-key
```

On a terminal, `parse` asks for the key when that item is missing, then stores it. Without a key, `parse` keeps the local heading classification and does not call Gemini. When a key is stored, `parse` sends only the extracted plain text, with email addresses removed. It does not send the HTML page. `parse --autoparse` sends that saved HTML to Gemini and writes the same section JSON. Email addresses are removed before the page is sent. Without `--autoparse`, `parse` does not send the HTML page. `parse --txt PATH` reads a text file of the job details and sends that text to Gemini. Email addresses are removed before the text is sent. It does not need `--autoparse`. `go test` does not call Gemini.

`analyze` reads that section JSON and writes the requirement JSON to stdout. It uses the on-device model when that model is available. If the on-device model is unavailable and a Gemini API key is already stored, Gemini writes the same analysis JSON. It does not read `.env`. `go test` does not run `/usr/bin/fm` and does not call Gemini.

```bash
docs-cli analyze --json PATH
```

An empty requirements list exits 1 with `docs-cli: requirements is empty` and does not run `fm` or Gemini.

`draft` reads a cover-letter template PDF and the section JSON from `parse`, then writes a letter with the on-device model. The template is a pattern for structure and voice. It also reads the CV or resume PDF named by `--cv`. The on-device model selects the skills, projects, and achievements in that CV that match the job, and drafts the letter from those facts. The letter covers that experience, interest in the employer and fit for the role, and a polite close with a call to action. The CV is not sent to Gemini. The on-device model drafts the whole letter from the job JSON and the template's structure and voice without copying square-bracket placeholders, and the program does not fill those placeholders afterward. The letter keeps the template's breaks: a blank line stays a blank line, and a single newline stays a single newline. The program trims trailing spaces and drops a line that is only a square-bracket placeholder. It needs macOS, Apple Intelligence, and `/usr/bin/fm`. A privileged user must already have run `sudo fm license` on that Mac. The letter is saved in a `drafts` directory in the working directory. Run from the repository root and that directory is `./drafts`. That directory is created if it does not exist. On success, stdout is the absolute path of the letter file. It does not write a Google Doc. Without --pdf it writes a timestamped .txt file in drafts. With --pdf NAME it writes that file name in drafts as a PDF instead, and NAME must end in .pdf. `go test` does not run `/usr/bin/fm` and does not read a real cover-letter template or a real CV.

```bash
docs-cli draft --json PATH --template PATH --cv PATH
docs-cli draft --json PATH --template PATH --cv PATH --pdf NAME.pdf
```

## Credits

Refresh tokens are stored with [zalando/go-keyring](https://github.com/zalando/go-keyring).

https://github.com/zalando/go-keyring
