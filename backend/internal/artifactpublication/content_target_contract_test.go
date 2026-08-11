package artifactpublication

// Contract tests for child SPEC #106 (C05-03): delivery version queries,
// authorized locator-free content targets, mutually exclusive
// owner/share-link/break-glass scopes, and the C04 reconstruction
// capability contract. All behavior is observed through the closed public
// Query seam; no private handler, SQL shape, or worker order is asserted.

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

// memberArtifactID returns the ArtifactID of the single member of an
// activated version through the ordinary exact-version query.
func memberArtifactID(t *testing.T, f *fixture, versionID ArtifactVersionID) ArtifactID {
	t.Helper()
	view, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryExactVersion, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		ArtifactVersionID: versionID,
	})
	if err != nil {
		t.Fatalf("query exact version: %v", err)
	}
	if len(view.Members) != 1 {
		t.Fatalf("expected exactly one member, got %d", len(view.Members))
	}
	return view.Members[0].ArtifactID
}

// activateVersion drives prepare -> verify -> activate for an explicit
// member set (used for non-deck members such as active-content reports).
func (f *fixture) activateVersion(t *testing.T, operationID string, members []ArtifactMemberSpec) (*evidenceSet, PublicationDecision) {
	t.Helper()
	set := f.buildEvidence(t, members)
	if _, err := f.core.Mutate(context.Background(), f.prepareIntent(operationID, f.preparePayload(operationID, set, members))); err != nil {
		t.Fatalf("prepare %s: %v", operationID, err)
	}
	if _, err := f.core.Mutate(context.Background(), f.verifyIntent(operationID, f.verifyPayload(set))); err != nil {
		t.Fatalf("verify %s: %v", operationID, err)
	}
	activated, err := f.core.Mutate(context.Background(), f.activateIntent(operationID))
	if err != nil {
		t.Fatalf("activate %s: %v", operationID, err)
	}
	return set, activated
}

// TestResolveContentTargetBindsExactFacts proves QueryResolveContentTarget
// binds the exact ArtifactVersionID, ArtifactID, manifest digest, member
// digest, size, media type, safe logical name, availability generation, and
// short-term intent (acceptance #5), and that the target is an opaque,
// locator-free digestible value that is not a ReadHandle or a storage
// locator (acceptance #6).
func TestResolveContentTargetBindsExactFacts(t *testing.T) {
	f := newFixture(t)
	_, activated := f.prepareVerifyActivate(t, "op-ct-exact")
	versionID := activated.ArtifactVersionID
	artifactID := memberArtifactID(t, f, versionID)
	owner := f.ownerScope(versionID)
	f.registerScope(versionID, owner)

	target := f.resolveTarget(t, versionID, artifactID, owner)
	if target.ArtifactVersionID != versionID {
		t.Fatalf("target version id = %q, want %q", target.ArtifactVersionID, versionID)
	}
	if target.ArtifactID != artifactID {
		t.Fatalf("target artifact id = %q, want %q", target.ArtifactID, artifactID)
	}
	if target.ManifestDigest != activated.ActivationEvidence.ManifestDigest {
		t.Fatalf("target manifest digest = %q, want %q", target.ManifestDigest, activated.ActivationEvidence.ManifestDigest)
	}
	if target.MemberDigest != testDigest("deck-content") {
		t.Fatalf("target member digest = %q, want the deck content digest", target.MemberDigest)
	}
	if target.Size != 1024 {
		t.Fatalf("target size = %d, want 1024", target.Size)
	}
	if target.MediaType != MediaTypePPTX {
		t.Fatalf("target media type = %q, want %q", target.MediaType, MediaTypePPTX)
	}
	if target.LogicalName != "Deck.pptx" {
		t.Fatalf("target logical name = %q, want the safe normalized name", target.LogicalName)
	}
	if target.Disposition != ContentDispositionAttachment {
		t.Fatalf("target disposition = %q, want attachment", target.Disposition)
	}
	if target.AvailabilityGeneration != owner.AvailabilityGeneration {
		t.Fatalf("target availability generation = %d, want %d", target.AvailabilityGeneration, owner.AvailabilityGeneration)
	}
	if target.Intent != ContentIntentDownload {
		t.Fatalf("target intent = %q, want download", target.Intent)
	}
	if target.ScopeKind != ContentScopeOwner {
		t.Fatalf("target scope kind = %q, want owner", target.ScopeKind)
	}
	if target.PolicyDomainID != f.policyDomain || target.TaskID != f.taskID {
		t.Fatalf("target scope = (%q, %q), want the fixture scope", target.PolicyDomainID, target.TaskID)
	}
	if target.Digest != target.CanonicalDigest() || !validDigest(target.Digest) {
		t.Fatalf("target digest must be the canonical digest: %q", target.Digest)
	}
	// The share-link delivery intent is a distinct short-term intent.
	shareView, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryResolveContentTarget, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		ArtifactVersionID: versionID, ArtifactID: artifactID,
		Scope: owner, ContentIntent: ContentIntentShareLinkDelivery,
	})
	if err != nil {
		t.Fatalf("resolve share-link delivery target: %v", err)
	}
	if shareView.ContentTarget.Intent != ContentIntentShareLinkDelivery ||
		shareView.ContentTarget.Digest == target.Digest {
		t.Fatalf("share-link delivery target must bind its own intent: %#v", shareView.ContentTarget)
	}
}

