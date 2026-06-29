package search

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/valpere/session-indexer/internal"
	"github.com/valpere/session-indexer/internal/db"
	"github.com/valpere/session-indexer/internal/embed"
)

type stubEmbedder struct {
	avail bool
	vecs  map[string][]float32
}

func (s stubEmbedder) Available() bool { return s.avail }
func (s stubEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	if v, ok := s.vecs[text]; ok {
		return v, nil
	}
	return []float32{0, 0, 1}, nil // query default
}

func seed(t *testing.T, emb embed.Embedder) string {
	t.Helper()
	dbp := filepath.Join(t.TempDir(), "sessions.db")
	d, err := db.Open(dbp)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	rows := []struct {
		content string
		vec     []float32
	}{
		{"ring buffer for the event queue avoids allocations", []float32{0, 0, 1}},
		{"json schema config validation approach", []float32{1, 0, 0}},
	}
	for i, r := range rows {
		c := internal.Chunk{SessionID: "s1", SessionDate: "2026-06-25", Role: "user",
			MessageIndex: i, ChunkIndex: 0, Content: r.content, CreatedAt: "2026-06-25T10:00:00Z"}
		id, _, _ := db.InsertChunk(d, c)
		db.InsertEmbedding(d, id, embed.EncodeVector(r.vec))
	}
	return dbp
}

func TestSearchCosineRanksClosestFirst(t *testing.T) {
	emb := stubEmbedder{avail: true}
	dbp := seed(t, emb)
	d, _ := db.Open(dbp)
	defer d.Close()
	// query vec defaults to {0,0,1} → closest is the ring buffer row.
	res, used, err := Search(d, emb, "queue buffering", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if !used {
		t.Fatal("usedEmbeddings = false, want true")
	}
	if len(res) == 0 || res[0].Content != "ring buffer for the event queue avoids allocations" {
		t.Fatalf("top result = %+v", res)
	}
}

func TestSearchFTSFallback(t *testing.T) {
	emb := stubEmbedder{avail: false}
	dbp := seed(t, stubEmbedder{avail: true})
	d, _ := db.Open(dbp)
	defer d.Close()
	res, used, err := Search(d, emb, "validation", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if used {
		t.Fatal("usedEmbeddings = true, want false (FTS fallback)")
	}
	if len(res) == 0 {
		t.Fatal("FTS returned no results for 'validation'")
	}
}

// TestSearchFallsBackToFTSWhenNoEmbeddings: Ollama is up but the DB has zero
// embedding rows (e.g. mined while Ollama was down, now back). Cosine would
// return nothing; search must fall back to FTS rather than go blind.
func TestSearchFallsBackToFTSWhenNoEmbeddings(t *testing.T) {
	dbp := filepath.Join(t.TempDir(), "sessions.db")
	d, err := db.Open(dbp)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	c := internal.Chunk{SessionID: "s1", SessionDate: "2026-06-25", Role: "user",
		MessageIndex: 0, ChunkIndex: 0, Content: "json schema config validation approach", CreatedAt: "2026-06-25T10:00:00Z"}
	if _, _, err := db.InsertChunk(d, c); err != nil {
		t.Fatal(err)
	}
	emb := stubEmbedder{avail: true}
	res, used, err := Search(d, emb, "validation", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if used {
		t.Fatal("usedEmbeddings = true, want false (no embeddings -> FTS fallback)")
	}
	if len(res) == 0 {
		t.Fatal("expected FTS results when embeddings table empty")
	}
}

// TestSearchFTSMatchesNonAdjacentTerms: the FTS fallback must OR the terms so
// a query like "config validation" matches a chunk where the words are not
// adjacent (a phrase match would miss it).
func TestSearchFTSMatchesNonAdjacentTerms(t *testing.T) {
	dbp := filepath.Join(t.TempDir(), "sessions.db")
	d, err := db.Open(dbp)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	c := internal.Chunk{SessionID: "s1", SessionDate: "2026-06-25", Role: "user",
		MessageIndex: 0, ChunkIndex: 0, Content: "the validation step checks the input config separately", CreatedAt: "2026-06-25T10:00:00Z"}
	if _, _, err := db.InsertChunk(d, c); err != nil {
		t.Fatal(err)
	}
	emb := stubEmbedder{avail: false}
	res, _, err := Search(d, emb, "config validation", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) == 0 {
		t.Fatal("phrase match failed; OR match should find non-adjacent terms")
	}
}

func TestGetStats(t *testing.T) {
	dbp := seed(t, stubEmbedder{avail: true})
	d, _ := db.Open(dbp)
	defer d.Close()
	st, err := GetStats(d, dbp)
	if err != nil {
		t.Fatalf("GetStats: %v", err)
	}
	if st.Chunks != 2 || st.Embedded != 2 || st.Pending != 0 {
		t.Fatalf("stats = %+v", st)
	}
	if st.DBSize == "" {
		t.Fatal("DBSize empty, want a human-readable size from os.Stat")
	}
}
