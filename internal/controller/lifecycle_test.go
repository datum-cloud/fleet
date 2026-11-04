package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/executor"
	fleetnetwork "go.datum.net/fleet/internal/network"
	"go.datum.net/fleet/internal/simulator"
)

type memoryExecutor struct {
	requests []simulator.Request
	results  map[string]simulator.OperationResult
	unknown  bool
	malform  bool
}

type failingResolver struct{}

func (failingResolver) ResolveImage(context.Context, ImageRequest) (ResolvedImage, error) {
	return ResolvedImage{}, errors.New("resolver unavailable")
}

type countingResolver struct {
	calls int
}

func (r *countingResolver) ResolveImage(ctx context.Context, request ImageRequest) (ResolvedImage, error) {
	r.calls++
	return (LocalDigestImageResolver{}).ResolveImage(ctx, request)
}

type fakeNetworkLifecycle struct {
	preflights     int
	ensures        int
	releases       int
	plan           fleetnetwork.Plan
	observation    fleetnetwork.Observation
	partialEnsure  bool
	partialRelease bool
}

func (f *fakeNetworkLifecycle) Preflight(_ context.Context, request fleetnetwork.PreflightRequest) (fleetnetwork.Plan, error) {
	f.preflights++
	digest, _ := fleetnetwork.DigestRequests(request.Interfaces)
	f.plan = fleetnetwork.Plan{RequestDigest: digest, Location: request.Location}
	for _, iface := range request.Interfaces {
		f.plan.Interfaces = append(f.plan.Interfaces, fleetnetwork.InterfacePlan{Name: iface.Name, NetworkNamespace: request.ClaimNamespace, NetworkName: iface.NetworkName, NetworkUID: types.UID("network-" + iface.Name), NetworkGeneration: 1, IPFamilies: append([]string(nil), iface.IPFamilies...), ReclaimPolicy: iface.ReclaimPolicy, ClaimName: "guarded-" + iface.Name, Decision: fleetnetwork.Decision{ID: "network-decision-" + iface.Name, Revision: "policy-r1"}})
	}
	return f.plan, nil
}

func (f *fakeNetworkLifecycle) Ensure(_ context.Context, request fleetnetwork.EnsureRequest) (fleetnetwork.Observation, error) {
	f.ensures++
	if request.Plan.RequestDigest != f.plan.RequestDigest {
		return fleetnetwork.Observation{}, fleetnetwork.ErrObjectConflict
	}
	if f.partialEnsure {
		return fleetnetwork.Observation{Lifecycle: []fleetnetwork.InterfaceLifecycleState{{Name: request.Plan.Interfaces[0].Name, GuardToken: "partial", CreateAdmitted: true}}}, fleetnetwork.ErrPending
	}
	return f.observation, nil
}

func (f *fakeNetworkLifecycle) Release(_ context.Context, request fleetnetwork.ReleaseRequest) (fleetnetwork.ReleaseObservation, error) {
	f.releases++
	if f.partialRelease {
		return fleetnetwork.ReleaseObservation{Lifecycle: []fleetnetwork.InterfaceLifecycleState{{Name: request.Plan.Interfaces[0].Name, GuardToken: "partial", CreateAdmitted: true, DeleteAdmitted: true}}}, fleetnetwork.ErrPending
	}
	states := append([]fleetnetwork.InterfaceLifecycleState(nil), request.Lifecycle...)
	if len(states) == 0 {
		for _, planned := range request.Plan.Interfaces {
			states = append(states, fleetnetwork.InterfaceLifecycleState{Name: planned.Name, GuardToken: "guard-" + planned.Name, CreateAdmitted: true, DeleteAdmitted: true})
		}
		return fleetnetwork.ReleaseObservation{Lifecycle: states}, fleetnetwork.ErrPending
	}
	for i := range states {
		if !states[i].DeleteAdmitted {
			return fleetnetwork.ReleaseObservation{}, fleetnetwork.ErrObjectConflict
		}
		states[i].DeleteCompleted = true
	}
	return fleetnetwork.ReleaseObservation{Verified: true, Lifecycle: states, Revision: "release-r1"}, nil
}

func (e *memoryExecutor) Execute(_ context.Context, request simulator.Request) (simulator.OperationResult, error) {
	e.requests = append(e.requests, request)
	now := time.Now()
	evidence := map[string]string{"cleanup": "clean", "install": "installed"}[request.Kind]
	result := simulator.OperationResult{ID: request.ID, Kind: request.Kind, Phase: "Succeeded", Result: evidence, CompletedAt: &now}
	if e.malform {
		result.CompletedAt = nil
	}
	e.results[request.ID] = result
	if e.unknown {
		e.unknown = false
		return simulator.OperationResult{}, executor.ErrOutcomeUnknown
	}
	return result, nil
}
func (e *memoryExecutor) Lookup(_ context.Context, id string) (simulator.OperationResult, bool, error) {
	result, ok := e.results[id]
	return result, ok, nil
}

