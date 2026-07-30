package runtimeexecution

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"time"
)

const canonicalRequestDomain = "slidesmith.runtime-execution.request/v1\n"
const canonicalTimeFormat = "2006-01-02T15:04:05.000000000Z"

type canonicalSchema struct {
	Major uint16 `json:"major"`
	Minor uint16 `json:"minor"`
}

type canonicalAuthority struct {
	Kind       string `json:"kind"`
	ID         string `json:"id"`
	Generation uint64 `json:"generation"`
}

type canonicalInput struct {
	Identity  string `json:"identity"`
	Digest    string `json:"digest"`
	SizeBytes uint64 `json:"size_bytes"`
}

type canonicalInputManifest struct {
	Identity                      string          `json:"identity"`
	Schema                        canonicalSchema `json:"schema"`
	Digest                        string          `json:"digest"`
	TotalSizeBytes                uint64          `json:"total_size_bytes"`
	InputCount                    uint64          `json:"input_count"`
	MaterializationEvidenceID     string          `json:"materialization_evidence_id"`
	MaterializationEvidenceDigest string          `json:"materialization_evidence_digest"`
}

type canonicalRuntimeViewRequirement struct {
	TaskWorkspaceID         string `json:"task_workspace_id"`
	MaterializationID       string `json:"materialization_id"`
	BaseRevisionID          string `json:"base_revision_id"`
	LifecycleGeneration     uint64 `json:"lifecycle_generation"`
	LifecycleFence          uint64 `json:"lifecycle_fence"`
	ExpiryPolicy            string `json:"expiry_policy"`
	OpenOperationDerivation string `json:"open_operation_derivation"`
}

type canonicalCatalogBinding struct {
	TemplateLockID     string `json:"template_lock_id"`
	TemplateLockDigest string `json:"template_lock_digest"`
	ClosureRootDigest  string `json:"closure_root_digest"`
	SafetyEpoch        uint64 `json:"safety_epoch"`
}

type canonicalProviderBinding struct {
	QuotaReservationID           string `json:"quota_reservation_id"`
	Generation                   uint64 `json:"generation"`
	Mode                         string `json:"mode"`
	GatewayRoutePolicyID         string `json:"gateway_route_policy_id"`
	GatewayRoutePolicyGeneration uint64 `json:"gateway_route_policy_generation"`
	CapabilityScope              uint64 `json:"capability_scope"`
	RoutePolicyExpiresAt         string `json:"route_policy_expires_at"`
}

type canonicalStart struct {
	Schema                      canonicalSchema                  `json:"schema"`
	Kind                        string                           `json:"kind"`
	OperationID                 string                           `json:"operation_id"`
	PersonalWorkspaceID         string                           `json:"personal_workspace_id"`
	TaskID                      string                           `json:"task_id"`
	PhaseRunID                  string                           `json:"phase_run_id"`
	RuntimeRunID                string                           `json:"runtime_run_id"`
	Attempt                     uint32                           `json:"attempt"`
	ExpectedTaskRevision        uint64                           `json:"expected_task_revision"`
	ExpectedRuntimeRevision     uint64                           `json:"expected_runtime_revision"`
	ExpectedOperationGeneration uint64                           `json:"expected_operation_generation"`
	ExpectedRuntimeFence        uint64                           `json:"expected_runtime_fence"`
	Authority                   canonicalAuthority               `json:"authority"`
	RuntimeBindingID            string                           `json:"runtime_binding_id"`
	RuntimeBindingDigest        string                           `json:"runtime_binding_digest"`
	ExecutionLockDigest         string                           `json:"execution_lock_digest"`
	CapabilityContractDigest    string                           `json:"capability_contract_digest"`
	AllowedPlatformImagesDigest string                           `json:"allowed_platform_images_digest"`
	ExecutorContractDigest      string                           `json:"executor_contract_digest"`
	ReleaseSafetyEpoch          uint64                           `json:"release_safety_epoch"`
	CatalogBinding              *canonicalCatalogBinding         `json:"catalog_binding"`
	WorkerClass                 string                           `json:"worker_class"`
	Effect                      string                           `json:"effect"`
	ImmutableInputManifest      canonicalInputManifest           `json:"immutable_input_manifest"`
	ImmutableInputs             []canonicalInput                 `json:"immutable_inputs"`
	OutputContractDigest        string                           `json:"output_contract_digest"`
	EvidenceContractDigest      string                           `json:"evidence_contract_digest"`
	RuntimeViewRequirement      *canonicalRuntimeViewRequirement `json:"runtime_view_requirement"`
	ResourceClassID             string                           `json:"resource_class_id"`
	ExecutionPolicyID           string                           `json:"execution_policy_id"`
	ProviderCapability          string                           `json:"provider_capability"`
	ProviderBinding             *canonicalProviderBinding        `json:"provider_binding"`
	NetworkPolicyID             string                           `json:"network_policy_id"`
	SecretPolicyID              string                           `json:"secret_policy_id"`
	Deadline                    string                           `json:"deadline"`
	CancellationPolicy          string                           `json:"cancellation_policy"`
}

