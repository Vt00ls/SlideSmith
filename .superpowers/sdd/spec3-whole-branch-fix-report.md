# SPEC3 Whole-Branch Fix Report

Date: 2026-07-13 (Asia/Shanghai)

## Outcome

PASS. All four Important and four Minor findings from
`.superpowers/sdd/spec3-whole-branch-review.md` were fixed as one TDD wave.
The implementation remains on `codex/spec-02-source-intake`; no push, pull
request, deployment, or external-checkout write was performed.

Starting HEAD: `9d61a8732d12b27177545c4bd0273ee70ca03f24`.

Code commits:

- `d254af89f0b98a540436584ca53480b705b75080` — same-stem provenance,
  cancellable draft checks, and exact publish rollback.
- `d71768298103be000763aebea638ce86dddab0be` — task-detail request lifecycle,
  SPEC4 Beautify copy, and ignored external-tree smoke protection.

## Finding resolution

### A. Same-stem source provenance

The canonical source manifest is now snapshotted as an in-memory provenance
value containing the authoritative manifest SHA-256 and exact canonical source
size/SHA-256 bindings. Candidate discovery receives that value explicitly and
revalidates contained, regular, non-symlinked candidate source files. Workspace
manifest data is not copied into API candidates; candidate-authored manifests
are rejected; authoritative manifest and source changes fence promotion.

Service regressions cover Save, Check, Confirm, Regenerate, direct candidate
discovery, candidate-manifest injection, candidate source mutation, and the
accurate retry equivalent: retry cleanup preserves provenance, the worker is
modeled returning a regenerated draft to the gate, and a real subsequent Save
candidate succeeds.

RED: Save/Check/Confirm/Regenerate failed with `requires content source` for
explicit `brand.pptx + brand.md`; candidate-authored provenance was accepted;
candidate source mutation had no provenance fence.

GREEN:

```bash
cd backend
go test ./internal/service -run 'TestTemplateFillAPICandidatesPreserveExplicitSameStemMarkdownProvenance|TestTemplateFillCandidateDiscoveryUsesAuthoritativeProvenance|TestTemplateFillRetryPreservesProvenanceForSubsequentAPICandidate|TestCheckTemplateFillPlanRejectsCandidateAuthoredProvenance|TestCheckTemplateFillPlanRejectsCandidateSourceMutationAgainstProvenance' -count=1 -v
```

All selected tests passed.

### B. Cancellation and claim fencing

`CheckTemplateFillPlan` retains its durable execution claim but releases
`template-fill-api.lock` before `runAgent`. It reacquires the lock afterward,
reloads the task, verifies status and claim ownership, and revalidates source
provenance plus the canonical draft SHA before promotion.

RED: all three blocking-agent regressions exceeded the 500 ms lock bound.

GREEN:

```bash
cd backend
go test ./internal/service -run 'TestCheckTemplateFillPlanReleasesAPILockForPromptCancellationAndFencesLateResult|TestCheckTemplateFillPlanReleasesAPILockButClaimRejectsConcurrentSaveAndConfirm|TestCheckTemplateFillPlanReacquiresLockAndRevalidatesBeforePromotion' -count=1 -v
```

All three passed. Cancellation returned while the agent remained blocked; the
late check failed without changing the canonical plan/report; concurrent Save
and Confirm failed promptly through the claim; the relocked check preserved a
newer canonical plan.

### C. Task-detail lifecycle and frontend minors

`TaskDetailPage` is keyed by task ID. Every load activates a task/hash-scoped
generation and produces one atomic snapshot containing task, events,
artifacts, runtime runs, phase runs, and the optional plan preview. Older or
wrong-route generations cannot commit. StrictMode cleanup invalidates the
first setup while the second setup remains usable. Retry revalidates current
route, hash, prop task ID, and loaded task ID before issuing the API request.

The detail page requests the plan only for the backend-readable statuses.
Template Fill deep links canonicalize non-Template-Fill tasks back to detail,
and Regenerate additionally requires `task.route === "template-fill"`.

RED: the new executable delayed A/B, overlapping poll, component-key,
readable-status, retry-scope, and deep-link checks failed against the old
component.

GREEN:

```bash
cd frontend
npm test
npm run typecheck
```

The executable suite passed 23/23 and typecheck passed.

### D. Publish rollback

`RuntimeWorkspacePublisher` now records each exact requested object key before
copying and rolls all attempted keys back internally on any partial error,
including write-then-error and cancellation. Each service-level root attempt
is cleanup-armed before publisher invocation. It records successful publisher
keys, contract keys before contract copy, and preassigned database IDs before
replace. Persisted rows returned by the list step are bound only when task ID
and exact attempted object key match. Cleanup uses `context.WithoutCancel`,
deletes exact IDs and exact keys, and never uses artifact `publish_version` or
an object-key prefix. The attempt is disarmed only after persisted contract
validation and the publish event succeed.

RED: 8/9 fault cases leaked objects; only the already-compensated DB-list case
passed. Failures covered publisher write-then-error, missing required artifact,
publish/final contract writes, publish/final contract copies, DB replace, and
request cancellation.

