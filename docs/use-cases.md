# Use Cases: session-indexer

---

## UC-1: Return to project after a break

**Actor:** Val, returning to a project after 2 weeks.

**Trigger:** Opens Claude Code. SessionStart hook injects last session summary
(from `session-end` skill). He also vaguely remembers a design decision.

**Flow:**
1. Val types: `session-indexer search "auth middleware approach" --db .claude/sessions.db`
2. Tool embeds query via bge-m3, runs exhaustive cosine over all indexed chunks.
3. Returns top-5 chunks from past sessions, each with date and 200-char snippet.
4. Val identifies which session, reads the relevant snippet, recalls context.

**Success:** Val recalls the decision without reading the full session transcript.

---

## UC-2: Find a discarded approach

**Actor:** Val, mid-session.

**Trigger:** Current approach isn't working. Val thinks they discussed an
alternative weeks ago but can't remember the exact wording.

**Flow:**
1. Val queries: `session-indexer search "config validation alternative" --db .claude/sessions.db`
2. Tool returns top-5 ranked results from past sessions.
3. One result from 3 weeks ago mentions the approach.
4. Val continues with that approach.

**Success:** Relevant chunk found despite Val not remembering exact terminology.

---

## UC-3: Session is indexed automatically on exit

**Actor:** Claude Code Stop hook.

**Trigger:** Val closes Claude Code (or runs `/exit`).

**Flow:**
1. Stop hook fires: both `bash .claude/hooks/session-end.sh` and `bash .claude/hooks/session-index.sh` (wired into a single `Stop` entry of `settings.local.json` — Claude Code 2.1.x runs only the first top-level entry, so both must live in the same entry's `hooks` array).
2. Each hook reads `transcript_path` from stdin JSON.
3. `session-end.sh` writes summary to `session-log.md` (LLM call via agy → opencode → raw transcript fallback).
4. `session-index.sh` calls `session-indexer mine <transcript_path> --db <project-root>/.claude/sessions.db` (silently no-ops until the binary is on PATH).
5. Binary opens/creates DB (schema version check), parses JSONL, filters noise, chunks messages (user/assistant/tool).
6. Inserts chunks into `chunks` + `chunks_fts`. Dedup by `(session_id, message_index, chunk_index)`.
7. For each chunk: calls Ollama `bge-m3` to generate embedding, stores in `embeddings`. Chunks past the 50s `context.Context` deadline are stored but flagged `Deferred` (no embedding row yet); backfill with `session-indexer embed` once Ollama is reachable. Embed errors never abort the mine (counted as `Skipped`).
8. Exits in <30s in the happy path (well within 60s hook timeout); can use up to 50s before deferring.

**Success:** Session indexed before JSONL is cleaned up by Claude Code.

---

## UC-4: Ollama unavailable (fallback)

**Actor:** Val, offline or Ollama not running.

**Trigger:** `session-indexer search "query" --db .claude/sessions.db`

**Flow:**
1. Tool probes Ollama (`GET /api/tags` timeout 2s) — fails.
2. Falls back to FTS5-only BM25 search.
3. Returns results ranked by BM25 relevance.
4. Output note: `(embedding unavailable — FTS5 results only)`

**Success:** Search still works, with lower recall quality, user is informed.

---

## UC-5: Automatic context injection at session start

**Actor:** Claude Code SessionStart hook (`session-recall.sh`).

**Trigger:** Val opens a new Claude Code session in this project.

**Flow:**
1. SessionStart fires: `session-recall.sh` derives a query from the current git
   branch name + last 3 commit messages.
2. Runs `session-indexer search "$QUERY" --db .claude/sessions.db --limit 5 --json`.
3. Filters out tool-call noise (raw JSON blobs from Bash/Read/Write/etc.).
4. Groups top results by date, truncates to 280 chars each.
5. Emits a `hookSpecificOutput` JSON with `additionalContext` injected into the
   session context by Claude Code.
6. Claude opens the session with relevant past chunks already visible.

**Success:** Claude begins the session with semantically relevant context from prior
work — without Val having to remember to ask.

**No-op conditions** (silent exit 0):
- `session-indexer` not in PATH
- `.claude/sessions.db` does not exist yet (first session)
- derived query is empty (no git context)
- search returns no chunks after noise filtering

---

## UC-6: Manual search via /recall skill

**Actor:** Val, mid-session, wanting to search past sessions interactively.

**Trigger:** Val types `/recall <query>` in Claude Code.

**Flow:**
1. Claude Code invokes `.claude/skills/session-recall/SKILL.md`.
2. The skill runs `session-indexer search "$QUERY" --db .claude/sessions.db --limit 10 --json`.
3. `jq` formats results: date · role · score · snippet (400 chars).
4. Tool-call chunks are flagged `[tool]` but not hidden.
5. Results printed in the session.

`/recall stats` shows index state (sessions, chunks, embeddings, pending).

**Success:** Val sees matching past chunks inline without leaving Claude Code.

---

## UC-7: Backfill missing embeddings

**Actor:** Val, after Ollama was unavailable during earlier mines.

**Trigger:** `session-indexer stats` shows "N pending embeddings".

**Flow:**
1. Val runs: `session-indexer embed --db .claude/sessions.db`
2. Tool probes Ollama + checks bge-m3 available.
3. For each chunk without an embedding: generates vector, inserts into `embeddings`.
4. Reports: "Embedded 47 pending chunks."

**Success:** All chunks have embeddings; search quality restored.

---

## UC-8: Rebuild index from existing JSONLs

**Actor:** Val, after a DB corruption or on a new machine.

**Trigger:** `.claude/sessions.db` is missing or corrupt.

**Flow:**
1. Val runs a shell loop:
   ```bash
   for f in ~/.claude/projects/-home-val-wrk-myproject/*.jsonl; do
     session-indexer mine "$f" --db .claude/sessions.db
   done
   # Then backfill any pending embeddings (if Ollama was unavailable during some mines):
   session-indexer embed --db .claude/sessions.db
   ```
2. Each call is idempotent: chunks with existing `(session_id, message_index, chunk_index)` are skipped via UNIQUE constraint (INSERT OR IGNORE).
3. DB is rebuilt from available JSONLs.

**Success:** Index rebuilt. Sessions deleted by Claude Code cleanup are lost
(expected — no backup mechanism in scope).

---

## UC-9: Inspect index state

**Actor:** Val, troubleshooting.

**Flow:**
1. `session-indexer stats --db .claude/sessions.db`
2. Output:
   ```
   Sessions indexed: 47
   Chunks total:     1823
   With embeddings:  1820  (3 pending — Ollama unavailable at mine time)
   Oldest entry:     2026-01-14
   Newest entry:     2026-06-25
   DB size:          4.2 MB
   ```

**Success:** Val understands the state of the index without opening SQLite directly.

---

## UC-10: Orchestrator recalls history before spawning a subagent

**Actor:** Claude Code orchestrating agent (main session), about to delegate
work via the Agent tool.

**Trigger:** A subagent's task (e.g. an architecture decision, a bug
diagnosis) would benefit from prior discussion in this project, but
subagents start cold — no `SessionStart` hook, no shared context.

