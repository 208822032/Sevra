package qa

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-chat-service/chat-server/llm"
)

type memoryKVLiteBackend struct {
	mu       sync.RWMutex
	values   map[string]string
	expires  map[string]time.Time
	getErr   error
	setErr   error
	delErr   error
	getErrFn func(string) error
	setErrFn func(string, string, time.Duration) error
	delErrFn func(string) error
	name     string
}

func newMemoryKVLiteBackend() *memoryKVLiteBackend {
	return &memoryKVLiteBackend{
		values:  make(map[string]string),
		expires: make(map[string]time.Time),
		name:    "memory-kvlite",
	}
}

func newCandidateQARecord(question, answer, provider, model, scope string) *llm.QARecord {
	questionID := BuildQuestionID(scope, ExactQuestionHash(question))
	return &llm.QARecord{
		QuestionID:          questionID,
		RecordID:            questionID,
		CanonicalQuestionID: questionID,
		Question:            question,
		RawQuestion:         question,
		CanonicalQuestion:   question,
		QuestionHash:        QuestionHash(question),
		RawQuestionHash:     ExactQuestionHash(question),
		NormalizedQuestion:  NormalizeQuestion(question),
		Answer:              answer,
		Provider:            provider,
		Model:               model,
		Scope:               scope,
		SourceType:          llm.SourceTypeAPI,
		Metadata:            map[string]string{"answer_source": llm.FinalAnswerSourceLLM},
	}
}

func (m *memoryKVLiteBackend) Name() string { return m.name }
func (m *memoryKVLiteBackend) Close() error { return nil }

func (m *memoryKVLiteBackend) RGet(ctx context.Context, key string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.getErrFn != nil {
		if err := m.getErrFn(key); err != nil {
			return "", err
		}
	}
	if m.getErr != nil {
		return "", m.getErr
	}
	if expiry, ok := m.expires[key]; ok && !expiry.IsZero() && time.Now().After(expiry) {
		delete(m.values, key)
		delete(m.expires, key)
		return "", nil
	}
	value, ok := m.values[key]
	if !ok {
		return "", nil
	}
	return value, nil
}

func (m *memoryKVLiteBackend) RSet(ctx context.Context, key, value string, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.setErrFn != nil {
		if err := m.setErrFn(key, value, ttl); err != nil {
			return err
		}
	}
	if m.setErr != nil {
		return m.setErr
	}
	m.values[key] = value
	if ttl > 0 {
		m.expires[key] = time.Now().Add(ttl)
	} else {
		delete(m.expires, key)
	}
	return nil
}

func (m *memoryKVLiteBackend) Del(ctx context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.delErrFn != nil {
		if err := m.delErrFn(key); err != nil {
			return err
		}
	}
	if m.delErr != nil {
		return m.delErr
	}
	delete(m.values, key)
	delete(m.expires, key)
	return nil
}

func TestExactAndNormalizedQuestionHashesDiffer(t *testing.T) {
	question := "How to cache answers?"
	if ExactQuestionHash(question) == QuestionHash(question) {
		t.Fatalf("expected exact and normalized hashes to differ for punctuation changes")
	}
}