// TestResolveContentTargetRequiresActivatedVersion proves prepared,
// verified-but-not-activated, rejected, and cancelled candidates never
// resolve as content targets (acceptance #11): only versions committed by
// atomic activation are deliverable.
func TestResolveContentTargetRequiresActivatedVersion(t *testing.T) {
	f := newFixture(t)

	// Prepared only.
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	prepared := f.mustPrepare(t, "op-ct-prepared", set)
	// Prepared and verified, never activated.
	verified := f.mustPrepare(t, "op-ct-verified", set)
	f.mustVerify(t, "op-ct-verified", set)
	// Rejected.
	rejected := f.mustPrepare(t, "op-ct-rejected", set)
	if _, err := f.core.Mutate(context.Background(), f.rejectIntent("op-ct-rejected", RejectCandidateSuperseded, nil)); err != nil {
		t.Fatalf("reject: %v", err)
	}
	// Cancelled.
	cancelled := f.mustPrepare(t, "op-ct-cancelled", set)
	if _, err := f.core.Mutate(context.Background(), f.cancelIntent("op-ct-cancelled", CancelTaskOrchestration)); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	versionIDs := []ArtifactVersionID{prepared.ArtifactVersionID, verified.ArtifactVersionID,
		rejected.ArtifactVersionID, cancelled.ArtifactVersionID}
	for _, versionID := range versionIDs {
		owner := f.ownerScope(versionID)
		f.registerScope(versionID, owner)
		_, err := f.core.Query(context.Background(), PublicationQuery{
			Kind: QueryResolveContentTarget, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
			ArtifactVersionID: versionID, ArtifactID: "artifact-1",
			Scope: owner, ContentIntent: ContentIntentDownload,
		})
		if !isCode(err, ErrorNotFound) {
			t.Fatalf("unactivated candidate %q must not resolve as a content target, got %v", versionID, err)
		}
	}
}

// TestResolveContentTargetCrossWorkspaceNonEnumerating proves exact
// version/member content-target lookup under the wrong Personal Workspace
// or Task fails closed with the same non-enumerating error as a nonexistent
// identity, and never discloses cross-workspace facts (acceptance #4, #12).
func TestResolveContentTargetCrossWorkspaceNonEnumerating(t *testing.T) {
	f := newFixture(t)
	_, activated := f.prepareVerifyActivate(t, "op-ct-cross")
	versionID := activated.ArtifactVersionID
	artifactID := memberArtifactID(t, f, versionID)
	owner := f.ownerScope(versionID)
	f.registerScope(versionID, owner)

	// The authoritative cross-workspace denial: same scope facts, but the
	// lookup targets a different policy domain / Task.
	_, wrongDomainErr := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryResolveContentTarget, PolicyDomainID: "policy-domain-other", TaskID: f.taskID,
		ArtifactVersionID: versionID, ArtifactID: artifactID,
		Scope: owner, ContentIntent: ContentIntentDownload,
	})
	_, wrongTaskErr := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryResolveContentTarget, PolicyDomainID: f.policyDomain, TaskID: "task-other",
		ArtifactVersionID: versionID, ArtifactID: artifactID,
		Scope: owner, ContentIntent: ContentIntentDownload,
	})
	_, nonexistentErr := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryResolveContentTarget, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		ArtifactVersionID: "artifact-version-nonexistent", ArtifactID: artifactID,
		Scope: owner, ContentIntent: ContentIntentDownload,
	})
	if !isCode(wrongDomainErr, ErrorNotFound) || !isCode(wrongTaskErr, ErrorNotFound) ||
		!isCode(nonexistentErr, ErrorNotFound) {
		t.Fatalf("cross-workspace lookups must fail closed: domain=%v task=%v missing=%v",
			wrongDomainErr, wrongTaskErr, nonexistentErr)
	}
	if wrongDomainErr.Error() != nonexistentErr.Error() ||
		wrongTaskErr.Error() != nonexistentErr.Error() {
		t.Fatalf("cross-workspace errors must be non-enumerating: domain=%q task=%q missing=%q",
			wrongDomainErr, wrongTaskErr, nonexistentErr)
	}
	for _, probe := range []string{string(versionID), string(artifactID), "policy-domain-other", "task-other"} {
		if strings.Contains(wrongDomainErr.Error(), probe) {
			t.Fatalf("cross-workspace error discloses %q", probe)
		}
	}
}

