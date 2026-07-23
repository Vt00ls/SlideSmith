# LLM Gateway and Usage Accounting

This document records the provider-egress, Usage Receipt, Usage Ledger, Quota
Reservation, and reconciliation decisions resolved in
[GitHub issue 12](https://github.com/Vt00ls/SlideSmith/issues/12).
[CONTEXT.md](../../CONTEXT.md) is authoritative for domain language,
[ADR 0009](../adr/0009-own-append-only-usage-ledgers-by-personal-workspace.md)
fixes Usage Ledger ownership,
[ADR 0010](../adr/0010-reserve-quota-at-phase-run-boundaries.md) fixes the
reservation boundary,
[ADR 0024](../adr/0024-centralize-provider-egress-and-usage-settlement.md)
records the central egress and settlement choice,
[runtime-execution.md](./runtime-execution.md) defines Runtime Run and fence
authority,
[scheduling-and-capacity-admission.md](./scheduling-and-capacity-admission.md)
defines quota-bearing admission and the opaque Reservation validation seam,
and the
[provider evidence research](./llm-provider-agent-compose-usage-evidence.md)
defines the native facts and unknowns that this contract must preserve, and
[observability-audit-and-cleanup-debt.md](./observability-audit-and-cleanup-debt.md)
defines correlation, signals, redaction, alert, and retention contracts.

The design fixes authority, interface depth, provider egress, request identity,
receipt trust, append-only settlement, reservation ordering, late and failed
usage, reconciliation, privacy, recovery, and adapter acceptance. It does not
select a provider, billing product, schema, wire encoding, SDK, price catalog,
retry count, reconciliation SLA, or implementation sequence.

## Decision summary

`LLM Gateway` is the sole logical production egress module for LLM and
generative-image provider calls. It may be horizontally deployed, but its
control-side authority remains in the Platform Control Plane. Owned egress
workers and true-external provider adapters execute the exact decisions. The
Gateway validates Runtime-bound grants, records logical Gateway Calls and real
outbound Gateway Attempts before egress, controls versioned provider routes and
credentials, captures provider-native evidence before protocol translation,
and issues authenticated content-free Usage Receipts.

`Usage Accounting` is a separate Platform Control Plane deep module. It
verifies and ingests Usage Receipts, owns each Personal Workspace's append-only
Usage Ledger, owns Phase Run-scoped Quota Reservations, appends corrections,
and reconciles late, duplicated, partial, estimated, and unknown usage.

Runtime execution success, Phase outcome, provider terminal state, and usage
settlement are independent facts. A Runtime fence can prevent a result from
affecting the Task while an already accepted provider Attempt still creates
usage. Missing receipt evidence never means zero.

Enterprise V1 keeps reservations in observation mode. A quota shortage cannot
block a Task, but authorization, integrity, Attempt-journal durability,
receipt durability, and reservation persistence remain fail-closed gates.

## Standing constraints

- The Platform Control Plane remains authoritative for identity, Task and run
  relationships, Runtime execution decisions, and usage records.
- Usage Ledger ownership is fixed to the Personal Workspace at consumption
  time. Task or Personal Workspace transfer is not supported, and export or
  purge does not rewrite historical usage ownership.
- Each Usage Ledger entry retains Task, Phase Run, Runtime Run, provider,
  model, resource dimension, evidence, and correction attribution.
- One Quota Reservation covers at most one Phase Run. It can cover all Runtime
  Runs and Gateway Calls in that attempt; retry creates a new Phase Run and
  Reservation.
- Failure, cancellation, timeout, stream interruption, and node loss may still
  consume provider resources. Unknown and late evidence remain first-class.
- Agent Compose v2607.10.0 does not retain authoritative provider request or
  usage evidence. Its result, logs, paths, sessions, and thread identity are
  adapter evidence only.
- Quota enforcement, billing, payment, invoicing, departmental chargeback, and
  provider selection remain outside enterprise V1.

## Authority and ownership matrix

| Fact or action | Authority | Relationship to these modules |
| --- | --- | --- |
| Personal Workspace ownership and machine authority | Identity & Ownership | Supplies mutually exclusive, generation-fenced scopes and grants |
| Task revision, Phase Run identity and outcome, Runtime Run membership | Task Orchestration | Creates attempts and asks Usage Accounting to reserve and close; never posts usage |
| Runtime operation, deadline, Sandbox Lease, fence, Runtime Binding and terminal result | Runtime Execution | Supplies a fenced Gateway Grant and correlates receipt references; never invents usage |
| Provider Route Revision, credential use, Gateway Call and Attempt journal, native evidence and Usage Receipt | LLM Gateway | Owns provider access and evidence issuance |
| Provider execution and native usage report | Provider adapter and provider | External evidence only; becomes Platform usage after Gateway capture and Usage Accounting verification |
| Receipt ingest, Usage Ledger, Quota Reservation, correction and reconciliation | Usage Accounting | Owns authoritative usage and reservation decisions in Platform PostgreSQL |
| Totals and operational views | Usage query projection | Rebuildable from authoritative records; never a mutation authority |
| Metrics, traces, logs and external audit projection | [Observability and audit](./observability-audit-and-cleanup-debt.md) | Consume authoritative identities and decisions; cannot settle usage |

The Gateway Attempt journal is not a second Usage Ledger. It owns provider
egress and observation facts. Usage Accounting alone owns authoritative
consumption postings and reservation state.

## Deep-module interfaces and seams

Representative semantic families, not final method or wire names, are:

```text
ProviderAccess.Decide(Invoke | Cancel | Inspect)
  -> GatewayDecision

UsageAccounting.Decide(
  IngestReceipt | CorrectUsage |
  AcquireReservation | RenewReservation |
  BeginCloseReservation | FinalizeReservation |
  ExpireReservation | Reconcile
)
  -> UsageDecision

UsageAccounting.Query(OwnerScope | AdministratorAggregateScope)
  -> UsageView
```

`ProviderAccess` hides provider routing, protocol translation, credentials,
SDK behavior, retry, native evidence, signing, reconciliation and transport.
`UsageAccounting` hides deduplication, dimension normalization, append-only
posting, reservation accounting, correction, aggregate reconciliation, and
query projections.

Provider egress is a true-external seam with pinned production adapters and
deterministic fault-injection adapters. Platform-to-egress and
Gateway-to-Usage delivery are remote-but-owned seams with authenticated
transport adapters for production and in-memory adapters for tests. These
internal seams do not enlarge either external interface.

## Provider egress, network and secrets

Every production LLM and generative-image call made for User work must cross
the Gateway. This includes model calls initiated by Agent Workers, approved
Tool Workers, Agent Compose, nested agent or SDK retries, and image-generation
tools. Ordinary web-asset retrieval remains under its own controlled network
adapter; if a provider operation creates settleable LLM or image usage, it
crosses the Gateway.

- Sandboxes, Agent Compose, Agent Workers, Tool Workers, and ordinary
  application callers receive no provider credential or arbitrary provider
  endpoint.
- Runtime network policy is default-deny and permits only the internal Gateway
  endpoint for provider-class traffic. Only Gateway egress adapters may reach
  an approved provider destination.
- Runtime Execution obtains a short-lived `GatewayGrant` through an owned
  machine-authority seam. The grant binds Personal Workspace, Task, Phase Run,
  Runtime Run, operation, ownership and authorization generation, Runtime
  Binding, Sandbox Lease and fence, safety epoch, permitted capability and
  Provider Route policy, Quota Reservation, and expiry.
- The Gateway revalidates current authority, fence, route policy, Reservation,
  and recovery mode at Call acceptance. A bearer token alone is insufficient.
- Provider credentials remain in a secret broker and are resolved only by the
  exact Provider Route adapter. Raw credentials never enter a request,
  receipt, Runtime Evidence, Task Workspace, Checkpoint, log, trace, crash
  dump, or cache.
- Recovery-degraded/read-only mode and Runtime cancellation fence new Gateway
  Calls. Already accepted Attempts may terminate or be cancelled and still
  produce usage evidence.

If the Runtime fence commits first, a new Call is rejected. If Gateway Call
acceptance commits first, the accepted Call may send and its usage is settled
even if cancellation commits later. Usage settlement does not resurrect a
Runtime Run or permit its output to cross a fence.

## Provider Route Revision and onboarding

A Provider Route Revision is an internal immutable configuration fact that
binds the provider and account scope, endpoint and protocol, API version,
credential reference, model and capability allowlist, data-handling policy,
native evidence mapping, retry policy, reconciliation adapter, and acceptance
evidence. It contains no credential value.

The Gateway selects only a route allowed by the current Gateway Grant and
records the exact revision on every Call and Attempt. Requested and returned
model are distinct. A model alias is not a returned snapshot. Provider or
model fallback is allowed only when an already authorized route policy names
it; every fallback is a new Attempt and is never silent.

Provider onboarding must prove, with pinned contract tests, request identity,
terminal and streaming usage behavior, retry visibility, cancel and timeout
behavior, native evidence capture, model reporting, retention configuration,
and reconciliation capability. Hidden SDK retries are not production eligible
unless the adapter exposes each real outbound Attempt to the Gateway. An
`OpenAI-compatible` label is not compatibility or metering evidence.

The production text and image providers, accounts, models, ZDR, data
residency, route values, retry limits, and reconciliation deadlines remain
deployment acceptance inputs. No provider is authorized merely by this
architecture decision.

## Gateway Call and Attempt identity

One Runtime Run may own zero or more Gateway Calls. One Gateway Call represents
one logical provider invocation and may own one or more Gateway Attempts. One
Gateway Attempt represents one real outbound provider request. Every actual
retry or fallback creates a new Attempt because it may create new consumption.

A trusted caller that can preserve a stable Call identity supplies a
scope-bound key and canonical request digest. Exact replay returns the
committed Call and current state; the same key with a different digest is a
typed conflict. If a protocol cannot preserve a trustworthy Call identity,
each accepted inbound request creates a new Call. The Gateway never guesses
deduplication from payload, prompt hash, Runtime Run, agent turn, thread, or
provider model.

The identities remain separate:

| Identity | Meaning | Deduplication authority |
| --- | --- | --- |
| Runtime Run and operation | Business and execution attribution | Never a provider-consumption dedup key |
| Gateway Call | Logical provider intent | Idempotent only with the same trusted key and request digest |
| Gateway Attempt | One real outbound request | Primary Platform egress identity; every retry is new |
| Client request ID | Diagnostic or caller correlation | Never assumed to be provider idempotency |
| Provider request ID | Server request correlation within provider, account and endpoint scope | Evidence correlation only |
| Provider object ID | Provider response or job identity within route scope | Deduplicates repeated observation of that same object |
| Requested and returned model | Request intent and provider result | Both retained; neither deduplicates consumption |

`X-Client-Request-Id` may carry the Gateway Attempt identity when a protocol
supports it, but it remains diagnostic. Provider object and request IDs are
always scoped by provider, account, endpoint, protocol, and route revision.

## Attempt lifecycle and linearization

The semantic Attempt progression is:

```text
Prepared -> Dispatching -> AwaitingEvidence -> ProviderReported
                    \-> Ambiguous -> Reconciled | Unresolved
Prepared -> NoSend
```

The Gateway Call-acceptance PostgreSQL transaction is the logical-call
linearization point. Before any provider send, the Gateway commits the Call,
canonical request binding, Attempt identity, exact route, grant correlation,
and an egress enactment. The Attempt-preparation transaction is therefore
durable before the true-external adapter can send.

`NoSend` is valid only when the owned adapter proves that the Attempt did not
cross the egress handoff. A crash, timeout, cancellation, connection failure,
or acknowledgement loss around the handoff produces `Ambiguous`, not zero.
The same Attempt is never blindly replayed after ambiguity. An authorized retry
policy creates another Attempt and records the relationship.

Provider headers and terminal body or event are captured before protocol
translation. Receipt evidence commits before a terminal provider success is
acknowledged to the caller. For a stream, partial events may already have been
forwarded, but the terminal event is not treated as durable completion until
the Receipt and delivery outbox commit. A Usage Accounting outage after that
commit does not roll back provider output; the outbox retries ingest.

Current state may be updated through compare-and-swap, but the transition and
observation history remains immutable. A stale Gateway worker, route revision,
grant, signing key, or reconciliation cursor cannot overwrite newer evidence.

## Usage Receipt contract and trust

A Usage Receipt is an immutable, versioned, content-free evidence envelope. It
binds at least:

- receipt schema, Receipt identity and evidence revision;
- issuer, key version, canonical digest, and producer authentication;
- Personal Workspace, Task, Phase Run, Runtime Run, operation, Gateway Call,
  and Gateway Attempt;
- ownership and authorization generation, Runtime Binding, lease and fence,
  and safety epoch accepted when the Call was authorized;
- Provider Route Revision, provider, account and reconciliation scopes,
  endpoint class, protocol, and API version;
- requested and returned model;
- client, provider request, and provider object identities when observed;
- gateway accepted, send, header, terminal, observed, and recorded times plus
  provider times when reported;
- operation kind, transport result, provider terminal result, and ambiguity or
  no-send reason;
- an extensible set of resource dimensions with unit, quantity, provenance,
  and evidence state;
- content-safe native evidence digest or opaque evidence reference;
- prior receipt, duplicate observation, correction, and superseding
  relationships.

Evidence states are explicit:

- `provider_reported`: a native provider terminal body, event, or formally
  scoped provider evidence supplied the value;
- `estimated`: a versioned, named estimator supplied the value and it never
  presents as provider actual;
- `unknown`: consumption may have occurred but the value is unavailable;
- `not_applicable`: the pinned provider contract proves the dimension does not
  apply;
- `known_zero_by_no_send`: the Gateway proved that no provider send occurred.

Gateway authentication proves the Platform issuer; it does not elevate an
estimate or aggregate to provider-reported evidence. Receipts crossing a
process or durable delivery seam carry a canonical digest and rotated
signature, MAC, or equivalent workload authentication whose verification
history remains available. The exact cryptographic algorithm and serialization
belong to implementation design. A provider is not described as signing usage
unless it actually supplies such a contract.

The default retained native evidence is the exact content-free header and usage
projection plus a content-safe digest. Prompt, response text, tool arguments or
results, reasoning content, images, masks, files, and credentials never enter a
Receipt or Usage Ledger. If a provider contract requires retaining a
content-bearing raw body, it uses a separate Personal Workspace-scoped Durable
Object, an opaque reference, encryption, explicit retention, and audited
break-glass. A plain public hash of low-entropy content is not a safe substitute
for that controlled evidence.

## Receipt ingest and deduplication

Usage Accounting verifies producer authority, schema, canonical digest,
signature or workload identity, Personal Workspace ownership, original
Runtime and grant correlation, exact route scope, evidence revision, and
resource-dimension contract before an authoritative write.

- `ReceiptID + canonical digest` is the delivery idempotency identity. Exact
  replay returns the original ingest decision. The same identity with a
  different digest is an integrity incident.
- Repeated retrieve, callback, polling, or outbox delivery for the same scoped
  provider object and evidence revision settles once while preserving first
  and last observation facts.
- Different provider objects or Gateway Attempts are never merged because the
  Call, Runtime Run, model, payload, or numeric usage is equal.
- A missing provider object or request ID never becomes a wildcard dedup key.
- Delayed and out-of-order receipts are valid when their original authority and
  immutable correlation verify. Current Runtime or Phase terminal state does
  not invalidate consumption that already occurred.
- Unauthorized, corrupt, cross-Workspace, inconsistent, or unknown-schema
  receipts are quarantined and do not post. Errors do not reveal another
  Personal Workspace, provider credential, endpoint, request body, or object.

Receipt ingest, immutable evidence relationships, Usage Ledger postings,
Reservation projection changes, mandatory correction audit, and an internal
outbox commit in one Platform PostgreSQL transaction. A response lost after
commit is recovered by replay; a crash before commit leaves no settlement.

## Usage Ledger and correction

Each Personal Workspace owns one logical append-only Usage Ledger. Entries
contain signed quantities and retain the originating Task, Phase Run, Runtime
Run, Gateway Call and Attempt, Receipt, provider, requested and returned model,
resource dimension and unit, evidence class, occurrence, observation and
recording times, policy versions, and correction relationships.

A versioned dimension registry defines units, additive behavior, parent and
child dimensions, and safe roll-ups. It prevents `total`, input, output, cache,
reasoning, image, audio, request-count, and other provider-specific facts from
being double-counted or forced into two universal token columns. Monetary cost
and pricing are not V1 usage dimensions.

Provider-reported values post as actual. Estimated values may be retained as a
separate provisional posting class but never mix into actual totals. Unknown,
not-applicable, and proven-no-send receipt states remain authoritative evidence
without fabricating a numeric actual posting.

Correction is append-only:

1. verify a stronger receipt, formally scoped provider correction, or
   reason-bound authorized repair;
2. append signed offsetting entries that reference the exact prior entries;
3. append replacement entries when a corrected value exists;
4. commit offsets, replacements, Reservation projection, reason, actor,
   evidence, and mandatory audit atomically.

Late actual evidence following unknown creates the first actual posting and
needs no artificial zero offset. Replacing an estimate offsets the estimate and
adds actual while keeping the projections separate. A routine correction
cannot change Personal Workspace ownership. A cross-Workspace attribution
conflict is an integrity incident requiring an explicitly authorized repair,
not an automatic reconciler transfer. Correction keys are idempotent and a
same-key/different-payload replay fails closed.

## Quota Reservation contract

A quota-bearing Phase Run may own at most one Quota Reservation. A Phase that
cannot schedule quota-bearing Runtime work, including a pure Confirmation Gate,
does not need one. The Reservation binds the Personal Workspace and Phase Run,
estimate vector, dimension and policy versions, mode, expiry, generation, and
every settled Ledger entry.

The state semantics are:

```text
Proposed -> Active -> Closing -> Settled
                         \-> ReconciliationPending -> Settled
                                                   -> ExpiredWithUnknown
Proposed | Active -> Released  # only with proof that no provider send can exist
```

- Task Orchestration may create Phase Run and Runtime Run identities first,
  but quota-bearing Scheduler Admission and GatewayGrant issuance require an
  `Active` Reservation.
- Acquire serializes concurrent holds within one Personal Workspace and is the
  future quota-check linearization point. V1 fixes `Observe` mode, so shortage
  never rejects; authorization, ownership, persistence, policy-integrity, and
  recovery-mode failures still reject.
- Renew requires the current Phase Run, Reservation generation, active
  authority, and a non-terminal Phase. An expired Reservation never revives.
- Every accepted Usage Ledger posting updates the settlement projection in its
  ingest transaction. Actual usage exceeding the estimate posts in full and
  records an overrun; it is never truncated.
- Phase terminal state begins closing, fences new Gateway Calls, and waits for
  known Attempts, receipt delivery, and reconciliation. Provider usage and
  Runtime terminal state do not share a transaction.
- Direct release is allowed only when no Attempt can have crossed egress.
  Otherwise the Reservation settles, reconciles, or reaches
  `ExpiredWithUnknown` under its versioned policy.
- Expiry releases the operational hold but never asserts zero. It retains an
  unresolved reconciliation obligation. A late Receipt posts normally against
  the historical Reservation without reopening or reviving it.
- Phase retry creates a new Phase Run and new Reservation. It never reuses an
  old hold or settlement projection.

The interface can later return an enforcement disposition such as
insufficient or deferred without changing identity or posting contracts.
Changing V1 from observation to enforcement is a separate product decision;
this architecture does not authorize it.

## Failure and reconciliation contract

| Condition | Required behavior |
| --- | --- |
| Invalid authority, stale fence, route mismatch, unavailable secret, or Call/Attempt persistence failure before send | Fail closed and do not send |
| Timeout, cancel, connection loss, stream interruption, Gateway crash, or unknown handoff | Record `Ambiguous`, report typed reconciliation-required failure, and never infer zero |
| Runtime Run fails, is cancelled, times out, or is fenced | Reject new Calls; best-effort cancel accepted Attempts; retain and settle late usage |
| Provider terminal evidence is available | Persist native evidence and Receipt before acknowledging terminal completion |
| Usage Accounting is unavailable after Receipt/outbox durability | Allow the current provider result to complete and retry ingest asynchronously |
| Attempt journal, receipt outbox, backlog capacity, or recovery durability can no longer be guaranteed | Stop accepting new provider Calls |
| Duplicate, delayed, or out-of-order Receipt | Idempotent, order-independent ingest after full verification |
| Unauthorized, corrupt, inconsistent, or cross-Workspace Receipt | Quarantine, create integrity evidence, do not post |
| Aggregate provider report differs from receipts | Attribute only when an exact reconciliation scope proves a unique target; otherwise retain an unresolved platform discrepancy |
| Restore contains a non-terminal Call or Attempt | Invalidate old grants, mark uncertain work ambiguous, reconcile, and never blindly replay |
| Recovery watermark forces read-only | Reject new Calls while allowing accepted Attempts to terminate and usage evidence to be retained |

Reconciliation consumes the Gateway Attempt journal and receipt outbox first,
then exact provider object retrieve, authenticated callbacks or batch results,
then formally scoped aggregate provider reports. A versioned provider deadline
may move an unresolved Attempt or Reservation to a terminal unresolved state,
but it does not turn unknown into zero or prevent later correction.

Provider aggregate APIs can prove a discrepancy in one account, project, key,
model, or time bucket. They may create a correction only when that scope maps
uniquely to one authoritative Attempt or Personal Workspace under the onboarded
contract. Shared-scope differences remain unresolved; proportional allocation
to Tasks or Runtime Runs is prohibited.

## Queries, privacy and administrative access

Owner queries require an Identity & Ownership-issued scope and expose only the
owning Personal Workspace's actual totals, separately labelled estimates,
unknown and late evidence counts, correction history appropriate for the
Owner, and reconciliation freshness. Provider request IDs, internal account
scope, route details, signing material, raw evidence, and other Workspace
existence remain private.

The enterprise V1 Platform Administrator interface exposes content-free
platform aggregates and provider, model, route-health, unknown, late,
correction, backlog, and reconciliation diagnostics without Personal Workspace,
Task, Runtime Run, Receipt, or provider-request identifiers. It grants no
implicit content access. Inspecting a content-bearing raw-evidence object still
requires a reason-bound audited break-glass grant.

Totals are projections rebuilt from append-only Ledger entries and verified
receipt state. A metric, cache, materialized total, dashboard, log, trace, or
provider aggregate is never a balance or correction authority.

## Retention, backup, restore and repair

Usage Ledger entries, Gateway Call and Attempt identities and evidence roots,
Usage Receipts, Quota Reservation history, corrections, unresolved
discrepancies, mandatory audit, and signing-verification history are retained
business records. Workspace Export or purge does not rewrite Usage Ledger
ownership. Optional content-bearing raw evidence follows its explicit,
Personal Workspace-scoped retention rather than inheriting unlimited Ledger
retention.

Authoritative metadata belongs to Platform PostgreSQL and the joint Recovery
Point. A retained raw-evidence Durable Object participates only when an
authoritative reference and retention policy require it. Gateway grants and
in-flight execution are never recovered as live capabilities.

Restore advances Gateway and authorization generations, rejects every old
grant, treats non-terminal egress as ambiguous, reconciles Reservations, and
rebuilds projections from the Ledger. It does not resend a provider create,
adopt an Agent Compose result, or infer usage from a log.

Repair can restore only evidence that matches the original authority, schema,
canonical digest, signature or key history, provider route and object scope,
Personal Workspace, and immutable correlation. A different quantity or
ownership relationship requires append-only correction or an integrity
incident. Missing evidence is never repaired by changing the expected digest,
parsing a transcript, or allocating aggregate differences.

## Highest-level scenarios and adapter contracts

The `ProviderAccess` and `UsageAccounting` interfaces are the highest-level test
seams. A deterministic harness with controllable time, provider responses,
egress handoff, signing keys, delivery, database faults, and reconciliation
cursors covers:

- zero, one, and many Calls per Runtime Run and Attempts per Call;
- exact replay, same-key/different-payload conflict, concurrent Invoke and
  cancel, and stale grants, fences, route revisions, and signing keys;
- crash before send, during handoff, after provider acceptance, before Receipt
  commit, after Receipt commit, and around Ledger commit or response delivery;
- streaming terminal success, terminal-before-disconnect, disconnect-before-
  terminal, provider failure, cancel, background retrieve, and batch results;
- hidden or explicit retry, fallback, duplicate provider object observation,
  missing IDs, late evidence, corrections, and aggregate discrepancies;
- provider-reported, estimated, unknown, not-applicable, and no-send dimensions
  plus parent/child roll-up protection;
- Reservation acquire, renew, concurrent admission, settle, overrun, release,
  expiry with unknown, retry, late settlement, and future enforcement result
  compatibility without enabling enforcement;
- unauthorized, corrupt, partial, inconsistent, cross-Task and cross-Workspace
  receipts and content, credential, endpoint, and existence non-leakage;
- ledger outage with durable outbox, backlog and recovery watermarks, restore,
  exact repair, and projection rebuild.

Provider, secret broker, network policy, Runtime Execution, Task Orchestration,
Scheduler, PostgreSQL, Durable Object, signing, transport, aggregate reporting,
audit, and query adapters receive black-box contracts where applicable. Tests
assert identities, decisions, attempts, evidence states, postings,
reservations, corrections, and outcomes rather than SQL shape, SDK calls,
provider JSON, endpoint strings, queue products, log text, or dashboard values.

## Hard cutover and deletion test

This is replace-not-layer migration. Once the new interfaces and adapter
contracts exist, the target architecture deletes rather than wraps:

- provider credentials and direct provider endpoints in Agent Compose,
  sandboxes, Agent Workers, Tool Workers, runtime scripts, and ordinary
  application services;
- unrestricted provider network egress from Runtime sandboxes;
- hidden provider SDK retries and direct image-provider invocation that cannot
  expose each Attempt;
- inference of usage from Agent Compose results, Codex thread usage, logs,
  transcripts, paths, sessions, provider aggregate buckets, or Runtime outcome;
- mutable Task or Workspace usage counters and caller-owned Ledger writes;
- optional Gateway bypasses, fail-open provider fallbacks, and compatibility
  facades around old direct-egress configuration.

Production Agent Compose and image adapters point only to the internal Gateway
and receive no provider credential. Development and tests use local or
in-memory Gateway and provider adapters through the same interface. Legacy
execution without authoritative receipts is not backfilled as zero or
fabricated actual. Issue 17 owns the record-level cutover sequence.

## Rejected alternatives and downstream inputs

Rejected alternatives include direct sandbox egress; one receipt per Runtime
Run or agent turn; treating provider, thread, payload, or client request IDs as
exact-once billing keys; reusing one Attempt for retry; mutable usage counters;
unknown-as-zero; synchronous dependence on Ledger availability after Receipt
durability; proportional allocation of aggregate discrepancies; content-bearing
Ledger entries; per-Runtime-Run or Task-wide Reservations; enabling quota
enforcement in V1; and making Agent Compose, a provider SDK, logs, telemetry, or
aggregate reports authoritative.

Stable downstream inputs are:

- the [observability and audit contract](./observability-audit-and-cleanup-debt.md)
  receives Gateway Call, Attempt, Receipt, Ledger, Reservation, correction,
  unknown, late, backlog, integrity, and reconciliation identities plus the
  authoritative-versus-projection line;
- the resolved
  [Scheduler contract](./scheduling-and-capacity-admission.md) receives Active
  Reservation as a prerequisite for quota-bearing Runtime admission while
  fairness and placement remain separate;
- issue 17 receives the hard-cutover deletion test and the prohibition on
  fabricating legacy usage;
- Runtime Execution receives the required Gateway-only network and secret seam,
  Gateway Grant and receipt-reference semantics, and late-usage independence;
- Task Orchestration receives Phase Run reservation acquire and close ordering
  without gaining Ledger mutation authority;
- Backup & Recovery retains authoritative usage and Gateway evidence, invalidates
  old grants, and reconciles rather than replays in-flight provider work.

Superseded decisions: none.

New decision-only tickets: none.

Remaining fog affecting the first implementation specification: none.
Concrete provider, account, model, retry limit, reconciliation deadline, ZDR,
residency, SDK, schema, method names, wire encoding, deployment size, and
pricing remain provider-onboarding, adapter-acceptance, deployment, or future
product inputs and do not reopen this module interface.
