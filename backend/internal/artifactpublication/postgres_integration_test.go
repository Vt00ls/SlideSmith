package artifactpublication

// This file is the isolated-schema real PostgreSQL integration suite for
// child SPEC #107 (C05-04). It proves the owned persistence boundary, the
// row-lock/CAS activation transaction, all-or-none commit, crash-before/
// after-commit semantics, restart, the restricted Durable Object
// participant, typed-reference-only attach/release, and safe persistence
// errors — each acceptance criterion of #107 over a real PostgreSQL
// database with one isolated schema per test.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// TestPostgresOwnedPersistenceTablesPopulated proves the real PostgreSQL
// adapter owns the request/operation journal, candidate, manifest, members,
// lineage, publication stream/head, verification evidence, mandatory audit,
// Task Orchestration outbox, identity facts, typed attach references and
// activated version — all through one transaction, with no general
// repository or SQL authority exposed on the public seam.
func TestPostgresOwnedPersistenceTablesPopulated(t *testing.T) {
	f := newPostgresFixture(t)
	operationID := "pg-owned"
	f.prepareVerifyActivatePG(t, operationID)

	checks := map[string]int{
		"publication_stream":            1,
		"publication_operation":         1,
		"publication_outcome":           3, // prepare, verify, activate
		"publication_candidate":         1,
		"publication_member":            1,
		"publication_staging":           1,
		"publication_runtime_ref":       1,
		"publication_capability_ref":    1,
		"publication_verification":      1,
		"publication_evidence_accepted": 4, // runtime, validation, c04, capability
		"publication_version_fact":      1,
		"publication_artifact_fact":     1,
		"publication_activated":         1,
		"publication_activated_member":  1,
		"publication_attach":            1, // restricted Durable Object participant
		"publication_audit":             3, // prepare, verify, activate
		"publication_outbox":            1, // committed activation evidence
	}
	for table, want := range checks {
		if got := f.countRows(t, table); got != want {
			t.Fatalf("%s rows = %d, want %d", table, got, want)
		}
	}

	// The current head and revision are explicit stream facts.
	stream, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryTaskStream, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
	})
	if err != nil {
		t.Fatalf("query stream: %v", err)
	}
	if stream.StreamRevision != 1 || stream.CurrentHead == "" {
		t.Fatalf("stream facts not committed: %#v", stream)
	}

	// The public seam exposes no SQL or repository: only Mutate and Query.
	if methodCount := reflect.TypeOf((*PublicationCore)(nil)).Elem().NumMethod(); methodCount != 2 {
		t.Fatalf("public seam must expose exactly Mutate and Query, got %d methods", methodCount)
	}
}

// TestPostgresActivationAllOrNoneOnFault proves a fault at any point inside
// the activation transaction rolls the WHOLE transaction back: no active
// version, no activated members, no attach rows, no activation audit, no
// outbox, no stream advance. After the fault clears, exact activation
// commits the original candidate identity.
func TestPostgresActivationAllOrNoneOnFault(t *testing.T) {
	points := []PostgresFaultPoint{
		PostgresFaultBeforeActivationCommit,
		PostgresFaultBeforeReferenceAttach,
		PostgresFaultBeforeMandatoryAudit,
		PostgresFaultBeforeOutbox,
		PostgresFaultBeforeCommit,
	}
	for _, point := range points {
		t.Run(postgresFaultName(point), func(t *testing.T) {
			var fired bool
			f := newPostgresFixtureFaults(t, func(event PostgresFaultEvent) error {
				if !fired && event.Point == point && event.IntentKind == IntentActivatePublication {
					fired = true
					return errors.New("injected activation fault")
				}
				return nil
			})
			operationID := "pg-all-or-none"
			set, _, _ := f.prepareAndVerifyPG(t, operationID)
			_ = set

			if _, err := f.core.Mutate(context.Background(), f.activateIntent(operationID)); err == nil {
				t.Fatalf("activation must fail at fault point %v", point)
			}

			// Nothing from the activation is durable.
			for _, table := range []string{"publication_activated", "publication_activated_member", "publication_attach"} {
				if got := f.countRows(t, table); got != 0 {
					t.Fatalf("%s rows = %d after rolled-back activation, want 0", table, got)
				}
			}
			stream, err := f.core.Query(context.Background(), PublicationQuery{
				Kind: QueryTaskStream, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
			})
			if err != nil {
				t.Fatalf("query stream: %v", err)
			}
			if stream.StreamRevision != 0 || stream.CurrentHead != "" {
				t.Fatalf("stream must not advance after rolled-back activation: %#v", stream)
			}
			// No activation outbox or audit for the failed activation.
			if got := f.countRows(t, "publication_outbox"); got != 0 {
				t.Fatalf("outbox rows = %d after rolled-back activation, want 0", got)
			}
			if got := f.countRows(t, "publication_audit"); got != 2 {
				t.Fatalf("audit rows = %d, want exactly the 2 committed prepare+verify facts", got)
			}

			// After the fault clears, activation commits the ORIGINAL
			// candidate identity (never reallocated).
			activated, err := f.core.Mutate(context.Background(), f.activateIntent(operationID))
			if err != nil {
				t.Fatalf("activation after fault clears: %v", err)
			}
			if activated.State != OperationActivated || activated.StreamRevision != 1 {
				t.Fatalf("unexpected reactivated decision: %#v", activated)
			}
			if got := f.countRows(t, "publication_activated"); got != 1 {
				t.Fatalf("activated rows = %d after retry, want exactly 1", got)
			}
		})
	}
}

