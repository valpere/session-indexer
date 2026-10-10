#!/bin/bash
# Stop hook: appends/updates today's entry in .claude/session-log.md.
#
# Behaviour:
#   - Skip if /session-end skill wrote the file within the last 2 hours
#   - Generate summary: local Ollama API (1-2 s) → agy (Gemini) → opencode (cloud)
#     → raw excerpt. The whole run keeps inside SESSION_END_BUDGET_S (default
#     57 s, under the 60 s Stop-hook limit) so the log is ALWAYS written — a
#     hook killed mid-chain never writes, the 2 h skip below never engages and
#     the next reply re-runs the same doomed chain (seen: 25% of runs timed out)
#   - If session-log.md already has an entry for today, replace it
#   - Otherwise append; rotate to keep last 10 day-entries
#
# Output: .claude/session-log.md (gitignored, per-project)

set -uo pipefail

# shellcheck source=_lib/hook-common.sh
source "$(dirname "$0")/_lib/hook-common.sh"
hook_setup_logging "session-end.sh"

INPUT=$(cat)
echo "[$(date -Iseconds)] session-end invoked" >> "$LOG_FILE"

LOG="${SESSION_LOG:-$(dirname "$0")/../session-log.md}"
TODAY=$(date '+%Y-%m-%d')

# Skip if /session-end skill already ran this session (file modified < 2h ago)
if [[ -f "$LOG" ]]; then
  AGE=$(( $(date +%s) - $(stat -c %Y "$LOG" 2>/dev/null || echo 0) ))
  if [[ $AGE -lt 7200 ]]; then
    echo "[$(date -Iseconds)] session-end: skill already ran (${AGE}s ago), skipping" >> "$LOG_FILE"
    exit 0
  fi
fi

# Locate session transcript
TRANSCRIPT=$(echo "$INPUT" | jq -r '.transcript_path // empty' 2>/dev/null || echo "")

