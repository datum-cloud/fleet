package site

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
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

// DeliveryMethods reports no declaration: the site's declaration reaches the
// coordinator only through the cell's report handoff.
func (r Report) DeliveryMethods(context.Context, string, *inventory.Device) ([]custody.OfferedMethod, bool, error) {
	return nil, false, nil
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

// WorkerIdentitySecret is the only Secret, in the site worker identity
// namespace, that holds the site workers' client certificate for the
// central media service, as tls.crt and tls.key. There is one per site
// worker identity, not per Device, and a worker gets exactly it.
const WorkerIdentitySecret = "fleet-site-worker-media"

// SecretWorkerIdentity reads the site worker's media service client
// certificate from its own Secret, by name and with a direct get, apart from
// any BMC credential.
type SecretWorkerIdentity struct {
	Reader    client.Reader
	Namespace string
}

// MediaClient returns the site worker's client certificate, or nil when the
// site has none.
func (s SecretWorkerIdentity) MediaClient(ctx context.Context, _ string) (*tls.Certificate, error) {
	secret := &corev1.Secret{}
	if err := s.Reader.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: WorkerIdentitySecret}, secret); apierrors.IsNotFound(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	identity, err := tls.X509KeyPair(secret.Data[corev1.TLSCertKey], secret.Data[corev1.TLSPrivateKeyKey])
	if err != nil {
		return nil, err
	}
	return &identity, nil
}
