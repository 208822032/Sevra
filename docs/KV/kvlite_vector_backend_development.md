# AI-chat：KVLite 向量检索后端开发清单

## 1. 目标与边界

本阶段将 AI-chat 的语义检索收敛为单一路径：KVLite 负责文本 embedding、HNSW
索引、精确读取和语义检索。不会保留 Go 本地 embedding、LSH ANN、候选桶或外部
`vectorDB` 的检索决策路径。

会话上下文的 KVLite/Redis 后端不属于本阶段；它与知识检索保持独立。

- [√] 已完成代码审计：现有 AI-chat QA 实现只发送 `RGET`、`RSET`、`PEXPIREAT` 和
  `DEL`，未调用 KVLite `VSET`、`VGET`、`VSEARCH`、`VDEL`。
- [√] 已确认当前 KVLite 已具备本地 ONNX `local_c` embedding、512 维
  `bge-small-zh-v1.5`、`core_hnsw_v2`、`VSET/VGET/VSEARCH/VDEL/VINFO`。
- [√] 已确认当前 KVLite `VSEARCH` 返回文本格式：
  `score:<score>|scope:<scope>|question:<question>|answer:<answer>`。
- [√] 已确认当前格式不能直接承载 AI-chat 原始值：现有 scope 含 `|`，回答也可能含
  Markdown 表格或分隔符。
- [√] 已明确第一版写入策略：仅允许携带审核标识的受控入库；LLM 成功回答不会自动
  写入知识库。
- [ ] 明确知识库生命周期：第一版采用持久知识库和显式 `VDEL` 管理；不继承旧 QA
  的 30 分钟 TTL。若未来必须按 record 设置 TTL，另立 KVLite 向量 TTL 阶段。

## 2. 新的 KVLite 数据约定

KVLite 本身不改动。AI-chat 新客户端对所有 V 命令使用以下可逆编码，以安全兼容
现有 `VSEARCH` 文本响应。

| 字段 | KVLite 中的值 | 用途 |
| --- | --- | --- |
| `scope` | `base64url(logical_scope)` | 排除 `|`，确保租户和知识域隔离 |
| `question` | 转义 `\\` 与 `|` 的原始问题 | 保留自然语言给 ONNX embedding，仅处理分隔符 |
| `answer` | `base64url(answer_envelope_json)` | 排除 `|`、换行和 Markdown 分隔符 |

`answer_envelope_json` 的初版字段：

```json
{
  "version": 1,
  "document_id": "stable-id",
  "answer": "原始答案",
  "source": "knowledge_base",
  "created_at": 0
}
```

- [√] 已定义 `logical_scope` 架构：必填 `tenant`、`knowledge_base`、`corpus_version`；
  可选 `provider`、`model`、`prompt_version`。
- [√] 当前运行配置固定提供 `tenant`、`knowledge_base` 和 `corpus_version`，启动时
  会校验三者非空；不得回退为全局共享 scope。多租户请求动态选取 scope 另立阶段。
- [√] 已实现 scope 的规范化 JSON + Base64URL 编解码；tenant、knowledge_base 和
  corpus_version 为必填字段。
- [√] 已实现 question 的可逆分隔符转义；先转义反斜杠，再转义竖线。
- [√] 已实现 answer envelope 的 JSON + Base64URL 编解码，并拒绝未知 JSON 字段和
  非规范 Base64URL。
- [√] 已设置 answer 原文上限为 384 KiB，并限制最终 Base64URL payload 不超过 520 KiB；
  这会同时覆盖 JSON 转义和 Base64 膨胀，为 KVLite 默认 2 MiB 响应下的多条结果保留空间。
- [√] 已实现 `VSEARCH` 严格解析器：仅接受固定字段顺序，检查 score、编码 scope、
  编码 question 和编码 answer；任何格式错误均返回协议错误，不产生猜测性命中。
- [√] 已编写包含 `|`、`\\`、换行、Markdown 表格和中文的协议往返测试。
- [√] 已在 Go 1.22.2 环境执行 `go test ./chat-server/retrieval/kvlite`，测试通过。

