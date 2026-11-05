package manager

import (
	"fmt"
	"net/url"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/rest"
)

// ProjectRESTConfig derives the exact Milo project control-plane endpoint from
// a root Milo credential. The input config is copied so the same credential can
// still be used for root-scoped operations by its owner.
func ProjectRESTConfig(root *rest.Config, route string) (*rest.Config, error) {
	if root == nil || strings.TrimSpace(root.Host) == "" || strings.TrimSpace(route) == "" {
		return nil, fmt.Errorf("%w: root Milo config and project route are required", ErrEventWiringUnavailable)
	}
	if problems := validation.IsDNS1123Subdomain(route); len(problems) != 0 {
		return nil, fmt.Errorf("%w: invalid project route", ErrEventWiringUnavailable)
	}
	project := rest.CopyConfig(root)
	project.Host = strings.TrimRight(project.Host, "/") + "/apis/resourcemanager.miloapis.com/v1alpha1/projects/" + url.PathEscape(route) + "/control-plane"
	return project, nil
}