func computeStartDigest(command StartRuntimeRun) (Digest, error) {
	encoded, err := canonicalStartEncoding(command)
	if err != nil {
		return Digest{}, err
	}
	return Digest(sha256.Sum256(append([]byte(canonicalRequestDomain), encoded...))), nil
}

func canonicalStartEncoding(command StartRuntimeRun) ([]byte, error) {
	if !validCanonicalStart(command) {
		return nil, newError(ErrorInvalidRequest)
	}
	inputs := make([]canonicalInput, len(command.ImmutableInputs))
	for index, input := range command.ImmutableInputs {
		inputs[index] = canonicalInput{
			Identity: input.Identity.String(), Digest: input.Digest.String(), SizeBytes: input.SizeBytes,
		}
	}
	sort.Slice(inputs, func(left, right int) bool { return inputs[left].Identity < inputs[right].Identity })
	for index := 1; index < len(inputs); index++ {
		if inputs[index-1].Identity == inputs[index].Identity {
			return nil, newError(ErrorInvalidRequest)
		}
	}
	var view *canonicalRuntimeViewRequirement
	if command.RuntimeViewRequirement != nil {
		view = &canonicalRuntimeViewRequirement{
			TaskWorkspaceID:         command.RuntimeViewRequirement.TaskWorkspaceID.String(),
			MaterializationID:       command.RuntimeViewRequirement.MaterializationID.String(),
			BaseRevisionID:          command.RuntimeViewRequirement.BaseRevisionID.String(),
			LifecycleGeneration:     uint64(command.RuntimeViewRequirement.LifecycleGeneration),
			LifecycleFence:          uint64(command.RuntimeViewRequirement.LifecycleFence),
			ExpiryPolicy:            runtimeViewExpiryPolicyName(command.RuntimeViewRequirement.ExpiryPolicy),
			OpenOperationDerivation: command.RuntimeViewRequirement.OpenOperationDerivation.String(),
		}
	}
	var catalog *canonicalCatalogBinding
	if command.CatalogBinding != nil {
		catalog = &canonicalCatalogBinding{
			TemplateLockID: command.CatalogBinding.TemplateLockID.String(), TemplateLockDigest: command.CatalogBinding.TemplateLockDigest.String(),
			ClosureRootDigest: command.CatalogBinding.ClosureRootDigest.String(), SafetyEpoch: uint64(command.CatalogBinding.SafetyEpoch),
		}
	}
	var provider *canonicalProviderBinding
	if command.ProviderBinding != nil {
		provider = &canonicalProviderBinding{
			QuotaReservationID: command.ProviderBinding.QuotaReservationID.String(), Generation: uint64(command.ProviderBinding.Generation),
			Mode: quotaReservationModeName(command.ProviderBinding.Mode), GatewayRoutePolicyID: command.ProviderBinding.GatewayRoutePolicyID.String(),
			GatewayRoutePolicyGeneration: uint64(command.ProviderBinding.GatewayRoutePolicyGeneration),
			CapabilityScope:              uint64(command.ProviderBinding.CapabilityScope),
			RoutePolicyExpiresAt:         command.ProviderBinding.RoutePolicyExpiresAt.UTC().Format(canonicalTimeFormat),
		}
	}
	encoded, err := json.Marshal(canonicalStart{
		Schema: canonicalSchema{Major: command.SchemaVersion.Major(), Minor: command.SchemaVersion.Minor()},
		Kind:   "start_runtime_run", OperationID: command.OperationID.String(),
		PersonalWorkspaceID: command.PersonalWorkspaceID.String(), TaskID: command.TaskID.String(),
		PhaseRunID: command.PhaseRunID.String(), RuntimeRunID: command.RuntimeRunID.String(),
		Attempt: command.Attempt, ExpectedTaskRevision: uint64(command.ExpectedTaskRevision),
		ExpectedRuntimeRevision:     uint64(command.ExpectedRuntimeRevision),
		ExpectedOperationGeneration: uint64(command.ExpectedOperationGeneration),
		ExpectedRuntimeFence:        uint64(command.ExpectedRuntimeFence),
		Authority:                   canonicalAuthority{Kind: authorityKindName(command.Authority.kind), ID: command.Authority.id.String(), Generation: uint64(command.Authority.generation)},
		RuntimeBindingID:            command.RuntimeBindingID.String(), RuntimeBindingDigest: command.RuntimeBindingDigest.String(),
		ExecutionLockDigest: command.ExecutionLockDigest.String(), CapabilityContractDigest: command.CapabilityContractDigest.String(),
		AllowedPlatformImagesDigest: command.AllowedPlatformImagesDigest.String(), ExecutorContractDigest: command.ExecutorContractDigest.String(),
		ReleaseSafetyEpoch: uint64(command.ReleaseSafetyEpoch), CatalogBinding: catalog,
		WorkerClass: workerClassName(command.WorkerClass), Effect: effectClassName(command.Effect),
		ImmutableInputManifest: canonicalInputManifest{
			Identity: command.ImmutableInputManifest.Identity.String(),
			Schema:   canonicalSchema{Major: command.ImmutableInputManifest.SchemaVersion.Major(), Minor: command.ImmutableInputManifest.SchemaVersion.Minor()},
			Digest:   command.ImmutableInputManifest.Digest.String(), TotalSizeBytes: command.ImmutableInputManifest.TotalSizeBytes,
			InputCount:                    command.ImmutableInputManifest.InputCount,
			MaterializationEvidenceID:     command.ImmutableInputManifest.MaterializationEvidenceID.String(),
			MaterializationEvidenceDigest: command.ImmutableInputManifest.MaterializationEvidenceDigest.String(),
		},
		ImmutableInputs: inputs, OutputContractDigest: command.OutputContractDigest.String(),
		EvidenceContractDigest: command.EvidenceContractDigest.String(), RuntimeViewRequirement: view,
		ResourceClassID: command.ResourceClassID.String(), ExecutionPolicyID: command.ExecutionPolicyID.String(),
		ProviderCapability: providerCapabilityName(command.ProviderCapability), ProviderBinding: provider,
		NetworkPolicyID: command.NetworkPolicyID.String(), SecretPolicyID: command.SecretPolicyID.String(),
		Deadline: command.Deadline.UTC().Format(canonicalTimeFormat), CancellationPolicy: cancellationPolicyName(command.CancellationPolicy),
	})
	if err != nil {
		return nil, newError(ErrorInvalidRequest)
	}
	return encoded, nil
}

