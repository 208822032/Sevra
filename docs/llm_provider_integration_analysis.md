# LLM Provider Integration Analysis

## 结论先行

当前项目最适合被包装成一个“带会话上下文缓存的多模型 AI-chat 服务”，进一步可以演化为一个 LLM 接入层 / middleware / gateway。它不是单纯的聊天 Demo，因为仓库里已经有会话链、上下文存储、流式传输、过滤、落库、向量回溯和 KVLite 热路径；但它也还不是一个真正完成的大模型平台，因为 provider 抽象、摘要压缩闭环、取消传播、错误分层和 provider 级观测还没有真正打通。

这份分析基于当前仓库中的真实实现，重点看了以下文件：

- [ai-chat-backend/cmd/main.go](../ai-chat-backend/cmd/main.go)
- [ai-chat-backend/pkg/controllers/chat.go](../ai-chat-backend/pkg/controllers/chat.go)
- [ai-chat-backend/pkg/config/config.go](../ai-chat-backend/pkg/config/config.go)
- [ai-chat-service/chat-server/main.go](../ai-chat-service/chat-server/main.go)
- [ai-chat-service/chat-server/server/server.go](../ai-chat-service/chat-server/server/server.go)
- [ai-chat-service/chat-server/server/app.go](../ai-chat-service/chat-server/server/app.go)
- [ai-chat-service/chat-server/chat-context/backend.go](../ai-chat-service/chat-server/chat-context/backend.go)
- [ai-chat-service/chat-server/chat-context/session.go](../ai-chat-service/chat-server/chat-context/session.go)
- [ai-chat-service/pkg/config/config.go](../ai-chat-service/pkg/config/config.go)
- [ai-chat-web/src/views/chat/index.vue](../ai-chat-web/src/views/chat/index.vue)
- [ai-chat-web/src/store/modules/chat/index.ts](../ai-chat-web/src/store/modules/chat/index.ts)
- [openai-api-proxy/proxy/proxy.go](../openai-api-proxy/proxy/proxy.go)
- [mock-openai-api/main.go](../mock-openai-api/main.go)
- [tokenizer.py](../ai-chat-backend/tokenizer.py)

## 1. 当前项目现状梳理

### 1.1 服务划分

当前仓库不是单体，而是一组围绕聊天链路拆开的独立服务：

- [ai-chat-web](../ai-chat-web) 是前端，负责聊天界面、本地历史、上下文开关和流式渲染。
- [ai-chat-backend](../ai-chat-backend) 是 HTTP 网关，主要把浏览器请求转成 gRPC 请求，再把 gRPC 流式结果转回前端。
- [ai-chat-service](../ai-chat-service) 是核心编排服务，负责 session 解析、敏感词/关键词处理、token 预算、上下文回放、模型调用和落库。
- [keywords-filter](../keywords-filter) 提供敏感词和关键词服务。
- [tokenizer](../tokenizer) 负责 token 统计。
- [openai-api-proxy](../openai-api-proxy) 是 OpenAI 兼容反向代理。
- [mock-openai-api](../mock-openai-api) 是本地 mock provider。
- [KVLite](../KVLite) 是可选上下文后端，不是默认主路径。

### 1.2 用户消息如何进入系统

实际链路是：

1. 浏览器在 [ai-chat-web/src/views/chat/index.vue](../ai-chat-web/src/views/chat/index.vue) 里把用户输入发到 `/api/chat-process`。
2. 前端用 axios 的下载进度回调做“伪流式”读取，把后端 chunked JSON 持续渲染到界面上。
3. [ai-chat-backend/pkg/controllers/chat.go](../ai-chat-backend/pkg/controllers/chat.go) 接收 HTTP 请求，把 `prompt` 和 `options` 包装成 gRPC 请求。
4. backend 通过 gRPC 调用 [ai-chat-service/chat-server/server/server.go](../ai-chat-service/chat-server/server/server.go) 的 `ChatCompletionStream`。
5. service 先做敏感词和关键词判断，再构造 prompt，调用 OpenAI-compatible provider，最后把结果流式返回，并异步保存上下文和聊天记录。

### 1.3 session / conversation / room / user 的关系

当前代码里这几个概念并不完全等价：

