package server

import (
	"context"
	"errors"
	"testing"

	"ai-chat-service/chat-server/llm"
	"ai-chat-service/chat-server/provider"
	"ai-chat-service/chat-server/retrieval/kvlite"
	"ai-chat-service/pkg/config"
	"ai-chat-service/proto"
)

type fakeVectorStore struct {
	getDocument *kvlite.Document
	getErr      error
	searchHits  []kvlite.Hit
	searchErr   error
	getCalls    int
	searchCalls int
}

func (f *fakeVectorStore) Upsert(context.Context, kvlite.Document) error {
	return errors.New("unexpected Upsert")
}
func (f *fakeVectorStore) Delete(context.Context, string, kvlite.LogicalScope) error {
	return errors.New("unexpected Delete")
}
func (f *fakeVectorStore) Health(context.Context) error { return nil }
func (f *fakeVectorStore) Close() error                 { return nil }
func (f *fakeVectorStore) Get(context.Context, string, kvlite.LogicalScope) (*kvlite.Document, error) {
	f.getCalls++
	return f.getDocument, f.getErr
}
func (f *fakeVectorStore) Search(context.Context, string, kvlite.LogicalScope, int, float64) ([]kvlite.Hit, error) {
	f.searchCalls++
	return f.searchHits, f.searchErr
}

func routeTestConfig() *config.Config {
	cfg := &config.Config{}
	cfg.Chat.Model = "test-model"
	cfg.Retrieval.KVLite.Enabled = true
	cfg.Retrieval.KVLite.TopK = 3
	cfg.Retrieval.KVLite.Threshold = 0.82
	cfg.Retrieval.KVLite.Scope.Tenant = "tenant-a"
	cfg.Retrieval.KVLite.Scope.KnowledgeBase = "help"
	cfg.Retrieval.KVLite.Scope.CorpusVersion = "v1"
	return cfg
}

func routeTestDocument() kvlite.Document {
	return kvlite.Document{
		Question: "如何重置密码",
		Scope:    kvlite.LogicalScope{Tenant: "tenant-a", KnowledgeBase: "help", CorpusVersion: "v1"},
		Answer: kvlite.AnswerEnvelope{
			Version: kvlite.AnswerEnvelopeVersion, DocumentID: "doc-password", Answer: "请在登录页点击忘记密码。", Source: "knowledge_base",
		},
	}
}

func newRouteTestApp(store kvlite.Store) *app {
	return &app{
		config:          routeTestConfig(),
		vectorStore:     store,
		openaiConf:      &openaiConf{Model: "test-model"},
		providerProfile: provider.ProviderProfile{Name: "test-provider"},
	}
}

func TestRouteUsesExactKVLiteHitWithoutSemanticSearch(t *testing.T) {
	doc := routeTestDocument()
	store := &fakeVectorStore{getDocument: &doc}
	decision, err := newRouteTestApp(store).resolveRouteDecision(context.Background(), &proto.ChatCompletionRequest{Message: doc.Question})
	if err != nil {
		t.Fatal(err)
	}
	if !decision.DirectReturnLocal || decision.ShouldCallLLM || decision.Mode != llm.AnswerModeKnowledgeBase || decision.Reason != "kvlite_exact_hit" {
		t.Fatalf("unexpected exact decision: %+v", decision)
	}
	if store.getCalls != 1 || store.searchCalls != 0 {
		t.Fatalf("calls get=%d search=%d, want 1/0", store.getCalls, store.searchCalls)
	}
	if len(decision.RetrievalHits) != 1 || decision.RetrievalHits[0].DocumentID != doc.Answer.DocumentID || decision.RetrievalHits[0].Score != 1 {
		t.Fatalf("unexpected trace hits: %+v", decision.RetrievalHits)
	}
}

func TestRouteUsesHighConfidenceSemanticKVLiteHit(t *testing.T) {
	doc := routeTestDocument()
	store := &fakeVectorStore{searchHits: []kvlite.Hit{{Document: doc, Score: 0.91}}}
	decision, err := newRouteTestApp(store).resolveRouteDecision(context.Background(), &proto.ChatCompletionRequest{Message: "忘记密码怎么办"})
	if err != nil {
		t.Fatal(err)
	}
	if !decision.DirectReturnLocal || decision.ShouldCallLLM || decision.Reason != "kvlite_vector_hit" || decision.RetrievalScore != 0.91 {
		t.Fatalf("unexpected semantic decision: %+v", decision)
	}
	if store.getCalls != 1 || store.searchCalls != 1 {
		t.Fatalf("calls get=%d search=%d, want 1/1", store.getCalls, store.searchCalls)
	}
}

func TestRouteFallsBackToLLMWhenKVLiteFails(t *testing.T) {
	store := &fakeVectorStore{getErr: kvlite.ErrUnavailable}
	decision, err := newRouteTestApp(store).resolveRouteDecision(context.Background(), &proto.ChatCompletionRequest{Message: "任意问题"})
	if err != nil {
		t.Fatal(err)
	}
	if decision.DirectReturnLocal || !decision.ShouldCallLLM || decision.Reason != "kvlite_unavailable" {
		t.Fatalf("unexpected fallback decision: %+v", decision)
	}
	if store.searchCalls != 0 {
		t.Fatalf("search calls = %d, want 0 after failed exact lookup", store.searchCalls)
	}
}

func TestRouteFallsBackToLLMWhenSemanticSearchIsNotReadyOrMalformed(t *testing.T) {
	for name, searchErr := range map[string]error{
		"not_ready": kvlite.ErrNotReady,
		"protocol":  kvlite.ErrProtocol,
	} {
		t.Run(name, func(t *testing.T) {
			store := &fakeVectorStore{searchErr: searchErr}
			decision, err := newRouteTestApp(store).resolveRouteDecision(context.Background(), &proto.ChatCompletionRequest{Message: "任意问题"})
			if err != nil {
				t.Fatal(err)
			}
			wantReason := kvlite.FailureReason(searchErr)
			if decision.DirectReturnLocal || !decision.ShouldCallLLM || decision.Reason != wantReason {
				t.Fatalf("unexpected fallback decision: %+v", decision)
			}
			if store.getCalls != 1 || store.searchCalls != 1 {
				t.Fatalf("calls get=%d search=%d, want 1/1", store.getCalls, store.searchCalls)
			}
		})
	}
}
