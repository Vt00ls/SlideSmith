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
	case QueryResolveContentTarget:
		return m.queryResolveContentTarget(query)
	case QueryVerifyContentTarget:
		return m.queryVerifyContentTarget(query)
	case QueryIssueC04ReconstructionCapability:
		return m.queryIssueC04ReconstructionCapability(query)
	case QueryVerifyC04ReconstructionCapability:
		return m.queryVerifyC04ReconstructionCapability(query)
	case QueryResidue:
		return m.queryResidue(query)
	case QueryCleanupDebt:
		return m.queryCleanupDebt(query)
	default:
		return PublicationView{}, &Error{Code: ErrorInvalidIntent}
	}
}

func (m *inMemory) queryResidue(query PublicationQuery) (PublicationView, error) {
	scope := operationScope{policyDomainID: query.PolicyDomainID, taskID: query.TaskID, operationID: query.OperationID}
	record := m.persistence.operations[scope]
	if record == nil || record.residue == nil {
		// Non-enumerating: the same error is returned whether the residue
		// exists in another scope or does not exist at all.
		return PublicationView{}, &Error{Code: ErrorNotFound}
	}
	view := residueView(record.residue)
	effectiveDisposition(view, record.residue, m.now())
	return PublicationView{
		Kind: QueryResidue, PolicyDomainID: query.PolicyDomainID, TaskID: query.TaskID,
		OperationID: record.operationID, State: record.state,
		OccurredAt: record.occurredAt, Residue: view,
	}, nil
}

func (m *inMemory) queryCleanupDebt(query PublicationQuery) (PublicationView, error) {
	if query.CleanupDebtID == "" && query.OperationID == "" {
		return PublicationView{}, &Error{Code: ErrorInvalidIntent}
	}
	if query.CleanupDebtID != "" {
		for scope, record := range m.persistence.operations {
			if record.debt == nil || record.debt.debtID != query.CleanupDebtID {
				continue
			}
			if scope.policyDomainID != query.PolicyDomainID || scope.taskID != query.TaskID {
				// Cross-workspace identity is never disclosed.
				return PublicationView{}, &Error{Code: ErrorNotFound}
			}
			return PublicationView{
				Kind: QueryCleanupDebt, PolicyDomainID: query.PolicyDomainID, TaskID: query.TaskID,
				OperationID: record.operationID, State: record.state,
				OccurredAt: record.occurredAt, CleanupDebt: debtView(record.debt),
			}, nil
		}
		return PublicationView{}, &Error{Code: ErrorNotFound}
	}
	scope := operationScope{policyDomainID: query.PolicyDomainID, taskID: query.TaskID, operationID: query.OperationID}
	record := m.persistence.operations[scope]
	if record == nil || record.debt == nil {
		return PublicationView{}, &Error{Code: ErrorNotFound}
	}
	return PublicationView{
		Kind: QueryCleanupDebt, PolicyDomainID: query.PolicyDomainID, TaskID: query.TaskID,
		OperationID: record.operationID, State: record.state,
		OccurredAt: record.occurredAt, CleanupDebt: debtView(record.debt),
	}, nil
}

// residueView builds the content-free inspection view of one residue.
func residueView(residue *residueRecord) *ResidueView {
	view := &ResidueView{
		OperationID: residue.operationID, PolicyDomainID: residue.policyDomainID,
		TaskID: residue.taskID, Owner: residue.owner,
		Generation: residue.generation, Fence: residue.fence,
		ReleaseIntent: residue.releaseIntent, OccurredAt: residue.occurredAt,
		Expiry: residue.expiry, Disposition: residue.disposition,
		RequiresReconciliation: residue.requiresReconciliation,
		AttemptCount:           residue.attemptCount, ConsecutiveFailures: residue.consecutiveFailures,
		NextRetryAt: residue.nextRetryAt, ClaimGeneration: residue.claimGeneration,
		ClaimFence: residue.claimFence, LastErrorCategory: residue.lastErrorCategory,
		ReleaseReceipt: cloneReleaseReceipt(residue.releaseReceipt),
		DebtID:         residue.debtID,
	}
	if residue.assembly != nil {
		view.AssemblyReference = residue.assembly.Reference
		view.AssemblyIdentityDigest = residue.assembly.IdentityDigest
	}
	for _, ref := range residue.stagingRefs {
		view.StagingRefs = append(view.StagingRefs, StagingReferenceView{
			Slot: ref.slot, ContentID: ref.contentID, ContentDigest: ref.contentDigest,
			Size: ref.size, Purpose: ref.purpose,
			PhysicalGeneration: ref.physicalGeneration, AdapterID: ref.adapterID,
		})
	}
	return view
}

