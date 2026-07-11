---
name: fix-review
description: Multi-model PR review with parallel fan-out. Three Ollama models (Cloud `:cloud` by default, local fallback) review the full PR diff in parallel; Claude acts as Arbiter, using vote count as a confidence prior to confirm/escalate/dismiss findings, then applies a single consolidated fix commit. Auto-merges (squash) when no fixes were reverted and the PR is mergeable; otherwise leaves it for manual review. Usage: /fix-review [PR-number]
---

# Skill: /fix-review (parallel)
# session-indexer — Automated PR Review

---

## OVERVIEW

Three model rounds run **in parallel**, then a single Claude arbiter
round adjudicates and applies the consolidated fix.

```
        ┌─→ Round 1 (model A)  ┐
diff ───┼─→ Round 2 (model B)  ┼─→ aggregate (dedupe + vote count)
        └─→ Round 3 (model C)  ┘
                                    ↓
                              Arbiter (Claude) — rules + ONE fix → commit → push
```

**Why parallel:**
- Wall time is `max(t₁, t₂, t₃)` instead of `t₁ + t₂ + t₃`.
- Three independent perspectives on the same diff (no caching effect).
- Vote count (1/3, 2/3, 3/3) is a strong confidence signal for the arbiter.
- One fix commit instead of four → cleaner PR history.

Only provider: **Ollama** (Cloud by default — `:cloud` models, free
tier — or local via `reviewer_set: local` in `config.yaml`).
OpenRouter was removed 2026-05-30 after it went pay-only.

**Merge:** auto-merge (squash + delete branch) when the run is clean.
"Clean" means no fixes were reverted, the PR has no merge conflicts, and
local gates either passed or only failed in layers this PR doesn't touch
(diff-scope rule). If anything blocks merge, the skill asks the user once
before acting. Caller (`/ship` or you) doesn't need to merge separately.

---

## RUN COMPLETION CONTRACT (do not skip)

The canonical step order is **Step 9 (Telemetry) → Step 10 (Merge) →
Step 11 (Summary)**. A run is not complete until **both** of these
have run:

1. **Step 9 telemetry** — `telemetry.jsonl` appended with one row per
   model round + one arbiter row.
2. **Step 11 final summary** — printed to the user.

Step 10 (auto-merge) is **not** the end of the run. Whatever Step 10
returns — `merged`, `merged (forced)`, `left open`, or `closed` —
control MUST flow into Step 11 and print the summary.

If a session ever ends up doing merge before telemetry (off the
canonical order), still do both: write telemetry first, then summary.

---

## STEP 0: Resolve PR + Load Config

If a PR number was given as argument, use it.

Otherwise detect from the current branch:
```bash
PR_NUMBER="${1:-$(gh pr view --json number --jq '.number' 2>/dev/null)}"
[ -z "$PR_NUMBER" ] && { echo "No open PR. Pass /fix-review <number>"; exit 1; }

gh pr view "$PR_NUMBER" --json number,title,headRefName,baseRefName,url
BASE_BRANCH=$(gh pr view "$PR_NUMBER" --json baseRefName --jq '.baseRefName')
```

**Load `config.yaml`** from this skill's directory. Extract:
- `provider` (always `ollama`)
- `reviewer_set` (`cloud` → `reviewers.ollama_cloud`, `local` → `reviewers.ollama_local`)
- `reviewers.{ollama_cloud|ollama_local}.round_{1,2,3}.model`
- `ollama_api_url` (cloud) or `ollama_api_url_local`
- `post_summary_to_pr`
- `telemetry_enabled`, `telemetry_file`

```bash
PROVIDER=ollama   # only provider — config.yaml `provider:` field is documentation-only
REVIEWER_SET=$(grep '^reviewer_set:' .claude/skills/fix-review/config.yaml | awk '{print $2}')
REVIEWER_SET="${REVIEWER_SET:-cloud}"
```

**Load API credentials and helpers:**

```bash
source .claude/skills/lib/env.sh
source .claude/skills/lib/rest.sh

if [ "$REVIEWER_SET" = "cloud" ]; then
  REVIEWER_BLOCK=ollama_cloud
  load_env_key OLLAMA_API_KEY
  API_KEY="$OLLAMA_API_KEY"
  API_URL=$(grep '^ollama_api_url:' .claude/skills/fix-review/config.yaml | awk '{print $2}')
else
  REVIEWER_BLOCK=ollama_local
  API_KEY=""   # local Ollama does not require auth
  API_URL=$(grep '^ollama_api_url_local:' .claude/skills/fix-review/config.yaml | awk '{print $2}')
fi

# CRITICAL: export API_KEY before any background jobs (&).
# Background subshells do NOT inherit bash functions (load_env_key etc.),
# but they DO inherit exported variables. Without this export, API_KEY
# is empty in every round and all Ollama Cloud calls return 401.
export API_KEY
```

