# Runtime Smoke

This smoke validates the first SlideSmith milestone: PPT Master runs inside
`agent-compose` and produces artifacts that the platform can publish.

Last verified: 2026-07-07 on test server `10.2.37.236` as `root`.

Successful run:

- Agent Compose daemon container: `slidesmith-agent-compose`
- Runtime image: `slidesmith/ppt-master-runtime:dev`
- Run ID: `run-ppt_master-9d82e256da27`
- Session ID: `7278d782-6f84-463f-a47f-6cf213b5a013`
- Status: `succeeded`
- Exit code: `0`
- Workspace host path:
  `/root/slidesmith-agent-compose-data/sessions/7278d782-6f84-463f-a47f-6cf213b5a013/workspace`
- PPTX:
  `projects/smoke_deck_ppt169_20260707/exports/smoke_deck_20260707_085519.pptx`
- PPTX validation: `slides=3`, `size=32390`
- SHA-256:
  `47777d106e53a2c78a92e8567ffd3f8dd8ea5ada25537c6470c72189713815b2`
- SVG quality check: 3 passed, 0 warnings, 0 errors.

The product-level report is stored at:

```text
/Users/vt/Documents/MyWorkSpace/Projects/SlideSmith/runtime-smoke-report.md
```

## Build Images

Build the base `agent-compose` guest image:

```bash
docker build \
  -t agent-compose-guest:latest \
  -f /Users/vt/Dev_space/chaitin_opensource/agent-compose/guest-images/Dockerfile.agent-compose-guest \
  /Users/vt/Dev_space/chaitin_opensource/agent-compose
```

Build the SlideSmith PPT Master runtime image:

```bash
DOCKER_BUILDKIT=1 docker build \
  -t slidesmith/ppt-master-runtime:dev \
  --build-context ppt_master=/Users/vt/Dev_space/ppt-master \
  -f runtime/ppt-master-runtime/Dockerfile \
  .
```

On a disk-constrained smoke host, use the lightweight build. It validates
Markdown mock export, not full PDF/Office conversion:

```bash
DOCKER_BUILDKIT=1 docker build \
  -t slidesmith/ppt-master-runtime:dev \
  --build-arg INSTALL_OFFICE_DEPS=false \
  --build-arg PY_DEPS_PROFILE=mock \
  --build-context ppt_master=/root/slidesmith-deps/ppt-master \
  -f runtime/ppt-master-runtime/Dockerfile \
  .
```

If the host Docker installation does not have buildx/BuildKit, copy the PPT
Master runtime tree into the build context and use the bundled Dockerfile:

```bash
mkdir -p runtime/ppt-master-runtime/ppt-master
rsync -a --delete \
  --exclude='.git/' \
  --exclude='examples/' \
  --exclude='projects/' \
  --exclude='**/__pycache__/' \
  /root/slidesmith-deps/ppt-master/ \
  runtime/ppt-master-runtime/ppt-master/
docker build \
  -t slidesmith/ppt-master-runtime:dev \
  -f runtime/ppt-master-runtime/Dockerfile.bundled \
  .
```

If the host already has a working full runtime image and only the vendored PPT
Master tree needs to be refreshed, use:

```bash
docker tag slidesmith/ppt-master-runtime:dev slidesmith/ppt-master-runtime:base
docker build \
  -t slidesmith/ppt-master-runtime:dev \
  -f runtime/ppt-master-runtime/Dockerfile.rebundle \
  .
```

## Run With Agent Compose

From `runtime/ppt-master-agent`:

```bash
agent-compose up
agent-compose run ppt_master --command "node workflows/ppt_workflow.js validate-env --profile mock" --json
agent-compose run ppt_master --command "node workflows/ppt_workflow.js prepare --profile smoke --input samples/input.md --project smoke_deck" --json
agent-compose run ppt_master --command "node workflows/ppt_workflow.js generate --profile smoke --project smoke_deck --confirmation mock" --json
agent-compose logs --follow
agent-compose ps --all --json
```

If local workspace snapshots do not persist across runs, use one command for
the first smoke:

```bash
agent-compose run ppt_master --command "node workflows/ppt_workflow.js prepare --profile smoke --input samples/input.md --project smoke_deck && node workflows/ppt_workflow.js generate --profile smoke --project smoke_deck --confirmation mock" --json
```

On the test server, run through the daemon container:

```bash
docker exec -w /data/work slidesmith-agent-compose \
  agent-compose --host http://127.0.0.1:7410 \
  -f /data/work/agent-compose.yml up --json

docker exec -w /data/work slidesmith-agent-compose \
  agent-compose --host http://127.0.0.1:7410 \
  -f /data/work/agent-compose.yml \
  run ppt_master \
  --command "node workflows/ppt_workflow.js prepare --profile smoke --input samples/input.md --project smoke_deck && node workflows/ppt_workflow.js generate --profile smoke --project smoke_deck --confirmation mock" \
  --json
```

