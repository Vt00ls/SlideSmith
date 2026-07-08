# SlideSmith MVP Runtime Smoke

SlideSmith is validating a runtime architecture where the platform owns task
state and artifacts, while agent-compose owns isolated execution.

## Runtime Boundary

- agent-compose runs the sandbox and captures run logs.
- PPT Master runs inside the sandbox.
- SlideSmith reads business status and published artifacts.

## Workflow

1. Upload user source material.
2. Prepare a PPT Master project.
3. Wait for confirmation.
4. Generate SVG slides.
5. Export a `.pptx`.

## Acceptance

The smoke succeeds when `svg_final/*.svg` and `exports/*.pptx` exist and can be
published back to platform storage.

