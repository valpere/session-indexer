package mine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/valpere/session-indexer/internal/db"
	"github.com/valpere/session-indexer/internal/embed"
)

// Result summarizes a mine run.
type Result struct {
	ChunksInserted int
	Embedded       int
	Skipped        int
	Deferred       int // inserted but not embedded — ctx deadline hit; backfill via `embed`
}

// Run parses jsonlPath, stores chunks in dbPath, and embeds new chunks when
// emb is available. Idempotent: re-mining the same file inserts nothing.
//
// Storing is split from embedding so a ctx deadline never loses a chunk:
// phase 1 inserts every chunk (fast), phase 2 embeds the newly-inserted ones
// and defers the rest when ctx is done. Deferred chunks have no embedding row
// and are backfilled by the `embed` subcommand once Ollama is reachable.
func Run(ctx context.Context, dbPath, jsonlPath string, emb embed.Embedder) (Result, error) {
	var res Result
	f, err := os.Open(jsonlPath)
	if err != nil {
		return res, fmt.Errorf("open jsonl: %w", err)
	}
	defer f.Close()

	stem := strings.TrimSuffix(filepath.Base(jsonlPath), filepath.Ext(jsonlPath))
	msgs, err := ParseJSONL(f, stem)
	if err != nil {
		return res, err
	}
	chunks := ChunkMessages(msgs)

	d, err := db.Open(dbPath)
	if err != nil {
		return res, err
	}
	defer d.Close()

	// Phase 1: store all chunks (fast, idempotent). Collect new ones to embed.
	available := emb.Available()
	type toEmbed struct {
		id      int64
		content string
	}
	var pending []toEmbed
	for _, c := range chunks {
		id, inserted, err := db.InsertChunk(d, c)
		if err != nil {
			return res, err
		}
		if !inserted {
			continue
		}
		res.ChunksInserted++
		if available {
			pending = append(pending, toEmbed{id, c.Content})
		}
	}

	// Phase 2: embed new chunks, respecting the ctx deadline. Once ctx is done,
	// remaining chunks are deferred (not embedded) — never abort the mine.
	for _, p := range pending {
		if ctx.Err() != nil {
			res.Deferred++
			continue
		}
		vec, err := emb.Embed(p.content)
		if err != nil {
			res.Skipped++
			continue
		}
		if err := db.InsertEmbedding(d, p.id, embed.EncodeVector(vec)); err != nil {
			res.Skipped++
		} else {
			res.Embedded++
		}
	}
	return res, nil
}
