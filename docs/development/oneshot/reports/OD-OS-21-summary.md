# OD-OS-21 执行报告

## 状态

**PASS（源码门禁通过；Flutter 运行门禁待本机）**

- 执行分支：`feat/oneshot-agent`
- 批次基线 Commit：`99ccc1fbb0f8e6bba59dcffe5f21fdf96cf3d404`
- 最终 Commit：本报告随单一交付 Commit 一并入库；精确 Hash 记录在最终交付回执中。Git Commit 无法在自身内容中稳定记录自身最终 Hash。
- 批次开始：`2026-07-28T13:52:17Z`
- 批次结束：`2026-07-28T14:25:19Z`
- 批次耗时：`33 分 02 秒`
- 单任务耗时：三个任务连续开发，未对任务边界单独计时。

## 设计结论

在现有 Flutter App 中新增完全独立的 `agent_tasks` feature，采用 `domain / data / presentation` 分层。该 feature 复用现有认证、Dio、Project、Provider 和路由基础设施，但不导入 xterm、Terminal、PTY Session 或 Session 领域模型。

Agent Tasks 与终端会话并列作为一级入口。REST、WebSocket、cursor、错误映射和 Provider One-shot capability 都通过后端冻结契约接入，不从 PTY Manifest 推断 One-shot 能力。

## 核心交付

- Task、Run、Event、Artifact、Provider Capability 完整领域模型和 JSON 解析。
- REST repository 与 WebSocket stream，支持 cursor 重连、指数退避、事件去重和认证错误停止重连。
- `/agent-tasks/new`、`/agent-tasks/:id` 路由与一级导航入口。
- 英文、西班牙文、中文共 79 个 `agentTasks` 文案 key，三语言完全一致。
- `/api/v1/providers` 增加可选 `oneshot` descriptor；原 PTY Provider 字段和路由保持兼容。
- One-shot 分页结构补齐 `items`、`next_cursor` JSON 标签，移动端兼容旧字段大小写，支持平滑升级。

## 修改文件

主要新增：

- `app/mobile/lib/features/agent_tasks/domain/agent_task_models.dart`
- `app/mobile/lib/features/agent_tasks/data/agent_tasks_api.dart`
- `app/mobile/lib/features/agent_tasks/data/agent_tasks_repository.dart`
- `app/mobile/lib/features/agent_tasks/data/agent_tasks_stream.dart`
- `app/mobile/lib/features/agent_tasks/presentation/agent_tasks_strings.dart`
- `app/mobile/lib/features/agent_tasks/presentation/agent_task_controllers.dart`
- `app/mobile/lib/features/agent_tasks/presentation/agent_tasks_screen.dart`
- `app/mobile/lib/features/agent_tasks/presentation/create_agent_task_screen.dart`
- `app/mobile/lib/features/agent_tasks/presentation/agent_task_detail_screen.dart`
- `app/mobile/lib/features/agent_tasks/presentation/widgets/agent_task_status_badge.dart`
- `app/mobile/test/features/agent_tasks/*`

共享接入：

- `app/mobile/lib/core/routing/app_router.dart`
- `app/mobile/lib/features/home/home_shell.dart`
- `app/i18n/en.json`、`es.json`、`zh.json`
- `internal/catalog/*`
- `internal/oneshot/appwire/catalog.go`
- `internal/oneshot/store/store.go`
- `internal/app/app.go`

## 开发中发现并修复的问题

1. Go 泛型分页对象缺少 JSON 标签，可能输出 `Items/NextCursor`，与 REST 风格及移动端解析不一致。
2. 通用 Provider Manifest 的 `supportsImages` 属于 PTY 能力，若直接复用会错误启用 Claude One-shot 附件。
3. One-shot Adapter 的真实 capability descriptor 未通过 Provider API 暴露。
4. WebSocket 错误原本统一重连，认证失效或明确权限拒绝可能形成无意义重连循环。

修复后，Catalog 保持执行域中立；One-shot AppWire 提供可选能力扩展。能力解析失败只隐藏 One-shot 控件，不影响现有交互式 Provider。

## 测试与门禁

- Agent Tasks 源码契约：PASS。
- feature Dart 文件：10 个。
- Flutter 专项测试文件：4 个，共 660 行。
- 三语言 i18n parity：PASS，79/79 key。
- Page JSON 隔离 Go 测试：PASS。
- Provider One-shot capability 隔离 Go 测试：PASS。
- OD-OS-18～20 控制面源码和隔离编译回归：PASS。
- `git diff --check`：PASS。

Flutter/Dart 不在当前沙箱中，`flutter analyze`、`flutter test`、slang 生成和 APK 构建未执行，未标记为通过。

## API、数据库、Telegram、移动端影响

- API：Provider 响应新增可选 `oneshot` 字段；旧客户端可忽略，不改变原字段。
- 数据库：无 Migration。
- Telegram：无行为修改；Telegram 创建的 Task 可通过同一 Task API 被移动端读取。
- 移动端：新增独立一级功能，不改变 Terminal 页面语义。

## PTY 兼容性

- PTY Source Baseline：16/16 PASS。
- PTY 负向变异：5/5 PASS。
- Session Channel Adapter、Channel Core、Telegram、Slack、Discord、Feishu：PASS。
- Agent Tasks feature 无 xterm、PTY Session 领域依赖。

## Commit 与 Push

- Commit 数：三个任务统一 1 次。
- Commit Hash：见最终交付回执。
- Push：推送至沙箱本地 Bare Remote，不是 GitHub。

## 未完成项

- Flutter analyze/test、slang 代码生成和 APK 构建。
- 真机导航、登录失效、网络中断和后台恢复验证。
- Go 1.25 全仓测试和构建。

## 下一任务

`OD-OS-24 — 跨端同步、附件与通知策略`