type fixture struct {
	coordinator *Coordinator
	executor    *memoryExecutor
	now         time.Time
	ids         int
	mu          sync.Mutex
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	kube := fake.NewClientBuilder().WithScheme(scheme).Build()
	exec := &memoryExecutor{results: map[string]simulator.OperationResult{}}
	coordinator, err := NewCoordinator(kube, "private", exec, LocalDigestImageResolver{}, nil, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{coordinator: coordinator, executor: exec, now: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	coordinator.now = func() time.Time { return f.now }
	coordinator.newID = func() (string, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.ids++
		return fmt.Sprintf("id-%d", f.ids), nil
	}
	return f
}

func (f *fixture) request(claimUID types.UID) AllocationRequest {
	claim := &fleetv1.MachineClaim{ObjectMeta: metav1.ObjectMeta{Namespace: "project", Name: "claim-" + string(claimUID), UID: claimUID, Finalizers: []string{MachineClaimFinalizer}}, Spec: fleetv1.MachineClaimSpec{MachineClassRef: fleetv1.LocalReference{Name: "class"}, MachineProfileRef: fleetv1.LocalReference{Name: "profile"}}}
	class := &fleetv1.MachineClass{ObjectMeta: metav1.ObjectMeta{Name: "class", UID: "class-uid"}, Spec: fleetv1.MachineClassSpec{Capacity: &fleetv1.CapacityPolicy{Machines: fleetv1.MachineSelector{Namespace: "provider"}}}}
	profile := &fleetv1.MachineProfile{ObjectMeta: metav1.ObjectMeta{Name: "profile", UID: "profile-uid"}, Spec: fleetv1.MachineProfileSpec{CPU: fleetv1.CPURequirement{Architecture: "amd64"}, Memory: fleetv1.MemoryRequirement{}}}
	conditions := []metav1.Condition{}
	for _, typ := range []string{"Prepared", "Healthy", "Available"} {
		conditions = append(conditions, metav1.Condition{Type: typ, Status: metav1.ConditionTrue, ObservedGeneration: 2, LastTransitionTime: metav1.Now()})
	}
	machine := fleetv1.Machine{ObjectMeta: metav1.ObjectMeta{Namespace: "provider", Name: "machine-1", UID: "machine-uid", Generation: 2}, Spec: fleetv1.MachineSpec{AllocationPolicy: "Enabled", ProviderRef: fleetv1.TypedLocalReference{APIGroup: "test", Kind: "Host", Name: "host-1"}}, Status: fleetv1.MachineStatus{Hardware: fleetv1.Hardware{CPU: fleetv1.HardwareCPU{Architecture: "amd64", Cores: 24}, Memory: fleetv1.HardwareMemory{Capacity: "128Gi"}}, Conditions: conditions}}
	return AllocationRequest{Claim: claim, Class: class, Profile: profile, Machines: []fleetv1.Machine{machine}, EvidenceObservedAt: map[types.UID]time.Time{machine.UID: f.now}, NamespaceUID: "namespace-uid", Admission: AdmissionAuthority{PrincipalUID: "user-uid", DecisionID: "decision-1"}}
}

func TestAllocationReleaseAndEpochReuse(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	first, second := f.request("one"), f.request("two")
	if err := f.coordinator.EnsureClaimCustody(ctx, first.Claim, first.NamespaceUID, first.Admission); err != nil {
		t.Fatal(err)
	}
	if err := f.coordinator.EnsureClaimCustody(ctx, second.Claim, second.NamespaceUID, second.Admission); err != nil {
		t.Fatal(err)
	}
	record, err := f.coordinator.Allocate(ctx, first)
	if err != nil || record.State != AssetCommitted || record.Epoch != 1 {
		t.Fatalf("Allocate = %#v, %v", record, err)
	}
	if _, err := f.coordinator.Allocate(ctx, second); !errors.Is(err, ErrInsufficientCapacity) {
		t.Fatalf("second allocation error = %v", err)
	}
	released, err := f.coordinator.Release(ctx, first.Claim.UID)
	if err != nil || !released.SafeToRemoveFinalizer || len(f.executor.requests) != 1 || f.executor.requests[0].Kind != "cleanup" {
		t.Fatalf("Release = %#v, %v, requests=%#v", released, err, f.executor.requests)
	}
	if again, err := f.coordinator.Release(ctx, first.Claim.UID); err != nil || !again.SafeToRemoveFinalizer {
		t.Fatalf("idempotent Release = %#v, %v", again, err)
	}
	record, err = f.coordinator.Allocate(ctx, second)
	if err != nil || record.Epoch != 2 || record.ClaimUID != second.Claim.UID {
		t.Fatalf("reallocate = %#v, %v", record, err)
	}
}

func TestConcurrentDifferingCandidatesSelectOnlyOneAsset(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	left, right := f.request("claim"), f.request("claim")
	right.Machines[0].Name, right.Machines[0].UID, right.Machines[0].Spec.ProviderRef.Name = "machine-2", "machine-2-uid", "host-2"
	right.EvidenceObservedAt = map[types.UID]time.Time{right.Machines[0].UID: f.now}
	if err := f.coordinator.EnsureClaimCustody(ctx, left.Claim, left.NamespaceUID, left.Admission); err != nil {
		t.Fatal(err)
	}
	errorsCh := make(chan error, 2)
	go func() { _, err := f.coordinator.Allocate(ctx, left); errorsCh <- err }()
	go func() { _, err := f.coordinator.Allocate(ctx, right); errorsCh <- err }()
	<-errorsCh
	<-errorsCh
	var secrets corev1.SecretList
	if err := f.coordinator.client.List(ctx, &secrets); err != nil {
		t.Fatal(err)
	}
	assets := 0
	for i := range secrets.Items {
		if len(secrets.Items[i].Name) >= len(assetRecordPrefix) && secrets.Items[i].Name[:len(assetRecordPrefix)] == assetRecordPrefix {
			assets++
		}
	}
	if assets != 1 {
		t.Fatalf("asset records = %d, want 1", assets)
	}
}

func TestOccupiedSelectedCandidateClearsOnlyUnmaterializedSelection(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	owner, waiter := f.request("owner"), f.request("waiter")
	for _, r := range []*AllocationRequest{&owner, &waiter} {
		if err := f.coordinator.EnsureClaimCustody(ctx, r.Claim, r.NamespaceUID, r.Admission); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.coordinator.Allocate(ctx, owner); err != nil {
		t.Fatal(err)
	}
	second := waiter.Machines[0]
	second.Name, second.UID, second.Spec.ProviderRef.Name = "machine-2", "machine-2-uid", "host-2"
	waiter.Machines = []fleetv1.Machine{waiter.Machines[0], second}
	waiter.EvidenceObservedAt[second.UID] = f.now
	record, err := f.coordinator.Allocate(ctx, waiter)
	if err != nil || record.MachineUID != second.UID {
		t.Fatalf("Allocate = %#v, %v", record, err)
	}
}

func TestReleasedAssetCannotBeReusedBeforeCustodyRecordsCompletion(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	first, second := f.request("one"), f.request("two")
	for _, claim := range []*fleetv1.MachineClaim{first.Claim, second.Claim} {
		if err := f.coordinator.EnsureClaimCustody(ctx, claim, "namespace-uid", AdmissionAuthority{PrincipalUID: "user-uid", DecisionID: "decision-1"}); err != nil {
			t.Fatal(err)
		}
	}
	asset, err := f.coordinator.Allocate(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.coordinator.setAssetState(ctx, asset.AssetID, first.Claim.UID, AssetCommitted, AssetReleased); err != nil {
		t.Fatal(err)
	}
	if _, err := f.coordinator.Allocate(ctx, second); !errors.Is(err, ErrInsufficientCapacity) {
		t.Fatalf("reuse before custody completion error = %v", err)
	}
	if result, err := f.coordinator.Release(ctx, first.Claim.UID); err != nil || !result.SafeToRemoveFinalizer {
		t.Fatalf("finish release = %#v, %v", result, err)
	}
	if asset, err := f.coordinator.Allocate(ctx, second); err != nil || asset.Epoch != 2 {
		t.Fatalf("reuse after custody completion = %#v, %v", asset, err)
	}
}

func TestCustodyProvesUnboundAndMissingBoundAuthorityFailsClosed(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	request := f.request("claim")
	if _, err := f.coordinator.Release(ctx, request.Claim.UID); !errors.Is(err, ErrRecordInvalid) {
		t.Fatalf("missing custody error = %v", err)
	}
	if err := f.coordinator.EnsureClaimCustody(ctx, request.Claim, request.NamespaceUID, request.Admission); err != nil {
		t.Fatal(err)
	}
	result, err := f.coordinator.Release(ctx, request.Claim.UID)
	if err != nil || !result.SafeToRemoveFinalizer || len(f.executor.requests) != 0 {
		t.Fatalf("unbound Release = %#v, %v", result, err)
	}

	f = newFixture(t)
	request = f.request("claim")
	if err := f.coordinator.EnsureClaimCustody(ctx, request.Claim, request.NamespaceUID, request.Admission); err != nil {
		t.Fatal(err)
	}
	record, secret, err := f.coordinator.getCustody(ctx, request.Claim.UID)
	if err != nil {
		t.Fatal(err)
	}
	record.State, record.AssetID = CustodyBound, "missing"
	if err := encodeRecord(secret, record, "custody"); err != nil {
		t.Fatal(err)
	}
	if err := f.coordinator.client.Update(ctx, secret); err != nil {
		t.Fatal(err)
	}
	if _, err := f.coordinator.Release(ctx, request.Claim.UID); !errors.Is(err, ErrRecordInvalid) {
		t.Fatalf("missing authority error = %v", err)
	}
}

func TestCustodyAloneCannotAllocateWithoutLiveFleetFinalizer(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	request := f.request("claim")
	if err := f.coordinator.EnsureClaimCustody(ctx, request.Claim, request.NamespaceUID, request.Admission); err != nil {
		t.Fatal(err)
	}
	request.Claim.Finalizers = nil
	if _, err := f.coordinator.Allocate(ctx, request); err == nil {
		t.Fatal("allocation without finalizer succeeded")
	}
	now := metav1.Now()
	request.Claim.Finalizers = []string{MachineClaimFinalizer}
	request.Claim.DeletionTimestamp = &now
	if _, err := f.coordinator.Allocate(ctx, request); err == nil {
		t.Fatal("deleting claim allocated")
	}
}

func TestReleaseRecoversPreparationBeforeCustodyAdvanceWithoutCleanup(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	request := f.request("claim")
	if err := f.coordinator.EnsureClaimCustody(ctx, request.Claim, request.NamespaceUID, request.Admission); err != nil {
		t.Fatal(err)
	}
	proposal, _, err := f.coordinator.proposeAsset(ctx, request, &request.Machines[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := f.coordinator.selectCustody(ctx, request, proposal); err != nil {
		t.Fatal(err)
	}
	if _, won, err := f.coordinator.prepareAsset(ctx, request, &request.Machines[0]); err != nil || !won {
		t.Fatalf("prepare won=%v error=%v", won, err)
	}
	result, err := f.coordinator.Release(ctx, request.Claim.UID)
	if err != nil || !result.SafeToRemoveFinalizer || len(f.executor.requests) != 0 {
		t.Fatalf("Release = %#v, %v, requests=%#v", result, err, f.executor.requests)
	}
}

func TestReleaseBetweenSelectionAndCreateTombstonesProposalAgainstStaleAllocator(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	request := f.request("claim")
	if err := f.coordinator.EnsureClaimCustody(ctx, request.Claim, request.NamespaceUID, request.Admission); err != nil {
		t.Fatal(err)
	}
	proposal, reusable, err := f.coordinator.proposeAsset(ctx, request, &request.Machines[0])
	if err != nil || !reusable {
		t.Fatalf("proposal=%#v reusable=%v err=%v", proposal, reusable, err)
	}
	if err := f.coordinator.selectCustody(ctx, request, proposal); err != nil {
		t.Fatal(err)
	}
	result, err := f.coordinator.Release(ctx, request.Claim.UID)
	if err != nil || !result.SafeToRemoveFinalizer {
		t.Fatalf("Release=%#v err=%v", result, err)
	}
	if _, won, err := f.coordinator.prepareAsset(ctx, request, &request.Machines[0]); err != nil || won {
		t.Fatalf("stale prepare won=%v err=%v", won, err)
	}
	record, _, err := f.coordinator.getAsset(ctx, proposal.AssetID)
	if err != nil || record.State != AssetReleased || record.Epoch != proposal.Epoch {
		t.Fatalf("asset=%#v err=%v", record, err)
	}
}

func TestPreparingRetryRevalidatesInputsAndDuplicateRecordsBlock(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	request := f.request("claim")
	if err := f.coordinator.EnsureClaimCustody(ctx, request.Claim, request.NamespaceUID, request.Admission); err != nil {
		t.Fatal(err)
	}
	proposal, _, err := f.coordinator.proposeAsset(ctx, request, &request.Machines[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := f.coordinator.selectCustody(ctx, request, proposal); err != nil {
		t.Fatal(err)
	}
	prepared, won, err := f.coordinator.prepareAsset(ctx, request, &request.Machines[0])
	if err != nil || !won {
		t.Fatalf("prepare = %#v, %v, %v", prepared, won, err)
	}
	request.Profile.UID = "different-profile-lifetime"
	if _, err := f.coordinator.Allocate(ctx, request); !errors.Is(err, ErrRecordInvalid) {
		t.Fatalf("retry error = %v", err)
	}

	request.Profile.UID = "profile-uid"
	if err := f.coordinator.bindCustody(ctx, request.Claim, prepared.AssetID); err != nil {
		t.Fatal(err)
	}
	duplicate := *prepared
	duplicate.AssetID, duplicate.MachineUID, duplicate.MachineName = "provider/test/Host/host-2", "machine-2", "machine-2"
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "private", Name: assetRecordName(duplicate.AssetID)}}
	if err := encodeRecord(secret, &duplicate, "asset"); err != nil {
		t.Fatal(err)
	}
	if err := f.coordinator.client.Create(ctx, secret); err != nil {
		t.Fatal(err)
	}
	if _, err := f.coordinator.Allocate(ctx, request); !errors.Is(err, ErrRecordInvalid) {
		t.Fatalf("duplicate error = %v", err)
	}
}

func TestInstallUsesPrivatePredecessorAndRecoversUnknownOutcome(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	request := f.request("claim")
	if err := f.coordinator.EnsureClaimCustody(ctx, request.Claim, request.NamespaceUID, request.Admission); err != nil {
		t.Fatal(err)
	}
	asset, err := f.coordinator.Allocate(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	request.Claim.Status.InstallationRevision = "forged-public-value"
	installation := &fleetv1.Installation{ObjectMeta: metav1.ObjectMeta{Namespace: request.Claim.Namespace, Name: "install", UID: "installation-uid"}, Spec: fleetv1.InstallationSpec{ClaimRef: fleetv1.ClaimReference{Name: request.Claim.Name}, ExpectedPredecessor: fleetv1.InstallationPredecessor{Type: "None"}, ImageRef: fleetv1.LocalReference{Name: "registry/image@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}}
	f.executor.unknown = true
	result, err := f.coordinator.Install(ctx, InstallationRequest{Claim: request.Claim, Installation: installation, NamespaceUID: "namespace-uid", Admission: installAdmission()})
	if err != nil || result.Revision == "" {
		t.Fatalf("Install = %#v, %v", result, err)
	}
	if result.Record.InstallationNamespace != installation.Namespace || result.Record.InstallationName != installation.Name || result.Record.InstallationUID != installation.UID || result.Record.ClaimNamespace != request.Claim.Namespace || result.Record.ClaimName != request.Claim.Name || result.Record.ClaimUID != request.Claim.UID {
		t.Fatalf("operation omitted recovery routing identity: %#v", result.Record)
	}
	invalid := result.Record
	invalid.InstallationName = ""
	if !errors.Is(validateInstallationRecord(&invalid), ErrRecordInvalid) {
		t.Fatal("operation without retained installation name validated")
	}
	if len(f.executor.requests) != 1 || f.executor.requests[0].AssetID != asset.AssetID {
		t.Fatalf("requests = %#v", f.executor.requests)
	}
	f.coordinator.images = failingResolver{}
	again, err := f.coordinator.Install(ctx, InstallationRequest{Claim: request.Claim, Installation: installation, NamespaceUID: "namespace-uid", Admission: installAdmission()})
	if err != nil || again.Revision != result.Revision || len(f.executor.requests) != 1 {
		t.Fatalf("idempotent retry = %#v, %v", again, err)
	}
	changed := installAdmission()
	changed.DecisionID = "different-install-decision"
	if _, err := f.coordinator.Install(ctx, InstallationRequest{Claim: request.Claim, Installation: installation, NamespaceUID: "namespace-uid", Admission: changed}); !errors.Is(err, ErrPredecessorConflict) {
		t.Fatalf("changed admission error = %v", err)
	}
}

func TestInstallReplayRejectsRetainedRoutingIdentitySubstitution(t *testing.T) {
	ctx := context.Background()
	for name, mutate := range map[string]func(*InstallationRecord){
		"installation namespace": func(record *InstallationRecord) {
			record.InstallationNamespace, record.ClaimNamespace = "other", "other"
		},
		"installation name": func(record *InstallationRecord) { record.InstallationName = "other" },
		"claim namespace": func(record *InstallationRecord) {
			record.ClaimNamespace, record.InstallationNamespace = "other", "other"
		},
		"claim name": func(record *InstallationRecord) { record.ClaimName = "other" },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			request := f.request("claim")
			if err := f.coordinator.EnsureClaimCustody(ctx, request.Claim, request.NamespaceUID, request.Admission); err != nil {
				t.Fatal(err)
			}
			asset, err := f.coordinator.Allocate(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			installation := &fleetv1.Installation{ObjectMeta: metav1.ObjectMeta{Namespace: request.Claim.Namespace, Name: "install", UID: "install-uid"}, Spec: fleetv1.InstallationSpec{ClaimRef: fleetv1.ClaimReference{Name: request.Claim.Name}, ExpectedPredecessor: fleetv1.InstallationPredecessor{Type: "None"}, ImageRef: fleetv1.LocalReference{Name: "image@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}}
			installRequest := InstallationRequest{Claim: request.Claim, Installation: installation, NamespaceUID: request.NamespaceUID, Admission: installAdmission()}
			image, _ := f.coordinator.images.ResolveImage(ctx, ImageRequest{NamespaceUID: request.NamespaceUID, ClaimUID: request.Claim.UID, InstallationUID: installation.UID, ImageName: installation.Spec.ImageRef.Name})
			scope := LocalClaimScope(request.NamespaceUID)
			operationName := operationRecordName(scope, request.Claim.UID, "None")
			record, secret, err := f.coordinator.getOrCreateOperation(ctx, operationName, installRequest, asset, "None", image)
			if err != nil {
				t.Fatal(err)
			}
			mutate(record)
			if err := encodeRecord(secret, record, "installation"); err != nil {
				t.Fatal(err)
			}
			if err := f.coordinator.client.Update(ctx, secret); err != nil {
				t.Fatal(err)
			}
			if _, err := f.coordinator.Install(ctx, installRequest); !errors.Is(err, ErrPredecessorConflict) {
				t.Fatalf("error=%v", err)
			}
			if len(f.executor.requests) != 0 {
				t.Fatalf("substituted routing identity reached executor: %#v", f.executor.requests)
			}
		})
	}
}

func TestInstallUpgradesAndRecoversLegacyV1Operation(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	request := f.request("claim")
	if err := f.coordinator.EnsureClaimCustody(ctx, request.Claim, request.NamespaceUID, request.Admission); err != nil {
		t.Fatal(err)
	}
	asset, err := f.coordinator.Allocate(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	installation := &fleetv1.Installation{ObjectMeta: metav1.ObjectMeta{Namespace: request.Claim.Namespace, Name: "install", UID: "install-uid"}, Spec: fleetv1.InstallationSpec{ClaimRef: fleetv1.ClaimReference{Name: request.Claim.Name}, ExpectedPredecessor: fleetv1.InstallationPredecessor{Type: "None"}, ImageRef: fleetv1.LocalReference{Name: "image@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}}
	installRequest := InstallationRequest{Claim: request.Claim, Installation: installation, NamespaceUID: request.NamespaceUID, Admission: installAdmission()}
	image, _ := f.coordinator.images.ResolveImage(ctx, ImageRequest{NamespaceUID: request.NamespaceUID, ClaimUID: request.Claim.UID, InstallationUID: installation.UID, ImageName: installation.Spec.ImageRef.Name})
	scope := LocalClaimScope(request.NamespaceUID)
	legacy := &InstallationRecord{SchemaVersion: 1, State: OperationAdmitted, OperationID: "install-legacy", InstallationUID: installation.UID, NamespaceUID: request.NamespaceUID, ClaimUID: request.Claim.UID, ClaimScope: scope, AssetID: asset.AssetID, BindingID: asset.BindingID, Epoch: asset.Epoch, Predecessor: "None", ResultRevision: "ir-legacy", Image: image, ImageName: installation.Spec.ImageRef.Name, Admission: installAdmission()}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "private", Name: operationRecordName(scope, request.Claim.UID, "None")}}
	if err := encodeRecord(secret, legacy, "installation"); err != nil {
		t.Fatal(err)
	}
	if err := f.coordinator.client.Create(ctx, secret); err != nil {
		t.Fatal(err)
	}
	result, err := f.coordinator.Install(ctx, installRequest)
	if err != nil || result.Revision != "ir-legacy" || len(f.executor.requests) != 1 {
		t.Fatalf("result=%#v err=%v requests=%#v", result, err, f.executor.requests)
	}
	upgraded, _, err := f.coordinator.getOperation(ctx, secret.Name)
	if err != nil || upgraded.SchemaVersion != installationRecordSchemaVersion || upgraded.InstallationNamespace != installation.Namespace || upgraded.InstallationName != installation.Name || upgraded.ClaimNamespace != request.Claim.Namespace || upgraded.ClaimName != request.Claim.Name {
		t.Fatalf("upgraded=%#v err=%v", upgraded, err)
	}
}

func TestReleaseRecoversRetainedActiveInstallationBeforeCleanup(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	request := f.request("claim")
	if err := f.coordinator.EnsureClaimCustody(ctx, request.Claim, request.NamespaceUID, request.Admission); err != nil {
		t.Fatal(err)
	}
	asset, err := f.coordinator.Allocate(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	installation := &fleetv1.Installation{ObjectMeta: metav1.ObjectMeta{Namespace: request.Claim.Namespace, Name: "install", UID: "install-uid"}, Spec: fleetv1.InstallationSpec{ClaimRef: fleetv1.ClaimReference{Name: request.Claim.Name}, ExpectedPredecessor: fleetv1.InstallationPredecessor{Type: "None"}, ImageRef: fleetv1.LocalReference{Name: "image@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}}
	image, _ := f.coordinator.images.ResolveImage(ctx, ImageRequest{NamespaceUID: request.NamespaceUID, ClaimUID: request.Claim.UID, InstallationUID: installation.UID, ImageName: installation.Spec.ImageRef.Name})
	op, _, err := f.coordinator.getOrCreateOperation(ctx, operationRecordName(LocalClaimScope(request.NamespaceUID), request.Claim.UID, "None"), InstallationRequest{Claim: request.Claim, Installation: installation, NamespaceUID: request.NamespaceUID, Admission: installAdmission()}, asset, "None", image)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.coordinator.activateOperation(ctx, asset.AssetID, asset.ClaimUID, "None", op.OperationID); err != nil {
		t.Fatal(err)
	}
	result, err := f.coordinator.Release(ctx, request.Claim.UID)
	if err != nil || !result.SafeToRemoveFinalizer || len(f.executor.requests) != 2 || f.executor.requests[0].Kind != "install" || f.executor.requests[1].Kind != "cleanup" {
		t.Fatalf("Release = %#v, %v, requests=%#v", result, err, f.executor.requests)
	}
}

func TestInstallRejectsDeletingOrUnprotectedClaim(t *testing.T) {
	ctx := context.Background()
	for _, deleting := range []bool{false, true} {
		t.Run(fmt.Sprintf("deleting-%t", deleting), func(t *testing.T) {
			f := newFixture(t)
			request := f.request("claim")
			if err := f.coordinator.EnsureClaimCustody(ctx, request.Claim, request.NamespaceUID, request.Admission); err != nil {
				t.Fatal(err)
			}
			if _, err := f.coordinator.Allocate(ctx, request); err != nil {
				t.Fatal(err)
			}
			if deleting {
				now := metav1.NewTime(f.now)
				request.Claim.DeletionTimestamp = &now
			} else {
				request.Claim.Finalizers = nil
			}
			installation := &fleetv1.Installation{ObjectMeta: metav1.ObjectMeta{Namespace: request.Claim.Namespace, Name: "install", UID: "install-uid"}, Spec: fleetv1.InstallationSpec{ClaimRef: fleetv1.ClaimReference{Name: request.Claim.Name}, ExpectedPredecessor: fleetv1.InstallationPredecessor{Type: "None"}, ImageRef: fleetv1.LocalReference{Name: "image@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}}
			if _, err := f.coordinator.Install(ctx, InstallationRequest{Claim: request.Claim, Installation: installation, NamespaceUID: request.NamespaceUID, Admission: installAdmission()}); err == nil {
				t.Fatal("installation admission remained open")
			}
			if len(f.executor.requests) != 0 {
				t.Fatalf("executor requests = %#v", f.executor.requests)
			}
		})
	}
}

func TestCoordinatorRejectsMalformedGenericExecutorEvidence(t *testing.T) {
	ctx := context.Background()
	request := simulator.Request{ID: "cleanup-binding", Kind: "cleanup"}
	for _, recover := range []bool{false, true} {
		t.Run(fmt.Sprintf("recover-%t", recover), func(t *testing.T) {
			f := newFixture(t)
			f.executor.malform = true
			f.executor.unknown = recover
			if _, err := f.coordinator.executeAndRecover(ctx, request); !errors.Is(err, executor.ErrOutcomeUnknown) {
				t.Fatalf("error = %v, want unknown outcome", err)
			}
		})
	}
}

func TestFailClosedInputsAndDiskCount(t *testing.T) {
	ctx := context.Background()
	for name, test := range map[string]struct {
		mutate func(*AllocationRequest)
		want   error
	}{
		"source": {mutate: func(r *AllocationRequest) {
			r.Class.Spec.Source = &fleetv1.SourceReference{Project: "other", Name: "class"}
		}, want: ErrUnsupported},
		"network": {mutate: func(r *AllocationRequest) {
			r.Claim.Spec.NetworkInterfaces = []fleetv1.NetworkInterfaceRequest{{Name: "management"}}
		}, want: fleetnetwork.ErrAuthorizationUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			r := f.request("claim")
			if err := f.coordinator.EnsureClaimCustody(ctx, r.Claim, r.NamespaceUID, r.Admission); err != nil {
				t.Fatal(err)
			}
			test.mutate(&r)
			if _, err := f.coordinator.Allocate(ctx, r); !errors.Is(err, test.want) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	profile := fleetv1.MachineProfileSpec{CPU: fleetv1.CPURequirement{Architecture: "amd64"}, Disks: []fleetv1.DiskRequirement{{Count: fleetv1.Constraint{Exact: int64ptr(1)}, Media: "ssd"}}}
	hardware := fleetv1.Hardware{CPU: fleetv1.HardwareCPU{Architecture: "amd64"}, Disks: []fleetv1.HardwareDisk{{Media: "ssd"}, {Media: "ssd"}}}
	if matches(profile, hardware) {
		t.Fatal("exactly one matching disk accepted two")
	}

	f := newFixture(t)
	r := f.request("install-claim")
	if err := f.coordinator.EnsureClaimCustody(ctx, r.Claim, r.NamespaceUID, r.Admission); err != nil {
		t.Fatal(err)
	}
	if _, err := f.coordinator.Allocate(ctx, r); err != nil {
		t.Fatal(err)
	}
	installation := &fleetv1.Installation{ObjectMeta: metav1.ObjectMeta{Namespace: r.Claim.Namespace, Name: "install", UID: "install"}, Spec: fleetv1.InstallationSpec{ClaimRef: fleetv1.ClaimReference{Name: r.Claim.Name}, ExpectedPredecessor: fleetv1.InstallationPredecessor{Type: "None"}, ImageRef: fleetv1.LocalReference{Name: "image@sha256:ABC"}}}
	if _, err := f.coordinator.Install(ctx, InstallationRequest{Claim: r.Claim, Installation: installation, NamespaceUID: "namespace", Admission: installAdmission()}); err == nil {
		t.Fatal("non-canonical digest accepted")
	}
	installation.Spec.BootstrapConfigRef = &fleetv1.LocalReference{Name: "secret"}
	if _, err := f.coordinator.Install(ctx, InstallationRequest{Claim: r.Claim, Installation: installation, NamespaceUID: "namespace", Admission: installAdmission()}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("bootstrap error = %v", err)
	}
}

func TestNetworkLifecycleRetainsInputsAndGatesProviderCleanup(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	networks := &fakeNetworkLifecycle{}
	f.coordinator.networks = networks
	request := f.request("networked")
	request.Profile.Spec.Interfaces = []fleetv1.InterfaceRequirement{{Name: "management"}}
	request.Machines[0].Status.Hardware.Interfaces = []fleetv1.HardwareInterface{{Name: "eno1"}}
	request.Claim.Spec.LocationRef = fleetv1.LocalReference{Name: "us-central-1"}
	request.Claim.Spec.NetworkInterfaces = []fleetv1.NetworkInterfaceRequest{{Name: "management", Network: fleetv1.NamespacedReference{Name: "management"}, IPFamilies: []string{"IPv4"}}}
	request.Location = fleetnetwork.LocationAuthority{Name: "us-central-1", UID: "location-uid", Revision: "location-r1"}
	if err := f.coordinator.EnsureClaimCustody(ctx, request.Claim, request.NamespaceUID, request.Admission); err != nil {
		t.Fatal(err)
	}
	asset, err := f.coordinator.Allocate(ctx, request)
	if err != nil || networks.preflights != 1 || asset.Network == nil || asset.Network.Plan.Interfaces[0].NetworkUID != "network-management" {
		t.Fatalf("Allocate = %#v, %v, preflights=%d", asset, err, networks.preflights)
	}
	networks.observation = fleetnetwork.Observation{Revision: "network-r1", ObservedAt: f.now, Interfaces: []fleetnetwork.InterfaceObservation{{Name: "management", ClaimUID: "network-claim-uid", ClaimGeneration: 4, NetworkInterfaceUID: "private-interface-uid", NetworkInterfaceName: "private-interface-name", MTU: 1440, Addresses: []fleetnetwork.Address{{Address: "10.20.30.42/24", Family: "IPv4", Primary: true}}, Conditions: []fleetnetwork.Condition{{Type: "Allocated", Status: metav1.ConditionTrue, Reason: "internal-detail", ObservedGeneration: 4}, {Type: "Prepared", Status: metav1.ConditionTrue, Reason: "internal-detail", ObservedGeneration: 4}, {Type: "Programmed", Status: metav1.ConditionTrue, Reason: "internal-detail", ObservedGeneration: 4}}}}}
	if observation, err := f.coordinator.ReconcileNetwork(ctx, request.Claim.UID); err != nil || !observation.Programmed(asset.Network.Plan) {
		t.Fatalf("ReconcileNetwork = %#v, %v", observation, err)
	}
	retained, _, err := f.coordinator.getAsset(ctx, asset.AssetID)
	if err != nil || retained.Network.Observation.Interfaces[0].ClaimUID != "network-claim-uid" {
		t.Fatalf("retained network = %#v, %v", retained.Network, err)
	}
	status, ready := ProjectNetworkStatus(retained.Network, f.now, time.Minute)
	if ready.Status != metav1.ConditionTrue || status.Interfaces[0].Conditions[0].Reason != "Verified" || status.Interfaces[0].Addresses[0].Address != "10.20.30.42/24" {
		t.Fatalf("projection = %#v, condition %#v", status, ready)
	}
	installation := &fleetv1.Installation{ObjectMeta: metav1.ObjectMeta{Namespace: request.Claim.Namespace, Name: "install", UID: "installation-uid"}, Spec: fleetv1.InstallationSpec{ClaimRef: fleetv1.ClaimReference{Name: request.Claim.Name}, ExpectedPredecessor: fleetv1.InstallationPredecessor{Type: "None"}, ImageRef: fleetv1.LocalReference{Name: "registry/image@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}}
	installed, err := f.coordinator.Install(ctx, InstallationRequest{Claim: request.Claim, Installation: installation, NamespaceUID: request.NamespaceUID, Admission: installAdmission()})
	if err != nil || installed.Record.NetworkInputs == nil || installed.Record.NetworkInputs.Revision != "network-r1" {
		t.Fatalf("Install = %#v, %v", installed, err)
	}
	if inputs := string(f.executor.requests[0].Inputs); strings.Contains(inputs, "network-claim-uid") || strings.Contains(inputs, "private-interface-uid") || !strings.Contains(inputs, "network-r1") {
		t.Fatalf("provider network inputs were not sanitized: %s", inputs)
	}
	current, secret, err := f.coordinator.getAsset(ctx, asset.AssetID)
	if err != nil {
		t.Fatal(err)
	}
	current.Network.Observation.ObservedAt = f.now.Add(-2 * time.Minute)
	if err := encodeRecord(secret, current, "asset"); err != nil {
		t.Fatal(err)
	}
	if err := f.coordinator.client.Update(ctx, secret); err != nil {
		t.Fatal(err)
	}
	if _, ready := ProjectNetworkStatus(current.Network, f.now, time.Minute); ready.Status != metav1.ConditionUnknown {
		t.Fatalf("stale network evidence projected ready: %#v", ready)
	}
	f.coordinator.images = failingResolver{}
	if recovered, err := f.coordinator.Install(ctx, InstallationRequest{Claim: request.Claim, Installation: installation, NamespaceUID: request.NamespaceUID, Admission: installAdmission()}); err != nil || recovered.Revision != installed.Revision || len(f.executor.requests) != 1 {
		t.Fatalf("retained install recovery = %#v, %v, provider=%#v", recovered, err, f.executor.requests)
	}
	f.executor.requests = nil
	first, err := f.coordinator.Release(ctx, request.Claim.UID)
	if !errors.Is(err, fleetnetwork.ErrPending) || first.State != AssetReleasing || len(f.executor.requests) != 0 {
		t.Fatalf("first Release = %#v, %v, provider=%#v", first, err, f.executor.requests)
	}
	second, err := f.coordinator.Release(ctx, request.Claim.UID)
	if err != nil || !second.SafeToRemoveFinalizer || len(f.executor.requests) != 1 || f.executor.requests[0].Kind != "cleanup" {
		t.Fatalf("second Release = %#v, %v, provider=%#v", second, err, f.executor.requests)
	}
}

func TestNetworkPreflightRunsBeforeHardwareSelection(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	networks := &fakeNetworkLifecycle{}
	f.coordinator.networks = networks
	request := f.request("networked")
	request.Profile.Spec.Interfaces = []fleetv1.InterfaceRequirement{{Name: "management"}}
	request.Claim.Spec.LocationRef = fleetv1.LocalReference{Name: "us-central-1"}
	request.Claim.Spec.NetworkInterfaces = []fleetv1.NetworkInterfaceRequest{{Name: "management", Network: fleetv1.NamespacedReference{Name: "management"}}}
	request.Location = fleetnetwork.LocationAuthority{Name: "us-central-1", UID: "location-uid", Revision: "location-r1"}
	request.Machines = nil
	if err := f.coordinator.EnsureClaimCustody(ctx, request.Claim, request.NamespaceUID, request.Admission); err != nil {
		t.Fatal(err)
	}
	if _, err := f.coordinator.Allocate(ctx, request); !errors.Is(err, ErrInsufficientCapacity) || networks.preflights != 1 {
		t.Fatalf("Allocate error = %v, preflights=%d", err, networks.preflights)
	}
}

func TestNetworkRequestShapeIsRejectedBeforePreflight(t *testing.T) {
	for name, mutate := range map[string]func(*fleetv1.NetworkInterfaceRequest){
		"duplicate families": func(request *fleetv1.NetworkInterfaceRequest) {
			request.IPFamilies = []string{"IPv4", "IPv4"}
		},
		"too many families": func(request *fleetv1.NetworkInterfaceRequest) {
			request.IPFamilies = []string{"IPv4", "IPv6", "IPv4"}
		},
		"unknown family": func(request *fleetv1.NetworkInterfaceRequest) {
			request.IPFamilies = []string{"IPX"}
		},
		"duplicate address classes": func(request *fleetv1.NetworkInterfaceRequest) {
			request.Addresses = []fleetv1.AddressRequest{{ClassRef: fleetv1.LocalReference{Name: "public"}}, {ClassRef: fleetv1.LocalReference{Name: "public"}}}
		},
		"too many address classes": func(request *fleetv1.NetworkInterfaceRequest) {
			for i := 0; i < 5; i++ {
				request.Addresses = append(request.Addresses, fleetv1.AddressRequest{ClassRef: fleetv1.LocalReference{Name: fmt.Sprintf("class-%d", i)}})
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			f := newFixture(t)
			networks := &fakeNetworkLifecycle{}
			f.coordinator.networks = networks
			request := f.request(types.UID(strings.ReplaceAll(name, " ", "-")))
			request.Profile.Spec.Interfaces = []fleetv1.InterfaceRequirement{{Name: "management"}}
			request.Claim.Spec.LocationRef = fleetv1.LocalReference{Name: "us-central-1"}
			request.Claim.Spec.NetworkInterfaces = []fleetv1.NetworkInterfaceRequest{{Name: "management", Network: fleetv1.NamespacedReference{Name: "management"}}}
			request.Location = fleetnetwork.LocationAuthority{Name: "us-central-1", UID: "location-uid", Revision: "location-r1"}
			mutate(&request.Claim.Spec.NetworkInterfaces[0])
			if err := f.coordinator.EnsureClaimCustody(ctx, request.Claim, request.NamespaceUID, request.Admission); err != nil {
				t.Fatal(err)
			}
			if _, err := f.coordinator.Allocate(ctx, request); !errors.Is(err, ErrRecordInvalid) {
				t.Fatalf("Allocate error = %v, want ErrRecordInvalid", err)
			}
			if networks.preflights != 0 {
				t.Fatalf("invalid request reached network preflight %d times", networks.preflights)
			}
		})
	}
}

func TestNetworkReleaseBeforeEnsurePersistsDeleteIntent(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	networks := &fakeNetworkLifecycle{}
	f.coordinator.networks = networks
	request := f.request("network-delete-before-ensure")
	request.Profile.Spec.Interfaces = []fleetv1.InterfaceRequirement{{Name: "management"}}
	request.Machines[0].Status.Hardware.Interfaces = []fleetv1.HardwareInterface{{Name: "eno1"}}
	request.Claim.Spec.LocationRef = fleetv1.LocalReference{Name: "us-central-1"}
	request.Claim.Spec.NetworkInterfaces = []fleetv1.NetworkInterfaceRequest{{Name: "management", Network: fleetv1.NamespacedReference{Name: "management"}}}
	request.Location = fleetnetwork.LocationAuthority{Name: "us-central-1", UID: "location-uid", Revision: "location-r1"}
	if err := f.coordinator.EnsureClaimCustody(ctx, request.Claim, request.NamespaceUID, request.Admission); err != nil {
		t.Fatal(err)
	}
	asset, err := f.coordinator.Allocate(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.coordinator.Release(ctx, request.Claim.UID); !errors.Is(err, fleetnetwork.ErrPending) {
		t.Fatalf("first Release error = %v", err)
	}
	retained, _, err := f.coordinator.getAsset(ctx, asset.AssetID)
	if err != nil || len(retained.Network.Lifecycle) != 1 || !retained.Network.Lifecycle[0].DeleteAdmitted {
		t.Fatalf("retained lifecycle = %#v, err=%v", retained.Network, err)
	}
	if result, err := f.coordinator.Release(ctx, request.Claim.UID); err != nil || !result.SafeToRemoveFinalizer {
		t.Fatalf("second Release = %#v, %v", result, err)
	}
}

func TestCoordinatorRejectsPartialNetworkLifecycleWithoutCorruptingRecord(t *testing.T) {
	for _, test := range []struct {
		name    string
		partial func(*fakeNetworkLifecycle)
		run     func(context.Context, *Coordinator, types.UID) error
		state   AssetState
	}{
		{
			name:    "ensure",
			partial: func(lifecycle *fakeNetworkLifecycle) { lifecycle.partialEnsure = true },
			run: func(ctx context.Context, coordinator *Coordinator, claimUID types.UID) error {
				_, err := coordinator.ReconcileNetwork(ctx, claimUID)
				return err
			},
			state: AssetCommitted,
		},
		{
			name:    "release",
			partial: func(lifecycle *fakeNetworkLifecycle) { lifecycle.partialRelease = true },
			run: func(ctx context.Context, coordinator *Coordinator, claimUID types.UID) error {
				_, err := coordinator.Release(ctx, claimUID)
				return err
			},
			state: AssetReleasing,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			f := newFixture(t)
			networks := &fakeNetworkLifecycle{}
			test.partial(networks)
			f.coordinator.networks = networks
			request := twoInterfaceNetworkRequest(f, types.UID("partial-"+test.name))
			if err := f.coordinator.EnsureClaimCustody(ctx, request.Claim, request.NamespaceUID, request.Admission); err != nil {
				t.Fatal(err)
			}
			asset, err := f.coordinator.Allocate(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			if err := test.run(ctx, f.coordinator, request.Claim.UID); !errors.Is(err, ErrRecordInvalid) {
				t.Fatalf("operation error = %v, want ErrRecordInvalid", err)
			}
			retained, _, err := f.coordinator.getAsset(ctx, asset.AssetID)
			if err != nil {
				t.Fatalf("retained asset became undecodable: %v", err)
			}
			if retained.State != test.state || len(retained.Network.Lifecycle) != 0 {
				t.Fatalf("retained asset = %#v, want state %s with no lifecycle", retained, test.state)
			}
		})
	}
}

func twoInterfaceNetworkRequest(f *fixture, claimUID types.UID) AllocationRequest {
	request := f.request(claimUID)
	request.Profile.Spec.Interfaces = []fleetv1.InterfaceRequirement{{Name: "management"}, {Name: "data"}}
	request.Machines[0].Status.Hardware.Interfaces = []fleetv1.HardwareInterface{{Name: "eno1"}, {Name: "eno2"}}
	request.Claim.Spec.LocationRef = fleetv1.LocalReference{Name: "us-central-1"}
	request.Claim.Spec.NetworkInterfaces = []fleetv1.NetworkInterfaceRequest{
		{Name: "management", Network: fleetv1.NamespacedReference{Name: "management"}},
		{Name: "data", Network: fleetv1.NamespacedReference{Name: "workload"}},
	}
	request.Location = fleetnetwork.LocationAuthority{Name: "us-central-1", UID: "location-uid", Revision: "location-r1"}
	return request
}

func int64ptr(value int64) *int64 { return &value }

func installAdmission() InstallationAdmissionAuthority {
	return InstallationAdmissionAuthority{PrincipalUID: "user-uid", DecisionID: "install-decision-1", CatalogAuthority: "local-test-catalog", CatalogRevision: "local-test-v1", ImageDecisionID: "local-test-sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
}
