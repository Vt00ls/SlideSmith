package runtimeexecution

import (
	"testing"
	"time"
)

func TestCleanupResolutionProofRequiresClassSpecificFacts(t *testing.T) {
	verifiedAt := time.Date(2026, 7, 28, 13, 0, 0, 0, time.UTC)
	base := cleanupResolutionProofState{
		ProofID: "cleanup-resolution-proof-contract", SchemaVersion: SchemaV1,
		IntegrityVersion: postgresCleanupResolutionProofIntegrityVersion,
		OwningModule:     postgresCleanupOwnerModule,
		DebtID:           "cleanup-debt-proof-contract", RuntimeRunID: "runtime-run-proof-contract",
		ResourceClass: cleanupResourceContainment, ResourceIdentityDigest: digest(201).String(),
		ResourceGeneration: 7, ResourceFence: 9,
		EvidenceSchemaVersion: SchemaV1, EvidenceRootID: "evidence-root-proof-contract",
		EvidenceRootDigest: digest(202).String(), ObservedAt: formatCleanupTime(verifiedAt),
		RecordedAt: formatCleanupTime(verifiedAt), SourceClockID: postgresMandatoryAuditSourceClock,
	}

	valid := map[string]cleanupResolutionProofState{
		"reclaimed": func() cleanupResolutionProofState {
			state := base
			state.ResolutionClass = cleanupResolutionReclaimed
			state.ResolutionReason = cleanupResolutionCleanupProven
			state.Disposition = cleanupProofDeletionOrReset
			state.DeletionOrResetProven = true
			state.ReferencesClear = true
			state.ContainmentClear = true
			return state
		}(),
		"already absent": func() cleanupResolutionProofState {
			state := base
			state.ResolutionClass = cleanupResolutionAlreadyAbsent
			state.ResolutionReason = cleanupResolutionExactGenerationAbsent
			state.Disposition = cleanupProofExactGenerationAbsent
			state.ExactGenerationAbsent = true
			state.ReferencesClear = true
			state.ContainmentClear = true
			return state
		}(),
		"retained by authority": func() cleanupResolutionProofState {
			state := base
			state.ResolutionClass = cleanupResolutionRetainedByAuthority
			state.ResolutionReason = cleanupResolutionCurrentAuthorityRetention
			state.Disposition = cleanupProofRetainedByAuthority
			state.RetainingAuthorityFactRoot = digest(203).String()
			return state
		}(),
	}
	for name, state := range valid {
		t.Run(name, func(t *testing.T) {
			if _, _, err := encodeCleanupResolutionProof(state); err != nil {
				t.Fatalf("valid proof rejected: %v", err)
			}
		})
	}

	invalid := map[string]cleanupResolutionProofState{
		"absence is not exact": func() cleanupResolutionProofState {
			state := valid["already absent"]
			state.ExactGenerationAbsent = false
			return state
		}(),
		"references remain": func() cleanupResolutionProofState {
			state := valid["already absent"]
			state.ReferencesClear = false
			return state
		}(),
		"containment remains": func() cleanupResolutionProofState {
			state := valid["already absent"]
			state.ContainmentClear = false
			return state
		}(),
		"reclamation is unproven": func() cleanupResolutionProofState {
			state := valid["reclaimed"]
			state.DeletionOrResetProven = false
			return state
		}(),
		"retention authority is absent": func() cleanupResolutionProofState {
			state := valid["retained by authority"]
			state.RetainingAuthorityFactRoot = ""
			return state
		}(),
		"administrator exception is unsupported": func() cleanupResolutionProofState {
			state := base
			state.ResolutionClass = cleanupResolutionAcceptedException
			state.ResolutionReason = cleanupResolutionAdministratorException
			return state
		}(),
	}
	for name, state := range invalid {
		t.Run(name, func(t *testing.T) {
			if _, _, err := encodeCleanupResolutionProof(state); err == nil {
				t.Fatal("invalid proof was accepted")
			}
		})
	}
}
