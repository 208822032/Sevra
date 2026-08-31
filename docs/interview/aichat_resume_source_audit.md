# AI Chat / KVLite 秋招简历源码事实审计

审计日期：2026-08-18（UTC）  
审计对象：当前工作区 `ai_chat_kvstore`，以**当前工作树控制流**为准；Git `HEAD` 为 `c741969`。  
本报告只审计，不修改业务代码。审计开始前工作区已存在大量未提交的检索迁移、启动脚本和文档变更；它们均保留原状。本文将当前工作树与已提交历史明确区分。

## 0. 执行结论

1. **KVLite 已进入真实业务链路，不是单独 Demo。** 当前 `ai-chat-service` 同时将其用于：
   - 会话 Context 后端（开发配置：`context_backend: kvlite`，9300；Redis 仅在 KVLite 网络/超时错误时 fallback）；
   - 独立知识库向量后端（28081），在每次 Chat 的路由中执行 `VGET -> VSEARCH`，高置信命中直接返回审核知识答案，未命中或后端错误才调用 LLM。
2. **这不是完整 RAG。** 当前高置信命中为“知识答案直返”；`retrievalPromptMessage` 虽存在，但路由没有将 `UseRetrievalAsContext` 置为 true，所以没有把 Top-K 文档注入 LLM prompt，也没有 rerank、引用生成或文档分段 RAG。
3. **SessionStore 抽象真实存在，但不是语义完全等价的可替换存储。** Redis 是逐 key `SET`；KVLite 用 RESP pipeline 批量发送 `RSET/PEXPIREAT/HSET/...`。二者都没有跨 key 原子提交，KVLite 还会在 timeout/连接错误时重试写。接口统一不代表事务、线性一致或相同故障语义。
4. **存在 P0 正确性风险：同一 session 的序号分配不是原子操作。** `AllocateTurnSeq` 读取 `LastSeq` 再返回 `LastSeq+1`；没有 mutex、CAS、Redis `INCR`/Lua 或 KVLite 原子计数。并发请求或多实例会复用 seq、覆盖 turn，并使 meta 与 mapping 不一致。
5. **业务闭环已达到“受控知识入库 → RDB 持久化 → 健康检查 → 精确/语义直返 → LLM 降级 → trace”的可验证程度**，但真实语料的 threshold 校准、动态多租户身份到 scope 的绑定、向量 HA、异步持久化 durability 尚未完成。因此可作为“KVLite 在 AI Chat 的受控知识检索落地”，不能宣称生产级多租户 RAG 平台。

## 1. 证据、范围与当前状态

### 1.1 本次直接验证

| 证据 | 结果 | 解释 |
|---|---|---|
| 当前源码控制流 | 已逐段审计 | 高于 README、注释和历史设计文档 |
| `cd ai-chat-service && go test ./...` | 通过 | 单元/模拟协议测试通过；外部依赖集成和真实 benchmark 默认通过环境变量跳过 |
| Context Redis/KVLite 原始 benchmark artifact | 存在 | `artifacts/benchmarks/real_redis_vs_kvlite_targets/report.{json,md}`，5 次 repeat |
| Vector live/RDB/压测 | 有可执行测试与阶段文档记录 | 当前仓中没有本次 vector benchmark 的原始 JSON artifact；不能把文档中的一次运行等同于持续基准证据 |
| Git history | 6 个提交，单一 author | 可证明演进，不足以证明整个项目从零自研 |

### 1.2 审计前已有未提交变更

开始时 `git status --short` 已包含：旧 `qa/`、LSH/ANN/外部 vectorDB 路径删除；新的 `chat-server/retrieval/kvlite/`、KVLite CLI、配置及文档；服务优雅退出脚本等。它们是**当前源码事实**，但尚未提交，故简历或交付时应先经过 review、测试并形成独立 commit。报告之外不更改这些文件。

## 2. 真实项目地图与架构

```text
ai-chat-web (Vue；上游 ChatGPT-Web 衍生)
        │ HTTP POST / chat 流
        ▼
ai-chat-backend (Gin)
  ChatProcess: request Context + bearer token
        │ gRPC server-stream；HTTP 端输出逐行 JSON（非 SSE/WebSocket）
        ▼
ai-chat-service (gRPC Core)
  sensitive/keywords ──► keywords-filter (gRPC)
  resolve session ─────► SessionStore
  route: VGET → VSEARCH ─► 独立 KVLite Vector :28081
        │ hit: 知识答案直返；miss/error: provider
        ▼
  Provider Registry ───► OpenAI-compatible API / mock / proxy
        │
        ├─ async CompleteTurn ─► KVLite Context :9300（Redis 网络故障 fallback）
        └─ async ChatRecord ───► MySQL

辅助组件：tokenizer（HTTP）、openai-api-proxy、mock-openai-api、keywords-filter；
`ai-chat-web/docker-compose` 仅为上游前端 app/nginx 组合，并非本项目完整部署编排。
```