**Load model names:**
```bash
MODEL_R1=$(yq -r ".reviewers.${REVIEWER_BLOCK}.round_1.model // \"\"" .claude/skills/fix-review/config.yaml)
MODEL_R2=$(yq -r ".reviewers.${REVIEWER_BLOCK}.round_2.model // \"\"" .claude/skills/fix-review/config.yaml)
MODEL_R3=$(yq -r ".reviewers.${REVIEWER_BLOCK}.round_3.model // \"\"" .claude/skills/fix-review/config.yaml)
```

If `yq` is not available, fall back to grep/awk.

**Probe Ollama Cloud + CLI failover** (only when `REVIEWER_SET=cloud`):

```bash
ACTIVE_PROVIDER=ollama     # "ollama" | "cli"
CLI_AGENTS=()              # name|cmd entries populated on failover
FAILOVER_TIER=""           # "" | "cli"
FAILOVER_REASON=""

probe_provider() {
  local payload resp http err
  payload=$(jq -n --arg m "$MODEL_R1" \
    '{model:$m,messages:[{role:"user",content:"OK"}],stream:false,max_tokens:3}')
  resp=$(curl -s --max-time 10 -w '\n%{http_code}' \
    -H "Content-Type: application/json" \
    ${API_KEY:+-H "Authorization: Bearer $API_KEY"} \
    -d "$payload" "$API_URL")
  http="${resp##*$'\n'}"
  resp="${resp%$'\n'*}"
  if [ "$http" -lt 200 ] || [ "$http" -ge 300 ]; then
    err=$(printf '%s' "$resp" | jq -r '
      if type == "object" then
        (.error | if type == "string" then . elif type == "object" then .message // tostring else tostring end)
      else . end // ""
    ' 2>/dev/null)
    FAILOVER_REASON="HTTP ${http}${err:+: ${err}}"
    return 1
  fi
  return 0
}

if [ "$REVIEWER_SET" = "cloud" ] && ! probe_provider; then
  echo "⚠️  FAILOVER: Ollama Cloud unavailable (${FAILOVER_REASON}) — engaging CLI tier" >&2
  count=$(yq '.reviewers.cli | length' .claude/skills/fix-review/config.yaml 2>/dev/null)
  for ((i=0; i<count; i++)); do
    name=$(yq -r ".reviewers.cli[$i].name" .claude/skills/fix-review/config.yaml)
    cmd=$(yq -r  ".reviewers.cli[$i].cmd"  .claude/skills/fix-review/config.yaml)
    CLI_AGENTS+=("${name}|${cmd}")
  done
  if [ "${#CLI_AGENTS[@]}" -eq 0 ]; then
    echo "✗ CLI tier empty — cannot fail over. Aborting." >&2; exit 1
  fi
  ACTIVE_PROVIDER=cli
  FAILOVER_TIER=cli
  echo "⚠️  FAILOVER: using CLI tier ($(printf '%s ' "${CLI_AGENTS[@]%%|*}"))" >&2
fi
```

**Telemetry helpers:**
```bash
TELEMETRY_ENABLED=$(grep '^telemetry_enabled:' .claude/skills/fix-review/config.yaml | awk '{print $2}')
TELEMETRY_FILE=$(grep '^telemetry_file:' .claude/skills/fix-review/config.yaml | awk '{print $2}')
TELEMETRY_ENABLED="${TELEMETRY_ENABLED:-true}"
TELEMETRY_FILE="${TELEMETRY_FILE:-.claude/skills/fix-review/telemetry.jsonl}"

now_ms() {
  date +%s%3N 2>/dev/null \
    || echo $(($(date +%s) * 1000))
}
```

---

## STEP 1: Build the Review Prompt

Get the full PR diff. `-U10` widens context per hunk to 10 lines.

```bash
DIFF=$(gh pr diff "${PR_NUMBER}")
```

### Detect diff type

