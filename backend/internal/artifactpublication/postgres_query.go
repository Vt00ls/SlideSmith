package artifactpublication

// This file implements the pure read-only Query seam of the real PostgreSQL
// owned persistence adapter. Queries never accept evidence, never create
// Durable Object handles, never write audit/outbox, never clean residue,
// and never change the current version. They reuse the same view builders as
// the in-memory authority (versionView, artifactVersionView,
// memberRecordView) and the same scope gates (validateScopeForVersion), so
// both adapters return identical public views for identical durable facts.

import (
	"context"
)

// Query is the pure read-only seam of the real PostgreSQL authority.
func (p *PostgresAuthority) Query(ctx context.Context, query PublicationQuery) (PublicationView, error) {
	if !validQueryKind(query.Kind) || query.PolicyDomainID == "" || query.TaskID == "" {
		return PublicationView{}, &Error{Code: ErrorInvalidIntent}
	}
	switch query.Kind {
	case QueryOperation:
		return p.queryOperation(ctx, query)
	case QueryCandidate:
		return p.queryCandidate(ctx, query)
	case QueryTaskStream:
		return p.queryTaskStream(ctx, query)
	case QueryExactVersion:
		return p.queryExactVersion(ctx, query)
	case QueryExactMember:
		return p.queryExactMember(ctx, query)
	case QueryVersionHistory:
		return p.queryVersionHistory(ctx, query)
	case QueryResolveContentTarget:
		return p.queryResolveContentTarget(ctx, query)
	case QueryVerifyContentTarget:
		return p.queryVerifyContentTarget(ctx, query)
	case QueryIssueC04ReconstructionCapability:
		return p.queryIssueC04ReconstructionCapability(ctx, query)
	case QueryVerifyC04ReconstructionCapability:
		return p.queryVerifyC04ReconstructionCapability(ctx, query)
	default:
		return PublicationView{}, &Error{Code: ErrorInvalidIntent}
	}
}

func (p *PostgresAuthority) queryOperation(ctx context.Context, query PublicationQuery) (PublicationView, error) {
	scope := operationScope{policyDomainID: query.PolicyDomainID, taskID: query.TaskID, operationID: query.OperationID}
	record, found, err := p.loadOperation(ctx, p.db, scope)
	if err != nil {
		return PublicationView{}, normalizePersistenceError(err)
	}
	if !found || record == nil {
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
			view.Members = append(view.Members, memberRecordView(member))
		}
	}
	view.Verification = m0Verification(record)
	return view, nil
}

func (p *PostgresAuthority) queryCandidate(ctx context.Context, query PublicationQuery) (PublicationView, error) {
	scope, found, err := p.loadOperationByVersion(ctx, p.db, query.ArtifactVersionID)
	if err != nil {
		return PublicationView{}, normalizePersistenceError(err)
	}
	if !found || scope.policyDomainID != query.PolicyDomainID || scope.taskID != query.TaskID {
		// Cross-workspace identity is never disclosed.
		return PublicationView{}, &Error{Code: ErrorNotFound}
	}
	record, found, err := p.loadOperation(ctx, p.db, scope)
	if err != nil {
		return PublicationView{}, normalizePersistenceError(err)
	}
	if !found || record == nil {
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
			view.Members = append(view.Members, memberRecordView(member))
		}
	}
	view.Verification = m0Verification(record)
	return view, nil
}

func (p *PostgresAuthority) queryTaskStream(ctx context.Context, query PublicationQuery) (PublicationView, error) {
	stream, found, err := p.loadStream(ctx, p.db, taskScope{policyDomainID: query.PolicyDomainID, taskID: query.TaskID})
	if err != nil {
		return PublicationView{}, normalizePersistenceError(err)
	}
	if !found || stream == nil {
		return PublicationView{}, &Error{Code: ErrorNotFound}
	}
	return PublicationView{
		Kind: QueryTaskStream, PolicyDomainID: query.PolicyDomainID, TaskID: query.TaskID,
		StreamRevision: stream.revision, CurrentHead: stream.currentHead,
	}, nil
}

