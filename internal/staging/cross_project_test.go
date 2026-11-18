package staging

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
	"go.datum.net/fleet/internal/manager"
	"go.datum.net/fleet/internal/simulator"
)

var (
	crossConsumerProject = controller.ProjectIdentity{AuthorityID: "staging", RouteName: "project-zdhcz", UID: "consumer-project-uid"}
	crossProviderProject = controller.ProjectIdentity{AuthorityID: "staging", RouteName: "datum-cloud", UID: "provider-project-uid"}
)

type crossProjectFixture struct {
	sourceClass   *fleetv1.MachineClass
	sourceProfile *fleetv1.MachineProfile
	machine       *fleetv1.MachineCapacity
	localClass    *fleetv1.MachineClass
	localProfile  *fleetv1.MachineProfile
	claim         *fleetv1.Machine
}

func crossProjectScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := fleetv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}

func newCrossProjectFixture(t *testing.T) crossProjectFixture {
	t.Helper()
	cores := int64(24)
	sourceProfile := &fleetv1.MachineProfile{ObjectMeta: metav1.ObjectMeta{Name: "general-purpose-24c-128g", UID: "source-profile-uid"}, Spec: fleetv1.MachineProfileSpec{CPU: fleetv1.CPURequirement{Architecture: "amd64", Cores: fleetv1.Constraint{Min: &cores}}, Memory: fleetv1.MemoryRequirement{Capacity: fleetv1.QuantityConstraint{Min: "128Gi"}}}}
	sourceClass := &fleetv1.MachineClass{ObjectMeta: metav1.ObjectMeta{Name: "central-metal", UID: "source-class-uid", Generation: 1}, Spec: fleetv1.MachineClassSpec{ProfilePolicy: &fleetv1.ProfilePolicy{AllowedProfiles: []fleetv1.LocalReference{{Name: sourceProfile.Name}}}, Access: &fleetv1.AccessPolicy{}, Capacity: &fleetv1.CapacityPolicy{MachineCapacities: fleetv1.MachineSelector{Namespace: "default", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"fleet.datumapis.com/capacity-pool": "central"}}}}}}
	machine := &fleetv1.MachineCapacity{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "central-metal-01", UID: "machine-uid", Labels: map[string]string{"fleet.datumapis.com/capacity-pool": "central"}}, Spec: fleetv1.MachineCapacitySpec{DeviceRef: fleetv1.LocalReference{Name: "central-metal-01"}, AllocationPolicy: "Enabled", CleaningPolicyRef: fleetv1.LocalReference{Name: "synthetic-reuse"}, ReclaimPolicy: "Retain"}}
	revision, err := publicationRevision(sourceClass, crossProviderProject)
	if err != nil {
		t.Fatal(err)
	}
	provenance := func(source client.Object) map[string]string {
		return map[string]string{publicationManagedByAnnotation: publicationManager, publicationProviderUIDAnnotation: string(crossProviderProject.UID), publicationSourceUIDAnnotation: string(source.GetUID()), publicationSourceNameAnnotation: source.GetName()}
	}
	localClass := &fleetv1.MachineClass{ObjectMeta: metav1.ObjectMeta{Name: sourceClass.Name, UID: "local-class-uid", Generation: 1, Annotations: provenance(sourceClass)}, Spec: fleetv1.MachineClassSpec{Source: &fleetv1.SourceReference{Project: crossProviderProject.RouteName, Name: sourceClass.Name}}, Status: fleetv1.MachineClassStatus{EvaluatedRevision: revision, ResolvedClass: &fleetv1.ResolvedClass{ProfilePolicy: *sourceClass.Spec.ProfilePolicy}, Conditions: []metav1.Condition{publicationCondition("SourceResolved", metav1.ConditionTrue, 1, "AuthorizedSource", "resolved")}}}
	localProfile := sourceProfile.DeepCopyObject().(*fleetv1.MachineProfile)
	localProfile.UID = "local-profile-uid"
	localProfile.Annotations = provenance(sourceProfile)
	localProfile.Status.Conditions = []metav1.Condition{publicationCondition("Published", metav1.ConditionTrue, localProfile.Generation, "SourceCopied", "copied")}
	claim := &fleetv1.Machine{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "cross-project", UID: "claim-uid"}, Spec: fleetv1.MachineSpec{MachineClassRef: fleetv1.LocalReference{Name: localClass.Name}, MachineProfileRef: fleetv1.LocalReference{Name: localProfile.Name}, LocationRef: fleetv1.LocalReference{Name: "us-central-1"}, PowerState: "Off"}}
	return crossProjectFixture{sourceClass: sourceClass, sourceProfile: sourceProfile, machine: machine, localClass: localClass, localProfile: localProfile, claim: claim}
}

