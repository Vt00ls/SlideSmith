package runtimeexecution

import (
	"context"
	"fmt"
	"sort"
	"time"
)

type InputReadCapabilityID struct{ value string }

func NewInputReadCapabilityID(value string) (InputReadCapabilityID, error) {
	value, err := newOpaqueReferenceID(value)
	return InputReadCapabilityID{value: value}, err
}

func (id InputReadCapabilityID) String() string { return id.value }

type InputContentClass uint8

const (
	InputClassSourceMaterial InputContentClass = iota + 1
	InputClassTaskWorkspaceState
	InputClassRuntimePackage
	InputClassCatalogPackage
)

type InputAuthorityScopeKind uint8

const (
	InputScopeTask InputAuthorityScopeKind = iota + 1
	InputScopeRuntimeBinding
	InputScopeCatalogBinding
)

type InputAuthorityScope struct {
	Kind                 InputAuthorityScopeKind
	PersonalWorkspaceID  PersonalWorkspaceID
	TaskID               TaskID
	RuntimeBindingID     RuntimeBindingID
	RuntimeBindingDigest Digest
	TemplateLockID       TemplateLockID
	TemplateLockDigest   Digest
}

type ResolvedImmutableInput struct {
	Identity                      ImmutableInputIdentity
	Digest                        Digest
	SizeBytes                     uint64
	Class                         InputContentClass
	ReadCapabilityID              InputReadCapabilityID
	MaterializationEvidenceID     EvidenceID
	MaterializationEvidenceDigest Digest
	MaterializedIdentity          ImmutableInputIdentity
	MaterializedDigest            Digest
	MaterializedSizeBytes         uint64
	MaterializationGeneration     uint64
	MaterializationFence          uint64
	AuthorizationGeneration       AuthorizationGeneration
	RuntimeRunID                  RuntimeRunID
	OperationID                   OperationID
	SandboxLeaseID                SandboxLeaseID
	LeaseGeneration               LeaseGeneration
	LeaseFence                    LeaseFence
	ExpiresAt                     time.Time
	LogicalLocation               string
	AuthorityScope                InputAuthorityScope
}

type ResolvedImmutableInputManifest struct {
	SchemaVersion                 SchemaVersion
	Identity                      ImmutableInputManifestIdentity
	Digest                        Digest
	TotalSizeBytes                uint64
	InputCount                    uint64
	MaterializationEvidenceID     EvidenceID
	MaterializationEvidenceDigest Digest
	Entries                       []ResolvedImmutableInput
}

type ExecutionCapsuleResolutionRequest struct {
	Start    StartRuntimeRun
	Snapshot RuntimeSnapshot
	Now      time.Time
}

type ExecutionCapsuleResolution struct {
	SchemaVersion        SchemaVersion
	EvidenceID           EvidenceID
	EvidenceDigest       Digest
	Inputs               ResolvedImmutableInputManifest
	Outputs              OutputContract
	Security             ExecutionSecurityContract
	GatewayDestinationID NetworkDestinationID
}

// ExecutionCapsuleResolver is a private C03 adapter seam. Implementations may
// consult the input, node-policy, network, and secret authorities, but C03
// validates the returned acceptance again against current durable authority.
type ExecutionCapsuleResolver interface {
	ResolveExecutionCapsule(context.Context, ExecutionCapsuleResolutionRequest) (ExecutionCapsuleResolution, error)
}

type ExecutionCapsuleResolverFunc func(
	context.Context,
	ExecutionCapsuleResolutionRequest,
) (ExecutionCapsuleResolution, error)

func (function ExecutionCapsuleResolverFunc) ResolveExecutionCapsule(
	ctx context.Context,
	request ExecutionCapsuleResolutionRequest,
) (ExecutionCapsuleResolution, error) {
	return function(ctx, request)
}

type CapsuleInputValidationCode uint8

const (
	CapsuleInputValidationSchema CapsuleInputValidationCode = iota + 1
	CapsuleInputValidationMissing
	CapsuleInputValidationDuplicate
	CapsuleInputValidationIntegrity
	CapsuleInputValidationScope
	CapsuleInputValidationStale
	CapsuleInputValidationPath
)

