package main

import (
	"context"
	"flag"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"
	"gopkg.in/yaml.v3"

	"github.com/slaghuis/design-indexer/internal/embedder"
	"github.com/slaghuis/design-indexer/internal/parser"
	"github.com/slaghuis/design-indexer/internal/sources"
	"github.com/slaghuis/design-indexer/internal/store"
)

type Config struct {
	Sources   []sources.Source `yaml:"sources"`
	Qdrant    struct {
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
	totalDocs, totalChunks, skipped := indexAll(ctx, log, cfg, emb, st, opts)
	log.Info("index complete",
		"docs", totalDocs, "chunks", totalChunks,
		"skipped", skipped, "duration", time.Since(start))

	if *watch {
		log.Info("watch mode not implemented in template; add fsnotify similar to indexer/")
		<-ctx.Done()
	}
}

func indexAll(ctx context.Context, log *slog.Logger, cfg *Config,
	emb *embedder.Ollama, st *store.Qdrant, opts parser.ChunkingOpts) (int, int, int) {

	type task struct {
		Source  string
		Root    string
		AbsPath string
		RelPath string
	}
	taskCh := make(chan task, 64)
	var totalDocs, totalChunks, skipped int
	var mu = make(chan struct{}, 1) // simple mutex via channel
	mu <- struct{}{}

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
				case taskCh <- task{src.Name, src.Root, r.AbsPath, r.RelPath}:
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
				doc, err := parser.ParseFile(t.Source, t.Root, t.AbsPath, t.RelPath)
				if err != nil {
					log.Warn("parse", "path", t.RelPath, "err", err)
					continue
				}
				// Hash check
				existing, _ := st.ExistingDocHash(gctx, doc.Source, doc.Path)
				if existing == doc.Hash {
					<-mu
					skipped++
					mu <- struct{}{}
					continue
				}
				// Rebuild: delete old chunks, insert new ones.
				_ = st.DeleteDoc(gctx, doc.Source, doc.Path)
				chunks := parser.ChunkDoc(doc, opts)
				var written int
				for _, c := range chunks {
					vec, err := emb.Embed(gctx, c.EmbedText())
					if err != nil {
						log.Warn("embed", "err", err)
						continue
					}
					if err := st.Upsert(gctx, c, vec); err != nil {
						log.Warn("upsert", "err", err)
						continue
					}
					written++
				}
				<-mu
				totalDocs++
				totalChunks += written
				mu <- struct{}{}
				log.Debug("indexed", "path", t.RelPath, "chunks", written)
			}
			return nil
		})
	}

	_ = g.Wait()
	return totalDocs, totalChunks, skipped
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