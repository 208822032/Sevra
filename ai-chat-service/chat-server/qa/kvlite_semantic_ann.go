package qa

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math/rand"
	"sort"
	"strings"

	"ai-chat-service/chat-server/llm"
)

const (
	semanticANNProjectionBits   = 6
	semanticANNProbeRadius      = 1
	semanticANNFallbackSource   = SemanticRecallSourceANNFallback
	semanticANNDefaultBucketKey = ""
)

type semanticANNIndex struct {
	Scope          string                           `json:"scope"`
	CorpusLimit    int                              `json:"corpus_limit"`
	EmbeddingDim   int                              `json:"embedding_dim"`
	ProjectionBits int                              `json:"projection_bits"`
	ProbeRadius    int                              `json:"probe_radius"`
	Version        string                           `json:"version"`
	Projections    [][]float32                      `json:"projections"`
	Entries        map[string]semanticANNIndexEntry `json:"entries"`
	Buckets        map[string][]string              `json:"buckets"`
}

type semanticANNIndexEntry struct {
	Record         *llm.QARecord `json:"record"`
	Vector         []float32     `json:"vector"`
	EmbeddingModel string        `json:"embedding_model,omitempty"`
}

type semanticANNHit struct {
	QuestionID string
	Record     *llm.QARecord
	Vector     []float32
	Score      float64
	Model      string
}

func normalizeSemanticMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case SemanticRecallModeANN:
		return SemanticRecallModeANN
	default:
		return SemanticRecallModeBruteForce
	}
}

func oppositeSemanticMode(mode string) string {
	switch normalizeSemanticMode(mode) {
	case SemanticRecallModeANN:
		return SemanticRecallModeBruteForce
	default:
		return SemanticRecallModeANN
	}
}

func semanticModeSource(mode string) string {
	switch normalizeSemanticMode(mode) {
	case SemanticRecallModeANN:
		return SemanticRecallSourceANNBucketedLSH
	default:
		return SemanticRecallSourceBruteForce
	}
}

func (s *KVLiteStore) semanticANNCacheKey(scope string, corpusLimit int) string {
	return fmt.Sprintf("%s|%d", normalizeScope(scope), normalizePositiveInt(corpusLimit, DefaultSemanticCorpusLimit))
}

func semanticANNCacheScopePrefix(scope string) string {
	return fmt.Sprintf("%s|", normalizeScope(scope))
}

func (s *KVLiteStore) invalidateSemanticANNIndex(scope string) {
	if s == nil {
		return
	}
	prefix := semanticANNCacheScopePrefix(scope)
	s.semanticANNCacheMu.Lock()
	defer s.semanticANNCacheMu.Unlock()
	if len(s.semanticANNCache) == 0 {
		return
	}
	for key := range s.semanticANNCache {
		if strings.HasPrefix(key, prefix) {
			delete(s.semanticANNCache, key)
		}
	}
}

func (s *KVLiteStore) primeSemanticANNIndex(ctx context.Context, scope string, corpusLimit int) error {
	_, err := s.ensureSemanticANNIndex(ctx, scope, corpusLimit)
	return err
}

func (s *KVLiteStore) ensureSemanticANNIndex(ctx context.Context, scope string, corpusLimit int) (*semanticANNIndex, error) {
	if s == nil || s.backend == nil || !s.semanticEnabled {
		return nil, nil
	}
	cacheKey := s.semanticANNCacheKey(scope, corpusLimit)
	s.semanticANNCacheMu.RLock()
	if cached, ok := s.semanticANNCache[cacheKey]; ok && cached != nil {
		s.semanticANNCacheMu.RUnlock()
		return cached, nil
	}
	s.semanticANNCacheMu.RUnlock()

	index, err := s.buildSemanticANNIndex(ctx, scope, corpusLimit)
	if err != nil || index == nil {
		return index, err
	}

	s.semanticANNCacheMu.Lock()
	if s.semanticANNCache == nil {
		s.semanticANNCache = make(map[string]*semanticANNIndex)
	}
	s.semanticANNCache[cacheKey] = index
	s.semanticANNCacheMu.Unlock()
	return index, nil
}

