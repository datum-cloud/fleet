package milo

import (
	"context"
	"encoding/json"
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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
	fleetnetwork "go.datum.net/fleet/internal/network"
	"go.datum.net/fleet/internal/simulator"
)

type testExecutor struct {
	mu       sync.Mutex
	requests []simulator.Request
}

func (e *testExecutor) Execute(_ context.Context, request simulator.Request) (simulator.OperationResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.requests = append(e.requests, request)
	now := time.Now()
	return simulator.OperationResult{ID: request.ID, Kind: request.Kind, Phase: "Succeeded", Result: map[string]string{"cleanup": "clean", "install": "installed"}[request.Kind], CompletedAt: &now}, nil
}
func (e *testExecutor) Lookup(context.Context, string) (simulator.OperationResult, bool, error) {
	return simulator.OperationResult{}, false, nil
}

type testProjects struct {
	identities map[string]controller.ProjectIdentity
	clients    map[string]client.Client
	invalid    map[string]bool
}

func (p *testProjects) Resolve(_ context.Context, route string) (controller.ProjectIdentity, client.Client, error) {
	if p.invalid[route] {
		return controller.ProjectIdentity{}, nil, ErrProjectLifetimeMismatch
	}
	value, ok := p.identities[route]
	if !ok {
		return controller.ProjectIdentity{}, nil, ErrProjectLifetimeMismatch
	}
	return value, p.clients[route], nil
}
func (p *testProjects) Verify(_ context.Context, project controller.ProjectIdentity) error {
	if p.invalid[project.RouteName] || p.identities[project.RouteName] != project {
		return ErrProjectLifetimeMismatch
	}
	return nil
}

type testSource struct{ deny error }

func (s *testSource) AuthorizeSource(_ context.Context, request SourceUseRequest) (SourceUseReceipt, error) {
	if s.deny != nil {
		return SourceUseReceipt{}, s.deny
	}
	d, _ := digest(request)
	return SourceUseReceipt{RequestDigest: d, PrincipalUID: "user-uid", DecisionID: "decision-1", PublicationRevision: request.PublicationRevision, FenceToken: "source-fence"}, nil
}

type testEntitlement struct{}

func (testEntitlement) Current(context.Context, client.Client) (EntitlementEvidence, error) {
	return EntitlementEvidence{Name: EntitlementName, UID: "entitlement-uid", Generation: 3, ObservedGeneration: 3, ServiceName: ServiceName}, nil
}

type testQuota struct {
	mu    sync.Mutex
	calls int
}

type pendingQuota struct{}

func (pendingQuota) Acquire(context.Context, client.Client, QuotaIntent) (QuotaHold, bool, error) {
	return QuotaHold{}, false, ErrQuotaUnknown
}

type pendingHoldQuota struct{}

func (pendingHoldQuota) Acquire(_ context.Context, _ client.Client, intent QuotaIntent) (QuotaHold, bool, error) {
	return QuotaHold{Intent: intent, UID: "pending-quota-uid", Generation: 1, ObservedGeneration: 1}, false, nil
}

func (q *testQuota) Acquire(_ context.Context, _ client.Client, intent QuotaIntent) (QuotaHold, bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.calls++
	return QuotaHold{Intent: intent, UID: "quota-uid", Generation: 2, ObservedGeneration: 2}, true, nil
}

type testFence struct {
	mu      sync.Mutex
	reject  bool
	stale   bool
	calls   int
	entered chan struct{}
	proceed chan struct{}
}

func (f *testFence) Commit(_ context.Context, commit AdmissionCommit) (AdmissionCommitReceipt, error) {
	f.mu.Lock()
	f.calls++
	reject, stale := f.reject, f.stale
	entered, proceed := f.entered, f.proceed
	f.mu.Unlock()
	if entered != nil {
		select {
		case entered <- struct{}{}:
		default:
		}
	}
	if proceed != nil {
		<-proceed
	}
	if reject {
		return AdmissionCommitReceipt{}, errors.New("revoked")
	}
	commitDigest, _ := digest(commit)
	if stale {
		commitDigest = "stale-hold-and-entitlement"
	}
	return AdmissionCommitReceipt{ContractDigest: commit.Authority.ContractDigest, CommitDigest: commitDigest, FenceToken: commit.Source.FenceToken, CommitID: "commit-1"}, nil
}

type testCustodian struct {
	mu             sync.Mutex
	fail           bool
	noHold         bool
	calls          int
	project        controller.ProjectIdentity
	quota          QuotaManager
	closed         map[string]bool
	acquireEntered chan struct{}
	acquireProceed chan struct{}
}

