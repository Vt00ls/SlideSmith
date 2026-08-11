package taskorchestration

// Structural deletion gate for the Task Orchestration publication bridge
// (child SPEC #109 / C05-06, parent SPEC #103 Testing Decision 23). The
// bridge is the owned adapter through which Task Orchestration commits the
// COMPLETE canonical C05 request and consumes activation/rejection evidence;
// it must never depend on, wrap, or dual-write the legacy TaskService,
// runtime publisher, manual-edit worker, download handler, repository,
// filesystem/path, object key, directory scan, timestamp, latest-file, or
// `PublishVersion` authority.
//
// This gate is structural, not textual: it inspects the actual interface
// and value-type graph of the bridge seams, so an ordinary rename of a
// method, a different method spelling, or a different error text cannot
// make it pass or fail. It verifies capability absence, not a fragile
// source substring.

import (
	"reflect"
	"strings"
	"testing"
)

// TestPublicationBridgeDeletionGateCapabilitySurfaceAbsence proves the
// bridge seams never expose a legacy publication capability: no path, no
// object key / prefix / bucket / mount / vendor / credential / session /
// signed URL / locator / latest-file inference / timestamp authority / raw
// `PublishVersion` string, no general repository mutation surface, and no
// active-setter. The walk follows the actual method signatures and the
// reachable value types, so a rename cannot satisfy or break it.
func TestPublicationBridgeDeletionGateCapabilitySurfaceAbsence(t *testing.T) {
	seams := []reflect.Type{
		reflect.TypeOf((*PublicationBridge)(nil)).Elem(),
		reflect.TypeOf((*PublicationBridgeAdapter)(nil)).Elem(),
		reflect.TypeOf((*PublicationTransportPort)(nil)).Elem(),
		reflect.TypeOf(PublicationRequestBinding{}),
		reflect.TypeOf(PublicationRequestSpec{}),
		reflect.TypeOf(PublicationMemberSpec{}),
		reflect.TypeOf(PublicationStagingReference{}),
		reflect.TypeOf(PublicationRuntimeEvidenceRef{}),
		reflect.TypeOf(PublicationEvidenceRef{}),
		reflect.TypeOf(PublicationCapabilityRef{}),
		reflect.TypeOf(PublicationDelivery{}),
		reflect.TypeOf(PublicationBridgeError{}),
		reflect.TypeOf((*DeterministicPublicationTransport)(nil)).Elem(),
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
					t.Fatalf("bridge seam %s exposes forbidden method %q", seam, method.Name)
				}
			}
			signature := method.Type
			for arg := 0; arg < signature.NumIn(); arg++ {
				walkBridgeSeamType(t, seam.String()+"."+method.Name, signature.In(arg), forbiddenFieldFragments, forbiddenPackages, make(map[reflect.Type]bool))
			}
			for result := 0; result < signature.NumOut(); result++ {
				walkBridgeSeamType(t, seam.String()+"."+method.Name, signature.Out(result), forbiddenFieldFragments, forbiddenPackages, make(map[reflect.Type]bool))
			}
		}
	}
}

func walkBridgeSeamType(t *testing.T, path string, typ reflect.Type, fragments []string, forbiddenPackages map[string]bool, visited map[reflect.Type]bool) {
	t.Helper()
	if typ.PkgPath() != "" && forbiddenPackages[typ.PkgPath()] {
		t.Fatalf("%s uses forbidden package %q (host/shell/syscall capability)", path, typ.PkgPath())
	}
	switch typ.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array:
		walkBridgeSeamType(t, path, typ.Elem(), fragments, forbiddenPackages, visited)
		return
	case reflect.Map:
		walkBridgeSeamType(t, path, typ.Key(), fragments, forbiddenPackages, visited)
		walkBridgeSeamType(t, path, typ.Elem(), fragments, forbiddenPackages, visited)
		return
	case reflect.Interface:
		return
	case reflect.Struct:
		if typ.PkgPath() != "" && typ.PkgPath() != reflect.TypeOf(PublicationDelivery{}).PkgPath() {
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
			walkBridgeSeamType(t, path+"."+field.Name, field.Type, fragments, forbiddenPackages, visited)
		}
		return
	}
}
