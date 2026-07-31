# OD-OS-21～23 汇总测试报告

## 结论

**源码门禁 PASS；Flutter 与完整运行时门禁待本机。**

本报告只把实际执行成功的检查标为 PASS。当前沙箱未安装 Flutter/Dart，且完整仓库要求 Go 1.25，因此对应门禁明确保留为待执行。

## 执行范围

| 范围 | 结果 | 证据 |
|---|---|---|
| OD-OS-21～23 Flutter feature 源码契约 | PASS | `evidence/OD-OS-21-23-validation.txt` |
| i18n 三语言 parity | PASS | 同上 |
| One-shot Page JSON 隔离测试 | PASS | 同上 |
| Provider One-shot capability 隔离测试 | PASS | 同上 |
| OD-OS-18～20 控制面源码回归 | PASS | 同上 |
| API `go test -race` | PASS，覆盖率 16.7% | 同上 |
| Channel Adapter `go test -race` | PASS，覆盖率 13.9% | 同上 |
| Notification `go test -race` | PASS，覆盖率 45.5% | 同上 |
| AppWire `go test -race` | PASS，覆盖率 18.8% | 同上 |
| task-state | PASS | 同上 |
| `git diff --check` | PASS | 同上 |
| PTY Source Baseline | PASS，16/16 | `evidence/OD-OS-21-23-pty-regression.txt` |
| PTY 负向变异 | PASS，5/5 | 同上 |
| Session Channel Adapter | PASS | 同上 |
| Channel Core | PASS | 同上 |
| Telegram、Slack、Discord、Feishu | PASS | 同上 |
| Flutter analyze/test/APK | 未执行 | Flutter/Dart 不可用 |
| Go 1.25 全仓门禁 | 未执行 | 当前沙箱 Go 1.23.2，下载被阻断 |
| 真实 PostgreSQL/真机 E2E | 未执行 | 无对应运行环境 |

## 新增测试资产

Flutter 测试共 4 组、660 行：

1. `agent_task_models_test.dart`
2. `agent_tasks_api_contract_test.dart`
3. `agent_tasks_stream_test.dart`
4. `agent_tasks_widget_test.dart`

Go 隔离契约测试：

1. `internal/oneshot/store/page_json_test.go`
2. `internal/catalog/handler_oneshot_test.go`
3. `internal/oneshot/appwire/catalog_test.go`

新增门禁：

1. `scripts/oneshot/check-mobile-agent-tasks.py`
2. `scripts/oneshot/check-mobile-agent-tasks.sh`

## 关键覆盖点

### OD-OS-21

- JSON 模型和所有生命周期状态。
- REST 分页和错误解析。
- WebSocket cursor、去重、认证失效和网络中断。
- 一级导航和独立 feature 边界。
- 三语言 79 个 key parity。
- Provider One-shot capability 不从 PTY 能力误推断。

### OD-OS-22

- 列表筛选、分页、刷新、空态、错误态和离线态。
- 创建与继续请求严格分离。
- Idempotency-Key 重用和表单变化后重置。
- Provider resume/attachment 能力控制。
- 小屏布局和创建成功导航。

### OD-OS-23

- 历史事件跨页回放。
- cursor 重连、重复事件去重、Run 切换隔离。
- stdout/stderr/raw 输出。
- Continue、Cancel、Retry 状态。
- Artifact SHA、size、Content-Length、Digest、ETag 完整性校验。
- 状态时间线和文件打开动作。

## 已知限制

- 源码门禁能验证目录边界、契约字段、测试存在、i18n 和隔离 Go 逻辑，不能替代 Dart 编译器。
- 4 组 Flutter 测试已经交付，但当前环境没有执行结果。
- WebSocket 后台恢复、超大输出滚动性能、系统文件打开需要 Android/iOS 真机验证。
- `/api/v1/providers` 的可选 `oneshot` 扩展已通过隔离测试，完整 Go 1.25 App 构建仍待执行。

## 最终状态

```text
OD-OS-21 source gate: PASS
OD-OS-22 source gate: PASS
OD-OS-23 source gate: PASS
Flutter runtime gate: PENDING
Go 1.25 full-repository gate: PENDING
PostgreSQL/mobile E2E gate: PENDING
```
