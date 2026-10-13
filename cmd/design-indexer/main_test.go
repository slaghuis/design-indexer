package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/slaghuis/design-indexer/internal/parser"
	"github.com/slaghuis/design-indexer/internal/sources"
)

type fakeEmb struct {
	failOn int
	calls  int
}

func (f *fakeEmb) Embed(ctx context.Context, text string) ([]float32, error) {
	f.calls++
	if f.failOn > 0 && f.calls == f.failOn {
		return nil, errors.New("embed failed")
	}
	return []float32{0.1, 0.2}, nil
}

type fakeStore struct {
	hash         string
	count        int
	deletes      int
	upserts      int
	failUpsertOn int
}

func (f *fakeStore) ExistingDocHash(ctx context.Context, source, path string) (string, error) {
	return f.hash, nil
}

func (f *fakeStore) ChunkCount(ctx context.Context, source, path string) (int, error) {
	return f.count, nil
}

func (f *fakeStore) DeleteDoc(ctx context.Context, source, path string) error {
	f.deletes++
	f.upserts = 0
	f.hash = ""
	f.count = 0
	return nil
}

func (f *fakeStore) Upsert(ctx context.Context, c parser.Chunk, vec []float32) error {
	next := f.upserts + 1
	if f.failUpsertOn > 0 && next == f.failUpsertOn {
		return errors.New("upsert failed")
	}
	f.upserts = next
	f.hash = c.Doc.Hash
	f.count = next
	return nil
}

func testDoc() *parser.Doc {
	body := strings.Repeat("a", 80) + "\n\n" + strings.Repeat("b", 80)
	return &parser.Doc{
		Source: "myservice",
		Path:   "ADR/0001-kafka.md",
		Title:  "Kafka",
		Kind:   "doc",
		Body:   body,
		Hash:   "hash-1",
	}
}

func testOpts() parser.ChunkingOpts {
	return parser.ChunkingOpts{MaxTokens: 5, MinTokens: 1, OverlapTokens: 0}
}

func TestIndexOneEmbedFailureKeepsPrevious(t *testing.T) {
	doc := testDoc()
	st := &fakeStore{hash: "old", count: 2}
	emb := &fakeEmb{failOn: 2}
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	_, outcome := indexOne(context.Background(), log, emb, st, doc, testOpts())
	if outcome != outcomeFailed {
		t.Fatalf("outcome %s", outcome)
	}
	if st.deletes != 0 {
		t.Fatalf("deleted previous doc %d times", st.deletes)
	}
	if st.hash != "old" {
		t.Fatalf("hash changed to %q", st.hash)
	}
}

func TestIndexOneUpsertFailureRollsBack(t *testing.T) {
	doc := testDoc()
	st := &fakeStore{failUpsertOn: 2}
	emb := &fakeEmb{}
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	_, outcome := indexOne(context.Background(), log, emb, st, doc, testOpts())
	if outcome != outcomeFailed {
		t.Fatalf("outcome %s", outcome)
	}
	if st.upserts != 0 || st.hash != "" {
		t.Fatalf("partial points remain upserts=%d hash=%q", st.upserts, st.hash)
	}
	if st.deletes < 2 {
		t.Fatalf("deletes = %d, want rollback", st.deletes)
	}
}

func TestIndexOneHealsPartialDoc(t *testing.T) {
	doc := testDoc()
	st := &fakeStore{hash: doc.Hash, count: 1}
	emb := &fakeEmb{}
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	n, outcome := indexOne(context.Background(), log, emb, st, doc, testOpts())
	if outcome != outcomeIndexed {
		t.Fatalf("outcome %s", outcome)
	}
	if n < 2 || st.count != n {
		t.Fatalf("wrote %d stored %d", n, st.count)
	}
}

func TestIndexOneSkipsCompleteDoc(t *testing.T) {
	doc := testDoc()
	chunks := parser.ChunkDoc(doc, testOpts())
	st := &fakeStore{hash: doc.Hash, count: len(chunks)}
	emb := &fakeEmb{}
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	_, outcome := indexOne(context.Background(), log, emb, st, doc, testOpts())
	if outcome != outcomeSkipped {
		t.Fatalf("outcome %s", outcome)
	}
	if emb.calls != 0 || st.deletes != 0 {
		t.Fatalf("calls %d deletes %d", emb.calls, st.deletes)
	}
}

func TestPrepareDocHonorsADRGlob(t *testing.T) {
	dir := t.TempDir()
	rel := "notes/decision.md"
	abs := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte("# A decision\n\nUse the queue.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	doc, err := prepareDoc("svc", dir, abs, rel, "")
	if err != nil {
		t.Fatal(err)
	}
	if doc.Kind == "adr" {
		t.Fatalf("kind %s without glob", doc.Kind)
	}
	doc, err = prepareDoc("svc", dir, abs, rel, "notes/**/*.md")
	if err != nil {
		t.Fatal(err)
	}
	if doc.Kind != "adr" {
		t.Fatalf("kind %s", doc.Kind)
	}
}

func TestSourceForPathPicksLongestRoot(t *testing.T) {
	cfg := &Config{Sources: []sources.Source{
		{Name: "central", Root: "/docs"},
		{Name: "pay", Root: "/services/payments"},
		{Name: "services", Root: "/services"},
	}}
	src, rel, ok := sourceForPath(cfg, "/services/payments/ADR/0001-kafka.md")
	if !ok || src.Name != "pay" || rel != "ADR/0001-kafka.md" {
		t.Fatalf("got name=%s rel=%s ok=%v", src.Name, rel, ok)
	}
}
