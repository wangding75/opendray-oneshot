# OD-OS-10 — Shell One-shot Adapter 与 Executor 骨架执行报告

## 1. 状态

`PASS — 源码门禁完成；Go 1.25、真实 PostgreSQL 与最终本机运行门禁保留`

## 2. 时间

- 开始：`2026-07-28T09:22:29+08:00`
- 结束：`2026-07-28T09:38:00+08:00`
- 耗时：约 `16 分钟`

## 3. Git 基线

- 分支：`feat/oneshot-agent`
- 基线 Commit：`0132b07f42a4def8ec06f7b0134162dec534711b`
- 最终 Commit：本报告所在的 `OD-OS-10` 单一提交；精确 Hash 记录在交付回执与 Git Bundle 中。
- Push：仅推送到沙箱本地 Bare Remote，不是 GitHub。

## 4. 设计结论

本任务建立普通子进程 One-shot 最小执行链，不复用 Interactive PTY：

```text
Delivery Queue
  → RunService
  → OneShotAdapter
  → ProcessExecutor(os/exec)
  → terminal Run + Task
  → Delivery Ack
```

Shell Adapter 仅用于测试 Fixture：

- 默认关闭；
- 只接受预注册命令名；
- Prompt 不作为 Shell 源代码解释；
- 工作目录与可执行文件必须为绝对路径；
- 环境变量必须进入白名单；
- Secret 环境变量在诊断结构中固定显示为 `[REDACTED]`；
- 子进程不接收交互式 stdin。

## 5. 修改文件

### 新增

- `internal/oneshot/adapter/types.go`
- `internal/oneshot/adapter/shell.go`
- `internal/oneshot/adapter/shell_test.go`
- `internal/oneshot/executor/process.go`
- `internal/oneshot/executor/process_test.go`
- `internal/oneshot/executor/run_service.go`
- `internal/oneshot/executor/run_service_test.go`
- `internal/oneshot/testdata/fixtures/success.sh`
- `internal/oneshot/testdata/fixtures/nonzero.sh`
- `scripts/oneshot/check-oneshot-executor.py`
- `scripts/oneshot/check-oneshot-executor.sh`
- `docs/development/oneshot/evidence/OD-OS-10-validation.txt`
- `docs/development/oneshot/reports/OD-OS-10-summary.md`

### 修改

- `internal/oneshot/store/run.go`
- `internal/oneshot/store/postgres_integration_test.go`
- `docs/development/oneshot/OPENDRAY_ONESHOT_DEVELOPMENT_TASKBOOK.md`
- `docs/development/oneshot/task-state.yaml`

## 6. 核心实现

1. 定义 `CommandSpec`、`ExecutionInput`、`ExecutionResult` 和 `OneShotAdapter`。
2. 实现默认关闭的 Shell Fixture Adapter 与确定性命令白名单。
3. 使用 `exec.CommandContext` 启动普通子进程，不创建 PTY。
4. 记录并持久化 Run 的 PID、开始时间、退出码和结束时间。
5. 实现 `queue.Worker → RunService → ProcessExecutor` 成功与失败闭环。
6. 增加 `FinalizeRunWithTask` PostgreSQL 事务，原子写入 terminal Run 与 Task 结果，避免终态 Run 与 running Task 分裂。
7. 增加 disabled worker 工厂门禁；One-shot 关闭时不创建 Worker。
8. stdout/stderr 当前只接入 writer seam，正式有序采集留给 `OD-OS-11`。

## 7. 修复前失败测试

任务开始前以下能力不存在，因此对应测试不可编译或不存在：

- Shell Adapter 默认关闭与命令白名单；
- 成功退出、非零退出、命令不存在、cwd 不存在；
- 环境变量白名单与 Secret 脱敏；
- Worker → RunService → Executor 完整链；
- terminal Run 与 Task 原子持久化；
- One-shot 关闭时 Worker 不启动。

## 8. 修复后测试

- Adapter isolated `go vet`：PASS
- Adapter `go test -race`：PASS，覆盖率 `75.0%`
- Executor isolated `go vet`：PASS
- Executor `go test -race`：PASS，覆盖率 `61.8%`
- Store isolated `go test -race`：PASS
- PostgreSQL build-tag 编译：PASS
- Shell 成功退出：PASS
- Shell 非零退出：PASS
- 命令不存在：PASS
- cwd 不存在：PASS
- 环境白名单：PASS
- Secret 脱敏：PASS
- Worker → RunService → Executor：PASS
- disabled worker：PASS
- 原子 terminal Run/Task finalization：源码及 PostgreSQL-tag 测试编译 PASS；真实数据库运行待本机。

## 9. 模块与回归门禁

- `scripts/oneshot/check-oneshot-executor.sh`：PASS
- `scripts/oneshot/check-oneshot-queue.sh`：PASS
- One-shot architecture boundaries：PASS
- PTY Source Baseline：`16/16 PASS`
- PTY checker negative mutations：`5/5 PASS`
- i18n parity：PASS
- `git diff --check`：PASS

## 10. 影响范围

| 范围 | 影响 |
|---|---|
| API | 无新增路由。 |
| 数据库 | 无新表；Store 新增 terminal Run/Task 原子事务。 |
| Telegram | 无行为修改。 |
| Flutter | 无修改。 |
| PTY Session | 无生产代码修改；回归门禁通过。 |
| One-shot | 新增普通子进程执行骨架与测试 Shell Adapter。 |

## 11. 未完成与运行限制

以下项目没有在当前沙箱虚假标记为通过：

- Go 1.25 全仓 `go test ./...`、`go test -race ./...`、`go vet ./...` 和最终构建；
- 真实 PostgreSQL 下 `FinalizeRunWithTask` 原子提交测试；
- Flutter analyze、test 与 APK 构建；
- stdout/stderr 有序采集、Artifact 和 StandardEvent；
- 完整进程树取消与超时监督。

## 12. 下一任务

`OD-OS-11 — stdout/stderr 有序采集、Artifact 与 StandardEvent`
