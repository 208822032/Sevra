package qa

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
)

type SemanticANNDistributionSummary struct {
	Count   uint64  `json:"count"`
	Min     float64 `json:"min,omitempty"`
	P50     float64 `json:"p50,omitempty"`
	P90     float64 `json:"p90,omitempty"`
	P95     float64 `json:"p95,omitempty"`
	Max     float64 `json:"max,omitempty"`
	Average float64 `json:"average,omitempty"`
}

type SemanticANNReasonQueryCount struct {
	Query string `json:"query"`
	Count uint64 `json:"count"`
}

type SemanticANNAnalysisReport struct {
	TotalSamples         uint64                                   `json:"total_samples"`
	DirectDecisionCounts map[string]uint64                        `json:"direct_decision_counts,omitempty"`
	FallbackReasonCounts map[string]uint64                        `json:"fallback_reason_counts,omitempty"`
	PathCounts           map[string]uint64                        `json:"path_counts,omitempty"`
	CandidateCount       SemanticANNDistributionSummary           `json:"candidate_count"`
	Top1Score            SemanticANNDistributionSummary           `json:"top1_score"`
	Top2Score            SemanticANNDistributionSummary           `json:"top2_score"`
	ScoreGap             SemanticANNDistributionSummary           `json:"score_gap"`
	TopQueriesByReason   map[string][]SemanticANNReasonQueryCount `json:"top_queries_by_reason,omitempty"`
	Conclusion           string                                   `json:"conclusion"`
}

func LoadSemanticANNSamples(path string) ([]SemanticANNSampleRecord, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("sample path is empty")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	samples := make([]SemanticANNSampleRecord, 0, 128)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var sample SemanticANNSampleRecord
		if err := json.Unmarshal([]byte(line), &sample); err != nil {
			return nil, fmt.Errorf("decode sample line %d: %w", lineNumber, err)
		}
		samples = append(samples, sample)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return samples, nil
}

func AnalyzeSemanticANNSamples(samples []SemanticANNSampleRecord, topN int) SemanticANNAnalysisReport {
	report := SemanticANNAnalysisReport{
		DirectDecisionCounts: make(map[string]uint64),
		FallbackReasonCounts: make(map[string]uint64),
		PathCounts:           make(map[string]uint64),
		TopQueriesByReason:   make(map[string][]SemanticANNReasonQueryCount),
	}
	if topN <= 0 {
		topN = 5
	}
	candidateValues := make([]float64, 0, len(samples))
	top1Values := make([]float64, 0, len(samples))
	top2Values := make([]float64, 0, len(samples))
	scoreGapValues := make([]float64, 0, len(samples))
	reasonQueryCounts := map[string]map[string]uint64{
		semanticANNReasonNoCandidates:        {},
		semanticANNReasonScoreBelowThreshold: {},
		semanticANNReasonScoreGapTooSmall:    {},
		semanticANNReasonAmbiguousTopK:       {},
	}

	for _, sample := range samples {
		report.TotalSamples++
		if decision := normalizeSemanticANNDecision(sample.DirectDecision); decision != "" {
			report.DirectDecisionCounts[decision]++
		}
		for _, reason := range semanticANNReasonKeys(sample.FallbackReason) {
			report.FallbackReasonCounts[reason]++
			if _, ok := reasonQueryCounts[reason]; ok {
				key := semanticANNSampleQueryKey(sample)
				if key != "" {
					if _, exists := reasonQueryCounts[reason][key]; !exists {
						reasonQueryCounts[reason][key] = 0
					}
					reasonQueryCounts[reason][key]++
				}
			}
		}
		switch {
		case sample.FinalHit && strings.EqualFold(strings.TrimSpace(sample.FinalSource), SemanticRecallSourceANNBucketedLSH) && normalizeSemanticANNDecision(sample.DirectDecision) == semanticANNDecisionHit:
			report.PathCounts["ann_direct_hit"]++
		case sample.FinalHit && strings.EqualFold(strings.TrimSpace(sample.FinalSource), SemanticRecallSourceANNFallback):
			report.PathCounts["brute_force_hit"]++
		default:
			report.PathCounts["final_miss"]++
		}
		candidateValues = append(candidateValues, float64(maxInt(sample.CandidateCount, 0)))
		top1Values = append(top1Values, sample.Top1Score)
		if sample.CandidateCount > 1 {
			top2Values = append(top2Values, sample.Top2Score)
			scoreGapValues = append(scoreGapValues, sample.ScoreGap)
		}
	}

	report.CandidateCount = buildSemanticANNDistributionSummary(candidateValues)
	report.Top1Score = buildSemanticANNDistributionSummary(top1Values)
	report.Top2Score = buildSemanticANNDistributionSummary(top2Values)
	report.ScoreGap = buildSemanticANNDistributionSummary(scoreGapValues)

	for reason, counts := range reasonQueryCounts {
		report.TopQueriesByReason[reason] = topSemanticANNQueryCounts(counts, topN)
	}
	report.Conclusion = semanticANNConclusion(report)
	return report
}

