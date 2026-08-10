package runtimeexecution

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"time"
)

const (
	postgresCleanupOwnerModule = "runtime_execution"
	postgresCleanupStateSchema = "slidesmith.runtime-execution.cleanup-debt/v1"
	postgresCleanupStateDomain = postgresCleanupStateSchema + "\n"
)

type cleanupResourceClass uint8

const (
	cleanupResourceProcess cleanupResourceClass = iota + 1
	cleanupResourceSandbox
	cleanupResourceLease
	cleanupResourceContainment
	cleanupResourceReset
)

type cleanupIntent uint8

const (
	cleanupIntentReclaim cleanupIntent = iota + 1
	cleanupIntentContain
	cleanupIntentReset
)

type cleanupDebtStatus uint8

const (
	cleanupDebtOpen cleanupDebtStatus = iota + 1
	cleanupDebtClaimed
	cleanupDebtRetryScheduled
	cleanupDebtBlocked
	cleanupDebtResolved
)

type cleanupRetryDisposition uint8

const (
	cleanupRetryNone cleanupRetryDisposition = iota
	cleanupRetryReady
	cleanupRetryClaimed
	cleanupRetryScheduled
	cleanupRetryBlocked
)

type cleanupFailureCategory uint8

const (
	cleanupFailureNone cleanupFailureCategory = iota
	cleanupFailureUnavailable
	cleanupFailureAuthorizationDenied
	cleanupFailureIntegrityConflict
)

type cleanupEstimateState uint8

const (
	cleanupEstimateUnknown cleanupEstimateState = iota + 1
	cleanupEstimateKnown
)

type cleanupEstimateMethod uint8

const (
	cleanupEstimateMethodUnknown cleanupEstimateMethod = iota
	cleanupEstimateAdapterObservation
	cleanupEstimateInventory
)

type cleanupBlockerClass uint16

const (
	cleanupBlockerLease cleanupBlockerClass = 1 << iota
	cleanupBlockerReference
	cleanupBlockerIncident
	cleanupBlockerGracePeriod
	cleanupBlockerQuarantine
	cleanupBlockerMask = cleanupBlockerLease | cleanupBlockerReference | cleanupBlockerIncident |
		cleanupBlockerGracePeriod | cleanupBlockerQuarantine
)

type cleanupResolutionClass uint8

const (
	cleanupResolutionReclaimed cleanupResolutionClass = iota + 1
	cleanupResolutionAlreadyAbsent
	cleanupResolutionRetainedByAuthority
	cleanupResolutionAcceptedException
)

type cleanupResolutionReason uint8

const (
	cleanupResolutionCleanupProven cleanupResolutionReason = iota + 1
	cleanupResolutionExactGenerationAbsent
	cleanupResolutionCurrentAuthorityRetention
	cleanupResolutionAdministratorException
)

type cleanupEstimation struct {
	State      cleanupEstimateState
	Method     cleanupEstimateMethod
	Bytes      uint64
	Inodes     uint64
	ObservedAt time.Time
}

type cleanupBlockerSummary struct {
	Classes cleanupBlockerClass
	Digest  Digest
}

type cleanupDebtRecord struct {
	DebtID                      string
	Revision                    uint64
	OwnerModule                 string
	PersonalWorkspaceID         PersonalWorkspaceID
	TaskID                      TaskID
	PhaseRunID                  PhaseRunID
	RuntimeRunID                RuntimeRunID
	OwnerAuthority              RuntimeAuthority
	ResourceClass               cleanupResourceClass
	ResourceIdentityDigest      Digest
	ResourceGeneration          uint64
	ResourceFence               uint64
	CleanupIntent               cleanupIntent
	CauseDecisionID             RuntimeDecisionID
	CauseOperationID            OperationID
	RetentionFactDigest         Digest
	EligibilityFactDigest       Digest
	Status                      cleanupDebtStatus
	Unresolved                  bool
	Uncontained                 bool
	CreatedAt                   time.Time
	EligibleAt                  time.Time
	FirstAttemptAt              time.Time
	LastAttemptAt               time.Time
	NextRetryAt                 time.Time
	AttemptCount                uint64
	ConsecutiveFailureCount     uint64
	ClaimGeneration             uint64
	ClaimFence                  uint64
	RetryDisposition            cleanupRetryDisposition
	LastErrorCategory           cleanupFailureCategory
	LastErrorDigest             Digest
	LastErrorEvidenceReference  string
	Estimation                  cleanupEstimation
	Blockers                    cleanupBlockerSummary
	ResolvedAt                  time.Time
	ResolutionClass             cleanupResolutionClass
	ResolutionReason            cleanupResolutionReason
	ResolutionAuthority         RuntimeAuthority
	ResolutionAuditFactID       string
	ResolutionEvidenceRoot      EvidenceRootSnapshot
	ResolutionExpiresAt         time.Time
	ResolutionIncidentReference string
	ResolutionTicketReference   string
	ResolutionApprovalReference string
	LastMutationID              string
}

