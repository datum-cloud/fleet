package standalone

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
	"go.datum.net/fleet/internal/executor"
)

const (
	PhaseAllocate = "allocate"
	PhaseInstall  = "install"
	PhaseRelease  = "release"
)

type Options struct {
	PrivateNamespace  string
	ProviderNamespace string
	FirstNamespace    string
	SecondNamespace   string
	ClassName         string
	ProfileName       string
	MachineName       string
	ClaimName         string
	InstallationName  string
	ImageName         string
	LocationName      string
	EvidenceAge       time.Duration
}

func DefaultOptions() Options {
	return Options{
		PrivateNamespace: "fleet-system-private", ProviderNamespace: "fleet-provider",
		FirstNamespace: "fleet-project-a", SecondNamespace: "fleet-project-b",
		ClassName: "standard-metal", ProfileName: "general-purpose-24c-128g",
		MachineName: "central1-baremetal-01", ClaimName: "allocation",
		InstallationName: "debian-12", ImageName: "debian-12@sha256:9f4ad8d8b8a6f6fc64d2cf2c7e70b9184b74f089adf8c36d84f4fcb99892f3cb",
		LocationName: "us-central-1", EvidenceAge: 5 * time.Minute,
	}
}

type Runner struct {
	Client   client.Client
	Executor executor.Client
	Options  Options
	Now      func() time.Time
}

type syntheticEvidence struct {
	SchemaVersion    int              `json:"schemaVersion"`
	MachineNamespace string           `json:"machineNamespace"`
	MachineName      string           `json:"machineName"`
	MachineUID       types.UID        `json:"machineUID"`
	Location         string           `json:"location"`
	ObservedAt       time.Time        `json:"observedAt"`
	Hardware         fleetv1.Hardware `json:"hardware"`
}

func (r Runner) Run(ctx context.Context, phase string) error {
	if r.Client == nil || r.Executor == nil {
		return errors.New("Kubernetes and simulator clients are required")
	}
	if r.Now == nil {
		r.Now = time.Now
	}
	if r.Options.EvidenceAge <= 0 {
		return errors.New("positive evidence age is required")
	}
	coordinator, err := controller.NewCoordinator(r.Client, r.Options.PrivateNamespace, r.Executor, controller.LocalDigestImageResolver{}, nil, r.Options.EvidenceAge)
	if err != nil {
		return err
	}
	switch phase {
	case PhaseAllocate:
		return r.allocate(ctx, coordinator)
	case PhaseInstall:
		return r.install(ctx, coordinator)
	case PhaseRelease:
		return r.release(ctx, coordinator)
	default:
		return fmt.Errorf("unsupported smoke phase %q", phase)
	}
}

func (r Runner) allocate(ctx context.Context, coordinator *controller.Coordinator) error {
	class, profile, machine, observedAt, err := r.inputs(ctx, true)
	if err != nil {
		return err
	}
	first, firstNS, firstAdmission, err := r.prepareClaim(ctx, coordinator, r.Options.FirstNamespace)
	if err != nil {
		return err
	}
	second, secondNS, secondAdmission, err := r.prepareClaim(ctx, coordinator, r.Options.SecondNamespace)
	if err != nil {
		return err
	}
	firstRequest := allocationRequest(first, firstNS, firstAdmission, class, profile, machine, observedAt)
	secondRequest := allocationRequest(second, secondNS, secondAdmission, class, profile, machine, observedAt)
	if err := coordinator.CheckCapacity(firstRequest); err != nil {
		return fmt.Errorf("first consumer capacity preflight: %w", err)
	}
	if err := coordinator.CheckCapacity(secondRequest); err != nil {
		return fmt.Errorf("second consumer capacity preflight: %w", err)
	}
	firstRecord, err := coordinator.Allocate(ctx, firstRequest)
	if err != nil {
		return fmt.Errorf("allocate first consumer: %w", err)
	}
	if err := r.projectBoundClaim(ctx, first, firstRecord, "", observedAt); err != nil {
		return err
	}
	// Both contenders use the same retained Available=True evidence. Private
	// custody, not a status projection racing the second request, decides the winner.
	_, err = coordinator.Allocate(ctx, secondRequest)
	if !errors.Is(err, controller.ErrInsufficientCapacity) {
		return fmt.Errorf("second consumer must wait for capacity: %w", err)
	}
	if firstRecord.ClaimUID != first.UID || firstRecord.ClaimNamespace != first.Namespace || firstRecord.ClaimName != first.Name || firstRecord.BindingID == "" {
		return fmt.Errorf("%w: retained exclusive owner does not match the winning claim", controller.ErrRecordInvalid)
	}
	if err := r.projectMachineOccupied(ctx, machine); err != nil {
		return err
	}
	return r.projectPendingClaim(ctx, second, "InsufficientCapacity", "The only synthetic machine is allocated to another consumer.")
}

