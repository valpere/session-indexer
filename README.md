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
(`ollama pull bge-m3:latest`). When unavailable, `mine` indexes without
embeddings and `search` falls back to FTS5 keyword ranking.

## Automatic indexing

A Stop hook (`.claude/hooks/session-index.sh`) runs `mine` on every session end.
It no-ops until `session-indexer` is on PATH.