当前 `scripts/start_all_services.sh` 也明确把 MySQL、Redis、tokenizer 和向量 KVLite 作为外部服务。因此“一个 compose 启动全部生产依赖”的说法不成立。

## 3. 上游与个人贡献边界

### 3.1 可证实的来源

| 模块 | 源码/Git 证据 | 归属结论 | 简历表述 |
|---|---|---|---|
| `ai-chat-web` | `package.json` 名为 `chatgpt-web`，author 为 ChenZhaoYu；MIT copyright 为 ChenZhaoYu | 明确上游前端基础，当前有 UI 适配 | “基于 ChatGPT-Web 前端进行接口/模型选择适配”，不要写从零前端 |
| `ai-chat-backend` | README 明确链接 Arvintian ChatGPT-Web，并说明 Gin backend + Chanzhaoyu frontend | 明确已有 backend 基础 | “在已有 Gin/gRPC Chat 基础上二次开发” |
| KVLite | `.gitmodules` 指向独立 `9.1-kvstore.git` | 外部/独立项目；本仓是客户端和业务接入，不是 KVLite 内核自研证据 | “将 KVLite 接入 AI Chat”，不要写“自研向量数据库” |
| 初始系统 | 初始提交 `c7921fa` 一次引入约 406 文件、前后端、代理、字典、文档压缩包 | 历史不足以支持“从零独立实现完整 ChatGPT 全栈” | 说明开源基座和自己负责的演进范围 |
| Provider routing | `424eaf1` 新增 provider catalog/registry、服务和前端适配 | 可作为本人二次开发演进证据 | 可写多 Provider 的配置化路由与 trace |
| 旧 QA/ANN | `d73f548`、`6ed6be6`、`c741969` 显示本人持续加入 QA、语义/ANN 评估 | 历史真实，但当前工作树已删除旧主路径 | 只作为“演进/重构背景”，不要把已删模块写成当前能力 |
| 新 KVLite V-command 客户端 | 当前未提交 `retrieval/kvlite/`、route、CLI/测试 | 当前个人二次开发事实；尚待提交归档 | 可写为“设计并实现 KVLiteVectorStore”，同时注明为当前工作树改动 |

### 3.2 可面试讲的 Git 演进（不超过五项）

1. **多 Provider 路由（`424eaf1`）**：从单一模型配置演进为 profile、模型目录、stream capability 和回退选择；代价是请求错误可能回落到默认 Provider，需 trace 说明实际选择。
2. **QA/KVLite 检索接入（`d73f548`）**：引入旧 QA 存储和预检索；当前已发现其 mock/ANN 分支难以与真实向量协议长期兼容。
3. **ANN/评估扩展（`6ed6be6`、`c741969`）**：曾建设 LSH/ANN、shadow、评估与前端 trace；当前工作树反向收敛，删除了这些非 KVLite 主路径。
4. **Context Redis/KVLite 对比**：当前有统一 SessionStore、真实 benchmark harness 和原始五次聚合 artifact；是最具后端工程深度的证据，但不能掩盖 seq race。
5. **KVLiteVectorStore 收敛（当前工作树）**：通过 V 命令、可逆编码、严格 RESP parser、受控入库、启动健康检查，替换 Go mock embedding/LSH/外部 vectorDB 决策；有清晰边界和验证。

## 4. 一次真实 Chat 请求：同步、异步与完成语义

### 4.1 实际路径

1. Web 调 Gin `ChatProcess`；Gin 从请求 context 建立 gRPC server-stream。
2. gRPC service `ChatCompletionStream` 同步 `ResolveSession`、敏感词检查和关键词提取。
3. `resolveRouteDecision`：若 vector store 就绪，按固定 scope 调 `VGET`；不存在再调 HNSW `VSEARCH`。命中后将 document ID、scope、score、question/answer preview 写入 gRPC trailer trace。
4. `buildChatCompletionRequest`：需要会话时同步加载 recent completed turns/可选 summary，随后同步 `AllocateTurnSeq` 和 `WriteOpenTurn`。这一步失败会使请求在调用 LLM 前失败。
5. 知识命中：同步将答案逐字符封装为 gRPC stream；未命中/故障：同步从 OpenAI-compatible provider `Recv`，每块同步 `stream.Send` 到 Gin。
6. stream EOF 或 unary provider 成功后，启动 goroutine 调 `saveContext`（将 open turn 完成）；另启动 goroutine 写 MySQL `chat_records`。
7. Gin 把每块 JSON 写到 HTTP 响应；gRPC trailer 带 trace/usage/budget。

