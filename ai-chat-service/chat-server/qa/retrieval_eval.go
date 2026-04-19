package qa

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"ai-chat-service/chat-server/llm"
)

const (
	retrievalEvalDefaultNamespace = "test:retrieval-eval"
	retrievalEvalDefaultTTL       = time.Hour
)

type RetrievalEvalSuite struct {
	Name          string                      `json:"name"`
	Version       string                      `json:"version"`
	Description   string                      `json:"description,omitempty"`
	DefaultScope  RetrievalEvalScope          `json:"default_scope"`
	Seeds         []RetrievalEvalSeed         `json:"seeds"`
	Cases         []RetrievalEvalCase         `json:"cases"`
	ParameterSets []RetrievalEvalParameterSet `json:"parameter_sets"`
}

type RetrievalEvalScope struct {
	Provider      string `json:"provider"`
	Model         string `json:"model"`
	PromptVersion string `json:"prompt_version"`
}

type RetrievalEvalSeed struct {
	ID       string              `json:"id"`
	Question string              `json:"question"`
	Answer   string              `json:"answer"`
	Scope    *RetrievalEvalScope `json:"scope,omitempty"`
	Aliases  []string            `json:"aliases,omitempty"`
	Tags     []string            `json:"tags,omitempty"`
}

type RetrievalEvalCase struct {
	ID               string              `json:"id"`
	Query            string              `json:"query"`
	ExpectedBehavior string              `json:"expected_behavior"`
	ExpectedLayer    string              `json:"expected_layer"`
	ExpectedAnswerID string              `json:"expected_answer_id,omitempty"`
	Scope            *RetrievalEvalScope `json:"scope,omitempty"`
	Tags             []string            `json:"tags,omitempty"`
	Notes            string              `json:"notes,omitempty"`
}

type RetrievalEvalParameterSet struct {
	Name                    string  `json:"name"`
	CandidateTopK           int     `json:"candidate_top_k"`
	CandidateScoreThreshold float64 `json:"candidate_score_threshold"`
	SemanticTopK            int     `json:"semantic_top_k"`
	SemanticScoreThreshold  float64 `json:"semantic_score_threshold"`
	SemanticCorpusLimit     int     `json:"semantic_corpus_limit"`
}

type RetrievalEvalReport struct {
	SuiteName               string                          `json:"suite_name"`
	SuiteVersion            string                          `json:"suite_version"`
	Description             string                          `json:"description,omitempty"`
	GeneratedAt             time.Time                       `json:"generated_at"`
	DatasetPath             string                          `json:"dataset_path,omitempty"`
	DefaultScope            RetrievalEvalScope              `json:"default_scope"`
	EmbeddingMode           string                          `json:"embedding_mode"`
	EmbeddingProfile        string                          `json:"embedding_profile,omitempty"`
	EmbeddingProvider       string                          `json:"embedding_provider,omitempty"`
	EmbeddingModel          string                          `json:"embedding_model,omitempty"`
	EmbeddingSource         string                          `json:"embedding_source,omitempty"`
	SemanticMode            string                          `json:"semantic_mode,omitempty"`
	ComparisonSemanticMode  string                          `json:"comparison_semantic_mode,omitempty"`
	SemanticComparison      RetrievalEvalSemanticComparison `json:"semantic_comparison,omitempty"`
	ANNRecommendation       string                          `json:"ann_recommendation,omitempty"`
	ANNRecommendationReason string                          `json:"ann_recommendation_reason,omitempty"`
	Results                 []RetrievalEvalParameterReport  `json:"results"`
}

type RetrievalEvalSemanticComparison struct {
	SelectedMode            string                          `json:"selected_mode,omitempty"`
	ComparisonMode          string                          `json:"comparison_mode,omitempty"`
	ANNHitCount             int                             `json:"ann_hit_count"`
	ANNHitRate              float64                         `json:"ann_hit_rate"`
	ANNAvgLatencyMs         float64                         `json:"ann_avg_latency_ms"`
	ANNP95LatencyMs         float64                         `json:"ann_p95_latency_ms"`
	ANNWrongHitRatio        float64                         `json:"ann_wrong_hit_ratio"`
	BruteForceHitCount      int                             `json:"brute_force_hit_count"`
	BruteForceHitRate       float64                         `json:"brute_force_hit_rate"`
	BruteForceAvgLatencyMs  float64                         `json:"brute_force_avg_latency_ms"`
	BruteForceP95LatencyMs  float64                         `json:"brute_force_p95_latency_ms"`
	BruteForceWrongHitRatio float64                         `json:"brute_force_wrong_hit_ratio"`
	ANNOnlyWins             int                             `json:"ann_only_wins"`
	BruteForceOnlyWins      int                             `json:"brute_force_only_wins"`
	ANNVsBruteForceOverlap  int                             `json:"ann_vs_bruteforce_overlap"`
	DiffCases               []RetrievalEvalSemanticDiffCase `json:"diff_cases,omitempty"`
}

type RetrievalEvalSemanticDiffCase struct {
	CaseID              string  `json:"case_id"`
	Query               string  `json:"query"`
	ExpectedBehavior    string  `json:"expected_behavior"`
	ExpectedLayer       string  `json:"expected_layer"`
	ExpectedAnswerID    string  `json:"expected_answer_id,omitempty"`
	ANNHit              bool    `json:"ann_hit"`
	ANNLayer            string  `json:"ann_layer,omitempty"`
	ANNAnswerID         string  `json:"ann_answer_id,omitempty"`
	ANNAnswer           string  `json:"ann_answer,omitempty"`
	ANNTopScore         float64 `json:"ann_top_score,omitempty"`
	ANNLatencyMs        float64 `json:"ann_latency_ms,omitempty"`
	BruteForceHit       bool    `json:"brute_force_hit"`
	BruteForceLayer     string  `json:"brute_force_layer,omitempty"`
	BruteForceAnswerID  string  `json:"brute_force_answer_id,omitempty"`
	BruteForceAnswer    string  `json:"brute_force_answer,omitempty"`
	BruteForceTopScore  float64 `json:"brute_force_top_score,omitempty"`
	BruteForceLatencyMs float64 `json:"brute_force_latency_ms,omitempty"`
	Winner              string  `json:"winner,omitempty"`
	MismatchReason      string  `json:"mismatch_reason,omitempty"`
}

type RetrievalEvalParameterReport struct {
	ParameterSet            RetrievalEvalParameterSet       `json:"parameter_set"`
	SemanticMode            string                          `json:"semantic_mode,omitempty"`
	ComparisonSemanticMode  string                          `json:"comparison_semantic_mode,omitempty"`
	TotalCases              int                             `json:"total_cases"`
	ExactHitCount           int                             `json:"exact_hit_count"`
	NormalizedHitCount      int                             `json:"normalized_hit_count"`
	AliasHitCount           int                             `json:"alias_hit_count"`
	CandidateHitCount       int                             `json:"candidate_hit_count"`
	SemanticHitCount        int                             `json:"semantic_hit_count"`
	FallbackToLLMCount      int                             `json:"fallback_to_llm_count"`
	WrongHitCount           int                             `json:"wrong_hit_count"`
	FalsePositiveCount      int                             `json:"false_positive_count"`
	FalseNegativeCount      int                             `json:"false_negative_count"`
	LexicalOnlyWins         int                             `json:"lexical_only_wins"`
	SemanticOnlyWins        int                             `json:"semantic_only_wins"`
	OverlapHits             int                             `json:"overlap_hits"`
	AllMissCount            int                             `json:"all_miss_count"`
	ExactHitRate            float64                         `json:"exact_hit_rate"`
	NormalizedHitRate       float64                         `json:"normalized_hit_rate"`
	AliasHitRate            float64                         `json:"alias_hit_rate"`
	CandidateHitRate        float64                         `json:"candidate_hit_rate"`
	SemanticHitRate         float64                         `json:"semantic_hit_rate"`
	FallbackToLLMRatio      float64                         `json:"fallback_to_llm_ratio"`
	WrongHitRatio           float64                         `json:"wrong_hit_ratio"`
	AvgLatencyMs            float64                         `json:"avg_latency_ms"`
	P95LatencyMs            float64                         `json:"p95_latency_ms"`
	LexicalAvgLatencyMs     float64                         `json:"lexical_avg_latency_ms"`
	LexicalP95LatencyMs     float64                         `json:"lexical_p95_latency_ms"`
	CandidateAvgLatencyMs   float64                         `json:"candidate_avg_latency_ms"`
	CandidateP95LatencyMs   float64                         `json:"candidate_p95_latency_ms"`
	SemanticAvgLatencyMs    float64                         `json:"semantic_avg_latency_ms"`
	SemanticP95LatencyMs    float64                         `json:"semantic_p95_latency_ms"`
	EmbeddingMode           string                          `json:"embedding_mode,omitempty"`
	EmbeddingProfile        string                          `json:"embedding_profile,omitempty"`
	EmbeddingProvider       string                          `json:"embedding_provider,omitempty"`
	EmbeddingModel          string                          `json:"embedding_model,omitempty"`
	EmbeddingSource         string                          `json:"embedding_source,omitempty"`
	SemanticComparison      RetrievalEvalSemanticComparison `json:"semantic_comparison,omitempty"`
	ANNRecommendation       string                          `json:"ann_recommendation,omitempty"`
	ANNRecommendationReason string                          `json:"ann_recommendation_reason,omitempty"`
	WrongHitCases           []RetrievalEvalCaseResult       `json:"wrong_hit_cases,omitempty"`
	FalseNegativeCases      []RetrievalEvalCaseResult       `json:"false_negative_cases,omitempty"`
	BorderlineCases         []RetrievalEvalCaseResult       `json:"borderline_cases,omitempty"`
	CaseResults             []RetrievalEvalCaseResult       `json:"case_results,omitempty"`
}

