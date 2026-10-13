package main

import (
	"context"
	"flag"
	"io/fs"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"golang.org/x/sync/errgroup"
	"gopkg.in/yaml.v3"

	"github.com/slaghuis/design-indexer/internal/embedder"
	"github.com/slaghuis/design-indexer/internal/parser"
	"github.com/slaghuis/design-indexer/internal/sources"
	"github.com/slaghuis/design-indexer/internal/store"
)

type Config struct {
	Sources []sources.Source `yaml:"sources"`
	Qdrant  struct {
		Host       string `yaml:"host"`
		Port       int    `yaml:"port"`
		Collection string `yaml:"collection"`
		Dim        uint64 `yaml:"dim"`
	} `yaml:"qdrant"`
	Ollama struct {
		BaseURL string `yaml:"base_url"`
		Model   string `yaml:"model"`
	} `yaml:"ollama"`
	Chunking struct {
		MaxTokens     int `yaml:"max_tokens"`
		MinTokens     int `yaml:"min_tokens"`
		OverlapTokens int `yaml:"overlap_tokens"`
	} `yaml:"chunking"`
	Concurrency int `yaml:"concurrency"`
}

type textEmbedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
}

type docStore interface {
	ExistingDocHash(ctx context.Context, source, path string) (string, error)
	ChunkCount(ctx context.Context, source, path string) (int, error)
	DeleteDoc(ctx context.Context, source, path string) error
	Upsert(ctx context.Context, c parser.Chunk, vec []float32) error
}

type indexOutcome string

const (
	outcomeIndexed indexOutcome = "indexed"
	outcomeSkipped indexOutcome = "skipped"
	outcomeFailed  indexOutcome = "failed"
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "config path")
	watch := flag.Bool("watch", false, "watch mode")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stdout,
		&slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		die("config: %v", err)
	}
	if cfg.Concurrency == 0 {
		cfg.Concurrency = 4
	}

	emb := embedder.NewOllama(cfg.Ollama.BaseURL, cfg.Ollama.Model)
	st, err := store.New(cfg.Qdrant.Host, cfg.Qdrant.Port,
		cfg.Qdrant.Collection, cfg.Qdrant.Dim)
	if err != nil {
		die("qdrant: %v", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	opts := parser.ChunkingOpts{
		MaxTokens:     cfg.Chunking.MaxTokens,
		MinTokens:     cfg.Chunking.MinTokens,
		OverlapTokens: cfg.Chunking.OverlapTokens,
	}
	if opts.MaxTokens == 0 {
		opts.MaxTokens = 500
	}
	if opts.MinTokens == 0 {
		opts.MinTokens = 50
	}
	if opts.OverlapTokens == 0 {
		opts.OverlapTokens = 60
	}

	start := time.Now()
	totalDocs, totalChunks, skipped, failed := indexAll(ctx, log, cfg, emb, st, opts)
	log.Info("index complete",
		"docs", totalDocs, "chunks", totalChunks,
		"skipped", skipped, "failed", failed, "duration", time.Since(start))

	if *watch {
		if err := watchSources(ctx, log, cfg, emb, st, opts); err != nil {
			die("watch: %v", err)
		}
	}
}

func indexAll(ctx context.Context, log *slog.Logger, cfg *Config,
	emb textEmbedder, st docStore, opts parser.ChunkingOpts) (int, int, int, int) {

	type task struct {
		Source  string
		Root    string
		AbsPath string
		RelPath string
		ADRGlob string
	}
	taskCh := make(chan task, 64)
	var totalDocs, totalChunks, skipped, failed int
	var mu sync.Mutex

	g, gctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		defer close(taskCh)
		for _, src := range cfg.Sources {
			refs, err := sources.Walk(src)
			if err != nil {
				log.Warn("walk failed", "source", src.Name, "err", err)
				continue
			}
			for _, r := range refs {
				select {
				case taskCh <- task{src.Name, src.Root, r.AbsPath, r.RelPath, src.ADRGlob}:
				case <-gctx.Done():
					return gctx.Err()
				}
			}
		}
		return nil
	})

	for i := 0; i < cfg.Concurrency; i++ {
		g.Go(func() error {
			for t := range taskCh {
				doc, err := prepareDoc(t.Source, t.Root, t.AbsPath, t.RelPath, t.ADRGlob)
				if err != nil {
					log.Warn("parse", "path", t.RelPath, "err", err)
					mu.Lock()
					failed++
					mu.Unlock()
					continue
				}
				n, outcome := indexOne(gctx, log, emb, st, doc, opts)
				mu.Lock()
				switch outcome {
				case outcomeSkipped:
					skipped++
				case outcomeIndexed:
					totalDocs++
					totalChunks += n
				default:
					failed++
				}
				mu.Unlock()
				log.Debug("indexed", "path", t.RelPath, "chunks", n, "outcome", outcome)
			}
			return nil
		})
	}

	_ = g.Wait()
	return totalDocs, totalChunks, skipped, failed
}

func prepareDoc(source, root, absPath, relPath, adrGlob string) (*parser.Doc, error) {
	doc, err := parser.ParseFile(source, root, absPath, relPath)
	if err != nil || doc == nil {
		return doc, err
	}
	if adrGlob != "" && sources.Match(relPath, adrGlob) {
		doc.Kind = "adr"
	}
	return doc, nil
}

