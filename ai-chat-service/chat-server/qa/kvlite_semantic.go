package qa

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"ai-chat-service/chat-server/llm"
	"ai-chat-service/chat-server/provider"
	"ai-chat-service/pkg/config"
)

type semanticProviderSelection struct {
	provider llm.EmbeddingProvider
	model    string
}

func semanticConfigFromConfig(cnf *config.Config) SemanticConfig {
	cfg := SemanticConfig{
		Enabled:       false,
		TopK:          DefaultSemanticTopK,
		Threshold:     DefaultSemanticMinScore,
		CorpusLimit:   DefaultSemanticCorpusLimit,
		EmbeddingDim:  DefaultSemanticEmbeddingDim,
		AsyncBackfill: true,
	}
	if cnf == nil {
		return cfg
	}
	semantic := cnf.Retrieval.Semantic
	cfg.Enabled = semantic.Enabled
	if semantic.TopK > 0 {
		cfg.TopK = semantic.TopK
	}
	if semantic.Threshold > 0 {
		cfg.Threshold = semantic.Threshold
	}
	if semantic.CorpusLimit > 0 {
		cfg.CorpusLimit = semantic.CorpusLimit
	}
	if strings.TrimSpace(semantic.EmbeddingModel) != "" {
		cfg.EmbeddingModel = strings.TrimSpace(semantic.EmbeddingModel)
	}
	if semantic.EmbeddingDim > 0 {
		cfg.EmbeddingDim = semantic.EmbeddingDim
	}
	cfg.AsyncBackfill = semantic.AsyncBackfill
	return cfg
}

func selectSemanticEmbeddingProvider(cnf *config.Config, cfg SemanticConfig) semanticProviderSelection {
	selection := semanticProviderSelection{
		model: firstNonEmpty(cfg.EmbeddingModel, "placeholder-hash-embedding"),
	}
	if cnf == nil {
		selection.provider = NewHashEmbeddingProvider(selection.model, cfg.EmbeddingDim)
		return selection
	}
	registry := provider.NewRegistry(cnf.ProviderProfiles())
	for _, name := range registry.ProfileNames() {
		candidateProvider, profile, ok := registry.ProfileByName(name)
		if !ok || candidateProvider == nil || !profile.SupportsEmbeddings {
			continue
		}
		embeddingProvider, ok := candidateProvider.(llm.EmbeddingProvider)
		if !ok {
			continue
		}
		selection.provider = embeddingProvider
		selection.model = firstNonEmpty(cfg.EmbeddingModel, profile.EmbeddingModel, profile.DefaultEmbeddingModel, profile.Name)
		if strings.TrimSpace(selection.model) == "" {
			selection.model = profile.Name
		}
		return selection
	}
	selection.provider = NewHashEmbeddingProvider(selection.model, cfg.EmbeddingDim)
	return selection
}

func (s *KVLiteStore) applySemanticConfig(cfg SemanticConfig) {
	if s == nil {
		return
	}
	s.semanticEnabled = cfg.Enabled
	if cfg.TopK > 0 {
		s.semanticTopK = cfg.TopK
	} else if s.semanticTopK <= 0 {
		s.semanticTopK = DefaultSemanticTopK
	}
	if cfg.Threshold > 0 {
		s.semanticThreshold = cfg.Threshold
	} else if s.semanticThreshold <= 0 {
		s.semanticThreshold = DefaultSemanticMinScore
	}
	if cfg.CorpusLimit > 0 {
		s.semanticCorpusLimit = cfg.CorpusLimit
	} else if s.semanticCorpusLimit <= 0 {
		s.semanticCorpusLimit = DefaultSemanticCorpusLimit
	}
	if cfg.EmbeddingDim > 0 {
		s.semanticEmbeddingDim = cfg.EmbeddingDim
	} else if s.semanticEmbeddingDim <= 0 {
		s.semanticEmbeddingDim = DefaultSemanticEmbeddingDim
	}
	if strings.TrimSpace(cfg.EmbeddingModel) != "" {
		s.semanticModel = strings.TrimSpace(cfg.EmbeddingModel)
	} else if strings.TrimSpace(s.semanticModel) == "" {
		s.semanticModel = "placeholder-hash-embedding"
	}
	if s.semanticEnabled && s.semanticProvider == nil {
		s.semanticProvider = NewHashEmbeddingProvider(s.semanticModel, s.semanticEmbeddingDim)
	}
}

