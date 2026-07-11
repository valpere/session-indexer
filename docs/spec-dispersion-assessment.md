# Specification dispersion assessment (H_spec sanity check, 2026-07-03)

Measured: docs/requirements.md @ 78fa059
Protocol: spec -> Go type definitions, x10 @ temperature=1.0;
          Go AST -> cosine similarity -> single-linkage clustering
H_max(N=10) = 3.32 bits

| Instrument                               | meanPairwiseSim | H@0.95 | clusters |
| ---------------------------------------- | --------------- | ------ | -------- |
| qwen3-coder:30b (Ollama local)           | 0.524           | 3.32   | 10/10    |
| kimi-k2.7-code (Ollama cloud, think=off) | 0.258           | 3.32   | 10/10    |

Reading: H saturated at ceiling on both instruments — every generation lands
in its own cluster. As an input for AI codegen agents, this document
constrains quality attributes and intent, but not type structure: FR prose
without schemas leaves the data-model space wide open. Reference points on
the same instrument A: vague toy spec 0.492, schema-pinned spec 0.730,
fully-specified baseline 1.000 (H=0).

Numbers are comparable only within one instrument+protocol configuration.
Raw data & harness: see `sanity/` in the article workspace.
