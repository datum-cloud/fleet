package site

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	"go.datum.net/fleet/internal/bmcdiscovery"
	"go.datum.net/fleet/internal/custody"
	"go.datum.net/fleet/internal/redfish"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// CredentialSecretName is the only name a Device's BMC credential Secret
// may have at a site. It derives from the "<namespace>/<name>" Device, so the
// worker of an operation can be granted get on exactly its Device's Secret
// and nothing else.
func CredentialSecretName(device string) string {
	sum := sha256.Sum256([]byte(device))
	return "bmc-" + hex.EncodeToString(sum[:])[:20]
}

// SecretCredentials reads one Device's BMC credential Secret in the site
// cluster, by its derived name and with a direct get: a worker may read only
// its own Device's credential and may list none. The Secret must still meet
// the contract BMC discovery uses, naming this Device in this project, and
// the approved pin comes from it.
type SecretCredentials struct {
	Reader    client.Reader
	Namespace string
	// Project is the provider project the Devices belong to.
	Project string
}

func (s SecretCredentials) Lookup(ctx context.Context, device string) (bmcdiscovery.Credential, bool, error) {
	secret := &corev1.Secret{}
	if err := s.Reader.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: CredentialSecretName(device)}, secret); apierrors.IsNotFound(err) {
		return bmcdiscovery.Credential{}, false, nil
	} else if err != nil {
		return bmcdiscovery.Credential{}, false, err
	}
	return bmcdiscovery.CredentialFor([]corev1.Secret{*secret}, s.Project, device)
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