type RetrievalEvalCaseResult struct {
	CaseID                           string  `json:"case_id"`
	Query                            string  `json:"query"`
	ExpectedBehavior                 string  `json:"expected_behavior"`
	ExpectedLayer                    string  `json:"expected_layer"`
	ExpectedAnswerID                 string  `json:"expected_answer_id,omitempty"`
	ExpectedAnswer                   string  `json:"expected_answer,omitempty"`
	Scope                            string  `json:"scope"`
	ActualLayer                      string  `json:"actual_layer"`
	ActualHit                        bool    `json:"actual_hit"`
	ActualAnswerID                   string  `json:"actual_answer_id,omitempty"`
	ActualAnswer                     string  `json:"actual_answer,omitempty"`
	ActualCandidateCount             int     `json:"actual_candidate_count,omitempty"`
	ActualCandidateTopScore          float64 `json:"actual_candidate_top_score,omitempty"`
	ActualSemanticCandidateCount     int     `json:"actual_semantic_candidate_count,omitempty"`
	ActualSemanticTopK               int     `json:"actual_semantic_top_k,omitempty"`
	ActualSemanticTopScore           float64 `json:"actual_semantic_top_score,omitempty"`
	ActualSemanticModel              string  `json:"actual_semantic_model,omitempty"`
	ActualSemanticSource             string  `json:"actual_semantic_source,omitempty"`
	ComparisonSemanticHit            bool    `json:"comparison_semantic_hit"`
	ComparisonSemanticLayer          string  `json:"comparison_semantic_layer,omitempty"`
	ComparisonSemanticAnswer         string  `json:"comparison_semantic_answer,omitempty"`
	ComparisonSemanticAnswerID       string  `json:"comparison_semantic_answer_id,omitempty"`
	FullLatencyMs                    float64 `json:"full_latency_ms,omitempty"`
	LexicalLatencyMs                 float64 `json:"lexical_latency_ms,omitempty"`
	CandidateLatencyMs               float64 `json:"candidate_latency_ms,omitempty"`
	SemanticLatencyMs                float64 `json:"semantic_latency_ms,omitempty"`
	ShadowSemanticTopScore           float64 `json:"shadow_semantic_top_score,omitempty"`
	ComparisonSemanticCandidateCount int     `json:"comparison_semantic_candidate_count,omitempty"`
	ComparisonSemanticTopK           int     `json:"comparison_semantic_top_k,omitempty"`
	ComparisonSemanticTopScore       float64 `json:"comparison_semantic_top_score,omitempty"`
	ComparisonSemanticModel          string  `json:"comparison_semantic_model,omitempty"`
	ComparisonSemanticSource         string  `json:"comparison_semantic_source,omitempty"`
	ComparisonSemanticLatencyMs      float64 `json:"comparison_semantic_latency_ms,omitempty"`
	ShadowLexicalHit                 bool    `json:"shadow_lexical_hit"`
	ShadowLexicalLayer               string  `json:"shadow_lexical_layer,omitempty"`
	ShadowLexicalAnswer              string  `json:"shadow_lexical_answer,omitempty"`
	ShadowLexicalAnswerID            string  `json:"shadow_lexical_answer_id,omitempty"`
	ShadowSemanticHit                bool    `json:"shadow_semantic_hit"`
	ShadowSemanticLayer              string  `json:"shadow_semantic_layer,omitempty"`
	ShadowSemanticAnswer             string  `json:"shadow_semantic_answer,omitempty"`
	ShadowSemanticAnswerID           string  `json:"shadow_semantic_answer_id,omitempty"`
	Status                           string  `json:"status,omitempty"`
	MismatchReason                   string  `json:"mismatch_reason,omitempty"`
	Borderline                       bool    `json:"borderline"`
}

type retrievalEvalSeedRuntime struct {
	Seed       RetrievalEvalSeed
	Scope      RetrievalEvalScope
	ScopeText  string
	QuestionID string
	Record     *llm.QARecord
}

type retrievalEvalPreparedSuite struct {
	Suite     *RetrievalEvalSuite
	SeedMap   map[string]retrievalEvalSeedRuntime
	Seeds     []retrievalEvalSeedRuntime
	Cases     []RetrievalEvalCase
	ParamSets []RetrievalEvalParameterSet
}

type RetrievalEvalOptions struct {
	EmbeddingSource RetrievalEvalEmbeddingSource
	SemanticMode    string
}

func LoadRetrievalEvalSuite(filePath string) (*RetrievalEvalSuite, error) {
	trimmedPath := strings.TrimSpace(filePath)
	if trimmedPath == "" {
		return nil, errors.New("retrieval eval dataset path is empty")
	}
	bytes, err := os.ReadFile(trimmedPath)
	if err != nil {
		return nil, err
	}
	var suite RetrievalEvalSuite
	if err := json.Unmarshal(bytes, &suite); err != nil {
		return nil, err
	}
	if err := suite.Validate(); err != nil {
		return nil, err
	}
	return &suite, nil
}

func (s *RetrievalEvalSuite) Validate() error {
	if s == nil {
		return errors.New("retrieval eval suite is nil")
	}
	s.Name = strings.TrimSpace(s.Name)
	if s.Name == "" {
		return errors.New("retrieval eval suite name is empty")
	}
	s.Version = strings.TrimSpace(s.Version)
	if s.Version == "" {
		return errors.New("retrieval eval suite version is empty")
	}
	if strings.TrimSpace(s.DefaultScope.Provider) == "" || strings.TrimSpace(s.DefaultScope.Model) == "" || strings.TrimSpace(s.DefaultScope.PromptVersion) == "" {
		return errors.New("retrieval eval suite default_scope must include provider, model, and prompt_version")
	}
	if len(s.Seeds) == 0 {
		return errors.New("retrieval eval suite seeds are empty")
	}
	if len(s.Cases) == 0 {
		return errors.New("retrieval eval suite cases are empty")
	}
	if len(s.ParameterSets) == 0 {
		return errors.New("retrieval eval suite parameter_sets are empty")
	}
	seedSeen := make(map[string]struct{}, len(s.Seeds))
	for index, seed := range s.Seeds {
		seedID := strings.TrimSpace(seed.ID)
		if seedID == "" {
			return fmt.Errorf("seed[%d] id is empty", index)
		}
		if _, ok := seedSeen[seedID]; ok {
			return fmt.Errorf("duplicate seed id %q", seedID)
		}
		seedSeen[seedID] = struct{}{}
		if strings.TrimSpace(seed.Question) == "" {
			return fmt.Errorf("seed[%s] question is empty", seedID)
		}
		if strings.TrimSpace(seed.Answer) == "" {
			return fmt.Errorf("seed[%s] answer is empty", seedID)
		}
		if err := validateOptionalScope(seed.Scope); err != nil {
			return fmt.Errorf("seed[%s] scope: %w", seedID, err)
		}
	}
	caseSeen := make(map[string]struct{}, len(s.Cases))
	for index, evalCase := range s.Cases {
		caseID := strings.TrimSpace(evalCase.ID)
		if caseID == "" {
			return fmt.Errorf("case[%d] id is empty", index)
		}
		if _, ok := caseSeen[caseID]; ok {
			return fmt.Errorf("duplicate case id %q", caseID)
		}
		caseSeen[caseID] = struct{}{}
		if strings.TrimSpace(evalCase.Query) == "" {
			return fmt.Errorf("case[%s] query is empty", caseID)
		}
		if strings.TrimSpace(evalCase.ExpectedBehavior) == "" {
			return fmt.Errorf("case[%s] expected_behavior is empty", caseID)
		}
		if strings.TrimSpace(evalCase.ExpectedLayer) == "" {
			return fmt.Errorf("case[%s] expected_layer is empty", caseID)
		}
		if err := validateExpectedBehavior(evalCase.ExpectedBehavior); err != nil {
			return fmt.Errorf("case[%s] expected_behavior: %w", caseID, err)
		}
		if err := validateExpectedLayer(evalCase.ExpectedLayer); err != nil {
			return fmt.Errorf("case[%s] expected_layer: %w", caseID, err)
		}
		if strings.EqualFold(strings.TrimSpace(evalCase.ExpectedBehavior), "hit") && strings.TrimSpace(evalCase.ExpectedAnswerID) == "" {
			return fmt.Errorf("case[%s] expected_answer_id is required for hit cases", caseID)
		}
		if err := validateOptionalScope(evalCase.Scope); err != nil {
			return fmt.Errorf("case[%s] scope: %w", caseID, err)
		}
	}
	paramSeen := make(map[string]struct{}, len(s.ParameterSets))
	for index, param := range s.ParameterSets {
		name := strings.TrimSpace(param.Name)
		if name == "" {
			return fmt.Errorf("parameter_sets[%d] name is empty", index)
		}
		if _, ok := paramSeen[name]; ok {
			return fmt.Errorf("duplicate parameter set name %q", name)
		}
		paramSeen[name] = struct{}{}
	}
	return nil
}

func EvaluateRetrievalSuite(ctx context.Context, suite *RetrievalEvalSuite, datasetPath string) (*RetrievalEvalReport, error) {
	return EvaluateRetrievalSuiteWithOptions(ctx, suite, datasetPath, RetrievalEvalOptions{})
}

