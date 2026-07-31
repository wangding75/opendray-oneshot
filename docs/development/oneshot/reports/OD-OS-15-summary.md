# OD-OS-15 — Codex One-shot Adapter 执行报告

## 1. 状态

`PASS — 源码实现和确定性门禁完成；真实 Codex CLI、登录凭据及 Go 1.25 全仓门禁待本机验证`

## 2. 时间与 Git 基线

- 开始：`2026-07-28T17:25:11+08:00`
- 实现提交：`2026-07-28T17:55:13+08:00`
- 基线 Commit：`f70358926821a1ec4209f6ac81bd182bb1d1ee76`
- 最终实现 Commit：`5e6e2130126af9f4138be07ac115f96dc97db494`
- Push：仅推送沙箱本地 Bare Remote，不是 GitHub。

## 3. 设计结论

Codex One-shot 使用普通子进程执行 `codex exec`，Prompt 通过 stdin 传递，启用 JSONL 输出并提取 `thread_id` 作为 Provider Context。恢复执行使用显式 `resume` 参数；认证、限流、恢复失败和未知输出均映射为稳定 One-shot 结果，不创建 PTY，也不回退 Interactive Session。

## 4. 修改文件

- `internal/oneshot/adapter/codex.go`
- `internal/oneshot/adapter/codex_test.go`
- `internal/oneshot/adapter/provider_common.go`
- `internal/oneshot/adapter/types.go`
- `internal/oneshot/adapter/shell.go`
- `internal/oneshot/executor/process_supervisor.go`
- `internal/oneshot/executor/process_supervisor_test.go`
- `internal/oneshot/testdata/codex/*.jsonl`

## 5. Red / Lock / Green

- Red：基线没有 Codex 非交互参数、JSONL、Context ID 和 resume contract，新门禁按预期失败。
- Lock：命令参数、stdin、workspace、JSONL、未知输出、认证、限流、恢复失败和取消输出用例已锁定。
- Green：Adapter/Executor 隔离 race test、静态边界和 provider source gate 通过。

## 6. 影响范围

| 范围 | 影响 |
|---|---|
| API | 暂无新路由，OD-OS-18 接入。 |
| 数据库 | 使用已有 Run、Artifact、StandardEvent、RuntimeContext Store，无新 Migration。 |
| Telegram | 无命令修改。 |
| Flutter | 无代码修改。 |
| PTY | 不使用 PTY；Interactive Session 行为未改。 |

## 7. 未完成项

- 沙箱未安装 Codex CLI，未执行本机 `--help`、版本和真实账号 smoke；
- 真实凭据下的 Context 延续、限流和取消验证；
- Go 1.25 全仓构建、race、vet、lint。

## 8. 下一任务

`OD-OS-16 — Claude Code One-shot Adapter`
