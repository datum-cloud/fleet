// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"reflect"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	fleetv1alpha1 "go.datum.net/fleet/internal/api/session/v1alpha1"
	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	fleetmetrics "go.datum.net/fleet/internal/metrics"
)

const (
	SystemNamespace                = "fleet-system"
	LocationNamespace              = "machine-access-system"
	PrincipalAnnotation            = "fleet.datumapis.com/requesting-principal"
	MachineUIDLabel                = "fleet.datumapis.com/machine-uid"
	AllocationNameLabel            = "fleet.datumapis.com/allocation-name"
	SessionUIDLabel                = "fleet.datumapis.com/session-uid"
	RevokedAtAnnotation            = "fleet.datumapis.com/revoked-at"
	MemberClusterLabel             = "fleet.datumapis.com/member-cluster"
	MemberClusterAnnotation        = "fleet.datumapis.com/member-cluster"
	ProviderProjectAnnotation      = "fleet.datumapis.com/provider-project"
	LocationNameAnnotation         = "fleet.datumapis.com/location-name"
	AllocationMachineUIDAnnotation = "fleet.datumapis.com/machine-uid"
	DefaultConnectWindow           = time.Minute
	DefaultAuthorityFreshness      = 2 * time.Minute
)

type SessionReconciler struct {
	client.Client
	SystemNamespace string
	Now             func() time.Time
	PerClaimLimit   int
	PerProjectLimit int
}

