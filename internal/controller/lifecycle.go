package controller

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/executor"
	fleetnetwork "go.datum.net/fleet/internal/network"
	"go.datum.net/fleet/internal/simulator"
)

const (
	MachineClaimFinalizer = "fleet.datumapis.com/machineclaim-protection"
	recordSchemaVersion   = 1
	assetRecordPrefix     = "fleet-asset-"
	custodyRecordPrefix   = "fleet-claim-"
	operationPrefix       = "fleet-install-"
	recordDataKey         = "record.json"
	recordTypeLabel       = "fleet.datumapis.com/private-record"
	claimUIDLabel         = "fleet.datumapis.com/claim-uid"
)

type CustodyState string

const (
	CustodyUnbound   CustodyState = "Unbound"
	CustodySelecting CustodyState = "Selecting"
	CustodyBound     CustodyState = "Bound"
	CustodyReleased  CustodyState = "Released"
)

// ClaimCustodyRecord is independent proof of whether a claim lifetime ever
// acquired an asset. A missing asset record is safe only while this record is
// Unbound; Bound custody without matching asset authority fails closed.
type ClaimCustodyRecord struct {
	SchemaVersion  int                `json:"schemaVersion"`
	State          CustodyState       `json:"state"`
	ClaimNamespace string             `json:"claimNamespace"`
	ClaimName      string             `json:"claimName"`
	ClaimUID       types.UID          `json:"claimUID"`
	NamespaceUID   types.UID          `json:"namespaceUID"`
	Admission      AdmissionAuthority `json:"admission"`
	AssetID        string             `json:"assetID,omitempty"`
	ProposedAsset  *AssetRecord       `json:"proposedAsset,omitempty"`
}

type AdmissionAuthority struct {
	PrincipalUID string `json:"principalUID"`
	DecisionID   string `json:"decisionID"`
}

func (a AdmissionAuthority) valid() bool {
	return strings.TrimSpace(a.PrincipalUID) != "" && strings.TrimSpace(a.DecisionID) != ""
}

var (
	ErrUnsupported          = errors.New("requested integration is unavailable")
	ErrInsufficientCapacity = errors.New("no eligible clean machine is available")
	ErrRecordInvalid        = errors.New("retained private lifecycle record is invalid")
	ErrClaimAlreadyReleased = errors.New("claim allocation was already released")
	ErrPredecessorConflict  = errors.New("installation predecessor was already consumed")
	ErrOperationActive      = errors.New("an admitted installation operation is not settled")
)

type AssetState string

const (
	AssetPreparing AssetState = "Preparing"
	AssetCommitted AssetState = "Committed"
	AssetReleasing AssetState = "Releasing"
	AssetReleased  AssetState = "Released"
)

type OperationState string

const (
	OperationAdmitted  OperationState = "Admitted"
	OperationSucceeded OperationState = "Succeeded"
)

// AssetRecord is private coordination state. It is deliberately retained after
// release so the next allocation can advance the executor epoch without
// reconstructing authority from public status.
type AssetRecord struct {
	SchemaVersion        int                        `json:"schemaVersion"`
	State                AssetState                 `json:"state"`
	AssetID              string                     `json:"assetID"`
	MachineNamespace     string                     `json:"machineNamespace"`
	MachineName          string                     `json:"machineName"`
	MachineUID           types.UID                  `json:"machineUID"`
	ClaimNamespace       string                     `json:"claimNamespace"`
	ClaimName            string                     `json:"claimName"`
	ClaimUID             types.UID                  `json:"claimUID"`
	ClassUID             types.UID                  `json:"classUID"`
	ProfileUID           types.UID                  `json:"profileUID"`
	BindingID            string                     `json:"bindingID"`
	Epoch                int64                      `json:"epoch"`
	FenceToken           string                     `json:"fenceToken"`
	CurrentRevision      string                     `json:"currentRevision"`
	ActiveOperationID    string                     `json:"activeOperationID,omitempty"`
	AcceptedRequirements fleetv1.MachineProfileSpec `json:"acceptedRequirements"`
	Hardware             fleetv1.Hardware           `json:"hardware"`
	Network              *NetworkRecord             `json:"network,omitempty"`
}

type ResolvedImage struct {
	Name             string    `json:"name"`
	UID              types.UID `json:"uid,omitempty"`
	Digest           string    `json:"digest"`
	CatalogAuthority string    `json:"catalogAuthority"`
	CatalogRevision  string    `json:"catalogRevision"`
	ImageDecisionID  string    `json:"imageDecisionID"`
}

func (i ResolvedImage) Reference() string { return i.Name + "@" + i.Digest }

func (i ResolvedImage) Validate() error {
	if strings.TrimSpace(i.Name) == "" || strings.Contains(i.Name, "@") {
		return errors.New("resolved image name must be nonempty and exclude a digest")
	}
	if !validSHA256(i.Digest) {
		return errors.New("resolved image digest must be canonical sha256 with 64 lowercase hex digits")
	}
	if strings.TrimSpace(i.CatalogAuthority) == "" || strings.TrimSpace(i.CatalogRevision) == "" || strings.TrimSpace(i.ImageDecisionID) == "" {
		return errors.New("resolved image authority is required")
	}
	return nil
}

type ImageRequest struct {
	NamespaceUID    types.UID
	ClaimUID        types.UID
	InstallationUID types.UID
	ImageName       string
}

type ImageResolver interface {
	ResolveImage(context.Context, ImageRequest) (ResolvedImage, error)
}

// LocalDigestImageResolver is test-only. Production must resolve image
// authority through an authenticated catalog and retain its lifetime identity.
type LocalDigestImageResolver struct{}

func (LocalDigestImageResolver) ResolveImage(_ context.Context, request ImageRequest) (ResolvedImage, error) {
	name, digest, ok := strings.Cut(request.ImageName, "@")
	image := ResolvedImage{Name: name, Digest: digest, CatalogAuthority: "local-test-catalog", CatalogRevision: "local-test-v1", ImageDecisionID: "local-test-" + digest}
	if !ok || image.Validate() != nil {
		return ResolvedImage{}, errors.New("local digest resolver requires name@sha256:<64 lowercase hex digits>")
	}
	return image, nil
}

type InstallationRecord struct {
	SchemaVersion   int                            `json:"schemaVersion"`
	State           OperationState                 `json:"state"`
	OperationID     string                         `json:"operationID"`
	InstallationUID types.UID                      `json:"installationUID"`
	NamespaceUID    types.UID                      `json:"namespaceUID"`
	ClaimUID        types.UID                      `json:"claimUID"`
	AssetID         string                         `json:"assetID"`
	BindingID       string                         `json:"bindingID"`
	Epoch           int64                          `json:"epoch"`
	Predecessor     string                         `json:"predecessor"`
	ResultRevision  string                         `json:"resultRevision"`
	Image           ResolvedImage                  `json:"image"`
	ImageName       string                         `json:"imageName"`
	Admission       InstallationAdmissionAuthority `json:"admission"`
	NetworkInputs   *fleetnetwork.Observation      `json:"networkInputs,omitempty"`
}

type InstallationAdmissionAuthority struct {
	PrincipalUID     string `json:"principalUID"`
	DecisionID       string `json:"decisionID"`
	CatalogAuthority string `json:"catalogAuthority"`
	CatalogRevision  string `json:"catalogRevision"`
	ImageDecisionID  string `json:"imageDecisionID"`
}

func (a InstallationAdmissionAuthority) valid() bool {
	return strings.TrimSpace(a.PrincipalUID) != "" && strings.TrimSpace(a.DecisionID) != "" && strings.TrimSpace(a.CatalogAuthority) != "" && strings.TrimSpace(a.CatalogRevision) != "" && strings.TrimSpace(a.ImageDecisionID) != ""
}

type Coordinator struct {
	client         client.Client
	namespace      string
	executor       executor.Client
	images         ImageResolver
	networks       fleetnetwork.Lifecycle
	maxEvidenceAge time.Duration
	now            func() time.Time
	newID          func() (string, error)
}

func NewCoordinator(kube client.Client, privateNamespace string, provider executor.Client, images ImageResolver, networks fleetnetwork.Lifecycle, maxEvidenceAge time.Duration) (*Coordinator, error) {
	if kube == nil || provider == nil || images == nil || privateNamespace == "" || maxEvidenceAge <= 0 {
		return nil, errors.New("client, private namespace, executor, image resolver, and positive evidence age are required")
	}
	return &Coordinator{client: kube, namespace: privateNamespace, executor: provider, images: images, networks: networks, maxEvidenceAge: maxEvidenceAge, now: time.Now, newID: randomID}, nil
}