type canonicalCancel struct {
	Schema                      canonicalSchema    `json:"schema"`
	Kind                        string             `json:"kind"`
	OperationID                 string             `json:"operation_id"`
	PersonalWorkspaceID         string             `json:"personal_workspace_id"`
	TaskID                      string             `json:"task_id"`
	PhaseRunID                  string             `json:"phase_run_id"`
	RuntimeRunID                string             `json:"runtime_run_id"`
	ExpectedRuntimeRevision     uint64             `json:"expected_runtime_revision"`
	ExpectedStartOperationID    string             `json:"expected_start_operation_id"`
	ExpectedOperationGeneration uint64             `json:"expected_operation_generation"`
	ExpectedRuntimeFence        uint64             `json:"expected_runtime_fence"`
	Authority                   canonicalAuthority `json:"authority"`
	Reason                      string             `json:"reason"`
	SafetyEpoch                 uint64             `json:"safety_epoch"`
	OccurredAt                  string             `json:"occurred_at"`
}

func computeCancelDigest(command CancelRuntimeRun) (Digest, error) {
	encoded, err := canonicalCancelEncoding(command)
	if err != nil {
		return Digest{}, err
	}
	return Digest(sha256.Sum256(append([]byte(canonicalRequestDomain), encoded...))), nil
}

func canonicalCancelEncoding(command CancelRuntimeRun) ([]byte, error) {
	if !validCancel(command) {
		return nil, newError(ErrorInvalidRequest)
	}
	encoded, err := json.Marshal(canonicalCancel{
		Schema: canonicalSchema{Major: command.SchemaVersion.Major(), Minor: command.SchemaVersion.Minor()},
		Kind:   "cancel_runtime_run", OperationID: command.OperationID.String(),
		PersonalWorkspaceID: command.PersonalWorkspaceID.String(), TaskID: command.TaskID.String(),
		PhaseRunID: command.PhaseRunID.String(), RuntimeRunID: command.RuntimeRunID.String(),
		ExpectedRuntimeRevision: uint64(command.ExpectedRuntimeRevision), ExpectedStartOperationID: command.ExpectedStartOperationID.String(),
		ExpectedOperationGeneration: uint64(command.ExpectedOperationGeneration), ExpectedRuntimeFence: uint64(command.ExpectedRuntimeFence),
		Authority: canonicalAuthority{Kind: authorityKindName(command.Authority.kind), ID: command.Authority.id.String(), Generation: uint64(command.Authority.generation)},
		Reason:    cancellationReasonName(command.Reason), SafetyEpoch: uint64(command.SafetyEpoch),
		OccurredAt: command.OccurredAt.UTC().Format(canonicalTimeFormat),
	})
	if err != nil {
		return nil, newError(ErrorInvalidRequest)
	}
	return encoded, nil
}

