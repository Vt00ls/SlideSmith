package taskworkspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestLocalFilesystemUsesRandomTemporaryEntriesAndImmutablePromotedFiles(t *testing.T) {
	store, _ := newLocalStoreForTest(t)
	request := localPrepareRequestForTest("prepare-random", true)
	temporaryEntries := make(map[string]struct{})
	store.fault = func(event LocalFilesystemFaultEvent) error {
		if event.Point != LocalFaultBeforeWrite || event.OperationID != request.Operation.ID {
			return nil
		}
		for _, residue := range store.state.Residues {
			if residue.OperationID == request.Operation.ID && residue.TemporaryEntry != "" {
				temporaryEntries[residue.TemporaryEntry] = struct{}{}
			}
		}
		return nil
	}
	prepared, err := store.PrepareCheckpoint(context.Background(), request)
	if err != nil {
		t.Fatalf("prepare local Checkpoint: %v", err)
	}
	if len(temporaryEntries) != len(request.Manifest.Members)+1 {
		t.Fatalf("temporary entries = %v", temporaryEntries)
	}
	for entry := range temporaryEntries {
		if !strings.HasPrefix(path.Base(entry), "temporary-") {
			t.Fatalf("temporary entry is reused or not random: %q", entry)
		}
	}
	if len(prepared.DurabilityReceipts) != len(request.Manifest.Members)+1 {
		t.Fatal("prepare omitted independently verified durability receipts")
	}
	for _, content := range store.state.Contents {
		info, err := store.root.Lstat(content.Entry)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o222 != 0 {
			t.Fatal("promoted physical generation is not an immutable regular file")
		}
		if writable, err := store.root.OpenFile(content.Entry, os.O_WRONLY, 0); err == nil {
			_ = writable.Close()
			t.Fatal("promoted physical generation remained writable")
		}
	}
	activatePreparedForTest(t, store, request, prepared)
	if _, err := store.VerifyCheckpoint(context.Background(), localVerifyRequestForTest(
		request, prepared, "materialize-immutable",
	)); err != nil {
		t.Fatalf("materialize verified Checkpoint: %v", err)
	}
	for _, materialization := range store.state.Materializations {
		for _, directory := range []string{
			materialization.Entry, materialization.PayloadEntry, materialization.PayloadEntry + "/state",
		} {
			info, err := store.root.Lstat(directory)
			if err != nil || !info.IsDir() || info.Mode().Perm()&0o222 != 0 {
				t.Fatalf("promoted materialization directory %q is writable", path.Base(directory))
			}
		}
		injected, err := store.root.OpenFile(
			materialization.Entry+"/unexpected-member", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600,
		)
		if err == nil {
			_ = injected.Close()
			t.Fatal("promoted materialization accepted a new member")
		}
	}
}

func TestLocalFilesystemPhysicallyReservesBytesAndInodesBeforeWrite(t *testing.T) {
	store, _ := newLocalStoreForTest(t)
	request := localPrepareRequestForTest("prepare-physical-reservation", false)
	observed := false
	store.fault = func(event LocalFilesystemFaultEvent) error {
		if observed || event.Point != LocalFaultBeforeWrite || event.OperationID != request.Operation.ID {
			return nil
		}
		for _, residue := range store.state.Residues {
			if residue.OperationID != request.Operation.ID || residue.PendingContent == nil {
				continue
			}
			info, err := store.root.Lstat(residue.TemporaryEntry)
			identity, identityOK := localIdentityFromFileInfo(info)
			if err != nil || !info.Mode().IsRegular() || uint64(info.Size()) != residue.PendingContent.Size ||
				!identityOK || identity != residue.PhysicalIdentity {
				return errors.New("actual temporary generation was not physically reserved")
			}
			if residue.PendingContent.Size != 0 {
				stat, ok := info.Sys().(*syscall.Stat_t)
				if !ok || stat.Blocks == 0 {
					return errors.New("actual temporary generation had no reserved blocks")
				}
			}
			observed = true
			return nil
		}
		return errors.New("write had no exact temporary generation reservation")
	}

	if _, err := store.PrepareCheckpoint(context.Background(), request); err != nil {
		t.Fatalf("prepare with physical reservation: %v", err)
	}
	if !observed {
		t.Fatal("write began without an observable physical bytes/inodes reservation")
	}
	for _, residue := range store.state.Residues {
		if residue.OperationID == request.Operation.ID && residue.PendingContent == nil &&
			residue.PendingMaterialization == nil {
			t.Fatal("successful prepare retained a separate, non-consumable capacity reservation")
		}
	}
}

func TestLocalFilesystemContentAdmissionIncludesTemporaryMetadataPeak(t *testing.T) {
	maximum := ^uint64(0)
	for _, test := range []struct {
		name     string
		capacity LocalFilesystemCapacity
	}{
		{
			name: "temporary state and metadata bytes",
			capacity: LocalFilesystemCapacity{
				AvailableBytes: 4 << 10, AvailableInodes: maximum,
			},
		},
		{
			name: "temporary state inode",
			capacity: LocalFilesystemCapacity{
				AvailableBytes: maximum, AvailableInodes: 2,
			},
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			store, _ := newLocalStoreForTest(t)
			store.capacity = func() (LocalFilesystemCapacity, error) {
				return test.capacity, nil
			}
			writeStarted := false
			store.fault = func(event LocalFilesystemFaultEvent) error {
				if event.Point == LocalFaultBeforeWrite {
					writeStarted = true
				}
				return nil
			}

			prepared, err := store.PrepareCheckpoint(
				context.Background(), localPrepareRequestForTest("prepare-content-peak", false),
			)
			assertLocalLifecycleError(t, err, ErrorResourceExhausted)
			if prepared.ManifestReference.ID != "" || len(prepared.ContentReferences) != 0 || writeStarted {
				t.Fatal("content admission began a write or exposed prepared authority without temporary peak capacity")
			}
		})
	}
}

func TestLocalFilesystemMaterializationIsImmutableAtAtomicPublication(t *testing.T) {
	store, _ := newLocalStoreForTest(t)
	prepare := localPrepareRequestForTest("prepare-immutable-publication", false)
	prepared, err := store.PrepareCheckpoint(context.Background(), prepare)
	if err != nil {
		t.Fatalf("prepare Checkpoint: %v", err)
	}
	activatePreparedForTest(t, store, prepare, prepared)
	observed := false
	var publicationErr error
	var points []LocalFilesystemFaultPoint
	store.fault = func(event LocalFilesystemFaultEvent) error {
		if event.OperationID == "materialize-immutable-publication" {
			points = append(points, event.Point)
		}
		if observed || event.Point != LocalFaultBeforePromotion ||
			event.OperationID != "materialize-immutable-publication" {
			return nil
		}
		for _, residue := range store.state.Residues {
			if residue.OperationID != event.OperationID || residue.PendingMaterialization == nil {
				continue
			}
			if _, published := store.state.Materializations[event.OperationID]; published {
				publicationErr = errors.New("materialization authority was published before the atomic state promotion")
				return publicationErr
			}
			wrapper, err := store.root.Lstat(residue.Entry)
			if err != nil || !wrapper.IsDir() || wrapper.Mode().Perm()&0o222 != 0 {
				publicationErr = err
				if err == nil {
					publicationErr = fmt.Errorf("wrapper mode %v", wrapper.Mode())
				}
				return errors.New("materialization was writable at the atomic publication boundary")
			}
			observed = true
			return nil
		}
		publicationErr = errors.New("materialization publication residue was not recorded")
		return publicationErr
	}
	if _, err := store.VerifyCheckpoint(context.Background(), localVerifyRequestForTest(
		prepare, prepared, "materialize-immutable-publication",
	)); err != nil {
		t.Fatalf("materialize immutable generation: %v (publication verification: %v, points: %v)",
			err, publicationErr, points)
	}
	if !observed {
		t.Fatal("materialization did not cross the atomic publication boundary")
	}
}

func TestLocalFilesystemMaterializationConsumesItsPhysicalReservation(t *testing.T) {
	store, _ := newLocalStoreForTest(t)
	prepare := localPrepareRequestForTest("prepare-materialization-reservation", false)
	prepared, err := store.PrepareCheckpoint(context.Background(), prepare)
	if err != nil {
		t.Fatalf("prepare Checkpoint: %v", err)
	}
	activatePreparedForTest(t, store, prepare, prepared)
	observed := false
	store.fault = func(event LocalFilesystemFaultEvent) error {
		if observed || event.Point != LocalFaultBeforePromotion ||
			event.OperationID != "materialize-physical-reservation" {
			return nil
		}
		for _, residue := range store.state.Residues {
			if residue.OperationID != event.OperationID || residue.PendingMaterialization == nil {
				continue
			}
			capacity, err := store.root.Lstat(
				residue.Entry + "/" + localMaterializationCapacity,
			)
			if err != nil || !capacity.Mode().IsRegular() ||
				uint64(capacity.Size()) != residue.Capacity.Bytes.Value {
				return errors.New("materialization did not carry its exact byte reservation")
			}
			if stat, ok := capacity.Sys().(*syscall.Stat_t); residue.Capacity.Bytes.Value != 0 &&
				(!ok || stat.Blocks == 0) {
				return errors.New("materialization capacity file had no allocated blocks")
			}
			observed = true
			return nil
		}
		return errors.New("materialization had no exact reservation residue")
	}
	if _, err := store.VerifyCheckpoint(context.Background(), localVerifyRequestForTest(
		prepare, prepared, "materialize-physical-reservation",
	)); err != nil {
		t.Fatalf("materialize from exact physical reservation: %v", err)
	}
	if !observed {
		t.Fatal("materialization promotion did not consume the reserved generation")
	}
}

func TestLocalFilesystemReservationExhaustionLeavesNoCommittedOrPartialState(t *testing.T) {
	store, _ := newLocalStoreForTest(t)
	request := localPrepareRequestForTest("prepare-reservation-exhaustion", false)
	writeStarted := false
	faulted := false
	store.fault = func(event LocalFilesystemFaultEvent) error {
		if event.Point == LocalFaultBeforeWrite && event.OperationID == request.Operation.ID {
			writeStarted = true
		}
		if !faulted && event.Point == LocalFaultBeforeCapacityReserve &&
			event.OperationID == request.Operation.ID {
			faulted = true
			return syscall.ENOSPC
		}
		return nil
	}

	prepared, err := store.PrepareCheckpoint(context.Background(), request)
	assertLocalLifecycleError(t, err, ErrorResourceExhausted)
	if prepared.ManifestReference.ID != "" || len(prepared.ContentReferences) != 0 ||
		!faulted || writeStarted || len(store.state.Contents) != 0 || len(store.state.References) != 0 {
		t.Fatal("reservation exhaustion exposed prepared or committed state")
	}
	if len(store.state.Residues) != 0 || len(store.state.CleanupResources) != 0 {
		t.Fatal("clean reservation exhaustion retained misleading partial state")
	}
}

func TestLocalFilesystemFailsClosedOnRacedFileReplacementAndUnsafeFileType(t *testing.T) {
	for _, test := range []struct {
		name    string
		replace func(*localFilesystemStore, localResidueRecord) error
	}{
		{
			name: "symbolic link containment escape",
			replace: func(store *localFilesystemStore, residue localResidueRecord) error {
				return store.root.Symlink("../../outside-authority", residue.Entry)
			},
		},
		{
			name: "raced same-byte regular file replacement",
			replace: func(store *localFilesystemStore, residue localResidueRecord) error {
				if err := store.root.Remove(residue.Entry); err != nil {
					return err
				}
				return store.root.WriteFile(residue.Entry, []byte("task-owned-state-one"), 0o400)
			},
		},
		{
			name: "unsafe fifo replacement",
			replace: func(store *localFilesystemStore, residue localResidueRecord) error {
				if err := store.root.Remove(residue.Entry); err != nil {
					return err
				}
				return unix.Mkfifo(store.root.Name()+"/"+residue.Entry, 0o400)
			},
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			store, _ := newLocalStoreForTest(t)
			request := localPrepareRequestForTest("prepare-race", false)
			replaced := false
			store.fault = func(event LocalFilesystemFaultEvent) error {
				if replaced || event.OperationID != request.Operation.ID {
					return nil
				}
				wanted := LocalFaultBeforePromotion
				if test.name != "symbolic link containment escape" {
					wanted = LocalFaultAfterPromotion
				}
				if event.Point != wanted {
					return nil
				}
				for _, residue := range store.state.Residues {
					if residue.OperationID == request.Operation.ID && residue.PendingContent != nil &&
						string(residue.PendingContent.ID) == event.SubjectID {
						replaced = true
						return test.replace(store, residue)
					}
				}
				return nil
			}
			prepared, err := store.PrepareCheckpoint(context.Background(), request)
			assertLocalLifecycleError(t, err, ErrorIntegrityFailure)
			if !replaced || prepared.ManifestReference.ID != "" || len(prepared.ContentReferences) != 0 {
				t.Fatal("raced replacement became verified Checkpoint content")
			}
		})
	}
}

func TestLocalFilesystemFailsClosedOnRacedMaterializationDirectoryEscape(t *testing.T) {
	store, rootName := newLocalStoreForTest(t)
	prepare := localPrepareRequestForTest("prepare-materialization-race", false)
	prepared, err := store.PrepareCheckpoint(context.Background(), prepare)
	if err != nil {
		t.Fatalf("prepare Checkpoint: %v", err)
	}
	activatePreparedForTest(t, store, prepare, prepared)
	outside := t.TempDir()
	outsideSentinel := outside + "/sentinel"
	if err := os.WriteFile(outsideSentinel, []byte("outside"), 0o600); err != nil {
		t.Fatalf("write outside sentinel: %v", err)
	}
	replaced := false
	store.fault = func(event LocalFilesystemFaultEvent) error {
		if replaced || event.Point != LocalFaultAfterPromotion || event.OperationID != "materialize-race" {
			return nil
		}
		for _, materialization := range store.state.Materializations {
			if materialization.OperationID == event.OperationID {
				replaced = true
				if err := store.makeMaterializationRemovable(materialization); err != nil {
					return err
				}
				if err := store.root.RemoveAll(materialization.Entry); err != nil {
					return err
				}
				return store.root.Symlink(outside, materialization.Entry)
			}
		}
		return nil
	}
	verify := localVerifyRequestForTest(prepare, prepared, "materialize-race")
	_, err = store.VerifyCheckpoint(context.Background(), verify)
	assertLocalLifecycleError(t, err, ErrorIntegrityFailure)
	if !replaced {
		t.Fatal("materialization directory replacement hook was not reached")
	}
	contents, err := os.ReadFile(outsideSentinel)
	if err != nil || string(contents) != "outside" {
		t.Fatal("containment escape modified the outside sentinel")
	}
	if strings.Contains(errString(err), rootName) {
		t.Fatal("adapter error disclosed its local root")
	}
}

