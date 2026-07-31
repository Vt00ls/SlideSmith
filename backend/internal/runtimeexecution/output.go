package runtimeexecution

import (
	"encoding/json"
	"path"
	"sort"
	"strings"
)

type OutputChannelID struct{ value string }
type OutputReferenceID struct{ value string }

func NewOutputChannelID(value string) (OutputChannelID, error) {
	value, err := newOpaqueReferenceID(value)
	return OutputChannelID{value: value}, err
}

func NewOutputReferenceID(value string) (OutputReferenceID, error) {
	value, err := newOpaqueReferenceID(value)
	return OutputReferenceID{value: value}, err
}

func (id OutputChannelID) String() string   { return id.value }
func (id OutputReferenceID) String() string { return id.value }

type OutputContentClass uint8

const (
	OutputClassDeck OutputContentClass = iota + 1
	OutputClassValidationReport
	OutputClassEvidence
	OutputClassOpaque
)

type OutputFileType uint8

const (
	OutputRegularFile OutputFileType = iota + 1
	OutputSymbolicLink
	OutputDevice
	OutputFIFO
	OutputSocket
)

type DeclaredOutputChannel struct {
	ChannelID    OutputChannelID
	LogicalPath  string
	Class        OutputContentClass
	Required     bool
	MaxSizeBytes uint64
}

type OutputContract struct {
	SchemaVersion     SchemaVersion
	ContractDigest    Digest
	Channels          []DeclaredOutputChannel
	MaxOutputCount    uint64
	MaxTotalSizeBytes uint64
}

type OutputProposalEntry struct {
	ChannelID         OutputChannelID
	LogicalPath       string
	Class             OutputContentClass
	FileType          OutputFileType
	LinkCount         uint64
	Digest            Digest
	SizeBytes         uint64
	VerifiedDigest    Digest
	VerifiedSizeBytes uint64
	OutputReferenceID OutputReferenceID
	Metadata          map[string]string
}

type OutputCompleteness uint8

const (
	OutputComplete OutputCompleteness = iota + 1
	OutputPartial
)

type OutputProposal struct {
	SchemaVersion    SchemaVersion
	Completeness     OutputCompleteness
	RuntimeSucceeded bool
	Entries          []OutputProposalEntry
}

type OutputTrust uint8

const (
	OutputUntrustedProposal OutputTrust = iota + 1
)

type CanonicalOutputEntry struct {
	ChannelID         OutputChannelID
	LogicalPath       string
	Class             OutputContentClass
	Digest            Digest
	SizeBytes         uint64
	OutputReferenceID OutputReferenceID
}

type CanonicalOutputManifest struct {
	SchemaVersion    SchemaVersion
	ContractDigest   Digest
	Digest           Digest
	Trust            OutputTrust
	Completeness     OutputCompleteness
	RuntimeSucceeded bool
	Entries          []CanonicalOutputEntry
}

type OutputValidationCode uint8

const (
	OutputValidationSchema OutputValidationCode = iota + 1
	OutputValidationMissingRequired
	OutputValidationUnexpected
	OutputValidationDuplicate
	OutputValidationCollision
	OutputValidationIntegrity
	OutputValidationLimit
	OutputValidationPath
	OutputValidationFileType
	OutputValidationSubstitution
	OutputValidationMetadata
)

type OutputValidationError struct{ code OutputValidationCode }

func (failure *OutputValidationError) Error() string {
	return "runtime output proposal failed closed validation"
}

func (failure *OutputValidationError) Code() OutputValidationCode {
	if failure == nil {
		return OutputValidationSchema
	}
	return failure.code
}

func outputValidationError(code OutputValidationCode) *OutputValidationError {
	return &OutputValidationError{code: code}
}

