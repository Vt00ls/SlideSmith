package artifactpublication

// This file delivers child SPEC #108 (C05-05): the durable Publication
// Residue lifecycle and the C05/Durable Object Cleanup Debt ownership
// boundary. It defines the closed residue disposition, release receipt and
// cleanup debt model, and the shared pure functions both the deterministic
// in-memory authority and the real PostgreSQL owned persistence adapter run
// so residue semantics can never diverge.
//
// Ownership boundary: the Durable Object authority owns physical staging,
// replica, cache, quarantine and physical reclamation; C05 owns only the
// publication assembly resource it actually created. A residue that carries
// only Durable Object typed staging references is a reconciliation backlog
// with NO Cleanup Debt (no DebtID is minted, nothing is duplicated). A
// residue that carries a C05-owned assembly resource creates exactly one
// C05-owned Cleanup Debt whose closure requires evidence-backed
// Reclaimed/AlreadyAbsent/RetainedByAuthority or an audited
// AcceptedException. Path disappearance, empty directories, object
// listings, logs, metrics or operator assertions can never close a debt or
// prove capacity reclaimed.

// ResidueDisposition is the closed set of durable residue release
// dispositions. It is recorded durably before any physical action and only
// transitions on evidence-backed receipts; an ambiguous receipt keeps the
// residue in release-requested (never guessed success, failure, zero bytes
// or already absent).
type ResidueDisposition string

const (
	// ResiduePending means the residue is durably recorded and no physical
	// release has been requested yet.
	ResiduePending ResidueDisposition = "pending"
	// ResidueReleaseRequested means a physical release attempt is in
	// flight. It stays here while the Durable Object receipt is ambiguous
	// or lost; reconciliation re-evaluates the original operation.
	ResidueReleaseRequested ResidueDisposition = "release_requested"
	// ResidueReleased is the evidence-backed closure: the Durable Object
	// authority receipt proves the exact typed staging references were
	// reclaimed.
	ResidueReleased ResidueDisposition = "released"
	// ResidueAlreadyAbsent is the evidence-backed closure: the Durable
	// Object authority receipt proves the exact references were already
	// absent.
	ResidueAlreadyAbsent ResidueDisposition = "already_absent"
	// ResidueRetainedByAuthority is the evidence-backed disposition: the
	// Durable Object authority retains the exact references and attests
	// retention.
	ResidueRetainedByAuthority ResidueDisposition = "retained_by_authority"
	// ResidueBlocked means a blocker (lease/reference/incident/grace/
	// quarantine) prevents physical release; the obligation remains.
	ResidueBlocked ResidueDisposition = "blocked"
	// ResidueExpired marks that the recorded retention window passed. It is
	// NOT a guess that anything is absent or reclaimed: the release
	// obligation continues until an evidence-backed receipt.
	ResidueExpired ResidueDisposition = "expired"
)

func validResidueDisposition(disposition ResidueDisposition) bool {
	switch disposition {
	case ResiduePending, ResidueReleaseRequested, ResidueReleased,
		ResidueAlreadyAbsent, ResidueRetainedByAuthority, ResidueBlocked, ResidueExpired:
		return true
	default:
		return false
	}
}

// residueClosed reports whether the disposition is an evidence-backed
// terminal closure. Only Reclaimed/AlreadyAbsent evidence (or an audited
// exception on the C05-owned debt side) closes a residue obligation; a
// metric, directory, listing or operator assertion never does.
func residueClosed(disposition ResidueDisposition) bool {
	switch disposition {
	case ResidueReleased, ResidueAlreadyAbsent, ResidueRetainedByAuthority:
		return true
	default:
		return false
	}
}

// ResidueErrorCategory is the closed, content-free error category recorded
// for the last residue release attempt. It feeds retry/backoff and safe
// error rendering; it never carries a raw downstream error chain.
type ResidueErrorCategory string

const (
	ResidueErrorNone                ResidueErrorCategory = ""
	ResidueErrorUnavailable         ResidueErrorCategory = "unavailable"
	ResidueErrorAuthorizationDenied ResidueErrorCategory = "authorization_denied"
	ResidueErrorIntegrityConflict   ResidueErrorCategory = "integrity_conflict"
	ResidueErrorStale               ResidueErrorCategory = "stale"
)