func TestCrossProjectClaimRejectsUntrustedOrDriftedPublication(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*crossProjectFixture)
		reason string
	}{
		{
			name:   "class source names another project",
			mutate: func(f *crossProjectFixture) { f.localClass.Spec.Source.Project = "project-elsewhere" },
			reason: "UnresolvedPublication",
		},
		{
			name: "class published for another provider",
			mutate: func(f *crossProjectFixture) {
				f.localClass.Annotations[publicationProviderUIDAnnotation] = "other-provider-project-uid"
			},
			reason: "UnresolvedPublication",
		},
		{
			name: "profile published for another provider",
			mutate: func(f *crossProjectFixture) {
				f.localProfile.Annotations[publicationProviderUIDAnnotation] = "other-provider-project-uid"
			},
			reason: "UnresolvedPublication",
		},
		{
			name: "source profile requirements changed",
			mutate: func(f *crossProjectFixture) {
				cores := int64(48)
				f.sourceProfile.Spec.CPU.Cores.Min = &cores
			},
			reason: "PublicationDrift",
		},
		{
			name:   "source class replaced under the same name",
			mutate: func(f *crossProjectFixture) { f.sourceClass.UID = "replacement-source-class-uid" },
			reason: "PublicationDrift",
		},
		{
			name:   "source profile replaced under the same name",
			mutate: func(f *crossProjectFixture) { f.sourceProfile.UID = "replacement-source-profile-uid" },
			reason: "PublicationDrift",
		},
		{
			name:   "source class no longer allows the profile",
			mutate: func(f *crossProjectFixture) { f.sourceClass.Spec.ProfilePolicy.AllowedProfiles = nil },
			reason: "PublicationDrift",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			scheme := crossProjectScheme(t)
			f := newCrossProjectFixture(t)
			tc.mutate(&f)
			namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default", UID: "namespace-uid"}}
			consumer := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&fleetv1.Machine{}).WithObjects(namespace, f.localClass, f.localProfile, f.claim).Build()
			providerKube := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&fleetv1.MachineCapacity{}).WithObjects(f.sourceClass, f.sourceProfile, f.machine).Build()
			private := fake.NewClientBuilder().WithScheme(scheme).Build()
			coordinator, err := controller.NewCoordinator(private, "fleet-system-private", &executor{results: map[string]simulator.OperationResult{}}, controller.LocalDigestImageResolver{}, nil, 10*time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			h := Handler{Public: consumer, Provider: providerKube, Coordinator: coordinator, Project: crossConsumerProject, ProviderProject: crossProviderProject, Location: "us-central-1", Now: time.Now}
			request := manager.ProjectRequest{AuthorityID: crossConsumerProject.AuthorityID, ProjectUID: crossConsumerProject.UID, Namespace: f.claim.Namespace, Name: f.claim.Name, UID: f.claim.UID}
			for i := 0; i < 2; i++ {
				if out := h.ReconcileEvent(ctx, request); out.RetryError != nil {
					t.Fatalf("deterministic rejection must wait for an object event, not retry: %v", out.RetryError)
				}
			}
			var claim fleetv1.Machine
			if err := consumer.Get(ctx, client.ObjectKeyFromObject(f.claim), &claim); err != nil {
				t.Fatal(err)
			}
			accepted := apiMeta.FindStatusCondition(claim.Status.Conditions, "Accepted")
			if claim.Status.Phase != "Pending" || accepted == nil || accepted.Status != metav1.ConditionFalse || accepted.Reason != tc.reason {
				t.Fatalf("claim status = %#v, want Accepted=False reason %s", claim.Status, tc.reason)
			}
			if claim.Status.AllocatedHardware != nil || len(claim.Finalizers) != 0 {
				t.Fatalf("rejected claim took custody: finalizers=%v hardware=%#v", claim.Finalizers, claim.Status.AllocatedHardware)
			}
			var machine fleetv1.MachineCapacity
			if err := providerKube.Get(ctx, client.ObjectKeyFromObject(f.machine), &machine); err != nil {
				t.Fatal(err)
			}
			if len(machine.Status.Conditions) != 0 {
				t.Fatalf("provider machine was touched for a rejected claim: %#v", machine.Status.Conditions)
			}
		})
	}
}

