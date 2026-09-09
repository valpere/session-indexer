---
name: apply-dreaming
description: "Read the latest session-indexer dreaming report and
  apply high-confidence findings. Routes constraint/architecture/code
  changes through a GitHub issue -> /ship (no /backlog step in this
  project). Direct branch+PR only for tooling scripts that don't
  touch CLAUDE.md's Key Constraints. Annotates report with
  [applied YYYY-MM-DD] markers."
user-invocable: true
argument-hint: "[week|latest]"
---

# /apply-dreaming (session-indexer)

Walks each dreaming-report finding interactively and leaves an audit
trail (`[applied YYYY-MM-DD]` / `[planned …]` / `[skipped …]` markers
appended to the report).

## When to invoke

- Monday morning after the Sunday cron-run produced a fresh report.
- Or any time after a manual `.claude/dreaming/dreaming.sh` run.
- Or `/apply-dreaming` (with optional `latest` or `2026-W##` argument).

## Inputs

- Optional argument: `latest` (default) or `YYYY-W##`.
- Project root: `~/wrk/projects/session-indexer/session-indexer/`.

## Steps

### 1. Locate report

```bash
WEEK="${1:-latest}"
DIR=".claude/dreaming/reports"
if [[ "$WEEK" == "latest" ]]; then
  REPORT=$(find "$DIR" -maxdepth 1 -type f -name '[0-9][0-9][0-9][0-9]-W*.md' \
           -printf '%T@ %p\n' 2>/dev/null \
           | sort -rn | head -1 | cut -d' ' -f2-)
else
  REPORT="$DIR/$WEEK.md"
fi
```

### 2. Parse into structured items

Read REPORT. For each numbered sub-item under a section, extract:

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

**Idempotency — skip already-marked items.** If the next non-blank
line after an item starts with `> [applied …]`, `> [planned …]`,
`> [skipped …]`, or `> [manual-review-required …]`, skip it silently.
The report is appended to (never rewritten) on each pass. Print a
summary at the start: `2026-W##: M new items (N already-processed
skipped)`.

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
  change, just a git-history fix) and `.claude/dreaming/*.sh` script
  edits that don't touch `CLAUDE.md`'s Key Constraints.

#### `constraint-fix` / `code-change` / `update-docs` / `update-tooling` — issue-and-ship

1. `gh issue create --title "<slug>" --body "..."` citing report
   `§<id>`, evidence (file:line/commit), suggested fix, acceptance
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

#### `update-tooling` (dreaming script only, not the prompt) — branch+PR

For fixes to `dreaming.sh` itself:

1. `git switch -c fix-dreaming-w##-<slug>` off `main`.
2. Edit the script; smoke-test with `bash -n .claude/dreaming/dreaming.sh`.
3. Commit, push, open a PR, merge per the project's normal PR
   convention.

#### `other` — manual review

Print suggestion + evidence. Don't apply. Annotate
`[manual-review-required 2026-MM-DD]`.

### 6. Annotate report

After each applied item, append (never rewrite the original):

```markdown
> [applied 2026-MM-DD: <action>; commit <sha>; PR <num>]
```

For created issues:
```markdown
> [planned 2026-MM-DD: issue #<N>; awaiting /ship]
```

For skipped:
```markdown
> [skipped 2026-MM-DD: <reason>]
```

### 7. Final summary

```
Applied: N (direct branch-hygiene / tooling PRs)
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
- ❌ Modify the report's original suggestions (annotate only).
- ❌ Apply a finding without verifying it against current code first.

## Companion skills

- `/ship` — issue → implement → `/fix-review` → merge → close.
- `/fix-review` — parallel multi-model review + Claude Arbiter (part of `/ship`).
- `/curate-minions` — the sibling project-tooling skill, same manual-sync convention.
- `/revival`, `/find-bugs`, `/improve` — complementary read-only checks.

## See also

- `.claude/dreaming/dreaming-prompt.md` — what the dreaming pass looks for.
- `.claude/dreaming/dreaming.sh` — how the pass is run (systemd timer).
- `CLAUDE.md` "Key Constraints" section — target of `constraint-fix` findings.