func validCanonicalStart(command StartRuntimeRun) bool {
	if !validOpaqueID(command.OperationID.String()) || !validOpaqueID(command.PersonalWorkspaceID.String()) ||
		!validOpaqueID(command.TaskID.String()) || !validOpaqueID(command.PhaseRunID.String()) ||
		!validOpaqueID(command.RuntimeRunID.String()) || command.Attempt == 0 || command.ExpectedTaskRevision == 0 ||
		command.ExpectedRuntimeRevision == 0 || command.ExpectedOperationGeneration == 0 || command.ExpectedRuntimeFence == 0 ||
		!validAuthority(command.Authority) || !validOpaqueID(command.RuntimeBindingID.String()) ||
		command.RuntimeBindingDigest == (Digest{}) || command.ExecutionLockDigest == (Digest{}) ||
		command.CapabilityContractDigest == (Digest{}) || command.AllowedPlatformImagesDigest == (Digest{}) ||
		command.ExecutorContractDigest == (Digest{}) || command.ReleaseSafetyEpoch == 0 || !validCatalogBinding(command.CatalogBinding) ||
		workerClassName(command.WorkerClass) == "" || effectClassName(command.Effect) == "" ||
		!validImmutableInputManifest(command.ImmutableInputManifest, command.ImmutableInputs) ||
		command.OutputContractDigest == (Digest{}) || command.EvidenceContractDigest == (Digest{}) ||
		!validOpaqueID(command.ResourceClassID.String()) || !validOpaqueID(command.ExecutionPolicyID.String()) ||
		providerCapabilityName(command.ProviderCapability) == "" || !validProviderBinding(command.ProviderCapability, command.ProviderBinding) ||
		!validOpaqueID(command.NetworkPolicyID.String()) || !validOpaqueID(command.SecretPolicyID.String()) ||
		command.Deadline.IsZero() || cancellationPolicyName(command.CancellationPolicy) == "" {
		return false
	}
	if command.Effect == EffectReadOnly && command.RuntimeViewRequirement != nil ||
		command.Effect == EffectMutating && !validRuntimeViewRequirement(command.RuntimeViewRequirement) {
		return false
	}
	for _, input := range command.ImmutableInputs {
		if !validOpaqueID(input.Identity.String()) || input.Digest == (Digest{}) {
			return false
		}
	}
	return true
}

func validStart(command StartRuntimeRun) bool {
	return validCanonicalStart(command) && validAdmissionGrantProof(command.AdmissionGrant)
}

func validAdmissionGrantProof(grant AdmissionGrantProof) bool {
	return validOpaqueID(grant.AdmissionGrantID.String()) && validOpaqueID(grant.WorkItemID.String()) &&
		grant.Generation > 0
}

// CanonicalStartPayload returns a defensive copy of the exact versioned C03
// request bytes whose digest is carried by Task Orchestration and Scheduler.
func CanonicalStartPayload(command StartRuntimeRun) ([]byte, error) {
	encoded, err := canonicalStartEncoding(command)
	if err != nil {
		return nil, err
	}
	if canonicalRequestDigest(encoded) != command.CanonicalRequestDigest {
		return nil, newError(ErrorIntegrityConflict)
	}
	return append([]byte(nil), encoded...), nil
}

// BindCanonicalStartPayload reconstructs the exact Task-authored request and
// attaches only the authenticated Scheduler proof. Re-encoding must reproduce
// the supplied bytes exactly, so delivery cannot supplement or normalize the
// authoritative payload.
func BindCanonicalStartPayload(
	payload []byte,
	expectedDigest Digest,
	grant AdmissionGrantProof,
) (StartRuntimeRun, error) {
	if !validAdmissionGrantProof(grant) {
		return StartRuntimeRun{}, newError(ErrorInvalidRequest)
	}
	command, err := ParseCanonicalStartPayload(payload, expectedDigest)
	if err != nil {
		return StartRuntimeRun{}, err
	}
	return command.WithAdmissionGrant(grant)
}

// ParseCanonicalStartPayload verifies and reconstructs a grant-independent
// Task-authored C03 Start request without creating an execution decision.
func ParseCanonicalStartPayload(payload []byte, expectedDigest Digest) (StartRuntimeRun, error) {
	if len(payload) == 0 || expectedDigest == (Digest{}) || canonicalRequestDigest(payload) != expectedDigest {
		return StartRuntimeRun{}, newError(ErrorIntegrityConflict)
	}
	var wire canonicalStart
	if err := json.Unmarshal(payload, &wire); err != nil || wire.Kind != "start_runtime_run" {
		return StartRuntimeRun{}, newError(ErrorInvalidRequest)
	}
	input, err := startInputFromCanonical(wire)
	if err != nil {
		return StartRuntimeRun{}, err
	}
	command, err := NewCanonicalStartRuntimeRun(input)
	if err != nil || command.CanonicalRequestDigest != expectedDigest {
		return StartRuntimeRun{}, newError(ErrorIntegrityConflict)
	}
	encoded, err := canonicalStartEncoding(command)
	if err != nil || !bytes.Equal(encoded, payload) {
		return StartRuntimeRun{}, newError(ErrorIntegrityConflict)
	}
	return command, nil
}

