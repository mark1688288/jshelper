# Changelog

Notable changes to `docs-cli` are recorded here. The source for a release is its git tag. Install steps stay on the GitHub Release.

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
