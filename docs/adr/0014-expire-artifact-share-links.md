# Expire artifact share links

Every Share Link is a revocable, time-bounded, read-only grant to one Artifact Version and requires a separate Access Code; permanent links are not supported. The platform applies a default and administrator-configured maximum lifetime, stores only a salted code verifier, rate-limits failed verification, audits access, and invalidates the grant when it expires, is revoked, or loses its Artifact Version. This trades some sharing convenience for a bounded exposure window appropriate to enterprise-intranet content.