type canonicalCleanupDebtState struct {
	Schema                        string                  `json:"schema"`
	DebtID                        string                  `json:"debt_id"`
	Revision                      uint64                  `json:"revision"`
	OwnerModule                   string                  `json:"owner_module"`
	PersonalWorkspaceID           string                  `json:"personal_workspace_id"`
	TaskID                        string                  `json:"task_id"`
	PhaseRunID                    string                  `json:"phase_run_id"`
	RuntimeRunID                  string                  `json:"runtime_run_id"`
	OwnerAuthorityKind            AuthorityKind           `json:"owner_authority_kind"`
	OwnerAuthorityID              string                  `json:"owner_authority_id"`
	OwnerAuthorityGeneration      AuthorizationGeneration `json:"owner_authority_generation"`
	ResourceClass                 cleanupResourceClass    `json:"resource_class"`
	ResourceIdentityDigest        string                  `json:"resource_identity_digest"`
	ResourceGeneration            uint64                  `json:"resource_generation"`
	ResourceFence                 uint64                  `json:"resource_fence"`
	CleanupIntent                 cleanupIntent           `json:"cleanup_intent"`
	CauseDecisionID               string                  `json:"cause_decision_id"`
	CauseOperationID              string                  `json:"cause_operation_id"`
	RetentionFactDigest           string                  `json:"retention_fact_digest"`
	EligibilityFactDigest         string                  `json:"eligibility_fact_digest"`
	Status                        cleanupDebtStatus       `json:"status"`
	Unresolved                    bool                    `json:"unresolved"`
	Uncontained                   bool                    `json:"uncontained"`
	CreatedAt                     string                  `json:"created_at"`
	EligibleAt                    string                  `json:"eligible_at"`
	FirstAttemptAt                string                  `json:"first_attempt_at"`
	LastAttemptAt                 string                  `json:"last_attempt_at"`
	NextRetryAt                   string                  `json:"next_retry_at"`
	AttemptCount                  uint64                  `json:"attempt_count"`
	ConsecutiveFailureCount       uint64                  `json:"consecutive_failure_count"`
	ClaimGeneration               uint64                  `json:"claim_generation"`
	ClaimFence                    uint64                  `json:"claim_fence"`
	RetryDisposition              cleanupRetryDisposition `json:"retry_disposition"`
	LastErrorCategory             cleanupFailureCategory  `json:"last_error_category"`
	LastErrorDigest               string                  `json:"last_error_digest"`
	LastErrorEvidenceReference    string                  `json:"last_error_evidence_reference"`
	EstimateState                 cleanupEstimateState    `json:"estimate_state"`
	EstimateMethod                cleanupEstimateMethod   `json:"estimate_method"`
	EstimatedBytes                uint64                  `json:"estimated_bytes"`
	EstimatedInodes               uint64                  `json:"estimated_inodes"`
	EstimateObservedAt            string                  `json:"estimate_observed_at"`
	BlockerClasses                cleanupBlockerClass     `json:"blocker_classes"`
	BlockerDigest                 string                  `json:"blocker_digest"`
	ResolvedAt                    string                  `json:"resolved_at"`
	ResolutionClass               cleanupResolutionClass  `json:"resolution_class"`
	ResolutionReason              cleanupResolutionReason `json:"resolution_reason"`
	ResolutionAuthorityKind       AuthorityKind           `json:"resolution_authority_kind"`
	ResolutionAuthorityID         string                  `json:"resolution_authority_id"`
	ResolutionAuthorityGeneration AuthorizationGeneration `json:"resolution_authority_generation"`
	ResolutionAuditFactID         string                  `json:"resolution_audit_fact_id"`
	ResolutionEvidenceSchema      SchemaVersion           `json:"resolution_evidence_schema"`
	ResolutionEvidenceRootID      string                  `json:"resolution_evidence_root_id"`
	ResolutionEvidenceRootDigest  string                  `json:"resolution_evidence_root_digest"`
	ResolutionExpiresAt           string                  `json:"resolution_expires_at"`
	ResolutionIncidentReference   string                  `json:"resolution_incident_reference,omitempty"`
	ResolutionTicketReference     string                  `json:"resolution_ticket_reference,omitempty"`
	ResolutionApprovalReference   string                  `json:"resolution_approval_reference,omitempty"`
	LastMutationID                string                  `json:"last_mutation_id"`
}

