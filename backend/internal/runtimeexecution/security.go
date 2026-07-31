package runtimeexecution

import (
	"sort"
	"time"
)

type NetworkDestinationID struct{ value string }
type SecretCapabilityID struct{ value string }

func NewNetworkDestinationID(value string) (NetworkDestinationID, error) {
	value, err := newOpaqueReferenceID(value)
	return NetworkDestinationID{value: value}, err
}

func NewSecretCapabilityID(value string) (SecretCapabilityID, error) {
	value, err := newOpaqueReferenceID(value)
	return SecretCapabilityID{value: value}, err
}

func (id NetworkDestinationID) String() string { return id.value }
func (id SecretCapabilityID) String() string   { return id.value }

type SecurityValidationContext struct {
	Now                         time.Time
	Deadline                    time.Time
	RuntimeRunID                RuntimeRunID
	OperationID                 OperationID
	ExecutionNodeID             ExecutionNodeID
	NodeGeneration              NodeGeneration
	NodeAttestationID           NodeAttestationID
	NodeAttestationGeneration   NodeAttestationGeneration
	SandboxLeaseID              SandboxLeaseID
	LeaseGeneration             LeaseGeneration
	LeaseFence                  LeaseFence
	RuntimeFence                RuntimeFence
	SafetyEpoch                 ReleaseSafetyEpoch
	LeaseExpiresAt              time.Time
	AuthorizationExpiresAt      time.Time
	AllowedPlatformImagesDigest Digest
	ExecutorContractDigest      Digest
	CapabilityContractDigest    Digest
	ExecutionPolicyID           ExecutionPolicyID
	NetworkPolicyID             NetworkPolicyID
	SecretPolicyID              SecretPolicyID
	ProviderCapability          ProviderCapability
	GatewayDestinationID        NetworkDestinationID
}

type HostileExecutionPolicy struct {
	ExecutionPolicyID             ExecutionPolicyID
	PolicyDigest                  Digest
	SandboxDriverDigest           Digest
	HostGeneration                uint64
	NodeGeneration                NodeGeneration
	KernelRuntimeDigest           Digest
	ActualImageDigest             Digest
	ActualExecutorDigest          Digest
	ImageAuthorizationDigest      Digest
	ExecutorAuthorizationDigest   Digest
	MountPolicyDigest             Digest
	WritableLocationsDigest       Digest
	CredentialPolicyDigest        Digest
	ResetStateDigest              Digest
	CapabilityCompatibilityDigest Digest
	AttestationID                 NodeAttestationID
	AttestationDigest             Digest
	AttestationGeneration         NodeAttestationGeneration
	AttestationExpiresAt          time.Time
}

type NetworkDestinationKind uint8

const (
	NetworkDestinationPlatformService NetworkDestinationKind = iota + 1
	NetworkDestinationLLMGateway
	NetworkDestinationDirectProvider
)

type NetworkProtocol uint8

const (
	NetworkProtocolTLS NetworkProtocol = iota + 1
	NetworkProtocolTCP
)

type NetworkPurpose uint8

const (
	NetworkPurposeInternalService NetworkPurpose = iota + 1
	NetworkPurposeProviderEgress
)

type NetworkRule struct {
	DestinationKind NetworkDestinationKind
	DestinationID   NetworkDestinationID
	Protocol        NetworkProtocol
	Port            uint16
	Purpose         NetworkPurpose
}

type ExecutionNetworkPolicy struct {
	NetworkPolicyID         NetworkPolicyID
	PolicyDigest            Digest
	Generation              uint64
	RuntimeRunID            RuntimeRunID
	OperationID             OperationID
	SandboxLeaseID          SandboxLeaseID
	LeaseGeneration         LeaseGeneration
	LeaseFence              LeaseFence
	RuntimeFence            RuntimeFence
	ExpiresAt               time.Time
	DefaultDenyEgress       bool
	DefaultDenyIngress      bool
	DNSPinned               bool
	RedirectsDenied         bool
	ProxyBypassDenied       bool
	AlternatePortsDenied    bool
	ResolvedEndpointsDigest Digest
	Rules                   []NetworkRule
}

type SecretClass uint8

const (
	SecretClassEphemeralRuntimeCredential SecretClass = iota + 1
	SecretClassObjectStoreCredential
	SecretClassRegistryCredential
	SecretClassPlatformPostgresCredential
	SecretClassSchedulerCredential
	SecretClassLongLivedProviderCredential
	SecretClassAgentComposeDaemonCredential
	SecretClassHostAdministrativeCredential
)

type SecretUse uint8

const (
	SecretUseLLMGateway SecretUse = iota + 1
	SecretUseToolRuntime
	SecretUseRegistryPull
)

