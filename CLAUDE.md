# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

Build/test/lint/format via the Makefile (`make build|install|test|test-race|test-pkg PKG=...|vet|fmt|clean`); see `Makefile`. `make fmt` is mutating (`gofmt -w .`). `make eval` runs the retrieval-quality harness (needs live Ollama + bge-m3 and the LoCoMo file; deliberately not part of `make test`).

## Architecture

Single Go binary, no daemon, no shared state between projects. Cobra subcommands (`facts` has several child verbs):

```
session-indexer mine    <jsonl-path> --db <path>        # parse JSONL → SQLite
session-indexer search  <query>      --db <path> [--limit N] [--json]
session-indexer embed                --db <path>        # backfill missing embeddings
session-indexer stats                --db <path>        # sessions, chunks, facts, pending, DB size
session-indexer distill              --db <path> [--threshold 0.7] [--context-cap 200] [--batch 1]
                                                                    # LLM-extract facts, manual, no deadline;
                                                                    # context-cap/batch are Ollama cost dials (see Facts Layer)
session-indexer facts search/list/get/related/supersede --db <path>  # query the facts layer
session-indexer sessions             --db <path> [--by-session]      # roll up by day (default) or session_id
session-indexer list                 --db <path> [--limit N] [--since D] [--until D] [--role R] [--session ID]
session-indexer show    <chunk-id>   --db <path>        # full chunk text + facts distilled from it
session-indexer tree    <jsonl-path> [--json]           # agent/subagent/tool-call tree of one transcript (no --db)
```

### File Layout

One package per concern under `internal/` (`db`, `mine`, `embed`, `search`, `distill`, `facts`, `browse`, `tree`); `cmd/session-indexer/main.go` is the Cobra root. Shared row types live in `internal/types.go`; see `docs/architecture.md` for the per-file layout.

### Storage

SQLite at `<project-root>/.claude/sessions.db` (gitignored, per-project). No CGO — use `modernc.org/sqlite`. Open with WAL + `synchronous=NORMAL` + `busy_timeout=5000`.

Schema version stored in `meta` table (`key='schema_version'`). On open: if version mismatches, exit with "schema version mismatch (X != Y): delete <db-path> and re-mine to rebuild". There is no `reindex` subcommand; users recover by deleting the DB and re-mining available JSONLs. Never silently evolve schema.

Dedup key: `UNIQUE(session_id, message_index, chunk_index)` — `INSERT OR IGNORE` makes `mine` idempotent.

`embeddings` rows carry a `model` column, tagged `"<provider>:<model>"` (e.g. `ollama:bge-m3:latest`) via `embed.ModelTag`. Search filters to the currently configured model's rows only and warns (doesn't silently mis-score) when other-model rows exist — `cosine()` can't tell "genuinely dissimilar" from "wrong dimensionality." `embed.ModelTag`/`ChunksNeedingEmbedding` (`internal/db/db.go`) mean switching the embedding model or provider no longer requires a DB wipe: `session-indexer embed` re-embeds the foreign-model rows in place.

### JSONL Parsing

Extract `user` and `assistant` turns where `isMeta=false`. Skip XML/HTML (`<`), slash commands (`/\w+`), and content <30 chars after stripping. Truncate any single tool block to 2KB. Chunk messages at 1500 chars on paragraph boundaries.

### Embeddings

Ollama REST: `POST localhost:11434/api/embed`, model `bge-m3:latest` (1024 dims). Override with `OLLAMA_HOST` (URL or `host:port`) and `OLLAMA_MODEL` env vars. Probe first with `GET /api/tags` (2s timeout) — if unavailable, skip embeddings and log a warning. Store as `encoding/binary` LittleEndian float32 BLOB.

`mine` runs with a 50s `context.Context` deadline (headroom under the 60s Stop-hook budget). Storing is fast and unconditional; embedding is the phase that respects the deadline. Chunks past the deadline are stored but flagged `Deferred` in the `Result` and left without an embedding row — backfill with `session-indexer embed`. Embed errors never abort the mine (counted as `Skipped`).

### Search

Primary: embed query → exhaustive cosine over all `embeddings` rows loaded into memory. Scale assumption: <10k chunks. Fallback to FTS5 BM25 when Ollama is unavailable **OR** the store has zero embeddings (e.g. mined while Ollama was down); FTS uses per-term OR recall, not phrase match. Output notes when the fallback is used.

### Retrieval eval

