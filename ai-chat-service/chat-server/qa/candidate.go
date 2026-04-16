package qa

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"ai-chat-service/chat-server/llm"
)

type candidateScoreBreakdown struct {
	BigramScore float64
	TokenScore  float64
	ExactBonus  float64
	Score       float64
}

func candidateBucketKey(scope, signal string) string {
	return fmt.Sprintf("qcand:%s:bg:%s", normalizeScope(scope), encodeCandidateSignal(signal))
}

func encodeCandidateSignal(signal string) string {
	return hex.EncodeToString([]byte(strings.TrimSpace(signal)))
}

func candidateSignalsFromQuestion(question string, limit int) []string {
	normalized := NormalizeQuestion(question)
	if normalized == "" {
		return nil
	}
	runes := []rune(normalized)
	if len(runes) == 1 {
		return []string{normalized}
	}
	signals := make([]string, 0, len(runes)-1)
	seen := make(map[string]struct{}, len(runes)-1)
	for i := 0; i < len(runes)-1; i++ {
		signal := string(runes[i : i+2])
		if _, ok := seen[signal]; ok {
			continue
		}
		seen[signal] = struct{}{}
		signals = append(signals, signal)
	}
	if len(signals) == 0 {
		signals = []string{normalized}
	}
	if limit > 0 && len(signals) > limit {
		signals = signals[:limit]
	}
	return signals
}

func candidateTokenize(question string) []string {
	trimmed := strings.TrimSpace(strings.ToLower(question))
	if trimmed == "" {
		return nil
	}
	tokens := make([]string, 0, len(trimmed))
	var builder strings.Builder
	flush := func() {
		if builder.Len() == 0 {
			return
		}
		token := strings.TrimSpace(builder.String())
		builder.Reset()
		if token != "" {
			tokens = append(tokens, token)
		}
	}
	for _, r := range trimmed {
		switch {
		case unicode.IsLetter(r) || unicode.IsNumber(r):
			builder.WriteRune(unicode.ToLower(r))
		case unicode.In(r, unicode.Han):
			flush()
			tokens = append(tokens, string(r))
		default:
			flush()
		}
	}
	flush()
	return uniqueStrings(tokens)
}

func candidateF1Score(queryTerms, candidateTerms []string) float64 {
	if len(queryTerms) == 0 || len(candidateTerms) == 0 {
		return 0
	}
	querySet := make(map[string]struct{}, len(queryTerms))
	for _, term := range queryTerms {
		querySet[term] = struct{}{}
	}
	candidateSet := make(map[string]struct{}, len(candidateTerms))
	for _, term := range candidateTerms {
		candidateSet[term] = struct{}{}
	}
	shared := 0
	for term := range querySet {
		if _, ok := candidateSet[term]; ok {
			shared++
		}
	}
	if shared == 0 {
		return 0
	}
	precision := float64(shared) / float64(len(candidateSet))
	recall := float64(shared) / float64(len(querySet))
	if precision+recall == 0 {
		return 0
	}
	return 2 * precision * recall / (precision + recall)
}

func candidateRankIDsByCount(counts map[string]int, limit int) []string {
	if len(counts) == 0 || limit <= 0 {
		return nil
	}
	ids := make([]string, 0, len(counts))
	for id := range counts {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if counts[ids[i]] != counts[ids[j]] {
			return counts[ids[i]] > counts[ids[j]]
		}
		return ids[i] < ids[j]
	})
	if len(ids) > limit {
		ids = ids[:limit]
	}
	return ids
}

func encodeCandidateBucketIDs(ids []string) (string, error) {
	return encodeJSONStringSlice(uniqueStrings(ids))
}

func decodeCandidateBucketIDs(payload string) ([]string, error) {
	trimmed := strings.TrimSpace(payload)
	if trimmed == "" {
		return nil, nil
	}
	var ids []string
	if err := json.Unmarshal([]byte(trimmed), &ids); err == nil {
		return uniqueStrings(ids), nil
	}
	return nil, fmt.Errorf("invalid candidate bucket payload")
}

func encodeJSONStringSlice(values []string) (string, error) {
	if values == nil {
		values = []string{}
	}
	bytes, err := json.Marshal(values)
	if err != nil {
		return "", err
	}
	return string(bytes), nil
}

func updateCandidateBucketIDs(existing []string, questionID string, limit int) []string {
	questionID = strings.TrimSpace(questionID)
	if questionID == "" {
		return uniqueStrings(existing)
	}
	if limit <= 0 {
		limit = DefaultCandidateBucketSize
	}
	updated := make([]string, 0, min(len(existing)+1, limit))
	updated = append(updated, questionID)
	seen := map[string]struct{}{questionID: {}}
	for _, id := range existing {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		updated = append(updated, id)
		if len(updated) >= limit {
			break
		}
	}
	return updated
}

func uniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		result = append(result, trimmed)
	}
	return result
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func scoreCandidateAnswer(queryQuestion string, candidate *llm.QARecord) candidateScoreBreakdown {
	if candidate == nil {
		return candidateScoreBreakdown{}
	}
	candidateQuestion := firstNonEmpty(candidate.CanonicalQuestion, candidate.RawQuestion, candidate.Question)
	queryNormalized := NormalizeQuestion(queryQuestion)
	candidateNormalized := NormalizeQuestion(candidateQuestion)
	bigramScore := candidateF1Score(candidateSignalsFromQuestion(queryNormalized, 0), candidateSignalsFromQuestion(candidateNormalized, 0))
	tokenScore := candidateF1Score(candidateTokenize(queryQuestion), candidateTokenize(candidateQuestion))
	exactBonus := 0.0
	if queryNormalized != "" && queryNormalized == candidateNormalized {
		exactBonus += 0.18
	}
	if strings.EqualFold(strings.TrimSpace(queryQuestion), strings.TrimSpace(candidateQuestion)) {
		exactBonus += 0.08
	}
	score := 0.78*bigramScore + 0.22*tokenScore + exactBonus
	if score > 1 {
		score = 1
	}
	return candidateScoreBreakdown{
		BigramScore: bigramScore,
		TokenScore:  tokenScore,
		ExactBonus:  exactBonus,
		Score:       score,
	}
}