// TestResolveContentTargetWrongScopeNonEnumerating proves a wrong, unknown,
// or stale owner/share-link/break-glass scope fails closed with the same
// non-enumerating error as a nonexistent version (acceptance #7, #12).
func TestResolveContentTargetWrongScopeNonEnumerating(t *testing.T) {
	f := newFixture(t)
	_, activated := f.prepareVerifyActivate(t, "op-ct-scope")
	versionID := activated.ArtifactVersionID
	artifactID := memberArtifactID(t, f, versionID)
	owner := f.ownerScope(versionID)
	f.registerScope(versionID, owner)

	baseline, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryResolveContentTarget, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		ArtifactVersionID: "artifact-version-nonexistent", ArtifactID: artifactID,
		Scope: owner, ContentIntent: ContentIntentDownload,
	})
	if err == nil || !isCode(err, ErrorNotFound) {
		t.Fatalf("baseline nonexistent error = %v, want not found", err)
	}
	_ = baseline

	attempts := []ContentScope{
		// Share Link scope is not registered for this version.
		f.shareScope(versionID),
		// BreakGlass scope is not registered for this version.
		f.breakGlassScope(versionID),
		// Owner scope with a rotated generation.
		{Kind: ContentScopeOwner, ID: owner.ID, AvailabilityGeneration: owner.AvailabilityGeneration + 1},
		// Owner scope with a different instance identity.
		{Kind: ContentScopeOwner, ID: "owner-principal-other", AvailabilityGeneration: owner.AvailabilityGeneration},
	}
	for _, scope := range attempts {
		_, attemptErr := f.core.Query(context.Background(), PublicationQuery{
			Kind: QueryResolveContentTarget, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
			ArtifactVersionID: versionID, ArtifactID: artifactID,
			Scope: scope, ContentIntent: ContentIntentDownload,
		})
		if attemptErr == nil || attemptErr.Error() != err.Error() {
			t.Fatalf("wrong scope %#v must be non-enumerating with the same error as a missing version: got %v want %q",
				scope, attemptErr, err.Error())
		}
	}

	// The correct scope still resolves.
	if _, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryResolveContentTarget, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		ArtifactVersionID: versionID, ArtifactID: artifactID,
		Scope: owner, ContentIntent: ContentIntentDownload,
	}); err != nil {
		t.Fatalf("correct scope must resolve: %v", err)
	}
}

// TestResolveContentTargetStaleScopeGenerationFailsClosed proves a
// revoked/rotated availability generation makes every later resolution and
// verification of the old epoch fail closed (acceptance #12 "revoked/stale
// generation").
func TestResolveContentTargetStaleScopeGenerationFailsClosed(t *testing.T) {
	f := newFixture(t)
	_, activated := f.prepareVerifyActivate(t, "op-ct-rotate")
	versionID := activated.ArtifactVersionID
	artifactID := memberArtifactID(t, f, versionID)
	owner := f.ownerScope(versionID)
	f.registerScope(versionID, owner)

	target := f.resolveTarget(t, versionID, artifactID, owner)
	if err := f.verifyTarget(t, target, owner); err != nil {
		t.Fatalf("target must verify while the availability epoch is current: %v", err)
	}

	// The availability epoch advances (rotation/revocation fence).
	f.rotateScope(versionID, owner, owner.AvailabilityGeneration+1)

	// Resolution with the old generation fails closed non-enumerating.
	_, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryResolveContentTarget, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		ArtifactVersionID: versionID, ArtifactID: artifactID,
		Scope: owner, ContentIntent: ContentIntentDownload,
	})
	if !isCode(err, ErrorNotFound) {
		t.Fatalf("stale-generation resolution = %v, want not found", err)
	}
	// Verification of the already-issued target fails closed too.
	if err := f.verifyTarget(t, target, owner); !isCode(err, ErrorNotFound) {
		t.Fatalf("stale-generation verification = %v, want not found", err)
	}

	// A fresh resolution under the new generation succeeds.
	rotated := ContentScope{Kind: owner.Kind, ID: owner.ID, AvailabilityGeneration: owner.AvailabilityGeneration + 1}
	fresh := f.resolveTarget(t, versionID, artifactID, rotated)
	if fresh.AvailabilityGeneration != rotated.AvailabilityGeneration {
		t.Fatalf("fresh target must bind the new generation: %#v", fresh)
	}
	if err := f.verifyTarget(t, fresh, rotated); err != nil {
		t.Fatalf("fresh target must verify: %v", err)
	}
}

