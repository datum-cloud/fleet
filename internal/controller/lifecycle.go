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
	// Installation records gained retained public routing identities in v2.
	// v1 is accepted only by the explicit request-bound upgrader below.
	installationRecordSchemaVersion = 2
	assetRecordPrefix               = "fleet-asset-"
	custodyRecordPrefix             = "fleet-claim-"
	operationPrefix                 = "fleet-install-"
	recordDataKey                   = "record.json"
	recordTypeLabel                 = "fleet.datumapis.com/private-record"
	claimUIDLabel                   = "fleet.datumapis.com/claim-uid"
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
	SchemaVersion   int                `json:"schemaVersion"`
	State           CustodyState       `json:"state"`
	ClaimNamespace  string             `json:"claimNamespace"`
	ClaimName       string             `json:"claimName"`
	ClaimUID        types.UID          `json:"claimUID"`
	NamespaceUID    types.UID          `json:"namespaceUID"`
	Scope           ClaimScope         `json:"scope"`
	Admission       AdmissionAuthority `json:"admission"`
	ConsumerProject ProjectIdentity    `json:"consumerProject,omitempty"`
	ProviderProject ProjectIdentity    `json:"providerProject,omitempty"`
	Authority       AuthorityRef       `json:"authority,omitempty"`
	ExternalHold    ExternalHoldRef    `json:"externalHold,omitempty"`
	AssetID         string             `json:"assetID,omitempty"`
	ProposedAsset   *AssetRecord       `json:"proposedAsset,omitempty"`
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
	AssetPreparing       AssetState = "Preparing"
	AssetCommitted       AssetState = "Committed"
	AssetReleasing       AssetState = "Releasing"
	AssetCleanupVerified AssetState = "CleanupVerified"
	AssetReleased        AssetState = "Released"
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
	ClaimScope           ClaimScope                 `json:"claimScope"`
	ClassUID             types.UID                  `json:"classUID"`
	ProfileUID           types.UID                  `json:"profileUID"`
	LocalClassUID        types.UID                  `json:"localClassUID"`
	LocalProfileUID      types.UID                  `json:"localProfileUID"`
	ConsumerProject      ProjectIdentity            `json:"consumerProject,omitempty"`
	ProviderProject      ProjectIdentity            `json:"providerProject,omitempty"`
	Authority            AuthorityRef               `json:"authority,omitempty"`
	ExternalHold         ExternalHoldRef            `json:"externalHold,omitempty"`
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
	SchemaVersion         int                            `json:"schemaVersion"`
	State                 OperationState                 `json:"state"`
	OperationID           string                         `json:"operationID"`
	InstallationNamespace string                         `json:"installationNamespace"`
	InstallationName      string                         `json:"installationName"`
	InstallationUID       types.UID                      `json:"installationUID"`
	NamespaceUID          types.UID                      `json:"namespaceUID"`
	ClaimNamespace        string                         `json:"claimNamespace"`
	ClaimName             string                         `json:"claimName"`
	ClaimUID              types.UID                      `json:"claimUID"`
	ClaimScope            ClaimScope                     `json:"claimScope"`
	ConsumerProject       ProjectIdentity                `json:"consumerProject,omitempty"`
	AssetID               string                         `json:"assetID"`
	BindingID             string                         `json:"bindingID"`
	Epoch                 int64                          `json:"epoch"`
	Predecessor           string                         `json:"predecessor"`
	ResultRevision        string                         `json:"resultRevision"`
	Image                 ResolvedImage                  `json:"image"`
	ImageName             string                         `json:"imageName"`
	Admission             InstallationAdmissionAuthority `json:"admission"`
	NetworkInputs         *fleetnetwork.Observation      `json:"networkInputs,omitempty"`
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
	LocalClassName     string
	LocalClassUID      types.UID
	LocalProfileName   string
	LocalProfileUID    types.UID
	ConsumerProject    ProjectIdentity
	ProviderProject    ProjectIdentity
	Authority          AuthorityRef
	ExternalHold       ExternalHoldRef
	Location           fleetnetwork.LocationAuthority
	networkPlan        *fleetnetwork.Plan
}

func allocationClaimScope(request AllocationRequest) ClaimScope {
	if request.ConsumerProject.Valid() {
		return ProjectClaimScope(request.ConsumerProject)
	}
	return LocalClaimScope(request.NamespaceUID)
}

// EnsureClaimCustody must be called before a claim finalizer is installed. It
// is idempotent for the exact claim lifetime and rejects deterministic-name
// squatting or reuse with a different identity.
func (c *Coordinator) EnsureClaimCustody(ctx context.Context, claim *fleetv1.MachineClaim, namespaceUID types.UID, admission AdmissionAuthority, project ...ProjectIdentity) error {
	if claim == nil || claim.Namespace == "" || claim.Name == "" || claim.UID == "" || namespaceUID == "" || !admission.valid() {
		return errors.New("claim lifetime identity is required")
	}
	scope := LocalClaimScope(namespaceUID)
	if len(project) > 1 || len(project) == 1 && !project[0].Valid() {
		return ErrRecordInvalid
	}
	if len(project) == 1 {
		scope = ProjectClaimScope(project[0])
	}
	name := custodyRecordName(scope, claim.UID)
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
	record := &ClaimCustodyRecord{SchemaVersion: recordSchemaVersion, State: CustodyUnbound, ClaimNamespace: claim.Namespace, ClaimName: claim.Name, ClaimUID: claim.UID, NamespaceUID: namespaceUID, Scope: scope, Admission: admission}
	secret = corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: c.namespace}}
	if err := encodeRecord(&secret, record, "custody"); err != nil {
		return err
	}
	if err := c.client.Create(ctx, &secret); apierrors.IsAlreadyExists(err) {
		return c.EnsureClaimCustody(ctx, claim, namespaceUID, admission, project...)
	} else {
		return err
	}
}

