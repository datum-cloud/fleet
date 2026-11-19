#!/usr/bin/env python3
"""Offline tests for hack/multiplane/flux.py: source staging, push records and
reconcile waits, with fake flux and kubectl executables. Needs kustomize."""
import argparse
import importlib.util
import json
import os
import pathlib
import shutil
import subprocess
import sys
import tempfile

root = pathlib.Path(sys.argv[1]).resolve()
spec = importlib.util.spec_from_file_location('flux', root / 'hack/multiplane/flux.py')
flux = importlib.util.module_from_spec(spec)
sys.path.insert(0, str(root / 'hack/multiplane'))
spec.loader.exec_module(flux)
failures = []


def check(name, ok, detail=''):
    if not ok:
        failures.append(name + (': ' + detail if detail else ''))


def expect_exit(name, fn, code):
    try:
        fn()
    except SystemExit as e:
        check(name, e.code == code, 'exit %s' % e.code)
        return
    check(name, False, 'no exit')


FAKE_KUBECTL = r'''#!/usr/bin/env python3
import json, os, sys
a = sys.argv[1:]
open(os.environ['FAKE_TRACE'], 'a').write(json.dumps(a) + '\n')
rev = os.environ.get('FAKE_REVISION', '')
ready = os.environ.get('FAKE_READY', 'True')
if 'annotate' in a: sys.exit(0)
def requested(name):
    calls = [json.loads(l) for l in open(os.environ['FAKE_TRACE'])]
    matches = [c[-1].split('=', 1)[1] for c in calls if 'annotate' in c and name in c]
    # FAKE_STRIP: a parent apply removed the first request before it was handled.
    if name == os.environ.get('FAKE_STRIP') and len(matches) < 2: return None
    return matches[-1] if matches else None
def handled(name):
    if os.environ.get('FAKE_STALE') == '1': return 'old-request'
    return requested(name)
def meta(name, **kw):
    r = requested(name)
    return dict(kw, name=name, annotations={'reconcile.fluxcd.io/requestedAt': r} if r else {})
if 'ocirepositories.source.toolkit.fluxcd.io' in a and 'fleet-gitops' in a:
    print(json.dumps({'metadata': meta('fleet-gitops', generation=1), 'status': {'observedGeneration': 1, 'conditions': [{'type': 'Ready', 'status': ready}], 'lastHandledReconcileAt': handled('fleet-gitops'), 'artifact': {'revision': rev}}})); sys.exit(0)
if 'kustomizations.kustomize.toolkit.fluxcd.io' in a:
    names = ['flux-system/fleet-cluster', 'flux-system/fleet-coordination', 'flux-system/fleet-federation', 'flux-system/kro',
             'flux-system/fleet-site-base', 'flux-system/fleet-site-graph', 'flux-system/fleet-site-services', 'flux-system/fleet-site-dispatch']
    items = [{'metadata': meta(n.split('/')[1], namespace=n.split('/')[0], generation=2), 'spec': {},
              'status': {'lastHandledReconcileAt': handled(n.split('/')[1]), 'observedGeneration': 2, 'lastAppliedRevision': rev, 'conditions': [{'type': 'Ready', 'status': ready, 'reason': 'ReconciliationSucceeded'}]}}
             for n in names]
    print(json.dumps({'items': items})); sys.exit(0)
sys.exit(1)
'''
FAKE_FLUX = r'''#!/usr/bin/env python3
import json, os, sys
open(os.environ['FAKE_TRACE'], 'a').write(json.dumps(['flux'] + sys.argv[1:]) + '\n')
print(json.dumps({'repository': 'x', 'digest': 'sha256:' + 'd' * 64}))
'''

if not shutil.which('kustomize'):
    print('FAIL flux tests: kustomize is required')
    sys.exit(1)