func TestKVLiteStoreExactNormalizedAndScopeIsolation(t *testing.T) {
	backend := newMemoryKVLiteBackend()
	store := NewKVLiteStoreWithBackend(backend, "test:qa", time.Minute)
	question := "How do I cache answers?"
	scope := BuildScope("deepseek", "deepseek-chat", "v1")
	record := &llm.QARecord{
		Question:        question,
		RawQuestion:     question,
		QuestionHash:    QuestionHash(question),
		RawQuestionHash: ExactQuestionHash(question),
		Answer:          "exact answer",
		Provider:        "deepseek",
		Model:           "deepseek-chat",
		Scope:           scope,
		PromptVersion:   "v1",
		SourceType:      llm.SourceTypeAPI,
		Metadata:        map[string]string{"answer_source": llm.FinalAnswerSourceLLM},
	}
	if err := store.Upsert(context.Background(), record, time.Minute); err != nil {
		t.Fatalf("upsert qa record: %v", err)
	}

	exactHit, err := store.Lookup(context.Background(), LookupRequest{
		Question:               question,
		ExactQuestionHash:      ExactQuestionHash(question),
		NormalizedQuestionHash: QuestionHash(question),
		Provider:               "deepseek",
		Model:                  "deepseek-chat",
		PromptVersion:          "v1",
		Scope:                  scope,
	})
	if err != nil {
		t.Fatalf("exact lookup: %v", err)
	}
	if !exactHit.Hit || exactHit.HitType != HitTypeExact {
		t.Fatalf("expected exact hit, got %+v", exactHit)
	}
	if exactHit.Answer != "exact answer" {
		t.Fatalf("unexpected exact answer: %+v", exactHit)
	}

	normalizedHit, err := store.Lookup(context.Background(), LookupRequest{
		Question:               "How do I cache answers!!!",
		ExactQuestionHash:      ExactQuestionHash("How do I cache answers!!!"),
		NormalizedQuestionHash: QuestionHash("How do I cache answers!!!"),
		Provider:               "deepseek",
		Model:                  "deepseek-chat",
		PromptVersion:          "v1",
		Scope:                  scope,
	})
	if err != nil {
		t.Fatalf("normalized lookup: %v", err)
	}
	if !normalizedHit.Hit || normalizedHit.HitType != HitTypeNormalized {
		t.Fatalf("expected normalized hit, got %+v", normalizedHit)
	}
	if normalizedHit.Answer != "exact answer" {
		t.Fatalf("unexpected normalized answer: %+v", normalizedHit)
	}

	otherScopeHit, err := store.Lookup(context.Background(), LookupRequest{
		Question:               question,
		ExactQuestionHash:      ExactQuestionHash(question),
		NormalizedQuestionHash: QuestionHash(question),
		Provider:               "openai",
		Model:                  "gpt-4o-mini",
		PromptVersion:          "v1",
		Scope:                  BuildScope("openai", "gpt-4o-mini", "v1"),
	})
	if err != nil {
		t.Fatalf("scope isolation lookup: %v", err)
	}
	if otherScopeHit.Hit {
		t.Fatalf("expected scope isolation miss, got %+v", otherScopeHit)
	}
}

func TestKVLiteStoreTTLExpiresEntries(t *testing.T) {
	backend := newMemoryKVLiteBackend()
	store := NewKVLiteStoreWithBackend(backend, "test:qa", 50*time.Millisecond)
	question := "What is TTL?"
	record := &llm.QARecord{
		Question:        question,
		RawQuestion:     question,
		QuestionHash:    QuestionHash(question),
		RawQuestionHash: ExactQuestionHash(question),
		Answer:          "ttl answer",
		Provider:        "deepseek",
		Model:           "deepseek-chat",
		Scope:           BuildScope("deepseek", "deepseek-chat", ""),
		SourceType:      llm.SourceTypeAPI,
		Metadata:        map[string]string{"answer_source": llm.FinalAnswerSourceLLM},
	}
	if err := store.Upsert(context.Background(), record, 50*time.Millisecond); err != nil {
		t.Fatalf("upsert ttl record: %v", err)
	}
	hit, err := store.Lookup(context.Background(), LookupRequest{
		Question:               question,
		ExactQuestionHash:      ExactQuestionHash(question),
		NormalizedQuestionHash: QuestionHash(question),
		Provider:               "deepseek",
		Model:                  "deepseek-chat",
		Scope:                  BuildScope("deepseek", "deepseek-chat", ""),
	})
	if err != nil {
		t.Fatalf("ttl lookup before expiry: %v", err)
	}
	if !hit.Hit {
		t.Fatalf("expected hit before ttl expiry")
	}
	time.Sleep(80 * time.Millisecond)
	hit, err = store.Lookup(context.Background(), LookupRequest{
		Question:               question,
		ExactQuestionHash:      ExactQuestionHash(question),
		NormalizedQuestionHash: QuestionHash(question),
		Provider:               "deepseek",
		Model:                  "deepseek-chat",
		Scope:                  BuildScope("deepseek", "deepseek-chat", ""),
	})
	if err != nil {
		t.Fatalf("ttl lookup after expiry: %v", err)
	}
	if hit.Hit {
		t.Fatalf("expected miss after ttl expiry, got %+v", hit)
	}
}