func (s *KVLiteStore) buildSemanticANNIndex(ctx context.Context, scope string, corpusLimit int) (*semanticANNIndex, error) {
	if s == nil || s.backend == nil || !s.semanticEnabled {
		return nil, nil
	}
	payload, err := s.backend.RGet(ctx, s.semanticScopeIndexKey(scope))
	if err != nil {
		return nil, err
	}
	candidateIDs, err := decodeCandidateBucketIDs(payload)
	if err != nil {
		return nil, err
	}
	if corpusLimit <= 0 {
		corpusLimit = DefaultSemanticCorpusLimit
	}
	if len(candidateIDs) == 0 {
		return nil, nil
	}
	if len(candidateIDs) > corpusLimit {
		candidateIDs = candidateIDs[:corpusLimit]
	}

	var index *semanticANNIndex
	for _, questionID := range candidateIDs {
		record, loadErr := s.loadRecord(ctx, questionID)
		if loadErr != nil {
			return nil, loadErr
		}
		if record == nil || strings.TrimSpace(record.Answer) == "" || !(record.SemanticReady || record.EmbeddingStatus == EmbeddingStatusReady) {
			continue
		}
		embeddingRef := firstNonEmpty(record.EmbeddingRef, semanticEmbeddingKey(record.QuestionID))
		if embeddingRef == "" {
			continue
		}
		vectorPayload, err := s.backend.RGet(ctx, s.prefixed(embeddingRef))
		if err != nil {
			return nil, err
		}
		vectorPayload = strings.TrimSpace(vectorPayload)
		if vectorPayload == "" {
			continue
		}
		embeddingPayload, err := decodeSemanticEmbeddingPayload(vectorPayload)
		if err != nil || len(embeddingPayload.Vector) == 0 {
			continue
		}
		if index == nil {
			index = newSemanticANNIndex(scope, corpusLimit, len(embeddingPayload.Vector))
		}
		if len(embeddingPayload.Vector) != index.EmbeddingDim {
			continue
		}
		index.addRecord(record, embeddingPayload.Vector, embeddingPayload.Model)
	}
	if index == nil || len(index.Entries) == 0 {
		return nil, nil
	}
	return index, nil
}

func newSemanticANNIndex(scope string, corpusLimit, embeddingDim int) *semanticANNIndex {
	projectionBits := semanticANNProjectionBits
	if embeddingDim > 0 && projectionBits > embeddingDim {
		projectionBits = embeddingDim
	}
	if projectionBits <= 0 {
		projectionBits = semanticANNProjectionBits
	}
	index := &semanticANNIndex{
		Scope:          normalizeScope(scope),
		CorpusLimit:    normalizePositiveInt(corpusLimit, DefaultSemanticCorpusLimit),
		EmbeddingDim:   embeddingDim,
		ProjectionBits: projectionBits,
		ProbeRadius:    semanticANNProbeRadius,
		Version:        buildSemanticANNIndexVersion(scope, corpusLimit, embeddingDim, projectionBits, semanticANNProbeRadius),
		Entries:        make(map[string]semanticANNIndexEntry),
		Buckets:        make(map[string][]string),
	}
	index.Projections = buildSemanticANNProjections(index.Scope, index.CorpusLimit, index.EmbeddingDim, index.ProjectionBits)
	return index
}

func buildSemanticANNIndexVersion(scope string, corpusLimit, embeddingDim, projectionBits, probeRadius int) string {
	return fmt.Sprintf("scope=%s|corpus=%d|dim=%d|bits=%d|probe=%d", normalizeScope(scope), normalizePositiveInt(corpusLimit, DefaultSemanticCorpusLimit), embeddingDim, projectionBits, probeRadius)
}