// CanonicalCancelPayload returns a defensive copy of the exact versioned C03
// cancellation bytes bound by Task Orchestration and Scheduler.
func CanonicalCancelPayload(command CancelRuntimeRun) ([]byte, error) {
	encoded, err := canonicalCancelEncoding(command)
	if err != nil {
		return nil, err
	}
	if canonicalRequestDigest(encoded) != command.CanonicalRequestDigest {
		return nil, newError(ErrorIntegrityConflict)
	}
	return append([]byte(nil), encoded...), nil
}

// ParseCanonicalCancelPayload verifies and reconstructs the exact Task-authored
// C03 cancellation request without supplementing its authority or identity.
func ParseCanonicalCancelPayload(payload []byte, expectedDigest Digest) (CancelRuntimeRun, error) {
	if len(payload) == 0 || expectedDigest == (Digest{}) || canonicalRequestDigest(payload) != expectedDigest {
		return CancelRuntimeRun{}, newError(ErrorIntegrityConflict)
	}
	var wire canonicalCancel
	if err := json.Unmarshal(payload, &wire); err != nil || wire.Kind != "cancel_runtime_run" ||
		wire.Schema.Major == 0 || wire.Authority.Kind != "task_orchestration" {
		return CancelRuntimeRun{}, newError(ErrorInvalidRequest)
	}
	reason := CancellationReason(0)
	switch wire.Reason {
	case "user_requested":
		reason = CancellationUserRequested
	case "administrator_requested":
		reason = CancellationAdministratorRequested
	}
	occurredAt, err := time.Parse(canonicalTimeFormat, wire.OccurredAt)
	if err != nil || reason == 0 {
		return CancelRuntimeRun{}, newError(ErrorInvalidRequest)
	}
	command, err := NewCancelRuntimeRun(CancelRuntimeRunInput{
		SchemaVersion:       NewSchemaVersion(wire.Schema.Major, wire.Schema.Minor),
		OperationID:         OperationID{value: wire.OperationID},
		PersonalWorkspaceID: PersonalWorkspaceID{value: wire.PersonalWorkspaceID},
		TaskID:              TaskID{value: wire.TaskID}, PhaseRunID: PhaseRunID{value: wire.PhaseRunID},
		RuntimeRunID:                RuntimeRunID{value: wire.RuntimeRunID},
		ExpectedRuntimeRevision:     RuntimeRevision(wire.ExpectedRuntimeRevision),
		ExpectedStartOperationID:    OperationID{value: wire.ExpectedStartOperationID},
		ExpectedOperationGeneration: OperationGeneration(wire.ExpectedOperationGeneration),
		ExpectedRuntimeFence:        RuntimeFence(wire.ExpectedRuntimeFence),
		Authority: NewTaskOrchestrationAuthority(
			AuthorityID{value: wire.Authority.ID}, AuthorizationGeneration(wire.Authority.Generation),
		),
		Reason: reason, SafetyEpoch: ReleaseSafetyEpoch(wire.SafetyEpoch), OccurredAt: occurredAt,
	})
	if err != nil || command.CanonicalRequestDigest != expectedDigest {
		return CancelRuntimeRun{}, newError(ErrorIntegrityConflict)
	}
	encoded, err := canonicalCancelEncoding(command)
	if err != nil || !bytes.Equal(encoded, payload) {
		return CancelRuntimeRun{}, newError(ErrorIntegrityConflict)
	}
	return command, nil
}

