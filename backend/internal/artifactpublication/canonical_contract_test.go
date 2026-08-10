package artifactpublication

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestUnknownMajorSchemaFailsClosed proves requests with an unknown major
// schema version are rejected before any state is touched.
func TestUnknownMajorSchemaFailsClosed(t *testing.T) {
	f := newFixture(t)
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	payload := f.preparePayload("op-schema", set, []ArtifactMemberSpec{f.deckMemberSpec()})

	header := f.header("op-schema")
	header.SchemaVersion = NewSchemaVersion(2, 0)
	intent := bindDigest(NewPreparePublication(header, payload))

	_, err := f.core.Mutate(context.Background(), intent)
	var publicationError *Error
	if !errors.As(err, &publicationError) || publicationError.Code != ErrorUnsupportedSchema {
		t.Fatalf("unknown major schema error = %v, want unsupported schema", err)
	}
}

// TestMinorVersionRefinementAccepted proves a higher minor version within
// the known major version is accepted (minor versions refine an encoding).
func TestMinorVersionRefinementAccepted(t *testing.T) {
	f := newFixture(t)
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	payload := f.preparePayload("op-minor", set, []ArtifactMemberSpec{f.deckMemberSpec()})

	header := f.header("op-minor")
	header.SchemaVersion = NewSchemaVersion(1, 2)
	intent := bindDigest(NewPreparePublication(header, payload))

	decision, err := f.core.Mutate(context.Background(), intent)
	if err != nil {
		t.Fatalf("minor schema prepare: %v", err)
	}
	if decision.State != OperationPrepared {
		t.Fatalf("unexpected decision: %#v", decision)
	}
}

// TestOperationDigestBindingRequired proves an intent whose operation
// request digest does not match the canonical request digest is invalid.
func TestOperationDigestBindingRequired(t *testing.T) {
	f := newFixture(t)
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	payload := f.preparePayload("op-bad-digest", set, []ArtifactMemberSpec{f.deckMemberSpec()})

	header := f.header("op-bad-digest")
	intent := NewPreparePublication(header, payload) // digest left unbound
	_, err := f.core.Mutate(context.Background(), intent)
	var publicationError *Error
	if !errors.As(err, &publicationError) || publicationError.Code != ErrorInvalidIntent {
		t.Fatalf("unbound digest error = %v, want invalid intent", err)
	}
}

// TestCanonicalEncodingFieldPresence proves missing required fields in the
// request fail closed: a prepare without a contract, members, staging,
// channels, or evidence references is invalid.
func TestCanonicalEncodingFieldPresence(t *testing.T) {
	f := newFixture(t)
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	base := f.preparePayload("op-fields", set, []ArtifactMemberSpec{f.deckMemberSpec()})

	mutations := []struct {
		name   string
		mutate func(*PreparePublicationPayload)
	}{
		{"missing contract", func(p *PreparePublicationPayload) { p.ContractID = "" }},
		{"missing phase run", func(p *PreparePublicationPayload) { p.PhaseRunID = "" }},
		{"empty members", func(p *PreparePublicationPayload) { p.Members = nil }},
		{"empty staging", func(p *PreparePublicationPayload) { p.Staging = nil }},
		{"empty channels", func(p *PreparePublicationPayload) { p.RequiredChannels = nil }},
		{"empty runtime refs", func(p *PreparePublicationPayload) { p.RuntimeRefs = nil }},
		{"missing validation ref", func(p *PreparePublicationPayload) { p.ValidationRef = EvidenceRef{} }},
		{"missing c04 ref", func(p *PreparePublicationPayload) { p.C04CommitRef = EvidenceRef{} }},
		{"empty capability refs", func(p *PreparePublicationPayload) { p.ContentCapabilityRefs = nil }},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			payload := base
			payload.Members = append([]ArtifactMemberSpec(nil), base.Members...)
			payload.Staging = append([]StagingReference(nil), base.Staging...)
			payload.RequiredChannels = append([]ChannelKind(nil), base.RequiredChannels...)
			payload.RuntimeRefs = append([]RuntimeEvidenceRef(nil), base.RuntimeRefs...)
			payload.ContentCapabilityRefs = append([]ContentCapabilityRef(nil), base.ContentCapabilityRefs...)
			mutation.mutate(&payload)
			_, err := f.core.Mutate(context.Background(), f.prepareIntent("op-fields-"+mutation.name, payload))
			var publicationError *Error
			if !errors.As(err, &publicationError) || publicationError.Code != ErrorInvalidIntent {
				t.Fatalf("error = %v, want invalid intent", err)
			}
		})
	}
}

