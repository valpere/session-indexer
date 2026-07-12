#!/usr/bin/env bash
# lib/rest.sh — REST helpers for skill scripts
#
# Usage:
#   source .claude/skills/lib/rest.sh
#
#   RESPONSE=$(rest_post "$URL" "$PAYLOAD" "$API_KEY")          # Bearer auth
#   RESPONSE=$(rest_post "$URL" "$PAYLOAD" "$API_KEY" "Token")  # custom scheme
#   RESPONSE=$(rest_post "$URL" "$PAYLOAD")                     # no auth (Ollama local)
#
# rest_post writes the raw response body to stdout.
# On connection failure or curl error it writes to stderr and returns exit code 1.
# HTTP-level errors (4xx/5xx) are returned as-is — callers check .error in the JSON.
#
# Set REST_TIMEOUT (seconds, default 120) or REST_OLLAMA_TIMEOUT (default 300)
# before sourcing to override.

# ---------------------------------------------------------------------------
# Core transport
# ---------------------------------------------------------------------------

rest_post() {
  local url="$1"
  local payload="$2"
  local api_key="${3:-}"
  local auth_scheme="${4:-Bearer}"
  local timeout="${REST_TIMEOUT:-120}"

  # Payload via stdin to avoid ARG_MAX limits on large PR diffs.
  local -a args=(-sS --max-time "$timeout"
    -H "Content-Type: application/json"
    --data-binary @-)
  [ -n "$api_key" ] && args+=(-H "Authorization: ${auth_scheme} ${api_key}")

  local response errfile exit_code
  errfile=$(mktemp)
  response=$(printf '%s' "$payload" | curl "${args[@]}" "$url" 2>"$errfile")
  exit_code=$?
  local curl_err; curl_err=$(cat "$errfile"); rm -f "$errfile"

  if [ $exit_code -ne 0 ]; then
    echo "ERROR: POST ${url} failed (curl exit ${exit_code}): ${curl_err}" >&2
    return 1
  fi
  printf '%s' "$response"
}

# Like rest_post but uses REST_OLLAMA_TIMEOUT (default 300s).
# Use for local/cloud Ollama calls where large models are slow.
rest_post_ollama() {
  local timeout="${REST_OLLAMA_TIMEOUT:-300}"
  REST_TIMEOUT="$timeout" rest_post "$@"
}

# ---------------------------------------------------------------------------
# Ollama — /api/chat
# Response: { "message": { "content": "..." } }
# ---------------------------------------------------------------------------

ollama_payload() {
  jq -n --arg model "$1" --arg prompt "$2" \
    '{model:$model, messages:[{role:"user",content:$prompt}], stream:false}'
}

# Includes a system role message (Ollama /api/chat supports it).
ollama_payload_system() {
  jq -n --arg model "$1" --arg sys "$2" --arg prompt "$3" \
    '{model:$model, messages:[{role:"system",content:$sys},{role:"user",content:$prompt}], stream:false}'
}

ollama_content() {
  printf '%s' "$1" | jq -r '.message.content // empty'
}

# Like ollama_payload_system but sets think:false (top-level field on
# /api/chat, not inside options) and a capped num_predict. Without this,
# reasoning-capable cloud models (deepseek, kimi, etc.) can burn their whole
# token budget on hidden thinking and return empty content on large prompts.
ollama_payload_system_no_think() {
  local num_predict="${4:-4000}"
  jq -n --arg model "$1" --arg sys "$2" --arg prompt "$3" --argjson np "$num_predict" \
    '{model:$model, messages:[{role:"system",content:$sys},{role:"user",content:$prompt}],
      stream:false, think:false, options:{num_predict:$np}}'
}

# ---------------------------------------------------------------------------
# OpenRouter — /api/v1/chat/completions (OpenAI-compatible)
# Response: { "choices": [{ "message": { "content": "..." } }] }
# ---------------------------------------------------------------------------

openrouter_payload() {
  jq -n --arg model "$1" --arg prompt "$2" \
    '{model:$model, messages:[{role:"user",content:$prompt}], stream:false}'
}

