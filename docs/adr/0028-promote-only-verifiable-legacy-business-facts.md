# Promote only verifiable legacy business facts

SlideSmith will migrate legacy business records through a fenced, ledgered
hard cutover that promotes a source fact into a target domain fact only when
its ownership, relationships, immutable content, and required target evidence
are exact and verifiable. Ambiguous records remain owner-scoped read-only
legacy history or protected quarantine; they cannot authorize execution,
recovery, publication, access, usage, audit, repair, or cleanup. Legacy
workspaces, sessions, caches, failed residue, paths, mutable registry rows, and
global repository surfaces are hard-replaced rather than wrapped.

One authorized seed external identity binds each single-owner legacy
deployment to one User and Personal Workspace. This mapping is an audited
deployment input, not first-login behavior, administrator ownership, a shared
system Workspace, or a nullable fallback. Tasks and Personal Workspaces remain
non-transferable. Missing or corrupt retained Source Material, active Artifact
Version members, or accepted catalog packages block cutover instead of being
silently lost or fabricated.

The cutover freezes legacy mutation, pins a complete pre-cutover Recovery
Point, stages and validates idempotent batches, atomically activates target
facts read-only, pins and verifies a post-cutover Recovery Point, and then uses
a separate audited `CommitCutover` to close routine rollback and enable target
mutation. Before that commit, rollback restores the whole pre-cutover compound
set; afterward the default is forward repair. Legacy execution deletion is a
separate irreversible operator action after the rollback boundary, and every
failed deletion becomes durable Cleanup Debt under the exact resource owner.

## Considered options

- Inferring an Execution Lock, Template Lock, Checkpoint, Phase success, or
  Runtime relationship from status, Runner Profile, environment, mutable tag,
  path, session, timestamps, directories, recent runs, or logs was rejected
  because those facts do not satisfy the accepted target authority contracts.
- Assigning unowned work to the first User, a Platform Administrator, a shared
  system Workspace, or a nullable owner was rejected because it would create
  unauthorized content ownership and bypass the Identity & Ownership seam.
- Online dual write and a read/write legacy compatibility façade were rejected
  because they retain two business authorities and prevent deletion of the
  shallow global, path, session, and mutable-registry interfaces.
- Treating every Artifact row or object path as a published Artifact was
  rejected because Source Material, immutable publication membership, staging,
  failure residue, and execution output have different authorities and
  retention.
- Cleaning directories before target validation and rollback closure was
  rejected because legacy execution state is excluded from Recovery Points and
  its deletion is irreversible.

## Consequences

- Completed, cancelled, and failed Task history remains readable. Every other
  legacy Task is terminated as non-recoverable; continuing work requires a new
  Task from retained Source Material or an Artifact Version.
- Execution and Template Locks are all-or-nothing conversions from exact
  immutable evidence. Tasks without them remain readable but non-executable.
- Phase and Runtime records with unprovable outcomes or relationships remain
  safe legacy history. Migration never invents Runtime Evidence, Sandbox
  Leases, Checkpoints, Task Workspace Revisions, Usage Receipts, Usage Ledger
  entries, or authoritative audit facts.
- Source Material and active Artifact Version members are copied and activated
  only through verified Durable Object receipts. Locator-bearing legacy
  manifest digests are retained as evidence, not reused as target authority.
- `TemplateRegistryEntry` produces at most a deterministic candidate for exact
  frozen bytes. Legacy status, paths, matching directories, and equal files do
  not prove approval, activation, a Resource Bundle, or a Task lock.
- A target-native, owner-scoped, read-only legacy-history projection is the
  only compatibility surface. It exposes no path, session, object locator,
  resume, retry, workspace, publication, or cleanup capability.
- Migration identity mappings, batch decisions, disposition roots, activation,
  rollback closure, audit, and Cleanup Debt are durable and idempotently
  reconcilable. GORM `AutoMigrate` is not the data-migration ledger.

This decision deepens ADR 0016 without superseding it and preserves ADRs 0001,
0006, 0008, 0015, 0017, 0019, 0020, 0021, 0022, 0023, and 0027.
