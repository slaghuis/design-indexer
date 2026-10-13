package store

import (
	"testing"

	"github.com/qdrant/go-client/qdrant"

	"github.com/slaghuis/design-indexer/internal/parser"
)

func TestChunkPayloadTagsDoNotPanic(t *testing.T) {
	doc := &parser.Doc{
		Source: "central",
		Path:   "adr/0023-nats.md",
		Kind:   "adr",
		Title:  "Use NATS",
		Frontmatter: parser.Frontmatter{
			ID:     "0023",
			Status: "Accepted",
			Tags:   []string{"messaging", "infrastructure"},
		},
		Hash: "abc",
	}
	payload := chunkPayload(parser.Chunk{
		Doc:         doc,
		ChunkIndex:  0,
		SectionPath: "Decision",
		Heading:     "Decision",
		Text:        "Adopt NATS.",
		StartLine:   1,
		EndLine:     4,
	})
	tags := payload["tags"].GetListValue()
	if tags == nil || len(tags.GetValues()) != 2 {
		t.Fatalf("tags = %#v", tags)
	}
	if tags.GetValues()[0].GetStringValue() != "messaging" {
		t.Fatalf("tag0 = %q", tags.GetValues()[0].GetStringValue())
	}

	doc.Frontmatter.Tags = nil
	payload = chunkPayload(parser.Chunk{Doc: doc, Text: "x"})
	if payload["tags"].GetListValue() == nil {
		t.Fatal("nil tags should encode as an empty list")
	}
}

func TestKeywordFieldType(t *testing.T) {
	ft := qdrant.FieldType_FieldTypeKeyword.Enum()
	if *ft != qdrant.FieldType_FieldTypeKeyword {
		t.Fatalf("field type %v", *ft)
	}
}