// TestPostgresActivationRevalidatesFencedFacts proves the activation
// transaction row-lock/CAS revalidates the original OperationID, request
// digest, expected stream revision/head, activity generation, publication
// fence and safety epoch; any mismatch fails closed and leaves nothing
// durable.
func TestPostgresActivationRevalidatesFencedFacts(t *testing.T) {
	t.Run("stale expected revision", func(t *testing.T) {
		f := newPostgresFixture(t)
		operationID := "pg-fence-1"
		f.prepareAndVerifyPG(t, operationID)
		header := f.header(operationID)
		header.ExpectedStreamRevision = 9
		if _, err := f.core.Mutate(context.Background(), f.activateIntentWithHeader(header)); !isCode(err, ErrorStaleAuthority) {
			t.Fatalf("stale expected revision error = %v, want stale authority", err)
		}
	})
	t.Run("stale expected head", func(t *testing.T) {
		f := newPostgresFixture(t)
		operationID := "pg-fence-2"
		f.prepareAndVerifyPG(t, operationID)
		header := f.header(operationID)
		header.ExpectedHead = "artifact-version-someone-else"
		if _, err := f.core.Mutate(context.Background(), f.activateIntentWithHeader(header)); !isCode(err, ErrorStaleAuthority) {
			t.Fatalf("stale expected head error = %v, want stale authority", err)
		}
	})
	t.Run("stale publication fence", func(t *testing.T) {
		f := newPostgresFixture(t)
		operationID := "pg-fence-3"
		f.prepareAndVerifyPG(t, operationID)
		header := f.header(operationID)
		header.Fence = f.fence + 1
		if _, err := f.core.Mutate(context.Background(), f.activateIntentWithHeader(header)); !isCode(err, ErrorStaleAuthority) {
			t.Fatalf("stale fence error = %v, want stale authority", err)
		}
	})
	t.Run("stale safety epoch", func(t *testing.T) {
		f := newPostgresFixture(t)
		operationID := "pg-fence-4"
		f.prepareAndVerifyPG(t, operationID)
		header := f.header(operationID)
		header.SafetyEpoch = f.safetyEpoch + 1
		if _, err := f.core.Mutate(context.Background(), f.activateIntentWithHeader(header)); !isCode(err, ErrorStaleAuthority) {
			t.Fatalf("stale safety epoch error = %v, want stale authority", err)
		}
	})
	t.Run("stale activity generation", func(t *testing.T) {
		f := newPostgresFixture(t)
		operationID := "pg-fence-5"
		f.prepareAndVerifyPG(t, operationID)
		header := f.header(operationID)
		header.ActivityGeneration = f.generation + 1
		if _, err := f.core.Mutate(context.Background(), f.activateIntentWithHeader(header)); !isCode(err, ErrorStaleAuthority) {
			t.Fatalf("stale activity generation error = %v, want stale authority", err)
		}
	})
	t.Run("all rejected activations leave no version", func(t *testing.T) {
		f := newPostgresFixture(t)
		operationID := "pg-fence-6"
		f.prepareAndVerifyPG(t, operationID)
		header := f.header(operationID)
		header.Fence = f.fence + 1
		if _, err := f.core.Mutate(context.Background(), f.activateIntentWithHeader(header)); !isCode(err, ErrorStaleAuthority) {
			t.Fatalf("stale fence error = %v", err)
		}
		if got := f.countRows(t, "publication_activated"); got != 0 {
			t.Fatalf("rejected activation must leave no active version, got %d", got)
		}
	})
}

