package server

import (
	"ai-chat-service/chat-server/data"
	"ai-chat-service/chat-server/llm"
	"ai-chat-service/chat-server/qa"
	vector_data "ai-chat-service/chat-server/vector-data"
	serviceconfig "ai-chat-service/pkg/config"
	bklog "ai-chat-service/pkg/log"
	"ai-chat-service/proto"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/grpc/metadata"
)

type errorQARetriever struct {
	err error
}

func (e *errorQARetriever) Lookup(ctx context.Context, req qa.LookupRequest) (qa.LookupResult, error) {
	return qa.LookupResult{}, e.err
}

func (e *errorQARetriever) Close() error {
	return nil
}

type candidateQARetriever struct {
	result qa.LookupResult
}

func (c *candidateQARetriever) Lookup(ctx context.Context, req qa.LookupRequest) (qa.LookupResult, error) {
	return c.result, nil
}

func (c *candidateQARetriever) Close() error {
	return nil
}

type fakeChatCompletionStreamServer struct {
	ctx  context.Context
	sent []*proto.ChatCompletionStreamResponse
}

func (f *fakeChatCompletionStreamServer) SetHeader(metadata.MD) error  { return nil }
func (f *fakeChatCompletionStreamServer) SendHeader(metadata.MD) error { return nil }
func (f *fakeChatCompletionStreamServer) SetTrailer(metadata.MD)       {}
func (f *fakeChatCompletionStreamServer) Context() context.Context {
	if f.ctx == nil {
		return context.Background()
	}
	return f.ctx
}
func (f *fakeChatCompletionStreamServer) SendMsg(m interface{}) error { return nil }
func (f *fakeChatCompletionStreamServer) RecvMsg(m interface{}) error { return io.EOF }

func (f *fakeChatCompletionStreamServer) Send(resp *proto.ChatCompletionStreamResponse) error {
	f.sent = append(f.sent, resp)
	return nil
}

func initQAPreflightTestConfig(t *testing.T) {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "dev.config.yaml")
	configText := `server:
  ip: 127.0.0.1
  port: 0
  accessToken: test-token
log:
  level: info
  logPath: runtime/logs/app.log
chat:
  api_key: test-key
  base_url: http://localhost:8084/v1
  model: deepseek-chat
  max_tokens: 4096
  temperature: 0.8
  top_p: 0.9
  presence_penalty: 0.8
  frequency_penalty: 0.5
  bot_desc: "你是一个AI助手"
  min_response_tokens: 256
  context_ttl: 1800
  context_len: 4
dependOn:
  sensitive:
    address: http://127.0.0.1:1
    accessToken: test
  keywords:
    address: http://127.0.0.1:1
    accessToken: test
  tokenizer:
    address: http://127.0.0.1:1
`
	if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
		t.Fatalf("write test config: %v", err)
	}
	serviceconfig.InitConfig(configPath)
}

func newQAPreflightTestService(t *testing.T, retriever qa.Retriever, chatData data.IChatRecordsData, vectorData vector_data.IChatRecordsData) *chatService {
	t.Helper()
	initQAPreflightTestConfig(t)
	logger := bklog.NewLogger()
	logger.SetOutput(io.Discard)
	cfg := serviceconfig.GetConfig()
	svc := NewChatService(chatData, vectorData, cfg, logger, nil, nil, retriever).(*chatService)
	svc.sensitiveFn = func(context.Context, *proto.ChatCompletionRequest) (bool, string, error) {
		return true, "", nil
	}
	svc.keywordsFn = func(context.Context, *proto.ChatCompletionRequest) []string {
		return []string{"qa preflight"}
	}
	return svc
}

