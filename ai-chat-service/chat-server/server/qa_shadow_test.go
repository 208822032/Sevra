package server

import (
	"ai-chat-service/chat-server/llm"
	metrics_bus "ai-chat-service/chat-server/metrics-bus"
	"ai-chat-service/chat-server/qa"
	"ai-chat-service/proto"
	"context"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"google.golang.org/grpc/metadata"
)

type shadowRecordingRetriever struct {
	request qa.LookupRequest
	result  qa.LookupResult
}

func (r *shadowRecordingRetriever) Lookup(ctx context.Context, req qa.LookupRequest) (qa.LookupResult, error) {
	r.request = req
	return r.result, nil
}

func (r *shadowRecordingRetriever) Close() error {
	return nil
}

func newShadowPreflightTestService(t *testing.T, retriever qa.Retriever) *chatService {
	t.Helper()
	svc := newQAPreflightTestService(t, retriever, nil, nil)
	svc.config.Retrieval.Semantic.Enabled = true
	svc.busMetrics = metrics_bus.NewBusMetrics(prometheus.NewRegistry())
	return svc
}

func buildShadowLookupResult() qa.LookupResult {
	return qa.LookupResult{
		Hit:                    true,
		HitType:                qa.HitTypeSemantic,
		Answer:                 "main semantic answer",
		QuestionID:             "main-answer-id",
		SemanticCandidateCount: 2,
		SemanticTopK:           2,
		SemanticTopScore:       0.93,
		SemanticModel:          "text-embedding-ada-002",
		SemanticSource:         qa.SemanticRecallSourceBruteForce,
		SemanticShadowTrace: llm.SemanticShadowTrace{
			ShadowSemanticEnabled:      true,
			ShadowSemanticHit:          true,
			ShadowSemanticLayer:        "semantic_ann",
			ShadowSemanticAnswer:       "shadow semantic answer",
			ShadowSemanticAnswerID:     "shadow-answer-id",
			ShadowSemanticTopScore:     0.91,
			ShadowSemanticTopK:         2,
			ShadowSemanticIndexSource:  qa.SemanticRecallSourceANNBucketedLSH,
			ShadowSemanticIndexVersion: "scope=provider=deepseek|corpus=16|dim=4|bits=4|probe=1",
			ShadowSemanticSameTop1:     false,
			ShadowSemanticSameAsFinal:  false,
			ShadowSemanticDiffCategory: "bf_hit_ann_hit_diff_top1",
			ShadowSemanticTopHits: []llm.RetrievalHit{{
				RecordID:        "shadow-answer-id",
				Score:           0.91,
				HitType:         qa.HitTypeSemantic,
				QuestionPreview: "shadow question",
				AnswerPreview:   "shadow semantic answer",
				Provider:        "deepseek",
				Model:           "deepseek-chat",
				SourceType:      llm.SourceTypeRetrieval,
			}},
		},
	}
}