```bash
is_docs_only_diff() {
  local files
  files=$(gh pr diff "${PR_NUMBER}" --name-only)
  [ -z "$files" ] && return 1
  while IFS= read -r f; do
    case "$f" in
      *.md|*.markdown|*.rst|*.adoc)                          ;;
      docs/*)                                                ;;
      README*|CHANGELOG*|LICENSE*|CONTRIBUTING*)             ;;
      CODE_OF_CONDUCT*|SECURITY*|AUTHORS*)                   ;;
      CLAUDE.md|AGENTS.md|GEMINI.md)                         ;;
      *.yaml|*.yml|*.toml)                                   ;;
      .env*|*/.env*)                                         ;;
      .gitignore|.editorconfig|.dockerignore)                ;;
      *)                                                     return 1 ;;
    esac
  done <<< "$files"
  return 0
}

DIFF_TYPE=code
if is_docs_only_diff; then
  DIFF_TYPE=docs
  echo "→ Documentation-only diff detected — using docs-prompt.txt"
fi
```

When `DIFF_TYPE=docs`, load the docs-specific prompt:

```bash
if [ "$DIFF_TYPE" = "docs" ]; then
  DOCS_PROMPT_FILE=".claude/skills/fix-review/docs-prompt.txt"
  [ ! -f "$DOCS_PROMPT_FILE" ] && DOCS_PROMPT_FILE="$HOME/.claude/skills/fix-review/docs-prompt.txt"
  if [ -f "$DOCS_PROMPT_FILE" ]; then
    PROMPT_TEMPLATE=$(cat "$DOCS_PROMPT_FILE")
  else
    DIFF_TYPE=code
  fi
fi
```

For `DIFF_TYPE=docs`: build `PROJECT_CONTEXT` from `head -150 CLAUDE.md` and substitute
both `{PROJECT_CONTEXT}` and `{DIFF}` placeholders.

**Prompt template** (default, `DIFF_TYPE=code`; substitute `$DIFF` inline):

```
You are a senior Go engineer reviewing a pull request in **session-indexer**, a
single Go 1.26 binary (pure Go, no CGO) with four subcommands: mine, embed,
search, stats. It indexes Claude Code JSONL sessions into a per-project SQLite
database (modernc.org/sqlite, WAL mode, FTS5) and retrieves them via
embedding-first cosine similarity (bge-m3 via Ollama, 1024-dim float32 BLOBs)
with FTS5 BM25 fallback. Layout: cmd/session-indexer/main.go (Cobra root),
internal/db/ (schema + open), internal/mine/ (parse → chunk → store → embed),
internal/embed/ (Ollama REST client), internal/search/ (cosine + FTS5).

Review the following git diff using the Code Review Pyramid — evaluate from
bottom to top, spending the most attention on the lower layers and less on
the higher ones:

  5 (top)  — Code style        → DO NOT FLAG. gofmt + go vet handle this.
  4        — Tests             → Table-driven _test.go files. Critical paths
                                 covered: JSONL parsing, chunking, noise filter,
                                 embedding probe fallback, cosine similarity,
                                 FTS5 keyword search. Race-friendly (go test -race
                                 must pass). Regression tests for edge cases
                                 (binary tool_result, empty session, etc.).
  3        — Documentation     → Exported APIs documented. Non-obvious logic
                                 (binary heuristic, chunking rules, schema
                                 version check) explained briefly.
  2        — Implementation    → Bugs, error wrapping (fmt.Errorf("...%w", err)),
                                 nil deref, missing defer rows.Close() /
                                 resp.Body.Close(), unhandled SQLite errors,
                                 float32 BLOB length check (len(blob) % 4 == 0
                                 and len(blob) == 4096 for bge-m3), Ollama probe
                                 timeout respected (2s for GET /api/tags),
                                 tool_result binary heuristic correctness
                                 (len > 10240 || base64 regex match),
                                 chunk dedup key stability (session_id fallback
                                 to filename stem when absent from JSONL),
                                 INSERT OR IGNORE idempotency on re-mine,
                                 FTS5 trigger sync (chunks_ai / chunks_ad),
                                 WAL busy_timeout=5000 set on every DB open.
  1 (base) — API / Architecture → Schema version check on every DB open (must
                                 return wrapped error on mismatch — not panic),
                                 per-project DB isolation (no hardcoded paths —
                                 DB path always from --db flag), noise filter
                                 thresholds match spec (<30 chars, XML/HTML
                                 prefix, slash-command prefix), chunking at
                                 paragraph boundary (not mid-word), cosine
                                 exhaustive over ALL embeddings rows (no subset
                                 sampling), Ollama probe failure must be non-
                                 fatal (warn + continue without embeddings).

Return ONLY a JSON array — no prose, no markdown fences, just the raw JSON.
Each item must have exactly these fields:
  "file"     — relative file path (string)
  "line"     — line number on the + side of the diff (integer)
  "layer"    — pyramid layer number 1–4 (integer)
  "severity" — one of: "error", "warning", "suggestion" (string)
  "body"     — clear description of the issue and how to fix it (string)

Severity guide:
  error      — must fix before merge (bug, race, layer-1 violation)
  warning    — should fix (missing test for critical path, undocumented public API)
  suggestion — nice to have (minor clarity improvement)

Do NOT flag: gofmt issues, import order, blank lines (layer 5 — automated).
Do NOT flag code not present in this diff.
Do NOT propose architectural rewrites; focus on what the diff actually changes.

If there are no issues, return an empty array: []

Git diff:
---
{DIFF}
---
```

