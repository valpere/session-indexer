// Package eval is a retrieval-only quality harness: it loads a public
// long-conversation benchmark (LoCoMo), indexes each conversation into a
// throwaway SQLite store, and measures whether internal/search surfaces the
// ground-truth evidence turns in its top-k. No LLM judge is involved —
// LoCoMo labels which turns answer each question, so recall is deterministic.
package eval

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Turn is one utterance with its benchmark-assigned dialogue id (e.g. "D1:3").
type Turn struct {
	DiaID   string
	Speaker string
	Text    string
	Date    string // YYYY-MM-DD, "" if the session date could not be parsed
}

// QA is one question with its ground-truth evidence turn ids.
type QA struct {
	Question string
	Evidence []string
	Category int
}

// Conversation is one LoCoMo sample: ordered turns plus answerable questions.
type Conversation struct {
	ID         string
	Turns      []Turn
	QAs        []QA // only questions with at least one parseable evidence id
	NoEvidence int  // questions dropped because no usable evidence id remained
}

var diaID = regexp.MustCompile(`D\d+:\d+`)

// ParseEvidence extracts every well-formed dialogue id from raw evidence
// strings. LoCoMo evidence is messy: several ids can share one string
// ("D8:6; D9:17", "D9:1 D4:4"), and a few entries are garbled ("D", "D:11:26").
// Garbage is dropped; duplicates are removed preserving first-seen order.
func ParseEvidence(raw []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range raw {
		for _, id := range diaID.FindAllString(s, -1) {
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	return out
}

var sessionKey = regexp.MustCompile(`^session_(\d+)$`)

type rawSample struct {
	SampleID     string                     `json:"sample_id"`
	Conversation map[string]json.RawMessage `json:"conversation"`
	QA           []struct {
		Question string   `json:"question"`
		Evidence []string `json:"evidence"`
		Category int      `json:"category"`
	} `json:"qa"`
}

// LoadLoCoMo reads locomo10.json. Sessions are ordered by their numeric
// suffix (session_2 before session_10), never lexically.
func LoadLoCoMo(path string) ([]Conversation, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read dataset: %w", err)
	}
	var samples []rawSample
	if err := json.Unmarshal(b, &samples); err != nil {
		return nil, fmt.Errorf("parse dataset: %w", err)
	}
	out := make([]Conversation, 0, len(samples))
	for _, s := range samples {
		c := Conversation{ID: s.SampleID}
		type sess struct {
			n    int
			key  string
			rows []struct {
				Speaker string `json:"speaker"`
				DiaID   string `json:"dia_id"`
				Text    string `json:"text"`
			}
		}
		var sessions []sess
		for k, raw := range s.Conversation {
			m := sessionKey.FindStringSubmatch(k)
			if m == nil {
				continue
			}
			n, _ := strconv.Atoi(m[1])
			ss := sess{n: n, key: k}
			if json.Unmarshal(raw, &ss.rows) != nil {
				continue
			}
			sessions = append(sessions, ss)
		}
		sort.Slice(sessions, func(i, j int) bool { return sessions[i].n < sessions[j].n })
		for _, ss := range sessions {
			date := parseSessionDate(s.Conversation[ss.key+"_date_time"])
			for _, r := range ss.rows {
				c.Turns = append(c.Turns, Turn{DiaID: r.DiaID, Speaker: r.Speaker, Text: r.Text, Date: date})
			}
		}
		for _, q := range s.QA {
			ev := ParseEvidence(q.Evidence)
			if len(ev) == 0 {
				c.NoEvidence++
				continue
			}
			c.QAs = append(c.QAs, QA{Question: q.Question, Evidence: ev, Category: q.Category})
		}
		out = append(out, c)
	}
	return out, nil
}

// parseSessionDate turns "1:56 pm on 8 May, 2023" into "2023-05-08"; "" when
// the field is missing or in an unexpected shape.
func parseSessionDate(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	t, err := time.Parse("3:04 pm on 2 January, 2006", strings.TrimSpace(s))
	if err != nil {
		return ""
	}
	return t.Format("2006-01-02")
}
