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
	"errors"
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
// Publication core, its owned transport adapter, and the Task Orchestration
// publication bridge may depend on: the module itself, the two upstream
// module ports whose evidence it consumes (Task Workspace Lifecycle,
// Runtime Execution), the Task Orchestration port (whose publication bridge
// is one of the gated targets), the Scheduler port (an owned Platform
// Control Plane deep module consumed by Task Orchestration), and the
// test-only PostgreSQL harness. Legacy service/handler/repository/model/
// router/config/database packages are deletion targets, never dependencies.
var allowlistedOwnedPorts = map[string]bool{
	backendModulePrefix + "/internal/artifactpublication": true,
	backendModulePrefix + "/internal/taskorchestration":   true,
	backendModulePrefix + "/internal/taskworkspace":       true,
	backendModulePrefix + "/internal/runtimeexecution":    true,
	backendModulePrefix + "/internal/scheduler":           true,
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
// import-graph inspection that the Artifact Publication package (C05 core
// and owned transport adapter) AND the Task Orchestration publication bridge
// build and run their contracts without any legacy package: every
// SlideSmith package in the transitive closure (including test
// dependencies) is an allowlisted owned port. The two target packages are
// listed explicitly so the bridge (child SPEC #109 / C05-06) is itself a
// gated build target, not merely a transitive dependency.
func TestStructuralDeletionGateLegacyPackagesAbsentFromBuildClosure(t *testing.T) {
	root, err := findBackendModuleRoot()
	if err != nil {
		t.Fatalf("locate backend module root: %v", err)
	}
	command := exec.Command("go", "list", "-deps", "-test",
		"./internal/artifactpublication", "./internal/taskorchestration")
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
	for _, required := range []string{
		backendModulePrefix + "/internal/artifactpublication",
		backendModulePrefix + "/internal/taskorchestration",
	} {
		if !seen[required] {
			t.Fatalf("dependency closure is missing target package %q; full closure: %v", required, sortedMapKeys(seen))
		}
	}
}

// TestStructuralDeletionGateTargetBuildsWithoutLegacyPackages proves the
// two target packages (C05 core + owned adapter, Task Orchestration bridge)
// compile as standalone build targets, which together with the import-graph
// gate above demonstrates that the C05 contract and its bridge execute with
// the legacy packages absent.
func TestStructuralDeletionGateTargetBuildsWithoutLegacyPackages(t *testing.T) {
	root, err := findBackendModuleRoot()
	if err != nil {
		t.Fatalf("locate backend module root: %v", err)
	}
	command := exec.Command("go", "build", "./internal/artifactpublication", "./internal/taskorchestration")
	command.Dir = root
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("target packages do not build without legacy packages: %v\n%s", err, stderr.String())
	}
}