type SecretPurpose struct {
	Class SecretClass
	Use   SecretUse
}

type SecretCapability struct {
	CapabilityID    SecretCapabilityID
	Class           SecretClass
	Use             SecretUse
	RuntimeRunID    RuntimeRunID
	OperationID     OperationID
	ExecutionNodeID ExecutionNodeID
	SandboxLeaseID  SandboxLeaseID
	LeaseGeneration LeaseGeneration
	LeaseFence      LeaseFence
	RuntimeFence    RuntimeFence
	SafetyEpoch     ReleaseSafetyEpoch
	Generation      uint64
	ExpiresAt       time.Time
	ScopeDigest     Digest
	Revoked         bool
}

type ExecutionSecretPolicy struct {
	SecretPolicyID  SecretPolicyID
	PolicyDigest    Digest
	Generation      uint64
	RuntimeRunID    RuntimeRunID
	OperationID     OperationID
	ExecutionNodeID ExecutionNodeID
	SandboxLeaseID  SandboxLeaseID
	LeaseGeneration LeaseGeneration
	LeaseFence      LeaseFence
	RuntimeFence    RuntimeFence
	SafetyEpoch     ReleaseSafetyEpoch
	ExpiresAt       time.Time
	Allowed         []SecretPurpose
	Capabilities    []SecretCapability
}

type ExecutionSecurityContract struct {
	SchemaVersion  SchemaVersion
	EvidenceID     EvidenceID
	EvidenceDigest Digest
	Policy         HostileExecutionPolicy
	Network        ExecutionNetworkPolicy
	Secrets        ExecutionSecretPolicy
}

type ExecutionSecurityAcceptance struct {
	EvidenceID           EvidenceID
	EvidenceDigest       Digest
	ActualImageDigest    Digest
	ActualExecutorDigest Digest
	NetworkRules         []NetworkRule
	SecretCapabilities   []SecretCapability
}

type SecurityValidationCode uint8

const (
	SecurityValidationSchema SecurityValidationCode = iota + 1
	SecurityValidationPolicy
	SecurityValidationNetwork
	SecurityValidationSecret
	SecurityValidationStale
)

type SecurityValidationError struct{ code SecurityValidationCode }

func (failure *SecurityValidationError) Error() string {
	return "runtime execution security contract failed closed validation"
}

func (failure *SecurityValidationError) Code() SecurityValidationCode {
	if failure == nil {
		return SecurityValidationSchema
	}
	return failure.code
}

func securityValidationError(code SecurityValidationCode) *SecurityValidationError {
	return &SecurityValidationError{code: code}
}

func ValidateExecutionSecurity(
	validation SecurityValidationContext,
	contract ExecutionSecurityContract,
) (ExecutionSecurityAcceptance, error) {
	validation.Now = validation.Now.UTC()
	if contract.SchemaVersion.Major() != SchemaV1.Major() || !validOpaqueID(contract.EvidenceID.String()) ||
		contract.EvidenceDigest == (Digest{}) {
		return ExecutionSecurityAcceptance{}, securityValidationError(SecurityValidationSchema)
	}
	policy := contract.Policy
	if policy.ExecutionPolicyID != validation.ExecutionPolicyID || policy.PolicyDigest == (Digest{}) ||
		policy.SandboxDriverDigest == (Digest{}) || policy.HostGeneration == 0 ||
		policy.KernelRuntimeDigest == (Digest{}) || policy.ActualImageDigest == (Digest{}) ||
		policy.ActualExecutorDigest == (Digest{}) || policy.ImageAuthorizationDigest == (Digest{}) ||
		policy.ExecutorAuthorizationDigest == (Digest{}) || policy.MountPolicyDigest == (Digest{}) ||
		policy.WritableLocationsDigest == (Digest{}) || policy.CredentialPolicyDigest == (Digest{}) ||
		policy.ResetStateDigest == (Digest{}) || policy.CapabilityCompatibilityDigest == (Digest{}) ||
		!validOpaqueID(policy.AttestationID.String()) || policy.AttestationDigest == (Digest{}) ||
		policy.AttestationGeneration == 0 || policy.AttestationExpiresAt.IsZero() {
		return ExecutionSecurityAcceptance{}, securityValidationError(SecurityValidationPolicy)
	}
	if policy.NodeGeneration != validation.NodeGeneration ||
		(validation.NodeAttestationID != (NodeAttestationID{}) && policy.AttestationID != validation.NodeAttestationID) ||
		(validation.NodeAttestationGeneration != 0 && policy.AttestationGeneration != validation.NodeAttestationGeneration) ||
		(validation.AllowedPlatformImagesDigest != (Digest{}) &&
			policy.ImageAuthorizationDigest != validation.AllowedPlatformImagesDigest) ||
		(validation.ExecutorContractDigest != (Digest{}) &&
			policy.ExecutorAuthorizationDigest != validation.ExecutorContractDigest) ||
		(validation.CapabilityContractDigest != (Digest{}) &&
			policy.CapabilityCompatibilityDigest != validation.CapabilityContractDigest) ||
		!validation.Now.Before(policy.AttestationExpiresAt) ||
		policy.AttestationExpiresAt.After(validation.LeaseExpiresAt) ||
		policy.AttestationExpiresAt.After(validation.AuthorizationExpiresAt) ||
		policy.AttestationExpiresAt.After(validation.Deadline) {
		return ExecutionSecurityAcceptance{}, securityValidationError(SecurityValidationStale)
	}
	if err := validateExecutionNetworkPolicy(validation, contract.Network); err != nil {
		return ExecutionSecurityAcceptance{}, err
	}
	if err := validateExecutionSecretPolicy(validation, contract.Secrets); err != nil {
		return ExecutionSecurityAcceptance{}, err
	}
	networkRules := append([]NetworkRule(nil), contract.Network.Rules...)
	sort.Slice(networkRules, func(left, right int) bool {
		if networkRules[left].DestinationID != networkRules[right].DestinationID {
			return networkRules[left].DestinationID.String() < networkRules[right].DestinationID.String()
		}
		return networkRules[left].Port < networkRules[right].Port
	})
	return ExecutionSecurityAcceptance{
		EvidenceID: contract.EvidenceID, EvidenceDigest: contract.EvidenceDigest,
		ActualImageDigest: policy.ActualImageDigest, ActualExecutorDigest: policy.ActualExecutorDigest,
		NetworkRules:       networkRules,
		SecretCapabilities: append([]SecretCapability(nil), contract.Secrets.Capabilities...),
	}, nil
}

