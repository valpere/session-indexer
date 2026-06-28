# session-indexer

Per-project semantic search over Claude Code session history. Pure Go, no CGO,
no daemon. Indexes JSONL transcripts into a per-project SQLite store and
retrieves them via bge-m3 embeddings (Ollama) with FTS5 BM25 fallback.

## Build

```bash
go build -o bin/session-indexer ./cmd/session-indexer
go install ./cmd/session-indexer   # to PATH
```

## Usage

```bash
session-indexer mine   <jsonl-path> --db .claude/sessions.db
session-indexer search "config validation approach" --db .claude/sessions.db [--limit N] [--json]
session-indexer embed  --db .claude/sessions.db     # backfill missing embeddings
session-indexer stats  --db .claude/sessions.db
```

## Embeddings

Optional. Requires Ollama on `localhost:11434` with `bge-m3:latest`
(`ollama pull bge-m3:latest`). `mine` runs with a 50s `context.Context`
deadline (headroom under the 60s Stop-hook budget): storing is fast and
unconditional, embedding is the phase that respects the deadline. Chunks
past the deadline are stored but flagged as **deferred** — no embedding
row yet, backfill with `session-indexer embed` once Ollama is reachable.
Embed errors never abort a mine — such chunks are stored and counted as
**Skipped** (same storage state as Deferred: present in the DB, no
embedding row, included in `stats --db` `pending`). Deferred and Skipped
differ only in cause (deadline vs error), not in backfill path.

When Ollama is unavailable or the store has zero embeddings, `search`
falls back to FTS5 BM25 with per-term OR recall (not phrase match) and
prints an output note indicating the lower quality.

## Automatic indexing

A Stop hook fires on every session end. It runs two commands inside one
`Stop` entry of `settings.local.json`: `bash .claude/hooks/session-end.sh`
(writes `session-log.md`) and `bash .claude/hooks/session-index.sh`
(runs `session-indexer mine <transcript> --db .claude/sessions.db`).
`session-index.sh` silently no-ops until `session-indexer` is on PATH.

> Note: in Claude Code 2.1.x, only the first top-level `Stop` entry's
> commands run. Put multiple Stop commands in the **same** entry's
> `hooks` array (the structure used here).

Hook logs go to `~/.cache/<project-name>/hooks.log`.
