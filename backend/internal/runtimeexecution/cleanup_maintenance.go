package runtimeexecution

import (
	"crypto/sha256"
	"encoding/json"
	"time"
)

// Cleanup maintenance commands are closed protected operational intents
// handled exclusively through RuntimeMaintenance. They are never variants of
// the public Execute(StartRuntimeRun|CancelRuntimeRun) or Inspect seam. Each
// command binds the debt-owning typed authority, a closed reason, expected
// Runtime revision/fence, exact resource generation/fence, and an idempotency
// key (OperationID). They cannot start or cancel a Runtime Run, mutate
// Task/Phase state, alter Scheduler counters, resolve C04 debt, or select a
// production site policy.

// CleanupObligationReason is the closed reason recorded when a C03-owned
// Cleanup Debt obligation is durably created before any physical cleanup
// attempt.
type CleanupObligationReason uint8

const (
	CleanupObligationPostLeaseTerminal CleanupObligationReason = iota + 1
	CleanupObligationContainmentRequired
	CleanupObligationResetRequired
	CleanupObligationNodeLost
)

// CleanupExceptionExpiryReason is the closed reason for an explicit decision
// that expires an audited AcceptedException and reopens the obligation.
type CleanupExceptionExpiryReason uint8

const (
	CleanupExceptionDurationElapsed CleanupExceptionExpiryReason = iota + 1
	CleanupExceptionCapacityPressure
	CleanupExceptionAuthorityDecision
)

// CleanupReopenReason is the closed reason for explicitly reopening a Cleanup
// Debt whose AcceptedException has expired.
type CleanupReopenReason uint8

const (
	CleanupReopenCapacityConsistency CleanupReopenReason = iota + 1
	CleanupReopenQuarantineConsistency
	CleanupReopenObligationRestored
)

// CleanupDebtRuntimeDecision is the protected result of a Cleanup Debt
// maintenance intent. CapacityReleased is set only by Reclaimed/AlreadyAbsent
// resolutions; an AcceptedException never reports reclaimed capacity.
type CleanupDebtRuntimeDecision struct {
	DebtID           string
	DebtRevision     uint64
	Status           cleanupDebtStatus
	Unresolved       bool
	Uncontained      bool
	RetryDisposition cleanupRetryDisposition
	ResolutionClass  cleanupResolutionClass
	ResolutionReason cleanupResolutionReason
	ExceptionUntil   time.Time
	CapacityReleased bool
	Expired          bool
	Reopened         bool
	Replayed         bool
}

type CreateCleanupObligationInput struct {
	SchemaVersion           SchemaVersion
	OperationID             OperationID
	Reason                  CleanupObligationReason
	ExpectedRuntimeRevision RuntimeRevision
	ExpectedRuntimeFence    RuntimeFence
	Obligation              cleanupDebtCreation
}

type CreateCleanupObligation struct {
	CreateCleanupObligationInput
	CanonicalRequestDigest Digest
}

func NewCreateCleanupObligation(input CreateCleanupObligationInput) (CreateCleanupObligation, error) {
	command := CreateCleanupObligation{CreateCleanupObligationInput: input}
	canonical, valid := canonicalCreateCleanupObligation(command)
	if !valid {
		return CreateCleanupObligation{}, newError(ErrorInvalidRequest)
	}
	command.CanonicalRequestDigest = Digest(sha256.Sum256(canonical))
	return command, nil
}

func (CreateCleanupObligation) runtimeMaintenanceCommand()                  {}
func (command CreateCleanupObligation) maintenanceOperationID() OperationID { return command.OperationID }
func (command CreateCleanupObligation) maintenanceDigest() Digest {
	return command.CanonicalRequestDigest
}

type RecordCleanupAttemptInput struct {
	SchemaVersion           SchemaVersion
	OperationID             OperationID
	ExpectedRuntimeRevision RuntimeRevision
	ExpectedRuntimeFence    RuntimeFence
	Attempt                 cleanupDebtAttempt
}

type RecordCleanupAttempt struct {
	RecordCleanupAttemptInput
	CanonicalRequestDigest Digest
}

func NewRecordCleanupAttempt(input RecordCleanupAttemptInput) (RecordCleanupAttempt, error) {
	command := RecordCleanupAttempt{RecordCleanupAttemptInput: input}
	canonical, valid := canonicalRecordCleanupAttempt(command)
	if !valid {
		return RecordCleanupAttempt{}, newError(ErrorInvalidRequest)
	}
	command.CanonicalRequestDigest = Digest(sha256.Sum256(canonical))
	return command, nil
}

func (RecordCleanupAttempt) runtimeMaintenanceCommand()                  {}
func (command RecordCleanupAttempt) maintenanceOperationID() OperationID { return command.OperationID }
func (command RecordCleanupAttempt) maintenanceDigest() Digest           { return command.CanonicalRequestDigest }

