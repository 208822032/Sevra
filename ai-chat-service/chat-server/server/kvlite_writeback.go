package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"ai-chat-service/chat-server/retrieval/kvlite"
)

// autoWriteback writes a successful LLM answer into the knowledge KVLite only
// after retrieval explicitly reported a miss. It is asynchronous and therefore
// never delays the response sent to the user.
func (a *app) autoWriteback(ctx context.Context, question, answer string) {
	if a == nil || a.vectorStore == nil || a.config == nil || !a.config.Retrieval.KVLite.AutoWriteback {
		return
	}
	if a.routeDecision.Reason != "kvlite_no_hit" || a.routeDecision.DirectReturnLocal {
		return
	}
	question = strings.TrimSpace(question)
	answer = strings.TrimSpace(answer)
	if question == "" || answer == "" || !writebackAnswerEligible(answer) {
		a.log.InfoF("KVLite auto writeback skipped reason=answer_not_eligible")
		return
	}
	scope, _, _, err := a.kvliteRetrievalConfig()
	if err != nil {
		a.log.Error(err)
		return
	}
	digest := sha256.Sum256([]byte(question))
	documentID := "auto-" + hex.EncodeToString(digest[:12])
	writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	doc := kvlite.Document{
		Question: question,
		Scope:    scope,
		Answer: kvlite.AnswerEnvelope{
			Version:    kvlite.AnswerEnvelopeVersion,
			DocumentID: documentID,
			Answer:     answer,
			Source:     "llm_auto",
			CreatedAt:  time.Now().Unix(),
		},
	}
	if err := a.vectorStore.Upsert(writeCtx, doc); err != nil {
		a.log.WarningF("KVLite auto writeback failed document_id=%s err=%v", documentID, err)
		return
	}
	a.log.InfoF("KVLite auto writeback succeeded document_id=%s question=%q", documentID, question)
}

func writebackAnswerEligible(answer string) bool {
	if len([]rune(answer)) < 8 {
		return false
	}
	lower := strings.ToLower(answer)
	for _, marker := range []string{"请告诉我", "请说明", "请提供更多", "我不知道", "无法确定"} {
		if strings.Contains(lower, strings.ToLower(marker)) {
			return false
		}
	}
	return true
}
