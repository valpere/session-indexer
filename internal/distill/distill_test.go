package distill

import (
	"context"
	"database/sql"
	"errors"
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
	candidates, err := c.Distill(context.Background(), "chunk text", nil)
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

// fixedDistiller is a concurrency-safe Distiller test double: every call
// gets the same canned response regardless of input or call order. Used by
// concurrency tests where stubDistiller's ordered call queue isn't
// meaningful (workers race to call Distill in unpredictable order).
type fixedDistiller struct {
	avail bool
	fn    func(chunk string, existing []internal.Fact) ([]Candidate, error)
}

func (f *fixedDistiller) Available() bool { return f.avail }
func (f *fixedDistiller) Distill(_ context.Context, chunk string, existing []internal.Fact) ([]Candidate, error) {
	return f.fn(chunk, existing)
}

// flakyDistiller fails its first failCount calls (thread-safe via atomic
// counter, so it also works under Config.Concurrency>1), then delegates to
// succeed — used to test Run's retry-on-failure behavior.
type flakyDistiller struct {
	avail     bool
	failCount int64
	calls     int64
	succeed   func(chunk string, existing []internal.Fact) ([]Candidate, error)
}

func (f *flakyDistiller) Available() bool { return f.avail }
func (f *flakyDistiller) Distill(_ context.Context, chunk string, existing []internal.Fact) ([]Candidate, error) {
	n := atomic.AddInt64(&f.calls, 1)
	if n <= f.failCount {
		return nil, errors.New("simulated transient failure")
	}
	return f.succeed(chunk, existing)
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

// TestRunRetriesOnFailureThenSucceeds models an Ollama cloud 429: the first
// two Distill calls for a chunk fail, the third (within MaxRetries budget)
// succeeds — the chunk must land as distilled within this same Run, not be
// left for the next one.
func TestRunRetriesOnFailureThenSucceeds(t *testing.T) {
	d := openTestDB(t)
	seedChunk(t, d, "chunk that succeeds on the third attempt", "2026-07-01")
	cli := &flakyDistiller{avail: true, failCount: 2, succeed: func(string, []internal.Fact) ([]Candidate, error) {
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
	cli := &stubDistiller{avail: true, calls: []func(string, []internal.Fact) ([]Candidate, error){
		func(string, []internal.Fact) ([]Candidate, error) {
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
	cli := &stubDistiller{avail: true, calls: []func(string, []internal.Fact) ([]Candidate, error){
		func(string, []internal.Fact) ([]Candidate, error) {
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
func (c *capturingDistiller) Distill(ctx context.Context, chunk string, existing []internal.Fact) ([]Candidate, error) {
	*c.captured = ctx
	return c.inner.Distill(ctx, chunk, existing)
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

func TestRunRespectsLimit(t *testing.T) {
	d := openTestDB(t)
	seedChunk(t, d, "chunk one", "2026-07-01")
	seedChunk(t, d, "chunk two", "2026-07-02")
	seedChunk(t, d, "chunk three", "2026-07-03")
	cli := &fixedDistiller{avail: true, fn: func(string, []internal.Fact) ([]Candidate, error) {
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
	cli := &fixedDistiller{avail: true, fn: func(string, []internal.Fact) ([]Candidate, error) {
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
	cli := &fixedDistiller{avail: true, fn: func(string, []internal.Fact) ([]Candidate, error) {
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
	cli := &fixedDistiller{avail: true, fn: func(string, []internal.Fact) ([]Candidate, error) {
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