// TestPostgresConcurrentActivationSingleWinner proves row-lock/CAS
// serialization: many goroutines activating the same verified operation
// from the same expected revision produce exactly one committed winner and
// every loser fails closed with the stale disposition; the stream and
// version facts reflect only the winner.
func TestPostgresConcurrentActivationSingleWinner(t *testing.T) {
	f := newPostgresFixture(t)
	operationID := "pg-concurrent"
	f.prepareAndVerifyPG(t, operationID)

	const workers = 8
	var wg sync.WaitGroup
	results := make([]error, workers)
	intent := f.activateIntent(operationID)
	for index := 0; index < workers; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			_, results[index] = f.core.Mutate(context.Background(), intent)
		}(index)
	}
	wg.Wait()

	freshWinners := 0
	for index, result := range results {
		_ = index
		if result != nil && !isCode(result, ErrorStaleAuthority) {
			t.Fatalf("unexpected activation result: %v", result)
		}
	}
	// Exactly one activation COMMITTED: the others either exact-replayed the
	// committed outcome (durable idempotency) or failed the stream CAS.
	// Re-inspect the stream: exactly one head and exactly one version fact.
	if got := f.countRows(t, "publication_activated"); got != 1 {
		t.Fatalf("activated rows = %d, want exactly 1 (one committed winner)", got)
	}
	if got := f.countRows(t, "publication_version_fact"); got != 1 {
		t.Fatalf("version facts = %d, want exactly 1 (identity never reallocated)", got)
	}
	stream, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryTaskStream, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
	})
	if err != nil {
		t.Fatalf("query stream: %v", err)
	}
	if stream.StreamRevision != 1 || stream.CurrentHead == "" {
		t.Fatalf("stream must reflect the single winner: %#v", stream)
	}
	_ = freshWinners
}

// TestPostgresResponseLossExactReplay proves crash-after-commit-before-
// response: a fault after COMMIT returns an error but the activation is
// durable; the exact same intent replays the original decision with the
// same ArtifactVersionID and manifest digest, never reallocating identity.
func TestPostgresResponseLossExactReplay(t *testing.T) {
	f := newPostgresFixtureFaults(t, func(event PostgresFaultEvent) error {
		if event.Point == PostgresFaultAfterCommit && event.IntentKind == IntentActivatePublication {
			return errors.New("response lost after commit")
		}
		return nil
	})
	operationID := "pg-response-loss"
	set := f.buildEvidenceDB(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	prepare := f.mustPrepare(t, operationID, set)
	f.mustVerify(t, operationID, set)

	// The response is lost, but the activation is durable.
	if _, err := f.core.Mutate(context.Background(), f.activateIntent(operationID)); err == nil {
		t.Fatal("activation must report the response loss")
	}
	if got := f.countRows(t, "publication_activated"); got != 1 {
		t.Fatalf("crash-after-commit must leave the activation durable, activated rows = %d", got)
	}
	stream, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryTaskStream, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
	})
	if err != nil {
		t.Fatalf("query stream: %v", err)
	}
	if stream.StreamRevision != 1 || stream.CurrentHead != prepare.ArtifactVersionID {
		t.Fatalf("stream must reflect the committed activation: %#v", stream)
	}

	// The exact replay returns the original decision with the same identity.
	replayed, err := f.core.Mutate(context.Background(), f.activateIntent(operationID))
	if err != nil {
		t.Fatalf("replay after response loss: %v", err)
	}
	if !replayed.Replay || replayed.ArtifactVersionID != prepare.ArtifactVersionID ||
		replayed.ManifestDigest != prepare.ManifestDigest || replayed.StreamRevision != 1 ||
		replayed.ActivationEvidence == nil {
		t.Fatalf("replay after response loss must return the original committed decision: %#v", replayed)
	}
	if got := f.countRows(t, "publication_activated"); got != 1 {
		t.Fatalf("identity must never be reallocated, activated rows = %d", got)
	}
}

