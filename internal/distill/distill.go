// Package distill extracts structured subject-predicate-object facts from
// mined chunks via an Ollama chat/generate model, judging supersession
// against a bounded context of currently-valid facts.
package distill

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/valpere/session-indexer/internal"
	"github.com/valpere/session-indexer/internal/db"
)

// ErrCircuitBreaker is returned by Run (wrapped, with the streak length) when
// cfg.CircuitBreaker consecutive chunks each exhaust their retry budget and
// still fail — a persistent condition (Ollama cloud quota/rate exhausted for
// the day, endpoint unreachable) rather than the transient blips MaxRetries
// already absorbs. Callers should treat it as "stop and try again later",
// not a bug: chunks already marked distilled before the trip are durable,
// so the next Run naturally resumes from where this one gave up.
var ErrCircuitBreaker = errors.New("distill: circuit breaker tripped")

// Candidate is one fact proposed by the distiller for a single chunk.
type Candidate struct {
	Subject       string
	Predicate     string
	Object        string
	Confidence    float64
	SupersedesIDs []int64
}

// Distiller extracts fact candidates from a chunk and reports availability.
type Distiller interface {
	Available() bool
	Distill(ctx context.Context, chunk string, existing []internal.Fact) ([]Candidate, error)
}

// Client is an Ollama-backed Distiller.
type Client struct {
	baseURL string
	model   string
	http    *http.Client
}

// NewClient returns a client configured from environment:
//
//	OLLAMA_HOST          — base URL (default: http://localhost:11434);
//	                        if value has no scheme, http:// is prepended
//	OLLAMA_DISTILL_MODEL — chat/generate model (default: glm-5.2:cloud);
//	                        distinct from OLLAMA_MODEL (embeddings) — must
//	                        be pulled separately
func NewClient() *Client {
	return NewClientWithModel("")
}

// NewClientWithModel is NewClient but modelOverride, if non-empty, wins over
// OLLAMA_DISTILL_MODEL and the built-in default — for a CLI --model flag.
func NewClientWithModel(modelOverride string) *Client {
	baseURL := os.Getenv("OLLAMA_HOST")
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	} else if !strings.Contains(baseURL, "://") {
		baseURL = "http://" + baseURL
	}
	model := modelOverride
	if model == "" {
		model = os.Getenv("OLLAMA_DISTILL_MODEL")
	}
	if model == "" {
		model = "glm-5.2:cloud"
	}
	return NewClientURL(baseURL, model)
}

// NewClientURL builds a client against an arbitrary base URL (tests).
func NewClientURL(baseURL, model string) *Client {
	// 120s: generate is slower than embed's 30s — extraction over a full
	// chunk plus supersession judgment is a larger completion than a
	// single embedding vector.
	return &Client{baseURL: baseURL, model: model, http: &http.Client{Timeout: 120 * time.Second}}
}

// Available probes GET /api/tags (2s) and checks the model is present.
func (c *Client) Available() bool {
	ctxClient := &http.Client{Timeout: 2 * time.Second}
	resp, err := ctxClient.Get(c.baseURL + "/api/tags")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if json.NewDecoder(resp.Body).Decode(&tags) != nil {
		return false
	}
	for _, m := range tags.Models {
		if m.Name == c.model {
			return true
		}
	}
	return false
}

// distillResponse is the JSON shape the model is instructed to return.
type distillResponse struct {
	Facts []struct {
		Subject       string  `json:"subject"`
		Predicate     string  `json:"predicate"`
		Object        string  `json:"object"`
		Confidence    float64 `json:"confidence"`
		SupersedesIDs []int64 `json:"supersedes_ids"`
	} `json:"facts"`
}