func (p *PostgresAuthority) queryExactVersion(ctx context.Context, query PublicationQuery) (PublicationView, error) {
	if query.ArtifactVersionID == "" {
		return PublicationView{}, &Error{Code: ErrorInvalidIntent}
	}
	activated, found, err := p.loadActivated(ctx, p.db, query.ArtifactVersionID)
	if err != nil {
		return PublicationView{}, normalizePersistenceError(err)
	}
	if !found || activated == nil {
		return PublicationView{}, &Error{Code: ErrorNotFound}
	}
	if activated.policyDomainID != query.PolicyDomainID || activated.taskID != query.TaskID {
		return PublicationView{}, &Error{Code: ErrorNotFound}
	}
	if err := validatePresentedScope(p.scopeResolver, query.Scope, *activated); err != nil {
		return PublicationView{}, err
	}
	return versionView(QueryExactVersion, query.PolicyDomainID, query.TaskID, *activated), nil
}

func (p *PostgresAuthority) queryExactMember(ctx context.Context, query PublicationQuery) (PublicationView, error) {
	if query.ArtifactVersionID == "" || query.ArtifactID == "" {
		return PublicationView{}, &Error{Code: ErrorInvalidIntent}
	}
	activated, found, err := p.loadActivated(ctx, p.db, query.ArtifactVersionID)
	if err != nil {
		return PublicationView{}, normalizePersistenceError(err)
	}
	if !found || activated == nil {
		return PublicationView{}, &Error{Code: ErrorNotFound}
	}
	if activated.policyDomainID != query.PolicyDomainID || activated.taskID != query.TaskID {
		return PublicationView{}, &Error{Code: ErrorNotFound}
	}
	if err := validatePresentedScope(p.scopeResolver, query.Scope, *activated); err != nil {
		return PublicationView{}, err
	}
	for _, member := range activated.members {
		if member.artifactID != query.ArtifactID {
			continue
		}
		memberView := memberRecordView(member)
		view := versionView(QueryExactMember, query.PolicyDomainID, query.TaskID, *activated)
		view.ArtifactID = query.ArtifactID
		view.Member = &memberView
		return view, nil
	}
	return PublicationView{}, &Error{Code: ErrorNotFound}
}

func (p *PostgresAuthority) queryVersionHistory(ctx context.Context, query PublicationQuery) (PublicationView, error) {
	versions, err := p.loadActivatedVersionsForTask(ctx, p.db, query.PolicyDomainID, query.TaskID)
	if err != nil {
		return PublicationView{}, normalizePersistenceError(err)
	}
	if len(versions) == 0 {
		return PublicationView{}, &Error{Code: ErrorNotFound}
	}
	view := PublicationView{
		Kind: QueryVersionHistory, PolicyDomainID: query.PolicyDomainID, TaskID: query.TaskID,
	}
	stream, found, err := p.loadStream(ctx, p.db, taskScope{policyDomainID: query.PolicyDomainID, taskID: query.TaskID})
	if err != nil {
		return PublicationView{}, normalizePersistenceError(err)
	}
	if found && stream != nil {
		view.StreamRevision = stream.revision
		view.CurrentHead = stream.currentHead
	}
	for _, activated := range versions {
		view.History = append(view.History, artifactVersionView(*activated))
	}
	return view, nil
}

