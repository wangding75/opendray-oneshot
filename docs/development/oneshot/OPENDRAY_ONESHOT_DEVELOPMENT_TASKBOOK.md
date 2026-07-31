
# OpenDray One-shot Agent 完整开发任务书 v1

## 1. 文档信息

| 项目 | 内容 |
|---|---|
| 上游仓库 | `https://github.com/Opendray/opendray` |
| 基线分支 | `main` |
| 开发分支 | `feat/oneshot-agent` |
| 目标 | 保留现有 PTY Session，全新扩展独立 One-shot Agent |
| 客户端范围 | Telegram + Flutter Mobile |
| Web UI | 本期不新增 One-shot 页面；仅保证现有 Web/PTTY 不回归 |
| 首批 Provider | Codex、Claude Code |
| 后续 Provider | Antigravity、OpenCode、Grok Build |
| 执行原则 | 串行、小步、测试先行、每任务独立 Commit/Push/报告 |

## 2. 产品目标

在现有 OpenDray 交互式 PTY Session 平台旁，新增完全独立的 One-shot Agent 任务平台：

```text
Telegram / Flutter Mobile
          │
          ▼
共享 Channel / Auth / Project / Provider / HTTP / WS
          │
          ├──────────── Interactive PTY Domain
          │              Session / PTY / RingBuffer / Terminal
          │
          └──────────── One-shot Domain
                         Task / Delivery / Run / RuntimeContext
                         stdout / stderr / Artifact / Retry / Cancel
```

## 3. 强制架构边界

### 3.1 允许共享

- 用户、认证、权限、API Key。
- Project 和受控工作目录解析。
- Provider 名称、CLI 路径、版本和账号凭据。
- PostgreSQL Pool、HTTP/WS 基础设施、Event Bus、Audit。
- Telegram Transport、消息标准化、附件接收、出站发送。
- Flutter 登录、网络、主题和通用 UI 组件。

### 3.2 禁止共享

- PTY Session 与 One-shot RuntimeContext。
- Session Manager 与 One-shot Run Manager。
- PTY 进程与 One-shot 子进程。
- RingBuffer/Transcript 与 StreamRecord/Artifact。
- Session 状态机与 Task/Run 状态机。
- Interactive binding 与 One-shot binding。
- Session API 与 One-shot API。
- Session cancel 与 One-shot cancel。

### 3.3 禁止实现

```text
Session.Mode = pty | oneshot
POST /sessions?mode=oneshot
/run → 创建 PTY Session
RuntimeContext → 引用 SessionID
Provider 不支持 One-shot → 静默回退 PTY
Telegram 根据“最近对象”猜测模式
Channel Hub 直接写 PTY或创建 Task
```

## 4. 分支创建命令

### PowerShell

```powershell
$ErrorActionPreference = 'Stop'
$branch = 'feat/oneshot-agent'

git fetch origin --prune

$dirty = git status --porcelain
if ($dirty) {
    throw "工作区存在未提交修改，禁止创建开发分支：`n$dirty"
}

git show-ref --verify --quiet "refs/heads/$branch"
if ($LASTEXITCODE -eq 0) {
    git switch $branch
} else {
    git ls-remote --exit-code --heads origin $branch *> $null
    if ($LASTEXITCODE -eq 0) {
        git switch --track "origin/$branch"
    } else {
        git switch main
        git pull --ff-only origin main
        git switch -c $branch
        git push -u origin $branch
    }
}

git branch --show-current
git rev-parse HEAD
git rev-parse "HEAD@{upstream}"
git status --short --untracked-files=all
```

### Bash

```bash
set -euo pipefail
branch='feat/oneshot-agent'

git fetch origin --prune

if [[ -n "$(git status --porcelain)" ]]; then
  echo "工作区存在未提交修改，禁止创建开发分支" >&2
  git status --short --untracked-files=all
  exit 1
fi

if git show-ref --verify --quiet "refs/heads/$branch"; then
  git switch "$branch"
elif git ls-remote --exit-code --heads origin "$branch" >/dev/null 2>&1; then
  git switch --track "origin/$branch"
else
  git switch main
  git pull --ff-only origin main
  git switch -c "$branch"
  git push -u origin "$branch"
fi

git branch --show-current
git rev-parse HEAD
git rev-parse 'HEAD@{upstream}'
git status --short --untracked-files=all
```

## 5. 全局执行规则

1. 必须按任务编号顺序执行，不得跳过。
2. 每次只执行一个任务；当前任务未 PASS，不得开始下一任务。
3. 每个任务必须先读取指定资料，再做根因/方案，再写失败测试，再实现。
4. 不得重新制定整体方案或把多个任务合成一个大任务。
5. 每个任务独立 Commit、Push 和报告。
6. 不得使用 `git add .`、amend、rebase、squash、force push。
7. 普通缺陷、测试失败、脚本误判和环境问题必须自行定位并继续。
8. 只有需要破坏冻结契约、覆盖来源不明修改、引入重大新依赖或缺少外部凭据时才允许 BLOCKED。
9. 所有时间记录真实开始/结束时间，不填写预计耗时。
10. 状态与证据必须可在中断后恢复。

## 6. 状态与报告

仓库内维护：

```text
docs/development/oneshot/
├── OPENDRAY_ONESHOT_DEVELOPMENT_TASKBOOK.md
├── task-state.yaml
├── contracts/
├── evidence/
└── reports/
    ├── OD-OS-00-summary.md
    └── ...
```

`task-state.yaml` 至少包含：

```yaml
project: opendray
branch: feat/oneshot-agent
base_commit: ""
current_task: OD-OS-00
current_phase: pending
status: pending
tasks:
  OD-OS-00:
    implementation: pending
    source_gate: pending
    runtime_gate: pending
    overall: pending
