package runtimeexecution

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

const executionCapsuleWireSchemaV1 = "slidesmith.runtime-execution.execution-capsule/v1"

type ExecutionCapsuleID struct{ value string }

func (id ExecutionCapsuleID) String() string { return id.value }

type CapsuleState uint8

const (
	CapsulePending CapsuleState = iota
	CapsulePrepared
)

type RuntimeCapsuleSnapshot struct {
	State     CapsuleState
	CapsuleID ExecutionCapsuleID
	Digest    Digest
}

type DispatchDisposition uint8

const (
	DispatchPending DispatchDisposition = iota + 1
	DispatchClaimed
	DispatchAcknowledged
)

type DispatchClaimRequest struct {
	RuntimeRunID RuntimeRunID
	CapsuleID    ExecutionCapsuleID
	Digest       Digest
}

type DispatchDelivery struct {
	OperationID   OperationID
	RuntimeRunID  RuntimeRunID
	CapsuleID     ExecutionCapsuleID
	CapsuleDigest Digest
	Capsule       []byte
	Disposition   DispatchDisposition
	DeliveryCount uint64
}

type DispatchAcknowledgementRequest struct {
	OperationID   OperationID
	RuntimeRunID  RuntimeRunID
	CapsuleID     ExecutionCapsuleID
	CapsuleDigest Digest
	AckDigest     Digest
}

type DispatchAcknowledgement struct {
	OperationID   OperationID
	RuntimeRunID  RuntimeRunID
	CapsuleID     ExecutionCapsuleID
	CapsuleDigest Digest
	AckDigest     Digest
	Disposition   DispatchDisposition
}

// OwnedDispatch is a private C03 transport seam. It is deliberately separate
// from RuntimeExecution and cannot mutate Runtime authority or capsule bytes.
type OwnedDispatch interface {
	ClaimDispatch(context.Context, DispatchClaimRequest) (DispatchDelivery, error)
	AcknowledgeDispatch(context.Context, DispatchAcknowledgementRequest) (DispatchAcknowledgement, error)
}

type executionCapsule struct {
	SchemaVersion                   SchemaVersion
	CapsuleID                       ExecutionCapsuleID
	PersonalWorkspaceID             PersonalWorkspaceID
	TaskID                          TaskID
	PhaseRunID                      PhaseRunID
	RuntimeRunID                    RuntimeRunID
	OperationID                     OperationID
	CanonicalRequestDigest          Digest
	RuntimeBindingID                RuntimeBindingID
	RuntimeBindingDigest            Digest
	ExecutionLockDigest             Digest
	CapabilityContractDigest        Digest
	AllowedPlatformImagesDigest     Digest
	ExecutorContractDigest          Digest
	WorkerClass                     WorkerClass
	Effect                          EffectClass
	OutputContractDigest            Digest
	EvidenceContractDigest          Digest
	ExecutionNodeID                 ExecutionNodeID
	NodeGeneration                  NodeGeneration
	NodeAuthorityID                 NodeAuthorityID
	WorkerAuthorityID               WorkerAuthorityID
	WorkerGeneration                WorkerGeneration
	SandboxLeaseID                  SandboxLeaseID
	LeaseGeneration                 LeaseGeneration
	LeaseFence                      LeaseFence
	RuntimeFence                    RuntimeFence
	AuthorizationGeneration         AuthorizationGeneration
	AuthorizationExpiresAt          time.Time
	ReleaseSafetyEpoch              ReleaseSafetyEpoch
	CatalogSafetyEpoch              CatalogSafetyEpoch
	Deadline                        time.Time
	NetworkPolicyID                 NetworkPolicyID
	SecretPolicyID                  SecretPolicyID
	RuntimeViewID                   RuntimeViewID
	RuntimeViewOpenOperationID      OperationID
	RuntimeViewOpenRequestDigest    Digest
	RuntimeViewLeaseAuthorityDigest Digest
	RuntimeViewExpiresAt            time.Time
	RuntimeViewLifecycleGeneration  TaskWorkspaceLifecycleGeneration
	RuntimeViewLifecycleFence       TaskWorkspaceLifecycleFence
	GatewayGrantID                  GatewayGrantID
	GatewayGrantGeneration          GatewayGrantGeneration
	GatewayGrantDigest              Digest
	GatewayGrantExpiresAt           time.Time
	QuotaReservationID              QuotaReservationID
	QuotaReservationGeneration      QuotaReservationGeneration
	ResolutionEvidenceID            EvidenceID
	ResolutionEvidenceDigest        Digest
	Inputs                          ResolvedImmutableInputManifest
	Outputs                         OutputContract
	Security                        ExecutionSecurityContract
	SecurityAcceptance              ExecutionSecurityAcceptance
	Trace                           TraceMetadata
}

type executionCapsuleInputWireV1 struct {
	Identity                      string `json:"identity"`
	Digest                        string `json:"digest"`
	SizeBytes                     uint64 `json:"size_bytes"`
	Class                         uint8  `json:"class"`
	ReadCapabilityID              string `json:"read_capability_id"`
	MaterializationEvidenceID     string `json:"materialization_evidence_id"`
	MaterializationEvidenceDigest string `json:"materialization_evidence_digest"`
	MaterializationGeneration     uint64 `json:"materialization_generation"`
	MaterializationFence          uint64 `json:"materialization_fence"`
	ExpiresAt                     string `json:"expires_at"`
	LogicalLocation               string `json:"logical_location"`
}

type executionCapsuleOutputChannelWireV1 struct {
	ChannelID    string `json:"channel_id"`
	LogicalPath  string `json:"logical_path"`
	Class        uint8  `json:"class"`
	Required     bool   `json:"required"`
	MaxSizeBytes uint64 `json:"max_size_bytes"`
}

type executionCapsuleNetworkRuleWireV1 struct {
	DestinationKind uint8  `json:"destination_kind"`
	DestinationID   string `json:"destination_id"`
	Protocol        uint8  `json:"protocol"`
	Port            uint16 `json:"port"`
	Purpose         uint8  `json:"purpose"`
}

type executionCapsuleSecretCapabilityWireV1 struct {
	CapabilityID string `json:"capability_id"`
	Class        uint8  `json:"class"`
	Use          uint8  `json:"use"`
	Generation   uint64 `json:"generation"`
	ExpiresAt    string `json:"expires_at"`
	ScopeDigest  string `json:"scope_digest"`
}

