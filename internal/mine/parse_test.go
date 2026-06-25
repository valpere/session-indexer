package mine

import (
	"strings"
	"testing"
)

func TestParseExtractsUserAndAssistantText(t *testing.T) {
	jsonl := strings.Join([]string{
		`{"type":"user","sessionId":"s9","timestamp":"2026-06-25T10:00:00Z","message":{"role":"user","content":"how do I open the db"}}`,
		`{"type":"assistant","sessionId":"s9","timestamp":"2026-06-25T10:00:05Z","message":{"role":"assistant","content":[{"type":"text","text":"Call db.Open with the path."}]}}`,
	}, "\n")
	msgs, err := ParseJSONL(strings.NewReader(jsonl), "fallback")
	if err != nil {
		t.Fatalf("ParseJSONL: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2", len(msgs))
	}
	if msgs[0].SessionID != "s9" || msgs[0].Role != "user" || msgs[0].MessageIndex != 0 {
		t.Fatalf("msg0 = %+v", msgs[0])
	}
	if !strings.Contains(msgs[1].Content, "db.Open") || msgs[1].MessageIndex != 1 {
		t.Fatalf("msg1 = %+v", msgs[1])
	}
}

func TestParseSkipsMeta(t *testing.T) {
	jsonl := `{"type":"user","isMeta":true,"sessionId":"s9","message":{"role":"user","content":"system reminder"}}`
	msgs, _ := ParseJSONL(strings.NewReader(jsonl), "fallback")
	if len(msgs) != 0 {
		t.Fatalf("got %d, want 0 (isMeta must be skipped)", len(msgs))
	}
}

func TestParseUsesFallbackSessionID(t *testing.T) {
	jsonl := `{"type":"user","message":{"role":"user","content":"no session id field here"}}`
	msgs, _ := ParseJSONL(strings.NewReader(jsonl), "from-filename")
	if len(msgs) != 1 || msgs[0].SessionID != "from-filename" {
		t.Fatalf("msgs = %+v", msgs)
	}
}

func TestParseTruncatesLargeToolBlock(t *testing.T) {
	big := strings.Repeat("x", 5000)
	jsonl := `{"type":"assistant","sessionId":"s1","message":{"role":"assistant","content":[{"type":"tool_result","content":"` + big + `"}]}}`
	msgs, _ := ParseJSONL(strings.NewReader(jsonl), "f")
	if len(msgs) != 1 {
		t.Fatalf("got %d, want 1", len(msgs))
	}
	if !strings.Contains(msgs[0].Content, "[truncated]") || len(msgs[0].Content) > 2100 {
		t.Fatalf("tool block not truncated: len=%d", len(msgs[0].Content))
	}
}