// TestContentScopeUnionImpossible proves owner, Share Link, and break-glass
// scopes cannot union: the query surface carries exactly one scope field
// (structurally) and a target resolved under one authority path never
// verifies under another (behaviorally) (acceptance #7).
func TestContentScopeUnionImpossible(t *testing.T) {
	// Structural: the public query type carries exactly one scope field, so
	// a caller cannot express a union of owner/share/break-glass scopes.
	queryType := reflect.TypeOf(PublicationQuery{})
	scopeFields := 0
	for index := 0; index < queryType.NumField(); index++ {
		if queryType.Field(index).Type == reflect.TypeOf(ContentScope{}) {
			scopeFields++
		}
	}
	if scopeFields != 1 {
		t.Fatalf("PublicationQuery must carry exactly one ContentScope field, got %d", scopeFields)
	}

	f := newFixture(t)
	_, activated := f.prepareVerifyActivate(t, "op-ct-union")
	versionID := activated.ArtifactVersionID
	artifactID := memberArtifactID(t, f, versionID)
	owner := f.ownerScope(versionID)
	share := f.shareScope(versionID)
	glass := f.breakGlassScope(versionID)
	f.registerScope(versionID, owner)
	f.registerScope(versionID, share)
	f.registerScope(versionID, glass)

	target := f.resolveTarget(t, versionID, artifactID, owner)
	if target.ScopeKind != ContentScopeOwner {
		t.Fatalf("target must bind the owner scope kind: %#v", target)
	}
	// Presenting the same target under the Share Link or break-glass path
	// fails closed: the resolved scope kind can never be swapped.
	if err := f.verifyTarget(t, target, share); !isCode(err, ErrorNotFound) {
		t.Fatalf("owner target under share scope = %v, want not found", err)
	}
	if err := f.verifyTarget(t, target, glass); !isCode(err, ErrorNotFound) {
		t.Fatalf("owner target under break-glass scope = %v, want not found", err)
	}
	// A target resolved under the Share Link path binds the share scope and
	// verifies only under the share scope.
	shareTarget := f.resolveTarget(t, versionID, artifactID, share)
	if shareTarget.ScopeKind != ContentScopeShareLink {
		t.Fatalf("share target must bind the share scope kind: %#v", shareTarget)
	}
	if err := f.verifyTarget(t, shareTarget, share); err != nil {
		t.Fatalf("share target under share scope must verify: %v", err)
	}
	if err := f.verifyTarget(t, shareTarget, owner); !isCode(err, ErrorNotFound) {
		t.Fatalf("share target under owner scope = %v, want not found", err)
	}
}

// TestResolveContentTargetActiveContentDispositionFailsClosed proves an
// unsafe active-content disposition (HTML/SVG members) fails closed for
// content delivery targets (acceptance #12 "active-content disposition").
func TestResolveContentTargetActiveContentDispositionFailsClosed(t *testing.T) {
	reportMember := ArtifactMemberSpec{
		Slot: "slot-report", Kind: ArtifactKindValidationReport, LogicalName: "ValidationReport.html",
		MediaType: MediaTypeHTML, Size: 512, ContentDigest: testDigest("report-content"),
	}
	f := newFixture(t)
	_, activated := f.activateVersion(t, "op-ct-active", []ArtifactMemberSpec{reportMember})
	versionID := activated.ArtifactVersionID
	artifactID := memberArtifactID(t, f, versionID)
	owner := f.ownerScope(versionID)
	f.registerScope(versionID, owner)

	_, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryResolveContentTarget, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		ArtifactVersionID: versionID, ArtifactID: artifactID,
		Scope: owner, ContentIntent: ContentIntentDownload,
	})
	if !isCode(err, ErrorInvalidIntent) {
		t.Fatalf("active-content download target = %v, want fail closed", err)
	}
	// The safe PPTX member of a separate first-generation stream still
	// resolves: only the active-content disposition is refused, never the
	// delivery of safe attachments.
	deckF := newFixture(t)
	_, deck := deckF.prepareVerifyActivate(t, "op-ct-deck")
	deckID := deck.ArtifactVersionID
	deckArtifactID := memberArtifactID(t, deckF, deckID)
	deckOwner := deckF.ownerScope(deckID)
	deckF.registerScope(deckID, deckOwner)
	if _, err := deckF.core.Query(context.Background(), PublicationQuery{
		Kind: QueryResolveContentTarget, PolicyDomainID: deckF.policyDomain, TaskID: deckF.taskID,
		ArtifactVersionID: deckID, ArtifactID: deckArtifactID,
		Scope: deckOwner, ContentIntent: ContentIntentDownload,
	}); err != nil {
		t.Fatalf("safe attachment must still resolve: %v", err)
	}
}