func validResidueErrorCategory(category ResidueErrorCategory) bool {
	switch category {
	case ResidueErrorNone, ResidueErrorUnavailable, ResidueErrorAuthorizationDenied,
		ResidueErrorIntegrityConflict, ResidueErrorStale:
		return true
	default:
		return false
	}
}

// ReleaseOutcome is the closed outcome of one Durable Object physical
// release attempt. Only released/already_absent/retained_by_authority are
// evidence-backed closures; ambiguous keeps the residue open for
// reconciliation; blocked keeps it open with blocker classes.
type ReleaseOutcome string

const (
	ReleaseOutcomeReleased            ReleaseOutcome = "released"
	ReleaseOutcomeAlreadyAbsent       ReleaseOutcome = "already_absent"
	ReleaseOutcomeRetainedByAuthority ReleaseOutcome = "retained_by_authority"
	ReleaseOutcomeAmbiguous           ReleaseOutcome = "ambiguous"
	ReleaseOutcomeBlocked             ReleaseOutcome = "blocked"
)

func validReleaseOutcome(outcome ReleaseOutcome) bool {
	switch outcome {
	case ReleaseOutcomeReleased, ReleaseOutcomeAlreadyAbsent,
		ReleaseOutcomeRetainedByAuthority, ReleaseOutcomeAmbiguous, ReleaseOutcomeBlocked:
		return true
	default:
		return false
	}
}

// ReleaseReceipt is the evidence-backed receipt returned by the Durable
// Object authority for one physical release attempt of the exact typed
// staging references of one residue. It binds the exact operation, the
// receipt identity, the issuing authority, the closed outcome and the
// diagnostic occurred-at instant. A receipt alone never confers closure: it
// must be produced by the registered Durable Object authority and validated
// against the exact residue facts before the residue disposition
// transitions.
type ReleaseReceipt struct {
	ReceiptID  string
	Digest     Digest
	Producer   EvidenceProducer
	Outcome    ReleaseOutcome
	Blockers   CleanupBlockerClass
	Expiry     Instant
	OccurredAt Instant
}

// CanonicalDigest deterministically encodes the release receipt. The digest
// commits the exact identity, producer, outcome and blocker facts; it never
// includes delivery attempts, trace IDs or telemetry attributes.
func (r ReleaseReceipt) CanonicalDigest() Digest {
	return canonicalDigest(map[string]any{
		"receipt_id":   r.ReceiptID,
		"producer_id":  string(r.Producer.AuthorityID),
		"producer_gen": uint64(r.Producer.Generation),
		"outcome":      string(r.Outcome),
		"blockers":     uint16(r.Blockers),
		"expiry":       uint64(r.Expiry),
		"occurred_at":  uint64(r.OccurredAt),
	})
}

// CleanupBlockerClass is the closed set of blocker classes that can hold a
// C05-owned Cleanup Debt or a residue release open. Blockers are recorded
// as content-free classes plus a digest; a blocker never leaks a locator.
type CleanupBlockerClass uint16

const (
	CleanupBlockerLease CleanupBlockerClass = 1 << iota
	CleanupBlockerReference
	CleanupBlockerIncident
	CleanupBlockerGracePeriod
	CleanupBlockerQuarantine
	CleanupBlockerMask CleanupBlockerClass = CleanupBlockerLease | CleanupBlockerReference |
		CleanupBlockerIncident | CleanupBlockerGracePeriod | CleanupBlockerQuarantine
)

func validCleanupBlockers(blockers CleanupBlockerClass) bool {
	return blockers&^CleanupBlockerMask == 0
}

// CleanupDebtID is the opaque, non-reused identity of one C05-owned Cleanup
// Debt. A DebtID exists only when C05 actually created a physical
// publication assembly resource; a staging-only residue keeps a
// reconciliation backlog and never duplicates a DebtID.
type CleanupDebtID string

// CleanupDebtStatus is the closed status set of a C05-owned Cleanup Debt.
type CleanupDebtStatus string

