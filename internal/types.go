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