func TestLocalFilesystemFailsClosedOnRacedUndeclaredMaterializationMember(t *testing.T) {
	store, _ := newLocalStoreForTest(t)
	prepare := localPrepareRequestForTest("prepare-materialization-extra", false)
	prepared, err := store.PrepareCheckpoint(context.Background(), prepare)
	if err != nil {
		t.Fatalf("prepare Checkpoint: %v", err)
	}
	activatePreparedForTest(t, store, prepare, prepared)
	injected := false
	store.fault = func(event LocalFilesystemFaultEvent) error {
		if injected || event.Point != LocalFaultBeforePromotion || event.OperationID != "materialize-extra" {
			return nil
		}
		for _, residue := range store.state.Residues {
			if residue.OperationID == event.OperationID && residue.PendingMaterialization != nil {
				injected = true
				if err := store.makeMaterializationRemovable(*residue.PendingMaterialization); err != nil {
					return err
				}
				return store.root.WriteFile(residue.Entry+"/undeclared", []byte("unsafe"), 0o400)
			}
		}
		return nil
	}
	verified, err := store.VerifyCheckpoint(context.Background(), localVerifyRequestForTest(
		prepare, prepared, "materialize-extra",
	))
	assertLocalLifecycleError(t, err, ErrorIntegrityFailure)
	if !injected || len(verified.ContentReferences) != 0 || len(store.state.Materializations) != 0 {
		t.Fatal("undeclared raced member became part of a verified materialization")
	}
}

func TestLocalFilesystemMaterializationPromotionDoesNotReplaceRacedTarget(t *testing.T) {
	store, _ := newLocalStoreForTest(t)
	prepare := localPrepareRequestForTest("prepare-materialization-no-replace", false)
	prepared, err := store.PrepareCheckpoint(context.Background(), prepare)
	if err != nil {
		t.Fatalf("prepare Checkpoint: %v", err)
	}
	activatePreparedForTest(t, store, prepare, prepared)
	var racedTarget string
	store.fault = func(event LocalFilesystemFaultEvent) error {
		if racedTarget != "" || event.Point != LocalFaultBeforePrivatePlacement ||
			event.OperationID != "materialize-no-replace" {
			return nil
		}
		for _, residue := range store.state.Residues {
			if residue.OperationID == event.OperationID && residue.PendingMaterialization != nil {
				racedTarget = residue.Entry
				return store.root.Mkdir(racedTarget, 0o700)
			}
		}
		return nil
	}
	verified, err := store.VerifyCheckpoint(context.Background(), localVerifyRequestForTest(
		prepare, prepared, "materialize-no-replace",
	))
	assertLocalLifecycleError(t, err, ErrorIntegrityFailure)
	if racedTarget == "" || len(verified.ContentReferences) != 0 || len(store.state.Materializations) != 0 {
		t.Fatal("raced materialization target became a verified physical generation")
	}
	directory, err := store.root.Open(racedTarget)
	if err != nil {
		t.Fatalf("open raced target: %v", err)
	}
	entries, readErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	if readErr != nil || closeErr != nil || len(entries) != 0 {
		t.Fatal("atomic promotion replaced the raced target directory")
	}
}

func TestLocalFilesystemRevalidatesReferenceAndLeaseImmediatelyBeforeExactGenerationCleanup(t *testing.T) {
	t.Run("re-reference race", func(t *testing.T) {
		store, _ := newLocalStoreForTest(t)
		prepare := localPrepareRequestForTest("prepare-reclaim-race", false)
		prepared, err := store.PrepareCheckpoint(context.Background(), prepare)
		if err != nil {
			t.Fatalf("prepare Checkpoint: %v", err)
		}
		resources, exactRoot := localResourcesForTest(prepared)
		release := localReferenceTransitionForTest(prepare, resources, exactRoot, "release-race")
		if _, err := store.ReleaseCheckpointReferences(context.Background(), release); err != nil {
			t.Fatalf("release references: %v", err)
		}
		reReferenced := false
		store.fault = func(event LocalFilesystemFaultEvent) error {
			if !reReferenced && event.Point == LocalFaultBeforeCleanupEntryRemove && event.OperationID == "reclaim-race" {
				reference := store.state.References[resources[0].ReferenceID]
				reference.Attached = true
				store.state.References[resources[0].ReferenceID] = reference
				reReferenced = true
			}
			return nil
		}
		reclaim := localReclaimRequestForTest(prepare, resources, exactRoot, "reclaim-race")
		evidence, err := store.ReclaimCheckpointContent(context.Background(), reclaim)
		if err != nil || evidence.Outcome != CheckpointRetainedByAuthority ||
			evidence.ReferenceState != CheckpointMechanicsBlocked {
			t.Fatalf("re-reference cleanup evidence = %#v, err = %v", evidence, err)
		}
		if !reReferenced {
			t.Fatal("cleanup did not reach the deterministic race boundary")
		}
		if _, err := store.root.Lstat(store.state.Contents[resources[0].ContentID].Entry); err != nil {
			t.Fatal("cleanup deleted content that regained reference authority")
		}
	})

	t.Run("active materialization lease", func(t *testing.T) {
		store, _ := newLocalStoreForTest(t)
		prepare := localPrepareRequestForTest("prepare-reclaim-lease", false)
		prepared, err := store.PrepareCheckpoint(context.Background(), prepare)
		if err != nil {
			t.Fatalf("prepare Checkpoint: %v", err)
		}
		activatePreparedForTest(t, store, prepare, prepared)
		if _, err := store.VerifyCheckpoint(context.Background(), localVerifyRequestForTest(
			prepare, prepared, "materialize-lease",
		)); err != nil {
			t.Fatalf("materialize Checkpoint: %v", err)
		}
		resources, exactRoot := localResourcesForTest(prepared)
		if _, err := store.ReleaseCheckpointReferences(context.Background(), localReferenceTransitionForTest(
			prepare, resources, exactRoot, "release-lease",
		)); err != nil {
			t.Fatalf("release references: %v", err)
		}
		evidence, err := store.ReclaimCheckpointContent(context.Background(), localReclaimRequestForTest(
			prepare, resources, exactRoot, "reclaim-lease",
		))
		if err != nil || evidence.Outcome != CheckpointRetainedByAuthority ||
			evidence.LeaseState != CheckpointMechanicsBlocked {
			t.Fatalf("active materialization cleanup evidence = %#v, err = %v", evidence, err)
		}
	})
}

func TestLocalFilesystemCleanupAmbiguityReconcilesToAlreadyAbsentAndReplays(t *testing.T) {
	store, _ := newLocalStoreForTest(t)
	prepare := localPrepareRequestForTest("prepare-cleanup-ambiguity", false)
	prepared, err := store.PrepareCheckpoint(context.Background(), prepare)
	if err != nil {
		t.Fatalf("prepare Checkpoint: %v", err)
	}
	resources, exactRoot := localResourcesForTest(prepared)
	if _, err := store.ReleaseCheckpointReferences(context.Background(), localReferenceTransitionForTest(
		prepare, resources, exactRoot, "release-ambiguity",
	)); err != nil {
		t.Fatalf("release references: %v", err)
	}
	faulted := false
	store.fault = func(event LocalFilesystemFaultEvent) error {
		if !faulted && event.Point == LocalFaultAfterCleanup && event.OperationID == "reclaim-ambiguity" {
			faulted = true
			return errors.New("cleanup acknowledgement lost")
		}
		return nil
	}
	reclaim := localReclaimRequestForTest(prepare, resources, exactRoot, "reclaim-ambiguity")
	if _, err := store.ReclaimCheckpointContent(context.Background(), reclaim); !errors.Is(err, ErrDurableObjectResultAmbiguous) {
		t.Fatalf("ambiguous cleanup error = %v", err)
	}
	second, err := store.ReclaimCheckpointContent(context.Background(), reclaim)
	if err != nil || second.Outcome != CheckpointAlreadyAbsent {
		t.Fatalf("cleanup reconciliation = %#v, err = %v", second, err)
	}
	third, err := store.ReclaimCheckpointContent(context.Background(), reclaim)
	if err != nil || third != second {
		t.Fatal("repeated exact-generation cleanup did not replay the same evidence")
	}
}

func TestLocalFilesystemCheckpointCleanupReconcilesPrivateClaimAfterRestart(t *testing.T) {
	for _, test := range []localCheckpointCleanupRestartScenario{
		{
			name: "reclaims exact claim", id: "reclaim",
			faultPoint: LocalFaultBeforeCleanupEntryRemove, wantOutcome: CheckpointReclaimed,
		},
		{
			name: "restores exact claim when reference returns", id: "reference",
			faultPoint: LocalFaultBeforeCleanupEntryRemove, reference: true,
			wantOutcome: CheckpointRetainedByAuthority,
		},
		{
			name: "rejects reference after deletion wins", id: "deleted-reference",
			faultPoint: LocalFaultAfterCleanup, reference: true, attachFails: true,
			wantOutcome: CheckpointAlreadyAbsent,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			testLocalFilesystemCheckpointCleanupRestart(t, test)
		})
	}
}

func TestLocalFilesystemAttachCheckpointReferencesIsAtomicAcrossResources(t *testing.T) {
	store, _ := newLocalStoreForTest(t)
	prepare := localPrepareRequestForTest("prepare-atomic-reference-attach", true)
	prepared, err := store.PrepareCheckpoint(context.Background(), prepare)
	if err != nil {
		t.Fatalf("prepare Checkpoint: %v", err)
	}
	resources, exactRoot := localResourcesForTest(prepared)
	if len(resources) < 2 {
		t.Fatal("atomic reference test requires multiple content generations")
	}
	if _, err := store.ReleaseCheckpointReferences(context.Background(), localReferenceTransitionForTest(
		prepare, resources, exactRoot, "release-atomic-reference-attach",
	)); err != nil {
		t.Fatalf("release references: %v", err)
	}
	later := store.state.Contents[resources[len(resources)-1].ContentID]
	if err := store.root.Remove(later.Entry); err != nil {
		t.Fatalf("remove later exact generation: %v", err)
	}
	attach := localReferenceTransitionForTest(
		prepare, resources, exactRoot, "attach-atomic-reference-attach",
	)
	if _, err := store.AttachCheckpointReferences(context.Background(), attach); err == nil {
		t.Fatal("multi-resource attach accepted a missing later generation")
	}
	for _, resource := range resources {
		if store.state.References[resource.ReferenceID].Attached {
			t.Fatal("failed multi-resource attach partially changed reference authority")
		}
	}
	if _, ok := store.state.ReferenceTransitions[attach.Operation.ID]; ok {
		t.Fatal("failed multi-resource attach persisted transition evidence")
	}
}

type localCheckpointCleanupRestartScenario struct {
	name        string
	id          string
	faultPoint  LocalFilesystemFaultPoint
	reference   bool
	attachFails bool
	wantOutcome CheckpointReclamationOutcome
}

func testLocalFilesystemCheckpointCleanupRestart(
	t *testing.T,
	test localCheckpointCleanupRestartScenario,
) {
	t.Helper()
	store, rootName := newLocalStoreForTest(t)
	prepare := localPrepareRequestForTest(OperationID("prepare-checkpoint-cleanup-restart-"+test.id), false)
	prepared, err := store.PrepareCheckpoint(context.Background(), prepare)
	if err != nil {
		t.Fatalf("prepare Checkpoint: %v", err)
	}
	resources, exactRoot := localResourcesForTest(prepared)
	if _, err := store.ReleaseCheckpointReferences(context.Background(), localReferenceTransitionForTest(
		prepare, resources, exactRoot, OperationID("release-checkpoint-cleanup-restart-"+test.id),
	)); err != nil {
		t.Fatalf("release references: %v", err)
	}
	reclaim := localReclaimRequestForTest(
		prepare, resources, exactRoot, OperationID("reclaim-checkpoint-cleanup-restart-"+test.id),
	)
	faulted := false
	store.fault = func(event LocalFilesystemFaultEvent) error {
		if !faulted && event.Point == test.faultPoint &&
			event.OperationID == reclaim.Operation.ID {
			faulted = true
			return errors.New("simulated restart at checkpoint cleanup boundary")
		}
		return nil
	}
	wantInterruption := ErrCleanupResultAmbiguous
	if test.faultPoint == LocalFaultAfterCleanup {
		wantInterruption = ErrDurableObjectResultAmbiguous
	}
	if _, err := store.ReclaimCheckpointContent(context.Background(), reclaim); !errors.Is(err, wantInterruption) {
		t.Fatalf("interrupted checkpoint cleanup error = %v", err)
	}
	if !faulted {
		t.Fatal("checkpoint cleanup did not reach the private claim boundary")
	}
	var interruptedTreeClaim localCleanupTreeClaim
	for _, claim := range store.state.CleanupTreeClaims {
		if claim.ResourceID == string(resources[0].ContentID) {
			interruptedTreeClaim = claim
			break
		}
	}
	if test.faultPoint == LocalFaultBeforeCleanupEntryRemove {
		if interruptedTreeClaim.ClaimEntry == "" {
			t.Fatal("checkpoint cleanup interruption did not persist its exact private claim")
		}
		if info, err := store.root.Lstat(interruptedTreeClaim.ClaimEntry); err != nil || !info.Mode().IsRegular() {
			t.Fatal("checkpoint cleanup interruption lost the exact claimed generation")
		}
	} else if len(store.state.CleanupTreeClaims) != 0 {
		t.Fatal("post-delete interruption retained a completed private claim")
	}

	restarted := restartLocalStoreForTest(t, rootName)
	if test.reference {
		_, attachErr := restarted.AttachCheckpointReferences(context.Background(), localReferenceTransitionForTest(
			prepare, resources, exactRoot, OperationID("reattach-checkpoint-cleanup-restart-"+test.id),
		))
		if test.attachFails {
			assertLocalLifecycleError(t, attachErr, ErrorIntegrityFailure)
		} else if attachErr != nil {
			t.Fatalf("reattach reference after restart: %v", attachErr)
		}
	}
	evidence, err := restarted.ReclaimCheckpointContent(context.Background(), reclaim)
	if err != nil || evidence.Outcome != test.wantOutcome {
		t.Fatalf("restart checkpoint cleanup = %#v, err = %v", evidence, err)
	}
	if len(restarted.state.CleanupClaims) != 0 || len(restarted.state.CleanupTreeClaims) != 0 {
		t.Fatal("restart checkpoint cleanup retained completed claim authority")
	}
	if interruptedTreeClaim.ClaimEntry != "" {
		if _, err := restarted.root.Lstat(interruptedTreeClaim.ClaimEntry); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("restart checkpoint cleanup left the private physical claim")
		}
	}
	if test.reference && !test.attachFails {
		content, ok := restarted.state.Contents[resources[0].ContentID]
		if !ok {
			t.Fatal("retained cleanup lost content authority")
		}
		payload, err := restarted.root.ReadFile(content.Entry)
		if err != nil || string(payload) != "task-owned-state-one" {
			t.Fatal("retained cleanup did not restore readable exact content")
		}
	} else if _, ok := restarted.state.Contents[resources[0].ContentID]; ok {
		t.Fatal("restart checkpoint cleanup retained reclaimed content authority")
	}
	replayed, err := restarted.ReclaimCheckpointContent(context.Background(), reclaim)
	if err != nil || replayed != evidence {
		t.Fatal("restart checkpoint cleanup did not replay exact reclamation evidence")
	}
}

