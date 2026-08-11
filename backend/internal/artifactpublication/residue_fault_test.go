package artifactpublication

// This file proves the C05-05 residue/debt flows are crash- and
// response-loss-safe (child SPEC #108): a crash after the physical release
// or after the durable assembly/debt write but before the response never
// freezes the residue and never duplicates a DebtID; retries re-evaluate
// the ORIGINAL operation or exact-replay the recorded decision.

import (
	"context"
	"errors"
	"testing"
)

// residueFaultFixture wires a FaultHook that fails once at the response
// boundary for one operation and one intent kind, with the residue release
// port and cleanup authority registered.
type residueFaultFixture struct {
	*fixture
	faultAt     FaultPoint
	operationID string
	intentKind  PublicationIntentKind
	failed      []FaultEvent
}

func newResidueFaultFixture(t *testing.T, point FaultPoint, operationID string, intentKind PublicationIntentKind) *residueFaultFixture {
	t.Helper()
	f := newFixture(t)
	ff := &residueFaultFixture{fixture: f, faultAt: point, operationID: operationID, intentKind: intentKind}
	fired := false
	ff.core = NewInMemory(InMemoryConfig{
		Now:                          func() Instant { return f.now },
		RuntimeAuthorityID:           f.runtimeAuthority,
		ValidationAuthorityID:        f.validationAuthority,
		C04AuthorityID:               f.c04Authority,
		DurableObjectAuthorityID:     f.durableObjectAuthority,
		TaskOrchestrationAuthorityID: f.taskOrchestrationAuthority,
		RecoveryAuthorityID:          f.recoveryAuthority,
		CleanupAuthorityID:           f.cleanupAuthority,
		PublicationAuthorityID:       f.publicationAuthority,
		CurrentContentCapability:     f.registry.resolve,
		CurrentContentScope:          f.scopes.resolve,
		ReleaseStaging:               f.releaseStaging,
		FaultHook: func(event FaultEvent) error {
			ff.failed = append(ff.failed, event)
			if event.Point == point && string(event.OperationID) == operationID && event.IntentKind == intentKind && !fired {
				fired = true
				return errors.New("injected fault")
			}
			return nil
		},
	}, f.persistence)
	return ff
}

// TestReleaseFaultBeforeResponseReEvaluates proves a crash after the
// physical release but before the response (ack/response loss) leaves the
// release facts durable; the retry re-evaluates the ORIGINAL operation
// against the Durable Object registry and returns the same evidence-backed
// closure.
func TestReleaseFaultBeforeResponseReEvaluates(t *testing.T) {
	ff := newResidueFaultFixture(t, FaultBeforeResponse, "op-release-fault", IntentReleaseResidue)
	ff.releaseStaging = func(refs []stagingRecord, safetyEpoch SafetyEpoch) (ReleaseReceipt, bool, error) {
		return ff.releaseReceipt("receipt-fault", ReleaseOutcomeReleased), true, nil
	}
	ff.rebuildWithRelease()
	operationID := "op-release-fault"

	rejectOperation(t, ff.fixture, operationID)
	if _, err := ff.core.Mutate(context.Background(), ff.releaseResidueIntent(operationID)); err == nil {
		t.Fatal("fault before response must abort the call")
	}
	// The residue state is durable (release-requested with a recorded
	// receipt) even though the response was lost.
	residue := ff.queryResidue(t, operationID)
	if residue.Disposition != ResidueReleased || residue.ReleaseReceipt == nil {
		t.Fatalf("residue after crash-before-response = %#v, want durable released state", residue)
	}
	// The retry re-evaluates and returns the same decision.
	decision, err := ff.core.Mutate(context.Background(), ff.releaseResidueIntent(operationID))
	if err != nil {
		t.Fatalf("retry release after response loss: %v", err)
	}
	if decision.ResidueDisposition != ResidueReleased {
		t.Fatalf("retry release disposition = %s, want released", decision.ResidueDisposition)
	}
	// No version was ever created by the release lifecycle.
	history, err := ff.core.Query(context.Background(), PublicationQuery{
		Kind: QueryVersionHistory, PolicyDomainID: ff.policyDomain, TaskID: ff.taskID,
	})
	if !isCode(err, ErrorNotFound) {
		t.Fatalf("history = %#v err=%v, want not found", history, err)
	}
}