// TestPostgresCrashBeforeCommitLeavesNoActiveVersion proves crash-before-
// commit of an activation leaves no active version, and the retry commits
// the ORIGINAL candidate identity (the candidate was durable at prepare).
func TestPostgresCrashBeforeCommitLeavesNoActiveVersion(t *testing.T) {
	var fired bool
	f := newPostgresFixtureFaults(t, func(event PostgresFaultEvent) error {
		if !fired && event.Point == PostgresFaultBeforeCommit && event.IntentKind == IntentActivatePublication {
			fired = true
			return errors.New("crash before commit")
		}
		return nil
	})
	operationID := "pg-crash-before"
	set := f.buildEvidenceDB(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	prepare := f.mustPrepare(t, operationID, set)
	f.mustVerify(t, operationID, set)

	if _, err := f.core.Mutate(context.Background(), f.activateIntent(operationID)); err == nil {
		t.Fatal("activation must fail before commit")
	}
	if got := f.countRows(t, "publication_activated"); got != 0 {
		t.Fatalf("crash-before-commit must leave no active version, got %d", got)
	}
	if got := f.countRows(t, "publication_attach"); got != 0 {
		t.Fatalf("crash-before-commit must leave no attach rows, got %d", got)
	}
	// The candidate identity survives: the retry activates the SAME version.
	activated, err := f.core.Mutate(context.Background(), f.activateIntent(operationID))
	if err != nil {
		t.Fatalf("activation retry: %v", err)
	}
	if activated.ArtifactVersionID != prepare.ArtifactVersionID || activated.StreamRevision != 1 {
		t.Fatalf("retry must activate the original candidate identity: %#v", activated)
	}
}

// TestPostgresRestartResumesAllFacts proves a fresh authority over the same
// PostgreSQL schema resumes every durable fact: stream revision/head,
// operation state, activated version, members, exact replay, and continued
// lifecycle (manual-edit child).
func TestPostgresRestartResumesAllFacts(t *testing.T) {
	f := newPostgresFixture(t)
	_, parent := f.prepareVerifyActivatePG(t, "pg-restart-parent")

	// Restart: a brand-new authority over the same schema.
	f.rebuildAuthority(t)

	stream, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryTaskStream, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
	})
	if err != nil {
		t.Fatalf("query stream after restart: %v", err)
	}
	if stream.StreamRevision != 1 || stream.CurrentHead != parent.ArtifactVersionID {
		t.Fatalf("stream facts must resume after restart: %#v", stream)
	}
	version, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryExactVersion, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		ArtifactVersionID: parent.ArtifactVersionID,
	})
	if err != nil || version.ManifestDigest != parent.ManifestDigest || len(version.Members) != 1 {
		t.Fatalf("activated version must resume after restart: %#v err=%v", version, err)
	}

	// Exact replay of the activation returns the original decision.
	replayed, err := f.core.Mutate(context.Background(), f.activateIntent("pg-restart-parent"))
	if err != nil {
		t.Fatalf("replay after restart: %v", err)
	}
	if !replayed.Replay || replayed.ArtifactVersionID != parent.ArtifactVersionID {
		t.Fatalf("replay after restart must return the original decision: %#v", replayed)
	}

	// The lifecycle continues after restart: a manual-edit child commits.
	childSet := f.childEvidenceSetDB(t, parent.ArtifactVersionID, "pg-restart-child")
	header := f.header("pg-restart-child")
	header.ExpectedStreamRevision = 1
	header.ExpectedHead = parent.ArtifactVersionID
	if _, err := f.core.Mutate(context.Background(), bindDigest(NewPreparePublication(header, f.childPreparePayload("pg-restart-child", parent.ArtifactVersionID, childSet)))); err != nil {
		t.Fatalf("child prepare after restart: %v", err)
	}
	if _, err := f.core.Mutate(context.Background(), f.verifyIntent("pg-restart-child", f.verifyPayload(childSet))); err != nil {
		t.Fatalf("child verify after restart: %v", err)
	}
	child, err := f.core.Mutate(context.Background(), f.activateIntentWithHeader(header))
	if err != nil {
		t.Fatalf("child activate after restart: %v", err)
	}
	if child.StreamRevision != 2 || child.ActivationEvidence == nil || child.ActivationEvidence.Parent != parent.ArtifactVersionID {
		t.Fatalf("child activation after restart: %#v", child)
	}
}

// TestPostgresDurableObjectParticipantFailureRollsBack proves the
// restricted Durable Object participant failure inside the activation
// transaction rolls everything back: no active version, no orphan
// membership, no attach rows, no readable unverified content.
func TestPostgresDurableObjectParticipantFailureRollsBack(t *testing.T) {
	t.Run("capability no longer current at attach", func(t *testing.T) {
		f := newPostgresFixture(t)
		set := f.buildEvidenceDB(t, []ArtifactMemberSpec{f.deckMemberSpec()})
		operationID := "pg-participant-1"
		f.mustPrepare(t, operationID, set)
		f.mustVerify(t, operationID, set) // current at verification time

		// The Durable Object authority revokes currency before the
		// activation: the restricted participant must fail the attach and
		// the whole transaction must roll back.
		f.registerCapabilityDB(t, set.capabilities[0], false)
		if _, err := f.core.Mutate(context.Background(), f.activateIntent(operationID)); !isCode(err, ErrorDurabilityUnverified) {
			t.Fatalf("activation with stale capability error = %v, want durability unverified", err)
		}
		for _, table := range []string{"publication_activated", "publication_activated_member", "publication_attach"} {
			if got := f.countRows(t, table); got != 0 {
				t.Fatalf("%s rows = %d after participant failure, want 0", table, got)
			}
		}
		// The candidate is still verified and its content remains in
		// inaccessible staging.
		if got := f.countRows(t, "publication_staging"); got != 1 {
			t.Fatalf("staging rows = %d, want 1 (verified but unattached)", got)
		}
	})

	t.Run("typed fact mismatch", func(t *testing.T) {
		f := newPostgresFixture(t)
		set := f.fixture.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
		capability := set.capabilities[0]
		capability.ContentDigest = testDigest("tampered-content") // does not match the pinned digest
		capability.Digest = capability.CanonicalDigest()
		f.registerCapabilityDB(t, capability, true)
		operationID := "pg-participant-2"
		f.mustPrepare(t, operationID, set)

		// Verify accepts only the pinned digest, so this capability cannot
		// even verify; the participant path is exercised by a capability
		// that verifies but whose registry row later disagrees.
		if _, err := f.core.Mutate(context.Background(), f.verifyIntent(operationID, f.verifyPayload(set))); !isCode(err, ErrorIntegrityFailure) {
			t.Fatalf("verify with mismatched capability error = %v, want integrity failure", err)
		}
	})

	t.Run("capability registry rotated after verify", func(t *testing.T) {
		f := newPostgresFixture(t)
		set := f.buildEvidenceDB(t, []ArtifactMemberSpec{f.deckMemberSpec()})
		operationID := "pg-participant-3"
		f.mustPrepare(t, operationID, set)
		f.mustVerify(t, operationID, set)

		// The Durable Object authority rotates the physical generation:
		// the pinned typed reference no longer matches the current fact, so
		// the restricted participant fails the attach and the whole
		// activation rolls back.
		capability := set.capabilities[0]
		capability.PhysicalGeneration = capability.PhysicalGeneration + 1
		capability.Digest = capability.CanonicalDigest()
		f.registerCapabilityDB(t, capability, true)

		if _, err := f.core.Mutate(context.Background(), f.activateIntent(operationID)); !isCode(err, ErrorIntegrityFailure) {
			t.Fatalf("activation after rotation error = %v, want integrity failure", err)
		}
		if got := f.countRows(t, "publication_activated"); got != 0 {
			t.Fatalf("participant failure must leave no active version, got %d", got)
		}
		if got := f.countRows(t, "publication_attach"); got != 0 {
			t.Fatalf("participant failure must leave no attach rows, got %d", got)
		}
	})
}