func TestLocalFilesystemCleanupFailsClosedOnRacedGenerationReplacement(t *testing.T) {
	store, _ := newLocalStoreForTest(t)
	prepare := localPrepareRequestForTest("prepare-cleanup-replacement", false)
	faulted := false
	store.fault = func(event LocalFilesystemFaultEvent) error {
		if !faulted && event.Point == LocalFaultBeforeReadback && event.OperationID == prepare.Operation.ID {
			faulted = true
			return errors.New("leave promoted failure residue")
		}
		return nil
	}
	if _, err := store.PrepareCheckpoint(context.Background(), prepare); err == nil || !faulted {
		t.Fatalf("prepare failure residue = %v", err)
	}
	var residue localResidueRecord
	for _, candidate := range store.state.Residues {
		if _, err := store.root.Lstat(candidate.Entry); err == nil {
			residue = candidate
			break
		}
	}
	if residue.ID == "" {
		t.Fatal("readback failure did not leave an exact promoted residue")
	}
	inspectionRequest := CleanupInspectionRequest{
		PolicyDomainID: prepare.PolicyDomainID, TaskID: prepare.TaskID,
		TaskWorkspaceID: prepare.TaskWorkspaceID, DebtID: "cleanup-replacement-debt",
		Owner: CleanupOwnerC04, ResourceClass: CleanupWorkspaceResidue,
		ResourceID: CleanupResourceID(residue.ID), ResourceGeneration: CleanupResourceGeneration(residue.Generation),
		RetryGeneration: 1, Generation: prepare.Generation, Fence: prepare.Fence,
		Operation: Operation{ID: "inspect-cleanup-replacement"},
	}
	inspection, err := store.InspectCleanup(context.Background(), inspectionRequest)
	if err != nil || inspection.Disposition != CleanupInspectionEligible {
		t.Fatalf("inspect exact failure residue = %#v, err = %v", inspection, err)
	}
	replaced := false
	store.fault = func(event LocalFilesystemFaultEvent) error {
		if !replaced && event.Point == LocalFaultAfterCleanupVerify && event.OperationID == "reclaim-cleanup-replacement" {
			replaced = true
			if err := store.root.Remove(residue.Entry); err != nil {
				return err
			}
			return store.root.WriteFile(residue.Entry, []byte("unrelated replacement"), 0o400)
		}
		return nil
	}
	attempt := CleanupAttemptRequest{
		PolicyDomainID: prepare.PolicyDomainID, TaskID: prepare.TaskID,
		TaskWorkspaceID: prepare.TaskWorkspaceID, DebtID: inspection.DebtID,
		Owner: inspection.Owner, ResourceClass: inspection.ResourceClass,
		ResourceID: inspection.ResourceID, ResourceGeneration: inspection.ResourceGeneration,
		RetryGeneration: inspection.RetryGeneration, Generation: inspection.Generation, Fence: inspection.Fence,
		InspectionEvidenceDigest: inspection.Digest, Operation: Operation{ID: "reclaim-cleanup-replacement"},
	}
	_, err = store.ReclaimCleanup(context.Background(), attempt)
	assertLocalLifecycleError(t, err, ErrorIntegrityFailure)
	if !replaced {
		t.Fatal("cleanup replacement race hook was not reached")
	}
	if contents, err := store.root.ReadFile(residue.Entry); err != nil || string(contents) != "unrelated replacement" {
		t.Fatal("cleanup deleted a raced replacement instead of failing closed")
	}
}

func TestLocalFilesystemCheckpointCleanupClaimsExactGenerationBeforeDelete(t *testing.T) {
	store, _ := newLocalStoreForTest(t)
	prepare := localPrepareRequestForTest("prepare-cleanup-claim-race", false)
	prepared, err := store.PrepareCheckpoint(context.Background(), prepare)
	if err != nil {
		t.Fatalf("prepare Checkpoint: %v", err)
	}
	resources, exactRoot := localResourcesForTest(prepared)
	if _, err := store.ReleaseCheckpointReferences(context.Background(), localReferenceTransitionForTest(
		prepare, resources, exactRoot, "release-cleanup-claim-race",
	)); err != nil {
		t.Fatalf("release references: %v", err)
	}
	content := store.state.Contents[resources[0].ContentID]
	replaced := false
	store.fault = func(event LocalFilesystemFaultEvent) error {
		if !replaced && event.Point == LocalFilesystemFaultPoint("after_cleanup_verification") &&
			event.OperationID == "reclaim-cleanup-claim-race" {
			replaced = true
			if err := store.root.Remove(content.Entry); err != nil {
				return err
			}
			return store.root.WriteFile(content.Entry, []byte("unrelated raced generation"), 0o400)
		}
		return nil
	}

	_, err = store.ReclaimCheckpointContent(context.Background(), localReclaimRequestForTest(
		prepare, resources, exactRoot, "reclaim-cleanup-claim-race",
	))
	assertLocalLifecycleError(t, err, ErrorIntegrityFailure)
	if !replaced {
		t.Fatal("cleanup did not reach the post-verification race boundary")
	}
	contents, err := store.root.ReadFile(content.Entry)
	if err != nil || string(contents) != "unrelated raced generation" {
		t.Fatal("cleanup deleted the raced pathname replacement")
	}
}

func TestLocalFilesystemCleanupRevalidatesClaimAtDeletion(t *testing.T) {
	store, _ := newLocalStoreForTest(t)
	prepare := localPrepareRequestForTest("prepare-cleanup-delete-race", false)
	prepared, err := store.PrepareCheckpoint(context.Background(), prepare)
	if err != nil {
		t.Fatalf("prepare Checkpoint: %v", err)
	}
	activatePreparedForTest(t, store, prepare, prepared)
	resources, exactRoot := localResourcesForTest(prepared)
	if _, err := store.ReleaseCheckpointReferences(context.Background(), localReferenceTransitionForTest(
		prepare, resources, exactRoot, "release-cleanup-delete-race",
	)); err != nil {
		t.Fatalf("release references: %v", err)
	}
	replaced := false
	var replacementEntry string
	store.fault = func(event LocalFilesystemFaultEvent) error {
		if replaced || event.Point != LocalFaultBeforeCleanupEntryRemove ||
			event.OperationID != "reclaim-cleanup-delete-race" {
			return nil
		}
		for _, claim := range store.state.CleanupTreeClaims {
			if claim.ResourceID != string(resources[0].ContentID) {
				continue
			}
			replaced = true
			replacementEntry = claim.ClaimEntry
			if err := store.root.Remove(claim.ClaimEntry); err != nil {
				return err
			}
			return store.root.WriteFile(claim.ClaimEntry, []byte("replacement generation"), 0o400)
		}
		return errors.New("cleanup delete hook observed no persisted exact claim")
	}

	_, err = store.ReclaimCheckpointContent(context.Background(), localReclaimRequestForTest(
		prepare, resources, exactRoot, "reclaim-cleanup-delete-race",
	))
	assertLocalLifecycleError(t, err, ErrorIntegrityFailure)
	if !replaced || replacementEntry == "" {
		t.Fatal("cleanup did not reach the exact claim deletion boundary")
	}
	contents, err := store.root.ReadFile(replacementEntry)
	if err != nil || string(contents) != "replacement generation" {
		t.Fatal("cleanup deleted a replacement installed after claim verification")
	}
}

func TestLocalFilesystemMaterializationCleanupClaimsExactGenerationBeforeDelete(t *testing.T) {
	store, _ := newLocalStoreForTest(t)
	prepare := localPrepareRequestForTest("prepare-materialization-cleanup-claim", false)
	prepared, err := store.PrepareCheckpoint(context.Background(), prepare)
	if err != nil {
		t.Fatalf("prepare Checkpoint: %v", err)
	}
	activatePreparedForTest(t, store, prepare, prepared)
	verify := localVerifyRequestForTest(prepare, prepared, "materialize-cleanup-claim")
	if _, err := store.VerifyCheckpoint(context.Background(), verify); err != nil {
		t.Fatalf("materialize Checkpoint: %v", err)
	}
	record := store.state.Materializations[verify.Operation.ID]
	materializationID := MaterializationID("materialization-cleanup-claim")
	if err := store.bindMaterialization(localMaterializationAuthority{
		OperationID: verify.Operation.ID, MaterializationID: materializationID,
		PolicyDomainID: prepare.PolicyDomainID, TaskID: prepare.TaskID,
		TaskWorkspaceID: prepare.TaskWorkspaceID, Generation: prepare.Generation,
		Fence: prepare.Fence, PhysicalRequired: true,
	}); err != nil {
		t.Fatalf("bind materialization: %v", err)
	}
	replaced := false
	store.fault = func(event LocalFilesystemFaultEvent) error {
		if !replaced && event.Point == LocalFaultAfterCleanupVerify &&
			event.OperationID == "expire-materialization-cleanup-claim" {
			replaced = true
			if err := store.makeMaterializationRemovable(record); err != nil {
				return err
			}
			if err := store.root.RemoveAll(record.Entry); err != nil {
				return err
			}
			if err := store.root.Mkdir(record.Entry, 0o700); err != nil {
				return err
			}
			return store.root.WriteFile(record.Entry+"/unrelated", []byte("raced directory"), 0o600)
		}
		return nil
	}
	expire := ExpireMaterializationRequest{
		PolicyDomainID: prepare.PolicyDomainID, TaskID: prepare.TaskID,
		TaskWorkspaceID: prepare.TaskWorkspaceID, MaterializationID: materializationID,
		RevisionID: prepare.RevisionID, CheckpointID: prepare.CheckpointID,
		Generation: prepare.Generation, Fence: prepare.Fence,
		ExpiryPolicyID: "test-expiry-policy", Operation: Operation{ID: "expire-materialization-cleanup-claim"},
	}

	residue, err := store.expireMaterialization(expire)
	assertLocalLifecycleError(t, err, ErrorIntegrityFailure)
	if residue == nil || !replaced {
		t.Fatal("materialization cleanup lost its debt resource or missed the race boundary")
	}
	contents, err := store.root.ReadFile(record.Entry + "/unrelated")
	if err != nil || string(contents) != "raced directory" {
		t.Fatal("materialization cleanup deleted the raced directory replacement")
	}
}

func TestLocalFilesystemCleanupClaimFailureKeepsPublishedMaterializationImmutableAndInaccessible(t *testing.T) {
	store, _ := newLocalStoreForTest(t)
	prepare := localPrepareRequestForTest("prepare-materialization-cleanup-fault", false)
	prepared, err := store.PrepareCheckpoint(context.Background(), prepare)
	if err != nil {
		t.Fatalf("prepare Checkpoint: %v", err)
	}
	activatePreparedForTest(t, store, prepare, prepared)
	verify := localVerifyRequestForTest(prepare, prepared, "materialize-cleanup-fault")
	if _, err := store.VerifyCheckpoint(context.Background(), verify); err != nil {
		t.Fatalf("materialize Checkpoint: %v", err)
	}
	record := store.state.Materializations[verify.Operation.ID]
	materializationID := MaterializationID("materialization-cleanup-fault")
	if err := store.bindMaterialization(localMaterializationAuthority{
		OperationID: verify.Operation.ID, MaterializationID: materializationID,
		PolicyDomainID: prepare.PolicyDomainID, TaskID: prepare.TaskID,
		TaskWorkspaceID: prepare.TaskWorkspaceID, Generation: prepare.Generation,
		Fence: prepare.Fence, PhysicalRequired: true,
	}); err != nil {
		t.Fatalf("bind materialization: %v", err)
	}
	faulted := false
	store.fault = func(event LocalFilesystemFaultEvent) error {
		if !faulted && event.Point == LocalFaultAfterCleanupVerify &&
			event.OperationID == "expire-materialization-cleanup-fault" {
			faulted = true
			return errors.New("cleanup claim interrupted")
		}
		return nil
	}
	expire := ExpireMaterializationRequest{
		PolicyDomainID: prepare.PolicyDomainID, TaskID: prepare.TaskID,
		TaskWorkspaceID: prepare.TaskWorkspaceID, MaterializationID: materializationID,
		RevisionID: prepare.RevisionID, CheckpointID: prepare.CheckpointID,
		Generation: prepare.Generation, Fence: prepare.Fence,
		ExpiryPolicyID: "test-expiry-policy", Operation: Operation{ID: "expire-materialization-cleanup-fault"},
	}
	residue, err := store.expireMaterialization(expire)
	if !errors.Is(err, ErrCleanupResultAmbiguous) || residue == nil || !faulted {
		t.Fatalf("interrupted cleanup claim = %#v, err = %v", residue, err)
	}
	wrapper, err := store.root.Lstat(record.Entry)
	if err != nil || !wrapper.IsDir() || wrapper.Mode().Perm()&0o222 != 0 {
		t.Fatal("interrupted cleanup claim left the published generation writable")
	}
	if _, err := store.VerifyCheckpoint(context.Background(), verify); err == nil {
		t.Fatal("a generation with a persisted cleanup claim remained materializable")
	}
}

