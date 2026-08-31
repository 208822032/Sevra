package server

import (
	chat_context "ai-chat-service/chat-server/chat-context"
	"ai-chat-service/pkg/config"
	"ai-chat-service/pkg/log"
	"ai-chat-service/proto"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sashabaranov/go-openai"
)

type fakePhase2Store struct {
	sessionID      string
	turns          []*chat_context.TurnRecord
	summary        *chat_context.SummaryRecord
	meta           *chat_context.SessionMeta
	resolvedInputs []string
	nextSeq        int64
}

func (f *fakePhase2Store) Name() string { return "fake-phase2" }
func (f *fakePhase2Store) Close() error { return nil }

func (f *fakePhase2Store) ResolveSession(ctx context.Context, sessionID, requestMessageID, parentMessageID string) (string, error) {
	if sessionID != "" {
		f.resolvedInputs = append(f.resolvedInputs, sessionID)
		return sessionID, nil
	}
	if f.sessionID != "" {
		f.resolvedInputs = append(f.resolvedInputs, f.sessionID)
		return f.sessionID, nil
	}
	if parentMessageID != "" {
		f.resolvedInputs = append(f.resolvedInputs, "compat")
		return "compat-session", nil
	}
	gen := fmt.Sprintf("session-%d", time.Now().UnixNano())
	f.resolvedInputs = append(f.resolvedInputs, gen)
	return gen, nil
}

func (f *fakePhase2Store) LoadRecentTurns(ctx context.Context, sessionID string, limit int) ([]*chat_context.TurnRecord, error) {
	return f.LoadTurnsSince(ctx, sessionID, 0, limit)
}

func (f *fakePhase2Store) LoadTurnsSince(ctx context.Context, sessionID string, afterSeq int64, limit int) ([]*chat_context.TurnRecord, error) {
	result := make([]*chat_context.TurnRecord, 0, limit)
	for _, turn := range f.turns {
		if turn == nil || turn.TurnStatus != chat_context.TurnStatusCompleted {
			continue
		}
		if turn.Seq <= afterSeq {
			continue
		}
		result = append(result, cloneTurn(turn))
	}
	if limit > 0 && len(result) > limit {
		result = result[len(result)-limit:]
	}
	return result, nil
}

func (f *fakePhase2Store) LoadSessionMeta(ctx context.Context, sessionID string) (*chat_context.SessionMeta, error) {
	if f.meta != nil {
		meta := *f.meta
		return &meta, nil
	}
	meta := &chat_context.SessionMeta{SessionID: sessionID}
	for _, turn := range f.turns {
		if turn != nil && turn.Seq > meta.LastSeq {
			meta.LastSeq = turn.Seq
		}
	}
	meta.TurnCount = int64(len(f.turns))
	return meta, nil
}

func (f *fakePhase2Store) LoadSummary(ctx context.Context, sessionID string) (*chat_context.SummaryRecord, error) {
	if f.summary == nil {
		return nil, nil
	}
	summary := *f.summary
	return &summary, nil
}

func (f *fakePhase2Store) StoreSummary(ctx context.Context, summary *chat_context.SummaryRecord, ttl time.Duration) error {
	if summary == nil {
		return nil
	}
	summaryCopy := *summary
	f.summary = &summaryCopy
	if f.meta == nil {
		f.meta = &chat_context.SessionMeta{SessionID: summary.SessionID}
	}
	f.meta.SessionID = summary.SessionID
	f.meta.SummarySeq = summary.SummarySeq
	f.meta.SummaryRef = fmt.Sprintf("%s:summary", summary.SessionID)
	return nil
}

func (f *fakePhase2Store) AllocateTurnSeq(ctx context.Context, sessionID string) (int64, error) {
	if f.meta != nil && f.meta.LastSeq > 0 {
		f.meta.LastSeq++
		return f.meta.LastSeq, nil
	}
	maxSeq := int64(0)
	for _, turn := range f.turns {
		if turn != nil && turn.Seq > maxSeq {
			maxSeq = turn.Seq
		}
	}
	if maxSeq > 0 {
		maxSeq++
		return maxSeq, nil
	}
	return 1, nil
}

func (f *fakePhase2Store) WriteOpenTurn(ctx context.Context, turn *chat_context.TurnRecord, ttl time.Duration) error {
	if turn == nil {
		return nil
	}
	updated := cloneTurn(turn)
	updated.TurnStatus = chat_context.TurnStatusOpen
	f.turns = upsertTurn(f.turns, updated)
	return nil
}

