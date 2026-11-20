// Package multiplane is an explicit local acceptance composition of the shared
// lifecycle, real Milo project APIs, and the Karmada executor transport.
// Admission, quota, catalog publication and hardware evidence are synthetic.
package multiplane

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	fleet "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
	"go.datum.net/fleet/internal/coordination"
	"go.datum.net/fleet/internal/executor"
	"go.datum.net/fleet/internal/manager"
	rm "go.miloapis.com/milo/pkg/apis/resourcemanager/v1alpha1"
	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const Namespace = "default"
const PrivateNamespace = "fleet-system-private"

// Location is the only location this acceptance composition serves. It is bound
// to exactly one Karmada member, Config.Cell, and to the one local capacity
// fixture. There is no location catalog: any other Machine locationRef is
// rejected before claim custody, authority records, finalizers, or allocation,
// so a request for elsewhere can never reserve or effect local hardware.
const Location = "local"

var ErrLocationNotServed = errors.New("machine location is not served by the configured cell")

type Config struct{ State, HubKubeconfig, PrivateKubeconfig, Cell, Image string }
type Project struct {
	API      client.Client
	Identity controller.ProjectIdentity
}
type Runner struct {
	Config      Config
	Provider    Project
	Consumers   [2]Project
	Private     client.Client
	Hub         *coordination.Hub
	Coordinator *controller.Coordinator
}

func Scheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = core.AddToScheme(s)
	_ = fleet.AddToScheme(s)
	_ = rm.AddToScheme(s)
	return s
}
func API(path string) (client.Client, error) {
	c, err := clientcmd.BuildConfigFromFlags("", path)
	if err != nil {
		return nil, err
	}
	c.Timeout = 30 * time.Second
	return client.New(c, client.Options{Scheme: Scheme()})
}
func New(ctx context.Context, c Config) (*Runner, error) {
	r := &Runner{Config: c}
	var err error
	r.Private, err = API(c.PrivateKubeconfig)
	if err != nil {
		return nil, err
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", c.HubKubeconfig)
	if err != nil {
		return nil, err
	}
	cfg.Timeout = 30 * time.Second
	hub, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	r.Hub = &coordination.Hub{API: hub, Namespace: "fleet-coordination", Cell: c.Cell, MaxAge: time.Minute}
	r.Coordinator, err = controller.NewCoordinator(r.Private, PrivateNamespace, r.Hub, controller.LocalDigestImageResolver{}, nil, time.Hour)
	if err != nil {
		return nil, err
	}
	for index, name := range []string{"provider", "consumer-a", "consumer-b"} {
		root, err := API(filepath.Join(c.State, name, "management-root.json"))
		if err != nil {
			return nil, err
		}
		identity, err := manager.ResolveProjectIdentity(ctx, root, "local-milo", name)
		if err != nil {
			return nil, err
		}
		project, err := API(filepath.Join(c.State, name, "management-project.json"))
		if err != nil {
			return nil, err
		}
		p := Project{API: project, Identity: identity}
		if index == 0 {
			r.Provider = p
		} else {
			r.Consumers[index-1] = p
		}
	}
	return r, nil
}
func (r *Runner) ScopedAsset() string {
	return r.Provider.Identity.AuthorityID + "/" + string(r.Provider.Identity.UID) + "/fleet-provider/fleet.datumapis.com/Device/synthetic-device-central-01"
}
func condition(kind string, status metav1.ConditionStatus, generation int64, reason string) metav1.Condition {
	return metav1.Condition{Type: kind, Status: status, ObservedGeneration: generation, Reason: reason, Message: "Local multi-plane acceptance; synthetic IAM, quota, catalog and networkless fixtures", LastTransitionTime: metav1.Now()}
}
func ensure(ctx context.Context, c client.Client, obj client.Object) error {
	before, _ := json.Marshal(obj)
	var want map[string]interface{}
	_ = json.Unmarshal(before, &want)
	err := c.Create(ctx, obj)
	if !apierrors.IsAlreadyExists(err) {
		return err
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(obj), obj); err != nil {
		return err
	}
	after, _ := json.Marshal(obj)
	var got map[string]interface{}
	_ = json.Unmarshal(after, &got)
	w, _ := json.Marshal(want["spec"])
	g, _ := json.Marshal(got["spec"])
	if string(w) != string(g) || obj.GetDeletionTimestamp() != nil {
		return errors.New("existing fixture differs; refusing replacement")
	}
	return nil
}

// Seed uses bootstrap only for provider facts/publication; requests use distinct
// customer credentials and a watch starts before each create.
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
	if err := json.Unmarshal([]byte(`{"metadata":{"name":"local-capacity","namespace":"fleet-provider","labels":{"pool":"local"}},"spec":{"deviceRef":{"name":"synthetic-device-central-01"},"allocationPolicy":"Enabled","cleaningPolicyRef":{"name":"local-cleaning"},"reclaimPolicy":"Retain"}}`), &capacity); err != nil {
		return err
	}
	provider, err := API(filepath.Join(c.State, "provider", "bootstrap-project.json"))
	if err != nil {
		return err
	}
	for _, obj := range []client.Object{&profile, &class, &capacity} {
		if err := ensure(ctx, provider, obj); err != nil {
			return err
		}
	}
	class.Status.EvaluatedRevision = "local-fixture-v1"
	if err := provider.Status().Update(ctx, &class); err != nil {
		return err
	}
	capacity.Status.Hardware = fleet.Hardware{CPU: fleet.HardwareCPU{Architecture: "amd64", Cores: 2}, Memory: fleet.HardwareMemory{Capacity: "2Gi"}}
	for _, kind := range []string{"Available", "Verified", "Prepared", "Healthy"} {
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
		cc := &fleet.MachineClass{ObjectMeta: metav1.ObjectMeta{Name: class.Name}, Spec: fleet.MachineClassSpec{Source: &fleet.SourceReference{Project: "provider", Name: class.Name}}}
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
func (r *Runner) request(ctx context.Context, index int) (controller.AllocationRequest, error) {
	p := r.Consumers[index]
	var m fleet.Machine
	var ns core.Namespace
	var class fleet.MachineClass
	var profile fleet.MachineProfile
	var capacity fleet.MachineCapacity
	var localClass fleet.MachineClass
	var localProfile fleet.MachineProfile
	objects := []struct {
		api client.Client
		key client.ObjectKey
		obj client.Object
	}{{p.API, client.ObjectKey{Namespace: Namespace, Name: "allocation"}, &m}, {p.API, client.ObjectKey{Name: Namespace}, &ns}, {r.Provider.API, client.ObjectKey{Name: "local-class"}, &class}, {r.Provider.API, client.ObjectKey{Name: "local-profile"}, &profile}, {r.Provider.API, client.ObjectKey{Namespace: "fleet-provider", Name: "local-capacity"}, &capacity}, {p.API, client.ObjectKey{Name: "local-class"}, &localClass}, {p.API, client.ObjectKey{Name: "local-profile"}, &localProfile}}
	for _, o := range objects {
		if err := o.api.Get(ctx, o.key, o.obj); err != nil {
			return controller.AllocationRequest{}, err
		}
	}
	if m.Spec.LocationRef.Name != Location || r.Config.Cell == "" || r.Hub != nil && r.Hub.Cell != r.Config.Cell {
		return controller.AllocationRequest{}, fmt.Errorf("%w: %q", ErrLocationNotServed, m.Spec.LocationRef.Name)
	}
	if localClass.Spec.Source == nil || localClass.Spec.Source.Project != "provider" || localClass.Spec.Source.Name != class.Name {
		return controller.AllocationRequest{}, errors.New("publication source mismatch")
	}
	observed := meta.FindStatusCondition(capacity.Status.Conditions, "Verified")
	if observed == nil {
		return controller.AllocationRequest{}, errors.New("missing hardware fixture evidence")
	}
	admission := controller.AdmissionAuthority{PrincipalUID: "local-fixture:" + string(p.Identity.UID), DecisionID: "local-fixture:" + string(m.UID)}
	if err := r.Coordinator.EnsureClaimCustody(ctx, &m, ns.UID, admission, p.Identity); err != nil {
		return controller.AllocationRequest{}, err
	}
	// Retain a concrete synthetic contract record, not just an invented reference.
	contract, _ := json.Marshal(struct {
		Consumer, Provider              controller.ProjectIdentity
		Claim, Capacity, Class, Profile types.UID
	}{p.Identity, r.Provider.Identity, m.UID, capacity.UID, class.UID, profile.UID})
	sum := sha256.Sum256(contract)
	digest := hex.EncodeToString(sum[:])
	name := "mp-authority-" + digest[:24]
	secret := &core.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: PrivateNamespace}, Data: map[string][]byte{"contract.json": contract}}
	if err := r.Private.Create(ctx, secret); apierrors.IsAlreadyExists(err) {
		var existing core.Secret
		if err := r.Private.Get(ctx, client.ObjectKeyFromObject(secret), &existing); err != nil {
			return controller.AllocationRequest{}, err
		}
		if string(existing.Data["contract.json"]) != string(contract) {
			return controller.AllocationRequest{}, errors.New("retained synthetic authority conflict")
		}
		secret = &existing
	} else if err != nil {
		return controller.AllocationRequest{}, err
	}
	authority := controller.AuthorityRef{Provider: "local-fixture", RecordName: name, RecordID: string(secret.UID), ContractDigest: digest}
	hold := controller.ExternalHoldRef{Provider: "local-fixture", Namespace: PrivateNamespace, Name: name, UID: secret.UID}
	if err := r.Coordinator.RetainClaimAuthority(ctx, m.UID, p.Identity, r.Provider.Identity, authority); err != nil {
		return controller.AllocationRequest{}, err
	}
	if err := r.Coordinator.RetainAllocationAuthority(ctx, m.UID, p.Identity, r.Provider.Identity, authority, hold); err != nil {
		return controller.AllocationRequest{}, err
	}
	hasFinalizer := false
	for _, f := range m.Finalizers {
		if f == controller.MachineFinalizer {
			hasFinalizer = true
		}
	}
	released, err := r.Coordinator.ClaimReleased(ctx, p.Identity, m.UID)
	if err != nil {
		return controller.AllocationRequest{}, err
	}
	if !hasFinalizer && !released {
		m.Finalizers = append(m.Finalizers, controller.MachineFinalizer)
		if err := p.API.Update(ctx, &m); err != nil {
			return controller.AllocationRequest{}, err
		}
	}
	return controller.AllocationRequest{Claim: &m, Class: &class, Profile: &profile, MachineCapacities: []fleet.MachineCapacity{capacity}, EvidenceObservedAt: map[types.UID]time.Time{capacity.UID: observed.LastTransitionTime.Time}, NamespaceUID: ns.UID, Admission: admission, LocalClassName: localClass.Name, LocalClassUID: localClass.UID, LocalProfileName: localProfile.Name, LocalProfileUID: localProfile.UID, ConsumerProject: p.Identity, ProviderProject: r.Provider.Identity, Authority: authority, ExternalHold: hold}, nil
}
func (r *Runner) Allocate(ctx context.Context) error {
	a, err := r.request(ctx, 0)
	if err != nil {
		return err
	}
	b, err := r.request(ctx, 1)
	if err != nil {
		return err
	}
	if a.NamespaceUID == b.NamespaceUID || a.ConsumerProject.SameLifetime(b.ConsumerProject) {
		return errors.New("consumer control planes do not have distinct identities")
	}
	if _, err = r.Coordinator.Allocate(ctx, a); err != nil {
		return err
	}
	if _, err = r.Coordinator.Allocate(ctx, b); !errors.Is(err, controller.ErrInsufficientCapacity) {
		return fmt.Errorf("contention must retain one winner: %v", err)
	}
	for index, request := range []controller.AllocationRequest{a, b} {
		m := request.Claim
		phase, reason := "Bound", "AwaitingVerifiedInstallation"
		status := metav1.ConditionTrue
		if index == 1 {
			phase, reason, status = "Pending", "InsufficientCapacity", metav1.ConditionFalse
		}
		m.Status.Phase = phase
		m.Status.Conditions = []metav1.Condition{condition("Bound", status, m.Generation, reason), condition("Ready", metav1.ConditionFalse, m.Generation, reason)}
		if err := r.Consumers[index].API.Status().Update(ctx, m); err != nil {
			return err
		}
	}
	return nil
}
func (r *Runner) Install(ctx context.Context) error { return r.InstallIndex(ctx, 0) }

