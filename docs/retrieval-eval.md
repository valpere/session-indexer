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

## Caveats — read before acting on the numbers

- LoCoMo is chat: short, context-poor utterances. session-indexer's real chunks
  are whole message paragraphs (≤1500 chars) from coding sessions, mixed
  Ukrainian/English. The cosine penalty may be smaller (or different) there.
  This measures one regime, not the product's real recall.
- One run, one embedding model, no tuning; k, tokenisation and the naive
  baseline's stop-word list are unvalidated choices.

## What to review next (not done in this change)

- `search` is cosine-first and uses FTS5 only as a fallback. On this data a
  **hybrid** (BM25 + cosine rank fusion) would likely beat both; worth an issue
  and an A/B in this harness (add a `hybrid` mode first, then decide).
- Re-run on a sample of real session chunks (a private, local-only variant of
  this harness) before changing production defaults.
- `distill --batch/--context-cap` and the distill model are **not** covered —
  this harness measures retrieval only.