func (p *PostgresAuthority) queryResolveContentTarget(ctx context.Context, query PublicationQuery) (PublicationView, error) {
	if query.ArtifactVersionID == "" || query.ArtifactID == "" {
		return PublicationView{}, &Error{Code: ErrorInvalidIntent}
	}
	if !validContentIntent(query.ContentIntent) || !query.Scope.valid() {
		return PublicationView{}, &Error{Code: ErrorInvalidIntent}
	}
	activated, found, err := p.loadActivated(ctx, p.db, query.ArtifactVersionID)
	if err != nil {
		return PublicationView{}, normalizePersistenceError(err)
	}
	if !found || activated == nil || activated.policyDomainID != query.PolicyDomainID || activated.taskID != query.TaskID {
		// Cross-workspace, unactivated, and unknown identities all resolve
		// to the same non-enumerating not-found error.
		return PublicationView{}, &Error{Code: ErrorNotFound}
	}
	member, ok := activated.memberByArtifactID(query.ArtifactID)
	if !ok {
		return PublicationView{}, &Error{Code: ErrorNotFound}
	}
	// The scope must be the current availability fact of the exact version
	// before any member fact is disclosed.
	if err := validateScopeForVersion(p.scopeResolver, query.Scope, *activated); err != nil {
		return PublicationView{}, err
	}
	disposition := dispositionForMediaType(member.mediaType)
	if disposition == ContentDispositionActive {
		return PublicationView{}, &Error{Code: ErrorInvalidIntent}
	}
	target := ArtifactContentTarget{
		SchemaVersion: activated.schemaVersion, PolicyDomainID: query.PolicyDomainID,
		TaskID: query.TaskID, ArtifactVersionID: activated.versionID,
		ArtifactID: member.artifactID, ManifestDigest: activated.manifestDigest,
		MemberDigest: member.contentDigest, Size: member.size,
		MediaType: member.mediaType, LogicalName: member.logicalName,
		Disposition:            disposition,
		AvailabilityGeneration: query.Scope.AvailabilityGeneration,
		Intent:                 query.ContentIntent,
		ScopeKind:              query.Scope.Kind,
		OccurredAt:             p.nowValue(),
	}
	target.Digest = target.CanonicalDigest()
	return PublicationView{
		Kind: QueryResolveContentTarget, PolicyDomainID: query.PolicyDomainID,
		TaskID: query.TaskID, ArtifactVersionID: query.ArtifactVersionID,
		ArtifactID: query.ArtifactID, ContentTarget: &target,
		State: OperationActivated,
	}, nil
}

func (p *PostgresAuthority) queryVerifyContentTarget(ctx context.Context, query PublicationQuery) (PublicationView, error) {
	target := query.ContentTarget
	if target == nil {
		return PublicationView{}, &Error{Code: ErrorInvalidIntent}
	}
	if !validContentIntent(target.Intent) || !validContentScopeKind(target.ScopeKind) ||
		!validContentDisposition(target.Disposition) || target.ArtifactID == "" {
		return PublicationView{}, &Error{Code: ErrorInvalidIntent}
	}
	if target.SchemaVersion.Major() != SchemaV1.Major() {
		return PublicationView{}, &Error{Code: ErrorUnsupportedSchema}
	}
	if !validDigest(target.Digest) || target.Digest != target.CanonicalDigest() {
		return PublicationView{}, &Error{Code: ErrorIntegrityConflict}
	}
	activated, found, err := p.loadActivated(ctx, p.db, target.ArtifactVersionID)
	if err != nil {
		return PublicationView{}, normalizePersistenceError(err)
	}
	if !found || activated == nil || activated.policyDomainID != target.PolicyDomainID || activated.taskID != target.TaskID {
		return PublicationView{}, &Error{Code: ErrorNotFound}
	}
	if activated.manifestDigest != target.ManifestDigest {
		return PublicationView{}, &Error{Code: ErrorIntegrityConflict}
	}
	member, ok := activated.memberByArtifactID(target.ArtifactID)
	if !ok {
		return PublicationView{}, &Error{Code: ErrorNotFound}
	}
	if member.contentDigest != target.MemberDigest || member.size != target.Size ||
		member.mediaType != target.MediaType || member.logicalName != target.LogicalName {
		return PublicationView{}, &Error{Code: ErrorIntegrityConflict}
	}
	if dispositionForMediaType(member.mediaType) != target.Disposition {
		return PublicationView{}, &Error{Code: ErrorIntegrityConflict}
	}
	presented := query.Scope
	if !presented.valid() {
		return PublicationView{}, &Error{Code: ErrorInvalidIntent}
	}
	if presented.Kind != target.ScopeKind {
		return PublicationView{}, &Error{Code: ErrorNotFound}
	}
	if err := validateScopeForVersion(p.scopeResolver, presented, *activated); err != nil {
		return PublicationView{}, err
	}
	if presented.AvailabilityGeneration != target.AvailabilityGeneration {
		return PublicationView{}, &Error{Code: ErrorNotFound}
	}
	return PublicationView{
		Kind: QueryVerifyContentTarget, PolicyDomainID: target.PolicyDomainID,
		TaskID: target.TaskID, ArtifactVersionID: target.ArtifactVersionID,
		ArtifactID: target.ArtifactID, ContentTarget: target,
		State: OperationActivated,
	}, nil
}

