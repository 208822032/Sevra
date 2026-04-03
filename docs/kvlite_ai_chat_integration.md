# KVLite AI-chat Integration

## 1. 项目背景与目标

AI-chat 的上下文存储原本以 Redis 为基线，负责按消息链回溯最近上下文、保存会话状态并支撑 prompt 组装。KVLite 接入的目标不是替代 Redis，而是在 AI-chat 的热上下文轻路径上做一个更贴近业务的数据结构与读写优化方案。

当前项目只聚焦两类能力：

- `session_id + seq` 驱动的会话内 turn 管理。
- 热上下文下 recent turns 和 session meta 的快速读写。

不追求的方向也很明确：

- 不做全量 Redis 替代。
- 不把 summary / shadow compare / dual-write / self-repair 继续当成默认主路径。
- 不把 skiptable、编码压缩、实验性 benchmark 当成主线资产。

## 2. 设计思路

KVLite_Base 的核心抽象是把一个 session 看成有序 turn 集合：

- `session_id` 定位会话。
- `seq` 定位会话内的 turn 顺序。
- turn 主存负责最近消息与完整 turn 内容。
- session meta 负责 `LastSeq`、`TurnCount`、`LastMessageID` 等轻量状态。

底层结构上，当前主线依然围绕 `rbtree + hash` 的组合：

- `rbtree` 适合按序存放 turn。
- `hash` 适合 session meta、索引和快速定位。

Redis 和 KVLite_Base 的职责边界是：

- Redis 作为基线、对照和必要 fallback。
- KVLite_Base 作为轻路径优化实现，服务热上下文读写。

## 3. 接入实现

当前保留的代码主线主要在以下位置：

- [chat-server/chat-context/backend.go](../ai-chat-service/chat-server/chat-context/backend.go)
- [chat-server/chat-context/session.go](../ai-chat-service/chat-server/chat-context/session.go)
- [chat-server/chat-context/session_hot_cache.go](../ai-chat-service/chat-server/chat-context/session_hot_cache.go)
- [chat-server/chat-context/real_redis_vs_kvlite_test.go](../ai-chat-service/chat-server/chat-context/real_redis_vs_kvlite_test.go)
- [chat-server/chat-context/real_redis_vs_kvlite_benchmark_test.go](../ai-chat-service/chat-server/chat-context/real_redis_vs_kvlite_benchmark_test.go)
- [chat-server/chat-context/kvlite_integration_test.go](../ai-chat-service/chat-server/chat-context/kvlite_integration_test.go)
- [chat-server/server/kvlite_phase2_test.go](../ai-chat-service/chat-server/server/kvlite_phase2_test.go)

配置切换保持简单：

- `context_backend = redis | kvlite`
- `context_fallback_to_redis = true | false`
- `context_namespace` 统一控制命名空间
- `kvlite.addr` 指向 KVLite 实例

当前主路径保留的能力：

- session 解析与回溯。
- recent turns 读取。
- session meta 读写。
- hot meta cache 与 recent-window cache。
- 真实 Redis / KVLite 双后端 benchmark。

已经不再作为主路径保留的 phase3 / phase4 增强项：

- summary 插入与 summary gating 的独立主线。
- shadow read、双写灰度、一致性对比、自愈修复的独立入口。
- skiptable 评估和编码压缩的 benchmark 旁路。

这些能力如果未来再做，只能作为单独实验，不再作为当前仓库的默认展示面。

## 4. 测试与验证

保留的单元和集成测试分工如下：

