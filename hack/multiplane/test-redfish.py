#!/usr/bin/env python3
"""Offline checks for the Redfish acceptance mode (run.sh --redfish).

Renders the Redfish site overlays and a staged Redfish source revision with
the real kustomize, and checks the static contracts the live run relies on:
workers have no hypervisor path, only the runtime is privileged, the lab
enrollment matches the lab targets, and every phase is implemented. Never
contacts a container runtime or cluster.
"""
import argparse
import importlib
import json
import os
import pathlib
import re
import shutil
import subprocess
import sys
import tempfile

root = pathlib.Path(sys.argv[1]).resolve()
os.environ['FLEET_SITE_PROVIDER'] = 'redfish'
sys.path.insert(0, str(root / 'hack/multiplane'))
import flux  # noqa: E402  (reads FLEET_SITE_PROVIDER at import)


def documents(text):
    """Rendered YAML as objects: PyYAML when present, otherwise yq (both are on CI runners)."""
    try:
        import yaml
        return [d for d in yaml.safe_load_all(text) if d]
    except ImportError:
        out = subprocess.run(['yq', '-o=json', '-I=0', '.'], input=text, text=True, capture_output=True, check=True).stdout
        return [json.loads(line) for line in out.splitlines() if line.strip() not in ('', 'null')]

failures = []


def check(name, ok, detail=''):
    if not ok:
        failures.append(name + (': ' + detail if detail else ''))


def build(path):
    r = subprocess.run(['kustomize', 'build', str(path)], text=True, capture_output=True)
    if r.returncode:
        failures.append('render %s: %s' % (path, r.stderr[-300:]))
        return []
    return documents(r.stdout)


graph = build(root / 'config/site/redfish/graph')
rgd = next((d for d in graph if d.get('kind') == 'ResourceGraphDefinition'), {})
resources = {r['id']: r for r in rgd.get('spec', {}).get('resources', [])}
worker = resources.get('workers', {}).get('template', {}).get('spec', {}).get('template', {}).get('spec', {})
container = (worker.get('containers') or [{}])[0]
env = {e['name']: e.get('value') for e in container.get('env', [])}
mounts = container.get('volumeMounts', [])
check('Redfish worker selects the Redfish provider', env.get('FLEET_SITE_PROVIDER') == 'redfish' and 'FLEET_LIBVIRT_URI' not in env, str(env.keys()))
check('Redfish worker has no libvirt socket or lab disk mount', not any('libvirt' in m.get('subPath', '') + m['mountPath'] or 'lab-disks' in m.get('subPath', '') for m in mounts), str(mounts))
check('Redfish worker console capture is read-only', any(m.get('subPath') == 'lab-console' and m.get('readOnly') for m in mounts))
check('Redfish worker mounts no credential and no host path', not any('secret' in v or 'hostPath' in v for v in worker.get('volumes', [])), str(worker.get('volumes')))
check('Redfish worker reads per-Device credentials from the credential namespace', env.get('FLEET_SITE_CREDENTIALS_NAMESPACE') == 'fleet-site-credentials'
      and env.get('FLEET_SITE_PROJECT') == 'provider', str(env.keys()))
creds = resources.get('workerCredentialRoles', {}).get('template', {})
check("per-operation credential Role gets only the handoff's one named Secret, in the credential namespace", creds.get('metadata', {}).get('namespace') == 'fleet-site-credentials'
      and creds.get('rules') == [{'apiGroups': [''], 'resources': ['secrets'], 'resourceNames': ['${handoff.spec.credential}'], 'verbs': ['get']}], json.dumps(creds)[:300])
binding = resources.get('workerCredentialBindings', {}).get('template', {})
check('per-operation credential RoleBinding binds its own Role to its own ServiceAccount', binding.get('metadata', {}).get('namespace') == 'fleet-site-credentials'
      and binding.get('roleRef', {}).get('name') == '${"w-" + handoff.metadata.name}'
      and binding.get('subjects') == [{'kind': 'ServiceAccount', 'name': '${"w-" + handoff.metadata.name}', 'namespace': 'fleet-site'}], json.dumps(binding)[:300])
check('Redfish worker waits for the Flux-owned BMC', 'bmc' in resources and resources['bmc'].get('externalRef', {}).get('metadata', {}).get('name') == 'fleet-site-bmc'
      and '${bmc.metadata.name}' in json.dumps(resources.get('workers', {})))
