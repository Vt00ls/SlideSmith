# PPT Master Runtime Image

This image extends an `agent-compose` guest image with PPT Master and the system
dependencies needed for source conversion, SVG finalization, and PPTX export.

Build the base guest image first from the `agent-compose` repository:

```bash
docker build \
  -t agent-compose-guest:latest \
  -f /Users/vt/Dev_space/chaitin_opensource/agent-compose/guest-images/Dockerfile.agent-compose-guest \
  /Users/vt/Dev_space/chaitin_opensource/agent-compose
```

Build this runtime image from the SlideSmith repository:

```bash
DOCKER_BUILDKIT=1 docker build \
  -t slidesmith/ppt-master-runtime:dev \
  --build-context ppt_master=/Users/vt/Dev_space/ppt-master \
  -f runtime/ppt-master-runtime/Dockerfile \
  .
```

If the host Docker installation does not have buildx/BuildKit, vendor the PPT
Master runtime tree into the build context and use the bundled Dockerfile:

```bash
mkdir -p runtime/ppt-master-runtime/ppt-master
rsync -a --delete \
  --exclude='.git/' \
  --exclude='examples/' \
  --exclude='projects/' \
  --exclude='**/__pycache__/' \
  /Users/vt/Dev_space/ppt-master/ \
  runtime/ppt-master-runtime/ppt-master/
docker build \
  -t slidesmith/ppt-master-runtime:dev \
  -f runtime/ppt-master-runtime/Dockerfile.bundled \
  .
```

To refresh the vendored PPT Master tree on a host that already has a runtime
image but cannot currently rebuild the agent-compose guest base, tag the
existing image as a base and use the rebundle Dockerfile. This path also installs
the Office/Pandoc/Poppler/font packages if they are missing from the base:

```bash
docker tag slidesmith/ppt-master-runtime:dev slidesmith/ppt-master-runtime:base
docker build \
  -t slidesmith/ppt-master-runtime:dev \
  -f runtime/ppt-master-runtime/Dockerfile.rebundle \
  .
```

For a disk-constrained smoke host where only Markdown mock export is needed,
skip large Office/Pandoc/Poppler packages:

```bash
DOCKER_BUILDKIT=1 docker build \
  -t slidesmith/ppt-master-runtime:dev \
  --build-arg INSTALL_OFFICE_DEPS=false \
  --build-arg PY_DEPS_PROFILE=mock \
  --build-context ppt_master=/Users/vt/Dev_space/ppt-master \
  -f runtime/ppt-master-runtime/Dockerfile \
  .
```

Smoke validation:

```bash
docker run --rm slidesmith/ppt-master-runtime:dev bash -lc '
  node -v &&
  npm -v &&
  python3 --version &&
  agent-compose-runtime --help >/tmp/runtime-help.txt &&
  python3 /opt/ppt-master/skills/ppt-master/scripts/project_manager.py --help &&
  python3 /opt/ppt-master/skills/ppt-master/scripts/svg_to_pptx.py --help >/tmp/svg-to-pptx-help.txt &&
  soffice --version &&
  pandoc --version &&
  pdftoppm -v
'
```