func normalizedCleanupEstimation(estimation cleanupEstimation) cleanupEstimation {
	if estimation.State == cleanupEstimateKnown {
		estimation.ObservedAt = postgresTimestamp(estimation.ObservedAt)
	}
	return estimation
}

func normalizeCleanupDebtRecord(record cleanupDebtRecord) cleanupDebtRecord {
	record.CreatedAt = postgresTimestamp(record.CreatedAt)
	record.EligibleAt = postgresTimestamp(record.EligibleAt)
	record.FirstAttemptAt = optionalPostgresTimestamp(record.FirstAttemptAt)
	record.LastAttemptAt = optionalPostgresTimestamp(record.LastAttemptAt)
	record.NextRetryAt = optionalPostgresTimestamp(record.NextRetryAt)
	record.Estimation = normalizedCleanupEstimation(record.Estimation)
	record.ResolvedAt = optionalPostgresTimestamp(record.ResolvedAt)
	record.ResolutionExpiresAt = optionalPostgresTimestamp(record.ResolutionExpiresAt)
	return record
}

func optionalPostgresTimestamp(value time.Time) time.Time {
	if value.IsZero() {
		return time.Time{}
	}
	return postgresTimestamp(value)
}

func validCleanupEstimation(estimation cleanupEstimation) bool {
	switch estimation.State {
	case cleanupEstimateUnknown:
		return estimation.Method == cleanupEstimateMethodUnknown && estimation.Bytes == 0 &&
			estimation.Inodes == 0 && estimation.ObservedAt.IsZero()
	case cleanupEstimateKnown:
		return (estimation.Method == cleanupEstimateAdapterObservation || estimation.Method == cleanupEstimateInventory) &&
			!estimation.ObservedAt.IsZero() && estimation.ObservedAt.Equal(postgresTimestamp(estimation.ObservedAt))
	default:
		return false
	}
}

func validCleanupBlockers(blockers cleanupBlockerSummary) bool {
	if blockers.Classes&^cleanupBlockerMask != 0 {
		return false
	}
	if blockers.Classes == 0 {
		return blockers.Digest == (Digest{})
	}
	return blockers.Digest != (Digest{})
}