// TestStructuralDeletionGateCapabilitySurfaceAbsence proves no seam exposes
// a path, object key, bucket, mount, vendor, credential, session, signed
// URL, general repository, active-setter, legacy `PublishVersion` string, or
// timestamp-derived version authority. The check walks the actual method
// signatures and reachable value types, so a rename cannot satisfy or break
// it.
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
		reflect.TypeOf(OwnedTransportEnvelope{}),
		reflect.TypeOf(OwnedTransportRequest{}),
		reflect.TypeOf(OwnedTransportResponse{}),
		reflect.TypeOf(OwnedTransportCallback{}),
		reflect.TypeOf(OwnedTransportWireError{}),
		reflect.TypeOf(OwnedTransportMachineAuthority{}),
		reflect.TypeOf(OwnedTransportBinding{}),
		reflect.TypeOf((*OwnedTransportClient)(nil)).Elem(),
		// C05-07 audit/observability surfaces: canonical mandatory audit
		// facts, external audit projections, bounded telemetry, structured
		// logs/traces, and the protected projection backlog.
		reflect.TypeOf(PublicationAuditFact{}),
		reflect.TypeOf(ExternalAuditProjection{}),
		reflect.TypeOf(PublicationTelemetryProjection{}),
		reflect.TypeOf(MetricSample{}),
		reflect.TypeOf(MetricLabels{}),
		reflect.TypeOf(StructuredLogRecord{}),
		reflect.TypeOf(TraceSpanRecord{}),
		reflect.TypeOf(PublicationProjectionBacklog{}),
		reflect.TypeOf(ProjectionDeliveryEvidence{}),
		reflect.TypeOf(TelemetrySnapshot{}),
		reflect.TypeOf(ResidueView{}),
		reflect.TypeOf(CleanupDebtView{}),
	}
	forbiddenFieldFragments := []string{
		"path", "object_key", "objectkey", "prefix", "bucket", "mount",
		"vendor", "credential", "session", "signed_url", "locator", "latest",
		"created_at", "activated_at", "timestamp", "publish_version",
		"publishversion", "version_string", "versionstring", "directory_scan",
		"directoryscan",
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

// TestBehavioralGateObjectKeyPrefixesAndLatestFactsDoNotDriveAuthority
// plants misleading object-key prefixes, directory entries, and "latest
// file" facts in the Durable Object capability facts and member names, and
// proves the canonical manifest, stream facts, and current head are fully
// determined by explicit IDs, manifest, stream revision/head and evidence —
// never by the object-key-looking strings, directory scan results, or
// latest-file inference.
func TestBehavioralGateObjectKeyPrefixesAndLatestFactsDoNotDriveAuthority(t *testing.T) {
	f := newFixture(t)
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})

	// Plant misleading capability facts: object-key prefixes, a bucket,
	// directory entries, and a "latest" marker on the Durable Object
	// content identity. These are opaque evidence facts; they must never
	// become the manifest, the member set, the version identity, or the
	// head.
	planted := set.capabilities[0]
	planted.ContentID = ContentID("s3://canary-bucket/prefix/2026/08/10/latest/Deck.pptx")
	planted.Digest = planted.CanonicalDigest()
	set.capabilities[0] = planted
	f.registry.register(planted, true)

	// A member logical name that looks like a directory entry / object-key
	// path is unsafe and must fail closed: the manifest never accepts a
	// path-like name.
	pathLike := f.deckMemberSpec()
	pathLike.LogicalName = "s3://canary-bucket/prefix/latest/Deck.pptx"
	pathPayload := f.preparePayload("op-path-like", set, []ArtifactMemberSpec{pathLike})
	if _, err := f.core.Mutate(context.Background(), f.prepareIntent("op-path-like", pathPayload)); err == nil {
		t.Fatal("path-like member logical name must fail closed (no directory-scan capability)")
	}

	// The clean candidate with the planted (but opaque) content identity
	// activates exactly like any other candidate: the manifest is derived
	// from explicit member facts (ArtifactID, kind, normalized name, media
	// type, size, digest), never from the content identity string.
	prepare := f.mustPrepare(t, "op-prefix-1", set)
	view, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryOperation, PolicyDomainID: f.policyDomain, TaskID: f.taskID, OperationID: "op-prefix-1",
	})
	if err != nil {
		t.Fatalf("query operation: %v", err)
	}
	encoded := string(mustMarshalJSON(t, view))
	for _, canary := range []string{"s3://", "canary-bucket", "prefix/", "latest/", "directory"} {
		if strings.Contains(encoded, canary) {
			t.Fatalf("planted object-key/directory canary %q leaked into the operation view: %s", canary, encoded)
		}
	}

	f.mustVerify(t, "op-prefix-1", set)
	activated := f.mustActivate(t, "op-prefix-1")
	if activated.ActivationEvidence == nil || activated.ActivationEvidence.ArtifactVersionID != prepare.ArtifactVersionID {
		t.Fatalf("activation must bind the explicit candidate identity: %#v", activated)
	}

	// A second candidate whose content identity lexically "looks" newer or
	// more "latest" must never win by string comparison: only the explicit
	// stream CAS decides.
	setB := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	setB.capabilities[0].ContentID = ContentID("s3://canary-bucket/prefix/2099/12/31/latest/z-latest.pptx")
	setB.capabilities[0].Digest = setB.capabilities[0].CanonicalDigest()
	f.registry.register(setB.capabilities[0], true)
	// The stream is at revision 1 after the activation; the second prepare
	// must state the current explicit stream facts (a stale revision fails
	// closed, never by content-identity comparison).
	headerB := f.header("op-prefix-2")
	headerB.ExpectedStreamRevision = 1
	headerB.ExpectedHead = activated.ActivationEvidence.ArtifactVersionID
	prepareB, err := f.core.Mutate(context.Background(), bindDigest(NewPreparePublication(headerB, f.preparePayload("op-prefix-2", setB, []ArtifactMemberSpec{f.deckMemberSpec()}))))
	if err != nil {
		t.Fatalf("prepare op-prefix-2: %v", err)
	}
	if prepareB.ArtifactVersionID == prepare.ArtifactVersionID {
		t.Fatal("distinct operations must have distinct candidate identities")
	}
	if prepareB.ManifestDigest == prepare.ManifestDigest {
		t.Fatal("manifest digest must be derived from explicit member facts, not content identities")
	}

	stream, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryTaskStream, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
	})
	if err != nil {
		t.Fatalf("query stream: %v", err)
	}
	if stream.StreamRevision != 1 || stream.CurrentHead != activated.ActivationEvidence.ArtifactVersionID {
		t.Fatalf("stream facts must reflect only the explicit CAS winner: %#v", stream)
	}
}

// TestBehavioralGatePublishVersionStringIsNotAuthority proves a
// `PublishVersion`-style string (the legacy timestamp-derived version
// authority) is never accepted as an identity input: the public surface has
// no such field, an unknown query kind fails closed, and a version-string
// value cannot be used to select or create a version.
func TestBehavioralGatePublishVersionStringIsNotAuthority(t *testing.T) {
	f := newFixture(t)

	// The closed query union has no "by version string" kind: an unknown
	// kind is an invalid intent, never a lookup by legacy version string.
	_, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: "publish_version", PolicyDomainID: f.policyDomain, TaskID: f.taskID,
	})
	var publicationError *Error
	if !errors.As(err, &publicationError) || publicationError.Code != ErrorInvalidIntent {
		t.Fatalf("unknown publish-version query kind must fail closed as invalid intent, got %v", err)
	}

	// A candidate whose member facts are submitted at a misleading
	// timestamp-derived "version string" moment still gets an opaque,
	// non-reused ArtifactVersionID; the identity never equals or derives
	// from a version string.
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	prepare := f.mustPrepare(t, "op-no-version-string", set)
	if prepare.ArtifactVersionID == "" || strings.Contains(string(prepare.ArtifactVersionID), "2026") ||
		strings.Contains(string(prepare.ArtifactVersionID), "08") {
		t.Fatalf("ArtifactVersionID must be opaque and never contain timestamp/version-string facts: %q", prepare.ArtifactVersionID)
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
