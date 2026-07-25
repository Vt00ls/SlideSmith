package taskworkspace

import (
	"context"
	"errors"
	"os"
	"path"
	"sort"
	"strings"
)

var (
	errLocalCleanupReferenceRetained = errors.New("local cleanup reference retained")
	errLocalCleanupLeaseRetained     = errors.New("local cleanup lease retained")
)

func (s *localFilesystemStore) AttachCheckpointReferences(
	_ context.Context,
	request CheckpointContentReferenceTransitionRequest,
) (CheckpointContentReferenceTransitionEvidence, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.transitionCheckpointReferences(request, true)
}

func (s *localFilesystemStore) ReleaseCheckpointReferences(
	_ context.Context,
	request CheckpointContentReferenceTransitionRequest,
) (CheckpointContentReferenceTransitionEvidence, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.transitionCheckpointReferences(request, false)
}

func (s *localFilesystemStore) transitionCheckpointReferences(
	request CheckpointContentReferenceTransitionRequest,
	attached bool,
) (CheckpointContentReferenceTransitionEvidence, error) {
	if replay, ok := s.state.ReferenceTransitions[request.Operation.ID]; ok {
		return replay, nil
	}
	for ordinal, resource := range request.Resources {
		reference, referenceOK := s.state.References[resource.ReferenceID]
		content, contentOK := s.state.Contents[resource.ContentID]
		if !referenceOK || !contentOK || reference.Reference.ContentID != resource.ContentID ||
			content.Generation != resource.GenerationID || content.Receipt.ID != resource.ReceiptID ||
			content.Domain != request.PolicyDomainID {
			return CheckpointContentReferenceTransitionEvidence{}, &Error{Code: ErrorIntegrityFailure}
		}
		if attached {
			event := LocalFilesystemFaultEvent{
				OperationID: request.Operation.ID, SubjectID: string(request.CheckpointID),
			}
			if err := s.restoreCheckpointContentClaims(event, content); err != nil {
				return CheckpointContentReferenceTransitionEvidence{}, err
			}
			if err := s.verifyContentRecord(request.Operation.ID, ordinal, content); err != nil {
				return CheckpointContentReferenceTransitionEvidence{}, err
			}
		}
	}
	for _, resource := range request.Resources {
		reference := s.state.References[resource.ReferenceID]
		reference.Attached = attached
		s.state.References[resource.ReferenceID] = reference
	}
	state := CheckpointContentReferencesReleased
	if attached {
		state = CheckpointContentReferencesAttached
	}
	evidence := CheckpointContentReferenceTransitionEvidence{
		ID:             EvidenceID(localEvidenceID("reference-transition", request.Operation.ID)),
		PolicyDomainID: request.PolicyDomainID, TaskID: request.TaskID,
		TaskWorkspaceID: request.TaskWorkspaceID, CheckpointID: request.CheckpointID,
		RetentionGeneration: request.RetentionGeneration,
		ExactGenerationRoot: request.ExactGenerationRoot, State: state,
		Generation: request.Generation, Fence: request.Fence,
		OperationID: request.Operation.ID, ObservedAt: s.now(),
	}
	evidence.Digest = evidence.CanonicalDigest()
	s.state.ReferenceTransitions[request.Operation.ID] = evidence
	if err := s.persistState(); err != nil {
		return CheckpointContentReferenceTransitionEvidence{}, ErrDurableObjectResultAmbiguous
	}
	return evidence, nil
}

func (s *localFilesystemStore) ReclaimCheckpointContent(
	_ context.Context,
	request ReclaimCheckpointContentRequest,
) (CheckpointContentReclamationEvidence, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if replay, ok := s.state.Reclamations[request.Operation.ID]; ok {
		return replay, nil
	}
	event := LocalFilesystemFaultEvent{OperationID: request.Operation.ID, SubjectID: string(request.CheckpointID)}
	state := CheckpointInventoryAbsent
	anyPresent := false
	for _, resource := range request.Resources {
		reference, referenceOK := s.state.References[resource.ReferenceID]
		content, contentOK := s.state.Contents[resource.ContentID]
		if referenceOK && reference.Reference.ContentID != resource.ContentID {
			return s.retainedCheckpointEvidence(request, CheckpointMechanicsBlocked,
				CheckpointMechanicsClear, CheckpointMechanicsClear, CheckpointInventoryPresent)
		}
		if referenceOK && reference.Attached {
			if contentOK {
				if err := s.restoreCheckpointContentClaims(event, content); err != nil {
					return CheckpointContentReclamationEvidence{}, err
				}
			}
			return s.retainedCheckpointEvidence(request, CheckpointMechanicsBlocked,
				CheckpointMechanicsClear, CheckpointMechanicsClear, CheckpointInventoryPresent)
		}
		if !contentOK {
			continue
		}
		if content.Domain != request.PolicyDomainID || content.Generation != resource.GenerationID ||
			content.Receipt.ID != resource.ReceiptID {
			return CheckpointContentReclamationEvidence{}, &Error{Code: ErrorIntegrityFailure}
		}
		if content.Quarantined {
			return s.retainedCheckpointEvidence(request, CheckpointMechanicsClear,
				CheckpointMechanicsClear, CheckpointMechanicsBlocked, CheckpointInventoryPresent)
		}
		for _, candidate := range s.state.References {
			if candidate.Reference.ContentID == resource.ContentID && candidate.Attached {
				if err := s.restoreCheckpointContentClaims(event, content); err != nil {
					return CheckpointContentReclamationEvidence{}, err
				}
				return s.retainedCheckpointEvidence(request, CheckpointMechanicsBlocked,
					CheckpointMechanicsClear, CheckpointMechanicsClear, CheckpointInventoryPresent)
			}
		}
		if s.hasActiveMaterialization(resource.ContentID) {
			if err := s.restoreCheckpointContentClaims(event, content); err != nil {
				return CheckpointContentReclamationEvidence{}, err
			}
			return s.retainedCheckpointEvidence(request, CheckpointMechanicsClear,
				CheckpointMechanicsBlocked, CheckpointMechanicsClear, CheckpointInventoryPresent)
		}
		info, err := s.root.Lstat(content.Entry)
		if err == nil {
			if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
				return CheckpointContentReclamationEvidence{}, &Error{Code: ErrorIntegrityFailure}
			}
			if err := s.verifyContentRecord(request.Operation.ID, 0, content); err != nil {
				return s.retainedCheckpointEvidence(request, CheckpointMechanicsClear,
					CheckpointMechanicsClear, CheckpointMechanicsBlocked, CheckpointInventoryPresent)
			}
			anyPresent = true
		} else if !errors.Is(err, os.ErrNotExist) {
			return CheckpointContentReclamationEvidence{}, ErrDurableObjectResultAmbiguous
		}
	}
	if err := s.inject(LocalFaultBeforeCleanup, event); err != nil {
		return CheckpointContentReclamationEvidence{}, ErrDurableObjectResultAmbiguous
	}
	// Revalidate authority and atomically claim each exact physical generation
	// immediately before deleting it. A pathname replacement can be moved into
	// quarantine by the claim, but it is verified and restored rather than
	// deleted when its private device/inode identity does not match.
	for ordinal, resource := range request.Resources {
		content, contentOK := s.state.Contents[resource.ContentID]
		if !contentOK {
			continue
		}
		if content.Domain != request.PolicyDomainID || content.Generation != resource.GenerationID ||
			content.Receipt.ID != resource.ReceiptID {
			return CheckpointContentReclamationEvidence{}, &Error{Code: ErrorStaleAuthority}
		}
		for _, candidate := range s.state.References {
			if candidate.Reference.ContentID == resource.ContentID && candidate.Attached {
				if err := s.restoreCheckpointContentClaims(event, content); err != nil {
					return CheckpointContentReclamationEvidence{}, err
				}
				return s.retainedCheckpointEvidence(request, CheckpointMechanicsBlocked,
					CheckpointMechanicsClear, CheckpointMechanicsClear, CheckpointInventoryPresent)
			}
		}
		if s.hasActiveMaterialization(resource.ContentID) {
			if err := s.restoreCheckpointContentClaims(event, content); err != nil {
				return CheckpointContentReclamationEvidence{}, err
			}
			return s.retainedCheckpointEvidence(request, CheckpointMechanicsClear,
				CheckpointMechanicsBlocked, CheckpointMechanicsClear, CheckpointInventoryPresent)
		}
		revalidate := func() error {
			current, ok := s.state.Contents[resource.ContentID]
			if !ok || current.Domain != request.PolicyDomainID ||
				current.Generation != resource.GenerationID || current.Receipt.ID != resource.ReceiptID ||
				current.Entry != content.Entry || current.PhysicalIdentity != content.PhysicalIdentity {
				return &Error{Code: ErrorStaleAuthority}
			}
			for _, candidate := range s.state.References {
				if candidate.Reference.ContentID == resource.ContentID && candidate.Attached {
					return errLocalCleanupReferenceRetained
				}
			}
			if s.hasActiveMaterialization(resource.ContentID) {
				return errLocalCleanupLeaseRetained
			}
			return nil
		}
		claimKey := localCleanupClaimKey(request.Operation.ID, string(resource.ContentID), content.Entry)
		rootClaim, rootClaimExists := s.state.CleanupClaims[claimKey]
		reconciled, reconcileErr := s.reconcileCleanupTreeClaims(
			event, string(resource.ContentID), string(resource.GenerationID), revalidate,
		)
		if errors.Is(reconcileErr, errLocalCleanupReferenceRetained) ||
			errors.Is(reconcileErr, errLocalCleanupLeaseRetained) {
			if rootClaimExists {
				if err := s.restoreCleanupClaim(
					event, string(resource.ContentID), content.Entry, rootClaim.ClaimEntry,
				); err != nil {
					return CheckpointContentReclamationEvidence{}, ErrDurableObjectResultAmbiguous
				}
			}
			if errors.Is(reconcileErr, errLocalCleanupReferenceRetained) {
				return s.retainedCheckpointEvidence(request, CheckpointMechanicsBlocked,
					CheckpointMechanicsClear, CheckpointMechanicsClear, CheckpointInventoryPresent)
			}
			return s.retainedCheckpointEvidence(request, CheckpointMechanicsClear,
				CheckpointMechanicsBlocked, CheckpointMechanicsClear, CheckpointInventoryPresent)
		}
		if reconcileErr != nil {
			return CheckpointContentReclamationEvidence{}, reconcileErr
		}
		if rootClaimExists {
			if _, rootRemoved := reconciled[rootClaim.ClaimEntry]; rootRemoved {
				anyPresent = true
				delete(s.state.Contents, resource.ContentID)
				if s.state.Deduplication[localDeduplicationKey(content.Domain, content.Digest, content.Size)] == resource.ContentID {
					delete(s.state.Deduplication, localDeduplicationKey(content.Domain, content.Digest, content.Size))
				}
				delete(s.state.References, resource.ReferenceID)
				delete(s.state.CleanupClaims, claimKey)
				continue
			}
		}
		if !rootClaimExists {
			if _, err := s.root.Lstat(content.Entry); errors.Is(err, os.ErrNotExist) {
				delete(s.state.Contents, resource.ContentID)
				if s.state.Deduplication[localDeduplicationKey(content.Domain, content.Digest, content.Size)] == resource.ContentID {
					delete(s.state.Deduplication, localDeduplicationKey(content.Domain, content.Digest, content.Size))
				}
				delete(s.state.References, resource.ReferenceID)
				continue
			} else if err != nil {
				return CheckpointContentReclamationEvidence{}, ErrDurableObjectResultAmbiguous
			}
		}
		claimedEntry, present, err := s.claimCleanupEntry(event, string(resource.ContentID),
			string(resource.GenerationID), content.Entry, content.PhysicalIdentity,
			localCleanupContentGeneration)
		if err != nil {
			return CheckpointContentReclamationEvidence{}, err
		}
		if !present {
			delete(s.state.Contents, resource.ContentID)
			if s.state.Deduplication[localDeduplicationKey(content.Domain, content.Digest, content.Size)] == resource.ContentID {
				delete(s.state.Deduplication, localDeduplicationKey(content.Domain, content.Digest, content.Size))
			}
			delete(s.state.References, resource.ReferenceID)
			delete(s.state.CleanupClaims, claimKey)
			continue
		}
		claimedContent := content
		claimedContent.Entry = claimedEntry
		if err := s.verifyContentRecord(request.Operation.ID, ordinal, claimedContent); err != nil {
			if restoreErr := s.restoreCleanupClaim(
				event, string(resource.ContentID), content.Entry, claimedEntry,
			); restoreErr != nil {
				return CheckpointContentReclamationEvidence{}, ErrDurableObjectResultAmbiguous
			}
			return s.retainedCheckpointEvidence(request, CheckpointMechanicsClear,
				CheckpointMechanicsClear, CheckpointMechanicsBlocked, CheckpointInventoryPresent)
		}
		deleteErr := s.deleteClaimedCleanupEntry(
			event, string(resource.ContentID), string(resource.GenerationID), content.Entry,
			claimedEntry, content.PhysicalIdentity, localCleanupContentGeneration, false,
			revalidate,
		)
		if errors.Is(deleteErr, errLocalCleanupReferenceRetained) ||
			errors.Is(deleteErr, errLocalCleanupLeaseRetained) {
			if err := s.restoreCleanupClaim(event, string(resource.ContentID), content.Entry, claimedEntry); err != nil {
				return CheckpointContentReclamationEvidence{}, ErrDurableObjectResultAmbiguous
			}
			if errors.Is(deleteErr, errLocalCleanupReferenceRetained) {
				return s.retainedCheckpointEvidence(request, CheckpointMechanicsBlocked,
					CheckpointMechanicsClear, CheckpointMechanicsClear, CheckpointInventoryPresent)
			}
			return s.retainedCheckpointEvidence(request, CheckpointMechanicsClear,
				CheckpointMechanicsBlocked, CheckpointMechanicsClear, CheckpointInventoryPresent)
		}
		if deleteErr != nil {
			return CheckpointContentReclamationEvidence{}, deleteErr
		}
		anyPresent = true
		if err := s.syncDirectory(localStagingDirectory, event); err != nil {
			return CheckpointContentReclamationEvidence{}, ErrDurableObjectResultAmbiguous
		}
		delete(s.state.Contents, resource.ContentID)
		if s.state.Deduplication[localDeduplicationKey(content.Domain, content.Digest, content.Size)] == resource.ContentID {
			delete(s.state.Deduplication, localDeduplicationKey(content.Domain, content.Digest, content.Size))
		}
		delete(s.state.References, resource.ReferenceID)
		delete(s.state.CleanupClaims, localCleanupClaimKey(request.Operation.ID, string(resource.ContentID), content.Entry))
	}
	if err := s.syncDirectory(localObjectsDirectory, event); err != nil {
		return CheckpointContentReclamationEvidence{}, ErrDurableObjectResultAmbiguous
	}
	if err := s.inject(LocalFaultAfterCleanup, event); err != nil {
		return CheckpointContentReclamationEvidence{}, ErrDurableObjectResultAmbiguous
	}
	outcome := CheckpointAlreadyAbsent
	if anyPresent {
		outcome = CheckpointReclaimed
	}
	evidence := s.checkpointReclamationEvidence(request, CheckpointMechanicsClear,
		CheckpointMechanicsClear, CheckpointMechanicsClear, state, outcome)
	s.state.Reclamations[request.Operation.ID] = evidence
	if err := s.persistState(); err != nil {
		return CheckpointContentReclamationEvidence{}, ErrDurableObjectResultAmbiguous
	}
	return evidence, nil
}

