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
session-indexer stats               --db <path>
```

### File Layout

```
cmd/session-indexer/main.go       — Cobra root + subcommands
internal/db/
  db.go                           — open, WAL, busy_timeout, schema version check
  schema.sql                      — embedded via go:embed
internal/mine/
  mine.go                         — orchestrate: parse → chunk → store → embed
  parse.go                        — JSONL → []Message
  chunk.go                        — []Message → []Chunk (split, filter, truncate)
internal/embed/
  embed.go                        — Ollama client, probe+model-check, float32 BLOB
internal/search/
  search.go                       — exhaustive cosine; FTS5 BM25 fallback
```

### Storage

SQLite at `<project-root>/.claude/sessions.db` (gitignored, per-project). No CGO — use `modernc.org/sqlite`. Open with WAL + `synchronous=NORMAL` + `busy_timeout=5000`.

Schema version stored in `meta` table (`key='schema_version'`). On open: if version mismatches, exit with "run: session-indexer reindex". Never silently evolve schema.

Dedup key: `UNIQUE(session_id, message_index, chunk_index)` — `INSERT OR IGNORE` makes `mine` idempotent.

### JSONL Parsing

Extract `user` and `assistant` turns where `isMeta=false`. Skip XML/HTML (`<`), slash commands (`/\w+`), and content <30 chars after stripping. Truncate any single tool block to 2KB. Chunk messages at 1500 chars on paragraph boundaries.

### Embeddings

Ollama REST: `POST localhost:11434/api/embed`, model `bge-m3:latest` (1024 dims). Probe first with `GET /api/tags` (2s timeout) — if unavailable, skip embeddings and log a warning. Store as `encoding/binary` LittleEndian float32 BLOB.

### Search

Primary: embed query → exhaustive cosine over all `embeddings` rows loaded into memory. Scale assumption: <10k chunks. Fallback when Ollama unavailable: FTS5 BM25, with output note indicating lower quality.

## Key Constraints

- **Pure Go, no CGO.** `go build` must produce a portable static binary.
- **Go 1.26+.** Use `go 1.26` in `go.mod`.
- **60-second budget.** `mine` runs inside a Claude Code Stop hook. Must complete well within 60s.
- **Mixed Ukrainian + English content.** `bge-m3` handles both; `unicode61 remove_diacritics 0` tokenizer for FTS5.
- **Per-project isolation.** No cross-project search, no shared state.

## Stop Hook Integration

`.claude/hooks/session-end.sh` calls `session-indexer mine <transcript_path> --db <project-root>/.claude/sessions.db` on every session end. The hook fires with JSON on stdin containing `transcript_path` and `session_id`. JSONL files may be deleted by Claude Code cleanup shortly after — mine at hook time.