type CapsuleInputValidationError struct{ code CapsuleInputValidationCode }

func (failure *CapsuleInputValidationError) Error() string {
	return "runtime execution input manifest failed closed validation"
}

func (failure *CapsuleInputValidationError) Code() CapsuleInputValidationCode {
	if failure == nil {
		return CapsuleInputValidationSchema
	}
	return failure.code
}

func capsuleInputValidationError(code CapsuleInputValidationCode) *CapsuleInputValidationError {
	return &CapsuleInputValidationError{code: code}
}

func validateExecutionCapsuleResolution(
	start StartRuntimeRun,
	snapshot RuntimeSnapshot,
	now time.Time,
	resolution ExecutionCapsuleResolution,
) (ExecutionCapsuleResolution, ExecutionSecurityAcceptance, error) {
	now = now.UTC()
	if resolution.SchemaVersion.Major() != SchemaV1.Major() ||
		!validOpaqueID(resolution.EvidenceID.String()) || resolution.EvidenceDigest == (Digest{}) {
		return ExecutionCapsuleResolution{}, ExecutionSecurityAcceptance{}, newError(ErrorIntegrityConflict)
	}
	inputs, err := validateResolvedImmutableInputs(start, snapshot, now, resolution.Inputs)
	if err != nil {
		if failure, ok := err.(*CapsuleInputValidationError); ok &&
			(failure.Code() == CapsuleInputValidationMissing || failure.Code() == CapsuleInputValidationScope) {
			return ExecutionCapsuleResolution{}, ExecutionSecurityAcceptance{}, newError(ErrorAuthorizationDenied)
		}
		return ExecutionCapsuleResolution{}, ExecutionSecurityAcceptance{}, newError(ErrorIntegrityConflict)
	}
	if resolution.Outputs.ContractDigest != start.OutputContractDigest {
		return ExecutionCapsuleResolution{}, ExecutionSecurityAcceptance{}, newError(ErrorIntegrityConflict)
	}
	if _, err := validateOutputContract(resolution.Outputs); err != nil {
		return ExecutionCapsuleResolution{}, ExecutionSecurityAcceptance{}, newError(ErrorIntegrityConflict)
	}
	validation := securityValidationContext(start, snapshot, now, resolution.GatewayDestinationID)
	security, err := ValidateExecutionSecurity(validation, resolution.Security)
	if err != nil {
		return ExecutionCapsuleResolution{}, ExecutionSecurityAcceptance{}, newError(ErrorIntegrityConflict)
	}
	resolution.Inputs.Entries = inputs
	resolution.Outputs.Channels = append([]DeclaredOutputChannel(nil), resolution.Outputs.Channels...)
	resolution.Security.Network.Rules = append([]NetworkRule(nil), resolution.Security.Network.Rules...)
	resolution.Security.Secrets.Allowed = append([]SecretPurpose(nil), resolution.Security.Secrets.Allowed...)
	resolution.Security.Secrets.Capabilities = append([]SecretCapability(nil), resolution.Security.Secrets.Capabilities...)
	return resolution, security, nil
}