func (s *localFilesystemStore) hasActiveMaterialization(contentID ContentID) bool {
	for _, materialization := range s.state.Materializations {
		for _, member := range materialization.Members {
			if member.ContentID == contentID {
				return true
			}
		}
	}
	return false
}

func (s *localFilesystemStore) retainedCheckpointEvidence(
	request ReclaimCheckpointContentRequest,
	references CheckpointMechanicsState,
	leases CheckpointMechanicsState,
	quarantine CheckpointMechanicsState,
	inventory CheckpointInventoryState,
) (CheckpointContentReclamationEvidence, error) {
	evidence := s.checkpointReclamationEvidence(request, references, leases, quarantine,
		inventory, CheckpointRetainedByAuthority)
	s.state.Reclamations[request.Operation.ID] = evidence
	if err := s.persistState(); err != nil {
		return CheckpointContentReclamationEvidence{}, ErrDurableObjectResultAmbiguous
	}
	return evidence, nil
}

func (s *localFilesystemStore) checkpointReclamationEvidence(
	request ReclaimCheckpointContentRequest,
	references CheckpointMechanicsState,
	leases CheckpointMechanicsState,
	quarantine CheckpointMechanicsState,
	inventory CheckpointInventoryState,
	outcome CheckpointReclamationOutcome,
) CheckpointContentReclamationEvidence {
	evidence := CheckpointContentReclamationEvidence{
		ID:             EvidenceID(localEvidenceID("checkpoint-reclamation", request.Operation.ID)),
		PolicyDomainID: request.PolicyDomainID, TaskID: request.TaskID,
		TaskWorkspaceID: request.TaskWorkspaceID, CheckpointID: request.CheckpointID,
		RetentionGeneration: request.RetentionGeneration,
		ExactGenerationRoot: request.ExactGenerationRoot,
		ReferenceState:      references, LeaseState: leases, QuarantineState: quarantine,
		InventoryState: inventory, Outcome: outcome, Generation: request.Generation,
		Fence: request.Fence, OperationID: request.Operation.ID, ObservedAt: s.now(),
	}
	evidence.Digest = evidence.CanonicalDigest()
	return evidence
}

func (s *localFilesystemStore) ObserveCheckpointInventory(
	_ context.Context,
	request ObserveCheckpointContentInventoryRequest,
) (CheckpointContentInventoryEvidence, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := CheckpointInventoryAbsent
	var resource InventoryResourceID
	var generation DurabilityGenerationID
	contentIDs := make([]ContentID, 0, len(s.state.Contents))
	for id := range s.state.Contents {
		contentIDs = append(contentIDs, id)
	}
	sort.Slice(contentIDs, func(i, j int) bool { return contentIDs[i] < contentIDs[j] })
	for _, id := range contentIDs {
		content := s.state.Contents[id]
		if content.Domain != request.PolicyDomainID {
			continue
		}
		state = CheckpointInventoryPresent
		resource = InventoryResourceID(content.ID)
		generation = content.Generation
		break
	}
	evidence := CheckpointContentInventoryEvidence{
		ID:             EvidenceID(localEvidenceID("inventory", request.Operation.ID)),
		PolicyDomainID: request.PolicyDomainID, TaskID: request.TaskID,
		TaskWorkspaceID: request.TaskWorkspaceID, ResourceID: resource,
		GenerationID: generation, State: state, OperationID: request.Operation.ID,
		ObservedAt: s.now(),
	}
	evidence.Digest = evidence.CanonicalDigest()
	return evidence, nil
}