func TestQAPreflightShadowConfigAndUnaryTracePropagation(t *testing.T) {
	t.Run("shadow disabled keeps request clean", func(t *testing.T) {
		retriever := &shadowRecordingRetriever{result: qa.LookupResult{
			Hit:            true,
			HitType:        qa.HitTypeSemantic,
			Answer:         "main semantic answer",
			QuestionID:     "main-answer-id",
			SemanticSource: qa.SemanticRecallSourceBruteForce,
		}}
		svc := newShadowPreflightTestService(t, retriever)
		request := &proto.ChatCompletionRequest{Id: "req-shadow-off", Message: "how do shadows behave?"}
		app := svc.newApp(request, nil, false)
		app.semanticShadowConfig = qa.SemanticShadowConfig{Enabled: false, TopK: 2, ScoreThreshold: 0.6, LogDiffOnly: true, SampleRate: 1}

		hit, result, err := app.tryQAPreflight(context.Background(), request)
		if err != nil {
			t.Fatalf("shadow-disabled preflight lookup: %v", err)
		}
		if !hit || result.Answer != "main semantic answer" {
			t.Fatalf("unexpected preflight result: %+v", result)
		}
		if retriever.request.SemanticMode != qa.SemanticRecallModeBruteForce {
			t.Fatalf("expected brute-force semantic mode, got %+v", retriever.request)
		}
		if retriever.request.SemanticShadow.Enabled {
			t.Fatalf("did not expect shadow to be enabled, got %+v", retriever.request.SemanticShadow)
		}

		response, meta, err := app.buildQAPreflightUnaryResponse(context.Background(), request, result)
		if err != nil {
			t.Fatalf("build disabled shadow unary response: %v", err)
		}
		if response == nil || len(response.Choices) != 1 || response.Choices[0].Message.Content != "main semantic answer" {
			t.Fatalf("unexpected unary response: %+v", response)
		}
		if meta.Trace.ShadowSemanticEnabled {
			t.Fatalf("did not expect shadow trace fields when shadow is disabled: %+v", meta.Trace)
		}
	})

	t.Run("shadow enabled records trace and metrics", func(t *testing.T) {
		retriever := &shadowRecordingRetriever{result: buildShadowLookupResult()}
		svc := newShadowPreflightTestService(t, retriever)
		request := &proto.ChatCompletionRequest{Id: "req-shadow-on", Message: "how do shadows behave?"}
		app := svc.newApp(request, nil, false)
		app.semanticShadowConfig = qa.SemanticShadowConfig{Enabled: true, TopK: 2, ScoreThreshold: 0.6, LogDiffOnly: true, SampleRate: 1}

		hit, result, err := app.tryQAPreflight(context.Background(), request)
		if err != nil {
			t.Fatalf("shadow-enabled preflight lookup: %v", err)
		}
		if !hit || result.Answer != "main semantic answer" {
			t.Fatalf("unexpected preflight result: %+v", result)
		}
		if !retriever.request.SemanticShadow.Enabled || retriever.request.SemanticShadow.TopK != 2 {
			t.Fatalf("expected shadow config to be passed to retriever, got %+v", retriever.request.SemanticShadow)
		}

		response, meta, err := app.buildQAPreflightUnaryResponse(context.Background(), request, result)
		if err != nil {
			t.Fatalf("build enabled shadow unary response: %v", err)
		}
		if response == nil || len(response.Choices) != 1 || response.Choices[0].Message.Content != "main semantic answer" {
			t.Fatalf("unexpected unary response: %+v", response)
		}
		if !meta.Trace.ShadowSemanticEnabled || !meta.Trace.ShadowSemanticHit {
			t.Fatalf("expected shadow trace to be populated, got %+v", meta.Trace)
		}
		if meta.Trace.ShadowSemanticAnswerID != "shadow-answer-id" {
			t.Fatalf("unexpected shadow answer id: %+v", meta.Trace)
		}
		if meta.Trace.ShadowSemanticSameTop1 {
			t.Fatalf("expected top1 mismatch between brute-force and ANN, got %+v", meta.Trace)
		}
		if meta.Trace.ShadowSemanticSameAsFinal {
			t.Fatalf("expected final answer to differ from ANN top1, got %+v", meta.Trace)
		}
		if meta.Trace.ShadowSemanticDiffCategory != "bf_hit_ann_hit_diff_top1" {
			t.Fatalf("unexpected diff category: %+v", meta.Trace)
		}
		if len(meta.Trace.ShadowSemanticTopHits) == 0 {
			t.Fatalf("expected top hits in shadow trace: %+v", meta.Trace)
		}

		if got := testutil.ToFloat64(svc.busMetrics.AnnShadowQueriesTotalCounter); got != 1 {
			t.Fatalf("unexpected ann shadow queries metric: %.0f", got)
		}
		if got := testutil.ToFloat64(svc.busMetrics.AnnShadowHitsTotalCounter); got != 1 {
			t.Fatalf("unexpected ann shadow hits metric: %.0f", got)
		}
		if got := testutil.ToFloat64(svc.busMetrics.AnnShadowDiffTotalCounter); got != 1 {
			t.Fatalf("unexpected ann shadow diff metric: %.0f", got)
		}
		if got := testutil.ToFloat64(svc.busMetrics.AnnShadowSameAsBruteforceTotalCounter); got != 0 {
			t.Fatalf("unexpected ann shadow same-as-bruteforce metric: %.0f", got)
		}
	})
}

