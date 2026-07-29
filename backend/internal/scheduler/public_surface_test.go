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
		"ApplyPhysicalCapacityReleaseReady",
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

func TestPersistedSchedulerStateCodesRemainBackwardCompatible(t *testing.T) {
	t.Parallel()

	grantStates := map[scheduler.GrantState]uint8{
		scheduler.GrantReservedUnbound: 1,
		scheduler.GrantBound:           2,
		scheduler.GrantExpiredUnbound:  3,
		scheduler.GrantTerminalNoLease: 4,
		scheduler.GrantReleased:        5,
		scheduler.GrantLeaseAttached:   6,
	}
	for state, want := range grantStates {
		if got := uint8(state); got != want {
			t.Errorf("persisted grant state %v = %d, want %d", state, got, want)
		}
	}

	reservationStates := map[scheduler.ReservationState]uint8{
		scheduler.ReservationReservedUnbound: 1,
		scheduler.ReservationBound:           2,
		scheduler.ReservationLeaseAttached:   3,
		scheduler.ReservationReleased:        5,
	}
	for state, want := range reservationStates {
		if got := uint8(state); got != want {
			t.Errorf("persisted reservation state %v = %d, want %d", state, got, want)
		}
	}
}