func startInputFromCanonical(wire canonicalStart) (StartRuntimeRunInput, error) {
	deadline, err := time.Parse(canonicalTimeFormat, wire.Deadline)
	if err != nil || wire.Schema.Major == 0 || wire.Authority.Kind != "task_orchestration" {
		return StartRuntimeRunInput{}, newError(ErrorInvalidRequest)
	}
	workerClass := WorkerClass(0)
	switch wire.WorkerClass {
	case "agent":
		workerClass = WorkerAgent
	case "tool":
		workerClass = WorkerTool
	}
	effect := EffectClass(0)
	switch wire.Effect {
	case "read_only":
		effect = EffectReadOnly
	case "mutating":
		effect = EffectMutating
	}
	providerCapability := ProviderCapability(0)
	switch wire.ProviderCapability {
	case "none":
		providerCapability = ProviderCapabilityNone
	case "required":
		providerCapability = ProviderCapabilityRequired
	}
	if wire.CancellationPolicy != "fence_first" || workerClass == 0 || effect == 0 || providerCapability == 0 {
		return StartRuntimeRunInput{}, newError(ErrorInvalidRequest)
	}
	runtimeBindingDigest, err := digestFromCanonicalText(wire.RuntimeBindingDigest)
	if err != nil {
		return StartRuntimeRunInput{}, err
	}
	executionLockDigest, err := digestFromCanonicalText(wire.ExecutionLockDigest)
	if err != nil {
		return StartRuntimeRunInput{}, err
	}
	capabilityDigest, err := digestFromCanonicalText(wire.CapabilityContractDigest)
	if err != nil {
		return StartRuntimeRunInput{}, err
	}
	imagesDigest, err := digestFromCanonicalText(wire.AllowedPlatformImagesDigest)
	if err != nil {
		return StartRuntimeRunInput{}, err
	}
	executorDigest, err := digestFromCanonicalText(wire.ExecutorContractDigest)
	if err != nil {
		return StartRuntimeRunInput{}, err
	}
	outputDigest, err := digestFromCanonicalText(wire.OutputContractDigest)
	if err != nil {
		return StartRuntimeRunInput{}, err
	}
	evidenceDigest, err := digestFromCanonicalText(wire.EvidenceContractDigest)
	if err != nil {
		return StartRuntimeRunInput{}, err
	}
	manifestDigest, err := digestFromCanonicalText(wire.ImmutableInputManifest.Digest)
	if err != nil {
		return StartRuntimeRunInput{}, err
	}
	var materializationEvidenceDigest Digest
	if wire.ImmutableInputManifest.MaterializationEvidenceDigest != (Digest{}).String() {
		materializationEvidenceDigest, err = digestFromCanonicalText(wire.ImmutableInputManifest.MaterializationEvidenceDigest)
		if err != nil {
			return StartRuntimeRunInput{}, err
		}
	}
	inputs := make([]ImmutableInputBinding, len(wire.ImmutableInputs))
	for index, item := range wire.ImmutableInputs {
		digest, digestErr := digestFromCanonicalText(item.Digest)
		if digestErr != nil {
			return StartRuntimeRunInput{}, digestErr
		}
		inputs[index] = ImmutableInputBinding{
			Identity: ImmutableInputIdentity{value: item.Identity}, Digest: digest, SizeBytes: item.SizeBytes,
		}
	}
	var runtimeView *RuntimeViewRequirement
	if wire.RuntimeViewRequirement != nil {
		if wire.RuntimeViewRequirement.ExpiryPolicy != "runtime_deadline" {
			return StartRuntimeRunInput{}, newError(ErrorInvalidRequest)
		}
		openDigest, digestErr := digestFromCanonicalText(wire.RuntimeViewRequirement.OpenOperationDerivation)
		if digestErr != nil {
			return StartRuntimeRunInput{}, digestErr
		}
		runtimeView = &RuntimeViewRequirement{
			TaskWorkspaceID:     TaskWorkspaceID{value: wire.RuntimeViewRequirement.TaskWorkspaceID},
			MaterializationID:   TaskWorkspaceMaterializationID{value: wire.RuntimeViewRequirement.MaterializationID},
			BaseRevisionID:      TaskWorkspaceRevisionID{value: wire.RuntimeViewRequirement.BaseRevisionID},
			LifecycleGeneration: TaskWorkspaceLifecycleGeneration(wire.RuntimeViewRequirement.LifecycleGeneration),
			LifecycleFence:      TaskWorkspaceLifecycleFence(wire.RuntimeViewRequirement.LifecycleFence),
			ExpiryPolicy:        RuntimeViewExpiryAtDeadline, OpenOperationDerivation: openDigest,
		}
	}
	var catalog *CatalogExecutionBinding
	if wire.CatalogBinding != nil {
		lockDigest, digestErr := digestFromCanonicalText(wire.CatalogBinding.TemplateLockDigest)
		if digestErr != nil {
			return StartRuntimeRunInput{}, digestErr
		}
		closureDigest, digestErr := digestFromCanonicalText(wire.CatalogBinding.ClosureRootDigest)
		if digestErr != nil {
			return StartRuntimeRunInput{}, digestErr
		}
		catalog = &CatalogExecutionBinding{
			TemplateLockID:     TemplateLockID{value: wire.CatalogBinding.TemplateLockID},
			TemplateLockDigest: lockDigest, ClosureRootDigest: closureDigest,
			SafetyEpoch: CatalogSafetyEpoch(wire.CatalogBinding.SafetyEpoch),
		}
	}
	var provider *ProviderExecutionBinding
	if wire.ProviderBinding != nil {
		mode := QuotaReservationMode(0)
		switch wire.ProviderBinding.Mode {
		case "observation":
			mode = QuotaReservationObservation
		case "enforced":
			mode = QuotaReservationEnforced
		}
		routePolicyExpiresAt, timeErr := time.Parse(canonicalTimeFormat, wire.ProviderBinding.RoutePolicyExpiresAt)
		if timeErr != nil {
			return StartRuntimeRunInput{}, newError(ErrorInvalidRequest)
		}
		provider = &ProviderExecutionBinding{
			QuotaReservationID: QuotaReservationID{value: wire.ProviderBinding.QuotaReservationID},
			Generation:         QuotaReservationGeneration(wire.ProviderBinding.Generation), Mode: mode,
			GatewayRoutePolicyID:         GatewayRoutePolicyID{value: wire.ProviderBinding.GatewayRoutePolicyID},
			GatewayRoutePolicyGeneration: GatewayRoutePolicyGeneration(wire.ProviderBinding.GatewayRoutePolicyGeneration),
			CapabilityScope:              ProviderCapabilityScope(wire.ProviderBinding.CapabilityScope), RoutePolicyExpiresAt: routePolicyExpiresAt,
		}
	}
	return StartRuntimeRunInput{
		SchemaVersion: NewSchemaVersion(wire.Schema.Major, wire.Schema.Minor),
		OperationID:   OperationID{value: wire.OperationID}, PersonalWorkspaceID: PersonalWorkspaceID{value: wire.PersonalWorkspaceID},
		TaskID: TaskID{value: wire.TaskID}, PhaseRunID: PhaseRunID{value: wire.PhaseRunID},
		RuntimeRunID: RuntimeRunID{value: wire.RuntimeRunID}, Attempt: wire.Attempt,
		ExpectedTaskRevision: TaskRevision(wire.ExpectedTaskRevision), ExpectedRuntimeRevision: RuntimeRevision(wire.ExpectedRuntimeRevision),
		ExpectedOperationGeneration: OperationGeneration(wire.ExpectedOperationGeneration), ExpectedRuntimeFence: RuntimeFence(wire.ExpectedRuntimeFence),
		Authority:        NewTaskOrchestrationAuthority(AuthorityID{value: wire.Authority.ID}, AuthorizationGeneration(wire.Authority.Generation)),
		RuntimeBindingID: RuntimeBindingID{value: wire.RuntimeBindingID}, RuntimeBindingDigest: runtimeBindingDigest,
		ExecutionLockDigest: executionLockDigest, CapabilityContractDigest: capabilityDigest,
		AllowedPlatformImagesDigest: imagesDigest, ExecutorContractDigest: executorDigest,
		ReleaseSafetyEpoch: ReleaseSafetyEpoch(wire.ReleaseSafetyEpoch), CatalogBinding: catalog,
		WorkerClass: workerClass, Effect: effect,
		ImmutableInputManifest: ImmutableInputManifestBinding{
			Identity:      ImmutableInputManifestIdentity{value: wire.ImmutableInputManifest.Identity},
			SchemaVersion: NewSchemaVersion(wire.ImmutableInputManifest.Schema.Major, wire.ImmutableInputManifest.Schema.Minor),
			Digest:        manifestDigest, TotalSizeBytes: wire.ImmutableInputManifest.TotalSizeBytes,
			InputCount:                    wire.ImmutableInputManifest.InputCount,
			MaterializationEvidenceID:     EvidenceID{value: wire.ImmutableInputManifest.MaterializationEvidenceID},
			MaterializationEvidenceDigest: materializationEvidenceDigest,
		},
		ImmutableInputs:      inputs,
		OutputContractDigest: outputDigest, EvidenceContractDigest: evidenceDigest, RuntimeViewRequirement: runtimeView,
		ResourceClassID: ResourceClassID{value: wire.ResourceClassID}, ExecutionPolicyID: ExecutionPolicyID{value: wire.ExecutionPolicyID},
		ProviderCapability: providerCapability, ProviderBinding: provider,
		NetworkPolicyID: NetworkPolicyID{value: wire.NetworkPolicyID}, SecretPolicyID: SecretPolicyID{value: wire.SecretPolicyID},
		Deadline: deadline, CancellationPolicy: CancellationFenceFirst,
	}, nil
}

