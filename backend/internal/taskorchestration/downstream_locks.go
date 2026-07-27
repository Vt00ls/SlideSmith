package taskorchestration

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"reflect"
	"sort"
)

type TemplateVersionID struct{ value string }
type ResourceBundleID struct{ value string }

func NewTemplateVersionID(value string) (TemplateVersionID, error) {
	if !validOpaqueID(value) {
		return TemplateVersionID{}, invalidIntentError()
	}
	return TemplateVersionID{value: value}, nil
}

func NewResourceBundleID(value string) (ResourceBundleID, error) {
	if !validOpaqueID(value) {
		return ResourceBundleID{}, invalidIntentError()
	}
	return ResourceBundleID{value: value}, nil
}

func (id TemplateVersionID) String() string { return id.value }
func (id ResourceBundleID) String() string  { return id.value }

type PinnedLockPurpose uint8

const (
	PinnedLockRetry PinnedLockPurpose = iota + 1
	PinnedLockRecovery
	PinnedLockManualEdit
)

func validPinnedLockPurpose(purpose PinnedLockPurpose) bool {
	return purpose >= PinnedLockRetry && purpose <= PinnedLockManualEdit
}

type PublishedExecutionContract struct {
	SchemaVersion  EvidenceSchemaVersion
	Producer       EvidenceProducer
	ExecutionLock  ExecutionLock
	ContractDigest EvidenceDigest
	SafetyEpoch    SafetyEpoch
}

type PinnedExecutionLockRequest struct {
	TaskID         TaskID
	ExecutionLock  ExecutionLock
	ContractDigest EvidenceDigest
	SafetyEpoch    SafetyEpoch
	Purpose        PinnedLockPurpose
}

type ResolvedExecutionContract struct {
	ExecutionLock  ExecutionLock
	ContractDigest EvidenceDigest
	Producer       EvidenceProducer
	SafetyEpoch    SafetyEpoch
}

type ReleaseManagementPort interface {
	ResolveExecutionLock(context.Context, ExecutionLockID) (PublishedExecutionContract, error)
}

type ReleaseManagementAdapter interface {
	Resolve(context.Context, PinnedExecutionLockRequest) (ResolvedExecutionContract, error)
}

type releaseManagementAdapter struct {
	port  ReleaseManagementPort
	locks *downstreamReplayShell[pinnedLockKey, executionLockRequestFingerprint, ResolvedExecutionContract]
}

type pinnedLockKey struct {
	taskID TaskID
	lockID string
}

type executionLockRequestFingerprint struct {
	executionContractDigest EvidenceDigest
	requestedContractDigest EvidenceDigest
	safetyEpoch             SafetyEpoch
}

func NewReleaseManagementAdapter(port ReleaseManagementPort) ReleaseManagementAdapter {
	return &releaseManagementAdapter{
		port: port,
		locks: newDownstreamReplayShell[
			pinnedLockKey, executionLockRequestFingerprint, ResolvedExecutionContract,
		](),
	}
}

func (adapter *releaseManagementAdapter) Resolve(
	ctx context.Context,
	request PinnedExecutionLockRequest,
) (ResolvedExecutionContract, error) {
	if failure := validatePinnedExecutionLockRequest(request); failure != nil {
		return ResolvedExecutionContract{}, failure
	}
	key := pinnedLockKey{request.TaskID, request.ExecutionLock.ID.String()}
	fingerprint := executionLockRequestFingerprint{
		executionContractDigest: ExecutionLockContractDigest(request.ExecutionLock),
		requestedContractDigest: request.ContractDigest,
		safetyEpoch:             request.SafetyEpoch,
	}
	return invokeDownstreamWithReplay(
		adapter.locks,
		key,
		fingerprint,
		adapter.port != nil,
		nil,
		func() (PublishedExecutionContract, error) {
			return adapter.port.ResolveExecutionLock(ctx, request.ExecutionLock.ID)
		},
		func(record PublishedExecutionContract) *DownstreamError {
			return validatePublishedExecutionContract(record, request)
		},
		func(record PublishedExecutionContract) ResolvedExecutionContract {
			return ResolvedExecutionContract{
				ExecutionLock: cloneExecutionLock(record.ExecutionLock), ContractDigest: record.ContractDigest,
				Producer: record.Producer, SafetyEpoch: record.SafetyEpoch,
			}
		},
		cloneResolvedExecutionContract,
	)
}

