# Agent Compose 企业 Runtime Execution 能力研究

状态：Issue [#11](https://github.com/Vt00ls/SlideSmith/issues/11) 的研究事实权威与后续架构输入

下游状态：Issue [#24](https://github.com/Vt00ls/SlideSmith/issues/24) 已据此确定 [Runtime Execution architecture contract](./runtime-execution.md)：生产 Agent/Tool 执行按 hostile execution 对待，具体 driver/host 配置必须通过威胁建模与加固验收；Agent Compose 保持可替换 adapter，不获得业务、workspace 或 lease authority。

研究日期：2026-07-22

上游基线：稳定 release [`v2607.10.0`](https://github.com/chaitin/agent-compose/releases/tag/v2607.10.0)，tag commit [`e14c4dbd5e3b0dec6178073902d67d2765390427`](https://github.com/chaitin/agent-compose/commit/e14c4dbd5e3b0dec6178073902d67d2765390427)

## 范围和结论

本文只建立 Agent Compose 作为 SlideSmith `Runtime Execution` 下游 adapter 的事实边界，不重新设计 [#16](https://github.com/Vt00ls/SlideSmith/issues/16) 已确定的 joint Recovery Point，也不重新设计 [#19](https://github.com/Vt00ls/SlideSmith/issues/19) 已确定的 `Task Orchestration` command/decision seam。

结论如下：

1. Agent Compose v2607.10.0 已提供可自动化的 v2 Connect/HTTP 契约：异步开始、同步/流式运行、查询、列表、日志跟随、取消、run events、sandbox 生命周期和 stats；`Run` 的公开终态是 `succeeded`、`failed`、`canceled`。[官方 v2 proto](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/proto/agentcompose/v2/agentcompose.proto#L28-L87)
2. 这些能力足以成为一个**节点内执行 adapter**，但不足以直接承担 SlideSmith 的企业业务权威：官方安全策略把项目称为 experimental/public preview，当前 daemon 认证只是一个授予完整控制面的共享 Bearer token，没有用户身份、RBAC 或细粒度授权。[SECURITY.md](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/SECURITY.md)；[daemon Bearer auth](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/docs/design/daemon_bearer_auth.md)
3. 隔离强度依赖 driver 和宿主部署。官方明确要求不要在没有独立威胁模型和加固评审时把任一 driver 当作可运行 hostile code；guest 当前必须以 root 运行，而 Codex/Claude/Gemini/OpenCode adapter 都采用绕过工具权限、允许网络的执行方式。[SECURITY.md](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/SECURITY.md#runtime-isolation)；[Guest Image ABI](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/docs/pages/guest-image-abi.md#31-image-and-user)；[runtime contract](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/docs/design/agent-compose-runtime_contract.md#10-provider-adapter-behavior)
4. 当前 release 没有每个 run 的 CPU、memory、GPU、网络策略或 resource class 输入。`stats` 是观测快照，不是资源准入；YAML `network.mode` 只接受 `default`，Docker sandbox 的 HostConfig 未设置 CPU/memory/pids limits。[YAML manual](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/docs/pages/agent-compose-yaml-manual.md#network)；[Docker HostConfig source](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/pkg/driver/docker_runtime.go#L784-L792)；[stats contract](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/docs/pages/command-line-manual.md#stats-show-sandbox-resource-stats)
5. 当前 SlideSmith adapter 与 v2607.10.0 存在明确 contract drift：旧式顶层 `workspace`、`session_id`、`/sessions/<id>/workspace` 都不符合 release contract；部署又使用可变 `latest`，实际运行 digest/版本无法从仓库证明。[当前 runtime YAML](https://github.com/Vt00ls/SlideSmith/blob/0eacf6ef7625e8e92a936dfbc4c7a02d9f569ca7/runtime/ppt-master-agent/agent-compose.yml)；[当前 adapter](https://github.com/Vt00ls/SlideSmith/blob/0eacf6ef7625e8e92a936dfbc4c7a02d9f569ca7/backend/internal/service/agent_compose.go#L17-L41)；[当前部署](https://github.com/Vt00ls/SlideSmith/blob/0eacf6ef7625e8e92a936dfbc4c7a02d9f569ca7/deploy/docker-compose.yml#L155-L169)
6. 推荐的后续架构输入是：SlideSmith 保持 Task、Phase Run、Runtime Run、Sandbox Lease、authorization、quota、commit 和 audit 权威；以 pinned v2 API adapter 提交稳定 operation identity，补齐 payload binding、fencing、timeout/cancel reconciliation、node/capacity policy 和证据封装。不得让 Agent Compose 的 project/run/session/workspace path 成为 SlideSmith 的业务或恢复权威。

## 证据纪律和部署状态

本文将证据分成三类：

- **Release contract**：以 `v2607.10.0` 为版本基线，官方 manual、proto 和源码引用全部固定到该 release 的 tag commit `e14c4dbd`。
- **SlideSmith repository observation**：只说明 commit `0eacf6e` 中的 adapter、YAML 和 Compose 声明；这不证明实际运行容器的状态。
- **Deployment observation**：2026-07-22 对当前环境执行只读 probe。`agent-compose` 不在 host `PATH`，Docker daemon socket 不可连接，因此无法读取正在运行 daemon 的 `version --json`、`status --json`、image digest、driver health 或 auth state。没有运行 `up`、`run` 或任何创建 workload 的 probe。

因此，本文可以确认“仓库默认会拉取 `ghcr.io/chaitin/agent-compose:latest`”，不能确认“某个现场当前正在运行 v2607.10.0”。`latest` 是可变 tag，仓库也没有记录 daemon/guest digest。[SlideSmith Compose](https://github.com/Vt00ls/SlideSmith/blob/0eacf6ef7625e8e92a936dfbc4c7a02d9f569ca7/deploy/docker-compose.yml#L155-L169)

同日 registry 只读观察显示，`ghcr.io/chaitin/agent-compose:latest` 与 `:v2607.10.0` 当时均解析为 multi-arch index digest `sha256:3117226250256b03d25815bd1bb584ade5ac225b83f55d527be122b8b75a61e8`；amd64 manifest label 报告 version `v2607.10.0` 和 revision `e14c4dbd5e3b0dec6178073902d67d2765390427`。[官方 GHCR package](https://github.com/chaitin/agent-compose/pkgs/container/agent-compose) 这只能证明 2026-07-22 的 registry tag 状态，不能反推任何已部署容器的 digest。

## 正式执行契约

### Run create、inspect、cancel 和终态

| 问题 | v2607.10.0 事实 | SlideSmith 约束 |
| --- | --- | --- |
| create/start | `RunService` 提供 `RunAgent`、`StartRun`、`RunAgentStream` 和双向 `RunAttach`；CLI `run -d` 用 `StartRun` 提交后返回 run id。[proto](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/proto/agentcompose/v2/agentcompose.proto#L28-L43)；[CLI manual](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/docs/pages/command-line-manual.md#run-run-a-sandbox) | adapter 可以异步提交；SlideSmith `Runtime Run` identity 必须先于调用 durable，不能用 Agent Compose run id 替代。 |
| inspect/list | `GetRun`、`ListRuns` 和 CLI `inspect run <run-id>` 返回 run detail；`RunSummary` 包含 project revision、agent、status、exit/error、timestamps、duration 和 sandbox id。[proto](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/proto/agentcompose/v2/agentcompose.proto#L864-L894)；[run detail](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/proto/agentcompose/v2/agentcompose.proto#L1092-L1131) | adapter 应持久保存 opaque run/sandbox identity 和原始 evidence envelope，不应解析 workspace host path。 |
| terminal state | 正式枚举是 `pending -> running -> succeeded|failed|canceled`；源码禁止从 terminal 转为另一状态。[proto enum](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/proto/agentcompose/v2/agentcompose.proto#L139-L146)；[transition source](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/pkg/runs/coordinator.go#L276-L307) | Agent Compose terminal 只说明 runtime process 视角；Phase outcome 仍由 SlideSmith validator/Task Orchestration 决定。 |
| cancel | `StopRun` 对 active in-process run 取消 context 并将 record 标为 `canceled`；对已经 terminal 的 run 返回 `stop_requested=false`。[StopRun source](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/pkg/agentcompose/api/run_handler.go#L486-L540)；[supervisor source](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/pkg/agentcompose/app/run_supervisor.go#L48-L92) | `StopRunResponse.stop_requested` 不能单独作为进程已停止、bytes 已隔离或 C04 不会 commit 的证明；adapter 必须继续 inspect sandbox/run，并由 SlideSmith fence late evidence。 |
| daemon restart | daemon 启动时把先前仍为 `pending`/`running` 的 project run 统一标为 `failed`，错误为 `daemon interrupted...`，不恢复同一 in-flight run。[startup reconciliation](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/pkg/agentcompose/app/background.go#L100-L153) | node/daemon crash 后不得把原 run 当作可透明续跑；重试创建新的 SlideSmith `Runtime Run`，符合 Phase Run 0..N Runtime Runs 的 standing model。 |

### Timeout

`RunAgentRequest` 没有 per-run timeout 字段。[proto request](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/proto/agentcompose/v2/agentcompose.proto#L782-L801) Agent prompt 受 daemon-wide `AGENT_TIMEOUT` 控制，默认 10 小时；源码在 AgentExecutor 内用该值创建 context deadline。[config](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/pkg/config/config.go#L15-L20)；[AgentExecutor](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/pkg/agentcompose/adapters/agent_executor.go#L32-L57) Command 路径也没有由 `RunAgentRequest` 传入的 run timeout。

SlideSmith 当前的 `SLIDESMITH_AGENT_COMPOSE_TIMEOUT` 默认 30 分钟只包围本地 CLI 进程和 detach poll；context 到期时 adapter 直接返回，未调用 `agent-compose stop`/`StopRun`。[adapter timeout](https://github.com/Vt00ls/SlideSmith/blob/0eacf6ef7625e8e92a936dfbc4c7a02d9f569ca7/backend/internal/service/agent_compose.go#L115-L203)；[timeout helper](https://github.com/Vt00ls/SlideSmith/blob/0eacf6ef7625e8e92a936dfbc4c7a02d9f569ca7/backend/internal/service/agent_compose.go#L337-L343) 这会产生“SlideSmith 已超时但 daemon 最长仍可继续运行”的 orphan/late-result 风险。

后续 adapter 必须把 Platform deadline 作为自己的权威 timer：到期先 fence SlideSmith Runtime Run，再调用 `StopRun`，随后 reconcile run terminal、sandbox stop/remove 和 C04 discard/commit evidence。不能把 CLI context cancellation等同于下游终止完成。

### Idempotency

v2 `RunAgentRequest.client_request_id` 是可用字段；Coordinator 会用 `(project_id, agent_name, source, client_request_id)` 生成稳定 run id。[proto](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/proto/agentcompose/v2/agentcompose.proto#L782-L801)；[Coordinator](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/pkg/runs/coordinator.go#L96-L161) 但有两个企业 seam 缺口：

1. CLI 没有让 caller 指定 stable request id；它把 project、agent、input 和当前 `RFC3339Nano` 时间拼成 id。因此同一 SlideSmith operation 的 CLI retry 必然生成新的 Agent Compose run。[CLI source](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/cmd/agent-compose/cli_run_command.go#L693-L696)；[request construction](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/cmd/agent-compose/cli_run_command.go#L330-L355)
2. 同一稳定 run id 冲突时，SQLite `INSERT ... ON CONFLICT(run_id) DO NOTHING` 返回已有 run，但没有比较新旧 prompt、driver、image、project revision 或其他 canonical request fields。[store source](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/pkg/storage/configstore/project_run_event_transaction.go#L13-L67)

因此 SlideSmith 不应继续把 CLI 作为企业写入 seam。后续 adapter 应直接使用 pinned v2 Connect client，提交由 SlideSmith enactment operation 派生的稳定 `client_request_id`；同时由 SlideSmith 保存 canonical request digest，并在重放前校验 same key/same payload。Agent Compose 的稳定 run id 是第二层去重，不是 payload-bound business idempotency authority。

### 并发和 linearization 限制

run transition 在 Go 层先读当前状态、校验，再执行不带 expected status/revision 的 `UPDATE project_run ... WHERE run_id = ?`。[transition source](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/pkg/runs/coordinator.go#L190-L242)；[update source](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/pkg/storage/configstore/project_run_event_transaction.go#L69-L128) 因此两个并发 terminal writer 都可能基于同一 `running` snapshot 通过校验，数据库层没有 compare-and-set fence。SlideSmith 必须以自己的 Runtime Run revision/fence 判定哪个 evidence 可接受，不能把 Agent Compose record 的最后写入者当成业务 linearization point。

## Sandbox、workspace 和 cleanup ownership

官方模型把 sandbox 定义为一个 agent run context 的 runtime isolation environment；daemon 持有 project state、scheduler、sandbox lifecycle、logs、images 和 API。[CLI concepts](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/docs/pages/command-line-manual.md#core-concepts)

release 的默认数据布局是 `<DATA_ROOT>/sandboxes/<sandbox-id>/`，其中包含 workspace、state、home、runtime、logs、VM state 和 proxy state；`Sandbox` wire shape使用 `sandbox_id` 和 `workspace_path`。[architecture storage model](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/docs/design/agent-compose_design.md#storage-model)；[proto](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/proto/agentcompose/v2/agentcompose.proto#L1008-L1038)

initial workspace provisioning 使用 staging + same-filesystem rename，状态是 `pending|failed|ready`；ready 后 stop/resume 保留已有 edits，删除 sandbox 会删除其 sandbox directory。官方同时明确同 sandbox 的 singleflight 只在进程内有效，不是多个 daemon 共享 `SANDBOX_ROOT` 的 distributed lock。[workspace provisioning](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/docs/design/agent-compose_design.md#workspace-provisioning-and-resume)

cleanup 能力较完整但仍是 Agent Compose 自己的资源域：

- `sandbox stop` 保留 resumable state；`sandbox rm` 使用 durable deletion journal，分阶段删除 driver resource、sandbox accessories、目录和 metadata，daemon restart 只恢复已标记 `deleting` 的 journal。[CLI sandbox lifecycle](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/docs/pages/command-line-manual.md#sandbox-manage-sandboxes)
- `sandbox prune` 默认 dry-run；不完整 ownership、corrupt/path-escaping、active 或 unknown-schema residue 即使 force 也不可删除。[同一官方章节](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/docs/pages/command-line-manual.md#sandbox-manage-sandboxes)
- optional `WORKSPACE_CLEANUP_TTL` 只回收 eligible stopped sandbox 的 workspace，保留 metadata/log/state；默认 0（关闭），不实现 disk-space watermark。[cache/cleanup manual](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/docs/pages/command-line-manual.md#cache-commands)

这些机制可作为 C04 cleanup adapter 的下游证据，但不能成为 SlideSmith Task Workspace authority。SlideSmith 的 Task Workspace Revision、Checkpoint、commit/discard 和 Cleanup Debt 仍由已接受的 C04 seam 管理；Agent Compose workspace 只能是一个 Runtime View，host path 必须保持 opaque。

### 当前 SlideSmith 的 contract drift

1. release 的严格 YAML schema 只有顶层 `workspaces`；顶层 singular `workspace` 被官方 manual 明确列为 invalid，而 SlideSmith 文件仍使用 singular `workspace`。[YAML migration note](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/docs/pages/agent-compose-yaml-manual.md#using-top-level-workspace)；[SlideSmith YAML](https://github.com/Vt00ls/SlideSmith/blob/0eacf6ef7625e8e92a936dfbc4c7a02d9f569ca7/runtime/ppt-master-agent/agent-compose.yml#L1-L14)
2. v2 proto 在 `RunAgentRequest`、`ListRunsRequest` 和 `RunSummary` 中 reserved `session_id`，改用 `sandbox_id`；SlideSmith adapter 仍递归解析 `session_id`，并在缺少 workspace path 时拼出 `/sessions/<session-id>/workspace`。[v2 proto](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/proto/agentcompose/v2/agentcompose.proto#L782-L801)；[RunSummary](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/proto/agentcompose/v2/agentcompose.proto#L1092-L1118)；[SlideSmith parser](https://github.com/Vt00ls/SlideSmith/blob/0eacf6ef7625e8e92a936dfbc4c7a02d9f569ca7/backend/internal/service/agent_compose.go#L345-L359)；[path inference](https://github.com/Vt00ls/SlideSmith/blob/0eacf6ef7625e8e92a936dfbc4c7a02d9f569ca7/backend/internal/service/agent_compose.go#L231-L237)
3. release 的 guest ABI 没有 version label 或 startup handshake，官方要求 custom guest 与每个 daemon release/driver 组合测试，并建议 daemon、runtime 和 guest 使用同 tag/digest。[Guest ABI compatibility](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/docs/pages/guest-image-abi.md#1-compatibility-model) SlideSmith daemon 用 `latest`，guest 也默认 `latest` 或 local `:dev`，不能证明版本匹配。[SlideSmith daemon image](https://github.com/Vt00ls/SlideSmith/blob/0eacf6ef7625e8e92a936dfbc4c7a02d9f569ca7/deploy/docker-compose.yml#L155-L169)；[env example](https://github.com/Vt00ls/SlideSmith/blob/0eacf6ef7625e8e92a936dfbc4c7a02d9f569ca7/deploy/agent-compose.env.example#L9-L14)

结论：在修复这些 drift、增加 release pin 和 contract tests 之前，不能声称当前 SlideSmith adapter 支持 v2607.10.0。

## 权限、隔离和网络

### Daemon control plane authentication

`AGENT_COMPOSE_AUTH_TOKEN` 为空时认证关闭；非空时一个 daemon-wide shared Bearer token 授予完整 control-plane access。它不是 user identity，也不是 RBAC/fine-grained permission boundary；health、runtime LLM facade、Jupyter proxy 和 webhook ingestion 有独立 trust/auth boundary。[official auth design](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/docs/design/daemon_bearer_auth.md)

Bearer token 不提供传输加密。官方允许 HTTP，但跨机部署要求 HTTPS、SSH tunnel、VPN 或等效保护；daemon TCP API 不应直接作为 browser/public entrypoint。[auth transport](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/docs/design/daemon_bearer_auth.md#transport-and-proxy-considerations)；[architecture constraint](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/docs/design/agent-compose_design.md#key-constraints)

SlideSmith 的 `agent-compose.env.example` 仍只写旧 UI `AUTH_USERNAME`/`AUTH_PASSWORD`/`AUTH_SECRET`，没有 `AGENT_COMPOSE_AUTH_TOKEN`；当前 Compose 也没有独立 agent-compose UI service，这些变量不能保护 daemon control plane。[SlideSmith env](https://github.com/Vt00ls/SlideSmith/blob/0eacf6ef7625e8e92a936dfbc4c7a02d9f569ca7/deploy/agent-compose.env.example#L1-L16) 实际部署是否通过外层网络策略获得保护，本次无法验证。

企业适配要求：

- User/administrator/worker authorization 在 SlideSmith `Identity & Ownership` 和 `Runtime Execution` seam 完成；只向 owned adapter 进程授予 daemon credential，不把 shared token暴露给用户或 guest。
- 每次调用绑定 Personal Workspace、Task、Phase Run、Runtime Run、node、operation 和 fence；返回给非 owner 的 error 必须由 SlideSmith sanitize。
- daemon endpoint 仅置于 owned internal network，并由 mTLS/HTTPS 或受控 tunnel 保护；shared token 需要 rotation、secret manager 和 audited access。Agent Compose 自身不能提供 per-principal audit。

### Guest execution and driver isolation

官方 Guest ABI 当前不支持 non-root image，`HOME` 固定 `/root`。Provider adapters 的默认行为包括 Codex `sandboxMode=danger-full-access`、`approvalPolicy=never`、`networkAccessEnabled=true`；Claude、Gemini 和 OpenCode也使用各自的 bypass/yolo/dangerously-skip-permissions 模式。[Guest ABI](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/docs/pages/guest-image-abi.md#31-image-and-user)；[provider behavior](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/docs/design/agent-compose-runtime_contract.md#10-provider-adapter-behavior)

这意味着 agent-level permission prompt 不是安全边界；外层 sandbox driver、mount manifest、network policy、credential scoping 和 immutable image 才是边界。官方自身也明确：Docker、BoxLite、Microsandbox 的 isolation 随 driver/host 而变，必须单独做 hostile-code threat model/hardening review。[SECURITY.md](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/SECURITY.md#runtime-isolation)

当前 YAML driver 事实：

- Docker 是默认 stable driver；BoxLite/Microsandbox只编入 full Linux build，要求 KVM 和 runtime artifacts；`firecracker` 当前 normalization 直接拒绝。[YAML driver manual](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/docs/pages/agent-compose-yaml-manual.md#driver)
- 官方 base Docker Compose 只挂 Docker socket，不要求 privileged；只有启用 BoxLite/Microsandbox 的 KVM overlay 增加 `privileged: true` 和 `/dev/kvm`。[official deployment](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/deploy/README.md#requirements)；[KVM overlay](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/docker-compose.kvm.yml)
- SlideSmith 选择 Docker driver，却让 agent-compose container `privileged: true` 并挂 host Docker socket；标准 Compose 还通过 `x-backend-volumes` 把 Docker socket 与 `/data` 同时挂给 API 和 worker。[SlideSmith driver](https://github.com/Vt00ls/SlideSmith/blob/0eacf6ef7625e8e92a936dfbc4c7a02d9f569ca7/runtime/ppt-master-agent/agent-compose.yml#L7-L14)；[agent-compose service](https://github.com/Vt00ls/SlideSmith/blob/0eacf6ef7625e8e92a936dfbc4c7a02d9f569ca7/deploy/docker-compose.yml#L155-L169)；[backend volumes](https://github.com/Vt00ls/SlideSmith/blob/0eacf6ef7625e8e92a936dfbc4c7a02d9f569ca7/deploy/docker-compose.yml#L51-L56)

`privileged + docker.sock` 会显著扩大任一 daemon/API/worker compromise 的宿主影响面。V1 生产部署必须按选择的 driver 最小化权限：Docker-only 拓扑移除 privileged；只有经 Class C 安全姿态确认并完成威胁模型后才启用 KVM driver overlay。API/worker 不应为调用 remote daemon 获得 Docker socket；当前 docker-exec CLI wrapper 是导致该权限耦合的实现细节。

### Network and resource limits

Agent Compose project `network.mode` 当前只接受 `default`，runtime topology 由 driver/daemon deployment 决定。[YAML network manual](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/docs/pages/agent-compose-yaml-manual.md#network) Docker sandbox container 继承 daemon 自身的 Compose network 或 Docker default network；HostConfig 没有 CPU、memory、pids、GPU 或 read-only-rootfs 设置。[Docker topology source](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/pkg/driver/docker_runtime.go#L784-L834) Microsandbox source 当前创建 `AllowAll` network policy，BoxLite source显式启用 network。[Microsandbox source](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/pkg/driver/microsandbox_runtime.go#L1010-L1024)；[BoxLite source](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/pkg/driver/boxlite_cgo.go#L996-L1008)

`stats` 可报告 CPU percent、memory usage/limit、network、block IO 和 uptime，并用 `unknown`/`unavailable` 表达 driver 不支持的项；它没有 admission、reservation 或 hard limit contract。[CLI stats](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/docs/pages/command-line-manual.md#stats-show-sandbox-resource-stats) `agent-compose.yml` 的 `AgentSpec` 也没有 resource class/CPU/memory/GPU 字段。[compose spec](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/pkg/compose/spec.go#L32-L49)

因此 SlideSmith scheduler/#20 必须拥有 resource class、reservation、node capacity 和 fairness；Execution Node/container runtime 必须拥有 cgroup、pids、ephemeral disk、GPU、egress allowlist/DNS/proxy 和 credential policy。Agent Compose stats 只能作为 telemetry/evidence，不得作为 capacity authority。

## 治理、审计和可观测证据

### 已有证据

v2 run record 可以提供：

- run/project/revision/agent/source/status/sandbox、exit/error、start/complete/duration；
- prompt、output、result JSON、logs path、artifacts dir、cleanup error、driver、image ref；
- structured run events（sequence、kind、text/name/payload、success、exit code、stop reason、created time）；
- follow logs 的 offset/final/status；
- sandbox stats snapshot。[v2 proto](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/proto/agentcompose/v2/agentcompose.proto#L864-L1131)

CLI manual 说明 `logs` 来自 run log artifacts，不自动读取 Codex/Claude/Gemini 等 provider 私有日志；`exec` 不创建 `ProjectRun`，只有 `run --command` 才产生 run audit/log/artifacts。[logs](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/docs/pages/command-line-manual.md#logs-show-logs)；[exec](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/docs/pages/command-line-manual.md#exec-execute-in-a-sandbox)

Agent result stdout contract只有 provider、threadId、stopReason、finalText、transcript、stderr；公开 `RunDetail` 也没有 provider request id、token usage、cost、SlideSmith Task/Phase/Runtime identity、input digest、Runtime Release digest、Sandbox Lease fence 或 resource receipt。[runtime result contract](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/docs/design/agent-compose-runtime_contract.md#64-stdout-structured-result)；[RunDetail proto](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/proto/agentcompose/v2/agentcompose.proto#L1092-L1131) Provider usage/receipt 的更细事实仍属于 [#14](https://github.com/Vt00ls/SlideSmith/issues/14)，本文不重复其调查。

### SlideSmith 必须补足的 evidence envelope

Runtime Execution adapter 至少持久记录：

- SlideSmith Runtime Run、Phase Run、Task、Personal Workspace、operation、idempotency key/request digest；
- exact Pipeline/Runtime Release/guest image/skill/resource bundle digests；
- execution node、daemon version/commit、compiled driver、selected driver、sandbox id、Agent Compose run id；
- immutable input manifest digest、Sandbox Lease/fence、deadline/cancel reason；
- Agent Compose raw terminal detail、ordered events/log offsets、exit/error/cleanup error、stats sample；
- adapter normalization version、validation result、C04 commit/discard evidence 和 late/stale rejection reason。

这些记录进入 SlideSmith append-only audit/observability projection；Agent Compose SQLite/log/path 不能成为第二个业务 audit authority。敏感 prompt/output/provider logs 必须按 Personal Workspace ownership、break-glass 和 retention policy保护，不能无差别复制到 metrics/log labels。

## 可靠性和多 execution node 约束

### 单 daemon、节点本地模型

官方 daemon 使用 `DATA_ROOT/data.db` 和节点本地 sandbox directory；active detached run 由进程内 `RunSupervisor` map 持有，daemon restart 后 active run 被标为 failed。[storage model](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/docs/design/agent-compose_design.md#storage-model)；[RunSupervisor](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/pkg/agentcompose/app/run_supervisor.go) workspace provisioning 明确没有多个 daemon 共享 root 的 distributed lock。[workspace provisioning](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/docs/design/agent-compose_design.md#workspace-provisioning-and-resume)

官方 `--host` 支持连接 remote daemon，但这是选择一个 endpoint，不是 cluster control plane；`compiled_drivers` 只报告 build capability，不探测 Docker daemon、KVM、runtime artifact、image access 或 driver health。[CLI remote host](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/docs/pages/command-line-manual.md#command-format)；[compiled capability](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/docs/design/agent-compose_design.md#compiled-driver-capability)

因此企业 V1 的可接受拓扑是“SlideSmith scheduler 选择 owned Execution Node；每个节点有独立 Agent Compose daemon/data root；Runtime Execution adapter 保存 node + run mapping”。禁止多个 daemon 共享一个 `DATA_ROOT`/`SANDBOX_ROOT`。节点丢失时，SlideSmith 从 durable Task Workspace/objects 重建 Runtime View并创建新 Runtime Run，不尝试 live migration 或恢复 Agent Compose in-flight session。

### Health and capability discovery

`agent-compose --json version` 的稳定字段为 `version`、`os`、`arch`、`compiled_drivers`；`/api/version` 增加同样 build fields。官方同时警告这不证明 runtime 健康。[compiled capability](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/docs/design/agent-compose_design.md#compiled-driver-capability) `status --json` 只返回 daemon status/version envelope。[CLI status](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/docs/pages/command-line-manual.md#status-query-daemon-status)

Runtime Execution readiness 应分层：daemon health、expected pinned version/commit、required compiled driver、driver-specific non-destructive readiness、pinned image digest available、data-root capacity/watermark、secret/LLM gateway availability。仅 health/status 成功不足以接纳 Runtime Run。

### Upgrade and compatibility

Guest ABI 官方明确没有 ABI version label或 compatibility handshake；runtime CLI/stdout protocol 是 internal release boundary，不保证任意 release 之间兼容。最安全做法是 daemon、guest 和 runtime 使用同 immutable tag/digest，并对每个 release/driver 组合测试。[Guest ABI](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/docs/pages/guest-image-abi.md#1-compatibility-model)

上游 release 目前是 preview，官方没有文档化的 versioned security-support window；SECURITY.md 说 security fixes 预计先落 main，直到 versioned release support 被文档化。[SECURITY supported versions](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/SECURITY.md#supported-versions) V1 必须建立内部 approved version catalog、digest pin、upgrade contract suite、rollback和 vulnerability intake，不能自动跟随 `latest`。

## 部署约束

| 约束 | 官方事实 | SlideSmith 输入 |
| --- | --- | --- |
| 平台 | 官方 installer/deployment 支持 Linux `amd64`/`arm64` multi-arch images；要求 Docker Engine + Compose v2。Release 不发布 standalone daemon binaries，只发布 installer assets。[deploy README](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/deploy/README.md) | 企业镜像镜像化、镜像仓库 mirror/scan/signature 和离线安装必须纳入 release pipeline。 |
| KVM | Docker driver 不要求 `/dev/kvm`；BoxLite/Microsandbox 要求 KVM、full Linux artifacts，并使用显式 privileged KVM overlay。[deploy README](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/deploy/README.md#requirements) | 没有威胁模型、host capability 和运维基线前，不能仅因“更隔离”切换 driver。 |
| 数据 | daemon data root包含 SQLite、sandboxes、logs/state、image/materialization cache；自动 cleanup 不等于 disk watermark。[storage model](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/docs/design/agent-compose_design.md#storage-model)；[cleanup](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/docs/pages/command-line-manual.md#cache-commands) | 每节点需要 bytes/inodes watermark、admission fence、cleanup debt和重新建模；业务恢复不备份 Agent Compose execution residue。 |
| 网络/TLS | daemon默认监听 `0.0.0.0:7410`，官方要求把它当 internal API；Bearer 不加密。[SECURITY deployment](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/SECURITY.md#deployment-guidance)；[auth transport](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/docs/design/daemon_bearer_auth.md#transport-and-proxy-considerations) | 只暴露到 owned internal network；外层终止 TLS/mTLS，并限制 source identities。 |
| license | 官方仓库 license 是 GNU AGPLv3。[LICENSE](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/LICENSE.txt) | 在企业内网部署、修改或与产品分发前完成法务/开源合规评审；本文不解释具体法律义务。 |

## 对 SlideSmith Runtime Execution 的稳定架构输入

以下输入供 [#24](https://github.com/Vt00ls/SlideSmith/issues/24) 使用，不替代该 ticket 的正式 resolution：

1. **边界**：Agent Compose 是 replaceable execution adapter，不是 deep module。SlideSmith `Runtime Execution` 拥有 Runtime Run process lifecycle、node/lease/fence、deadline/cancel reconciliation、typed evidence；Task Orchestration只接受 Runtime Execution 验证后的 evidence。
2. **接口**：企业写入路径使用 pinned v2 Connect client，不 shell out CLI。CLI 可保留为人工诊断工具，但不能承担稳定 idempotency、typed error、auth identity、timeout/cancel 或 capability negotiation。
3. **身份**：调用前 durable 生成 SlideSmith operation/Runtime Run id；把稳定 operation id映射到 `client_request_id`，保存 canonical request digest。保存 `(execution_node, agent_compose_run_id, sandbox_id)`，但都保持 opaque。
4. **输入绑定**：每个 request 绑定 exact Task/Phase/Runtime identity、immutable input manifest、Pipeline/Runtime Release、guest digest、capability/resource class 和 Sandbox Lease fence。Agent Compose project revision/image ref 只能补充，不能替代这些 bindings。
5. **超时和取消**：Platform timer 先 fence，再 `StopRun`，最后 reconcile terminal + sandbox + C04。`canceled` record 不是 commit/discard proof；late success/failure 不能越过 fence。
6. **crash/retry**：daemon/node interruption使当前 Runtime Run failed/unknown；同一 Phase Run可创建新的 Runtime Run，禁止从 session/path推断恢复。ack loss 重放同一 stable request identity；payload mismatch fail closed。
7. **隔离**：默认 Docker driver也必须有 outer cgroup/pids/disk/egress/credential controls；guest root和 provider permission bypass要求 sandbox是唯一安全边界。driver选择和 hostile-code posture 属于明确的安全决策。
8. **多节点**：每节点独立 daemon/root；scheduler owns placement/capacity。禁止 shared Agent Compose root；节点恢复从 SlideSmith durable object/C04 materialize，而非 Agent Compose session replication。
9. **证据**：将 raw Agent Compose detail/events/log offsets/stats封装进 SlideSmith typed evidence并签入 audit projection；provider usage/request receipt缺口由 #14/#12 处理。
10. **版本**：daemon/guest/runtime全部按 approved digest pin；admission 比对 version/compiled driver和实际 readiness；升级必须跑 adapter contract、failure injection、cancel race、restart和cleanup tests。

## 风险登记

| 优先级 | 风险 | 证据 | 必需控制 |
| --- | --- | --- | --- |
| Critical | 当前 `latest` + 旧 YAML/session path 可能在升级后直接不兼容或写错 workspace。 | [YAML manual](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/docs/pages/agent-compose-yaml-manual.md#using-top-level-workspace)；[v2 proto](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/proto/agentcompose/v2/agentcompose.proto#L782-L801)；[SlideSmith adapter](https://github.com/Vt00ls/SlideSmith/blob/0eacf6ef7625e8e92a936dfbc4c7a02d9f569ca7/backend/internal/service/agent_compose.go) | pin release/digests；replace CLI/path contract；release contract tests。 |
| Critical | `privileged + docker.sock` 及 API/worker socket sharing扩大 host compromise blast radius。 | [official Docker-only deployment](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/deploy/README.md#requirements)；[SlideSmith Compose](https://github.com/Vt00ls/SlideSmith/blob/0eacf6ef7625e8e92a936dfbc4c7a02d9f569ca7/deploy/docker-compose.yml#L51-L56) | Docker-only 去 privileged；remote API adapter；移除 API/worker Docker socket。 |
| High | daemon auth 默认关闭，当前 env 的 UI `AUTH_*` 不保护 daemon；shared token也不是 per-user auth。 | [official auth](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/docs/design/daemon_bearer_auth.md)；[SlideSmith env](https://github.com/Vt00ls/SlideSmith/blob/0eacf6ef7625e8e92a936dfbc4c7a02d9f569ca7/deploy/agent-compose.env.example) | owned network + TLS/mTLS + daemon token；SlideSmith ownership authorization/audit。 |
| High | SlideSmith 30m timeout不 StopRun；daemon prompt默认可运行10h。 | [SlideSmith timeout](https://github.com/Vt00ls/SlideSmith/blob/0eacf6ef7625e8e92a936dfbc4c7a02d9f569ca7/backend/internal/service/agent_compose.go#L115-L203)；[Agent timeout](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/pkg/agentcompose/adapters/agent_executor.go#L45-L57) | platform timer、fence、StopRun、terminal/sandbox reconciliation。 |
| High | CLI retry无法提供稳定 idempotency；API same-key mismatch未比较 payload。 | [CLI id](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/cmd/agent-compose/cli_run_command.go#L693-L696)；[store conflict](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/pkg/storage/configstore/project_run_event_transaction.go#L13-L67) | direct v2 adapter + canonical request digest + conflict fail closed。 |
| High | run transition缺少数据库 CAS fence；并发 cancel/success可能最后写入覆盖。 | [Coordinator](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/pkg/runs/coordinator.go#L190-L242)；[SQL update](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/pkg/storage/configstore/project_run_event_transaction.go#L69-L128) | SlideSmith revision/fence 决定 evidence acceptance；测试 cancel/late-result race。 |
| High | guest root + provider permission bypass + allow-all/default network意味着 Agent Compose 内部 prompt permission 不是安全边界。 | [Guest ABI](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/docs/pages/guest-image-abi.md#31-image-and-user)；[provider behavior](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/docs/design/agent-compose-runtime_contract.md#10-provider-adapter-behavior) | outer sandbox/cgroup/egress/secret/mount hardening；driver threat model。 |
| High | preview security support未承诺 versioned window。 | [SECURITY.md](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/SECURITY.md#supported-versions) | internal version catalog、vuln monitoring、fast rollback和vendor exit seam。 |
| Medium | restart统一 fail active run，没有透明恢复；多个 daemon不能共享 root。 | [startup reconciliation](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/pkg/agentcompose/app/background.go#L100-L153)；[workspace concurrency](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/docs/design/agent-compose_design.md#workspace-provisioning-and-resume) | node-local daemon；new Runtime Run retry；durable C04 re-materialization。 |
| Medium | stats 无 admission/limits；network只支持 default。 | [stats](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/docs/pages/command-line-manual.md#stats-show-sandbox-resource-stats)；[network](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/docs/pages/agent-compose-yaml-manual.md#network) | #20 scheduler/capacity authority和宿主 resource enforcement。 |
| Medium | run evidence缺少 provider request/usage receipt和 SlideSmith bindings。 | [result contract](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/docs/design/agent-compose-runtime_contract.md#64-stdout-structured-result)；[RunDetail](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/proto/agentcompose/v2/agentcompose.proto#L1119-L1131) | adapter evidence envelope；#14/#12 补 usage receipt。 |
| Medium | AGPLv3 对企业采用构成合规输入。 | [LICENSE](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/LICENSE.txt) | 产品采用前法务/开源合规批准。 |

## 未决问题

以下问题没有可靠一手事实可在本研究中自动关闭；它们是后续 decision/spec 输入，不阻塞本文的事实结论：

1. 企业 V1 对 hostile prompt/code 的威胁等级，以及 Docker、BoxLite、Microsandbox 中哪个 driver/host hardening满足该等级。这改变安全姿态，属于 #24 的明确决策。
2. 最终 execution-node resource class、CPU/memory/pids/ephemeral-disk/GPU、egress和并发值；Agent Compose 不提供这些 business inputs，属于 #20。
3. provider request id、token usage、cost 和可信 receipt 的实际 provider/Agent Compose 可用度；由 #14/#12 决定。
4. 现场实际 daemon/guest image digest、version、compiled driver、auth/TLS状态和 KVM/driver health。当前环境无可连接 daemon；上线前必须用只读 inventory补证。
5. v2 Connect API 的长期 compatibility/support承诺。官方称 v1 为 stable compatibility API、v2 为 primary project/run path，但 preview policy没有 versioned support window；需要内部 pin + contract suite而非推定兼容。[API boundaries](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/docs/design/agent-compose_design.md#api-boundaries)；[SECURITY.md](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/SECURITY.md#supported-versions)
6. Agent Compose AGPLv3 在 SlideSmith 的具体部署、修改、分发方式下的义务；只能由法务/开源合规确认。

没有发现需要改写 #16 或 #19 resolution 的事实。研究新增的 fog 都有现有下游归属：Runtime seam/driver/security 归 #24，capacity归 #20，usage归 #14/#12，observability归 #13，release pin/compatibility归 #22。

影响 #11 研究完成的 remaining fog：none。