type AllocationRequest struct {
	Claim              *fleetv1.MachineClaim
	Class              *fleetv1.MachineClass
	Profile            *fleetv1.MachineProfile
	Machines           []fleetv1.Machine
	EvidenceObservedAt map[types.UID]time.Time
	NamespaceUID       types.UID
	Admission          AdmissionAuthority
	Location           fleetnetwork.LocationAuthority
	networkPlan        *fleetnetwork.Plan
}

// EnsureClaimCustody must be called before a claim finalizer is installed. It
// is idempotent for the exact claim lifetime and rejects deterministic-name
// squatting or reuse with a different identity.
func (c *Coordinator) EnsureClaimCustody(ctx context.Context, claim *fleetv1.MachineClaim, namespaceUID types.UID, admission AdmissionAuthority) error {
	if claim == nil || claim.Namespace == "" || claim.Name == "" || claim.UID == "" || namespaceUID == "" || !admission.valid() {
		return errors.New("claim lifetime identity is required")
	}
	name := custodyRecordName(claim.UID)
	var secret corev1.Secret
	err := c.client.Get(ctx, client.ObjectKey{Namespace: c.namespace, Name: name}, &secret)
	if err == nil {
		record, err := decodeCustodySecret(&secret)
		if err != nil {
			return err
		}
		if record.ClaimNamespace != claim.Namespace || record.ClaimName != claim.Name || record.ClaimUID != claim.UID || record.NamespaceUID != namespaceUID || record.Admission != admission {
			return fmt.Errorf("%w: custody lifetime does not match claim", ErrRecordInvalid)
		}
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	record := &ClaimCustodyRecord{SchemaVersion: recordSchemaVersion, State: CustodyUnbound, ClaimNamespace: claim.Namespace, ClaimName: claim.Name, ClaimUID: claim.UID, NamespaceUID: namespaceUID, Admission: admission}
	secret = corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: c.namespace}}
	if err := encodeRecord(&secret, record, "custody"); err != nil {
		return err
	}
	if err := c.client.Create(ctx, &secret); apierrors.IsAlreadyExists(err) {
		return c.EnsureClaimCustody(ctx, claim, namespaceUID, admission)
	} else {
		return err
	}
}

func (c *Coordinator) Allocate(ctx context.Context, request AllocationRequest) (*AssetRecord, error) {
	if err := validateAllocationRequest(request); err != nil {
		return nil, err
	}
	if request.Class.Spec.Source != nil {
		return nil, fmt.Errorf("%w: cross-project MachineClass sources are not implemented", ErrUnsupported)
	}
	if request.Claim.DeletionTimestamp != nil || !contains(request.Claim.Finalizers, MachineClaimFinalizer) {
		return nil, errors.New("claim must be live and carry the Fleet finalizer before allocation")
	}
	custody, _, err := c.getCustody(ctx, request.Claim.UID)
	if err != nil {
		return nil, fmt.Errorf("claim custody must exist before allocation: %w", err)
	}
	if custody.ClaimNamespace != request.Claim.Namespace || custody.ClaimName != request.Claim.Name || custody.NamespaceUID != request.NamespaceUID || custody.Admission != request.Admission || custody.State == CustodyReleased {
		return nil, fmt.Errorf("%w: claim custody does not authorize allocation", ErrRecordInvalid)
	}
	existing, err := c.recordsForClaim(ctx, request.Claim.UID)
	if err != nil {
		return nil, err
	}
	if len(existing) > 1 {
		return nil, fmt.Errorf("%w: claim has duplicate asset records", ErrRecordInvalid)
	}
	if len(existing) == 1 {
		record := existing[0]
		if err := validateAssetForRequest(record, request); err != nil {
			return nil, err
		}
		if custody.State == CustodySelecting && custody.AssetID == record.AssetID && record.State == AssetPreparing {
			if !c.recordCandidateEligible(record, request) {
				return nil, ErrInsufficientCapacity
			}
			if err := c.bindCustody(ctx, request.Claim, record.AssetID); err != nil {
				return nil, err
			}
		} else if custody.State != CustodyBound || custody.AssetID != record.AssetID {
			return nil, fmt.Errorf("%w: custody and asset authority disagree", ErrRecordInvalid)
		}
		if record.State == AssetReleased || record.State == AssetReleasing {
			return nil, ErrClaimAlreadyReleased
		}
		return c.commitPrepared(ctx, record.AssetID, request.Claim.UID)
	}
	if len(request.Claim.Spec.NetworkInterfaces) != 0 {
		if c.networks == nil || !request.Location.Valid() || request.Location.Name != request.Claim.Spec.LocationRef.Name {
			return nil, fleetnetwork.ErrAuthorizationUnavailable
		}
		interfaces, err := networkRequests(request)
		if err != nil {
			return nil, err
		}
		plan, err := c.networks.Preflight(ctx, fleetnetwork.PreflightRequest{ClaimNamespace: request.Claim.Namespace, ClaimUID: request.Claim.UID, NamespaceUID: request.NamespaceUID, PrincipalUID: request.Admission.PrincipalUID, Location: request.Location, Interfaces: interfaces})
		if err != nil {
			return nil, err
		}
		request.networkPlan = &plan
	}

	selector, err := metav1.LabelSelectorAsSelector(&request.Class.Spec.Capacity.Machines.Selector)
	if err != nil {
		return nil, fmt.Errorf("invalid MachineClass selector: %w", err)
	}
	candidates := append([]fleetv1.Machine(nil), request.Machines...)
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Namespace+"/"+candidates[i].Name < candidates[j].Namespace+"/"+candidates[j].Name
	})
	for i := range candidates {
		machine := &candidates[i]
		if machine.Namespace != request.Class.Spec.Capacity.Machines.Namespace || !selector.Matches(labels.Set(machine.Labels)) || !machineEligible(machine, request.EvidenceObservedAt[machine.UID], c.now(), c.maxEvidenceAge) || !matches(request.Profile.Spec, machine.Status.Hardware) {
			continue
		}
		candidateID := machineAssetID(machine)
		if custody.State == CustodySelecting && custody.AssetID != candidateID {
			continue
		}
		if custody.State == CustodyUnbound {
			proposal, reusable, err := c.proposeAsset(ctx, request, machine)
			if err != nil {
				return nil, err
			}
			if !reusable {
				continue
			}
			if err := c.selectCustody(ctx, request.Claim, proposal); err != nil {
				return nil, err
			}
			custody.State, custody.AssetID, custody.ProposedAsset = CustodySelecting, candidateID, proposal
		}
		prepared, won, err := c.prepareAsset(ctx, request, machine)
		if err != nil {
			return nil, err
		}
		if !won {
			if custody.State == CustodySelecting && custody.AssetID == candidateID {
				if err := c.clearSelection(ctx, request.Claim.UID, candidateID); err != nil {
					return nil, err
				}
				custody.State, custody.AssetID = CustodyUnbound, ""
			}
			continue
		}
		if err := c.bindCustody(ctx, request.Claim, prepared.AssetID); err != nil {
			return nil, err
		}
		return c.commitPrepared(ctx, prepared.AssetID, request.Claim.UID)
	}
	return nil, ErrInsufficientCapacity
}

func machineAssetID(machine *fleetv1.Machine) string {
	return fmt.Sprintf("%s/%s/%s/%s", machine.Namespace, machine.Spec.ProviderRef.APIGroup, machine.Spec.ProviderRef.Kind, machine.Spec.ProviderRef.Name)
}

func (c *Coordinator) recordCandidateEligible(record *AssetRecord, request AllocationRequest) bool {
	selector, err := metav1.LabelSelectorAsSelector(&request.Class.Spec.Capacity.Machines.Selector)
	if err != nil {
		return false
	}
	for i := range request.Machines {
		m := &request.Machines[i]
		if machineAssetID(m) == record.AssetID && m.UID == record.MachineUID && m.Namespace == record.MachineNamespace && m.Name == record.MachineName && m.Namespace == request.Class.Spec.Capacity.Machines.Namespace && selector.Matches(labels.Set(m.Labels)) && machineEligible(m, request.EvidenceObservedAt[m.UID], c.now(), c.maxEvidenceAge) && matches(request.Profile.Spec, m.Status.Hardware) {
			return true
		}
	}
	return false
}

