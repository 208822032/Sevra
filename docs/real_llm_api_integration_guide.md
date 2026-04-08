# 真实 LLM API 接入指南

这份文档是给当前仓库的实操版接入说明，基于 [llm_provider_integration_analysis.md](llm_provider_integration_analysis.md) 和 [kvlite_ai_chat_retrieval_evolution.md](kvlite_ai_chat_retrieval_evolution.md) 进一步收敛成“照着做就能接上”的步骤。

它只讲当前仓库真实支持的接法，不假设已经切到新的 API 体系。

## 1. 当前仓库的接入链路

当前链路是：前端选择 provider/model，backend 只负责把这个选择透传给 service，真正的模型调用发生在 service 侧的 provider adapter。

1. 前端在 [ai-chat-web/src/views/chat/index.vue](../ai-chat-web/src/views/chat/index.vue) 里通过 `/api/config` 拉取 catalog，通过模型选择器让用户选 provider/model。
2. 前端发起聊天时，`fetchChatAPIProcess` 会把 `provider` 和 `model` 一起带到 `/api/chat-process`，同时还会带上 `prompt`、`options` 和 `systemMessage`，见 [ai-chat-web/src/api/index.ts](../ai-chat-web/src/api/index.ts)。
3. backend 的 [ai-chat-backend/pkg/controllers/chat.go](../ai-chat-backend/pkg/controllers/chat.go) 在 `ChatProcess` 里把前端选中的 `provider/model` 填到 gRPC 请求的 `ChatParam` 中，再转发给 `ai-chat-service`。
4. service 的 [ai-chat-service/chat-server/server/server.go](../ai-chat-service/chat-server/server/server.go) 会先构建 `provider.Registry`，再用 `ResolveSelection` 决定实际使用哪个 provider 和哪个 model。
5. 真正发请求的是 [ai-chat-service/chat-server/provider/openai_compatible.go](../ai-chat-service/chat-server/provider/openai_compatible.go)，它用 `go-openai` 客户端把请求发到 `base_url` 对应的 OpenAI-compatible endpoint。

要特别说明一点：本仓库里没有单独的 `provider/profile/registry` 物理子目录；这些职责现在分别落在下面几个文件里：

- [ai-chat-service/chat-server/provider/types.go](../ai-chat-service/chat-server/provider/types.go)：ProviderProfile、Registry、选择逻辑、能力定义。
- [ai-chat-service/chat-server/provider/catalog.go](../ai-chat-service/chat-server/provider/catalog.go)：catalog 输出、默认 provider、model 列表。
- [ai-chat-service/chat-server/provider/openai_compatible.go](../ai-chat-service/chat-server/provider/openai_compatible.go)：OpenAI-compatible adapter。

backend 和 service 目前是两份独立配置。前端拿到的 catalog 来自 backend 的配置，实际调用模型用的是 service 的配置。所以要保证 provider name、model 列表、默认顺序在两边一致；`api_key` 和 `base_url` 只对 service 侧的真实调用有意义。

## 2. 为什么当前最适合走统一 provider/profile

当前代码已经把“不同厂商”收敛成了统一的 provider/profile 模型，因此最稳妥的接法就是统一走 OpenAI-compatible。

- [ai-chat-service/chat-server/provider/types.go](../ai-chat-service/chat-server/provider/types.go) 里的 `ProviderProfile` 已经把 `name`、`kind`、`base_url`、`api_key`、`default_model`、`models`、`supports_stream`、`enabled`、`priority` 等字段统一起来了。
- [ai-chat-service/chat-server/provider/catalog.go](../ai-chat-service/chat-server/provider/catalog.go) 会把 profile 变成前端能读的 catalog，前端只需要展示 provider/model，不需要知道每家厂商的底层差异。
- [ai-chat-service/chat-server/server/app.go](../ai-chat-service/chat-server/server/app.go) 在 `newApp` 里会调用 `registry.ResolveSelection(requestedProvider, requestedModel, stream)`，所以选中的 provider/model 和最终实际使用的 provider/model 可以区分开来。
- [ai-chat-backend/pkg/controllers/chat.go](../ai-chat-backend/pkg/controllers/chat.go) 会把 `selectedProvider/selectedModel` 透传到 service，前端还能从 trace 里看到 `selected_*` 和 `actual_*` 的差异。

这就是为什么当前仓库优先走 OpenAI-compatible 统一接法：