func (s *localFilesystemStore) expireMaterialization(
	request ExpireMaterializationRequest,
) (*localFilesystemResidue, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	operationID, ok := s.state.MaterializationIDs[request.MaterializationID]
	if !ok {
		return nil, nil
	}
	record, ok := s.state.Materializations[operationID]
	if !ok || record.MaterializationID != request.MaterializationID ||
		record.PolicyDomainID != request.PolicyDomainID || record.TaskID != request.TaskID ||
		record.TaskWorkspaceID != request.TaskWorkspaceID || record.Generation != request.Generation ||
		record.Fence != request.Fence {
		return nil, &Error{Code: ErrorStaleAuthority}
	}
	cleanupResource := localCleanupResource{
		ID: string(request.MaterializationID), Generation: record.PhysicalGeneration,
		Entry: record.Entry, PhysicalIdentity: record.PhysicalIdentity,
		Kind: localCleanupMaterializationGeneration, Capacity: record.Capacity,
	}
	s.state.CleanupResources[cleanupResource.ID] = cleanupResource
	if err := s.persistState(); err != nil {
		return &localFilesystemResidue{id: cleanupResource.ID, generation: cleanupResource.Generation, capacity: cleanupResource.Capacity}, err
	}
	event := LocalFilesystemFaultEvent{OperationID: request.Operation.ID, SubjectID: string(request.MaterializationID)}
	if err := s.inject(LocalFaultBeforeCleanup, event); err != nil {
		return &localFilesystemResidue{id: cleanupResource.ID, generation: cleanupResource.Generation, capacity: cleanupResource.Capacity}, ErrCleanupResultAmbiguous
	}
	if err := s.verifyMaterialization(record); err != nil {
		return &localFilesystemResidue{id: cleanupResource.ID, generation: cleanupResource.Generation, capacity: cleanupResource.Capacity}, err
	}
	claimedEntry, present, claimErr := s.claimCleanupEntry(event, cleanupResource.ID,
		cleanupResource.Generation, cleanupResource.Entry, cleanupResource.PhysicalIdentity,
		cleanupResource.Kind)
	if claimErr != nil {
		return &localFilesystemResidue{id: cleanupResource.ID, generation: cleanupResource.Generation, capacity: cleanupResource.Capacity}, claimErr
	}
	if present {
		deleteErr := s.deleteClaimedCleanupEntry(
			event, cleanupResource.ID, cleanupResource.Generation, cleanupResource.Entry,
			claimedEntry, cleanupResource.PhysicalIdentity, cleanupResource.Kind, true,
			func() error {
				current, currentOK := s.state.Materializations[operationID]
				resource, resourceOK := s.state.CleanupResources[cleanupResource.ID]
				if !currentOK || !resourceOK || current.MaterializationID != request.MaterializationID ||
					current.PhysicalGeneration != cleanupResource.Generation ||
					current.PhysicalIdentity != cleanupResource.PhysicalIdentity ||
					resource.Generation != cleanupResource.Generation ||
					resource.PhysicalIdentity != cleanupResource.PhysicalIdentity {
					return &Error{Code: ErrorStaleAuthority}
				}
				return nil
			},
		)
		if deleteErr != nil {
			return &localFilesystemResidue{id: cleanupResource.ID, generation: cleanupResource.Generation, capacity: cleanupResource.Capacity}, deleteErr
		}
	}
	if err := s.syncDirectory(localStagingDirectory, event); err != nil || s.inject(LocalFaultAfterCleanup, event) != nil {
		return &localFilesystemResidue{id: cleanupResource.ID, generation: cleanupResource.Generation, capacity: cleanupResource.Capacity}, ErrCleanupResultAmbiguous
	}
	delete(s.state.Materializations, operationID)
	delete(s.state.MaterializationIDs, request.MaterializationID)
	delete(s.state.CleanupResources, cleanupResource.ID)
	delete(s.state.CleanupClaims, localCleanupClaimKey(request.Operation.ID, cleanupResource.ID, cleanupResource.Entry))
	if err := s.persistState(); err != nil {
		return &localFilesystemResidue{id: cleanupResource.ID, generation: cleanupResource.Generation, capacity: cleanupResource.Capacity}, ErrCleanupResultAmbiguous
	}
	return nil, nil
}

func (s *localFilesystemStore) makeMaterializationRemovable(record localMaterializationRecord) error {
	directories := map[string]localPhysicalIdentity{
		record.Entry:        record.PhysicalIdentity,
		record.PayloadEntry: record.PayloadIdentity,
	}
	for relative, identity := range record.DirectoryIdentities {
		if relative != "." {
			directories[record.PayloadEntry+"/"+relative] = identity
		}
	}
	names := make([]string, 0, len(directories))
	for name := range directories {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool { return len(names[i]) > len(names[j]) })
	for _, name := range names {
		info, err := s.root.Lstat(name)
		identity, identityOK := localIdentityFromFileInfo(info)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !identityOK ||
			!directories[name].Known || identity != directories[name] || s.root.Chmod(name, 0o700) != nil {
			return ErrLocalFilesystemUnavailable
		}
	}
	return nil
}

func (s *localFilesystemStore) InspectCleanup(
	_ context.Context,
	request CleanupInspectionRequest,
) (CleanupInspectionEvidence, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if replay, ok := s.state.CleanupInspections[request.Operation.ID]; ok {
		return replay, nil
	}
	event := LocalFilesystemFaultEvent{
		OperationID: request.Operation.ID, SubjectID: string(request.ResourceID),
	}
	if err := s.inject(LocalFaultBeforeCleanup, event); err != nil {
		return CleanupInspectionEvidence{}, ErrCleanupResultAmbiguous
	}
	resource, ok := s.state.CleanupResources[string(request.ResourceID)]
	disposition := CleanupInspectionAlreadyAbsent
	capacity := localKnownCapacity(0, 0)
	referenceState := CleanupAuthorityClear
	var blockers []CleanupBlocker
	if ok {
		if resource.Generation != string(request.ResourceGeneration) {
			return CleanupInspectionEvidence{}, &Error{Code: ErrorStaleAuthority}
		}
		if s.cleanupResourceRetainedByReference(resource) {
			disposition = CleanupInspectionRetainedByAuthority
			capacity = resource.Capacity
			referenceState = CleanupAuthorityBlocked
			blockers = []CleanupBlocker{CleanupReferenceBlocker}
		}
		for _, entry := range s.localCleanupEntries(resource) {
			present, err := s.verifyCleanupEntry(resource, entry)
			if err != nil {
				return CleanupInspectionEvidence{}, err
			}
			if present && disposition != CleanupInspectionRetainedByAuthority {
				disposition = CleanupInspectionEligible
				capacity = resource.Capacity
			}
		}
		pendingTreeClaim, err := s.verifyCleanupTreeClaims(resource.ID, resource.Generation)
		if err != nil {
			return CleanupInspectionEvidence{}, err
		}
		if pendingTreeClaim && disposition != CleanupInspectionRetainedByAuthority {
			disposition = CleanupInspectionEligible
			capacity = resource.Capacity
		}
	}
	evidence := CleanupInspectionEvidence{
		ID:             CleanupEvidenceID(localEvidenceID("cleanup-inspection", request.Operation.ID)),
		PolicyDomainID: request.PolicyDomainID, TaskID: request.TaskID,
		TaskWorkspaceID: request.TaskWorkspaceID, DebtID: request.DebtID,
		Owner: request.Owner, ResourceClass: request.ResourceClass,
		ResourceID: request.ResourceID, ResourceGeneration: request.ResourceGeneration,
		RetryGeneration: request.RetryGeneration, Generation: request.Generation,
		Fence: request.Fence, ReferenceState: referenceState,
		LeaseState: CleanupAuthorityClear, GraceState: CleanupAuthorityClear,
		IncidentState: CleanupAuthorityClear, QuarantineState: CleanupAuthorityClear,
		Disposition: disposition, Blockers: blockers, Capacity: capacity, ObservedAt: s.now(),
	}
	evidence.Digest = evidence.CanonicalDigest()
	s.state.CleanupInspections[request.Operation.ID] = evidence
	if disposition == CleanupInspectionRetainedByAuthority {
		if err := s.restoreCleanupResourceClaims(event, resource); err != nil {
			delete(s.state.CleanupInspections, request.Operation.ID)
			return CleanupInspectionEvidence{}, err
		}
		delete(s.state.CleanupResources, string(request.ResourceID))
	}
	if err := s.persistState(); err != nil {
		return CleanupInspectionEvidence{}, ErrCleanupResultAmbiguous
	}
	return evidence, nil
}