func (r Runner) install(ctx context.Context, coordinator *controller.Coordinator) error {
	claim, namespace, admission, err := r.claim(ctx, r.Options.FirstNamespace)
	if err != nil {
		return err
	}
	var installation fleetv1.Installation
	if err := r.Client.Get(ctx, client.ObjectKey{Namespace: claim.Namespace, Name: r.Options.InstallationName}, &installation); err != nil {
		return err
	}
	imageDecision := "local-test-" + imageDigest(r.Options.ImageName)
	result, err := coordinator.Install(ctx, controller.InstallationRequest{Claim: claim, Installation: &installation, NamespaceUID: namespace.UID, Admission: controller.InstallationAdmissionAuthority{PrincipalUID: admission.PrincipalUID, DecisionID: "standalone-install-" + string(installation.UID), CatalogAuthority: "local-test-catalog", CatalogRevision: "local-test-v1", ImageDecisionID: imageDecision}})
	if err != nil {
		return fmt.Errorf("install synthetic image: %w", err)
	}
	now := metav1.NewTime(r.Now().UTC())
	installation.Status = fleetv1.InstallationStatus{Phase: "Ready", InstallationRevision: result.Revision, CompletedAt: &now, Conditions: []metav1.Condition{condition("Accepted", metav1.ConditionTrue, installation.Generation, "SyntheticFixtureAccepted", "The standalone fixture admitted this request; this is not Milo authorization."), condition("Executing", metav1.ConditionFalse, installation.Generation, "Completed", "The persistent simulator returned a verified terminal result."), condition("Ready", metav1.ConditionTrue, installation.Generation, "Installed", "The synthetic provider verified installation completion.")}}
	if err := r.Client.Status().Update(ctx, &installation); err != nil {
		return err
	}
	claim.Status.CurrentInstallationRef = &fleetv1.LocalReference{Name: installation.Name}
	claim.Status.InstallationRevision = result.Revision
	apiMeta.SetStatusCondition(&claim.Status.Conditions, condition("Ready", metav1.ConditionTrue, claim.Generation, "SyntheticHandoffReady", "Synthetic installation and powered-off handoff are verified."))
	return r.Client.Status().Update(ctx, claim)
}

func (r Runner) release(ctx context.Context, coordinator *controller.Coordinator) error {
	claim, _, _, err := r.claim(ctx, r.Options.FirstNamespace)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if err == nil {
		claimUID, claimResourceVersion := claim.UID, claim.ResourceVersion
		if claim.DeletionTimestamp == nil {
			if err := r.requestExactDeletion(ctx, claim, claimUID, claimResourceVersion); err != nil {
				return err
			}
		}
		result, releaseErr := coordinator.Release(ctx, claimUID)
		if releaseErr != nil {
			return fmt.Errorf("release first consumer: %w", releaseErr)
		}
		if !result.SafeToRemoveFinalizer {
			return errors.New("cleanup did not authorize finalizer removal")
		}
		if err := r.removeExactFinalizer(ctx, client.ObjectKey{Namespace: r.Options.FirstNamespace, Name: r.Options.ClaimName}, claimUID); err != nil {
			return err
		}
	}
	class, profile, machine, observedAt, err := r.inputs(ctx, false)
	if err != nil {
		return err
	}
	second, secondNS, admission, err := r.claim(ctx, r.Options.SecondNamespace)
	if err != nil {
		return err
	}
	record, err := coordinator.Allocate(ctx, allocationRequest(second, secondNS, admission, class, profile, machine, observedAt))
	if err != nil {
		return fmt.Errorf("allocate waiting consumer: %w", err)
	}
	if record.Epoch < 2 {
		return fmt.Errorf("reused asset epoch = %d, want at least 2", record.Epoch)
	}
	if err := r.projectBoundClaim(ctx, second, record, "", observedAt); err != nil {
		return err
	}
	return r.projectMachineOccupied(ctx, machine)
}

