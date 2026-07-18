// Package facts provides read verbs over the distilled facts layer: keyword
// search, single-fact lookup with supersedes edges, and depth-1 related
// facts. Pure functions over *sql.DB — no shared framework exists in this
// codebase, so none is invented here.
package facts

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/valpere/session-indexer/internal"
)

// Search returns up to limit facts matching query via FTS5 BM25 over
// facts_fts. Tombstoned facts are excluded unless includeExpired is true.
func Search(d *sql.DB, query string, limit int, includeExpired bool) ([]internal.Fact, error) {
	match := ftsMatchExpr(query)
	if match == "" {
		return nil, nil // no usable terms; caller prints "(no results)"
	}
	q := `SELECT f.id, f.subject, f.predicate, f.object, f.confidence,
	             f.source_chunk_id, f.session_date, f.created_at, f.until, f.superseded_by
	      FROM facts f JOIN facts_fts ON f.id = facts_fts.rowid
	      WHERE facts_fts MATCH ?`
	if !includeExpired {
		q += ` AND f.until IS NULL`
	}
	q += ` ORDER BY bm25(facts_fts) LIMIT ?`
	rows, err := d.Query(q, match, limit)
	if err != nil {
		return nil, fmt.Errorf("facts fts query: %w", err)
	}
	defer rows.Close()
	return scanFacts(rows)
}

// Get returns fact id along with its supersedes edges: outgoing is what
// this fact points to (superseded_by), incoming is the set of older facts
// whose superseded_by references id.
func Get(d *sql.DB, id int64) (fact internal.Fact, incoming, outgoing []internal.Fact, err error) {
	row := d.QueryRow(
		`SELECT id, subject, predicate, object, confidence,
		        source_chunk_id, session_date, created_at, until, superseded_by
		 FROM facts WHERE id=?`, id)
	fact, err = scanFactRow(row)
	if err != nil {
		return internal.Fact{}, nil, nil, err
	}

	incRows, err := d.Query(
		`SELECT id, subject, predicate, object, confidence,
		        source_chunk_id, session_date, created_at, until, superseded_by
		 FROM facts WHERE superseded_by=?`, id)
	if err != nil {
		return internal.Fact{}, nil, nil, fmt.Errorf("query incoming: %w", err)
	}
	defer incRows.Close()
	incoming, err = scanFacts(incRows)
	if err != nil {
		return internal.Fact{}, nil, nil, err
	}

	if fact.SupersededBy != nil {
		outRows, err := d.Query(
			`SELECT id, subject, predicate, object, confidence,
			        source_chunk_id, session_date, created_at, until, superseded_by
			 FROM facts WHERE id=?`, *fact.SupersededBy)
		if err != nil {
			return internal.Fact{}, nil, nil, fmt.Errorf("query outgoing: %w", err)
		}
		defer outRows.Close()
		outgoing, err = scanFacts(outRows)
		if err != nil {
			return internal.Fact{}, nil, nil, err
		}
	}
	return fact, incoming, outgoing, nil
}

// Related returns the depth-1 union of id's incoming and outgoing supersedes
// neighbors. No BFS/depth parameter in v1 — YAGNI until a real need surfaces.
func Related(d *sql.DB, id int64) ([]internal.Fact, error) {
	_, incoming, outgoing, err := Get(d, id)
	if err != nil {
		return nil, err
	}
	return append(incoming, outgoing...), nil
}

// ftsMatchExpr builds an OR of quoted terms from the query for keyword
// recall — the same per-term-OR approach as internal/search.ftsMatchExpr
// (unexported there; reimplemented here rather than shared, matching this
// codebase's no-shared-framework convention).
func ftsMatchExpr(query string) string {
	var terms []string
	for _, w := range strings.Fields(query) {
		terms = append(terms, `"`+strings.ReplaceAll(w, `"`, `""`)+`"`)
	}
	return strings.Join(terms, " OR ")
}

// scanFactRow scans a single *sql.Row into an internal.Fact.
func scanFactRow(row *sql.Row) (internal.Fact, error) {
	var f internal.Fact
	var sourceChunkID sql.NullInt64
	var until sql.NullString
	var supersededBy sql.NullInt64
	err := row.Scan(&f.ID, &f.Subject, &f.Predicate, &f.Object, &f.Confidence,
		&sourceChunkID, &f.SessionDate, &f.CreatedAt, &until, &supersededBy)
	if err != nil {
		return internal.Fact{}, fmt.Errorf("scan fact: %w", err)
	}
	applyNullable(&f, sourceChunkID, until, supersededBy)
	return f, nil
}

// scanFacts drains rows into []internal.Fact. Callers own closing rows.
func scanFacts(rows *sql.Rows) ([]internal.Fact, error) {
	var out []internal.Fact
	for rows.Next() {
		var f internal.Fact
		var sourceChunkID sql.NullInt64
		var until sql.NullString
		var supersededBy sql.NullInt64
		if err := rows.Scan(&f.ID, &f.Subject, &f.Predicate, &f.Object, &f.Confidence,
			&sourceChunkID, &f.SessionDate, &f.CreatedAt, &until, &supersededBy); err != nil {
			return nil, err
		}
		applyNullable(&f, sourceChunkID, until, supersededBy)
		out = append(out, f)
	}
	return out, rows.Err()
}

func applyNullable(f *internal.Fact, sourceChunkID sql.NullInt64, until sql.NullString, supersededBy sql.NullInt64) {
	if sourceChunkID.Valid {
		f.SourceChunkID = sourceChunkID.Int64
	}
	if until.Valid {
		u := until.String
		f.Until = &u
	}
	if supersededBy.Valid {
		s := supersededBy.Int64
		f.SupersededBy = &s
	}
}
