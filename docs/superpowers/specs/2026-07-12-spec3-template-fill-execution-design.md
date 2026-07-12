# SPEC3 Template Fill Execution Design

## Status

Approved for implementation on `codex/spec-02-source-intake` on 2026-07-12.
The authoritative feature plan remains:

`/Users/vt/Documents/MyWorkSpace/Projects/SlideSmith/plan/SPEC-03-Template-Fill-PPTX.md`

This document resolves preflight conflicts between that plan, the completed
SPEC2 branch, and the real `ppt-master` Template Fill CLI. It narrows unsafe
claims without changing the core workflow.

## Product scope

- SPEC3 implements only the `template-fill` route.
- Template Fill v1 accepts exactly one `.pptx` template and at least one
  readable content source. Other OOXML presentation formats remain valid
  SPEC2 source-intake inputs, but Template Fill input discovery rejects them
  at `template_fill_plan.inputs` with their filenames and presentation count.
- `beautify` remains blocked for SPEC4.
- The uploaded `.pptx` is the native Template Fill template. The existing
  catalog-template selection remains unchanged in this SPEC to avoid changing
  the main-route creation contract; Template Fill UI copy must clarify that
  the uploaded PPTX drives this workflow.
- Template Fill never invokes the SVG import/finalize/export toolchain.

## Route and phase architecture

The runtime sequence is:

```text
route_select
  -> source_prepare
  -> template_fill_plan
  -> awaiting_template_fill_confirm
  -> template_fill_check
  -> template_fill_apply
  -> template_fill_validate
  -> publish
  -> completed
```

Template Fill phases are registered without changing the existing main-route
`NextPipelinePhase` chain. Route-aware phase ordering is used where an ordered
sequence is needed; the main route remains unchanged. Planning, checking,
applying, validating, and the user gate all support cancellation.

`route_policy.go` and its existing SPEC2 tests are part of the workflow wiring
task even though the source plan omitted them from that task's file list.

## Input and plan contracts

- `fill_plan.source_pptx` is serialized as the project-relative path
  `sources/<name>.pptx`. Validation canonicalizes it and compares it with the
  unique discovered input.
- A content source must be readable (`.md`, `.markdown`, `.txt`, `.text`,
  `.csv`, or `.tsv`). An archived-only `.xls` does not satisfy this contract.
- Paths are deterministic, contained by the project/workspace, regular, and
  non-symlinked.
- The generated deck readback `<template-stem>.md` is excluded. If the original
  source manifest proves that a same-stem Markdown file was explicitly
  uploaded, it remains a business content source.
- Every plan slide must reference a `source_slide` present in the slide
  library, even when the slide has no edits. This closes a gap where the
  upstream checker can pass an invalid source slide and the applier fails.
- Empty `svg_output/` and `svg_final/` directories created by project
  initialization are allowed; Template Fill contracts reject SVG files and
  main-route `design_spec.md`/`spec_lock.md` outputs.
- Strict JSON readers return missing/corrupt-file errors. They do not reuse the
  existing permissive `readJSONMap` helper.

## Runtime safety

- `template-fill-check` removes any old check report before invoking the
  upstream CLI. Exit 1 is normalized to a successful user gate only when a new,
  valid `template_fill_pptx_check.v1` report was produced.
- Apply and validate run with `check=False`, inspect return codes explicitly,
  and raise catchable errors so runtime status becomes failed rather than
  remaining active.
- Apply never passes `--force`. It snapshots matching exports and requires a
  newly created timestamped `.pptx` from the current invocation.
- Validate removes stale validation outputs first and requires newly written,
  non-empty readback and report files with zero errors.
- Runtime tests isolate all workspace state paths; the external
  `/Users/vt/Dev_space/ppt-master` checkout remains read-only.

## Plan gate and API consistency

- Plan saves are atomic: validate a temporary candidate, then replace the
  canonical plan. Invalid JSON/contracts leave the last valid plan untouched.
- Saving forces `status = "draft"` and removes the old check report so its
  errors cannot be mistaken for the new plan.
- “Check plan” always checks a draft and always returns to the user gate.
  Only “Confirm and export” writes `status = "confirmed"` and can advance to
  apply after an error-free check.
- Confirm and regenerate compensate filesystem changes if the task-state
  transition fails.
- Wrong route/status is consistently an HTTP 400 under the repository's
  existing handler convention; not found remains 404.
- All five Template Fill endpoints receive service, handler, and router
  coverage.

## Frontend behavior

- Unsaved JSON sets a dirty state. Check and confirm are disabled with a clear
  “save first” hint; no action silently uses stale text.
- After save, the stale check report is absent and confirm may queue the
  server-side confirmed check.
- Retry actions are phase-aware. `template_fill_plan.inputs` exposes only
  prepare/plan recovery and explains that multiple uploaded PPTX files require
  a corrected task because SPEC3 has no source deletion API.
- Completed Template Fill tasks open the plan page or download the PPTX rather
  than entering the SVG preview.
- Template Fill artifacts are not hidden by the existing eight-item truncation.
- The plan page displays `why_fit`, risk, notes presence, edits, and tolerant
  ERROR/WARN report rows. It has narrow-screen layout coverage.
- Executable frontend regressions are run explicitly in acceptance; typecheck
  alone is not treated as UI behavior verification.

## Verification strategy

Each task follows RED/GREEN TDD and receives a task-scoped review. Final
verification includes:

- all backend tests and `go vet`;
- frontend executable UI regressions, typecheck, and build;
- Python unit tests and real `.pptx + content.md` check/apply/validate smoke;
- main-route and beautify regressions;
- a whole-branch review with no open Critical or Important findings.

Live browser/API/worker/agent-compose UAT is reported separately if the local
environment cannot run it; it is never silently claimed.