- `user` 主要还是前端本地状态和登录 cookie 语义，后端没有完整的用户域模型。
- `room` 在后端基本没有实体化，前端的聊天列表更像本地会话槽位。
- `conversation` 在前端表现为 `history.uuid` 和 `conversationId/parentMessageId` 一类的链路标识。
- `session` 是服务端真正的状态单位，核心是 `session_id + seq + turn`。

这里有一个很重要的事实：前端和后端字段命名并不完全一致。前端更偏 ChatGPT Web 风格的 `conversationId` / `parentMessageId`，而后端和存储层更偏 `session_id` / `pid` / `seq`。当前真正串起来的桥是 `parentMessageId` 和服务端的 `resolveSession` 逻辑，而不是一个统一的 conversation/session 域模型。

### 1.4 当前已经具备的 AI 能力

仓库里已经有不少“不是 demo”的能力：

- 模型调用本身已经存在，且用的是 `go-openai` 的 `base_url + api_key` 方式，这意味着 OpenAI-compatible provider 路线已经被验证过。
- 服务端已经支持流式输出，gRPC 流再被 backend 转成 HTTP chunk。
- `chat-context` 已经有 `TurnRecord`、`SessionMeta`、`SummaryRecord` 这类结构，说明上下文不是散落在临时变量里。
- Redis / KVLite 已经抽象成 `SessionStore`，并且有 fallback 机制和 hot cache。
- MySQL 存聊天记录，VectorDB 存关键词索引，敏感词和关键词过滤也在模型调用前做了。
- 现有 metrics 已经覆盖 gRPC 请求、KVLite 读写、缓存命中、fallback、修复、summary 相关指标的定义。

### 1.5 当前还没有真正闭环的能力

这部分是关键，也是为什么现在还不能算“真正接入大模型”：

- provider 抽象没有成型，当前只有 `go-openai` 这一条硬编码调用链。
- summary 相关数据结构、缓存字段和指标存在，但从实际 ChatCompletion 主链路看，`StoreSummary` / `LoadSummary` 没有真正被主流程调用。
- backend 侧把 HTTP 请求转 gRPC 时使用了 `context.Background()`，请求取消和超时没有完整传递到下游。
- tokenizer 服务只支持少量 OpenAI 模型名，DeepSeek / Qwen 等模型如果不扩展，很容易在 token 预算上失真。
- provider 级别的指标、错误分类、token usage 和 fallback 统计还不完整。
- `/api/config` 目前还是静态占位返回，不是一个真正的 provider/model capability 描述接口。

### 1.6 现状总结

可以把现在的项目理解成：

- 传输层已经有了。
- session / turn 存储已经有了。
- provider 调用已经有了。
- 上下文缓存和 KVLite 也已经有了。

缺的是把这些能力收敛成一个清晰的 LLM 接入面，而不是“各自都能跑，但没有统一编排”。

## 2. 当前项目最适合的大模型接入方向

### 2.1 最适合的形态

我不建议把这个项目讲成“单纯的 AI 对话应用”，也不建议一上来就讲成“全功能 RAG / Agent 平台”。最贴合当前现状的形态是：

**带会话上下文缓存的多模型 AI-chat 服务，向 LLM middleware / gateway 演化。**

理由很直接：

- 前端已经是聊天应用形态。
- 服务端已经有 session / turn / summary / cache / fallback 这些中间层能力。
- provider 已经是 OpenAI-compatible 语义，天然适合接 OpenAI、DeepSeek、Qwen 这类接口。
- 现有 KVLite 相关工作本身就适合作为“会话状态存储 + 热路径优化”的技术故事。

### 2.2 真正有价值的落地点

如果真要接 OpenAI / DeepSeek / Qwen，价值不在“能聊天”本身，而在下面这些闭环：

- 会话上下文不是全量回放，而是 `summary + recent turns + current turn` 的组合。
- provider 不是写死的，而是可切换、可降级、可观测的。
- 流式输出从 provider 一直透传到前端，中间保留取消和超时语义。
- Redis / KVLite 不只是缓存，而是会话状态机的存储层。
- MySQL / VectorDB / 关键词过滤不是摆设，而是业务侧上下文治理和命中优化的一部分。

### 2.3 哪些方向现在做价值不大

以下方向不适合现在优先做：

- 直接上全量 RAG。
- 直接上工具调用 / function calling / agent loop。
- 给每个模型供应商写一套完全不同的业务代码。
- 把 embedding 检索当成当前项目主线。
- 在还没有 provider 抽象的前提下先做复杂的多租户、房间、权限、计费。

