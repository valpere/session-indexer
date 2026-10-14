// Command session-indexer-eval measures retrieval quality (Recall@k, MRR,
// latency, context size) of internal/search on the public LoCoMo benchmark.
// It needs a live local Ollama (bge-m3) for the cosine modes, so it is a
// separate binary run via `make eval`, never part of `make test`.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/valpere/session-indexer/internal/db"
	"github.com/valpere/session-indexer/internal/embed"
	"github.com/valpere/session-indexer/internal/eval"
)

func main() {
	var (
		data    = flag.String("data", "tmp/eval/data/locomo10.json", "path to locomo10.json (CC BY-NC 4.0 — keep local, never commit)")
		cache   = flag.String("cache", "tmp/eval/db", "directory for per-conversation SQLite stores (doubles as the embedding cache)")
		out     = flag.String("out", "tmp/eval", "directory for the JSON report")
		modes   = flag.String("modes", "", "comma-separated (default: all non-mine modes for -real-db, plus cosine-mine for LoCoMo): cosine, bm25, naive, hybrid, cosine-mine, bm25-mine, hybrid-mine")
		limit   = flag.Int("convs", 0, "evaluate only the first N conversations (0 = all)")
		realDB  = flag.String("real-db", "", "evaluate a real session store instead of LoCoMo: facts are the questions, their source chunks the evidence (private data: snapshotted under -cache, only aggregates are printed)")
		facts   = flag.Int("facts", 300, "with -real-db: how many facts to sample (cosine scans every vector per query, so keep this modest)")
		queries = flag.String("queries", "", "with -real-db: JSON file of recall-style queries (from -gen-queries) used instead of fact-queries; private, keep under tmp/")
		genN    = flag.Int("gen-queries", 0, "with -real-db and -queries: sample N non-sensitive chunks, have the generator write a recall-style query for each, save to -queries and exit (chunk text is sent to the generator)")
		genMod  = flag.String("gen-model", "gemini-3.8-flash-medium", "model for -gen-queries (agy CLI, see `agy models`)")
	)
	flag.Parse()
	if *modes == "" {
		*modes = "cosine,bm25,naive,hybrid"
		if *realDB == "" {
			*modes += ",cosine-mine"
		}
	}
	var err error
	if *realDB != "" {
		err = runReal(*realDB, *cache, *out, strings.Split(*modes, ","), *facts, recallOpts{file: *queries, gen: *genN, model: *genMod})
	} else {
		err = run(*data, *cache, *out, strings.Split(*modes, ","), *limit)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(data, cache, out string, modes []string, limit int) error {
	if err := validateModes(modes); err != nil {
		return err
	}
	convs, err := eval.LoadLoCoMo(data)
	if err != nil {
		return err
	}
	if limit > 0 && limit < len(convs) {
		convs = convs[:limit]
	}
	ks := []int{1, 3, 5, 10}
	emb := embed.NewClient()
	rep := eval.Report{Dataset: "LoCoMo (locomo10)", Date: time.Now().Format("2006-01-02"), Ks: ks}
	for _, c := range convs {
		rep.NoEvidence += c.NoEvidence
	}
	for _, mode := range modes {
		needsCosine := strings.HasPrefix(mode, "cosine") || strings.HasPrefix(mode, "hybrid")
		if needsCosine && !emb.Available() {
			return fmt.Errorf("mode %q needs Ollama with %s: start it, or drop the mode", mode, "bge-m3")
		}
		var all []eval.QResult
		byCat := map[int][]eval.QResult{}
		for _, c := range convs {
			fmt.Fprintf(os.Stderr, "[%s] %s (%d turns, %d questions)\n", mode, c.ID, len(c.Turns), len(c.QAs))
			qs, err := evalOne(mode, c, cache, emb, ks)
			if err != nil {
				return err
			}
			for _, q := range qs {
				all = append(all, q)
				byCat[q.Category] = append(byCat[q.Category], q)
			}
		}
		mr := eval.ModeReport{Mode: mode, Overall: eval.Summarize(all, ks), PerCategory: map[int]eval.Summary{}}
		for cat, qs := range byCat {
			mr.PerCategory[cat] = eval.Summarize(qs, ks)
		}
		rep.Modes = append(rep.Modes, mr)
	}
	fmt.Print(eval.RenderMarkdown(rep))
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(out, "report-"+rep.Date+".json"), b, 0o644)
}

// validateModes fails fast on an unknown mode — otherwise a typo would silently
// fall through to the cosine path and report the wrong numbers.
func validateModes(modes []string) error {
	for _, m := range modes {
		switch m {
		case "cosine", "bm25", "naive", "hybrid", "cosine-mine", "bm25-mine", "hybrid-mine":
		default:
			return fmt.Errorf("unknown mode %q (want cosine, bm25, naive, hybrid, cosine-mine, bm25-mine, hybrid-mine)", m)
		}
	}
	return nil
}

func evalOne(mode string, c eval.Conversation, cache string, emb *embed.Client, ks []int) ([]eval.QResult, error) {
	idx := eval.ContentIndex(c.Turns)
	if mode == "naive" {
		return eval.Evaluate(c, idx, eval.NaiveRetriever(c.Turns), ks)
	}
	var embedder embed.Embedder = emb
	if mode == "bm25" || mode == "bm25-mine" {
		embedder = eval.NoEmbedder{}
	}
	// BM25 modes reuse the cosine stores (same chunks); building them with the
	// real embedder keeps those caches warm for the cosine modes too.
	build := eval.BuildDirectDB
	suffix := ".db"
	if strings.HasSuffix(mode, "-mine") {
		build, suffix = eval.BuildMineDB, ".mine.db"
	}
	d, err := build(filepath.Join(cache, c.ID+suffix), c, buildEmbedder(embedder, emb))
	if err != nil {
		return nil, err
	}
	defer d.Close()
	var r eval.Retriever = eval.DBRetriever(d, embedder)
	if strings.HasPrefix(mode, "hybrid") {
		// Same store, two paths: cosine via the real embedder, BM25 via the FTS fallback.
		r = eval.HybridRetriever(eval.DBRetriever(d, emb), eval.DBRetriever(d, eval.NoEmbedder{}), hybridDepth)
	}
	return eval.Evaluate(c, idx, r, ks)
}

// hybridDepth is how many results each side contributes to rank fusion.
const hybridDepth = 50

// buildEmbedder returns the embedder used to populate a store: the real one
// whenever Ollama is up, so a BM25 run never leaves a store half-embedded.
func buildEmbedder(wanted embed.Embedder, real *embed.Client) embed.Embedder {
	if real.Available() {
		return real
	}
	return wanted
}

// validateRealModes allows only the modes that make sense on an already-mined
// store: the "-mine" variants re-ingest a dataset through the parser.
func validateRealModes(modes []string) error {
	for _, m := range modes {
		switch m {
		case "cosine", "bm25", "naive", "hybrid":
		default:
			return fmt.Errorf("mode %q is not valid with -real-db (want cosine, bm25, naive, hybrid)", m)
		}
	}
	return nil
}

// runReal evaluates a real session store. The store is first snapshotted (the
// live file is never opened for writing), then facts become queries and their
// source chunks the evidence. Output is aggregate metrics only — never chunk
// or fact text — so the numbers are safe to quote while the data stays private.
func runReal(realDB, cache, out string, modes []string, nFacts int, ro recallOpts) error {
	if err := validateRealModes(modes); err != nil {
		return err
	}
	if nFacts <= 0 {
		return fmt.Errorf("-facts must be > 0 (cosine scans every vector per query; sample a bounded set)")
	}
	if err := os.MkdirAll(cache, 0o755); err != nil {
		return err
	}
	snap := filepath.Join(cache, "real.snapshot.db")
	removeSnapshot := func() {
		for _, s := range []string{"", "-wal", "-shm"} {
			_ = os.Remove(snap + s)
		}
	}
	removeSnapshot()
	// The snapshot is a full copy of private chunk text: delete it when done (this
	// defer is registered before d.Close, so it runs after the handle is closed).
	defer removeSnapshot()
	if err := eval.Snapshot(realDB, snap); err != nil {
		return err
	}
	d, err := db.Open(snap)
	if err != nil {
		return err
	}
	defer d.Close()
	conv, err := eval.FromStore(d, nFacts)
	if err != nil {
		return err
	}
	queryKind := "fact-queries"
	switch {
	case ro.gen > 0:
		return generateRecall(conv, ro)
	case ro.file != "":
		b, err := os.ReadFile(ro.file)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(b, &conv.QAs); err != nil {
			return fmt.Errorf("read %s: %w", ro.file, err)
		}
		queryKind = "recall-queries"
	}
	emb := embed.NewClient()
	for _, m := range modes {
		if (m == "cosine" || m == "hybrid") && !emb.Available() {
			return fmt.Errorf("mode %q needs Ollama with bge-m3", m)
		}
	}
	if needsEmbeddings(modes) {
		models, err := db.EmbeddingModels(d)
		if err != nil {
			return err
		}
		tag, haveTag := embed.ModelTag(emb), false
		for _, mc := range models {
			haveTag = haveTag || mc.Model == tag
		}
		if !haveTag {
			return fmt.Errorf("store has no embeddings for %s (it has %v); run `session-indexer embed` first", tag, models)
		}
	}
	fmt.Fprintf(os.Stderr, "real store: %d chunks, %d %s\n", len(conv.Turns), len(conv.QAs), queryKind)

	ks := []int{1, 3, 5, 10}
	idx := eval.ContentIndex(conv.Turns)
	rep := eval.Report{Dataset: fmt.Sprintf("real session store (private; %d chunks, %d %s)", len(conv.Turns), len(conv.QAs), queryKind),
		Date: time.Now().Format("2006-01-02"), Ks: ks}
	for _, m := range modes {
		var r eval.Retriever
		switch m {
		case "cosine":
			r = eval.DBRetriever(d, emb)
		case "bm25":
			r = eval.DBRetriever(d, eval.NoEmbedder{})
		case "naive":
			r = eval.NaiveRetriever(conv.Turns)
		case "hybrid":
			r = eval.HybridRetriever(eval.DBRetriever(d, emb), eval.DBRetriever(d, eval.NoEmbedder{}), hybridDepth)
		}
		fmt.Fprintf(os.Stderr, "[%s] evaluating\n", m)
		qs, err := eval.Evaluate(conv, idx, r, ks)
		if err != nil {
			return err
		}
		rep.Modes = append(rep.Modes, eval.ModeReport{Mode: m, Overall: eval.Summarize(qs, ks), PerCategory: map[int]eval.Summary{}})
	}
	fmt.Print(eval.RenderMarkdown(rep))
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(out, "report-real-"+rep.Date+reportSuffix(ro)+".json"), b, 0o644)
}