1. 一条代码路径同时覆盖 OpenAI、千问、DeepSeek。
2. 前端 catalog 和后端实际调用可以共享同一套 profile 结构。
3. fallback、alias、enabled/priority、stream 支持这些差异都能放进 profile，不需要为每家写一整套业务分支。

## 3. 如何真正通过 API 接入 OpenAI

### 3.1 需要什么

OpenAI 这条路径需要三样东西：

- API Key。
- OpenAI-compatible base URL，通常是 `https://api.openai.com/v1`。
- 你账号里真实可用的 model 名称。

### 3.2 配置放哪里

当前仓库里真正会影响运行的地方有两份：

- backend 配置：`ai-chat-backend/dev.config.yaml` 或 `ai-chat-stack/configs/ai-chat-backend.yaml`。
- service 配置：`ai-chat-service/dev.config.yaml` 或 `ai-chat-stack/configs/ai-chat-service.yaml`。

如果你只想让 UI 正确显示模型列表，backend 配置里的 `providers` 列表必须存在并且和 service 对齐。
如果你想让请求真的打到 OpenAI，service 配置里的 `providers` 列表必须带上真实的 `api_key` 和 `base_url`。

### 3.3 请求如何发出去

真实请求路径是：

`前端 -> /api/chat-process -> backend 透传 provider/model -> service ResolveSelection -> OpenAICompatibleProvider.Chat 或 ChatStream -> go-openai CreateChatCompletion* -> OpenAI endpoint`

对应代码分别在：

- 前端请求拼装：[ai-chat-web/src/api/index.ts](../ai-chat-web/src/api/index.ts)
- backend 转发：[ai-chat-backend/pkg/controllers/chat.go](../ai-chat-backend/pkg/controllers/chat.go)
- service 选型：[ai-chat-service/chat-server/server/app.go](../ai-chat-service/chat-server/server/app.go)
- 真实调用：[ai-chat-service/chat-server/provider/openai_compatible.go](../ai-chat-service/chat-server/provider/openai_compatible.go)

### 3.4 最小配置示例

说明：当前仓库没有 `api_key_env` 这个解析字段，下面把环境变量名写成注释，只是为了说明密钥来源。`viper` 当前只会读取已经落盘的 YAML，不会自动展开 `${ENV}`。

```yaml
providers:
  - name: openai
    kind: openai-compatible
    base_url: https://api.openai.com/v1
    # api_key_env: OPENAI_API_KEY
    api_key: "由 OPENAI_API_KEY 在部署层渲染"
    default_model: gpt-4o-mini
    models:
      - gpt-4o-mini
      - gpt-4.1-mini
    supports_stream: true
    supports_non_stream: true
    enabled: true
    priority: 300
    context_window: 128000
```

如果你现在还想保留旧的单模型配置，`chat.api_key`、`chat.base_url`、`chat.model` 仍然能跑；但只要你想做多 provider，就建议直接改成 `providers` 列表。

## 4. 如何真正通过 API 接入千问

### 4.1 千问的正确接法

千问这条路径也应该走 OpenAI-compatible 接法，不建议直接把原生 DashScope API 混进当前主链路。

当前仓库的 provider adapter 只实现了 OpenAI-compatible 的 Chat Completions 风格，所以千问要接进来，应该使用它的 compatible-mode endpoint，而不是重新写一条完全不同的业务路径。

### 4.2 需要替换哪些配置

千问接入时，通常只替换这三类信息：

- `api_key`：换成你自己的千问密钥。
- `base_url`：换成千问的 OpenAI-compatible endpoint。
- `default_model` / `models`：换成你账号里实际可用的千问模型名。

### 4.3 哪些地方和 OpenAI 共用同一条代码路径

千问和 OpenAI 在当前仓库里共用同一条代码路径：

- 都通过 `kind: openai-compatible` 进入 [ai-chat-service/chat-server/provider/openai_compatible.go](../ai-chat-service/chat-server/provider/openai_compatible.go)。
- 都由 [ai-chat-service/chat-server/provider/types.go](../ai-chat-service/chat-server/provider/types.go) 里的 profile 描述能力。
- 都由 [ai-chat-service/chat-server/provider/catalog.go](../ai-chat-service/chat-server/provider/catalog.go) 生成 catalog。
- 都由 [ai-chat-service/chat-server/server/app.go](../ai-chat-service/chat-server/server/app.go) 的 `ResolveSelection` 负责最终选择。

