# Bind recovery to joint database and object points

SlideSmith treats a recoverable state as one joint Recovery Point that binds a PostgreSQL point-in-time target to every committed durable-object reference and its independently verified immutable backup bytes. PostgreSQL and object bytes therefore share an RPO of at most 15 minutes. Recovery is staged: verified Task metadata and Artifact Versions return read-only within four hours, while Source Material, Checkpoints, release/catalog dependencies, Runtime Images, and full mutation and execution return within eight hours.

Enterprise V1 remains a single-site deployment without automatic failover, but its recovery baseline covers loss or compromise of the entire production site. At least one encrypted, immutable copy lives in an independent failure and authority domain. Production credentials cannot delete it or shorten its retention, while restore and decryption require a Platform Administrator and an independent key or backup custodian. Physical offline media remains an optional deployment enhancement rather than a V1 acceptance requirement.

Joint Recovery Points provide a 35-day point-in-time window. Authorized deletion or Personal Workspace purge immediately removes online references and records recovery-suppression facts, but already locked backup copies may retain encrypted, inaccessible bytes until that window expires. Recovery never re-enables an old Share Link or Access Code. These choices preserve immutable disaster recovery without introducing per-Personal Workspace application keys or treating backup as a legal archive.

## Considered options

- Giving PostgreSQL and object storage independent RPOs was rejected because a newer database paired with older bytes would create committed references that cannot be restored and would violate compound authority.
- Relying on coordinated vendor snapshots or object-store listing was rejected because neither establishes business ownership, reference membership, digest integrity, or a portable recovery contract.
- Continuing mutations after the recovery watermark exceeds 15 minutes was rejected because it would turn the RPO into an observational target instead of a durability boundary. The platform enters read-only protection at the limit.
- Letting one production or administrator credential write, read, decrypt, and delete backups was rejected because one compromise could destroy both the primary site and its recovery path.
- Rewriting immutable backups on every business deletion was rejected because it weakens ransomware resistance and is incompatible with the accepted V1 encryption model.
- Requiring physical offline media was rejected as a V1 baseline because an independently controlled immutable copy plus offline credential custody meets the confirmed threat model while preserving the four- and eight-hour RTOs.

## Consequences

- A Recovery Point becomes valid only after PostgreSQL base/WAL evidence, a committed-reference inventory, every referenced object receipt, required OCI inventory, key-version evidence, and a signed canonical manifest are locked in the independent backup domain.
- The latest finalized point defines the recovery watermark. Mutations are fenced no later than the point at which its protected-through age would exceed 15 minutes.
- Restore occurs in a fresh isolated environment, is idempotent and generation-fenced, and promotes first to read-only and then to full operation through explicit validation gates.
- All pre-incident Share Links and Access Codes remain invalid after restore; an Owner must issue new ones.
- The normal PITR window is 35 days. Legacy cutover pins verified pre- and post-cutover Recovery Points until its separate migration decision closes the rollback window.
- Monthly sampled and quarterly full drills are the target operating baseline. The first implementation phase establishes the contracts, evidence, gates, and one pre-cutover end-to-end exercise; recurring automation may follow during operational hardening.
