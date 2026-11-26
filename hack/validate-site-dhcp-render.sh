#!/usr/bin/env bash
# Renders the site DHCP service with its synthetic base configuration and
# checks the contract docs/operations/boot-network.md states.
set -euo pipefail
cd "$(dirname "$0")/.."
kustomize build config/site/dhcp/example | python3 -c '
import json, sys
import yaml
docs = [d for d in yaml.safe_load_all(sys.stdin) if d]
def one(kind, name):
    found = [d for d in docs if d["kind"] == kind and d["metadata"]["name"] == name]
    assert len(found) == 1, (kind, name)
    return found[0]
assert not [d for d in docs if d["kind"] == "Secret"], "the render must carry no Secret"
boot = one("Deployment", "fleet-site-boot")["spec"]["template"]["spec"]
feed = next(c for c in boot["containers"] if c["name"] == "dhcp-feed")
assert feed["command"][:2] == ["fleet-multiplane", "dhcp-feed"] and "-tls-cert" in feed["command"] and "-tls-key" in feed["command"], feed["command"]
assert {"name": "state", "mountPath": "/var/lib/fleet-local", "subPath": "journal", "readOnly": True} in feed["volumeMounts"], "the feed reads the store read-only"
ref = next(e["valueFrom"]["configMapKeyRef"] for e in feed["env"] if e["name"] == "FLEET_REDFISH_ENROLLMENT")
enrollment = json.loads(one("ConfigMap", ref["name"])["data"][ref["key"]])
service = one("Service", "fleet-site-dhcp-feed")
assert service["spec"]["type"] == "ClusterIP" and service["spec"]["ports"][0]["port"] == 8081
kea = one("StatefulSet", "fleet-site-dhcp")["spec"]
assert kea["replicas"] >= 2 and not kea["template"]["spec"].get("hostNetwork"), "Kea runs in the central cluster without host networking"
lb = one("Service", "fleet-site-dhcp")["spec"]
assert lb["type"] == "LoadBalancer" and lb["ports"] == [{"name": "dhcp", "port": 67, "targetPort": 67, "protocol": "UDP"}], lb
assert lb["selector"] == kea["selector"]["matchLabels"], "one address in front of every instance"
assert lb.get("sessionAffinity") == "ClientIP", "one relay exchange stays on one instance"
containers = {c["name"]: c for c in kea["template"]["spec"]["containers"]}
assert "@sha256:" in containers["kea"]["image"], "Kea is pinned by digest"
sync = containers["sync"]["command"]
assert "-ca-file=/etc/fleet-dhcp-tls/ca.crt" in sync and "-feed=${FLEET_DHCP_FEED_URL}" in sync, sync
socket = next(a for a in sync if a.startswith("-kea-socket=")).split("=", 1)[1]
base = json.loads(one("ConfigMap", "fleet-site-dhcp-base")["data"]["kea-dhcp4.conf"])["Dhcp4"]
assert base["control-socket"]["socket-name"] == socket and socket.startswith("/var/run/kea/"), "Kea and the synchronizer share one socket under /var/run/kea"
assert all("pools" not in s for s in base["subnet4"]), "the base configuration has no pools"
assert base["interfaces-config"]["dhcp-socket-type"] == "udp", "Kea answers relayed requests on a UDP socket"
assert base.get("authoritative") is False, "an instance without the reservation stays silent instead of refusing"
assert all("next-server" not in s for s in base["subnet4"]), "no TFTP server"
assert any(c["name"] == "HTTPClient" and c["boot-file-name"].startswith("http") for c in base["client-classes"]), "UEFI HTTP boot"
assert any(e["name"] == "POD_NAME" for e in containers["sync"]["env"]), "the instance name is the Pod name"
pods = ["%s-%d" % (one("StatefulSet", "fleet-site-dhcp")["metadata"]["name"], i) for i in range(kea["replicas"])]
assert enrollment["dhcp"] == "synced" and enrollment["dhcpInstances"] == pods, "the expected DHCP instances are exactly the StatefulSet Pods: %s" % enrollment["dhcpInstances"]
assert "chmod 0750 /var/run/kea" in " ".join(containers["kea"]["command"]), "Kea 2.6 needs its socket directory 0750"
for pod in (boot, kea["template"]["spec"]):
    volumes = {v["name"] for v in pod["volumes"]}
    for container in pod["containers"]:
        missing = {m["name"] for m in container.get("volumeMounts", [])} - volumes
        assert not missing, (container["name"], missing)
labels = one("Deployment", "fleet-site-boot")["spec"]["template"]["metadata"]["labels"]
assert all(labels.get(k) == v for k, v in service["spec"]["selector"].items()), "the feed Service selects the boot Pod"
budget = one("PodDisruptionBudget", "fleet-site-dhcp")["spec"]["selector"]["matchLabels"]
assert budget == kea["selector"]["matchLabels"], "the budget covers the Kea Pods"
print("PASS site DHCP render: %d objects, feed over TLS to the expected instances, Kea pinned behind one UDP 67 load balancer, HTTP boot, no Secret, no pool" % len(docs))
'
