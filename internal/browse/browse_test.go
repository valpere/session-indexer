package browse

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

func insertChunk(t *testing.T, d *sql.DB, sessionID, date, role string, msgIdx int) int64 {
	t.Helper()
	id, inserted, err := db.InsertChunk(d, internal.Chunk{
		SessionID: sessionID, SessionDate: date, Role: role,
		MessageIndex: msgIdx, ChunkIndex: 0, Content: "content " + date,
		CreatedAt: date + "T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("InsertChunk: %v", err)
	}
	if !inserted {
		t.Fatalf("InsertChunk: expected a new row for %s/%d", sessionID, msgIdx)
	}
	return id
}

func TestListChunksEmptyStore(t *testing.T) {
	d := openTestDB(t)
	res, err := ListChunks(d, ListOpts{Limit: 10})
	if err != nil {
		t.Fatalf("ListChunks: %v", err)
	}
	if len(res) != 0 {
		t.Fatalf("results = %+v, want none on empty store", res)
	}
}

func TestListChunksOrdersByDateDescThenID(t *testing.T) {
	d := openTestDB(t)
	// Insert an "old" chunk (low date) AFTER a "new" one (high id, old date) —
	// mirrors re-mining an older JSONL later. id order must not win over date.
	insertChunk(t, d, "s1", "2026-08-20", "user", 0)
	oldButLastInserted := insertChunk(t, d, "s1", "2026-06-01", "user", 1)
	newest := insertChunk(t, d, "s1", "2026-08-25", "user", 2)

	res, err := ListChunks(d, ListOpts{Limit: 10})
	if err != nil {
		t.Fatalf("ListChunks: %v", err)
	}
	if len(res) != 3 {
		t.Fatalf("got %d results, want 3", len(res))
	}
	if res[0].ID != newest {
		t.Fatalf("first result id = %d, want newest %d (date order, not id order)", res[0].ID, newest)
	}
	if res[len(res)-1].ID != oldButLastInserted {
		t.Fatalf("last result id = %d, want oldest-dated %d despite being inserted last", res[len(res)-1].ID, oldButLastInserted)
	}
}

func TestListChunksFilters(t *testing.T) {
	d := openTestDB(t)
	insertChunk(t, d, "s1", "2026-08-01", "user", 0)
	insertChunk(t, d, "s1", "2026-08-10", "assistant", 1)
	insertChunk(t, d, "s2", "2026-08-20", "user", 0)

	cases := []struct {
		name string
		opts ListOpts
		want int
	}{
		{"since", ListOpts{Limit: 10, Since: "2026-08-05"}, 2},
		{"until", ListOpts{Limit: 10, Until: "2026-08-05"}, 1},
		{"role", ListOpts{Limit: 10, Role: "assistant"}, 1},
		{"session", ListOpts{Limit: 10, Session: "s2"}, 1},
		{"limit", ListOpts{Limit: 1}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := ListChunks(d, tc.opts)
			if err != nil {
				t.Fatalf("ListChunks: %v", err)
			}
			if len(res) != tc.want {
				t.Fatalf("got %d results, want %d", len(res), tc.want)
			}
		})
	}
}

func TestListChunksNegativeLimit(t *testing.T) {
	d := openTestDB(t)
	insertChunk(t, d, "s1", "2026-08-01", "user", 0)
	if _, err := ListChunks(d, ListOpts{Limit: -1}); err == nil {
		t.Fatal("ListChunks: expected error for negative limit (SQLite treats it as unbounded)")
	}
}

func TestListChunksInvalidDate(t *testing.T) {
	d := openTestDB(t)
	if _, err := ListChunks(d, ListOpts{Limit: 10, Since: "not-a-date"}); err == nil {
		t.Fatal("ListChunks: expected error for invalid since date")
	}
	if _, err := ListChunks(d, ListOpts{Limit: 10, Until: "2026/08/01"}); err == nil {
		t.Fatal("ListChunks: expected error for invalid until date")
	}
}

