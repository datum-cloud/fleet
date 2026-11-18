// Package requestresolver runs the production requested-hardware projection
// across every engaged project cluster. It registers exactly one controller,
// which watches only Machines and MachineProfiles. It never starts Fleet's
// lifecycle controllers, never opens a private namespace, and never watches
// networks, quota, entitlements, locations, or Secrets.
package requestresolver

import (
	"context"
	"errors"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mchandler "sigs.k8s.io/multicluster-runtime/pkg/handler"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/requestedhardware"
)

const (
	// ControllerName is the resolver's only controller.
	ControllerName = "fleet-request-resolver"

	// IndexMachineProfileName indexes Machines by spec.machineProfileRef.name
	// in every engaged project cache, so a profile event fans out only to the
	// Machines that reference it.
	IndexMachineProfileName = "fleet.requestresolver/machine-profile-name"
)

// ClusterGetter resolves an engaged project cluster. mcmanager.Manager
// satisfies it; a project that is not (or no longer) engaged returns an error
// wrapping multicluster.ErrClusterNotFound.
type ClusterGetter interface {
	GetCluster(context.Context, multicluster.ClusterName) (cluster.Cluster, error)
}

// Reconciler projects requested hardware for one Machine in one engaged
// project. Each request is scoped by its cluster name: reads and writes go
// only through that project's cluster client, so a failure in one project
// never reaches another.
type Reconciler struct {
	Clusters ClusterGetter
}

func (r Reconciler) Reconcile(ctx context.Context, request mcreconcile.Request) (reconcile.Result, error) {
	if r.Clusters == nil {
		return reconcile.Result{}, errors.New("request resolver has no cluster source")
	}
	cl, err := r.Clusters.GetCluster(ctx, request.ClusterName)
	if errors.Is(err, multicluster.ErrClusterNotFound) {
		// The project left the consumer set (or is being disengaged). Its
		// Machines are no longer ours to describe: drop the request without
		// writing anything, in particular never an unresolved condition.
		log.FromContext(ctx).V(1).Info("dropping request for a project that is not engaged", "project", request.ClusterName)
		return reconcile.Result{}, nil
	}
	if err != nil {
		return reconcile.Result{}, err
	}
	// The resolver keeps no custody or authority records, so there is no
	// retained Machine lifetime to match: it projects whichever Machine
	// currently has this name in the engaged project.
	return reconcile.Result{}, requestedhardware.Project(ctx, cl.GetClient(), request.NamespacedName, "")
}

// IndexMachineProfile is the extractor behind IndexMachineProfileName.
func IndexMachineProfile(object client.Object) []string {
	machine, ok := object.(*fleetv1.Machine)
	if !ok || machine.Spec.MachineProfileRef.Name == "" {
		return nil
	}
	return []string{machine.Spec.MachineProfileRef.Name}
}

// Setup registers the resolver's index and its single controller on mgr. The
// controller engages provider clusters only, never the host cluster.
func Setup(ctx context.Context, mgr mcmanager.Manager) error {
	if mgr == nil {
		return errors.New("request resolver requires a multicluster manager")
	}
	if err := mgr.GetFieldIndexer().IndexField(ctx, &fleetv1.Machine{}, IndexMachineProfileName, IndexMachineProfile); err != nil {
		return err
	}
	return mcbuilder.ControllerManagedBy(mgr).
		Named(ControllerName).
		For(&fleetv1.Machine{}, mcbuilder.WithEngageWithLocalCluster(false), mcbuilder.WithEngageWithProviderClusters(true)).
		Watches(&fleetv1.MachineProfile{}, profileHandler, mcbuilder.WithEngageWithLocalCluster(false), mcbuilder.WithEngageWithProviderClusters(true)).
		Complete(Reconciler{Clusters: mgr})
}

// profileHandler maps a MachineProfile event to the Machines in the same
// project that reference it and are not yet pinned. A pinned snapshot is never
// re-resolved, so re-queuing it would only re-read the Machine.
func profileHandler(name multicluster.ClusterName, cl cluster.Cluster) mchandler.EventHandler {
	return mchandler.ForCluster(handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, object client.Object) []reconcile.Request {
		return MachinesForProfile(ctx, cl.GetClient(), object.GetName())
	}), name)
}

// MachinesForProfile lists the unpinned Machines that reference profile in
// one project.
func MachinesForProfile(ctx context.Context, reader client.Reader, profile string) []reconcile.Request {
	if profile == "" {
		return nil
	}
	var machines fleetv1.MachineList
	if err := reader.List(ctx, &machines, client.MatchingFields{IndexMachineProfileName: profile}); err != nil {
		// The cache is the event source; a failed indexed read here can only
		// come from a stopping or broken cache. Machine events still arrive
		// on their own watch.
		log.FromContext(ctx).Error(err, "listing Machines for MachineProfile event", "profile", profile)
		return nil
	}
	requests := make([]reconcile.Request, 0, len(machines.Items))
	for index := range machines.Items {
		machine := &machines.Items[index]
		if machine.Status.RequestedHardware != nil || machine.Spec.MachineProfileRef.Name != profile {
			continue
		}
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(machine)})
	}
	return requests
}
