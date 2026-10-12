package tree

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const sid = "049efdb4-0000-4000-8000-000000000001"

// writeFile creates dir/name (making parents) with lines joined by newlines.
func writeFile(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assistant(uuid, parent, req, model string, out int, blocks string) string {
	return `{"type":"assistant","uuid":"` + uuid + `","parentUuid":` + parent + `,"requestId":"` + req +
		`","message":{"model":"` + model + `","content":[` + blocks + `],"usage":{"output_tokens":` +
		strconv.Itoa(out) + `}}}`
}

func toolUseBlock(id, name, input string) string {
	return `{"type":"tool_use","id":"` + id + `","name":"` + name + `","input":` + input + `,"caller":{"type":"direct"}}`
}

func toolResultLine(id string, isErr bool) string {
	e := ""
	if isErr {
		e = `,"is_error":true`
	}
	return `{"type":"user","uuid":"r-` + id + `","parentUuid":"x","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + id + `","content":"ok"` + e + `}]}}`
}

func TestBuildNoSubagents(t *testing.T) {
	p := filepath.Join(t.TempDir(), sid+".jsonl")
	writeFile(t, p,
		`{"type":"ai-title","aiTitle":"my session"}`,
		`{"type":"user","uuid":"u0","parentUuid":null,"message":{"role":"user","content":"hi"}}`,
		assistant("a1", `"u0"`, "req1", "claude-x", 10, toolUseBlock("t1", "Bash", `{}`)+","+toolUseBlock("t2", "Read", `{}`)),
		toolResultLine("t1", false),
		toolResultLine("t2", true),
	)
	s, err := Build(p)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if s.ID != sid || s.Title != "my session" {
		t.Fatalf("id/title = %q/%q", s.ID, s.Title)
	}
	r := s.Root
	if r.Label != "main" || r.Model != "claude-x" || r.OutputTokens != 10 {
		t.Fatalf("root = %+v", r)
	}
	if len(r.Tools) != 2 || !r.Tools[0].OK || r.Tools[1].OK || r.Tools[1].Pending {
		t.Fatalf("tools = %+v", r.Tools)
	}
	if r.Status != StatusDone {
		t.Fatalf("status = %q, want done", r.Status)
	}
	if len(r.Children) != 0 {
		t.Fatalf("unexpected children: %d", len(r.Children))
	}
}

func TestBuildPendingToolMeansInProgress(t *testing.T) {
	p := filepath.Join(t.TempDir(), sid+".jsonl")
	writeFile(t, p, assistant("a1", `null`, "req1", "m", 1, toolUseBlock("t1", "Bash", `{}`)))
	s, err := Build(p)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Root.Tools[0].Pending || s.Root.Status != StatusInProgress {
		t.Fatalf("root = %+v", s.Root)
	}
}

func TestBuildDedupesTokensByRequestID(t *testing.T) {
	// One logical turn split across lines repeats the same cumulative usage.
	p := filepath.Join(t.TempDir(), sid+".jsonl")
	writeFile(t, p,
		assistant("a1", `null`, "reqA", "m", 100, `{"type":"text","text":"x"}`),
		assistant("a2", `"a1"`, "reqA", "m", 100, toolUseBlock("t1", "Bash", `{}`)),
		assistant("a3", `"a2"`, "reqB", "m", 50, `{"type":"text","text":"y"}`),
	)
	s, err := Build(p)
	if err != nil {
		t.Fatal(err)
	}
	if s.Root.OutputTokens != 150 {
		t.Fatalf("tokens = %d, want 150", s.Root.OutputTokens)
	}
}

func TestBuildJoinsSubagentByToolUseID(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, sid+".jsonl")
	writeFile(t, p,
		assistant("a1", `null`, "req1", "main-m", 5,
			toolUseBlock("toolu_spawn", "Agent", `{"description":"survey code","subagent_type":"Explore"}`)),
		toolResultLine("toolu_spawn", false),
	)
	sub := filepath.Join(dir, sid, "subagents")
	writeFile(t, filepath.Join(sub, "agent-abc123.jsonl"),
		`{"type":"assistant","agentId":"abc123","uuid":"s1","parentUuid":null,"requestId":"sr1","message":{"model":"sub-m","content":[`+
			toolUseBlock("st1", "Grep", `{}`)+`],"usage":{"output_tokens":7}}}`,
		`{"type":"user","agentId":"abc123","uuid":"sr","parentUuid":"s1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"st1","content":"ok"}]}}`,
	)
	writeFile(t, filepath.Join(sub, "agent-abc123.meta.json"),
		`{"agentType":"Explore","description":"survey code","toolUseId":"toolu_spawn"}`)

	s, err := Build(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Root.Children) != 1 {
		t.Fatalf("children = %d, want 1", len(s.Root.Children))
	}
	c := s.Root.Children[0]
	if c.AgentID != "abc123" || c.Model != "sub-m" || c.OutputTokens != 7 || c.Label != "Explore" ||
		c.Description != "survey code" {
		t.Fatalf("child = %+v", c)
	}
	if len(c.Tools) != 1 || !c.Tools[0].OK {
		t.Fatalf("child tools = %+v", c.Tools)
	}
	// The spawn call itself stays on the parent's tool list.
	if len(s.Root.Tools) != 1 || s.Root.Tools[0].Name != "Agent" {
		t.Fatalf("root tools = %+v", s.Root.Tools)
	}
}