// rebuildWithRelease rebuilds the authority keeping the current release
// port and clock (used by the residue fault fixture after the port is set).
func (ff *residueFaultFixture) rebuildWithRelease() {
	f := ff.fixture
	ff.core = NewInMemory(InMemoryConfig{
		Now:                          func() Instant { return f.now },
		RuntimeAuthorityID:           f.runtimeAuthority,
		ValidationAuthorityID:        f.validationAuthority,
		C04AuthorityID:               f.c04Authority,
		DurableObjectAuthorityID:     f.durableObjectAuthority,
		TaskOrchestrationAuthorityID: f.taskOrchestrationAuthority,
		RecoveryAuthorityID:          f.recoveryAuthority,
		CleanupAuthorityID:           f.cleanupAuthority,
		PublicationAuthorityID:       f.publicationAuthority,
		CurrentContentCapability:     f.registry.resolve,
		CurrentContentScope:          f.scopes.resolve,
		ReleaseStaging:               f.releaseStaging,
		FaultHook:                    ff.core.(*inMemory).config.FaultHook,
	}, f.persistence)
}

// TestRecordAssemblyFaultBeforeResponseNeverDuplicatesDebt proves a crash
// after the debt write but before the response exact-replays the original
// DebtID and never mints a duplicate.
func TestRecordAssemblyFaultBeforeResponseNeverDuplicatesDebt(t *testing.T) {
	ff := newResidueFaultFixture(t, FaultBeforeResponse, "op-assembly-fault", IntentRecordResidueAssembly)
	operationID := "op-assembly-fault"

	rejectedWithResidue(t, ff.fixture, operationID)
	assembly := ff.assemblyReference()
	if _, err := ff.core.Mutate(context.Background(), ff.recordAssemblyIntent(operationID, assembly)); err == nil {
		t.Fatal("fault before response must abort the call")
	}
	replayed, err := ff.core.Mutate(context.Background(), ff.recordAssemblyIntent(operationID, assembly))
	if err != nil {
		t.Fatalf("retry record assembly: %v", err)
	}
	if !replayed.Replay || replayed.CleanupDebtID == "" {
		t.Fatalf("retry must exact-replay the original debt: %#v", replayed)
	}
	// Exactly one debt exists for the operation.
	debt := ff.queryDebt(t, operationID)
	if debt.DebtID != replayed.CleanupDebtID {
		t.Fatalf("exactly one debt expected, got %#v", debt)
	}
}

// TestResolveDebtFaultBeforeResponseReplay proves a crash after the
// resolution write but before the response exact-replays the resolved
// decision; the debt is not resolved twice.
func TestResolveDebtFaultBeforeResponseReplay(t *testing.T) {
	ff := newResidueFaultFixture(t, FaultBeforeResponse, "op-resolve-fault", IntentResolveCleanupDebt)
	operationID := "op-resolve-fault"

	rejectedWithResidue(t, ff.fixture, operationID)
	assembly := ff.assemblyReference()
	if _, err := ff.core.Mutate(context.Background(), ff.recordAssemblyIntent(operationID, assembly)); err != nil {
		t.Fatalf("record assembly: %v", err)
	}
	evidence := ff.cleanupResolutionEvidence(assembly.IdentityDigest, assembly.Generation, assembly.Fence)
	if _, err := ff.core.Mutate(context.Background(), ff.resolveDebtIntent(operationID, CleanupResolutionReclaimed, evidence)); err == nil {
		t.Fatal("fault before response must abort the call")
	}
	replayed, err := ff.core.Mutate(context.Background(), ff.resolveDebtIntent(operationID, CleanupResolutionReclaimed, evidence))
	if err != nil {
		t.Fatalf("retry resolve debt: %v", err)
	}
	if !replayed.Replay || replayed.CleanupDebtStatus != CleanupDebtResolved ||
		replayed.ResolutionClass != CleanupResolutionReclaimed {
		t.Fatalf("retry must exact-replay the resolution: %#v", replayed)
	}
}
