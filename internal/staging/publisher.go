package staging

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlcluster "sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
	"go.datum.net/fleet/internal/manager"
)

const (
	publicationManagedByAnnotation   = "fleet.datumapis.com/publication-managed-by"
	publicationProviderUIDAnnotation = "fleet.datumapis.com/publication-provider-project-uid"
	publicationSourceUIDAnnotation   = "fleet.datumapis.com/publication-source-uid"
	publicationSourceNameAnnotation  = "fleet.datumapis.com/publication-source-name"
	publicationManager               = "synthetic-staging-publisher"
)

var errPublicationConflict = errors.New("synthetic publication conflicts with an existing consumer object")

// PublisherOptions is the deliberately narrow publication contract used by the
// synthetic staging environment. Production publication remains disabled until
// caller-preserving IAM and durable catalog event adapters are available.
type PublisherOptions struct {
	ConsumerProject controller.ProjectIdentity
	ProviderProject controller.ProjectIdentity
	ClassName       string
	ProfileName     string
}

func (o PublisherOptions) valid() bool {
	return o.ConsumerProject.Valid() && o.ProviderProject.Valid() && !o.ConsumerProject.SameLifetime(o.ProviderProject) && o.ClassName != "" && o.ProfileName != ""
}

// SetupPublisher wires provider and consumer watches without a polling resync.
// It publishes into exactly one engaged consumer project, the configured
// publication consumer, and only while the cluster engaged under that name
// holds the exact consumer project lifetime. Other engaged projects are never
// written.
func SetupPublisher(mgr mcmanager.Manager, projects *manager.Projects, provider ctrlcluster.Cluster, options PublisherOptions) error {
	if mgr == nil || projects == nil || provider == nil || !options.valid() {
		return fmt.Errorf("synthetic publisher requires exact provider and consumer projects")
	}
	consumerEvents := func(name multicluster.ClusterName, _ ctrlcluster.Cluster) handler.TypedEventHandler[client.Object, mcreconcile.Request] {
		return handler.TypedEnqueueRequestsFromMapFunc(func(_ context.Context, object client.Object) []mcreconcile.Request {
			return consumerPublicationRequests(options, name, object)
		})
	}
	providerEvents := handler.TypedEnqueueRequestsFromMapFunc(func(_ context.Context, object client.Object) []mcreconcile.Request {
		return publicationRequests(options, object)
	})
	engage := []mcbuilder.WatchesOption{mcbuilder.WithEngageWithLocalCluster(false), mcbuilder.WithEngageWithProviderClusters(true)}
	controller := mcbuilder.TypedControllerManagedBy[mcreconcile.Request](mgr).
		Named("fleet-synthetic-publication").
		Watches(&fleetv1.MachineClass{}, consumerEvents, engage...).
		Watches(&fleetv1.MachineProfile{}, consumerEvents, engage...).
		Watches(&corev1.Namespace{}, consumerEvents, engage...).
		WatchesRawSource(source.TypedKind(provider.GetCache(), client.Object(&fleetv1.MachineClass{}), providerEvents)).
		WatchesRawSource(source.TypedKind(provider.GetCache(), client.Object(&fleetv1.MachineProfile{}), providerEvents))
	return controller.Complete(EngagedPublisher{Projects: projects, Provider: provider.GetClient(), Options: options})
}

// consumerPublicationRequests maps an event from an engaged project. Only the
// publication consumer's events wake the publisher; any other project's
// objects, including same-named classes and profiles, are ignored.
func consumerPublicationRequests(options PublisherOptions, cluster multicluster.ClusterName, object client.Object) []mcreconcile.Request {
	if cluster != multicluster.ClusterName(options.ConsumerProject.RouteName) {
		return nil
	}
	return publicationRequests(options, object)
}

