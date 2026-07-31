# OD-OS-14 — One-shot Provider Capability 与 Adapter Registry 执行报告

## 1. 状态

`PASS — 源码门禁完成；真实 Provider 探测将在 Codex/Claude Adapter 阶段验证`

## 2. 时间与 Git 基线

- 开始：`2026-07-28T15:45:00+08:00`
- 结束：`2026-07-28T16:42:00+08:00`
- 基线 Commit：`019e251ba5f97f81dc8d21e6e806a259c8e591ca`
- 最终 Commit：本报告所在的 `OD-OS-12/13/14` 单一提交；精确 Hash 见交付回执和 Git Bundle。
- Push：仅推送沙箱本地 Bare Remote，不是 GitHub。

## 3. 设计结论

建立独立 One-shot Adapter Registry，公开 `SupportsNonInteractive`、`SupportsResume`、`StructuredOutput`、`Attachments`、`Cancellation`。共享边界只允许 CLI 路径、版本、启用状态、环境和凭据租约；禁止共享 PTY args、交互输入或 Session 类型。未知、禁用、重复注册和版本不兼容均返回稳定 One-shot 错误，绝不回退 PTY。

## 4. 修改文件

- `internal/oneshot/adapter/types.go`
- `internal/oneshot/adapter/registry.go`
- `internal/oneshot/adapter/registry_test.go`
- `internal/oneshot/adapter/shell.go`
- `scripts/oneshot/check-provider-registry.py`

## 5. Red / Lock / Green

- Red：OD-OS-11 基线缺少 Registry 和 capability contract，新门禁按预期失败。
- Lock：注册、重复注册、未知 Provider、禁用、版本不兼容、共享凭据和 capability 暴露用例已锁定。
- Green：Adapter 隔离 `go test -race` 与架构 import 门禁通过。

## 6. 能力和错误行为

- Provider/Adapter 双重启用状态；
- 最低 Provider 版本比较；
- capability descriptor 可供后续 API 和 Flutter 使用；
- credential acquire/release 通过窄接口；
- shared Provider metadata 深拷贝，避免外部修改；
- duplicate：`oneshot.run_conflict`；
- unknown/unsupported/version mismatch：`oneshot.unsupported_provider`；
- subsystem/provider disabled：`oneshot.disabled`；
- metadata lookup failure：`oneshot.provider_unavailable`。

## 7. 影响与边界

| 范围 | 影响 |
|---|---|
| API | 暂未新增路由；已提供稳定 Descriptor，供 OD-OS-18 暴露。 |
| 数据库 | 无 Migration。 |
| Telegram | 无命令修改。 |
| Flutter | 无代码修改；后续可按 capability 隐藏不支持操作。 |
| PTY Session | 未导入 Session 或 PTY Catalog 启动参数。 |
| One-shot | 为 Codex、Claude Code 和 RuntimeContext Adapter 提供统一注册入口。 |

## 8. 未完成项

- Codex 真实 CLI 版本和 capability 探测；
- Claude Code 真实 CLI 版本和 capability 探测；
- ProviderCatalog/CredentialAllocator 与应用层现有 Provider 服务的正式装配；
- Go 1.25 全仓门禁与 Flutter 门禁。

## 9. 下一任务

`OD-OS-15 — Codex One-shot Adapter`
