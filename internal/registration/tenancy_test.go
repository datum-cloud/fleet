package registration

import (
	"context"
	"strings"
	"testing"

	"go.datum.net/fleet/internal/controller"
	iam "go.miloapis.com/milo/pkg/apis/iam/v1alpha1"
	authenticationv1 "k8s.io/api/authentication/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// tripwire is a project client that fails the test on any read or write, so
// a test can prove that a decision was reached before Devices, capacity or a
// SubjectAccessReview were touched.
func tripwire(t *testing.T, why string) client.Client {
	t.Helper()
	fail := func(operation string) error {
		t.Errorf("%s: project client %s was called", why, operation)
		return context.Canceled
	}
	return interceptor.NewClient(fake.NewClientBuilder().WithScheme(testScheme(t)).Build(), interceptor.Funcs{
		Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			return fail("Get")
		},
		List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
			return fail("List")
		},
		Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error {
			return fail("Create")
		},
	})
}

// tripwireAuthorize fails the test if a SubjectAccessReview is attempted.
func tripwireAuthorize(t *testing.T, why string) func(context.Context, Project, authenticationv1.UserInfo, string, string, string) error {
	return func(_ context.Context, _ Project, _ authenticationv1.UserInfo, _, resource, _ string) error {
		t.Errorf("%s: SubjectAccessReview for %s was attempted", why, resource)
		return context.Canceled
	}
}

// decisionPaths are the two admission paths that resolve a project: a
// registration create, and an update that tries to leave Disabled.
func decisionPaths(t *testing.T) map[string]admission.Request {
	t.Helper()
	_, device := fixture(t)
	return map[string]admission.Request{
		"create":          registrationRequest(t, device),
		"enabling update": capacityUpdate(t, device, "Enabled"),
	}
}

// The provider-project check is the only gate on which project a request is
// judged in: Resolve, the webhook match conditions and the
// SubjectAccessReview all take the project from the request. A request
// routed through a project outside the configured scope must be denied
// before any of them run. The Resolve stub here behaves like the real one
// and resolves whatever name it is given, so no later check can mask a
// missing scope guard.
func TestOutOfScopeProjectIsDeniedBeforeAnyRead(t *testing.T) {
	for path, request := range decisionPaths(t) {
		t.Run(path, func(t *testing.T) {
			why := "out-of-scope project"
			handler := &Handler{Projects: []string{"provider"}, Resolve: func(_ context.Context, name string) (Project, error) {
				t.Errorf("%s: Resolve(%q) was called", why, name)
				return Project{Identity: controller.ProjectIdentity{AuthorityID: "milo", RouteName: name, UID: types.UID("project-" + name)}, Client: tripwire(t, why)}, nil
			}, Authorize: tripwireAuthorize(t, why)}
			request.UserInfo.Extra = map[string]authenticationv1.ExtraValue{iam.ParentKindExtraKey: {"Project"}, iam.ParentAPIGroupExtraKey: {"resourcemanager.miloapis.com"}, iam.ParentNameExtraKey: {"consumer"}}
			response := handler.Handle(context.Background(), request)
			if response.Allowed || !strings.Contains(response.Result.Message, "outside the configured provider registration scope") {
				t.Fatalf("out-of-scope project: %+v", response.Result)
			}
		})
	}
}

// Resolve must return the project that was asked for. If it returns a
// different route, the request is denied before the returned project's
// client is used for a Device read or a SubjectAccessReview.
func TestResolvedRouteMismatchIsDenied(t *testing.T) {
	for path, request := range decisionPaths(t) {
		t.Run(path, func(t *testing.T) {
			why := "mismatched route"
			handler := &Handler{Projects: []string{"provider"}, Resolve: func(context.Context, string) (Project, error) {
				return Project{Identity: controller.ProjectIdentity{AuthorityID: "milo", RouteName: "elsewhere", UID: "project-elsewhere"}, Client: tripwire(t, why)}, nil
			}, Authorize: tripwireAuthorize(t, why)}
			response := handler.Handle(context.Background(), request)
			if response.Allowed || response.Result.Message != "provider project lifetime is unavailable" {
				t.Fatalf("mismatched route: %+v", response.Result)
			}
		})
	}
}