func digestFromCanonicalText(value string) (Digest, error) {
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != len(Digest{}) {
		return Digest{}, newError(ErrorInvalidRequest)
	}
	var digest Digest
	copy(digest[:], decoded)
	return digest, nil
}

func validCancel(command CancelRuntimeRun) bool {
	return validOpaqueID(command.OperationID.String()) && validOpaqueID(command.PersonalWorkspaceID.String()) &&
		validOpaqueID(command.TaskID.String()) && validOpaqueID(command.PhaseRunID.String()) && validOpaqueID(command.RuntimeRunID.String()) &&
		command.ExpectedRuntimeRevision > 0 && validOpaqueID(command.ExpectedStartOperationID.String()) &&
		command.ExpectedOperationGeneration > 0 && command.ExpectedRuntimeFence > 0 && validAuthority(command.Authority) &&
		cancellationReasonName(command.Reason) != "" && command.SafetyEpoch > 0 && !command.OccurredAt.IsZero()
}

func validRuntimeViewRequirement(requirement *RuntimeViewRequirement) bool {
	return requirement != nil && validOpaqueID(requirement.TaskWorkspaceID.String()) && validOpaqueID(requirement.MaterializationID.String()) &&
		validOpaqueID(requirement.BaseRevisionID.String()) &&
		requirement.LifecycleGeneration > 0 && requirement.LifecycleFence > 0 && runtimeViewExpiryPolicyName(requirement.ExpiryPolicy) != "" &&
		requirement.OpenOperationDerivation != (Digest{})
}