### 2.4 结论

当前项目最值得优先做的是：

1. **多模型 provider 统一抽象**。
2. **会话上下文管理 + summary 压缩 + KVLite 热路径**。
3. **流式输出、取消传播、fallback 和观测闭环**。

## 3. OpenAI / DeepSeek / 千问接入方式分析

### 3.1 三者的共同点

这三家最适合的切入方式不是各写一套 API，而是把它们都视为“OpenAI-compatible provider profile”：

- 都有聊天补全语义。
- 都可以表达 system / user / assistant messages。
- 都有流式输出语义。
- 大多数常用参数都可以映射到 `model`、`max_tokens`、`temperature`、`top_p`、`presence_penalty`、`frequency_penalty` 这一套字段。

### 3.2 三者的差异点

- **OpenAI**：作为基线实现最合适，语义最标准，适合做主 provider。
- **DeepSeek**：如果按 OpenAI-compatible 接法接入，和 OpenAI 的差异主要在 base_url、模型名、鉴权和返回节奏；业务层不应该感知太多差异。
- **Qwen / DashScope**：如果使用其 OpenAI-compatible 模式，可以走同一条 provider 抽象；如果使用原生 DashScope API，就需要单独 adapter，不建议混进主业务逻辑。

### 3.3 该怎么抽象

建议把 provider 抽象成“一个统一的聊天执行器”，而不是供应商名字的集合。核心要求是：

- chat / stream chat 是主接口。
- health check 是可选但强烈建议有。
- model info / capabilities 也建议有。
- embeddings / tool calls 先预留，不要在第一版强行做进主链路。

### 3.4 兼容层的建议

最合理的策略是：

- 用一个通用的 OpenAI-compatible provider adapter 兜住大部分场景。
- 用 provider profile 区分 OpenAI / DeepSeek / Qwen。
- 用 capability flags 标注某个 profile 是否支持 stream、tool calls、embeddings、response format 等能力。
- 如果以后需要原生 DashScope，再单独加 adapter，不要让原生 API 侵入主链路。

### 3.5 当前仓库里已有的验证基础

当前仓库已经证明了“兼容层”这条路线是可行的：

- [ai-chat-service/chat-server/server/app.go](../ai-chat-service/chat-server/server/app.go) 已经用 `go-openai` client + `base_url` 方式调用模型。
- [openai-api-proxy/proxy/proxy.go](../openai-api-proxy/proxy/proxy.go) 说明上游可以被当作 OpenAI-compatible 入口来转发。
- [mock-openai-api/main.go](../mock-openai-api/main.go) 已经能模拟 OpenAI 风格的非流式和流式响应。

### 3.6 需要特别注意的差异

- 当前 [tokenizer.py](../ai-chat-backend/tokenizer.py) 只支持少量 OpenAI 模型名，如果换到 DeepSeek / Qwen，token 统计要同步扩展。
- 有些 provider 对某些参数的支持不是完全等价的，adapter 里要做参数过滤和降级，而不是让业务层手工 if/else。
- Qwen / DashScope 如果走兼容接口，最好先做小流量验证，确认 stream chunk、finish reason 和错误码都和预期一致。

## 4. 为什么不能只停留在“调用接口”

### 4.1 会话上下文不是请求参数

LLM 接入真正难的不是 `api_key + base_url`，而是“如何把会话状态稳定地装进 prompt”。当前项目已经有 turn / meta / summary 这些结构，所以真正的工作是把它们变成可控的 prompt 组装器，而不是把消息数组直接硬塞给 provider。

### 4.2 token 预算是硬约束

当前 [tokenizer.py](../ai-chat-backend/tokenizer.py) 只覆盖少量模型名，这说明 token 预算本身就是一等公民。没有 token budget，就没有上下文裁剪、summary 触发和长回复控制。

### 4.3 streaming、取消和超时不是可选项

当前前端已经按流式交互来写了，后端也已经支持 chunked 结果。所以 LLM 接入必须把取消、超时、断连和 partial output 当成主流程能力，而不是异常分支。

### 4.4 失败要分层，而不是一把梭

LLM 失败原因至少要分成：

- 鉴权失败。
- provider 不可用。
- rate limit。
- 输入超限。
- 内容拦截。
- 网络超时。
- 协议不兼容。