func (s *localFilesystemStore) ReclaimCleanup(
	_ context.Context,
	request CleanupAttemptRequest,
) (CleanupAttemptEvidence, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if replay, ok := s.state.CleanupAttempts[request.Operation.ID]; ok {
		return replay, nil
	}
	resource, ok := s.state.CleanupResources[string(request.ResourceID)]
	outcome := CleanupAlreadyAbsent
	capacity := localKnownCapacity(0, 0)
	event := LocalFilesystemFaultEvent{OperationID: request.Operation.ID, SubjectID: string(request.ResourceID)}
	if ok {
		if resource.Generation != string(request.ResourceGeneration) {
			return CleanupAttemptEvidence{}, &Error{Code: ErrorStaleAuthority}
		}
		if err := s.inject(LocalFaultBeforeCleanup, event); err != nil {
			return CleanupAttemptEvidence{}, ErrCleanupResultAmbiguous
		}
		current, stillPresent := s.state.CleanupResources[string(request.ResourceID)]
		if !stillPresent {
			ok = false
		} else if current.Generation != string(request.ResourceGeneration) || current.Entry != resource.Entry ||
			current.TemporaryEntry != resource.TemporaryEntry || current.PhysicalIdentity != resource.PhysicalIdentity {
			return CleanupAttemptEvidence{}, &Error{Code: ErrorStaleAuthority}
		}
	}
	if ok && s.cleanupResourceRetainedByReference(resource) {
		if err := s.restoreCleanupResourceClaims(event, resource); err != nil {
			return CleanupAttemptEvidence{}, err
		}
		outcome = CleanupRetainedByAuthority
		delete(s.state.CleanupResources, resource.ID)
		delete(s.state.Residues, resource.ID)
		ok = false
	}
	if ok {
		revalidate := func() error {
			current, currentOK := s.state.CleanupResources[resource.ID]
			if !currentOK || current.Generation != resource.Generation ||
				current.Entry != resource.Entry || current.TemporaryEntry != resource.TemporaryEntry ||
				current.PhysicalIdentity != resource.PhysicalIdentity {
				return &Error{Code: ErrorStaleAuthority}
			}
			if s.cleanupResourceRetainedByReference(resource) {
				return errLocalCleanupReferenceRetained
			}
			return nil
		}
		reconciled, reconcileErr := s.reconcileCleanupTreeClaims(
			event, resource.ID, resource.Generation, revalidate,
		)
		if errors.Is(reconcileErr, errLocalCleanupReferenceRetained) {
			if err := s.restoreCleanupResourceClaims(event, resource); err != nil {
				return CleanupAttemptEvidence{}, ErrCleanupResultAmbiguous
			}
		} else if reconcileErr != nil {
			return CleanupAttemptEvidence{}, reconcileErr
		}
		present := len(reconciled) != 0
		retainedAfterClaim := false
		if errors.Is(reconcileErr, errLocalCleanupReferenceRetained) {
			retainedAfterClaim = true
		}
		for _, entry := range s.localCleanupEntries(resource) {
			if retainedAfterClaim {
				break
			}
			entryPresent, err := s.verifyCleanupEntry(resource, entry)
			if err != nil {
				return CleanupAttemptEvidence{}, err
			}
			if !entryPresent {
				continue
			}
			present = true
			claimedEntry, claimed, claimErr := s.claimCleanupEntry(event, resource.ID,
				resource.Generation, entry, resource.PhysicalIdentity, resource.Kind)
			if claimErr != nil {
				return CleanupAttemptEvidence{}, claimErr
			}
			if !claimed {
				continue
			}
			deleteErr := s.deleteClaimedCleanupEntry(
				event, resource.ID, resource.Generation, entry, claimedEntry,
				resource.PhysicalIdentity, resource.Kind,
				resource.Kind == localCleanupMaterializationGeneration,
				revalidate,
			)
			if errors.Is(deleteErr, errLocalCleanupReferenceRetained) {
				if err := s.restoreCleanupClaim(event, resource.ID, entry, claimedEntry); err != nil {
					return CleanupAttemptEvidence{}, ErrCleanupResultAmbiguous
				}
				retainedAfterClaim = true
				break
			}
			if deleteErr != nil {
				return CleanupAttemptEvidence{}, deleteErr
			}
			if err := s.syncDirectory(localStagingDirectory, event); err != nil {
				return CleanupAttemptEvidence{}, ErrCleanupResultAmbiguous
			}
		}
		if retainedAfterClaim {
			outcome = CleanupRetainedByAuthority
		} else {
			if err := s.inject(LocalFaultAfterCleanup, event); err != nil {
				return CleanupAttemptEvidence{}, ErrCleanupResultAmbiguous
			}
			if present {
				outcome = CleanupReclaimed
				capacity = resource.Capacity
			}
			s.forgetCleanedMaterialization(resource)
			s.forgetCleanedContent(resource)
		}
		delete(s.state.CleanupResources, resource.ID)
		delete(s.state.Residues, resource.ID)
		for key, claim := range s.state.CleanupClaims {
			if claim.ResourceID == resource.ID && claim.Generation == resource.Generation {
				delete(s.state.CleanupClaims, key)
			}
		}
	}
	evidence := CleanupAttemptEvidence{
		ID:             CleanupEvidenceID(localEvidenceID("cleanup-attempt", request.Operation.ID)),
		PolicyDomainID: request.PolicyDomainID, TaskID: request.TaskID,
		TaskWorkspaceID: request.TaskWorkspaceID, DebtID: request.DebtID,
		Owner: request.Owner, ResourceClass: request.ResourceClass,
		ResourceID: request.ResourceID, ResourceGeneration: request.ResourceGeneration,
		RetryGeneration: request.RetryGeneration, Generation: request.Generation,
		Fence: request.Fence, InspectionEvidenceDigest: request.InspectionEvidenceDigest,
		Outcome: outcome, Capacity: capacity, ObservedAt: s.now(),
	}
	evidence.Digest = evidence.CanonicalDigest()
	s.state.CleanupAttempts[request.Operation.ID] = evidence
	if err := s.persistState(); err != nil {
		return CleanupAttemptEvidence{}, ErrCleanupResultAmbiguous
	}
	return evidence, nil
}

func (s *localFilesystemStore) cleanupResourceRetainedByReference(resource localCleanupResource) bool {
	contentID := resource.ContentID
	if contentID == "" {
		residue, ok := s.state.Residues[resource.ID]
		if ok && residue.PendingContent != nil {
			contentID = residue.PendingContent.ID
		}
	}
	if contentID == "" {
		return false
	}
	for _, reference := range s.state.References {
		if reference.Reference.ContentID == contentID && reference.Attached {
			return true
		}
	}
	return false
}

func (s *localFilesystemStore) forgetCleanedContent(resource localCleanupResource) {
	residue, ok := s.state.Residues[resource.ID]
	if !ok || residue.PendingContent == nil || residue.Generation != resource.Generation {
		return
	}
	content := *residue.PendingContent
	stored, ok := s.state.Contents[content.ID]
	if !ok || stored.Generation != content.Generation || stored.Entry != content.Entry ||
		stored.PhysicalIdentity != content.PhysicalIdentity {
		return
	}
	for _, reference := range s.state.References {
		if reference.Reference.ContentID == content.ID && reference.Attached {
			return
		}
	}
	delete(s.state.Contents, content.ID)
	key := localDeduplicationKey(content.Domain, content.Digest, content.Size)
	if s.state.Deduplication[key] == content.ID {
		delete(s.state.Deduplication, key)
	}
	for referenceID, reference := range s.state.References {
		if reference.Reference.ContentID == content.ID && !reference.Attached {
			delete(s.state.References, referenceID)
		}
	}
	delete(s.state.PreparedCheckpoints, residue.OperationID)
}

func (s *localFilesystemStore) localCleanupEntries(resource localCleanupResource) []string {
	entries := make([]string, 0, 2)
	seen := make(map[string]struct{})
	for _, entry := range []string{resource.Entry, resource.TemporaryEntry} {
		if entry == "" {
			continue
		}
		if _, duplicate := seen[entry]; duplicate {
			continue
		}
		seen[entry] = struct{}{}
		entries = append(entries, entry)
	}
	for _, claim := range s.state.CleanupClaims {
		if claim.ResourceID != resource.ID || claim.Generation != resource.Generation || claim.ClaimEntry == "" {
			continue
		}
		if _, duplicate := seen[claim.ClaimEntry]; duplicate {
			continue
		}
		seen[claim.ClaimEntry] = struct{}{}
		entries = append(entries, claim.ClaimEntry)
	}
	sort.Strings(entries)
	return entries
}

func (s *localFilesystemStore) verifyCleanupEntry(resource localCleanupResource, entry string) (bool, error) {
	info, err := s.root.Lstat(entry)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, ErrCleanupResultAmbiguous
	}
	identity, identityOK := localIdentityFromFileInfo(info)
	if info.Mode()&os.ModeSymlink != 0 || !identityOK || !resource.PhysicalIdentity.Known ||
		identity != resource.PhysicalIdentity ||
		resource.Kind == localCleanupContentGeneration && !info.Mode().IsRegular() ||
		resource.Kind == localCleanupMaterializationGeneration && !info.IsDir() {
		return false, &Error{Code: ErrorIntegrityFailure}
	}
	return true, nil
}

func (s *localFilesystemStore) verifyCleanupTreeClaims(
	resourceID string,
	generation string,
) (bool, error) {
	pending := false
	for _, claim := range s.state.CleanupTreeClaims {
		if claim.ResourceID != resourceID || claim.Generation != generation {
			continue
		}
		pending = true
		entryInfo, entryErr := s.root.Lstat(claim.Entry)
		claimInfo, claimErr := s.root.Lstat(claim.ClaimEntry)
		entryPresent := entryErr == nil
		claimPresent := claimErr == nil
		if entryErr != nil && !errors.Is(entryErr, os.ErrNotExist) ||
			claimErr != nil && !errors.Is(claimErr, os.ErrNotExist) {
			return false, ErrCleanupResultAmbiguous
		}
		if entryPresent && claimPresent {
			return false, &Error{Code: ErrorIntegrityFailure}
		}
		if entryPresent {
			if err := verifyCleanupTreeClaimInfo(claim, entryInfo); err != nil {
				return false, err
			}
		}
		if claimPresent {
			if err := verifyCleanupTreeClaimInfo(claim, claimInfo); err != nil {
				return false, err
			}
		}
	}
	return pending, nil
}