func (c *testCustodian) Acquire(ctx context.Context, project controller.ProjectIdentity, authority controller.AuthorityRef, intent QuotaIntent) (QuotaHold, bool, error) {
	if c.acquireEntered != nil {
		select {
		case c.acquireEntered <- struct{}{}:
		default:
		}
	}
	if c.acquireProceed != nil {
		<-c.acquireProceed
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.project = project
	if c.closed[authority.RecordID] {
		return QuotaHold{}, false, ErrQuotaUnknown
	}
	return c.quota.Acquire(ctx, nil, intent)
}

func (c *testCustodian) Recover(_ context.Context, project controller.ProjectIdentity, authority controller.AuthorityRef, intent QuotaIntent) (IntentResolution, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.project = project
	if c.fail {
		return IntentResolution{}, errors.New("recovery unknown")
	}
	if c.noHold {
		intentDigest, _ := digest(intent)
		if c.closed == nil {
			c.closed = map[string]bool{}
		}
		c.closed[authority.RecordID] = true
		return IntentResolution{NoHold: &NoHoldReceipt{AuthorityRecordID: authority.RecordID, IntentDigest: intentDigest, AcknowledgmentID: "no-hold-1"}}, nil
	}
	hold := QuotaHold{Intent: intent, UID: "quota-uid", Generation: 2, ObservedGeneration: 2}
	return IntentResolution{Hold: &hold}, nil
}

func (c *testCustodian) Settle(_ context.Context, project controller.ProjectIdentity, authority controller.AuthorityRef, hold QuotaHold) (SettlementReceipt, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	c.project = project
	if c.fail {
		return SettlementReceipt{}, errors.New("settlement unknown")
	}
	holdDigest, _ := digest(hold)
	return SettlementReceipt{AuthorityRecordID: authority.RecordID, Hold: hold.Ref(), HoldDigest: holdDigest, AcknowledgmentID: "settled-1"}, nil
}

type adapterFixture struct {
	adapter    *Adapter
	prepare    PrepareRequest
	allocation controller.AllocationRequest
	executor   *testExecutor
	quota      *testQuota
	fence      *testFence
	custodian  *testCustodian
	records    *RecordStore
}

func newAdapterFixture(t *testing.T) *adapterFixture {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	privateClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	consumerClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	providerClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	executor := &testExecutor{}
	coordinator, err := controller.NewCoordinator(privateClient, "fleet-private", executor, controller.LocalDigestImageResolver{}, nil, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	records, err := NewRecordStore(privateClient, "fleet-private")
	if err != nil {
		t.Fatal(err)
	}
	consumer := controller.ProjectIdentity{AuthorityID: "milo-test", RouteName: "consumer", UID: "consumer-project-uid"}
	provider := controller.ProjectIdentity{AuthorityID: "milo-test", RouteName: "provider", UID: "provider-project-uid"}
	projects := &testProjects{identities: map[string]controller.ProjectIdentity{"consumer": consumer, "provider": provider}, clients: map[string]client.Client{"consumer": consumerClient, "provider": providerClient}, invalid: map[string]bool{}}
	quota, fence := &testQuota{}, &testFence{}
	custodian := &testCustodian{quota: quota, closed: map[string]bool{}}
	adapter := &Adapter{Records: records, Projects: projects, Sources: &testSource{}, Entitlements: testEntitlement{}, Fence: fence, Custodian: custodian, Lifecycle: coordinator}
	now := time.Now()
	location := fleetnetwork.LocationAuthority{Name: "us-central-1", UID: "location-uid", Revision: "location-r1"}
	claim := &fleetv1.MachineClaim{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "allocation", UID: "claim-uid", Finalizers: []string{controller.MachineClaimFinalizer}}, Spec: fleetv1.MachineClaimSpec{MachineClassRef: fleetv1.LocalReference{Name: "published-class"}, MachineProfileRef: fleetv1.LocalReference{Name: "published-profile"}, LocationRef: fleetv1.LocalReference{Name: location.Name}}}
	localClass := &fleetv1.MachineClass{ObjectMeta: metav1.ObjectMeta{Name: "published-class", UID: "local-class-uid"}, Spec: fleetv1.MachineClassSpec{Source: &fleetv1.SourceReference{Project: "provider", Name: "provider-class"}}}
	sourceClass := &fleetv1.MachineClass{ObjectMeta: metav1.ObjectMeta{Name: "provider-class", UID: "source-class-uid"}, Spec: fleetv1.MachineClassSpec{Capacity: &fleetv1.CapacityPolicy{Machines: fleetv1.MachineSelector{Namespace: "inventory"}}}, Status: fleetv1.MachineClassStatus{EvaluatedRevision: "class-r1"}}
	profileSpec := fleetv1.MachineProfileSpec{CPU: fleetv1.CPURequirement{Architecture: "amd64"}, Memory: fleetv1.MemoryRequirement{Capacity: fleetv1.QuantityConstraint{Min: "1Gi"}}}
	localProfile := &fleetv1.MachineProfile{ObjectMeta: metav1.ObjectMeta{Name: "published-profile", UID: "local-profile-uid"}, Spec: profileSpec}
	sourceProfile := &fleetv1.MachineProfile{ObjectMeta: metav1.ObjectMeta{Name: "source-profile", UID: "source-profile-uid"}, Spec: profileSpec}
	machine := fleetv1.Machine{ObjectMeta: metav1.ObjectMeta{Namespace: "inventory", Name: "machine-1", UID: "machine-uid", Generation: 1}, Spec: fleetv1.MachineSpec{AllocationPolicy: "Enabled", ProviderRef: fleetv1.TypedLocalReference{APIGroup: "example.io", Kind: "Server", Name: "server-1"}}, Status: fleetv1.MachineStatus{Hardware: fleetv1.Hardware{CPU: fleetv1.HardwareCPU{Architecture: "amd64"}, Memory: fleetv1.HardwareMemory{Capacity: "8Gi"}}, Conditions: []metav1.Condition{{Type: "Prepared", Status: metav1.ConditionTrue, ObservedGeneration: 1}, {Type: "Healthy", Status: metav1.ConditionTrue, ObservedGeneration: 1}, {Type: "Available", Status: metav1.ConditionTrue, ObservedGeneration: 1}}}}
	return &adapterFixture{adapter: adapter, prepare: PrepareRequest{ConsumerRoute: "consumer", ProviderRoute: "provider", NamespaceUID: "namespace-uid", Claim: claim, LocalClass: localClass, SourceClass: sourceClass, LocalProfile: localProfile, SourceProfile: sourceProfile, PublicationRevision: "publication-r1", Location: location}, allocation: controller.AllocationRequest{Claim: claim, Class: sourceClass, Profile: sourceProfile, Location: location, Machines: []fleetv1.Machine{machine}, EvidenceObservedAt: map[types.UID]time.Time{machine.UID: now}}, executor: executor, quota: quota, fence: fence, custodian: custodian, records: records}
}

func TestAdapterAllocationAndReleaseRequiresExactSettlement(t *testing.T) {
	ctx := context.Background()
	f := newAdapterFixture(t)
	prepared, err := f.adapter.PrepareClaim(ctx, f.prepare)
	if err != nil {
		t.Fatal(err)
	}
	asset, err := f.adapter.Allocate(ctx, prepared, f.allocation)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(asset.AssetID, "milo-test/provider-project-uid/") {
		t.Fatalf("asset ID = %q", asset.AssetID)
	}
	f.custodian.fail = true
	result, err := f.adapter.Release(ctx, prepared, f.prepare.Claim.UID)
	if !errors.Is(err, ErrQuotaReleaseUnknown) || result.State != controller.AssetCleanupVerified || result.SafeToRemoveFinalizer {
		t.Fatalf("unknown release = %#v, %v", result, err)
	}
	f.custodian.fail = false
	result, err = f.adapter.Release(ctx, prepared, f.prepare.Claim.UID)
	if err != nil || !result.SafeToRemoveFinalizer || result.State != controller.AssetReleased {
		t.Fatalf("release = %#v, %v", result, err)
	}
	record, err := f.records.Get(ctx, prepared.Authority)
	if err != nil || record.State != StateReleased || record.Settlement == nil {
		t.Fatalf("record = %#v, %v", record, err)
	}
}

func TestAdapterRecoversPreparedAuthorityWithoutReauthorizing(t *testing.T) {
	ctx := context.Background()
	f := newAdapterFixture(t)
	prepared, err := f.adapter.PrepareClaim(ctx, f.prepare)
	if err != nil {
		t.Fatal(err)
	}
	f.adapter.Sources = &testSource{deny: errors.New("caller is no longer available")}
	recovered, err := f.adapter.PrepareOrRecoverClaim(ctx, f.prepare)
	if err != nil {
		t.Fatal(err)
	}
	if recovered != prepared {
		t.Fatalf("recovered = %#v, want %#v", recovered, prepared)
	}

	changed := f.prepare
	changed.LocalProfile = changed.LocalProfile.DeepCopyObject().(*fleetv1.MachineProfile)
	changed.LocalProfile.Spec.Memory.Capacity.Min = "2Gi"
	if _, err := f.adapter.PrepareOrRecoverClaim(ctx, changed); !errors.Is(err, ErrAuthorityInvalid) {
		t.Fatalf("changed retained inputs error = %v, want ErrAuthorityInvalid", err)
	}
}

func TestAdapterRepairsPartialPreparation(t *testing.T) {
	for _, stage := range []string{"authority", "custody"} {
		t.Run(stage, func(t *testing.T) {
			ctx := context.Background()
			f := newAdapterFixture(t)
			lost := errors.New("simulated manager crash")
			if stage == "authority" {
				f.adapter.afterAuthorityCreate = func() error { return lost }
			} else {
				f.adapter.afterCustodyEnsure = func() error { return lost }
			}
			if _, err := f.adapter.PrepareClaim(ctx, f.prepare); !errors.Is(err, lost) {
				t.Fatalf("partial prepare error = %v, want simulated crash", err)
			}
			f.adapter.afterAuthorityCreate = nil
			f.adapter.afterCustodyEnsure = nil
			prepared, err := f.adapter.PrepareOrRecoverClaim(ctx, f.prepare)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.adapter.Allocate(ctx, prepared, f.allocation); err != nil {
				t.Fatalf("allocation after repaired preparation: %v", err)
			}
		})
	}
}

func TestAdapterReleaseClaimNeedsNoPublicInputs(t *testing.T) {
	ctx := context.Background()
	f := newAdapterFixture(t)
	prepared, err := f.adapter.PrepareClaim(ctx, f.prepare)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.adapter.Allocate(ctx, prepared, f.allocation); err != nil {
		t.Fatal(err)
	}
	result, err := f.adapter.ReleaseClaim(ctx, prepared.ConsumerProject, f.prepare.Claim.UID)
	if err != nil || !result.SafeToRemoveFinalizer || result.State != controller.AssetReleased {
		t.Fatalf("release by retained claim lifetime = %#v, %v", result, err)
	}
}

func TestAdapterMissingAuthorityDoesNotBypassRetainedCustody(t *testing.T) {
	ctx := context.Background()
	f := newAdapterFixture(t)
	prepared, err := f.adapter.PrepareClaim(ctx, f.prepare)
	if err != nil {
		t.Fatal(err)
	}
	var authority corev1.Secret
	if err := f.records.client.Get(ctx, client.ObjectKey{Namespace: "fleet-private", Name: prepared.Authority.RecordName}, &authority); err != nil {
		t.Fatal(err)
	}
	if err := f.records.client.Delete(ctx, &authority); err != nil {
		t.Fatal(err)
	}
	if _, err := f.adapter.ReleaseClaim(ctx, prepared.ConsumerProject, f.prepare.Claim.UID); !errors.Is(err, controller.ErrRecordInvalid) {
		t.Fatalf("missing authority with retained custody error = %v, want ErrRecordInvalid", err)
	}
	if _, err := f.adapter.PrepareOrRecoverClaim(ctx, f.prepare); !errors.Is(err, controller.ErrRecordInvalid) {
		t.Fatalf("reprepare with missing authority and retained custody error = %v, want ErrRecordInvalid", err)
	}
}

func TestAdapterProvesNeverAdmittedClaimHasNoAuthority(t *testing.T) {
	f := newAdapterFixture(t)
	consumer, _, err := f.adapter.Projects.Resolve(context.Background(), "consumer")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.adapter.ReleaseClaim(context.Background(), consumer, "never-admitted"); !errors.Is(err, ErrClaimAuthorityNotFound) {
		t.Fatalf("never-admitted release error = %v, want ErrClaimAuthorityNotFound", err)
	}
}

func TestAdapterFailsClosedWithoutProductionAuthorityProtocols(t *testing.T) {
	ctx := context.Background()
	f := newAdapterFixture(t)
	f.adapter.Sources = nil
	if _, err := f.adapter.PrepareClaim(ctx, f.prepare); !errors.Is(err, ErrAuthorityUnavailable) {
		t.Fatalf("source error = %v", err)
	}
	f = newAdapterFixture(t)
	prepared, err := f.adapter.PrepareClaim(ctx, f.prepare)
	if err != nil {
		t.Fatal(err)
	}
	f.adapter.Fence = nil
	if _, err := f.adapter.Allocate(ctx, prepared, f.allocation); !errors.Is(err, ErrAdmissionFenceUnavailable) {
		t.Fatalf("fence error = %v", err)
	}
	if f.quota.calls != 0 {
		t.Fatalf("quota calls = %d", f.quota.calls)
	}
	f.adapter.Fence = &testFence{}
	f.adapter.Custodian = nil
	if _, err := f.adapter.Allocate(ctx, prepared, f.allocation); !errors.Is(err, ErrQuotaCustodyUnavailable) {
		t.Fatalf("custody error = %v", err)
	}
}

func TestAllocationRejectsAuthoritySubstitutionBeforeEffects(t *testing.T) {
	mutations := map[string]func(*controller.AllocationRequest){
		"claim lifetime": func(r *controller.AllocationRequest) {
			r.Claim = r.Claim.DeepCopyObject().(*fleetv1.MachineClaim)
			r.Claim.UID = "replacement"
		},
		"source class": func(r *controller.AllocationRequest) {
			r.Class = r.Class.DeepCopyObject().(*fleetv1.MachineClass)
			r.Class.UID = "replacement"
		},
		"class revision": func(r *controller.AllocationRequest) {
			r.Class = r.Class.DeepCopyObject().(*fleetv1.MachineClass)
			r.Class.Status.EvaluatedRevision = "replacement"
		},
		"class content": func(r *controller.AllocationRequest) {
			r.Class = r.Class.DeepCopyObject().(*fleetv1.MachineClass)
			r.Class.Spec.Capacity.Machines.Namespace = "replacement"
		},
		"source profile": func(r *controller.AllocationRequest) {
			r.Profile = r.Profile.DeepCopyObject().(*fleetv1.MachineProfile)
			r.Profile.UID = "replacement"
		},
		"requirements": func(r *controller.AllocationRequest) {
			r.Profile = r.Profile.DeepCopyObject().(*fleetv1.MachineProfile)
			r.Profile.Spec.CPU.Architecture = "arm64"
		},
		"location": func(r *controller.AllocationRequest) {
			r.Location.UID = "replacement"
		},
		"claim location": func(r *controller.AllocationRequest) {
			r.Claim = r.Claim.DeepCopyObject().(*fleetv1.MachineClaim)
			r.Claim.Spec.LocationRef.Name = "replacement"
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			f := newAdapterFixture(t)
			prepared, err := f.adapter.PrepareClaim(ctx, f.prepare)
			if err != nil {
				t.Fatal(err)
			}
			mutate(&f.allocation)
			if _, err := f.adapter.Allocate(ctx, prepared, f.allocation); !errors.Is(err, ErrAuthorityInvalid) {
				t.Fatalf("error = %v", err)
			}
			if f.quota.calls != 0 || f.fence.calls != 0 || len(f.executor.requests) != 0 {
				t.Fatalf("effects: quota=%d fence=%d provider=%d", f.quota.calls, f.fence.calls, len(f.executor.requests))
			}
		})
	}
}

