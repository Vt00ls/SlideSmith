package taskworkspace

import "sort"

type (
	CheckpointID       string
	StateMemberID      string
	ContentID          string
	EvidenceID         string
	EvidenceRoot       string
	ValidationDecision string
	DurabilityDecision string
)

const (
	ValidationAccepted ValidationDecision = "accepted"
	DurabilityVerified DurabilityDecision = "verified"
)

type DurabilityEvidence struct {
	ID            EvidenceID
	Digest        Digest
	ContentID     ContentID
	ContentDigest Digest
	Size          uint64
	Decision      DurabilityDecision
}

func (e DurabilityEvidence) CanonicalDigest() Digest {
	return canonicalDigest(struct {
		ID            EvidenceID
		ContentID     ContentID
		ContentDigest Digest
		Size          uint64
		Decision      DurabilityDecision
	}{
		ID:            e.ID,
		ContentID:     e.ContentID,
		ContentDigest: e.ContentDigest,
		Size:          e.Size,
		Decision:      e.Decision,
	})
}

type DeclaredStateMember struct {
	ID                 StateMemberID
	ContentID          ContentID
	ContentDigest      Digest
	Size               uint64
	DurabilityEvidence DurabilityEvidence
}

type DeclaredStateManifest struct {
	Digest  Digest
	Members []DeclaredStateMember
}

func (m DeclaredStateManifest) CanonicalDigest() Digest {
	return canonicalDigest(m.canonicalValue())
}

func (m DeclaredStateManifest) canonicalValue() declaredStateManifestCanonical {
	members := append([]DeclaredStateMember(nil), m.Members...)
	sort.Slice(members, func(i, j int) bool {
		return members[i].ID < members[j].ID
	})
	return declaredStateManifestCanonical{Members: members}
}

type declaredStateManifestCanonical struct {
	Members []DeclaredStateMember
}

type ValidationAuthorityID string

type ValidationEvidence struct {
	ID                    EvidenceID
	Digest                Digest
	ValidationAuthorityID ValidationAuthorityID
	PolicyDomainID        PolicyDomainID
	TaskID                TaskID
	TaskWorkspaceID       TaskWorkspaceID
	RuntimeViewID         RuntimeViewID
	BaseRevisionID        RevisionID
	PhaseRunID            PhaseRunID
	RuntimeRunID          RuntimeRunID
	ManifestDigest        Digest
	Generation            Generation
	Fence                 Fence
	Decision              ValidationDecision
}

func (e ValidationEvidence) CanonicalDigest() Digest {
	return canonicalDigest(struct {
		ID                    EvidenceID
		ValidationAuthorityID ValidationAuthorityID
		PolicyDomainID        PolicyDomainID
		TaskID                TaskID
		TaskWorkspaceID       TaskWorkspaceID
		RuntimeViewID         RuntimeViewID
		BaseRevisionID        RevisionID
		PhaseRunID            PhaseRunID
		RuntimeRunID          RuntimeRunID
		ManifestDigest        Digest
		Generation            Generation
		Fence                 Fence
		Decision              ValidationDecision
	}{
		ID:                    e.ID,
		ValidationAuthorityID: e.ValidationAuthorityID,
		PolicyDomainID:        e.PolicyDomainID,
		TaskID:                e.TaskID,
		TaskWorkspaceID:       e.TaskWorkspaceID,
		RuntimeViewID:         e.RuntimeViewID,
		BaseRevisionID:        e.BaseRevisionID,
		PhaseRunID:            e.PhaseRunID,
		RuntimeRunID:          e.RuntimeRunID,
		ManifestDigest:        e.ManifestDigest,
		Generation:            e.Generation,
		Fence:                 e.Fence,
		Decision:              e.Decision,
	})
}

type CommitRuntimeViewRequest struct {
	PolicyDomainID          PolicyDomainID
	TaskID                  TaskID
	TaskWorkspaceID         TaskWorkspaceID
	RuntimeViewID           RuntimeViewID
	BaseRevisionID          RevisionID
	ExpectedCurrentRevision RevisionID
	Generation              Generation
	Fence                   Fence
	ValidationEvidence      ValidationEvidence
	DeclaredStateManifest   DeclaredStateManifest
	Operation               Operation
}

func (r CommitRuntimeViewRequest) CanonicalRequestDigest() Digest {
	return canonicalDigest(struct {
		Kind                    string
		PolicyDomainID          PolicyDomainID
		TaskID                  TaskID
		TaskWorkspaceID         TaskWorkspaceID
		RuntimeViewID           RuntimeViewID
		BaseRevisionID          RevisionID
		ExpectedCurrentRevision RevisionID
		Generation              Generation
		Fence                   Fence
		ValidationEvidence      ValidationEvidence
		DeclaredStateManifest   declaredStateManifestCanonical
		OperationID             OperationID
	}{
		Kind:                    "commit_runtime_view",
		PolicyDomainID:          r.PolicyDomainID,
		TaskID:                  r.TaskID,
		TaskWorkspaceID:         r.TaskWorkspaceID,
		RuntimeViewID:           r.RuntimeViewID,
		BaseRevisionID:          r.BaseRevisionID,
		ExpectedCurrentRevision: r.ExpectedCurrentRevision,
		Generation:              r.Generation,
		Fence:                   r.Fence,
		ValidationEvidence:      r.ValidationEvidence,
		DeclaredStateManifest:   r.DeclaredStateManifest.canonicalValue(),
		OperationID:             r.Operation.ID,
	})
}

type CommitRuntimeViewResult struct {
	TaskWorkspaceID          TaskWorkspaceID
	RevisionID               RevisionID
	CheckpointID             CheckpointID
	BaseRevisionID           RevisionID
	PredecessorRevisionID    RevisionID
	ManifestDigest           Digest
	ValidationEvidenceID     EvidenceID
	ValidationEvidenceDigest Digest
	ContentEvidenceRoot      EvidenceRoot
	DurabilityEvidenceRoot   EvidenceRoot
	Generation               Generation
	Fence                    Fence
	Operation                Operation
}
