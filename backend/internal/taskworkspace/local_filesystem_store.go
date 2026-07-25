package taskworkspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path"
	"sort"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	localObjectsDirectory          = "objects"
	localStagingDirectory          = "staging"
	localMaterializationsDirectory = "materializations"
	localMaterializationPayload    = "payload"
	localMaterializationCapacity   = "capacity"
	localStateEntry                = "adapter-state.json"
)

type localFilesystemStoreConfig struct {
	root        *os.Root
	source      LocalContentSource
	capacity    LocalFilesystemCapacityProbe
	random      io.Reader
	fault       func(LocalFilesystemFaultEvent) error
	now         func() Instant
	authorityID DurabilityAuthorityID
	adapterID   DurabilityAdapterID
}

type localFilesystemStore struct {
	mu sync.Mutex
	localFilesystemStoreConfig
	state localFilesystemState
}

type localFilesystemState struct {
	Contents             map[ContentID]localContentRecord                             `json:"contents"`
	Deduplication        map[string]ContentID                                         `json:"deduplication"`
	References           map[ContentReferenceID]localReferenceRecord                  `json:"references"`
	PreparedCheckpoints  map[OperationID]localPreparedCheckpointRecord                `json:"prepared_checkpoints"`
	Materializations     map[OperationID]localMaterializationRecord                   `json:"materializations"`
	MaterializationIDs   map[MaterializationID]OperationID                            `json:"materialization_ids"`
	Residues             map[string]localResidueRecord                                `json:"residues"`
	ReferenceTransitions map[OperationID]CheckpointContentReferenceTransitionEvidence `json:"reference_transitions"`
	Reclamations         map[OperationID]CheckpointContentReclamationEvidence         `json:"reclamations"`
	CleanupResources     map[string]localCleanupResource                              `json:"cleanup_resources"`
	CleanupClaims        map[string]localCleanupClaim                                 `json:"cleanup_claims"`
	CleanupTreeClaims    map[string]localCleanupTreeClaim                             `json:"cleanup_tree_claims"`
	CleanupInspections   map[OperationID]CleanupInspectionEvidence                    `json:"cleanup_inspections"`
	CleanupAttempts      map[OperationID]CleanupAttemptEvidence                       `json:"cleanup_attempts"`
}

type localPreparedCheckpointRecord struct {
	RequestDigest Digest                    `json:"request_digest"`
	Content       VerifiedCheckpointContent `json:"content"`
	Activated     bool                      `json:"activated"`
}

type localContentRecord struct {
	ID               ContentID              `json:"id"`
	Domain           PolicyDomainID         `json:"domain"`
	Digest           Digest                 `json:"digest"`
	Size             uint64                 `json:"size"`
	Entry            string                 `json:"entry"`
	Generation       DurabilityGenerationID `json:"generation"`
	PhysicalIdentity localPhysicalIdentity  `json:"physical_identity"`
	Receipt          DurabilityReceipt      `json:"receipt"`
	Quarantined      bool                   `json:"quarantined"`
}

type localReferenceRecord struct {
	Reference ContentReference `json:"reference"`
	Attached  bool             `json:"attached"`
}

type localMaterializedMember struct {
	LogicalMember LogicalMember `json:"logical_member"`
	ContentID     ContentID     `json:"content_id"`
	Digest        Digest        `json:"digest"`
	Size          uint64        `json:"size"`
}

type localMaterializationRecord struct {
	OperationID         OperationID                      `json:"operation_id"`
	MaterializationID   MaterializationID                `json:"materialization_id"`
	PolicyDomainID      PolicyDomainID                   `json:"policy_domain_id"`
	TaskID              TaskID                           `json:"task_id"`
	TaskWorkspaceID     TaskWorkspaceID                  `json:"task_workspace_id"`
	Generation          Generation                       `json:"generation"`
	Fence               Fence                            `json:"fence"`
	Published           bool                             `json:"published"`
	PhysicalGeneration  string                           `json:"physical_generation"`
	PhysicalIdentity    localPhysicalIdentity            `json:"physical_identity"`
	Entry               string                           `json:"entry"`
	PayloadEntry        string                           `json:"payload_entry"`
	PayloadIdentity     localPhysicalIdentity            `json:"payload_identity"`
	DirectoryIdentities map[string]localPhysicalIdentity `json:"directory_identities"`
	CapacityIdentity    localPhysicalIdentity            `json:"capacity_identity"`
	Members             []localMaterializedMember        `json:"members"`
	Capacity            CleanupCapacity                  `json:"capacity"`
}

type localResidueRecord struct {
	ID                     string                      `json:"id"`
	Generation             string                      `json:"generation"`
	OperationID            OperationID                 `json:"operation_id"`
	Entry                  string                      `json:"entry"`
	TemporaryEntry         string                      `json:"temporary_entry"`
	PhysicalIdentity       localPhysicalIdentity       `json:"physical_identity"`
	Kind                   localCleanupResourceKind    `json:"kind"`
	Capacity               CleanupCapacity             `json:"capacity"`
	PendingContent         *localContentRecord         `json:"pending_content,omitempty"`
	PendingMaterialization *localMaterializationRecord `json:"pending_materialization,omitempty"`
}

type localCleanupResource struct {
	ID               string                   `json:"id"`
	Generation       string                   `json:"generation"`
	ContentID        ContentID                `json:"content_id,omitempty"`
	Entry            string                   `json:"entry"`
	TemporaryEntry   string                   `json:"temporary_entry,omitempty"`
	PhysicalIdentity localPhysicalIdentity    `json:"physical_identity"`
	Kind             localCleanupResourceKind `json:"kind"`
	Capacity         CleanupCapacity          `json:"capacity"`
	DebtRegistered   bool                     `json:"debt_registered"`
}

type localCleanupResourceKind string

const (
	localCleanupContentGeneration         localCleanupResourceKind = "content_generation"
	localCleanupMaterializationGeneration localCleanupResourceKind = "materialization_generation"
)

type localContentWriteReservation struct {
	residueID string
	temporary string
	identity  localPhysicalIdentity
}

type localCleanupClaim struct {
	OperationID      OperationID              `json:"operation_id"`
	ResourceID       string                   `json:"resource_id"`
	Generation       string                   `json:"generation"`
	Entry            string                   `json:"entry"`
	ClaimEntry       string                   `json:"claim_entry"`
	PhysicalIdentity localPhysicalIdentity    `json:"physical_identity"`
	Kind             localCleanupResourceKind `json:"kind"`
}

type localCleanupTreeClaim struct {
	ResourceID       string                `json:"resource_id"`
	Generation       string                `json:"generation"`
	Entry            string                `json:"entry"`
	ClaimEntry       string                `json:"claim_entry"`
	PhysicalIdentity localPhysicalIdentity `json:"physical_identity"`
	Directory        bool                  `json:"directory"`
}

type localPhysicalIdentity struct {
	Known  bool   `json:"known"`
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}

type localFilesystemResidue struct {
	id         string
	generation string
	capacity   CleanupCapacity
}

type localMaterializationAuthority struct {
	OperationID       OperationID
	MaterializationID MaterializationID
	PolicyDomainID    PolicyDomainID
	TaskID            TaskID
	TaskWorkspaceID   TaskWorkspaceID
	Generation        Generation
	Fence             Fence
	PhysicalRequired  bool
}

func newLocalFilesystemStore(config localFilesystemStoreConfig) (*localFilesystemStore, error) {
	store := &localFilesystemStore{localFilesystemStoreConfig: config}
	for _, directory := range []string{
		localObjectsDirectory,
		localStagingDirectory,
		localMaterializationsDirectory,
	} {
		if err := store.root.MkdirAll(directory, 0o700); err != nil {
			return nil, err
		}
		info, err := store.root.Lstat(directory)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, ErrLocalFilesystemUnavailable
		}
	}
	if err := store.loadState(); err != nil {
		return nil, err
	}
	store.initializeState()
	return store, nil
}

