# Problem Brief: Per-Project Semantic Session Recall

**For:** Agent assigned to design and implement per-project semantic search
over Claude Code conversation history.

**Context owner:** Valentyn Solomko (Val), CTO / solo engineer on personal
projects at `~/wrk/projects/`. Projects use Claude Code heavily; sessions
are long and context-rich.

---

## The Problem

Val switches between multiple projects. When returning after days or weeks,
he can't recall what was discussed in previous sessions. Two distinct needs:

### Need 1 — "Where did I stop?" (SOLVED)

**Status: implemented** via `session-end` hook + `/session-end` skill.

A Stop hook writes `.claude/last-session.md` (structured summary: what was
done, current state, open questions, next steps). A SessionStart hook injects
it into the next session. See `docs/session-recall-setup.md` in vmm-rada.

### Need 2 — "I vaguely remember we discussed X" (UNSOLVED)

User wants to search past sessions for something specific they half-remember
but can't precisely word. Example: "we talked about some config validation
approach a few weeks ago — what was it?"

This requires **semantic search** over conversation history — not full-text
grep (you don't remember the exact words), not the current session summary
(the memory is older than one session).

---

## What Was Tried

### mempalace (Python + ChromaDB + HNSW)

**Architecture:** indexes Claude Code session JSONL files into a ChromaDB
vector store. Provides an MCP server with `mempalace_search` tool for
semantic queries.

**Failure modes discovered:**
1. **HNSW corruption bug**: `quarantine_invalid_hnsw_metadata()` in
   `chroma.py` triggers when `index_metadata.pickle` has `dimensionality=None`
   but vectors exist. Happens on every MCP server restart after `repair`.
   Workaround: manually patch pickle to set `dimensionality=384`. Fragile.
2. **Missing Stop hook**: sessions aren't auto-mined unless Stop hook is
   configured. Easy to miss on new projects.
3. **JSONL files get deleted** by Claude Code's own cleanup after sessions
   end. By the time mempalace tries to mine them, they may be gone.
4. **Centralized palace**: all projects share one ChromaDB instance. One
   corruption event destroys vectors for all projects simultaneously.

**Verdict:** architecture is correct (semantic search over sessions), but
implementation is fragile. Not suitable as a reliable production tool.

### agentmemory (TypeScript + SQLite + iii engine)

- 24K stars, actively maintained
- Uses SQLite instead of ChromaDB → no HNSW files → no binary corruption
- In-memory vector index rebuilt from SQLite on startup → survive restarts
- Project namespace isolation via `project` parameter

**But:** still centralized (one server for all projects, 4 required ports).
If the server fails, ALL projects lose recall simultaneously. Val explicitly
rejected centralized solutions: "centralized solution centralizes failure."

**Verdict:** better implementation, same architectural flaw.

---

## Requirements for the Solution

1. **Per-project isolation.** Failure in one project's recall store must not
   affect any other project. Ideally: data lives inside the project directory.
2. **No external server.** No daemon to start/stop, no port binding, no
   process management. Must work offline.
3. **Reliable storage.** No binary index files that silently corrupt. SQLite
   is acceptable (WAL mode, atomic writes). Flat files are fine.
4. **Semantic/conceptual search.** User remembers the idea, not the exact
   words. Keyword grep is insufficient. Need embedding-based similarity.
5. **Claude Code integration.** Should work as an MCP tool, a skill, or a
   bash command Val can pipe into.
6. **Minimal dependencies.** Something Val can install once and forget.

---

## Ideas to Explore (Not Yet Evaluated)

### A. Per-project SQLite FTS + embeddings

Store conversation chunks in a per-project `~/.local/share/<project>/sessions.db`
with:
- `content` TEXT (the chunk)
- `embedding` BLOB (serialized vector)
- `session_date` TEXT
- FTS5 virtual table for keyword search

Embedding: use a local model (e.g. `nomic-embed-text` via Ollama) or
call the Anthropic API. Query: generate embedding for the query, cosine
similarity over stored vectors.

**Pro:** no external service, per-project file, sqlite is crash-safe
**Con:** need an embedding model; Ollama adds a soft dependency

### B. LLM-based recall (no embeddings)

Store conversation summaries in structured markdown per session
(`.claude/sessions/YYYY-MM-DD-HH.md`). On query, use `claude -p` to
read the last N summaries and find the relevant one.

**Pro:** zero infrastructure, pure text, no embedding model
**Con:** expensive (each query reads many files + LLM call), not semantic
(misses paraphrases), doesn't scale past ~50 sessions

### C. BM25 + periodic re-indexing

Use `sqlite-fts5` BM25 ranking (built into SQLite) over raw session text.
No embeddings. Better than grep but worse than semantic. Index rebuilt
periodically (e.g., Stop hook mines last 10 sessions into local DB).

**Pro:** no embedding model, single SQLite file, fast queries
**Con:** keyword-based — fails "I don't remember the exact term" case

### D. ChromaDB per-project (embedded mode)

ChromaDB supports embedded (in-process, no server) mode with a file path.
Each project gets its own `~/.local/share/<project>/chroma/` directory.
Failure is isolated.

**Pro:** same semantic quality as mempalace, no server
**Con:** still has HNSW files (same corruption risk); Python dependency

### E. Qdrant embedded (Rust, via FFI or subprocess)

Qdrant has a `qdrant-client` with embedded/in-memory mode. Potentially
more reliable than ChromaDB's HNSW.

**Status:** needs investigation — whether Rust embedded mode is available
without a Qdrant server binary.

### F. Fix mempalace per-project

Run a separate mempalace instance per project (different port, different
palace directory). Failure is isolated. The HNSW corruption bug would still
need a fix, but at least it wouldn't take down all projects.

**Status:** unclear if mempalace supports per-instance configuration.

---

## Key Constraints

- Val's projects are at `~/wrk/projects/<name>/<repo>/` (personal) and
  `~/wrk/oblabz/projects/<name>/<repo>/` (team, OB Labz)
- Go is preferred for tools; Python acceptable for one-off processing;
  **no Python for user-facing scripts/agents** (per global CLAUDE.md)
- Bash + SQLite + Ollama are all available on Val's machine
- `claude` CLI is available (can use `claude -p` for LLM calls)
- Sessions are in `~/.claude/projects/-<path-hash>/*.jsonl` — deleted after
  some time by Claude Code. Must mine BEFORE deletion.

---

## Suggested Starting Point

The most promising approach for the agent to evaluate first:

**Approach A (SQLite FTS + embeddings) with Ollama for local embeddings.**

Rationale:
- SQLite is battle-tested, crash-safe, per-project
- Ollama is already likely on Val's machine (used for other projects)
- `nomic-embed-text` is fast, free, runs offline
- FTS5 provides keyword fallback if embeddings are unavailable
- A simple Go binary (`session-indexer`) could handle indexing + querying
- A Stop hook mines the session before JSONL deletion

Implementation sketch:
1. Stop hook calls `session-indexer mine <jsonl-path> --db <project>/.sessions.db`
2. The indexer chunks messages, generates embeddings via Ollama, stores in SQLite
3. Query: `session-indexer search "config validation approach" --db .sessions.db`
4. Wired as a Claude Code skill: `/recall <query>`

The DB file (`.sessions.db`) would be gitignored, per-project, and
replaceable without data loss if corrupted (just re-mine from JSOLs while
they still exist).
