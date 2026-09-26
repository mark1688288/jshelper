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

1. If you are not using the example project `docs-cli-dev` and `Docs CLI`, edit `GCP_PROJECT_ID` and `GCP_PROJECT_NAME` in `.env.example` first. Bootstrap, the Cloud Console, and the Desktop client all use that id.

2. From the repository root, check the example file, then create the project and enable both APIs. You need a logged-in `gcloud`. An empty OAuth client is expected: the script prints Console URLs and exits 0.

```bash
APP_ENV=development bash config/bootstrap.sh --check
bash config/bootstrap.sh
```

`bash config/bootstrap.sh` creates the Cloud project when it is missing and enables `docs.googleapis.com` and `drive.googleapis.com`. Enabling the Drive API still needs this Cloud project. Bootstrap does not create a Drive folder. `APP_ENV=production bash config/bootstrap.sh` reads `.env` and does the same project and API work. It does not move an OAuth client, and it does not create a Drive folder.

3. In that same `GCP_PROJECT_ID`, finish these Console steps by hand. Create a Desktop client. Leave Web and IAP clients unused.

- Branding: `https://console.cloud.google.com/auth/branding?project=<GCP_PROJECT_ID>`
- Audience: External, Testing, and your test user. `https://console.cloud.google.com/auth/audience?project=<GCP_PROJECT_ID>`
- Data Access: add only `https://www.googleapis.com/auth/drive`. `https://console.cloud.google.com/auth/scopes?project=<GCP_PROJECT_ID>`
- Clients: Desktop app named `Docs CLI Desktop`. `https://console.cloud.google.com/auth/clients?project=<GCP_PROJECT_ID>`

Testing can grant this restricted scope to the listed test user. This version does not publish the consent screen, so CASA is not part of setup. After you change the scope, run `docs-cli auth`. A successful `auth` overwrites the keyring item. The refresh token is the operating-system keyring item `docs-cli` / `refresh-token`. It is not a file in the project directory. The client secret stays in `.env` and `credentials.json`. It is not written to the keyring. The first time on macOS, Keychain may ask for permission. Refusing that prompt exits with `docs-cli: keyring unavailable`.

4. Development reads your local `.env.example` only. Put this project's client id, client secret, both email addresses (replace `you@gmail.com`), and the Drive folder id there. Do not commit the secret or the folder id. `Load` rejects the placeholder email. `create` fails while the folder id is empty.

```bash
go run ./cmd/docs-cli auth
go run ./cmd/docs-cli create --title "Demo" --text "hello" --folder <folder id or folder URL>
```

5. Production uses the release binary from the build section. From the repository root, run `./docs-cli setup`. It copies `.env.example` to `.env` (mode `0600`) and asks for values that are still missing. It does not run bootstrap, and it does not replace an `.env` that already exists. Press Enter on `GCP_PROJECT_ID` and `GCP_PROJECT_NAME` unless you intend to switch Cloud projects. A new project id leaves the Desktop client behind in the old project. Clear the client id and secret, then repeat step 3 in the new project. The wizard asks for `GOOGLE_DRIVE_FOLDER_ID`. That question is the Drive folder, not `GCP_FOLDER_ID`.

6. Create a document with the release binary:

```bash
./docs-cli create --title "Demo" --text "hello"
```