### 4.4 注意事项

- base URL 必须是 OpenAI-compatible 的完整 endpoint，通常应带 `/v1`。
- 当前 adapter 会 `TrimSuffix("/")`，但不会替你补 `/v1`，所以不要只填域名根路径。
- 当前仓库是 Chat Completions 风格，不是原生 DashScope SDK 风格；如果你改成原生接口，就需要新增 adapter。

### 4.5 最小配置示例

```yaml
providers:
  - name: qwen
    kind: openai-compatible
    base_url: https://dashscope.aliyuncs.com/compatible-mode/v1
    # api_key_env: QWEN_API_KEY
    api_key: "由 QWEN_API_KEY 在部署层渲染"
    default_model: qwen-flash
    models:
      - qwen-flash
      - qwen-plus
    supports_stream: true
    supports_non_stream: true
    enabled: true
    priority: 200
    context_window: 128000
```

上面的 `qwen-flash` 是默认示例，`qwen-plus` 是可选模型；你应该以自己账号里当前可用的模型名为准。

## 5. 如何真正通过 API 接入 DeepSeek

### 5.1 DeepSeek 的正确接法

DeepSeek 也应该走 OpenAI-compatible 接法。

对当前仓库来说，DeepSeek 不是特殊分支，而是一个 profile 变化：同样的 Chat Completions 调用，同样的流式语义，只是 `api_key`、`base_url` 和模型名不同。

### 5.2 需要替换哪些配置

- `api_key`：换成 DeepSeek 的密钥。
- `base_url`：换成 DeepSeek 的 OpenAI-compatible endpoint。
- `default_model` / `models`：换成你账号可用的 DeepSeek 模型名。

### 5.3 哪些地方共用同一条代码路径

和 OpenAI 一样，DeepSeek 共用：

- [ai-chat-service/chat-server/provider/openai_compatible.go](../ai-chat-service/chat-server/provider/openai_compatible.go)
- [ai-chat-service/chat-server/provider/types.go](../ai-chat-service/chat-server/provider/types.go)
- [ai-chat-service/chat-server/provider/catalog.go](../ai-chat-service/chat-server/provider/catalog.go)
- [ai-chat-service/chat-server/server/app.go](../ai-chat-service/chat-server/server/app.go)

### 5.4 最小配置示例

```yaml
providers:
  - name: deepseek
    kind: openai-compatible
    base_url: https://api.deepseek.com/v1
    # api_key_env: DEEPSEEK_API_KEY
    api_key: "由 DEEPSEEK_API_KEY 在部署层渲染"
    default_model: deepseek-chat
    models:
      - deepseek-chat
      - deepseek-reasoner
    supports_stream: true
    supports_non_stream: true
    enabled: true
    priority: 250
    context_window: 128000
```

这里同样建议把 `models` 只写你当前账号真实可用的模型。

## 6. 统一 provider profile 配置示例

下面是一份可以同时表达 OpenAI、千问、DeepSeek 的统一 profile 示例。这里的 `api_key_env` 只是部署层提示，不是当前仓库的解析字段。

```yaml
providers:
  - name: openai
    kind: openai-compatible
    base_url: https://api.openai.com/v1
    # api_key_env: OPENAI_API_KEY
    api_key: "由 OPENAI_API_KEY 在部署层渲染"
    default_model: gpt-4o-mini
    models:
      - gpt-4o-mini
      - gpt-4.1-mini
    supports_stream: true
    supports_non_stream: true
    enabled: true
    priority: 300
    context_window: 128000

  - name: qwen
    kind: openai-compatible
    base_url: https://dashscope.aliyuncs.com/compatible-mode/v1
    # api_key_env: QWEN_API_KEY
    api_key: "由 QWEN_API_KEY 在部署层渲染"
    default_model: qwen-flash
    models:
      - qwen-flash
      - qwen-plus
    supports_stream: true
    supports_non_stream: true
    enabled: true
    priority: 200
    context_window: 128000

  - name: deepseek
    kind: openai-compatible
    base_url: https://api.deepseek.com/v1
    # api_key_env: DEEPSEEK_API_KEY
    api_key: "由 DEEPSEEK_API_KEY 在部署层渲染"
    default_model: deepseek-chat
    models:
      - deepseek-chat
      - deepseek-reasoner
    supports_stream: true
    supports_non_stream: true
    enabled: true
    priority: 250
    context_window: 128000
```