### 4.2 “回答成功”到底意味着什么

| 状态 | 在用户收到成功响应前？ | crash 后果 |
|---|---|---|
| Session 解析、历史读取 | 是 | 无新状态 |
| 用户 open turn（含 meta） | 是 | 若已成功则用户问题已留存；若之后崩溃会留下 `open` turn |
| KVLite 知识命中或 LLM 上游响应 | 是 | 用户已可得到答案 |
| assistant completed turn | **否**，`go saveContext` | 回答可能不进入后续 history；下次只看到 open turn，且 loader 会跳过未完成 turn |
| MySQL 审计记录 | **否**，`go persistChatRecord` | 记录可能丢失 |
| 向量知识回写 | 不存在 | LLM 回答不会自动污染知识库，这是有意设计 |

因此当前是“**用户可见回答成功 + best-effort 后置持久化**”，不是异步可靠持久化。`saveContext` 使用 `context.Background()`，没有 WAL/outbox/MQ、重试状态、幂等消费或有界 worker pool；高 QPS 下会出现 goroutine 和下游压力累积。生产化建议：请求内先写可恢复 outbox，固定容量 worker/队列处理完成态和 MySQL，按 `(session_id, seq, completion_id)` 幂等重试。

### 4.3 Streaming 与取消

- 上游 `ChatStream` 采用派生自 gRPC stream context 的带超时 context；handler 在 `Send` 或 `Recv` 错误时返回，defer 的 `chatStream.Close()` 会 cancel 上游。因此客户端断开通常能传递 cancellation。
- 流式循环是同步 `Recv -> Send`，没有显式缓冲 channel；慢客户端会阻塞 `Send`，不再继续消费上游，属于串行 backpressure，而非完善的限流/队列实现。
- 没有每 token 新 goroutine；风险主要来自每请求的两个后置写 goroutine 及未实际启用的 shadow compare goroutine。

## 5. Session / Turn 数据模型与并发审计

### 5.1 模型与键

| 对象 | 内容 | Key |
|---|---|---|
| `TurnRecord` | `SessionID`、`Seq`、open/completed/failed、用户/助手消息与 ID、token、时间戳 | `ai:chat:sess:<session_id>:turn:%010d` |
| `SessionMeta` | `HeadSeq`、`LastSeq`、`TurnCount`、TTL/expire、compat、summary ref/seq、last message ID | `ai:chat:sess:<session_id>:meta` |
| `SummaryRecord` | 摘要范围、文本、token、过期时间 | `ai:chat:sess:<session_id>:summary` |
| 兼容映射 | request/parent message ID → session ID | `ai:chat:compat:msg:<id>`、`ai:chat:compat:pid:<id>` |

`session_id + seq` 使 turn 可顺序恢复，十进制零填充也保证键字典序与 seq 一致；但实现并不 scan，而是读 meta 的 `LastSeq` 后从起始 seq 逐项 `RGET`，只返回 completed turn。`LoadTurnsSince(afterSeq, limit)` 在 remaining 大于 limit 时会取**最近**窗口，名称不代表严格从 afterSeq 开始的完整增量。

### 5.2 P0：`AllocateTurnSeq` 竞争

当前实现：`loadMetaCached/loadMeta → meta.LastSeq + 1`，分配时没有写入 reservation。

```text
已提交 LastSeq=10
Request A: Read 10 ────────────────────── returns 11
Request B:      Read 10 ───────────────── returns 11
A WriteOpenTurn(11) → meta.LastSeq=11, TurnCount+1
B WriteOpenTurn(11) → 覆盖 turn:11，meta 再 +1
```

结果包括：用户/助手 turn 覆盖、`TurnCount` 与实际 turn 数不符、compat mapping 指向被覆盖请求、并发 completion 互相覆盖。进程内 hot cache 反而扩大同值可见窗口；即使加进程内 mutex 也不能解决多实例。

**当前没有** Redis `INCR`/Lua、KVLite 原子 increment/CAS、数据库锁或分布式锁；同 session 并发测试也未覆盖该竞争。此项应在合并/上线前列为 P0。

推荐方案：

- Redis：将 `last_seq` 设为独立 counter，Lua 中原子 `INCR` + 创建 open turn/meta（或最少以 `INCR` 发号并用版本校验更新 meta）。
- KVLite：需要存储端原子 counter/CAS/compare-and-set 或 dedicated sequence command；不能用 client pipeline 模拟原子性。
- 两端：以 `(session_id, request_message_id)` 设请求幂等键，重复请求返回同一 seq；完成态用 compare-version，禁止旧 completion 覆盖新 turn。

