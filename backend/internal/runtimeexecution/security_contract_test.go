package runtimeexecution

import (
	"testing"
	"time"
)

func TestHostileExecutionSecurityContractFailsClosedAcrossPolicyNetworkAndSecrets(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 30, 10, 0, 0, 0, time.UTC)
	context := SecurityValidationContext{
		Now: now, Deadline: now.Add(20 * time.Minute),
		RuntimeRunID: mustRuntimeRunID(t, "security-runtime"), OperationID: mustOperationID(t, "security-operation"),
		ExecutionNodeID: startNodeID(t, "security-node"), NodeGeneration: 4,
		SandboxLeaseID: SandboxLeaseID{value: "security-lease"}, LeaseGeneration: 5, LeaseFence: 6,
		RuntimeFence: 7, SafetyEpoch: 8, LeaseExpiresAt: now.Add(10 * time.Minute),
		AuthorizationExpiresAt: now.Add(8 * time.Minute),
		ExecutionPolicyID:      mustExecutionPolicyID(t, "security-policy"),
		NetworkPolicyID:        mustNetworkPolicyID(t, "security-network"), SecretPolicyID: mustSecretPolicyID(t, "security-secret"),
		ProviderCapability:   ProviderCapabilityRequired,
		GatewayDestinationID: mustNetworkDestinationID(t, "llm-gateway"),
	}
	contract := validExecutionSecurityContract(t, context)
	accepted, err := ValidateExecutionSecurity(context, contract)
	if err != nil {
		t.Fatalf("valid security contract: %v", err)
	}
	if accepted.ActualImageDigest == (Digest{}) || accepted.ActualExecutorDigest == (Digest{}) ||
		len(accepted.SecretCapabilities) != 1 || len(accepted.NetworkRules) != 1 {
		t.Fatalf("accepted security facts = %+v", accepted)
	}

	tests := []struct {
		name string
		edit func(*ExecutionSecurityContract)
		code SecurityValidationCode
	}{
		{name: "missing sandbox driver proof", edit: func(value *ExecutionSecurityContract) { value.Policy.SandboxDriverDigest = Digest{} }, code: SecurityValidationPolicy},
		{name: "missing kernel proof", edit: func(value *ExecutionSecurityContract) { value.Policy.KernelRuntimeDigest = Digest{} }, code: SecurityValidationPolicy},
		{name: "wrong node generation", edit: func(value *ExecutionSecurityContract) { value.Policy.NodeGeneration++ }, code: SecurityValidationStale},
		{name: "expired attestation", edit: func(value *ExecutionSecurityContract) { value.Policy.AttestationExpiresAt = now }, code: SecurityValidationStale},
		{name: "attestation outlives lease authorization", edit: func(value *ExecutionSecurityContract) {
			value.Policy.AttestationExpiresAt = context.AuthorizationExpiresAt.Add(time.Second)
		}, code: SecurityValidationStale},
		{name: "egress not default deny", edit: func(value *ExecutionSecurityContract) { value.Network.DefaultDenyEgress = false }, code: SecurityValidationNetwork},
		{name: "ingress not default deny", edit: func(value *ExecutionSecurityContract) { value.Network.DefaultDenyIngress = false }, code: SecurityValidationNetwork},
		{name: "dns bypass", edit: func(value *ExecutionSecurityContract) { value.Network.DNSPinned = false }, code: SecurityValidationNetwork},
		{name: "redirect bypass", edit: func(value *ExecutionSecurityContract) { value.Network.RedirectsDenied = false }, code: SecurityValidationNetwork},
		{name: "proxy bypass", edit: func(value *ExecutionSecurityContract) { value.Network.ProxyBypassDenied = false }, code: SecurityValidationNetwork},
		{name: "alternate port bypass", edit: func(value *ExecutionSecurityContract) { value.Network.AlternatePortsDenied = false }, code: SecurityValidationNetwork},
		{name: "direct provider endpoint", edit: func(value *ExecutionSecurityContract) {
			value.Network.Rules[0].DestinationKind = NetworkDestinationDirectProvider
		}, code: SecurityValidationNetwork},
		{name: "provider egress not gateway", edit: func(value *ExecutionSecurityContract) {
			value.Network.Rules[0].DestinationID = mustNetworkDestinationID(t, "other-egress")
		}, code: SecurityValidationNetwork},
		{name: "stale network fence", edit: func(value *ExecutionSecurityContract) { value.Network.LeaseFence++ }, code: SecurityValidationStale},
		{name: "network grant outlives lease authorization", edit: func(value *ExecutionSecurityContract) {
			value.Network.ExpiresAt = context.AuthorizationExpiresAt.Add(time.Second)
		}, code: SecurityValidationStale},
		{name: "expired secret", edit: func(value *ExecutionSecurityContract) { value.Secrets.Capabilities[0].ExpiresAt = now }, code: SecurityValidationSecret},
		{name: "secret policy outlives lease authorization", edit: func(value *ExecutionSecurityContract) {
			value.Secrets.ExpiresAt = context.AuthorizationExpiresAt.Add(time.Second)
		}, code: SecurityValidationSecret},
		{name: "secret capability outlives lease authorization", edit: func(value *ExecutionSecurityContract) {
			value.Secrets.Capabilities[0].ExpiresAt = context.AuthorizationExpiresAt.Add(time.Second)
		}, code: SecurityValidationSecret},
		{name: "wrong secret purpose", edit: func(value *ExecutionSecurityContract) { value.Secrets.Capabilities[0].Use = SecretUseRegistryPull }, code: SecurityValidationSecret},
		{name: "registry pull purpose disguised as ephemeral", edit: func(value *ExecutionSecurityContract) {
			value.Secrets.Allowed[0].Use = SecretUseRegistryPull
			value.Secrets.Capabilities[0].Use = SecretUseRegistryPull
		}, code: SecurityValidationSecret},
		{name: "secret scope expansion", edit: func(value *ExecutionSecurityContract) { value.Secrets.Allowed = nil }, code: SecurityValidationSecret},
		{name: "revoked secret", edit: func(value *ExecutionSecurityContract) { value.Secrets.Capabilities[0].Revoked = true }, code: SecurityValidationSecret},
		{name: "Platform PostgreSQL credential", edit: func(value *ExecutionSecurityContract) {
			value.Secrets.Capabilities[0].Class = SecretClassPlatformPostgresCredential
		}, code: SecurityValidationSecret},
		{name: "object store credential", edit: func(value *ExecutionSecurityContract) {
			value.Secrets.Capabilities[0].Class = SecretClassObjectStoreCredential
		}, code: SecurityValidationSecret},
		{name: "registry credential", edit: func(value *ExecutionSecurityContract) {
			value.Secrets.Capabilities[0].Class = SecretClassRegistryCredential
		}, code: SecurityValidationSecret},
		{name: "Scheduler credential", edit: func(value *ExecutionSecurityContract) {
			value.Secrets.Capabilities[0].Class = SecretClassSchedulerCredential
		}, code: SecurityValidationSecret},
		{name: "long-lived provider credential", edit: func(value *ExecutionSecurityContract) {
			value.Secrets.Capabilities[0].Class = SecretClassLongLivedProviderCredential
		}, code: SecurityValidationSecret},
		{name: "Agent Compose daemon credential", edit: func(value *ExecutionSecurityContract) {
			value.Secrets.Capabilities[0].Class = SecretClassAgentComposeDaemonCredential
		}, code: SecurityValidationSecret},
		{name: "host administrative credential", edit: func(value *ExecutionSecurityContract) {
			value.Secrets.Capabilities[0].Class = SecretClassHostAdministrativeCredential
		}, code: SecurityValidationSecret},
		{name: "unknown schema", edit: func(value *ExecutionSecurityContract) { value.SchemaVersion = NewSchemaVersion(2, 0) }, code: SecurityValidationSchema},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := cloneExecutionSecurityContract(contract)
			test.edit(&changed)
			_, err := ValidateExecutionSecurity(context, changed)
			failure, ok := err.(*SecurityValidationError)
			if !ok || failure.Code() != test.code {
				t.Fatalf("error=%T %v, want code %v", err, err, test.code)
			}
		})
	}
	t.Run("LLM Gateway secret on non-provider Runtime", func(t *testing.T) {
		nonProvider := context
		nonProvider.ProviderCapability = ProviderCapabilityNone
		nonProvider.GatewayDestinationID = NetworkDestinationID{}
		changed := cloneExecutionSecurityContract(contract)
		changed.Network.Rules = nil
		_, err := ValidateExecutionSecurity(nonProvider, changed)
		failure, ok := err.(*SecurityValidationError)
		if !ok || failure.Code() != SecurityValidationSecret {
			t.Fatalf("error=%T %v, want secret denial", err, err)
		}
	})
	t.Run("LLM Gateway cannot masquerade as an internal service", func(t *testing.T) {
		nonProvider := context
		nonProvider.ProviderCapability = ProviderCapabilityNone
		nonProvider.GatewayDestinationID = NetworkDestinationID{}
		changed := cloneExecutionSecurityContract(contract)
		changed.Network.Rules[0].Purpose = NetworkPurposeInternalService
		changed.Secrets.Allowed = nil
		changed.Secrets.Capabilities = nil
		_, err := ValidateExecutionSecurity(nonProvider, changed)
		failure, ok := err.(*SecurityValidationError)
		if !ok || failure.Code() != SecurityValidationNetwork {
			t.Fatalf("error=%T %v, want network purpose denial", err, err)
		}
	})
}

