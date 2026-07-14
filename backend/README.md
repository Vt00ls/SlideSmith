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
export SLIDESMITH_PPT_RUNNER_PROFILE=full-ppt-master
export SLIDESMITH_FULL_PPT_DEFAULT_ENABLED=false
export SLIDESMITH_FULL_PPT_PREFLIGHT_STRICT=true
export SLIDESMITH_RESOURCE_PHASE_ENABLED=true
export SLIDESMITH_RESOURCE_NETWORK_ENABLED=false
export SLIDESMITH_RESOURCE_WEB_IMAGE_ENABLED=false
export SLIDESMITH_RESOURCE_AI_IMAGE_ENABLED=false
export SLIDESMITH_RESOURCE_FORMULA_NETWORK_ENABLED=false
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

Each task locks its effective runner profile before entering runtime prepare.
The runtime manifest uses `slidesmith.runtime_manifest.v2`, and retries and
worker recovery read the immutable task lock instead of the current process
environment. Changing deployment configuration affects only tasks that have
not yet locked a profile.

The split full PPT Master phases read the workspace manifest and
`skills/ppt-master/SKILL.md`; `/opt/ppt-master` is only a runtime fallback when a
workspace script is missing.

`SLIDESMITH_PPT_RUNNER_PROFILE` accepts:

- `real-lite`: explicit smoke/fallback path; reads uploaded source text and confirmation values before generating specs, SVG, and PPTX.
- `smoke`: fixed runtime/export health check deck.
- `full-ppt-master` (or alias `full`): standard main-route request. With `SLIDESMITH_FULL_PPT_DEFAULT_ENABLED=true`, it runs independent strategist spec, executor SVG, quality, export, and platform publish phases. It never falls back after a task is locked.

`SLIDESMITH_FULL_PPT_DEFAULT_ENABLED=false` is the rollout/rollback gate and
maps only newly locked full requests to `real-lite`. Production must keep
`SLIDESMITH_FULL_PPT_PREFLIGHT_STRICT=true`; missing compose, skill references,
scripts, Python, imports, writable workspace, or locked template roots then
fails at `source_prepare.full_runtime_preflight` before confirmation.

Full main tasks run `image_acquire` as a required worker phase between the
approved spec and SVG execution. It writes a hash-bound resource policy,
requirements snapshot, and canonical `.slidesmith/resources_manifest.json`.
SVG execution remains blocked until paths, hashes, MIME types, limits,
fallbacks, and resource-use bindings pass validation. Disabling the resource
phase fails a locked full task instead of silently skipping it. Web, AI, and
network-backed formula paths additionally require confirmation, deployment
switches, and an allowlisted provider; all are offline by default.

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
GET  /api/tasks/{id}/resources
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
spec_generating -> image_acquiring -> svg_generating -> quality_checking -> exporting -> publishing -> completed
```