func validCleanupDebtRecord(record cleanupDebtRecord) bool {
	if !validOpaqueID(record.DebtID) || record.Revision == 0 || record.OwnerModule != postgresCleanupOwnerModule ||
		!validOpaqueID(record.PersonalWorkspaceID.String()) || !validOpaqueID(record.TaskID.String()) ||
		!validOpaqueID(record.PhaseRunID.String()) || !validOpaqueID(record.RuntimeRunID.String()) ||
		!validAuthority(record.OwnerAuthority) || record.ResourceClass < cleanupResourceProcess ||
		record.ResourceClass > cleanupResourceReset || record.ResourceIdentityDigest == (Digest{}) ||
		record.ResourceGeneration == 0 || record.ResourceFence == 0 ||
		record.CleanupIntent < cleanupIntentReclaim || record.CleanupIntent > cleanupIntentReset ||
		!validOpaqueID(record.CauseDecisionID.String()) || !validOpaqueID(record.CauseOperationID.String()) ||
		record.RetentionFactDigest == (Digest{}) || record.EligibilityFactDigest == (Digest{}) ||
		record.CreatedAt.IsZero() || record.EligibleAt.IsZero() ||
		!record.CreatedAt.Equal(postgresTimestamp(record.CreatedAt)) ||
		!record.EligibleAt.Equal(postgresTimestamp(record.EligibleAt)) ||
		!validCleanupEstimation(record.Estimation) || !validCleanupBlockers(record.Blockers) ||
		!validOpaqueID(record.LastMutationID) {
		return false
	}
	if record.AttemptCount == 0 {
		if !record.FirstAttemptAt.IsZero() || !record.LastAttemptAt.IsZero() || record.ConsecutiveFailureCount != 0 ||
			record.ClaimGeneration != 0 || record.ClaimFence != 0 || record.LastErrorCategory != cleanupFailureNone ||
			record.LastErrorDigest != (Digest{}) || record.LastErrorEvidenceReference != "" {
			return false
		}
	} else if record.FirstAttemptAt.IsZero() || record.LastAttemptAt.IsZero() ||
		record.FirstAttemptAt.After(record.LastAttemptAt) || record.ConsecutiveFailureCount == 0 ||
		record.ConsecutiveFailureCount > record.AttemptCount || record.ClaimGeneration == 0 || record.ClaimFence == 0 ||
		record.LastErrorCategory < cleanupFailureUnavailable || record.LastErrorCategory > cleanupFailureIntegrityConflict ||
		record.LastErrorDigest == (Digest{}) ||
		(record.LastErrorEvidenceReference != "" && !validOpaqueID(record.LastErrorEvidenceReference)) ||
		!record.FirstAttemptAt.Equal(postgresTimestamp(record.FirstAttemptAt)) ||
		!record.LastAttemptAt.Equal(postgresTimestamp(record.LastAttemptAt)) {
		return false
	}
	if !record.NextRetryAt.IsZero() && !record.NextRetryAt.Equal(postgresTimestamp(record.NextRetryAt)) {
		return false
	}
	if record.Status == cleanupDebtResolved {
		return validResolvedCleanupDebt(record)
	}
	if !record.Unresolved || !record.ResolvedAt.IsZero() || record.ResolutionClass != 0 ||
		record.ResolutionReason != 0 || record.ResolutionAuthority != (RuntimeAuthority{}) ||
		record.ResolutionAuditFactID != "" || record.ResolutionEvidenceRoot != (EvidenceRootSnapshot{}) ||
		!record.ResolutionExpiresAt.IsZero() {
		return false
	}
	switch record.Status {
	case cleanupDebtOpen:
		return record.RetryDisposition == cleanupRetryReady && record.Blockers.Classes == 0
	case cleanupDebtClaimed:
		return record.RetryDisposition == cleanupRetryClaimed && record.ClaimGeneration > 0 && record.ClaimFence > 0
	case cleanupDebtRetryScheduled:
		return record.RetryDisposition == cleanupRetryScheduled && record.AttemptCount > 0 && record.Blockers.Classes == 0
	case cleanupDebtBlocked:
		return record.RetryDisposition == cleanupRetryBlocked && record.Blockers.Classes != 0
	default:
		return false
	}
}

func validResolvedCleanupDebt(record cleanupDebtRecord) bool {
	if record.Unresolved || record.RetryDisposition != cleanupRetryNone || record.ResolvedAt.IsZero() ||
		!record.ResolvedAt.Equal(postgresTimestamp(record.ResolvedAt)) ||
		record.ResolutionClass < cleanupResolutionReclaimed ||
		record.ResolutionClass > cleanupResolutionAcceptedException ||
		record.ResolutionReason < cleanupResolutionCleanupProven ||
		record.ResolutionReason > cleanupResolutionAdministratorException ||
		!validAuthority(record.ResolutionAuthority) || !validOpaqueID(record.ResolutionAuditFactID) ||
		!knownEvidenceRoot(record.ResolutionEvidenceRoot) ||
		record.ResolutionEvidenceRoot.EvidenceRootID == (EvidenceRootID{}) {
		return false
	}
	return validCleanupResolutionDisposition(record)
}