last_commit: ""
last_push: ""
updated_at: ""
```

允许阶段：

```text
reading
analysis
solution_design
test_red
development
minimal_validation
module_validation
scope_check
commit
push
state_update
completed
blocked
```

## 7. 每个任务固定执行流程

```text
Git 检查
→ 读取输入资料
→ 检查依赖
→ 记录现状与根因
→ 写实施方案
→ 新增失败测试并证明 RED
→ 开发
→ 最小验证
→ 模块验证
→ 范围检查
→ Commit
→ Push
→ 写任务报告
→ 更新任务书和 task-state
→ 进入下一任务
```

## 8. Git 前置检查

每个任务开始执行：

```bash
git branch --show-current
git rev-parse HEAD
git rev-parse 'HEAD@{upstream}'
git status --short --untracked-files=all
git diff --cached --name-status
```

发现来源不明修改时，禁止 reset/restore/checkout 覆盖，必须报告并阻塞。

## 9. 统一质量门禁

### Go

```bash
go vet ./...
go test -race -timeout=5m -coverprofile=coverage.out ./...
golangci-lint run
go build ./cmd/opendray
```

### Web

```bash
corepack enable
pnpm install --frozen-lockfile
pnpm --filter web lint
pnpm --filter web build
```

### Mobile

```bash
cd app/mobile
flutter pub get
dart format --output=none --set-exit-if-changed .
flutter analyze
flutter test
flutter build apk --release
```

### i18n

```bash
node scripts/check-i18n-parity.mjs
```

完整门禁只在任务明确要求或最终验收时执行；普通任务先跑最小测试和相关模块测试。

## 10. 总任务列表

| 状态 | 任务 ID | 优先级 | 任务名称 | 依赖 |
|---|---|---:|---|---|
| ☑ | OD-OS-00 | P0 | 开发分支、环境预检与基线登记 | 无 |
| ☑ | OD-OS-01 | P0 | 现有 PTY Session 行为特征化与回归门禁（源码门禁完成，本机运行门禁保留） | OD-OS-00 |
| ◐ | OD-OS-02 | P0 | 双执行域架构与依赖边界冻结（实现完成，运行验证待本机） | OD-OS-01 |
| ☑ | OD-OS-03 | P0 | One-shot 契约、状态机、API 与错误码冻结 | OD-OS-02 |
| ☑ | OD-OS-04 | P0 | Channel Core 入站分发接口（源码门禁完成，本机运行门禁保留） | OD-OS-03 |
| ☑ | OD-OS-05 | P0 | Session Channel Adapter 迁移（源码门禁完成，本机运行门禁保留） | OD-OS-04 |
| ☑ | OD-OS-06 | P1 | 共享出站 Channel Delivery 抽象 | OD-OS-05 |
| ☑ | OD-OS-07 | P0 | One-shot 领域模型与状态机实现（源码门禁完成，本机 Go 1.25 全仓验证保留） | OD-OS-03 |
| ☑ | OD-OS-08 | P0 | One-shot PostgreSQL Migration 与 Store | OD-OS-07 |
| ☑ | OD-OS-09 | P0 | Delivery Queue、Lease、幂等与死信（源码门禁完成，本机 PostgreSQL/Go 1.25 运行门禁保留） | OD-OS-08 |
| ☑ | OD-OS-10 | P0 | Shell One-shot Adapter 与 Executor 骨架（源码门禁完成，本机 Go 1.25/PostgreSQL 运行门禁保留） | OD-OS-09 |
| ☑ | OD-OS-11 | P0 | stdout/stderr 有序采集、Artifact 与 StandardEvent（源码门禁完成，本机 Go 1.25/PostgreSQL 运行门禁保留） | OD-OS-10 |
| ☑ | OD-OS-12 | P0 | ProcessSupervisor、取消、超时与进程树 | OD-OS-11 |
| ☑ | OD-OS-13 | P0 | 执行 Saga、失败补偿与崩溃恢复 | OD-OS-12 |
| ☑ | OD-OS-14 | P1 | One-shot Provider Capability 与 Adapter Registry | OD-OS-13 |
| ☑ | OD-OS-15 | P0 | Codex One-shot Adapter | OD-OS-14 |
| ☑ | OD-OS-16 | P0 | Claude Code One-shot Adapter | OD-OS-14 |
| ☑ | OD-OS-17 | P0 | RuntimeContext 与 Continue/Resume | OD-OS-15, OD-OS-16 |
| ☑ | OD-OS-18 | P0 | Task/Run REST API、权限、审计与幂等 | OD-OS-17 |
| ☑ | OD-OS-19 | P1 | One-shot WebSocket、事件回放与通知 Outbox | OD-OS-18 |
| ☑ | OD-OS-20 | P0 | Telegram One-shot 命令、绑定与结果回传 | OD-OS-06, OD-OS-18, OD-OS-19 |
| ☑ | OD-OS-21 | P0 | Flutter Agent Tasks 数据层与导航基础 | OD-OS-18, OD-OS-19 |
| ☑ | OD-OS-22 | P0 | Flutter Task 列表与创建流程 | OD-OS-21 |
| ☑ | OD-OS-23 | P0 | Flutter Task/Run 详情、实时输出与控制 | OD-OS-22 |
| ☑ | OD-OS-24 | P1 | 跨端同步、附件与通知策略 | OD-OS-20, OD-OS-23 |
| ☑ | OD-OS-25 | P0 Gate | 安全、可靠性、性能与故障注入 | OD-OS-24 |
| ☑ | OD-OS-26 | P0 Final Gate | PTY 全量回归、双模式验收、文档与交付 | OD-OS-25 |

---

# 11. 任务详情

## 1. OD-OS-00 — 开发分支、环境预检与基线登记

### 任务状态

- [x] 已完成

### 优先级

`P0`

### 依赖

无

### 目标

从上游 main 的最新干净提交创建 feat/oneshot-agent，冻结开发环境、基线 Commit、现有测试结果和仓库结构，建立可恢复的任务状态。

### 必须读取

- `README.md、README.zh.md`
- `CONTRIBUTING.md、SECURITY.md、VERSIONING.md`
- `.github/workflows/ci.yml、.golangci.yml`
- `go.mod、package.json、pnpm-workspace.yaml`
- `app/web/package.json、app/mobile/pubspec.yaml、app/mobile/analysis_options.yaml`
- `internal/session/、internal/channel/、internal/store/`

### 允许修改范围

- `docs/development/oneshot/**`
- `必要时新增 scripts/oneshot/preflight.*，但不得修改产品运行逻辑`

### 执行步骤

1. 确认 origin 指向用户自己的 Fork；upstream 指向 Opendray/opendray。若缺失 upstream，仅登记，不擅自修改远端。
2. 确认工作区完全干净，不得覆盖未提交内容。
3. 从最新 main 创建并推送 feat/oneshot-agent；记录 base_commit、branch_head、upstream_main_commit。
4. 收集 Go、Node、pnpm、Flutter、Dart、PostgreSQL、Git 版本和 OS 信息。
5. 记录当前目录树、Go package 列表、Web workspace、Mobile feature 目录和 migration 列表。
6. 运行现有最小构建和测试，失败时区分环境失败与基线代码失败。
7. 创建 docs/development/oneshot/task-state.yaml、reports/、contracts/、evidence/。
8. 将本任务书复制到仓库的 docs/development/oneshot/ 目录并登记 SHA-256。

### 必须新增或补齐的测试

- go test ./...（必要时先完成 web build 以满足 go:embed）
- go vet ./...
- pnpm install --frozen-lockfile
- pnpm --filter web lint
- pnpm --filter web build
- flutter pub get、flutter analyze、flutter test
- node scripts/check-i18n-parity.mjs

### 验收标准

- 分支为 feat/oneshot-agent，并已设置 origin/feat/oneshot-agent 上游。
- 基线 Commit、环境版本和所有基线测试结果被记录。
- 工作区干净；没有业务代码变更。
- 任务状态文件能明确指向 OD-OS-00 completed 和下一任务 OD-OS-01。

### 建议 Commit

```text
chore(oneshot): initialize development branch and baseline
```

### 必须生成的报告

`docs/development/oneshot/reports/OD-OS-00-summary.md`

报告必须包含：

- 状态：PASS / NEEDS CHANGES / BLOCKED。
- 实际开始时间、结束时间、耗时。
- 基线 Commit、最终 Commit。
- 根因或设计结论。
- 修改文件。
- 修复前失败测试。
- 修复后通过测试。
- 模块测试和完整门禁。
- API、数据库、Telegram、移动端影响。
- 兼容性与 PTY 回归。
- Commit Hash、Push 结果。
- 未完成项和下一任务。


## 2. OD-OS-01 — 现有 PTY Session 行为特征化与回归门禁

### 任务状态

- [x] 已完成源码实现与可执行源码门禁；Go/Flutter 运行门禁按用户决定留到本机最终验收

### 优先级

`P0`

### 依赖

OD-OS-00

### 目标

在改造渠道层之前，用测试冻结当前 PTY Session、Telegram 回复路由、移动端终端和 Session API 的真实行为。

### 必须读取

- `internal/session/manager.go、session.go、pump.go、ringbuf.go、handler.go`
- `internal/session/signals_unix.go、signals_windows.go`
- `internal/channel/hub.go、channel.go、command.go、controls.go`
- `internal/channel/telegram/**`
- `现有 session/channel 相关测试`
- `app/mobile/lib 中 Session、Terminal、Channel 功能`

### 允许修改范围

- `internal/session/*_test.go`
- `internal/channel/*_test.go`
- `internal/channel/telegram/*_test.go`
- `app/mobile/test/**`
- `docs/development/oneshot/contracts/pty-baseline.md`

### 执行步骤

1. 绘制并记录 Telegram → Channel Hub → SessionInputter → PTY 的当前调用链。
2. 冻结 Session 创建、输入、resize、attach/reconnect、idle、terminate、resume、host restart reconcile 的行为。
3. 冻结 Telegram reply-to-session、last session fallback、显式 session command、通知回执和输入注入行为。
4. 增加行为特征测试，不改变当前产品行为。
5. 记录现有 Session API 路由、请求/响应和 WebSocket 事件。
6. 记录移动端当前 Session/Terminal 功能的页面与数据依赖。
7. 生成 PTY regression gate 脚本，供后续每个改造阶段复用。

### 必须新增或补齐的测试

- Session 生命周期、ring buffer、input、resize、resume、terminate 测试。
- Telegram 明确回复、普通文本 fallback、错误 Session、跨会话隔离测试。
- Channel 消息持久化和 SessionID 绑定测试。
- Flutter Session/Terminal 现有 widget 与 repository 测试。

### 验收标准

- 基线测试能在不改生产行为的情况下覆盖关键 PTY 流程。
- 后续若误删 PTY 回复、重连或终端能力，测试必须失败。
- 基线文档明确列出允许变更与禁止变更。

### 建议 Commit

```text
test(oneshot): freeze interactive PTY behavior
```

### 必须生成的报告

`docs/development/oneshot/reports/OD-OS-01-summary.md`

报告必须包含：

- 状态：PASS / NEEDS CHANGES / BLOCKED。
- 实际开始时间、结束时间、耗时。
- 基线 Commit、最终 Commit。
- 根因或设计结论。
- 修改文件。
- 修复前失败测试。
- 修复后通过测试。
- 模块测试和完整门禁。
- API、数据库、Telegram、移动端影响。
- 兼容性与 PTY 回归。
- Commit Hash、Push 结果。
- 未完成项和下一任务。


## 3. OD-OS-02 — 双执行域架构与依赖边界冻结

### 任务状态

- [x] 实现完成；运行级验证待本机工具链

### 优先级

`P0`

### 依赖

OD-OS-01

### 目标

冻结 Interactive PTY 与 One-shot Agent 两个独立 bounded context，明确共享能力、禁止依赖和演进边界。

### 必须读取

- `docs/ 中现有 ADR 和架构文档`
- `internal/session、internal/channel、internal/catalog、internal/providers`
- `internal/app、internal/gateway 的装配方式`

### 允许修改范围

- `docs/adr/ 下一个可用编号的 One-shot ADR`
- `docs/development/oneshot/contracts/architecture.md`
- `scripts/oneshot/check-boundaries.*`

### 执行步骤

1. 定义 InteractiveSession 与 OneShotTask/Run/RuntimeContext 的独立概念。
2. 冻结禁止方案：Session.mode、/sessions?mode=oneshot、One-shot 使用 PTY、RuntimeContext 引用 SessionID。
3. 定义允许共享：认证、项目、Provider 元数据、凭据、HTTP/WS 基础、Channel Transport、Event Bus、PostgreSQL Pool、审计。
4. 定义禁止共享：生命周期、进程实例、状态机、输出存储、Binding、API 资源、取消逻辑。
5. 确定 package 依赖方向：channel core 不依赖 session/oneshot；两端通过入口适配器依赖 channel 接口。
6. 增加静态边界检查，阻止 internal/session 与 internal/oneshot 相互 import。
7. 确定功能开关：oneshot.enabled=false 时现有 PTY 完整工作。

### 必须新增或补齐的测试

- 静态 import 边界测试。
- 配置关闭 One-shot 后 Gateway 能启动且 PTY 测试通过。
- ADR 链接和架构文档完整性检查。

### 验收标准

- ADR 状态为 Accepted，并记录替代方案和后果。
- 边界脚本在错误 import 时失败。
- 所有后续任务均能引用冻结契约，不得重新设计整体架构。

### 建议 Commit

```text
docs(oneshot): freeze dual execution domain architecture
```

### 必须生成的报告

`docs/development/oneshot/reports/OD-OS-02-summary.md`

报告必须包含：

- 状态：PASS / NEEDS CHANGES / BLOCKED。
- 实际开始时间、结束时间、耗时。
- 基线 Commit、最终 Commit。
- 根因或设计结论。
- 修改文件。
- 修复前失败测试。
- 修复后通过测试。
- 模块测试和完整门禁。
- API、数据库、Telegram、移动端影响。
- 兼容性与 PTY 回归。
- Commit Hash、Push 结果。
- 未完成项和下一任务。


## 4. OD-OS-03 — One-shot 契约、状态机、API 与错误码冻结

### 任务状态

- [x] 实现完成；契约机器门禁通过

### 优先级

`P0`

### 依赖

OD-OS-02

### 目标

在实现代码前冻结 Task、Delivery、Run、RuntimeContext、StreamRecord、StandardEvent、Artifact、API、事件和错误码。

### 必须读取

- `internal/session/handler.go 的 REST/WS 风格`
- `internal/eventbus/**`
- `internal/audit/**、internal/auth/**`
- `internal/store/migrations/**`
- `docs/integration-guide.md`

### 允许修改范围

- `docs/development/oneshot/contracts/domain-model.md`
- `docs/development/oneshot/contracts/state-machines.md`
- `docs/development/oneshot/contracts/http-api.md`
- `docs/development/oneshot/contracts/events.md`
- `docs/development/oneshot/contracts/errors.md`
- `契约 schema/fixture/validator`

### 执行步骤

1. 定义 Task、Delivery、Run、RuntimeContext、StreamRecord、StandardEvent、Artifact 字段和不可变量。
2. 定义 Task、Delivery、Run、RuntimeContext 状态机及每条允许转换。
3. 定义创建、查询、继续、取消、重试、事件、Artifact API。
4. 定义 Idempotency-Key、principal/project/provider 所有权和跨端来源字段。
5. 定义事件 namespace 为 oneshot.*，禁止复用 session.*。
6. 定义错误码：disabled、unsupported_provider、invalid_transition、context_not_found、context_owner_mismatch、resume_failed、run_conflict、cancel_failed、timeout、delivery_exhausted 等。
7. 定义分页、时间、排序、错误响应和审计字段。
8. 生成机器可校验 fixture，禁止仅靠 Markdown 描述。

### 必须新增或补齐的测试

- 状态机表完整性和不可达状态检查。
- JSON fixture/schema 验证。
- API 路径不得与 /sessions、/custom-tasks 冲突。
- 错误码唯一性和文档覆盖检查。

### 验收标准

- 契约能独立指导后端、Telegram 和移动端开发。
- 每个状态转换、错误码和事件都有唯一语义。
- 实现任务不得无授权修改冻结契约。

### 建议 Commit

```text
docs(oneshot): freeze domain API and event contracts
```

### 必须生成的报告

`docs/development/oneshot/reports/OD-OS-03-summary.md`

报告必须包含：

- 状态：PASS / NEEDS CHANGES / BLOCKED。
- 实际开始时间、结束时间、耗时。
- 基线 Commit、最终 Commit。
- 根因或设计结论。
- 修改文件。
- 修复前失败测试。
- 修复后通过测试。
- 模块测试和完整门禁。
- API、数据库、Telegram、移动端影响。
- 兼容性与 PTY 回归。
- Commit Hash、Push 结果。
- 未完成项和下一任务。


## 5. OD-OS-04 — Channel Core 入站分发接口

### 任务状态

- [x] 源码实现完成；本机 Go 1.25 运行门禁保留

### 优先级

`P0`

### 依赖

OD-OS-03

### 目标

在不改变现有 Telegram 行为的前提下，引入中立 InboundDispatcher，让 Channel Core 不再承担目标执行域决策。

### 必须读取

- `internal/channel/channel.go、hub.go、registry.go、command.go`
- `internal/channel/telegram/**`
- `internal/app 或 gateway 启动装配`

### 允许修改范围

- `internal/channel/**`
- 入口装配层
- 对应测试

### 执行步骤

1. 定义中立 ChannelMessage、ReplyAddress、Attachment、InboundDispatcher 接口。
2. 保持 Telegram Transport、鉴权、入站持久化、文件下载、限流和 Update 去重不变。
3. 在 Hub 中增加确定性的 dispatcher 调用点，但此任务仅注册现有 Interactive 处理器占位。
4. 确保 Channel Core 不创建 One-shot Task，也不新增 One-shot 分支。
5. 增加 handled/not-handled/error 的清晰语义和审计记录。
6. 为 dispatcher 超时、panic、重复处理和未处理消息建立行为。

### 必须新增或补齐的测试

- dispatcher 接收标准化消息。
- handled=true 不再执行后续 fallback。
- handled=false 维持现有 Session fallback。
- dispatcher 错误不会重复注入 PTY。
- Telegram Update 去重不回归。

### 验收标准

- 现有 Telegram PTY 行为不变。
- Channel Core 已具备第二处理器接入点。
- 当前尚未创建任何 One-shot 对象。

### 建议 Commit

```text
refactor(channel): add neutral inbound dispatcher
```

### 必须生成的报告

`docs/development/oneshot/reports/OD-OS-04-summary.md`

报告必须包含：

- 状态：PASS / NEEDS CHANGES / BLOCKED。
- 实际开始时间、结束时间、耗时。
- 基线 Commit、最终 Commit。
- 根因或设计结论。
- 修改文件。
- 修复前失败测试。
- 修复后通过测试。
- 模块测试和完整门禁。
- API、数据库、Telegram、移动端影响。
- 兼容性与 PTY 回归。
- Commit Hash、Push 结果。
- 未完成项和下一任务。


## 6. OD-OS-05 — Session Channel Adapter 迁移

### 任务状态

- [x] 已完成（源码门禁完成；Go 1.25 / Flutter 运行门禁保留到本机）

### 优先级

`P0`

### 依赖

OD-OS-04

### 目标

把 Session 目标解析、reply binding、PTY 输入注入和 Session 通知语义从 Channel Hub 迁到独立 Session Channel Adapter。

### 必须读取

- `internal/channel/hub.go 中 SessionInputter、lastSess、activeSess、outboundIndex、pending、lastDelivered`
- `internal/session/manager.go 的输入接口`
- `现有 channel/session integration tests`

### 允许修改范围

- `新增 internal/session/channeladapter/**`
- `internal/channel/hub.go 的 Session 专属逻辑迁出`
- 程序装配和测试

### 执行步骤

1. 定义 InteractiveHandler、SessionTargetResolver、InteractiveBindingStore、SessionNotifier。
2. 迁移显式 Session 命令、reply-to-session、last session fallback 和 PTY 输入注入。
3. 保留逐字符或现有输入行为，不趁机重写 PTY 输入协议。
4. 迁移 Session idle/ended/waiting 通知生成；Channel Core 只发送中立 OutboundMessage。
5. 确保迁移前后消息顺序、错误提示、typing 状态和 cooldown 行为一致。
6. 移除 Channel Hub 对 SessionInputter 的直接依赖。

### 必须新增或补齐的测试

- 运行 OD-OS-01 的全部 PTY regression gate。
- Session adapter 单元测试。
- 迁移前后 golden fixture 对比。
- 错误 Session、过期 binding、跨 conversation 隔离。

### 验收标准

- internal/channel 不再直接写 PTY。
- Session Channel Adapter 独立承担交互模式行为。
- 所有现有 Telegram Session 测试通过。

### 建议 Commit

```text
refactor(session): isolate channel adapter from channel core
```

### 必须生成的报告

`docs/development/oneshot/reports/OD-OS-05-summary.md`

报告必须包含：

- 状态：PASS / NEEDS CHANGES / BLOCKED。
- 实际开始时间、结束时间、耗时。
- 基线 Commit、最终 Commit。
- 根因或设计结论。
- 修改文件。
- 修复前失败测试。
- 修复后通过测试。
- 模块测试和完整门禁。
- API、数据库、Telegram、移动端影响。
- 兼容性与 PTY 回归。
- Commit Hash、Push 结果。
- 未完成项和下一任务。


## 7. OD-OS-06 — 共享出站 Channel Delivery 抽象

### 任务状态

- [x] 已完成

### 优先级

`P1`

### 依赖

OD-OS-05

### 目标

抽取可由 PTY 和 One-shot 共同使用的中立出站发送、编辑、分段、限流、重试和回执能力，但不共享业务通知判断。

### 必须读取

- `internal/channel/card.go、typing.go、hub.go、各 channel sender`
- 现有 outbound message 持久化

### 允许修改范围

- `internal/channel/delivery/** 或等效目录`
- Session Notifier 适配
- 测试

### 执行步骤

1. 定义 OutboundMessage、ChannelDeliveryService、DeliveryReceipt、ChannelDeliveryAttempt。
2. 抽取超长文本分段、格式转义、附件发送、编辑消息、限流和重发。
3. 区分渠道发送重试与任务执行重试。
4. 增加 outbox 或等效持久化，避免 Gateway 崩溃后丢失完成通知。
5. 迁移现有 Session 通知使用该服务，不改变用户看到的内容。

### 必须新增或补齐的测试

- Telegram 超长内容分段和顺序。
- 发送失败重试不重复执行业务动作。
- 编辑失败 fallback。
- outbox 崩溃恢复。
- Session 通知回归。

### 验收标准

- PTY 和未来 One-shot 可共享 Transport/Delivery。
- Channel Delivery 不依赖 session 或 oneshot。
- 任务重试和通知重发数据结构清晰分离。

### 建议 Commit

```text
refactor(channel): extract outbound delivery service
```

### 必须生成的报告

`docs/development/oneshot/reports/OD-OS-06-summary.md`

报告必须包含：

- 状态：PASS / NEEDS CHANGES / BLOCKED。
- 实际开始时间、结束时间、耗时。
- 基线 Commit、最终 Commit。
- 根因或设计结论。
- 修改文件。
- 修复前失败测试。
- 修复后通过测试。
- 模块测试和完整门禁。
- API、数据库、Telegram、移动端影响。
- 兼容性与 PTY 回归。
- Commit Hash、Push 结果。
- 未完成项和下一任务。


## 8. OD-OS-07 — One-shot 领域模型与状态机实现

### 任务状态

- [x] 已完成源码实现、纯领域单元测试与架构边界门禁；Go 1.25 全仓运行验证按用户决定留到本机最终验收

### 优先级

`P0`

### 依赖

OD-OS-03

### 目标

实现纯领域对象、状态转换和不可变量，不接数据库、不启动进程、不接 Telegram。

### 必须读取

- `冻结的 domain-model.md、state-machines.md、errors.md`
- 项目现有 ID、时间、错误处理惯例

### 允许修改范围

- `新增 internal/oneshot/domain/**`
- 纯单元测试

### 执行步骤

1. 实现 Task、Delivery、Run、RuntimeContext、StreamRecord、StandardEvent、Artifact。
2. 实现显式状态转换函数，禁止外部任意赋值。
3. 实现所有权、项目、Provider、当前 Run 和 Context 约束。
4. 实现不可变 Source/ReplyAddress 快照。
5. 实现稳定错误类型和错误码。
6. 不导入 internal/session。

### 必须新增或补齐的测试

- 每条合法状态转换。
- 每条非法状态转换。
- 跨 principal/project/provider Context 使用失败。
- 一个 Task 不能同时绑定两个活动 Run。
- 完成、取消、超时等终态不可逆。

### 验收标准

- 领域测试覆盖冻结状态机全部边。
- package 无数据库、HTTP、PTY 或 Channel 具体依赖。
- 架构边界检查通过。

### 建议 Commit

```text
feat(oneshot): add isolated domain model and state machines
```

### 必须生成的报告

`docs/development/oneshot/reports/OD-OS-07-summary.md`

报告必须包含：

- 状态：PASS / NEEDS CHANGES / BLOCKED。
- 实际开始时间、结束时间、耗时。
- 基线 Commit、最终 Commit。
- 根因或设计结论。
- 修改文件。
- 修复前失败测试。
- 修复后通过测试。
- 模块测试和完整门禁。
- API、数据库、Telegram、移动端影响。
- 兼容性与 PTY 回归。
- Commit Hash、Push 结果。
- 未完成项和下一任务。


## 9. OD-OS-08 — One-shot PostgreSQL Migration 与 Store

### 任务状态

- [x] 已完成（源码门禁完成；Go 1.25 与真实 PostgreSQL 运行门禁保留到本机调试）

### 优先级

`P0`

### 依赖

OD-OS-07

### 目标

增加独立 One-shot 表、索引、约束和 Store，实现事务一致性与旧版本安全升级。

### 必须读取

- `internal/store/migrate.go、migrations/**`
- 现有 subsystem-owned query 模式
- 冻结数据库契约

### 允许修改范围

- `internal/store/migrations/<next>_oneshot.sql`
- `internal/oneshot/store/**`
- DB integration tests

### 执行步骤

1. 新增 oneshot_tasks、deliveries、runs、runtime_contexts、stream_records、standard_events、artifacts、channel_bindings、idempotency_keys、notification_outbox。
2. 建立外键、唯一键、CHECK、部分唯一索引和查询索引。
3. Task/Delivery/Run 创建使用数据库事务。
4. Store 方法必须接受 context，错误包装符合项目风格。
5. 增加 migration 重复执行、空库升级、已有数据升级和 rollback-on-failure 测试。
6. 禁止修改 sessions 表为 One-shot 添加 mode。

### 必须新增或补齐的测试

- 空数据库 migrate。
- 重复 migrate 幂等。
- 唯一约束、外键和状态 CHECK。
- 并发创建活动 Run 的冲突。
- Store CRUD、分页、所有权过滤。

### 验收标准

- Migration 在事务中完成并记录 schema_migrations。
- One-shot 与 Session 表无业务外键交叉。
- 数据库约束能阻止关键不一致。

### 建议 Commit

```text
feat(oneshot): add postgres schema and stores
```

### 必须生成的报告

`docs/development/oneshot/reports/OD-OS-08-summary.md`

报告必须包含：

- 状态：PASS / NEEDS CHANGES / BLOCKED。
- 实际开始时间、结束时间、耗时。
- 基线 Commit、最终 Commit。
- 根因或设计结论。
- 修改文件。
- 修复前失败测试。
- 修复后通过测试。
- 模块测试和完整门禁。
- API、数据库、Telegram、移动端影响。
- 兼容性与 PTY 回归。
- Commit Hash、Push 结果。
- 未完成项和下一任务。


## 10. OD-OS-09 — Delivery Queue、Lease、幂等与死信

### 任务状态

- [x] 已完成（源码门禁；本机运行门禁保留）

### 优先级

`P0`

### 依赖

OD-OS-08

### 目标

基于 PostgreSQL 实现可靠异步投递，确保多 Worker 不重复消费，崩溃后可恢复。

### 必须读取

- 冻结 Delivery 状态机
- pgx transaction 使用方式
- 现有 Gateway 后台 worker 生命周期

### 允许修改范围

- `internal/oneshot/queue/**`
- `internal/oneshot/application/dispatch_service.go`
- 测试和配置

### 执行步骤

1. 使用 FOR UPDATE SKIP LOCKED 领取可用 Delivery。
2. 实现 lease_owner、lease_until、attempt、available_at、max_attempts。
3. 实现 ack、nack、retry backoff、dead-letter、cancel。
4. 实现 Telegram/API Idempotency-Key 与请求 payload hash。
5. 确保相同来源消息只创建一个 Task；相同 key 不同 payload 返回冲突。
6. Worker 崩溃后过期 lease 可重新领取，但不得重复已有成功 Run。

### 必须新增或补齐的测试

- 多个 Worker 竞争同一 Delivery。
- lease 过期恢复。
- ack/nack 状态转换。
- 最大重试进入 dead-letter。
- 重复 Update/Idempotency-Key。
- 服务重启后不重复执行已成功任务。

### 验收标准

- 并发测试中同一 Delivery 只有一个有效领取者。
- 所有重试路径可审计。
- 幂等逻辑由数据库约束和事务共同保证。

### 建议 Commit

```text
feat(oneshot): add reliable postgres delivery queue
```

### 必须生成的报告

`docs/development/oneshot/reports/OD-OS-09-summary.md`

报告必须包含：

- 状态：PASS / NEEDS CHANGES / BLOCKED。
- 实际开始时间、结束时间、耗时。
- 基线 Commit、最终 Commit。
- 根因或设计结论。
- 修改文件。
- 修复前失败测试。
- 修复后通过测试。
- 模块测试和完整门禁。
- API、数据库、Telegram、移动端影响。
- 兼容性与 PTY 回归。
- Commit Hash、Push 结果。
- 未完成项和下一任务。


## 11. OD-OS-10 — Shell One-shot Adapter 与 Executor 骨架

### 任务状态

- [x] 已完成（源码门禁通过；本机完整运行门禁保留）

### 优先级

`P0`

### 依赖

OD-OS-09

### 目标

以受控 Shell fixture 验证普通子进程 One-shot 执行链，不接真实 AI Provider。

### 必须读取

- `os/exec、context cancellation 的项目使用习惯`
- `现有 catalog/provider 描述，但不得调用 PTY Spawn`
- 冻结 Adapter 契约

### 允许修改范围

- `internal/oneshot/adapter/shell.go`
- `internal/oneshot/executor/**`
- 测试 fixture scripts

### 执行步骤

1. 定义 CommandSpec、ExecutionInput、ExecutionResult、OneShotAdapter 最小接口。
2. 实现 Shell 测试 Adapter，只允许测试白名单命令。
3. 使用 exec.CommandContext 或等效普通进程启动，不创建 PTY。
4. 创建 Run 并保存 pid、start time、exit code、finish time。
5. 将 Worker → RunService → Executor 的最小成功链跑通。
6. 明确生产环境 Shell One-shot 默认关闭。

### 必须新增或补齐的测试

- 成功退出、非零退出、命令不存在、cwd 不存在。
- 环境变量白名单和 secret 脱敏。
- 证明没有调用 pty.Start。
- 证明没有写 sessions/session_transcripts。

### 验收标准

- 一个 Delivery 能驱动一个普通子进程并完成 Run。
- One-shot 关闭时 Worker 不启动。
- PTY 回归门禁通过。

### 建议 Commit

```text
feat(oneshot): add shell adapter and executor skeleton
```

### 必须生成的报告

`docs/development/oneshot/reports/OD-OS-10-summary.md`

报告必须包含：

- 状态：PASS / NEEDS CHANGES / BLOCKED。
- 实际开始时间、结束时间、耗时。
- 基线 Commit、最终 Commit。
- 根因或设计结论。
- 修改文件。
- 修复前失败测试。
- 修复后通过测试。
- 模块测试和完整门禁。
- API、数据库、Telegram、移动端影响。
- 兼容性与 PTY 回归。
- Commit Hash、Push 结果。
- 未完成项和下一任务。


## 12. OD-OS-11 — stdout/stderr 有序采集、Artifact 与 StandardEvent

### 任务状态

- [x] 已完成（源码门禁通过；本机完整运行门禁保留）

### 优先级

`P0`

### 依赖

OD-OS-10

### 目标

按真实接收顺序增量保存 stdout/stderr、原始字节、标准事件和最终 Artifact。

### 必须读取

- `冻结 StreamRecord/StandardEvent/Artifact 契约`
- 现有 JSONL 解析能力仅作参考，不直接耦合 Session transcript

### 允许修改范围

- `internal/oneshot/executor/stream_reader.go、output_collector.go`
- `internal/oneshot/store event/artifact store`
- 测试 fixture

### 执行步骤

1. 并发读取 stdout/stderr，每个 chunk 分配全局单调 sequence。
2. 保存 stream、sequence、received_at、byte_length、raw artifact reference。
3. 文本解码与原始字节分离；非法编码不得丢失。
4. 实现流式落盘，避免大输出全部驻留内存。
5. Shell Adapter 生成 passthrough StandardEvent。
6. 最终结果必须能反查原始 StreamRecord。

### 必须新增或补齐的测试

- stdout/stderr 交错输出。
- 无换行长输出。
- UTF-8 多字节跨 chunk。
- 非法字节。
- 大输出内存边界。
- sequence 唯一、递增、可分页恢复。

### 验收标准

- 事件顺序反映实际接收顺序，不是固定 stdout 优先。
- 原始字节完整可取回。
- 输出不写入 PTY ring buffer 或 session transcript。

### 建议 Commit

```text
feat(oneshot): capture ordered streams and artifacts
```

### 必须生成的报告

`docs/development/oneshot/reports/OD-OS-11-summary.md`

报告必须包含：

- 状态：PASS / NEEDS CHANGES / BLOCKED。
- 实际开始时间、结束时间、耗时。
- 基线 Commit、最终 Commit。
- 根因或设计结论。
- 修改文件。
- 修复前失败测试。
- 修复后通过测试。
- 模块测试和完整门禁。
- API、数据库、Telegram、移动端影响。
- 兼容性与 PTY 回归。
- Commit Hash、Push 结果。
- 未完成项和下一任务。


## 13. OD-OS-12 — ProcessSupervisor、取消、超时与进程树

### 任务状态

- [x] 已完成（源码门禁通过；完整运行门禁待本机）

### 优先级

`P0`

### 依赖

OD-OS-11

### 目标

实现独立 One-shot ProcessSupervisor，取消和超时必须终止整个进程树并保留终止前输出。

### 必须读取

- `internal/session/signals_unix.go、signals_windows.go 仅作平台参考`
- `部署环境 Linux/macOS/WSL2 的事实`
- `冻结 cancel/timeout 契约`

### 允许修改范围

- `internal/oneshot/executor/process_supervisor*.go`
- 配置和平台测试

### 执行步骤

1. 定义 Start、TerminateTree、KillTree、Wait、IsAlive。
2. Unix/WSL2 使用独立进程组；Windows 原生支持按项目实际部署策略实现或明确不支持。
3. 取消先 TERM/优雅退出，超过 grace period 后 KILL。
4. 超时使用同一清理链。
5. 进程退出后继续排空 stdout/stderr。
6. 取消 API 只能在实际终止成功或明确失败后更新终态。

### 必须新增或补齐的测试

- 父进程启动多级子进程。
- 取消后无残留进程。
- 超时后无残留进程。
- TERM 无响应时 KILL fallback。
- 取消前和超时前输出保留。
- 重复取消幂等。

### 验收标准

- 取消不是仅修改数据库状态。
- 支持平台均有证据；未支持平台由 capability 明确拒绝。
- PTY Session 终止实现未被 One-shot 复用或修改。

### 建议 Commit

```text
feat(oneshot): add isolated process supervision and cancellation
```

### 必须生成的报告

`docs/development/oneshot/reports/OD-OS-12-summary.md`

报告必须包含：

- 状态：PASS / NEEDS CHANGES / BLOCKED。
- 实际开始时间、结束时间、耗时。
- 基线 Commit、最终 Commit。
- 根因或设计结论。
- 修改文件。
- 修复前失败测试。
- 修复后通过测试。
- 模块测试和完整门禁。
- API、数据库、Telegram、移动端影响。
- 兼容性与 PTY 回归。
- Commit Hash、Push 结果。
- 未完成项和下一任务。


## 14. OD-OS-13 — 执行 Saga、失败补偿与崩溃恢复

### 任务状态

- [x] 已完成（源码门禁通过；完整运行门禁待本机）

### 优先级

`P0`

### 依赖

OD-OS-12

### 目标

建立 Delivery、Run、凭据租约、输出和 Task 状态的一致执行 Saga。

### 必须读取

- 冻结状态机和错误码
- 队列、Store、Executor、Channel outbox

### 允许修改范围

- `internal/oneshot/application/run_service.go`
- `recovery/reconciler`
- 故障注入测试

### 执行步骤

1. 实现 reserve Delivery → create Run → acquire credential → start → capture → persist → ack 的显式步骤。
2. 为每个步骤定义补偿：Run failed、进程清理、输出排空、凭据释放、nack/retry、Task 更新、通知。
3. ACK 失败不得返回成功。
4. 服务启动时 reconcile starting/running/collecting_output 和过期 lease。
5. 已完成进程但 ACK 失败时不得重跑模型，应恢复提交状态。
6. 保存 failure_stage、primary_error、compensation_error。

### 必须新增或补齐的测试

- Run 创建失败、进程启动失败、输出写入失败、Artifact 失败。
- ACK/NACK 失败。
- Worker 在每个阶段崩溃。
- Gateway 重启恢复。
- 凭据租约释放。

### 验收标准

- 不存在永久 running/reserved 且无恢复路径的记录。
- 失败原因和补偿失败都可审计。
- 重启后不会重复执行已完成 Run。

### 建议 Commit

```text
feat(oneshot): make run lifecycle crash-consistent
```

### 必须生成的报告

`docs/development/oneshot/reports/OD-OS-13-summary.md`

报告必须包含：

- 状态：PASS / NEEDS CHANGES / BLOCKED。
- 实际开始时间、结束时间、耗时。
- 基线 Commit、最终 Commit。
- 根因或设计结论。
- 修改文件。
- 修复前失败测试。
- 修复后通过测试。
- 模块测试和完整门禁。
- API、数据库、Telegram、移动端影响。
- 兼容性与 PTY 回归。
- Commit Hash、Push 结果。
- 未完成项和下一任务。


## 15. OD-OS-14 — One-shot Provider Capability 与 Adapter Registry

### 任务状态

- [x] 已完成（源码门禁通过；完整运行门禁待本机）

### 优先级

`P1`

### 依赖

OD-OS-13

### 目标

建立独立于 PTY 启动流程的 One-shot Provider Adapter 注册、能力探测和共享元数据接口。

### 必须读取

- `internal/catalog/adapter.go、catalog.go、builtin/**`
- `internal/providers/**、cliacct/**`
- 冻结 Adapter 契约

### 允许修改范围

- `internal/oneshot/adapter/adapter.go、registry.go`
- `共享 ProviderCatalog/CredentialAllocator 小接口`
- 测试

### 执行步骤

1. 定义 SupportsNonInteractive、SupportsResume、StructuredOutput、Attachments、Cancellation。
2. 共享 CLI 路径、版本、账号、环境变量，但不共享 PTY args/input。
3. Provider 不支持 One-shot 时明确拒绝，禁止自动回退 PTY。
4. Adapter 注册冲突、缺失和版本不兼容返回稳定错误。
5. 将 capability 暴露给 API/移动端。

### 必须新增或补齐的测试

- 注册、重复注册、未知 Provider。
- unsupported capability。
- 共享凭据分配但不导入 session package。
- one-shot disabled/provider disabled。

### 验收标准

- One-shot Adapter 与 PTY Catalog 的边界清晰。
- 客户端可准确隐藏不支持的操作。
- 架构 import 检查通过。

### 建议 Commit

```text
feat(oneshot): add provider capability registry
```

### 必须生成的报告

`docs/development/oneshot/reports/OD-OS-14-summary.md`

报告必须包含：

- 状态：PASS / NEEDS CHANGES / BLOCKED。
- 实际开始时间、结束时间、耗时。
- 基线 Commit、最终 Commit。
- 根因或设计结论。
- 修改文件。
- 修复前失败测试。
- 修复后通过测试。
- 模块测试和完整门禁。
- API、数据库、Telegram、移动端影响。
- 兼容性与 PTY 回归。
- Commit Hash、Push 结果。
- 未完成项和下一任务。


## 16. OD-OS-15 — Codex One-shot Adapter

### 任务状态

- [x] 源码实现完成；完整运行门禁待本机

### 优先级

`P0`

### 依赖

OD-OS-14

### 目标

接入当前安装版本 Codex CLI 的真实非交互执行、结构化输出、Context ID 和取消能力。

### 必须读取

- Codex 当前官方 CLI 帮助和本机版本输出
- `internal/session/codex_jsonl.go 仅作解析参考`
- `catalog 中 Codex descriptor/account 逻辑`

### 允许修改范围

- `internal/oneshot/adapter/codex.go`
- Codex fixtures 和集成测试
- capability 文档

### 执行步骤

1. 先运行 codex --help/版本探测，冻结实际参数，不凭记忆编码。
2. 实现新任务命令构造、cwd、模型/推理参数、环境和凭据。
3. 优先使用官方结构化/JSON 输出模式。
4. 提取真实 provider context/session/thread ID。
5. 标准化工具调用、文本、最终结果和错误。
6. 实现取消和退出码映射。
7. 无凭据的 CI 使用 deterministic fake CLI；真实环境另有集成门禁。

### 必须新增或补齐的测试

- 参数构造 golden test。
- JSON/JSONL fixture。
- 真实新任务 smoke test。
- 非零退出、认证失败、限流、未知输出。
- 取消和输出保留。

### 验收标准

- 真实 Codex One-shot 不创建 PTY。
- 最终结果、原始输出和 Context ID 可追溯。
- 未安装/未登录时返回可诊断错误。

### 建议 Commit

```text
feat(oneshot): add codex non-interactive adapter
```

### 必须生成的报告

`docs/development/oneshot/reports/OD-OS-15-summary.md`

报告必须包含：

- 状态：PASS / NEEDS CHANGES / BLOCKED。
- 实际开始时间、结束时间、耗时。
- 基线 Commit、最终 Commit。
- 根因或设计结论。
- 修改文件。
- 修复前失败测试。
- 修复后通过测试。
- 模块测试和完整门禁。
- API、数据库、Telegram、移动端影响。
- 兼容性与 PTY 回归。
- Commit Hash、Push 结果。
- 未完成项和下一任务。


## 17. OD-OS-16 — Claude Code One-shot Adapter

### 任务状态

- [x] 源码实现完成；完整运行门禁待本机

### 优先级

`P0`

### 依赖

OD-OS-14

### 目标

接入当前安装版本 Claude Code CLI 的真实非交互执行、结构化输出、Context ID 和取消能力。

### 必须读取

- Claude Code 当前官方 CLI 帮助和本机版本输出
- `internal/session/claude_jsonl.go 仅作解析参考`
- `catalog/账号逻辑`

### 允许修改范围

- `internal/oneshot/adapter/claude.go`
- Claude fixtures 和集成测试
- capability 文档

### 执行步骤

1. 先探测当前 CLI 的 print/non-interactive、output-format、resume 等真实参数。
2. 实现新任务命令、cwd、模型、权限模式和凭据环境。
3. 解析结构化事件和最终结果。
4. 提取真实 Provider Context ID。
5. 映射认证、限流、权限拒绝、工具失败和进程错误。
6. 严格脱敏 CLAUDE_CONFIG_DIR、Token 和环境变量。

### 必须新增或补齐的测试

- 参数构造 golden test。
- 结构化输出 fixture。
- 真实新任务 smoke test。
- 认证/限流/权限错误。
- 取消和输出保留。

### 验收标准

- 真实 Claude One-shot 不创建 PTY。
- 原始输出与标准事件均完整。
- Provider 能力声明与实际测试一致。

### 建议 Commit

```text
feat(oneshot): add claude code non-interactive adapter
```

### 必须生成的报告

`docs/development/oneshot/reports/OD-OS-16-summary.md`

报告必须包含：

- 状态：PASS / NEEDS CHANGES / BLOCKED。
- 实际开始时间、结束时间、耗时。
- 基线 Commit、最终 Commit。
- 根因或设计结论。
- 修改文件。
- 修复前失败测试。
- 修复后通过测试。
- 模块测试和完整门禁。
- API、数据库、Telegram、移动端影响。
- 兼容性与 PTY 回归。
- Commit Hash、Push 结果。
- 未完成项和下一任务。


## 18. OD-OS-17 — RuntimeContext 与 Continue/Resume

### 任务状态

- [x] 源码实现完成；完整运行门禁待本机

### 优先级

`P0`

### 依赖

OD-OS-15, OD-OS-16

### 目标

实现 One-shot 独有 RuntimeContext；继续任务时启动新进程并使用 Provider 官方 resume 能力。

### 必须读取

- `Codex/Claude Adapter 的真实 Context ID 和 resume 参数`
- 冻结 RuntimeContext 所有权契约

### 允许修改范围

- `internal/oneshot/application/continuation_service.go`
- context store、adapter resume
- 集成测试

### 执行步骤

1. 首次 Run 成功后保存真实 ProviderContextID。
2. Continue 创建新的 Run，不复用旧进程。
3. 恢复前校验 principal、project、provider、workspace path 和 Context status。
4. 同一 RuntimeContext 禁止并发 Run。
5. resume 失败不得静默创建新 Context。
6. Provider 不支持 resume 时返回 capability error。

### 必须新增或补齐的测试

- Codex 新上下文 → 第二次 Run 读取第一次上下文。
- Claude 新上下文 → 第二次 Run 读取第一次上下文。
- 跨用户、项目、Provider、目录恢复失败。
- 并发恢复冲突。
- resume failure 不创建新 Context。

### 验收标准

- 上下文延续由真实 Provider 行为证明，不是只比较 ID。
- RuntimeContext 不引用 sessions 表。
- 失败不会误报为成功或新会话。

### 建议 Commit

```text
feat(oneshot): add isolated runtime context continuation
```

### 必须生成的报告

`docs/development/oneshot/reports/OD-OS-17-summary.md`

报告必须包含：

- 状态：PASS / NEEDS CHANGES / BLOCKED。
- 实际开始时间、结束时间、耗时。
- 基线 Commit、最终 Commit。
- 根因或设计结论。
- 修改文件。
- 修复前失败测试。
- 修复后通过测试。
- 模块测试和完整门禁。
- API、数据库、Telegram、移动端影响。
- 兼容性与 PTY 回归。
- Commit Hash、Push 结果。
- 未完成项和下一任务。


## 19. OD-OS-18 — Task/Run REST API、权限、审计与幂等

### 任务状态

- [x] 源码实现完成；运行门禁待本机环境

### 优先级

`P0`

### 依赖

OD-OS-17

### 目标

实现完整 One-shot REST 控制面，所有写操作强制权限、所有权、审计和幂等。

### 必须读取

- `internal/session/handler.go、channel/handler.go`
- `internal/auth、audit、integration API key scope`
- `冻结 http-api.md`

### 允许修改范围

- `internal/oneshot/api/**`
- Gateway route wiring
- API tests

### 执行步骤

1. 实现 create/list/get/continue/cancel/retry、runs/events/artifacts。
2. 逐路由校验 oneshot scope，不只声明 scope。
3. 按 principal/project 过滤；管理员越权行为需明确审计。
4. 写操作支持 Idempotency-Key。
5. 取消必须调用 ProcessSupervisor；retry 创建新 Delivery/Run。
6. 统一错误响应、分页、排序和时间格式。
7. 记录审计主体、资源、结果、request ID、idempotency key。

### 必须新增或补齐的测试

- 正常 CRUD/control 流程。
- 无 scope、错误 scope、跨用户、跨项目。
- 幂等重复请求和 payload 冲突。
- 取消实际终止进程。
- Artifact 所有权。
- OpenAPI/contract fixture 对齐。

### 验收标准

- API 与冻结契约一致。
- 每个写路由都有权限与审计证据。
- 不存在仅改状态不执行控制动作的接口。

### 建议 Commit

```text
feat(oneshot): expose secure task and run REST APIs
```

### 必须生成的报告

`docs/development/oneshot/reports/OD-OS-18-summary.md`

报告必须包含：

- 状态：PASS / NEEDS CHANGES / BLOCKED。
- 实际开始时间、结束时间、耗时。
- 基线 Commit、最终 Commit。
- 根因或设计结论。
- 修改文件。
- 修复前失败测试。
- 修复后通过测试。
- 模块测试和完整门禁。
- API、数据库、Telegram、移动端影响。
- 兼容性与 PTY 回归。
- Commit Hash、Push 结果。
- 未完成项和下一任务。


## 20. OD-OS-19 — One-shot WebSocket、事件回放与通知 Outbox

### 任务状态

- [x] 源码实现完成；运行门禁待本机环境

### 优先级

`P1`

### 依赖

OD-OS-18

### 目标

为移动端和集成方提供实时 Task/Run 事件、断线续传和完成通知可靠投递。

### 必须读取

- 现有 WS utilities、eventbus、Session stream
- Channel Delivery outbox
- `冻结 events.md`

### 允许修改范围

- `internal/oneshot/api/websocket.go`
- `event publisher/replay cursor`
- notification outbox integration

### 执行步骤

1. 实现 task stream 和 run stream。
2. 事件携带 sequence/cursor；客户端重连可从 cursor 回放。
3. 慢消费者有缓冲上限和明确断开策略。
4. 事件持久化先于发布，避免发布成功但数据库缺失。
5. Task 完成/失败/等待输入写入通知 outbox。
6. 事件 namespace 与 session.* 完全分开。

### 必须新增或补齐的测试

- 实时顺序、断线重连、重复事件去重。
- 慢消费者。
- 服务重启后的事件回放。
- outbox 发送失败重试不重跑任务。
- 跨用户订阅拒绝。

### 验收标准

- 移动端可仅依靠 API+WS 获取完整状态。
- 事件无丢失、可回放、有顺序。
- Session stream 不受影响。

### 建议 Commit

```text
feat(oneshot): add replayable task and run streams
```

### 必须生成的报告

`docs/development/oneshot/reports/OD-OS-19-summary.md`

报告必须包含：

- 状态：PASS / NEEDS CHANGES / BLOCKED。
- 实际开始时间、结束时间、耗时。
- 基线 Commit、最终 Commit。
- 根因或设计结论。
- 修改文件。
- 修复前失败测试。
- 修复后通过测试。
- 模块测试和完整门禁。
- API、数据库、Telegram、移动端影响。
- 兼容性与 PTY 回归。
- Commit Hash、Push 结果。
- 未完成项和下一任务。


## 21. OD-OS-20 — Telegram One-shot 命令、绑定与结果回传

### 任务状态

- [x] 源码实现完成；运行门禁待本机环境

### 优先级

`P0`

### 依赖

OD-OS-06, OD-OS-18, OD-OS-19

### 目标

在共享 Telegram Transport 上新增明确 One-shot 命令和独立 binding，绝不模糊路由到 PTY。

### 必须读取

- `internal/channel/telegram/**`
- Session Channel Adapter
- Channel Delivery Service
- 冻结 Telegram routing contract

### 允许修改范围

- `internal/oneshot/channeladapter/**`
- `Telegram 命令 parser/help/buttons`
- oneshot_channel_bindings
- E2E tests

### 执行步骤

1. 实现 /run、/tasks、/task、/continue、/cancel、/retry。
2. 支持 provider/project 明确选择和按钮交互。
3. 一条 Telegram Update 通过来源唯一键只创建一个 Task。
4. 保存 chat/thread/source message/reply address 独立 binding。
5. One-shot 通知回复只继续对应 Task；PTY 通知回复只进入 Session。
6. 普通文本维持现有 PTY fallback，不自动猜 One-shot。
7. 完成结果分摘要、日志/Artifact 链接和继续按钮。

### 必须新增或补齐的测试

- 每个命令的权限、参数和错误。
- 重复 Update。
- reply-to-One-shot 与 reply-to-PTY 双向隔离。
- 通知发送失败只重发通知。
- 跨 chat/thread/user 隔离。
- 超长结果和附件。

### 验收标准

- TG 可创建、查询、继续、取消、重试 One-shot。
- 任何测试中都不存在消息误投到另一模式。
- OD-OS-01 PTY Telegram 回归全部通过。

### 建议 Commit

```text
feat(oneshot): add isolated telegram task commands
```

### 必须生成的报告

`docs/development/oneshot/reports/OD-OS-20-summary.md`

报告必须包含：

- 状态：PASS / NEEDS CHANGES / BLOCKED。
- 实际开始时间、结束时间、耗时。
- 基线 Commit、最终 Commit。
- 根因或设计结论。
- 修改文件。
- 修复前失败测试。
- 修复后通过测试。
- 模块测试和完整门禁。
- API、数据库、Telegram、移动端影响。
- 兼容性与 PTY 回归。
- Commit Hash、Push 结果。
- 未完成项和下一任务。


## 22. OD-OS-21 — Flutter Agent Tasks 数据层与导航基础

### 任务状态

- [x] 已完成（源码门禁通过；Flutter 运行门禁待本机）

### 优先级

`P0`

### 依赖

OD-OS-18, OD-OS-19

### 目标

在现有 Flutter App 中新增独立 Agent Tasks feature、API client、WS repository、模型和一级导航，不修改 Terminal 页面语义。

### 必须读取

- `app/mobile/lib 现有 features、routing、API、auth、WS、i18n 组织`
- `app/mobile/test`
- 冻结移动端页面契约

### 允许修改范围

- `app/mobile/lib/features/agent_tasks/**`
- 共享导航和 i18n
- 移动端测试

### 执行步骤

1. 建立 data/domain/presentation 分层。
2. 实现 Task、Run、Event、Artifact、Capability 模型和序列化。
3. 实现 REST client、WS stream、cursor reconnect、错误映射。
4. 增加“Agent 任务”一级入口，与“终端会话”并列。
5. 增加所有现有语言 key；不得只补中英文。
6. 确保 feature 不依赖 xterm/PTY Session 模型。

### 必须新增或补齐的测试

- JSON 模型、API repository、WS reconnect。
- 路由和导航。
- 登录失效、权限错误、网络中断。
- i18n parity。
- Session/Terminal widget 回归。

### 验收标准

- App 可进入独立 Agent Tasks 空态。
- 数据层能连接真实后端契约。
- 现有 Terminal 页面和导航不回归。

### 建议 Commit

```text
feat(mobile): add agent task feature foundation
```

### 必须生成的报告

`docs/development/oneshot/reports/OD-OS-21-summary.md`

报告必须包含：

- 状态：PASS / NEEDS CHANGES / BLOCKED。
- 实际开始时间、结束时间、耗时。
- 基线 Commit、最终 Commit。
- 根因或设计结论。
- 修改文件。
- 修复前失败测试。
- 修复后通过测试。
- 模块测试和完整门禁。
- API、数据库、Telegram、移动端影响。
- 兼容性与 PTY 回归。
- Commit Hash、Push 结果。
- 未完成项和下一任务。


## 23. OD-OS-22 — Flutter Task 列表与创建流程

### 任务状态

- [x] 已完成（源码门禁通过；Flutter 运行门禁待本机）

### 优先级

`P0`

### 依赖

OD-OS-21

### 目标

实现移动端 Task 列表、筛选、详情入口和创建 One-shot Task 的完整流程。

### 必须读取

- `现有 Sessions/Projects/Providers 页面设计和组件`
- Provider capability API

### 允许修改范围

- `agent_tasks presentation/task_list、create_task`
- 相关状态管理和测试

### 执行步骤

1. 列表展示 pending/queued/running/waiting/completed/failed/cancelled/timed_out。
2. 支持分页、刷新、筛选、空态、错误态、离线态。
3. 创建页选择 Project、Provider、新 Context/继续 Context、Prompt、附件、超时、Telegram 通知。
4. 按 Provider capability 隐藏不支持的 resume/attachment。
5. 提交使用 Idempotency-Key，防止重复点击。
6. 创建成功进入 Task 详情并订阅事件。

### 必须新增或补齐的测试

- 列表所有状态和分页。
- 创建表单校验。
- 重复点击幂等。
- Provider capability。
- 网络失败重试。
- 可访问性和小屏布局。

### 验收标准

- 移动端能稳定创建真实 One-shot Task。
- 没有 Terminal 控件混入 Task 页面。
- TG 创建的 Task 能在列表看到。

### 建议 Commit

```text
feat(mobile): add task list and creation flow
```

### 必须生成的报告

`docs/development/oneshot/reports/OD-OS-22-summary.md`

报告必须包含：

- 状态：PASS / NEEDS CHANGES / BLOCKED。
- 实际开始时间、结束时间、耗时。
- 基线 Commit、最终 Commit。
- 根因或设计结论。
- 修改文件。
- 修复前失败测试。
- 修复后通过测试。
- 模块测试和完整门禁。
- API、数据库、Telegram、移动端影响。
- 兼容性与 PTY 回归。
- Commit Hash、Push 结果。
- 未完成项和下一任务。


## 24. OD-OS-23 — Flutter Task/Run 详情、实时输出与控制

### 任务状态

- [x] 已完成（源码门禁通过；Flutter 运行门禁待本机）

### 优先级

`P0`

### 依赖

OD-OS-22

### 目标

实现 Task/Run 状态时间线、实时输出、最终结果、Artifact、Continue、Cancel 和 Retry。

### 必须读取

- `One-shot API/WS 契约`
- 现有 Markdown、代码高亮、文件下载能力

### 允许修改范围

- agent_tasks task_detail、run_detail、output_view、artifact_view
- 测试

### 执行步骤

1. Task 详情展示 source、project、provider、context、current run、状态时间线。
2. Run 详情展示 StandardEvent，允许切换 stdout/stderr/raw。
3. 按 cursor 实时追加并去重；重连后补齐。
4. 实现 Continue、Cancel、Retry 的确认、loading、结果和错误。
5. Artifact 下载/打开必须校验权限和完整性。
6. waiting_input 状态突出显示并提供 Continue 输入。

### 必须新增或补齐的测试

- 事件顺序、重复、断线续传。
- 取消后 UI 与进程真实状态一致。
- Continue 创建新 Run。
- Retry 与 Continue 区分。
- 大输出虚拟化或性能。
- Artifact 下载。

### 验收标准

- 手机可完整操作任务，无需 Web/SSH。
- 状态变化与后端事实一致。
- 现有 Terminal 详情功能不回归。

### 建议 Commit

```text
feat(mobile): add task run details and controls
```

### 必须生成的报告

`docs/development/oneshot/reports/OD-OS-23-summary.md`

报告必须包含：

- 状态：PASS / NEEDS CHANGES / BLOCKED。
- 实际开始时间、结束时间、耗时。
- 基线 Commit、最终 Commit。
- 根因或设计结论。
- 修改文件。
- 修复前失败测试。
- 修复后通过测试。
- 模块测试和完整门禁。
- API、数据库、Telegram、移动端影响。
- 兼容性与 PTY 回归。
- Commit Hash、Push 结果。
- 未完成项和下一任务。


## 25. OD-OS-24 — 跨端同步、附件与通知策略

### 任务状态

- [x] 已完成源码实现与可执行源码门禁；本机/真实环境运行门禁保留

### 优先级

`P1`

### 依赖

OD-OS-20, OD-OS-23

### 目标

保证 Telegram 与移动端共享同一 Task/Run，附件和通知不会形成两套状态或重复执行。

### 必须读取

- Channel attachment staging
- `Mobile file/image picker`
- notification outbox

### 允许修改范围

- One-shot attachment service
- `TG/Mobile source binding`
- 通知偏好和跨端测试

### 执行步骤

1. 统一附件 staging、扫描、大小/MIME 限制、生命周期和 Artifact 引用。
2. 移动端创建的 Task 可选 Telegram 完成通知。
3. TG 创建/取消/继续后移动端通过 WS 立即同步。
4. 移动端操作后 Telegram 通知只发送一次。
5. 通知失败与任务状态分离。
6. 审计每次跨端操作的 principal、source 和 reply address。

### 必须新增或补齐的测试

- TG 创建→Mobile 可见。
- Mobile cancel→TG 状态通知。
- TG continue→Mobile 新 Run。
- 重复通知去重。
- 恶意/超大附件、路径穿越、MIME 欺骗。

### 验收标准

- 同一 Task 在两个客户端只有一套后端事实。
- 附件安全且可追溯。
- 通知重试不创建新 Run。

### 建议 Commit

```text
feat(oneshot): synchronize mobile telegram and attachments
```

### 必须生成的报告

`docs/development/oneshot/reports/OD-OS-24-summary.md`

报告必须包含：

- 状态：PASS / NEEDS CHANGES / BLOCKED。
- 实际开始时间、结束时间、耗时。
- 基线 Commit、最终 Commit。
- 根因或设计结论。
- 修改文件。
- 修复前失败测试。
- 修复后通过测试。
- 模块测试和完整门禁。
- API、数据库、Telegram、移动端影响。
- 兼容性与 PTY 回归。
- Commit Hash、Push 结果。
- 未完成项和下一任务。


## 26. OD-OS-25 — 安全、可靠性、性能与故障注入

### 任务状态

- [x] 已完成源码实现与可执行源码门禁；本机/真实环境运行门禁保留

### 优先级

`P0 Gate`

### 依赖

OD-OS-24

### 目标

验证 One-shot 在远程消息派发场景下的权限、数据隔离、崩溃恢复、并发和资源边界。

### 必须读取

- `SECURITY.md、现有 auth/audit/secret 处理`
- 全部 One-shot 代码和契约

### 允许修改范围

- 测试、fixture、验证脚本、必要缺陷修复
- `docs/development/oneshot/evidence/**`

### 执行步骤

1. 执行跨用户、跨项目、跨 Provider、跨 Context、跨 Channel binding 攻击测试。
2. 执行命令注入、环境变量泄漏、路径穿越、附件攻击、日志 secret 检查。
3. 执行 Worker/Gateway/DB/Channel 故障注入。
4. 执行高并发 Task、队列竞争、大输出、慢消费者、长任务、取消风暴。
5. 检查 goroutine、进程、文件、DB 连接和磁盘资源泄漏。
6. 记录性能基线，不以牺牲正确性换吞吐。

### 必须新增或补齐的测试

- go test -race 相关包和全量。
- 数据库并发和故障测试。
- API authz fuzz/negative tests。
- 输出 parser fuzz tests。
- 移动端弱网/断网恢复。
- Telegram 重复 Update 和限流。

### 验收标准

- 无已知 P0/P1 安全与一致性问题。
- 所有故障场景有确定终态和恢复证据。
- 无孤儿进程、重复 Run、跨用户数据泄漏。

### 建议 Commit

```text
test(oneshot): harden security reliability and performance
```

### 必须生成的报告

`docs/development/oneshot/reports/OD-OS-25-summary.md`

报告必须包含：

- 状态：PASS / NEEDS CHANGES / BLOCKED。
- 实际开始时间、结束时间、耗时。
- 基线 Commit、最终 Commit。
- 根因或设计结论。
- 修改文件。
- 修复前失败测试。
- 修复后通过测试。
- 模块测试和完整门禁。
- API、数据库、Telegram、移动端影响。
- 兼容性与 PTY 回归。
- Commit Hash、Push 结果。
- 未完成项和下一任务。


## 27. OD-OS-26 — PTY 全量回归、双模式验收、文档与交付

### 任务状态

- [x] 已完成源码实现与可执行源码门禁；本机/真实环境运行门禁保留

### 优先级

`P0 Final Gate`

### 依赖

OD-OS-25

### 目标

证明现有 PTY 完整保留、One-shot 闭环可用、两模式完全解耦，并生成可审查交付包。

### 必须读取

- 全部任务报告、契约、代码和测试
- README、配置、安装和移动端文档

### 允许修改范围

- 最终测试与缺陷修复
- `README/config/docs/changelog`
- `docs/development/oneshot/final-acceptance-report.md`

### 执行步骤

1. 执行 Web build/lint、Go vet/race/build/lint、i18n、Flutter analyze/test/build。
2. 执行真实 Codex 与 Claude 新任务、Continue、Cancel、Failure 闭环。
3. 执行 Telegram 和移动端完整用户链路。
4. 执行 OD-OS-01 PTY 全量回归。
5. 执行架构边界、数据库无交叉、功能开关和独立停启测试。
6. 更新 config.example.toml、README、Telegram 命令、移动端说明、API 集成文档。
7. 生成变更文件、测试、证据、已知限制、迁移、回滚和版本建议。
8. 确认 Git 工作区干净并 Push 所有 Commit。

### 必须新增或补齐的测试

- pnpm --filter web lint && pnpm --filter web build。
- go vet ./...。
- go test -race -timeout=5m -coverprofile=coverage.out ./...。
- golangci-lint run。
- go build ./cmd/opendray。
- node scripts/check-i18n-parity.mjs。
- flutter analyze、flutter test、flutter build apk --release。
- 双模式 E2E 和真实 Provider smoke。

### 验收标准

- 关闭 One-shot 后 PTY 全部正常。
- 不启动 PTY Session 也能运行 One-shot Worker。
- session 与 oneshot package 无双向依赖、数据表无业务交叉。
- TG 与移动端均能创建、查看、继续、取消和获取结果。
- 所有门禁 PASS，工作区干净，报告可复现。

### 建议 Commit

```text
feat(oneshot): complete isolated task execution platform
```

### 必须生成的报告

`docs/development/oneshot/reports/OD-OS-26-summary.md`

报告必须包含：

- 状态：PASS / NEEDS CHANGES / BLOCKED。
- 实际开始时间、结束时间、耗时。
- 基线 Commit、最终 Commit。
- 根因或设计结论。
- 修改文件。
- 修复前失败测试。
- 修复后通过测试。
- 模块测试和完整门禁。
- API、数据库、Telegram、移动端影响。
- 兼容性与 PTY 回归。
- Commit Hash、Push 结果。
- 未完成项和下一任务。


---

# 12. 里程碑门禁

## M0：基线与契约

包含：

- OD-OS-00 ～ OD-OS-03

门禁：

- 分支和基线固定。
- PTY 行为有回归测试。
- 双域边界、状态机、API、事件和错误码已冻结。
- 未实现 One-shot 生产代码。

## M1：渠道解耦

包含：

- OD-OS-04 ～ OD-OS-06

门禁：

- Channel Core 不直接写 PTY。
- Session Channel Adapter 承担原 PTY 行为。
- 出站 Transport 可共享。
- 现有 Telegram PTY 行为完全不变。

## M2：One-shot 内核

包含：

- OD-OS-07 ～ OD-OS-13

门禁：

- Task/Delivery/Run/Context 独立。
- PostgreSQL Queue、普通进程、输出、取消、Saga 可工作。
- 不创建 PTY、不写 Session 表。
- Shell fixture 完整通过。

## M3：真实 Provider

包含：

- OD-OS-14 ～ OD-OS-17

门禁：

- Codex、Claude Code 支持真实 One-shot。
- Context ID 和 Continue 已由真实行为证明。
- 不支持能力明确拒绝，绝不回退 PTY。

## M4：控制面与客户端

包含：

- OD-OS-18 ～ OD-OS-24

门禁：

- REST/WS、Telegram、Flutter Mobile 闭环。
- TG 和 Mobile 使用同一 Task/Run。
- 消息和状态不会跨模式、跨用户、跨项目。

## M5：最终质量门禁

包含：

- OD-OS-25 ～ OD-OS-26

门禁：

- 安全、故障、并发、资源测试通过。
- PTY 全量回归通过。
- 双模式解耦证据通过。
- 完整文档、迁移和回滚说明齐全。

# 13. 中断恢复

恢复时先执行：

```bash
git branch --show-current
git status --short --untracked-files=all
git log -5 --oneline
git diff --name-status
git diff --stat
```

然后读取：

```text
docs/development/oneshot/task-state.yaml
docs/development/oneshot/OPENDRAY_ONESHOT_DEVELOPMENT_TASKBOOK.md
docs/development/oneshot/reports/
```

恢复规则：

1. 已 Commit 且已 Push：只补状态和报告，不重复开发。
2. 已 Commit 未 Push：核对测试后继续 Push。
3. 有当前任务未提交修改：从最后记录阶段继续，先跑最小测试。
4. 来源不明修改：保留现场并 BLOCKED。
5. 测试卡住：检查进程、日志和最小用例，不直接重跑完整门禁。
6. 不得因上下文重启而重新制定整体方案。

# 14. 最终验收清单

- [ ] 原 PTY Session 创建、重连、输入、resize、resume、terminate 全部正常。
- [ ] 现有 Web Terminal 和 Flutter Terminal 正常。
- [ ] Telegram 回复原 Session 正常。
- [ ] One-shot 一个 Run 使用一个普通子进程，不创建 PTY。
- [ ] Task、Delivery、Run、RuntimeContext 状态正确。
- [ ] stdout/stderr 按真实接收顺序保存。
- [ ] 超时和取消前输出完整保留。
- [ ] 取消实际终止整个进程树。
- [ ] Gateway/Worker 重启后不重复执行。
- [ ] Codex 和 Claude Code 新任务与 Continue 真实通过。
- [ ] TG 和 Mobile 可以创建、查看、继续、取消、重试。
- [ ] TG 与 Mobile 状态一致。
- [ ] session 与 oneshot 包无相互依赖。
- [ ] One-shot 表不引用 Session 业务表。
- [ ] 关闭 One-shot 后现有 OpenDray 正常。
- [ ] 完整 Go/Web/Mobile/i18n 门禁 PASS。
- [ ] 所有任务独立 Commit、Push、报告完成。
- [ ] Git 工作区干净。

# 15. 首次执行入口

完成分支创建后，只执行 `OD-OS-00`。

不要在同一轮开始 `OD-OS-01` 之外的实现工作。每个任务 PASS、Commit、Push、更新状态后，再进入下一任务。