func TestLocalFilesystemCleanupRevalidatesMaterializationMembersAfterMakingClaimRemovable(t *testing.T) {
	store, _ := newLocalStoreForTest(t)
	prepare := localPrepareRequestForTest("prepare-materialization-child-race", false)
	prepared, err := store.PrepareCheckpoint(context.Background(), prepare)
	if err != nil {
		t.Fatalf("prepare Checkpoint: %v", err)
	}
	activatePreparedForTest(t, store, prepare, prepared)
	verify := localVerifyRequestForTest(prepare, prepared, "materialize-child-race")
	if _, err := store.VerifyCheckpoint(context.Background(), verify); err != nil {
		t.Fatalf("materialize Checkpoint: %v", err)
	}
	materializationID := MaterializationID("materialization-child-race")
	if err := store.bindMaterialization(localMaterializationAuthority{
		OperationID: verify.Operation.ID, MaterializationID: materializationID,
		PolicyDomainID: prepare.PolicyDomainID, TaskID: prepare.TaskID,
		TaskWorkspaceID: prepare.TaskWorkspaceID, Generation: prepare.Generation,
		Fence: prepare.Fence, PhysicalRequired: true,
	}); err != nil {
		t.Fatalf("bind materialization: %v", err)
	}
	replaced := false
	var replacementEntry string
	store.fault = func(event LocalFilesystemFaultEvent) error {
		if replaced || event.Point != LocalFaultBeforeCleanupEntryRemove ||
			event.OperationID != "expire-materialization-child-race" {
			return nil
		}
		for _, claim := range store.state.CleanupClaims {
			if claim.OperationID != event.OperationID || claim.Kind != localCleanupMaterializationGeneration {
				continue
			}
			replaced = true
			replacementEntry = claim.ClaimEntry + "/payload/state/deck.json"
			if err := store.root.Remove(replacementEntry); err != nil {
				return err
			}
			return store.root.WriteFile(replacementEntry, []byte("unrelated child generation"), 0o400)
		}
		return errors.New("cleanup prepare hook observed no exact materialization claim")
	}
	expire := ExpireMaterializationRequest{
		PolicyDomainID: prepare.PolicyDomainID, TaskID: prepare.TaskID,
		TaskWorkspaceID: prepare.TaskWorkspaceID, MaterializationID: materializationID,
		RevisionID: prepare.RevisionID, CheckpointID: prepare.CheckpointID,
		Generation: prepare.Generation, Fence: prepare.Fence,
		ExpiryPolicyID: "test-expiry-policy", Operation: Operation{ID: "expire-materialization-child-race"},
	}
	residue, err := store.expireMaterialization(expire)
	assertLocalLifecycleError(t, err, ErrorIntegrityFailure)
	if residue == nil || !replaced || replacementEntry == "" {
		t.Fatal("cleanup did not preserve debt authority for the raced child generation")
	}
	contents, err := store.root.ReadFile(replacementEntry)
	if err != nil || string(contents) != "unrelated child generation" {
		t.Fatal("cleanup deleted a child replacement without fail-closed revalidation")
	}
}

func TestLocalFilesystemCleanupRetriesPartiallyDeletedClaimAfterRestart(t *testing.T) {
	store, rootName := newLocalStoreForTest(t)
	prepare := localPrepareRequestForTest("prepare-partial-cleanup-restart", false)
	prepared, err := store.PrepareCheckpoint(context.Background(), prepare)
	if err != nil {
		t.Fatalf("prepare Checkpoint: %v", err)
	}
	activatePreparedForTest(t, store, prepare, prepared)
	verify := localVerifyRequestForTest(prepare, prepared, "materialize-partial-cleanup-restart")
	if _, err := store.VerifyCheckpoint(context.Background(), verify); err != nil {
		t.Fatalf("materialize Checkpoint: %v", err)
	}
	materializationID := MaterializationID("materialization-partial-cleanup-restart")
	if err := store.bindMaterialization(localMaterializationAuthority{
		OperationID: verify.Operation.ID, MaterializationID: materializationID,
		PolicyDomainID: prepare.PolicyDomainID, TaskID: prepare.TaskID,
		TaskWorkspaceID: prepare.TaskWorkspaceID, Generation: prepare.Generation,
		Fence: prepare.Fence, PhysicalRequired: true,
	}); err != nil {
		t.Fatalf("bind materialization: %v", err)
	}
	store.fault = func(event LocalFilesystemFaultEvent) error {
		if event.Point != LocalFaultBeforeCleanupEntryRemove ||
			event.OperationID != "expire-partial-cleanup-restart" {
			return nil
		}
		for _, claim := range store.state.CleanupTreeClaims {
			if claim.ResourceID == string(materializationID) && claim.Directory &&
				path.Dir(claim.Entry) == localStagingDirectory {
				return errors.New("simulated process interruption after claiming the exact root")
			}
		}
		return nil
	}
	expire := ExpireMaterializationRequest{
		PolicyDomainID: prepare.PolicyDomainID, TaskID: prepare.TaskID,
		TaskWorkspaceID: prepare.TaskWorkspaceID, MaterializationID: materializationID,
		RevisionID: prepare.RevisionID, CheckpointID: prepare.CheckpointID,
		Generation: prepare.Generation, Fence: prepare.Fence,
		ExpiryPolicyID: "test-expiry-policy", Operation: Operation{ID: "expire-partial-cleanup-restart"},
	}
	residue, err := store.expireMaterialization(expire)
	if !errors.Is(err, ErrCleanupResultAmbiguous) || residue == nil {
		t.Fatalf("partially deleted cleanup = %#v, err = %v", residue, err)
	}
	var interruptedClaim localCleanupClaim
	for _, claim := range store.state.CleanupClaims {
		if claim.OperationID == expire.Operation.ID && claim.ResourceID == string(materializationID) {
			interruptedClaim = claim
			break
		}
	}
	if interruptedClaim.ClaimEntry == "" {
		t.Fatal("partial cleanup lost its exact persisted claim")
	}
	var interruptedTreeClaim localCleanupTreeClaim
	for _, claim := range store.state.CleanupTreeClaims {
		if claim.ResourceID == string(materializationID) && claim.Entry == interruptedClaim.ClaimEntry {
			interruptedTreeClaim = claim
			break
		}
	}
	if interruptedTreeClaim.ClaimEntry == "" {
		t.Fatal("cleanup interruption did not persist the exact per-entry claim")
	}
	if info, err := store.root.Lstat(interruptedTreeClaim.ClaimEntry); err != nil || !info.IsDir() {
		t.Fatal("cleanup interruption lost the atomically claimed exact root")
	}

	restarted := restartLocalStoreForTest(t, rootName)
	resource := restarted.state.CleanupResources[string(materializationID)]
	inspectionRequest := CleanupInspectionRequest{
		PolicyDomainID: prepare.PolicyDomainID, TaskID: prepare.TaskID,
		TaskWorkspaceID: prepare.TaskWorkspaceID, DebtID: "partial-cleanup-restart-debt",
		Owner: CleanupOwnerC04, ResourceClass: CleanupWorkspaceResidue,
		ResourceID: CleanupResourceID(resource.ID), ResourceGeneration: CleanupResourceGeneration(resource.Generation),
		RetryGeneration: 1, Generation: prepare.Generation, Fence: prepare.Fence,
		Operation: Operation{ID: "inspect-partial-cleanup-restart"},
	}
	inspection, err := restarted.InspectCleanup(context.Background(), inspectionRequest)
	if err != nil || inspection.Disposition != CleanupInspectionEligible {
		t.Fatalf("inspect partial cleanup = %#v, err = %v", inspection, err)
	}
	attempt := CleanupAttemptRequest{
		PolicyDomainID: prepare.PolicyDomainID, TaskID: prepare.TaskID,
		TaskWorkspaceID: prepare.TaskWorkspaceID, DebtID: inspection.DebtID,
		Owner: inspection.Owner, ResourceClass: inspection.ResourceClass,
		ResourceID: inspection.ResourceID, ResourceGeneration: inspection.ResourceGeneration,
		RetryGeneration: inspection.RetryGeneration, Generation: inspection.Generation, Fence: inspection.Fence,
		InspectionEvidenceDigest: inspection.Digest, Operation: Operation{ID: "reclaim-partial-cleanup-restart"},
	}
	evidence, err := restarted.ReclaimCleanup(context.Background(), attempt)
	if err != nil || evidence.Outcome != CleanupReclaimed {
		t.Fatalf("reclaim partial cleanup = %#v, err = %v", evidence, err)
	}
	if _, err := restarted.root.Lstat(interruptedClaim.ClaimEntry); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("partial cleanup claim remained after restart reconciliation")
	}
	if _, err := restarted.root.Lstat(interruptedTreeClaim.ClaimEntry); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("exact per-entry claim remained after restart reconciliation")
	}
	if len(restarted.state.CleanupTreeClaims) != 0 {
		t.Fatal("restart reconciliation retained completed per-entry claim authority")
	}
	if _, ok := restarted.state.CleanupResources[string(materializationID)]; ok {
		t.Fatal("restart reconciliation retained completed Cleanup Debt authority")
	}
}

func TestLocalFilesystemCleanupClaimRenameFailureRestoresReadOnlyWrapper(t *testing.T) {
	store, _ := newLocalStoreForTest(t)
	prepare := localPrepareRequestForTest("prepare-materialization-claim-rename-failure", false)
	prepared, err := store.PrepareCheckpoint(context.Background(), prepare)
	if err != nil {
		t.Fatalf("prepare Checkpoint: %v", err)
	}
	activatePreparedForTest(t, store, prepare, prepared)
	verify := localVerifyRequestForTest(prepare, prepared, "materialize-claim-rename-failure")
	if _, err := store.VerifyCheckpoint(context.Background(), verify); err != nil {
		t.Fatalf("materialize Checkpoint: %v", err)
	}
	record := store.state.Materializations[verify.Operation.ID]
	materializationID := MaterializationID("materialization-claim-rename-failure")
	if err := store.bindMaterialization(localMaterializationAuthority{
		OperationID: verify.Operation.ID, MaterializationID: materializationID,
		PolicyDomainID: prepare.PolicyDomainID, TaskID: prepare.TaskID,
		TaskWorkspaceID: prepare.TaskWorkspaceID, Generation: prepare.Generation,
		Fence: prepare.Fence, PhysicalRequired: true,
	}); err != nil {
		t.Fatalf("bind materialization: %v", err)
	}
	injected := false
	store.fault = func(event LocalFilesystemFaultEvent) error {
		if injected || event.Point != LocalFaultAfterCleanupVerify ||
			event.OperationID != "expire-claim-rename-failure" {
			return nil
		}
		for _, claim := range store.state.CleanupClaims {
			if claim.OperationID == event.OperationID {
				injected = true
				return store.root.Mkdir(claim.ClaimEntry, 0o700)
			}
		}
		return errors.New("cleanup verification hook observed no persisted claim")
	}
	expire := ExpireMaterializationRequest{
		PolicyDomainID: prepare.PolicyDomainID, TaskID: prepare.TaskID,
		TaskWorkspaceID: prepare.TaskWorkspaceID, MaterializationID: materializationID,
		RevisionID: prepare.RevisionID, CheckpointID: prepare.CheckpointID,
		Generation: prepare.Generation, Fence: prepare.Fence,
		ExpiryPolicyID: "test-expiry-policy", Operation: Operation{ID: "expire-claim-rename-failure"},
	}
	residue, err := store.expireMaterialization(expire)
	assertLocalLifecycleError(t, err, ErrorIntegrityFailure)
	if residue == nil || !injected {
		t.Fatal("claim rename failure lost its cleanup obligation")
	}
	wrapper, err := store.root.Lstat(record.Entry)
	if err != nil || wrapper.Mode().Perm()&0o222 != 0 {
		t.Fatal("claim rename failure left the original materialization writable")
	}
}

func TestLocalFilesystemRestartReclaimsIncompleteMaterializationSkeleton(t *testing.T) {
	store, rootName := newLocalStoreForTest(t)
	prepare := localPrepareRequestForTest("prepare-incomplete-materialization", false)
	prepared, err := store.PrepareCheckpoint(context.Background(), prepare)
	if err != nil {
		t.Fatalf("prepare Checkpoint: %v", err)
	}
	activatePreparedForTest(t, store, prepare, prepared)
	faulted := false
	store.fault = func(event LocalFilesystemFaultEvent) error {
		if !faulted && event.Point == LocalFaultAfterMaterializationSkeleton &&
			event.OperationID == "materialize-incomplete-skeleton" {
			faulted = true
			return syscall.ENOSPC
		}
		return nil
	}
	verify := localVerifyRequestForTest(prepare, prepared, "materialize-incomplete-skeleton")
	_, err = store.VerifyCheckpoint(context.Background(), verify)
	assertLocalLifecycleError(t, err, ErrorResourceExhausted)
	if !faulted {
		t.Fatal("materialization did not stop at the incomplete skeleton boundary")
	}

	restartedRoot, err := os.OpenRoot(rootName)
	if err != nil {
		t.Fatalf("reopen local root: %v", err)
	}
	t.Cleanup(func() { _ = restartedRoot.Close() })
	restarted, err := newLocalFilesystemStore(localFilesystemStoreConfig{
		root: restartedRoot,
		source: LocalContentSourceFunc(func(_ context.Context, digest Digest) (io.ReadCloser, error) {
			payload, ok := localStoreTestPayloads()[digest]
			if !ok {
				return nil, errors.New("content unavailable")
			}
			return io.NopCloser(bytes.NewReader(payload)), nil
		}),
		random: bytes.NewReader(deterministicLocalRandomBytes()),
		now:    func() Instant { return 100 }, authorityID: "durability-authority-1",
		adapterID: "local-development-adapter-v1",
	})
	if err != nil {
		t.Fatalf("restart local store: %v", err)
	}
	var residue localResidueRecord
	for _, candidate := range restarted.state.Residues {
		if candidate.OperationID == verify.Operation.ID && candidate.PendingMaterialization != nil {
			residue = candidate
			break
		}
	}
	if residue.ID == "" {
		t.Fatal("restart lost the incomplete generation cleanup obligation")
	}
	inspectionRequest := CleanupInspectionRequest{
		PolicyDomainID: prepare.PolicyDomainID, TaskID: prepare.TaskID,
		TaskWorkspaceID: prepare.TaskWorkspaceID, DebtID: "incomplete-materialization-debt",
		Owner: CleanupOwnerC04, ResourceClass: CleanupWorkspaceResidue,
		ResourceID: CleanupResourceID(residue.ID), ResourceGeneration: CleanupResourceGeneration(residue.Generation),
		RetryGeneration: 1, Generation: prepare.Generation, Fence: prepare.Fence,
		Operation: Operation{ID: "inspect-incomplete-materialization"},
	}
	inspection, err := restarted.InspectCleanup(context.Background(), inspectionRequest)
	if err != nil || inspection.Disposition != CleanupInspectionEligible {
		t.Fatalf("inspect incomplete generation = %#v, err = %v", inspection, err)
	}
	attempt := CleanupAttemptRequest{
		PolicyDomainID: prepare.PolicyDomainID, TaskID: prepare.TaskID,
		TaskWorkspaceID: prepare.TaskWorkspaceID, DebtID: inspection.DebtID,
		Owner: inspection.Owner, ResourceClass: inspection.ResourceClass,
		ResourceID: inspection.ResourceID, ResourceGeneration: inspection.ResourceGeneration,
		RetryGeneration: inspection.RetryGeneration, Generation: inspection.Generation, Fence: inspection.Fence,
		InspectionEvidenceDigest: inspection.Digest, Operation: Operation{ID: "reclaim-incomplete-materialization"},
	}
	evidence, err := restarted.ReclaimCleanup(context.Background(), attempt)
	if err != nil || evidence.Outcome != CleanupReclaimed {
		t.Fatalf("reclaim incomplete generation = %#v, err = %v", evidence, err)
	}
	if _, err := restarted.root.Lstat(residue.TemporaryEntry); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("reconciled incomplete generation remained on disk")
	}
}