### 6.1 这个示例里每个字段的含义

- `name`：前端和 backend/service 都会拿它当 provider 标识。
- `kind`：当前仓库已实现的主路径是 `openai-compatible`。
- `base_url`：实际请求的 OpenAI-compatible endpoint，服务端 adapter 会直接拿它创建 client。
- `api_key`：真实鉴权密钥。不要把它误解成前端字段。
- `default_model`：该 provider 默认使用的模型名。
- `models`：允许前端展示和选择的模型列表。
- `supports_stream`：当前 UI 主要依赖流式响应，所以实际对话模型最好保持 `true`。
- `enabled`：是否进入 registry。
- `priority`：catalog 排序和默认 provider 选择会用到。
- `context_window`：用于预算和上下文截断。

### 6.2 backend 和 service 要不要都写同一份

建议尽量保持同一份 provider 列表，只是在密钥上做区分：

- backend 需要 catalog 能展示正确的 provider/model，所以 provider 名称、模型列表、优先级、默认值应该和 service 一致。
- service 需要真实调用，所以 `api_key` 和 `base_url` 必须是有效值。

如果你不想把真正的密钥写进 backend 配置，也可以让 backend 只保留 catalog 所需的最小字段，但前后端的 provider 名称和模型名必须一致，否则前端会出现“能选到但服务端不认”的情况。

## 7. 新增 provider 时到底该改哪里

### 7.1 如果还是 OpenAI-compatible

如果新 provider 也是 OpenAI-compatible，而且请求/响应语义和当前 adapter 一样，通常只需要补 profile，不需要改业务代码。

也就是说，下面这些场景通常只改配置就够了：

- 新增一个 OpenAI-compatible 账号。
- 切换 base_url。
- 更换模型名。
- 调整默认 provider 顺序。
- 通过 `model_aliases` 做模型别名映射。

### 7.2 什么时候需要新增 adapter

如果 provider 不是 OpenAI-compatible，或者虽然兼容但在请求参数、返回格式、流式协议、错误语义上都不一致，就应该在 [ai-chat-service/chat-server/provider/](../ai-chat-service/chat-server/provider/) 下新增 adapter。

典型需要新增 adapter 的情况：

- 需要原生 SDK，而不是 Chat Completions 风格。
- 鉴权方式不是简单 API Key。
- 返回体不是 OpenAI-compatible 的 stream chunk。
- 需要特殊的模型映射、工具调用、响应格式或多轮协议。

### 7.3 什么时候只改配置就够了

只要满足下面条件，一般就只改配置：

- 该 provider 能按 OpenAI-compatible endpoint 收请求。
- `kind` 仍然是 `openai-compatible`。
- `supports_stream` / `supports_non_stream` 的能力和实际一致。
- 只需要更换密钥、base_url、模型名、优先级、别名。

### 7.4 需要看的扩展点目录/文件

- [ai-chat-service/chat-server/provider/types.go](../ai-chat-service/chat-server/provider/types.go)：profile、registry、model alias、能力位。
- [ai-chat-service/chat-server/provider/catalog.go](../ai-chat-service/chat-server/provider/catalog.go)：catalog 输出和默认选择。
- [ai-chat-service/chat-server/provider/openai_compatible.go](../ai-chat-service/chat-server/provider/openai_compatible.go)：统一调用 adapter。
- [ai-chat-service/pkg/config/config.go](../ai-chat-service/pkg/config/config.go)：service 读取配置。
- [ai-chat-backend/pkg/config/config.go](../ai-chat-backend/pkg/config/config.go)：backend 读取配置。
- [ai-chat-backend/pkg/config/catalog.go](../ai-chat-backend/pkg/config/catalog.go)：backend 的 catalog 输出。
- [ai-chat-backend/pkg/controllers/chat.go](../ai-chat-backend/pkg/controllers/chat.go)：把前端选择透传到 service。
- [ai-chat-service/chat-server/server/app.go](../ai-chat-service/chat-server/server/app.go)：最终选择实际 provider/model。

## 8. 我如何联调和排错

下面按可执行顺序写，默认你已经能访问 Redis、MySQL、VectorDB、tokenizer、keywords-filter，以及你选择的 OpenAI-compatible 上游。

### 8.1 配环境变量

先准备你自己的密钥变量，例如：