func validateResolvedImmutableInputs(
	start StartRuntimeRun,
	snapshot RuntimeSnapshot,
	now time.Time,
	manifest ResolvedImmutableInputManifest,
) ([]ResolvedImmutableInput, error) {
	want := start.ImmutableInputManifest
	if manifest.SchemaVersion.Major() != SchemaV1.Major() || manifest.Identity != want.Identity ||
		manifest.Digest != want.Digest || manifest.TotalSizeBytes != want.TotalSizeBytes ||
		manifest.InputCount != want.InputCount ||
		manifest.MaterializationEvidenceID != want.MaterializationEvidenceID ||
		manifest.MaterializationEvidenceDigest != want.MaterializationEvidenceDigest ||
		uint64(len(manifest.Entries)) != want.InputCount || len(manifest.Entries) != len(start.ImmutableInputs) {
		return nil, capsuleInputValidationError(CapsuleInputValidationMissing)
	}
	expected := make(map[ImmutableInputIdentity]ImmutableInputBinding, len(start.ImmutableInputs))
	for _, input := range start.ImmutableInputs {
		if _, duplicate := expected[input.Identity]; duplicate {
			return nil, capsuleInputValidationError(CapsuleInputValidationDuplicate)
		}
		expected[input.Identity] = input
	}
	seen := make(map[ImmutableInputIdentity]struct{}, len(manifest.Entries))
	locations := make(map[string]struct{}, len(manifest.Entries))
	validated := append([]ResolvedImmutableInput(nil), manifest.Entries...)
	var total uint64
	for index := range validated {
		entry := &validated[index]
		declared, exists := expected[entry.Identity]
		if !exists {
			return nil, capsuleInputValidationError(CapsuleInputValidationMissing)
		}
		if _, duplicate := seen[entry.Identity]; duplicate {
			return nil, capsuleInputValidationError(CapsuleInputValidationDuplicate)
		}
		seen[entry.Identity] = struct{}{}
		if entry.Digest == (Digest{}) || entry.Digest != declared.Digest || entry.SizeBytes != declared.SizeBytes ||
			entry.MaterializedIdentity != entry.Identity || entry.MaterializedDigest != entry.Digest ||
			entry.MaterializedSizeBytes != entry.SizeBytes ||
			!validOpaqueReferenceID(entry.ReadCapabilityID.String()) ||
			!validOpaqueID(entry.MaterializationEvidenceID.String()) ||
			entry.MaterializationEvidenceDigest == (Digest{}) || entry.MaterializationGeneration == 0 ||
			entry.MaterializationFence == 0 || entry.Class < InputClassSourceMaterial ||
			entry.Class > InputClassCatalogPackage {
			return nil, capsuleInputValidationError(CapsuleInputValidationIntegrity)
		}
		if !safeSandboxLogicalPath(entry.LogicalLocation) {
			return nil, capsuleInputValidationError(CapsuleInputValidationPath)
		}
		if _, collision := locations[entry.LogicalLocation]; collision {
			return nil, capsuleInputValidationError(CapsuleInputValidationDuplicate)
		}
		locations[entry.LogicalLocation] = struct{}{}
		if entry.RuntimeRunID != start.RuntimeRunID || entry.OperationID != start.OperationID ||
			entry.SandboxLeaseID != snapshot.Lease.LeaseID || entry.LeaseGeneration != snapshot.Lease.Generation ||
			entry.LeaseFence != snapshot.Lease.Fence ||
			entry.AuthorizationGeneration != start.Authority.AuthorizationGeneration() ||
			entry.ExpiresAt.IsZero() || !now.Before(entry.ExpiresAt) ||
			entry.ExpiresAt.After(snapshot.Lease.ExpiresAt) ||
			entry.ExpiresAt.After(snapshot.Lease.AuthorizationExpiresAt) || entry.ExpiresAt.After(start.Deadline) {
			return nil, capsuleInputValidationError(CapsuleInputValidationStale)
		}
		if !validInputAuthorityScope(start, entry.Class, entry.AuthorityScope) {
			return nil, capsuleInputValidationError(CapsuleInputValidationScope)
		}
		if total > want.TotalSizeBytes-entry.SizeBytes {
			return nil, capsuleInputValidationError(CapsuleInputValidationIntegrity)
		}
		total += entry.SizeBytes
	}
	if len(seen) != len(expected) || total != want.TotalSizeBytes {
		return nil, capsuleInputValidationError(CapsuleInputValidationMissing)
	}
	sort.Slice(validated, func(left, right int) bool {
		return validated[left].Identity.String() < validated[right].Identity.String()
	})
	return validated, nil
}