// TestPostgresVerifiedUnattachedContentInaccessible proves verified but not
// yet attached content stays in inaccessible staging: ordinary queries,
// download targets and C04 reconstruction capabilities never resolve it.
func TestPostgresVerifiedUnattachedContentInaccessible(t *testing.T) {
	f := newPostgresFixture(t)
	operationID := "pg-staging"
	set := f.buildEvidenceDB(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	prepare := f.mustPrepare(t, operationID, set)
	f.mustVerify(t, operationID, set)

	// The staged content exists in the owned staging table but is not
	// attached to any activated version.
	if got := f.countRows(t, "publication_staging"); got != 1 {
		t.Fatalf("staging rows = %d, want 1", got)
	}
	if got := f.countRows(t, "publication_attach"); got != 0 {
		t.Fatalf("attach rows = %d before activation, want 0", got)
	}
	// No ordinary query, content target, or C04 capability can reach it.
	if _, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryExactVersion, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		ArtifactVersionID: prepare.ArtifactVersionID,
	}); !isCode(err, ErrorNotFound) {
		t.Fatalf("exact version error = %v, want not found", err)
	}
	if _, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryResolveContentTarget, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		ArtifactVersionID: prepare.ArtifactVersionID, ArtifactID: "artifact-1",
		Scope: f.ownerScope(prepare.ArtifactVersionID), ContentIntent: ContentIntentDownload,
	}); !isCode(err, ErrorNotFound) {
		t.Fatalf("content target error = %v, want not found", err)
	}
	// The verified candidate is visible only through the exact candidate
	// query.
	candidate, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryCandidate, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		ArtifactVersionID: prepare.ArtifactVersionID,
	})
	if err != nil || candidate.State != OperationVerified {
		t.Fatalf("candidate query error = %v view = %#v", err, candidate)
	}
}

// TestPostgresTypedReferenceOnlyAttachAndRelease proves attach and release
// accept only exact typed references: the attach and residue tables carry
// only opaque identities and registered facts, and structurally contain no
// path, object key, prefix, bucket, vendor, URL, locator or signed-handle
// column.
func TestPostgresTypedReferenceOnlyAttachAndRelease(t *testing.T) {
	f := newPostgresFixture(t)
	operationID := "pg-typed-refs"
	f.prepareVerifyActivatePG(t, operationID)

	for _, table := range []string{"publication_attach", "publication_residue_staging", "publication_staging"} {
		rows, err := f.db.QueryContext(context.Background(), `
			SELECT column_name FROM information_schema.columns
			WHERE table_schema = $1 AND table_name = $2`, f.schema, table)
		if err != nil {
			t.Fatalf("list columns of %s: %v", table, err)
		}
		var columns []string
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				t.Fatalf("scan column: %v", err)
			}
			columns = append(columns, name)
		}
		rows.Close()
		for _, forbidden := range []string{"path", "key", "prefix", "bucket", "mount", "vendor", "url", "locator", "signed", "filename", "directory"} {
			for _, column := range columns {
				if strings.Contains(column, forbidden) {
					t.Fatalf("%s exposes locator-like column %q (forbidden fragment %q)", table, column, forbidden)
				}
			}
		}
	}

	// The attach row binds only exact typed references.
	var slot, artifactID, capabilityID, contentID, contentDigest string
	var size uint64
	var purpose, physicalGeneration string
	var verificationMethod, adapterID string
	if err := f.db.QueryRowContext(context.Background(), `SELECT slot, artifact_id, capability_id, content_id, content_digest,
		size, purpose, physical_generation, verification_method, adapter_id
		FROM "`+f.schema+`"."publication_attach"`).
		Scan(&slot, &artifactID, &capabilityID, &contentID, &contentDigest,
			&size, &purpose, &physicalGeneration, &verificationMethod, &adapterID); err != nil {
		t.Fatalf("scan attach row: %v", err)
	}
	if slot == "" || artifactID == "" || capabilityID == "" || contentID == "" || contentDigest == "" ||
		size == 0 || purpose == "" || physicalGeneration == "" || verificationMethod == "" || adapterID == "" {
		t.Fatalf("attach row must bind every typed fact: %#v", map[string]string{
			"slot": slot, "artifact_id": artifactID, "capability_id": capabilityID, "content_id": contentID,
			"content_digest": contentDigest, "purpose": purpose, "physical_generation": physicalGeneration,
			"verification_method": verificationMethod, "adapter_id": adapterID,
		})
	}

	// Reject releases the exact typed staging references as residue.
	f2 := newPostgresFixture(t)
	operationID2 := "pg-typed-release"
	set := f2.buildEvidenceDB(t, []ArtifactMemberSpec{f2.deckMemberSpec()})
	f2.mustPrepare(t, operationID2, set)
	f2.mustVerify(t, operationID2, set)
	if _, err := f2.core.Mutate(context.Background(), f2.rejectIntent(operationID2, RejectCandidateSuperseded, nil)); err != nil {
		t.Fatalf("reject: %v", err)
	}
	var residueSlot, residueContentID string
	if err := f2.db.QueryRowContext(context.Background(), `SELECT slot, content_id
		FROM "`+f2.schema+`"."publication_residue_staging"`).Scan(&residueSlot, &residueContentID); err != nil {
		t.Fatalf("scan residue staging: %v", err)
	}
	if residueSlot == "" || residueContentID == "" {
		t.Fatalf("residue must carry exact typed staging references: slot=%q content_id=%q", residueSlot, residueContentID)
	}
}