func (s *localFilesystemStore) claimCleanupEntry(
	event LocalFilesystemFaultEvent,
	resourceID string,
	generation string,
	entry string,
	physicalIdentity localPhysicalIdentity,
	kind localCleanupResourceKind,
) (string, bool, error) {
	key := localCleanupClaimKey(event.OperationID, resourceID, entry)
	claim, exists := s.state.CleanupClaims[key]
	if !exists {
		claimName, err := s.randomEntry("cleanup-claim")
		if err != nil {
			return "", false, ErrCleanupResultAmbiguous
		}
		claim = localCleanupClaim{
			OperationID: event.OperationID, ResourceID: resourceID, Generation: generation,
			Entry: entry, ClaimEntry: localStagingDirectory + "/" + claimName,
			PhysicalIdentity: physicalIdentity, Kind: kind,
		}
		s.state.CleanupClaims[key] = claim
		s.setMaterializationPublished(claim, false)
		if err := s.persistState(); err != nil {
			return "", false, ErrCleanupResultAmbiguous
		}
	} else if claim.OperationID != event.OperationID || claim.ResourceID != resourceID ||
		claim.Generation != generation || claim.Entry != entry ||
		claim.PhysicalIdentity != physicalIdentity || claim.Kind != kind {
		return "", false, &Error{Code: ErrorStaleAuthority}
	} else {
		s.setMaterializationPublished(claim, false)
	}

	originalInfo, originalErr := s.root.Lstat(claim.Entry)
	claimedInfo, claimedErr := s.root.Lstat(claim.ClaimEntry)
	originalPresent := originalErr == nil
	claimedPresent := claimedErr == nil
	if originalErr != nil && !errors.Is(originalErr, os.ErrNotExist) ||
		claimedErr != nil && !errors.Is(claimedErr, os.ErrNotExist) || originalPresent && claimedPresent {
		return "", false, ErrCleanupResultAmbiguous
	}
	if claimedPresent {
		if !localCleanupIdentityMatches(claim, claimedInfo) {
			return "", false, &Error{Code: ErrorIntegrityFailure}
		}
		if kind == localCleanupMaterializationGeneration && claimedInfo.Mode().Perm()&0o222 != 0 {
			if err := s.root.Chmod(claim.ClaimEntry, 0o500); err != nil ||
				s.syncDirectory(claim.ClaimEntry, LocalFilesystemFaultEvent{}) != nil {
				return "", false, ErrCleanupResultAmbiguous
			}
		}
		return claim.ClaimEntry, true, nil
	}
	if !originalPresent {
		return "", false, nil
	}
	if !localCleanupIdentityMatches(claim, originalInfo) {
		return "", false, &Error{Code: ErrorIntegrityFailure}
	}
	if err := s.inject(LocalFaultAfterCleanupVerify, event); err != nil {
		return "", false, ErrCleanupResultAmbiguous
	}
	if kind == localCleanupMaterializationGeneration {
		directory, err := s.root.Open(claim.Entry)
		if err != nil {
			return "", false, ErrCleanupResultAmbiguous
		}
		stat, statErr := directory.Stat()
		if statErr != nil || !os.SameFile(originalInfo, stat) {
			_ = directory.Close()
			return "", false, &Error{Code: ErrorIntegrityFailure}
		}
		if directory.Chmod(0o700) != nil || directory.Close() != nil {
			_ = directory.Close()
			return "", false, ErrCleanupResultAmbiguous
		}
	}
	if err := s.renameEntryNoReplace(claim.Entry, claim.ClaimEntry); err != nil {
		if kind == localCleanupMaterializationGeneration {
			if restoreErr := s.restoreClaimReadOnly(claim); restoreErr != nil {
				return "", false, ErrCleanupResultAmbiguous
			}
		}
		if errors.Is(err, os.ErrExist) {
			return "", false, &Error{Code: ErrorIntegrityFailure}
		}
		return "", false, ErrCleanupResultAmbiguous
	}
	if err := s.syncDirectory(path.Dir(claim.Entry), event); err != nil ||
		s.syncDirectory(path.Dir(claim.ClaimEntry), event) != nil {
		return "", false, ErrCleanupResultAmbiguous
	}
	claimedInfo, err := s.root.Lstat(claim.ClaimEntry)
	if err != nil {
		return "", false, ErrCleanupResultAmbiguous
	}
	if !localCleanupIdentityMatches(claim, claimedInfo) {
		if restoreErr := s.renameEntryNoReplace(claim.ClaimEntry, claim.Entry); restoreErr != nil {
			return "", false, ErrCleanupResultAmbiguous
		}
		if s.syncDirectory(path.Dir(claim.Entry), LocalFilesystemFaultEvent{}) != nil ||
			s.syncDirectory(path.Dir(claim.ClaimEntry), LocalFilesystemFaultEvent{}) != nil {
			return "", false, ErrCleanupResultAmbiguous
		}
		return "", false, &Error{Code: ErrorIntegrityFailure}
	}
	if kind == localCleanupMaterializationGeneration {
		if err := s.root.Chmod(claim.ClaimEntry, 0o500); err != nil ||
			s.syncDirectory(claim.ClaimEntry, LocalFilesystemFaultEvent{}) != nil {
			return "", false, ErrCleanupResultAmbiguous
		}
	}
	return claim.ClaimEntry, true, nil
}

func (s *localFilesystemStore) deleteClaimedCleanupEntry(
	event LocalFilesystemFaultEvent,
	resourceID string,
	generation string,
	originalEntry string,
	claimedEntry string,
	physicalIdentity localPhysicalIdentity,
	kind localCleanupResourceKind,
	recursive bool,
	revalidate func() error,
) error {
	if revalidate != nil {
		if err := revalidate(); err != nil {
			return err
		}
	}
	key := localCleanupClaimKey(event.OperationID, resourceID, originalEntry)
	claim, ok := s.state.CleanupClaims[key]
	if !ok || claim.OperationID != event.OperationID || claim.ResourceID != resourceID ||
		claim.Generation != generation || claim.Entry != originalEntry || claim.ClaimEntry != claimedEntry ||
		claim.PhysicalIdentity != physicalIdentity || claim.Kind != kind {
		return &Error{Code: ErrorStaleAuthority}
	}
	reconciled, err := s.reconcileCleanupTreeClaims(event, resourceID, generation, revalidate)
	if err != nil {
		return err
	}
	if _, rootAlreadyRemoved := reconciled[claimedEntry]; rootAlreadyRemoved {
		return nil
	}
	claimedInfo, err := s.root.Lstat(claimedEntry)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &Error{Code: ErrorIntegrityFailure}
		}
		return ErrCleanupResultAmbiguous
	}
	if !localCleanupIdentityMatches(claim, claimedInfo) {
		return &Error{Code: ErrorIntegrityFailure}
	}
	if kind == localCleanupMaterializationGeneration {
		if err := s.verifyCleanupMaterializationClaim(
			resourceID, generation, originalEntry, claimedEntry, true,
		); err != nil {
			return err
		}
	}
	tree, err := s.snapshotCleanupTree(claimedEntry)
	if err != nil {
		return err
	}
	claimedInfo, err = s.root.Lstat(claimedEntry)
	if err != nil || !localCleanupIdentityMatches(claim, claimedInfo) {
		return &Error{Code: ErrorIntegrityFailure}
	}
	if kind == localCleanupMaterializationGeneration {
		if err := s.verifyCleanupMaterializationClaim(
			resourceID, generation, originalEntry, claimedEntry, true,
		); err != nil {
			return err
		}
	}
	if recursive {
		if err := s.makeCleanupTreeRemovable(tree); err != nil {
			return err
		}
		if err := s.inject(LocalFaultAfterCleanupPrepare, event); err != nil {
			return ErrCleanupResultAmbiguous
		}
		claimedInfo, err = s.root.Lstat(claimedEntry)
		if err != nil || !localCleanupIdentityMatches(claim, claimedInfo) {
			return &Error{Code: ErrorIntegrityFailure}
		}
		if kind == localCleanupMaterializationGeneration {
			if err := s.verifyCleanupMaterializationClaim(
				resourceID, generation, originalEntry, claimedEntry, false,
			); err != nil {
				return err
			}
		}
		if revalidate != nil {
			if err := revalidate(); err != nil {
				return err
			}
		}
	}
	if err := s.inject(LocalFaultBeforeCleanupDelete, event); err != nil {
		return ErrCleanupResultAmbiguous
	}
	if revalidate != nil {
		if err := revalidate(); err != nil {
			return err
		}
	}
	claimedInfo, err = s.root.Lstat(claimedEntry)
	if err != nil || !localCleanupIdentityMatches(claim, claimedInfo) {
		return &Error{Code: ErrorIntegrityFailure}
	}
	if kind == localCleanupMaterializationGeneration {
		if err := s.verifyCleanupMaterializationClaim(
			resourceID, generation, originalEntry, claimedEntry, !recursive,
		); err != nil {
			return err
		}
	}
	return s.removeExactCleanupTree(event, resourceID, generation, claimedEntry, tree, revalidate)
}

type localCleanupTreeNode struct {
	identity  localPhysicalIdentity
	directory bool
}

func (s *localFilesystemStore) snapshotCleanupTree(
	entry string,
) (map[string]localCleanupTreeNode, error) {
	tree := make(map[string]localCleanupTreeNode)
	if err := s.snapshotCleanupTreeEntry(entry, tree); err != nil {
		return nil, err
	}
	return tree, nil
}

func (s *localFilesystemStore) snapshotCleanupTreeEntry(
	entry string,
	tree map[string]localCleanupTreeNode,
) error {
	info, err := s.root.Lstat(entry)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &Error{Code: ErrorIntegrityFailure}
		}
		return ErrCleanupResultAmbiguous
	}
	identity, identityOK := localIdentityFromFileInfo(info)
	if info.Mode()&os.ModeSymlink != 0 || !identityOK || !identity.Known ||
		!info.IsDir() && !info.Mode().IsRegular() {
		return &Error{Code: ErrorIntegrityFailure}
	}
	if _, duplicate := tree[entry]; duplicate {
		return &Error{Code: ErrorIntegrityFailure}
	}
	tree[entry] = localCleanupTreeNode{identity: identity, directory: info.IsDir()}
	if !info.IsDir() {
		return nil
	}
	directory, err := s.root.Open(entry)
	if err != nil {
		return ErrCleanupResultAmbiguous
	}
	stat, statErr := directory.Stat()
	entries, readErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	if statErr != nil || !stat.IsDir() || !os.SameFile(info, stat) {
		return &Error{Code: ErrorIntegrityFailure}
	}
	if readErr != nil || closeErr != nil {
		return ErrCleanupResultAmbiguous
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, candidate := range entries {
		child, childErr := cleanupTreeEntry(entry, candidate.Name())
		if childErr != nil {
			return childErr
		}
		if err := s.snapshotCleanupTreeEntry(child, tree); err != nil {
			return err
		}
	}
	return nil
}

func (s *localFilesystemStore) makeCleanupTreeRemovable(
	tree map[string]localCleanupTreeNode,
) error {
	directories := make([]string, 0, len(tree))
	for entry, node := range tree {
		if node.directory {
			directories = append(directories, entry)
		}
	}
	sort.Slice(directories, func(i, j int) bool {
		if depthI, depthJ := strings.Count(directories[i], "/"), strings.Count(directories[j], "/"); depthI != depthJ {
			return depthI > depthJ
		}
		return directories[i] < directories[j]
	})
	for _, entry := range directories {
		node := tree[entry]
		directory, err := s.root.Open(entry)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return &Error{Code: ErrorIntegrityFailure}
			}
			return ErrCleanupResultAmbiguous
		}
		stat, statErr := directory.Stat()
		identity, identityOK := localIdentityFromFileInfo(stat)
		if statErr != nil || !stat.IsDir() || stat.Mode()&os.ModeSymlink != 0 ||
			!identityOK || identity != node.identity {
			_ = directory.Close()
			return &Error{Code: ErrorIntegrityFailure}
		}
		chmodErr := directory.Chmod(0o700)
		closeErr := directory.Close()
		if chmodErr != nil || closeErr != nil {
			return ErrCleanupResultAmbiguous
		}
		if err := s.verifyCleanupTreeNode(entry, node); err != nil {
			return err
		}
	}
	return nil
}

func (s *localFilesystemStore) removeExactCleanupTree(
	event LocalFilesystemFaultEvent,
	resourceID string,
	generation string,
	entry string,
	tree map[string]localCleanupTreeNode,
	revalidate func() error,
) error {
	ordinal := 0
	return s.removeExactCleanupTreeEntry(
		event, resourceID, generation, entry, tree, revalidate, &ordinal,
	)
}

