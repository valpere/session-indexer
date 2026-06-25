package db

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/valpere/session-indexer/internal"
)

func tempDB(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "sessions.db")
}

func TestOpenCreatesSchemaAndVersion(t *testing.T) {
	d, err := Open(tempDB(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()
	var v string
	if err := d.QueryRow(`SELECT value FROM meta WHERE key='schema_version'`).Scan(&v); err != nil {
		t.Fatalf("read version: %v", err)
	}
	if v != SchemaVersion {
		t.Fatalf("version = %q, want %q", v, SchemaVersion)
	}
}

func TestInsertChunkIdempotent(t *testing.T) {
	d, _ := Open(tempDB(t))
	defer d.Close()
	c := internal.Chunk{SessionID: "s1", SessionDate: "2026-06-25", Role: "user",
		MessageIndex: 0, ChunkIndex: 0, Content: "hello world content", CreatedAt: "2026-06-25T10:00:00Z"}
	id1, ins1, err := InsertChunk(d, c)
	if err != nil || !ins1 {
		t.Fatalf("first insert: id=%d ins=%v err=%v", id1, ins1, err)
	}
	_, ins2, err := InsertChunk(d, c)
	if err != nil {
		t.Fatalf("second insert err: %v", err)
	}
	if ins2 {
		t.Fatal("duplicate insert reported inserted=true; want false")
	}
	var n int
	d.QueryRow(`SELECT COUNT(*) FROM chunks`).Scan(&n)
	if n != 1 {
		t.Fatalf("chunk count = %d, want 1", n)
	}
}

func TestOpenVersionMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mismatch.db")
	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open fresh: %v", err)
	}
	if _, err := d.Exec(`UPDATE meta SET value='99' WHERE key='schema_version'`); err != nil {
		t.Fatalf("inject bad version: %v", err)
	}
	d.Close()

	_, err = Open(path)
	if err == nil {
		t.Fatal("expected error on version mismatch, got nil")
	}
	if !strings.Contains(err.Error(), "schema version mismatch") {
		t.Fatalf("error = %q, want 'schema version mismatch'", err.Error())
	}
}

func TestInsertEmbeddingAndPendingList(t *testing.T) {
	d, _ := Open(tempDB(t))
	defer d.Close()
	c := internal.Chunk{SessionID: "s1", SessionDate: "2026-06-25", Role: "user",
		MessageIndex: 0, ChunkIndex: 0, Content: "needs an embedding here", CreatedAt: "2026-06-25T10:00:00Z"}
	id, _, _ := InsertChunk(d, c)
	pending, err := ChunksWithoutEmbeddings(d)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending before = %d err=%v, want 1", len(pending), err)
	}
	if err := InsertEmbedding(d, id, []byte{1, 2, 3, 4}); err != nil {
		t.Fatalf("InsertEmbedding: %v", err)
	}
	pending, _ = ChunksWithoutEmbeddings(d)
	if len(pending) != 0 {
		t.Fatalf("pending after = %d, want 0", len(pending))
	}
}
