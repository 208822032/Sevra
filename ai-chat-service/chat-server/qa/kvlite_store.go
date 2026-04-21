package qa

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"ai-chat-service/chat-server/llm"
	"ai-chat-service/pkg/config"
)

type kvliteBackend interface {
	RGet(ctx context.Context, key string) (string, error)
	RSet(ctx context.Context, key, value string, ttl time.Duration) error
	Del(ctx context.Context, key string) error
	Close() error
	Name() string
}

type KVLiteStore struct {
	backend    kvliteBackend
	namespace  string
	defaultTTL time.Duration

	semanticEnabled          bool
	semanticTopK             int
	semanticThreshold        float64
	semanticANNTopK          int
	semanticANNMinScoreGap   float64
	semanticCorpusLimit      int
	semanticEmbeddingDim     int
	semanticEmbeddingProfile string
	semanticModel            string
	semanticProvider         llm.EmbeddingProvider
	semanticANNAudit         *SemanticANNAuditTrail

	semanticANNCacheMu sync.RWMutex
	semanticANNCache   map[string]*semanticANNIndex
}

type KVLiteOption func(*KVLiteStore)

func WithKVLiteBackend(backend kvliteBackend) KVLiteOption {
	return func(store *KVLiteStore) {
		if store != nil && backend != nil {
			store.backend = backend
		}
	}
}

func WithKVLiteNamespace(namespace string) KVLiteOption {
	return func(store *KVLiteStore) {
		if store == nil {
			return
		}
		namespace = strings.TrimSpace(namespace)
		if namespace != "" {
			store.namespace = namespace
		}
	}
}

func WithKVLiteDefaultTTL(ttl time.Duration) KVLiteOption {
	return func(store *KVLiteStore) {
		if store != nil && ttl > 0 {
			store.defaultTTL = ttl
		}
	}
}

func WithKVLiteSemanticProvider(provider llm.EmbeddingProvider) KVLiteOption {
	return func(store *KVLiteStore) {
		if store != nil && provider != nil {
			store.semanticProvider = provider
		}
	}
}

func WithKVLiteSemanticConfig(cfg SemanticConfig) KVLiteOption {
	return func(store *KVLiteStore) {
		if store == nil {
			return
		}
		store.applySemanticConfig(cfg)
	}
}

func NewKVLiteStore(cnf *config.Config, options ...KVLiteOption) (Store, error) {
	store := &KVLiteStore{
		namespace:              defaultQANamespace(cnf),
		defaultTTL:             defaultQATTL(cnf),
		semanticANNTopK:        DefaultSemanticANNTopK,
		semanticANNMinScoreGap: DefaultSemanticANNMinScoreGap,
		semanticANNAudit:       newSemanticANNAuditTrail(SemanticANNAuditConfig{}),
		semanticANNCache:       make(map[string]*semanticANNIndex),
	}
	if cnf != nil {
		store.applySemanticConfig(semanticConfigFromConfig(cnf))
		store.initSemanticProvider(cnf)
	}
	for _, option := range options {
		if option != nil {
			option(store)
		}
	}
	if store.backend == nil {
		if cnf == nil || strings.TrimSpace(cnf.Context.KVLite.Addr) == "" {
			return nil, errors.New("kvlite qa store is not configured")
		}
		store.backend = newKVLiteBackend(cnf, store.namespace)
	}
	if store.backend == nil {
		return nil, errors.New("kvlite qa store backend is not available")
	}
	return store, nil
}

func NewKVLiteStoreWithBackend(backend kvliteBackend, namespace string, ttl time.Duration, options ...KVLiteOption) Store {
	store := &KVLiteStore{
		backend:                backend,
		namespace:              normalizeQANamespace(namespace),
		defaultTTL:             ttl,
		semanticANNTopK:        DefaultSemanticANNTopK,
		semanticANNMinScoreGap: DefaultSemanticANNMinScoreGap,
		semanticANNAudit:       newSemanticANNAuditTrail(SemanticANNAuditConfig{}),
		semanticANNCache:       make(map[string]*semanticANNIndex),
	}
	for _, option := range options {
		if option != nil {
			option(store)
		}
	}
	return store
}