func semanticANNSampleQueryKey(sample SemanticANNSampleRecord) string {
	if trimmed := strings.TrimSpace(sample.NormalizedQuery); trimmed != "" {
		return trimmed
	}
	if trimmed := strings.TrimSpace(sample.Query); trimmed != "" {
		return trimmed
	}
	return strings.TrimSpace(sample.RequestID)
}

func buildSemanticANNDistributionSummary(values []float64) SemanticANNDistributionSummary {
	if len(values) == 0 {
		return SemanticANNDistributionSummary{}
	}
	sortedValues := append([]float64(nil), values...)
	sort.Float64s(sortedValues)
	var sum float64
	for _, value := range sortedValues {
		sum += value
	}
	return SemanticANNDistributionSummary{
		Count:   uint64(len(sortedValues)),
		Min:     sortedValues[0],
		P50:     semanticANNPercentile(sortedValues, 50),
		P90:     semanticANNPercentile(sortedValues, 90),
		P95:     semanticANNPercentile(sortedValues, 95),
		Max:     sortedValues[len(sortedValues)-1],
		Average: sum / float64(len(sortedValues)),
	}
}

func semanticANNPercentile(sortedValues []float64, percentile float64) float64 {
	if len(sortedValues) == 0 {
		return 0
	}
	if percentile <= 0 {
		return sortedValues[0]
	}
	if percentile >= 100 {
		return sortedValues[len(sortedValues)-1]
	}
	position := (percentile / 100) * float64(len(sortedValues)-1)
	lowerIndex := int(math.Floor(position))
	upperIndex := int(math.Ceil(position))
	if lowerIndex == upperIndex {
		return sortedValues[lowerIndex]
	}
	weight := position - float64(lowerIndex)
	return sortedValues[lowerIndex]*(1-weight) + sortedValues[upperIndex]*weight
}

func topSemanticANNQueryCounts(counts map[string]uint64, topN int) []SemanticANNReasonQueryCount {
	if len(counts) == 0 || topN <= 0 {
		return nil
	}
	items := make([]SemanticANNReasonQueryCount, 0, len(counts))
	for query, count := range counts {
		items = append(items, SemanticANNReasonQueryCount{Query: query, Count: count})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Count == items[j].Count {
			return items[i].Query < items[j].Query
		}
		return items[i].Count > items[j].Count
	})
	if topN > len(items) {
		topN = len(items)
	}
	return items[:topN]
}