// TestVerifyContentTargetFailsClosedOnTamper proves verification of a
// presented content target re-derives the current immutable facts: any
// tampering (member digest, size, manifest, scope kind, or digest) fails
// closed as an integrity conflict or non-enumerating denial, and the query
// never creates a Durable Object read handle (acceptance #6, #8).
func TestVerifyContentTargetFailsClosedOnTamper(t *testing.T) {
	f := newFixture(t)
	_, activated := f.prepareVerifyActivate(t, "op-ct-verify")
	versionID := activated.ArtifactVersionID
	artifactID := memberArtifactID(t, f, versionID)
	owner := f.ownerScope(versionID)
	f.registerScope(versionID, owner)

	target := f.resolveTarget(t, versionID, artifactID, owner)
	if err := f.verifyTarget(t, target, owner); err != nil {
		t.Fatalf("genuine target must verify: %v", err)
	}

	// Tampering with any binding fact fails closed.
	tampered := *target
	tampered.MemberDigest = testDigest("other-content")
	if err := f.verifyTarget(t, &tampered, owner); !isCode(err, ErrorIntegrityConflict) {
		t.Fatalf("tampered member digest = %v, want integrity conflict", err)
	}
	tampered = *target
	tampered.Size = 999
	if err := f.verifyTarget(t, &tampered, owner); !isCode(err, ErrorIntegrityConflict) {
		t.Fatalf("tampered size = %v, want integrity conflict", err)
	}
	tampered = *target
	tampered.ManifestDigest = testDigest("other-manifest")
	if err := f.verifyTarget(t, &tampered, owner); !isCode(err, ErrorIntegrityConflict) {
		t.Fatalf("tampered manifest digest = %v, want integrity conflict", err)
	}
	tampered = *target
	tampered.LogicalName = "../escape.pptx"
	if err := f.verifyTarget(t, &tampered, owner); !isCode(err, ErrorIntegrityConflict) {
		t.Fatalf("tampered logical name = %v, want integrity conflict", err)
	}
	tampered = *target
	tampered.Intent = ContentIntentShareLinkDelivery // not re-signed
	if err := f.verifyTarget(t, &tampered, owner); !isCode(err, ErrorIntegrityConflict) {
		t.Fatalf("unsigned intent swap = %v, want integrity conflict", err)
	}
	// A re-signed target under a different scope kind never verifies under
	// the owner scope: scope union is rejected even when the digest is
	// internally consistent.
	tampered = *target
	tampered.ScopeKind = ContentScopeShareLink
	tampered.Digest = tampered.CanonicalDigest()
	if err := f.verifyTarget(t, &tampered, owner); !isCode(err, ErrorNotFound) {
		t.Fatalf("re-signed scope-kind swap = %v, want not found", err)
	}
	// A version that was never activated never verifies.
	tampered = *target
	tampered.ArtifactVersionID = "artifact-version-nonexistent"
	tampered.Digest = tampered.CanonicalDigest()
	if err := f.verifyTarget(t, &tampered, owner); !isCode(err, ErrorNotFound) {
		t.Fatalf("nonexistent version verification = %v, want not found", err)
	}
}

// TestIssueC04ReconstructionCapabilityBindsExactFacts proves the C04
// reconstruction capability binds the publication authority, policy domain,
// Task, exact ArtifactVersionID, manifest digest, availability generation,
// and expiry (acceptance #9).
func TestIssueC04ReconstructionCapabilityBindsExactFacts(t *testing.T) {
	f := newFixture(t)
	_, activated := f.prepareVerifyActivate(t, "op-c04-issue")
	versionID := activated.ArtifactVersionID
	owner := f.ownerScope(versionID)
	f.registerScope(versionID, owner)
	expiry := f.now + 3600

	capability := f.issueC04Capability(t, versionID, owner, expiry)
	if capability.PublicationAuthorityID != f.publicationAuthority {
		t.Fatalf("capability publication authority = %q, want %q", capability.PublicationAuthorityID, f.publicationAuthority)
	}
	if capability.PolicyDomainID != f.policyDomain || capability.TaskID != f.taskID {
		t.Fatalf("capability scope = (%q, %q), want the fixture scope", capability.PolicyDomainID, capability.TaskID)
	}
	if capability.ArtifactVersionID != versionID {
		t.Fatalf("capability version = %q, want %q", capability.ArtifactVersionID, versionID)
	}
	if capability.ManifestDigest != activated.ActivationEvidence.ManifestDigest {
		t.Fatalf("capability manifest digest = %q, want %q", capability.ManifestDigest, activated.ActivationEvidence.ManifestDigest)
	}
	if capability.AvailabilityGeneration != owner.AvailabilityGeneration {
		t.Fatalf("capability availability generation = %d, want %d", capability.AvailabilityGeneration, owner.AvailabilityGeneration)
	}
	if capability.ExpiresAt != expiry {
		t.Fatalf("capability expiry = %d, want %d", capability.ExpiresAt, expiry)
	}
	if capability.Digest != capability.CanonicalDigest() || !validDigest(capability.Digest) {
		t.Fatalf("capability digest must be canonical: %q", capability.Digest)
	}

	// Verification of the genuine capability succeeds.
	if err := f.verifyC04Capability(t, capability, owner); err != nil {
		t.Fatalf("genuine capability must verify: %v", err)
	}
}