`internal/eval` + `cmd/session-indexer-eval` (separate binary, not a subcommand) measure Recall@{1,3,5,10} / MRR / latency / context size of `internal/search` on the public LoCoMo benchmark, deterministically (the dataset labels evidence turns; no LLM judge, no cloud calls). Modes: cosine, bm25 (FTS fallback), naive keyword baseline, hybrid (RRF of cosine + BM25, harness-only), and `*-mine` (through the real mine filters). Per-conversation stores in `tmp/eval/db/` double as the embedding cache; dataset stays in gitignored `tmp/eval/data/` (LoCoMo is CC BY-NC 4.0 — never commit or redistribute). `--real-db <store>` runs the same metrics on a real session store (facts as queries, source chunks as evidence; snapshot copy, aggregate output only); `--queries <file>` swaps in recall-style queries written by an LLM via `agy` (`--gen-queries N`; chunk text is sent to the generator, secrets/PII are filtered first). Results and caveats: `docs/retrieval-eval.md`.

### Browsing and Tree

`sessions`/`list`/`show` (`internal/browse`) are query-free reads, kept out of `internal/search` (ranked retrieval only); `sessions` groups by `session_date`, not `session_id`; no `session_date` index (measured, not needed). `tree <jsonl-path>` (`internal/tree`) is a read-only structural view of one transcript, separate from `internal/mine`'s flattening parser; it reads no DB. Design and join rules: `docs/architecture.md` ("Browsing", "Tree").

### Facts Layer

Distilled subject-predicate-object claims, separate from raw-text `search`. `distill` (manual, never hooked into `mine`/Stop-hook budget — `context.Background()`, 120s HTTP timeout) calls Ollama `/api/generate` (`OLLAMA_DISTILL_MODEL` env or `--model` flag, default `glm-5.2:cloud`) for chunks not yet in `distilled_chunks` — `--batch N` (default 1) groups N chunks into one numbered-chunks call with per-fact `chunk_index` attribution (Ollama Cloud bills per call) — feeding it a bounded `CurrentFacts` context (`--context-cap`, default 200) for supersession judgment.

Confidence gate is a **deterministic Go check** (default 0.7), not an LLM-enforced instruction — the model's self-reported confidence is advisory input only. Supersession is judged automatically by the model, but the model may only cite fact ids from the context it was actually given (validated in Go); `SupersedeFact` no-ops (not an error) on an already-tombstoned fact. `facts supersede <new> <old>` is a manual audit/override backstop using the same function. See `docs/architecture.md`'s "Facts Layer" section for the full design rationale (deliberate deviations from litopys).

Tombstone resolution is filter-based (`WHERE until IS NULL`), not chain-walking. `facts get`/`facts related` are depth-1 only — no BFS.

## Key Constraints

- **Pure Go, no CGO.** `go build` must produce a portable static binary.
- **Go 1.26.9+.** Use `go 1.26.9` in `go.mod` (patch-pinned, and the pin moves: re-pin whenever a stdlib CVE reachable via `internal/distill`'s HTTP client is disclosed. `1.26.5` had four such CVEs: GO-2026-6218, GO-2026-6090, GO-2026-5972, GO-2026-5026, all fixed in `1.26.6`; GO-2026-6617 and GO-2026-6613 (net/http) are fixed in `1.26.9`; see issue #31 for the prior GO-2026-5856 precedent that established this patch-pinning practice).
- **60-second budget.** `mine` runs inside a Claude Code Stop hook. Must complete well within 60s (enforced internally via a 50s `context.Context` deadline — see Embeddings).
- **Mixed Ukrainian + English content.** `bge-m3` handles both; `unicode61 remove_diacritics 0` tokenizer for FTS5.
- **Per-project isolation.** No cross-project search, no shared state.

## Stop Hook Integration

`.claude/settings.local.json` wires two Stop-hook commands into a single `Stop` entry: `bash .claude/hooks/session-end.sh` (writes `session-log.md`) and `bash .claude/hooks/session-index.sh` (runs `session-indexer mine <transcript_path> --db <project-root>/.claude/sessions.db`). The hook fires with JSON on stdin containing `transcript_path` and `session_id`. JSONL files may be deleted by Claude Code cleanup shortly after — mine at hook time. The `session-index.sh` guard silently no-ops until `session-indexer` is on `PATH`.

> Note: in Claude Code 2.1.x, multiple **top-level** `Stop` array entries are not all fired — only the first runs. Put multiple Stop commands in the **same** entry's `hooks` array (the structure used here).

## Before Any Commit on a Feature/Chore Branch

There is no dedicated "commit" skill in this project — ad hoc commit
requests bypass `/ship`'s structural freshness check (it always branches
from an updated `main`). Before committing directly onto the current
branch:

1. Check whether the current branch already has a merged PR: `gh pr list --state merged --search "head:<branch-name>"`.
2. If yes, the branch is stale — do **not** commit onto it. Instead: `git checkout main && git pull --ff-only && git checkout -b <new-branch-name>` before making any new commit.
3. If the current branch has no merged PR yet (still open, or never pushed), committing onto it is fine as-is.