func TestQAPreflightLookupHitTypes(t *testing.T) {
	memory := qa.NewMemoryRetriever()
	memory.PutExact("How do I cache answers?", qa.LookupResult{
		Hit:        true,
		Answer:     "exact answer",
		QuestionID: "exact-1",
		Metadata: map[string]string{
			"question": "How do I cache answers?",
			"provider": "deepseek",
			"model":    "deepseek-chat",
		},
	})
	normalizedKey := qa.QuestionHash(qa.NormalizeQuestion("How do I cache answers?"))
	memory.PutNormalized(normalizedKey, qa.LookupResult{
		Hit:        true,
		Answer:     "normalized answer",
		QuestionID: "normalized-1",
		Metadata: map[string]string{
			"question": "How do I cache answers?",
			"provider": "deepseek",
			"model":    "deepseek-chat",
		},
	})

	svc := newQAPreflightTestService(t, memory, nil, nil)
	appExact := svc.newApp(&proto.ChatCompletionRequest{Message: "How do I cache answers?"}, nil, false)
	hit, result, err := appExact.tryQAPreflight(context.Background(), &proto.ChatCompletionRequest{Message: "How do I cache answers?"})
	if err != nil {
		t.Fatalf("exact preflight lookup: %v", err)
	}
	if !hit {
		t.Fatalf("expected exact hit")
	}
	if result.HitType != qa.HitTypeExact {
		t.Fatalf("expected exact hit type, got %+v", result)
	}
	if result.Answer != "exact answer" {
		t.Fatalf("unexpected exact answer: %+v", result)
	}

	appNormalized := svc.newApp(&proto.ChatCompletionRequest{Message: "How do I cache answers!!!"}, nil, false)
	hit, result, err = appNormalized.tryQAPreflight(context.Background(), &proto.ChatCompletionRequest{Message: "How do I cache answers!!!"})
	if err != nil {
		t.Fatalf("normalized preflight lookup: %v", err)
	}
	if !hit {
		t.Fatalf("expected normalized hit")
	}
	if result.HitType != qa.HitTypeNormalized {
		t.Fatalf("expected normalized hit type, got %+v", result)
	}
	if result.Answer != "normalized answer" {
		t.Fatalf("unexpected normalized answer: %+v", result)
	}
}

func TestQAPreflightUnaryResponseBuildsQAStoreTrace(t *testing.T) {
	svc := newQAPreflightTestService(t, qa.NewNoopRetriever(), nil, nil)
	app := svc.newApp(&proto.ChatCompletionRequest{Message: "What is the cache path?"}, nil, false)
	app.sessionID = "session-1"
	result := qa.LookupResult{
		Hit:        true,
		HitType:    qa.HitTypeNormalized,
		Answer:     "cached answer",
		QuestionID: "record-42",
		Metadata: map[string]string{
			"question": "What is the cache path?",
			"provider": "deepseek",
			"model":    "deepseek-chat",
		},
	}
	response, meta, err := app.buildQAPreflightUnaryResponse(context.Background(), &proto.ChatCompletionRequest{Message: "What is the cache path?"}, result)
	if err != nil {
		t.Fatalf("build unary preflight response: %v", err)
	}
	if response == nil || len(response.Choices) != 1 {
		t.Fatalf("unexpected unary response: %+v", response)
	}
	if got := response.Choices[0].Message.Content; got != "cached answer" {
		t.Fatalf("unexpected unary content: %q", got)
	}
	if response.SessionId != "session-1" {
		t.Fatalf("unexpected session id: %+v", response)
	}
	if meta.Trace.FinalAnswerSource != llm.FinalAnswerSourceQAStore {
		t.Fatalf("expected qa store final source, got %+v", meta.Trace)
	}
	if meta.Trace.QAHitType != qa.HitTypeNormalized {
		t.Fatalf("expected normalized qa hit type, got %+v", meta.Trace)
	}
	if !meta.Trace.CacheHit || !meta.Trace.UsedLocalRetrieval {
		t.Fatalf("expected cache hit flags, got %+v", meta.Trace)
	}
	if len(meta.Trace.RetrievedHits) != 1 {
		t.Fatalf("expected one retrieved hit, got %+v", meta.Trace)
	}
	if meta.Trace.RetrievedHits[0].HitType != qa.HitTypeNormalized {
		t.Fatalf("unexpected retrieved hit type: %+v", meta.Trace.RetrievedHits[0])
	}
	if meta.Trace.RetrievedHits[0].RecordID != "record-42" {
		t.Fatalf("unexpected retrieved hit record id: %+v", meta.Trace.RetrievedHits[0])
	}
	if response.Usage == nil || response.Usage.TotalTokens == 0 {
		t.Fatalf("expected response usage to be populated: %+v", response)
	}
}