## 3. 清理旧语义检索实现

此步骤不删除 `chat-context` 中的会话 KVLite/Redis 实现；只清理旧 QA 语义检索及
外部向量检索主路径。

- [√] 已删除 `chat-server/qa/kvlite_semantic.go`。
- [√] 已删除 `chat-server/qa/kvlite_semantic_ann.go`、shadow、audit 和对应 LSH 测试。
- [√] 已删除 `chat-server/qa/candidate.go` 及候选桶索引代码和测试。
- [√] 已删除旧 QA `RGET/RSET/PEXPIREAT` 存储协议及 64 维 mock embedding payload。
- [√] 已删除旧 QA 检索评估报告、命令和只适用于 LSH/brute-force 的配置项。
- [√] 已从 `server/route.go` 删除 `vectorData.QueryData()` 的检索决策；阶段 4 接入前，
  服务显式保持 API-only 并记录 `kvlite_retrieval_not_connected`。
- [√] 已确认无其他生产调用，并删除 `vectorData` 注入、`vector.InitDB()` 和
  `vectorDB` 配置。
- [√] 已将随仓旧 `KVLite/` 副本标记为非运行时依赖，并新增外部依赖说明；运行时固定使用当前
  KVLite 版本和独立部署配置。
- [√] 已更新启动与架构文档，删除“9300 旧 KVLite / mock embedding / 外部 vectorDB
  是语义主路径”的描述。

## 4. 新实现：KVLiteVectorStore

新实现放入独立目录，例如 `chat-server/retrieval/kvlite/`，不复用旧
`kvliteBackend` 的普通键接口。

```go
type Store interface {
    Upsert(ctx context.Context, doc Document) error
    Get(ctx context.Context, question string, scope LogicalScope) (*Document, error)
    Search(ctx context.Context, query string, scope LogicalScope, topK int, threshold float64) ([]Hit, error)
    Delete(ctx context.Context, question string, scope LogicalScope) error
    Health(ctx context.Context) error
}
```

- [√] 已新建独立 RESP 连接池，仅实现 `VSET`、`VGET`、`VSEARCH`、`VDEL`、`VINFO`；不复用
  chat-context 的普通键连接。
- [√] 已实现 `Upsert`：编码后发送 `VSET question answer scope`；只有 `+OK` 才视为知识写入完成。
- [√] 已实现 `Get`：编码后发送 `VGET question scope`；解码 answer envelope，`NO EXIST` 返回空结果。
- [√] 已实现 `Search`：编码 query/scope，发送 `VSEARCH query scope topK threshold`，
  严格解析为 `[]Hit`。
- [√] 已实现 `Delete`：编码后发送 `VDEL question scope`。
- [√] 已实现 `Health`：调用 `VINFO`，校验 `index_ready=1`、`index_type=core_hnsw_v2`、
  `dimension=512`、`metric=ip`。当前 KVLite 的 `VINFO` 未单列 `enabled`，成功的 `VINFO`
  由其 `vector_enabled` 前置检查保证，等价于 `enabled=1`；未来若返回该字段则显式校验。
- [√] 已实现 trace 适配器：将查询结果的 score、编码 scope、question、document ID 和来源写入
  现有 `response trace`；实际路由调用留待阶段 5。
- [√] 已将 KVLite 连接、协议、模型或索引未就绪错误规范化为稳定原因；Store 不伪造命中，
  是否回源 LLM 由路由层决定。
  LLM 由路由层决定，不伪造本地答案。

## 5. 新路由与写入流程

第一版只做“高置信命中直返，否则回源 LLM”，避免把 RAG prompt 注入和缓存语义混在
同一阶段。

```text
request
  -> KVLiteVectorStore.Get（精确）
  -> KVLiteVectorStore.Search（HNSW 语义）
  -> score >= threshold：返回知识答案
  -> 否则：调用 LLM
```

- [√] 已替换 QA preflight：只按 `Get -> Search` 调用 `KVLiteVectorStore`；精确或
  高置信 HNSW 命中直返知识答案，其余情况只回源 LLM，不注入 RAG prompt。