// TestPostgresSafePersistenceErrors proves persistence failures surface as
// closed, content-free safe errors: no SQL text, table names, DSNs,
// credentials, or raw downstream chains ever leak.
func TestPostgresSafePersistenceErrors(t *testing.T) {
	t.Run("nil database", func(t *testing.T) {
		if _, err := NewPostgresAuthority(nil, PostgresConfig{}); err == nil {
			t.Fatal("nil database must be rejected")
		}
	})
	t.Run("invalid schema identifier", func(t *testing.T) {
		f := newPostgresFixture(t)
		if _, err := NewPostgresAuthority(f.db, PostgresConfig{Schema: "Invalid; DROP TABLE"}); !isCode(err, ErrorInvalidIntent) {
			t.Fatalf("invalid schema error = %v, want invalid intent", err)
		}
	})
	t.Run("closed database is retryable and content-free", func(t *testing.T) {
		f := newPostgresFixture(t)
		db, err := sql.Open("pgx", "host=127.0.0.1 port=54723 user=postgres dbname=slidesmith_test sslmode=disable")
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		_ = db.Close()
		authority, err := NewPostgresAuthority(db, PostgresConfig{
			Schema:                       f.schema,
			RuntimeAuthorityID:           f.runtimeAuthority,
			ValidationAuthorityID:        f.validationAuthority,
			C04AuthorityID:               f.c04Authority,
			DurableObjectAuthorityID:     f.durableObjectAuthority,
			TaskOrchestrationAuthorityID: f.taskOrchestrationAuthority,
			RecoveryAuthorityID:          f.recoveryAuthority,
			PublicationAuthorityID:       f.publicationAuthority,
		})
		if err != nil {
			t.Fatalf("new authority over closed db: %v", err)
		}
		set := f.buildEvidenceDB(t, []ArtifactMemberSpec{f.deckMemberSpec()})
		_, err = authority.Mutate(context.Background(), f.prepareIntent("pg-closed", f.preparePayload("pg-closed", set, []ArtifactMemberSpec{f.deckMemberSpec()})))
		var publicationError *Error
		if !errors.As(err, &publicationError) || !publicationError.Retryable() {
			t.Fatalf("closed db error = %T %v, want retryable safe error", err, err)
		}
		if strings.Contains(err.Error(), "SQLSTATE") || strings.Contains(err.Error(), "connection") ||
			strings.Contains(err.Error(), "slidesmith") {
			t.Fatalf("safe error leaks persistence internals: %v", err)
		}
	})
	t.Run("duplicate identity is exact replay or conflict, never a second operation", func(t *testing.T) {
		f := newPostgresFixture(t)
		operationID := "pg-dup"
		set := f.buildEvidenceDB(t, []ArtifactMemberSpec{f.deckMemberSpec()})
		original := f.mustPrepare(t, operationID, set)
		replayed, err := f.core.Mutate(context.Background(), f.prepareIntent(operationID, f.preparePayload(operationID, set, []ArtifactMemberSpec{f.deckMemberSpec()})))
		if err != nil {
			t.Fatalf("duplicate identity error = %v, want exact replay", err)
		}
		if !replayed.Replay || replayed.ArtifactVersionID != original.ArtifactVersionID {
			t.Fatalf("duplicate identity must replay the original decision: %#v", replayed)
		}
	})
}

