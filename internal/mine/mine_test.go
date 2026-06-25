package mine

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// fakeEmbedder returns a fixed-length vector and reports available.
type fakeEmbedder struct{ avail bool }

func (f fakeEmbedder) Available() bool { return f.avail }
func (f fakeEmbedder) Embed(string) ([]float32, error) {
	return []float32{0.1, 0.2, 0.3}, nil
}

func writeJSONL(t *testing.T, lines string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "abc123.jsonl")
	if err := os.WriteFile(p, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunInsertsChunksAndEmbeds(t *testing.T) {
	jsonl := `{"type":"user","sessionId":"s1","timestamp":"2026-06-25T10:00:00Z","message":{"role":"user","content":"This is a long enough question about database design choices."}}`
	jp := writeJSONL(t, jsonl)
	dbp := filepath.Join(t.TempDir(), "sessions.db")
	res, err := Run(dbp, jp, fakeEmbedder{avail: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ChunksInserted != 1 || res.Embedded != 1 {
		t.Fatalf("result = %+v, want 1 inserted / 1 embedded", res)
	}
}

func TestRunIdempotent(t *testing.T) {
	jsonl := `{"type":"user","sessionId":"s1","timestamp":"2026-06-25T10:00:00Z","message":{"role":"user","content":"This is a long enough question about database design choices."}}`
	jp := writeJSONL(t, jsonl)
	dbp := filepath.Join(t.TempDir(), "sessions.db")
	if _, err := Run(dbp, jp, fakeEmbedder{avail: false}); err != nil {
		t.Fatal(err)
	}
	res, err := Run(dbp, jp, fakeEmbedder{avail: false})
	if err != nil {
		t.Fatal(err)
	}
	if res.ChunksInserted != 0 {
		t.Fatalf("second run inserted %d, want 0", res.ChunksInserted)
	}
}

type errEmbedder struct{}

func (e errEmbedder) Available() bool                 { return true }
func (e errEmbedder) Embed(string) ([]float32, error) { return nil, fmt.Errorf("embed failed") }

func TestRunSkippedOnEmbedError(t *testing.T) {
	jsonl := `{"type":"user","sessionId":"s1","timestamp":"2026-06-25T10:00:00Z","message":{"role":"user","content":"This is a long enough question about database design choices."}}`
	jp := writeJSONL(t, jsonl)
	dbp := filepath.Join(t.TempDir(), "sessions.db")
	res, err := Run(dbp, jp, errEmbedder{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ChunksInserted != 1 || res.Skipped != 1 || res.Embedded != 0 {
		t.Fatalf("result = %+v, want 1 inserted / 1 skipped / 0 embedded", res)
	}
}

func TestRunSkipsEmbeddingsWhenUnavailable(t *testing.T) {
	jsonl := `{"type":"user","sessionId":"s1","timestamp":"2026-06-25T10:00:00Z","message":{"role":"user","content":"This is a long enough question about database design choices."}}`
	jp := writeJSONL(t, jsonl)
	dbp := filepath.Join(t.TempDir(), "sessions.db")
	res, err := Run(dbp, jp, fakeEmbedder{avail: false})
	if err != nil {
		t.Fatal(err)
	}
	if res.ChunksInserted != 1 || res.Embedded != 0 {
		t.Fatalf("result = %+v, want 1 inserted / 0 embedded", res)
	}
}
