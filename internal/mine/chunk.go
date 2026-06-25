package mine

import (
	"regexp"
	"strings"
	"time"

	"github.com/valpere/session-indexer/internal"
)

const maxChunkChars = 1500

var slashCmd = regexp.MustCompile(`^/\w+`)

// ChunkMessages turns parsed messages into stored chunks: noise-filtered,
// split at paragraph boundaries to <=1500 chars, with date metadata.
func ChunkMessages(msgs []Message) []internal.Chunk {
	var out []internal.Chunk
	for _, m := range msgs {
		date := m.Timestamp
		if len(date) >= 10 {
			date = date[:10]
		} else {
			date = time.Now().Format("2006-01-02")
		}
		created := m.Timestamp
		if created == "" {
			created = time.Now().Format(time.RFC3339)
		}
		ci := 0
		for _, part := range splitToSize(m.Content, maxChunkChars) {
			if isNoise(part) {
				continue
			}
			out = append(out, internal.Chunk{
				SessionID:    m.SessionID,
				SessionDate:  date,
				Role:         m.Role,
				MessageIndex: m.MessageIndex,
				ChunkIndex:   ci,
				Content:      part,
				CreatedAt:    created,
			})
			ci++
		}
	}
	return out
}

// isNoise reports whether a chunk should be dropped.
func isNoise(s string) bool {
	t := strings.TrimSpace(s)
	if len(t) < 30 {
		return true
	}
	if strings.HasPrefix(t, "<") {
		return true
	}
	return slashCmd.MatchString(t)
}

// splitToSize splits text on paragraph boundaries so each part is <= max chars.
// Uses rune-aware hard-split to preserve UTF-8 validity for Cyrillic content.
func splitToSize(text string, max int) []string {
	if len([]rune(text)) <= max {
		return []string{text}
	}
	var parts []string
	var cur strings.Builder
	for _, para := range strings.Split(text, "\n\n") {
		paraRunes := []rune(para)
		if cur.Len() > 0 && len([]rune(cur.String()))+len(paraRunes)+2 > max {
			parts = append(parts, strings.TrimSpace(cur.String()))
			cur.Reset()
		}
		if len(paraRunes) > max {
			// hard-split on rune boundary
			for len(paraRunes) > max {
				parts = append(parts, string(paraRunes[:max]))
				paraRunes = paraRunes[max:]
			}
			if len(paraRunes) > 0 {
				if cur.Len() > 0 {
					cur.WriteString("\n\n")
				}
				cur.WriteString(string(paraRunes))
			}
			continue
		}
		if cur.Len() > 0 {
			cur.WriteString("\n\n")
		}
		cur.WriteString(para)
	}
	if strings.TrimSpace(cur.String()) != "" {
		parts = append(parts, strings.TrimSpace(cur.String()))
	}
	return parts
}
