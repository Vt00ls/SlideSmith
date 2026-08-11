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
	"fmt"
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

// DurableObjectReleaseParticipant is the restricted same-PostgreSQL Durable
// Object participant for physical residue release (child SPEC #108). It
// performs the physical release of the EXACT typed staging references of
// one residue inside the caller's release transaction and returns an
// evidence-backed receipt. It validates every typed fact against the
// current Durable Object authority registry, verifies that NO activated
// member reference (attach row) exists for the references (activated
// member references can never be touched by cleanup), and fails closed on
// any mismatch. It cannot list objects, cannot infer a publication from a
// path, prefix, bucket, vendor or physical locator, and cannot perform any
// remote I/O.
type DurableObjectReleaseParticipant interface {
	// Release releases the exact typed staging references and returns the
	// evidence-backed receipt. Outcome released/already_absent/
	// retained_by_authority are evidence-backed closures; outcome ambiguous
	// keeps the residue release-requested for reconciliation; outcome
	// blocked keeps it open with blocker classes.
	Release(ctx context.Context, tx *sql.Tx, policyDomainID PolicyDomainID, taskID TaskID, operationID PublicationOperationID, safetyEpoch SafetyEpoch, refs []stagingRecord) (ReleaseReceipt, error)
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

// postgresDurableObjectRelease is the default restricted release
// participant backed by the adapter's owned publication_do_capability,
// publication_attach and publication_do_release tables in the same
// PostgreSQL database.
type postgresDurableObjectRelease struct {
	authority *PostgresAuthority
}

var _ DurableObjectReleaseParticipant = (*postgresDurableObjectRelease)(nil)

func (d *postgresDurableObjectRelease) Release(
	ctx context.Context,
	tx *sql.Tx,
	policyDomainID PolicyDomainID,
	taskID TaskID,
	operationID PublicationOperationID,
	safetyEpoch SafetyEpoch,
	refs []stagingRecord,
) (ReleaseReceipt, error) {
	authority := d.authority
	now := authority.nowValue()
	// Re-verify every exact typed reference against the current Durable
	// Object authority registry BEFORE any physical action; a single
	// unresolvable, mismatched or attached reference fails the whole
	// release closed (no partial release, no guessed outcome).
	missing := 0
	for _, ref := range refs {
		rows, err := tx.QueryContext(ctx, `SELECT capability_id, producer_authority_id, producer_generation,
			content_id, content_digest, size, purpose, physical_generation, verification_method,
			adapter_id, generation, fence, safety_epoch, current, released
			FROM `+authority.q("publication_do_capability")+`
			WHERE policy_domain_id = $1 AND purpose = $2 AND content_id = $3 AND content_digest = $4
			  AND size = $5 AND physical_generation = $6 AND adapter_id = $7
			FOR UPDATE`,
			string(policyDomainID), string(ref.purpose), string(ref.contentID), string(ref.contentDigest),
			ref.size, ref.physicalGeneration, string(ref.adapterID))
		if err != nil {
			return ReleaseReceipt{}, normalizePersistenceError(err)
		}
		foundCapabilities := false
		anyReleased := true
		for rows.Next() {
			foundCapabilities = true
			var capabilityID, producerID, purpose, contentID, contentDigest, method, adapterID string
			var producerGeneration, generation, fence, registryEpoch uint64
			var size, physicalGeneration uint64
			var current, released bool
			if err := rows.Scan(&capabilityID, &producerID, &producerGeneration,
				&contentID, &contentDigest, &size, &purpose, &physicalGeneration, &method,
				&adapterID, &generation, &fence, &registryEpoch, &current, &released); err != nil {
				rows.Close()
				return ReleaseReceipt{}, normalizePersistenceError(err)
			}
			if producerID != string(authority.doAuth) ||
				contentID != string(ref.contentID) || contentDigest != string(ref.contentDigest) ||
				size != ref.size || physicalGeneration != ref.physicalGeneration ||
				adapterID != string(ref.adapterID) || registryEpoch != uint64(safetyEpoch) {
				rows.Close()
				// The exact typed reference no longer matches the current
				// Durable Object registry: stale cleanup fails closed.
				return ReleaseReceipt{}, &Error{Code: ErrorStaleAuthority}
			}
			anyReleased = anyReleased && released
			if !current {
				rows.Close()
				// Not currently resolvable (in-flight/expired): ambiguous. The
				// residue stays release-requested and is never guessed closed.
				return ReleaseReceipt{
					Producer:   EvidenceProducer{AuthorityID: authority.doAuth, Generation: 1},
					Outcome:    ReleaseOutcomeAmbiguous,
					OccurredAt: now,
				}, nil
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return ReleaseReceipt{}, normalizePersistenceError(err)
		}
		rows.Close()
		if !foundCapabilities {
			missing++
			continue
		}
		if anyReleased {
			// The Durable Object registry attests the exact references are
			// already released: the re-run is an idempotent released receipt.
			return d.receiptFor(ctx, tx, policyDomainID, taskID, operationID, now, ReleaseOutcomeReleased)
		}
		// Activated member references can never be touched by cleanup: if
		// ANY of the references was attached to an Artifact Version, the
		// release fails closed.
		attached := 0
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM `+authority.q("publication_attach")+`
			WHERE policy_domain_id = $1 AND content_id = $2 AND content_digest = $3
			  AND size = $4 AND physical_generation = $5 AND adapter_id = $6`,
			string(policyDomainID), string(ref.contentID), string(ref.contentDigest),
			ref.size, ref.physicalGeneration, string(ref.adapterID)).Scan(&attached); err != nil {
			return ReleaseReceipt{}, normalizePersistenceError(err)
		}
		if attached > 0 {
			return ReleaseReceipt{}, &Error{Code: ErrorTerminalConflict}
		}
	}
	if len(refs) > 0 && missing == len(refs) {
		// The Durable Object authority attests every exact typed reference
		// is absent from its registry (already absent). This is the DO's
		// authoritative evidence, never a path/listing/operator guess.
		return d.receiptFor(ctx, tx, policyDomainID, taskID, operationID, now, ReleaseOutcomeAlreadyAbsent)
	}
	if missing > 0 {
		// Partial absence is an inconsistent residue: fail closed.
		return ReleaseReceipt{}, &Error{Code: ErrorIntegrityFailure}
	}
	// All references verified: perform the physical release (mark the
	// capabilities released) and return the evidence-backed released
	// receipt in the same transaction.
	for _, ref := range refs {
		if _, err := tx.ExecContext(ctx, `UPDATE `+authority.q("publication_do_capability")+`
			SET released = TRUE
			WHERE policy_domain_id = $1 AND purpose = $2 AND content_id = $3 AND content_digest = $4
			  AND size = $5 AND physical_generation = $6 AND adapter_id = $7`,
			string(policyDomainID), string(ref.purpose), string(ref.contentID), string(ref.contentDigest),
			ref.size, ref.physicalGeneration, string(ref.adapterID)); err != nil {
			return ReleaseReceipt{}, normalizePersistenceError(err)
		}
	}
	return d.receiptFor(ctx, tx, policyDomainID, taskID, operationID, now, ReleaseOutcomeReleased)
}

// receiptFor writes the Durable Object release evidence row and returns the
// evidence-backed receipt for the exact operation.
func (d *postgresDurableObjectRelease) receiptFor(
	ctx context.Context,
	tx *sql.Tx,
	policyDomainID PolicyDomainID,
	taskID TaskID,
	operationID PublicationOperationID,
	now Instant,
	outcome ReleaseOutcome,
) (ReleaseReceipt, error) {
	receiptID := fmt.Sprintf("do-release-%s-%d", string(operationID), now)
	receipt := ReleaseReceipt{
		ReceiptID: receiptID,
		Producer:  EvidenceProducer{AuthorityID: d.authority.doAuth, Generation: 1},
		Outcome:   outcome, OccurredAt: now,
	}
	receipt.Digest = receipt.CanonicalDigest()
	if _, err := tx.ExecContext(ctx, `INSERT INTO `+d.authority.q("publication_do_release")+`
		(receipt_id, policy_domain_id, task_id, operation_id, producer_authority_id,
		 producer_generation, outcome, blocker_classes, expiry, occurred_at, digest)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (receipt_id) DO NOTHING`,
		receiptID, string(policyDomainID), string(taskID), string(operationID),
		string(d.authority.doAuth), uint64(1), string(outcome), uint64(0), int64(0),
		int64(now), string(receipt.Digest)); err != nil {
		return ReleaseReceipt{}, normalizePersistenceError(err)
	}
	return receipt, nil
}
