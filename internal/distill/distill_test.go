package distill

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/valpere/session-indexer/internal"
	"github.com/valpere/session-indexer/internal/db"
)

func TestAvailableTrueWhenModelPresent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"models":[{"name":"qwen2.5:latest"}]}`))
	}))
	defer srv.Close()
	c := NewClientURL(srv.URL, "qwen2.5:latest")
	if !c.Available() {
		t.Fatal("Available() = false, want true")
	}
}

func TestAvailableFalseWhenModelMissing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"models":[{"name":"llama3:latest"}]}`))
	}))
	defer srv.Close()
	c := NewClientURL(srv.URL, "qwen2.5:latest")
	if c.Available() {
		t.Fatal("Available() = true, want false (model absent)")
	}
}

func TestDistillParsesFacts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"response":"{\"facts\":[{\"subject\":\"session-indexer\",\"predicate\":\"has\",\"object\":\"33 merged PRs\",\"confidence\":0.95,\"supersedes_ids\":[]}]}"}`))
	}))
	defer srv.Close()
	c := NewClientURL(srv.URL, "qwen2.5:latest")
	candidates, err := c.Distill(context.Background(), "chunk text", nil)
	if err != nil {
		t.Fatalf("Distill: %v", err)
	}
	if len(candidates) != 1 || candidates[0].Subject != "session-indexer" {
		t.Fatalf("candidates = %+v", candidates)
	}
	if candidates[0].Confidence != 0.95 {
		t.Fatalf("confidence = %v, want 0.95", candidates[0].Confidence)
	}
}

func TestDistillSurfacesHTTPStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"model loading failed"}`))
	}))
	defer srv.Close()
	c := NewClientURL(srv.URL, "qwen2.5:latest")
	_, err := c.Distill(context.Background(), "chunk", nil)
	if err == nil {
		t.Fatal("expected error for 500 response, got nil")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("error should mention status 500, got: %v", err)
	}
}

func TestDistillRespectsContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()
	c := NewClientURL(srv.URL, "qwen2.5:latest")
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled
	_, err := c.Distill(ctx, "chunk", nil)
	if err == nil {
		t.Fatal("expected error for cancelled context, got nil")
	}
}

