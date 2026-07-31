# OD-OS-09 完成报告：Delivery Queue、Lease、幂等与死信

## 1. 状态

**PASS（源码门禁） / PENDING（本机运行门禁）**

- PostgreSQL Queue、Worker Lease、幂等、重试、死信、取消及崩溃恢复源码实现完成。
- 隔离 `go vet`、`go test -race`、PostgreSQL build-tag 编译及全部上游源码回归门禁通过。
- 沙箱没有真实 PostgreSQL、Go 1.25、Flutter/Dart，因此真实数据库并发/恢复集成测试与全仓运行门禁未执行，不能标记为通过。

## 2. 时间与基线

- 开始时间：2026-07-27T23:42:09+0800
- 结束时间：2026-07-28T00:20:02+0800
- 耗时：约 38 分钟
- 基线 Commit：`962c3218b2c4ebb0f8e27e9198b3f6ae441035d6`
- 最终 Commit：本报告所在的 OD-OS-09 单一提交；精确 Hash 记录在交付回执和 Git Bundle 中。
- 分支：`feat/oneshot-agent`

## 3. 设计结论

One-shot 执行投递必须使用 PostgreSQL 作为唯一可靠队列事实源。领取、租约、重试、死信、幂等和取消均由数据库事务及数据库时间控制，不能依赖进程内内存状态，也不能复用 Channel Notification Delivery 或 Interactive PTY Session。

核心原则：

1. `FOR UPDATE OF d SKIP LOCKED` 保证多 Worker 竞争时单次领取；
2. `clock_timestamp()` 是租约和重试时间的唯一权威；
3. Worker 在执行期间周期续租，避免长任务完成后因 lease 过期无法 Ack；
4. 过期 lease 仅在 `run_id IS NULL` 时重新领取；已经拥有 Run 的 Delivery 不允许产生第二个 Run；
5. terminal Run 的过期 Delivery由队列恢复流程确认，不重复执行；
6. API/Mobile/Web 必须提供 Idempotency-Key；Telegram 根据 Channel 与源消息生成稳定 key；
7. 同 key 同 payload 返回原始 Task/Delivery；同 key 不同 payload 返回 `oneshot.idempotency_conflict`；
8. 重试达到 `max_attempts` 后进入 dead-letter，并将仍处于 queued 的 Task 标记为 failed。

## 4. 主要修改

### Queue 与 Worker

新增 `internal/oneshot/queue/`：

- PostgreSQL Queue：enqueue、claim、renew、ack、nack、dead-letter、cancel；
- Serializable Task + Delivery + Idempotency 原子创建；
- 多 Worker `SKIP LOCKED` 竞争；
- lease_owner、lease_until、attempt、available_at、max_attempts；
- 指数退避、最大尝试、死信；
- Worker 执行期间自动续租；
- Processor panic 转为受控可重试错误；
- terminal Run 恢复确认；
- 结构化 Queue 审计事件；
- MemoryQueue 作为竞态与状态测试实现。

### Application Dispatch

新增 `internal/oneshot/application/dispatch_service.go`：

- 创建并校验 Task 与初始 Delivery；
- API、Mobile、Web Idempotency-Key 强制校验；
- Telegram 稳定幂等键；
- canonical payload SHA-256，覆盖 Owner、Project、Provider、Source、Prompt、附件、Options 和 max_attempts；
- 原始创建响应持久化并用于幂等回放。

### 测试与门禁

- 多 Worker 单赢家；
- lease 过期重新领取；
- Worker 长处理自动续租；
- ack/nack/backoff/dead-letter/cancel；
- 最大重试耗尽；
- Idempotency replay 与 payload conflict；
- Telegram 稳定去重；
- terminal Run 防重复执行；
- PostgreSQL build-tag 集成测试；
- 静态 SQL INSERT 列数、重复列和 placeholder 连续性检查；
- `check-oneshot-queue.py` 与 `check-oneshot-queue.sh`。

## 5. 修复前失败条件

基线没有 Queue、Worker 或 DispatchService，以下验收条件均不成立：

- 多 Worker 竞争；
- lease 领取、续租和过期恢复；
- ack/nack/backoff/dead-letter/cancel；
- API/Telegram 幂等；
- 重启后防止重复成功 Run。

证据：`docs/development/oneshot/evidence/OD-OS-09-red.txt`。

## 6. 验证结果

通过：

- OD-OS-09 静态源码契约；
- Queue/Application `gofmt`；
- Domain/Queue/Application 隔离 `go vet`；
- Domain/Queue/Application 隔离 `go test -race`；
- PostgreSQL build-tag 测试编译；
- Worker lease heartbeat 测试；
- OD-OS-08 Store 门禁；
- OD-OS-07 Domain 门禁；
- OD-OS-04/05 已知问题回归门禁；
- PTY Source Baseline 16/16；
- PTY 负向变异 5/5；
- Telegram、Slack、Discord、Feishu 隔离回归；
- i18n parity；
- task-state 一致性；
- `git diff --check`。

待本机：

- 真实 PostgreSQL 多 Worker 竞争；
- 真实 PostgreSQL lease expiry/reclaim；
- 真实 PostgreSQL 幂等并发事务；
- Gateway 重启恢复；
- Go 1.25 全仓测试、vet、lint 和 build；
- Flutter analyze/test/build。

完整证据：`docs/development/oneshot/evidence/OD-OS-09-validation.txt`。

## 7. 影响评估

- API：未新增 HTTP 路由；新增后续 API 可调用的 DispatchService。
- 数据库：复用 OD-OS-08 已冻结的 One-shot 表和约束；本阶段没有修改 Session 表。
- Telegram：新增稳定源消息幂等算法；尚未接入 Telegram 命令入口。
- 移动端：无代码修改；后续创建 Task API 必须提供 Idempotency-Key。
- PTY：无生产代码修改；全部 PTY 源码回归门禁通过。
- 安全：Queue 不导入 Session、Channel 或 PTY，不执行命令，不保存 secret。

## 8. Commit 与 Push

- 建议/实际提交信息：`feat(oneshot): add reliable postgres delivery queue`
- 提交数量：1
- Push 目标：本地沙箱 Bare Remote；不是 GitHub Remote。
- 精确 Commit 与 Push 结果见最终交付回执。

## 9. 未完成项

运行级验证必须在具备以下环境的本机执行：

```bash
OPENDRAY_DEV_DB_URL='postgres://...' scripts/oneshot/check-oneshot-queue.sh
```

同时还需要 Go 1.25 和 Flutter 3.41+/Dart 3.11+ 执行最终全仓门禁。

## 10. 下一任务

`OD-OS-10 — Shell One-shot Adapter 与 Executor 骨架`