with tempfile.TemporaryDirectory() as tmp:
    tmp = pathlib.Path(tmp)
    bin_dir = tmp / 'bin'
    bin_dir.mkdir()
    (bin_dir / 'kubectl').write_text(FAKE_KUBECTL.replace('#!/usr/bin/env python3', '#!' + sys.executable))
    (bin_dir / 'kubectl').chmod(0o755)
    trace = tmp / 'trace'
    os.environ.update(PATH=str(bin_dir) + os.pathsep + os.environ['PATH'], FAKE_TRACE=str(trace))
    s = tmp / 'state'
    (s / 'flux-tools').mkdir(parents=True)
    (s / 'evidence').mkdir()
    (s / 'prefix').write_text('fleet-mp-0000abcd\n')
    # A tiny stand-in for the release install.yaml, accepted by overriding its pin.
    fake_install = 'apiVersion: v1\nkind: Namespace\nmetadata: {name: flux-system}\n'
    for name in ('source-controller', 'kustomize-controller', 'helm-controller', 'notification-controller', 'image-automation-controller',
                 'image-reflector-controller', 'source-watcher'):
        fake_install += '---\napiVersion: apps/v1\nkind: Deployment\nmetadata: {name: %s, namespace: flux-system}\nspec:\n  template:\n    spec:\n      containers:\n        - {name: manager, image: ghcr.io/fluxcd/%s:v0, args: []}\n' % (name, name)
    fake_install += '---\napiVersion: v1\nkind: Service\nmetadata: {name: source-watcher, namespace: flux-system}\n'
    (s / 'flux-tools' / 'install.yaml').write_text(fake_install)
    flux.INSTALL_SHA256 = flux.sha256(s / 'flux-tools' / 'install.yaml')
    (s / 'flux-tools' / 'flux').write_text(FAKE_FLUX.replace('#!/usr/bin/env python3', '#!' + sys.executable))
    (s / 'flux-tools' / 'flux').chmod(0o755)
    env = {'registryHost': 'fleet-mp-0000abcd-registry', 'registryPort': 5555, 'karmadaPort': 1, 'cell': 'fleet-mp-0000abcd-cell',
           'cellEndpoint': 'https://fleet-mp-0000abcd-cell-control-plane:6443', 'siteImage': 'fleet-mp-0000abcd-registry:5000/fleet-site@sha256:' + 'a' * 64,
           'siteImageDigest': 'sha256:' + 'a' * 64}
    (s / 'gitops-environment.json').write_text(json.dumps(env))
    (s / 'scoped-asset').write_text('local-milo/uid/fleet-provider/fleet.datumapis.com/Device/synthetic-device-central-01\n')

    def ns(**kw):
        base = dict(state=str(s), root=str(root), revision=1, site='absent', bad_kro_chart=False, drop_site_volume=False, purpose='test', cluster=[], timeout=5)
        base.update(kw)
        return argparse.Namespace(**base)

    def render(rev, cluster):
        out = subprocess.run(['kustomize', 'build', str(s / 'gitops' / ('r%d' % rev) / 'environment' / cluster)], text=True, capture_output=True, check=True).stdout
        return out

    check('stage platform revision', flux.stage(ns(revision=1)) == 0)
    cell1 = render(1, 'cell')
    check('r1 has no site layers', 'name: fleet-site-services' not in cell1 and 'name: fleet-site-dispatch' not in cell1 and 'name: fleet-site-base' in cell1)
    check('sync source is the run registry', 'url: oci://fleet-mp-0000abcd-registry:5000/fleet-gitops' in cell1 and 'tag: local' in cell1)
    check('controllers pinned by digest', 'ghcr.io/fluxcd/source-controller@sha256:' in cell1)
    check('unused controllers removed', 'image-automation-controller' not in cell1 and 'source-watcher' not in cell1)
    hub1 = render(1, 'hub')
    check('hub environment beside the Karmada kubeconfig', 'name: fleet-environment\n  namespace: karmada-system' in hub1 and 'name: fleet-environment\n  namespace: flux-system' in hub1)
    check('stage dispatch revision', flux.stage(ns(revision=3, site='dispatch')) == 0)
    cell3 = render(3, 'cell')
    check('dispatch revision carries the site', 'FLEET_SITE_DISPATCH: "true"' in cell3 and 'path: ./config/site/instance' in cell3 and 'FLEET_SCOPED_ASSET' in cell3
          and 'path: ./config/site/services' in cell3 and ('FLEET_BOOT_SERVICE_IP: %s\n' % flux.BOOT_SERVICE_IP) in cell3.replace('"', ''))
    # Flux replaces an undefined variable with an empty string, which would give
    # the boot Service a random ClusterIP: every substituted variable must be defined.
    import re
    env_keys = set(re.findall(r'^  (FLEET_[A-Z_]+): ', cell3, re.M))
    used = set()
    for layer in ('config/site/services', 'config/site/instance'):
        for f in (root / layer).glob('*.yaml'):
            used |= set(re.findall(r'\$\{(FLEET_[A-Z_]+)\}', f.read_text()))
    check('substituted variables defined', used and used <= env_keys, str(sorted(used - env_keys)))
    check('site accelerator defaults to TCG', 'FLEET_LOCAL_VM_ACCEL: tcg\n' in cell3.replace('"', ''))
    check('graph uses no Flux variables', not re.findall(r'\$\{FLEET_', (root / 'config/site/graph/rgd.yaml').read_text()))
    check('stage off revision', flux.stage(ns(revision=2, site='off')) == 0 and 'FLEET_SITE_DISPATCH: "false"' in render(2, 'cell'))
    flux.stage(ns(revision=4, site='dispatch', bad_kro_chart=True))
    check('bad chart revision patches only kro', flux.MISSING_DIGEST in render(4, 'cell') and flux.KRO_CHART_DIGEST not in render(4, 'cell'))
    flux.stage(ns(revision=6, site='dispatch', drop_site_volume=True))
    check('volume removal revision', '$patch: delete' in render(6, 'cell') and 'fleet-site-state' in render(6, 'cell'))
    sums = (s / 'gitops' / 'r3.SHA256SUMS').read_text()
    check('content sums cover checkout config', 'config/site/graph/rgd.yaml' in sums and 'environment/cell/environment.yaml' in sums)
    check('no credentials or Secrets staged', 'kind: Secret' not in cell3 + hub1)

    # A retained credential anywhere in the source refuses the revision.
    (s / 'provider').mkdir()
    secret = 'z' * 64
    (s / 'provider' / 'tokens.csv').write_text(secret + ',bootstrap,bootstrap,system:masters\n')
    leaky = tmp / 'leaky'
    shutil.copytree(root / 'config', leaky / 'config')
    (leaky / 'config/site/instance/leak.yaml').write_text('# ' + secret + '\n')
    expect_exit('stage refuses a retained token', lambda: flux.stage(ns(revision=9, root=str(leaky))), 1)
    (leaky / 'config/site/instance/leak.yaml').write_text('apiVersion: v1\nkind: Secret\nmetadata: {name: x}\n')
    expect_exit('stage refuses a Secret manifest', lambda: flux.stage(ns(revision=9, root=str(leaky))), 1)
    os.environ['FLUX_FAKE'] = '1'
    tampered = s / 'flux-tools' / 'install.yaml'
    tampered.write_text(fake_install + '# changed\n')
    expect_exit('stage refuses a changed install manifest', lambda: flux.stage(ns(revision=9)), 1)
    tampered.write_text(fake_install)

    # Push records the digest, revision and checkout identity.
    check('push', flux.push(ns(revision=3)) == 0)
    rec = flux.current(s)
    check('push record', rec['digest'] == 'sha256:' + 'd' * 64 and rec['fluxRevision'] == 'local@sha256:' + 'd' * 64 and rec['checkoutDirty'] is True)
    calls = [json.loads(l) for l in trace.read_text().splitlines()]
    pushc = next(c for c in calls if c[:3] == ['flux', 'push', 'artifact'])
    check('push is reproducible and insecure-local only', '--reproducible' in pushc and '--insecure-registry' in pushc and 'oci://127.0.0.1:5555/fleet-gitops:local' in pushc)

    # Waits require Ready, the current generation and the pushed revision.
    (s / 'cell-admin.yaml').write_text('')
    os.environ['FAKE_REVISION'] = rec['fluxRevision']
    os.environ['FAKE_READY'] = 'True'
    check('wait converged', isinstance(flux.wait_cluster(s, 'cell', 10), dict))
    check('wait requested a source reconcile', any('annotate' in c and 'flux-system/fleet-gitops'.split('/')[1] in c for c in
                                                     [json.loads(l) for l in trace.read_text().splitlines()]))
    trace.write_text('')
    os.environ['FAKE_STRIP'] = 'fleet-cluster'
    stripped = flux.wait_cluster(s, 'cell', 20)
    receipts = json.loads((s / 'evidence' / 'flux-reconciles.json').read_text())
    check('stripped request reissued and acknowledged', stripped['flux-system/fleet-cluster']['lastHandledReconcileAt'] == stripped['flux-system/fleet-cluster']['requestedAt']
          and receipts[-1]['lostRequestsReissued'] == {'flux-system/fleet-cluster': 1}, json.dumps(receipts[-1].get('lostRequestsReissued')))
    os.environ.pop('FAKE_STRIP')
    os.environ['FAKE_STALE'] = '1'
    expect_exit('Ready old status cannot satisfy new reconcile', lambda: flux.wait_cluster(s, 'cell', 4), 4)
    os.environ.pop('FAKE_STALE')
    os.environ['FAKE_READY'] = 'False'
    expect_exit('wait not Ready times out', lambda: flux.wait_cluster(s, 'cell', 4), 4)
    os.environ['FAKE_READY'] = 'True'
    os.environ['FAKE_REVISION'] = 'local@sha256:' + 'e' * 64
    expect_exit('wait on another revision times out', lambda: flux.wait_cluster(s, 'cell', 4), 4)
    check('wait at an explicit revision', isinstance(flux.wait_cluster(s, 'cell', 10, revision='local@sha256:' + 'e' * 64), dict))

