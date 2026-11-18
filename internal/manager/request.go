package manager

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
)

// ProjectRequest is the only queue identity used by Fleet's project-facing
// controllers. ClusterName names the engaged project cluster the request
// belongs to; namespace and name are routing hints; AuthorityID, ProjectUID,
// and UID prevent equal names or UIDs from different project API servers from
// sharing work. A request is only ever reconciled through the runtime bound to
// its ClusterName, and only when that runtime's identity matches AuthorityID
// and ProjectUID.
type ProjectRequest struct {
	ClusterName multicluster.ClusterName
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

// Cluster and WithCluster implement multicluster-runtime's ClusterAware.
func (r ProjectRequest) Cluster() multicluster.ClusterName { return r.ClusterName }

func (r ProjectRequest) WithCluster(name multicluster.ClusterName) ProjectRequest {
	r.ClusterName = name
	return r
}

func (r ProjectRequest) String() string {
	return fmt.Sprintf("cluster://%s/%s/%s/%s/%s", r.ClusterName, r.ProjectUID, r.Namespace, r.Name, r.UID)
}