func (s *KVLiteStore) Name() string {
	if s == nil || s.backend == nil {
		return "unknown"
	}
	return s.backend.Name()
}

func (s *KVLiteStore) Close() error {
	if s == nil || s.backend == nil {
		if s != nil && s.semanticANNAudit != nil {
			_ = s.semanticANNAudit.Close()
		}
		return nil
	}
	if s.semanticANNAudit != nil {
		_ = s.semanticANNAudit.Close()
	}
	return s.backend.Close()
}

func (s *KVLiteStore) Upsert(ctx context.Context, record *llm.QARecord, ttl time.Duration) error {
	if s == nil || s.backend == nil || record == nil {
		return nil
	}
	stored, err := normalizeQARecordForStore(record, ttl, s.defaultTTL)
	if err != nil {
		return err
	}
	if s.semanticEnabled {
		if stored.EmbeddingStatus == "" {
			stored.EmbeddingStatus = EmbeddingStatusPending
		}
		if stored.EmbeddingModel == "" {
			stored.EmbeddingModel = s.semanticModel
		}
		if stored.EmbeddingDim <= 0 {
			stored.EmbeddingDim = s.semanticEmbeddingDim
		}
		if stored.Metadata == nil {
			stored.Metadata = map[string]string{}
		}
		stored.Metadata["embedding_status"] = stored.EmbeddingStatus
		if stored.EmbeddingModel != "" {
			stored.Metadata["embedding_model"] = stored.EmbeddingModel
		}
		if stored.EmbeddingDim > 0 {
			stored.Metadata["embedding_dim"] = fmt.Sprintf("%d", stored.EmbeddingDim)
		}
	}
	payload, err := json.Marshal(stored)
	if err != nil {
		return err
	}
	writeTTL := ttlForWrite(stored, ttl, s.defaultTTL)
	mainKey := s.recordKey(stored.QuestionID)
	exactKey := s.exactIndexKey(stored.Scope, stored.RawQuestionHash)
	normalizedKey := s.normalizedIndexKey(stored.Scope, stored.QuestionHash)
	written := make([]string, 0, 3)
	if err := s.backend.RSet(ctx, mainKey, string(payload), writeTTL); err != nil {
		return err
	}
	written = append(written, mainKey)
	if err := s.backend.RSet(ctx, exactKey, stored.QuestionID, writeTTL); err != nil {
		s.rollback(ctx, written)
		return err
	}
	written = append(written, exactKey)
	if err := s.backend.RSet(ctx, normalizedKey, stored.QuestionID, writeTTL); err != nil {
		s.rollback(ctx, written)
		return err
	}
	if err := s.writeCandidateIndexes(ctx, stored, writeTTL); err != nil {
		return err
	}
	if s.semanticEnabled && (stored.SemanticReady || stored.EmbeddingStatus == EmbeddingStatusReady) {
		s.invalidateSemanticANNIndex(stored.Scope)
	}
	return nil
}

func (s *KVLiteStore) AddAlias(ctx context.Context, canonicalQuestionID, aliasQuestion, scope string, ttl time.Duration) error {
	if s == nil || s.backend == nil {
		return nil
	}
	canonicalQuestionID = strings.TrimSpace(canonicalQuestionID)
	aliasQuestion = strings.TrimSpace(aliasQuestion)
	scope = normalizeQANamespace(scope)
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
	canonicalRecord, err := s.loadRecord(ctx, canonicalQuestionID)
	if err != nil {
		return err
	}
	if canonicalRecord == nil {
		return fmt.Errorf("canonical question %s not found", canonicalQuestionID)
	}
	canonicalScope := normalizeQANamespace(canonicalRecord.Scope)
	if canonicalScope != scope {
		return fmt.Errorf("alias scope %s does not match canonical scope %s", scope, canonicalScope)
	}
	writeTTL := ttlForAliasWrite(canonicalRecord, ttl, s.defaultTTL)
	aliasKey := s.aliasIndexKey(scope, aliasHash)
	if err := s.backend.RSet(ctx, aliasKey, canonicalQuestionID, writeTTL); err != nil {
		return err
	}
	return nil
}