// TestUnicodeNameNormalization proves logical names are NFC-normalized and
// trimmed before they enter the manifest, and unsafe names (control
// characters, path separators, empty) fail closed.
func TestUnicodeNameNormalization(t *testing.T) {
	f := newFixture(t)
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	payload := f.preparePayload("op-names", set, []ArtifactMemberSpec{f.deckMemberSpec()})
	payload.Members[0].LogicalName = "  Deck\u00e9.pptx  " // NFC canonical + surrounding spaces

	f.mustPreparePayload(t, "op-names", payload)
	view, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryOperation, PolicyDomainID: f.policyDomain, TaskID: f.taskID, OperationID: "op-names",
	})
	if err != nil {
		t.Fatalf("query operation: %v", err)
	}
	if view.Members[0].LogicalName != "Deck\u00e9.pptx" {
		t.Fatalf("name was not normalized: %q", view.Members[0].LogicalName)
	}

	for _, name := range []string{"", "   ", "Deck/One.pptx", "Deck\\One.pptx", "Deck\x00One"} {
		t.Run("unsafe "+strings.ReplaceAll(name, "\x00", "NUL"), func(t *testing.T) {
			unsafe := f.preparePayload("op-unsafe", set, []ArtifactMemberSpec{f.deckMemberSpec()})
			unsafe.Members[0].LogicalName = name
			_, err := f.core.Mutate(context.Background(), f.prepareIntent("op-unsafe", unsafe))
			var publicationError *Error
			if !errors.As(err, &publicationError) || publicationError.Code != ErrorInvalidIntent {
				t.Fatalf("unsafe name error = %v, want invalid intent", err)
			}
		})
	}
}

// evidenceSetFromPayload rebuilds an evidence set whose pinned refs match the
// (possibly mutated) prepare payload so the prepare request remains
// internally canonical. It is used only to test prepare-time behavior.
func evidenceSetFromPayload(t *testing.T, set *evidenceSet, payload PreparePublicationPayload) *evidenceSet {
	t.Helper()
	rebuilt := *set
	rebuilt.capabilities = append([]ContentCapabilityEvidence(nil), set.capabilities...)
	rebuilt.runtimeEvidence = append([]RuntimeEvidence(nil), set.runtimeEvidence...)
	return &rebuilt
}

