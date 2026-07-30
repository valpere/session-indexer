#!/usr/bin/env bash
# lib/agents.sh — external coding-agent CLI adapters for /fix-review tier 2
#
# Usage:
#   source .claude/skills/lib/agents.sh
#
#   OUT=$(run_external_agent "cursor-agent" "auto" "$PROMPT_FILE")
#   try_external_agents 1 "$PROMPT_FILE" "$CONFIG"   # cascades the ordered
#                                                     # reviewers.external_agents
#                                                     # list from config.yaml
#
# Each of cursor-agent/omp/codex/opencode/kilo is invoked read-only
# (no edits, no shell execution) and already unwraps its own tool-specific
# output envelope, so callers get plain text back — the SAME code-fence-strip
# + JSON-array-validate logic already used for Ollama responses (see
# fix-review/SKILL.md STEP 4 parse_round()) works unchanged downstream.
#
# All prompt content is piped via stdin/file-ref, never a shell argument —
# PR diffs can be large enough to risk ARG_MAX.
#
# `agy` was evaluated and dropped: even with the full review prompt (not
# just a throwaway one-liner) it consistently explores the filesystem/
# environment instead of answering directly, in both --mode plan and
# unrestricted modes. Not a prompt-wording problem — verified 2026-07-29.

# ---------------------------------------------------------------------------
# Per-tool adapters — read-only invocation + envelope extraction, verified
# against real calls with a realistic review prompt (not just --help docs).
# ---------------------------------------------------------------------------

agent_cursor_agent() {
  local model="$1" prompt_file="$2"
  cursor-agent --print --output-format json --mode plan \
    ${model:+--model "$model"} < "$prompt_file" \
    | jq -r '.result // empty'
}

agent_omp() {
  local model="$1" prompt_file="$2"
  # omp's own "@file" syntax for message content (not stdin).
  omp -p --mode json --no-tools ${model:+--model "$model"} "@$prompt_file" \
    | jq -rs '[.[] | select(.type=="message_update") | .assistantMessageEvent
               | select(.type=="text_end") | .content] | last // empty'
}

agent_codex() {
  local model="$1" prompt_file="$2"
  # Plain-text output with a banner + echoed prompt around the answer;
  # take the last line that looks like a JSON array (codex prints the
  # final answer twice — once inline, once in a closing summary).
  codex exec --sandbox read-only ${model:+-m "$model"} - < "$prompt_file" 2>/dev/null \
    | awk '/^\[/' | tail -1
}

agent_opencode() {
  local model="$1" prompt_file="$2"
  # No default model on this install — always pass one explicitly or the
  # call errors out (verified 2026-07-29). config.yaml must set a model
  # for this tool.
  opencode run --format json ${model:+--model "$model"} < "$prompt_file" \
    | jq -rs '[.[] | select(.type=="text")] | last | .part.text // empty'
}

agent_kilo() {
  local model="$1" prompt_file="$2"
  # Same CLI/event shape as opencode (fork/whitelabel of the same project),
  # but has a working default model — --model stays optional.
  kilo run --format json ${model:+--model "$model"} < "$prompt_file" \
    | jq -rs '[.[] | select(.type=="text")] | last | .part.text // empty'
}

# ---------------------------------------------------------------------------
# Dispatcher + cascade
# ---------------------------------------------------------------------------

# Usage: run_external_agent "<tool>" "<model-or-empty>" "<prompt-file>"
# Prints extracted text to stdout. Empty output == failure for this tool.
run_external_agent() {
  local tool="$1" model="$2" prompt_file="$3"
  case "$tool" in
    cursor-agent) agent_cursor_agent "$model" "$prompt_file" ;;
    omp)          agent_omp          "$model" "$prompt_file" ;;
    codex)        agent_codex        "$model" "$prompt_file" ;;
    opencode)     agent_opencode     "$model" "$prompt_file" ;;
    kilo)         agent_kilo         "$model" "$prompt_file" ;;
    *)
      echo "warn: unknown external agent tool '$tool' — skipping" >&2
      return 1
      ;;
  esac
}

# Usage: try_external_agents <round-n> <prompt-file> <config-path> <run-dir>
# Walks reviewers.external_agents in config.yaml order; first tool that
# returns non-empty output wins. Writes round_${n}.raw.json / .meta /
# .failover on success (same marker-file convention SKILL.md STEP 3/11
# already use for the ollama_local tier). Returns 1 if every tool failed
# (or the config key is absent) so the caller falls through to ollama_local.
try_external_agents() {
  local n="$1" prompt_file="$2" config="$3" run_dir="$4"
  local entry tool model out

  # Read the config list on fd 3, not stdin — several adapters (cursor-agent,
  # codex, opencode) read their prompt from stdin, and a `while read` loop
  # driven by stdin would otherwise race/interleave with those inner reads
  # (confirmed empirically: without this, a tool mid-loop got truncated
  # input because it silently shared the loop's stdin stream).
  while IFS= read -r entry <&3; do
    [ -z "$entry" ] && continue
    tool=$(jq -r '.tool' <<<"$entry")
    model=$(jq -r '.model // empty' <<<"$entry")
    echo "warn: round ${n} trying external agent ${tool}${model:+ ($model)}" >&2
    out=$(run_external_agent "$tool" "$model" "$prompt_file" 2>/dev/null </dev/null)
    if [ -n "$(printf '%s' "$out" | tr -d '[:space:]')" ]; then
      printf '%s' "$out" > "$run_dir/round_${n}.raw.json"
      printf '%s\n0' "$tool" > "$run_dir/round_${n}.meta"
      printf 'external_agents:%s' "$tool" > "$run_dir/round_${n}.failover"
      return 0
    fi
    echo "warn: round ${n} external agent ${tool} failed/empty — trying next" >&2
  done 3< <(yq -c '.reviewers.external_agents[]' "$config" 2>/dev/null)

  return 1
}
