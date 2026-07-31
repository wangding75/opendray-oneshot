# OD-OS-18 — Task/Run REST API、权限、审计与幂等执行报告

## 1. 状态

`PASS — 源码实现、隔离 race 测试和确定性门禁完成；Go 1.25 全仓与真实 PostgreSQL 运行门禁待本机验证`

## 2. 时间与 Git 基线

- 开始：`2026-07-28T19:46:40+08:00`
- 结束：`2026-07-28T20:55:00+08:00`
- 基线 Commit：`2f93ab8b1397e3f8a8787afd31f8c3673bb0b747`
- 最终 Commit：本报告所在的 `OD-OS-18/19/20` 单一提交；精确 Hash 见交付回执和 Git Bundle。
- Push：仅推送沙箱本地 Bare Remote，不是 GitHub。

## 3. 设计结论

新增 `/api/v1/oneshot` 控制面，完整覆盖 Task 创建、列表、详情、继续、取消、重试，以及 Run、事件和 Artifact 查询。每条路由独立校验 scope 和资源所有权；写请求必须携带 `Idempotency-Key`；审计动作与冻结契约完全一致，原始幂等键只记录 SHA-256 摘要。取消不仅修改状态，还会调用活跃 Run 取消和恢复进程树终止接口。retry 在 Serializable 事务内同时完成任务版本推进、Delivery 创建和幂等记录。

## 4. 核心交付

- 13 条冻结 REST/WS 路由全部挂载；
- `oneshot:task:*`、`oneshot:run:read`、`oneshot:artifact:read`、订阅 scope 逐路由执行；
- principal、project、task、run、artifact 所有权过滤；
- create/continue/retry 请求幂等和 payload 冲突检测；
- request ID、统一错误响应、不透明分页游标；
- Artifact `Digest`、`ETag`、内容类型和下载头；
- 审计成功/失败均记录，动作名采用冻结的 `oneshot.*` 命名。

## 5. Red / Lock / Green

- Red：基线只有 Application/Store 能力，没有 Gateway REST 路由、逐路由 scope、审计和 Artifact 下载入口。
- Lock：缺 scope、跨主体、错误幂等 payload、原始幂等键泄露、可猜测事件游标、Artifact 摘要和跨域 WebSocket 用例已锁定。
- Green：API 隔离 `go test -race`、静态契约检查、`git diff --check` 通过。

## 6. 审查中修复的问题

1. 审计动作曾缺少 `oneshot.` 前缀，与冻结契约不一致；已改为精确动作名。
2. 审计事件曾保存原始幂等键；已改为 `sha256:<digest>`。
3. Run 事件列表曾重复追加当前批次；已消除重复返回。
4. HTTP 事件分页曾暴露纯数字序号；已改为不透明 cursor。
5. retry 的幂等记录与 Delivery 创建存在并发窗口；已收敛为 Serializable 单事务并支持竞态后回放。
6. 读取接口的跨项目/所有权拒绝，以及写接口的 disabled、缺幂等键和非法 JSON 早退路径曾未统一审计；现已全部写入失败审计，且不记录原始请求体。

## 7. 影响范围

| 范围 | 影响 |
|---|---|
| API | 新增完整 One-shot Task/Run/Artifact 控制面。 |
| 数据库 | 复用现有任务、运行、交付、制品和幂等表；retry 原子事务增强。 |
| Telegram | 为 OD-OS-20 提供稳定控制服务和查询入口。 |
| Flutter | OD-OS-21 可直接基于 REST/WS 契约实现数据层。 |
| PTY | 未修改 Session REST、PTY 进程或输入路由。 |

## 8. 验证结果

- OD-OS-18 静态契约门禁：PASS；
- API `go vet`：PASS；
- API `go test -race`：PASS；
- API 覆盖率：16.7%；
- One-shot Domain/Store/Queue/Application 历史回归：PASS；
- PTY Source Baseline：16/16 PASS；负向变异：5/5 PASS。

## 9. 未完成项

- Go 1.25 全仓 test/race/vet/lint/build；
- 真实 PostgreSQL 权限、幂等竞态、取消和 Artifact 大文件下载集成测试；
- 浏览器/移动端真实 WebSocket 和 REST 联调。

## 10. 下一依赖任务

`OD-OS-19 — One-shot WebSocket、事件回放与通知 Outbox`