func (s *localFilesystemStore) initializeState() {
	if s.state.Contents == nil {
		s.state.Contents = make(map[ContentID]localContentRecord)
	}
	if s.state.Deduplication == nil {
		s.state.Deduplication = make(map[string]ContentID)
	}
	if s.state.References == nil {
		s.state.References = make(map[ContentReferenceID]localReferenceRecord)
	}
	if s.state.PreparedCheckpoints == nil {
		s.state.PreparedCheckpoints = make(map[OperationID]localPreparedCheckpointRecord)
	}
	if s.state.Materializations == nil {
		s.state.Materializations = make(map[OperationID]localMaterializationRecord)
	}
	if s.state.MaterializationIDs == nil {
		s.state.MaterializationIDs = make(map[MaterializationID]OperationID)
	}
	if s.state.Residues == nil {
		s.state.Residues = make(map[string]localResidueRecord)
	}
	if s.state.ReferenceTransitions == nil {
		s.state.ReferenceTransitions = make(map[OperationID]CheckpointContentReferenceTransitionEvidence)
	}
	if s.state.Reclamations == nil {
		s.state.Reclamations = make(map[OperationID]CheckpointContentReclamationEvidence)
	}
	if s.state.CleanupResources == nil {
		s.state.CleanupResources = make(map[string]localCleanupResource)
	}
	if s.state.CleanupClaims == nil {
		s.state.CleanupClaims = make(map[string]localCleanupClaim)
	}
	if s.state.CleanupTreeClaims == nil {
		s.state.CleanupTreeClaims = make(map[string]localCleanupTreeClaim)
	}
	if s.state.CleanupInspections == nil {
		s.state.CleanupInspections = make(map[OperationID]CleanupInspectionEvidence)
	}
	if s.state.CleanupAttempts == nil {
		s.state.CleanupAttempts = make(map[OperationID]CleanupAttemptEvidence)
	}
}

func (s *localFilesystemStore) loadState() error {
	info, err := s.root.Lstat(localStateEntry)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return ErrLocalFilesystemUnavailable
	}
	encoded, err := s.root.ReadFile(localStateEntry)
	if err != nil || json.Unmarshal(encoded, &s.state) != nil {
		return ErrLocalFilesystemUnavailable
	}
	return nil
}

func (s *localFilesystemStore) persistState() error {
	encoded, err := json.Marshal(s.state)
	if err != nil {
		return err
	}
	temporary, err := s.randomEntry("state-temporary")
	if err != nil {
		return err
	}
	file, err := s.root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		_ = file.Close()
		if !ok {
			_ = s.root.Remove(temporary)
		}
	}()
	if _, err := file.Write(encoded); err != nil || file.Sync() != nil || file.Close() != nil {
		return ErrLocalFilesystemUnavailable
	}
	if info, err := s.root.Lstat(localStateEntry); err == nil && !info.Mode().IsRegular() {
		return ErrLocalFilesystemUnavailable
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return ErrLocalFilesystemUnavailable
	}
	if err := s.root.Rename(temporary, localStateEntry); err != nil {
		return err
	}
	if err := s.syncDirectory("", LocalFilesystemFaultEvent{}); err != nil {
		return err
	}
	ok = true
	return nil
}

func (s *localFilesystemStore) PrepareCheckpoint(
	ctx context.Context,
	request PrepareCheckpointContentRequest,
) (result VerifiedCheckpointContent, returnErr error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if digestBytesLocal(request.CanonicalManifest) != request.Manifest.Digest {
		return VerifiedCheckpointContent{}, &Error{Code: ErrorIntegrityFailure}
	}
	if _, ok := validateDeclaredStateManifest(request.Manifest); !ok {
		return VerifiedCheckpointContent{}, &Error{Code: ErrorIntegrityFailure}
	}
	if prepared, ok := s.state.PreparedCheckpoints[request.Operation.ID]; ok {
		if prepared.RequestDigest != request.Operation.RequestDigest {
			return VerifiedCheckpointContent{}, &Error{Code: ErrorIntegrityFailure}
		}
		return s.replayPreparedCheckpoint(request, prepared.Content)
	}
	type plannedContent struct {
		member  DeclaredStateMember
		payload []byte
		record  localContentRecord
		new     bool
	}
	planned := make([]plannedContent, 0, len(request.Manifest.Members))
	missingBytes := uint64(0)
	missingInodes := uint64(0)
	for _, member := range request.Manifest.canonicalValue().Members {
		payload, err := s.readSource(ctx, member.ContentDigest, member.Size)
		if err != nil {
			return VerifiedCheckpointContent{}, err
		}
		record, exists, err := s.planContent(request.PolicyDomainID, member.ContentDigest, member.Size)
		if err != nil {
			return VerifiedCheckpointContent{}, &Error{Code: ErrorDurabilityUnverified}
		}
		if !exists {
			missingBytes = saturatingAdd(missingBytes, member.Size)
			missingInodes = saturatingAdd(missingInodes, 1)
		}
		planned = append(planned, plannedContent{member: member, payload: payload, record: record, new: !exists})
	}

	references := make([]ContentReference, len(planned))
	for index := range planned {
		referenceID, err := s.randomEntry("reference")
		if err != nil {
			return VerifiedCheckpointContent{}, &Error{Code: ErrorRetryableUnavailable}
		}
		references[index] = localContentReference(referenceID, CheckpointMemberReference,
			request, planned[index].member, planned[index].record)
	}
	manifest := checkpointManifestForLocal(request.Manifest, references)
	manifestBytes := manifest.CanonicalBytes()
	manifestRecord, manifestExists, err := s.planContent(request.PolicyDomainID, manifest.Digest, uint64(len(manifestBytes)))
	if err != nil {
		return VerifiedCheckpointContent{}, &Error{Code: ErrorDurabilityUnverified}
	}
	if !manifestExists {
		missingBytes = saturatingAdd(missingBytes, uint64(len(manifestBytes)))
		missingInodes = saturatingAdd(missingInodes, 1)
	}
	newContentRecords := make([]localContentRecord, 0, missingInodes)
	for _, content := range planned {
		if content.new {
			newContentRecords = append(newContentRecords, content.record)
		}
	}
	if !manifestExists {
		newContentRecords = append(newContentRecords, manifestRecord)
	}
	reservations, err := s.reserveContentWrites(request.Operation.ID, newContentRecords,
		missingBytes, missingInodes)
	if err != nil {
		return VerifiedCheckpointContent{}, err
	}

	for index := range planned {
		if err := reportLocalProgress(request, DurableContentPrepareBefore, index, string(planned[index].member.ID)); err != nil {
			return VerifiedCheckpointContent{}, err
		}
		if planned[index].new {
			record, err := s.writeImmutable(request.Operation.ID, index, planned[index].record,
				planned[index].payload, reservations[planned[index].record.ID])
			if err != nil {
				return VerifiedCheckpointContent{}, err
			}
			planned[index].record = record
		} else if err := s.verifyContentRecord(request.Operation.ID, index, planned[index].record); err != nil {
			return VerifiedCheckpointContent{}, err
		}
		s.state.References[references[index].ID] = localReferenceRecord{Reference: references[index]}
		if err := reportLocalProgress(request, DurableContentPrepareAfter, index, string(planned[index].record.ID)); err != nil {
			return VerifiedCheckpointContent{}, err
		}
	}
	manifestOrdinal := len(planned)
	if err := reportLocalProgress(request, DurableContentPrepareBefore, manifestOrdinal, "checkpoint-manifest"); err != nil {
		return VerifiedCheckpointContent{}, err
	}
	if !manifestExists {
		manifestRecord, err = s.writeImmutable(request.Operation.ID, manifestOrdinal, manifestRecord,
			manifestBytes, reservations[manifestRecord.ID])
		if err != nil {
			return VerifiedCheckpointContent{}, err
		}
	} else if err := s.verifyContentRecord(request.Operation.ID, manifestOrdinal, manifestRecord); err != nil {
		return VerifiedCheckpointContent{}, err
	}
	manifestReferenceID, err := s.randomEntry("reference")
	if err != nil {
		return VerifiedCheckpointContent{}, &Error{Code: ErrorRetryableUnavailable}
	}
	manifestReference := localContentReference(manifestReferenceID, CheckpointManifestReference,
		request, DeclaredStateMember{}, manifestRecord)
	s.state.References[manifestReference.ID] = localReferenceRecord{Reference: manifestReference}
	if err := reportLocalProgress(request, DurableContentPrepareAfter, manifestOrdinal, string(manifestRecord.ID)); err != nil {
		return VerifiedCheckpointContent{}, err
	}
	receipts := make([]DurabilityReceipt, 0, len(planned)+1)
	receipts = append(receipts, manifestRecord.Receipt)
	for _, content := range planned {
		receipts = append(receipts, content.record.Receipt)
	}
	result = VerifiedCheckpointContent{
		Manifest:           manifest,
		ManifestReference:  manifestReference,
		ContentReferences:  references,
		DurabilityReceipts: uniqueLocalReceipts(receipts),
	}
	s.state.PreparedCheckpoints[request.Operation.ID] = localPreparedCheckpointRecord{
		RequestDigest: request.Operation.RequestDigest,
		Content:       cloneVerifiedCheckpointContent(result),
	}
	if err := s.persistState(); err != nil {
		return VerifiedCheckpointContent{}, ErrDurableObjectResultAmbiguous
	}
	return result, nil
}

