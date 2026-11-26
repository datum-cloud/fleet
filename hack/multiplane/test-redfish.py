#!/usr/bin/env python3
"""Offline checks for the Redfish acceptance mode (run.sh --redfish).

Renders the Redfish site overlays and a staged Redfish source revision with
the real kustomize, and checks the static contracts the live run relies on:
workers have no hypervisor path, only the runtime is privileged, the lab
enrollment matches the lab targets, and every phase is implemented. Never
contacts a container runtime or cluster.
"""
import argparse
import json
import os
import pathlib
import re
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
check("per-operation credential Role gets only the Secret of the Device its assignment names, in the credential namespace", creds.get('metadata', {}).get('namespace') == 'fleet-site-credentials'
      and creds.get('rules') == [{'apiGroups': [''], 'resources': ['secrets'], 'resourceNames': ["${'bmc-' + '%x'.format([hash.sha256(json.unmarshal(handoff.spec.assignmentJSON).device)]).substring(0, 20)}"], 'verbs': ['get']}], json.dumps(creds)[:300])
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
check('lab DHCP is static: no provisioning attachment published', enrollment.get('dhcp') == 'static' and not any('provisioning' in t for t in enrollment.get('targets', [])))
check('enrollment matches the lab targets exactly', len(enrollment.get('targets', [])) == 2 and all(
    t['systemID'] in by_system and by_system[t['systemID']]['uuid'] == t['systemUUID'] and by_system[t['systemID']]['mac'] == t['mac']
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

base = build(root / 'config/site/redfish/base')
policy = next((d for d in base if d.get('kind') == 'ValidatingAdmissionPolicy' and d['metadata']['name'] == 'fleet-site-kro-worker-jobs'), {})
text = json.dumps(policy)
check('admission denies libvirt mounts and URIs for kro worker Jobs', "startsWith('/run/fleet-libvirt')" in text and "FLEET_LIBVIRT_URI" in text)
identity = json.dumps(next((d for d in base if d.get('kind') == 'ValidatingAdmissionPolicy' and d['metadata']['name'] == 'fleet-site-kro-worker-identity'), {}))
handoff_crd = documents((root / 'config/multiplane/coordination/crd.yaml').read_text())[0]
handoff_spec = handoff_crd['spec']['versions'][0]['schema']['openAPIV3Schema']['properties']['spec']
check('no handoff field names a credential Secret, so a forged one is refused', 'credential' not in handoff_spec.get('properties', {})
      and not handoff_spec.get('x-kubernetes-preserve-unknown-fields'), json.dumps(handoff_spec)[:300])
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
         'reuse', 'delivery-faults', 'install-b', 'release-b', 'boot-denials', 'isolation', 'evidence']
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