- [chat-server/chat-context/session_store_test.go](../ai-chat-service/chat-server/chat-context/session_store_test.go)：基础读写、meta 更新、TTL、fallback。
- [chat-server/chat-context/hot_path_test.go](../ai-chat-service/chat-server/chat-context/hot_path_test.go)：hot meta cache 和 recent-window cache。
- [chat-server/chat-context/kvlite_integration_test.go](../ai-chat-service/chat-server/chat-context/kvlite_integration_test.go)：真实 KVLite 服务端到端验证。
- [chat-server/chat-context/real_redis_vs_kvlite_test.go](../ai-chat-service/chat-server/chat-context/real_redis_vs_kvlite_test.go)：真实 benchmark 主体、repeat/aggregate 支撑。
- [chat-server/chat-context/real_redis_vs_kvlite_benchmark_test.go](../ai-chat-service/chat-server/chat-context/real_redis_vs_kvlite_benchmark_test.go)：种子确定性、聚合统计、写路径热 meta 复用。
- [chat-server/server/kvlite_phase2_test.go](../ai-chat-service/chat-server/server/kvlite_phase2_test.go)：会话 ID 优先级、recent-turn prompt 组装。

快速验证建议只用 3 个入口：

```bash
bash scripts/test_kvlite_base.sh
bash scripts/verify_kvlite_base_flow.sh
bash scripts/bench_real_redis_vs_kvlite.sh
```

如果要验证多轮真实压测，则使用 repeat 入口：

```bash
bash scripts/bench_real_kvlite_targets_repeat.sh
```

## 5. 压测设计

当前 benchmark 只保留三类目标 workload：

- `read_heavy`
- `hot_sessions`
- `write_heavy`

这样设计的原因很直接：

- `read_heavy` 代表 recent turns 回放压力。
- `hot_sessions` 代表少量 session 高命中、高复用压力。
- `write_heavy` 代表 open / complete / meta 更新密集压力。

repeat 口径统一为：

- 固定 seed base，保证 trace 可复现。
- warmup 与正式采样分离。
- 每轮 100000 requests。
- 默认 5 轮，可通过 `REAL_BENCH_REPEAT` 调到 10 轮。
- 聚合输出 median、mean、stddev 和 run variance。

这个设计的目的不是做“全场景压 Redis”，而是让 AI-chat 热上下文主路径在可重复、可解释的条件下看清楚真实收益。

## 6. 压测结果与结论

当前以 5 轮、每轮 100000 requests、warmup 5000 的聚合结果为准：

| workload | Redis median QPS | KVLite median QPS | Redis median p99(ms) | KVLite median p99(ms) | winner |
| --- | ---: | ---: | ---: | ---: | --- |
| hot_sessions | 2642.10 | 2418.51 | 1.173 | 1.290 | Redis |
| read_heavy | 2780.69 | 2696.15 | 1.699 | 1.695 | Redis |
| write_heavy | 1609.25 | 1835.29 | 0.994 | 0.789 | KVLite |

这说明当前工程上最准确的结论是：

- KVLite_Base 已经在写多场景形成稳定优势。
- `hot_sessions` 和 `read_heavy` 在当前 repeat 聚合下仍然更偏 Redis。
- 真实项目表述应该是“KVLite_Base 在 AI-chat 热上下文轻路径上具备局部优势”，而不是“全面替代 Redis”。

这套结果足以支撑简历表达，因为它体现的是：

- 有明确业务目标，而不是纯存储引擎炫技。
- 有真实 Redis 对照，而不是只在内存 mock 上自证。
- 有 repeat 和聚合，不是单轮偶然数据。

## 7. 项目收敛结论

最终这个项目可以被理解成一个清晰的工程主线：

1. 把 AI-chat 的上下文读写从纯 Redis 路径收敛到 `session_id + seq` 的轻量会话模型。
2. 用 KVLite_Base 承载热上下文轻路径，并保留 Redis 作为 baseline / fallback。
3. 用真实 benchmark 证明它在目标场景里确实有价值，但不把它包装成 Redis 全量替代品。

当前仓库里真正值得长期保留的核心资产只有三类：

- 接入实现。
- 必要测试。
- 真实 benchmark 设计和最终聚合报告。

其余 phase 文档、实验脚本、重复 benchmark 壳、历史报告模板，都属于过程资产，不再作为主展示面。