# OD-OS-11 — stdout/stderr 有序采集、Artifact 与 StandardEvent 执行报告

## 1. 状态

`PASS — 源码门禁完成；Go 1.25、真实 PostgreSQL 与最终本机运行门禁保留`

## 2. 时间

- 开始：`2026-07-28T10:04:55+08:00`
- 结束：`2026-07-28T10:29:00+08:00`
- 耗时：约 `24 分钟`

## 3. Git 基线

- 分支：`feat/oneshot-agent`
- 基线 Commit：`54cbcb00e76c95f2a9550fdc81812e8c806d0a8f`
- 最终 Commit：本报告所在的 `OD-OS-11` 单一提交；精确 Hash 记录在交付回执与 Git Bundle 中。
- Push：仅推送到沙箱本地 Bare Remote，不是 GitHub。

## 4. 设计结论

本任务将普通子进程输出链补齐为：

```text
stdout / stderr
  → Run 级有界 StreamWriter
  → 全局顺序 OutputCollector
  → 原始字节 Artifact
  → StreamRecord
  → Adapter passthrough StandardEvent
  → PostgreSQL 原子 OutputBatch
  → final_result manifest Artifact
```

关键结论：

1. stdout/stderr 通过同一个 Collector 锁串行分配 Run 内全局单调 sequence，不采用固定 stdout 优先级。
2. Writer 按固定 chunk 大小施加背压，不聚合完整进程输出，避免大输出常驻内存。
3. 原始字节先进入服务器控制的文件 Artifact Storage；数据库只保存不可变元数据、SHA-256、storage key 和引用。
4. UTF-8 文本解码与原始字节分离；多字节字符跨 chunk 通过每个 stream 独立增量 decoder 恢复，非法或二进制字节仍可完整取回。
5. Shell Fixture Adapter 为每个持久化 StreamRecord 生成 `shell.passthrough` StandardEvent；StandardEvent 不替代原始证据。
6. 最终 `final_result` JSON Artifact 记录 Run ID、sequence 范围、stdout/stderr 字节数和冻结的 events 回放入口，可反查原始 StreamRecord 与 raw Artifact。
7. Output cursor 从 PostgreSQL 恢复 stream/event sequence 和每个 stream byte offset，支持分页读取与恢复后继续追加。

## 5. 修改文件

### 新增

- `internal/oneshot/executor/stream_reader.go`
- `internal/oneshot/executor/output_collector.go`
- `internal/oneshot/executor/output_collector_test.go`
- `internal/oneshot/testdata/fixtures/interleaved.sh`
- `scripts/oneshot/check-oneshot-output.py`
- `scripts/oneshot/check-oneshot-output.sh`
- `docs/development/oneshot/evidence/OD-OS-11-red.txt`
- `docs/development/oneshot/evidence/OD-OS-11-validation.txt`
- `docs/development/oneshot/reports/OD-OS-11-summary.md`

### 修改

- `internal/oneshot/adapter/types.go`
- `internal/oneshot/adapter/shell.go`
- `internal/oneshot/adapter/shell_test.go`
- `internal/oneshot/executor/process.go`
- `internal/oneshot/executor/run_service.go`
- `internal/oneshot/executor/run_service_test.go`
- `internal/oneshot/store/output.go`
- `docs/development/oneshot/OPENDRAY_ONESHOT_DEVELOPMENT_TASKBOOK.md`
- `docs/development/oneshot/task-state.yaml`

## 6. Red 证据

新增测试后、实现前执行现有 Executor Gate，编译按预期失败：

```text
vet: internal/oneshot/executor/output_collector_test.go:19:12: undefined: OutputCursor
```

完整记录：`docs/development/oneshot/evidence/OD-OS-11-red.txt`。

## 7. 修复后测试

覆盖场景：

- stdout/stderr 交错输出及实际接收顺序；
- 无换行 5 MiB+ 长输出；
- 单次 Write 自动拆为不超过配置上限的 chunk；
- UTF-8 多字节跨 chunk；
- 非法与 binary 原始字节；
- raw Artifact 完整读回；
- StreamRecord/Event sequence 唯一、递增；
- cursor 恢复后的 sequence 与 byte offset；
- final_result Artifact 到 Run/StreamRecord 的追踪；
- Worker → RunService → ProcessExecutor 的真实交错 Shell Fixture；
- Shell passthrough StandardEvent；
- Artifact storage 路径穿越与覆盖写入拒绝；
- output metadata 持久化失败后停止后续写入。

模块结果：

- Adapter `go test -race`：PASS，覆盖率约 `75.9%`。
- Executor `go test -race`：PASS，覆盖率约 `69.8%`。
- Store `go test -race`：PASS。
- PostgreSQL build-tag 编译：PASS。
- 专用 `OD-OS-11` 静态门禁：PASS。

## 8. 模块与回归门禁

- `scripts/oneshot/check-oneshot-output.sh`：PASS。
- `scripts/oneshot/check-oneshot-executor.sh`：PASS。
- `scripts/oneshot/check-oneshot-store.sh`：PASS。
- `scripts/oneshot/check-oneshot-queue.sh`：PASS。
- `scripts/oneshot/check-oneshot-domain.sh`：PASS。
- One-shot architecture boundaries：PASS。
- OD-OS-04/05 已知问题回归：PASS。
- PTY Source Baseline：`16/16 PASS`。
- PTY negative mutations：`5/5 PASS`。
- i18n parity：PASS。
- `git diff --check`：PASS。

## 9. 影响范围

| 范围 | 影响 |
|---|---|
| API | 无新增路由；final manifest 仅引用冻结的 `/api/v1/oneshot/runs/{run_id}/events`。 |
| 数据库 | 无新 Migration；Store 增加 output cursor 查询和 executor-facing 原子 AppendOutput。 |
| Artifact | 新增服务器控制的文件存储实现，raw chunk 与 final manifest 均不可变、带 SHA-256。 |
| Telegram | 无路由或交互行为修改。 |
| Flutter | 无修改。 |
| PTY Session | 无生产代码修改；未写 PTY ring buffer 或 session transcript。 |
| One-shot | 完成普通子进程输出采集、原始证据、标准事件与最终结果 Artifact。 |

## 10. 兼容性与边界

- One-shot 输出链不导入 `internal/session`、PTY 包或 Session ID。
- 不发布 `session.*`，不写 `sessions` 或 `session_transcripts`。
- 原有 `ProcessExecutor.Start` 保留；新增 Run-scoped `StartWithOutput`，避免并发 Run 共享 Writer。
- Store 原有 `PersistOutputBatch` 保留；新增窄接口 `AppendOutput` 和 `LoadOutputCursor`。

## 11. 未完成与运行限制

当前沙箱未虚假标记以下门禁为通过：

- Go 1.25 全仓 `go test ./...`、`go test -race ./...`、`go vet ./...`、lint 和最终构建；
- 真实 PostgreSQL 下 output batch、cursor 恢复、并发 sequence 约束测试；
- Gateway 重启后对 Artifact 文件根目录的真实配置与权限验证；
- Flutter analyze、test 与 APK 构建；
- ProcessSupervisor、进程树取消与超时清理。

## 12. 下一任务

`OD-OS-12 — ProcessSupervisor、取消、超时与进程树`
