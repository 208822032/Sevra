# 本地服务管理

`start_all_services.sh` 只管理它启动的本地进程：mock OpenAI、代理、两个关键词服务、
AI-chat gRPC 服务和 AI-chat backend。MySQL、Redis、tokenizer 和 28081 向量 KVLite
均是外部依赖；脚本会在启动前检查向量 KVLite 的 TCP 连通性，AI-chat 启动时再通过
`VINFO` 进行严格健康检查。

```bash
./scripts/start_all_services.sh start
./scripts/start_all_services.sh status
./scripts/start_all_services.sh stop
./scripts/start_all_services.sh restart
```

服务二进制、日志和 PID 状态位于 `runtime/services/`。停止会向每个服务独立进程组发送
`SIGTERM`，等待 `GRACE_SECONDS`（默认 15 秒）后才发送 `SIGKILL`。脚本只终止 PID 状态
文件中且命令行仍匹配本次构建二进制的进程，避免误杀无关服务。

设置 `START_WEB=1` 可一并启动前端。设置 `KVLITE_VECTOR_HOST`、
`KVLITE_VECTOR_PORT` 可覆盖外部向量 KVLite 预检地址。