func TestPublisherWithdrawsResolutionWhenSourceBecomesUnpublishable(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*fleetv1.MachineClass)
		reason string
	}{
		{
			name: "source class references a third project",
			mutate: func(class *fleetv1.MachineClass) {
				class.Spec.Source = &fleetv1.SourceReference{Project: "project-elsewhere", Name: "upstream-metal"}
			},
			reason: "InvalidSourceClass",
		},
		{
			name:   "source class has no capacity",
			mutate: func(class *fleetv1.MachineClass) { class.Spec.Capacity = nil },
			reason: "InvalidSourceClass",
		},
		{
			name:   "source class no longer allows the profile",
			mutate: func(class *fleetv1.MachineClass) { class.Spec.ProfilePolicy.AllowedProfiles = nil },
			reason: "ProfileNotAllowed",
		},
		{
			name:   "source class has no profile policy",
			mutate: func(class *fleetv1.MachineClass) { class.Spec.ProfilePolicy = nil },
			reason: "ProfileNotAllowed",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			scheme := crossProjectScheme(t)
			f := newCrossProjectFixture(t)
			providerKube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(f.sourceClass, f.sourceProfile).Build()
			consumer := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&fleetv1.MachineClass{}, &fleetv1.MachineProfile{}).Build()
			publisher := &Publisher{Consumer: consumer, Provider: providerKube, Options: PublisherOptions{ConsumerProject: crossConsumerProject, ProviderProject: crossProviderProject, ClassName: f.sourceClass.Name, ProfileName: f.sourceProfile.Name}}
			if _, err := publisher.Reconcile(ctx, reconcile.Request{}); err != nil {
				t.Fatal(err)
			}
			var published fleetv1.MachineClass
			if err := consumer.Get(ctx, client.ObjectKey{Name: f.sourceClass.Name}, &published); err != nil {
				t.Fatal(err)
			}
			if !sourceResolved(&published) {
				t.Fatalf("precondition: published class not resolved: %#v", published.Status)
			}
			var source fleetv1.MachineClass
			if err := providerKube.Get(ctx, client.ObjectKey{Name: f.sourceClass.Name}, &source); err != nil {
				t.Fatal(err)
			}
			tc.mutate(&source)
			if err := providerKube.Update(ctx, &source); err != nil {
				t.Fatal(err)
			}
			if _, err := publisher.Reconcile(ctx, reconcile.Request{}); err != nil {
				t.Fatalf("deterministic rejection must wait for an object event, not retry: %v", err)
			}
			if err := consumer.Get(ctx, client.ObjectKey{Name: f.sourceClass.Name}, &published); err != nil {
				t.Fatal(err)
			}
			resolved := apiMeta.FindStatusCondition(published.Status.Conditions, "SourceResolved")
			if sourceResolved(&published) || published.Status.ResolvedClass != nil || resolved == nil || resolved.Status != metav1.ConditionFalse || resolved.Reason != tc.reason {
				t.Fatalf("published class status = %#v, want SourceResolved=False reason %s", published.Status, tc.reason)
			}
			if published.Spec.Source == nil || published.Spec.Source.Project != crossProviderProject.RouteName {
				t.Fatalf("publisher rewrote the consumer reference: %#v", published.Spec)
			}
		})
	}
}

func TestPublisherDoesNotPublishUnpublishableSource(t *testing.T) {
	ctx := context.Background()
	scheme := crossProjectScheme(t)
	f := newCrossProjectFixture(t)
	f.sourceClass.Spec.ProfilePolicy.AllowedProfiles = []fleetv1.LocalReference{{Name: "some-other-profile"}}
	providerKube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(f.sourceClass, f.sourceProfile).Build()
	consumer := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&fleetv1.MachineClass{}, &fleetv1.MachineProfile{}).Build()
	publisher := &Publisher{Consumer: consumer, Provider: providerKube, Options: PublisherOptions{ConsumerProject: crossConsumerProject, ProviderProject: crossProviderProject, ClassName: f.sourceClass.Name, ProfileName: f.sourceProfile.Name}}
	if _, err := publisher.Reconcile(ctx, reconcile.Request{}); err != nil {
		t.Fatal(err)
	}
	var classes fleetv1.MachineClassList
	var profiles fleetv1.MachineProfileList
	if err := consumer.List(ctx, &classes); err != nil {
		t.Fatal(err)
	}
	if err := consumer.List(ctx, &profiles); err != nil {
		t.Fatal(err)
	}
	if len(classes.Items) != 0 || len(profiles.Items) != 0 {
		t.Fatalf("publisher exposed a disallowed profile: classes=%d profiles=%d", len(classes.Items), len(profiles.Items))
	}
}