func TestQAPreflightShadowStreamAndFinalAnswerComparison(t *testing.T) {
	retriever := &shadowRecordingRetriever{result: buildShadowLookupResult()}
	svc := newShadowPreflightTestService(t, retriever)
	request := &proto.ChatCompletionRequest{Id: "req-shadow-stream", Message: "how do shadows behave?"}
	app := svc.newApp(request, nil, true)
	app.semanticShadowConfig = qa.SemanticShadowConfig{Enabled: true, TopK: 2, ScoreThreshold: 0.6, LogDiffOnly: true, SampleRate: 1}

	hit, result, err := app.tryQAPreflight(context.Background(), request)
	if err != nil {
		t.Fatalf("shadow stream preflight lookup: %v", err)
	}
	if !hit {
		t.Fatalf("expected preflight hit for shadow stream test")
	}
	chunks, meta, err := app.buildQAPreflightStreamResponses(context.Background(), request, result)
	if err != nil {
		t.Fatalf("build shadow stream responses: %v", err)
	}
	if meta.Trace.ShadowSemanticAnswerID != "shadow-answer-id" || !meta.Trace.ShadowSemanticEnabled {
		t.Fatalf("unexpected shadow stream trace: %+v", meta.Trace)
	}
	if len(chunks) < 3 {
		t.Fatalf("expected stream chunks, got %+v", chunks)
	}
	var content strings.Builder
	for _, chunk := range chunks {
		if chunk == nil || len(chunk.Choices) == 0 {
			continue
		}
		content.WriteString(chunk.Choices[0].Delta.Content)
	}
	if got := content.String(); got != "main semantic answer" {
		t.Fatalf("unexpected stream content: %q", got)
	}

	decision := llm.RouteDecision{Mode: llm.AnswerModeAPI}
	app.qaLookupResult = result
	trace := app.buildAnswerTrace(decision, llm.FinalAnswerSourceLLM, false, false, "shadow semantic answer")
	if !trace.ShadowSemanticSameAsFinal {
		t.Fatalf("expected final answer comparison to be true, got %+v", trace)
	}
	if trace.ShadowSemanticDiffCategory != "bf_hit_ann_hit_diff_top1" {
		t.Fatalf("unexpected final answer shadow diff category: %+v", trace)
	}
}

func TestChatCompletionStreamWithShadowKeepsResponseContent(t *testing.T) {
	retriever := &shadowRecordingRetriever{result: buildShadowLookupResult()}
	svc := newShadowPreflightTestService(t, retriever)
	svc.config.Retrieval.Semantic.ShadowANN.Enabled = true
	svc.config.Retrieval.Semantic.ShadowANN.TopK = 2
	svc.config.Retrieval.Semantic.ShadowANN.ScoreThreshold = 0.6
	svc.config.Retrieval.Semantic.ShadowANN.LogDiffOnly = true
	svc.config.Retrieval.Semantic.ShadowANN.SampleRate = 1
	request := &proto.ChatCompletionRequest{Id: "req-shadow-rpc", Message: "how do shadows behave?"}
	stream := &fakeChatCompletionStreamServer{ctx: metadata.NewIncomingContext(context.Background(), metadata.MD{})}
	if err := svc.ChatCompletionStream(request, stream); err != nil {
		t.Fatalf("chat completion stream with shadow: %v", err)
	}
	if len(stream.sent) < 3 {
		t.Fatalf("expected stream chunks, got %+v", stream.sent)
	}
	var content strings.Builder
	for _, chunk := range stream.sent {
		if chunk == nil || len(chunk.Choices) == 0 {
			continue
		}
		content.WriteString(chunk.Choices[0].Delta.Content)
	}
	if got := content.String(); got != "main semantic answer" {
		t.Fatalf("unexpected streamed content: %q", got)
	}
}
