# OD-OS-18 / 19 / 20 测试报告

## 1. 总结

- 结论：`PASS — 源码门禁与当前沙箱可执行测试通过`
- 基线：`2f93ab8b1397e3f8a8787afd31f8c3673bb0b747`
- 测试环境：Linux 沙箱、Go 1.23.2 隔离兼容模块、无 Flutter、无真实 PostgreSQL。
- 完整运行时结论：`PENDING`，未伪报 Go 1.25、真实 PostgreSQL、真实 Telegram 或 Flutter 门禁。

## 2. 新增任务专项测试

| 模块 | 命令/门禁 | 结果 | 覆盖率 |
|---|---|---:|---:|
| OD-OS-18 REST/API | `check-control-api.py` | PASS | — |
| API | `go vet` + `go test -race -cover` | PASS | 16.7% |
| OD-OS-19 WS/Outbox | `check-replay-outbox.py` | PASS | — |
| Notification | `go vet` + `go test -race -cover` | PASS | 45.5% |
| OD-OS-20 Telegram Adapter | `check-telegram-oneshot.py` | PASS | — |
| Channel Adapter | `go vet` + `go test -race -cover` | PASS | 13.9% |
| App wiring adapter | `go vet` + compile | PASS | 0.0% |

## 3. 历史 One-shot 回归

| 模块 | 结果 | 覆盖率/说明 |
|---|---:|---|
| Adapter | PASS | 77.4% |
| Application | PASS | 58.5% |
| Executor | PASS | 67.1% |
| Queue | PASS | 31.4% |
| Recovery | PASS | 59.9% |
| Saga | PASS | 78.6% |
| Store | PASS | race + PostgreSQL build-tag 编译 |
| Domain | PASS | 68.6% |
| Windows/macOS 交叉编译 | PASS | 隔离 Provider/Executor 门禁 |
| 架构边界 | PASS | One-shot 无 Session/PT​Y 依赖越界 |

## 4. PTY 与 Channel 回归

| 检查 | 结果 |
|---|---:|
| PTY Source Baseline | 16/16 PASS |
| PTY 负向变异 | 5/5 PASS |
| Session Channel Adapter | PASS |
| Channel Core | PASS |
| Telegram Transport | PASS |
| Slack Transport | PASS |
| Discord Transport | PASS |
| Feishu Transport | PASS |
| i18n parity | PASS |

## 5. 已锁定的关键失败场景

- 缺少 scope、错误 scope、跨主体和跨项目访问；
- create/continue/retry 缺幂等键或同键不同 payload；
- 审计泄露原始幂等键；
- 可猜测 Run event cursor；
- Task retry/continue 后重复 lifecycle topic 被唯一约束吞掉；
- Outbox 发送失败触发 Provider 重跑；
- 通知重复发送或无法恢复精确 reply binding；
- Telegram 可变 username 作为身份；
- 同线程多个 Task、多个用户以及 PTY/One-shot 回复串线；
- 普通文本被错误路由为 One-shot。

## 6. 未执行门禁

1. Go 1.25 全仓 `test -race`、`vet`、`lint`、build；
2. 真实 PostgreSQL Migration、并发 lease、事务回滚、ACK/Outbox 故障与重启恢复；
3. Linux/macOS/WSL2 真实进程树取消压力测试；
4. 真实 Telegram Bot 命令、按钮、附件和回复 E2E；
5. Flutter analyze、test 和 APK 构建；
6. 浏览器/移动端 WebSocket 断线重连、慢消费者和压力测试。

## 7. 证据

- `docs/development/oneshot/evidence/OD-OS-18-20-validation.txt`
- `docs/development/oneshot/evidence/OD-OS-18-20-pty-regression.txt`
