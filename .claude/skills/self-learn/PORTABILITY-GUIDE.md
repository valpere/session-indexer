# /self-learn — Portability Guide

Install reference for Claude Code agents.

---

## What this skill does

Structured feedback loop: logs sourced mistakes and wins as JSONL, identifies
recurring patterns as scoped improvement candidates (without auto-editing `CLAUDE.md`),
and runs retrospectives to surface systemic insights.

Commands: `/self-learn` (status) · `/self-learn log` · `/self-learn retro`
· `/self-learn init` · `/self-learn tips`

---

## Governance prerequisite

Any change to `<project>/.claude/` requires explicit user permission.
Before installing, confirm the user has authorized adding this skill.
(Global rule: `~/.claude/CLAUDE.md` §"Правило: зміни в `.claude/`")

---

## Install

Canonical source: `~/wrk/common_claude/skills/self-learn/`

### Quick start (generic)

```bash
mkdir -p .claude/skills/self-learn/
cp ~/wrk/common_claude/skills/self-learn/SKILL.md .claude/skills/self-learn/
cp ~/wrk/common_claude/skills/self-learn/PORTABILITY-GUIDE.md .claude/skills/self-learn/
```

Then run `/self-learn init` — it creates `_patterns/` and `_knowledge-base/`
in the project root if they don't exist.

Verify: `.claude/skills/self-learn/SKILL.md` exists and `_patterns/mistakes.jsonl` was created.

### Full inject (project-specific)

```bash
mkdir -p .claude/skills/generate-self-learn
cp ~/wrk/common_claude/skills/self-learn/generate/SKILL.md .claude/skills/generate-self-learn/SKILL.md
```

Then run `/generate-self-learn` — reads `CLAUDE.md`, extracts explicit banned
patterns, pre-populates `_patterns/anti-patterns.md` with them, and creates
`_patterns/`. Do not wire `PostToolUse` prompt hooks or unverified LLM `Stop`
autolog hooks. `generate-self-learn` can be deleted from `.claude/skills/` once it's run.

---

## Optional: project name override

If the working directory name isn't a good project identifier, create
`.claude/skills/self-learn/config`:

```
project_name: My Project
```

The skill checks this file first, then falls back to the directory name.

---

## Optional: .gitignore

Raw JSONL files are per-session learning data. Pick a policy:

| Policy | Gitignore entries |
|--------|------------------|
| Solo / sensitive | `_patterns/mistakes.jsonl`, `_patterns/wins.jsonl` |
| Team learning | commit everything |
| Summaries only | gitignore JSONL, keep `.md` files tracked |

---

## Usage cadence

After any significant task: `/self-learn log`

| Project activity | Retro cadence |
|-----------------|---------------|
| Active development (daily commits) | Weekly |
| Maintenance mode | Bi-weekly |
| Sprint-based | End of each sprint |
| Ad-hoc | When `mistakes.jsonl` hits 10+ entries |

---

## Integration with other skills

These skills produce good logging candidates — add reminders to CLAUDE.md
after wiring them in:

| Skill | What to log |
|-------|-------------|
| `/fix-review` | Arbiter confirmations → mistake patterns; dismissals → anti-patterns |
| `/deliver` | Smooth pipeline → win; late-caught issues → mistake |
| `/live-test` | Bugs found → mistake (caught earlier?); flows that caught real issues → win |

Check which of these skills are available in the target project before
adding integration reminders.

---

## How candidates work

```
2× same pattern  →  recorded as a scoped improvement candidate with source examples (no auto-edits to CLAUDE.md)
3× same pattern  →  recorded with its conditions and scope in _patterns/anti-patterns.md (not a universal prohibition)
win.reusable_in covers 2+ areas  →  recorded in _patterns/cross-project.md
```

---

## File formats

### mistakes.jsonl

```json
{"date":"2026-03-30","project":"growth-core","task":"Steam API sync","mistake":"Used wrong date format (ISO instead of DD-MM-YYYY)","resolution":"Added format conversion in sync function","pattern":"Check date format expectations before first API call","severity":"medium","category":"api_error","had_verification":false}
```

### wins.jsonl

```json
{"date":"2026-03-30","project":"growth-core","task":"Manager RPC design","win":"Used SECURITY DEFINER RPCs instead of client-side service key","pattern":"Server-side functions for cross-RLS access are safer than exposing service keys","reusable_in":"any Supabase project with role-based data access","had_verification":true,"used_plan_mode":true,"delegation":"none","decomposed":false}
```

### Severity guide

| Severity | When |
|----------|------|
| `high` | Production impact, data loss, or >30 min rework |
| `medium` | Multiple retries, cascading wrong assumptions |
| `low` | Minor inconvenience, caught quickly |
