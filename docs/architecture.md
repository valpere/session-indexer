# Architecture: session-indexer

---

## Overview

A single Go binary with three subcommands. No daemon, no server, no shared
state between projects.

```
session-indexer
├── mine    — parse JSONL → index into SQLite (chunks + embeddings)
├── embed   — backfill embeddings for chunks that lack them
├── search  — embedding-first cosine; FTS5 fallback when Ollama unavailable
└── stats   — report index state
```

---

## Component Diagram

```
Claude Code JSONL
      │
      ▼
┌─────────────┐     ┌──────────────────┐
│  mine cmd   │────▶│  JSONL Parser    │ extracts user/assistant turns
└─────────────┘     └────────┬─────────┘
                             │ chunks (text, metadata)
                             ▼
                    ┌──────────────────┐
                    │  Chunker         │ splits long messages, filters noise
                    └────────┬─────────┘
                             │
              ┌──────────────┴──────────────┐
              ▼                             ▼
   ┌──────────────────┐          ┌──────────────────┐
   │  SQLite DB       │          │  Ollama Client   │
   │  chunks + fts5   │◀─────────│  bge-m3:latest   │
   │  embeddings BLOB │          │  POST /api/embed  │
   └──────────────────┘          └──────────────────┘
              │
              ▼
┌─────────────────────┐
│  search cmd         │
│  1. embed(query)     │
│  2. cosine over all  │
│     embeddings rows  │
│  3. JOIN chunks      │
│  4. print results    │
│  (fallback: FTS5     │
│   BM25 if Ollama     │
│   down OR no         │
│   embeddings yet)    │
└─────────────────────┘
```

---

## SQLite Schema

```sql
CREATE TABLE meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
-- populated on DB creation: INSERT INTO meta VALUES ('schema_version', '1');

CREATE TABLE chunks (
    id            INTEGER PRIMARY KEY,
    session_id    TEXT    NOT NULL,
    session_date  TEXT    NOT NULL,   -- YYYY-MM-DD (from JSONL timestamp)
    role          TEXT    NOT NULL,   -- "user" | "assistant"
    message_index INTEGER NOT NULL,   -- 0-based ordinal of message within session
    chunk_index   INTEGER NOT NULL,   -- 0-based ordinal of chunk within message
    content       TEXT    NOT NULL,
    created_at    TEXT    NOT NULL    -- RFC3339 (for display/sort, not dedup)
);

-- FTS5 content table (keeps content in sync with chunks)
CREATE VIRTUAL TABLE chunks_fts USING fts5(
    content,
    content='chunks',
    content_rowid='id',
    tokenize="unicode61 remove_diacritics 0"
);

-- Triggers to keep FTS5 in sync
CREATE TRIGGER chunks_ai AFTER INSERT ON chunks BEGIN
    INSERT INTO chunks_fts(rowid, content) VALUES (new.id, new.content);
END;
CREATE TRIGGER chunks_ad AFTER DELETE ON chunks BEGIN
    INSERT INTO chunks_fts(chunks_fts, rowid, content) VALUES ('delete', old.id, old.content);
END;

-- Embeddings stored as raw float32 BLOB (1024 floats = 4096 bytes for bge-m3)
CREATE TABLE embeddings (
    chunk_id  INTEGER PRIMARY KEY REFERENCES chunks(id) ON DELETE CASCADE,
    vector    BLOB    NOT NULL
);

-- Dedup key: positional within session, stable across re-mines
CREATE UNIQUE INDEX idx_chunks_dedup
    ON chunks(session_id, message_index, chunk_index);
```

On DB open:
```sql
PRAGMA journal_mode=WAL;
PRAGMA synchronous=NORMAL;
PRAGMA busy_timeout=5000;   -- concurrent Stop hooks don't crash
```