func validCleanupResolutionDisposition(record cleanupDebtRecord) bool {
	switch record.ResolutionClass {
	case cleanupResolutionReclaimed:
		return record.ResolutionReason == cleanupResolutionCleanupProven && !record.Uncontained &&
			record.Blockers.Classes == 0 && record.ResolutionExpiresAt.IsZero() &&
			record.ResolutionApprovalReference == "" && record.ResolutionIncidentReference == "" &&
			record.ResolutionTicketReference == ""
	case cleanupResolutionAlreadyAbsent:
		return record.ResolutionReason == cleanupResolutionExactGenerationAbsent && !record.Uncontained &&
			record.Blockers.Classes == 0 && record.ResolutionExpiresAt.IsZero() &&
			record.ResolutionApprovalReference == "" && record.ResolutionIncidentReference == "" &&
			record.ResolutionTicketReference == ""
	case cleanupResolutionRetainedByAuthority:
		return record.ResolutionReason == cleanupResolutionCurrentAuthorityRetention &&
			record.Blockers.Classes != 0 && record.ResolutionExpiresAt.IsZero() &&
			record.ResolutionApprovalReference == "" && record.ResolutionIncidentReference == "" &&
			record.ResolutionTicketReference == ""
	case cleanupResolutionAcceptedException:
		return record.ResolutionReason == cleanupResolutionAdministratorException &&
			!record.ResolutionExpiresAt.IsZero() && record.ResolutionExpiresAt.After(record.ResolvedAt) &&
			record.ResolutionExpiresAt.Equal(postgresTimestamp(record.ResolutionExpiresAt)) &&
			validOpaqueID(record.ResolutionApprovalReference) &&
			(record.ResolutionIncidentReference == "" || validOpaqueID(record.ResolutionIncidentReference)) &&
			(record.ResolutionTicketReference == "" || validOpaqueID(record.ResolutionTicketReference))
	default:
		return false
	}
}

func encodeCleanupDebtRecord(record cleanupDebtRecord) ([]byte, Digest, error) {
	record = normalizeCleanupDebtRecord(record)
	if !validCleanupDebtRecord(record) {
		return nil, Digest{}, newPersistenceError(PersistenceStateCorrupt)
	}
	state := canonicalCleanupDebtState{
		Schema: postgresCleanupStateSchema, DebtID: record.DebtID, Revision: record.Revision,
		OwnerModule: record.OwnerModule, PersonalWorkspaceID: record.PersonalWorkspaceID.String(),
		TaskID: record.TaskID.String(), PhaseRunID: record.PhaseRunID.String(), RuntimeRunID: record.RuntimeRunID.String(),
		OwnerAuthorityKind: record.OwnerAuthority.kind, OwnerAuthorityID: record.OwnerAuthority.id.String(),
		OwnerAuthorityGeneration: record.OwnerAuthority.generation, ResourceClass: record.ResourceClass,
		ResourceIdentityDigest: record.ResourceIdentityDigest.String(), ResourceGeneration: record.ResourceGeneration,
		ResourceFence: record.ResourceFence, CleanupIntent: record.CleanupIntent,
		CauseDecisionID: record.CauseDecisionID.String(), CauseOperationID: record.CauseOperationID.String(),
		RetentionFactDigest: record.RetentionFactDigest.String(), EligibilityFactDigest: record.EligibilityFactDigest.String(),
		Status: record.Status, Unresolved: record.Unresolved, Uncontained: record.Uncontained,
		CreatedAt: formatCleanupTime(record.CreatedAt), EligibleAt: formatCleanupTime(record.EligibleAt),
		FirstAttemptAt: formatOptionalCleanupTime(record.FirstAttemptAt),
		LastAttemptAt:  formatOptionalCleanupTime(record.LastAttemptAt), NextRetryAt: formatOptionalCleanupTime(record.NextRetryAt),
		AttemptCount: record.AttemptCount, ConsecutiveFailureCount: record.ConsecutiveFailureCount,
		ClaimGeneration: record.ClaimGeneration, ClaimFence: record.ClaimFence,
		RetryDisposition: record.RetryDisposition, LastErrorCategory: record.LastErrorCategory,
		LastErrorDigest:            formatOptionalCleanupDigest(record.LastErrorDigest),
		LastErrorEvidenceReference: record.LastErrorEvidenceReference,
		EstimateState:              record.Estimation.State, EstimateMethod: record.Estimation.Method,
		EstimatedBytes: record.Estimation.Bytes, EstimatedInodes: record.Estimation.Inodes,
		EstimateObservedAt: formatOptionalCleanupTime(record.Estimation.ObservedAt),
		BlockerClasses:     record.Blockers.Classes, BlockerDigest: formatOptionalCleanupDigest(record.Blockers.Digest),
		ResolvedAt: formatOptionalCleanupTime(record.ResolvedAt), ResolutionClass: record.ResolutionClass,
		ResolutionReason: record.ResolutionReason, ResolutionAuthorityKind: record.ResolutionAuthority.kind,
		ResolutionAuthorityID:         record.ResolutionAuthority.id.String(),
		ResolutionAuthorityGeneration: record.ResolutionAuthority.generation,
		ResolutionAuditFactID:         record.ResolutionAuditFactID,
		ResolutionEvidenceSchema:      record.ResolutionEvidenceRoot.SchemaVersion,
		ResolutionEvidenceRootID:      record.ResolutionEvidenceRoot.EvidenceRootID.String(),
		ResolutionEvidenceRootDigest:  formatOptionalCleanupDigest(record.ResolutionEvidenceRoot.Digest),
		ResolutionExpiresAt:           formatOptionalCleanupTime(record.ResolutionExpiresAt), LastMutationID: record.LastMutationID,
		ResolutionIncidentReference: record.ResolutionIncidentReference,
		ResolutionTicketReference:   record.ResolutionTicketReference,
		ResolutionApprovalReference: record.ResolutionApprovalReference,
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return nil, Digest{}, newPersistenceError(PersistenceStateCorrupt)
	}
	return encoded, digestBytes(append([]byte(postgresCleanupStateDomain), encoded...)), nil
}