- `OPENAI_API_KEY`
- `QWEN_API_KEY`
- `DEEPSEEK_API_KEY`

如果你不是在模板层渲染配置，而是直接写 YAML，那就把 `api_key` 写成最终值；当前仓库不会自动展开 `${ENV}`。

### 8.2 配 provider profile

把 backend 和 service 的 provider 列表配好，至少要保证：

- `name` 一致。
- `default_model` 一致。
- `models` 一致。
- `enabled` 和 `priority` 的预期一致。

实际调用相关的 `base_url` 和 `api_key` 以 service 配置为准。

如果你在 Swarm/Compose 里跑，参考 [ai-chat-startup-guide.md](ai-chat-startup-guide.md) 里对 `ai-chat-service` 和 `ai-chat-backend` 的启动说明，分别对应：

- `go run ./chat-server --config=dev.config.yaml`
- `go run ./cmd --config=dev.config.yaml`

### 8.3 启动 ai-chat-service

使用 service 的配置文件启动核心服务。

```bash
go run ./chat-server --config=dev.config.yaml
```

如果你的 config 里已经配好了 `providers`，service 会先构建 registry，再根据前端传来的 provider/model 做选择。

### 8.4 启动 ai-chat-backend

```bash
go run ./cmd --config=dev.config.yaml
```

backend 会暴露 `/api/config`、`/api/models` 和 `/api/chat-process`。

### 8.5 打开前端

开发态可以直接跑 `ai-chat-web`：

```bash
pnpm dev
```

如果你要验证“真实运行链路”是否生效，建议先执行一次 build/sync，把前端静态 bundle 同步到 backend 的 `www/` 目录，然后直接访问 backend 根路径。你已经有现成脚本：`./scripts/build_and_sync_frontend.sh`。

### 8.6 拉模型列表接口

先看 catalog 是否正确：

- `GET /api/config`
- `GET /api/models`

这两个接口在 backend 里都走同一个 `Config` handler，返回的 `data` 就是 `ProviderCatalog`。

你要重点看这些字段：

- `selectedProvider`
- `selectedModel`
- `defaultProvider`
- `defaultModel`
- `providers[*].enabled`
- `providers[*].default_model`
- `providers[*].models[*].model`

### 8.7 选择模型发送请求

在前端里选好 provider/model 后，`fetchChatAPIProcess` 会把这两个字段一并带到 `/api/chat-process`。

发出去的请求里，你应该能看到：

- `provider`
- `model`
- `prompt`
- `options`
- `systemMessage`

如果前端选了模型，但后端没有按你期望的模型发出去，先比对 backend 的 catalog 和 service 的 config 是否一致，再看 service 里 `ResolveSelection` 是否触发了 fallback。

### 8.8 看 trace / usage / budget 是否正确

流式响应结束后，backend 会从 gRPC trailer 里取出三类元信息，键名分别是：

- `x-ai-trace`
- `x-ai-usage`
- `x-ai-budget`

backend 端的 [ai-chat-backend/pkg/controllers/chat.go](../ai-chat-backend/pkg/controllers/chat.go) 会把这些 trailer 解析成最终响应里的 `trace`、`usage` 和 `budget`。

你要重点检查：

- `trace.selected_provider`
- `trace.selected_model`
- `trace.actual_provider`
- `trace.actual_model`
- `trace.fallback_used`
- `trace.fallback_reason`
- `trace.final_answer_source`
- `usage.usage_source`
- `budget.budget_source`

如果是流式返回，前端页面也会把 `trace`、`usage`、`budget` 直接渲染出来。

## 9. 故障排查

### 9.1 API Key 错误

症状通常是 401、unauthorized 或 provider 认证失败。

检查点：

- `api_key` 是否真的写到了 service 的配置里。
- 你注入的环境变量名是否正确。
- 当前 provider profile 是否被服务端实际读取。

对应代码里，OpenAI-compatible adapter 在 [ai-chat-service/chat-server/provider/openai_compatible.go](../ai-chat-service/chat-server/provider/openai_compatible.go) 会把鉴权错误包装成 `ProviderError`，类目大概率是 `auth`。

### 9.2 Base URL 配错

症状通常是连接失败、404、路由不通，或者请求打到了不对的网关。

检查点：

- 是否写成了 OpenAI-compatible 的完整 endpoint。
- 是否带了 `/v1`。
- 是否多写了尾部斜杠或路径前缀。