func (r *SessionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var session fleetv1alpha1.MachineManagementSession
	if err := r.Get(ctx, req.NamespacedName, &session); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !session.DeletionTimestamp.IsZero() {
		return r.revoke(ctx, &session)
	}
	if !controllerutil.ContainsFinalizer(&session, fleetv1alpha1.SessionFinalizer) {
		patch := client.MergeFrom(session.DeepCopy())
		controllerutil.AddFinalizer(&session, fleetv1alpha1.SessionFinalizer)
		if err := r.Patch(ctx, &session, patch); err != nil {
			return ctrl.Result{}, err
		}
	}
	if session.UID == "" {
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}
	admitted, err := r.bindingForSession(ctx, session.UID)
	if err == nil && admitted == nil {
		return r.admit(ctx, &session)
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	return r.reflect(ctx, &session, admitted)
}

func (r *SessionReconciler) admit(ctx context.Context, session *fleetv1alpha1.MachineManagementSession) (ctrl.Result, error) {
	principal := session.Annotations[PrincipalAnnotation]
	if principal == "" {
		return r.reject(ctx, session, "PrincipalMissing", "Admission did not retain the authenticated principal.")
	}
	var machine fleetv1.Machine
	key := types.NamespacedName{Namespace: session.Namespace, Name: session.Spec.MachineRef.Name}
	if err := r.Get(ctx, key, &machine); err != nil {
		if apierrors.IsNotFound(err) {
			return r.reject(ctx, session, "MachineNotFound", "The Machine does not exist.")
		}
		return ctrl.Result{}, err
	}
	var allocations fleetv1.MachineAllocationList
	if err := r.List(ctx, &allocations, client.InNamespace(r.systemNamespace())); err != nil {
		return ctrl.Result{}, err
	}
	matching := make([]*fleetv1.MachineAllocation, 0, 1)
	for i := range allocations.Items {
		a := &allocations.Items[i]
		if a.Status.Phase == "Active" && a.Spec.MachineRef.Namespace == machine.Namespace && a.Spec.MachineRef.Name == machine.Name {
			matching = append(matching, a)
		}
	}
	if len(matching) != 1 {
		return r.reject(ctx, session, "MachineNotAllocated", "The Machine does not have exactly one active authoritative allocation.")
	}
	a := matching[0]
	if a.Annotations[AllocationMachineUIDAnnotation] != string(machine.UID) {
		return r.reject(ctx, session, "AllocationChanged", "The active allocation does not belong to this Machine incarnation.")
	}
	var capacity fleetv1.MachineCapacity
	capacityKey := types.NamespacedName{Namespace: a.Spec.MachineCapacityRef.Namespace, Name: a.Spec.MachineCapacityRef.Name}
	if err := r.Get(ctx, capacityKey, &capacity); err != nil {
		if apierrors.IsNotFound(err) {
			return r.reject(ctx, session, "CapacityNotFound", "The allocated MachineCapacity is unavailable.")
		}
		return ctrl.Result{}, err
	}
	member := a.Annotations[MemberClusterAnnotation]
	location := a.Annotations[LocationNameAnnotation]
	providerProject := a.Annotations[ProviderProjectAnnotation]
	if member == "" || location == "" {
		return r.reject(ctx, session, "LocationNotFederated", "The allocation location has no registered federation member.")
	}
	if providerProject == "" || capacity.Spec.DeviceRef.Name == "" {
		return r.reject(ctx, session, "ProviderTargetUnresolved", "The allocation does not resolve to registered provider capacity.")
	}
	if result, err := r.enforceLimits(ctx, a.Spec.MachineRef.Project, machine.UID); err != nil || result != nil {
		if err != nil {
			return ctrl.Result{}, err
		}
		return r.reject(ctx, session, "SessionLimitExceeded", *result)
	}
	now := r.now()
	ttl := session.Spec.TTL.Duration
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	connect := metav1.NewTime(now.Add(DefaultConnectWindow))
	expiry := metav1.NewTime(now.Add(ttl))
	b := &fleetv1alpha1.MachineManagementSessionBinding{
		TypeMeta: metav1.TypeMeta{APIVersion: fleetv1alpha1.GroupVersion.String(), Kind: "MachineManagementSessionBinding"},
		ObjectMeta: metav1.ObjectMeta{Name: "mms-" + randomCorrelationID(), Namespace: r.systemNamespace(), Labels: map[string]string{
			SessionUIDLabel: string(session.UID), MachineUIDLabel: string(machine.UID), AllocationNameLabel: a.Name,
		}},
		Spec: fleetv1alpha1.MachineManagementSessionBindingSpec{
			SessionNamespace: session.Namespace, SessionName: session.Name, SessionUID: session.UID,
			RequestingPrincipal: principal, ConsumerProject: a.Spec.MachineRef.Project, ProviderProject: providerProject,
			MachineNamespace: machine.Namespace, MachineName: machine.Name, MachineUID: machine.UID,
			AllocationName: a.Name, AllocationUID: a.UID,
			MachineCapacityNamespace: capacity.Namespace, MachineCapacityName: capacity.Name, MachineCapacityUID: capacity.UID,
			DeviceName: capacity.Spec.DeviceRef.Name, LocationName: location, MemberCluster: member,
			Protocol: session.Spec.Protocol, AccessProfile: session.Spec.AccessProfile,
			ClientPublicKey: session.Spec.ClientPublicKey, ConnectBefore: connect, ExpiresAt: expiry,
			SessionCorrelationID: randomCorrelationID(), OperationCorrelationID: randomCorrelationID(),
		},
	}
	if err := r.Create(ctx, b); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}
	fleetmetrics.SessionAdmissions.WithLabelValues("admitted").Inc()
	slog.InfoContext(ctx, "machine-management session admitted",
		"session", session.Name, "machine", machine.Name, "allocation", a.Name,
		"principal", principal, "sessionCorrelationID", b.Spec.SessionCorrelationID)
	return ctrl.Result{RequeueAfter: time.Second}, nil
}

func (r *SessionReconciler) enforceLimits(ctx context.Context, project string, machineUID types.UID) (*string, error) {
	var list fleetv1alpha1.MachineManagementSessionBindingList
	if err := r.List(ctx, &list, client.InNamespace(r.systemNamespace())); err != nil {
		return nil, err
	}
	machineCount, projectCount := 0, 0
	now := r.now()
	for i := range list.Items {
		b := &list.Items[i]
		if !b.Spec.ExpiresAt.After(now) || b.Annotations[RevokedAtAnnotation] != "" {
			continue
		}
		if b.Spec.MachineUID == machineUID {
			machineCount++
		}
		if b.Spec.ConsumerProject == project {
			projectCount++
		}
	}
	cl := r.PerClaimLimit
	if cl == 0 {
		cl = 1
	}
	pl := r.PerProjectLimit
	if pl == 0 {
		pl = 5
	}
	if machineCount >= cl {
		s := fmt.Sprintf("The Machine already has %d active session(s); limit is %d.", machineCount, cl)
		return &s, nil
	}
	if projectCount >= pl {
		s := fmt.Sprintf("The project already has %d active session(s); limit is %d.", projectCount, pl)
		return &s, nil
	}
	return nil, nil
}

