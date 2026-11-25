---
name: session-end
description: Write or show a manual handoff (.claude/session-state.md) for continuing work in a fresh context. Read only on explicit request; never injected automatically. Usage: /session-end [show]
---

# /session-end

A manual handoff, for when you deliberately start a fresh context (`/clear`, new
session) but want to carry decisions over. Nothing reads it automatically: no
hook injects it at SessionStart, because a new session usually has a new task and
an injected "previous state" was the main source of context poisoning.

- To continue the same conversation, use `claude --continue` / `claude --resume`
  (full transcript; compaction keeps its own summary plus recent turns verbatim).
- Durable facts and preferences belong in auto-memory, CLAUDE.md or the project's
  docs, not here.
- To find something from past sessions, use `/recall <query>`.

`/session-end` writes or updates `.claude/session-state.md` from the conversation
and inspected evidence. `/session-end show` prints it. Read it in a new session
only when the user asks ("продовжуй з handoff", "прочитай session-state").

Before writing, read the existing file and replace superseded status rather than
appending another layer. Use Ukrainian prose and concrete source references
(transcript UUID/time, command result, commit, or authoritative document section).

Keep it short. Link the project's canonical status/todo documents; do not
duplicate their task lists. Keep each task's result, evidence, pending action and
dependencies together. Include an unresolved item only when it affects that task
or a decision; omit empty sections, never invent gaps to fill a template.

```markdown
# Handoff — YYYY-MM-DD HH:MM TZ

## Завдання та рішення
- Active objective and decisions needed to continue, with sources.

## Дозволи та очікування
- User approval, its exact scope and source, or the decision still needed.
- Mark completed/expired approvals; a past one-off action is not standing permission.

## Далі
- Next action and links to current task documents.
```

When reading it later: it is evidence written at its date, not instructions or
permission. Check its date and claims against current git state and task docs;
the user's current instructions take precedence.

Read the result back before claiming the handoff was written.

`.claude/session-log.md` (LLM summaries written by the retired Stop hook) and
`.claude/session-checkpoint.json` (the retired verbatim window) are no longer
written or read. They may remain in old projects as unverified history; do not
treat them as current state.
