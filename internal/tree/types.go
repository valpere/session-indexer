// Package tree builds a read-only structural view of a Claude Code session
// transcript: the main agent, the subagents it spawned, and each agent's tool
// calls. Unlike internal/mine, which flattens a transcript into text chunks,
// this keeps the envelope fields (agentId, tool_use ids, usage) that the tree
// needs. The wire format is undocumented and Claude-Code-internal, so every
// parser here is defensive: an unreadable line is skipped, never fatal.
package tree

// Status is an agent's state as derivable from a finished transcript.
type Status string

const (
	StatusDone       Status = "done"
	StatusInProgress Status = "in-progress" // at least one tool call has no result yet
)

// ParentState distinguishes the three shapes of a line's parentUuid: absent
// (flat metadata line), present-and-null (the root entry), present-and-set.
type ParentState int

const (
	ParentAbsent ParentState = iota
	ParentNull
	ParentSet
)

// ToolCall is one tool_use block and, once seen, its tool_result outcome.
type ToolCall struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	OK      bool   `json:"ok"`      // false if the matching result had is_error: true
	Pending bool   `json:"pending"` // no matching tool_result seen
}

// Node is one agent (or a workflow group) in the session tree.
type Node struct {
	AgentID      string     `json:"agent_id,omitempty"` // "" for the main agent and groups
	Label        string     `json:"label"`
	Description  string     `json:"description,omitempty"`
	Model        string     `json:"model,omitempty"`
	Status       Status     `json:"status"`
	OutputTokens int64      `json:"output_tokens"`
	Group        bool       `json:"group,omitempty"` // workflow container, not an agent
	Tools        []ToolCall `json:"tools"`
	Children     []*Node    `json:"children,omitempty"`
}

// Session is the whole tree for one transcript file.
type Session struct {
	ID    string `json:"id"`
	Title string `json:"title,omitempty"`
	Root  *Node  `json:"root"`
}