### 5.3 SessionStore 语义矩阵

| API/行为 | Redis | KVLite | 语义一致？ | 原子性/TTL 差异 |
|---|---|---|---|---|
| `ResolveSession` | 读 compat key | 同 | 基本一致 | 都是普通 key，不是 hash |
| `LoadRecentTurns/LoadTurnsSince` | meta + 逐 turn GET | 同 | 基本一致 | 都可能读到部分写入状态 |
| `Load/StoreSessionMeta` | `SET key value ttl` | `HSET` 后 `PEXPIREAT` | 接口一致 | Redis 单 key 带 TTL；KVLite 是两命令，TTL 可漏设 |
| `AllocateTurnSeq` | read + add | read + add | 同样有 race | 都非原子 |
| `WriteOpenTurn` | turn、meta、mapping 串行写 | 多命令 RESP pipeline | 非严格一致 | 都无跨 key transaction；KVLite 一次 flush 降 RTT |
| `CompleteTurn` | 串行更新 | pipeline 更新 | 非严格一致 | completion 与 meta 可部分成功 |
| `StoreSummary` | 先 summary 后 meta | 同 | 基本一致 | summary/meta 同样无原子提交 |
| TTL/删除 | Redis 原生 TTL/DEL | PEXPIREAT/DEL | 表面相似 | PEXPIREAT 与数据写分离；接口无 scan/DeleteSession |

结论：这是可用于 A/B 的后端接口，不是“Redis/KVLite 完全可互换的强语义存储抽象”。

## 6. Pipeline、fallback、shadow 与缓存

### 6.1 RESP Pipeline 不是事务

KVLite `execBatch` 将多个 RESP 数组写入同一 buffered writer，**一次 Flush**，再顺序读取 N 个响应。`WriteOpenTurn` 的 batch 包含 `RSET(turn)`、`PEXPIREAT(turn)`、`HSET(meta)`、`PEXPIREAT(meta)` 与兼容 mapping。

这只减少 RTT，未出现 `MULTI/EXEC`、rollback、commit marker 或原子 storage-side batch：

```text
RSET turn          success
PEXPIREAT turn     success
HSET meta          fail/connection break
=> open turn 存在，meta 未推进或 TTL/mapping 缺失
```

KVLite client 对 timeout/connection error 可重试整批写；“服务已执行、客户端未收到 response”会导致重发。多数 RSET 是覆盖式且相对幂等，但结合 seq race/非版本化 meta 仍可能放大不一致。简历禁止写“事务批量提交”。

### 6.2 Composite fallback / shadow compare

当前开发配置为 `KVLite primary + Redis fallback`。仅当 primary 返回 `ErrKvliteTimeout` 或 `ErrKvliteConnError` 才 fallback；空结果不 fallback。这是 availability-first 的故障切换，**不是双写强一致**。

典型不确定写：KVLite 已写成功但 response timeout → 服务把它当失败 → Redis fallback 写成功。随后正常读优先 KVLite，两个后端可能拥有不同 turn/meta/seq；没有默认 reconciliation。`dualWrite` 字段未在 `NewStore` 配置，shadow compare 的 flags/timeout 也未初始化，且当前主读写路径未调用 `shadowCompare*`。代码中虽有 background shadow/repair 函数，当前配置/控制流并未形成启用能力；即便启用也是异步 eventual repair，不能成为一致性承诺。

### 6.3 Local cache / hot cache

存在两套进程内 cache：`sessionHotCache`（meta、recent window，默认 10 秒）和 server `sessionFastPathCache`（meta 3 秒、summary 5 秒）。它们以 session 为键，写后更新/窗口推进，读时用 `LastSeq` 使 window 失效；不是分布式缓存，跨实例可陈旧。

两者都有 mutex，但 `state(sessionID)` 会为每个访问过的 session 创建 map entry，过期仅清 value/window，**无容量、LRU 或整体回收**，长期进程内存不受限。`loadSessionSummaryFast` 当前直接读 store，未消费 fastPathCache 的 summary，因此 summary cache 不是已验证的有效热路径。

### 6.4 QA Cache 的实际层级

当前生产 route 不再有 normalized QA cache、alias、candidate bucket、Go embedding、LSH ANN 或自动 LLM writeback。真实 precedence 为：

```text
VGET exact question + static logical scope
  → 命中：直返
VSEARCH HNSW + threshold
  → 首名命中：直返
empty / unavailable / not-ready / protocol error
  → LLM
```