type ResolveCleanupDebtInput struct {
	SchemaVersion           SchemaVersion
	OperationID             OperationID
	ExpectedRuntimeRevision RuntimeRevision
	ExpectedRuntimeFence    RuntimeFence
	ApprovalReference       string
	IncidentReference       string
	TicketReference         string
	Resolution              cleanupDebtResolution
}

type ResolveCleanupDebt struct {
	ResolveCleanupDebtInput
	CanonicalRequestDigest Digest
}

func NewResolveCleanupDebt(input ResolveCleanupDebtInput) (ResolveCleanupDebt, error) {
	if input.IncidentReference != "" {
		input.Resolution.IncidentReference = input.IncidentReference
	}
	if input.TicketReference != "" {
		input.Resolution.TicketReference = input.TicketReference
	}
	if input.ApprovalReference != "" {
		input.Resolution.ApprovalReference = input.ApprovalReference
	}
	command := ResolveCleanupDebt{ResolveCleanupDebtInput: input}
	canonical, valid := canonicalResolveCleanupDebt(command)
	if !valid {
		return ResolveCleanupDebt{}, newError(ErrorInvalidRequest)
	}
	command.CanonicalRequestDigest = Digest(sha256.Sum256(canonical))
	return command, nil
}

func (ResolveCleanupDebt) runtimeMaintenanceCommand()                  {}
func (command ResolveCleanupDebt) maintenanceOperationID() OperationID { return command.OperationID }
func (command ResolveCleanupDebt) maintenanceDigest() Digest           { return command.CanonicalRequestDigest }

type ExpireCleanupDebtExceptionInput struct {
	SchemaVersion           SchemaVersion
	OperationID             OperationID
	Reason                  CleanupExceptionExpiryReason
	ExpectedRuntimeRevision RuntimeRevision
	ExpectedRuntimeFence    RuntimeFence
	DebtID                  string
	PersonalWorkspaceID     PersonalWorkspaceID
	RuntimeRunID            RuntimeRunID
	Authority               RuntimeAuthority
	ExpectedRevision        uint64
	ResourceGeneration      uint64
	ResourceFence           uint64
	ExpiredAt               time.Time
}

type ExpireCleanupDebtException struct {
	ExpireCleanupDebtExceptionInput
	CanonicalRequestDigest Digest
}

func NewExpireCleanupDebtException(input ExpireCleanupDebtExceptionInput) (ExpireCleanupDebtException, error) {
	input.ExpiredAt = input.ExpiredAt.UTC()
	command := ExpireCleanupDebtException{ExpireCleanupDebtExceptionInput: input}
	canonical, valid := canonicalExpireCleanupDebtException(command)
	if !valid {
		return ExpireCleanupDebtException{}, newError(ErrorInvalidRequest)
	}
	command.CanonicalRequestDigest = Digest(sha256.Sum256(canonical))
	return command, nil
}

func (ExpireCleanupDebtException) runtimeMaintenanceCommand()                  {}
func (command ExpireCleanupDebtException) maintenanceOperationID() OperationID { return command.OperationID }
func (command ExpireCleanupDebtException) maintenanceDigest() Digest {
	return command.CanonicalRequestDigest
}

type ReopenCleanupDebtInput struct {
	SchemaVersion           SchemaVersion
	OperationID             OperationID
	Reason                  CleanupReopenReason
	ExpectedRuntimeRevision RuntimeRevision
	ExpectedRuntimeFence    RuntimeFence
	DebtID                  string
	PersonalWorkspaceID     PersonalWorkspaceID
	RuntimeRunID            RuntimeRunID
	Authority               RuntimeAuthority
	ExpectedRevision        uint64
	ResourceGeneration      uint64
	ResourceFence           uint64
	ReopenedAt              time.Time
}

type ReopenCleanupDebt struct {
	ReopenCleanupDebtInput
	CanonicalRequestDigest Digest
}

func NewReopenCleanupDebt(input ReopenCleanupDebtInput) (ReopenCleanupDebt, error) {
	input.ReopenedAt = input.ReopenedAt.UTC()
	command := ReopenCleanupDebt{ReopenCleanupDebtInput: input}
	canonical, valid := canonicalReopenCleanupDebt(command)
	if !valid {
		return ReopenCleanupDebt{}, newError(ErrorInvalidRequest)
	}
	command.CanonicalRequestDigest = Digest(sha256.Sum256(canonical))
	return command, nil
}

func (ReopenCleanupDebt) runtimeMaintenanceCommand()                  {}
func (command ReopenCleanupDebt) maintenanceOperationID() OperationID { return command.OperationID }
func (command ReopenCleanupDebt) maintenanceDigest() Digest           { return command.CanonicalRequestDigest }