type executionCapsuleWireV1 struct {
	SchemaVersion                      string                                   `json:"schema_version"`
	CapsuleID                          string                                   `json:"capsule_id"`
	PersonalWorkspaceID                string                                   `json:"personal_workspace_id"`
	TaskID                             string                                   `json:"task_id"`
	PhaseRunID                         string                                   `json:"phase_run_id"`
	RuntimeRunID                       string                                   `json:"runtime_run_id"`
	OperationID                        string                                   `json:"operation_id"`
	CanonicalRequestDigest             string                                   `json:"canonical_request_digest"`
	RuntimeBindingID                   string                                   `json:"runtime_binding_id"`
	RuntimeBindingDigest               string                                   `json:"runtime_binding_digest"`
	ExecutionLockDigest                string                                   `json:"execution_lock_digest"`
	CapabilityContractDigest           string                                   `json:"capability_contract_digest"`
	AllowedPlatformImagesDigest        string                                   `json:"allowed_platform_images_digest"`
	ExecutorContractDigest             string                                   `json:"executor_contract_digest"`
	WorkerClass                        uint8                                    `json:"worker_class"`
	Effect                             uint8                                    `json:"effect"`
	OutputContractDigest               string                                   `json:"output_contract_digest"`
	EvidenceContractDigest             string                                   `json:"evidence_contract_digest"`
	ExecutionNodeID                    string                                   `json:"execution_node_id"`
	NodeGeneration                     uint64                                   `json:"node_generation"`
	NodeAuthorityID                    string                                   `json:"node_authority_id"`
	WorkerAuthorityID                  string                                   `json:"worker_authority_id"`
	WorkerGeneration                   uint64                                   `json:"worker_generation"`
	SandboxLeaseID                     string                                   `json:"sandbox_lease_id"`
	LeaseGeneration                    uint64                                   `json:"lease_generation"`
	LeaseFence                         uint64                                   `json:"lease_fence"`
	RuntimeFence                       uint64                                   `json:"runtime_fence"`
	AuthorizationGeneration            uint64                                   `json:"authorization_generation"`
	AuthorizationExpiresAt             string                                   `json:"authorization_expires_at"`
	ReleaseSafetyEpoch                 uint64                                   `json:"release_safety_epoch"`
	CatalogSafetyEpoch                 uint64                                   `json:"catalog_safety_epoch"`
	Deadline                           string                                   `json:"deadline"`
	NetworkPolicyID                    string                                   `json:"network_policy_id"`
	SecretPolicyID                     string                                   `json:"secret_policy_id"`
	ExecutionPolicyID                  string                                   `json:"execution_policy_id"`
	RuntimeViewID                      string                                   `json:"runtime_view_id,omitempty"`
	RuntimeViewOpenOperationID         string                                   `json:"runtime_view_open_operation_id,omitempty"`
	RuntimeViewOpenRequestDigest       string                                   `json:"runtime_view_open_request_digest,omitempty"`
	RuntimeViewLeaseAuthorityDigest    string                                   `json:"runtime_view_lease_authority_digest,omitempty"`
	RuntimeViewExpiresAt               string                                   `json:"runtime_view_expires_at,omitempty"`
	RuntimeViewLifecycleGeneration     uint64                                   `json:"runtime_view_lifecycle_generation,omitempty"`
	RuntimeViewLifecycleFence          uint64                                   `json:"runtime_view_lifecycle_fence,omitempty"`
	GatewayGrantID                     string                                   `json:"gateway_grant_id,omitempty"`
	GatewayGrantGeneration             uint64                                   `json:"gateway_grant_generation,omitempty"`
	GatewayGrantDigest                 string                                   `json:"gateway_grant_digest,omitempty"`
	GatewayGrantExpiresAt              string                                   `json:"gateway_grant_expires_at,omitempty"`
	QuotaReservationID                 string                                   `json:"quota_reservation_id,omitempty"`
	QuotaReservationGeneration         uint64                                   `json:"quota_reservation_generation,omitempty"`
	ResolutionEvidenceID               string                                   `json:"resolution_evidence_id"`
	ResolutionEvidenceDigest           string                                   `json:"resolution_evidence_digest"`
	InputManifestIdentity              string                                   `json:"input_manifest_identity"`
	InputManifestDigest                string                                   `json:"input_manifest_digest"`
	InputMaterializationEvidenceID     string                                   `json:"input_materialization_evidence_id"`
	InputMaterializationEvidenceDigest string                                   `json:"input_materialization_evidence_digest"`
	Inputs                             []executionCapsuleInputWireV1            `json:"inputs"`
	OutputMaxCount                     uint64                                   `json:"output_max_count"`
	OutputMaxTotalSizeBytes            uint64                                   `json:"output_max_total_size_bytes"`
	OutputChannels                     []executionCapsuleOutputChannelWireV1    `json:"output_channels"`
	SecurityEvidenceID                 string                                   `json:"security_evidence_id"`
	SecurityEvidenceDigest             string                                   `json:"security_evidence_digest"`
	ExecutionPolicyDigest              string                                   `json:"execution_policy_digest"`
	SandboxDriverDigest                string                                   `json:"sandbox_driver_digest"`
	HostGeneration                     uint64                                   `json:"host_generation"`
	KernelRuntimeDigest                string                                   `json:"kernel_runtime_digest"`
	ActualImageDigest                  string                                   `json:"actual_image_digest"`
	ActualExecutorDigest               string                                   `json:"actual_executor_digest"`
	ImageAuthorizationDigest           string                                   `json:"image_authorization_digest"`
	ExecutorAuthorizationDigest        string                                   `json:"executor_authorization_digest"`
	MountPolicyDigest                  string                                   `json:"mount_policy_digest"`
	WritableLocationsDigest            string                                   `json:"writable_locations_digest"`
	CredentialPolicyDigest             string                                   `json:"credential_policy_digest"`
	ResetStateDigest                   string                                   `json:"reset_state_digest"`
	CapabilityCompatibilityDigest      string                                   `json:"capability_compatibility_digest"`
	AttestationID                      string                                   `json:"attestation_id"`
	AttestationDigest                  string                                   `json:"attestation_digest"`
	AttestationGeneration              uint64                                   `json:"attestation_generation"`
	AttestationExpiresAt               string                                   `json:"attestation_expires_at"`
	NetworkPolicyDigest                string                                   `json:"network_policy_digest"`
	NetworkGeneration                  uint64                                   `json:"network_generation"`
	NetworkExpiresAt                   string                                   `json:"network_expires_at"`
	DefaultDenyEgress                  bool                                     `json:"default_deny_egress"`
	DefaultDenyIngress                 bool                                     `json:"default_deny_ingress"`
	DNSPinned                          bool                                     `json:"dns_pinned"`
	RedirectsDenied                    bool                                     `json:"redirects_denied"`
	ProxyBypassDenied                  bool                                     `json:"proxy_bypass_denied"`
	AlternatePortsDenied               bool                                     `json:"alternate_ports_denied"`
	ResolvedEndpointsDigest            string                                   `json:"resolved_endpoints_digest"`
	NetworkRules                       []executionCapsuleNetworkRuleWireV1      `json:"network_rules"`
	SecretPolicyDigest                 string                                   `json:"secret_policy_digest"`
	SecretGeneration                   uint64                                   `json:"secret_generation"`
	SecretExpiresAt                    string                                   `json:"secret_expires_at"`
	SecretCapabilities                 []executionCapsuleSecretCapabilityWireV1 `json:"secret_capabilities"`
	TraceID                            string                                   `json:"trace_id,omitempty"`
}

type retainedExecutionCapsule struct {
	snapshot            RuntimeCapsuleSnapshot
	dispatchOperationID OperationID
	wire                []byte
	decoded             executionCapsule
	disposition         DispatchDisposition
	deliveryCount       uint64
	ackDigest           Digest
}

