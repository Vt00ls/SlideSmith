package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/slidesmith/slidesmith/backend/internal/model"
)

func TestBuildBeautifyInputsContractDiscoversUniquePPTXAndArrayManifest(t *testing.T) {
	fixture := newBeautifyContractFixture(t, 2)
	contract, err := ValidateBeautifyInputsContract(fixture.projectPath, fixture.taskID)
	if err != nil {
		t.Fatal(err)
	}
	if contract.SlideCount != 2 || contract.Canvas.AspectRatio <= 1 || contract.ImageManifest == nil || contract.ImageCount != 1 {
		t.Fatalf("inputs contract = %#v", contract)
	}
	for _, path := range []string{contract.SourcePPTX.Path, contract.SourceMarkdown.Path, contract.Identity.Path, contract.SlideLibrary.Path, contract.SourceProfile.Path} {
		if filepath.IsAbs(path) || strings.Contains(path, "..") || path != filepath.ToSlash(path) {
			t.Fatalf("unsafe contract path %q", path)
		}
	}
}

func TestBuildBeautifyInputsContractRejectsMultipleAndVariants(t *testing.T) {
	for _, test := range []struct {
		name     string
		filename string
		want     string
	}{
		{name: "multiple", filename: "other.pptx", want: "multiple_pptx"},
		{name: "variant", filename: "other.pptm", want: "unsupported_source_type"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newBeautifyContractFixture(t, 1)
			writeBeautifyTestPPTX(t, filepath.Join(fixture.projectPath, "sources", test.filename), 1)
			if _, err := BuildBeautifyInputsContract(fixture.projectPath, fixture.taskID, model.RunnerProfileFullPPTMaster); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestValidateBeautifyInputsContractRejectsMutationAndSymlink(t *testing.T) {
	fixture := newBeautifyContractFixture(t, 1)
	writeBeautifyTestFile(t, filepath.Join(fixture.projectPath, "sources", "deck.md"), "mutated\n")
	if _, err := ValidateBeautifyInputsContract(fixture.projectPath, fixture.taskID); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("mutation error = %v", err)
	}

	fixture = newBeautifyContractFixture(t, 1)
	identity := filepath.Join(fixture.projectPath, "analysis", "deck.identity.json")
	if err := os.Remove(identity); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "identity.json")
	writeBeautifyTestFile(t, outside, `{}`)
	if err := os.Symlink(outside, identity); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateBeautifyInputsContract(fixture.projectPath, fixture.taskID); err == nil || !strings.Contains(strings.ToLower(err.Error()), "symlink") {
		t.Fatalf("symlink error = %v", err)
	}
}

func TestValidateBeautifyInputsContractRejectsLiveSourceSetChange(t *testing.T) {
	fixture := newBeautifyContractFixture(t, 1)
	writeBeautifyTestPPTX(t, filepath.Join(fixture.projectPath, "sources", "added.pptx"), 1)
	if _, err := ValidateBeautifyInputsContract(fixture.projectPath, fixture.taskID); err == nil || !strings.Contains(err.Error(), "multiple_pptx") {
		t.Fatalf("error = %v", err)
	}
}
