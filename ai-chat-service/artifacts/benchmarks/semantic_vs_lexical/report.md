# Retrieval Quality Report

- Suite: ai-chat retrieval quality suite
- Version: v1
- Description: Stable retrieval evaluation suite for exact, normalized, alias, candidate, semantic, and hard-negative analysis.
- Generated At: 2026-04-17T04:10:42Z
- Dataset: /home/zhengpan/share/Linux/chapter_11/11.4-ai-chat/ai-chat-service/chat-server/qa/testdata/retrieval_eval_suite.v1.json
- Embedding Mode: provider
- Embedding Profile: openai-embeddings
- Embedding Provider: openai-embeddings
- Embedding Model: text-embedding-ada-002
- Embedding Source: mode=provider|profile=openai-embeddings|provider=openai-embeddings|model=text-embedding-ada-002
- ANN Recommendation: ann_prototype_candidate
- ANN Recommendation Reason: parameter_set=tight_candidate reason=semantic_only_wins=2 wrong_hit_ratio=0.00% semantic_avg_latency_ms=0.150

## Parameter Sets

### baseline

- Embedding Mode: provider
- Embedding Profile: openai-embeddings
- Embedding Model: text-embedding-ada-002
- ANN Recommendation: continue_semantic_eval
- ANN Recommendation Reason: wrong_hit_ratio=12.50%

| Metric | Value |
|---|---:|
| total_cases | 8 |
| exact_hit_rate | 12.50% |
| normalized_hit_rate | 12.50% |
| alias_hit_rate | 12.50% |
| candidate_hit_rate | 37.50% |
| semantic_hit_rate | 12.50% |
| fallback_to_LLM_ratio | 12.50% |
| wrong_hit_ratio | 12.50% |
| lexical_only_wins | 1 |
| semantic_only_wins | 1 |
| overlap_hits | 5 |
| all_miss | 1 |
| avg_latency_ms | 0.335 |
| p95_latency_ms | 0.896 |
| lexical_avg_latency_ms | 0.390 |
| lexical_p95_latency_ms | 1.836 |
| candidate_avg_latency_ms | 0.333 |
| candidate_p95_latency_ms | 0.412 |
| semantic_avg_latency_ms | 0.472 |
| semantic_p95_latency_ms | 1.110 |
| ann_recommendation | continue_semantic_eval |
| ann_recommendation_reason | wrong_hit_ratio=12.50% |

#### Wrong Hits

| case | expected | actual | answer | answer_id | candidate_score | semantic_score | reason |
|---|---|---|---|---|---:|---:|---|
| hard-negative-cache-gpu | miss | candidate | "clear-cache" | provider=deepseek|model=deepseek-chat|prompt_version=v1:0e99fd611a91bdd5500cc90d7b26c8cf709f9b2cac1cec1b8d0dd59824ef8ac3 | 0.6587 | 0.0000 | expected miss but actual layer candidate hit |

#### Borderline Cases

| case | layer | candidate_score | semantic_score | reason |
|---|---|---:|---:|---|
| hard-negative-cache-gpu | candidate | 0.6587 | 0.0000 | expected miss but actual layer candidate hit |

### tight_candidate

- Embedding Mode: provider
- Embedding Profile: openai-embeddings
- Embedding Model: text-embedding-ada-002
- ANN Recommendation: ann_prototype_candidate
- ANN Recommendation Reason: semantic_only_wins=2 wrong_hit_ratio=0.00% semantic_avg_latency_ms=0.150

| Metric | Value |
|---|---:|
| total_cases | 8 |
| exact_hit_rate | 12.50% |
| normalized_hit_rate | 12.50% |
| alias_hit_rate | 12.50% |
| candidate_hit_rate | 12.50% |
| semantic_hit_rate | 25.00% |
| fallback_to_LLM_ratio | 25.00% |
| wrong_hit_ratio | 0.00% |
| lexical_only_wins | 0 |
| semantic_only_wins | 2 |
| overlap_hits | 4 |
| all_miss | 2 |
| avg_latency_ms | 0.273 |
| p95_latency_ms | 0.453 |
| lexical_avg_latency_ms | 0.281 |
| lexical_p95_latency_ms | 1.045 |
| candidate_avg_latency_ms | 0.306 |
| candidate_p95_latency_ms | 0.404 |
| semantic_avg_latency_ms | 0.150 |
| semantic_p95_latency_ms | 0.238 |
| ann_recommendation | ann_prototype_candidate |
| ann_recommendation_reason | semantic_only_wins=2 wrong_hit_ratio=0.00% semantic_avg_latency_ms=0.150 |

### semantic_wide

- Embedding Mode: provider
- Embedding Profile: openai-embeddings
- Embedding Model: text-embedding-ada-002
- ANN Recommendation: continue_semantic_eval
- ANN Recommendation Reason: wrong_hit_ratio=12.50%

| Metric | Value |
|---|---:|
| total_cases | 8 |
| exact_hit_rate | 12.50% |
| normalized_hit_rate | 12.50% |
| alias_hit_rate | 12.50% |
| candidate_hit_rate | 37.50% |
| semantic_hit_rate | 12.50% |
| fallback_to_LLM_ratio | 12.50% |
| wrong_hit_ratio | 12.50% |
| lexical_only_wins | 0 |
| semantic_only_wins | 1 |
| overlap_hits | 6 |
| all_miss | 1 |
| avg_latency_ms | 0.290 |
| p95_latency_ms | 0.821 |
| lexical_avg_latency_ms | 0.224 |
| lexical_p95_latency_ms | 0.487 |
| candidate_avg_latency_ms | 0.303 |
| candidate_p95_latency_ms | 0.383 |
| semantic_avg_latency_ms | 0.140 |
| semantic_p95_latency_ms | 0.203 |
| ann_recommendation | continue_semantic_eval |
| ann_recommendation_reason | wrong_hit_ratio=12.50% |

#### Wrong Hits

| case | expected | actual | answer | answer_id | candidate_score | semantic_score | reason |
|---|---|---|---|---|---:|---:|---|
| hard-negative-cache-gpu | miss | candidate | "clear-cache" | provider=deepseek|model=deepseek-chat|prompt_version=v1:0e99fd611a91bdd5500cc90d7b26c8cf709f9b2cac1cec1b8d0dd59824ef8ac3 | 0.6587 | 0.0000 | expected miss but actual layer candidate hit |

#### Borderline Cases

| case | layer | candidate_score | semantic_score | reason |
|---|---|---:|---:|---|
| hard-negative-cache-gpu | candidate | 0.6587 | 0.0000 | expected miss but actual layer candidate hit |