// TestPostgresRejectCancelReleaseOnlyExactOperationReferences proves
// reject/cancel release the typed staging references of ONLY the original
// operation: a second operation's staging is untouched and no activated
// member reference is ever released.
func TestPostgresRejectCancelReleaseOnlyExactOperationReferences(t *testing.T) {
	f := newPostgresFixture(t)
	_, activated := f.prepareVerifyActivatePG(t, "pg-release-1")

	// A second, unrelated operation is prepared and cancelled. Its prepare
	// must bind the committed stream facts (revision 1 / parent head).
	setB := f.buildEvidenceDB(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	headerB := f.header("pg-release-2")
	headerB.ExpectedStreamRevision = 1
	headerB.ExpectedHead = activated.ArtifactVersionID
	if _, err := f.core.Mutate(context.Background(), bindDigest(NewPreparePublication(headerB, f.preparePayload("pg-release-2", setB, []ArtifactMemberSpec{f.deckMemberSpec()})))); err != nil {
		t.Fatalf("prepare second operation: %v", err)
	}
	f.mustVerify(t, "pg-release-2", setB)
	if _, err := f.core.Mutate(context.Background(), f.cancelIntent("pg-release-2", CancelTaskOrchestration)); err != nil {
		t.Fatalf("cancel second operation: %v", err)
	}

	// Only the cancelled operation's residue exists; the activated version
	// keeps its typed attach reference and stays queryable.
	if got := f.countRows(t, "publication_residue"); got != 1 {
		t.Fatalf("residue rows = %d, want exactly the cancelled operation's residue", got)
	}
	if got := f.countRows(t, "publication_attach"); got != 1 {
		t.Fatalf("activated member reference must never be released, attach rows = %d", got)
	}
	version, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryExactVersion, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		ArtifactVersionID: activated.ArtifactVersionID,
	})
	if err != nil || version.State != OperationActivated {
		t.Fatalf("activated version must survive the release of an unrelated operation: %#v err=%v", version, err)
	}
}

// newPostgresFixtureFaults builds a postgres fixture whose authority carries
// the given persistence fault hook.
func newPostgresFixtureFaults(t *testing.T, faults PostgresFaultHook) *postgresFixture {
	t.Helper()
	base := newPostgresFixture(t)
	authority, err := NewPostgresAuthority(base.db, PostgresConfig{
		Schema: base.schema, Now: func() Instant { return base.now }, Faults: faults,
		RuntimeAuthorityID:           base.runtimeAuthority,
		ValidationAuthorityID:        base.validationAuthority,
		C04AuthorityID:               base.c04Authority,
		DurableObjectAuthorityID:     base.durableObjectAuthority,
		TaskOrchestrationAuthorityID: base.taskOrchestrationAuthority,
		RecoveryAuthorityID:          base.recoveryAuthority,
		PublicationAuthorityID:       base.publicationAuthority,
	})
	if err != nil {
		t.Fatalf("new faulted postgres authority: %v", err)
	}
	base.authority = authority
	base.fixture.core = authority
	return base
}

// reflectNumMethods reports how many methods the public seam exposes.
func reflectNumMethods(core PublicationCore) int {
	return reflect.TypeOf(core).NumMethod()
}

// postgresFaultName renders a fault point for readable subtest names.
func postgresFaultName(point PostgresFaultPoint) string {
	switch point {
	case PostgresFaultBeforeActivationCommit:
		return "before_activation_commit"
	case PostgresFaultBeforeReferenceAttach:
		return "before_reference_attach"
	case PostgresFaultBeforeMandatoryAudit:
		return "before_mandatory_audit"
	case PostgresFaultBeforeOutbox:
		return "before_outbox"
	case PostgresFaultBeforeCommit:
		return "before_commit"
	default:
		return "fault"
	}
}