func (s *KVLiteStore) initSemanticProvider(cnf *config.Config) {
	if s == nil {
		return
	}
	selection := selectSemanticEmbeddingProvider(cnf, SemanticConfig{
		Enabled:        s.semanticEnabled,
		TopK:           s.semanticTopK,
		Threshold:      s.semanticThreshold,
		CorpusLimit:    s.semanticCorpusLimit,
		EmbeddingDim:   s.semanticEmbeddingDim,
		EmbeddingModel: s.semanticModel,
	})
	if selection.provider != nil {
		s.semanticProvider = selection.provider
	}
	if strings.TrimSpace(selection.model) != "" {
		s.semanticModel = selection.model
	}
}

func (s *KVLiteStore) BackfillSemanticEmbedding(ctx context.Context, record *llm.QARecord) error {
	if s == nil || s.backend == nil || !s.semanticEnabled || record == nil {
		return nil
	}
	questionID := firstNonEmpty(record.QuestionID, record.CanonicalQuestionID, record.RecordID)
	if strings.TrimSpace(questionID) == "" {
		return fmt.Errorf("semantic backfill question id is empty")
	}
	question := firstNonEmpty(record.CanonicalQuestion, record.RawQuestion, record.Question)
	if strings.TrimSpace(question) == "" {
		return nil
	}
	answer := strings.TrimSpace(record.Answer)
	if answer == "" {
		return nil
	}
	provider := s.semanticProvider
	if provider == nil {
		return nil
	}
	vectors, err := provider.Embed(ctx, []string{question})
	if err != nil {
		return err
	}
	if len(vectors) == 0 || len(vectors[0]) == 0 {
		return nil
	}
	vector := vectors[0]
	now := time.Now().Unix()
	ref := semanticEmbeddingKey(questionID)
	payload := semanticEmbeddingPayload{
		Model:     firstNonEmpty(s.semanticModel, record.EmbeddingModel, provider.Name()),
		Provider:  provider.Name(),
		Dim:       len(vector),
		Vector:    append([]float32(nil), vector...),
		UpdatedAt: now,
	}
	encoded, err := encodeSemanticEmbeddingPayload(payload)
	if err != nil {
		return err
	}
	writeTTL := ttlForWrite(record, 0, s.defaultTTL)
	stored := cloneQARecord(record)
	stored.EmbeddingModel = payload.Model
	stored.EmbeddingStatus = EmbeddingStatusReady
	stored.EmbeddingDim = payload.Dim
	stored.EmbeddingUpdatedAt = now
	stored.EmbeddingRef = ref
	stored.SemanticReady = true
	if stored.Metadata == nil {
		stored.Metadata = map[string]string{}
	}
	stored.Metadata["embedding_model"] = payload.Model
	stored.Metadata["embedding_status"] = stored.EmbeddingStatus
	stored.Metadata["embedding_dim"] = fmt.Sprintf("%d", payload.Dim)
	stored.Metadata["embedding_updated_at"] = fmt.Sprintf("%d", now)
	stored.Metadata["embedding_ref"] = ref
	stored.Metadata["semantic_ready"] = "true"

	mainPayload, err := json.Marshal(stored)
	if err != nil {
		return err
	}
	if err := s.backend.RSet(ctx, s.recordKey(questionID), string(mainPayload), writeTTL); err != nil {
		return err
	}
	if err := s.backend.RSet(ctx, s.prefixed(ref), encoded, writeTTL); err != nil {
		return err
	}
	if err := s.upsertSemanticScopeIndex(ctx, stored.Scope, questionID, writeTTL); err != nil {
		return err
	}
	s.invalidateSemanticANNIndex(stored.Scope)
	return nil
}

func (s *KVLiteStore) lookupBySemantic(ctx context.Context, req LookupRequest) (LookupResult, bool, error) {
	if s == nil || s.backend == nil || !s.semanticEnabled {
		return LookupResult{Hit: false}, false, nil
	}
	queryQuestion := firstNonEmpty(req.Question, req.NormalizedQuestion)
	if strings.TrimSpace(queryQuestion) == "" {
		return LookupResult{Hit: false}, false, nil
	}
	provider := s.semanticProvider
	if provider == nil {
		return LookupResult{Hit: false}, false, nil
	}
	queryVectors, err := provider.Embed(ctx, []string{queryQuestion})
	if err != nil {
		return LookupResult{}, false, err
	}
	if len(queryVectors) == 0 || len(queryVectors[0]) == 0 {
		return LookupResult{Hit: false}, false, nil
	}
	return s.lookupBySemanticWithVector(ctx, req, queryVectors[0])
}

