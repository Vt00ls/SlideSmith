package artifactpublication

// This file is the test fixture for the real PostgreSQL owned persistence
// adapter. It reuses every pure helper of the deterministic in-memory
// fixture (headers, payloads, evidence sets, intents) but runs them against
// a PostgresAuthority over an isolated real PostgreSQL schema. Durable
// Object capabilities and Identity & Ownership / Sharing availability facts
// are registered in the adapter's own restricted tables so the default
// DB-backed resolvers and the restricted attach participant exercise the
// real SQL path.

import (
	"context"
	"database/sql"
	"testing"

	"github.com/slidesmith/slidesmith/backend/internal/testpostgres"
)

// postgresFixture is a fixture over the real PostgreSQL authority. It
// embeds the deterministic fixture so every payload/evidence/intent helper
// is shared, but the core is a PostgresAuthority and the Durable Object /
// scope registries live in the owned SQL tables.
type postgresFixture struct {
	*fixture
	db        *sql.DB
	schema    string
	authority *PostgresAuthority
}

// newPostgresFixture opens an isolated real PostgreSQL schema and builds a
// PostgresAuthority over it with the default DB-backed resolvers and the
// default restricted Durable Object attach participant.
func newPostgresFixture(t *testing.T) *postgresFixture {
	t.Helper()
	db, schema := testpostgres.Open(t, "c05_04_publication")
	base := newFixture(t)
	f := &postgresFixture{fixture: base, db: db, schema: schema}
	authority, err := NewPostgresAuthority(db, PostgresConfig{
		Schema: schema, Now: func() Instant { return base.now },
		RuntimeAuthorityID:           base.runtimeAuthority,
		ValidationAuthorityID:        base.validationAuthority,
		C04AuthorityID:               base.c04Authority,
		DurableObjectAuthorityID:     base.durableObjectAuthority,
		TaskOrchestrationAuthorityID: base.taskOrchestrationAuthority,
		RecoveryAuthorityID:          base.recoveryAuthority,
		CleanupAuthorityID:           base.cleanupAuthority,
		PublicationAuthorityID:       base.publicationAuthority,
	})
	if err != nil {
		t.Fatalf("new postgres authority: %v", err)
	}
	if err := authority.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate postgres authority: %v", err)
	}
	f.authority = authority
	base.core = authority
	return f
}

// newPostgresFixtureOver builds a fixture over an already-open schema (used
// by restart tests that need a second authority over the same schema).
func newPostgresFixtureOver(t *testing.T, db *sql.DB, schema string) *postgresFixture {
	t.Helper()
	base := newFixture(t)
	f := &postgresFixture{fixture: base, db: db, schema: schema}
	authority, err := NewPostgresAuthority(db, PostgresConfig{
		Schema: schema, Now: func() Instant { return base.now },
		RuntimeAuthorityID:           base.runtimeAuthority,
		ValidationAuthorityID:        base.validationAuthority,
		C04AuthorityID:               base.c04Authority,
		DurableObjectAuthorityID:     base.durableObjectAuthority,
		TaskOrchestrationAuthorityID: base.taskOrchestrationAuthority,
		RecoveryAuthorityID:          base.recoveryAuthority,
		CleanupAuthorityID:           base.cleanupAuthority,
		PublicationAuthorityID:       base.publicationAuthority,
	})
	if err != nil {
		t.Fatalf("new postgres authority: %v", err)
	}
	f.authority = authority
	base.core = authority
	return f
}

// rebuildAuthority constructs a fresh PostgresAuthority over the same
// schema (restart): every durable fact must resume from SQL.
func (f *postgresFixture) rebuildAuthority(t *testing.T) {
	t.Helper()
	rebuilt := newPostgresFixtureOver(t, f.db, f.schema)
	f.authority = rebuilt.authority
	f.fixture.core = rebuilt.fixture.core
}