openrouter_payload_system() {
  jq -n --arg model "$1" --arg sys "$2" --arg prompt "$3" \
    '{model:$model, messages:[{role:"system",content:$sys},{role:"user",content:$prompt}], stream:false}'
}

# Same as openrouter_payload_system but adds reasoning:{exclude:true}.
# Required for DeepSeek models: without it the <think> chain exhausts max_tokens
# and the response comes back with content:null and finish_reason:length.
openrouter_payload_system_no_think() {
  jq -n --arg model "$1" --arg sys "$2" --arg prompt "$3" \
    '{model:$model, messages:[{role:"system",content:$sys},{role:"user",content:$prompt}],
      stream:false, reasoning:{exclude:true}}'
}

openrouter_content() {
  printf '%s' "$1" | jq -r '.choices[0].message.content // empty'
}

# ---------------------------------------------------------------------------
# Provider-agnostic helpers
# Use when provider is determined at runtime from config.yaml.
# PROVIDER values: "openrouter" | "ollama" | anything else → ollama path
# ---------------------------------------------------------------------------

# Build a user-only chat payload for the given provider.
# Usage: PAYLOAD=$(chat_payload "$PROVIDER" "$MODEL" "$PROMPT")
chat_payload() {
  case "$1" in
    openrouter) openrouter_payload "$2" "$3" ;;
    *)          ollama_payload     "$2" "$3" ;;
  esac
}

# Build a payload with system + user messages for the given provider.
# Usage: PAYLOAD=$(chat_payload_system "$PROVIDER" "$MODEL" "$SYSTEM" "$PROMPT")
chat_payload_system() {
  case "$1" in
    openrouter) openrouter_payload_system "$2" "$3" "$4" ;;
    *)          ollama_payload_system     "$2" "$3" "$4" ;;
  esac
}

# Like chat_payload_system but suppresses reasoning tokens on thinking models.
# Use for DeepSeek-family models on OpenRouter, or any reasoning-capable
# model on Ollama (deepseek, kimi, etc. via /api/chat's top-level think field).
# Optional 5th arg: num_predict cap, Ollama branch only (default 4000).
chat_payload_system_no_think() {
  case "$1" in
    openrouter) openrouter_payload_system_no_think "$2" "$3" "$4" ;;
    *)          ollama_payload_system_no_think      "$2" "$3" "$4" "$5" ;;
  esac
}

# Extract assistant content from a raw API response.
# Usage: CONTENT=$(chat_content "$PROVIDER" "$RESPONSE")
chat_content() {
  case "$1" in
    openrouter) openrouter_content "$2" ;;
    *)          ollama_content     "$2" ;;
  esac
}

# Extract token counts from a response.
# Returns "PROMPT_TOKENS COMPLETION_TOKENS" (space-separated).
# Ollama responses return "null null" (no token data in /api/chat).
# Usage: read pt ct < <(chat_tokens "$PROVIDER" "$RESPONSE")
chat_tokens() {
  if [ "$1" = "openrouter" ]; then
    local p c
    p=$(printf '%s' "$2" | jq -r '.usage.prompt_tokens     // empty' 2>/dev/null)
    c=$(printf '%s' "$2" | jq -r '.usage.completion_tokens // empty' 2>/dev/null)
    printf '%s %s' "${p:-null}" "${c:-null}"
  else
    printf 'null null'
  fi
}

# Convenience: POST + extract content in a single call.
# Usage: CONTENT=$(chat_ask "$PROVIDER" "$URL" "$KEY" "$MODEL" "$SYSTEM" "$PROMPT")
# Use chat_payload_system_no_think variant when models are DeepSeek-family.
chat_ask() {
  local provider="$1" url="$2" key="$3" model="$4" sys="$5" prompt="$6"
  local payload response
  payload=$(chat_payload_system "$provider" "$model" "$sys" "$prompt")
  response=$(rest_post "$url" "$payload" "$key")
  chat_content "$provider" "$response"
}
