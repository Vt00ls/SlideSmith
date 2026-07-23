# Architecture Decision Authority

This policy governs Wayfinder and architecture-decision work in this repository. It delegates derived and reversible choices to agents while keeping product authority, irreversible consequences, and high-impact security or operational policy with the User.

It applies when the User asks an agent to resolve an architecture ticket autonomously or invokes the `resolve-architecture-decisions` skill. It does not silently broaden an ordinary implementation, review, or diagnostic request.

## Operating modes

### Batch mode (default)

- Resolve Class A and Class B decisions without interrupting the User.
- Pause only for a Class C decision.
- When no Class C decision remains, present one complete resolution for final confirmation.
- Do not write the resolution, close issues, commit, or push until that final confirmation.

### Delegated mode

- Resolve Class A and Class B decisions without interrupting the User.
- Pause only for a Class C decision.
- If no Class C decision remains, record and publish the resolution using the automatic actions allowed below without a final confirmation.
- The User must explicitly request delegated mode for the current ticket or give an equivalent terminal instruction.

### Serial grilling mode

- Ask every decision one at a time and wait for the User.
- Use only when the User explicitly invokes `grilling`, requests a question-by-question interview, or asks to personally decide every branch.

An issue label or body that says `wayfinder:grilling` describes the need to stress-test the decision tree. When the User explicitly invokes `resolve-architecture-decisions`, that wording does not force serial questioning; the selected batch or delegated mode controls interaction.

## Decision classes

### Class A — Derived technical detail

Decide automatically when standing constraints and evidence imply the answer and reasonable alternatives do not change an external business contract.

Examples:

- idempotency, retry, fencing, and crash-reconciliation mechanics;
- typed error and fail-closed behavior already required by policy;
- fault injection and adapter contract coverage;
- cache poisoning defenses and atomic temporary-file promotion;
- internal observability projection and evidence fields;
- deterministic naming or representation details with no external compatibility impact.

### Class B — Reversible architecture choice

Decide automatically using the recommended answer when the choice is bounded, reversible, and consistent with accepted scope and ADRs. Record the chosen option, material alternatives, and reversal cost in the final resolution.

Examples:

- internal seam placement that preserves established module authority;
- private state-machine decomposition;
- internal interface naming and result shapes;
- default algorithms or policy values that remain configurable;
- test-adapter structure and replace-not-layer migration sequencing;
- performance optimizations that cannot weaken correctness, authorization, or durability.

### Class C — Human authority required

Pause and ask the smallest decision that resolves the branch when any of these conditions applies:

- changes enterprise V1 in-scope or out-of-scope behavior;
- authorizes irreversible deletion, retention reduction, lossy migration, or unrecoverable cutover;
- changes identity, ownership, authorization, administrator power, external content exposure, encryption policy, or compliance posture;
- accepts data loss, weaker fail-closed behavior, or a new trust assumption;
- supersedes or materially conflicts with an accepted ADR or confirmed Wayfinder decision;
- creates significant vendor lock-in, recurring cost, capacity commitment, or externally visible SLO trade-off;
- changes a public or cross-module business contract in a way that two reasonable stakeholders could value differently;
- lacks an authoritative fact that cannot be discovered from the repository, issue tracker, configured tools, or primary documentation.

Do not use a numerical confidence score as a substitute for this classification. If a choice is hard to reverse, changes who bears risk, or requires product preference, classify it as Class C.

## Resolution workflow

1. Read `AGENTS.md`, this policy, the target issue and comments, the Wayfinder map, native dependencies, `CONTEXT.md`, relevant ADRs and architecture documents, and the current implementation.
2. Discover facts from the environment instead of asking the User.
3. Build the complete decision tree and resolve dependencies between branches before presenting questions.
4. Classify every unresolved branch as A, B, or C and keep a private decision ledger containing evidence, recommendation, alternatives, and downstream effects.
5. Resolve A and B branches automatically.
6. Stress-test the combined design against:
   - domain vocabulary and standing decisions;
   - module authority, interface depth, locality, and the deletion test;
   - ownership, authorization, information leakage, and audit;
   - concurrency, idempotency, cancellation, crash, retry, and stale writers;
   - missing, corrupt, partial, duplicated, and inconsistent state;
   - retention, reclamation, backup, restore, and repair;
   - capacity, bytes, inodes, cost, and operational diagnostics;
   - migration, compatibility, adapter contracts, and highest-level tests.
7. If a Class C branch remains, ask one question with a recommended answer and concrete consequences. After the answer, continue automatically rather than reopening resolved A/B branches.
8. Produce one complete resolution containing the decisions, authority matrix, module/interface/seam, state and failure semantics, retention/security/test rules, rejected alternatives, downstream inputs, and remaining fog.
9. Apply the selected operating mode's confirmation and publication rules.

When a new User answer supersedes a standing decision, identify every conflicting issue, ADR, scope statement, domain definition, dependency, and downstream ticket. Preserve historical resolution comments and add an explicit superseding correction rather than rewriting history.

## Decision recording

The repository is the durable engineering authority; GitHub issues provide workflow, discussion, and traceability.

- Update `CONTEXT.md` only for resolved domain language, never implementation detail.
- Create or update ADRs only for hard-to-reverse, surprising, genuinely traded-off decisions.
- Put complete operational and interface contracts in `docs/architecture/`.
- Publish a self-contained resolution comment on the issue and link it from the Wayfinder map.
- Keep native child/dependency relationships and the textual frontier in sync.
- Create a new decision-only child ticket when confirmed scope introduces unresolved downstream policy. Do not create implementation tickets unless requested.
- Do not close a ticket with unresolved Class C branches or unnamed remaining fog that affects the first implementation SPEC.

## Automatic action authority

After batch-mode final confirmation, or immediately after a delegated-mode resolution with no unresolved Class C branch, the agent may:

- update `CONTEXT.md`, ADRs, architecture documents, and agent policy documents;
- comment on and close the resolved GitHub issue;
- add superseding correction comments to related issues;
- update the Wayfinder map and native child/dependency relationships;
- create decision-only downstream tickets required by confirmed scope;
- stage and commit only the scoped documentation and policy changes;
- push the current non-default branch after verifying the worktree and remote.

This standing authority does not permit the agent to:

- merge a pull request or push directly to the default branch;
- modify production data, credentials, infrastructure, or external archives;
- execute destructive migration, purge, retention reduction, or irreversible cleanup;
- implement product code or schema changes for a decision-only ticket;
- create implementation tickets, release artifacts, or vendor commitments without request;
- include unrelated or concurrent worktree changes in a commit;
- bypass repository checks, mandatory audit, or a failed verification gate.

If another session has modified the shared worktree, preserve its changes, stage explicit files only, and stop if the scopes overlap.

## Handoff

End with:

- the resolution and issue links;
- changed authoritative documents;
- validation performed;
- commit and push state when authorized;
- newly unblocked frontier tickets;
- any Class C decision still requiring the User.
