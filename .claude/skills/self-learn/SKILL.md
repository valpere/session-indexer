---
name: self-learn
description: "Self-learning system — log mistakes/wins, run retrospectives, promote patterns to hard rules, and coach on workflow quality. Usage: /self-learn [log|retro|status|init|tips]"
---

# Skill: /self-learn
# Portable Self-Learning System

---

## OVERVIEW

A self-learning loop that captures mistakes and wins, promotes recurring patterns
to hard rules, runs periodic retrospectives, and provides proactive workflow coaching.

```
/self-learn              → show status (counts, recent entries, pending promotions)
/self-learn log          → interactive: ask what happened, classify, and log it
/self-learn retro        → run a full retrospective analysis
/self-learn init         → initialize _patterns/ and _knowledge-base/ for a new project
/self-learn tips         → analyze recent patterns and give personalized workflow tips
```

The system gets smarter with every interaction. Log from day one.

---

## STEP 0: Resolve Command

Parse the argument:
- No argument or `status` → jump to **STATUS**
- `log` → jump to **LOG**
- `retro` → jump to **RETRO**
- `init` → jump to **INIT**
- `tips` → jump to **TIPS**

---

## INIT — Bootstrap for New Project

Check if the directories and files exist. Create anything missing:

```
_patterns/
  mistakes.jsonl          # one JSON object per line
  wins.jsonl              # one JSON object per line
  cross-project.md        # patterns that apply across projects/modules
  anti-patterns.md        # approaches that keep failing — stop trying them
_knowledge-base/
  decisions.md            # Architecture Decision Records
  api-docs/               # per-API gotcha files (e.g. steam-connect.md, supabase.md)
```

For each file, create it only if it doesn't exist. Never overwrite existing data.

