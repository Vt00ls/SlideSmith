package scheduler_test

import (
	"reflect"
	"sort"
	"testing"

	"github.com/slidesmith/slidesmith/backend/internal/scheduler"
)

func TestSchedulingSurfaceHasNoGeneralCapacityReleaseOrNodeReadinessMutation(t *testing.T) {
	t.Parallel()

	interfaceType := reflect.TypeOf((*scheduler.Scheduling)(nil)).Elem()
	methods := make([]string, interfaceType.NumMethod())
	for index := 0; index < interfaceType.NumMethod(); index++ {
		methods[index] = interfaceType.Method(index).Name
	}
	sort.Strings(methods)
	want := []string{
		"ApplyNoLeasePhysicalDisposition",
		"ApplyRuntimeFencedOrTerminal",
		"ClaimAndAdmit",
		"ClaimCancellation",
		"Inspect",
	}
	if !reflect.DeepEqual(methods, want) {
		t.Fatalf("Scheduling methods = %v, want restricted authority surface %v", methods, want)
	}
	for _, forbidden := range []string{
		"ReleaseCapacity", "ReleaseNodeCapacity", "SetNodeReady", "MarkNodeReady", "UpdateNodeReadiness",
	} {
		if _, found := interfaceType.MethodByName(forbidden); found {
			t.Fatalf("Scheduling exposes forbidden general mutation %q", forbidden)
		}
	}
}
