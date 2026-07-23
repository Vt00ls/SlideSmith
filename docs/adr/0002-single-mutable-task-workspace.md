# Use one mutable Task Workspace with immutable recovery and publication

Each Task has one mutable Task Workspace, managed by the execution platform and reused across Phases; per-run snapshots, mounts, and sandbox directories remain infrastructure details rather than additional domain workspaces. Each successful Phase publishes an immutable Checkpoint for recovery, while durable user-facing results are published as Artifact Versions. This avoids maintaining two long-lived mutable workspace copies while keeping recoverable execution state separate from durable business outputs.
