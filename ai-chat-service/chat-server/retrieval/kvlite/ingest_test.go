package kvlite

import (
	"context"
	"errors"
	"testing"
)

type fakeIngestionStore struct {
	existing *Document
	upserts  int
}

func (f *fakeIngestionStore) Upsert(context.Context, Document) error { f.upserts++; return nil }
func (f *fakeIngestionStore) Get(context.Context, string, LogicalScope) (*Document, error) {
	return f.existing, nil
}
func (f *fakeIngestionStore) Search(context.Context, string, LogicalScope, int, float64) ([]Hit, error) {
	return nil, nil
}
func (f *fakeIngestionStore) Delete(context.Context, string, LogicalScope) error { return nil }
func (f *fakeIngestionStore) Health(context.Context) error                       { return nil }
func (f *fakeIngestionStore) Close() error                                       { return nil }

func TestControlledWriterRequiresApprovalAndDeduplicates(t *testing.T) {
	doc := testDocument()
	store := &fakeIngestionStore{}
	writer, err := NewControlledWriter(store)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.UpsertApproved(context.Background(), ApprovedDocument{Document: doc}); !errors.Is(err, ErrIngestionNotApproved) {
		t.Fatalf("missing approval error = %v", err)
	}
	if err := writer.UpsertApproved(context.Background(), ApprovedDocument{Document: doc, ApprovalID: "review-42"}); err != nil {
		t.Fatalf("approved write: %v", err)
	}
	if store.upserts != 1 {
		t.Fatalf("upserts = %d, want 1", store.upserts)
	}
	store.existing = &doc
	if err := writer.UpsertApproved(context.Background(), ApprovedDocument{Document: doc, ApprovalID: "review-42"}); err != nil {
		t.Fatalf("idempotent write: %v", err)
	}
	if store.upserts != 1 {
		t.Fatalf("duplicate upserts = %d, want 1", store.upserts)
	}
	conflict := doc
	conflict.Answer.DocumentID = "doc-other"
	if err := writer.UpsertApproved(context.Background(), ApprovedDocument{Document: conflict, ApprovalID: "review-43"}); !errors.Is(err, ErrDocumentConflict) {
		t.Fatalf("conflict error = %v", err)
	}
}