注意：当前 adapter 会去掉尾部 `/`，但不会替你补 `/v1`。

### 9.3 模型名不对

症状通常是模型被 fallback、接口报 invalid model，或者前端选了一个服务端不认的模型。

检查点：

- `default_model` 是否是你账号真实可用的模型。
- `models` 列表里是否真的包含该模型。
- `model_aliases` 是否把别名映射到了实际模型。

如果模型不被当前 provider 接受，服务端的 `ResolveSelection` 可能会返回 `fallback_used=true`，并把 `fallback_reason` 标成 `invalid_model_for_provider` 或 `unknown_model`。

### 9.4 provider 被禁用

症状通常是 UI 里还能看到，但 service 实际不使用，或者完全从 catalog 里消失。

检查点：

- `enabled: false` 是否被你不小心关掉了。
- backend 和 service 的 config 是否不一致。

service 侧的 registry 在构建时会跳过 `enabled=false` 的 profile，所以这个字段不是摆设。

### 9.5 stream 不支持

症状通常是前端卡住、请求失败，或者实际改成了非流式结果。

检查点：

- `supports_stream` 是否为 `true`。
- `supports_non_stream` 是否也允许非流式兜底。
- 你选的 provider 是否真的支持流式输出。

当前前端主路径依赖流式响应，所以实际接入的聊天模型最好先保持 `supports_stream: true`。

### 9.6 前端选了模型但后端未生效

最常见原因是 backend 和 service 的 provider 列表不一致。

检查点：

- `GET /api/config` 的 catalog 里显示的 provider/model 和 service 实际配置是否一致。
- `trace.selected_*` 和 `trace.actual_*` 是否发生了 fallback。
- backend 的 catalog 是不是用了旧配置。

当前仓库里，前端选择只负责“发请求时带上 provider/model”；真正决定实际调用哪个模型的是 service 侧的 `ResolveSelection`。

### 9.7 trace 和选择不一致

这不一定是错误，也可能是正常 fallback。

如果看到：

- `selected_provider/selected_model` 和 `actual_provider/actual_model` 不一致

先看：

- `fallback_used`
- `fallback_reason`
- `enabled`
- `supports_stream`
- `model_aliases`

如果 `fallback_used=true`，那就说明服务端没有按你原始选择直接执行，而是做了降级或回退。

### 9.8 tokenizer 估算和真实 usage 有偏差

当前仓库里 token 统计并不是所有厂商都能做到完全精确，所以 `usage_source` 或 `budget_source` 可能是 `estimated` / `fallback`。

检查点：

- `x-ai-usage` 里是不是只有估算值。
- `x-ai-budget` 里是不是 `budget_source=estimated`。
- tokenizer 对当前模型名是否已经支持。

如果模型名不在 tokenizer 的支持范围内，token 数就会偏估，预算提示也只能作为近似值使用。

## 10. 当前仓库已支持的内容

当前这条接入路径已经支持：

- backend `/api/config` 和 `/api/models` 返回 provider catalog。
- frontend 可以选择 provider/model，并把选择发给 backend。
- service 侧已经有统一的 provider registry / profile / selection 逻辑。
- OpenAI-compatible adapter 已经能走 `go-openai` 的 `CreateChatCompletion` 和 `CreateChatCompletionStream`。
- `trace`、`usage`、`budget` 已经能从 service 通过 trailer 回传到 backend，再渲染到前端。
- legacy 的单模型 `chat.api_key/base_url/model` 仍然可用，适合单 provider 场景。

## 11. 仍需后续补充的内容

下面这些内容目前还不是这条主路径的默认能力，后续如果要继续扩展，再单独做：

- 更完善的密钥管理和环境变量渲染机制。当前仓库没有 `api_key_env` 解析字段，密钥注入还是部署层事情。
- 非 OpenAI-compatible 的原生 SDK adapter。
- 更完整的 provider 能力矩阵，例如工具调用、embeddings、响应格式约束等。
- backend 和 service 共享同一份 provider 配置源，减少两边 catalog 漂移。
- 更细的错误分类、重试策略、健康检查和多 provider 自动切换策略。

如果你后面要继续扩展，这份文档建议的下一步优先级仍然是：

1. 保持 backend catalog 和 service registry 一致。
2. 先把 OpenAI-compatible 统一层稳定住。
3. 再加新的 provider adapter，而不是先改业务主链路。