func validateAssetForRequest(record *AssetRecord, request AllocationRequest) error {
	// Specs contain slices, so compare their canonical JSON rather than
	// allowing a recovered Preparing record to drift across retries.
	got, _ := json.Marshal(record.AcceptedRequirements)
	want, _ := json.Marshal(request.Profile.Spec)
	if record.ClaimNamespace != request.Claim.Namespace || record.ClaimName != request.Claim.Name || record.ClaimUID != request.Claim.UID || record.ClassUID != request.Class.UID || record.ProfileUID != request.Profile.UID || string(got) != string(want) {
		return fmt.Errorf("%w: retained asset inputs differ from allocation retry", ErrRecordInvalid)
	}
	interfaces, err := networkRequests(request)
	if err != nil {
		return err
	}
	digest, err := fleetnetwork.DigestRequests(interfaces)
	if err != nil {
		return err
	}
	if (record.Network == nil) != (len(interfaces) == 0) || record.Network != nil && (record.Network.Plan.RequestDigest != digest || record.Network.Plan.Location.Name != request.Claim.Spec.LocationRef.Name) {
		return fmt.Errorf("%w: retained network inputs differ from allocation retry", ErrRecordInvalid)
	}
	return nil
}

func networkRequests(request AllocationRequest) ([]fleetnetwork.InterfaceRequest, error) {
	slots := map[string]bool{}
	for _, iface := range request.Profile.Spec.Interfaces {
		slots[iface.Name] = true
	}
	seen := map[string]bool{}
	result := make([]fleetnetwork.InterfaceRequest, 0, len(request.Claim.Spec.NetworkInterfaces))
	for _, requested := range request.Claim.Spec.NetworkInterfaces {
		if requested.Name == "" || seen[requested.Name] || !slots[requested.Name] || requested.Network.Name == "" {
			return nil, fmt.Errorf("%w: network interface does not name one unique profile slot", ErrRecordInvalid)
		}
		seen[requested.Name] = true
		reclaim, ok := fleetnetwork.NormalizeReclaimPolicy(requested.ReclaimPolicy)
		if !ok {
			return nil, fmt.Errorf("%w: invalid network reclaim policy", ErrRecordInvalid)
		}
		namespace := requested.Network.Namespace
		if namespace == "" {
			namespace = request.Claim.Namespace
		}
		item := fleetnetwork.InterfaceRequest{Name: requested.Name, NetworkName: requested.Network.Name, NetworkNS: namespace, IPFamilies: append([]string(nil), requested.IPFamilies...), ReclaimPolicy: reclaim}
		for _, address := range requested.Addresses {
			if address.ClassRef.Name == "" {
				return nil, fmt.Errorf("%w: address class name is required", ErrRecordInvalid)
			}
			item.AddressClasses = append(item.AddressClasses, address.ClassRef.Name)
		}
		if !fleetnetwork.ValidAttachmentLists(item.IPFamilies, item.AddressClasses) {
			return nil, fmt.Errorf("%w: network attachment families or address classes violate NSO cardinality or uniqueness", ErrRecordInvalid)
		}
		result = append(result, item)
	}
	return result, nil
}

func validateAllocationRequest(request AllocationRequest) error {
	if request.Claim == nil || request.Class == nil || request.Profile == nil || request.Class.Spec.Capacity == nil {
		return errors.New("claim, class, profile, and class capacity are required")
	}
	if request.Claim.UID == "" || request.Class.UID == "" || request.Profile.UID == "" || request.Claim.Namespace == "" || request.Claim.Name == "" || request.NamespaceUID == "" || !request.Admission.valid() {
		return errors.New("claim, class, and profile lifetime identities are required")
	}
	if request.Class.Name != request.Claim.Spec.MachineClassRef.Name || request.Profile.Name != request.Claim.Spec.MachineProfileRef.Name {
		return errors.New("resolved class or profile does not match the claim")
	}
	return nil
}

func machineEligible(machine *fleetv1.Machine, observedAt, now time.Time, maxAge time.Duration) bool {
	if machine.UID == "" || machine.Spec.AllocationPolicy != "Enabled" || observedAt.IsZero() || observedAt.After(now) || now.Sub(observedAt) > maxAge {
		return false
	}
	for _, conditionType := range []string{"Prepared", "Healthy", "Available"} {
		condition := apiMeta.FindStatusCondition(machine.Status.Conditions, conditionType)
		if condition == nil || condition.Status != metav1.ConditionTrue || condition.ObservedGeneration != machine.Generation {
			return false
		}
	}
	return true
}

func (c *Coordinator) prepareAsset(ctx context.Context, request AllocationRequest, machine *fleetv1.Machine) (*AssetRecord, bool, error) {
	assetID := machineAssetID(machine)
	custody, _, err := c.getCustody(ctx, request.Claim.UID)
	if err != nil {
		return nil, false, err
	}
	if custody.State != CustodySelecting || custody.AssetID != assetID || custody.ProposedAsset == nil || !sameRecord(custody.ProposedAsset, newAssetRecord(request, machine, assetID, custody.ProposedAsset.Epoch, custody.ProposedAsset.BindingID, custody.ProposedAsset.FenceToken)) {
		return nil, false, nil
	}
	proposal := custody.ProposedAsset
	name := assetRecordName(assetID)
	var secret corev1.Secret
	err = c.client.Get(ctx, client.ObjectKey{Namespace: c.namespace, Name: name}, &secret)
	if err == nil {
		existing, decodeErr := decodeAssetSecret(&secret)
		if decodeErr != nil {
			return nil, false, decodeErr
		}
		if existing.State != AssetReleased {
			if existing.ClaimUID != request.Claim.UID {
				return nil, false, nil
			}
			if err := validateAssetForRequest(existing, request); err != nil {
				return nil, false, err
			}
			if existing.MachineNamespace != machine.Namespace || existing.MachineName != machine.Name || existing.MachineUID != machine.UID {
				return nil, false, ErrRecordInvalid
			}
			return existing, sameRecord(existing, proposal), nil
		}
		previousCustody, _, custodyErr := c.getCustody(ctx, existing.ClaimUID)
		if custodyErr != nil {
			return nil, false, custodyErr
		}
		if previousCustody.State != CustodyReleased || previousCustody.AssetID != existing.AssetID {
			return nil, false, nil
		}
		if proposal.Epoch != existing.Epoch+1 {
			return nil, false, ErrRecordInvalid
		}
		next := proposal
		if err := encodeRecord(&secret, next, "asset"); err != nil {
			return nil, false, err
		}
		if err := c.client.Update(ctx, &secret); apierrors.IsConflict(err) {
			return c.prepareAsset(ctx, request, machine)
		} else if err != nil {
			return nil, false, err
		}
		return next, true, nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, false, err
	}
	if proposal.Epoch != 1 {
		return nil, false, ErrRecordInvalid
	}
	record := proposal
	secret = corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: c.namespace}}
	if err := encodeRecord(&secret, record, "asset"); err != nil {
		return nil, false, err
	}
	if err := c.client.Create(ctx, &secret); apierrors.IsAlreadyExists(err) {
		return c.prepareAsset(ctx, request, machine)
	} else if err != nil {
		return nil, false, err
	}
	return record, true, nil
}

func sameRecord(a, b *AssetRecord) bool {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return string(left) == string(right)
}

func (c *Coordinator) proposeAsset(ctx context.Context, request AllocationRequest, machine *fleetv1.Machine) (*AssetRecord, bool, error) {
	assetID := machineAssetID(machine)
	epoch := int64(1)
	existing, _, err := c.getAsset(ctx, assetID)
	if err == nil {
		if existing.State != AssetReleased {
			return nil, false, nil
		}
		previous, _, err := c.getCustody(ctx, existing.ClaimUID)
		if err != nil {
			return nil, false, err
		}
		if previous.State != CustodyReleased || previous.AssetID != assetID {
			return nil, false, nil
		}
		epoch = existing.Epoch + 1
	} else if !apierrors.IsNotFound(err) {
		return nil, false, err
	}
	binding, fence, err := c.authorityIDs()
	if err != nil {
		return nil, false, err
	}
	return newAssetRecord(request, machine, assetID, epoch, binding, fence), true, nil
}

func newAssetRecord(request AllocationRequest, machine *fleetv1.Machine, assetID string, epoch int64, bindingID, fenceToken string) *AssetRecord {
	record := &AssetRecord{SchemaVersion: recordSchemaVersion, State: AssetPreparing, AssetID: assetID,
		MachineNamespace: machine.Namespace, MachineName: machine.Name, MachineUID: machine.UID,
		ClaimNamespace: request.Claim.Namespace, ClaimName: request.Claim.Name, ClaimUID: request.Claim.UID,
		ClassUID: request.Class.UID, ProfileUID: request.Profile.UID, BindingID: bindingID, Epoch: epoch,
		FenceToken: fenceToken, CurrentRevision: "None", AcceptedRequirements: request.Profile.Spec, Hardware: machine.Status.Hardware}
	if request.networkPlan != nil {
		record.Network = &NetworkRecord{Plan: *request.networkPlan}
	}
	return record
}

