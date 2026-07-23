# Separate authoritative facts from expiring telemetry projections

SlideSmith will keep authoritative domain facts and mandatory audit facts in
Platform PostgreSQL under their owning Platform Control Plane modules. Metrics,
distributed traces, structured logs, dashboards, and external audit delivery
remain incomplete, expiring projections that can be rebuilt where their source
facts are retained. Authoritative correlation uses immutable business,
decision, operation, revision, generation, fence, and evidence relationships;
a trace ID is diagnostic context and never a lifecycle, idempotency,
authorization, settlement, recovery, or cleanup key.

Mandatory audit commits in the same transaction as the protected decision and
fails that decision closed when it cannot be recorded. Failure of a telemetry
adapter or external audit sink does not roll back an already committed domain
or audit fact. Cleanup Debt remains a durable obligation under the module that
owns the resource and can be resolved only by verified cleanup evidence or an
explicitly authorized, audited decision, never by a log, metric, trace,
directory, bucket listing, or process observation.

## Considered options

- Treating logs or traces as the common event source was rejected because
  sampling, partial delivery, retention, redaction, clock ambiguity, and
  adapter failure cannot preserve business linearization or mandatory audit.
- Treating an external audit sink as the only audit record was rejected because
  a remote outage would either lose required evidence or make every Platform
  transaction depend synchronously on a vendor transport.
- Using one global correlation or trace identity was rejected because delivery
  replay, business retry, Runtime retry, provider retry, cleanup retry, and
  restore have different identity and causation semantics.
- Using high-cardinality business identities in metric labels was rejected
  because it creates unbounded cost, data leakage, and denial-of-service risk
  without adding authority.
- Resolving cleanup from inventory or telemetry was rejected because physical
  presence cannot prove ownership, retention, lease, fence, grace-period, or
  deletion authority.

## Consequences

- Owning modules commit mandatory audit with protected operations and expose
  typed fact projections. A shared append-only audit facility owns integrity,
  retention, access, and delivery mechanics without becoming another business
  state machine.
- W3C trace context may cross owned transports, but business identities travel
  in authenticated typed envelopes and baggage is empty by default. Agent,
  Tool, Agent Compose, and provider telemetry remains untrusted evidence until
  correlated through the owning Platform authority.
- Metrics use registered bounded dimensions; exact User, Personal Workspace,
  Task, run, grant, receipt, debt, node, provider-request, path, locator, trace,
  and free-form error values stay out of primary labels.
- Structured logs and traces are protected, redacted, and short-lived. They do
  not enter a Recovery Point or survive as hidden content archives.
- Audit persistence failure blocks the audited operation. Ordinary telemetry
  or external audit-delivery failure produces degraded health, retry, backlog,
  and alert evidence without changing the committed business result.
- Cleanup Debt records owner, opaque resource, retry, failure, estimated bytes
  and inodes, age, fence, blockers, and resolution evidence until verified or
  explicitly accepted through mandatory audit.
- Implementation hard-replaces post-transition best-effort Task events,
  path-bearing cleanup markers, and caller-defined telemetry maps rather than
  wrapping them as target audit or observability authority.