func cleanupMaintenanceCanonical(domain string, reason uint8, payloadDigest Digest) []byte {
	payload, _ := json.Marshal(struct {
		Reason        uint8  `json:"reason"`
		PayloadDigest string `json:"payload_digest"`
	}{Reason: reason, PayloadDigest: payloadDigest.String()})
	canonical := append([]byte(domain+"\n"), payload...)
	return canonical
}

func canonicalCreateCleanupObligation(command CreateCleanupObligation) ([]byte, bool) {
	if command.SchemaVersion.Major() != SchemaV1.Major() ||
		command.OperationID.String() != command.Obligation.MutationID ||
		command.Reason < CleanupObligationPostLeaseTerminal || command.Reason > CleanupObligationNodeLost {
		return nil, false
	}
	payloadDigest, err := cleanupCreationDigest(command.Obligation)
	if err != nil {
		return nil, false
	}
	return cleanupMaintenanceCanonical("slidesmith.runtime-execution.cleanup-maintenance-create/v1",
		uint8(command.Reason), payloadDigest), true
}

func canonicalRecordCleanupAttempt(command RecordCleanupAttempt) ([]byte, bool) {
	if command.SchemaVersion.Major() != SchemaV1.Major() ||
		command.OperationID.String() != command.Attempt.MutationID {
		return nil, false
	}
	payloadDigest, err := cleanupAttemptDigest(command.Attempt)
	if err != nil {
		return nil, false
	}
	return cleanupMaintenanceCanonical("slidesmith.runtime-execution.cleanup-maintenance-attempt/v1",
		uint8(command.Attempt.FailureCategory), payloadDigest), true
}

func canonicalResolveCleanupDebt(command ResolveCleanupDebt) ([]byte, bool) {
	if command.SchemaVersion.Major() != SchemaV1.Major() ||
		command.OperationID.String() != command.Resolution.MutationID {
		return nil, false
	}
	payloadDigest, err := cleanupResolutionDigest(command.Resolution)
	if err != nil {
		return nil, false
	}
	return cleanupMaintenanceCanonical("slidesmith.runtime-execution.cleanup-maintenance-resolution/v1",
		uint8(command.Resolution.Class), payloadDigest), true
}

func canonicalExpireCleanupDebtException(command ExpireCleanupDebtException) ([]byte, bool) {
	input := command.ExpireCleanupDebtExceptionInput
	if input.SchemaVersion.Major() != SchemaV1.Major() || !validOpaqueID(input.OperationID.String()) ||
		input.Reason < CleanupExceptionDurationElapsed || input.Reason > CleanupExceptionAuthorityDecision ||
		!validOpaqueID(input.DebtID) || !validOpaqueID(input.PersonalWorkspaceID.String()) ||
		!validOpaqueID(input.RuntimeRunID.String()) || !validAuthority(input.Authority) ||
		input.ExpectedRevision == 0 || input.ResourceGeneration == 0 || input.ResourceFence == 0 ||
		input.ExpiredAt.IsZero() || input.ExpiredAt != input.ExpiredAt.UTC() {
		return nil, false
	}
	payloadDigest, err := cleanupExceptionExpiryDigest(input)
	if err != nil {
		return nil, false
	}
	return cleanupMaintenanceCanonical("slidesmith.runtime-execution.cleanup-maintenance-expire/v1",
		uint8(input.Reason), payloadDigest), true
}

func canonicalReopenCleanupDebt(command ReopenCleanupDebt) ([]byte, bool) {
	input := command.ReopenCleanupDebtInput
	if input.SchemaVersion.Major() != SchemaV1.Major() || !validOpaqueID(input.OperationID.String()) ||
		input.Reason < CleanupReopenCapacityConsistency || input.Reason > CleanupReopenObligationRestored ||
		!validOpaqueID(input.DebtID) || !validOpaqueID(input.PersonalWorkspaceID.String()) ||
		!validOpaqueID(input.RuntimeRunID.String()) || !validAuthority(input.Authority) ||
		input.ExpectedRevision == 0 || input.ResourceGeneration == 0 || input.ResourceFence == 0 ||
		input.ReopenedAt.IsZero() || input.ReopenedAt != input.ReopenedAt.UTC() {
		return nil, false
	}
	payloadDigest, err := cleanupReopenDigest(input)
	if err != nil {
		return nil, false
	}
	return cleanupMaintenanceCanonical("slidesmith.runtime-execution.cleanup-maintenance-reopen/v1",
		uint8(input.Reason), payloadDigest), true
}

type canonicalCleanupExceptionExpiry struct {
	Schema             string `json:"schema"`
	OperationID        string `json:"operation_id"`
	DebtID             string `json:"debt_id"`
	PersonalWorkspaceID string `json:"personal_workspace_id"`
	RuntimeRunID       string `json:"runtime_run_id"`
	AuthorityKind      AuthorityKind           `json:"authority_kind"`
	AuthorityID        string                  `json:"authority_id"`
	AuthorityGeneration AuthorizationGeneration `json:"authority_generation"`
	ExpectedRevision   uint64                  `json:"expected_revision"`
	ResourceGeneration uint64                  `json:"resource_generation"`
	ResourceFence      uint64                  `json:"resource_fence"`
	ExpiredAt          string                  `json:"expired_at"`
}

