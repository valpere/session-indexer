# Investigation: `session-indexer tree` (agent/subagent structural view)

**For:** anyone (agent or Val) who re-proposes a JSONL structural/tree view
inside `session-indexer`. Read this first — the case was evaluated and
closed once already; don't restart from zero.

**Status: evaluated, not built.** Scoped 2026-08-27, decided the same day.
Source scope doc preserved in full below as reference material, in case the
decision is later reopened (see "What would change this decision").

---

## The Proposal

Port [`zoetrope`](https://github.com/furkankly/zoetrope) (a Rust TUI,
installed at `~/.local/bin/zoetrope`) into `session-indexer` as a new
`session-indexer tree <jsonl-path>` subcommand: parse a session's JSONL
transcript into its actual structure (main agent → spawned subagents →
tool calls, with status/token counts) and render it as an indented tree,
headless (Phase 1), with live-tailing deferred to an unscoped Phase 2.

Motivation as originally stated: avoid a second toolchain/binary (zoetrope
needed a prebuilt release because this machine's system `rustc` 1.75.0 is
below zoetrope's MSRV 1.88), and reuse `session-indexer`'s existing
JSONL-parsing familiarity and CLI conventions.

## Why It Wasn't Built

Run through `/should-this-exist` before any implementation started:

**Step 1 — the concrete case was never named.** The scope doc has no
answer to "how often would this actually get used" or "what's currently
painful." The toolchain-friction motivation was real *before* zoetrope was
built, but zoetrope is already built, installed, and — per the scope doc's
own words — verified correct against a real 71.8MB multi-agent transcript.
That cost is already paid; the marginal cost of typing `zoetrope inspect
<file>` instead of `session-indexer tree <file>` going forward is close to
zero.

**Step 2 — the non-agent alternative already wins on every axis that
matters:**

| | zoetrope (today) | `session-indexer tree` Phase 1 (proposed) |
|---|---|---|
| Cost to get it | already paid, working | new package + parser + tests, ongoing maintenance |
| Capability | headless `inspect` **and** live tailing | headless only — live mode explicitly deferred, unscoped |
| Correctness risk | zero — it's the ground truth the scope doc itself cross-checks against | a second, independent parser of an undocumented, admittedly shifting format |

Phase 1 as scoped is a strict *subset* of what's already installed and
already working, built at real engineering cost, to save typing one binary
name instead of another.

**Step 3 — kill criteria all fire:**
- No named target frequency or user pain (vague-user criterion).
- No stated success metric (unmeasurable-success criterion).
- The simplest alternative (do nothing — keep using zoetrope) already
  covers ~100% of Phase 1's value, and more (live mode).
- Maintenance cost is asymmetric and one-sided: `internal/tree` would own
  an independent parser against Claude Code's undocumented,
  already-multiply-revised transcript format — a real, ongoing
  maintenance burden the scope doc itself flags ("if this turns out
  stale... re-read the current `transcript.rs`/`session.rs`") — for a
  capability with no stated recurring need.

**Architectural fit, separately from the zoetrope comparison:** every
other `internal/*` package in this repo (`mine`, `embed`, `search`,
`facts`, `browse`) either builds or queries the SQLite index. `tree` as
scoped takes no `--db` at all — it's a second, unrelated tool (a JSONL
structural visualizer) sharing a binary with a semantic-recall tool only
because both happen to read Claude Code transcripts. That's a much weaker
shared-abstraction case than `browse`'s addition (a new query surface over
the *existing* index) earlier in this project's history.

## Decision

**Don't build.** Not "not yet" with an open follow-up — the case as
scoped is dominated by the existing alternative on cost, capability, and
risk, with no stated recurring pain to weigh against that. Re-proposing
this unchanged should point back here rather than restart the evaluation.

## What Would Change This Decision

- A **concrete, actually-occurred** case where zoetrope was reached for
  and the friction of a separate binary was the real blocker — not a
  hypothetical.