func (c *Coordinator) commitPrepared(ctx context.Context, assetID string, claimUID types.UID) (*AssetRecord, error) {
	var committed *AssetRecord
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		record, secret, err := c.getAsset(ctx, assetID)
		if err != nil {
			return err
		}
		if record.ClaimUID != claimUID {
			return fmt.Errorf("%w: asset belongs to another claim", ErrRecordInvalid)
		}
		if record.State == AssetCommitted {
			committed = record
			return nil
		}
		if record.State != AssetPreparing {
			return fmt.Errorf("%w: cannot commit asset in state %s", ErrRecordInvalid, record.State)
		}
		record.State = AssetCommitted
		if err := encodeRecord(secret, record, "asset"); err != nil {
			return err
		}
		if err := c.client.Update(ctx, secret); err != nil {
			return err
		}
		committed = record
		return nil
	})
	return committed, err
}

// ReconcileNetwork materializes and observes NSO claims only after the asset
// binding is committed. It uses the retained plan rather than the public claim.
func (c *Coordinator) ReconcileNetwork(ctx context.Context, claimUID types.UID) (fleetnetwork.Observation, error) {
	if c.networks == nil || claimUID == "" {
		return fleetnetwork.Observation{}, fleetnetwork.ErrAuthorizationUnavailable
	}
	records, err := c.recordsForClaim(ctx, claimUID)
	if err != nil || len(records) != 1 {
		if err != nil {
			return fleetnetwork.Observation{}, err
		}
		return fleetnetwork.Observation{}, ErrRecordInvalid
	}
	asset := records[0]
	if asset.State != AssetCommitted || asset.Network == nil {
		return fleetnetwork.Observation{}, ErrRecordInvalid
	}
	previous := asset.Network.Observation
	previous.Lifecycle = append([]fleetnetwork.InterfaceLifecycleState(nil), asset.Network.Lifecycle...)
	observation, ensureErr := c.networks.Ensure(ctx, fleetnetwork.EnsureRequest{ClaimUID: claimUID, BindingID: asset.BindingID, Plan: asset.Network.Plan, Previous: previous})
	if observation.Revision != "" || len(observation.Lifecycle) != 0 {
		err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
			current, secret, getErr := c.getAsset(ctx, asset.AssetID)
			if getErr != nil {
				return getErr
			}
			if current.ClaimUID != claimUID || current.State != AssetCommitted || current.Network == nil || current.Network.Plan.RequestDigest != asset.Network.Plan.RequestDigest {
				return ErrRecordInvalid
			}
			// An unsuccessful refresh is not evidence that the last verified
			// allocation disappeared. Preserve a Programmed observation (and its
			// addresses) while retaining any complete lifecycle progress needed
			// for an exact retry.
			if ensureErr == nil || !current.Network.Observation.Programmed(current.Network.Plan) {
				current.Network.Observation = observation
			}
			if len(observation.Lifecycle) != 0 {
				current.Network.Lifecycle = append([]fleetnetwork.InterfaceLifecycleState(nil), observation.Lifecycle...)
			}
			if err := validateNetworkRecord(current.Network); err != nil {
				return err
			}
			if err := encodeRecord(secret, current, "asset"); err != nil {
				return err
			}
			return c.client.Update(ctx, secret)
		})
		if err != nil {
			return observation, err
		}
	}
	return observation, ensureErr
}

func (c *Coordinator) authorityIDs() (string, string, error) {
	binding, err := c.newID()
	if err != nil {
		return "", "", err
	}
	fence, err := c.newID()
	if err != nil {
		return "", "", err
	}
	return binding, fence, nil
}

type ReleaseResult struct {
	State                 AssetState
	SafeToRemoveFinalizer bool
}

func (c *Coordinator) Release(ctx context.Context, claimUID types.UID) (ReleaseResult, error) {
	if claimUID == "" {
		return ReleaseResult{}, errors.New("claim UID is required")
	}
	custody, _, err := c.getCustody(ctx, claimUID)
	if err != nil {
		return ReleaseResult{}, fmt.Errorf("%w: claim custody is missing or invalid: %v", ErrRecordInvalid, err)
	}
	// Released custody is the terminal authority for this claim lifetime. The
	// asset record may already belong to a later epoch and another claim by the
	// time a conflicted finalizer update retries release.
	if custody.State == CustodyReleased {
		return ReleaseResult{State: AssetReleased, SafeToRemoveFinalizer: true}, nil
	}
	records, err := c.recordsForClaim(ctx, claimUID)
	if err != nil {
		return ReleaseResult{}, err
	}
	if len(records) == 0 {
		if custody.State == CustodyReleased && custody.AssetID == "" {
			return ReleaseResult{State: AssetReleased, SafeToRemoveFinalizer: true}, nil
		}
		if custody.State == CustodySelecting && custody.AssetID != "" {
			if err := c.materializeSelectionForRelease(ctx, custody); err != nil {
				return ReleaseResult{}, err
			}
			updated, _, err := c.getCustody(ctx, claimUID)
			if err != nil {
				return ReleaseResult{}, err
			}
			if updated.State == CustodyReleased {
				return ReleaseResult{State: AssetReleased, SafeToRemoveFinalizer: true}, nil
			}
			return c.Release(ctx, claimUID)
		}
		if custody.State != CustodyUnbound || custody.AssetID != "" {
			return ReleaseResult{}, fmt.Errorf("%w: bound custody has no asset authority", ErrRecordInvalid)
		}
		if err := c.releaseCustody(ctx, claimUID, ""); err != nil {
			return ReleaseResult{}, err
		}
		return ReleaseResult{State: AssetReleased, SafeToRemoveFinalizer: true}, nil
	}
	if len(records) != 1 {
		return ReleaseResult{}, fmt.Errorf("%w: claim has duplicate asset records", ErrRecordInvalid)
	}
	record := records[0]
	if custody.State == CustodyReleased && custody.AssetID == record.AssetID && record.State == AssetReleased {
		return ReleaseResult{State: AssetReleased, SafeToRemoveFinalizer: true}, nil
	}
	if custody.State == CustodySelecting && custody.AssetID == record.AssetID && record.State == AssetPreparing {
		// Allocation may have crashed after the exclusive asset create but
		// before custody was advanced. Adopt that exact preparation solely to
		// release it; Preparing has admitted no provider effect.
		claim := &fleetv1.MachineClaim{ObjectMeta: metav1.ObjectMeta{Namespace: record.ClaimNamespace, Name: record.ClaimName, UID: record.ClaimUID}}
		if err := c.bindCustody(ctx, claim, record.AssetID); err != nil {
			return ReleaseResult{}, err
		}
		custody.State, custody.AssetID = CustodyBound, record.AssetID
	}
	if custody.State != CustodyBound || custody.AssetID != record.AssetID {
		return ReleaseResult{}, fmt.Errorf("%w: custody and asset authority disagree", ErrRecordInvalid)
	}
	if record.ActiveOperationID != "" {
		if err := c.settleActiveOperation(ctx, record); err != nil {
			return ReleaseResult{State: record.State}, err
		}
		return c.Release(ctx, claimUID)
	}
	if record.State == AssetReleased {
		if err := c.releaseCustody(ctx, claimUID, record.AssetID); err != nil {
			return ReleaseResult{}, err
		}
		return ReleaseResult{State: AssetReleased, SafeToRemoveFinalizer: true}, nil
	}
	if record.State == AssetPreparing {
		if operations, err := c.operationsForBinding(ctx, record.BindingID); err != nil {
			return ReleaseResult{}, err
		} else if len(operations) != 0 {
			return ReleaseResult{}, fmt.Errorf("%w: preparing binding has retained operations", ErrRecordInvalid)
		}
		if _, err := c.setAssetState(ctx, record.AssetID, claimUID, AssetPreparing, AssetReleased); err != nil {
			return ReleaseResult{}, err
		}
		if err := c.releaseCustody(ctx, claimUID, record.AssetID); err != nil {
			return ReleaseResult{}, err
		}
		return ReleaseResult{State: AssetReleased, SafeToRemoveFinalizer: true}, nil
	}
	if record.State == AssetCommitted {
		record, err = c.setAssetState(ctx, record.AssetID, claimUID, AssetCommitted, AssetReleasing)
		if err != nil {
			return ReleaseResult{}, err
		}
	}
	if record.State != AssetReleasing {
		return ReleaseResult{}, fmt.Errorf("%w: unexpected release state %s", ErrRecordInvalid, record.State)
	}
	if record.Network != nil && record.Network.ReleaseRevision == "" {
		if c.networks == nil {
			return ReleaseResult{State: AssetReleasing}, fleetnetwork.ErrAuthorizationUnavailable
		}
		released, releaseErr := c.networks.Release(ctx, fleetnetwork.ReleaseRequest{ClaimUID: record.ClaimUID, BindingID: record.BindingID, Plan: record.Network.Plan, Observation: record.Network.Observation, Lifecycle: record.Network.Lifecycle})
		if len(released.Lifecycle) != 0 || released.Verified {
			if err := c.retainNetworkRelease(ctx, record.AssetID, record.ClaimUID, released); err != nil {
				return ReleaseResult{State: AssetReleasing}, err
			}
			record, _, err = c.getAsset(ctx, record.AssetID)
			if err != nil {
				return ReleaseResult{State: AssetReleasing}, err
			}
		}
		if releaseErr != nil {
			return ReleaseResult{State: AssetReleasing}, releaseErr
		}
		if !released.Verified {
			return ReleaseResult{State: AssetReleasing}, fleetnetwork.ErrPending
		}
	}
	request := simulator.Request{ID: "cleanup-" + record.BindingID, Kind: "cleanup", AssetID: record.AssetID, BindingID: record.BindingID, Epoch: record.Epoch, FenceToken: record.FenceToken}
	if _, err := c.executeAndRecover(ctx, request); err != nil {
		return ReleaseResult{State: AssetReleasing}, err
	}
	if _, err := c.setAssetState(ctx, record.AssetID, claimUID, AssetReleasing, AssetReleased); err != nil {
		return ReleaseResult{}, err
	}
	if err := c.releaseCustody(ctx, claimUID, record.AssetID); err != nil {
		return ReleaseResult{}, err
	}
	return ReleaseResult{State: AssetReleased, SafeToRemoveFinalizer: true}, nil
}