func TestLocalFilesystemFailureResidueStaysInaccessibleAndCreatesCleanupDebt(t *testing.T) {
	store, _ := newLocalStoreForTest(t)
	persistence := NewInMemoryPersistence()
	authority := localSandboxAuthorityForTest()
	config := InMemoryConfig{
		ValidationAuthorityID:          "validation-authority-1",
		DurabilityAuthorityID:          "durability-authority-1",
		DurableObject:                  store,
		CheckpointReclamation:          store,
		Cleanup:                        store,
		Persistence:                    persistence,
		SandboxLeaseAuthorityID:        "sandbox-authority-1",
		CurrentSandboxLeaseAuthorities: []SandboxLeaseAuthority{authority},
		Now:                            func() Instant { return 100 },
		ExpiryPolicy: ExpiryPolicy{
			ID:                      "test-expiry-policy",
			MaterializationLifetime: 1_000,
			RuntimeViewLifetime:     100,
		},
	}
	lifecycle := &localFilesystemLifecycle{Lifecycle: NewInMemory(config), store: store}
	confirmed := localConfirmForTest(t, lifecycle)
	materialized := localMaterializeForTest(t, lifecycle, confirmed)
	view := localOpenViewForTest(t, lifecycle, confirmed, materialized, authority)
	manifest := localPrepareRequestForTest("unused-manifest-source", false).Manifest
	commit := localCommitForTest(confirmed, view, authority, manifest, "commit-residue")
	faulted := false
	store.fault = func(event LocalFilesystemFaultEvent) error {
		if !faulted && event.Point == LocalFaultBeforeReadback && event.OperationID == commit.Operation.ID {
			faulted = true
			return errors.New("readback interrupted")
		}
		return nil
	}
	result, err := lifecycle.CommitRuntimeView(context.Background(), commit)
	assertLocalLifecycleError(t, err, ErrorDurabilityUnverified)
	if result.RevisionID != "" || result.CheckpointID != "" || !faulted {
		t.Fatal("failed readback returned committed authority")
	}
	if len(store.state.Contents) != 0 || len(store.state.References) != 0 {
		t.Fatal("failure residue became accessible verified content")
	}
	if len(persistence.cleanupDebts) != len(store.state.Residues) || len(persistence.cleanupDebts) == 0 {
		t.Fatalf("Cleanup Debt records = %d, exact failure residues = %d",
			len(persistence.cleanupDebts), len(store.state.Residues))
	}
	var debt CleanupDebt
	for _, record := range persistence.cleanupDebts {
		debt = record.debt
		if debt.State != CleanupDebtOpen || debt.ResourceID == "" || debt.ResourceGeneration == "" ||
			!debt.Capacity.Bytes.Known || !debt.Capacity.Inodes.Known {
			t.Fatalf("failure Cleanup Debt = %#v", debt)
		}
	}
	claim := ClaimCleanupDebtRequest{
		PolicyDomainID: debt.PolicyDomainID, TaskID: debt.TaskID, DebtID: debt.DebtID,
		ExpectedRetryGeneration: debt.RetryGeneration, Operation: Operation{ID: "claim-residue"},
	}
	claim.Operation.RequestDigest = claim.CanonicalRequestDigest()
	claimed, err := lifecycle.ClaimCleanupDebt(context.Background(), claim)
	if err != nil {
		t.Fatalf("claim failure residue debt: %v", err)
	}
	reconcile := ReconcileCleanupDebtRequest{
		PolicyDomainID: claimed.PolicyDomainID, TaskID: claimed.TaskID, DebtID: claimed.DebtID,
		ClaimID: claimed.ClaimID, ClaimGeneration: claimed.ClaimGeneration,
		RetryGeneration: claimed.RetryGeneration, Generation: confirmed.Generation,
		Fence: confirmed.Fence, Operation: Operation{ID: "reconcile-residue"},
	}
	reconcile.Operation.RequestDigest = reconcile.CanonicalRequestDigest()
	resolved, err := lifecycle.ReconcileCleanupDebt(context.Background(), reconcile)
	if err != nil || resolved.State != CleanupDebtResolved || resolved.Resolution != CleanupReclaimed {
		t.Fatalf("failure residue reconciliation = %#v, err = %v", resolved, err)
	}
}

func TestLocalFilesystemPreparedReferencesActivateOnlyAfterAuthoritativeCommit(t *testing.T) {
	store, _ := newLocalStoreForTest(t)
	persistence := NewInMemoryPersistence()
	authority := localSandboxAuthorityForTest()
	faulted := false
	config := InMemoryConfig{
		ValidationAuthorityID: "validation-authority-1", DurabilityAuthorityID: "durability-authority-1",
		DurableObject: store, CheckpointReclamation: store, Cleanup: store, Persistence: persistence,
		SandboxLeaseAuthorityID:        "sandbox-authority-1",
		CurrentSandboxLeaseAuthorities: []SandboxLeaseAuthority{authority},
		Now:                            func() Instant { return 100 },
		FaultHook: func(event FaultEvent) error {
			if !faulted && event.Point == FaultBeforeAuthoritativeTransaction &&
				event.OperationID == "commit-prepared-reference" {
				faulted = true
				return errors.New("authoritative transaction interrupted")
			}
			return nil
		},
		ExpiryPolicy: ExpiryPolicy{
			ID: "test-expiry-policy", MaterializationLifetime: 1_000, RuntimeViewLifetime: 100,
		},
	}
	lifecycle := &localFilesystemLifecycle{Lifecycle: NewInMemory(config), store: store}
	confirmed := localConfirmForTest(t, lifecycle)
	materialized := localMaterializeForTest(t, lifecycle, confirmed)
	view := localOpenViewForTest(t, lifecycle, confirmed, materialized, authority)
	manifest := localPrepareRequestForTest("unused-manifest-source", false).Manifest
	commit := localCommitForTest(confirmed, view, authority, manifest, "commit-prepared-reference")

	result, err := lifecycle.CommitRuntimeView(context.Background(), commit)
	assertLocalLifecycleError(t, err, ErrorReconciliationRequired)
	if result.RevisionID != "" || result.CheckpointID != "" || !faulted {
		t.Fatal("pre-transaction failure returned committed authority")
	}
	if len(store.state.References) == 0 {
		t.Fatal("pre-transaction failure did not preserve prepared references for exact replay")
	}
	for _, reference := range store.state.References {
		if reference.Attached {
			t.Fatal("prepared reference became attached before the authoritative transaction")
		}
	}
	if len(store.state.Residues) == 0 || len(persistence.cleanupDebts) != len(store.state.Residues) {
		t.Fatalf("pre-transaction prepared residues = %d, Cleanup Debts = %d",
			len(store.state.Residues), len(persistence.cleanupDebts))
	}

	committed, err := lifecycle.CommitRuntimeView(context.Background(), commit)
	if err != nil {
		t.Fatalf("replay prepared commit: %v", err)
	}
	wantAttached := map[ContentReferenceID]struct{}{
		committed.CheckpointEvidence.ManifestReference.ID: {},
	}
	for _, reference := range committed.CheckpointEvidence.ContentReferences {
		wantAttached[reference.ID] = struct{}{}
	}
	attached := 0
	for id, reference := range store.state.References {
		_, wanted := wantAttached[id]
		if reference.Attached != wanted {
			t.Fatalf("reference %q attached = %v, want %v", id, reference.Attached, wanted)
		}
		if reference.Attached {
			attached++
		}
	}
	if attached != len(wantAttached) {
		t.Fatalf("attached reference count = %d, want %d", attached, len(wantAttached))
	}
	var debt CleanupDebt
	for _, record := range persistence.cleanupDebts {
		debt = record.debt
		break
	}
	claim := ClaimCleanupDebtRequest{
		PolicyDomainID: debt.PolicyDomainID, TaskID: debt.TaskID, DebtID: debt.DebtID,
		ExpectedRetryGeneration: debt.RetryGeneration, Operation: Operation{ID: "claim-activated-prepared-debt"},
	}
	claim.Operation.RequestDigest = claim.CanonicalRequestDigest()
	claimed, err := lifecycle.ClaimCleanupDebt(context.Background(), claim)
	if err != nil {
		t.Fatalf("claim activated prepared debt: %v", err)
	}
	reconcile := ReconcileCleanupDebtRequest{
		PolicyDomainID: claimed.PolicyDomainID, TaskID: claimed.TaskID, DebtID: claimed.DebtID,
		ClaimID: claimed.ClaimID, ClaimGeneration: claimed.ClaimGeneration,
		RetryGeneration: claimed.RetryGeneration, Generation: committed.Generation,
		Fence: committed.Fence, Operation: Operation{ID: "reconcile-activated-prepared-debt"},
	}
	reconcile.Operation.RequestDigest = reconcile.CanonicalRequestDigest()
	resolved, err := lifecycle.ReconcileCleanupDebt(context.Background(), reconcile)
	if err != nil || resolved.State != CleanupDebtResolved || resolved.Resolution != CleanupRetainedByAuthority {
		t.Fatalf("activated prepared debt resolution = %#v, err = %v", resolved, err)
	}
	replayed, err := lifecycle.CommitRuntimeView(context.Background(), commit)
	if err != nil || replayed.RevisionID != committed.RevisionID ||
		replayed.CheckpointID != committed.CheckpointID || replayed.Fence != committed.Fence {
		t.Fatal("exact replay did not preserve the activated commit result")
	}
}

func TestLocalFilesystemPreparedGenerationCannotMaterializeBeforeAuthoritativeActivation(t *testing.T) {
	store, _ := newLocalStoreForTest(t)
	prepare := localPrepareRequestForTest("prepare-inaccessible", false)
	prepared, err := store.PrepareCheckpoint(context.Background(), prepare)
	if err != nil {
		t.Fatalf("prepare Checkpoint: %v", err)
	}

	result, err := store.VerifyCheckpoint(context.Background(), localVerifyRequestForTest(
		prepare, prepared, "materialize-inaccessible",
	))
	assertLocalLifecycleError(t, err, ErrorIntegrityFailure)
	if len(result.ContentReferences) != 0 || len(store.state.Materializations) != 0 {
		t.Fatal("unactivated prepared bytes became materializable")
	}
}

func TestLocalFilesystemFailsClosedWhenFailureResidueDebtCannotBePersisted(t *testing.T) {
	store, _ := newLocalStoreForTest(t)
	persistence := NewInMemoryPersistence()
	authority := localSandboxAuthorityForTest()
	core := NewInMemory(InMemoryConfig{
		ValidationAuthorityID: "validation-authority-1", DurabilityAuthorityID: "durability-authority-1",
		DurableObject: store, CheckpointReclamation: store, Cleanup: store, Persistence: persistence,
		SandboxLeaseAuthorityID:        "sandbox-authority-1",
		CurrentSandboxLeaseAuthorities: []SandboxLeaseAuthority{authority},
		Now:                            func() Instant { return 100 },
		ExpiryPolicy: ExpiryPolicy{
			ID: "test-expiry-policy", MaterializationLifetime: 1_000, RuntimeViewLifetime: 100,
		},
	})
	lifecycle := &localFilesystemLifecycle{
		Lifecycle: cleanupObligationFailureLifecycle{Lifecycle: core},
		store:     store,
	}
	confirmed := localConfirmForTest(t, lifecycle)
	materialized := localMaterializeForTest(t, lifecycle, confirmed)
	view := localOpenViewForTest(t, lifecycle, confirmed, materialized, authority)
	manifest := localPrepareRequestForTest("unused-manifest-source", false).Manifest
	commit := localCommitForTest(confirmed, view, authority, manifest, "commit-debt-persistence-failure")
	faulted := false
	store.fault = func(event LocalFilesystemFaultEvent) error {
		if !faulted && event.Point == LocalFaultBeforeReadback && event.OperationID == commit.Operation.ID {
			faulted = true
			return errors.New("leave exact failure residue")
		}
		return nil
	}

	result, err := lifecycle.CommitRuntimeView(context.Background(), commit)
	assertLocalLifecycleError(t, err, ErrorReconciliationRequired)
	if result.RevisionID != "" || result.CheckpointID != "" || !faulted || len(store.state.Residues) == 0 {
		t.Fatal("debt persistence failure returned authority or lost its exact residue")
	}
	if len(persistence.cleanupDebts) != 0 {
		t.Fatal("failing Cleanup Debt adapter unexpectedly persisted an obligation")
	}
}

func TestLocalFilesystemExpiryFailsClosedWhenCleanupDebtCannotBePersisted(t *testing.T) {
	store, _ := newLocalStoreForTest(t)
	persistence := NewInMemoryPersistence()
	authority := localSandboxAuthorityForTest()
	now := Instant(100)
	core := NewInMemory(InMemoryConfig{
		ValidationAuthorityID: "validation-authority-1", DurabilityAuthorityID: "durability-authority-1",
		DurableObject: store, CheckpointReclamation: store, Cleanup: store, Persistence: persistence,
		SandboxLeaseAuthorityID:        "sandbox-authority-1",
		CurrentSandboxLeaseAuthorities: []SandboxLeaseAuthority{authority},
		Now:                            func() Instant { return now },
		ExpiryPolicy: ExpiryPolicy{
			ID: "test-expiry-policy", MaterializationLifetime: 10, RuntimeViewLifetime: 100,
		},
	})
	lifecycle := &localFilesystemLifecycle{
		Lifecycle: cleanupObligationFailureLifecycle{Lifecycle: core},
		store:     store,
	}
	confirmed := localConfirmForTest(t, lifecycle)
	base := localMaterializeForTest(t, lifecycle, confirmed)
	view := localOpenViewForTest(t, lifecycle, confirmed, base, authority)
	manifest := localPrepareRequestForTest("unused-manifest-source", false).Manifest
	if _, err := lifecycle.CommitRuntimeView(context.Background(), localCommitForTest(
		confirmed, view, authority, manifest, "commit-before-expiry-debt-failure",
	)); err != nil {
		t.Fatalf("commit Checkpoint: %v", err)
	}
	current := localConfirmForTestWithOperation(t, lifecycle, "confirm-before-expiry-debt-failure")
	materialize := MaterializeRequest{
		PolicyDomainID: "policy-domain-1", TaskID: "task-1", TaskWorkspaceID: current.TaskWorkspaceID,
		RevisionID: current.CurrentRevisionID, CheckpointID: current.CurrentCheckpointID,
		Generation: current.Generation, Fence: current.Fence,
		Operation: Operation{ID: "materialize-before-expiry-debt-failure"},
	}
	materialize.Operation.RequestDigest = materialize.CanonicalRequestDigest()
	materialized, err := lifecycle.Materialize(context.Background(), materialize)
	if err != nil {
		t.Fatalf("materialize committed Checkpoint: %v", err)
	}
	now = 111
	faulted := false
	store.fault = func(event LocalFilesystemFaultEvent) error {
		if !faulted && event.Point == LocalFaultBeforeCleanup &&
			event.OperationID == "expire-debt-persistence-failure" {
			faulted = true
			return errors.New("cleanup result ambiguous")
		}
		return nil
	}
	expire := ExpireMaterializationRequest{
		PolicyDomainID: "policy-domain-1", TaskID: "task-1", TaskWorkspaceID: current.TaskWorkspaceID,
		MaterializationID: materialized.MaterializationID, RevisionID: current.CurrentRevisionID,
		CheckpointID: current.CurrentCheckpointID, Generation: current.Generation, Fence: current.Fence,
		ExpiryPolicyID: "test-expiry-policy", Operation: Operation{ID: "expire-debt-persistence-failure"},
	}
	expire.Operation.RequestDigest = expire.CanonicalRequestDigest()

	result, err := lifecycle.ExpireMaterialization(context.Background(), expire)
	assertLocalLifecycleError(t, err, ErrorReconciliationRequired)
	if result.MaterializationID != "" || !faulted || len(store.state.CleanupResources) == 0 {
		t.Fatal("expiry debt persistence failure reported success or lost its cleanup resource")
	}
	if len(persistence.cleanupDebts) != 0 {
		t.Fatal("failing Cleanup Debt adapter unexpectedly persisted an expiry obligation")
	}
}

