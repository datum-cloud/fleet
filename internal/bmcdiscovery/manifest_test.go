package bmcdiscovery

import (
	"os"
	"path/filepath"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	"sigs.k8s.io/yaml"
)

var baseDir = filepath.Join("..", "..", "config", "base", "bmc-discovery")

func TestShippedConfigLoads(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "bmc-discovery")
	c, err := LoadConfig(filepath.Join(baseDir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if c.CredentialNamespace != "bmc-discovery" || c.Interval.Duration.Hours() != 1 {
		t.Fatalf("config = %+v", c)
	}
}

func TestDeploymentIsSingleUnprivilegedReplica(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(baseDir, "deployment.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var d appsv1.Deployment
	if err := yaml.UnmarshalStrict(data, &d); err != nil {
		t.Fatal(err)
	}
	if d.Spec.Replicas == nil || *d.Spec.Replicas != 1 || d.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType {
		t.Fatal("discovery has no leader election: it must be one replica with Recreate")
	}
	pod := d.Spec.Template.Spec
	if pod.SecurityContext == nil || pod.SecurityContext.RunAsNonRoot == nil || !*pod.SecurityContext.RunAsNonRoot || pod.HostNetwork {
		t.Fatal("pod must run as non-root without host networking")
	}
	if len(pod.Containers) != 1 {
		t.Fatal("discovery is one container")
	}
	sc := pod.Containers[0].SecurityContext
	if sc == nil || sc.ReadOnlyRootFilesystem == nil || !*sc.ReadOnlyRootFilesystem || sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation ||
		sc.Privileged != nil || sc.Capabilities == nil || len(sc.Capabilities.Add) != 0 || len(sc.Capabilities.Drop) != 1 || sc.Capabilities.Drop[0] != "ALL" {
		t.Fatalf("container security context = %+v", sc)
	}
}
