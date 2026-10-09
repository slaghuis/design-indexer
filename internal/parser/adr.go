package parser

import (
	"strings"
)

// DecisionSummary is a best-effort extraction of the key "Decision" and
// "Status" from an ADR, used by the find_decision tool.
type DecisionSummary struct {
	Status       string
	Decision     string
	Context      string
	Consequences string
}

func ExtractDecision(doc *Doc) DecisionSummary {
	ds := DecisionSummary{Status: doc.Frontmatter.Status}

	sections := splitSections(doc.Body)
	for _, s := range sections {
		hl := strings.ToLower(s.Heading)
		switch {
		case strings.HasPrefix(hl, "decision"):
			ds.Decision = truncate(s.Text, 1200)
		case strings.HasPrefix(hl, "context"):
			ds.Context = truncate(s.Text, 800)
		case strings.HasPrefix(hl, "consequences"),
			strings.HasPrefix(hl, "trade-off"):
			ds.Consequences = truncate(s.Text, 800)
		case strings.HasPrefix(hl, "status") && ds.Status == "":
			ds.Status = strings.TrimSpace(s.Text)
		}
	}
	return ds
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n… (truncated)"
}