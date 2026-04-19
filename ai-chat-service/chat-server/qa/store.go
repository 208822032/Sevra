package qa

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"ai-chat-service/chat-server/llm"
)

const (
	HitTypeExact      = "exact"
	HitTypeNormalized = "normalized"
	HitTypeAlias      = "alias"
	HitTypeCandidate  = "candidate"
	HitTypeSemantic   = "semantic"

	DefaultScope = "global"

	SemanticRecallModeBruteForce = "brute_force"
	SemanticRecallModeANN        = "ann"

	DefaultCandidateTopK           = 6
	DefaultCandidateMinScore       = 0.62
	DefaultCandidateSignalLimit    = 24
	DefaultCandidateRecallPoolSize = 24
	DefaultCandidateBucketSize     = 64
	CandidateRecallSourceBigram    = "char_bigram"

	DefaultSemanticTopK                = 4
	DefaultSemanticMinScore            = 0.84
	DefaultSemanticCorpusLimit         = 128
	DefaultSemanticEmbeddingDim        = 64
	SemanticRecallSourceBruteForce     = "bruteforce_cosine"
	SemanticRecallSourceANNBucketedLSH = "ann_bucketed_lsh"
	SemanticRecallSourceANNFallback    = "ann_fallback_bruteforce"

	EmbeddingStatusPending = "pending"
	EmbeddingStatusReady   = "ready"
	EmbeddingStatusMissing = "missing"
	EmbeddingStatusFailed  = "failed"
)

type LookupRequest struct {
	Question               string
	ExactQuestionHash      string
	NormalizedQuestion     string
	NormalizedQuestionHash string
	Provider               string
	Model                  string
	Scope                  string
	PromptVersion          string
	CandidateTopK          int
	CandidateMinScore      float64
	SemanticEnabled        bool
	SemanticMode           string
	SemanticTopK           int
	SemanticThreshold      float64
	SemanticModel          string
	SemanticEmbeddingDim   int
	SemanticCorpusLimit    int
	SemanticShadow         SemanticShadowConfig
	Metadata               map[string]string
}

type SemanticShadowConfig struct {
	Enabled        bool
	TopK           int
	ScoreThreshold float64
	LogDiffOnly    bool
	SampleRate     float64
}

type LookupResult struct {
	Hit                         bool
	HitType                     string
	Answer                      string
	Question                    string
	QuestionID                  string
	CanonicalQuestionID         string
	CanonicalQuestion           string
	CanonicalQuestionHash       string
	AliasQuestion               string
	AliasQuestionHash           string
	CandidateCount              int
	CandidateTopScore           float64
	CandidateSource             string
	SemanticCandidateCount      int
	SemanticTopK                int
	SemanticTopScore            float64
	SemanticModel               string
	SemanticSource              string
	SemanticModeRequested       string
	SemanticModeUsed            string
	SemanticFallbackUsed        bool
	SemanticFallbackReason      string
	SemanticLatencyMS           float64
	SemanticANNLatencyMS        float64
	SemanticBruteForceLatencyMS float64
	SemanticShadowTrace         llm.SemanticShadowTrace `json:"semantic_shadow_trace,omitempty"`
	Scope                       string
	Metadata                    map[string]string
}

type Retriever interface {
	Lookup(ctx context.Context, req LookupRequest) (LookupResult, error)
	Close() error
}

type Store interface {
	Retriever
	Upsert(ctx context.Context, record *llm.QARecord, ttl time.Duration) error
	AddAlias(ctx context.Context, canonicalQuestionID, aliasQuestion, scope string, ttl time.Duration) error
}

type SemanticBackfiller interface {
	BackfillSemanticEmbedding(ctx context.Context, record *llm.QARecord) error
}

func NormalizeQuestion(question string) string {
	return llm.NormalizeQuestion(question)
}

func QuestionHash(question string) string {
	return llm.QuestionHash(question)
}

func ExactQuestionHash(question string) string {
	return llm.ExactQuestionHash(question)
}

func BuildScope(provider, model, promptVersion string) string {
	parts := make([]string, 0, 3)
	if trimmed := strings.TrimSpace(provider); trimmed != "" {
		parts = append(parts, "provider="+trimmed)
	}
	if trimmed := strings.TrimSpace(model); trimmed != "" {
		parts = append(parts, "model="+trimmed)
	}
	if trimmed := strings.TrimSpace(promptVersion); trimmed != "" {
		parts = append(parts, "prompt_version="+trimmed)
	}
	if len(parts) == 0 {
		return DefaultScope
	}
	return strings.Join(parts, "|")
}

