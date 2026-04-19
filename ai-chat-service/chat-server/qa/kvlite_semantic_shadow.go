package qa

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"strings"
	"time"

	"ai-chat-service/chat-server/llm"
)

func normalizeSemanticShadowConfig(cfg SemanticShadowConfig) SemanticShadowConfig {
	if cfg.TopK <= 0 {
		cfg.TopK = DefaultSemanticTopK
	}
	if cfg.ScoreThreshold <= 0 {
		cfg.ScoreThreshold = 0.80
	}
	if cfg.SampleRate <= 0 {
		cfg.SampleRate = 1.0
	}
	if cfg.SampleRate > 1 {
		cfg.SampleRate = 1
	}
	return cfg
}

func shouldExecuteSemanticShadow(req LookupRequest, cfg SemanticShadowConfig) bool {
	cfg = normalizeSemanticShadowConfig(cfg)
	if !cfg.Enabled {
		return false
	}
	if cfg.SampleRate >= 1 {
		return true
	}
	if cfg.SampleRate <= 0 {
		return false
	}
	sampleKey := semanticShadowSampleKey(req)
	if sampleKey == "" {
		return true
	}
	hash := sha256.Sum256([]byte(sampleKey))
	value := binary.LittleEndian.Uint64(hash[:8])
	fraction := float64(value) / float64(^uint64(0))
	return fraction < cfg.SampleRate
}

func semanticShadowSampleKey(req LookupRequest) string {
	metadataRequestID := ""
	metadataSessionID := ""
	if req.Metadata != nil {
		metadataRequestID = strings.TrimSpace(req.Metadata["request_id"])
		metadataSessionID = strings.TrimSpace(req.Metadata["session_id"])
	}
	parts := []string{
		firstNonEmpty(metadataRequestID, metadataSessionID, req.Question, req.NormalizedQuestion),
		effectiveLookupScope(req),
		strings.TrimSpace(req.Provider),
		strings.TrimSpace(req.Model),
	}
	return strings.Join(parts, "|")
}

func semanticShadowSameTop1(mainResult LookupResult, shadow llm.SemanticShadowTrace) bool {
	if !mainResult.Hit || !shadow.ShadowSemanticHit {
		return false
	}
	mainAnswerID := firstNonEmpty(mainResult.CanonicalQuestionID, mainResult.QuestionID)
	shadowAnswerID := strings.TrimSpace(shadow.ShadowSemanticAnswerID)
	if mainAnswerID == "" || shadowAnswerID == "" {
		return false
	}
	return mainAnswerID == shadowAnswerID
}

func semanticShadowDiffCategory(mainResult LookupResult, shadow llm.SemanticShadowTrace) string {
	if !shadow.ShadowSemanticEnabled {
		return ""
	}
	if strings.TrimSpace(shadow.ShadowSemanticError) != "" {
		if shadow.ShadowSemanticFallbackUsed {
			return "ann_index_missing"
		}
		return "ann_error"
	}
	mainHit := mainResult.Hit
	shadowHit := shadow.ShadowSemanticHit
	switch {
	case mainHit && shadowHit && semanticShadowSameTop1(mainResult, shadow):
		return "bf_hit_ann_hit_same_top1"
	case mainHit && shadowHit:
		return "bf_hit_ann_hit_diff_top1"
	case mainHit && !shadowHit:
		return "bf_hit_ann_miss"
	case !mainHit && shadowHit:
		return "bf_miss_ann_hit"
	default:
		return "bf_miss_ann_miss"
	}
}

