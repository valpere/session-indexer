# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```bash
# Build
go build -o bin/session-indexer ./cmd/session-indexer

# Install to PATH
go install ./cmd/session-indexer

# Test all
go test ./...

# Test single package
go test ./internal/mine/...

# Test with race detector
go test -race ./...

# Lint
go vet ./...

# Format
gofmt -w .
```

There is no Makefile yet — add one when build steps become non-trivial.

## Architecture

Single Go binary, no daemon, no shared state between projects. Four subcommands via Cobra:

```
session-indexer mine   <jsonl-path> --db <path>        # parse JSONL → SQLite
session-indexer search <query>      --db <path> [--limit N] [--json]
session-indexer embed               --db <path>        # backfill missing embeddings
session-indexer stats               --db <path>        # sessions, chunks, pending, DB size
```

### File Layout

```
cmd/session-indexer/main.go       — Cobra root + subcommands
internal/types.go                 — shared row types: Chunk, SearchResult
internal/db/
  db.go                           — open, WAL, busy_timeout, schema version check
  schema.sql                      — embedded via go:embed
internal/mine/
  mine.go                         — orchestrate: parse → chunk → store → embed (two-phase: store first, embed respects ctx deadline; deferred chunks backfilled via `embed`). Defines `mine.Result` (ChunksInserted / Embedded / Skipped / Deferred).
  parse.go                        — JSONL → []Message
  chunk.go                        — []Message → []Chunk (split, filter, truncate; rune-safe hard-split for Cyrillic)
internal/embed/
  embed.go                        — Ollama client, probe+model-check, float32 BLOB
internal/search/
  search.go                       — exhaustive cosine; FTS5 BM25 fallback
```

### Storage

SQLite at `<project-root>/.claude/sessions.db` (gitignored, per-project). No CGO — use `modernc.org/sqlite`. Open with WAL + `synchronous=NORMAL` + `busy_timeout=5000`.

Schema version stored in `meta` table (`key='schema_version'`). On open: if version mismatches, exit with "schema version mismatch (X != Y): delete <db-path> and re-mine to rebuild". There is no `reindex` subcommand; users recover by deleting the DB and re-mining available JSONLs. Never silently evolve schema.

Dedup key: `UNIQUE(session_id, message_index, chunk_index)` — `INSERT OR IGNORE` makes `mine` idempotent.

### JSONL Parsing

Extract `user` and `assistant` turns where `isMeta=false`. Skip XML/HTML (`<`), slash commands (`/\w+`), and content <30 chars after stripping. Truncate any single tool block to 2KB. Chunk messages at 1500 chars on paragraph boundaries.

### Embeddings

Ollama REST: `POST localhost:11434/api/embed`, model `bge-m3:latest` (1024 dims). Probe first with `GET /api/tags` (2s timeout) — if unavailable, skip embeddings and log a warning. Store as `encoding/binary` LittleEndian float32 BLOB.

`mine` runs with a 50s `context.Context` deadline (headroom under the 60s Stop-hook budget). Storing is fast and unconditional; embedding is the phase that respects the deadline. Chunks past the deadline are stored but flagged `Deferred` in the `Result` and left without an embedding row — backfill with `session-indexer embed`. Embed errors never abort the mine (counted as `Skipped`).

### Search

Primary: embed query → exhaustive cosine over all `embeddings` rows loaded into memory. Scale assumption: <10k chunks. Fallback to FTS5 BM25 when Ollama is unavailable **OR** the store has zero embeddings (e.g. mined while Ollama was down); FTS uses per-term OR recall, not phrase match. Output notes when the fallback is used.

## Key Constraints

- **Pure Go, no CGO.** `go build` must produce a portable static binary.
- **Go 1.26+.** Use `go 1.26` in `go.mod`.
- **60-second budget.** `mine` runs inside a Claude Code Stop hook. Must complete well within 60s (enforced internally via a 50s `context.Context` deadline — see Embeddings).
- **Mixed Ukrainian + English content.** `bge-m3` handles both; `unicode61 remove_diacritics 0` tokenizer for FTS5.
- **Per-project isolation.** No cross-project search, no shared state.

## Stop Hook Integration

`.claude/settings.local.json` wires two Stop-hook commands into a single `Stop` entry: `bash .claude/hooks/session-end.sh` (writes `session-log.md`) and `bash .claude/hooks/session-index.sh` (runs `session-indexer mine <transcript_path> --db <project-root>/.claude/sessions.db`). The hook fires with JSON on stdin containing `transcript_path` and `session_id`. JSONL files may be deleted by Claude Code cleanup shortly after — mine at hook time. The `session-index.sh` guard silently no-ops until `session-indexer` is on `PATH`.

> Note: in Claude Code 2.1.x, multiple **top-level** `Stop` array entries are not all fired — only the first runs. Put multiple Stop commands in the **same** entry's `hooks` array (the structure used here).
