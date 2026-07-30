package runtimeexecution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/taskworkspace"
)

// PrerequisiteState is closed. Zero and unknown values fail capsule readiness.
type PrerequisiteState uint8

const (
	PrerequisitePending PrerequisiteState = iota + 1
	PrerequisiteAccepted
	PrerequisiteRejected
	PrerequisiteNotApplicable
	PrerequisiteReconciliationRequired
)

type PrerequisiteFailure uint8

const (
	PrerequisiteFailureNone PrerequisiteFailure = iota
	PrerequisiteFailureWrongBinding
	PrerequisiteFailureRevoked
	PrerequisiteFailureStale
	PrerequisiteFailureIncompatible
	PrerequisiteFailureMissing
	PrerequisiteFailureCorrupt
	PrerequisiteFailureDuplicate
	PrerequisiteFailureCrossScope
	PrerequisiteFailureInvalidInput
	PrerequisiteFailureDependencyUnavailable
)

type PrerequisiteFact struct {
	State          PrerequisiteState
	OperationID    OperationID
	RequestDigest  Digest
	EvidenceID     EvidenceID
	EvidenceDigest Digest
	Failure        PrerequisiteFailure
}

type RuntimeReadinessSnapshot struct {
	Lease           PrerequisiteFact
	RuntimeBinding  PrerequisiteFact
	RuntimeView     PrerequisiteFact
	ImmutableInputs PrerequisiteFact
	LLMGateway      PrerequisiteFact
	CapsuleReady    bool
}

type RuntimeViewBindingSnapshot struct {
	RuntimeViewID               RuntimeViewID
	OpenOperationID             OperationID
	OpenRequestDigest           Digest
	SandboxLeaseAuthorityDigest Digest
	SandboxLeaseID              SandboxLeaseID
	LeaseGeneration             LeaseGeneration
	LeaseFence                  LeaseFence
	Effect                      EffectClass
	ExpiresAt                   time.Time
	LifecycleGeneration         TaskWorkspaceLifecycleGeneration
	LifecycleFence              TaskWorkspaceLifecycleFence
}

type RuntimeViewPrerequisitePort interface {
	OpenRuntimeView(context.Context, taskworkspace.OpenRuntimeViewRequest) (taskworkspace.OpenRuntimeViewResult, error)
	FenceRuntimeView(context.Context, taskworkspace.FenceRuntimeViewRequest) (taskworkspace.FenceRuntimeViewResult, error)
	DiscardRuntimeView(context.Context, taskworkspace.DiscardRuntimeViewRequest) (taskworkspace.DiscardRuntimeViewResult, error)
	InspectOperation(context.Context, taskworkspace.InspectOperationRequest) (taskworkspace.OperationInspection, error)
	ReconcileOperation(context.Context, taskworkspace.ReconcileOperationRequest) (taskworkspace.OperationInspection, error)
}

var _ RuntimeViewPrerequisitePort = (taskworkspace.Lifecycle)(nil)

type RuntimeViewPrerequisiteAdapter struct {
	OpenRuntimeViewFunc    func(context.Context, taskworkspace.OpenRuntimeViewRequest) (taskworkspace.OpenRuntimeViewResult, error)
	FenceRuntimeViewFunc   func(context.Context, taskworkspace.FenceRuntimeViewRequest) (taskworkspace.FenceRuntimeViewResult, error)
	DiscardRuntimeViewFunc func(context.Context, taskworkspace.DiscardRuntimeViewRequest) (taskworkspace.DiscardRuntimeViewResult, error)
	InspectOperationFunc   func(context.Context, taskworkspace.InspectOperationRequest) (taskworkspace.OperationInspection, error)
	ReconcileOperationFunc func(context.Context, taskworkspace.ReconcileOperationRequest) (taskworkspace.OperationInspection, error)
}

func (adapter RuntimeViewPrerequisiteAdapter) FenceRuntimeView(
	ctx context.Context,
	request taskworkspace.FenceRuntimeViewRequest,
) (taskworkspace.FenceRuntimeViewResult, error) {
	if adapter.FenceRuntimeViewFunc == nil {
		return taskworkspace.FenceRuntimeViewResult{}, &taskworkspace.Error{Code: taskworkspace.ErrorRetryableUnavailable}
	}
	return adapter.FenceRuntimeViewFunc(ctx, request)
}

func (adapter RuntimeViewPrerequisiteAdapter) DiscardRuntimeView(
	ctx context.Context,
	request taskworkspace.DiscardRuntimeViewRequest,
) (taskworkspace.DiscardRuntimeViewResult, error) {
	if adapter.DiscardRuntimeViewFunc == nil {
		return taskworkspace.DiscardRuntimeViewResult{}, &taskworkspace.Error{Code: taskworkspace.ErrorRetryableUnavailable}
	}
	return adapter.DiscardRuntimeViewFunc(ctx, request)
}

func (adapter RuntimeViewPrerequisiteAdapter) OpenRuntimeView(
	ctx context.Context,
	request taskworkspace.OpenRuntimeViewRequest,
) (taskworkspace.OpenRuntimeViewResult, error) {
	if adapter.OpenRuntimeViewFunc == nil {
		return taskworkspace.OpenRuntimeViewResult{}, &taskworkspace.Error{Code: taskworkspace.ErrorRetryableUnavailable}
	}
	return adapter.OpenRuntimeViewFunc(ctx, request)
}

func (adapter RuntimeViewPrerequisiteAdapter) InspectOperation(
	ctx context.Context,
	request taskworkspace.InspectOperationRequest,
) (taskworkspace.OperationInspection, error) {
	if adapter.InspectOperationFunc == nil {
		return taskworkspace.OperationInspection{}, &taskworkspace.Error{Code: taskworkspace.ErrorRetryableUnavailable}
	}
	return adapter.InspectOperationFunc(ctx, request)
}

func (adapter RuntimeViewPrerequisiteAdapter) ReconcileOperation(
	ctx context.Context,
	request taskworkspace.ReconcileOperationRequest,
) (taskworkspace.OperationInspection, error) {
	if adapter.ReconcileOperationFunc == nil {
		return taskworkspace.OperationInspection{}, &taskworkspace.Error{Code: taskworkspace.ErrorRetryableUnavailable}
	}
	return adapter.ReconcileOperationFunc(ctx, request)
}

type PrerequisiteObservationDisposition uint8

const (
	PrerequisiteObservationAccepted PrerequisiteObservationDisposition = iota + 1
	PrerequisiteObservationRejected
	PrerequisiteObservationRetryable
	PrerequisiteObservationAmbiguous
)

type PrerequisiteObservation struct {
	Disposition    PrerequisiteObservationDisposition
	EvidenceID     EvidenceID
	EvidenceDigest Digest
	Failure        PrerequisiteFailure
}

type RuntimeBindingAuthorization struct {
	PersonalWorkspaceID         PersonalWorkspaceID
	TaskID                      TaskID
	PhaseRunID                  PhaseRunID
	RuntimeRunID                RuntimeRunID
	RuntimeBindingID            RuntimeBindingID
	RuntimeBindingDigest        Digest
	ExecutionLockDigest         Digest
	CapabilityContractDigest    Digest
	AllowedPlatformImagesDigest Digest
	ExecutorContractDigest      Digest
	OutputContractDigest        Digest
	EvidenceContractDigest      Digest
	ReleaseSafetyEpoch          ReleaseSafetyEpoch
	CatalogBinding              *CatalogExecutionBinding
}

type RuntimeBindingValidationRequest struct {
	OperationID            OperationID
	CanonicalRequestDigest Digest
	Authorization          RuntimeBindingAuthorization
}

type ImmutableInputValidationRequest struct {
	OperationID            OperationID
	CanonicalRequestDigest Digest
	Authorization          RuntimeBindingAuthorization
	Manifest               ImmutableInputManifestBinding
	Inputs                 []ImmutableInputBinding
}

type retainedRuntimeViewOpen struct {
	Request       taskworkspace.OpenRuntimeViewRequest
	RequestDigest Digest
}

type runtimeViewTerminalKind uint8

const (
	runtimeViewTerminalFence runtimeViewTerminalKind = iota + 1
	runtimeViewTerminalDiscard
)

type runtimeViewTerminalState uint8

const (
	runtimeViewTerminalPending runtimeViewTerminalState = iota + 1
	runtimeViewTerminalAccepted
	runtimeViewTerminalRejected
	runtimeViewTerminalReconciliationRequired
)

type retainedRuntimeViewTerminal struct {
	Kind           runtimeViewTerminalKind
	FenceRequest   taskworkspace.FenceRuntimeViewRequest
	DiscardRequest taskworkspace.DiscardRuntimeViewRequest
	RequestDigest  Digest
	State          runtimeViewTerminalState
	ErrorCode      taskworkspace.ErrorCode
}

type runtimeViewTerminalObligation struct {
	Kind          runtimeViewTerminalKind
	FenceReason   taskworkspace.RuntimeViewFenceReason
	DiscardReason taskworkspace.RuntimeViewDiscardReason
}

func runtimeViewTerminalObligationFor(
	state RuntimeState,
	outcome RuntimeOutcome,
	cleanup RuntimeLeaseCleanupSnapshot,
	lease RuntimeLeaseSnapshot,
) (runtimeViewTerminalObligation, bool) {
	if state == RuntimeStopping && outcome == RuntimeOutcomeNone && cleanup.FenceRuntimeView &&
		lease.AcquireStatus == LeaseGranted {
		reason, valid := runtimeViewFenceReasonForLeaseDisposition(lease.Disposition)
		if !valid {
			return runtimeViewTerminalObligation{}, false
		}
		return runtimeViewTerminalObligation{
			Kind: runtimeViewTerminalFence, FenceReason: reason,
		}, true
	}
	if state != RuntimeTerminal {
		return runtimeViewTerminalObligation{}, false
	}
	switch outcome {
	case RuntimeCancelled:
		return runtimeViewTerminalObligation{
			Kind: runtimeViewTerminalFence, FenceReason: taskworkspace.RuntimeViewCancelled,
		}, true
	case RuntimeRejected:
		return runtimeViewTerminalObligation{
			Kind: runtimeViewTerminalDiscard, DiscardReason: taskworkspace.RuntimeViewValidationRejected,
		}, true
	case RuntimeTimedOut, RuntimeFailed:
		return runtimeViewTerminalObligation{
			Kind: runtimeViewTerminalDiscard, DiscardReason: taskworkspace.RuntimeViewRuntimeFailed,
		}, true
	default:
		return runtimeViewTerminalObligation{}, false
	}
}

