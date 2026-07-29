package runtimeexecution

import (
	"crypto/sha256"
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
	QuotaReservationID   string `json:"quota_reservation_id"`
	Generation           uint64 `json:"generation"`
	Mode                 string `json:"mode"`
	GatewayRoutePolicyID string `json:"gateway_route_policy_id"`
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
	if !validStart(command) {
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

func validStart(command StartRuntimeRun) bool {
	if !validOpaqueID(command.OperationID.String()) || !validOpaqueID(command.PersonalWorkspaceID.String()) ||
		!validOpaqueID(command.TaskID.String()) || !validOpaqueID(command.PhaseRunID.String()) ||
		!validOpaqueID(command.RuntimeRunID.String()) || command.Attempt == 0 || command.ExpectedTaskRevision == 0 ||
		command.ExpectedRuntimeRevision == 0 || command.ExpectedOperationGeneration == 0 || command.ExpectedRuntimeFence == 0 ||
		!validAuthority(command.Authority) || !validOpaqueID(command.RuntimeBindingID.String()) ||
		command.RuntimeBindingDigest == (Digest{}) || command.ExecutionLockDigest == (Digest{}) ||
		command.CapabilityContractDigest == (Digest{}) || command.AllowedPlatformImagesDigest == (Digest{}) ||
		command.ExecutorContractDigest == (Digest{}) || command.ReleaseSafetyEpoch == 0 || !validCatalogBinding(command.CatalogBinding) ||
		workerClassName(command.WorkerClass) == "" || effectClassName(command.Effect) == "" ||
		command.OutputContractDigest == (Digest{}) || command.EvidenceContractDigest == (Digest{}) ||
		!validOpaqueID(command.ResourceClassID.String()) || !validOpaqueID(command.ExecutionPolicyID.String()) ||
		providerCapabilityName(command.ProviderCapability) == "" || !validProviderBinding(command.ProviderCapability, command.ProviderBinding) ||
		!validOpaqueID(command.NetworkPolicyID.String()) || !validOpaqueID(command.SecretPolicyID.String()) ||
		command.Deadline.IsZero() || cancellationPolicyName(command.CancellationPolicy) == "" ||
		!validOpaqueID(command.AdmissionGrant.AdmissionGrantID.String()) || !validOpaqueID(command.AdmissionGrant.WorkItemID.String()) ||
		command.AdmissionGrant.Generation == 0 {
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

func validCatalogBinding(binding *CatalogExecutionBinding) bool {
	return binding == nil || validOpaqueID(binding.TemplateLockID.String()) && binding.TemplateLockDigest != (Digest{}) &&
		binding.ClosureRootDigest != (Digest{}) && binding.SafetyEpoch > 0
}

func validProviderBinding(capability ProviderCapability, binding *ProviderExecutionBinding) bool {
	if capability == ProviderCapabilityNone {
		return binding == nil
	}
	return capability == ProviderCapabilityRequired && binding != nil && validOpaqueID(binding.QuotaReservationID.String()) &&
		binding.Generation > 0 && quotaReservationModeName(binding.Mode) != "" && validOpaqueID(binding.GatewayRoutePolicyID.String())
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
