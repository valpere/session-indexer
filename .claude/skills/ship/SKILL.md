---
name: ship
description: "session-indexer ship pipeline: issue → implement → review → merge → close. Usage: /ship [--yes|-y] [issue-number | title]"
---

# Skill: /ship
# Issue → Merged PR Pipeline

```
/ship                    → auto-select next open issue (asks confirmation)
/ship --yes              → auto-select and proceed, skip UI prompts
/ship -y                 → same as --yes
/ship {issue-id}         → ship a specific issue by number
/ship {issue title}      → find by title, then ship
/ship --yes {issue-id}   → skip UI prompts (technical ambiguities always ask)

  → resolve issue        (fetch title + description)
  → analyze ambiguities  (scan description + codebase → ask decisions before coding)
  → mark in-progress
  → code-generator       (implement → tests → review agents → simplify → docs → PR)
  → /fix-review          (multi-model rounds + Claude Arbiter — does NOT merge)
  → merge PR             (/ship owns the merge)
  → post comment         (summary: what changed, PR link)
  → close issue
  → log to /self-learn (if installed)
  → final report
```

One command. Pauses only for genuine technical decisions before coding.

---

## STEP 0: Resolve Issue

**Flag detection:** Check for `--yes` / `-y` anywhere in arguments. Set `YES_MODE=true`, strip flag.

**Argument classification** (after stripping flags):
- No argument → auto-select (see below)
- Numeric or `#N` → `gh issue view {N} --repo valpere/session-indexer --json number,title,body,url`
- Otherwise → title search

**YES_MODE:**
- Skips "Proceed?" confirmation prompts and title disambiguation
- **Does NOT skip STEP 0.5** — technical decisions always require input

### If no argument — auto-select

```bash
gh issue list --repo valpere/session-indexer --assignee @me --state open \
  --json number,title,url,milestone,labels --limit 10
```

Prefer issues in the active milestone. Within milestone, prefer `priority:high` / `urgent` labels.

If `YES_MODE=false`, present top candidate and wait:
```
Suggested: #{number} — {title}
Milestone: {milestone} | {url}
Proceed? [Y / pick another / cancel]
```

If `YES_MODE=true`, print selection and proceed immediately.

### If a title string was given

```bash
gh issue list --repo valpere/session-indexer --state open --search "{title}" \
  --json number,title,url --limit 10
```

One close match → use directly. Multiple → present up to 5. None → stop.

### Validation

Extract `ISSUE_NUMBER`, `ISSUE_TITLE`, `ISSUE_BODY`, `ISSUE_URL`.

If issue is not open: stop — "Issue #{N} is already closed."

Print:
```
Shipping: #{ISSUE_NUMBER} {ISSUE_TITLE}
{ISSUE_URL}
```

Mark in-progress:
```bash
gh issue edit {ISSUE_NUMBER} --repo valpere/session-indexer --add-label "in-progress"
```

---

## STEP 0.5: Analyze Task & Resolve Ambiguities

Before touching any code, scan the issue body and codebase for decisions that must be made
first. **Runs even in YES_MODE — technical decisions are not UI prompts.**

Scan for:
- "Decision:", "Options:", "or (b)", "discuss before starting", "if/or" branches
- Two implementation strategies side-by-side
- Scope conditionals: "if not already fixed in #{N}" → verify in code
- Dependency on another issue's output
- Constants or limits where the value isn't specified

For each ambiguity:
1. Search codebase for 1–3 concrete options grounded in existing patterns
2. Classify: multiple options → present with tradeoffs; dependency → verify in code; no options → ask directly
3. Collect all decisions in one pass before proceeding

**Format:**
```
Before I start coding, I need to resolve {N} decision(s):

1. {Ambiguity title}
   Context: {one sentence}
   A) {option} — {tradeoff}
   B) {option} — {tradeoff}
```

Zero ambiguities: print `"No ambiguities — proceeding."` and continue.

---

## STEP 1: Launch Code Generator

```
Agent(code-generator):
  "Implement: {ISSUE_TITLE}

   Issue body:
   {ISSUE_BODY}

   Issue URL: {ISSUE_URL}

   Implementation pipeline:
   1. Git worktree for isolation
   2. Implement the feature/fix
   3. Run: go vet ./... && go test -race ./...
   4. Launch test-generator for new hooks/utils/services — skip if changeset is
      migrations, components, docs, or .claude config files only
   5. Re-run go test -race ./... after test generation
   6. Parallel review: static-analysis + security-reviewer
   7. Apply review findings
   8. code-simplifier → docs-maintainer
   9. Create PR (include '{ISSUE_URL}' in body)
   10. Return PR number + URL"
```

Wait for completion. On error or no PR: print error, stop.