// TestIssueC04RequiresPlatformAuthority proves C04 itself can never request
// a reconstruction capability: only the Platform (Task Orchestration
// authority) may select the exact Artifact Version (acceptance #10).
func TestIssueC04RequiresPlatformAuthority(t *testing.T) {
	f := newFixture(t)
	_, activated := f.prepareVerifyActivate(t, "op-c04-auth")
	versionID := activated.ArtifactVersionID
	owner := f.ownerScope(versionID)
	f.registerScope(versionID, owner)
	expiry := f.now + 3600

	for _, authority := range []PublicationAuthority{
		{Kind: AuthorityTaskWorkspaceLifecycle, ID: f.c04Authority, Generation: 1},
		{Kind: AuthorityRuntimeExecution, ID: f.runtimeAuthority, Generation: 1},
		{Kind: AuthorityTaskOrchestration, ID: "unknown-orchestrator", Generation: 1},
		{Kind: AuthorityRecovery, ID: f.recoveryAuthority, Generation: 1},
	} {
		_, err := f.core.Query(context.Background(), PublicationQuery{
			Kind: QueryIssueC04ReconstructionCapability, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
			ArtifactVersionID: versionID, Scope: owner, ExpiresAt: expiry,
			Authority: authority,
		})
		if !isCode(err, ErrorOwnershipDenied) {
			t.Fatalf("issuance under authority %#v = %v, want ownership denied", authority, err)
		}
	}
	// The Platform's own authority issues successfully.
	if _, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryIssueC04ReconstructionCapability, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		ArtifactVersionID: versionID, Scope: owner, ExpiresAt: expiry,
		Authority: f.platformAuthority(),
	}); err != nil {
		t.Fatalf("platform issuance must succeed: %v", err)
	}
}

// TestIssueC04RequiresExactVersionNotLatest proves C05 issues a C04
// reconstruction capability only for the exact Artifact Version selected by
// the Platform: an empty identity, an unactivated candidate, and a
// cross-workspace identity all fail closed, and no query kind accepts a
// current/latest marker (acceptance #10).
func TestIssueC04RequiresExactVersionNotLatest(t *testing.T) {
	f := newFixture(t)

	// An unactivated candidate is prepared first (stream still at revision
	// 0); it must never be issuable as a reconstruction input.
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	candidate := f.mustPrepare(t, "op-c04-candidate", set)

	_, activated := f.prepareVerifyActivate(t, "op-c04-exact")
	versionID := activated.ArtifactVersionID
	owner := f.ownerScope(versionID)
	f.registerScope(versionID, owner)
	expiry := f.now + 3600

	// No exact identity: invalid intent, never a "latest" resolution.
	_, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryIssueC04ReconstructionCapability, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		Scope: owner, ExpiresAt: expiry, Authority: f.platformAuthority(),
	})
	if !isCode(err, ErrorInvalidIntent) {
		t.Fatalf("empty version issuance = %v, want invalid intent", err)
	}

	// The unactivated candidate never issues.
	candidateOwner := f.ownerScope(candidate.ArtifactVersionID)
	f.registerScope(candidate.ArtifactVersionID, candidateOwner)
	_, err = f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryIssueC04ReconstructionCapability, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		ArtifactVersionID: candidate.ArtifactVersionID, Scope: candidateOwner, ExpiresAt: expiry,
		Authority: f.platformAuthority(),
	})
	if !isCode(err, ErrorNotFound) {
		t.Fatalf("unactivated candidate issuance = %v, want not found", err)
	}

	// A cross-workspace identity never issues.
	_, err = f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryIssueC04ReconstructionCapability, PolicyDomainID: "policy-domain-other", TaskID: f.taskID,
		ArtifactVersionID: versionID, Scope: owner, ExpiresAt: expiry,
		Authority: f.platformAuthority(),
	})
	if !isCode(err, ErrorNotFound) {
		t.Fatalf("cross-workspace issuance = %v, want not found", err)
	}
}

// TestVerifyC04ReconstructionCapabilityFailuresFailsClosed proves C04
// capability verification re-derives the current facts: tampering, an
// expired capability, a wrong publication authority, a stale availability
// generation, and a revoked scope all fail closed (acceptance #9, #10, #12).
func TestVerifyC04ReconstructionCapabilityFailuresFailsClosed(t *testing.T) {
	f := newFixture(t)
	_, activated := f.prepareVerifyActivate(t, "op-c04-verify")
	versionID := activated.ArtifactVersionID
	owner := f.ownerScope(versionID)
	f.registerScope(versionID, owner)
	expiry := f.now + 3600

	capability := f.issueC04Capability(t, versionID, owner, expiry)

	// Tampered digest.
	tampered := *capability
	tampered.ManifestDigest = testDigest("other-manifest")
	if err := f.verifyC04Capability(t, &tampered, owner); !isCode(err, ErrorIntegrityConflict) {
		t.Fatalf("tampered capability = %v, want integrity conflict", err)
	}
	// Wrong publication authority binding.
	tampered = *capability
	tampered.PublicationAuthorityID = "other-publication-authority"
	tampered.Digest = tampered.CanonicalDigest()
	if err := f.verifyC04Capability(t, &tampered, owner); !isCode(err, ErrorIntegrityConflict) {
		t.Fatalf("wrong publication authority = %v, want integrity conflict", err)
	}
	// Wrong availability generation binding.
	tampered = *capability
	tampered.AvailabilityGeneration = owner.AvailabilityGeneration + 1
	tampered.Digest = tampered.CanonicalDigest()
	if err := f.verifyC04Capability(t, &tampered, owner); !isCode(err, ErrorNotFound) {
		t.Fatalf("stale availability generation = %v, want not found", err)
	}
	// Revoked scope: the presented scope is no longer a current fact.
	f.revokeScope(versionID, owner)
	if err := f.verifyC04Capability(t, capability, owner); !isCode(err, ErrorNotFound) {
		t.Fatalf("revoked scope verification = %v, want not found", err)
	}
}

