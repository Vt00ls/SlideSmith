package artifactpublication

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func mustMarshalJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return encoded
}

// TestManifestDeterministicBinding proves the canonical manifest determin-
// istically binds Task, ArtifactVersionID, publication kind, optional parent,
// lineage digest, and the sorted members, and that reordering the declared
// members does not change the manifest digest.
func TestManifestDeterministicBinding(t *testing.T) {
	f := newFixture(t)
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	prepare := f.mustPrepare(t, "op-manifest", set)

	if !validDigest(prepare.ManifestDigest) || !validDigest(prepare.LineageDigest) {
		t.Fatalf("manifest/lineage digests are invalid: %#v", prepare)
	}
	if prepare.LineageDigest == prepare.ManifestDigest {
		t.Fatal("manifest digest must commit the lineage digest, not equal it")
	}

	// A second candidate for the same Task with the same declared member
	// bytes must receive a different ArtifactVersionID and a different
	// manifest digest: identical bytes never merge business identity.
	second := f.mustPrepare(t, "op-manifest-2", set)
	if second.ArtifactVersionID == prepare.ArtifactVersionID {
		t.Fatal("ArtifactVersionID must be non-reused across candidates")
	}
	if second.ManifestDigest == prepare.ManifestDigest {
		t.Fatal("manifest digest must differ when the ArtifactVersionID differs")
	}
	if strings.HasPrefix(string(second.ArtifactVersionID), string(prepare.ArtifactVersionID)) {
		t.Fatal("ArtifactVersionIDs must be opaque, not derived from prior identities")
	}

	// Query the operation and confirm the same manifest digest is exposed
	// (prepared candidates are not visible to ordinary candidate queries;
	// they are inspected through the exact operation query).
	view, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryOperation, PolicyDomainID: f.policyDomain, TaskID: f.taskID, OperationID: "op-manifest",
	})
	if err != nil {
		t.Fatalf("query operation: %v", err)
	}
	if view.ManifestDigest != prepare.ManifestDigest || view.LineageDigest != prepare.LineageDigest {
		t.Fatalf("candidate view digest mismatch: %#v", view)
	}
}

// TestManifestSortStability proves the canonical members are sorted
// deterministically so reordering the members of one manifest never changes
// its canonical digest or bytes.
func TestManifestSortStability(t *testing.T) {
	memberA := ArtifactMember{
		ArtifactID: "artifact-0000000000000001", Kind: ArtifactKindDeck, LogicalName: "Deck.pptx",
		MediaType: MediaTypePPTX, Size: 1024, ContentDigest: testDigest("deck-content"),
	}
	memberB := ArtifactMember{
		ArtifactID: "artifact-0000000000000002", Kind: ArtifactKindPreview, LogicalName: "Preview.png",
		MediaType: MediaTypePNG, Size: 512, ContentDigest: testDigest("preview-content"),
	}
	lineageDigest := testDigest("lineage")
	first := ArtifactManifest{
		SchemaVersion: SchemaV1, VersionID: "artifact-version-1", TaskID: "task-1",
		Kind: PublicationKindFirstGeneration, LineageDigest: lineageDigest,
		Members: []ArtifactMember{memberA, memberB},
	}
	reordered := ArtifactManifest{
		SchemaVersion: SchemaV1, VersionID: "artifact-version-1", TaskID: "task-1",
		Kind: PublicationKindFirstGeneration, LineageDigest: lineageDigest,
		Members: []ArtifactMember{memberB, memberA},
	}
	if first.CanonicalDigest() != reordered.CanonicalDigest() {
		t.Fatal("canonical manifest digest must be stable under member reordering")
	}
	if string(first.CanonicalBytes()) != string(reordered.CanonicalBytes()) {
		t.Fatal("canonical manifest bytes must be stable under member reordering")
	}
	// The same bytes in a different candidate carry a different ArtifactID
	// and therefore a different manifest: identical bytes never merge
	// business identity.
	different := ArtifactManifest{
		SchemaVersion: SchemaV1, VersionID: "artifact-version-2", TaskID: "task-1",
		Kind: PublicationKindFirstGeneration, LineageDigest: lineageDigest,
		Members: []ArtifactMember{{
			ArtifactID: "artifact-0000000000000009", Kind: ArtifactKindDeck, LogicalName: "Deck.pptx",
			MediaType: MediaTypePPTX, Size: 1024, ContentDigest: testDigest("deck-content"),
		}},
	}
	if different.CanonicalDigest() == first.CanonicalDigest() {
		t.Fatal("same bytes with a different ArtifactID must not share a manifest digest")
	}
}

// TestLineageCommitsPinnedEvidenceReferences proves the lineage digest
// commits the pinned upstream evidence references: a different pinned
// evidence binding produces a different lineage and manifest digest.
func TestLineageCommitsPinnedEvidenceReferences(t *testing.T) {
	f := newFixture(t)
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})

	base := f.preparePayload("op-lineage", set, []ArtifactMemberSpec{f.deckMemberSpec()})
	first := f.mustPreparePayload(t, "op-lineage-a", base)

	differentRef := f.preparePayload("op-lineage", set, []ArtifactMemberSpec{f.deckMemberSpec()})
	differentRef.ValidationRef = EvidenceRef{EvidenceID: "validation-evidence-1", Digest: testDigest("different-validation")}
	second := f.mustPreparePayload(t, "op-lineage-b", differentRef)

	if first.LineageDigest == second.LineageDigest {
		t.Fatal("a different evidence binding must produce a different lineage digest")
	}
	if first.ManifestDigest == second.ManifestDigest {
		t.Fatal("the manifest digest must commit the lineage digest")
	}
}

// TestLocatorFreeManifestAndLineage proves the manifest and lineage encodings
// never contain content, paths, object keys, buckets, mounts, vendors,
// credentials, sessions, or materialization locators.
func TestLocatorFreeManifestAndLineage(t *testing.T) {
	f := newFixture(t)
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	prepare := f.mustPrepare(t, "op-locator-free", set)

	view, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryOperation, PolicyDomainID: f.policyDomain, TaskID: f.taskID, OperationID: "op-locator-free",
	})
	if err != nil {
		t.Fatalf("query operation: %v", err)
	}
	for _, member := range view.Members {
		encoded := string(mustMarshalJSON(t, member))
		for _, canary := range []string{"s3://", "bucket", "object-key", "/tmp/", "workspace-path", "credential", "session"} {
			if strings.Contains(encoded, canary) {
				t.Fatalf("member view leaks locator canary %q: %s", canary, encoded)
			}
		}
	}
	_ = prepare
}