if [[ -z "$TRANSCRIPT" || ! -f "$TRANSCRIPT" ]]; then
  PROJECT_HASH=$(pwd | sed 's|/|-|g')
  TRANSCRIPT=$(ls -t "$HOME/.claude/projects/$PROJECT_HASH"/*.jsonl 2>/dev/null | head -1 || echo "")
fi

if [[ -z "$TRANSCRIPT" || ! -f "$TRANSCRIPT" ]]; then
  echo "[$(date -Iseconds)] session-end: no transcript found, skipping" >> "$LOG_FILE"
  exit 0
fi

# Extract last 30 exchanges from JSONL
EXCERPT=$(jq -R -n -r '
  [inputs | select(length > 0) | (try fromjson catch empty)] |
  map(select(. != null and (.message.role == "user" or .message.role == "assistant"))) |
  map(
    (.message.content) as $c |
    (if ($c | type) == "array" then
        ($c | map(select(.type == "text") | .text) | join(""))
      else ($c // "") end) as $text |
    {role: .message.role, text: ($text | if length > 600 then .[0:600] else . end)}
  ) |
  map(select(.text != "")) |
  .[-30:] |
  map("[\(.role)]: \(.text)") |
  join("\n\n")
' < "$TRANSCRIPT" 2>/dev/null || echo "")

if [[ -z "$EXCERPT" ]]; then
  echo "[$(date -Iseconds)] session-end: empty transcript, skipping" >> "$LOG_FILE"
  exit 0
fi

PROMPT="Write a session summary. Use exactly this structure (no preamble, start directly with the header):

## $TODAY

### Що зробили
- completed items

### Поточний стан
- current branch / open PR / what works / what is broken

### Відкриті питання
- unresolved questions (omit this section entirely if none)

### Наступні кроки
- what to pick up next session, in priority order

Rules: 10-20 bullets total, Ukrainian for content, English for code/file names/identifiers.

SESSION TRANSCRIPT:
$EXCERPT"

# --- LLM call helpers ---

# is_valid_summary rejects anything that doesn't start with the expected
# "## $TODAY" header — guards against a model ignoring the prompt and
# returning generic chit-chat (observed: agy silently dropping the prompt
# when passed via stdin, e.g. "I am currently running on Gemini 3.5
# Flash..."). A non-empty result is not enough on its own; it must also
# look like the summary we asked for, or the next tier should be tried.
is_valid_summary() {
  [[ "$1" == "## $TODAY"* ]]
}

# Tier 1: straight to the local Ollama API (it forwards :cloud models). ~1-2 s
# a call, where the agy/opencode CLIs need 15-45 s each (CLI + session start).
# Two models in turn, each capped at 20 s. Model tags retire periodically —
# audit this default with the fix-review model lists after any Ollama
# retirement notice (see dreaming/ollama-model-retirement-instructions.md);
# override per machine with SESSION_END_MODELS="tag1 tag2".
try_ollama_api() {
  local url="${OLLAMA_API_URL:-http://localhost:11434}"
  local body result model
  for model in ${SESSION_END_MODELS:-minimax-m3:cloud gpt-oss:120b-cloud}; do
    (( $(remaining_budget) < 8 )) && return 1
    echo "[$(date -Iseconds)] session-end: trying ollama api model: $model" >> "$LOG_FILE"
    body=$(jq -n --arg m "$model" --arg p "$1" \
      '{model:$m,stream:false,think:false,messages:[{role:"user",content:$p}]}') || return 1
    result=$(curl -s -m 20 "$url/api/chat" -d "$body" 2>>"$LOG_FILE" | jq -r '.message.content // empty' 2>>"$LOG_FILE") || true
    is_valid_summary "$result" && { echo "$result"; return 0; }
    echo "[$(date -Iseconds)] session-end: ollama api model $model returned no usable summary" >> "$LOG_FILE"
  done
  return 1
}

# remaining_budget prints the seconds left of the hook budget, so a slow CLI
# tier is cut off (and the raw-excerpt fallback still runs) instead of the
# whole hook being killed before it writes anything. $SECONDS = time since
# this script started.
remaining_budget() { echo $(( ${SESSION_END_BUDGET_S:-57} - SECONDS )); }

try_agy() {
  # Refreshed 2026-10-10 — the 3.5 series is retired (live "invalid model
  # selection" error; `agy models` starts at 3.6). Same list as canonical
  # ~/wrk/common_claude/skills/session-end/hooks/session-end.sh.
  local models=(
    "Gemini 3.8 Flash (Low)"
    "Gemini 3.8 Flash (Medium)"
    "Gemini 3.8 Flash (High)"
    "Gemini 3.1 Pro (Low)"
    "Gemini 3.1 Pro (High)"
  )
  command -v agy &>/dev/null || return 1
  for model in "${models[@]}"; do
    local budget; budget=$(remaining_budget)
    (( budget < 8 )) && return 1   # not enough budget left for another try
    (( budget > 45 )) && budget=45
    echo "[$(date -Iseconds)] session-end: trying agy model: $model (budget ${budget}s)" >> "$LOG_FILE"
    local result
    # Prompt as a positional arg, not stdin — `agy -p` reads the prompt
    # from its argument; piping via `<<<` leaves it unset and agy falls
    # back to a generic interactive-style greeting instead of erroring.
    result=$(timeout "$budget" agy -p "$1" --model "$model" 2>>"$LOG_FILE") && \
      is_valid_summary "$result" && { echo "$result"; return 0; }
    echo "[$(date -Iseconds)] session-end: agy model $model returned no usable summary" >> "$LOG_FILE"
  done
  return 1
}

# Write excerpt to temp file for opencode --file
EXCERPT_TMP=$(mktemp /tmp/session-end-excerpt.XXXXXX)
echo "$EXCERPT" > "$EXCERPT_TMP"
trap 'rm -f "$EXCERPT_TMP"' EXIT

OPENCODE_MSG="Summarize the session transcript in the attached file. Use exactly this structure (no preamble):

## $TODAY

### Що зробили
- completed items

### Поточний стан
- current branch / open PR / what works / what is broken

### Відкриті питання
- unresolved questions (omit section if none)

### Наступні кроки
- what to pick up next session

Rules: 10-20 bullets total, Ukrainian for content, English for code/file names."

try_opencode() {
  # 2026-09-27: qwen3.5:cloud retired on Ollama Cloud 2026-09-25 (wave
  # announced 2026-09-17 alongside glm-5.1/deepseek-v4-flash:0731, both
  # already swapped elsewhere - this fallback list was missed). Canonical
  # 1:1 replacement per common/dreaming/ollama-model-retirement-instructions.md:
  # glm-5.3-flash or deepseek-v4.1-flash; picked the latter to avoid a
  # second glm entry alongside glm-5.2 above.
  local models=(
    "ollama/glm-5.2:cloud"
    "ollama/kimi-k2.7-code:cloud"
    "ollama/minimax-m3:cloud"
    "ollama/deepseek-v4.1-flash:cloud"
  )
  command -v opencode &>/dev/null || return 1
  for model in "${models[@]}"; do
    local budget; budget=$(remaining_budget)
    (( budget < 8 )) && return 1   # not enough budget left for another try
    (( budget > 45 )) && budget=45
    echo "[$(date -Iseconds)] session-end: trying opencode model: $model (budget ${budget}s)" >> "$LOG_FILE"
    local result
    result=$(timeout "$budget" opencode run "$OPENCODE_MSG" \
      --file "$EXCERPT_TMP" --model "$model" --format json 2>>"$LOG_FILE" | \
      jq -R -n -r '
        [inputs | select(length > 0) | (try fromjson catch empty)] |
        map(select(. != null and .type == "text")) |
        map(.part.text) |
        join("")
      ') && is_valid_summary "$result" && { echo "$result"; return 0; }
    echo "[$(date -Iseconds)] session-end: opencode model $model returned no usable summary" >> "$LOG_FILE"
  done
  return 1
}

# --- Generate summary ---

SUMMARY=""

SUMMARY=$(try_ollama_api "$PROMPT" 2>>"$LOG_FILE") || true

if [[ -z "$SUMMARY" ]]; then
  SUMMARY=$(try_agy "$PROMPT" 2>>"$LOG_FILE") || true
fi

if [[ -z "$SUMMARY" ]]; then
  SUMMARY=$(try_opencode 2>>"$LOG_FILE") || true
fi

if [[ -z "$SUMMARY" ]]; then
  echo "[$(date -Iseconds)] session-end: all LLMs failed, using raw excerpt" >> "$LOG_FILE"
  SUMMARY="## $TODAY

### Transcript excerpt (auto-summary unavailable)

\`\`\`
$(echo "$EXCERPT" | head -60)
\`\`\`"
fi

# --- Append/update/rotate session-log.md ---

TRIMMED_SUMMARY=$(printf '%s' "$SUMMARY" | sed -e '/./,$!d' | sed -e :a -e '/^\n*$/{$d;N;ba' -e '}')
hook_rotate_log "$LOG" "$TRIMMED_SUMMARY"

echo "[$(date -Iseconds)] session-end: wrote $(wc -l < "$LOG") lines to $LOG" >> "$LOG_FILE"