Hold the prompt template in a shell variable, e.g. `PROMPT_TEMPLATE`,
then substitute `{DIFF}` per-call:
```bash
PROMPT=$(printf '%s' "$PROMPT_TEMPLATE" | sed "s|{DIFF}|$(printf '%s' "$DIFF" | sed 's/[\\/&]/\\&/g')|")
```

---

## STEP 2: Fan Out — Three Models in Parallel

```bash
RUN_DIR=$(mktemp -d -t fix-review-XXXX)
WALL_START_MS=$(now_ms)

REVIEW_SYSTEM_MSG="You are a senior code reviewer. Your entire response MUST be a raw JSON array — nothing else. Start with [ and end with ]. No prose, no markdown fences, no explanations before or after. If there are no issues output exactly: []"

export PROVIDER REVIEWER_BLOCK API_URL API_KEY PROMPT RUN_DIR \
       MODEL_R1 MODEL_R2 MODEL_R3 REVIEW_SYSTEM_MSG
export -f rest_post now_ms 2>/dev/null

run_round() {
  local n="$1" model="$2"
  local r_start r_end payload response think pt ct
  r_start=$(now_ms)
  think=$(yq -r ".reviewers.${REVIEWER_BLOCK}.round_${n}.think // true" \
    .claude/skills/fix-review/config.yaml 2>/dev/null)
  payload=$(jq -n --arg m "$model" --arg sys "$REVIEW_SYSTEM_MSG" \
    --arg user "$PROMPT" --argjson think "$think" \
    '{model:$m,messages:[{role:"system",content:$sys},{role:"user",content:$user}],stream:false,think:$think}')
  response=$(rest_post "$API_URL" "$payload" "$API_KEY") \
    || response='{"_error":"rest_post_failed"}'
  r_end=$(now_ms)

  printf '%s' "$response" > "$RUN_DIR/round_${n}.raw.json"
  pt=$(printf '%s' "$response" | jq -r '.prompt_eval_count // empty')
  ct=$(printf '%s' "$response" | jq -r '.eval_count // empty')
  printf '%s\n%s\n%s %s\n' "$model" "$((r_end - r_start))" \
    "${pt:-null}" "${ct:-null}" > "$RUN_DIR/round_${n}.meta"
}

run_cli_round() {
  local n="$1" name="$2" cmd="$3"
  local r_start r_end response
  r_start=$(now_ms)
  response=$(printf '%s' "$PROMPT" | timeout 300 sh -c "$cmd" 2>/dev/null) || response=""
  r_end=$(now_ms)
  printf '%s' "$response" > "$RUN_DIR/round_${n}.raw.txt"
  printf '%s\n%s\n%s\n' "$name" "$((r_end - r_start))" "null null" \
    > "$RUN_DIR/round_${n}.meta"
}
export -f run_round run_cli_round

if [ "$ACTIVE_PROVIDER" = "cli" ]; then
  n=1
  for entry in "${CLI_AGENTS[@]}"; do
    name="${entry%%|*}"; cmd="${entry#*|}"
    run_cli_round "$n" "$name" "$cmd" &
    n=$((n + 1))
  done
  wait
  NUM_ROUNDS=$((n - 1))
else
  run_round 1 "$MODEL_R1" &
  run_round 2 "$MODEL_R2" &
  run_round 3 "$MODEL_R3" &
  wait
  NUM_ROUNDS=3
fi

WALL_END_MS=$(now_ms)
WALL_TIME_MS=$((WALL_END_MS - WALL_START_MS))
```

> If `export -f` doesn't propagate (older bash, restricted shells), inline
> the `run_round` body inside `bash -c '...' &` blocks. Contract: each
> background job writes `round_N.raw.json` + `round_N.meta`
> (3 lines: `model\nduration_ms\nprompt_tokens completion_tokens`).

---

## STEP 3: Parse Each Response → Tagged Findings

