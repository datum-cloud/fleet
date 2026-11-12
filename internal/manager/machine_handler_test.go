package manager

import (
	"context"
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
	fleetnetwork "go.datum.net/fleet/internal/network"
)

type machineInputsFunc func(context.Context, controller.ProjectIdentity, *fleetv1.Machine, *corev1.Namespace) (MachineInputs, error)

func (f machineInputsFunc) ResolveMachineInputs(ctx context.Context, project controller.ProjectIdentity, claim *fleetv1.Machine, namespace *corev1.Namespace) (MachineInputs, error) {
	return f(ctx, project, claim, namespace)
}

type fakeMachineLifecycle struct {
	retainErr   error
	allocateErr error
	releaseErr  error
	release     controller.ReleaseResult
	retains     int
	allocations int
	releases    int
}

type deleteBeforeFinalizerClient struct {
	client.Client
	deleted bool
}

type mutateBeforeFinalizerClient struct {
	client.Client
	mutated bool
}

func (c *mutateBeforeFinalizerClient) Update(ctx context.Context, object client.Object, options ...client.UpdateOption) error {
	claim, ok := object.(*fleetv1.Machine)
	if ok && !c.mutated && containsString(claim.Finalizers, controller.MachineFinalizer) {
		c.mutated = true
		var current fleetv1.Machine
		if err := c.Get(ctx, client.ObjectKeyFromObject(claim), &current); err != nil {
			return err
		}
		current.Spec.MachineProfileRef.Name = "new-profile"
		if err := c.Client.Update(ctx, &current); err != nil {
			return err
		}
	}
	return c.Client.Update(ctx, object, options...)
}

func (c *deleteBeforeFinalizerClient) Update(ctx context.Context, object client.Object, options ...client.UpdateOption) error {
	claim, ok := object.(*fleetv1.Machine)
	if ok && !c.deleted && containsString(claim.Finalizers, controller.MachineFinalizer) {
		c.deleted = true
		var current fleetv1.Machine
		if err := c.Get(ctx, client.ObjectKeyFromObject(claim), &current); err != nil {
			return err
		}
		if err := c.Client.Delete(ctx, &current); err != nil {
			return err
		}
		return errors.New("claim deleted before finalizer update")
	}
	return c.Client.Update(ctx, object, options...)
}

func (f *fakeMachineLifecycle) RetainAuthority(context.Context, controller.ProjectIdentity, *fleetv1.Machine, MachineInputs) error {
	f.retains++
	return f.retainErr
}

func (f *fakeMachineLifecycle) Allocate(context.Context, controller.ProjectIdentity, *fleetv1.Machine, MachineInputs) error {
	f.allocations++
	return f.allocateErr
}

func (f *fakeMachineLifecycle) Release(context.Context, controller.ProjectIdentity, types.UID) (controller.ReleaseResult, error) {
	f.releases++
	return f.release, f.releaseErr
}

func TestMachineHandlerRetainsAuthorityBeforeFinalizerAndAllocation(t *testing.T) {
	handler, kube, lifecycle, request, expires := newMachineHandlerFixture(t)
	outcome := handler.ReconcileEvent(context.Background(), request)
	if outcome.RetryError != nil || outcome.Deadline.Mode != DeadlineSchedule || !outcome.Deadline.At.Equal(expires) {
		t.Fatalf("first outcome = %#v", outcome)
	}
	if lifecycle.retains != 1 || lifecycle.allocations != 0 {
		t.Fatalf("first calls retain=%d allocate=%d", lifecycle.retains, lifecycle.allocations)
	}
	var updated fleetv1.Machine
	if err := kube.Get(context.Background(), client.ObjectKey{Namespace: request.Namespace, Name: request.Name}, &updated); err != nil {
		t.Fatal(err)
	}
	if !containsString(updated.Finalizers, controller.MachineFinalizer) {
		t.Fatal("Fleet finalizer was not installed after authority retention")
	}

	outcome = handler.ReconcileEvent(context.Background(), request)
	if outcome.RetryError != nil || lifecycle.retains != 2 || lifecycle.allocations != 1 {
		t.Fatalf("second outcome=%#v calls retain=%d allocate=%d", outcome, lifecycle.retains, lifecycle.allocations)
	}
}

func TestMachineHandlerWaitsForDependencyEventsWithoutTimedRetry(t *testing.T) {
	handler, _, lifecycle, request, _ := newMachineHandlerFixture(t)
	handler.Inputs = machineInputsFunc(func(context.Context, controller.ProjectIdentity, *fleetv1.Machine, *corev1.Namespace) (MachineInputs, error) {
		return MachineInputs{}, errors.Join(ErrDependencyPending, errors.New("location unavailable"))
	})
	outcome := handler.ReconcileEvent(context.Background(), request)
	if outcome.RetryError != nil || outcome.Deadline.Mode != DeadlineUnchanged {
		t.Fatalf("pending outcome = %#v", outcome)
	}
	if lifecycle.retains != 0 || lifecycle.allocations != 0 {
		t.Fatalf("pending dependency invoked lifecycle: %#v", lifecycle)
	}
}

