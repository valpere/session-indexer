package eval

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/valpere/session-indexer/internal"
	"github.com/valpere/session-indexer/internal/db"
)

// seedReal builds a store with nChunks chunks and one fact per even chunk.
func seedReal(t *testing.T, nChunks int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "real.db")
	d, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for i := 0; i < nChunks; i++ {
		id, _, err := db.InsertChunk(d, internal.Chunk{
			SessionID: "s", SessionDate: "2026-10-01", Role: "user", MessageIndex: i, ChunkIndex: 0,
			Content: fmt.Sprintf("chunk number %d talks about topic%d in detail", i, i), CreatedAt: "2026-10-01T00:00:00Z",
		})
		if err != nil {
			t.Fatal(err)
		}
		if i%2 == 0 {
			if _, err := db.InsertFact(d, internal.Fact{
				Subject: fmt.Sprintf("topic%d", i), Predicate: "is", Object: "discussed",
				Confidence: 0.9, SourceChunkID: id, SessionDate: "2026-10-01", CreatedAt: "2026-10-01T00:00:00Z",
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	return path
}

func TestSnapshotDoesNotTouchSource(t *testing.T) {
	src := seedReal(t, 4)
	dst := filepath.Join(t.TempDir(), "snap.db")
	if err := Snapshot(src, dst); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(dst)
	if err != nil {
		t.Fatalf("snapshot must be a valid store: %v", err)
	}
	defer d.Close()
	var n int
	d.QueryRow(`SELECT COUNT(*) FROM chunks`).Scan(&n)
	if n != 4 {
		t.Fatalf("snapshot chunks = %d, want 4", n)
	}
	if err := Snapshot(src, dst); err == nil {
		t.Fatal("must refuse to overwrite an existing snapshot (VACUUM INTO would fail; surface it)")
	}
}

func TestFromStoreBuildsTurnsAndFactQuestions(t *testing.T) {
	d, err := db.Open(seedReal(t, 10))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	c, err := FromStore(d, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Turns) != 10 || len(c.QAs) != 5 {
		t.Fatalf("turns=%d qas=%d, want 10/5", len(c.Turns), len(c.QAs))
	}
	q := c.QAs[0]
	if !strings.Contains(q.Question, "topic0") || len(q.Evidence) != 1 || q.Evidence[0] != c.Turns[0].DiaID {
		t.Fatalf("qa0 = %+v, turn0 = %s", q, c.Turns[0].DiaID)
	}
}

func TestFromStoreSampleIsDeterministicAndBounded(t *testing.T) {
	d, err := db.Open(seedReal(t, 40))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	a, _ := FromStore(d, 7)
	b, _ := FromStore(d, 7)
	if len(a.QAs) != 7 || len(b.QAs) != 7 {
		t.Fatalf("sample sizes %d/%d, want 7", len(a.QAs), len(b.QAs))
	}
	for i := range a.QAs {
		if a.QAs[i].Question != b.QAs[i].Question {
			t.Fatalf("sample not deterministic at %d", i)
		}
	}
}

func TestFromStoreOneQuestionPerSourceChunk(t *testing.T) {
	d, err := db.Open(seedReal(t, 4))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	// A second fact for chunk 0 must not give it double weight.
	db.InsertFact(d, internal.Fact{Subject: "extra", Predicate: "is", Object: "fact", Confidence: 0.9, SourceChunkID: 1, SessionDate: "2026-10-01", CreatedAt: "x"})
	c, _ := FromStore(d, 100)
	seen := map[string]int{}
	for _, q := range c.QAs {
		seen[q.Evidence[0]]++
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("source chunk %s has %d questions, want 1", id, n)
		}
	}
}

func TestEvaluateOverRealStoreFindsSourceChunk(t *testing.T) {
	d, err := db.Open(seedReal(t, 10))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	c, _ := FromStore(d, 100)
	qs, err := Evaluate(c, ContentIndex(c.Turns), DBRetriever(d, NoEmbedder{}), []int{1, 3})
	if err != nil {
		t.Fatal(err)
	}
	s := Summarize(qs, []int{1, 3})
	if s.N != 5 || s.RecallAny[1] != 1 {
		t.Fatalf("summary = %+v (BM25 over unique topic tokens should be perfect)", s)
	}
}

func TestSnapshotHandlesAwkwardPaths(t *testing.T) {
	// '?' and '#' are URI metacharacters: an unescaped DSN would drop mode=ro or
	// open a different file.
	src := seedReal(t, 3)
	dir := filepath.Join(t.TempDir(), "we?ird#dir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	awk := filepath.Join(dir, "src?x#y.db")
	// Copy the main file and any -wal/-shm sidecars (a WAL store keeps rows there).
	for _, suf := range []string{"", "-wal", "-shm"} {
		b, err := os.ReadFile(src + suf)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(awk+suf, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dst := filepath.Join(t.TempDir(), "snap'o.db") // db.Open (verification) can't take ?/# in a path, the source side can
	if err := Snapshot(awk, dst); err != nil {
		t.Fatalf("Snapshot with awkward paths: %v", err)
	}
	d, err := db.Open(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var n int
	d.QueryRow(`SELECT COUNT(*) FROM chunks`).Scan(&n)
	if n != 3 {
		t.Fatalf("chunks = %d, want 3", n)
	}
}

func TestSnapshotSeesUncheckpointedWALWrites(t *testing.T) {
	// A live writer keeps rows in the -wal file; the snapshot must include them.
	src := filepath.Join(t.TempDir(), "live.db")
	d, err := db.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close() // deliberately left open during Snapshot
	for i := 0; i < 5; i++ {
		if _, _, err := db.InsertChunk(d, internal.Chunk{SessionID: "s", SessionDate: "2026-10-01", Role: "user",
			MessageIndex: i, Content: fmt.Sprintf("live chunk %d with enough text", i), CreatedAt: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	dst := filepath.Join(t.TempDir(), "snap.db")
	if err := Snapshot(src, dst); err != nil {
		t.Fatal(err)
	}
	s, err := db.Open(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var n int
	s.QueryRow(`SELECT COUNT(*) FROM chunks`).Scan(&n)
	if n != 5 {
		t.Fatalf("snapshot chunks = %d, want 5 (WAL contents must be included)", n)
	}
}