func indexOne(ctx context.Context, log *slog.Logger, emb textEmbedder, st docStore, doc *parser.Doc, opts parser.ChunkingOpts) (int, indexOutcome) {
	existing, err := st.ExistingDocHash(ctx, doc.Source, doc.Path)
	if err != nil {
		log.Warn("hash lookup", "path", doc.Path, "err", err)
		return 0, outcomeFailed
	}
	chunks := parser.ChunkDoc(doc, opts)
	if existing == doc.Hash {
		n, err := st.ChunkCount(ctx, doc.Source, doc.Path)
		if err != nil {
			log.Warn("count", "path", doc.Path, "err", err)
			return 0, outcomeFailed
		}
		if n == len(chunks) {
			return 0, outcomeSkipped
		}
		log.Info("reindexing partial doc", "path", doc.Path, "stored", n, "expected", len(chunks))
	}

	vecs := make([][]float32, len(chunks))
	for i, c := range chunks {
		vec, err := emb.Embed(ctx, c.EmbedText())
		if err != nil {
			log.Warn("embed", "path", doc.Path, "err", err)
			return 0, outcomeFailed
		}
		vecs[i] = vec
	}

	if err := st.DeleteDoc(ctx, doc.Source, doc.Path); err != nil {
		log.Warn("delete", "path", doc.Path, "err", err)
		return 0, outcomeFailed
	}
	for i, c := range chunks {
		if err := st.Upsert(ctx, c, vecs[i]); err != nil {
			log.Warn("upsert", "path", doc.Path, "err", err)
			if derr := st.DeleteDoc(ctx, doc.Source, doc.Path); derr != nil {
				log.Warn("rollback", "path", doc.Path, "err", derr)
			}
			return 0, outcomeFailed
		}
	}
	return len(chunks), outcomeIndexed
}

func watchSources(ctx context.Context, log *slog.Logger, cfg *Config, emb textEmbedder, st docStore, opts parser.ChunkingOpts) error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer w.Close()

	for _, src := range cfg.Sources {
		if err := addWatchTree(w, src.Root); err != nil {
			log.Warn("watch root", "root", src.Root, "err", err)
		}
	}
	log.Info("watching sources", "count", len(cfg.Sources))

	var (
		timer   *time.Timer
		timerC  <-chan time.Time
		pending = map[string]fsnotify.Op{}
	)
	for {
		select {
		case <-ctx.Done():
			return nil
		case err, ok := <-w.Errors:
			if !ok {
				return nil
			}
			log.Warn("watch", "err", err)
		case ev, ok := <-w.Events:
			if !ok {
				return nil
			}
			if ev.Has(fsnotify.Create) {
				if fi, statErr := os.Stat(ev.Name); statErr == nil && fi.IsDir() && !sources.SkipDir(fi.Name()) {
					if addErr := addWatchTree(w, ev.Name); addErr != nil {
						log.Warn("watch dir", "path", ev.Name, "err", addErr)
					}
				}
			}
			if ev.Has(fsnotify.Write) || ev.Has(fsnotify.Create) || ev.Has(fsnotify.Remove) || ev.Has(fsnotify.Rename) {
				pending[ev.Name] |= ev.Op
				timerC = armTimer(&timer)
			}
		case <-timerC:
			batch := pending
			pending = map[string]fsnotify.Op{}
			timerC = nil
			for path, op := range batch {
				removed := op.Has(fsnotify.Remove) || op.Has(fsnotify.Rename)
				if _, statErr := os.Stat(path); statErr == nil {
					removed = false
				}
				handleWatchPath(ctx, log, cfg, emb, st, opts, path, removed)
			}
		}
	}
}

func armTimer(timer **time.Timer) <-chan time.Time {
	if *timer == nil {
		*timer = time.NewTimer(250 * time.Millisecond)
		return (*timer).C
	}
	if !(*timer).Stop() {
		select {
		case <-(*timer).C:
		default:
		}
	}
	(*timer).Reset(250 * time.Millisecond)
	return (*timer).C
}

func addWatchTree(w *fsnotify.Watcher, root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		if sources.SkipDir(d.Name()) {
			return filepath.SkipDir
		}
		return w.Add(path)
	})
}

func handleWatchPath(ctx context.Context, log *slog.Logger, cfg *Config, emb textEmbedder, st docStore, opts parser.ChunkingOpts, abs string, removed bool) {
	src, rel, ok := sourceForPath(cfg, abs)
	if !ok || !src.Includes(rel) {
		return
	}
	if removed {
		if err := st.DeleteDoc(ctx, src.Name, rel); err != nil {
			log.Warn("watch delete", "path", rel, "err", err)
		}
		return
	}
	if fi, err := os.Stat(abs); err != nil || fi.IsDir() {
		return
	}
	doc, err := prepareDoc(src.Name, src.Root, abs, rel, src.ADRGlob)
	if err != nil {
		log.Warn("watch parse", "path", rel, "err", err)
		return
	}
	n, outcome := indexOne(ctx, log, emb, st, doc, opts)
	log.Info("watch indexed", "path", rel, "chunks", n, "outcome", outcome)
}

func sourceForPath(cfg *Config, abs string) (sources.Source, string, bool) {
	abs = filepath.Clean(abs)
	var best sources.Source
	var rel string
	bestLen := -1
	for _, src := range cfg.Sources {
		root := filepath.Clean(src.Root)
		r, err := filepath.Rel(root, abs)
		if err != nil {
			continue
		}
		r = filepath.ToSlash(r)
		if r == "." || r == ".." || strings.HasPrefix(r, "../") {
			continue
		}
		if len(root) > bestLen {
			best = src
			rel = r
			bestLen = len(root)
		}
	}
	return best, rel, bestLen >= 0
}

func loadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	for i := range c.Sources {
		c.Sources[i].Root = expandHome(c.Sources[i].Root)
	}
	return &c, nil
}

func expandHome(p string) string {
	if len(p) >= 2 && p[:2] == "~/" {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[2:])
	}
	return p
}

func die(f string, a ...any) {
	log.Fatalf(f, a...)
}
