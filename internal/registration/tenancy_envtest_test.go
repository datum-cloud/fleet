package registration

import (
	"context"
	"sync/atomic"
	"testing"

	metal "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
	iam "go.miloapis.com/milo/pkg/apis/iam/v1alpha1"
	authenticationv1 "k8s.io/api/authentication/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TestWebhookJudgesOnlyTheConfiguredProject runs the provider-project scope
// check and the resolved-route check behind the packaged webhook. The
// handler's Resolve resolves any name it is given, as the production one
// does, and its project client and SubjectAccessReview fail the test when a
// request outside the configured scope, or with a mismatched route, reaches
// them.
func TestWebhookJudgesOnlyTheConfiguredProject(t *testing.T) {
	api := startAPI(t)
	ctx := context.Background()
	registeredDevice := api.device("tenancy-registered", "default", "tenancy-serial-1", "77777777-7777-7777-7777-777777777781")
	candidate := api.device("tenancy-candidate", "default", "tenancy-serial-2", "77777777-7777-7777-7777-777777777782")

	var mismatched atomic.Bool
	outOfScope := tripwire(t, "out-of-scope project")
	misrouted := tripwire(t, "mismatched route")
	api.installHandler(&Handler{Projects: []string{"datum-cloud"}, Resolve: func(_ context.Context, name string) (Project, error) {
		if name != "datum-cloud" {
			t.Errorf("Resolve(%q) was called for a project outside the scope", name)
			return Project{Identity: controller.ProjectIdentity{AuthorityID: "milo", RouteName: name, UID: types.UID("project-" + name)}, Client: outOfScope}, nil
		}
		if mismatched.Load() {
			return Project{Identity: controller.ProjectIdentity{AuthorityID: "milo", RouteName: "elsewhere", UID: "project-elsewhere"}, Client: misrouted}, nil
		}
		return Project{Identity: controller.ProjectIdentity{AuthorityID: "milo", RouteName: "datum-cloud", UID: "project-1"}, Client: api.admin}, nil
	}, Authorize: func(ctx context.Context, project Project, actor authenticationv1.UserInfo, group, resource, name string) error {
		if values := actor.Extra[iam.ParentNameExtraKey]; len(values) != 1 || values[0] != "datum-cloud" {
			t.Errorf("SubjectAccessReview for %s was attempted for project %v", resource, values)
			return context.Canceled
		}
		if mismatched.Load() {
			t.Errorf("SubjectAccessReview for %s was attempted after a mismatched route", resource)
			return context.Canceled
		}
		return Authorize(ctx, project, actor, group, resource, name)
	}})
	operator := api.actor("operator", "system:masters")
	registered := api.registration(registeredDevice)
	if err := operator.Create(ctx, registered); err != nil {
		t.Fatalf("in-scope registration denied: %v", err)
	}
	enable := func(actor client.Client) error {
		current := &metal.MachineCapacity{}
		if err := api.admin.Get(ctx, client.ObjectKeyFromObject(registered), current); err != nil {
			t.Fatal(err)
		}
		current.Spec.AllocationPolicy = "Enabled"
		current.Spec.CleaningPolicyRef.Name = "invented-policy"
		return actor.Update(ctx, current)
	}

	// The outsider is cluster-admin in the API server, so only the webhook's
	// scope check stands between it and a verdict about Devices.
	outsider := api.actorIn("other-provider", "outsider", "system:masters")
	t.Run("a project outside the scope cannot register", func(t *testing.T) {
		capacity := api.registration(candidate)
		requireDenied(t, outsider.Create(ctx, capacity), "outside the configured provider registration scope")
		if api.exists(capacity) {
			t.Fatal("out-of-scope registration persisted")
		}
	})
	t.Run("a project outside the scope cannot learn why capacity stays Disabled", func(t *testing.T) {
		requireDenied(t, enable(outsider), "outside the configured provider registration scope")
	})

	mismatched.Store(true)
	t.Run("a mismatched route denies registration", func(t *testing.T) {
		capacity := api.registration(candidate)
		requireDenied(t, operator.Create(ctx, capacity), "provider project lifetime is unavailable")
		if api.exists(capacity) {
			t.Fatal("misrouted registration persisted")
		}
	})
	t.Run("a mismatched route denies an enabling update", func(t *testing.T) {
		requireDenied(t, enable(operator), "provider project lifetime is unavailable")
	})
}
