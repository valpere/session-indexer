// Package distill extracts structured subject-predicate-object facts from
// mined chunks via an Ollama chat/generate model, judging supersession
// against a bounded context of currently-valid facts.
package distill

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/valpere/session-indexer/internal"
	"github.com/valpere/session-indexer/internal/db"
)

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
//	OLLAMA_DISTILL_MODEL — chat/generate model (default: qwen2.5:latest);
//	                        distinct from OLLAMA_MODEL (embeddings) — must
//	                        be pulled separately
func NewClient() *Client {
	baseURL := os.Getenv("OLLAMA_HOST")
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	} else if !strings.Contains(baseURL, "://") {
		baseURL = "http://" + baseURL
	}
	model := os.Getenv("OLLAMA_DISTILL_MODEL")
	if model == "" {
		model = "qwen2.5:latest"
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
	if err := json.Unmarshal([]byte(out.Response), &parsed); err != nil {
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
	Threshold  float64 // minimum confidence to store a fact (default 0.7)
	ContextCap int     // max current facts to feed the distiller for supersession judgment (default 200)
}

// Result summarizes a distill run.
type Result struct {
	ChunksDistilled int
	FactsInserted   int
	BelowThreshold  int
	Superseded      int
	Failed          int
}

// Run distills every chunk pending in d via cli, applying cfg's confidence
// gate deterministically in Go (the model's self-reported confidence is
// advisory input to this hard check, not the enforcement mechanism).
//
// Uses context.Background() internally per pending chunk's Distill call —
// distill is a manual, non-time-boxed command, explicitly exempt from
// mine's 50s/Stop-hook budget. The passed ctx still governs overall
// cancellation (e.g. Ctrl-C) between chunks.
func Run(ctx context.Context, d *sql.DB, cli Distiller, cfg Config) (Result, error) {
	var res Result
	pending, err := db.ChunksWithoutFacts(d)
	if err != nil {
		return res, err
	}
	for _, p := range pending {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		existing, err := db.CurrentFacts(d, cfg.ContextCap+1)
		if err != nil {
			return res, err
		}
		// ContextCap exceeded: omit context and skip auto-supersession for
		// this call — an oversized context would blow the prompt budget
		// and the model has nothing reliable to judge supersession against.
		contextExceeded := len(existing) > cfg.ContextCap
		promptContext := existing
		if contextExceeded {
			promptContext = nil
		}
		allowedIDs := make(map[int64]bool, len(promptContext))
		for _, f := range promptContext {
			allowedIDs[f.ID] = true
		}

		candidates, err := cli.Distill(ctx, p.Content, promptContext)
		if err != nil {
			res.Failed++
			continue // chunk not marked — retried on the next run
		}
		res.ChunksDistilled++

		now := time.Now().UTC().Format(time.RFC3339)
		for _, c := range candidates {
			if c.Confidence < cfg.Threshold {
				res.BelowThreshold++
				continue
			}
			newID, err := db.InsertFact(d, internal.Fact{
				Subject:       c.Subject,
				Predicate:     c.Predicate,
				Object:        c.Object,
				Confidence:    c.Confidence,
				SourceChunkID: p.ID,
				SessionDate:   p.SessionDate,
				CreatedAt:     now,
			})
			if err != nil {
				res.Failed++
				continue
			}
			res.FactsInserted++
			if contextExceeded {
				continue
			}
			for _, oldID := range c.SupersedesIDs {
				// Safeguard: the model may only cite fact ids from the
				// context it was actually given.
				if !allowedIDs[oldID] {
					continue
				}
				changed, err := db.SupersedeFact(d, newID, oldID, now)
				if err == nil && changed {
					res.Superseded++
				}
			}
		}
		if err := db.MarkChunkDistilled(d, p.ID, now); err != nil {
			return res, err
		}
	}
	return res, nil
}