func semanticANNConclusion(report SemanticANNAnalysisReport) string {
	if report.TotalSamples == 0 {
		return "没有样本，无法判断是否值得进入下一阶段"
	}
	directHit := report.PathCounts["ann_direct_hit"]
	bruteForceHit := report.PathCounts["brute_force_hit"]
	finalMiss := report.PathCounts["final_miss"]
	coverageScore := report.FallbackReasonCounts[semanticANNReasonNoCandidates] + report.FallbackReasonCounts[semanticANNReasonScoreBelowThreshold]
	sortingScore := report.FallbackReasonCounts[semanticANNReasonScoreGapTooSmall] + report.FallbackReasonCounts[semanticANNReasonAmbiguousTopK]
	directHitRatio := float64(directHit) / float64(report.TotalSamples)
	fallbackRatio := float64(bruteForceHit+finalMiss) / float64(report.TotalSamples)
	if directHitRatio >= 0.70 && fallbackRatio <= 0.25 {
		return "direct hit 已经稳定且 fallback 占比较低，建议保持当前方案不动"
	}
	if coverageScore >= sortingScore {
		return "更像覆盖问题：优先看 ann_no_candidates / ann_score_below_threshold，而不是先上 rerank"
	}
	return "更像排序不稳问题：ann_score_gap_too_small / ann_ambiguous_topk 占主导，才值得考虑 rerank"
}

func (r SemanticANNAnalysisReport) RenderText() string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "[semantic-ann-eval] samples: %d\n", r.TotalSamples)
	fmt.Fprintf(&builder, "[semantic-ann-eval] direct decision: hit=%d fallback=%d miss=%d\n", r.DirectDecisionCounts[semanticANNDecisionHit], r.DirectDecisionCounts[semanticANNDecisionFallback], r.DirectDecisionCounts[semanticANNDecisionMiss])
	fmt.Fprintf(&builder, "[semantic-ann-eval] path distribution: ann_direct_hit=%d brute_force_hit=%d final_miss=%d\n", r.PathCounts["ann_direct_hit"], r.PathCounts["brute_force_hit"], r.PathCounts["final_miss"])
	builder.WriteString("[semantic-ann-eval] fallback reasons:\n")
	for _, key := range sortedSemanticANNKeys(r.FallbackReasonCounts) {
		fmt.Fprintf(&builder, "  - %s: %d\n", key, r.FallbackReasonCounts[key])
	}
	builder.WriteString("[semantic-ann-eval] top1 score: ")
	builder.WriteString(renderSemanticANNDistribution(r.Top1Score))
	builder.WriteByte('\n')
	builder.WriteString("[semantic-ann-eval] score gap: ")
	builder.WriteString(renderSemanticANNDistribution(r.ScoreGap))
	builder.WriteByte('\n')
	builder.WriteString("[semantic-ann-eval] candidate count: ")
	builder.WriteString(renderSemanticANNDistribution(r.CandidateCount))
	builder.WriteByte('\n')
	builder.WriteString("[semantic-ann-eval] queries likely to fall into key reasons:\n")
	for _, reason := range []string{semanticANNReasonNoCandidates, semanticANNReasonScoreBelowThreshold, semanticANNReasonScoreGapTooSmall, semanticANNReasonAmbiguousTopK} {
		queries := r.TopQueriesByReason[reason]
		if len(queries) == 0 {
			continue
		}
		fmt.Fprintf(&builder, "  - %s:\n", reason)
		for _, item := range queries {
			fmt.Fprintf(&builder, "    %s (%d)\n", item.Query, item.Count)
		}
	}
	fmt.Fprintf(&builder, "[semantic-ann-eval] conclusion: %s\n", r.Conclusion)
	return builder.String()
}

func renderSemanticANNDistribution(summary SemanticANNDistributionSummary) string {
	if summary.Count == 0 {
		return "no samples"
	}
	return fmt.Sprintf("count=%d min=%.4f p50=%.4f p90=%.4f p95=%.4f max=%.4f avg=%.4f", summary.Count, summary.Min, summary.P50, summary.P90, summary.P95, summary.Max, summary.Average)
}

func sortedSemanticANNKeys(values map[string]uint64) []string {
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}
