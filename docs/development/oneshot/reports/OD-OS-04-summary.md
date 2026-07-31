# OD-OS-04 执行报告

## 状态

PASS（源码实现与源码门禁通过；Go 1.25 全模块运行门禁待本机执行）

## 时间

- 开始：2026-07-27T14:59:23+0800
- 结束：2026-07-27T15:15:47+0800
- 耗时：16 分 24 秒

## Git

- 基线 Commit：`b96e212631ba195582dd4efea9575eb41119442a`
- 实现 Commit：`3b27cd0`
- 分支：`feat/oneshot-agent`
- Push：`sandbox-origin/feat/oneshot-agent` 成功

## 设计结论

Channel Transport、鉴权、持久化和命令处理继续保留在 Channel Core；普通入站消息在持久化后交给中立 `InboundDispatcher`。Dispatcher 通过确定性链执行，低优先级数值先运行，第一个 `handled` 立即终止后续处理。错误、panic 和 timeout 都是终止性结果，不再回落到 PTY，避免部分执行后重复投递。

本任务仅注册现有 Interactive PTY 兼容处理器。其内部 Session 路由状态仍暂存于 Hub，按任务书在 OD-OS-05 迁移至 `internal/session/channeladapter`。

## 修改文件

- `internal/channel/channel.go`
- `internal/channel/dispatch.go`
- `internal/channel/dispatch_test.go`
- `internal/channel/hub.go`
- `internal/channel/telegram/telegram.go`
- `internal/channel/telegram/telegram_test.go`
- `internal/app/app.go`
- `scripts/oneshot/check-channel-dispatch.sh`
- `docs/development/oneshot/evidence/OD-OS-04-source-gate.txt`
- `docs/development/oneshot/evidence/OD-OS-04-sandbox-compat-go-test.txt`

## 完成内容

- 新增中立 `ReplyAddress`、`Attachment` 和扩展后的 `ChannelMessage`。
- 新增 `InboundDispatcher`、`InboundDispatcherFunc`、`InboundDispatcherChain`。
- 同名 handler 重复注册时执行替换，不产生重复投递。
- 新增 handled、not-handled、failed、timed-out、panicked 审计事件。
- `handleInbound` 不再直接调用 PTY 写入，而是委托 dispatcher。
- 应用装配层仅注册 Interactive PTY 兼容处理器。
- Telegram 保留原 polling/offset 去重行为，并补充 `SourceMessageID`。
- 当前未创建任何 One-shot 领域对象、数据表或执行逻辑。

## 测试

### 新增测试

- Dispatcher 接收标准化消息与 ReplyAddress。
- 确定性优先级及 handled 后停止。
- 同名注册不重复执行。
- handled 不进入 Interactive fallback。
- not-handled 保留现有 PTY 路由。
- dispatcher error 不重复注入 PTY。
- panic 和 timeout 终止并生成审计事件。
- Telegram 下一次 `getUpdates` offset 前移。

### 已执行

- `scripts/oneshot/check-channel-dispatch.sh`：PASS。
- `scripts/oneshot/check-boundaries.sh`：PASS。
- `scripts/oneshot/check-pty-regression.sh`：源码门禁 15/15 PASS，负向变异 5/5 PASS。
- i18n parity：PASS。
- `git diff --check`：PASS。
- Go 1.23 + 本地最小依赖桩的 `internal/channel` 与 `internal/channel/telegram` 兼容性编译测试：PASS。

### 未执行

沙箱缺少 Go 1.25 且无法联网下载工具链与依赖，因此未执行真实仓库：

```bash
GOTOOLCHAIN=local go test ./internal/channel/...
```

该验证必须在本机调试阶段执行，不得视为已通过。

## 影响

- API：无新增 REST API。
- 数据库：无 Migration；现有 `channel_messages` 写入行为不变。
- Telegram：Transport、鉴权、命令、offset 去重和 PTY 回复行为保持；新增中立 SourceMessageID。
- Mobile：无修改。
- PTY：保留原 rune-by-rune 输入、target resolution 和 typing wait 行为。

## 遗留项

- OD-OS-05 将 Interactive Session 状态、Binding、target resolver 和 PTY input 从 Channel Hub 迁出。
- Go 1.25 真实测试、本机 Telegram/PTY 链路验证保留到本机调试门禁。

## 下一任务

`OD-OS-05 — Session Channel Adapter 迁移`

---

## 后续修复说明

初次完成后发现未知 Slash Command 不能进入执行域。该问题已在 Commit `ee337bf96c3370bf033ce5bea4b4963453510fad` 修复。完整证据见：

`docs/development/oneshot/reports/OD-OS-04-05-repair-summary.md`