func (s *localFilesystemStore) replayPreparedCheckpoint(
	request PrepareCheckpointContentRequest,
	prepared VerifiedCheckpointContent,
) (VerifiedCheckpointContent, error) {
	for ordinal, reference := range prepared.ContentReferences {
		if err := reportLocalProgress(request, DurableContentPrepareBefore, ordinal,
			string(reference.StateMemberID)); err != nil {
			return VerifiedCheckpointContent{}, err
		}
		record, ok := s.state.Contents[reference.ContentID]
		if !ok || record.Digest != reference.ContentDigest || record.Size != reference.Size ||
			record.Domain != request.PolicyDomainID {
			return VerifiedCheckpointContent{}, &Error{Code: ErrorIntegrityFailure}
		}
		if err := s.verifyContentRecord(request.Operation.ID, ordinal, record); err != nil {
			return VerifiedCheckpointContent{}, err
		}
		if err := reportLocalProgress(request, DurableContentPrepareAfter, ordinal,
			string(reference.ContentID)); err != nil {
			return VerifiedCheckpointContent{}, err
		}
	}
	manifestOrdinal := len(prepared.ContentReferences)
	if err := reportLocalProgress(request, DurableContentPrepareBefore, manifestOrdinal,
		"checkpoint-manifest"); err != nil {
		return VerifiedCheckpointContent{}, err
	}
	manifestRecord, ok := s.state.Contents[prepared.ManifestReference.ContentID]
	if !ok || manifestRecord.Digest != prepared.ManifestReference.ContentDigest ||
		manifestRecord.Size != prepared.ManifestReference.Size || manifestRecord.Domain != request.PolicyDomainID {
		return VerifiedCheckpointContent{}, &Error{Code: ErrorIntegrityFailure}
	}
	if err := s.verifyContentRecord(request.Operation.ID, manifestOrdinal, manifestRecord); err != nil {
		return VerifiedCheckpointContent{}, err
	}
	if err := reportLocalProgress(request, DurableContentPrepareAfter, manifestOrdinal,
		string(prepared.ManifestReference.ContentID)); err != nil {
		return VerifiedCheckpointContent{}, err
	}
	return cloneVerifiedCheckpointContent(prepared), nil
}

func (s *localFilesystemStore) VerifyCheckpoint(
	_ context.Context,
	request VerifyCheckpointContentRequest,
) (VerifiedCheckpointContent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if digestBytesLocal(request.CanonicalManifest) != request.Manifest.Digest ||
		request.Manifest.Digest != request.Manifest.CanonicalDigest() {
		return VerifiedCheckpointContent{}, &Error{Code: ErrorIntegrityFailure}
	}
	references := append([]ContentReference{request.Expected.ManifestReference}, request.Expected.ContentReferences...)
	receipts := make([]DurabilityReceipt, 0, len(references))
	for ordinal, reference := range references {
		storedReference, referenceOK := s.state.References[reference.ID]
		record, ok := s.state.Contents[reference.ContentID]
		if !referenceOK || !storedReference.Attached || storedReference.Reference != reference ||
			!ok || record.Domain != request.PolicyDomainID || record.Digest != reference.ContentDigest ||
			record.Size != reference.Size || record.Generation == "" || record.Quarantined {
			return VerifiedCheckpointContent{}, &Error{Code: ErrorIntegrityFailure}
		}
		if err := s.verifyContentRecord(request.Operation.ID, ordinal, record); err != nil {
			return VerifiedCheckpointContent{}, err
		}
		receipts = append(receipts, record.Receipt)
	}
	content := VerifiedCheckpointContent{
		Manifest:           cloneCheckpointManifest(request.Manifest),
		ManifestReference:  request.Expected.ManifestReference,
		ContentReferences:  append([]ContentReference(nil), request.Expected.ContentReferences...),
		DurabilityReceipts: uniqueLocalReceipts(receipts),
	}
	if err := s.ensureMaterialization(request, content); err != nil {
		return VerifiedCheckpointContent{}, err
	}
	return content, nil
}

func (s *localFilesystemStore) readSource(ctx context.Context, digest Digest, size uint64) ([]byte, error) {
	reader, err := s.source.OpenContent(ctx, digest)
	if err != nil || reader == nil {
		return nil, &Error{Code: ErrorIntegrityFailure}
	}
	defer reader.Close()
	payload, err := io.ReadAll(io.LimitReader(reader, int64(size)+1))
	if err != nil || uint64(len(payload)) != size || digestBytesLocal(payload) != digest {
		return nil, &Error{Code: ErrorIntegrityFailure}
	}
	return payload, nil
}

func (s *localFilesystemStore) planContent(
	domain PolicyDomainID,
	digest Digest,
	size uint64,
) (localContentRecord, bool, error) {
	key := localDeduplicationKey(domain, digest, size)
	if contentID, ok := s.state.Deduplication[key]; ok {
		record, found := s.state.Contents[contentID]
		if !found || record.Domain != domain || record.Digest != digest || record.Size != size {
			return localContentRecord{}, false, ErrLocalFilesystemUnavailable
		}
		for _, reference := range s.state.References {
			if reference.Reference.ContentID == contentID && reference.Attached {
				return record, true, nil
			}
		}
	}
	contentID, err := s.randomEntry("content")
	if err != nil {
		return localContentRecord{}, false, err
	}
	generation, err := s.randomEntry("generation")
	if err != nil {
		return localContentRecord{}, false, err
	}
	entry, err := s.randomEntry("object")
	if err != nil {
		return localContentRecord{}, false, err
	}
	record := localContentRecord{
		ID: ContentID(contentID), Domain: domain, Digest: digest, Size: size,
		Entry:      localObjectsDirectory + "/" + entry,
		Generation: DurabilityGenerationID(generation),
	}
	return record, false, nil
}

func (s *localFilesystemStore) writeImmutable(
	operationID OperationID,
	ordinal int,
	record localContentRecord,
	payload []byte,
	reservation localContentWriteReservation,
) (localContentRecord, error) {
	if reservation.residueID == "" || reservation.temporary == "" || !reservation.identity.Known {
		return localContentRecord{}, &Error{Code: ErrorResourceExhausted}
	}
	residue, ok := s.state.Residues[reservation.residueID]
	if !ok || residue.OperationID != operationID || residue.TemporaryEntry != reservation.temporary ||
		residue.PendingContent == nil || residue.PendingContent.ID != record.ID ||
		residue.PhysicalIdentity != reservation.identity {
		return localContentRecord{}, &Error{Code: ErrorIntegrityFailure}
	}
	record.PhysicalIdentity = reservation.identity
	event := LocalFilesystemFaultEvent{OperationID: operationID, SubjectID: string(record.ID), Ordinal: ordinal}
	if err := s.inject(LocalFaultBeforeWrite, event); err != nil {
		return localContentRecord{}, &Error{Code: ErrorDurabilityUnverified}
	}
	file, err := s.root.OpenFile(reservation.temporary, os.O_WRONLY, 0)
	if err != nil {
		return localContentRecord{}, &Error{Code: ErrorDurabilityUnverified}
	}
	info, statErr := file.Stat()
	identity, identityOK := localIdentityFromFileInfo(info)
	if statErr != nil || !info.Mode().IsRegular() || !identityOK || identity != reservation.identity {
		_ = file.Close()
		return localContentRecord{}, &Error{Code: ErrorDurabilityUnverified}
	}
	hash := sha256.New()
	written, writeErr := io.Copy(io.MultiWriter(file, hash), bytes.NewReader(payload))
	if writeErr != nil || uint64(written) != record.Size ||
		Digest("sha256:"+hex.EncodeToString(hash.Sum(nil))) != record.Digest {
		_ = file.Close()
		return localContentRecord{}, &Error{Code: ErrorDurabilityUnverified}
	}
	if err := s.inject(LocalFaultAfterWrite, event); err != nil {
		_ = file.Close()
		return localContentRecord{}, &Error{Code: ErrorDurabilityUnverified}
	}
	if err := file.Chmod(0o400); err != nil || s.inject(LocalFaultBeforeFileSync, event) != nil || file.Sync() != nil {
		_ = file.Close()
		return localContentRecord{}, &Error{Code: ErrorDurabilityUnverified}
	}
	if err := s.inject(LocalFaultAfterFileSync, event); err != nil || file.Close() != nil {
		return localContentRecord{}, &Error{Code: ErrorDurabilityUnverified}
	}
	if err := s.inject(LocalFaultBeforePromotion, event); err != nil {
		return localContentRecord{}, &Error{Code: ErrorDurabilityUnverified}
	}
	if _, err := s.root.Lstat(record.Entry); err == nil || !errors.Is(err, os.ErrNotExist) {
		return localContentRecord{}, &Error{Code: ErrorIntegrityFailure}
	}
	// Link is the no-replace atomic publication point. Removing the random
	// temporary name leaves one immutable inode at the promoted generation.
	if err := s.root.Link(reservation.temporary, record.Entry); err != nil {
		return localContentRecord{}, &Error{Code: ErrorDurabilityUnverified}
	}
	if err := s.root.Remove(reservation.temporary); err != nil {
		return localContentRecord{}, ErrDurableObjectResultAmbiguous
	}
	if err := s.syncDirectory(localObjectsDirectory, event); err != nil {
		return localContentRecord{}, ErrDurableObjectResultAmbiguous
	}
	if err := s.inject(LocalFaultAfterPromotion, event); err != nil {
		return localContentRecord{}, ErrDurableObjectResultAmbiguous
	}
	if err := s.verifyContentRecord(operationID, ordinal, record); err != nil {
		return localContentRecord{}, err
	}
	record.Receipt, err = s.newReceipt(record)
	if err != nil {
		return localContentRecord{}, ErrDurableObjectResultAmbiguous
	}
	s.state.Contents[record.ID] = record
	s.state.Deduplication[localDeduplicationKey(record.Domain, record.Digest, record.Size)] = record.ID
	if err := s.persistState(); err != nil {
		return localContentRecord{}, ErrDurableObjectResultAmbiguous
	}
	return record, nil
}