func retainRuntimeViewTerminalObligation(
	open taskworkspace.OpenRuntimeViewRequest,
	binding RuntimeViewBindingSnapshot,
	obligation runtimeViewTerminalObligation,
) (*retainedRuntimeViewTerminal, error) {
	switch obligation.Kind {
	case runtimeViewTerminalFence:
		request, requestDigest, err := runtimeViewFenceRequest(open, binding, obligation.FenceReason)
		if err != nil {
			return nil, err
		}
		return &retainedRuntimeViewTerminal{
			Kind: runtimeViewTerminalFence, FenceRequest: request,
			RequestDigest: requestDigest, State: runtimeViewTerminalPending,
		}, nil
	case runtimeViewTerminalDiscard:
		request, requestDigest, err := runtimeViewDiscardRequest(open, binding, obligation.DiscardReason)
		if err != nil {
			return nil, err
		}
		return &retainedRuntimeViewTerminal{
			Kind: runtimeViewTerminalDiscard, DiscardRequest: request,
			RequestDigest: requestDigest, State: runtimeViewTerminalPending,
		}, nil
	default:
		return nil, newError(ErrorIntegrityConflict)
	}
}

type RuntimeBindingValidator interface {
	ValidateRuntimeBinding(context.Context, RuntimeBindingValidationRequest) (PrerequisiteObservation, error)
}

type RuntimeBindingValidatorFunc func(
	context.Context,
	RuntimeBindingValidationRequest,
) (PrerequisiteObservation, error)

func (function RuntimeBindingValidatorFunc) ValidateRuntimeBinding(
	ctx context.Context,
	request RuntimeBindingValidationRequest,
) (PrerequisiteObservation, error) {
	return function(ctx, request)
}

type ImmutableInputValidator interface {
	ValidateImmutableInputs(context.Context, ImmutableInputValidationRequest) (PrerequisiteObservation, error)
}

type ImmutableInputValidatorFunc func(
	context.Context,
	ImmutableInputValidationRequest,
) (PrerequisiteObservation, error)

func (function ImmutableInputValidatorFunc) ValidateImmutableInputs(
	ctx context.Context,
	request ImmutableInputValidationRequest,
) (PrerequisiteObservation, error) {
	return function(ctx, request)
}

func initialRuntimeReadiness(start StartRuntimeRun) RuntimeReadinessSnapshot {
	runtimeView := PrerequisiteFact{State: PrerequisitePending}
	if start.Effect == EffectReadOnly {
		runtimeView.State = PrerequisiteNotApplicable
	}
	inputs := PrerequisiteFact{State: PrerequisitePending}
	llmGateway := PrerequisiteFact{State: PrerequisitePending}
	if start.ProviderCapability == ProviderCapabilityNone {
		llmGateway.State = PrerequisiteNotApplicable
	}
	return RuntimeReadinessSnapshot{
		Lease:           PrerequisiteFact{State: PrerequisitePending},
		RuntimeBinding:  PrerequisiteFact{State: PrerequisitePending},
		RuntimeView:     runtimeView,
		ImmutableInputs: inputs,
		LLMGateway:      llmGateway,
	}
}

func updateCapsuleReadiness(
	readiness *RuntimeReadinessSnapshot,
	binding RuntimeViewBindingSnapshot,
	lease RuntimeLeaseSnapshot,
) {
	readiness.CapsuleReady = lease.AcquireStatus == LeaseGranted && lease.Disposition == LeaseActive &&
		prerequisiteSatisfied(readiness.Lease) &&
		prerequisiteSatisfied(readiness.RuntimeBinding) &&
		runtimeViewPrerequisiteSatisfied(readiness.RuntimeView, binding, lease) &&
		prerequisiteSatisfied(readiness.ImmutableInputs) &&
		prerequisiteSatisfied(readiness.LLMGateway)
}

func projectCapsuleReadinessAt(snapshot *RuntimeSnapshot, now time.Time) {
	if snapshot == nil || snapshot.Readiness == (RuntimeReadinessSnapshot{}) {
		return
	}
	if snapshot.Gateway != (GatewayPrerequisiteSnapshot{}) {
		snapshot.Readiness.LLMGateway = gatewayPrerequisiteFactAt(snapshot.Gateway, now)
	}
	want := snapshot.Readiness
	updateCapsuleReadiness(&want, snapshot.RuntimeViewBinding, snapshot.Lease)
	currentLeaseAuthority := now.Before(snapshot.Lease.ExpiresAt) &&
		now.Before(snapshot.Lease.AuthorizationExpiresAt)
	currentRuntimeViewAuthority := snapshot.RuntimeViewBinding == (RuntimeViewBindingSnapshot{}) ||
		now.Before(snapshot.RuntimeViewBinding.ExpiresAt)
	snapshot.Readiness.CapsuleReady = want.CapsuleReady && currentLeaseAuthority && currentRuntimeViewAuthority
}

func gatewayRequestPrerequisitesSatisfiedAt(snapshot RuntimeSnapshot, now time.Time) bool {
	if snapshot.State != RuntimePreparingPrerequisites || snapshot.Outcome != RuntimeOutcomeNone ||
		snapshot.Readiness == (RuntimeReadinessSnapshot{}) || !now.UTC().Before(snapshot.Deadline) ||
		snapshot.Lease.AcquireStatus != LeaseGranted || snapshot.Lease.Disposition != LeaseActive ||
		!now.UTC().Before(snapshot.Lease.ExpiresAt) || !now.UTC().Before(snapshot.Lease.AuthorizationExpiresAt) {
		return false
	}
	currentRuntimeViewAuthority := snapshot.RuntimeViewBinding == (RuntimeViewBindingSnapshot{}) ||
		now.UTC().Before(snapshot.RuntimeViewBinding.ExpiresAt)
	return prerequisiteSatisfied(snapshot.Readiness.Lease) &&
		prerequisiteSatisfied(snapshot.Readiness.RuntimeBinding) &&
		runtimeViewPrerequisiteSatisfied(snapshot.Readiness.RuntimeView, snapshot.RuntimeViewBinding, snapshot.Lease) &&
		currentRuntimeViewAuthority && prerequisiteSatisfied(snapshot.Readiness.ImmutableInputs)
}

func runtimeViewPrerequisiteSatisfied(
	fact PrerequisiteFact,
	binding RuntimeViewBindingSnapshot,
	lease RuntimeLeaseSnapshot,
) bool {
	if fact.State == PrerequisiteNotApplicable {
		return binding == (RuntimeViewBindingSnapshot{})
	}
	return fact.State == PrerequisiteAccepted && runtimeViewBindingMatchesLease(binding, lease)
}

func runtimeViewBindingMatchesLease(binding RuntimeViewBindingSnapshot, lease RuntimeLeaseSnapshot) bool {
	return binding != (RuntimeViewBindingSnapshot{}) && lease.AcquireStatus == LeaseGranted &&
		lease.Disposition == LeaseActive && binding.SandboxLeaseID == lease.LeaseID &&
		binding.LeaseGeneration == lease.Generation && binding.LeaseFence == lease.Fence &&
		!binding.ExpiresAt.After(lease.ExpiresAt)
}

func prerequisiteSatisfied(fact PrerequisiteFact) bool {
	return fact.State == PrerequisiteAccepted || fact.State == PrerequisiteNotApplicable
}

func knownRuntimeReadiness(
	readiness RuntimeReadinessSnapshot,
	binding RuntimeViewBindingSnapshot,
	lease RuntimeLeaseSnapshot,
) bool {
	if readiness == (RuntimeReadinessSnapshot{}) {
		return true
	}
	facts := [...]PrerequisiteFact{
		readiness.Lease, readiness.RuntimeBinding, readiness.RuntimeView,
		readiness.ImmutableInputs, readiness.LLMGateway,
	}
	for _, fact := range facts {
		if !knownPrerequisiteFact(fact) {
			return false
		}
	}
	want := readiness
	updateCapsuleReadiness(&want, binding, lease)
	return want.CapsuleReady == readiness.CapsuleReady
}

func knownPrerequisiteFact(fact PrerequisiteFact) bool {
	if fact.State < PrerequisitePending || fact.State > PrerequisiteReconciliationRequired ||
		fact.Failure > PrerequisiteFailureDependencyUnavailable {
		return false
	}
	switch fact.State {
	case PrerequisiteAccepted:
		return validOpaqueID(fact.OperationID.String()) && fact.RequestDigest != (Digest{}) &&
			validOpaqueID(fact.EvidenceID.String()) && fact.EvidenceDigest != (Digest{}) &&
			fact.Failure == PrerequisiteFailureNone
	case PrerequisiteRejected:
		return validOpaqueID(fact.OperationID.String()) && fact.RequestDigest != (Digest{}) &&
			fact.EvidenceID == (EvidenceID{}) && fact.EvidenceDigest == (Digest{}) &&
			fact.Failure > PrerequisiteFailureNone
	case PrerequisiteReconciliationRequired:
		return validOpaqueID(fact.OperationID.String()) && fact.RequestDigest != (Digest{}) &&
			fact.EvidenceID == (EvidenceID{}) && fact.EvidenceDigest == (Digest{}) &&
			fact.Failure == PrerequisiteFailureDependencyUnavailable
	case PrerequisitePending, PrerequisiteNotApplicable:
		return fact.OperationID == (OperationID{}) && fact.RequestDigest == (Digest{}) &&
			fact.EvidenceID == (EvidenceID{}) && fact.EvidenceDigest == (Digest{}) &&
			fact.Failure == PrerequisiteFailureNone
	default:
		return false
	}
}

func runtimeBindingValidationRequest(start StartRuntimeRun) RuntimeBindingValidationRequest {
	operationID, requestDigest := stablePrerequisiteOperation(
		"runtime-binding", start.RuntimeRunID.String(), start.CanonicalRequestDigest.String(),
	)
	return RuntimeBindingValidationRequest{
		OperationID: operationID, CanonicalRequestDigest: requestDigest,
		Authorization: runtimeBindingAuthorization(start),
	}
}