// Distill returns fact candidates extracted from chunk via POST /api/generate.
// The request is cancelled when ctx is done. Single attempt, no retry — a
// failed chunk is left un-distilled for the next run, matching embed's
// documented no-retry convention.
func (c *Client) Distill(ctx context.Context, chunk string, existing []internal.Fact) ([]Candidate, error) {
	prompt := buildPrompt(chunk, existing)
	body, _ := json.Marshal(map[string]any{
		"model":  c.model,
		"prompt": prompt,
		"stream": false,
		"format": "json",
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ollama distill: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama distill: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return nil, fmt.Errorf("ollama distill: status %d: %s",
			resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	var out struct {
		Response string `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode distill response: %w", err)
	}
	if strings.TrimSpace(out.Response) == "" {
		return nil, fmt.Errorf("ollama returned an empty response")
	}
	var parsed distillResponse
	if err := json.Unmarshal([]byte(stripMarkdownFence(out.Response)), &parsed); err != nil {
		return nil, fmt.Errorf("decode facts json: %w", err)
	}
	candidates := make([]Candidate, 0, len(parsed.Facts))
	for _, f := range parsed.Facts {
		candidates = append(candidates, Candidate{
			Subject:       f.Subject,
			Predicate:     f.Predicate,
			Object:        f.Object,
			Confidence:    f.Confidence,
			SupersedesIDs: f.SupersedesIDs,
		})
	}
	return candidates, nil
}

// stripMarkdownFence removes a wrapping ```json ... ``` (or bare ``` ... ```)
// fence some models emit despite "format":"json" — observed with
// gemma4:31b-cloud; glm-5.2:cloud does not do this. A no-op on plain JSON.
func stripMarkdownFence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	s = strings.TrimPrefix(s, "```")
	if nl := strings.IndexByte(s, '\n'); nl != -1 && strings.TrimSpace(s[:nl]) != "" {
		// Fence's opening line is a language tag (e.g. "json") — drop it.
		s = s[nl+1:]
	}
	s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	return strings.TrimSpace(s)
}

const promptTemplate = `You extract atomic, durable facts from a single Claude Code conversation
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
%s

CHUNK:
%s`

// buildPrompt renders promptTemplate. Kept as plain string formatting, not
// text/template — this codebase has no templating framework and one call
// site doesn't justify adding one.
func buildPrompt(chunk string, existing []internal.Fact) string {
	var factsBlock strings.Builder
	if len(existing) == 0 {
		factsBlock.WriteString("(none yet)")
	} else {
		for _, f := range existing {
			fmt.Fprintf(&factsBlock, " - [%d] %s | %s | %s\n", f.ID, f.Subject, f.Predicate, f.Object)
		}
	}
	return fmt.Sprintf(promptTemplate, strings.TrimRight(factsBlock.String(), "\n"), chunk)
}

// Config controls a distill Run.
type Config struct {
	Threshold   float64 // minimum confidence to store a fact (default 0.7)
	ContextCap  int     // max current facts to feed the distiller for supersession judgment (default 200)
	Concurrency int     // chunks distilled in parallel; <=1 runs strictly sequential (default)
	Limit       int     // max chunks to process this run; <=0 means no limit (process all pending)

	// MaxRetries is the number of extra Distill attempts for a chunk whose
	// first call errors (e.g. HTTP 429 from Ollama cloud under concurrent
	// load, or a transient timeout), before giving up and leaving the
	// chunk for the next run. 0 (default) preserves the original
	// single-attempt behavior. Each retry waits retryBackoff(attempt).
	MaxRetries int

	// CircuitBreaker stops Run early — no further chunks dispatched,
	// already-in-flight ones allowed to finish — after this many
	// consecutive chunks each exhaust MaxRetries and still fail. 0
	// (default) disables it: Run always drains every pending chunk
	// regardless of failure streaks, matching the original behavior.
	CircuitBreaker int

	// OnProgress, if set, is called synchronously after each chunk is
	// resolved (success or failure), always from Run's own single
	// outcome-processing goroutine (never from a worker) — cheap and
	// non-blocking work only (e.g. a log line).
	OnProgress func(done, total int, res Result)
}

// retryBackoff is the delay before retry attempt n (0-indexed, so n=0 is the
// wait before the first retry): ~500ms, 1s, 2s, 4s, capped at 8s, each
// jittered +/-25%. Ollama cloud's 429 responses carry no Retry-After header
// to key off instead. The jitter matters under concurrency: a fixed
// schedule means every worker that got 429'd in the same instant retries in
// the same instant too, reproducing the same burst that caused the 429 in
// the first place (observed empirically — retries alone, un-jittered, still
// left ~20% of a concurrency=20 run failed).
func retryBackoff(attempt int) time.Duration {
	const maxDelay = 8 * time.Second
	// 500ms*2^4 already reaches maxDelay, so clamp attempt before the
	// shift — 1<<attempt otherwise overflows int64 around attempt~=35
	// (reachable via --retries with a large value), producing a negative
	// d that made rand.Int64N(int64(d)/2) panic.
	if attempt > 4 {
		attempt = 4
	}
	d := 500 * time.Millisecond * (1 << attempt)
	if d > maxDelay {
		d = maxDelay
	}
	jitter := time.Duration(rand.Int64N(int64(d)/2)) - d/4 // +/-25%
	return d + jitter
}

// Result summarizes a distill run.
type Result struct {
	ChunksDistilled int
	FactsInserted   int
	BelowThreshold  int
	Superseded      int
	Failed          int
}

// fetchOutcome is one chunk's Distill call result, produced by a worker and
// consumed by Run's single DB-writing loop.
type fetchOutcome struct {
	chunk           db.PendingFactChunk
	candidates      []Candidate
	err             error
	promptContext   []internal.Fact
	contextExceeded bool
}

// Run distills every chunk pending in d via cli, applying cfg's confidence
// gate deterministically in Go (the model's self-reported confidence is
// advisory input to this hard check, not the enforcement mechanism).
//
// Uses context.Background() internally per pending chunk's Distill call —
// distill is a manual, non-time-boxed command, explicitly exempt from
// mine's 50s/Stop-hook budget. The passed ctx still governs overall
// cancellation (e.g. Ctrl-C) between chunks.
//
// cfg.Concurrency workers fetch existing-facts context and call cli.Distill
// (the network-bound step) in parallel; every DB write (InsertFact,
// SupersedeFact, MarkChunkDistilled) happens sequentially in this function's
// own goroutine as outcomes arrive, so SQLite never sees concurrent writers.
// A side effect: "current facts" context fed to concurrently-in-flight
// chunks may be a few facts stale relative to strictly sequential Run — the
// same tradeoff any batched/parallel supersession pipeline makes.
//
// A chunk whose Distill call errors is retried up to cfg.MaxRetries times
// with retryBackoff between attempts, still within this same call — Ollama
// cloud returns a plain 429 (no Retry-After) once concurrent in-flight
// requests exceed roughly 20, and without this a high-concurrency run loses
// a large fraction of its chunks to rate limiting instead of just slowing
// down. A chunk still failing after the retry budget is left unmarked, same
// as before — picked up on the next Run.
//
// If cfg.CircuitBreaker consecutive chunks each exhaust their retry budget,
// Run stops dispatching further chunks (already-in-flight ones are allowed
// to finish, same "graceful, not abrupt" treatment as ctx cancellation) and
// returns ErrCircuitBreaker. External cancellation of ctx itself (e.g. the
// caller wiring SIGINT/SIGTERM via signal.NotifyContext) is handled the same
// way and returns ctx.Err(). Either way, every chunk marked distilled before
// the stop is durable — nothing to "resume" beyond calling Run again later;
// ChunksWithoutFacts will simply pick up where this call left off.
func Run(ctx context.Context, d *sql.DB, cli Distiller, cfg Config) (Result, error) {
	var res Result
	pending, err := db.ChunksWithoutFacts(d)
	if err != nil {
		return res, err
	}
	if cfg.Limit > 0 && len(pending) > cfg.Limit {
		pending = pending[:cfg.Limit]
	}
	total := len(pending)

	concurrency := cfg.Concurrency
	if concurrency < 1 {
		concurrency = 1
	}

	// runCtx is what dispatch/workers actually watch for "stop now": it's
	// cancelled either by the caller (ctx) or by us, internally, when the
	// circuit breaker trips. Distinguishing the two causes at the end is
	// what lets Run report ErrCircuitBreaker instead of a bare ctx.Err()
	// for a self-inflicted stop.
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()

	jobs := make(chan db.PendingFactChunk)
	outcomes := make(chan fetchOutcome)

	var wg sync.WaitGroup
	wg.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		go func() {
			defer wg.Done()
			for p := range jobs {
				if runCtx.Err() != nil {
					return
				}
				existing, err := db.CurrentFacts(d, cfg.ContextCap+1)
				if err != nil {
					outcomes <- fetchOutcome{chunk: p, err: err}
					continue
				}
				// ContextCap exceeded: omit context and skip
				// auto-supersession for this call — an oversized context
				// would blow the prompt budget and the model has nothing
				// reliable to judge supersession against.
				contextExceeded := len(existing) > cfg.ContextCap
				promptContext := existing
				if contextExceeded {
					promptContext = nil
				}
				// context.Background(), not runCtx: distill is exempt from
				// the caller's deadline (see doc comment above) — runCtx
				// only governs cancellation between chunks/retries, an
				// in-flight Ollama call is always let finish.
				var candidates []Candidate
				var derr error
				for attempt := 0; ; attempt++ {
					candidates, derr = cli.Distill(context.Background(), p.Content, promptContext)
					if derr == nil || attempt >= cfg.MaxRetries {
						break
					}
					select {
					case <-time.After(retryBackoff(attempt)):
					case <-runCtx.Done():
					}
					if runCtx.Err() != nil {
						break
					}
				}
				outcomes <- fetchOutcome{
					chunk: p, candidates: candidates, err: derr,
					promptContext: promptContext, contextExceeded: contextExceeded,
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, p := range pending {
			select {
			case jobs <- p:
			case <-runCtx.Done():
				return
			}
		}
	}()
	go func() {
		wg.Wait()
		close(outcomes)
	}()

	done := 0
	var consecutiveFailures int
	var breakerTripped bool
	for oc := range outcomes {
		done++
		if oc.err != nil {
			res.Failed++ // chunk not marked — retried on the next run
			if cfg.CircuitBreaker > 0 {
				consecutiveFailures++
				if consecutiveFailures >= cfg.CircuitBreaker && !breakerTripped {
					breakerTripped = true
					cancelRun()
				}
			}
			if cfg.OnProgress != nil {
				cfg.OnProgress(done, total, res)
			}
			continue
		}
		consecutiveFailures = 0
		res.ChunksDistilled++

		allowedIDs := make(map[int64]bool, len(oc.promptContext))
		for _, f := range oc.promptContext {
			allowedIDs[f.ID] = true
		}

		now := time.Now().UTC().Format(time.RFC3339)
		// storeFailed tracks whether any candidate for this chunk hit a DB
		// error (InsertFact/SupersedeFact) rather than a confidence-gate
		// discard. On a store failure the chunk is left unmarked so it's
		// retried on the next run, same as a Distill call failure above —
		// a transient DB error must not permanently drop a candidate fact.
		var storeFailed bool
		for _, c := range oc.candidates {
			if c.Confidence < cfg.Threshold {
				res.BelowThreshold++
				continue
			}
			newID, err := db.InsertFact(d, internal.Fact{
				Subject:       c.Subject,
				Predicate:     c.Predicate,
				Object:        c.Object,
				Confidence:    c.Confidence,
				SourceChunkID: oc.chunk.ID,
				SessionDate:   oc.chunk.SessionDate,
				CreatedAt:     now,
			})
			if err != nil {
				res.Failed++
				storeFailed = true
				continue
			}
			res.FactsInserted++
			if oc.contextExceeded {
				continue
			}
			for _, oldID := range c.SupersedesIDs {
				// Safeguard: the model may only cite fact ids from the
				// context it was actually given.
				if !allowedIDs[oldID] {
					continue
				}
				changed, err := db.SupersedeFact(d, newID, oldID, now)
				if err != nil {
					res.Failed++
					storeFailed = true
					continue
				}
				if changed {
					res.Superseded++
				}
			}
		}
		if !storeFailed {
			// A MarkChunkDistilled error here is a DB-level failure, not a
			// content problem — the chunk is retried next run just like the
			// other failure paths above, rather than aborting the fan-out.
			if err := db.MarkChunkDistilled(d, oc.chunk.ID, now); err != nil {
				res.Failed++
			}
		}
		if cfg.OnProgress != nil {
			cfg.OnProgress(done, total, res)
		}
	}
	if breakerTripped {
		return res, fmt.Errorf("%w after %d consecutive chunk failures", ErrCircuitBreaker, cfg.CircuitBreaker)
	}
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	return res, nil
}