func (s *localFilesystemStore) verifyContentRecord(
	operationID OperationID,
	ordinal int,
	record localContentRecord,
) error {
	event := LocalFilesystemFaultEvent{OperationID: operationID, SubjectID: string(record.ID), Ordinal: ordinal}
	if err := s.inject(LocalFaultBeforeReadback, event); err != nil {
		return &Error{Code: ErrorDurabilityUnverified}
	}
	lstat, err := s.root.Lstat(record.Entry)
	identity, identityOK := localIdentityFromFileInfo(lstat)
	if err != nil || !lstat.Mode().IsRegular() || lstat.Mode()&os.ModeSymlink != 0 ||
		lstat.Mode().Perm()&0o222 != 0 || !identityOK || !record.PhysicalIdentity.Known ||
		identity != record.PhysicalIdentity {
		s.quarantine(record.ID)
		return &Error{Code: ErrorIntegrityFailure}
	}
	file, err := s.root.Open(record.Entry)
	if err != nil {
		s.quarantine(record.ID)
		return &Error{Code: ErrorIntegrityFailure}
	}
	stat, statErr := file.Stat()
	hash := sha256.New()
	read, readErr := io.Copy(hash, file)
	closeErr := file.Close()
	if statErr != nil || !stat.Mode().IsRegular() || !os.SameFile(lstat, stat) || readErr != nil || closeErr != nil ||
		uint64(read) != record.Size || Digest("sha256:"+hex.EncodeToString(hash.Sum(nil))) != record.Digest {
		s.quarantine(record.ID)
		return &Error{Code: ErrorIntegrityFailure}
	}
	if err := s.inject(LocalFaultAfterReadback, event); err != nil {
		return &Error{Code: ErrorDurabilityUnverified}
	}
	return nil
}

func (s *localFilesystemStore) quarantine(contentID ContentID) {
	if record, ok := s.state.Contents[contentID]; ok {
		record.Quarantined = true
		s.state.Contents[contentID] = record
		_ = s.persistState()
	}
}

func (s *localFilesystemStore) newReceipt(record localContentRecord) (DurabilityReceipt, error) {
	receiptID, err := s.randomEntry("receipt")
	if err != nil {
		return DurabilityReceipt{}, err
	}
	writeID, err := s.randomEntry("write")
	if err != nil {
		return DurabilityReceipt{}, err
	}
	receipt := DurabilityReceipt{
		ID: DurabilityReceiptID(receiptID), DurabilityAuthorityID: s.authorityID,
		DurableWriteID: DurableWriteID(writeID), DurabilityAdapterID: s.adapterID,
		PolicyDomainID: record.Domain, ContentID: record.ID, ContentDigest: record.Digest,
		Size: record.Size, DurabilityGenerationID: record.Generation,
		VerificationMethod: VerificationIndependentReadback,
		VerifiedAt:         time.Unix(0, int64(s.now())).UTC(), Decision: DurabilityVerified,
	}
	receipt.EvidenceDigest = receipt.CanonicalDigest()
	return receipt, nil
}

func (s *localFilesystemStore) reserveContentWrites(
	operationID OperationID,
	records []localContentRecord,
	bytesRequired uint64,
	inodesRequired uint64,
) (map[ContentID]localContentWriteReservation, error) {
	reservations := make(map[ContentID]localContentWriteReservation, len(records))
	if len(records) == 0 {
		return reservations, nil
	}
	if bytesRequired > math.MaxInt64 || inodesRequired != uint64(len(records)) {
		return nil, &Error{Code: ErrorResourceExhausted}
	}
	for _, record := range records {
		temporaryName, err := s.randomEntry("temporary")
		if err != nil {
			return nil, s.failContentWriteReservations(reservations, &Error{Code: ErrorRetryableUnavailable})
		}
		residueID, err := s.randomEntry("residue")
		if err != nil {
			return nil, s.failContentWriteReservations(reservations, &Error{Code: ErrorRetryableUnavailable})
		}
		reservation := localContentWriteReservation{
			residueID: residueID,
			temporary: localObjectsDirectory + "/" + temporaryName,
		}
		residue := localResidueRecord{
			ID: residueID, Generation: string(record.Generation), OperationID: operationID,
			Entry: record.Entry, TemporaryEntry: reservation.temporary,
			Kind: localCleanupContentGeneration, Capacity: localKnownCapacity(record.Size, 1),
			PendingContent: &record,
		}
		s.state.Residues[residue.ID] = residue
		s.registerCleanupResource(residue)
		reservations[record.ID] = reservation
	}
	allocationUnit, err := s.filesystemAllocationUnit()
	if err != nil {
		return nil, s.discardPlannedContentWriteReservations(reservations, &Error{Code: ErrorRetryableUnavailable})
	}
	encodedState, err := json.Marshal(s.state)
	if err != nil {
		return nil, s.discardPlannedContentWriteReservations(reservations, &Error{Code: ErrorRetryableUnavailable})
	}
	payloadAllocation := uint64(0)
	for _, record := range records {
		payloadAllocation = saturatingAdd(
			payloadAllocation, roundLocalAllocation(record.Size, allocationUnit),
		)
	}
	stateTemporaryBytes := saturatingAdd(
		roundLocalAllocation(uint64(len(encodedState)), allocationUnit), allocationUnit,
	)
	metadataEntries := saturatingAdd(saturatingMultiply(inodesRequired, 2), 2)
	metadataBytes := saturatingMultiply(metadataEntries, allocationUnit)
	admissionBytes := saturatingAdd(
		payloadAllocation, saturatingAdd(stateTemporaryBytes, metadataBytes),
	)
	admissionInodes := saturatingAdd(inodesRequired, 1)
	capacity, err := s.availableCapacity()
	if err != nil {
		return nil, s.discardPlannedContentWriteReservations(reservations, &Error{Code: ErrorRetryableUnavailable})
	}
	if capacity.AvailableBytes < admissionBytes || capacity.AvailableInodes < admissionInodes {
		return nil, s.discardPlannedContentWriteReservations(reservations, &Error{Code: ErrorResourceExhausted})
	}
	if err := s.inject(LocalFaultBeforeCapacityReserve, LocalFilesystemFaultEvent{
		OperationID: operationID, SubjectID: "content-generations",
	}); err != nil {
		if errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT) {
			return nil, s.discardPlannedContentWriteReservations(reservations, &Error{Code: ErrorResourceExhausted})
		}
		return nil, s.discardPlannedContentWriteReservations(reservations, &Error{Code: ErrorRetryableUnavailable})
	}
	if err := s.persistState(); err != nil {
		return nil, s.failContentWriteReservations(reservations, &Error{Code: ErrorRetryableUnavailable})
	}

	for _, record := range records {
		reservation := reservations[record.ID]
		residue := s.state.Residues[reservation.residueID]
		file, err := s.root.OpenFile(reservation.temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return nil, s.failContentWriteReservations(reservations, localReservationError(err))
		}
		info, statErr := file.Stat()
		identity, identityOK := localIdentityFromFileInfo(info)
		reserveErr := statErr
		if reserveErr == nil && (!identityOK || !info.Mode().IsRegular()) {
			reserveErr = ErrLocalFilesystemUnavailable
		}
		if reserveErr == nil {
			reserveErr = physicallyReserveFile(file, int64(record.Size))
		}
		if reserveErr == nil {
			reserveErr = file.Sync()
		}
		closeErr := file.Close()
		if reserveErr == nil {
			reserveErr = closeErr
		}
		reservation.identity = identity
		reservations[record.ID] = reservation
		if reserveErr != nil {
			return nil, s.failContentWriteReservations(reservations, localReservationError(reserveErr))
		}
		record.PhysicalIdentity = identity
		residue.PhysicalIdentity = identity
		residue.PendingContent = &record
		s.state.Residues[residue.ID] = residue
		resource := s.state.CleanupResources[residue.ID]
		resource.PhysicalIdentity = identity
		s.state.CleanupResources[residue.ID] = resource
		if err := s.persistState(); err != nil {
			return nil, s.failContentWriteReservations(reservations, &Error{Code: ErrorRetryableUnavailable})
		}
	}
	if err := s.syncDirectory(localObjectsDirectory, LocalFilesystemFaultEvent{}); err != nil {
		return nil, s.failContentWriteReservations(reservations, &Error{Code: ErrorRetryableUnavailable})
	}
	return reservations, nil
}

