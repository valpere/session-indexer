#!/bin/bash
# SessionStart hook: injects semantically relevant past session context.
# Derives a search query from git branch + recent commits, searches the
# per-project session index (.claude/sessions.db), and injects matching
# chunks as additionalContext.
#
# Complements session-last.sh (which injects the last structured entry).
# This hook adds semantic recall across all indexed sessions.

set -uo pipefail

source "$(dirname "$0")/_lib/hook-common.sh"
hook_setup_logging "session-recall.sh"

if ! command -v session-indexer >/dev/null 2>&1; then
    echo "[$(date -Iseconds)] session-recall: session-indexer not in PATH, skipping" >> "$LOG_FILE"
    exit 0
fi

PROJECT_ROOT=$(git rev-parse --show-toplevel 2>/dev/null || echo "$PWD")
DB="$PROJECT_ROOT/.claude/sessions.db"

if [[ ! -f "$DB" ]]; then
    echo "[$(date -Iseconds)] session-recall: no sessions.db yet, skipping" >> "$LOG_FILE"
    exit 0
fi

# Derive search query from git context (branch name + recent commit messages)
BRANCH=$(git rev-parse --abbrev-ref HEAD 2>/dev/null | sed 's/[_\/-]/ /g' || echo "")
COMMITS=$(git log --oneline -3 2>/dev/null | cut -d' ' -f2- | tr '\n' ' ' || echo "")
QUERY="$(printf '%s %s' "$BRANCH" "$COMMITS" | tr -s ' ' | sed 's/^ //;s/ $//')"

if [[ -z "$QUERY" ]]; then
    echo "[$(date -Iseconds)] session-recall: empty query, skipping" >> "$LOG_FILE"
    exit 0
fi

echo "[$(date -Iseconds)] session-recall: query='${QUERY:0:80}'" >> "$LOG_FILE"

RESULTS=$(session-indexer search "$QUERY" --db "$DB" --limit 5 --json 2>/dev/null || echo "[]")

CONTEXT=$(printf '%s' "$RESULTS" | jq -r '
  map(.Content = ((.Content // "") | gsub("^\\s+|\\s+$"; "")))
  | map(select(
    .Content | test("^(Bash|Read|Write|Edit|Glob|Grep|WebFetch|WebSearch|Agent|Task)\\s*\\{") | not
  ))
  | group_by(.SessionDate // "unknown")
  | map({date: (.[0].SessionDate // "unknown"), chunks: (sort_by(.Score) | reverse | .[0:2])})
  | sort_by(.date) | reverse | .[0:3]
  | .[]
  | "### \(.date)",
    (.chunks[] | "  [\(.Score | tostring | .[0:5])] \(.Content[0:280])\(if (.Content | length) > 280 then "..." else "" end)"),
    ""
' 2>/dev/null | sed 's/[[:space:]]*$//')

if [[ -z "$CONTEXT" ]]; then
    echo "[$(date -Iseconds)] session-recall: no usable results after filtering" >> "$LOG_FILE"
    exit 0
fi

echo "[$(date -Iseconds)] session-recall: injecting context" >> "$LOG_FILE"

jq -n --arg ctx "$CONTEXT" '{
  hookSpecificOutput: {
    hookEventName: "SessionStart",
    additionalContext: ("Relevant past sessions (semantic search):\n\n" + $ctx)
  }
}'
