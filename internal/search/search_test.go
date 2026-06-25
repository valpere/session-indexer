package search

import (
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
func (s stubEmbedder) Embed(text string) ([]float32, error) {
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

func TestGetStats(t *testing.T) {
	dbp := seed(t, stubEmbedder{avail: true})
	d, _ := db.Open(dbp)
	defer d.Close()
	st, err := GetStats(d)
	if err != nil {
		t.Fatalf("GetStats: %v", err)
	}
	if st.Chunks != 2 || st.Embedded != 2 || st.Pending != 0 {
		t.Fatalf("stats = %+v", st)
	}
}