func TestQAPreflightCandidateResponseBuildsCandidateTrace(t *testing.T) {
	result := qa.LookupResult{
		Hit:                 true,
		HitType:             qa.HitTypeCandidate,
		Answer:              "candidate answer",
		QuestionID:          "candidate-1",
		CanonicalQuestionID: "candidate-1",
		CandidateCount:      3,
		CandidateTopScore:   0.91,
		CandidateSource:     qa.CandidateRecallSourceBigram,
		Metadata: map[string]string{
			"question":  "How do I flush the Redis cache safely?",
			"provider":  "deepseek",
			"model":     "deepseek-chat",
			"record_id": "candidate-1",
		},
	}
	svc := newQAPreflightTestService(t, &candidateQARetriever{result: result}, nil, nil)
	request := &proto.ChatCompletionRequest{Id: "req-candidate", Message: "How do I flush the Redis cache quickly?"}
	app := svc.newApp(request, nil, false)
	hit, lookupResult, err := app.tryQAPreflight(context.Background(), request)
	if err != nil {
		t.Fatalf("candidate preflight lookup: %v", err)
	}
	if !hit || lookupResult.HitType != qa.HitTypeCandidate {
		t.Fatalf("expected candidate hit, got %+v", lookupResult)
	}
	if lookupResult.CandidateCount != 3 || lookupResult.CandidateTopScore != 0.91 {
		t.Fatalf("unexpected candidate lookup result: %+v", lookupResult)
	}

	unaryResponse, unaryMeta, err := app.buildQAPreflightUnaryResponse(context.Background(), request, lookupResult)
	if err != nil {
		t.Fatalf("build candidate unary response: %v", err)
	}
	if unaryResponse == nil || len(unaryResponse.Choices) != 1 || unaryResponse.Choices[0].Message.Content != "candidate answer" {
		t.Fatalf("unexpected candidate unary response: %+v", unaryResponse)
	}
	if unaryMeta.Trace.FinalAnswerSource != llm.FinalAnswerSourceQACandidate {
		t.Fatalf("expected candidate final source, got %+v", unaryMeta.Trace)
	}
	if unaryMeta.Trace.QAHitType != qa.HitTypeCandidate {
		t.Fatalf("expected candidate hit type, got %+v", unaryMeta.Trace)
	}
	if unaryMeta.Trace.CandidateCount != 3 || unaryMeta.Trace.CandidateTopScore != 0.91 {
		t.Fatalf("unexpected candidate trace: %+v", unaryMeta.Trace)
	}
	if unaryMeta.Trace.CandidateSource != qa.CandidateRecallSourceBigram {
		t.Fatalf("unexpected candidate source: %+v", unaryMeta.Trace)
	}

	chunks, streamMeta, err := app.buildQAPreflightStreamResponses(context.Background(), request, lookupResult)
	if err != nil {
		t.Fatalf("build candidate stream responses: %v", err)
	}
	if streamMeta.Trace.FinalAnswerSource != llm.FinalAnswerSourceQACandidate {
		t.Fatalf("expected stream candidate final source, got %+v", streamMeta.Trace)
	}
	if streamMeta.Trace.CandidateCount != 3 || streamMeta.Trace.CandidateTopScore != 0.91 {
		t.Fatalf("unexpected candidate stream trace: %+v", streamMeta.Trace)
	}
	if len(chunks) < 3 {
		t.Fatalf("expected candidate stream chunks, got %+v", chunks)
	}
	var content strings.Builder
	for _, chunk := range chunks {
		if chunk == nil || len(chunk.Choices) == 0 {
			continue
		}
		content.WriteString(chunk.Choices[0].Delta.Content)
	}
	if got := content.String(); got != "candidate answer" {
		t.Fatalf("unexpected candidate stream content: %q", got)
	}
}