func TestKVLiteStoreLookupBackendError(t *testing.T) {
	backend := newMemoryKVLiteBackend()
	backend.getErr = errors.New("backend read failure")
	store := NewKVLiteStoreWithBackend(backend, "test:qa", time.Minute)
	_, err := store.Lookup(context.Background(), LookupRequest{
		Question:               "read error",
		ExactQuestionHash:      ExactQuestionHash("read error"),
		NormalizedQuestionHash: QuestionHash("read error"),
		Scope:                  BuildScope("deepseek", "deepseek-chat", ""),
		Provider:               "deepseek",
		Model:                  "deepseek-chat",
	})
	if err == nil {
		t.Fatalf("expected lookup error")
	}
}

func TestKVLiteStoreAliasLookupReturnsCanonicalAnswer(t *testing.T) {
	backend := newMemoryKVLiteBackend()
	store := NewKVLiteStoreWithBackend(backend, "test:qa", time.Minute)
	scope := BuildScope("deepseek", "deepseek-chat", "v1")
	canonicalQuestion := "How do I keep the canonical answer?"
	canonicalID := BuildQuestionID(scope, ExactQuestionHash(canonicalQuestion))
	record := &llm.QARecord{
		QuestionID:          canonicalID,
		RecordID:            canonicalID,
		CanonicalQuestionID: canonicalID,
		Question:            canonicalQuestion,
		RawQuestion:         canonicalQuestion,
		CanonicalQuestion:   canonicalQuestion,
		QuestionHash:        QuestionHash(canonicalQuestion),
		RawQuestionHash:     ExactQuestionHash(canonicalQuestion),
		NormalizedQuestion:  NormalizeQuestion(canonicalQuestion),
		Answer:              "canonical answer",
		Provider:            "deepseek",
		Model:               "deepseek-chat",
		Scope:               scope,
		PromptVersion:       "v1",
		SourceType:          llm.SourceTypeAPI,
		Metadata:            map[string]string{"answer_source": llm.FinalAnswerSourceLLM},
	}
	if err := store.Upsert(context.Background(), record, time.Minute); err != nil {
		t.Fatalf("upsert canonical record: %v", err)
	}
	aliasQuestion := "How do I keep the answer canonical?"
	if err := store.AddAlias(context.Background(), canonicalID, aliasQuestion, scope, time.Minute); err != nil {
		t.Fatalf("add alias: %v", err)
	}

	hit, err := store.Lookup(context.Background(), LookupRequest{
		Question:               aliasQuestion,
		ExactQuestionHash:      ExactQuestionHash(aliasQuestion),
		NormalizedQuestionHash: QuestionHash(aliasQuestion),
		Provider:               "deepseek",
		Model:                  "deepseek-chat",
		PromptVersion:          "v1",
		Scope:                  scope,
	})
	if err != nil {
		t.Fatalf("alias lookup: %v", err)
	}
	if !hit.Hit || hit.HitType != HitTypeAlias {
		t.Fatalf("expected alias hit, got %+v", hit)
	}
	if hit.Answer != "canonical answer" {
		t.Fatalf("unexpected alias answer: %+v", hit)
	}
	if hit.QuestionID != canonicalID || hit.CanonicalQuestionID != canonicalID {
		t.Fatalf("expected canonical question id on alias hit, got %+v", hit)
	}
	if hit.AliasQuestion != aliasQuestion {
		t.Fatalf("unexpected alias question on hit: %+v", hit)
	}
	if hit.Metadata["alias_question"] != aliasQuestion {
		t.Fatalf("expected alias question metadata, got %+v", hit.Metadata)
	}
}

