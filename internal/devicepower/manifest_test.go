package devicepower

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"
)

func TestShippedConfigLoads(t *testing.T) {
	t.Setenv("POD_NAMESPACE", siteNS)
	c, err := LoadConfig(filepath.Join(siteRoot, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if c.WorkerNamespace != siteNS || c.ObserveTimeout.Duration.Minutes() != 3 {
		t.Fatalf("config = %+v", c)
	}
}

func TestDispatcherDeploymentAndRBAC(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(siteRoot, "deployment.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var d appsv1.Deployment
	if err := yaml.UnmarshalStrict(data, &d); err != nil {
		t.Fatal(err)
	}
	pod := d.Spec.Template.Spec
	if d.Spec.Replicas == nil || *d.Spec.Replicas != 1 || pod.HostNetwork || pod.SecurityContext == nil || !*pod.SecurityContext.RunAsNonRoot || len(pod.Containers) != 1 {
		t.Fatal("the dispatcher is one unprivileged replica")
	}
	for _, v := range pod.Volumes {
		if v.Secret != nil && v.Secret.SecretName != "fleet-device-power-milo-kubeconfig" {
			t.Fatalf("the dispatcher mounts Secret %s", v.Secret.SecretName)
		}
	}
	// The dispatcher holds no Secret or Pod permission and cannot change a
	// Job once created.
	for _, r := range decodeFile[rbacv1.Role](t, filepath.Join(siteRoot, "rbac.yaml"))[1].Rules {
		if slices.Contains(r.Resources, "secrets") || slices.Contains(r.Resources, "pods") || slices.Contains(r.Verbs, "*") || slices.Contains(r.Verbs, "escalate") || slices.Contains(r.Verbs, "bind") {
			t.Fatalf("dispatcher rule %+v", r)
		}
		if slices.Contains(r.Resources, "jobs") && (slices.Contains(r.Verbs, "update") || slices.Contains(r.Verbs, "patch") || slices.Contains(r.Verbs, "delete")) {
			t.Fatalf("dispatcher may change Jobs: %+v", r)
		}
	}
}