func TestLocalFilesystemExpiryDebtRegistrationAmbiguityReconcilesWithoutDuplicateDebt(t *testing.T) {
	store, rootName := newLocalStoreForTest(t)
	persistence := NewInMemoryPersistence()
	authority := localSandboxAuthorityForTest()
	now := Instant(100)
	config := InMemoryConfig{
		ValidationAuthorityID: "validation-authority-1", DurabilityAuthorityID: "durability-authority-1",
		DurableObject: store, CheckpointReclamation: store, Cleanup: store, Persistence: persistence,
		SandboxLeaseAuthorityID:        "sandbox-authority-1",
		CurrentSandboxLeaseAuthorities: []SandboxLeaseAuthority{authority},
		Now:                            func() Instant { return now },
		ExpiryPolicy: ExpiryPolicy{
			ID: "test-expiry-policy", MaterializationLifetime: 10, RuntimeViewLifetime: 100,
		},
	}
	lifecycle := &localFilesystemLifecycle{Lifecycle: NewInMemory(config), store: store}
	confirmed := localConfirmForTest(t, lifecycle)
	base := localMaterializeForTest(t, lifecycle, confirmed)
	view := localOpenViewForTest(t, lifecycle, confirmed, base, authority)
	manifest := localPrepareRequestForTest("unused-manifest-source", false).Manifest
	if _, err := lifecycle.CommitRuntimeView(context.Background(), localCommitForTest(
		confirmed, view, authority, manifest, "commit-before-expiry-debt-registration-ambiguity",
	)); err != nil {
		t.Fatalf("commit Checkpoint: %v", err)
	}
	current := localConfirmForTestWithOperation(t, lifecycle, "confirm-before-expiry-debt-registration-ambiguity")
	materialize := MaterializeRequest{
		PolicyDomainID: "policy-domain-1", TaskID: "task-1", TaskWorkspaceID: current.TaskWorkspaceID,
		RevisionID: current.CurrentRevisionID, CheckpointID: current.CurrentCheckpointID,
		Generation: current.Generation, Fence: current.Fence,
		Operation: Operation{ID: "materialize-before-expiry-debt-registration-ambiguity"},
	}
	materialize.Operation.RequestDigest = materialize.CanonicalRequestDigest()
	materialized, err := lifecycle.Materialize(context.Background(), materialize)
	if err != nil {
		t.Fatalf("materialize committed Checkpoint: %v", err)
	}

	now = 111
	expire := ExpireMaterializationRequest{
		PolicyDomainID: "policy-domain-1", TaskID: "task-1", TaskWorkspaceID: current.TaskWorkspaceID,
		MaterializationID: materialized.MaterializationID, RevisionID: current.CurrentRevisionID,
		CheckpointID: current.CurrentCheckpointID, Generation: current.Generation, Fence: current.Fence,
		ExpiryPolicyID: "test-expiry-policy", Operation: Operation{ID: "expire-debt-registration-ambiguity"},
	}
	expire.Operation.RequestDigest = expire.CanonicalRequestDigest()
	registrationFaulted := false
	store.fault = func(event LocalFilesystemFaultEvent) error {
		if event.OperationID != expire.Operation.ID {
			return nil
		}
		if event.Point == LocalFaultBeforeCleanup {
			return errors.New("cleanup result ambiguous")
		}
		if !registrationFaulted && event.Point == LocalFaultAfterCompletionMutation && event.Ordinal == 0 {
			registrationFaulted = true
			return errors.New("crash after Cleanup Debt registration mutation")
		}
		return nil
	}

	result, err := lifecycle.ExpireMaterialization(context.Background(), expire)
	assertLocalLifecycleError(t, err, ErrorReconciliationRequired)
	if result.MaterializationID != "" || !registrationFaulted || len(persistence.cleanupDebts) != 1 {
		t.Fatalf("debt registration ambiguity = %#v, faulted = %t, debts = %d",
			result, registrationFaulted, len(persistence.cleanupDebts))
	}
	inspection, err := lifecycle.InspectOperation(context.Background(), InspectOperationRequest{
		PolicyDomainID: expire.PolicyDomainID, TaskID: expire.TaskID, OperationID: expire.Operation.ID,
	})
	if err != nil || inspection.Disposition != OperationReconciliationRequired ||
		inspection.ExpireMaterialization != nil {
		t.Fatalf("ambiguous debt registration inspection = %#v, err = %v", inspection, err)
	}

	restartedStore := restartLocalStoreForTest(t, rootName)
	restartedStore.fault = func(event LocalFilesystemFaultEvent) error {
		if event.OperationID == expire.Operation.ID && event.Point == LocalFaultBeforeCleanup {
			return errors.New("cleanup remains ambiguous")
		}
		return nil
	}
	restartedConfig := config
	restartedConfig.DurableObject = restartedStore
	restartedConfig.CheckpointReclamation = restartedStore
	restartedConfig.Cleanup = restartedStore
	restarted := &localFilesystemLifecycle{Lifecycle: NewInMemory(restartedConfig), store: restartedStore}
	reconciled, err := restarted.ReconcileOperation(context.Background(), ReconcileOperationRequest{
		PolicyDomainID: expire.PolicyDomainID, TaskID: expire.TaskID, OperationID: expire.Operation.ID,
	})
	if err != nil || reconciled.Disposition != OperationTerminal ||
		reconciled.ExpireMaterialization == nil || len(persistence.cleanupDebts) != 1 ||
		len(restartedStore.state.CleanupResources) != 1 {
		t.Fatalf("debt registration reconciliation = %#v, debts = %d, resources = %d, err = %v",
			reconciled, len(persistence.cleanupDebts), len(restartedStore.state.CleanupResources), err)
	}
	for _, resource := range restartedStore.state.CleanupResources {
		if !resource.DebtRegistered {
			t.Fatal("reconciliation reported terminal before the Cleanup Debt marker was durable")
		}
	}
	repeated, err := restarted.ReconcileOperation(context.Background(), ReconcileOperationRequest{
		PolicyDomainID: expire.PolicyDomainID, TaskID: expire.TaskID, OperationID: expire.Operation.ID,
	})
	if err != nil || !reflect.DeepEqual(repeated, reconciled) || len(persistence.cleanupDebts) != 1 {
		t.Fatalf("repeated debt registration reconciliation = %#v, debts = %d, err = %v",
			repeated, len(persistence.cleanupDebts), err)
	}
}

type cleanupObligationFailureLifecycle struct {
	Lifecycle
}

func (cleanupObligationFailureLifecycle) CreateCleanupObligation(
	context.Context,
	CreateCleanupObligationRequest,
) (CleanupDebt, error) {
	return CleanupDebt{}, errors.New("Cleanup Debt persistence unavailable")
}

func TestLocalFilesystemPrePromotionResidueCleanupReclaimsTemporaryEntry(t *testing.T) {
	store, _ := newLocalStoreForTest(t)
	persistence := NewInMemoryPersistence()
	authority := localSandboxAuthorityForTest()
	config := InMemoryConfig{
		ValidationAuthorityID: "validation-authority-1", DurabilityAuthorityID: "durability-authority-1",
		DurableObject: store, CheckpointReclamation: store, Cleanup: store, Persistence: persistence,
		SandboxLeaseAuthorityID:        "sandbox-authority-1",
		CurrentSandboxLeaseAuthorities: []SandboxLeaseAuthority{authority},
		Now:                            func() Instant { return 100 },
		ExpiryPolicy: ExpiryPolicy{
			ID: "test-expiry-policy", MaterializationLifetime: 1_000, RuntimeViewLifetime: 100,
		},
	}
	lifecycle := &localFilesystemLifecycle{Lifecycle: NewInMemory(config), store: store}
	confirmed := localConfirmForTest(t, lifecycle)
	materialized := localMaterializeForTest(t, lifecycle, confirmed)
	view := localOpenViewForTest(t, lifecycle, confirmed, materialized, authority)
	manifest := localPrepareRequestForTest("unused-manifest-source", false).Manifest
	commit := localCommitForTest(confirmed, view, authority, manifest, "commit-temporary-residue")
	faulted := false
	store.fault = func(event LocalFilesystemFaultEvent) error {
		if !faulted && event.Point == LocalFaultBeforePromotion && event.OperationID == commit.Operation.ID {
			faulted = true
			return errors.New("promotion interrupted")
		}
		return nil
	}
	_, err := lifecycle.CommitRuntimeView(context.Background(), commit)
	assertLocalLifecycleError(t, err, ErrorDurabilityUnverified)
	if !faulted || len(store.state.Residues) == 0 ||
		len(persistence.cleanupDebts) != len(store.state.Residues) {
		t.Fatal("pre-promotion failure did not record every exact residue and Cleanup Debt")
	}
	var temporaryResidue localResidueRecord
	for _, residue := range store.state.Residues {
		if info, err := store.root.Lstat(residue.TemporaryEntry); err == nil && info.Mode().IsRegular() {
			temporaryResidue = residue
			break
		}
	}
	if temporaryResidue.ID == "" {
		t.Fatal("expected inaccessible temporary failure residue")
	}
	var debt CleanupDebt
	for _, record := range persistence.cleanupDebts {
		if record.debt.ResourceID == CleanupResourceID(temporaryResidue.ID) {
			debt = record.debt
			break
		}
	}
	if debt.DebtID == "" {
		t.Fatal("temporary residue had no exact Cleanup Debt")
	}
	claim := ClaimCleanupDebtRequest{
		PolicyDomainID: debt.PolicyDomainID, TaskID: debt.TaskID, DebtID: debt.DebtID,
		ExpectedRetryGeneration: debt.RetryGeneration, Operation: Operation{ID: "claim-temporary-residue"},
	}
	claim.Operation.RequestDigest = claim.CanonicalRequestDigest()
	claimed, err := lifecycle.ClaimCleanupDebt(context.Background(), claim)
	if err != nil {
		t.Fatalf("claim temporary residue debt: %v", err)
	}
	reconcile := ReconcileCleanupDebtRequest{
		PolicyDomainID: claimed.PolicyDomainID, TaskID: claimed.TaskID, DebtID: claimed.DebtID,
		ClaimID: claimed.ClaimID, ClaimGeneration: claimed.ClaimGeneration,
		RetryGeneration: claimed.RetryGeneration, Generation: confirmed.Generation,
		Fence: confirmed.Fence, Operation: Operation{ID: "reconcile-temporary-residue"},
	}
	reconcile.Operation.RequestDigest = reconcile.CanonicalRequestDigest()
	resolved, err := lifecycle.ReconcileCleanupDebt(context.Background(), reconcile)
	if err != nil || resolved.State != CleanupDebtResolved || resolved.Resolution != CleanupReclaimed {
		t.Fatalf("temporary residue reconciliation = %#v, err = %v", resolved, err)
	}
	if _, err := store.root.Lstat(temporaryResidue.TemporaryEntry); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("Cleanup Debt resolved while the temporary residue remained")
	}
}

func TestLocalFilesystemPreparedResidueCleanupForgetsUnattachedContentAuthority(t *testing.T) {
	store, _ := newLocalStoreForTest(t)
	prepare := localPrepareRequestForTest("prepare-unattached-cleanup", false)
	if _, err := store.PrepareCheckpoint(context.Background(), prepare); err != nil {
		t.Fatalf("prepare unattached content: %v", err)
	}
	var residue localResidueRecord
	for _, candidate := range store.state.Residues {
		if candidate.OperationID == prepare.Operation.ID && candidate.PendingContent != nil {
			residue = candidate
			break
		}
	}
	if residue.PendingContent == nil {
		t.Fatal("prepare did not retain an exact unattached content residue")
	}
	inspectionRequest := CleanupInspectionRequest{
		PolicyDomainID: prepare.PolicyDomainID, TaskID: prepare.TaskID,
		TaskWorkspaceID: prepare.TaskWorkspaceID, DebtID: "unattached-cleanup-debt",
		Owner: CleanupOwnerC04, ResourceClass: CleanupWorkspaceResidue,
		ResourceID: CleanupResourceID(residue.ID), ResourceGeneration: CleanupResourceGeneration(residue.Generation),
		RetryGeneration: 1, Generation: prepare.Generation, Fence: prepare.Fence,
		Operation: Operation{ID: "inspect-unattached-cleanup"},
	}
	inspection, err := store.InspectCleanup(context.Background(), inspectionRequest)
	if err != nil || inspection.Disposition != CleanupInspectionEligible {
		t.Fatalf("inspect unattached prepared residue = %#v, err = %v", inspection, err)
	}
	attempt := CleanupAttemptRequest{
		PolicyDomainID: prepare.PolicyDomainID, TaskID: prepare.TaskID,
		TaskWorkspaceID: prepare.TaskWorkspaceID, DebtID: inspection.DebtID,
		Owner: inspection.Owner, ResourceClass: inspection.ResourceClass,
		ResourceID: inspection.ResourceID, ResourceGeneration: inspection.ResourceGeneration,
		RetryGeneration: inspection.RetryGeneration, Generation: inspection.Generation, Fence: inspection.Fence,
		InspectionEvidenceDigest: inspection.Digest, Operation: Operation{ID: "reclaim-unattached-cleanup"},
	}
	evidence, err := store.ReclaimCleanup(context.Background(), attempt)
	if err != nil || evidence.Outcome != CleanupReclaimed {
		t.Fatalf("reclaim unattached prepared residue = %#v, err = %v", evidence, err)
	}
	contentID := residue.PendingContent.ID
	if _, exists := store.state.Contents[contentID]; exists {
		t.Fatal("physical cleanup retained inaccessible content registry authority")
	}
	for _, reference := range store.state.References {
		if reference.Reference.ContentID == contentID {
			t.Fatal("physical cleanup retained an unattached content reference")
		}
	}
	key := localDeduplicationKey(residue.PendingContent.Domain,
		residue.PendingContent.Digest, residue.PendingContent.Size)
	if store.state.Deduplication[key] == contentID {
		t.Fatal("physical cleanup retained a stale deduplication entry")
	}
}

