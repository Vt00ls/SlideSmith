package artifactpublication

// PostgresFaultPoint is the closed set of bounded persistence fault points
// in the real PostgreSQL owned persistence adapter. Every point sits at a
// precise boundary of the journal/candidate/evidence/activation/attach/
// audit/outbox/commit/response path so fault tests can prove crash-before-
// commit leaves nothing durable and crash-after-commit-before-response can
// be exact-replayed without reallocating identity.
type PostgresFaultPoint uint8

const (
	// PostgresFaultBeforeRequestJournal aborts before the operation journal
	// row is inserted (prepare only).
	PostgresFaultBeforeRequestJournal PostgresFaultPoint = iota + 1
	// PostgresFaultBeforeCandidatePersistence aborts before the candidate,
	// members, staging references, and pinned evidence references are
	// persisted (prepare only).
	PostgresFaultBeforeCandidatePersistence
	// PostgresFaultBeforeEvidenceAcceptance aborts before evidence is
	// evaluated (verify only).
	PostgresFaultBeforeEvidenceAcceptance
	// PostgresFaultBeforeVerificationResult aborts after evidence
	// evaluation but before the verification result is persisted (verify
	// only).
	PostgresFaultBeforeVerificationResult
	// PostgresFaultBeforeActivationCommit aborts after the row-lock/CAS
	// revalidation but before the Artifact Version, members, lineage,
	// stream advance, terminal operation, attach, audit, and evidence are
	// committed (activate only).
	PostgresFaultBeforeActivationCommit
	// PostgresFaultBeforeReferenceAttach aborts before the restricted
	// Durable Object participant attaches the exact typed references
	// (activate only).
	PostgresFaultBeforeReferenceAttach
	// PostgresFaultBeforeMandatoryAudit aborts before the mandatory audit
	// row is inserted (all intents).
	PostgresFaultBeforeMandatoryAudit
	// PostgresFaultBeforeOutbox aborts before the Task Orchestration outbox
	// row is inserted (terminal dispositions: activate, reject, cancel).
	PostgresFaultBeforeOutbox
	// PostgresFaultBeforeCommit aborts before COMMIT: the whole
	// transaction rolls back and nothing is durable (crash-before-commit).
	PostgresFaultBeforeCommit
	// PostgresFaultAfterCommit aborts after COMMIT but before the decision
	// is returned: the facts are durable but the caller lost the response
	// (crash-after-commit-before-response). A retry with the same canonical
	// intent exact-replays the committed decision.
	PostgresFaultAfterCommit
)

// PostgresFaultEvent reports one persistence fault injection.
type PostgresFaultEvent struct {
	Point       PostgresFaultPoint
	OperationID PublicationOperationID
	IntentKind  PublicationIntentKind
	SubjectID   string
}

// PostgresFaultHook injects a fault at one bounded persistence point. A
// non-nil error aborts the mutation exactly at that point.
type PostgresFaultHook func(PostgresFaultEvent) error
