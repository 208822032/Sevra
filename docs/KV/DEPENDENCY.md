# KVLite 外部依赖

AI-chat 不再把仓库内的 `KVLite/` 副本作为运行时依赖，也不会启动旧 QA 数据或外部
VectorDB。知识检索应连接独立部署、已启用 embedding 的 KVLite 实例。

- 推荐工作副本：`/home/zhengpan/share/Linux/KVLite`
- 已验证版本：`d3f7b09`（当前工作副本）
- 28080 旧验证服务配置：`config/kvstore-embedding.conf`
- AI-chat 专用服务配置：`/home/zhengpan/share/Linux/KVLite/config/kvstore-ai-chat-vector.conf`
- AI-chat 向量服务地址：`127.0.0.1:28081`
- 模型：本地 ONNX `bge-small-zh-v1.5`，512 维、归一化内积、`core_hnsw_v2`

AI-chat 的 `context.kvlite` 仍是会话上下文配置，与上述向量服务相互独立。后续阶段会新增
专用的检索配置，不能以旧 `9300` 会话端口或删除前的 `vectorDB` 配置代替。

28080 实例仍用于独立功能验证。AI-chat 使用独立的 28081 实例、
`data/ai-chat-vector/ai-chat-vector.rdb` 和 20,000 HNSW 容量；不得在运行中的共享实例上
直接改 RDB 路径或容量。该容量约消耗 162.6 MiB HNSW 内存，记录文本与 embedding 另计。

## 单节点运行边界

当前 KVLite 向量索引没有主从复制或故障自动切换。28081 是单节点依赖，RDB 是恢复介质而
不是高可用副本：上线前必须将 `data/ai-chat-vector/ai-chat-vector.rdb` 纳入定期备份，并
演练从备份/部署重启后的 `VINFO`、`VGET`、`VSEARCH` 恢复。AI-chat 的启动策略目前是
`reject`；运行期 KVLite 故障或索引未就绪时，路由不会伪造知识答案，而是记录原因并回源
LLM。只有具备明确的复制、故障切换和一致性验证设计后，才可变更这一边界声明。
