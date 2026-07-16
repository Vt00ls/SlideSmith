# SlideSmith Presentation Production

SlideSmith turns source material and presentation design constraints into editable, validated slide decks. This context defines the language used to describe deck composition and the route-specific lifecycle that produces it.

## Language

### Work and outputs

**Task**:
A single user request to create, restyle, or fill one Deck. It owns the intent, inputs, confirmations, production route, and resulting artifacts as one lifecycle.
_Avoid_: Project, job, run

**Source Material**:
User-provided information that supplies the meaning and content of a Deck independently of its visual structure.
_Avoid_: Template, asset, prompt

**Deck**:
An ordered collection of Slides intended to be delivered and presented as one coherent whole.
_Avoid_: Presentation file, PPT, project

**Artifact**:
A durable Task outcome or piece of evidence, such as a Deck, preview, plan, or validation report.
_Avoid_: Temporary file, workspace file

### Templates and composition

**Catalog Template**:
A selectable, versioned design package that supplies visual identity and structural guidance to a Task. It is a design input, not the output Deck, regardless of whether its catalog kind is a layout, deck, or brand.
_Avoid_: Template, Fill Template, Source Deck

**Template Lock**:
The immutable snapshot of the Catalog Template selected for a Task, ensuring that the same design package governs the Task throughout processing and retries.
_Avoid_: Template selection, current template

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
The production strategy selected for a Task from its intent and Source Material. A Route determines which phases and confirmation gates make up the Task's Generation Pipeline.
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

**Generation Pipeline**:
The Route-specific lifecycle that transforms a Task's inputs into a validated, published Deck through ordered Phases and Confirmation Gates.
_Avoid_: Rendering Pipeline, workflow when referring to the whole lifecycle

**Phase**:
A bounded step in a Generation Pipeline that consumes established Task state and produces a new outcome, decision, or Artifact.
_Avoid_: Task, Route, status

**Confirmation Gate**:
A point in the Generation Pipeline where user approval is required before later Phases may proceed.
_Avoid_: Phase completion, automatic validation

**Rendering**:
The activity that realizes approved Slide definitions and Element placements as concrete visual slide output. Rendering is part of some Routes, not a synonym for the complete Generation Pipeline.
_Avoid_: Generation Pipeline, export, validation