check('graph ids still valid for kro', all(re.fullmatch(r'[a-z][a-zA-Z0-9]*', i) for i in resources) and 'runtime' not in resources, str(list(resources)))
check('privileged nothing in the Redfish graph', 'privileged' not in json.dumps(rgd))

enrollment = json.loads(env.get('FLEET_REDFISH_ENROLLMENT', '{}'))
targets = json.loads((root / 'hack/redfish-lab/targets.json').read_text())
by_system = {t['system']: t for t in targets}
check('every enrolled BMC endpoint is the fixed BMC Service', enrollment.get('targets') and all(t.get('endpoint') == 'https://%s:8443' % flux.BMC_SERVICE_IP for t in enrollment['targets']))
check('lab DHCP is static and the enrollment names no boot NIC or address', enrollment.get('dhcp') == 'static' and not any('provisioning' in t or 'mac' in t for t in enrollment.get('targets', [])))
check('a static enrollment names no DHCP instances', 'dhcpInstances' not in enrollment, str(enrollment.get('dhcpInstances')))
check('enrollment matches the lab targets exactly', len(enrollment.get('targets', [])) == 2 and all(
    t['systemID'] in by_system and by_system[t['systemID']]['uuid'] == t['systemUUID']
    and by_system[t['systemID']]['console'].endswith('/' + t['console']) for t in enrollment.get('targets', [])), json.dumps(enrollment)[:300])
check('only target A is the published capacity Device', [t.get('device') for t in enrollment.get('targets', [])][:1] == ['fleet-provider/synthetic-device-central-01']
      and enrollment['targets'][0].get('systemID') == 'System-1')

services = build(root / 'config/site/redfish/services')
deployments = {d['metadata']['name']: d for d in services if d.get('kind') == 'Deployment'}
check('Redfish services add the BMC', sorted(deployments) == ['fleet-site-bmc', 'fleet-site-boot', 'fleet-site-observer', 'fleet-site-runtime'], str(sorted(deployments)))
privileged = [n for n, d in deployments.items() if any(c.get('securityContext', {}).get('privileged') for c in d['spec']['template']['spec']['containers'])]
check('only the runtime is privileged in Redfish mode', privileged == ['fleet-site-runtime'], str(privileged))
socket_holders = sorted(n for n, d in deployments.items() if any(m.get('subPath') == 'libvirt-socket' for c in d['spec']['template']['spec']['containers'] for m in c.get('volumeMounts', [])))
check('only the runtime and the lab BMC hold the libvirt socket', socket_holders == ['fleet-site-bmc', 'fleet-site-runtime'], str(socket_holders))
bmc = deployments.get('fleet-site-bmc', {})
check('BMC image and Service IP substituted by Flux', '${FLEET_BMC_IMAGE}' in json.dumps(bmc) and any(
    d.get('kind') == 'Service' and d['metadata']['name'] == 'fleet-site-bmc' and d['spec']['clusterIP'] == '${FLEET_BMC_SERVICE_IP}' for d in services))
check('every site role selects the Redfish provider', all(any(e.get('name') == 'FLEET_SITE_PROVIDER' and e.get('value') == 'redfish' for e in deployments[n]['spec']['template']['spec']['containers'][0].get('env', []))
                                                          for n in ('fleet-site-runtime', 'fleet-site-boot', 'fleet-site-observer')))

check('static Redfish services run no DHCP of their own', not any(c['name'] in ('kea', 'dhcp-sync', 'dhcp-feed') for d in deployments.values() for c in d['spec']['template']['spec']['containers']))

# Synchronized lab DHCP (run.sh --synced-dhcp).
synced_graph = build(root / 'config/site/redfish/synced/graph')
synced_rgd = next((d for d in synced_graph if d.get('kind') == 'ResourceGraphDefinition'), {})
synced_env = {e['name']: e.get('value') for e in next((r for r in synced_rgd.get('spec', {}).get('resources', []) if r['id'] == 'workers'), {}).get('template', {}).get('spec', {}).get('template', {}).get('spec', {}).get('containers', [{}])[0].get('env', [])}
synced_enrollment = json.loads(synced_env.get('FLEET_REDFISH_ENROLLMENT', '{}'))
check('synced graph changes only the DHCP mode and declares the one lab instance', synced_enrollment.get('dhcp') == 'synced' and synced_enrollment.get('dhcpInstances') == ['lab-kea']
      and {k: v for k, v in dict(synced_enrollment, dhcp='static').items() if k != 'dhcpInstances'} == enrollment
      and {k: v for k, v in synced_env.items() if k != 'FLEET_REDFISH_ENROLLMENT'} == {k: v for k, v in env.items() if k != 'FLEET_REDFISH_ENROLLMENT'})