func (s *KVLiteStore) Lookup(ctx context.Context, req LookupRequest) (LookupResult, error) {
	if s == nil || s.backend == nil {
		return LookupResult{Hit: false}, nil
	}
	scope := effectiveLookupScope(req)
	if exactHash := normalizeExactHashInput(firstNonEmpty(req.ExactQuestionHash, req.Question)); exactHash != "" {
		if result, ok, err := s.lookupByIndex(ctx, s.exactIndexKey(scope, exactHash), HitTypeExact); err != nil {
			return LookupResult{}, err
		} else if ok {
			return result, nil
		}
	}
	normalizedHash := normalizeNormalizedHashInput(firstNonEmpty(req.NormalizedQuestionHash, req.NormalizedQuestion, req.Question))
	if normalizedHash != "" {
		if result, ok, err := s.lookupByIndex(ctx, s.normalizedIndexKey(scope, normalizedHash), HitTypeNormalized); err != nil {
			return LookupResult{}, err
		} else if ok {
			return result, nil
		}
	}
	if aliasHash := normalizeAliasHashInput(firstNonEmpty(req.Question, req.NormalizedQuestion)); aliasHash != "" {
		if result, ok, err := s.lookupByAlias(ctx, scope, aliasHash, firstNonEmpty(req.Question, req.NormalizedQuestion)); err != nil {
			return LookupResult{}, err
		} else if ok {
			return result, nil
		}
	}
	if result, ok, err := s.lookupByCandidate(ctx, req); err != nil {
		return LookupResult{}, err
	} else if ok {
		return result, nil
	}
	if result, ok, err := s.lookupBySemantic(ctx, req); err != nil {
		return LookupResult{}, err
	} else if ok {
		return result, nil
	}
	return LookupResult{Hit: false}, nil
}

func (s *KVLiteStore) lookupByCandidate(ctx context.Context, req LookupRequest) (LookupResult, bool, error) {
	queryQuestion := firstNonEmpty(req.Question, req.NormalizedQuestion)
	if strings.TrimSpace(queryQuestion) == "" {
		return LookupResult{Hit: false}, false, nil
	}
	scope := effectiveLookupScope(req)
	topK := req.CandidateTopK
	if topK <= 0 {
		topK = DefaultCandidateTopK
	}
	minScore := req.CandidateMinScore
	if minScore <= 0 {
		minScore = DefaultCandidateMinScore
	}
	candidateIDs, candidateCount, err := s.recallCandidateIDs(ctx, scope, queryQuestion, topK)
	if err != nil {
		return LookupResult{}, false, err
	}
	if len(candidateIDs) == 0 {
		return LookupResult{Hit: false}, false, nil
	}
	bestScore := 0.0
	var bestRecord *llm.QARecord
	for _, questionID := range candidateIDs {
		record, loadErr := s.loadRecord(ctx, questionID)
		if loadErr != nil {
			return LookupResult{}, false, loadErr
		}
		if record == nil || strings.TrimSpace(record.Answer) == "" {
			continue
		}
		if normalizeScope(record.Scope) != scope {
			continue
		}
		score := scoreCandidateAnswer(queryQuestion, record)
		if score.Score > bestScore {
			bestScore = score.Score
			bestRecord = record
		}
	}
	if bestRecord == nil || bestScore < minScore {
		return LookupResult{Hit: false}, false, nil
	}
	result := lookupResultFromRecord(bestRecord)
	result.Hit = true
	result.HitType = HitTypeCandidate
	result.CandidateCount = candidateCount
	result.CandidateTopScore = bestScore
	result.CandidateSource = CandidateRecallSourceBigram
	if result.Metadata == nil {
		result.Metadata = map[string]string{}
	}
	result.Metadata["hit_type"] = HitTypeCandidate
	result.Metadata["candidate_count"] = fmt.Sprintf("%d", candidateCount)
	result.Metadata["candidate_top_score"] = fmt.Sprintf("%.4f", bestScore)
	result.Metadata["candidate_source"] = CandidateRecallSourceBigram
	return result, true, nil
}