For the platform MVP path, use `real-lite` instead of `smoke`. `real-lite`
still executes through `agent-compose run --command`, but it reads uploaded
source text and the confirmed options before generating `design_spec.md`,
`spec_lock.md`, `svg_output/*.svg`, and `exports/*.pptx`:

```bash
agent-compose run ppt_master --command "node workflows/ppt_workflow.js prepare --profile real-lite --input samples/input.md --project real_lite_deck" --json
agent-compose run ppt_master --command "node workflows/ppt_workflow.js generate --profile real-lite --project real_lite_deck --confirmation mock" --json
```

For the full PPT Master path, enable the requested profile and rollout gate:

```bash
export SLIDESMITH_PPT_RUNNER_PROFILE=full-ppt-master
export SLIDESMITH_FULL_PPT_DEFAULT_ENABLED=true
export SLIDESMITH_FULL_PPT_PREFLIGHT_STRICT=true
```

Prepare uses `--profile full-ppt-master`. Generation is never one monolithic
prompt: the backend creates independent `spec_generate`, `svg_execute`,
`quality_check`, `finalize_export`, and platform `publish` runs. Export does not
invoke `ppt_runner.py publish`.

Before running an end-to-end task in that mode, validate that the daemon-side
Codex/LLM provider is configured:

```bash
agent-compose run ppt_master \
  --prompt "In the current workspace, create .slidesmith/prompt_probe.txt containing the single word ok, then stop. Do not modify anything else." \
  --json
```

If the probe fails with `decode agent result for codex`, `no result payload`, or
transport timeout messages, fix the agent-compose daemon LLM facade/provider
configuration before debugging SlideSmith.

On the current test server, Codex prompt mode requires the daemon container to
run with the upstream provider config from `/root/.codex/config.toml` and the
root environment:

```text
HTTP_LISTEN=0.0.0.0:7410
AUTH_USERNAME=admin
AUTH_PASSWORD=<random internal password>
AUTH_SECRET=<random internal secret>
AGENT_COMPOSE_RUNTIME_BASE_URL=http://slidesmith-agent-compose:7410
LLM_API_ENDPOINT=https://ai-api-gateway.app.baizhi.cloud/api/openai
LLM_API_PROTOCOL=responses
LLM_API_KEY=<from root environment>
LLM_MODEL=gpt-5.5
LLM_TIMEOUT=5m
```

The daemon must be attached to the user-defined `agent-compose_default` Docker
network so guest runtime containers can resolve `slidesmith-agent-compose`.
Using `LLM_API_PROTOCOL=chat_completions` reached the upstream model but produced
an empty Codex event stream through the runtime facade on this host.

Validate the generated PPTX:

```bash
docker run --rm \
  -v /root/slidesmith-agent-compose-data/sessions/7278d782-6f84-463f-a47f-6cf213b5a013/workspace:/workspace \
  -w /workspace \
  slidesmith/ppt-master-runtime:dev \
  python3 -c "from pathlib import Path; from pptx import Presentation; p=sorted(Path('projects').glob('smoke_deck_ppt169_*/exports/*.pptx'))[-1]; prs=Presentation(p); print(p); print('slides=%d' % len(prs.slides)); print('size=%d' % p.stat().st_size)"
```

## Expected Artifacts

Runtime workspace should contain:

```text
.slidesmith/status.json
.slidesmith/events.ndjson
.slidesmith/artifacts.json
projects/smoke_deck/design_spec.md
projects/smoke_deck/spec_lock.md
projects/smoke_deck/.slidesmith/resource_plan.json
projects/smoke_deck/.slidesmith/resource_policy.json
projects/smoke_deck/.slidesmith/resources_manifest.json
projects/smoke_deck/analysis/resource_requirements.json
projects/smoke_deck/svg_output/*.svg
projects/smoke_deck/svg_final/*.svg
projects/smoke_deck/exports/*.pptx
```

## Scope

The `smoke` generation mode validates the runtime and PPT Master export chain
with fixed pages. The `real-lite` generation mode validates source-driven
content flow without using a long-running Codex prompt. The `full-ppt-master`
mode validates the task-locked, split agent path. Each phase reads
`.slidesmith/runtime_manifest.json`, loads `skills/ppt-master/SKILL.md`, and
obeys its own output contract. The platform publisher is the only publish
entrypoint. `/opt/ppt-master` remains a runtime fallback, not the primary skill
source.

Run the deterministic full-main plus four-resource-fixture SPEC-05 contract
smoke locally with:

```bash
python3 runtime/ppt-master-agent/scripts/full_main_smoke.py
```

The test server has limited free disk under `/`; keep using
`Dockerfile.smoke`, `INSTALL_OFFICE_DEPS=false`, and `PY_DEPS_PROFILE=mock`
until a larger host or disk cleanup is available.