// TestC04CapabilityExpiryFailsClosed proves the capability's declared
// expiry is enforced by verification: once the diagnostic clock passes it,
// the capability fails closed and a fresh one must be issued (acceptance
// #9, #12).
func TestC04CapabilityExpiryFailsClosed(t *testing.T) {
	f := newFixture(t)
	_, activated := f.prepareVerifyActivate(t, "op-c04-expiry")
	versionID := activated.ArtifactVersionID
	owner := f.ownerScope(versionID)
	f.registerScope(versionID, owner)

	capability := f.issueC04Capability(t, versionID, owner, f.now+3600)
	if err := f.verifyC04Capability(t, capability, owner); err != nil {
		t.Fatalf("capability must verify before expiry: %v", err)
	}

	// The clock passes the expiry.
	f.now = capability.ExpiresAt + 1
	if err := f.verifyC04Capability(t, capability, owner); !isCode(err, ErrorStaleAuthority) {
		t.Fatalf("expired capability = %v, want stale authority", err)
	}

	// A fresh capability for the same exact version still works.
	fresh := f.issueC04Capability(t, versionID, owner, f.now+3600)
	if err := f.verifyC04Capability(t, fresh, owner); err != nil {
		t.Fatalf("fresh capability must verify: %v", err)
	}
}

// TestExactVersionMemberLookupScopeFailsClosed proves exact version and
// member lookup fails closed non-enumerating under a wrong authority scope
// (acceptance #4).
func TestExactVersionMemberLookupScopeFailsClosed(t *testing.T) {
	f := newFixture(t)
	_, activated := f.prepareVerifyActivate(t, "op-scoped-lookup")
	versionID := activated.ArtifactVersionID
	artifactID := memberArtifactID(t, f, versionID)
	owner := f.ownerScope(versionID)
	f.registerScope(versionID, owner)

	// With the correct scope both lookups resolve.
	if _, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryExactVersion, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		ArtifactVersionID: versionID, Scope: owner,
	}); err != nil {
		t.Fatalf("scoped exact version = %v", err)
	}
	if _, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryExactMember, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		ArtifactVersionID: versionID, ArtifactID: artifactID, Scope: owner,
	}); err != nil {
		t.Fatalf("scoped exact member = %v", err)
	}

	// Wrong scope kinds resolve to the same non-enumerating error as a
	// missing identity.
	_, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryExactVersion, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		ArtifactVersionID: "artifact-version-nonexistent",
	})
	if err == nil || !isCode(err, ErrorNotFound) {
		t.Fatalf("baseline = %v, want not found", err)
	}
	for _, scope := range []ContentScope{f.shareScope(versionID), f.breakGlassScope(versionID)} {
		for _, kind := range []PublicationQueryKind{QueryExactVersion, QueryExactMember} {
			query := PublicationQuery{
				Kind: kind, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
				ArtifactVersionID: versionID, Scope: scope,
			}
			if kind == QueryExactMember {
				query.ArtifactID = artifactID
			}
			_, scopeErr := f.core.Query(context.Background(), query)
			if scopeErr == nil || scopeErr.Error() != err.Error() {
				t.Fatalf("wrong-scope %s = %v, want the non-enumerating %q", kind, scopeErr, err.Error())
			}
		}
	}
}

// TestQueryUnionIsClosed proves the query union is the closed set of
// ordinary and content-target/C04 query kinds and unknown kinds fail
// closed.
func TestQueryUnionIsClosed(t *testing.T) {
	allKinds := []PublicationQueryKind{
		QueryOperation, QueryCandidate, QueryTaskStream, QueryExactVersion,
		QueryExactMember, QueryVersionHistory, QueryResolveContentTarget,
		QueryVerifyContentTarget, QueryIssueC04ReconstructionCapability,
		QueryVerifyC04ReconstructionCapability,
	}
	seen := make(map[PublicationQueryKind]bool, len(allKinds))
	for _, kind := range allKinds {
		if !validQueryKind(kind) {
			t.Fatalf("kind %q must be valid", kind)
		}
		if seen[kind] {
			t.Fatalf("duplicate kind %q", kind)
		}
		seen[kind] = true
	}
	for _, unknown := range []PublicationQueryKind{"current", "latest", "download_all", "share", ""} {
		if validQueryKind(unknown) {
			t.Fatalf("kind %q must be rejected by the closed union", unknown)
		}
	}
	f := newFixture(t)
	_, err := f.core.Query(context.Background(), PublicationQuery{Kind: "latest"})
	if !isCode(err, ErrorInvalidIntent) {
		t.Fatalf("unknown query kind = %v, want invalid intent", err)
	}
}

