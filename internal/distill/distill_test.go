package distill

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
	candidates, err := c.Distill(context.Background(), []string{"chunk text"}, nil)
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

// TestDistillParsesFencedFacts verifies a response wrapped in a markdown
// ```json fence still parses — observed from gemma4:31b-cloud, which
// ignores the "format":"json" request parameter. glm-5.2:cloud does not do
// this, so this guards against a model swap silently breaking every chunk.
func TestDistillParsesFencedFacts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"response":"` +
			"```json\\n{\\\"facts\\\":[{\\\"subject\\\":\\\"s\\\",\\\"predicate\\\":\\\"p\\\",\\\"object\\\":\\\"o\\\",\\\"confidence\\\":0.9,\\\"supersedes_ids\\\":[]}]}\\n```" +
			`"}`))
	}))
	defer srv.Close()
	c := NewClientURL(srv.URL, "gemma4:31b-cloud")
	candidates, err := c.Distill(context.Background(), []string{"chunk text"}, nil)
	if err != nil {
		t.Fatalf("Distill: %v", err)
	}
	if len(candidates) != 1 || candidates[0].Subject != "s" {
		t.Fatalf("candidates = %+v", candidates)
	}
}

func TestStripMarkdownFence(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain JSON untouched", `{"facts":[]}`, `{"facts":[]}`},
		{"fenced with json tag", "```json\n{\"facts\":[]}\n```", `{"facts":[]}`},
		{"fenced without tag", "```\n{\"facts\":[]}\n```", `{"facts":[]}`},
		{"surrounding whitespace", "  \n```json\n{\"facts\":[]}\n```\n  ", `{"facts":[]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := stripMarkdownFence(tc.in)
			if got != tc.want {
				t.Fatalf("stripMarkdownFence(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestDistillSurfacesHTTPStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"model loading failed"}`))
	}))
	defer srv.Close()
	c := NewClientURL(srv.URL, "qwen2.5:latest")
	_, err := c.Distill(context.Background(), []string{"chunk"}, nil)
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
	_, err := c.Distill(ctx, []string{"chunk"}, nil)
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
	_, err := c.Distill(context.Background(), []string{"chunk"}, nil)
	if err == nil {
		t.Fatal("expected error for empty response, got nil")
	}
}

// stubDistiller is a Distiller test double driven by a queue of canned
// responses, one per Distill call (matching call order).
type stubDistiller struct {
	avail bool
	calls []func(chunks []string, existing []internal.Fact) ([]Candidate, error)
	n     int
}

func (s *stubDistiller) Available() bool { return s.avail }
func (s *stubDistiller) Distill(_ context.Context, chunks []string, existing []internal.Fact) ([]Candidate, error) {
	f := s.calls[s.n]
	s.n++
	return f(chunks, existing)
}

// fixedDistiller is a concurrency-safe Distiller test double: every call
// gets the same canned response regardless of input or call order. Used by
// concurrency tests where stubDistiller's ordered call queue isn't
// meaningful (workers race to call Distill in unpredictable order).
type fixedDistiller struct {
	avail bool
	fn    func(chunks []string, existing []internal.Fact) ([]Candidate, error)
}

func (f *fixedDistiller) Available() bool { return f.avail }
func (f *fixedDistiller) Distill(_ context.Context, chunks []string, existing []internal.Fact) ([]Candidate, error) {
	return f.fn(chunks, existing)
}

// flakyDistiller fails its first failCount calls (thread-safe via atomic
// counter, so it also works under Config.Concurrency>1), then delegates to
// succeed — used to test Run's retry-on-failure behavior.
type flakyDistiller struct {
	avail     bool
	failCount int64
	calls     int64
	succeed   func(chunks []string, existing []internal.Fact) ([]Candidate, error)
}

func (f *flakyDistiller) Available() bool { return f.avail }
func (f *flakyDistiller) Distill(_ context.Context, chunks []string, existing []internal.Fact) ([]Candidate, error) {
	n := atomic.AddInt64(&f.calls, 1)
	if n <= f.failCount {
		return nil, errors.New("simulated transient failure")
	}
	return f.succeed(chunks, existing)
}