func (s *localFilesystemStore) discardPlannedContentWriteReservations(
	reservations map[ContentID]localContentWriteReservation,
	cause error,
) error {
	for _, reservation := range reservations {
		delete(s.state.Residues, reservation.residueID)
		delete(s.state.CleanupResources, reservation.residueID)
	}
	return cause
}

func localReservationError(err error) error {
	if errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT) {
		return &Error{Code: ErrorResourceExhausted}
	}
	return &Error{Code: ErrorRetryableUnavailable}
}

func (s *localFilesystemStore) failContentWriteReservations(
	reservations map[ContentID]localContentWriteReservation,
	cause error,
) error {
	cleanupFailed := false
	for contentID, reservation := range reservations {
		residue, ok := s.state.Residues[reservation.residueID]
		if ok {
			cleaned := false
			info, err := s.root.Lstat(reservation.temporary)
			if err == nil {
				identity, identityOK := localIdentityFromFileInfo(info)
				if reservation.identity.Known && identityOK && identity == reservation.identity {
					if err := s.root.Remove(reservation.temporary); err != nil {
						cleanupFailed = true
					} else {
						cleaned = true
					}
				} else {
					cleanupFailed = true
				}
			} else if errors.Is(err, os.ErrNotExist) {
				cleaned = true
			} else {
				cleanupFailed = true
			}
			if cleaned {
				delete(s.state.Residues, residue.ID)
				delete(s.state.CleanupResources, residue.ID)
			}
		}
		delete(reservations, contentID)
	}
	if s.syncDirectory(localObjectsDirectory, LocalFilesystemFaultEvent{}) != nil || s.persistState() != nil {
		cleanupFailed = true
	}
	if cleanupFailed {
		return &Error{Code: ErrorReconciliationRequired}
	}
	return cause
}

func (s *localFilesystemStore) availableCapacity() (LocalFilesystemCapacity, error) {
	if s.capacity != nil {
		return s.capacity()
	}
	directory, err := s.root.Open(".")
	if err != nil {
		return LocalFilesystemCapacity{}, err
	}
	defer directory.Close()
	var stats unix.Statfs_t
	if err := unix.Fstatfs(int(directory.Fd()), &stats); err != nil {
		return LocalFilesystemCapacity{}, err
	}
	return LocalFilesystemCapacity{
		AvailableBytes:  saturatingMultiply(uint64(stats.Bavail), uint64(stats.Bsize)),
		AvailableInodes: uint64(stats.Ffree),
	}, nil
}

func (s *localFilesystemStore) ensureMaterialization(
	request VerifyCheckpointContentRequest,
	content VerifiedCheckpointContent,
) error {
	if existing, ok := s.state.Materializations[request.Operation.ID]; ok {
		if !existing.Published || s.materializationHasCleanupClaim(existing) {
			return &Error{Code: ErrorStaleAuthority}
		}
		return s.verifyMaterialization(existing)
	}
	members := make([]localMaterializedMember, len(content.Manifest.Members))
	bytesRequired := uint64(0)
	directories := map[string]struct{}{".": {}}
	for index, member := range content.Manifest.canonicalValue().Members {
		if !safeLogicalMember(member.LogicalMember) || member.Type != StateMemberRegularFile {
			return &Error{Code: ErrorIntegrityFailure}
		}
		members[index] = localMaterializedMember{
			LogicalMember: member.LogicalMember, ContentID: member.ContentID,
			Digest: member.ContentDigest, Size: member.Size,
		}
		bytesRequired = saturatingAdd(bytesRequired, member.Size)
		for parent := path.Dir(string(member.LogicalMember)); parent != "."; parent = path.Dir(parent) {
			directories[parent] = struct{}{}
		}
	}
	physicalInodes := uint64(len(directories) + 2) // wrapper + payload directories + capacity file
	physicalGeneration, err := s.randomEntry("materialization-generation")
	if err != nil {
		return &Error{Code: ErrorRetryableUnavailable}
	}
	temporaryName, err := s.randomEntry("materialization-temporary")
	if err != nil {
		return &Error{Code: ErrorRetryableUnavailable}
	}
	stage := localStagingDirectory + "/" + temporaryName
	entry := localMaterializationsDirectory + "/" + physicalGeneration
	payloadEntry := entry + "/" + localMaterializationPayload
	record := localMaterializationRecord{
		OperationID: request.Operation.ID, PolicyDomainID: request.PolicyDomainID,
		TaskID: request.TaskID, TaskWorkspaceID: request.TaskWorkspaceID,
		Generation: request.Generation, Fence: request.Fence,
		PhysicalGeneration: physicalGeneration, Entry: entry, PayloadEntry: payloadEntry, Members: members,
		Capacity: localKnownCapacity(bytesRequired, physicalInodes),
	}
	residueID, err := s.randomEntry("residue")
	if err != nil {
		return &Error{Code: ErrorRetryableUnavailable}
	}
	residue := localResidueRecord{
		ID: residueID, Generation: physicalGeneration, OperationID: request.Operation.ID,
		Entry: entry, TemporaryEntry: stage, Kind: localCleanupMaterializationGeneration,
		Capacity: record.Capacity, PendingMaterialization: &record,
	}
	s.state.Residues[residue.ID] = residue
	s.registerCleanupResource(residue)
	reservationBytes, err := s.ensureMaterializationCapacity(
		request.Operation.ID, bytesRequired, physicalInodes, uint64(len(members)),
	)
	if err != nil {
		delete(s.state.Residues, residue.ID)
		delete(s.state.CleanupResources, residue.ID)
		return err
	}
	record.Capacity = localKnownCapacity(reservationBytes, physicalInodes)
	residue.Capacity = record.Capacity
	residue.PendingMaterialization = &record
	s.state.Residues[residue.ID] = residue
	cleanupResource := s.state.CleanupResources[residue.ID]
	cleanupResource.Capacity = record.Capacity
	s.state.CleanupResources[residue.ID] = cleanupResource
	if err := s.persistState(); err != nil {
		return &Error{Code: ErrorRetryableUnavailable}
	}
	if err := s.root.Mkdir(stage, 0o700); err != nil {
		return localPhysicalMutationError(err)
	}
	stageInfo, err := s.root.Lstat(stage)
	identity, identityOK := localIdentityFromFileInfo(stageInfo)
	if err != nil || !identityOK {
		return &Error{Code: ErrorDurabilityUnverified}
	}
	record.PhysicalIdentity = identity
	residue.PhysicalIdentity = identity
	residue.PendingMaterialization = &record
	s.state.Residues[residue.ID] = residue
	cleanupResource = s.state.CleanupResources[residue.ID]
	cleanupResource.PhysicalIdentity = identity
	s.state.CleanupResources[residue.ID] = cleanupResource
	if err := s.persistState(); err != nil {
		return &Error{Code: ErrorRetryableUnavailable}
	}
	stagePayload := stage + "/" + localMaterializationPayload
	if err := s.root.Mkdir(stagePayload, 0o700); err != nil {
		return localPhysicalMutationError(err)
	}
	payloadInfo, err := s.root.Lstat(stagePayload)
	payloadIdentity, payloadIdentityOK := localIdentityFromFileInfo(payloadInfo)
	if err != nil || !payloadIdentityOK || !payloadInfo.IsDir() || payloadInfo.Mode()&os.ModeSymlink != 0 {
		return &Error{Code: ErrorDurabilityUnverified}
	}
	record.PayloadIdentity = payloadIdentity
	residue.PendingMaterialization = &record
	s.state.Residues[residue.ID] = residue
	if err := s.persistState(); err != nil {
		return &Error{Code: ErrorRetryableUnavailable}
	}
	directoryNames := make([]string, 0, len(directories))
	for directory := range directories {
		directoryNames = append(directoryNames, directory)
	}
	sort.Slice(directoryNames, func(i, j int) bool {
		return len(directoryNames[i]) < len(directoryNames[j])
	})
	for _, directory := range directoryNames {
		if directory == "." {
			continue
		}
		if err := s.root.MkdirAll(stagePayload+"/"+directory, 0o700); err != nil {
			return localPhysicalMutationError(err)
		}
	}
	record.DirectoryIdentities = make(map[string]localPhysicalIdentity, len(directoryNames))
	for _, directory := range directoryNames {
		entryName := stagePayload
		if directory != "." {
			entryName += "/" + directory
		}
		info, err := s.root.Lstat(entryName)
		identity, identityOK := localIdentityFromFileInfo(info)
		if err != nil || !identityOK || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return &Error{Code: ErrorDurabilityUnverified}
		}
		record.DirectoryIdentities[directory] = identity
	}
	if err := s.inject(LocalFaultAfterMaterializationSkeleton, LocalFilesystemFaultEvent{
		OperationID: request.Operation.ID, SubjectID: physicalGeneration,
	}); err != nil {
		return localPhysicalMutationError(err)
	}
	capacityEntry := stage + "/" + localMaterializationCapacity
	capacityFile, err := s.root.OpenFile(capacityEntry, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return localPhysicalMutationError(err)
	}
	capacityErr := physicallyReserveFile(capacityFile, int64(reservationBytes))
	if capacityErr == nil {
		capacityErr = capacityFile.Chmod(0o400)
	}
	if capacityErr == nil {
		capacityErr = capacityFile.Sync()
	}
	closeErr := capacityFile.Close()
	if capacityErr == nil {
		capacityErr = closeErr
	}
	if capacityErr != nil {
		return localPhysicalMutationError(capacityErr)
	}
	capacityInfo, err := s.root.Lstat(capacityEntry)
	capacityIdentity, capacityIdentityOK := localIdentityFromFileInfo(capacityInfo)
	if err != nil || !capacityIdentityOK || !capacityInfo.Mode().IsRegular() ||
		capacityInfo.Mode()&os.ModeSymlink != 0 {
		return &Error{Code: ErrorDurabilityUnverified}
	}
	record.CapacityIdentity = capacityIdentity
	residue.PendingMaterialization = &record
	s.state.Residues[residue.ID] = residue
	if err := s.persistState(); err != nil {
		return &Error{Code: ErrorRetryableUnavailable}
	}
	for _, member := range members {
		contentRecord, ok := s.state.Contents[member.ContentID]
		if !ok || contentRecord.Digest != member.Digest || contentRecord.Size != member.Size || contentRecord.Quarantined {
			return &Error{Code: ErrorIntegrityFailure}
		}
		if err := s.root.Link(contentRecord.Entry, stagePayload+"/"+string(member.LogicalMember)); err != nil {
			return localPhysicalMutationError(err)
		}
	}
	sort.Slice(directoryNames, func(i, j int) bool {
		return len(directoryNames[i]) > len(directoryNames[j])
	})
	for _, directory := range directoryNames {
		entryName := stagePayload
		if directory != "." {
			entryName += "/" + directory
		}
		if err := s.root.Chmod(entryName, 0o500); err != nil ||
			s.syncDirectory(entryName, LocalFilesystemFaultEvent{}) != nil {
			return &Error{Code: ErrorDurabilityUnverified}
		}
	}
	event := LocalFilesystemFaultEvent{OperationID: request.Operation.ID, SubjectID: physicalGeneration}
	if err := s.inject(LocalFaultBeforePrivatePlacement, event); err != nil {
		return &Error{Code: ErrorDurabilityUnverified}
	}
	if _, err := s.root.Lstat(entry); err == nil || !errors.Is(err, os.ErrNotExist) {
		return &Error{Code: ErrorIntegrityFailure}
	}
	// The random directory is first placed at its stable private location. It
	// is not a published generation until the verified read-only record is
	// atomically installed in adapter state below.
	if err := s.renameEntryNoReplace(stage, entry); err != nil {
		if errors.Is(err, os.ErrExist) {
			return &Error{Code: ErrorIntegrityFailure}
		}
		if errors.Is(err, ErrDurableObjectResultAmbiguous) {
			return err
		}
		return &Error{Code: ErrorDurabilityUnverified}
	}
	if err := s.root.Chmod(entry, 0o500); err != nil ||
		s.syncDirectory(entry, event) != nil ||
		s.syncDirectory(localStagingDirectory, event) != nil ||
		s.syncDirectory(localMaterializationsDirectory, event) != nil {
		return &Error{Code: ErrorDurabilityUnverified}
	}
	if err := s.verifyMaterialization(record); err != nil {
		return err
	}
	if err := s.inject(LocalFaultBeforePromotion, event); err != nil {
		return &Error{Code: ErrorDurabilityUnverified}
	}
	if err := s.verifyMaterialization(record); err != nil {
		return err
	}
	record.Published = true
	s.state.Materializations[request.Operation.ID] = record
	delete(s.state.Residues, residue.ID)
	delete(s.state.CleanupResources, residue.ID)
	if err := s.persistState(); err != nil {
		return ErrDurableObjectResultAmbiguous
	}
	if err := s.inject(LocalFaultAfterPromotion, event); err != nil {
		return ErrDurableObjectResultAmbiguous
	}
	if err := s.verifyMaterialization(record); err != nil {
		return err
	}
	return nil
}

