# 前端发布与同步指南

## 真实运行链路

- 前端源码在 [ai-chat-web](../ai-chat-web)
- 生产构建产物输出到 [ai-chat-web/dist](../ai-chat-web/dist)
- backend 实际托管的静态目录是 [ai-chat-backend/www](../ai-chat-backend/www)
- backend 通过 [ai-chat-backend/cmd/main.go](../ai-chat-backend/cmd/main.go) 提供静态站点服务，首页最终落到 `index.html`

## 为什么只改源码不够

浏览器实际加载的是 backend/www 里的静态 bundle，不是源码目录本身。只改 [ai-chat-web](../ai-chat-web) 之后，如果不重新 build 并同步到 [ai-chat-backend/www](../ai-chat-backend/www)，页面不会生效。

## 正确发布步骤

1. 修改 [ai-chat-web](../ai-chat-web) 下的前端源码
2. 执行 `./scripts/build_and_sync_frontend.sh`
3. 确认脚本输出里出现 build 成功和同步到 [ai-chat-backend/www](../ai-chat-backend/www)
4. 如需快速检查，再执行 `./scripts/frontend_smoke.sh`

## 这次新增的命令

- `./scripts/build_and_sync_frontend.sh`：构建 `ai-chat-web` 并镜像同步到 backend 的运行时静态目录
- `./scripts/frontend_smoke.sh`：运行最小前端 smoke 检查，覆盖 catalog、持久化、fallback trace 和流式解析

## smoke 检查覆盖点

- `/config` 和 `/models` 能返回 catalog
- provider/model 选择能写回并在刷新后恢复
- selected / actual 不一致时能显示 fallback 提示
- 流式 chunk 解析和消息渲染逻辑没有被破坏

## 常用确认方式

- 直接看 [ai-chat-backend/www/index.html](../ai-chat-backend/www/index.html) 和 [ai-chat-backend/www/assets](../ai-chat-backend/www/assets) 是否被同步刷新
- 跑完 smoke 后，确认终端没有报错
