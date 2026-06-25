package mine

import (
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
}

// Run parses jsonlPath, stores chunks in dbPath, and embeds new chunks
// when emb is available. Idempotent: re-mining the same file inserts nothing.
func Run(dbPath, jsonlPath string, emb embed.Embedder) (Result, error) {
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

	available := emb.Available()
	for _, c := range chunks {
		id, inserted, err := db.InsertChunk(d, c)
		if err != nil {
			return res, err
		}
		if !inserted {
			continue
		}
		res.ChunksInserted++
		if !available {
			continue
		}
		vec, err := emb.Embed(c.Content)
		if err != nil {
			res.Skipped++
			continue // never abort the mine on an embed failure
		}
		if err := db.InsertEmbedding(d, id, embed.EncodeVector(vec)); err != nil {
			res.Skipped++
		} else {
			res.Embedded++
		}
	}
	return res, nil
}