// TestContentTargetAndCapabilityLocatorFree proves the content target and
// the C04 reconstruction capability never contain or produce a path, object
// key, bucket, vendor URL, signed URL, credential, materialization locator,
// ReadHandle, or bytes, even in their wire encodings (acceptance #6).
func TestContentTargetAndCapabilityLocatorFree(t *testing.T) {
	f := newFixture(t)
	_, activated := f.prepareVerifyActivate(t, "op-ct-locator")
	versionID := activated.ArtifactVersionID
	artifactID := memberArtifactID(t, f, versionID)
	owner := f.ownerScope(versionID)
	f.registerScope(versionID, owner)

	target := f.resolveTarget(t, versionID, artifactID, owner)
	capability := f.issueC04Capability(t, versionID, owner, f.now+3600)

	encoded := string(mustMarshalJSON(t, target)) + string(mustMarshalJSON(t, capability))
	for _, canary := range canaryValues {
		if strings.Contains(encoded, canary) {
			t.Fatalf("content target/capability leaks canary %q", canary)
		}
	}
	for _, fragment := range []string{"readhandle", "read_handle", "signed", "objectkey", "object_key", "bucket", "locator"} {
		if strings.Contains(strings.ToLower(encoded), fragment) {
			t.Fatalf("content target/capability encoding exposes locator fragment %q", fragment)
		}
	}

	// A path-like logical name is rejected at prepare, so a locator can
	// never enter a member fact and therefore never enter a content target.
	pathMember := ArtifactMemberSpec{
		Slot: "slot-deck", Kind: ArtifactKindDeck, LogicalName: "s3://canary-bucket/path/key.pptx",
		MediaType: MediaTypePPTX, Size: 1024, ContentDigest: testDigest("deck-content"),
	}
	set := f.buildEvidence(t, []ArtifactMemberSpec{pathMember})
	_, err := f.core.Mutate(context.Background(), f.prepareIntent("op-ct-path", f.preparePayload("op-ct-path", set, []ArtifactMemberSpec{pathMember})))
	if !isCode(err, ErrorInvalidIntent) {
		t.Fatalf("path-like logical name = %v, want invalid intent", err)
	}
}

// TestQueryNeverCreatesDurableObjectHandle proves the content-target and
// C04 queries are pure read-only: they resolve and verify targets and
// capabilities from immutable version facts and the current availability
// fact without touching the Durable Object authority at all, and a content
// delivery flow must still perform its own mandatory access audit before
// any Durable Object open (acceptance #8).
func TestQueryNeverCreatesDurableObjectHandle(t *testing.T) {
	f := newFixture(t)
	_, activated := f.prepareVerifyActivate(t, "op-ct-do")
	versionID := activated.ArtifactVersionID
	artifactID := memberArtifactID(t, f, versionID)
	owner := f.ownerScope(versionID)
	f.registerScope(versionID, owner)

	// The Durable Object capability registry is emptied: the C05 content
	// target queries must still succeed because they never consult the
	// Durable Object authority.
	for id := range f.registry.facts {
		delete(f.registry.facts, id)
	}
	for id := range f.registry.current {
		delete(f.registry.current, id)
	}
	target := f.resolveTarget(t, versionID, artifactID, owner)
	if err := f.verifyTarget(t, target, owner); err != nil {
		t.Fatalf("target verification must not need the Durable Object authority: %v", err)
	}
	capability := f.issueC04Capability(t, versionID, owner, f.now+3600)
	if err := f.verifyC04Capability(t, capability, owner); err != nil {
		t.Fatalf("capability verification must not need the Durable Object authority: %v", err)
	}
	// The target/capability never carry a content identity, capability
	// identity, or any Durable Object handle material.
	encoded := string(mustMarshalJSON(t, target)) + string(mustMarshalJSON(t, capability))
	for _, fragment := range []string{"contentid", "content_id", "capabilityid", "capability_id", "readhandle", "handle"} {
		if strings.Contains(strings.ToLower(encoded), fragment) {
			t.Fatalf("content surface exposes Durable Object material %q", fragment)
		}
	}
	// The immutable version facts are unchanged by all of the above.
	view, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryExactVersion, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		ArtifactVersionID: versionID,
	})
	if err != nil {
		t.Fatalf("query exact version: %v", err)
	}
	if view.ManifestDigest != activated.ActivationEvidence.ManifestDigest {
		t.Fatalf("version facts changed by content queries: %#v", view)
	}
}
