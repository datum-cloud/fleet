package bmcdiscovery

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// The shipped credential Role is enough for discovery's Secret cache and for
// --once, and grants no get by name. A Secret that is not a credential is
// still returned by an unselected list, which is why the render check keeps
// other Secrets out of discovery's namespace.
func TestShippedRoleServesTheCredentialCacheAndRefusesGet(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS required")
	}
	environment := &envtest.Environment{}
	admin, err := environment.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := environment.Stop(); err != nil {
			t.Error(err)
		}
	})
	scheme, err := NewScheme()
	if err != nil {
		t.Fatal(err)
	}
	if err := rbacv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	adminClient, err := client.New(admin, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	create := func(object client.Object) {
		t.Helper()
		if err := adminClient.Create(ctx, object); err != nil {
			t.Fatal(err)
		}
	}
	create(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: credNS}})
	data, err := os.ReadFile(filepath.Join(baseDir, "rbac.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	decoder := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	for {
		var object unstructured.Unstructured
		if err := decoder.Decode(&object.Object); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		object.SetNamespace(credNS)
		create(&object)
	}
	credential := secret("device-a", "")
	credential.UID, credential.ResourceVersion = "", ""
	create(credential)
	create(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "unrelated", Namespace: credNS}, Data: map[string][]byte{"token": []byte("synthetic")}})

	discovery := rest.CopyConfig(admin)
	discovery.Impersonate = rest.ImpersonationConfig{UserName: "system:serviceaccount:" + credNS + ":fleet-bmc-discovery",
		Groups: []string{"system:serviceaccounts", "system:serviceaccounts:" + credNS, "system:authenticated"}}
	c := Config{CredentialNamespace: credNS}
	secrets, err := credentialCluster(c, discovery, scheme)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = secrets.Start(ctx) }()
	if !secrets.GetCache().WaitForCacheSync(ctx) {
		t.Fatal("credential Secret cache did not sync under the shipped Role")
	}
	var cached corev1.SecretList
	if err := secrets.GetCache().List(ctx, &cached, client.InNamespace(credNS), client.MatchingLabels{CredentialLabel: "true"}); err != nil {
		t.Fatal(err)
	}
	if len(cached.Items) != 1 || cached.Items[0].Name != credential.Name {
		t.Fatalf("cached credential Secrets = %v, want only %s", cached.Items, credential.Name)
	}

	direct, err := client.New(discovery, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	// --once lists the labelled Secrets directly.
	var listed corev1.SecretList
	if err := direct.List(ctx, &listed, client.InNamespace(credNS), client.MatchingLabels{CredentialLabel: "true"}); err != nil {
		t.Fatalf("--once list under the shipped Role: %v", err)
	}
	if len(listed.Items) != 1 {
		t.Fatalf("listed credential Secrets = %d, want 1", len(listed.Items))
	}
	if err := direct.Get(ctx, client.ObjectKey{Namespace: credNS, Name: credential.Name}, &corev1.Secret{}); !apierrors.IsForbidden(err) {
		t.Fatalf("get credential Secret by name = %v, want Forbidden", err)
	}
	var everything corev1.SecretList
	if err := direct.List(ctx, &everything, client.InNamespace(credNS)); err != nil {
		t.Fatal(err)
	}
	if len(everything.Items) != 2 {
		t.Fatalf("unselected list = %d Secrets; RBAC was expected to return every Secret in the namespace", len(everything.Items))
	}
	if err := direct.List(ctx, &corev1.SecretList{}, client.InNamespace("default")); !apierrors.IsForbidden(err) {
		t.Fatalf("list Secrets in another namespace = %v, want Forbidden", err)
	}
}
