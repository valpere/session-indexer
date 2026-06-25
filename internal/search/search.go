// Package search ranks stored chunks by cosine (embedding-first) with an
// FTS5 BM25 fallback when Ollama is unavailable.
package search

import (
	"database/sql"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/valpere/session-indexer/internal"
	"github.com/valpere/session-indexer/internal/embed"
)

// Search returns up to limit ranked results. usedEmbeddings is false when
// the FTS5 fallback was used.
func Search(d *sql.DB, emb embed.Embedder, query string, limit int) ([]internal.SearchResult, bool, error) {
	if emb.Available() {
		res, err := cosineSearch(d, emb, query, limit)
		return res, true, err
	}
	res, err := ftsSearch(d, query, limit)
	return res, false, err
}

func cosineSearch(d *sql.DB, emb embed.Embedder, query string, limit int) ([]internal.SearchResult, error) {
	qv, err := emb.Embed(query)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	rows, err := d.Query(`SELECT chunk_id, vector FROM embeddings`)
	if err != nil {
		return nil, fmt.Errorf("load embeddings: %w", err)
	}
	defer rows.Close()
	type scored struct {
		id    int64
		score float64
	}
	var all []scored
	for rows.Next() {
		var id int64
		var blob []byte
		if err := rows.Scan(&id, &blob); err != nil {
			return nil, err
		}
		all = append(all, scored{id, cosine(qv, embed.DecodeVector(blob))})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(all, func(i, j int) bool { return all[i].score > all[j].score })
	if len(all) > limit {
		all = all[:limit]
	}
	var out []internal.SearchResult
	for _, s := range all {
		var r internal.SearchResult
		err := d.QueryRow(`SELECT session_date, role, content FROM chunks WHERE id=?`, s.id).
			Scan(&r.SessionDate, &r.Role, &r.Content)
		if err != nil {
			return nil, err
		}
		r.Score = s.score
		out = append(out, r)
	}
	return out, nil
}

func ftsSearch(d *sql.DB, query string, limit int) ([]internal.SearchResult, error) {
	match := `"` + strings.ReplaceAll(query, `"`, `""`) + `"`
	rows, err := d.Query(
		`SELECT c.session_date, c.role, c.content, bm25(chunks_fts) AS rank
		 FROM chunks c JOIN chunks_fts ON c.id = chunks_fts.rowid
		 WHERE chunks_fts MATCH ?
		 ORDER BY rank LIMIT ?`, match, limit)
	if err != nil {
		return nil, fmt.Errorf("fts query: %w", err)
	}
	defer rows.Close()
	var out []internal.SearchResult
	for rows.Next() {
		var r internal.SearchResult
		var rank float64
		if err := rows.Scan(&r.SessionDate, &r.Role, &r.Content, &rank); err != nil {
			return nil, err
		}
		r.Score = -rank // bm25 lower is better; negate so higher=better
		out = append(out, r)
	}
	return out, rows.Err()
}

func cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// Stats describes index state for the stats subcommand.
type Stats struct {
	Sessions int
	Chunks   int
	Embedded int
	Pending  int
	Oldest   string
	Newest   string
}

// GetStats reports index counts and date range.
func GetStats(d *sql.DB) (Stats, error) {
	var s Stats
	q := func(query string, dest any) error { return d.QueryRow(query).Scan(dest) }
	if err := q(`SELECT COUNT(DISTINCT session_id) FROM chunks`, &s.Sessions); err != nil {
		return s, err
	}
	if err := q(`SELECT COUNT(*) FROM chunks`, &s.Chunks); err != nil {
		return s, err
	}
	if err := q(`SELECT COUNT(*) FROM embeddings`, &s.Embedded); err != nil {
		return s, err
	}
	s.Pending = s.Chunks - s.Embedded
	// Date range is best-effort; empty store leaves these blank.
	var oldest, newest sql.NullString
	_ = d.QueryRow(`SELECT MIN(session_date), MAX(session_date) FROM chunks`).Scan(&oldest, &newest)
	if oldest.Valid {
		s.Oldest = oldest.String
	}
	if newest.Valid {
		s.Newest = newest.String
	}
	return s, nil
}
