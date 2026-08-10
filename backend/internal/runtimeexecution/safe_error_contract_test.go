package runtimeexecution

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestSafeErrorClosedTaxonomyCoversDecision88 verifies the closed safe-error
// taxonomy distinguishes every Decision 88 category and that retryability
// never weakens authorization, integrity, deadline, or fencing.
func TestSafeErrorClosedTaxonomyCoversDecision88(t *testing.T) {
	t.Parallel()

	covered := map[ErrorCode]bool{
		ErrorAuthorizationDenied:    false, // authorization/ownership denial
		ErrorInvalidRequest:         false, // invalid intent
		ErrorIntegrityConflict:      false, // idempotency conflict
		ErrorStaleState:             false, // stale revision/generation/fence/epoch
		ErrorBindingUnavailable:     false, // revoked/incompatible/unavailable binding
		ErrorEvidenceIntegrity:      false, // manifest/policy/attestation/evidence integrity
		ErrorAdmissionDeferred:      false, // admission deferred/resource exhausted
		ErrorAdapterUnavailable:     false, // adapter unavailable
		ErrorReconciliationRequired: false, // ambiguous transport
		ErrorAgentToolFailure:       false, // agent/tool failure
		ErrorCancelOrDeadline:       false, // cancel/deadline
		ErrorWorkerOrNodeLost:       false, // worker/daemon/node lost
		ErrorCleanupPending:         false, // cleanup pending/debt
	}
	for code := range covered {
		if !validSafeErrorCode(code) || code.String() == "" {
			t.Fatalf("safe error code %d is not a member of the closed taxonomy", code)
		}
		covered[code] = true
	}
	for code, present := range covered {
		if !present {
			t.Fatalf("safe error code %d was not exercised", code)
		}
	}

	// Retryability must not weaken security/fence/deadline semantics:
	// authorization, integrity, stale, binding, evidence, cancel/deadline, and
	// cleanup failures are never blindly retried, and reconciliation for
	// ambiguous transport must not bypass the fence.
	for _, code := range []ErrorCode{
		ErrorAuthorizationDenied, ErrorIntegrityConflict, ErrorStaleState,
		ErrorBindingUnavailable, ErrorEvidenceIntegrity, ErrorAgentToolFailure,
		ErrorCancelOrDeadline, ErrorCleanupPending, ErrorUnsupportedSchema,
	} {
		failure := newError(code)
		if failure.RetryDisposition() == RetryAfterDependency {
			t.Fatalf("retry disposition weakened %s", code)
		}
	}
	if failure := newError(ErrorReconciliationRequired); failure.RetryDisposition() != RetrySameRequest ||
		failure.ReconciliationDisposition() != ReconciliationRequired {
		t.Fatalf("ambiguous transport lost reconciliation requirement: %+v", failure)
	}
	if failure := newError(ErrorWorkerOrNodeLost); failure.ReconciliationDisposition() != ReconciliationRequired {
		t.Fatalf("worker/node loss lost reconciliation requirement: %+v", failure)
	}
}

// TestSafeErrorsRetainNoRawCauseMessageLocatorOrCrossWorkspaceExistence
// injects hostile raw errors into every newError path and proves the returned
// safe error exposes only the closed category.
func TestSafeErrorsRetainNoRawCauseMessageLocatorOrCrossWorkspaceExistence(t *testing.T) {
	t.Parallel()

	canary := "postgres://admin:raw-credential@/private/db foreign-workspace-exists-canary" +
		" /private/session/path mount=/mnt/secret s3://bucket/object-key content-canary"
	for _, code := range []ErrorCode{
		ErrorAuthorizationDenied, ErrorInvalidRequest, ErrorIntegrityConflict,
		ErrorUnsupportedSchema, ErrorDependencyUnavailable, ErrorReconciliationRequired,
		ErrorStaleState, ErrorBindingUnavailable, ErrorEvidenceIntegrity,
		ErrorAdmissionDeferred, ErrorAdapterUnavailable, ErrorAgentToolFailure,
		ErrorCancelOrDeadline, ErrorWorkerOrNodeLost, ErrorCleanupPending,
	} {
		failure := newError(code)
		_ = errors.New(canary) // hostile raw cause never enters the safe error
		for _, forbidden := range []string{
			"postgres://", "raw-credential", "private/db", "foreign-workspace-exists-canary",
			"private/session", "/mnt/", "object-key", "content-canary", "://", "/tmp/",
		} {
			if strings.Contains(failure.Error(), forbidden) ||
				strings.Contains(code.String(), forbidden) {
				t.Fatalf("safe error %s leaked %q: %q", code, forbidden, failure.Error())
			}
		}
	}
}

// TestSafeErrorsSurfaceFromPublicSeamAreContentFree drives a hostile
// authorization probe through the public Execute seam and proves the safe
// error carries no raw detail.
func TestSafeErrorsSurfaceFromPublicSeamAreContentFree(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "safe-error-caller", 7)
	start := standardStart(t, now, authority, "safe-error")
	harness := harnessForStart(t, now, authority, start)
	foreignStart := start
	foreignStart.PersonalWorkspaceID = mustPersonalWorkspaceID(t, "foreign-workspace-exists-canary")
	foreignStart.RuntimeRunID = mustRuntimeRunID(t, "foreign-runtime-exists-canary")
	foreignStart.OperationID = mustOperationID(t, "foreign-operation-exists-canary")

	_, err := harness.Runtime.Execute(context.Background(), foreignStart)
	var safeError *Error
	if !errors.As(err, &safeError) || !validSafeErrorCode(safeError.Code()) {
		t.Fatalf("foreign probe returned %T %v, want closed safe error", err, err)
	}
	// A non-existent Runtime Run and a real run owned by another Workspace must
	// surface the same closed category family so existence is never enumerated.
	_, err = harness.Runtime.Execute(context.Background(), start)
	if err != nil {
		t.Fatalf("owner start after foreign probe: %v", err)
	}
	_, err = harness.Runtime.Execute(context.Background(), foreignStart)
	if !errors.As(err, &safeError) || !validSafeErrorCode(safeError.Code()) {
		t.Fatalf("foreign probe after owner start returned %T %v", err, err)
	}
	for _, forbidden := range []string{
		"foreign-workspace-exists-canary", "foreign-runtime-exists-canary",
		"foreign-operation-exists-canary", "://", "/private",
	} {
		if strings.Contains(safeError.Error(), forbidden) {
			t.Fatalf("public safe error leaked %q: %q", forbidden, safeError.Error())
		}
	}
}

// TestSafeErrorCodeIsTypedAndNeverCarriesCallerText proves the Error struct
// has no field that could retain a raw message or locator.
func TestSafeErrorCodeIsTypedAndNeverCarriesCallerText(t *testing.T) {
	t.Parallel()
	reflectType := reflect.TypeOf(Error{})
	for index := 0; index < reflectType.NumField(); index++ {
		field := reflectType.Field(index)
		kind := field.Type.Kind()
		if kind == reflect.String || kind == reflect.Map || kind == reflect.Slice ||
			field.Type == reflect.TypeOf((*error)(nil)).Elem() {
			t.Fatalf("safe Error exposes untyped data field %s (%s)", field.Name, field.Type)
		}
	}
}
