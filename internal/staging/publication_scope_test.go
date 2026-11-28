package staging

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	"go.datum.net/fleet/internal/allocation"
	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
	"go.datum.net/fleet/internal/manager"
)

var (
	scopeConsumer = controller.ProjectIdentity{AuthorityID: "staging", RouteName: "consumer", UID: "consumer-uid"}
	scopeProvider = controller.ProjectIdentity{AuthorityID: "staging", RouteName: "datum-cloud", UID: "provider-uid"}
	scopeOptions  = PublisherOptions{ConsumerProject: scopeConsumer, ProviderProject: scopeProvider, ClassName: "central-metal", ProfileName: "general-purpose-24c-128g"}
)

type lookup map[multicluster.ClusterName]*manager.ProjectRuntime

func (l lookup) Get(name multicluster.ClusterName) *manager.ProjectRuntime { return l[name] }

func scopeScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := fleetv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}

func engaged(t *testing.T, name string, project controller.ProjectIdentity) (*manager.ProjectRuntime, client.Client) {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(scopeScheme(t)).WithStatusSubresource(&fleetv1.MachineClass{}, &fleetv1.MachineProfile{}).Build()
	return &manager.ProjectRuntime{Binding: manager.ProjectBinding{ClusterName: multicluster.ClusterName(name), Project: project, Client: c}}, c
}

func sourceProvider(t *testing.T) client.Client {
	t.Helper()
	profile := &fleetv1.MachineProfile{ObjectMeta: metav1.ObjectMeta{Name: scopeOptions.ProfileName, UID: "source-profile-uid"}, Spec: fleetv1.MachineProfileSpec{CPU: fleetv1.CPURequirement{Architecture: "amd64", Cores: 24}, Memory: fleetv1.MemoryRequirement{Capacity: "128Gi"}}}
	class := &fleetv1.MachineClass{ObjectMeta: metav1.ObjectMeta{Name: scopeOptions.ClassName, UID: "source-class-uid", Generation: 1}, Spec: fleetv1.MachineClassSpec{ProfilePolicy: &fleetv1.ProfilePolicy{AllowedProfiles: []fleetv1.LocalReference{{Name: profile.Name}}}, Access: &fleetv1.AccessPolicy{}, Capacity: &fleetv1.CapacityPolicy{MachineCapacities: fleetv1.MachineSelector{Namespace: "default"}}}}
	return fake.NewClientBuilder().WithScheme(scopeScheme(t)).WithObjects(class, profile).Build()
}

func published(t *testing.T, c client.Client) int {
	t.Helper()
	var classes fleetv1.MachineClassList
	var profiles fleetv1.MachineProfileList
	if err := c.List(context.Background(), &classes); err != nil {
		t.Fatal(err)
	}
	if err := c.List(context.Background(), &profiles); err != nil {
		t.Fatal(err)
	}
	return len(classes.Items) + len(profiles.Items)
}

func request(cluster string) mcreconcile.Request {
	return mcreconcile.Request{ClusterName: multicluster.ClusterName(cluster), Request: reconcile.Request{}}
}

func TestPublicationEventsComeOnlyFromTheConsumerAndPublishedNames(t *testing.T) {
	named := func(object client.Object, name string) client.Object { object.SetName(name); return object }
	cases := []struct {
		name    string
		cluster string
		object  client.Object
		want    int
	}{
		{"consumer class", "consumer", named(&fleetv1.MachineClass{}, scopeOptions.ClassName), 1},
		{"consumer profile", "consumer", named(&fleetv1.MachineProfile{}, scopeOptions.ProfileName), 1},
		{"consumer namespace replay", "consumer", named(&corev1.Namespace{}, "default"), 1},
		{"other project's same-named class", "other", named(&fleetv1.MachineClass{}, scopeOptions.ClassName), 0},
		{"other project's same-named profile", "other", named(&fleetv1.MachineProfile{}, scopeOptions.ProfileName), 0},
		{"other project's namespace", "other", named(&corev1.Namespace{}, "default"), 0},
		{"unpublished class name", "consumer", named(&fleetv1.MachineClass{}, "another-class"), 0},
		{"unpublished profile name", "consumer", named(&fleetv1.MachineProfile{}, "another-profile"), 0},
		{"unrelated kind", "consumer", named(&fleetv1.Machine{}, scopeOptions.ClassName), 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := consumerPublicationRequests(scopeOptions, multicluster.ClusterName(tc.cluster), tc.object)
			if len(got) != tc.want {
				t.Fatalf("requests = %v, want %d", got, tc.want)
			}
			for _, r := range got {
				if r.ClusterName != "consumer" {
					t.Fatalf("request targets %q", r.ClusterName)
				}
			}
		})
	}
	if got := publicationRequests(scopeOptions, named(&fleetv1.MachineClass{}, "another-class")); len(got) != 0 {
		t.Fatalf("provider event for an unpublished class = %v", got)
	}
}

