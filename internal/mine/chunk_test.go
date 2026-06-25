package mine

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func msg(content string) Message {
	return Message{SessionID: "s1", Role: "user", MessageIndex: 0,
		Content: content, Timestamp: "2026-06-25T10:00:00Z"}
}

func TestChunkFiltersNoise(t *testing.T) {
	cases := []string{
		"<system-reminder>do not</system-reminder>", // starts with <
		"/compact now please run it",                // slash command
		"too short",                                 // < 30 chars
	}
	for _, c := range cases {
		got := ChunkMessages([]Message{msg(c)})
		if len(got) != 0 {
			t.Errorf("content %q produced %d chunks, want 0", c, len(got))
		}
	}
}

func TestChunkKeepsRealContent(t *testing.T) {
	got := ChunkMessages([]Message{msg("This is a normal message well over thirty characters long.")})
	if len(got) != 1 {
		t.Fatalf("got %d chunks, want 1", len(got))
	}
	if got[0].SessionDate != "2026-06-25" || got[0].ChunkIndex != 0 {
		t.Fatalf("chunk = %+v", got[0])
	}
}

func TestChunkSplitsLongMessage(t *testing.T) {
	para := strings.Repeat("word ", 200) // ~1000 chars
	long := para + "\n\n" + para         // ~2000 chars, paragraph boundary
	got := ChunkMessages([]Message{msg(long)})
	if len(got) < 2 {
		t.Fatalf("got %d chunks, want >= 2", len(got))
	}
	for i, c := range got {
		if len(c.Content) > 1500 {
			t.Errorf("chunk %d len %d > 1500", i, len(c.Content))
		}
		if c.ChunkIndex != i {
			t.Errorf("chunk %d has ChunkIndex %d", i, c.ChunkIndex)
		}
	}
}

func TestChunkCyrillicHardSplit(t *testing.T) {
	// 1600 Cyrillic runes × 2 bytes each = 3200 bytes, single paragraph (no \n\n).
	// Old byte-indexing would corrupt at byte 1500 (mid-rune); rune-aware split must
	// produce 2 valid-UTF-8 chunks each ≤ 1500 runes.
	para := strings.Repeat("ї", 1600) // ї = U+0457, 2 bytes in UTF-8
	got := ChunkMessages([]Message{{
		SessionID: "s1", Role: "user", MessageIndex: 0,
		Content:   para,
		Timestamp: "2026-06-25T10:00:00Z",
	}})
	if len(got) < 2 {
		t.Fatalf("got %d chunks, want >= 2 for 1600-rune Cyrillic paragraph", len(got))
	}
	for i, c := range got {
		if !utf8.ValidString(c.Content) {
			t.Errorf("chunk %d is not valid UTF-8", i)
		}
		if len([]rune(c.Content)) > 1500 {
			t.Errorf("chunk %d has %d runes, want <= 1500", i, len([]rune(c.Content)))
		}
	}
}