func (s *localFilesystemStore) removeExactCleanupTreeEntry(
	event LocalFilesystemFaultEvent,
	resourceID string,
	generation string,
	entry string,
	tree map[string]localCleanupTreeNode,
	revalidate func() error,
	ordinal *int,
) error {
	node, ok := tree[entry]
	if !ok {
		return &Error{Code: ErrorIntegrityFailure}
	}
	if err := s.verifyCleanupTreeNode(entry, node); err != nil {
		return err
	}
	if node.directory {
		children, err := s.cleanupTreeChildren(entry, tree)
		if err != nil {
			return err
		}
		for _, child := range children {
			if err := s.removeExactCleanupTreeEntry(
				event, resourceID, generation, child, tree, revalidate, ordinal,
			); err != nil {
				return err
			}
		}
	}
	if revalidate != nil {
		if err := revalidate(); err != nil {
			return err
		}
	}
	if node.directory {
		empty, err := s.cleanupTreeDirectoryEmpty(entry, node)
		if err != nil {
			return err
		}
		if !empty {
			return &Error{Code: ErrorIntegrityFailure}
		}
	}
	claim, present, err := s.claimCleanupTreeEntry(
		event, resourceID, generation, entry, node,
	)
	if err != nil {
		return err
	}
	if !present {
		return &Error{Code: ErrorIntegrityFailure}
	}
	event.Ordinal = *ordinal
	*ordinal = *ordinal + 1
	return s.removeClaimedCleanupTreeEntry(event, claim, revalidate)
}

func (s *localFilesystemStore) claimCleanupTreeEntry(
	event LocalFilesystemFaultEvent,
	resourceID string,
	generation string,
	entry string,
	node localCleanupTreeNode,
) (localCleanupTreeClaim, bool, error) {
	key := localCleanupTreeClaimKey(resourceID, generation, entry)
	claim, exists := s.state.CleanupTreeClaims[key]
	if !exists {
		claimName, err := s.randomEntry("cleanup-entry-claim")
		if err != nil {
			return localCleanupTreeClaim{}, false, ErrCleanupResultAmbiguous
		}
		claim = localCleanupTreeClaim{
			ResourceID: resourceID, Generation: generation, Entry: entry,
			ClaimEntry:       localStagingDirectory + "/" + claimName,
			PhysicalIdentity: node.identity, Directory: node.directory,
		}
		s.state.CleanupTreeClaims[key] = claim
		if err := s.persistState(); err != nil {
			return localCleanupTreeClaim{}, false, ErrCleanupResultAmbiguous
		}
	} else if claim.ResourceID != resourceID || claim.Generation != generation ||
		claim.Entry != entry || claim.PhysicalIdentity != node.identity || claim.Directory != node.directory {
		return localCleanupTreeClaim{}, false, &Error{Code: ErrorStaleAuthority}
	}

	entryInfo, entryErr := s.root.Lstat(claim.Entry)
	claimInfo, claimErr := s.root.Lstat(claim.ClaimEntry)
	entryPresent := entryErr == nil
	claimPresent := claimErr == nil
	if entryErr != nil && !errors.Is(entryErr, os.ErrNotExist) ||
		claimErr != nil && !errors.Is(claimErr, os.ErrNotExist) {
		return localCleanupTreeClaim{}, false, ErrCleanupResultAmbiguous
	}
	if entryPresent && claimPresent {
		return localCleanupTreeClaim{}, false, &Error{Code: ErrorIntegrityFailure}
	}
	if claimPresent {
		if err := verifyCleanupTreeClaimInfo(claim, claimInfo); err != nil {
			return localCleanupTreeClaim{}, false, err
		}
		return claim, true, nil
	}
	if !entryPresent {
		return claim, false, nil
	}
	if err := verifyCleanupTreeClaimInfo(claim, entryInfo); err != nil {
		return localCleanupTreeClaim{}, false, err
	}
	if err := s.renameEntryNoReplace(claim.Entry, claim.ClaimEntry); err != nil {
		if errors.Is(err, os.ErrExist) {
			return localCleanupTreeClaim{}, false, &Error{Code: ErrorIntegrityFailure}
		}
		return localCleanupTreeClaim{}, false, ErrCleanupResultAmbiguous
	}
	if s.syncDirectory(path.Dir(claim.Entry), event) != nil ||
		s.syncDirectory(localStagingDirectory, event) != nil {
		return localCleanupTreeClaim{}, false, ErrCleanupResultAmbiguous
	}
	claimInfo, err := s.root.Lstat(claim.ClaimEntry)
	if err != nil {
		return localCleanupTreeClaim{}, false, ErrCleanupResultAmbiguous
	}
	if err := verifyCleanupTreeClaimInfo(claim, claimInfo); err != nil {
		if restoreErr := s.restoreCleanupTreeClaim(event, claim); restoreErr != nil {
			return localCleanupTreeClaim{}, false, err
		}
		return localCleanupTreeClaim{}, false, err
	}
	return claim, true, nil
}

func (s *localFilesystemStore) removeClaimedCleanupTreeEntry(
	event LocalFilesystemFaultEvent,
	claim localCleanupTreeClaim,
	revalidate func() error,
) error {
	if revalidate != nil {
		if err := revalidate(); err != nil {
			if restoreErr := s.restoreCleanupTreeClaim(event, claim); restoreErr != nil {
				return ErrCleanupResultAmbiguous
			}
			return err
		}
	}
	if err := s.inject(LocalFaultBeforeCleanupEntryRemove, event); err != nil {
		return ErrCleanupResultAmbiguous
	}
	if revalidate != nil {
		if err := revalidate(); err != nil {
			if restoreErr := s.restoreCleanupTreeClaim(event, claim); restoreErr != nil {
				return ErrCleanupResultAmbiguous
			}
			return err
		}
	}
	info, err := s.root.Lstat(claim.ClaimEntry)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &Error{Code: ErrorIntegrityFailure}
		}
		return ErrCleanupResultAmbiguous
	}
	if err := verifyCleanupTreeClaimInfo(claim, info); err != nil {
		return err
	}
	if claim.Directory {
		empty, err := s.cleanupTreeDirectoryEmpty(claim.ClaimEntry, localCleanupTreeNode{
			identity: claim.PhysicalIdentity, directory: true,
		})
		if err != nil {
			return err
		}
		if !empty {
			return &Error{Code: ErrorIntegrityFailure}
		}
	}
	if revalidate != nil {
		if err := revalidate(); err != nil {
			if restoreErr := s.restoreCleanupTreeClaim(event, claim); restoreErr != nil {
				return ErrCleanupResultAmbiguous
			}
			return err
		}
	}
	if err := s.root.Remove(claim.ClaimEntry); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &Error{Code: ErrorIntegrityFailure}
		}
		return ErrCleanupResultAmbiguous
	}
	if err := s.syncDirectory(localStagingDirectory, event); err != nil {
		return ErrCleanupResultAmbiguous
	}
	delete(s.state.CleanupTreeClaims, localCleanupTreeClaimKey(
		claim.ResourceID, claim.Generation, claim.Entry,
	))
	if err := s.persistState(); err != nil {
		return ErrCleanupResultAmbiguous
	}
	return nil
}

func (s *localFilesystemStore) restoreCleanupTreeClaim(
	event LocalFilesystemFaultEvent,
	claim localCleanupTreeClaim,
) error {
	info, err := s.root.Lstat(claim.ClaimEntry)
	if err != nil || verifyCleanupTreeClaimInfo(claim, info) != nil {
		return &Error{Code: ErrorIntegrityFailure}
	}
	if err := s.renameEntryNoReplace(claim.ClaimEntry, claim.Entry); err != nil {
		return err
	}
	if s.syncDirectory(path.Dir(claim.Entry), event) != nil ||
		s.syncDirectory(localStagingDirectory, event) != nil {
		return ErrCleanupResultAmbiguous
	}
	delete(s.state.CleanupTreeClaims, localCleanupTreeClaimKey(
		claim.ResourceID, claim.Generation, claim.Entry,
	))
	return s.persistState()
}

func (s *localFilesystemStore) reconcileCleanupTreeClaims(
	event LocalFilesystemFaultEvent,
	resourceID string,
	generation string,
	revalidate func() error,
) (map[string]struct{}, error) {
	keys := make([]string, 0)
	for key, claim := range s.state.CleanupTreeClaims {
		if claim.ResourceID == resourceID && claim.Generation == generation {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	reconciled := make(map[string]struct{}, len(keys))
	for ordinal, key := range keys {
		claim := s.state.CleanupTreeClaims[key]
		node := localCleanupTreeNode{identity: claim.PhysicalIdentity, directory: claim.Directory}
		claimed, present, err := s.claimCleanupTreeEntry(
			event, claim.ResourceID, claim.Generation, claim.Entry, node,
		)
		if err != nil {
			return nil, err
		}
		if !present {
			delete(s.state.CleanupTreeClaims, key)
			if err := s.persistState(); err != nil {
				return nil, ErrCleanupResultAmbiguous
			}
			reconciled[claim.Entry] = struct{}{}
			continue
		}
		event.Ordinal = ordinal
		if err := s.removeClaimedCleanupTreeEntry(event, claimed, revalidate); err != nil {
			return nil, err
		}
		reconciled[claim.Entry] = struct{}{}
	}
	return reconciled, nil
}

func verifyCleanupTreeClaimInfo(claim localCleanupTreeClaim, info os.FileInfo) error {
	identity, identityOK := localIdentityFromFileInfo(info)
	if info == nil || info.Mode()&os.ModeSymlink != 0 || !identityOK ||
		identity != claim.PhysicalIdentity || claim.Directory != info.IsDir() ||
		!claim.Directory && !info.Mode().IsRegular() {
		return &Error{Code: ErrorIntegrityFailure}
	}
	return nil
}

func localCleanupTreeClaimKey(resourceID string, generation string, entry string) string {
	return resourceID + "\x00" + generation + "\x00" + entry
}

func (s *localFilesystemStore) verifyCleanupTreeNode(
	entry string,
	node localCleanupTreeNode,
) error {
	info, err := s.root.Lstat(entry)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &Error{Code: ErrorIntegrityFailure}
		}
		return ErrCleanupResultAmbiguous
	}
	identity, identityOK := localIdentityFromFileInfo(info)
	if info.Mode()&os.ModeSymlink != 0 || !identityOK || identity != node.identity ||
		node.directory != info.IsDir() || !node.directory && !info.Mode().IsRegular() {
		return &Error{Code: ErrorIntegrityFailure}
	}
	return nil
}