func (s *KVLiteStore) recallCandidateIDs(ctx context.Context, scope, queryQuestion string, topK int) ([]string, int, error) {
	signals := candidateSignalsFromQuestion(queryQuestion, DefaultCandidateSignalLimit)
	if len(signals) == 0 {
		return nil, 0, nil
	}
	counts := make(map[string]int, len(signals))
	for _, signal := range signals {
		payload, err := s.backend.RGet(ctx, s.candidateBucketKey(scope, signal))
		if err != nil {
			return nil, 0, err
		}
		ids, err := decodeCandidateBucketIDs(payload)
		if err != nil {
			return nil, 0, err
		}
		for _, questionID := range ids {
			counts[questionID]++
		}
	}
	if len(counts) == 0 {
		return nil, 0, nil
	}
	poolLimit := topK * 4
	if poolLimit < DefaultCandidateRecallPoolSize {
		poolLimit = DefaultCandidateRecallPoolSize
	}
	if poolLimit > len(counts) {
		poolLimit = len(counts)
	}
	return candidateRankIDsByCount(counts, poolLimit), len(counts), nil
}

func (s *KVLiteStore) lookupByAlias(ctx context.Context, scope, aliasHash, aliasQuestion string) (LookupResult, bool, error) {
	if strings.TrimSpace(aliasHash) == "" {
		return LookupResult{Hit: false}, false, nil
	}
	canonicalQuestionID, err := s.backend.RGet(ctx, s.aliasIndexKey(scope, aliasHash))
	if err != nil {
		return LookupResult{}, false, err
	}
	canonicalQuestionID = strings.TrimSpace(canonicalQuestionID)
	if canonicalQuestionID == "" {
		return LookupResult{Hit: false}, false, nil
	}
	record, err := s.loadRecord(ctx, canonicalQuestionID)
	if err != nil {
		return LookupResult{}, false, err
	}
	if record == nil || strings.TrimSpace(record.Answer) == "" {
		return LookupResult{Hit: false}, false, nil
	}
	result := lookupResultFromRecord(record)
	result.Hit = true
	result.HitType = HitTypeAlias
	result.AliasQuestion = strings.TrimSpace(aliasQuestion)
	result.AliasQuestionHash = aliasHash
	result.CanonicalQuestionID = firstNonEmpty(result.CanonicalQuestionID, result.QuestionID, canonicalQuestionID)
	result.QuestionID = result.CanonicalQuestionID
	if result.Metadata == nil {
		result.Metadata = map[string]string{}
	}
	result.Metadata["hit_type"] = HitTypeAlias
	result.Metadata["alias_question"] = result.AliasQuestion
	result.Metadata["alias_question_hash"] = aliasHash
	result.Metadata["canonical_question_id"] = result.CanonicalQuestionID
	return result, true, nil
}

func (s *KVLiteStore) lookupByIndex(ctx context.Context, indexKey, hitType string) (LookupResult, bool, error) {
	if strings.TrimSpace(indexKey) == "" {
		return LookupResult{Hit: false}, false, nil
	}
	questionID, err := s.backend.RGet(ctx, indexKey)
	if err != nil {
		return LookupResult{}, false, err
	}
	questionID = strings.TrimSpace(questionID)
	if questionID == "" {
		return LookupResult{Hit: false}, false, nil
	}
	record, err := s.loadRecord(ctx, questionID)
	if err != nil {
		return LookupResult{}, false, err
	}
	if record == nil || strings.TrimSpace(record.Answer) == "" {
		return LookupResult{Hit: false}, false, nil
	}
	result := lookupResultFromRecord(record)
	result.Hit = true
	result.HitType = hitType
	if result.QuestionID == "" {
		result.QuestionID = questionID
	}
	if result.Metadata == nil {
		result.Metadata = map[string]string{}
	}
	result.Metadata["hit_type"] = hitType
	result.Metadata["question_id"] = result.QuestionID
	return result, true, nil
}