// publicationRequests maps the published class and profile, by exact name,
// and the consumer's Namespaces to the one publication request.
func publicationRequests(options PublisherOptions, object client.Object) []mcreconcile.Request {
	if object == nil {
		return nil
	}
	switch object.(type) {
	case *corev1.Namespace:
		// Engagement replays the consumer's Namespaces, which wakes the
		// publisher even when provider events arrived before the consumer was
		// engaged and nothing has been published there yet.
	case *fleetv1.MachineClass:
		if object.GetName() != options.ClassName {
			return nil
		}
	case *fleetv1.MachineProfile:
		if object.GetName() != options.ProfileName {
			return nil
		}
	default:
		return nil
	}
	return []mcreconcile.Request{{ClusterName: multicluster.ClusterName(options.ConsumerProject.RouteName), Request: reconcile.Request{NamespacedName: types.NamespacedName{Name: options.ClassName}}}}
}

// ProjectLookup finds an engaged project's runtime; manager.Projects
// implements it.
type ProjectLookup interface {
	Get(multicluster.ClusterName) *manager.ProjectRuntime
}

// EngagedPublisher runs Publisher against the engaged publication consumer.
type EngagedPublisher struct {
	Projects ProjectLookup
	Provider client.Client
	Options  PublisherOptions
}

func (p EngagedPublisher) Reconcile(ctx context.Context, request mcreconcile.Request) (reconcile.Result, error) {
	if p.Projects == nil || request.ClusterName != multicluster.ClusterName(p.Options.ConsumerProject.RouteName) {
		return reconcile.Result{}, nil
	}
	runtime := p.Projects.Get(request.ClusterName)
	if runtime == nil {
		// Not engaged: its engagement replays the consumer objects.
		return reconcile.Result{}, nil
	}
	if !runtime.Binding.Project.SameLifetime(p.Options.ConsumerProject) {
		return reconcile.Result{}, fmt.Errorf("%w: publication consumer %q is engaged as a different project lifetime", manager.ErrProjectIdentityUnavailable, request.ClusterName)
	}
	return (&Publisher{Consumer: runtime.Binding.Client, Provider: p.Provider, Options: p.Options}).Reconcile(ctx, request.Request)
}

type Publisher struct {
	Consumer client.Client
	Provider client.Client
	Options  PublisherOptions
}

func (p *Publisher) Reconcile(ctx context.Context, _ reconcile.Request) (reconcile.Result, error) {
	if p == nil || p.Consumer == nil || p.Provider == nil || !p.Options.valid() {
		return reconcile.Result{}, fmt.Errorf("synthetic publisher is not configured")
	}
	var sourceClass fleetv1.MachineClass
	if err := p.Provider.Get(ctx, client.ObjectKey{Name: p.Options.ClassName}, &sourceClass); err != nil {
		if apierrors.IsNotFound(err) {
			return reconcile.Result{}, p.markClassUnresolved(ctx, "SourceClassNotFound", "The provider MachineClass does not exist.")
		}
		return reconcile.Result{}, err
	}
	if sourceClass.Spec.Source != nil || sourceClass.Spec.Capacity == nil {
		return reconcile.Result{}, p.markClassUnresolved(ctx, "InvalidSourceClass", "The provider MachineClass is not a capacity definition.")
	}
	if !profileAllowed(sourceClass.Spec.ProfilePolicy, p.Options.ProfileName) {
		return reconcile.Result{}, p.markClassUnresolved(ctx, "ProfileNotAllowed", "The provider MachineClass does not allow the published MachineProfile.")
	}
	var sourceProfile fleetv1.MachineProfile
	if err := p.Provider.Get(ctx, client.ObjectKey{Name: p.Options.ProfileName}, &sourceProfile); err != nil {
		if apierrors.IsNotFound(err) {
			return reconcile.Result{}, p.markClassUnresolved(ctx, "SourceProfileNotFound", "The provider MachineProfile does not exist.")
		}
		return reconcile.Result{}, err
	}
	if err := p.ensureProfile(ctx, &sourceProfile); err != nil {
		if errors.Is(err, errPublicationConflict) {
			return reconcile.Result{}, p.markClassUnresolved(ctx, "PublicationConflict", err.Error())
		}
		return reconcile.Result{}, err
	}
	if err := p.ensureClass(ctx, &sourceClass); err != nil {
		if errors.Is(err, errPublicationConflict) {
			return reconcile.Result{}, p.markClassUnresolved(ctx, "PublicationConflict", err.Error())
		}
		return reconcile.Result{}, err
	}
	return reconcile.Result{}, nil
}

