# Changelog

All notable changes to `session-indexer` are documented here. Format loosely
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versioning
follows the project-specific [SemVer policy](docs/versioning.md) (any schema
bump gets at least a MINOR release, never a PATCH).

Each GitHub Release also carries auto-generated, PR-by-PR notes — this file
is the shorter, thematically-grouped summary; see
[Releases](https://github.com/valpere/session-indexer/releases) for the
full per-PR history.

## [Unreleased]

## [0.3.0] - 2026-08-27

### Added
- Browse the index without a query: `sessions` (roll up by day or, with
  `--by-session`, by raw `session_id`), `list` (newest-first chunks with
  date/role/session filters), `show <chunk-id>` (full chunk text plus the
  facts distilled from it — closes the `facts get <id>` →
  `source_chunk_id` provenance gap), and `facts list` (query-free
  counterpart to `facts search`). No schema change, no forced re-mine.

### Changed
- `embeddings` now records a `model` tag per row (`SchemaVersion` 2 → 3).
  `search` filters to the currently configured model's rows and warns on
  a mismatch instead of silently scoring vectors of a different
  dimensionality; `embed` re-embeds foreign-model rows in place, so
  switching embedding provider/model no longer requires a full DB wipe
  (only this one-time schema bump does — see
  [`docs/migrations/schema-v2-to-v3.md`](docs/migrations/schema-v2-to-v3.md)
  for an in-place, no-data-loss upgrade path).
- CI hardening: `govulncheck` now runs on a weekly schedule in addition to
  push/PR, files a tracked issue on a scheduled failure, and is pinned as
  a `go.mod` tool dependency (Dependabot-tracked) instead of an unpinned
  `go install ...@latest`. The failure-notification step's `issues:
  write` permission is scoped to its own job, never granted to ordinary
  push/PR runs.

### Fixed
- A negative `--limit` on `list`/`facts list` is now rejected — SQLite
  treats a negative `LIMIT` as unbounded, which would have silently
  returned every row instead of erroring.

## [0.2.0] - 2026-07-24

### Added
- Facts layer: `distill` extracts structured subject-predicate-object
  claims from mined chunks via an LLM call (Ollama `/api/generate`,
  default `glm-5.2:cloud`, `--model` override), with a deterministic
  confidence gate and automatic supersession (bounded-context judgment,
  validated against already-tombstoned/out-of-context fact ids).
  `facts search/get/related/supersede` query the resulting claim store,
  separate from raw-text `search`.
- `distill` concurrency (`--concurrency`), retry with exponential backoff
  on transient failures (`--retries`), a circuit breaker for persistent
  outages (`--circuit-breaker`), a `--limit` cap, and live progress
  output. Safe to interrupt (Ctrl-C/SIGTERM) — resumes cleanly on the
  next run, no partial-chunk loss.
- Release workflow: cross-compiled binaries (`linux/amd64`,
  `linux/arm64`, `darwin/amd64`, `darwin/arm64`, `windows/amd64`)
  attached to a GitHub Release on every `v*` tag push. Scheduled
  `govulncheck` security scanning and Dependabot.

### Changed
- Dropped the Python 3 dependency from `session-recall`'s hook scripts in
  favor of `jq` — matches this project's own pure-Go/bash convention.
- `session-recall`'s `/recall` skill moved to a single user-level copy;
  the per-project mechanism (hooks, `.claude/sessions.db`) stays
  project-local.

### Fixed
- Patch-pinned `go.mod` to Go 1.26.5, fixing a stdlib CVE
  (GO-2026-5856) reachable via the CI vulncheck job.

## [0.1.0] - 2026-06-29

Initial release. Core pipeline: `mine` (JSONL → SQLite, idempotent,
chunked on paragraph boundaries with a noise filter), `embed` (Ollama
`bge-m3:latest`, float32 BLOB storage, availability probe with graceful
degradation), `search` (embedding-first exhaustive cosine similarity with
an FTS5 BM25 fallback), and `stats`. Schema-version guard on every DB
open (hard error on mismatch, no silent auto-migration, by design). Stop
hook wiring for automatic per-session indexing.

[Unreleased]: https://github.com/valpere/session-indexer/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/valpere/session-indexer/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/valpere/session-indexer/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/valpere/session-indexer/releases/tag/v0.1.0