func TestKVLiteStoreMultipleAliasesShareCanonicalRecord(t *testing.T) {
	backend := newMemoryKVLiteBackend()
	store := NewKVLiteStoreWithBackend(backend, "test:qa", time.Minute)
	scope := BuildScope("deepseek", "deepseek-chat", "v1")
	canonicalQuestion := "What is the authoritative answer?"
	canonicalID := BuildQuestionID(scope, ExactQuestionHash(canonicalQuestion))
	record := &llm.QARecord{
		QuestionID:          canonicalID,
		RecordID:            canonicalID,
		CanonicalQuestionID: canonicalID,
		Question:            canonicalQuestion,
		RawQuestion:         canonicalQuestion,
		CanonicalQuestion:   canonicalQuestion,
		QuestionHash:        QuestionHash(canonicalQuestion),
		RawQuestionHash:     ExactQuestionHash(canonicalQuestion),
		NormalizedQuestion:  NormalizeQuestion(canonicalQuestion),
		Answer:              "authoritative answer",
		Provider:            "deepseek",
		Model:               "deepseek-chat",
		Scope:               scope,
		PromptVersion:       "v1",
		SourceType:          llm.SourceTypeAPI,
		Metadata:            map[string]string{"answer_source": llm.FinalAnswerSourceLLM},
	}
	if err := store.Upsert(context.Background(), record, time.Minute); err != nil {
		t.Fatalf("upsert canonical record: %v", err)
	}
	aliases := []string{"What is the real answer?", "Show me the real answer"}
	for _, aliasQuestion := range aliases {
		if err := store.AddAlias(context.Background(), canonicalID, aliasQuestion, scope, time.Minute); err != nil {
			t.Fatalf("add alias %q: %v", aliasQuestion, err)
		}
		hit, err := store.Lookup(context.Background(), LookupRequest{
			Question:               aliasQuestion,
			ExactQuestionHash:      ExactQuestionHash(aliasQuestion),
			NormalizedQuestionHash: QuestionHash(aliasQuestion),
			Provider:               "deepseek",
			Model:                  "deepseek-chat",
			PromptVersion:          "v1",
			Scope:                  scope,
		})
		if err != nil {
			t.Fatalf("alias lookup %q: %v", aliasQuestion, err)
		}
		if !hit.Hit || hit.QuestionID != canonicalID || hit.Answer != "authoritative answer" {
			t.Fatalf("unexpected alias hit for %q: %+v", aliasQuestion, hit)
		}
	}

	backend.mu.RLock()
	defer backend.mu.RUnlock()
	mainRecordCount := 0
	for key := range backend.values {
		if strings.Contains(key, ":qa:") && strings.HasPrefix(key, "test:qa:qa:") {
			mainRecordCount++
		}
	}
	if mainRecordCount != 1 {
		t.Fatalf("expected one canonical main record, got %d", mainRecordCount)
	}
}

func TestKVLiteStoreAliasScopeIsolation(t *testing.T) {
	backend := newMemoryKVLiteBackend()
	store := NewKVLiteStoreWithBackend(backend, "test:qa", time.Minute)
	canonicalQuestion := "How do I isolate alias scope?"
	scopeA := BuildScope("deepseek", "deepseek-chat", "v1")
	canonicalID := BuildQuestionID(scopeA, ExactQuestionHash(canonicalQuestion))
	record := &llm.QARecord{
		QuestionID:          canonicalID,
		RecordID:            canonicalID,
		CanonicalQuestionID: canonicalID,
		Question:            canonicalQuestion,
		RawQuestion:         canonicalQuestion,
		CanonicalQuestion:   canonicalQuestion,
		QuestionHash:        QuestionHash(canonicalQuestion),
		RawQuestionHash:     ExactQuestionHash(canonicalQuestion),
		NormalizedQuestion:  NormalizeQuestion(canonicalQuestion),
		Answer:              "scope answer",
		Provider:            "deepseek",
		Model:               "deepseek-chat",
		Scope:               scopeA,
		PromptVersion:       "v1",
		SourceType:          llm.SourceTypeAPI,
		Metadata:            map[string]string{"answer_source": llm.FinalAnswerSourceLLM},
	}
	if err := store.Upsert(context.Background(), record, time.Minute); err != nil {
		t.Fatalf("upsert canonical record: %v", err)
	}
	aliasQuestion := "How do I isolate the answer?"
	if err := store.AddAlias(context.Background(), canonicalID, aliasQuestion, scopeA, time.Minute); err != nil {
		t.Fatalf("add alias: %v", err)
	}

	hit, err := store.Lookup(context.Background(), LookupRequest{
		Question:               aliasQuestion,
		ExactQuestionHash:      ExactQuestionHash(aliasQuestion),
		NormalizedQuestionHash: QuestionHash(aliasQuestion),
		Provider:               "openai",
		Model:                  "gpt-4o-mini",
		PromptVersion:          "v1",
		Scope:                  BuildScope("openai", "gpt-4o-mini", "v1"),
	})
	if err != nil {
		t.Fatalf("scope isolation lookup: %v", err)
	}
	if hit.Hit {
		t.Fatalf("expected scope isolation miss, got %+v", hit)
	}
}

