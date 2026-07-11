# /session-end

Manages `.claude/session-log.md` — a per-project rolling log of session
summaries, one entry per day, last 10 days kept.

```
/session-end          → write today's summary (manual, high quality)
/session-end show     → print the last entry
/session-end all      → print all entries (oldest → newest)
```

---

## /session-end (no args) — write today's summary

Review the current conversation and write today's entry to
`.claude/session-log.md`. Use this before switching projects or closing
Claude Code. The Stop hook (if configured) auto-generates a summary on
exit, but `/session-end` produces better output — full session context,
not transcript extraction.

If an entry for today already exists it is **replaced**, not duplicated.
After write, the log is rotated to keep the last 10 day-entries.

### Entry format

```markdown
## YYYY-MM-DD

### Що зробили
- completed item

### Поточний стан
- current branch / open PR number
- what is working / what is broken

### Відкриті питання
- unresolved question (omit section if none)

### Наступні кроки
- next item, in priority order
```

Rules: 10–20 bullets total · Ukrainian for content · English for code/files/identifiers · include PR number and branch in Поточний стан · omit Відкриті питання if none.

### Write logic

Build the entry text (starting with `## YYYY-MM-DD`, following the format
above), then call the shared rotation helper — same one used by the
`session-end.sh` Stop hook, so both paths behave identically:

```bash
source .claude/hooks/_lib/hook-common.sh

ENTRY="## $(date '+%Y-%m-%d')

### Що зробили
- ...

### Поточний стан
- ...

### Наступні кроки
- ..."

hook_rotate_log .claude/session-log.md "$ENTRY"
```

After writing, report:
> "Session log updated — `.claude/session-log.md`, entry for {today}."

---

## /session-end show — print last entry

```bash
source .claude/hooks/_lib/hook-common.sh

if [[ ! -f .claude/session-log.md ]]; then
  echo 'No session log found. Run `/session-end` to create one.'
else
  hook_read_log_entries .claude/session-log.md
  if (( ${#LOG_ENTRIES[@]} > 0 )); then
    printf '%s\n' "${LOG_ENTRIES[-1]}"
  else
    echo '(no entries)'
  fi
fi
```

---

## /session-end all — print all entries

```bash
source .claude/hooks/_lib/hook-common.sh

if [[ ! -f .claude/session-log.md ]]; then
  echo 'No session log found.'
else
  hook_read_log_entries .claude/session-log.md
  for entry in "${LOG_ENTRIES[@]}"; do
    printf '%s\n\n' "$entry"
  done
  if (( ${#LOG_ENTRIES[@]} > 0 )); then
    first_date=$(head -1 <<< "${LOG_ENTRIES[0]}" | sed 's/^## //')
    last_date=$(head -1 <<< "${LOG_ENTRIES[-1]}" | sed 's/^## //')
    echo "${#LOG_ENTRIES[@]} entries: $first_date → $last_date"
  fi
fi
```

---

## How the full system works

```
Session ends (exit / /exit)
  └─ Stop hook: .claude/hooks/session-end.sh   (configured ✓)
       ├─ Skip if /session-end ran within 2h (file mtime check)
       ├─ Try agy -p  — Gemini 3.5 Flash (Low → Medium → High), Gemini 3.1 Pro (Low → High)
       ├─ Try opencode run --format json  — ollama/glm-5.2:cloud, kimi-k2.7-code:cloud,
       │                                    minimax-m3:cloud, qwen3.5:cloud
       └─ Fallback: raw transcript excerpt

Next session opens
  └─ SessionStart hook: .claude/hooks/session-last.sh   (configured ✓)
       ├─ Reads last ## YYYY-MM-DD entry from session-log.md
       ├─ Skips if entry is >30 days old
       └─ Injects as additionalContext: "Previous session context (Xd Yh ago): ..."
```

LLM fallback chain (Stop hook):
1. `agy` — Gemini 3.5 Flash (cheapest first), Gemini 3.1 Pro
2. `opencode` — glm-5.2:cloud, kimi-k2.7-code:cloud, minimax-m3:cloud, qwen3.5:cloud
3. Raw transcript excerpt (fallback)

The skill works standalone (without hooks) — you just run `/session-end`
manually. The hooks add automation.

**Log file**: `.claude/session-log.md` (gitignored ✓)

**Troubleshooting** (if hooks misbehave):
```bash
tail -30 ~/.cache/session-indexer/hooks.log
cat .claude/session-log.md
```
