# Retrieval-quality eval (LoCoMo)

`make eval` measures how well `internal/search` surfaces the right text, with
no LLM judge and no cloud calls: LoCoMo labels which dialogue turns answer each
question, so recall is deterministic.

## Method

- Dataset: [LoCoMo](https://github.com/snap-research/locomo) `locomo10.json`
  (10 conversations, 1986 questions). **License CC BY-NC 4.0** — used for local,
  non-commercial evaluation only. Download it yourself into `tmp/eval/data/`
  (gitignored); it is never committed or redistributed.
- Each conversation → one throwaway SQLite store in `tmp/eval/db/` (this also
  caches embeddings; reruns embed nothing). Each utterance = one chunk.
- Per question: `search.Search(db, embedder, question, k)`; a hit is a result
  whose text belongs to an evidence turn. Results are mapped back to turns by
  content (`SearchResult` carries no chunk id; no change to `internal/types.go`).
  Utterances with identical text share ids, which is mildly optimistic.
- Metrics: Recall@{1,3,5,10} (**any** evidence turn / **all** evidence turns),
  MRR, mean search latency (includes the query embedding), mean size of the
  top-5 context (est. tokens = chars/4).
- Modes: `cosine` (bge-m3 via local Ollama), `bm25` (the FTS5 fallback path),
  `naive` (keyword-overlap baseline — the "simplest grep"), `cosine-mine`
  (same, but data goes through the real `mine` parser/noise filter instead of
  direct insert).
- Data hygiene: 4 questions have no parseable evidence id and are excluded
  (reported in the output). Evidence strings can pack several ids
  (`"D8:6; D9:17"`) or be garbled (`"D"`, `"D:11:26"`); well-formed ids are
  extracted, garbage dropped.
- Category names follow the official LoCoMo labels. memorybench's loader labels
  2 as multi-hop and 4 as world-knowledge; the data disagrees (category 2 is
  "When did …" temporal questions, 4 is the largest group = single-hop), so the
  official labels are used here. Category 5 (adversarial) questions do carry
  evidence in this file and are included, reported separately.

## First run (2026-10-12, bge-m3:latest, 1982 questions)

| mode | R@1 | R@3 | R@5 | R@10 | MRR | latency | top-5 ctx |
|---|---|---|---|---|---|---|---|
| cosine | 0.064 | 0.136 | 0.182 | 0.262 | 0.116 | 28.3 ms | 118 tok |
| cosine-mine | 0.064 | 0.136 | 0.182 | 0.262 | 0.116 | 27.9 ms | 120 tok |
| hybrid (RRF constant 60, depth 50) | 0.152 | 0.294 | 0.367 | 0.453 | 0.243 | 34.8 ms | 143 tok |
| **bm25** | **0.262** | **0.427** | **0.484** | **0.563** | **0.359** | 1.0 ms | 155 tok |
| naive keywords | 0.230 | 0.348 | 0.402 | 0.475 | 0.304 | 0.1 ms | 207 tok |

(Recall = any-evidence. Per-category and all-evidence numbers are in the
`make eval` output and `tmp/eval/report-<date>.json`.)

## Findings

1. **Cosine is the worst mode on this benchmark** — below even the naive
   keyword baseline; BM25 is ~4× better at R@1. This is a finding, not a
   harness bug: the vectors are valid (4096-byte, correct model tag) and
   spot-checking queries shows cosine returning replies that merely *address*
   the same person ("Thanks, Caroline! …") — the name token dominates short
   chat utterances, while the lexically-specific evidence turn ranks lower.
2. **Multi-hop "all evidence" recall is ≈0 for every mode** (single-turn
   retrieval cannot assemble several evidence turns at k≤10).
3. **The `mine` noise filter costs almost nothing here** (`cosine` ≈
   `cosine-mine`): dropping `<30`-char turns loses few evidence turns.
4. **Equal-weight hybrid does not beat BM25 on LoCoMo** (issue #88 step 1): RRF over cosine+BM25 lands between the two (R@1 0.152 vs BM25 0.262, MRR 0.243 vs 0.359) — the weak cosine ranking drags the fused list down. Untested: weighted fusion (e.g. BM25-heavy), a larger/smaller `depth`, other `k`. So on this benchmark the data supports "BM25 is the strong signal", not "fuse equally".

## Caveats — read before acting on the numbers

- LoCoMo is chat: short, context-poor utterances. session-indexer's real chunks
  are whole message paragraphs (≤1500 chars) from coding sessions, mixed
  Ukrainian/English. The cosine penalty may be smaller (or different) there.
  This measures one regime, not the product's real recall.
- One run, one embedding model, no tuning; k, tokenisation and the naive
  baseline's stop-word list are unvalidated choices.

## What to review next (not done in this change)

- `search` is cosine-first and uses FTS5 only as a fallback. `hybrid` mode now exists in the harness (equal-weight RRF) and did not beat BM25 here; try weighted fusion before drawing conclusions (issue #88).
- Re-run on a sample of real session chunks (a private, local-only variant of
  this harness) before changing production defaults.
- `distill --batch/--context-cap` and the distill model are **not** covered —
  this harness measures retrieval only.

## Real session store (private, #88 step 2)

`session-indexer-eval --real-db .claude/sessions.db --facts 1000` evaluates a
real store. The store is first copied with `VACUUM INTO` through a read-only
connection (the live DB is never written), **facts are the questions** (query =
`subject predicate object`) and each fact's **source chunk is the evidence**.
One fact per source chunk, sampled at an even stride (deterministic). Only
aggregate metrics are printed or written (`tmp/eval/report-real-<date>.json`);
no chunk or fact text leaves the machine.

First run (2026-10-12, this repo's own store: 38,317 chunks, bge-m3, 1000 sampled facts):

| mode | R@1 | R@3 | R@5 | R@10 | MRR | latency | top-5 ctx |
|---|---|---|---|---|---|---|---|
| cosine (current default) | 0.256 | 0.393 | 0.462 | 0.522 | 0.342 | 208 ms | 515 tok |
| bm25 | 0.332 | 0.494 | 0.546 | 0.626 | 0.424 | 21 ms | 811 tok |
| naive keywords | 0.208 | 0.355 | 0.422 | 0.496 | 0.298 | 14 ms | 1472 tok |
| hybrid (RRF) | 0.331 | 0.501 | 0.565 | 0.640 | 0.431 | 230 ms | 694 tok |

What it says, and what it does not:

- Both BM25 and hybrid beat the current cosine-first default on this proxy
  (MRR 0.424 / 0.431 vs 0.342). The ordering matches LoCoMo.
- **Hybrid vs BM25 is a tie within noise**: R@1 within 0.1 point (0.331 vs 0.332), +1–2 points at k≥3 on
  N=1000 (a single proportion near 0.5 has a ~±3 point 95% interval; paired
  significance was not computed). Hybrid costs ~10× the latency (it runs the
  full-scan cosine side) for no demonstrated gain.
- **The proxy favours lexical methods.** The query is an LLM paraphrase of the
  chunk it points back to, so it shares vocabulary with its evidence by
  construction. It does not model the use case cosine exists for — a human
  half-remembering an idea in different words. Real recall for such queries is
  **not measured here**, and `search`'s output for them is the thing users see.
- Facts are only an incomplete label: a fact may be supported by several
  near-duplicate chunks, and only its source chunk counts as a hit (strict,
  slightly pessimistic); results are mapped back to chunks by content, so
  identical texts share ids (slightly optimistic). The store is also this
  repo's own sessions only — one project, mixed UA/EN.

**Conclusion for #88 step 3 (changing `search` defaults): not justified by this
data alone.** The measured gap is real but the query model is biased toward
BM25, and hybrid does not beat BM25 here. A defensible next step needs labelled
queries that are *not* derived from their evidence (e.g. human-written
recall queries with a known target) — until then, leave `search` as is.

## Real store, recall-style queries (private, #88 step 3 input)

The fact-query proxy above is lexically biased, so a second query set was built
that does not share its evidence's vocabulary:
`session-indexer-eval --real-db .claude/sessions.db --queries tmp/eval/recall.json --gen-queries 200`
samples non-sensitive chunks (≥150 chars; anything matching secret/PII patterns
is excluded *before* text leaves the machine), asks an LLM (`agy`,
`gemini-3.8-flash-medium` — a different family from the `glm` that distilled the
facts and from `bge-m3`) to write the question a person would ask while only
half-remembering each chunk, forbidding reuse of its words, and then drops in Go
any query whose content-word stems overlap the chunk by more than 25%. Run the
same command without `--gen-queries` to evaluate. Chunk text is sent to the
generator; the query file stays under gitignored `tmp/` and only aggregates are
reported.

N=200 queries (220 generated, 0 dropped by the overlap filter, 4 of 26 batches
failed). One target chunk per query, 38,317 chunks:

| mode | R@1 | R@5 | R@10 | MRR |
|---|---|---|---|---|
| cosine | 0.020 | 0.065 | 0.100 | 0.037 |
| hybrid | 0.020 | 0.030 | 0.060 | 0.027 |
| bm25 | 0.000 | 0.000 | 0.005 | 0.001 |
| naive | 0.000 | 0.000 | 0.000 | 0.000 |

- **The ordering flips**: on recall-style queries cosine is clearly best and
  BM25 is near zero, the opposite of the fact-query and LoCoMo results. The
  default (`cosine`-first, FTS only as fallback) is the right one for this use.
- Hybrid (equal-weight RRF) is worse than cosine alone here, and it was no
  better than BM25 on fact-queries, so it earns nothing on either set.
- Absolute numbers are low: the target is one specific chunk among many
  near-duplicates from the same discussions, and the queries are deliberately
  vocabulary-free. Read them as a ranking of methods, not as expected recall.
- Caveats: model-generated (not human) queries, N=200 (cosine R@10 ≈ ±4
  points), single project, mixed UA/EN, the generator saw only a 700-char prefix
  of each chunk.

**Conclusion for #88 step 3: leave `search` defaults unchanged.** Neither
BM25-first nor hybrid wins on both query styles; cosine-first wins where it
matters (recall) and loses only on the lexical proxy.
