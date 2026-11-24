---
name: session-end
description: Write or show the reviewed project handoff (.claude/session-state.md). Preserve decisions and distinguish verified results, untested paths, permissions, and open choices.
---

# /session-end

`/session-end` writes or updates `.claude/session-state.md` from the conversation
and inspected evidence. `/session-end show` reads it; `/session-end all` may also
read `.claude/session-log.md` as historical, unverified material.

Before writing, read the existing handoff and reconcile decisions with current
task records. Preserve still-relevant decisions and their sources; replace
superseded status rather than appending another layer. Use Ukrainian prose and
concrete source references (transcript UUID/time, command result, commit, or
authoritative document section).

Keep the handoff short. Link the project's canonical status/todo documents for
progress; do not duplicate their task lists as current state. For concurrent
tasks, keep each task's result, evidence, pending action, and actual
dependencies together. Preserve applicable earlier verification with its source;
another turn or session does not invalidate it. Include an unresolved item only
when it affects that task or a decision; omit empty sections, never invent gaps
to fill a template.

```markdown
# Перевірений стан — YYYY-MM-DD HH:MM TZ

## Завдання та рішення
- Active objective and decisions needed to continue, with sources.

<!-- Optional, within the relevant task: a material unresolved check and its
effect on completion or a decision. Omit when none; no filler section. -->

## Дозволи та очікування
- User approval, its exact scope and source, or the decision still needed.
- Mark completed/expired approvals; do not turn a past one-off action into standing permission or a permanent prohibition.

## Далі
- Next action and links to current task documents.
```

Read the result back before claiming the handoff was updated. Update it after
material decisions and before switching tasks or ending a long session, rather
than relying on repeated compaction.

The Stop hook `.claude/hooks/session-end.sh` independently writes
`.claude/session-checkpoint.json`: recent verbatim turns with source IDs, bounded
in size, with oversized messages explicitly referred back to their source. It
makes no model calls and never rewrites the reviewed handoff or the legacy log.
The full transcript remains the journal; the checkpoint is only a recent window.

SessionStart injects the reviewed handoff and checkpoint references via
`session-last.sh`. Verbatim turns are included after compaction or when there
is no reviewed handoff.
