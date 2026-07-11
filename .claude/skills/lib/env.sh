#!/usr/bin/env bash
# lib/env.sh — load named API keys for skill scripts
#
# Usage:
#   source .claude/skills/lib/env.sh
#
#   load_env_key OPENROUTER_API_KEY          # auto-search: .env.local → .env → shell env
#   load_env_key OLLAMA_API_KEY .env.local   # explicit file (no fallback)
#
# After load_env_key the variable is exported into the current shell.
# Strips surrounding quotes and trailing whitespace to prevent invisible auth failures.
#
# Search order (no explicit file):
#   1. .env.local   (gitignored, preferred for secrets)
#   2. .env         (may be committed — use for non-secret defaults only)
#   3. Shell environment (already exported by the caller's shell)

load_env_key() {
  local key="$1"
  local explicit_file="${2:-}"

  _lvk_extract() {
    local k="$1" f="$2"
    grep "^${k}=" "$f" 2>/dev/null \
      | head -1 \
      | cut -d= -f2- \
      | sed 's/^["'"'"']//;s/["'"'"']$//' \
      | sed 's/[[:space:]]*$//'
  }

  local value=""

  if [ -n "$explicit_file" ]; then
    value=$(_lvk_extract "$key" "$explicit_file")
    if [ -z "$value" ]; then
      echo "WARNING: ${key} not found in ${explicit_file}" >&2
      return 1
    fi
  else
    for f in .env.local .env; do
      [ -f "$f" ] || continue
      value=$(_lvk_extract "$key" "$f")
      [ -n "$value" ] && break
    done

    if [ -z "$value" ]; then
      # Already exported by the calling shell?
      value=$(printenv "$key" 2>/dev/null || true)
    fi

    if [ -z "$value" ]; then
      echo "WARNING: ${key} not found in .env.local, .env, or shell environment" >&2
      return 1
    fi
  fi

  export "$key"="$value"
}