func runtimeBindingAuthorization(start StartRuntimeRun) RuntimeBindingAuthorization {
	var catalog *CatalogExecutionBinding
	if start.CatalogBinding != nil {
		copyBinding := *start.CatalogBinding
		catalog = &copyBinding
	}
	return RuntimeBindingAuthorization{
		PersonalWorkspaceID: start.PersonalWorkspaceID, TaskID: start.TaskID,
		PhaseRunID: start.PhaseRunID, RuntimeRunID: start.RuntimeRunID,
		RuntimeBindingID: start.RuntimeBindingID, RuntimeBindingDigest: start.RuntimeBindingDigest,
		ExecutionLockDigest: start.ExecutionLockDigest, CapabilityContractDigest: start.CapabilityContractDigest,
		AllowedPlatformImagesDigest: start.AllowedPlatformImagesDigest,
		ExecutorContractDigest:      start.ExecutorContractDigest, OutputContractDigest: start.OutputContractDigest,
		EvidenceContractDigest: start.EvidenceContractDigest, ReleaseSafetyEpoch: start.ReleaseSafetyEpoch,
		CatalogBinding: catalog,
	}
}

func immutableInputValidationRequest(start StartRuntimeRun) ImmutableInputValidationRequest {
	operationID, requestDigest := stablePrerequisiteOperation(
		"immutable-inputs", start.RuntimeRunID.String(), start.CanonicalRequestDigest.String(),
	)
	return ImmutableInputValidationRequest{
		OperationID: operationID, CanonicalRequestDigest: requestDigest,
		Authorization: runtimeBindingAuthorization(start), Manifest: start.ImmutableInputManifest,
		Inputs: append([]ImmutableInputBinding(nil), start.ImmutableInputs...),
	}
}

func stablePrerequisiteOperation(kind string, values ...string) (OperationID, Digest) {
	payload := "slidesmith.runtime-execution.prerequisite/" + kind + "/v1\n"
	for _, value := range values {
		payload += value + "\n"
	}
	digest := Digest(sha256.Sum256([]byte(payload)))
	return OperationID{value: fmt.Sprintf("prerequisite-%s-%x", kind, digest[:12])}, digest
}

func prerequisiteFactFromObservation(
	operationID OperationID,
	requestDigest Digest,
	observation PrerequisiteObservation,
	err error,
) (PrerequisiteFact, error) {
	if err != nil {
		return PrerequisiteFact{
			State: PrerequisiteReconciliationRequired, OperationID: operationID,
			RequestDigest: requestDigest, Failure: PrerequisiteFailureDependencyUnavailable,
		}, nil
	}
	switch observation.Disposition {
	case PrerequisiteObservationAccepted:
		if !validOpaqueID(observation.EvidenceID.String()) || observation.EvidenceDigest == (Digest{}) ||
			observation.Failure != PrerequisiteFailureNone {
			return PrerequisiteFact{}, newError(ErrorIntegrityConflict)
		}
		return PrerequisiteFact{
			State: PrerequisiteAccepted, OperationID: operationID, RequestDigest: requestDigest,
			EvidenceID: observation.EvidenceID, EvidenceDigest: observation.EvidenceDigest,
		}, nil
	case PrerequisiteObservationRejected:
		if observation.EvidenceID != (EvidenceID{}) || observation.EvidenceDigest != (Digest{}) ||
			observation.Failure <= PrerequisiteFailureNone ||
			observation.Failure > PrerequisiteFailureCrossScope {
			return PrerequisiteFact{}, newError(ErrorIntegrityConflict)
		}
		return PrerequisiteFact{
			State: PrerequisiteRejected, OperationID: operationID,
			RequestDigest: requestDigest, Failure: observation.Failure,
		}, nil
	case PrerequisiteObservationRetryable, PrerequisiteObservationAmbiguous:
		if observation.EvidenceID != (EvidenceID{}) || observation.EvidenceDigest != (Digest{}) ||
			observation.Failure != PrerequisiteFailureNone &&
				observation.Failure != PrerequisiteFailureDependencyUnavailable {
			return PrerequisiteFact{}, newError(ErrorIntegrityConflict)
		}
		return PrerequisiteFact{
			State: PrerequisiteReconciliationRequired, OperationID: operationID,
			RequestDigest: requestDigest, Failure: PrerequisiteFailureDependencyUnavailable,
		}, nil
	default:
		return PrerequisiteFact{}, newError(ErrorIntegrityConflict)
	}
}

func unavailablePrerequisiteFact(operationID OperationID, requestDigest Digest) PrerequisiteFact {
	return PrerequisiteFact{
		State: PrerequisiteReconciliationRequired, OperationID: operationID,
		RequestDigest: requestDigest, Failure: PrerequisiteFailureDependencyUnavailable,
	}
}

func nonEnumeratingImmutableInputFact(fact PrerequisiteFact) PrerequisiteFact {
	if fact.State != PrerequisiteRejected {
		return fact
	}
	switch fact.Failure {
	case PrerequisiteFailureMissing, PrerequisiteFailureCorrupt,
		PrerequisiteFailureDuplicate, PrerequisiteFailureCrossScope:
		fact.Failure = PrerequisiteFailureInvalidInput
	}
	return fact
}

type postLeaseTerminalCause uint8

const (
	postLeaseDeadline postLeaseTerminalCause = iota + 1
	postLeaseRuntimeBindingRejected
	postLeaseRuntimeViewRejected
	postLeaseImmutableInputsRejected
)

func (cause postLeaseTerminalCause) outcome() RuntimeOutcome {
	if cause == postLeaseDeadline {
		return RuntimeTimedOut
	}
	return RuntimeRejected
}

func (cause postLeaseTerminalCause) runtimeViewDiscardReason() taskworkspace.RuntimeViewDiscardReason {
	if cause == postLeaseDeadline {
		return taskworkspace.RuntimeViewRuntimeFailed
	}
	return taskworkspace.RuntimeViewValidationRejected
}

func validPostLeaseTerminalCause(cause postLeaseTerminalCause, fact PrerequisiteFact) bool {
	if cause == postLeaseDeadline {
		return fact == (PrerequisiteFact{})
	}
	return cause >= postLeaseRuntimeBindingRejected && cause <= postLeaseImmutableInputsRejected &&
		fact.State == PrerequisiteRejected && knownPrerequisiteFact(fact)
}