Extract `PR_NUMBER`, `PR_URL`, `BRANCH_NAME`.

Mark in-review:
```bash
gh issue edit {ISSUE_NUMBER} --repo valpere/session-indexer \
  --add-label "in-review" --remove-label "in-progress"
```

---

## STEP 2: Run /fix-review

### Docs-only check

```bash
gh pr diff {PR_NUMBER} --repo valpere/session-indexer --name-only
```

If every changed file matches `*.md`, `*.txt`, `docs/**`, `.github/**/*.md` →
skip /fix-review, go to STEP 3. Print:
```
Skipping /fix-review — PR is docs-only. Code Review Pyramid has nothing to evaluate.
```

Mixed PRs (any code file present): /fix-review runs. When in doubt, run it.

### Standard

```
/fix-review {PR_NUMBER}
```

Multi-model rounds + Claude Arbiter. Does **NOT** merge — STEP 3 owns the merge.

Collect summary (fixed / skipped / open). If unresolved items remain, note them and continue.

---

## STEP 3: Merge PR

```bash
gh pr merge {PR_NUMBER} --repo valpere/session-indexer --squash --delete-branch
```

If checks pending: `gh pr merge {PR_NUMBER} --repo valpere/session-indexer --auto --squash --delete-branch`
then poll every 30s (max 30 min).

If conflicts: stop — "PR #{PR_NUMBER} has merge conflicts. Resolve and re-run."

**Timeout:** 30 min. If still open: ask user to merge manually, then re-run `/ship {ISSUE_NUMBER}`
to complete the tracker update.

Verify:
```bash
gh pr view {PR_NUMBER} --repo valpere/session-indexer --json state,mergedAt --jq '{state,mergedAt}'
```

---

## STEP 4: Post Completion Comment

```bash
gh issue comment {ISSUE_NUMBER} --repo valpere/session-indexer --body "$(cat <<'EOF'
## Shipped — PR #{PR_NUMBER}

**Summary:** {one-paragraph description of what changed and why}

**Key changes:**
- {file or component}: {what changed and why}

**Tests:** {what was added or verified}

**PR:** {PR_URL}
EOF
)"
```

If the call fails: warn and continue.

---

## STEP 5: Close Issue

```bash
gh issue close {ISSUE_NUMBER} --repo valpere/session-indexer \
  --comment "Shipped in PR #{PR_NUMBER}."
```

On failure: warn, ask user to close {ISSUE_URL} manually.

---

## STEP 6: Log to /self-learn (if installed)

Skip silently if `.claude/skills/self-learn/` doesn't exist in this project.

`/self-learn log`'s Step 1 is written for interactive human invocation
("Ask the user: what happened?") — `/ship` runs this step itself, without
a human in the loop, so do **not** wait for input. Classify and log
directly using what you already observed during this run, following the
self-learn LOG step's own field schema and "if the user describes the
event, classify it yourself" allowance:
- Smooth run, zero `/fix-review` escalations → **win**
- `/fix-review` caught something that should have been prevented earlier
  (wrong pattern, missed edge case, convention violation) → **mistake**
- STEP 0.5 ambiguity analysis surfaced a decision that would otherwise have
  produced a wrong implementation → **win**

If the log call fails or `/self-learn` isn't installed, don't block the
pipeline on it — note it in the final report's Warnings and move on.

---

## STEP 7: Final Report

```
## /ship complete — #{ISSUE_NUMBER} {ISSUE_TITLE}

Issue:   {ISSUE_URL}  (closed)
PR:      {PR_URL}  (merged, branch deleted)

/fix-review: {fixed} fixed · {skipped} skipped · {open} still open

Pipeline:
✓ Issue resolved + ambiguities cleared
✓ Code generated (code-generator)
✓ go vet ./... && go test -race ./... passed
✓ test-generator ran (or skipped — docs/migrations only)
✓ static-analysis | security-reviewer
✓ code-simplifier + docs-maintainer
✓ PR created + /fix-review (multi-model + Arbiter)
✓ PR merged (squash)
✓ Completion comment posted
✓ Issue closed
```

Append **Warnings** for any skipped items or non-fatal failures.

---

## RULES

1. Issue must be ready/approved before shipping — not auto-verified.
2. One PR per issue — reuse existing branch PR if it exists.
3. Never force-push — rebase if branch diverged from main.
4. **`/ship` owns the merge (STEP 3), not `/fix-review`.**
5. All review agents run inside code-generator — do not re-launch in `/ship`.
6. Tracker updates are best-effort — surface failures, never silently skip.
7. 30-minute merge timeout — stop and report. Never loop indefinitely.
8. Docs-only PRs skip /fix-review — the Pyramid has nothing to evaluate.
