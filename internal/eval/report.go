package eval

import (
	"fmt"
	"sort"
	"strings"
)

// CategoryNames are the official LoCoMo question categories. Verified against
// the data (category 2 = "When did ..." temporal questions; category 4 is the
// largest = single-hop). memorybench's loader labels 2 as multi-hop and 4 as
// world-knowledge, which does not match the dataset.
var CategoryNames = map[int]string{
	1: "multi-hop", 2: "temporal", 3: "open-domain", 4: "single-hop", 5: "adversarial",
}

// ModeReport is one retrieval mode's results.
type ModeReport struct {
	Mode        string
	Overall     Summary
	PerCategory map[int]Summary
}

// Report is the full evaluation outcome, written as JSON and rendered as Markdown.
type Report struct {
	Dataset    string
	Date       string
	Ks         []int
	NoEvidence int // questions excluded because no usable evidence id remained
	Modes      []ModeReport
}

// RenderMarkdown renders r deterministically (categories ascending).
func RenderMarkdown(r Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Retrieval eval — %s (%s)\n\nQuestions without usable evidence (excluded): %d\n", r.Dataset, r.Date, r.NoEvidence)
	for _, m := range r.Modes {
		fmt.Fprintf(&b, "\n## %s\n\n| group | N |", m.Mode)
		sep := "|---|---|"
		for _, k := range r.Ks {
			fmt.Fprintf(&b, " R@%d any | R@%d all |", k, k)
			sep += "---|---|"
		}
		b.WriteString(" MRR | lat ms | ctx tok |\n" + sep + "---|---|---|\n")
		row := func(name string, s Summary) {
			fmt.Fprintf(&b, "| %s | %d |", name, s.N)
			for _, k := range r.Ks {
				fmt.Fprintf(&b, " %.3f | %.3f |", s.RecallAny[k], s.RecallAll[k])
			}
			fmt.Fprintf(&b, " %.3f | %.1f | %.0f |\n", s.MRR, s.AvgLatencyMs, s.AvgContextTokens)
		}
		row("overall", m.Overall)
		cats := make([]int, 0, len(m.PerCategory))
		for c := range m.PerCategory {
			cats = append(cats, c)
		}
		sort.Ints(cats)
		for _, c := range cats {
			row(fmt.Sprintf("cat %d %s", c, CategoryNames[c]), m.PerCategory[c])
		}
	}
	return b.String()
}