# Fault guards reject root reconciliation undoing suspension or changing generation.
import fault_guard
obj = {'metadata': {'uid': 'same', 'generation': 2}, 'spec': {'suspend': True}}
dep = {'spec': {'replicas': 0}}
pods = {'items': []}
receipt = fault_guard.validate(obj, dep, pods)
check('fault guard retains identity', fault_guard.validate(obj, dep, pods, receipt) == receipt)
for changed in (
    {'metadata': {'uid': 'same', 'generation': 3}, 'spec': {'suspend': True}},
    {'metadata': {'uid': 'same', 'generation': 2}, 'spec': {'suspend': False}},
):
    try:
        fault_guard.validate(changed, dep, pods, receipt)
        check('fault guard rejects drift', False)
    except ValueError:
        pass
try:
    fault_guard.validate(obj, dep, {'items': [{}]}, receipt)
    check('fault guard rejects live Pods', False)
except ValueError:
    pass

root = {'metadata': {'generation': 4}, 'status': {'observedGeneration': 4, 'lastAppliedRevision': 'local@sha256:r3',
                                                 'lastHandledReconcileAt': 't3', 'conditions': [{'type': 'Ready', 'status': 'True'}]}}
check('root receipt at the fault revision', fault_guard.root_receipt(root, 'local@sha256:r3')['lastHandledReconcileAt'] == 't3')
for bad in ({'lastAppliedRevision': 'local@sha256:r2'}, {'observedGeneration': 3}, {'conditions': [{'type': 'Ready', 'status': 'False'}]}):
    stale = json.loads(json.dumps(root))
    stale['status'].update(bad)
    try:
        fault_guard.root_receipt(stale, 'local@sha256:r3')
        check('root receipt rejects %s' % sorted(bad), False)
    except ValueError:
        pass