所以 `VGET` 是知识库的 exact read，不是 provider response cache；旧 QA 的相关 trace 字段仍能在 HTTP 类型中看到，但当前不会由新路由填充。文档和字段遗留不能当作现有能力。

## 7. Semantic Retrieval / KVLite Vector 业务落地

### 7.1 当前真正调用与数据

- 客户端仅实现并使用 `VSET`、`VGET`、`VSEARCH`、`VDEL`、`VINFO`；**未调用 `VINDEX`**。
- `VSET(question, encoded-answer, encoded-scope)` 将原问题交给 KVLite 本地 ONNX embedding；服务端模型由独立 KVLite 配置承担。AI Chat 的 Provider 虽定义 `Embed`，当前路由并不调用它。
- 当前预期向量能力：`bge-small-zh-v1.5`、512 维、IP、`core_hnsw_v2`；`Health` 强校验 `VINFO` 的 ready/index/dimension/metric。
- answer 不是 embedding input；它以 `base64url(JSON envelope)` 存储 `version/document_id/answer/source/created_at`。scope 是 canonical JSON 的 Base64URL；question 先转义 `\\` 再转义 `|`。严格 parser 只接受 `score|scope|question|answer` 的固定顺序，畸形返回报 protocol error，不猜测命中。

这解决的是“**审核知识问答的精确与语义检索直返**”，不是 session memory 的向量化，也不是通用文档 RAG。

### 7.2 ANN 所处位置与 fallback

HNSW 是 `VGET` miss 后唯一 ANN 路径，不存在 brute-force fallback 或外部 vectorDB fallback。`empty result` 是正常未命中；连接/模型未就绪/RESP 格式错误均经 `FailureReason` 进入 LLM，而不会伪造本地答案。该区分已由 store/route 单测覆盖。

注意：`top_k=1`，route 只消费首名。配置 `threshold=0.90` 被注释为开发保守保护值；不是已完成业务质量标定。静态 scope 的 Base64 编码可防止**已正确传入的** scope 混淆，但 HTTP 请求没有 tenant authentication 到 scope 的映射，因此动态多租户隔离未完成。

### 7.3 受控入库与持久化闭环

`cmd/kvlite-ingest` 读取一份严格 JSON manifest，调用 `ControlledWriter`：需要非空 `approval_id`、`source=knowledge_base`、正 `created_at`；同 question/scope 且相同 document ID 可幂等，其他 ID 冲突拒绝。它不扫描聊天记录，不会自动把 LLM 回答入库。局限：approval ID 只是非空字符串，没有身份鉴权、审核记录或签名；相同 document ID 的内容变化会被当作幂等跳过，当前没有 update/version lifecycle。

28081 是独立向量实例，文档记录了 RDB 停止/重启后 `VGET/VSEARCH/VINFO` 恢复。KVLite 向量主从复制未实现，故当前是单节点 RDB 恢复，不是高可用。建议把审核系统/源文档作为 KB 的 authoritative source，RDB 作为运行时索引持久化，并做定期备份和恢复演练。

## 8. Provider、存储职责与配置

### 8.1 Provider abstraction

Registry 按 enabled/priority 建立 OpenAI-compatible provider，按 requested provider/model/stream capability 选择；未知 provider/model 或不支持 stream 时回落默认 profile，并将 requested/actual/fallback 记录在 trace。每次上游调用有 timeout，错误分类为 canceled/timeout/auth/rate-limit/invalid/upstream。没有 retry policy 的实际执行（profile 虽有 `MaxRetries` 字段，但当前 provider 调用未使用），也没有跨 provider 的运行时健康路由。

Chat 和 embedding 在类型设计上可不同，但当前业务 embedding 固定在 KVLite local ONNX；Provider embedding capability 没进入检索链路。

### 8.2 存储角色表

| Storage | 当前数据 | 当前 Source of Truth / 角色 | Cache/fallback 边界 |
|---|---|---|---|
| KVLite Context :9300 | session turn、meta、compat、summary | 当前配置下会话历史主读写 | Redis 只在 KVLite timeout/conn error fallback；可能分叉 |
| Redis | Context fallback；旧 `ContextCache` 类型 | 不是当前配置下的稳定双写副本 | 不等于 KVLite 的强一致镜像 |
| KVLite Vector :28081 | 审核 knowledge QA、HNSW embedding/index | 运行时可检索知识库/RDB；上游审核原始材料不在仓中 | 运行时故障回源 LLM；单节点无复制 |
| MySQL | `chat_records`：用户/AI 文本、token、keywords、创建时间 | 异步审计记录；不承载按 session 恢复 | 当前非同步 durable，不应称会话主存储 |
| 进程内 map | session hot/meta/window | 性能优化 | 非分布式、无上限、短 TTL |