func ExecutionLockContractDigest(lock ExecutionLock) EvidenceDigest {
	encoded, _ := json.Marshal(map[string]any{
		"compatibility_approval_id": lock.CompatibilityApprovalID.String(),
		"execution_lock_id":         lock.ID.String(),
		"pipeline_contract":         lock.PipelineContract.canonical(),
		"pipeline_version_id":       lock.PipelineVersionID.String(),
		"runtime_release_id":        lock.RuntimeReleaseID.String(),
	})
	sum := sha256.Sum256(encoded)
	return EvidenceDigest(sum)
}

func validatePinnedExecutionLockRequest(request PinnedExecutionLockRequest) *DownstreamError {
	if !validOpaqueID(request.TaskID.String()) || !validPinnedLockPurpose(request.Purpose) ||
		request.ContractDigest == (EvidenceDigest{}) || request.SafetyEpoch == 0 ||
		!validExecutionLock(request.ExecutionLock) ||
		request.ContractDigest != ExecutionLockContractDigest(request.ExecutionLock) {
		return newDownstreamError(DownstreamInvalidEnactment)
	}
	return nil
}

func validatePublishedExecutionContract(
	record PublishedExecutionContract,
	request PinnedExecutionLockRequest,
) *DownstreamError {
	if record.SchemaVersion.Major() != EvidenceSchemaV1.Major() {
		return newDownstreamError(DownstreamUnsupportedSchema)
	}
	if !validOpaqueID(record.Producer.AuthorityID.String()) || record.Producer.Generation == 0 ||
		record.SafetyEpoch == 0 || !validExecutionLock(record.ExecutionLock) || record.ContractDigest == (EvidenceDigest{}) ||
		record.ContractDigest != ExecutionLockContractDigest(record.ExecutionLock) {
		return newDownstreamError(DownstreamCorruptEvidence)
	}
	if record.SafetyEpoch != request.SafetyEpoch {
		return newDownstreamError(DownstreamStale)
	}
	if record.ExecutionLock.ID != request.ExecutionLock.ID ||
		record.ContractDigest != request.ContractDigest ||
		!reflect.DeepEqual(record.ExecutionLock, request.ExecutionLock) {
		return newDownstreamError(DownstreamIntegrityConflict)
	}
	return nil
}

func validExecutionLock(lock ExecutionLock) bool {
	if !validOpaqueID(lock.ID.String()) || !validOpaqueID(lock.PipelineVersionID.String()) ||
		!validOpaqueID(lock.RuntimeReleaseID.String()) ||
		!validOpaqueID(lock.CompatibilityApprovalID.String()) ||
		lock.PipelineContract.SchemaVersion != PipelineContractV1 ||
		lock.PipelineContract.PipelineVersionID != lock.PipelineVersionID || len(lock.PipelineContract.Routes) == 0 {
		return false
	}
	for _, route := range lock.PipelineContract.Routes {
		if route.Route.String() == "" || !validPhaseGraph(route.EntryPhase, route.Phases) {
			return false
		}
	}
	if lock.PipelineContract.ManualEditEntryPhase != (PhaseKey{}) ||
		len(lock.PipelineContract.ManualEditPhases) != 0 {
		return validPhaseGraph(
			lock.PipelineContract.ManualEditEntryPhase, lock.PipelineContract.ManualEditPhases,
		)
	}
	return true
}

func cloneExecutionLock(lock ExecutionLock) ExecutionLock {
	pinned := clonePinnedTaskStart(PinnedTaskStart{ExecutionLock: lock})
	return pinned.ExecutionLock
}

func cloneResolvedExecutionContract(resolved ResolvedExecutionContract) ResolvedExecutionContract {
	resolved.ExecutionLock = cloneExecutionLock(resolved.ExecutionLock)
	return resolved
}

type ResourceBundleContract struct {
	ResourceBundleID ResourceBundleID
	ManifestDigest   EvidenceDigest
	PackageDigest    EvidenceDigest
}

type PublishedTemplateLockContract struct {
	SchemaVersion                EvidenceSchemaVersion
	Producer                     EvidenceProducer
	TemplateLockID               TemplateLockID
	TemplateVersionID            TemplateVersionID
	TemplateManifestDigest       EvidenceDigest
	TemplatePackageDigest        EvidenceDigest
	CompatibilityEvidenceID      EvidenceID
	CompatibilityEvidenceDigest  EvidenceDigest
	CompatibilityExecutionLockID ExecutionLockID
	LockDigest                   EvidenceDigest
	ObservedGeneration           ProducerGeneration
	SafetyEpoch                  SafetyEpoch
	BundleClosure                []ResourceBundleContract
}

