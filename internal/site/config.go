package site

import (
	"context"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	"go.datum.net/fleet/internal/bmcdiscovery"
	"go.datum.net/fleet/internal/custody"
	"go.datum.net/fleet/internal/redfish"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// SecretCredentials reads per-Device BMC credential Secrets in the site
// cluster, using the contract BMC discovery uses. The approved pin comes from
// the same Secret.
type SecretCredentials struct {
	Reader    client.Reader
	Namespace string
	// Project is the provider project the Devices belong to.
	Project string
}

func (s SecretCredentials) Lookup(ctx context.Context, device string) (bmcdiscovery.Credential, bool, error) {
	var secrets corev1.SecretList
	if err := s.Reader.List(ctx, &secrets, client.InNamespace(s.Namespace), client.MatchingLabels{bmcdiscovery.CredentialLabel: "true"}); err != nil {
		return bmcdiscovery.Credential{}, false, err
	}
	return bmcdiscovery.CredentialFor(secrets.Items, s.Project, device)
}

// StaticTargets is a site's explicit enrollment, keyed by
// "<namespace>/<name>". The site never infers which Devices it serves.
type StaticTargets map[string]redfish.Target

func (t StaticTargets) Target(device string) (redfish.Target, bool) {
	target, ok := t[device]
	return target, ok
}

// Report is the site's fence report for the coordinator. Targets is the
// explicit list of Devices this site serves.
type Report struct {
	Site    string
	Store   *Store
	Targets StaticTargets
}

var _ custody.Sites = Report{}

func (r Report) Serving(device *inventory.Device) []string {
	if _, ok := r.Targets[device.Namespace+"/"+device.Name]; ok {
		return []string{r.Site}
	}
	return nil
}

func (r Report) Fence(_ context.Context, site string, device *inventory.Device) (custody.SiteFence, bool, error) {
	if site != r.Site {
		return custody.SiteFence{}, false, nil
	}
	fence, stopped, err := r.Store.Current(device.Namespace + "/" + device.Name)
	if err != nil {
		return custody.SiteFence{}, false, err
	}
	return custody.SiteFence{Seen: fence, Stopped: stopped}, true, nil
}
