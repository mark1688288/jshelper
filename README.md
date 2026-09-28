# jshelper

Private repository. Module path: `github.com/mark1688288/jshelper`.

```bash
go env -w GOPRIVATE=github.com/mark1688288/jshelper
git config url.git@github.com:.insteadOf https://github.com/
```

Do not commit `.env`, `credentials.json`, `token.json`, `/design`, an OAuth client secret, or a personal `GOOGLE_DRIVE_FOLDER_ID`.

`docs-cli` creates one Google Doc in a Drive folder you already have. There is one setup path. Decide `GCP_PROJECT_ID` before you create the OAuth client. That id is the Cloud project that owns the client and the enabled APIs. It is separate from the Drive folder. `GCP_FOLDER_ID` is an optional Cloud Resource Manager folder. `GOOGLE_DRIVE_FOLDER_ID` is the Drive folder, and `create` requires it.

The consent screen authorizes this app to see, edit, create, and delete all of your Google Drive files. The program only reads the one folder you name and creates one document there.

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

1. Decide `GCP_PROJECT_ID`. That id is the Cloud project that owns the OAuth client and the enabled APIs. It is separate from the Drive folder. `GCP_FOLDER_ID` is an optional Cloud Resource Manager folder. `GOOGLE_DRIVE_FOLDER_ID` is the Drive folder, and `create` requires it.

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

   Testing can grant this restricted scope to the listed test user. This version does not publish the consent screen, so CASA is not part of setup. The consent screen authorizes this app to see, edit, create, and delete all of your Google Drive files. The program only reads the one folder you name and creates one document there.

4. Put the client id and secret into the env file only after that Desktop client exists. Do not commit the secret or the folder id. `Load` rejects the placeholder email `you@gmail.com`.

   Development reads `.env.example` only. Put the client id, client secret, both email addresses, and the Drive folder id there, then:

   ```bash
   go run ./cmd/docs-cli auth
   go run ./cmd/docs-cli create --title "Demo" --text "hello" --folder <folder id or folder URL>
   ```

   Production runs `./docs-cli setup` again. `DOCS_CLI_PROJECT_READY` already matches the project id, so this round asks only for the client id and secret, writes `credentials.json` (mode `0600`), and does not listen on a port.

5. `docs-cli auth` performs loopback consent and stores the refresh token. It does not create the client id. After a scope change, run `docs-cli auth` again. A successful `auth` overwrites the keyring item `docs-cli` / `refresh-token`. The refresh token is not a file in the project directory. The client secret stays in `.env` and `credentials.json`. The first time on macOS, Keychain may ask for permission. Refusing that prompt exits with `docs-cli: keyring unavailable`.

   ```bash
   ./docs-cli auth
   ./docs-cli create --title "Demo" --text "hello"
   ```

Production `./docs-cli create` and `./docs-cli auth` use the same state machine as `setup`. When `DOCS_CLI_PROJECT_READY` is not the current `GCP_PROJECT_ID` and the first-round values are already valid, those commands run bootstrap and then exit 1 with the Desktop app instruction. They do not listen. The client id and secret are asked only when `DOCS_CLI_PROJECT_READY` equals the current `GCP_PROJECT_ID`. The round that writes the marker does not start a listener. Use setup, the Console steps, setup again, then auth.