synced_services = build(root / 'config/site/redfish/synced/services')
synced_deployments = {d['metadata']['name']: d for d in synced_services if d.get('kind') == 'Deployment'}
pod = lambda n: synced_deployments.get(n, {}).get('spec', {}).get('template', {}).get('spec', {})
runtime_containers = {c['name']: c for c in pod('fleet-site-runtime').get('containers', [])}
check('synced runtime adds Kea and the synchronizer beside libvirt and the report relay', sorted(runtime_containers) == ['dhcp-sync', 'kea', 'libvirt', 'report-relay'], str(sorted(runtime_containers)))
check('Kea and the synchronizer are not privileged and add only the network capabilities Kea needs',
      not any(runtime_containers.get(n, {}).get('securityContext', {}).get('privileged') for n in ('kea', 'dhcp-sync'))
      and runtime_containers.get('kea', {}).get('securityContext', {}).get('capabilities') == {'drop': ['ALL'], 'add': ['NET_BIND_SERVICE', 'NET_RAW']}
      and runtime_containers.get('dhcp-sync', {}).get('securityContext', {}).get('capabilities') == {'drop': ['ALL']})
sync_args = runtime_containers.get('dhcp-sync', {}).get('command', [])
check('the synchronizer reports as one fixed instance over TLS', '-instance=lab-kea' in sync_args and '-feed=https://fleet-site-dhcp-feed.fleet-site.svc:8081' in sync_args
      and '-ca-file=/etc/fleet-dhcp-tls/ca.crt' in sync_args, str(sync_args))
check('Kea makes its control socket directory 0750 and waits for the lab bridge', 'chmod 0750 /var/run/kea' in ' '.join(runtime_containers.get('kea', {}).get('command', []))
      and 'fleetpxe0' in ' '.join(runtime_containers.get('kea', {}).get('command', [])))
check('the feed runs beside the boot service', any(c['name'] == 'dhcp-feed' and c['command'][:2] == ['fleet-multiplane', 'dhcp-feed'] for c in pod('fleet-site-boot').get('containers', [])))
feed_env = next((c.get('env', []) for c in pod('fleet-site-boot').get('containers', []) if c['name'] == 'dhcp-feed'), [])
feed_ref = next((e.get('valueFrom', {}).get('configMapKeyRef', {}) for e in feed_env if e['name'] == 'FLEET_REDFISH_ENROLLMENT'), {})
feed_enrollment = json.loads(next((d['data'].get(feed_ref.get('key'), '{}') for d in synced_services if d.get('kind') == 'ConfigMap' and d['metadata']['name'] == feed_ref.get('name')), '{}'))
check('the feed reads exactly the workers\' synced enrollment', feed_ref and feed_enrollment == synced_enrollment, json.dumps(feed_ref))
check('the expected DHCP instance set is the one synchronizer\'s instance', ['-instance=' + i for i in feed_enrollment.get('dhcpInstances', [])] == [a for a in sync_args if a.startswith('-instance=')], str(feed_enrollment.get('dhcpInstances')))
check('the lab BMC defines the network without DHCP', any(e == {'name': 'FLEET_LAB_DHCP', 'value': 'synced'} for e in pod('fleet-site-bmc').get('containers', [{}])[0].get('env', [])))
kea_config = json.loads(next((d['data']['kea-dhcp4.conf'] for d in synced_services if d.get('kind') == 'ConfigMap' and d['metadata']['name'] == 'fleet-site-dhcp-base'), '{}'))
subnets = kea_config.get('Dhcp4', {}).get('subnet4', [])
check('lab Kea serves the bridge subnet from reservations only, with the boot program', kea_config.get('Dhcp4', {}).get('interfaces-config', {}).get('interfaces') == ['fleetpxe0']
      and len(subnets) == 1 and subnets[0].get('subnet') == '192.168.77.0/24' and 'pools' not in subnets[0]
      and subnets[0].get('boot-file-name') == 'http://${FLEET_BOOT_SERVICE_IP}:8080/pxe/boot.ipxe'
      and kea_config['Dhcp4'].get('control-socket', {}).get('socket-name') == '/var/run/kea/kea4-ctrl-socket', json.dumps(kea_config)[:300])
