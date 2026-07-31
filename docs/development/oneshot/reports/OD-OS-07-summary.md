# OD-OS-07 完成报告：One-shot 领域模型与状态机实现

## 1. 状态

**PASS — 源码实现、纯领域单元测试、冻结契约一致性和架构边界门禁通过；Go 1.25 全仓运行验证按用户决定保留到本机最终调试。**

## 2. 时间

- 开始：2026-07-27T18:31:04+0800（首个任务文件写入时间）
- 结束：2026-07-27T18:53:24+0800
- 耗时：22 分 20 秒

## 3. Git 基线与提交

- 基线 Commit：`a84f08863ae2a9660a0b4bb7d60c885671132d83`
- 实现 Commit：`6444559afd5ac897e2dabacc7a0cae40bb2eabf3`
- 实现提交说明：`feat(oneshot): add isolated domain model and state machines`
- Push 目标：本次报告与任务状态提交后统一推送到本地沙箱 Remote `sandbox-origin/feat/oneshot-agent`。
- 说明：该 Remote 是 `/mnt/data` 中的本地 Bare Repository，不是 GitHub；最终回执以实际 Push 结果为准。

## 4. 设计结论

新增完全隔离的纯领域包：

```text
internal/oneshot/domain
```

该包只包含：

- 领域聚合和不可变值对象；
- 稳定 ID 与错误码；
- 显式状态转换；
- 所有权、项目、Provider、Run、RuntimeContext 约束；
- 持久化/API Snapshot 与受校验的 Restore 入口；
- 纯单元测试和冻结契约一致性检查。

该包不包含：

- PostgreSQL、Migration 或 Store；
- HTTP、WebSocket 或路由；
- Channel、Telegram 或通知发送；
- 进程启动、PTY、Session、PID 管理或 Executor。

## 5. 实现内容

### 领域资源

实现冻结契约中的 7 个资源：

1. `Task`
2. `Delivery`
3. `Run`
4. `RuntimeContext`
5. `StreamRecord`
6. `StandardEvent`
7. `Artifact`

### 状态机

实现 4 套显式状态机：

- Task：8 个状态、16 条合法边；
- Delivery：6 个状态、8 条合法边；
- Run：9 个状态、15 条合法边；
- RuntimeContext：4 个状态、6 条合法边。

聚合状态字段保持私有，外部不能直接赋值；状态变化只能调用领域命令方法。Restore 入口只接受满足结构和不可变量的持久化 Snapshot。

### 核心不可变量

- Task 的 principal、project、provider、source 和原始 prompt 不可变；
- Source 和 ReplyAddress 使用深拷贝快照；
- 一个 Task 同时最多一个活动 Run；
- 一个 Delivery 最多绑定一个 Run；
- Continue 必须使用 owner/project/provider 完全匹配且已获取的 RuntimeContext；
- RuntimeContext `busy` 阻止并发恢复；
- `invalid`、`revoked`、Run 终态、Delivery 终态和 Task `cancelled` 不可逆；
- Run 终态必须写入 `finished_at`；
- 取消和超时只有在清理已确认后才能进入终态；
- 输出未持久化时不能完成 Run；
- StreamRecord 和 StandardEvent 提供同 Run 严格递增序列验证；
- Artifact 使用 SHA-256、相对 storage key 和 Task 派生所有权；
- RuntimeContext 不包含 Session ID、PID、PTY 或进程句柄。

### 稳定错误

实现冻结契约中的 26 个 `oneshot.*` 错误码、稳定 `DomainError`、错误码提取、包装和 retryable 映射。

## 6. Red 证据

基线 `a84f088...` 中不存在：

```text
internal/oneshot/domain
```

也不存在 One-shot Task 聚合、状态转换和领域测试。

证据：`docs/development/oneshot/evidence/OD-OS-07-red.txt`。

## 7. Green 验证

执行：

```text
scripts/oneshot/check-oneshot-domain.sh
```

结果：PASS。

验证覆盖：

- Go 格式；
- 7 个 Snapshot 字段和 ID 前缀与冻结契约完全一致；
- 4 套状态集合与冻结状态机完全一致；
- 26 个错误码及 retryable 属性完全一致；
- 聚合状态字段没有导出；
- 无 Session、Channel、PTY、HTTP、PostgreSQL 依赖；
- `go vet`；
- `go test -race`；
- 语句覆盖率 68.6%；
- 45 条合法状态边；
- 152 个非法状态对；
- principal/project/provider Context 越权；
- Task 双活动 Run 冲突；
- Source、ReplyAddress、DeliveryInput、StandardEvent、Artifact 深拷贝；
- 终态不可逆；
- 取消、超时、输出持久化守卫；
- 冻结契约校验器及其负向自测；
- 双执行域架构边界；
- OD-OS-04/05 已知问题回归；
- PTY Source Baseline 16/16；
- PTY 负向变异 5/5；
- i18n parity；
- task-state 一致性；
- `git diff --check`。

完整日志：`docs/development/oneshot/evidence/OD-OS-07-validation.txt`。

## 8. 修改文件

### 生产代码

```text
internal/oneshot/domain/*.go
```

主要文件：

- `task.go`
- `delivery.go`
- `run.go`
- `runtime_context.go`
- `stream_record.go`
- `standard_event.go`
- `artifact.go`
- `errors.go`
- `id.go`
- `owner.go`
- `source.go`

### 测试

```text
internal/oneshot/domain/*_test.go
```

### 门禁

```text
scripts/oneshot/check-domain-contract.py
scripts/oneshot/check-oneshot-domain.sh
```

### 过程文件

```text
docs/development/oneshot/evidence/OD-OS-07-red.txt
docs/development/oneshot/evidence/OD-OS-07-validation.txt
docs/development/oneshot/reports/OD-OS-07-summary.md
docs/development/oneshot/task-state.yaml
docs/development/oneshot/OPENDRAY_ONESHOT_DEVELOPMENT_TASKBOOK.md
```

## 9. 影响评估

| 范围 | 影响 |
|---|---|
| API | 无外部 HTTP/WebSocket API 修改。 |
| 数据库 | 无 Migration、表或 Store 修改。 |
| 进程 | 未启动子进程，未实现 Executor。 |
| Telegram/Channel | 无修改。 |
| PTY Session | 无修改；架构边界和 PTY 回归门禁通过。 |
| Flutter | 无修改。 |
| One-shot | 新增后续 Queue、Store、Executor 和 API 共用的纯领域基础。 |

## 10. 未完成运行门禁

当前沙箱未能执行：

- 仓库要求的 Go 1.25 全仓 `go test ./...`、`go test -race ./...`、`go vet ./...`；
- 完整 Web embed 后的 Gateway 构建；
- Flutter analyze/test/build。

本任务领域包无第三方依赖，已在隔离模块使用本地 Go 执行 vet、race test 和覆盖率；但全仓 Go 1.25 运行门禁仍保持 `pending_runtime_validation`，不得解释为已通过。

## 11. 下一任务

`OD-OS-08 — One-shot PostgreSQL Migration 与 Store`