不同错误对应不同动作：重试、切换 provider、降级为非流式、返回用户错误，或者直接失败，不能混在一起。

### 4.5 只有可观测，才能真的演示和压测

如果没有 provider / model / latency / token usage / cache hit / fallback 次数 / cancel 次数这些指标，LLM 接入最后只会变成“看起来能跑”。而当前项目已经有 metrics 的基础，所以应该把它补成真实闭环。

### 4.6 只有可测试，才能说是真接入

真正能交付的 LLM 接入应该能做到：

- 不依赖真实 API key 也能测。
- mock provider 和真实 provider 的协议一致。
- prompt builder、context manager、fallback policy 都有单测。
- benchmark 脚本能覆盖真实链路，而不是只测一个 HTTP endpoint。

## 5. 建议的 provider 抽象与模块设计

### 5.1 核心接口建议

建议把 provider 抽象成下面这种最小接口，而不是把所有未来能力一口气塞进去：

```go
type Provider interface {
    Name() string
    Capabilities() CapabilitySet
    Health(ctx context.Context) error
    Chat(ctx context.Context, req ChatRequest) (*ChatResult, error)
    ChatStream(ctx context.Context, req ChatRequest) (ChatStream, error)
}
```

`ChatRequest` 里建议只放当前真正需要的字段：

- `Model`
- `Messages`
- `MaxTokens`
- `Temperature`
- `TopP`
- `PresencePenalty`
- `FrequencyPenalty`
- `Stream`
- `Timeout`
- `RequestID`
- `SessionID`

`CapabilitySet` 里先放这些就够了：

- `SupportsStreaming`
- `SupportsToolCalls`
- `SupportsEmbeddings`
- `SupportsHealthCheck`
- `SupportsModelList`

### 5.2 模块边界建议

建议把当前 service 侧拆成下面几层：

- `chat-server/provider`：只负责 provider adapter 和 registry。
- `chat-server/prompt`：只负责 prompt 组装、summary 合并和 token 预算。
- `chat-server/chat-context`：继续负责 turn / meta / summary 的存储和回放。
- `chat-server/orchestrator` 或继续放在 `chat-server/server`：负责过滤、上下文加载、provider 调用、保存上下文和流式转发。
- `chat-server/telemetry`：负责 provider、context、fallback、summary 的观测。

### 5.3 需要做的，不需要过度设计的

必须做：

- provider 统一抽象。
- provider profile / model catalog。
- 错误分类。
- token 预算控制。
- 取消传播。
- fallback 策略。

暂时不要做：

- 统一 embeddings 和 chat 的大而全接口。
- 把工具调用、RAG、agent 逻辑强行塞进第一版。
- 为每个供应商写完全独立的业务路径。

### 5.4 为什么这里不建议用“只配置 base_url”就结束

当前代码已经证明 `base_url` 能跑通，但这只解决了传输层，不解决这些问题：

- provider 能力差异。
- model 名称差异。
- 错误分类。
- fallback。
- token 统计。
- 可观测性。

所以真正正确的设计是“通用 provider 抽象 + OpenAI-compatible profiles”，而不是“把 OpenAI 的 base_url 改成别家地址”。

## 6. 上下文缓存 / summary / KVLite 的接入方案

### 6.1 当前已经存在的上下文模型

当前 `chat-context` 已经不是一团散数据，而是明确的三层模型：

- `TurnRecord`：一轮对话的完整状态，包含 user / assistant message、tokens、status、seq、message id、时间戳。
- `SessionMeta`：会话级轻量状态，包含 `HeadSeq`、`LastSeq`、`TurnCount`、`LastMessageID`、`SummarySeq` 等。
- `SummaryRecord`：摘要压缩结果，包含压缩范围、摘要文本、摘要 tokens、来源 turn 数和压缩模式。

这套模型已经很适合 LLM 会话了，关键不是推倒重来，而是把它真正接入 prompt builder。

### 6.2 当前存储层分别承担什么

- Redis / KVLite：会话 turn、meta、summary、消息 id 索引等热路径状态。
- MySQL：聊天记录落库和历史审计。
- VectorDB：当前更多是关键词命中后的近似问答回退，不是完整 RAG。
- 前端 localStorage：UI 历史和会话列表，不是服务端真相源。

### 6.3 适合 LLM 的缓存键设计