func EvaluateRetrievalSuiteWithOptions(ctx context.Context, suite *RetrievalEvalSuite, datasetPath string, options RetrievalEvalOptions) (*RetrievalEvalReport, error) {
	if suite == nil {
		return nil, errors.New("retrieval eval suite is nil")
	}
	if err := suite.Validate(); err != nil {
		return nil, err
	}
	prepared, err := prepareRetrievalEvalSuite(suite)
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	embeddingSource, err := normalizeRetrievalEvalEmbeddingSource(options.EmbeddingSource)
	if err != nil {
		return nil, err
	}
	semanticMode := normalizeSemanticMode(options.SemanticMode)
	comparisonSemanticMode := oppositeSemanticMode(semanticMode)
	sharedBackend := newRetrievalEvalMemoryKVLiteBackend()
	seedStore, err := buildRetrievalEvalStore(sharedBackend, retrievalEvalDefaultNamespace, retrievalEvalDefaultTTL, SemanticConfig{
		Enabled:        true,
		TopK:           DefaultSemanticTopK,
		Threshold:      DefaultSemanticMinScore,
		CorpusLimit:    DefaultSemanticCorpusLimit,
		EmbeddingModel: embeddingSource.Model,
		EmbeddingDim:   DefaultSemanticEmbeddingDim,
	}, embeddingSource)
	if err != nil {
		return nil, err
	}
	if err := seedRetrievalEvalStore(ctx, seedStore, prepared.Seeds); err != nil {
		return nil, err
	}
	report := &RetrievalEvalReport{
		SuiteName:              suite.Name,
		SuiteVersion:           suite.Version,
		Description:            suite.Description,
		GeneratedAt:            time.Now().UTC(),
		DatasetPath:            strings.TrimSpace(datasetPath),
		DefaultScope:           suite.DefaultScope,
		EmbeddingMode:          strings.TrimSpace(string(embeddingSource.Mode)),
		EmbeddingProfile:       strings.TrimSpace(embeddingSource.ProfileName),
		EmbeddingProvider:      strings.TrimSpace(embeddingSource.ProviderName),
		EmbeddingModel:         strings.TrimSpace(embeddingSource.Model),
		EmbeddingSource:        embeddingSource.Describe(),
		SemanticMode:           semanticMode,
		ComparisonSemanticMode: comparisonSemanticMode,
		Results:                make([]RetrievalEvalParameterReport, 0, len(prepared.ParamSets)),
	}
	for _, paramSet := range prepared.ParamSets {
		result, err := evaluateRetrievalEvalParameterSet(ctx, sharedBackend, prepared, paramSet, embeddingSource, semanticMode)
		if err != nil {
			return nil, err
		}
		result.ANNRecommendation, result.ANNRecommendationReason = decideRetrievalEvalRecommendation(embeddingSource.Mode, result)
		report.Results = append(report.Results, result)
	}
	if best := selectRetrievalEvalBestParameterReport(report.Results); best != nil {
		report.SemanticComparison = best.SemanticComparison
		report.SemanticComparison.SelectedMode = semanticMode
		report.SemanticComparison.ComparisonMode = comparisonSemanticMode
	}
	report.ANNRecommendation, report.ANNRecommendationReason = combineRetrievalEvalRecommendations(report.Results)
	return report, nil
}

func prepareRetrievalEvalSuite(suite *RetrievalEvalSuite) (*retrievalEvalPreparedSuite, error) {
	prepared := &retrievalEvalPreparedSuite{
		Suite:     suite,
		SeedMap:   make(map[string]retrievalEvalSeedRuntime, len(suite.Seeds)),
		Seeds:     make([]retrievalEvalSeedRuntime, 0, len(suite.Seeds)),
		Cases:     append([]RetrievalEvalCase(nil), suite.Cases...),
		ParamSets: append([]RetrievalEvalParameterSet(nil), suite.ParameterSets...),
	}
	for _, seed := range suite.Seeds {
		runtimeSeed, err := buildRetrievalEvalSeedRuntime(seed, suite.DefaultScope)
		if err != nil {
			return nil, err
		}
		prepared.SeedMap[runtimeSeed.Seed.ID] = runtimeSeed
		prepared.Seeds = append(prepared.Seeds, runtimeSeed)
	}
	for index, evalCase := range prepared.Cases {
		if strings.EqualFold(strings.TrimSpace(evalCase.ExpectedBehavior), "hit") {
			if _, ok := prepared.SeedMap[strings.TrimSpace(evalCase.ExpectedAnswerID)]; !ok {
				return nil, fmt.Errorf("case[%s] references unknown expected_answer_id %q", evalCase.ID, evalCase.ExpectedAnswerID)
			}
		}
		if strings.TrimSpace(evalCase.ExpectedLayer) == "miss" && !strings.EqualFold(strings.TrimSpace(evalCase.ExpectedBehavior), "miss") {
			prepared.Cases[index].ExpectedBehavior = "miss"
		}
	}
	return prepared, nil
}

func buildRetrievalEvalSeedRuntime(seed RetrievalEvalSeed, defaultScope RetrievalEvalScope) (retrievalEvalSeedRuntime, error) {
	scope := resolveRetrievalEvalScope(seed.Scope, defaultScope)
	scopeText := scope.String()
	questionID := BuildQuestionID(scopeText, ExactQuestionHash(seed.Question))
	record := &llm.QARecord{
		QuestionID:          questionID,
		RecordID:            questionID,
		CanonicalQuestionID: questionID,
		Question:            seed.Question,
		RawQuestion:         seed.Question,
		CanonicalQuestion:   seed.Question,
		QuestionHash:        QuestionHash(seed.Question),
		RawQuestionHash:     ExactQuestionHash(seed.Question),
		NormalizedQuestion:  NormalizeQuestion(seed.Question),
		Answer:              seed.Answer,
		Provider:            scope.Provider,
		Model:               scope.Model,
		PromptVersion:       scope.PromptVersion,
		Scope:               scopeText,
		SourceType:          llm.SourceTypeAPI,
		Metadata: map[string]string{
			"answer_source": llm.FinalAnswerSourceLLM,
			"seed_id":       seed.ID,
		},
	}
	return retrievalEvalSeedRuntime{
		Seed:       seed,
		Scope:      scope,
		ScopeText:  scopeText,
		QuestionID: questionID,
		Record:     record,
	}, nil
}

func evaluateRetrievalEvalParameterSet(ctx context.Context, backend kvliteBackend, prepared *retrievalEvalPreparedSuite, paramSet RetrievalEvalParameterSet, embeddingSource RetrievalEvalEmbeddingSource, semanticMode string) (RetrievalEvalParameterReport, error) {
	lexicalStoreIface := NewKVLiteStoreWithBackend(backend, retrievalEvalDefaultNamespace, retrievalEvalDefaultTTL)
	lexicalStore, ok := lexicalStoreIface.(*KVLiteStore)
	if !ok {
		return RetrievalEvalParameterReport{}, fmt.Errorf("unexpected lexical store type %T", lexicalStoreIface)
	}
	semanticStore, err := buildRetrievalEvalStore(backend, retrievalEvalDefaultNamespace, retrievalEvalDefaultTTL, SemanticConfig{
		Enabled:        true,
		TopK:           normalizePositiveInt(paramSet.SemanticTopK, DefaultSemanticTopK),
		Threshold:      normalizePositiveFloat(paramSet.SemanticScoreThreshold, DefaultSemanticMinScore),
		CorpusLimit:    normalizePositiveInt(paramSet.SemanticCorpusLimit, DefaultSemanticCorpusLimit),
		EmbeddingDim:   DefaultSemanticEmbeddingDim,
		EmbeddingModel: embeddingSource.Model,
	}, embeddingSource)
	if err != nil {
		return RetrievalEvalParameterReport{}, err
	}
	parameterReport := RetrievalEvalParameterReport{ParameterSet: paramSet}
	var actualLatencies []time.Duration
	var lexicalLatencies []time.Duration
	var candidateLatencies []time.Duration
	var semanticLatencies []time.Duration
	results := make([]RetrievalEvalCaseResult, 0, len(prepared.Cases))
	for _, evalCase := range prepared.Cases {
		result, actualLatency, lexicalLatency, candidateLatency, semanticLatency, _, err := evaluateRetrievalEvalCase(ctx, lexicalStore, semanticStore, prepared.Seeds, evalCase, paramSet, semanticMode)
		if err != nil {
			return RetrievalEvalParameterReport{}, err
		}
		results = append(results, result)
		if actualLatency > 0 {
			actualLatencies = append(actualLatencies, actualLatency)
		}
		if lexicalLatency > 0 {
			lexicalLatencies = append(lexicalLatencies, lexicalLatency)
		}
		if candidateLatency > 0 {
			candidateLatencies = append(candidateLatencies, candidateLatency)
		}
		if semanticLatency > 0 {
			semanticLatencies = append(semanticLatencies, semanticLatency)
		}
	}
	parameterReport.EmbeddingMode = strings.TrimSpace(string(embeddingSource.Mode))
	parameterReport.EmbeddingProfile = strings.TrimSpace(embeddingSource.ProfileName)
	parameterReport.EmbeddingProvider = strings.TrimSpace(embeddingSource.ProviderName)
	parameterReport.EmbeddingModel = strings.TrimSpace(embeddingSource.Model)
	parameterReport.EmbeddingSource = embeddingSource.Describe()
	parameterReport.SemanticMode = normalizeSemanticMode(semanticMode)
	parameterReport.ComparisonSemanticMode = oppositeSemanticMode(parameterReport.SemanticMode)
	parameterReport.CaseResults = results
	parameterReport.TotalCases = len(results)
	for _, result := range results {
		switch result.ActualLayer {
		case HitTypeExact:
			parameterReport.ExactHitCount++
		case HitTypeNormalized:
			parameterReport.NormalizedHitCount++
		case HitTypeAlias:
			parameterReport.AliasHitCount++
		case HitTypeCandidate:
			parameterReport.CandidateHitCount++
		case HitTypeSemantic:
			parameterReport.SemanticHitCount++
		default:
			parameterReport.FallbackToLLMCount++
		}
		if result.Status == "false_positive" {
			parameterReport.FalsePositiveCount++
			parameterReport.WrongHitCases = append(parameterReport.WrongHitCases, result)
			parameterReport.WrongHitCount++
		}
		if result.Status == "false_negative" {
			parameterReport.FalseNegativeCount++
			parameterReport.FalseNegativeCases = append(parameterReport.FalseNegativeCases, result)
		}
		if result.Borderline {
			parameterReport.BorderlineCases = append(parameterReport.BorderlineCases, result)
		}
		if result.ShadowLexicalHit && result.ShadowSemanticHit {
			parameterReport.OverlapHits++
		} else if result.ShadowLexicalHit && !result.ShadowSemanticHit {
			parameterReport.LexicalOnlyWins++
		} else if result.ShadowSemanticHit && !result.ShadowLexicalHit {
			parameterReport.SemanticOnlyWins++
		} else {
			parameterReport.AllMissCount++
		}
	}
	parameterReport.ExactHitRate = ratio(parameterReport.ExactHitCount, parameterReport.TotalCases)
	parameterReport.NormalizedHitRate = ratio(parameterReport.NormalizedHitCount, parameterReport.TotalCases)
	parameterReport.AliasHitRate = ratio(parameterReport.AliasHitCount, parameterReport.TotalCases)
	parameterReport.CandidateHitRate = ratio(parameterReport.CandidateHitCount, parameterReport.TotalCases)
	parameterReport.SemanticHitRate = ratio(parameterReport.SemanticHitCount, parameterReport.TotalCases)
	parameterReport.FallbackToLLMRatio = ratio(parameterReport.FallbackToLLMCount, parameterReport.TotalCases)
	parameterReport.WrongHitRatio = ratio(parameterReport.WrongHitCount, parameterReport.TotalCases)
	parameterReport.AvgLatencyMs = durationAvgMillis(actualLatencies)
	parameterReport.P95LatencyMs = durationP95Millis(actualLatencies)
	parameterReport.LexicalAvgLatencyMs = durationAvgMillis(lexicalLatencies)
	parameterReport.LexicalP95LatencyMs = durationP95Millis(lexicalLatencies)
	parameterReport.CandidateAvgLatencyMs = durationAvgMillis(candidateLatencies)
	parameterReport.CandidateP95LatencyMs = durationP95Millis(candidateLatencies)
	parameterReport.SemanticAvgLatencyMs = durationAvgMillis(semanticLatencies)
	parameterReport.SemanticP95LatencyMs = durationP95Millis(semanticLatencies)
	parameterReport.SemanticComparison = buildRetrievalEvalSemanticComparison(results, parameterReport.SemanticMode, parameterReport.ComparisonSemanticMode)
	parameterReport.ANNRecommendation, parameterReport.ANNRecommendationReason = decideRetrievalEvalRecommendation(embeddingSource.Mode, parameterReport)
	return parameterReport, nil
}