func (s *localFilesystemStore) cleanupTreeChildren(
	entry string,
	tree map[string]localCleanupTreeNode,
) ([]string, error) {
	node := tree[entry]
	directory, err := s.root.Open(entry)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, &Error{Code: ErrorIntegrityFailure}
		}
		return nil, ErrCleanupResultAmbiguous
	}
	stat, statErr := directory.Stat()
	entries, readErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	identity, identityOK := localIdentityFromFileInfo(stat)
	if statErr != nil || !stat.IsDir() || !identityOK || identity != node.identity {
		return nil, &Error{Code: ErrorIntegrityFailure}
	}
	if readErr != nil || closeErr != nil {
		return nil, ErrCleanupResultAmbiguous
	}
	expectedChildren := make([]string, 0, len(entries))
	prefix := entry + "/"
	for candidate := range tree {
		relative := strings.TrimPrefix(candidate, prefix)
		if relative == candidate || strings.Contains(relative, "/") {
			continue
		}
		expectedChildren = append(expectedChildren, candidate)
	}
	sort.Strings(expectedChildren)
	observedChildren := make([]string, 0, len(entries))
	for _, candidate := range entries {
		child, childErr := cleanupTreeEntry(entry, candidate.Name())
		if childErr != nil {
			return nil, childErr
		}
		if _, expected := tree[child]; !expected {
			return nil, &Error{Code: ErrorIntegrityFailure}
		}
		observedChildren = append(observedChildren, child)
	}
	sort.Strings(observedChildren)
	if len(observedChildren) != len(expectedChildren) {
		return nil, &Error{Code: ErrorIntegrityFailure}
	}
	for index := range expectedChildren {
		if observedChildren[index] != expectedChildren[index] {
			return nil, &Error{Code: ErrorIntegrityFailure}
		}
	}
	return expectedChildren, nil
}

func (s *localFilesystemStore) cleanupTreeDirectoryEmpty(
	entry string,
	node localCleanupTreeNode,
) (bool, error) {
	info, err := s.root.Lstat(entry)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, &Error{Code: ErrorIntegrityFailure}
		}
		return false, ErrCleanupResultAmbiguous
	}
	identity, identityOK := localIdentityFromFileInfo(info)
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !identityOK || identity != node.identity {
		return false, &Error{Code: ErrorIntegrityFailure}
	}
	directory, err := s.root.Open(entry)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, &Error{Code: ErrorIntegrityFailure}
		}
		return false, ErrCleanupResultAmbiguous
	}
	stat, statErr := directory.Stat()
	entries, readErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	if statErr != nil || !stat.IsDir() || !os.SameFile(info, stat) {
		return false, &Error{Code: ErrorIntegrityFailure}
	}
	if readErr != nil {
		return false, ErrCleanupResultAmbiguous
	}
	if closeErr != nil {
		return false, ErrCleanupResultAmbiguous
	}
	return len(entries) == 0, nil
}

func cleanupTreeEntry(parent string, name string) (string, error) {
	if name == "" || name == "." || name == ".." || path.Base(name) != name || strings.Contains(name, "/") {
		return "", &Error{Code: ErrorIntegrityFailure}
	}
	entry := path.Join(parent, name)
	if entry == parent || !strings.HasPrefix(entry, parent+"/") {
		return "", &Error{Code: ErrorIntegrityFailure}
	}
	return entry, nil
}

func (s *localFilesystemStore) restoreCleanupClaim(
	event LocalFilesystemFaultEvent,
	resourceID string,
	originalEntry string,
	claimedEntry string,
) error {
	claim, ok := s.state.CleanupClaims[localCleanupClaimKey(event.OperationID, resourceID, originalEntry)]
	if !ok || claim.ClaimEntry != claimedEntry {
		return ErrCleanupResultAmbiguous
	}
	info, err := s.root.Lstat(claimedEntry)
	if err != nil || !localCleanupIdentityMatches(claim, info) {
		return ErrCleanupResultAmbiguous
	}
	if err := s.renameEntryNoReplace(claimedEntry, originalEntry); err != nil {
		return ErrCleanupResultAmbiguous
	}
	if s.syncDirectory(path.Dir(originalEntry), event) != nil ||
		s.syncDirectory(path.Dir(claimedEntry), event) != nil {
		return ErrCleanupResultAmbiguous
	}
	s.setMaterializationPublished(claim, true)
	delete(s.state.CleanupClaims, localCleanupClaimKey(event.OperationID, resourceID, originalEntry))
	if err := s.persistState(); err != nil {
		s.setMaterializationPublished(claim, false)
		return ErrCleanupResultAmbiguous
	}
	return nil
}

