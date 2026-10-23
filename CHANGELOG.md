# Changelog

Notable changes to `docs-cli` are recorded here. The source for a release is its git tag. Install steps stay on the GitHub Release.

## 1.2.0 - 2026-10-23

### Added

- `docs-cli grok-key` stores a Grok API key in the keyring item `docs-cli` / `grok-api-key`. The key is typed at the terminal and is not echoed. It is not written to `.env` or `credentials.json`.
- `analyze --json` and `analyze --batch` accept `--grok`. That flag asks Grok for the analysis JSON, skips the on-device model and Gemini, and does not fall back.
- A Grok request uses the same job description and extraction instructions as Gemini. Reasoning effort is high. The built-in model id is `grok-4.7`.
- When analyze calls Grok, the model id is `GROK_MODEL` from `.env`. A missing or empty value uses `grok-4.7`. The API key is not read from `.env`.
- `--gemini` and `--grok` together are rejected. `draft` does not accept `--grok`. Without `--grok`, analyze still tries the on-device model first.

### Documentation

- The README adds the xAI Grok section, text badges for xAI and Grok, and the grok-key and grok-analyze screenshots.
- The supported release in the security policy is 1.2.0. The policy names the Grok API key with the other keyring secrets.

## 1.1.0 - 2026-10-15

### Changed

- Gemini requests use `gemini-3.8-flash` when `GEMINI_MODEL` is empty. The 1.0.0 default was `gemini-3.1-flash-lite`.
- Every Gemini `generateContent` request sets `thinkingLevel` to `HIGH`. Thought parts are dropped before the JSON is parsed.
- The Gemini timeout is five minutes.
- When `parse` calls Gemini, the system instruction is the shared parse prompt. That covers section mapping, `--autoparse`, `--txt`, and `--batch`.
- Page mode is the default when the first line is not `Blocks:` or `Page:`.
- Exact duties headings include `job duties` and `duties`.
- Empty header fields can be filled from `Job title`, `Company`, `Location`, `Classification`, and `Work type` lines, or from an unlabeled JobsDB block around `View all jobs`.
- When `analyze` calls Gemini, the user message is the filtered section JSON (`title_block`, benefits, duties, and requirements) and the system instruction is the extraction prompt. Emails in that JSON are redacted.
- On-device `analyze` still uses `AnalysisInstructions` and the labeled prompt text. `analyze --gemini` still skips `fm`.

### Documentation

- The README adds badges, the OAuth, analysis, demo, and tech-distribution screenshots, and a Gemini terms note.
- The README corrects the parse sentence and the Drive connection sentence.
- JobsDB is no longer named in the README or the security policy.
- The supported release in the security policy is 1.1.0.

## 1.0.0 - 2026-10-10

### Added

- Initial Apple silicon release of `docs-cli`.
- The archive contains the production binary, `LICENSE`, `.env.example`, and `config/bootstrap.sh`.
