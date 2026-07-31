# OD-OS-19 — One-shot WebSocket、事件回放与通知 Outbox 执行报告

## 1. 状态

`PASS — 源码实现、隔离 race 测试和确定性门禁完成；真实 PostgreSQL、网络断线和多实例运行门禁待本机验证`

## 2. 时间与 Git 基线

- 开始：`2026-07-28T19:46:40+08:00`
- 结束：`2026-07-28T20:55:00+08:00`
- 基线 Commit：`2f93ab8b1397e3f8a8787afd31f8c3673bb0b747`
- 最终 Commit：本报告所在的 `OD-OS-18/19/20` 单一提交；精确 Hash 见交付回执和 Git Bundle。
- Push：仅推送沙箱本地 Bare Remote，不是 GitHub。

## 3. 设计结论

Task/Run 流只回放数据库中的持久化事实，不依赖进程内 EventBus 作为事实源。新增统一 lifecycle event 表；Task/Run 状态事件、终态通知 Outbox 与业务终态在同一事务提交。Run WebSocket 合并生命周期事件和标准输出事件，按稳定顺序返回不透明 cursor。通知 Worker 使用 `FOR UPDATE ... SKIP LOCKED` 租约、退避重试和死信；发送端携带稳定投递幂等键，Provider 执行与通知发送完全隔离。

## 4. 核心交付

- Task stream、Run stream 与断线 cursor 回放；
- Run 生命周期和标准化 stdout/stderr 统一排序；
- 固定 64 条批次缓冲和 5 秒写超时，慢消费者明确断开；
- `oneshot_lifecycle_events` 持久化事实表；
- 终态事件、Task/Run 状态和通知 Outbox 同事务；
- Outbox 领取、lease、重试、delivered、dead-letter；
- 通知失败只重发通知，不调用 Executor，不重跑 Provider；
- 发送成功后保存具体出站消息 ID，为 Telegram 精确 reply binding 提供事实。

## 5. Red / Lock / Green

- Red：基线输出事件可持久化，但 Task/Run 生命周期缺少可回放事实源，完成通知也没有可靠 Outbox。
- Lock：重连 cursor、终态事务、发送失败重试、稳定投递幂等键、精确出站消息绑定和禁止 Executor 依赖用例已锁定。
- Green：通知模块 `go test -race`、WebSocket/Outbox 静态门禁和历史输出回归通过。

## 6. 审查中修复的问题

1. 初版只回放标准输出，缺少 Task/Run 生命周期事实；新增 lifecycle event 表和事务写入。
2. 终态通知曾由执行后异步追加，存在状态成功但通知事实丢失窗口；已合并到终态事务。
3. 通知重试曾可能重复发送结果消息；现使用稳定 transport delivery key，发送成功后再补精确 binding。
4. claude-code Provider 在全新数据库中可能触发外键失败；Migration 先创建安全别名记录，再兼容复制既有 claude 配置。
5. Task 生命周期曾以 topic 做全局唯一，retry/continue 的第二次 queued/running 事件会被静默丢弃；已改为按 Task version 唯一，只有单个 Run 内的同 topic 事件去重。

## 7. 影响范围

| 范围 | 影响 |
|---|---|
| API | Task/Run WS 可重放，HTTP 事件与 WS 共用不透明 cursor。 |
| 数据库 | 新增 lifecycle event、通知 Outbox 索引和精确 reply binding 索引。 |
| Telegram | 终态通知能够建立指向具体出站消息的 One-shot binding。 |
| Flutter | 可仅凭 REST+WS 恢复完整 Task/Run 状态。 |
| PTY | `session.*` namespace、Session stream 和 PTY EventBus 未修改。 |

## 8. 验证结果

- OD-OS-19 静态契约门禁：PASS；
- Notification `go vet`：PASS；
- Notification `go test -race`：PASS；
- Notification 覆盖率：45.5%；
- Store/Output/Saga/Recovery 历史回归：PASS；
- PTY Source Baseline：16/16 PASS；负向变异：5/5 PASS。

## 9. 未完成项

- 真实 PostgreSQL 多 Worker `SKIP LOCKED` 竞争、lease 过期和死信测试；
- Gateway 重启后的真实 WebSocket 回放与网络断线测试；
- 真实 Telegram/Slack/Discord/Feishu transport 幂等发送验证；
- 大量慢消费者和长时间流式输出压力测试。

## 10. 下一依赖任务

`OD-OS-20 — Telegram One-shot 命令、绑定与结果回传`
