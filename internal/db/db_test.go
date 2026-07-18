package db

import (
	"database/sql"
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

func TestChunksWithoutEmbeddingsEmptyDB(t *testing.T) {
	d, _ := Open(tempDB(t))
	defer d.Close()
	pending, err := ChunksWithoutEmbeddings(d)
	if err != nil {
		t.Fatalf("ChunksWithoutEmbeddings on empty DB: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("empty DB has %d pending chunks, want 0", len(pending))
	}
}

func TestInsertEmbeddingReplacement(t *testing.T) {
	// INSERT OR REPLACE must overwrite the previous embedding, not duplicate it.
	d, _ := Open(tempDB(t))
	defer d.Close()
	c := internal.Chunk{SessionID: "s1", SessionDate: "2026-06-25", Role: "user",
		MessageIndex: 0, ChunkIndex: 0, Content: "test chunk content here", CreatedAt: "2026-06-25T10:00:00Z"}
	id, _, _ := InsertChunk(d, c)
	if err := InsertEmbedding(d, id, []byte{1, 0, 0, 0}); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if err := InsertEmbedding(d, id, []byte{2, 0, 0, 0}); err != nil {
		t.Fatalf("second insert (replace): %v", err)
	}
	var count int
	d.QueryRow(`SELECT COUNT(*) FROM embeddings WHERE chunk_id=?`, id).Scan(&count)
	if count != 1 {
		t.Fatalf("embedding count = %d, want 1 (INSERT OR REPLACE)", count)
	}
	var blob []byte
	d.QueryRow(`SELECT vector FROM embeddings WHERE chunk_id=?`, id).Scan(&blob)
	if len(blob) == 0 || blob[0] != 2 {
		t.Fatalf("stored vector first byte = %v, want 2 (latest value)", blob)
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

func TestInsertFactAndCurrentFacts(t *testing.T) {
	d, _ := Open(tempDB(t))
	defer d.Close()
	id, err := InsertFact(d, internal.Fact{
		Subject: "session-indexer", Predicate: "has", Object: "33 merged PRs",
		Confidence: 0.9, SessionDate: "2026-07-01", CreatedAt: "2026-07-01T10:00:00Z",
	})
	if err != nil {
		t.Fatalf("InsertFact: %v", err)
	}
	if id == 0 {
		t.Fatal("InsertFact returned id 0")
	}
	current, err := CurrentFacts(d, 10)
	if err != nil {
		t.Fatalf("CurrentFacts: %v", err)
	}
	if len(current) != 1 || current[0].ID != id {
		t.Fatalf("CurrentFacts = %+v, want one fact with id %d", current, id)
	}
	if current[0].Until != nil {
		t.Fatalf("Until = %v, want nil (currently valid)", *current[0].Until)
	}
}

func TestChunksWithoutFactsExcludesDistilled(t *testing.T) {
	d, _ := Open(tempDB(t))
	defer d.Close()
	c := internal.Chunk{SessionID: "s1", SessionDate: "2026-07-01", Role: "user",
		MessageIndex: 0, ChunkIndex: 0, Content: "a chunk with nothing extractable", CreatedAt: "2026-07-01T10:00:00Z"}
	id, _, _ := InsertChunk(d, c)
	pending, err := ChunksWithoutFacts(d)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending before = %+v err=%v, want 1", pending, err)
	}
	// Mark distilled with zero facts produced — must still drop from pending.
	if err := MarkChunkDistilled(d, id, "2026-07-01T10:05:00Z"); err != nil {
		t.Fatalf("MarkChunkDistilled: %v", err)
	}
	pending, err = ChunksWithoutFacts(d)
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending after = %+v err=%v, want none (zero-fact chunk must not stay pending)", pending, err)
	}
}

func TestSupersedeFactStampsUntilAndEdge(t *testing.T) {
	d, _ := Open(tempDB(t))
	defer d.Close()
	oldID, _ := InsertFact(d, internal.Fact{Subject: "project", Predicate: "status", Object: "not started",
		Confidence: 0.9, SessionDate: "2026-07-01", CreatedAt: "2026-07-01T10:00:00Z"})
	newID, _ := InsertFact(d, internal.Fact{Subject: "project", Predicate: "status", Object: "in progress",
		Confidence: 0.9, SessionDate: "2026-07-02", CreatedAt: "2026-07-02T10:00:00Z"})
	changed, err := SupersedeFact(d, newID, oldID, "2026-07-02T10:00:00Z")
	if err != nil {
		t.Fatalf("SupersedeFact: %v", err)
	}
	if !changed {
		t.Fatal("SupersedeFact reported changed=false, want true")
	}
	var until sql.NullString
	var supersededBy sql.NullInt64
	if err := d.QueryRow(`SELECT until, superseded_by FROM facts WHERE id=?`, oldID).
		Scan(&until, &supersededBy); err != nil {
		t.Fatalf("read supersede state: %v", err)
	}
	if !until.Valid || until.String != "2026-07-02T10:00:00Z" {
		t.Fatalf("until = %+v, want stamped timestamp", until)
	}
	if !supersededBy.Valid || supersededBy.Int64 != newID {
		t.Fatalf("superseded_by = %+v, want %d", supersededBy, newID)
	}
}

func TestSupersedeFactNoOpWhenAlreadyTombstoned(t *testing.T) {
	d, _ := Open(tempDB(t))
	defer d.Close()
	oldID, _ := InsertFact(d, internal.Fact{Subject: "s", Predicate: "p", Object: "o1",
		Confidence: 0.9, SessionDate: "2026-07-01", CreatedAt: "2026-07-01T10:00:00Z"})
	newID, _ := InsertFact(d, internal.Fact{Subject: "s", Predicate: "p", Object: "o2",
		Confidence: 0.9, SessionDate: "2026-07-02", CreatedAt: "2026-07-02T10:00:00Z"})
	otherID, _ := InsertFact(d, internal.Fact{Subject: "s", Predicate: "p", Object: "o3",
		Confidence: 0.9, SessionDate: "2026-07-03", CreatedAt: "2026-07-03T10:00:00Z"})

	if changed, err := SupersedeFact(d, newID, oldID, "2026-07-02T10:00:00Z"); err != nil || !changed {
		t.Fatalf("first supersede: changed=%v err=%v, want true/nil", changed, err)
	}
	// Attempt to re-tombstone the same fact via a different superseder — no-op.
	changed, err := SupersedeFact(d, otherID, oldID, "2026-07-03T10:00:00Z")
	if err != nil {
		t.Fatalf("second supersede: %v", err)
	}
	if changed {
		t.Fatal("SupersedeFact on an already-tombstoned fact reported changed=true, want false (no-op)")
	}
	var supersededBy sql.NullInt64
	if err := d.QueryRow(`SELECT superseded_by FROM facts WHERE id=?`, oldID).Scan(&supersededBy); err != nil {
		t.Fatalf("read superseded_by: %v", err)
	}
	if !supersededBy.Valid || supersededBy.Int64 != newID {
		t.Fatalf("superseded_by = %+v, want unchanged at %d (first supersede wins)", supersededBy, newID)
	}
}
