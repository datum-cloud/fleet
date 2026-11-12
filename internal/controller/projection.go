// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"fmt"
	"reflect"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	fleetv1alpha1 "go.datum.net/fleet/api/v1alpha1"
)

// ProjectionReconciler carries the least-privileged session copy into one
// authoritative location API. It never participates in Redfish transport.
type ProjectionReconciler struct {
	client.Client
	LocationClient    client.Client
	SystemNamespace   string
	LocationNamespace string
	Now               func() time.Time
	Freshness         time.Duration
}

func (r *ProjectionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var b fleetv1alpha1.MachineManagementSessionBinding
	if err := r.Get(ctx, req.NamespacedName, &b); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if req.Namespace != r.systemNamespace() {
		return ctrl.Result{}, nil
	}
	key := types.NamespacedName{Namespace: r.locationNamespace(), Name: b.Name}
	active := r.allocationStillActive(ctx, &b)
	if !active && b.Annotations[RevokedAtAnnotation] == "" {
		patch := client.MergeFrom(b.DeepCopy())
		if b.Annotations == nil {
			b.Annotations = map[string]string{}
		}
		// Allocation release is a one-way fence for this admitted session. An
		// allocator later reusing the claim name or reactivating a record cannot
		// resurrect authority tied to the old binding epoch.
		b.Annotations[RevokedAtAnnotation] = r.now().UTC().Format(time.RFC3339Nano)
		if err := r.Patch(ctx, &b, patch); err != nil {
			return ctrl.Result{}, err
		}
	}
	if b.Annotations[RevokedAtAnnotation] != "" || !active {
		return r.removeProjection(ctx, &b, key)
	}
	var p fleetv1alpha1.ProjectedMachineManagementSession
	err := r.LocationClient.Get(ctx, key, &p)
	if apierrors.IsNotFound(err) {
		p = *r.build(&b)
		if err := r.LocationClient.Create(ctx, &p); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	if !projectionMatches(&b, &p) {
		setNamedCondition(&b.Status.Conditions, fleetv1alpha1.ConditionReady, b.Generation, metav1.ConditionFalse, "ProjectionChanged", "Location projection no longer matches the admitted session inputs.", r.now())
		return ctrl.Result{}, r.Status().Update(ctx, &b)
	}
	// Refresh only cached-authority time while all immutable binding inputs match.
	fresh := metav1.NewTime(r.now().Add(r.freshness()))
	if p.Spec.AuthorityFreshUntil.Before(&fresh) {
		patch := client.MergeFrom(p.DeepCopy())
		p.Spec.AuthorityFreshUntil = fresh
		if err := r.LocationClient.Patch(ctx, &p, patch); err != nil {
			return ctrl.Result{}, err
		}
	}
	if !reflect.DeepEqual(b.Status.Connection, p.Status.Connection) || !reflect.DeepEqual(b.Status.Conditions, p.Status.Conditions) {
		b.Status.Connection = p.Status.Connection
		b.Status.Conditions = append([]metav1.Condition(nil), p.Status.Conditions...)
		for i := range b.Status.Conditions {
			b.Status.Conditions[i].ObservedGeneration = b.Generation
		}
		if err := r.Status().Update(ctx, &b); err != nil {
			return ctrl.Result{}, err
		}
	}
	if p.Status.Connection == nil {
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}
	return ctrl.Result{RequeueAfter: r.freshness() / 2}, nil
}

func (r *ProjectionReconciler) allocationStillActive(ctx context.Context, b *fleetv1alpha1.MachineManagementSessionBinding) bool {
	var list fleetv1alpha1.MachineAllocationBindingList
	if err := r.List(ctx, &list, client.InNamespace(r.systemNamespace()), client.MatchingLabels{ClaimUIDLabel: string(b.Spec.ClaimUID)}); err != nil || len(list.Items) != 1 {
		return false
	}
	a := list.Items[0].Spec
	return a.Active && a.ClaimUID == b.Spec.ClaimUID && a.MachineUID == b.Spec.MachineUID && a.BindingID == b.Spec.BindingID && a.BindingEpoch == b.Spec.BindingEpoch && a.LocationUID == b.Spec.LocationUID && a.LocationRevision == b.Spec.LocationRevision && a.AdapterRoute == b.Spec.AdapterRoute
}

func (r *ProjectionReconciler) removeProjection(ctx context.Context, b *fleetv1alpha1.MachineManagementSessionBinding, key types.NamespacedName) (ctrl.Result, error) {
	var p fleetv1alpha1.ProjectedMachineManagementSession
	err := r.LocationClient.Get(ctx, key, &p)
	if err == nil {
		if p.DeletionTimestamp.IsZero() {
			if err := r.LocationClient.Delete(ctx, &p); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}
	if !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	setNamedCondition(&b.Status.Conditions, "CleanupConfirmed", b.Generation, metav1.ConditionTrue, "LocationTeardownConfirmed", "The location session projection was removed.", r.now())
	setNamedCondition(&b.Status.Conditions, fleetv1alpha1.ConditionReady, b.Generation, metav1.ConditionFalse, "Revoked", "The session authority has ended.", r.now())
	return ctrl.Result{}, r.Status().Update(ctx, b)
}

func (r *ProjectionReconciler) build(b *fleetv1alpha1.MachineManagementSessionBinding) *fleetv1alpha1.ProjectedMachineManagementSession {
	s := b.Spec
	return &fleetv1alpha1.ProjectedMachineManagementSession{TypeMeta: metav1.TypeMeta{APIVersion: fleetv1alpha1.GroupVersion.String(), Kind: "ProjectedMachineManagementSession"}, ObjectMeta: metav1.ObjectMeta{Name: b.Name, Namespace: r.locationNamespace(), Labels: map[string]string{SessionUIDLabel: string(s.SessionUID), ClaimUIDLabel: string(s.ClaimUID)}}, Spec: fleetv1alpha1.ProjectedMachineManagementSessionSpec{SessionUID: s.SessionUID, ClaimUID: s.ClaimUID, MachineUID: s.MachineUID, BindingID: s.BindingID, BindingEpoch: s.BindingEpoch, LocationUID: s.LocationUID, LocationRevision: s.LocationRevision, BareMetalMachineUID: s.BareMetalMachineUID, AccessProfileRevision: s.AccessProfileRevision, ClientPublicKey: s.ClientPublicKey, ConnectBefore: s.ConnectBefore, ExpiresAt: s.ExpiresAt, AuthorityFreshUntil: metav1.NewTime(r.now().Add(r.freshness())), SessionCorrelationID: s.SessionCorrelationID, OperationCorrelationID: s.OperationCorrelationID, AdapterRoute: s.AdapterRoute, BMCSystemID: s.BMCSystemID}}
}

func projectionMatches(b *fleetv1alpha1.MachineManagementSessionBinding, p *fleetv1alpha1.ProjectedMachineManagementSession) bool {
	s := b.Spec
	x := p.Spec
	return x.SessionUID == s.SessionUID && x.ClaimUID == s.ClaimUID && x.MachineUID == s.MachineUID && x.BindingID == s.BindingID && x.BindingEpoch == s.BindingEpoch && x.LocationUID == s.LocationUID && x.LocationRevision == s.LocationRevision && x.BareMetalMachineUID == s.BareMetalMachineUID && x.AccessProfileRevision == s.AccessProfileRevision && x.ClientPublicKey == s.ClientPublicKey && x.ConnectBefore.Equal(&s.ConnectBefore) && x.ExpiresAt.Equal(&s.ExpiresAt) && x.AdapterRoute == s.AdapterRoute && x.BMCSystemID == s.BMCSystemID
}
func (r *ProjectionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.LocationClient == nil {
		return fmt.Errorf("location client is required")
	}
	r.Client = mgr.GetClient()
	return ctrl.NewControllerManagedBy(mgr).
		For(&fleetv1alpha1.MachineManagementSessionBinding{}).
		Watches(&fleetv1alpha1.MachineAllocationBinding{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
			a := obj.(*fleetv1alpha1.MachineAllocationBinding)
			var list fleetv1alpha1.MachineManagementSessionBindingList
			if err := r.List(ctx, &list, client.InNamespace(r.systemNamespace()), client.MatchingLabels{ClaimUIDLabel: string(a.Spec.ClaimUID)}); err != nil {
				return nil
			}
			out := make([]reconcile.Request, 0, len(list.Items))
			for i := range list.Items {
				out = append(out, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: list.Items[i].Namespace, Name: list.Items[i].Name}})
			}
			return out
		})).Complete(r)
}
func (r *ProjectionReconciler) systemNamespace() string {
	if r.SystemNamespace != "" {
		return r.SystemNamespace
	}
	return SystemNamespace
}
func (r *ProjectionReconciler) locationNamespace() string {
	if r.LocationNamespace != "" {
		return r.LocationNamespace
	}
	return LocationNamespace
}
func (r *ProjectionReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}
func (r *ProjectionReconciler) freshness() time.Duration {
	if r.Freshness > 0 {
		return r.Freshness
	}
	return DefaultAuthorityFreshness
}

var _ = apimeta.FindStatusCondition
