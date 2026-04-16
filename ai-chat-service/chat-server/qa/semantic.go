package qa

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"ai-chat-service/chat-server/llm"
)

type SemanticConfig struct {
	Enabled        bool
	TopK           int
	Threshold      float64
	CorpusLimit    int
	EmbeddingModel string
	EmbeddingDim   int
	AsyncBackfill  bool
}

type semanticEmbeddingPayload struct {
	Model     string    `json:"model"`
	Provider  string    `json:"provider,omitempty"`
	Dim       int       `json:"dim"`
	Vector    []float32 `json:"vector"`
	UpdatedAt int64     `json:"updated_at"`
}

type semanticFeature struct {
	Value  string
	Weight float32
}

type hashEmbeddingProvider struct {
	model string
	dim   int
}

func NewHashEmbeddingProvider(model string, dim int) llm.EmbeddingProvider {
	if dim <= 0 {
		dim = DefaultSemanticEmbeddingDim
	}
	model = strings.TrimSpace(model)
	if model == "" {
		model = "placeholder-hash-embedding"
	}
	return &hashEmbeddingProvider{model: model, dim: dim}
}

func (p *hashEmbeddingProvider) Name() string {
	if p == nil {
		return ""
	}
	return p.model
}

func (p *hashEmbeddingProvider) Capabilities() llm.CapabilitySet {
	return llm.CapabilitySet{
		SupportsEmbeddings: true,
	}
}

func (p *hashEmbeddingProvider) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if p == nil {
		return nil, fmt.Errorf("embedding provider is nil")
	}
	if len(texts) == 0 {
		return nil, nil
	}
	vectors := make([][]float32, 0, len(texts))
	for _, text := range texts {
		vectors = append(vectors, semanticVectorFromText(text, p.dim))
	}
	return vectors, nil
}

func semanticEmbeddingKey(questionID string) string {
	return fmt.Sprintf("qemb:%s", strings.TrimSpace(questionID))
}

func semanticScopeIndexKey(scope string) string {
	return fmt.Sprintf("qsemlist:%s", normalizeScope(scope))
}

func semanticFeatureTerms(text string) []semanticFeature {
	normalized := NormalizeQuestion(text)
	features := make([]semanticFeature, 0, 1+len(strings.Fields(text))*2)
	if normalized != "" {
		features = append(features, semanticFeature{Value: normalized, Weight: 2})
	}
	for _, token := range candidateTokenize(text) {
		features = append(features, semanticFeature{Value: token, Weight: 1.25})
	}
	for _, signal := range candidateSignalsFromQuestion(text, 32) {
		features = append(features, semanticFeature{Value: signal, Weight: 0.75})
	}
	return uniqueSemanticFeatures(features)
}

func uniqueSemanticFeatures(features []semanticFeature) []semanticFeature {
	if len(features) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(features))
	result := make([]semanticFeature, 0, len(features))
	for _, feature := range features {
		value := strings.TrimSpace(feature.Value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, semanticFeature{Value: value, Weight: feature.Weight})
	}
	return result
}

func semanticVectorFromText(text string, dim int) []float32 {
	if dim <= 0 {
		dim = DefaultSemanticEmbeddingDim
	}
	vector := make([]float32, dim)
	features := semanticFeatureTerms(text)
	if len(features) == 0 {
		return vector
	}
	for _, feature := range features {
		if feature.Value == "" {
			continue
		}
		sum := sha256.Sum256([]byte(feature.Value))
		index := int(binary.LittleEndian.Uint64(sum[:8]) % uint64(dim))
		sign := float32(1)
		if sum[8]&1 == 1 {
			sign = -1
		}
		vector[index] += sign * feature.Weight
	}
	normalizeSemanticVector(vector)
	return vector
}

func normalizeSemanticVector(vector []float32) {
	var norm float64
	for _, value := range vector {
		norm += float64(value * value)
	}
	if norm <= 0 {
		return
	}
	scale := float32(1 / math.Sqrt(norm))
	for index := range vector {
		vector[index] *= scale
	}
}

func cosineSimilarity(left, right []float32) float64 {
	if len(left) == 0 || len(right) == 0 {
		return 0
	}
	limit := len(left)
	if len(right) < limit {
		limit = len(right)
	}
	var dot float64
	var leftNorm float64
	var rightNorm float64
	for index := 0; index < limit; index++ {
		leftValue := float64(left[index])
		rightValue := float64(right[index])
		dot += leftValue * rightValue
		leftNorm += leftValue * leftValue
		rightNorm += rightValue * rightValue
	}
	if leftNorm <= 0 || rightNorm <= 0 {
		return 0
	}
	return dot / (math.Sqrt(leftNorm) * math.Sqrt(rightNorm))
}

func encodeSemanticEmbeddingPayload(payload semanticEmbeddingPayload) (string, error) {
	if payload.Dim <= 0 {
		payload.Dim = len(payload.Vector)
	}
	if payload.Vector == nil {
		payload.Vector = []float32{}
	}
	bytes, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(bytes), nil
}

func decodeSemanticEmbeddingPayload(payload string) (semanticEmbeddingPayload, error) {
	trimmed := strings.TrimSpace(payload)
	if trimmed == "" {
		return semanticEmbeddingPayload{}, nil
	}
	result := semanticEmbeddingPayload{}
	if err := json.Unmarshal([]byte(trimmed), &result); err != nil {
		return semanticEmbeddingPayload{}, err
	}
	if result.Dim <= 0 {
		result.Dim = len(result.Vector)
	}
	return result, nil
}
