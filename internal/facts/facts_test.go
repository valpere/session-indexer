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