// RetainClaimAuthority links finalizer custody to a protected authority record
// before any external hold or provider effect is requested.
func (c *Coordinator) RetainClaimAuthority(ctx context.Context, claimUID types.UID, consumer, provider ProjectIdentity, authority AuthorityRef) error {
	if claimUID == "" || !consumer.Valid() || !provider.Valid() || !authority.Valid() {
		return errors.New("exact project and authority lifetimes are required")
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		record, secret, err := c.getCustodyScoped(ctx, ProjectClaimScope(consumer), claimUID)
		if err != nil {
			return err
		}
		if record.Authority.Valid() {
			if record.ConsumerProject == consumer && record.ProviderProject == provider && record.Authority == authority {
				return nil
			}
			return ErrRecordInvalid
		}
		if record.State != CustodyUnbound || record.ExternalHold.Valid() {
			return ErrRecordInvalid
		}
		record.ConsumerProject, record.ProviderProject, record.Authority = consumer, provider, authority
		if err := encodeRecord(secret, record, "custody"); err != nil {
			return err
		}
		return c.client.Update(ctx, secret)
	})
}

// RetainAllocationAuthority attaches cross-project authority and an exact
// external hold to unbound custody before allocation. It is idempotent for the
// same authority and rejects recovery with a substituted project or hold lifetime.
func (c *Coordinator) RetainAllocationAuthority(ctx context.Context, claimUID types.UID, consumer, provider ProjectIdentity, authority AuthorityRef, hold ExternalHoldRef) error {
	if claimUID == "" || !consumer.Valid() || !provider.Valid() || !authority.Valid() || !hold.Valid() {
		return errors.New("exact project, authority, and hold lifetimes are required")
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		record, secret, err := c.getCustodyScoped(ctx, ProjectClaimScope(consumer), claimUID)
		if err != nil {
			return err
		}
		if record.State != CustodyUnbound || record.ExternalHold.Valid() {
			if record.ConsumerProject == consumer && record.ProviderProject == provider && record.Authority == authority && record.ExternalHold == hold {
				return nil
			}
			return ErrRecordInvalid
		}
		if record.ConsumerProject != consumer || record.ProviderProject != provider || record.Authority != authority {
			return ErrRecordInvalid
		}
		record.ExternalHold = hold
		if err := encodeRecord(secret, record, "custody"); err != nil {
			return err
		}
		return c.client.Update(ctx, secret)
	})
}

// ClearSettledExternalHold permits a new allocation attempt only after the
// exact hold owner has acknowledged settlement and custody is still unbound.
func (c *Coordinator) ClearSettledExternalHold(ctx context.Context, claimUID types.UID, authority AuthorityRef, hold ExternalHoldRef, project ...ProjectIdentity) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var record *ClaimCustodyRecord
		var secret *corev1.Secret
		var err error
		if len(project) == 1 && project[0].Valid() {
			record, secret, err = c.getCustodyScoped(ctx, ProjectClaimScope(project[0]), claimUID)
		} else if len(project) == 0 {
			record, secret, err = c.getCustody(ctx, claimUID)
		} else {
			return ErrRecordInvalid
		}
		if err != nil {
			return err
		}
		if record.State != CustodyUnbound || record.AssetID != "" || record.Authority != authority {
			return ErrRecordInvalid
		}
		if !record.ExternalHold.Valid() {
			return nil
		}
		if record.ExternalHold != hold {
			return ErrRecordInvalid
		}
		record.ExternalHold = ExternalHoldRef{}
		if err := encodeRecord(secret, record, "custody"); err != nil {
			return err
		}
		return c.client.Update(ctx, secret)
	})
}

func (c *Coordinator) Allocate(ctx context.Context, request AllocationRequest) (*AssetRecord, error) {
	if err := validateAllocationRequest(request); err != nil {
		return nil, err
	}
	if request.Class.Spec.Source != nil {
		return nil, fmt.Errorf("%w: sourced classes must be resolved by an authority adapter", ErrUnsupported)
	}
	if request.Claim.DeletionTimestamp != nil || !contains(request.Claim.Finalizers, MachineClaimFinalizer) {
		return nil, errors.New("claim must be live and carry the Fleet finalizer before allocation")
	}
	scope := allocationClaimScope(request)
	custody, _, err := c.getCustodyScoped(ctx, scope, request.Claim.UID)
	if err != nil {
		return nil, fmt.Errorf("claim custody must exist before allocation: %w", err)
	}
	if custody.ClaimNamespace != request.Claim.Namespace || custody.ClaimName != request.Claim.Name || custody.NamespaceUID != request.NamespaceUID || custody.Admission != request.Admission || custody.State == CustodyReleased || !allocationAuthorityMatches(custody, request) {
		return nil, fmt.Errorf("%w: claim custody does not authorize allocation", ErrRecordInvalid)
	}
	existing, err := c.recordsForClaimScoped(ctx, request.Claim.UID, custody.ConsumerProject)
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
			if err := c.bindCustody(ctx, request.Claim, record.AssetID, scope); err != nil {
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
		candidateID := machineAssetID(machine, request.ProviderProject)
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
			if err := c.selectCustody(ctx, request, proposal); err != nil {
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
				if err := c.clearSelection(ctx, request.Claim.UID, candidateID, scope); err != nil {
					return nil, err
				}
				custody.State, custody.AssetID = CustodyUnbound, ""
			}
			continue
		}
		if err := c.bindCustody(ctx, request.Claim, prepared.AssetID, scope); err != nil {
			return nil, err
		}
		return c.commitPrepared(ctx, prepared.AssetID, request.Claim.UID)
	}
	return nil, ErrInsufficientCapacity
}

func machineAssetID(machine *fleetv1.Machine, provider ProjectIdentity) string {
	asset := fmt.Sprintf("%s/%s/%s/%s", machine.Namespace, machine.Spec.ProviderRef.APIGroup, machine.Spec.ProviderRef.Kind, machine.Spec.ProviderRef.Name)
	if provider.Valid() {
		return fmt.Sprintf("%s/%s/%s", provider.AuthorityID, provider.UID, asset)
	}
	return asset
}

