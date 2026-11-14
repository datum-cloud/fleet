package nso

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	networkingv1 "go.datum.net/network-services-operator/api/v1alpha"

	fleetnetwork "go.datum.net/fleet/internal/network"
)

type testAuthorizer struct {
	t *testing.T
}

func (a testAuthorizer) AuthorizeNetworkUse(_ context.Context, request fleetnetwork.UseRequest) (fleetnetwork.Decision, error) {
	if request.PrincipalUID != "principal-uid" || request.NetworkUID != "network-uid" || request.NamespaceUID != "namespace-uid" {
		a.t.Fatalf("caller or lifetime identity lost: %#v", request)
	}
	return fleetnetwork.Decision{ID: "network-decision", Revision: "policy-r1"}, nil
}

type memoryGuard struct {
	mu              sync.Mutex
	tokens          map[GuardRequest]string
	uids            map[GuardRequest]types.UID
	closed          map[GuardRequest]bool
	failReserveName string
	failCloseName   string
}

func (g *memoryGuard) CreateOrRecover(_ context.Context, request GuardRequest, token string, effect func() (types.UID, error)) (types.UID, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if token == "" || g.tokens[request] != token || g.closed[request] {
		return "", errors.New("create fenced")
	}
	if g.uids[request] != "" {
		return g.uids[request], nil
	}
	uid, err := effect()
	if err != nil || uid == "" {
		return "", err
	}
	g.uids[request] = uid
	return uid, nil
}

func (g *memoryGuard) Close(_ context.Context, request GuardRequest, token string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if request.Name == g.failCloseName || token == "" || g.tokens[request] != token {
		return errors.New("unknown guard")
	}
	g.closed[request] = true
	return nil
}

func (g *memoryGuard) Reserve(_ context.Context, request GuardRequest) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if request.Name == g.failReserveName {
		return "", errors.New("reserve failed")
	}
	if token := g.tokens[request]; token != "" {
		return token, nil
	}
	token := "guard-" + request.Name
	g.tokens[request] = token
	return token, nil
}

func (g *memoryGuard) Verify(_ context.Context, request GuardRequest, token string, uid types.UID) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if token == "" || uid == "" || g.tokens[request] != token || g.uids[request] != uid {
		return errors.New("unproved object")
	}
	return nil
}

func (g *memoryGuard) Proof(_ context.Context, request GuardRequest, token string) (GuardProof, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if token == "" || g.tokens[request] != token || !g.closed[request] {
		return GuardProof{}, errors.New("unproved guard")
	}
	return GuardProof{CreatedUID: g.uids[request]}, nil
}

type testVerifier struct{ err error }

func (v testVerifier) Verify(_ context.Context, plan fleetnetwork.InterfacePlan) error {
	if plan.NetworkUID == "" {
		return fleetnetwork.ErrEvidenceStale
	}
	return v.err
}

type createClient struct {
	client.Client
	mu           sync.Mutex
	creates      int
	loseResponse bool
	deletes      int
	loseDeleteAt int
}

func (c *createClient) Delete(ctx context.Context, object client.Object, options ...client.DeleteOption) error {
	c.mu.Lock()
	c.deletes++
	lose := c.loseDeleteAt == c.deletes
	c.mu.Unlock()
	if err := c.Client.Delete(ctx, object, options...); err != nil {
		return err
	}
	if lose {
		return errors.New("delete response lost")
	}
	return nil
}

func (c *createClient) Create(ctx context.Context, object client.Object, options ...client.CreateOption) error {
	c.mu.Lock()
	c.creates++
	if object.GetUID() == "" {
		object.SetUID(types.UID(fmt.Sprintf("created-%d", c.creates)))
	}
	lose := c.loseResponse
	c.loseResponse = false
	c.mu.Unlock()
	if err := c.Client.Create(ctx, object, options...); err != nil {
		return err
	}
	if lose {
		return errors.New("response lost")
	}
	return nil
}