func TestAllocationRejectsDeletingOrUnprotectedClaimBeforeAdmissionEffects(t *testing.T) {
	for _, mutate := range []func(*fleetv1.MachineClaim){
		func(claim *fleetv1.MachineClaim) { claim.Finalizers = nil },
		func(claim *fleetv1.MachineClaim) {
			now := metav1.Now()
			claim.DeletionTimestamp = &now
		},
	} {
		ctx := context.Background()
		f := newAdapterFixture(t)
		prepared, err := f.adapter.PrepareClaim(ctx, f.prepare)
		if err != nil {
			t.Fatal(err)
		}
		f.allocation.Claim = f.allocation.Claim.DeepCopyObject().(*fleetv1.MachineClaim)
		mutate(f.allocation.Claim)
		if _, err := f.adapter.Allocate(ctx, prepared, f.allocation); !errors.Is(err, ErrAuthorityInvalid) {
			t.Fatalf("allocation error = %v", err)
		}
		if f.quota.calls != 0 || f.fence.calls != 0 || len(f.executor.requests) != 0 {
			t.Fatalf("effects: quota=%d fence=%d provider=%d", f.quota.calls, f.fence.calls, len(f.executor.requests))
		}
	}
}

func TestFenceInvalidationWinsAndSettlesBeforeRetry(t *testing.T) {
	ctx := context.Background()
	f := newAdapterFixture(t)
	prepared, err := f.adapter.PrepareClaim(ctx, f.prepare)
	if err != nil {
		t.Fatal(err)
	}
	f.fence.reject = true
	if _, err := f.adapter.Allocate(ctx, prepared, f.allocation); err == nil || f.custodian.calls != 1 {
		t.Fatalf("allocation error = %v, settlements=%d", err, f.custodian.calls)
	}
	if len(f.executor.requests) != 0 {
		t.Fatalf("provider effects = %#v", f.executor.requests)
	}
	record, err := f.records.Get(ctx, prepared.Authority)
	if err != nil || record.State != StatePrepared || record.QuotaHold != nil {
		t.Fatalf("record = %#v, %v", record, err)
	}
	f.fence.reject = false
	if _, err := f.adapter.Allocate(ctx, prepared, f.allocation); err != nil {
		t.Fatal(err)
	}
}

