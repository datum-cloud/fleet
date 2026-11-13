//go:build ignore

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	apps "k8s.io/api/apps/v1"
	core "k8s.io/api/core/v1"
	networking "k8s.io/api/networking/v1"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
)

func require(ok bool, message string) {
	if !ok {
		fmt.Fprintln(os.Stderr, message)
		os.Exit(1)
	}
}

func main() {
	objects := map[string]json.RawMessage{}
	decoder := utilyaml.NewYAMLOrJSONDecoder(os.Stdin, 4096)
	for {
		var raw json.RawMessage
		err := decoder.Decode(&raw)
		if err == io.EOF {
			break
		}
		require(err == nil, fmt.Sprint(err))
		var meta struct {
			Kind     string
			Metadata struct{ Name, Namespace string }
		}
		require(json.Unmarshal(raw, &meta) == nil, "invalid manifest")
		require(meta.Kind != "Secret", "staging credentials must be supplied externally")
		objects[meta.Kind+"/"+meta.Metadata.Name] = raw
	}
	read := func(key string, out any) {
		raw, ok := objects[key]
		require(ok, "missing "+key)
		require(json.Unmarshal(raw, out) == nil, "invalid "+key)
	}
	var manager, provider apps.Deployment
	var service core.Service
	var pvc core.PersistentVolumeClaim
	var account core.ServiceAccount
	var policy networking.NetworkPolicy
	read("Deployment/fleet-manager", &manager)
	read("Deployment/fleet-synthetic-provider", &provider)
	read("Service/fleet-synthetic-provider", &service)
	read("PersistentVolumeClaim/fleet-synthetic-provider-state", &pvc)
	read("ServiceAccount/fleet-synthetic-provider", &account)
	read("NetworkPolicy/fleet-synthetic-provider", &policy)
	require(len(manager.Spec.Template.Spec.Containers) == 1, "manager must not contain a simulator sidecar")
	require(len(provider.Spec.Template.Spec.Containers) == 1, "provider must contain one simulator")
	require(provider.Spec.Replicas != nil && *provider.Spec.Replicas == 1 && provider.Spec.Strategy.Type == apps.RecreateDeploymentStrategyType, "provider requires one replica and Recreate to protect single-writer state")
	require(provider.Spec.Template.Spec.AutomountServiceAccountToken != nil && !*provider.Spec.Template.Spec.AutomountServiceAccountToken, "provider must not mount a Kubernetes API credential")
	require(account.AutomountServiceAccountToken != nil && !*account.AutomountServiceAccountToken, "provider account must disable token automount")
	require(provider.Spec.Template.Spec.ServiceAccountName == account.Name, "provider must use its own account")
	require(pvc.Annotations["kustomize.toolkit.fluxcd.io/prune"] == "disabled", "provider state must survive Flux pruning")
	require(len(pvc.Spec.AccessModes) == 1 && pvc.Spec.AccessModes[0] == core.ReadWriteOnce, "state requires ReadWriteOnce storage")
	m, p := manager.Spec.Template.Spec.Containers[0], provider.Spec.Template.Spec.Containers[0]
	require(m.Image == p.Image, "manager and provider must use the published release image")
	require(len(p.Command) == 1 && p.Command[0] == "/fleet-simulator", "provider must run the simulator")
	has := func(args []string, value string) bool {
		for _, arg := range args {
			if arg == value {
				return true
			}
		}
		return false
	}
	require(has(m.Args, "--synthetic-provider-url=http://"+service.Name+"."+service.Namespace+".svc:8090"), "manager must use provider Service DNS")
	require(has(m.Args, "--synthetic-provider-token-file=/var/run/fleet/provider/token") && has(p.Args, "--runtime-token-file=/var/run/fleet/provider/token"), "both processes need the dedicated runtime credential")
	require(has(p.Args, "--listen-address=0.0.0.0:8090") && has(p.Args, "--state-file=/var/lib/fleet/state.json"), "provider runtime and persistent state paths must match mounts")
	for _, arg := range p.Args {
		require(!strings.HasPrefix(arg, "--admin-"), "provider must not expose an admin listener")
	}
	for _, pod := range []core.PodSpec{manager.Spec.Template.Spec, provider.Spec.Template.Spec} {
		mounted := false
		for _, mount := range pod.Containers[0].VolumeMounts {
			if mount.MountPath != "/var/run/fleet/provider" {
				continue
			}
			require(mount.ReadOnly && mount.SubPath == "", "runtime token must be read-only and support Secret rotation")
			for _, volume := range pod.Volumes {
				if volume.Name == mount.Name && volume.Secret != nil && volume.Secret.SecretName == "fleet-synthetic-provider-auth" {
					for _, item := range volume.Secret.Items {
						if item.Key == "token" && item.Path == "token" {
							mounted = true
						}
					}
				}
			}
		}
		require(mounted, "runtime token mount must resolve to the external Secret")
	}
	persistent := false
	for _, mount := range p.VolumeMounts {
		if mount.MountPath != "/var/lib/fleet" {
			continue
		}
		for _, volume := range provider.Spec.Template.Spec.Volumes {
			if volume.Name == mount.Name && volume.PersistentVolumeClaim != nil && volume.PersistentVolumeClaim.ClaimName == pvc.Name {
				persistent = true
			}
		}
	}
	require(persistent, "provider state must be backed by its retained PVC")
	require(service.Spec.Type == "" || service.Spec.Type == core.ServiceTypeClusterIP, "provider service must remain internal")
	require(len(service.Spec.Ports) == 1 && service.Spec.Ports[0].Port == 8090 && service.Spec.Ports[0].TargetPort.StrVal == "runtime", "Service must expose only the runtime port")
	require(len(p.Ports) == 1 && p.Ports[0].Name == "runtime" && p.Ports[0].ContainerPort == 8090, "Service target port must resolve")
	require(len(service.Spec.Selector) > 0, "Service must have a selector")
	for key, value := range service.Spec.Selector {
		require(provider.Spec.Template.Labels[key] == value && manager.Spec.Template.Labels[key] != value, "Service must select only the provider")
	}
	require(policy.Spec.PodSelector.MatchLabels["app.kubernetes.io/name"] == "fleet-synthetic-provider", "policy must select the provider")
	require(len(policy.Spec.PolicyTypes) == 2 && len(policy.Spec.Egress) == 0, "provider must deny egress")
	require(len(policy.Spec.Ingress) == 1 && len(policy.Spec.Ingress[0].From) == 1 && len(policy.Spec.Ingress[0].Ports) == 1, "provider must have one narrow ingress rule")
	peer := policy.Spec.Ingress[0].From[0]
	require(peer.NamespaceSelector == nil && peer.IPBlock == nil && peer.PodSelector != nil && len(peer.PodSelector.MatchLabels) == 1 && peer.PodSelector.MatchLabels["app.kubernetes.io/name"] == "fleet-manager", "only same-namespace manager pods may call the provider")
	port := policy.Spec.Ingress[0].Ports[0]
	require(port.Port != nil && port.Port.IntVal == 8090 && port.Protocol != nil && *port.Protocol == core.ProtocolTCP, "network policy must allow only the runtime port")
	fmt.Println("synthetic provider deployment, authentication, routing, isolation, and state checks passed")
}
