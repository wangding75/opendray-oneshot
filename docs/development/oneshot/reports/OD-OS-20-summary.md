# OD-OS-20 — Telegram One-shot 命令、绑定与结果回传执行报告

## 1. 状态

`PASS — 源码实现、隔离 race 测试和 PTY 回归完成；真实 Telegram Bot 与 PostgreSQL E2E 待本机验证`

## 2. 时间与 Git 基线

- 开始：`2026-07-28T19:46:40+08:00`
- 结束：`2026-07-28T20:55:00+08:00`
- 基线 Commit：`2f93ab8b1397e3f8a8787afd31f8c3673bb0b747`
- 最终 Commit：本报告所在的 `OD-OS-18/19/20` 单一提交；精确 Hash 见交付回执和 Git Bundle。
- Push：仅推送沙箱本地 Bare Remote，不是 GitHub。

## 3. 设计结论

Telegram One-shot 使用明确命令进入，普通文本继续保留现有 PTY fallback。One-shot Adapter 优先级为 100，Session Adapter 为 1000；只有带具体 One-shot 出站消息 ID 的回复才会继续对应 Task。主体使用 Telegram 数字用户 ID，不使用可变 username。绑定键同时包含 principal、channel、conversation、thread 和 source message，防止同线程多个任务、多个用户以及 PTY/One-shot 双执行域互相截获。

## 4. 核心交付

- `/run`、`/tasks`、`/task`、`/continue`、`/cancel`、`/retry`；
- provider/project/workspace 显式参数和按钮；
- Telegram Update 来源幂等键；
- chat/thread/source message/reply address 独立 binding；
- 精确 reply-to-One-shot 继续 Task；
- 普通文本和 reply-to-PTY 返回 `not_handled`，继续 Session 链路；
- 完成结果摘要、Artifact 链接和继续按钮；
- 同线程多 One-shot 结果可并存，不覆盖绑定。

## 5. Red / Lock / Green

- Red：基线 Telegram 只有 PTY Session 命令，没有 One-shot 命令和独立绑定。
- Lock：重复 Update、稳定数字主体、跨 chat/thread/user、同线程多任务、reply-to-One-shot、reply-to-PTY 和普通文本 fallback 用例已锁定。
- Green：Channel Adapter 隔离 `go test -race`、Telegram 静态门禁、Session Channel Adapter 与四种 Channel transport 回归通过。

## 6. 审查中修复的问题

1. 初版按宽泛 thread binding 解析回复，同线程同时存在 One-shot 与 PTY 时可能误截获；已改为具体出站消息 ID 精确匹配。
2. 初版主体可能使用 Telegram username，改名后身份不稳定；已改为 `tg_user_id` 数字 ID。
3. 原绑定唯一约束无法容纳同用户同线程多个任务；已改为包含 source message 的表达式唯一索引。
4. 通知发送和 binding 落库之间可能因数据库短暂失败造成重复消息；已加入稳定投递幂等键和发送后绑定恢复。

## 7. 影响范围

| 范围 | 影响 |
|---|---|
| API | Telegram 命令复用 OD-OS-18 Control/Application 服务，不建平行实现。 |
| 数据库 | Channel binding 增加具体出站消息维度。 |
| Telegram | 新增明确 One-shot 命令和结果回复闭环。 |
| Flutter | 无移动端代码修改。 |
| PTY | Session Adapter 优先级和普通文本 fallback 保持不变。 |

## 8. 验证结果

- OD-OS-20 静态契约门禁：PASS；
- Channel Adapter `go vet`：PASS；
- Channel Adapter `go test -race`：PASS；
- Channel Adapter 覆盖率：13.9%；
- Session Channel Adapter race 测试：PASS；
- Telegram/Slack/Discord/Feishu transport race 回归：PASS；
- PTY Source Baseline：16/16 PASS；负向变异：5/5 PASS；i18n parity：PASS。

## 9. 未完成项

- 真实 Telegram Bot 命令、按钮、超长文本和附件 E2E；
- 真实 PostgreSQL 同线程多任务/多用户 binding 并发测试；
- Telegram provider/project 真实可用列表与权限联调；
- Flutter Agent Tasks 页面和通知策略由 OD-OS-21～24 完成。

## 10. 下一任务

`OD-OS-21 — Flutter Agent Tasks 数据层与导航基础`