func (c *Coordinator) retainNetworkRelease(ctx context.Context, assetID string, claimUID types.UID, released fleetnetwork.ReleaseObservation) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		record, secret, err := c.getAsset(ctx, assetID)
		if err != nil {
			return err
		}
		if record.ClaimUID != claimUID || record.State != AssetReleasing || record.Network == nil {
			return ErrRecordInvalid
		}
		if len(released.Lifecycle) != 0 {
			record.Network.Lifecycle = append([]fleetnetwork.InterfaceLifecycleState(nil), released.Lifecycle...)
		}
		if released.Verified {
			if released.Revision == "" {
				return ErrRecordInvalid
			}
			record.Network.ReleaseRevision = released.Revision
		}
		if err := validateNetworkRecord(record.Network); err != nil {
			return err
		}
		if err := encodeRecord(secret, record, "asset"); err != nil {
			return err
		}
		return c.client.Update(ctx, secret)
	})
}

func (c *Coordinator) settleActiveOperation(ctx context.Context, asset *AssetRecord) error {
	operations, err := c.operationsForBinding(ctx, asset.BindingID)
	if err != nil {
		return err
	}
	var operation *InstallationRecord
	for _, candidate := range operations {
		if candidate.OperationID == asset.ActiveOperationID {
			if operation != nil {
				return ErrRecordInvalid
			}
			operation = candidate
		}
	}
	if operation == nil {
		return ErrOperationActive
	}
	request := simulator.Request{ID: operation.OperationID, Kind: "install", AssetID: asset.AssetID, BindingID: asset.BindingID, Epoch: asset.Epoch, FenceToken: asset.FenceToken, Inputs: installationInputs(operation)}
	if operation.State == OperationAdmitted {
		if _, err := c.executeAndRecover(ctx, request); err != nil {
			return err
		}
		operation, err = c.markOperationSucceeded(ctx, operationRecordName(operation.ClaimUID, operation.Predecessor), operation)
		if err != nil {
			return err
		}
	}
	return c.publishInstalledRevision(ctx, asset.AssetID, asset.ClaimUID, operation)
}

type InstallationRequest struct {
	Claim        *fleetv1.MachineClaim
	Installation *fleetv1.Installation
	NamespaceUID types.UID
	Admission    InstallationAdmissionAuthority
}

type InstallationResult struct {
	Revision string
	Record   InstallationRecord
}

func (c *Coordinator) Install(ctx context.Context, request InstallationRequest) (InstallationResult, error) {
	if request.Claim == nil || request.Installation == nil || request.Claim.UID == "" || request.Installation.UID == "" || request.NamespaceUID == "" || !request.Admission.valid() {
		return InstallationResult{}, errors.New("claim, installation, and namespace lifetime identities are required")
	}
	if request.Installation.Namespace != request.Claim.Namespace || request.Installation.Spec.ClaimRef.Name != request.Claim.Name {
		return InstallationResult{}, errors.New("installation does not target the supplied claim lifetime")
	}
	if request.Claim.DeletionTimestamp != nil || !contains(request.Claim.Finalizers, MachineClaimFinalizer) {
		return InstallationResult{}, errors.New("claim must be live and carry the Fleet finalizer before installation admission")
	}
	if request.Installation.Spec.BootstrapConfigRef != nil {
		return InstallationResult{}, fmt.Errorf("%w: bootstrap retention and authorization are not implemented", ErrUnsupported)
	}
	predecessor, err := predecessorValue(request.Installation.Spec.ExpectedPredecessor)
	if err != nil {
		return InstallationResult{}, err
	}
	assets, err := c.recordsForClaim(ctx, request.Claim.UID)
	if err != nil {
		return InstallationResult{}, err
	}
	if len(assets) != 1 {
		return InstallationResult{}, fmt.Errorf("%w: claim does not have one retained asset", ErrRecordInvalid)
	}
	asset := assets[0]
	if asset.State != AssetCommitted {
		return InstallationResult{}, ErrPredecessorConflict
	}
	custody, _, err := c.getCustody(ctx, request.Claim.UID)
	if err != nil || custody.NamespaceUID != request.NamespaceUID || custody.Admission.PrincipalUID != request.Admission.PrincipalUID {
		return InstallationResult{}, ErrRecordInvalid
	}
	operationName := operationRecordName(request.Claim.UID, predecessor)
	operation, _, err := c.getOperation(ctx, operationName)
	if apierrors.IsNotFound(err) {
		if asset.Network != nil && (!asset.Network.Observation.Prepared(asset.Network.Plan) || asset.Network.Observation.ObservedAt.After(c.now()) || c.now().Sub(asset.Network.Observation.ObservedAt) > c.maxEvidenceAge) {
			return InstallationResult{}, fleetnetwork.ErrEvidenceStale
		}
		image, resolveErr := c.images.ResolveImage(ctx, ImageRequest{NamespaceUID: request.NamespaceUID, ClaimUID: request.Claim.UID, InstallationUID: request.Installation.UID, ImageName: request.Installation.Spec.ImageRef.Name})
		if resolveErr != nil {
			return InstallationResult{}, resolveErr
		}
		if err := image.Validate(); err != nil {
			return InstallationResult{}, err
		}
		if image.CatalogAuthority != request.Admission.CatalogAuthority || image.CatalogRevision != request.Admission.CatalogRevision || image.ImageDecisionID != request.Admission.ImageDecisionID {
			return InstallationResult{}, ErrRecordInvalid
		}
		operation, _, err = c.getOrCreateOperation(ctx, operationName, request, asset, predecessor, image)
	}
	if err != nil {
		return InstallationResult{}, err
	}
	if operation.InstallationUID != request.Installation.UID || operation.NamespaceUID != request.NamespaceUID || operation.ClaimUID != request.Claim.UID || operation.AssetID != asset.AssetID || operation.BindingID != asset.BindingID || operation.Epoch != asset.Epoch || operation.Predecessor != predecessor || operation.ImageName != request.Installation.Spec.ImageRef.Name || operation.Admission != request.Admission {
		return InstallationResult{}, ErrPredecessorConflict
	}
	if operation.State == OperationSucceeded {
		if err := c.publishInstalledRevision(ctx, asset.AssetID, asset.ClaimUID, operation); err != nil {
			return InstallationResult{}, err
		}
		return InstallationResult{Revision: operation.ResultRevision, Record: *operation}, nil
	}
	if asset.CurrentRevision != predecessor || (asset.ActiveOperationID != "" && asset.ActiveOperationID != operation.OperationID) {
		return InstallationResult{}, ErrPredecessorConflict
	}
	if err := c.activateOperation(ctx, asset.AssetID, asset.ClaimUID, predecessor, operation.OperationID); err != nil {
		return InstallationResult{}, err
	}
	execRequest := simulator.Request{ID: operation.OperationID, Kind: "install", AssetID: asset.AssetID, BindingID: asset.BindingID, Epoch: asset.Epoch, FenceToken: asset.FenceToken, Inputs: installationInputs(operation)}
	if _, err := c.executeAndRecover(ctx, execRequest); err != nil {
		return InstallationResult{}, err
	}
	operation, err = c.markOperationSucceeded(ctx, operationName, operation)
	if err != nil {
		return InstallationResult{}, err
	}
	if err := c.publishInstalledRevision(ctx, asset.AssetID, asset.ClaimUID, operation); err != nil {
		return InstallationResult{}, err
	}
	return InstallationResult{Revision: operation.ResultRevision, Record: *operation}, nil
}