// A request naming another engaged project is ignored, never published there.
func TestEngagedPublisherIgnoresOtherProjects(t *testing.T) {
	consumer, consumerClient := engaged(t, "consumer", scopeConsumer)
	other, otherClient := engaged(t, "other", controller.ProjectIdentity{AuthorityID: "staging", RouteName: "other", UID: "other-uid"})
	publisher := EngagedPublisher{Projects: lookup{"consumer": consumer, "other": other}, Provider: sourceProvider(t), Options: scopeOptions}
	if _, err := publisher.Reconcile(context.Background(), request("other")); err != nil {
		t.Fatalf("a request for another project must be ignored, got %v", err)
	}
	if published(t, otherClient) != 0 || published(t, consumerClient) != 0 {
		t.Fatal("a request for another project published something")
	}
	if _, err := publisher.Reconcile(context.Background(), request("consumer")); err != nil {
		t.Fatal(err)
	}
	if published(t, consumerClient) != 2 || published(t, otherClient) != 0 {
		t.Fatal("publication did not land only in the consumer")
	}
}

// The consumer's name engaged as a different project lifetime (deleted and
// recreated) is refused and never written.
func TestEngagedPublisherRefusesAReplacedConsumerLifetime(t *testing.T) {
	replaced, replacedClient := engaged(t, "consumer", controller.ProjectIdentity{AuthorityID: "staging", RouteName: "consumer", UID: "replacement-uid"})
	publisher := EngagedPublisher{Projects: lookup{"consumer": replaced}, Provider: sourceProvider(t), Options: scopeOptions}
	if _, err := publisher.Reconcile(context.Background(), request("consumer")); !errors.Is(err, manager.ErrProjectIdentityUnavailable) {
		t.Fatalf("err = %v, want the lifetime mismatch refused", err)
	}
	if published(t, replacedClient) != 0 {
		t.Fatal("published into a replaced consumer lifetime")
	}
	if _, err := (EngagedPublisher{Projects: lookup{}, Provider: sourceProvider(t), Options: scopeOptions}).Reconcile(context.Background(), request("consumer")); err != nil {
		t.Fatalf("an unengaged consumer waits for engagement: %v", err)
	}
}

// The staging handler is installed only for the consumer's exact lifetime.
func TestConsumerHandlersScopeStagingToTheConsumerLifetime(t *testing.T) {
	isStaging := func(h manager.Handlers) bool { _, ok := h.Machine.(Handler); return ok }
	c := fake.NewClientBuilder().WithScheme(scopeScheme(t)).Build()
	// The factory wires the power projector from the allocator's provider.
	factory := ConsumerHandlers(manager.DefaultHandlers, scopeConsumer, Handler{Allocator: &allocation.Allocator{Provider: c, Namespace: providerNamespace}, Location: "us-central-1"})
	consumer, err := factory(manager.ProjectBinding{ClusterName: "consumer", Project: scopeConsumer, Client: c})
	if err != nil || !isStaging(consumer) {
		t.Fatalf("consumer handlers = %#v, %v", consumer.Machine, err)
	}
	other, err := factory(manager.ProjectBinding{ClusterName: "other", Project: controller.ProjectIdentity{AuthorityID: "staging", RouteName: "other", UID: "other-uid"}, Client: c})
	if err != nil || isStaging(other) {
		t.Fatalf("another project got the staging handler: %#v, %v", other.Machine, err)
	}
	if _, err := factory(manager.ProjectBinding{ClusterName: "consumer", Project: controller.ProjectIdentity{AuthorityID: "staging", RouteName: "consumer", UID: "replacement-uid"}, Client: c}); !errors.Is(err, manager.ErrProjectIdentityUnavailable) {
		t.Fatalf("replaced consumer lifetime err = %v, want refused", err)
	}
}
