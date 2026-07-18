# Architecture: session-indexer

---

## Overview

A single Go binary with six subcommands (`facts` has four child verbs). No
daemon, no server, no shared state between projects.

```
session-indexer
├── mine    — parse JSONL → index into SQLite (chunks + embeddings)
├── embed   — backfill embeddings for chunks that lack them
├── search  — embedding-first cosine; FTS5 fallback when Ollama unavailable
├── stats   — report index state
├── distill — extract structured facts from mined chunks (LLM, manual)
└── facts   — query the distilled facts layer
    ├── search     — FTS5 keyword search
    ├── get        — one fact + supersedes edges
    ├── related    — depth-1 supersedes neighbors
    └── supersede  — manual tombstone (audit/override backstop)
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
-- populated on DB creation: INSERT INTO meta VALUES ('schema_version', '2');

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

-- Distilled facts: flat subject-predicate-object triples. No per-type node
-- tables, no separate edges table — supersedes is the only relation and is
-- functional/at-most-one, so a self-referential superseded_by column
-- captures it fully.
CREATE TABLE facts (
    id              INTEGER PRIMARY KEY,
    subject         TEXT    NOT NULL,
    predicate       TEXT    NOT NULL,
    object          TEXT    NOT NULL,
    confidence      REAL    NOT NULL,
    source_chunk_id INTEGER REFERENCES chunks(id) ON DELETE SET NULL,
    session_date    TEXT    NOT NULL,   -- denormalized from source chunk
    created_at      TEXT    NOT NULL,   -- distilled-at
    until           TEXT,               -- tombstone; NULL = currently valid
    superseded_by   INTEGER REFERENCES facts(id) ON DELETE SET NULL
);

-- FTS5 mirrors chunks_fts exactly (same tokenizer, same trigger shape)
CREATE VIRTUAL TABLE facts_fts USING fts5(
    subject, predicate, object,
    content='facts', content_rowid='id',
    tokenize="unicode61 remove_diacritics 0"
);
CREATE TRIGGER facts_ai AFTER INSERT ON facts BEGIN
    INSERT INTO facts_fts(rowid, subject, predicate, object)
    VALUES (new.id, new.subject, new.predicate, new.object);
END;
CREATE TRIGGER facts_ad AFTER DELETE ON facts BEGIN
    INSERT INTO facts_fts(facts_fts, rowid, subject, predicate, object)
    VALUES ('delete', old.id, old.subject, old.predicate, old.object);
END;

-- Distill progress marker, decoupled from produced facts — a chunk
-- legitimately yields zero facts, so "has a facts row" can't double as
-- the pending marker (that would re-distill zero-fact chunks forever).
CREATE TABLE distilled_chunks (
    chunk_id     INTEGER PRIMARY KEY REFERENCES chunks(id) ON DELETE CASCADE,
    distilled_at TEXT NOT NULL
);
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
| `tool_result` blocks (inside user content) | extract text content; skip if binary heuristic (length >10KB) — regex `^[A-Za-z0-9+/]{60,}` was removed as too broad (matched valid long alphanumeric output) |
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
POST http://localhost:11434/api/embed   (default; override with OLLAMA_HOST)
{
  "model": "bge-m3:latest",            (default; override with OLLAMA_MODEL)
  "input": "<chunk content>"
}
→ { "embeddings": [[float32 × 1024]] }
```

Storage: `encoding/binary` LittleEndian float32 slice → BLOB.

Cosine similarity computed in Go at query time, exhaustively over **all** stored
embedding vectors (FTS5 is only the fallback path, not a pre-filter).

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

**Partial embedding store:** cosine search activates as soon as any
embedding row exists (`hasEmbeddings` checks for ≥1 row). Chunks that
are stored but not yet embedded (deferred or skipped during `mine`) are
invisible to cosine search until `embed` backfills them — FTS fallback
does **not** activate for a partial store. `search` warns on stderr when
pending embeddings exist: `warn: N chunks not yet embedded — results may
be incomplete; run: session-indexer embed --db <path>`.

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

## Facts Layer

A distilled, structured layer of subject-predicate-object claims sitting
alongside the raw-text `search`. Ported (scoped down) from litopys — see
"Explicitly out of scope" in `docs/requirements.md` for what was
deliberately left out. Closes a gap raw-text search cannot: a stale fact
about a project (e.g. "implementation not started") surfaces only during
periodic review unless something distills and tombstones it at index time.

### Distill flow

