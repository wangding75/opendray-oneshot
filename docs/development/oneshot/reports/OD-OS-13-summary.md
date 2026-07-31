# OD-OS-13 — 执行 Saga、失败补偿与崩溃恢复执行报告

## 1. 状态

`PASS — 源码门禁完成；真实 PostgreSQL 故障恢复门禁待本机`

## 2. 时间与 Git 基线

- 开始：`2026-07-28T15:45:00+08:00`
- 结束：`2026-07-28T16:42:00+08:00`
- 基线 Commit：`019e251ba5f97f81dc8d21e6e806a259c8e591ca`
- 最终 Commit：本报告所在的 `OD-OS-12/13/14` 单一提交；精确 Hash 见交付回执和 Git Bundle。
- Push：仅推送沙箱本地 Bare Remote，不是 GitHub。

## 3. 设计结论

执行链冻结为持久化 Saga：`run_created → credential_acquired → command_built → process_started → running_persisted → process_exited → output_committed → terminal_persisted → credential_released → acknowledged`。Run 与初始 Saga checkpoint 在同一事务中创建，完成进程但 ACK 失败时只恢复提交，不重新执行 Provider。

## 4. 修改文件

- `internal/oneshot/saga/types.go`
- `internal/oneshot/saga/types_test.go`
- `internal/oneshot/executor/run_service.go`
- `internal/oneshot/executor/run_service_test.go`
- `internal/oneshot/executor/run_service_saga_test.go`
- `internal/oneshot/recovery/reconciler.go`
- `internal/oneshot/recovery/reconciler_test.go`
- `internal/oneshot/store/run.go`
- `internal/oneshot/store/saga.go`
- `internal/oneshot/store/postgres_integration_test.go`
- `internal/oneshot/queue/types.go`
- `internal/oneshot/queue/worker.go`
- `internal/oneshot/queue/worker_test.go`
- `internal/oneshot/queue/memory.go`
- `internal/oneshot/queue/memory_test.go`
- `internal/oneshot/queue/postgres.go`
- `internal/store/migrations/0084_oneshot_run_saga.sql`
- `scripts/oneshot/check-run-saga.py`

## 5. Red / Lock / Green

- Red：OD-OS-11 基线缺少 Saga、Reconciler 和 Migration，新门禁按预期失败。
- Lock：Run 创建、启动、输出、Artifact、ACK、崩溃 checkpoint、重启恢复和凭据释放故障用例已锁定。
- Green：Saga、Recovery、Queue、Store 和 Executor 隔离 race 门禁通过；PostgreSQL build-tag 编译通过。

## 6. 一致性与恢复保证

- Task、Delivery、Run 和 `run_created` checkpoint 原子创建；
- failure stage、primary error、compensation error 可审计；
- Processor 返回 `ActionRecover` 时不重启已绑定 Run；
- Queue ACK 成功但 AckObserver checkpoint 失败时，Reconciler 可继续关闭 Saga；
- 终态 Run 的恢复只执行 credential release、recovered ACK 和 acknowledged checkpoint；
- starting/running/collecting_output 仅在 Delivery lease 过期后进入恢复，避免 Reconciler 误杀活跃 Worker；
- 启动恢复失败时 Reconciler fail-closed，不允许静默进入正常执行；
- 凭据租约 ID 持久化，可在 Gateway 重启后释放。

## 7. 影响与边界

| 范围 | 影响 |
|---|---|
| API | 无新增路由。 |
| 数据库 | 新增 `oneshot_run_sagas` Migration；未修改 Session 表。 |
| Queue | 新增 recovered ACK 和显式 `ActionRecover` 语义。 |
| Telegram | 无交互修改。 |
| Flutter | 无修改。 |
| PTY Session | 无 import、无状态复用、无 Migration 引用。 |
| One-shot | 形成可审计、可补偿、可重启恢复的执行生命周期。 |

## 8. 未完成项

- 真实 PostgreSQL 下 Migration、事务回滚、并发恢复和 ACK 故障注入；
- Gateway 真实重启期间的 OS 进程残留测试；
- Go 1.25 全仓门禁；
- Flutter 门禁。

## 9. 下一任务

`OD-OS-14 — One-shot Provider Capability 与 Adapter Registry`
