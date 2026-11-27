package site

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	"go.datum.net/fleet/internal/bmcdiscovery"
	"go.datum.net/fleet/internal/custody"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func credentialSecret(name, project, device string, data map[string]string) *corev1.Secret {
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "fleet-site", UID: types.UID("uid-" + name), ResourceVersion: "1",
		Labels:      map[string]string{bmcdiscovery.CredentialLabel: "true"},
		Annotations: map[string]string{bmcdiscovery.ProjectAnnotation: project, bmcdiscovery.NamespaceAnnotation: "default", bmcdiscovery.NameAnnotation: device}}}
	secret.Data = map[string][]byte{}
	for key, value := range data {
		secret.Data[key] = []byte(value)
	}
	return secret
}

// TestSecretCredentialsGetOnlyTheDevicesOwnSecret reads a Device's
// credential by its derived name with one get, and never lists: a reader
// that refuses every list still works. The Secret must name the Device in
// the project; a Secret of another name, project or Device is not used.
func TestSecretCredentialsGetOnlyTheDevicesOwnSecret(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	pin := strings.Repeat("ab", 32)
	own := CredentialSecretName("default/server-example-001")
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		credentialSecret(own, "provider-example", "server-example-001", map[string]string{"username": "operator", "password": "synthetic-secret", "tlsPublicKeySHA256": pin}),
		// Named for server-example-002 but bound to server-example-001.
		credentialSecret(CredentialSecretName("default/server-example-002"), "provider-example", "server-example-001", map[string]string{"username": "operator", "password": "synthetic-secret"}),
		// Bound to server-example-003 but not under its derived name.
		credentialSecret("server-example-003", "provider-example", "server-example-003", map[string]string{"username": "operator", "password": "synthetic-secret"}),
		credentialSecret(CredentialSecretName("default/server-example-004"), "provider-other", "server-example-004", map[string]string{"username": "operator", "password": "synthetic-secret"}),
	).WithInterceptorFuncs(interceptor.Funcs{List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
		return errors.New("a worker may not list credential Secrets")
	}}).Build()
	credentials := SecretCredentials{Reader: reader, Namespace: "fleet-site", Project: "provider-example"}
	ctx := context.Background()
	credential, found, err := credentials.Lookup(ctx, "default/server-example-001")
	if err != nil || !found || credential.Username != "operator" || credential.Pin != pin {
		t.Fatalf("credential = %+v %v %v", credential, found, err)
	}
	for _, device := range []string{"default/server-example-002", "default/server-example-003", "default/server-example-004"} {
		if _, found, err := credentials.Lookup(ctx, device); found || err != nil {
			t.Errorf("%s: found = %v, err = %v; want no credential", device, found, err)
		}
	}
	if CredentialSecretName("default/server-example-001") == CredentialSecretName("other/server-example-001") {
		t.Fatal("two Devices share a credential Secret name")
	}
}

func TestReportGivesTheCoordinatorSeenAndStoppedFences(t *testing.T) {
	store := newStore(t)
	report := Report{Site: "site-example-a", Store: store, Targets: StaticTargets{"default/server-example-001": {}}}
	var _ custody.Sites = report
	served := &inventory.Device{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "server-example-001"}}
	other := &inventory.Device{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "server-example-002"}}
	if sites := report.Serving(served); len(sites) != 1 || sites[0] != "site-example-a" {
		t.Fatalf("serving = %v", sites)
	}
	if sites := report.Serving(other); len(sites) != 0 {
		t.Fatal("the site claims a Device it does not enroll")
	}
	if err := store.Raise("default/server-example-001", 4); err != nil {
		t.Fatal(err)
	}
	fence, reported, err := report.Fence(context.Background(), "site-example-a", served)
	if err != nil || !reported || fence.Seen != 4 || fence.Stopped != 0 {
		t.Fatalf("fence = %+v %v %v", fence, reported, err)
	}
	if err := store.ConfirmStopped("default/server-example-001", 4); err != nil {
		t.Fatal(err)
	}
	if fence, _, _ := report.Fence(context.Background(), "site-example-a", served); fence.Stopped != 4 {
		t.Fatalf("fence = %+v", fence)
	}
}

// TestTheWorkerIdentityIsItsOwnSecret reads the site worker's media service
// client certificate from its one named Secret, with a direct get. Another
// site's identity Secret beside it is never read.
func TestTheWorkerIdentityIsItsOwnSecret(t *testing.T) {
	pair := func(cn string) map[string][]byte {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: cn}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
		der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
		if err != nil {
			t.Fatal(err)
		}
		keyDER, err := x509.MarshalECPrivateKey(key)
		if err != nil {
			t.Fatal(err)
		}
		return map[string][]byte{corev1.TLSCertKey: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
			corev1.TLSPrivateKeyKey: pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})}
	}
	own := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: WorkerIdentitySecret, Namespace: "fleet-site-worker-identity"}, Data: pair("fleet-site-worker:site-example-a")}
	other := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: WorkerIdentitySecret + "-another-site", Namespace: "fleet-site-worker-identity"}, Data: pair("fleet-site-worker:site-example-b")}
	var read []string
	reader := interceptor.NewClient(fake.NewClientBuilder().WithObjects(own, other).Build(), interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			read = append(read, key.String())
			return c.Get(ctx, key, obj, opts...)
		},
		List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
			return errors.New("the worker identity must never be listed")
		},
	})
	identity, err := SecretWorkerIdentity{Reader: reader, Namespace: "fleet-site-worker-identity"}.MediaClient(context.Background(), "default/server-example-001")
	if err != nil || identity == nil {
		t.Fatalf("identity = %v, %v", identity, err)
	}
	leaf, _ := x509.ParseCertificate(identity.Certificate[0])
	if leaf.Subject.CommonName != "fleet-site-worker:site-example-a" || len(read) != 1 || read[0] != "fleet-site-worker-identity/"+WorkerIdentitySecret {
		t.Fatalf("read %v as %s", read, leaf.Subject.CommonName)
	}
	// A site with no identity Secret has no media service identity.
	none, err := SecretWorkerIdentity{Reader: fake.NewClientBuilder().Build(), Namespace: "fleet-site-worker-identity"}.MediaClient(context.Background(), "")
	if err != nil || none != nil {
		t.Fatalf("no Secret = %v, %v", none, err)
	}
}