func buildExecutionCapsule(
	start StartRuntimeRun,
	snapshot RuntimeSnapshot,
	now time.Time,
	resolution ExecutionCapsuleResolution,
	security ExecutionSecurityAcceptance,
) (executionCapsule, []byte, Digest, error) {
	if !capsulePrerequisitesReadyAt(snapshot, now.UTC()) || snapshot.Lease.AcquireStatus != LeaseGranted ||
		snapshot.Lease.Disposition != LeaseActive || snapshot.Operation.OperationID != start.OperationID ||
		snapshot.Operation.Digest != start.CanonicalRequestDigest || snapshot.RuntimeRunID != start.RuntimeRunID {
		return executionCapsule{}, nil, Digest{}, newError(ErrorIntegrityConflict)
	}
	identityMaterial := []byte(fmt.Sprintf("slidesmith.runtime-execution.execution-capsule-identity/v1\n%s\n%s\n%s\n%d\n%d",
		start.RuntimeRunID.String(), start.CanonicalRequestDigest.String(), snapshot.Lease.LeaseID.String(),
		snapshot.Lease.Generation, snapshot.Lease.Fence))
	identityDigest := digestBytes(identityMaterial)
	capsuleID := ExecutionCapsuleID{value: fmt.Sprintf("execution-capsule-%x", identityDigest[:12])}
	capsule := executionCapsule{
		SchemaVersion: SchemaV1, CapsuleID: capsuleID,
		PersonalWorkspaceID: start.PersonalWorkspaceID, TaskID: start.TaskID, PhaseRunID: start.PhaseRunID,
		RuntimeRunID: start.RuntimeRunID, OperationID: start.OperationID,
		CanonicalRequestDigest: start.CanonicalRequestDigest, RuntimeBindingID: start.RuntimeBindingID,
		RuntimeBindingDigest: start.RuntimeBindingDigest, ExecutionLockDigest: start.ExecutionLockDigest,
		CapabilityContractDigest:    start.CapabilityContractDigest,
		AllowedPlatformImagesDigest: start.AllowedPlatformImagesDigest,
		ExecutorContractDigest:      start.ExecutorContractDigest,
		WorkerClass:                 start.WorkerClass, Effect: start.Effect,
		OutputContractDigest: start.OutputContractDigest, EvidenceContractDigest: start.EvidenceContractDigest,
		ExecutionNodeID: snapshot.Node.ExecutionNodeID, NodeGeneration: snapshot.Node.Generation,
		NodeAuthorityID:   snapshot.Lease.NodeAuthorityID,
		WorkerAuthorityID: snapshot.Lease.WorkerAuthorityID, WorkerGeneration: snapshot.Lease.WorkerGeneration,
		SandboxLeaseID:  snapshot.Lease.LeaseID,
		LeaseGeneration: snapshot.Lease.Generation, LeaseFence: snapshot.Lease.Fence,
		RuntimeFence: snapshot.RuntimeFence, AuthorizationGeneration: start.Authority.AuthorizationGeneration(),
		AuthorizationExpiresAt: snapshot.Lease.AuthorizationExpiresAt,
		ReleaseSafetyEpoch:     start.ReleaseSafetyEpoch, Deadline: start.Deadline.UTC(),
		NetworkPolicyID: start.NetworkPolicyID, SecretPolicyID: start.SecretPolicyID, Trace: start.Trace,
		ResolutionEvidenceID: resolution.EvidenceID, ResolutionEvidenceDigest: resolution.EvidenceDigest,
		Inputs: resolution.Inputs, Outputs: resolution.Outputs, Security: resolution.Security,
		SecurityAcceptance: security,
	}
	if start.CatalogBinding != nil {
		capsule.CatalogSafetyEpoch = start.CatalogBinding.SafetyEpoch
	}
	if start.Effect == EffectMutating {
		binding := snapshot.RuntimeViewBinding
		capsule.RuntimeViewID = binding.RuntimeViewID
		capsule.RuntimeViewOpenOperationID = binding.OpenOperationID
		capsule.RuntimeViewOpenRequestDigest = binding.OpenRequestDigest
		capsule.RuntimeViewLeaseAuthorityDigest = binding.SandboxLeaseAuthorityDigest
		capsule.RuntimeViewExpiresAt = binding.ExpiresAt.UTC()
		capsule.RuntimeViewLifecycleGeneration = binding.LifecycleGeneration
		capsule.RuntimeViewLifecycleFence = binding.LifecycleFence
	}
	if snapshot.Gateway.Applicability == GatewayPrerequisiteRequired && snapshot.Gateway.Ready {
		grant := snapshot.Gateway.CurrentGrant
		capsule.GatewayGrantID = grant.GatewayGrantID
		capsule.GatewayGrantGeneration = grant.Generation
		capsule.GatewayGrantDigest = grant.CanonicalDigest
		capsule.GatewayGrantExpiresAt = grant.ExpiresAt.UTC()
		capsule.QuotaReservationID = grant.QuotaReservationID
		capsule.QuotaReservationGeneration = grant.QuotaReservationGeneration
	}
	inputWires := make([]executionCapsuleInputWireV1, 0, len(capsule.Inputs.Entries))
	for _, input := range capsule.Inputs.Entries {
		inputWires = append(inputWires, executionCapsuleInputWireV1{
			Identity: input.Identity.String(), Digest: input.Digest.String(), SizeBytes: input.SizeBytes,
			Class: uint8(input.Class), ReadCapabilityID: input.ReadCapabilityID.String(),
			MaterializationEvidenceID:     input.MaterializationEvidenceID.String(),
			MaterializationEvidenceDigest: input.MaterializationEvidenceDigest.String(),
			MaterializationGeneration:     input.MaterializationGeneration, MaterializationFence: input.MaterializationFence,
			ExpiresAt: input.ExpiresAt.UTC().Format(time.RFC3339Nano), LogicalLocation: input.LogicalLocation,
		})
	}
	outputWires := make([]executionCapsuleOutputChannelWireV1, 0, len(capsule.Outputs.Channels))
	for _, channel := range capsule.Outputs.Channels {
		outputWires = append(outputWires, executionCapsuleOutputChannelWireV1{
			ChannelID: channel.ChannelID.String(), LogicalPath: channel.LogicalPath, Class: uint8(channel.Class),
			Required: channel.Required, MaxSizeBytes: channel.MaxSizeBytes,
		})
	}
	networkWires := make([]executionCapsuleNetworkRuleWireV1, 0, len(security.NetworkRules))
	for _, rule := range security.NetworkRules {
		networkWires = append(networkWires, executionCapsuleNetworkRuleWireV1{
			DestinationKind: uint8(rule.DestinationKind), DestinationID: rule.DestinationID.String(),
			Protocol: uint8(rule.Protocol), Port: rule.Port, Purpose: uint8(rule.Purpose),
		})
	}
	secretWires := make([]executionCapsuleSecretCapabilityWireV1, 0, len(security.SecretCapabilities))
	for _, capability := range security.SecretCapabilities {
		secretWires = append(secretWires, executionCapsuleSecretCapabilityWireV1{
			CapabilityID: capability.CapabilityID.String(), Class: uint8(capability.Class), Use: uint8(capability.Use),
			Generation: capability.Generation, ExpiresAt: capability.ExpiresAt.UTC().Format(time.RFC3339Nano),
			ScopeDigest: capability.ScopeDigest.String(),
		})
	}
	policy := capsule.Security.Policy
	network := capsule.Security.Network
	secrets := capsule.Security.Secrets
	wire := executionCapsuleWireV1{
		SchemaVersion: executionCapsuleWireSchemaV1, CapsuleID: capsule.CapsuleID.String(),
		PersonalWorkspaceID: capsule.PersonalWorkspaceID.String(), TaskID: capsule.TaskID.String(),
		PhaseRunID: capsule.PhaseRunID.String(), RuntimeRunID: capsule.RuntimeRunID.String(),
		OperationID: capsule.OperationID.String(), CanonicalRequestDigest: capsule.CanonicalRequestDigest.String(),
		RuntimeBindingID: capsule.RuntimeBindingID.String(), RuntimeBindingDigest: capsule.RuntimeBindingDigest.String(),
		ExecutionLockDigest:         capsule.ExecutionLockDigest.String(),
		CapabilityContractDigest:    capsule.CapabilityContractDigest.String(),
		AllowedPlatformImagesDigest: capsule.AllowedPlatformImagesDigest.String(),
		ExecutorContractDigest:      capsule.ExecutorContractDigest.String(), WorkerClass: uint8(capsule.WorkerClass),
		Effect: uint8(capsule.Effect), OutputContractDigest: capsule.OutputContractDigest.String(),
		EvidenceContractDigest: capsule.EvidenceContractDigest.String(), ExecutionNodeID: capsule.ExecutionNodeID.String(),
		NodeGeneration: uint64(capsule.NodeGeneration), NodeAuthorityID: capsule.NodeAuthorityID.String(),
		WorkerAuthorityID: capsule.WorkerAuthorityID.String(), WorkerGeneration: uint64(capsule.WorkerGeneration),
		SandboxLeaseID: capsule.SandboxLeaseID.String(), LeaseGeneration: uint64(capsule.LeaseGeneration),
		LeaseFence: uint64(capsule.LeaseFence), RuntimeFence: uint64(capsule.RuntimeFence),
		AuthorizationGeneration: uint64(capsule.AuthorizationGeneration),
		AuthorizationExpiresAt:  capsule.AuthorizationExpiresAt.Format(time.RFC3339Nano),
		ReleaseSafetyEpoch:      uint64(capsule.ReleaseSafetyEpoch),
		CatalogSafetyEpoch:      uint64(capsule.CatalogSafetyEpoch), Deadline: capsule.Deadline.Format(time.RFC3339Nano),
		NetworkPolicyID: capsule.NetworkPolicyID.String(), SecretPolicyID: capsule.SecretPolicyID.String(),
		ExecutionPolicyID:               policy.ExecutionPolicyID.String(),
		RuntimeViewID:                   capsule.RuntimeViewID.String(),
		RuntimeViewOpenOperationID:      capsule.RuntimeViewOpenOperationID.String(),
		RuntimeViewOpenRequestDigest:    optionalDigestText(capsule.RuntimeViewOpenRequestDigest),
		RuntimeViewLeaseAuthorityDigest: optionalDigestText(capsule.RuntimeViewLeaseAuthorityDigest),
		RuntimeViewExpiresAt:            optionalTimeText(capsule.RuntimeViewExpiresAt),
		RuntimeViewLifecycleGeneration:  uint64(capsule.RuntimeViewLifecycleGeneration),
		RuntimeViewLifecycleFence:       uint64(capsule.RuntimeViewLifecycleFence),
		GatewayGrantID:                  capsule.GatewayGrantID.String(),
		GatewayGrantGeneration:          uint64(capsule.GatewayGrantGeneration), GatewayGrantDigest: optionalDigestText(capsule.GatewayGrantDigest),
		GatewayGrantExpiresAt:      optionalTimeText(capsule.GatewayGrantExpiresAt),
		QuotaReservationID:         capsule.QuotaReservationID.String(),
		QuotaReservationGeneration: uint64(capsule.QuotaReservationGeneration),
		ResolutionEvidenceID:       capsule.ResolutionEvidenceID.String(), ResolutionEvidenceDigest: capsule.ResolutionEvidenceDigest.String(),
		InputManifestIdentity: capsule.Inputs.Identity.String(), InputManifestDigest: capsule.Inputs.Digest.String(),
		InputMaterializationEvidenceID:     capsule.Inputs.MaterializationEvidenceID.String(),
		InputMaterializationEvidenceDigest: capsule.Inputs.MaterializationEvidenceDigest.String(), Inputs: inputWires,
		OutputMaxCount: capsule.Outputs.MaxOutputCount, OutputMaxTotalSizeBytes: capsule.Outputs.MaxTotalSizeBytes,
		OutputChannels:     outputWires,
		SecurityEvidenceID: capsule.Security.EvidenceID.String(), SecurityEvidenceDigest: capsule.Security.EvidenceDigest.String(),
		ExecutionPolicyDigest: policy.PolicyDigest.String(), SandboxDriverDigest: policy.SandboxDriverDigest.String(),
		HostGeneration:      policy.HostGeneration,
		KernelRuntimeDigest: policy.KernelRuntimeDigest.String(), ActualImageDigest: policy.ActualImageDigest.String(),
		ActualExecutorDigest: policy.ActualExecutorDigest.String(), ImageAuthorizationDigest: policy.ImageAuthorizationDigest.String(),
		ExecutorAuthorizationDigest: policy.ExecutorAuthorizationDigest.String(), MountPolicyDigest: policy.MountPolicyDigest.String(),
		WritableLocationsDigest: policy.WritableLocationsDigest.String(), CredentialPolicyDigest: policy.CredentialPolicyDigest.String(),
		ResetStateDigest: policy.ResetStateDigest.String(), CapabilityCompatibilityDigest: policy.CapabilityCompatibilityDigest.String(),
		AttestationID: policy.AttestationID.String(), AttestationDigest: policy.AttestationDigest.String(),
		AttestationGeneration: uint64(policy.AttestationGeneration), AttestationExpiresAt: policy.AttestationExpiresAt.UTC().Format(time.RFC3339Nano),
		NetworkPolicyDigest: network.PolicyDigest.String(), NetworkGeneration: network.Generation,
		NetworkExpiresAt: network.ExpiresAt.UTC().Format(time.RFC3339Nano), DefaultDenyEgress: network.DefaultDenyEgress,
		DefaultDenyIngress: network.DefaultDenyIngress, DNSPinned: network.DNSPinned, RedirectsDenied: network.RedirectsDenied,
		ProxyBypassDenied: network.ProxyBypassDenied, AlternatePortsDenied: network.AlternatePortsDenied,
		ResolvedEndpointsDigest: network.ResolvedEndpointsDigest.String(), NetworkRules: networkWires,
		SecretPolicyDigest: secrets.PolicyDigest.String(), SecretGeneration: secrets.Generation,
		SecretExpiresAt: secrets.ExpiresAt.UTC().Format(time.RFC3339Nano), SecretCapabilities: secretWires,
	}
	if capsule.Trace.TraceID != (TraceID{}) {
		wire.TraceID = fmt.Sprintf("%x", capsule.Trace.TraceID[:])
	}
	canonical, err := json.Marshal(wire)
	if err != nil {
		return executionCapsule{}, nil, Digest{}, newError(ErrorIntegrityConflict)
	}
	return capsule, canonical, digestBytes(canonical), nil
}