func decodeCleanupDebtRecord(encoded []byte) (cleanupDebtRecord, Digest, error) {
	var state canonicalCleanupDebtState
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil || ensureJSONEOF(decoder) != nil || state.Schema != postgresCleanupStateSchema {
		return cleanupDebtRecord{}, Digest{}, newPersistenceError(PersistenceStateCorrupt)
	}
	resourceDigest, ok := parseCleanupDigest(state.ResourceIdentityDigest, true)
	if !ok {
		return cleanupDebtRecord{}, Digest{}, newPersistenceError(PersistenceStateCorrupt)
	}
	retentionDigest, ok := parseCleanupDigest(state.RetentionFactDigest, true)
	if !ok {
		return cleanupDebtRecord{}, Digest{}, newPersistenceError(PersistenceStateCorrupt)
	}
	eligibilityDigest, ok := parseCleanupDigest(state.EligibilityFactDigest, true)
	if !ok {
		return cleanupDebtRecord{}, Digest{}, newPersistenceError(PersistenceStateCorrupt)
	}
	lastErrorDigest, ok := parseCleanupDigest(state.LastErrorDigest, false)
	if !ok {
		return cleanupDebtRecord{}, Digest{}, newPersistenceError(PersistenceStateCorrupt)
	}
	blockerDigest, ok := parseCleanupDigest(state.BlockerDigest, false)
	if !ok {
		return cleanupDebtRecord{}, Digest{}, newPersistenceError(PersistenceStateCorrupt)
	}
	resolutionEvidenceDigest, ok := parseCleanupDigest(state.ResolutionEvidenceRootDigest, false)
	if !ok {
		return cleanupDebtRecord{}, Digest{}, newPersistenceError(PersistenceStateCorrupt)
	}
	record := cleanupDebtRecord{
		DebtID: state.DebtID, Revision: state.Revision, OwnerModule: state.OwnerModule,
		PersonalWorkspaceID: PersonalWorkspaceID{value: state.PersonalWorkspaceID}, TaskID: TaskID{value: state.TaskID},
		PhaseRunID: PhaseRunID{value: state.PhaseRunID}, RuntimeRunID: RuntimeRunID{value: state.RuntimeRunID},
		OwnerAuthority: RuntimeAuthority{id: AuthorityID{value: state.OwnerAuthorityID},
			generation: state.OwnerAuthorityGeneration, kind: state.OwnerAuthorityKind},
		ResourceClass: state.ResourceClass, ResourceIdentityDigest: resourceDigest,
		ResourceGeneration: state.ResourceGeneration, ResourceFence: state.ResourceFence,
		CleanupIntent: state.CleanupIntent, CauseDecisionID: RuntimeDecisionID{value: state.CauseDecisionID},
		CauseOperationID: OperationID{value: state.CauseOperationID}, RetentionFactDigest: retentionDigest,
		EligibilityFactDigest: eligibilityDigest, Status: state.Status, Unresolved: state.Unresolved,
		Uncontained: state.Uncontained, AttemptCount: state.AttemptCount,
		ConsecutiveFailureCount: state.ConsecutiveFailureCount, ClaimGeneration: state.ClaimGeneration,
		ClaimFence: state.ClaimFence, RetryDisposition: state.RetryDisposition,
		LastErrorCategory: state.LastErrorCategory, LastErrorDigest: lastErrorDigest,
		LastErrorEvidenceReference: state.LastErrorEvidenceReference,
		Estimation: cleanupEstimation{State: state.EstimateState, Method: state.EstimateMethod,
			Bytes: state.EstimatedBytes, Inodes: state.EstimatedInodes},
		Blockers:        cleanupBlockerSummary{Classes: state.BlockerClasses, Digest: blockerDigest},
		ResolutionClass: state.ResolutionClass, ResolutionReason: state.ResolutionReason,
		ResolutionAuthority: RuntimeAuthority{id: AuthorityID{value: state.ResolutionAuthorityID},
			generation: state.ResolutionAuthorityGeneration, kind: state.ResolutionAuthorityKind},
		ResolutionAuditFactID: state.ResolutionAuditFactID,
		ResolutionEvidenceRoot: EvidenceRootSnapshot{SchemaVersion: state.ResolutionEvidenceSchema,
			EvidenceRootID: EvidenceRootID{value: state.ResolutionEvidenceRootID}, Digest: resolutionEvidenceDigest},
		ResolutionIncidentReference: state.ResolutionIncidentReference,
		ResolutionTicketReference:   state.ResolutionTicketReference,
		ResolutionApprovalReference: state.ResolutionApprovalReference,
		LastMutationID:              state.LastMutationID,
	}
	var err error
	if record.CreatedAt, err = parseCleanupTime(state.CreatedAt, true); err != nil {
		return cleanupDebtRecord{}, Digest{}, err
	}
	if record.EligibleAt, err = parseCleanupTime(state.EligibleAt, true); err != nil {
		return cleanupDebtRecord{}, Digest{}, err
	}
	if record.FirstAttemptAt, err = parseCleanupTime(state.FirstAttemptAt, false); err != nil {
		return cleanupDebtRecord{}, Digest{}, err
	}
	if record.LastAttemptAt, err = parseCleanupTime(state.LastAttemptAt, false); err != nil {
		return cleanupDebtRecord{}, Digest{}, err
	}
	if record.NextRetryAt, err = parseCleanupTime(state.NextRetryAt, false); err != nil {
		return cleanupDebtRecord{}, Digest{}, err
	}
	if record.Estimation.ObservedAt, err = parseCleanupTime(state.EstimateObservedAt, false); err != nil {
		return cleanupDebtRecord{}, Digest{}, err
	}
	if record.ResolvedAt, err = parseCleanupTime(state.ResolvedAt, false); err != nil {
		return cleanupDebtRecord{}, Digest{}, err
	}
	if record.ResolutionExpiresAt, err = parseCleanupTime(state.ResolutionExpiresAt, false); err != nil {
		return cleanupDebtRecord{}, Digest{}, err
	}
	if !validCleanupDebtRecord(record) {
		return cleanupDebtRecord{}, Digest{}, newPersistenceError(PersistenceStateCorrupt)
	}
	canonical, err := json.Marshal(state)
	if err != nil {
		return cleanupDebtRecord{}, Digest{}, newPersistenceError(PersistenceStateCorrupt)
	}
	digest := digestBytes(append([]byte(postgresCleanupStateDomain), canonical...))
	return record, digest, nil
}

func formatCleanupTime(value time.Time) string {
	return postgresTimestamp(value).Format(canonicalTimeFormat)
}

func formatOptionalCleanupTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return formatCleanupTime(value)
}

func parseCleanupTime(value string, required bool) (time.Time, error) {
	if value == "" && !required {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(canonicalTimeFormat, value)
	if err != nil || parsed.Format(canonicalTimeFormat) != value {
		return time.Time{}, newPersistenceError(PersistenceStateCorrupt)
	}
	return parsed, nil
}

func formatOptionalCleanupDigest(value Digest) string {
	if value == (Digest{}) {
		return ""
	}
	return value.String()
}

func parseCleanupDigest(value string, required bool) (Digest, bool) {
	if value == "" && !required {
		return Digest{}, true
	}
	if !validDigestText(value) {
		return Digest{}, false
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != len(Digest{}) {
		return Digest{}, false
	}
	var digest Digest
	copy(digest[:], decoded)
	return digest, true
}