Schema version check on every open:
```go
var v string
db.QueryRow(`SELECT value FROM meta WHERE key='schema_version'`).Scan(&v)
if v != SchemaVersion {
    return fmt.Errorf("schema version mismatch (%s != %s): delete %s and re-mine to rebuild", v, SchemaVersion, path)
}
```
There is no `reindex` subcommand — schema bumps are not auto-evolved; users
recover by deleting the DB and re-mining available JSONLs (mine is
idempotent via `INSERT OR IGNORE`).

---

## JSONL Parsing

Relevant record types:

| type | action |
|------|--------|
| `user` where `isMeta=false` | extract `message.content` |
| `assistant` | extract `message.content[].text` (array of blocks) |
| anything else | skip |

Content extraction:

| record type | action |
|-------------|--------|
| `user` where `isMeta=false` | extract `message.content` |
| `assistant` where `isMeta=false` | extract `message.content[].text` (type=text blocks) |
| `tool_use` blocks (inside assistant content) | extract `name` + text fields from `input` (skip binary/path-only inputs) |
| `tool_result` blocks (inside user content) | extract text content; skip if base64/binary heuristic (length >10KB or matches `^[A-Za-z0-9+/]{60,}`) |
| anything else | skip |

Tool content truncation:
- Truncate any single tool block to 2KB max: `content[:2048] + "\n[truncated]"`
- This caps one large file read at ~33 lines instead of hundreds of chunks

Noise filter (skip chunk after extraction if any match):
- Starts with `<` (XML/HTML — system prompts, hook output)
- Matches `^/\w+` (slash commands)
- Stripped length < 30 chars

Chunking:
- Max chunk size: 1500 chars
- If message > 1500 chars: split on paragraph boundary, each part = separate chunk
- Preserve `session_id`, `role`, `timestamp` on each chunk

---

## Embedding

Ollama REST call:
```
POST http://localhost:11434/api/embed
{
  "model": "bge-m3:latest",
  "input": "<chunk content>"
}
→ { "embeddings": [[float32 × 1024]] }
```

Storage: `encoding/binary` LittleEndian float32 slice → BLOB.

Cosine similarity computed in Go at query time over candidates returned by FTS5.

Ollama probe before embedding:
```
GET http://localhost:11434/api/tags  (timeout 2s)
```
If probe fails → skip embedding for this run, leave `embeddings` row absent.
Log: `warn: ollama unavailable, indexed without embeddings`.

If probe succeeds but `bge-m3:latest` absent from tag list:
Log: `error: bge-m3:latest not found — run: ollama pull bge-m3:latest`
Skip embedding (same as probe failure).

---

## Search Algorithm

**Primary: embedding-first (exhaustive cosine)**