func (p *PostgresAuthority) queryIssueC04ReconstructionCapability(ctx context.Context, query PublicationQuery) (PublicationView, error) {
	if query.ArtifactVersionID == "" {
		return PublicationView{}, &Error{Code: ErrorInvalidIntent}
	}
	if !query.Scope.valid() {
		return PublicationView{}, &Error{Code: ErrorInvalidIntent}
	}
	if query.ExpiresAt <= p.nowValue() {
		return PublicationView{}, &Error{Code: ErrorInvalidIntent}
	}
	if p.publicationAuth == "" {
		return PublicationView{}, &Error{Code: ErrorRetryableUnavailable}
	}
	if query.Authority.Kind != AuthorityTaskOrchestration || query.Authority.ID != p.toAuth {
		return PublicationView{}, &Error{Code: ErrorOwnershipDenied}
	}
	activated, found, err := p.loadActivated(ctx, p.db, query.ArtifactVersionID)
	if err != nil {
		return PublicationView{}, normalizePersistenceError(err)
	}
	if !found || activated == nil || activated.policyDomainID != query.PolicyDomainID || activated.taskID != query.TaskID {
		return PublicationView{}, &Error{Code: ErrorNotFound}
	}
	if err := validateScopeForVersion(p.scopeResolver, query.Scope, *activated); err != nil {
		return PublicationView{}, err
	}
	capability := C04ReconstructionCapability{
		SchemaVersion:          activated.schemaVersion,
		PublicationAuthorityID: p.publicationAuth,
		PolicyDomainID:         query.PolicyDomainID,
		TaskID:                 query.TaskID,
		ArtifactVersionID:      activated.versionID,
		ManifestDigest:         activated.manifestDigest,
		AvailabilityGeneration: query.Scope.AvailabilityGeneration,
		ExpiresAt:              query.ExpiresAt,
		OccurredAt:             p.nowValue(),
	}
	capability.Digest = capability.CanonicalDigest()
	return PublicationView{
		Kind: QueryIssueC04ReconstructionCapability, PolicyDomainID: query.PolicyDomainID,
		TaskID: query.TaskID, ArtifactVersionID: query.ArtifactVersionID,
		C04Capability: &capability, State: OperationActivated,
	}, nil
}

func (p *PostgresAuthority) queryVerifyC04ReconstructionCapability(ctx context.Context, query PublicationQuery) (PublicationView, error) {
	capability := query.C04Capability
	if capability == nil {
		return PublicationView{}, &Error{Code: ErrorInvalidIntent}
	}
	if capability.SchemaVersion.Major() != SchemaV1.Major() {
		return PublicationView{}, &Error{Code: ErrorUnsupportedSchema}
	}
	if !validDigest(capability.Digest) || capability.Digest != capability.CanonicalDigest() {
		return PublicationView{}, &Error{Code: ErrorIntegrityConflict}
	}
	if capability.PublicationAuthorityID != p.publicationAuth {
		return PublicationView{}, &Error{Code: ErrorIntegrityConflict}
	}
	if capability.ExpiresAt <= p.nowValue() {
		return PublicationView{}, &Error{Code: ErrorStaleAuthority}
	}
	activated, found, err := p.loadActivated(ctx, p.db, capability.ArtifactVersionID)
	if err != nil {
		return PublicationView{}, normalizePersistenceError(err)
	}
	if !found || activated == nil || activated.policyDomainID != capability.PolicyDomainID || activated.taskID != capability.TaskID {
		return PublicationView{}, &Error{Code: ErrorNotFound}
	}
	if activated.manifestDigest != capability.ManifestDigest {
		return PublicationView{}, &Error{Code: ErrorIntegrityConflict}
	}
	presented := query.Scope
	if !presented.valid() {
		return PublicationView{}, &Error{Code: ErrorInvalidIntent}
	}
	if err := validateScopeForVersion(p.scopeResolver, presented, *activated); err != nil {
		return PublicationView{}, err
	}
	if presented.AvailabilityGeneration != capability.AvailabilityGeneration {
		return PublicationView{}, &Error{Code: ErrorNotFound}
	}
	return PublicationView{
		Kind: QueryVerifyC04ReconstructionCapability, PolicyDomainID: capability.PolicyDomainID,
		TaskID: capability.TaskID, ArtifactVersionID: capability.ArtifactVersionID,
		C04Capability: capability, State: OperationActivated,
	}, nil
}