func TestQAPreflightSemanticResponseBuildsSemanticTrace(t *testing.T) {
	result := qa.LookupResult{
		Hit:                         true,
		HitType:                     qa.HitTypeSemantic,
		Answer:                      "semantic answer",
		QuestionID:                  "semantic-1",
		SemanticCandidateCount:      4,
		SemanticTopK:                3,
		SemanticTopScore:            0.93,
		SemanticModel:               "text-embedding-ada-002",
		SemanticSource:              qa.SemanticRecallSourceBruteForce,
		SemanticModeRequested:       qa.SemanticRecallModeANN,
		SemanticModeUsed:            qa.SemanticRecallModeBruteForce,
		SemanticFallbackUsed:        true,
		SemanticFallbackReason:      "ann_index_missing",
		SemanticLatencyMS:           12.5,
		SemanticANNLatencyMS:        4.2,
		SemanticBruteForceLatencyMS: 8.3,
		Metadata: map[string]string{
			"question":  "How do I reset the cache safely?",
			"provider":  "deepseek",
			"model":     "deepseek-chat",
			"record_id": "semantic-1",
		},
	}
	svc := newQAPreflightTestService(t, &candidateQARetriever{result: result}, nil, nil)
	request := &proto.ChatCompletionRequest{Id: "req-semantic", Message: "How do I reset the cache safely?"}
	app := svc.newApp(request, nil, false)
	hit, lookupResult, err := app.tryQAPreflight(context.Background(), request)
	if err != nil {
		t.Fatalf("semantic preflight lookup: %v", err)
	}
	if !hit || lookupResult.HitType != qa.HitTypeSemantic {
		t.Fatalf("expected semantic hit, got %+v", lookupResult)
	}

	unaryResponse, unaryMeta, err := app.buildQAPreflightUnaryResponse(context.Background(), request, lookupResult)
	if err != nil {
		t.Fatalf("build semantic unary response: %v", err)
	}
	if unaryResponse == nil || len(unaryResponse.Choices) != 1 || unaryResponse.Choices[0].Message.Content != "semantic answer" {
		t.Fatalf("unexpected semantic unary response: %+v", unaryResponse)
	}
	if unaryMeta.Trace.FinalAnswerSource != llm.FinalAnswerSourceQASemantic {
		t.Fatalf("expected semantic final source, got %+v", unaryMeta.Trace)
	}
	if unaryMeta.Trace.AnswerSourceKind != llm.AnswerSourceKindCache {
		t.Fatalf("expected semantic cache source kind, got %+v", unaryMeta.Trace)
	}
	if unaryMeta.Trace.QAHitType != qa.HitTypeSemantic {
		t.Fatalf("expected semantic hit type, got %+v", unaryMeta.Trace)
	}
	if unaryMeta.Trace.SemanticCandidateCount != 4 || unaryMeta.Trace.SemanticTopK != 3 {
		t.Fatalf("unexpected semantic trace counts: %+v", unaryMeta.Trace)
	}
	if unaryMeta.Trace.SemanticTopScore != 0.93 {
		t.Fatalf("unexpected semantic top score: %+v", unaryMeta.Trace)
	}
	if unaryMeta.Trace.SemanticModel != "text-embedding-ada-002" || unaryMeta.Trace.SemanticSource != qa.SemanticRecallSourceBruteForce {
		t.Fatalf("unexpected semantic model/source: %+v", unaryMeta.Trace)
	}
	if unaryMeta.Trace.SemanticModeRequested != qa.SemanticRecallModeANN || unaryMeta.Trace.SemanticModeUsed != qa.SemanticRecallModeBruteForce {
		t.Fatalf("unexpected semantic mode trace: %+v", unaryMeta.Trace)
	}
	if !unaryMeta.Trace.SemanticFallbackUsed || unaryMeta.Trace.SemanticFallbackReason != "ann_index_missing" {
		t.Fatalf("unexpected semantic fallback trace: %+v", unaryMeta.Trace)
	}
	if unaryMeta.Trace.SemanticLatencyMS != 12.5 || unaryMeta.Trace.SemanticANNLatencyMS != 4.2 || unaryMeta.Trace.SemanticBruteForceLatencyMS != 8.3 {
		t.Fatalf("unexpected semantic latency trace: %+v", unaryMeta.Trace)
	}

	chunks, streamMeta, err := app.buildQAPreflightStreamResponses(context.Background(), request, lookupResult)
	if err != nil {
		t.Fatalf("build semantic stream responses: %v", err)
	}
	if streamMeta.Trace.FinalAnswerSource != llm.FinalAnswerSourceQASemantic {
		t.Fatalf("expected semantic stream final source, got %+v", streamMeta.Trace)
	}
	if streamMeta.Trace.AnswerSourceKind != llm.AnswerSourceKindCache {
		t.Fatalf("expected semantic stream cache source kind, got %+v", streamMeta.Trace)
	}
	if streamMeta.Trace.SemanticCandidateCount != 4 || streamMeta.Trace.SemanticTopK != 3 {
		t.Fatalf("unexpected semantic stream trace counts: %+v", streamMeta.Trace)
	}
	if streamMeta.Trace.SemanticTopScore != 0.93 {
		t.Fatalf("unexpected semantic stream top score: %+v", streamMeta.Trace)
	}
	if streamMeta.Trace.SemanticModel != "text-embedding-ada-002" || streamMeta.Trace.SemanticSource != qa.SemanticRecallSourceBruteForce {
		t.Fatalf("unexpected semantic stream model/source: %+v", streamMeta.Trace)
	}
	if streamMeta.Trace.SemanticModeRequested != qa.SemanticRecallModeANN || streamMeta.Trace.SemanticModeUsed != qa.SemanticRecallModeBruteForce {
		t.Fatalf("unexpected semantic stream mode trace: %+v", streamMeta.Trace)
	}
	if !streamMeta.Trace.SemanticFallbackUsed || streamMeta.Trace.SemanticFallbackReason != "ann_index_missing" {
		t.Fatalf("unexpected semantic stream fallback trace: %+v", streamMeta.Trace)
	}
	if streamMeta.Trace.SemanticLatencyMS != 12.5 || streamMeta.Trace.SemanticANNLatencyMS != 4.2 || streamMeta.Trace.SemanticBruteForceLatencyMS != 8.3 {
		t.Fatalf("unexpected semantic stream latency trace: %+v", streamMeta.Trace)
	}
	if len(chunks) < 3 {
		t.Fatalf("expected semantic stream chunks, got %+v", chunks)
	}
	var content strings.Builder
	for _, chunk := range chunks {
		if chunk == nil || len(chunk.Choices) == 0 {
			continue
		}
		content.WriteString(chunk.Choices[0].Delta.Content)
	}
	if got := content.String(); got != "semantic answer" {
		t.Fatalf("unexpected semantic stream content: %q", got)
	}
}

