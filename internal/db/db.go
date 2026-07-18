// Package db owns the SQLite store: open, schema versioning, inserts.
package db

import (
	"database/sql"
	_ "embed"
	"fmt"

	"github.com/valpere/session-indexer/internal"
	_ "modernc.org/sqlite"
)

// SchemaVersion is the schema the binary expects. Bump on any DDL change.
const SchemaVersion = "2"

//go:embed schema.sql
var schemaSQL string

// Open returns a ready DB: WAL, NORMAL sync, 5s busy timeout, FK on.
// Creates the schema on a fresh DB; errors on a version mismatch.
func Open(path string) (*sql.DB, error) {
	dsn := "file:" + path +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)"
	d, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	var v string
	err = d.QueryRow(`SELECT value FROM meta WHERE key='schema_version'`).Scan(&v)
	switch {
	case err == sql.ErrNoRows:
		// meta exists but unversioned — treat as corrupt/old.
		d.Close()
		return nil, fmt.Errorf("schema version missing: delete %s and re-mine to rebuild", path)
	case err != nil:
		// meta table absent → fresh DB; create schema.
		if _, execErr := d.Exec(schemaSQL); execErr != nil {
			d.Close()
			return nil, fmt.Errorf("create schema: %w", execErr)
		}
		return d, nil
	case v != SchemaVersion:
		d.Close()
		return nil, fmt.Errorf("schema version mismatch (%s != %s): delete %s and re-mine to rebuild", v, SchemaVersion, path)
	default:
		return d, nil
	}
}

// InsertChunk inserts c, ignoring duplicates on the dedup index.
// inserted is false when the row already existed.
func InsertChunk(d *sql.DB, c internal.Chunk) (id int64, inserted bool, err error) {
	res, err := d.Exec(
		`INSERT OR IGNORE INTO chunks
		 (session_id, session_date, role, message_index, chunk_index, content, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		c.SessionID, c.SessionDate, c.Role, c.MessageIndex, c.ChunkIndex, c.Content, c.CreatedAt)
	if err != nil {
		return 0, false, fmt.Errorf("insert chunk: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return 0, false, nil // duplicate ignored
	}
	id, _ = res.LastInsertId()
	return id, true, nil
}

// InsertEmbedding stores the float32 BLOB for a chunk (idempotent).
func InsertEmbedding(d *sql.DB, chunkID int64, vector []byte) error {
	_, err := d.Exec(
		`INSERT OR REPLACE INTO embeddings(chunk_id, vector) VALUES (?, ?)`,
		chunkID, vector)
	if err != nil {
		return fmt.Errorf("insert embedding: %w", err)
	}
	return nil
}

// PendingChunk is a chunk lacking an embedding.
type PendingChunk struct {
	ID      int64
	Content string
}

// ChunksWithoutEmbeddings lists chunks with no embeddings row.
func ChunksWithoutEmbeddings(d *sql.DB) ([]PendingChunk, error) {
	rows, err := d.Query(
		`SELECT c.id, c.content FROM chunks c
		 LEFT JOIN embeddings e ON e.chunk_id = c.id
		 WHERE e.chunk_id IS NULL`)
	if err != nil {
		return nil, fmt.Errorf("query pending: %w", err)
	}
	defer rows.Close()
	var out []PendingChunk
	for rows.Next() {
		var p PendingChunk
		if err := rows.Scan(&p.ID, &p.Content); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// InsertFact inserts f. No dedup index — distillation isn't deterministic;
// dedup/supersession is the distill LLM's job, not a unique constraint.
// A zero SourceChunkID is stored as NULL (no source chunk), not as a
// dangling foreign key to chunk id 0.
func InsertFact(d *sql.DB, f internal.Fact) (id int64, err error) {
	var sourceChunkID any
	if f.SourceChunkID != 0 {
		sourceChunkID = f.SourceChunkID
	}
	res, err := d.Exec(
		`INSERT INTO facts
		 (subject, predicate, object, confidence, source_chunk_id, session_date, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		f.Subject, f.Predicate, f.Object, f.Confidence, sourceChunkID, f.SessionDate, f.CreatedAt)
	if err != nil {
		return 0, fmt.Errorf("insert fact: %w", err)
	}
	id, _ = res.LastInsertId()
	return id, nil
}

// MarkChunkDistilled records that chunkID has been through distill, even if
// it yielded zero facts — otherwise a zero-fact chunk would be re-distilled
// on every run.
func MarkChunkDistilled(d *sql.DB, chunkID int64, at string) error {
	_, err := d.Exec(
		`INSERT OR REPLACE INTO distilled_chunks(chunk_id, distilled_at) VALUES (?, ?)`,
		chunkID, at)
	if err != nil {
		return fmt.Errorf("mark chunk distilled: %w", err)
	}
	return nil
}

// PendingFactChunk is a chunk that has not yet been through distill.
type PendingFactChunk struct {
	ID          int64
	Content     string
	SessionDate string
}

// ChunksWithoutFacts lists chunks with no distilled_chunks row — the
// distill analogue of ChunksWithoutEmbeddings. Decoupled from "has a facts
// row" because a chunk legitimately yields zero facts.
func ChunksWithoutFacts(d *sql.DB) ([]PendingFactChunk, error) {
	rows, err := d.Query(
		`SELECT c.id, c.content, c.session_date FROM chunks c
		 LEFT JOIN distilled_chunks dc ON dc.chunk_id = c.id
		 WHERE dc.chunk_id IS NULL`)
	if err != nil {
		return nil, fmt.Errorf("query pending facts: %w", err)
	}
	defer rows.Close()
	var out []PendingFactChunk
	for rows.Next() {
		var p PendingFactChunk
		if err := rows.Scan(&p.ID, &p.Content, &p.SessionDate); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// CurrentFacts returns up to limit non-tombstoned facts, most recent first —
// the bounded context fed to the distiller for supersession judgment.
func CurrentFacts(d *sql.DB, limit int) ([]internal.Fact, error) {
	rows, err := d.Query(
		`SELECT id, subject, predicate, object, confidence, source_chunk_id,
		        session_date, created_at, until, superseded_by
		 FROM facts WHERE until IS NULL ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("query current facts: %w", err)
	}
	defer rows.Close()
	return scanFacts(rows)
}

// SupersedeFact tombstones oldID in favor of newID, stamping until. Returns
// false (no error) if oldID was already tombstoned — the call is a no-op,
// not a failure. Shared by both auto-supersession (distill) and the manual
// `facts supersede` command.
func SupersedeFact(d *sql.DB, newID, oldID int64, until string) (bool, error) {
	res, err := d.Exec(
		`UPDATE facts SET until=?, superseded_by=? WHERE id=? AND until IS NULL`,
		until, newID, oldID)
	if err != nil {
		return false, fmt.Errorf("supersede fact: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
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
		out = append(out, f)
	}
	return out, rows.Err()
}