func TestDistillEmptyResponseGuard(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"response":""}`))
	}))
	defer srv.Close()
	c := NewClientURL(srv.URL, "qwen2.5:latest")
	_, err := c.Distill(context.Background(), "chunk", nil)
	if err == nil {
		t.Fatal("expected error for empty response, got nil")
	}
}

// stubDistiller is a Distiller test double driven by a queue of canned
// responses, one per Distill call (matching call order).
type stubDistiller struct {
	avail bool
	calls []func(chunk string, existing []internal.Fact) ([]Candidate, error)
	n     int
}

func (s *stubDistiller) Available() bool { return s.avail }
func (s *stubDistiller) Distill(_ context.Context, chunk string, existing []internal.Fact) ([]Candidate, error) {
	f := s.calls[s.n]
	s.n++
	return f(chunk, existing)
}

var seedChunkCounter int

func seedChunk(t *testing.T, d *sql.DB, content, sessionDate string) int64 {
	t.Helper()
	c := internal.Chunk{SessionID: "s1", SessionDate: sessionDate, Role: "user",
		MessageIndex: seedChunkCounter, ChunkIndex: 0, Content: content, CreatedAt: sessionDate + "T10:00:00Z"}
	seedChunkCounter++
	id, inserted, err := db.InsertChunk(d, c)
	if err != nil || !inserted {
		t.Fatalf("seedChunk: id=%d inserted=%v err=%v", id, inserted, err)
	}
	return id
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestRunAppliesConfidenceGate(t *testing.T) {
	d := openTestDB(t)
	seedChunk(t, d, "chunk one content here", "2026-07-01")
	cli := &stubDistiller{avail: true, calls: []func(string, []internal.Fact) ([]Candidate, error){
		func(string, []internal.Fact) ([]Candidate, error) {
			return []Candidate{
				{Subject: "s", Predicate: "p", Object: "high", Confidence: 0.9},
				{Subject: "s", Predicate: "p", Object: "low", Confidence: 0.4},
			}, nil
		},
	}}
	res, err := Run(context.Background(), d, cli, Config{Threshold: 0.7, ContextCap: 200})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.FactsInserted != 1 || res.BelowThreshold != 1 {
		t.Fatalf("res = %+v, want FactsInserted=1 BelowThreshold=1", res)
	}
}

func TestRunAppliesSupersession(t *testing.T) {
	d := openTestDB(t)
	// First chunk establishes a fact.
	seedChunk(t, d, "old status chunk", "2026-07-01")
	cli := &stubDistiller{avail: true, calls: []func(string, []internal.Fact) ([]Candidate, error){
		func(string, []internal.Fact) ([]Candidate, error) {
			return []Candidate{{Subject: "project", Predicate: "status", Object: "not started", Confidence: 0.9}}, nil
		},
	}}
	if _, err := Run(context.Background(), d, cli, Config{Threshold: 0.7, ContextCap: 200}); err != nil {
		t.Fatalf("Run 1: %v", err)
	}
	existing, err := db.CurrentFacts(d, 200)
	if err != nil || len(existing) != 1 {
		t.Fatalf("CurrentFacts after run 1: %v err=%v", existing, err)
	}
	oldID := existing[0].ID

	// Second chunk supersedes the first, citing the id it was given.
	seedChunk(t, d, "new status chunk", "2026-07-02")
	cli2 := &stubDistiller{avail: true, calls: []func(string, []internal.Fact) ([]Candidate, error){
		func(_ string, given []internal.Fact) ([]Candidate, error) {
			if len(given) != 1 || given[0].ID != oldID {
				t.Fatalf("expected context to contain old fact id %d, got %+v", oldID, given)
			}
			return []Candidate{{Subject: "project", Predicate: "status", Object: "in progress",
				Confidence: 0.9, SupersedesIDs: []int64{oldID}}}, nil
		},
	}}
	res, err := Run(context.Background(), d, cli2, Config{Threshold: 0.7, ContextCap: 200})
	if err != nil {
		t.Fatalf("Run 2: %v", err)
	}
	if res.Superseded != 1 {
		t.Fatalf("res.Superseded = %d, want 1", res.Superseded)
	}
	current, err := db.CurrentFacts(d, 200)
	if err != nil || len(current) != 1 || current[0].Object != "in progress" {
		t.Fatalf("current facts after supersession = %+v err=%v", current, err)
	}
}

func TestRunMarksChunkOnZeroFacts(t *testing.T) {
	d := openTestDB(t)
	seedChunk(t, d, "chunk with nothing extractable", "2026-07-01")
	cli := &stubDistiller{avail: true, calls: []func(string, []internal.Fact) ([]Candidate, error){
		func(string, []internal.Fact) ([]Candidate, error) { return nil, nil },
	}}
	res, err := Run(context.Background(), d, cli, Config{Threshold: 0.7, ContextCap: 200})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ChunksDistilled != 1 || res.FactsInserted != 0 {
		t.Fatalf("res = %+v, want ChunksDistilled=1 FactsInserted=0", res)
	}
	pending, err := db.ChunksWithoutFacts(d)
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending after zero-fact chunk = %+v err=%v, want none (must not re-distill)", pending, err)
	}
}

func TestRunLeavesChunkOnError(t *testing.T) {
	d := openTestDB(t)
	seedChunk(t, d, "chunk that fails distillation", "2026-07-01")
	cli := &stubDistiller{avail: true, calls: []func(string, []internal.Fact) ([]Candidate, error){
		func(string, []internal.Fact) ([]Candidate, error) {
			return nil, context.DeadlineExceeded
		},
	}}
	res, err := Run(context.Background(), d, cli, Config{Threshold: 0.7, ContextCap: 200})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Failed != 1 {
		t.Fatalf("res.Failed = %d, want 1", res.Failed)
	}
	pending, err := db.ChunksWithoutFacts(d)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending after failed chunk = %+v err=%v, want 1 (must be retried later)", pending, err)
	}
}

func TestRunRejectsSupersedeIDOutsideContext(t *testing.T) {
	d := openTestDB(t)
	seedChunk(t, d, "chunk citing an id it was never given", "2026-07-01")
	cli := &stubDistiller{avail: true, calls: []func(string, []internal.Fact) ([]Candidate, error){
		func(string, []internal.Fact) ([]Candidate, error) {
			// existing is empty (no prior facts), yet the model claims to
			// supersede id 999 — must be rejected, not applied blindly.
			return []Candidate{{Subject: "s", Predicate: "p", Object: "o",
				Confidence: 0.9, SupersedesIDs: []int64{999}}}, nil
		},
	}}
	res, err := Run(context.Background(), d, cli, Config{Threshold: 0.7, ContextCap: 200})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Superseded != 0 {
		t.Fatalf("res.Superseded = %d, want 0 (id 999 was never in the given context)", res.Superseded)
	}
}