## 9. Benchmark 与测试审计

### 9.1 Redis vs KVLite Context benchmark（原始 artifact）

artifact 的方法：同一真实 SessionStore workload，5 次 repeat、每次 5,000 warmup，总 3,000,000 requests/turns；记录 QPS、p50/p95/p99、errors、hot cache 命中。它包含客户端/网络/序列化/存储访问，但不是 LLM 或 vector HNSW benchmark。环境仅标注 namespace/seed，未在 artifact 中充分记录 CPU、Redis/KVLite 是否同机、版本和资源隔离，跨机器外推需谨慎。

| workload | Redis median QPS / p99 ms | KVLite median QPS / p99 ms | errors | 审计结论 |
|---|---:|---:|---:|---|
| hot_sessions | 2642.10 / 1.173 | 2418.51 / 1.290 | KVLite 1，Redis 0 | Redis 更好且 KVLite 稳定性不足 |
| read_heavy | 2780.69 / 1.699 | 2696.15 / 1.695 | 0 / 0 | 吞吐 Redis 更好，尾延迟接近 |
| write_heavy | 1609.25 / 0.994 | 1835.29 / 0.789 | 0 / 0 | KVLite 在此 workload 有局部优势 |

因此以下表述评级为：

| 表述 | 评级 | 理由 |
|---|---|---|
| “KVLite 替代 Redis” | 不推荐 | 有 semantics/并发/fallback 风险，2/3 workload Redis 更好 |
| “Redis/KVLite 可切换 Context Backend” | 谨慎 | 接口和配置成立，但语义不等价 |
| “统一 SessionStore 支持 Redis/KVLite A/B” | 推荐 | 源码和原始 artifact 支持 |
| “AI Chat Context workload 的 A/B benchmark” | 推荐 | 方法/原始结果可追溯 |
| “KVLite write-heavy 优于 Redis” | 推荐但限定 | 仅本环境、五次聚合、该 workload；不可泛化 |
| “KVLite 全面超过 Redis” | 不推荐 | artifact 直接反驳 |

### 9.2 Vector benchmark / 质量证据

`cmd/kvlite-benchmark` 是独立 scope、VSET/VSEARCH/VDEL 的可清理端到端工具。阶段文档记录过：50 unique-answer docs、500 query、并发 4、512/IP/HNSW、threshold -1，Recall@1 0.984、0 error、140.90 QPS、P50/P95/P99 28.542/34.810/38.942 ms。这是合成中文 QA fixture 和本地 embedding cache 开启下的 client-path signal；原始 JSON 不是当前仓内 artifact，不能作为正式质量结论。

`cmd/kvlite-calibrate` 的设计是正确的：审核 holdout（>=100）遍历 topK/threshold，以 precision >= .99、negative false-direct <= .005 给最低 threshold 建议。真实业务标注集和产出尚不存在；`.90` 仍只是暂定保护线。

### 9.3 测试矩阵

| 类别 | 已覆盖 | 强度 | 主要缺口 |
|---|---|---|---|
| 编码/RESP parser | Base64URL、分隔符、Markdown、畸形响应 | 强 | 与未来 KVLite 协议版本契约 |
| Vector Store | V 命令、连接复用、unavailable/not-ready | 强（模拟） | 压测报告持久化、长期连接故障 |
| Vector live | health、受控 VSET/VGET/VSEARCH/VDEL、scope/Markdown | 有，显式 opt-in | 本次 `go test` 未执行 live 环境 |
| Vector RDB 恢复 | 阶段文档记录 | 中 | 自动化 CI/原始 artifact 缺失 |
| 路由 | exact、semantic、后端/协议故障回源 | 强 | 真实 provider + KVLite E2E |
| Session/Turn | 顺序、TTL、open turn 过滤、repair | 中 | **同 session 并发 seq race 未测** |
| Redis/KVLite context integration | opt-in real server | 中 | 不确定写/重试后分叉、multi-instance |
| Pipeline | 客户端实现和部分故障 repair | 中 | partial batch / atomicity 不可证明 |
| fallback/shadow | fallback 单测 | 弱-中 | unknown-success、repair/compare 当前未启用 |
| local cache | hit/invalidation/order | 中 | capacity、multi-instance stale、内存回收 |
| Provider | catalog selection | 中 | retry、真正多 provider 失败切换 |
| Streaming | 基本 controller/provider path | 中 | 慢客户端、取消、goroutine leak 的集成测试 |
| Async persistence | 无专门 durability 测试 | 弱 | crash/retry/outbox |
| Benchmark | context 原始 artifact；vector harness | 中 | 环境元数据、真实语料质量、持续 CI |

## 10. 风险分级与修复方向

