# Route content reads through mutually exclusive owner, Share Link, and break-glass paths

Every User-content read must use exactly one `WorkspaceOwnerPrincipal`, `SharePrincipal`, or `BreakGlassPrincipal`. `AdministratorPrincipal` grants metadata and operational authority only; it cannot read content or impersonate an Owner. `Identity & Ownership` remains the authorization facade, while internal Sharing and BreakGlass components own their distinct grant, verification, generation, expiry, revocation, rate-limit, and audit protocols. All three paths converge only after policy validation at an authorization-first, mandatory-audit content seam that opens an intent-bound `Durable Object` read handle.

A Share Link is one terminally revocable grant to one immutable Artifact Version. It has a seven-day default lifetime and a thirty-day platform hard maximum, cannot be extended after issuance, uses a high-entropy link token plus a separately verified high-entropy Access Code, and stores no recoverable plaintext secrets. Code rotation advances the grant generation and invalidates every Verification Session. User disable or identity rebind, target deletion or unavailability, Workspace purge, and recovery-epoch advancement terminally invalidate affected grants; reactivation, exact byte repair, or restore never resurrects them.

Break-glass requires two distinct active Platform Administrators, a reason and incident reference, an exact target and closed read intent, recent enterprise authentication, mandatory audit, and a non-renewable grant that defaults to thirty minutes and cannot exceed sixty minutes. A one-shot Workspace Export acceptance may create a separately fenced machine operation, but it does not grant interactive Workspace browsing and cannot authorize purge.

## Considered options

- Combining owner, share, and administrator roles in one generic principal was rejected because permission union and reusable repositories would make privilege escalation and accidental impersonation difficult to exclude.
- Allowing a Platform Administrator to self-approve content access was rejected because the role already has broad operational authority and content inspection needs independent accountability.
- Long-lived signed URLs, public CDN caching, or TTL-only authorization caches were rejected because they create a revocation and expiry bypass after the Platform Control Plane has withdrawn access.
- Permanent Share Links, in-place lifetime extension, plaintext or recoverable Access Codes, permanent verification lockout, and restored pre-incident grants were rejected because they widen or resurrect content exposure without a new owner decision.

## Consequences

- Owner, share, administrator metadata, and break-glass route groups and application interfaces remain separate. Protected repositories require typed scopes and place the authorization predicate in the authoritative query or transaction.
- Every content handle is created only after the exact business object is resolved and mandatory audit commits. A stream that already linearized may complete, but every new handle and Range request reauthorizes current generations, expiry, target availability, and recovery epoch.
- Verification Sessions are server-side, short-lived, generation-bound capabilities. Verification is rate-limited across candidate link, grant, and network dimensions, and unknown, wrong, expired, revoked, or inaccessible grants use non-leaking public behavior.
- Shared HTTP caches and origin-blind delivery are unavailable in V1. Verified byte caches remain permitted behind the authorization gate, and active content defaults to attachment or an isolated rendering origin.
- Share and break-glass lifecycle, verification, access, revocation, and suppression facts are retained authoritative audit records. Secrets, content, and physical locators never enter audit or telemetry.
- Migration hard-replaces global Task-ID and path-based content access; no optional authorization facade or legacy Share Grant is retained.
