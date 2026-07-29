package runtimeexecution

import (
	"reflect"
	"sort"
	"testing"
)

func TestPublicSurfaceHasOnlyExecuteAndInspect(t *testing.T) {
	t.Parallel()

	interfaceType := reflect.TypeOf((*RuntimeExecution)(nil)).Elem()
	if interfaceType.NumMethod() != 2 {
		t.Fatalf("RuntimeExecution has %d methods, want 2", interfaceType.NumMethod())
	}
	methods := []string{interfaceType.Method(0).Name, interfaceType.Method(1).Name}
	sort.Strings(methods)
	if !reflect.DeepEqual(methods, []string{"Execute", "Inspect"}) {
		t.Fatalf("public Runtime methods = %v", methods)
	}
	for _, forbidden := range []string{"AcquireLease", "GrantLease", "ReleaseLease", "SetLease", "MutateLease"} {
		if _, found := interfaceType.MethodByName(forbidden); found {
			t.Fatalf("RuntimeExecution exposes caller-controlled lease mutation %q", forbidden)
		}
	}

	commandType := reflect.TypeOf((*RuntimeCommand)(nil)).Elem()
	if commandType.NumMethod() != 2 {
		t.Fatalf("RuntimeCommand marker surface = %d methods", commandType.NumMethod())
	}
	for index := 0; index < commandType.NumMethod(); index++ {
		if commandType.Method(index).PkgPath == "" {
			t.Fatalf("RuntimeCommand method %q is externally implementable", commandType.Method(index).Name)
		}
	}
	commands := []RuntimeCommand{StartRuntimeRun{}, CancelRuntimeRun{}}
	for _, command := range commands {
		switch command.commandKind() {
		case CommandStartRuntimeRun, CommandCancelRuntimeRun:
		default:
			t.Fatalf("unknown public command variant %T", command)
		}
	}

	snapshotType := reflect.TypeOf(RuntimeSnapshot{})
	for index := 0; index < snapshotType.NumField(); index++ {
		field := snapshotType.Field(index)
		switch field.Type.Kind() {
		case reflect.Map, reflect.Slice, reflect.Pointer, reflect.Interface, reflect.Func, reflect.Chan:
			t.Fatalf("snapshot field %s is caller-mutable through %s", field.Name, field.Type)
		}
	}
	decisionType := reflect.TypeOf(RuntimeDecision{})
	if decisionType.NumField() != 2 || decisionType.Field(0).Type != reflect.TypeOf(RuntimeDecisionFact{}) ||
		decisionType.Field(1).Type != reflect.TypeOf(RuntimeSnapshot{}) {
		t.Fatalf("RuntimeDecision exposes non-durable completion mechanics: %v", decisionType)
	}

	assertIndependentType(t, RuntimeRevision(0), OperationGeneration(0))
	assertIndependentType(t, RuntimeRevision(0), RuntimeFence(0))
	assertIndependentType(t, RuntimeFence(0), LeaseFence(0))
	assertIndependentType(t, LeaseGeneration(0), AuthorizationGeneration(0))
	assertIndependentType(t, AuthorizationGeneration(0), ReleaseSafetyEpoch(0))
	assertIndependentType(t, ReleaseSafetyEpoch(0), CatalogSafetyEpoch(0))
	assertIndependentType(t, AdmissionGrantGeneration(0), QuotaReservationGeneration(0))
	assertIndependentType(t, TaskWorkspaceLifecycleGeneration(0), OperationGeneration(0))
	assertIndependentType(t, TaskWorkspaceLifecycleFence(0), RuntimeFence(0))
	assertIndependentType(t, RuntimeRunID{}, OperationID{})
	assertIndependentType(t, EvidenceID{}, EvidenceRootID{})
	assertIndependentType(t, ExecutionNodeID{}, SandboxLeaseID{})
	assertIndependentType(t, TaskWorkspaceID{}, RuntimeRunID{})
	assertIndependentType(t, TaskWorkspaceMaterializationID{}, TaskWorkspaceRevisionID{})
	assertIndependentType(t, TemplateLockID{}, RuntimeBindingID{})
	assertIndependentType(t, QuotaReservationID{}, AdmissionGrantID{})
	assertIndependentType(t, TaskWorkspaceRevisionID{}, RuntimeRevision(0))
	assertIndependentType(t, OperationID{}, RuntimeDecisionID{})
	assertIndependentType(t, RuntimeDecisionID{}, EvidenceRootID{})
}

func TestLeaseLifecycleUsesIndependentClosedMaintenanceSurface(t *testing.T) {
	t.Parallel()
	maintenance := reflect.TypeOf((*RuntimeMaintenance)(nil)).Elem()
	if maintenance.NumMethod() != 1 || maintenance.Method(0).Name != "Maintain" {
		t.Fatalf("RuntimeMaintenance methods = %v, want only Maintain", maintenance)
	}
	command := reflect.TypeOf((*RuntimeMaintenanceCommand)(nil)).Elem()
	if command.NumMethod() != 3 {
		t.Fatalf("RuntimeMaintenanceCommand marker surface = %d methods", command.NumMethod())
	}
	for index := 0; index < command.NumMethod(); index++ {
		if command.Method(index).PkgPath == "" {
			t.Fatalf("RuntimeMaintenanceCommand method %q is externally implementable", command.Method(index).Name)
		}
	}
	for _, variant := range []RuntimeMaintenanceCommand{
		RenewSandboxLease{}, FenceSandboxLease{}, ConfirmSandboxReset{}, AttestExecutionNode{},
	} {
		if variant == nil {
			t.Fatal("maintenance variant is nil")
		}
	}
}

func assertIndependentType(t *testing.T, left, right any) {
	t.Helper()
	leftType := reflect.TypeOf(left)
	rightType := reflect.TypeOf(right)
	if leftType == rightType || leftType.AssignableTo(rightType) || rightType.AssignableTo(leftType) {
		t.Fatalf("authority types are not independent: %s and %s", leftType, rightType)
	}
}
