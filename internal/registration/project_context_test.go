package registration

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	iam "go.miloapis.com/milo/pkg/apis/iam/v1alpha1"
	filters "go.miloapis.com/milo/pkg/server/filters"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/authentication/user"
	requestcontext "k8s.io/apiserver/pkg/endpoints/request"
)

func TestPinnedMiloRouteOverwritesCallerProjectExtras(t *testing.T) {
	for _, route := range []string{"datum-cloud", "unrelated-consumer"} {
		t.Run(route, func(t *testing.T) {
			caller := &user.DefaultInfo{Name: "operator", UID: "actor-1", Extra: map[string][]string{iam.ParentKindExtraKey: {"Organization"}, iam.ParentAPIGroupExtraKey: {"caller.invalid"}, iam.ParentNameExtraKey: {"forged", "datum-cloud"}}}
			called := false
			downstream := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				called = true
				actor, ok := requestcontext.UserFrom(request.Context())
				if !ok || actor.GetName() != caller.Name || actor.GetUID() != caller.UID {
					t.Fatal("caller identity was not preserved")
				}
				for key, expected := range map[string][]string{iam.ParentKindExtraKey: {"Project"}, iam.ParentAPIGroupExtraKey: {"resourcemanager.miloapis.com"}, iam.ParentNameExtraKey: {route}} {
					if !reflect.DeepEqual(actor.GetExtra()[key], expected) {
						t.Fatalf("routing did not establish trusted %s: %v", key, actor.GetExtra())
					}
				}
				writer.WriteHeader(http.StatusNoContent)
			})
			router := filters.ProjectRouterWithRequestInfo(filters.ProjectContextAuthorizationDecorator(downstream), &requestcontext.RequestInfoFactory{APIPrefixes: sets.NewString("api", "apis"), GrouplessAPIPrefixes: sets.NewString("api")})
			request := httptest.NewRequest(http.MethodPost, "/apis/resourcemanager.miloapis.com/v1alpha1/projects/"+route+"/control-plane/apis/fleet.datumapis.com/v1alpha1/namespaces/default/devices", nil)
			request = request.WithContext(requestcontext.WithUser(request.Context(), caller))
			router.ServeHTTP(httptest.NewRecorder(), request)
			if !called || !reflect.DeepEqual(caller.Extra[iam.ParentNameExtraKey], []string{"forged", "datum-cloud"}) {
				t.Fatal("routing failed or modified cached caller context")
			}
		})
	}
}

func TestPinnedMiloNestedScopesCannotProveUnrelatedProject(t *testing.T) {
	for _, nested := range []struct{ path, kind, group string }{
		{"resourcemanager.miloapis.com/v1alpha1/organizations/unrelated-consumer", "Organization", "resourcemanager.miloapis.com"},
		{"iam.miloapis.com/v1alpha1/users/unrelated-consumer", "User", "iam.miloapis.com"},
	} {
		t.Run(nested.kind, func(t *testing.T) {
			called := false
			downstream := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				called = true
				actor, ok := requestcontext.UserFrom(request.Context())
				if !ok || !reflect.DeepEqual(actor.GetExtra()[iam.ParentKindExtraKey], []string{nested.kind}) || !reflect.DeepEqual(actor.GetExtra()[iam.ParentAPIGroupExtraKey], []string{nested.group}) || !reflect.DeepEqual(actor.GetExtra()[iam.ParentNameExtraKey], []string{"unrelated-consumer"}) {
					t.Fatal("pinned Milo nested-scope behavior changed; reassess the admission trust boundary")
				}
				writer.WriteHeader(http.StatusNoContent)
			})
			decorated := filters.ProjectContextAuthorizationDecorator(filters.OrganizationContextAuthorizationDecorator(filters.UserContextAuthorizationDecorator(downstream)))
			routed := filters.UserContextHandler(filters.OrganizationContextHandler(decorated, nil), nil)
			router := filters.ProjectRouterWithRequestInfo(routed, &requestcontext.RequestInfoFactory{APIPrefixes: sets.NewString("api", "apis"), GrouplessAPIPrefixes: sets.NewString("api")})
			request := httptest.NewRequest(http.MethodPost, "/apis/resourcemanager.miloapis.com/v1alpha1/projects/datum-cloud/control-plane/apis/"+nested.path+"/control-plane/apis/fleet.datumapis.com/v1alpha1/namespaces/default/devices", nil)
			request = request.WithContext(requestcontext.WithUser(request.Context(), &user.DefaultInfo{Name: "operator", UID: "actor-1"}))
			router.ServeHTTP(httptest.NewRecorder(), request)
			if !called {
				t.Fatal("nested route did not reach the scope decorators")
			}
		})
	}
}
