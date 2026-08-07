package runtimeexecution

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
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
	assertIndependentType(t, GatewayRecoveryGeneration(0), AuthorizationGeneration(0))
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

func TestGatewayUsageContractsCarryNoProviderOrPersistenceAuthority(t *testing.T) {
	types := []reflect.Type{
		reflect.TypeOf(ProviderExecutionBinding{}),
		reflect.TypeOf(GatewayGrantRequest{}),
		reflect.TypeOf(GatewayGrant{}),
		reflect.TypeOf(GatewayGrantDecision{}),
		reflect.TypeOf(GatewayRecoverySnapshot{}),
		reflect.TypeOf(GatewayCallRequest{}),
		reflect.TypeOf(GatewayCallAuthorityFact{}),
		reflect.TypeOf(GatewayCallExternalAuthorityFact{}),
		reflect.TypeOf(GatewayCallDecision{}),
		reflect.TypeOf(GatewayAttemptSettlement{}),
		reflect.TypeOf(UsageReceiptReference{}),
		reflect.TypeOf(RuntimeUsageEvidenceSnapshot{}),
		reflect.TypeOf(postgresGatewayGrantRequestState{}),
		reflect.TypeOf(postgresGatewayGrantDecisionState{}),
	}
	forbidden := []string{
		"credential", "api_key", "apikey", "access_token", "provider_endpoint", "direct_endpoint",
		"provider_url", "vendor", "raw_response", "provider_response", "dsn", "sql", "file_path", "host_path",
	}
	visited := make(map[reflect.Type]bool)
	var inspect func(reflect.Type, string)
	inspect = func(typ reflect.Type, path string) {
		for typ.Kind() == reflect.Pointer {
			typ = typ.Elem()
		}
		if visited[typ] || typ.PkgPath() != reflect.TypeOf(RuntimeSnapshot{}).PkgPath() || typ.Kind() != reflect.Struct {
			return
		}
		visited[typ] = true
		for index := 0; index < typ.NumField(); index++ {
			field := typ.Field(index)
			name := strings.ToLower(field.Name)
			jsonName := strings.ToLower(strings.Split(field.Tag.Get("json"), ",")[0])
			for _, fragment := range forbidden {
				if strings.Contains(name, fragment) || strings.Contains(jsonName, fragment) {
					t.Fatalf("%s.%s exposes forbidden authority fragment %q", path, field.Name, fragment)
				}
			}
			inspect(field.Type, path+"."+field.Name)
		}
	}
	for _, typ := range types {
		inspect(typ, typ.Name())
	}

	encoded, err := json.Marshal(postgresGatewayGrantRequestState{})
	if err != nil {
		t.Fatal(err)
	}
	wireKeys := strings.ToLower(string(encoded))
	for _, fragment := range forbidden {
		if strings.Contains(wireKeys, fragment) {
			t.Fatalf("Gateway persistence wire contains forbidden authority key %q: %s", fragment, wireKeys)
		}
	}

	grantPort := reflect.TypeOf((*GatewayGrantAdapter)(nil)).Elem()
	callPort := reflect.TypeOf((*GatewayCallAccess)(nil)).Elem()
	validator := reflect.TypeOf((*GatewayCallAuthorityValidator)(nil)).Elem()
	externalAuthority := reflect.TypeOf((*GatewayCallExternalAuthority)(nil)).Elem()
	if grantPort.NumMethod() != 2 || callPort.NumMethod() != 2 || validator.NumMethod() != 1 ||
		validator.Method(0).Name != "ValidateGatewayCall" || externalAuthority.NumMethod() != 1 ||
		externalAuthority.Method(0).Name != "ValidateGatewayCallExternalAuthority" {
		t.Fatalf("Gateway internal seams widened: grant=%v call=%v validator=%v external=%v",
			grantPort, callPort, validator, externalAuthority)
	}
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
		CreateCleanupObligation{}, RecordCleanupAttempt{}, ResolveCleanupDebt{},
		ExpireCleanupDebtException{}, ReopenCleanupDebt{},
	} {
		if variant == nil {
			t.Fatal("maintenance variant is nil")
		}
		if _, isPublic := any(variant).(RuntimeCommand); isPublic {
			t.Fatalf("maintenance variant %T leaked into the public Execute/Inspect union", variant)
		}
	}
}

func TestOperationalDiagnosticsIsReadOnlyAndNonEnumerating(t *testing.T) {
	t.Parallel()
	diagnostics := reflect.TypeOf((*OperationalDiagnostics)(nil)).Elem()
	if diagnostics.NumMethod() != 1 || diagnostics.Method(0).Name != "Diagnose" {
		t.Fatalf("OperationalDiagnostics methods = %v, want only read-only Diagnose", diagnostics)
	}
	queryType := reflect.TypeOf(OperationalDiagnosticQuery{})
	if queryType.NumField() != 9 {
		t.Fatalf("OperationalDiagnosticQuery has %d fields, want closed 8", queryType.NumField())
	}
	viewType := reflect.TypeOf(OperationalDiagnosticView{})
	for index := 0; index < viewType.NumField(); index++ {
		field := viewType.Field(index)
		if field.Type.Kind() == reflect.Slice || field.Type.Kind() == reflect.Map {
			t.Fatalf("diagnostic view field %s is enumerating through %s", field.Name, field.Type)
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
