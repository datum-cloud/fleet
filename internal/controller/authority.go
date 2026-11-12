package controller

import (
	"strings"

	"k8s.io/apimachinery/pkg/types"
)

// ProjectIdentity identifies a project control-plane lifetime. RouteName is a
// routing hint; AuthorityID and UID are the canonical lifetime identity.
type ProjectIdentity struct {
	AuthorityID string    `json:"authorityID"`
	RouteName   string    `json:"routeName"`
	UID         types.UID `json:"uid"`
}

func (p ProjectIdentity) Valid() bool {
	return strings.TrimSpace(p.AuthorityID) != "" && strings.TrimSpace(p.RouteName) != "" && p.UID != ""
}

func (p ProjectIdentity) SameLifetime(other ProjectIdentity) bool {
	return p.AuthorityID == other.AuthorityID && p.UID == other.UID
}

// ClaimScope prevents equal claim UIDs from independent project API servers
// from sharing private lifecycle custody. Standalone callers use an explicit
// local authority with the namespace lifetime as the project key.
type ClaimScope struct {
	AuthorityID string    `json:"authorityID"`
	ProjectUID  types.UID `json:"projectUID"`
}

func (s ClaimScope) Valid() bool {
	return strings.TrimSpace(s.AuthorityID) != "" && s.ProjectUID != ""
}

func ProjectClaimScope(project ProjectIdentity) ClaimScope {
	return ClaimScope{AuthorityID: project.AuthorityID, ProjectUID: project.UID}
}

func LocalClaimScope(namespaceUID types.UID) ClaimScope {
	return ClaimScope{AuthorityID: "local", ProjectUID: namespaceUID}
}

// AuthorityRef points at a service-controlled authority record. ContractDigest
// commits to its immutable admission inputs; the mutable record advances through
// quota and release recovery states under compare-and-swap updates.
type AuthorityRef struct {
	Provider       string `json:"provider"`
	RecordName     string `json:"recordName"`
	RecordID       string `json:"recordID"`
	ContractDigest string `json:"contractDigest"`
}

func (r AuthorityRef) Valid() bool {
	return strings.TrimSpace(r.Provider) != "" && strings.TrimSpace(r.RecordName) != "" && strings.TrimSpace(r.RecordID) != "" && strings.TrimSpace(r.ContractDigest) != ""
}

// ExternalHoldRef identifies an allocation hold whose owner must explicitly
// acknowledge settlement before Fleet releases custody.
type ExternalHoldRef struct {
	Provider  string    `json:"provider"`
	Namespace string    `json:"namespace"`
	Name      string    `json:"name"`
	UID       types.UID `json:"uid"`
}

func (r ExternalHoldRef) Valid() bool {
	return strings.TrimSpace(r.Provider) != "" && strings.TrimSpace(r.Namespace) != "" && strings.TrimSpace(r.Name) != "" && r.UID != ""
}
