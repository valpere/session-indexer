#!/bin/bash
# Stop hook: save recent verbatim turns, with source IDs, for session recovery.
# No model call, paraphrasing, daily replacement, or modification of manual notes.
set -euo pipefail
# Headless claude runs (weekly/dreaming) must not write or inject recovery state.
if [[ "${CLAUDE_NON_INTERACTIVE:-}" == "1" ]]; then cat >/dev/null || true; exit 0; fi
source "$(dirname "$0")/_lib/hook-common.sh"
hook_setup_logging "session-end.sh"
umask 077
PROJECT_ROOT="${CLAUDE_PROJECT_DIR:-$(git rev-parse --show-toplevel 2>/dev/null || pwd)}"
INPUT=$(cat)
TRANSCRIPT=$(printf '%s' "$INPUT" | jq -r '.transcript_path // empty')
if [[ ! -f "$TRANSCRIPT" ]]; then
  echo "[$(date -Iseconds)] session-end: transcript unavailable; previous checkpoint kept" >> "$LOG_FILE"
  exit 0
fi
OUT="$PROJECT_ROOT/.claude/session-checkpoint.json"
mkdir -p "$(dirname "$OUT")"
# Serialize concurrent Stop hooks; write atomically so SessionStart cannot read half a file.
exec 9>"$OUT.lock"
flock -w 5 9 || exit 0
STAGED=$(mktemp "$OUT.XXXXXX")
trap 'rm -f "$STAGED"' EXIT
# A bounded tail avoids reparsing hundreds of MB after every reply. The original
# transcript remains the complete journal; this file is explicitly only a window.
# Malformed final lines (a concurrent transcript append) are skipped individually.
if ! tail -n 500 "$TRANSCRIPT" | jq -Rnc --arg source "$TRANSCRIPT" --arg generated "$(date -u +%FT%TZ)" '
  [inputs | fromjson? |
    select((.type == "user" or .type == "assistant") and
      (.isCompactSummary != true) and (.isMeta != true)) |
    . as $entry | .message as $m |
    ($m.content | if type == "string" then . else
      [.[]? | select(.type == "text") | .text] | join("\n") end) as $text |
    select(($text | length) > 0) |
    {id: ($entry.uuid // "unknown"), at: ($entry.timestamp // "unknown"),
     role: $m.role, text: $text}
  ] | .[-16:] |
  map(if (.text | length) > 12000 then
    .text = "[Message exceeds the recovery window; read the full message at source + id above. No summary was substituted.]"
    else . end) |
  reverse | reduce .[] as $m ({size: 0, messages: []};
    if (.size + ($m.text | length)) <= 24000 then
      .size += ($m.text | length) | .messages += [$m]
    else . end) | .messages | reverse |
  {schema_version: 1, generated_at: $generated, source: $source,
   scope: "Recent verbatim turns only; earlier decisions may be outside this window. Read session-state.md and source when needed.",
   messages: .} | select((.messages | length) > 0)
' > "$STAGED"; then
  echo "[$(date -Iseconds)] session-end: extraction failed; previous checkpoint kept" >> "$LOG_FILE"
  exit 0
fi
if ! jq -e '.schema_version == 1 and (.messages | length) > 0' "$STAGED" >/dev/null 2>&1; then
  echo "[$(date -Iseconds)] session-end: no usable turns; previous checkpoint kept" >> "$LOG_FILE"
  exit 0
fi
mv "$STAGED" "$OUT"
echo "[$(date -Iseconds)] session-end: saved verbatim checkpoint" >> "$LOG_FILE"