```
session-indexer distill --db <path> [--threshold 0.7]
    │
    └─ for each chunk in ChunksWithoutFacts (distilled_chunks marker):
         │
         ├─ CurrentFacts(limit=ContextCap) — bounded context of
         │  non-tombstoned facts, fed to the model for supersession judgment
         │
         ├─ POST /api/generate  { model: OLLAMA_DISTILL_MODEL, chunk, existing facts }
         │  → candidates: [{subject, predicate, object, confidence, supersedes_ids}]
         │
         ├─ per candidate:
         │    confidence < threshold?  → discard (BelowThreshold++)
         │    else → InsertFact; for each supersedes_ids entry validated
         │           against the context actually given → SupersedeFact
         │
         └─ MarkChunkDistilled — regardless of fact count (zero-fact chunks
            must not be re-distilled forever)
```

`distill` is a separate, manually-invoked subcommand — it never hooks into
`mine`'s two-phase run or its 50s Stop-hook deadline. `context.Background()`
governs each chunk's `Distill` call internally.

### Confidence gate — a deliberate deviation from litopys

The gate (default 0.7) is a **deterministic Go check**
(`internal/distill.Run`), not an LLM-enforced instruction. litopys's
equivalent 0.7 threshold is instructional/human-reviewed, backed by a
quarantine-and-promote workflow. session-indexer has no such review loop
(the existing `dreaming`/`apply-dreaming` cycle already covers that role),
so the model's self-reported confidence is treated as advisory input to a
hard code-level check, not the enforcement mechanism itself.

### Supersession — automatic, with a manual backstop

