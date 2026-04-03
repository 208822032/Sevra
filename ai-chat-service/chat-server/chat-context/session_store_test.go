package chat_context

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"ai-chat-service/pkg/config"

	"github.com/sashabaranov/go-openai"
)

type failingRawBackend struct{}

func (f failingRawBackend) Name() string { return "failing" }
func (f failingRawBackend) Close() error { return nil }
func (f failingRawBackend) RGet(ctx context.Context, key string) (string, error) {
	return "", errors.New("backend down")
}
func (f failingRawBackend) RSet(ctx context.Context, key, value string, ttl time.Duration) error {
	return errors.New("backend down")
}
func (f failingRawBackend) HGet(ctx context.Context, key string) (string, error) {
	return "", errors.New("backend down")
}
func (f failingRawBackend) HSet(ctx context.Context, key, value string, ttl time.Duration) error {
	return errors.New("backend down")
}
func (f failingRawBackend) TTL(ctx context.Context, key string) (time.Duration, error) {
	return 0, errors.New("backend down")
}
func (f failingRawBackend) Del(ctx context.Context, key string) error {
	return errors.New("backend down")
}

func buildCompletedTurn(t *testing.T, store SessionStore, sessionID string, requestID, parentID, userText, assistantText string, ttl time.Duration) *TurnRecord {
	t.Helper()
	turn := &TurnRecord{
		SessionID:        sessionID,
		RequestMessageID: requestID,
		ParentMessageID:  parentID,
		UserMessage: openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleUser,
			Content: userText,
		},
		UserTokens:  3,
		TotalTokens: 3,
	}
	seq, err := store.AllocateTurnSeq(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("allocate seq: %v", err)
	}
	turn.Seq = seq
	if err := store.WriteOpenTurn(context.Background(), turn, ttl); err != nil {
		t.Fatalf("write open turn: %v", err)
	}
	assistantMsg := openai.ChatCompletionMessage{Role: openai.ChatMessageRoleAssistant, Content: assistantText}
	if err := store.CompleteTurn(context.Background(), sessionID, seq, "assistant-"+requestID, assistantMsg, 5, ttl); err != nil {
		t.Fatalf("complete turn: %v", err)
	}
	turn.AssistantMessage = assistantMsg
	turn.AssistantMessageID = "assistant-" + requestID
	turn.AssistantTokens = 5
	turn.TotalTokens = 8
	turn.TurnStatus = TurnStatusCompleted
	return turn
}

func TestKVLiteContext_SaveAndLoadRecentTurns(t *testing.T) {
	raw := newMemoryRawBackend("memory")
	store := newSessionStore(raw, "ai:chat")
	sessionID := "sess-1"
	turn1 := buildCompletedTurn(t, store, sessionID, "msg-1", "pid-0", "hello", "world", 10*time.Second)
	turn2 := buildCompletedTurn(t, store, sessionID, "msg-2", "assistant-msg-1", "how are you", "fine", 10*time.Second)

	turns, err := store.LoadRecentTurns(context.Background(), sessionID, 2)
	if err != nil {
		t.Fatalf("load recent turns: %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("unexpected turns length: %d", len(turns))
	}
	if turns[0].Seq != turn1.Seq || turns[1].Seq != turn2.Seq {
		t.Fatalf("turn order mismatch: %+v", turns)
	}
	if turns[0].UserMessage.Content != "hello" || turns[1].AssistantMessage.Content != "fine" {
		t.Fatalf("unexpected turn contents: %+v", turns)
	}
}

func TestKVLiteContext_MetaUpdate(t *testing.T) {
	raw := newMemoryRawBackend("memory")
	store := newSessionStore(raw, "ai:chat")
	sessionID := "sess-meta"
	turn := &TurnRecord{
		SessionID:        sessionID,
		RequestMessageID: "msg-meta-1",
		ParentMessageID:  "pid-meta-0",
		UserMessage: openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleUser,
			Content: "meta test",
		},
		UserTokens: 2,
	}
	seq, err := store.AllocateTurnSeq(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("allocate seq: %v", err)
	}
	turn.Seq = seq
	if err := store.WriteOpenTurn(context.Background(), turn, 20*time.Second); err != nil {
		t.Fatalf("write open turn: %v", err)
	}
	metaStr, ok := raw.dump(metaKey("ai:chat", sessionID))
	if !ok {
		t.Fatalf("meta not found")
	}
	meta := &SessionMeta{}
	if err := json.Unmarshal([]byte(metaStr), meta); err != nil {
		t.Fatalf("unmarshal meta: %v", err)
	}
	if meta.LastSeq != 1 || meta.TurnCount != 1 || meta.SessionID != sessionID || meta.TTLSeconds != 20 {
		t.Fatalf("unexpected meta after begin: %+v", meta)
	}
	assistantMsg := openai.ChatCompletionMessage{Role: openai.ChatMessageRoleAssistant, Content: "ok"}
	if err := store.CompleteTurn(context.Background(), sessionID, seq, "assistant-msg-meta-1", assistantMsg, 4, 20*time.Second); err != nil {
		t.Fatalf("complete turn: %v", err)
	}
	metaStr, ok = raw.dump(metaKey("ai:chat", sessionID))
	if !ok {
		t.Fatalf("meta missing after complete")
	}
	if err := json.Unmarshal([]byte(metaStr), meta); err != nil {
		t.Fatalf("unmarshal meta after complete: %v", err)
	}
	if meta.LastMessageID != "assistant-msg-meta-1" || meta.TurnCount != 1 || meta.LastSeq != seq {
		t.Fatalf("unexpected meta after complete: %+v", meta)
	}
}

