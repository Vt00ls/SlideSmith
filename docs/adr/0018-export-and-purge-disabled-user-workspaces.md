# Export and purge disabled-user workspaces instead of transferring tasks

SlideSmith does not transfer a Personal Workspace or Task between Users. User
disable fences owner, Share, and mutation authority without starting an
automatic deletion timer. Once the Workspace is quiescent, two distinct
Platform Administrators may use an exact-target, one-shot break-glass grant to
create a complete temporary Workspace Export and prove its delivery to an
allowlisted external administrative archive through an authenticated Export
Delivery Receipt.

Purge is a separate dual-controlled administrator intent. It requires a
verified generation-bound export, complete eligibility checks, a
non-bypassable 24-hour cooling-off period, requester reauthentication, healthy
backup and suppression authority, and an external preservation-clearance
assertion. A Purge Fence activated in an independent immutable suppression
authority is the irreversible linearization point. Before it, the disabled
User may be reactivated and pending purge cancelled; after it, only forward
repair is permitted and no database, exact-byte repair, or Recovery Point may
restore the Workspace.

Purge removes SlideSmith online authority immediately and releases semantic
content references through their owning modules. Primary Durable Object bytes
follow the existing default seven-day reclamation grace; encrypted immutable
backup residue expires through the normal 35-day Recovery Point window and is
never made readable by restore. The external administrative archive and bytes
already disclosed to recipients remain outside SlideSmith purge.

After purge, the User, Personal Workspace, and external-identity mapping are
terminal and cannot be reactivated or reused in enterprise V1. SlideSmith
retains only a content-free Workspace Tombstone, export and purge evidence,
recovery suppression, Usage Ledger history and authoritative audit required by
policy. V1 has no legal-hold or e-discovery workflow; a known or uncertain
external preservation obligation blocks purge and cannot be overridden by a
Platform Administrator.

## Considered options

- Transferring a quiescent Task aggregate to another Personal Workspace was rejected because it would move content across ownership and deduplication domains, complicate authorization generations, retention, recovery, backup, and future encryption policy.
- Retaining the Workspace Export archive inside SlideSmith was rejected because it would duplicate the content that purge is intended to reclaim.
- Combining export and purge into one operation was rejected because failed, partial, or unverified delivery must never authorize deletion.
- Automatic purge after disable was rejected because a timer is not User,
  retention, legal, or irreversible-deletion authority.
- Administrator-selected partial export and incomplete-export override were
  rejected because purge could silently destroy required retained work.
- Restoring after the Purge Fence or reusing a purged identity was rejected
  because it would make deletion and audit semantics generation-dependent and
  reversible through an older authority point.
- Rewriting locked backups for immediate erasure was rejected because it would
  weaken the accepted ransomware-resistant recovery domain.

## Consequences

- Workspace Export and purge are distinct administrator intents with independent authorization, audit, idempotency, and failure semantics.
- Exported bytes are staging content under an expiring lease, not an Artifact
  Version, backup, or new Personal Workspace. The deterministic ZIP64 archive
  is complete only when its signed canonical manifest, archive digest and
  external receipt all verify.
- Purge revalidates Workspace generation, quiescence, coverage roots, receipt,
  references, leases, integrity, preservation clearance, Recovery Point
  watermark and exceptional pins before activating the Purge Fence. Delivery
  failure leaves the retained Workspace unchanged.
- Export and purge are asynchronous, fenced and idempotently reconcilable.
  Failure before the Purge Fence may be cancelled; failure after it keeps the
  Workspace inaccessible and proceeds through forward repair.
- Semantic deletion, primary object reclamation and immutable backup expiry are
  separate state machines. Physical failure becomes Cleanup Debt and never
  reports reclaimed capacity falsely.
- Complete operational, retention, failure and adapter contracts are recorded
  in
  [workspace-export-and-purge.md](../architecture/workspace-export-and-purge.md).
- This decision supersedes the Task-transfer portion of the resolution in GitHub issue 18; the identity, ownership, principal, authorization, and audit decisions in that resolution remain in force.