func TestKVLiteStoreAliasLookupBackendError(t *testing.T) {
	backend := newMemoryKVLiteBackend()
	backend.getErrFn = func(key string) error {
		if strings.Contains(key, "qalias:") {
			return errors.New("alias backend failure")
		}
		return nil
	}
	store := NewKVLiteStoreWithBackend(backend, "test:qa", time.Minute)
	scope := BuildScope("deepseek", "deepseek-chat", "v1")
	canonicalQuestion := "How do alias failures behave?"
	canonicalID := BuildQuestionID(scope, ExactQuestionHash(canonicalQuestion))
	record := &llm.QARecord{
		QuestionID:          canonicalID,
		RecordID:            canonicalID,
		CanonicalQuestionID: canonicalID,
		Question:            canonicalQuestion,
		RawQuestion:         canonicalQuestion,
		CanonicalQuestion:   canonicalQuestion,
		QuestionHash:        QuestionHash(canonicalQuestion),
		RawQuestionHash:     ExactQuestionHash(canonicalQuestion),
		NormalizedQuestion:  NormalizeQuestion(canonicalQuestion),
		Answer:              "failure answer",
		Provider:            "deepseek",
		Model:               "deepseek-chat",
		Scope:               scope,
		PromptVersion:       "v1",
		SourceType:          llm.SourceTypeAPI,
		Metadata:            map[string]string{"answer_source": llm.FinalAnswerSourceLLM},
	}
	if err := store.Upsert(context.Background(), record, time.Minute); err != nil {
		t.Fatalf("upsert canonical record: %v", err)
	}
	aliasQuestion := "How do alias failures happen?"
	if err := store.AddAlias(context.Background(), canonicalID, aliasQuestion, scope, time.Minute); err != nil {
		t.Fatalf("add alias: %v", err)
	}

	_, err := store.Lookup(context.Background(), LookupRequest{
		Question:               aliasQuestion,
		ExactQuestionHash:      ExactQuestionHash(aliasQuestion),
		NormalizedQuestionHash: QuestionHash(aliasQuestion),
		Provider:               "deepseek",
		Model:                  "deepseek-chat",
		PromptVersion:          "v1",
		Scope:                  scope,
	})
	if err == nil {
		t.Fatalf("expected alias lookup error")
	}
}

func TestKVLiteStoreCandidateLookupReturnsBestMatch(t *testing.T) {
	backend := newMemoryKVLiteBackend()
	store := NewKVLiteStoreWithBackend(backend, "test:qa", time.Minute)
	scope := BuildScope("deepseek", "deepseek-chat", "v1")
	recordA := newCandidateQARecord("How do I flush the Redis cache safely?", "candidate A answer", "deepseek", "deepseek-chat", scope)
	recordB := newCandidateQARecord("How do I clear the Redis cache safely?", "candidate B answer", "deepseek", "deepseek-chat", scope)
	if err := store.Upsert(context.Background(), recordA, time.Minute); err != nil {
		t.Fatalf("upsert candidate A: %v", err)
	}
	if err := store.Upsert(context.Background(), recordB, time.Minute); err != nil {
		t.Fatalf("upsert candidate B: %v", err)
	}

	query := "How do I flush the Redis cache quickly?"
	hit, err := store.Lookup(context.Background(), LookupRequest{
		Question:               query,
		ExactQuestionHash:      ExactQuestionHash(query),
		NormalizedQuestionHash: QuestionHash(query),
		Provider:               "deepseek",
		Model:                  "deepseek-chat",
		PromptVersion:          "v1",
		Scope:                  scope,
	})
	if err != nil {
		t.Fatalf("candidate lookup: %v", err)
	}
	if !hit.Hit || hit.HitType != HitTypeCandidate {
		t.Fatalf("expected candidate hit, got %+v", hit)
	}
	if hit.Answer != "candidate A answer" {
		t.Fatalf("unexpected candidate answer: %+v", hit)
	}
	if hit.QuestionID != recordA.QuestionID || hit.CanonicalQuestionID != recordA.CanonicalQuestionID {
		t.Fatalf("expected candidate A record ids, got %+v", hit)
	}
	if hit.CandidateCount < 2 {
		t.Fatalf("expected at least two recalled candidates, got %+v", hit)
	}
	if hit.CandidateTopScore <= 0.7 {
		t.Fatalf("expected a strong candidate score, got %+v", hit)
	}
	if hit.CandidateSource != CandidateRecallSourceBigram {
		t.Fatalf("unexpected candidate source: %+v", hit)
	}
	if hit.Metadata["candidate_source"] != CandidateRecallSourceBigram {
		t.Fatalf("expected candidate source metadata, got %+v", hit.Metadata)
	}
}

