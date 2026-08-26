// Package browse provides read-only enumeration over stored chunks —
// list/show without a query, as distinct from internal/search's ranking.
// Deliberately its own package: internal/search's doc comment scopes it to
// ranked retrieval, and browse.ListChunks/GetChunk return internal.ChunkSummary
// (has an ID, no Score) rather than internal.SearchResult (has a Score,
// meaningless when nothing was ranked).
//
// session_date, not session_id, is the default rollup axis: `--resume` keeps
// one session_id alive for months, so grouping by session_id in practice
// produces a few huge buckets and many single-digit ones, while session_date
// gives evenly sized buckets that match how people recall work. No index
// exists on session_date by design — a full scan over the expected corpus
// size (thousands of chunks) is single-digit milliseconds, and adding one
// would force a schema bump (and the re-mine that comes with it) for no
// measurable gain.
package browse

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/valpere/session-indexer/internal"
)

// ListOpts filters ListChunks. Zero-value string fields mean "no filter";
// Limit is applied as-is (callers, e.g. a Cobra flag, own the default).
type ListOpts struct {
	Limit   int
	Since   string // YYYY-MM-DD inclusive; "" = unbounded
	Until   string // YYYY-MM-DD inclusive; "" = unbounded
	Role    string // "user" | "assistant"; "" = both
	Session string // exact session_id; "" = all
}

// dateLayout is the stored format for chunks.session_date.
const dateLayout = "2006-01-02"

func validateDate(field, s string) error {
	if s == "" {
		return nil
	}
	if _, err := time.Parse(dateLayout, s); err != nil {
		return fmt.Errorf("invalid %s %q: want YYYY-MM-DD", field, s)
	}
	return nil
}

// ListChunks returns chunks newest-first (by session_date, then id — id
// alone is insufficient: re-mining an older JSONL later inserts rows whose
// id is high but whose session_date is old, and date order is what a
// browse view should honor).
func ListChunks(d *sql.DB, o ListOpts) ([]internal.ChunkSummary, error) {
	if err := validateDate("since", o.Since); err != nil {
		return nil, err
	}
	if err := validateDate("until", o.Until); err != nil {
		return nil, err
	}
	q := `SELECT c.id, c.session_id, c.session_date, c.role, c.content,
	             e.chunk_id IS NOT NULL, dc.chunk_id IS NOT NULL
	      FROM chunks c
	      LEFT JOIN embeddings e ON e.chunk_id = c.id
	      LEFT JOIN distilled_chunks dc ON dc.chunk_id = c.id
	      WHERE 1=1`
	var args []any
	if o.Since != "" {
		q += ` AND c.session_date >= ?`
		args = append(args, o.Since)
	}
	if o.Until != "" {
		q += ` AND c.session_date <= ?`
		args = append(args, o.Until)
	}
	if o.Role != "" {
		q += ` AND c.role = ?`
		args = append(args, o.Role)
	}
	if o.Session != "" {
		q += ` AND c.session_id = ?`
		args = append(args, o.Session)
	}
	q += ` ORDER BY c.session_date DESC, c.id DESC LIMIT ?`
	args = append(args, o.Limit)

	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("list chunks: %w", err)
	}
	defer rows.Close()

	var out []internal.ChunkSummary
	for rows.Next() {
		var c internal.ChunkSummary
		if err := rows.Scan(&c.ID, &c.SessionID, &c.SessionDate, &c.Role, &c.Content,
			&c.HasEmbedding, &c.Distilled); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetChunk returns one chunk by id, or a wrapped sql.ErrNoRows when absent.
func GetChunk(d *sql.DB, id int64) (internal.ChunkSummary, error) {
	var c internal.ChunkSummary
	err := d.QueryRow(
		`SELECT c.id, c.session_id, c.session_date, c.role, c.content,
		        e.chunk_id IS NOT NULL, dc.chunk_id IS NOT NULL
		 FROM chunks c
		 LEFT JOIN embeddings e ON e.chunk_id = c.id
		 LEFT JOIN distilled_chunks dc ON dc.chunk_id = c.id
		 WHERE c.id = ?`, id).
		Scan(&c.ID, &c.SessionID, &c.SessionDate, &c.Role, &c.Content,
			&c.HasEmbedding, &c.Distilled)
	if err != nil {
		return internal.ChunkSummary{}, fmt.Errorf("chunk %d: %w", id, err)
	}
	return c, nil
}

// Days rolls chunks up by session_date, newest first — the default view
// for `sessions` (see package doc for why session_date, not session_id).
func Days(d *sql.DB) ([]internal.DaySummary, error) {
	rows, err := d.Query(
		`SELECT c.session_date, COUNT(*), COUNT(e.chunk_id), COUNT(dc.chunk_id),
		        COUNT(DISTINCT c.session_id)
		 FROM chunks c
		 LEFT JOIN embeddings e ON e.chunk_id = c.id
		 LEFT JOIN distilled_chunks dc ON dc.chunk_id = c.id
		 GROUP BY c.session_date
		 ORDER BY c.session_date DESC`)
	if err != nil {
		return nil, fmt.Errorf("days rollup: %w", err)
	}
	defer rows.Close()

	var out []internal.DaySummary
	for rows.Next() {
		var s internal.DaySummary
		if err := rows.Scan(&s.SessionDate, &s.Chunks, &s.Embedded, &s.Distilled, &s.Sessions); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Sessions rolls chunks up by session_id, most-recently-active first — the
// `--by-session` view (see package doc: skewed on purpose, to make the
// --resume-collapses-months behavior visible rather than surprising).
func Sessions(d *sql.DB) ([]internal.SessionSummary, error) {
	rows, err := d.Query(
		`SELECT c.session_id, COUNT(*), COUNT(e.chunk_id), COUNT(dc.chunk_id),
		        MIN(c.session_date), MAX(c.session_date)
		 FROM chunks c
		 LEFT JOIN embeddings e ON e.chunk_id = c.id
		 LEFT JOIN distilled_chunks dc ON dc.chunk_id = c.id
		 GROUP BY c.session_id
		 ORDER BY MAX(c.session_date) DESC, COUNT(*) DESC`)
	if err != nil {
		return nil, fmt.Errorf("sessions rollup: %w", err)
	}
	defer rows.Close()

	var out []internal.SessionSummary
	for rows.Next() {
		var s internal.SessionSummary
		if err := rows.Scan(&s.SessionID, &s.Chunks, &s.Embedded, &s.Distilled,
			&s.FirstDate, &s.LastDate); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
