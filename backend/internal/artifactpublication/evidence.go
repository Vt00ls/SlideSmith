package artifactpublication

// EvidenceAuthorityKind is the closed set of registered upstream authorities
// that may produce evidence consumed by the Artifact Publication authority.
type EvidenceAuthorityKind string

const (
	// AuthorityRuntimeExecution issues Runtime Evidence.
	AuthorityRuntimeExecution EvidenceAuthorityKind = "runtime_execution"
	// AuthorityPlatformValidation issues Platform validation evidence.
	AuthorityPlatformValidation EvidenceAuthorityKind = "platform_validation"
	// AuthorityTaskWorkspaceLifecycle issues C04 commit evidence.
	AuthorityTaskWorkspaceLifecycle EvidenceAuthorityKind = "task_workspace_lifecycle"
	// AuthorityDurableObject issues verified-content capabilities.
	AuthorityDurableObject EvidenceAuthorityKind = "durable_object"
	// AuthorityTaskOrchestration issues publication intents and cancels.
	AuthorityTaskOrchestration EvidenceAuthorityKind = "task_orchestration"
	// AuthorityRecovery is the protected recovery authority for cancels.
	AuthorityRecovery EvidenceAuthorityKind = "protected_recovery"
)

func validEvidenceAuthorityKind(kind EvidenceAuthorityKind) bool {
	switch kind {
	case AuthorityRuntimeExecution, AuthorityPlatformValidation,
		AuthorityTaskWorkspaceLifecycle, AuthorityDurableObject,
		AuthorityTaskOrchestration, AuthorityRecovery:
		return true
	default:
		return false
	}
}

// EvidenceProducer identifies the authority that produced an evidence
// record and the authority generation it was issued under.
type EvidenceProducer struct {
	AuthorityID AuthorityID
	Generation  Generation
}

// RuntimeEvidence is the normalized evidence of one Runtime Run produced by
// the Runtime Execution authority. It binds the same Task, Phase Run,
// Runtime Run, Runtime Binding, terminal outcome, declared output channel,
// canonical output-proposal manifest digest, and opaque proposal reference.
// Raw worker output, Agent Compose results, paths, or process status are
// never evidence.
type RuntimeEvidence struct {
	ID                           EvidenceID
	Digest                       Digest
	Producer                     EvidenceProducer
	PolicyDomainID               PolicyDomainID
	TaskID                       TaskID
	PhaseRunID                   PhaseRunID
	RuntimeRunID                 RuntimeRunID
	RuntimeOperationID           string
	RuntimeBindingDigest         Digest
	Channel                      ChannelKind
	OutputProposalManifestDigest Digest
	ProposalRef                  string
	Outcome                      string
	Generation                   Generation
	Fence                        Fence
	SafetyEpoch                  SafetyEpoch
}

func (e RuntimeEvidence) CanonicalDigest() Digest {
	return canonicalDigest(map[string]any{
		"evidence_id":                     string(e.ID),
		"producer_authority_id":           string(e.Producer.AuthorityID),
		"producer_generation":             uint64(e.Producer.Generation),
		"policy_domain_id":                string(e.PolicyDomainID),
		"task_id":                         string(e.TaskID),
		"phase_run_id":                    string(e.PhaseRunID),
		"runtime_run_id":                  string(e.RuntimeRunID),
		"runtime_operation_id":            e.RuntimeOperationID,
		"runtime_binding_digest":          string(e.RuntimeBindingDigest),
		"channel":                         string(e.Channel),
		"output_proposal_manifest_digest": string(e.OutputProposalManifestDigest),
		"proposal_ref":                    e.ProposalRef,
		"outcome":                         e.Outcome,
		"generation":                      uint64(e.Generation),
		"fence":                           uint64(e.Fence),
		"safety_epoch":                    uint64(e.SafetyEpoch),
	})
}

// ValidationEvidence is the normalized Platform validation evidence produced
// by a registered validator authority. It accepts the exact pinned
// publication contract and binds every required Runtime Evidence plus the
// same output-proposal manifest facts.
type ValidationEvidence struct {
	ID                           EvidenceID
	Digest                       Digest
	Producer                     EvidenceProducer
	PolicyDomainID               PolicyDomainID
	TaskID                       TaskID
	PhaseRunID                   PhaseRunID
	ContractID                   PublicationContractID
	RuntimeEvidenceRefs          []EvidenceRef
	OutputProposalManifestDigest Digest
	Decision                     string
	Generation                   Generation
	Fence                        Fence
	SafetyEpoch                  SafetyEpoch
}

func (e ValidationEvidence) CanonicalDigest() Digest {
	refs := make([]map[string]any, 0, len(e.RuntimeEvidenceRefs))
	for _, ref := range e.RuntimeEvidenceRefs {
		refs = append(refs, ref.canonical())
	}
	return canonicalDigest(map[string]any{
		"evidence_id":                     string(e.ID),
		"producer_authority_id":           string(e.Producer.AuthorityID),
		"producer_generation":             uint64(e.Producer.Generation),
		"policy_domain_id":                string(e.PolicyDomainID),
		"task_id":                         string(e.TaskID),
		"phase_run_id":                    string(e.PhaseRunID),
		"contract_id":                     string(e.ContractID),
		"runtime_evidence_refs":           refs,
		"output_proposal_manifest_digest": string(e.OutputProposalManifestDigest),
		"decision":                        e.Decision,
		"generation":                      uint64(e.Generation),
		"fence":                           uint64(e.Fence),
		"safety_epoch":                    uint64(e.SafetyEpoch),
	})
}