func TestLocalFilesystemMaterializationFailureCreatesCleanupDebt(t *testing.T) {
	store, _ := newLocalStoreForTest(t)
	persistence := NewInMemoryPersistence()
	authority := localSandboxAuthorityForTest()
	config := InMemoryConfig{
		ValidationAuthorityID: "validation-authority-1", DurabilityAuthorityID: "durability-authority-1",
		DurableObject: store, CheckpointReclamation: store, Cleanup: store, Persistence: persistence,
		SandboxLeaseAuthorityID:        "sandbox-authority-1",
		CurrentSandboxLeaseAuthorities: []SandboxLeaseAuthority{authority},
		Now:                            func() Instant { return 100 },
		ExpiryPolicy: ExpiryPolicy{
			ID: "test-expiry-policy", MaterializationLifetime: 1_000, RuntimeViewLifetime: 100,
		},
	}
	lifecycle := &localFilesystemLifecycle{Lifecycle: NewInMemory(config), store: store}
	confirmed := localConfirmForTest(t, lifecycle)
	base := localMaterializeForTest(t, lifecycle, confirmed)
	view := localOpenViewForTest(t, lifecycle, confirmed, base, authority)
	manifest := localPrepareRequestForTest("unused-manifest-source", false).Manifest
	if _, err := lifecycle.CommitRuntimeView(context.Background(), localCommitForTest(
		confirmed, view, authority, manifest, "commit-before-materialization-failure",
	)); err != nil {
		t.Fatalf("commit Checkpoint: %v", err)
	}
	current := localConfirmForTestWithOperation(t, lifecycle, "confirm-before-materialization-failure")
	faulted := false
	store.fault = func(event LocalFilesystemFaultEvent) error {
		if !faulted && event.Point == LocalFaultBeforePromotion &&
			event.OperationID == "materialize-failure-residue" {
			faulted = true
			return errors.New("materialization promotion interrupted")
		}
		return nil
	}
	request := MaterializeRequest{
		PolicyDomainID: "policy-domain-1", TaskID: "task-1", TaskWorkspaceID: current.TaskWorkspaceID,
		RevisionID: current.CurrentRevisionID, CheckpointID: current.CurrentCheckpointID,
		Generation: current.Generation, Fence: current.Fence,
		Operation: Operation{ID: "materialize-failure-residue"},
	}
	request.Operation.RequestDigest = request.CanonicalRequestDigest()
	result, err := lifecycle.Materialize(context.Background(), request)
	assertLocalLifecycleError(t, err, ErrorDurabilityUnverified)
	if result.MaterializationID != "" || !faulted || len(persistence.cleanupDebts) != 1 {
		t.Fatal("failed materialization returned authority or omitted its Cleanup Debt")
	}
	for _, record := range persistence.cleanupDebts {
		if record.debt.ResourceClass != CleanupWorkspaceResidue ||
			record.debt.Generation != current.Generation || record.debt.Fence != current.Fence {
			t.Fatalf("materialization failure Cleanup Debt = %#v", record.debt)
		}
	}
}

func TestLocalFilesystemOldMaterializationFailureDebtUsesCurrentWorkspaceAuthority(t *testing.T) {
	store, _ := newLocalStoreForTest(t)
	persistence := NewInMemoryPersistence()
	authority := localSandboxAuthorityForTest()
	secondAuthority := authority
	secondAuthority.ID = "sandbox-lease-2"
	secondAuthority.EvidenceID = "sandbox-evidence-2"
	secondAuthority.RuntimeRunID = "runtime-run-2"
	secondAuthority.RuntimeOperationID = "runtime-operation-2"
	secondAuthority.Digest = secondAuthority.CanonicalDigest()
	now := Instant(100)
	config := InMemoryConfig{
		ValidationAuthorityID: "validation-authority-1", DurabilityAuthorityID: "durability-authority-1",
		DurableObject: store, CheckpointReclamation: store, Cleanup: store, Persistence: persistence,
		SandboxLeaseAuthorityID:        "sandbox-authority-1",
		CurrentSandboxLeaseAuthorities: []SandboxLeaseAuthority{authority, secondAuthority},
		Now:                            func() Instant { return now },
		ExpiryPolicy: ExpiryPolicy{
			ID: "test-expiry-policy", MaterializationLifetime: 10, RuntimeViewLifetime: 10,
		},
	}
	lifecycle := &localFilesystemLifecycle{Lifecycle: NewInMemory(config), store: store}
	confirmed := localConfirmForTest(t, lifecycle)
	base := localMaterializeForTest(t, lifecycle, confirmed)
	firstView := localOpenViewForTest(t, lifecycle, confirmed, base, authority)
	manifest := localPrepareRequestForTest("unused-old-cleanup-manifest", false).Manifest
	if _, err := lifecycle.CommitRuntimeView(context.Background(), localCommitForTest(
		confirmed, firstView, authority, manifest, "commit-before-old-cleanup",
	)); err != nil {
		t.Fatalf("commit materializable Checkpoint: %v", err)
	}
	confirmed = localConfirmForTestWithOperation(t, lifecycle, "confirm-before-old-cleanup")
	materialize := MaterializeRequest{
		PolicyDomainID: "policy-domain-1", TaskID: "task-1",
		TaskWorkspaceID: confirmed.TaskWorkspaceID, RevisionID: confirmed.CurrentRevisionID,
		CheckpointID: confirmed.CurrentCheckpointID, Generation: confirmed.Generation, Fence: confirmed.Fence,
		Operation: Operation{ID: "materialize-before-old-cleanup"},
	}
	materialize.Operation.RequestDigest = materialize.CanonicalRequestDigest()
	materialized, err := lifecycle.Materialize(context.Background(), materialize)
	if err != nil {
		t.Fatalf("materialize committed Checkpoint: %v", err)
	}
	open := OpenRuntimeViewRequest{
		PolicyDomainID: "policy-domain-1", TaskID: "task-1",
		TaskWorkspaceID: confirmed.TaskWorkspaceID, MaterializationID: materialized.MaterializationID,
		BaseRevisionID: confirmed.CurrentRevisionID, PhaseRunID: secondAuthority.PhaseRunID,
		RuntimeRunID: secondAuthority.RuntimeRunID, RuntimeOperationID: secondAuthority.RuntimeOperationID,
		SandboxLeaseAuthority: secondAuthority, EffectClass: RuntimeViewMutating, ExpiresAt: 900,
		Generation: confirmed.Generation, Fence: confirmed.Fence, Operation: Operation{ID: "open-before-old-cleanup"},
	}
	open.Operation.RequestDigest = open.CanonicalRequestDigest()
	view, err := lifecycle.OpenRuntimeView(context.Background(), open)
	if err != nil {
		t.Fatalf("open view before recovery fence: %v", err)
	}
	fence := FenceRuntimeViewRequest{
		PolicyDomainID: "policy-domain-1", TaskID: "task-1",
		TaskWorkspaceID: confirmed.TaskWorkspaceID, RuntimeViewID: view.RuntimeViewID,
		RuntimeOperationID: view.RuntimeOperationID, SandboxLeaseAuthority: view.SandboxLeaseAuthority,
		BaseRevisionID: view.BaseRevisionID, ExpectedCurrentRevision: confirmed.CurrentRevisionID,
		Generation: confirmed.Generation, Fence: confirmed.Fence,
		Reason: RuntimeViewRecoveryGenerationAdvanced, Operation: Operation{ID: "advance-for-old-cleanup"},
	}
	fence.Operation.RequestDigest = fence.CanonicalRequestDigest()
	if _, err := lifecycle.FenceRuntimeView(context.Background(), fence); err != nil {
		t.Fatalf("advance recovery authority: %v", err)
	}
	current := localConfirmForTestWithOperation(t, lifecycle, "confirm-current-cleanup-authority")
	if current.Generation <= confirmed.Generation || current.Fence <= confirmed.Fence {
		t.Fatal("recovery fence did not advance workspace authority")
	}

	now = 1_001
	faulted := false
	store.fault = func(event LocalFilesystemFaultEvent) error {
		if !faulted && event.Point == LocalFaultBeforeCleanup && event.OperationID == "expire-old-materialization" {
			faulted = true
			return errors.New("physical cleanup unavailable")
		}
		return nil
	}
	expire := ExpireMaterializationRequest{
		PolicyDomainID: "policy-domain-1", TaskID: "task-1",
		TaskWorkspaceID: confirmed.TaskWorkspaceID, MaterializationID: materialized.MaterializationID,
		RevisionID: confirmed.CurrentRevisionID, CheckpointID: confirmed.CurrentCheckpointID,
		Generation: confirmed.Generation, Fence: confirmed.Fence,
		ExpiryPolicyID: "test-expiry-policy", Operation: Operation{ID: "expire-old-materialization"},
	}
	expire.Operation.RequestDigest = expire.CanonicalRequestDigest()
	if _, err := lifecycle.ExpireMaterialization(context.Background(), expire); err != nil {
		t.Fatalf("expire old materialization with durable Cleanup Debt: %v", err)
	}
	if !faulted || len(persistence.cleanupDebts) != 1 {
		t.Fatal("old materialization cleanup failure omitted its Cleanup Debt")
	}
	for _, record := range persistence.cleanupDebts {
		if record.debt.Generation != current.Generation || record.debt.Fence != current.Fence {
			t.Fatalf("Cleanup Debt authority = %d/%d, want current %d/%d",
				record.debt.Generation, record.debt.Fence, current.Generation, current.Fence)
		}
	}
}

func TestLocalFilesystemRestartDoesNotPromoteTerminalFailureResidue(t *testing.T) {
	store, rootName := newLocalStoreForTest(t)
	request := localPrepareRequestForTest("prepare-terminal-restart", false)
	faulted := false
	store.fault = func(event LocalFilesystemFaultEvent) error {
		if !faulted && event.Point == LocalFaultBeforeReadback && event.OperationID == request.Operation.ID {
			faulted = true
			return errors.New("terminal readback failure")
		}
		return nil
	}
	if _, err := store.PrepareCheckpoint(context.Background(), request); err == nil || !faulted {
		t.Fatalf("prepare terminal failure = %v", err)
	}
	if len(store.state.Contents) != 0 || len(store.state.Residues) == 0 ||
		len(store.state.CleanupResources) != len(store.state.Residues) {
		t.Fatal("terminal failure did not leave only cleanup-owned exact residues")
	}

	restartedRoot, err := os.OpenRoot(rootName)
	if err != nil {
		t.Fatalf("reopen local root: %v", err)
	}
	t.Cleanup(func() { _ = restartedRoot.Close() })
	restarted, err := newLocalFilesystemStore(localFilesystemStoreConfig{
		root: restartedRoot,
		source: LocalContentSourceFunc(func(_ context.Context, digest Digest) (io.ReadCloser, error) {
			payload, ok := localStoreTestPayloads()[digest]
			if !ok {
				return nil, errors.New("content unavailable")
			}
			return io.NopCloser(bytes.NewReader(payload)), nil
		}),
		random: bytes.NewReader(deterministicLocalRandomBytes()),
		now:    func() Instant { return 100 }, authorityID: "durability-authority-1",
		adapterID: "local-development-adapter-v1",
	})
	if err != nil {
		t.Fatalf("restart local store: %v", err)
	}
	if len(restarted.state.Contents) != 0 || len(restarted.state.References) != 0 ||
		len(restarted.state.Residues) != len(store.state.Residues) ||
		len(restarted.state.CleanupResources) != len(store.state.CleanupResources) {
		t.Fatal("restart promoted terminal failure residue into content authority")
	}
}

func TestLocalFilesystemRestartReplaysAndActivatesExactPreparedGeneration(t *testing.T) {
	store, rootName := newLocalStoreForTest(t)
	request := localPrepareRequestForTest("prepare-process-restart", false)
	prepared, err := store.PrepareCheckpoint(context.Background(), request)
	if err != nil {
		t.Fatalf("prepare before process restart: %v", err)
	}
	if len(store.state.Residues) == 0 {
		t.Fatal("prepared physical generations were not retained for activation")
	}

	restartedRoot, err := os.OpenRoot(rootName)
	if err != nil {
		t.Fatalf("reopen local root: %v", err)
	}
	t.Cleanup(func() { _ = restartedRoot.Close() })
	restarted, err := newLocalFilesystemStore(localFilesystemStoreConfig{
		root: restartedRoot,
		source: LocalContentSourceFunc(func(_ context.Context, digest Digest) (io.ReadCloser, error) {
			payload, ok := localStoreTestPayloads()[digest]
			if !ok {
				return nil, errors.New("content unavailable")
			}
			return io.NopCloser(bytes.NewReader(payload)), nil
		}),
		random: bytes.NewReader(deterministicLocalRandomBytes()),
		now:    func() Instant { return 100 }, authorityID: "durability-authority-1",
		adapterID: "local-development-adapter-v1",
	})
	if err != nil {
		t.Fatalf("restart local store: %v", err)
	}
	replayed, err := restarted.PrepareCheckpoint(context.Background(), request)
	if err != nil || replayed.ManifestReference.ID != prepared.ManifestReference.ID ||
		len(replayed.ContentReferences) != len(prepared.ContentReferences) ||
		replayed.ContentReferences[0].ID != prepared.ContentReferences[0].ID {
		t.Fatalf("restart prepared replay = %#v, err = %v", replayed, err)
	}
	evidence := CheckpointEvidence{
		Manifest: prepared.Manifest, ManifestReference: prepared.ManifestReference,
		ContentReferences: prepared.ContentReferences, DurabilityReceipts: prepared.DurabilityReceipts,
	}
	if err := restarted.activateCheckpoint(request.Operation.ID, evidence); err != nil {
		t.Fatalf("activate exact prepared generation after restart: %v", err)
	}
	if err := restarted.activateCheckpoint(request.Operation.ID, evidence); err != nil {
		t.Fatalf("idempotent activation after restart: %v", err)
	}
	if len(restarted.state.Residues) != 0 || len(restarted.state.CleanupResources) != 0 {
		t.Fatal("activation retained prepared residue or cleanup authority")
	}
	for _, reference := range restarted.state.References {
		if !reference.Attached {
			t.Fatal("restart activation left an exact prepared reference unattached")
		}
	}
}

