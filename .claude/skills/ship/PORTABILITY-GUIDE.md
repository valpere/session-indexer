# /ship — Portability Guide

Install reference for Claude Code agents.

---

## What this skill does

Full pipeline from issue to merged PR: resolve issue → analyze ambiguities →
implement (code-generator agent) → multi-model review (/fix-review) → merge →
post comment → close issue.

Commands: `/ship`, `/ship --yes`, `/ship {issue-id}`, `/ship {title}`

The `--yes` flag skips UI confirmation prompts. Technical ambiguities (STEP 0.5)
always require input regardless of YES_MODE.

---

## Governance prerequisite

Any change to `<project>/.claude/` requires explicit user permission.
Before installing, confirm the user has authorized it.
(Global rule: `~/.claude/CLAUDE.md` §"Правило: зміни в `.claude/`")

---

## Install

**Recommended: run `/generate-ship`** — it discovers the project's stack, issue
tracker, and agent names, then writes a fully wired version automatically.

**Manual install** (canonical source: `~/wrk/common/skills/ship/`):

```bash
mkdir -p .claude/skills/ship/
cp ~/wrk/common/skills/ship/SKILL.md .claude/skills/ship/
cp ~/wrk/common/skills/ship/PORTABILITY-GUIDE.md .claude/skills/ship/
```

Then fill in the placeholders in `.claude/skills/ship/SKILL.md`:

| Placeholder | What to set |
|-------------|------------|
| `{REPO}` | `gh repo view --json nameWithOwner --jq .nameWithOwner` |
| `{BUILD_CMD}` | e.g. `go build ./...` or `npm run build` |
| `{LINT_CMD}` | e.g. `go vet ./...` or `npm run lint` |
| `{TEST_CMD}` | e.g. `go test -race ./...` or `npm test` |
| `{AGENT_NAME}` | name of code-generator agent in `.claude/agents/` |

---

## Issue tracker adapter

The canonical SKILL.md uses GitHub Issues by default (`gh` CLI, no MCP needed).
For other trackers, replace the `<!-- TRACKER -->` blocks:

| Operation | GitHub Issues (default) | ClickUp (MCP) |
|-----------|------------------------|---------------|
| List open | `gh issue list --assignee @me` | `clickup_search(statuses=["TODO","DEVELOPMENT"])` |
| Fetch | `gh issue view {N} --json ...` | `clickup_get_task(task_id=ID)` |
| Mark in-progress | `gh issue edit --add-label in-progress` | `clickup_update_task(status="DEVELOPMENT")` |
| Mark in-review | `gh issue edit --add-label in-review` | `clickup_update_task(status="CHECK")` |
| Post comment | `gh issue comment {N} --body "..."` | `clickup_create_task_comment(task_id, text)` |
| Close | `gh issue close {N}` | `clickup_update_task(status="APPROVED")` |
| Sprint detection | GitHub Milestone (`--milestone`) | `clickup_get_workspace_hierarchy()` |

---

## Dependencies

| Dependency | Required | Notes |
|------------|----------|-------|
| `/fix-review` skill | Yes | `.claude/skills/fix-review/` or user-level |
| `{AGENT_NAME}` agent | Yes | In `.claude/agents/` — discovered by `/generate-ship` |
| `static-analysis` agent | No | Run inside code-generator |
| `security-reviewer` agent | No | Run inside code-generator |
| `test-generator` agent | No | Run inside code-generator for hooks/utils/services |
| Tracker MCP | Only for non-GitHub | ClickUp / Linear MCP if not using gh CLI |

---

## Integration with /self-learn

After `/ship` completes, log to `/self-learn`:
- Smooth run with no /fix-review escalations → win
- /fix-review caught something that should have been prevented earlier → mistake
- Ambiguity analysis saved a wrong implementation choice → win