func validImmutableInputManifest(
	manifest ImmutableInputManifestBinding,
	inputs []ImmutableInputBinding,
) bool {
	if !validOpaqueID(manifest.Identity.String()) || manifest.SchemaVersion.Major() != SchemaV1.Major() ||
		manifest.Digest == (Digest{}) || manifest.InputCount != uint64(len(inputs)) {
		return false
	}
	var size uint64
	for _, input := range inputs {
		if ^uint64(0)-size < input.SizeBytes {
			return false
		}
		size += input.SizeBytes
	}
	if size != manifest.TotalSizeBytes {
		return false
	}
	if len(inputs) == 0 {
		return manifest.MaterializationEvidenceID == (EvidenceID{}) &&
			manifest.MaterializationEvidenceDigest == (Digest{})
	}
	return validOpaqueID(manifest.MaterializationEvidenceID.String()) &&
		manifest.MaterializationEvidenceDigest != (Digest{})
}

func validCatalogBinding(binding *CatalogExecutionBinding) bool {
	return binding == nil || validOpaqueID(binding.TemplateLockID.String()) && binding.TemplateLockDigest != (Digest{}) &&
		binding.ClosureRootDigest != (Digest{}) && binding.SafetyEpoch > 0
}

func validProviderBinding(capability ProviderCapability, binding *ProviderExecutionBinding) bool {
	if capability == ProviderCapabilityNone {
		return binding == nil
	}
	return capability == ProviderCapabilityRequired && binding != nil && validOpaqueID(binding.QuotaReservationID.String()) &&
		binding.Generation > 0 && quotaReservationModeName(binding.Mode) != "" && validOpaqueID(binding.GatewayRoutePolicyID.String()) &&
		binding.GatewayRoutePolicyGeneration > 0 && binding.CapabilityScope != 0 &&
		binding.CapabilityScope&^knownProviderCapabilityScope == 0 &&
		!binding.RoutePolicyExpiresAt.IsZero()
}

func validAuthority(authority RuntimeAuthority) bool {
	return validOpaqueID(authority.id.String()) && authority.generation > 0 && authorityKindName(authority.kind) != ""
}

func validOpaqueID(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || character == '-' || character == '_' || character == '.' || character == ':' || character == '/' {
			continue
		}
		return false
	}
	return true
}

func authorityKindName(kind AuthorityKind) string {
	switch kind {
	case AuthorityTaskOrchestration:
		return "task_orchestration"
	case AuthorityAdministrator:
		return "administrator"
	default:
		return ""
	}
}

func workerClassName(class WorkerClass) string {
	switch class {
	case WorkerAgent:
		return "agent"
	case WorkerTool:
		return "tool"
	default:
		return ""
	}
}

func effectClassName(effect EffectClass) string {
	switch effect {
	case EffectReadOnly:
		return "read_only"
	case EffectMutating:
		return "mutating"
	default:
		return ""
	}
}

func cancellationPolicyName(policy CancellationPolicy) string {
	if policy == CancellationFenceFirst {
		return "fence_first"
	}
	return ""
}

func cancellationReasonName(reason CancellationReason) string {
	switch reason {
	case CancellationUserRequested:
		return "user_requested"
	case CancellationAdministratorRequested:
		return "administrator_requested"
	default:
		return ""
	}
}

func runtimeViewExpiryPolicyName(policy RuntimeViewExpiryPolicy) string {
	if policy == RuntimeViewExpiryAtDeadline {
		return "runtime_deadline"
	}
	return ""
}

func providerCapabilityName(capability ProviderCapability) string {
	switch capability {
	case ProviderCapabilityNone:
		return "none"
	case ProviderCapabilityRequired:
		return "required"
	default:
		return ""
	}
}

func quotaReservationModeName(mode QuotaReservationMode) string {
	switch mode {
	case QuotaReservationObservation:
		return "observation"
	case QuotaReservationEnforced:
		return "enforced"
	default:
		return ""
	}
}

func earlier(left, right time.Time) time.Time {
	if left.Before(right) {
		return left.UTC()
	}
	return right.UTC()
}
