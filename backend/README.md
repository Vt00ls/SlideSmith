# SlideSmith Backend

MVP backend for task state, uploads, confirmations, artifacts, and the
`agent-compose` runtime adapter.

## Local Run

```bash
cd /Users/vt/Dev_space/slidesmith/backend
go mod tidy
go run ./cmd/server
```

Defaults:

- HTTP: `:8080`
- Database: SQLite at `data/slidesmith.db`
- Storage: local disk at `storage/`
- Agent Compose: disabled unless `SLIDESMITH_AGENT_COMPOSE_ENABLED=true`

## Runtime Adapter

The backend integrates with `agent-compose` through CLI JSON only. It does not
import `agent-compose` internal Go packages.

Useful environment variables:

```bash
export SLIDESMITH_AGENT_COMPOSE_ENABLED=true
export SLIDESMITH_AGENT_COMPOSE_HOST=http://127.0.0.1:7410
export SLIDESMITH_AGENT_COMPOSE_FILE=../runtime/ppt-master-agent/agent-compose.yml
export SLIDESMITH_AGENT_COMPOSE_WORKDIR=../runtime/ppt-master-agent
export SLIDESMITH_AGENT_COMPOSE_WORKSPACE_ROOT=../runtime/ppt-master-agent/task-workspaces
export SLIDESMITH_AGENT_COMPOSE_RUNTIME_IMAGE=slidesmith/ppt-master-runtime:dev
export SLIDESMITH_PPT_MASTER_SKILL_DIR=/Users/vt/Dev_space/ppt-master/skills/ppt-master
export SLIDESMITH_AGENT_COMPOSE_SESSION_ROOT=/root/slidesmith-agent-compose-data
export SLIDESMITH_PPT_RUNNER_PROFILE=real-lite
```

`SLIDESMITH_AGENT_COMPOSE_WORKDIR` is the seed directory that contains
`scripts/` and `workflows/`. Each task gets its own generated workspace under
`SLIDESMITH_AGENT_COMPOSE_WORKSPACE_ROOT` by default. The generated workspace is
the primary source for PPT Master runtime assets:

```text
skills/ppt-master/SKILL.md
skills/ppt-master/scripts/
.slidesmith/runtime_manifest.json
.slidesmith/skill_lock.json
```

The full PPT Master prompt reads the workspace manifest and
`skills/ppt-master/SKILL.md`; `/opt/ppt-master` is only a runtime fallback when a
workspace script is missing.

`SLIDESMITH_PPT_RUNNER_PROFILE` accepts:

- `real-lite`: platform default; reads uploaded source text and confirmation values before generating specs, SVG, and PPTX.
- `smoke`: fixed runtime/export health check deck.
- `full-ppt-master`: uses `real-lite` only for prepare/confirmation scaffolding, then calls `agent-compose run --prompt` so the Codex agent executes the full PPT Master workflow for design spec, SVG, and PPTX generation. This requires a working agent-compose LLM provider/facade config on the daemon.

## Core API

```text
POST /api/tasks
POST /api/tasks/{id}/files
POST /api/tasks/{id}/start
GET  /api/tasks/{id}/events
GET  /api/tasks/{id}/events/stream
GET  /api/tasks/{id}/confirmations
POST /api/tasks/{id}/confirmations
GET  /api/tasks/{id}/artifacts
GET  /api/tasks/{id}/artifacts/{artifactId}/content
GET  /api/tasks/{id}/download/pptx
```

## Worker

Runtime prepare and generate work is handled by a separate worker process:

```bash
cd /Users/vt/Dev_space/slidesmith/backend
go run ./cmd/worker
```

The API moves tasks into queued business states; the worker advances:

```text
runtime_preparing -> source_converting -> awaiting_confirm
spec_generating   -> svg_generating -> quality_checking -> exporting -> publishing -> completed
```
