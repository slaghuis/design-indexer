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
			if shouldSkipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(src.Root, path)
		if err != nil {
			return nil
		}
		if !matchAny(rel, src.IncludePatterns, true) {
			return nil
		}
		if matchAny(rel, src.ExcludePatterns, false) {
			return nil
		}
		out = append(out, FileRef{Source: src.Name, AbsPath: path, RelPath: rel})
		return nil
	})
	return out, err
}

func matchAny(path string, patterns []string, defaultIfEmpty bool) bool {
	if len(patterns) == 0 {
		return defaultIfEmpty
	}
	for _, p := range patterns {
		ok, _ := filepath.Match(p, path)
		if ok {
			return true
		}
		// support **/ prefix as substring match
		if strings.Contains(p, "**/") {
			if strings.HasSuffix(path, strings.TrimPrefix(p, "**/")) {
				return true
			}
		}
	}
	return false
}

func shouldSkipDir(name string) bool {
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