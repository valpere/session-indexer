---
name: housekeeping
description: "session-indexer repo health check. Universal hygiene + Go vet/fmt + project-specific checks. Usage: /housekeeping"
---

# Skill: /housekeeping
# Repo Health Check

---

## OVERVIEW

```
/housekeeping  →  run checks  →  Markdown table: Check | Status | Detail
                              →  Summary: N passed, M failed
```

Read-only. Never modifies files, never commits, never opens a PR.
Run any time for a hygiene snapshot. Any FAIL = exit signal to fix before shipping.

---

## UNIVERSAL CHECKS

### Check 1 — Stale Local Branches

**Goal:** ≤ 10 local branches after pruning remote-tracking refs.

```bash
git remote prune origin 2>&1 | tail -3
LOCAL_COUNT=$(git branch | grep -v '^\*' | wc -l | tr -d ' ')
```

**Pass:** `LOCAL_COUNT <= 10`
**Fail:** "N local branches — prune merged ones"

Cleanup tip:
```bash
git branch --merged main | grep -v 'main\|^\*'
# delete with: git branch -d <branch>
```

---

### Check 2 — Debug Output in Source

**Goal:** Zero debug/print statements in production source (excluding test files).

Auto-detect language and run the appropriate check:

```bash
# JavaScript/TypeScript
COUNT=$(grep -r --include="*.ts" --include="*.tsx" --include="*.js" \
  --exclude="*.test.*" --exclude="*.spec.*" \
  -l "console\.log(" src/ 2>/dev/null | wc -l | tr -d ' ')

# Go
COUNT=$(grep -r --include="*.go" \
  --exclude="*_test.go" \
  -l "fmt\.Println\|fmt\.Printf\|fmt\.Print(" . 2>/dev/null \
  | grep -v "_test.go" | wc -l | tr -d ' ')

# Python
COUNT=$(grep -r --include="*.py" \
  -l "^\s*print(" . 2>/dev/null \
  | grep -v "test_\|_test.py" | wc -l | tr -d ' ')
```

Run whichever applies. If multiple apply, sum them.

**Pass:** `COUNT == 0`
**Fail:** list offending files (up to 5, then "+ N more")

---

### Check 3 — Tracked .env File

**Goal:** `.env` must not be tracked by git (would leak secrets).

```bash
TRACKED=$(git ls-files .env 2>/dev/null)
```

**Pass:** empty result
**Fail:** "`.env` is tracked — add to .gitignore and run `git rm --cached .env`"

---

### Check 4 — Tracked Backup Files

**Goal:** `backup/` directory (if it exists) must not be tracked by git.

```bash
TRACKED=$(git ls-files backup/ 2>/dev/null)
```

