package facts

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/valpere/session-indexer/internal"
	"github.com/valpere/session-indexer/internal/db"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func insertFact(t *testing.T, d *sql.DB, subject, predicate, object string) int64 {
	t.Helper()
	id, err := db.InsertFact(d, internal.Fact{
		Subject: subject, Predicate: predicate, Object: object,
		Confidence: 0.9, SessionDate: "2026-07-01", CreatedAt: "2026-07-01T10:00:00Z",
	})
	if err != nil {
		t.Fatalf("InsertFact: %v", err)
	}
	return id
}

// insertFactFull is insertFact with control over date/confidence/source
// chunk, for List/BySourceChunk tests that need to vary those fields.
func insertFactFull(t *testing.T, d *sql.DB, subject, predicate, object, date string, confidence float64, sourceChunkID int64) int64 {
	t.Helper()
	id, err := db.InsertFact(d, internal.Fact{
		Subject: subject, Predicate: predicate, Object: object,
		Confidence: confidence, SourceChunkID: sourceChunkID,
		SessionDate: date, CreatedAt: date + "T10:00:00Z",
	})
	if err != nil {
		t.Fatalf("InsertFact: %v", err)
	}
	return id
}

func TestSearchEmptyStore(t *testing.T) {
	d := openTestDB(t)
	res, err := Search(d, "anything", 5, false)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) != 0 {
		t.Fatalf("results = %+v, want none on empty store", res)
	}
}

func TestSearchExcludesExpiredByDefault(t *testing.T) {
	d := openTestDB(t)
	oldID := insertFact(t, d, "project", "status", "not started")
	newID := insertFact(t, d, "project", "status", "in progress")
	if _, err := db.SupersedeFact(d, newID, oldID, "2026-07-02T10:00:00Z"); err != nil {
		t.Fatalf("SupersedeFact: %v", err)
	}
	res, err := Search(d, "status", 5, false)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	for _, f := range res {
		if f.ID == oldID {
			t.Fatalf("expired fact %d present in default search results: %+v", oldID, res)
		}
	}
	found := false
	for _, f := range res {
		if f.ID == newID {
			found = true
		}
	}
	if !found {
		t.Fatalf("current fact %d missing from search results: %+v", newID, res)
	}
}

func TestSearchIncludeExpired(t *testing.T) {
	d := openTestDB(t)
	oldID := insertFact(t, d, "project", "status", "not started")
	newID := insertFact(t, d, "project", "status", "in progress")
	if _, err := db.SupersedeFact(d, newID, oldID, "2026-07-02T10:00:00Z"); err != nil {
		t.Fatalf("SupersedeFact: %v", err)
	}
	res, err := Search(d, "status", 5, true)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	found := false
	for _, f := range res {
		if f.ID == oldID {
			found = true
		}
	}
	if !found {
		t.Fatalf("expired fact %d missing with includeExpired=true: %+v", oldID, res)
	}
}

func TestGetReturnsSupersedesEdges(t *testing.T) {
	d := openTestDB(t)
	oldID := insertFact(t, d, "project", "status", "not started")
	newID := insertFact(t, d, "project", "status", "in progress")
	if _, err := db.SupersedeFact(d, newID, oldID, "2026-07-02T10:00:00Z"); err != nil {
		t.Fatalf("SupersedeFact: %v", err)
	}

	oldFact, oldIncoming, oldOutgoing, err := Get(d, oldID)
	if err != nil {
		t.Fatalf("Get(old): %v", err)
	}
	if oldFact.Until == nil {
		t.Fatal("old fact Until is nil, want a tombstone timestamp")
	}
	if len(oldIncoming) != 0 {
		t.Fatalf("old fact incoming = %+v, want none", oldIncoming)
	}
	if len(oldOutgoing) != 1 || oldOutgoing[0].ID != newID {
		t.Fatalf("old fact outgoing = %+v, want [newID=%d]", oldOutgoing, newID)
	}

	newFact, newIncoming, newOutgoing, err := Get(d, newID)
	if err != nil {
		t.Fatalf("Get(new): %v", err)
	}
	if newFact.Until != nil {
		t.Fatalf("new fact Until = %v, want nil (currently valid)", *newFact.Until)
	}
	if len(newIncoming) != 1 || newIncoming[0].ID != oldID {
		t.Fatalf("new fact incoming = %+v, want [oldID=%d]", newIncoming, oldID)
	}
	if len(newOutgoing) != 0 {
		t.Fatalf("new fact outgoing = %+v, want none", newOutgoing)
	}
}

func TestRelatedDepth1(t *testing.T) {
	d := openTestDB(t)
	oldID := insertFact(t, d, "project", "status", "not started")
	newID := insertFact(t, d, "project", "status", "in progress")
	if _, err := db.SupersedeFact(d, newID, oldID, "2026-07-02T10:00:00Z"); err != nil {
		t.Fatalf("SupersedeFact: %v", err)
	}
	related, err := Related(d, newID)
	if err != nil {
		t.Fatalf("Related: %v", err)
	}
	if len(related) != 1 || related[0].ID != oldID {
		t.Fatalf("Related(new) = %+v, want [oldID=%d]", related, oldID)
	}
}