func buildRetrievalEvalStore(backend kvliteBackend, namespace string, ttl time.Duration, semanticCfg SemanticConfig, embeddingSource RetrievalEvalEmbeddingSource) (*KVLiteStore, error) {
	if backend == nil {
		return nil, errors.New("retrieval eval backend is nil")
	}
	if embeddingSource.Provider == nil {
		return nil, errors.New("retrieval eval embedding source is missing a provider")
	}
	storeIface := NewKVLiteStoreWithBackend(backend, namespace, ttl,
		WithKVLiteSemanticConfig(semanticCfg),
		WithKVLiteSemanticProvider(embeddingSource.Provider),
	)
	store, ok := storeIface.(*KVLiteStore)
	if !ok {
		return nil, fmt.Errorf("unexpected kvlite store type %T", storeIface)
	}
	return store, nil
}

func decideRetrievalEvalRecommendation(mode RetrievalEvalEmbeddingMode, report RetrievalEvalParameterReport) (string, string) {
	normalizedMode := strings.ToLower(strings.TrimSpace(string(mode)))
	if normalizedMode != string(RetrievalEvalEmbeddingModeProvider) {
		return "continue_semantic_eval", "placeholder_mode_is_not_actionable_for_ann"
	}
	if report.TotalCases == 0 {
		return "continue_semantic_eval", "empty_evaluation_suite"
	}
	if report.SemanticOnlyWins == 0 {
		return "stay_with_lexical", "semantic_only_wins=0"
	}
	if report.WrongHitRatio > 0.10 {
		return "continue_semantic_eval", fmt.Sprintf("wrong_hit_ratio=%.2f%%", report.WrongHitRatio*100)
	}
	if report.SemanticOnlyWins >= 2 && report.SemanticAvgLatencyMs <= report.CandidateAvgLatencyMs*2 {
		return "ann_prototype_candidate", fmt.Sprintf("semantic_only_wins=%d wrong_hit_ratio=%.2f%% semantic_avg_latency_ms=%.3f", report.SemanticOnlyWins, report.WrongHitRatio*100, report.SemanticAvgLatencyMs)
	}
	return "continue_semantic_eval", fmt.Sprintf("semantic_only_wins=%d insufficient_for_ann", report.SemanticOnlyWins)
}

func combineRetrievalEvalRecommendations(results []RetrievalEvalParameterReport) (string, string) {
	if len(results) == 0 {
		return "continue_semantic_eval", "no_parameter_sets"
	}
	bestRecommendation := "stay_with_lexical"
	bestReason := ""
	bestRank := recommendationRank(bestRecommendation)
	for _, result := range results {
		recommendation := strings.TrimSpace(result.ANNRecommendation)
		if recommendation == "" {
			recommendation = "continue_semantic_eval"
		}
		rank := recommendationRank(recommendation)
		if rank < bestRank {
			continue
		}
		if rank > bestRank || bestReason == "" {
			bestRank = rank
			bestRecommendation = recommendation
			bestReason = fmt.Sprintf("parameter_set=%s reason=%s", result.ParameterSet.Name, strings.TrimSpace(result.ANNRecommendationReason))
		}
	}
	if bestReason == "" {
		bestReason = "no_recommendation_reason"
	}
	return bestRecommendation, bestReason
}

func recommendationRank(value string) int {
	switch strings.TrimSpace(value) {
	case "stay_with_lexical":
		return 0
	case "continue_semantic_eval":
		return 1
	case "ann_prototype_candidate":
		return 2
	default:
		return -1
	}
}

type retrievalEvalSemanticSnapshot struct {
	Hit       bool
	Layer     string
	Answer    string
	AnswerID  string
	TopScore  float64
	LatencyMs float64
}

func selectRetrievalEvalBestParameterReport(results []RetrievalEvalParameterReport) *RetrievalEvalParameterReport {
	if len(results) == 0 {
		return nil
	}
	bestIndex := -1
	bestRank := recommendationRank("stay_with_lexical")
	for index := range results {
		recommendation := strings.TrimSpace(results[index].ANNRecommendation)
		if recommendation == "" {
			recommendation = "continue_semantic_eval"
		}
		rank := recommendationRank(recommendation)
		if rank < bestRank {
			continue
		}
		if rank > bestRank || bestIndex < 0 {
			bestRank = rank
			bestIndex = index
		}
	}
	if bestIndex < 0 {
		return nil
	}
	return &results[bestIndex]
}

func buildRetrievalEvalSemanticComparison(results []RetrievalEvalCaseResult, selectedMode, comparisonMode string) RetrievalEvalSemanticComparison {
	selectedMode = normalizeSemanticMode(selectedMode)
	comparisonMode = normalizeSemanticMode(comparisonMode)
	comparison := RetrievalEvalSemanticComparison{
		SelectedMode:   selectedMode,
		ComparisonMode: comparisonMode,
		DiffCases:      make([]RetrievalEvalSemanticDiffCase, 0, len(results)),
	}
	if len(results) == 0 {
		return comparison
	}
	annLatencies := make([]float64, 0, len(results))
	bruteForceLatencies := make([]float64, 0, len(results))
	annWrongCount := 0
	bruteForceWrongCount := 0
	for _, result := range results {
		annSnapshot := semanticSnapshotForMode(result, selectedMode, SemanticRecallModeANN)
		bruteForceSnapshot := semanticSnapshotForMode(result, selectedMode, SemanticRecallModeBruteForce)
		annCorrect := semanticSnapshotMatchesExpectation(result, annSnapshot)
		bruteForceCorrect := semanticSnapshotMatchesExpectation(result, bruteForceSnapshot)

		if annSnapshot.Hit {
			comparison.ANNHitCount++
			annLatencies = append(annLatencies, annSnapshot.LatencyMs)
		}
		if bruteForceSnapshot.Hit {
			comparison.BruteForceHitCount++
			bruteForceLatencies = append(bruteForceLatencies, bruteForceSnapshot.LatencyMs)
		}
		if !annCorrect {
			annWrongCount++
		}
		if !bruteForceCorrect {
			bruteForceWrongCount++
		}
		if annCorrect && !bruteForceCorrect {
			comparison.ANNOnlyWins++
		}
		if bruteForceCorrect && !annCorrect {
			comparison.BruteForceOnlyWins++
		}
		if semanticSnapshotsSameOutcome(annSnapshot, bruteForceSnapshot) {
			comparison.ANNVsBruteForceOverlap++
			continue
		}
		winner := semanticComparisonWinner(annCorrect, bruteForceCorrect, annSnapshot, bruteForceSnapshot)
		mismatchReason := semanticComparisonReason(annSnapshot, bruteForceSnapshot, winner, annCorrect, bruteForceCorrect)
		comparison.DiffCases = append(comparison.DiffCases, RetrievalEvalSemanticDiffCase{
			CaseID:              result.CaseID,
			Query:               result.Query,
			ExpectedBehavior:    result.ExpectedBehavior,
			ExpectedLayer:       result.ExpectedLayer,
			ExpectedAnswerID:    result.ExpectedAnswerID,
			ANNHit:              annSnapshot.Hit,
			ANNLayer:            annSnapshot.Layer,
			ANNAnswerID:         annSnapshot.AnswerID,
			ANNAnswer:           annSnapshot.Answer,
			ANNTopScore:         annSnapshot.TopScore,
			ANNLatencyMs:        annSnapshot.LatencyMs,
			BruteForceHit:       bruteForceSnapshot.Hit,
			BruteForceLayer:     bruteForceSnapshot.Layer,
			BruteForceAnswerID:  bruteForceSnapshot.AnswerID,
			BruteForceAnswer:    bruteForceSnapshot.Answer,
			BruteForceTopScore:  bruteForceSnapshot.TopScore,
			BruteForceLatencyMs: bruteForceSnapshot.LatencyMs,
			Winner:              winner,
			MismatchReason:      mismatchReason,
		})
	}
	total := len(results)
	comparison.ANNHitRate = ratio(comparison.ANNHitCount, total)
	comparison.BruteForceHitRate = ratio(comparison.BruteForceHitCount, total)
	comparison.ANNAvgLatencyMs = float64Avg(annLatencies)
	comparison.ANNP95LatencyMs = float64P95(annLatencies)
	comparison.BruteForceAvgLatencyMs = float64Avg(bruteForceLatencies)
	comparison.BruteForceP95LatencyMs = float64P95(bruteForceLatencies)
	comparison.ANNWrongHitRatio = ratio(annWrongCount, total)
	comparison.BruteForceWrongHitRatio = ratio(bruteForceWrongCount, total)
	return comparison
}

