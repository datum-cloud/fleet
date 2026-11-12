package milo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
	fleetnetwork "go.datum.net/fleet/internal/network"
)

var (
	ErrAuthorityUnavailable      = errors.New("caller-preserving source authority is unavailable")
	ErrAdmissionFenceUnavailable = errors.New("atomic admission fence is unavailable")
	ErrQuotaCustodyUnavailable   = errors.New("protected quota custody is unavailable")
	ErrProjectLifetimeMismatch   = errors.New("project lifetime does not match retained authority")
	ErrAuthorityInvalid          = errors.New("retained Milo authority is invalid")
	ErrEntitlementUnavailable    = errors.New("current Fleet entitlement is unavailable")
	ErrQuotaPending              = errors.New("Milo quota grant is pending")
	ErrQuotaUnknown              = errors.New("Milo quota outcome is unknown")
	ErrQuotaReleaseUnknown       = errors.New("Milo quota release is not acknowledged")
	ErrClaimAuthorityNotFound    = errors.New("claim has no Milo authority or lifecycle custody")
)

const (
	ServiceName          = "fleet.datumapis.com"
	EntitlementName      = "fleet"
	MachineQuotaResource = "fleet.datumapis.com/machines"
)

type ProjectResolver interface {
	Resolve(context.Context, string) (controller.ProjectIdentity, client.Client, error)
	Verify(context.Context, controller.ProjectIdentity) error
}

type ProfileKind string

const (
	ProfileCustom    ProfileKind = "Custom"
	ProfilePublished ProfileKind = "Published"
)

type SourceUseRequest struct {
	ConsumerProject     controller.ProjectIdentity     `json:"consumerProject"`
	ProviderProject     controller.ProjectIdentity     `json:"providerProject"`
	NamespaceUID        types.UID                      `json:"namespaceUID"`
	ClaimUID            types.UID                      `json:"claimUID"`
	LocalClassUID       types.UID                      `json:"localClassUID"`
	SourceClassUID      types.UID                      `json:"sourceClassUID"`
	SourceClassRevision string                         `json:"sourceClassRevision"`
	SourceClassDigest   string                         `json:"sourceClassDigest"`
	LocalProfileUID     types.UID                      `json:"localProfileUID"`
	SourceProfileUID    types.UID                      `json:"sourceProfileUID,omitempty"`
	ProfileKind         ProfileKind                    `json:"profileKind"`
	RequirementsDigest  string                         `json:"requirementsDigest"`
	PublicationRevision string                         `json:"publicationRevision"`
	Location            fleetnetwork.LocationAuthority `json:"location"`
}

func (r SourceUseRequest) Validate() error {
	if !r.ConsumerProject.Valid() || !r.ProviderProject.Valid() || r.NamespaceUID == "" || r.ClaimUID == "" || r.LocalClassUID == "" || r.SourceClassUID == "" || r.SourceClassRevision == "" || r.SourceClassDigest == "" || r.LocalProfileUID == "" || r.RequirementsDigest == "" || r.PublicationRevision == "" || !r.Location.Valid() {
		return ErrAuthorityInvalid
	}
	if r.ProfileKind == ProfilePublished && r.SourceProfileUID == "" || r.ProfileKind == ProfileCustom && r.SourceProfileUID != "" || r.ProfileKind != ProfilePublished && r.ProfileKind != ProfileCustom {
		return ErrAuthorityInvalid
	}
	return nil
}

type SourceUseReceipt struct {
	RequestDigest       string `json:"requestDigest"`
	PrincipalUID        string `json:"principalUID"`
	DecisionID          string `json:"decisionID"`
	PublicationRevision string `json:"publicationRevision"`
	FenceToken          string `json:"fenceToken"`
}

type SourceAuthorizer interface {
	AuthorizeSource(context.Context, SourceUseRequest) (SourceUseReceipt, error)
}

type EntitlementEvidence struct {
	Name               string    `json:"name"`
	UID                types.UID `json:"uid"`
	Generation         int64     `json:"generation"`
	ObservedGeneration int64     `json:"observedGeneration"`
	ServiceName        string    `json:"serviceName"`
}

func (e EntitlementEvidence) Valid() bool {
	return e.Name == EntitlementName && e.UID != "" && e.Generation > 0 && e.ObservedGeneration == e.Generation && e.ServiceName == ServiceName
}

type EntitlementReader interface {
	Current(context.Context, client.Client) (EntitlementEvidence, error)
}

type QuotaIntent struct {
	ConsumerProject controller.ProjectIdentity `json:"consumerProject"`
	Namespace       string                     `json:"namespace"`
	ClaimName       string                     `json:"claimName"`
	ClaimUID        types.UID                  `json:"claimUID"`
	ResourceType    string                     `json:"resourceType"`
	Amount          int64                      `json:"amount"`
	Name            string                     `json:"name"`
}

func (i QuotaIntent) Valid() bool {
	return i.ConsumerProject.Valid() && i.Namespace != "" && i.ClaimName != "" && i.ClaimUID != "" && i.ResourceType == MachineQuotaResource && i.Amount == 1 && i.Name == QuotaIntentFor(i.ConsumerProject, i.Namespace, i.ClaimName, i.ClaimUID).Name
}

type QuotaHold struct {
	Intent             QuotaIntent `json:"intent"`
	UID                types.UID   `json:"uid"`
	Generation         int64       `json:"generation"`
	ObservedGeneration int64       `json:"observedGeneration"`
}