func (s *KVLiteStore) loadRecord(ctx context.Context, questionID string) (*llm.QARecord, error) {
	if s == nil || s.backend == nil {
		return nil, nil
	}
	mainKey := s.recordKey(questionID)
	payload, err := s.backend.RGet(ctx, mainKey)
	if err != nil {
		return nil, err
	}
	payload = strings.TrimSpace(payload)
	if payload == "" {
		return nil, nil
	}
	record := &llm.QARecord{}
	if err := json.Unmarshal([]byte(payload), record); err != nil {
		return nil, err
	}
	if record.QuestionID == "" {
		record.QuestionID = questionID
	}
	if record.RecordID == "" {
		record.RecordID = record.QuestionID
	}
	if record.CanonicalQuestionID == "" {
		record.CanonicalQuestionID = record.QuestionID
	}
	if record.Question == "" {
		record.Question = record.RawQuestion
	}
	if record.RawQuestion == "" {
		record.RawQuestion = record.Question
	}
	if record.CanonicalQuestion == "" {
		record.CanonicalQuestion = firstNonEmpty(record.RawQuestion, record.Question)
	}
	if record.NormalizedQuestion == "" {
		record.NormalizedQuestion = NormalizeQuestion(firstNonEmpty(record.CanonicalQuestion, record.RawQuestion, record.Question))
	}
	if record.QuestionHash == "" {
		record.QuestionHash = QuestionHash(firstNonEmpty(record.RawQuestion, record.Question))
	}
	if record.RawQuestionHash == "" {
		record.RawQuestionHash = ExactQuestionHash(firstNonEmpty(record.RawQuestion, record.Question))
	}
	if record.Scope == "" {
		record.Scope = BuildScope(record.Provider, record.Model, record.PromptVersion)
	}
	if record.EmbeddingStatus == EmbeddingStatusReady {
		record.SemanticReady = true
	}
	if record.EmbeddingStatus == "" && record.SemanticReady {
		record.EmbeddingStatus = EmbeddingStatusReady
	}
	if record.SemanticReady && record.EmbeddingStatus == "" {
		record.EmbeddingStatus = EmbeddingStatusReady
	}
	if record.EmbeddingRef == "" && record.SemanticReady {
		record.EmbeddingRef = semanticEmbeddingKey(record.QuestionID)
	}
	if record.EmbeddingModel == "" && record.SemanticReady {
		record.EmbeddingModel = s.semanticModel
	}
	if record.Metadata == nil {
		record.Metadata = map[string]string{}
	}
	record.Metadata["canonical_question_id"] = record.CanonicalQuestionID
	record.Metadata["canonical_question"] = record.CanonicalQuestion
	record.Metadata["canonical_question_hash"] = firstNonEmpty(record.Metadata["canonical_question_hash"], record.RawQuestionHash)
	if record.EmbeddingStatus != "" {
		record.Metadata["embedding_status"] = record.EmbeddingStatus
	}
	if record.EmbeddingModel != "" {
		record.Metadata["embedding_model"] = record.EmbeddingModel
	}
	if record.EmbeddingDim > 0 {
		record.Metadata["embedding_dim"] = fmt.Sprintf("%d", record.EmbeddingDim)
	}
	if record.EmbeddingUpdatedAt > 0 {
		record.Metadata["embedding_updated_at"] = fmt.Sprintf("%d", record.EmbeddingUpdatedAt)
	}
	if record.EmbeddingRef != "" {
		record.Metadata["embedding_ref"] = record.EmbeddingRef
	}
	if record.SemanticReady {
		record.Metadata["semantic_ready"] = "true"
	}
	return record, nil
}