check('no Secret in the synced services', not any(d.get('kind') == 'Secret' for d in synced_services))
for name, d in synced_deployments.items():
    volumes = {v['name'] for v in d['spec']['template']['spec'].get('volumes', [])}
    missing = {m['name'] for c in d['spec']['template']['spec']['containers'] for m in c.get('volumeMounts', [])} - volumes
    check('synced %s mounts only volumes it declares' % name, not missing, str(missing))
os.environ['FLEET_LAB_DHCP'] = 'synced'
importlib.reload(flux)
check('synced Flux inventory holds the base configuration, the feed enrollment and the feed Service',
      {'fleet-site_fleet-site-dhcp-base__ConfigMap', 'fleet-site_fleet-site-dhcp-feed__Service', 'fleet-site_fleet-site-redfish-enrollment__ConfigMap'} <= set(flux.LAYER_INVENTORY['flux-system/fleet-site-services']))
# Synced mode runs real IPAM on the platform cluster: its namespace,
# PostgreSQL and NetworkPolicy from this source, exactly, and its apiserver
# from IPAM's published manifests pinned by digest.
check('synced platform waits for both IPAM layers', flux.EXPECTED['platform'][-2:] == ['flux-system/ipam-system', 'flux-system/ipam-apiserver'])
ipam_layer = build(root / 'config/multiplane/ipam')
ipam_ids = sorted('%s_%s_%s_%s' % (d['metadata'].get('namespace', ''), d['metadata']['name'], d['apiVersion'].rpartition('/')[0], d['kind']) for d in ipam_layer)
check('IPAM layer inventory is exact', ipam_ids == flux.PLATFORM_LAYER_INVENTORY['flux-system/ipam-system'], str(ipam_ids))
check('no Secret in the IPAM layer', not any(d.get('kind') == 'Secret' for d in ipam_layer))
postgres = next((d for d in ipam_layer if d.get('kind') == 'StatefulSet'), {}).get('spec', {}).get('template', {}).get('spec', {})
check('IPAM PostgreSQL is pinned and reads its password from the enrolled Secret',
      re.search(r'@sha256:[0-9a-f]{64}$', postgres.get('containers', [{}])[0].get('image', '')) is not None
      and {'name': 'POSTGRES_PASSWORD', 'valueFrom': {'secretKeyRef': {'name': 'postgres-credentials', 'key': 'password'}}} in postgres['containers'][0].get('env', []))
ipam_flux = documents((root / 'config/multiplane/clusters/platform-ipam/ipam.yaml').read_text())
ipam_source = next((d for d in ipam_flux if d['kind'] == 'OCIRepository'), {})
ipam_apiserver = next((d for d in ipam_flux if d['kind'] == 'Kustomization' and d['metadata']['name'] == 'ipam-apiserver'), {}).get('spec', {})
check('IPAM manifests and image pinned by digest', ipam_source.get('spec', {}).get('ref') == {'digest': flux.IPAM_KUSTOMIZE_DIGEST}
      and ipam_source['spec']['url'] == 'oci://ghcr.io/milo-os/ipam-kustomize' and ipam_apiserver.get('path') == './base'
      and [i['name'] + '@' + i['digest'] for i in ipam_apiserver.get('images', [])] == [flux.IPAM_IMAGE])
check('IPAM delegates to the provider Milo API with quota off', ipam_apiserver.get('targetNamespace') == 'ipam-system'
      and {'name': 'milo-provider', 'namespace': 'flux-system'} in ipam_apiserver.get('dependsOn', [])
      and all(x in json.dumps(ipam_apiserver['patches']) for x in ('{name: ENABLE_QUOTA, value: \\"false\\"}', 'AUTHORIZATION_KUBECONFIG', 'ipam-milo-credentials', 'ipam-apiserver-tls')))
os.environ['FLEET_LAB_DHCP'] = 'static'
importlib.reload(flux)
check('static mode runs no IPAM', 'flux-system/ipam-apiserver' not in flux.EXPECTED['platform'] and not flux.PLATFORM_LAYER_INVENTORY)
provider_milo = json.dumps(build(root / 'config/multiplane/milo/provider'))
check('provider Milo API runs the aggregation layer', all(f in provider_milo for f in (
    '--proxy-client-cert-file=/private/front-proxy-client.crt', '--requestheader-client-ca-file=/private/front-proxy-ca.crt',
    '--requestheader-allowed-names=fleet-front-proxy-client', '--requestheader-extra-headers-prefix=X-Remote-Extra-')))
