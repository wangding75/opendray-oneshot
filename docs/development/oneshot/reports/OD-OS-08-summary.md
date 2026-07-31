# OD-OS-08 完成报告：One-shot PostgreSQL Migration 与 Store

## 1. 状态

**PASS（源码门禁） / PENDING（本机运行门禁）**

- 源码实现、静态契约、隔离编译、竞态测试和回归门禁已通过。
- 沙箱没有真实 PostgreSQL、Go 1.25、Flutter/Dart，因此真实数据库迁移/并发集成测试与全仓运行门禁未执行，不能标记为通过。

## 2. 时间与基线

- 开始时间：2026-07-27T18:53:24+0800
- 结束时间：2026-07-27T23:42:09+0800
- 耗时：约 4 小时 49 分钟
- 基线 Commit：`11a2eb323a739a7b7c1313d8171852b356ab7dfa`
- 最终 Commit：本报告所在的 OD-OS-08 单一提交；精确 Hash 记录在交付回执和 Git Bundle 中。
- 分支：`feat/oneshot-agent`

## 3. 设计结论

One-shot 持久化必须拥有独立表、独立约束和独立 Store，不通过 `sessions`、PTY 或 Channel 领域承载业务状态。

本阶段建立 10 张独立表：

1. `oneshot_tasks`
2. `oneshot_deliveries`
3. `oneshot_runs`
4. `oneshot_runtime_contexts`
5. `oneshot_stream_records`
6. `oneshot_standard_events`
7. `oneshot_artifacts`
8. `oneshot_channel_bindings`
9. `oneshot_idempotency_keys`
10. `oneshot_notification_outbox`

数据库约束负责阻止关键不一致，包括所有权错配、Provider 错配、Delivery/Run 错配、Artifact/Run/Task 错配、序列冲突和单 Task 多活动 Run。

## 4. 主要修改

### Migration

- 新增 `internal/store/migrations/0083_oneshot.sql`。
- 增加主键、外键、复合外键、状态 CHECK、唯一键、部分唯一索引和查询索引。
- Task 当前 Run 与 Delivery Run 使用可延迟复合外键，允许同一事务建立互相引用。
- 禁止 `ALTER TABLE sessions`、`REFERENCES sessions`、`session_id` 和 `Session.Mode`。

### Store

新增 `internal/oneshot/store/`：

- Task/Delivery 原子创建和更新；
- Task/Delivery/Run/RuntimeContext CRUD；
- 所有查询带 Principal 所有权过滤；
- 基于 `(created_at,id)` 的稳定游标分页；
- Task/RuntimeContext 乐观版本控制；
- Task/Delivery/Run 同事务创建；
- Artifact、StreamRecord、StandardEvent 原子批量持久化；
- Stream/Event 严格递增序列检查；
- Channel Binding、Idempotency 和 Notification Outbox 基础 Store；
- PostgreSQL 错误映射到冻结的 `oneshot.*` 错误码；
- 所有公开 Store 方法首参数均为 `context.Context`。

### 测试与门禁

- 新增 Store 静态/契约测试；
- 新增 PostgreSQL build-tag 集成测试；
- 新增 migration 重复执行与事务回滚测试；
- 新增 `check-oneshot-store.py` 和 `check-oneshot-store.sh`；
- 在无依赖沙箱中用隔离模块和 pgx stub 执行 `go vet`、`go test -race` 与 PostgreSQL-tag 编译门禁。

## 5. 修复前失败条件

基线缺少 Migration 和 Store，以下验收条件均不成立：

- 空库升级与重复迁移；
- Task/Delivery/Run 事务一致性；
- 单 Task 单活动 Run 数据库约束；
- CRUD、分页和所有权过滤；
- 输出记录原子持久化；
- Migration rollback-on-failure。

证据：`docs/development/oneshot/evidence/OD-OS-08-red.txt`。

## 6. 验证结果

通过：

- Migration 精确表集合与隔离检查；
- 状态集合、外键、唯一键、部分唯一索引检查；
- Store 导出方法 Context 检查；
- Store/Domain 隔离 `go vet`；
- Store/Domain 隔离 `go test -race`；
- PostgreSQL build-tag 测试编译；
- OD-OS-07 Domain 门禁；
- OD-OS-04/05 已知问题回归门禁；
- PTY Source Baseline 16/16；
- PTY 负向变异 5/5；
- i18n parity；
- `git diff --check`。

待本机：

- 真实 PostgreSQL 空库 migrate；
- 重复 migrate；
- rollback-on-failure；
- 唯一约束/外键/CHECK 实际数据库验证；
- 并发活动 Run 冲突；
- Go 1.25 全仓测试与构建；
- Flutter analyze/test/build。

完整证据：`docs/development/oneshot/evidence/OD-OS-08-validation.txt`。

## 7. 影响评估

- API：无新增 HTTP/WS 路由。
- 数据库：新增 10 张 One-shot 独立表；不修改 Session 表。
- Telegram：无行为修改；仅为后续 One-shot Binding/幂等提供持久化基础。
- 移动端：无代码修改。
- PTY：无生产代码修改，源码回归门禁通过。
- 兼容性：旧数据库通过增量 Migration 升级；Migration 由现有 runner 在事务中执行并记录版本。

## 8. Commit 与 Push

- 建议/实际提交信息：`feat(oneshot): add postgres schema and stores`
- 提交数量：1
- Push 目标：本地沙箱 Bare Remote；不是 GitHub Remote。
- 精确 Commit 与 Push 结果见最终交付回执。

## 9. 未完成项

运行级验证必须在具备以下环境的本机执行：

- Go 1.25；
- 可用且可清理的 PostgreSQL 测试数据库；
- Flutter 3.41+/Dart 3.11+。

建议数据库门禁：

```bash
OPENDRAY_DEV_DB_URL='postgres://...' scripts/oneshot/check-oneshot-store.sh
```

## 10. 下一任务

`OD-OS-09 — Delivery Queue、Lease、幂等与死信`
