---
name: session-recall
description: "Per-project semantic session history. Indexes Claude session transcripts into a local SQLite DB and injects relevant past context at SessionStart. Replaces mempalace with per-project isolation. Usage: /recall <query>"
---

# /recall

Searches past sessions for this project using semantic similarity (bge-m3) or
BM25 keyword fallback. Requires `session-indexer` in PATH and a local
`.claude/sessions.db` (built by the Stop hook after each session).

```
/recall <query>          → search and display matching session chunks
/recall stats            → show index state (sessions, chunks, embeddings)
```

---

## /recall \<query\>

```bash
PROJECT_ROOT=$(git rev-parse --show-toplevel 2>/dev/null || echo "$PWD")
DB="$PROJECT_ROOT/.claude/sessions.db"

if [[ ! -f "$DB" ]]; then
  echo "No session index found at $DB"
  echo "The Stop hook (session-index.sh) builds it automatically at session end."
  echo "Run a session and exit to populate the index, then retry."
  exit 0
fi

if ! command -v session-indexer >/dev/null 2>&1; then
  echo "session-indexer not in PATH. See /generate-session-recall for install instructions."
  exit 0
fi

QUERY="$*"
RESULTS=$(session-indexer search "$QUERY" --db "$DB" --limit 10 --json)
printf '%s' "$RESULTS" | jq -r '
  if length == 0 then "No results."
  else
    "\(length) result(s):\n",
    (to_entries[] |
      "[\(.key + 1)] \(.value.SessionDate) · \(.value.Role)\(
        if (.value.Content | test("^(Bash|Read|Write|Edit|Glob|Grep|WebFetch|WebSearch|Agent|Task)\\s*\\{"))
        then " [tool]" else "" end
      ) · score=\(.value.Score | tostring | .[0:5])",
      "    \(.value.Content[0:400])\(if (.value.Content | length) > 400 then "..." else "" end)",
      ""
    )
  end
'
```

---

## /recall stats

```bash
PROJECT_ROOT=$(git rev-parse --show-toplevel 2>/dev/null || echo "$PWD")
DB="$PROJECT_ROOT/.claude/sessions.db"

if [[ ! -f "$DB" ]]; then
  echo "No session index yet."
  exit 0
fi

session-indexer stats --db "$DB"
```

---

## How the full system works

```
Session ends (exit / /exit)
  ├─ session-end.sh    — writes LLM summary to .claude/session-log.md
  └─ session-index.sh  — mines JSONL transcript → .claude/sessions.db (append-only)

Next session opens
  ├─ session-last.sh   — injects last session-log.md entry (structured summary)
  └─ session-recall.sh — searches sessions.db by branch+commit context (semantic)
```

Both SessionStart hooks fire together — `session-last` gives "what we did last
time", `session-recall` gives "what's relevant to current work" across all history.

**Database**: `.claude/sessions.db` — per-project SQLite, gitignored, append-only.
Each session adds chunks; nothing is ever overwritten. This is the key difference
from mempalace (centralised mutable store that corrupted history).

**Search**: bge-m3 vector embeddings when available (requires Ollama + bge-m3 pull),
automatic BM25 FTS5 fallback when embeddings are absent.

**Troubleshooting**:
```bash
# Check hook logs
tail -30 ~/.cache/$(basename "$(git rev-parse --show-toplevel 2>/dev/null || echo "$PWD")")/hooks.log

# Manual index check
session-indexer stats --db .claude/sessions.db

# Backfill embeddings (if Ollama + bge-m3 available)
session-indexer embed --db .claude/sessions.db
```

---

## For orchestrators / subagent prep

Subagents spawned via the Agent tool start cold — no SessionStart hook, no
shared context, no awareness of this project's `.claude/sessions.db`. If a
subagent's task would benefit from past decisions or discussions, query
history yourself *before* spawning it, then fold the relevant results into
the subagent's prompt (subagent prompts must be self-contained).

```bash
PROJECT_ROOT=$(git rev-parse --show-toplevel 2>/dev/null || echo "$PWD")
DB="$PROJECT_ROOT/.claude/sessions.db"

[[ -f "$DB" ]] && command -v session-indexer >/dev/null 2>&1 || exit 0

session-indexer search "<query>" --db "$DB" --limit 5 --json | jq -r '
  .[] | "[\(.SessionDate) · \(.Role)] \(.Content[0:300])"
'
```

Limitations:
- Subagent tool allowlists (`bug-fixer`, `code-generator`, `tech-lead`, etc.)
  don't include the `Skill` tool — a spawned subagent can't invoke `/recall`
  or this skill itself mid-task. This section is for the orchestrator to run
  *before* spawning, not for the subagent to run on its own.
  Same root cause as the general agent/skill boundary documented in
  `~/wrk/common/skills/README.md`'s design principles — see that note
  before assuming any skill will auto-trigger inside a subagent.
- No tool-call noise filtering here (unlike `/recall <query>` and
  `session-recall.sh`) — the orchestrator curates what goes into the
  subagent prompt anyway; filter manually if a query pulls in noise.