milo_py = (root / 'hack/multiplane/milo.py').read_text()
check('the lab claims from IPAM, not a stand-in CRD', 'ipclaims.yaml' not in milo_py and not (root / 'internal/multiplane/ipam.go').exists()
      and "'reservations':{'leading':100,'trailing':56,'unitPrefixLength':32}" in milo_py and "'cidr':'192.168.77.0/24'" in milo_py)

# IPAM evidence: a claim's record must come from inside the boot window, and
# name an allocation that names the claim back and holds exactly address/32.
import redfish_steps  # noqa: E402
state = lambda frm, to, claims, allocations: {'from': frm, 'to': to, 'sample': {'claims': claims, 'allocations': allocations}}
claim = {'name': 'op-a-boot', 'phase': 'Bound', 'address': '192.168.77.100', 'allocation': 'alloc-1'}
allocation = {'name': 'alloc-1', 'claim': 'op-a-boot', 'cidr': '192.168.77.100/32', 'phase': 'Ready'}
t0 = redfish_steps.at('2026-11-20T10:00:00Z').timestamp()
held = [state('2026-11-20T10:00:00Z', '2026-11-20T10:05:00Z', [claim], [allocation])]
check('IPAM record found inside the window', (redfish_steps.ipam_record(held, 'op-a-boot', '192.168.77.100', t0 + 60, t0 + 120) or {}).get('allocation') == 'alloc-1')
check('IPAM record outside the window refused', redfish_steps.ipam_record(held, 'op-a-boot', '192.168.77.100', t0 + 400, t0 + 500) is None)
check('IPAM record for another address refused', redfish_steps.ipam_record(held, 'op-a-boot', '192.168.77.101', t0, t0 + 60) is None)
check('IPAM record for another claim refused', redfish_steps.ipam_record(held, 'op-b-boot', '192.168.77.100', t0, t0 + 60) is None)
for name, bad in (('names another claim', dict(allocation, claim='op-b-boot')), ('holds a wider block', dict(allocation, cidr='192.168.77.100/31'))):
    check('IPAM allocation that %s refused' % name, redfish_steps.ipam_record([state('2026-11-20T10:00:00Z', '2026-11-20T10:05:00Z', [claim], [bad])],
                                                                         'op-a-boot', '192.168.77.100', t0, t0 + 60) is None)
check('a pending claim is not an allocation', redfish_steps.ipam_record([state('2026-11-20T10:00:00Z', '2026-11-20T10:05:00Z', [dict(claim, phase='Pending')], [allocation])],
                                                                        'op-a-boot', '192.168.77.100', t0, t0 + 60) is None)
check('a failed sample is no record', redfish_steps.ipam_record([{'from': '2026-11-20T10:00:00Z', 'to': '2026-11-20T10:05:00Z', 'sample': {'error': 'timed out'}}],
                                                                'op-a-boot', '192.168.77.100', t0, t0 + 60) is None)
failed = lambda frm, to: {'from': frm, 'to': to, 'sample': {'error': 'connection refused'}}
outage = [state('2026-11-20T09:55:00Z', '2026-11-20T09:59:58Z', [claim], [allocation]), failed('2026-11-20T10:00:00Z', '2026-11-20T10:01:00Z')]
check('an IPAM outage inside the window, bracketed by the same allocation, is a record',
      (redfish_steps.ipam_record(outage + [state('2026-11-20T10:01:02Z', '2026-11-20T10:05:00Z', [claim], [allocation])], 'op-a-boot', '192.168.77.100', t0 + 10, t0 + 50) or {}).get('observedTo') == '2026-11-20T10:05:00Z')
check('an outage bracketed by another allocation is no record',
      redfish_steps.ipam_record(outage + [state('2026-11-20T10:01:02Z', '2026-11-20T10:05:00Z', [dict(claim, allocation='alloc-2')], [dict(allocation, name='alloc-2')])],
                                'op-a-boot', '192.168.77.100', t0 + 10, t0 + 50) is None)
