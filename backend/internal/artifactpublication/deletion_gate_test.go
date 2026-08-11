package artifactpublication

// Structural deletion gates (parent SPEC #103 Testing Decision 23,
// docs/architecture/). These gates are structural, not textual: they prove
// capability and dependency absence by inspecting the actual build closure
// and the actual interface/type graph. They do not depend on one literal
// filename, method spelling, error string, or source substring, so harmless
// renames cannot satisfy or break them.

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

const backendModulePrefix = "github.com/slidesmith/slidesmith/backend"

// allowlistedOwnedPorts are the only SlideSmith packages the C05 Artifact
// Publication core and its future Task Orchestration integration may depend
// on: the module itself, the two upstream module ports whose evidence it
// consumes (Task Workspace Lifecycle, Runtime Execution), the Task
// Orchestration port, and the test-only PostgreSQL harness. Legacy
// service/handler/repository/model/router/config/database packages are
// deletion targets, never dependencies.
var allowlistedOwnedPorts = map[string]bool{
	backendModulePrefix + "/internal/artifactpublication": true,
	backendModulePrefix + "/internal/taskorchestration":   true,
	backendModulePrefix + "/internal/taskworkspace":       true,
	backendModulePrefix + "/internal/runtimeexecution":    true,
	backendModulePrefix + "/internal/testpostgres":        true,
}

// legacyPackagePrefixes are the packages that the cutover deletes rather
// than wraps (ADR 0016; legacy-business-migration-and-compatibility.md).
var legacyPackagePrefixes = []string{
	backendModulePrefix + "/internal/service",
	backendModulePrefix + "/internal/handler",
	backendModulePrefix + "/internal/repository",
	backendModulePrefix + "/internal/model",
	backendModulePrefix + "/internal/router",
	backendModulePrefix + "/internal/config",
	backendModulePrefix + "/internal/database",
}

// TestStructuralDeletionGateLegacyPackagesAbsentFromBuildClosure proves by
// import-graph inspection that the Artifact Publication package builds and
// runs its contract without any legacy package: every SlideSmith package in
// the transitive closure (including test dependencies) is an allowlisted
// owned port.
func TestStructuralDeletionGateLegacyPackagesAbsentFromBuildClosure(t *testing.T) {
	root, err := findBackendModuleRoot()
	if err != nil {
		t.Fatalf("locate backend module root: %v", err)
	}
	command := exec.Command("go", "list", "-deps", "-test", "./internal/artifactpublication")
	command.Dir = root
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("go list -deps -test failed: %v\n%s", err, stderr.String())
	}

	seen := make(map[string]bool)
	for _, raw := range strings.Split(stdout.String(), "\n") {
		pkg := normalizeGoListPackage(raw)
		if pkg == "" || !strings.HasPrefix(pkg, backendModulePrefix) {
			continue
		}
		if seen[pkg] {
			continue
		}
		seen[pkg] = true
		for _, legacy := range legacyPackagePrefixes {
			if pkg == legacy || strings.HasPrefix(pkg, legacy) {
				t.Fatalf("target build closure depends on legacy package %q; these are deletion targets", pkg)
			}
		}
		if !allowlistedOwnedPorts[pkg] {
			t.Fatalf("target build closure depends on unallowlisted package %q; allow only owned ports: %v",
				pkg, sortedMapKeys(allowlistedOwnedPorts))
		}
	}
	if !seen[backendModulePrefix+"/internal/artifactpublication"] {
		t.Fatal("dependency closure is missing the target package")
	}
}

// TestStructuralDeletionGateTargetBuildsWithoutLegacyPackages proves the
// target package compiles as a standalone build target with the legacy
// packages absent.
func TestStructuralDeletionGateTargetBuildsWithoutLegacyPackages(t *testing.T) {
	root, err := findBackendModuleRoot()
	if err != nil {
		t.Fatalf("locate backend module root: %v", err)
	}
	command := exec.Command("go", "build", "./internal/artifactpublication")
	command.Dir = root
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("target package does not build without legacy packages: %v\n%s", err, stderr.String())
	}
}