// debtView builds the content-free inspection view of one C05-owned debt.
func debtView(debt *cleanupDebtRecord) *CleanupDebtView {
	view := &CleanupDebtView{
		DebtID: debt.debtID, Revision: debt.revision,
		OperationID: debt.operationID, PolicyDomainID: debt.policyDomainID,
		TaskID: debt.taskID, Owner: debt.owner,
		ResourceReference: debt.resourceRef, ResourceIdentityDigest: debt.resourceDigest,
		ResourceGeneration: debt.resourceGeneration, ResourceFence: debt.resourceFence,
		Status: debt.status, CreatedAt: debt.createdAt, EligibleAt: debt.eligibleAt,
		FirstAttemptAt: debt.firstAttemptAt, LastAttemptAt: debt.lastAttemptAt,
		NextRetryAt: debt.nextRetryAt, AttemptCount: debt.attemptCount,
		ConsecutiveFailures: debt.consecutiveFailures, ClaimGeneration: debt.claimGeneration,
		ClaimFence: debt.claimFence, RetryDisposition: debt.retryDisposition,
		LastErrorCategory: debt.lastErrorCategory, Blockers: debt.blockers,
		ResolvedAt: debt.resolvedAt, ResolutionClass: debt.resolutionClass,
		ResolutionReason:      debt.resolutionReason,
		ResolutionEvidence:    cloneResolutionEvidence(debt.resolutionEvidence),
		ResolutionAuditFactID: debt.resolutionAuditFactID,
		ResolutionExpiresAt:   debt.resolutionExpiresAt,
	}
	return view
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
// ordinary query, and cross-workspace identities are never disclosed. When
// an authority scope is presented it must be the current availability fact
// of the exact version, otherwise the lookup fails closed non-enumerating.
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
	if err := validatePresentedScope(m.currentContentScope, query.Scope, activated); err != nil {
		return PublicationView{}, err
	}
	return versionView(QueryExactVersion, query.PolicyDomainID, query.TaskID, activated), nil
}