### P0 / correctness & security

1. **同 session `AllocateTurnSeq` 非原子竞争**：会覆盖 turn 和破坏 meta；先做存储端原子发号/幂等 request mapping，再允许并发或多副本部署。
2. **受 Git 跟踪开发配置含非空真实形态 secret**：`ai-chat-service/dev.config.yaml`、`ai-chat-backend/dev.config.yaml`、proxy/mock/stack 等包含 API key、access token、DSN 或 Redis password。不要在报告、日志或简历复述其值；应立即轮换、撤销、从历史/当前 tracking 中移除，改环境变量/secret manager 和示例配置。即使是测试 key，也应按泄露处理。
3. **fallback 的不确定写可分叉**：timeout 后 Redis fallback 不能得出 KVLite 未写成功；不能称强一致。生产前需要 idempotency token、写状态/版本、outbox 或显式迁移策略。

### P1 / engineering

1. RESP pipeline 非事务，部分写和 TTL/mapping 漏写可见；用 storage-side transactional/CAS primitive 或可重放 journal。
2. 回答后无界 goroutine + `context.Background()`，进程 crash 丢 complete turn/MySQL record；换 bounded queue/outbox。
3. 两套无界进程内 session map；加 max entries、LRU/TTL sweep，并在多实例禁用其一致性假设。
4. shadow/repair 看似存在但当前未配置/未接入主读路径；要么删掉避免误导，要么显式配置、限流、指标和压测。
5. 静态 retrieval scope 不是 tenant auth；必须由经认证的 request identity 派生 scope，并校验写入同一授权边界。
6. MySQL `chat_records` 缺 session ID/request ID 等关联字段，且异步；不能作为可靠 history/query audit。

### P2 / productionization

- 审核/文档版本/删除和 update 生命周期、真实语料 calibration。
- KVLite vector 备份、恢复演练、容量迁移；当前无向量复制/HA。
- provider circuit breaker/retry budget、结构化 tracing/metrics、限流/租户配额。
- RAG prompt、rerank、citation 仅在业务需要时另立设计，不应用“补功能”掩盖 P0/P1。

P2 不降低秋招项目价值；P0/P1 必须在面试时如实说明并准备改进设计。

## 11. 最值得写入简历的候选（最多六项）

| 等级 | 候选 | 可写事实与证据 | 必须带的边界 |
|---|---|---|---|
| S | Redis/KVLite `SessionStore` + Session/Turn 模型 | `session_id+seq`、meta、TTL、recent history；真实 A/B artifact | 当前 seq 非原子，pipeline 非事务，不能宣称强一致 |
| S | KVLiteVectorStore 业务接入 | 独立 RESP pool、VGET/VSEARCH、严格 codec/parser、health、trace、路由直返/LLM 回源 | 是 KB Q&A 直返，不是完整 RAG；代码尚未提交 |
| A | 受控知识入库与索引恢复闭环 | approval-shaped manifest、冲突/幂等、独立 RDB、live tests/恢复记录 | approval 未鉴权，真实质量 threshold 未校准，单节点 |
| A | 面向真实 Context workload 的 Redis/KVLite benchmark | 5 repeat、warmup、p99/QPS/errors/hot stats 原始 artifact | KVLite 仅 write-heavy 优势，hot_sessions 有 1 error |
| A | 配置化 Provider routing + streaming trace | provider/model capability 选择，requested vs actual fallback trace，gRPC stream→HTTP | 无实际 retry/circuit breaker；基座并非从零 |
| B | 会话热缓存与 fast path | meta/window TTL、失效测试/指标 | 仅本地 map、无容量限制、跨实例不一致 |

不要单独把 Gin、gRPC、Redis、MySQL、Docker、OpenAI API 当亮点；它们是实现载体而非可深挖成果。

## 12. 最值得准备的模块排行榜

| 模块 | 面试价值 | 难度 | 是否写简历 |
|---|---|---:|---|
| Session/Turn + seq correctness | 极高 | 高 | 是，必须主动讲 race |
| Redis/KVLite abstraction 与一致性差异 | 极高 | 高 | 是 |
| KVLite RESP pipeline / partial failure | 极高 | 高 | 是 |
| KVLite V-command retrieval codec/parser | 极高 | 高 | 是 |
| HNSW 直返路由和错误/empty 区分 | 高 | 中高 | 是 |
| benchmark 方法与解读 | 高 | 中高 | 是 |
| 异步 context/MySQL durability | 高 | 高 | 不做亮点，做风险题 |
| fallback/shadow compare | 高 | 高 | 谨慎，仅说明未启用/限制 |
| provider selection + trace | 中高 | 中 | 是 |
| streaming cancellation/backpressure | 中高 | 中高 | 可准备 |
| 本地 hot cache | 中 | 中 | 不单独写 |
| 前端 UI | 低（本方向） | 中 | 不写为自研 |