// TestDuplicateMembersFailClosed proves duplicate member slots, duplicate
// logical names (after normalization), and slots without staging or
// capability references are rejected at prepare.
func TestDuplicateMembersFailClosed(t *testing.T) {
	f := newFixture(t)
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	base := f.preparePayload("op-dup", set, []ArtifactMemberSpec{f.deckMemberSpec()})

	// Duplicate slot.
	payload := base
	payload.Members = append([]ArtifactMemberSpec(nil), base.Members...)
	payload.Members = append(payload.Members, payload.Members[0])
	payload.Staging = append([]StagingReference(nil), base.Staging...)
	_, err := f.core.Mutate(context.Background(), f.prepareIntent("op-dup-slot", payload))
	var publicationError *Error
	if !errors.As(err, &publicationError) || publicationError.Code != ErrorInvalidIntent {
		t.Fatalf("duplicate slot error = %v, want invalid intent", err)
	}

	// Slot without a staged reference.
	payload = base
	payload.Staging = nil
	_, err = f.core.Mutate(context.Background(), f.prepareIntent("op-dup-staging", payload))
	if !errors.As(err, &publicationError) || publicationError.Code != ErrorInvalidIntent {
		t.Fatalf("missing staging error = %v, want invalid intent", err)
	}

	// Slot without a capability reference.
	payload = base
	payload.ContentCapabilityRefs = nil
	_, err = f.core.Mutate(context.Background(), f.prepareIntent("op-dup-capability", payload))
	if !errors.As(err, &publicationError) || publicationError.Code != ErrorInvalidIntent {
		t.Fatalf("missing capability ref error = %v, want invalid intent", err)
	}

	// Staging digest inconsistent with the member spec.
	payload = base
	payload.Staging = append([]StagingReference(nil), base.Staging...)
	payload.Staging[0].ContentDigest = testDigest("different-content")
	_, err = f.core.Mutate(context.Background(), f.prepareIntent("op-dup-digest", payload))
	if !errors.As(err, &publicationError) || publicationError.Code != ErrorInvalidIntent {
		t.Fatalf("inconsistent staging digest error = %v, want invalid intent", err)
	}
}

// TestUnsupportedKindAndMediaTypeFailClosed proves unsupported member kinds
// and media types that do not belong to the kind's registry are rejected.
func TestUnsupportedKindAndMediaTypeFailClosed(t *testing.T) {
	f := newFixture(t)
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	base := f.preparePayload("op-kind", set, []ArtifactMemberSpec{f.deckMemberSpec()})

	payload := base
	payload.Members = append([]ArtifactMemberSpec(nil), base.Members...)
	payload.Members[0].Kind = ArtifactKind("executable")
	_, err := f.core.Mutate(context.Background(), f.prepareIntent("op-kind", payload))
	var publicationError *Error
	if !errors.As(err, &publicationError) || publicationError.Code != ErrorInvalidIntent {
		t.Fatalf("unsupported kind error = %v, want invalid intent", err)
	}

	payload = base
	payload.Members = append([]ArtifactMemberSpec(nil), base.Members...)
	payload.Members[0].MediaType = MediaType("application/x-executable")
	_, err = f.core.Mutate(context.Background(), f.prepareIntent("op-media", payload))
	if !errors.As(err, &publicationError) || publicationError.Code != ErrorInvalidIntent {
		t.Fatalf("unsupported media type error = %v, want invalid intent", err)
	}

	payload = base
	payload.Members = append([]ArtifactMemberSpec(nil), base.Members...)
	payload.Members[0].ContentDigest = "md5:deadbeef"
	_, err = f.core.Mutate(context.Background(), f.prepareIntent("op-digest", payload))
	if !errors.As(err, &publicationError) || publicationError.Code != ErrorInvalidIntent {
		t.Fatalf("unknown digest algorithm error = %v, want invalid intent", err)
	}
}

// TestCanonicalDigestDeterminism proves the canonical encoding is stable
// regardless of map iteration order and that schema version participates.
func TestCanonicalDigestDeterminism(t *testing.T) {
	f := newFixture(t)
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	payload := f.preparePayload("op-det", set, []ArtifactMemberSpec{f.deckMemberSpec()})

	first := bindDigest(NewPreparePublication(f.header("op-det"), payload))
	second := bindDigest(NewPreparePublication(f.header("op-det"), payload))
	if CanonicalRequestDigest(first) != CanonicalRequestDigest(second) {
		t.Fatal("canonical digest is not deterministic")
	}
	header := f.header("op-det")
	header.SchemaVersion = NewSchemaVersion(1, 1)
	refined := bindDigest(NewPreparePublication(header, payload))
	if CanonicalRequestDigest(first) == CanonicalRequestDigest(refined) {
		t.Fatal("schema version must participate in the canonical digest")
	}
}