func (f *fakePhase2Store) CompleteTurn(ctx context.Context, sessionID string, seq int64, assistantMessageID string, assistantMessage openai.ChatCompletionMessage, assistantTokens int, ttl time.Duration) error {
	for i, turn := range f.turns {
		if turn != nil && turn.Seq == seq {
			turn.AssistantMessage = assistantMessage
			turn.AssistantMessageID = assistantMessageID
			turn.AssistantTokens = assistantTokens
			turn.TotalTokens = turn.UserTokens + assistantTokens
			turn.TurnStatus = chat_context.TurnStatusCompleted
			turn.UpdatedAtMS = time.Now().UnixNano() / int64(time.Millisecond)
			f.turns[i] = turn
			return nil
		}
	}
	return nil
}

func cloneTurn(turn *chat_context.TurnRecord) *chat_context.TurnRecord {
	if turn == nil {
		return nil
	}
	copyTurn := *turn
	return &copyTurn
}

func upsertTurn(turns []*chat_context.TurnRecord, turn *chat_context.TurnRecord) []*chat_context.TurnRecord {
	for i, existing := range turns {
		if existing != nil && existing.Seq == turn.Seq {
			turns[i] = turn
			return turns
		}
	}
	return append(turns, turn)
}

func preparePhase2Config(t *testing.T, tokenizerURL string) {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "dev.config.yaml")
	configText := fmt.Sprintf(`server:
  ip: 127.0.0.1
  port: 50055
  accessToken: test-token
log:
  level: info
  logPath: runtime/logs/app.log
chat:
  api_key: test-key
  base_url: http://localhost:8084/v1
  model: gpt-3.5-turbo
  max_tokens: 128
  temperature: 0.8
  top_p: 0.9
  presence_penalty: 0.8
  frequency_penalty: 0.5
  bot_desc: "你是一个AI助手"
  min_response_tokens: 16
  context_ttl: 1800
  context_len: 4
context:
  context_backend: redis
  context_fallback_to_redis: true
  context_namespace: ai:chat
  kvlite:
    addr: 127.0.0.1:9300
    password: ""
    timeout_ms: 2000
redis:
  host: 127.0.0.1
  port: 6379
  pwd: ""
mysql:
  dsn: ""
  maxLifeTime: 3600
  maxOpenConn: 10
  maxIdleConn: 10
dependOn:
  sensitive:
    address: localhost:50053
    accessToken: test
  keywords:
    address: localhost:50054
    accessToken: test
  tokenizer:
    address: %s
`, tokenizerURL)
	if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	config.InitConfig(configPath)
}

func newTokenizerServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/tokenizer/") {
			http.NotFound(w, r)
			return
		}
		defer r.Body.Close()
		payload := &openai.ChatCompletionMessage{}
		if err := json.NewDecoder(r.Body).Decode(payload); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 500, "msg": err.Error()})
			return
		}
		tokens := len([]rune(payload.Content))/2 + 1
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "num_tokens": tokens, "msg": "ok"})
	}))
}

func newTestChatService(t *testing.T, store chat_context.SessionStore) *chatService {
	t.Helper()
	logger := log.NewLogger()
	logger.SetOutput(io.Discard)
	return &chatService{config: config.GetConfig(), log: logger, contextStore: store, fastPathCache: newSessionFastPathCache()}
}

func TestSessionIDPreferredOverPIDCompat(t *testing.T) {
	tokenizerSrv := newTokenizerServer(t)
	defer tokenizerSrv.Close()
	preparePhase2Config(t, tokenizerSrv.URL)
	store := &fakePhase2Store{sessionID: "compat-session"}
	svc := newTestChatService(t, store)
	in := &proto.ChatCompletionRequest{
		Message:       "hello",
		Id:            "msg-explicit",
		Pid:           "legacy-parent",
		SessionId:     "explicit-session",
		EnableContext: true,
		ChatParam:     &proto.ChatParam{Model: "gpt-3.5-turbo", MaxTokens: 128, MinResponseTokens: 16, ContextLen: 2},
	}
	app := svc.newApp(in, store, false)
	_, _, _, _, err := app.buildChatCompletionRequest(context.Background(), in, false)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if app.sessionID != "explicit-session" {
		t.Fatalf("expected explicit session id, got %s", app.sessionID)
	}
	if app.pendingTurn == nil || app.pendingTurn.SessionID != "explicit-session" {
		t.Fatalf("pending turn did not keep explicit session id: %+v", app.pendingTurn)
	}
}