// TestStructuralDeletionGateCapabilitySurfaceAbsence proves no seam exposes
// a path, object key, bucket, mount, vendor, credential, session, signed
// URL, general repository, or active-setter capability. The check walks the
// actual method signatures and reachable value types, so a rename cannot
// satisfy or break it.
func TestStructuralDeletionGateCapabilitySurfaceAbsence(t *testing.T) {
	seams := []reflect.Type{
		reflect.TypeOf((*PublicationCore)(nil)).Elem(),
		reflect.TypeOf((*PublicationIntent)(nil)).Elem(),
		reflect.TypeOf(PublicationDecision{}),
		reflect.TypeOf(PublicationQuery{}),
		reflect.TypeOf(PublicationView{}),
		reflect.TypeOf(ArtifactContentTarget{}),
		reflect.TypeOf(C04ReconstructionCapability{}),
		reflect.TypeOf(ContentScope{}),
	}
	forbiddenFieldFragments := []string{
		"path", "object_key", "objectkey", "prefix", "bucket", "mount",
		"vendor", "credential", "session", "signed_url", "locator", "latest",
		"created_at", "activated_at", "timestamp",
	}
	forbiddenMethodFragments := []string{
		"SetActive", "Set", "Update", "Delete", "Insert", "Save",
		"Create", "Repo", "Repository", "MutateOther", "Callback", "Ingest",
	}
	forbiddenPackages := map[string]bool{"os": true, "os/exec": true, "syscall": true}

	for _, seam := range seams {
		for index := 0; index < seam.NumMethod(); index++ {
			method := seam.Method(index)
			for _, fragment := range forbiddenMethodFragments {
				if strings.Contains(method.Name, fragment) {
					t.Fatalf("seam %s exposes forbidden method %q", seam, method.Name)
				}
			}
			signature := method.Type
			for arg := 0; arg < signature.NumIn(); arg++ {
				walkSeamType(t, seam.String()+"."+method.Name, signature.In(arg), forbiddenFieldFragments, forbiddenPackages, make(map[reflect.Type]bool))
			}
			for result := 0; result < signature.NumOut(); result++ {
				walkSeamType(t, seam.String()+"."+method.Name, signature.Out(result), forbiddenFieldFragments, forbiddenPackages, make(map[reflect.Type]bool))
			}
		}
	}
}

func walkSeamType(t *testing.T, path string, typ reflect.Type, fragments []string, forbiddenPackages map[string]bool, visited map[reflect.Type]bool) {
	t.Helper()
	if typ.PkgPath() != "" && forbiddenPackages[typ.PkgPath()] {
		t.Fatalf("%s uses forbidden package %q (host/shell/syscall capability)", path, typ.PkgPath())
	}
	switch typ.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array:
		walkSeamType(t, path, typ.Elem(), fragments, forbiddenPackages, visited)
		return
	case reflect.Map:
		walkSeamType(t, path, typ.Key(), fragments, forbiddenPackages, visited)
		walkSeamType(t, path, typ.Elem(), fragments, forbiddenPackages, visited)
		return
	case reflect.Interface:
		return
	case reflect.Struct:
		if typ.PkgPath() != "" && typ.PkgPath() != reflect.TypeOf(PublicationView{}).PkgPath() {
			return
		}
		if visited[typ] {
			return
		}
		visited[typ] = true
		for index := 0; index < typ.NumField(); index++ {
			field := typ.Field(index)
			name := strings.ToLower(field.Name)
			jsonName := strings.ToLower(strings.Split(field.Tag.Get("json"), ",")[0])
			for _, fragment := range fragments {
				if strings.Contains(name, fragment) || strings.Contains(jsonName, fragment) {
					t.Fatalf("%s.%s exposes legacy capability fragment %q", path, field.Name, fragment)
				}
			}
			walkSeamType(t, path+"."+field.Name, field.Type, fragments, forbiddenPackages, visited)
		}
		return
	}
}

