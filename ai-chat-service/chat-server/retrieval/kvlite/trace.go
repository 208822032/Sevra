package kvlite

import (
	"errors"

	"ai-chat-service/chat-server/llm"
)

// AppendHitsToTrace preserves the complete retrieval identity needed by the
// response metadata. Routing owns the decision to call this helper; transport
// failures are never converted into synthetic hits here.
func AppendHitsToTrace(trace *llm.AnswerTrace, hits []Hit) error {
	if trace == nil {
		return errors.New("nil answer trace")
	}
	for _, hit := range hits {
		encodedScope, err := EncodeScope(hit.Scope)
		if err != nil {
			return err
		}
		trace.RetrievedHits = append(trace.RetrievedHits, llm.RetrievalHit{
			RecordID:        hit.Answer.DocumentID,
			DocumentID:      hit.Answer.DocumentID,
			Score:           hit.Score,
			Scope:           encodedScope,
			HitType:         "kvlite_vector",
			QuestionPreview: llm.PreviewText(hit.Question, 128),
			AnswerPreview:   llm.PreviewText(hit.Answer.Answer, 256),
			SourceType:      llm.SourceTypeRetrieval,
			Source:          hit.Answer.Source,
		})
	}
	return nil
}

// FailureReason gives the routing layer a stable, non-answer reason to place
// in a trace or metric before it chooses whether to fall back to an LLM.
func FailureReason(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrNotReady):
		return "kvlite_not_ready"
	case errors.Is(err, ErrProtocol):
		return "kvlite_protocol_error"
	case errors.Is(err, ErrUnavailable):
		return "kvlite_unavailable"
	default:
		return "kvlite_request_error"
	}
}