func semanticSnapshotForMode(result RetrievalEvalCaseResult, selectedMode string, mode string) retrievalEvalSemanticSnapshot {
	if normalizeSemanticMode(selectedMode) == normalizeSemanticMode(mode) {
		return retrievalEvalSemanticSnapshot{
			Hit:       result.ShadowSemanticHit,
			Layer:     result.ShadowSemanticLayer,
			Answer:    strings.TrimSpace(result.ShadowSemanticAnswer),
			AnswerID:  strings.TrimSpace(result.ShadowSemanticAnswerID),
			TopScore:  result.ShadowSemanticTopScore,
			LatencyMs: result.SemanticLatencyMs,
		}
	}
	return retrievalEvalSemanticSnapshot{
		Hit:       result.ComparisonSemanticHit,
		Layer:     result.ComparisonSemanticLayer,
		Answer:    strings.TrimSpace(result.ComparisonSemanticAnswer),
		AnswerID:  strings.TrimSpace(result.ComparisonSemanticAnswerID),
		TopScore:  result.ComparisonSemanticTopScore,
		LatencyMs: result.ComparisonSemanticLatencyMs,
	}
}

func semanticSnapshotMatchesExpectation(result RetrievalEvalCaseResult, snapshot retrievalEvalSemanticSnapshot) bool {
	if strings.EqualFold(strings.TrimSpace(result.ExpectedBehavior), "miss") {
		return !snapshot.Hit
	}
	if !snapshot.Hit {
		return false
	}
	if expectedID := strings.TrimSpace(result.ExpectedAnswerID); expectedID != "" && strings.TrimSpace(snapshot.AnswerID) != "" {
		return strings.EqualFold(expectedID, snapshot.AnswerID)
	}
	if expectedAnswer := strings.TrimSpace(result.ExpectedAnswer); expectedAnswer != "" && strings.TrimSpace(snapshot.Answer) != "" {
		return strings.EqualFold(expectedAnswer, snapshot.Answer)
	}
	return true
}

func semanticSnapshotsSameOutcome(left, right retrievalEvalSemanticSnapshot) bool {
	if left.Hit != right.Hit {
		return false
	}
	if !left.Hit {
		return true
	}
	if left.AnswerID != "" && right.AnswerID != "" {
		return strings.EqualFold(left.AnswerID, right.AnswerID)
	}
	if left.Answer != "" || right.Answer != "" {
		return strings.EqualFold(left.Answer, right.Answer)
	}
	return true
}

func semanticComparisonWinner(annCorrect, bruteForceCorrect bool, annSnapshot, bruteForceSnapshot retrievalEvalSemanticSnapshot) string {
	switch {
	case annCorrect && !bruteForceCorrect:
		return SemanticRecallModeANN
	case bruteForceCorrect && !annCorrect:
		return SemanticRecallModeBruteForce
	case semanticSnapshotsSameOutcome(annSnapshot, bruteForceSnapshot):
		return "overlap"
	default:
		return "mixed"
	}
}

func semanticComparisonReason(annSnapshot, bruteForceSnapshot retrievalEvalSemanticSnapshot, winner string, annCorrect, bruteForceCorrect bool) string {
	return fmt.Sprintf("winner=%s ann(hit=%t answer_id=%s score=%.4f latency_ms=%.3f correct=%t) brute_force(hit=%t answer_id=%s score=%.4f latency_ms=%.3f correct=%t)",
		winner,
		annSnapshot.Hit,
		firstNonEmpty(annSnapshot.AnswerID, "n/a"),
		annSnapshot.TopScore,
		annSnapshot.LatencyMs,
		annCorrect,
		bruteForceSnapshot.Hit,
		firstNonEmpty(bruteForceSnapshot.AnswerID, "n/a"),
		bruteForceSnapshot.TopScore,
		bruteForceSnapshot.LatencyMs,
		bruteForceCorrect,
	)
}

func float64Avg(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	var total float64
	for _, value := range values {
		total += value
	}
	return total / float64(len(values))
}

func float64P95(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	index := int(math.Ceil(float64(len(sorted))*0.95)) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return sorted[index]
}

func evaluateRetrievalEvalCase(ctx context.Context, lexicalStore, semanticStore *KVLiteStore, seedRuns []retrievalEvalSeedRuntime, evalCase RetrievalEvalCase, paramSet RetrievalEvalParameterSet, semanticMode string) (RetrievalEvalCaseResult, time.Duration, time.Duration, time.Duration, time.Duration, time.Duration, error) {
	caseScope := resolveRetrievalEvalScope(evalCase.Scope, RetrievalEvalScope{})
	if caseScope.Provider == "" {
		caseScope = resolveRetrievalEvalScope(evalCase.Scope, RetrievalEvalScope{Provider: "deepseek", Model: "deepseek-chat", PromptVersion: "v1"})
	}
	scopeText := BuildScope(caseScope.Provider, caseScope.Model, caseScope.PromptVersion)
	query := strings.TrimSpace(evalCase.Query)
	selectedSemanticMode := normalizeSemanticMode(semanticMode)
	comparisonSemanticMode := oppositeSemanticMode(selectedSemanticMode)
	lookupReq := LookupRequest{
		Question:               query,
		ExactQuestionHash:      ExactQuestionHash(query),
		NormalizedQuestion:     NormalizeQuestion(query),
		NormalizedQuestionHash: QuestionHash(query),
		Provider:               caseScope.Provider,
		Model:                  caseScope.Model,
		PromptVersion:          caseScope.PromptVersion,
		Scope:                  scopeText,
		CandidateTopK:          normalizePositiveInt(paramSet.CandidateTopK, DefaultCandidateTopK),
		CandidateMinScore:      normalizePositiveFloat(paramSet.CandidateScoreThreshold, DefaultCandidateMinScore),
		SemanticEnabled:        true,
		SemanticMode:           selectedSemanticMode,
		SemanticTopK:           normalizePositiveInt(paramSet.SemanticTopK, DefaultSemanticTopK),
		SemanticThreshold:      normalizePositiveFloat(paramSet.SemanticScoreThreshold, DefaultSemanticMinScore),
		SemanticCorpusLimit:    normalizePositiveInt(paramSet.SemanticCorpusLimit, DefaultSemanticCorpusLimit),
	}
	lexicalReq := lookupReq
	lexicalReq.SemanticEnabled = false
	lexicalReq.SemanticMode = ""
	comparisonSemanticReq := lookupReq
	comparisonSemanticReq.SemanticMode = comparisonSemanticMode

	fullStart := time.Now()
	actualResult, actualErr := semanticStore.Lookup(ctx, lookupReq)
	actualLatency := time.Since(fullStart)
	if actualErr != nil {
		return RetrievalEvalCaseResult{}, 0, 0, 0, 0, 0, actualErr
	}

	lexicalStart := time.Now()
	lexicalResult, lexicalErr := lexicalStore.Lookup(ctx, lexicalReq)
	lexicalLatency := time.Since(lexicalStart)
	if lexicalErr != nil {
		return RetrievalEvalCaseResult{}, 0, 0, 0, 0, 0, lexicalErr
	}

	candidateStart := time.Now()
	_, _, candidateErr := semanticStore.lookupByCandidate(ctx, lookupReq)
	candidateLatency := time.Since(candidateStart)
	if candidateErr != nil {
		return RetrievalEvalCaseResult{}, 0, 0, 0, 0, 0, candidateErr
	}

	semanticStart := time.Now()
	semanticResult, _, semanticErr := semanticStore.lookupBySemantic(ctx, lookupReq)
	semanticLatency := time.Since(semanticStart)
	if semanticErr != nil {
		return RetrievalEvalCaseResult{}, 0, 0, 0, 0, 0, semanticErr
	}

	comparisonSemanticStart := time.Now()
	comparisonSemanticResult, _, comparisonSemanticErr := semanticStore.lookupBySemantic(ctx, comparisonSemanticReq)
	comparisonSemanticLatency := time.Since(comparisonSemanticStart)
	if comparisonSemanticErr != nil {
		return RetrievalEvalCaseResult{}, 0, 0, 0, 0, 0, comparisonSemanticErr
	}

	expectedAnswer, expectedAnswerID := expectedRetrievalEvalAnswer(seedRuns, evalCase.ExpectedAnswerID)
	actualAnswerID := firstNonEmpty(actualResult.CanonicalQuestionID, actualResult.QuestionID)
	actualAnswer := strings.TrimSpace(actualResult.Answer)
	shadowLexicalAnswerID := firstNonEmpty(lexicalResult.CanonicalQuestionID, lexicalResult.QuestionID)
	shadowSemanticAnswerID := firstNonEmpty(semanticResult.CanonicalQuestionID, semanticResult.QuestionID)
	comparisonSemanticAnswerID := firstNonEmpty(comparisonSemanticResult.CanonicalQuestionID, comparisonSemanticResult.QuestionID)
	actualLayer := actualResult.HitType
	if !actualResult.Hit {
		actualLayer = "miss"
	}
	status := "correct"
	mismatchReason := ""
	borderline := isBorderlineRetrievalEvalCase(actualResult, paramSet)
	if strings.EqualFold(strings.TrimSpace(evalCase.ExpectedBehavior), "miss") {
		if actualResult.Hit {
			status = "false_positive"
			mismatchReason = fmt.Sprintf("expected miss but actual layer %s hit", actualLayer)
		}
	} else {
		if !actualResult.Hit {
			status = "false_negative"
			mismatchReason = fmt.Sprintf("expected %s but actual miss", evalCase.ExpectedLayer)
		} else if !strings.EqualFold(actualLayer, strings.TrimSpace(evalCase.ExpectedLayer)) {
			status = "false_negative"
			mismatchReason = fmt.Sprintf("expected layer %s but actual layer %s", strings.TrimSpace(evalCase.ExpectedLayer), actualLayer)
		} else if expectedAnswer != "" && !strings.EqualFold(actualAnswer, expectedAnswer) {
			status = "false_negative"
			mismatchReason = fmt.Sprintf("expected answer %q but actual answer %q", expectedAnswer, actualAnswer)
		}
	}
	if expectedAnswer == "" && !strings.EqualFold(strings.TrimSpace(evalCase.ExpectedBehavior), "miss") {
		expectedAnswer = actualAnswer
	}
	if status == "correct" && strings.EqualFold(strings.TrimSpace(evalCase.ExpectedBehavior), "hit") && expectedAnswerID != "" && actualAnswerID != "" && !strings.EqualFold(expectedAnswerID, actualAnswerID) {
		status = "false_negative"
		mismatchReason = fmt.Sprintf("expected answer id %q but actual answer id %q", expectedAnswerID, actualAnswerID)
	}
	return RetrievalEvalCaseResult{
		CaseID:                           evalCase.ID,
		Query:                            query,
		ExpectedBehavior:                 strings.TrimSpace(evalCase.ExpectedBehavior),
		ExpectedLayer:                    strings.TrimSpace(evalCase.ExpectedLayer),
		ExpectedAnswerID:                 expectedAnswerID,
		ExpectedAnswer:                   expectedAnswer,
		Scope:                            scopeText,
		ActualLayer:                      actualLayer,
		ActualHit:                        actualResult.Hit,
		ActualAnswerID:                   actualAnswerID,
		ActualAnswer:                     actualAnswer,
		ActualCandidateCount:             actualResult.CandidateCount,
		ActualCandidateTopScore:          actualResult.CandidateTopScore,
		ActualSemanticCandidateCount:     actualResult.SemanticCandidateCount,
		ActualSemanticTopK:               actualResult.SemanticTopK,
		ActualSemanticTopScore:           actualResult.SemanticTopScore,
		ActualSemanticModel:              actualResult.SemanticModel,
		ActualSemanticSource:             actualResult.SemanticSource,
		ComparisonSemanticHit:            comparisonSemanticResult.Hit,
		ComparisonSemanticLayer:          comparisonSemanticResult.HitType,
		ComparisonSemanticAnswer:         strings.TrimSpace(comparisonSemanticResult.Answer),
		ComparisonSemanticAnswerID:       comparisonSemanticAnswerID,
		FullLatencyMs:                    float64(actualLatency) / float64(time.Millisecond),
		LexicalLatencyMs:                 float64(lexicalLatency) / float64(time.Millisecond),
		CandidateLatencyMs:               float64(candidateLatency) / float64(time.Millisecond),
		SemanticLatencyMs:                float64(semanticLatency) / float64(time.Millisecond),
		ShadowSemanticTopScore:           semanticResult.SemanticTopScore,
		ComparisonSemanticCandidateCount: comparisonSemanticResult.SemanticCandidateCount,
		ComparisonSemanticTopK:           comparisonSemanticResult.SemanticTopK,
		ComparisonSemanticTopScore:       comparisonSemanticResult.SemanticTopScore,
		ComparisonSemanticModel:          comparisonSemanticResult.SemanticModel,
		ComparisonSemanticSource:         comparisonSemanticResult.SemanticSource,
		ComparisonSemanticLatencyMs:      float64(comparisonSemanticLatency) / float64(time.Millisecond),
		ShadowLexicalHit:                 lexicalResult.Hit,
		ShadowLexicalLayer:               lexicalResult.HitType,
		ShadowLexicalAnswer:              strings.TrimSpace(lexicalResult.Answer),
		ShadowLexicalAnswerID:            shadowLexicalAnswerID,
		ShadowSemanticHit:                semanticResult.Hit,
		ShadowSemanticLayer:              semanticResult.HitType,
		ShadowSemanticAnswer:             strings.TrimSpace(semanticResult.Answer),
		ShadowSemanticAnswerID:           shadowSemanticAnswerID,
		Status:                           status,
		MismatchReason:                   mismatchReason,
		Borderline:                       borderline,
	}, actualLatency, lexicalLatency, candidateLatency, semanticLatency, comparisonSemanticLatency, nil
}

