package qa

import (
	"bufio"
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

func init() {
	rand.Seed(time.Now().UnixNano())
}

type SemanticANNAuditConfig struct {
	SampleEnabled      bool
	SampleRate         float64
	ExportFallbackOnly bool
	ExportPath         string
}

type SemanticANNSampleRecord struct {
	Timestamp                 int64   `json:"ts,omitempty"`
	Query                     string  `json:"query,omitempty"`
	NormalizedQuery           string  `json:"normalized_query,omitempty"`
	SemanticMode              string  `json:"semantic_mode,omitempty"`
	SemanticModeUsed          string  `json:"semantic_mode_used,omitempty"`
	TopK                      int     `json:"top_k,omitempty"`
	Threshold                 float64 `json:"threshold,omitempty"`
	MinScoreGap               float64 `json:"min_score_gap,omitempty"`
	CandidateCount            int     `json:"candidate_count,omitempty"`
	Top1Score                 float64 `json:"top1_score,omitempty"`
	Top2Score                 float64 `json:"top2_score,omitempty"`
	ScoreGap                  float64 `json:"score_gap,omitempty"`
	DirectDecision            string  `json:"direct_decision,omitempty"`
	FallbackReason            string  `json:"fallback_reason,omitempty"`
	FinalSource               string  `json:"final_source,omitempty"`
	FinalHit                  bool    `json:"final_hit"`
	SelectedEmbeddingProvider string  `json:"selected_embedding_provider,omitempty"`
	SelectedEmbeddingModel    string  `json:"selected_embedding_model,omitempty"`
	ActualEmbeddingProvider   string  `json:"actual_embedding_provider,omitempty"`
	ActualEmbeddingModel      string  `json:"actual_embedding_model,omitempty"`
	Scope                     string  `json:"scope,omitempty"`
	CorpusLimit               int     `json:"corpus_limit,omitempty"`
	RequestID                 string  `json:"request_id,omitempty"`
	SessionID                 string  `json:"session_id,omitempty"`
	QAHitType                 string  `json:"qa_hit_type,omitempty"`
}

type SemanticANNNumericSummary struct {
	Count   uint64  `json:"count"`
	Min     float64 `json:"min,omitempty"`
	Max     float64 `json:"max,omitempty"`
	Sum     float64 `json:"sum,omitempty"`
	Average float64 `json:"average,omitempty"`
}

type SemanticANNStatsSnapshot struct {
	Total                uint64                    `json:"total"`
	DirectDecisionCounts map[string]uint64         `json:"direct_decision_counts,omitempty"`
	FallbackReasonCounts map[string]uint64         `json:"fallback_reason_counts,omitempty"`
	PathCounts           map[string]uint64         `json:"path_counts,omitempty"`
	CandidateCount       SemanticANNNumericSummary `json:"candidate_count"`
	Top1Score            SemanticANNNumericSummary `json:"top1_score"`
	Top2Score            SemanticANNNumericSummary `json:"top2_score"`
	ScoreGap             SemanticANNNumericSummary `json:"score_gap"`
}

type semanticANNNumericAccumulator struct {
	count uint64
	sum   float64
	min   float64
	max   float64
	ready bool
}

func (a *semanticANNNumericAccumulator) Observe(value float64) {
	if a == nil {
		return
	}
	if !a.ready {
		a.min = value
		a.max = value
		a.ready = true
	} else {
		if value < a.min {
			a.min = value
		}
		if value > a.max {
			a.max = value
		}
	}
	a.count++
	a.sum += value
}

func (a semanticANNNumericAccumulator) Snapshot() SemanticANNNumericSummary {
	if a.count == 0 {
		return SemanticANNNumericSummary{}
	}
	return SemanticANNNumericSummary{
		Count:   a.count,
		Min:     a.min,
		Max:     a.max,
		Sum:     a.sum,
		Average: a.sum / float64(a.count),
	}
}

type semanticANNStatsAccumulator struct {
	total                uint64
	directDecisionCounts map[string]uint64
	fallbackReasonCounts map[string]uint64
	pathCounts           map[string]uint64
	candidateCount       semanticANNNumericAccumulator
	top1Score            semanticANNNumericAccumulator
	top2Score            semanticANNNumericAccumulator
	scoreGap             semanticANNNumericAccumulator
}

func newSemanticANNStatsAccumulator() semanticANNStatsAccumulator {
	return semanticANNStatsAccumulator{
		directDecisionCounts: make(map[string]uint64),
		fallbackReasonCounts: make(map[string]uint64),
		pathCounts:           make(map[string]uint64),
	}
}

func normalizeSemanticANNDecision(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func semanticANNReasonKeys(reason string) []string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil
	}
	prefix := semanticANNReasonDirectRejectedFallbackBrute + ":"
	if strings.HasPrefix(reason, prefix) {
		inner := strings.TrimSpace(strings.TrimPrefix(reason, prefix))
		keys := []string{semanticANNReasonDirectRejectedFallbackBrute}
		if inner != "" {
			keys = append(keys, inner)
		}
		return keys
	}
	return []string{reason}
}