func TestKVLiteContext_TTLExpire(t *testing.T) {
	raw := newMemoryRawBackend("memory")
	store := newSessionStore(raw, "ai:chat")
	sessionID := "sess-ttl"
	buildCompletedTurn(t, store, sessionID, "msg-ttl-1", "pid-ttl-0", "ttl", "ok", 1*time.Second)
	time.Sleep(2 * time.Second)
	turns, err := store.LoadRecentTurns(context.Background(), sessionID, 1)
	if err != nil {
		t.Fatalf("load recent turns: %v", err)
	}
	if len(turns) != 0 {
		t.Fatalf("expected expired turns to be hidden, got %+v", turns)
	}
	if _, ok := raw.dump(metaKey("ai:chat", sessionID)); ok {
		t.Fatalf("expected meta to expire")
	}
}

func TestKVLiteContext_IncompleteTurnNotReturned(t *testing.T) {
	raw := newMemoryRawBackend("memory")
	store := newSessionStore(raw, "ai:chat")
	sessionID := "sess-open"
	turn := &TurnRecord{
		SessionID:        sessionID,
		RequestMessageID: "msg-open-1",
		ParentMessageID:  "pid-open-0",
		TurnStatus:       TurnStatusOpen,
		UserMessage: openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleUser,
			Content: "unfinished",
		},
		UserTokens:  2,
		TotalTokens: 2,
	}
	seq, err := store.AllocateTurnSeq(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("allocate seq: %v", err)
	}
	turn.Seq = seq
	if err := store.WriteOpenTurn(context.Background(), turn, 20*time.Second); err != nil {
		t.Fatalf("write open turn: %v", err)
	}
	turns, err := store.LoadRecentTurns(context.Background(), sessionID, 1)
	if err != nil {
		t.Fatalf("load recent turns: %v", err)
	}
	if len(turns) != 0 {
		t.Fatalf("open turn should not be returned: %+v", turns)
	}
}

func TestKVLiteContext_FallbackToRedis(t *testing.T) {
	cnf := &config.Config{}
	cnf.Context.KVLite.Addr = "127.0.0.1:1"
	cnf.Context.KVLite.TimeoutMS = 50
	cnf.Context.KVLite.ConnectTimeoutMS = 50
	cnf.Context.KVLite.ReadTimeoutMS = 50
	cnf.Context.KVLite.WriteTimeoutMS = 50
	primary := newSessionStore(newKVLiteRawBackend(cnf, "ai:chat", nil), "ai:chat")
	fallback := newSessionStore(newMemoryRawBackend("redis-mock"), "ai:chat")
	store := &compositeStore{primary: primary, fallback: fallback, fallbackEnable: true, dualWrite: false, name: "kvlite+redis"}
	sessionID, err := store.ResolveSession(context.Background(), "", "msg-fallback-1", "")
	if err != nil {
		t.Fatalf("resolve session: %v", err)
	}
	turn := &TurnRecord{
		SessionID:        sessionID,
		RequestMessageID: "msg-fallback-1",
		UserMessage:      openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser, Content: "hello"},
		UserTokens:       3,
		TotalTokens:      3,
	}
	seq, err := store.AllocateTurnSeq(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("allocate seq via fallback: %v", err)
	}
	turn.Seq = seq
	if err := store.WriteOpenTurn(context.Background(), turn, 10*time.Second); err != nil {
		t.Fatalf("write open turn via fallback: %v", err)
	}
	if err := store.CompleteTurn(context.Background(), sessionID, seq, "assistant-msg-fallback-1", openai.ChatCompletionMessage{Role: openai.ChatMessageRoleAssistant, Content: "ok"}, 4, 10*time.Second); err != nil {
		t.Fatalf("complete turn via fallback: %v", err)
	}
	turns, err := store.LoadRecentTurns(context.Background(), sessionID, 1)
	if err != nil {
		t.Fatalf("load turns via fallback: %v", err)
	}
	if len(turns) != 1 || turns[0].UserMessage.Content != "hello" {
		t.Fatalf("unexpected fallback turns: %+v", turns)
	}
}