check('an answer inside the window without the allocation breaks the bracket',
      redfish_steps.ipam_record([outage[0], state('2026-11-20T10:00:00Z', '2026-11-20T10:00:20Z', [], []), failed('2026-11-20T10:00:22Z', '2026-11-20T10:01:00Z'),
                                 state('2026-11-20T10:01:02Z', '2026-11-20T10:05:00Z', [claim], [allocation])], 'op-a-boot', '192.168.77.100', t0 + 10, t0 + 50) is None)
check('an answer before the window without the allocation clears the sample before it',
      redfish_steps.ipam_record([outage[0], state('2026-11-20T09:59:59Z', '2026-11-20T09:59:59Z', [], []), failed('2026-11-20T10:00:00Z', '2026-11-20T10:01:00Z'),
                                 state('2026-11-20T10:01:02Z', '2026-11-20T10:05:00Z', [claim], [allocation])], 'op-a-boot', '192.168.77.100', t0 + 10, t0 + 50) is None)
owners = redfish_steps.lease_owners([('192.168.77.100', 'm', t0 + 30), ('192.168.77.100', 'm', t0 + 300), ('192.168.77.101', 'm', t0 + 30)],
                                    {'op-a': ('192.168.77.100', t0, t0 + 60), 'op-b': ('192.168.77.100', t0 + 200, t0 + 400)})
check('each Kea lease belongs to the boot window holding its address', [o for _, o in owners] == [['op-a'], ['op-b'], []], str(owners))
check('run.sh accepts --synced-dhcp only with --redfish', "--synced-dhcp) lab_dhcp=synced" in (root / 'hack/multiplane/run.sh').read_text()
      and '[[ $lab_dhcp == static || $provider == redfish ]] || usage' in (root / 'hack/multiplane/run.sh').read_text())
check('the synced acceptance asserts the DHCP evidence', 'rf dhcp-synced' in (root / 'hack/multiplane/acceptance-redfish.sh').read_text())
check('synced evidence requires the DHCP phase', "markers += ('redfish-dhcp-synced: ',)" in (root / 'hack/multiplane/evidence.py').read_text()
      and "print('redfish-dhcp-synced: " in (root / 'hack/multiplane/redfish_steps.py').read_text())

base = build(root / 'config/site/redfish/base')
policy = next((d for d in base if d.get('kind') == 'ValidatingAdmissionPolicy' and d['metadata']['name'] == 'fleet-site-kro-worker-jobs'), {})
text = json.dumps(policy)
check('admission denies libvirt mounts and URIs for kro worker Jobs', "startsWith('/run/fleet-libvirt')" in text and "FLEET_LIBVIRT_URI" in text)
identity = json.dumps(next((d for d in base if d.get('kind') == 'ValidatingAdmissionPolicy' and d['metadata']['name'] == 'fleet-site-kro-worker-identity'), {}))
check('admission confines credential Roles to get on one named Secret', 'fleet-site-credentials' in identity and "r.verbs == ['get']" in identity and 'size(r.resourceNames) == 1' in identity
      and "r.verbs == ['list']" not in identity)
check('BMC identity in the base', any(d.get('kind') == 'ServiceAccount' and d['metadata']['name'] == 'fleet-site-bmc' for d in base))
check('no Secret in any Redfish overlay', not any(d.get('kind') == 'Secret' for d in base + services + graph))