func predecessorValue(value fleetv1.InstallationPredecessor) (string, error) {
	switch value.Type {
	case "None":
		if value.Revision == "" {
			return "None", nil
		}
	case "Revision":
		if value.Revision != "" {
			return value.Revision, nil
		}
	}
	return "", errors.New("expectedPredecessor must be None without revision or Revision with a nonempty revision")
}

func sameNetworkInputs(inputs *fleetnetwork.Observation, record *NetworkRecord) bool {
	if inputs == nil || record == nil {
		return inputs == nil && record == nil
	}
	left, _ := json.Marshal(inputs)
	right, _ := json.Marshal(record.Observation)
	return string(left) == string(right)
}

func installationInputs(operation *InstallationRecord) json.RawMessage {
	type interfaceInput struct {
		Name      string                 `json:"name"`
		MTU       int32                  `json:"mtu,omitempty"`
		Addresses []fleetnetwork.Address `json:"addresses,omitempty"`
	}
	type networkInput struct {
		Revision   string           `json:"revision"`
		Interfaces []interfaceInput `json:"interfaces"`
	}
	value := struct {
		Image   string        `json:"image"`
		Network *networkInput `json:"network,omitempty"`
	}{Image: operation.Image.Reference()}
	if operation.NetworkInputs != nil {
		value.Network = &networkInput{Revision: operation.NetworkInputs.Revision}
		for _, iface := range operation.NetworkInputs.Interfaces {
			value.Network.Interfaces = append(value.Network.Interfaces, interfaceInput{Name: iface.Name, MTU: iface.MTU, Addresses: append([]fleetnetwork.Address(nil), iface.Addresses...)})
		}
	}
	b, _ := json.Marshal(value)
	return b
}

func (c *Coordinator) getOrCreateOperation(ctx context.Context, name string, request InstallationRequest, asset *AssetRecord, predecessor string, image ResolvedImage) (*InstallationRecord, *corev1.Secret, error) {
	var secret corev1.Secret
	err := c.client.Get(ctx, client.ObjectKey{Namespace: c.namespace, Name: name}, &secret)
	if err == nil {
		record, decodeErr := decodeOperationSecret(&secret)
		return record, &secret, decodeErr
	}
	if !apierrors.IsNotFound(err) {
		return nil, nil, err
	}
	operationID, err := c.newID()
	if err != nil {
		return nil, nil, err
	}
	revisionID, err := c.newID()
	if err != nil {
		return nil, nil, err
	}
	record := &InstallationRecord{SchemaVersion: recordSchemaVersion, State: OperationAdmitted, OperationID: "install-" + operationID, InstallationUID: request.Installation.UID, NamespaceUID: request.NamespaceUID, ClaimUID: request.Claim.UID, AssetID: asset.AssetID, BindingID: asset.BindingID, Epoch: asset.Epoch, Predecessor: predecessor, ResultRevision: "ir-" + revisionID, Image: image, ImageName: request.Installation.Spec.ImageRef.Name, Admission: request.Admission}
	if asset.Network != nil {
		inputs := asset.Network.Observation
		record.NetworkInputs = &inputs
	}
	secret = corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: c.namespace}}
	if err := encodeRecord(&secret, record, "installation"); err != nil {
		return nil, nil, err
	}
	if err := c.client.Create(ctx, &secret); apierrors.IsAlreadyExists(err) {
		return c.getOrCreateOperation(ctx, name, request, asset, predecessor, image)
	} else if err != nil {
		return nil, nil, err
	}
	return record, &secret, nil
}

func (c *Coordinator) getOperation(ctx context.Context, name string) (*InstallationRecord, *corev1.Secret, error) {
	var secret corev1.Secret
	if err := c.client.Get(ctx, client.ObjectKey{Namespace: c.namespace, Name: name}, &secret); err != nil {
		return nil, nil, err
	}
	record, err := decodeOperationSecret(&secret)
	return record, &secret, err
}

func (c *Coordinator) activateOperation(ctx context.Context, assetID string, claimUID types.UID, predecessor, operationID string) error {
	record, secret, err := c.getAsset(ctx, assetID)
	if err != nil {
		return err
	}
	if record.ClaimUID != claimUID || record.State != AssetCommitted || record.CurrentRevision != predecessor {
		return ErrPredecessorConflict
	}
	if record.ActiveOperationID != "" && record.ActiveOperationID != operationID {
		return ErrOperationActive
	}
	record.ActiveOperationID = operationID
	if err := encodeRecord(secret, record, "asset"); err != nil {
		return err
	}
	return c.client.Update(ctx, secret)
}

func (c *Coordinator) publishInstalledRevision(ctx context.Context, assetID string, claimUID types.UID, operation *InstallationRecord) error {
	record, secret, err := c.getAsset(ctx, assetID)
	if err != nil {
		return err
	}
	if record.ClaimUID != claimUID || record.State != AssetCommitted {
		return ErrPredecessorConflict
	}
	if record.CurrentRevision == operation.ResultRevision && record.ActiveOperationID == "" {
		return nil
	}
	if record.CurrentRevision != operation.Predecessor || record.ActiveOperationID != operation.OperationID {
		return ErrPredecessorConflict
	}
	record.CurrentRevision = operation.ResultRevision
	record.ActiveOperationID = ""
	if err := encodeRecord(secret, record, "asset"); err != nil {
		return err
	}
	return c.client.Update(ctx, secret)
}

func (c *Coordinator) markOperationSucceeded(ctx context.Context, name string, expected *InstallationRecord) (*InstallationRecord, error) {
	var secret corev1.Secret
	if err := c.client.Get(ctx, client.ObjectKey{Namespace: c.namespace, Name: name}, &secret); err != nil {
		return nil, err
	}
	record, err := decodeOperationSecret(&secret)
	if err != nil {
		return nil, err
	}
	if record.OperationID != expected.OperationID || record.InstallationUID != expected.InstallationUID {
		return nil, ErrRecordInvalid
	}
	if record.State == OperationSucceeded {
		return record, nil
	}
	record.State = OperationSucceeded
	if err := encodeRecord(&secret, record, "installation"); err != nil {
		return nil, err
	}
	if err := c.client.Update(ctx, &secret); err != nil {
		return nil, err
	}
	return record, nil
}

func (c *Coordinator) executeAndRecover(ctx context.Context, request simulator.Request) (simulator.OperationResult, error) {
	result, err := c.executor.Execute(ctx, request)
	if err == nil {
		return result, validateTerminalResult(request, result)
	}
	if !errors.Is(err, executor.ErrOutcomeUnknown) {
		return simulator.OperationResult{}, err
	}
	retained, found, lookupErr := c.executor.Lookup(ctx, request.ID)
	if lookupErr != nil {
		return simulator.OperationResult{}, fmt.Errorf("%w: lookup failed: %v", executor.ErrOutcomeUnknown, lookupErr)
	}
	if !found {
		return simulator.OperationResult{}, err
	}
	if validateTerminalResult(request, retained) != nil {
		return simulator.OperationResult{}, fmt.Errorf("%w: retained result does not match request", executor.ErrOutcomeUnknown)
	}
	return retained, nil
}

func validateTerminalResult(request simulator.Request, result simulator.OperationResult) error {
	if result.ID != request.ID || result.Kind != request.Kind || result.Phase != "Succeeded" || result.CompletedAt == nil {
		return fmt.Errorf("%w: executor result is not the correlated terminal operation", executor.ErrOutcomeUnknown)
	}
	want := map[string]string{"cleanup": "clean", "install": "installed"}[request.Kind]
	if want != "" && result.Result != want {
		return fmt.Errorf("%w: %s result lacks %q evidence", executor.ErrOutcomeUnknown, request.Kind, want)
	}
	return nil
}

