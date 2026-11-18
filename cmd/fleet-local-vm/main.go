// fleet-local-vm is an explicit local acceptance harness, not a deployed controller.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	api "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
	"go.datum.net/fleet/internal/executor"
	"go.datum.net/fleet/internal/localvm"
	"go.datum.net/fleet/internal/simulator"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	"os"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"strings"
	"time"
)

type crashAfterEffect struct{ *localvm.Provider }

func (p crashAfterEffect) Execute(ctx context.Context, q simulator.Request) (simulator.OperationResult, error) {
	r, e := p.Provider.Execute(ctx, q)
	if e == nil && q.Kind == "install" {
		os.Exit(75)
	}
	return r, e
}

func main() {
	if e := work(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func work() error {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	p, e := localvm.Open()
	if e != nil {
		return e
	}
	if len(os.Args) == 2 && os.Args[1] == "boot-denials" {
		summary, err := p.CheckClosedBoot("")
		if err != nil {
			return err
		}
		return os.WriteFile(localvm.Root+"/boot-denials.txt", []byte(summary+"\n"), 0600)
	}
	if len(os.Args) == 2 && os.Args[1] == "boot-service" {
		return p.ServeBoot()
	}
	if len(os.Args) == 2 && os.Args[1] == "image" {
		fmt.Println(p.ImageReference())
		return nil
	}
	if len(os.Args) != 2 {
		return errors.New("phase required")
	}
	config, e := clientcmd.BuildConfigFromFlags("", "/run/fleet/kubeconfig")
	if e != nil {
		return e
	}
	scheme := runtime.NewScheme()
	core.AddToScheme(scheme)
	api.AddToScheme(scheme)
	k, e := client.New(config, client.Options{Scheme: scheme})
	if e != nil {
		return e
	}
	var backend executor.Client = p
	if os.Args[1] == "install-crash" {
		backend = crashAfterEffect{p}
	}
	c, e := controller.NewCoordinator(k, "fleet-system-private", backend, controller.LocalDigestImageResolver{}, nil, time.Hour)
	if e != nil {
		return e
	}
	var capacity api.MachineCapacity
	var class api.MachineClass
	var profile api.MachineProfile
	for _, item := range []struct {
		key client.ObjectKey
		obj client.Object
	}{{client.ObjectKey{Namespace: "fleet-provider", Name: "central1-baremetal-01"}, &capacity}, {client.ObjectKey{Name: "standard-metal"}, &class}, {client.ObjectKey{Name: "general-purpose-24c-128g"}, &profile}} {
		if e = k.Get(ctx, item.key, item.obj); e != nil {
			return e
		}
	}
	// Explicit operator-owned local inventory; no imported device is adopted.
	capacity.Status.Hardware = api.Hardware{CPU: api.HardwareCPU{Architecture: "amd64", Cores: 2, Threads: 2, Sockets: 1}, Memory: api.HardwareMemory{Capacity: "2Gi"}}
	now := time.Now().UTC()
	for _, name := range []string{"Prepared", "Healthy", "Available"} {
		capacity.Status.Conditions = append(capacity.Status.Conditions, meta.Condition{Type: name, Status: meta.ConditionTrue, ObservedGeneration: capacity.Generation, LastTransitionTime: meta.NewTime(now), Reason: "LocalDisposableDevice", Message: "Operator-created local VM slot; private custody is authoritative."})
	}
	if e = k.Status().Update(ctx, &capacity); e != nil {
		return e
	}
	getClaim := func(ns string) (*api.Machine, *core.Namespace, error) {
		m := &api.Machine{}
		n := &core.Namespace{}
		if e := k.Get(ctx, client.ObjectKey{Namespace: ns, Name: "allocation"}, m); e != nil {
			return nil, nil, e
		}
		if e := k.Get(ctx, client.ObjectKey{Name: ns}, n); e != nil {
			return nil, nil, e
		}
		return m, n, nil
	}
	allocate := func(ns string) (*controller.AssetRecord, error) {
		m, n, e := getClaim(ns)
		if e != nil {
			return nil, e
		}
		auth := controller.AdmissionAuthority{PrincipalUID: "local-operator", DecisionID: "local-" + string(m.UID)}
		if e = c.EnsureClaimCustody(ctx, m, n.UID, auth); e != nil {
			return nil, e
		}
		if len(m.Finalizers) == 0 {
			m.Finalizers = []string{controller.MachineFinalizer}
			if e = k.Update(ctx, m); e != nil {
				return nil, e
			}
		}
		return c.Allocate(ctx, controller.AllocationRequest{Claim: m, Class: &class, Profile: &profile, MachineCapacities: []api.MachineCapacity{capacity}, EvidenceObservedAt: map[types.UID]time.Time{capacity.UID: now}, NamespaceUID: n.UID, Admission: auth, LocalClassName: class.Name, LocalClassUID: class.UID, LocalProfileName: profile.Name, LocalProfileUID: profile.UID})
	}
	phase := os.Args[1]
	switch phase {
	case "allocate":
		r, e := allocate("fleet-project-a")
		if e != nil {
			return e
		}
		if _, e = allocate("fleet-project-b"); !errors.Is(e, controller.ErrInsufficientCapacity) {
			return fmt.Errorf("contention failed: %v", e)
		}
		fmt.Printf("exclusive allocation %s asset %s\n", r.BindingID, r.AssetID)
	case "install", "install-crash", "reinstall":
		m, n, e := getClaim("fleet-project-a")
		if e != nil {
			return e
		}
		name := "debian-12"
		if phase == "reinstall" {
			name += "-reinstall"
		}
		var in api.Installation
		if e = k.Get(ctx, client.ObjectKey{Namespace: m.Namespace, Name: name}, &in); e != nil {
			return e
		}
		m.Status.Phase = "Pending"
		m.Status.Conditions = []meta.Condition{{Type: "Ready", Status: meta.ConditionUnknown, ObservedGeneration: m.Generation, LastTransitionTime: meta.Now(), Reason: "LocalInstallationUnverified", Message: "Installation outcome has not been verified."}}
		if e = k.Status().Update(ctx, m); e != nil {
			return e
		}
		result, e := c.Install(ctx, controller.InstallationRequest{Claim: m, Installation: &in, NamespaceUID: n.UID, Admission: controller.InstallationAdmissionAuthority{PrincipalUID: "local-operator", DecisionID: "local-" + string(in.UID), CatalogAuthority: "local-test-catalog", CatalogRevision: "local-test-v1", ImageDecisionID: "local-test-" + strings.Split(p.ImageReference(), "@")[1]}})
		if e != nil {
			return e
		}
		receipt, found, e := p.Lookup(ctx, result.Record.OperationID)
		if e != nil || !found || receipt.InputDigest != result.RequestDigest {
			return errors.New("exact provider receipt missing")
		}
		in.Status = api.InstallationStatus{Phase: "Ready", CompletedAt: &meta.Time{Time: *receipt.CompletedAt}, Conditions: []meta.Condition{{Type: "Ready", Status: meta.ConditionTrue, ObservedGeneration: in.Generation, LastTransitionTime: meta.Now(), Reason: "LocalDiskBootVerified", Message: "Pinned Linux installer and disk boot verified through local libvirt."}}}
		if e = k.Status().Update(ctx, &in); e != nil {
			return e
		}
		m.Status.CurrentInstallationRef = &api.LocalReference{Name: name}
		m.Status.Phase = "Ready"
		m.Status.Conditions = []meta.Condition{{Type: "Ready", Status: meta.ConditionTrue, ObservedGeneration: m.Generation, LastTransitionTime: meta.Now(), Reason: "LocalDiskBootVerified", Message: "Exact installation-bound disk boot and powered-off handoff verified."}}
		if e = k.Status().Update(ctx, m); e != nil {
			return e
		}
		fmt.Printf("verified installation %s operation %s\n", in.UID, receipt.ID)
	case "release":
		m, _, e := getClaim("fleet-project-a")
		if e != nil {
			return e
		}
		if m.DeletionTimestamp == nil {
			if e = k.Delete(ctx, m, client.Preconditions{UID: &m.UID, ResourceVersion: &m.ResourceVersion}); e != nil {
				return e
			}
		}
		r, e := c.Release(ctx, m.UID)
		if e != nil {
			return e
		}
		if !r.SafeToRemoveFinalizer {
			return errors.New("cleanup not verified")
		}
		current := &api.Machine{}
		if e = k.Get(ctx, client.ObjectKeyFromObject(m), current); e != nil {
			return e
		}
		if current.UID != m.UID {
			return errors.New("consumer machine identity changed during release")
		}
		kept := current.Finalizers[:0]
		for _, f := range current.Finalizers {
			if f != controller.MachineFinalizer {
				kept = append(kept, f)
			}
		}
		current.Finalizers = kept
		if e = k.Update(ctx, current); e != nil {
			return e
		}

		if _, e = allocate("fleet-project-b"); e != nil {
			return e
		}
		fmt.Println("cleanup verified; capacity exclusively reused by second consumer")
	case "evidence":
		var installs api.InstallationList
		if e = k.List(ctx, &installs); e != nil {
			return e
		}
		b, _ := json.MarshalIndent(installs, "", "  ")
		return os.WriteFile(localvm.Root+"/installations.json", b, 0600)
	default:
		return errors.New("unsupported phase")
	}
	return nil
}