Also check for `.claude/skills/self-learn/config` — if absent, do not create it
(it's optional; the user creates it manually to set `project_name:`).

**`mistakes.jsonl`** — create empty (0 bytes).
**`wins.jsonl`** — create empty (0 bytes).

**`cross-project.md`** — create with header:
```markdown
# Cross-Project Patterns

Patterns proven in 2+ areas/projects. Promoted automatically by /self-learn retro.

---
```

**`anti-patterns.md`** — create with header:
```markdown
# Anti-Patterns

Approaches that keep failing. When you're about to try one of these, stop and find an alternative.

---
```

**`_knowledge-base/decisions.md`** — create with header:
```markdown
# Architecture Decision Records

| Date | Decision | Context | Alternatives Considered |
|------|----------|---------|------------------------|
```

After creating, print:
> "Self-learning initialized. Directories and pattern files are ready.
> Start logging with `/self-learn log` after any significant task."

---

## LOG — Capture a Mistake or Win

### Step 1: Determine type

Ask the user:
> "What happened? I'll classify it as a **mistake** (something went wrong or was corrected)
> or a **win** (something worked well, was confirmed, or saved time).
>
> Describe it briefly, or say 'mistake' or 'win' to start from the category."

If the user describes the event, classify it yourself:
- User correction, failed approach, wrong assumption, redo → **mistake**
- Confirmed approach, clever solution, time saved, bug caught early → **win**

Confirm the classification before logging:
> "I'd classify this as a [mistake/win]. Sound right?"

### Step 2: Build the entry

**For a mistake**, gather these fields (ask for any you can't infer from context):

Project name resolution order: `.claude/skills/self-learn/config` (key `project_name:`)
→ basename of current working directory → ask the user.

```json
{
  "date": "YYYY-MM-DD",
  "project": "<config file → working directory name → ask>",
  "task": "<what was being done>",
  "mistake": "<what went wrong>",
  "resolution": "<how it was fixed>",
  "pattern": "<generalizable lesson — one sentence>",
  "severity": "low|medium|high",
  "category": "<see category list below>",
  "had_verification": true|false,
  "session_hygiene": "<optional: kitchen_sink|correction_spiral|context_overload|null>"
}
```

**Mistake categories** (classify every mistake into one):

| Category | When to use |
|----------|------------|
| `api_error` | Wrong endpoint, format, auth, or field name |
| `wrong_assumption` | Guessed instead of verified (module name, flag, behavior) |
| `missed_context` | Didn't read docs, existing code, or CLAUDE.md |
| `didnt_ask` | Acted on ambiguity instead of clarifying |
| `prompt_quality` | Vague or under-specified prompt led to wrong output |
| `context_waste` | Didn't `/clear`, kitchen-sink session, or context overload |
| `skipped_planning` | Jumped to code without plan mode on a multi-file task |
| `tooling_error` | Wrong tool, flag, command, or config |
| `other` | Doesn't fit above categories |

**Session hygiene flags** (optional — set when relevant):

| Flag | When to use |
|------|------------|
| `kitchen_sink` | Mixed unrelated tasks in one session without `/clear` |
| `correction_spiral` | 3+ corrections on the same issue in one session |
| `context_overload` | Context window filled with irrelevant files/output |
| `infinite_exploration` | Unbounded investigation that read too many files |

Severity guide:

| Severity | Definition |
|----------|-----------|
| **high** | Production impact, data loss, or significant rework (>30 min wasted) |
| **medium** | Multiple retries, wasted significant time, wrong assumptions that cascaded |
| **low** | Minor inconvenience, caught quickly, small rework |

**For a win**, gather these fields:

```json
{
  "date": "YYYY-MM-DD",
  "project": "<config file → working directory name → ask>",
  "task": "<what was being done>",
  "win": "<what worked well>",
  "pattern": "<reusable lesson — one sentence>",
  "reusable_in": "<where else this applies>",
  "had_verification": true|false,
  "used_plan_mode": true|false,
  "delegation": "subagent|manual|none",
  "decomposed": true|false
}
```

**Field explanations for new fields:**
- `had_verification` — Did the task have tests, linter checks, screenshots, or expected outputs for self-checking? (The #1 best practice from Anthropic: "the single highest-leverage thing you can do")
- `used_plan_mode` — Was plan mode / explore-first approach used before implementation?
- `delegation` — Was work delegated to subagents to protect main context?
- `decomposed` — Was the work broken into phases (search → plan → execute → verify) rather than a monolithic prompt?

### Step 3: Append to file

Append the JSON object as a single line to the appropriate file:
- Mistakes → `_patterns/mistakes.jsonl`
- Wins → `_patterns/wins.jsonl`

Use the Write tool to append (read existing content, add new line, write back).
Never overwrite — always append.

### Step 4: Check for promotion triggers

After logging, immediately check:

**For mistakes:**
1. Read all entries in `mistakes.jsonl`
2. Extract the `pattern` field from each
3. Group similar patterns (same API, same type of error, same root cause)
4. If any pattern appears **2+ times** → trigger promotion:

> "This mistake has occurred [N] times. Promoting to a hard rule in CLAUDE.md:
>
> - **[RULE NAME]** *(promoted YYYY-MM-DD — [N] mistakes)*: [instruction].
>   Never [bad approach] — always [correct approach]. Verify by [verification step]."

Then actually add the rule to the project's CLAUDE.md under a `## Self-Learning Hard Rules`
section (create the section if it doesn't exist). Add it at the end of that section.

**For wins:**
1. Read the `reusable_in` field
2. If it mentions 2+ distinct areas or "all projects" → add to `_patterns/cross-project.md`

### Step 5: Confirm

Print the logged entry and any promotions:
> "Logged [mistake/win]. [N] total [mistakes/wins] tracked.
> [Promoted to hard rule in CLAUDE.md. / No promotion triggered.]"

---

## STATUS — Dashboard

Read the pattern files and display a summary:

```
## Self-Learning Status

### Pattern Store
- Mistakes: [N] logged ([H] high, [M] medium, [L] low)
- Wins: [N] logged
- Cross-project patterns: [N]
- Anti-patterns: [N]
- Hard rules promoted: [N]

### Verification Rate
- Tasks with verification: [N]% (wins: [X]%, mistakes: [Y]%)
- Insight: [correlation observation]

### Mistake Categories
| Category | Count | % |
|----------|-------|---|
| [category] | [N] | [%] |

### Session Hygiene
- Kitchen sink sessions: [N]
- Correction spirals: [N]
- Context overloads: [N]

### Recent (last 5)
| Date | Type | Summary |
|------|------|---------|
| ... | mistake/win | pattern field |

### Pending Promotions
[List any patterns that have occurred 2+ times but haven't been promoted yet]

### Last Retrospective
[Date of last retro, or "Never — run /self-learn retro"]
```

---

## RETRO — Full Retrospective

### Step 1: Gather data

Read all of:
- `_patterns/mistakes.jsonl` — parse each line as JSON
- `_patterns/wins.jsonl` — parse each line as JSON
- `_patterns/cross-project.md`
- `_patterns/anti-patterns.md`
- The project's CLAUDE.md (for existing hard rules)

If `mistakes.jsonl` + `wins.jsonl` have fewer than 3 entries total:
> "Not enough data for a meaningful retrospective. Log more interactions first.
> Current count: [N] mistakes, [M] wins."
Stop.

### Step 2: Analyze

**Group mistakes by category:**
Count entries per `category` field. If entries lack the field, infer from description.

**Identify recurring patterns:**
- Any mistake pattern occurring 2+ times → flag for hard rule promotion
- Any mistake pattern occurring 3+ times → flag as anti-pattern

**Verification correlation:**
- Calculate win rate for tasks WITH verification vs WITHOUT
- If tasks without verification fail 2x+ more → flag for hard rule

**Plan mode correlation:**
- Calculate success rate for tasks that used plan mode vs didn't
- If skipping planning correlates with failures → flag for hard rule

**Session hygiene analysis:**
- Count each session_hygiene flag across all mistakes
- If any flag appears 3+ times → promote to anti-pattern

**Delegation analysis:**
- Compare success rate of tasks using subagent delegation vs none
- If delegation correlates with fewer context-related failures → flag as win pattern

**Decomposition analysis:**
- Compare success rate of decomposed tasks (phased) vs monolithic prompts
- If monolithic tasks fail more → promote decomposition as a best practice

**Identify cross-project wins:**
- Any win with `reusable_in` mentioning 2+ areas

**Check for stale patterns:**
- Any hard rule in CLAUDE.md that hasn't been triggered in 30+ days
- Any anti-pattern that might no longer apply (tech/API changed)

### Step 3: Generate report

```markdown
## Retrospective Report — {date}

### Stats
- Period: {earliest entry date} → {latest entry date}
- Mistakes: {N} ({high} high, {medium} medium, {low} low)
- Wins: {N}

### Verification Impact
- Tasks WITH verification: {N}% success rate
- Tasks WITHOUT verification: {N}% success rate
- Verdict: {observation}

### Planning Impact
- Tasks WITH plan mode: {N}% success rate
- Tasks WITHOUT plan mode: {N}% success rate
- Verdict: {observation}

### Top Mistake Categories
| Category | Count | Trend |
|----------|-------|-------|
| {category} | {N} | ↑ ↓ → |

### Session Hygiene Issues
- Kitchen sink sessions: {N} → {recommendation}
- Correction spirals: {N} → {recommendation}
- Context overloads: {N} → {recommendation}

### Recurring Mistakes (Need Hard Rules)
{For each pattern occurring 2+ times:}
- **{pattern}** — occurred {N} times (dates: {list})
  → PROMOTE to CLAUDE.md as hard rule

### Anti-Patterns to Stop
{For each pattern occurring 3+ times:}
- **{pattern}** — tried {N} times, failed every time
  → Add to anti-patterns.md

### Wins Worth Replicating
{For each win with broad applicability:}
- **{pattern}** — applicable to: {reusable_in}

### Cross-Project Opportunities
{Patterns from one area that could solve problems in another}

### Knowledge Base Updates
{New API gotchas, new decisions to document}

### Stale Patterns
{Hard rules or anti-patterns that may no longer apply}

### Action Items
- [ ] {specific update to make}
```

### Step 4: Auto-apply

After generating the report, apply the changes:

1. **Promote recurring mistakes** to CLAUDE.md (under `## Self-Learning Hard Rules`)
2. **Add new anti-patterns** to `_patterns/anti-patterns.md`
3. **Add cross-project patterns** to `_patterns/cross-project.md`
4. **Flag stale patterns** for user review (don't auto-remove)

### Step 5: Confirm

Print the report and list what was changed:
> "Retrospective complete. Applied:
> - [N] new hard rules promoted to CLAUDE.md
> - [N] new anti-patterns logged
> - [N] cross-project patterns updated
> - [N] stale patterns flagged for review
>
> Anything surprise you? Anything I should weight differently?"

---

## TIPS — Personalized Workflow Coaching

Analyze the pattern store and give actionable, personalized tips based on the user's
actual tracked data. These tips come from official Claude Code best practices,
real production patterns, and the user's own history.

### Step 1: Read pattern data

Read `_patterns/mistakes.jsonl` and `_patterns/wins.jsonl`.

### Step 2: Analyze and generate tips

For each category below, check if the user's data suggests the tip is relevant.
Only show tips that are backed by the user's own patterns — don't dump generic advice.

**Verification tips** (if `had_verification` is false on 50%+ of mistakes):
> "**Add verification to your workflow.** {N}% of your mistakes happened on tasks
> without verification (tests, linter checks, screenshots). Tasks WITH verification
> had a {X}% success rate. Try: include `run the tests after implementing` in your
> prompts, or add a PostToolUse hook that auto-runs your formatter."

**Plan mode tips** (if `skipped_planning` category has 2+ entries):
> "**Plan before multi-file changes.** You've had {N} mistakes from jumping straight
> to code. Try: press Shift+Tab for plan mode, explore the code first, ask Claude
> to create a plan, then switch to normal mode to implement."

**Context hygiene tips** (if `context_waste`/`session_hygiene` flags appear):
> "**Use /clear more aggressively.** {N} mistakes were context-related.
> Run `/clear` between unrelated tasks. If you've corrected Claude 2+ times on
> the same issue, `/clear` and rewrite a better prompt — a clean session with a
> better prompt almost always outperforms accumulated corrections."

**Delegation tips** (if context-related mistakes exist but few subagent wins):
> "**Delegate research to subagents.** When Claude reads many files to investigate
> something, it fills your context. Try: `use subagents to investigate X` — they
> explore in a separate context and report back summaries."

**Prompt quality tips** (if `prompt_quality` category has entries):
> "**Be more specific in your prompts.** {N} mistakes came from vague instructions.
> Instead of `fix the login bug`, try: `users report login fails after session timeout.
> Check the auth flow in src/auth/, especially token refresh. Write a failing test
> that reproduces the issue, then fix it.`"

**API integration tips** (if `api_error` category has entries):
> "**Test APIs with minimal payloads first.** {N} API mistakes logged. Before
> building the full integration, send the simplest possible request and confirm
> the format, field names, and auth work. Document gotchas immediately in
> `_knowledge-base/api-docs/`."

**Decomposition tips** (if mistakes involve large multi-step tasks):
> "**Break complex work into phases.** Instead of one massive prompt like
> 'refactor this entire module, update tests, and fix docs,' decompose into:
> search phase → plan phase → execute phase → verify phase.
> The architecture is designed for decomposition, not monolithic prompts."

**Command utilization tips** (if pattern data shows underuse of slash commands):
> "**You're underusing the command surface.** Claude Code has ~85 slash commands.
> Key ones to adopt: `/compact` (compress context, save tokens), `/plan` (map
> work before executing), `/review` and `/security-review` (structured code
> review), `/cost` (track spending), `/resume` (pick up where you left off).
> Most users know 5 commands — learn 15 and your workflow transforms."

**Permission tips** (if mistakes involve repeated permission interruptions or slow workflows):
> "**Configure wildcard permissions for recurring workflows.** Instead of
> approving every git command, set `Bash(git *)` as always-allow. For file
> edits in your source: `Edit(src/*)`. Set these in `.claude/settings.json`
> once and stop babysitting every action."

**Operator mindset tips** (always show if < 10 total entries — new users):
> "**Think like an operator, not a chatbot user.** The biggest gap between
> average and top users: top users design a better operating environment
> (CLAUDE.md, permissions, hooks, MCP, skills) rather than just writing
> better prompts. Prompting is one lever. Environment design is the
> multiplier. Invest in your CLAUDE.md, permission rules, and connected tools."

**Winning patterns** (always show top 3 wins by reusability):
> "**Your best patterns:**
> 1. {win pattern} — used in {reusable_in}
> 2. {win pattern} — used in {reusable_in}
> 3. {win pattern} — used in {reusable_in}"

### Step 3: Summary score

Calculate a simple "learning score":

```
Score = (wins / (wins + mistakes)) * 100

Trend: compare last 7 entries vs previous 7 entries
```

> "**Learning score: {N}%** ({trend: improving/stable/declining})
> {observation about what's driving the trend}"

---

## ALWAYS-ON BEHAVIORS

These behaviors run automatically, not just when /self-learn is invoked.
They should be integrated into the project's CLAUDE.md as reminders.

### Before Any Task
1. Check `_patterns/mistakes.jsonl` — have I made a related mistake before?
2. Check `_patterns/anti-patterns.md` — am I about to try something that keeps failing?
3. Check `_patterns/wins.jsonl` — is there a proven approach for this?
4. If the task involves an API, check `_knowledge-base/api-docs/` for known gotchas
5. If the task touches multiple files, suggest plan mode first

### During Any Task
- When unsure about an API's behavior → test with minimal payload first
- When a function/module might not exist → verify before using it
- When you spot a potential issue unrelated to the current task → flag it:
  "Heads up: I noticed [X]"
- When context is getting long → suggest `/clear` or subagent delegation

### After Any Significant Task
1. Did anything fail that I had to retry? → Offer to log mistake
2. Did I find a clever solution? → Offer to log win
3. Did the user correct me? → Log mistake immediately (don't ask)
4. Did the user confirm a non-obvious approach? → Log win (easy to miss — watch for it)
5. Did the task have verification? → Note for correlation tracking

### On Session Start
1. Check recently modified pattern files for context
2. If recent high-severity mistakes exist, mention them:
   > "Heads up: recent issue logged — [pattern]. I'll watch for this."

### Session Hygiene Monitoring
- If the conversation has mixed 3+ unrelated topics → suggest `/clear`
- If the same correction has been made 2+ times → suggest `/clear` and a fresh prompt
- If file reads exceed 20 in one investigation → suggest subagent delegation

---

## RULES

1. **Never overwrite pattern files** — always append. Data is sacred.
2. **Never delete entries** — mark as resolved/stale instead.
3. **Always confirm classification** before logging — "I'd classify this as a [X]. Sound right?"
4. **Log confirmations, not just corrections** — if you only track mistakes, you become overly cautious and drift from validated approaches.
5. **Severity is honest** — don't downplay to avoid looking bad. High means high.
6. **Promotion is automatic** — 2+ occurrences = hard rule. Don't wait for the user to ask.
7. **Retrospectives are non-judgmental** — they analyze patterns, not blame.
8. **Cross-project patterns require evidence** — at least 2 distinct contexts confirming the pattern.
9. **Anti-patterns are conclusive** — 3+ failures of the same approach. Not just "didn't work once."
10. **Stale patterns get flagged, not auto-removed** — the user decides if they're still relevant.
11. **Tips are data-driven** — only show tips backed by the user's own pattern data, not generic advice.
12. **New fields are optional** — `category`, `had_verification`, `session_hygiene`, `used_plan_mode`, and `delegation` enrich analysis but should never block logging. If unknown, omit the field.