func (c *Coordinator) setAssetState(ctx context.Context, assetID string, claimUID types.UID, from, to AssetState) (*AssetRecord, error) {
	record, secret, err := c.getAsset(ctx, assetID)
	if err != nil {
		return nil, err
	}
	if record.ClaimUID != claimUID || record.State != from || record.ActiveOperationID != "" {
		return nil, fmt.Errorf("%w: asset transition %s to %s rejected", ErrRecordInvalid, record.State, to)
	}
	record.State = to
	if err := encodeRecord(secret, record, "asset"); err != nil {
		return nil, err
	}
	if err := c.client.Update(ctx, secret); err != nil {
		return nil, err
	}
	return record, nil
}

func (c *Coordinator) getAsset(ctx context.Context, assetID string) (*AssetRecord, *corev1.Secret, error) {
	var secret corev1.Secret
	if err := c.client.Get(ctx, client.ObjectKey{Namespace: c.namespace, Name: assetRecordName(assetID)}, &secret); err != nil {
		return nil, nil, err
	}
	record, err := decodeAssetSecret(&secret)
	return record, &secret, err
}

func (c *Coordinator) getCustody(ctx context.Context, claimUID types.UID) (*ClaimCustodyRecord, *corev1.Secret, error) {
	var secret corev1.Secret
	if err := c.client.Get(ctx, client.ObjectKey{Namespace: c.namespace, Name: custodyRecordName(claimUID)}, &secret); err != nil {
		return nil, nil, err
	}
	record, err := decodeCustodySecret(&secret)
	return record, &secret, err
}

func (c *Coordinator) bindCustody(ctx context.Context, claim *fleetv1.MachineClaim, assetID string) error {
	record, secret, err := c.getCustody(ctx, claim.UID)
	if err != nil {
		return err
	}
	if record.ClaimNamespace != claim.Namespace || record.ClaimName != claim.Name {
		return ErrRecordInvalid
	}
	if record.State == CustodyBound && record.AssetID == assetID {
		return nil
	}
	if record.State != CustodySelecting || record.AssetID != assetID {
		return ErrRecordInvalid
	}
	record.State, record.AssetID = CustodyBound, assetID
	if err := encodeRecord(secret, record, "custody"); err != nil {
		return err
	}
	return c.client.Update(ctx, secret)
}

func (c *Coordinator) selectCustody(ctx context.Context, claim *fleetv1.MachineClaim, proposal *AssetRecord) error {
	if proposal == nil || proposal.ClaimUID != claim.UID || validateAssetRecord(proposal) != nil {
		return ErrRecordInvalid
	}
	record, secret, err := c.getCustody(ctx, claim.UID)
	if err != nil {
		return err
	}
	if record.State == CustodySelecting && record.AssetID == proposal.AssetID && sameRecord(record.ProposedAsset, proposal) {
		return nil
	}
	if record.State != CustodyUnbound || record.AssetID != "" {
		return ErrRecordInvalid
	}
	record.State, record.AssetID, record.ProposedAsset = CustodySelecting, proposal.AssetID, proposal
	if err := encodeRecord(secret, record, "custody"); err != nil {
		return err
	}
	return c.client.Update(ctx, secret)
}

func (c *Coordinator) materializeSelectionForRelease(ctx context.Context, custody *ClaimCustodyRecord) error {
	if custody.ProposedAsset == nil || custody.ProposedAsset.AssetID != custody.AssetID || custody.ProposedAsset.ClaimUID != custody.ClaimUID {
		return ErrRecordInvalid
	}
	proposal := custody.ProposedAsset
	var secret corev1.Secret
	err := c.client.Get(ctx, client.ObjectKey{Namespace: c.namespace, Name: assetRecordName(proposal.AssetID)}, &secret)
	if err == nil {
		existing, err := decodeAssetSecret(&secret)
		if err != nil {
			return err
		}
		if existing.ClaimUID != custody.ClaimUID {
			return c.releaseCustody(ctx, custody.ClaimUID, custody.AssetID)
		}
		if !sameRecord(existing, proposal) {
			return ErrRecordInvalid
		}
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	secret = corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: assetRecordName(proposal.AssetID), Namespace: c.namespace}}
	if err := encodeRecord(&secret, proposal, "asset"); err != nil {
		return err
	}
	if err := c.client.Create(ctx, &secret); apierrors.IsAlreadyExists(err) {
		return c.materializeSelectionForRelease(ctx, custody)
	} else {
		return err
	}
}

func (c *Coordinator) clearSelection(ctx context.Context, claimUID types.UID, assetID string) error {
	records, err := c.recordsForClaim(ctx, claimUID)
	if err != nil {
		return err
	}
	if len(records) != 0 {
		return fmt.Errorf("%w: cannot clear selection with retained asset authority", ErrRecordInvalid)
	}
	record, secret, err := c.getCustody(ctx, claimUID)
	if err != nil {
		return err
	}
	if record.State != CustodySelecting || record.AssetID != assetID {
		return ErrRecordInvalid
	}
	record.State, record.AssetID = CustodyUnbound, ""
	record.ProposedAsset = nil
	if err := encodeRecord(secret, record, "custody"); err != nil {
		return err
	}
	return c.client.Update(ctx, secret)
}

func (c *Coordinator) releaseCustody(ctx context.Context, claimUID types.UID, assetID string) error {
	record, secret, err := c.getCustody(ctx, claimUID)
	if err != nil {
		return err
	}
	if record.State == CustodyReleased {
		if record.AssetID != assetID {
			return ErrRecordInvalid
		}
		return nil
	}
	if assetID == "" {
		if record.State != CustodyUnbound || record.AssetID != "" {
			return ErrRecordInvalid
		}
	} else if (record.State != CustodyBound && record.State != CustodySelecting) || record.AssetID != assetID {
		return ErrRecordInvalid
	}
	record.State = CustodyReleased
	if err := encodeRecord(secret, record, "custody"); err != nil {
		return err
	}
	return c.client.Update(ctx, secret)
}

func (c *Coordinator) recordsForClaim(ctx context.Context, claimUID types.UID) ([]*AssetRecord, error) {
	var list corev1.SecretList
	if err := c.client.List(ctx, &list, client.InNamespace(c.namespace)); err != nil {
		return nil, err
	}
	var records []*AssetRecord
	for i := range list.Items {
		secret := &list.Items[i]
		if !strings.HasPrefix(secret.Name, assetRecordPrefix) {
			continue
		}
		record, err := decodeAssetSecret(secret)
		if err != nil {
			return nil, err
		}
		if record.ClaimUID == claimUID {
			records = append(records, record)
		}
	}
	return records, nil
}

func (c *Coordinator) operationsForBinding(ctx context.Context, bindingID string) ([]*InstallationRecord, error) {
	var list corev1.SecretList
	if err := c.client.List(ctx, &list, client.InNamespace(c.namespace)); err != nil {
		return nil, err
	}
	var records []*InstallationRecord
	for i := range list.Items {
		secret := &list.Items[i]
		if !strings.HasPrefix(secret.Name, operationPrefix) {
			continue
		}
		record, err := decodeOperationSecret(secret)
		if err != nil {
			return nil, err
		}
		if record.BindingID == bindingID {
			records = append(records, record)
		}
	}
	return records, nil
}

func encodeRecord(secret *corev1.Secret, value any, recordType string) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if secret.Labels == nil {
		secret.Labels = map[string]string{}
	}
	secret.Type = corev1.SecretType("fleet.datumapis.com/private-lifecycle")
	secret.Labels[recordTypeLabel] = recordType
	secret.Data = map[string][]byte{recordDataKey: b}
	switch record := value.(type) {
	case *AssetRecord:
		secret.Labels[claimUIDLabel] = string(record.ClaimUID)
	case *InstallationRecord:
		secret.Labels[claimUIDLabel] = string(record.ClaimUID)
	case *ClaimCustodyRecord:
		secret.Labels[claimUIDLabel] = string(record.ClaimUID)
	}
	return nil
}

func decodeCustodySecret(secret *corev1.Secret) (*ClaimCustodyRecord, error) {
	var record ClaimCustodyRecord
	if secret.Type != corev1.SecretType("fleet.datumapis.com/private-lifecycle") || secret.Labels[recordTypeLabel] != "custody" || json.Unmarshal(secret.Data[recordDataKey], &record) != nil || validateCustodyRecord(&record) != nil || secret.Name != custodyRecordName(record.ClaimUID) || secret.Labels[claimUIDLabel] != string(record.ClaimUID) {
		return nil, fmt.Errorf("%w: custody secret %s/%s", ErrRecordInvalid, secret.Namespace, secret.Name)
	}
	return &record, nil
}

