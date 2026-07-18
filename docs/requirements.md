# Requirements: session-indexer

Source: [`docs/investigation/session-recall-semantic-search.md`](investigation/session-recall-semantic-search.md)

---

## Problem

When returning to a project after days or weeks, the user cannot recall what
was discussed in previous Claude Code sessions. Specifically:

- **Need 1 — "Where did I stop?"** — SOLVED by `session-end` skill + Stop hook.
- **Need 2 — "I vaguely remember we discussed X"** — UNSOLVED. Requires semantic
  search over conversation history when the user remembers the idea but not the
  exact words.

---

## Functional Requirements

### FR-1: Index sessions

The tool must parse Claude Code JSONL session files and extract meaningful
conversation content into a searchable local store.

- Source: `~/.claude/projects/<project-hash>/<session-id>.jsonl`
- Extract: `type: user` and `type: assistant` messages where `isMeta != true`
- Filter out: slash-command echoes, system-reminder tags, permission prompts,
  very short messages (<30 chars after stripping)
- Idempotent: re-indexing the same session must not create duplicates

### FR-2: Semantic search

The tool must find relevant conversation chunks given a natural-language query.

- Primary: exhaustive cosine similarity over all embedded chunks (bge-m3 via Ollama)
- Fallback: FTS5 BM25 keyword ranking when Ollama is unavailable
- Return: ranked list of chunks with date, role, and 200-char snippet (word-boundary truncated)

### FR-3: Per-project isolation

Each project maintains its own independent index.

- Index lives at `<project-root>/.claude/sessions.db` (gitignored)
- Corruption or deletion of one project's DB does not affect any other
- No shared daemon, no shared port, no shared state

### FR-4: Automatic mining on session end

A Stop hook calls `session-indexer mine` before Claude Code closes the JSONL.

- Must complete within 60 seconds (Stop hook timeout)
- JSONL is available at hook invocation time
- Hook passes the project root and JSONL path to the binary

### FR-5: CLI interface

```
session-indexer mine    <jsonl-path> --db <path>
session-indexer search  <query>      --db <path> [--limit N] [--json]
session-indexer embed                --db <path>
session-indexer stats                --db <path>
session-indexer distill              --db <path> [--threshold N]
session-indexer facts search   <query>          --db <path> [--limit N] [--json] [--include-expired]
session-indexer facts get      <id>             --db <path> [--json]
session-indexer facts related  <id>             --db <path> [--json]
session-indexer facts supersede <new-id> <old-id> --db <path>
```

- `mine`: parse JSONL → insert chunks + generate embeddings. Idempotent.
- `search`: embedding-first cosine (FTS5 fallback when Ollama unavailable). `--limit` default 5. `--json` for machine-readable output.
- `embed`: backfill embeddings for chunks missing them (run after Ollama comes back online).
- `stats`: report index state (sessions, chunks, pending embeddings, current facts, pending distill, DB size).
- `distill`: extract structured facts from mined chunks via an LLM call (see FR-6). `--threshold` default 0.7.
- `facts search/get/related/supersede`: query and manually correct the distilled facts layer (see FR-7).

### FR-6: Distill facts

The tool must extract atomic subject-predicate-object facts from mined
chunks via a manually-invoked LLM call, judging supersession against a
bounded context of currently-valid facts about the same project.

- Source: chunks not yet processed by `distill` (`distilled_chunks` progress marker, decoupled from produced facts — a chunk legitimately yields zero facts)
- Extraction: a single Ollama `/api/generate` call per pending chunk, given the chunk plus a bounded set of current facts for supersession judgment
- Confidence gate: a deterministic Go check (default threshold 0.7) against the model's self-reported confidence — advisory input to a hard check, not an LLM-enforced instruction
- Supersession: judged automatically by the distill call; the model may only cite fact ids from the context it was given (validated in Go); a manual `facts supersede` command exists as an audit/override backstop
- Idempotent: re-running `distill` skips chunks already marked, regardless of whether they produced facts
- `distill` is a separate, manually-invoked subcommand — never hooks into `mine`'s two-phase run or its 50s deadline (see NFR-4)

### FR-7: Query facts

