# PROTOTYPE — Task Orchestration interface comparison

This is throwaway decision evidence for
[Vt00ls/SlideSmith#19](https://github.com/Vt00ls/SlideSmith/issues/19). It is
not production code and must not be imported by the backend.

## Question

Can one small Task Orchestration interface express all three enterprise V1
Routes, Confirmation Gates, Phase Run history with zero or more Runtime Runs,
fixed release locks, retry/cancel/recovery, C04 commit evidence, and
post-publication manual edit without exposing handlers, workers, Agent Compose,
or filesystem paths? The prototype compares a command/decision boundary with
an event/enactment boundary under the same in-memory state machine and failure
scenarios.

Run the interactive prototype from `backend/`:

```bash
go run ./cmd/task-orchestration-prototype
```

Run the repeatable comparison used as decision evidence:

```bash
go run ./cmd/task-orchestration-prototype -scenario compare
```

The model deliberately has no persistence, tests, production adapters, schema,
or compatibility layer. Every action renders the full relevant state.
