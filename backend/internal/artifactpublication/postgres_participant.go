package artifactpublication

// This file defines the restricted same-PostgreSQL Durable Object
// participant (child SPEC #107, parent decision 24). The participant can
// only attach the exact typed references of one verified candidate inside
// the activation transaction: it validates every typed fact (policy domain,
// purpose, ContentID, digest, size, immutable write intent, physical
// generation, verification method, adapter identity and the activation
// safety epoch) against the current Durable Object authority registry and
// inserts the attach rows in the same transaction. It cannot list objects,
// cannot infer a publication from a path, prefix, bucket, vendor or
// physical locator, and cannot perform any remote I/O. Physical release on
// reject/cancel uses the same exact typed staging references.

import (
	"context"
	"database/sql"
	"errors"
)

// DurableObjectAttachReference is one exact typed reference to attach for
// one member slot. It carries only opaque identities and registered facts,
// never a materialization locator.
type DurableObjectAttachReference struct {
	Slot               MemberSlotID
	ArtifactID         ArtifactID
	CapabilityID       ContentCapabilityID
	ContentID          ContentID
	ContentDigest      Digest
	Size               uint64
	Purpose            ContentPurpose
	PhysicalGeneration uint64
	VerificationMethod VerificationMethod
	AdapterID          AdapterID
}

// DurableObjectAttachParticipant is the restricted same-PostgreSQL Durable
// Object participant. Attach runs inside the caller's activation
// transaction; any error rolls back the whole transaction (no half active
// version, no orphan membership, no readable unverified content). The
// participant never touches the public seam and never exposes a repository.
type DurableObjectAttachParticipant interface {
	// Attach attaches the exact typed references of one candidate to one
	// Artifact Version inside the given transaction. It must validate every
	// typed fact against the current Durable Object authority registry and
	// fail closed on any mismatch, missing capability, stale/rotated
	// generation, or current-validity loss.
	Attach(ctx context.Context, tx *sql.Tx, policyDomainID PolicyDomainID, taskID TaskID, versionID ArtifactVersionID, safetyEpoch SafetyEpoch, refs []DurableObjectAttachReference) error
}

// postgresDurableObjectAttach is the default restricted participant backed
// by the adapter's owned publication_do_capability registry and
// publication_attach table in the same PostgreSQL database.
type postgresDurableObjectAttach struct {
	authority *PostgresAuthority
}

var _ DurableObjectAttachParticipant = (*postgresDurableObjectAttach)(nil)

func (d *postgresDurableObjectAttach) Attach(
	ctx context.Context,
	tx *sql.Tx,
	policyDomainID PolicyDomainID,
	taskID TaskID,
	versionID ArtifactVersionID,
	safetyEpoch SafetyEpoch,
	refs []DurableObjectAttachReference,
) error {
	authority := d.authority
	now := authority.nowValue()
	for _, ref := range refs {
		// Lock the current capability row so a concurrent revocation or
		// generation rotation cannot interleave with the attach. The
		// capability must exist AND be currently valid in the Durable
		// Object authority registry: bytes existing or a receipt returning
		// is never enough (parent SPEC #103, user stories 24/25).
		var producerID string
		var producerGeneration, generation, fence uint64
		var domain, purpose, contentID, contentDigest string
		var size uint64
		var writeIntent string
		var physicalGeneration uint64
		var verificationMethod, adapterID string
		var registryEpoch uint64
		err := tx.QueryRowContext(ctx, `SELECT producer_authority_id, producer_generation, policy_domain_id, purpose,
			content_id, content_digest, size, write_intent, physical_generation, verification_method,
			adapter_id, generation, fence, safety_epoch
			FROM `+authority.q("publication_do_capability")+`
			WHERE capability_id = $1 AND current = TRUE FOR UPDATE`, string(ref.CapabilityID)).
			Scan(&producerID, &producerGeneration, &domain, &purpose, &contentID, &contentDigest,
				&size, &writeIntent, &physicalGeneration, &verificationMethod,
				&adapterID, &generation, &fence, &registryEpoch)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				// Missing, unknown, or no-longer-current capability: the
				// typed reference cannot be attached. Fail closed.
				return &Error{Code: ErrorDurabilityUnverified}
			}
			return &Error{Code: ErrorRetryableUnavailable}
		}
		// Every typed fact must bind exactly: policy domain, purpose,
		// ContentID, digest, size, immutable write intent, physical
		// generation, verification method, adapter identity and the safety
		// epoch of the activation. A ContentID or digest match alone never
		// confers membership, ownership, or authorization.
		if producerID != string(authority.doAuth) ||
			domain != string(policyDomainID) ||
			purpose != string(ContentPurposePublicationMember) ||
			contentID != string(ref.ContentID) ||
			contentDigest != string(ref.ContentDigest) ||
			size != ref.Size ||
			writeIntent != string(WriteIntentImmutable) ||
			physicalGeneration != ref.PhysicalGeneration ||
			verificationMethod != string(ref.VerificationMethod) ||
			adapterID != string(ref.AdapterID) ||
			registryEpoch != uint64(safetyEpoch) {
			return &Error{Code: ErrorIntegrityFailure}
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO `+authority.q("publication_attach")+`
			(version_id, policy_domain_id, task_id, slot, artifact_id, capability_id,
			 content_id, content_digest, size, purpose, physical_generation,
			 verification_method, adapter_id, attached_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
			string(versionID), string(policyDomainID), string(taskID),
			string(ref.Slot), string(ref.ArtifactID), string(ref.CapabilityID),
			string(ref.ContentID), string(ref.ContentDigest), ref.Size,
			string(ref.Purpose), ref.PhysicalGeneration, string(ref.VerificationMethod),
			string(ref.AdapterID), now)
		if err != nil {
			return normalizePersistenceError(err)
		}
		_, _, _ = producerGeneration, generation, fence
	}
	return nil
}
