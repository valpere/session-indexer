# /deliver — Portability Guide

Install reference for Claude Code agents.

---

## What this skill does

Full pipeline from issue to merged PR: resolve issue → analyze ambiguities →
implement (code-generator agent) → multi-model review (/fix-review) → merge →
post comment → close issue.

Commands: `/deliver`, `/deliver --yes`, `/deliver {issue-id}`, `/deliver {title}`

The `--yes` flag skips UI confirmation prompts. Technical ambiguities (STEP 0.5)
always require input regardless of YES_MODE.

---

## Governance prerequisite

Any change to `<project>/.claude/` requires explicit user permission.
Before installing, confirm the user has authorized it.
(Global rule: `~/.claude/CLAUDE.md` §"Правило: зміни в `.claude/`")

---

## Install

**Recommended: run `/generate-deliver`** — it discovers the project's stack, issue
tracker, and agent names, then writes a fully wired version automatically.

**Manual install** (canonical source: `~/wrk/common_claude/skills/deliver/`):

```bash
mkdir -p .claude/skills/deliver/
cp ~/wrk/common_claude/skills/deliver/SKILL.md .claude/skills/deliver/
cp ~/wrk/common_claude/skills/deliver/PORTABILITY-GUIDE.md .claude/skills/deliver/
```

Then fill in the placeholders in `.claude/skills/deliver/SKILL.md`:

| Placeholder | What to set |
|-------------|------------|
| `{REPO}` | `gh repo view --json nameWithOwner --jq .nameWithOwner` |
| `{BUILD_CMD}` | e.g. `go build ./...` or `npm run build` |
| `{LINT_CMD}` | e.g. `go vet ./...` or `npm run lint` |
| `{TEST_CMD}` | e.g. `go test -race ./...` or `npm test` |
| `{AGENT_NAME}` | name of code-generator agent in `.claude/agents/` |

---

## Issue tracker

The canonical SKILL.md only supports GitHub Issues (`gh` CLI, no MCP needed) —
list, fetch, mark in-progress/in-review, comment, and close all go through
`gh issue *`. No other tracker adapter is currently wired up.

---

## Dependencies

| Dependency | Required | Notes |
|------------|----------|-------|
| `/fix-review` skill | Yes | `.claude/skills/fix-review/` (project-local) |
| `{AGENT_NAME}` agent | Yes | In `.claude/agents/` — discovered by `/generate-deliver` |
| `static-analysis` agent | No | Run inside code-generator |
| `security-reviewer` agent | No | Run inside code-generator |
| `test-generator` agent | No | Run inside code-generator for hooks/utils/services |

---

## Integration with /self-learn

After `/deliver` completes, log to `/self-learn`:
- Smooth run with no /fix-review escalations → win
- /fix-review caught something that should have been prevented earlier → mistake
- Ambiguity analysis saved a wrong implementation choice → win
