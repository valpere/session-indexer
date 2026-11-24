#!/bin/bash
# Shared logging setup for Claude Code hooks installed via /generate-session-end.
#
# Usage: source this file, then call hook_setup_logging "<script-name>"
# Sets LOG_FILE (global) and redirects stderr to the log + terminal.
#
# Log dir is per-project, derived from the repo name so each project's
# hooks log separately (e.g. session-indexer -> ~/.cache/session-indexer/).
# Override with HOOK_LOG_DIR.

hook_setup_logging() {
  local script_name="$1"
  local project_name
  project_name=$(basename "$(git rev-parse --show-toplevel 2>/dev/null || echo "$PWD")")
  # Never operate on the ~/.cache root itself (e.g. cwd=/ with no git → basename "/" → "/").
  case "$project_name" in ""|"/"|".") project_name="session-end-hooks" ;; esac
  LOG_DIR="${HOOK_LOG_DIR:-${HOME}/.cache/${project_name}}"
  mkdir -p "$LOG_DIR" && chmod 700 "$LOG_DIR"
  LOG_FILE="$LOG_DIR/hooks.log"
  exec 2> >(tee -a "$LOG_FILE" >&2)
  echo "[$(date -Iseconds)] $script_name invoked" >> "$LOG_FILE"
}

# WHITELIST of project roots that may be indexed (session-recall) or dreamed —
# user rule 2026-10-25 (supersedes the earlier blacklist): only projects under
# ~/wrk/projects, ~/wrk/demo and this tooling repo ~/wrk/common_claude.
# Everything else (~/wrk/projects_fh, ~/wrk/freelance, ~/wrk/oblabz, ...) is NEVER processed.
# ALLOWED_PROJECT_ROOTS (colon-separated) overrides the default (used by tests).
# hook_project_allowed <project-root>: exit 0 if the root IS or lies under an allowed root.
hook_allowed_roots() { printf '%s\n' "${ALLOWED_PROJECT_ROOTS:-$HOME/wrk/projects:$HOME/wrk/demo:$HOME/wrk/common_claude}" | tr ':' '\n'; }

hook_project_allowed() {
  local root r real
  local -a roots
  root=$(realpath -m -- "$1" 2>/dev/null) || return 1
  mapfile -t roots < <(hook_allowed_roots)
  for r in "${roots[@]}"; do
    real=$(realpath -m -- "$r" 2>/dev/null) || continue
    [[ "$root" == "$real" ]] && return 0
    case "$root/" in "$real"/?*) return 0 ;; esac
  done
  return 1
}

# Inverse, used by the hooks' guards: exit 0 = skip this project.
hook_project_excluded() { ! hook_project_allowed "$1"; }
