package kvlite

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

var (
	ErrIngestionNotApproved = errors.New("KVLite knowledge ingestion is not approved")
	ErrDocumentConflict     = errors.New("KVLite knowledge document conflicts with existing question")
)

// ApprovedDocument is the only first-party write path for the knowledge base.
// It deliberately does not accept a generated chat answer without an explicit
// approval identifier supplied by an operator or a future authenticated task.
type ApprovedDocument struct {
	Document
	ApprovalID string
}

type ControlledWriter interface {
	UpsertApproved(ctx context.Context, document ApprovedDocument) error
}

type controlledWriter struct{ store Store }

func NewControlledWriter(store Store) (ControlledWriter, error) {
	if store == nil {
		return nil, fmt.Errorf("nil KVLite vector store")
	}
	return &controlledWriter{store: store}, nil
}

func (w *controlledWriter) UpsertApproved(ctx context.Context, approved ApprovedDocument) error {
	if w == nil || w.store == nil {
		return fmt.Errorf("nil KVLite controlled writer")
	}
	approved.ApprovalID = strings.TrimSpace(approved.ApprovalID)
	if approved.ApprovalID == "" {
		return ErrIngestionNotApproved
	}
	if approved.Answer.Source != "knowledge_base" || approved.Answer.CreatedAt <= 0 {
		return ErrIngestionNotApproved
	}
	existing, err := w.store.Get(ctx, approved.Question, approved.Scope)
	if err != nil {
		return err
	}
	if existing != nil {
		if existing.Answer.DocumentID == approved.Answer.DocumentID {
			return nil
		}
		return ErrDocumentConflict
	}
	return w.store.Upsert(ctx, approved.Document)
}
