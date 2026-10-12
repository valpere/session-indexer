package tree

import (
	"fmt"
	"io"
	"strings"
)

// Render writes s as an indented text tree.
func Render(w io.Writer, s *Session) {
	agents, tools := count(s.Root)
	header := "session " + s.ID
	if s.Title != "" {
		header += " — " + s.Title
	}
	fmt.Fprintf(w, "%s\n  %d agent(s), %d tool call(s)\n\n", header, agents, tools)
	renderNode(w, s.Root, 0)
}

// count totals agents (workflow groups excluded) and tool calls under n.
func count(n *Node) (agents, tools int) {
	if !n.Group {
		agents = 1
	}
	tools = len(n.Tools)
	for _, c := range n.Children {
		a, t := count(c)
		agents, tools = agents+a, tools+t
	}
	return
}

func glyph(s Status) string {
	if s == StatusInProgress {
		return "●"
	}
	return "✓"
}

func renderNode(w io.Writer, n *Node, depth int) {
	ind := strings.Repeat("  ", depth)
	line := fmt.Sprintf("%s%s %s (%s)", ind, glyph(n.Status), n.Label, n.Status)
	if n.Description != "" {
		line += fmt.Sprintf(" — %q", n.Description)
	}
	fmt.Fprintln(w, line)
	if !n.Group {
		stats := fmt.Sprintf("tokens: %d", n.OutputTokens)
		if n.Model != "" {
			stats = "model: " + n.Model + ", " + stats
		}
		var ok, bad, pend int
		for _, t := range n.Tools {
			switch {
			case t.Pending:
				pend++
			case t.OK:
				ok++
			default:
				bad++
			}
		}
		fmt.Fprintf(w, "%s    %s\n%s    tools: %d (%d✓ %d✗ %d⏳)\n", ind, stats, ind, len(n.Tools), ok, bad, pend)
	}
	for _, c := range n.Children {
		renderNode(w, c, depth+1)
	}
}