func buildSemanticShadowRetrievalHits(hits []semanticANNHit) []llm.RetrievalHit {
	if len(hits) == 0 {
		return nil
	}
	results := make([]llm.RetrievalHit, 0, len(hits))
	for _, hit := range hits {
		if hit.Record == nil {
			continue
		}
		question := firstNonEmpty(hit.Record.CanonicalQuestion, hit.Record.RawQuestion, hit.Record.Question)
		results = append(results, llm.RetrievalHit{
			RecordID:        firstNonEmpty(hit.Record.CanonicalQuestionID, hit.Record.QuestionID, hit.Record.RecordID),
			Score:           hit.Score,
			HitType:         HitTypeSemantic,
			QuestionPreview: llm.PreviewText(question, 128),
			AnswerPreview:   llm.PreviewText(hit.Record.Answer, 256),
			Provider:        hit.Record.Provider,
			Model:           firstNonEmpty(hit.Record.Model, hit.Model, hit.Record.EmbeddingModel),
			SessionID:       hit.Record.SessionID,
			TurnSeq:         hit.Record.TurnSeq,
			SourceType:      llm.SourceTypeRetrieval,
		})
	}
	return results
}

func (s *KVLiteStore) lookupBySemanticANNShadowWithVector(ctx context.Context, req LookupRequest, queryVector []float32, mainResult LookupResult) llm.SemanticShadowTrace {
	shadow := llm.SemanticShadowTrace{}
	shadowCfg := normalizeSemanticShadowConfig(req.SemanticShadow)
	if s == nil || s.backend == nil || !s.semanticEnabled || !shadowCfg.Enabled || len(queryVector) == 0 {
		return shadow
	}
	if !shouldExecuteSemanticShadow(req, shadowCfg) {
		return shadow
	}
	shadow.ShadowSemanticEnabled = true
	shadow.ShadowSemanticLayer = "semantic_ann"
	shadow.ShadowSemanticTopK = shadowCfg.TopK
	shadow.ShadowSemanticIndexSource = SemanticRecallSourceANNBucketedLSH
	start := time.Now()
	defer func() {
		shadow.ShadowSemanticLatencyMS = float64(time.Since(start)) / float64(time.Millisecond)
	}()

	scope := effectiveLookupScope(req)
	corpusLimit := req.SemanticCorpusLimit
	if corpusLimit <= 0 {
		corpusLimit = s.semanticCorpusLimit
	}
	if corpusLimit <= 0 {
		corpusLimit = DefaultSemanticCorpusLimit
	}
	index, err := s.ensureSemanticANNIndex(ctx, scope, corpusLimit)
	if err != nil {
		shadow.ShadowSemanticError = err.Error()
		shadow.ShadowSemanticFallbackUsed = true
		shadow.ShadowSemanticIndexSource = semanticANNFallbackSource
		shadow.ShadowSemanticDiffCategory = semanticShadowDiffCategory(mainResult, shadow)
		return shadow
	}
	if index == nil || len(index.Entries) == 0 {
		shadow.ShadowSemanticError = "ann index missing"
		shadow.ShadowSemanticFallbackUsed = true
		shadow.ShadowSemanticIndexSource = semanticANNFallbackSource
		shadow.ShadowSemanticDiffCategory = semanticShadowDiffCategory(mainResult, shadow)
		return shadow
	}
	hits, _, ok := index.topMatches(queryVector, shadowCfg.TopK)
	shadow.ShadowSemanticIndexVersion = index.Version
	if !ok || len(hits) == 0 {
		shadow.ShadowSemanticDiffCategory = semanticShadowDiffCategory(mainResult, shadow)
		return shadow
	}
	shadow.ShadowSemanticTopHits = buildSemanticShadowRetrievalHits(hits)
	best := hits[0]
	shadow.ShadowSemanticHit = best.Score >= shadowCfg.ScoreThreshold
	shadow.ShadowSemanticAnswerID = best.QuestionID
	if best.Record != nil {
		shadow.ShadowSemanticAnswer = strings.TrimSpace(best.Record.Answer)
	}
	shadow.ShadowSemanticTopScore = best.Score
	shadow.ShadowSemanticSameTop1 = semanticShadowSameTop1(mainResult, shadow)
	shadow.ShadowSemanticDiffCategory = semanticShadowDiffCategory(mainResult, shadow)
	return shadow
}