func TestFenceReceiptCannotReplayAcrossHoldOrEntitlement(t *testing.T) {
	ctx := context.Background()
	f := newAdapterFixture(t)
	prepared, err := f.adapter.PrepareClaim(ctx, f.prepare)
	if err != nil {
		t.Fatal(err)
	}
	f.fence.stale = true
	if _, err := f.adapter.Allocate(ctx, prepared, f.allocation); !errors.Is(err, ErrAuthorityInvalid) {
		t.Fatalf("error = %v", err)
	}
	if f.custodian.calls != 1 || len(f.executor.requests) != 0 {
		t.Fatalf("settlements=%d effects=%d", f.custodian.calls, len(f.executor.requests))
	}
}

func TestFenceCommitWinsBeforeBinding(t *testing.T) {
	ctx := context.Background()
	f := newAdapterFixture(t)
	prepared, err := f.adapter.PrepareClaim(ctx, f.prepare)
	if err != nil {
		t.Fatal(err)
	}
	f.fence.entered, f.fence.proceed = make(chan struct{}, 1), make(chan struct{})
	done := make(chan error, 1)
	go func() { _, err := f.adapter.Allocate(ctx, prepared, f.allocation); done <- err }()
	<-f.fence.entered
	close(f.fence.proceed)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	record, err := f.records.Get(ctx, prepared.Authority)
	if err != nil || record.State != StateBound || record.FenceReceipt == nil {
		t.Fatalf("record = %#v, %v", record, err)
	}
}