func buildSemanticANNProjections(scope string, corpusLimit, embeddingDim, projectionBits int) [][]float32 {
	if embeddingDim <= 0 || projectionBits <= 0 {
		return nil
	}
	seedBytes := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%d|%d", normalizeScope(scope), corpusLimit, embeddingDim, projectionBits)))
	seed := int64(binary.LittleEndian.Uint64(seedBytes[:8]))
	rng := rand.New(rand.NewSource(seed))
	projections := make([][]float32, projectionBits)
	for bit := 0; bit < projectionBits; bit++ {
		projection := make([]float32, embeddingDim)
		for index := 0; index < embeddingDim; index++ {
			projection[index] = float32(rng.NormFloat64())
		}
		projections[bit] = projection
	}
	return projections
}

func (idx *semanticANNIndex) addRecord(record *llm.QARecord, vector []float32, embeddingModel string) {
	if idx == nil || record == nil || len(vector) == 0 {
		return
	}
	questionID := firstNonEmpty(record.QuestionID, record.CanonicalQuestionID, record.RecordID)
	if questionID == "" {
		return
	}
	if idx.EmbeddingDim <= 0 {
		idx.EmbeddingDim = len(vector)
	}
	if len(vector) != idx.EmbeddingDim {
		return
	}
	clonedRecord := cloneQARecord(record)
	clonedVector := append([]float32(nil), vector...)
	entry := semanticANNIndexEntry{
		Record:         clonedRecord,
		Vector:         clonedVector,
		EmbeddingModel: firstNonEmpty(embeddingModel, clonedRecord.EmbeddingModel),
	}
	idx.Entries[questionID] = entry
	bucketKey := idx.bucketKey(clonedVector)
	idx.Buckets[bucketKey] = append(idx.Buckets[bucketKey], questionID)
}

func (idx *semanticANNIndex) bucketKey(vector []float32) string {
	if idx == nil || len(vector) == 0 || len(idx.Projections) == 0 {
		return semanticANNDefaultBucketKey
	}
	var builder strings.Builder
	builder.Grow(len(idx.Projections))
	for _, projection := range idx.Projections {
		if projectionDot(vector, projection) >= 0 {
			builder.WriteByte('1')
		} else {
			builder.WriteByte('0')
		}
	}
	return builder.String()
}

func projectionDot(vector, projection []float32) float64 {
	if len(vector) == 0 || len(projection) == 0 {
		return 0
	}
	limit := len(vector)
	if len(projection) < limit {
		limit = len(projection)
	}
	var dot float64
	for index := 0; index < limit; index++ {
		dot += float64(vector[index] * projection[index])
	}
	return dot
}

func (idx *semanticANNIndex) probeBucketKeys(vector []float32) []string {
	baseKey := idx.bucketKey(vector)
	if baseKey == "" {
		return nil
	}
	keys := make([]string, 0, 1+len(baseKey))
	keys = append(keys, baseKey)
	if idx == nil || idx.ProbeRadius <= 0 {
		return keys
	}
	seen := map[string]struct{}{baseKey: {}}
	for bitIndex := 0; bitIndex < len(baseKey); bitIndex++ {
		variant := flipSemanticANNBit(baseKey, bitIndex)
		if _, ok := seen[variant]; ok {
			continue
		}
		seen[variant] = struct{}{}
		keys = append(keys, variant)
	}
	return keys
}

func flipSemanticANNBit(value string, index int) string {
	if index < 0 || index >= len(value) {
		return value
	}
	bytes := []byte(value)
	if bytes[index] == '1' {
		bytes[index] = '0'
	} else {
		bytes[index] = '1'
	}
	return string(bytes)
}

func (idx *semanticANNIndex) bestMatch(vector []float32) (semanticANNHit, int, bool, error) {
	hits, candidateCount, err := idx.queryTopMatches(vector, 1)
	if err != nil {
		return semanticANNHit{}, candidateCount, false, err
	}
	if len(hits) == 0 {
		return semanticANNHit{}, candidateCount, false, nil
	}
	return hits[0], candidateCount, true, nil
}

