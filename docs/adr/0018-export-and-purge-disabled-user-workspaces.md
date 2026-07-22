# Export and purge disabled-user workspaces instead of transferring tasks

SlideSmith does not transfer a Personal Workspace or Task between Users. When a disabled User's retained work must leave the platform, a Platform Administrator may use audited break-glass to create a temporary Workspace Export, prove delivery to an external administrative archive, and then invoke a separate explicitly authorized purge. The Personal Workspace identity is never reassigned or reused, and SlideSmith retains only its tombstone, export evidence, audit facts, and Usage Ledger history required by policy after purge.

## Considered options

- Transferring a quiescent Task aggregate to another Personal Workspace was rejected because it would move content across ownership and deduplication domains, complicate authorization generations, retention, recovery, backup, and future encryption policy.
- Retaining the Workspace Export archive inside SlideSmith was rejected because it would duplicate the content that purge is intended to reclaim.
- Combining export and purge into one operation was rejected because failed, partial, or unverified delivery must never authorize deletion.

## Consequences

- Workspace Export and purge are distinct administrator intents with independent authorization, audit, idempotency, and failure semantics.
- Exported bytes are staging content under an expiring lease, not an Artifact Version, backup, or new Personal Workspace. Successful export requires a verified external delivery receipt bound to the export manifest digest.
- Purge revalidates the Workspace generation and successful export evidence before detaching durable references. Delivery failure leaves the Personal Workspace and its retained work unchanged.
- This decision supersedes the Task-transfer portion of the resolution in GitHub issue 18; the identity, ownership, principal, authorization, and audit decisions in that resolution remain in force.