func newAdapterFixture(t *testing.T) (*Adapter, *createClient, fleetnetwork.PreflightRequest) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := networkingv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	network := &networkingv1.Network{ObjectMeta: metav1.ObjectMeta{Name: "management", Namespace: "project", UID: "network-uid", Generation: 3}, Spec: networkingv1.NetworkSpec{IPFamilies: []networkingv1.IPFamily{networkingv1.IPv4Protocol, networkingv1.IPv6Protocol}}, Status: networkingv1.NetworkStatus{Conditions: []metav1.Condition{{Type: networkingv1.NetworkReady, Status: metav1.ConditionTrue, ObservedGeneration: 3}}}}
	base := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&networkingv1.Network{}, &networkingv1.NetworkInterfaceClaim{}).WithObjects(network).Build()
	kube := &createClient{Client: base}
	location := fleetnetwork.LocationAuthority{Name: "us-central-1", UID: "location-uid", Revision: "location-r1"}
	adapter, err := New(kube, testAuthorizer{t: t}, &memoryGuard{tokens: map[GuardRequest]string{}, uids: map[GuardRequest]types.UID{}, closed: map[GuardRequest]bool{}}, testVerifier{}, location)
	if err != nil {
		t.Fatal(err)
	}
	adapter.now = func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) }
	request := fleetnetwork.PreflightRequest{ClaimNamespace: "project", ClaimUID: "claim-uid", NamespaceUID: "namespace-uid", PrincipalUID: "principal-uid", Location: location, Interfaces: []fleetnetwork.InterfaceRequest{{Name: "management", NetworkName: "management", NetworkNS: "project", IPFamilies: []string{"IPv4"}, ReclaimPolicy: "Delete"}}}
	return adapter, kube, request
}

func TestPreflightFailsClosedOnStaleNetworkEvidence(t *testing.T) {
	adapter, kube, request := newAdapterFixture(t)
	var network networkingv1.Network
	if err := kube.Get(context.Background(), client.ObjectKey{Namespace: "project", Name: "management"}, &network); err != nil {
		t.Fatal(err)
	}
	network.Status.Conditions[0].ObservedGeneration = 2
	if err := kube.Status().Update(context.Background(), &network); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Preflight(context.Background(), request); !errors.Is(err, fleetnetwork.ErrEvidenceStale) {
		t.Fatalf("Preflight error = %v", err)
	}
}

func TestPreflightRequiresExactAddressClassLifetime(t *testing.T) {
	adapter, _, request := newAdapterFixture(t)
	request.Interfaces[0].AddressClasses = []string{"public"}
	if _, err := adapter.Preflight(context.Background(), request); !errors.Is(err, fleetnetwork.ErrAuthorizationUnavailable) {
		t.Fatalf("Preflight error = %v", err)
	}
}