func validInputAuthorityScope(start StartRuntimeRun, class InputContentClass, scope InputAuthorityScope) bool {
	switch scope.Kind {
	case InputScopeTask:
		return (class == InputClassSourceMaterial || class == InputClassTaskWorkspaceState) &&
			scope.PersonalWorkspaceID == start.PersonalWorkspaceID && scope.TaskID == start.TaskID &&
			scope.RuntimeBindingID == (RuntimeBindingID{}) && scope.RuntimeBindingDigest == (Digest{}) &&
			scope.TemplateLockID == (TemplateLockID{}) && scope.TemplateLockDigest == (Digest{})
	case InputScopeRuntimeBinding:
		return class == InputClassRuntimePackage && scope.PersonalWorkspaceID == (PersonalWorkspaceID{}) &&
			scope.TaskID == (TaskID{}) && scope.RuntimeBindingID == start.RuntimeBindingID &&
			scope.RuntimeBindingDigest == start.RuntimeBindingDigest && scope.TemplateLockID == (TemplateLockID{}) &&
			scope.TemplateLockDigest == (Digest{})
	case InputScopeCatalogBinding:
		return class == InputClassCatalogPackage && start.CatalogBinding != nil &&
			scope.PersonalWorkspaceID == (PersonalWorkspaceID{}) && scope.TaskID == (TaskID{}) &&
			scope.RuntimeBindingID == (RuntimeBindingID{}) && scope.RuntimeBindingDigest == (Digest{}) &&
			scope.TemplateLockID == start.CatalogBinding.TemplateLockID &&
			scope.TemplateLockDigest == start.CatalogBinding.TemplateLockDigest
	default:
		return false
	}
}

func securityValidationContext(
	start StartRuntimeRun,
	snapshot RuntimeSnapshot,
	now time.Time,
	gatewayDestinationID NetworkDestinationID,
) SecurityValidationContext {
	return SecurityValidationContext{
		Now: now, Deadline: start.Deadline, RuntimeRunID: start.RuntimeRunID, OperationID: start.OperationID,
		ExecutionNodeID: snapshot.Node.ExecutionNodeID, NodeGeneration: snapshot.Node.Generation,
		NodeAttestationID: snapshot.Node.AttestationID, NodeAttestationGeneration: snapshot.Node.AttestationGeneration,
		SandboxLeaseID: snapshot.Lease.LeaseID, LeaseGeneration: snapshot.Lease.Generation,
		LeaseFence: snapshot.Lease.Fence, RuntimeFence: snapshot.RuntimeFence,
		SafetyEpoch: start.ReleaseSafetyEpoch, LeaseExpiresAt: snapshot.Lease.ExpiresAt,
		AuthorizationExpiresAt:      snapshot.Lease.AuthorizationExpiresAt,
		AllowedPlatformImagesDigest: start.AllowedPlatformImagesDigest,
		ExecutorContractDigest:      start.ExecutorContractDigest,
		CapabilityContractDigest:    start.CapabilityContractDigest,
		ExecutionPolicyID:           start.ExecutionPolicyID, NetworkPolicyID: start.NetworkPolicyID,
		SecretPolicyID: start.SecretPolicyID, ProviderCapability: start.ProviderCapability,
		GatewayDestinationID: gatewayDestinationID,
	}
}

// deterministicCapsuleResolver is the controlled adapter used only by the
// deterministic harness and PostgreSQL contract fixtures.
type deterministicCapsuleResolver struct{}

// NewDeterministicCapsuleResolver returns the controlled Capsule resolver used
// by cross-package contract fixtures. PostgreSQL authorities still fail closed
// unless callers explicitly configure this adapter or a production resolver.
func NewDeterministicCapsuleResolver() ExecutionCapsuleResolver {
	return deterministicCapsuleResolver{}
}

