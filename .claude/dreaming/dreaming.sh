#!/bin/bash
# session-indexer project dreaming pass.
#
# Purpose: scheduled (weekly) curation of THIS project only — read-only,
# outputs a report. Scoped to session-indexer alone (never cross-mines other
# projects).
#
# Schedule: systemd user timer (Sun, spread after the other dreaming
# timers — see ~/wrk/common/dreaming/HOW-TO-APPLY.md; Persistent=true
# catches up at next login if the machine was off).

set -euo pipefail

# Ensure claude/gh/jq are reachable when invoked from a minimal systemd env
export PATH="$HOME/.local/bin:/usr/local/bin:/usr/bin:/bin:$PATH"

# Route claude-code through Ollama (localhost:11434, Device Key auth).
# Avoids Anthropic weekly limits — see ~/wrk/common/dreaming/dreaming.sh
# for the rationale and env-vars justification.
export ANTHROPIC_AUTH_TOKEN=ollama
export ANTHROPIC_API_KEY=""
export ANTHROPIC_BASE_URL=http://localhost:11434

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"  # .claude/dreaming -> project root
REPORTS_DIR="$SCRIPT_DIR/reports"
PROMPT_FILE="$SCRIPT_DIR/dreaming-prompt.md"
WEEK="$(date +%Y-W%V)"
REPORT="$REPORTS_DIR/$WEEK.md"
LOG="$REPORTS_DIR/.dreaming.log"

mkdir -p "$REPORTS_DIR"

if [[ ! -f "$PROMPT_FILE" ]]; then
  echo "[dreaming] missing prompt file: $PROMPT_FILE" >&2
  exit 1
fi

if [[ ! -d "$PROJECT_DIR/.git" ]]; then
  echo "[dreaming] not a git repo: $PROJECT_DIR" >&2
  exit 1
fi

echo "[$(date -Iseconds)] session-indexer dreaming pass started" >> "$LOG"

cd "$PROJECT_DIR"

PROMPT="$(cat "$PROMPT_FILE")
Today is $(date -I).
Current branch: $(git rev-parse --abbrev-ref HEAD).
Project root: $PROJECT_DIR.
Write the report to stdout."

# `if` guards the pipeline so `set -e` doesn't abort the script right
# here on a non-zero claude exit — a bare `cmd; EXIT=$?` line under
# errexit never reaches the EXIT=$? assignment; the whole error-handling
# block below it becomes dead code exactly when it's needed.
if echo "$PROMPT" | claude \
  --print \
  --model minimax-m3:cloud \
  --fallback-model kimi-k2.7-code:cloud \
  --allowed-tools "Read,Glob,Grep,Bash(ls:*),Bash(cat:*),Bash(wc:*),Bash(stat:*),Bash(find:*),Bash(git log:*),Bash(git diff:*),Bash(git show:*),Bash(git rev-parse:*),Bash(gh pr list:*),Bash(gh pr view:*),Bash(gh issue list:*),Bash(awk:*),Bash(sort:*),Bash(uniq:*)" \
  > "$REPORT" 2>> "$LOG"; then
  EXIT=0
else
  EXIT=$?
fi
echo "[$(date -Iseconds)] session-indexer dreaming finished (exit=$EXIT, report=$REPORT)" >> "$LOG"

if [[ $EXIT -ne 0 ]]; then
  echo "[dreaming] non-zero exit; check $LOG" >&2
  exit "$EXIT"
fi

SIZE=$(wc -c < "$REPORT")
if [[ "$SIZE" -lt 500 ]]; then
  echo "[dreaming] WARNING: report suspiciously small ($SIZE bytes); check $REPORT" >&2
fi

echo "[dreaming] OK: $REPORT ($SIZE bytes)"
