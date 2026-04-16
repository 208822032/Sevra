# ai-chat-service

这是 ai-chat 项目里用于上下文存储和会话回放的服务模块。当前主线已经收敛到 KVLite_Base：只保留 `session_id + seq`、`rbtree + hash`、recent N turns 读取、session meta、Redis 基线和必要 fallback。

## 当前主线

- `context_backend = redis | kvlite`
- KVLite 只承担轻路径上下文读写
- Redis 作为对照基线和必要回退
- summary / shadow compare / repair / dual_write 已不再作为默认主路径

## 推荐入口

- 基础测试：`scripts/test_kvlite_base.sh`
- 基础流程验证：`scripts/verify_kvlite_base_flow.sh`
- 总结文档：`docs/kvlite_ai_chat_integration.md`
- 真实压测：`scripts/bench_real_redis_vs_kvlite.sh`
- 多轮聚合压测：`scripts/bench_real_kvlite_targets_repeat.sh`
- 语义 vs 词法评估：`scripts/bench_semantic_vs_lexical.sh`

## 语义实验

`ai-chat-service` 里已经有一个 feature-flagged 的 brute-force semantic 兜底层。它只在 exact / normalized / alias / candidate 都没有命中时参与，而且仍然保持 lexical 优先，不是 ANN，也不依赖外部 vector index。

建议优先看这几个位置：

- 配置样例：`dev.config.yaml` 的 `retrieval.semantic`
- 运行时 trace：`qa_hit_type`、`candidate_count`、`candidate_top_score`、`candidate_source`、`semantic_candidate_count`、`semantic_top_k`、`semantic_top_score`、`semantic_model`、`semantic_source`、`final_answer_source`
- 评估入口：`scripts/bench_semantic_vs_lexical.sh`

如果你要判断后续是否值得进入 ANN 阶段，先看语义评估里是否真的出现了稳定的 semantic-only 收益，再看 `corpus_limit` 和 lookup latency 是否已经开始成为瓶颈。
