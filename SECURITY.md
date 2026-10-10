# Security policy

## Supported versions

There are no releases. Only the latest commit on `main` is supported.

## Reporting a vulnerability

Report through GitHub private vulnerability reporting:

https://github.com/mark1688288/jshelper/security/advisories/new

Do not open a public issue or pull request for a vulnerability. Do not include a live OAuth client secret, refresh token, Gemini API key, or `.env` file. Use placeholders.

In scope: how `docs-cli` handles secrets and local files. That includes `.env`, `credentials.json`, the keyring items for the refresh token and the Gemini API key, the OAuth loopback, `config/bootstrap.sh`, file permissions, and file paths.

Out of scope: Google, Google Cloud, the Gemini API, `gcloud`, and JobsDB. A person who already has the local account or the keychain is out of scope.

This is maintained by one person. There is no bug bounty and no response-time guarantee. The aim is to acknowledge a report within 7 days. A confirmed issue is fixed on `main`. A GitHub Security Advisory is published when the issue affects people using that commit.