func (deterministicCapsuleResolver) ResolveExecutionCapsule(
	_ context.Context,
	request ExecutionCapsuleResolutionRequest,
) (ExecutionCapsuleResolution, error) {
	start, snapshot := request.Start, request.Snapshot
	expiresAt := earliestTime(start.Deadline, snapshot.Lease.ExpiresAt, snapshot.Lease.AuthorizationExpiresAt)
	entries := make([]ResolvedImmutableInput, 0, len(start.ImmutableInputs))
	for index, input := range start.ImmutableInputs {
		capabilityDigest := digestBytes([]byte("slidesmith.runtime-execution.input-read-capability/v1\n" + input.Identity.String()))
		entries = append(entries, ResolvedImmutableInput{
			Identity: input.Identity, Digest: input.Digest, SizeBytes: input.SizeBytes,
			Class:                         InputClassSourceMaterial,
			ReadCapabilityID:              InputReadCapabilityID{value: fmt.Sprintf("input-read-capability-%x", capabilityDigest[:12])},
			MaterializationEvidenceID:     EvidenceID{value: fmt.Sprintf("input-materialization-%d-%s", index+1, input.Identity.String())},
			MaterializationEvidenceDigest: digestBytes([]byte("input-materialization-evidence\n" + input.Identity.String())),
			MaterializedIdentity:          input.Identity, MaterializedDigest: input.Digest, MaterializedSizeBytes: input.SizeBytes,
			MaterializationGeneration: 1, MaterializationFence: 1,
			AuthorizationGeneration: start.Authority.AuthorizationGeneration(),
			RuntimeRunID:            start.RuntimeRunID, OperationID: start.OperationID,
			SandboxLeaseID: snapshot.Lease.LeaseID, LeaseGeneration: snapshot.Lease.Generation,
			LeaseFence: snapshot.Lease.Fence, ExpiresAt: expiresAt,
			LogicalLocation: fmt.Sprintf("inputs/%03d.bin", index+1),
			AuthorityScope: InputAuthorityScope{
				Kind: InputScopeTask, PersonalWorkspaceID: start.PersonalWorkspaceID, TaskID: start.TaskID,
			},
		})
	}
	gatewayDestinationID := NetworkDestinationID{}
	networkRules := []NetworkRule(nil)
	secretAllowed := []SecretPurpose(nil)
	secretCapabilities := []SecretCapability(nil)
	if start.ProviderCapability == ProviderCapabilityRequired {
		gatewayDestinationID = NetworkDestinationID{value: "controlled-llm-gateway"}
		networkRules = []NetworkRule{{
			DestinationKind: NetworkDestinationLLMGateway, DestinationID: gatewayDestinationID,
			Protocol: NetworkProtocolTLS, Port: 443, Purpose: NetworkPurposeProviderEgress,
		}}
		secretAllowed = []SecretPurpose{{Class: SecretClassEphemeralRuntimeCredential, Use: SecretUseLLMGateway}}
		secretCapabilities = []SecretCapability{{
			CapabilityID: SecretCapabilityID{value: "opaque-gateway-capability"},
			Class:        SecretClassEphemeralRuntimeCredential, Use: SecretUseLLMGateway,
			RuntimeRunID: start.RuntimeRunID, OperationID: start.OperationID,
			ExecutionNodeID: snapshot.Node.ExecutionNodeID, SandboxLeaseID: snapshot.Lease.LeaseID,
			LeaseGeneration: snapshot.Lease.Generation, LeaseFence: snapshot.Lease.Fence,
			RuntimeFence: snapshot.RuntimeFence, SafetyEpoch: start.ReleaseSafetyEpoch,
			Generation: 1, ExpiresAt: expiresAt, ScopeDigest: digestBytes([]byte("gateway-capability-scope")),
		}}
	}
	securityEvidenceID := EvidenceID{value: "execution-security-" + start.RuntimeRunID.String()}
	return ExecutionCapsuleResolution{
		SchemaVersion: SchemaV1, EvidenceID: EvidenceID{value: "capsule-resolution-" + start.RuntimeRunID.String()},
		EvidenceDigest: digestBytes([]byte("capsule-resolution\n" + start.CanonicalRequestDigest.String())),
		Inputs: ResolvedImmutableInputManifest{
			SchemaVersion: SchemaV1, Identity: start.ImmutableInputManifest.Identity,
			Digest: start.ImmutableInputManifest.Digest, TotalSizeBytes: start.ImmutableInputManifest.TotalSizeBytes,
			InputCount:                    start.ImmutableInputManifest.InputCount,
			MaterializationEvidenceID:     start.ImmutableInputManifest.MaterializationEvidenceID,
			MaterializationEvidenceDigest: start.ImmutableInputManifest.MaterializationEvidenceDigest,
			Entries:                       entries,
		},
		Outputs: OutputContract{
			SchemaVersion: SchemaV1, ContractDigest: start.OutputContractDigest,
			Channels: []DeclaredOutputChannel{{
				ChannelID: OutputChannelID{value: "declared-runtime-output"}, LogicalPath: "outputs/result.bin",
				Class: OutputClassOpaque, Required: true, MaxSizeBytes: 64 << 20,
			}},
			MaxOutputCount: 1, MaxTotalSizeBytes: 64 << 20,
		},
		Security: ExecutionSecurityContract{
			SchemaVersion: SchemaV1, EvidenceID: securityEvidenceID,
			EvidenceDigest: digestBytes([]byte("execution-security\n" + start.CanonicalRequestDigest.String())),
			Policy: HostileExecutionPolicy{
				ExecutionPolicyID: start.ExecutionPolicyID, PolicyDigest: digestBytes([]byte("execution-policy")),
				SandboxDriverDigest: digestBytes([]byte("sandbox-driver")), HostGeneration: 1,
				NodeGeneration: snapshot.Node.Generation, KernelRuntimeDigest: digestBytes([]byte("kernel-runtime")),
				ActualImageDigest: digestBytes([]byte("actual-image")), ActualExecutorDigest: digestBytes([]byte("actual-executor")),
				ImageAuthorizationDigest:    start.AllowedPlatformImagesDigest,
				ExecutorAuthorizationDigest: start.ExecutorContractDigest,
				MountPolicyDigest:           digestBytes([]byte("mount-policy")), WritableLocationsDigest: digestBytes([]byte("writable-locations")),
				CredentialPolicyDigest: digestBytes([]byte("credential-policy")), ResetStateDigest: digestBytes([]byte("reset-state")),
				CapabilityCompatibilityDigest: start.CapabilityContractDigest,
				AttestationID:                 snapshot.Node.AttestationID, AttestationDigest: digestBytes([]byte("node-attestation")),
				AttestationGeneration: snapshot.Node.AttestationGeneration, AttestationExpiresAt: expiresAt,
			},
			Network: ExecutionNetworkPolicy{
				NetworkPolicyID: start.NetworkPolicyID, PolicyDigest: digestBytes([]byte("network-policy")), Generation: 1,
				RuntimeRunID: start.RuntimeRunID, OperationID: start.OperationID,
				SandboxLeaseID: snapshot.Lease.LeaseID, LeaseGeneration: snapshot.Lease.Generation,
				LeaseFence: snapshot.Lease.Fence, RuntimeFence: snapshot.RuntimeFence, ExpiresAt: expiresAt,
				DefaultDenyEgress: true, DefaultDenyIngress: true, DNSPinned: true,
				RedirectsDenied: true, ProxyBypassDenied: true, AlternatePortsDenied: true,
				ResolvedEndpointsDigest: digestBytes([]byte("resolved-network-endpoints")), Rules: networkRules,
			},
			Secrets: ExecutionSecretPolicy{
				SecretPolicyID: start.SecretPolicyID, PolicyDigest: digestBytes([]byte("secret-policy")), Generation: 1,
				RuntimeRunID: start.RuntimeRunID, OperationID: start.OperationID,
				ExecutionNodeID: snapshot.Node.ExecutionNodeID, SandboxLeaseID: snapshot.Lease.LeaseID,
				LeaseGeneration: snapshot.Lease.Generation, LeaseFence: snapshot.Lease.Fence,
				RuntimeFence: snapshot.RuntimeFence, SafetyEpoch: start.ReleaseSafetyEpoch,
				ExpiresAt: expiresAt, Allowed: secretAllowed, Capabilities: secretCapabilities,
			},
		},
		GatewayDestinationID: gatewayDestinationID,
	}, nil
}
