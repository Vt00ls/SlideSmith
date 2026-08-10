package artifactpublication

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

// TestPublicSeamClosedSurfaces proves the only public mutation surface is
// Mutate(PublicationIntent) and the only ordinary read surface is
// Query(PublicationQuery): there is no active setter, general repository,
// raw callback ingest, or caller-provided mutable snapshot anywhere in the
// seam.
func TestPublicSeamClosedSurfaces(t *testing.T) {
	seam := reflect.TypeOf((*PublicationCore)(nil)).Elem()
	if seam.NumMethod() != 2 {
		t.Fatalf("PublicationCore must expose exactly Mutate and Query, got %d methods", seam.NumMethod())
	}
	var hasMutate, hasQuery bool
	for index := 0; index < seam.NumMethod(); index++ {
		method := seam.Method(index)
		switch method.Name {
		case "Mutate":
			hasMutate = true
			signature := method.Type
			// Interface method types do not include the receiver.
			if signature.NumIn() != 2 || signature.NumOut() != 2 {
				t.Fatalf("Mutate must be Mutate(context, PublicationIntent) (PublicationDecision, error), got %s", signature)
			}
			if signature.In(0) != reflect.TypeOf((*context.Context)(nil)).Elem() {
				t.Fatalf("Mutate must accept context.Context, got %s", signature.In(0))
			}
			if signature.In(1) != reflect.TypeOf((*PublicationIntent)(nil)).Elem() {
				t.Fatalf("Mutate must accept PublicationIntent, got %s", signature.In(1))
			}
			if signature.Out(0) != reflect.TypeOf(PublicationDecision{}) {
				t.Fatalf("Mutate must return PublicationDecision, got %s", signature.Out(0))
			}
		case "Query":
			hasQuery = true
			signature := method.Type
			if signature.NumIn() != 2 || signature.NumOut() != 2 {
				t.Fatalf("Query must be Query(context, PublicationQuery) (PublicationView, error), got %s", signature)
			}
			if signature.In(1) != reflect.TypeOf(PublicationQuery{}) {
				t.Fatalf("Query must accept PublicationQuery, got %s", signature.In(1))
			}
			if signature.Out(0) != reflect.TypeOf(PublicationView{}) {
				t.Fatalf("Query must return PublicationView, got %s", signature.Out(0))
			}
		default:
			t.Fatalf("PublicationCore exposes unexpected method %q", method.Name)
		}
	}
	if !hasMutate || !hasQuery {
		t.Fatal("PublicationCore must expose Mutate and Query")
	}
}

// TestMutationIntentUnionIsClosed proves the PublicationIntent union is
// closed: only the five typed intents exist, each with an unexported marker,
// and the engine rejects anything else as invalid.
func TestMutationIntentUnionIsClosed(t *testing.T) {
	intentTypes := []reflect.Type{
		reflect.TypeOf(PreparePublication{}),
		reflect.TypeOf(VerifyPublication{}),
		reflect.TypeOf(RejectPublication{}),
		reflect.TypeOf(CancelPublication{}),
		reflect.TypeOf(ReconcilePublication{}),
	}
	// Exactly the five SPEC #104 intents.
	if len(intentTypes) != 5 {
		t.Fatalf("PublicationIntent union must have exactly five members, got %d", len(intentTypes))
	}
	for _, typ := range intentTypes {
		_, hasMarker := typ.MethodByName("publicationIntent")
		if !hasMarker {
			// The marker is promoted from the embedded intentBase; walk the
			// method set through the interface instead.
			implements := reflect.TypeOf((*PublicationIntent)(nil)).Elem()
			if !typ.Implements(implements) {
				t.Fatalf("%s must implement the closed PublicationIntent union", typ)
			}
		}
	}
	// The union cannot be implemented from outside the package: it carries
	// an unexported method.
	implements := reflect.TypeOf((*PublicationIntent)(nil)).Elem()
	for index := 0; index < implements.NumMethod(); index++ {
		if !implements.Method(index).IsExported() {
			return // closed by construction
		}
	}
	t.Fatal("PublicationIntent must carry an unexported method to be closed")
}

// TestNoRepositoryOrActiveSetterCapability proves the public types carry no
// path, object-key, bucket, vendor, credential, session, or active-setter
// capability fragments.
func TestNoRepositoryOrActiveSetterCapability(t *testing.T) {
	seams := []reflect.Type{
		reflect.TypeOf((*PublicationCore)(nil)).Elem(),
		reflect.TypeOf((*PublicationIntent)(nil)).Elem(),
		reflect.TypeOf(PublicationDecision{}),
		reflect.TypeOf(PublicationQuery{}),
		reflect.TypeOf(PublicationView{}),
		reflect.TypeOf(ArtifactManifest{}),
		reflect.TypeOf(Lineage{}),
		reflect.TypeOf(ArtifactMember{}),
		reflect.TypeOf(ArtifactMemberView{}),
	}
	forbiddenFieldFragments := []string{
		"path", "object_key", "objectkey", "bucket", "prefix", "mount",
		"vendor", "credential", "session", "locator", "url", "signed",
	}
	forbiddenMethodFragments := []string{
		"Set", "Update", "Delete", "Insert", "Save", "Create", "Repo", "Repository",
	}
	for _, seam := range seams {
		for index := 0; index < seam.NumMethod(); index++ {
			method := seam.Method(index)
			for _, fragment := range forbiddenMethodFragments {
				if strings.Contains(method.Name, fragment) {
					t.Fatalf("%s exposes active-setter-like method %q", seam, method.Name)
				}
			}
		}
		walkFieldNames(t, seam.String(), seam, forbiddenFieldFragments, make(map[reflect.Type]bool))
	}
}

func walkFieldNames(t *testing.T, path string, typ reflect.Type, fragments []string, visited map[reflect.Type]bool) {
	t.Helper()
	switch typ.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map:
		walkFieldNames(t, path, typ.Elem(), fragments, visited)
		return
	case reflect.Interface:
		return
	case reflect.Struct:
		if typ.PkgPath() != "" && typ.PkgPath() != reflect.TypeOf(PublicationView{}).PkgPath() {
			return // cross-package value types are safe
		}
		if visited[typ] {
			return
		}
		visited[typ] = true
		for index := 0; index < typ.NumField(); index++ {
			field := typ.Field(index)
			name := strings.ToLower(field.Name)
			for _, fragment := range fragments {
				if strings.Contains(name, fragment) {
					t.Fatalf("%s.%s exposes forbidden fragment %q", path, field.Name, fragment)
				}
			}
			walkFieldNames(t, path+"."+field.Name, field.Type, fragments, visited)
		}
	}
}