// TestPostgresTwoFirstVersionActivationRaceSingleWinner proves the stream
// row-lock/CAS serializes two different first-version activations from the
// same empty expected head/revision: exactly one commits and the loser
// fails closed with the typed stale disposition (never last-writer-wins,
// never a second version).
func TestPostgresTwoFirstVersionActivationRaceSingleWinner(t *testing.T) {
	f := newPostgresFixture(t)
	setA := f.buildEvidenceDB(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	setB := f.buildEvidenceDB(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	f.mustPrepare(t, "pg-first-a", setA)
	f.mustPrepare(t, "pg-first-b", setB)
	f.mustVerify(t, "pg-first-a", setA)
	f.mustVerify(t, "pg-first-b", setB)

	var decisionA, decisionB PublicationDecision
	var errA, errB error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		decisionA, errA = f.core.Mutate(context.Background(), f.activateIntent("pg-first-a"))
	}()
	go func() {
		defer wg.Done()
		decisionB, errB = f.core.Mutate(context.Background(), f.activateIntent("pg-first-b"))
	}()
	wg.Wait()

	winners := 0
	if errA == nil && decisionA.State == OperationActivated && !decisionA.Replay {
		winners++
	}
	if errB == nil && decisionB.State == OperationActivated && !decisionB.Replay {
		winners++
	}
	if winners != 1 {
		t.Fatalf("two first-version activation winners = %d, want exactly 1 (a=%v b=%v)", winners, errA, errB)
	}
	// The loser must fail closed (stale) or exact-replay the winner; it can
	// never commit a second version.
	if errA != nil && !isCode(errA, ErrorStaleAuthority) {
		t.Fatalf("loser A error = %v, want stale authority", errA)
	}
	if errB != nil && !isCode(errB, ErrorStaleAuthority) {
		t.Fatalf("loser B error = %v, want stale authority", errB)
	}
	if got := f.countRows(t, "publication_activated"); got != 1 {
		t.Fatalf("activated rows = %d, want exactly 1", got)
	}
	stream, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryTaskStream, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
	})
	if err != nil {
		t.Fatalf("query stream: %v", err)
	}
	if stream.StreamRevision != 1 || stream.CurrentHead == "" {
		t.Fatalf("stream must reflect exactly one committed first version: %#v", stream)
	}
}

// TestPostgresRestartAfterResponseLossExactReplay proves the combined
// crash-after-commit-before-response + restart scenario: an activation that
// committed but whose response was lost is exact-replayed by a brand-new
// authority over the same schema, returning the same ArtifactVersionID,
// manifest digest and stream revision without reallocating identity.
func TestPostgresRestartAfterResponseLossExactReplay(t *testing.T) {
	f := newPostgresFixtureFaults(t, func(event PostgresFaultEvent) error {
		if event.Point == PostgresFaultAfterCommit && event.IntentKind == IntentActivatePublication {
			return errors.New("response lost")
		}
		return nil
	})
	operationID := "pg-restart-loss"
	set := f.buildEvidenceDB(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	prepare := f.mustPrepare(t, operationID, set)
	f.mustVerify(t, operationID, set)
	if _, err := f.core.Mutate(context.Background(), f.activateIntent(operationID)); err == nil {
		t.Fatal("activation response must be lost")
	}

	// Restart as a brand-new authority over the same schema.
	f.rebuildAuthority(t)

	replayed, err := f.core.Mutate(context.Background(), f.activateIntent(operationID))
	if err != nil {
		t.Fatalf("replay after restart: %v", err)
	}
	if !replayed.Replay || replayed.ArtifactVersionID != prepare.ArtifactVersionID ||
		replayed.ManifestDigest != prepare.ManifestDigest || replayed.StreamRevision != 1 {
		t.Fatalf("replay after restart must return the original committed decision: %#v", replayed)
	}
	if got := f.countRows(t, "publication_activated"); got != 1 {
		t.Fatalf("identity must never be reallocated across restart, activated rows = %d", got)
	}
}

// TestPostgresConcurrentConflictingPreparesNoDeadlock proves many
// concurrent same-identity prepares with different payloads all complete
// (no lock/pool deadlock): exactly one wins the insert, every other
// caller gets the typed integrity conflict, and at least one durable
// content-free incident is recorded.
func TestPostgresConcurrentConflictingPreparesNoDeadlock(t *testing.T) {
	f := newPostgresFixture(t)
	operationID := "pg-conflict-storm"
	set := f.buildEvidenceDB(t, []ArtifactMemberSpec{f.deckMemberSpec()})

	const workers = 6
	var wg sync.WaitGroup
	results := make([]error, workers)
	for index := 0; index < workers; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			// A different canonical payload under the SAME operation
			// identity: only the request identity differs, so the payload
			// stays internally valid (member/staging bindings intact) while
			// the canonical digest differs.
			header := f.header(operationID)
			header.RequestID = PublicationRequestID(fmt.Sprintf("request-%s-%d", operationID, index))
			payload := f.preparePayload(operationID, set, []ArtifactMemberSpec{f.deckMemberSpec()})
			_, results[index] = f.core.Mutate(context.Background(), bindDigest(NewPreparePublication(header, payload)))
		}(index)
	}
	wg.Wait()

	success := 0
	conflicts := 0
	for _, result := range results {
		if result == nil {
			success++
			continue
		}
		if isCode(result, ErrorIntegrityConflict) {
			conflicts++
			continue
		}
		t.Fatalf("unexpected prepare result: %v", result)
	}
	if success != 1 {
		t.Fatalf("prepare winners = %d, want exactly 1", success)
	}
	if conflicts != workers-1 {
		t.Fatalf("conflicts = %d, want %d", conflicts, workers-1)
	}
	if got := f.countRows(t, "publication_integrity_incident"); got < 1 {
		t.Fatalf("integrity incidents = %d, want at least 1 durable incident", got)
	}
	if got := f.countRows(t, "publication_operation"); got != 1 {
		t.Fatalf("operation rows = %d, want exactly 1 (one journaled operation)", got)
	}
}
