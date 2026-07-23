# LLM provider 与 Agent Compose usage evidence 事实研究

状态：Issue [#14](https://github.com/Vt00ls/SlideSmith/issues/14) 的一手资料研究输入

研究日期：2026-07-23

下游：Issue [#12](https://github.com/Vt00ls/SlideSmith/issues/12) 的 Gateway、usage receipt、Usage Ledger 与 Quota Reservation 架构决策

## 结论

1. 当前仓库只证明 PPT Master 使用 Agent Compose 的 `codex` agent，并提供了 OpenAI Responses 端点的配置示例；没有选定生产 LLM provider。测试服务器曾使用一个第三方 OpenAI-compatible endpoint，但不能由 OpenAI 官方契约推定其真实上游、计量、幂等或保留行为。AI image 默认关闭且 provider allowlist 为空，因此也没有已选 image provider。
2. 对 OpenAI Responses API，最强的逐请求 usage evidence 是原生终态 Response：`response.id`、返回的 `model`、provider 时间、顶层 `usage`，再配合创建请求 HTTP 响应的 `x-request-id`。流式请求必须观察到终态事件；在终态前断流时，已消费量不可由 partial deltas 推导。
3. `X-Client-Request-Id` 只是调用方提供的诊断关联。官方要求每个 request 唯一，但没有把它定义为幂等键。当前研究也没有在 Responses/Image create 的正式契约中找到通用幂等保证；超时、重试或断流后的新 create 必须按可能发生另一笔消费处理。
4. OpenAI Organization Usage API 是按分钟或更粗粒度聚合的 reconciliation evidence，没有 provider response/request ID。共享 project、API key 或 model 时，它不能把差额唯一归到某个 SlideSmith Runtime Run。
5. Agent Compose v2607.10.0 不保存原始 provider usage receipt。LLM facade 在同协议路径能透传响应 header/body，在跨协议路径会 decode/re-encode；Facade Token 可以在运行期把 sandbox/provider/model/run 关联起来，但 token 会被删除或过期，且没有 request/usage 表。LLMService 和公开 Run contracts 没有 usage 字段。
6. Agent Compose 的 Codex adapter 使用固定的 `@openai/codex-sdk` 0.144.1。该 SDK 在 `turn.completed` 正式给出 input、cached input、output、reasoning output tokens，但 adapter 忽略该事件并从 `AgentResult` 丢弃 usage。因此 Agent Compose 输出不能成为 Usage Ledger 的权威 receipt。
7. #12 已获得足以作架构决策的事实输入，可以正式解除 research blocker；它必须设计为 partial/late/unknown-aware，而不能要求所有调用都产生完整、同步、exact-once 的 provider receipt。本文仍有 provider 选型和正式 SLA 等 remaining unknowns，但这些应成为 #12 的显式配置、验收或 reconciliation 边界，不需要继续阻塞 #14。

## 范围、版本与证据强度

本研究没有发送真实 provider 请求，没有读取凭据，只使用官方文档、正式 API 契约、固定版本官方源码和仓库只读证据。

| 范围 | 固定点 | 说明 |
| --- | --- | --- |
| OpenAI API | 官方 OpenAPI `2.3.0`，[`openai/openai-openapi` commit `f9400172`](https://github.com/openai/openai-openapi/blob/f9400172ebe08522ab228b771d885e3bd5456e42/openapi.yaml)，`openapi.yaml` blob `2dce9f6e7cbf217f5593b2f15a564b920b5c7f06`，2026-07-23 读取；REST version header 文档当前值 `2020-10-01` | OpenAI 是仓库示例中的直接候选。OpenAI 事实只适用于 OpenAI 官方 API，不自动适用于 compatible gateway。 |
| Agent Compose | `v2607.10.0`, commit [`e14c4dbd5e3b0dec6178073902d67d2765390427`](https://github.com/chaitin/agent-compose/tree/e14c4dbd5e3b0dec6178073902d67d2765390427) | 这是已接受的 architecture research baseline；当前 Compose 文件仍使用 mutable `latest`，实际部署 digest/version 未证明。 |
| Agent Compose Codex dependency | `@openai/codex-sdk` `0.144.1`; Codex tag `rust-v0.144.1`, commit [`44918ea10c0f99151c6710411b4322c2f5c96bea`](https://github.com/openai/codex/tree/44918ea10c0f99151c6710411b4322c2f5c96bea) | Agent Compose lockfile 固定 0.144.1；本文不把更晚版本行为倒推到该版本。 |
| SlideSmith implementation | commit [`0199fc9254ad6fbfcaa840ad2a4969052d042bd9`](https://github.com/Vt00ls/SlideSmith/tree/0199fc9254ad6fbfcaa840ad2a4969052d042bd9) | 只读检查 gateway/ledger/adapter/config gaps。 |

证据强度：

- **High**：官方 OpenAPI/schema，或固定 tag/commit 的正式源码和 public contract。
- **Medium**：官方 narrative guide、示例，或仓库当前配置/实现；事实可靠但未必是跨版本兼容承诺。
- **Low**：有边界的 negative source scan 或由缺失字段作出的结论；只用于说明“当前未发现/未提供”，不推定未来行为。

## 当前 provider 与协议边界

| 仓库证据 | 可证明事实 | 不可推定事实 | 强度 |
| --- | --- | --- | --- |
| [`runtime/ppt-master-agent/agent-compose.yml`](https://github.com/Vt00ls/SlideSmith/blob/0199fc9254ad6fbfcaa840ad2a4969052d042bd9/runtime/ppt-master-agent/agent-compose.yml#L7-L14) | 当前 agent provider 是 `codex`。 | Codex 最终连接的生产 provider、account/project、model snapshot。 | Medium |
| [`deploy/agent-compose.env.example`](https://github.com/Vt00ls/SlideSmith/blob/0199fc9254ad6fbfcaa840ad2a4969052d042bd9/deploy/agent-compose.env.example#L18-L25) | 示例候选是 `https://api.openai.com` + `responses`；model 未填写。 | 生产已选 OpenAI，或已固定 model/version。 | Medium |
| [`docs/runtime-smoke.md`](https://github.com/Vt00ls/SlideSmith/blob/0199fc9254ad6fbfcaa840ad2a4969052d042bd9/docs/runtime-smoke.md#L165-L185) | 测试服务器记录过第三方 endpoint、`responses` protocol 和 `gpt-5.5` 字符串。 | endpoint 的真实上游是 OpenAI；它遵守 OpenAI usage、request ID、retention、retry 或计费契约。 | Medium |
| [`deploy/docker-compose.yml`](https://github.com/Vt00ls/SlideSmith/blob/0199fc9254ad6fbfcaa840ad2a4969052d042bd9/deploy/docker-compose.yml#L32-L40) / [prebuilt](https://github.com/Vt00ls/SlideSmith/blob/0199fc9254ad6fbfcaa840ad2a4969052d042bd9/deploy/docker-compose.prebuilt.yml#L30-L38) | AI image 默认 disabled，allowed AI providers 默认空。 | `openai`、`azure-openai` 或其他 image provider 已选。 | High |
| [`deploy/docker-compose.yml`](https://github.com/Vt00ls/SlideSmith/blob/0199fc9254ad6fbfcaa840ad2a4969052d042bd9/deploy/docker-compose.yml#L155-L170) | Agent Compose image 默认 `latest`。 | 现场运行的是 v2607.10.0 或任一可复现 digest。 | High |

因此下表中的 OpenAI Image、Batch 和 Organization Usage 都是“若 #12/后续选用 OpenAI 官方 API”的条件事实，不是当前生产选型声明。

## Usage fact matrix

| Surface / dimension | 原生可得 facts | identity、model 与时间 | 缺口与失败边界 | 来源 / 强度 |
| --- | --- | --- | --- | --- |
| OpenAI Responses，非流式 text/multimodal | `input_tokens`；`input_tokens_details.cached_tokens`、`cache_write_tokens`；`output_tokens`；`output_tokens_details.reasoning_tokens`；`total_tokens`。 | body 有 `id`、`created_at`、完成时的 `completed_at`、`status`、返回的 `model`；HTTP header 有 server-generated `x-request-id`。请求 alias 与返回 snapshot 可能不同，必须同时保留 requested/returned model。 | usage 没有货币 cost 或单价；没有 per-tool-call token split。只有实际收到含 usage 的 Response 才是逐请求事实。 | [Responses OpenAPI](https://api.openai.com/v1/responses)、[debugging request IDs](https://developers.openai.com/api/reference/overview#debugging-requests)；High |
| OpenAI Responses，streaming | 终态 `response.completed` 携带完整 Response 和 usage；事件有 `sequence_number`。 | 创建响应仍有 Response ID；创建 POST 的 `x-request-id` 与 Response ID 是不同身份。 | partial text/tool events不是 usage receipt。终态前断流时 inline usage unknown；普通同步 stream 无恢复保证。 | [streaming guide](https://developers.openai.com/api/docs/guides/streaming-responses)、[response.completed](https://developers.openai.com/api/reference/resources/responses/streaming-events#response.completed)；High |
| Responses background + stream | 可轮询终态；以 `background=true, stream=true` 创建时可用 `sequence_number`/`starting_after` 恢复 stream；重复 cancel 是幂等的。 | 已取得 Response ID 后可继续关联同一 provider object。 | `store=false`/ZDR 下 background 仍为异步执行临时落盘；cancelled/failed/incomplete Response 的 usage 只有在返回对象实际非空时才可用，官方没有承诺每个失败都给 usage。 | [background mode](https://developers.openai.com/api/docs/guides/background)、[Your data](https://developers.openai.com/api/docs/guides/your-data)；High/Medium |
| Tool calls | function/built-in tool item 有 item ID、call ID/type/status；该 Response 的顶层 token usage仍可用。 | 可把 tool event 关联到 Response ID。 | token usage 是整个 Response aggregate，不是每个 function/tool 的 token split；ResponseUsage schema没有逐工具费用或统一 cost 字段。tool 参数/结果是内容，不应进入 Usage Ledger。 | [Responses OpenAPI tool example](https://api.openai.com/v1/responses)；High |
| Prompt cache | cache read 是 `cached_tokens`；当前 OpenAPI/guide 对新模型还报告 `cache_write_tokens`。 | `prompt_cache_key`/cache options 是请求意图，不证明 hit；返回 usage 才证明 read/write dimension。 | provider/model 可能不支持某维度；缺失必须是 unknown/not-applicable，不能写 0。cache retention 随 model/组织数据策略变化。 | [prompt caching](https://developers.openai.com/api/docs/guides/prompt-caching)、[Responses OpenAPI](https://api.openai.com/v1/responses)；High/Medium |
| Reasoning | reasoning tokens 是 output token details 的子维度，并计入 output/total。 | returned model 决定该维度是否有意义。 | reasoning text/summary/encrypted content 与 token count不同；不得为了计量保存 hidden reasoning content。字段缺失不能估成 visible output 的差。 | [Responses OpenAPI](https://api.openai.com/v1/responses)；High |
| OpenAI Batch（条件候选） | 每个成功 output line 包含 input `custom_id`、provider `request_id` 和底层 response body/usage；失败写 error file。 | `custom_id` 在 input file 内必须唯一，用于乱序结果关联；底层 response保留 model/usage。 | batch 是异步 24h window；cancel/expire 时仅已完成请求有 response，官方说明 completed requests 的已消费 tokens 会收费。`custom_id` 不是跨 batch provider 幂等承诺。Agent Compose facade不暴露 Batch endpoint。 | [Batch guide](https://developers.openai.com/api/docs/guides/batch)；Medium |
| OpenAI Images generate（条件候选） | `total_tokens`、`input_tokens`、`output_tokens`；input details 有 `text_tokens`、`image_tokens`。流式只有 `image_generation.completed` 给最终 usage；partial image 不给最终总量。 | response example 有 `created`，但没有 Response object ID 或 returned model；model/size/quality/n 主要是 gateway 请求事实。HTTP `x-request-id`仍应捕获。 | partial images 会增加 image output tokens；失败 body没有 usage 保证。图片二进制、prompt、mask不可放账本。 | [Images OpenAPI](https://api.openai.com/v1/images/generations)、[image generation usage](https://developers.openai.com/api/docs/guides/tools-image-generation#usage)；High/Medium |
| Organization completions usage | 最小 1m bucket；当前 OpenAPI逐字段为 `input_tokens`、`input_cached_tokens`、`input_cache_write_tokens`、`input_uncached_tokens`、`output_tokens`、`input_text_tokens`、`output_text_tokens`、`input_cached_text_tokens`、`input_audio_tokens`、`input_cached_audio_tokens`、`output_audio_tokens`、`input_image_tokens`、`input_cached_image_tokens`、`output_image_tokens`、`num_model_requests`；可按 project/user/API key/model/batch/service tier group。 | bucket 只有 `start_time`/`end_time` 和选定 group dimensions。 | 没有 response ID、`x-request-id` 或 gateway attempt ID；是聚合 reconciliation evidence，不能在共享维度下拆成单次 Runtime Run。API 未在本研究范围内给出逐请求 correction/finality SLA。 | [Organization completions usage OpenAPI](https://api.openai.com/v1/organization/usage/completions)；High |
| Organization images usage | 1m/1h/1d bucket；`images`、`num_model_requests`；可按 project/user/API key/model/size/source group。 | 同上，只有 bucket/group identity。 | 没有 image request ID 或 token breakdown，不能替代逐请求 receipt。 | [Organization images usage OpenAPI](https://api.openai.com/v1/organization/usage/images)；High |
| Agent Compose LLMService | 返回 text、model、response ID、finish reason、JSON。 | 有 response ID/model，但没有 upstream `x-request-id`、provider time 或 run binding。 | response parser不声明/解析 usage；错误路径只返回 message。 | [`GenerateResult`/parser](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/pkg/llms/client.go#L13-L54)、[proto](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/proto/agentcompose/v2/agentcompose.proto#L1811-L1823)、[runtime SDK](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/runtime/agent-compose-runtime-sdk/src/llm.ts#L16-L22)；High |
| Agent Compose runtime LLM facade | 同协议成功/错误响应透传 body/header；response header只排除 content length/encoding，因此可把 upstream `x-request-id`传给 guest。跨协议会 bridge。 | Facade Token带 sandbox、provider、model、wire API、source、Agent Compose run ID。 | handler没有写 usage evidence；upstream network error变为 generic 502。跨协议 normalization不是原始 evidence，且不能证明保留未来/unsupported usage维度。 | [facade routes/handler](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/pkg/agentcompose/proxy/runtime_llm.go#L40-L175)、[header forwarding](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/pkg/llms/facade_http.go#L30-L58)、[FacadeToken](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/pkg/llms/model.go#L61-L72)；High |
| Agent Compose Codex agent result | 上游 Codex SDK 0.144.1 的 `turn.completed` 有 input/cached/output/reasoning usage。 | SDK thread ID不是 provider request/response ID。 | Agent Compose event handler只处理 thread、failure和 item events；`AgentResult`无 usage，故正式输出丢弃 SDK usage。 | [Codex SDK events](https://github.com/openai/codex/blob/44918ea10c0f99151c6710411b4322c2f5c96bea/sdk/typescript/src/events.ts#L20-L36)、[Agent Compose handler](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/runtime/javascript/src/runners/codex.ts#L141-L226)、[AgentResult](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/runtime/javascript/src/types.ts#L4-L11)；High |

## Provider request identity、model、时间与归因

这些身份不可合并：

| identity/fact | 含义 | 可否去重计量 |
| --- | --- | --- |
| SlideSmith Runtime Run / operation | 业务与执行归因；一个 Runtime Run 内可有多个 model calls。 | 否；按它去重会丢掉合法的多次调用和 retry consumption。 |
| Gateway logical call | 一次业务 model-call intent，可跨多个 outbound attempts。 | 否；用于聚合，不代表 provider exact-once。 |
| Gateway attempt ID | 每次真正 outbound provider request 的稳定、本地唯一身份。 | 是 gateway 内部主关联键；每次 retry必须新建 attempt。 |
| `X-Client-Request-Id` | OpenAI 接受的 caller diagnostic ID，ASCII、最多 512 chars，官方要求每个 request 唯一。 | 否；[官方只承诺诊断查找](https://developers.openai.com/api/reference/overview#supplying-your-own-request-id-with-x-client-request-id)，没有幂等语义。可承载 gateway attempt ID。 |
| `x-request-id` | provider 对一次 HTTP request 生成的 server request ID。 | 当存在时，是最强的 provider-attempt dedup/correlation fact之一；必须与 provider/account/endpoint scope 一起使用。 |
| Responses `response.id` | provider response object identity；可用于 retrieve/cancel/background resume。 | 可防止同一 provider object 的重复 ingest；不能证明另一次 create 没有产生另一个 object。 |
| Batch `custom_id` | input file内 caller correlation。 | 仅在 batch/file scope去重结果；不是 provider create幂等键。 |
| requested / returned model | 前者是 gateway request fact，后者是 provider response fact。 | 两者都保留；alias可能解析到不同 snapshot，不能只记录请求字符串。 |
| gateway sent/headers/terminal-observed timestamps | SlideSmith 自己的接收/发送/观察时间。 | 可靠描述平台观察顺序；不是 provider执行时间。 |
| provider `created_at`/`completed_at` 或 image `created` | provider wall-clock fact。 | 作为 evidence原样保存；不替代 gateway monotonic latency，且可能只有秒粒度。 |

OpenAI `x-request-id` 与 `X-Client-Request-Id` 的正式边界见 [API overview](https://developers.openai.com/api/reference/overview#debugging-requests)。Provider gateway 必须在 protocol translation 之前捕获原生 header、returned model、终态 body/event和 provider时间；否则 Agent Compose bridge或adapter可能只留下归一化字段。

## 不可观测 failure matrix

`exact` 仅表示 provider 原生逐请求终态 evidence 已被捕获，不表示价格或账单永不修正。`unknown` 是事实状态，不是零。

| 场景 | provider 是否可能已消费 | 可获得 evidence | 必须记录的事实状态 |
| --- | --- | --- | --- |
| Gateway 本地 validation/authorization在创建 outbound request前拒绝 | 否，前提是 gateway 能证明没有 send attempt。 | 本地拒绝原因、无 attempt。 | provider usage `not_applicable/known_zero_by_no_send`；不得把普通网络错误归到此类。 |
| connect/TLS/network error，未收到 response headers | 可能；客户端不知道 provider 是否收到。 | gateway attempt、`X-Client-Request-Id`；没有 `x-request-id`。OpenAI支持人员可能借 client ID查询，但这不是自动 receipt API。 | usage `unknown`; later reconciliation eligible。 |
| HTTP 4xx、429、5xx | 官方没有保证每个错误 body含 usage。请求可能有 `x-request-id`。 | status/error、headers；只在实际 body含正式 usage时记录该 usage。 | 默认 `unknown/not_reported`，不能由状态码推定 0。 |
| 非流式 request timeout、caller cancel、response body读失败 | provider可能继续或已经完成。 | 若已得到 Response ID且对象可 retrieve，之后可补；否则只有 attempt/headers。 | 初始 `unknown`，允许 late receipt。 |
| streaming 收到 `response.completed` + usage | 已完成。 | 原生 terminal event、Response ID、usage、model。 | `exact/provider_reported`。 |
| streaming 在 terminal event前中断 | 可能已消费全部或部分；partial deltas没有总 usage契约。 | attempt、可能的 Response ID/headers、partial event sequence。 | `unknown`; 不得 tokenise partial text作 provider actual。 |
| 同步 Responses cancel（终止连接） | 官方只说明通过断开连接取消；不能证明 provider在何时停止。 | 本地 cancel time，可能有早期 Response ID。 | `unknown`，直到 retrieve/terminal/reconciliation evidence。 |
| background cancel | provider返回最终 Response object；重复 cancel调用本身幂等。 | terminal object/status；usage若实际存在则捕获。 | usage存在则 provider-reported；否则 `unknown/not_reported`。 |
| Batch cancel/expire | 已完成请求可能已消费；未完成请求被取消。 | completed output lines有逐请求 usage；expired/error lines有 error。 | 逐行结算；不能用 batch总状态把所有行记 0或记一次总量。 |
| Agent Compose runtime SDK `llm()` timeout | SDK只 abort fetch并抛 timeout；没有 usage return。 | Agent Compose local error。 | usage `unknown`; Agent Compose结果不是 receipt。 |
| SlideSmith detached poll timeout | 当前 client直接结束 context，没有调用 Agent Compose `StopRun`；下游可能继续并产生 late usage。 | 最近一次 run status和 local timeout。 | Runtime Run先按现有 fence处理；usage保持 unknown并继续 reconcile。 |
| Agent Compose facade upstream network error | handler返回 generic 502，未把 upstream request/error evidence保存到 Run。 | facade 502；有时 guest已收到早期 header。 | Gateway若独立捕获则以 gateway为准；否则 unknown。 |
| Codex stream failure/retry | 一个 logical turn可能发起新的 sampling request，每个都可能消费。 | Codex最终 turn usage只在完成时给 aggregate；Agent Compose又丢弃该字段。 | 每个 gateway attempt独立；不能只按 turn/run写一条。 |
| Agent Compose daemon/node crash | Agent Compose没有 usage store；active run失败也不能恢复已丢失的 terminal provider event。 | Run failure/log residue、gateway自身 evidence。 | Gateway evidence仍可 late ingest；仅 Agent Compose evidence时 unknown。 |
| Organization Usage 后来出现差额 | aggregate表明某 bucket/group发生消费，但不能唯一映射共享 Runtime Run。 | bucket/group totals。 | reconciliation discrepancy；不能伪造逐请求 receipt。 |

当前 SlideSmith adapter 的 request/result只有 Phase/command/prompt/path和 Agent Compose run/session/status/exit/workspace/raw JSON/error；没有 Runtime Run correlation、provider request identity、model或usage。Detached timeout只退出 polling。[当前 contract与timeout](https://github.com/Vt00ls/SlideSmith/blob/0199fc9254ad6fbfcaa840ad2a4969052d042bd9/backend/internal/service/agent_compose.go#L17-L41)；[poll timeout](https://github.com/Vt00ls/SlideSmith/blob/0199fc9254ad6fbfcaa840ad2a4969052d042bd9/backend/internal/service/agent_compose.go#L153-L203)。（Medium）

## Retry、幂等与 dedup 风险

### 已证明的 retry 行为

- 当前 SlideSmith/Agent Compose 的已证明路径是 Codex 0.144.1，不是标准 OpenAI Python/Node SDK。作为未来 Gateway 的条件风险，OpenAI 官方 `openai-python` 与 `openai-node` 当前源码文档都声明默认自动 retry 2次，覆盖 connection errors、408、409、429和 5xx；但 #12 尚未选择或固定 Gateway SDK/version，所以这些默认值不能冒充当前 runtime事实。若未来采用，必须固定版本、显式配置并纳入attempt-level contract test。[openai-python README at `e67afa8`](https://github.com/openai/openai-python/blob/e67afa88433dad9f97e733ae8f2ee6e9c240bc51/README.md#L694-L712)，[openai-node README at `4ced1a8`](https://github.com/openai/openai-node/blob/4ced1a8eaba3f5e960b94090a75e8048f7642439/README.md#L387-L405)。（High，条件事实）
- Codex 0.144.1 默认 `request_max_retries=4`、`stream_max_retries=5`；HTTP request policy会对 5xx、timeout/network retry，不对 429 retry，并为每个 attempt重新构造 Request。[provider defaults/policy](https://github.com/openai/codex/blob/44918ea10c0f99151c6710411b4322c2f5c96bea/codex-rs/model-provider-info/src/lib.rs#L26-L33)，[policy mapping](https://github.com/openai/codex/blob/44918ea10c0f99151c6710411b4322c2f5c96bea/codex-rs/model-provider-info/src/lib.rs#L261-L277)，[fresh request loop](https://github.com/openai/codex/blob/44918ea10c0f99151c6710411b4322c2f5c96bea/codex-rs/codex-client/src/retry.rs#L49-L72)。（High）
- stream sampling retry会从 history重建 prompt并重新调用 sampling；transport fallback还能扩展实际尝试次数，所以不能把一个 Codex turn假设成一个 provider request。[sampling retry loop](https://github.com/openai/codex/blob/44918ea10c0f99151c6710411b4322c2f5c96bea/codex-rs/core/src/session/turn.rs#L1139-L1206)，[fallback/retry](https://github.com/openai/codex/blob/44918ea10c0f99151c6710411b4322c2f5c96bea/codex-rs/core/src/responses_retry.rs#L20-L78)。（High）
- Codex 把 thread ID放进 `x-client-request-id`，而 OpenAI官方要求该 header每个 request唯一。流重试可能复用 thread ID；该值既不能区分 attempts，也没有 provider幂等语义。[Codex header](https://github.com/openai/codex/blob/44918ea10c0f99151c6710411b4322c2f5c96bea/codex-rs/codex-api/src/endpoint/responses.rs#L70-L98)。（High）
- Codex只在 `ResponseEvent::Completed`记录 token usage；stream在 `response.completed` 前关闭时记录 failure。它会在 telemetry/inference trace里关联 upstream `x-request-id`，但 Agent Compose public result没有导出这些 telemetry facts。[Codex event mapping](https://github.com/openai/codex/blob/44918ea10c0f99151c6710411b4322c2f5c96bea/codex-rs/core/src/client.rs#L1913-L2082)。（High）

### 风险与约束

| 风险 | 后果 | #12 必须维持的边界 |
| --- | --- | --- |
| OpenAI create没有本研究可引用的通用 `Idempotency-Key`/exact-once保证 | timeout后retry可能成为多个可计量请求。 | 每个 outbound attempt独立建 receipt/evidence；不得以 logical call、Runtime Run或相同 payload去重。 |
| client、Codex、Agent Compose/SlideSmith重试层叠 | 一个业务 intent产生未知数量 provider attempts。 | retry必须在 Gateway可见；禁止绕过 Gateway的 provider egress。 |
| provider内部 retry不可观测 | 外部只见一个 request ID/最终 usage，无法拆内部执行。 | 把 provider终态 usage当 provider声明的单请求 aggregate，不编造内部 attempts。 |
| `x-request-id`、Response ID、client ID混用 | 重复 ingest或把两个真实消费错误合并。 | provider/account/endpoint + gateway attempt + server request ID + provider object ID分字段、分语义关联。 |
| 同一 Response被 retrieve/poll多次 | 同一终态 body重复到达。 | 相同 provider object/evidence version幂等 ingest；保留 first/last observed time，不重复结算。 |
| Agent Compose protocol bridge归一化 | 原生字段、未来 usage维度或错误语义可能被降级。 | authoritative capture发生在 upstream-native side、bridge之前；bridge输出只作 adapter evidence。 |
| Organization Usage aggregate晚到或与 receipts不一致 | 无法无损分摊到共享运行。 | 只建立 reconciliation discrepancy/correction候选；不得按比例伪造逐请求 actual。 |

## Agent Compose 是否保存并关联原始 usage evidence

答案是：**能做短生命周期的 run/provider/model关联，但没有保存原始 usage evidence。**

1. Facade Token正式字段有 sandbox ID、token hash/fingerprint、model、provider ID、wire API、source、Agent Compose run ID和 issued/expires/revoked times。[model](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/pkg/llms/model.go#L61-L72)（High）
2. SQLite schema只为 provider、model、provider-model和 facade token建表；没有 request、response、usage或receipt表。Prompt-attached/agent token在调用结束被删除，sandbox lifecycle还会 revoke/清理 token，因此它不是耐久 usage evidence。[schema](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/pkg/storage/configstore/llm_config.go#L19-L67)，[delete/retention](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/pkg/storage/configstore/llm_config.go#L204-L250)（High）
3. RunEvent只有 generic `payload_json`；RunSummary/RunDetail只有 run status、timestamps、prompt、output/result/log path等，没有 provider request/response/model/usage的正式字段。[RunEvent](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/proto/agentcompose/v2/agentcompose.proto#L925-L938)，[Run contracts](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/proto/agentcompose/v2/agentcompose.proto#L1092-L1130)（High）
4. 对固定版本 production source 的 bounded scan，在 facade、LLMService、storage、proto、runtime result contract中未发现正式 usage/request-ID persistence surface。（Low，negative evidence）
5. 因而中心 Gateway必须自己持久化最小化的 provider-native evidence或受限 evidence reference/digest，并用 SlideSmith Runtime Run/operation/gateway call/attempt关联；不能等待 Agent Compose run完成后从 `inspect`/stdout重建。

## Late usage、correction 与 unknown/estimated

- **Provider-reported exact**：只用于捕获到原生逐请求 usage的字段；“exact”不表示价格恒定，也不表示其他缺失维度为零。
- **Unknown**：请求可能到达或消费，但没有终态 usage；必须保留原因和 evidence state。现有 [Runtime Execution contract](./runtime-execution.md) 已规定 missing evidence never means zero usage。
- **Not applicable**：协议/model正式不产生某维度，且该能力由 pinned provider contract证明。它与 provider没有返回字段不同。
- **Estimated**：仅在 #12明确选择估算源/算法时使用，并与 provider-reported字段分开；partial text tokenizer、本地图片尺寸或请求参数都不能冒充 provider actual。
- **Late**：终态 retrieve/background、Gateway outbox重放或 aggregate reconciliation可能晚于 Runtime Run terminal。Runtime Run/Phase outcome fence不能阻止账本接受合法 late usage evidence。
- **Correction**：没有发现 OpenAI逐请求 correction feed/finality SLA。若后续收到更强或更晚 evidence，保持 append-only并引用原事实作 offsetting/correction entry；不得覆盖历史。该约束来自 [ADR 0009](../adr/0009-own-append-only-usage-ledgers-by-personal-workspace.md) 和 Issue #14 standing decision。
- **Aggregate discrepancy**：Organization Usage只能证明 bucket/group差额；在没有唯一隔离维度时只能保持 unresolved reconciliation，不得把差额任意分摊给 Runtime Run。

## 日志、内容与隐私边界

1. Usage Ledger/receipt只能保存归因身份、provider/protocol/model、request/object identities、timestamps、usage数值及其 known/unknown/estimated状态、evidence hash/reference和必要错误分类。不得保存 prompt、response text、tool arguments/results、reasoning content、图片、mask、文件内容或 provider credential。
2. 原始 provider body常含用户 prompt/output、tool payload、reasoning/图像数据。若为审计保留原始 evidence，必须放入单独的 access-controlled durable object，按 Personal Workspace ownership、retention、encryption和 audited break-glass治理；账本只持 opaque reference/digest。Agent Compose RunDetail本身正式包含 prompt/output/result/log path，不能无差别复制到 receipt、metrics或log labels。[RunDetail](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/proto/agentcompose/v2/agentcompose.proto#L1119-L1130)
3. Agent Compose facade读取并转发最多 64 MiB request body；所检查 handler没有显式持久化 body，但这只是 bounded negative evidence，不能当作部署日志/反向代理永不记录内容的保证。[facade handler](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/pkg/agentcompose/proxy/runtime_llm.go#L66-L105)
4. OpenAI官方说明默认 abuse-monitoring logs可能含 customer content并保留最多30天；Responses application state在 `store=true`/默认下至少30天，ZDR会强制 `store=false`。Prompt caching可能保存加密KV tensors，具体 retention随model/策略变化；background即使 `store=false`也为异步执行临时落盘。最终 provider/region/ZDR/store/cache配置属于选型和合规验收，不能由 receipt设计隐式决定。[Your data](https://developers.openai.com/api/docs/guides/your-data)、[background mode](https://developers.openai.com/api/docs/guides/background)
5. `x-request-id`、Response ID、hashed token fingerprint通常是 operational metadata，但仍应按最小权限和retention处理；绝不记录 raw Authorization/API key/facade token。Agent Compose header filter会剥离 authorization/cookie和名称含 token/secret/api-key/auth 的 request headers。[header filter](https://github.com/chaitin/agent-compose/blob/e14c4dbd5e3b0dec6178073902d67d2765390427/pkg/llms/facade_http.go#L97-L123)

## 为 #12 提供的硬约束

以下是事实导出的 architecture constraints，不是 implementation SPEC或 ledger schema：

1. 所有生产 LLM/image provider egress必须经过能看到每个 upstream-native attempt 的中心 Gateway；Agent Compose facade/result不能担任 authoritative receipt producer。
2. 一个 Runtime Run可关联零到多个 gateway calls，一个 gateway call可关联一到多个 outbound attempts；每个 attempt有独立 identity、发送/观察时间和 evidence state。
3. `X-Client-Request-Id`每个 upstream request唯一，并可承载 gateway attempt identity，但不得视为 provider幂等键。Codex thread ID、Agent Compose run ID、Runtime Run ID、prompt hash均不得作为 provider billing dedup key。
4. Gateway在 protocol translation之前捕获创建 POST的 `x-request-id`、provider object ID、requested/returned model、provider/gateway times、terminal native usage和原始 evidence digest/reference；跨协议/Agent Compose输出只作次级 evidence。
5. 只有收到正式 provider usage的维度可标 provider-reported；missing、unknown、not-applicable、estimated必须显式区分。失败、timeout、cancel和断流不得自动结算为零。
6. retry为新 outbound attempt并可能产生新消费；同一 provider object的重复 retrieve/terminal投递需要幂等 ingest，但不同 provider request/response objects不得因 logical call相同而合并。
7. receipt ingest和Usage Ledger允许 Runtime Run/Phase terminal之后的 late evidence；correction append并引用原事实，不能覆写。Runtime execution fence控制业务结果，不应丢弃合法usage。
8. Organization Usage只作 aggregate reconciliation fallback。没有唯一 project/API key等隔离时，差额保持 unresolved，不得估算分摊为逐请求 actual。
9. provider model alias与returned snapshot分开记录；image API若不返回 model，requested model只能标 gateway-reported。Gateway观察时间与provider时间也分开。
10. usage/cost维度必须可扩展：当前 Responses含cache write/read、reasoning和多模态aggregate，Images/Organization Usage又有不同维度。#12不能把事实模型封死为 `input_tokens/output_tokens` 两列或假定所有 provider同构。
11. receipt/ledger默认不含内容。需要保存raw evidence时只持受控opaque reference/digest，并沿用Personal Workspace ownership、retention与break-glass边界。
12. provider/protocol/model、request-ID、failure/cancel/retry、late availability和data-retention行为必须成为 provider onboarding contract tests；OpenAI-compatible label本身不是兼容性或计量证明。

## Remaining unknowns

以下 unknowns 是真实未决项；不是 none：

1. 生产 text LLM provider、account/project isolation、base endpoint、model alias/snapshot、API version及其正式计量/retention contract尚未选定。测试服务器的第三方 compatible endpoint真实上游和契约未知。
2. 生产 image provider尚未选定，当前能力disabled且allowlist为空；因此OpenAI Images事实只能作候选输入。
3. 现场实际 Agent Compose daemon/guest/runtime image digest和版本未知；Compose `latest`不能证明 v2607.10.0行为。
4. OpenAI 对失败、cancelled、incomplete Response何时一定返回非空 usage没有逐状态保证；没有找到 Responses/Image create的通用幂等保证。
5. OpenAI Organization Usage的可见延迟、bucket最终性、历史修正窗口和逐请求correction feed/SLA没有在所查正式契约中给出。
6. built-in tool调用的逐工具计量/费用、货币cost、折扣/价格版本没有出现在 Responses逐请求 Usage schema；#12若需要cost而非resource usage，仍需独立的官方billing/cost contract研究或明确只记resource facts。
7. provider内部retry、路由和计量修正通常不可见；除非最终选定provider提供正式attempt/correction evidence，Gateway不能拆分。
8. `store`、ZDR/MAM、prompt-cache retention、data residency和raw evidence保留策略依赖最终组织合同/合规配置，当前仓库不能证明。
9. 对 aggregate-only discrepancy是否采用独立credential/project隔离以提高归因精度，以及保留为unresolved还是走人工correction，属于 #12需决定的policy，不是provider事实。

这些 unknowns 不阻塞 #12 开始 grilling：它们已经有明确的 authority、配置或验收归属，并且事实边界要求 #12 保留 unknown/reconciliation 状态。
