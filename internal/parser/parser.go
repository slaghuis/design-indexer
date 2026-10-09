package parser

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Frontmatter struct {
	ID            string    `yaml:"id"`
	Title         string    `yaml:"title"`
	Status        string    `yaml:"status"`
	Date          string    `yaml:"date"`
	Deciders      []string  `yaml:"deciders"`
	Supersedes    string    `yaml:"supersedes"`
	SupersededBy  string    `yaml:"superseded_by"`
	Tags          []string  `yaml:"tags"`
}

type Doc struct {
	Source      string      // source name (e.g. "central", "myservice")
	Path        string      // relative path within source root
	AbsPath     string
	Kind        string      // "adr" | "doc" | "runbook" | "readme"
	Title       string
	Frontmatter Frontmatter
	Body        string      // markdown without frontmatter
	ModTime     time.Time
	Hash        string      // sha of full file contents
}

func ParseFile(source, root, absPath, relPath string) (*Doc, error) {
	b, err := os.ReadFile(absPath)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return nil, err
	}

	fm, body, err := splitFrontmatter(b)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", relPath, err)
	}

	title := fm.Title
	if title == "" {
		title = extractH1(body)
	}
	if title == "" {
		title = relPath
	}

	kind := classify(relPath, fm, body)

	h := sha256.Sum256(b)

	return &Doc{
		Source:      source,
		Path:        relPath,
		AbsPath:     absPath,
		Kind:        kind,
		Title:       title,
		Frontmatter: fm,
		Body:        string(body),
		ModTime:     info.ModTime(),
		Hash:        hex.EncodeToString(h[:]),
	}, nil
}

var frontmatterDelim = []byte("---\n")

func splitFrontmatter(src []byte) (Frontmatter, []byte, error) {
	if !bytes.HasPrefix(src, frontmatterDelim) {
		return Frontmatter{}, src, nil
	}
	end := bytes.Index(src[4:], frontmatterDelim)
	if end == -1 {
		return Frontmatter{}, src, errors.New("unclosed frontmatter")
	}
	raw := src[4 : 4+end]
	body := src[4+end+4:]

	var fm Frontmatter
	if err := yaml.Unmarshal(raw, &fm); err != nil {
		// Non-fatal: treat as no frontmatter.
		return Frontmatter{}, src, nil
	}
	return fm, body, nil
}

func extractH1(body []byte) string {
	lines := bytes.Split(body, []byte("\n"))
	for _, l := range lines {
		t := bytes.TrimSpace(l)
		if bytes.HasPrefix(t, []byte("# ")) {
			return strings.TrimSpace(string(t[2:]))
		}
		if len(t) > 0 && !bytes.HasPrefix(t, []byte("#")) {
			return ""
		}
	}
	return ""
}

func classify(path string, fm Frontmatter, body []byte) string {
	lp := strings.ToLower(path)
	switch {
	case strings.Contains(lp, "adr/") || strings.HasPrefix(lp, "adr/"),
		isNumberedADR(path):
		return "adr"
	case strings.Contains(lp, "runbook"), strings.Contains(lp, "ops/"):
		return "runbook"
	case strings.HasSuffix(lp, "readme.md"):
		return "readme"
	default:
		return "doc"
	}
}

func isNumberedADR(path string) bool {
	base := path
	if i := strings.LastIndex(path, "/"); i >= 0 {
		base = path[i+1:]
	}
	// Match "0001-foo.md", "0023-bar.md"
	if len(base) < 6 {
		return false
	}
	for i := 0; i < 4; i++ {
		if base[i] < '0' || base[i] > '9' {
			return false
		}
	}
	return base[4] == '-'
}