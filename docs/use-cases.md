# Use Cases: session-indexer

---

## UC-1: Return to project after a break

**Actor:** Val, returning to a project after 2 weeks.

**Trigger:** Opens Claude Code. SessionStart hook injects last session summary
(from `session-end` skill). He also vaguely remembers a design decision.

**Flow:**
1. Val types: `session-indexer search "auth middleware approach" --db .claude/sessions.db`
2. Tool embeds query via bge-m3, runs exhaustive cosine over all indexed chunks.
3. Returns top-5 chunks from past sessions, each with date and 200-char snippet.
4. Val identifies which session, reads the relevant snippet, recalls context.

**Success:** Val recalls the decision without reading the full session transcript.

---

## UC-2: Find a discarded approach

**Actor:** Val, mid-session.

**Trigger:** Current approach isn't working. Val thinks they discussed an
alternative weeks ago but can't remember the exact wording.

**Flow:**
1. Val queries: `session-indexer search "config validation alternative" --db .claude/sessions.db`
2. Tool returns top-5 ranked results from past sessions.
3. One result from 3 weeks ago mentions the approach.
4. Val continues with that approach.

**Success:** Relevant chunk found despite Val not remembering exact terminology.

---

## UC-3: Session is indexed automatically on exit

**Actor:** Claude Code Stop hook.

**Trigger:** Val closes Claude Code (or runs `/exit`).

**Flow:**
1. Stop hook fires: both `bash .claude/hooks/session-end.sh` and `bash .claude/hooks/session-index.sh` (wired into a single `Stop` entry of `settings.local.json` — Claude Code 2.1.x runs only the first top-level entry, so both must live in the same entry's `hooks` array).
2. Each hook reads `transcript_path` from stdin JSON.
3. `session-end.sh` writes summary to `session-log.md` (LLM call via agy → opencode → raw transcript fallback).
4. `session-index.sh` calls `session-indexer mine <transcript_path> --db <project-root>/.claude/sessions.db` (silently no-ops until the binary is on PATH).
5. Binary opens/creates DB (schema version check), parses JSONL, filters noise, chunks messages (user/assistant/tool).
6. Inserts chunks into `chunks` + `chunks_fts`. Dedup by `(session_id, message_index, chunk_index)`.
7. For each chunk: calls Ollama `bge-m3` to generate embedding, stores in `embeddings`. Chunks past the 50s `context.Context` deadline are stored but flagged `Deferred` (no embedding row yet); backfill with `session-indexer embed` once Ollama is reachable. Embed errors never abort the mine (counted as `Skipped`).
8. Exits in <30s in the happy path (well within 60s hook timeout); can use up to 50s before deferring.

**Success:** Session indexed before JSONL is cleaned up by Claude Code.

---

## UC-4: Ollama unavailable (fallback)

**Actor:** Val, offline or Ollama not running.

**Trigger:** `session-indexer search "query" --db .claude/sessions.db`

**Flow:**
1. Tool probes Ollama (`GET /api/tags` timeout 2s) — fails.
2. Falls back to FTS5-only BM25 search.
3. Returns results ranked by BM25 relevance.
4. Output note: `(embedding unavailable — FTS5 results only)`

**Success:** Search still works, with lower recall quality, user is informed.

---

## UC-7: Backfill missing embeddings

**Actor:** Val, after Ollama was unavailable during earlier mines.

**Trigger:** `session-indexer stats` shows "N pending embeddings".

**Flow:**
1. Val runs: `session-indexer embed --db .claude/sessions.db`
2. Tool probes Ollama + checks bge-m3 available.
3. For each chunk without an embedding: generates vector, inserts into `embeddings`.
4. Reports: "Embedded 47 pending chunks."

**Success:** All chunks have embeddings; search quality restored.

---

## UC-5: Rebuild index from existing JSONLs

**Actor:** Val, after a DB corruption or on a new machine.

**Trigger:** `.claude/sessions.db` is missing or corrupt.

**Flow:**
1. Val runs a shell loop:
   ```bash
   for f in ~/.claude/projects/-home-val-wrk-myproject/*.jsonl; do
     session-indexer mine "$f" --db .claude/sessions.db
   done
   # Then backfill any pending embeddings (if Ollama was unavailable during some mines):
   session-indexer embed --db .claude/sessions.db
   ```
2. Each call is idempotent: chunks with existing `(session_id, message_index, chunk_index)` are skipped via UNIQUE constraint (INSERT OR IGNORE).
3. DB is rebuilt from available JSONLs.

**Success:** Index rebuilt. Sessions deleted by Claude Code cleanup are lost
(expected — no backup mechanism in scope).

---

## UC-6: Inspect index state

**Actor:** Val, troubleshooting.

**Flow:**
1. `session-indexer stats --db .claude/sessions.db`
2. Output:
   ```
   Sessions indexed: 47
   Chunks total:     1823
   With embeddings:  1820  (3 pending — Ollama unavailable at mine time)
   Oldest entry:     2026-01-14
   Newest entry:     2026-06-25
   DB size:          4.2 MB
   ```

**Success:** Val understands the state of the index without opening SQLite directly.