type canonicalCleanupReopen struct {
	Schema             string `json:"schema"`
	OperationID        string `json:"operation_id"`
	DebtID             string `json:"debt_id"`
	PersonalWorkspaceID string `json:"personal_workspace_id"`
	RuntimeRunID       string `json:"runtime_run_id"`
	AuthorityKind      AuthorityKind           `json:"authority_kind"`
	AuthorityID        string                  `json:"authority_id"`
	AuthorityGeneration AuthorizationGeneration `json:"authority_generation"`
	ExpectedRevision   uint64                  `json:"expected_revision"`
	ResourceGeneration uint64                  `json:"resource_generation"`
	ResourceFence      uint64                  `json:"resource_fence"`
	ReopenedAt         string                  `json:"reopened_at"`
}

func cleanupExceptionExpiryDigest(input ExpireCleanupDebtExceptionInput) (Digest, error) {
	payload := canonicalCleanupExceptionExpiry{
		Schema: "slidesmith.runtime-execution.cleanup-maintenance-expire/v1", OperationID: input.OperationID.String(),
		DebtID: input.DebtID, PersonalWorkspaceID: input.PersonalWorkspaceID.String(),
		RuntimeRunID: input.RuntimeRunID.String(), AuthorityKind: input.Authority.kind,
		AuthorityID: input.Authority.id.String(), AuthorityGeneration: input.Authority.generation,
		ExpectedRevision: input.ExpectedRevision, ResourceGeneration: input.ResourceGeneration,
		ResourceFence: input.ResourceFence, ExpiredAt: formatCleanupTime(input.ExpiredAt),
	}
	return cleanupMutationDigest(payload.Schema, payload)
}

func cleanupReopenDigest(input ReopenCleanupDebtInput) (Digest, error) {
	payload := canonicalCleanupReopen{
		Schema: "slidesmith.runtime-execution.cleanup-maintenance-reopen/v1", OperationID: input.OperationID.String(),
		DebtID: input.DebtID, PersonalWorkspaceID: input.PersonalWorkspaceID.String(),
		RuntimeRunID: input.RuntimeRunID.String(), AuthorityKind: input.Authority.kind,
		AuthorityID: input.Authority.id.String(), AuthorityGeneration: input.Authority.generation,
		ExpectedRevision: input.ExpectedRevision, ResourceGeneration: input.ResourceGeneration,
		ResourceFence: input.ResourceFence, ReopenedAt: formatCleanupTime(input.ReopenedAt),
	}
	return cleanupMutationDigest(payload.Schema, payload)
}

func cleanupDebtDecisionFromRecord(record cleanupDebtRecord, capacityReleased bool) CleanupDebtRuntimeDecision {
	decision := CleanupDebtRuntimeDecision{
		DebtID: record.DebtID, DebtRevision: record.Revision, Status: record.Status,
		Unresolved: record.Unresolved, Uncontained: record.Uncontained,
		RetryDisposition: record.RetryDisposition, ResolutionClass: record.ResolutionClass,
		ResolutionReason: record.ResolutionReason, ExceptionUntil: record.ResolutionExpiresAt,
		CapacityReleased: capacityReleased,
	}
	return decision
}

func validCleanupDebtDecisionState(decision CleanupDebtRuntimeDecision) bool {
	if !validOpaqueID(decision.DebtID) || decision.DebtRevision == 0 ||
		decision.Status < cleanupDebtOpen || decision.Status > cleanupDebtResolved {
		return false
	}
	if decision.Status != cleanupDebtResolved {
		if !decision.Unresolved || decision.ResolutionClass != 0 || decision.ResolutionReason != 0 ||
			!decision.ExceptionUntil.IsZero() {
			return false
		}
	} else if decision.Unresolved || decision.ResolutionClass < cleanupResolutionReclaimed ||
		decision.ResolutionClass > cleanupResolutionAcceptedException ||
		decision.ResolutionReason < cleanupResolutionCleanupProven ||
		decision.ResolutionReason > cleanupResolutionAdministratorException {
		return false
	}
	if decision.Status == cleanupDebtResolved {
		if decision.CapacityReleased {
			return decision.ResolutionClass == cleanupResolutionReclaimed ||
				decision.ResolutionClass == cleanupResolutionAlreadyAbsent
		}
		return decision.ResolutionClass == cleanupResolutionAcceptedException ||
			decision.ResolutionClass == cleanupResolutionRetainedByAuthority
	}
	return true
}