# Ownership audit: one Flux layer per shared object, kro only for workers.
def world(**faults):
    inv = {k: list(v) for k, v in flux.LAYER_INVENTORY.items()}
    base_inv = []
    objects = {}
    for (kind, ns, name), layer in flux.SHARED.items():
        objects[(kind, ns, name)] = {'metadata': {'name': name, 'uid': 'u-' + name, 'labels': {'kustomize.toolkit.fluxcd.io/name': layer}}}
        if layer == 'fleet-site-base':
            base_inv.append('%s_%s_x_%s' % (ns or '', name, kind))
    worker = 'w-op-' + 'a' * 40
    for kind, ns in flux.WORKER_KINDS:
        objects[(kind, ns, worker)] = {'metadata': {'name': worker, 'uid': 'k-' + kind, 'labels': {'kro.run/node-id': kind, 'app.kubernetes.io/managed-by': 'kro'}}}
    if faults.get('kro_deployment'):
        objects[('deployments', 'fleet-site', 'fleet-site-boot')]['metadata']['labels']['kro.run/node-id'] = 'boot'
    if faults.get('flux_job'):
        objects[('jobs', 'fleet-site', worker)]['metadata']['labels']['kustomize.toolkit.fluxcd.io/name'] = 'fleet-site-services'
    if faults.get('job_in_inventory'):
        inv['flux-system/fleet-site-services'] = sorted(inv['flux-system/fleet-site-services'] + ['fleet-site_%s_batch_Job' % worker])
    if faults.get('kro_configmap'):
        objects[('configmaps', 'fleet-site', 'kro-extra')] = {'metadata': {'name': 'kro-extra', 'uid': 'x', 'labels': {'kro.run/node-id': 'extra'}}}
    if faults.get('pvc_in_services'):
        inv['flux-system/fleet-site-services'] = sorted(inv['flux-system/fleet-site-services'] + ['fleet-site_fleet-site-state__PersistentVolumeClaim'])
    if faults.get('missing_service'):
        del objects[('services', 'fleet-site', 'fleet-site-boot')]
    if faults.get('manual_job'):
        objects[('jobs', 'fleet-site', 'manual')] = {'metadata': {'name': 'manual', 'uid': 'm', 'labels': {}}}
    deps = {'flux-system/fleet-site-base': ['fleet-coordination'] + (['kro'] if faults.get('base_on_kro') else []),
            'flux-system/fleet-site-services': ['fleet-site-base'], 'flux-system/fleet-site-graph': ['kro', 'fleet-site-base'],
            'flux-system/fleet-site-dispatch': ['fleet-site-graph', 'fleet-site-services', 'fleet-federation']}
    ks = {n: {'inventory': inv.get(n, base_inv if n == 'flux-system/fleet-site-base' else []), 'dependsOn': [{'name': d} for d in deps.get(n, [])]}
          for n in list(flux.LAYER_INVENTORY) + ['flux-system/fleet-site-base']}
    return objects, {'cell': {'kustomizations': ks}, 'hub': {'kustomizations': {}}, 'platform': {'kustomizations': {}}}