func TestCreateSessionWhenNoSessionIDAndNoPID(t *testing.T) {
	tokenizerSrv := newTokenizerServer(t)
	defer tokenizerSrv.Close()
	preparePhase2Config(t, tokenizerSrv.URL)
	store := &fakePhase2Store{}
	svc := newTestChatService(t, store)
	in := &proto.ChatCompletionRequest{
		Message:   "hello",
		Id:        "msg-new-session",
		ChatParam: &proto.ChatParam{Model: "gpt-3.5-turbo", MaxTokens: 128, MinResponseTokens: 16, ContextLen: 2},
	}
	app := svc.newApp(in, store, false)
	_, _, _, _, err := app.buildChatCompletionRequest(context.Background(), in, false)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if app.sessionID == "" {
		t.Fatalf("expected generated session id")
	}
	if app.pendingTurn == nil || app.pendingTurn.SessionID == "" {
		t.Fatalf("pending turn missing session id: %+v", app.pendingTurn)
	}
}

func TestKVLiteBasePromptUsesRecentTurnsOnly(t *testing.T) {
	tokenizerSrv := newTokenizerServer(t)
	defer tokenizerSrv.Close()
	preparePhase2Config(t, tokenizerSrv.URL)
	store := &fakePhase2Store{
		sessionID: "sess-summary",
		turns: []*chat_context.TurnRecord{
			{SessionID: "sess-summary", Seq: 3, TurnStatus: chat_context.TurnStatusCompleted, UserMessage: openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser, Content: "turn-3 user"}, AssistantMessage: openai.ChatCompletionMessage{Role: openai.ChatMessageRoleAssistant, Content: "turn-3 assistant"}, TotalTokens: 6},
			{SessionID: "sess-summary", Seq: 4, TurnStatus: chat_context.TurnStatusCompleted, UserMessage: openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser, Content: "turn-4 user"}, AssistantMessage: openai.ChatCompletionMessage{Role: openai.ChatMessageRoleAssistant, Content: "turn-4 assistant"}, TotalTokens: 6},
		},
	}
	svc := newTestChatService(t, store)
	in := &proto.ChatCompletionRequest{
		Message:       "current question",
		Id:            "msg-summary",
		SessionId:     "sess-summary",
		EnableContext: true,
		ChatParam:     &proto.ChatParam{Model: "gpt-3.5-turbo", MaxTokens: 128, MinResponseTokens: 16, ContextLen: 4},
	}
	app := svc.newApp(in, store, false)
	req, _, _, _, err := app.buildChatCompletionRequest(context.Background(), in, false)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if len(req.Messages) < 4 {
		t.Fatalf("expected recent turns in prompt, got %+v", req.Messages)
	}
	if req.Messages[0].Role != openai.ChatMessageRoleSystem {
		t.Fatalf("expected bot desc first, got %+v", req.Messages[0])
	}
	if !strings.Contains(req.Messages[1].Content, "turn-3 user") || !strings.Contains(req.Messages[2].Content, "turn-3 assistant") {
		t.Fatalf("recent turns missing from prompt: %+v", req.Messages)
	}
}

func TestKVLiteBasePromptDoesNotRequireSummary(t *testing.T) {
	tokenizerSrv := newTokenizerServer(t)
	defer tokenizerSrv.Close()
	preparePhase2Config(t, tokenizerSrv.URL)
	store := &fakePhase2Store{
		sessionID: "sess-no-summary",
		turns: []*chat_context.TurnRecord{
			{SessionID: "sess-no-summary", Seq: 1, TurnStatus: chat_context.TurnStatusCompleted, UserMessage: openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser, Content: "only turn"}, AssistantMessage: openai.ChatCompletionMessage{Role: openai.ChatMessageRoleAssistant, Content: "only answer"}, TotalTokens: 6},
		},
	}
	svc := newTestChatService(t, store)
	in := &proto.ChatCompletionRequest{
		Message:       "fresh question",
		Id:            "msg-no-summary",
		SessionId:     "sess-no-summary",
		EnableContext: true,
		ChatParam:     &proto.ChatParam{Model: "gpt-3.5-turbo", MaxTokens: 128, MinResponseTokens: 16, ContextLen: 4},
	}
	app := svc.newApp(in, store, false)
	req, _, _, _, err := app.buildChatCompletionRequest(context.Background(), in, false)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if !strings.Contains(req.Messages[len(req.Messages)-1].Content, "fresh question") {
		t.Fatalf("current user message missing: %+v", req.Messages)
	}
}