func TestSecretCapabilityUseIsBoundToCurrentPurposeExpiryFenceAndRevocation(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.July, 30, 10, 30, 0, 0, time.UTC)
	validation := SecurityValidationContext{
		Now: now, Deadline: now.Add(20 * time.Minute), RuntimeRunID: mustRuntimeRunID(t, "secret-use-runtime"),
		OperationID: mustOperationID(t, "secret-use-operation"), ExecutionNodeID: startNodeID(t, "secret-use-node"),
		NodeGeneration: 1, SandboxLeaseID: SandboxLeaseID{value: "secret-use-lease"},
		LeaseGeneration: 2, LeaseFence: 3, RuntimeFence: 4, SafetyEpoch: 5, LeaseExpiresAt: now.Add(10 * time.Minute),
		AuthorizationExpiresAt: now.Add(8 * time.Minute),
		ExecutionPolicyID:      mustExecutionPolicyID(t, "secret-use-policy"),
		NetworkPolicyID:        mustNetworkPolicyID(t, "secret-use-network"), SecretPolicyID: mustSecretPolicyID(t, "secret-use-secret"),
		ProviderCapability: ProviderCapabilityRequired, GatewayDestinationID: mustNetworkDestinationID(t, "secret-use-gateway"),
	}
	contract := validExecutionSecurityContract(t, validation)
	capability := contract.Secrets.Capabilities[0]
	if err := ValidateSecretCapabilityUse(validation, capability, SecretUseLLMGateway, false); err != nil {
		t.Fatalf("current secret use: %v", err)
	}
	stale := validation
	stale.LeaseFence++
	if err := ValidateSecretCapabilityUse(stale, capability, SecretUseLLMGateway, false); err == nil {
		t.Fatal("stale fence authorized secret use")
	}
	if err := ValidateSecretCapabilityUse(validation, capability, SecretUseToolRuntime, false); err == nil {
		t.Fatal("wrong purpose authorized secret use")
	}
	if err := ValidateSecretCapabilityUse(validation, capability, SecretUseLLMGateway, true); err == nil {
		t.Fatal("revocation did not fence secret use")
	}
	registryPull := capability
	registryPull.Use = SecretUseRegistryPull
	if err := ValidateSecretCapabilityUse(validation, registryPull, SecretUseRegistryPull, false); err == nil {
		t.Fatal("sandbox received a registry-pull secret capability")
	}
}

