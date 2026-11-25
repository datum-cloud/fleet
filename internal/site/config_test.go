package site

import (
	"context"
	"strings"
	"testing"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	"go.datum.net/fleet/internal/bmcdiscovery"
	"go.datum.net/fleet/internal/custody"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
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

func TestSecretCredentialsUseThePerDeviceContract(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	pin := strings.Repeat("ab", 32)
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		credentialSecret("server-a", "provider-example", "server-example-001", map[string]string{"username": "operator", "password": "synthetic-secret", "tlsPublicKeySHA256": pin}),
		credentialSecret("server-b", "provider-example", "server-example-002", map[string]string{"username": "operator", "password": "synthetic-secret"}),
		credentialSecret("server-b-2", "provider-example", "server-example-002", map[string]string{"username": "other", "password": "synthetic-secret"}),
		credentialSecret("elsewhere", "provider-other", "server-example-003", map[string]string{"username": "operator", "password": "synthetic-secret"}),
	).Build()
	credentials := SecretCredentials{Reader: reader, Namespace: "fleet-site", Project: "provider-example"}
	ctx := context.Background()
	credential, found, err := credentials.Lookup(ctx, "default/server-example-001")
	if err != nil || !found || credential.Username != "operator" || credential.Pin != pin {
		t.Fatalf("credential = %+v %v %v", credential, found, err)
	}
	// Two Secrets naming one Device are a conflict, never a choice.
	if _, _, err := credentials.Lookup(ctx, "default/server-example-002"); err == nil {
		t.Fatal("two credential Secrets for one Device were accepted")
	}
	// A Secret for another project's Device is not this Device's.
	if _, found, _ := credentials.Lookup(ctx, "default/server-example-003"); found {
		t.Fatal("another project's credential was used")
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