// needsEmbeddings reports whether any mode searches by vector, i.e. whether the
// store must hold embeddings for the current model (bm25/naive-only runs don't).
func needsEmbeddings(modes []string) bool {
	for _, m := range modes {
		if m == "cosine" || m == "hybrid" {
			return true
		}
	}
	return false
}

// recallOpts selects recall-style queries (generated, or loaded from file) over
// the default fact-queries for a -real-db run.
type recallOpts struct {
	file  string // JSON QA list; read for evaluation, written by generation
	gen   int    // >0: generate this many queries and exit
	model string
}

func reportSuffix(ro recallOpts) string {
	if ro.file != "" {
		return "-recall"
	}
	return ""
}

// generateRecall samples non-sensitive chunks, asks the generator for a
// recall-style query each, and saves the survivors to ro.file. The file is as
// private as the store (queries paraphrase chunk text); only counts are printed.
func generateRecall(conv eval.Conversation, ro recallOpts) error {
	if ro.file == "" {
		return fmt.Errorf("-gen-queries needs -queries <file> to write to")
	}
	if _, err := exec.LookPath("agy"); err != nil {
		return fmt.Errorf("-gen-queries needs the agy CLI on PATH: %w", err)
	}
	// Generate from 1.3× the target: the overlap filter drops a share.
	sample := eval.SampleChunks(conv.Turns, ro.gen*13/10)
	fmt.Fprintf(os.Stderr, "sending %d non-sensitive chunks to agy (%s) in batches\n", len(sample), ro.model)
	qas, st, err := eval.GenerateQueries(context.Background(), eval.AgyGenerator(ro.model), sample, 0)
	if err != nil {
		return err
	}
	if len(qas) > ro.gen {
		qas = qas[:ro.gen]
	}
	b, err := json.MarshalIndent(qas, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(ro.file, b, 0o600); err != nil {
		return err
	}
	fmt.Printf("generated %d, kept %d (saved %d), dropped: overlap %d, bad index %d, malformed %d; failed batches %d\n",
		st.Generated, st.Kept, len(qas), st.Overlap, st.BadIndex, st.Malformed, st.BatchErrors)
	return nil
}
