# SlideSmith Presentation Production

SlideSmith turns source material and presentation design constraints into editable, validated slide decks. This context defines the language used to describe deck composition and the route-specific lifecycle that produces it.

## Language

### Platform ownership and sharing

**User**:
An authenticated person who owns one Personal Workspace and the work created within it.
_Avoid_: Member, tenant user, account

**Personal Workspace**:
A User's durable private space for creating and managing Tasks and their outputs. Other Users have no implicit access to it, and its identity is never transferred or reused.
_Avoid_: User directory, Task Workspace, Sandbox Workspace, transferable workspace

**Platform Administrator**:
An authenticated operator with platform-wide operational authority but no implicit right to inspect a User's content; exceptional content access requires an explicit audited grant.
_Avoid_: Workspace owner, tenant administrator, member

**Share Link**:
A revocable, time-bounded grant that exposes one published Artifact Version without granting access to its owning Task or Personal Workspace.
_Avoid_: Public link, Task share, workspace invitation

**Access Code**:
A separate secret required to use a Share Link.
_Avoid_: Password, link token, invitation code

**Workspace Export**:
An audited administrative package delivered outside SlideSmith from one disabled User's Personal Workspace before an independently authorized purge. It does not transfer the Personal Workspace or any Task to another User.
_Avoid_: Workspace transfer, Artifact Version, backup

### Platform boundaries

**Platform Control Plane**:
The authoritative owner of identity, access, Task orchestration, release locks, publication metadata, sharing, and usage records.
_Avoid_: Execution Data Plane, runtime, worker

**Execution Data Plane**:
The isolated execution environment that performs Runtime Runs, mutates Task Workspace content, and returns execution evidence without deciding authoritative business state.
_Avoid_: Platform Control Plane, Task owner, business database

**Recovery Point**:
A validated joint recovery identity that binds one PostgreSQL point-in-time target to the exact committed durable-object and runtime-package inventories required to restore it.
_Avoid_: Database snapshot, object-store snapshot, backup job, live replica

### Usage and quota

**Usage Ledger**:
The append-only record of measured consumption and corrective adjustments owned by one Personal Workspace, with entries attributed to their originating Task, Phase Run, and Runtime Run. Moving work never rewrites its historical usage ownership.
_Avoid_: Task usage counter, quota balance, billing estimate

**Quota Reservation**:
A time-bounded hold against one Personal Workspace's available quota, associated with a single Phase Run and settled against its actual Usage Ledger entries when the attempt ends.
_Avoid_: Usage entry, quota limit, Sandbox Lease

### Work and outputs

**Task**:
A single request within a Personal Workspace to create, restyle, or fill one Deck. It owns the intent, inputs, confirmations, production route, and resulting artifacts as one lifecycle.
_Avoid_: Project, job, run

**Task Workspace**:
A Task's sole logical identity for mutable working state, reused across its Phases; its physical materialization may expire and be rebuilt from a Checkpoint. It is neither a User ownership boundary nor a durable Task output.
_Avoid_: Personal Workspace, Execution Workspace, Sandbox Workspace, run snapshot

**Task Workspace Revision**:
An authoritative immutable identity for the Task Workspace state produced by one validated mutation. Revisions order recoverable state without exposing how or where its bytes are materialized.
_Avoid_: Checkpoint, directory version, run snapshot

**Runtime View**:
An isolated, disposable view through which one Runtime Run proposes mutations from a Task Workspace Revision. Its changes are not authoritative unless validation succeeds and the view is committed.
_Avoid_: Task Workspace, Sandbox Workspace, session directory

**Checkpoint**:
An immutable recovery record created for every successful Phase Run and other validated Task Workspace commit, binding a Task Workspace Revision to only the declared recoverable Task-owned state. Different Checkpoints may share deduplicated content but remain distinct recovery identities.
_Avoid_: Artifact Version, workspace backup, directory snapshot, autosave

**Cleanup Debt**:
A persistent obligation representing execution data that should have been reclaimed but was not. It remains retriable and observable until cleanup succeeds or an authorized decision resolves it.
_Avoid_: Cleanup marker, log-only cleanup failure, failed residue

**Source Material**:
User-provided information that supplies the meaning and content of a Deck independently of its visual structure.
_Avoid_: Template, asset, prompt

**Deck**:
An ordered collection of Slides intended to be delivered and presented as one coherent whole.
_Avoid_: Presentation file, PPT, project

**Artifact**:
A durable file or piece of evidence published as a member of one Artifact Version, such as a Deck, preview, plan, or validation report.
_Avoid_: Artifact Version, temporary file, workspace file

**Artifact Version**:
An immutable published manifest of related Artifacts owned by one Task, retained and shared independently of later versions while preserving its lineage from any parent version.
_Avoid_: Artifact, current output, single-file version

### Runtime and packages

**Runtime Release**:
An immutable, approved combination of a Core Skill, executor contract, and toolchain that a Task pins so production and retries retain the same behavior.
_Avoid_: Runtime image, deployment, latest runtime