func (s *semanticANNStatsAccumulator) observe(record SemanticANNSampleRecord) {
	if s == nil {
		return
	}
	s.total++
	if decision := normalizeSemanticANNDecision(record.DirectDecision); decision != "" {
		s.directDecisionCounts[decision]++
	}
	for _, reason := range semanticANNReasonKeys(record.FallbackReason) {
		s.fallbackReasonCounts[reason]++
	}
	switch {
	case record.FinalHit && strings.EqualFold(strings.TrimSpace(record.FinalSource), SemanticRecallSourceANNBucketedLSH) && normalizeSemanticANNDecision(record.DirectDecision) == semanticANNDecisionHit:
		s.pathCounts["ann_direct_hit"]++
	case record.FinalHit && strings.EqualFold(strings.TrimSpace(record.FinalSource), SemanticRecallSourceANNFallback):
		s.pathCounts["brute_force_hit"]++
	default:
		s.pathCounts["final_miss"]++
	}
	s.candidateCount.Observe(float64(record.CandidateCount))
	s.top1Score.Observe(record.Top1Score)
	if record.CandidateCount > 1 {
		s.top2Score.Observe(record.Top2Score)
		s.scoreGap.Observe(record.ScoreGap)
	}
}

func cloneCountMap(src map[string]uint64) map[string]uint64 {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]uint64, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

func (s semanticANNStatsAccumulator) Snapshot() SemanticANNStatsSnapshot {
	return SemanticANNStatsSnapshot{
		Total:                s.total,
		DirectDecisionCounts: cloneCountMap(s.directDecisionCounts),
		FallbackReasonCounts: cloneCountMap(s.fallbackReasonCounts),
		PathCounts:           cloneCountMap(s.pathCounts),
		CandidateCount:       s.candidateCount.Snapshot(),
		Top1Score:            s.top1Score.Snapshot(),
		Top2Score:            s.top2Score.Snapshot(),
		ScoreGap:             s.scoreGap.Snapshot(),
	}
}

type semanticANNSampleWriter struct {
	mu     sync.Mutex
	path   string
	file   *os.File
	writer *bufio.Writer
	closed bool
}

func newSemanticANNSampleWriter(path string) *semanticANNSampleWriter {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	return &semanticANNSampleWriter{path: path}
}

func (w *semanticANNSampleWriter) openLocked() error {
	if w == nil || w.path == "" {
		return nil
	}
	if w.file != nil {
		return nil
	}
	dir := filepath.Dir(w.path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	file, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	w.file = file
	w.writer = bufio.NewWriter(file)
	return nil
}

func (w *semanticANNSampleWriter) write(record SemanticANNSampleRecord) error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	if err := w.openLocked(); err != nil {
		w.closed = true
		return err
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if _, err := w.writer.Write(encoded); err != nil {
		w.closed = true
		return err
	}
	if err := w.writer.WriteByte('\n'); err != nil {
		w.closed = true
		return err
	}
	return w.writer.Flush()
}

func (w *semanticANNSampleWriter) Close() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	if w.writer != nil {
		if err := w.writer.Flush(); err != nil {
			if w.file != nil {
				_ = w.file.Close()
			}
			return err
		}
	}
	if w.file != nil {
		return w.file.Close()
	}
	return nil
}

type SemanticANNAuditTrail struct {
	mu     sync.Mutex
	cfg    SemanticANNAuditConfig
	stats  semanticANNStatsAccumulator
	writer *semanticANNSampleWriter
}

func newSemanticANNAuditTrail(cfg SemanticANNAuditConfig) *SemanticANNAuditTrail {
	if cfg.SampleRate <= 0 {
		cfg.SampleRate = DefaultSemanticANNSampleRate
	}
	if cfg.SampleRate > 1 {
		cfg.SampleRate = 1
	}
	if !cfg.SampleEnabled && !cfg.ExportFallbackOnly {
		cfg.ExportFallbackOnly = DefaultSemanticANNExportFallbackOnly
	}
	audit := &SemanticANNAuditTrail{
		cfg:   cfg,
		stats: newSemanticANNStatsAccumulator(),
	}
	if cfg.SampleEnabled {
		audit.writer = newSemanticANNSampleWriter(cfg.ExportPath)
	}
	return audit
}