// Package-level counter, safe only because no test in this suite (or this
// codebase generally) uses t.Parallel() — revisit if that ever changes.
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
	cli := &stubDistiller{avail: true, calls: []func([]string, []internal.Fact) ([]Candidate, error){
		func([]string, []internal.Fact) ([]Candidate, error) {
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
	cli := &stubDistiller{avail: true, calls: []func([]string, []internal.Fact) ([]Candidate, error){
		func([]string, []internal.Fact) ([]Candidate, error) {
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
	cli2 := &stubDistiller{avail: true, calls: []func([]string, []internal.Fact) ([]Candidate, error){
		func(_ []string, given []internal.Fact) ([]Candidate, error) {
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
	cli := &stubDistiller{avail: true, calls: []func([]string, []internal.Fact) ([]Candidate, error){
		func([]string, []internal.Fact) ([]Candidate, error) { return nil, nil },
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
	cli := &stubDistiller{avail: true, calls: []func([]string, []internal.Fact) ([]Candidate, error){
		func([]string, []internal.Fact) ([]Candidate, error) {
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

// TestRunRetriesOnFailureThenSucceeds models an Ollama cloud 429: the first
// two Distill calls for a chunk fail, the third (within MaxRetries budget)
// succeeds — the chunk must land as distilled within this same Run, not be
// left for the next one.
func TestRunRetriesOnFailureThenSucceeds(t *testing.T) {
	d := openTestDB(t)
	seedChunk(t, d, "chunk that succeeds on the third attempt", "2026-07-01")
	cli := &flakyDistiller{avail: true, failCount: 2, succeed: func([]string, []internal.Fact) ([]Candidate, error) {
		return []Candidate{{Subject: "s", Predicate: "p", Object: "o", Confidence: 0.9}}, nil
	}}
	res, err := Run(context.Background(), d, cli, Config{Threshold: 0.7, ContextCap: 200, MaxRetries: 2})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Failed != 0 || res.ChunksDistilled != 1 || res.FactsInserted != 1 {
		t.Fatalf("res = %+v, want Failed=0 ChunksDistilled=1 FactsInserted=1", res)
	}
	if got := atomic.LoadInt64(&cli.calls); got != 3 {
		t.Fatalf("Distill called %d times, want 3 (1 initial + 2 retries)", got)
	}
	pending, err := db.ChunksWithoutFacts(d)
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending after eventual success = %+v err=%v, want none", pending, err)
	}
}

// TestRunRetryBudgetExhausted verifies a chunk that fails every attempt,
// including all retries, is still left pending for the next run — retry
// must not turn a permanent failure into a silently dropped chunk.
func TestRunRetryBudgetExhausted(t *testing.T) {
	d := openTestDB(t)
	seedChunk(t, d, "chunk that always fails", "2026-07-01")
	cli := &flakyDistiller{avail: true, failCount: 1000}
	res, err := Run(context.Background(), d, cli, Config{Threshold: 0.7, ContextCap: 200, MaxRetries: 1})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Failed != 1 {
		t.Fatalf("res.Failed = %d, want 1", res.Failed)
	}
	if got := atomic.LoadInt64(&cli.calls); got != 2 {
		t.Fatalf("Distill called %d times, want 2 (1 initial + 1 retry)", got)
	}
	pending, err := db.ChunksWithoutFacts(d)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending after exhausted retries = %+v err=%v, want 1 (retried next run)", pending, err)
	}
}

// TestRunLeavesChunkOnInsertFactError verifies that a DB-level failure
// storing a candidate fact (not a Distill call failure) also leaves the
// chunk unmarked, consistent with the "chunk not marked — retried on the
// next run" contract used for Distill call failures. A transient DB error
// must not permanently drop a fact candidate that was successfully extracted.
func TestRunLeavesChunkOnInsertFactError(t *testing.T) {
	d := openTestDB(t)
	seedChunk(t, d, "chunk whose fact fails to store", "2026-07-01")
	// Drop facts_fts (the trigger target for INSERT INTO facts) so
	// InsertFact fails deterministically inside Run, without touching
	// production code to inject a fault. CurrentFacts (a plain SELECT on
	// facts, called earlier in the loop) is unaffected.
	if _, err := d.Exec(`DROP TABLE facts_fts`); err != nil {
		t.Fatalf("drop facts_fts table: %v", err)
	}
	cli := &stubDistiller{avail: true, calls: []func([]string, []internal.Fact) ([]Candidate, error){
		func([]string, []internal.Fact) ([]Candidate, error) {
			return []Candidate{{Subject: "s", Predicate: "p", Object: "o", Confidence: 0.9}}, nil
		},
	}}
	res, err := Run(context.Background(), d, cli, Config{Threshold: 0.7, ContextCap: 200})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Failed != 1 {
		t.Fatalf("res.Failed = %d, want 1", res.Failed)
	}
	if res.FactsInserted != 0 {
		t.Fatalf("res.FactsInserted = %d, want 0", res.FactsInserted)
	}
	pending, err := db.ChunksWithoutFacts(d)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending after InsertFact failure = %+v err=%v, want 1 (must be retried later, not permanently dropped)", pending, err)
	}
}

// TestRunUsesBackgroundContextForDistillCall verifies that Distill is
// always called with a context independent of Run's outer ctx — distill is
// exempt from the caller's deadline by design; the outer ctx only governs
// cancellation between chunks. A caller passing an already-past-deadline
// (but not yet Err()-returning at the loop-top check) context must not leak
// that deadline into the per-chunk Distill call.
func TestRunUsesBackgroundContextForDistillCall(t *testing.T) {
	d := openTestDB(t)
	seedChunk(t, d, "chunk checking context independence", "2026-07-01")
	var gotCtx context.Context
	cli := &stubDistiller{avail: true, calls: []func([]string, []internal.Fact) ([]Candidate, error){
		func([]string, []internal.Fact) ([]Candidate, error) {
			return nil, nil
		},
	}}
	// Wrap stubDistiller's Distill to capture the ctx it actually receives.
	capturing := &capturingDistiller{inner: cli, captured: &gotCtx}
	if _, err := Run(context.Background(), d, capturing, Config{Threshold: 0.7, ContextCap: 200}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if gotCtx == nil {
		t.Fatal("Distill was never called")
	}
	if gotCtx != context.Background() {
		t.Fatalf("Distill received a ctx other than context.Background(): %v", gotCtx)
	}
}

type capturingDistiller struct {
	inner    Distiller
	captured *context.Context
}

func (c *capturingDistiller) Available() bool { return c.inner.Available() }
func (c *capturingDistiller) Distill(ctx context.Context, chunks []string, existing []internal.Fact) ([]Candidate, error) {
	*c.captured = ctx
	return c.inner.Distill(ctx, chunks, existing)
}

func TestRunRejectsSupersedeIDOutsideContext(t *testing.T) {
	d := openTestDB(t)
	seedChunk(t, d, "chunk citing an id it was never given", "2026-07-01")
	cli := &stubDistiller{avail: true, calls: []func([]string, []internal.Fact) ([]Candidate, error){
		func([]string, []internal.Fact) ([]Candidate, error) {
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

func TestRunRespectsLimit(t *testing.T) {
	d := openTestDB(t)
	seedChunk(t, d, "chunk one", "2026-07-01")
	seedChunk(t, d, "chunk two", "2026-07-02")
	seedChunk(t, d, "chunk three", "2026-07-03")
	cli := &fixedDistiller{avail: true, fn: func([]string, []internal.Fact) ([]Candidate, error) {
		return nil, nil
	}}
	res, err := Run(context.Background(), d, cli, Config{Threshold: 0.7, ContextCap: 200, Limit: 2})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ChunksDistilled != 2 {
		t.Fatalf("res.ChunksDistilled = %d, want 2", res.ChunksDistilled)
	}
	pending, err := db.ChunksWithoutFacts(d)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending after Limit=2 run = %+v err=%v, want 1 left over", pending, err)
	}
}

// TestRunConcurrencyProcessesAllChunks seeds enough chunks that a
// Concurrency>1 run has multiple workers genuinely in flight, and checks
// every chunk is still accounted for exactly once. Run with -race: workers
// share d and cli, so a locking bug here shows up as a race, not just a
// wrong count.
func TestRunConcurrencyProcessesAllChunks(t *testing.T) {
	d := openTestDB(t)
	const n = 20
	for i := 0; i < n; i++ {
		seedChunk(t, d, "concurrent chunk", "2026-07-01")
	}
	var calls int64
	cli := &fixedDistiller{avail: true, fn: func([]string, []internal.Fact) ([]Candidate, error) {
		atomic.AddInt64(&calls, 1)
		return []Candidate{{Subject: "s", Predicate: "p", Object: "o", Confidence: 0.9}}, nil
	}}
	res, err := Run(context.Background(), d, cli, Config{Threshold: 0.7, ContextCap: 200, Concurrency: 5})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ChunksDistilled != n || res.FactsInserted != n {
		t.Fatalf("res = %+v, want ChunksDistilled=%d FactsInserted=%d", res, n, n)
	}
	if atomic.LoadInt64(&calls) != n {
		t.Fatalf("Distill called %d times, want %d", calls, n)
	}
	pending, err := db.ChunksWithoutFacts(d)
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending after full concurrent run = %+v err=%v, want none", pending, err)
	}
}

func TestRunProgressCallback(t *testing.T) {
	d := openTestDB(t)
	const n = 5
	for i := 0; i < n; i++ {
		seedChunk(t, d, "chunk", "2026-07-01")
	}
	cli := &fixedDistiller{avail: true, fn: func([]string, []internal.Fact) ([]Candidate, error) {
		return nil, nil
	}}
	var mu sync.Mutex
	var doneSeen []int
	var lastTotal int
	res, err := Run(context.Background(), d, cli, Config{
		Threshold: 0.7, ContextCap: 200, Concurrency: 3,
		OnProgress: func(done, total int, _ Result) {
			mu.Lock()
			defer mu.Unlock()
			doneSeen = append(doneSeen, done)
			lastTotal = total
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ChunksDistilled != n {
		t.Fatalf("res.ChunksDistilled = %d, want %d", res.ChunksDistilled, n)
	}
	if len(doneSeen) != n {
		t.Fatalf("OnProgress called %d times, want %d", len(doneSeen), n)
	}
	if lastTotal != n {
		t.Fatalf("OnProgress total = %d, want %d", lastTotal, n)
	}
	sort.Ints(doneSeen)
	for i, v := range doneSeen {
		if v != i+1 {
			t.Fatalf("OnProgress done values = %v, want 1..%d each exactly once", doneSeen, n)
		}
	}
}

// TestRunCtxCancellationDuringConcurrentRun cancels ctx while several
// workers are genuinely in flight (Concurrency>1) and asserts Run still
// returns promptly with ctx.Err() — a worker that sees ctx cancelled at
// the top of its jobs loop returns without sending an outcome for that
// chunk, but wg.Wait()+close(outcomes) still fires regardless, so this
// must never hang or leak, only leave the un-dispatched chunks pending
// for the next run. Run with -race: a bug here would show as a hang
// (caught by the timeout below) or a data race on res/doneSeen.
func TestRunCtxCancellationDuringConcurrentRun(t *testing.T) {
	d := openTestDB(t)
	const n = 50
	for i := 0; i < n; i++ {
		seedChunk(t, d, "chunk", "2026-07-01")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cli := &fixedDistiller{avail: true, fn: func([]string, []internal.Fact) ([]Candidate, error) {
		time.Sleep(20 * time.Millisecond)
		return []Candidate{{Subject: "s", Predicate: "p", Object: "o", Confidence: 0.9}}, nil
	}}
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()

	done := make(chan struct{})
	var res Result
	var err error
	go func() {
		res, err = Run(ctx, d, cli, Config{Threshold: 0.7, ContextCap: 200, Concurrency: 5})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return within 5s after ctx cancellation — hang or goroutine leak")
	}
	if err == nil {
		t.Fatalf("Run returned nil error after ctx cancellation, res=%+v", res)
	}

	pending, perr := db.ChunksWithoutFacts(d)
	if perr != nil {
		t.Fatalf("ChunksWithoutFacts: %v", perr)
	}
	if res.ChunksDistilled+len(pending) != n {
		t.Fatalf("res.ChunksDistilled=%d + pending=%d = %d, want %d (every chunk accounted for exactly once)",
			res.ChunksDistilled, len(pending), res.ChunksDistilled+len(pending), n)
	}
}

// TestRunCircuitBreakerStopsEarly models a persistent Ollama outage (quota
// exhausted, endpoint down — not the transient 429 MaxRetries already
// absorbs): every chunk fails every attempt. With Concurrency 1 the run is
// deterministic, so once CircuitBreaker consecutive full-failures accrue,
// Run must stop dispatching immediately rather than grinding through the
// rest of the backlog at MaxRetries cost per chunk.
func TestRunCircuitBreakerStopsEarly(t *testing.T) {
	d := openTestDB(t)
	const n = 20
	for i := 0; i < n; i++ {
		seedChunk(t, d, "chunk that always fails", "2026-07-01")
	}
	cli := &flakyDistiller{avail: true, failCount: 1_000_000}
	res, err := Run(context.Background(), d, cli, Config{
		Threshold: 0.7, ContextCap: 200, MaxRetries: 0, CircuitBreaker: 3,
	})
	if !errors.Is(err, ErrCircuitBreaker) {
		t.Fatalf("err = %v, want ErrCircuitBreaker", err)
	}
	// Exactly 3 in the common case, but the dispatcher can race one more
	// job into the (single, here) worker's receive loop before it observes
	// the cancellation triggered by the 3rd failure — that job is already
	// "in flight" by this package's stop-gracefully contract, so a small
	// overshoot is expected, not a bug. What must never happen is draining
	// anywhere near the full backlog.
	if res.Failed < 3 || res.Failed > 5 {
		t.Fatalf("res.Failed = %d, want 3-5 (stops shortly after the 3rd consecutive failure)", res.Failed)
	}
	if got := atomic.LoadInt64(&cli.calls); got >= n {
		t.Fatalf("Distill called %d times, want well under %d (dispatch must stop, not drain the whole backlog)", got, n)
	}
	pending, err := db.ChunksWithoutFacts(d)
	if err != nil || len(pending) != n {
		t.Fatalf("pending after breaker trip = %+v err=%v, want all %d left for a later run", pending, err, n)
	}
}

// TestRunCircuitBreakerResetsOnSuccess verifies a success anywhere in the
// stream resets the consecutive-failure count — a breaker that counted
// total failures instead of a streak would trip on a backlog that's mostly
// fine but has scattered unrelated failures, which is not the "persistent
// outage" signal it's meant to catch.
func TestRunCircuitBreakerResetsOnSuccess(t *testing.T) {
	d := openTestDB(t)
	// Fail, fail, succeed, fail, fail, succeed, ... — never 3 in a row,
	// even though 8 of 12 chunks fail overall (well past a naive total
	// count of 3).
	const n = 12
	for i := 0; i < n; i++ {
		seedChunk(t, d, "chunk", "2026-07-01")
	}
	var calls int64
	cli := &fixedDistiller{avail: true, fn: func([]string, []internal.Fact) ([]Candidate, error) {
		n := atomic.AddInt64(&calls, 1)
		if n%3 == 0 {
			return []Candidate{{Subject: "s", Predicate: "p", Object: "o", Confidence: 0.9}}, nil
		}
		return nil, errors.New("simulated failure")
	}}
	res, err := Run(context.Background(), d, cli, Config{
		Threshold: 0.7, ContextCap: 200, MaxRetries: 0, CircuitBreaker: 3,
	})
	if err != nil {
		t.Fatalf("Run: %v, want nil (breaker must not trip on a failure streak that never reaches 3)", err)
	}
	if res.ChunksDistilled != 4 || res.Failed != 8 {
		t.Fatalf("res = %+v, want ChunksDistilled=4 Failed=8", res)
	}
	pending, err := db.ChunksWithoutFacts(d)
	if err != nil || len(pending) != n-4 {
		t.Fatalf("pending = %+v err=%v, want %d (only the successes marked)", pending, err, n-4)
	}
}

// TestRunCircuitBreakerDisabledByDefault verifies CircuitBreaker's zero
// value drains the whole backlog regardless of failure streak length,
// preserving pre-circuit-breaker behavior for existing callers.
func TestRunCircuitBreakerDisabledByDefault(t *testing.T) {
	d := openTestDB(t)
	const n = 10
	for i := 0; i < n; i++ {
		seedChunk(t, d, "chunk that always fails", "2026-07-01")
	}
	cli := &flakyDistiller{avail: true, failCount: 1_000_000}
	res, err := Run(context.Background(), d, cli, Config{Threshold: 0.7, ContextCap: 200, MaxRetries: 0})
	if err != nil {
		t.Fatalf("Run: %v, want nil (CircuitBreaker=0 must never trip)", err)
	}
	if res.Failed != n {
		t.Fatalf("res.Failed = %d, want %d (every chunk attempted)", res.Failed, n)
	}
}

// TestBuildPromptSingleChunkMatchesOriginalPrompt pins the single-chunk
// prompt byte-for-byte: batching must not change what a one-chunk call
// sends, so every existing deployment (BatchSize 1) keeps the exact prompt
// its model was tuned on. Any intentional prompt change must update this
// golden string deliberately, not drift into it.
func TestBuildPromptSingleChunkMatchesOriginalPrompt(t *testing.T) {
	got := buildPrompt([]string{"body text"}, []internal.Fact{
		{ID: 7, Subject: "s", Predicate: "p", Object: "o"},
	})
	want := `You extract atomic, durable facts from a single Claude Code conversation
chunk. The chunk may be in English or Ukrainian or both — extract facts in
the language they are stated; do not translate.

Return ONLY JSON: {"facts":[{"subject":"...","predicate":"...","object":"...",
"confidence":0.0,"supersedes_ids":[]}]}

Each fact is a subject-predicate-object triple about the USER, the PROJECT,
its CODE, DECISIONS, or CONVENTIONS. Extract only durable, reusable facts —
not transient chit-chat, not restatements of the assistant's own reasoning.

Confidence: explicitly stated as current truth → 0.9+; clearly implied →
0.5-0.8; speculation/hedged/uncertain → below 0.5. Assign honestly —
low-confidence facts will be discarded.

You are given the project's CURRENT KNOWN FACTS (id + statement). If a fact
you extract makes one of them false or out of date (status change, reversed
decision, corrected value about the SAME subject), put that id in
"supersedes_ids". Only use ids from this list. If nothing is superseded, use
an empty array. Do not supersede a fact that is merely related but still true.

CURRENT KNOWN FACTS:
 - [7] s | p | o

CHUNK:
body text`
	if got != want {
		t.Fatalf("single-chunk prompt drifted from the pre-batching original:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// TestBuildPromptBatchNumbersChunks verifies the multi-chunk prompt carries
// the numbered-chunks layout and chunk_index schema the model needs for
// per-fact attribution, and that an empty facts context renders "(none yet)".
func TestBuildPromptBatchNumbersChunks(t *testing.T) {
	got := buildPrompt([]string{"alpha", "beta"}, nil)
	for _, want := range []string{
		`"chunk_index":1`,
		"CHUNKS:\n[1] alpha\n\n[2] beta",
		"CURRENT KNOWN FACTS:\n(none yet)",
		"numbered Claude Code conversation",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("batch prompt missing %q; prompt:\n%s", want, got)
		}
	}
	if strings.Contains(got, "CHUNK:\n") {
		t.Fatalf("batch prompt must not use the single-chunk CHUNK: section; prompt:\n%s", got)
	}
}

// TestDistillParsesChunkIndex verifies the client surfaces the model's
// self-reported per-fact chunk_index so Run can attribute facts to chunks
// within a batched call.
func TestDistillParsesChunkIndex(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"response":"{\"facts\":[{\"subject\":\"s\",\"predicate\":\"p\",\"object\":\"o\",\"confidence\":0.9,\"chunk_index\":2,\"supersedes_ids\":[]}]}"}`))
	}))
	defer srv.Close()
	c := NewClientURL(srv.URL, "qwen2.5:latest")
	candidates, err := c.Distill(context.Background(), []string{"one", "two"}, nil)
	if err != nil {
		t.Fatalf("Distill: %v", err)
	}
	if len(candidates) != 1 || candidates[0].ChunkIndex != 2 {
		t.Fatalf("candidates = %+v, want one with ChunkIndex=2", candidates)
	}
}

// TestRunBatchesChunksPerCall seeds 5 chunks and runs at BatchSize 4:
// exactly 2 Distill calls (a full batch of 4 plus a partial 1), all 5
// chunks marked, and every fact attributed to the chunk its chunk_index
// names — not lumped onto the first chunk of the batch.
func TestRunBatchesChunksPerCall(t *testing.T) {
	d := openTestDB(t)
	var ids []int64
	for i := 0; i < 5; i++ {
		ids = append(ids, seedChunk(t, d, fmt.Sprintf("chunk %d content", i), "2026-07-01"))
	}
	var calls int64
	cli := &fixedDistiller{avail: true, fn: func(chunks []string, _ []internal.Fact) ([]Candidate, error) {
		atomic.AddInt64(&calls, 1)
		if len(chunks) != 4 && len(chunks) != 1 {
			t.Errorf("Distill call got %d chunks, want 4 (full batch) or 1 (partial last batch)", len(chunks))
		}
		// Subject = the chunk's own content, unique across batches, so
		// attribution can be asserted per chunk even though chunk indexes
		// restart at 1 in each call.
		cands := make([]Candidate, 0, len(chunks))
		for i := range chunks {
			cands = append(cands, Candidate{
				Subject: chunks[i], Predicate: "p", Object: "o",
				Confidence: 0.9, ChunkIndex: i + 1,
			})
		}
		return cands, nil
	}}
	res, err := Run(context.Background(), d, cli, Config{Threshold: 0.7, ContextCap: 200, BatchSize: 4})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := atomic.LoadInt64(&calls); got != 2 {
		t.Fatalf("Distill called %d times, want 2 (one per batch)", got)
	}
	if res.ChunksDistilled != 5 || res.FactsInserted != 5 || res.Failed != 0 {
		t.Fatalf("res = %+v, want ChunksDistilled=5 FactsInserted=5 Failed=0", res)
	}
	pending, err := db.ChunksWithoutFacts(d)
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending after batched run = %+v err=%v, want none (partial batch included)", pending, err)
	}
	// Per-fact attribution: the fact extracted from chunk i (subject = its
	// content) must cite that chunk's id.
	facts, err := db.CurrentFacts(d, 10)
	if err != nil {
		t.Fatalf("CurrentFacts: %v", err)
	}
	seen := map[string]int64{}
	for _, f := range facts {
		seen[f.Subject] = f.SourceChunkID
	}
	for i, id := range ids {
		subj := fmt.Sprintf("chunk %d content", i)
		if seen[subj] != id {
			t.Errorf("fact %q attributed to chunk %d, want %d", subj, seen[subj], id)
		}
	}
}

// TestRunLeavesBatchOnInsertFactError is the batched counterpart of
// TestRunLeavesChunkOnInsertFactError: a DB-level failure storing any
// candidate fact in a batch must leave the WHOLE batch unmarked, so every
// chunk in it is retried on the next run — the batch is the unit of
// marking, and a transient DB error on one candidate must not silently
// mark its (equally unstored) siblings as done.
func TestRunLeavesBatchOnInsertFactError(t *testing.T) {
	d := openTestDB(t)
	for i := 0; i < 4; i++ {
		seedChunk(t, d, "chunk whose fact fails to store", "2026-07-01")
	}
	// Drop facts_fts (the trigger target for INSERT INTO facts) so
	// InsertFact fails deterministically inside Run — same fault-injection
	// trick as TestRunLeavesChunkOnInsertFactError, which explains why it
	// works without touching production code.
	if _, err := d.Exec(`DROP TABLE facts_fts`); err != nil {
		t.Fatalf("drop facts_fts table: %v", err)
	}
	cli := &fixedDistiller{avail: true, fn: func(chunks []string, _ []internal.Fact) ([]Candidate, error) {
		if len(chunks) != 4 {
			t.Errorf("Distill call got %d chunks, want the full batch of 4", len(chunks))
		}
		return []Candidate{{Subject: "s", Predicate: "p", Object: "o", Confidence: 0.9}}, nil
	}}
	res, err := Run(context.Background(), d, cli, Config{Threshold: 0.7, ContextCap: 200, BatchSize: 4})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.FactsInserted != 0 {
		t.Fatalf("res.FactsInserted = %d, want 0", res.FactsInserted)
	}
	if res.Failed < 1 {
		t.Fatalf("res.Failed = %d, want >= 1 (the failed insert must be counted)", res.Failed)
	}
	pending, err := db.ChunksWithoutFacts(d)
	if err != nil || len(pending) != 4 {
		t.Fatalf("pending after InsertFact failure in batch = %+v err=%v, want all 4 (whole batch retried, not just the failing candidate)", pending, err)
	}
}

// TestRunBatchFailsAsUnit verifies a batch whose Distill call fails after
// its retry budget leaves ALL its chunks unmarked (retried on the next run)
// and counts every chunk as failed — batching must not silently drop a
// chunk whose extraction succeeded only in a call that then errored, which
// is why the whole batch is the unit of retry and marking.
func TestRunBatchFailsAsUnit(t *testing.T) {
	d := openTestDB(t)
	for i := 0; i < 4; i++ {
		seedChunk(t, d, "chunk in a failing batch", "2026-07-01")
	}
	var calls int64
	cli := &fixedDistiller{avail: true, fn: func(_ []string, _ []internal.Fact) ([]Candidate, error) {
		atomic.AddInt64(&calls, 1)
		return nil, errors.New("simulated batch failure")
	}}
	res, err := Run(context.Background(), d, cli, Config{Threshold: 0.7, ContextCap: 200, BatchSize: 4, MaxRetries: 1})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Failed != 4 || res.ChunksDistilled != 0 {
		t.Fatalf("res = %+v, want Failed=4 ChunksDistilled=0", res)
	}
	if got := atomic.LoadInt64(&calls); got != 2 {
		t.Fatalf("Distill called %d times, want 2 (1 initial + 1 retry for the whole batch)", got)
	}
	pending, err := db.ChunksWithoutFacts(d)
	if err != nil || len(pending) != 4 {
		t.Fatalf("pending after failed batch = %+v err=%v, want all 4 (batch retried as a unit next run)", pending, err)
	}
}

// TestRunInvalidChunkIndexAttributedToFirstChunk covers the model's
// chunk_index being absent (0) or out of range: the fact is stored anyway,
// attributed to the batch's first chunk, rather than dropped or misindexed.
func TestRunInvalidChunkIndexAttributedToFirstChunk(t *testing.T) {
	d := openTestDB(t)
	firstID := seedChunk(t, d, "first chunk in batch", "2026-07-01")
	seedChunk(t, d, "second chunk in batch", "2026-07-01")
	seedChunk(t, d, "third chunk in batch", "2026-07-01")
	cli := &fixedDistiller{avail: true, fn: func(_ []string, _ []internal.Fact) ([]Candidate, error) {
		return []Candidate{
			{Subject: "absent", Predicate: "p", Object: "o", Confidence: 0.9, ChunkIndex: 0},
			{Subject: "out-of-range", Predicate: "p", Object: "o", Confidence: 0.9, ChunkIndex: 99},
		}, nil
	}}
	res, err := Run(context.Background(), d, cli, Config{Threshold: 0.7, ContextCap: 200, BatchSize: 3})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.FactsInserted != 2 {
		t.Fatalf("res.FactsInserted = %d, want 2 (invalid index must not drop the fact)", res.FactsInserted)
	}
	facts, err := db.CurrentFacts(d, 10)
	if err != nil {
		t.Fatalf("CurrentFacts: %v", err)
	}
	for _, f := range facts {
		if f.SourceChunkID != firstID {
			t.Errorf("fact %q attributed to chunk %d, want first chunk %d", f.Subject, f.SourceChunkID, firstID)
		}
	}
}

// TestRunRespectsContextCap verifies ContextCap plumbing at both edges:
// facts at or under the cap are passed to the distiller as context, and
// more facts than the cap disables the context entirely (the
// contextExceeded path) rather than sending a truncated slice.
func TestRunRespectsContextCap(t *testing.T) {
	d := openTestDB(t)
	// Phase 1: establish 2 live facts via a normal run.
	for i := 0; i < 2; i++ {
		seedChunk(t, d, fmt.Sprintf("fact-bearing chunk %d", i), "2026-07-01")
	}
	store := &fixedDistiller{avail: true, fn: func(chunks []string, _ []internal.Fact) ([]Candidate, error) {
		return []Candidate{{Subject: chunks[0], Predicate: "p", Object: "o", Confidence: 0.9}}, nil
	}}
	if _, err := Run(context.Background(), d, store, Config{Threshold: 0.7, ContextCap: 200}); err != nil {
		t.Fatalf("Run seed: %v", err)
	}

	// Phase 2: a new chunk at ContextCap 2 — the 2 existing facts fit, so
	// the distiller must receive all of them.
	seedChunk(t, d, "chunk at cap", "2026-07-02")
	atCap := &fixedDistiller{avail: true, fn: func(_ []string, given []internal.Fact) ([]Candidate, error) {
		if len(given) != 2 {
			t.Errorf("existing context = %d facts, want 2 (ContextCap 2 with 2 live facts)", len(given))
		}
		return nil, nil
	}}
	if _, err := Run(context.Background(), d, atCap, Config{Threshold: 0.7, ContextCap: 2}); err != nil {
		t.Fatalf("Run at cap: %v", err)
	}

	// Phase 3: ContextCap 1 against 2+ live facts — exceeded, so the
	// distiller must get no context at all (and supersession is skipped).
	seedChunk(t, d, "chunk over cap", "2026-07-02")
	overCap := &fixedDistiller{avail: true, fn: func(_ []string, given []internal.Fact) ([]Candidate, error) {
		if given != nil {
			t.Errorf("existing context = %d facts, want none (ContextCap 1 with 2+ live facts)", len(given))
		}
		return nil, nil
	}}
	if _, err := Run(context.Background(), d, overCap, Config{Threshold: 0.7, ContextCap: 1}); err != nil {
		t.Fatalf("Run over cap: %v", err)
	}
}
