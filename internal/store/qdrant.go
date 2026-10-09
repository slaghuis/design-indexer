package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/qdrant/go-client/qdrant"

	"github.com/slaghuis/design-indexer/internal/parser"
)

type Qdrant struct {
	client     *qdrant.Client
	collection string
	dim        uint64
}

func New(host string, port int, collection string, dim uint64) (*Qdrant, error) {
	c, err := qdrant.NewClient(&qdrant.Config{Host: host, Port: port})
	if err != nil {
		return nil, err
	}
	q := &Qdrant{client: c, collection: collection, dim: dim}
	if err := q.ensureCollection(context.Background()); err != nil {
		return nil, err
	}
	return q, nil
}

func (q *Qdrant) ensureCollection(ctx context.Context) error {
	exists, err := q.client.CollectionExists(ctx, q.collection)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	if err := q.client.CreateCollection(ctx, &qdrant.CreateCollection{
		CollectionName: q.collection,
		VectorsConfig: qdrant.NewVectorsConfig(&qdrant.VectorParams{
			Size:     q.dim,
			Distance: qdrant.Distance_Cosine,
		}),
	}); err != nil {
		return err
	}
	// Field indexes for fast filtering
	for _, f := range []string{"source", "kind", "adr_id", "status"} {
		_ = q.client.CreateFieldIndex(ctx, &qdrant.CreateFieldIndexCollection{
			CollectionName: q.collection,
			FieldName:      f,
			FieldType:      qdrant.NewFieldTypeKeyword(),
		})
	}
	return nil
}

func pointID(source, path string, chunkIndex int) string {
	h := sha256.Sum256([]byte(source + "::" + path + "::" + fmt.Sprintf("%d", chunkIndex)))
	hx := hex.EncodeToString(h[:16])
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		hx[0:8], hx[8:12], hx[12:16], hx[16:20], hx[20:32])
}

func (q *Qdrant) Upsert(ctx context.Context, c parser.Chunk, vec []float32) error {
	d := c.Doc
	payload := qdrant.NewValueMap(map[string]any{
		"source":        d.Source,
		"path":          d.Path,
		"kind":          d.Kind,
		"title":         d.Title,
		"adr_id":        d.Frontmatter.ID,
		"status":        d.Frontmatter.Status,
		"tags":          d.Frontmatter.Tags,
		"section_path":  c.SectionPath,
		"heading":       c.Heading,
		"text":          c.Text,
		"start_line":    c.StartLine,
		"end_line":      c.EndLine,
		"chunk_index":   c.ChunkIndex,
		"hash":          c.Hash,
		"doc_hash":      d.Hash,
	})

	_, err := q.client.Upsert(ctx, &qdrant.UpsertPoints{
		CollectionName: q.collection,
		Points: []*qdrant.PointStruct{
			{
				Id:      qdrant.NewIDUUID(pointID(d.Source, d.Path, c.ChunkIndex)),
				Vectors: qdrant.NewVectors(vec...),
				Payload: payload,
			},
		},
	})
	return err
}

// ExistingDocHash returns the stored doc_hash for any chunk of this doc,
// or empty string if the doc is not indexed.
func (q *Qdrant) ExistingDocHash(ctx context.Context, source, path string) (string, error) {
	limit := uint32(1)
	resp, err := q.client.Scroll(ctx, &qdrant.ScrollPoints{
		CollectionName: q.collection,
		Filter: &qdrant.Filter{
			Must: []*qdrant.Condition{
				qdrant.NewMatch("source", source),
				qdrant.NewMatch("path", path),
			},
		},
		Limit:       &limit,
		WithPayload: qdrant.NewWithPayload(true),
	})
	if err != nil || len(resp) == 0 {
		return "", err
	}
	if v, ok := resp[0].Payload["doc_hash"]; ok {
		return v.GetStringValue(), nil
	}
	return "", nil
}

// DeleteDoc removes all chunks for a document.
func (q *Qdrant) DeleteDoc(ctx context.Context, source, path string) error {
	_, err := q.client.Delete(ctx, &qdrant.DeletePoints{
		CollectionName: q.collection,
		Points: qdrant.NewPointsSelectorFilter(&qdrant.Filter{
			Must: []*qdrant.Condition{
				qdrant.NewMatch("source", source),
				qdrant.NewMatch("path", path),
			},
		}),
	})
	return err
}