func TestMachineHandlerPendingAllocationKeepsOnlyEvidenceDeadline(t *testing.T) {
	handler, _, lifecycle, request, expires := newMachineHandlerFixture(t)
	first := handler.ReconcileEvent(context.Background(), request)
	if first.RetryError != nil {
		t.Fatal(first.RetryError)
	}
	lifecycle.allocateErr = errors.Join(ErrDependencyPending, errors.New("quota grant pending"))
	outcome := handler.ReconcileEvent(context.Background(), request)
	if outcome.RetryError != nil || outcome.Deadline.Mode != DeadlineSchedule || !outcome.Deadline.At.Equal(expires) {
		t.Fatalf("pending allocation outcome = %#v", outcome)
	}
}

func TestMachineHandlerDoesNotAdoptRecreatedClaimName(t *testing.T) {
	handler, _, lifecycle, request, _ := newMachineHandlerFixture(t)
	request.UID = "old-claim"
	lifecycle.release.SafeToRemoveFinalizer = true
	outcome := handler.ReconcileEvent(context.Background(), request)
	if outcome.RetryError != nil || outcome.Deadline.Mode != DeadlineCancel {
		t.Fatalf("recreated outcome = %#v", outcome)
	}
	if lifecycle.retains != 0 || lifecycle.allocations != 0 || lifecycle.releases != 1 {
		t.Fatalf("recreated name invoked lifecycle: %#v", lifecycle)
	}
}

func TestMachineHandlerReleasesRetainedAuthorityAfterPublicClaimDisappears(t *testing.T) {
	handler, kube, lifecycle, request, _ := newMachineHandlerFixture(t)
	lifecycle.release.SafeToRemoveFinalizer = true
	var claim fleetv1.Machine
	if err := kube.Get(context.Background(), client.ObjectKey{Namespace: request.Namespace, Name: request.Name}, &claim); err != nil {
		t.Fatal(err)
	}
	if err := kube.Delete(context.Background(), &claim); err != nil {
		t.Fatal(err)
	}
	outcome := handler.ReconcileEvent(context.Background(), request)
	if outcome.RetryError != nil || outcome.Deadline.Mode != DeadlineCancel || lifecycle.releases != 1 {
		t.Fatalf("missing claim outcome=%#v releases=%d", outcome, lifecycle.releases)
	}
}

func TestMachineHandlerRecoversDeletionBetweenAuthorityAndFinalizer(t *testing.T) {
	handler, kube, lifecycle, request, _ := newMachineHandlerFixture(t)
	racingClient := &deleteBeforeFinalizerClient{Client: kube}
	handler.Client = racingClient
	first := handler.ReconcileEvent(context.Background(), request)
	if first.RetryError == nil || lifecycle.retains != 1 || lifecycle.releases != 0 {
		t.Fatalf("authority/finalizer race outcome=%#v lifecycle=%#v", first, lifecycle)
	}
	lifecycle.release.SafeToRemoveFinalizer = true
	second := handler.ReconcileEvent(context.Background(), request)
	if second.RetryError != nil || second.Deadline.Mode != DeadlineCancel || lifecycle.releases != 1 {
		t.Fatalf("recovery outcome=%#v lifecycle=%#v", second, lifecycle)
	}
}

func TestMachineHandlerDoesNotFinalizeSpecNewerThanRetainedAuthority(t *testing.T) {
	handler, kube, lifecycle, request, _ := newMachineHandlerFixture(t)
	handler.Client = &mutateBeforeFinalizerClient{Client: kube}
	outcome := handler.ReconcileEvent(context.Background(), request)
	if outcome.RetryError == nil || lifecycle.retains != 1 {
		t.Fatalf("concurrent spec outcome=%#v lifecycle=%#v", outcome, lifecycle)
	}
	var claim fleetv1.Machine
	if err := kube.Get(context.Background(), client.ObjectKey{Namespace: request.Namespace, Name: request.Name}, &claim); err != nil {
		t.Fatal(err)
	}
	if containsString(claim.Finalizers, controller.MachineFinalizer) || claim.Spec.MachineProfileRef.Name != "new-profile" {
		t.Fatalf("new spec was finalized with old authority: %#v", claim)
	}
}

