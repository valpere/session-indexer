You are doing a **dreaming pass** for the **session-indexer** project —
async, scheduled curation of project context. This is sleep-time
consolidation: review what accumulated since last pass, identify
patterns, suggest curation. Read-only — output a report, don't modify
anything.

## Project context

- **session-indexer** — single Go binary, semantic search over Claude
  Code JSONL session transcripts. No daemon, no shared state between
  projects, pure Go/no CGO. Six subcommands (`mine`, `search`, `embed`,
  `stats`, `distill`, `facts`, `sessions`, `list`, `show`).
- Deployed and actively maintained via systemd across 8+ projects
  (`~/wrk/{common,projects/*/*,freelance/*/*}`) — this dreaming pass
  covers only **this repo's own code and docs**, not the deployed
  instances.
- Workflow: `/ship` (issue → implement → `/fix-review` → merge → close)
  — no `/backlog` step here; issues are created directly (`gh issue
  create` or manually) and `/ship <issue-number>` picks them up.
- "Before Any Commit" convention: a branch with an already-merged PR is
  stale — never commit onto it directly, branch fresh from `main`
  instead (see `CLAUDE.md`).

## Targets

| Path | What to look for |
|------|-------------------|
| `CLAUDE.md` "Key Constraints" | Recent commits that may violate: pure-Go/no-CGO, the Go 1.26.6+ patch pin (CVE-driven — check `go.mod`'s `go` directive hasn't regressed to an unpinned/older patch), the 60s Stop-hook budget (50s internal deadline), per-project isolation (no cross-project reads/writes) |
| `docs/architecture.md` | Stale description of the Facts Layer / storage schema / search fallback logic vs. current `internal/` code |
| `internal/db/schema.sql` vs `internal/types.go` | Schema drift — a column that changed without inline comments in both, or `schema_version` not bumped when it should have been |
| Recent PR review comments | Recurring `/fix-review` themes across merged PRs |
| `.claude/skills/` | Large roster (`curate-minions`, `debug`, `doubt-driven-development`, `find-bugs`, `fix-review`, `grill-me`, `housekeeping`, `improve`, `revival`, `self-learn`, `ship`) — flag any two that now read as overlapping |
| `git log --since="30 days ago"` | Commits onto a branch that already had a merged PR (the exact anti-pattern `CLAUDE.md`'s "Before Any Commit" section exists to prevent) |
| `internal/distill/` | Any change to the confidence gate, supersession logic, or `--context-cap`/`--batch` defaults not reflected in `CLAUDE.md`'s Facts Layer section |

## What to find

### 1. Key-Constraints violations

For each of the 5 bullets in `CLAUDE.md`'s "Key Constraints", search
recent commits (`git log --since="30 days ago" -p`) for anything that
plausibly violates it:
- CGO reintroduced (`import "C"`, a non-`modernc.org/sqlite` driver).
- `go.mod`'s `go` directive dropped below `1.26.6` or the comment
  explaining the patch pin was removed without justification.
- A new code path in `mine`/hook integration that could exceed the 60s
  budget (no context deadline, an unbounded loop, a synchronous
  network call without a timeout).
- Any query or file path that isn't scoped to `--db <path>` (a
  cross-project leak).

### 2. Architecture/schema drift

Spot-check `docs/architecture.md`'s Facts Layer description (confidence
gate, supersession, tombstone-via-`until`) against `internal/distill/
distill.go` and `internal/facts/facts.go`. Flag anything the doc claims
that the code no longer does, or vice versa. Same check for
`internal/db/schema.sql` vs. `internal/types.go`'s row structs.

### 3. Recurring `/fix-review` themes

`gh pr list --state merged --limit 20` + `gh pr view N --json comments`.
A theme repeating 3+ times is a candidate for a new `CLAUDE.md`
convention or a `self-learn` pattern entry.

### 4. Skill roster overlap

Read `.claude/skills/*/SKILL.md` frontmatter descriptions — do
`debug`/`doubt-driven-development`/`find-bugs`/`improve`/`revival`
still carve out genuinely distinct territory, or has usage converged
on 1-2 of them with the rest going stale?

### 5. Stale-branch commit anti-pattern

`git log --all --since="30 days ago"` — for each branch with commits,
check `gh pr list --state merged --search "head:<branch>"`. Flag any
branch that received a commit *after* its PR was already merged (the
exact mistake `CLAUDE.md`'s "Before Any Commit" section documents a
real past incident about).

## Report format

```markdown
# session-indexer dreaming — <ISO week>

## TL;DR
<3-5 bullet summary>

## 1. Key-Constraints violations
### a) <finding>
- Confidence: high|medium|low
- Evidence: <file:line, commit sha>
- Suggest: <action>

## 2. Architecture/schema drift
...

## 3. Recurring /fix-review themes
...

## 4. Skill roster overlap
...

## 5. Stale-branch commit anti-pattern
...

## 6. Open questions
<what you couldn't verify from a read-only pass>
```

Confidence levels: **high** = directly verified against a specific
file/commit; **medium** = pattern observed but not exhaustively
checked; **low** = a hunch worth someone's attention, not a confirmed
finding. Don't fabricate evidence — say "couldn't verify" rather than
guess. This project is deployed to 8+ other projects via systemd — a
false "Key Constraints violation" claim wastes review time on
something that could break production maintenance across all of them.