func (a *SemanticANNAuditTrail) Observe(record SemanticANNSampleRecord) {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.stats.observe(record)
	shouldExport := false
	if a.writer != nil && a.cfg.SampleEnabled {
		decision := normalizeSemanticANNDecision(record.DirectDecision)
		if !(a.cfg.ExportFallbackOnly && decision == semanticANNDecisionHit) {
			if a.cfg.SampleRate >= 1 {
				shouldExport = true
			} else if a.cfg.SampleRate > 0 {
				shouldExport = rand.Float64() < a.cfg.SampleRate
			}
		}
	}
	writer := a.writer
	a.mu.Unlock()
	if shouldExport && writer != nil {
		_ = writer.write(record)
	}
}

func (a *SemanticANNAuditTrail) Snapshot() SemanticANNStatsSnapshot {
	if a == nil {
		return SemanticANNStatsSnapshot{}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stats.Snapshot()
}

func (a *SemanticANNAuditTrail) Close() error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.writer == nil {
		return nil
	}
	err := a.writer.Close()
	a.writer = nil
	return err
}

func (s *KVLiteStore) observeSemanticANNOutcome(req LookupRequest, result LookupResult) {
	if s == nil || s.semanticANNAudit == nil {
		return
	}
	query := strings.TrimSpace(firstNonEmpty(req.Question, req.NormalizedQuestion))
	normalizedQuery := strings.TrimSpace(firstNonEmpty(req.NormalizedQuestion, NormalizeQuestion(query)))
	semanticMode := normalizeSemanticMode(firstNonEmpty(result.SemanticModeRequested, req.SemanticMode))
	semanticModeUsed := normalizeSemanticMode(result.SemanticModeUsed)
	topK := result.SemanticANNTopK
	if topK <= 0 {
		topK = req.SemanticANNTopK
	}
	if topK <= 0 {
		topK = req.SemanticTopK
	}
	minScoreGap := result.SemanticANNMinScoreGap
	if minScoreGap <= 0 {
		minScoreGap = req.SemanticANNMinScoreGap
	}
	selectedProvider := strings.TrimSpace(s.semanticEmbeddingProfile)
	selectedModel := strings.TrimSpace(s.semanticModel)
	actualProvider := ""
	if s.semanticProvider != nil {
		actualProvider = strings.TrimSpace(s.semanticProvider.Name())
	}
	requestID := ""
	sessionID := ""
	if req.Metadata != nil {
		requestID = strings.TrimSpace(req.Metadata["request_id"])
		sessionID = strings.TrimSpace(req.Metadata["session_id"])
	}
	record := SemanticANNSampleRecord{
		Timestamp:                 time.Now().Unix(),
		Query:                     query,
		NormalizedQuery:           normalizedQuery,
		SemanticMode:              semanticMode,
		SemanticModeUsed:          semanticModeUsed,
		TopK:                      topK,
		Threshold:                 req.SemanticThreshold,
		MinScoreGap:               minScoreGap,
		CandidateCount:            result.SemanticCandidateCount,
		Top1Score:                 result.SemanticANNTopScore,
		Top2Score:                 result.SemanticANNTop2Score,
		ScoreGap:                  result.SemanticANNScoreGap,
		DirectDecision:            normalizeSemanticANNDecision(result.SemanticANNDecision),
		FallbackReason:            strings.TrimSpace(result.SemanticFallbackReason),
		FinalSource:               strings.TrimSpace(result.SemanticSource),
		FinalHit:                  result.Hit,
		SelectedEmbeddingProvider: selectedProvider,
		SelectedEmbeddingModel:    selectedModel,
		ActualEmbeddingProvider:   firstNonEmpty(actualProvider, selectedProvider, selectedModel),
		ActualEmbeddingModel:      firstNonEmpty(selectedModel, actualProvider),
		Scope:                     strings.TrimSpace(effectiveLookupScope(req)),
		CorpusLimit:               req.SemanticCorpusLimit,
		RequestID:                 requestID,
		SessionID:                 sessionID,
		QAHitType:                 strings.TrimSpace(result.HitType),
	}
	s.semanticANNAudit.Observe(record)
}

func (s *KVLiteStore) SemanticANNStatsSnapshot() SemanticANNStatsSnapshot {
	if s == nil || s.semanticANNAudit == nil {
		return SemanticANNStatsSnapshot{}
	}
	return s.semanticANNAudit.Snapshot()
}