func (s *localFilesystemStore) ensureMaterializationCapacity(
	operationID OperationID,
	bytesRequired uint64,
	inodesRequired uint64,
	memberEntries uint64,
) (uint64, error) {
	allocationUnit, err := s.filesystemAllocationUnit()
	if err != nil {
		return 0, &Error{Code: ErrorRetryableUnavailable}
	}
	encodedState, err := json.Marshal(s.state)
	if err != nil {
		return 0, &Error{Code: ErrorRetryableUnavailable}
	}
	stateTemporaryBytes := saturatingAdd(
		roundLocalAllocation(uint64(len(encodedState)), allocationUnit), allocationUnit,
	)
	metadataEntries := saturatingAdd(inodesRequired, saturatingAdd(memberEntries, 4))
	metadataBytes := saturatingMultiply(metadataEntries, allocationUnit)
	reservationBytes := saturatingAdd(
		roundLocalAllocation(bytesRequired, allocationUnit),
		saturatingAdd(stateTemporaryBytes, metadataBytes),
	)
	admissionBytes := saturatingAdd(
		roundLocalAllocation(reservationBytes, allocationUnit),
		saturatingAdd(stateTemporaryBytes, metadataBytes),
	)
	admissionInodes := saturatingAdd(inodesRequired, 1) // atomic state-file temporary inode
	if reservationBytes > math.MaxInt64 {
		return 0, &Error{Code: ErrorResourceExhausted}
	}
	capacity, err := s.availableCapacity()
	if err != nil {
		return 0, &Error{Code: ErrorRetryableUnavailable}
	}
	if capacity.AvailableBytes < admissionBytes || capacity.AvailableInodes < admissionInodes {
		return 0, &Error{Code: ErrorResourceExhausted}
	}
	if err := s.inject(LocalFaultBeforeCapacityReserve, LocalFilesystemFaultEvent{
		OperationID: operationID, SubjectID: "materialization-generation",
	}); err != nil {
		return 0, localPhysicalMutationError(err)
	}
	return reservationBytes, nil
}

func (s *localFilesystemStore) filesystemAllocationUnit() (uint64, error) {
	directory, err := s.root.Open(".")
	if err != nil {
		return 0, err
	}
	defer directory.Close()
	var stats unix.Statfs_t
	if err := unix.Fstatfs(int(directory.Fd()), &stats); err != nil {
		return 0, err
	}
	if stats.Bsize <= 0 {
		return 0, ErrLocalFilesystemUnavailable
	}
	return uint64(stats.Bsize), nil
}

func roundLocalAllocation(value uint64, unit uint64) uint64 {
	if value == 0 || unit == 0 {
		return value
	}
	remainder := value % unit
	if remainder == 0 {
		return value
	}
	return saturatingAdd(value, unit-remainder)
}

func localPhysicalMutationError(err error) error {
	if errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT) {
		return &Error{Code: ErrorResourceExhausted}
	}
	return &Error{Code: ErrorDurabilityUnverified}
}

func (s *localFilesystemStore) verifyMaterialization(record localMaterializationRecord) error {
	return s.verifyMaterializationMode(record, true)
}

func (s *localFilesystemStore) verifyRemovableMaterialization(record localMaterializationRecord) error {
	return s.verifyMaterializationMode(record, false)
}

