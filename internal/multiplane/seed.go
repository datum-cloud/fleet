package multiplane

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	core "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	fleet "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/manager"
	"go.datum.net/fleet/internal/tenancy"
)

// Seed uses bootstrap only for provider facts and publication: the profile,
// class and capacity, and one Device for each enrolled Device. Requests use
// distinct customer credentials, and a watch starts before each create.
// Creating a Device sends nothing and makes no capacity Available: custody
// starts Unverified until the cell reports its fence.
func Seed(ctx context.Context, c Config) error {
	var profile fleet.MachineProfile
	var class fleet.MachineClass
	var capacity fleet.MachineCapacity
	if err := json.Unmarshal([]byte(`{"metadata":{"name":"local-profile"},"spec":{"cpu":{"architecture":"amd64","cores":2},"memory":{"capacity":"2Gi"}}}`), &profile); err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(`{"metadata":{"name":"local-class"},"spec":{"profilePolicy":{"allowedProfiles":[{"name":"local-profile"}],"customProfiles":{"enabled":false}},"capacity":{"machineCapacities":{"namespace":"fleet-provider","selector":{"matchLabels":{"pool":"local"}}}}}}`), &class); err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(`{"metadata":{"name":"local-capacity","namespace":"fleet-provider","labels":{"pool":"local"}},"spec":{"deviceRef":{"name":"`+Device+`"},"allocationPolicy":"Enabled","cleaningPolicyRef":{"name":"local-cleaning"},"reclaimPolicy":"Retain"}}`), &capacity); err != nil {
		return err
	}
	provider, err := API(filepath.Join(c.State, "provider", "bootstrap-project.json"))
	if err != nil {
		return err
	}
	devices, err := enrolledDevices(c)
	if err != nil {
		return err
	}
	for _, obj := range append([]client.Object{&profile, &class, &capacity}, devices...) {
		if err := ensure(ctx, provider, obj); err != nil {
			return err
		}
	}
	class.Status.EvaluatedRevision = "local-fixture-v1"
	if err := provider.Status().Update(ctx, &class); err != nil {
		return err
	}
	// The capacity projector is not wired, so the fixture writes Prepared,
	// Healthy and Available. Custody decides which Machine gets the Device.
	capacity.Status.Hardware = fleet.Hardware{CPU: fleet.HardwareCPU{Architecture: "amd64", Cores: 2}, Memory: fleet.HardwareMemory{Capacity: "2Gi"}}
	for _, kind := range []string{"Available", "Prepared", "Healthy"} {
		capacity.Status.Conditions = append(capacity.Status.Conditions, condition(kind, metav1.ConditionTrue, capacity.Generation, "SyntheticHardwareObservation"))
	}
	if err := provider.Status().Update(ctx, &capacity); err != nil {
		return err
	}
	var routing []map[string]string
	for _, name := range []string{"consumer-a", "consumer-b"} {
		api, err := API(filepath.Join(c.State, name, "bootstrap-project.json"))
		if err != nil {
			return err
		}
		cp := profile.DeepCopyObject().(*fleet.MachineProfile)
		cp.ObjectMeta = metav1.ObjectMeta{Name: profile.Name}
		cc := &fleet.MachineClass{ObjectMeta: metav1.ObjectMeta{Name: class.Name}, Spec: fleet.MachineClassSpec{Source: &fleet.SourceReference{PublisherRef: tenancy.Project("provider"), Name: class.Name}}}
		if err := ensure(ctx, api, cp); err != nil {
			return err
		}
		if err := ensure(ctx, api, cc); err != nil {
			return err
		}
		customer, err := clientcmd.BuildConfigFromFlags("", filepath.Join(c.State, name, "customer-project.json"))
		if err != nil {
			return err
		}
		d, err := dynamic.NewForConfig(customer)
		if err != nil {
			return err
		}
		gvr := fleet.GroupVersion.WithResource("machines")
		seconds := int64(30)
		w, err := d.Resource(gvr).Namespace(Namespace).Watch(ctx, metav1.ListOptions{TimeoutSeconds: &seconds})
		if err != nil {
			return err
		}
		user, err := API(filepath.Join(c.State, name, "customer-project.json"))
		if err != nil {
			w.Stop()
			return err
		}
		machine := &fleet.Machine{ObjectMeta: metav1.ObjectMeta{Name: "allocation", Namespace: Namespace}, Spec: fleet.MachineSpec{MachineClassRef: fleet.LocalReference{Name: class.Name}, MachineProfileRef: fleet.LocalReference{Name: profile.Name}, LocationRef: fleet.LocalReference{Name: Location}, PowerState: "Off"}}
		if err := ensure(ctx, user, machine); err != nil {
			w.Stop()
			return err
		}
		select {
		case event, ok := <-w.ResultChan():
			observed, typed := event.Object.(metav1.Object)
			if !ok || event.Type != "ADDED" || !typed || observed.GetUID() != machine.UID || observed.GetName() != machine.Name || observed.GetNamespace() != Namespace {
				w.Stop()
				return errors.New("Milo create watch did not deliver Added")
			}
		case <-ctx.Done():
			w.Stop()
			return ctx.Err()
		}
		w.Stop()
		installation := &fleet.Installation{ObjectMeta: metav1.ObjectMeta{Name: "debian-pxe", Namespace: Namespace}, Spec: fleet.InstallationSpec{MachineRef: fleet.MachineReference{Name: machine.Name}, ExpectedPredecessor: fleet.InstallationPredecessor{Type: "None"}, ImageRef: fleet.LocalReference{Name: c.Image}}}
		if err := ensure(ctx, user, installation); err != nil {
			return err
		}
		var ns core.Namespace
		if err := api.Get(ctx, client.ObjectKey{Name: Namespace}, &ns); err != nil {
			return err
		}
		root, err := API(filepath.Join(c.State, name, "management-root.json"))
		if err != nil {
			return err
		}
		identity, err := manager.ResolveProjectIdentity(ctx, root, "local-milo", name)
		if err != nil {
			return err
		}
		routing = append(routing, map[string]string{"route": name, "namespace": Namespace, "namespaceUID": string(ns.UID), "projectUID": string(identity.UID), "machineUID": string(machine.UID), "watch": "Added exact UID"})
	}
	evidence, err := json.MarshalIndent(routing, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(c.State, "routing.json"), evidence, 0600)
}

// enrolledDevices is one synthetic Device for each Device the cell enrolls,
// with the identity the enrollment records.
func enrolledDevices(c Config) ([]client.Object, error) {
	keys := make([]string, 0, len(c.Enrollment))
	for key := range c.Enrollment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var out []client.Object
	for _, key := range keys {
		namespace, name, _ := strings.Cut(key, "/")
		if namespace != DeviceNamespace {
			return nil, errors.New("an enrolled Device is outside the provider's Device namespace")
		}
		target := c.Enrollment[key]
		out = append(out, &inventory.Device{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}, Spec: inventory.DeviceSpec{
			ClaimRef:   inventory.LocalReference{Name: "claim-" + name},
			Identity:   inventory.DeviceIdentity{Model: "synthetic", Serial: "serial-" + name, SystemUUID: target.SystemUUID},
			BMC:        &inventory.BMC{Endpoint: target.BMCEndpoint},
			Interfaces: []inventory.Interface{{Name: "primary", MACAddress: target.BootMACAddress}},
		}})
	}
	if len(out) == 0 {
		return nil, errors.New("the cell enrolls no Device")
	}
	return out, nil
}