const (
	CleanupDebtOpen           CleanupDebtStatus = "open"
	CleanupDebtClaimed        CleanupDebtStatus = "claimed"
	CleanupDebtRetryScheduled CleanupDebtStatus = "retry_scheduled"
	CleanupDebtBlocked        CleanupDebtStatus = "blocked"
	CleanupDebtResolved       CleanupDebtStatus = "resolved"
)

func validCleanupDebtStatus(status CleanupDebtStatus) bool {
	switch status {
	case CleanupDebtOpen, CleanupDebtClaimed, CleanupDebtRetryScheduled,
		CleanupDebtBlocked, CleanupDebtResolved:
		return true
	default:
		return false
	}
}

// CleanupRetryDisposition is the closed retry disposition of a C05-owned
// Cleanup Debt.
type CleanupRetryDisposition string

const (
	CleanupRetryNone      CleanupRetryDisposition = ""
	CleanupRetryReady     CleanupRetryDisposition = "ready"
	CleanupRetryClaimed   CleanupRetryDisposition = "claimed"
	CleanupRetryScheduled CleanupRetryDisposition = "scheduled"
	CleanupRetryBlocked   CleanupRetryDisposition = "blocked"
)

func validCleanupRetryDisposition(disposition CleanupRetryDisposition) bool {
	switch disposition {
	case CleanupRetryNone, CleanupRetryReady, CleanupRetryClaimed,
		CleanupRetryScheduled, CleanupRetryBlocked:
		return true
	default:
		return false
	}
}

// CleanupDebtResolutionClass is the closed set of evidence-backed (or
// audited) resolution classes that can close a C05-owned Cleanup Debt. A
// path disappearance, empty directory, object listing, log, metric or
// operator assertion can never close a debt: only a typed ResolveCleanupDebt
// intent with the matching evidence/approval closes it.
type CleanupDebtResolutionClass string

const (
	CleanupResolutionReclaimed           CleanupDebtResolutionClass = "reclaimed"
	CleanupResolutionAlreadyAbsent       CleanupDebtResolutionClass = "already_absent"
	CleanupResolutionRetainedByAuthority CleanupDebtResolutionClass = "retained_by_authority"
	CleanupResolutionAcceptedException   CleanupDebtResolutionClass = "accepted_exception"
)

func validCleanupResolutionClass(class CleanupDebtResolutionClass) bool {
	switch class {
	case CleanupResolutionReclaimed, CleanupResolutionAlreadyAbsent,
		CleanupResolutionRetainedByAuthority, CleanupResolutionAcceptedException:
		return true
	default:
		return false
	}
}

// CleanupResolutionReason is the closed reason bound to one resolution
// class. It never carries free-form operator text.
type CleanupResolutionReason string

const (
	CleanupResolutionReasonCleanupProven             CleanupResolutionReason = "cleanup_proven"
	CleanupResolutionReasonExactGenerationAbsent     CleanupResolutionReason = "exact_generation_absent"
	CleanupResolutionReasonCurrentAuthorityRetention CleanupResolutionReason = "current_authority_retention"
	CleanupResolutionReasonAdministratorException    CleanupResolutionReason = "administrator_exception"
)

func validCleanupResolutionReason(reason CleanupResolutionReason) bool {
	switch reason {
	case CleanupResolutionReasonCleanupProven, CleanupResolutionReasonExactGenerationAbsent,
		CleanupResolutionReasonCurrentAuthorityRetention, CleanupResolutionReasonAdministratorException:
		return true
	default:
		return false
	}
}

// resolutionReasonForClass returns the single valid reason for a
// resolution class (the reason is fixed by the class; no free-form reason).
func resolutionReasonForClass(class CleanupDebtResolutionClass) CleanupResolutionReason {
	switch class {
	case CleanupResolutionReclaimed:
		return CleanupResolutionReasonCleanupProven
	case CleanupResolutionAlreadyAbsent:
		return CleanupResolutionReasonExactGenerationAbsent
	case CleanupResolutionRetainedByAuthority:
		return CleanupResolutionReasonCurrentAuthorityRetention
	case CleanupResolutionAcceptedException:
		return CleanupResolutionReasonAdministratorException
	default:
		return ""
	}
}