```
query string
    │
    └─── (if Ollama available)
         embed(query) → query_vec[1024]
         SELECT chunk_id, vector FROM embeddings          -- load all into memory
         cosine(query_vec, each vector) → scored[]
         sort by score DESC, take top --limit (default 5)
         JOIN chunks ON id to fetch content + metadata
         → results

Fallback (Ollama unavailable OR embeddings table is empty):
    FTS5 BM25:
    SELECT c.session_date, c.role, c.content, bm25(chunks_fts) AS rank
    FROM chunks c JOIN chunks_fts ON c.id = chunks_fts.rowid
    WHERE chunks_fts MATCH <per-term OR match>
    ORDER BY rank LIMIT --limit
    print note: "(embedding unavailable — FTS5 keyword results only)"

The FTS5 query is built by splitting the user query on whitespace and
OR-ing the terms as individually quoted FTS5 phrases (e.g. `"config"
OR "validation"`), not wrapping the whole query in one phrase. This is
better recall at the cost of precision — the fallback exists for
"vaguely remember the idea, not the exact words", which is the same
semantic-search need the cosine path solves.

The fallback also triggers when Ollama is up but the store has zero
embeddings (e.g. mined while Ollama was down, now back). Without this,
cosine would return nothing and search would go blind.

**Scale assumption:** personal session recall, <10k chunks (~40MB vectors in-memory).
Revisit if index exceeds 50k chunks.

Output format:
```
[2026-06-10 | user]
We discussed using a ring buffer for the event queue to avoid allocations...
──────────────────────────────────────────────────────
[2026-05-28 | assistant]
The config validation approach using JSON Schema has a key advantage...
```

---

## File Layout

```
session-indexer/
├── cmd/
│   └── session-indexer/
│       └── main.go          — cobra root + subcommands
├── internal/
│   ├── db/
│   │   ├── db.go            — open, schema, WAL, busy_timeout, version check
│   │   └── schema.sql       — embedded via go:embed
│   ├── mine/
│   │   ├── mine.go          — orchestrate: parse → chunk → store → embed
│   │   ├── parse.go         — JSONL → []Message (user/assistant/tool blocks)
│   │   └── chunk.go         — []Message → []Chunk (split, filter, truncate)
│   ├── embed/
│   │   └── embed.go         — Ollama client, probe+model-check, float32 BLOB
│   └── search/
│       └── search.go        — exhaustive cosine; FTS5 fallback
├── docs/
│   ├── requirements.md
│   ├── use-cases.md
│   └── architecture.md
├── .claude/
│   ├── hooks/
│   │   ├── session-end.sh
│   │   ├── session-last.sh
│   │   └── _lib/hook-common.sh
│   ├── skills/              — injected from common
│   └── settings.local.json
├── .gitignore
├── go.mod                   — go 1.26
└── Makefile
```

---

## Integration: Stop Hook

The Stop hook receives JSON on stdin. Relevant fields:

```json
{
  "session_id":       "93e23a86-...",
  "cwd":              "/home/val/wrk/myproject",
  "stop_hook_active": false,
  "transcript_path":  "/home/val/.claude/projects/-home-val-wrk-myproject/<session-id>.jsonl"
}
```

Two hooks run on every session stop, both wired inside a single `Stop` entry
of `settings.local.json` (see warning below):

- `bash .claude/hooks/session-end.sh` — writes `session-log.md` via an LLM
  call (agy → opencode → raw transcript fallback). Includes a 2h "skill
  already ran" mtime-skip to avoid double-work.
- `bash .claude/hooks/session-index.sh` — runs `session-indexer mine` on the
  transcript and silently no-ops until `session-indexer` is on PATH.

`session-index.sh` (simplified):

```bash
INPUT=$(cat)
TRANSCRIPT=$(echo "$INPUT" | jq -r '.transcript_path')
[[ -z "$TRANSCRIPT" || ! -f "$TRANSCRIPT" ]] && exit 0
command -v session-indexer >/dev/null 2>&1 || exit 0
PROJECT_ROOT=$(git rev-parse --show-toplevel 2>/dev/null || echo "$PWD")
session-indexer mine "$TRANSCRIPT" --db "$PROJECT_ROOT/.claude/sessions.db"
```

Hook logs go to `~/.cache/<project-name>/hooks.log` (per-project, derived
from the repo name by `hook-common.sh`).

> **Warning — Claude Code 2.1.x multi-Stop-hook quirk:** when `hooks.Stop`
> is an array of multiple top-level entries, only the **first** entry's
> commands run. Put multiple Stop commands in the **same** entry's
> `hooks` array (the structure used here). Verified empirically:
> 2026-06-25 to 2026-06-28, sessions went unmined until the settings
> file was collapsed to a single entry.

Note: `--project` flag removed (redundant — DB path is per-project).

---

## Open Questions

1. Should embeddings be generated synchronously during `mine` (blocking, slower)
   or queued and generated in a separate `embed` pass?
   → Synchronous for now; revisit if 60s hook timeout is hit in practice.
2. Should `search` output be structured (JSON) for use in skills/pipes,
   or human-readable only?
   → Human-readable default + `--json` flag for scripting.