func TestQAPreflightUsesConfiguredSemanticMode(t *testing.T) {
	retriever := &shadowRecordingRetriever{result: qa.LookupResult{
		Hit:                   true,
		HitType:               qa.HitTypeSemantic,
		Answer:                "ann answer",
		QuestionID:            "ann-1",
		SemanticModeRequested: qa.SemanticRecallModeANN,
		SemanticModeUsed:      qa.SemanticRecallModeANN,
		SemanticSource:        qa.SemanticRecallSourceANNBucketedLSH,
		Metadata: map[string]string{
			"question": "How do I reset the cache safely?",
			"provider": "deepseek",
			"model":    "deepseek-chat",
		},
	}}
	svc := newQAPreflightTestService(t, retriever, nil, nil)
	svc.config.Retrieval.Semantic.Enabled = true
	svc.config.Retrieval.Semantic.Mode = qa.SemanticRecallModeANN
	request := &proto.ChatCompletionRequest{Id: "req-ann-mode", Message: "How do I reset the cache safely?"}
	app := svc.newApp(request, nil, false)
	hit, lookupResult, err := app.tryQAPreflight(context.Background(), request)
	if err != nil {
		t.Fatalf("ann preflight lookup: %v", err)
	}
	if !hit || lookupResult.HitType != qa.HitTypeSemantic {
		t.Fatalf("expected semantic hit, got %+v", lookupResult)
	}
	if retriever.request.SemanticMode != qa.SemanticRecallModeANN {
		t.Fatalf("expected configured ANN mode to reach retriever, got %+v", retriever.request)
	}
	if !retriever.request.SemanticEnabled {
		t.Fatalf("expected semantic lookup to remain enabled, got %+v", retriever.request)
	}
	if lookupResult.SemanticModeRequested != qa.SemanticRecallModeANN || lookupResult.SemanticModeUsed != qa.SemanticRecallModeANN {
		t.Fatalf("unexpected semantic mode on lookup result: %+v", lookupResult)
	}

	unaryResponse, unaryMeta, err := app.buildQAPreflightUnaryResponse(context.Background(), request, lookupResult)
	if err != nil {
		t.Fatalf("build ann unary response: %v", err)
	}
	if unaryResponse == nil || len(unaryResponse.Choices) != 1 || unaryResponse.Choices[0].Message.Content != "ann answer" {
		t.Fatalf("unexpected ann unary response: %+v", unaryResponse)
	}
	if unaryMeta.Trace.SemanticModeRequested != qa.SemanticRecallModeANN || unaryMeta.Trace.SemanticModeUsed != qa.SemanticRecallModeANN {
		t.Fatalf("unexpected ann unary trace: %+v", unaryMeta.Trace)
	}
	if unaryMeta.Trace.SemanticFallbackUsed {
		t.Fatalf("did not expect fallback on healthy ANN lookup: %+v", unaryMeta.Trace)
	}

	chunks, streamMeta, err := app.buildQAPreflightStreamResponses(context.Background(), request, lookupResult)
	if err != nil {
		t.Fatalf("build ann stream responses: %v", err)
	}
	if len(chunks) < 3 {
		t.Fatalf("expected ann stream chunks, got %+v", chunks)
	}
	if streamMeta.Trace.SemanticModeRequested != qa.SemanticRecallModeANN || streamMeta.Trace.SemanticModeUsed != qa.SemanticRecallModeANN {
		t.Fatalf("unexpected ann stream trace: %+v", streamMeta.Trace)
	}
	if streamMeta.Trace.SemanticFallbackUsed {
		t.Fatalf("did not expect ANN fallback on stream lookup: %+v", streamMeta.Trace)
	}
}

