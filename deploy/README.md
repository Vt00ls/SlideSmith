# SlideSmith Docker Compose Deployment

Stage 6 MVP deployment runs the platform as one Docker Compose stack:

- `frontend`: nginx static frontend and `/api` reverse proxy
- `api`: SlideSmith backend API
- `worker`: task worker
- `postgres`: platform database
- `redis`: reserved queue/cache dependency for the next worker iteration
- `agent-compose`: full agent-compose daemon service
- PPT Master runtime image: `slidesmith/ppt-master-runtime:dev`

The backend still talks to agent-compose through the CLI boundary. Inside the
containers, the CLI wrapper uses Docker to execute:

```text
docker exec slidesmith-agent-compose agent-compose ...
```

No agent-compose code is imported by SlideSmith.

## Prepare Files

From the repo root:

```bash
cp deploy/.env.example deploy/.env
cp deploy/agent-compose.env.example deploy/agent-compose.env
```

Edit `deploy/.env`:

```text
SLIDESMITH_APP_DATA_ROOT=/root/slidesmith-compose/data/app
SLIDESMITH_AGENT_COMPOSE_DATA_ROOT=/root/slidesmith-compose/data/agent-compose
SLIDESMITH_AGENT_COMPOSE_HOST_SESSION_ROOT=/root/slidesmith-compose/data/agent-compose/sessions
SLIDESMITH_RUNTIME_SEED_HOST=/root/slidesmith/runtime/ppt-master-agent
SLIDESMITH_PPT_MASTER_SKILL_DIR_HOST=/root/slidesmith-deps/ppt-master/skills/ppt-master
POSTGRES_PASSWORD=<strong password>
```

Edit `deploy/agent-compose.env`:

```text
AUTH_PASSWORD=<strong password>
AUTH_SECRET=<long random secret>
```

For `full-ppt-master`, also configure the daemon LLM facade/provider:

```text
LLM_API_ENDPOINT=<provider endpoint>
LLM_API_PROTOCOL=responses
LLM_API_KEY=<provider key>
LLM_MODEL=<model>
LLM_TIMEOUT=5m
```

Use `SLIDESMITH_PPT_RUNNER_PROFILE=real-lite` explicitly for the first smoke
validation. The standard configuration requests `full-ppt-master` while
`SLIDESMITH_FULL_PPT_DEFAULT_ENABLED=false` keeps the rollout gate closed.

## Build Runtime Image

The stack expects the PPT Master runtime image to exist on the Docker host:

```bash
DOCKER_BUILDKIT=1 docker build \
  -t slidesmith/ppt-master-runtime:dev \
  --build-context ppt_master=/root/slidesmith-deps/ppt-master \
  -f runtime/ppt-master-runtime/Dockerfile \
  .
```

If BuildKit named contexts are unavailable on the host, use the bundled
Dockerfile flow documented in `docs/runtime-smoke.md`.

## Start Stack

```bash
cd deploy
docker compose --env-file .env -f docker-compose.yml build
docker compose --env-file .env -f docker-compose.yml up -d
docker compose --env-file .env -f docker-compose.yml ps
```

If Docker Hub is slow or unavailable, override these build base images in
`deploy/.env` with a reachable mirror. If dependency downloads are also slow,
override the Go and npm registries too:

```text
SLIDESMITH_GO_IMAGE=golang:1.25-alpine
SLIDESMITH_DOCKER_CLI_IMAGE=docker:27-cli
SLIDESMITH_NODE_IMAGE=node:22-alpine
SLIDESMITH_NGINX_IMAGE=nginx:1.27-alpine
SLIDESMITH_GOPROXY=https://goproxy.cn,direct
SLIDESMITH_NPM_CONFIG_REGISTRY=https://registry.npmmirror.com/
```

The app is exposed at:

```text
http://<server-ip>/
```

The agent-compose daemon port is bound to `127.0.0.1:7410` by default for local
debugging only.

## Smoke Validation

1. Open `http://<server-ip>/`.
2. Create a task and upload a Markdown file.
3. Start the task.
4. Confirm Tier 1.
5. Confirm Tier 2.
6. Wait for `completed`.
7. Verify SVG preview and PPTX download.

Expected published storage layout:

```text
tasks/{task_id}/artifacts/
  source/
  design_spec.md
  spec_lock.md
  svg_output/
  svg_final/
  exports/*.pptx
  logs/
  manifest/runtime_artifacts.json
```

## Switch To Full PPT Master

Validate prompt mode first:

```bash
docker exec -w /data/work slidesmith-agent-compose \
  agent-compose --host http://127.0.0.1:7410 \
  run ppt_master \
  --prompt "In the current workspace, create .slidesmith/prompt_probe.txt containing ok, then stop." \
  --json
```

After the probe succeeds:

```bash
sed -i 's/^SLIDESMITH_PPT_RUNNER_PROFILE=.*/SLIDESMITH_PPT_RUNNER_PROFILE=full-ppt-master/' deploy/.env
sed -i 's/^SLIDESMITH_FULL_PPT_DEFAULT_ENABLED=.*/SLIDESMITH_FULL_PPT_DEFAULT_ENABLED=true/' deploy/.env
docker compose --env-file deploy/.env -f deploy/docker-compose.yml up -d worker api
```

Run the repository full-main fixture command before production rollout. To
roll back new tasks, set `SLIDESMITH_FULL_PPT_DEFAULT_ENABLED=false` and restart
API/worker. Already locked full tasks remain full and must never be silently
rerouted to `real-lite`.

## Current MVP Limits

- No HTTPS termination.
- No public auth layer for the SlideSmith frontend/API.
- No file size policy beyond nginx `client_max_body_size`.
- No worker concurrency limiter beyond `SLIDESMITH_WORKER_BATCH_SIZE`.
- Redis is included but not yet used for queueing.
- Storage is local disk through `StorageService`; S3/OSS/MinIO adapters are
  future work.
- Workspace cleanup policy is not automated yet.

## Network-Limited Prebuilt Validation

If the server cannot pull Docker Hub build images, use the prebuilt profile.
This profile reuses the already available `ghcr.io/chaitin/agent-compose:latest`
image as the backend/worker/frontend runtime base. It is intended for smoke
validation and defaults to SQLite, so it does not require Postgres or Redis
images.

Build local artifacts first:

```bash
cd /Users/vt/Dev_space/slidesmith
mkdir -p backend/bin
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -C backend -o bin/slidesmith-server ./cmd/server
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -C backend -o bin/slidesmith-worker ./cmd/worker
npm --prefix frontend run build
```

Prepare env files:

```bash
cp deploy/.env.prebuilt.example deploy/.env.prebuilt
cp deploy/agent-compose.env.example deploy/agent-compose.env.prebuilt
```

Start on non-conflicting ports:

```bash
cd deploy
docker compose --env-file .env.prebuilt -f docker-compose.prebuilt.yml build
docker compose --env-file .env.prebuilt -f docker-compose.prebuilt.yml up -d
```

Default endpoints:

```text
frontend:      http://<server-ip>:18081/
api:           http://<server-ip>:18082/healthz
agent-compose: http://127.0.0.1:17410/
```

After this smoke passes, decide whether to stop the manually started services
and move the main Compose stack to port `80`.
