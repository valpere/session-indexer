# session-indexer

[![CI](https://github.com/valpere/session-indexer/actions/workflows/ci.yml/badge.svg)](https://github.com/valpere/session-indexer/actions/workflows/ci.yml)

Per-project semantic search over Claude Code session history. Indexes JSONL
transcripts into a per-project SQLite store; retrieves via bge-m3 embeddings
(Ollama) with FTS5 BM25 fallback. Automatically injects relevant past context
at session start.

**Problem it solves:** returning to a project after a week and needing to find
"what did we decide about X" across dozens of past sessions. `session-end`
gives you "where I left off last time"; `session-indexer` gives you "what we
discussed across all history" — by semantic similarity, not grep.

**Why not mempalace?** Mempalace uses a centralised mutable store shared across
all projects. A single corruption wipes history for every project. `session-indexer`
is append-only and per-project (`.claude/sessions.db`) — the worst that can
happen is losing one project's DB, which is fully recoverable by re-mining the
available JSONLs (`mine` is idempotent).

## Prerequisites

- **Go 1.26+** — to build the binary
- **Ollama** — for vector embeddings (optional but recommended)
  - `ollama pull bge-m3:latest` — 1024-dim multilingual model (EN + UA)
- **jq** — used by the SessionStart hook to format the hook output JSON
- **python3** — used by the `/recall` skill and `session-recall.sh` for output formatting

## Quick Start

```bash
# 1. Build and install the binary
go install ./cmd/session-indexer

# 2. (Optional) Pull the embedding model
ollama pull bge-m3:latest

# 3. Wire the hooks into your project (one-time setup)
#    Copy session-index.sh + session-recall.sh → .claude/hooks/
#    Update .claude/settings.local.json with Stop + SessionStart entries
#    Install /recall skill → .claude/skills/session-recall/SKILL.md
#    See "Hook Setup" below for the exact steps.

# 4. End a Claude Code session — Stop hook mines it into .claude/sessions.db
#    (The hook silently no-ops until session-indexer is in PATH)

# 5. Open a new session — SessionStart hook injects relevant past context
#    automatically based on current git branch + recent commits

# 6. Search manually at any time
session-indexer search "config validation approach" --db .claude/sessions.db
# or from inside Claude Code:
# /recall config validation approach
```

## Build

```bash
go build -o bin/session-indexer ./cmd/session-indexer
go install ./cmd/session-indexer   # to PATH (activates the Stop hook guard)
```

## Usage

```bash
session-indexer mine   <jsonl-path> --db .claude/sessions.db
session-indexer search <query>      --db .claude/sessions.db [--limit N] [--json]
session-indexer embed               --db .claude/sessions.db
session-indexer stats               --db .claude/sessions.db
```

### `mine` output

```
mined: 23 chunks inserted, 21 embedded, 0 skipped, 2 deferred
```

- **inserted** — new chunks stored (duplicates skipped via INSERT OR IGNORE)
- **embedded** — chunks that got a vector embedding from Ollama
- **skipped** — embed errors (Ollama returned an error); stored in DB, no embedding, backfill via `embed`
- **deferred** — embed deadline hit (50s ctx timeout); stored in DB, no embedding, same backfill path

### `search --json` output schema

```json
[
  {
    "SessionDate": "2026-06-10",
    "Role":        "user",
    "Content":     "We decided to use a ring buffer for the event queue…",
    "Score":       0.847
  }
]
```

`Score` is cosine similarity (0–1) in embedding mode, or negated BM25 rank in
FTS5 fallback mode (higher is always better in both cases).

## Embeddings

Requires Ollama on `localhost:11434` with `bge-m3:latest`. Override with
environment variables:

| Variable | Default | Description |
|---|---|---|
| `OLLAMA_HOST` | `http://localhost:11434` | Ollama base URL (scheme optional: `localhost:11434` works) |
| `OLLAMA_MODEL` | `bge-m3:latest` | Embedding model name |

`mine` runs with a 50s `context.Context` deadline (headroom under the 60s
Stop-hook budget): storing is fast and unconditional; embedding respects the
deadline. Chunks past the deadline are stored but `Deferred` (no embedding row);
backfill with `session-indexer embed`. Embed errors count as `Skipped` — same
storage state, same backfill path, different cause.

When Ollama is unavailable or the store has zero embeddings, `search` falls back
to FTS5 BM25 with per-term OR recall and notes this in the output.

## Hook Setup

Two Stop hooks run on every session end (wired in a single `Stop` entry of
`settings.local.json` — Claude Code 2.1.x runs only the first top-level entry):

```json
{
  "hooks": {
    "Stop": [
      {
        "hooks": [
          { "type": "command", "command": "bash .claude/hooks/session-end.sh",   "timeout": 60 },
          { "type": "command", "command": "bash .claude/hooks/session-index.sh", "timeout": 60 }
        ]
      }
    ],
    "SessionStart": [
      {
        "hooks": [
          { "type": "command", "command": "bash .claude/hooks/session-last.sh",   "timeout": 10 },
          { "type": "command", "command": "bash .claude/hooks/session-recall.sh", "timeout": 15 }
        ]
      }
    ]
  }
}
```

`session-index.sh` silently no-ops until `session-indexer` is on PATH.
`session-recall.sh` no-ops until `.claude/sessions.db` exists (after the first indexed session).

Hook logs go to `~/.cache/<project-name>/hooks.log`.

## Troubleshooting

**Hooks not running:**
Check that both commands are in the same `Stop` entry's `hooks` array (not two
separate top-level `Stop` entries). See Hook Setup above.

**Schema version mismatch:**
```
schema version mismatch (X != Y): delete .claude/sessions.db and re-mine to rebuild
```
Delete the DB and re-run `mine` on your JSONLs — `mine` is idempotent.

**Search returns poor results / FTS5 fallback:**
```bash
session-indexer stats --db .claude/sessions.db   # check pending count
session-indexer embed --db .claude/sessions.db   # backfill embeddings
```

**Search warns "N chunks not yet embedded — results may be incomplete":**
Some chunks are stored but have no embedding (interrupted mine, Ollama was
down, or deadline hit). Cosine search only ranks embedded chunks — unembedded
ones are invisible until backfilled. FTS5 fallback only activates when zero
embeddings exist, not for a partial store. Fix: run `session-indexer embed`.

**Read hook logs:**
```bash
tail -40 ~/.cache/$(basename "$(git rev-parse --show-toplevel)")/hooks.log
```

**DB size:** scale assumption is <10k chunks (~40MB vectors in memory). No hard
limit, but `search` loads all embedding rows into memory for cosine; if the DB
grows beyond ~50k chunks, revisit.
