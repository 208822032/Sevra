# AI Chat KVLite

这是一个由前端、HTTP 网关、gRPC 聊天服务和多个支撑服务组成的本地 AI Chat 开发项目。项目支持 OpenAI 兼容模型（当前开发配置默认使用 DeepSeek），并接入 KVLite 作为知识向量检索后端。

当前知识检索流程为：

1. 用户问题先查询知识 KVLite（`127.0.0.1:28080`）。
2. 命中且相似度不低于阈值时，直接返回 KVLite 答案，不调用 LLM。
3. 未命中时调用 LLM。
4. LLM 返回有效答案后，异步通过 `VSET` 写回知识 KVLite，避免阻塞用户响应。

会话上下文 KVLite（默认 `127.0.0.1:9300`）与知识向量 KVLite 是两个独立实例，不能混用端口。

## 目录

| 目录 | 作用 |
| --- | --- |
| `ai-chat-web` | Vue/Nuxt 前端，开发端口 5173 |
| `ai-chat-backend` | HTTP 网关，端口 7080 |
| `ai-chat-service` | 聊天核心 gRPC 服务，端口 50055 |
| `keywords-filter` | 敏感词和关键词 gRPC 服务，端口 50053/50054 |
| `tokenizer` | Token 统计服务，端口 3002 |
| `mock-openai-api` | 本地 OpenAI 兼容模拟服务，端口 8083 |
| `openai-api-proxy` | OpenAI 兼容代理，端口 8084 |
| `KVLite` | 外部 KVLite 源码和二进制 |
| `scripts/start_all_services.sh` | 统一构建、启动、停止和查看项目服务 |

## 环境要求

- Linux
- Go（用于构建 Go 服务）
- Node.js 与 pnpm（用于启动前端）
- Redis（默认 `127.0.0.1:6379`）
- MySQL 8（默认 `127.0.0.1:3306`）
- tokenizer（默认 `127.0.0.1:3002`）
- 知识向量 KVLite（默认 `127.0.0.1:28080`，必须使用真实 embedding 配置）
- 会话 KVLite（仅当 `context_backend: kvlite` 时需要，默认 `127.0.0.1:9300`）

不要把 API Key、Redis 密码或数据库密码提交到公共仓库。开发配置中的密钥应改为本地私有配置或环境变量；已暴露的密钥应及时轮换。

## 配置

主要配置文件：

- `ai-chat-service/dev.config.yaml`
- `ai-chat-backend/dev.config.yaml`
- `ai-chat-web/.env.develop`

当前开发配置的关键值：

```yaml
# ai-chat-service/dev.config.yaml
retrieval:
  kvlite:
    enabled: true
    address: "127.0.0.1:28080"
    startup_policy: "reject"
    top_k: 1
    threshold: 0.75
    auto_writeback: true
    scope:
      tenant: "local-dev"
      knowledge_base: "ai-chat"
      corpus_version: "v1"

context:
  context_backend: "kvlite"
  context_fallback_to_redis: true
  kvlite:
    addr: "127.0.0.1:9300"

redis:
  host: "127.0.0.1"
  port: 6379

mysql:
  # 请按本机账号密码填写，不要复制到公共仓库
  dsn: "ai_chat:密码@tcp(127.0.0.1:3306)/ai_chat?..."
```

前端默认请求地址为 `http://localhost:7080/`。从其他机器访问时，将 `ai-chat-web/.env.develop` 中的地址改为运行本项目主机的 IP，例如 `http://192.168.43.131:7080/`，并重启前端。

## 启动外部依赖

### 知识向量 KVLite（28080）

```bash
cd /home/zhengpan/share/Linux/KVLite
./kvstore -c config/kvstore-embedding.conf > kvstore.log 2>&1 &
ss -ltnp | grep ':28080'
```

确认向量索引已经准备好：

```bash
./build/tests/bin/kvstore_client 127.0.0.1 28080 -c 'VINFO'
```

应看到 `index_ready:1`、`dimension:512` 和 `index_type:core_hnsw_v2`。

### 会话 KVLite（9300）

仅当 `context.context_backend` 为 `kvlite` 时启动，使用会话配置文件 `master.conf`（不要使用 28080 的 embedding 配置）：

```bash
cd /home/zhengpan/share/Linux/KVLite
./kvstore -c config/master.conf > kvstore-session.log 2>&1 &
ss -ltnp | grep ':9300'
```

如果改为 Redis 会话存储，则将 `context_backend` 改为 `redis`，会话 KVLite 可以不启动。

### Redis 和 MySQL

```bash
sudo systemctl enable --now redis-server
redis-cli -a '你的Redis密码' ping

sudo systemctl enable --now mysql
sudo mysql -e 'SELECT VERSION();'
```

