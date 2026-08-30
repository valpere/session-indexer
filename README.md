# session-indexer

[![CI](https://github.com/valpere/session-indexer/actions/workflows/ci.yml/badge.svg)](https://github.com/valpere/session-indexer/actions/workflows/ci.yml)
[![session-indexer - Semantic search over your own Claude Code session history | Product Hunt](https://api.producthunt.com/widgets/embed-image/v1/featured.svg?post_id=1230655&theme=light)](https://www.producthunt.com/products/session-indexer)

Per-project semantic search over Claude Code session history. Indexes JSONL
transcripts into a per-project SQLite store; retrieves via bge-m3 embeddings
(Ollama) with FTS5 BM25 fallback. Automatically injects relevant past context
at session start.

**Problem it solves:** returning to a project after a week and needing to find
"what did we decide about X" across dozens of past sessions. `session-end`
gives you "where I left off last time"; `session-indexer` gives you "what we
discussed across all history" — by semantic similarity, not grep.

**Scope: single developer, single machine.** This indexes *your own*
individual sessions with Claude Code in a project — not a team's shared
history, not a multi-user store. If you need to share findings with
teammates, that's a conversation/PR/doc, not something this tool does.
See below for why that's a deliberate choice, not a limitation.

**Why not a centralised memory tool?**
[mempalace](https://github.com/MemPalace/mempalace),
[agentmemory](https://github.com/rohitg00/agentmemory), and
[MemMachine](https://github.com/MemMachine/MemMachine) all run on a single
shared backend — mempalace in ChromaDB, agentmemory via an `iii` engine MCP
server, MemMachine via a Neo4j + SQL backend behind a REST server
(self-hosted or their managed cloud; MemMachine does add logical per-tenant
isolation via org/project IDs, unlike the other two). That single-backend
architecture still has one fatal flaw: **if it dies, everything on it dies
at once.** A corrupt ChromaDB index, a crashed MCP server, or an unreachable
MemMachine/Neo4j instance takes down memory for every project and tenant
depending on that instance simultaneously, and recovery is non-trivial.
MemMachine in particular targets multi-tenant SaaS agent products (CRM,
healthcare, finance assistants) — a different problem than a solo dev's
per-project recall tool.

`session-indexer` is per-project and append-only (`.claude/sessions.db` lives
inside the project's `.claude/` dir). The worst failure mode is losing one
project's DB — fully recoverable by re-running `mine` on the available JSONLs,
since `mine` is idempotent. Every project is isolated; nothing you do in one
can break another.

## Prerequisites

- **Go 1.26.6+** — to build the binary
- **Ollama** — for vector embeddings (optional but recommended)
  - Install: [ollama.com/download](https://ollama.com/download) — native packages for macOS, Linux, Windows
  - `ollama pull bge-m3:latest` — 1024-dim multilingual model (EN + UA)
- **jq** — used by hooks and `/recall` for JSON formatting

## Quick Start

```bash
# 1. Build and install the binary
go install ./cmd/session-indexer

# 2. (Optional) Pull the embedding model
ollama pull bge-m3:latest

# 3. Wire the hooks into your project (one-time setup)
#    Copy session-index.sh + session-recall.sh → .claude/hooks/
#    Update .claude/settings.local.json with Stop + SessionStart entries
#    /recall is a user-level skill (~/.claude/skills/session-recall/, symlinked
#    from ~/wrk/common/skills/session-recall/) — no per-project install needed.
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
session-indexer mine    <jsonl-path> --db .claude/sessions.db
session-indexer search  <query>      --db .claude/sessions.db [--limit N] [--json]
session-indexer embed                --db .claude/sessions.db
session-indexer stats                --db .claude/sessions.db
session-indexer distill              --db .claude/sessions.db [--threshold 0.7] [--model <name>]
session-indexer facts search   <query>            --db .claude/sessions.db [--limit N] [--json] [--include-expired]
session-indexer facts get      <id>               --db .claude/sessions.db [--json]
session-indexer facts related  <id>               --db .claude/sessions.db [--json]
session-indexer facts supersede <new-id> <old-id> --db .claude/sessions.db

# Browse without a query
session-indexer sessions             --db .claude/sessions.db [--by-session] [--json]
session-indexer list                 --db .claude/sessions.db [--limit N] [--since D] [--until D] [--role R] [--session ID] [--json]
session-indexer show <chunk-id>      --db .claude/sessions.db [--json]
session-indexer facts list           --db .claude/sessions.db [--limit N] [--since D] [--until D] [--min-confidence F] [--include-expired] [--json]
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

### Facts layer — `distill` and `facts`

A separate, manually-invoked layer that distills durable
subject-predicate-object facts from mined chunks via an LLM call, alongside
the raw-text `search` above. Never runs automatically — not wired into the
Stop hook, no deadline. See ["Facts Layer"](docs/architecture.md#facts-layer)
in the architecture doc for the full design (confidence gate, supersession
safeguards).

```bash
# Extract facts from chunks not yet distilled (idempotent — safe to re-run)
session-indexer distill --db .claude/sessions.db --threshold 0.7
# → Distilled 12 chunks: 5 facts stored, 3 below threshold, 1 superseded

# Query
session-indexer facts search "implementation status" --db .claude/sessions.db
# → [7] session-indexer | has | 33 merged PRs (confidence 0.92)

session-indexer facts get 7 --db .claude/sessions.db
# → shows the fact plus any incoming/outgoing supersedes edges

session-indexer facts related 7 --db .claude/sessions.db

# Manual override (audit/backstop — distill already judges supersession automatically)
session-indexer facts supersede 9 7 --db .claude/sessions.db
```

#### Cost dials — `--context-cap` and `--batch`

Ollama Cloud bills by **GPU-time per call**, not per token, and the default
distill prompt fights both halves of that:

- every call re-sends a `CURRENT KNOWN FACTS` context block — at the default
  cap of 200 facts it's ~14KB, roughly **30× the average ~470-char chunk**
  it's there to judge supersession against;
- each pending chunk is its own call, so a heavy coding day across several
  projects multiplies into thousands of billable calls.

Two flags address the two multipliers. Both default to the original
behavior, so nothing changes unless you opt in:

| Flag | Default | What it does | Effect |
|------|---------|--------------|--------|
| `--context-cap` | 200 | max current facts fed to the model for supersession judgment | smaller cap = smaller prompt = cheaper per call |
| `--batch` | 1 | pending chunks per Ollama call (numbered-chunks prompt, per-fact `chunk_index` attribution) | batch N ≈ N× fewer calls |

```bash
# The nightly scheduled-maintenance invocation (what session-maintain.sh runs):
session-indexer distill --db .claude/sessions.db \
    --concurrency 4 --context-cap 30 --batch 4
# → distill: 8/8 chunks (81 facts, 0 below threshold, 0 failed) [2m55s]
#   Distilled 8 chunks: 81 facts stored, 0 below threshold, 0 superseded
# (8 chunks consumed in 2 Ollama calls — two full batches of 4)

# Smoke-test the dials on a few chunks before committing to them:
session-indexer distill --db .claude/sessions.db --context-cap 30 --batch 4 --limit 8
```

A batch retries, fails, and gets marked distilled **as a unit** — the whole
batch is left pending if its call errors or a fact insert fails, so nothing
is silently dropped and the next run picks it up. A `--batch 1` call sends
the byte-identical pre-batching prompt.

**Dialing back:** if distilled-fact quality regresses (supersession misses on
older facts, garbage/noisy facts), move `--batch` toward 1 and `--context-cap`
toward 200; the defaults are exactly the pre-flag behavior. The 30/4
combination above cut a measured peak of 80% of a weekly Ollama Pro quota
(call volume, not per-call price) to a modeled ~10–15%.

## Browsing without a query

`search` and `facts search` both need a query term; `stats` gives aggregates
only. `sessions`, `list`, `show`, and `facts list` fill the gap — enumerate
what's actually in the store, and close the provenance loop from a fact back
to the text it was distilled from:

```bash
# What's in the store, by day (the default — see why below)
session-indexer sessions --db .claude/sessions.db
# → 2026-08-25    43 chunks  (43 embedded, 12 distilled, 2 sessions)
#   2026-08-24   156 chunks  (156 embedded, 89 distilled, 1 sessions)

# Raw session_id rollup instead — usually far more skewed, see below
session-indexer sessions --db .claude/sessions.db --by-session

# Recent chunks, newest first, with filters
session-indexer list --db .claude/sessions.db --limit 5 --since 2026-08-20 --role assistant

# Close the provenance loop: a fact's source_chunk_id, then the chunk itself
session-indexer facts get 7 --db .claude/sessions.db
# → source_chunk_id: 135
session-indexer show 135 --db .claude/sessions.db
# → full chunk text, plus every fact distilled from it (including 7)

# Facts without a query term
session-indexer facts list --db .claude/sessions.db --limit 10 --min-confidence 0.9
```

`sessions` defaults to grouping by `session_date`, not `session_id`. In
practice `session_id` is a poor browse axis: `--resume` keeps the same
`sessionId` alive for months, so a handful of ids end up owning most chunks
while the rest own a handful each — `--by-session` shows this raw view when
you want it, but `session_date` gives evenly-sized, recall-shaped buckets by
default. No index exists on `session_date`; a full scan is single-digit
milliseconds even on a store with tens of thousands of chunks, so adding one
isn't worth another schema bump.

## Embeddings

Requires Ollama on `localhost:11434` with `bge-m3:latest`. Override with
environment variables:

| Variable | Default | Description |
|---|---|---|
| `OLLAMA_HOST` | `http://localhost:11434` | Ollama base URL (scheme optional: `localhost:11434` works) |
| `OLLAMA_MODEL` | `bge-m3:latest` | Embedding model name |
| `OLLAMA_DISTILL_MODEL` | `glm-5.2:cloud` | Chat/generate model used by `distill` — distinct from `OLLAMA_MODEL`, must be pulled separately (`ollama pull glm-5.2:cloud` or your chosen model). Override per-invocation with `distill --model <name>` (wins over the env var). |

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

## FAQ

**Why isn't `.claude/sessions.db` committed to the repo? Won't I lose it?**

`.claude/sessions.db` is gitignored on purpose. Two independent reasons come
up when people ask this, and it's worth separating them:

- *"Should the whole team see this DB?"* — No. `session-indexer` indexes
  your own individual sessions with Claude Code, not a team-shared history
  (see "Scope" above). Committing it to the project repo would put one
  person's session log in front of every collaborator on every PR, for no
  benefit — nobody else's Claude Code instance reads or needs it. Adding
  it to git also means merge conflicts on every commit touching the DB,
  since SQLite files aren't line-mergeable.
- *"But what if I lose the machine / disk?"* — That's a real, separate
  concern: backup, not team sharing. The DB is fully rebuildable from your
  local JSONL transcripts (`session-indexer mine` is idempotent — see
  UC-8 in [`docs/use-cases.md`](docs/use-cases.md)), so the worst case is
  re-mining, not permanent loss, as long as the transcripts themselves
  survive. If you want the DB backed up beyond that, the right place is
  your own personal dotfiles/backup tooling (e.g. a private dotfiles repo,
  Time Machine, restic) — *not* the project repo, since that would
  reintroduce the team-visibility and merge-conflict problems above for a
  file only you need. Whether to back it up at all is entirely your call;
  the tool takes no position on it beyond keeping it out of the shared repo.

**What about git worktrees — does each worktree get its own DB?**

Currently, yes: `.claude/sessions.db` resolves relative to whichever
checkout Claude Code is running from, and Claude Code uses linked git
worktrees by default for isolated work. Since `.claude/sessions.db` is
gitignored, a linked worktree doesn't see the main checkout's existing DB
and starts indexing independently, splitting your session history across
worktrees rather than sharing it. This is a known limitation, not
intentional design (per-project isolation is intentional — see
[`docs/requirements.md`](docs/requirements.md) FR-3 — worktree splitting
within one project isn't). If this affects your workflow, open an issue.

**How do I see what's actually in the index, without already knowing what to search for?**

`stats` gives aggregates and `search`/`facts search` both require a query
term — neither helps when you just want to look around, especially on a
project with a long history, before trusting what gets auto-injected at
session start. Four verbs close that gap:

```bash
session-indexer sessions --db .claude/sessions.db
# 2026-08-26    268 chunks  (268 embedded, 0 distilled, 1 sessions)
# 2026-08-25    430 chunks  (430 embedded, 0 distilled, 2 sessions)
# ...

session-indexer list --db .claude/sessions.db --limit 5 --since 2026-08-20

session-indexer show 135 --db .claude/sessions.db
# [chunk 135 | 2026-06-25 | assistant]
# <full text>
# Facts distilled from this chunk: 2
#   [1] Task 6: Mine orchestrator | is implemented in | ...
```

`show` is the one that actually matters: every distilled fact already
carries a `source_chunk_id`, but before this existed there was no way to
look at the chunk it pointed to — `facts get 7` reported an id with
nowhere to go. `show` closes that loop, which is the real trust-building
step, not just a listing.

`sessions` defaults to grouping by day rather than by the raw session id
— with `--resume` a single session id can stay alive for months, so
day-grouping is the more honest view of what's actually in the store on a
long-running project (`--by-session` gives the raw id-based rollup when
you want that view instead). See ["Browsing"](docs/architecture.md#browsing)
in the architecture doc for the full design rationale.

## Querying facts (discipline)

The facts layer is a supersedable claim store, not a flat lookup table — a
matching search hit is not automatically the current truth. For any
non-trivial answer drawn from `facts`, follow all four steps before citing
a fact:

1. **`facts search <query>`** — find candidate facts.
2. **`facts get <id>`** — read the fact plus its supersedes edges.
3. **`facts related <id>`** — check for an *incoming* supersedes edge (a
   newer fact that replaced this one). If present, jump to the newer fact
   and repeat from step 2.
4. **Check `until`** — a non-null `until` means the fact is tombstoned;
   don't cite it as current (it's still visible via `--include-expired`
   for historical context, but never as present-tense truth).

**Anti-pattern:** answering after step 1 alone. `facts search` ranks by
keyword match, not recency or validity — a stale, superseded fact can
easily outrank its replacement on pure BM25 score if it happens to phrase
the query terms more directly. Skipping steps 2–4 is exactly how a
distilled-but-superseded fact (e.g. an old "implementation not started"
claim) gets cited as current truth — the same class of drift this feature
exists to catch.

## Troubleshooting

**Hooks not running:**
Check that both commands are in the same `Stop` entry's `hooks` array (not two
separate top-level `Stop` entries). See Hook Setup above.

**Schema version mismatch:**
```
schema version mismatch (X != Y): delete .claude/sessions.db and re-mine to rebuild
```
Default path: delete the DB and re-run `mine` on your JSONLs — `mine` is
idempotent. **But check first** if this is an established project: Claude
Code prunes old JSONL transcripts after a while, so a long-running DB can
hold more history than re-mining could actually reconstruct. If your
current version bumped from schema 2 to 3, an in-place migration that
loses nothing is available — see
[`docs/migrations/schema-v2-to-v3.md`](docs/migrations/schema-v2-to-v3.md).

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

## Versioning & Releases

[SemVer](https://semver.org/), with one project rule: any release that
bumps `internal/db.SchemaVersion` gets at least a MINOR bump, never a
PATCH — treat a MINOR release as potentially requiring a DB migration
until `v1.0.0` (not yet reached). See [`docs/versioning.md`](docs/versioning.md)
for the full policy (branching, release cutting) and
[`CHANGELOG.md`](CHANGELOG.md) for what changed in each release.
Pre-built binaries for Linux/macOS/Windows are attached to each
[GitHub Release](https://github.com/valpere/session-indexer/releases).