func TestBuildMetaDiscoveredAfterSpawnOutOfOrder(t *testing.T) {
	// Subagent file listed (sorted) before the spawn's parent exists in the
	// scan order must still join: the join happens after both are parsed.
	dir := t.TempDir()
	p := filepath.Join(dir, sid+".jsonl")
	writeFile(t, p, assistant("a1", `null`, "r", "m", 1, toolUseBlock("T", "Task", `{}`)))
	sub := filepath.Join(dir, sid, "subagents")
	writeFile(t, filepath.Join(sub, "agent-aaa.jsonl"), `{"type":"assistant","agentId":"aaa","uuid":"u","requestId":"q","message":{"content":[]}}`)
	writeFile(t, filepath.Join(sub, "agent-aaa.meta.json"), `{"toolUseId":"T"}`)
	s, err := Build(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Root.Children) != 1 || s.Root.Children[0].AgentID != "aaa" {
		t.Fatalf("children = %+v", s.Root.Children)
	}
}

func TestBuildOrphanSubagentAttachesToRoot(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, sid+".jsonl")
	writeFile(t, p, assistant("a1", `null`, "r", "m", 1, `{"type":"text","text":"x"}`))
	sub := filepath.Join(dir, sid, "subagents")
	writeFile(t, filepath.Join(sub, "agent-zzz.jsonl"), `{"type":"assistant","agentId":"zzz","uuid":"u","requestId":"q","message":{"content":[]}}`)
	s, err := Build(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Root.Children) != 1 || s.Root.Children[0].AgentID != "zzz" {
		t.Fatalf("orphan not attached: %+v", s.Root.Children)
	}
}

func TestBuildWorkflowAgentsNestUnderWorkflowSpawn(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, sid+".jsonl")
	writeFile(t, p,
		assistant("a1", `null`, "r", "m", 1, toolUseBlock("toolu_wf", "Workflow", `{}`)),
		`{"type":"user","uuid":"wr","parentUuid":"a1","toolUseResult":{"taskType":"local_workflow","runId":"wf_1","workflowName":"code-review"},"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_wf","content":"launched"}]}}`,
	)
	wf := filepath.Join(dir, sid, "subagents", "workflows", "wf_1")
	writeFile(t, filepath.Join(wf, "journal.jsonl"), `{"type":"started","key":"k","agentId":"w1"}`)
	writeFile(t, filepath.Join(wf, "agent-w1.jsonl"), `{"type":"assistant","agentId":"w1","uuid":"u","requestId":"q","message":{"content":[],"usage":{"output_tokens":3}}}`)
	writeFile(t, filepath.Join(wf, "agent-w1.meta.json"), `{"agentType":"workflow-subagent"}`)

	s, err := Build(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Root.Children) != 1 {
		t.Fatalf("root children = %d", len(s.Root.Children))
	}
	g := s.Root.Children[0]
	if g.Label != "code-review" || len(g.Children) != 1 || g.Children[0].AgentID != "w1" {
		t.Fatalf("workflow group = %+v", g)
	}
}

func TestBuildSkipsMalformedLinesWithoutPanic(t *testing.T) {
	p := filepath.Join(t.TempDir(), sid+".jsonl")
	writeFile(t, p,
		`{not json`,
		``,
		`{"type":"assistant","uuid":"a1","requestId":"r","message":{"model":"m","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{}}`, // truncated
		`{"type":"mystery","x":1}`,
		assistant("a2", `null`, "r2", "m", 4, `{"type":"text","text":"ok"}`),
	)
	s, err := Build(p)
	if err != nil {
		t.Fatalf("Build must tolerate bad lines: %v", err)
	}
	if s.Root.OutputTokens != 4 {
		t.Fatalf("tokens = %d, want 4", s.Root.OutputTokens)
	}
}

func TestEnvelopeParentUUIDTriState(t *testing.T) {
	cases := []struct {
		line string
		want ParentState
	}{
		{`{"type":"user","uuid":"a"}`, ParentAbsent},
		{`{"type":"user","uuid":"a","parentUuid":null}`, ParentNull},
		{`{"type":"user","uuid":"a","parentUuid":"p"}`, ParentSet},
	}
	for _, c := range cases {
		e, ok := parseLine([]byte(c.line))
		if !ok {
			t.Fatalf("parseLine(%s) failed", c.line)
		}
		if e.Parent != c.want {
			t.Errorf("%s: parent = %v, want %v", c.line, e.Parent, c.want)
		}
	}
}

func TestBuildMissingSubagentsDirIsNotAnError(t *testing.T) {
	p := filepath.Join(t.TempDir(), sid+".jsonl")
	writeFile(t, p, assistant("a1", `null`, "r", "m", 1, `{"type":"text","text":"x"}`))
	if _, err := Build(p); err != nil {
		t.Fatal(err)
	}
}

func TestRenderIndentedTree(t *testing.T) {
	s := &Session{
		ID: "sess", Title: "t",
		Root: &Node{
			Label: "main", Status: StatusInProgress, Model: "m", OutputTokens: 12,
			Tools: []ToolCall{{Name: "Agent", OK: true}},
			Children: []*Node{{
				AgentID: "abc", Label: "Explore", Description: "survey", Status: StatusDone,
				Tools: []ToolCall{{OK: true}, {OK: false}, {Pending: true}},
			}},
		},
	}
	var buf bytes.Buffer
	Render(&buf, s)
	out := buf.String()
	for _, want := range []string{
		"session sess — t",
		"2 agent(s), 4 tool call(s)",
		"● main (in-progress)",
		"model: m, tokens: 12",
		"  ✓ Explore (done) — \"survey\"",
		"tools: 3 (1✓ 1✗ 1⏳)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q in:\n%s", want, out)
		}
	}
}