当前代码里的 key 设计已经很接近可用形态，建议继续沿用并稳定下来：

- `namespace:sess:{session_id}:turn:{seq}`：单轮 turn。
- `namespace:sess:{session_id}:meta`：会话 meta。
- `namespace:sess:{session_id}:summary`：摘要结果。
- `namespace:compat:msg:{message_id}`：请求消息 id -> session 映射。
- `namespace:compat:pid:{message_id}`：父消息 id -> session 映射。

其中 `namespace` 继续保留，是为了隔离环境、租户或者不同实验线。

### 6.4 会话读取和 prompt 回放应该怎么做

推荐的 prompt 组装顺序是：

1. system prompt。
2. summary message（如果存在）。
3. 最近 `N` 轮 completed turns。
4. 当前 user message。

这里要特别注意：summary 只能压缩旧内容，不能覆盖最近轮次原文。

### 6.5 summary 应该什么时候触发

建议 summary 是“写后异步压缩”，而不是“请求时同步压缩”。触发条件可以是下面几类之一：

- 已完成的 turn 数超过阈值。
- prompt token 接近上限。
- 最近窗口已经超过 summary 预算。
- 会话长时间活跃，且前缀内容明显可压缩。

summary 的写入建议发生在 assistant response 完整落地之后，这样摘要才是对完整 turn 的压缩，而不是半轮对话。

### 6.6 summary 里应该存什么

建议只存这些东西：

- 压缩后的摘要文本。
- 被压缩的 turn 范围。
- 摘要对应的最后 seq。
- 摘要 token 数。
- 压缩模式。

不要把 summary 做成“另一个完整历史副本”，否则只会把存储和调试复杂度重新做一遍。

### 6.7 KVLite 的真正切入点

如果要讲“KVLite 替代 Redis 做 AI-chat 上下文缓存”，真正值得展示的不是“能不能存 KV”，而是下面这些点：

- `session_id + seq` 的 turn 模型。
- 最近窗口读写。
- session meta 快速定位。
- open turn -> completed turn 的状态机写法。
- write-heavy / hot-session 场景下的性能收益。

不适合强行放进 KVLite 的内容：

- embedding 向量。
- 大段原始日志。
- 训练/分析用的离线数据集。
- 复杂的跨会话搜索结果。

### 6.8 现有代码里 summary 为什么还不能算闭环

当前 `chat-context` 里 summary 的结构、缓存和指标已经存在，但主链路里没有真正把 summary 生成和读取接上。也就是说：

- 数据结构在。
- 指标在。
- 读取函数在。
- 但 summary 生成与主请求路径的联动还没真正完成。

这正是“有架子但没闭环”的典型状态。

### 6.9 KVLite 该怎么做实验

建议的实验维度不是只看数据库吞吐，而是看端到端会话表现：

- `read_heavy`：偏重 recent turns 回放。
- `hot_sessions`：少量 session 高频读写。
- `write_heavy`：open / complete / meta 更新更密集。

评估指标建议包括：

- QPS。
- p50 / p95 / p99 延迟。
- cache hit 率。
- fallback 次数。
- prompt rebuild latency。
- 总请求失败率。

当前仓库里已经有 Redis vs KVLite 的 benchmark 框架，这一点非常适合作为项目亮点。

## 7. streaming / 容错 / fallback 设计

### 7.1 当前 streaming 路径是什么

当前路径是：

- provider 侧输出 chunk。
- service 把 chunk 转成 gRPC stream response。
- backend 把 gRPC stream 再转成 HTTP chunked JSON。
- 前端用下载进度事件逐条解析 JSON。

这条链路本身已经足够支撑真正的流式回答，不需要一上来就重写成别的协议。

### 7.2 最小正确方案里建议保留什么

建议保留当前的 chunked JSON 方式，原因很简单：

- 前端已经适配好了。
- backend 和 service 都已经在用。
- 改动最小，风险最低。

如果以后想把浏览器侧协议做得更标准，可以再考虑 SSE，但不是第一优先级。

### 7.3 取消传播应该怎么做

当前 backend 使用 `context.Background()` 传给下游 gRPC 调用，这是一个真正需要补的点。更合理的方式是把 HTTP 请求上下文一路传下去：

- 浏览器 abort -> backend request context cancel。
- backend 取消 gRPC 调用。
- service 取消 provider stream。
- provider 客户端关闭连接。

