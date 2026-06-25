# Consilium Review: session-indexer docs

Reviewers: claude, agy, cursor-agent, kilo (4/7 — opencode server error, kimi unconfigured, codex stdin issue)
Date: 2026-06-26

---

## CRITICAL — blockers before implementation

### C1. Hybrid search flaw: FTS-first defeats semantic recall (agy, cursor, kilo)

Architecture proposes FTS5 BM25 → top-50 → embedding re-rank.
**If the user's words don't appear in the text, FTS returns 0 candidates and
embeddings never fire.** This is the exact failure mode Need 2 tries to solve.

Options:
- A) Exhaustive cosine over all embedded chunks (viable at small scale: 5k×4KB = 20MB in-memory)
- B) Two parallel passes (FTS top-50 + embedding top-50), merge, dedupe
- C) Add `sqlite-vec` extension (breaks pure-Go NFR)

**Decision needed before writing `search.go`.**

### C2. Stop hook env var unknown (all four)

`$CLAUDE_SESSION_FILE` is assumed in the integration snippet but marked "unknown"
in architecture open questions. The entire FR-4 / UC-3 flow depends on this.

**Must verify empirically before writing the hook integration.**

### C3. Dedup key fragile: timestamp collisions (claude, agy, cursor, kilo)

✅ Resolved: replaced `(session_id, role, created_at)` with `(session_id, message_index, chunk_index)`.
Added `message_index` and `chunk_index` columns to schema. `created_at` kept for display/sort only.

---

## HIGH — fix before implementation

### H1. tool_use / tool_result should not be skipped (agy, cursor, kilo)

✅ Resolved: index tool content with truncation.
- `tool_use`: name + text fields from input
- `tool_result`: text content, skip base64/binary (>10KB or long b64 match)
- Truncate any single tool block to 2KB + `[truncated]` marker

### H2. session_id source undefined (claude, cursor, kilo)

✅ Resolved: use `sessionId` field from JSONL records; fall back to filename stem.

### H3. No embedding backfill path (claude, agy, cursor, kilo)

✅ Resolved: added `session-indexer embed --db <path>` subcommand + UC-7.

### H4. FTS5 tokenizer unspecified for Ukrainian (cursor)

✅ Resolved: `tokenize="unicode61 remove_diacritics 0"` in schema.

### H5. No schema migration strategy (claude, cursor, kilo)

✅ Resolved: `meta` table with `schema_version`; mismatch → error + "run reindex".

### H6. Large tool outputs pass noise filter (claude)

✅ Resolved: tool blocks truncated to 2KB + `[truncated]` before chunking.

---

## MEDIUM — fix in docs before coding

### M1. project column redundant (agy, cursor, kilo)

✅ Resolved: removed `project` column and `--project` flag from schema and CLI.

### M2. Concurrent access — missing busy_timeout (agy, kilo)

✅ Resolved: `PRAGMA busy_timeout=5000` added on DB open.

### M3. bge-m3 model-missing not handled (claude)

✅ Resolved: probe checks tag list; missing model → clear error + pull instruction.

### M4. Chunk position not stored (claude, kilo)

✅ Resolved: `message_index` and `chunk_index` columns added to schema.

### M5. FTS5 ORDER BY rank syntax wrong (cursor)

✅ Resolved: FTS5 fallback uses `ORDER BY bm25(chunks_fts)` with rowid JOIN.

---

## FIXES — mechanical corrections

| # | Issue | File | Fix |
|---|-------|------|-----|
| F1 | "JSOLs" typo | use-cases.md | ✅ fixed → "JSONLs" |
| F2 | 60s vs 30s inconsistency | requirements.md, use-cases.md | ✅ 60s = timeout constraint; 30s = target in NFR-4 |
| F3 | mine before/after summary | use-cases.md, architecture.md | ✅ mine runs AFTER summary |
| F4 | isMeta filter only for user | architecture.md | ✅ added to assistant row in parse table |
| F5 | top-3 vs 5 inconsistency | use-cases.md | ✅ unified to 5 everywhere |
| F6 | `$CLAUDE_SESSION_FILE` assumed | architecture.md | ✅ replaced with `transcript_path` from stdin JSON |

---

## Open questions (unresolved, need decision)

1. **C1 search strategy** — ✅ resolved: embedding-first exhaustive cosine; FTS5 as Ollama-unavailable fallback
2. **C2 Stop hook env var** — ✅ resolved: `transcript_path` field in stdin JSON (not an env var). Confirmed from ralph-loop and hookify plugins.
3. **H1 tool content** — ✅ resolved: index truncated tool text (2KB cap)
4. **FTS5 in modernc.org/sqlite** — ✅ verified: v1.53.0 supports FTS5 content tables, triggers, `unicode61 remove_diacritics 0` tokenizer, `bm25()`, and Cyrillic search. Smoke test passed.
5. **Semantic-only fallback** — ✅ resolved: primary IS exhaustive cosine; FTS5 IS the fallback