func (s *localFilesystemStore) restoreCleanupResourceClaims(
	event LocalFilesystemFaultEvent,
	resource localCleanupResource,
) error {
	treeKeys := make([]string, 0)
	for key, claim := range s.state.CleanupTreeClaims {
		if claim.ResourceID == resource.ID && claim.Generation == resource.Generation {
			treeKeys = append(treeKeys, key)
		}
	}
	sort.Strings(treeKeys)
	for _, key := range treeKeys {
		claim := s.state.CleanupTreeClaims[key]
		if info, err := s.root.Lstat(claim.ClaimEntry); err == nil {
			if verifyCleanupTreeClaimInfo(claim, info) != nil {
				return &Error{Code: ErrorIntegrityFailure}
			}
			if err := s.restoreCleanupTreeClaim(event, claim); err != nil {
				return err
			}
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return ErrCleanupResultAmbiguous
		}
		if info, err := s.root.Lstat(claim.Entry); err == nil {
			if verifyCleanupTreeClaimInfo(claim, info) != nil {
				return &Error{Code: ErrorIntegrityFailure}
			}
			delete(s.state.CleanupTreeClaims, key)
			if err := s.persistState(); err != nil {
				return ErrCleanupResultAmbiguous
			}
			continue
		} else if errors.Is(err, os.ErrNotExist) {
			return &Error{Code: ErrorIntegrityFailure}
		} else {
			return ErrCleanupResultAmbiguous
		}
	}
	keys := make([]string, 0)
	for key, claim := range s.state.CleanupClaims {
		if claim.ResourceID == resource.ID && claim.Generation == resource.Generation {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		claim := s.state.CleanupClaims[key]
		originalInfo, originalErr := s.root.Lstat(claim.Entry)
		claimInfo, claimErr := s.root.Lstat(claim.ClaimEntry)
		originalPresent := originalErr == nil
		claimPresent := claimErr == nil
		if originalErr != nil && !errors.Is(originalErr, os.ErrNotExist) ||
			claimErr != nil && !errors.Is(claimErr, os.ErrNotExist) {
			return ErrCleanupResultAmbiguous
		}
		if originalPresent && claimPresent {
			return &Error{Code: ErrorIntegrityFailure}
		}
		if !originalPresent && !claimPresent {
			return &Error{Code: ErrorIntegrityFailure}
		}
		if originalPresent {
			if !localCleanupIdentityMatches(claim, originalInfo) {
				return &Error{Code: ErrorIntegrityFailure}
			}
			s.setMaterializationPublished(claim, true)
			delete(s.state.CleanupClaims, key)
			if err := s.persistState(); err != nil {
				return ErrCleanupResultAmbiguous
			}
			continue
		}
		if !localCleanupIdentityMatches(claim, claimInfo) {
			return &Error{Code: ErrorIntegrityFailure}
		}
		claimEvent := event
		claimEvent.OperationID = claim.OperationID
		if err := s.restoreCleanupClaim(
			claimEvent, resource.ID, claim.Entry, claim.ClaimEntry,
		); err != nil {
			return err
		}
	}
	return nil
}

func (s *localFilesystemStore) restoreCheckpointContentClaims(
	event LocalFilesystemFaultEvent,
	content localContentRecord,
) error {
	return s.restoreCleanupResourceClaims(event, localCleanupResource{
		ID: string(content.ID), Generation: string(content.Generation), Entry: content.Entry,
		PhysicalIdentity: content.PhysicalIdentity, Kind: localCleanupContentGeneration,
	})
}

func (s *localFilesystemStore) setMaterializationPublished(claim localCleanupClaim, published bool) {
	if claim.Kind != localCleanupMaterializationGeneration {
		return
	}
	for operationID, materialization := range s.state.Materializations {
		if materialization.Entry == claim.Entry && materialization.PhysicalGeneration == claim.Generation &&
			materialization.PhysicalIdentity == claim.PhysicalIdentity {
			materialization.Published = published
			s.state.Materializations[operationID] = materialization
		}
	}
}

func (s *localFilesystemStore) restoreClaimReadOnly(claim localCleanupClaim) error {
	for _, entry := range []string{claim.Entry, claim.ClaimEntry} {
		info, err := s.root.Lstat(entry)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !localCleanupIdentityMatches(claim, info) {
			continue
		}
		if err := s.root.Chmod(entry, 0o500); err != nil {
			return err
		}
		if err := s.syncDirectory(entry, LocalFilesystemFaultEvent{}); err != nil {
			return err
		}
		return nil
	}
	return &Error{Code: ErrorIntegrityFailure}
}

func localCleanupIdentityMatches(claim localCleanupClaim, info os.FileInfo) bool {
	identity, ok := localIdentityFromFileInfo(info)
	if !ok || !claim.PhysicalIdentity.Known || identity != claim.PhysicalIdentity {
		return false
	}
	return claim.Kind == localCleanupContentGeneration && info.Mode().IsRegular() ||
		claim.Kind == localCleanupMaterializationGeneration && info.IsDir()
}

func localCleanupClaimKey(operationID OperationID, resourceID string, entry string) string {
	return string(operationID) + "\x00" + resourceID + "\x00" + entry
}

func (s *localFilesystemStore) verifyCleanupMaterializationClaim(
	resourceID string,
	generation string,
	originalEntry string,
	claimedEntry string,
	requireReadOnly bool,
) error {
	materialization, ok := s.cleanupMaterializationRecord(resourceID, generation, originalEntry, claimedEntry)
	if !ok {
		return &Error{Code: ErrorIntegrityFailure}
	}
	if !localMaterializationRecordComplete(materialization) {
		return s.verifyPartialMaterialization(materialization)
	}
	if requireReadOnly {
		if err := s.verifyMaterialization(materialization); err == nil {
			return nil
		}
		if !s.cleanupMaterializationHasMissingEntry(materialization) {
			return &Error{Code: ErrorIntegrityFailure}
		}
		return s.verifyPartialMaterialization(materialization)
	}
	if err := s.verifyRemovableMaterialization(materialization); err == nil {
		return nil
	}
	if !s.cleanupMaterializationHasMissingEntry(materialization) {
		return &Error{Code: ErrorIntegrityFailure}
	}
	return s.verifyPartialMaterialization(materialization)
}

func (s *localFilesystemStore) cleanupMaterializationHasMissingEntry(
	record localMaterializationRecord,
) bool {
	entries := []string{
		record.Entry,
		record.PayloadEntry,
		record.Entry + "/" + localMaterializationCapacity,
	}
	for relative := range record.DirectoryIdentities {
		if relative != "." {
			entries = append(entries, record.PayloadEntry+"/"+relative)
		}
	}
	for _, member := range record.Members {
		entries = append(entries, record.PayloadEntry+"/"+string(member.LogicalMember))
	}
	for _, entry := range entries {
		if _, err := s.root.Lstat(entry); errors.Is(err, os.ErrNotExist) {
			return true
		}
	}
	return false
}

func (s *localFilesystemStore) cleanupMaterializationRecord(
	resourceID string,
	generation string,
	originalEntry string,
	claimedEntry string,
) (localMaterializationRecord, bool) {
	for _, materialization := range s.state.Materializations {
		if materialization.PhysicalGeneration == generation &&
			s.cleanupEntryDescendsFromClaim(resourceID, generation, materialization.Entry, originalEntry) {
			materialization.Entry = claimedEntry
			materialization.PayloadEntry = claimedEntry + "/" + localMaterializationPayload
			return materialization, true
		}
	}
	if residue, ok := s.state.Residues[resourceID]; ok && residue.PendingMaterialization != nil &&
		(s.cleanupEntryDescendsFromClaim(resourceID, generation, residue.Entry, originalEntry) ||
			s.cleanupEntryDescendsFromClaim(resourceID, generation, residue.TemporaryEntry, originalEntry)) &&
		residue.PendingMaterialization.PhysicalGeneration == generation {
		materialization := *residue.PendingMaterialization
		materialization.Entry = claimedEntry
		materialization.PayloadEntry = claimedEntry + "/" + localMaterializationPayload
		return materialization, true
	}
	return localMaterializationRecord{}, false
}

func (s *localFilesystemStore) cleanupEntryDescendsFromClaim(
	resourceID string,
	generation string,
	baseEntry string,
	candidateEntry string,
) bool {
	if baseEntry == "" {
		return false
	}
	reachable := map[string]struct{}{baseEntry: {}}
	for changed := true; changed; {
		changed = false
		for _, claim := range s.state.CleanupClaims {
			if claim.ResourceID != resourceID || claim.Generation != generation {
				continue
			}
			if _, ok := reachable[claim.Entry]; !ok {
				continue
			}
			if _, ok := reachable[claim.ClaimEntry]; ok {
				continue
			}
			reachable[claim.ClaimEntry] = struct{}{}
			changed = true
		}
	}
	_, ok := reachable[candidateEntry]
	return ok
}

func localMaterializationRecordComplete(record localMaterializationRecord) bool {
	if !record.PhysicalIdentity.Known || !record.PayloadIdentity.Known || !record.CapacityIdentity.Known {
		return false
	}
	expectedDirectories := map[string]struct{}{".": {}}
	for _, member := range record.Members {
		for parent := path.Dir(string(member.LogicalMember)); parent != "."; parent = path.Dir(parent) {
			expectedDirectories[parent] = struct{}{}
		}
	}
	if len(record.DirectoryIdentities) != len(expectedDirectories) {
		return false
	}
	for directory := range expectedDirectories {
		if !record.DirectoryIdentities[directory].Known {
			return false
		}
	}
	return true
}

func (s *localFilesystemStore) verifyPartialMaterialization(record localMaterializationRecord) error {
	wrapperInfo, err := s.root.Lstat(record.Entry)
	wrapperIdentity, wrapperIdentityOK := localIdentityFromFileInfo(wrapperInfo)
	if err != nil || !wrapperInfo.IsDir() || wrapperInfo.Mode()&os.ModeSymlink != 0 ||
		!wrapperIdentityOK || !record.PhysicalIdentity.Known || wrapperIdentity != record.PhysicalIdentity {
		return &Error{Code: ErrorIntegrityFailure}
	}
	wrapper, err := s.root.Open(record.Entry)
	if err != nil {
		return &Error{Code: ErrorIntegrityFailure}
	}
	wrapperStat, statErr := wrapper.Stat()
	wrapperEntries, readErr := wrapper.ReadDir(-1)
	closeErr := wrapper.Close()
	if statErr != nil || !os.SameFile(wrapperInfo, wrapperStat) || readErr != nil || closeErr != nil {
		return &Error{Code: ErrorIntegrityFailure}
	}
	for _, entry := range wrapperEntries {
		switch entry.Name() {
		case localMaterializationPayload:
			if !entry.IsDir() || s.verifyPartialMaterializationPayload(record) != nil {
				return &Error{Code: ErrorIntegrityFailure}
			}
		case localMaterializationCapacity:
			info, err := s.root.Lstat(record.Entry + "/" + localMaterializationCapacity)
			identity, identityOK := localIdentityFromFileInfo(info)
			if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
				record.CapacityIdentity.Known && (!identityOK || identity != record.CapacityIdentity) ||
				record.Capacity.Bytes.Known && uint64(info.Size()) > record.Capacity.Bytes.Value {
				return &Error{Code: ErrorIntegrityFailure}
			}
		default:
			return &Error{Code: ErrorIntegrityFailure}
		}
	}
	return nil
}

func (s *localFilesystemStore) verifyPartialMaterializationPayload(record localMaterializationRecord) error {
	payloadInfo, err := s.root.Lstat(record.PayloadEntry)
	payloadIdentity, payloadIdentityOK := localIdentityFromFileInfo(payloadInfo)
	if err != nil || !payloadInfo.IsDir() || payloadInfo.Mode()&os.ModeSymlink != 0 ||
		record.PayloadIdentity.Known && (!payloadIdentityOK || payloadIdentity != record.PayloadIdentity) {
		return &Error{Code: ErrorIntegrityFailure}
	}
	expectedFiles := make(map[string]localMaterializedMember, len(record.Members))
	expectedDirectories := map[string]struct{}{".": {}}
	for _, member := range record.Members {
		expectedFiles[string(member.LogicalMember)] = member
		for parent := path.Dir(string(member.LogicalMember)); parent != "."; parent = path.Dir(parent) {
			expectedDirectories[parent] = struct{}{}
		}
	}
	return s.verifyPartialMaterializationDirectory(
		record, ".", expectedFiles, expectedDirectories,
	)
}

func (s *localFilesystemStore) verifyPartialMaterializationDirectory(
	record localMaterializationRecord,
	relative string,
	expectedFiles map[string]localMaterializedMember,
	expectedDirectories map[string]struct{},
) error {
	entryName := record.PayloadEntry
	if relative != "." {
		entryName += "/" + relative
	}
	info, err := s.root.Lstat(entryName)
	identity, identityOK := localIdentityFromFileInfo(info)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return &Error{Code: ErrorIntegrityFailure}
	}
	if expectedIdentity := record.DirectoryIdentities[relative]; expectedIdentity.Known &&
		(!identityOK || identity != expectedIdentity) {
		return &Error{Code: ErrorIntegrityFailure}
	}
	directory, err := s.root.Open(entryName)
	if err != nil {
		return &Error{Code: ErrorIntegrityFailure}
	}
	stat, statErr := directory.Stat()
	entries, readErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	if statErr != nil || !os.SameFile(info, stat) || readErr != nil || closeErr != nil {
		return &Error{Code: ErrorIntegrityFailure}
	}
	for _, entry := range entries {
		candidate := entry.Name()
		if relative != "." {
			candidate = path.Join(relative, candidate)
		}
		candidateInfo, err := s.root.Lstat(record.PayloadEntry + "/" + candidate)
		if err != nil || candidateInfo.Mode()&os.ModeSymlink != 0 {
			return &Error{Code: ErrorIntegrityFailure}
		}
		if candidateInfo.IsDir() {
			if _, ok := expectedDirectories[candidate]; !ok {
				return &Error{Code: ErrorIntegrityFailure}
			}
			if err := s.verifyPartialMaterializationDirectory(
				record, candidate, expectedFiles, expectedDirectories,
			); err != nil {
				return err
			}
			continue
		}
		member, ok := expectedFiles[candidate]
		content, contentOK := s.state.Contents[member.ContentID]
		contentInfo, contentErr := s.root.Lstat(content.Entry)
		if !ok || !contentOK || contentErr != nil || !candidateInfo.Mode().IsRegular() ||
			!os.SameFile(candidateInfo, contentInfo) {
			return &Error{Code: ErrorIntegrityFailure}
		}
	}
	return nil
}

func (s *localFilesystemStore) forgetCleanedMaterialization(resource localCleanupResource) {
	for operationID, materialization := range s.state.Materializations {
		if materialization.Entry != resource.Entry || materialization.PhysicalGeneration != resource.Generation {
			continue
		}
		delete(s.state.Materializations, operationID)
		if materialization.MaterializationID != "" {
			delete(s.state.MaterializationIDs, materialization.MaterializationID)
		}
	}
}

func localEvidenceID(kind string, operationID OperationID) string {
	return kind + "-" + string(canonicalDigest(struct {
		Kind        string
		OperationID OperationID
	}{kind, operationID}))[7:23]
}
