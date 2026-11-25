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