func validateExecutionNetworkPolicy(
	validation SecurityValidationContext,
	policy ExecutionNetworkPolicy,
) error {
	if policy.NetworkPolicyID != validation.NetworkPolicyID || policy.PolicyDigest == (Digest{}) ||
		policy.Generation == 0 || policy.ResolvedEndpointsDigest == (Digest{}) ||
		!policy.DefaultDenyEgress || !policy.DefaultDenyIngress || !policy.DNSPinned ||
		!policy.RedirectsDenied || !policy.ProxyBypassDenied || !policy.AlternatePortsDenied {
		return securityValidationError(SecurityValidationNetwork)
	}
	if policy.RuntimeRunID != validation.RuntimeRunID || policy.OperationID != validation.OperationID ||
		policy.SandboxLeaseID != validation.SandboxLeaseID || policy.LeaseGeneration != validation.LeaseGeneration ||
		policy.LeaseFence != validation.LeaseFence || policy.RuntimeFence != validation.RuntimeFence ||
		policy.ExpiresAt.IsZero() || !validation.Now.Before(policy.ExpiresAt) ||
		policy.ExpiresAt.After(validation.LeaseExpiresAt) ||
		policy.ExpiresAt.After(validation.AuthorizationExpiresAt) || policy.ExpiresAt.After(validation.Deadline) {
		return securityValidationError(SecurityValidationStale)
	}
	seen := make(map[NetworkDestinationID]struct{}, len(policy.Rules))
	providerGateway := false
	for _, rule := range policy.Rules {
		if !validOpaqueReferenceID(rule.DestinationID.String()) || rule.Protocol != NetworkProtocolTLS || rule.Port != 443 ||
			rule.DestinationKind == NetworkDestinationDirectProvider ||
			rule.DestinationKind < NetworkDestinationPlatformService || rule.DestinationKind > NetworkDestinationDirectProvider ||
			rule.Purpose < NetworkPurposeInternalService || rule.Purpose > NetworkPurposeProviderEgress ||
			rule.DestinationKind == NetworkDestinationLLMGateway && rule.Purpose != NetworkPurposeProviderEgress {
			return securityValidationError(SecurityValidationNetwork)
		}
		if _, exists := seen[rule.DestinationID]; exists {
			return securityValidationError(SecurityValidationNetwork)
		}
		seen[rule.DestinationID] = struct{}{}
		if rule.Purpose == NetworkPurposeProviderEgress {
			if rule.DestinationKind != NetworkDestinationLLMGateway ||
				rule.DestinationID != validation.GatewayDestinationID {
				return securityValidationError(SecurityValidationNetwork)
			}
			providerGateway = true
		}
	}
	if validation.ProviderCapability == ProviderCapabilityRequired && !providerGateway {
		return securityValidationError(SecurityValidationNetwork)
	}
	return nil
}

