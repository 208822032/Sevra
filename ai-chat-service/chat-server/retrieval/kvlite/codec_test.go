package kvlite

import (
	"errors"
	"strings"
	"testing"
)

func TestCodecRoundTripWithDelimitedChineseMarkdownPayload(t *testing.T) {
	scope := LogicalScope{
		Tenant:        "租户|A",
		KnowledgeBase: "帮助中心\\v2",
		CorpusVersion: "2026-08\nrelease",
		Provider:      "deepseek",
		Model:         "deepseek-chat",
		PromptVersion: "prompt|v1",
	}
	question := "如何处理 | 管道符与 \\ 反斜杠？\n请给出示例"
	envelope := AnswerEnvelope{
		Version:    AnswerEnvelopeVersion,
		DocumentID: "doc|001\\中文",
		Answer:     "可以安全保存。\n\n| 字段 | 值 |\n| --- | --- |\n| 状态 | `ok|ready` |",
		Source:     "knowledge_base",
		CreatedAt:  1723881600,
	}

	encodedScope, err := EncodeScope(scope)
	if err != nil {
		t.Fatalf("EncodeScope: %v", err)
	}
	encodedQuestion, err := EncodeQuestion(question)
	if err != nil {
		t.Fatalf("EncodeQuestion: %v", err)
	}
	encodedAnswer, err := EncodeAnswer(envelope)
	if err != nil {
		t.Fatalf("EncodeAnswer: %v", err)
	}
	for name, value := range map[string]string{
		"scope": encodedScope, "question": encodedQuestion, "answer": encodedAnswer,
	} {
		if strings.Contains(value, "|") {
			t.Fatalf("encoded %s contains VSEARCH delimiter: %q", name, value)
		}
	}

	raw := "score:0.812500|scope:" + encodedScope + "|question:" + encodedQuestion + "|answer:" + encodedAnswer
	hit, err := ParseVSearchItem(raw)
	if err != nil {
		t.Fatalf("ParseVSearchItem: %v", err)
	}
	if hit.Score != 0.8125 || hit.Scope != scope || hit.Question != question || hit.Answer != envelope {
		t.Fatalf("round-trip mismatch: %+v", hit)
	}
}

func TestQuestionCodecRejectsMalformedEscapes(t *testing.T) {
	for _, encoded := range []string{"plain|delimiter", "trailing\\", "unknown\\x"} {
		if _, err := DecodeQuestion(encoded); !errors.Is(err, ErrInvalidQuestion) {
			t.Fatalf("DecodeQuestion(%q) error = %v, want ErrInvalidQuestion", encoded, err)
		}
	}
}

func TestAnswerCodecRejectsOversizeAnswer(t *testing.T) {
	_, err := EncodeAnswer(AnswerEnvelope{
		Version:    AnswerEnvelopeVersion,
		DocumentID: "doc-1",
		Answer:     strings.Repeat("x", DefaultMaxAnswerBytes+1),
		Source:     "knowledge_base",
	})
	if !errors.Is(err, ErrAnswerTooLarge) {
		t.Fatalf("EncodeAnswer oversized error = %v, want ErrAnswerTooLarge", err)
	}
}

func TestAnswerCodecRejectsEncodedPayloadOverLimit(t *testing.T) {
	_, err := EncodeAnswer(AnswerEnvelope{
		Version:    AnswerEnvelopeVersion,
		DocumentID: "doc-1",
		Answer:     strings.Repeat("\\\"", DefaultMaxAnswerBytes/2),
		Source:     "knowledge_base",
	})
	if !errors.Is(err, ErrAnswerTooLarge) {
		t.Fatalf("EncodeAnswer expanded payload error = %v, want ErrAnswerTooLarge", err)
	}
}

func TestParseVSearchItemRejectsMalformedOrUnsafeFields(t *testing.T) {
	validScope, err := EncodeScope(LogicalScope{Tenant: "t", KnowledgeBase: "kb", CorpusVersion: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	validQuestion, err := EncodeQuestion("question")
	if err != nil {
		t.Fatal(err)
	}
	validAnswer, err := EncodeAnswer(AnswerEnvelope{Version: AnswerEnvelopeVersion, DocumentID: "doc", Answer: "answer", Source: "knowledge_base"})
	if err != nil {
		t.Fatal(err)
	}

	for _, raw := range []string{
		"score:NaN|scope:" + validScope + "|question:" + validQuestion + "|answer:" + validAnswer,
		"score:0.8|scope:" + validScope + "|question:raw|pipe|answer:" + validAnswer,
		"score:0.8|scope:not-base64|question:" + validQuestion + "|answer:" + validAnswer,
		"scope:" + validScope + "|score:0.8|question:" + validQuestion + "|answer:" + validAnswer,
	} {
		if _, err := ParseVSearchItem(raw); !errors.Is(err, ErrInvalidVSearchPayload) {
			t.Fatalf("ParseVSearchItem(%q) error = %v, want ErrInvalidVSearchPayload", raw, err)
		}
	}
}

func TestScopeCodecRequiresBaseFields(t *testing.T) {
	if _, err := EncodeScope(LogicalScope{Tenant: "tenant", KnowledgeBase: "kb"}); !errors.Is(err, ErrInvalidScope) {
		t.Fatalf("EncodeScope missing corpus version error = %v, want ErrInvalidScope", err)
	}
}