func (s *KVLiteStore) lookupBySemanticWithVector(ctx context.Context, req LookupRequest, queryVector []float32) (LookupResult, bool, error) {
	mode := normalizeSemanticMode(req.SemanticMode)
	if mode == SemanticRecallModeANN {
		result, hit, err := s.lookupBySemanticANNWithVector(ctx, req, queryVector)
		return result, hit, err
	}
	start := time.Now()
	result, hit, err := s.lookupBySemanticBruteForceWithVector(ctx, req, queryVector)
	if err != nil {
		return result, hit, err
	}
	applySemanticLookupOutcome(&result, SemanticRecallModeBruteForce, SemanticRecallModeBruteForce, false, "", SemanticRecallSourceBruteForce, 0, time.Since(start), time.Since(start))
	result.SemanticShadowTrace = s.lookupBySemanticANNShadowWithVector(ctx, req, queryVector, result)
	return result, hit, nil
}

func applySemanticLookupOutcome(result *LookupResult, requestedMode, usedMode string, fallbackUsed bool, fallbackReason string, semanticSource string, annLatency, bruteForceLatency, totalLatency time.Duration) {
	if result == nil {
		return
	}
	requestedMode = normalizeSemanticMode(requestedMode)
	usedMode = normalizeSemanticMode(usedMode)
	if requestedMode == "" {
		requestedMode = usedMode
	}
	if usedMode == "" {
		usedMode = requestedMode
	}
	if totalLatency <= 0 {
		totalLatency = annLatency + bruteForceLatency
	}
	if result.Metadata == nil {
		result.Metadata = map[string]string{}
	}
	result.SemanticModeRequested = requestedMode
	result.SemanticModeUsed = usedMode
	result.SemanticFallbackUsed = fallbackUsed
	result.SemanticFallbackReason = strings.TrimSpace(fallbackReason)
	result.SemanticLatencyMS = float64(totalLatency) / float64(time.Millisecond)
	result.SemanticANNLatencyMS = float64(annLatency) / float64(time.Millisecond)
	result.SemanticBruteForceLatencyMS = float64(bruteForceLatency) / float64(time.Millisecond)
	if semanticSource != "" {
		result.SemanticSource = semanticSource
	}
	result.Metadata["semantic_mode_requested"] = requestedMode
	result.Metadata["semantic_mode_used"] = usedMode
	result.Metadata["semantic_fallback_used"] = fmt.Sprintf("%t", fallbackUsed)
	if strings.TrimSpace(fallbackReason) != "" {
		result.Metadata["semantic_fallback_reason"] = fallbackReason
	} else {
		delete(result.Metadata, "semantic_fallback_reason")
	}
	result.Metadata["semantic_latency_ms"] = fmt.Sprintf("%.3f", result.SemanticLatencyMS)
	result.Metadata["semantic_ann_latency_ms"] = fmt.Sprintf("%.3f", result.SemanticANNLatencyMS)
	result.Metadata["semantic_bruteforce_latency_ms"] = fmt.Sprintf("%.3f", result.SemanticBruteForceLatencyMS)
	result.Metadata["semantic_mode"] = usedMode
	if semanticSource != "" {
		result.Metadata["semantic_source"] = semanticSource
	}
}

func (s *KVLiteStore) lookupBySemanticANNWithVector(ctx context.Context, req LookupRequest, queryVector []float32) (LookupResult, bool, error) {
	requestedMode := normalizeSemanticMode(req.SemanticMode)
	if requestedMode != SemanticRecallModeANN {
		requestedMode = SemanticRecallModeANN
	}
	annStart := time.Now()
	annResult, annHit, shouldFallback, fallbackReason, annErr := s.lookupBySemanticANNDirectWithVector(ctx, req, queryVector)
	annLatency := time.Since(annStart)
	if !shouldFallback && annErr == nil {
		applySemanticLookupOutcome(&annResult, requestedMode, SemanticRecallModeANN, false, "", SemanticRecallSourceANNBucketedLSH, annLatency, 0, annLatency)
		return annResult, annHit, nil
	}
	bruteForceStart := time.Now()
	bruteForceResult, bruteForceHit, bruteForceErr := s.lookupBySemanticBruteForceWithVector(ctx, req, queryVector)
	bruteForceLatency := time.Since(bruteForceStart)
	totalLatency := annLatency + bruteForceLatency
	if bruteForceErr != nil {
		applySemanticLookupOutcome(&bruteForceResult, requestedMode, SemanticRecallModeBruteForce, true, fallbackReason, semanticANNFallbackSource, annLatency, bruteForceLatency, totalLatency)
		return bruteForceResult, bruteForceHit, bruteForceErr
	}
	applySemanticLookupOutcome(&bruteForceResult, requestedMode, SemanticRecallModeBruteForce, true, fallbackReason, semanticANNFallbackSource, annLatency, bruteForceLatency, totalLatency)
	if annErr != nil {
		return bruteForceResult, bruteForceHit, nil
	}
	return bruteForceResult, bruteForceHit, nil
}