// registerCapabilityDB inserts one verified-content capability into the
// restricted Durable Object authority registry with its current-validity
// flag. It is the SQL equivalent of the in-memory capabilityRegistry.
func (f *postgresFixture) registerCapabilityDB(t *testing.T, capability ContentCapabilityEvidence, current bool) {
	t.Helper()
	_, err := f.db.ExecContext(context.Background(), `INSERT INTO "`+f.schema+`"."publication_do_capability"
		(capability_id, producer_authority_id, producer_generation, policy_domain_id, purpose,
		 content_id, content_digest, size, write_intent, physical_generation, verification_method,
		 adapter_id, generation, fence, safety_epoch, digest, current)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
		ON CONFLICT (capability_id) DO UPDATE
			SET producer_authority_id = EXCLUDED.producer_authority_id,
			    producer_generation = EXCLUDED.producer_generation,
			    policy_domain_id = EXCLUDED.policy_domain_id,
			    purpose = EXCLUDED.purpose,
			    content_id = EXCLUDED.content_id,
			    content_digest = EXCLUDED.content_digest,
			    size = EXCLUDED.size,
			    write_intent = EXCLUDED.write_intent,
			    physical_generation = EXCLUDED.physical_generation,
			    verification_method = EXCLUDED.verification_method,
			    adapter_id = EXCLUDED.adapter_id,
			    generation = EXCLUDED.generation,
			    fence = EXCLUDED.fence,
			    safety_epoch = EXCLUDED.safety_epoch,
			    digest = EXCLUDED.digest,
			    current = EXCLUDED.current`,
		string(capability.ID), string(capability.Producer.AuthorityID), uint64(capability.Producer.Generation),
		string(capability.PolicyDomainID), string(capability.Purpose),
		string(capability.ContentID), string(capability.ContentDigest), capability.Size,
		string(capability.WriteIntent), capability.PhysicalGeneration, string(capability.VerificationMethod),
		string(capability.AdapterID), uint64(capability.Generation), uint64(capability.Fence),
		uint64(capability.SafetyEpoch), string(capability.Digest), current)
	if err != nil {
		t.Fatalf("register capability in postgres registry: %v", err)
	}
}

// registerScopeDB inserts one current availability fact into the restricted
// Identity & Ownership / Sharing registry table.
func (f *postgresFixture) registerScopeDB(t *testing.T, key ContentScopeKey, scope ContentScope) {
	t.Helper()
	_, err := f.db.ExecContext(context.Background(), `INSERT INTO "`+f.schema+`"."publication_content_scope"
		(policy_domain_id, task_id, artifact_version_id, scope_kind, scope_id, availability_generation)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		string(key.PolicyDomainID), string(key.TaskID), string(key.ArtifactVersionID),
		string(scope.Kind), string(scope.ID), uint64(scope.AvailabilityGeneration))
	if err != nil {
		t.Fatalf("register scope in postgres registry: %v", err)
	}
}

// revokeScopeDB removes the current availability fact (revocation).
func (f *postgresFixture) revokeScopeDB(t *testing.T, key ContentScopeKey) {
	t.Helper()
	if _, err := f.db.ExecContext(context.Background(), `DELETE FROM "`+f.schema+`"."publication_content_scope"
		WHERE policy_domain_id = $1 AND task_id = $2 AND artifact_version_id = $3 AND scope_kind = $4 AND scope_id = $5`,
		string(key.PolicyDomainID), string(key.TaskID), string(key.ArtifactVersionID),
		string(key.Kind), string(key.ID)); err != nil {
		t.Fatalf("revoke scope in postgres registry: %v", err)
	}
}

// buildEvidenceDB builds one evidence set and registers its Durable Object
// capabilities as currently valid in the SQL registry.
func (f *postgresFixture) buildEvidenceDB(t *testing.T, members []ArtifactMemberSpec) *evidenceSet {
	t.Helper()
	set := f.fixture.buildEvidence(t, members)
	for _, capability := range set.capabilities {
		f.registerCapabilityDB(t, capability, true)
	}
	return set
}

// childEvidenceSetDB builds the manual-edit child evidence set and
// registers its capability in the SQL registry.
func (f *postgresFixture) childEvidenceSetDB(t *testing.T, parent ArtifactVersionID, operationID string) *evidenceSet {
	t.Helper()
	set := f.fixture.childEvidenceSet(t, parent, operationID)
	for _, capability := range set.capabilities {
		f.registerCapabilityDB(t, capability, true)
	}
	return set
}

// registerScopeForVersionDB registers the standard owner scope for one
// exact activated version.
func (f *postgresFixture) registerScopeForVersionDB(t *testing.T, versionID ArtifactVersionID) {
	t.Helper()
	f.registerScopeDB(t, f.scopeKey(versionID, ContentScopeOwner, "owner-principal-1"), f.ownerScope(versionID))
}

// countRows returns the row count of one owned table (test inspection of
// durability facts; the public seam never exposes SQL).
func (f *postgresFixture) countRows(t *testing.T, table string) int {
	t.Helper()
	var count int
	if err := f.db.QueryRowContext(context.Background(),
		`SELECT count(*) FROM "`+f.schema+`"."`+table+`"`).Scan(&count); err != nil {
		t.Fatalf("count %s rows: %v", table, err)
	}
	return count
}
