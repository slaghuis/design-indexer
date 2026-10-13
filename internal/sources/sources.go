package sources

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Source struct {
	Name            string   `yaml:"name"`
	Root            string   `yaml:"root"`
	IncludePatterns []string `yaml:"include_patterns"`
	ExcludePatterns []string `yaml:"exclude_patterns"`
	ADRGlob         string   `yaml:"adr_glob"`
}

type Config struct {
	Sources []Source `yaml:"sources"`
}

func Load(path string) ([]Source, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var full struct {
		Sources []Source `yaml:"sources"`
	}
	if err := yaml.Unmarshal(b, &full); err != nil {
		return nil, err
	}
	for i := range full.Sources {
		full.Sources[i].Root = expandHome(full.Sources[i].Root)
	}
	return full.Sources, nil
}

type FileRef struct {
	Source  string
	AbsPath string
	RelPath string
}

func Walk(src Source) ([]FileRef, error) {
	var out []FileRef
	err := filepath.WalkDir(src.Root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if SkipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(src.Root, path)
		if err != nil {
			return nil
		}
		if !src.Includes(rel) {
			return nil
		}
		out = append(out, FileRef{Source: src.Name, AbsPath: path, RelPath: rel})
		return nil
	})
	return out, err
}

// Includes reports whether rel matches this source's include patterns and
// none of its exclude patterns. Patterns use slash separators and support **.
func (s Source) Includes(rel string) bool {
	rel = filepath.ToSlash(rel)
	if !matchAny(rel, s.IncludePatterns, true) {
		return false
	}
	return !matchAny(rel, s.ExcludePatterns, false)
}

// Match reports whether path matches a slash-separated glob. ** matches
// across directories, including zero directories.
func Match(path, pattern string) bool {
	path = strings.Trim(filepath.ToSlash(path), "/")
	pattern = strings.Trim(filepath.ToSlash(pattern), "/")
	return matchSegs(splitSegs(path), splitSegs(pattern))
}

func matchAny(path string, patterns []string, defaultIfEmpty bool) bool {
	if len(patterns) == 0 {
		return defaultIfEmpty
	}
	for _, p := range patterns {
		if Match(path, p) {
			return true
		}
	}
	return false
}

func splitSegs(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "/")
}

func matchSegs(path, pat []string) bool {
	for {
		if len(pat) == 0 {
			return len(path) == 0
		}
		if pat[0] == "**" {
			for len(pat) > 1 && pat[1] == "**" {
				pat = pat[1:]
			}
			if matchSegs(path, pat[1:]) {
				return true
			}
			if len(path) == 0 {
				return false
			}
			path = path[1:]
			continue
		}
		if len(path) == 0 {
			return false
		}
		ok, err := filepath.Match(pat[0], path[0])
		if err != nil || !ok {
			return false
		}
		path = path[1:]
		pat = pat[1:]
	}
}

// SkipDir reports directories the walker never descends into.
func SkipDir(name string) bool {
	switch name {
	case ".git", "node_modules", "vendor", "dist", "bin", ".idea", ".vscode":
		return true
	}
	return false
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[2:])
	}
	return p
}
