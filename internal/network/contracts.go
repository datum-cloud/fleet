package network

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func DigestRequests(requests []InterfaceRequest) (string, error) {
	canonical := append([]InterfaceRequest(nil), requests...)
	sort.Slice(canonical, func(i, j int) bool { return canonical[i].Name < canonical[j].Name })
	b, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

var (
	ErrAuthorizationUnavailable = errors.New("caller-preserving network authorization is unavailable")
	ErrAuthorizationDenied      = errors.New("network use is not authorized")
	ErrEvidenceStale            = errors.New("network evidence is stale")
	ErrObjectConflict           = errors.New("network object conflicts with retained authority")
	ErrPending                  = errors.New("network operation is pending")
)

type LocationAuthority struct {
	Name     string    `json:"name"`
	UID      types.UID `json:"uid"`
	Revision string    `json:"revision"`
}

func (a LocationAuthority) Valid() bool {
	return a.Name != "" && a.UID != "" && a.Revision != ""
}

type InterfaceRequest struct {
	Name           string   `json:"name"`
	NetworkName    string   `json:"networkName"`
	NetworkNS      string   `json:"networkNamespace"`
	IPFamilies     []string `json:"ipFamilies,omitempty"`
	AddressClasses []string `json:"addressClasses,omitempty"`
	ReclaimPolicy  string   `json:"reclaimPolicy"`
}

// ValidAttachmentLists mirrors the cardinality and uniqueness constraints of
// the NSO NetworkInterfaceClaim contract so invalid requests fail before Fleet
// reserves physical capacity.
func ValidAttachmentLists(ipFamilies, addressClasses []string) bool {
	if len(ipFamilies) > 2 || len(addressClasses) > 4 {
		return false
	}
	seenFamilies := map[string]bool{}
	for _, family := range ipFamilies {
		if (family != "IPv4" && family != "IPv6") || seenFamilies[family] {
			return false
		}
		seenFamilies[family] = true
	}
	seenClasses := map[string]bool{}
	for _, class := range addressClasses {
		if class == "" || seenClasses[class] {
			return false
		}
		seenClasses[class] = true
	}
	return true
}

type PreflightRequest struct {
	ClaimNamespace string
	ClaimUID       types.UID
	NamespaceUID   types.UID
	PrincipalUID   string
	Location       LocationAuthority
	Interfaces     []InterfaceRequest
}

type UseRequest struct {
	PrincipalUID   string
	ClaimUID       types.UID
	Namespace      string
	NamespaceUID   types.UID
	NetworkName    string
	NetworkUID     types.UID
	AddressClasses []string
	Location       LocationAuthority
}

type Decision struct {
	ID               string               `json:"id"`
	Revision         string               `json:"revision"`
	AddressClassUIDs map[string]types.UID `json:"addressClassUIDs,omitempty"`
}

func (d Decision) Valid() bool { return d.ID != "" && d.Revision != "" }

// UseAuthorizer must evaluate the initiating principal against the target
// network scope. Implementations must not replace PrincipalUID with the
// controller service account.
type UseAuthorizer interface {
	AuthorizeNetworkUse(context.Context, UseRequest) (Decision, error)
}

type Plan struct {
	RequestDigest string            `json:"requestDigest"`
	Location      LocationAuthority `json:"location"`
	Interfaces    []InterfacePlan   `json:"interfaces"`
}

type InterfacePlan struct {
	Name              string               `json:"name"`
	NetworkNamespace  string               `json:"networkNamespace"`
	NetworkName       string               `json:"networkName"`
	NetworkUID        types.UID            `json:"networkUID"`
	NetworkGeneration int64                `json:"networkGeneration"`
	IPFamilies        []string             `json:"ipFamilies,omitempty"`
	AddressClasses    []string             `json:"addressClasses,omitempty"`
	AddressClassUIDs  map[string]types.UID `json:"addressClassUIDs,omitempty"`
	ReclaimPolicy     string               `json:"reclaimPolicy"`
	ClaimName         string               `json:"claimName"`
	Decision          Decision             `json:"decision"`
}

func (p Plan) Valid() bool {
	if p.RequestDigest == "" || !p.Location.Valid() || len(p.Interfaces) == 0 {
		return false
	}
	seen := map[string]bool{}
	for _, iface := range p.Interfaces {
		if iface.Name == "" || seen[iface.Name] || iface.NetworkNamespace == "" || iface.NetworkName == "" || iface.NetworkUID == "" || iface.NetworkGeneration < 0 || iface.ClaimName == "" || !iface.Decision.Valid() || !ValidAttachmentLists(iface.IPFamilies, iface.AddressClasses) || (iface.ReclaimPolicy != "Delete" && iface.ReclaimPolicy != "Retain") {
			return false
		}
		seen[iface.Name] = true
		for _, class := range iface.AddressClasses {
			if iface.AddressClassUIDs[class] == "" || iface.Decision.AddressClassUIDs[class] != iface.AddressClassUIDs[class] {
				return false
			}
		}
	}
	return true
}

type EnsureRequest struct {
	ClaimUID  types.UID
	BindingID string
	Plan      Plan
	Previous  Observation
}

type Condition struct {
	Type               string                 `json:"type"`
	Status             metav1.ConditionStatus `json:"status"`
	Reason             string                 `json:"reason,omitempty"`
	ObservedGeneration int64                  `json:"observedGeneration"`
}

type Address struct {
	Address string `json:"address"`
	Family  string `json:"family"`
	Gateway string `json:"gateway,omitempty"`
	Primary bool   `json:"primary,omitempty"`
	Class   string `json:"class,omitempty"`
}

type Observation struct {
	Revision   string                    `json:"revision"`
	ObservedAt time.Time                 `json:"observedAt"`
	Interfaces []InterfaceObservation    `json:"interfaces"`
	Lifecycle  []InterfaceLifecycleState `json:"lifecycle"`
}

type InterfaceLifecycleState struct {
	Name                  string    `json:"name"`
	GuardToken            string    `json:"guardToken"`
	CreatedUID            types.UID `json:"createdUID,omitempty"`
	CreateAdmitted        bool      `json:"createAdmitted"`
	DeleteAdmitted        bool      `json:"deleteAdmitted,omitempty"`
	DeleteCompleted       bool      `json:"deleteCompleted,omitempty"`
	RetainedInterfaceName string    `json:"retainedInterfaceName,omitempty"`
	RetainedInterfaceUID  types.UID `json:"retainedInterfaceUID,omitempty"`
	NoBindingVerified     bool      `json:"noBindingVerified,omitempty"`
}

type InterfaceObservation struct {
	Name                 string      `json:"name"`
	ClaimUID             types.UID   `json:"claimUID"`
	ClaimGeneration      int64       `json:"claimGeneration"`
	NetworkInterfaceUID  types.UID   `json:"networkInterfaceUID,omitempty"`
	NetworkInterfaceName string      `json:"networkInterfaceName,omitempty"`
	MTU                  int32       `json:"mtu,omitempty"`
	Addresses            []Address   `json:"addresses,omitempty"`
	Conditions           []Condition `json:"conditions,omitempty"`
}

func (o Observation) Prepared(plan Plan) bool {
	return o.hasCurrent(plan, "Allocated", "Prepared")
}

func (o Observation) Programmed(plan Plan) bool {
	return o.hasCurrent(plan, "Allocated", "Prepared", "Programmed")
}

func (o Observation) hasCurrent(plan Plan, required ...string) bool {
	if o.Revision == "" || o.ObservedAt.IsZero() || len(o.Interfaces) != len(plan.Interfaces) {
		return false
	}
	byName := map[string]InterfaceObservation{}
	for _, iface := range o.Interfaces {
		byName[iface.Name] = iface
	}
	for _, planned := range plan.Interfaces {
		observed, ok := byName[planned.Name]
		if !ok || observed.ClaimUID == "" {
			return false
		}
		for _, requiredType := range required {
			current := false
			for _, condition := range observed.Conditions {
				if condition.Type == requiredType && condition.Status == metav1.ConditionTrue && condition.ObservedGeneration == observed.ClaimGeneration {
					current = true
				}
			}
			if !current {
				return false
			}
		}
	}
	return true
}

type ReleaseRequest struct {
	ClaimUID    types.UID
	BindingID   string
	Plan        Plan
	Observation Observation
	Lifecycle   []InterfaceLifecycleState
}

type ReleaseObservation struct {
	Verified  bool
	Lifecycle []InterfaceLifecycleState
	Revision  string
}

type Lifecycle interface {
	Preflight(context.Context, PreflightRequest) (Plan, error)
	Ensure(context.Context, EnsureRequest) (Observation, error)
	Release(context.Context, ReleaseRequest) (ReleaseObservation, error)
}

func NormalizeReclaimPolicy(value string) (string, bool) {
	switch strings.TrimSpace(value) {
	case "", "Delete":
		return "Delete", true
	case "Retain":
		return "Retain", true
	default:
		return "", false
	}
}