func (r Runner) inputs(ctx context.Context, createEvidence bool) (*fleetv1.MachineClass, *fleetv1.MachineProfile, *fleetv1.Machine, time.Time, error) {
	var class fleetv1.MachineClass
	var profile fleetv1.MachineProfile
	var machine fleetv1.Machine
	if err := r.Client.Get(ctx, client.ObjectKey{Name: r.Options.ClassName}, &class); err != nil {
		return nil, nil, nil, time.Time{}, err
	}
	if err := r.Client.Get(ctx, client.ObjectKey{Name: r.Options.ProfileName}, &profile); err != nil {
		return nil, nil, nil, time.Time{}, err
	}
	if err := r.Client.Get(ctx, client.ObjectKey{Namespace: r.Options.ProviderNamespace, Name: r.Options.MachineName}, &machine); err != nil {
		return nil, nil, nil, time.Time{}, err
	}
	if class.Spec.Capacity == nil || class.Spec.Capacity.Machines.Namespace != r.Options.ProviderNamespace || machine.Namespace != r.Options.ProviderNamespace {
		return nil, nil, nil, time.Time{}, errors.New("synthetic provider scope does not match configuration")
	}
	evidence, err := r.loadEvidence(ctx, &machine, createEvidence)
	if err != nil {
		return nil, nil, nil, time.Time{}, err
	}
	machine.Status.Hardware = evidence.Hardware
	machine.Status.Conditions = []metav1.Condition{condition("Prepared", metav1.ConditionTrue, machine.Generation, "SyntheticEvidence", "Synthetic fixture evidence is current."), condition("Healthy", metav1.ConditionTrue, machine.Generation, "SyntheticEvidence", "Synthetic fixture evidence is current."), condition("Available", metav1.ConditionTrue, machine.Generation, "SyntheticEvidence", "Private custody remains authoritative for exclusivity.")}
	if err := r.Client.Status().Update(ctx, &machine); err != nil {
		return nil, nil, nil, time.Time{}, err
	}
	return &class, &profile, &machine, evidence.ObservedAt, nil
}

func (r Runner) prepareClaim(ctx context.Context, coordinator *controller.Coordinator, namespaceName string) (*fleetv1.MachineClaim, *corev1.Namespace, controller.AdmissionAuthority, error) {
	claim, ns, admission, err := r.claim(ctx, namespaceName)
	if err != nil {
		return nil, nil, admission, err
	}
	if claim.Spec.LocationRef.Name != r.Options.LocationName || claim.Spec.MachineClassRef.Name != r.Options.ClassName || claim.Spec.MachineProfileRef.Name != r.Options.ProfileName || len(claim.Spec.NetworkInterfaces) != 0 || claim.Spec.PowerState != "Off" {
		return nil, nil, admission, fmt.Errorf("%w: claim does not match the configured synthetic fixture", controller.ErrRecordInvalid)
	}
	if err := coordinator.EnsureClaimCustody(ctx, claim, ns.UID, admission); err != nil {
		return nil, nil, admission, err
	}
	if !contains(claim.Finalizers, controller.MachineClaimFinalizer) {
		claim.Finalizers = append(claim.Finalizers, controller.MachineClaimFinalizer)
		if err := r.Client.Update(ctx, claim); err != nil {
			return nil, nil, admission, err
		}
	}
	return claim, ns, admission, nil
}

func (r Runner) claim(ctx context.Context, namespaceName string) (*fleetv1.MachineClaim, *corev1.Namespace, controller.AdmissionAuthority, error) {
	var claim fleetv1.MachineClaim
	if err := r.Client.Get(ctx, client.ObjectKey{Namespace: namespaceName, Name: r.Options.ClaimName}, &claim); err != nil {
		return nil, nil, controller.AdmissionAuthority{}, err
	}
	ns := &corev1.Namespace{}
	if err := r.Client.Get(ctx, client.ObjectKey{Name: namespaceName}, ns); err != nil {
		return nil, nil, controller.AdmissionAuthority{}, err
	}
	a := controller.AdmissionAuthority{PrincipalUID: "standalone-fixture-" + string(ns.UID), DecisionID: "standalone-claim-" + string(claim.UID)}
	return &claim, ns, a, nil
}