func TestListChunksHasEmbeddingAndDistilledFlags(t *testing.T) {
	d := openTestDB(t)
	id := insertChunk(t, d, "s1", "2026-08-01", "user", 0)
	if err := db.InsertEmbedding(d, id, []byte{0, 0, 0, 0}, "ollama:bge-m3:latest"); err != nil {
		t.Fatalf("InsertEmbedding: %v", err)
	}
	if err := db.MarkChunkDistilled(d, id, "2026-08-01T00:00:00Z"); err != nil {
		t.Fatalf("MarkChunkDistilled: %v", err)
	}
	other := insertChunk(t, d, "s1", "2026-08-02", "user", 1)

	res, err := ListChunks(d, ListOpts{Limit: 10})
	if err != nil {
		t.Fatalf("ListChunks: %v", err)
	}
	byID := map[int64]internal.ChunkSummary{}
	for _, c := range res {
		byID[c.ID] = c
	}
	if !byID[id].HasEmbedding || !byID[id].Distilled {
		t.Fatalf("chunk %d = %+v, want HasEmbedding and Distilled true", id, byID[id])
	}
	if byID[other].HasEmbedding || byID[other].Distilled {
		t.Fatalf("chunk %d = %+v, want both false", other, byID[other])
	}
}

func TestGetChunkFound(t *testing.T) {
	d := openTestDB(t)
	id := insertChunk(t, d, "s1", "2026-08-01", "user", 0)
	c, err := GetChunk(d, id)
	if err != nil {
		t.Fatalf("GetChunk: %v", err)
	}
	if c.ID != id || c.SessionID != "s1" || c.SessionDate != "2026-08-01" {
		t.Fatalf("GetChunk = %+v, want id=%d session=s1 date=2026-08-01", c, id)
	}
}

func TestGetChunkMissing(t *testing.T) {
	d := openTestDB(t)
	if _, err := GetChunk(d, 9999); err == nil {
		t.Fatal("GetChunk: expected error for missing chunk id")
	}
}

func TestDaysRollup(t *testing.T) {
	d := openTestDB(t)
	insertChunk(t, d, "s1", "2026-08-01", "user", 0)
	insertChunk(t, d, "s2", "2026-08-01", "user", 0) // same date, different session
	insertChunk(t, d, "s1", "2026-08-02", "user", 1)

	res, err := Days(d)
	if err != nil {
		t.Fatalf("Days: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("got %d day rows, want 2", len(res))
	}
	// newest first
	if res[0].SessionDate != "2026-08-02" {
		t.Fatalf("first day = %s, want 2026-08-02 (newest first)", res[0].SessionDate)
	}
	var d1 internal.DaySummary
	for _, r := range res {
		if r.SessionDate == "2026-08-01" {
			d1 = r
		}
	}
	if d1.Chunks != 2 || d1.Sessions != 2 {
		t.Fatalf("2026-08-01 rollup = %+v, want Chunks=2 Sessions=2", d1)
	}
}

func TestSessionsRollup(t *testing.T) {
	d := openTestDB(t)
	insertChunk(t, d, "s1", "2026-06-01", "user", 0)
	insertChunk(t, d, "s1", "2026-08-01", "user", 1) // s1 spans months
	insertChunk(t, d, "s2", "2026-08-02", "user", 0)

	res, err := Sessions(d)
	if err != nil {
		t.Fatalf("Sessions: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("got %d session rows, want 2", len(res))
	}
	var s1 internal.SessionSummary
	for _, r := range res {
		if r.SessionID == "s1" {
			s1 = r
		}
	}
	if s1.Chunks != 2 || s1.FirstDate != "2026-06-01" || s1.LastDate != "2026-08-01" {
		t.Fatalf("s1 rollup = %+v, want Chunks=2 FirstDate=2026-06-01 LastDate=2026-08-01", s1)
	}
	// most-recently-active first: s2 (2026-08-02) before s1 (last active 2026-08-01)
	if res[0].SessionID != "s2" {
		t.Fatalf("first session = %s, want s2 (most recently active)", res[0].SessionID)
	}
}