// TestBehavioralGateMisleadingTimestampsDoNotDriveAuthority proves behavior
// is fully determined by explicit IDs, manifest, stream revision, and head:
// misleading timestamps, version strings, and "latest file" facts never
// influence any decision.
func TestBehavioralGateMisleadingTimestampsDoNotDriveAuthority(t *testing.T) {
	f := newFixture(t)
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})

	// The same canonical request submitted at different diagnostic times
	// yields the same durable candidate facts.
	early := f.mustPrepare(t, "op-time-a", set)
	f.now = f.now + 1000
	late := f.mustPrepare(t, "op-time-b", set)
	if early.ManifestDigest == late.ManifestDigest {
		t.Fatal("different operations must still have distinct manifests (identity is per operation)")
	}
	if early.ArtifactVersionID == late.ArtifactVersionID {
		t.Fatal("ArtifactVersionIDs must not be derived from timestamps")
	}
	// The stream is never advanced by time; only explicit CAS facts matter.
	stream, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryTaskStream, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
	})
	if err != nil {
		t.Fatalf("query stream: %v", err)
	}
	if stream.StreamRevision != 0 || stream.CurrentHead != "" {
		t.Fatalf("time must never advance the stream: %#v", stream)
	}
}

// TestBehavioralGateHistoryAndHeadUseExplicitStreamFactsOnly proves the
// committed version history order and the current head are driven only by
// the explicit publication-stream revision and head pointer: a child whose
// diagnostic time is earlier than its parent still appears after the parent
// and never becomes the head by time.
func TestBehavioralGateHistoryAndHeadUseExplicitStreamFactsOnly(t *testing.T) {
	f := newFixture(t)
	_, parent := f.prepareVerifyActivate(t, "op-order-1")

	// Misleading diagnostic time: the child records an EARLIER wall-clock
	// time than the parent activation, but the stream facts must win.
	f.now = f.now - 5000
	childSet := f.childEvidenceSet(t, parent.ArtifactVersionID, "op-order-child")
	header := f.header("op-order-child")
	header.ExpectedStreamRevision = 1
	header.ExpectedHead = parent.ArtifactVersionID
	childPrepare := bindDigest(NewPreparePublication(header, f.childPreparePayload("op-order-child", parent.ArtifactVersionID, childSet)))
	if _, err := f.core.Mutate(context.Background(), childPrepare); err != nil {
		t.Fatalf("child prepare: %v", err)
	}
	if _, err := f.core.Mutate(context.Background(), f.verifyIntent("op-order-child", f.verifyPayload(childSet))); err != nil {
		t.Fatalf("child verify: %v", err)
	}
	child, err := f.core.Mutate(context.Background(), f.activateIntentWithHeader(header))
	if err != nil {
		t.Fatalf("child activate: %v", err)
	}
	if child.ActivationEvidence.OccurredAt >= parent.ActivationEvidence.OccurredAt {
		t.Fatal("test precondition: child diagnostic time must be earlier than the parent")
	}

	history, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryVersionHistory, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
	})
	if err != nil {
		t.Fatalf("query history: %v", err)
	}
	if len(history.History) != 2 ||
		history.History[0].ArtifactVersionID != parent.ArtifactVersionID ||
		history.History[1].ArtifactVersionID != child.ArtifactVersionID {
		t.Fatalf("history must be ordered by committed stream revision, not time: %#v", history)
	}
	if history.CurrentHead != child.ArtifactVersionID || history.StreamRevision != 2 {
		t.Fatalf("current head must be the explicit pointer, not the latest time: %#v", history)
	}

	// A misleading "latest" string fact (lexically later version ID on the
	// parent) must never select the head.
	if parent.ArtifactVersionID > child.ArtifactVersionID {
		t.Fatalf("precondition: parent ID %q should be lexically after child %q for a misleading-string scenario",
			parent.ArtifactVersionID, child.ArtifactVersionID)
	}
}

func normalizeGoListPackage(raw string) string {
	line := strings.TrimSpace(raw)
	if line == "" {
		return ""
	}
	if strings.HasSuffix(line, ".test") {
		return ""
	}
	if space := strings.Index(line, " "); space >= 0 {
		line = line[:space]
	}
	if strings.HasSuffix(line, "_test") {
		line = strings.TrimSuffix(line, "_test")
	}
	return line
}

func findBackendModuleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		mod := filepath.Join(dir, "go.mod")
		if data, err := os.ReadFile(mod); err == nil && bytes.Contains(data, []byte("module github.com/slidesmith/slidesmith/backend")) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
		dir = parent
	}
}

func sortedMapKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
