# OD-OS-05 — Session Channel Adapter 迁移执行报告

## 状态

`PASS（源码开发与沙箱可执行门禁通过；Go 1.25 / Flutter 全量运行门禁按用户决定保留到本机调试）`

## 时间

| 项目 | 值 |
|---|---|
| 开始 | `2026-07-27T15:44:30+0800` |
| 结束 | `2026-07-27T16:19:33+0800` |
| 记录执行时长 | `35m03s` |

## Git

| 项目 | 值 |
|---|---|
| 分支 | `feat/oneshot-agent` |
| 基线 Commit | `f8a615bccbb069cd259d1bfa6da2446997621ffc` |
| 实现 Commit | `d391bed18065c02ef80ad01b30dfc2d5fef8d353` |
| Commit 信息 | `refactor(session): isolate channel adapter from channel core` |
| Push | `PASS — sandbox-origin/feat/oneshot-agent`（完成状态提交：`cdb0dc1`） |

## 设计结论

原 `internal/channel.Hub` 同时承担 Telegram/Channel 基础设施和 Interactive Session 业务语义，直接保存 Session 目标状态、reply binding、pending turn、last delivered output，并直接向 PTY 写入输入。这会阻止 Channel Core 被 One-shot 复用，也使两种执行域发生生命周期耦合。

本任务完成以下拆分：

```text
Channel Transport / Channel Core
  ↓ 中立 ChannelMessage / InboundDispatcher / ChannelHost
Session Channel Adapter
  ├── InteractiveBindingStore
  ├── InteractiveHandler
  ├── InputSubmitter
  ├── ReplyTracker
  └── SessionNotifier
  ↓
Session Manager / PTY
```

Channel Core 现在只负责渠道接收、标准化、持久化、分发和中立出站能力；Interactive Session 的目标解析、PTY 输入、turn tracking 和通知语义由 `internal/session/channeladapter` 独立承担。

## 主要实现

1. 新增 `internal/session/channeladapter`，定义 Adapter、BindingStore、Handler、InputSubmitter、ReplyTracker、SessionNotifier 和 Session 卡片。
2. Binding 解析优先级冻结为：reply-to outbound message → conversation active target → conversation last-notified target。
3. 所有 routing state 按 `channelID + conversationID` 隔离，并支持 binding TTL 与 LRU 上限。
4. 保持原 PTY 输入协议：按 Unicode rune 逐个写入，rune 间隔 5ms、settle 30ms、最后独立写入 `\r`。
5. 中途写入失败时立即停止，不再提交 Enter，并将消息标记为 handled，防止 Channel fallback 重复投递。
6. Session lifecycle 事件订阅、typing、turn timeout、late reply、idle output suppression 和卡片通知迁入 SessionNotifier/ReplyTracker。
7. Telegram 出站消息补充 `outbound_msg_id` 与 `outbound_conversation_id`，供精确 reply binding 使用。
8. 应用装配层注册 Session Adapter 为 Interactive Dispatcher，并把 Channel Core 与 Session Manager 直接依赖移除。
9. `/select`、`/peek` 的 active target 改为 conversation 级作用域。

## 修改文件

- `M	docs/development/oneshot/contracts/pty-baseline.md`
- `M	docs/development/oneshot/contracts/pty-test-matrix.yaml`
- `A	docs/development/oneshot/evidence/OD-OS-05-final-validation.txt`
- `A	docs/development/oneshot/evidence/OD-OS-05-green.txt`
- `A	docs/development/oneshot/evidence/OD-OS-05-isolated-tests.txt`
- `A	docs/development/oneshot/evidence/OD-OS-05-red.txt`
- `M	internal/app/app.go`
- `M	internal/app/channel_commands.go`
- `M	internal/app/channel_commands_test.go`
- `A	internal/channel/adapter_host.go`
- `M	internal/channel/channel.go`
- `M	internal/channel/controls_test.go`
- `M	internal/channel/dispatch_test.go`
- `M	internal/channel/hub.go`
- `M	internal/channel/hub_command_test.go`
- `M	internal/channel/hub_cooldown_test.go`
- `D	internal/channel/hub_integration_isolation_test.go`
- `D	internal/channel/hub_pty_baseline_test.go`
- `D	internal/channel/hub_submit_test.go`
- `D	internal/channel/reply_trim_test.go`
- `M	internal/channel/telegram/telegram.go`
- `M	internal/channel/telegram/telegram_test.go`
- `D	internal/channel/typing.go`
- `A	internal/session/channeladapter/adapter.go`
- `A	internal/session/channeladapter/adapter_test.go`
- `A	internal/session/channeladapter/binding_store.go`
- `A	internal/session/channeladapter/binding_store_test.go`
- `A	internal/session/channeladapter/cards.go`
- `A	internal/session/channeladapter/handler.go`
- `A	internal/session/channeladapter/input_submitter.go`
- `A	internal/session/channeladapter/input_submitter_test.go`
- `A	internal/session/channeladapter/notifier.go`
- `A	internal/session/channeladapter/notifier_test.go`
- `A	internal/session/channeladapter/reply_tracker.go`
- `A	internal/session/channeladapter/testdata/session_idle_card.txt`
- `M	scripts/oneshot/check-channel-dispatch.sh`
- `M	scripts/oneshot/check-pty-regression.sh`
- `M	scripts/oneshot/check-pty-source-baseline-test.sh`
- `M	scripts/oneshot/check-pty-source-baseline.py`
- `A	scripts/oneshot/check-session-channel-adapter-compat.sh`
- `A	scripts/oneshot/check-session-channel-adapter.sh`

