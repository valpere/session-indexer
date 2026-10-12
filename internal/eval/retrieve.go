package eval

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/valpere/session-indexer/internal"
	"github.com/valpere/session-indexer/internal/db"
	"github.com/valpere/session-indexer/internal/embed"
	"github.com/valpere/session-indexer/internal/mine"
	"github.com/valpere/session-indexer/internal/search"
)

// contextK is how many top results count toward the context-size measurement.
const contextK = 5

// Retriever returns the Content of the top-k results for a query, best first.
type Retriever func(query string, k int) ([]string, error)

// NoEmbedder is an always-unavailable embedder: search.Search with it takes
// the FTS5 BM25 fallback path, which is how BM25 mode is measured.
type NoEmbedder struct{}

func (NoEmbedder) Available() bool { return false }
func (NoEmbedder) Embed(context.Context, string) ([]float32, error) {
	return nil, errors.New("embeddings disabled")
}

// DBRetriever searches d through the production internal/search path.
func DBRetriever(d *sql.DB, emb embed.Embedder) Retriever {
	return func(q string, k int) ([]string, error) {
		res, _, err := search.Search(d, emb, q, k)
		if err != nil {
			return nil, err
		}
		out := make([]string, len(res))
		for i, r := range res {
			out[i] = r.Content
		}
		return out, nil
	}
}

var stopwords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`the and for are was were with that this from have has had you your not but what when where who whom which why how did does can could would should will its they them their about into over than then there here also just very been being`) {
		stopwords[w] = true
	}
}

// tokenize lowercases text and splits it on non-alphanumerics, keeping tokens
// of 3+ runes that are not stopwords.
func tokenize(text string) []string {
	var out []string
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if len([]rune(w)) >= 3 && !stopwords[w] {
			out = append(out, w)
		}
	}
	return out
}

// NaiveRetriever is the "simplest grep" baseline: rank turns by how many
// distinct query keywords they contain (ties keep conversation order).
func NaiveRetriever(turns []Turn) Retriever {
	sets := make([]map[string]bool, len(turns))
	for i, t := range turns {
		sets[i] = map[string]bool{}
		for _, w := range tokenize(t.Text) {
			sets[i][w] = true
		}
	}
	return func(q string, k int) ([]string, error) {
		qs := map[string]bool{}
		for _, w := range tokenize(q) {
			qs[w] = true
		}
		type scored struct{ i, score int }
		var sc []scored
		for i, s := range sets {
			n := 0
			for w := range qs {
				if s[w] {
					n++
				}
			}
			if n > 0 {
				sc = append(sc, scored{i, n})
			}
		}
		sort.SliceStable(sc, func(a, b int) bool { return sc[a].score > sc[b].score })
		var out []string
		for _, s := range sc {
			if len(out) == k {
				break
			}
			out = append(out, strings.TrimSpace(turns[s.i].Text))
		}
		return out, nil
	}
}

// ContentIndex maps an utterance's trimmed text to the dialogue ids that carry
// it. search results expose no chunk id, so results are mapped back to turns by
// content — the least invasive option (no change to internal/types.go).
func ContentIndex(turns []Turn) map[string][]string {
	idx := map[string][]string{}
	for _, t := range turns {
		k := strings.TrimSpace(t.Text)
		idx[k] = append(idx[k], t.DiaID)
	}
	return idx
}

func role(speaker string, first *string) string {
	if *first == "" {
		*first = speaker
	}
	if speaker == *first {
		return "user"
	}
	return "assistant"
}

func dateOrUnknown(d string) string {
	if d == "" {
		return "unknown"
	}
	return d
}

// embedPending embeds every chunk lacking a vector for emb's model. A no-op
// when emb is unavailable. Because it keys on ChunksNeedingEmbedding, a
// persistent DB file acts as the embedding cache: reruns embed nothing.
func embedPending(d *sql.DB, emb embed.Embedder) error {
	if !emb.Available() {
		return nil
	}
	tag := embed.ModelTag(emb)
	pend, err := db.ChunksNeedingEmbedding(d, tag)
	if err != nil {
		return err
	}
	for _, p := range pend {
		v, err := emb.Embed(context.Background(), p.Content)
		if err != nil {
			return fmt.Errorf("embed chunk %d: %w", p.ID, err)
		}
		if err := db.InsertEmbedding(d, p.ID, embed.EncodeVector(v), tag); err != nil {
			return err
		}
	}
	return nil
}

// BuildDirectDB indexes every turn as one chunk, bypassing mine's noise
// filter, so it measures pure retrieval. The DB at path is reused if present.
func BuildDirectDB(path string, conv Conversation, emb embed.Embedder) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	d, err := db.Open(path)
	if err != nil {
		return nil, err
	}
	first := ""
	for i, t := range conv.Turns {
		date := dateOrUnknown(t.Date)
		if _, _, err := db.InsertChunk(d, internal.Chunk{
			SessionID: conv.ID, SessionDate: date, Role: role(t.Speaker, &first),
			MessageIndex: i, ChunkIndex: 0, Content: strings.TrimSpace(t.Text),
			CreatedAt: date + "T00:00:00Z",
		}); err != nil {
			d.Close()
			return nil, err
		}
	}
	if err := embedPending(d, emb); err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

// BuildMineDB runs the conversation through the real mine pipeline (parser,
// noise filter, chunker). The gap vs BuildDirectDB is the recall lost to those
// filters on chat-style short utterances.
func BuildMineDB(path string, conv Conversation, emb embed.Embedder) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp("", "eval-"+conv.ID+"-*.jsonl")
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	enc := json.NewEncoder(f)
	first := ""
	for _, t := range conv.Turns {
		r := role(t.Speaker, &first)
		ts := t.Date
		if ts == "" {
			ts = "1970-01-01" // fixed sentinel: a wall-clock date would differ between runs
		}
		if err := enc.Encode(map[string]any{
			"type": r, "sessionId": conv.ID, "timestamp": ts + "T00:00:00Z",
			"message": map[string]any{"role": r, "content": t.Text},
		}); err != nil {
			f.Close()
			return nil, fmt.Errorf("write corpus: %w", err)
		}
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	if _, err := mine.Run(context.Background(), path, f.Name(), emb); err != nil {
		return nil, err
	}
	d, err := db.Open(path)
	if err != nil {
		return nil, err
	}
	if err := embedPending(d, emb); err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

// Evaluate runs every question of conv through r and judges the results.
func Evaluate(conv Conversation, idx map[string][]string, r Retriever, ks []int) ([]QResult, error) {
	maxK := 0
	for _, k := range ks {
		maxK = max(maxK, k)
	}
	out := make([]QResult, 0, len(conv.QAs))
	for _, q := range conv.QAs {
		t0 := time.Now()
		contents, err := r(q.Question, maxK)
		lat := float64(time.Since(t0).Microseconds()) / 1000
		if err != nil {
			return nil, fmt.Errorf("%s: %w", conv.ID, err)
		}
		ranked := make([][]string, len(contents))
		ctx := 0
		for i, c := range contents {
			ranked[i] = idx[strings.TrimSpace(c)]
			if i < contextK {
				ctx += len(c) / 4
			}
		}
		out = append(out, QResult{Category: q.Category, J: Judge(q.Evidence, ranked, ks), LatencyMs: lat, ContextTokens: ctx})
	}
	return out, nil
}
