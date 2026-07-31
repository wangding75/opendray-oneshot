# OD-OS-12 — ProcessSupervisor、取消、超时与进程树执行报告

## 1. 状态

`PASS — 源码门禁完成；Go 1.25 与目标平台真实运行门禁待本机`

## 2. 时间与 Git 基线

- 开始：`2026-07-28T15:45:00+08:00`
- 结束：`2026-07-28T16:42:00+08:00`
- 基线 Commit：`019e251ba5f97f81dc8d21e6e806a259c8e591ca`
- 最终 Commit：本报告所在的 `OD-OS-12/13/14` 单一提交；精确 Hash 见交付回执和 Git Bundle。
- Push：仅推送沙箱本地 Bare Remote，不是 GitHub。

## 3. 设计结论

One-shot 普通子进程改由独立 `ProcessSupervisor` 管理：Linux 使用独立进程组，取消和超时均执行 TERM → grace period → KILL，同一 `cmd.Wait` 在进程树清理后完成 stdout/stderr 排空。Windows 原生平台明确拒绝进程树监督，要求 WSL2/Linux，不自动降级到 PTY 或 Session 信号实现。

## 4. 修改文件

- `internal/oneshot/executor/process.go`
- `internal/oneshot/executor/process_supervisor.go`
- `internal/oneshot/executor/process_supervisor_linux.go`
- `internal/oneshot/executor/process_supervisor_unix.go`
- `internal/oneshot/executor/process_supervisor_windows.go`
- `internal/oneshot/executor/process_supervisor_test.go`
- `internal/oneshot/executor/run_service.go`
- `scripts/oneshot/check-process-supervisor.py`
- `scripts/oneshot/check-oneshot-runtime-core.sh`

## 5. Red / Lock / Green

- Red：在 OD-OS-11 基线运行新门禁，因缺少 `process_supervisor.go` 按预期失败，证据见 `evidence/OD-OS-12-14-red.txt`。
- Lock：多级子进程、TERM-ignore、超时、输出排空和重复取消用例已固定在 `process_supervisor_test.go`。
- Green：专用静态门禁和隔离 `go test -race` 均通过。

## 6. 验证覆盖

- 父进程和多级子进程整树终止；
- timeout 使用相同清理链；
- TERM 无响应时 KILL fallback；
- 终止前 stdout/stderr 完整保留；
- 重复取消幂等；
- leader 自然退出但后台 descendant 继承输出管道时，仍能整树清理且 Wait 不挂死；
- 外部遗留进程组启动恢复；
- Linux zombie-only 进程组不会误判为仍在运行；
- Windows 原生 capability 明确不支持。

## 7. 影响与边界

| 范围 | 影响 |
|---|---|
| API | 无新增路由；为后续取消 API 提供可靠执行边界。 |
| 数据库 | 无直接表结构修改。 |
| Telegram | 无交互修改。 |
| Flutter | 无修改。 |
| PTY Session | 未复用或修改 Session 信号代码；架构门禁通过。 |
| One-shot | 取消和超时不再仅修改状态，必须确认进程树清理结果。 |

## 8. 未完成项

- Go 1.25 全仓构建、race、vet、lint；
- macOS 真实进程组测试；
- WSL2 真实进程树测试；
- Windows 原生明确拒绝行为的真实平台测试；
- Flutter 门禁。

## 9. 下一任务

`OD-OS-13 — 执行 Saga、失败补偿与崩溃恢复`