# A staged Redfish revision points the cell's site layers at the overlays.
with tempfile.TemporaryDirectory() as tmp:
    s = pathlib.Path(tmp) / 'state'
    (s / 'flux-tools').mkdir(parents=True)
    (s / 'evidence').mkdir()
    (s / 'prefix').write_text('fleet-mp-0000abcd\n')
    # The same stand-in for the release install.yaml that test-flux.py uses.
    fake = 'apiVersion: v1\nkind: Namespace\nmetadata: {name: flux-system}\n'
    for name in ('source-controller', 'kustomize-controller', 'helm-controller', 'notification-controller', 'image-automation-controller',
                 'image-reflector-controller', 'source-watcher'):
        fake += '---\napiVersion: apps/v1\nkind: Deployment\nmetadata: {name: %s, namespace: flux-system}\nspec:\n  template:\n    spec:\n      containers:\n        - {name: manager, image: ghcr.io/fluxcd/%s:v0, args: []}\n' % (name, name)
    fake += '---\napiVersion: v1\nkind: Service\nmetadata: {name: source-watcher, namespace: flux-system}\n'
    (s / 'flux-tools' / 'install.yaml').write_text(fake)
    flux.INSTALL_SHA256 = flux.sha256(s / 'flux-tools' / 'install.yaml')
    (s / 'gitops-environment.json').write_text(json.dumps({'registryHost': 'fleet-mp-0000abcd-registry', 'registryPort': 5555, 'karmadaPort': 1, 'cell': 'fleet-mp-0000abcd-cell',
                                                           'cellEndpoint': 'https://x:6443', 'siteImage': 'r/fleet-site@sha256:' + 'a' * 64, 'siteImageDigest': 'sha256:' + 'a' * 64,
                                                           'bmcImage': 'r/fleet-bmc@sha256:' + 'b' * 64, 'bmcImageDigest': 'sha256:' + 'b' * 64}))
    args = argparse.Namespace(state=str(s), root=str(root), revision=2, site='off', bad_kro_chart=False, drop_site_volume=True, purpose='test', cluster=[], timeout=5)
    check('stage a Redfish revision', flux.stage(args) == 0)
    cell = subprocess.run(['kustomize', 'build', str(s / 'gitops/r2/environment/cell')], text=True, capture_output=True).stdout
    for layer in ('base', 'services', 'graph'):
        check('cell %s layer uses the Redfish overlay' % layer, 'path: ./config/site/redfish/' + layer in cell)
    check('Redfish environment values defined', 'FLEET_BMC_IMAGE: r/fleet-bmc@sha256:' in cell.replace('"', '') and ('FLEET_BMC_SERVICE_IP: %s' % flux.BMC_SERVICE_IP) in cell.replace('"', ''))
    check('volume-removal patch still targets the Redfish base', '$patch: delete' in cell)
    used = set()
    for f in (root / 'config/site/redfish/services').glob('*.yaml'):
        used |= set(re.findall(r'\$\{(FLEET_[A-Z_]+)\}', f.read_text()))
    defined = set(re.findall(r'^  (FLEET_[A-Z_]+): ', cell, re.M))
    check('Redfish substituted variables defined', used <= defined, str(sorted(used - defined)))
    # The same revision with synchronized lab DHCP selects the synced graph
    # and services and keeps the Redfish base.
    os.environ['FLEET_LAB_DHCP'] = 'synced'
    importlib.reload(flux)
    flux.INSTALL_SHA256 = flux.sha256(s / 'flux-tools' / 'install.yaml')
    shutil.rmtree(s / 'gitops')
    check('stage a synced Redfish revision', flux.stage(args) == 0)
    cell = subprocess.run(['kustomize', 'build', str(s / 'gitops/r2/environment/cell')], text=True, capture_output=True).stdout
    check('synced cell layers use the synced overlays', 'path: ./config/site/redfish/synced/services' in cell and 'path: ./config/site/redfish/synced/graph' in cell
          and 'path: ./config/site/redfish/base' in cell)
    used = set()
    for f in (root / 'config/site/redfish/synced/services').glob('*.yaml'):
        used |= set(re.findall(r'\$\{(FLEET_[A-Z_]+)\}', f.read_text()))
    check('synced substituted variables defined', used <= set(re.findall(r'^  (FLEET_[A-Z_]+): ', cell, re.M)), str(sorted(used)))
    platform = subprocess.run(['kustomize', 'build', str(s / 'gitops/r2/environment/platform')], text=True, capture_output=True).stdout
    check('synced platform reconciles IPAM beside the Milo APIs', 'name: ipam-apiserver' in platform and 'path: ./config/multiplane/ipam' in platform
          and 'url: oci://ghcr.io/milo-os/ipam-kustomize' in platform and 'name: milo-provider' in platform)
    os.environ['FLEET_LAB_DHCP'] = 'static'
    importlib.reload(flux)
    flux.INSTALL_SHA256 = flux.sha256(s / 'flux-tools' / 'install.yaml')
    shutil.rmtree(s / 'gitops')
    check('stage a static Redfish revision', flux.stage(args) == 0)
    platform = subprocess.run(['kustomize', 'build', str(s / 'gitops/r2/environment/platform')], text=True, capture_output=True).stdout
    check('static platform runs no IPAM', 'ipam' not in platform and 'name: milo-provider' in platform)
check('ownership audit expects the BMC', ('deployments', 'fleet-site', 'fleet-site-bmc') in flux.SHARED and 'fleet-site-bmc' in flux.SITE_DEPLOYMENTS)

