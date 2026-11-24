package bmcdiscovery

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
)

// Reconciler discovers one Device per reconcile. Devices without spec.bmc or
// without a credential Secret are skipped and their status is not written.
type Reconciler struct {
	// Devices reads Devices and writes only their status subresource.
	Devices client.Client
	// Secrets reads credential Secrets in Namespace of the local cluster.
	Secrets   client.Reader
	Namespace string
	Project   string
	BMC       BMC
	// Interval, RetryBase and MinContact are Config's durations.
	Interval, RetryBase, MinContact time.Duration
	Now                             func() time.Time

	mu      sync.Mutex
	targets map[types.NamespacedName]*target
	// contacts is the last attempt per endpoint, across Devices.
	contacts map[string]time.Time
}

// target is the in-memory attempt schedule of one Device. It is rebuilt
// after a restart, which therefore contacts each BMC once more.
type target struct {
	next     time.Time
	failures int
	version  string
	endpoint string
}

func (r *Reconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// credentialsFor lists the credential Secrets naming the Device.
func (r *Reconciler) credentialsFor(ctx context.Context, key types.NamespacedName) ([]corev1.Secret, error) {
	var list corev1.SecretList
	if err := r.Secrets.List(ctx, &list, client.InNamespace(r.Namespace), client.MatchingLabels{CredentialLabel: "true"}); err != nil {
		return nil, err
	}
	var out []corev1.Secret
	for _, s := range list.Items {
		if deviceKey(&s, r.Project) == key.String() {
			out = append(out, s)
		}
	}
	return out, nil
}

// due reports whether the Device's BMC should be contacted now, or else how
// long until it should be.
func (r *Reconciler) due(key types.NamespacedName, endpoint, version string, now time.Time) (bool, time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t := r.targets[key]
	if t != nil && t.version == version && t.endpoint == endpoint && now.Before(t.next) {
		return false, t.next.Sub(now)
	}
	if last, ok := r.contacts[endpoint]; ok && now.Before(last.Add(r.MinContact)) {
		return false, last.Add(r.MinContact).Sub(now)
	}
	return true, 0
}

// record schedules the next attempt after one finished.
func (r *Reconciler) record(key types.NamespacedName, endpoint, version string, uncertain bool, now time.Time) time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.targets == nil {
		r.targets, r.contacts = map[types.NamespacedName]*target{}, map[string]time.Time{}
	}
	r.contacts[endpoint] = now
	t := r.targets[key]
	if t == nil || t.version != version || t.endpoint != endpoint {
		t = &target{version: version, endpoint: endpoint}
		r.targets[key] = t
	}
	wait := r.Interval
	if uncertain {
		t.failures++
		wait = r.RetryBase << min(t.failures-1, 20)
		if wait <= 0 || wait > r.Interval {
			wait = r.Interval
		}
	} else {
		t.failures = 0
	}
	t.next = now.Add(wait)
	return wait
}

func (r *Reconciler) forget(key types.NamespacedName) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.targets, key)
}

func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	var device inventory.Device
	if err := r.Devices.Get(ctx, req.NamespacedName, &device); err != nil {
		if apierrors.IsNotFound(err) {
			r.forget(req.NamespacedName)
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}
	if device.Spec.BMC == nil || !device.DeletionTimestamp.IsZero() {
		r.forget(req.NamespacedName)
		return reconcile.Result{}, nil
	}
	secrets, err := r.credentialsFor(ctx, req.NamespacedName)
	if err != nil {
		return reconcile.Result{}, err
	}
	cred, found, credErr := selectCredential(secrets)
	if !found {
		r.forget(req.NamespacedName)
		return reconcile.Result{}, nil
	}
	now := r.now()
	endpoint := device.Spec.BMC.Endpoint
	status := &device.DeepCopyObject().(*inventory.Device).Status
	contactNow, wait := r.due(req.NamespacedName, endpoint, cred.Version, now)
	if contactNow {
		a := contact(ctx, r.BMC, endpoint, cred, credErr, now)
		wait = r.record(req.NamespacedName, endpoint, cred.Version, a.uncertain, now)
		apply(status, a, device.Generation)
	}
	var all inventory.DeviceList
	if err := r.Devices.List(ctx, &all); err != nil {
		return reconcile.Result{}, err
	}
	derive(status, &device, sharingKey(&device, status, all.Items), metav1.NewTime(now.UTC().Truncate(time.Second)))
	status.ObservedGeneration = device.Generation
	if !sameStatus(&device.Status, status) {
		patch := client.MergeFromWithOptions(device.DeepCopyObject().(*inventory.Device), client.MergeFromWithOptimisticLock{})
		device.Status = *status
		if err := r.Devices.Status().Patch(ctx, &device, patch); err != nil {
			// The observation is not retained; contact again, still subject
			// to the per-endpoint gap.
			r.forget(req.NamespacedName)
			return reconcile.Result{}, err
		}
	}
	return reconcile.Result{RequeueAfter: wait}, nil
}

func sameStatus(a, b *inventory.DeviceStatus) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}
