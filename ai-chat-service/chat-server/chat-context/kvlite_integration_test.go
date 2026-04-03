package chat_context

import (
	"ai-chat-service/pkg/config"
	"context"
	"os"
	"testing"
	"time"

	"github.com/sashabaranov/go-openai"
)

func newIntegrationStore(t *testing.T) SessionStore {
	t.Helper()
	if os.Getenv("RUN_KVLITE_INTEGRATION") != "1" {
		t.Skip("integration disabled")
	}
	addr := os.Getenv("KVLITE_ADDR")
	if addr == "" {
		t.Skip("KVLITE_ADDR not provided")
	}
	namespace := os.Getenv("KVLITE_NAMESPACE")
	if namespace == "" {
		namespace = "ai:chat"
	}
	cnf := &config.Config{}
	cnf.Context.KVLite.Addr = addr
	cnf.Context.KVLite.TimeoutMS = int((2 * time.Second) / time.Millisecond)
	cnf.Context.KVLite.ConnectTimeoutMS = int((2 * time.Second) / time.Millisecond)
	cnf.Context.KVLite.ReadTimeoutMS = int((2 * time.Second) / time.Millisecond)
	cnf.Context.KVLite.WriteTimeoutMS = int((2 * time.Second) / time.Millisecond)
	return newSessionStore(newKVLiteRawBackend(cnf, namespace, nil), namespace)
}

func TestKVLiteContext_EndToEndWithRealServer(t *testing.T) {
	store := newIntegrationStore(t)
	defer store.Close()

	sessionID, err := store.ResolveSession(context.Background(), "", "msg-real-1", "")
	if err != nil {
		t.Fatalf("resolve session: %v", err)
	}
	turn := &TurnRecord{
		SessionID:        sessionID,
		RequestMessageID: "msg-real-1",
		ParentMessageID:  "pid-real-0",
		UserMessage:      openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser, Content: "hello kvlite"},
		UserTokens:       2,
		TotalTokens:      2,
	}
	seq, err := store.AllocateTurnSeq(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("allocate seq: %v", err)
	}
	turn.Seq = seq
	if err := store.WriteOpenTurn(context.Background(), turn, 5*time.Second); err != nil {
		t.Fatalf("write open turn: %v", err)
	}
	if err := store.CompleteTurn(context.Background(), sessionID, seq, "assistant-real-1", openai.ChatCompletionMessage{Role: openai.ChatMessageRoleAssistant, Content: "world"}, 3, 5*time.Second); err != nil {
		t.Fatalf("complete turn: %v", err)
	}
	turns, err := store.LoadRecentTurns(context.Background(), sessionID, 1)
	if err != nil {
		t.Fatalf("load turns: %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("unexpected turns length: %d", len(turns))
	}
	if turns[0].UserMessage.Content != "hello kvlite" || turns[0].AssistantMessage.Content != "world" {
		t.Fatalf("unexpected turn content: %+v", turns[0])
	}
}

func TestKVLiteContext_EndToEndFlow(t *testing.T) {
	store := newIntegrationStore(t)
	defer store.Close()

	turn1SessionID, err := store.ResolveSession(context.Background(), "", "msg-flow-1", "")
	if err != nil {
		t.Fatalf("resolve session turn1: %v", err)
	}
	turn1 := &TurnRecord{
		SessionID:        turn1SessionID,
		RequestMessageID: "msg-flow-1",
		ParentMessageID:  "pid-flow-0",
		UserMessage:      openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser, Content: "what is kvlite"},
		UserTokens:       3,
		TotalTokens:      3,
	}
	seq1, err := store.AllocateTurnSeq(context.Background(), turn1SessionID)
	if err != nil {
		t.Fatalf("allocate seq turn1: %v", err)
	}
	turn1.Seq = seq1
	if err := store.WriteOpenTurn(context.Background(), turn1, 5*time.Second); err != nil {
		t.Fatalf("write turn1: %v", err)
	}
	if err := store.CompleteTurn(context.Background(), turn1SessionID, seq1, "assistant-flow-1", openai.ChatCompletionMessage{Role: openai.ChatMessageRoleAssistant, Content: "a kv engine"}, 4, 5*time.Second); err != nil {
		t.Fatalf("complete turn1: %v", err)
	}

	turn2SessionID, err := store.ResolveSession(context.Background(), "", "msg-flow-2", "assistant-flow-1")
	if err != nil {
		t.Fatalf("resolve session turn2: %v", err)
	}
	if turn2SessionID != turn1SessionID {
		t.Fatalf("expected same session, got %s and %s", turn1SessionID, turn2SessionID)
	}
	turn2 := &TurnRecord{
		SessionID:        turn2SessionID,
		RequestMessageID: "msg-flow-2",
		ParentMessageID:  "assistant-flow-1",
		UserMessage:      openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser, Content: "how do turns work"},
		UserTokens:       3,
		TotalTokens:      3,
	}
	seq2, err := store.AllocateTurnSeq(context.Background(), turn2SessionID)
	if err != nil {
		t.Fatalf("allocate seq turn2: %v", err)
	}
	turn2.Seq = seq2
	if err := store.WriteOpenTurn(context.Background(), turn2, 5*time.Second); err != nil {
		t.Fatalf("write turn2: %v", err)
	}
	if err := store.CompleteTurn(context.Background(), turn2SessionID, seq2, "assistant-flow-2", openai.ChatCompletionMessage{Role: openai.ChatMessageRoleAssistant, Content: "by seq order"}, 4, 5*time.Second); err != nil {
		t.Fatalf("complete turn2: %v", err)
	}

	turns, err := store.LoadRecentTurns(context.Background(), turn1SessionID, 2)
	if err != nil {
		t.Fatalf("load recent turns: %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("expected 2 turns, got %d", len(turns))
	}
	if turns[0].Seq != seq1 || turns[1].Seq != seq2 {
		t.Fatalf("unexpected turn order: %+v", turns)
	}
	if turns[0].UserMessage.Content != "what is kvlite" || turns[1].AssistantMessage.Content != "by seq order" {
		t.Fatalf("unexpected turn content: %+v", turns)
	}
}
