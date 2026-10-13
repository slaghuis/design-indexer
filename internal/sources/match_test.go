package sources

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMatchConfigPatterns(t *testing.T) {
	cases := []struct {
		path, pattern string
		want          bool
	}{
		{"README.md", "**/*.md", true},
		{"docs/runbook.md", "**/*.md", true},
		{"docs/ops/deploy.md", "**/*.md", true},
		{"ADR/0001-kafka.md", "**/*.md", true},
		{"ADR/0001-kafka.md", "ADR/**/*.md", true},
		{"adr/0023-nats.md", "adr/**/*.md", true},
		{"docs/runbook.md", "docs/**/*.md", true},
		{"docs/ops/deploy.md", "docs/**/*.md", true},
		{"README.md", "README.md", true},
		{"docs/README.md", "README.md", false},
		{"foo/node_modules/a.md", "**/node_modules/**", true},
		{"node_modules/a.md", "**/node_modules/**", true},
		{"README.md", "**/node_modules/**", false},
		{"notes/ignore.txt", "**/*.md", false},
	}
	for _, tc := range cases {
		if got := Match(tc.path, tc.pattern); got != tc.want {
			t.Errorf("Match(%q, %q) = %v, want %v", tc.path, tc.pattern, got, tc.want)
		}
	}
}

func TestWalkServicePatterns(t *testing.T) {
	root := t.TempDir()
	files := []string{
		"README.md",
		"docs/runbook.md",
		"docs/ops/deploy.md",
		"ADR/0001-kafka.md",
		"notes/ignore.txt",
		"node_modules/pkg/README.md",
	}
	for _, f := range files {
		p := filepath.Join(root, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("# x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	refs, err := Walk(Source{
		Name: "myservice",
		Root: root,
		IncludePatterns: []string{
			"README.md",
			"docs/**/*.md",
			"ADR/**/*.md",
		},
		ExcludePatterns: []string{"**/node_modules/**"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, r := range refs {
		got[filepath.ToSlash(r.RelPath)] = true
	}
	for _, want := range []string{"README.md", "docs/runbook.md", "docs/ops/deploy.md", "ADR/0001-kafka.md"} {
		if !got[want] {
			t.Errorf("missing %s in %v", want, got)
		}
	}
	if got["notes/ignore.txt"] || got["node_modules/pkg/README.md"] {
		t.Fatalf("unexpected files: %v", got)
	}
}
