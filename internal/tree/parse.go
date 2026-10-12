package tree

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
)

// entry is the subset of one JSONL line the tree needs.
type entry struct {
	Type      string
	Parent    ParentState
	RequestID string
	AgentID   string
	Title     string // ai-title lines
	Model     string
	OutTokens int64
	ToolUses  []toolUse
	Results   []toolResult
	Launch    *workflowLaunch // user line acknowledging a workflow spawn
}

type toolUse struct {
	ID, Name string
}

type toolResult struct {
	ToolUseID string
	IsError   bool
}

type workflowLaunch struct {
	RunID, Name string
}

type rawLine struct {
	Type      string          `json:"type"`
	ParentRaw json.RawMessage `json:"parentUuid"`
	RequestID string          `json:"requestId"`
	AgentID   string          `json:"agentId"`
	AITitle   string          `json:"aiTitle"`
	Message   *struct {
		Model   string          `json:"model"`
		Content json.RawMessage `json:"content"`
		Usage   *struct {
			OutputTokens int64 `json:"output_tokens"`
		} `json:"usage"`
	} `json:"message"`
	ToolUseResult json.RawMessage `json:"toolUseResult"`
}

type rawBlock struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	ToolUseID string `json:"tool_use_id"`
	IsError   bool   `json:"is_error"`
}

// parseLine decodes one JSONL line; ok is false for blank or malformed lines.
func parseLine(b []byte) (entry, bool) {
	b = bytes.TrimSpace(b)
	if len(b) == 0 {
		return entry{}, false
	}
	var r rawLine
	if err := json.Unmarshal(b, &r); err != nil {
		return entry{}, false
	}
	e := entry{Type: r.Type, RequestID: r.RequestID, AgentID: r.AgentID, Title: r.AITitle}
	switch {
	case len(r.ParentRaw) == 0:
		e.Parent = ParentAbsent
	case string(r.ParentRaw) == "null":
		e.Parent = ParentNull
	default:
		e.Parent = ParentSet
	}
	if r.Message != nil {
		e.Model = r.Message.Model
		if r.Message.Usage != nil {
			e.OutTokens = r.Message.Usage.OutputTokens
		}
		// content is a bare string for plain user turns; only arrays hold blocks.
		var blocks []rawBlock
		if json.Unmarshal(r.Message.Content, &blocks) == nil {
			for _, bl := range blocks {
				switch bl.Type {
				case "tool_use":
					e.ToolUses = append(e.ToolUses, toolUse{ID: bl.ID, Name: bl.Name})
				case "tool_result":
					e.Results = append(e.Results, toolResult{ToolUseID: bl.ToolUseID, IsError: bl.IsError})
				}
			}
		}
	}
	if len(r.ToolUseResult) > 0 {
		var v struct {
			TaskType     string `json:"taskType"`
			RunID        string `json:"runId"`
			WorkflowName string `json:"workflowName"`
		}
		// toolUseResult is an object or a bare string; a string just fails here.
		if json.Unmarshal(r.ToolUseResult, &v) == nil && v.TaskType == "local_workflow" && v.RunID != "" {
			e.Launch = &workflowLaunch{RunID: v.RunID, Name: v.WorkflowName}
		}
	}
	return e, true
}

// forEachEntry streams the parseable lines of the JSONL file at path. Lines
// have no size cap (real transcripts hold multi-MB tool results), and bad
// lines are skipped.
func forEachEntry(path string, fn func(entry)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<20)
	for {
		line, err := r.ReadBytes('\n')
		if e, ok := parseLine(line); ok {
			fn(e)
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