func seedRetrievalEvalStore(ctx context.Context, store *KVLiteStore, seeds []retrievalEvalSeedRuntime) error {
	if store == nil {
		return errors.New("retrieval eval store is nil")
	}
	backfiller, ok := interface{}(store).(SemanticBackfiller)
	if !ok {
		return errors.New("retrieval eval store does not implement semantic backfiller")
	}
	for _, seed := range seeds {
		if err := store.Upsert(ctx, seed.Record, retrievalEvalDefaultTTL); err != nil {
			return err
		}
		if err := backfiller.BackfillSemanticEmbedding(ctx, seed.Record); err != nil {
			return err
		}
		for _, aliasQuestion := range seed.Seed.Aliases {
			if err := store.AddAlias(ctx, seed.QuestionID, aliasQuestion, seed.ScopeText, retrievalEvalDefaultTTL); err != nil {
				return err
			}
		}
	}
	return nil
}

func expectedRetrievalEvalAnswer(seeds []retrievalEvalSeedRuntime, expectedAnswerID string) (string, string) {
	trimmed := strings.TrimSpace(expectedAnswerID)
	if trimmed == "" {
		return "", ""
	}
	for _, seed := range seeds {
		if seed.Seed.ID == trimmed {
			return seed.Record.Answer, seed.QuestionID
		}
	}
	return "", ""
}

func validateOptionalScope(scope *RetrievalEvalScope) error {
	if scope == nil {
		return nil
	}
	if strings.TrimSpace(scope.Provider) == "" || strings.TrimSpace(scope.Model) == "" || strings.TrimSpace(scope.PromptVersion) == "" {
		return errors.New("scope must include provider, model, and prompt_version")
	}
	return nil
}

func validateExpectedBehavior(value string) error {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "hit", "miss":
		return nil
	default:
		return fmt.Errorf("unsupported expected_behavior %q", value)
	}
}

func validateExpectedLayer(value string) error {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case HitTypeExact, HitTypeNormalized, HitTypeAlias, HitTypeCandidate, HitTypeSemantic, "miss":
		return nil
	default:
		return fmt.Errorf("unsupported expected_layer %q", value)
	}
}

func resolveRetrievalEvalScope(scope *RetrievalEvalScope, fallback RetrievalEvalScope) RetrievalEvalScope {
	result := fallback
	if result.Provider == "" {
		result.Provider = "deepseek"
	}
	if result.Model == "" {
		result.Model = "deepseek-chat"
	}
	if result.PromptVersion == "" {
		result.PromptVersion = "v1"
	}
	if scope != nil {
		if trimmed := strings.TrimSpace(scope.Provider); trimmed != "" {
			result.Provider = trimmed
		}
		if trimmed := strings.TrimSpace(scope.Model); trimmed != "" {
			result.Model = trimmed
		}
		if trimmed := strings.TrimSpace(scope.PromptVersion); trimmed != "" {
			result.PromptVersion = trimmed
		}
	}
	return result
}

func (s RetrievalEvalScope) String() string {
	return BuildScope(s.Provider, s.Model, s.PromptVersion)
}

func normalizePositiveInt(value, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

func normalizePositiveFloat(value, fallback float64) float64 {
	if value > 0 {
		return value
	}
	return fallback
}

func ratio(numerator, denominator int) float64 {
	if denominator <= 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}

func durationAvgMillis(values []time.Duration) float64 {
	if len(values) == 0 {
		return 0
	}
	var total time.Duration
	for _, value := range values {
		total += value
	}
	return float64(total) / float64(time.Millisecond) / float64(len(values))
}

func durationP95Millis(values []time.Duration) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	index := int(math.Ceil(float64(len(sorted))*0.95)) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return float64(sorted[index]) / float64(time.Millisecond)
}

func isBorderlineRetrievalEvalCase(result LookupResult, paramSet RetrievalEvalParameterSet) bool {
	margin := 0.05
	switch result.HitType {
	case HitTypeCandidate:
		threshold := normalizePositiveFloat(paramSet.CandidateScoreThreshold, DefaultCandidateMinScore)
		return result.CandidateTopScore > 0 && result.CandidateTopScore <= threshold+margin
	case HitTypeSemantic:
		threshold := normalizePositiveFloat(paramSet.SemanticScoreThreshold, DefaultSemanticMinScore)
		return result.SemanticTopScore > 0 && result.SemanticTopScore <= threshold+margin
	default:
		return false
	}
}

func (r *RetrievalEvalReport) WriteJSON(filePath string) error {
	if r == nil {
		return errors.New("retrieval eval report is nil")
	}
	if strings.TrimSpace(filePath) == "" {
		return errors.New("retrieval eval report json path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		return err
	}
	bytes, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filePath, bytes, 0o644)
}