func TestDeleteAfterLostQuotaCreateResponseBlocksUntilCustodianRecovers(t *testing.T) {
	ctx := context.Background()
	f := newAdapterFixture(t)
	prepared, err := f.adapter.PrepareClaim(ctx, f.prepare)
	if err != nil {
		t.Fatal(err)
	}
	f.custodian.quota = pendingQuota{}
	if _, err := f.adapter.Allocate(ctx, prepared, f.allocation); !errors.Is(err, ErrQuotaUnknown) {
		t.Fatalf("allocation error = %v", err)
	}
	f.custodian.fail = true
	result, err := f.adapter.Release(ctx, prepared, f.prepare.Claim.UID)
	if !errors.Is(err, ErrQuotaUnknown) || result.SafeToRemoveFinalizer || !result.ExternalSettlementRequired {
		t.Fatalf("unknown intent release = %#v, %v", result, err)
	}
	if len(f.executor.requests) != 0 {
		t.Fatalf("provider effects = %#v", f.executor.requests)
	}
	f.custodian.fail = false
	result, err = f.adapter.Release(ctx, prepared, f.prepare.Claim.UID)
	if err != nil || !result.SafeToRemoveFinalizer {
		t.Fatalf("recovered release = %#v, %v", result, err)
	}
}

func TestDeleteAfterQuotaIntentAcceptsOnlyExactNoHoldProof(t *testing.T) {
	ctx := context.Background()
	f := newAdapterFixture(t)
	prepared, err := f.adapter.PrepareClaim(ctx, f.prepare)
	if err != nil {
		t.Fatal(err)
	}
	f.custodian.quota = pendingQuota{}
	if _, err := f.adapter.Allocate(ctx, prepared, f.allocation); !errors.Is(err, ErrQuotaUnknown) {
		t.Fatalf("allocation error = %v", err)
	}
	f.custodian.noHold = true
	result, err := f.adapter.Release(ctx, prepared, f.prepare.Claim.UID)
	if err != nil || !result.SafeToRemoveFinalizer {
		t.Fatalf("release = %#v, %v", result, err)
	}
	record, err := f.records.Get(ctx, prepared.Authority)
	if err != nil || record.NoHold == nil || record.NoHold.AcknowledgmentID == "" {
		t.Fatalf("record = %#v, %v", record, err)
	}
}

func TestDelayedAllocationCannotResurrectReleasedAuthority(t *testing.T) {
	ctx := context.Background()
	f := newAdapterFixture(t)
	prepared, err := f.adapter.PrepareClaim(ctx, f.prepare)
	if err != nil {
		t.Fatal(err)
	}
	entered, proceed := make(chan struct{}), make(chan struct{})
	f.adapter.afterLifecycleAllocate = func() { close(entered); <-proceed }
	allocateDone := make(chan error, 1)
	go func() { _, err := f.adapter.Allocate(ctx, prepared, f.allocation); allocateDone <- err }()
	<-entered
	result, err := f.adapter.Release(ctx, prepared, f.prepare.Claim.UID)
	if err != nil || !result.SafeToRemoveFinalizer {
		t.Fatalf("release = %#v, %v", result, err)
	}
	close(proceed)
	if err := <-allocateDone; !errors.Is(err, ErrAuthorityInvalid) {
		t.Fatalf("delayed allocation error = %v", err)
	}
	record, err := f.records.Get(ctx, prepared.Authority)
	if err != nil || record.State != StateReleased {
		t.Fatalf("record = %#v, %v", record, err)
	}
}