```bash
parse_round() {
  local n="$1"
  local model content
  model=$(head -1 "$RUN_DIR/round_${n}.meta")
  if [ "$ACTIVE_PROVIDER" = "cli" ]; then
    content=$(cat "$RUN_DIR/round_${n}.raw.txt")
  else
    content=$(jq -r '.message.content // empty' "$RUN_DIR/round_${n}.raw.json")
  fi
  content=$(printf '%s' "$content" | sed -E 's/^```(json)?[[:space:]]*//; s/```[[:space:]]*$//')

  if ! echo "$content" | jq -e 'type == "array"' >/dev/null 2>&1; then
    echo "warn: round ${n} (${model}) returned non-array — counting 0 findings" >&2
    echo "[]" > "$RUN_DIR/round_${n}.findings.json"
    return
  fi
  echo "$content" | jq --arg m "$model" 'map(. + {model: $m})' \
    > "$RUN_DIR/round_${n}.findings.json"
}
for n in $(seq 1 "$NUM_ROUNDS"); do parse_round "$n"; done
```

If a round errored: 0 findings — don't retry.

---

## STEP 4: Aggregate — Dedupe + Vote Count

Merge all rounds. Group by `(file, line)`. Record votes, models, longest body,
worst severity, lowest layer.

```bash
jq -s '
  flatten
  | group_by(.file + ":" + (.line|tostring))
  | map({
      file:     .[0].file,
      line:     .[0].line,
      votes:    length,
      models:   [.[] | .model],
      bodies:   [.[] | .body],
      body:     ([.[] | .body] | sort_by(length) | last),
      severity: ([.[] | .severity] | unique
                  | (if any(. == "error") then "error"
                     elif any(. == "warning") then "warning"
                     else "suggestion" end)),
      layer:    ([.[] | .layer] | min)
    })
  | sort_by(.layer,
            (if .severity == "error" then 0
             elif .severity == "warning" then 1
             else 2 end),
            -.votes)
' "$RUN_DIR"/round_*.findings.json > "$RUN_DIR/aggregated.json"

TOTAL_FINDINGS=$(jq 'length' "$RUN_DIR/aggregated.json")
declare -A VOTE_BAND
for v in $(seq 1 "$NUM_ROUNDS"); do
  VOTE_BAND[$v]=$(jq --argjson v "$v" '[.[] | select(.votes == $v)] | length' "$RUN_DIR/aggregated.json")
done
TOP_VOTE_COUNT="${VOTE_BAND[$NUM_ROUNDS]:-0}"
```

Sorted critical-first: layer 1 errors 3/3 votes → layer 4 suggestions 1/3.

If `TOTAL_FINDINGS == 0`: skip to arbiter independent scan (Step 5 still runs).

---

## STEP 5: Arbiter (Claude)

Read `$RUN_DIR/aggregated.json`. For each finding, rule:

| Ruling | When |
|---|---|
| **CONFIRM** | Real issue. Default for `votes ≥ 2` unless clearly false-positive. |
| **ESCALATE** | Real issue, more severe than tagged. |
| **DISMISS** | False positive, conflicts with project rules, or layer-5 noise. Default for `votes == 1` unless obviously real. |
| **DEFER** | Real but out of scope for this PR. Log, don't fix. |

**Vote count is a confidence prior, not a verdict.**

**Independent scan** of the full diff — flag anything all three models missed.
Pay special attention to session-indexer specifics:
- **JSONL parsing**: tool_result binary heuristic (`len > 10240 || base64 regex`) — off-by-one or regex anchor bugs
- **Chunk dedup key**: if `session_id` absent from JSONL, must fall back to filename stem (not panic or empty string)
- **Ollama probe failure**: `GET /api/tags` 2s timeout; failure must log `warn: ollama unavailable` and continue without embeddings — not block or panic
- **FTS5 trigger sync**: `chunks_ai` / `chunks_ad` triggers must fire on every INSERT/DELETE — check trigger definitions in schema.sql
- **Schema version check**: `db.QueryRow("SELECT value FROM meta WHERE key='schema_version'")` must return a wrapped error on mismatch — not panic, not silent continue
- **Float32 BLOB**: `encoding/binary` LittleEndian; must validate `len(blob) == 4096` (1024 × 4 bytes) before cosine to avoid NaN
- **WAL busy_timeout**: `PRAGMA busy_timeout=5000` must be set on every DB open path (concurrent Stop hooks can deadlock otherwise)
- **Per-project isolation**: DB path always from `--db` flag — no default hardcoded to project root

**Apply CONFIRM + ESCALATE fixes** via the Edit tool. Minimal change per fix; no opportunistic refactoring.

Save rulings to `$RUN_DIR/arbiter.json`:
```jsonc
[
  {"file":"...", "line":42, "ruling":"CONFIRM", "votes":3, "body":"..."},
  ...
]
```

---

## STEP 6: Run Quality Gates

```bash
go build ./... 2>&1 | tail -20
go vet ./... 2>&1 | tail -20
go test -race ./... 2>&1 | tail -30
```

No Makefile yet. When a Makefile is added with `build:`, `vet:`, `test:` targets,
switch to `make build && make vet && make test`.

### Diff-scope check before reverting

1. **Check what files this PR touches** —
   `gh pr diff "${PR_NUMBER}" --name-only`.
2. **Map failure to layer**:
   - `go build` / `go vet` / `go test -race` → only from `.go` file changes.
   - Schema / FTS5 trigger test → only if `internal/db/schema.sql` changed.
   - SQLite WAL / concurrent hook test → only if `internal/db/` or `internal/mine/` changed.
3. **Decide**:
   - Failing layer **touched by diff** → identify which fix broke it, revert, log as
     `reverted — caused build/test failure`, re-run gates.
   - Failing layer **not touched** → mark as **pre-existing**. Log in summary.
     Do **not** revert. Do **not** silently pass.

A docs/skill-only PR (`.claude/`, `*.md`) cannot cause Go build failures by construction.

```bash
GATES_OK=no
# ... run gates, then if pass-clean or all-pre-existing-skip:
#   GATES_OK=yes
```

---

## STEP 6a: Compute Aggregates Before Output

```bash
SEQ_SUM_MS=0
for n in $(seq 1 "$NUM_ROUNDS"); do
  meta="$RUN_DIR/round_${n}.meta"
  [ -f "$meta" ] || continue
  d=$(sed -n '2p' "$meta")
  SEQ_SUM_MS=$((SEQ_SUM_MS + ${d:-0}))