func allocationRequest(claim *fleetv1.MachineClaim, ns *corev1.Namespace, admission controller.AdmissionAuthority, class *fleetv1.MachineClass, profile *fleetv1.MachineProfile, machine *fleetv1.Machine, observed time.Time) controller.AllocationRequest {
	return controller.AllocationRequest{Claim: claim, Class: class, Profile: profile, Machines: []fleetv1.Machine{*machine}, EvidenceObservedAt: map[types.UID]time.Time{machine.UID: observed}, NamespaceUID: ns.UID, Admission: admission, LocalClassName: class.Name, LocalClassUID: class.UID, LocalProfileName: profile.Name, LocalProfileUID: profile.UID}
}

func (r Runner) projectBoundClaim(ctx context.Context, claim *fleetv1.MachineClaim, record *controller.AssetRecord, revision string, observedAt time.Time) error {
	observed := metav1.NewTime(observedAt.UTC())
	claim.Status = fleetv1.MachineClaimStatus{Phase: "Ready", Placement: &fleetv1.Placement{LocationRef: fleetv1.LocalReference{Name: r.Options.LocationName}}, AcceptedProfileRef: &fleetv1.LocalReference{Name: r.Options.ProfileName}, AllocatedHardware: &fleetv1.AllocatedHardwareStatus{ObservedAt: &observed, Hardware: record.Hardware}, InstallationRevision: revision, Power: &fleetv1.PowerStatus{State: "Off"}, Conditions: []metav1.Condition{condition("Accepted", metav1.ConditionTrue, claim.Generation, "SyntheticFixtureAccepted", "This standalone fixture does not represent Milo admission."), condition("Bound", metav1.ConditionTrue, claim.Generation, "ExclusiveBinding", "Private custody contains the exclusive binding."), condition("NetworkReady", metav1.ConditionTrue, claim.Generation, "NoAttachmentsRequired", "No network interfaces were requested."), condition("Ready", metav1.ConditionTrue, claim.Generation, "PoweredOffHandoff", "Synthetic powered-off handoff is ready.")}}
	return r.Client.Status().Update(ctx, claim)
}

func (r Runner) projectPendingClaim(ctx context.Context, claim *fleetv1.MachineClaim, reason, message string) error {
	claim.Status = fleetv1.MachineClaimStatus{Phase: "Pending", Placement: &fleetv1.Placement{LocationRef: fleetv1.LocalReference{Name: r.Options.LocationName}}, Conditions: []metav1.Condition{condition("Accepted", metav1.ConditionTrue, claim.Generation, "SyntheticFixtureAccepted", "This standalone fixture does not represent Milo admission."), condition("Bound", metav1.ConditionFalse, claim.Generation, reason, message), condition("Ready", metav1.ConditionFalse, claim.Generation, reason, message)}}
	return r.Client.Status().Update(ctx, claim)
}

func (r Runner) projectMachineOccupied(ctx context.Context, machine *fleetv1.Machine) error {
	apiMeta.SetStatusCondition(&machine.Status.Conditions, condition("Available", metav1.ConditionFalse, machine.Generation, "Allocated", "Private custody records an active allocation."))
	return r.Client.Status().Update(ctx, machine)
}

func (r Runner) removeExactFinalizer(ctx context.Context, key client.ObjectKey, uid types.UID) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var current fleetv1.MachineClaim
		if err := r.Client.Get(ctx, key, &current); apierrors.IsNotFound(err) {
			return nil
		} else if err != nil {
			return err
		}
		if current.UID != uid {
			return fmt.Errorf("%w: claim name was recreated before finalizer removal", controller.ErrRecordInvalid)
		}
		if !contains(current.Finalizers, controller.MachineClaimFinalizer) {
			return nil
		}
		current.Finalizers = remove(current.Finalizers, controller.MachineClaimFinalizer)
		return r.Client.Update(ctx, &current)
	})
}

