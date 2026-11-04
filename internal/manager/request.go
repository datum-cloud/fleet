package manager

import (
	"strings"

	"k8s.io/apimachinery/pkg/types"
)

// ProjectRequest is the only queue identity used by Fleet's project-facing
// controllers. Namespace and name are routing hints; AuthorityID, ProjectUID,
// and UID prevent equal names or UIDs from different project API servers from
// sharing work.
type ProjectRequest struct {
	AuthorityID string
	ProjectUID  types.UID
	Namespace   string
	Name        string
	UID         types.UID
}

func (r ProjectRequest) Valid() bool {
	return strings.TrimSpace(r.AuthorityID) != "" && r.ProjectUID != "" && r.Namespace != "" && r.Name != "" && r.UID != ""
}

func (r ProjectRequest) SameProject(authorityID string, projectUID types.UID) bool {
	return r.AuthorityID == authorityID && r.ProjectUID == projectUID
}
