# Security policy

## Supported versions

The supported version is the latest GitHub Release. That release is [1.0.0](https://github.com/mark1688288/jshelper/releases/tag/v1.0.0), tag `v1.0.0`. The supported artifact is `docs-cli_darwin_arm64.tar.gz` on that release. Source for the binary is tag `v1.0.0`.

A newer release replaces 1.0.0 as the supported version. Other commits on `main` are not supported on their own.

## Reporting a vulnerability

Report through GitHub private vulnerability reporting:

https://github.com/mark1688288/jshelper/security/advisories/new

Do not open a public issue or pull request for a vulnerability. Do not include a live OAuth client secret, refresh token, Gemini API key, or `.env` file. Use placeholders.

In scope: how `docs-cli` handles secrets and local files. That includes `.env`, `credentials.json`, the keyring items for the refresh token and the Gemini API key, the OAuth loopback, `config/bootstrap.sh`, file permissions, and file paths.

Out of scope: Google, Google Cloud, the Gemini API, `gcloud`. A person who already has the local account or the keychain is out of scope.

This is maintained by one person. There is no bug bounty and no response-time guarantee. The aim is to acknowledge a report within 7 days. A confirmed issue is fixed on `main` and published in a GitHub Release. A GitHub Security Advisory is published when the issue affects a supported release.
