# OD-OS-06 完成报告：共享出站 Channel Delivery 抽象

## 1. 状态

**PASS — 源码实现与当前可执行源码门禁通过；Go 1.25、真实 PostgreSQL 和 Flutter 全仓运行门禁待本机执行。**

## 2. 时间

- 开始：2026-07-27T17:45:00+0800
- 结束：2026-07-27T18:18:26+0800
- 耗时：33 分 26 秒

## 3. Git 基线与提交

- 基线 Commit：`6f726639ca88de5e083e631ff24eb629b39b4140`
- 最终实现 Commit：`14bb4132de057ea9d2376a488216c6a3f303126d`
- 实现提交说明：`refactor(channel): extract outbound delivery service`
- Push：PASS，已推送至本地沙箱 Remote `sandbox-origin/feat/oneshot-agent`
- 说明：该 Remote 是 `/mnt/data` 中的本地 Bare Repository，不是 GitHub。

## 4. 设计结论

本任务将 Channel Transport 的发送能力从业务通知判断中分离，形成 PTY Session 与未来 One-shot Agent 均可复用的中立 Delivery 层：

```text
Session / future One-shot adapter
  ↓
channel.OutboundDeliveryService
  ↓
durable outbox + segmentation + rate limit + edit fallback + transport retry
  ↓
Telegram / Slack / Discord / Feishu / other Channel Transport
```

核心边界：

- Delivery 不导入 `internal/session` 或 `internal/oneshot`；
- Transport 重试使用 `ChannelDeliveryAttempt`，不复用 Task/Run 重试状态；
- 消息先写入 durable outbox，再执行 Transport 副作用；
- 每个分段发送后写入 progress，Gateway 重启后从未完成部分恢复；
- Session Notifier 仅负责业务通知判断，并通过稳定幂等键调用共享 Delivery；
- 用户可见的 Session 通知文本和卡片内容保持不变。

## 5. 主要修改

### 共享 Delivery

新增 `internal/channel/delivery/`：

- `model.go`：`OutboundMessage`、`DeliveryReceipt`、`ChannelDeliveryAttempt`；
- `service.go`：持久化发送、分段、编辑 fallback、重试、恢复、回执；
- `outbox.go`：Outbox Store 与消息记录接口；
- `postgres_store.go`：PostgreSQL 持久化实现和租约恢复；
- `memory_store.go`：隔离测试实现；
- `segment.go`：Unicode 安全、优先换行的超长文本分段；
- `limiter.go`：按 Channel 串行预留发送时隙，避免并发 burst；
- `parts.go`：文本、卡片、图片和文件能力降级；
- `card_snapshot.go`：卡片持久化快照。

### Channel Core 与应用装配

- 新增 `internal/channel/delivery_contract.go`；
- `Hub.Deliver` 优先委托共享 Delivery；
- Channel 命令响应、平台通知和 SendTest 使用统一 Delivery；
- Delivery Lifecycle 随 Hub 启停；
- Shutdown 先停止 Delivery Worker，再清空 Channel Registry，避免把在途通知错误标记为 dead；
- `internal/app/app.go` 装配 PostgreSQL Outbox Store。

### Session 适配

- Session 通知和 Turn Reply 增加稳定 `delivery_idempotency_key`；
- Session Notifier 和 ReplyTracker 继续使用 `Host.Deliver`，不直接操作 Transport；
- 通知内容未改变。

### 数据库

新增 Migration：

```text
internal/store/migrations/0082_channel_delivery_outbox.sql
```

包含：

- `channel_delivery_outbox`；
- `channel_delivery_attempts`；
- 幂等键；
- 状态、重试时间、租约；
- 分段进度；
- Delivery Receipt；
- dead-letter 状态。

## 6. Red 证据

基线 `6f726639...` 中以下必要能力不存在：

- `internal/channel/delivery/service.go`；
- `internal/channel/delivery/model.go`；
- `internal/channel/delivery_contract.go`；
- `0082_channel_delivery_outbox.sql`。

证据：`docs/development/oneshot/evidence/OD-OS-06-red.txt`。

## 7. Green 验证

执行：

```text
scripts/oneshot/check-channel-delivery.sh
```

结果：PASS。

覆盖：

- Delivery 包 `go vet`；
- Delivery 包 `go test -race`；
- Telegram 超长内容分段和顺序；
- Transport 失败重试不重复逻辑投递；
- 编辑失败 fallback；
- Outbox 过期租约崩溃恢复；
- 卡片和附件能力；
- 并发限流时隙隔离；
- Session 通知稳定幂等键；
- Channel Core Delivery 委托；
- Telegram、Slack、Discord、Feishu Transport 兼容；
- 架构边界；
- PTY Source Baseline 16/16；
- PTY 门禁负向变异 5/5；
- i18n parity；
- task-state 一致性；
- `git diff --check`。

完整日志：`docs/development/oneshot/evidence/OD-OS-06-validation.txt`。

## 8. 已知问题回归

再次执行：

```text
scripts/oneshot/check-known-issue-fixes.sh
```

结果：PASS。

确认 OD-OS-04/05 已知问题未回归：

- 长 PTY 输入不截断；
- 同 Session FIFO；
- 非 Telegram Conversation Binding；
- Slash Command 分发；
- 单一 task-state 事实源。

证据：`docs/development/oneshot/evidence/OD-OS-04-05-post-OD-OS-06-regression.txt`。

## 9. 影响评估

| 范围 | 影响 |
|---|---|
| API | 未新增外部 HTTP/WebSocket API。 |
| 数据库 | 新增 Migration 0082 和两个 Delivery Outbox 表。 |
| Telegram | 超长文本按顺序分段；保留真实 Conversation/Message Receipt。 |
| 其他 Channel | Slack、Discord、Feishu 通过共享 Delivery；其他 Transport 可经相同接口使用。 |
| PTY | Session 通知迁移到共享 Delivery；PTY 输入与 Session 状态机未改变。 |
| Flutter | 无产品代码修改。 |
| One-shot | 尚未实现领域对象；后续可直接复用该 Delivery 接口。 |

## 10. 未完成运行门禁

当前沙箱未能执行：

- Go 1.25 全仓 `go test ./...`、`go test -race ./...`、`go build`；
- 真实 PostgreSQL Migration、租约竞争和 Gateway 重启集成测试；
- Flutter analyze/test/build；
- 真实 Telegram Bot E2E。

这些项目保持 `pending_runtime_validation`，不得解释为已通过。

## 11. 下一任务

`OD-OS-07 — One-shot 领域模型与状态机实现`