// CleanupResolutionEvidence is the evidence-backed closure evidence of a
// C05-owned Cleanup Debt. For reclaimed/already_absent/retained_by_authority
// it must be produced by the registered Durable Object authority (the
// physical reclamation authority) and bind the exact resource identity
// digest and generation. An audited AcceptedException carries an approval
// reference and an audit fact instead.
type CleanupResolutionEvidence struct {
	EvidenceID EvidenceID
	Digest     Digest
	Producer   EvidenceProducer
	// ResourceIdentityDigest binds the evidence to the exact C05-owned
	// assembly resource identity the debt names.
	ResourceIdentityDigest Digest
	ResourceGeneration     uint64
	ResourceFence          uint64
	OccurredAt             Instant
}

// CanonicalDigest deterministically encodes the cleanup resolution
// evidence.
func (e CleanupResolutionEvidence) CanonicalDigest() Digest {
	return canonicalDigest(map[string]any{
		"evidence_id":              string(e.EvidenceID),
		"producer_id":              string(e.Producer.AuthorityID),
		"producer_gen":             uint64(e.Producer.Generation),
		"resource_identity_digest": string(e.ResourceIdentityDigest),
		"resource_generation":      e.ResourceGeneration,
		"resource_fence":           e.ResourceFence,
		"occurred_at":              uint64(e.OccurredAt),
	})
}

// assemblyResource is the opaque C05-owned physical publication assembly
// resource reference. It is the ONLY C05-owned physical resource a residue
// can carry; everything else (staging, replica, cache, quarantine,
// reclamation) is owned by the Durable Object authority. It never contains
// a path, object key, prefix, bucket, vendor or locator.
type assemblyResource struct {
	// Reference is the opaque resource reference C05 minted when it
	// created the assembly resource.
	Reference string
	// IdentityDigest is the content-free identity digest of the resource.
	IdentityDigest Digest
	Generation     uint64
	Fence          uint64
}

// residueClosedByEvidence reports whether the residue release obligation is
// closed by evidence (released/already_absent) or an audited exception. A
// retained-by-authority residue keeps its obligation open on the C05 side
// even though the Durable Object authority attests retention.
func residueClosedByEvidence(disposition ResidueDisposition) bool {
	switch disposition {
	case ResidueReleased, ResidueAlreadyAbsent:
		return true
	default:
		return false
	}
}

// cleanupBackoffSeconds is the shared exponential backoff policy for
// retryable release/cleanup failures: 60s, 120s, 240s, ... capped at 64
// minutes. The policy is a pure function so both adapters and the tests
// observe the same schedule.
func cleanupBackoffSeconds(consecutiveFailures uint64) uint64 {
	if consecutiveFailures == 0 {
		return 0
	}
	shift := consecutiveFailures - 1
	if shift > 6 {
		shift = 6
	}
	return 60 << shift
}

// nextCleanupRetry computes the next retry instant after a retryable
// failure.
func nextCleanupRetry(now Instant, consecutiveFailures uint64) Instant {
	return now + Instant(cleanupBackoffSeconds(consecutiveFailures))
}

// validateReleaseReceipt is the pure evidence gate for a release receipt.
// It fails closed unless the receipt is produced by the registered Durable
// Object authority, carries a valid canonical digest, has a closed outcome,
// valid blocker classes, and a non-empty identity. Ambiguous is a valid
// outcome but NEVER closes the residue (it keeps release-requested).
func validateReleaseReceipt(receipt ReleaseReceipt, durableObjectAuthorityID AuthorityID) bool {
	if receipt.ReceiptID == "" || !validDigest(receipt.Digest) ||
		receipt.Producer.AuthorityID != durableObjectAuthorityID ||
		receipt.Producer.Generation == 0 || !validReleaseOutcome(receipt.Outcome) ||
		!validCleanupBlockers(receipt.Blockers) || receipt.OccurredAt == 0 {
		return false
	}
	if receipt.Digest != receipt.CanonicalDigest() {
		return false
	}
	if receipt.Outcome == ReleaseOutcomeBlocked && receipt.Blockers == 0 {
		return false
	}
	if receipt.Outcome != ReleaseOutcomeBlocked && receipt.Blockers != 0 {
		return false
	}
	return true
}

