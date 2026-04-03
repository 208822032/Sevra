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