done
SPEEDUP=$(awk -v s="$SEQ_SUM_MS" -v w="$WALL_TIME_MS" \
  'BEGIN{ if (w>0) printf "%.2f", s/w; else print "n/a" }')

if [ "$ACTIVE_PROVIDER" = "cli" ]; then
  MODELS_LIST=$(printf '%s | ' "${CLI_AGENTS[@]%%|*}" | sed 's/ | $//')
else
  MODELS_LIST="${MODEL_R1} | ${MODEL_R2} | ${MODEL_R3}"
fi

VOTE_BAND_REPORT=""
for v in $(seq "$NUM_ROUNDS" -1 1); do
  count="${VOTE_BAND[$v]:-0}"
  [ "$count" -eq 0 ] && continue
  tag=""
  [ "$v" = "$NUM_ROUNDS" ] && tag=" (unanimous)"
  [ "$v" = 1 ]            && tag=" (low confidence)"
  VOTE_BAND_REPORT+="  ${v}/${NUM_ROUNDS} votes: ${count}${tag}"$'\n'
done
[ -z "$VOTE_BAND_REPORT" ] && VOTE_BAND_REPORT="  (no findings)"

FAILOVER_SECTION=""
if [ -n "$FAILOVER_TIER" ]; then
  agents_csv=$(printf '%s, ' "${CLI_AGENTS[@]%%|*}" | sed 's/, $//')
  FAILOVER_SECTION=$(cat <<EOF

### ⚠️ Provider failover

Primary provider (ollama / Ollama Cloud) was unavailable.
Tier used: ${FAILOVER_TIER}
Reason:    ${FAILOVER_REASON}
Agents:    ${agents_csv}

Action: regenerate OLLAMA_API_KEY at https://ollama.com/settings/keys
or adjust reviewers.cli in config.yaml.
EOF
)
fi

CONFIRMED_COUNT=$(jq '[.[] | select(.ruling=="CONFIRM")]   | length' "$RUN_DIR/arbiter.json")
ESCALATED_COUNT=$(jq '[.[] | select(.ruling=="ESCALATE")]  | length' "$RUN_DIR/arbiter.json")
DISMISSED_COUNT=$(jq '[.[] | select(.ruling=="DISMISS")]   | length' "$RUN_DIR/arbiter.json")
DEFERRED_COUNT=$( jq '[.[] | select(.ruling=="DEFER")]     | length' "$RUN_DIR/arbiter.json")
ADDED_NEW_COUNT=$(jq '[.[] | select(.added_new == true)]   | length' "$RUN_DIR/arbiter.json")

PR_URL=$(gh pr view "$PR_NUMBER" --json url --jq '.url')
```

Reference these names — and only these — in Steps 7-10. Naming canon: `SEQ_SUM_MS`.

---

## STEP 7: Single Commit + Push

```bash
git add -A
git restore --staged .claude/skills/fix-review/telemetry.jsonl 2>/dev/null || true