// applyReleaseReceipt is the pure residue disposition transition. It maps
// an evidence-backed receipt to the residue disposition. Ambiguous keeps
// release-requested (reconciliation required); blocked keeps the residue
// open with the blocker classes recorded on the receipt.
func applyReleaseReceipt(outcome ReleaseOutcome) ResidueDisposition {
	switch outcome {
	case ReleaseOutcomeReleased:
		return ResidueReleased
	case ReleaseOutcomeAlreadyAbsent:
		return ResidueAlreadyAbsent
	case ReleaseOutcomeRetainedByAuthority:
		return ResidueRetainedByAuthority
	case ReleaseOutcomeBlocked:
		return ResidueBlocked
	default:
		return ResidueReleaseRequested
	}
}

// validateCleanupResolutionEvidence is the pure evidence gate for an
// evidence-backed debt resolution. The evidence must be produced by the
// registered Durable Object authority (the physical reclamation authority),
// carry a valid canonical digest, and bind the exact resource identity
// digest and generation/fence the debt names.
func validateCleanupResolutionEvidence(evidence CleanupResolutionEvidence, durableObjectAuthorityID AuthorityID, debt *cleanupDebtRecord) bool {
	if evidence.EvidenceID == "" || !validDigest(evidence.Digest) ||
		evidence.Producer.AuthorityID != durableObjectAuthorityID ||
		evidence.Producer.Generation == 0 || evidence.OccurredAt == 0 {
		return false
	}
	if evidence.Digest != evidence.CanonicalDigest() {
		return false
	}
	if debt == nil {
		return false
	}
	if evidence.ResourceIdentityDigest != debt.resourceDigest ||
		evidence.ResourceGeneration != debt.resourceGeneration ||
		evidence.ResourceFence != debt.resourceFence {
		return false
	}
	return true
}

// releaseErrorCategory maps a safe error to the content-free residue error
// category recorded for retry/backoff.
func releaseErrorCategory(err *Error) ResidueErrorCategory {
	if err == nil {
		return ResidueErrorNone
	}
	switch err.Code {
	case ErrorOwnershipDenied:
		return ResidueErrorAuthorizationDenied
	case ErrorIntegrityConflict, ErrorIntegrityFailure:
		return ResidueErrorIntegrityConflict
	case ErrorStaleAuthority:
		return ResidueErrorStale
	default:
		return ResidueErrorUnavailable
	}
}

// recordResidueAttempt is the pure retry/backoff state update for a failed
// or ambiguous release attempt. Retryable failures and ambiguous outcomes
// increment the consecutive failure counter and schedule the next retry;
// non-retryable (authorization/stale/integrity) failures keep the residue
// open without scheduling a blind retry.
func recordResidueAttempt(residue *residueRecord, category ResidueErrorCategory, now Instant) {
	residue.attemptCount++
	switch category {
	case ResidueErrorUnavailable:
		residue.consecutiveFailures++
		residue.nextRetryAt = nextCleanupRetry(now, residue.consecutiveFailures)
	case ResidueErrorNone:
		// Evidence-backed success: the attempt succeeded.
		residue.consecutiveFailures = 0
		residue.nextRetryAt = 0
	default:
		residue.consecutiveFailures++
		// Authorization/stale/integrity failures fail closed and are not
		// blindly retried by backoff; the operator reconciles.
		residue.nextRetryAt = 0
	}
	residue.lastErrorCategory = category
}

// recordDebtAttempt is the pure retry/backoff state update for one C05
// cleanup debt attempt.
func recordDebtAttempt(debt *cleanupDebtRecord, category ResidueErrorCategory, now Instant) {
	debt.attemptCount++
	if debt.firstAttemptAt == 0 {
		debt.firstAttemptAt = now
	}
	debt.lastAttemptAt = now
	debt.lastErrorCategory = category
	switch category {
	case ResidueErrorNone:
		debt.consecutiveFailures = 0
		debt.nextRetryAt = 0
		debt.retryDisposition = CleanupRetryReady
	case ResidueErrorUnavailable:
		debt.consecutiveFailures++
		debt.nextRetryAt = nextCleanupRetry(now, debt.consecutiveFailures)
		debt.retryDisposition = CleanupRetryScheduled
	default:
		debt.consecutiveFailures++
		debt.nextRetryAt = 0
		debt.retryDisposition = CleanupRetryBlocked
	}
}
