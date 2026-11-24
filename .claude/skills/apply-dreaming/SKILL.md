---
name: apply-dreaming
description: "Read the latest central dreaming report (session-indexer
  findings only) and apply high-confidence findings. Routes constraint/architecture/code
  changes through a GitHub issue -> /ship (no /backlog step in this
  project). Direct branch+PR only for tooling scripts that don't
  touch CLAUDE.md's Key Constraints. Records outcomes in issues/PRs; never edits the central report."
user-invocable: true
argument-hint: "[week|latest]"
---

# /apply-dreaming (session-indexer)

Walks each session-indexer finding of the **central** dreaming report
(`~/wrk/common_claude/reports/dreaming/`, produced by
`~/wrk/common_claude/dreaming/dreaming.sh`) interactively. Per-project
dreaming (`.claude/dreaming/dreaming.sh`) was retired 2026-11-02; old local
reports under `.claude/dreaming/reports/` are history, not instructions.
The central report lives in another repo: do not annotate or edit it — the
audit trail is the issue/PR that cites `§<id>`.

## When to invoke

- After a fresh central dreaming report appears.
- Or `/apply-dreaming` (with optional `latest` or `2026-W##` argument).

## Inputs

- Optional argument: `latest` (default) or `YYYY-W##`.
- Project root: `~/wrk/projects/session-indexer/session-indexer/`.

## Steps

### 1. Locate report

```bash
WEEK="${1:-latest}"
DIR="$HOME/wrk/common_claude/reports/dreaming"
if [[ "$WEEK" == "latest" ]]; then
  REPORT=$(find "$DIR" -maxdepth 1 -type f -name '[0-9][0-9][0-9][0-9]-W*.md' \
           -printf '%T@ %p\n' 2>/dev/null \
           | sort -rn | head -1 | cut -d' ' -f2-)
else
  REPORT="$DIR/$WEEK.md"
fi
```

### 2. Parse into structured items

Read REPORT. Keep only items that cite session-indexer (its repo path or
`~/.claude/projects/-home-val-wrk-projects-session-indexer-session-indexer/`);
other projects' items are out of scope. For each remaining numbered sub-item under a section, extract:

- `id`, `title`, `confidence` (high/medium/low)
- `evidence` — file:line / commit sha
- `suggestion`
- `category` — infer from the suggestion:
  - **any Key Constraints violation** (CGO, Go patch pin, 60s budget,
    per-project isolation) → `constraint-fix` — this project is
    deployed via systemd across 8+ other projects; treat as
    highest-priority regardless of stated confidence
  - "update `docs/architecture.md`" / schema drift → `update-docs`
  - "code change / fix drift / implement" → `code-change`
  - "merge skills / new convention" → `update-tooling`
  - "stale-branch commit" → `branch-hygiene`
  - else → `other`

**Confidence inheritance.** If a sub-item has no explicit `confidence`
field, inherit it from the enclosing section. Default `medium` only
when neither declares one.

**Idempotency.** Before acting on an item, check `gh issue list --state all
--search "<report-week> §<id>"` — skip items that already have an issue or PR.

### 3. Show TL;DR + counts

```
2026-W##: N items (X high, Y medium, Z low)
TL;DR: ...
Process all? [y/select/skip-low/abort]
```

### 4. Triage walk

Iterate `high → medium → low`, but **surface any `constraint-fix` item
first regardless of its stated confidence** — this binary runs inside
other projects' Stop hooks; a regression here is a multi-project
outage, not a local inconvenience.

```
[H 1/N] §<id>  <title>
  Evidence: <file:line / commit>
  Suggestion: <suggestion>

  [a]pply / [s]kip / [v]erify-first / [e]vidence / [q]uit
```

For `low` (non-constraint): skip silently unless the user opted in.

### 5. Apply per category

**Routing rule.** Two paths — this project has no `/backlog` step, so
"plan-and-gate" means a GitHub issue directly, not a plan file:

- **Issue-and-ship path** for anything touching correctness or the
  Key Constraints: `constraint-fix`, `code-change`, `update-docs`,
  `update-tooling`. `gh issue create` with a body citing the dreaming
  finding, then `/ship <issue-number>`.
- **Direct branch+PR path** only for `branch-hygiene` cleanup (no code
  change, just a git-history fix).

#### `constraint-fix` / `code-change` / `update-docs` / `update-tooling` — issue-and-ship

1. `gh issue create --title "<slug>" --body "..."` citing report
   `<YYYY-W##> §<id>`, evidence (file:line/commit), suggested fix, acceptance
   criteria. For `constraint-fix`, explicitly name which Key
   Constraint is at risk.
2. Tell the user: "Created issue #<N>. Run `/ship <N>` to implement."
3. Don't implement directly — `/ship` owns the issue → implement →
   `/fix-review` → merge → close pipeline.

#### `branch-hygiene` — direct fix, no code change

1. Confirm the branch has a merged PR (`gh pr list --state merged
   --search "head:<branch>"`).
2. If a commit landed on it after merge, that commit's changes need to
   move to a fresh branch off `main` — cherry-pick onto
   `git checkout main && git pull --ff-only && git checkout -b
   <new-branch>`, verify, open a fresh PR. Don't leave orphaned commits
   on the stale branch.

#### `other` — manual review

Print suggestion + evidence. Don't apply; list it under "Manual review"
in the final summary.

### 6. Final summary

```
Applied: N (direct branch-hygiene PRs)
Issues created: M (awaiting /ship)
Manual review: K
Skipped: P

PRs opened: <list>
Issues pending: <list>

Next steps: run /ship on each issue (constraint-fix issues first).
```

## Constraints (CRITICAL)

- **NEVER commit or push directly to `main`.**
- **NEVER downgrade a `constraint-fix` item's priority** based on
  stated confidence — surface it first regardless. This binary's
  behavior is load-bearing for 8+ other projects' Stop hooks.
- **NEVER auto-apply low confidence** (non-constraint) without explicit request.
- **ALWAYS cite report-section** (`§<id>`) in issue bodies, commit
  messages, and PR bodies.
- **Confirm before destructive ops** even at high confidence.
- **Check for the stale-branch anti-pattern** (`CLAUDE.md`'s "Before
  Any Commit" section) before committing anything onto an existing
  branch, not just for `branch-hygiene` items.

## Anti-patterns

- ❌ Commit or push to `main` directly, tooling included.
- ❌ Treat a `constraint-fix` finding as routine — it needs the same
  scrutiny as a multi-project regression report, not a shrug because
  the model's confidence label said "low."
- ❌ Commit onto a branch that already has a merged PR.
- ❌ Edit the central report (another repo) — cite it, don't annotate it.
- ❌ Apply a finding without verifying it against current code first.

## Companion skills

- `/ship` — issue → implement → `/fix-review` → merge → close.
- `/fix-review` — parallel multi-model review + Claude Arbiter (part of `/ship`).
- `/revival`, `/find-bugs`, `/improve` — complementary read-only checks.

## See also

- `~/wrk/common_claude/dreaming/` — the central dreaming pass (what it looks for, how it runs).
- `CLAUDE.md` "Key Constraints" section — target of `constraint-fix` findings.