func validateCustodyRecord(r *ClaimCustodyRecord) error {
	if r.SchemaVersion != recordSchemaVersion || r.ClaimNamespace == "" || r.ClaimName == "" || r.ClaimUID == "" || r.NamespaceUID == "" || !r.Admission.valid() {
		return ErrRecordInvalid
	}
	switch r.State {
	case CustodyUnbound:
		if r.AssetID != "" || r.ProposedAsset != nil {
			return ErrRecordInvalid
		}
	case CustodySelecting, CustodyBound, CustodyReleased:
		// Released may have no asset only for a claim proven never bound.
		if (r.State == CustodySelecting || r.State == CustodyBound) && r.AssetID == "" {
			return ErrRecordInvalid
		}
		if r.State == CustodySelecting && (r.ProposedAsset == nil || r.ProposedAsset.AssetID != r.AssetID || r.ProposedAsset.ClaimUID != r.ClaimUID || validateAssetRecord(r.ProposedAsset) != nil) {
			return ErrRecordInvalid
		}
	default:
		return ErrRecordInvalid
	}
	return nil
}

func decodeAssetSecret(secret *corev1.Secret) (*AssetRecord, error) {
	var record AssetRecord
	if secret.Type != corev1.SecretType("fleet.datumapis.com/private-lifecycle") || secret.Labels[recordTypeLabel] != "asset" || json.Unmarshal(secret.Data[recordDataKey], &record) != nil || validateAssetRecord(&record) != nil || secret.Name != assetRecordName(record.AssetID) || secret.Labels[claimUIDLabel] != string(record.ClaimUID) {
		return nil, fmt.Errorf("%w: asset secret %s/%s", ErrRecordInvalid, secret.Namespace, secret.Name)
	}
	return &record, nil
}

func decodeOperationSecret(secret *corev1.Secret) (*InstallationRecord, error) {
	var record InstallationRecord
	if secret.Type != corev1.SecretType("fleet.datumapis.com/private-lifecycle") || secret.Labels[recordTypeLabel] != "installation" || json.Unmarshal(secret.Data[recordDataKey], &record) != nil || validateInstallationRecord(&record) != nil || secret.Name != operationRecordName(record.ClaimUID, record.Predecessor) || secret.Labels[claimUIDLabel] != string(record.ClaimUID) {
		return nil, fmt.Errorf("%w: installation secret %s/%s", ErrRecordInvalid, secret.Namespace, secret.Name)
	}
	return &record, nil
}

func validateAssetRecord(r *AssetRecord) error {
	if r.SchemaVersion != recordSchemaVersion || r.AssetID == "" || r.MachineNamespace == "" || r.MachineName == "" || r.MachineUID == "" || r.ClaimNamespace == "" || r.ClaimName == "" || r.ClaimUID == "" || r.ClassUID == "" || r.ProfileUID == "" || r.BindingID == "" || r.Epoch < 1 || r.FenceToken == "" || r.CurrentRevision == "" {
		return ErrRecordInvalid
	}
	switch r.State {
	case AssetPreparing, AssetCommitted, AssetReleasing, AssetReleased:
	default:
		return ErrRecordInvalid
	}
	if (r.State == AssetPreparing || r.State == AssetReleasing || r.State == AssetReleased) && r.ActiveOperationID != "" {
		return ErrRecordInvalid
	}
	if validateNetworkRecord(r.Network) != nil {
		return ErrRecordInvalid
	}
	if r.Network != nil && r.State == AssetReleased && r.Network.ReleaseRevision == "" && (r.Network.Observation.Revision != "" || len(r.Network.Lifecycle) != 0) {
		return ErrRecordInvalid
	}
	return nil
}

func validateInstallationRecord(r *InstallationRecord) error {
	if r.SchemaVersion != recordSchemaVersion || !strings.HasPrefix(r.OperationID, "install-") || r.InstallationUID == "" || r.NamespaceUID == "" || r.ClaimUID == "" || r.AssetID == "" || r.BindingID == "" || r.Epoch < 1 || r.Predecessor == "" || !strings.HasPrefix(r.ResultRevision, "ir-") || r.ImageName == "" || !r.Admission.valid() || r.Image.Validate() != nil || r.Image.CatalogAuthority != r.Admission.CatalogAuthority || r.Image.CatalogRevision != r.Admission.CatalogRevision || r.Image.ImageDecisionID != r.Admission.ImageDecisionID {
		return ErrRecordInvalid
	}
	if r.State != OperationAdmitted && r.State != OperationSucceeded {
		return ErrRecordInvalid
	}
	if r.NetworkInputs != nil && (r.NetworkInputs.Revision == "" || r.NetworkInputs.ObservedAt.IsZero()) {
		return ErrRecordInvalid
	}
	return nil
}

func assetRecordName(assetID string) string { return assetRecordPrefix + shortHash(assetID) }
func custodyRecordName(claimUID types.UID) string {
	return custodyRecordPrefix + shortHash(string(claimUID))
}
func operationRecordName(claimUID types.UID, predecessor string) string {
	return operationPrefix + shortHash(string(claimUID)+"/"+predecessor)
}
func shortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:24]
}

func randomID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
func validSHA256(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil && strings.ToLower(value) == value
}

func matches(profile fleetv1.MachineProfileSpec, hardware fleetv1.Hardware) bool {
	if profile.CPU.Architecture != hardware.CPU.Architecture || !matchesCount(profile.CPU.Cores, hardware.CPU.Cores) || !matchesQuantity(profile.Memory.Capacity, hardware.Memory.Capacity) {
		return false
	}
	var gpuCount int64
	for _, gpu := range hardware.GPUs {
		if profile.GPUs != nil && (profile.GPUs.Vendor == "" || profile.GPUs.Vendor == gpu.Vendor) && (len(profile.GPUs.Models) == 0 || contains(profile.GPUs.Models, gpu.Model)) && matchesQuantity(profile.GPUs.MemoryPerDevice, gpu.Memory) {
			gpuCount++
		}
	}
	if profile.GPUs != nil && !matchesCount(profile.GPUs.Count, gpuCount) {
		return false
	}
	used := make([]bool, len(hardware.Disks))
	for _, group := range profile.Disks {
		var candidates []int
		for i, disk := range hardware.Disks {
			if !used[i] && (group.Media == "" || group.Media == disk.Media) && (group.Interface == "" || group.Interface == disk.Interface) && matchesQuantity(group.Capacity, disk.Capacity) {
				candidates = append(candidates, i)
			}
		}
		if !matchesCount(group.Count, int64(len(candidates))) {
			return false
		}
		count := requiredCount(group.Count)
		// A disk group always requires at least one distinct device when no
		// explicit minimum or exact count is supplied. Fail closed on malformed
		// negative counts and on groups with too few candidates before slicing.
		if count < 0 || int64(len(candidates)) < count {
			return false
		}
		for _, index := range candidates[:count] {
			used[index] = true
		}
	}
	usedInterfaces := make([]bool, len(hardware.Interfaces))
	for _, requirement := range profile.Interfaces {
		found := false
		for i, iface := range hardware.Interfaces {
			if !usedInterfaces[i] && (requirement.Type == "" || requirement.Type == iface.Type) && matchesQuantity(requirement.Speed, iface.Speed) {
				usedInterfaces[i] = true
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func requiredCount(c fleetv1.Constraint) int64 {
	if c.Exact != nil {
		return *c.Exact
	}
	if c.Min != nil {
		return *c.Min
	}
	return 1
}
func matchesCount(c fleetv1.Constraint, value int64) bool {
	if c.Exact != nil {
		return value == *c.Exact
	}
	return (c.Min == nil || value >= *c.Min) && (c.Max == nil || value <= *c.Max)
}
func matchesQuantity(c fleetv1.QuantityConstraint, value string) bool {
	if c.Min == "" && c.Max == "" && c.Exact == "" {
		return true
	}
	v, err := resource.ParseQuantity(value)
	if err != nil {
		return false
	}
	if c.Exact != "" {
		q, err := resource.ParseQuantity(c.Exact)
		return err == nil && v.Cmp(q) == 0
	}
	if c.Min != "" {
		q, err := resource.ParseQuantity(c.Min)
		if err != nil || v.Cmp(q) < 0 {
			return false
		}
	}
	if c.Max != "" {
		q, err := resource.ParseQuantity(c.Max)
		if err != nil || v.Cmp(q) > 0 {
			return false
		}
	}
	return true
}
func contains(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}