if [ "$(git diff --cached --name-only | wc -l)" -eq 0 ]; then
  echo "No fixes applied — nothing to commit."
  COMMIT_SHA="(no-op)"
else
  git commit -m "fix(pr#${PR_NUMBER}): address /fix-review findings

$(jq -r '.[] | select(.ruling == "CONFIRM" or .ruling == "ESCALATE")
       | "- \(.file):\(.line) — \(.body | gsub("\n"; " ") | .[0:80])"' \
       "$RUN_DIR/arbiter.json" | head -20)

Co-Authored-By: Claude Sonnet 4.6 <noreply@anthropic.com>"
  git push
  COMMIT_SHA=$(git rev-parse --short HEAD)
fi
```

---

## STEP 8: Optional PR Summary Comment

If `post_summary_to_pr: true`:

```bash
gh pr comment "$PR_NUMBER" --body "$(cat <<EOF
<details>
<summary>/fix-review — ${PROVIDER} parallel pass · ${TOTAL_FINDINGS} findings · ${CONFIRMED_COUNT} fixed · ${DISMISSED_COUNT} dismissed</summary>

Wall time: ${WALL_TIME_MS} ms (vs sum-sequential ${SEQ_SUM_MS} ms — ${SPEEDUP}× speedup)
Models: ${MODELS_LIST}
Arbiter: Claude (vote count used as confidence prior)

| File:Line | Votes | Layer | Sev | Ruling |
|-----------|-------|-------|-----|--------|
$(jq -r --argjson n "$NUM_ROUNDS" '.[] | "| \(.file):\(.line) | \(.votes)/\($n) | L\(.layer) | \(.severity) | \(.ruling) |"' "$RUN_DIR/arbiter.json")
</details>
EOF
)"
```

---

## STEP 9: Telemetry — JSONL Append

Three round entries + one arbiter entry per run.

**Round entry:**
```jsonc
{
  "timestamp": "2026-06-25T12:34:56Z",
  "pr_number": 1,
  "round_number": 1,
  "model": "deepseek-v4-flash:cloud",
  "provider": "ollama",
  "findings_count": 3,
  "prompt_tokens": 8000,
  "completion_tokens": 400,
  "estimated_cost_usd": null,
  "duration_ms": 6200,
  "parallel": true
}
```

**Arbiter entry:**
```jsonc
{
  "timestamp": "2026-06-25T12:35:10Z",
  "pr_number": 1,
  "round_number": "arbiter",
  "model": "claude",
  "provider": "local",
  "confirmed": 2,
  "escalated": 0,
  "dismissed": 1,
  "added_new": 1,
  "parallel": true,
  "wall_time_ms": 7800
}
```

`estimated_cost_usd` is always `null` — Ollama Cloud free tier has no per-token billing.

```bash
COST=null

if [ "$TELEMETRY_ENABLED" = "true" ]; then
  TIMESTAMP=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
  ROUND_PROVIDER="$ACTIVE_PROVIDER"

  for N in $(seq 1 "$NUM_ROUNDS"); do
    meta="$RUN_DIR/round_${N}.meta"
    [ -f "$meta" ] || continue
    MODEL=$(sed -n '1p' "$meta")
    DURATION_MS=$(sed -n '2p' "$meta")
    read PROMPT_TOKENS COMPLETION_TOKENS < <(sed -n '3p' "$meta")
    FINDINGS_COUNT=$(jq 'length' "$RUN_DIR/round_${N}.findings.json")

    jq -cn \
      --arg    ts        "$TIMESTAMP" \
      --argjson pr       "${PR_NUMBER}" \
      --argjson round    "${N}" \
      --arg    model     "$MODEL" \
      --arg    provider  "$ROUND_PROVIDER" \
      --argjson findings "${FINDINGS_COUNT}" \
      --argjson ptokens  "${PROMPT_TOKENS:-null}" \
      --argjson ctokens  "${COMPLETION_TOKENS:-null}" \
      --argjson cost     "${COST:-null}" \
      --argjson duration "${DURATION_MS}" \
      '{timestamp:$ts, pr_number:$pr, round_number:$round, model:$model,
        provider:$provider, findings_count:$findings,
        prompt_tokens:$ptokens, completion_tokens:$ctokens,
        estimated_cost_usd:$cost, duration_ms:$duration, parallel:true}' \
      >> "$TELEMETRY_FILE" 2>/dev/null \
      || echo "warn: telemetry write failed for round ${N} — continuing" >&2
  done

  jq -cn \
    --arg    ts        "$TIMESTAMP" \
    --argjson pr       "${PR_NUMBER}" \
    --arg    round     "arbiter" \
    --arg    model     "claude" \
    --arg    provider  "local" \
    --argjson confirmed  "${CONFIRMED_COUNT}" \
    --argjson escalated  "${ESCALATED_COUNT}" \
    --argjson dismissed  "${DISMISSED_COUNT}" \
    --argjson added_new  "${ADDED_NEW_COUNT}" \
    --argjson wall       "${WALL_TIME_MS}" \
    '{timestamp:$ts, pr_number:$pr, round_number:$round, model:$model,
      provider:$provider, confirmed:$confirmed, escalated:$escalated,
      dismissed:$dismissed, added_new:$added_new,
      parallel:true, wall_time_ms:$wall}' \
    >> "$TELEMETRY_FILE" 2>/dev/null \
    || echo "warn: telemetry write failed for arbiter — continuing" >&2