func (r Runner) requestExactDeletion(ctx context.Context, claim *fleetv1.MachineClaim, uid types.UID, resourceVersion string) error {
	if err := r.Client.Delete(ctx, claim, client.Preconditions{UID: &uid, ResourceVersion: &resourceVersion}); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	var current fleetv1.MachineClaim
	if err := r.Client.Get(ctx, client.ObjectKeyFromObject(claim), &current); apierrors.IsNotFound(err) {
		claim.UID = ""
		return nil
	} else if err != nil {
		return err
	}
	if current.UID != uid {
		return fmt.Errorf("%w: claim name was recreated during release", controller.ErrRecordInvalid)
	}
	*claim = current
	return nil
}

func (r Runner) loadEvidence(ctx context.Context, machine *fleetv1.Machine, create bool) (syntheticEvidence, error) {
	name := evidenceName(machine.UID)
	var secret corev1.Secret
	err := r.Client.Get(ctx, client.ObjectKey{Namespace: r.Options.PrivateNamespace, Name: name}, &secret)
	if apierrors.IsNotFound(err) && create {
		record := syntheticEvidence{
			SchemaVersion: 1, MachineNamespace: machine.Namespace, MachineName: machine.Name,
			MachineUID: machine.UID, Location: r.Options.LocationName, ObservedAt: r.Now().UTC(),
			Hardware: fleetv1.Hardware{CPU: fleetv1.HardwareCPU{Architecture: "amd64", Cores: 24, Threads: 48, Sockets: 2}, Memory: fleetv1.HardwareMemory{Capacity: "128Gi"}},
		}
		data, marshalErr := json.Marshal(record)
		if marshalErr != nil {
			return syntheticEvidence{}, marshalErr
		}
		secret = corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: r.Options.PrivateNamespace, Labels: map[string]string{"fleet.datumapis.com/private-record": "synthetic-evidence"}}, Data: map[string][]byte{"record.json": data}}
		if createErr := r.Client.Create(ctx, &secret); apierrors.IsAlreadyExists(createErr) {
			return r.loadEvidence(ctx, machine, false)
		} else if createErr != nil {
			return syntheticEvidence{}, createErr
		}
		return record, nil
	}
	if err != nil {
		return syntheticEvidence{}, err
	}
	var record syntheticEvidence
	if jsonErr := json.Unmarshal(secret.Data["record.json"], &record); jsonErr != nil {
		return syntheticEvidence{}, fmt.Errorf("decode retained synthetic evidence: %w", jsonErr)
	}
	if record.SchemaVersion != 1 || record.MachineNamespace != machine.Namespace || record.MachineName != machine.Name || record.MachineUID != machine.UID || record.Location != r.Options.LocationName || record.ObservedAt.IsZero() || record.ObservedAt.After(r.Now()) || r.Now().Sub(record.ObservedAt) > r.Options.EvidenceAge || record.Hardware.CPU.Architecture != "amd64" || record.Hardware.CPU.Cores != 24 || record.Hardware.Memory.Capacity != "128Gi" {
		return syntheticEvidence{}, fmt.Errorf("%w: retained synthetic evidence differs from configured fixture", controller.ErrRecordInvalid)
	}
	return record, nil
}

func evidenceName(uid types.UID) string {
	sum := sha256.Sum256([]byte(uid))
	return "fleet-evidence-" + hex.EncodeToString(sum[:8])
}

func condition(t string, s metav1.ConditionStatus, g int64, reason, message string) metav1.Condition {
	return metav1.Condition{Type: t, Status: s, ObservedGeneration: g, LastTransitionTime: metav1.Now(), Reason: reason, Message: message}
}
func imageDigest(value string) string {
	for i := 0; i < len(value); i++ {
		if value[i] == '@' {
			return value[i+1:]
		}
	}
	return ""
}
func contains(v []string, w string) bool {
	for _, x := range v {
		if x == w {
			return true
		}
	}
	return false
}
func remove(v []string, w string) []string {
	out := v[:0]
	for _, x := range v {
		if x != w {
			out = append(out, x)
		}
	}
	return out
}