type PinnedTemplateLockRequest struct {
	TaskID             TaskID
	TemplateLockID     TemplateLockID
	LockDigest         EvidenceDigest
	ObservedGeneration ProducerGeneration
	SafetyEpoch        SafetyEpoch
	Purpose            PinnedLockPurpose
}

type ResolvedTemplateLockContract struct {
	TemplateLockID               TemplateLockID
	TemplateVersionID            TemplateVersionID
	TemplateManifestDigest       EvidenceDigest
	TemplatePackageDigest        EvidenceDigest
	CompatibilityEvidenceID      EvidenceID
	CompatibilityEvidenceDigest  EvidenceDigest
	CompatibilityExecutionLockID ExecutionLockID
	LockDigest                   EvidenceDigest
	ObservedGeneration           ProducerGeneration
	SafetyEpoch                  SafetyEpoch
	Producer                     EvidenceProducer
	BundleClosure                []ResourceBundleContract
}

type CatalogPublicationPort interface {
	ResolveTemplateLock(context.Context, TemplateLockID) (PublishedTemplateLockContract, error)
}

type CatalogPublicationAdapter interface {
	Resolve(context.Context, PinnedTemplateLockRequest) (ResolvedTemplateLockContract, error)
}

type catalogPublicationAdapter struct {
	port  CatalogPublicationPort
	locks *downstreamReplayShell[pinnedLockKey, EvidenceDigest, ResolvedTemplateLockContract]
}

func NewCatalogPublicationAdapter(port CatalogPublicationPort) CatalogPublicationAdapter {
	return &catalogPublicationAdapter{
		port:  port,
		locks: newDownstreamReplayShell[pinnedLockKey, EvidenceDigest, ResolvedTemplateLockContract](),
	}
}

func (adapter *catalogPublicationAdapter) Resolve(
	ctx context.Context,
	request PinnedTemplateLockRequest,
) (ResolvedTemplateLockContract, error) {
	if failure := validatePinnedTemplateLockRequest(request); failure != nil {
		return ResolvedTemplateLockContract{}, failure
	}
	key := pinnedLockKey{request.TaskID, request.TemplateLockID.String()}
	requestDigest := pinnedTemplateLockRequestDigest(request)
	return invokeDownstreamWithReplay(
		adapter.locks,
		key,
		requestDigest,
		adapter.port != nil,
		nil,
		func() (PublishedTemplateLockContract, error) {
			return adapter.port.ResolveTemplateLock(ctx, request.TemplateLockID)
		},
		func(record PublishedTemplateLockContract) *DownstreamError {
			return validatePublishedTemplateLockContract(record, request)
		},
		func(record PublishedTemplateLockContract) ResolvedTemplateLockContract {
			return ResolvedTemplateLockContract{
				TemplateLockID: record.TemplateLockID, TemplateVersionID: record.TemplateVersionID,
				TemplateManifestDigest:       record.TemplateManifestDigest,
				TemplatePackageDigest:        record.TemplatePackageDigest,
				CompatibilityEvidenceID:      record.CompatibilityEvidenceID,
				CompatibilityEvidenceDigest:  record.CompatibilityEvidenceDigest,
				CompatibilityExecutionLockID: record.CompatibilityExecutionLockID,
				LockDigest:                   record.LockDigest, ObservedGeneration: record.ObservedGeneration,
				SafetyEpoch: record.SafetyEpoch, Producer: record.Producer,
				BundleClosure: append([]ResourceBundleContract(nil), record.BundleClosure...),
			}
		},
		cloneResolvedTemplateLockContract,
	)
}

