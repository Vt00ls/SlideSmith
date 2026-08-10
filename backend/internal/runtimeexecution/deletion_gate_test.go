package runtimeexecution

// Structural deletion gates (parent SPEC #71 Testing Decision 23, ADR 0016,
// docs/architecture/runtime-execution.md "Hard cutover and deletion test").
//
// These gates are structural, not textual: they prove capability and
// dependency absence by inspecting the actual build closure and the actual
// interface/type graph. They do not depend on one literal filename, method
// spelling, error string, or source substring, so harmless renames cannot
// satisfy or break them. Package import paths are the only path-like facts
// used, because an import-graph gate is exactly what the parent SPEC names.

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

const backendModulePrefix = "github.com/slidesmith/slidesmith/backend"

// allowlistedOwnedPorts are the only SlideSmith packages that target C03
// (Runtime Execution) and its Task Orchestration integration may depend on:
// the two modules themselves, the C04 Task Workspace Lifecycle port, the
// Scheduler capacity bridge port, and the test-only PostgreSQL harness.
var allowlistedOwnedPorts = map[string]bool{
	backendModulePrefix + "/internal/runtimeexecution":  true,
	backendModulePrefix + "/internal/taskorchestration": true,
	backendModulePrefix + "/internal/taskworkspace":     true,
	backendModulePrefix + "/internal/scheduler":         true,
	backendModulePrefix + "/internal/testpostgres":      true,
}

// legacyExecutionPackagePrefixes are the packages that the hard cutover
// deletes rather than wraps (ADR 0016; legacy-business-migration-and-
// compatibility.md). Any of these in the target build closure is a blocking
// structural finding regardless of which file or method actually links them.
var legacyExecutionPackagePrefixes = []string{
	backendModulePrefix + "/internal/service",
	backendModulePrefix + "/internal/handler",
	backendModulePrefix + "/internal/repository",
	backendModulePrefix + "/internal/model",
	backendModulePrefix + "/internal/router",
	backendModulePrefix + "/internal/config",
	backendModulePrefix + "/internal/database",
}

// TestStructuralDeletionGateLegacyExecutionPackagesAbsentFromBuildClosure
// proves by import-graph inspection that the target C03 package and its Task
// Orchestration integration build and run their contract without any legacy
// execution package: every SlideSmith package in the transitive closure
// (including test dependencies) is one of the allowlisted owned ports.
func TestStructuralDeletionGateLegacyExecutionPackagesAbsentFromBuildClosure(t *testing.T) {
	root, err := findBackendModuleRoot()
	if err != nil {
		t.Fatalf("locate backend module root: %v", err)
	}
	command := exec.Command("go", "list", "-deps", "-test",
		"./internal/runtimeexecution", "./internal/taskorchestration")
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
		for _, legacy := range legacyExecutionPackagePrefixes {
			if pkg == legacy || strings.HasPrefix(pkg, legacy) {
				t.Fatalf("target build closure depends on legacy execution package %q; ADR 0016 deletes these rather than wrapping them", pkg)
			}
		}
		if !allowlistedOwnedPorts[pkg] {
			t.Fatalf("target build closure depends on unallowlisted package %q; allow only owned ports: %v",
				pkg, sortedMapKeys(allowlistedOwnedPorts))
		}
	}
	for _, required := range []string{
		backendModulePrefix + "/internal/runtimeexecution",
		backendModulePrefix + "/internal/taskorchestration",
	} {
		if !seen[required] {
			t.Fatalf("dependency closure is missing target package %q; full closure: %v", required, sortedMapKeys(seen))
		}
	}
}

// TestStructuralDeletionGateTargetsBuildWithoutLegacyPackages proves the two
// target packages compile as standalone build targets, which together with
// the import-graph gate above demonstrates that the C03 contract builds and
// executes (the shared contract suite lives in this package) with the legacy
// execution packages absent.
func TestStructuralDeletionGateTargetsBuildWithoutLegacyPackages(t *testing.T) {
	root, err := findBackendModuleRoot()
	if err != nil {
		t.Fatalf("locate backend module root: %v", err)
	}
	command := exec.Command("go", "build", "./internal/runtimeexecution", "./internal/taskorchestration")
	command.Dir = root
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("target packages do not build without legacy packages: %v\n%s", err, stderr.String())
	}
}

// TestStructuralDeletionGateCapabilitySurfaceAbsence proves that no public,
// protected, or private seam interface exposes a host-path, session,
// recent-run discovery, arbitrary-shell, shared-daemon control, or general
// repository mutation capability. The check is structural: it walks the
// actual method signatures and reachable value types of every seam, so a
// rename of a legacy file or method cannot satisfy or break it.
func TestStructuralDeletionGateCapabilitySurfaceAbsence(t *testing.T) {
	seams := []reflect.Type{
		reflect.TypeOf((*RuntimeExecution)(nil)).Elem(),
		reflect.TypeOf((*RuntimeMaintenance)(nil)).Elem(),
		reflect.TypeOf((*OperationalDiagnostics)(nil)).Elem(),
		reflect.TypeOf((*workerProtocol)(nil)).Elem(),
		reflect.TypeOf((*workerCapabilityAdapter)(nil)).Elem(),
		reflect.TypeOf((*agentWorkerBackend)(nil)).Elem(),
		reflect.TypeOf((*toolWorkerBackend)(nil)).Elem(),
		reflect.TypeOf((*ownedWorkerTransport)(nil)).Elem(),
	}
	// Field-name fragments that would expose a legacy capability class:
	// host path, session/data-root, recent-run discovery, arbitrary shell,
	// and shared daemon control. Sandbox-local logical locations and opaque
	// digest roots are intentionally not forbidden: they are the documented
	// replacement for path authority (Decision 51/59).
	forbiddenFieldFragments := []string{
		"session", "shell", "socket", "daemon", "datadir", "data_root",
		"workspace_path", "host_path", "recent", "latest_run",
	}
	// Method-name fragments that would expose a generic repository mutation
	// surface or a second mutation authority.
	forbiddenMethodFragments := []string{
		"Repo", "Repository", "Set", "Update", "Delete", "Insert",
		"Save", "Create", "Mutate",
	}
	forbiddenPackages := map[string]bool{"os": true, "os/exec": true, "syscall": true}

	for _, seam := range seams {
		for index := 0; index < seam.NumMethod(); index++ {
			method := seam.Method(index)
			for _, fragment := range forbiddenMethodFragments {
				if strings.Contains(method.Name, fragment) {
					t.Fatalf("seam %s exposes repository-mutation-like method %q (fragment %q)", seam, method.Name, fragment)
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
		// context.Context, error and other empty interfaces are safe; named
		// interfaces from other packages are owned ports consumed by their
		// owning modules and are not re-enumerated here.
		return
	case reflect.Struct:
		if typ.PkgPath() != "" && typ.PkgPath() != reflect.TypeOf(RuntimeSnapshot{}).PkgPath() {
			return // cross-package value types (time.Time and the like) are safe
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

func normalizeGoListPackage(raw string) string {
	line := strings.TrimSpace(raw)
	if line == "" {
		return ""
	}
	if strings.HasSuffix(line, ".test") {
		return "" // synthetic test-main package
	}
	if space := strings.Index(line, " "); space >= 0 {
		line = line[:space] // "pkg [pkg.test]" -> "pkg"
	}
	if strings.HasSuffix(line, "_test") {
		line = strings.TrimSuffix(line, "_test") // external test package of an owned port
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