**Pass:** empty result (or `backup/` doesn't exist)
**Fail:** list the tracked backup files

---

### Check 5 — TODO/FIXME Count (informational)

**Goal:** Report count. No threshold — visibility only.

```bash
COUNT=$(grep -r --include="*.ts" --include="*.tsx" \
  --include="*.go" --include="*.py" --include="*.js" \
  -E "//\s*(TODO|FIXME)|#\s*(TODO|FIXME)" \
  --exclude-dir=node_modules --exclude-dir=.git \
  . 2>/dev/null | wc -l | tr -d ' ')
```

**Status:** Always `INFO`.
**Detail:** "N TODO/FIXME comments" — append " (consider a cleanup sprint)" if > 20.

This check never contributes to the failed count.

---

### Check 6 — Project Layout Drift

**Goal:** files in `docs/` should not be working drafts or duplicates of
canonical artifacts in the sibling `../context/` directory (per the
personal-projects convention `~/wrk/projects/<name>/<name>/` repo +
`~/wrk/projects/<name>/context/` notes).

```bash
# 6a — draft / iter / dated working files that escaped into docs/
STRAY=$(find docs -maxdepth 1 -type f \( \
  -name '*-DRAFT.md' -o \
  -name 'review-prompt-*.md' -o \
  -name '*-iter[0-9]*.md' -o \
  -name '20[0-9][0-9]-[0-9][0-9]-[0-9][0-9]-*.md' \
\) 2>/dev/null)

# 6b — exact or date-prefixed duplicates of files in ../context/
DUPES=""
if [ -d ../context ]; then
  for f in docs/*.md 2>/dev/null; do
    [ -f "$f" ] || continue
    base=$(basename "$f")

    # Pattern A: exact same filename in ../context/
    if [ -f "../context/$base" ]; then
      DUPES="$DUPES $f (exact: ../context/$base)"
      continue
    fi

    # Pattern B: context/ has date-prefixed twin
    # (docs/X.md ↔ ../context/YYYY-MM-DD-X.md)
    for ctx in ../context/20[0-9][0-9]-[0-9][0-9]-[0-9][0-9]-"$base"; do
      [ -f "$ctx" ] || continue
      DUPES="$DUPES $f (date-prefixed twin: $ctx)"
      break
    done
  done
fi
```

**Pass:** no strays, no duplicates.
**Fail:** list offenders (up to 5, then "+ N more"). Recommend manual review
— files may be intentional copies for repo distribution, or strays that
should be deleted / moved to `../context/`.
**Skip:** `../context/` does not exist (this isn't a convention-based project).

---

### Check 7 — CLAUDE.md Key Files Exist

**Goal:** files explicitly listed under a "Key Files" / "Ключові файли"
section of `CLAUDE.md` should actually exist on disk.

```bash
MISSING=""
if [ -f CLAUDE.md ]; then
  # Pull section between heading and next heading; extract backtick-quoted paths
  MISSING=$(awk '
    /^##.*[Kk]ey [Ff]iles|^##.*[Кк]лючов.*файл/ {in_section=1; next}
    /^##/ && in_section {in_section=0}
    in_section
  ' CLAUDE.md | grep -oE '`[^`]+`' | tr -d '`' | while read f; do
    # filter: only paths that look like real files (contain / or end in known ext)
    case "$f" in
      */*|*.md|*.go|*.py|*.ts|*.tsx|*.js|*.yaml|*.yml|*.json|*.xlsx|*.toml)
        [ -e "$f" ] || echo "$f"
        ;;
    esac
  done)
fi
```

**Pass:** all listed files exist (or no Key Files section).
**Fail:** list missing paths — likely doc drift after a rename/delete.
**Skip:** no `CLAUDE.md` at repo root.

---

### Check 8 — Skill Temp Dir Accumulation (informational)

**Goal:** report `/tmp/<skill>-*` directories older than 7 days from
interactive skill runs (`fix-review`, `lookup-docs`, `apply-dreaming`, etc).

```bash
COUNT=$(find /tmp -maxdepth 1 -type d -mtime +7 \
  \( -name 'fix-review-*' -o -name 'lookup-docs-*' -o -name 'apply-dreaming-*' \) \
  2>/dev/null | wc -l | tr -d ' ')
```

**Status:** Always `INFO`.
**Detail:** "N stale skill temp dirs in /tmp" — append " (rm -rf /tmp/<prefix>-* to clean)" if > 5.

This check never contributes to the failed count.

---

## STACK-SPECIFIC CHECKS (Go — session-indexer)

### Check 9 — go vet

**Goal:** `go vet` must pass with zero errors.

```bash
go vet ./... 2>&1
```

**Pass:** no output (exit 0)
**Fail:** list the vet errors
**Skip:** `go.mod` does not exist (project not yet implemented)

---

### Check 10 — Formatting (gofmt)

**Goal:** All `.go` files are properly formatted.

```bash
UNFORMATTED=$(gofmt -l . 2>/dev/null | grep -v vendor)
COUNT=$(echo "$UNFORMATTED" | grep -c . 2>/dev/null || echo 0)
```

**Pass:** 0 unformatted files
**Fail:** list unformatted files (up to 5, then "+ N more"); fix with `gofmt -w .`
**Skip:** no `.go` files found

---

### Check 11 — Build Artifacts and DB Not Tracked

**Goal:** Generated files (`bin/`, `session-indexer` binary, `.claude/sessions.db`)
must not be tracked by git.

```bash
TRACKED=$(git ls-files bin/ session-indexer .claude/sessions.db 2>/dev/null)
```

**Pass:** empty result
**Fail:** list the tracked files — add to `.gitignore` and `git rm --cached <file>`

---

## OUTPUT FORMAT

```
## /housekeeping — Repo Health Report

| Check | Status | Detail |
|-------|--------|--------|
| Stale local branches | PASS | 6 local branches |
| Debug output in src | FAIL | 2 files: src/hooks/useData.ts, src/util/api.ts |
| Tracked .env | PASS | — |
| Tracked backup files | PASS | — |
| TODO/FIXME count | INFO | 11 TODO/FIXME comments |
| Project layout drift | PASS | — |
| CLAUDE.md key files | SKIP | no CLAUDE.md |
| Skill temp dirs | INFO | 3 stale skill temp dirs in /tmp |

**4 passed, 1 failed** (2 informational, 1 skipped)
```

Status values:
- `PASS` — check succeeded
- `FAIL` — check failed (must be addressed)
- `INFO` — informational only, never counted as failed
- `SKIP` — could not run (missing tools, no artifacts)

Summary: `N passed, M failed` — with optional `(K informational, J skipped)`.

---

## RULES

1. **Read-only** — never modify files, commit, push, or open a PR.
2. **Run from repo root** — all paths relative to repository root.
3. **INFO checks never count as failures** (TODO/FIXME is always INFO).
4. **SKIP is not failure** — a skipped check doesn't increment failed count.
5. **Graceful degradation** — if a tool is unavailable, mark check SKIP and continue.
6. **No auto-fix** — this skill reports; for fixes use the appropriate skill.
7. **Exit signal** — if any check is FAIL, end with: "Run /housekeeping again after fixing the issues above."

---

## NOTE

Checks 6-7 assume the personal-projects layout convention
(`~/wrk/projects/<name>/<name>/` repo + sibling `context/` notes) — they
SKIP gracefully when the convention doesn't apply (no `../context/`, no
`CLAUDE.md`). Checks 9-11 are session-indexer-specific and SKIP when `go.mod`
or `.go` files are absent (project in pre-implementation stage).
