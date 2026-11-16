package controller

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	coordinationv1 "go.datum.net/fleet/internal/api/coordination/v1alpha1"
	sessionv1 "go.datum.net/fleet/internal/api/session/v1alpha1"
	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
)

type managementResolver struct {
	access ManagementAccess
	err    error
}

func (r *managementResolver) ResolveManagementAccess(_ context.Context, _ sessionv1.MachineManagementSessionBindingSpec) (ManagementAccess, error) {
	return r.access, r.err
}

func managementFixture(t *testing.T) (*fixture, AllocationRequest, *AssetRecord, *LifecycleSessionAuthority, sessionv1.MachineManagementSessionBindingSpec) {
	t.Helper()
	ctx := context.Background()
	f := newFixture(t)
	request := f.request("customer")
	request.Claim.Spec.LocationRef.Name = "location"
	if err := f.coordinator.EnsureClaimCustody(ctx, request.Claim, request.NamespaceUID, request.Admission); err != nil {
		t.Fatal(err)
	}
	asset, err := f.coordinator.Allocate(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	authority := &LifecycleSessionAuthority{Coordinator: f.coordinator, Resolver: &managementResolver{access: ManagementAccess{Scope: LocalClaimScope(request.NamespaceUID), NamespaceUID: request.NamespaceUID, MachineUID: request.Claim.UID, CapacityNamespace: asset.MachineNamespace, CapacityName: asset.MachineName, CapacityUID: asset.MachineUID, DeviceName: asset.DeviceName, ConsumerProject: ProjectIdentity{AuthorityID: "local", RouteName: "consumer", UID: request.NamespaceUID}, ProviderProject: ProjectIdentity{AuthorityID: "local", RouteName: "provider", UID: "provider-uid"}, MemberCluster: "cell", LocationName: "location"}}}
	binding := sessionv1.MachineManagementSessionBindingSpec{SessionNamespace: request.Claim.Namespace, SessionName: "access", SessionUID: "session", RequestingPrincipal: "principal", MachineNamespace: request.Claim.Namespace, MachineName: request.Claim.Name, MachineUID: request.Claim.UID, Protocol: "Redfish", AccessProfile: "Provisioning", ClientPublicKey: "key", ConnectBefore: metav1.NewTime(f.now.Add(time.Minute)), ExpiresAt: metav1.NewTime(f.now.Add(time.Hour)), SessionCorrelationID: "session-correlation", OperationCorrelationID: "operation-correlation"}
	return f, request, asset, authority, binding
}

func confirmedBinding(spec sessionv1.MachineManagementSessionBindingSpec) *sessionv1.MachineManagementSessionBinding {
	return &sessionv1.MachineManagementSessionBinding{ObjectMeta: metav1.ObjectMeta{Name: bindingName(spec.SessionUID), Namespace: SystemNamespace, Generation: 2, Annotations: map[string]string{RevokedAtAnnotation: "revoked"}}, Spec: spec, Status: sessionv1.MachineManagementSessionBindingStatus{Conditions: []metav1.Condition{{Type: "CleanupConfirmed", Status: metav1.ConditionTrue, ObservedGeneration: 2, Reason: "Verified"}}}}
}

func TestManagementCustodyBlocksReleaseAndReuse(t *testing.T) {
	ctx := context.Background()
	f, request, asset, authority, proposed := managementFixture(t)
	retained, err := authority.Acquire(ctx, proposed)
	if err != nil {
		t.Fatal(err)
	}
	if retained.AllocationName != assetRecordName(asset.AssetID) || retained.AllocationUID != types.UID(asset.BindingID) || retained.MachineUID == retained.MachineCapacityUID || retained.DeviceName != asset.DeviceName {
		t.Fatalf("identities collapsed: %+v", retained)
	}
	if !authority.Active(ctx, &sessionv1.MachineManagementSessionBinding{Spec: retained}) {
		t.Fatal("retained session inactive")
	}
	result, err := f.coordinator.Release(ctx, request.Claim.UID)
	if !errors.Is(err, ErrManagementActive) || result.SafeToRemoveFinalizer || len(f.executor.requests) != 0 {
		t.Fatalf("release crossed live session: %+v %v", result, err)
	}
	if authority.Active(ctx, &sessionv1.MachineManagementSessionBinding{Spec: retained}) {
		t.Fatal("release still refreshes session authority")
	}
	competitor := f.request("other")
	if err := f.coordinator.EnsureClaimCustody(ctx, competitor.Claim, competitor.NamespaceUID, competitor.Admission); err != nil {
		t.Fatal(err)
	}
	if _, err := f.coordinator.Allocate(ctx, competitor); !errors.Is(err, ErrInsufficientCapacity) {
		t.Fatalf("reuse before teardown: %v", err)
	}
	confirmation := confirmedBinding(retained)
	confirmation.Status.Conditions[0].Status = metav1.ConditionUnknown
	if err := authority.Complete(ctx, confirmation); !errors.Is(err, ErrManagementActive) {
		t.Fatalf("unknown teardown cleared hold: %v", err)
	}
	confirmation.Status.Conditions[0].Status = metav1.ConditionTrue
	confirmation.Status.Conditions[0].ObservedGeneration--
	if err := authority.Complete(ctx, confirmation); !errors.Is(err, ErrManagementActive) {
		t.Fatalf("stale teardown cleared hold: %v", err)
	}
	confirmation.Status.Conditions[0].ObservedGeneration++
	authority = &LifecycleSessionAuthority{Coordinator: f.coordinator}
	if err := authority.Complete(ctx, confirmation); err != nil {
		t.Fatal(err)
	}
	if err := authority.Complete(ctx, confirmation); err != nil {
		t.Fatalf("completion not replayable: %v", err)
	}
	result, err = f.coordinator.Release(ctx, request.Claim.UID)
	if err != nil || !result.SafeToRemoveFinalizer || len(f.executor.requests) != 1 {
		t.Fatalf("verified teardown did not unblock cleanup: %+v %v", result, err)
	}
	next, err := f.coordinator.Allocate(ctx, competitor)
	if err != nil || next.Epoch <= asset.Epoch || next.BindingID == asset.BindingID || next.Management != nil {
		t.Fatalf("reuse inherited authority: %+v %v", next, err)
	}
}

func TestManagementInstallationAndSessionAdmissionSerialize(t *testing.T) {
	ctx := context.Background()
	for iteration := 0; iteration < 20; iteration++ {
		f, request, asset, authority, proposed := managementFixture(t)
		var acquireErr, installErr error
		var wait sync.WaitGroup
		wait.Add(2)
		go func() { defer wait.Done(); _, acquireErr = authority.Acquire(ctx, proposed) }()
		go func() {
			defer wait.Done()
			installErr = f.coordinator.activateOperation(ctx, asset.AssetID, request.Claim.UID, "None", "installation")
		}()
		wait.Wait()
		if acquireErr == nil && installErr == nil {
			t.Fatal("session and installation both acquired hardware authority")
		}
		stored, _, err := f.coordinator.getAsset(ctx, asset.AssetID)
		if err != nil || managementHeld(stored) && stored.ActiveOperationID != "" {
			t.Fatalf("conflicting durable authority: %+v %v", stored, err)
		}
	}
}

func TestManagementInstallWaitsForTeardown(t *testing.T) {
	ctx := context.Background()
	f, request, _, authority, proposed := managementFixture(t)
	retained, err := authority.Acquire(ctx, proposed)
	if err != nil {
		t.Fatal(err)
	}
	installation := &fleetv1.Installation{ObjectMeta: metav1.ObjectMeta{Namespace: request.Claim.Namespace, Name: "install", UID: "installation-uid"}, Spec: fleetv1.InstallationSpec{MachineRef: fleetv1.MachineReference{Name: request.Claim.Name}, ExpectedPredecessor: fleetv1.InstallationPredecessor{Type: "None"}, ImageRef: fleetv1.LocalReference{Name: "registry/image@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}}
	installRequest := InstallationRequest{Claim: request.Claim, Installation: installation, NamespaceUID: request.NamespaceUID, Admission: installAdmission()}
	if _, err := f.coordinator.Install(ctx, installRequest); !errors.Is(err, ErrManagementActive) || len(f.executor.requests) != 0 {
		t.Fatalf("install bypassed session: %v", err)
	}
	if authority.Active(ctx, &sessionv1.MachineManagementSessionBinding{Spec: retained}) {
		t.Fatal("install did not revoke management refresh")
	}
	if err := authority.Complete(ctx, confirmedBinding(retained)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.coordinator.Install(ctx, installRequest); err != nil || len(f.executor.requests) != 1 {
		t.Fatalf("install did not resume: %v", err)
	}
}

func TestManagementParallelSessionsAndReleaseCAS(t *testing.T) {
	ctx := context.Background()
	for iteration := 0; iteration < 20; iteration++ {
		f, request, asset, authority, proposed := managementFixture(t)
		other := proposed
		other.SessionUID, other.SessionName = "other", "other"
		var firstErr, secondErr error
		var wait sync.WaitGroup
		wait.Add(2)
		go func() { defer wait.Done(); _, firstErr = authority.Acquire(ctx, proposed) }()
		go func() { defer wait.Done(); _, secondErr = authority.Acquire(ctx, other) }()
		wait.Wait()
		if (firstErr == nil) == (secondErr == nil) {
			t.Fatalf("expected exactly one session: %v %v", firstErr, secondErr)
		}
		stored, _, err := f.coordinator.getAsset(ctx, asset.AssetID)
		if err != nil {
			t.Fatal(err)
		}
		if err := authority.Complete(ctx, confirmedBinding(stored.Management.Binding)); err != nil {
			t.Fatal(err)
		}
		proposed.SessionUID = "racing-release"
		var releaseErr error
		wait.Add(2)
		go func() { defer wait.Done(); _, firstErr = authority.Acquire(ctx, proposed) }()
		go func() { defer wait.Done(); _, releaseErr = f.coordinator.Release(ctx, request.Claim.UID) }()
		wait.Wait()
		stored, _, err = f.coordinator.getAsset(ctx, asset.AssetID)
		if err != nil {
			t.Fatal(err)
		}
		if firstErr == nil && releaseErr == nil || managementHeld(stored) && len(f.executor.requests) != 0 {
			t.Fatal("release executed cleanup concurrently with management custody")
		}
	}
}

func TestOldTeardownCannotClearNextSession(t *testing.T) {
	ctx := context.Background()
	f, _, asset, authority, proposed := managementFixture(t)
	first, err := authority.Acquire(ctx, proposed)
	if err != nil {
		t.Fatal(err)
	}
	confirmation := confirmedBinding(first)
	if err := authority.Complete(ctx, confirmation); err != nil {
		t.Fatal(err)
	}
	proposed.SessionUID, proposed.SessionName = "next-session", "next-access"
	next, err := authority.Acquire(ctx, proposed)
	if err != nil {
		t.Fatal(err)
	}
	if err := authority.Complete(ctx, confirmation); err != nil {
		t.Fatal(err)
	}
	stored, _, err := f.coordinator.getAsset(ctx, asset.AssetID)
	if err != nil || !managementHeld(stored) || stored.Management.Binding.SessionUID != next.SessionUID {
		t.Fatalf("old cleanup touched new authority: %+v %v", stored, err)
	}
	authority.Resolver.(*managementResolver).err = errors.New("project authorization revoked")
	if authority.Active(ctx, &sessionv1.MachineManagementSessionBinding{Spec: next}) {
		t.Fatal("refresh did not reauthorize")
	}
}

func TestSessionAdmissionConsumesLifecycleCustody(t *testing.T) {
	ctx := context.Background()
	f, request, asset, authority, proposed := managementFixture(t)
	session := &sessionv1.MachineManagementSession{ObjectMeta: metav1.ObjectMeta{Name: proposed.SessionName, Namespace: proposed.SessionNamespace, UID: proposed.SessionUID, Annotations: map[string]string{PrincipalAnnotation: proposed.RequestingPrincipal}}, Spec: sessionv1.MachineManagementSessionSpec{MachineRef: sessionv1.LocalReference{Name: request.Claim.Name}, Protocol: proposed.Protocol, AccessProfile: proposed.AccessProfile, ClientPublicKey: proposed.ClientPublicKey}}
	public := fake.NewClientBuilder().WithScheme(testScheme(t)).WithStatusSubresource(session, &sessionv1.MachineManagementSessionBinding{}).WithObjects(session, request.Claim).Build()
	reconciler := &SessionReconciler{Client: public, Lifecycle: authority, Now: func() time.Time { return f.now }}
	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(session)}); err != nil {
		t.Fatal(err)
	}
	var binding sessionv1.MachineManagementSessionBinding
	if err := public.Get(ctx, client.ObjectKey{Namespace: SystemNamespace, Name: bindingName(session.UID)}, &binding); err != nil {
		t.Fatal(err)
	}
	if binding.Spec.AllocationUID != types.UID(asset.BindingID) || binding.Spec.DeviceName != asset.DeviceName || binding.Spec.MachineCapacityUID != asset.MachineUID {
		t.Fatalf("binding did not use authoritative custody: %+v", binding.Spec)
	}
	if err := public.Get(ctx, client.ObjectKeyFromObject(session), session); err != nil {
		t.Fatal(err)
	}
	if err := public.Delete(ctx, session); err != nil {
		t.Fatal(err)
	}
	reconciler.Now = func() time.Time { return time.Now().Add(time.Hour) }
	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(session)}); err != nil {
		t.Fatal(err)
	}
	if err := public.Get(ctx, client.ObjectKeyFromObject(session), session); err != nil || !contains(session.Finalizers, sessionv1.SessionFinalizer) {
		t.Fatalf("unknown teardown dropped finalizer: %v", err)
	}
}

func TestManagementAuthorizationAndPinnedTargets(t *testing.T) {
	ctx := context.Background()
	for _, mutation := range []string{"missing-authorizer", "denied", "project-lifetime", "namespace-lifetime", "machine-lifetime", "location", "device-unpinned", "capacity-recreated", "device-changed"} {
		t.Run(mutation, func(t *testing.T) {
			f, _, asset, authority, proposed := managementFixture(t)
			resolver := authority.Resolver.(*managementResolver)
			switch mutation {
			case "missing-authorizer":
				authority.Resolver = nil
			case "denied":
				resolver.err = errors.New("denied")
			case "project-lifetime":
				resolver.access.Scope.ProjectUID = "recreated"
			case "machine-lifetime":
				proposed.MachineUID = "recreated"
			case "namespace-lifetime":
				resolver.access.NamespaceUID = "recreated"
			case "capacity-recreated":
				resolver.access.CapacityUID = "recreated"
			case "device-changed":
				resolver.access.DeviceName = "other-device"
			case "location":
				resolver.access.LocationName = "other"
			case "device-unpinned":
				stored, secret, _ := f.coordinator.getAsset(ctx, asset.AssetID)
				stored.DeviceName = ""
				if err := encodeRecord(secret, stored, "asset"); err != nil {
					t.Fatal(err)
				}
				if err := f.coordinator.client.Update(ctx, secret); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := authority.Acquire(ctx, proposed); err == nil {
				t.Fatal("untrusted input acquired authority")
			}
		})
	}
}

func TestManagementRecoveryRetainsExactInputsDuringRelease(t *testing.T) {
	ctx := context.Background()
	f, request, _, authority, proposed := managementFixture(t)
	retained, err := authority.Acquire(ctx, proposed)
	if err != nil {
		t.Fatal(err)
	}
	proposed.ExpiresAt = metav1.NewTime(f.now.Add(2 * time.Hour))
	proposed.SessionCorrelationID = "new-random-value"
	replayed, err := authority.Acquire(ctx, proposed)
	if err != nil || !equality.Semantic.DeepEqual(replayed, retained) {
		t.Fatalf("replay changed inputs: %+v %v", replayed, err)
	}
	if _, err := f.coordinator.Release(ctx, request.Claim.UID); !errors.Is(err, ErrManagementActive) {
		t.Fatal(err)
	}
	session := &sessionv1.MachineManagementSession{ObjectMeta: metav1.ObjectMeta{Name: retained.SessionName, Namespace: retained.SessionNamespace, UID: retained.SessionUID, Annotations: map[string]string{PrincipalAnnotation: retained.RequestingPrincipal}}, Spec: sessionv1.MachineManagementSessionSpec{MachineRef: sessionv1.LocalReference{Name: retained.MachineName}, Protocol: retained.Protocol, AccessProfile: retained.AccessProfile, ClientPublicKey: retained.ClientPublicKey}}
	authority = &LifecycleSessionAuthority{Coordinator: f.coordinator}
	recovered, err := authority.Recover(ctx, session)
	if err != nil || !equality.Semantic.DeepEqual(recovered, &retained) {
		t.Fatalf("orphaned hold cannot recover: %+v %v", recovered, err)
	}
	if err := sessionv1.AddToScheme(f.coordinator.client.Scheme()); err != nil {
		t.Fatal(err)
	}
	if err := f.coordinator.client.Create(ctx, session); err != nil {
		t.Fatal(err)
	}
	reconciler := &SessionReconciler{Client: f.coordinator.client, Lifecycle: authority}
	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(session)}); err != nil {
		t.Fatal(err)
	}
	var binding sessionv1.MachineManagementSessionBinding
	if err := f.coordinator.client.Get(ctx, client.ObjectKey{Namespace: SystemNamespace, Name: bindingName(session.UID)}, &binding); err != nil || !equality.Semantic.DeepEqual(binding.Spec, retained) {
		t.Fatalf("binding recovery: %+v %v", binding, err)
	}
}

