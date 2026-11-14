package staging

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
)

func TestPublisherCreatesProtectedReferenceAndExactProfileCopy(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := fleetv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	cores := int64(24)
	profile := &fleetv1.MachineProfile{ObjectMeta: metav1.ObjectMeta{Name: "general-purpose-24c-128g", UID: "source-profile-uid"}, Spec: fleetv1.MachineProfileSpec{CPU: fleetv1.CPURequirement{Architecture: "amd64", Cores: fleetv1.Constraint{Min: &cores}}, Memory: fleetv1.MemoryRequirement{Capacity: fleetv1.QuantityConstraint{Min: "128Gi"}}}}
	class := &fleetv1.MachineClass{ObjectMeta: metav1.ObjectMeta{Name: "central-metal", UID: "source-class-uid", Generation: 3}, Spec: fleetv1.MachineClassSpec{ProfilePolicy: &fleetv1.ProfilePolicy{AllowedProfiles: []fleetv1.LocalReference{{Name: profile.Name}}}, Access: &fleetv1.AccessPolicy{}, Capacity: &fleetv1.CapacityPolicy{MachineCapacities: fleetv1.MachineSelector{Namespace: "default"}}}}
	provider := fake.NewClientBuilder().WithScheme(scheme).WithObjects(class, profile).Build()
	consumer := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&fleetv1.MachineClass{}, &fleetv1.MachineProfile{}).Build()
	consumerProject := controller.ProjectIdentity{AuthorityID: "staging", RouteName: "project-zdhcz", UID: "consumer-uid"}
	providerProject := controller.ProjectIdentity{AuthorityID: "staging", RouteName: "datum-cloud", UID: "provider-uid"}
	publisher := &Publisher{Consumer: consumer, Provider: provider, Options: PublisherOptions{ConsumerProject: consumerProject, ProviderProject: providerProject, ClassName: class.Name, ProfileName: profile.Name}}
	if _, err := publisher.Reconcile(ctx, reconcile.Request{}); err != nil {
		t.Fatal(err)
	}
	var publishedClass fleetv1.MachineClass
	if err := consumer.Get(ctx, client.ObjectKey{Name: class.Name}, &publishedClass); err != nil {
		t.Fatal(err)
	}
	if publishedClass.Spec.Source == nil || publishedClass.Spec.Source.Project != providerProject.RouteName || publishedClass.Spec.Source.Name != class.Name || !sourceResolved(&publishedClass) {
		t.Fatalf("published class = %#v", publishedClass)
	}
	if publishedClass.Status.ResolvedClass.ProfilePolicy.AllowedProfiles[0].Name != profile.Name || publishedClass.Annotations[publicationSourceUIDAnnotation] != string(class.UID) {
		t.Fatalf("published class provenance/status = %#v", publishedClass)
	}
	var publishedProfile fleetv1.MachineProfile
	if err := consumer.Get(ctx, client.ObjectKey{Name: profile.Name}, &publishedProfile); err != nil {
		t.Fatal(err)
	}
	if publishedProfile.Annotations[publicationSourceUIDAnnotation] != string(profile.UID) || publishedProfile.Annotations[publicationManagedByAnnotation] != publicationManager || publishedProfile.Spec.CPU.Cores.Min == nil || *publishedProfile.Spec.CPU.Cores.Min != cores {
		t.Fatalf("published profile = %#v", publishedProfile)
	}
	if _, err := publisher.Reconcile(ctx, reconcile.Request{}); err != nil {
		t.Fatalf("idempotent reconcile: %v", err)
	}
}

func TestPublisherDoesNotAdoptConflictingConsumerObject(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := fleetv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	profile := &fleetv1.MachineProfile{ObjectMeta: metav1.ObjectMeta{Name: "profile", UID: "source-profile"}, Spec: fleetv1.MachineProfileSpec{CPU: fleetv1.CPURequirement{Architecture: "amd64"}}}
	class := &fleetv1.MachineClass{ObjectMeta: metav1.ObjectMeta{Name: "class", UID: "source-class"}, Spec: fleetv1.MachineClassSpec{ProfilePolicy: &fleetv1.ProfilePolicy{AllowedProfiles: []fleetv1.LocalReference{{Name: profile.Name}}}, Capacity: &fleetv1.CapacityPolicy{MachineCapacities: fleetv1.MachineSelector{Namespace: "default"}}}}
	conflict := &fleetv1.MachineProfile{ObjectMeta: metav1.ObjectMeta{Name: profile.Name, UID: "consumer-profile"}, Spec: profile.Spec}
	provider := fake.NewClientBuilder().WithScheme(scheme).WithObjects(class, profile).Build()
	consumer := fake.NewClientBuilder().WithScheme(scheme).WithObjects(conflict).Build()
	publisher := &Publisher{Consumer: consumer, Provider: provider, Options: PublisherOptions{ConsumerProject: controller.ProjectIdentity{AuthorityID: "a", RouteName: "consumer", UID: "consumer"}, ProviderProject: controller.ProjectIdentity{AuthorityID: "a", RouteName: "provider", UID: "provider"}, ClassName: class.Name, ProfileName: profile.Name}}
	if _, err := publisher.Reconcile(ctx, reconcile.Request{}); err != nil {
		t.Fatalf("deterministic conflict must wait for an object event, not retry: %v", err)
	}
	var unchanged fleetv1.MachineProfile
	if err := consumer.Get(ctx, client.ObjectKey{Name: profile.Name}, &unchanged); err != nil || unchanged.UID != conflict.UID || unchanged.Annotations[publicationManagedByAnnotation] != "" {
		t.Fatalf("conflicting consumer profile was adopted: %#v, %v", unchanged, err)
	}
}