func TemplateLockContractDigest(record PublishedTemplateLockContract) EvidenceDigest {
	bundles := make([]map[string]any, len(record.BundleClosure))
	for index, bundle := range record.BundleClosure {
		bundles[index] = map[string]any{
			"manifest_digest":    bundle.ManifestDigest.String(),
			"package_digest":     bundle.PackageDigest.String(),
			"resource_bundle_id": bundle.ResourceBundleID.String(),
		}
	}
	sort.Slice(bundles, func(i, j int) bool {
		left := bundles[i]["resource_bundle_id"].(string) + "\x00" +
			bundles[i]["manifest_digest"].(string) + "\x00" + bundles[i]["package_digest"].(string)
		right := bundles[j]["resource_bundle_id"].(string) + "\x00" +
			bundles[j]["manifest_digest"].(string) + "\x00" + bundles[j]["package_digest"].(string)
		return left < right
	})
	encoded, _ := json.Marshal(map[string]any{
		"bundle_closure":                  bundles,
		"observed_generation":             uint64(record.ObservedGeneration),
		"producer_authority_id":           record.Producer.AuthorityID.String(),
		"producer_generation":             uint64(record.Producer.Generation),
		"safety_epoch":                    uint64(record.SafetyEpoch),
		"schema_version":                  uint64(record.SchemaVersion),
		"template_lock_id":                record.TemplateLockID.String(),
		"template_version_id":             record.TemplateVersionID.String(),
		"template_manifest_digest":        record.TemplateManifestDigest.String(),
		"template_package_digest":         record.TemplatePackageDigest.String(),
		"compatibility_evidence_id":       record.CompatibilityEvidenceID.String(),
		"compatibility_evidence_digest":   record.CompatibilityEvidenceDigest.String(),
		"compatibility_execution_lock_id": record.CompatibilityExecutionLockID.String(),
	})
	sum := sha256.Sum256(encoded)
	return EvidenceDigest(sum)
}

func validatePinnedTemplateLockRequest(request PinnedTemplateLockRequest) *DownstreamError {
	if !validOpaqueID(request.TaskID.String()) || !validOpaqueID(request.TemplateLockID.String()) ||
		request.LockDigest == (EvidenceDigest{}) || request.ObservedGeneration == 0 ||
		request.SafetyEpoch == 0 || !validPinnedLockPurpose(request.Purpose) {
		return newDownstreamError(DownstreamInvalidEnactment)
	}
	return nil
}

func validatePublishedTemplateLockContract(
	record PublishedTemplateLockContract,
	request PinnedTemplateLockRequest,
) *DownstreamError {
	if record.SchemaVersion.Major() != EvidenceSchemaV1.Major() {
		return newDownstreamError(DownstreamUnsupportedSchema)
	}
	if !validOpaqueID(record.Producer.AuthorityID.String()) || record.Producer.Generation == 0 ||
		!validOpaqueID(record.TemplateLockID.String()) || !validOpaqueID(record.TemplateVersionID.String()) ||
		record.TemplateManifestDigest == (EvidenceDigest{}) || record.TemplatePackageDigest == (EvidenceDigest{}) ||
		!validOpaqueID(record.CompatibilityEvidenceID.String()) ||
		record.CompatibilityEvidenceDigest == (EvidenceDigest{}) ||
		!validOpaqueID(record.CompatibilityExecutionLockID.String()) ||
		record.ObservedGeneration == 0 || record.SafetyEpoch == 0 ||
		record.LockDigest == (EvidenceDigest{}) || record.LockDigest != TemplateLockContractDigest(record) ||
		!validBundleClosure(record.BundleClosure) {
		return newDownstreamError(DownstreamCorruptEvidence)
	}
	if record.TemplateLockID != request.TemplateLockID || record.LockDigest != request.LockDigest {
		return newDownstreamError(DownstreamIntegrityConflict)
	}
	if record.ObservedGeneration != request.ObservedGeneration || record.SafetyEpoch != request.SafetyEpoch {
		return newDownstreamError(DownstreamStale)
	}
	return nil
}

func validBundleClosure(closure []ResourceBundleContract) bool {
	seen := make(map[ResourceBundleID]struct{}, len(closure))
	for _, bundle := range closure {
		if !validOpaqueID(bundle.ResourceBundleID.String()) ||
			bundle.ManifestDigest == (EvidenceDigest{}) || bundle.PackageDigest == (EvidenceDigest{}) {
			return false
		}
		if _, duplicate := seen[bundle.ResourceBundleID]; duplicate {
			return false
		}
		seen[bundle.ResourceBundleID] = struct{}{}
	}
	return true
}

func pinnedTemplateLockRequestDigest(request PinnedTemplateLockRequest) EvidenceDigest {
	encoded, _ := json.Marshal(map[string]any{
		"lock_digest":         request.LockDigest.String(),
		"observed_generation": uint64(request.ObservedGeneration),
		"safety_epoch":        uint64(request.SafetyEpoch),
		"task_id":             request.TaskID.String(),
		"template_lock_id":    request.TemplateLockID.String(),
	})
	sum := sha256.Sum256(encoded)
	return EvidenceDigest(sum)
}

func cloneResolvedTemplateLockContract(
	resolved ResolvedTemplateLockContract,
) ResolvedTemplateLockContract {
	resolved.BundleClosure = append([]ResourceBundleContract(nil), resolved.BundleClosure...)
	return resolved
}