## 13. 30 道基于源码的项目深挖题

### Level 1：架构和职责

1. 为什么 Context KVLite（9300）与 Vector KVLite（28081）分实例？
2. MySQL、KVLite、Redis 分别保存什么，谁是 session history 的主读写端？
3. 一次 HTTP Chat 如何变成 gRPC stream，再变为 HTTP 返回？为什么它不是 SSE？
4. 为什么当前 KVLite 语义能力应称“知识问答直返”而非完整 RAG？
5. `VGET` 和 `VSEARCH` 在当前路由的 precedence 是什么？
6. Provider profile 如何从 requested provider/model 得到 actual provider/model？
7. 为什么 answer envelope 不参与 embedding，而 question 保持自然语言？
8. `logical_scope` 的 tenant/knowledge_base/corpus_version 分别隔离什么？

### Level 2：源码控制流与协议

9. `TurnRecord`、`SessionMeta` 与 compat mapping 的键 schema 是什么？为什么 seq 要零填充？
10. `LoadRecentTurns` 为什么只返回 completed turn？open turn 在崩溃后如何表现？
11. `VSEARCH` 的四字段文本返回为什么必须对 scope/question/answer 做不同编码？
12. 为什么 question 必须先转义反斜杠，再转义 `|`？
13. parser 对异常 score、未知 JSON 字段、非 canonical Base64URL 分别如何处理？
14. `Health` 为什么校验 512/core_hnsw_v2/IP/index_ready，而不只检查 TCP 可连？
15. `ControlledWriter` 如何幂等？同一 document ID 内容变化会发生什么？
16. route 如何区分 no-hit、unavailable、not-ready、protocol error？哪些会调用 LLM？

### Level 3：并发、一致性、durability

17. `AllocateTurnSeq` 的读加一 race 如何发生？hot cache 为什么不能解决它？
18. Redis `INCR` 方案与 KVLite CAS/sequence command 分别如何设计？
19. RESP pipeline 为什么不是 transaction？给出 RSET 成功、HSET 失败的状态图。
20. KVLite response timeout 后 fallback Redis write 为什么可能造成双写分叉？
21. 为什么 pipeline 整批重试会放大不确定写问题？
22. 当前 fallback 的触发条件为什么不包括空读？这种选择有什么 trade-off？
23. shadow compare/repair 为什么不能称强一致？当前为什么甚至不算启用能力？
24. LLM stream 已发给客户端后 `go saveContext` 还没完成时 crash，哪些数据会丢？

### Level 4：生产化

25. 如何把后置 context/MySQL 写改为可靠异步，而不降低首 token latency？
26. 如何让 multi-instance session cache 不返回陈旧 history，同时控制内存上限？
27. 如何把静态 retrieval scope 改为基于鉴权 tenant 的动态 scope，且避免越权入库/查询？
28. 为什么 vector threshold=.90 不能直接写成最终业务参数？校准集如何构造正例、负例和指标？
29. 向量单节点 RDB 恢复与高可用有什么本质区别？容量接近 initial_capacity 时如何迁移？
30. benchmark 中 KVLite write-heavy 胜、但其他两个场景负，如何向面试官解释而不选择性呈现数据？

## 14. 明确不建议写的内容

- “从零开发完整 ChatGPT Web 全栈系统”。前端/后端上游证据直接否定。
- “完整 RAG 平台”或“所有回答都经 HNSW/RAG”。当前为 `VGET/VSEARCH` 高置信直返。
- “KVLite 全面替代 Redis”或“KVLite 全面性能领先”。原始 benchmark 不支持。
- “事务 Pipeline”“强一致双写”“异步可靠持久化”。源码没有 transaction、双写一致协议或 durable queue/WAL。
- “生产级多模型平台”。有配置选择和 trace，但没有 retry/circuit breaker/租户治理。
- “完全自研前端”或“自研向量数据库”。

## 15. 最终定位（≤200字）

这是一个基于开源 ChatGPT-Web/Gin 基座、重点完成 Go 服务端 KVLite 落地的 AI Chat 工程：将 KVLite 真实接入 session context 与受控知识 QA 的 HNSW 检索，并用 RESP 协议兼容、健康检查、降级 trace 和 Redis/KVLite 基准形成业务闭环。最强点是存储建模、协议与评测；必须说明上游前端/后端边界，以及 seq 原子性、异步 durability、真实语料校准和向量 HA 尚未完成。此阶段应优先修正确性而非继续堆 Feature。