func newLocalStoreForTest(t *testing.T) (*localFilesystemStore, string) {
	t.Helper()
	rootName := t.TempDir()
	root, err := os.OpenRoot(rootName)
	if err != nil {
		t.Fatalf("open test root: %v", err)
	}
	t.Cleanup(func() { _ = root.Close() })
	t.Cleanup(func() { makeLocalTestTreeRemovable(rootName) })
	payloads := localStoreTestPayloads()
	store, err := newLocalFilesystemStore(localFilesystemStoreConfig{
		root: root,
		source: LocalContentSourceFunc(func(_ context.Context, digest Digest) (io.ReadCloser, error) {
			payload, ok := payloads[digest]
			if !ok {
				return nil, errors.New("content unavailable")
			}
			return io.NopCloser(bytes.NewReader(payload)), nil
		}),
		random: bytes.NewReader(deterministicLocalRandomBytes()),
		now:    func() Instant { return 100 }, authorityID: "durability-authority-1",
		adapterID: "local-development-adapter-v1",
	})
	if err != nil {
		t.Fatalf("create local store: %v", err)
	}
	return store, rootName
}

func restartLocalStoreForTest(t *testing.T, rootName string) *localFilesystemStore {
	t.Helper()
	root, err := os.OpenRoot(rootName)
	if err != nil {
		t.Fatalf("reopen local root: %v", err)
	}
	t.Cleanup(func() { _ = root.Close() })
	payloads := localStoreTestPayloads()
	store, err := newLocalFilesystemStore(localFilesystemStoreConfig{
		root: root,
		source: LocalContentSourceFunc(func(_ context.Context, digest Digest) (io.ReadCloser, error) {
			payload, ok := payloads[digest]
			if !ok {
				return nil, errors.New("content unavailable")
			}
			return io.NopCloser(bytes.NewReader(payload)), nil
		}),
		random: bytes.NewReader(bytes.Repeat([]byte{0xa5}, 4096)),
		now:    func() Instant { return 100 }, authorityID: "durability-authority-1",
		adapterID: "local-development-adapter-v1",
	})
	if err != nil {
		t.Fatalf("restart local store: %v", err)
	}
	return store
}

func makeLocalTestTreeRemovable(root string) {
	_ = filepath.WalkDir(root, func(entry string, item os.DirEntry, err error) error {
		if err == nil && item.IsDir() {
			_ = os.Chmod(entry, 0o700)
		}
		return nil
	})
}

func localPrepareRequestForTest(operationID OperationID, twoMembers bool) PrepareCheckpointContentRequest {
	payloads := localStoreTestPayloads()
	members := []DeclaredStateMember{{
		ID: "member-1", LogicalMember: "state/deck.json", Type: StateMemberRegularFile,
		Mode: 0o600, Class: StateMemberTaskOwnedMutable,
		ContentDigest: digestBytesLocal(payloads[digestBytesLocal([]byte("task-owned-state-one"))]),
		Size:          uint64(len("task-owned-state-one")),
	}}
	if twoMembers {
		members = append(members, DeclaredStateMember{
			ID: "member-2", LogicalMember: "state/notes.json", Type: StateMemberRegularFile,
			Mode: 0o600, Class: StateMemberTaskOwnedMutable,
			ContentDigest: digestBytesLocal([]byte("task-owned-state-two")),
			Size:          uint64(len("task-owned-state-two")),
		})
	}
	manifest := DeclaredStateManifest{Members: members}
	manifest.Digest = manifest.CanonicalDigest()
	return PrepareCheckpointContentRequest{
		PolicyDomainID: "policy-domain-1", TaskID: "task-1", TaskWorkspaceID: "workspace-1",
		RuntimeViewID: "view-1", RevisionID: "revision-2", CheckpointID: "checkpoint-1",
		Manifest: manifest, CanonicalManifest: manifest.CanonicalBytes(), Generation: 1, Fence: 1,
		Operation: Operation{ID: operationID, RequestDigest: digestBytesLocal([]byte("request-" + string(operationID)))},
	}
}

func localStoreTestPayloads() map[Digest][]byte {
	first := []byte("task-owned-state-one")
	second := []byte("task-owned-state-two")
	return map[Digest][]byte{digestBytesLocal(first): first, digestBytesLocal(second): second}
}

func localVerifyRequestForTest(
	prepare PrepareCheckpointContentRequest,
	prepared VerifiedCheckpointContent,
	operationID OperationID,
) VerifyCheckpointContentRequest {
	return VerifyCheckpointContentRequest{
		PolicyDomainID: prepare.PolicyDomainID, TaskID: prepare.TaskID,
		TaskWorkspaceID: prepare.TaskWorkspaceID, RevisionID: prepare.RevisionID,
		CheckpointID: prepare.CheckpointID, Manifest: prepared.Manifest,
		CanonicalManifest: prepared.Manifest.CanonicalBytes(), Expected: prepared,
		Generation: prepare.Generation, Fence: prepare.Fence,
		Operation: Operation{ID: operationID, RequestDigest: digestBytesLocal([]byte("request-" + string(operationID)))},
	}
}

func activatePreparedForTest(
	t *testing.T,
	store *localFilesystemStore,
	prepare PrepareCheckpointContentRequest,
	prepared VerifiedCheckpointContent,
) {
	t.Helper()
	if err := store.activateCheckpoint(prepare.Operation.ID, CheckpointEvidence{
		Manifest: prepared.Manifest, ManifestReference: prepared.ManifestReference,
		ContentReferences: prepared.ContentReferences, DurabilityReceipts: prepared.DurabilityReceipts,
	}); err != nil {
		t.Fatalf("activate prepared Checkpoint: %v", err)
	}
}

func localResourcesForTest(prepared VerifiedCheckpointContent) ([]CheckpointContentGeneration, Digest) {
	receipts := make(map[ContentID]DurabilityReceipt, len(prepared.DurabilityReceipts))
	for _, receipt := range prepared.DurabilityReceipts {
		receipts[receipt.ContentID] = receipt
	}
	references := append([]ContentReference{prepared.ManifestReference}, prepared.ContentReferences...)
	resources := make([]CheckpointContentGeneration, 0, len(references))
	for _, reference := range references {
		receipt := receipts[reference.ContentID]
		resources = append(resources, CheckpointContentGeneration{
			ContentID: reference.ContentID, ReferenceID: reference.ID,
			ReceiptID: receipt.ID, GenerationID: receipt.DurabilityGenerationID,
		})
	}
	sort.Slice(resources, func(i, j int) bool {
		if resources[i].ContentID == resources[j].ContentID {
			return resources[i].ReferenceID < resources[j].ReferenceID
		}
		return resources[i].ContentID < resources[j].ContentID
	})
	return resources, canonicalDigest(resources)
}

func localReferenceTransitionForTest(
	prepare PrepareCheckpointContentRequest,
	resources []CheckpointContentGeneration,
	exactRoot Digest,
	operationID OperationID,
) CheckpointContentReferenceTransitionRequest {
	return CheckpointContentReferenceTransitionRequest{
		PolicyDomainID: prepare.PolicyDomainID, TaskID: prepare.TaskID,
		TaskWorkspaceID: prepare.TaskWorkspaceID, CheckpointID: prepare.CheckpointID,
		RevisionID: prepare.RevisionID, RetentionGeneration: 2,
		Resources: resources, ExactGenerationRoot: exactRoot,
		Generation: prepare.Generation, Fence: prepare.Fence,
		Operation: Operation{ID: operationID, RequestDigest: digestBytesLocal([]byte("request-" + string(operationID)))},
	}
}

func localReclaimRequestForTest(
	prepare PrepareCheckpointContentRequest,
	resources []CheckpointContentGeneration,
	exactRoot Digest,
	operationID OperationID,
) ReclaimCheckpointContentRequest {
	return ReclaimCheckpointContentRequest{
		PolicyDomainID: prepare.PolicyDomainID, TaskID: prepare.TaskID,
		TaskWorkspaceID: prepare.TaskWorkspaceID, CheckpointID: prepare.CheckpointID,
		RevisionID: prepare.RevisionID, RetentionGeneration: 2,
		Resources: resources, ExactGenerationRoot: exactRoot,
		Generation: prepare.Generation, Fence: prepare.Fence,
		Operation: Operation{ID: operationID, RequestDigest: digestBytesLocal([]byte("request-" + string(operationID)))},
	}
}

func deterministicLocalRandomBytes() []byte {
	result := make([]byte, 16*512)
	for index := range result {
		result[index] = byte(index/16 + 1)
	}
	return result
}

func assertLocalLifecycleError(t *testing.T, err error, code ErrorCode) {
	t.Helper()
	var lifecycleError *Error
	if !errors.As(err, &lifecycleError) || lifecycleError.Code != code {
		t.Fatalf("local lifecycle error = %T/%v, want %q", err, err, code)
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func localSandboxAuthorityForTest() SandboxLeaseAuthority {
	authority := SandboxLeaseAuthority{
		ID: "sandbox-lease-1", EvidenceID: "sandbox-evidence-1",
		AuthorityID: "sandbox-authority-1", PolicyDomainID: "policy-domain-1",
		TaskID: "task-1", PhaseRunID: "phase-run-1", RuntimeRunID: "runtime-run-1",
		RuntimeOperationID: "runtime-operation-1", EffectClass: RuntimeViewMutating,
		LeaseGeneration: 1, LeaseFence: 1, ExpiresAt: 1_000,
	}
	authority.Digest = authority.CanonicalDigest()
	return authority
}

func localConfirmForTest(t *testing.T, lifecycle Lifecycle) ConfirmTaskWorkspaceResult {
	return localConfirmForTestWithOperation(t, lifecycle, "confirm-residue")
}

func localConfirmForTestWithOperation(
	t *testing.T,
	lifecycle Lifecycle,
	operationID OperationID,
) ConfirmTaskWorkspaceResult {
	t.Helper()
	request := ConfirmTaskWorkspaceRequest{
		PolicyDomainID: "policy-domain-1", TaskID: "task-1", Operation: Operation{ID: operationID},
	}
	request.Operation.RequestDigest = request.CanonicalRequestDigest()
	result, err := lifecycle.ConfirmTaskWorkspace(context.Background(), request)
	if err != nil {
		t.Fatalf("confirm Task Workspace: %v", err)
	}
	return result
}

func localMaterializeForTest(
	t *testing.T,
	lifecycle Lifecycle,
	confirmed ConfirmTaskWorkspaceResult,
) MaterializeResult {
	t.Helper()
	request := MaterializeRequest{
		PolicyDomainID: "policy-domain-1", TaskID: "task-1",
		TaskWorkspaceID: confirmed.TaskWorkspaceID, RevisionID: confirmed.CurrentRevisionID,
		CheckpointID: confirmed.CurrentCheckpointID, Generation: confirmed.Generation,
		Fence: confirmed.Fence, Operation: Operation{ID: "materialize-residue"},
	}
	request.Operation.RequestDigest = request.CanonicalRequestDigest()
	result, err := lifecycle.Materialize(context.Background(), request)
	if err != nil {
		t.Fatalf("materialize Task Workspace: %v", err)
	}
	return result
}

func localOpenViewForTest(
	t *testing.T,
	lifecycle Lifecycle,
	confirmed ConfirmTaskWorkspaceResult,
	materialized MaterializeResult,
	authority SandboxLeaseAuthority,
) OpenRuntimeViewResult {
	t.Helper()
	request := OpenRuntimeViewRequest{
		PolicyDomainID: "policy-domain-1", TaskID: "task-1",
		TaskWorkspaceID: confirmed.TaskWorkspaceID, MaterializationID: materialized.MaterializationID,
		BaseRevisionID: confirmed.CurrentRevisionID, PhaseRunID: authority.PhaseRunID,
		RuntimeRunID: authority.RuntimeRunID, RuntimeOperationID: authority.RuntimeOperationID,
		SandboxLeaseAuthority: authority, EffectClass: RuntimeViewMutating, ExpiresAt: 900,
		Generation: confirmed.Generation, Fence: confirmed.Fence,
		Operation: Operation{ID: "open-residue"},
	}
	request.Operation.RequestDigest = request.CanonicalRequestDigest()
	result, err := lifecycle.OpenRuntimeView(context.Background(), request)
	if err != nil {
		t.Fatalf("open Runtime View: %v", err)
	}
	return result
}

func localCommitForTest(
	confirmed ConfirmTaskWorkspaceResult,
	view OpenRuntimeViewResult,
	authority SandboxLeaseAuthority,
	manifest DeclaredStateManifest,
	operationID OperationID,
) CommitRuntimeViewRequest {
	validation := ValidationEvidence{
		ID: "validation-evidence-1", ValidationAuthorityID: "validation-authority-1",
		PolicyDomainID: "policy-domain-1", TaskID: "task-1",
		TaskWorkspaceID: confirmed.TaskWorkspaceID, RuntimeViewID: view.RuntimeViewID,
		BaseRevisionID: confirmed.CurrentRevisionID, PhaseRunID: view.PhaseRunID,
		RuntimeRunID: view.RuntimeRunID, RuntimeOperationID: view.RuntimeOperationID,
		SandboxLeaseAuthorityDigest: authority.Digest, ManifestDigest: manifest.Digest,
		Generation: confirmed.Generation, Fence: confirmed.Fence, Decision: ValidationAccepted,
	}
	validation.Digest = validation.CanonicalDigest()
	request := CommitRuntimeViewRequest{
		PolicyDomainID: "policy-domain-1", TaskID: "task-1",
		TaskWorkspaceID: confirmed.TaskWorkspaceID, RuntimeViewID: view.RuntimeViewID,
		RuntimeOperationID: view.RuntimeOperationID, SandboxLeaseAuthority: authority,
		BaseRevisionID: confirmed.CurrentRevisionID, ExpectedCurrentRevision: confirmed.CurrentRevisionID,
		Generation: confirmed.Generation, Fence: confirmed.Fence,
		ValidationEvidence: validation, DeclaredStateManifest: manifest,
		Operation: Operation{ID: operationID},
	}
	request.Operation.RequestDigest = request.CanonicalRequestDigest()
	return request
}