// InstallSecond installs consumer B's Installation after it reused the
// capacity, through the same lifecycle and federation path.
func (r *Runner) InstallSecond(ctx context.Context) error { return r.InstallIndex(ctx, 1) }

func (r *Runner) InstallIndex(ctx context.Context, index int) error {
	request, err := r.request(ctx, index)
	if err != nil {
		return err
	}
	var install fleet.Installation
	if err := r.Consumers[index].API.Get(ctx, client.ObjectKey{Namespace: Namespace, Name: "debian-pxe"}, &install); err != nil {
		return err
	}
	result, err := r.Coordinator.Install(ctx, controller.InstallationRequest{Claim: request.Claim, Installation: &install, NamespaceUID: request.NamespaceUID, ConsumerProject: request.ConsumerProject, Admission: controller.InstallationAdmissionAuthority{PrincipalUID: request.Admission.PrincipalUID, DecisionID: "local-install:" + string(install.UID), CatalogAuthority: "local-test-catalog", CatalogRevision: "local-test-v1", ImageDecisionID: "local-test-" + imageDigest(install.Spec.ImageRef.Name)}})
	if err != nil {
		return err
	}
	verified, found, err := r.Hub.Lookup(ctx, result.Record.OperationID)
	if err != nil || !found || verified.InputDigest != result.RequestDigest {
		return executor.ErrOutcomeUnknown
	}
	install.Status.Phase = "Ready"
	install.Status.Conditions = []metav1.Condition{condition("Ready", metav1.ConditionTrue, install.Generation, "LocalPXEVerifiedViaKarmada")}
	if err := r.Consumers[index].API.Status().Update(ctx, &install); err != nil {
		return err
	}
	m := request.Claim
	m.Status.CurrentInstallationRef = &fleet.LocalReference{Name: install.Name}
	m.Status.Phase = "Ready"
	meta.SetStatusCondition(&m.Status.Conditions, condition("Ready", metav1.ConditionTrue, m.Generation, "LocalPXEVerifiedViaKarmada"))
	return r.Consumers[index].API.Status().Update(ctx, m)
}
func (r *Runner) Release(ctx context.Context) error       { return r.ReleaseIndex(ctx, 0) }
func (r *Runner) ReleaseSecond(ctx context.Context) error { return r.ReleaseIndex(ctx, 1) }
func (r *Runner) ReleaseIndex(ctx context.Context, index int) error {
	request, err := r.request(ctx, index)
	if err != nil {
		return err
	}
	result, err := r.Coordinator.ReleaseScoped(ctx, request.ConsumerProject, request.Claim.UID)
	if err != nil {
		return err
	}
	if result.ExternalSettlementRequired {
		result, err = r.Coordinator.CompleteExternalRelease(ctx, request.Claim.UID, request.Authority, request.ExternalHold, request.ConsumerProject)
		if err != nil {
			return err
		}
	}
	if !result.SafeToRemoveFinalizer {
		return executor.ErrOutcomeUnknown
	}
	return publishRelease(ctx, r.Consumers[index].API, request.Claim)
}