这样才能让“用户点停止”真的生效，而不是只是前端停显示。

### 7.4 容错和 fallback 的正确顺序

建议的错误处理顺序是：

1. 输入检查失败，直接返回。
2. 敏感词 / 业务过滤命中，直接返回业务提示，不调用 provider。
3. provider 网络错误 / 429 / 5xx，触发重试或 fallback。
4. provider 鉴权错误 / 请求格式错误 / 内容拦截错误，不做盲目 fallback。
5. provider stream 中途失败，视情况返回部分结果、切换为非流式，或者直接失败。

fallback 建议是显式配置的，不要偷偷切换，以免调试和计费都混乱。

### 7.5 非流式兜底要不要留

建议保留。原因是：

- 它可以作为测试和故障排查兜底。
- provider 流式失败时可以作为恢复策略之一。
- 对某些批处理场景，非流式更容易做断言。

### 7.6 最近一次成功结果要不要缓存

不建议把“最近一次成功回答”当成通用缓存策略。对自由对话来说，答案高度依赖上下文，盲目缓存只会污染结果。

如果要做缓存，只建议做两类：

- 对完全相同、且上下文可证明一致的请求做短期缓存。
- 对检索型、可重复且确定性的业务问答做显式缓存。

## 8. 最小正确接入方案

### 8.1 目标

在不过度重构的前提下，让当前项目真正能接入至少 1~3 家模型提供方，并具备基本可用性、可切换性和会话上下文能力。

### 8.2 建议改动点

**服务端配置层**

- 扩展 [ai-chat-service/pkg/config/config.go](../ai-chat-service/pkg/config/config.go) 的 provider 配置。
- 增加 provider profile、默认 provider、fallback provider、超时和重试参数。

**provider 层**

- 在 [ai-chat-service/chat-server](../ai-chat-service/chat-server) 下增加 provider adapter 和 registry。
- 默认实现一个 OpenAI-compatible provider。
- 为 DeepSeek 和 Qwen 只配置 profile，不分叉业务代码。

**prompt/context 层**

- 在 [ai-chat-service/chat-server/server/app.go](../ai-chat-service/chat-server/server/app.go) 里把 summary 读取、recent turns、system prompt 组装成一个独立 prompt builder。
- 让 summary 进入真正的 prompt 回放路径。

**传输层**

- 修复 [ai-chat-backend/pkg/controllers/chat.go](../ai-chat-backend/pkg/controllers/chat.go) 的 request context 传播。
- 保持现有 chunked JSON 流式协议，不强迫前端重写。

**token 预算层**

- 扩展 [tokenizer.py](../ai-chat-backend/tokenizer.py) 或者把 token 估算抽象到 provider 侧。
- 至少让 DeepSeek / Qwen 的模型名不至于直接 NotImplemented。

**观测层**

- 给 provider 增加 latency、error class、usage、fallback、cancel 等指标。
- 给 prompt builder 增加 summary hit / miss / trigger / bypass 等指标。

### 8.3 数据流应该怎么走

1. 前端发 prompt 和上下文开关。
2. backend 仅负责协议转换和流式转发。
3. service 解析 session，做敏感词 / 关键词治理。
4. prompt builder 从 turn / meta / summary 组装上下文。
5. provider adapter 根据 profile 调用 OpenAI / DeepSeek / Qwen。
6. 流式 chunk 逐段返回前端。
7. 成功后异步保存 turn、chat_records、必要的 summary。

### 8.4 这版先不要做什么

- 不做 embeddings / RAG。
- 不做多-agent。
- 不做工具调用编排。
- 不做复杂的多房间多租户权限系统。
- 不把 provider 逻辑下沉到 backend。

## 9. 理想完成度方案

### 9.1 理想模块边界

如果后续要做成一个完整且可讲清楚的项目，建议模块边界收敛成下面这样：

- `provider`：OpenAI / DeepSeek / Qwen 等适配器。
- `prompt`：system prompt、summary、recent turns、预算控制。
- `context`：turn / meta / summary / hot cache / KVLite / Redis。
- `orchestrator`：过滤、路由、fallback、保存、重试。
- `telemetry`：metrics、trace、log redaction。
- `benchmark`：Redis vs KVLite、provider mock、真实 provider 压测。

### 9.2 理想数据结构

理想状态下，项目里至少要保留下面这些核心结构：