func (h QuotaHold) Valid() bool {
	return h.Intent.Valid() && h.UID != "" && h.Generation > 0 && h.ObservedGeneration == h.Generation
}

func (h QuotaHold) Ref() controller.ExternalHoldRef {
	return controller.ExternalHoldRef{Provider: "milo", Namespace: h.Intent.Namespace, Name: h.Intent.Name, UID: h.UID}
}

type QuotaManager interface {
	Acquire(context.Context, client.Client, QuotaIntent) (QuotaHold, bool, error)
}

type SettlementReceipt struct {
	AuthorityRecordID string                     `json:"authorityRecordID"`
	Hold              controller.ExternalHoldRef `json:"hold"`
	HoldDigest        string                     `json:"holdDigest"`
	AcknowledgmentID  string                     `json:"acknowledgmentID"`
}

// QuotaCustodian is the protected owner of an intent's complete lifetime.
// Implementations must serialize Acquire with Recover/NoHold closure and
// Settle so an acquire effect cannot occur after a NoHoldReceipt closes the
// exact project/authority/intent tuple.
type QuotaCustodian interface {
	Acquire(context.Context, controller.ProjectIdentity, controller.AuthorityRef, QuotaIntent) (QuotaHold, bool, error)
	Recover(context.Context, controller.ProjectIdentity, controller.AuthorityRef, QuotaIntent) (IntentResolution, error)
	Settle(context.Context, controller.ProjectIdentity, controller.AuthorityRef, QuotaHold) (SettlementReceipt, error)
}

type IntentResolution struct {
	Hold   *QuotaHold     `json:"hold,omitempty"`
	NoHold *NoHoldReceipt `json:"noHold,omitempty"`
}

type NoHoldReceipt struct {
	AuthorityRecordID string `json:"authorityRecordID"`
	IntentDigest      string `json:"intentDigest"`
	AcknowledgmentID  string `json:"acknowledgmentID"`
}

type AdmissionCommit struct {
	Authority   controller.AuthorityRef `json:"authority"`
	SourceUse   SourceUseRequest        `json:"sourceUse"`
	Source      SourceUseReceipt        `json:"source"`
	Entitlement EntitlementEvidence     `json:"entitlement"`
	Hold        QuotaHold               `json:"hold"`
}

type AdmissionCommitReceipt struct {
	ContractDigest string `json:"contractDigest"`
	CommitDigest   string `json:"commitDigest"`
	FenceToken     string `json:"fenceToken"`
	CommitID       string `json:"commitID"`
}

type AdmissionFence interface {
	Commit(context.Context, AdmissionCommit) (AdmissionCommitReceipt, error)
}

type AuthorityState string

const (
	StatePrepared            AuthorityState = "Prepared"
	StateQuotaIntent         AuthorityState = "QuotaIntent"
	StateQuotaGranted        AuthorityState = "QuotaGranted"
	StateBound               AuthorityState = "Bound"
	StateCleanupVerified     AuthorityState = "CleanupVerified"
	StateQuotaReleasePending AuthorityState = "QuotaReleasePending"
	StateReleased            AuthorityState = "Released"
	StateQuarantined         AuthorityState = "Quarantined"
)

type AuthorityRecord struct {
	SchemaVersion        int                        `json:"schemaVersion"`
	RecordID             string                     `json:"recordID"`
	State                AuthorityState             `json:"state"`
	ClaimNamespace       string                     `json:"claimNamespace"`
	ClaimName            string                     `json:"claimName"`
	ClaimUID             types.UID                  `json:"claimUID"`
	SourceRequest        SourceUseRequest           `json:"sourceRequest"`
	SourceReceipt        SourceUseReceipt           `json:"sourceReceipt"`
	ContractDigest       string                     `json:"contractDigest"`
	AcceptedRequirements fleetv1.MachineProfileSpec `json:"acceptedRequirements"`
	Entitlement          *EntitlementEvidence       `json:"entitlement,omitempty"`
	QuotaIntent          *QuotaIntent               `json:"quotaIntent,omitempty"`
	QuotaHold            *QuotaHold                 `json:"quotaHold,omitempty"`
	FenceReceipt         *AdmissionCommitReceipt    `json:"fenceReceipt,omitempty"`
	AssetID              string                     `json:"assetID,omitempty"`
	BindingID            string                     `json:"bindingID,omitempty"`
	Settlement           *SettlementReceipt         `json:"settlement,omitempty"`
	LastSettledHold      *QuotaHold                 `json:"lastSettledHold,omitempty"`
	LastSettlement       *SettlementReceipt         `json:"lastSettlement,omitempty"`
	NoHold               *NoHoldReceipt             `json:"noHold,omitempty"`
}

func digest(value any) (string, error) {
	b, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func shortDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:12])
}

func requirementsDigest(profile *fleetv1.MachineProfile) (string, error) {
	if profile == nil {
		return "", ErrAuthorityInvalid
	}
	return digest(profile.Spec)
}

func validateReceipt(request SourceUseRequest, receipt SourceUseReceipt) error {
	want, err := digest(request)
	if err != nil {
		return err
	}
	if receipt.RequestDigest != want || strings.TrimSpace(receipt.PrincipalUID) == "" || strings.TrimSpace(receipt.DecisionID) == "" || receipt.PublicationRevision != request.PublicationRevision || strings.TrimSpace(receipt.FenceToken) == "" {
		return ErrAuthorityInvalid
	}
	return nil
}