The distill call judges supersession automatically, given the bounded
`CurrentFacts` context. This is a deliberate deviation from litopys, which
has no automated supersession at all — only explicit human/agent
`litopys_link` calls. A purely-manual approach (litopys's model) just
relocates the same review burden this feature exists to remove; a
deterministic same-subject-different-object heuristic produces false
positives on paraphrase and misses genuine contradictions phrased
differently — exactly the language problem an LLM call is already solving
mid-chunk.

Safeguards:
- The model may only cite fact ids from the context it was actually given
  (`Run` validates every `supersedes_ids` entry against the provided set —
  `TestRunRejectsSupersedeIDOutsideContext` covers this)
- The superseding fact must itself pass the confidence gate before it's
  inserted
- `SupersedeFact` only tombstones a fact whose `until IS NULL` — a no-op
  (not an error) if already tombstoned, so re-application is safe
- Every auto-supersession is reversible/auditable via the preserved
  `superseded_by` edge; `facts supersede <new> <old>` exists as a manual
  audit/override backstop using the same `SupersedeFact` function

If the current-facts set exceeds `ContextCap` (200), the distill call omits
context entirely for that chunk and auto-supersession is skipped for it —
an oversized context blows the prompt budget and the model has nothing
reliable to judge supersession against.

### Tombstone / supersedes resolution

Filter-based, not chain-walking — the one litopys algorithm ported
near-verbatim. `facts search` and `stats`'s current-facts count both filter
on `WHERE until IS NULL`; there is no recursive walk of `superseded_by`
chains. `facts get <id>` and `facts related <id>` are depth-1 only:
`incoming` (facts whose `superseded_by = id`) and `outgoing` (what `id`'s
own `superseded_by` points to, if any). No BFS, no multi-hop traversal —
YAGNI until a real need surfaces (see requirements.md's Out of Scope).

### Distill model

Ollama REST call:
```
POST http://localhost:11434/api/generate   (default; override with OLLAMA_HOST)
{
  "model": "qwen2.5:latest",   (default; override with OLLAMA_DISTILL_MODEL —
                                 a chat/generate model, distinct from
                                 bge-m3:latest; must be pulled separately)
  "prompt": "<template with CURRENT KNOWN FACTS + CHUNK>",
  "stream": false,
  "format": "json"
}
→ { "response": "{\"facts\":[...]}" }
```

120s HTTP client timeout (vs embed's 30s) — generate is a larger completion
than a single embedding vector. Single attempt, no retry — matches embed's
documented no-retry convention; a failed chunk is left un-distilled
(`Failed++`, not marked in `distilled_chunks`) and retried on the next run.

Availability probe is the identical `GET /api/tags` (2s timeout) pattern
used by `embed` — if the configured `OLLAMA_DISTILL_MODEL` isn't in the tag
list, `distill` fails gracefully with `ollama unavailable — start it and
pull the distill model (OLLAMA_DISTILL_MODEL)`, matching NFR-1.

### Changelog

**2026-07-18** — Added the facts layer (`distill`, `facts search/get/related/supersede`), `SchemaVersion` "1" → "2". Existing DBs must be deleted and re-mined (no migration framework, by design — see NFR-2).

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
│   ├── search/
│   │   └── search.go        — exhaustive cosine; FTS5 fallback; Stats
│   ├── distill/
│   │   └── distill.go       — Ollama generate client + orchestration (confidence gate, supersession)
│   └── facts/
│       └── facts.go         — read verbs: search (FTS5), get, related
├── docs/
│   ├── requirements.md
│   ├── use-cases.md
│   └── architecture.md
├── .claude/                   — tracked (agents/, hooks/, skills/); only
│   │                            sessions.db, session-log.md, settings.local.json,
│   │                            and telemetry.jsonl are gitignored (per-machine
│   │                            state, see .gitignore)
│   │                            Canonical source: ~/wrk/common/skills/session-recall/
│   ├── hooks/
│   │   ├── session-end.sh    — Stop: LLM summary → session-log.md
│   │   ├── session-index.sh  — Stop: mine JSONL → sessions.db
│   │   ├── session-last.sh   — SessionStart: inject last summary
│   │   ├── session-recall.sh — SessionStart: semantic context injection
│   │   └── _lib/hook-common.sh  — logging + shared session-log.md rotation (jq/bash, no python3)
│   ├── skills/
│   │   └── session-recall/SKILL.md  — /recall <query> skill + orchestrator subagent-prep section
│   └── settings.local.json
├── .gitignore
├── go.mod                   — go 1.26.5
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
  already ran" mtime-skip to avoid double-work. JSONL excerpt extraction,
  the opencode JSONL response parsing, and the log rotation itself are
  all `jq`/bash — no python3.
- `bash .claude/hooks/session-index.sh` — runs `session-indexer mine` on the
  transcript and silently no-ops until `session-indexer` is on PATH.

`session-index.sh` (simplified):

```bash
INPUT=$(cat)
TRANSCRIPT=$(echo "$INPUT" | jq -r '.transcript_path // empty')
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

## Integration: SessionStart Hook

`session-recall.sh` fires on every `SessionStart` event and injects semantically
relevant past session chunks as `additionalContext`.

```bash
SESSION_START hooks:
  1. session-last.sh   — injects last session-log.md entry (structured summary)
  2. session-recall.sh — derives a query from git branch + last 3 commit messages,
                         runs `session-indexer search "$QUERY" --db "$DB" --limit 5 --json`,
                         filters tool-call noise,
                         groups by date, injects top 3 dates × 2 chunks
```

The query is derived automatically — no user input required:
```bash
BRANCH=$(git rev-parse --abbrev-ref HEAD | sed 's/[_\/-]/ /g')
COMMITS=$(git log --oneline -3 | cut -d' ' -f2- | tr '\n' ' ')
QUERY="$BRANCH $COMMITS"
```

Output format (injected as `additionalContext`):
```
Relevant past sessions (semantic search):

### 2026-06-28
  [0.91] We decided to use a ring buffer for the event queue to avoid allocations…
  [0.87] The db.Open idempotency comes from INSERT OR IGNORE on the dedup index…

### 2026-06-20
  [0.82] Chunker splits on paragraph boundaries, max 1500 chars per chunk…
```

`session-recall.sh` silently no-ops if:
- `session-indexer` is not in PATH
- `.claude/sessions.db` does not exist yet (first session)
- the derived query is empty (no git context)

---

## Orchestrator / Subagent Recall Pattern

Subagents spawned via the Agent tool start cold — no `SessionStart` hook, no
shared context. `.claude/skills/session-recall/SKILL.md` documents the same
`search` subcommand used by `/recall`, but for a different caller: the
*orchestrator*, invoking it directly before spawning a subagent whose task
benefits from project history (rather than through the Skill tool):

```bash
session-indexer search "<query>" --db .claude/sessions.db --limit 5 --json \
  | jq -r '.[] | "[\(.SessionDate) · \(.Role)] \(.Content[0:300])"'
```

Results are folded into the subagent's prompt (subagent prompts must be
self-contained). This is orchestrator-side only: current subagent tool
allowlists (`bug-fixer`, `code-generator`, `tech-lead`, etc.) exclude the
`Skill` tool, so a spawned subagent cannot invoke `/recall` or this
entrypoint itself mid-task.

