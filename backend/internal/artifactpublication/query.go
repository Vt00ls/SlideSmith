package artifactpublication

import "context"

// Query is the pure read-only seam of the Artifact Publication authority.
// It never accepts evidence, never creates Durable Object handles, never
// writes audit or outbox, never cleans residue, and never changes current
// version. Unknown or cross-scope targets resolve to the same non-enumerating
// not-found error.
func (m *inMemory) Query(ctx context.Context, query PublicationQuery) (PublicationView, error) {
	if !validQueryKind(query.Kind) || query.PolicyDomainID == "" || query.TaskID == "" {
		return PublicationView{}, &Error{Code: ErrorInvalidIntent}
	}
	m.persistence.mu.Lock()
	defer m.persistence.mu.Unlock()

	switch query.Kind {
	case QueryOperation:
		return m.queryOperation(query)
	case QueryCandidate:
		return m.queryCandidate(query)
	case QueryTaskStream:
		return m.queryTaskStream(query)
	default:
		return PublicationView{}, &Error{Code: ErrorInvalidIntent}
	}
}

func (m *inMemory) queryOperation(query PublicationQuery) (PublicationView, error) {
	scope := operationScope{policyDomainID: query.PolicyDomainID, taskID: query.TaskID, operationID: query.OperationID}
	record := m.persistence.operations[scope]
	if record == nil {
		// Non-enumerating: the same error is returned whether the target
		// exists in another scope or does not exist at all.
		return PublicationView{}, &Error{Code: ErrorNotFound}
	}
	view := PublicationView{
		Kind: QueryOperation, PolicyDomainID: query.PolicyDomainID, TaskID: query.TaskID,
		OperationID: record.operationID, State: record.state,
		StreamRevision: record.streamRevision, OccurredAt: record.occurredAt,
		ResidueRelease: record.residue != nil,
	}
	if record.candidate != nil {
		view.ArtifactVersionID = record.candidate.versionID
		view.ManifestDigest = record.candidate.manifestDigest
		view.LineageDigest = record.candidate.lineageDigest
		view.PublicationKind = record.candidate.kind
		view.Parent = record.candidate.parent
		view.ContractID = record.candidate.contractID
		for _, member := range record.candidate.members {
			view.Members = append(view.Members, ArtifactMemberView{
				ArtifactID: member.artifactID, Kind: member.kind, LogicalName: member.logicalName,
				MediaType: member.mediaType, Size: member.size, ContentDigest: member.contentDigest,
			})
		}
	}
	view.Verification = m0Verification(record)
	return view, nil
}

func (m *inMemory) queryCandidate(query PublicationQuery) (PublicationView, error) {
	scope, ok := m.persistence.versionIndex[query.ArtifactVersionID]
	if !ok {
		return PublicationView{}, &Error{Code: ErrorNotFound}
	}
	if scope.policyDomainID != query.PolicyDomainID || scope.taskID != query.TaskID {
		// Cross-workspace identity is never disclosed.
		return PublicationView{}, &Error{Code: ErrorNotFound}
	}
	record := m.persistence.operations[scope]
	if record == nil {
		return PublicationView{}, &Error{Code: ErrorNotFound}
	}
	// Ordinary queries expose immutable publication-safe metadata only for
	// verified candidates; prepared, verifying, rejected, cancelled, and
	// residue are visible only through the exact operation query.
	if record.state != OperationVerified {
		return PublicationView{}, &Error{Code: ErrorNotFound}
	}
	view := PublicationView{
		Kind: QueryCandidate, PolicyDomainID: query.PolicyDomainID, TaskID: query.TaskID,
		OperationID: record.operationID, State: record.state,
		StreamRevision: record.streamRevision, OccurredAt: record.occurredAt,
		ResidueRelease: record.residue != nil,
	}
	if record.candidate != nil {
		view.ArtifactVersionID = record.candidate.versionID
		view.ManifestDigest = record.candidate.manifestDigest
		view.LineageDigest = record.candidate.lineageDigest
		view.PublicationKind = record.candidate.kind
		view.Parent = record.candidate.parent
		view.ContractID = record.candidate.contractID
		for _, member := range record.candidate.members {
			view.Members = append(view.Members, ArtifactMemberView{
				ArtifactID: member.artifactID, Kind: member.kind, LogicalName: member.logicalName,
				MediaType: member.mediaType, Size: member.size, ContentDigest: member.contentDigest,
			})
		}
	}
	view.Verification = m0Verification(record)
	return view, nil
}

func (m *inMemory) queryTaskStream(query PublicationQuery) (PublicationView, error) {
	stream := m.persistence.streams[taskScope{policyDomainID: query.PolicyDomainID, taskID: query.TaskID}]
	if stream == nil {
		return PublicationView{}, &Error{Code: ErrorNotFound}
	}
	return PublicationView{
		Kind: QueryTaskStream, PolicyDomainID: query.PolicyDomainID, TaskID: query.TaskID,
		StreamRevision: stream.revision, CurrentHead: stream.currentHead,
	}, nil
}