## Red 证据

- `docs/development/oneshot/evidence/OD-OS-05-red.txt`
- 初始门禁明确失败：缺少 `internal/session/channeladapter/adapter.go`。

## Green 与模块验证

| 验证 | 结果 |
|---|---|
| `scripts/oneshot/check-session-channel-adapter.sh` | PASS |
| `scripts/oneshot/check-session-channel-adapter-compat.sh` | PASS |
| Session Adapter 隔离 `go test -race` | PASS |
| Channel Core 隔离 `go test -race` | PASS |
| Telegram Transport 隔离 `go test -race` | PASS |
| `scripts/oneshot/check-channel-dispatch.sh` | PASS |
| `scripts/oneshot/check-boundaries.sh` | PASS |
| PTY source regression | 16/16 PASS |
| PTY checker mutation tests | 5/5 PASS |
| i18n parity | PASS |
| gofmt | PASS |
| Shell syntax | PASS |
| `git diff --check` | PASS |

证据：

- `docs/development/oneshot/evidence/OD-OS-05-green.txt`
- `docs/development/oneshot/evidence/OD-OS-05-isolated-tests.txt`
- `docs/development/oneshot/evidence/OD-OS-05-final-validation.txt`

## 测试覆盖

- reply binding、active target、last target 优先级。
- 过期 binding fallback。
- channel/conversation 隔离。
- LRU 和 ClearChannel。
- Unicode rune-by-rune PTY 写入。
- partial write failure、context cancellation、missing inputter。
- typing timeout 后 late reply。
- Session idle notification golden fixture。
- integration origin 过滤、mute、suppression。
- Telegram outbound reply metadata。
- 应用命令 active target 的 conversation 级隔离。

## 影响分析

### API

- 未新增或修改对外 REST API。
- Channel 内部增加中立 Adapter Host/Target Controller 接口。

### 数据库

- 无 Migration。
- 当前 Interactive Binding 仍为内存实现，行为与原 Channel Hub 一致。

### Telegram

- 原 Session 回复、last target fallback、typing、turn result 和通知行为保留。
- 出站 metadata 现在包含精确 message/conversation 标识。

### 移动端

- 无 Flutter 产品代码修改。
- Session `/input`、`/resize`、WebSocket 现有契约由 PTY 回归门禁继续保护。

### PTY 兼容性

- PTY Session Manager、PTY 创建、resize、resume、terminate 未改写。
- 仅迁移消息接入和通知适配职责。
- PTY source regression 16/16 和 mutation tests 5/5 通过。

## 未在沙箱宣称通过的门禁

当前沙箱仅有 Go 1.23.2，仓库要求 Go 1.25；同时未安装 Flutter/Dart。因此以下最终运行门禁保留到本机：

```bash
PTY_REGRESSION_REQUIRE_GO=1 \
PTY_REGRESSION_REQUIRE_MOBILE=1 \
scripts/oneshot/check-pty-regression.sh
```

这不是源码失败；本任务已通过真实源码复制后的隔离 `go test -race`。最终发布验收前仍必须在规定工具链运行完整仓库门禁。

## 验收结论

- `internal/channel` 不再直接写 PTY：PASS。
- Session Channel Adapter 独立承担 Interactive 行为：PASS。
- reply binding、错误 Session、过期 binding、跨 conversation 隔离：PASS。
- Telegram Transport 隔离测试：PASS。
- 现有 PTY 源码级回归：PASS。

## 下一任务

`OD-OS-06 — 共享出站 Channel Delivery 抽象`

---

## 后续修复说明

初次完成后发现 PTY 长消息可能被 Dispatcher deadline 截断，且非 Telegram 主动通知会话绑定不完整。相关问题已在 Commit `ee337bf96c3370bf033ce5bea4b4963453510fad` 修复。完整证据见：

`docs/development/oneshot/reports/OD-OS-04-05-repair-summary.md`