func validateExecutionSecretPolicy(
	validation SecurityValidationContext,
	policy ExecutionSecretPolicy,
) error {
	if policy.SecretPolicyID != validation.SecretPolicyID || policy.PolicyDigest == (Digest{}) || policy.Generation == 0 {
		return securityValidationError(SecurityValidationSecret)
	}
	if policy.RuntimeRunID != validation.RuntimeRunID || policy.OperationID != validation.OperationID ||
		policy.ExecutionNodeID != validation.ExecutionNodeID || policy.SandboxLeaseID != validation.SandboxLeaseID ||
		policy.LeaseGeneration != validation.LeaseGeneration || policy.LeaseFence != validation.LeaseFence ||
		policy.RuntimeFence != validation.RuntimeFence || policy.SafetyEpoch != validation.SafetyEpoch {
		return securityValidationError(SecurityValidationStale)
	}
	if policy.ExpiresAt.IsZero() || !validation.Now.Before(policy.ExpiresAt) ||
		policy.ExpiresAt.After(validation.LeaseExpiresAt) ||
		policy.ExpiresAt.After(validation.AuthorizationExpiresAt) || policy.ExpiresAt.After(validation.Deadline) {
		return securityValidationError(SecurityValidationSecret)
	}
	allowed := make(map[SecretPurpose]struct{}, len(policy.Allowed))
	for _, purpose := range policy.Allowed {
		if !sandboxSecretPurposeAllowed(validation, purpose) {
			return securityValidationError(SecurityValidationSecret)
		}
		allowed[purpose] = struct{}{}
	}
	seen := make(map[SecretCapabilityID]struct{}, len(policy.Capabilities))
	for _, capability := range policy.Capabilities {
		if forbiddenSecretClass(capability.Class) || capability.Class != SecretClassEphemeralRuntimeCredential ||
			!validOpaqueReferenceID(capability.CapabilityID.String()) || capability.Generation == 0 ||
			capability.ScopeDigest == (Digest{}) || capability.Revoked {
			return securityValidationError(SecurityValidationSecret)
		}
		if _, exists := allowed[SecretPurpose{Class: capability.Class, Use: capability.Use}]; !exists {
			return securityValidationError(SecurityValidationSecret)
		}
		if _, exists := seen[capability.CapabilityID]; exists {
			return securityValidationError(SecurityValidationSecret)
		}
		seen[capability.CapabilityID] = struct{}{}
		if err := ValidateSecretCapabilityUse(validation, capability, capability.Use, false); err != nil {
			return err
		}
	}
	return nil
}

func ValidateSecretCapabilityUse(
	validation SecurityValidationContext,
	capability SecretCapability,
	use SecretUse,
	revoked bool,
) error {
	if revoked || capability.Revoked ||
		!validOpaqueReferenceID(capability.CapabilityID.String()) || capability.Generation == 0 ||
		capability.ScopeDigest == (Digest{}) ||
		!sandboxSecretPurposeAllowed(validation, SecretPurpose{Class: capability.Class, Use: capability.Use}) ||
		capability.Use != use || capability.RuntimeRunID != validation.RuntimeRunID ||
		capability.OperationID != validation.OperationID || capability.ExecutionNodeID != validation.ExecutionNodeID ||
		capability.SandboxLeaseID != validation.SandboxLeaseID || capability.LeaseGeneration != validation.LeaseGeneration ||
		capability.LeaseFence != validation.LeaseFence || capability.RuntimeFence != validation.RuntimeFence ||
		capability.SafetyEpoch != validation.SafetyEpoch || !validation.Now.UTC().Before(capability.ExpiresAt) ||
		capability.ExpiresAt.After(validation.LeaseExpiresAt) ||
		capability.ExpiresAt.After(validation.AuthorizationExpiresAt) || capability.ExpiresAt.After(validation.Deadline) {
		return securityValidationError(SecurityValidationSecret)
	}
	return nil
}

func sandboxSecretPurposeAllowed(validation SecurityValidationContext, purpose SecretPurpose) bool {
	if purpose.Class != SecretClassEphemeralRuntimeCredential || forbiddenSecretClass(purpose.Class) {
		return false
	}
	switch purpose.Use {
	case SecretUseLLMGateway:
		return validation.ProviderCapability == ProviderCapabilityRequired &&
			validOpaqueReferenceID(validation.GatewayDestinationID.String())
	case SecretUseToolRuntime:
		return true
	default:
		return false
	}
}

func forbiddenSecretClass(class SecretClass) bool {
	return class >= SecretClassObjectStoreCredential && class <= SecretClassHostAdministrativeCredential
}