func (s *KVLiteStore) lookupBySemanticANNDirectWithVector(ctx context.Context, req LookupRequest, queryVector []float32) (LookupResult, bool, bool, string, error) {
	if s == nil || s.backend == nil || !s.semanticEnabled {
		return LookupResult{Hit: false}, false, false, "", nil
	}
	if len(queryVector) == 0 {
		return LookupResult{Hit: false}, false, true, "ann_query_vector_empty", nil
	}
	scope := effectiveLookupScope(req)
	topK := req.SemanticTopK
	if topK <= 0 {
		topK = s.semanticTopK
	}
	if topK <= 0 {
		topK = DefaultSemanticTopK
	}
	threshold := req.SemanticThreshold
	if threshold <= 0 {
		threshold = s.semanticThreshold
	}
	if threshold <= 0 {
		threshold = DefaultSemanticMinScore
	}
	corpusLimit := req.SemanticCorpusLimit
	if corpusLimit <= 0 {
		corpusLimit = s.semanticCorpusLimit
	}
	if corpusLimit <= 0 {
		corpusLimit = DefaultSemanticCorpusLimit
	}
	index, err := s.ensureSemanticANNIndex(ctx, scope, corpusLimit)
	if err != nil {
		return LookupResult{}, false, true, fmt.Sprintf("ann_index_load_error: %v", err), err
	}
	if index == nil || len(index.Entries) == 0 {
		return LookupResult{Hit: false}, false, true, "ann_index_missing", nil
	}
	best, candidateCount, ok, queryErr := index.bestMatch(queryVector)
	if queryErr != nil {
		return LookupResult{}, false, true, fmt.Sprintf("ann_query_error: %v", queryErr), queryErr
	}
	if !ok || best.Record == nil {
		return LookupResult{Hit: false}, false, true, "ann_no_result", nil
	}
	if best.Score < threshold {
		return LookupResult{Hit: false}, false, true, "ann_low_score", nil
	}
	result := lookupResultFromRecord(best.Record)
	result.Hit = true
	result.HitType = HitTypeSemantic
	result.SemanticCandidateCount = candidateCount
	result.SemanticTopK = topK
	result.SemanticTopScore = best.Score
	result.SemanticModel = firstNonEmpty(best.Model, s.semanticModel, best.Record.EmbeddingModel)
	result.SemanticSource = SemanticRecallSourceANNBucketedLSH
	if result.Metadata == nil {
		result.Metadata = map[string]string{}
	}
	result.Metadata["hit_type"] = HitTypeSemantic
	result.Metadata["semantic_mode"] = SemanticRecallModeANN
	result.Metadata["semantic_candidate_count"] = fmt.Sprintf("%d", candidateCount)
	result.Metadata["semantic_top_k"] = fmt.Sprintf("%d", topK)
	result.Metadata["semantic_top_score"] = fmt.Sprintf("%.4f", best.Score)
	result.Metadata["semantic_model"] = result.SemanticModel
	result.Metadata["semantic_source"] = SemanticRecallSourceANNBucketedLSH
	return result, true, false, "", nil
}

