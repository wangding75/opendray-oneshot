# OD-OS-04 / OD-OS-05 已知问题修复报告

## 状态

`PASS（源码修复和沙箱可执行门禁通过；Go 1.25 / Flutter 全仓运行门禁仍待本机执行）`

## 时间

| 项目 | 值 |
|---|---|
| 开始 | `2026-07-27T16:55:00+0800` |
| 结束 | `2026-07-27T17:30:35+0800` |
| 实现 Commit | `ee337bf96c3370bf033ce5bea4b4963453510fad` |
| 分支 | `feat/oneshot-agent` |

## 修复范围

本轮只修复 OD-OS-04 / OD-OS-05 已确认缺陷，没有开始 OD-OS-06，也没有创建 One-shot 领域对象。

### 1. PTY 长消息被统一 Dispatcher 超时截断

根因：Interactive Session Adapter 在 Channel Core 的短路由 Context 内逐 Rune 写入 PTY。消息接近 1000 字符时，5 秒 Dispatcher deadline 可能在输入中途取消，造成残缺 Prompt 留在终端。

修复：

- 新增 `InputQueue`，按 Session 建立独立 FIFO lane；
- Dispatcher 只完成目标解析与有界入队，快速返回 `handled`；
- PTY 实际写入使用 Adapter 自有生命周期 Context，不继承 Channel Core 的短路由 deadline；
- 不同 Session 独立执行，同一 Session 严格串行；
- 队列关闭、队列满和写入失败均返回明确错误或发送失败通知；
- 保留逐 Rune 输入和独立 `\r` 提交协议。

新增回归覆盖：

- 路由 Context 到期后，输入仍完整结束；
- 5000 个 Unicode Rune 完整写入并提交 Enter；
- 两条连续长消息严格输出为 `message1\rmessage2\r`，无交叉、无截断。

### 2. 非 Telegram 主动通知绑定到 `default`

根因：Session 主动通知使用占位 Conversation ID，真实 Slack Channel、Discord Channel、Feishu Chat 只在 Transport 内部解析，Session Binding Store 无法得到真实会话地址。

修复：

- 新增中立 `OutboundAddressResolver`；
- Hub 在发送和持久化前解析真实 Conversation / Thread；
- Telegram、Slack、Discord、Feishu 均实现真实目标解析；
- 四种 Transport 均记录：
  - `outbound_msg_id`；
  - `outbound_conversation_id`；
  - 支持时记录 `outbound_thread_id`；
- Session Notifier 使用真实 Conversation 建立 last binding，并使用原生消息 ID 建立精确 reply binding。

### 3. One-shot Slash Command 无法到达执行域

根因：Channel Core 在调用 `InboundDispatcher` 前直接处理所有 Slash Command，未注册的 `/run`、`/task`、`/cancel` 会立刻得到 Unknown Command。

修复后的确定性顺序：

```text
已注册 Channel Command
  → Channel CommandRegistry 处理

未注册 Slash Command
  → InboundDispatcherChain
  → 某执行域 handled 时停止
  → 所有执行域 not_handled 后才返回 Unknown Command
```

同时，Interactive PTY Adapter 不接收 Slash Command，避免将 One-shot 命令写入 Shell。

### 4. `task-state.yaml` 存在多个冲突事实源

根因：`completed_tasks`、`implementation_complete_tasks`、`local_runtime_validation_pending` 三个列表分别描述任务状态，OD-OS-02 在不同列表中结论不一致。

修复：

- 删除全部旧列表；
- 统一使用 `tasks.<task-id>` 映射；
- 每个任务独立记录 `implementation`、`source_gate`、`runtime_gate`、`overall`；
- 新增 `check-task-state.py`，阻止旧字段重新出现并校验状态组合一致性。

## 主要修改文件

### Channel Core 与 Transport

- `internal/channel/channel.go`
- `internal/channel/adapter_host.go`
- `internal/channel/hub.go`
- `internal/channel/dispatch_test.go`
- `internal/channel/hub_command_test.go`
- `internal/channel/telegram/telegram.go`
- `internal/channel/slack/slack.go`
- `internal/channel/discord/discord.go`
- `internal/channel/feishu/feishu.go`
- 对应四种 Transport 测试文件

### Session Channel Adapter

- `internal/session/channeladapter/input_queue.go`
- `internal/session/channeladapter/adapter.go`
- `internal/session/channeladapter/handler.go`
- `internal/session/channeladapter/binding_store.go`
- `internal/session/channeladapter/adapter_test.go`
- `internal/session/channeladapter/notifier_test.go`

### Gate 与状态

- `scripts/oneshot/check-known-issue-fixes.sh`
- `scripts/oneshot/check-channel-transport-compat.sh`
- `scripts/oneshot/check-task-state.py`
- `scripts/oneshot/check-channel-dispatch.sh`
- `scripts/oneshot/check-session-channel-adapter.sh`
- `docs/development/oneshot/task-state.yaml`

## 验证结果

执行命令：

```bash
scripts/oneshot/check-known-issue-fixes.sh
```

结果：

- repair Go 文件 `gofmt`：PASS；
- Shell 语法：PASS；
- task-state 单一事实源：PASS；
- Session Channel Adapter `go test -race` 隔离门禁：PASS；
- Channel Core `go test -race` 隔离门禁：PASS；
- Telegram Transport `go test -race`：PASS；
- Slack Transport `go test -race`：PASS；
- Discord Transport `go test -race`：PASS；
- Feishu Transport `go test -race`：PASS；
- Channel Dispatcher 源码门禁：PASS；
- 双执行域架构边界：PASS；
- PTY source baseline：16/16 PASS；
- PTY 门禁负向自测：5/5 PASS；
- i18n parity：PASS；
- `git diff --check`：PASS。

证据：

`docs/development/oneshot/evidence/OD-OS-04-05-repair-validation.txt`

## 影响说明

| 范围 | 影响 |
|---|---|
| API | 无新增或删除 HTTP API。 |
| 数据库 | 无 Migration、无表结构变化。 |
| PTY | 输入协议保持逐 Rune + 独立 Enter；只改变执行 Context 和串行队列。 |
| Telegram | 保留现有行为，并统一使用出站元数据常量。 |
| Slack / Discord / Feishu | 主动通知现在可绑定真实会话及原生消息 ID。 |
| Flutter | 无生产代码变化。 |
| One-shot Domain | 尚未实现；仅为未来命令路由保留中立入口。 |

## 未完成运行门禁

当前沙箱仍缺少：

- Go 1.25；
- Flutter 3.41 / Dart 3.11；
- 完整依赖和 PostgreSQL 环境。

因此以下命令仍必须在本机执行，不能视为已经通过：

```bash
PTY_REGRESSION_REQUIRE_GO=1 \
PTY_REGRESSION_REQUIRE_MOBILE=1 \
scripts/oneshot/check-pty-regression.sh
```

## 下一任务

`OD-OS-06 — 共享出站 Channel Delivery 抽象`