func TestChatCompletionShortCircuitsOnQAPreflightHit(t *testing.T) {
	memory := qa.NewMemoryRetriever()
	memory.PutExact("Where is the doc?", qa.LookupResult{
		Hit:        true,
		Answer:     "qa answer",
		QuestionID: "qa-1",
		Metadata: map[string]string{
			"question": "Where is the doc?",
			"provider": "deepseek",
			"model":    "deepseek-chat",
		},
	})
	svc := newQAPreflightTestService(t, memory, nil, nil)
	resp, err := svc.ChatCompletion(context.Background(), &proto.ChatCompletionRequest{Id: "req-1", Message: "Where is the doc?"})
	if err != nil {
		t.Fatalf("chat completion preflight hit: %v", err)
	}
	if resp == nil || len(resp.Choices) != 1 {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if got := resp.Choices[0].Message.Content; got != "qa answer" {
		t.Fatalf("unexpected response content: %q", got)
	}
}

func TestChatCompletionFallsBackWhenQAPreflightMissesOrErrors(t *testing.T) {
	t.Run("miss", func(t *testing.T) {
		vectorStore := &fakeVectorRecordsData{
			expectedProvider: "deepseek",
			expectedModel:    "deepseek-chat",
			hitID:            "88",
			hitScore:         0.995,
		}
		svc := newQAPreflightTestService(t, qa.NewNoopRetriever(), &fakeChatRecordsData{record: &data.ChatRecord{ID: 88, UserMsg: "cached question", AIMsg: "cached answer"}}, vectorStore)
		resp, err := svc.ChatCompletion(context.Background(), &proto.ChatCompletionRequest{Id: "req-2", Message: "explicit model scope"})
		if err != nil {
			t.Fatalf("chat completion miss fallback: %v", err)
		}
		if resp == nil || len(resp.Choices) != 1 {
			t.Fatalf("unexpected response: %+v", resp)
		}
		if got := resp.Choices[0].Message.Content; got != "cached answer" {
			t.Fatalf("unexpected fallback content: %q", got)
		}
	})

	t.Run("error", func(t *testing.T) {
		vectorStore := &fakeVectorRecordsData{
			expectedProvider: "deepseek",
			expectedModel:    "deepseek-chat",
			hitID:            "88",
			hitScore:         0.995,
		}
		svc := newQAPreflightTestService(t, &errorQARetriever{err: errors.New("qa store down")}, &fakeChatRecordsData{record: &data.ChatRecord{ID: 88, UserMsg: "cached question", AIMsg: "cached answer"}}, vectorStore)
		resp, err := svc.ChatCompletion(context.Background(), &proto.ChatCompletionRequest{Id: "req-3", Message: "explicit model scope"})
		if err != nil {
			t.Fatalf("chat completion error fallback: %v", err)
		}
		if resp == nil || len(resp.Choices) != 1 {
			t.Fatalf("unexpected response: %+v", resp)
		}
		if got := resp.Choices[0].Message.Content; got != "cached answer" {
			t.Fatalf("unexpected fallback content: %q", got)
		}
	})
}

func TestChatCompletionStreamShortCircuitsOnQAPreflightHit(t *testing.T) {
	memory := qa.NewMemoryRetriever()
	memory.PutExact("Where is the doc?", qa.LookupResult{
		Hit:        true,
		Answer:     "qa answer",
		QuestionID: "qa-1",
		Metadata: map[string]string{
			"question": "Where is the doc?",
			"provider": "deepseek",
			"model":    "deepseek-chat",
		},
	})
	svc := newQAPreflightTestService(t, memory, nil, nil)
	stream := &fakeChatCompletionStreamServer{ctx: metadata.NewIncomingContext(context.Background(), metadata.MD{})}
	if err := svc.ChatCompletionStream(&proto.ChatCompletionRequest{Id: "req-4", Message: "Where is the doc?"}, stream); err != nil {
		t.Fatalf("chat completion stream preflight hit: %v", err)
	}
	if len(stream.sent) < 3 {
		t.Fatalf("expected stream chunks, got %+v", stream.sent)
	}
	var content strings.Builder
	for i, chunk := range stream.sent {
		if chunk == nil || len(chunk.Choices) == 0 {
			t.Fatalf("unexpected stream chunk at %d: %+v", i, chunk)
		}
		if i == 0 && chunk.Choices[0].FinishReason != "" {
			t.Fatalf("expected empty finish reason on first chunk: %+v", chunk)
		}
		if i == len(stream.sent)-1 && chunk.Choices[0].FinishReason != "stop" {
			t.Fatalf("expected stop finish reason on last chunk: %+v", chunk)
		}
		content.WriteString(chunk.Choices[0].Delta.Content)
	}
	if got := content.String(); got != "qa answer" {
		t.Fatalf("unexpected streamed content: %q", got)
	}
}