// queryExactMember resolves one exact member of an activated Artifact
// Version by ArtifactID. Members of never-activated candidates are not
// visible, and cross-workspace identities are never disclosed. When an
// authority scope is presented it must be the current availability fact of
// the exact version, otherwise the lookup fails closed non-enumerating.
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
	if err := validatePresentedScope(m.currentContentScope, query.Scope, activated); err != nil {
		return PublicationView{}, err
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

// validatePresentedScope enforces acceptance #4: an exact version/member
// lookup that presents an authority scope must resolve the current
// availability fact of that exact version, otherwise it fails closed with
// the same non-enumerating not-found error as a missing or cross-workspace
// identity. A scope is optional for ordinary version/member queries (the
// historical C05-02 contract), but when one is claimed it must be correct.
// It is shared by the in-memory authority and the real PostgreSQL adapter
// through the same narrow scope-resolution port.
func validatePresentedScope(resolve func(ContentScopeKey) (ContentScope, bool), scope ContentScope, activated activatedRecord) *Error {
	if scope.Kind == "" && scope.ID == "" && scope.AvailabilityGeneration == 0 {
		return nil // no scope presented: ordinary lookup semantics
	}
	if !scope.valid() {
		return &Error{Code: ErrorInvalidIntent}
	}
	return validateScopeForVersion(resolve, scope, activated)
}

// validateScopeForVersion checks that the presented scope is the current
// availability fact of the exact Artifact Version. Unknown, revoked, or
// stale/rotated scopes fail closed with a non-enumerating not-found error
// that is identical whether the scope is wrong or the version does not
// exist. This is the only scope gate in the module: C05 never creates a
// principal, share token, Access Code, Verification Session, or implicit
// administrator content authority, and owner/share/break-glass scopes can
// never union.
func validateScopeForVersion(resolve func(ContentScopeKey) (ContentScope, bool), scope ContentScope, activated activatedRecord) *Error {
	key := ContentScopeKey{
		PolicyDomainID: activated.policyDomainID, TaskID: activated.taskID,
		ArtifactVersionID: activated.versionID, Kind: scope.Kind, ID: scope.ID,
	}
	current, ok := resolve(key)
	if !ok {
		// Unknown or revoked scope: non-enumerating not-found, never
		// discloses the version or the scope existence.
		return &Error{Code: ErrorNotFound}
	}
	if current.Kind != scope.Kind || current.ID != scope.ID ||
		current.AvailabilityGeneration != scope.AvailabilityGeneration {
		// A scope mismatch or a stale/rotated availability generation fails
		// closed: the generation is the revocation fence.
		return &Error{Code: ErrorNotFound}
	}
	return nil
}

// queryResolveContentTarget resolves one exact member of one activated
// Artifact Version into a locator-free opaque ArtifactContentTarget under
// exactly one typed owner/share-link/break-glass scope and one short-term
// intent. Only versions committed by atomic activation resolve; prepared,
// verifying, rejected, cancelled, and residue are never resolvable. The
// query never creates a Durable Object read handle; mandatory access audit
// and authorization remain with the content delivery flow before any
// Durable Object open. Active-content dispositions fail closed.
func (m *inMemory) queryResolveContentTarget(query PublicationQuery) (PublicationView, error) {
	if query.ArtifactVersionID == "" || query.ArtifactID == "" {
		return PublicationView{}, &Error{Code: ErrorInvalidIntent}
	}
	if !validContentIntent(query.ContentIntent) || !query.Scope.valid() {
		return PublicationView{}, &Error{Code: ErrorInvalidIntent}
	}
	activated, ok := m.persistence.activated[query.ArtifactVersionID]
	if !ok || activated.policyDomainID != query.PolicyDomainID || activated.taskID != query.TaskID {
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
	if err := validateScopeForVersion(m.currentContentScope, query.Scope, activated); err != nil {
		return PublicationView{}, err
	}
	disposition := dispositionForMediaType(member.mediaType)
	if disposition == ContentDispositionActive {
		// Unsafe active-content disposition fails closed: C05 cannot
		// guarantee the safe active-content handling that HTML/SVG require,
		// so it refuses to issue a delivery target for them.
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
		OccurredAt:             m.now(),
	}
	target.Digest = target.CanonicalDigest()
	return PublicationView{
		Kind: QueryResolveContentTarget, PolicyDomainID: query.PolicyDomainID,
		TaskID: query.TaskID, ArtifactVersionID: query.ArtifactVersionID,
		ArtifactID: query.ArtifactID, ContentTarget: &target,
		State: OperationActivated,
	}, nil
}

// queryVerifyContentTarget re-validates a presented ArtifactContentTarget
// against the current immutable version facts and the current availability
// fact of the presented scope. Tampering, an expired/unknown version, a
// revoked/rotated scope, a stale availability generation, and a scope-kind
// union all fail closed. It remains a pure read-only query: it never
// creates a Durable Object read handle, never writes audit, and never
// changes any version fact.
func (m *inMemory) queryVerifyContentTarget(query PublicationQuery) (PublicationView, error) {
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
		// A tampered or re-signed target fails closed as an integrity
		// conflict.
		return PublicationView{}, &Error{Code: ErrorIntegrityConflict}
	}
	activated, ok := m.persistence.activated[target.ArtifactVersionID]
	if !ok || activated.policyDomainID != target.PolicyDomainID || activated.taskID != target.TaskID {
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
		// Scope union attempt: a target resolved under one authority path
		// can never be presented under another.
		return PublicationView{}, &Error{Code: ErrorNotFound}
	}
	if err := validateScopeForVersion(m.currentContentScope, presented, activated); err != nil {
		return PublicationView{}, err
	}
	if presented.AvailabilityGeneration != target.AvailabilityGeneration {
		// The target was issued under an availability epoch that is no
		// longer current (revoked or rotated): stale and fail closed.
		return PublicationView{}, &Error{Code: ErrorNotFound}
	}
	return PublicationView{
		Kind: QueryVerifyContentTarget, PolicyDomainID: target.PolicyDomainID,
		TaskID: target.TaskID, ArtifactVersionID: target.ArtifactVersionID,
		ArtifactID: target.ArtifactID, ContentTarget: target,
		State: OperationActivated,
	}, nil
}

// queryIssueC04ReconstructionCapability issues the exact Artifact Version
// input capability for C04 manual-edit reconstruction. Only the Platform
// (Task Orchestration authority) may select the exact version; C04 can
// never request issuance, and C05 never resolves a current/latest version
// here. Issuance requires exactly one typed scope whose current availability
// generation is bound into the capability, a declared expiry in the future,
// and an activated version in the exact policy domain/Task. Unactivated
// candidates and residue never resolve.
func (m *inMemory) queryIssueC04ReconstructionCapability(query PublicationQuery) (PublicationView, error) {
	if query.ArtifactVersionID == "" {
		return PublicationView{}, &Error{Code: ErrorInvalidIntent}
	}
	if !query.Scope.valid() {
		return PublicationView{}, &Error{Code: ErrorInvalidIntent}
	}
	if query.ExpiresAt <= m.now() {
		// A capability that is already expired is useless: the Platform
		// must declare a future expiry.
		return PublicationView{}, &Error{Code: ErrorInvalidIntent}
	}
	if m.config.PublicationAuthorityID == "" {
		return PublicationView{}, &Error{Code: ErrorRetryableUnavailable}
	}
	if query.Authority.Kind != AuthorityTaskOrchestration ||
		query.Authority.ID != m.config.TaskOrchestrationAuthorityID {
		// Only the Platform's exact selection can be issued; C04, Runtime,
		// validators, and unregistered authorities are denied.
		return PublicationView{}, &Error{Code: ErrorOwnershipDenied}
	}
	activated, ok := m.persistence.activated[query.ArtifactVersionID]
	if !ok || activated.policyDomainID != query.PolicyDomainID || activated.taskID != query.TaskID {
		return PublicationView{}, &Error{Code: ErrorNotFound}
	}
	if err := validateScopeForVersion(m.currentContentScope, query.Scope, activated); err != nil {
		return PublicationView{}, err
	}
	capability := C04ReconstructionCapability{
		SchemaVersion:          activated.schemaVersion,
		PublicationAuthorityID: m.config.PublicationAuthorityID,
		PolicyDomainID:         query.PolicyDomainID,
		TaskID:                 query.TaskID,
		ArtifactVersionID:      activated.versionID,
		ManifestDigest:         activated.manifestDigest,
		AvailabilityGeneration: query.Scope.AvailabilityGeneration,
		ExpiresAt:              query.ExpiresAt,
		OccurredAt:             m.now(),
	}
	capability.Digest = capability.CanonicalDigest()
	return PublicationView{
		Kind: QueryIssueC04ReconstructionCapability, PolicyDomainID: query.PolicyDomainID,
		TaskID: query.TaskID, ArtifactVersionID: query.ArtifactVersionID,
		C04Capability: &capability, State: OperationActivated,
	}, nil
}

// queryVerifyC04ReconstructionCapability verifies a presented C04
// reconstruction capability against the current version facts, the
// publication authority identity, the current availability fact of the
// presented scope, the bound availability generation, and the declared
// expiry. Expired, tampered, cross-scope, or stale capabilities fail
// closed; the capability can never select a current/latest version or a
// publication target.
func (m *inMemory) queryVerifyC04ReconstructionCapability(query PublicationQuery) (PublicationView, error) {
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
	if capability.PublicationAuthorityID != m.config.PublicationAuthorityID {
		// The capability must come from the publication authority itself.
		return PublicationView{}, &Error{Code: ErrorIntegrityConflict}
	}
	if capability.ExpiresAt <= m.now() {
		// The declared expiry has passed: the Platform must issue a fresh
		// capability for a fresh reconstruction.
		return PublicationView{}, &Error{Code: ErrorStaleAuthority}
	}
	activated, ok := m.persistence.activated[capability.ArtifactVersionID]
	if !ok || activated.policyDomainID != capability.PolicyDomainID || activated.taskID != capability.TaskID {
		return PublicationView{}, &Error{Code: ErrorNotFound}
	}
	if activated.manifestDigest != capability.ManifestDigest {
		return PublicationView{}, &Error{Code: ErrorIntegrityConflict}
	}
	presented := query.Scope
	if !presented.valid() {
		return PublicationView{}, &Error{Code: ErrorInvalidIntent}
	}
	if err := validateScopeForVersion(m.currentContentScope, presented, activated); err != nil {
		return PublicationView{}, err
	}
	if presented.AvailabilityGeneration != capability.AvailabilityGeneration {
		// The capability is bound to a stale/rotated availability epoch.
		return PublicationView{}, &Error{Code: ErrorNotFound}
	}
	return PublicationView{
		Kind: QueryVerifyC04ReconstructionCapability, PolicyDomainID: capability.PolicyDomainID,
		TaskID: capability.TaskID, ArtifactVersionID: capability.ArtifactVersionID,
		C04Capability: capability, State: OperationActivated,
	}, nil
}

// memberByArtifactID returns the exact member of an activated version by
// ArtifactID.
func (a activatedRecord) memberByArtifactID(id ArtifactID) (memberRecord, bool) {
	for _, member := range a.members {
		if member.artifactID == id {
			return member, true
		}
	}
	return memberRecord{}, false
}