数据库结构位于 `docs/sql/create_db.sql`。应用使用的数据库至少需要包含 `ai_chat.chat_records` 表。

### tokenizer

统一启动脚本不会启动 tokenizer。请单独在 `tokenizer` 目录运行项目约定的启动命令，并确认 3002 端口可用：

```bash
ss -ltnp | grep ':3002'
```

## 统一启动和关闭

在项目根目录执行：

```bash
cd /home/zhengpan/share/Linux/ai_chat_kvstore
./scripts/start_all_services.sh start
./scripts/start_all_services.sh status
./scripts/start_all_services.sh stop
./scripts/start_all_services.sh restart
```

当前脚本默认启动前端（`START_WEB=1`）。如果只启动后端服务：

```bash
START_WEB=0 ./scripts/start_all_services.sh start
```

脚本只管理它自己启动的进程；Redis、MySQL、tokenizer 和两个 KVLite 实例属于外部依赖。启动前会检查知识向量 KVLite 的 `127.0.0.1:28080` 端口，因此该端口未监听时会直接退出。

运行状态、构建后的二进制和日志位于：

```text
runtime/services/services.tsv
runtime/services/bin/
runtime/services/logs/
```

## 知识检索测试

### 前端两阶段测试

对一个全新问题：

1. 第一次提问应显示 `模式：api`，并在几秒后写回 KVLite。
2. 等待日志出现 `KVLite auto writeback succeeded`。
3. 用语义相近的第二个问题提问，应显示 `模式：kvlite`。

示例：

```text
第一次：公司打印机无法打印应该怎么处理
第二次：办公打印机不能使用怎么办
```

查看写回日志：

```bash
grep 'KVLite auto writeback' \
  ai-chat-service/runtime/logs/app.log
```

### 使用命令查看相似度

当前知识 scope 由配置中的 tenant、knowledge_base 和 corpus_version 组成。使用下面命令自动生成正确的 scope 编码，避免手动复制时换行：

```bash
cd /home/zhengpan/share/Linux/KVLite
SCOPE=$(printf '%s' '{"tenant":"local-dev","knowledge_base":"ai-chat","corpus_version":"v1"}' \
  | base64 -w0 | tr -d '=')

./build/tests/bin/kvstore_client 127.0.0.1 28080 \
  -c "VSEARCH 办公打印机不能使用怎么办 $SCOPE 10 0.0"
```

最后的 `0.0` 仅表示查看所有候选结果。输出中的 `score` 是相似度，`answer` 是 Base64URL 编码的答案内容。

当前测试语料下推荐阈值为 `0.75`：已覆盖相似问题最低约 `0.797` 的样本，同时与无关问题最高约 `0.562` 保持间隔。知识库大幅扩充后，应重新使用正、负样本校准阈值。

## 日志与故障排查

### KVLite 28080 超时

```bash
ss -ltnp | grep ':28080'
cd /home/zhengpan/share/Linux/KVLite
./kvstore -c config/kvstore-embedding.conf > kvstore.log 2>&1 &
```

### 前端启动健康检查超时

确认 `pnpm`、Node.js 版本和 5173 端口：

```bash
node --version
pnpm --version
ss -ltnp | grep ':5173'
tail -n 100 runtime/services/logs/ai-chat-web.log
```

### 请求很慢

先比较网关日志中的请求耗时：

```bash
tail -n 100 runtime/services/logs/ai-chat-backend.log
```

通常 API 请求需要数秒，而 KVLite 直接命中应在几十毫秒级。若第二次仍走 API，检查：

- 第一次是否真的出现 `KVLite auto writeback succeeded`；
- 两次请求是否使用同一个 scope；
- `threshold` 是否高于实际相似度；
- 运行中的 `ai-chat-service` 是否已重新构建并重启；
- tokenizer、Redis、MySQL 是否可连接。

## 开发验证

在 `ai-chat-service` 目录运行：

```bash
go test ./...
```

当前直接写回相关测试覆盖配置解析、KVLite 客户端和聊天服务路由；写回失败只记录 warning，不会撤销已经返回给用户的 LLM 答案。

## 安全和生产注意事项

- 自动写回适合开发测试；生产环境应增加领域过滤、人工审核或可信度策略，避免天气、写诗等非知识问题污染知识库。
- 不要把真实 API Key、Redis 密码、MySQL 密码提交到 Git。
- `VSET` 写回使用当前 scope；变更 tenant、knowledge base 或 corpus version 后，旧数据不会自动跨 scope 命中。
- `context.kvlite`（会话）和 `retrieval.kvlite`（知识向量）必须保持不同端口和不同用途。