func (s *localFilesystemStore) verifyMaterializationMode(
	record localMaterializationRecord,
	requireReadOnlyDirectories bool,
) error {
	wrapperInfo, err := s.root.Lstat(record.Entry)
	wrapperIdentity, wrapperIdentityOK := localIdentityFromFileInfo(wrapperInfo)
	if err != nil || !wrapperInfo.IsDir() || wrapperInfo.Mode()&os.ModeSymlink != 0 ||
		requireReadOnlyDirectories && wrapperInfo.Mode().Perm()&0o222 != 0 || !wrapperIdentityOK ||
		!record.PhysicalIdentity.Known || wrapperIdentity != record.PhysicalIdentity {
		return &Error{Code: ErrorIntegrityFailure}
	}
	wrapper, err := s.root.Open(record.Entry)
	if err != nil {
		return &Error{Code: ErrorIntegrityFailure}
	}
	wrapperStat, statErr := wrapper.Stat()
	wrapperEntries, readErr := wrapper.ReadDir(-1)
	closeErr := wrapper.Close()
	if statErr != nil || !os.SameFile(wrapperInfo, wrapperStat) || readErr != nil || closeErr != nil ||
		len(wrapperEntries) != 2 {
		return &Error{Code: ErrorIntegrityFailure}
	}
	seenPayload := false
	seenCapacity := false
	for _, entry := range wrapperEntries {
		switch entry.Name() {
		case localMaterializationPayload:
			seenPayload = entry.IsDir()
		case localMaterializationCapacity:
			seenCapacity = !entry.IsDir()
		default:
			return &Error{Code: ErrorIntegrityFailure}
		}
	}
	capacityInfo, err := s.root.Lstat(record.Entry + "/" + localMaterializationCapacity)
	capacityIdentity, capacityIdentityOK := localIdentityFromFileInfo(capacityInfo)
	if err != nil || !seenPayload || !seenCapacity || !capacityInfo.Mode().IsRegular() ||
		capacityInfo.Mode()&os.ModeSymlink != 0 || capacityInfo.Mode().Perm()&0o222 != 0 ||
		!capacityIdentityOK || !record.CapacityIdentity.Known || capacityIdentity != record.CapacityIdentity ||
		!record.Capacity.Bytes.Known || uint64(capacityInfo.Size()) != record.Capacity.Bytes.Value {
		return &Error{Code: ErrorIntegrityFailure}
	}
	payloadInfo, err := s.root.Lstat(record.PayloadEntry)
	payloadIdentity, payloadIdentityOK := localIdentityFromFileInfo(payloadInfo)
	if err != nil || !payloadIdentityOK || !record.PayloadIdentity.Known ||
		payloadIdentity != record.PayloadIdentity {
		return &Error{Code: ErrorIntegrityFailure}
	}
	expectedFiles := make(map[string]struct{}, len(record.Members))
	expectedDirectories := map[string]struct{}{".": {}}
	for _, member := range record.Members {
		expectedFiles[string(member.LogicalMember)] = struct{}{}
		for parent := path.Dir(string(member.LogicalMember)); parent != "."; parent = path.Dir(parent) {
			expectedDirectories[parent] = struct{}{}
		}
	}
	seenFiles := make(map[string]struct{}, len(expectedFiles))
	seenDirectories := make(map[string]struct{}, len(expectedDirectories))
	if err := s.verifyMaterializationDirectory(
		record.PayloadEntry, ".", expectedFiles, expectedDirectories, seenFiles, seenDirectories,
		record.DirectoryIdentities, requireReadOnlyDirectories,
	); err != nil || len(seenFiles) != len(expectedFiles) || len(seenDirectories) != len(expectedDirectories) {
		return &Error{Code: ErrorIntegrityFailure}
	}
	for ordinal, member := range record.Members {
		content, ok := s.state.Contents[member.ContentID]
		if !ok {
			return &Error{Code: ErrorIntegrityFailure}
		}
		materializedInfo, err := s.root.Lstat(record.PayloadEntry + "/" + string(member.LogicalMember))
		if err != nil || !materializedInfo.Mode().IsRegular() || materializedInfo.Mode()&os.ModeSymlink != 0 {
			return &Error{Code: ErrorIntegrityFailure}
		}
		contentInfo, err := s.root.Lstat(content.Entry)
		if err != nil || !os.SameFile(materializedInfo, contentInfo) {
			return &Error{Code: ErrorIntegrityFailure}
		}
		if err := s.verifyContentRecord(record.OperationID, ordinal, content); err != nil {
			return err
		}
	}
	return nil
}

func (s *localFilesystemStore) verifyMaterializationDirectory(
	rootEntry string,
	relative string,
	expectedFiles map[string]struct{},
	expectedDirectories map[string]struct{},
	seenFiles map[string]struct{},
	seenDirectories map[string]struct{},
	directoryIdentities map[string]localPhysicalIdentity,
	requireReadOnly bool,
) error {
	entry := rootEntry
	if relative != "." {
		entry += "/" + relative
	}
	lstat, err := s.root.Lstat(entry)
	identity, identityOK := localIdentityFromFileInfo(lstat)
	expectedIdentity, expectedIdentityOK := directoryIdentities[relative]
	if err != nil || !lstat.IsDir() || lstat.Mode()&os.ModeSymlink != 0 ||
		requireReadOnly && lstat.Mode().Perm()&0o222 != 0 || !identityOK ||
		!expectedIdentityOK || !expectedIdentity.Known || identity != expectedIdentity {
		return &Error{Code: ErrorIntegrityFailure}
	}
	directory, err := s.root.Open(entry)
	if err != nil {
		return &Error{Code: ErrorIntegrityFailure}
	}
	stat, statErr := directory.Stat()
	entries, readErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	if statErr != nil || !stat.IsDir() || !os.SameFile(lstat, stat) || readErr != nil || closeErr != nil {
		return &Error{Code: ErrorIntegrityFailure}
	}
	seenDirectories[relative] = struct{}{}
	for _, candidate := range entries {
		candidateRelative := candidate.Name()
		if relative != "." {
			candidateRelative = path.Join(relative, candidate.Name())
		}
		candidateEntry := rootEntry + "/" + candidateRelative
		info, err := s.root.Lstat(candidateEntry)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return &Error{Code: ErrorIntegrityFailure}
		}
		if info.IsDir() {
			if _, ok := expectedDirectories[candidateRelative]; !ok {
				return &Error{Code: ErrorIntegrityFailure}
			}
			if err := s.verifyMaterializationDirectory(
				rootEntry, candidateRelative, expectedFiles, expectedDirectories, seenFiles, seenDirectories,
				directoryIdentities, requireReadOnly,
			); err != nil {
				return err
			}
			continue
		}
		if _, ok := expectedFiles[candidateRelative]; !ok || !info.Mode().IsRegular() ||
			info.Mode().Perm()&0o222 != 0 {
			return &Error{Code: ErrorIntegrityFailure}
		}
		seenFiles[candidateRelative] = struct{}{}
	}
	return nil
}

func (s *localFilesystemStore) bindMaterialization(authority localMaterializationAuthority) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.state.Materializations[authority.OperationID]
	if !ok {
		// The initial empty workspace has no physical members to materialize.
		if authority.PhysicalRequired {
			return ErrLocalFilesystemUnavailable
		}
		return nil
	}
	if authority.MaterializationID == "" || !record.Published || record.PolicyDomainID != authority.PolicyDomainID ||
		record.TaskID != authority.TaskID || record.TaskWorkspaceID != authority.TaskWorkspaceID ||
		record.Generation != authority.Generation || record.Fence != authority.Fence {
		return ErrLocalFilesystemUnavailable
	}
	if s.materializationHasCleanupClaim(record) {
		return ErrLocalFilesystemUnavailable
	}
	if record.MaterializationID != "" && record.MaterializationID != authority.MaterializationID {
		return ErrLocalFilesystemUnavailable
	}
	record.MaterializationID = authority.MaterializationID
	s.state.Materializations[authority.OperationID] = record
	s.state.MaterializationIDs[authority.MaterializationID] = authority.OperationID
	return s.persistState()
}

func (s *localFilesystemStore) materializationHasCleanupClaim(record localMaterializationRecord) bool {
	for _, claim := range s.state.CleanupClaims {
		if claim.Generation == record.PhysicalGeneration && claim.Entry == record.Entry &&
			claim.PhysicalIdentity == record.PhysicalIdentity &&
			claim.Kind == localCleanupMaterializationGeneration {
			return true
		}
	}
	return false
}

