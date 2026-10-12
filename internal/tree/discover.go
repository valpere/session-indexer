package tree

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// agentFile is a discovered agent-<id>.jsonl with its sidecar metadata.
type agentFile struct {
	AgentID     string
	Path        string
	AgentType   string
	Description string
	ToolUseID   string // equals the spawning tool_use block's id in the parent
}

// subagentsDir is <dir>/<session-uuid>/subagents for <dir>/<session-uuid>.jsonl.
// It is created lazily by Claude Code, so it may not exist.
func subagentsDir(sessionPath string) string {
	stem := strings.TrimSuffix(filepath.Base(sessionPath), filepath.Ext(sessionPath))
	return filepath.Join(filepath.Dir(sessionPath), stem, "subagents")
}

// scanAgentFiles lists agent-*.jsonl in dir (sorted by agent id), reading each
// .meta.json sidecar if present. A missing dir yields no files, not an error.
func scanAgentFiles(dir string) []agentFile {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []agentFile
	for _, e := range ents {
		name := e.Name()
		id, ok := strings.CutPrefix(name, "agent-")
		if !ok || e.IsDir() || !strings.HasSuffix(id, ".jsonl") {
			continue
		}
		id = strings.TrimSuffix(id, ".jsonl")
		if id == "" {
			continue
		}
		af := agentFile{AgentID: id, Path: filepath.Join(dir, name)}
		if b, err := os.ReadFile(filepath.Join(dir, "agent-"+id+".meta.json")); err == nil {
			var m struct {
				AgentType   string `json:"agentType"`
				Description string `json:"description"`
				ToolUseID   string `json:"toolUseId"`
			}
			if json.Unmarshal(b, &m) == nil {
				af.AgentType, af.Description, af.ToolUseID = m.AgentType, m.Description, m.ToolUseID
			}
		}
		out = append(out, af)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AgentID < out[j].AgentID })
	return out
}

// scanWorkflowIDs lists subagents/workflows/<id> directory names, sorted.
func scanWorkflowIDs(subDir string) []string {
	ents, err := os.ReadDir(filepath.Join(subDir, "workflows"))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}