func (r *SessionReconciler) reflect(ctx context.Context, s *fleetv1alpha1.MachineManagementSession, b *fleetv1alpha1.MachineManagementSessionBinding) (ctrl.Result, error) {
	if equalityStatus(s, b) {
		if r.now().After(b.Spec.ExpiresAt.Time) {
			patch := client.MergeFrom(b.DeepCopy())
			if b.Annotations == nil {
				b.Annotations = map[string]string{}
			}
			b.Annotations[RevokedAtAnnotation] = r.now().UTC().Format(time.RFC3339Nano)
			if err := r.Patch(ctx, b, patch); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{RequeueAfter: time.Until(b.Spec.ExpiresAt.Time)}, nil
	}
	s.Status.ConnectBefore = b.Spec.ConnectBefore.DeepCopy()
	s.Status.ExpiresAt = b.Spec.ExpiresAt.DeepCopy()
	s.Status.Connection = b.Status.Connection
	if c := apimeta.FindStatusCondition(b.Status.Conditions, fleetv1alpha1.ConditionReady); c != nil {
		condition := *c
		condition.ObservedGeneration = s.Generation
		apimeta.SetStatusCondition(&s.Status.Conditions, condition)
	} else {
		setCondition(&s.Status.Conditions, s.Generation, metav1.ConditionUnknown, "Pending", "Waiting for location projection.", r.now())
	}
	if err := r.Status().Update(ctx, s); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: time.Until(b.Spec.ExpiresAt.Time)}, nil
}

func (r *SessionReconciler) reject(ctx context.Context, s *fleetv1alpha1.MachineManagementSession, reason, message string) (ctrl.Result, error) {
	fleetmetrics.SessionAdmissions.WithLabelValues(reason).Inc()
	slog.WarnContext(ctx, "machine-management session rejected", "session", s.Name, "reason", reason)
	setCondition(&s.Status.Conditions, s.Generation, metav1.ConditionFalse, reason, message, r.now())
	return ctrl.Result{}, r.Status().Update(ctx, s)
}

func (r *SessionReconciler) revoke(ctx context.Context, s *fleetv1alpha1.MachineManagementSession) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(s, fleetv1alpha1.SessionFinalizer) {
		return ctrl.Result{}, nil
	}
	b, err := r.bindingForSession(ctx, s.UID)
	if err == nil && b != nil {
		if b.Annotations[RevokedAtAnnotation] == "" {
			patch := client.MergeFrom(b.DeepCopy())
			if b.Annotations == nil {
				b.Annotations = map[string]string{}
			}
			b.Annotations[RevokedAtAnnotation] = r.now().UTC().Format(time.RFC3339Nano)
			if err := r.Patch(ctx, b, patch); err != nil {
				return ctrl.Result{}, err
			}
			slog.InfoContext(ctx, "machine-management session revocation requested", "session", s.Name, "allocation", b.Spec.AllocationName, "sessionCorrelationID", b.Spec.SessionCorrelationID)
		}
		if c := apimeta.FindStatusCondition(b.Status.Conditions, "CleanupConfirmed"); c != nil && c.Status == metav1.ConditionTrue {
			fleetmetrics.RevocationLatency.Observe(r.now().Sub(s.DeletionTimestamp.Time).Seconds())
			patch := client.MergeFrom(s.DeepCopy())
			controllerutil.RemoveFinalizer(s, fleetv1alpha1.SessionFinalizer)
			return ctrl.Result{}, r.Patch(ctx, s, patch)
		}
		if r.now().After(s.DeletionTimestamp.Add(30 * time.Second)) {
			fleetmetrics.RevocationLatency.Observe(r.now().Sub(s.DeletionTimestamp.Time).Seconds())
			setNamedCondition(&b.Status.Conditions, "CleanupConfirmed", b.Generation, metav1.ConditionUnknown, "LocationUnconfirmed", "The location did not confirm session teardown.", r.now())
			if err := r.Status().Update(ctx, b); err != nil {
				return ctrl.Result{}, err
			}
			patch := client.MergeFrom(s.DeepCopy())
			controllerutil.RemoveFinalizer(s, fleetv1alpha1.SessionFinalizer)
			return ctrl.Result{}, r.Patch(ctx, s, patch)
		}
		return ctrl.Result{RequeueAfter: time.Second}, nil
	} else if err != nil {
		return ctrl.Result{}, err
	}
	patch := client.MergeFrom(s.DeepCopy())
	controllerutil.RemoveFinalizer(s, fleetv1alpha1.SessionFinalizer)
	return ctrl.Result{}, r.Patch(ctx, s, patch)
}

