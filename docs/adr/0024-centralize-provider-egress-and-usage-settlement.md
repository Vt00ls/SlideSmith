# Centralize provider egress and usage settlement

SlideSmith will route every production LLM and generative-image provider call through one logical `LLM Gateway`. The Gateway is a Platform-controlled deep module whose control-side authority records every logical Gateway Call and every real outbound Gateway Attempt before egress, validates a short-lived Runtime-bound grant, owns provider routing and credentials, captures provider-native evidence, and issues authenticated content-free Usage Receipts. Sandboxes, Agent Compose, Agent Workers, Tool Workers, and ordinary application callers cannot hold provider credentials or reach provider endpoints directly.

`Usage Accounting` is a separate Platform Control Plane deep module. It verifies Usage Receipts, owns each Personal Workspace's append-only Usage Ledger, owns Phase Run-scoped Quota Reservations, appends offsetting corrections, and reconciles late, duplicated, partial, estimated, and unknown usage. A receipt, provider response, Runtime result, aggregate provider report, log, or metric becomes authoritative usage only through this seam. Missing evidence is never zero, a retry always creates a new Gateway Attempt, and a late receipt remains valid after Runtime Run or Phase Run terminal state when its original authority and correlation are intact.

Enterprise V1 keeps Quota Reservation in observation mode. A quota shortage does not reject a Task, but failure to prove authorization, attempt identity, receipt durability, or reservation persistence fails new provider egress closed. The interface preserves a future enforcement disposition without enabling enforcement, billing, payment, invoicing, or chargeback.

## Considered options

- Allowing sandboxes or Agent Compose to call providers directly was rejected because provider credentials and retries would escape Platform fencing, provider-native receipt capture, and usage reconciliation.
- Treating one Runtime Run, agent turn, thread, prompt digest, or client request ID as one billable request was rejected because a Runtime Run can contain many model calls and every retry can create another real provider consumption event.
- Asking provider idempotency or an SDK retry loop to provide exactly-once charging was rejected because the researched provider contracts do not make that guarantee and hidden retries would bypass Gateway Attempt authority.
- Storing usage as mutable counters was rejected because duplicate delivery, late evidence, corrections, retry consumption, restore, and audit require append-only postings and immutable evidence relationships.
- Treating unknown usage as zero or proportionally allocating aggregate discrepancies was rejected because neither assertion is supported by provider evidence.
- Merging Usage Accounting into Runtime Execution or Task Orchestration was rejected because execution outcome, Phase progression, provider egress, and usage settlement have distinct authorities and failure clocks.

## Consequences

- A Gateway Call may own one or more Gateway Attempts. Every real retry or permitted fallback is a new Attempt, while duplicate observation of the same scoped provider object is ingested once.
- The Gateway persists an Attempt before sending. A crash around egress creates ambiguity and reconciliation work; it never authorizes blind replay of that Attempt.
- Runtime cancellation or fencing rejects new Calls but does not erase usage from an already accepted Attempt. Gateway and ledger failure semantics are therefore independent of Runtime outcome.
- Usage Receipts carry explicit provider-reported, estimated, unknown, not-applicable, or proven-no-send evidence state. Content-bearing provider bodies are not Ledger material.
- A Phase Run acquires at most one Quota Reservation before quota-bearing Runtime admission. Retry creates a new Phase Run and Reservation. Late usage can settle after the Reservation has closed without reopening it.
- Provider, model, account, retry limit, reconciliation delay, ZDR, residency, and data-retention choices remain versioned provider-onboarding inputs. No provider or billing product is selected by this decision.