func TestLostCreateResponseRecoversExactGuardedClaimAndConcurrentEnsureConverges(t *testing.T) {
	adapter, kube, request := newAdapterFixture(t)
	plan, err := adapter.Preflight(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	ensure := fleetnetwork.EnsureRequest{ClaimUID: request.ClaimUID, BindingID: "binding-1", Plan: plan}
	admitted, err := adapter.Ensure(context.Background(), ensure)
	if !errors.Is(err, fleetnetwork.ErrPending) || len(admitted.Lifecycle) != 1 || kube.creates != 0 {
		t.Fatalf("admission = %#v, %v creates=%d", admitted, err, kube.creates)
	}
	ensure.Previous = admitted
	kube.loseResponse = true
	results := make(chan fleetnetwork.Observation, 2)
	errorsCh := make(chan error, 2)
	for range 2 {
		go func() {
			observation, err := adapter.Ensure(context.Background(), ensure)
			results <- observation
			errorsCh <- err
		}()
	}
	first, second := <-results, <-results
	for range 2 {
		if err := <-errorsCh; !errors.Is(err, fleetnetwork.ErrPending) {
			t.Fatalf("Ensure error = %v", err)
		}
	}
	if len(first.Interfaces) != 1 || len(second.Interfaces) != 1 || first.Interfaces[0].ClaimUID == "" || first.Interfaces[0].ClaimUID != second.Interfaces[0].ClaimUID {
		t.Fatalf("observations did not converge: %#v %#v", first, second)
	}
	var claims networkingv1.NetworkInterfaceClaimList
	if err := kube.List(context.Background(), &claims, client.InNamespace("project")); err != nil || len(claims.Items) != 1 {
		t.Fatalf("claims = %d, err %v", len(claims.Items), err)
	}
}

func TestForeignSameNameClaimIsNotAdopted(t *testing.T) {
	adapter, kube, request := newAdapterFixture(t)
	plan, err := adapter.Preflight(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	foreign := &networkingv1.NetworkInterfaceClaim{ObjectMeta: metav1.ObjectMeta{Name: plan.Interfaces[0].ClaimName, Namespace: "project", UID: "foreign-uid", Annotations: map[string]string{claimUIDAnnotation: string(request.ClaimUID)}}, Spec: networkingv1.NetworkInterfaceClaimSpec{Network: networkingv1.LocalNetworkRef{Name: "management"}}}
	if err := kube.Create(context.Background(), foreign); err != nil {
		t.Fatal(err)
	}
	ensure := fleetnetwork.EnsureRequest{ClaimUID: request.ClaimUID, BindingID: "binding-1", Plan: plan}
	admitted, admitErr := adapter.Ensure(context.Background(), ensure)
	if !errors.Is(admitErr, fleetnetwork.ErrPending) {
		t.Fatal(admitErr)
	}
	ensure.Previous = admitted
	_, err = adapter.Ensure(context.Background(), ensure)
	if !errors.Is(err, fleetnetwork.ErrObjectConflict) {
		t.Fatalf("Ensure error = %v", err)
	}
}

func TestReleaseRequiresRetainedDeleteAdmissionBeforeAbsence(t *testing.T) {
	adapter, kube, request := newAdapterFixture(t)
	plan, err := adapter.Preflight(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	observation, err := adapter.Ensure(context.Background(), fleetnetwork.EnsureRequest{ClaimUID: request.ClaimUID, BindingID: "binding-1", Plan: plan})
	if !errors.Is(err, fleetnetwork.ErrPending) {
		t.Fatal(err)
	}
	observation, err = adapter.Ensure(context.Background(), fleetnetwork.EnsureRequest{ClaimUID: request.ClaimUID, BindingID: "binding-1", Plan: plan, Previous: observation})
	if !errors.Is(err, fleetnetwork.ErrPending) {
		t.Fatal(err)
	}
	release := fleetnetwork.ReleaseRequest{ClaimUID: request.ClaimUID, BindingID: "binding-1", Plan: plan, Observation: observation, Lifecycle: observation.Lifecycle}
	first, err := adapter.Release(context.Background(), release)
	if !errors.Is(err, fleetnetwork.ErrPending) || len(first.Lifecycle) != 1 || !first.Lifecycle[0].DeleteAdmitted || first.Verified {
		t.Fatalf("first release = %#v, %v", first, err)
	}
	release.Lifecycle = first.Lifecycle
	second, err := adapter.Release(context.Background(), release)
	if !errors.Is(err, fleetnetwork.ErrPending) || second.Verified {
		t.Fatalf("second release = %#v, %v", second, err)
	}
	release.Lifecycle = second.Lifecycle
	third, err := adapter.Release(context.Background(), release)
	if err != nil || !third.Verified {
		t.Fatalf("third release = %#v, %v", third, err)
	}
	if _, err := adapter.Release(context.Background(), fleetnetwork.ReleaseRequest{ClaimUID: request.ClaimUID, BindingID: "binding-1", Plan: plan, Observation: observation}); !errors.Is(err, fleetnetwork.ErrPending) {
		t.Fatalf("missing retained admission should re-admit without delete: %v", err)
	}
	_ = kube
}

func TestReleaseBeforeEnsureAdmitsEveryDeleteWithoutCreating(t *testing.T) {
	adapter, kube, request := newAdapterFixture(t)
	request.Interfaces = append(request.Interfaces, fleetnetwork.InterfaceRequest{Name: "data", NetworkName: "management", NetworkNS: "project", ReclaimPolicy: "Delete"})
	plan, err := adapter.Preflight(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	release := fleetnetwork.ReleaseRequest{ClaimUID: request.ClaimUID, BindingID: "binding-1", Plan: plan}
	admitted, err := adapter.Release(context.Background(), release)
	if !errors.Is(err, fleetnetwork.ErrPending) || len(admitted.Lifecycle) != 2 || kube.creates != 0 {
		t.Fatalf("admitted=%#v err=%v creates=%d", admitted, err, kube.creates)
	}
	for _, state := range admitted.Lifecycle {
		if !state.CreateAdmitted || !state.DeleteAdmitted || state.DeleteCompleted {
			t.Fatalf("state=%#v", state)
		}
	}
	release.Lifecycle = admitted.Lifecycle
	completed, err := adapter.Release(context.Background(), release)
	if err != nil || !completed.Verified {
		t.Fatalf("completed=%#v err=%v", completed, err)
	}
}

func TestDeleteAdmissionFencesStaleCreateIntent(t *testing.T) {
	adapter, kube, request := newAdapterFixture(t)
	plan, err := adapter.Preflight(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	ensure := fleetnetwork.EnsureRequest{ClaimUID: request.ClaimUID, BindingID: "binding-1", Plan: plan}
	createIntent, err := adapter.Ensure(context.Background(), ensure)
	if !errors.Is(err, fleetnetwork.ErrPending) {
		t.Fatal(err)
	}
	release := fleetnetwork.ReleaseRequest{ClaimUID: request.ClaimUID, BindingID: "binding-1", Plan: plan}
	deleteIntent, err := adapter.Release(context.Background(), release)
	if !errors.Is(err, fleetnetwork.ErrPending) {
		t.Fatal(err)
	}
	release.Lifecycle = deleteIntent.Lifecycle
	completed, err := adapter.Release(context.Background(), release)
	if err != nil || !completed.Verified {
		t.Fatalf("release=%#v err=%v", completed, err)
	}
	ensure.Previous = createIntent
	if _, err := adapter.Ensure(context.Background(), ensure); !errors.Is(err, fleetnetwork.ErrObjectConflict) {
		t.Fatalf("stale Ensure error=%v", err)
	}
	if kube.creates != 0 {
		t.Fatalf("stale create effects=%d", kube.creates)
	}
}

func TestCopiedGuardTokenSameNameRecreationFailsUIDBinding(t *testing.T) {
	adapter, kube, request := newAdapterFixture(t)
	plan, err := adapter.Preflight(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	ensure := fleetnetwork.EnsureRequest{ClaimUID: request.ClaimUID, BindingID: "binding-1", Plan: plan}
	admitted, _ := adapter.Ensure(context.Background(), ensure)
	ensure.Previous = admitted
	observed, err := adapter.Ensure(context.Background(), ensure)
	if !errors.Is(err, fleetnetwork.ErrPending) {
		t.Fatal(err)
	}
	var original networkingv1.NetworkInterfaceClaim
	key := client.ObjectKey{Namespace: "project", Name: plan.Interfaces[0].ClaimName}
	if err := kube.Get(context.Background(), key, &original); err != nil {
		t.Fatal(err)
	}
	if err := kube.Delete(context.Background(), &original); err != nil {
		t.Fatal(err)
	}
	copy := original.DeepCopy()
	copy.ResourceVersion = ""
	copy.UID = "copied-uid"
	copy.DeletionTimestamp = nil
	if err := kube.Create(context.Background(), copy); err != nil {
		t.Fatal(err)
	}
	ensure.Previous = observed
	if _, err := adapter.Ensure(context.Background(), ensure); !errors.Is(err, fleetnetwork.ErrObjectConflict) {
		t.Fatalf("Ensure error=%v", err)
	}
}

func TestEnsureRevalidatesNetworkGenerationReadyAndExternalAuthority(t *testing.T) {
	adapter, kube, request := newAdapterFixture(t)
	plan, err := adapter.Preflight(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	ensure := fleetnetwork.EnsureRequest{ClaimUID: request.ClaimUID, BindingID: "binding-1", Plan: plan}
	admitted, _ := adapter.Ensure(context.Background(), ensure)
	ensure.Previous = admitted
	var network networkingv1.Network
	if err := kube.Get(context.Background(), client.ObjectKey{Namespace: "project", Name: "management"}, &network); err != nil {
		t.Fatal(err)
	}
	network.Generation++
	if err := kube.Update(context.Background(), &network); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Ensure(context.Background(), ensure); !errors.Is(err, fleetnetwork.ErrEvidenceStale) {
		t.Fatalf("generation error=%v", err)
	}

	adapter, _, request = newAdapterFixture(t)
	plan, _ = adapter.Preflight(context.Background(), request)
	ensure = fleetnetwork.EnsureRequest{ClaimUID: request.ClaimUID, BindingID: "binding-1", Plan: plan}
	admitted, _ = adapter.Ensure(context.Background(), ensure)
	ensure.Previous = admitted
	adapter.verifier = testVerifier{err: fleetnetwork.ErrEvidenceStale}
	if _, err := adapter.Ensure(context.Background(), ensure); !errors.Is(err, fleetnetwork.ErrEvidenceStale) {
		t.Fatalf("authority error=%v", err)
	}
}

func TestEnsureKeepsExistingProgrammedClaimAcrossNetworkGenerationChange(t *testing.T) {
	ctx := context.Background()
	adapter, kube, request := newAdapterFixture(t)
	plan, err := adapter.Preflight(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	ensure := fleetnetwork.EnsureRequest{ClaimUID: request.ClaimUID, BindingID: "binding-1", Plan: plan}
	admitted, err := adapter.Ensure(ctx, ensure)
	if !errors.Is(err, fleetnetwork.ErrPending) {
		t.Fatal(err)
	}
	ensure.Previous = admitted
	created, err := adapter.Ensure(ctx, ensure)
	if !errors.Is(err, fleetnetwork.ErrPending) {
		t.Fatal(err)
	}

	var claim networkingv1.NetworkInterfaceClaim
	key := client.ObjectKey{Namespace: "project", Name: plan.Interfaces[0].ClaimName}
	if err := kube.Get(ctx, key, &claim); err != nil {
		t.Fatal(err)
	}
	claim.Status.Addresses = []networkingv1.NetworkInterfaceAddress{{Address: "10.20.30.42/24", Family: networkingv1.IPv4Protocol, Primary: true}}
	for _, conditionType := range []string{networkingv1.NetworkInterfaceClaimAllocated, networkingv1.NetworkInterfaceClaimPrepared, networkingv1.NetworkInterfaceClaimProgrammed} {
		claim.Status.Conditions = append(claim.Status.Conditions, metav1.Condition{Type: conditionType, Status: metav1.ConditionTrue, ObservedGeneration: claim.Generation})
	}
	if err := kube.Status().Update(ctx, &claim); err != nil {
		t.Fatal(err)
	}
	ensure.Previous = created
	programmed, err := adapter.Ensure(ctx, ensure)
	if err != nil || !programmed.Programmed(plan) {
		t.Fatalf("programmed=%#v err=%v", programmed, err)
	}

	var network networkingv1.Network
	if err := kube.Get(ctx, client.ObjectKey{Namespace: "project", Name: "management"}, &network); err != nil {
		t.Fatal(err)
	}
	network.Generation++
	if err := kube.Update(ctx, &network); err != nil {
		t.Fatal(err)
	}
	ensure.Previous = programmed
	refreshed, err := adapter.Ensure(ctx, ensure)
	if err != nil || !refreshed.Programmed(plan) || len(refreshed.Interfaces[0].Addresses) != 1 || refreshed.Interfaces[0].Addresses[0].Address != "10.20.30.42/24" {
		t.Fatalf("refreshed=%#v err=%v", refreshed, err)
	}
}

func TestAdmissionFailuresNeverReturnPartialLifecycleVectors(t *testing.T) {
	for _, phase := range []string{"reserve", "close"} {
		t.Run(phase, func(t *testing.T) {
			adapter, _, request := newAdapterFixture(t)
			request.Interfaces = append(request.Interfaces, fleetnetwork.InterfaceRequest{Name: "data", NetworkName: "management", NetworkNS: "project", ReclaimPolicy: "Delete"})
			plan, err := adapter.Preflight(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			guard := adapter.guard.(*memoryGuard)
			secondName := plan.Interfaces[1].ClaimName
			if phase == "reserve" {
				guard.failReserveName = secondName
			} else {
				guard.failCloseName = secondName
			}
			if observation, err := adapter.Ensure(context.Background(), fleetnetwork.EnsureRequest{ClaimUID: request.ClaimUID, BindingID: "binding-1", Plan: plan}); phase == "reserve" && (err == nil || len(observation.Lifecycle) != 0) {
				t.Fatalf("Ensure=%#v err=%v", observation, err)
			}
			released, err := adapter.Release(context.Background(), fleetnetwork.ReleaseRequest{ClaimUID: request.ClaimUID, BindingID: "binding-1", Plan: plan})
			if err == nil || len(released.Lifecycle) != 0 {
				t.Fatalf("Release=%#v err=%v", released, err)
			}
		})
	}
}

func TestExistingExactClaimRecoversGuardUIDAfterAPICreateCommit(t *testing.T) {
	adapter, kube, request := newAdapterFixture(t)
	plan, err := adapter.Preflight(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	ensure := fleetnetwork.EnsureRequest{ClaimUID: request.ClaimUID, BindingID: "binding-1", Plan: plan}
	intent, err := adapter.Ensure(context.Background(), ensure)
	if !errors.Is(err, fleetnetwork.ErrPending) {
		t.Fatal(err)
	}
	desired := desiredClaim(ensure, plan.Interfaces[0], intent.Lifecycle[0].GuardToken)
	desired.UID = "api-committed-uid"
	if err := kube.Create(context.Background(), desired); err != nil {
		t.Fatal(err)
	}
	ensure.Previous = intent
	observation, err := adapter.Ensure(context.Background(), ensure)
	if !errors.Is(err, fleetnetwork.ErrPending) || len(observation.Lifecycle) != 1 || observation.Lifecycle[0].CreatedUID != desired.UID {
		t.Fatalf("observation=%#v err=%v", observation, err)
	}
}

func TestDirectReleaseRecoversGuardUIDAfterAPICreateCommit(t *testing.T) {
	for _, reclaimPolicy := range []string{"Delete", "Retain"} {
		t.Run(reclaimPolicy, func(t *testing.T) {
			adapter, kube, request := newAdapterFixture(t)
			request.Interfaces[0].ReclaimPolicy = reclaimPolicy
			plan, err := adapter.Preflight(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			ensure := fleetnetwork.EnsureRequest{ClaimUID: request.ClaimUID, BindingID: "binding-1", Plan: plan}
			intent, err := adapter.Ensure(context.Background(), ensure)
			if !errors.Is(err, fleetnetwork.ErrPending) {
				t.Fatal(err)
			}
			desired := desiredClaim(ensure, plan.Interfaces[0], intent.Lifecycle[0].GuardToken)
			desired.UID = types.UID("api-committed-" + reclaimPolicy)
			if err := kube.Create(context.Background(), desired); err != nil {
				t.Fatal(err)
			}
			released, err := adapter.Release(context.Background(), fleetnetwork.ReleaseRequest{ClaimUID: request.ClaimUID, BindingID: "binding-1", Plan: plan, Lifecycle: intent.Lifecycle})
			if !errors.Is(err, fleetnetwork.ErrPending) || len(released.Lifecycle) != 1 || released.Lifecycle[0].CreatedUID != desired.UID || !released.Lifecycle[0].DeleteAdmitted {
				t.Fatalf("Release=%#v err=%v", released, err)
			}
			guardRequest := GuardRequest{Namespace: plan.Interfaces[0].NetworkNamespace, Name: plan.Interfaces[0].ClaimName, ClaimUID: request.ClaimUID, BindingID: "binding-1", PlanDigest: plan.RequestDigest}
			if err := adapter.guard.Verify(context.Background(), guardRequest, released.Lifecycle[0].GuardToken, desired.UID); err != nil {
				t.Fatalf("release did not retain the recovered UID binding: %v", err)
			}
		})
	}
}

func TestDirectReleaseRecoverySurvivesLaterInterfaceAdmissionFailure(t *testing.T) {
	adapter, kube, request := newAdapterFixture(t)
	request.Interfaces = append(request.Interfaces, fleetnetwork.InterfaceRequest{Name: "data", NetworkName: "management", NetworkNS: "project", ReclaimPolicy: "Delete"})
	plan, err := adapter.Preflight(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	ensure := fleetnetwork.EnsureRequest{ClaimUID: request.ClaimUID, BindingID: "binding-1", Plan: plan}
	intent, err := adapter.Ensure(context.Background(), ensure)
	if !errors.Is(err, fleetnetwork.ErrPending) || len(intent.Lifecycle) != 2 {
		t.Fatalf("Ensure=%#v err=%v", intent, err)
	}
	desired := desiredClaim(ensure, plan.Interfaces[0], intent.Lifecycle[0].GuardToken)
	desired.UID = "api-committed-first"
	if err := kube.Create(context.Background(), desired); err != nil {
		t.Fatal(err)
	}
	guard := adapter.guard.(*memoryGuard)
	guard.failCloseName = plan.Interfaces[1].ClaimName
	release := fleetnetwork.ReleaseRequest{ClaimUID: request.ClaimUID, BindingID: "binding-1", Plan: plan, Lifecycle: intent.Lifecycle}
	if failed, err := adapter.Release(context.Background(), release); err == nil || len(failed.Lifecycle) != 0 {
		t.Fatalf("first Release=%#v err=%v", failed, err)
	}
	guard.failCloseName = ""
	recovered, err := adapter.Release(context.Background(), release)
	if !errors.Is(err, fleetnetwork.ErrPending) || len(recovered.Lifecycle) != 2 || recovered.Lifecycle[0].CreatedUID != desired.UID {
		t.Fatalf("retry Release=%#v err=%v", recovered, err)
	}
}

func TestMalformedLifecycleVectorsFailClosed(t *testing.T) {
	adapter, kube, request := newAdapterFixture(t)
	plan, err := adapter.Preflight(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	state := fleetnetwork.InterfaceLifecycleState{Name: plan.Interfaces[0].Name, GuardToken: "copied", CreateAdmitted: true}
	malformed := []fleetnetwork.InterfaceLifecycleState{state, state}
	if observation, err := adapter.Ensure(context.Background(), fleetnetwork.EnsureRequest{ClaimUID: request.ClaimUID, BindingID: "binding-1", Plan: plan, Previous: fleetnetwork.Observation{Lifecycle: malformed}}); !errors.Is(err, fleetnetwork.ErrObjectConflict) || len(observation.Lifecycle) != 0 {
		t.Fatalf("Ensure=%#v err=%v", observation, err)
	}
	if released, err := adapter.Release(context.Background(), fleetnetwork.ReleaseRequest{ClaimUID: request.ClaimUID, BindingID: "binding-1", Plan: plan, Lifecycle: malformed}); !errors.Is(err, fleetnetwork.ErrObjectConflict) || len(released.Lifecycle) != 0 {
		t.Fatalf("Release=%#v err=%v", released, err)
	}
	duplicateObservation := []fleetnetwork.InterfaceObservation{{Name: plan.Interfaces[0].Name, ClaimUID: "first"}, {Name: plan.Interfaces[0].Name, ClaimUID: "second"}}
	if observation, err := adapter.Ensure(context.Background(), fleetnetwork.EnsureRequest{ClaimUID: request.ClaimUID, BindingID: "binding-1", Plan: plan, Previous: fleetnetwork.Observation{Interfaces: duplicateObservation}}); !errors.Is(err, fleetnetwork.ErrObjectConflict) || len(observation.Lifecycle) != 0 {
		t.Fatalf("Ensure duplicate observation=%#v err=%v", observation, err)
	}
	if released, err := adapter.Release(context.Background(), fleetnetwork.ReleaseRequest{ClaimUID: request.ClaimUID, BindingID: "binding-1", Plan: plan, Observation: fleetnetwork.Observation{Interfaces: duplicateObservation}}); !errors.Is(err, fleetnetwork.ErrObjectConflict) || len(released.Lifecycle) != 0 {
		t.Fatalf("Release duplicate observation=%#v err=%v", released, err)
	}
	if kube.creates != 0 {
		t.Fatalf("malformed lifecycle caused %d creates", kube.creates)
	}
}

func TestRetainNeverCreatedCompletesButCreatedUnknownBindingStaysPending(t *testing.T) {
	t.Run("never created", func(t *testing.T) {
		adapter, _, request := newAdapterFixture(t)
		request.Interfaces[0].ReclaimPolicy = "Retain"
		plan, _ := adapter.Preflight(context.Background(), request)
		release := fleetnetwork.ReleaseRequest{ClaimUID: request.ClaimUID, BindingID: "binding-1", Plan: plan}
		intent, err := adapter.Release(context.Background(), release)
		if !errors.Is(err, fleetnetwork.ErrPending) {
			t.Fatal(err)
		}
		release.Lifecycle = intent.Lifecycle
		completed, err := adapter.Release(context.Background(), release)
		if err != nil || !completed.Verified || !completed.Lifecycle[0].NoBindingVerified {
			t.Fatalf("completed=%#v err=%v", completed, err)
		}
	})
	t.Run("created binding unknown", func(t *testing.T) {
		adapter, _, request := newAdapterFixture(t)
		request.Interfaces[0].ReclaimPolicy = "Retain"
		plan, _ := adapter.Preflight(context.Background(), request)
		ensure := fleetnetwork.EnsureRequest{ClaimUID: request.ClaimUID, BindingID: "binding-1", Plan: plan}
		intent, _ := adapter.Ensure(context.Background(), ensure)
		ensure.Previous = intent
		created, err := adapter.Ensure(context.Background(), ensure)
		if !errors.Is(err, fleetnetwork.ErrPending) {
			t.Fatal(err)
		}
		release := fleetnetwork.ReleaseRequest{ClaimUID: request.ClaimUID, BindingID: "binding-1", Plan: plan, Lifecycle: created.Lifecycle}
		admitted, err := adapter.Release(context.Background(), release)
		if !errors.Is(err, fleetnetwork.ErrPending) {
			t.Fatal(err)
		}
		release.Lifecycle = admitted.Lifecycle
		deleted, err := adapter.Release(context.Background(), release)
		if !errors.Is(err, fleetnetwork.ErrPending) {
			t.Fatalf("delete err=%v", err)
		}
		release.Lifecycle = deleted.Lifecycle
		if completed, err := adapter.Release(context.Background(), release); !errors.Is(err, fleetnetwork.ErrPending) || completed.Verified {
			t.Fatalf("completed=%#v err=%v", completed, err)
		}
	})
	t.Run("created and exact bound identity", func(t *testing.T) {
		adapter, kube, request := newAdapterFixture(t)
		request.Interfaces[0].ReclaimPolicy = "Retain"
		plan, _ := adapter.Preflight(context.Background(), request)
		ensure := fleetnetwork.EnsureRequest{ClaimUID: request.ClaimUID, BindingID: "binding-1", Plan: plan}
		intent, _ := adapter.Ensure(context.Background(), ensure)
		ensure.Previous = intent
		created, err := adapter.Ensure(context.Background(), ensure)
		if !errors.Is(err, fleetnetwork.ErrPending) {
			t.Fatal(err)
		}
		iface := &networkingv1.NetworkInterface{ObjectMeta: metav1.ObjectMeta{Name: "retained-iface", Namespace: "project", UID: "retained-iface-uid"}, Spec: networkingv1.NetworkInterfaceSpec{Network: networkingv1.LocalNetworkRef{Name: "management"}, ClaimRef: &networkingv1.NetworkInterfaceClaimRef{Name: plan.Interfaces[0].ClaimName}}, Status: networkingv1.NetworkInterfaceStatus{Phase: networkingv1.NetworkInterfacePhaseBound}}
		if err := kube.Create(context.Background(), iface); err != nil {
			t.Fatal(err)
		}
		var claim networkingv1.NetworkInterfaceClaim
		if err := kube.Get(context.Background(), client.ObjectKey{Namespace: "project", Name: plan.Interfaces[0].ClaimName}, &claim); err != nil {
			t.Fatal(err)
		}
		claim.Status.NetworkInterfaceRef = &networkingv1.LocalNetworkInterfaceRef{Name: iface.Name}
		if err := kube.Status().Update(context.Background(), &claim); err != nil {
			t.Fatal(err)
		}
		release := fleetnetwork.ReleaseRequest{ClaimUID: request.ClaimUID, BindingID: "binding-1", Plan: plan, Lifecycle: created.Lifecycle}
		admitted, err := adapter.Release(context.Background(), release)
		if !errors.Is(err, fleetnetwork.ErrPending) {
			t.Fatal(err)
		}
		release.Lifecycle = admitted.Lifecycle
		deleted, err := adapter.Release(context.Background(), release)
		if !errors.Is(err, fleetnetwork.ErrPending) || deleted.Lifecycle[0].RetainedInterfaceUID != iface.UID {
			t.Fatalf("deleted=%#v err=%v", deleted, err)
		}
		if err := kube.Get(context.Background(), client.ObjectKeyFromObject(iface), iface); err != nil {
			t.Fatal(err)
		}
		iface.Spec.ClaimRef = nil
		iface.Status.Phase = networkingv1.NetworkInterfacePhaseAvailable
		if err := kube.Update(context.Background(), iface); err != nil {
			t.Fatal(err)
		}
		release.Lifecycle = deleted.Lifecycle
		completed, err := adapter.Release(context.Background(), release)
		if err != nil || !completed.Verified {
			t.Fatalf("completed=%#v err=%v", completed, err)
		}
	})
}

func TestMultiInterfaceDeleteLostResponseRetainsFullRetryVector(t *testing.T) {
	adapter, kube, request := newAdapterFixture(t)
	request.Interfaces = append(request.Interfaces, fleetnetwork.InterfaceRequest{Name: "data", NetworkName: "management", NetworkNS: "project", ReclaimPolicy: "Delete"})
	plan, err := adapter.Preflight(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	ensure := fleetnetwork.EnsureRequest{ClaimUID: request.ClaimUID, BindingID: "binding-1", Plan: plan}
	intent, _ := adapter.Ensure(context.Background(), ensure)
	ensure.Previous = intent
	observed, err := adapter.Ensure(context.Background(), ensure)
	if !errors.Is(err, fleetnetwork.ErrPending) {
		t.Fatal(err)
	}
	release := fleetnetwork.ReleaseRequest{ClaimUID: request.ClaimUID, BindingID: "binding-1", Plan: plan, Observation: observed, Lifecycle: observed.Lifecycle}
	deleteIntent, err := adapter.Release(context.Background(), release)
	if !errors.Is(err, fleetnetwork.ErrPending) {
		t.Fatal(err)
	}
	release.Lifecycle = deleteIntent.Lifecycle
	kube.loseDeleteAt = 1
	partial, err := adapter.Release(context.Background(), release)
	if err == nil || len(partial.Lifecycle) != len(plan.Interfaces) {
		t.Fatalf("partial=%#v err=%v", partial, err)
	}
	release.Lifecycle = partial.Lifecycle
	for attempt := 0; attempt < 4; attempt++ {
		next, nextErr := adapter.Release(context.Background(), release)
		release.Lifecycle = next.Lifecycle
		if nextErr == nil {
			if !next.Verified {
				t.Fatal("completed without verification")
			}
			return
		}
		if !errors.Is(nextErr, fleetnetwork.ErrPending) {
			t.Fatalf("retry=%#v err=%v", next, nextErr)
		}
	}
	t.Fatal("release did not converge")
}
