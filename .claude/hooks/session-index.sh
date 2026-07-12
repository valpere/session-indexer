#!/bin/bash
# Stop hook: indexes the session transcript into per-project SQLite via
# session-indexer mine. Runs independently of session-end.sh so it fires
# even when the summary step exits early (e.g. /session-end already ran).

set -uo pipefail

# shellcheck source=_lib/hook-common.sh
source "$(dirname "$0")/_lib/hook-common.sh"
hook_setup_logging "session-index.sh"

INPUT=$(cat)

TRANSCRIPT=$(echo "$INPUT" | jq -r '.transcript_path // empty' 2>/dev/null || echo "")

if [[ -z "$TRANSCRIPT" || ! -f "$TRANSCRIPT" ]]; then
    echo "[$(date -Iseconds)] session-index: no transcript, skipping" >> "$LOG_FILE"
    exit 0
fi

if ! command -v session-indexer >/dev/null 2>&1; then
    echo "[$(date -Iseconds)] session-index: session-indexer not in PATH, skipping" >> "$LOG_FILE"
    exit 0
fi

PROJECT_ROOT=$(hook_project_root)
DB="$PROJECT_ROOT/.claude/sessions.db"

echo "[$(date -Iseconds)] session-index: mining $(basename "$TRANSCRIPT") → $DB" >> "$LOG_FILE"
if session-indexer mine "$TRANSCRIPT" --db "$DB" >> "$LOG_FILE" 2>&1; then
    echo "[$(date -Iseconds)] session-index: done" >> "$LOG_FILE"
else
    MINE_EXIT=$?
    echo "[$(date -Iseconds)] session-index: session-indexer exited $MINE_EXIT" >> "$LOG_FILE"
fi