func decodeExecutionCapsule(canonical []byte) (executionCapsule, error) {
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	decoder.DisallowUnknownFields()
	var wire executionCapsuleWireV1
	if err := decoder.Decode(&wire); err != nil {
		return executionCapsule{}, newError(ErrorUnsupportedSchema)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return executionCapsule{}, newError(ErrorUnsupportedSchema)
	}
	if wire.Inputs == nil || wire.OutputChannels == nil || wire.NetworkRules == nil || wire.SecretCapabilities == nil {
		return executionCapsule{}, newError(ErrorUnsupportedSchema)
	}
	reencoded, err := json.Marshal(wire)
	if err != nil || !bytes.Equal(reencoded, canonical) {
		return executionCapsule{}, newError(ErrorUnsupportedSchema)
	}
	if wire.SchemaVersion != executionCapsuleWireSchemaV1 {
		return executionCapsule{}, newError(ErrorUnsupportedSchema)
	}
	deadline, err := time.Parse(time.RFC3339Nano, wire.Deadline)
	if err != nil {
		return executionCapsule{}, newError(ErrorIntegrityConflict)
	}
	workspaceID, err := NewPersonalWorkspaceID(wire.PersonalWorkspaceID)
	if err != nil {
		return executionCapsule{}, err
	}
	taskID, err := NewTaskID(wire.TaskID)
	if err != nil {
		return executionCapsule{}, err
	}
	phaseRunID, err := NewPhaseRunID(wire.PhaseRunID)
	if err != nil {
		return executionCapsule{}, err
	}
	runtimeRunID, err := NewRuntimeRunID(wire.RuntimeRunID)
	if err != nil {
		return executionCapsule{}, err
	}
	operationID, err := NewOperationID(wire.OperationID)
	if err != nil {
		return executionCapsule{}, err
	}
	runtimeBindingID, err := NewRuntimeBindingID(wire.RuntimeBindingID)
	if err != nil {
		return executionCapsule{}, err
	}
	nodeID, err := NewExecutionNodeID(wire.ExecutionNodeID)
	if err != nil {
		return executionCapsule{}, err
	}
	networkPolicyID, err := NewNetworkPolicyID(wire.NetworkPolicyID)
	if err != nil {
		return executionCapsule{}, err
	}
	secretPolicyID, err := NewSecretPolicyID(wire.SecretPolicyID)
	if err != nil {
		return executionCapsule{}, err
	}
	requestDigest, err := digestFromCanonicalText(wire.CanonicalRequestDigest)
	if err != nil {
		return executionCapsule{}, err
	}
	bindingDigest, err := digestFromCanonicalText(wire.RuntimeBindingDigest)
	if err != nil {
		return executionCapsule{}, err
	}
	lockDigest, err := digestFromCanonicalText(wire.ExecutionLockDigest)
	if err != nil {
		return executionCapsule{}, err
	}
	capabilityContractDigest, err := digestFromCanonicalText(wire.CapabilityContractDigest)
	if err != nil {
		return executionCapsule{}, err
	}
	allowedPlatformImagesDigest, err := digestFromCanonicalText(wire.AllowedPlatformImagesDigest)
	if err != nil {
		return executionCapsule{}, err
	}
	executorContractDigest, err := digestFromCanonicalText(wire.ExecutorContractDigest)
	if err != nil {
		return executionCapsule{}, err
	}
	outputDigest, err := digestFromCanonicalText(wire.OutputContractDigest)
	if err != nil {
		return executionCapsule{}, err
	}
	evidenceDigest, err := digestFromCanonicalText(wire.EvidenceContractDigest)
	if err != nil {
		return executionCapsule{}, err
	}
	resolutionDigest, err := digestFromCanonicalText(wire.ResolutionEvidenceDigest)
	if err != nil {
		return executionCapsule{}, err
	}
	inputManifestDigest, err := digestFromCanonicalText(wire.InputManifestDigest)
	if err != nil {
		return executionCapsule{}, err
	}
	inputMaterializationDigest, err := digestFromCanonicalText(wire.InputMaterializationEvidenceDigest)
	if err != nil {
		return executionCapsule{}, err
	}
	securityEvidenceDigest, err := digestFromCanonicalText(wire.SecurityEvidenceDigest)
	if err != nil {
		return executionCapsule{}, err
	}
	policyDigest, err := digestFromCanonicalText(wire.ExecutionPolicyDigest)
	if err != nil {
		return executionCapsule{}, err
	}
	sandboxDriverDigest, err := digestFromCanonicalText(wire.SandboxDriverDigest)
	if err != nil {
		return executionCapsule{}, err
	}
	kernelRuntimeDigest, err := digestFromCanonicalText(wire.KernelRuntimeDigest)
	if err != nil {
		return executionCapsule{}, err
	}
	actualImageDigest, err := digestFromCanonicalText(wire.ActualImageDigest)
	if err != nil {
		return executionCapsule{}, err
	}
	actualExecutorDigest, err := digestFromCanonicalText(wire.ActualExecutorDigest)
	if err != nil {
		return executionCapsule{}, err
	}
	imageAuthorizationDigest, err := digestFromCanonicalText(wire.ImageAuthorizationDigest)
	if err != nil {
		return executionCapsule{}, err
	}
	executorAuthorizationDigest, err := digestFromCanonicalText(wire.ExecutorAuthorizationDigest)
	if err != nil {
		return executionCapsule{}, err
	}
	mountPolicyDigest, err := digestFromCanonicalText(wire.MountPolicyDigest)
	if err != nil {
		return executionCapsule{}, err
	}
	writableLocationsDigest, err := digestFromCanonicalText(wire.WritableLocationsDigest)
	if err != nil {
		return executionCapsule{}, err
	}
	credentialPolicyDigest, err := digestFromCanonicalText(wire.CredentialPolicyDigest)
	if err != nil {
		return executionCapsule{}, err
	}
	resetStateDigest, err := digestFromCanonicalText(wire.ResetStateDigest)
	if err != nil {
		return executionCapsule{}, err
	}
	compatibilityDigest, err := digestFromCanonicalText(wire.CapabilityCompatibilityDigest)
	if err != nil {
		return executionCapsule{}, err
	}
	attestationDigest, err := digestFromCanonicalText(wire.AttestationDigest)
	if err != nil {
		return executionCapsule{}, err
	}
	networkPolicyDigest, err := digestFromCanonicalText(wire.NetworkPolicyDigest)
	if err != nil {
		return executionCapsule{}, err
	}
	resolvedEndpointsDigest, err := digestFromCanonicalText(wire.ResolvedEndpointsDigest)
	if err != nil {
		return executionCapsule{}, err
	}
	secretPolicyDigest, err := digestFromCanonicalText(wire.SecretPolicyDigest)
	if err != nil {
		return executionCapsule{}, err
	}
	attestationExpiresAt, err := time.Parse(time.RFC3339Nano, wire.AttestationExpiresAt)
	if err != nil {
		return executionCapsule{}, newError(ErrorIntegrityConflict)
	}
	networkExpiresAt, err := time.Parse(time.RFC3339Nano, wire.NetworkExpiresAt)
	if err != nil {
		return executionCapsule{}, newError(ErrorIntegrityConflict)
	}
	secretExpiresAt, err := time.Parse(time.RFC3339Nano, wire.SecretExpiresAt)
	if err != nil {
		return executionCapsule{}, newError(ErrorIntegrityConflict)
	}
	authorizationExpiresAt, err := time.Parse(time.RFC3339Nano, wire.AuthorizationExpiresAt)
	if err != nil {
		return executionCapsule{}, newError(ErrorIntegrityConflict)
	}
	trace := TraceMetadata{}
	if wire.TraceID != "" {
		if len(wire.TraceID) != hex.EncodedLen(len(trace.TraceID)) {
			return executionCapsule{}, newError(ErrorIntegrityConflict)
		}
		decodedTrace, traceErr := hex.DecodeString(wire.TraceID)
		if traceErr != nil || hex.EncodeToString(decodedTrace) != wire.TraceID {
			return executionCapsule{}, newError(ErrorIntegrityConflict)
		}
		copy(trace.TraceID[:], decodedTrace)
	}
	inputs := make([]ResolvedImmutableInput, 0, len(wire.Inputs))
	for _, inputWire := range wire.Inputs {
		identity, inputErr := NewImmutableInputIdentity(inputWire.Identity)
		if inputErr != nil {
			return executionCapsule{}, inputErr
		}
		inputDigest, inputErr := digestFromCanonicalText(inputWire.Digest)
		if inputErr != nil {
			return executionCapsule{}, inputErr
		}
		readCapabilityID, inputErr := NewInputReadCapabilityID(inputWire.ReadCapabilityID)
		if inputErr != nil {
			return executionCapsule{}, inputErr
		}
		materializationEvidenceID, inputErr := NewEvidenceID(inputWire.MaterializationEvidenceID)
		if inputErr != nil {
			return executionCapsule{}, inputErr
		}
		materializationDigest, inputErr := digestFromCanonicalText(inputWire.MaterializationEvidenceDigest)
		if inputErr != nil {
			return executionCapsule{}, inputErr
		}
		expiresAt, inputErr := time.Parse(time.RFC3339Nano, inputWire.ExpiresAt)
		if inputErr != nil {
			return executionCapsule{}, newError(ErrorIntegrityConflict)
		}
		if !safeSandboxLogicalPath(inputWire.LogicalLocation) || inputWire.MaterializationGeneration == 0 ||
			inputWire.MaterializationFence == 0 || inputWire.Class < uint8(InputClassSourceMaterial) ||
			inputWire.Class > uint8(InputClassCatalogPackage) {
			return executionCapsule{}, newError(ErrorIntegrityConflict)
		}
		inputs = append(inputs, ResolvedImmutableInput{
			Identity: identity, Digest: inputDigest, SizeBytes: inputWire.SizeBytes,
			Class: InputContentClass(inputWire.Class), ReadCapabilityID: readCapabilityID,
			MaterializationEvidenceID:     materializationEvidenceID,
			MaterializationEvidenceDigest: materializationDigest,
			MaterializationGeneration:     inputWire.MaterializationGeneration,
			MaterializationFence:          inputWire.MaterializationFence, ExpiresAt: expiresAt.UTC(),
			LogicalLocation: inputWire.LogicalLocation,
		})
	}
	channels := make([]DeclaredOutputChannel, 0, len(wire.OutputChannels))
	for _, channelWire := range wire.OutputChannels {
		channelID, channelErr := NewOutputChannelID(channelWire.ChannelID)
		if channelErr != nil || !safeSandboxLogicalPath(channelWire.LogicalPath) {
			return executionCapsule{}, newError(ErrorIntegrityConflict)
		}
		channels = append(channels, DeclaredOutputChannel{
			ChannelID: channelID, LogicalPath: channelWire.LogicalPath,
			Class: OutputContentClass(channelWire.Class), Required: channelWire.Required,
			MaxSizeBytes: channelWire.MaxSizeBytes,
		})
	}
	networkRules := make([]NetworkRule, 0, len(wire.NetworkRules))
	for _, ruleWire := range wire.NetworkRules {
		destinationID, ruleErr := NewNetworkDestinationID(ruleWire.DestinationID)
		if ruleErr != nil {
			return executionCapsule{}, ruleErr
		}
		networkRules = append(networkRules, NetworkRule{
			DestinationKind: NetworkDestinationKind(ruleWire.DestinationKind), DestinationID: destinationID,
			Protocol: NetworkProtocol(ruleWire.Protocol), Port: ruleWire.Port, Purpose: NetworkPurpose(ruleWire.Purpose),
		})
	}
	secretCapabilities := make([]SecretCapability, 0, len(wire.SecretCapabilities))
	for _, capabilityWire := range wire.SecretCapabilities {
		capabilityID, capabilityErr := NewSecretCapabilityID(capabilityWire.CapabilityID)
		if capabilityErr != nil {
			return executionCapsule{}, capabilityErr
		}
		scopeDigest, capabilityErr := digestFromCanonicalText(capabilityWire.ScopeDigest)
		if capabilityErr != nil {
			return executionCapsule{}, capabilityErr
		}
		expiresAt, capabilityErr := time.Parse(time.RFC3339Nano, capabilityWire.ExpiresAt)
		if capabilityErr != nil {
			return executionCapsule{}, newError(ErrorIntegrityConflict)
		}
		secretCapabilities = append(secretCapabilities, SecretCapability{
			CapabilityID: capabilityID, Class: SecretClass(capabilityWire.Class), Use: SecretUse(capabilityWire.Use),
			Generation: capabilityWire.Generation, ExpiresAt: expiresAt.UTC(), ScopeDigest: scopeDigest,
		})
	}
	capsule := executionCapsule{
		SchemaVersion: SchemaV1, CapsuleID: ExecutionCapsuleID{value: wire.CapsuleID},
		PersonalWorkspaceID: workspaceID, TaskID: taskID, PhaseRunID: phaseRunID,
		RuntimeRunID: runtimeRunID, OperationID: operationID, CanonicalRequestDigest: requestDigest,
		RuntimeBindingID: runtimeBindingID, RuntimeBindingDigest: bindingDigest, ExecutionLockDigest: lockDigest,
		CapabilityContractDigest:    capabilityContractDigest,
		AllowedPlatformImagesDigest: allowedPlatformImagesDigest,
		ExecutorContractDigest:      executorContractDigest,
		WorkerClass:                 WorkerClass(wire.WorkerClass), Effect: EffectClass(wire.Effect),
		OutputContractDigest: outputDigest, EvidenceContractDigest: evidenceDigest,
		ExecutionNodeID: nodeID, NodeGeneration: NodeGeneration(wire.NodeGeneration),
		NodeAuthorityID:   NodeAuthorityID{value: wire.NodeAuthorityID},
		WorkerAuthorityID: WorkerAuthorityID{value: wire.WorkerAuthorityID},
		WorkerGeneration:  WorkerGeneration(wire.WorkerGeneration),
		SandboxLeaseID:    SandboxLeaseID{value: wire.SandboxLeaseID},
		LeaseGeneration:   LeaseGeneration(wire.LeaseGeneration), LeaseFence: LeaseFence(wire.LeaseFence),
		RuntimeFence: RuntimeFence(wire.RuntimeFence), AuthorizationGeneration: AuthorizationGeneration(wire.AuthorizationGeneration),
		AuthorizationExpiresAt: authorizationExpiresAt.UTC(),
		ReleaseSafetyEpoch:     ReleaseSafetyEpoch(wire.ReleaseSafetyEpoch),
		CatalogSafetyEpoch:     CatalogSafetyEpoch(wire.CatalogSafetyEpoch), Deadline: deadline.UTC(),
		NetworkPolicyID: networkPolicyID, SecretPolicyID: secretPolicyID,
		Trace:         trace,
		RuntimeViewID: RuntimeViewID{value: wire.RuntimeViewID}, GatewayGrantID: GatewayGrantID{value: wire.GatewayGrantID},
		GatewayGrantGeneration:     GatewayGrantGeneration(wire.GatewayGrantGeneration),
		QuotaReservationID:         QuotaReservationID{value: wire.QuotaReservationID},
		QuotaReservationGeneration: QuotaReservationGeneration(wire.QuotaReservationGeneration),
		ResolutionEvidenceID:       EvidenceID{value: wire.ResolutionEvidenceID}, ResolutionEvidenceDigest: resolutionDigest,
		Inputs: ResolvedImmutableInputManifest{
			SchemaVersion: SchemaV1, Identity: ImmutableInputManifestIdentity{value: wire.InputManifestIdentity},
			Digest: inputManifestDigest, InputCount: uint64(len(inputs)),
			MaterializationEvidenceID:     EvidenceID{value: wire.InputMaterializationEvidenceID},
			MaterializationEvidenceDigest: inputMaterializationDigest, Entries: inputs,
		},
		Outputs: OutputContract{
			SchemaVersion: SchemaV1, ContractDigest: outputDigest, Channels: channels,
			MaxOutputCount: wire.OutputMaxCount, MaxTotalSizeBytes: wire.OutputMaxTotalSizeBytes,
		},
		Security: ExecutionSecurityContract{
			SchemaVersion: SchemaV1, EvidenceID: EvidenceID{value: wire.SecurityEvidenceID}, EvidenceDigest: securityEvidenceDigest,
			Policy: HostileExecutionPolicy{
				ExecutionPolicyID: ExecutionPolicyID{value: wire.ExecutionPolicyID}, PolicyDigest: policyDigest,
				SandboxDriverDigest: sandboxDriverDigest, HostGeneration: wire.HostGeneration,
				NodeGeneration: NodeGeneration(wire.NodeGeneration), KernelRuntimeDigest: kernelRuntimeDigest,
				ActualImageDigest: actualImageDigest, ActualExecutorDigest: actualExecutorDigest,
				ImageAuthorizationDigest: imageAuthorizationDigest, ExecutorAuthorizationDigest: executorAuthorizationDigest,
				MountPolicyDigest: mountPolicyDigest, WritableLocationsDigest: writableLocationsDigest,
				CredentialPolicyDigest: credentialPolicyDigest, ResetStateDigest: resetStateDigest,
				CapabilityCompatibilityDigest: compatibilityDigest, AttestationID: NodeAttestationID{value: wire.AttestationID},
				AttestationDigest: attestationDigest, AttestationGeneration: NodeAttestationGeneration(wire.AttestationGeneration),
				AttestationExpiresAt: attestationExpiresAt.UTC(),
			},
			Network: ExecutionNetworkPolicy{
				NetworkPolicyID: networkPolicyID, PolicyDigest: networkPolicyDigest, Generation: wire.NetworkGeneration,
				ExpiresAt: networkExpiresAt.UTC(), DefaultDenyEgress: wire.DefaultDenyEgress,
				DefaultDenyIngress: wire.DefaultDenyIngress, DNSPinned: wire.DNSPinned,
				RedirectsDenied: wire.RedirectsDenied, ProxyBypassDenied: wire.ProxyBypassDenied,
				AlternatePortsDenied: wire.AlternatePortsDenied, ResolvedEndpointsDigest: resolvedEndpointsDigest,
				Rules: networkRules,
			},
			Secrets: ExecutionSecretPolicy{
				SecretPolicyID: secretPolicyID, PolicyDigest: secretPolicyDigest, Generation: wire.SecretGeneration,
				ExpiresAt: secretExpiresAt.UTC(), Capabilities: secretCapabilities,
			},
		},
		SecurityAcceptance: ExecutionSecurityAcceptance{
			EvidenceID: EvidenceID{value: wire.SecurityEvidenceID}, EvidenceDigest: securityEvidenceDigest,
			ActualImageDigest: actualImageDigest, ActualExecutorDigest: actualExecutorDigest,
			NetworkRules: networkRules, SecretCapabilities: secretCapabilities,
		},
	}
	if wire.GatewayGrantDigest != "" {
		capsule.GatewayGrantDigest, err = digestFromCanonicalText(wire.GatewayGrantDigest)
		if err != nil {
			return executionCapsule{}, err
		}
	}
	if wire.GatewayGrantExpiresAt != "" {
		capsule.GatewayGrantExpiresAt, err = time.Parse(time.RFC3339Nano, wire.GatewayGrantExpiresAt)
		if err != nil {
			return executionCapsule{}, newError(ErrorIntegrityConflict)
		}
		capsule.GatewayGrantExpiresAt = capsule.GatewayGrantExpiresAt.UTC()
	}
	switch capsule.Effect {
	case EffectMutating:
		if !validOpaqueID(wire.RuntimeViewID) || !validOpaqueID(wire.RuntimeViewOpenOperationID) ||
			wire.RuntimeViewOpenRequestDigest == "" || wire.RuntimeViewLeaseAuthorityDigest == "" ||
			wire.RuntimeViewExpiresAt == "" || wire.RuntimeViewLifecycleGeneration == 0 ||
			wire.RuntimeViewLifecycleFence == 0 {
			return executionCapsule{}, newError(ErrorIntegrityConflict)
		}
		capsule.RuntimeViewOpenOperationID, err = NewOperationID(wire.RuntimeViewOpenOperationID)
		if err != nil {
			return executionCapsule{}, err
		}
		capsule.RuntimeViewOpenRequestDigest, err = digestFromCanonicalText(wire.RuntimeViewOpenRequestDigest)
		if err != nil {
			return executionCapsule{}, err
		}
		capsule.RuntimeViewLeaseAuthorityDigest, err = digestFromCanonicalText(wire.RuntimeViewLeaseAuthorityDigest)
		if err != nil {
			return executionCapsule{}, err
		}
		capsule.RuntimeViewExpiresAt, err = time.Parse(time.RFC3339Nano, wire.RuntimeViewExpiresAt)
		if err != nil {
			return executionCapsule{}, newError(ErrorIntegrityConflict)
		}
		capsule.RuntimeViewExpiresAt = capsule.RuntimeViewExpiresAt.UTC()
		capsule.RuntimeViewLifecycleGeneration = TaskWorkspaceLifecycleGeneration(wire.RuntimeViewLifecycleGeneration)
		capsule.RuntimeViewLifecycleFence = TaskWorkspaceLifecycleFence(wire.RuntimeViewLifecycleFence)
	case EffectReadOnly:
		if wire.RuntimeViewID != "" || wire.RuntimeViewOpenOperationID != "" ||
			wire.RuntimeViewOpenRequestDigest != "" || wire.RuntimeViewLeaseAuthorityDigest != "" ||
			wire.RuntimeViewExpiresAt != "" || wire.RuntimeViewLifecycleGeneration != 0 ||
			wire.RuntimeViewLifecycleFence != 0 {
			return executionCapsule{}, newError(ErrorIntegrityConflict)
		}
	}
	if !validOpaqueID(capsule.CapsuleID.String()) || !validOpaqueID(capsule.SandboxLeaseID.String()) ||
		capsule.CapabilityContractDigest == (Digest{}) || capsule.AllowedPlatformImagesDigest == (Digest{}) ||
		capsule.ExecutorContractDigest == (Digest{}) || capsule.NodeGeneration == 0 ||
		!validOpaqueID(capsule.NodeAuthorityID.String()) || !validOpaqueID(capsule.WorkerAuthorityID.String()) ||
		capsule.WorkerGeneration == 0 || capsule.AuthorizationExpiresAt.IsZero() ||
		capsule.LeaseGeneration == 0 || capsule.LeaseFence == 0 || capsule.RuntimeFence == 0 ||
		capsule.AuthorizationGeneration == 0 || capsule.ReleaseSafetyEpoch == 0 ||
		capsule.WorkerClass < WorkerAgent || capsule.WorkerClass > WorkerTool ||
		capsule.Effect < EffectReadOnly || capsule.Effect > EffectMutating || capsule.Deadline.IsZero() ||
		!validOpaqueID(capsule.ResolutionEvidenceID.String()) || !validOpaqueID(capsule.Security.EvidenceID.String()) ||
		len(capsule.Inputs.Entries) == 0 || len(capsule.Outputs.Channels) == 0 {
		return executionCapsule{}, newError(ErrorIntegrityConflict)
	}
	if !validDecodedCapsuleInputs(capsule.Inputs) || !validDecodedCapsuleSecurity(capsule) {
		return executionCapsule{}, newError(ErrorIntegrityConflict)
	}
	if _, err := validateOutputContract(capsule.Outputs); err != nil {
		return executionCapsule{}, newError(ErrorIntegrityConflict)
	}
	return capsule, nil
}