func TestKVLiteBackendPoolAndFallback(t *testing.T) {
	cnf := &config.Config{}
	cnf.Context.KVLite.Addr = "127.0.0.1:1"
	cnf.Context.KVLite.TimeoutMS = 50
	cnf.Context.KVLite.ConnectTimeoutMS = 50
	cnf.Context.KVLite.ReadTimeoutMS = 50
	cnf.Context.KVLite.WriteTimeoutMS = 50
	cnf.Context.KVLite.PoolSize = 2
	cnf.Context.KVLite.MaxIdle = 1
	cnf.Context.KVLite.MaxRetries = 1
	cnf.Context.KVLite.RetryBackoffMS = 10
	primary := newSessionStore(newKVLiteRawBackend(cnf, "ai:chat", nil), "ai:chat")
	fallbackRaw := newMemoryRawBackend("redis-mock")
	fallback := newSessionStore(fallbackRaw, "ai:chat")
	sessionID := "sess-backend-fallback"
	meta := &SessionMeta{SessionID: sessionID, LastSeq: 1, TurnCount: 1, UpdatedAtMS: nowMS()}
	metaBytes, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal meta: %v", err)
	}
	fallbackRaw.set(metaKey("ai:chat", sessionID), string(metaBytes), 0)
	store := &compositeStore{primary: primary, fallback: fallback, fallbackEnable: true, dualWrite: false, name: "kvlite+redis"}
	for i := 0; i < 2; i++ {
		gotMeta, err := store.LoadSessionMeta(context.Background(), sessionID)
		if err != nil {
			t.Fatalf("load meta via fallback: %v", err)
		}
		if gotMeta == nil || gotMeta.SessionID != sessionID {
			t.Fatalf("unexpected meta via fallback: %+v", gotMeta)
		}
	}
}

type flakyMetaRawBackend struct {
	inner           *memoryRawBackend
	failNextHSet    bool
	failNextHSetErr error
}

func newFlakyMetaRawBackend(name string) *flakyMetaRawBackend {
	return &flakyMetaRawBackend{inner: newMemoryRawBackend(name), failNextHSet: true, failNextHSetErr: fmt.Errorf("temporary meta write failure")}
}

func (f *flakyMetaRawBackend) Name() string { return f.inner.Name() }
func (f *flakyMetaRawBackend) Close() error { return f.inner.Close() }
func (f *flakyMetaRawBackend) RGet(ctx context.Context, key string) (string, error) {
	return f.inner.RGet(ctx, key)
}
func (f *flakyMetaRawBackend) RSet(ctx context.Context, key, value string, ttl time.Duration) error {
	return f.inner.RSet(ctx, key, value, ttl)
}
func (f *flakyMetaRawBackend) HGet(ctx context.Context, key string) (string, error) {
	return f.inner.HGet(ctx, key)
}
func (f *flakyMetaRawBackend) HSet(ctx context.Context, key, value string, ttl time.Duration) error {
	if f.failNextHSet {
		f.failNextHSet = false
		return f.failNextHSetErr
	}
	return f.inner.HSet(ctx, key, value, ttl)
}
func (f *flakyMetaRawBackend) TTL(ctx context.Context, key string) (time.Duration, error) {
	return f.inner.TTL(ctx, key)
}
func (f *flakyMetaRawBackend) Del(ctx context.Context, key string) error {
	return f.inner.Del(ctx, key)
}

func TestMetaRepairAfterPartialFailure(t *testing.T) {
	raw := newFlakyMetaRawBackend("flaky")
	store := newSessionStore(raw, "ai:chat")
	sessionID := "sess-repair"
	turn := &TurnRecord{
		SessionID:        sessionID,
		RequestMessageID: "msg-repair-1",
		ParentMessageID:  "pid-repair-0",
		UserMessage: openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleUser,
			Content: "repair me",
		},
		UserTokens:  2,
		TotalTokens: 2,
	}
	seq, err := store.AllocateTurnSeq(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("allocate seq: %v", err)
	}
	turn.Seq = seq
	if err := store.WriteOpenTurn(context.Background(), turn, 20*time.Second); err != nil {
		t.Fatalf("write open turn with repair: %v", err)
	}
	meta, err := store.LoadSessionMeta(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("load meta: %v", err)
	}
	if meta == nil || meta.LastSeq != seq || meta.TurnCount != 1 {
		t.Fatalf("expected repaired meta, got %+v", meta)
	}
}