func TestPrivateSecurityReferencesRejectNetworkAndSecretLocatorShapes(t *testing.T) {
	t.Parallel()
	if _, err := NewNetworkDestinationID("https://api.provider.example/v1"); err == nil {
		t.Fatal("network endpoint locator was accepted as an opaque destination reference")
	}
	if _, err := NewSecretCapabilityID("vault://platform/runtime-secret"); err == nil {
		t.Fatal("secret locator was accepted as an opaque broker capability reference")
	}
}

func validExecutionSecurityContract(
	t *testing.T,
	validation SecurityValidationContext,
) ExecutionSecurityContract {
	t.Helper()
	capabilityID, err := NewSecretCapabilityID("secret-capability")
	if err != nil {
		t.Fatal(err)
	}
	evidenceID := mustEvidenceID(t, "security-evidence")
	expiresAt := validation.Now.Add(5 * time.Minute)
	return ExecutionSecurityContract{
		SchemaVersion: SchemaV1, EvidenceID: evidenceID, EvidenceDigest: digest(201),
		Policy: HostileExecutionPolicy{
			ExecutionPolicyID: validation.ExecutionPolicyID, PolicyDigest: digest(202),
			SandboxDriverDigest: digest(203), HostGeneration: 3, NodeGeneration: validation.NodeGeneration,
			KernelRuntimeDigest: digest(204), ActualImageDigest: digest(205), ActualExecutorDigest: digest(206),
			ImageAuthorizationDigest: digest(207), ExecutorAuthorizationDigest: digest(208),
			MountPolicyDigest: digest(209), WritableLocationsDigest: digest(210),
			CredentialPolicyDigest: digest(211), ResetStateDigest: digest(212),
			CapabilityCompatibilityDigest: digest(213), AttestationID: mustNodeAttestationID(t, "security-attestation"),
			AttestationDigest: digest(214), AttestationGeneration: 2, AttestationExpiresAt: expiresAt,
		},
		Network: ExecutionNetworkPolicy{
			NetworkPolicyID: validation.NetworkPolicyID, PolicyDigest: digest(215), Generation: 2,
			RuntimeRunID: validation.RuntimeRunID, OperationID: validation.OperationID,
			SandboxLeaseID: validation.SandboxLeaseID, LeaseGeneration: validation.LeaseGeneration,
			LeaseFence: validation.LeaseFence, RuntimeFence: validation.RuntimeFence, ExpiresAt: expiresAt,
			DefaultDenyEgress: true, DefaultDenyIngress: true, DNSPinned: true,
			RedirectsDenied: true, ProxyBypassDenied: true, AlternatePortsDenied: true,
			ResolvedEndpointsDigest: digest(216),
			Rules: []NetworkRule{{
				DestinationKind: NetworkDestinationLLMGateway, DestinationID: validation.GatewayDestinationID,
				Protocol: NetworkProtocolTLS, Port: 443, Purpose: NetworkPurposeProviderEgress,
			}},
		},
		Secrets: ExecutionSecretPolicy{
			SecretPolicyID: validation.SecretPolicyID, PolicyDigest: digest(217), Generation: 2,
			RuntimeRunID: validation.RuntimeRunID, OperationID: validation.OperationID,
			ExecutionNodeID: validation.ExecutionNodeID, SandboxLeaseID: validation.SandboxLeaseID,
			LeaseGeneration: validation.LeaseGeneration, LeaseFence: validation.LeaseFence,
			RuntimeFence: validation.RuntimeFence, SafetyEpoch: validation.SafetyEpoch, ExpiresAt: expiresAt,
			Allowed: []SecretPurpose{{Class: SecretClassEphemeralRuntimeCredential, Use: SecretUseLLMGateway}},
			Capabilities: []SecretCapability{{
				CapabilityID: capabilityID, Class: SecretClassEphemeralRuntimeCredential, Use: SecretUseLLMGateway,
				RuntimeRunID: validation.RuntimeRunID, OperationID: validation.OperationID,
				ExecutionNodeID: validation.ExecutionNodeID, SandboxLeaseID: validation.SandboxLeaseID,
				LeaseGeneration: validation.LeaseGeneration, LeaseFence: validation.LeaseFence,
				RuntimeFence: validation.RuntimeFence, SafetyEpoch: validation.SafetyEpoch,
				Generation: 2, ExpiresAt: expiresAt, ScopeDigest: digest(218),
			}},
		},
	}
}

func cloneExecutionSecurityContract(value ExecutionSecurityContract) ExecutionSecurityContract {
	value.Network.Rules = append([]NetworkRule(nil), value.Network.Rules...)
	value.Secrets.Allowed = append([]SecretPurpose(nil), value.Secrets.Allowed...)
	value.Secrets.Capabilities = append([]SecretCapability(nil), value.Secrets.Capabilities...)
	return value
}

func mustNetworkDestinationID(t *testing.T, value string) NetworkDestinationID {
	t.Helper()
	id, err := NewNetworkDestinationID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustNodeAttestationID(t *testing.T, value string) NodeAttestationID {
	t.Helper()
	id, err := NewNodeAttestationID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