- [√] 已删除 `SemanticMode=ann/brute_force`、本地 embedding profile 和 LSH 阈值配置。
- [√] AI-chat 开发配置已指向独立向量 KVLite `127.0.0.1:28081`。
- [√] 已增加启动期 `VINFO` 健康检查，并要求显式配置 `reject` 或 `fallback_llm`；独立
  实例已就绪，开发配置选择 `reject`，避免服务无感退化为未检索聊天服务。
- [√] 已新建独立实例配置 `/home/zhengpan/share/Linux/KVLite/config/kvstore-ai-chat-vector.conf`，
  运行数据目录为 `data/ai-chat-vector`，RDB 为 `ai-chat-vector.rdb`；不复用旧 mock 或
  28080 的历史测试数据。
- [√] 已按源码预分配模型设置 `initial_capacity=20000`。`VINFO` 实测 HNSW 内存为
  170,448,696 字节（约 162.6 MiB）、容量 20,000；当前主机有足够余量。容量到达 70% 前
  必须扩容到新实例并迁移，不能依赖在线扩容。
- [ ] 用真实语料校准 `topK` 和 threshold；不沿用旧 LSH 的 `0.94`，也不将 KVLite
  单机 smoke 的 `0.75` 当作最终业务阈值。当前路由只消费第一名，故开发配置为
  `top_k=1`；`threshold=0.90` 仅是保守预校准保护值，未宣称已完成真实语料校准。
- [√] 已提供只读校准任务 `go run ./cmd/kvlite-calibrate`，按人工标注数据输出 `Recall@K`、
  直返 precision、coverage、负例 false-direct rate，并只在样本数不少于 100 时给出最低
  合格 threshold 建议。
- [√] 已提供内部 `ControlledWriter`：必须有审核标识，固定 `knowledge_base` 来源和
  `created_at`，按同 scope + question 精确读取实现幂等，冲突 document ID 会拒绝写入；
  未暴露自动入库或公开写接口。
- [√] 已提供人工受控任务 `go run ./cmd/kvlite-ingest`：只接收单份严格 JSON 清单，启动前
  检查 KVLite 健康状态，并固定使用 AI-chat 配置中的 logical scope。

受控入库清单示例（由审核/发布流程生成，`created_at` 为 Unix 秒）：

```json
{
  "approval_id": "review-20260817-001",
  "document_id": "account-password-reset-v1",
  "question": "如何重置密码？",
  "answer": "请在登录页点击“忘记密码”，按提示完成验证后设置新密码。",
  "created_at": 1786963200
}
```

人工确认内容后执行：

```bash
cd /home/zhengpan/share/Linux/ai_chat_kvstore/ai-chat-service
go run ./cmd/kvlite-ingest -config dev.config.yaml -input /path/to/reviewed-document.json
```

该命令不会扫描目录、不会读取聊天记录，也不会自动将 LLM 回答写入知识库。

校准集只可由已审核的真实语料生成；`expected_document_id` 为空表示明确的负例，必须回源
LLM。最小格式如下：

```json
{
  "version": 1,
  "cases": [
    {"id": "reset-1", "query": "忘记密码后怎样重新设置？", "expected_document_id": "account-password-reset-v1"},
    {"id": "negative-1", "query": "怎样申请开发者接口密钥？", "expected_document_id": ""}
  ]
}
```

```bash
go run ./cmd/kvlite-calibrate -config dev.config.yaml -dataset /path/to/reviewed-holdout.json
```

只有 holdout 集不少于 100 条、并满足 `precision >= 0.99`、负例 false-direct rate
不超过 `0.005` 时，才可将命令输出的 `recommended_threshold` 写回配置。两条 bootstrap
smoke 已验证命令和度量路径，但只输出“无建议”；其中同义问在 0.80 命中、0.85 起未直返，
不能据此替代真实语料决策。

## 6. 验收与上线

- [√] 单元测试：编码、解码、RESP parser、错误响应、连接复用、受控入库幂等与路由
  精确命中/语义命中/故障回源均已覆盖。
