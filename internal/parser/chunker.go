package parser

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode/utf8"
)

type Chunk struct {
	Doc           *Doc
	ChunkIndex    int
	SectionPath   string    // "Decision > Trade-offs"
	Heading       string    // immediate heading
	Text          string    // chunk body
	TokensApprox  int
	StartLine     int
	EndLine       int
	Hash          string
}

type ChunkingOpts struct {
	MaxTokens     int
	MinTokens     int
	OverlapTokens int
}

// ChunkDoc produces section-aware chunks.
// Rules:
//   - Split at H2/H3 boundaries first.
//   - If a section is still larger than MaxTokens, further split by paragraphs
//     with overlap, respecting code fences (never split inside ```).
//   - Enrich each chunk's Text with Title + SectionPath so embeddings carry
//     context.
func ChunkDoc(doc *Doc, opts ChunkingOpts) []Chunk {
	sections := splitSections(doc.Body)
	var chunks []Chunk
	for _, sec := range sections {
		for _, body := range splitBySize(sec.Text, opts) {
			c := Chunk{
				Doc:          doc,
				SectionPath:  sec.PathString(),
				Heading:      sec.Heading,
				Text:         body,
				StartLine:    sec.StartLine,
				EndLine:      sec.EndLine,
				TokensApprox: approxTokens(body),
			}
			h := sha256.Sum256([]byte(c.Text))
			c.Hash = hex.EncodeToString(h[:])
			chunks = append(chunks, c)
		}
	}
	for i := range chunks {
		chunks[i].ChunkIndex = i
	}
	return chunks
}

// EmbedText composes the string the embedder sees.
func (c Chunk) EmbedText() string {
	var b strings.Builder
	fmt.Fprintf(&b, "title: %s\n", c.Doc.Title)
	if c.Doc.Frontmatter.Status != "" {
		fmt.Fprintf(&b, "status: %s\n", c.Doc.Frontmatter.Status)
	}
	if c.SectionPath != "" {
		fmt.Fprintf(&b, "section: %s\n", c.SectionPath)
	}
	if len(c.Doc.Frontmatter.Tags) > 0 {
		fmt.Fprintf(&b, "tags: %s\n", strings.Join(c.Doc.Frontmatter.Tags, ", "))
	}
	b.WriteString("---\n")
	b.WriteString(c.Text)
	return b.String()
}

// --- internals ---

type section struct {
	Level     int      // 1=H1, 2=H2, etc.
	Heading   string
	Parents   []string
	Text      string
	StartLine int
	EndLine   int
}

func (s section) PathString() string {
	all := append([]string{}, s.Parents...)
	if s.Heading != "" {
		all = append(all, s.Heading)
	}
	return strings.Join(all, " > ")
}

func splitSections(body string) []section {
	lines := strings.Split(body, "\n")
	var sections []section
	var current section
	current.StartLine = 1
	headingStack := []string{} // levels 1..3

	flush := func(endLine int) {
		current.EndLine = endLine
		current.Text = strings.TrimSpace(current.Text)
		if current.Text != "" {
			sections = append(sections, current)
		}
	}

	inCodeFence := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inCodeFence = !inCodeFence
		}
		if !inCodeFence && isHeading(trimmed) {
			// Flush previous
			flush(i)
			level, text := parseHeading(trimmed)
			// Maintain heading stack for parents
			if level-1 > len(headingStack) {
				// Treat as sibling: pad stack
				for len(headingStack) < level-1 {
					headingStack = append(headingStack, "")
				}
			} else {
				headingStack = headingStack[:level-1]
			}
			current = section{
				Level:     level,
				Heading:   text,
				Parents:   append([]string{}, headingStack...),
				StartLine: i + 1,
			}
			headingStack = append(headingStack, text)
			continue
		}
		current.Text += line + "\n"
	}
	flush(len(lines))
	return sections
}

func isHeading(line string) bool {
	return strings.HasPrefix(line, "# ") ||
		strings.HasPrefix(line, "## ") ||
		strings.HasPrefix(line, "### ") ||
		strings.HasPrefix(line, "#### ")
}

func parseHeading(line string) (level int, text string) {
	for i := 0; i < len(line); i++ {
		if line[i] != '#' {
			return i, strings.TrimSpace(line[i:])
		}
	}
	return 0, ""
}

// splitBySize breaks a section's text into chunks that respect
// MaxTokens, with overlap, and never cut inside a code fence.
func splitBySize(text string, opts ChunkingOpts) []string {
	if approxTokens(text) <= opts.MaxTokens {
		return []string{text}
	}
	paragraphs := splitParagraphs(text)
	var chunks []string
	var current strings.Builder
	currentTokens := 0

	flush := func() {
		if current.Len() > 0 {
			t := strings.TrimSpace(current.String())
			if approxTokens(t) >= opts.MinTokens {
				chunks = append(chunks, t)
			} else if len(chunks) > 0 {
				// Merge tiny tail into previous chunk
				chunks[len(chunks)-1] += "\n\n" + t
			} else {
				chunks = append(chunks, t)
			}
		}
	}

	for _, p := range paragraphs {
		pTokens := approxTokens(p)
		if currentTokens+pTokens > opts.MaxTokens && current.Len() > 0 {
			flush()
			// Overlap: take the trailing portion of what we just flushed.
			overlap := tailByTokens(chunks[len(chunks)-1], opts.OverlapTokens)
			current.Reset()
			current.WriteString(overlap)
			current.WriteString("\n\n")
			currentTokens = approxTokens(overlap)
		}
		current.WriteString(p)
		current.WriteString("\n\n")
		currentTokens += pTokens
	}
	flush()
	return chunks
}

// splitParagraphs is code-fence-aware: a fenced block is one paragraph.
func splitParagraphs(text string) []string {
	lines := strings.Split(text, "\n")
	var out []string
	var cur strings.Builder
	inFence := false
	flush := func() {
		s := strings.TrimRight(cur.String(), "\n")
		if s != "" {
			out = append(out, s)
		}
		cur.Reset()
	}
	for _, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "```") {
			inFence = !inFence
			cur.WriteString(l + "\n")
			if !inFence {
				flush()
			}
			continue
		}
		if inFence {
			cur.WriteString(l + "\n")
			continue
		}
		if strings.TrimSpace(l) == "" {
			flush()
			continue
		}
		cur.WriteString(l + "\n")
	}
	flush()
	return out
}

func approxTokens(s string) int {
	return utf8.RuneCountInString(s) / 4
}

func tailByTokens(s string, tokens int) string {
	targetChars := tokens * 4
	if len(s) <= targetChars {
		return s
	}
	// Try to cut on a sentence or paragraph boundary.
	cut := len(s) - targetChars
	if idx := strings.Index(s[cut:], "\n\n"); idx >= 0 {
		return s[cut+idx+2:]
	}
	if idx := strings.Index(s[cut:], ". "); idx >= 0 {
		return s[cut+idx+2:]
	}
	return s[cut:]
}