GREEN:

```bash
cd backend
go test ./internal/service -run '^(TestRuntimeWorkspacePublisherRollsBackExactObjectsOnWriteThenError|TestTemplateFillPublishRejectsMissingKindBeforePersistence|TestTemplateFillPublishRollsBackPreDBContractFailures|TestTemplateFillPublishRollsBackExactAttemptOnDBReplaceFailure|TestTemplateFillPublishRollsBackExactAttemptOnDBListFailure|TestTemplateFillPublishCleanupIgnoresCancelledRequestContext)$' -count=1 -v
go test ./internal/service -run '^TestTemplateFillPublish' -count=1
go test ./internal/service -run '^TestRuntimeWorkspacePublisher' -count=1
```

All passed, including existing post-DB count/final-contract mutation,
publish-version mutation, joined cleanup-error, and previous-version
preservation regressions.

### E. Beautify copy

The visible blocked-workflow message now says the full Beautify workflow is
deferred to SPEC-04. `WorkflowExecutable=false`, the failure phase,
`NextSpec=SPEC-04-Beautify-PPTX.md`, and main-route behavior are unchanged.

RED: the focused policy test observed the stale SPEC-02 copy. GREEN:

```bash
cd backend
go test ./internal/service -run '^(TestRouteExecutionPolicyAllowsBeautifyIntakeButBlocksWorkflow|TestProcessPrepareDispatchesRouteAfterSourcePrepare)$' -count=1
```

### F. Ignored external tree

The smoke guard now snapshots the exact external `skills/ppt-master` subtree,
including ignored files and excluding `.git`. Node type, size, and content
SHA-256 are compared. The existing exact fixture, external Git-status,
`__pycache__`, and repository-state checks remain separate. Both the new tree
and pycache traversal are scoped to the skill subtree, so the unrelated large
`examples` checkout is not scanned or hashed.

RED: ignored same-size log/cache/projects mutations left Git status unchanged
and escaped the old guard; the exhaustive-finalizer regression lacked the
skill-tree difference. GREEN:

```bash
cd runtime/ppt-master-agent/scripts
python3 -m unittest test_ppt_runner.RealTemplateFillSafetyGuardTests
```

All 5 safety-guard tests passed, including preservation of the primary test
failure while every safety component is still compared.

## Post-commit full gates

Backend:

```bash
cd backend
go test ./... -count=1
go vet ./...
```

PASS. Handler, repository, router, and service packages passed; vet emitted no
diagnostics.

Python:

```bash
cd runtime/ppt-master-agent
PYTHONDONTWRITEBYTECODE=1 python3 scripts/test_ppt_runner.py
SLIDESMITH_RUN_REAL_TEMPLATE_FILL_SMOKE=1 \
SLIDESMITH_TEMPLATE_FILL_SMOKE_PPTX=/Users/vt/Dev_space/ppt-master/examples/ppt169_kubernetes_blueprint_2026/exports/kubernetes_blueprint_2026.pptx \
SLIDESMITH_TEMPLATE_FILL_SMOKE_CONTENT=/Users/vt/Dev_space/ppt-master/examples/ppt169_kubernetes_blueprint_2026/sources/kubernetes_architecture.md \
PYTHONDONTWRITEBYTECODE=1 \
python3 scripts/test_ppt_runner.py RealTemplateFillRunnerSmokeTests.test_check_apply_validate
```

PASS. Full suite: 34 tests, 3 expected gated skips. Dedicated real smoke: 1/1;
`validate_errors=0`, 1 slide, 32,119-byte generated PPTX, 569-byte readback.

Frontend:

```bash
cd frontend
npm test
npm run typecheck
npm run build
```

PASS. Executable regressions: 23/23. Typecheck and Vite production build both
passed.

Repository hygiene:

```bash
git diff --check
git status --short --branch
```

PASS after the two code commits; tracked worktree was clean.

## External read-only evidence

External checkout final revision:
`3e8cd06a74e4280272177e1cc37a082741c927c3`.
`git status --porcelain=v1 --untracked-files=all` was empty.

The exact `skills/ppt-master` content digest before and after the real smoke was
unchanged:

```text
5e71e258a54615c52da2fd13052073e7bd64fb71316c3b0d47dcc7cb80afac0f
```

The real-smoke fixtures retained their approved sizes and SHA-256 values:

```text
105402 bytes  ddcaef381c298e0c5f4d1c636731044ed513e30166c07e79dae70ff5896227a3  kubernetes_blueprint_2026.pptx
12239 bytes   c823b8ff8e5733d7e0a778256cd0d03cf7b71da3cd8bb2cf15a70d5431d72906  kubernetes_architecture.md
```

## Self-review and remaining concerns

All eight formal findings were rechecked against the final diff. Main-route
phase behavior, the approved plan-page lease and `replaceState`, Template Fill
canonical-only publishing, and the blocked Beautify workflow remain intact.

Remaining concerns: **0**.
