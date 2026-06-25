package mine

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// Message is one extracted conversation turn.
type Message struct {
	SessionID    string
	Role         string
	MessageIndex int
	Content      string
	Timestamp    string
}

const toolBlockCap = 2048

var base64Run = regexp.MustCompile(`[A-Za-z0-9+/]{60,}`)

type rawRecord struct {
	Type      string          `json:"type"`
	IsMeta    bool            `json:"isMeta"`
	SessionID string          `json:"sessionId"`
	Timestamp string          `json:"timestamp"`
	Message   json.RawMessage `json:"message"`
}

type rawMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type contentBlock struct {
	Type    string          `json:"type"`
	Text    string          `json:"text"`
	Name    string          `json:"name"`
	Input   json.RawMessage `json:"input"`
	Content json.RawMessage `json:"content"`
}

// ParseJSONL reads JSONL records and returns kept user/assistant turns.
// fallbackSessionID is used when a record omits sessionId.
func ParseJSONL(r io.Reader, fallbackSessionID string) ([]Message, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024) // allow long JSONL lines
	var out []Message
	idx := 0
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var rec rawRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue // skip malformed lines, don't abort the mine
		}
		if rec.IsMeta || (rec.Type != "user" && rec.Type != "assistant") {
			continue
		}
		var msg rawMessage
		if err := json.Unmarshal(rec.Message, &msg); err != nil {
			continue
		}
		content := extractContent(msg.Content)
		if content == "" {
			continue
		}
		sid := rec.SessionID
		if sid == "" {
			sid = fallbackSessionID
		}
		out = append(out, Message{
			SessionID:    sid,
			Role:         rec.Type,
			MessageIndex: idx,
			Content:      content,
			Timestamp:    rec.Timestamp,
		})
		idx++
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan jsonl: %w", err)
	}
	return out, nil
}

// extractContent handles both string content and block-array content.
func extractContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	var blocks []contentBlock
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var b strings.Builder
	for _, blk := range blocks {
		switch blk.Type {
		case "text":
			b.WriteString(blk.Text)
			b.WriteString("\n")
		case "tool_use":
			b.WriteString(truncTool(blk.Name + " " + string(blk.Input)))
			b.WriteString("\n")
		case "tool_result":
			text := toolResultText(blk.Content)
			if text == "" || isBinary(text) {
				continue
			}
			b.WriteString(truncTool(text))
			b.WriteString("\n")
		}
	}
	return strings.TrimSpace(b.String())
}

func toolResultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []contentBlock
	if json.Unmarshal(raw, &blocks) == nil {
		var b strings.Builder
		for _, blk := range blocks {
			if blk.Type == "text" {
				b.WriteString(blk.Text)
			}
		}
		return b.String()
	}
	return ""
}

func isBinary(s string) bool {
	// Treat as binary only when content exceeds 10 KB; the base64Run regex alone
	// is too broad (matches any long alphanumeric run) and discards valid text.
	return len(s) > 10*1024
}

func truncTool(s string) string {
	if len(s) > toolBlockCap {
		return s[:toolBlockCap] + "\n[truncated]"
	}
	return s
}