func TestMachineHandlerReleaseRemovesFinalizerOnlyAfterSafeSettlement(t *testing.T) {
	handler, kube, lifecycle, request, _ := newMachineHandlerFixture(t)
	var claim fleetv1.Machine
	key := client.ObjectKey{Namespace: request.Namespace, Name: request.Name}
	if err := kube.Get(context.Background(), key, &claim); err != nil {
		t.Fatal(err)
	}
	claim.Finalizers = []string{controller.MachineFinalizer}
	if err := kube.Update(context.Background(), &claim); err != nil {
		t.Fatal(err)
	}
	if err := kube.Delete(context.Background(), &claim); err != nil {
		t.Fatal(err)
	}

	outcome := handler.ReconcileEvent(context.Background(), request)
	if !errors.Is(outcome.RetryError, ErrEventWiringUnavailable) || lifecycle.releases != 1 {
		t.Fatalf("unsafe release outcome=%#v calls=%d", outcome, lifecycle.releases)
	}
	if err := kube.Get(context.Background(), key, &claim); err != nil {
		t.Fatal(err)
	}
	if !containsString(claim.Finalizers, controller.MachineFinalizer) {
		t.Fatal("unsafe release removed finalizer")
	}

	lifecycle.release.SafeToRemoveFinalizer = true
	outcome = handler.ReconcileEvent(context.Background(), request)
	if outcome.RetryError != nil || outcome.Deadline.Mode != DeadlineCancel {
		t.Fatalf("safe release outcome = %#v", outcome)
	}
	if err := kube.Get(context.Background(), key, &claim); err == nil {
		if containsString(claim.Finalizers, controller.MachineFinalizer) {
			t.Fatal("safe release retained finalizer")
		}
	} else if !apierrors.IsNotFound(err) {
		t.Fatalf("read safely released claim: %v", err)
	}
}

func TestMachineHandlerRetriesInfrastructureFailure(t *testing.T) {
	handler, _, lifecycle, request, _ := newMachineHandlerFixture(t)
	want := errors.New("authority store unavailable")
	lifecycle.retainErr = want
	outcome := handler.ReconcileEvent(context.Background(), request)
	if !errors.Is(outcome.RetryError, want) {
		t.Fatalf("retry error = %v, want %v", outcome.RetryError, want)
	}
}

func newMachineHandlerFixture(t *testing.T) (MachineHandler, client.Client, *fakeMachineLifecycle, ProjectRequest, time.Time) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := fleetv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default", UID: "namespace-uid"}}
	claim := &fleetv1.Machine{ObjectMeta: metav1.ObjectMeta{Namespace: namespace.Name, Name: "allocation", UID: "claim-uid"}, Spec: fleetv1.MachineSpec{MachineClassRef: fleetv1.LocalReference{Name: "standard"}, MachineProfileRef: fleetv1.LocalReference{Name: "general"}, LocationRef: fleetv1.LocalReference{Name: "us-central-1"}}}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(namespace, claim).Build()
	project := controller.ProjectIdentity{AuthorityID: "staging-milo", RouteName: "datum-cloud", UID: "project-uid"}
	expires := time.Date(2026, 11, 5, 18, 0, 0, 0, time.UTC)
	localClass := &fleetv1.MachineClass{ObjectMeta: metav1.ObjectMeta{Name: "standard", UID: "class-uid"}}
	profile := &fleetv1.MachineProfile{ObjectMeta: metav1.ObjectMeta{Name: "general", UID: "profile-uid"}}
	inputs := MachineInputs{ConsumerRoute: project.RouteName, ProviderRoute: project.RouteName, NamespaceUID: namespace.UID, LocalClass: localClass, SourceClass: localClass, LocalProfile: profile, PublicationRevision: "same-project-v1", Location: fleetnetwork.LocationAuthority{Name: "us-central-1", UID: "location-uid", Revision: "sha256:revision"}, EvidenceExpiresAt: expires}
	lifecycle := &fakeMachineLifecycle{}
	handler := MachineHandler{Client: kube, Project: project, Lifecycle: lifecycle, Inputs: machineInputsFunc(func(_ context.Context, gotProject controller.ProjectIdentity, gotClaim *fleetv1.Machine, gotNamespace *corev1.Namespace) (MachineInputs, error) {
		if gotProject != project || gotClaim.UID != claim.UID || gotNamespace.UID != namespace.UID {
			t.Fatalf("resolver received wrong lifetimes: %#v %#v %#v", gotProject, gotClaim.UID, gotNamespace.UID)
		}
		return inputs, nil
	})}
	request := ProjectRequest{AuthorityID: project.AuthorityID, ProjectUID: project.UID, Namespace: claim.Namespace, Name: claim.Name, UID: claim.UID}
	return handler, kube, lifecycle, request, expires
}
