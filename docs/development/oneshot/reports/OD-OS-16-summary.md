# OD-OS-16 — Claude Code One-shot Adapter 执行报告

## 1. 状态

`PASS — 源码实现和确定性门禁完成；真实 Claude Code CLI、登录凭据及 Go 1.25 全仓门禁待本机验证`

## 2. 时间与 Git 基线

- 开始：`2026-07-28T17:25:11+08:00`
- 实现提交：`2026-07-28T17:55:23+08:00`
- 基线 Commit：`f70358926821a1ec4209f6ac81bd182bb1d1ee76`
- 最终实现 Commit：`5c1c84c5b77d3a03b8d51245598a0176fbb65e53`
- Push：仅推送沙箱本地 Bare Remote，不是 GitHub。

## 3. 设计结论

Claude Code One-shot 使用普通子进程执行 print 模式，Prompt 通过 stdin 传递，采用 `stream-json` 输出并提取 `session_id`。恢复执行显式使用 Provider resume 参数；认证、限流、权限拒绝、恢复失败和未知事件均保留原始输出并映射为稳定 One-shot 结果，不创建 PTY。

## 4. 修改文件

- `internal/oneshot/adapter/claude.go`
- `internal/oneshot/adapter/claude_test.go`
- `internal/oneshot/testdata/claude/*.jsonl`

## 5. Red / Lock / Green

- Red：基线没有 Claude 非交互参数、stream JSON、Context ID 和 resume contract。
- Lock：命令参数、stdin、模型、权限模式、session ID、认证、限流、权限拒绝和恢复失败用例已锁定。
- Green：Adapter 隔离 race test、fixture 和 provider source gate 通过。

## 6. 影响范围

| 范围 | 影响 |
|---|---|
| API | 暂无新路由，OD-OS-18 接入。 |
| 数据库 | 使用已有 One-shot Store，无新 Migration。 |
| Telegram | 无命令修改。 |
| Flutter | 无代码修改。 |
| PTY | 不使用 PTY；Interactive Session 行为未改。 |

## 7. 未完成项

- 沙箱未安装 Claude Code CLI，未执行本机帮助、版本和真实账号 smoke；
- 真实权限模式、Context 延续、限流和取消验证；
- Go 1.25 全仓构建、race、vet、lint。

## 8. 下一任务

`OD-OS-17 — RuntimeContext 与 Continue/Resume`