func (r *SessionReconciler) bindingForSession(ctx context.Context, uid types.UID) (*fleetv1alpha1.MachineManagementSessionBinding, error) {
	var list fleetv1alpha1.MachineManagementSessionBindingList
	if err := r.List(ctx, &list, client.InNamespace(r.systemNamespace()), client.MatchingLabels{SessionUIDLabel: string(uid)}); err != nil {
		return nil, err
	}
	if len(list.Items) == 0 {
		return nil, nil
	}
	if len(list.Items) != 1 || list.Items[0].Spec.SessionUID != uid {
		return nil, fmt.Errorf("expected one protected binding for session UID")
	}
	return &list.Items[0], nil
}

func (r *SessionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.Client = mgr.GetClient()
	return ctrl.NewControllerManagedBy(mgr).
		For(&fleetv1alpha1.MachineManagementSession{}).
		Watches(&fleetv1alpha1.MachineManagementSessionBinding{}, handler.EnqueueRequestsFromMapFunc(func(_ context.Context, obj client.Object) []reconcile.Request {
			b := obj.(*fleetv1alpha1.MachineManagementSessionBinding)
			return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: b.Spec.SessionNamespace, Name: b.Spec.SessionName}}}
		})).
		WithOptions(controller.Options{MaxConcurrentReconciles: 1}).Complete(r)
}
func (r *SessionReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}
func (r *SessionReconciler) systemNamespace() string {
	if r.SystemNamespace != "" {
		return r.SystemNamespace
	}
	return SystemNamespace
}

func setCondition(cs *[]metav1.Condition, generation int64, status metav1.ConditionStatus, reason, message string, now time.Time) {
	apimeta.SetStatusCondition(cs, metav1.Condition{Type: fleetv1alpha1.ConditionReady, Status: status, Reason: reason, Message: message, ObservedGeneration: generation, LastTransitionTime: metav1.NewTime(now)})
}
func setNamedCondition(cs *[]metav1.Condition, name string, generation int64, status metav1.ConditionStatus, reason, message string, now time.Time) {
	apimeta.SetStatusCondition(cs, metav1.Condition{Type: name, Status: status, Reason: reason, Message: message, ObservedGeneration: generation, LastTransitionTime: metav1.NewTime(now)})
}
func randomCorrelationID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func equalityStatus(s *fleetv1alpha1.MachineManagementSession, b *fleetv1alpha1.MachineManagementSessionBinding) bool {
	return reflect.DeepEqual(s.Status.Connection, b.Status.Connection) && reflect.DeepEqual(s.Status.Conditions, b.Status.Conditions) && s.Status.ConnectBefore != nil && s.Status.ConnectBefore.Equal(&b.Spec.ConnectBefore) && s.Status.ExpiresAt != nil && s.Status.ExpiresAt.Equal(&b.Spec.ExpiresAt)
}