**Flow:**
1. Orchestrator runs `session-indexer search "<query>" --db .claude/sessions.db --limit 5 --json` directly — documented in `.claude/skills/session-recall/SKILL.md` under "For orchestrators / subagent prep".
2. Formats results with `jq` (date, role, snippet). Unlike `/recall`, this
   path skips the tool-call noise filter (the regex that drops raw
   Bash/Read/Write/etc. JSON blobs) — the orchestrator curates what goes
   into the subagent prompt anyway.
3. Folds the relevant snippets into the subagent's prompt, since subagent prompts must be self-contained.

**Success:** The subagent starts with accurate historical context instead of
re-deriving decisions already made, without the subagent itself needing
search access.

**Limitation:** Orchestrator-side only — current subagent tool allowlists
(`bug-fixer`, `code-generator`, `tech-lead`, etc.) exclude the `Skill` tool,
so a spawned subagent cannot invoke `/recall` or this entrypoint itself
mid-task.

---

## UC-11: Catch a stale project fact before citing it

**Actor:** Val or an agent, mid-session, about to state something as current
truth about the project (e.g. "implementation status," a past decision).

**Trigger:** A raw-text `search` hit surfaces an old session where something
was said that may no longer be true — session history is append-only, so an
outdated statement never disappears on its own.

**Flow:**
1. Periodically (or before shipping a batch of sessions), run:
   `session-indexer distill --db .claude/sessions.db --threshold 0.7`
2. The LLM call extracts subject-predicate-object facts from newly-mined
   chunks and judges supersession against currently-valid facts about the
   same subject — a corrected/updated statement auto-tombstones the fact
   it replaces.
3. Before citing any fact, follow the discipline in README's "Querying
   facts": `facts search <query>` → `facts get <id>` → `facts related <id>`
   (check for an incoming supersedes edge) → check `until`.
4. If step 3 surfaces a tombstoned fact, the current one is found via its
   `superseded_by`/incoming edge instead — the stale claim is never cited
   as present-tense truth.

**Success:** A fact distilled from an old session that has since become
false (e.g. "not started" → later sessions show it shipped) is caught at
query time via its tombstone, not months later during a periodic review.
This is the concrete gap the facts layer exists to close — see
`docs/architecture.md`'s "Facts Layer" section for the full rationale.

---

## UC-12: Manually correct a missed or wrong supersession

**Actor:** Val, after noticing `facts search` still surfaces two
contradictory facts about the same subject as both "current."

**Trigger:** `distill`'s automatic supersession judgment is bounded by
`ContextCap` (200) and by whatever the model actually noticed in a given
chunk — it can miss a contradiction the LLM didn't recognize as the same
subject, or one that arose in two mine runs distilled far apart in time.

**Flow:**
1. `session-indexer facts search "<subject>" --db .claude/sessions.db
   --include-expired` to see the full history for that subject.
2. Identify which fact id is actually current and which is stale.
3. `session-indexer facts supersede <new-id> <old-id> --db
   .claude/sessions.db` — manually tombstones `<old-id>` in favor of
   `<new-id>`.
4. Re-running `facts search` (without `--include-expired`) now excludes
   the corrected fact; `facts get <old-id>` shows the `until` timestamp
   and `superseded_by` edge as if `distill` had caught it automatically.

**Success:** The facts layer stays correct even when automatic
supersession misses a case — the same `SupersedeFact` function backs both
the automatic and manual paths, so the resulting state is indistinguishable
from an automatic supersession. This is the "audit/override backstop"
referenced throughout the design docs, exercised for real rather than
staying purely theoretical.