func validDecodedCapsuleInputs(manifest ResolvedImmutableInputManifest) bool {
	if !validOpaqueID(manifest.Identity.String()) || manifest.Digest == (Digest{}) ||
		!validOpaqueID(manifest.MaterializationEvidenceID.String()) ||
		manifest.MaterializationEvidenceDigest == (Digest{}) || uint64(len(manifest.Entries)) != manifest.InputCount {
		return false
	}
	identities := make(map[ImmutableInputIdentity]struct{}, len(manifest.Entries))
	locations := make(map[string]struct{}, len(manifest.Entries))
	for _, input := range manifest.Entries {
		if _, exists := identities[input.Identity]; exists {
			return false
		}
		identities[input.Identity] = struct{}{}
		if _, exists := locations[input.LogicalLocation]; exists {
			return false
		}
		locations[input.LogicalLocation] = struct{}{}
	}
	return true
}

func validDecodedCapsuleSecurity(capsule executionCapsule) bool {
	policy := capsule.Security.Policy
	if !validOpaqueID(policy.ExecutionPolicyID.String()) || policy.PolicyDigest == (Digest{}) ||
		policy.SandboxDriverDigest == (Digest{}) || policy.HostGeneration == 0 || policy.NodeGeneration == 0 ||
		policy.KernelRuntimeDigest == (Digest{}) || policy.ActualImageDigest == (Digest{}) ||
		policy.ActualExecutorDigest == (Digest{}) || policy.ImageAuthorizationDigest == (Digest{}) ||
		policy.ExecutorAuthorizationDigest == (Digest{}) || policy.MountPolicyDigest == (Digest{}) ||
		policy.WritableLocationsDigest == (Digest{}) || policy.CredentialPolicyDigest == (Digest{}) ||
		policy.ResetStateDigest == (Digest{}) || policy.CapabilityCompatibilityDigest == (Digest{}) ||
		!validOpaqueID(policy.AttestationID.String()) || policy.AttestationDigest == (Digest{}) ||
		policy.AttestationGeneration == 0 || policy.AttestationExpiresAt.IsZero() {
		return false
	}
	network := capsule.Security.Network
	if network.NetworkPolicyID != capsule.NetworkPolicyID || network.PolicyDigest == (Digest{}) ||
		network.Generation == 0 || network.ExpiresAt.IsZero() || network.ResolvedEndpointsDigest == (Digest{}) ||
		!network.DefaultDenyEgress || !network.DefaultDenyIngress || !network.DNSPinned ||
		!network.RedirectsDenied || !network.ProxyBypassDenied || !network.AlternatePortsDenied {
		return false
	}
	seenDestinations := make(map[NetworkDestinationID]struct{}, len(network.Rules))
	providerGateway := false
	for _, rule := range network.Rules {
		if !validOpaqueReferenceID(rule.DestinationID.String()) || rule.Protocol != NetworkProtocolTLS || rule.Port != 443 ||
			rule.DestinationKind == NetworkDestinationDirectProvider ||
			rule.DestinationKind < NetworkDestinationPlatformService || rule.DestinationKind > NetworkDestinationDirectProvider ||
			rule.Purpose < NetworkPurposeInternalService || rule.Purpose > NetworkPurposeProviderEgress ||
			rule.DestinationKind == NetworkDestinationLLMGateway && rule.Purpose != NetworkPurposeProviderEgress {
			return false
		}
		if _, exists := seenDestinations[rule.DestinationID]; exists {
			return false
		}
		seenDestinations[rule.DestinationID] = struct{}{}
		if rule.Purpose == NetworkPurposeProviderEgress {
			if rule.DestinationKind != NetworkDestinationLLMGateway {
				return false
			}
			providerGateway = true
		}
	}
	provider := capsule.GatewayGrantID != (GatewayGrantID{}) || capsule.GatewayGrantGeneration != 0 ||
		capsule.GatewayGrantDigest != (Digest{}) || !capsule.GatewayGrantExpiresAt.IsZero() ||
		capsule.QuotaReservationID != (QuotaReservationID{}) || capsule.QuotaReservationGeneration != 0
	if provider {
		if !validOpaqueID(capsule.GatewayGrantID.String()) || capsule.GatewayGrantGeneration == 0 ||
			capsule.GatewayGrantDigest == (Digest{}) || capsule.GatewayGrantExpiresAt.IsZero() ||
			!validOpaqueID(capsule.QuotaReservationID.String()) || capsule.QuotaReservationGeneration == 0 || !providerGateway {
			return false
		}
	} else if providerGateway {
		return false
	}
	secrets := capsule.Security.Secrets
	if secrets.SecretPolicyID != capsule.SecretPolicyID || secrets.PolicyDigest == (Digest{}) ||
		secrets.Generation == 0 || secrets.ExpiresAt.IsZero() {
		return false
	}
	seenCapabilities := make(map[SecretCapabilityID]struct{}, len(secrets.Capabilities))
	for _, capability := range secrets.Capabilities {
		if !validOpaqueReferenceID(capability.CapabilityID.String()) || capability.Class != SecretClassEphemeralRuntimeCredential ||
			(capability.Use != SecretUseLLMGateway && capability.Use != SecretUseToolRuntime) ||
			capability.Generation == 0 || capability.ExpiresAt.IsZero() || capability.ScopeDigest == (Digest{}) ||
			capability.Revoked || capability.Use == SecretUseLLMGateway && !provider {
			return false
		}
		if _, exists := seenCapabilities[capability.CapabilityID]; exists {
			return false
		}
		seenCapabilities[capability.CapabilityID] = struct{}{}
	}
	return true
}