fi
```

---

## STEP 10: Auto-merge (or ask if blocked)

Conditions for clean auto-merge — all must hold:

1. **No reverts** — `arbiter.json` contains no `"reverted"` rulings.
2. **PR mergeable** — no merge conflicts.
3. **Gates green or pre-existing-skip** — `GATES_OK=yes`.
4. **Remote checks not failing** — `gh pr checks` shows no `fail`/`error` rows.

```bash
MERGEABLE=$(gh pr view "$PR_NUMBER" --json mergeable --jq '.mergeable')
HAS_REVERT=$(jq -e 'any(.[]?; .ruling == "reverted")' "$RUN_DIR/arbiter.json" >/dev/null 2>&1 && echo "yes" || echo "no")
GATES_OK="${GATES_OK:-no}"
CHECKS_OUT=$(gh pr checks "$PR_NUMBER" 2>&1 || true)
if echo "$CHECKS_OUT" | grep -qE '^[a-zA-Z0-9_./-]+\s+(fail|error|cancelled)'; then
  CHECKS_OK=no
else
  CHECKS_OK=yes
fi

BLOCKING=()
[ "$MERGEABLE" != "MERGEABLE" ] && BLOCKING+=("PR not mergeable: ${MERGEABLE}")
[ "$HAS_REVERT" = "yes" ]       && BLOCKING+=("one or more fixes reverted (gate failure caused by this PR)")
[ "$GATES_OK"  != "yes" ]       && BLOCKING+=("local gates failed in a layer this PR touches")
[ "$CHECKS_OK" != "yes" ]       && BLOCKING+=("remote checks failing — see 'gh pr checks ${PR_NUMBER}'")

if [ ${#BLOCKING[@]} -eq 0 ]; then
  if ! gh pr merge "$PR_NUMBER" --auto --squash --delete-branch 2>/dev/null; then
    gh pr merge "$PR_NUMBER" --squash --delete-branch
  fi
  MERGE_STATUS="merged (squash)"
else
  cat <<EOM
PR #${PR_NUMBER} cannot auto-merge:
$(printf '  - %s\n' "${BLOCKING[@]}")

What should I do?
  1. Merge anyway (squash + delete branch)
  2. Leave open — you'll handle it manually
  3. Close PR — abandon
EOM
fi
```

**→ Now proceed to Step 11.** Whatever Step 10 returned does **not** end the run.

---

## STEP 11: Final Summary (printed)

```
## /fix-review (parallel) — PR #${PR_NUMBER}

Provider: ${ACTIVE_PROVIDER}
Rounds:   ${NUM_ROUNDS}
Models:   ${MODELS_LIST}
Wall time: ${WALL_TIME_MS} ms (sum-sequential ${SEQ_SUM_MS} ms — ${SPEEDUP}× speedup)

Aggregated findings: ${TOTAL_FINDINGS}
${VOTE_BAND_REPORT}

Arbiter:
  Confirmed: ${CONFIRMED_COUNT}
  Escalated: ${ESCALATED_COUNT}
  Dismissed: ${DISMISSED_COUNT}
  Deferred:  ${DEFERRED_COUNT}
  Added new: ${ADDED_NEW_COUNT}

Tests: ${TEST_RESULT}
Lint:  ${LINT_RESULT}

Commit: ${COMMIT_SHA}
PR:     ${PR_URL}
Merge:  ${MERGE_STATUS}
Telemetry: ${TELEMETRY_FILE}
${FAILOVER_SECTION}
```

---

## SWITCHING REVIEWER SET

Switch between Ollama Cloud and local Ollama:
- "switch fix-review to local"
- "switch fix-review to cloud"

```bash
sed -i "s/^reviewer_set: .*/reviewer_set: {new_set}/" .claude/skills/fix-review/config.yaml
```

On the next run, Step 0 picks the matching `reviewers.ollama_{cloud,local}` block.
