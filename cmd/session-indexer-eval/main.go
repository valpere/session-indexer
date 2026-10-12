// Command session-indexer-eval measures retrieval quality (Recall@k, MRR,
// latency, context size) of internal/search on the public LoCoMo benchmark.
// It needs a live local Ollama (bge-m3) for the cosine modes, so it is a
// separate binary run via `make eval`, never part of `make test`.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/valpere/session-indexer/internal/embed"
	"github.com/valpere/session-indexer/internal/eval"
)

func main() {
	var (
		data  = flag.String("data", "tmp/eval/data/locomo10.json", "path to locomo10.json (CC BY-NC 4.0 — keep local, never commit)")
		cache = flag.String("cache", "tmp/eval/db", "directory for per-conversation SQLite stores (doubles as the embedding cache)")
		out   = flag.String("out", "tmp/eval", "directory for the JSON report")
		modes = flag.String("modes", "cosine,bm25,naive,cosine-mine", "comma-separated: cosine, bm25, naive, cosine-mine, bm25-mine")
		limit = flag.Int("convs", 0, "evaluate only the first N conversations (0 = all)")
	)
	flag.Parse()
	if err := run(*data, *cache, *out, strings.Split(*modes, ","), *limit); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(data, cache, out string, modes []string, limit int) error {
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
		needsCosine := strings.HasPrefix(mode, "cosine")
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
	return eval.Evaluate(c, idx, eval.DBRetriever(d, embedder), ks)
}

// buildEmbedder returns the embedder used to populate a store: the real one
// whenever Ollama is up, so a BM25 run never leaves a store half-embedded.
func buildEmbedder(wanted embed.Embedder, real *embed.Client) embed.Embedder {
	if real.Available() {
		return real
	}
	return wanted
}