func (s *localFilesystemStore) activateCheckpoint(
	operationID OperationID,
	evidence CheckpointEvidence,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	prepared, ok := s.state.PreparedCheckpoints[operationID]
	if !ok || prepared.RequestDigest == "" {
		return ErrLocalFilesystemUnavailable
	}
	wanted := append([]ContentReference{evidence.ManifestReference}, evidence.ContentReferences...)
	preparedReferences := append([]ContentReference{prepared.Content.ManifestReference},
		prepared.Content.ContentReferences...)
	if len(wanted) != len(preparedReferences) {
		return ErrLocalFilesystemUnavailable
	}
	preparedByID := make(map[ContentReferenceID]ContentReference, len(preparedReferences))
	for _, reference := range preparedReferences {
		preparedByID[reference.ID] = reference
	}
	contentIDs := make(map[ContentID]struct{}, len(wanted))
	for _, reference := range wanted {
		expected, exists := preparedByID[reference.ID]
		stored, storedOK := s.state.References[reference.ID]
		if !exists || !storedOK || expected != reference || stored.Reference != reference {
			return ErrLocalFilesystemUnavailable
		}
		stored.Attached = true
		s.state.References[reference.ID] = stored
		contentIDs[reference.ContentID] = struct{}{}
	}
	for id, residue := range s.state.Residues {
		if residue.OperationID != operationID || residue.PendingContent == nil {
			continue
		}
		if _, activated := contentIDs[residue.PendingContent.ID]; !activated {
			continue
		}
		delete(s.state.Residues, id)
		if resource := s.state.CleanupResources[id]; !resource.DebtRegistered {
			delete(s.state.CleanupResources, id)
		}
	}
	prepared.Activated = true
	s.state.PreparedCheckpoints[operationID] = prepared
	if err := s.persistState(); err != nil {
		return ErrDurableObjectResultAmbiguous
	}
	return nil
}

func (s *localFilesystemStore) failureResidues(operationID OperationID) []localFilesystemResidue {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result []localFilesystemResidue
	for _, residue := range s.state.Residues {
		if residue.OperationID != operationID {
			continue
		}
		result = append(result, localFilesystemResidue{
			id: residue.ID, generation: residue.Generation, capacity: residue.Capacity,
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].id < result[j].id })
	return result
}

func (s *localFilesystemStore) markCleanupDebtRegistered(id string, generation string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	resource, ok := s.state.CleanupResources[id]
	if !ok || resource.Generation != generation {
		return ErrLocalFilesystemUnavailable
	}
	resource.DebtRegistered = true
	s.state.CleanupResources[id] = resource
	return s.persistState()
}

func (s *localFilesystemStore) registerCleanupResource(residue localResidueRecord) {
	var contentID ContentID
	if residue.PendingContent != nil {
		contentID = residue.PendingContent.ID
	}
	s.state.CleanupResources[residue.ID] = localCleanupResource{
		ID: residue.ID, Generation: residue.Generation, ContentID: contentID, Entry: residue.Entry,
		TemporaryEntry: residue.TemporaryEntry, PhysicalIdentity: residue.PhysicalIdentity,
		Kind: residue.Kind, Capacity: residue.Capacity,
	}
}

func localIdentityFromFileInfo(info os.FileInfo) (localPhysicalIdentity, bool) {
	if info == nil {
		return localPhysicalIdentity{}, false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return localPhysicalIdentity{}, false
	}
	return localPhysicalIdentity{Known: true, Device: uint64(stat.Dev), Inode: uint64(stat.Ino)}, true
}

func (s *localFilesystemStore) randomEntry(kind string) (string, error) {
	random := make([]byte, 16)
	if _, err := io.ReadFull(s.random, random); err != nil {
		return "", err
	}
	return kind + "-" + hex.EncodeToString(random), nil
}

func (s *localFilesystemStore) inject(point LocalFilesystemFaultPoint, event LocalFilesystemFaultEvent) error {
	if s.fault == nil || point == "" {
		return nil
	}
	event.Point = point
	return s.fault(event)
}

func (s *localFilesystemStore) syncDirectory(entry string, event LocalFilesystemFaultEvent) error {
	if event.OperationID != "" {
		if err := s.inject(LocalFaultBeforeDirectorySync, event); err != nil {
			return err
		}
	}
	name := entry
	if name == "" {
		name = "."
	}
	directory, err := s.root.Open(name)
	if err != nil {
		return err
	}
	err = directory.Sync()
	closeErr := directory.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil && event.OperationID != "" {
		err = s.inject(LocalFaultAfterDirectorySync, event)
	}
	return err
}

func (s *localFilesystemStore) renameEntryNoReplace(stage, entry string) error {
	fromDirectory, err := s.root.Open(path.Dir(stage))
	if err != nil {
		return err
	}
	toDirectory, err := s.root.Open(path.Dir(entry))
	if err != nil {
		_ = fromDirectory.Close()
		return err
	}
	renameErr := renameEntryNoReplaceAt(
		int(fromDirectory.Fd()), path.Base(stage), int(toDirectory.Fd()), path.Base(entry),
	)
	fromCloseErr := fromDirectory.Close()
	toCloseErr := toDirectory.Close()
	if renameErr != nil {
		return renameErr
	}
	if fromCloseErr != nil || toCloseErr != nil {
		return ErrDurableObjectResultAmbiguous
	}
	return nil
}

func reportLocalProgress(
	request PrepareCheckpointContentRequest,
	boundary DurableContentPrepareBoundary,
	ordinal int,
	subjectID string,
) error {
	if request.Progress == nil {
		return nil
	}
	return request.Progress(DurableContentPrepareProgress{
		Boundary: boundary, SubjectID: subjectID, Ordinal: ordinal,
	})
}

func localContentReference(
	id string,
	referenceType ContentReferenceType,
	request PrepareCheckpointContentRequest,
	member DeclaredStateMember,
	record localContentRecord,
) ContentReference {
	reference := ContentReference{
		ID: ContentReferenceID(id), Type: referenceType, PolicyDomainID: request.PolicyDomainID,
		TaskID: request.TaskID, TaskWorkspaceID: request.TaskWorkspaceID,
		RevisionID: request.RevisionID, CheckpointID: request.CheckpointID,
		StateMemberID: member.ID, LogicalMember: member.LogicalMember,
		ContentID: record.ID, ContentDigest: record.Digest, Size: record.Size,
		OperationID: request.Operation.ID,
	}
	reference.EvidenceDigest = reference.CanonicalDigest()
	return reference
}

func checkpointManifestForLocal(
	declared DeclaredStateManifest,
	references []ContentReference,
) CheckpointManifest {
	byMember := make(map[StateMemberID]ContentReference, len(references))
	for _, reference := range references {
		byMember[reference.StateMemberID] = reference
	}
	manifest := CheckpointManifest{
		DeclaredStateDigest: declared.Digest,
		Members:             make([]CheckpointManifestMember, len(declared.Members)),
	}
	for index, member := range declared.Members {
		manifest.Members[index] = CheckpointManifestMember{
			ID: member.ID, LogicalMember: member.LogicalMember, Type: member.Type,
			Mode: member.Mode, Class: member.Class, ContentID: byMember[member.ID].ContentID,
			ContentDigest: member.ContentDigest, Size: member.Size,
		}
	}
	manifest.Digest = manifest.CanonicalDigest()
	return manifest
}

func uniqueLocalReceipts(receipts []DurabilityReceipt) []DurabilityReceipt {
	byContent := make(map[ContentID]DurabilityReceipt, len(receipts))
	for _, receipt := range receipts {
		byContent[receipt.ContentID] = receipt
	}
	result := make([]DurabilityReceipt, 0, len(byContent))
	for _, receipt := range byContent {
		result = append(result, receipt)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ContentID < result[j].ContentID })
	return result
}

func localDeduplicationKey(domain PolicyDomainID, digest Digest, size uint64) string {
	return string(canonicalDigest(struct {
		Domain PolicyDomainID
		Digest Digest
		Size   uint64
	}{domain, digest, size}))
}

func digestBytesLocal(payload []byte) Digest {
	sum := sha256.Sum256(payload)
	return Digest("sha256:" + hex.EncodeToString(sum[:]))
}

func cloneVerifiedCheckpointContent(content VerifiedCheckpointContent) VerifiedCheckpointContent {
	clone := content
	clone.Manifest = cloneCheckpointManifest(content.Manifest)
	clone.ContentReferences = append([]ContentReference(nil), content.ContentReferences...)
	clone.DurabilityReceipts = append([]DurabilityReceipt(nil), content.DurabilityReceipts...)
	return clone
}

func localKnownCapacity(bytes, inodes uint64) CleanupCapacity {
	return CleanupCapacity{Bytes: KnownCleanupQuantity(bytes), Inodes: KnownCleanupQuantity(inodes)}
}

func saturatingAdd(left, right uint64) uint64 {
	result := left + right
	if result < left {
		return ^uint64(0)
	}
	return result
}

func saturatingMultiply(left, right uint64) uint64 {
	if left == 0 || right == 0 {
		return 0
	}
	if left > ^uint64(0)/right {
		return ^uint64(0)
	}
	return left * right
}