func (s *KVLiteStore) writeCandidateIndexes(ctx context.Context, record *llm.QARecord, ttl time.Duration) error {
	if s == nil || s.backend == nil || record == nil {
		return nil
	}
	baseQuestion := firstNonEmpty(record.NormalizedQuestion, record.CanonicalQuestion, record.RawQuestion, record.Question)
	signals := candidateSignalsFromQuestion(baseQuestion, DefaultCandidateSignalLimit)
	if len(signals) == 0 {
		return nil
	}
	writeTTL := ttlForWrite(record, ttl, s.defaultTTL)
	var firstErr error
	for _, signal := range signals {
		bucketKey := s.candidateBucketKey(record.Scope, signal)
		payload, err := s.backend.RGet(ctx, bucketKey)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		existingIDs, err := decodeCandidateBucketIDs(payload)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		updatedIDs := updateCandidateBucketIDs(existingIDs, record.QuestionID, DefaultCandidateBucketSize)
		encoded, err := encodeCandidateBucketIDs(updatedIDs)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if err := s.backend.RSet(ctx, bucketKey, encoded, writeTTL); err != nil {
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

func (s *KVLiteStore) rollback(ctx context.Context, keys []string) {
	if s == nil || s.backend == nil {
		return
	}
	for i := len(keys) - 1; i >= 0; i-- {
		key := strings.TrimSpace(keys[i])
		if key == "" {
			continue
		}
		_ = s.backend.Del(ctx, key)
	}
}

func (s *KVLiteStore) recordKey(questionID string) string {
	return s.prefixed(MainRecordKey(questionID))
}

func (s *KVLiteStore) exactIndexKey(scope, exactHash string) string {
	return s.prefixed(ExactIndexKey(scope, exactHash))
}

func (s *KVLiteStore) normalizedIndexKey(scope, normalizedHash string) string {
	return s.prefixed(NormalizedIndexKey(scope, normalizedHash))
}

func (s *KVLiteStore) aliasIndexKey(scope, aliasHash string) string {
	return s.prefixed(AliasIndexKey(scope, aliasHash))
}

func (s *KVLiteStore) candidateBucketKey(scope, signal string) string {
	return s.prefixed(candidateBucketKey(scope, signal))
}

func (s *KVLiteStore) prefixed(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return key
	}
	if s == nil || strings.TrimSpace(s.namespace) == "" {
		return key
	}
	return fmt.Sprintf("%s:%s", strings.TrimSpace(s.namespace), key)
}

func effectiveLookupScope(req LookupRequest) string {
	if trimmed := strings.TrimSpace(req.Scope); trimmed != "" {
		return trimmed
	}
	return BuildScope(req.Provider, req.Model, req.PromptVersion)
}

func normalizeQARecordForStore(record *llm.QARecord, ttl, defaultTTL time.Duration) (*llm.QARecord, error) {
	if record == nil {
		return nil, errors.New("qa record is nil")
	}
	stored := cloneQARecord(record)
	question := firstNonEmpty(stored.RawQuestion, stored.Question)
	if question == "" {
		return nil, errors.New("qa record question is empty")
	}
	stored.Question = question
	stored.RawQuestion = question
	if stored.QuestionHash == "" {
		stored.QuestionHash = QuestionHash(question)
	}
	if stored.RawQuestionHash == "" {
		stored.RawQuestionHash = ExactQuestionHash(question)
	}
	if stored.Scope == "" {
		stored.Scope = BuildScope(stored.Provider, stored.Model, stored.PromptVersion)
	}
	if stored.EmbeddingStatus == EmbeddingStatusReady {
		stored.SemanticReady = true
	}
	if stored.CanonicalQuestionID == "" {
		stored.CanonicalQuestionID = stored.QuestionID
	}
	if stored.CanonicalQuestion == "" {
		stored.CanonicalQuestion = stored.Question
	}
	if stored.PromptVersion == "" {
		stored.PromptVersion = stored.Metadata["prompt_version"]
	}
	if stored.QuestionID == "" {
		stored.QuestionID = BuildQuestionID(stored.Scope, stored.RawQuestionHash)
	}
	if stored.QuestionID == "" {
		return nil, errors.New("qa record question_id is empty")
	}
	if stored.RecordID == "" {
		stored.RecordID = stored.QuestionID
	}
	now := time.Now().Unix()
	if stored.CreatedAt == 0 {
		stored.CreatedAt = now
	}
	stored.UpdatedAt = now
	if stored.HitCount <= 0 {
		stored.HitCount = 1
	}
	if stored.LastHitAt <= 0 {
		stored.LastHitAt = now
	}
	if stored.SourceType == "" {
		stored.SourceType = llm.SourceTypeAPI
	}
	if stored.Metadata == nil {
		stored.Metadata = map[string]string{}
	}
	stored.Metadata["question_id"] = stored.QuestionID
	stored.Metadata["record_id"] = stored.RecordID
	stored.Metadata["question"] = stored.Question
	stored.Metadata["raw_question"] = stored.RawQuestion
	stored.Metadata["canonical_question"] = stored.CanonicalQuestion
	stored.Metadata["canonical_question_id"] = stored.CanonicalQuestionID
	stored.Metadata["normalized_question"] = stored.NormalizedQuestion
	stored.Metadata["normalized_question_hash"] = stored.QuestionHash
	stored.Metadata["raw_question_hash"] = stored.RawQuestionHash
	stored.Metadata["canonical_question_hash"] = stored.RawQuestionHash
	stored.Metadata["scope"] = stored.Scope
	stored.Metadata["prompt_version"] = stored.PromptVersion
	stored.Metadata["provider"] = stored.Provider
	stored.Metadata["model"] = stored.Model
	stored.Metadata["source_type"] = stored.SourceType
	stored.Metadata["answer_source"] = firstNonEmpty(stored.Metadata["answer_source"], llm.FinalAnswerSourceLLM)
	stored.Metadata["hit_count"] = fmt.Sprintf("%d", stored.HitCount)
	stored.Metadata["last_hit_at"] = fmt.Sprintf("%d", stored.LastHitAt)
	if stored.ExpiresAt <= 0 {
		ttlToUse := ttlForWrite(stored, ttl, defaultTTL)
		if ttlToUse > 0 {
			stored.ExpiresAt = now + int64(ttlToUse/time.Second)
		}
	}
	return stored, nil
}

func ttlForAliasWrite(record *llm.QARecord, ttl, defaultTTL time.Duration) time.Duration {
	if record != nil && record.ExpiresAt > 0 {
		remaining := time.Until(time.Unix(record.ExpiresAt, 0))
		if remaining > 0 {
			if ttl > 0 && ttl < remaining {
				return ttl
			}
			return remaining
		}
	}
	if ttl > 0 {
		return ttl
	}
	if defaultTTL > 0 {
		return defaultTTL
	}
	return 0
}

func ttlForWrite(record *llm.QARecord, ttl, defaultTTL time.Duration) time.Duration {
	if ttl > 0 {
		return ttl
	}
	if record != nil && record.ExpiresAt > 0 {
		remaining := time.Until(time.Unix(record.ExpiresAt, 0))
		if remaining > 0 {
			return remaining
		}
	}
	if defaultTTL > 0 {
		return defaultTTL
	}
	return 0
}

func defaultQANamespace(cnf *config.Config) string {
	if cnf == nil {
		return "ai:chat:qa"
	}
	base := normalizeQANamespace(cnf.Context.Namespace)
	if base == "" {
		base = "ai:chat"
	}
	return base + ":qa"
}

func normalizeQANamespace(namespace string) string {
	if trimmed := strings.TrimSpace(namespace); trimmed != "" {
		return trimmed
	}
	return "ai:chat:qa"
}

func defaultQATTL(cnf *config.Config) time.Duration {
	if cnf == nil || cnf.Chat.ContextTTL <= 0 {
		return 24 * time.Hour
	}
	return time.Duration(cnf.Chat.ContextTTL) * time.Second
}

func cloneQARecord(record *llm.QARecord) *llm.QARecord {
	if record == nil {
		return nil
	}
	cloned := *record
	if record.Metadata != nil {
		cloned.Metadata = make(map[string]string, len(record.Metadata))
		for key, value := range record.Metadata {
			cloned.Metadata[key] = value
		}
	}
	if record.RetrievedHits != nil {
		cloned.RetrievedHits = append([]llm.RetrievalHit(nil), record.RetrievedHits...)
	}
	return &cloned
}