- [√] 协议集成测试：已对运行中的 `127.0.0.1:28080` 完成受控临时文档
  `VSET -> VGET -> VSEARCH -> VDEL` 验收；测试使用唯一 `stage5-probe-` ID 并删除该文档。
- [√] 独立实例恢复测试：审核文档写入 `127.0.0.1:28081` 后优雅停止，生成 2,589 字节 RDB；
  重启后精确读取和语义检索均恢复，`VINFO` 显示 `index_ready=1`、`capacity=20000`。
- [√] 语义测试：`TestLiveKVLiteSemanticScopeAndMarkdown` 已在 28081 运行；中文同义问
  命中正确文档，天气负例在 `threshold=0.99` 不直返，另一 tenant scope 在 `threshold=-1`
  仍无结果，且含 Markdown 表格的答案经 `VGET` 可逆读取。测试使用唯一 scope 并清理。
- [√] 恢复测试：KVLite 优雅停止并由 RDB 恢复后，审核文档的 `VGET` 和 `VSEARCH` 均可用；
  `VINFO` 验证 `index_ready=1`、`capacity=20000`。当前再次核对 28081 仍为该状态。
- [√] 降级测试：Store 测试覆盖 KVLite 不可达、`VINFO index_ready=0` 和畸形 `VSEARCH`
  响应；路由测试覆盖 `ErrUnavailable`、`ErrNotReady`、`ErrProtocol`，均不直返知识答案，
  而是保留失败原因并要求回源 LLM。开发配置为 `startup_policy=reject`，所以启动期后端
  不健康会拒绝启动；运行期故障仍按上述路由语义降级。
- [√] 压测：新增 `go run ./cmd/kvlite-benchmark`，仅走 AI-chat 的 KVLite RESP 客户端和
  28081 的 ONNX/HNSW 路径。2026-08-17 的可复现运行使用
  `KVLite/tests/fixtures/qa_zh_1000.tsv`：50 个唯一答案文档、500 个查询、
  `bge-small-zh-v1.5`、512 维、`core_hnsw_v2`、IP、`top_k=1`、`threshold=-1`、并发 4；
  结果为 Recall@1 `0.9840`、0 错误、140.90 QPS、P50/P95/P99 `28.542/34.810/38.942 ms`。
  基准独立 scope 已自动 `VDEL`，报告确认 `cleanup_completed=true`；随后 `VINFO` 为
  `active_records=1`（仅审核 bootstrap 文档）和 `backend_memory_bytes=170,449,968`
  （约 162.6 MiB）。该 fixture 是合成中文 QA 的客户端路径与吞吐信号，不能替代真实
  业务语料的命中质量与 threshold 校准。
- [√] 上线前已确认单节点边界：当前 KVLite 向量主从复制未实现，不能宣称向量高可用。
  生产运行必须定期备份独立 `ai-chat-vector.rdb`、演练部署重启恢复，并在 KVLite 不可达或
  未就绪时依赖上层回源 LLM；这些是上线运行要求，而非复制方案的替代品。

基准复跑（只写入并清理独立 benchmark scope）：

```bash
cd /home/zhengpan/share/Linux/ai_chat_kvstore/ai-chat-service
go run ./cmd/kvlite-benchmark \
  -config dev.config.yaml \
  -dataset /home/zhengpan/share/Linux/KVLite/tests/fixtures/qa_zh_1000.tsv \
  -documents 100 -queries 500 -concurrency 4 \
  -output /tmp/kvlite-stage6-benchmark.json
```

## 7. 完成定义

- [√] 生产语义检索代码仅通过 `KVLiteVectorStore` 调用当前 KVLite V 命令。
- [√] 运行时代码不再依赖外部 vectorDB、Go 本地 embedding、LSH ANN 或候选桶。
- [√] 原始 question、answer、scope 在当前 `VSEARCH` 文本返回协议下可逆且无歧义。
- [ ] 真实业务语料的质量评估和 threshold 校准尚未完成；其余 KVLite ONNX/HNSW、恢复、
  隔离、降级与合成 fixture 压测均已有可执行验证证据。