- Discovering the real want is zoetrope's **live-tailing mode**, which
  neither the current zoetrope-as-is nor the proposed Phase 1 uniquely
  solves inside `session-indexer` — that would argue for scoping *that*
  capability directly (a live-tail view) rather than reviving a headless
  Phase 1 that's strictly weaker than the status quo.
- Claude Code's transcript format changing in a way that breaks zoetrope
  itself with no upstream fix forthcoming — at that point the "already
  working, already maintained elsewhere" argument no longer holds.

---

## Appendix: Original Scope Document (verbatim)

Preserved for the parsing spec, which is genuinely well-researched
(verified against zoetrope's real source rather than reverse-engineered
from samples) and would still be the right starting point if this
decision is ever reopened.

<details>
<summary>Click to expand — full original scope doc</summary>

# `session-indexer tree` — feature scope

Status: **scoped, not implemented**. Written 2026-08-27 per Val's request
after installing/evaluating `furkankly/zoetrope` (see
`~/wrk/common/adoptions/closed.md`). Read this before writing any code —
it's the design + the parsing spec, grounded in verified source, not a
sketch to re-derive from scratch.

## Motivation

`zoetrope` (Rust TUI, installed at `~/.local/bin/zoetrope`) renders a
Claude Code session's own JSONL transcript as a live/replay flow graph:
main agent → subagents → tool calls, with status and token counts. It's
useful, but it's a separate binary with its own toolchain (needed a
prebuilt release because this machine's system `rustc` is 1.75.0, well
under zoetrope's MSRV 1.88 — see the adoptions entry).

`session-indexer` already parses this exact JSONL format (`internal/mine`)
— but only to flatten it into a linear user/assistant chunk sequence for
embedding/search. It has **no session-structure model at all**: no agent/
subagent tree, no tool-call status, no per-turn model/token info. This
feature adds that structural view natively, reusing session-indexer's own
conventions (cobra subcommand, `internal/<feature>` package), with no new
runtime dependency and no separate toolchain.

## Non-goals (deliberately excluded, mirrors why porting all of zoetrope
wasn't worth it)

- No interactive node editing — no drag, resize, multi-select, or
  connection authoring. This is a **read-only** view of an already-fixed
  session structure.
- No general node-graph layout engine (no Sugiyama, no arbitrary-graph
  pan/zoom). A Claude Code session's structure is a **tree** (main agent
  with subagent branches, each carrying its own tool-call sequence), not
  an arbitrary graph — a much simpler layout problem.
- No WASM/browser build. Terminal-only, like the rest of `session-indexer`.

## Do not reverse-engineer the JSONL schema independently

The transcript format is undocumented and Claude-Code-internal. Don't
grep sample files and guess field semantics — zoetrope's author already
did this defensively and correctly (verified against this machine's real
sessions via `zoetrope inspect`, including a 71.8MB multi-agent transcript
that came out exactly right). Port the **parsing algorithm**, not just
the shape, from:

- `~/wrk/github_repos/orgs/furkankly/zoetrope/src/transcript.rs` (1370
  lines) — the full serde data model + envelope + discovery functions.
  Read the module doc comment at the top first (defensive-parsing
  philosophy: unknown `type` → skip, never panic on a bad line).
- `~/wrk/github_repos/orgs/furkankly/zoetrope/src/state/session.rs` —
  how spawn tool-calls join to discovered subagent files at runtime
  (`tool_use_id` correlation, out-of-order arrival handling).

