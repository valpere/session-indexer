#!/bin/bash
# Shared logging setup for session-indexer Claude Code hooks.
#
# Usage: source this file, then call hook_setup_logging "<script-name>"
# Sets LOG_FILE (global) and redirects stderr to the log + terminal.
#
# Log dir is per-project, derived from the repo name so each project's
# hooks log separately (session-indexer -> ~/.cache/session-indexer/).
# Override with HOOK_LOG_DIR.

hook_setup_logging() {
  local script_name="$1"
  local project_name
  project_name=$(basename "$(git rev-parse --show-toplevel 2>/dev/null || echo "$PWD")")
  LOG_DIR="${HOOK_LOG_DIR:-${HOME}/.cache/${project_name}}"
  mkdir -p "$LOG_DIR" && chmod 700 "$LOG_DIR"
  LOG_FILE="$LOG_DIR/hooks.log"
  exec 2> >(tee -a "$LOG_FILE" >&2)
  echo "[$(date -Iseconds)] $script_name invoked" >> "$LOG_FILE"
}

# Populates global array LOG_ENTRIES with each "## YYYY-MM-DD ..." block
# (trimmed of trailing blank lines), in file order. Empty array if the file
# is missing or has no day-headers.
hook_read_log_entries() {
  local log_file="$1"
  LOG_ENTRIES=()
  [[ -f "$log_file" ]] || return 0

  local -a starts
  mapfile -t starts < <(grep -n '^## [0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]' "$log_file" | cut -d: -f1)
  local total_lines
  total_lines=$(wc -l < "$log_file")

  local n=${#starts[@]}
  local i s e block
  for ((i = 0; i < n; i++)); do
    s=${starts[i]}
    if (( i + 1 < n )); then
      e=$(( starts[i + 1] - 1 ))
    else
      e=$total_lines
    fi
    block=$(sed -n "${s},${e}p" "$log_file" | sed -e :a -e '/^\n*$/{$d;N;ba' -e '}')
    LOG_ENTRIES+=("$block")
  done
}

# Writes/replaces today's entry in a rolling day-log, keeping the last
# $max_keep entries (default 10). $entry must start with "## YYYY-MM-DD".
# If a block anywhere in the log already has today's header, it is
# replaced in place; otherwise the entry is appended as a new day. Scans
# the whole list (not just the last block) so a same-day entry is never
# silently duplicated if entries are ever out of chronological order.
hook_rotate_log() {
  local log_file="$1" entry="$2" max_keep="${3:-10}"
  local today_header
  today_header=$(head -1 <<< "$entry")

  if [[ ! -f "$log_file" ]]; then
    printf '%s\n' "$entry" > "$log_file"
    return
  fi

  hook_read_log_entries "$log_file"
  local -a blocks=("${LOG_ENTRIES[@]}")

  local today_idx=-1
  local i
  for i in "${!blocks[@]}"; do
    [[ "${blocks[$i]}" == "$today_header"* ]] && today_idx=$i
  done
  if (( today_idx >= 0 )); then
    unset 'blocks[today_idx]'
    blocks=("${blocks[@]}")   # re-index after unset
    blocks+=("$entry")
  else
    blocks+=("$entry")
  fi

  local total=${#blocks[@]}
  local start_idx=0
  if (( total > max_keep )); then
    start_idx=$(( total - max_keep ))
  fi

  {
    local i
    for ((i = start_idx; i < total; i++)); do
      printf '%s\n' "${blocks[i]}"
      (( i < total - 1 )) && printf '\n'
    done
  } > "${log_file}.tmp"
  mv "${log_file}.tmp" "$log_file"
}