package bmcdiscovery

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
)

const pinIndex = "status.discovery.tls.publicKeySHA256"

// NewScheme registers the kinds discovery reads.
func NewScheme() (*runtime.Scheme, error) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		return nil, err
	}
	return scheme, inventory.AddToScheme(scheme)
}

// NewManager builds the discovery controller. The manager's cluster is the
// Milo project; a second cluster reads only labelled Secrets in the credential
// namespace. There is no leader election: run exactly one replica.
func NewManager(c Config, milo, local *rest.Config, scheme *runtime.Scheme, bmc BMC) (manager.Manager, error) {
	namespaces := map[string]cache.Config{}
	for _, ns := range c.DeviceNamespaces {
		namespaces[ns] = cache.Config{}
	}
	probe := c.HealthProbeAddress
	if probe == "" {
		probe = "0"
	}
	mgr, err := manager.New(milo, manager.Options{
		Scheme:                 scheme,
		Cache:                  cache.Options{DefaultNamespaces: namespaces, DefaultTransform: cache.TransformStripManagedFields()},
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: probe,
		LeaderElection:         false,
	})
	if err != nil {
		return nil, fmt.Errorf("build Milo project manager: %w", err)
	}
	selector := labels.SelectorFromSet(labels.Set{CredentialLabel: "true"})
	secrets, err := cluster.New(local, func(o *cluster.Options) {
		o.Scheme = scheme
		o.Cache = cache.Options{DefaultNamespaces: map[string]cache.Config{c.CredentialNamespace: {}},
			ByObject: map[client.Object]cache.ByObject{&corev1.Secret{}: {Label: selector}}, DefaultTransform: cache.TransformStripManagedFields()}
	})
	if err != nil {
		return nil, fmt.Errorf("build credential Secret cache: %w", err)
	}
	if err := mgr.Add(secrets); err != nil {
		return nil, err
	}
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &inventory.Device{}, pinIndex, func(o client.Object) []string {
		if d := o.(*inventory.Device).Status.Discovery; d != nil && d.TLS != nil {
			return []string{d.TLS.PublicKeySHA256}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	r := &Reconciler{Devices: mgr.GetClient(), Secrets: secrets.GetCache(), Namespace: c.CredentialNamespace, Project: c.Project, BMC: bmc,
		Interval: c.Interval.Duration, RetryBase: c.RetryBase.Duration, MinContact: c.MinContactInterval.Duration}
	// A Device whose observed key changes re-derives every Device holding the
	// old or the new key; that pass reads only retained status.
	peers := func(ctx context.Context, q workqueue.TypedRateLimitingInterface[reconcile.Request], self client.Object, pins ...string) {
		for _, pin := range pins {
			if pin == "" {
				continue
			}
			var list inventory.DeviceList
			if err := mgr.GetClient().List(ctx, &list, client.MatchingFields{pinIndex: pin}); err != nil {
				continue
			}
			for _, d := range list.Items {
				if d.Namespace != self.GetNamespace() || d.Name != self.GetName() {
					q.Add(reconcile.Request{NamespacedName: types.NamespacedName{Namespace: d.Namespace, Name: d.Name}})
				}
			}
		}
	}
	pinOf := func(o client.Object) string {
		if d := o.(*inventory.Device).Status.Discovery; d != nil && d.TLS != nil {
			return d.TLS.PublicKeySHA256
		}
		return ""
	}
	sharedKey := handler.Funcs{
		CreateFunc: func(ctx context.Context, e event.CreateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			peers(ctx, q, e.Object, pinOf(e.Object))
		},
		UpdateFunc: func(ctx context.Context, e event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			if old, cur := pinOf(e.ObjectOld), pinOf(e.ObjectNew); old != cur {
				peers(ctx, q, e.ObjectNew, old, cur)
			}
		},
		DeleteFunc: func(ctx context.Context, e event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			peers(ctx, q, e.Object, pinOf(e.Object))
		},
	}
	credential := handler.EnqueueRequestsFromMapFunc(func(_ context.Context, o client.Object) []reconcile.Request {
		key := deviceKey(o.(*corev1.Secret), c.Project)
		ns, name, ok := strings.Cut(key, "/")
		if !ok {
			return nil
		}
		return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}}}
	})
	err = builder.ControllerManagedBy(mgr).
		Named("bmc-discovery").
		For(&inventory.Device{}).
		Watches(&inventory.Device{}, sharedKey).
		WatchesRawSource(source.Kind(secrets.GetCache(), client.Object(&corev1.Secret{}), credential)).
		WithOptions(controller.Options{MaxConcurrentReconciles: c.Concurrency}).
		Complete(r)
	if err != nil {
		return nil, err
	}
	if c.HealthProbeAddress != "" {
		if err := mgr.AddHealthzCheck("ping", healthz.Ping); err != nil {
			return nil, err
		}
		if err := mgr.AddReadyzCheck("ping", healthz.Ping); err != nil {
			return nil, err
		}
	}
	return mgr, nil
}

// OnceResult is what --once would write for one Device.
type OnceResult struct {
	Namespace string                 `json:"namespace"`
	Name      string                 `json:"name"`
	Status    inventory.DeviceStatus `json:"status"`
}

// RunOnce contacts every eligible Device's BMC once, concurrently up to
// Config.Concurrency, and writes the statuses it would have written to out as
// JSON. It writes nothing to Milo: the Device client is used for reads only.
func RunOnce(ctx context.Context, c Config, devices, secrets client.Reader, bmc BMC, out io.Writer) error {
	var all []inventory.Device
	for _, ns := range c.DeviceNamespaces {
		var list inventory.DeviceList
		if err := devices.List(ctx, &list, client.InNamespace(ns)); err != nil {
			return fmt.Errorf("list Devices in %s: %w", ns, err)
		}
		all = append(all, list.Items...)
	}
	var secretList corev1.SecretList
	if err := secrets.List(ctx, &secretList, client.InNamespace(c.CredentialNamespace), client.MatchingLabels{CredentialLabel: "true"}); err != nil {
		return fmt.Errorf("list credential Secrets: %w", err)
	}
	byDevice := map[string][]corev1.Secret{}
	for _, s := range secretList.Items {
		if key := deviceKey(&s, c.Project); key != "" {
			byDevice[key] = append(byDevice[key], s)
		}
	}
	results := make([]*OnceResult, len(all))
	sem := make(chan struct{}, c.Concurrency)
	var wg sync.WaitGroup
	for i := range all {
		device := &all[i]
		cred, found, credErr := selectCredential(byDevice[device.Namespace+"/"+device.Name])
		if device.Spec.BMC == nil || !found {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			status := device.DeepCopyObject().(*inventory.Device).Status
			apply(&status, contact(ctx, bmc, device.Spec.BMC.Endpoint, cred, credErr, time.Now().UTC().Truncate(time.Second)), device.Generation)
			results[i] = &OnceResult{Namespace: device.Namespace, Name: device.Name, Status: status}
		}()
	}
	wg.Wait()
	// Shared keys are judged against this run's observations.
	for i, r := range results {
		if r != nil {
			all[i].Status = r.Status
		}
	}
	var printed []OnceResult
	for i, r := range results {
		if r == nil {
			continue
		}
		derive(&r.Status, &all[i], sharingKey(&all[i], &r.Status, all), metav1.NewTime(time.Now().UTC().Truncate(time.Second)))
		r.Status.ObservedGeneration = all[i].Generation
		printed = append(printed, *r)
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(printed)
}
