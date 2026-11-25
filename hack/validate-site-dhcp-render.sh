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
service = one("Service", "fleet-site-dhcp-feed")
assert service["spec"]["type"] == "ClusterIP" and service["spec"]["ports"][0]["port"] == 8081
kea = one("StatefulSet", "fleet-site-dhcp")["spec"]
assert kea["replicas"] >= 2 and kea["template"]["spec"]["hostNetwork"] is True
containers = {c["name"]: c for c in kea["template"]["spec"]["containers"]}
assert "@sha256:" in containers["kea"]["image"], "Kea is pinned by digest"
sync = containers["sync"]["command"]
assert "-ca-file=/etc/fleet-dhcp-tls/ca.crt" in sync and any(a.startswith("-feed=https://") for a in sync), sync
socket = next(a for a in sync if a.startswith("-kea-socket=")).split("=", 1)[1]
base = json.loads(one("ConfigMap", "fleet-site-dhcp-base")["data"]["kea-dhcp4.conf"])["Dhcp4"]
assert base["control-socket"]["socket-name"] == socket and socket.startswith("/var/run/kea/"), "Kea and the synchronizer share one socket under /var/run/kea"
assert all("pools" not in s for s in base["subnet4"]), "the base configuration has no pools"
assert any(e["name"] == "POD_NAME" for e in containers["sync"]["env"]), "the instance name is the Pod name"
print("PASS site DHCP render: %d objects, feed over TLS, Kea pinned, no Secret, no pool" % len(docs))
'