func TestListEmptyStore(t *testing.T) {
	d := openTestDB(t)
	res, err := List(d, ListOpts{Limit: 10})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(res) != 0 {
		t.Fatalf("results = %+v, want none on empty store", res)
	}
}

func TestListExcludesExpiredByDefault(t *testing.T) {
	d := openTestDB(t)
	oldID := insertFactFull(t, d, "project", "status", "not started", "2026-07-01", 0.9, 0)
	newID := insertFactFull(t, d, "project", "status", "in progress", "2026-07-02", 0.9, 0)
	if _, err := db.SupersedeFact(d, newID, oldID, "2026-07-03T10:00:00Z"); err != nil {
		t.Fatalf("SupersedeFact: %v", err)
	}

	res, err := List(d, ListOpts{Limit: 10})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(res) != 1 || res[0].ID != newID {
		t.Fatalf("List = %+v, want only [newID=%d]", res, newID)
	}

	withExpired, err := List(d, ListOpts{Limit: 10, IncludeExpired: true})
	if err != nil {
		t.Fatalf("List(IncludeExpired): %v", err)
	}
	if len(withExpired) != 2 {
		t.Fatalf("List(IncludeExpired) = %+v, want both facts", withExpired)
	}
}

func TestListFilters(t *testing.T) {
	d := openTestDB(t)
	insertFactFull(t, d, "a", "is", "x", "2026-07-01", 0.5, 0)
	insertFactFull(t, d, "b", "is", "y", "2026-07-10", 0.95, 0)
	insertFactFull(t, d, "c", "is", "z", "2026-07-20", 0.7, 0)

	cases := []struct {
		name string
		opts ListOpts
		want int
	}{
		{"since", ListOpts{Limit: 10, Since: "2026-07-05"}, 2},
		{"until", ListOpts{Limit: 10, Until: "2026-07-05"}, 1},
		{"min-confidence", ListOpts{Limit: 10, MinConfidence: 0.9}, 1},
		{"limit", ListOpts{Limit: 1}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := List(d, tc.opts)
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(res) != tc.want {
				t.Fatalf("got %d results, want %d", len(res), tc.want)
			}
		})
	}
}

func TestListNegativeLimit(t *testing.T) {
	d := openTestDB(t)
	insertFactFull(t, d, "a", "is", "x", "2026-07-01", 0.9, 0)
	if _, err := List(d, ListOpts{Limit: -1}); err == nil {
		t.Fatal("List: expected error for negative limit (SQLite treats it as unbounded)")
	}
}

func TestBySourceChunk(t *testing.T) {
	d := openTestDB(t)
	chunkID, inserted, err := db.InsertChunk(d, internal.Chunk{
		SessionID: "s1", SessionDate: "2026-07-01", Role: "assistant",
		MessageIndex: 0, ChunkIndex: 0, Content: "some content",
		CreatedAt: "2026-07-01T10:00:00Z",
	})
	if err != nil || !inserted {
		t.Fatalf("InsertChunk: inserted=%v err=%v", inserted, err)
	}
	otherChunkID, _, err := db.InsertChunk(d, internal.Chunk{
		SessionID: "s1", SessionDate: "2026-07-01", Role: "assistant",
		MessageIndex: 1, ChunkIndex: 0, Content: "other content",
		CreatedAt: "2026-07-01T10:00:00Z",
	})
	if err != nil {
		t.Fatalf("InsertChunk (other): %v", err)
	}

	f1 := insertFactFull(t, d, "a", "is", "x", "2026-07-01", 0.9, chunkID)
	f2 := insertFactFull(t, d, "a", "is", "y", "2026-07-01", 0.9, chunkID)
	insertFactFull(t, d, "b", "is", "z", "2026-07-01", 0.9, otherChunkID)

	res, err := BySourceChunk(d, chunkID)
	if err != nil {
		t.Fatalf("BySourceChunk: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("BySourceChunk(%d) = %+v, want 2 facts", chunkID, res)
	}
	ids := map[int64]bool{res[0].ID: true, res[1].ID: true}
	if !ids[f1] || !ids[f2] {
		t.Fatalf("BySourceChunk(%d) = %+v, want [%d, %d]", chunkID, res, f1, f2)
	}
}

func TestBySourceChunkIncludesTombstoned(t *testing.T) {
	d := openTestDB(t)
	chunkID, _, err := db.InsertChunk(d, internal.Chunk{
		SessionID: "s1", SessionDate: "2026-07-01", Role: "assistant",
		MessageIndex: 0, ChunkIndex: 0, Content: "content",
		CreatedAt: "2026-07-01T10:00:00Z",
	})
	if err != nil {
		t.Fatalf("InsertChunk: %v", err)
	}
	oldID := insertFactFull(t, d, "project", "status", "not started", "2026-07-01", 0.9, chunkID)
	newID := insertFactFull(t, d, "project", "status", "in progress", "2026-07-02", 0.9, chunkID)
	if _, err := db.SupersedeFact(d, newID, oldID, "2026-07-03T10:00:00Z"); err != nil {
		t.Fatalf("SupersedeFact: %v", err)
	}

	res, err := BySourceChunk(d, chunkID)
	if err != nil {
		t.Fatalf("BySourceChunk: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("BySourceChunk = %+v, want both facts (including tombstoned)", res)
	}
}