func BuildQuestionID(scope, exactHash string) string {
	scope = normalizeScope(scope)
	exactHash = strings.TrimSpace(exactHash)
	if exactHash == "" {
		return ""
	}
	return fmt.Sprintf("%s:%s", scope, exactHash)
}

func ExactIndexKey(scope, exactHash string) string {
	return fmt.Sprintf("qexact:%s:%s", normalizeScope(scope), strings.TrimSpace(exactHash))
}

func NormalizedIndexKey(scope, normalizedHash string) string {
	return fmt.Sprintf("qnorm:%s:%s", normalizeScope(scope), strings.TrimSpace(normalizedHash))
}

func AliasIndexKey(scope, aliasHash string) string {
	return fmt.Sprintf("qalias:%s:%s", normalizeScope(scope), strings.TrimSpace(aliasHash))
}

func MainRecordKey(questionID string) string {
	return fmt.Sprintf("qa:%s", strings.TrimSpace(questionID))
}

func normalizeScope(scope string) string {
	if trimmed := strings.TrimSpace(scope); trimmed != "" {
		return trimmed
	}
	return DefaultScope
}

func isHexHash(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		case r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

func normalizeExactHashInput(question string) string {
	trimmed := strings.TrimSpace(question)
	if trimmed == "" {
		return ""
	}
	if isHexHash(trimmed) {
		return strings.ToLower(trimmed)
	}
	return ExactQuestionHash(trimmed)
}

func normalizeNormalizedHashInput(question string) string {
	trimmed := strings.TrimSpace(question)
	if trimmed == "" {
		return ""
	}
	if isHexHash(trimmed) {
		return strings.ToLower(trimmed)
	}
	return QuestionHash(trimmed)
}

func normalizeAliasHashInput(question string) string {
	return normalizeNormalizedHashInput(question)
}

type noopRetriever struct{}

func NewNoopRetriever() Retriever {
	return noopRetriever{}
}

func (noopRetriever) Lookup(ctx context.Context, req LookupRequest) (LookupResult, error) {
	return LookupResult{Hit: false}, nil
}

func (noopRetriever) Close() error {
	return nil
}

func (noopRetriever) Upsert(ctx context.Context, record *llm.QARecord, ttl time.Duration) error {
	return nil
}

func (noopRetriever) AddAlias(ctx context.Context, canonicalQuestionID, aliasQuestion, scope string, ttl time.Duration) error {
	return nil
}

type MemoryRetriever struct {
	mu         sync.RWMutex
	exact      map[string]LookupResult
	normalized map[string]LookupResult
	records    map[string]LookupResult
	aliases    map[string]string
}

func NewMemoryRetriever() *MemoryRetriever {
	return &MemoryRetriever{
		exact:      make(map[string]LookupResult),
		normalized: make(map[string]LookupResult),
		records:    make(map[string]LookupResult),
		aliases:    make(map[string]string),
	}
}

func (m *MemoryRetriever) Close() error {
	return nil
}

func (m *MemoryRetriever) Upsert(ctx context.Context, record *llm.QARecord, ttl time.Duration) error {
	m.PutRecord(record)
	return nil
}

func (m *MemoryRetriever) AddAlias(ctx context.Context, canonicalQuestionID, aliasQuestion, scope string, ttl time.Duration) error {
	if m == nil {
		return errors.New("memory qa store is nil")
	}
	canonicalQuestionID = strings.TrimSpace(canonicalQuestionID)
	aliasQuestion = strings.TrimSpace(aliasQuestion)
	scope = normalizeScope(scope)
	if canonicalQuestionID == "" {
		return errors.New("canonical question id is empty")
	}
	if aliasQuestion == "" {
		return errors.New("alias question is empty")
	}
	aliasHash := normalizeAliasHashInput(aliasQuestion)
	if aliasHash == "" {
		return errors.New("alias hash is empty")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	canonicalRecord, ok := m.records[canonicalQuestionID]
	if !ok {
		return fmt.Errorf("canonical question id %s not found", canonicalQuestionID)
	}
	if canonicalScope := normalizeScope(canonicalRecord.Scope); canonicalScope != scope {
		return fmt.Errorf("alias scope %s does not match canonical scope %s", scope, canonicalScope)
	}
	m.aliases[AliasIndexKey(scope, aliasHash)] = canonicalQuestionID
	return nil
}

func (m *MemoryRetriever) Lookup(ctx context.Context, req LookupRequest) (LookupResult, error) {
	if m == nil {
		return LookupResult{Hit: false}, nil
	}
	question := strings.TrimSpace(req.Question)
	if question == "" {
		return LookupResult{Hit: false}, nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()

	if result, ok := m.exact[normalizeExactHashInput(req.ExactQuestionHash)]; ok {
		return cloneLookupResult(result, HitTypeExact), nil
	}
	if result, ok := m.exact[normalizeExactHashInput(question)]; ok {
		return cloneLookupResult(result, HitTypeExact), nil
	}

	normalizedKey := strings.TrimSpace(req.NormalizedQuestionHash)
	if normalizedKey == "" {
		normalizedKey = normalizeNormalizedHashInput(req.NormalizedQuestion)
	}
	if normalizedKey == "" {
		normalizedKey = normalizeNormalizedHashInput(question)
	}
	if normalizedKey == "" {
		return LookupResult{Hit: false}, nil
	}
	if result, ok := m.normalized[normalizedKey]; ok {
		return cloneLookupResult(result, HitTypeNormalized), nil
	}

	aliasKey := normalizeAliasHashInput(req.Question)
	if aliasKey == "" {
		aliasKey = normalizeAliasHashInput(req.NormalizedQuestion)
	}
	if aliasKey != "" {
		if canonicalQuestionID, ok := m.aliases[AliasIndexKey(normalizeScope(req.Scope), aliasKey)]; ok {
			if canonicalRecord, ok := m.records[canonicalQuestionID]; ok {
				result := cloneLookupResult(canonicalRecord, HitTypeAlias)
				result.AliasQuestion = strings.TrimSpace(firstNonEmpty(req.Question, req.NormalizedQuestion))
				result.AliasQuestionHash = aliasKey
				result.CanonicalQuestionID = firstNonEmpty(result.CanonicalQuestionID, result.QuestionID, canonicalQuestionID)
				result.QuestionID = result.CanonicalQuestionID
				if result.Metadata == nil {
					result.Metadata = map[string]string{}
				}
				result.Metadata["hit_type"] = HitTypeAlias
				result.Metadata["alias_question"] = result.AliasQuestion
				result.Metadata["alias_question_hash"] = aliasKey
				result.Metadata["canonical_question_id"] = result.CanonicalQuestionID
				return result, nil
			}
		}
	}

	return LookupResult{Hit: false}, nil
}

func (m *MemoryRetriever) PutRecord(record *llm.QARecord) {
	if m == nil || record == nil {
		return
	}
	result := lookupResultFromRecord(record)
	question := strings.TrimSpace(firstNonEmpty(record.CanonicalQuestion, record.RawQuestion, record.Question))
	questionID := firstNonEmpty(record.CanonicalQuestionID, record.QuestionID, record.RecordID)
	exactKey := normalizeExactHashInput(firstNonEmpty(record.RawQuestionHash, record.Metadata["raw_question_hash"], question))
	normalizedKey := normalizeNormalizedHashInput(firstNonEmpty(record.QuestionHash, record.Metadata["normalized_question_hash"], record.NormalizedQuestion, question))

	m.mu.Lock()
	defer m.mu.Unlock()
	if questionID != "" {
		m.records[questionID] = cloneLookupResult(result, "")
	}
	if exactKey != "" {
		m.exact[exactKey] = cloneLookupResult(result, HitTypeExact)
	}
	if question != "" {
		m.exact[normalizeExactHashInput(question)] = cloneLookupResult(result, HitTypeExact)
	}
	if normalizedKey != "" {
		m.normalized[normalizedKey] = cloneLookupResult(result, HitTypeNormalized)
	}
}

func (m *MemoryRetriever) PutExact(question string, result LookupResult) {
	if m == nil {
		return
	}
	key := normalizeExactHashInput(question)
	if key == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.exact[key] = cloneLookupResult(result, HitTypeExact)
}

func (m *MemoryRetriever) PutNormalized(question string, result LookupResult) {
	if m == nil {
		return
	}
	key := normalizeNormalizedHashInput(question)
	if key == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.normalized[key] = cloneLookupResult(result, HitTypeNormalized)
}

func lookupResultFromRecord(record *llm.QARecord) LookupResult {
	if record == nil {
		return LookupResult{Hit: false}
	}
	metadata := make(map[string]string, len(record.Metadata)+8)
	for key, value := range record.Metadata {
		metadata[key] = value
	}
	question := firstNonEmpty(record.CanonicalQuestion, record.RawQuestion, record.Question)
	if question != "" {
		metadata["question"] = question
		metadata["raw_question"] = question
		metadata["canonical_question"] = question
	}
	canonicalQuestionID := firstNonEmpty(record.CanonicalQuestionID, record.QuestionID, record.RecordID)
	if canonicalQuestionID != "" {
		metadata["canonical_question_id"] = canonicalQuestionID
	}
	canonicalQuestionHash := firstNonEmpty(record.Metadata["canonical_question_hash"], record.RawQuestionHash, record.QuestionHash)
	if canonicalQuestionHash != "" {
		metadata["canonical_question_hash"] = canonicalQuestionHash
	}
	if record.NormalizedQuestion != "" {
		metadata["normalized_question"] = record.NormalizedQuestion
	}
	if record.QuestionHash != "" {
		metadata["normalized_question_hash"] = record.QuestionHash
	}
	if record.RawQuestionHash != "" {
		metadata["raw_question_hash"] = record.RawQuestionHash
	}
	if record.QuestionID != "" {
		metadata["question_id"] = record.QuestionID
	}
	if record.RecordID != "" {
		metadata["record_id"] = record.RecordID
	}
	if record.CanonicalQuestionID != "" {
		metadata["canonical_question_id"] = record.CanonicalQuestionID
	}
	if record.CanonicalQuestion != "" {
		metadata["canonical_question"] = record.CanonicalQuestion
	}
	if record.Scope != "" {
		metadata["scope"] = record.Scope
	}
	if record.PromptVersion != "" {
		metadata["prompt_version"] = record.PromptVersion
	}
	if record.Provider != "" {
		metadata["provider"] = record.Provider
	}
	if record.Model != "" {
		metadata["model"] = record.Model
	}
	if record.HitType != "" {
		metadata["hit_type"] = record.HitType
	}
	if record.HitCount > 0 {
		metadata["hit_count"] = fmt.Sprintf("%d", record.HitCount)
	}
	if record.LastHitAt > 0 {
		metadata["last_hit_at"] = fmt.Sprintf("%d", record.LastHitAt)
	}
	if answerSource := strings.TrimSpace(record.Metadata["answer_source"]); answerSource != "" {
		metadata["answer_source"] = answerSource
	}
	if record.EmbeddingModel != "" {
		metadata["embedding_model"] = record.EmbeddingModel
	}
	if record.EmbeddingStatus != "" {
		metadata["embedding_status"] = record.EmbeddingStatus
	}
	if record.EmbeddingDim > 0 {
		metadata["embedding_dim"] = fmt.Sprintf("%d", record.EmbeddingDim)
	}
	if record.EmbeddingUpdatedAt > 0 {
		metadata["embedding_updated_at"] = fmt.Sprintf("%d", record.EmbeddingUpdatedAt)
	}
	if record.EmbeddingRef != "" {
		metadata["embedding_ref"] = record.EmbeddingRef
	}
	if record.SemanticReady || record.EmbeddingStatus == EmbeddingStatusReady {
		metadata["semantic_ready"] = "true"
	}
	scope := firstNonEmpty(record.Scope, record.Metadata["scope"])
	if scope == "" {
		scope = BuildScope(record.Provider, record.Model, firstNonEmpty(record.PromptVersion, record.Metadata["prompt_version"]))
	}
	return LookupResult{
		Hit:                   true,
		Answer:                record.Answer,
		Question:              question,
		QuestionID:            canonicalQuestionID,
		CanonicalQuestionID:   canonicalQuestionID,
		CanonicalQuestion:     question,
		CanonicalQuestionHash: canonicalQuestionHash,
		Scope:                 scope,
		Metadata:              metadata,
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func cloneLookupResult(result LookupResult, hitType string) LookupResult {
	cloned := result
	if hitType != "" {
		cloned.HitType = hitType
	}
	cloned.SemanticShadowTrace = cloneSemanticShadowTrace(cloned.SemanticShadowTrace)
	if cloned.Metadata != nil {
		metadata := make(map[string]string, len(cloned.Metadata))
		for key, value := range cloned.Metadata {
			metadata[key] = value
		}
		cloned.Metadata = metadata
	}
	cloned.Hit = true
	return cloned
}

func cloneSemanticShadowTrace(trace llm.SemanticShadowTrace) llm.SemanticShadowTrace {
	cloned := trace
	if trace.ShadowSemanticTopHits != nil {
		cloned.ShadowSemanticTopHits = make([]llm.RetrievalHit, len(trace.ShadowSemanticTopHits))
		copy(cloned.ShadowSemanticTopHits, trace.ShadowSemanticTopHits)
	}
	return cloned
}