func ValidateOutputProposal(
	contract OutputContract,
	proposal OutputProposal,
) (CanonicalOutputManifest, error) {
	declared, err := validateOutputContract(contract)
	if err != nil {
		return CanonicalOutputManifest{}, err
	}
	if proposal.SchemaVersion.Major() != SchemaV1.Major() ||
		proposal.Completeness < OutputComplete || proposal.Completeness > OutputPartial {
		return CanonicalOutputManifest{}, outputValidationError(OutputValidationSchema)
	}
	if uint64(len(proposal.Entries)) > contract.MaxOutputCount {
		return CanonicalOutputManifest{}, outputValidationError(OutputValidationLimit)
	}
	seenChannels := make(map[OutputChannelID]struct{}, len(proposal.Entries))
	seenPaths := make(map[string]struct{}, len(proposal.Entries))
	present := make(map[OutputChannelID]struct{}, len(proposal.Entries))
	canonical := make([]CanonicalOutputEntry, 0, len(proposal.Entries))
	var total uint64
	for _, entry := range proposal.Entries {
		channel, exists := declared[entry.ChannelID]
		if !exists {
			return CanonicalOutputManifest{}, outputValidationError(OutputValidationUnexpected)
		}
		if _, exists := seenChannels[entry.ChannelID]; exists {
			return CanonicalOutputManifest{}, outputValidationError(OutputValidationDuplicate)
		}
		seenChannels[entry.ChannelID] = struct{}{}
		if !safeSandboxLogicalPath(entry.LogicalPath) {
			return CanonicalOutputManifest{}, outputValidationError(OutputValidationPath)
		}
		if _, exists := seenPaths[entry.LogicalPath]; exists {
			return CanonicalOutputManifest{}, outputValidationError(OutputValidationCollision)
		}
		seenPaths[entry.LogicalPath] = struct{}{}
		if entry.LogicalPath != channel.LogicalPath || entry.Class != channel.Class {
			return CanonicalOutputManifest{}, outputValidationError(OutputValidationSubstitution)
		}
		if entry.FileType != OutputRegularFile || entry.LinkCount != 1 {
			return CanonicalOutputManifest{}, outputValidationError(OutputValidationFileType)
		}
		if entry.Digest == (Digest{}) || entry.VerifiedDigest != entry.Digest ||
			entry.VerifiedSizeBytes != entry.SizeBytes || !validOpaqueReferenceID(entry.OutputReferenceID.String()) {
			return CanonicalOutputManifest{}, outputValidationError(OutputValidationIntegrity)
		}
		if len(entry.Metadata) != 0 {
			return CanonicalOutputManifest{}, outputValidationError(OutputValidationMetadata)
		}
		if entry.SizeBytes > channel.MaxSizeBytes || entry.SizeBytes > contract.MaxTotalSizeBytes ||
			total > contract.MaxTotalSizeBytes-entry.SizeBytes {
			return CanonicalOutputManifest{}, outputValidationError(OutputValidationLimit)
		}
		total += entry.SizeBytes
		present[entry.ChannelID] = struct{}{}
		canonical = append(canonical, CanonicalOutputEntry{
			ChannelID: entry.ChannelID, LogicalPath: entry.LogicalPath, Class: entry.Class,
			Digest: entry.Digest, SizeBytes: entry.SizeBytes, OutputReferenceID: entry.OutputReferenceID,
		})
	}
	for id, channel := range declared {
		if channel.Required {
			if _, exists := present[id]; !exists {
				return CanonicalOutputManifest{}, outputValidationError(OutputValidationMissingRequired)
			}
		}
	}
	sort.Slice(canonical, func(left, right int) bool {
		return canonical[left].ChannelID.String() < canonical[right].ChannelID.String()
	})
	type canonicalOutputEntryWire struct {
		ChannelID         string             `json:"channel_id"`
		LogicalPath       string             `json:"logical_path"`
		Class             OutputContentClass `json:"class"`
		Digest            string             `json:"digest"`
		SizeBytes         uint64             `json:"size_bytes"`
		OutputReferenceID string             `json:"output_reference_id"`
	}
	wireEntries := make([]canonicalOutputEntryWire, 0, len(canonical))
	for _, entry := range canonical {
		wireEntries = append(wireEntries, canonicalOutputEntryWire{
			ChannelID: entry.ChannelID.String(), LogicalPath: entry.LogicalPath, Class: entry.Class,
			Digest: entry.Digest.String(), SizeBytes: entry.SizeBytes,
			OutputReferenceID: entry.OutputReferenceID.String(),
		})
	}
	wire := struct {
		SchemaVersion    string                     `json:"schema_version"`
		ContractDigest   string                     `json:"contract_digest"`
		Trust            OutputTrust                `json:"trust"`
		Completeness     OutputCompleteness         `json:"completeness"`
		RuntimeSucceeded bool                       `json:"runtime_succeeded"`
		Entries          []canonicalOutputEntryWire `json:"entries"`
	}{
		SchemaVersion:  "slidesmith.runtime-execution.output-manifest/v1",
		ContractDigest: contract.ContractDigest.String(), Trust: OutputUntrustedProposal,
		Completeness: proposal.Completeness, RuntimeSucceeded: proposal.RuntimeSucceeded, Entries: wireEntries,
	}
	encoded, err := json.Marshal(wire)
	if err != nil {
		return CanonicalOutputManifest{}, outputValidationError(OutputValidationIntegrity)
	}
	return CanonicalOutputManifest{
		SchemaVersion: proposal.SchemaVersion, ContractDigest: contract.ContractDigest,
		Digest: digestBytes(encoded), Trust: OutputUntrustedProposal,
		Completeness: proposal.Completeness, RuntimeSucceeded: proposal.RuntimeSucceeded, Entries: canonical,
	}, nil
}

func validateOutputContract(contract OutputContract) (map[OutputChannelID]DeclaredOutputChannel, error) {
	if contract.SchemaVersion.Major() != SchemaV1.Major() || contract.ContractDigest == (Digest{}) ||
		contract.MaxOutputCount == 0 || contract.MaxTotalSizeBytes == 0 ||
		uint64(len(contract.Channels)) > contract.MaxOutputCount {
		return nil, outputValidationError(OutputValidationSchema)
	}
	declared := make(map[OutputChannelID]DeclaredOutputChannel, len(contract.Channels))
	paths := make(map[string]struct{}, len(contract.Channels))
	for _, channel := range contract.Channels {
		if !validOpaqueReferenceID(channel.ChannelID.String()) || !safeSandboxLogicalPath(channel.LogicalPath) ||
			channel.Class < OutputClassDeck || channel.Class > OutputClassOpaque || channel.MaxSizeBytes == 0 {
			return nil, outputValidationError(OutputValidationSchema)
		}
		if _, exists := declared[channel.ChannelID]; exists {
			return nil, outputValidationError(OutputValidationDuplicate)
		}
		if _, exists := paths[channel.LogicalPath]; exists {
			return nil, outputValidationError(OutputValidationCollision)
		}
		declared[channel.ChannelID] = channel
		paths[channel.LogicalPath] = struct{}{}
	}
	return declared, nil
}

func safeSandboxLogicalPath(value string) bool {
	if value == "" || strings.ContainsRune(value, '\x00') || strings.Contains(value, "\\") ||
		strings.HasPrefix(value, "/") || strings.Contains(value, ":") || path.Clean(value) != value ||
		value == "." || strings.HasPrefix(value, "../") || strings.Contains(value, "/../") {
		return false
	}
	return true
}