func TestRetryRecoversCoreBindingBeforeCurrentCapacityCheck(t *testing.T) {
	ctx := context.Background()
	f := newAdapterFixture(t)
	prepared, err := f.adapter.PrepareClaim(ctx, f.prepare)
	if err != nil {
		t.Fatal(err)
	}
	asset, err := f.adapter.Allocate(ctx, prepared, f.allocation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.records.Update(ctx, prepared.Authority, func(current *AuthorityRecord) error {
		current.State, current.AssetID, current.BindingID = StateQuotaGranted, "", ""
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	f.allocation.Machines = nil
	recovered, err := f.adapter.Allocate(ctx, prepared, f.allocation)
	if err != nil || recovered.BindingID != asset.BindingID {
		t.Fatalf("recovered = %#v, %v", recovered, err)
	}
}

func TestReleaseRecoversPersistedSettlementReceiptWithoutSecondExternalEffect(t *testing.T) {
	ctx := context.Background()
	f := newAdapterFixture(t)
	prepared, err := f.adapter.PrepareClaim(ctx, f.prepare)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.adapter.Allocate(ctx, prepared, f.allocation); err != nil {
		t.Fatal(err)
	}
	record, err := f.records.Get(ctx, prepared.Authority)
	if err != nil {
		t.Fatal(err)
	}
	result, err := f.adapter.Lifecycle.ReleaseScoped(ctx, prepared.ConsumerProject, f.prepare.Claim.UID)
	if err != nil || result.State != controller.AssetCleanupVerified {
		t.Fatalf("cleanup = %#v, %v", result, err)
	}
	holdDigest, _ := digest(*record.QuotaHold)
	receipt := SettlementReceipt{AuthorityRecordID: prepared.Authority.RecordID, Hold: record.QuotaHold.Ref(), HoldDigest: holdDigest, AcknowledgmentID: "persisted-ack"}
	if _, err := f.records.Update(ctx, prepared.Authority, func(current *AuthorityRecord) error {
		current.State, current.Settlement = StateQuotaReleasePending, &receipt
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before := f.custodian.calls
	result, err = f.adapter.Release(ctx, prepared, f.prepare.Claim.UID)
	if err != nil || !result.SafeToRemoveFinalizer || f.custodian.calls != before {
		t.Fatalf("recovery = %#v, %v, external calls %d->%d", result, err, before, f.custodian.calls)
	}
}

func TestReleaseInstallsHoldPersistedBeforeCoreCustody(t *testing.T) {
	ctx := context.Background()
	f := newAdapterFixture(t)
	prepared, err := f.adapter.PrepareClaim(ctx, f.prepare)
	if err != nil {
		t.Fatal(err)
	}
	intent := QuotaIntentFor(prepared.ConsumerProject, f.prepare.Claim.Namespace, f.prepare.Claim.Name, f.prepare.Claim.UID)
	hold := QuotaHold{Intent: intent, UID: "crash-hold-uid", Generation: 1, ObservedGeneration: 1}
	entitlement := EntitlementEvidence{Name: EntitlementName, UID: "entitlement-uid", Generation: 1, ObservedGeneration: 1, ServiceName: ServiceName}
	if _, err := f.records.Update(ctx, prepared.Authority, func(current *AuthorityRecord) error {
		current.State, current.QuotaIntent, current.QuotaHold, current.Entitlement = StateQuotaGranted, &intent, &hold, &entitlement
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	result, err := f.adapter.Release(ctx, prepared, f.prepare.Claim.UID)
	if err != nil || !result.SafeToRemoveFinalizer || f.custodian.calls != 1 {
		t.Fatalf("release after hold-write crash = %#v, %v, settlements=%d", result, err, f.custodian.calls)
	}
}

func TestPendingHoldDeletionSettlesBeforeFinalizer(t *testing.T) {
	ctx := context.Background()
	f := newAdapterFixture(t)
	prepared, err := f.adapter.PrepareClaim(ctx, f.prepare)
	if err != nil {
		t.Fatal(err)
	}
	f.custodian.quota = pendingHoldQuota{}
	if _, err := f.adapter.Allocate(ctx, prepared, f.allocation); !errors.Is(err, ErrQuotaPending) {
		t.Fatalf("pending allocation = %v", err)
	}
	result, err := f.adapter.Release(ctx, prepared, f.prepare.Claim.UID)
	if err != nil || !result.SafeToRemoveFinalizer || f.custodian.calls != 1 {
		t.Fatalf("pending-hold release = %#v, %v, settlements=%d", result, err, f.custodian.calls)
	}
}

func TestAllocateRejectsTerminalAndCleanupStatesBeforeEffects(t *testing.T) {
	for _, state := range []AuthorityState{StateCleanupVerified, StateReleased, StateQuarantined} {
		t.Run(string(state), func(t *testing.T) {
			ctx := context.Background()
			f := newAdapterFixture(t)
			prepared, err := f.adapter.PrepareClaim(ctx, f.prepare)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.records.Update(ctx, prepared.Authority, func(current *AuthorityRecord) error {
				current.State = state
				if state == StateCleanupVerified {
					intent := QuotaIntentFor(prepared.ConsumerProject, f.prepare.Claim.Namespace, f.prepare.Claim.Name, f.prepare.Claim.UID)
					hold := QuotaHold{Intent: intent, UID: "cleanup-hold", Generation: 1, ObservedGeneration: 1}
					entitlement := EntitlementEvidence{Name: EntitlementName, UID: "entitlement-uid", Generation: 1, ObservedGeneration: 1, ServiceName: ServiceName}
					current.QuotaIntent, current.QuotaHold, current.Entitlement = &intent, &hold, &entitlement
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := f.adapter.Allocate(ctx, prepared, f.allocation); !errors.Is(err, ErrAuthorityInvalid) {
				t.Fatalf("allocation error = %v", err)
			}
			if f.quota.calls != 0 || f.fence.calls != 0 || len(f.executor.requests) != 0 {
				t.Fatalf("effects: quota=%d fence=%d provider=%d", f.quota.calls, f.fence.calls, len(f.executor.requests))
			}
		})
	}
}

func TestDelayedAbortedSettlementCannotOverwriteReleased(t *testing.T) {
	ctx := context.Background()
	f := newAdapterFixture(t)
	prepared, err := f.adapter.PrepareClaim(ctx, f.prepare)
	if err != nil {
		t.Fatal(err)
	}
	intent := QuotaIntentFor(prepared.ConsumerProject, f.prepare.Claim.Namespace, f.prepare.Claim.Name, f.prepare.Claim.UID)
	hold := QuotaHold{Intent: intent, UID: "aborted-hold", Generation: 1, ObservedGeneration: 1}
	entitlement := EntitlementEvidence{Name: EntitlementName, UID: "entitlement-uid", Generation: 1, ObservedGeneration: 1, ServiceName: ServiceName}
	holdDigest, _ := digest(hold)
	receipt := SettlementReceipt{AuthorityRecordID: prepared.Authority.RecordID, Hold: hold.Ref(), HoldDigest: holdDigest, AcknowledgmentID: "settled"}
	if _, err := f.records.Update(ctx, prepared.Authority, func(current *AuthorityRecord) error {
		current.State, current.QuotaIntent, current.QuotaHold, current.Entitlement, current.Settlement = StateQuotaReleasePending, &intent, &hold, &entitlement, &receipt
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.adapter.Lifecycle.RetainAllocationAuthority(ctx, f.prepare.Claim.UID, prepared.ConsumerProject, prepared.ProviderProject, prepared.Authority, hold.Ref()); err != nil {
		t.Fatal(err)
	}
	entered, proceed := make(chan struct{}), make(chan struct{})
	f.adapter.afterAbortedHoldClear = func() { close(entered); <-proceed }
	allocateDone := make(chan error, 1)
	go func() { _, err := f.adapter.Allocate(ctx, prepared, f.allocation); allocateDone <- err }()
	<-entered
	result, err := f.adapter.Release(ctx, prepared, f.prepare.Claim.UID)
	if err != nil || !result.SafeToRemoveFinalizer {
		t.Fatalf("concurrent release = %#v, %v", result, err)
	}
	close(proceed)
	if err := <-allocateDone; !errors.Is(err, ErrAuthorityInvalid) {
		t.Fatalf("delayed settlement error = %v", err)
	}
	record, err := f.records.Get(ctx, prepared.Authority)
	if err != nil || record.State != StateReleased {
		t.Fatalf("record = %#v, %v", record, err)
	}
}

func TestReleaseUsesRetainedProjectWhenConsumerRouteUnavailableOrReused(t *testing.T) {
	for _, mode := range []string{"unavailable", "reused"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			f := newAdapterFixture(t)
			prepared, err := f.adapter.PrepareClaim(ctx, f.prepare)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.adapter.Allocate(ctx, prepared, f.allocation); err != nil {
				t.Fatal(err)
			}
			projects := f.adapter.Projects.(*testProjects)
			if mode == "unavailable" {
				projects.invalid["consumer"] = true
			} else {
				projects.identities["consumer"] = controller.ProjectIdentity{AuthorityID: "milo-test", RouteName: "consumer", UID: "replacement-project-uid"}
			}
			result, err := f.adapter.Release(ctx, prepared, f.prepare.Claim.UID)
			if err != nil || !result.SafeToRemoveFinalizer {
				t.Fatalf("release = %#v, %v", result, err)
			}
			if f.custodian.project != prepared.ConsumerProject {
				t.Fatalf("custodian project = %#v", f.custodian.project)
			}
		})
	}
}

func TestPersistedFenceReceiptIsRevalidatedOnDecode(t *testing.T) {
	for _, corrupt := range []func(*AuthorityRecord){
		func(record *AuthorityRecord) { record.FenceReceipt.CommitDigest = "stale" },
		func(record *AuthorityRecord) { record.FenceReceipt.FenceToken = "replacement" },
	} {
		ctx := context.Background()
		f := newAdapterFixture(t)
		prepared, err := f.adapter.PrepareClaim(ctx, f.prepare)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.adapter.Allocate(ctx, prepared, f.allocation); err != nil {
			t.Fatal(err)
		}
		var secret corev1.Secret
		key := client.ObjectKey{Namespace: "fleet-private", Name: prepared.Authority.RecordName}
		if err := f.records.client.Get(ctx, key, &secret); err != nil {
			t.Fatal(err)
		}
		var record AuthorityRecord
		if err := json.Unmarshal(secret.Data[authorityDataKey], &record); err != nil {
			t.Fatal(err)
		}
		corrupt(&record)
		secret.Data[authorityDataKey], _ = json.Marshal(record)
		if err := f.records.client.Update(ctx, &secret); err != nil {
			t.Fatal(err)
		}
		if _, err := f.records.Get(ctx, prepared.Authority); !errors.Is(err, ErrAuthorityInvalid) {
			t.Fatalf("corrupt record error = %v", err)
		}
	}
}

func TestNoHoldClosureWinsAgainstPausedAcquire(t *testing.T) {
	ctx := context.Background()
	f := newAdapterFixture(t)
	prepared, err := f.adapter.PrepareClaim(ctx, f.prepare)
	if err != nil {
		t.Fatal(err)
	}
	f.custodian.noHold = true
	f.custodian.acquireEntered = make(chan struct{}, 1)
	f.custodian.acquireProceed = make(chan struct{})
	allocateDone := make(chan error, 1)
	go func() { _, err := f.adapter.Allocate(ctx, prepared, f.allocation); allocateDone <- err }()
	<-f.custodian.acquireEntered
	result, err := f.adapter.Release(ctx, prepared, f.prepare.Claim.UID)
	if err != nil || !result.SafeToRemoveFinalizer {
		t.Fatalf("release = %#v, %v", result, err)
	}
	close(f.custodian.acquireProceed)
	if err := <-allocateDone; !errors.Is(err, ErrQuotaUnknown) {
		t.Fatalf("paused acquire error = %v", err)
	}
	if f.quota.calls != 0 {
		t.Fatalf("quota acquire effects after closure = %d", f.quota.calls)
	}
}

func TestAbortedHoldClearIsRetryableAfterCrash(t *testing.T) {
	ctx := context.Background()
	f := newAdapterFixture(t)
	prepared, err := f.adapter.PrepareClaim(ctx, f.prepare)
	if err != nil {
		t.Fatal(err)
	}
	intent := QuotaIntentFor(prepared.ConsumerProject, f.prepare.Claim.Namespace, f.prepare.Claim.Name, f.prepare.Claim.UID)
	hold := QuotaHold{Intent: intent, UID: "aborted-hold", Generation: 1, ObservedGeneration: 1}
	entitlement := EntitlementEvidence{Name: EntitlementName, UID: "entitlement-uid", Generation: 1, ObservedGeneration: 1, ServiceName: ServiceName}
	holdDigest, _ := digest(hold)
	receipt := SettlementReceipt{AuthorityRecordID: prepared.Authority.RecordID, Hold: hold.Ref(), HoldDigest: holdDigest, AcknowledgmentID: "settled"}
	if _, err := f.records.Update(ctx, prepared.Authority, func(current *AuthorityRecord) error {
		current.State, current.QuotaIntent, current.QuotaHold, current.Entitlement, current.Settlement = StateQuotaReleasePending, &intent, &hold, &entitlement, &receipt
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.adapter.Lifecycle.RetainAllocationAuthority(ctx, f.prepare.Claim.UID, prepared.ConsumerProject, prepared.ProviderProject, prepared.Authority, hold.Ref()); err != nil {
		t.Fatal(err)
	}
	if err := f.adapter.Lifecycle.ClearSettledExternalHold(ctx, f.prepare.Claim.UID, prepared.Authority, hold.Ref(), prepared.ConsumerProject); err != nil {
		t.Fatal(err)
	}
	if _, err := f.adapter.Allocate(ctx, prepared, f.allocation); !errors.Is(err, ErrQuotaPending) {
		t.Fatalf("restart = %v", err)
	}
	record, err := f.records.Get(ctx, prepared.Authority)
	if err != nil || record.State != StatePrepared || record.QuotaHold != nil {
		t.Fatalf("record = %#v, %v", record, err)
	}
}

func TestMalformedAuthorityStateShapesFailDecode(t *testing.T) {
	mutations := []func(*AuthorityRecord){
		func(record *AuthorityRecord) { record.State = StateQuotaIntent },
		func(record *AuthorityRecord) {
			intent := QuotaIntentFor(record.SourceRequest.ConsumerProject, record.ClaimNamespace, record.ClaimName, record.ClaimUID)
			record.QuotaIntent = &intent
		},
		func(record *AuthorityRecord) {
			record.State = StateReleased
			intent := QuotaIntentFor(record.SourceRequest.ConsumerProject, record.ClaimNamespace, record.ClaimName, record.ClaimUID)
			entitlement := EntitlementEvidence{Name: EntitlementName, UID: "entitlement-uid", Generation: 1, ObservedGeneration: 1, ServiceName: ServiceName}
			record.QuotaIntent, record.Entitlement = &intent, &entitlement
		},
	}
	for i, mutate := range mutations {
		t.Run(fmt.Sprintf("case-%d", i), func(t *testing.T) {
			ctx := context.Background()
			f := newAdapterFixture(t)
			prepared, err := f.adapter.PrepareClaim(ctx, f.prepare)
			if err != nil {
				t.Fatal(err)
			}
			var secret corev1.Secret
			key := client.ObjectKey{Namespace: "fleet-private", Name: prepared.Authority.RecordName}
			if err := f.records.client.Get(ctx, key, &secret); err != nil {
				t.Fatal(err)
			}
			var record AuthorityRecord
			if err := json.Unmarshal(secret.Data[authorityDataKey], &record); err != nil {
				t.Fatal(err)
			}
			mutate(&record)
			secret.Data[authorityDataKey], _ = json.Marshal(record)
			if err := f.records.client.Update(ctx, &secret); err != nil {
				t.Fatal(err)
			}
			if _, err := f.records.Get(ctx, prepared.Authority); !errors.Is(err, ErrAuthorityInvalid) {
				t.Fatalf("malformed record error = %v", err)
			}
		})
	}
}