The tool must support read verbs over the distilled facts layer, and a
manual override for supersession.

- `facts search <query>`: FTS5 BM25 keyword search over `subject`/`predicate`/`object`; excludes tombstoned facts unless `--include-expired`
- `facts get <id>`: a single fact plus its supersedes edges (incoming: older facts this one replaced; outgoing: the fact that replaced this one, if any)
- `facts related <id>`: depth-1 union of incoming/outgoing supersedes neighbors — no BFS/deeper traversal in v1
- `facts supersede <new-id> <old-id>`: manually tombstone `old-id` in favor of `new-id`; no-op (not an error) if `old-id` is already tombstoned

---

## Non-Functional Requirements

### NFR-1: No external server

No daemon, no background process, no port. The binary runs, does its work,
and exits. Ollama is treated as an optional accelerator, not a hard dependency.

### NFR-2: Reliable storage

SQLite in WAL mode. No binary index files that can silently corrupt (ruled out
ChromaDB/HNSW for this reason). A corrupted or deleted DB can be rebuilt by
re-mining available JSONLs.

Schema versioned via `meta` table (`schema_version`). On open, if version
mismatches binary expectation: print wrapped error naming both versions and
instructing the user to delete the DB and re-mine available JSONLs (mine is
idempotent). No silent schema evolution. There is no `reindex` subcommand.

### NFR-3: Pure Go build

Single static binary. No CGO. `modernc.org/sqlite` for SQLite. Zero system
library dependencies. `go build` produces a portable binary.

### NFR-4: Performance

- `mine`: index one session in <30s on CPU (embedding 100 chunks via Ollama); must complete within 60s Stop hook timeout. Enforced internally via a 50s `context.Context` deadline. The mine run is split into two phases: (1) storing every chunk (fast, idempotent), then (2) embedding new chunks under the deadline. Chunks past the deadline are stored but flagged `Deferred` in `mine.Result` and left without an embedding row — backfill with `session-indexer embed`. Embed errors never abort a mine (counted as `Skipped`).
- `search`: return results in <2s (FTS5 fallback), <5s (embedding cosine path). The fallback is per-term OR recall (not phrase match) and also triggers when the store has zero embeddings.
- `distill` is explicitly exempt from `mine`'s determinism/latency budget — it is a manual command with no deadline, using `context.Background()` internally per chunk. A `/api/generate` call is slower than `/api/embed` (120s HTTP timeout vs 30s), and supersession judgment over a bounded fact context adds further latency that has no place inside a 60s Stop-hook window.

### NFR-5: Language support

Content is mixed Ukrainian + English. The embedding model (bge-m3) must handle
both languages with equivalent quality.

---

## Constraints

- Go 1.26.5+ (project language; patch-pinned in `go.mod` — `1.26.4` had GO-2026-5856, a `crypto/tls` ECH privacy leak fixed in `1.26.5`; see issue #31)
- SQLite via `modernc.org/sqlite` (pure Go, no CGO)
- Ollama local: `bge-m3:latest` for embeddings (1024 dims)
- Claude Code JSONL format: lines of JSON, `type` field discriminates records; `sessionId` field present in each record (used as session_id; fall back to filename stem if absent)
- Project root detected via `git rev-parse --show-toplevel` or passed explicitly
- JSONL files at `~/.claude/projects/<hash>/<session-id>.jsonl` — may be
  deleted by Claude Code cleanup; must mine at Stop hook time

---

## Out of Scope

- Cross-project search (by design: per-project isolation)
- Real-time indexing during a session
- Web UI or TUI
- Cloud sync or backup
- MCP server wrapper (can be added later as a thin layer)
- Litopys's full 6-type node taxonomy (person/project/system/concept/event/lesson) — one flat `facts` table is enough for a single-project tool
- Litopys's skill-detector / quarantine-and-promote human-review workflow — redundant with this repo's existing `dreaming`/`apply-dreaming` cycle
- Cross-project facts
- Per-fact embeddings (facts search is FTS5 BM25 only in v1 — facts are short, structured, and few per project)
- Time-travel `--as-of` queries
- BFS/multi-hop traversal for `facts related` (depth-1 only in v1)
