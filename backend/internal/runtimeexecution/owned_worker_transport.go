package runtimeexecution

import (
	"context"
	"time"
)

// OwnedWorkerTransportWireSchemaV1 is the strict versioned wire schema for the
// production-shaped owned transport that carries the private worker protocol
// (accept/heartbeat/observe/stop) between an owned worker and the Runtime
// authority. Machine authorization is carried by the exact worker/node
// authority binding; the envelope is versioned and never carries host paths,
// sessions, credentials, or arbitrary shell.
const OwnedWorkerTransportWireSchemaV1 = "slidesmith.runtime-execution.owned-worker-transport/v1"

// OwnedWorkerTransportEnvelope is the strict wire envelope. Version is
// mandatory; the machine authorization is the exact WorkerAuthorityID +
// NodeAuthorityID + generations that C03 already validates. The payload is a
// content-free canonical digest plus the opaque serialized worker protocol
// message. Opaque identity values are carried as strings because the ID types
// hold unexported values that JSON cannot round-trip; the worker protocol
// re-validates every field from the delivered command object so the wire never
// becomes a second authority.
type OwnedWorkerTransportEnvelope struct {
	SchemaVersion     string           `json:"schema_version"`
	Kind              string           `json:"kind"`
	OperationID       string           `json:"operation_id"`
	RuntimeRunID      string           `json:"runtime_run_id"`
	WorkerAuthorityID string           `json:"worker_authority_id"`
	WorkerGeneration  WorkerGeneration `json:"worker_generation"`
	NodeAuthorityID   string           `json:"node_authority_id"`
	NodeGeneration    NodeGeneration   `json:"node_generation"`
	CanonicalDigest   Digest           `json:"canonical_digest"`
	Payload           []byte           `json:"payload,omitempty"`
}

func validOwnedWorkerTransportEnvelope(envelope OwnedWorkerTransportEnvelope) bool {
	return envelope.SchemaVersion == OwnedWorkerTransportWireSchemaV1 &&
		validOpaqueID(envelope.OperationID) && validOpaqueID(envelope.RuntimeRunID) &&
		validOpaqueID(envelope.WorkerAuthorityID) && envelope.WorkerGeneration > 0 &&
		validOpaqueID(envelope.NodeAuthorityID) && envelope.NodeGeneration > 0 &&
		envelope.CanonicalDigest != (Digest{})
}

// OwnedWorkerMachineAuthorization is the explicit machine authorization a
// production-shaped worker must present on every transport message. The worker
// cannot choose these values: they are bound by the Runtime authority and by
// the worker/node identity that the worker protocol already validates.
type OwnedWorkerMachineAuthorization struct {
	WorkerAuthorityID WorkerAuthorityID
	WorkerGeneration  WorkerGeneration
	NodeAuthorityID   NodeAuthorityID
	NodeGeneration    NodeGeneration
}

// ownedWorkerTransport is the production-shaped owned transport + worker. It
// wraps the private worker protocol with a strict wire envelope (versioning,
// machine auth, canonical digest, delivery count for at-least-once) and
// reconciles ack-loss by re-issuing the exact original operation. The worker
// protocol remains the only authority: this transport only verifies identity
// and forwards the exact command object.
type ownedWorkerTransport struct {
	worker workerProtocol
	now    func() time.Time
}

func newOwnedWorkerTransport(worker workerProtocol, now func() time.Time) *ownedWorkerTransport {
	if now == nil {
		now = time.Now
	}
	return &ownedWorkerTransport{worker: worker, now: now}
}

// deliverEnvelope validates machine authorization, strict versioning and the
// canonical digest (at-least-once), then forwards the exact command to the
// worker protocol. Ack-loss is handled by the caller re-delivering the exact
// envelope: the worker protocol itself is idempotent on exact operation.
func (transport *ownedWorkerTransport) deliverEnvelope(
	ctx context.Context,
	envelope OwnedWorkerTransportEnvelope,
	machine OwnedWorkerMachineAuthorization,
	command interface{},
) error {
	if !validOwnedWorkerTransportEnvelope(envelope) {
		return newError(ErrorInvalidRequest)
	}
	if machine.WorkerAuthorityID.String() != envelope.WorkerAuthorityID ||
		machine.WorkerGeneration != envelope.WorkerGeneration ||
		machine.NodeAuthorityID.String() != envelope.NodeAuthorityID ||
		machine.NodeGeneration != envelope.NodeGeneration {
		return newError(ErrorAuthorizationDenied)
	}
	switch typed := command.(type) {
	case workerAccept:
		if envelope.Kind != "worker_accept" || envelope.OperationID != typed.OperationID.String() ||
			envelope.RuntimeRunID != typed.RuntimeRunID.String() ||
			envelope.WorkerAuthorityID != typed.WorkerAuthorityID.String() ||
			envelope.WorkerGeneration != typed.WorkerGeneration ||
			envelope.NodeAuthorityID != typed.NodeAuthorityID.String() ||
			envelope.NodeGeneration != typed.NodeGeneration ||
			envelope.CanonicalDigest != typed.CanonicalDigest {
			return newError(ErrorIntegrityConflict)
		}
		_, err := transport.worker.accept(ctx, typed)
		return err
	case workerHeartbeat:
		if envelope.Kind != "worker_heartbeat" || envelope.OperationID != typed.OperationID.String() ||
			envelope.RuntimeRunID != typed.RuntimeRunID.String() ||
			envelope.WorkerAuthorityID != typed.Lease.WorkerAuthorityID.String() ||
			envelope.WorkerGeneration != typed.Lease.WorkerGeneration ||
			envelope.NodeAuthorityID != typed.Lease.NodeAuthorityID.String() ||
			envelope.NodeGeneration != typed.Node.Generation ||
			envelope.CanonicalDigest != typed.CanonicalDigest {
			return newError(ErrorIntegrityConflict)
		}
		_, err := transport.worker.heartbeat(ctx, typed)
		return err
	case workerObserve:
		if envelope.Kind != "worker_observe" || envelope.OperationID != typed.Ref.AcceptOperationID.String() ||
			envelope.RuntimeRunID != typed.Ref.RuntimeRunID.String() ||
			envelope.WorkerAuthorityID != typed.Ref.WorkerAuthorityID.String() ||
			envelope.WorkerGeneration != typed.Ref.WorkerGeneration ||
			envelope.NodeAuthorityID != typed.Ref.NodeAuthorityID.String() ||
			envelope.NodeGeneration != typed.Ref.NodeGeneration ||
			envelope.CanonicalDigest != typed.CanonicalDigest {
			return newError(ErrorIntegrityConflict)
		}
		_, err := transport.worker.observe(ctx, typed)
		return err
	case workerStopIntent:
		if envelope.Kind != "worker_stop" || envelope.OperationID != typed.OperationID.String() ||
			envelope.RuntimeRunID != typed.RuntimeRunID.String() ||
			envelope.WorkerAuthorityID != typed.WorkerAuthorityID.String() ||
			envelope.WorkerGeneration != typed.WorkerGeneration ||
			envelope.NodeAuthorityID != typed.NodeAuthorityID.String() ||
			envelope.NodeGeneration != typed.NodeGeneration ||
			envelope.CanonicalDigest != typed.CanonicalDigest {
			return newError(ErrorIntegrityConflict)
		}
		_, err := transport.worker.stop(ctx, typed)
		return err
	default:
		return newError(ErrorInvalidRequest)
	}
}

func workerHeartbeatOperationID(command workerHeartbeat) OperationID { return command.OperationID }
