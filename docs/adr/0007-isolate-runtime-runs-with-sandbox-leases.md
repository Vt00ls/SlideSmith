# Isolate runtime runs with independent sandbox leases

Each Runtime Run may acquire one time-bounded, exclusive Sandbox Lease, and neither the lease nor hidden sandbox state is reused by a later Runtime Run. Task state crosses runs only through the Task Workspace, Checkpoints, and explicit contracts; a released, expired, or revoked lease leaves no authoritative Task state behind. Infrastructure may recycle a physical sandbox only after a complete reset and under a new lease, allowing pooling without creating cross-run, cross-Task, or cross-User state leakage.
