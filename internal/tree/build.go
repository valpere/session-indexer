package tree

import (
	"fmt"
	"path/filepath"
	"strings"
)

// isSpawn reports whether a tool name starts a subagent. "Task" is the legacy
// name of "Agent"; "Workflow" spawns a group of them.
func isSpawn(name string) bool {
	return name == "Agent" || name == "Task" || name == "Workflow"
}

// agentData is what one transcript file contributes before the tree is joined.
type agentData struct {
	title     string
	model     string
	tokens    int64
	tools     []ToolCall
	spawnIDs  []string          // tool_use ids of spawn calls, in order
	launches  map[string]string // tool_use id -> workflow run id (from results)
	runNames  map[string]string // workflow run id -> name
	seenReq   map[string]bool
	toolIndex map[string]int
}

// parseAgent reads one transcript file into agentData.
func parseAgent(path string) (*agentData, error) {
	d := &agentData{
		launches: map[string]string{}, runNames: map[string]string{},
		seenReq: map[string]bool{}, toolIndex: map[string]int{},
	}
	err := forEachEntry(path, func(e entry) {
		if e.Title != "" {
			d.title = e.Title
		}
		if e.Type == "assistant" {
			if e.Model != "" {
				d.model = e.Model
			}
			// One logical turn is split across lines that repeat the same
			// cumulative usage; count it once per requestId (lines without
			// one are summed per line).
			if e.RequestID == "" || !d.seenReq[e.RequestID] {
				d.tokens += e.OutTokens
				if e.RequestID != "" {
					d.seenReq[e.RequestID] = true
				}
			}
			for _, tu := range e.ToolUses {
				if _, dup := d.toolIndex[tu.ID]; dup && tu.ID != "" {
					continue
				}
				d.toolIndex[tu.ID] = len(d.tools)
				d.tools = append(d.tools, ToolCall{ID: tu.ID, Name: tu.Name, Pending: true})
				if isSpawn(tu.Name) {
					d.spawnIDs = append(d.spawnIDs, tu.ID)
				}
			}
		}
		for _, r := range e.Results {
			if i, ok := d.toolIndex[r.ToolUseID]; ok {
				d.tools[i].Pending = false
				d.tools[i].OK = !r.IsError
			}
			if e.Launch != nil {
				d.launches[r.ToolUseID] = e.Launch.RunID
			}
		}
		if e.Launch != nil {
			d.runNames[e.Launch.RunID] = e.Launch.Name
		}
	})
	return d, err
}

func (d *agentData) node(label string) *Node {
	n := &Node{Label: label, Model: d.model, OutputTokens: d.tokens, Tools: d.tools, Status: StatusDone}
	for _, t := range d.tools {
		if t.Pending {
			n.Status = StatusInProgress
			break
		}
	}
	return n
}

// Build parses the transcript at sessionPath and any subagent files that
// belong to it, joining each subagent to the spawn call that created it by
// tool_use id. A subagent whose spawn can't be found attaches to the root so
// it is never silently dropped.
func Build(sessionPath string) (*Session, error) {
	main, err := parseAgent(sessionPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", sessionPath, err)
	}
	s := &Session{
		ID:    strings.TrimSuffix(filepath.Base(sessionPath), filepath.Ext(sessionPath)),
		Title: main.title,
		Root:  main.node("main"),
	}

	owners := map[*Node]*agentData{s.Root: main} // node -> its parsed data
	byTool := map[string][]*Node{}               // spawning tool_use id -> children
	var orphans []*Node
	runToTool := map[string]string{}
	runNames := map[string]string{}
	collect := func(d *agentData) {
		for tool, run := range d.launches {
			runToTool[run] = tool
		}
		for run, name := range d.runNames {
			runNames[run] = name
		}
	}
	collect(main)

	// join=false is for workflow agents: their meta has no toolUseId, they hang
	// off their group node rather than a spawn call.
	addAgents := func(files []agentFile, join bool) []*Node {
		var nodes []*Node
		for _, f := range files {
			d, err := parseAgent(f.Path)
			if err != nil {
				continue // unreadable subagent file: skip, keep the rest
			}
			label := f.AgentType
			if label == "" {
				label = "agent"
			}
			n := d.node(label)
			n.AgentID, n.Description = f.AgentID, f.Description
			owners[n] = d
			collect(d)
			nodes = append(nodes, n)
			if !join {
				continue
			}
			if f.ToolUseID != "" {
				byTool[f.ToolUseID] = append(byTool[f.ToolUseID], n)
			} else {
				orphans = append(orphans, n)
			}
		}
		return nodes
	}

	sub := subagentsDir(sessionPath)
	addAgents(scanAgentFiles(sub), true)

	type group struct {
		node *Node
		id   string
	}
	var groups []group
	for _, wf := range scanWorkflowIDs(sub) {
		g := &Node{Label: "workflow " + wf, Status: StatusDone, Group: true}
		g.Children = addAgents(scanAgentFiles(filepath.Join(sub, "workflows", wf)), false)
		groups = append(groups, group{g, wf})
	}
	for _, g := range groups {
		if name := runNames[g.id]; name != "" {
			g.node.Label = name
		}
		if tool, ok := runToTool[g.id]; ok {
			byTool[tool] = append(byTool[tool], g.node)
		} else {
			orphans = append(orphans, g.node)
		}
	}

	// Attach children in spawn order, recursing so a subagent's own spawns
	// nest under it. Workflow-group children were already attached above.
	attached := map[*Node]bool{}
	var attach func(n *Node)
	attach = func(n *Node) {
		d := owners[n]
		if d == nil {
			return
		}
		for _, id := range d.spawnIDs {
			for _, c := range byTool[id] {
				if attached[c] {
					continue
				}
				attached[c] = true
				n.Children = append(n.Children, c)
				attach(c)
			}
		}
	}
	attach(s.Root)
	for _, ns := range byTool {
		for _, c := range ns {
			if !attached[c] {
				orphans = append(orphans, c)
			}
		}
	}
	for _, o := range orphans {
		if attached[o] {
			continue
		}
		attached[o] = true
		s.Root.Children = append(s.Root.Children, o)
		attach(o)
	}
	return s, nil
}
