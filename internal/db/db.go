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
const SchemaVersion = "1"

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
		return nil, fmt.Errorf("schema version missing: run: session-indexer reindex")
	case err != nil:
		// meta table absent → fresh DB; create schema.
		if _, execErr := d.Exec(schemaSQL); execErr != nil {
			d.Close()
			return nil, fmt.Errorf("create schema: %w", execErr)
		}
		return d, nil
	case v != SchemaVersion:
		d.Close()
		return nil, fmt.Errorf("schema version mismatch (%s != %s): run: session-indexer reindex", v, SchemaVersion)
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