// ValidatedExportEvidence is the manual-edit source binding produced by C04
// reconstruction. It binds the exact source Artifact Version and the exact
// reconstruction evidence so a child cannot be derived from an arbitrary
// version.
type ValidatedExportEvidence struct {
	ID                       string
	Digest                   Digest
	PublicationAuthorityID   AuthorityID
	PolicyDomainID           PolicyDomainID
	TaskID                   TaskID
	TaskWorkspaceID          TaskWorkspaceID
	SourceArtifactVersionID  ArtifactVersionID
	ReconstructionEvidenceID EvidenceID
	RevisionID               RevisionID
	CheckpointID             CheckpointID
	ValidationEvidenceID     EvidenceID
	Generation               Generation
	Fence                    Fence
}

func (e ValidatedExportEvidence) CanonicalDigest() Digest {
	return canonicalDigest(map[string]any{
		"id":                         string(e.ID),
		"publication_authority_id":   string(e.PublicationAuthorityID),
		"policy_domain_id":           string(e.PolicyDomainID),
		"task_id":                    string(e.TaskID),
		"task_workspace_id":          string(e.TaskWorkspaceID),
		"source_artifact_version_id": string(e.SourceArtifactVersionID),
		"reconstruction_evidence_id": string(e.ReconstructionEvidenceID),
		"revision_id":                string(e.RevisionID),
		"checkpoint_id":              string(e.CheckpointID),
		"validation_evidence_id":     string(e.ValidationEvidenceID),
		"generation":                 uint64(e.Generation),
		"fence":                      uint64(e.Fence),
	})
}

// C04CommitEvidence is the normalized Task Workspace Lifecycle commit
// evidence produced by the C04 authority. It binds the same Task and Task
// Workspace, the exact validation evidence, the resulting Revision and
// distinct Checkpoint, the declared-state manifest digest, and the content
// and durability evidence roots.
type C04CommitEvidence struct {
	ID                          EvidenceID
	Digest                      Digest
	Producer                    EvidenceProducer
	PolicyDomainID              PolicyDomainID
	TaskID                      TaskID
	TaskWorkspaceID             TaskWorkspaceID
	RevisionID                  RevisionID
	CheckpointID                CheckpointID
	ValidationEvidenceID        EvidenceID
	ValidationEvidenceDigest    Digest
	DeclaredStateManifestDigest Digest
	ContentEvidenceRoot         string
	DurabilityEvidenceRoot      string
	OperationID                 string
	Generation                  Generation
	Fence                       Fence
	SafetyEpoch                 SafetyEpoch
	ValidatedExportEvidence     *ValidatedExportEvidence
}

func (e C04CommitEvidence) CanonicalDigest() Digest {
	export := any(nil)
	if e.ValidatedExportEvidence != nil {
		export = string(e.ValidatedExportEvidence.CanonicalDigest())
	}
	return canonicalDigest(map[string]any{
		"evidence_id":                    string(e.ID),
		"producer_authority_id":          string(e.Producer.AuthorityID),
		"producer_generation":            uint64(e.Producer.Generation),
		"policy_domain_id":               string(e.PolicyDomainID),
		"task_id":                        string(e.TaskID),
		"task_workspace_id":              string(e.TaskWorkspaceID),
		"revision_id":                    string(e.RevisionID),
		"checkpoint_id":                  string(e.CheckpointID),
		"validation_evidence_id":         string(e.ValidationEvidenceID),
		"validation_evidence_digest":     string(e.ValidationEvidenceDigest),
		"declared_state_manifest_digest": string(e.DeclaredStateManifestDigest),
		"content_evidence_root":          e.ContentEvidenceRoot,
		"durability_evidence_root":       e.DurabilityEvidenceRoot,
		"operation_id":                   e.OperationID,
		"generation":                     uint64(e.Generation),
		"fence":                          uint64(e.Fence),
		"safety_epoch":                   uint64(e.SafetyEpoch),
		"validated_export_evidence":      export,
	})
}

// ContentCapabilityEvidence is the receipt-bound verified-content capability
// issued by the Durable Object authority for one member. A ContentID or
// digest match alone never confers membership, ownership, or authorization;
// the capability must be current in the Durable Object authority registry
// and bind the exact policy domain, purpose, content identity, digest, size,
// write intent, immutable physical generation, verification method, and
// adapter identity.
type ContentCapabilityEvidence struct {
	ID                 ContentCapabilityID
	Digest             Digest
	Producer           EvidenceProducer
	PolicyDomainID     PolicyDomainID
	Purpose            ContentPurpose
	ContentID          ContentID
	MemberSlot         MemberSlotID
	ContentDigest      Digest
	Size               uint64
	WriteIntent        WriteIntent
	PhysicalGeneration uint64
	VerificationMethod VerificationMethod
	AdapterID          AdapterID
	Generation         Generation
	Fence              Fence
	SafetyEpoch        SafetyEpoch
}

func (e ContentCapabilityEvidence) CanonicalDigest() Digest {
	return canonicalDigest(map[string]any{
		"capability_id":         string(e.ID),
		"producer_authority_id": string(e.Producer.AuthorityID),
		"producer_generation":   uint64(e.Producer.Generation),
		"policy_domain_id":      string(e.PolicyDomainID),
		"purpose":               string(e.Purpose),
		"content_id":            string(e.ContentID),
		"member_slot":           string(e.MemberSlot),
		"content_digest":        string(e.ContentDigest),
		"size":                  e.Size,
		"write_intent":          string(e.WriteIntent),
		"physical_generation":   e.PhysicalGeneration,
		"verification_method":   string(e.VerificationMethod),
		"adapter_id":            string(e.AdapterID),
		"generation":            uint64(e.Generation),
		"fence":                 uint64(e.Fence),
		"safety_epoch":          uint64(e.SafetyEpoch),
	})
}
