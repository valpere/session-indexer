---
name: debug
description: "Systematic bug diagnosis — reproduce, isolate, hypothesize, verify, fix. Usage: /debug [what's broken]"
---

# Skill: /debug
# Systematic Bug Diagnosis

---

## PROTOCOL

Bugs have two parts: the **symptom** (what you observe) and the **cause** (what's actually wrong). The protocol moves from symptom → cause → fix, one verified step at a time.

### Phase 1 — Reproduce

**Goal:** Reliable, minimal reproduction before touching any code.

1. State the symptom precisely: what input, what output, what was expected.
2. Find the smallest input that triggers it.
3. Verify you can reproduce it consistently.
4. Is this a regression? `git log --oneline -20` — when did it last work?

**If you can't reproduce it:** Stop. Gather more information. Unreproducible bugs are unsolvable.

### Phase 2 — Isolate

**Goal:** Narrow the search space to the smallest possible area.

1. Identify the layers involved: UI → API → service → DB → external?
2. Test each boundary — where does correct input produce wrong output?
3. Binary-search the call stack: disable half, does the bug disappear?
4. Is it environment-specific? dev vs prod? one machine vs all?

**Isolation heuristics:**
- Works in tests, breaks live → check environment (config, secrets, timing)
- Started after a deploy → `git bisect`
- Intermittent → look for shared mutable state, races, time-dependent logic

### Phase 3 — Hypothesize

State one falsifiable hypothesis before changing any code:

```
Hypothesis  : [what I think is wrong]
Evidence for: [what supports this]
Against     : [what doesn't fit]
Test        : [one action that confirms or refutes]
```

**One hypothesis at a time.** Testing multiple simultaneously makes it impossible to know what fixed it.

### Phase 4 — Verify

1. Run the test that confirms or refutes.
2. **Confirmed** → move to fix.
3. **Refuted** → form a new hypothesis. Don't modify the failing thing yet.
4. Keep a short log: what you tried, what you learned.

### Phase 5 — Fix

1. Make the minimal change that fixes the root cause — not the symptom.
2. Run the full test suite.
3. Add a regression test that would have caught this bug.
4. Commit message explains *why*, not just *what*:
   ```
   fix: [what was broken]

   Root cause: [why it happened]
   Fix: [what changed and why this is correct]
   ```

---

## COMMON BUG PATTERNS

| Pattern | Symptom | Where to look |
|---------|---------|---------------|
| **Off-by-one** | Wrong last/first element | Loop bounds, slice indices, pagination |
| **Nil/null dereference** | Crash on access | Unguarded pointer use, missing null checks |
| **Race condition** | Intermittent, timing-dependent | Shared state, goroutines/threads, async code |
| **Wrong input assumptions** | Works in tests, breaks in prod | Input validation, edge cases, empty/max values |
| **Config/env mismatch** | Works locally, breaks in CI/prod | `.env` files, env var names, defaults |
| **Stale state** | Shows old data | Caches, memoization, DB transaction isolation |
| **Type coercion** | Wrong math, unexpected falsy | JS `==`, int/float truncation, string/int mixing |
| **Dependency version** | Broke after upgrade | Changelog, breaking changes, peer dependencies |

---

## REGRESSION TEST FORMAT

```
// Regression: [short description of what was broken]
// Arrange: exact conditions that triggered the bug
// Act:     the action that was broken
// Assert:  the correct outcome
```

The test must reproduce the **exact** failing scenario, not a simplified analogue.

---

## RULES

- **Reproduce before touching code.** A fix without reproduction is a guess.
- **One change at a time.** Multiple simultaneous changes make causation unknowable.
- **Fix the root cause, not the symptom.** Wrapping a bug in an `if` is not a fix.
- **Always add a regression test.** If it broke once, it can break again.
- **`git bisect` for regressions.** Faster than reading the diff.
- Tag unverified claims `[hypothesis]` when communicating status.

---

## PROJECT QUICK REFERENCE

Stack: **Go** [inferred — no go.mod yet; project in planning stage as of 2026-06-25]

### Test commands

```bash
# Run all
go test ./...

# Run all with race detector (preferred)
go test -race ./...

# Run single package
go test ./internal/mine/...

# Run single test by name
go test ./internal/mine/... -run TestChunkFilter

# Coverage
go test -cover ./...

# Verbose output (shows each test name)
go test -v ./...
```

### Debug logging

No debug env vars in source yet [project pre-implementation]. When adding:
- Use `os.Getenv("SESSION_INDEXER_DEBUG")` or similar for verbose output
- Ollama probe failures already log to stderr: `warn: ollama unavailable, indexed without embeddings`
- SQLite errors surface via returned `error` — check `err != nil` after every DB call

To manually inspect the SQLite DB:
```bash
# Open index
sqlite3 .claude/sessions.db

# Check schema version
SELECT * FROM meta;

# Count chunks and pending embeddings
SELECT COUNT(*) FROM chunks;
SELECT COUNT(*) FROM chunks WHERE id NOT IN (SELECT chunk_id FROM embeddings);

# WAL checkpoint status
PRAGMA wal_checkpoint;
```

### Known fragile areas

Derived from architecture docs — no git history yet:

| Area | File (planned) | Risk |
|------|---------------|------|
| JSONL parsing | `internal/mine/parse.go` | Binary heuristic for tool_result (`len>10KB` or base64 pattern); tool block 2KB truncation edge cases |
| Noise filter | `internal/mine/chunk.go` | Strips chunks <30 chars after strip — easy to over-filter multilingual content |
| Dedup logic | `internal/mine/mine.go` | `INSERT OR IGNORE` on `(session_id, message_index, chunk_index)` — if `session_id` is absent from JSONL, falls back to filename stem; mismatch = duplicates |
| Ollama probe | `internal/embed/embed.go` | 2s timeout on `GET /api/tags`; `bge-m3:latest` model-name match is exact string — version suffix in tag list will fail silently |
| Float32 BLOB | `internal/embed/embed.go` | `encoding/binary` LittleEndian; corrupted BLOB = cosine NaN; check `len(blob) % 4 == 0` and `len(blob) == 4096` |
| Schema version | `internal/db/db.go` | Hard exit on mismatch — if user has old DB, they must run `reindex`; no migration path |
| FTS5 sync | `internal/db/schema.sql` | Triggers keep FTS5 in sync; if trigger fires after content delete without FTS delete → phantom results |
| Search fallback | `internal/search/search.go` | Cosine over all embeddings loaded into memory — at 10k+ chunks this is ~40MB; no pagination |

### Stack-specific debug notes

**Pure Go / no CGO:**
- `modernc.org/sqlite` — no system `libsqlite3` needed; if build fails with sqlite errors, check `go.mod` replaces or proxy issues
- Race detector (`-race`) is safe to run — no CGO exclusions needed

**Ollama connectivity:**
```bash
# Is Ollama running?
curl -s http://localhost:11434/api/tags | jq '.models[].name'

# Is bge-m3 available?
curl -s http://localhost:11434/api/tags | jq '[.models[].name] | map(select(startswith("bge-m3")))'

# Manual embed test
curl -s -X POST http://localhost:11434/api/embed \
  -d '{"model":"bge-m3:latest","input":"test"}' | jq '.embeddings[0] | length'
# Expected: 1024
```

**SQLite WAL:**
```bash
# If reads appear stale after write
PRAGMA wal_checkpoint(FULL);

# Check WAL file size (>10MB = checkpoint not running)
ls -lh .claude/sessions.db-wal
```

**Stop hook timing:**
- Hook timeout = 60s; `mine` must complete within it
- To time a real mine run: `time session-indexer mine <jsonl> --db .claude/sessions.db`
- If Ollama is slow, embedding 100 chunks can hit ~30s; test with `--skip-embed` flag if added
