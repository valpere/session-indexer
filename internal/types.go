// Package internal holds row types shared across db, mine, and search.
package internal

// Chunk is one stored, searchable unit of conversation.
type Chunk struct {
	SessionID    string
	SessionDate  string // YYYY-MM-DD
	Role         string // "user" | "assistant"
	MessageIndex int    // 0-based ordinal within session
	ChunkIndex   int    // 0-based ordinal within message
	Content      string
	CreatedAt    string // RFC3339, for display/sort only
}

// SearchResult is one ranked hit returned by search.
type SearchResult struct {
	SessionDate string
	Role        string
	Content     string
	Score       float64 // cosine similarity, or BM25 (negated) in fallback
}

// Fact is one distilled subject-predicate-object claim, optionally
// tombstoned by a newer fact that supersedes it.
type Fact struct {
	ID            int64
	Subject       string
	Predicate     string
	Object        string
	Confidence    float64
	SourceChunkID int64
	SessionDate   string
	CreatedAt     string  // RFC3339, distilled-at
	Until         *string // tombstone timestamp; nil = currently valid
	SupersededBy  *int64  // id of the fact that superseded this one
}

// ChunkSummary is one stored chunk as returned by the browse verbs
// (list/show) — distinct from SearchResult, whose Score is meaningless
// when nothing was ranked, and which lacks ID/SessionID needed to browse.
type ChunkSummary struct {
	ID           int64
	SessionID    string
	SessionDate  string
	Role         string
	Content      string
	HasEmbedding bool
	Distilled    bool
}

// DaySummary rolls up chunks by session_date, the default browse axis
// (session_id is a poor axis in practice — see internal/browse doc).
type DaySummary struct {
	SessionDate string
	Chunks      int
	Embedded    int
	Distilled   int
	Sessions    int // distinct session_ids touching this date
}

// SessionSummary rolls up chunks by session_id (the --by-session view).
type SessionSummary struct {
	SessionID string
	Chunks    int
	Embedded  int
	Distilled int
	FirstDate string
	LastDate  string
}