func (engine *invariantEngine) advanceInMemoryRuntimeBindingPrerequisite(
	ctx context.Context,
	start StartRuntimeRun,
	decision RuntimeDecision,
) (RuntimeDecision, error) {
	if decision.Fact.Disposition != DecisionAccepted {
		return decision, nil
	}
	engine.store.mu.Lock()
	record := engine.store.runtimes[start.RuntimeRunID]
	if record == nil || record.acceptedStartDigest != start.CanonicalRequestDigest {
		engine.store.mu.Unlock()
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
	if record.fixture.State == RuntimeTerminal ||
		record.fixture.State != RuntimeWaitingForLease && record.fixture.State != RuntimeReconciling &&
			record.fixture.State != RuntimePreparingPrerequisites {
		decision.Snapshot = snapshot(record, SnapshotSchemaCurrent)
		engine.store.mu.Unlock()
		return decision, nil
	}
	state := record.readiness.RuntimeBinding.State
	postLease := record.fixture.State == RuntimePreparingPrerequisites
	engine.store.mu.Unlock()

	if state == PrerequisitePending || state == PrerequisiteAccepted ||
		state == PrerequisiteReconciliationRequired {
		request := runtimeBindingValidationRequest(start)
		fact := unavailablePrerequisiteFact(request.OperationID, request.CanonicalRequestDigest)
		if engine.runtimeBindingValidator != nil {
			observation, observationErr := engine.runtimeBindingValidator.ValidateRuntimeBinding(ctx, request)
			var factErr error
			fact, factErr = prerequisiteFactFromObservation(
				request.OperationID, request.CanonicalRequestDigest, observation, observationErr,
			)
			if factErr != nil {
				return RuntimeDecision{}, factErr
			}
		} else if !postLease {
			return decision, nil
		}
		if err := engine.persistInMemoryPrerequisiteFact(
			start, prerequisiteRuntimeBinding, request.OperationID, request.CanonicalRequestDigest, fact,
		); err != nil {
			return RuntimeDecision{}, err
		}
	}

	engine.store.mu.Lock()
	record = engine.store.runtimes[start.RuntimeRunID]
	if record == nil {
		engine.store.mu.Unlock()
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
	decision.Snapshot = snapshot(record, SnapshotSchemaCurrent)
	engine.store.mu.Unlock()
	return decision, nil
}

func (engine *invariantEngine) advanceInMemoryRuntimeBindingRejection(
	start StartRuntimeRun,
	decision RuntimeDecision,
) (RuntimeDecision, error) {
	if decision.Fact.Disposition != DecisionAccepted {
		return decision, nil
	}
	engine.store.mu.Lock()
	record := engine.store.runtimes[start.RuntimeRunID]
	if record == nil || record.acceptedStartDigest != start.CanonicalRequestDigest {
		engine.store.mu.Unlock()
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
	if record.fixture.State == RuntimeTerminal ||
		record.fixture.State != RuntimeWaitingForLease && record.fixture.State != RuntimeReconciling {
		decision.Snapshot = snapshot(record, SnapshotSchemaCurrent)
		engine.store.mu.Unlock()
		return decision, nil
	}
	if record.readiness.RuntimeBinding.State == PrerequisiteRejected {
		return engine.finishInMemoryNoLeaseLocked(
			record, decision, RuntimeRejected, PreLeaseTerminalImmutableBinding, engine.clock.current(),
		)
	}
	decision.Snapshot = snapshot(record, SnapshotSchemaCurrent)
	engine.store.mu.Unlock()
	return decision, nil
}

func (engine *invariantEngine) advancePostLeasePrerequisites(
	ctx context.Context,
	start StartRuntimeRun,
	decision RuntimeDecision,
) (RuntimeDecision, error) {
	if decision.Fact.Disposition != DecisionAccepted {
		return decision, nil
	}
	engine.store.mu.Lock()
	record := engine.store.runtimes[start.RuntimeRunID]
	if record == nil || record.acceptedStartDigest != start.CanonicalRequestDigest {
		engine.store.mu.Unlock()
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
	if obligation, required := runtimeViewTerminalObligationFor(
		record.fixture.State, record.fixture.Outcome, record.cleanup, record.lease,
	); required && record.fixture.State == RuntimeTerminal {
		decision.Snapshot = snapshot(record, SnapshotSchemaCurrent)
		engine.store.mu.Unlock()
		var err error
		switch obligation.Kind {
		case runtimeViewTerminalFence:
			err = engine.advanceInMemoryRuntimeViewFence(ctx, start.RuntimeRunID, obligation.FenceReason)
		case runtimeViewTerminalDiscard:
			err = engine.advanceInMemoryRuntimeViewDiscard(ctx, start.RuntimeRunID, obligation.DiscardReason)
		default:
			err = newError(ErrorIntegrityConflict)
		}
		if err != nil {
			return RuntimeDecision{}, err
		}
		engine.store.mu.Lock()
		if current := engine.store.runtimes[start.RuntimeRunID]; current != nil {
			decision.Snapshot = snapshot(current, SnapshotSchemaCurrent)
		}
		engine.store.mu.Unlock()
		return decision, nil
	}
	if record.fixture.State != RuntimePreparingPrerequisites ||
		record.lease.AcquireStatus != LeaseGranted || record.lease.Disposition != LeaseActive {
		decision.Snapshot = snapshot(record, SnapshotSchemaCurrent)
		engine.store.mu.Unlock()
		return decision, nil
	}
	if !engine.clock.current().Before(record.deadline) {
		decision, err := engine.finishInMemoryPostLeaseTerminalLocked(
			record, decision, postLeaseDeadline, PrerequisiteFact{},
		)
		if err != nil {
			return RuntimeDecision{}, err
		}
		if err := engine.advanceInMemoryRuntimeViewDiscard(
			ctx, start.RuntimeRunID, taskworkspace.RuntimeViewRuntimeFailed,
		); err != nil {
			return RuntimeDecision{}, err
		}
		engine.store.mu.Lock()
		if current := engine.store.runtimes[start.RuntimeRunID]; current != nil {
			decision.Snapshot = snapshot(current, SnapshotSchemaCurrent)
		}
		engine.store.mu.Unlock()
		return decision, nil
	}
	if record.readiness == (RuntimeReadinessSnapshot{}) {
		record.readiness = initialRuntimeReadiness(start)
	}
	record.readiness.Lease = leasePrerequisiteFact(record.lease)
	updateCapsuleReadiness(&record.readiness, record.runtimeViewBinding, record.lease)
	engine.store.mu.Unlock()

	engine.store.mu.Lock()
	record = engine.store.runtimes[start.RuntimeRunID]
	if record == nil {
		engine.store.mu.Unlock()
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
	runtimeBindingFact := record.readiness.RuntimeBinding
	runtimeBindingAccepted := prerequisiteSatisfied(runtimeBindingFact)
	viewState := record.readiness.RuntimeView.State
	engine.store.mu.Unlock()
	if runtimeBindingFact.State == PrerequisiteRejected {
		terminal, terminalErr := engine.finishInMemoryPostLeasePrerequisiteRejection(
			start.RuntimeRunID, decision, postLeaseRuntimeBindingRejected, runtimeBindingFact,
		)
		if terminalErr != nil {
			return RuntimeDecision{}, terminalErr
		}
		if err := engine.advanceInMemoryRuntimeViewDiscard(
			ctx, start.RuntimeRunID, taskworkspace.RuntimeViewValidationRejected,
		); err != nil {
			return RuntimeDecision{}, err
		}
		engine.store.mu.Lock()
		if current := engine.store.runtimes[start.RuntimeRunID]; current != nil {
			terminal.Snapshot = snapshot(current, SnapshotSchemaCurrent)
		}
		engine.store.mu.Unlock()
		return terminal, nil
	}
	if runtimeBindingAccepted && start.Effect == EffectMutating &&
		(viewState == PrerequisitePending || viewState == PrerequisiteReconciliationRequired) &&
		engine.runtimeViewPrerequisite != nil {
		request, requestDigest, prepareErr := engine.prepareInMemoryRuntimeViewOpen(start)
		if prepareErr != nil {
			return RuntimeDecision{}, prepareErr
		}
		result, openErr := engine.runtimeViewPrerequisite.OpenRuntimeView(ctx, request)
		if openErr != nil {
			result, openErr = inspectOrReconcileRuntimeViewOpen(ctx, engine.runtimeViewPrerequisite, request, openErr)
		}
		fact, binding, factErr := runtimeViewFactFromResult(request, requestDigest, result, openErr)
		if factErr != nil {
			return RuntimeDecision{}, factErr
		}
		if err := engine.persistInMemoryRuntimeViewFact(start, request, requestDigest, fact, binding); err != nil {
			return RuntimeDecision{}, err
		}
		if fact.State == PrerequisiteAccepted {
			if err := engine.reconcileInMemoryRuntimeViewAfterOpen(ctx, start.RuntimeRunID); err != nil {
				return RuntimeDecision{}, err
			}
		}
	}

	engine.store.mu.Lock()
	record = engine.store.runtimes[start.RuntimeRunID]
	if record == nil {
		engine.store.mu.Unlock()
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
	inputState := record.readiness.ImmutableInputs.State
	runtimeBindingAccepted = prerequisiteSatisfied(record.readiness.RuntimeBinding)
	runtimeViewAccepted := runtimeViewPrerequisiteSatisfied(
		record.readiness.RuntimeView, record.runtimeViewBinding, record.lease,
	)
	runtimeViewFact := record.readiness.RuntimeView
	discardRejectedInputs := inputState == PrerequisiteRejected &&
		record.readiness.RuntimeView.State == PrerequisiteAccepted
	engine.store.mu.Unlock()
	if runtimeViewFact.State == PrerequisiteRejected {
		return engine.finishInMemoryPostLeasePrerequisiteRejection(
			start.RuntimeRunID, decision, postLeaseRuntimeViewRejected, runtimeViewFact,
		)
	}
	if runtimeBindingAccepted && runtimeViewAccepted &&
		(inputState == PrerequisitePending || inputState == PrerequisiteReconciliationRequired) &&
		engine.immutableInputValidator != nil {
		request := immutableInputValidationRequest(start)
		observation, observationErr := engine.immutableInputValidator.ValidateImmutableInputs(ctx, request)
		fact, factErr := prerequisiteFactFromObservation(
			request.OperationID, request.CanonicalRequestDigest, observation, observationErr,
		)
		if factErr != nil {
			return RuntimeDecision{}, factErr
		}
		fact = nonEnumeratingImmutableInputFact(fact)
		if err := engine.persistInMemoryPrerequisiteFact(
			start, prerequisiteImmutableInputs, request.OperationID, request.CanonicalRequestDigest, fact,
		); err != nil {
			return RuntimeDecision{}, err
		}
		discardRejectedInputs = fact.State == PrerequisiteRejected
	}
	if discardRejectedInputs {
		engine.store.mu.Lock()
		record = engine.store.runtimes[start.RuntimeRunID]
		if record == nil {
			engine.store.mu.Unlock()
			return RuntimeDecision{}, newError(ErrorIntegrityConflict)
		}
		inputFact := record.readiness.ImmutableInputs
		terminal, terminalErr := engine.finishInMemoryPostLeaseTerminalLocked(
			record, decision, postLeaseImmutableInputsRejected, inputFact,
		)
		if terminalErr != nil {
			return RuntimeDecision{}, terminalErr
		}
		if err := engine.advanceInMemoryRuntimeViewDiscard(
			ctx, start.RuntimeRunID, taskworkspace.RuntimeViewValidationRejected,
		); err != nil {
			return RuntimeDecision{}, err
		}
		engine.store.mu.Lock()
		if current := engine.store.runtimes[start.RuntimeRunID]; current != nil {
			terminal.Snapshot = snapshot(current, SnapshotSchemaCurrent)
		}
		engine.store.mu.Unlock()
		return terminal, nil
	}

	engine.store.mu.Lock()
	record = engine.store.runtimes[start.RuntimeRunID]
	if record == nil {
		engine.store.mu.Unlock()
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
	updateCapsuleReadiness(&record.readiness, record.runtimeViewBinding, record.lease)
	decision.Snapshot = snapshot(record, SnapshotSchemaCurrent)
	engine.store.mu.Unlock()
	return decision, nil
}

func (engine *invariantEngine) reconcileInMemoryRuntimeViewAfterOpen(
	ctx context.Context,
	runtimeRunID RuntimeRunID,
) error {
	engine.store.mu.Lock()
	record := engine.store.runtimes[runtimeRunID]
	if record == nil {
		engine.store.mu.Unlock()
		return newError(ErrorIntegrityConflict)
	}
	if record.fixture.State == RuntimeStopping && record.fixture.Outcome == RuntimeOutcomeNone &&
		record.cleanup.FenceRuntimeView {
		reason := taskworkspace.RuntimeViewFenceReason("")
		valid := false
		if retained := record.runtimeViewTerminal; retained != nil {
			if retained.Kind != runtimeViewTerminalFence {
				engine.store.mu.Unlock()
				return newError(ErrorIntegrityConflict)
			}
			reason, valid = retained.FenceRequest.Reason, true
		} else {
			obligation, required := runtimeViewTerminalObligationFor(
				record.fixture.State, record.fixture.Outcome, record.cleanup, record.lease,
			)
			if required && obligation.Kind == runtimeViewTerminalFence {
				reason, valid = obligation.FenceReason, true
			}
		}
		engine.store.mu.Unlock()
		if !valid {
			return newError(ErrorIntegrityConflict)
		}
		return engine.advanceInMemoryRuntimeViewFence(ctx, runtimeRunID, reason)
	}
	obligation, required := runtimeViewTerminalObligationFor(
		record.fixture.State, record.fixture.Outcome, record.cleanup, record.lease,
	)
	if record.fixture.State != RuntimeTerminal {
		engine.store.mu.Unlock()
		return nil
	}
	engine.store.mu.Unlock()
	if !required {
		return newError(ErrorIntegrityConflict)
	}
	switch obligation.Kind {
	case runtimeViewTerminalFence:
		return engine.advanceInMemoryRuntimeViewFence(ctx, runtimeRunID, obligation.FenceReason)
	case runtimeViewTerminalDiscard:
		return engine.advanceInMemoryRuntimeViewDiscard(ctx, runtimeRunID, obligation.DiscardReason)
	default:
		return newError(ErrorIntegrityConflict)
	}
}

func runtimeViewFenceReasonForLeaseDisposition(
	disposition LeaseDisposition,
) (taskworkspace.RuntimeViewFenceReason, bool) {
	switch disposition {
	case LeaseExpired:
		return taskworkspace.RuntimeViewTimedOut, true
	case LeaseRevoked:
		return taskworkspace.RuntimeViewRevoked, true
	default:
		return taskworkspace.RuntimeViewFenceReason(""), false
	}
}

func (engine *invariantEngine) finishInMemoryPostLeasePrerequisiteRejection(
	runtimeRunID RuntimeRunID,
	decision RuntimeDecision,
	cause postLeaseTerminalCause,
	fact PrerequisiteFact,
) (RuntimeDecision, error) {
	engine.store.mu.Lock()
	record := engine.store.runtimes[runtimeRunID]
	if record == nil {
		engine.store.mu.Unlock()
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
	return engine.finishInMemoryPostLeaseTerminalLocked(record, decision, cause, fact)
}

func (engine *invariantEngine) finishInMemoryPostLeaseTerminalLocked(
	record *runtimeRecord,
	decision RuntimeDecision,
	cause postLeaseTerminalCause,
	fact PrerequisiteFact,
) (RuntimeDecision, error) {
	if !validPostLeaseTerminalCause(cause, fact) ||
		record.fixture.State != RuntimePreparingPrerequisites || record.fixture.Outcome != RuntimeOutcomeNone ||
		record.lease.AcquireStatus != LeaseGranted || record.lease.Disposition != LeaseActive {
		engine.store.mu.Unlock()
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
	startBinding := record.operation
	leaseBinding := record.lease
	node := engine.store.nodes[startBinding.ExecutionNodeID]
	if node == nil || node.ActiveRuntimeRunID != record.fixture.RuntimeRunID ||
		node.ActiveLeaseID != leaseBinding.LeaseID || node.Occupancy != NodeOccupied {
		engine.store.mu.Unlock()
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
	operationValues := []string{
		record.fixture.RuntimeRunID.String(),
		record.acceptedStart.OperationID.String(), record.deadline.Format(time.RFC3339Nano),
		leaseBinding.AcquireOperationID.String(), leaseBinding.AcquireDigest.String(),
		fmt.Sprint(cause),
	}
	operationKind := "post-lease-deadline"
	if cause != postLeaseDeadline {
		operationKind = "post-lease-prerequisite-rejected"
		operationValues = append(operationValues, fact.OperationID.String(), fact.RequestDigest.String(), fmt.Sprint(fact.Failure))
	}
	operationID, operationDigest := stablePrerequisiteOperation(operationKind, operationValues...)
	record.fixture.RuntimeRevision++
	record.fixture.OperationGeneration++
	record.fixture.RuntimeFence++
	record.fixture.State = RuntimeTerminal
	record.fixture.Outcome = cause.outcome()
	record.operation = startBinding
	record.operation.OperationID = operationID
	record.operation.Digest = operationDigest
	record.operation.Generation = record.fixture.OperationGeneration
	record.lease.Generation++
	record.lease.Fence++
	record.lease.SandboxFence++
	record.lease.Disposition = LeaseRevoked
	updateCapsuleReadiness(&record.readiness, record.runtimeViewBinding, record.lease)
	node.Occupancy = NodeOccupancyUnknown
	node.Quarantined = true
	node.Containment = ContainmentPending
	node.Reset = ResetRequired
	record.node = nodeSnapshot(*node)
	record.cleanup = RuntimeLeaseCleanupSnapshot{
		Status: LeaseCleanupPending, OperationID: operationID,
		CanonicalRequestDigest: operationDigest, StopMainProcess: true, StopChildProcesses: true,
		RevokeSecrets: true, RemoveNetwork: true, FenceRuntimeView: false,
		ReconcileContainment: true,
	}
	record.capacity = RuntimeCapacitySnapshot{
		LogicalRelease: LogicalCapacityReleaseReady, NoLease: NoLeaseDispositionNone,
		Physical: PhysicalCapacityUnknownOrQuarantined,
	}
	record.capacityEvidence = RuntimeCapacityEvidenceSnapshot{
		RuntimeFencedOrTerminal: RuntimeFencedOrTerminalEvidence{
			WorkItemID: startBinding.WorkItemID, AdmissionGrantID: startBinding.AdmissionGrantID,
			GrantGeneration: startBinding.GrantGeneration, RuntimeRunID: record.fixture.RuntimeRunID,
			StartOperationID: record.acceptedStart.OperationID, StartDigest: record.acceptedStartDigest,
			TerminalDecisionID: RuntimeDecisionID{value: "runtime-post-lease-" + operationDigest.String()[:24]},
			RuntimeRevision:    record.fixture.RuntimeRevision, RuntimeFence: record.fixture.RuntimeFence,
			SchedulerEpoch: startBinding.SchedulerEpoch, PolicyVersion: startBinding.PolicyVersion,
			LeaseAcquireOperationID: leaseBinding.AcquireOperationID,
			LeaseAcquireDigest:      leaseBinding.AcquireDigest,
		},
	}
	record.reconciliation = ReconciliationStable
	decision.Snapshot = snapshot(record, SnapshotSchemaCurrent)
	engine.store.mu.Unlock()
	return decision, nil
}

type prerequisiteKind uint8

const (
	prerequisiteRuntimeBinding prerequisiteKind = iota + 1
	prerequisiteRuntimeView
	prerequisiteImmutableInputs
)

func (engine *invariantEngine) persistInMemoryPrerequisiteFact(
	start StartRuntimeRun,
	kind prerequisiteKind,
	operationID OperationID,
	requestDigest Digest,
	fact PrerequisiteFact,
) error {
	engine.store.mu.Lock()
	defer engine.store.mu.Unlock()
	record := engine.store.runtimes[start.RuntimeRunID]
	if record == nil || record.acceptedStartDigest != start.CanonicalRequestDigest ||
		!validPrerequisitePersistenceStage(record, kind) {
		return newError(ErrorIntegrityConflict)
	}
	var retained *PrerequisiteFact
	switch kind {
	case prerequisiteRuntimeBinding:
		retained = &record.readiness.RuntimeBinding
	case prerequisiteImmutableInputs:
		retained = &record.readiness.ImmutableInputs
	default:
		return newError(ErrorIntegrityConflict)
	}
	if retained.OperationID != (OperationID{}) &&
		(retained.OperationID != operationID || retained.RequestDigest != requestDigest) {
		return newError(ErrorIntegrityConflict)
	}
	if retained.State == PrerequisiteRejected ||
		kind != prerequisiteRuntimeBinding && retained.State == PrerequisiteAccepted {
		if retained.OperationID != operationID || retained.RequestDigest != requestDigest {
			return newError(ErrorIntegrityConflict)
		}
		return nil
	}
	if kind == prerequisiteRuntimeBinding && retained.State == PrerequisiteAccepted &&
		fact.State == PrerequisiteAccepted {
		if *retained != fact {
			return newError(ErrorIntegrityConflict)
		}
		return nil
	}
	*retained = fact
	updateCapsuleReadiness(&record.readiness, record.runtimeViewBinding, record.lease)
	return nil
}

func validPrerequisitePersistenceStage(record *runtimeRecord, kind prerequisiteKind) bool {
	postLease := record.fixture.State == RuntimePreparingPrerequisites &&
		record.lease.AcquireStatus == LeaseGranted && record.lease.Disposition == LeaseActive
	if kind != prerequisiteRuntimeBinding {
		return postLease
	}
	preLease := (record.fixture.State == RuntimeWaitingForLease || record.fixture.State == RuntimeReconciling) &&
		(record.lease.AcquireStatus == LeaseAcquirePending ||
			record.lease.AcquireStatus == LeaseAcquireReconciliationRequired) &&
		record.lease.Disposition == LeaseDispositionNone
	return preLease || postLease
}

func (engine *invariantEngine) prepareInMemoryRuntimeViewOpen(
	start StartRuntimeRun,
) (taskworkspace.OpenRuntimeViewRequest, Digest, error) {
	engine.store.mu.Lock()
	defer engine.store.mu.Unlock()
	record := engine.store.runtimes[start.RuntimeRunID]
	if record == nil || record.acceptedStartDigest != start.CanonicalRequestDigest ||
		record.fixture.State != RuntimePreparingPrerequisites || record.lease.AcquireStatus != LeaseGranted ||
		record.lease.Disposition != LeaseActive || start.RuntimeViewRequirement == nil {
		return taskworkspace.OpenRuntimeViewRequest{}, Digest{}, newError(ErrorIntegrityConflict)
	}
	if record.runtimeViewOpen != nil {
		request := record.runtimeViewOpen.Request
		if digestFromTaskWorkspace(request.Operation.RequestDigest) != record.runtimeViewOpen.RequestDigest ||
			request.Operation.RequestDigest != request.CanonicalRequestDigest() {
			return taskworkspace.OpenRuntimeViewRequest{}, Digest{}, newError(ErrorIntegrityConflict)
		}
		return request, record.runtimeViewOpen.RequestDigest, nil
	}
	request, requestDigest, err := runtimeViewOpenRequest(start, record.lease)
	if err != nil {
		return taskworkspace.OpenRuntimeViewRequest{}, Digest{}, err
	}
	record.runtimeViewOpen = &retainedRuntimeViewOpen{Request: request, RequestDigest: requestDigest}
	record.readiness.RuntimeView = PrerequisiteFact{
		State: PrerequisiteReconciliationRequired, OperationID: OperationID{value: string(request.Operation.ID)},
		RequestDigest: requestDigest, Failure: PrerequisiteFailureDependencyUnavailable,
	}
	updateCapsuleReadiness(&record.readiness, record.runtimeViewBinding, record.lease)
	return request, requestDigest, nil
}

func (engine *invariantEngine) persistInMemoryRuntimeViewFact(
	start StartRuntimeRun,
	request taskworkspace.OpenRuntimeViewRequest,
	requestDigest Digest,
	fact PrerequisiteFact,
	binding RuntimeViewBindingSnapshot,
) error {
	engine.store.mu.Lock()
	defer engine.store.mu.Unlock()
	record := engine.store.runtimes[start.RuntimeRunID]
	if record == nil || record.acceptedStartDigest != start.CanonicalRequestDigest ||
		record.runtimeViewOpen == nil || record.runtimeViewOpen.Request != request ||
		record.runtimeViewOpen.RequestDigest != requestDigest {
		return newError(ErrorIntegrityConflict)
	}
	retained := record.readiness.RuntimeView
	if retained.State == PrerequisiteAccepted || retained.State == PrerequisiteRejected {
		if retained.OperationID != fact.OperationID || retained.RequestDigest != requestDigest {
			return newError(ErrorIntegrityConflict)
		}
		return nil
	}
	var lateTerminal *retainedRuntimeViewTerminal
	if fact.State == PrerequisiteAccepted {
		obligation, required := runtimeViewTerminalObligationFor(
			record.fixture.State, record.fixture.Outcome, record.cleanup, record.lease,
		)
		if required && record.runtimeViewTerminal != nil {
			return newError(ErrorIntegrityConflict)
		}
		if required {
			var err error
			lateTerminal, err = retainRuntimeViewTerminalObligation(request, binding, obligation)
			if err != nil {
				return err
			}
		}
	}
	record.readiness.RuntimeView = fact
	if fact.State == PrerequisiteAccepted {
		record.runtimeViewBinding = binding
	} else if binding != (RuntimeViewBindingSnapshot{}) {
		return newError(ErrorIntegrityConflict)
	}
	if lateTerminal != nil {
		record.runtimeViewTerminal = lateTerminal
	}
	updateCapsuleReadiness(&record.readiness, record.runtimeViewBinding, record.lease)
	return nil
}

func runtimeViewOpenRequest(
	start StartRuntimeRun,
	lease RuntimeLeaseSnapshot,
) (taskworkspace.OpenRuntimeViewRequest, Digest, error) {
	requirement := start.RuntimeViewRequirement
	if start.Effect != EffectMutating || requirement == nil ||
		lease.AcquireStatus != LeaseGranted || lease.Disposition != LeaseActive {
		return taskworkspace.OpenRuntimeViewRequest{}, Digest{}, newError(ErrorIntegrityConflict)
	}
	operationID, _ := stablePrerequisiteOperation(
		"c04-open", requirement.OpenOperationDerivation.String(), start.RuntimeRunID.String(),
		start.OperationID.String(), lease.LeaseID.String(), fmt.Sprint(lease.Generation),
		fmt.Sprint(lease.Fence), lease.ExpiresAt.UTC().Format(time.RFC3339Nano),
	)
	effect := taskworkspace.RuntimeViewMutating
	leaseAuthority := taskworkspace.SandboxLeaseAuthority{
		ID:             taskworkspace.SandboxLeaseID(lease.LeaseID.String()),
		EvidenceID:     taskworkspace.EvidenceID("lease-evidence-" + lease.LeaseID.String()),
		AuthorityID:    taskworkspace.SandboxLeaseAuthorityID("runtime-execution-" + lease.NodeAuthorityID.String()),
		PolicyDomainID: taskworkspace.PolicyDomainID(start.PersonalWorkspaceID.String()),
		TaskID:         taskworkspace.TaskID(start.TaskID.String()), PhaseRunID: taskworkspace.PhaseRunID(start.PhaseRunID.String()),
		RuntimeRunID:       taskworkspace.RuntimeRunID(start.RuntimeRunID.String()),
		RuntimeOperationID: taskworkspace.OperationID(start.OperationID.String()), EffectClass: effect,
		LeaseGeneration: taskworkspace.LeaseGeneration(lease.Generation), LeaseFence: taskworkspace.LeaseFence(lease.Fence),
		ExpiresAt: taskworkspace.Instant(lease.ExpiresAt.UnixNano()),
	}
	leaseAuthority.Digest = leaseAuthority.CanonicalDigest()
	request := taskworkspace.OpenRuntimeViewRequest{
		PolicyDomainID: leaseAuthority.PolicyDomainID, TaskID: leaseAuthority.TaskID,
		TaskWorkspaceID:   taskworkspace.TaskWorkspaceID(requirement.TaskWorkspaceID.String()),
		MaterializationID: taskworkspace.MaterializationID(requirement.MaterializationID.String()),
		BaseRevisionID:    taskworkspace.RevisionID(requirement.BaseRevisionID.String()),
		PhaseRunID:        leaseAuthority.PhaseRunID, RuntimeRunID: leaseAuthority.RuntimeRunID,
		RuntimeOperationID: leaseAuthority.RuntimeOperationID, SandboxLeaseAuthority: leaseAuthority,
		EffectClass: effect, ExpiresAt: leaseAuthority.ExpiresAt,
		Generation: taskworkspace.Generation(requirement.LifecycleGeneration),
		Fence:      taskworkspace.Fence(requirement.LifecycleFence),
		Operation:  taskworkspace.Operation{ID: taskworkspace.OperationID(operationID.String())},
	}
	request.Operation.RequestDigest = request.CanonicalRequestDigest()
	requestDigest, err := parseTaskWorkspaceDigest(request.Operation.RequestDigest)
	if err != nil {
		return taskworkspace.OpenRuntimeViewRequest{}, Digest{}, err
	}
	return request, requestDigest, nil
}

func inspectOrReconcileRuntimeViewOpen(
	ctx context.Context,
	port RuntimeViewPrerequisitePort,
	request taskworkspace.OpenRuntimeViewRequest,
	openErr error,
) (taskworkspace.OpenRuntimeViewResult, error) {
	if !runtimeViewOpenMayBeAmbiguous(openErr) {
		return taskworkspace.OpenRuntimeViewResult{}, openErr
	}
	inspectRequest := taskworkspace.InspectOperationRequest{
		PolicyDomainID: request.PolicyDomainID, TaskID: request.TaskID, OperationID: request.Operation.ID,
	}
	inspection, inspectErr := port.InspectOperation(ctx, inspectRequest)
	if inspectErr == nil && inspection.Disposition == taskworkspace.OperationTerminal {
		return runtimeViewResultFromInspection(inspection)
	}
	reconcileRequest := taskworkspace.ReconcileOperationRequest(inspectRequest)
	inspection, reconcileErr := port.ReconcileOperation(ctx, reconcileRequest)
	if reconcileErr != nil {
		return taskworkspace.OpenRuntimeViewResult{}, reconcileErr
	}
	if inspection.Disposition != taskworkspace.OperationTerminal {
		return taskworkspace.OpenRuntimeViewResult{}, &taskworkspace.Error{Code: taskworkspace.ErrorReconciliationRequired}
	}
	return runtimeViewResultFromInspection(inspection)
}

func runtimeViewResultFromInspection(
	inspection taskworkspace.OperationInspection,
) (taskworkspace.OpenRuntimeViewResult, error) {
	if inspection.Error != nil {
		return taskworkspace.OpenRuntimeViewResult{}, inspection.Error
	}
	if inspection.OpenRuntimeView == nil {
		return taskworkspace.OpenRuntimeViewResult{}, &taskworkspace.Error{Code: taskworkspace.ErrorReconciliationRequired}
	}
	return *inspection.OpenRuntimeView, nil
}

func runtimeViewOpenMayBeAmbiguous(err error) bool {
	var lifecycleErr *taskworkspace.Error
	if !errors.As(err, &lifecycleErr) {
		return true
	}
	return lifecycleErr.Code == taskworkspace.ErrorReconciliationRequired ||
		lifecycleErr.Code == taskworkspace.ErrorRetryableUnavailable ||
		lifecycleErr.Code == taskworkspace.ErrorDurabilityUnverified
}

func runtimeViewFactFromResult(
	request taskworkspace.OpenRuntimeViewRequest,
	requestDigest Digest,
	result taskworkspace.OpenRuntimeViewResult,
	err error,
) (PrerequisiteFact, RuntimeViewBindingSnapshot, error) {
	operationID := OperationID{value: string(request.Operation.ID)}
	if err != nil {
		if runtimeViewOpenMayBeAmbiguous(err) {
			return PrerequisiteFact{
				State: PrerequisiteReconciliationRequired, OperationID: operationID,
				RequestDigest: requestDigest, Failure: PrerequisiteFailureDependencyUnavailable,
			}, RuntimeViewBindingSnapshot{}, nil
		}
		failure := PrerequisiteFailureWrongBinding
		var lifecycleErr *taskworkspace.Error
		if errors.As(err, &lifecycleErr) && lifecycleErr.Code == taskworkspace.ErrorStaleAuthority {
			failure = PrerequisiteFailureStale
		}
		return PrerequisiteFact{
			State: PrerequisiteRejected, OperationID: operationID, RequestDigest: requestDigest, Failure: failure,
		}, RuntimeViewBindingSnapshot{}, nil
	}
	if !runtimeViewOpenResultMatches(request, result) {
		return PrerequisiteFact{}, RuntimeViewBindingSnapshot{}, newError(ErrorIntegrityConflict)
	}
	viewID, viewErr := NewRuntimeViewID(string(result.RuntimeViewID))
	authorityDigest, digestErr := parseTaskWorkspaceDigest(request.SandboxLeaseAuthority.Digest)
	if viewErr != nil || digestErr != nil {
		return PrerequisiteFact{}, RuntimeViewBindingSnapshot{}, newError(ErrorIntegrityConflict)
	}
	_, evidenceDigest := stablePrerequisiteOperation(
		"c04-open-evidence", requestDigest.String(), viewID.String(), fmt.Sprint(result.ExpiresAt),
	)
	fact := PrerequisiteFact{
		State: PrerequisiteAccepted, OperationID: operationID, RequestDigest: requestDigest,
		EvidenceID:     EvidenceID{value: fmt.Sprintf("c04-open-evidence-%x", evidenceDigest[:12])},
		EvidenceDigest: evidenceDigest,
	}
	binding := RuntimeViewBindingSnapshot{
		RuntimeViewID: viewID, OpenOperationID: operationID, OpenRequestDigest: requestDigest,
		SandboxLeaseAuthorityDigest: authorityDigest,
		SandboxLeaseID:              SandboxLeaseID{value: string(request.SandboxLeaseAuthority.ID)},
		LeaseGeneration:             LeaseGeneration(request.SandboxLeaseAuthority.LeaseGeneration),
		LeaseFence:                  LeaseFence(request.SandboxLeaseAuthority.LeaseFence), Effect: EffectMutating,
		ExpiresAt:           time.Unix(0, int64(result.ExpiresAt)).UTC(),
		LifecycleGeneration: TaskWorkspaceLifecycleGeneration(result.Generation),
		LifecycleFence:      TaskWorkspaceLifecycleFence(result.Fence),
	}
	return fact, binding, nil
}

func runtimeViewOpenResultMatches(
	request taskworkspace.OpenRuntimeViewRequest,
	result taskworkspace.OpenRuntimeViewResult,
) bool {
	return result.PolicyDomainID == request.PolicyDomainID && result.TaskID == request.TaskID &&
		result.RuntimeViewID != "" && result.TaskWorkspaceID == request.TaskWorkspaceID &&
		result.MaterializationID == request.MaterializationID && result.BaseRevisionID == request.BaseRevisionID &&
		result.PhaseRunID == request.PhaseRunID && result.RuntimeRunID == request.RuntimeRunID &&
		result.RuntimeOperationID == request.RuntimeOperationID &&
		result.SandboxLeaseAuthority == request.SandboxLeaseAuthority && result.EffectClass == request.EffectClass &&
		result.ExpiresAt > 0 && result.ExpiresAt <= request.ExpiresAt &&
		result.Generation == request.Generation && result.Fence == request.Fence && result.Operation == request.Operation
}

func (engine *invariantEngine) advanceInMemoryRuntimeViewFence(
	ctx context.Context,
	runtimeRunID RuntimeRunID,
	reason taskworkspace.RuntimeViewFenceReason,
) error {
	request, shouldDeliver, err := engine.prepareInMemoryRuntimeViewFence(runtimeRunID, reason)
	if err != nil || !shouldDeliver {
		return err
	}
	result, terminalErr := engine.runtimeViewPrerequisite.FenceRuntimeView(ctx, request)
	if terminalErr != nil {
		result, terminalErr = inspectOrReconcileRuntimeViewFence(
			ctx, engine.runtimeViewPrerequisite, request, terminalErr,
		)
	}
	return engine.persistInMemoryRuntimeViewFenceResult(runtimeRunID, request, result, terminalErr)
}

func (engine *invariantEngine) advanceInMemoryRuntimeViewDiscard(
	ctx context.Context,
	runtimeRunID RuntimeRunID,
	reason taskworkspace.RuntimeViewDiscardReason,
) error {
	request, shouldDeliver, err := engine.prepareInMemoryRuntimeViewDiscard(runtimeRunID, reason)
	if err != nil || !shouldDeliver {
		return err
	}
	result, terminalErr := engine.runtimeViewPrerequisite.DiscardRuntimeView(ctx, request)
	if terminalErr != nil {
		result, terminalErr = inspectOrReconcileRuntimeViewDiscard(
			ctx, engine.runtimeViewPrerequisite, request, terminalErr,
		)
	}
	return engine.persistInMemoryRuntimeViewDiscardResult(runtimeRunID, request, result, terminalErr)
}

func (engine *invariantEngine) prepareInMemoryRuntimeViewFence(
	runtimeRunID RuntimeRunID,
	reason taskworkspace.RuntimeViewFenceReason,
) (taskworkspace.FenceRuntimeViewRequest, bool, error) {
	if engine.runtimeViewPrerequisite == nil {
		return taskworkspace.FenceRuntimeViewRequest{}, false, nil
	}
	engine.store.mu.Lock()
	defer engine.store.mu.Unlock()
	record := engine.store.runtimes[runtimeRunID]
	if record == nil {
		return taskworkspace.FenceRuntimeViewRequest{}, false, newError(ErrorIntegrityConflict)
	}
	if record.runtimeViewBinding == (RuntimeViewBindingSnapshot{}) {
		return taskworkspace.FenceRuntimeViewRequest{}, false, nil
	}
	if record.runtimeViewOpen == nil {
		return taskworkspace.FenceRuntimeViewRequest{}, false, newError(ErrorIntegrityConflict)
	}
	if retained := record.runtimeViewTerminal; retained != nil {
		if retained.State == runtimeViewTerminalAccepted || retained.State == runtimeViewTerminalRejected {
			return taskworkspace.FenceRuntimeViewRequest{}, false, nil
		}
		if retained.Kind != runtimeViewTerminalFence || retained.FenceRequest.Reason != reason ||
			retained.FenceRequest.Operation.RequestDigest != retained.FenceRequest.CanonicalRequestDigest() ||
			digestFromTaskWorkspace(retained.FenceRequest.Operation.RequestDigest) != retained.RequestDigest {
			return taskworkspace.FenceRuntimeViewRequest{}, false, newError(ErrorIntegrityConflict)
		}
		return retained.FenceRequest, true, nil
	}
	request, requestDigest, err := runtimeViewFenceRequest(record.runtimeViewOpen.Request, record.runtimeViewBinding, reason)
	if err != nil {
		return taskworkspace.FenceRuntimeViewRequest{}, false, err
	}
	record.runtimeViewTerminal = &retainedRuntimeViewTerminal{
		Kind: runtimeViewTerminalFence, FenceRequest: request,
		RequestDigest: requestDigest, State: runtimeViewTerminalPending,
	}
	return request, true, nil
}

func (engine *invariantEngine) prepareInMemoryRuntimeViewDiscard(
	runtimeRunID RuntimeRunID,
	reason taskworkspace.RuntimeViewDiscardReason,
) (taskworkspace.DiscardRuntimeViewRequest, bool, error) {
	if engine.runtimeViewPrerequisite == nil {
		return taskworkspace.DiscardRuntimeViewRequest{}, false, nil
	}
	engine.store.mu.Lock()
	defer engine.store.mu.Unlock()
	record := engine.store.runtimes[runtimeRunID]
	if record == nil {
		return taskworkspace.DiscardRuntimeViewRequest{}, false, newError(ErrorIntegrityConflict)
	}
	if record.runtimeViewBinding == (RuntimeViewBindingSnapshot{}) {
		return taskworkspace.DiscardRuntimeViewRequest{}, false, nil
	}
	if record.runtimeViewOpen == nil {
		return taskworkspace.DiscardRuntimeViewRequest{}, false, newError(ErrorIntegrityConflict)
	}
	if retained := record.runtimeViewTerminal; retained != nil {
		if retained.State == runtimeViewTerminalAccepted || retained.State == runtimeViewTerminalRejected {
			return taskworkspace.DiscardRuntimeViewRequest{}, false, nil
		}
		if retained.Kind != runtimeViewTerminalDiscard || retained.DiscardRequest.Reason != reason ||
			retained.DiscardRequest.Operation.RequestDigest != retained.DiscardRequest.CanonicalRequestDigest() ||
			digestFromTaskWorkspace(retained.DiscardRequest.Operation.RequestDigest) != retained.RequestDigest {
			return taskworkspace.DiscardRuntimeViewRequest{}, false, newError(ErrorIntegrityConflict)
		}
		return retained.DiscardRequest, true, nil
	}
	request, requestDigest, err := runtimeViewDiscardRequest(record.runtimeViewOpen.Request, record.runtimeViewBinding, reason)
	if err != nil {
		return taskworkspace.DiscardRuntimeViewRequest{}, false, err
	}
	record.runtimeViewTerminal = &retainedRuntimeViewTerminal{
		Kind: runtimeViewTerminalDiscard, DiscardRequest: request,
		RequestDigest: requestDigest, State: runtimeViewTerminalPending,
	}
	return request, true, nil
}

func runtimeViewFenceRequest(
	open taskworkspace.OpenRuntimeViewRequest,
	binding RuntimeViewBindingSnapshot,
	reason taskworkspace.RuntimeViewFenceReason,
) (taskworkspace.FenceRuntimeViewRequest, Digest, error) {
	if !validRuntimeViewOpenBinding(open, binding) {
		return taskworkspace.FenceRuntimeViewRequest{}, Digest{}, newError(ErrorIntegrityConflict)
	}
	operationID, _ := stablePrerequisiteOperation(
		"c04-view-terminal", string(open.Operation.ID), binding.RuntimeViewID.String(),
	)
	request := taskworkspace.FenceRuntimeViewRequest{
		PolicyDomainID: open.PolicyDomainID, TaskID: open.TaskID,
		TaskWorkspaceID: open.TaskWorkspaceID, RuntimeViewID: taskworkspace.RuntimeViewID(binding.RuntimeViewID.String()),
		RuntimeOperationID: open.RuntimeOperationID, SandboxLeaseAuthority: open.SandboxLeaseAuthority,
		BaseRevisionID: open.BaseRevisionID, ExpectedCurrentRevision: open.BaseRevisionID,
		Generation: open.Generation, Fence: open.Fence, Reason: reason,
		Operation: taskworkspace.Operation{ID: taskworkspace.OperationID(operationID.String())},
	}
	request.Operation.RequestDigest = request.CanonicalRequestDigest()
	digest, err := parseTaskWorkspaceDigest(request.Operation.RequestDigest)
	if err != nil {
		return taskworkspace.FenceRuntimeViewRequest{}, Digest{}, err
	}
	return request, digest, nil
}

func runtimeViewDiscardRequest(
	open taskworkspace.OpenRuntimeViewRequest,
	binding RuntimeViewBindingSnapshot,
	reason taskworkspace.RuntimeViewDiscardReason,
) (taskworkspace.DiscardRuntimeViewRequest, Digest, error) {
	if !validRuntimeViewOpenBinding(open, binding) {
		return taskworkspace.DiscardRuntimeViewRequest{}, Digest{}, newError(ErrorIntegrityConflict)
	}
	operationID, _ := stablePrerequisiteOperation(
		"c04-view-terminal", string(open.Operation.ID), binding.RuntimeViewID.String(),
	)
	request := taskworkspace.DiscardRuntimeViewRequest{
		PolicyDomainID: open.PolicyDomainID, TaskID: open.TaskID,
		TaskWorkspaceID: open.TaskWorkspaceID, RuntimeViewID: taskworkspace.RuntimeViewID(binding.RuntimeViewID.String()),
		RuntimeOperationID: open.RuntimeOperationID, SandboxLeaseAuthority: open.SandboxLeaseAuthority,
		BaseRevisionID: open.BaseRevisionID, ExpectedCurrentRevision: open.BaseRevisionID,
		Generation: open.Generation, Fence: open.Fence, Reason: reason,
		Operation: taskworkspace.Operation{ID: taskworkspace.OperationID(operationID.String())},
	}
	request.Operation.RequestDigest = request.CanonicalRequestDigest()
	digest, err := parseTaskWorkspaceDigest(request.Operation.RequestDigest)
	if err != nil {
		return taskworkspace.DiscardRuntimeViewRequest{}, Digest{}, err
	}
	return request, digest, nil
}

func validRuntimeViewOpenBinding(
	open taskworkspace.OpenRuntimeViewRequest,
	binding RuntimeViewBindingSnapshot,
) bool {
	return binding != (RuntimeViewBindingSnapshot{}) && open.Operation.ID != "" &&
		open.Operation.RequestDigest == open.CanonicalRequestDigest() &&
		binding.OpenOperationID.String() == string(open.Operation.ID) &&
		binding.OpenRequestDigest == digestFromTaskWorkspace(open.Operation.RequestDigest) &&
		binding.SandboxLeaseAuthorityDigest == digestFromTaskWorkspace(open.SandboxLeaseAuthority.Digest) &&
		binding.SandboxLeaseID.String() == string(open.SandboxLeaseAuthority.ID) &&
		binding.LeaseGeneration == LeaseGeneration(open.SandboxLeaseAuthority.LeaseGeneration) &&
		binding.LeaseFence == LeaseFence(open.SandboxLeaseAuthority.LeaseFence) &&
		binding.Effect == EffectMutating && open.EffectClass == taskworkspace.RuntimeViewMutating &&
		binding.LifecycleGeneration == TaskWorkspaceLifecycleGeneration(open.Generation) &&
		binding.LifecycleFence == TaskWorkspaceLifecycleFence(open.Fence)
}

func (engine *invariantEngine) persistInMemoryRuntimeViewFenceResult(
	runtimeRunID RuntimeRunID,
	request taskworkspace.FenceRuntimeViewRequest,
	result taskworkspace.FenceRuntimeViewResult,
	terminalErr error,
) error {
	state, code, err := runtimeViewFenceTerminalState(request, result, terminalErr)
	if err != nil {
		return err
	}
	engine.store.mu.Lock()
	defer engine.store.mu.Unlock()
	record := engine.store.runtimes[runtimeRunID]
	if record == nil || record.runtimeViewTerminal == nil ||
		record.runtimeViewTerminal.Kind != runtimeViewTerminalFence ||
		record.runtimeViewTerminal.FenceRequest != request {
		return newError(ErrorIntegrityConflict)
	}
	record.runtimeViewTerminal.State = state
	record.runtimeViewTerminal.ErrorCode = code
	return nil
}

func (engine *invariantEngine) persistInMemoryRuntimeViewDiscardResult(
	runtimeRunID RuntimeRunID,
	request taskworkspace.DiscardRuntimeViewRequest,
	result taskworkspace.DiscardRuntimeViewResult,
	terminalErr error,
) error {
	state, code, err := runtimeViewDiscardTerminalState(request, result, terminalErr)
	if err != nil {
		return err
	}
	engine.store.mu.Lock()
	defer engine.store.mu.Unlock()
	record := engine.store.runtimes[runtimeRunID]
	if record == nil || record.runtimeViewTerminal == nil ||
		record.runtimeViewTerminal.Kind != runtimeViewTerminalDiscard ||
		record.runtimeViewTerminal.DiscardRequest != request {
		return newError(ErrorIntegrityConflict)
	}
	record.runtimeViewTerminal.State = state
	record.runtimeViewTerminal.ErrorCode = code
	return nil
}

func runtimeViewFenceTerminalState(
	request taskworkspace.FenceRuntimeViewRequest,
	result taskworkspace.FenceRuntimeViewResult,
	err error,
) (runtimeViewTerminalState, taskworkspace.ErrorCode, error) {
	if err != nil {
		if runtimeViewOpenMayBeAmbiguous(err) {
			return runtimeViewTerminalReconciliationRequired, taskworkspace.ErrorReconciliationRequired, nil
		}
		return runtimeViewTerminalRejected, normalizedTaskWorkspaceErrorCode(err), nil
	}
	if result.TaskWorkspaceID != request.TaskWorkspaceID || result.RuntimeViewID != request.RuntimeViewID ||
		result.BaseRevisionID != request.BaseRevisionID || result.CurrentRevisionID != request.ExpectedCurrentRevision ||
		result.Reason != request.Reason || result.Generation < request.Generation ||
		result.PreviousFence != request.Fence || result.Fence <= request.Fence || result.Operation != request.Operation {
		return 0, "", newError(ErrorIntegrityConflict)
	}
	return runtimeViewTerminalAccepted, "", nil
}

func runtimeViewDiscardTerminalState(
	request taskworkspace.DiscardRuntimeViewRequest,
	result taskworkspace.DiscardRuntimeViewResult,
	err error,
) (runtimeViewTerminalState, taskworkspace.ErrorCode, error) {
	if err != nil {
		if runtimeViewOpenMayBeAmbiguous(err) {
			return runtimeViewTerminalReconciliationRequired, taskworkspace.ErrorReconciliationRequired, nil
		}
		return runtimeViewTerminalRejected, normalizedTaskWorkspaceErrorCode(err), nil
	}
	if result.TaskWorkspaceID != request.TaskWorkspaceID || result.RuntimeViewID != request.RuntimeViewID ||
		result.BaseRevisionID != request.BaseRevisionID || result.CurrentRevisionID != request.ExpectedCurrentRevision ||
		result.Reason != request.Reason || result.Generation != request.Generation ||
		result.Fence != request.Fence || result.Operation != request.Operation {
		return 0, "", newError(ErrorIntegrityConflict)
	}
	return runtimeViewTerminalAccepted, "", nil
}

func normalizedTaskWorkspaceErrorCode(err error) taskworkspace.ErrorCode {
	var lifecycleErr *taskworkspace.Error
	if errors.As(err, &lifecycleErr) {
		return lifecycleErr.Code
	}
	return taskworkspace.ErrorRetryableUnavailable
}

func inspectOrReconcileRuntimeViewFence(
	ctx context.Context,
	port RuntimeViewPrerequisitePort,
	request taskworkspace.FenceRuntimeViewRequest,
	terminalErr error,
) (taskworkspace.FenceRuntimeViewResult, error) {
	inspection, err := inspectOrReconcileRuntimeViewTerminal(
		ctx, port, request.PolicyDomainID, request.TaskID, request.Operation.ID, terminalErr,
	)
	if err != nil {
		return taskworkspace.FenceRuntimeViewResult{}, err
	}
	if inspection.Error != nil {
		return taskworkspace.FenceRuntimeViewResult{}, inspection.Error
	}
	if inspection.FenceRuntimeView == nil {
		return taskworkspace.FenceRuntimeViewResult{}, &taskworkspace.Error{Code: taskworkspace.ErrorReconciliationRequired}
	}
	return *inspection.FenceRuntimeView, nil
}

func inspectOrReconcileRuntimeViewDiscard(
	ctx context.Context,
	port RuntimeViewPrerequisitePort,
	request taskworkspace.DiscardRuntimeViewRequest,
	terminalErr error,
) (taskworkspace.DiscardRuntimeViewResult, error) {
	inspection, err := inspectOrReconcileRuntimeViewTerminal(
		ctx, port, request.PolicyDomainID, request.TaskID, request.Operation.ID, terminalErr,
	)
	if err != nil {
		return taskworkspace.DiscardRuntimeViewResult{}, err
	}
	if inspection.Error != nil {
		return taskworkspace.DiscardRuntimeViewResult{}, inspection.Error
	}
	if inspection.DiscardRuntimeView == nil {
		return taskworkspace.DiscardRuntimeViewResult{}, &taskworkspace.Error{Code: taskworkspace.ErrorReconciliationRequired}
	}
	return *inspection.DiscardRuntimeView, nil
}

func inspectOrReconcileRuntimeViewTerminal(
	ctx context.Context,
	port RuntimeViewPrerequisitePort,
	policyDomainID taskworkspace.PolicyDomainID,
	taskID taskworkspace.TaskID,
	operationID taskworkspace.OperationID,
	terminalErr error,
) (taskworkspace.OperationInspection, error) {
	if !runtimeViewOpenMayBeAmbiguous(terminalErr) {
		return taskworkspace.OperationInspection{}, terminalErr
	}
	request := taskworkspace.InspectOperationRequest{
		PolicyDomainID: policyDomainID, TaskID: taskID, OperationID: operationID,
	}
	inspection, inspectErr := port.InspectOperation(ctx, request)
	if inspectErr == nil && inspection.Disposition == taskworkspace.OperationTerminal {
		return inspection, nil
	}
	inspection, reconcileErr := port.ReconcileOperation(ctx, taskworkspace.ReconcileOperationRequest(request))
	if reconcileErr != nil {
		return taskworkspace.OperationInspection{}, reconcileErr
	}
	if inspection.Disposition != taskworkspace.OperationTerminal {
		return taskworkspace.OperationInspection{}, &taskworkspace.Error{Code: taskworkspace.ErrorReconciliationRequired}
	}
	return inspection, nil
}

func parseTaskWorkspaceDigest(value taskworkspace.Digest) (Digest, error) {
	text := string(value)
	if !strings.HasPrefix(text, "sha256:") {
		return Digest{}, newError(ErrorIntegrityConflict)
	}
	decoded, err := hex.DecodeString(strings.TrimPrefix(text, "sha256:"))
	if err != nil || len(decoded) != len(Digest{}) {
		return Digest{}, newError(ErrorIntegrityConflict)
	}
	var digest Digest
	copy(digest[:], decoded)
	return digest, nil
}

func digestFromTaskWorkspace(value taskworkspace.Digest) Digest {
	digest, _ := parseTaskWorkspaceDigest(value)
	return digest
}

func knownRuntimeViewBinding(binding RuntimeViewBindingSnapshot, readiness RuntimeReadinessSnapshot) bool {
	if binding == (RuntimeViewBindingSnapshot{}) {
		return readiness == (RuntimeReadinessSnapshot{}) || readiness.RuntimeView.State != PrerequisiteAccepted
	}
	return readiness.RuntimeView.State == PrerequisiteAccepted &&
		validOpaqueID(binding.RuntimeViewID.String()) && validOpaqueID(binding.OpenOperationID.String()) &&
		binding.OpenRequestDigest != (Digest{}) && binding.SandboxLeaseAuthorityDigest != (Digest{}) &&
		validOpaqueID(binding.SandboxLeaseID.String()) && binding.LeaseGeneration > 0 && binding.LeaseFence > 0 &&
		binding.Effect == EffectMutating && !binding.ExpiresAt.IsZero() &&
		binding.LifecycleGeneration > 0 && binding.LifecycleFence > 0
}

func leasePrerequisiteFact(lease RuntimeLeaseSnapshot) PrerequisiteFact {
	operationID, evidenceDigest := stablePrerequisiteOperation(
		"lease-evidence", lease.LeaseID.String(), fmt.Sprint(lease.Generation), fmt.Sprint(lease.Fence),
		lease.AcquireOperationID.String(), lease.AcquireDigest.String(),
	)
	return PrerequisiteFact{
		State: PrerequisiteAccepted, OperationID: operationID, RequestDigest: lease.AcquireDigest,
		EvidenceID: EvidenceID{value: "lease-evidence-" + lease.LeaseID.String()}, EvidenceDigest: evidenceDigest,
	}
}
