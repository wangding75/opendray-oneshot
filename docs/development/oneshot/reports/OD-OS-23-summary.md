# OD-OS-23 执行报告

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

Task 详情以持久化 REST 回放为初始化事实源，以 WebSocket cursor stream 作为实时增量。切换 Run、刷新和重连时先停止旧订阅，清空旧 cursor，分页回放目标 Run 的全部历史事件，再从新 cursor 订阅；事件按 ID/sequence 去重并设置 10,000 条内存上限。

Continue、Cancel、Retry 是三个明确动作。Artifact 下载在落盘和打开前同时校验元数据 SHA-256、元数据大小、响应 `Content-Length`、`Digest` 和 `ETag`。

## 核心交付

- Task source、Project、Provider、Context、当前 Run 和状态时间线。
- Run 选择器，支持 stdout、stderr、raw 三种视图。
- 历史事件完整分页回放、实时追加、cursor 重连和去重。
- 大输出虚拟化展示与 10,000 条有界缓存。
- waiting_input 强提示和 Continue 输入。
- Continue、Cancel、Retry 确认、loading、成功和错误状态。
- Artifact 列表、完整性校验、保存及显式“打开”动作。
- Run 切换时阻止旧订阅帧混入新 Run。

## 修改文件

主要实现集中于：

- `agent_task_detail_screen.dart`
- `agent_task_controllers.dart`
- `agent_tasks_stream.dart`
- `agent_tasks_api.dart`
- `agent_task_models.dart`
- `agent_tasks_stream_test.dart`
- `agent_tasks_widget_test.dart`

## 开发中发现并修复的问题

1. 切换 Run 时曾复用上一 Run 的 cursor，可能跳过目标 Run 的早期事件。
2. 历史事件只加载第一页，大输出和长任务记录不完整。
3. 旧 Run 的异步 WebSocket 帧可能在取消订阅后短暂进入新 Run 页面。
4. 刷新期间若先推进 cursor、后覆盖事件集合，存在丢失竞态。
5. Artifact 初版只验证 SHA，未验证元数据大小与响应长度。
6. 页面缺少明确状态时间线和下载后的“打开”动作。

修复后采用：停止旧流 → 清空 cursor → 全量分页回放 → 去重 → 新流订阅；并用 `_subscribedRunId` 拒绝旧 Run 帧。

## 测试与门禁

- Event 顺序、重复和 cursor tracker 测试已补齐。
- WebSocket 认证错误、权限错误、网络中断和重连测试已补齐。
- Continue/Retry request 区分测试已补齐。
- Artifact header/hash/size 校验测试已补齐。
- 详情状态、输出 Tab、控制动作和小屏 Widget 测试已补齐。
- Agent Tasks 源码门禁：PASS。
- 控制面 API/Channel/Notification/AppWire 隔离 `go test -race`：PASS。
- PTY/Channel 回归：PASS。

Flutter/Dart 当前不可用，因此 Flutter 测试未执行；状态保持“源码门禁通过、运行门禁待本机”。

## API、数据库、Telegram、移动端影响

- API：消费 Task/Run/Event/Artifact 与 WS 冻结契约，无新写路由。
- 数据库：无新增 Migration，依赖 OD-OS-19 的持久化事件事实源。
- Telegram：Telegram 端操作产生的 Task/Run 状态可由移动端 REST/WS 显示；跨端通知去重属于下一任务。
- 移动端：完整操作 Task，不需要进入 Web/SSH；Terminal 详情保持原路径。

## PTY 兼容性

- Agent Task 详情未导入 Terminal/xterm 控件。
- PTY Source Baseline：16/16 PASS。
- PTY 负向变异：5/5 PASS。
- Session Channel Adapter、Channel Core、Telegram、Slack、Discord、Feishu：PASS。

## Commit 与 Push

- Commit 数：三个任务统一 1 次。
- Commit Hash：见最终交付回执。
- Push：推送至沙箱本地 Bare Remote，不是 GitHub。

## 未完成项

- Flutter analyze/test、真实设备大输出性能和后台重连。
- Android/iOS 文件打开权限与系统应用联调。
- 真实 Artifact 下载中断、磁盘不足和恶意响应测试。
- 跨端附件、通知去重和审计属于 OD-OS-24。

## 下一任务

`OD-OS-24 — 跨端同步、附件与通知策略`
