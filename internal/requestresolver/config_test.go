package requestresolver

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	servicesv1alpha1 "go.miloapis.com/service-catalog/api/v1alpha1"
)

const productionShape = `apiVersion: config.fleet.datumapis.com/v1alpha1
kind: RequestResolver
discovery:
  mode: milo
  discoveryKubeconfigPath: /etc/fleet/discovery/kubeconfig
  projectKubeconfigPath: /etc/fleet/projects/kubeconfig
  consumerScopedProjection:
    providerProject: datum-cloud
    serviceNames:
      - metal.datumapis.com
`

func TestParseConfigAcceptsProductionShape(t *testing.T) {
	config, err := ParseConfig([]byte(productionShape))
	if err != nil {
		t.Fatal(err)
	}
	csp := config.Discovery.ConsumerScopedProjection
	if config.Discovery.Mode != "milo" || csp == nil || csp.ProviderProject != "datum-cloud" || len(csp.ServiceNames) != 1 || config.Discovery.ProjectKubeconfigPath != "/etc/fleet/projects/kubeconfig" {
		t.Fatalf("config = %#v", config)
	}
}

func TestParseConfigDefaultsProjectKubeconfigToDiscovery(t *testing.T) {
	config, err := ParseConfig([]byte(strings.Replace(productionShape, "  projectKubeconfigPath: /etc/fleet/projects/kubeconfig\n", "", 1)))
	if err != nil {
		t.Fatal(err)
	}
	if config.Discovery.ProjectKubeconfigPath != "/etc/fleet/discovery/kubeconfig" {
		t.Fatalf("project kubeconfig = %q", config.Discovery.ProjectKubeconfigPath)
	}
}

func TestParseConfigAcceptsSingleMode(t *testing.T) {
	if _, err := ParseConfig([]byte("apiVersion: config.fleet.datumapis.com/v1alpha1\nkind: RequestResolver\ndiscovery:\n  mode: single\n")); err != nil {
		t.Fatal(err)
	}
}

func TestParseConfigFailsClosed(t *testing.T) {
	cases := map[string]string{
		"unknown field":           strings.Replace(productionShape, "  mode: milo\n", "  mode: milo\n  internalServiceDiscovery: true\n", 1),
		"wrong kind":              strings.Replace(productionShape, "kind: RequestResolver", "kind: WorkloadOperator", 1),
		"missing mode":            strings.Replace(productionShape, "  mode: milo\n", "", 1),
		"unsupported mode":        strings.Replace(productionShape, "mode: milo", "mode: kind", 1),
		"all projects":            strings.SplitAfter(productionShape, "projectKubeconfigPath: /etc/fleet/projects/kubeconfig\n")[0],
		"missing discovery":       strings.Replace(productionShape, "  discoveryKubeconfigPath: /etc/fleet/discovery/kubeconfig\n", "", 1),
		"missing provider":        strings.Replace(productionShape, "    providerProject: datum-cloud\n", "", 1),
		"other service":           strings.Replace(productionShape, "metal.datumapis.com", "compute.datumapis.com", 1),
		"extra service":           productionShape + "      - compute.datumapis.com\n",
		"single with kubeconfigs": "apiVersion: config.fleet.datumapis.com/v1alpha1\nkind: RequestResolver\ndiscovery:\n  mode: single\n  discoveryKubeconfigPath: /x\n",
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseConfig([]byte(data)); !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("err = %v, want ErrInvalidConfig", err)
			}
		})
	}
}

func TestLoadConfigRequiresAPath(t *testing.T) {
	if _, err := LoadConfig(""); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("err = %v", err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(productionShape), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err != nil {
		t.Fatal(err)
	}
}

func TestNewConsumerDiscoveryScopesToTheProviderProject(t *testing.T) {
	scheme := testScheme(t)
	if err := servicesv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	server := newDiscoveryServer(t)
	root := &rest.Config{Host: server.URL}
	template := &rest.Config{Host: server.URL + "/template"}
	csp := ConsumerScopedProjectionConfig{ProviderProject: "datum-cloud", ServiceNames: []string{MetalServiceName}}
	discovery, err := NewConsumerDiscovery(root, template, csp, scheme)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := discovery.ProviderManager.GetConfig().Host, server.URL+"/apis/resourcemanager.miloapis.com/v1alpha1/projects/datum-cloud/control-plane"; got != want {
		t.Fatalf("provider project host = %q, want %q", got, want)
	}
	if _, ok := any(discovery.Provider).(multicluster.ProviderRunnable); !ok {
		t.Fatal("consumer provider no longer needs non-leader start wiring; revisit NewManager")
	}
	if _, err := NewConsumerDiscovery(root, template, ConsumerScopedProjectionConfig{ProviderProject: "datum-cloud", ServiceNames: []string{"compute.datumapis.com"}}, scheme); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("other service err = %v", err)
	}
}

// newDiscoveryServer serves just enough synthetic API discovery for
// controller-runtime to build REST mappings for the service catalog types. It
// never serves objects; the test never starts the managers.
func newDiscoveryServer(t *testing.T) *httptest.Server {
	t.Helper()
	group := metav1.APIGroup{
		Name:             servicesv1alpha1.GroupVersion.Group,
		Versions:         []metav1.GroupVersionForDiscovery{{GroupVersion: servicesv1alpha1.GroupVersion.String(), Version: servicesv1alpha1.GroupVersion.Version}},
		PreferredVersion: metav1.GroupVersionForDiscovery{GroupVersion: servicesv1alpha1.GroupVersion.String(), Version: servicesv1alpha1.GroupVersion.Version},
	}
	resources := metav1.APIResourceList{GroupVersion: servicesv1alpha1.GroupVersion.String(), APIResources: []metav1.APIResource{
		{Name: "services", Kind: "Service", Namespaced: false, Verbs: []string{"get", "list", "watch"}},
		{Name: "serviceconsumers", Kind: "ServiceConsumer", Namespaced: true, Verbs: []string{"get", "list", "watch"}},
	}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var body any
		switch {
		case strings.HasSuffix(r.URL.Path, "/api"):
			body = metav1.APIVersions{Versions: []string{}}
		case strings.HasSuffix(r.URL.Path, "/apis"):
			body = metav1.APIGroupList{Groups: []metav1.APIGroup{group}}
		case strings.HasSuffix(r.URL.Path, "/apis/"+servicesv1alpha1.GroupVersion.String()):
			body = resources
		default:
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(server.Close)
	return server
}
