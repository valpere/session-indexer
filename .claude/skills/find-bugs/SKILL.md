---
name: find-bugs
description: "Read-only bug audit of the current branch vs a base ref. Dispatches subagents per stack (Go, JS/TS, etc.), consolidates findings, produces a prioritized report. Does not fix. Usage: /find-bugs [base-ref]"
---

# Skill: /find-bugs
# Bug Audit — Read-Only

Audit the current branch for bugs without touching code. Produces a
prioritized report; user decides what to fix.

---

## Steps

### Step 1 — Determine scope

```bash
BASE="${1:-main}"

# If on a feature branch: diff vs base
git rev-parse --verify "$BASE" 2>/dev/null && \
  DIFF=$(git diff "$BASE"...HEAD) || \
  DIFF=$(git diff HEAD~10...HEAD)

# Fallback: on main with no base arg → recent commits
if [[ -z "$DIFF" ]]; then
  DIFF=$(git log --since='2 weeks ago' -p --no-merges)
fi

echo "Auditing $(git rev-parse --abbrev-ref HEAD) vs $BASE"
echo "Diff size: $(echo "$DIFF" | wc -l) lines"
```

If diff > 2000 lines: warn and ask if user wants to narrow scope
(specific files or commits) before proceeding.

### Step 2 — Read project context

```bash
head -100 CLAUDE.md 2>/dev/null
cat .claude/context-essentials.md 2>/dev/null

# Detect stack
cat go.mod 2>/dev/null | head -3
cat package.json 2>/dev/null | python3 -c "
import sys,json; d=json.load(sys.stdin)
print({**d.get('dependencies',{}), **d.get('devDependencies',{})}.keys())
" 2>/dev/null

# Changed files by type
git diff "$BASE"...HEAD --name-only | sort
```

### Step 3 — Dispatch stack-aware subagents

Dispatch **in parallel** based on detected stack. Each subagent gets:
- The full diff
- Project context (CLAUDE.md / context-essentials.md excerpt)
- Stack-specific checklist (below)

**Always dispatch — Generic bug finder:**

Checklist:
- Null/nil dereference on unchecked return values
- Resource leaks (files, connections, goroutines not closed/cancelled)
- Unhandled errors silently discarded (`_ = err`, bare `catch {}`)
- Off-by-one in loops, slice bounds, index arithmetic
- Race conditions: shared mutable state accessed from goroutines/async
- Incorrect error propagation (wrapping lost, status code swallowed)
- Context not propagated into blocking calls
- Missing timeout on external calls (HTTP, DB, queue)

**If `.go` files changed — Go reviewer:**

Additional checklist:
- `defer f.Close()` missing after open (cursor/file leak)
- `sync.Mutex` copied by value
- `go func()` capturing loop variable by reference (pre-Go 1.22)
- `http.Response.Body` not closed
- `context.WithCancel` leak (cancel not called on all paths)
- Integer overflow in slice/index math
- `iota` misuse in const blocks

**If `.ts` / `.tsx` / `.js` files changed — JS/TS reviewer:**

Additional checklist:
- `await` missing on async calls (fire-and-forget)
- Unhandled promise rejection
- `useEffect` missing dependency array or stale closure
- `JSON.parse` without try/catch
- Type assertion `as X` hiding runtime mismatch
- `undefined` access on optional chaining result used as non-optional

**If DB/ORM files changed (any stack) — DB reviewer:**

Additional checklist:
- Query result not closed / cursor leak
- Missing transaction rollback on error path
- N+1 query pattern introduced
- Unique constraint not enforced at app level (only in migration)
- Index missing for new filter/sort introduced in diff

### Step 4 — Consolidate

Merge all subagent reports:
- Deduplicate by `(file, line)` — keep highest severity, merge bodies
- Remove findings already caught by `make lint` / linter output
- Remove "consider adding X" without concrete evidence in the diff

### Step 5 — Report

```markdown
# Bug audit — <branch> vs <base> — <date>

## Critical — block merge
- `file.go:42` **nil deref**: `resp.Body` read before nil check after `http.Get` error path.
  Fix: check `err != nil` before using `resp`.

## High
- ...

## Medium
- ...

## Low / nits
- ...

---
Findings: <N> total (<critical> critical, <high> high, <medium> medium, <low> low)
```

Save to `/tmp/<project>-bugs-<short-sha>.md` and print inline.

Final message:
> "<N> findings. Address with Edit or a dedicated fix subagent."

---

## Constraints

- **Read-only.** No edits, no commits, no `git checkout`.
- **Cite file:line** for every finding — no location = skip it.
- **Differentiate certainty**: "definite bug" vs "potential hazard" vs "worth checking".
- Severity guide:

  | Level | Definition |
  |-------|-----------|
  | Critical | Definite crash, data corruption, or security hole in the diff |
  | High | Likely bug under realistic conditions; resource leak |
  | Medium | Potential bug; depends on caller contract not visible in diff |
  | Low | Minor hazard, defensive improvement, nit |

---

## Anti-patterns

- ❌ Generic "consider adding error handling" without showing which call and why.
- ❌ Severity inflation — not everything is critical.
- ❌ Reproducing what `make lint` / `go vet` already reports.
- ❌ Flagging code not present in the diff.
