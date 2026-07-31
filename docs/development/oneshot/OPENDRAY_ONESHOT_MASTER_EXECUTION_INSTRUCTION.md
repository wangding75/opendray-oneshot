# OpenDray One-shot Agent 连续执行总指令

本任务为执行任务，直接执行当前任务。

仓库：OpenDray Fork
基线分支：`main`
开发分支：`feat/oneshot-agent`
任务书：`docs/development/oneshot/OPENDRAY_ONESHOT_DEVELOPMENT_TASKBOOK.md`

## 执行要求

1. 先确认或创建 `feat/oneshot-agent`，不得在 `main` 开发。
2. 完整读取任务书的全局规则、当前任务详情、依赖和验收标准。
3. 从第一个未完成任务开始，严格按 `OD-OS-00` 到 `OD-OS-26` 顺序执行。
4. 每次只执行一个任务。当前任务未 PASS、Commit、Push、记录状态，不得进入下一任务。
5. 每个任务先做代码现状分析和小型实施方案，再写能捕获问题的失败测试，再实现。
6. 禁止重新制定整体架构、合并任务、提前实现后续任务、绕过冻结契约或削弱测试。
7. 普通代码缺陷、测试失败、环境问题、脚本误判和验收缺口必须自行定位并修复，直至 PASS。
8. 只有必须破坏冻结契约、覆盖来源不明修改、引入重大新依赖、需要外部凭据或无法恢复的外部故障时才允许 BLOCKED。
9. 每个任务独立 Commit 和 Push；禁止 `git add .`、amend、rebase、squash、force push。
10. 每个任务更新任务书复选框、`task-state.yaml` 和对应 summary 报告。
11. 中断恢复时读取 Git 状态、task-state、任务书和报告，从最后未完成阶段继续，不重新执行已完成任务。

## 强制架构边界

- 保留现有 PTY Session 全部功能。
- One-shot 不得使用 PTY，不得调用 Session Manager，不得写 Session 表或 RingBuffer。
- 禁止 `Session.Mode = pty | oneshot`。
- Channel Transport 可以共享；收到消息后的 Interactive Handler 和 One-shot Handler 必须分开。
- RuntimeContext 与 PTY Session 完全无关。
- Provider 不支持非交互时明确拒绝，不得回退 PTY。
- Telegram 不得根据“最近对象”模糊猜测模式。
- TG 与 Flutter Mobile 使用同一 Task/Run 后端事实。

## 每个任务开始前

```bash
git branch --show-current
git rev-parse HEAD
git rev-parse 'HEAD@{upstream}'
git status --short --untracked-files=all
git diff --cached --name-status
```

发现来源不明修改时禁止覆盖，输出 BLOCKED 报告。

## 每个任务固定流程

```text
读取
→ 现状/根因
→ 当前任务方案
→ 失败测试 RED
→ 开发
→ 最小验证
→ 模块验证
→ 范围检查
→ Commit
→ Push
→ 报告和状态
→ 下一任务
```

## 当前入口

立即执行 `OD-OS-00 开发分支、环境预检与基线登记`。

持续执行后续任务；只有出现需要用户决策的真实阻塞才停止。
