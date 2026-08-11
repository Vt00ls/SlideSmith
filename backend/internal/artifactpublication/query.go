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
	case QueryExactVersion:
		return m.queryExactVersion(query)
	case QueryExactMember:
		return m.queryExactMember(query)
	case QueryVersionHistory:
		return m.queryVersionHistory(query)
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
	view.ActivationEvidence = cloneEvidence(record.activationEvidence)
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

// queryExactVersion resolves one activated Artifact Version by identity.
// Only versions committed by atomic activation are visible; prepared or
// verified candidates that were never activated are not visible to this
// ordinary query, and cross-workspace identities are never disclosed.
func (m *inMemory) queryExactVersion(query PublicationQuery) (PublicationView, error) {
	if query.ArtifactVersionID == "" {
		return PublicationView{}, &Error{Code: ErrorInvalidIntent}
	}
	activated, ok := m.persistence.activated[query.ArtifactVersionID]
	if !ok {
		return PublicationView{}, &Error{Code: ErrorNotFound}
	}
	if activated.policyDomainID != query.PolicyDomainID || activated.taskID != query.TaskID {
		return PublicationView{}, &Error{Code: ErrorNotFound}
	}
	return versionView(QueryExactVersion, query.PolicyDomainID, query.TaskID, activated), nil
}

// queryExactMember resolves one exact member of an activated Artifact
// Version by ArtifactID. Members of never-activated candidates are not
// visible, and cross-workspace identities are never disclosed.
func (m *inMemory) queryExactMember(query PublicationQuery) (PublicationView, error) {
	if query.ArtifactVersionID == "" || query.ArtifactID == "" {
		return PublicationView{}, &Error{Code: ErrorInvalidIntent}
	}
	activated, ok := m.persistence.activated[query.ArtifactVersionID]
	if !ok {
		return PublicationView{}, &Error{Code: ErrorNotFound}
	}
	if activated.policyDomainID != query.PolicyDomainID || activated.taskID != query.TaskID {
		return PublicationView{}, &Error{Code: ErrorNotFound}
	}
	for _, member := range activated.members {
		if member.artifactID != query.ArtifactID {
			continue
		}
		memberView := memberRecordView(member)
		view := versionView(QueryExactMember, query.PolicyDomainID, query.TaskID, activated)
		view.ArtifactID = query.ArtifactID
		view.Member = &memberView
		return view, nil
	}
	return PublicationView{}, &Error{Code: ErrorNotFound}
}

// queryVersionHistory returns the Task's committed version history ordered
// exclusively by the explicit committed stream revision. Diagnostic time,
// ID lexical order, row order, and file discovery never participate; the
// current head is the explicit stream pointer.
func (m *inMemory) queryVersionHistory(query PublicationQuery) (PublicationView, error) {
	versions := m.activatedVersionsForTask(query.PolicyDomainID, query.TaskID)
	if len(versions) == 0 {
		return PublicationView{}, &Error{Code: ErrorNotFound}
	}
	view := PublicationView{
		Kind: QueryVersionHistory, PolicyDomainID: query.PolicyDomainID, TaskID: query.TaskID,
	}
	stream := m.persistence.streams[taskScope{policyDomainID: query.PolicyDomainID, taskID: query.TaskID}]
	if stream != nil {
		view.StreamRevision = stream.revision
		view.CurrentHead = stream.currentHead
	}
	for _, activated := range versions {
		view.History = append(view.History, artifactVersionView(activated))
	}
	return view, nil
}

// versionView renders one activated version as an immutable query view.
func versionView(kind PublicationQueryKind, policyDomainID PolicyDomainID, taskID TaskID, activated activatedRecord) PublicationView {
	view := PublicationView{
		Kind: kind, PolicyDomainID: policyDomainID, TaskID: taskID,
		OperationID: activated.operationID, State: OperationActivated,
		StreamRevision:     activated.streamRevision,
		ArtifactVersionID:  activated.versionID,
		ManifestDigest:     activated.manifestDigest,
		LineageDigest:      activated.lineageDigest,
		PublicationKind:    activated.kind,
		Parent:             activated.parent,
		ContractID:         activated.contractID,
		ActivationEvidence: cloneEvidence(activated.evidence),
		OccurredAt:         activated.occurredAt,
	}
	for _, member := range activated.members {
		view.Members = append(view.Members, memberRecordView(member))
	}
	return view
}

// artifactVersionView renders the immutable history entry of one activated
// version.
func artifactVersionView(activated activatedRecord) ArtifactVersionView {
	view := ArtifactVersionView{
		ArtifactVersionID: activated.versionID,
		PublicationKind:   activated.kind,
		Parent:            activated.parent,
		ManifestDigest:    activated.manifestDigest,
		LineageDigest:     activated.lineageDigest,
		StreamRevision:    activated.streamRevision,
		OperationID:       activated.operationID,
		ContractID:        activated.contractID,
	}
	for _, member := range activated.members {
		view.Members = append(view.Members, memberRecordView(member))
	}
	return view
}

func memberRecordView(member memberRecord) ArtifactMemberView {
	return ArtifactMemberView{
		ArtifactID: member.artifactID, Kind: member.kind, LogicalName: member.logicalName,
		MediaType: member.mediaType, Size: member.size, ContentDigest: member.contentDigest,
	}
}