# Every Redfish phase is implemented, and the lab fault hooks stay in the lab.
sequence = (root / 'hack/multiplane/acceptance-redfish.sh').read_text()
steps = (root / 'hack/multiplane/steps.sh').read_text()
redfish_steps = (root / 'hack/multiplane/redfish_steps.py').read_text()
for name in re.findall(r'^step (\S+)', sequence, re.M):
    check('step implemented: ' + name, ' %s)' % name in steps or '|%s)' % name in steps or ' %s|' % name in steps)
for name in re.findall(r'^rf (\S+)', sequence, re.M):
    check('redfish step implemented: ' + name, "'%s':" % name in redfish_steps)
order = ['baseline', 'discovery-faults', 'allocate', 'arm-lost-response', 'dispatch', 'bmc-interrupt', 'interrupt-pod', 'interrupt-job', 'lost-response', 'resume', 'release',
         'reuse', 'delivery-faults', 'deadline-admit', 'machine-delete', 'deadline-hold', 'release-b', 'deadline-settled', 'rebuild', 'boot-denials', 'isolation', 'evidence']
positions = [sequence.find(' ' + n + ' ') for n in order]
check('Redfish phase order', all(p >= 0 for p in positions) and positions == sorted(positions), str(list(zip(order, positions))))
for path in sorted(list((root / 'internal/redfish').glob('*.go')) + list((root / 'internal/site/redfishwork').glob('*.go'))):
    if not path.name.endswith('_test.go'):
        check('no lab fault hook in production code: ' + path.name, not re.search(r'\bfaults?\b|drop-response|hide-reset', path.read_text(), re.I))
adapter = (root / 'hack/redfish-lab/fleet_lab_adapter.py').read_text()
check('adapter keeps upstream unmodified (imports it)', 'import emulator' in adapter and 'COPY --from=upstream . /opt/redfish-emulator' in (root / 'hack/redfish-lab/Containerfile').read_text())
check('upstream source pinned by digest and commit', re.search(r'sha256=[0-9a-f]{64}', (root / 'hack/redfish-lab/upstream.sh').read_text()) is not None
      and '5f51d8ac48e46665a2a85242434f125aa7b25172' in (root / 'hack/redfish-lab/upstream.sh').read_text())
check('BMC base image pinned by digest', re.search(r'^FROM \S+@sha256:[0-9a-f]{64}$', (root / 'hack/redfish-lab/Containerfile').read_text(), re.M) is not None)
lock = (root / 'hack/redfish-lab/requirements.lock').read_text()
check('Python dependencies hash-pinned', all('==' in l for l in lock.splitlines() if l and not l.startswith(('#', ' '))) and lock.count('--hash=sha256:') >= 18)
check('no OpenStack dependency', not re.search(r'sushy|openstack|ironic', (lock + adapter + (root / 'hack/redfish-lab/Containerfile').read_text()).lower()))

run_sh = (root / 'hack/multiplane/run.sh').read_text()
# $state/provider is the Milo provider project's credential directory.
check('site provider marker does not collide with the provider project state', '"$state/site-provider"' in run_sh and '> "$state/provider"' not in run_sh)

# Every Redfish evidence marker must be printed by some phase, or the
# evidence phase fails after a fully passing run.
evidence_src = (root / 'hack/multiplane/evidence.py').read_text()
block = evidence_src.split('if REDFISH:\n    markers += (')[1].split(')\nfor marker')[0]
sources = ''.join(p.read_text() for p in list((root / 'hack/multiplane').glob('*.py')) + list((root / 'hack/multiplane').glob('*.sh')) + [root / 'cmd/fleet-multiplane/main.go']
                  if p.name != 'evidence.py')
for marker in re.findall(r"'([^']+)'", block):
    check('evidence marker is printed by a phase: ' + marker, marker in sources or marker.split(': ')[0] + ': verified' == marker)
teardown = (root / 'hack/multiplane/teardown.sh').read_text()
check('teardown removes the synthetic BMC credential after a pass', '"$state/redfish"' in teardown)
check('teardown removes the BMC image tags', '"$prefix-bmc-image"' in teardown and 'fleet-bmc:$prefix' in teardown)

if failures:
    print('FAIL Redfish mode tests:\n  ' + '\n  '.join(failures))
    sys.exit(1)
print('PASS Redfish mode tests')