func (c *Coordinator) recordCandidateEligible(record *AssetRecord, request AllocationRequest) bool {
	selector, err := metav1.LabelSelectorAsSelector(&request.Class.Spec.Capacity.Machines.Selector)
	if err != nil {
		return false
	}
	for i := range request.Machines {
		m := &request.Machines[i]
		if machineAssetID(m, request.ProviderProject) == record.AssetID && m.UID == record.MachineUID && m.Namespace == record.MachineNamespace && m.Name == record.MachineName && m.Namespace == request.Class.Spec.Capacity.Machines.Namespace && selector.Matches(labels.Set(m.Labels)) && machineEligible(m, request.EvidenceObservedAt[m.UID], c.now(), c.maxEvidenceAge) && matches(request.Profile.Spec, m.Status.Hardware) {
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
	if record.ClaimNamespace != request.Claim.Namespace || record.ClaimName != request.Claim.Name || record.ClaimUID != request.Claim.UID || record.ClassUID != request.Class.UID || record.ProfileUID != request.Profile.UID || record.LocalClassUID != localClassUID(request) || record.LocalProfileUID != localProfileUID(request) || record.ConsumerProject != request.ConsumerProject || record.ProviderProject != request.ProviderProject || record.Authority != request.Authority || record.ExternalHold != request.ExternalHold || string(got) != string(want) {
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

func validateCapacityRequest(request AllocationRequest) error {
	if request.Claim == nil || request.Class == nil || request.Profile == nil || request.Class.Spec.Capacity == nil {
		return errors.New("claim, class, profile, and class capacity are required")
	}
	if request.Claim.UID == "" || request.Class.UID == "" || request.Profile.UID == "" || request.Claim.Namespace == "" || request.Claim.Name == "" || request.NamespaceUID == "" || !request.Admission.valid() {
		return errors.New("claim, class, and profile lifetime identities are required")
	}
	className, profileName := request.LocalClassName, request.LocalProfileName
	if className == "" {
		className = request.Class.Name
	}
	if profileName == "" {
		profileName = request.Profile.Name
	}
	if className != request.Claim.Spec.MachineClassRef.Name || profileName != request.Claim.Spec.MachineProfileRef.Name {
		return errors.New("resolved class or profile does not match the claim")
	}
	return nil
}

func validateAllocationRequest(request AllocationRequest) error {
	if err := validateCapacityRequest(request); err != nil {
		return err
	}
	projectAuthority := request.ConsumerProject.Valid() || request.ProviderProject.Valid() || request.Authority.Valid() || request.ExternalHold.Valid()
	if projectAuthority && (!request.ConsumerProject.Valid() || !request.ProviderProject.Valid() || !request.Authority.Valid() || !request.ExternalHold.Valid() || localClassUID(request) == "" || localProfileUID(request) == "") {
		return errors.New("cross-project allocation requires exact project, source, authority, and hold lifetimes")
	}
	return nil
}

func localClassUID(request AllocationRequest) types.UID {
	if request.LocalClassUID != "" {
		return request.LocalClassUID
	}
	return request.Class.UID
}

func localProfileUID(request AllocationRequest) types.UID {
	if request.LocalProfileUID != "" {
		return request.LocalProfileUID
	}
	return request.Profile.UID
}

func allocationAuthorityMatches(custody *ClaimCustodyRecord, request AllocationRequest) bool {
	return custody.ConsumerProject == request.ConsumerProject && custody.ProviderProject == request.ProviderProject && custody.Authority == request.Authority && custody.ExternalHold == request.ExternalHold
}

// CheckCapacity performs the same current hardware selection test as Allocate
// without retaining selection or authorizing effects. Allocate always rechecks.
func (c *Coordinator) CheckCapacity(request AllocationRequest) error {
	if err := validateCapacityRequest(request); err != nil {
		return err
	}
	selector, err := metav1.LabelSelectorAsSelector(&request.Class.Spec.Capacity.Machines.Selector)
	if err != nil {
		return fmt.Errorf("invalid MachineClass selector: %w", err)
	}
	for i := range request.Machines {
		machine := &request.Machines[i]
		if machine.Namespace == request.Class.Spec.Capacity.Machines.Namespace && selector.Matches(labels.Set(machine.Labels)) && machineEligible(machine, request.EvidenceObservedAt[machine.UID], c.now(), c.maxEvidenceAge) && matches(request.Profile.Spec, machine.Status.Hardware) {
			return nil
		}
	}
	return ErrInsufficientCapacity
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
	assetID := machineAssetID(machine, request.ProviderProject)
	custody, _, err := c.getCustodyScoped(ctx, allocationClaimScope(request), request.Claim.UID)
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
		previousCustody, _, custodyErr := c.getCustodyForAsset(ctx, existing)
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
	assetID := machineAssetID(machine, request.ProviderProject)
	epoch := int64(1)
	existing, _, err := c.getAsset(ctx, assetID)
	if err == nil {
		if existing.State != AssetReleased {
			return nil, false, nil
		}
		previous, _, err := c.getCustodyForAsset(ctx, existing)
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
		ClaimNamespace: request.Claim.Namespace, ClaimName: request.Claim.Name, ClaimUID: request.Claim.UID, ClaimScope: allocationClaimScope(request),
		ClassUID: request.Class.UID, ProfileUID: request.Profile.UID, LocalClassUID: localClassUID(request), LocalProfileUID: localProfileUID(request),
		ConsumerProject: request.ConsumerProject, ProviderProject: request.ProviderProject, Authority: request.Authority, ExternalHold: request.ExternalHold,
		BindingID: bindingID, Epoch: epoch,
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
	return c.reconcileNetwork(ctx, ProjectIdentity{}, claimUID)
}

func (c *Coordinator) ReconcileNetworkScoped(ctx context.Context, consumer ProjectIdentity, claimUID types.UID) (fleetnetwork.Observation, error) {
	if !consumer.Valid() {
		return fleetnetwork.Observation{}, ErrRecordInvalid
	}
	return c.reconcileNetwork(ctx, consumer, claimUID)
}

func (c *Coordinator) reconcileNetwork(ctx context.Context, consumer ProjectIdentity, claimUID types.UID) (fleetnetwork.Observation, error) {
	if c.networks == nil || claimUID == "" {
		return fleetnetwork.Observation{}, fleetnetwork.ErrAuthorizationUnavailable
	}
	records, err := c.recordsForClaimScoped(ctx, claimUID, consumer)
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
			current.Network.Observation = observation
			current.Network.Lifecycle = append([]fleetnetwork.InterfaceLifecycleState(nil), observation.Lifecycle...)
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
	State                      AssetState
	SafeToRemoveFinalizer      bool
	ExternalSettlementRequired bool
}

func (c *Coordinator) Release(ctx context.Context, claimUID types.UID) (ReleaseResult, error) {
	return c.release(ctx, ClaimScope{}, claimUID)
}

func (c *Coordinator) ReleaseScoped(ctx context.Context, consumer ProjectIdentity, claimUID types.UID) (ReleaseResult, error) {
	if !consumer.Valid() {
		return ReleaseResult{}, ErrRecordInvalid
	}
	return c.release(ctx, ProjectClaimScope(consumer), claimUID)
}

func (c *Coordinator) release(ctx context.Context, scope ClaimScope, claimUID types.UID) (ReleaseResult, error) {
	if claimUID == "" {
		return ReleaseResult{}, errors.New("claim UID is required")
	}
	var custody *ClaimCustodyRecord
	var err error
	if scope.Valid() {
		custody, _, err = c.getCustodyScoped(ctx, scope, claimUID)
	} else {
		custody, _, err = c.getCustody(ctx, claimUID)
	}
	if err != nil {
		return ReleaseResult{}, fmt.Errorf("%w: claim custody is missing or invalid: %v", ErrRecordInvalid, err)
	}
	records, err := c.recordsForClaimScoped(ctx, claimUID, custody.ConsumerProject)
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
			updated, _, err := c.getCustodyScoped(ctx, custody.Scope, claimUID)
			if err != nil {
				return ReleaseResult{}, err
			}
			if updated.State == CustodyReleased {
				return ReleaseResult{State: AssetReleased, SafeToRemoveFinalizer: true}, nil
			}
			return c.release(ctx, custody.Scope, claimUID)
		}
		if custody.State != CustodyUnbound || custody.AssetID != "" {
			return ReleaseResult{}, fmt.Errorf("%w: bound custody has no asset authority", ErrRecordInvalid)
		}
		if custody.ExternalHold.Valid() {
			return ReleaseResult{State: AssetCleanupVerified, ExternalSettlementRequired: true}, nil
		}
		if err := c.releaseCustody(ctx, claimUID, "", custody.Scope); err != nil {
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
		if err := c.bindCustody(ctx, claim, record.AssetID, custody.Scope); err != nil {
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
		return c.release(ctx, custody.Scope, claimUID)
	}
	if record.State == AssetReleased {
		if err := c.releaseCustody(ctx, claimUID, record.AssetID, custody.Scope); err != nil {
			return ReleaseResult{}, err
		}
		return ReleaseResult{State: AssetReleased, SafeToRemoveFinalizer: true}, nil
	}
	if record.State == AssetCleanupVerified {
		return ReleaseResult{State: AssetCleanupVerified, ExternalSettlementRequired: true}, nil
	}
	if record.State == AssetPreparing {
		if operations, err := c.operationsForBinding(ctx, record.BindingID); err != nil {
			return ReleaseResult{}, err
		} else if len(operations) != 0 {
			return ReleaseResult{}, fmt.Errorf("%w: preparing binding has retained operations", ErrRecordInvalid)
		}
		target := AssetReleased
		if record.ExternalHold.Valid() {
			target = AssetCleanupVerified
		}
		if _, err := c.setAssetState(ctx, record.AssetID, claimUID, AssetPreparing, target); err != nil {
			return ReleaseResult{}, err
		}
		if target == AssetCleanupVerified {
			return ReleaseResult{State: target, ExternalSettlementRequired: true}, nil
		}
		if err := c.releaseCustody(ctx, claimUID, record.AssetID, custody.Scope); err != nil {
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
	target := AssetReleased
	if record.ExternalHold.Valid() {
		target = AssetCleanupVerified
	}
	if _, err := c.setAssetState(ctx, record.AssetID, claimUID, AssetReleasing, target); err != nil {
		return ReleaseResult{}, err
	}
	if target == AssetCleanupVerified {
		return ReleaseResult{State: target, ExternalSettlementRequired: true}, nil
	}
	if err := c.releaseCustody(ctx, claimUID, record.AssetID, custody.Scope); err != nil {
		return ReleaseResult{}, err
	}
	return ReleaseResult{State: AssetReleased, SafeToRemoveFinalizer: true}, nil
}

// CompleteExternalRelease consumes an exact external settlement acknowledgment.
// Object absence is not an acknowledgment and this method never queries the
// external owner; callers must supply the retained authority and hold identities.
func (c *Coordinator) CompleteExternalRelease(ctx context.Context, claimUID types.UID, authority AuthorityRef, hold ExternalHoldRef, project ...ProjectIdentity) (ReleaseResult, error) {
	if claimUID == "" || !authority.Valid() || !hold.Valid() {
		return ReleaseResult{}, ErrRecordInvalid
	}
	var custody *ClaimCustodyRecord
	var err error
	if len(project) == 1 && project[0].Valid() {
		custody, _, err = c.getCustodyScoped(ctx, ProjectClaimScope(project[0]), claimUID)
	} else if len(project) == 0 {
		custody, _, err = c.getCustody(ctx, claimUID)
	} else {
		return ReleaseResult{}, ErrRecordInvalid
	}
	if err != nil || custody.Authority != authority || custody.ExternalHold != hold {
		return ReleaseResult{}, ErrRecordInvalid
	}
	records, err := c.recordsForClaimScoped(ctx, claimUID, custody.ConsumerProject)
	if err != nil {
		return ReleaseResult{}, err
	}
	if len(records) == 0 {
		if custody.State != CustodyUnbound || custody.AssetID != "" {
			return ReleaseResult{}, ErrRecordInvalid
		}
		if err := c.releaseCustody(ctx, claimUID, "", custody.Scope); err != nil {
			return ReleaseResult{}, err
		}
		return ReleaseResult{State: AssetReleased, SafeToRemoveFinalizer: true}, nil
	}
	if len(records) != 1 || records[0].State != AssetCleanupVerified || records[0].Authority != authority || records[0].ExternalHold != hold {
		return ReleaseResult{}, ErrRecordInvalid
	}
	if _, err := c.setAssetState(ctx, records[0].AssetID, claimUID, AssetCleanupVerified, AssetReleased); err != nil {
		return ReleaseResult{}, err
	}
	if err := c.releaseCustody(ctx, claimUID, records[0].AssetID, custody.Scope); err != nil {
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
		operation, err = c.markOperationSucceeded(ctx, operationRecordName(operation.ClaimScope, operation.ClaimUID, operation.Predecessor), operation)
		if err != nil {
			return err
		}
	}
	return c.publishInstalledRevision(ctx, asset.AssetID, asset.ClaimUID, operation)
}

type InstallationRequest struct {
	Claim           *fleetv1.MachineClaim
	Installation    *fleetv1.Installation
	NamespaceUID    types.UID
	ClaimScope      ClaimScope
	ConsumerProject ProjectIdentity
	Admission       InstallationAdmissionAuthority
}

func installationClaimScope(request InstallationRequest) (ClaimScope, error) {
	want := LocalClaimScope(request.NamespaceUID)
	if request.ConsumerProject.Valid() {
		want = ProjectClaimScope(request.ConsumerProject)
	}
	if request.ClaimScope.Valid() && request.ClaimScope != want {
		return ClaimScope{}, ErrRecordInvalid
	}
	return want, nil
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
	scope, err := installationClaimScope(request)
	if err != nil {
		return InstallationResult{}, err
	}
	assets, err := c.recordsForClaimScoped(ctx, request.Claim.UID, request.ConsumerProject)
	if err != nil {
		return InstallationResult{}, err
	}
	if len(assets) != 1 {
		return InstallationResult{}, fmt.Errorf("%w: claim does not have one retained asset", ErrRecordInvalid)
	}
	asset := assets[0]
	if asset.State != AssetCommitted || asset.ClaimNamespace != request.Claim.Namespace || asset.ClaimName != request.Claim.Name || asset.ClaimUID != request.Claim.UID || asset.ClaimScope != scope || asset.ConsumerProject != request.ConsumerProject {
		return InstallationResult{}, ErrPredecessorConflict
	}
	custody, _, err := c.getCustodyScoped(ctx, scope, request.Claim.UID)
	if err != nil || custody.State != CustodyBound || custody.AssetID != asset.AssetID || custody.ClaimNamespace != request.Claim.Namespace || custody.ClaimName != request.Claim.Name || custody.ClaimUID != request.Claim.UID || custody.Scope != scope || custody.NamespaceUID != request.NamespaceUID || custody.Admission.PrincipalUID != request.Admission.PrincipalUID || custody.ConsumerProject != asset.ConsumerProject || custody.ProviderProject != asset.ProviderProject || custody.Authority != asset.Authority || custody.ExternalHold != asset.ExternalHold {
		return InstallationResult{}, ErrRecordInvalid
	}
	operationName := operationRecordName(scope, request.Claim.UID, predecessor)
	operation, _, err := c.getOperation(ctx, operationName)
	if errors.Is(err, ErrRecordInvalid) {
		operation, err = c.upgradeLegacyOperation(ctx, operationName, request, asset, scope, predecessor)
	}
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
	if operation.InstallationNamespace != request.Installation.Namespace || operation.InstallationName != request.Installation.Name || operation.InstallationUID != request.Installation.UID || operation.NamespaceUID != request.NamespaceUID || operation.ClaimNamespace != request.Claim.Namespace || operation.ClaimName != request.Claim.Name || operation.ClaimUID != request.Claim.UID || operation.ClaimScope != scope || operation.ConsumerProject != request.ConsumerProject || operation.AssetID != asset.AssetID || operation.BindingID != asset.BindingID || operation.Epoch != asset.Epoch || operation.Predecessor != predecessor || operation.ImageName != request.Installation.Spec.ImageRef.Name || operation.Admission != request.Admission {
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
	scope, scopeErr := installationClaimScope(request)
	if scopeErr != nil {
		return nil, nil, scopeErr
	}
	record := &InstallationRecord{SchemaVersion: installationRecordSchemaVersion, State: OperationAdmitted, OperationID: "install-" + operationID, InstallationNamespace: request.Installation.Namespace, InstallationName: request.Installation.Name, InstallationUID: request.Installation.UID, NamespaceUID: request.NamespaceUID, ClaimNamespace: request.Claim.Namespace, ClaimName: request.Claim.Name, ClaimUID: request.Claim.UID, ClaimScope: scope, ConsumerProject: request.ConsumerProject, AssetID: asset.AssetID, BindingID: asset.BindingID, Epoch: asset.Epoch, Predecessor: predecessor, ResultRevision: "ir-" + revisionID, Image: image, ImageName: request.Installation.Spec.ImageRef.Name, Admission: request.Admission}
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

// upgradeLegacyOperation upgrades a v1 operation only while an exact public
// request is available to supply the names that v1 failed to retain. A v1
// record without that request remains quarantined: guessing a name from a UID
// would retarget admitted provider authority.
func (c *Coordinator) upgradeLegacyOperation(ctx context.Context, name string, request InstallationRequest, asset *AssetRecord, scope ClaimScope, predecessor string) (*InstallationRecord, error) {
	var result *InstallationRecord
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var secret corev1.Secret
		if err := c.client.Get(ctx, client.ObjectKey{Namespace: c.namespace, Name: name}, &secret); err != nil {
			return err
		}
		if current, err := decodeOperationSecret(&secret); err == nil {
			result = current
			return nil
		}
		legacy, err := decodeLegacyOperationSecret(&secret)
		if err != nil {
			return err
		}
		if legacy.InstallationUID != request.Installation.UID || legacy.NamespaceUID != request.NamespaceUID || legacy.ClaimUID != request.Claim.UID || legacy.ClaimScope != scope || legacy.ConsumerProject != request.ConsumerProject || legacy.AssetID != asset.AssetID || legacy.BindingID != asset.BindingID || legacy.Epoch != asset.Epoch || legacy.Predecessor != predecessor || legacy.ImageName != request.Installation.Spec.ImageRef.Name || legacy.Admission != request.Admission {
			return ErrPredecessorConflict
		}
		legacy.SchemaVersion = installationRecordSchemaVersion
		legacy.InstallationNamespace = request.Installation.Namespace
		legacy.InstallationName = request.Installation.Name
		legacy.ClaimNamespace = request.Claim.Namespace
		legacy.ClaimName = request.Claim.Name
		if err := encodeRecord(&secret, legacy, "installation"); err != nil {
			return err
		}
		if err := c.client.Update(ctx, &secret); err != nil {
			return err
		}
		result = legacy
		return nil
	})
	return result, err
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
	var list corev1.SecretList
	if err := c.client.List(ctx, &list, client.InNamespace(c.namespace), client.MatchingLabels{recordTypeLabel: "custody", claimUIDLabel: string(claimUID)}); err != nil {
		return nil, nil, err
	}
	if len(list.Items) != 1 {
		return nil, nil, ErrRecordInvalid
	}
	record, err := decodeCustodySecret(&list.Items[0])
	return record, &list.Items[0], err
}

func (c *Coordinator) getCustodyScoped(ctx context.Context, scope ClaimScope, claimUID types.UID) (*ClaimCustodyRecord, *corev1.Secret, error) {
	if !scope.Valid() || claimUID == "" {
		return nil, nil, ErrRecordInvalid
	}
	var secret corev1.Secret
	if err := c.client.Get(ctx, client.ObjectKey{Namespace: c.namespace, Name: custodyRecordName(scope, claimUID)}, &secret); err != nil {
		return nil, nil, err
	}
	record, err := decodeCustodySecret(&secret)
	return record, &secret, err
}

// HasClaimCustody reports whether the exact project-scoped claim lifetime has
// any retained custody record. It is used only to distinguish a genuinely
// never-admitted claim from authority-record loss; a present record never
// authorizes cleanup bypass.
func (c *Coordinator) HasClaimCustody(ctx context.Context, consumer ProjectIdentity, claimUID types.UID) (bool, error) {
	if !consumer.Valid() || claimUID == "" {
		return false, ErrRecordInvalid
	}
	_, _, err := c.getCustodyScoped(ctx, ProjectClaimScope(consumer), claimUID)
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (c *Coordinator) getCustodyForAsset(ctx context.Context, asset *AssetRecord) (*ClaimCustodyRecord, *corev1.Secret, error) {
	return c.getCustodyScoped(ctx, asset.ClaimScope, asset.ClaimUID)
}

func (c *Coordinator) bindCustody(ctx context.Context, claim *fleetv1.MachineClaim, assetID string, scopes ...ClaimScope) error {
	var record *ClaimCustodyRecord
	var secret *corev1.Secret
	var err error
	if len(scopes) == 1 {
		record, secret, err = c.getCustodyScoped(ctx, scopes[0], claim.UID)
	} else {
		record, secret, err = c.getCustody(ctx, claim.UID)
	}
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

func (c *Coordinator) selectCustody(ctx context.Context, request AllocationRequest, proposal *AssetRecord) error {
	claim := request.Claim
	if proposal == nil || proposal.ClaimUID != claim.UID || validateAssetRecord(proposal) != nil {
		return ErrRecordInvalid
	}
	record, secret, err := c.getCustodyScoped(ctx, allocationClaimScope(request), claim.UID)
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
			return c.releaseCustody(ctx, custody.ClaimUID, custody.AssetID, custody.Scope)
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

func (c *Coordinator) clearSelection(ctx context.Context, claimUID types.UID, assetID string, scopes ...ClaimScope) error {
	var record *ClaimCustodyRecord
	var secret *corev1.Secret
	var err error
	if len(scopes) == 1 {
		record, secret, err = c.getCustodyScoped(ctx, scopes[0], claimUID)
	} else {
		record, secret, err = c.getCustody(ctx, claimUID)
	}
	if err != nil {
		return err
	}
	records, err := c.recordsForClaimScoped(ctx, claimUID, record.ConsumerProject)
	if err != nil {
		return err
	}
	if len(records) != 0 {
		return fmt.Errorf("%w: cannot clear selection with retained asset authority", ErrRecordInvalid)
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

func (c *Coordinator) releaseCustody(ctx context.Context, claimUID types.UID, assetID string, scopes ...ClaimScope) error {
	var record *ClaimCustodyRecord
	var secret *corev1.Secret
	var err error
	if len(scopes) == 1 {
		record, secret, err = c.getCustodyScoped(ctx, scopes[0], claimUID)
	} else {
		record, secret, err = c.getCustody(ctx, claimUID)
	}
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
	return c.recordsForClaimScoped(ctx, claimUID, ProjectIdentity{})
}

func (c *Coordinator) recordsForClaimScoped(ctx context.Context, claimUID types.UID, consumer ProjectIdentity) ([]*AssetRecord, error) {
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
		if record.ClaimUID == claimUID && (consumer.Valid() && record.ConsumerProject == consumer || !consumer.Valid() && !record.ConsumerProject.Valid()) {
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
	if secret.Type != corev1.SecretType("fleet.datumapis.com/private-lifecycle") || secret.Labels[recordTypeLabel] != "custody" || json.Unmarshal(secret.Data[recordDataKey], &record) != nil || validateCustodyRecord(&record) != nil || secret.Name != custodyRecordName(record.Scope, record.ClaimUID) || secret.Labels[claimUIDLabel] != string(record.ClaimUID) {
		return nil, fmt.Errorf("%w: custody secret %s/%s", ErrRecordInvalid, secret.Namespace, secret.Name)
	}
	return &record, nil
}

func validateCustodyRecord(r *ClaimCustodyRecord) error {
	if r.SchemaVersion != recordSchemaVersion || r.ClaimNamespace == "" || r.ClaimName == "" || r.ClaimUID == "" || r.NamespaceUID == "" || !r.Scope.Valid() || !r.Admission.valid() {
		return ErrRecordInvalid
	}
	projectAuthority := r.ConsumerProject.Valid() || r.ProviderProject.Valid() || r.Authority.Valid() || r.ExternalHold.Valid()
	if projectAuthority && (!r.ConsumerProject.Valid() || !r.ProviderProject.Valid() || !r.Authority.Valid()) {
		return ErrRecordInvalid
	}
	if r.ConsumerProject.Valid() && r.Scope != ProjectClaimScope(r.ConsumerProject) {
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
	if secret.Type != corev1.SecretType("fleet.datumapis.com/private-lifecycle") || secret.Labels[recordTypeLabel] != "installation" || json.Unmarshal(secret.Data[recordDataKey], &record) != nil || validateInstallationRecord(&record) != nil || secret.Name != operationRecordName(record.ClaimScope, record.ClaimUID, record.Predecessor) || secret.Labels[claimUIDLabel] != string(record.ClaimUID) {
		return nil, fmt.Errorf("%w: installation secret %s/%s", ErrRecordInvalid, secret.Namespace, secret.Name)
	}
	return &record, nil
}

// PrivateRecordRoute is the minimal routing identity exposed by the lifecycle
// record owner after the complete persisted record and deterministic Secret
// identity have been validated.
type PrivateRecordRoute struct {
	RecordType            string
	ClaimNamespace        string
	ClaimName             string
	ClaimUID              types.UID
	InstallationNamespace string
	InstallationName      string
	InstallationUID       types.UID
	ConsumerProject       ProjectIdentity
	NamespaceUIDs         []types.UID
	MachineUIDs           []types.UID
	NetworkUIDs           []types.UID
	NICClaimUIDs          []types.UID
	InterfaceUIDs         []types.UID
	AuthorityRecordIDs    []string
	QuotaUIDs             []types.UID
	EntitlementUIDs       []types.UID
	PendingEntitlement    bool
}

func DecodePrivateLifecycleRoute(secret *corev1.Secret) (PrivateRecordRoute, error) {
	if secret == nil || secret.Type != corev1.SecretType("fleet.datumapis.com/private-lifecycle") {
		return PrivateRecordRoute{}, ErrRecordInvalid
	}
	switch secret.Labels[recordTypeLabel] {
	case "custody":
		record, err := decodeCustodySecret(secret)
		if err != nil {
			return PrivateRecordRoute{}, err
		}
		route := PrivateRecordRoute{RecordType: "custody", ClaimNamespace: record.ClaimNamespace, ClaimName: record.ClaimName, ClaimUID: record.ClaimUID, ConsumerProject: record.ConsumerProject, NamespaceUIDs: []types.UID{record.NamespaceUID}}
		appendAuthorityIndexes(&route, record.Authority, record.ExternalHold)
		if record.ProposedAsset != nil {
			appendAssetIndexes(&route, record.ProposedAsset)
		}
		return route, nil
	case "asset":
		record, err := decodeAssetSecret(secret)
		if err != nil {
			return PrivateRecordRoute{}, err
		}
		route := PrivateRecordRoute{RecordType: "asset", ClaimNamespace: record.ClaimNamespace, ClaimName: record.ClaimName, ClaimUID: record.ClaimUID, ConsumerProject: record.ConsumerProject}
		appendAssetIndexes(&route, record)
		return route, nil
	case "installation":
		record, err := decodeOperationSecret(secret)
		if err != nil {
			return PrivateRecordRoute{}, err
		}
		route := PrivateRecordRoute{RecordType: "installation", ClaimNamespace: record.ClaimNamespace, ClaimName: record.ClaimName, ClaimUID: record.ClaimUID, InstallationNamespace: record.InstallationNamespace, InstallationName: record.InstallationName, InstallationUID: record.InstallationUID, ConsumerProject: record.ConsumerProject, NamespaceUIDs: []types.UID{record.NamespaceUID}}
		if record.NetworkInputs != nil {
			appendObservationIndexes(&route, *record.NetworkInputs)
		}
		return route, nil
	default:
		return PrivateRecordRoute{}, ErrRecordInvalid
	}
}

func appendAuthorityIndexes(route *PrivateRecordRoute, authority AuthorityRef, hold ExternalHoldRef) {
	if authority.Valid() {
		route.AuthorityRecordIDs = append(route.AuthorityRecordIDs, authority.RecordID)
	}
	if hold.Valid() {
		route.QuotaUIDs = append(route.QuotaUIDs, hold.UID)
	}
}

func appendAssetIndexes(route *PrivateRecordRoute, record *AssetRecord) {
	route.MachineUIDs = append(route.MachineUIDs, record.MachineUID)
	appendAuthorityIndexes(route, record.Authority, record.ExternalHold)
	if record.Network == nil {
		return
	}
	for _, iface := range record.Network.Plan.Interfaces {
		route.NetworkUIDs = append(route.NetworkUIDs, iface.NetworkUID)
	}
	appendObservationIndexes(route, record.Network.Observation)
	for _, state := range record.Network.Lifecycle {
		if state.CreatedUID != "" {
			route.NICClaimUIDs = append(route.NICClaimUIDs, state.CreatedUID)
		}
		if state.RetainedInterfaceUID != "" {
			route.InterfaceUIDs = append(route.InterfaceUIDs, state.RetainedInterfaceUID)
		}
	}
}

func appendObservationIndexes(route *PrivateRecordRoute, observation fleetnetwork.Observation) {
	for _, iface := range observation.Interfaces {
		if iface.ClaimUID != "" {
			route.NICClaimUIDs = append(route.NICClaimUIDs, iface.ClaimUID)
		}
		if iface.NetworkInterfaceUID != "" {
			route.InterfaceUIDs = append(route.InterfaceUIDs, iface.NetworkInterfaceUID)
		}
	}
}

func decodeLegacyOperationSecret(secret *corev1.Secret) (*InstallationRecord, error) {
	var record InstallationRecord
	if secret.Type != corev1.SecretType("fleet.datumapis.com/private-lifecycle") || secret.Labels[recordTypeLabel] != "installation" || json.Unmarshal(secret.Data[recordDataKey], &record) != nil || validateLegacyInstallationRecord(&record) != nil || secret.Name != operationRecordName(record.ClaimScope, record.ClaimUID, record.Predecessor) || secret.Labels[claimUIDLabel] != string(record.ClaimUID) {
		return nil, fmt.Errorf("%w: legacy installation secret %s/%s", ErrRecordInvalid, secret.Namespace, secret.Name)
	}
	return &record, nil
}

func validateAssetRecord(r *AssetRecord) error {
	if r.SchemaVersion != recordSchemaVersion || r.AssetID == "" || r.MachineNamespace == "" || r.MachineName == "" || r.MachineUID == "" || r.ClaimNamespace == "" || r.ClaimName == "" || r.ClaimUID == "" || !r.ClaimScope.Valid() || r.ClassUID == "" || r.ProfileUID == "" || r.LocalClassUID == "" || r.LocalProfileUID == "" || r.BindingID == "" || r.Epoch < 1 || r.FenceToken == "" || r.CurrentRevision == "" {
		return ErrRecordInvalid
	}
	projectAuthority := r.ConsumerProject.Valid() || r.ProviderProject.Valid() || r.Authority.Valid() || r.ExternalHold.Valid()
	if projectAuthority && (!r.ConsumerProject.Valid() || !r.ProviderProject.Valid() || !r.Authority.Valid() || !r.ExternalHold.Valid()) {
		return ErrRecordInvalid
	}
	if r.ConsumerProject.Valid() && r.ClaimScope != ProjectClaimScope(r.ConsumerProject) {
		return ErrRecordInvalid
	}
	switch r.State {
	case AssetPreparing, AssetCommitted, AssetReleasing, AssetCleanupVerified, AssetReleased:
	default:
		return ErrRecordInvalid
	}
	if (r.State == AssetPreparing || r.State == AssetReleasing || r.State == AssetCleanupVerified || r.State == AssetReleased) && r.ActiveOperationID != "" {
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
	if r.SchemaVersion != installationRecordSchemaVersion || !strings.HasPrefix(r.OperationID, "install-") || r.InstallationNamespace == "" || r.InstallationName == "" || r.InstallationUID == "" || r.NamespaceUID == "" || r.ClaimNamespace == "" || r.ClaimName == "" || r.ClaimUID == "" || !r.ClaimScope.Valid() || r.AssetID == "" || r.BindingID == "" || r.Epoch < 1 || r.Predecessor == "" || !strings.HasPrefix(r.ResultRevision, "ir-") || r.ImageName == "" || !r.Admission.valid() || r.Image.Validate() != nil || r.Image.CatalogAuthority != r.Admission.CatalogAuthority || r.Image.CatalogRevision != r.Admission.CatalogRevision || r.Image.ImageDecisionID != r.Admission.ImageDecisionID {
		return ErrRecordInvalid
	}
	if r.InstallationNamespace != r.ClaimNamespace {
		return ErrRecordInvalid
	}
	if r.ConsumerProject.Valid() && r.ClaimScope != ProjectClaimScope(r.ConsumerProject) || !r.ConsumerProject.Valid() && r.ClaimScope != LocalClaimScope(r.NamespaceUID) {
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

func validateLegacyInstallationRecord(r *InstallationRecord) error {
	if r.SchemaVersion != 1 || r.InstallationNamespace != "" || r.InstallationName != "" || r.ClaimNamespace != "" || r.ClaimName != "" || !strings.HasPrefix(r.OperationID, "install-") || r.InstallationUID == "" || r.NamespaceUID == "" || r.ClaimUID == "" || !r.ClaimScope.Valid() || r.AssetID == "" || r.BindingID == "" || r.Epoch < 1 || r.Predecessor == "" || !strings.HasPrefix(r.ResultRevision, "ir-") || r.ImageName == "" || !r.Admission.valid() || r.Image.Validate() != nil || r.Image.CatalogAuthority != r.Admission.CatalogAuthority || r.Image.CatalogRevision != r.Admission.CatalogRevision || r.Image.ImageDecisionID != r.Admission.ImageDecisionID {
		return ErrRecordInvalid
	}
	if r.ConsumerProject.Valid() && r.ClaimScope != ProjectClaimScope(r.ConsumerProject) || !r.ConsumerProject.Valid() && r.ClaimScope != LocalClaimScope(r.NamespaceUID) {
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
func custodyRecordName(scope ClaimScope, claimUID types.UID) string {
	return custodyRecordPrefix + shortHash(scope.AuthorityID+"/"+string(scope.ProjectUID)+"/"+string(claimUID))
}
func operationRecordName(scope ClaimScope, claimUID types.UID, predecessor string) string {
	return operationPrefix + shortHash(scope.AuthorityID+"/"+string(scope.ProjectUID)+"/"+string(claimUID)+"/"+predecessor)
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
