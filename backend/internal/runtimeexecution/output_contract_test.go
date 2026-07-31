package runtimeexecution

import (
	"testing"
)

func TestDeclaredOutputChannelsValidateUntrustedProposalSecurityMatrix(t *testing.T) {
	t.Parallel()

	contract := OutputContract{
		SchemaVersion: SchemaV1, ContractDigest: digest(13), MaxOutputCount: 2, MaxTotalSizeBytes: 30,
		Channels: []DeclaredOutputChannel{
			{ChannelID: mustOutputChannelID(t, "deck"), LogicalPath: "outputs/deck.pptx", Class: OutputClassDeck, Required: true, MaxSizeBytes: 20},
			{ChannelID: mustOutputChannelID(t, "report"), LogicalPath: "outputs/report.json", Class: OutputClassValidationReport, Required: false, MaxSizeBytes: 10},
		},
	}
	valid := OutputProposal{
		SchemaVersion: SchemaV1, Completeness: OutputComplete, RuntimeSucceeded: true,
		Entries: []OutputProposalEntry{{
			ChannelID: mustOutputChannelID(t, "deck"), LogicalPath: "outputs/deck.pptx",
			Class: OutputClassDeck, FileType: OutputRegularFile, LinkCount: 1,
			Digest: digest(41), SizeBytes: 20, VerifiedDigest: digest(41), VerifiedSizeBytes: 20,
			OutputReferenceID: mustOutputReferenceID(t, "output-deck"),
		}},
	}
	manifest, err := ValidateOutputProposal(contract, valid)
	if err != nil {
		t.Fatalf("valid output: %v", err)
	}
	if manifest.Trust != OutputUntrustedProposal || manifest.Completeness != OutputComplete || !manifest.RuntimeSucceeded ||
		manifest.ContractDigest != contract.ContractDigest || manifest.Digest == (Digest{}) || len(manifest.Entries) != 1 {
		t.Fatalf("canonical output manifest = %+v", manifest)
	}
	partial := valid
	partial.Completeness = OutputPartial
	partialManifest, err := ValidateOutputProposal(contract, partial)
	if err != nil || partialManifest.Completeness != OutputPartial || partialManifest.Digest == manifest.Digest {
		t.Fatalf("output completeness was not bound independently: %+v err=%v", partialManifest, err)
	}
	notSucceeded := valid
	notSucceeded.RuntimeSucceeded = false
	failedManifest, err := ValidateOutputProposal(contract, notSucceeded)
	if err != nil || failedManifest.Digest == manifest.Digest || failedManifest.Trust != OutputUntrustedProposal {
		t.Fatalf("Runtime outcome was not bound separately as an untrusted proposal: %+v err=%v", failedManifest, err)
	}
	differentReference := valid
	differentReference.Entries = append([]OutputProposalEntry(nil), valid.Entries...)
	differentReference.Entries[0].OutputReferenceID = mustOutputReferenceID(t, "output-deck-replacement")
	differentReferenceManifest, err := ValidateOutputProposal(contract, differentReference)
	if err != nil {
		t.Fatalf("validate replacement output reference: %v", err)
	}
	if differentReferenceManifest.Digest == manifest.Digest {
		t.Fatal("canonical output manifest digest did not bind the opaque output reference")
	}

	tests := []struct {
		name string
		edit func(*OutputProposal)
		code OutputValidationCode
	}{
		{name: "missing required output", edit: func(value *OutputProposal) { value.Entries = nil }, code: OutputValidationMissingRequired},
		{name: "unexpected output", edit: func(value *OutputProposal) { value.Entries[0].ChannelID = mustOutputChannelID(t, "unexpected") }, code: OutputValidationUnexpected},
		{name: "duplicate channel", edit: func(value *OutputProposal) { value.Entries = append(value.Entries, value.Entries[0]) }, code: OutputValidationDuplicate},
		{name: "path collision", edit: func(value *OutputProposal) {
			second := value.Entries[0]
			second.ChannelID = mustOutputChannelID(t, "report")
			value.Entries = append(value.Entries, second)
		}, code: OutputValidationCollision},
		{name: "digest mismatch", edit: func(value *OutputProposal) { value.Entries[0].VerifiedDigest = digest(42) }, code: OutputValidationIntegrity},
		{name: "size mismatch", edit: func(value *OutputProposal) { value.Entries[0].VerifiedSizeBytes = 19 }, code: OutputValidationIntegrity},
		{name: "object-store locator disguised as output reference", edit: func(value *OutputProposal) {
			value.Entries[0].OutputReferenceID = OutputReferenceID{value: "s3://private-bucket/object-key"}
		}, code: OutputValidationIntegrity},
		{name: "oversize", edit: func(value *OutputProposal) { value.Entries[0].SizeBytes = 21; value.Entries[0].VerifiedSizeBytes = 21 }, code: OutputValidationLimit},
		{name: "count limit", edit: func(value *OutputProposal) {
			value.Entries = append(value.Entries, OutputProposalEntry{
				ChannelID: mustOutputChannelID(t, "report"), LogicalPath: "outputs/report.json",
				Class: OutputClassValidationReport, FileType: OutputRegularFile, LinkCount: 1,
				Digest: digest(43), SizeBytes: 1, VerifiedDigest: digest(43), VerifiedSizeBytes: 1,
				OutputReferenceID: mustOutputReferenceID(t, "output-report"),
			}, value.Entries[0])
		}, code: OutputValidationLimit},
		{name: "path traversal", edit: func(value *OutputProposal) { value.Entries[0].LogicalPath = "../host/secret" }, code: OutputValidationPath},
		{name: "absolute path", edit: func(value *OutputProposal) { value.Entries[0].LogicalPath = "/host/secret" }, code: OutputValidationPath},
		{name: "symlink escape", edit: func(value *OutputProposal) { value.Entries[0].FileType = OutputSymbolicLink }, code: OutputValidationFileType},
		{name: "hardlink escape", edit: func(value *OutputProposal) { value.Entries[0].LinkCount = 2 }, code: OutputValidationFileType},
		{name: "device", edit: func(value *OutputProposal) { value.Entries[0].FileType = OutputDevice }, code: OutputValidationFileType},
		{name: "fifo", edit: func(value *OutputProposal) { value.Entries[0].FileType = OutputFIFO }, code: OutputValidationFileType},
		{name: "socket", edit: func(value *OutputProposal) { value.Entries[0].FileType = OutputSocket }, code: OutputValidationFileType},
		{name: "cross-channel substitution", edit: func(value *OutputProposal) { value.Entries[0].Class = OutputClassValidationReport }, code: OutputValidationSubstitution},
		{name: "undeclared metadata", edit: func(value *OutputProposal) { value.Entries[0].Metadata = map[string]string{"host_path": "/private"} }, code: OutputValidationMetadata},
		{name: "unknown schema", edit: func(value *OutputProposal) { value.SchemaVersion = NewSchemaVersion(2, 0) }, code: OutputValidationSchema},
		{name: "unknown completeness", edit: func(value *OutputProposal) { value.Completeness = 0 }, code: OutputValidationSchema},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			proposal := valid
			proposal.Entries = append([]OutputProposalEntry(nil), valid.Entries...)
			test.edit(&proposal)
			_, err := ValidateOutputProposal(contract, proposal)
			failure, ok := err.(*OutputValidationError)
			if !ok || failure.Code() != test.code {
				t.Fatalf("error=%T %v code=%v, want %v", err, err, failure, test.code)
			}
		})
	}
}

func mustOutputChannelID(t *testing.T, value string) OutputChannelID {
	t.Helper()
	id, err := NewOutputChannelID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustOutputReferenceID(t *testing.T, value string) OutputReferenceID {
	t.Helper()
	id, err := NewOutputReferenceID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