**Core Skill**:
The versioned instructions, references, and executable production logic that define how the runtime performs presentation work. It belongs to a Runtime Release rather than to a Task's inputs or outputs.
_Avoid_: Catalog Template, Resource Bundle, prompt

### Templates and composition

**Catalog Template**:
A stable catalog identity for a reusable design offering that supplies visual identity and structural guidance through its Template Versions.
_Avoid_: Template Version, Fill Template, Source Deck

**Template Version**:
An immutable published revision of a Catalog Template containing its design definition and exact references to any required Resource Bundles.
_Avoid_: Catalog Template, current template, Fill Template

**Resource Bundle**:
An immutable, versioned collection of non-executable visual assets shared by Template Versions when the assets require independent distribution, reuse, retention, or license management.
_Avoid_: Resource, Core Skill, Artifact

**Template Lock**:
The immutable record of the Template Version selected for a Task and the exact digests of its required Resource Bundles, ensuring the same design inputs govern production and retries.
_Avoid_: Template selection, Catalog Template, latest version

**Fill Template**:
An uploaded editable Deck whose existing Slides are candidates for reuse with new content in the Template Fill Route. It is Source Material, not a selected Catalog Template.
_Avoid_: Template, Catalog Template, layout

**Slide Library**:
A structured view of the Slides available in a source Deck for selection, planning, and mapping into an output Deck.
_Avoid_: Template catalog, output Deck

**Slide**:
A content-bearing visual unit within a Deck that owns a composition of Elements. A Slide's identity is local to its Deck and distinct from the position it occupies in another Deck.
_Avoid_: Page when referring to the content-bearing unit, screen

**Page Position**:
The ordinal slot a Slide occupies in a specific Deck, allowing a source Slide to map to a different position in an output Deck.
_Avoid_: Slide ID, Slide

**Element**:
An addressable content object placed on a Slide, such as a text block, image, table, chart, shape, or formula. An Element may refer to a Resource but remains part of the Slide's composition.
_Avoid_: Resource, template, slide

**Resource**:
A source or generated asset that one or more Elements may use, such as an image, icon, or chart data. A Resource is not an Element until it is placed into a Slide composition.
_Avoid_: Element, Artifact

**Design Specification**:
The approved description of the intended Deck structure, Slide roles, content allocation, and visual constraints that guides realization of the output Deck.
_Avoid_: Template, implementation plan

**Fill Plan**:
The approved mapping from target content and story order to source Slides and their Page Positions in the output Deck.
_Avoid_: Design Specification, Slide Library

### Production flow

**Route**:
The production strategy selected for a Task from its intent and Source Material. A Route selects the approved Pipeline Version used to create the Task's Generation Pipeline.
_Avoid_: URL route, runner profile

**Generation Route**:
The Route that creates a new Deck from Source Material under the guidance of a Catalog Template and Design Specification.
_Avoid_: Main route, default route

**Beautify Route**:
The Route that redesigns an existing source Deck while preserving its frozen visible content and Slide count.
_Avoid_: Generation Route, Template Fill Route

**Template Fill Route**:
The Route that creates an output Deck by selecting, ordering, and filling Slides from a Fill Template with new Source Material.
_Avoid_: Generation Route, Beautify Route

**Pipeline Definition**:
A platform-approved, versioned blueprint for a Route's Phases, dependencies, Confirmation Gates, contracts, retry rules, and required runtime capabilities.
_Avoid_: Core Skill, Runtime Release, workflow script

**Pipeline Version**:
An immutable approved revision of a Pipeline Definition that a Task pins once its Route is determined.
_Avoid_: Current pipeline, latest pipeline, Generation Pipeline

**Generation Pipeline**:
The Task-specific enactment of a pinned Pipeline Version that transforms its inputs into a validated, published Deck through Phases and Confirmation Gates.
_Avoid_: Rendering Pipeline, workflow when referring to the whole lifecycle

**Phase**:
A bounded step in a Generation Pipeline that consumes established Task state and produces a new outcome, decision, or Artifact.
_Avoid_: Task, Route, status

**Phase Run**:
One attempt by a Task to enact a Phase from its pinned Pipeline Version, aggregating execution and validation evidence into a single outcome without overwriting earlier attempts.
_Avoid_: Phase, Runtime Run, Task status

**Runtime Run**:
One invocation of an approved runtime capability that belongs to exactly one Phase Run. A mutating Runtime Run operates through one isolated Runtime View, while a Phase Run may require no Runtime Runs or coordinate several before determining its outcome.
_Avoid_: Phase Run, Task, Sandbox Lease

**Sandbox Lease**:
A time-bounded exclusive grant allowing one Runtime Run to use a sandboxed execution environment. It carries no Task state beyond the lease; durable mutable execution state remains in the Task Workspace.
_Avoid_: Sandbox Workspace, Task Workspace, Runtime Run

**Confirmation Gate**:
A point in the Generation Pipeline where user approval is required before later Phases may proceed.
_Avoid_: Phase completion, automatic validation

**Rendering**:
The activity that realizes approved Slide definitions and Element placements as concrete visual slide output. Rendering is part of some Routes, not a synonym for the complete Generation Pipeline.
_Avoid_: Generation Pipeline, export, validation
