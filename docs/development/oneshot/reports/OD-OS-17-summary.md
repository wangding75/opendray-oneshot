# OD-OS-17 — RuntimeContext 与 Continue/Resume 执行报告

## 1. 状态

`PASS — 源码实现和确定性门禁完成；真实 Provider、PostgreSQL 和 Go 1.25 运行门禁待本机验证`

## 2. 时间与 Git 基线

- 开始：`2026-07-28T17:50:38+08:00`
- 实现提交：`2026-07-28T17:57:31+08:00`
- 基线 Commit：`5c1c84c5b77d3a03b8d51245598a0176fbb65e53`
- 最终实现 Commit：`033278ac4ef49c8e98c4db93c893fcab47af8b19`
- Push：仅推送沙箱本地 Bare Remote，不是 GitHub。

## 3. 设计结论

首次成功 Run 从 Provider 结构化输出捕获真实 Context ID，并创建独立 RuntimeContext。Continue 通过应用服务校验 principal、project、provider、workspace 和 context status，原子创建新 Delivery；Worker 原子获取 context busy lease、创建新 Run 与新普通子进程，并向 Provider 传入 resume context。resume 失败保留原 Context，不静默创建替代 Context。

## 4. 修改文件

- `internal/oneshot/application/continuation_service.go`
- `internal/oneshot/application/continuation_service_test.go`
- `internal/oneshot/executor/run_service.go`
- `internal/oneshot/executor/run_service_test.go`
- `internal/oneshot/queue/memory.go`
- `internal/oneshot/recovery/reconciler.go`
- `internal/oneshot/recovery/reconciler_test.go`
- `internal/oneshot/store/run.go`
- `internal/oneshot/store/runtime_context.go`
- `internal/oneshot/store/task_delivery.go`
- `scripts/oneshot/check-runtime-context-continuation.py`

## 5. Red / Lock / Green

- Red：基线缺少 Continue 应用入口、原子 busy lease、Provider Context 传递和崩溃释放逻辑。
- Lock：跨用户、项目、Provider、workspace、busy context、幂等冲突、并发 Continue、resume failure 和恢复释放用例已锁定。
- Green：Application、Executor、Recovery、Store race tests 和 source gate 通过。

## 6. 可靠性与不变量

- Continue 每次创建新 Run 和新进程，不复用旧进程；
- 同一 RuntimeContext 只允许一个 busy Run；
- Task/Delivery/Run/RuntimeContext/Saga 在关键阶段原子更新；
- 相同 Idempotency-Key 与相同 payload 返回原结果；不同 payload 返回冲突；
- Gateway 崩溃恢复会释放卡住的 busy Context；
- RuntimeContext 不引用 sessions 表，不导入 Session 或 PTY。

## 7. 影响范围

| 范围 | 影响 |
|---|---|
| API | 应用服务已就绪，REST 路由在 OD-OS-18 暴露。 |
| 数据库 | 复用既有 RuntimeContext、Delivery、Run、Saga、Idempotency 表；无新 Migration。 |
| Telegram | 无命令修改。 |
| Flutter | 无代码修改。 |
| PTY | 无依赖、无行为变更。 |

## 8. 未完成项

- 真实 Codex/Claude Context 延续行为验证；
- 真实 PostgreSQL 并发 Continue、事务回滚与崩溃恢复；
- Go 1.25 全仓构建、race、vet、lint；
- REST、WebSocket、Telegram 和 Flutter 闭环由后续任务完成。

## 9. 下一任务

`OD-OS-18 — Task/Run REST API、权限、审计与幂等`