func dispatchOperationID(capsuleID ExecutionCapsuleID, digest Digest) OperationID {
	material := digestBytes([]byte("slidesmith.runtime-execution.dispatch/v1\n" + capsuleID.String() + "\n" + digest.String()))
	return OperationID{value: fmt.Sprintf("runtime-dispatch-%x", material[:12])}
}

func optionalDigestText(value Digest) string {
	if value == (Digest{}) {
		return ""
	}
	return value.String()
}

func optionalTimeText(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func (engine *invariantEngine) ensureInMemoryExecutionCapsule(
	ctx context.Context,
	start StartRuntimeRun,
	decision RuntimeDecision,
) (RuntimeDecision, error) {
	if decision.Fact.Disposition != DecisionAccepted {
		return decision, nil
	}
	engine.store.mu.Lock()
	record := engine.store.runtimes[start.RuntimeRunID]
	if record == nil {
		engine.store.mu.Unlock()
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
	if record.capsule.snapshot.State == CapsulePrepared {
		decision.Snapshot = snapshot(record, SnapshotSchemaCurrent)
		engine.store.mu.Unlock()
		return decision, nil
	}
	if record.acceptedStartDigest != start.CanonicalRequestDigest ||
		record.fixture.State != RuntimePreparingPrerequisites || record.fixture.Outcome != RuntimeOutcomeNone {
		decision.Snapshot = snapshot(record, SnapshotSchemaCurrent)
		engine.store.mu.Unlock()
		return decision, nil
	}
	now := engine.clock.current()
	projected := snapshot(record, SnapshotSchemaCurrent)
	if !capsulePrerequisitesReadyAt(projected, now) {
		decision.Snapshot = projected
		engine.store.mu.Unlock()
		return decision, nil
	}
	resolver := engine.executionCapsuleResolver
	engine.store.mu.Unlock()
	if resolver == nil {
		decision.Snapshot = projected
		return decision, nil
	}
	resolution, err := resolver.ResolveExecutionCapsule(ctx, ExecutionCapsuleResolutionRequest{
		Start: start, Snapshot: projected, Now: now,
	})
	if err != nil {
		return RuntimeDecision{}, newError(ErrorDependencyUnavailable)
	}
	resolution, security, err := validateExecutionCapsuleResolution(start, projected, now, resolution)
	if err != nil {
		return RuntimeDecision{}, err
	}

	engine.store.mu.Lock()
	defer engine.store.mu.Unlock()
	record = engine.store.runtimes[start.RuntimeRunID]
	if record == nil {
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
	currentNow := engine.clock.current()
	current := snapshot(record, SnapshotSchemaCurrent)
	if !capsulePrerequisitesReadyAt(current, currentNow) ||
		record.fixture.State != RuntimePreparingPrerequisites || record.fixture.Outcome != RuntimeOutcomeNone {
		return RuntimeDecision{}, newError(ErrorAuthorizationDenied)
	}
	if record.gateway.Applicability == GatewayPrerequisiteRequired {
		grant := record.gateway.CurrentGrant
		if !quotaReservationAuthorizesGateway(engine.store.reservations[grant.QuotaReservationID], record, currentNow) {
			return RuntimeDecision{}, newError(ErrorAuthorizationDenied)
		}
	}
	resolution, security, err = validateExecutionCapsuleResolution(start, current, currentNow, resolution)
	if err != nil {
		return RuntimeDecision{}, err
	}
	capsule, canonical, capsuleDigest, err := buildExecutionCapsule(
		start, current, currentNow, resolution, security,
	)
	if err != nil {
		return RuntimeDecision{}, err
	}
	if record.capsule.snapshot.State == CapsulePrepared {
		if record.capsule.snapshot.Digest != capsuleDigest || !bytes.Equal(record.capsule.wire, canonical) {
			return RuntimeDecision{}, newError(ErrorIntegrityConflict)
		}
		decision.Snapshot = snapshot(record, SnapshotSchemaCurrent)
		return decision, nil
	}
	dispatchID := dispatchOperationID(capsule.CapsuleID, capsuleDigest)
	record.capsule = retainedExecutionCapsule{
		snapshot: RuntimeCapsuleSnapshot{
			State: CapsulePrepared, CapsuleID: capsule.CapsuleID, Digest: capsuleDigest,
		},
		dispatchOperationID: dispatchID,
		wire:                append([]byte(nil), canonical...), decoded: capsule, disposition: DispatchPending,
	}
	updateCapsuleReadiness(&record.readiness, record.runtimeViewBinding, record.lease, record.capsule.snapshot)
	decision.Snapshot = snapshot(record, SnapshotSchemaCurrent)
	return decision, nil
}

func (engine *invariantEngine) ClaimDispatch(
	ctx context.Context,
	request DispatchClaimRequest,
) (DispatchDelivery, error) {
	if ctx == nil || ctx.Err() != nil {
		return DispatchDelivery{}, newError(ErrorDependencyUnavailable)
	}
	engine.store.mu.Lock()
	defer engine.store.mu.Unlock()
	record := engine.store.runtimes[request.RuntimeRunID]
	if record == nil || record.capsule.snapshot.State != CapsulePrepared {
		return DispatchDelivery{}, newError(ErrorAuthorizationDenied)
	}
	if request.CapsuleID != record.capsule.snapshot.CapsuleID || request.Digest != record.capsule.snapshot.Digest {
		return DispatchDelivery{}, newError(ErrorAuthorizationDenied)
	}
	if record.capsule.disposition == DispatchAcknowledged {
		return DispatchDelivery{}, newError(ErrorIntegrityConflict)
	}
	now := engine.clock.current()
	projected := snapshot(record, SnapshotSchemaCurrent)
	projectCapsuleReadinessAt(&projected, now)
	if !projected.Readiness.CapsuleReady || !executionCapsuleDispatchCurrent(record, now) {
		return DispatchDelivery{}, newError(ErrorAuthorizationDenied)
	}
	if record.gateway.Applicability == GatewayPrerequisiteRequired {
		grant := record.gateway.CurrentGrant
		if !quotaReservationAuthorizesGateway(engine.store.reservations[grant.QuotaReservationID], record, now) {
			return DispatchDelivery{}, newError(ErrorAuthorizationDenied)
		}
	}
	record.capsule.deliveryCount++
	record.capsule.disposition = DispatchClaimed
	return DispatchDelivery{
		OperationID: record.capsule.dispatchOperationID, RuntimeRunID: request.RuntimeRunID,
		CapsuleID: record.capsule.snapshot.CapsuleID, CapsuleDigest: record.capsule.snapshot.Digest,
		Capsule: append([]byte(nil), record.capsule.wire...), Disposition: record.capsule.disposition,
		DeliveryCount: record.capsule.deliveryCount,
	}, nil
}

func executionCapsuleDispatchCurrent(record *runtimeRecord, now time.Time) bool {
	if record == nil || record.capsule.snapshot.State != CapsulePrepared ||
		record.capsule.decoded.CapsuleID == (ExecutionCapsuleID{}) {
		return false
	}
	capsule := record.capsule.decoded
	current := snapshot(record, SnapshotSchemaCurrent)
	if record.fixture.State != RuntimePreparingPrerequisites || record.fixture.Outcome != RuntimeOutcomeNone ||
		!now.UTC().Before(record.deadline) || !capsulePrerequisitesReadyAt(current, now.UTC()) ||
		capsule.PersonalWorkspaceID != record.fixture.PersonalWorkspaceID || capsule.TaskID != record.fixture.TaskID ||
		capsule.PhaseRunID != record.fixture.PhaseRunID || capsule.RuntimeRunID != record.fixture.RuntimeRunID ||
		capsule.OperationID != record.operation.OperationID || capsule.CanonicalRequestDigest != record.operation.Digest ||
		capsule.ExecutionNodeID != record.node.ExecutionNodeID ||
		capsule.NodeGeneration != record.node.Generation ||
		capsule.NodeAuthorityID != record.lease.NodeAuthorityID ||
		capsule.WorkerAuthorityID != record.lease.WorkerAuthorityID ||
		capsule.WorkerGeneration != record.lease.WorkerGeneration ||
		capsule.Security.Policy.NodeGeneration != record.node.Generation ||
		capsule.Security.Policy.AttestationID != record.node.AttestationID ||
		capsule.Security.Policy.AttestationGeneration != record.node.AttestationGeneration ||
		record.node.Readiness != NodeReady || record.node.Quarantined ||
		capsule.SandboxLeaseID != record.lease.LeaseID || capsule.LeaseGeneration != record.lease.Generation ||
		capsule.LeaseFence != record.lease.Fence || capsule.RuntimeFence != record.fixture.RuntimeFence ||
		capsule.AuthorizationGeneration != record.fixture.Owner.AuthorizationGeneration() ||
		!capsule.AuthorizationExpiresAt.Equal(record.lease.AuthorizationExpiresAt) ||
		capsule.ReleaseSafetyEpoch != record.fixture.SafetyEpoch || capsule.CatalogSafetyEpoch != record.catalogSafetyEpoch ||
		capsule.Security.Policy.ExecutionPolicyID != record.operation.ExecutionPolicyID ||
		capsule.NetworkPolicyID != capsule.Security.Network.NetworkPolicyID ||
		capsule.SecretPolicyID != capsule.Security.Secrets.SecretPolicyID ||
		!now.UTC().Before(capsule.Security.Policy.AttestationExpiresAt) ||
		!now.UTC().Before(capsule.Security.Network.ExpiresAt) || !now.UTC().Before(capsule.Security.Secrets.ExpiresAt) {
		return false
	}
	for _, input := range capsule.Inputs.Entries {
		if !now.UTC().Before(input.ExpiresAt) {
			return false
		}
	}
	for _, capability := range capsule.SecurityAcceptance.SecretCapabilities {
		if !now.UTC().Before(capability.ExpiresAt) || capability.Revoked {
			return false
		}
	}
	if capsule.Effect == EffectMutating {
		binding := record.runtimeViewBinding
		if capsule.RuntimeViewID == (RuntimeViewID{}) || capsule.RuntimeViewID != binding.RuntimeViewID ||
			capsule.RuntimeViewOpenOperationID != binding.OpenOperationID ||
			capsule.RuntimeViewOpenRequestDigest != binding.OpenRequestDigest ||
			capsule.RuntimeViewLeaseAuthorityDigest != binding.SandboxLeaseAuthorityDigest ||
			!capsule.RuntimeViewExpiresAt.Equal(binding.ExpiresAt) ||
			capsule.RuntimeViewLifecycleGeneration != binding.LifecycleGeneration ||
			capsule.RuntimeViewLifecycleFence != binding.LifecycleFence {
			return false
		}
	} else if record.runtimeViewBinding != (RuntimeViewBindingSnapshot{}) || capsule.RuntimeViewID != (RuntimeViewID{}) ||
		capsule.RuntimeViewOpenOperationID != (OperationID{}) || capsule.RuntimeViewOpenRequestDigest != (Digest{}) ||
		capsule.RuntimeViewLeaseAuthorityDigest != (Digest{}) || !capsule.RuntimeViewExpiresAt.IsZero() ||
		capsule.RuntimeViewLifecycleGeneration != 0 || capsule.RuntimeViewLifecycleFence != 0 {
		return false
	}
	if record.gateway.Applicability == GatewayPrerequisiteRequired {
		grant := record.gateway.CurrentGrant
		return record.gateway.Ready && capsule.GatewayGrantID == grant.GatewayGrantID &&
			capsule.GatewayGrantGeneration == grant.Generation && capsule.GatewayGrantDigest == grant.CanonicalDigest &&
			capsule.QuotaReservationID == grant.QuotaReservationID &&
			capsule.QuotaReservationGeneration == grant.QuotaReservationGeneration &&
			capsule.GatewayGrantExpiresAt.Equal(grant.ExpiresAt)
	}
	return capsule.GatewayGrantID == (GatewayGrantID{}) && capsule.GatewayGrantDigest == (Digest{}) &&
		capsule.QuotaReservationID == (QuotaReservationID{})
}

func (engine *invariantEngine) AcknowledgeDispatch(
	ctx context.Context,
	request DispatchAcknowledgementRequest,
) (DispatchAcknowledgement, error) {
	if ctx == nil || ctx.Err() != nil {
		return DispatchAcknowledgement{}, newError(ErrorDependencyUnavailable)
	}
	engine.store.mu.Lock()
	defer engine.store.mu.Unlock()
	record := engine.store.runtimes[request.RuntimeRunID]
	if record == nil || request.AckDigest == (Digest{}) ||
		record.capsule.snapshot.State != CapsulePrepared ||
		request.OperationID != record.capsule.dispatchOperationID ||
		request.CapsuleID != record.capsule.snapshot.CapsuleID ||
		request.CapsuleDigest != record.capsule.snapshot.Digest {
		return DispatchAcknowledgement{}, newError(ErrorAuthorizationDenied)
	}
	if record.capsule.disposition == DispatchAcknowledged {
		if record.capsule.ackDigest != request.AckDigest {
			return DispatchAcknowledgement{}, newError(ErrorIntegrityConflict)
		}
		return dispatchAcknowledgement(record, request.AckDigest), nil
	}
	if record.capsule.disposition != DispatchClaimed {
		return DispatchAcknowledgement{}, newError(ErrorIntegrityConflict)
	}
	record.capsule.disposition = DispatchAcknowledged
	record.capsule.ackDigest = request.AckDigest
	return dispatchAcknowledgement(record, request.AckDigest), nil
}

func dispatchAcknowledgement(record *runtimeRecord, ackDigest Digest) DispatchAcknowledgement {
	return DispatchAcknowledgement{
		OperationID:   record.capsule.dispatchOperationID,
		RuntimeRunID:  record.fixture.RuntimeRunID,
		CapsuleID:     record.capsule.snapshot.CapsuleID,
		CapsuleDigest: record.capsule.snapshot.Digest,
		AckDigest:     ackDigest, Disposition: DispatchAcknowledged,
	}
}

func knownRuntimeCapsuleSnapshot(capsule RuntimeCapsuleSnapshot) bool {
	if capsule == (RuntimeCapsuleSnapshot{}) {
		return true
	}
	return capsule.State == CapsulePrepared && validOpaqueID(capsule.CapsuleID.String()) &&
		capsule.Digest != (Digest{})
}