func profileAllowed(policy *fleetv1.ProfilePolicy, name string) bool {
	if policy == nil {
		return false
	}
	for _, profile := range policy.AllowedProfiles {
		if profile.Name == name {
			return true
		}
	}
	return false
}

func (p *Publisher) annotations(source client.Object) map[string]string {
	return map[string]string{
		publicationManagedByAnnotation:   publicationManager,
		publicationProviderUIDAnnotation: string(p.Options.ProviderProject.UID),
		publicationSourceUIDAnnotation:   string(source.GetUID()),
		publicationSourceNameAnnotation:  source.GetName(),
	}
}

func (p *Publisher) ensureProfile(ctx context.Context, sourceProfile *fleetv1.MachineProfile) error {
	var local fleetv1.MachineProfile
	err := p.Consumer.Get(ctx, client.ObjectKey{Name: p.Options.ProfileName}, &local)
	if apierrors.IsNotFound(err) {
		local = fleetv1.MachineProfile{ObjectMeta: metav1.ObjectMeta{Name: p.Options.ProfileName, Annotations: p.annotations(sourceProfile)}, Spec: sourceProfile.Spec}
		if err := p.Consumer.Create(ctx, &local); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else {
		if !p.owned(&local, sourceProfile) {
			return fmt.Errorf("%w: MachineProfile %q has different ownership or source lifetime", errPublicationConflict, local.Name)
		}
		// Published requirements are immutable. A changed source must use a new
		// profile name instead of silently changing an admitted hardware contract.
		if !reflect.DeepEqual(local.Spec, sourceProfile.Spec) {
			return fmt.Errorf("%w: published MachineProfile %q differs from its source; publish a new versioned profile", errPublicationConflict, local.Name)
		}
	}
	want := []metav1.Condition{publicationCondition("Published", metav1.ConditionTrue, local.Generation, "SourceCopied", "Requirements are an exact publisher-owned copy of the provider profile.")}
	if conditionsEqual(local.Status.Conditions, want) {
		return nil
	}
	local.Status.Conditions = want
	return p.Consumer.Status().Update(ctx, &local)
}

func (p *Publisher) ensureClass(ctx context.Context, sourceClass *fleetv1.MachineClass) error {
	var local fleetv1.MachineClass
	err := p.Consumer.Get(ctx, client.ObjectKey{Name: p.Options.ClassName}, &local)
	wantSpec := fleetv1.MachineClassSpec{Source: &fleetv1.SourceReference{Project: p.Options.ProviderProject.RouteName, Name: sourceClass.Name}}
	if apierrors.IsNotFound(err) {
		local = fleetv1.MachineClass{ObjectMeta: metav1.ObjectMeta{Name: p.Options.ClassName, Annotations: p.annotations(sourceClass)}, Spec: wantSpec}
		if err := p.Consumer.Create(ctx, &local); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else if !p.owned(&local, sourceClass) || !reflect.DeepEqual(local.Spec, wantSpec) {
		return fmt.Errorf("%w: MachineClass %q has different ownership, source lifetime, or spec", errPublicationConflict, local.Name)
	}
	revision, err := publicationRevision(sourceClass, p.Options.ProviderProject)
	if err != nil {
		return err
	}
	want := fleetv1.MachineClassStatus{
		EvaluatedRevision: revision,
		ResolvedClass:     &fleetv1.ResolvedClass{ProfilePolicy: dereferenceProfilePolicy(sourceClass.Spec.ProfilePolicy), Access: dereferenceAccess(sourceClass.Spec.Access)},
		Conditions:        []metav1.Condition{publicationCondition("SourceResolved", metav1.ConditionTrue, local.Generation, "AuthorizedSource", "The publisher resolved the exact provider class lifetime.")},
	}
	if classStatusEqual(local.Status, want) {
		return nil
	}
	local.Status = want
	return p.Consumer.Status().Update(ctx, &local)
}

func (p *Publisher) markClassUnresolved(ctx context.Context, reason, message string) error {
	var local fleetv1.MachineClass
	if err := p.Consumer.Get(ctx, client.ObjectKey{Name: p.Options.ClassName}, &local); apierrors.IsNotFound(err) {
		return nil
	} else if err != nil {
		return err
	}
	want := local.Status
	want.ResolvedClass = nil
	want.Conditions = []metav1.Condition{publicationCondition("SourceResolved", metav1.ConditionFalse, local.Generation, reason, message)}
	if classStatusEqual(local.Status, want) {
		return nil
	}
	local.Status = want
	return p.Consumer.Status().Update(ctx, &local)
}

func (p *Publisher) owned(local client.Object, source client.Object) bool {
	a := local.GetAnnotations()
	return a[publicationManagedByAnnotation] == publicationManager && a[publicationProviderUIDAnnotation] == string(p.Options.ProviderProject.UID) && a[publicationSourceUIDAnnotation] == string(source.GetUID()) && a[publicationSourceNameAnnotation] == source.GetName()
}

func publicationRevision(class *fleetv1.MachineClass, provider controller.ProjectIdentity) (string, error) {
	content := struct {
		ProviderUID types.UID                `json:"providerUID"`
		ClassUID    types.UID                `json:"classUID"`
		Generation  int64                    `json:"generation"`
		Spec        fleetv1.MachineClassSpec `json:"spec"`
	}{provider.UID, class.UID, class.Generation, class.Spec}
	b, err := json.Marshal(content)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func dereferenceAccess(access *fleetv1.AccessPolicy) fleetv1.AccessPolicy {
	if access == nil {
		return fleetv1.AccessPolicy{}
	}
	return *access
}

func publicationCondition(kind string, status metav1.ConditionStatus, generation int64, reason, message string) metav1.Condition {
	return metav1.Condition{Type: kind, Status: status, ObservedGeneration: generation, LastTransitionTime: metav1.Now(), Reason: reason, Message: message}
}

func conditionsEqual(left, right []metav1.Condition) bool {
	if len(left) != len(right) {
		return false
	}
	left = append([]metav1.Condition(nil), left...)
	right = append([]metav1.Condition(nil), right...)
	for i := range left {
		left[i].LastTransitionTime = metav1.Time{}
		right[i].LastTransitionTime = metav1.Time{}
	}
	return reflect.DeepEqual(left, right)
}

func classStatusEqual(left, right fleetv1.MachineClassStatus) bool {
	left.Conditions = append([]metav1.Condition(nil), left.Conditions...)
	right.Conditions = append([]metav1.Condition(nil), right.Conditions...)
	for i := range left.Conditions {
		left.Conditions[i].LastTransitionTime = metav1.Time{}
	}
	for i := range right.Conditions {
		right.Conditions[i].LastTransitionTime = metav1.Time{}
	}
	return reflect.DeepEqual(left, right)
}

func sourceResolved(class *fleetv1.MachineClass) bool {
	condition := apiMeta.FindStatusCondition(class.Status.Conditions, "SourceResolved")
	return condition != nil && condition.Status == metav1.ConditionTrue && condition.ObservedGeneration == class.Generation && class.Status.ResolvedClass != nil && class.Status.EvaluatedRevision != ""
}