def audit(**faults):
    objects, snaps = world(**faults)

    def fake_kubectl(state, cluster, *args, check=True, **kw):
        a = [x for x in args if x not in ('-o', 'json')]
        ns = a[1] if a[0] == '-n' else None
        kind, name = a[-2], a[-1]
        o = objects.get((kind, ns, name))
        return argparse.Namespace(returncode=0 if o else 1, stdout=json.dumps(o or {}), stderr='')

    def fake_kget(state, cluster, *args, **kw):
        ns, kind = args[1], args[3]
        return {'items': [o for (k, n, _), o in objects.items() if k == kind and n == ns]}
    saved = flux.kubectl, flux.kget
    flux.kubectl, flux.kget = fake_kubectl, fake_kget
    try:
        return flux.ownership('unused', snaps)
    finally:
        flux.kubectl, flux.kget = saved


record, problems = audit()
check('ownership audit passes a single-writer site', problems == [] and len(record['kroWrittenObjects']) == 4, str(problems))
for fault in ('kro_deployment', 'flux_job', 'job_in_inventory', 'kro_configmap', 'pvc_in_services', 'missing_service', 'manual_job', 'base_on_kro'):
    check('ownership audit rejects ' + fault, audit(**{fault: True})[1] != [])

if failures:
    print('FAIL flux tests:\n  ' + '\n  '.join(failures))
    sys.exit(1)
print('PASS flux tests')