Key facts already verified in-source (cite these, don't re-derive):

**Main transcript envelope** (`user`/`assistant`/`system`/`attachment`
entries only — everything else, e.g. `ai-title`, `mode`,
`permission-mode`, `file-history-snapshot`, `queue-operation`, is flat
session metadata with no `uuid`/`parentUuid`, route to session info, not
the tree):
- `uuid` — this entry's id.
- `parentUuid` — **tri-state**: absent (flat metadata line) vs.
  present-and-null (the root entry) vs. present-and-set (a normal entry).
  Distinguishing "absent" from "null" matters for root detection — in Go,
  model as `*string` isn't enough; use a wrapper or a "was the key present"
  check (`json.RawMessage` + manual presence test, or two-pass unmarshal).
- `isSidechain` (bool) — present but **not the mechanism used for
  subagents in current Claude Code versions** on this machine (confirmed:
  0 sidechain entries in a real subagent-bearing session). Don't build the
  tree on this field alone.
- `agentId` — **present on every line of a subagent file**, absent from
  the main file. This is the real "which file/agent does this line belong
  to" signal once you've already located a subagent file.
- `attributionAgent` — looks similar, **do not use it as a join key**
  (zoetrope's own comment: "Do NOT rely on it — join on `agentId`").
- `message.model`, `message.usage` (input/output/cache tokens) — present
  on `assistant` entries, needed for the per-node token display zoetrope
  shows (`tokens: 2618012` in the earlier `inspect` output on this
  session).

**Tool-call / spawn detection** (assistant entry, `message.content[]`
blocks):
- `tool_use` blocks: `{type, id, name, input, caller}`. A tool_use with
  `name` in `{"Agent", "Task", "Workflow"}` is a **spawn** call — `Task`
  is the legacy name for `Agent`; treat all three as spawns (single
  source of truth: zoetrope's `is_spawn_tool()`).
- `tool_result` blocks live on the **next `user` entry**, matched to the
  originating `tool_use` by `tool_use_id` (a `tool_result.is_error: true`
  is a tool failure — surface it, don't drop it).

**Subagent file discovery** (this is the part with no analogue in
`internal/mine` today — new code):
- Sibling directory: `<same-dir-as-session-file>/subagents/`.
- Each subagent file: `subagents/agent-<17-hex-id>.jsonl`, optionally
  paired with `subagents/agent-<17-hex-id>.meta.json`.
- Workflows nest one level deeper:
  `subagents/workflows/<workflow-id>/journal.jsonl`, with that workflow's
  own `agent-*.jsonl` files alongside it.
- **The join key from a spawn call to its subagent**: the subagent's meta
  (`tool_use_id` field) equals the `id` of the `Agent`/`Task`/`Workflow`
  `tool_use` block in the *parent* file that spawned it — not any
  positional or timing heuristic. Zoetrope's own comment states this
  explicitly: `tool_use_id === the Agent tool_use block .id in the main
  [transcript]`.
- Subagent directories are created **lazily** — a session with no
  subagents simply has no `subagents/` directory. Not an error, just an
  empty result.

If any of the above turns out stale (Claude Code changes its transcript
format again — it already has multiple undocumented revisions per
zoetrope's own defensive-parsing stance), **re-read the current
`transcript.rs`/`session.rs` rather than patching around a guess** — that
repo is the more actively maintained spec of the two.

## Proposed design

### Package: `internal/tree`

New package, sibling to `internal/mine`, `internal/browse`, etc.

```
internal/tree/
  types.go     // SessionNode, ToolCall, tri-state parentUuid helper
  parse.go     // per-line envelope + content-block parsing (structural,
               // NOT internal/mine's flattening parser — different need)
  discover.go  // subagents/ dir walk, agent-<id>.jsonl + .meta.json,
               // workflows/<id>/journal.jsonl
  build.go     // join spawns -> subagent files -> tree; produces the
               // root SessionNode
  render.go    // headless indented-text renderer (see CLI shape below)
  tree_test.go // fixtures: (a) no-subagent session, (b) one subagent,
               // (c) nested workflow, (d) malformed/truncated line
               // (must not panic — same defensive stance as zoetrope)
```

`internal/mine`'s `ParseJSONL` is **not reused** — it deliberately
discards structure (flattens to user/assistant text chunks for
embedding). This package parses the same files for a different purpose
and needs the raw envelope fields `mine` throws away. Some overlap in
low-level JSON-block types (`tool_use`/`tool_result` block shapes) is
fine to share via `internal/types.go` if it turns out identical; don't
force a shared abstraction if the two parsers' needs diverge — see DRY
vs. premature abstraction judgment call at implementation time.

### Data model sketch

```go
type SessionNode struct {
    AgentID   string // "" for the main agent
    Label     string // "main" or the spawn tool's description/name
    Model     string
    Status    string // "active" | "idle" | "done" | "error"
    Tokens    int64  // summed input+output+cache across this agent's turns
    ToolCalls []ToolCall
    Children  []*SessionNode // spawned subagents, in spawn order
}

type ToolCall struct {
    ID       string
    Name     string
    OK       bool // false if the matching tool_result had is_error: true
    Pending  bool // no matching tool_result seen yet
}
```

Exact fields TBD at implementation time against real fixture data — this
is a starting sketch, not a contract.

### CLI shape

Phase 1 — headless only, mirrors zoetrope's `inspect` subcommand exactly
(same reason it's useful: scriptable, no TTY needed, easy to test):

```
session-indexer tree <jsonl-path>
```

No `--db` needed — this reads a transcript file directly and prints,
same pattern as `session-indexer mine` takes a path but writes to a DB;
`tree` takes a path and writes to stdout, nothing stored.

Output: indented tree, one line per node/tool-call, e.g.:

```
session 049efdb4... — facts-layer-session-indexer
  3 agent(s), 3362 tool call(s), 124 queued

● main (active) — model: claude-sonnet-5, tokens: 2618012
  ✓ Explore (done) — "Survey vmm-rada-web-ui and sibling setups"
      tools: 39 (36✓ 3✗)
  ✓ Explore (done) — "Find minimax-m2.5 and kimi-k2.5 references"
      tools: 30 (28✓ 2✗)
```

(Format is illustrative — match `zoetrope inspect`'s output closely
enough to be recognizable, adjust to Go idiom / session-indexer's
existing CLI output style, e.g. `browse`'s table conventions, as a
secondary concern.)

Phase 2 (explicitly deferred, separate follow-up task — don't build this
until Phase 1 is dogfooded and proven useful):

- `--follow` flag or a `session-indexer watch` subcommand: live-updating
  view via `bubbletea`/`tcell`, tailing the file as it grows (like
  zoetrope's default live mode). Needs its own scoping pass — polling vs.
  fsnotify, redraw throttling, terminal resize handling — not trivial
  to bolt onto Phase 1's headless renderer without a design pass first.

## Verification plan

1. **Unit tests** — the 4 fixtures listed above under `tree_test.go`,
   plus a fixture exercising the tri-state `parentUuid` (absent vs.
   null vs. set) and a fixture with an out-of-order subagent meta arrival
   (meta discovered after the spawn call was already parsed — same
   ordering hazard zoetrope's `session.rs` explicitly handles).
2. **Cross-check against zoetrope on real data** — run both
   `zoetrope inspect <file>` and `session-indexer tree <file>` on the
   same real session files (at minimum: a session with no subagents, and
   the current project's own large multi-agent session used to verify
   zoetrope itself) and confirm agent count / tool-call count / token
   counts match. Zoetrope is the ground truth here since it's already
   confirmed correct against this exact data.
3. **Malformed-input safety** — feed truncated/corrupted JSONL lines,
   confirm no panic, matches `internal/mine.ParseJSONL`'s existing
   "skip malformed lines, don't abort" convention.

## Open questions for implementation time (not blocking the scope)

- Exact `Status` derivation rule for a `SessionNode` (zoetrope has
  `active`/`idle` states tied to live-tailing, which Phase 1 doesn't
  have — headless mode probably only needs `done`/`error`/`in-progress`
  based on whether the file has a trailing incomplete turn).
- Whether to expose `--json` output alongside the human-readable tree
  (session-indexer's `search`/`browse` commands already have this
  pattern via `asJSON` flags in `main.go` — likely yes, for consistency).

</details>