func (idx *semanticANNIndex) queryTopMatches(vector []float32, limit int) ([]semanticANNHit, int, error) {
	if idx == nil {
		return nil, 0, fmt.Errorf("ann index is missing")
	}
	if len(vector) == 0 {
		return nil, 0, fmt.Errorf("ann query vector is empty")
	}
	if idx.EmbeddingDim > 0 && len(vector) != idx.EmbeddingDim {
		return nil, 0, fmt.Errorf("ann query vector dimension mismatch: got %d want %d", len(vector), idx.EmbeddingDim)
	}
	if len(idx.Projections) == 0 {
		return nil, 0, fmt.Errorf("ann index projections are missing")
	}
	bucketKeys := idx.probeBucketKeys(vector)
	if len(bucketKeys) == 0 {
		return nil, 0, nil
	}
	candidateIDs := make([]string, 0, len(idx.Entries))
	seen := make(map[string]struct{}, len(idx.Entries))
	for _, bucketKey := range bucketKeys {
		for _, questionID := range idx.Buckets[bucketKey] {
			if _, ok := seen[questionID]; ok {
				continue
			}
			seen[questionID] = struct{}{}
			candidateIDs = append(candidateIDs, questionID)
		}
	}
	if len(candidateIDs) == 0 {
		return nil, 0, nil
	}
	hits := make([]semanticANNHit, 0, len(candidateIDs))
	for _, questionID := range candidateIDs {
		entry, ok := idx.Entries[questionID]
		if !ok || entry.Record == nil || len(entry.Vector) == 0 {
			return nil, 0, fmt.Errorf("ann index contains invalid candidate %q", questionID)
		}
		hits = append(hits, semanticANNHit{
			QuestionID: questionID,
			Record:     entry.Record,
			Vector:     entry.Vector,
			Score:      cosineSimilarity(vector, entry.Vector),
			Model:      entry.EmbeddingModel,
		})
	}
	if len(hits) == 0 {
		return nil, 0, nil
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score == hits[j].Score {
			return hits[i].QuestionID < hits[j].QuestionID
		}
		return hits[i].Score > hits[j].Score
	})
	if limit <= 0 || limit > len(hits) {
		limit = len(hits)
	}
	cloned := make([]semanticANNHit, limit)
	copy(cloned, hits[:limit])
	return cloned, len(candidateIDs), nil
}

func (idx *semanticANNIndex) topMatches(vector []float32, limit int) ([]semanticANNHit, int, bool) {
	hits, candidateCount, err := idx.queryTopMatches(vector, limit)
	if err != nil || len(hits) == 0 {
		return nil, 0, false
	}
	return hits, candidateCount, true
}

func (idx *semanticANNIndex) clone() *semanticANNIndex {
	if idx == nil {
		return nil
	}
	cloned := *idx
	if idx.Projections != nil {
		cloned.Projections = make([][]float32, len(idx.Projections))
		for index, projection := range idx.Projections {
			cloned.Projections[index] = append([]float32(nil), projection...)
		}
	}
	if idx.Entries != nil {
		cloned.Entries = make(map[string]semanticANNIndexEntry, len(idx.Entries))
		for key, entry := range idx.Entries {
			clonedEntry := entry
			if entry.Record != nil {
				clonedEntry.Record = cloneQARecord(entry.Record)
			}
			clonedEntry.Vector = append([]float32(nil), entry.Vector...)
			cloned.Entries[key] = clonedEntry
		}
	}
	if idx.Buckets != nil {
		cloned.Buckets = make(map[string][]string, len(idx.Buckets))
		for key, values := range idx.Buckets {
			cloned.Buckets[key] = append([]string(nil), values...)
		}
	}
	return &cloned
}