func (s *KVLiteStore) lookupBySemanticBruteForceWithVector(ctx context.Context, req LookupRequest, queryVector []float32) (LookupResult, bool, error) {
	if s == nil || s.backend == nil || !s.semanticEnabled {
		return LookupResult{Hit: false}, false, nil
	}
	if len(queryVector) == 0 {
		return LookupResult{Hit: false}, false, nil
	}
	scope := effectiveLookupScope(req)
	topK := req.SemanticTopK
	if topK <= 0 {
		topK = s.semanticTopK
	}
	if topK <= 0 {
		topK = DefaultSemanticTopK
	}
	threshold := req.SemanticThreshold
	if threshold <= 0 {
		threshold = s.semanticThreshold
	}
	if threshold <= 0 {
		threshold = DefaultSemanticMinScore
	}
	corpusLimit := req.SemanticCorpusLimit
	if corpusLimit <= 0 {
		corpusLimit = s.semanticCorpusLimit
	}
	if corpusLimit <= 0 {
		corpusLimit = DefaultSemanticCorpusLimit
	}
	payload, err := s.backend.RGet(ctx, s.semanticScopeIndexKey(scope))
	if err != nil {
		return LookupResult{}, false, err
	}
	candidateIDs, err := decodeCandidateBucketIDs(payload)
	if err != nil {
		return LookupResult{Hit: false}, false, nil
	}
	if len(candidateIDs) == 0 {
		return LookupResult{Hit: false}, false, nil
	}
	if len(candidateIDs) > corpusLimit {
		candidateIDs = candidateIDs[:corpusLimit]
	}
	type semanticHit struct {
		id     string
		record *llm.QARecord
		score  float64
		model  string
	}
	matches := make([]semanticHit, 0, len(candidateIDs))
	for _, questionID := range candidateIDs {
		record, loadErr := s.loadRecord(ctx, questionID)
		if loadErr != nil {
			return LookupResult{}, false, loadErr
		}
		if record == nil || strings.TrimSpace(record.Answer) == "" || !(record.SemanticReady || record.EmbeddingStatus == EmbeddingStatusReady) {
			continue
		}
		vectorPayload, err := s.backend.RGet(ctx, s.prefixed(record.EmbeddingRef))
		if err != nil {
			return LookupResult{}, false, err
		}
		vectorPayload = strings.TrimSpace(vectorPayload)
		if vectorPayload == "" {
			continue
		}
		embeddingPayload, err := decodeSemanticEmbeddingPayload(vectorPayload)
		if err != nil || len(embeddingPayload.Vector) == 0 {
			continue
		}
		score := cosineSimilarity(queryVector, embeddingPayload.Vector)
		matches = append(matches, semanticHit{
			id:     questionID,
			record: record,
			score:  score,
			model:  embeddingPayload.Model,
		})
	}
	if len(matches) == 0 {
		return LookupResult{Hit: false}, false, nil
	}
	for i := 0; i < len(matches)-1; i++ {
		for j := i + 1; j < len(matches); j++ {
			if matches[j].score > matches[i].score || (matches[j].score == matches[i].score && matches[j].id < matches[i].id) {
				matches[i], matches[j] = matches[j], matches[i]
			}
		}
	}
	best := matches[0]
	if best.score < threshold {
		return LookupResult{Hit: false}, false, nil
	}
	result := lookupResultFromRecord(best.record)
	result.Hit = true
	result.HitType = HitTypeSemantic
	result.SemanticCandidateCount = len(candidateIDs)
	result.SemanticTopK = topK
	result.SemanticTopScore = best.score
	result.SemanticModel = firstNonEmpty(best.model, s.semanticModel, best.record.EmbeddingModel)
	result.SemanticSource = SemanticRecallSourceBruteForce
	if result.Metadata == nil {
		result.Metadata = map[string]string{}
	}
	result.Metadata["hit_type"] = HitTypeSemantic
	result.Metadata["semantic_mode"] = SemanticRecallModeBruteForce
	result.Metadata["semantic_candidate_count"] = fmt.Sprintf("%d", len(candidateIDs))
	result.Metadata["semantic_top_k"] = fmt.Sprintf("%d", topK)
	result.Metadata["semantic_top_score"] = fmt.Sprintf("%.4f", best.score)
	result.Metadata["semantic_model"] = result.SemanticModel
	result.Metadata["semantic_source"] = SemanticRecallSourceBruteForce
	return result, true, nil
}

func (s *KVLiteStore) semanticScopeIndexKey(scope string) string {
	return s.prefixed(semanticScopeIndexKey(scope))
}

func (s *KVLiteStore) upsertSemanticScopeIndex(ctx context.Context, scope, questionID string, ttl time.Duration) error {
	if s == nil || s.backend == nil || !s.semanticEnabled {
		return nil
	}
	payload, err := s.backend.RGet(ctx, s.semanticScopeIndexKey(scope))
	if err != nil {
		return err
	}
	existingIDs, err := decodeCandidateBucketIDs(payload)
	if err != nil {
		return err
	}
	updatedIDs := updateCandidateBucketIDs(existingIDs, questionID, s.semanticCorpusLimit)
	encoded, err := encodeCandidateBucketIDs(updatedIDs)
	if err != nil {
		return err
	}
	return s.backend.RSet(ctx, s.semanticScopeIndexKey(scope), encoded, ttl)
}
