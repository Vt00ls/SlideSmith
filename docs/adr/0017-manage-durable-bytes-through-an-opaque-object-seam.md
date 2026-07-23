# Manage durable bytes through an opaque object seam

PostgreSQL is authoritative for durable-object intents, semantic metadata, typed references, verification receipts, retention eligibility, and business activation, while the durable store is authoritative for the actual bytes at adapter-private immutable locations. A shared `Durable Object` deep module is the only seam through which Source Material, Artifact Version members, template and resource packages, and C04 Checkpoint content prepare, attach, read, materialize, reconcile, and release bytes. Its interface exposes opaque content identities, verified evidence, read handles, and materialization leases rather than buckets, object keys, paths, mounts, vendors, or physical deletion.

## Considered options

- Treating PostgreSQL rows alone as proof that bytes exist was rejected because metadata cannot prove payload durability or integrity.
- Treating bucket contents or object-store manifests as business authority was rejected because listing and locator conventions cannot establish ownership, publication membership, or retention intent.
- Giving each business module its own storage adapter was rejected because acknowledgement, verification, repair, cache safety, reconciliation, and reclamation would be duplicated across callers.
- Using digest as the business identity was rejected because equal bytes do not imply equal ownership, authorization, or lifecycle.

## Consequences

- Business identities reference opaque `ContentID` values; digest and size remain immutable integrity facts and deduplication inputs.
- User content deduplicates only within its Personal Workspace policy domain. Platform-owned packages use a separate platform domain.
- Authoritative durable-object registry records share the Platform PostgreSQL authority so typed references can activate atomically with their owning business records. The object store remains a write-once byte carrier, not a second metadata authority.
- Cross-store writes use durable intents, strict verification receipts, a PostgreSQL activation transaction, and idempotent reconciliation rather than XA or caller-managed rollback.
- Source Material is a Task-owned durable input, not an Artifact without an Artifact Version. Artifact Version and Checkpoint manifests are location-independent and authoritative through PostgreSQL metadata and explicit references.
- Runtime Images remain content-addressed OCI registry artifacts; ordinary object payloads do not absorb container pull semantics.
- Missing content, digest mismatch, failed acknowledgement, unverified reads, and incomplete repair fail closed. An immutable identity can only be repaired with exact matching bytes.
- Physical deletion follows explicit reference and lease release, a non-zero reclamation grace period, and retriable Cleanup Debt.