func (r *RetrievalEvalReport) WriteMarkdown(filePath string) error {
	if r == nil {
		return errors.New("retrieval eval report is nil")
	}
	if strings.TrimSpace(filePath) == "" {
		return errors.New("retrieval eval report markdown path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(filePath, []byte(r.RenderMarkdown()), 0o644)
}

func (r *RetrievalEvalReport) RenderText() string {
	if r == nil {
		return "retrieval eval report is nil"
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "[retrieval-eval] suite=%s version=%s generated_at=%s\n", r.SuiteName, r.SuiteVersion, r.GeneratedAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(&builder, "[retrieval-eval] embedding_mode=%s embedding_profile=%s embedding_provider=%s embedding_model=%s embedding_source=%s ann_recommendation=%s reason=%s\n",
		embeddingReportValue(r.EmbeddingMode),
		embeddingReportValue(r.EmbeddingProfile),
		embeddingReportValue(r.EmbeddingProvider),
		embeddingReportValue(r.EmbeddingModel),
		embeddingReportValue(r.EmbeddingSource),
		embeddingReportValue(r.ANNRecommendation),
		embeddingReportValue(r.ANNRecommendationReason),
	)
	fmt.Fprintf(&builder, "[retrieval-eval] semantic_mode=%s comparison_semantic_mode=%s ann_hit_rate=%.2f%% brute_force_hit_rate=%.2f%% ann_only_wins=%d brute_force_only_wins=%d overlap=%d ann_avg_latency_ms=%.3f brute_force_avg_latency_ms=%.3f ann_wrong_hit_ratio=%.2f%% brute_force_wrong_hit_ratio=%.2f%%\n",
		embeddingReportValue(r.SemanticMode),
		embeddingReportValue(r.ComparisonSemanticMode),
		r.SemanticComparison.ANNHitRate*100,
		r.SemanticComparison.BruteForceHitRate*100,
		r.SemanticComparison.ANNOnlyWins,
		r.SemanticComparison.BruteForceOnlyWins,
		r.SemanticComparison.ANNVsBruteForceOverlap,
		r.SemanticComparison.ANNAvgLatencyMs,
		r.SemanticComparison.BruteForceAvgLatencyMs,
		r.SemanticComparison.ANNWrongHitRatio*100,
		r.SemanticComparison.BruteForceWrongHitRatio*100,
	)
	for _, result := range r.Results {
		fmt.Fprintf(&builder, "[retrieval-eval] parameter_set=%s total_cases=%d exact=%.2f%% normalized=%.2f%% alias=%.2f%% candidate=%.2f%% semantic=%.2f%% fallback=%.2f%% wrong_hit=%.2f%% lexical_only=%d semantic_only=%d overlap=%d all_miss=%d avg=%.3fms p95=%.3fms lexical_avg=%.3fms candidate_avg=%.3fms semantic_avg=%.3fms ann=%s reason=%s\n",
			result.ParameterSet.Name,
			result.TotalCases,
			result.ExactHitRate*100,
			result.NormalizedHitRate*100,
			result.AliasHitRate*100,
			result.CandidateHitRate*100,
			result.SemanticHitRate*100,
			result.FallbackToLLMRatio*100,
			result.WrongHitRatio*100,
			result.LexicalOnlyWins,
			result.SemanticOnlyWins,
			result.OverlapHits,
			result.AllMissCount,
			result.AvgLatencyMs,
			result.P95LatencyMs,
			result.LexicalAvgLatencyMs,
			result.CandidateAvgLatencyMs,
			result.SemanticAvgLatencyMs,
			result.ANNRecommendation,
			result.ANNRecommendationReason,
		)
		fmt.Fprintf(&builder, "[retrieval-eval] semantic_comparison selected=%s comparison=%s ann_hit_rate=%.2f%% brute_force_hit_rate=%.2f%% ann_only_wins=%d brute_force_only_wins=%d overlap=%d ann_avg_latency_ms=%.3f brute_force_avg_latency_ms=%.3f ann_wrong_hit_ratio=%.2f%% brute_force_wrong_hit_ratio=%.2f%% diff_cases=%d\n",
			embeddingReportValue(result.SemanticMode),
			embeddingReportValue(result.ComparisonSemanticMode),
			result.SemanticComparison.ANNHitRate*100,
			result.SemanticComparison.BruteForceHitRate*100,
			result.SemanticComparison.ANNOnlyWins,
			result.SemanticComparison.BruteForceOnlyWins,
			result.SemanticComparison.ANNVsBruteForceOverlap,
			result.SemanticComparison.ANNAvgLatencyMs,
			result.SemanticComparison.BruteForceAvgLatencyMs,
			result.SemanticComparison.ANNWrongHitRatio*100,
			result.SemanticComparison.BruteForceWrongHitRatio*100,
			len(result.SemanticComparison.DiffCases),
		)
		if len(result.WrongHitCases) > 0 {
			builder.WriteString("[retrieval-eval] wrong_hits:\n")
			for _, wrongHit := range result.WrongHitCases {
				fmt.Fprintf(&builder, "  - case=%s actual_layer=%s answer=%q answer_id=%s candidate_score=%.4f semantic_score=%.4f reason=%s\n",
					wrongHit.CaseID,
					wrongHit.ActualLayer,
					wrongHit.ActualAnswer,
					wrongHit.ActualAnswerID,
					wrongHit.ActualCandidateTopScore,
					wrongHit.ActualSemanticTopScore,
					wrongHit.MismatchReason,
				)
			}
		}
		if len(result.FalseNegativeCases) > 0 {
			builder.WriteString("[retrieval-eval] false_negatives:\n")
			for _, falseNegative := range result.FalseNegativeCases {
				fmt.Fprintf(&builder, "  - case=%s expected_layer=%s actual_layer=%s reason=%s\n",
					falseNegative.CaseID,
					falseNegative.ExpectedLayer,
					falseNegative.ActualLayer,
					falseNegative.MismatchReason,
				)
			}
		}
		if len(result.SemanticComparison.DiffCases) > 0 {
			builder.WriteString("[retrieval-eval] semantic_diff_cases:\n")
			for _, diffCase := range result.SemanticComparison.DiffCases {
				fmt.Fprintf(&builder, "  - case=%s winner=%s ann_hit=%t ann_answer_id=%s brute_force_hit=%t brute_force_answer_id=%s reason=%s\n",
					diffCase.CaseID,
					diffCase.Winner,
					diffCase.ANNHit,
					firstNonEmpty(diffCase.ANNAnswerID, "n/a"),
					diffCase.BruteForceHit,
					firstNonEmpty(diffCase.BruteForceAnswerID, "n/a"),
					diffCase.MismatchReason,
				)
			}
		}
	}
	return builder.String()
}

func (r *RetrievalEvalReport) RenderMarkdown() string {
	if r == nil {
		return "retrieval eval report is nil"
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "# Retrieval Quality Report\n\n")
	fmt.Fprintf(&builder, "- Suite: %s\n", r.SuiteName)
	fmt.Fprintf(&builder, "- Version: %s\n", r.SuiteVersion)
	if strings.TrimSpace(r.Description) != "" {
		fmt.Fprintf(&builder, "- Description: %s\n", r.Description)
	}
	fmt.Fprintf(&builder, "- Generated At: %s\n", r.GeneratedAt.UTC().Format(time.RFC3339))
	if strings.TrimSpace(r.DatasetPath) != "" {
		fmt.Fprintf(&builder, "- Dataset: %s\n", r.DatasetPath)
	}
	fmt.Fprintf(&builder, "- Embedding Mode: %s\n", embeddingReportValue(r.EmbeddingMode))
	if strings.TrimSpace(r.EmbeddingProfile) != "" {
		fmt.Fprintf(&builder, "- Embedding Profile: %s\n", r.EmbeddingProfile)
	}
	if strings.TrimSpace(r.EmbeddingProvider) != "" {
		fmt.Fprintf(&builder, "- Embedding Provider: %s\n", r.EmbeddingProvider)
	}
	if strings.TrimSpace(r.EmbeddingModel) != "" {
		fmt.Fprintf(&builder, "- Embedding Model: %s\n", r.EmbeddingModel)
	}
	if strings.TrimSpace(r.EmbeddingSource) != "" {
		fmt.Fprintf(&builder, "- Embedding Source: %s\n", r.EmbeddingSource)
	}
	if strings.TrimSpace(r.SemanticMode) != "" || strings.TrimSpace(r.ComparisonSemanticMode) != "" {
		fmt.Fprintf(&builder, "- Semantic Mode: %s\n", embeddingReportValue(r.SemanticMode))
		fmt.Fprintf(&builder, "- Comparison Semantic Mode: %s\n", embeddingReportValue(r.ComparisonSemanticMode))
	}
	if r.SemanticComparison.ANNHitRate > 0 || r.SemanticComparison.BruteForceHitRate > 0 || len(r.SemanticComparison.DiffCases) > 0 {
		fmt.Fprintf(&builder, "- ANN Hit Rate: %.2f%%\n", r.SemanticComparison.ANNHitRate*100)
		fmt.Fprintf(&builder, "- Brute Force Hit Rate: %.2f%%\n", r.SemanticComparison.BruteForceHitRate*100)
		fmt.Fprintf(&builder, "- ANN Only Wins: %d\n", r.SemanticComparison.ANNOnlyWins)
		fmt.Fprintf(&builder, "- Brute Force Only Wins: %d\n", r.SemanticComparison.BruteForceOnlyWins)
		fmt.Fprintf(&builder, "- ANN vs Brute Force Overlap: %d\n", r.SemanticComparison.ANNVsBruteForceOverlap)
		fmt.Fprintf(&builder, "- ANN Avg Latency Ms: %.3f\n", r.SemanticComparison.ANNAvgLatencyMs)
		fmt.Fprintf(&builder, "- Brute Force Avg Latency Ms: %.3f\n", r.SemanticComparison.BruteForceAvgLatencyMs)
		fmt.Fprintf(&builder, "- ANN Wrong Hit Ratio: %.2f%%\n", r.SemanticComparison.ANNWrongHitRatio*100)
		fmt.Fprintf(&builder, "- Brute Force Wrong Hit Ratio: %.2f%%\n", r.SemanticComparison.BruteForceWrongHitRatio*100)
	}
	if strings.TrimSpace(r.ANNRecommendation) != "" {
		fmt.Fprintf(&builder, "- ANN Recommendation: %s\n", r.ANNRecommendation)
	}
	if strings.TrimSpace(r.ANNRecommendationReason) != "" {
		fmt.Fprintf(&builder, "- ANN Recommendation Reason: %s\n", r.ANNRecommendationReason)
	}
	fmt.Fprintf(&builder, "\n## Parameter Sets\n\n")
	for _, result := range r.Results {
		fmt.Fprintf(&builder, "### %s\n\n", result.ParameterSet.Name)
		fmt.Fprintf(&builder, "- Embedding Mode: %s\n", embeddingReportValue(result.EmbeddingMode))
		if strings.TrimSpace(result.EmbeddingProfile) != "" {
			fmt.Fprintf(&builder, "- Embedding Profile: %s\n", result.EmbeddingProfile)
		}
		if strings.TrimSpace(result.EmbeddingModel) != "" {
			fmt.Fprintf(&builder, "- Embedding Model: %s\n", result.EmbeddingModel)
		}
		if strings.TrimSpace(result.SemanticMode) != "" || strings.TrimSpace(result.ComparisonSemanticMode) != "" {
			fmt.Fprintf(&builder, "- Semantic Mode: %s\n", embeddingReportValue(result.SemanticMode))
			fmt.Fprintf(&builder, "- Comparison Semantic Mode: %s\n", embeddingReportValue(result.ComparisonSemanticMode))
		}
		if strings.TrimSpace(result.ANNRecommendation) != "" {
			fmt.Fprintf(&builder, "- ANN Recommendation: %s\n", result.ANNRecommendation)
		}
		if strings.TrimSpace(result.ANNRecommendationReason) != "" {
			fmt.Fprintf(&builder, "- ANN Recommendation Reason: %s\n", result.ANNRecommendationReason)
		}
		if result.SemanticComparison.ANNHitRate > 0 || result.SemanticComparison.BruteForceHitRate > 0 || len(result.SemanticComparison.DiffCases) > 0 {
			builder.WriteString("\n")
			builder.WriteString("#### Semantic Comparison\n\n")
			builder.WriteString("| Metric | Value |\n")
			builder.WriteString("|---|---:|\n")
			fmt.Fprintf(&builder, "| semantic_mode | %s |\n", escapeMarkdownCell(result.SemanticMode))
			fmt.Fprintf(&builder, "| comparison_semantic_mode | %s |\n", escapeMarkdownCell(result.ComparisonSemanticMode))
			fmt.Fprintf(&builder, "| ann_hit_rate | %.2f%% |\n", result.SemanticComparison.ANNHitRate*100)
			fmt.Fprintf(&builder, "| brute_force_hit_rate | %.2f%% |\n", result.SemanticComparison.BruteForceHitRate*100)
			fmt.Fprintf(&builder, "| ann_only_wins | %d |\n", result.SemanticComparison.ANNOnlyWins)
			fmt.Fprintf(&builder, "| brute_force_only_wins | %d |\n", result.SemanticComparison.BruteForceOnlyWins)
			fmt.Fprintf(&builder, "| ann_vs_bruteforce_overlap | %d |\n", result.SemanticComparison.ANNVsBruteForceOverlap)
			fmt.Fprintf(&builder, "| ann_avg_latency_ms | %.3f |\n", result.SemanticComparison.ANNAvgLatencyMs)
			fmt.Fprintf(&builder, "| ann_p95_latency_ms | %.3f |\n", result.SemanticComparison.ANNP95LatencyMs)
			fmt.Fprintf(&builder, "| brute_force_avg_latency_ms | %.3f |\n", result.SemanticComparison.BruteForceAvgLatencyMs)
			fmt.Fprintf(&builder, "| brute_force_p95_latency_ms | %.3f |\n", result.SemanticComparison.BruteForceP95LatencyMs)
			fmt.Fprintf(&builder, "| ann_wrong_hit_ratio | %.2f%% |\n", result.SemanticComparison.ANNWrongHitRatio*100)
			fmt.Fprintf(&builder, "| brute_force_wrong_hit_ratio | %.2f%% |\n", result.SemanticComparison.BruteForceWrongHitRatio*100)
			if len(result.SemanticComparison.DiffCases) > 0 {
				builder.WriteString("\n")
				builder.WriteString("##### Diff Cases\n\n")
				builder.WriteString("| case | winner | ann_hit | ann_answer_id | brute_force_hit | brute_force_answer_id | reason |\n")
				builder.WriteString("|---|---|---|---|---|---|---|\n")
				for _, diffCase := range result.SemanticComparison.DiffCases {
					fmt.Fprintf(&builder, "| %s | %s | %t | %s | %t | %s | %s |\n",
						diffCase.CaseID,
						escapeMarkdownCell(diffCase.Winner),
						diffCase.ANNHit,
						escapeMarkdownCell(firstNonEmpty(diffCase.ANNAnswerID, "n/a")),
						diffCase.BruteForceHit,
						escapeMarkdownCell(firstNonEmpty(diffCase.BruteForceAnswerID, "n/a")),
						escapeMarkdownCell(diffCase.MismatchReason),
					)
				}
				builder.WriteString("\n")
			}
		}
		builder.WriteString("\n")
		builder.WriteString("| Metric | Value |\n")
		builder.WriteString("|---|---:|\n")
		fmt.Fprintf(&builder, "| total_cases | %d |\n", result.TotalCases)
		fmt.Fprintf(&builder, "| exact_hit_rate | %.2f%% |\n", result.ExactHitRate*100)
		fmt.Fprintf(&builder, "| normalized_hit_rate | %.2f%% |\n", result.NormalizedHitRate*100)
		fmt.Fprintf(&builder, "| alias_hit_rate | %.2f%% |\n", result.AliasHitRate*100)
		fmt.Fprintf(&builder, "| candidate_hit_rate | %.2f%% |\n", result.CandidateHitRate*100)
		fmt.Fprintf(&builder, "| semantic_hit_rate | %.2f%% |\n", result.SemanticHitRate*100)
		fmt.Fprintf(&builder, "| fallback_to_LLM_ratio | %.2f%% |\n", result.FallbackToLLMRatio*100)
		fmt.Fprintf(&builder, "| wrong_hit_ratio | %.2f%% |\n", result.WrongHitRatio*100)
		fmt.Fprintf(&builder, "| lexical_only_wins | %d |\n", result.LexicalOnlyWins)
		fmt.Fprintf(&builder, "| semantic_only_wins | %d |\n", result.SemanticOnlyWins)
		fmt.Fprintf(&builder, "| overlap_hits | %d |\n", result.OverlapHits)
		fmt.Fprintf(&builder, "| all_miss | %d |\n", result.AllMissCount)
		fmt.Fprintf(&builder, "| avg_latency_ms | %.3f |\n", result.AvgLatencyMs)
		fmt.Fprintf(&builder, "| p95_latency_ms | %.3f |\n", result.P95LatencyMs)
		fmt.Fprintf(&builder, "| lexical_avg_latency_ms | %.3f |\n", result.LexicalAvgLatencyMs)
		fmt.Fprintf(&builder, "| lexical_p95_latency_ms | %.3f |\n", result.LexicalP95LatencyMs)
		fmt.Fprintf(&builder, "| candidate_avg_latency_ms | %.3f |\n", result.CandidateAvgLatencyMs)
		fmt.Fprintf(&builder, "| candidate_p95_latency_ms | %.3f |\n", result.CandidateP95LatencyMs)
		fmt.Fprintf(&builder, "| semantic_avg_latency_ms | %.3f |\n", result.SemanticAvgLatencyMs)
		fmt.Fprintf(&builder, "| semantic_p95_latency_ms | %.3f |\n", result.SemanticP95LatencyMs)
		fmt.Fprintf(&builder, "| ann_recommendation | %s |\n", escapeMarkdownCell(result.ANNRecommendation))
		fmt.Fprintf(&builder, "| ann_recommendation_reason | %s |\n", escapeMarkdownCell(result.ANNRecommendationReason))
		builder.WriteString("\n")
		if len(result.WrongHitCases) > 0 {
			builder.WriteString("#### Wrong Hits\n\n")
			builder.WriteString("| case | expected | actual | answer | answer_id | candidate_score | semantic_score | reason |\n")
			builder.WriteString("|---|---|---|---|---|---:|---:|---|\n")
			for _, wrongHit := range result.WrongHitCases {
				fmt.Fprintf(&builder, "| %s | %s | %s | %q | %s | %.4f | %.4f | %s |\n",
					wrongHit.CaseID,
					wrongHit.ExpectedLayer,
					wrongHit.ActualLayer,
					wrongHit.ActualAnswer,
					wrongHit.ActualAnswerID,
					wrongHit.ActualCandidateTopScore,
					wrongHit.ActualSemanticTopScore,
					escapeMarkdownCell(wrongHit.MismatchReason),
				)
			}
			builder.WriteString("\n")
		}
		if len(result.BorderlineCases) > 0 {
			builder.WriteString("#### Borderline Cases\n\n")
			builder.WriteString("| case | layer | candidate_score | semantic_score | reason |\n")
			builder.WriteString("|---|---|---:|---:|---|\n")
			for _, borderline := range result.BorderlineCases {
				fmt.Fprintf(&builder, "| %s | %s | %.4f | %.4f | %s |\n",
					borderline.CaseID,
					borderline.ActualLayer,
					borderline.ActualCandidateTopScore,
					borderline.ActualSemanticTopScore,
					escapeMarkdownCell(borderline.MismatchReason),
				)
			}
			builder.WriteString("\n")
		}
	}
	return builder.String()
}

func embeddingReportValue(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "n/a"
	}
	return trimmed
}

func escapeMarkdownCell(value string) string {
	value = strings.ReplaceAll(value, "|", "\\|")
	value = strings.ReplaceAll(value, "\n", " ")
	return value
}