// publishRelease runs only after verified cleanup made removal safe. Status is
// written first because a status response overwrites the in-memory object,
// including metadata, from the server; the finalizer is removed afterwards
// from that fresh copy so the removal is not silently reverted.
func publishRelease(ctx context.Context, api client.Client, m *fleet.Machine) error {
	// Retire the current installation before clearing the reference, so retries
	// can finish projection if either API write fails after cleanup.
	if ref := m.Status.CurrentInstallationRef; ref != nil {
		var install fleet.Installation
		err := api.Get(ctx, client.ObjectKey{Namespace: m.Namespace, Name: ref.Name}, &install)
		if err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		if err == nil {
			install.Status.Phase = "Released"
			meta.SetStatusCondition(&install.Status.Conditions, condition("Ready", metav1.ConditionFalse, install.Generation, "Released"))
			if err := api.Status().Update(ctx, &install); err != nil {
				return err
			}
		}
	}
	m.Status.CurrentInstallationRef = nil
	m.Status.Phase = "Released"
	meta.SetStatusCondition(&m.Status.Conditions, condition("Bound", metav1.ConditionFalse, m.Generation, "Released"))
	meta.SetStatusCondition(&m.Status.Conditions, condition("Ready", metav1.ConditionFalse, m.Generation, "Released"))
	if err := api.Status().Update(ctx, m); err != nil {
		return err
	}
	retainedFinalizers := make([]string, 0, len(m.Finalizers))
	for _, f := range m.Finalizers {
		if f != controller.MachineFinalizer {
			retainedFinalizers = append(retainedFinalizers, f)
		}
	}
	m.Finalizers = retainedFinalizers
	return api.Update(ctx, m)
}
func (r *Runner) Reuse(ctx context.Context) error {
	b, err := r.request(ctx, 1)
	if err != nil {
		return err
	}
	if _, err = r.Coordinator.Allocate(ctx, b); err != nil {
		return err
	}
	// Public status follows only the coordinator's committed reuse binding;
	// readiness still requires a verified installation for this allocation.
	m := b.Claim
	m.Status.Phase = "Bound"
	meta.SetStatusCondition(&m.Status.Conditions, condition("Bound", metav1.ConditionTrue, m.Generation, "AwaitingVerifiedInstallation"))
	meta.SetStatusCondition(&m.Status.Conditions, condition("Ready", metav1.ConditionFalse, m.Generation, "AwaitingVerifiedInstallation"))
	return r.Consumers[1].API.Status().Update(ctx, m)
}
func ReadConfig(path string) (Config, error) {
	var c Config
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	err = json.Unmarshal(b, &c)
	return c, err
}

func imageDigest(value string) string { _, digest, _ := strings.Cut(value, "@"); return digest }