- `TurnRecord`：单轮原子状态。
- `SessionMeta`：会话 cursor 和轻状态。
- `SummaryRecord`：压缩前缀。
- `ProviderProfile`：provider 配置与能力。
- `CapabilitySet`：能力声明。
- `ChatRequest` / `ChatResult`：内部统一请求和结果。
- `FallbackPolicy`：容错策略。

这套结构的核心思想是：

- 原始事实来源只保留一份。
- prompt 可以重建。
- summary 只是压缩层，不是新的事实源。

### 9.3 理想的上下文策略

最理想的长会话策略不是“越存越多”，而是：

- 最近窗口保留原文。
- 更早的内容进入 summary。
- 需要时可以继续查 turn 原文。
- 不同 provider 用同一个上下文策略，只在参数上做适配。

### 9.4 理想的观测和评估口径

建议最终在项目里固定下面几类指标：

- provider latency。
- token usage。
- summary trigger / hit / miss / bypass。
- context rebuild latency。
- cache hit rate。
- fallback rate。
- cancel rate。
- end-to-end request p50 / p95 / p99。
- Redis vs KVLite 在不同 workload 下的差异。

### 9.5 理想方案和 RAG / tools 的边界

理想完成度不等于“把所有 LLM 常见能力都塞进去”。当前仓库最合理的演进顺序还是：

1. 多 provider。
2. 上下文压缩。
3. 流式和 fallback。
4. 指标和 benchmark。
5. 如果后面真的有文档问答、知识库检索或工具执行需求，再单独引入 embeddings / RAG / tools。

## 10. 面试价值与项目包装建议

### 10.1 最适合对外讲成什么项目

如果按上面的方案真正落地，最适合对外讲成：

**基于 Redis / KVLite 会话缓存的多模型 AI-chat 服务，兼具 OpenAI-compatible provider 接入、上下文压缩、流式输出和 fallback 能力。**

这个说法比“我接了一个接口”强很多，因为它表达的是完整工程链路，而不是单点对接。

### 10.2 真正能打动面试官的亮点

真亮点应该是这些：

- provider 抽象做对了，而不是把 base_url 写死。
- session / turn / summary 这套上下文模型是真的能支撑长对话。
- KVLite 不是口头替代 Redis，而是有真实 workload 和 benchmark 对比。
- streaming、取消、fallback、超时都能跑通，不只是返回一个 JSON。
- 有 metrics / benchmark / mock provider，说明你是按工程闭环做的。

### 10.3 不是亮点的东西

以下内容单独拿出来都不够强：

- 只改一个 `base_url`。
- 只把一个 mock provider 接上去。
- 只做一个简单聊天页面。
- 只做静态代理转发。

### 10.4 面试里的回答方式

比较好的讲法是：

> 我没有把 LLM 当成一个“发请求的黑盒”，而是把它当成一个有 session、有上下文预算、有压缩需求、有 fallback 和观测需求的长期状态系统。我的核心工作是把 provider、上下文、传输和存储解耦，让同一套会话层可以切换 OpenAI / DeepSeek / Qwen，并且能在 Redis / KVLite 之间做真实 benchmark 对比。

这个回答的技术密度足够高，也符合当前代码实际。

## 11. 建议优先级排序

### 11.1 第一优先级

**provider 统一抽象 + 配置化 profile + 取消传播。**

原因：这是把“能调用接口”变成“能接多家模型且不改业务”的最小门槛。

### 11.2 第二优先级

**上下文回放 + summary 压缩 + tokenizer 兼容。**

原因：这是把项目从“短对话”提升到“真正可用的长会话”的关键。

### 11.3 第三优先级

**观测、fallback、benchmark。**

原因：这是证明方案不是 demo、也不是拍脑袋设计的关键。

### 11.4 第四优先级

**如果后面真的有业务需要，再上 embeddings / RAG / tools。**

原因：这些能力很重要，但它们不是当前仓库最先应该补的那一层。

### 11.5 最终简短结论

- 优先接入的顺序：先做 OpenAI-compatible 统一层，再落 OpenAI 和 DeepSeek，Qwen 作为第二阶段的兼容 profile。
- 最值得优先做的两个能力：provider 抽象、上下文压缩。
- 最能写进简历并经得起追问的两个点：多 provider 统一抽象 + fallback，KVLite-backed session context benchmark。