func TestProjectionIntentDistinguishesNeverStartedFromUnknown(t *testing.T) {
	ctx := context.Background()
	for _, projected := range []bool{false, true} {
		f, request, _, authority, proposed := managementFixture(t)
		retained, err := authority.Acquire(ctx, proposed)
		if err != nil {
			t.Fatal(err)
		}
		binding := &sessionv1.MachineManagementSessionBinding{Spec: retained}
		if projected {
			if err := authority.BeginProjection(ctx, binding); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := f.coordinator.Release(ctx, request.Claim.UID); !errors.Is(err, ErrManagementActive) {
			t.Fatal(err)
		}
		binding.Annotations = map[string]string{RevokedAtAnnotation: "revoked"}
		closed, err := authority.CancelUnprojected(ctx, binding)
		if err != nil || closed == projected {
			t.Fatalf("projection intent %v, cancellation %v: %v", projected, closed, err)
		}
		if err := authority.BeginProjection(ctx, binding); err == nil {
			t.Fatal("release permitted a new projection")
		}
		if !projected {
			if _, err := f.coordinator.Release(ctx, request.Claim.UID); !errors.Is(err, ErrManagementActive) {
				t.Fatal("hold cleared before confirmation was retained")
			}
			if err := authority.Complete(ctx, confirmedBinding(retained)); err != nil {
				t.Fatal(err)
			}
		}
		result, err := f.coordinator.Release(ctx, request.Claim.UID)
		if projected && (!errors.Is(err, ErrManagementActive) || result.SafeToRemoveFinalizer || len(f.executor.requests) != 0) {
			t.Fatalf("uncertain projection allowed cleanup: %+v %v", result, err)
		}
		if !projected && (err != nil || !result.SafeToRemoveFinalizer) {
			t.Fatalf("never-projected hold could not settle: %+v %v", result, err)
		}
	}
}

func TestLegacyAssetRecordsCannotAdmitManagement(t *testing.T) {
	ctx := context.Background()
	f, request, asset, authority, proposed := managementFixture(t)
	stored, secret, err := f.coordinator.getAsset(ctx, asset.AssetID)
	if err != nil {
		t.Fatal(err)
	}
	stored.SchemaVersion = recordSchemaVersion
	if err := encodeRecord(secret, stored, "asset"); err != nil {
		t.Fatal(err)
	}
	if err := f.coordinator.client.Update(ctx, secret); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.Acquire(ctx, proposed); !errors.Is(err, ErrRecordInvalid) {
		t.Fatalf("legacy record acquired a hold invisible to older writers: %v", err)
	}
	if result, err := f.coordinator.Release(ctx, request.Claim.UID); err != nil || !result.SafeToRemoveFinalizer {
		t.Fatalf("legacy custody cannot drain: %+v %v", result, err)
	}
}

func TestLifecycleProjectionRequiresCurrentTeardownBeforeCleanup(t *testing.T) {
	ctx := context.Background()
	f, request, _, authority, proposed := managementFixture(t)
	retained, err := authority.Acquire(ctx, proposed)
	if err != nil {
		t.Fatal(err)
	}
	binding := &sessionv1.MachineManagementSessionBinding{ObjectMeta: metav1.ObjectMeta{Name: bindingName(retained.SessionUID), Namespace: SystemNamespace, Generation: 1}, Spec: retained}
	scheme := testScheme(t)
	primary := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(binding).WithObjects(binding).Build()
	federation := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&coordinationv1.ProjectedMachineManagementSession{}).Build()
	reconciler := &ProjectionReconciler{Client: primary, FederationClient: federation, Lifecycle: authority, Now: func() time.Time { return f.now }}
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(binding)}
	if _, err := reconciler.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	if _, err := f.coordinator.Release(ctx, request.Claim.UID); !errors.Is(err, ErrManagementActive) {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	var projected coordinationv1.ProjectedMachineManagementSession
	key := client.ObjectKey{Namespace: LocationNamespace, Name: binding.Name}
	if err := federation.Get(ctx, key, &projected); err != nil {
		t.Fatal(err)
	}
	if projected.Spec.DesiredState != sessionv1.ProjectionRevoked {
		t.Fatal("release not propagated")
	}
	projected.Generation = 3
	if err := federation.Update(ctx, &projected); err != nil {
		t.Fatal(err)
	}
	apimeta.SetStatusCondition(&projected.Status.Conditions, metav1.Condition{Type: "CleanupConfirmed", Status: metav1.ConditionTrue, ObservedGeneration: 2, Reason: "Old"})
	if err := federation.Status().Update(ctx, &projected); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	if _, err := f.coordinator.Release(ctx, request.Claim.UID); !errors.Is(err, ErrManagementActive) {
		t.Fatalf("stale evidence cleared custody: %v", err)
	}
	projected.Status.TeardownSpecDigest = coordinationv1.TeardownDigest(&projected)
	projected.Status.Conditions[0].ObservedGeneration = 77
	if err := federation.Status().Update(ctx, &projected); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	if _, err := f.coordinator.Release(ctx, request.Claim.UID); err != nil {
		t.Fatal(err)
	}
	if err := federation.Get(ctx, key, &projected); err == nil {
		t.Fatal("settled projection was not removed")
	}
}