func TestKVLiteStoreCandidateLookupRespectsScopeIsolation(t *testing.T) {
	backend := newMemoryKVLiteBackend()
	store := NewKVLiteStoreWithBackend(backend, "test:qa", time.Minute)
	scopeA := BuildScope("deepseek", "deepseek-chat", "v1")
	scopeB := BuildScope("openai", "gpt-4o-mini", "v1")
	record := newCandidateQARecord("How do I flush the Redis cache safely?", "scoped candidate answer", "deepseek", "deepseek-chat", scopeA)
	if err := store.Upsert(context.Background(), record, time.Minute); err != nil {
		t.Fatalf("upsert scoped candidate: %v", err)
	}

	query := "How do I flush the Redis cache quickly?"
	hit, err := store.Lookup(context.Background(), LookupRequest{
		Question:               query,
		ExactQuestionHash:      ExactQuestionHash(query),
		NormalizedQuestionHash: QuestionHash(query),
		Provider:               "openai",
		Model:                  "gpt-4o-mini",
		PromptVersion:          "v1",
		Scope:                  scopeB,
	})
	if err != nil {
		t.Fatalf("candidate scope lookup: %v", err)
	}
	if hit.Hit {
		t.Fatalf("expected scope-isolated miss, got %+v", hit)
	}
}

func TestKVLiteStoreCandidateLookupBackendError(t *testing.T) {
	backend := newMemoryKVLiteBackend()
	store := NewKVLiteStoreWithBackend(backend, "test:qa", time.Minute)
	scope := BuildScope("deepseek", "deepseek-chat", "v1")
	record := newCandidateQARecord("How do I flush the Redis cache safely?", "candidate answer", "deepseek", "deepseek-chat", scope)
	if err := store.Upsert(context.Background(), record, time.Minute); err != nil {
		t.Fatalf("upsert candidate record: %v", err)
	}
	backend.getErrFn = func(key string) error {
		if strings.Contains(key, "qcand:") {
			return errors.New("candidate backend failure")
		}
		return nil
	}

	query := "How do I flush the Redis cache quickly?"
	_, err := store.Lookup(context.Background(), LookupRequest{
		Question:               query,
		ExactQuestionHash:      ExactQuestionHash(query),
		NormalizedQuestionHash: QuestionHash(query),
		Provider:               "deepseek",
		Model:                  "deepseek-chat",
		PromptVersion:          "v1",
		Scope:                  scope,
	})
	if err == nil {
		t.Fatalf("expected candidate lookup error")
	}
}

func TestKVLiteStoreCandidateWriteBackendError(t *testing.T) {
	backend := newMemoryKVLiteBackend()
	backend.setErrFn = func(key, value string, ttl time.Duration) error {
		if strings.Contains(key, "qcand:") {
			return errors.New("candidate write failure")
		}
		return nil
	}
	store := NewKVLiteStoreWithBackend(backend, "test:qa", time.Minute)
	scope := BuildScope("deepseek", "deepseek-chat", "v1")
	record := newCandidateQARecord("How do I flush the Redis cache safely?", "candidate answer", "deepseek", "deepseek-chat", scope)
	if err := store.Upsert(context.Background(), record, time.Minute); err == nil {
		t.Fatalf("expected candidate write error")
	}
}
