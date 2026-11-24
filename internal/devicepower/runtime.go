package devicepower

import (
	"context"
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	metal "go.datum.net/fleet/internal/api/v1alpha1"
)

// NewScheme registers the kinds the dispatcher and worker use.
func NewScheme() (*runtime.Scheme, error) {
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, batchv1.AddToScheme, coordinationv1.AddToScheme, rbacv1.AddToScheme,
		inventory.AddToScheme, metal.AddToScheme} {
		if err := add(scheme); err != nil {
			return nil, err
		}
	}
	return scheme, nil
}

// NewManager builds the dispatcher. The manager's cluster is the Milo
// project; a second cluster is the site, where it reads and writes only its
// own labelled objects in the worker namespace. There is no leader election:
// run one replica. A second one is safe, because every acceptance, lock,
// record and Job write is optimistic or create-once, but it is not needed.
func NewManager(c Config, milo, local *rest.Config, scheme *runtime.Scheme) (manager.Manager, error) {
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
	selector := labels.SelectorFromSet(labels.Set{PartOfLabel: PartOf})
	site, err := cluster.New(local, func(o *cluster.Options) {
		o.Scheme = scheme
		o.Cache = cache.Options{DefaultNamespaces: map[string]cache.Config{c.WorkerNamespace: {}}, DefaultLabelSelector: selector,
			DefaultTransform: cache.TransformStripManagedFields()}
	})
	if err != nil {
		return nil, fmt.Errorf("build site cache: %w", err)
	}
	if err := mgr.Add(site); err != nil {
		return nil, err
	}
	r := &Reconciler{Milo: mgr.GetClient(), MiloReader: mgr.GetAPIReader(), Site: site.GetClient(), SiteReader: site.GetAPIReader(),
		Namespace: c.WorkerNamespace, Config: c}
	toOperation := handler.EnqueueRequestsFromMapFunc(func(_ context.Context, o client.Object) []reconcile.Request {
		ns, name := o.GetAnnotations()[OperationNamespace], o.GetAnnotations()[OperationName]
		if ns == "" || name == "" {
			return nil
		}
		return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}}}
	})
	err = builder.ControllerManagedBy(mgr).
		Named("device-power-dispatcher").
		For(&inventory.DevicePowerOperation{}).
		WatchesRawSource(source.Kind(site.GetCache(), client.Object(&corev1.ConfigMap{}), toOperation)).
		WatchesRawSource(source.Kind(site.GetCache(), client.Object(&batchv1.Job{}), toOperation)).
		WithOptions(controller.Options{MaxConcurrentReconciles: 4}).
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
