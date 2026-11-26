#!/usr/bin/env python3
"""Flux source, bootstrap and reconciliation evidence for the local acceptance.

The acceptance reconciles every long-running service from one OCI artifact,
staged from the current checkout and pushed to this run's local registry.
This module owns only:

  tools      download and verify the pinned Flux CLI and install manifest
  stage      build one source revision (checkout config + generated environment)
  push       push a staged revision and record its digest
  bootstrap  apply one cluster's Flux components and sync objects, once
  publish    stage + push + request reconcile + wait for the given clusters
  wait       wait until a cluster's Flux objects are Ready at the pushed revision
  record     write a sanitized snapshot of a cluster's Flux objects
  verify, drift, restart, fail-closed, retention, graph-lifecycle
             acceptance scenarios; ownership audits who writes each site object

Every Kubernetes write it makes is either the documented bootstrap boundary
(Flux components and sync objects, applied once), a reconcile request
annotation, or explicit fault injection whose repair is left to Flux.
Nothing here stores credentials in a source revision.
"""
import argparse
import datetime
import hashlib
import json
import os
import pathlib
import re
import shutil
import subprocess
import sys
import tarfile
import time
import urllib.request

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
import harness  # noqa: E402  (secret discovery and exit codes)

FLUX_VERSION = '2.9.6'
RELEASE = 'https://github.com/fluxcd/flux2/releases/download/v%s/' % FLUX_VERSION
# From flux_2.9.6_checksums.txt of the v2.9.6 GitHub release.
INSTALL_SHA256 = '9c1fda7e401429531ed1478f67ba06b3edff6513b75da7ee47bde9f1c4d4251c'
CLI = {('darwin', 'arm64'): ('flux_2.9.6_darwin_arm64.tar.gz', '7008a758da4b8d57c5845aad6552f256d4d23630c2b2de2ca8da362f9a48c126'),
       ('darwin', 'x86_64'): ('flux_2.9.6_darwin_amd64.tar.gz', 'c3c3532c8c6cf689ef7b93125b33dc170df84a9d5d036f578568bfdf88a2d7e6'),
       ('linux', 'x86_64'): ('flux_2.9.6_linux_amd64.tar.gz', 'b4d22673e9246cbd628881f1a9ef3b090085dced291e42d804555cee8e8d42c5'),
       ('linux', 'aarch64'): ('flux_2.9.6_linux_arm64.tar.gz', '6663c154755b732f43dc993d72321f71c2fc1ff0bcb94b2696e0a8638fa562b4')}
REGISTRY_IMAGE = 'registry:3.0.0@sha256:6c5666b861f3505b116bb9aa9b25175e71210414bd010d92035ff64018f9457e'
KRO_CHART_DIGEST = 'sha256:4e8de4d34b7e2c3cede958e140e26cd6d34a377062e63caf355a636d2bf4a67c'
KRO_CHART_LAYER_SHA256 = 'ceeb630764eb79ca4220dfdcc2907b819a27c30f68cb60d09c1ae3fa2c58b5cd'
KARMADA_CHART_SHA256 = '773ab194cb92dad81d479d2199b968a2c6b0b8366306ba47961afd67616834fe'
# A well-formed digest that no registry serves: the fail-closed revision.
MISSING_DIGEST = 'sha256:' + hashlib.sha256(b'fleet: deliberately absent chart').hexdigest()
TAG = 'local'
CLUSTERS = ('platform', 'hub', 'cell')
KUBECONFIG = {'platform': 'platform-admin.yaml', 'hub': 'host.yaml', 'cell': 'cell-admin.yaml'}
SOURCE_DIRS = ('config/multiplane', 'config/site')
# Expected Flux Kustomizations per cluster (namespace/name), besides the root.
EXPECTED = {
    'platform': ['flux-system/milo-provider', 'flux-system/milo-consumer-a', 'flux-system/milo-consumer-b'],
    'hub': ['flux-system/karmada', 'karmada-system/fleet-karmada-coordination',
            'karmada-system/fleet-karmada-api', 'karmada-system/fleet-karmada-members'],
    'cell': ['flux-system/fleet-coordination', 'flux-system/fleet-federation', 'flux-system/kro',
             'flux-system/fleet-site-base', 'flux-system/fleet-site-graph'],
}
HELMRELEASES = {'hub': ['flux-system/karmada'], 'cell': ['flux-system/kro']}
# Cell site layers present from revision 2 onwards.
SITE_LAYERS = ['flux-system/fleet-site-services', 'flux-system/fleet-site-dispatch']
# A fixed ClusterIP in the static band (first 256 addresses) of kind's default
# 10.96.0.0/16 Service range, so a recreated boot Service keeps its origin.
BOOT_SERVICE_IP = '10.96.0.80'
SITE, COORD = 'fleet-site', 'fleet-coordination'
DISPATCHER = ('handoffdispatchers.site.fleet.datumapis.com', 'dispatcher')
RGD = 'fleet-handoff-dispatcher'
# Flux-owned shared site objects and the layer (Kustomization) that writes each.
SHARED = {('deployments', SITE, 'fleet-site-runtime'): 'fleet-site-services', ('deployments', SITE, 'fleet-site-boot'): 'fleet-site-services',
          ('deployments', SITE, 'fleet-site-observer'): 'fleet-site-services', ('services', SITE, 'fleet-site-boot'): 'fleet-site-services',
          ('configmaps', SITE, 'fleet-site-worker-policy'): 'fleet-site-services',
          ('serviceaccounts', SITE, 'fleet-site-runtime'): 'fleet-site-base', ('serviceaccounts', SITE, 'fleet-site-boot'): 'fleet-site-base',
          ('serviceaccounts', SITE, 'fleet-site-observer'): 'fleet-site-base', ('persistentvolumeclaims', SITE, 'fleet-site-state'): 'fleet-site-base',
          ('roles', COORD, 'fleet-site-observer'): 'fleet-site-base', ('rolebindings', COORD, 'fleet-site-observer'): 'fleet-site-base',
          ('roles', SITE, 'fleet-site-kro-workers'): 'fleet-site-base', ('rolebindings', SITE, 'fleet-site-kro-workers'): 'fleet-site-base',
          ('roles', COORD, 'fleet-site-kro-worker-rbac'): 'fleet-site-base', ('rolebindings', COORD, 'fleet-site-kro-worker-rbac'): 'fleet-site-base',
          ('clusterroles', None, 'fleet-site-kro'): 'fleet-site-base',
          ('validatingadmissionpolicies', None, 'fleet-site-kro-worker-jobs'): 'fleet-site-base',
          ('validatingadmissionpolicybindings', None, 'fleet-site-kro-worker-jobs'): 'fleet-site-base',
          ('validatingadmissionpolicies', None, 'fleet-site-kro-worker-identity'): 'fleet-site-base',
          ('validatingadmissionpolicybindings', None, 'fleet-site-kro-worker-identity'): 'fleet-site-base'}
# Exact inventories of the layers that must hold nothing else.
# Site provider: libvirt (default) or redfish, from run.sh --provider. Redfish
# mode selects the config/site/redfish overlays and adds the Flux-owned lab BMC.
PROVIDER = os.environ.get('FLEET_SITE_PROVIDER', 'libvirt')
BMC_SERVICE_IP = '10.96.0.81'
SITE_DEPLOYMENTS = ('fleet-site-runtime', 'fleet-site-boot', 'fleet-site-observer') + (('fleet-site-bmc',) if PROVIDER == 'redfish' else ())
if PROVIDER == 'redfish':
    SHARED.update({('deployments', SITE, 'fleet-site-bmc'): 'fleet-site-services', ('services', SITE, 'fleet-site-bmc'): 'fleet-site-services',
                   ('serviceaccounts', SITE, 'fleet-site-bmc'): 'fleet-site-base'})
# Lab DHCP: static (libvirt dnsmasq) or synced (run.sh --synced-dhcp, Redfish
# only): the site's Kea, fed by Fleet's reservation feed, from the
# config/site/redfish/synced overlays.
LAB_DHCP = os.environ.get('FLEET_LAB_DHCP', 'static')
if LAB_DHCP not in ('static', 'synced') or (LAB_DHCP == 'synced' and PROVIDER != 'redfish'):
    raise SystemExit('FLEET_LAB_DHCP=synced needs the Redfish provider')
SYNCED_OBJECTS = ['fleet-site_fleet-site-dhcp-base__ConfigMap', 'fleet-site_fleet-site-dhcp-feed__Service', 'fleet-site_fleet-site-redfish-enrollment__ConfigMap'] if LAB_DHCP == 'synced' else []
if LAB_DHCP == 'synced':
    SHARED.update({('configmaps', SITE, 'fleet-site-dhcp-base'): 'fleet-site-services', ('services', SITE, 'fleet-site-dhcp-feed'): 'fleet-site-services',
                   ('configmaps', SITE, 'fleet-site-redfish-enrollment'): 'fleet-site-services'})
# Synced mode also runs IPAM (milo-os/ipam v0.6.0) on the platform cluster,
# aggregated by the provider project's Milo API: its namespace and PostgreSQL
# from this source, and its apiserver from IPAM's published manifests. The
# published auth-reader RoleBinding renders into ipam-system, not kube-system,
# so it grants nothing; it is inert here because IPAM reads Milo's
# extension-apiserver-authentication through its authentication kubeconfig.
IPAM_KUSTOMIZE_DIGEST = 'sha256:62305dde600b8efa10a68a786e60b2e826837897cd93378c9e94d7adbff3f647'
IPAM_IMAGE = 'ghcr.io/milo-os/ipam@sha256:18d472179ddff8db77a812fd0f0874248dd08e5a2425197152cc4cc2e9a7ce0d'
PLATFORM_LAYER_INVENTORY = {
    'flux-system/ipam-system': sorted(['_ipam-system__Namespace', 'ipam-system_ipam-postgres__Service', 'ipam-system_ipam-postgres_apps_StatefulSet',
                                       'ipam-system_ipam-apiserver-milo_networking.k8s.io_NetworkPolicy']),
    'flux-system/ipam-apiserver': sorted(['ipam-system_ipam-apiserver__ServiceAccount', '_ipam-apiserver_rbac.authorization.k8s.io_ClusterRole',
                                          'ipam-system_ipam-apiserver-auth-reader_rbac.authorization.k8s.io_RoleBinding',
                                          '_ipam-apiserver_rbac.authorization.k8s.io_ClusterRoleBinding',
                                          '_ipam-apiserver:system:auth-delegator_rbac.authorization.k8s.io_ClusterRoleBinding',
                                          'ipam-system_ipam-apiserver__Service', 'ipam-system_ipam-apiserver_apps_Deployment',
                                          'ipam-system_ipam-apiserver_policy_PodDisruptionBudget', 'ipam-system_ipam-apiserver_networking.k8s.io_NetworkPolicy']),
} if LAB_DHCP == 'synced' else {}
EXPECTED['platform'] += list(PLATFORM_LAYER_INVENTORY)
LAYER_INVENTORY = {
    'flux-system/fleet-site-services': sorted(['fleet-site_fleet-site-boot__Service', 'fleet-site_fleet-site-worker-policy__ConfigMap',
                                               'fleet-site_fleet-site-runtime_apps_Deployment', 'fleet-site_fleet-site-boot_apps_Deployment',
                                               'fleet-site_fleet-site-observer_apps_Deployment'] + (
                                                   ['fleet-site_fleet-site-bmc__Service', 'fleet-site_fleet-site-bmc_apps_Deployment'] if PROVIDER == 'redfish' else []) + SYNCED_OBJECTS),
    'flux-system/fleet-site-graph': ['_fleet-handoff-dispatcher_kro.run_ResourceGraphDefinition'],
    'flux-system/fleet-site-dispatch': ['fleet-site_dispatcher_site.fleet.datumapis.com_HandoffDispatcher'],
}
# kro writes only these per-operation kinds, all named w-<handoff>.
WORKER_KINDS = (('jobs', SITE), ('serviceaccounts', SITE), ('roles', COORD), ('rolebindings', COORD))
FORBIDDEN = (re.compile(r'^kind:\s*Secret\s*$', re.M), re.compile(r'-----BEGIN [A-Z ]*PRIVATE KEY-----'),
             re.compile(r'''^\s*(token|client-key-data|password)\s*:\s*\S{16,}''', re.M))


def now():
    return datetime.datetime.now(datetime.timezone.utc).isoformat()


def die(message, code=harness.EXIT_FAILED):
    print(message, file=sys.stderr)
    raise SystemExit(code)


def kubectl(state, cluster, *args, input=None, check=True, timeout=60, config=None):
    path = pathlib.Path(state) / (config or KUBECONFIG[cluster])
    try:
        r = subprocess.run(['kubectl', '--kubeconfig', str(path), '--request-timeout=20s', *args], input=input,
                           text=True, capture_output=True, timeout=timeout)
    except subprocess.TimeoutExpired:
        die('kubectl %s on %s timed out' % (' '.join(args[:3]), cluster), harness.EXIT_TIMEOUT)
    if check and r.returncode:
        die('kubectl %s on %s failed: %s' % (' '.join(args[:4]), cluster, r.stderr.strip()[:400]))
    return r


def kget(state, cluster, *args, config=None):
    return json.loads(kubectl(state, cluster, *args, '-o', 'json', config=config).stdout)


def sha256(path):
    return hashlib.sha256(pathlib.Path(path).read_bytes()).hexdigest()


def write(path, value):
    path = pathlib.Path(path)
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + '\n')
    path.chmod(0o600)


def flux_bin(state):
    return str(pathlib.Path(state) / 'flux-tools' / 'flux')


# ---------------------------------------------------------------- tools

def fetch(url, dest, digest):
    if not dest.exists() or sha256(dest) != digest:
        tmp = dest.with_suffix('.part')
        with urllib.request.urlopen(url, timeout=120) as r, open(tmp, 'wb') as f:
            shutil.copyfileobj(r, f)
        tmp.replace(dest)
    if sha256(dest) != digest:
        die('%s digest mismatch: %s' % (dest.name, sha256(dest)))


def tools(args):
    d = pathlib.Path(args.state) / 'flux-tools'
    d.mkdir(mode=0o700, exist_ok=True)
    key = (os.uname().sysname.lower(), os.uname().machine)
    if key not in CLI:
        die('no pinned Flux CLI for %s/%s' % key, harness.EXIT_PREFLIGHT)
    archive, digest = CLI[key]
    fetch(RELEASE + archive, d / archive, digest)
    fetch(RELEASE + 'install.yaml', d / 'install.yaml', INSTALL_SHA256)
    with tarfile.open(d / archive) as t:
        t.extract('flux', d, filter='data')
    version = subprocess.run([str(d / 'flux'), 'version', '--client'], text=True, capture_output=True).stdout.strip()
    if version != 'flux: v' + FLUX_VERSION:
        die('unexpected Flux CLI version: ' + version)
    record = {'version': 'v' + FLUX_VERSION, 'cli': {'archive': archive, 'sha256': digest, 'binarySHA256': sha256(d / 'flux')},
              'installManifest': {'url': RELEASE + 'install.yaml', 'sha256': INSTALL_SHA256}}
    write(d / 'tools.json', record)
    print('flux-tools: Flux CLI v%s and install manifest verified (install.yaml sha256 %s)' % (FLUX_VERSION, INSTALL_SHA256))
    return 0


# ---------------------------------------------------------------- source

def environment(state):
    s = pathlib.Path(state)
    return json.loads((s / 'gitops-environment.json').read_text())


def sync_objects(cluster, url):
    """The per-cluster sync objects that flux bootstrap would commit as gotk-sync.yaml."""
    return '''# Generated per environment: the OCI source and the root sync for %(c)s.
apiVersion: source.toolkit.fluxcd.io/v1
kind: OCIRepository
metadata:
  name: fleet-gitops
  namespace: flux-system
spec:
  interval: 1m
  url: %(url)s
  ref:
    tag: %(tag)s
  # Local seam: a plain-HTTP registry on the disposable kind network. A real
  # environment uses TLS and verifies signatures (spec.verify).
  insecure: true
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: fleet-cluster
  namespace: flux-system
spec:
  interval: 10m
  retryInterval: 30s
  timeout: 5m
  prune: true
  wait: false
  sourceRef:
    kind: OCIRepository
    name: fleet-gitops
  path: ./environment/%(c)s
''' % {'c': cluster, 'url': url, 'tag': TAG}


def config_map(name, namespace, data):
    lines = ['apiVersion: v1', 'kind: ConfigMap', 'metadata:', '  name: ' + name, '  namespace: ' + namespace, 'data:']
    lines += ['  %s: %s' % (k, json.dumps(str(v))) for k, v in sorted(data.items())]
    return '\n'.join(lines) + '\n'


def stage(args):
    s = pathlib.Path(args.state).resolve()
    root = pathlib.Path(args.root).resolve()
    env = environment(s)
    out = s / 'gitops' / ('r%d' % args.revision)
    if out.exists():
        shutil.rmtree(out)
    out.mkdir(parents=True, mode=0o700)
    for name in SOURCE_DIRS:
        shutil.copytree(root / name, out / name, ignore=shutil.ignore_patterns('__pycache__', '*.pyc', '.*'))
    tools_dir = s / 'flux-tools'
    if sha256(tools_dir / 'install.yaml') != INSTALL_SHA256:
        die('install manifest changed after verification')
    components = out / 'environment' / 'flux-components'
    components.mkdir(parents=True)
    shutil.copy(tools_dir / 'install.yaml', components / 'gotk-components.yaml')
    shutil.copy(root / 'config/multiplane/flux/components.yaml', components / 'kustomization.yaml')
    url = 'oci://%s:5000/fleet-gitops' % env['registryHost']
    marker = 'r%d' % args.revision
    for cluster in CLUSTERS:
        d = out / 'environment' / cluster
        (d / 'flux-system').mkdir(parents=True)
        (d / 'flux-system' / 'gotk-sync.yaml').write_text(sync_objects(cluster, url))
        (d / 'flux-system' / 'kustomization.yaml').write_text(
            'apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - ../../flux-components\n  - gotk-sync.yaml\n')
        resources = ['flux-system', '../../config/multiplane/clusters/' + cluster, 'environment.yaml']
        data = {'FLEET_CELL': env['cell'], 'FLEET_CELL_ENDPOINT': env['cellEndpoint'], 'FLEET_SOURCE_MARKER': marker}
        maps = []
        patches = []
        if cluster == 'platform' and LAB_DHCP == 'synced':
            resources.insert(2, '../../config/multiplane/clusters/platform-ipam')
        if cluster == 'hub':
            # Karmada-API Kustomizations live beside their kubeconfig Secret.
            maps.append(config_map('fleet-environment', 'karmada-system', data))
        if cluster == 'cell':
            if PROVIDER == 'redfish':
                # Environment selection, as a commit would make it: the cell's
                # site layers reconcile the Redfish overlays of the same source.
                for layer in ('fleet-site-base', 'fleet-site-graph') + (('fleet-site-services',) if args.site != 'absent' else ()):
                    kind = layer.rsplit('-', 1)[1]
                    overlay = 'synced/' + kind if LAB_DHCP == 'synced' and kind != 'base' else kind
                    patches.append(child_path(layer, './config/site/redfish/' + overlay))
                if args.site != 'absent':
                    data.update(FLEET_BMC_IMAGE=env['bmcImage'], FLEET_BMC_SERVICE_IP=BMC_SERVICE_IP)
            if args.site != 'absent':
                resources.insert(2, '../../config/multiplane/clusters/cell-site')
                data.update(FLEET_SITE_IMAGE=env['siteImage'], FLEET_BOOT_SERVICE_IP=BOOT_SERVICE_IP,
                            FLEET_SITE_DISPATCH='true' if args.site == 'dispatch' else 'false')
            if args.bad_kro_chart:
                patches.append(child_patch('kro', [{'target': {'kind': 'OCIRepository', 'name': 'kro-chart'},
                                                    'patch': '- op: replace\n  path: /spec/ref/digest\n  value: %s\n' % MISSING_DIGEST}]))
            if args.drop_site_volume:
                patches.append(child_patch('fleet-site-base', [{'patch': '$patch: delete\napiVersion: v1\nkind: PersistentVolumeClaim\n'
                                                                         'metadata:\n  name: fleet-site-state\n  namespace: fleet-site\n'}]))
        maps.insert(0, config_map('fleet-environment', 'flux-system', data))
        (d / 'environment.yaml').write_text('# Generated, non-secret environment values for revision %s.\n' % marker + '---\n'.join(maps))
        k = 'apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n' + ''.join('  - %s\n' % r for r in resources)
        if patches:
            k += 'patches:\n' + ''.join(patches)
        (d / 'kustomization.yaml').write_text(k)
    files = sorted(p for p in out.rglob('*') if p.is_file())
    secrets = harness.secrets(s)
    for p in files:
        text = p.read_text(errors='ignore')
        if any(f.search(text) for f in FORBIDDEN) or any(v in text for v in secrets):
            die('refusing to stage credential-like content: ' + str(p.relative_to(out)))
    # Every environment must render before it is published.
    for cluster in CLUSTERS:
        r = subprocess.run(['kustomize', 'build', str(out / 'environment' / cluster)], capture_output=True, text=True)
        if r.returncode:
            die('environment %s does not render: %s' % (cluster, r.stderr[-400:]))
    sums = ''.join('%s  %s\n' % (sha256(p), p.relative_to(out)) for p in files)
    (s / 'gitops' / ('r%d.SHA256SUMS' % args.revision)).write_text(sums)
    print('stage: revision %s with %d files (%s); no credential content' % (marker, len(files), ', '.join(
        k for k, v in (('site', args.site != 'absent'), ('dispatch', args.site == 'dispatch'), ('bad-kro-chart', args.bad_kro_chart),
                       ('site-volume-removed', args.drop_site_volume)) if v) or 'platform only'))
    return 0


def child_path(name, path):
    """Point a child Kustomization at another path of the same source revision."""
    return '  - target: {kind: Kustomization, name: %s}\n    patch: |-\n      - op: replace\n        path: /spec/path\n        value: %s\n' % (name, path)


def child_patch(name, patches):
    """Patch a child Kustomization's spec.patches: the effect of a source commit."""
    return '  - target: {kind: Kustomization, name: %s}\n    patch: |-\n      - op: add\n        path: /spec/patches\n        value: %s\n' % (
        name, json.dumps(patches))


def revisions(state):
    p = pathlib.Path(state) / 'flux-revisions.json'
    return json.loads(p.read_text()) if p.exists() else []


def push(args):
    s = pathlib.Path(args.state).resolve()
    env = environment(s)
    root = pathlib.Path(args.root).resolve()
    out = s / 'gitops' / ('r%d' % args.revision)
    head = subprocess.run(['git', '-C', str(root), 'rev-parse', 'HEAD'], text=True, capture_output=True).stdout.strip()
    branch = subprocess.run(['git', '-C', str(root), 'branch', '--show-current'], text=True, capture_output=True).stdout.strip() or 'detached'
    sums = s / 'gitops' / ('r%d.SHA256SUMS' % args.revision)
    content = hashlib.sha256(sums.read_bytes()).hexdigest()
    r = subprocess.run([flux_bin(s), 'push', 'artifact', 'oci://127.0.0.1:%s/fleet-gitops:%s' % (env['registryPort'], TAG), '--path', str(out),
                        '--source', 'file://fleet-checkout/' + branch, '--revision', '%s@sha1:%s' % (branch, head), '--reproducible', '--insecure-registry',
                        '--annotations', 'org.opencontainers.image.description=fleet local acceptance r%d; uncommitted checkout content sha256 %s' % (args.revision, content),
                        '--output', 'json'], text=True, capture_output=True, timeout=180)
    if r.returncode:
        die('flux push artifact failed: ' + r.stderr[-400:])
    pushed = json.loads(r.stdout)
    digest = pushed['digest']
    record = {'revision': args.revision, 'marker': 'r%d' % args.revision, 'digest': digest, 'fluxRevision': '%s@%s' % (TAG, digest),
              'pushedAt': now(), 'gitHead': head, 'gitBranch': branch, 'checkoutDirty': True, 'contentSumsSHA256': content,
              'files': len(sums.read_text().splitlines()), 'purpose': args.purpose}
    all_revs = [x for x in revisions(s) if x['revision'] != args.revision] + [record]
    write(s / 'flux-revisions.json', all_revs)
    shutil.copy(sums, s / 'evidence' / ('gitops-r%d.SHA256SUMS' % args.revision))
    print('push: r%d %s (%s)' % (args.revision, digest, args.purpose))
    return 0


def current(state):
    revs = revisions(state)
    if not revs:
        die('no source revision has been pushed')
    return revs[-1]


# ---------------------------------------------------------------- reconcile and wait

def request(state, cluster, kind, ref):
    namespace, name = ref.split('/')
    token = now()
    kubectl(state, cluster, '-n', namespace, 'annotate', '--overwrite', kind, name, REQUEST + '=' + token)
    return token


REQUEST = 'reconcile.fluxcd.io/requestedAt'


def acknowledged(obj, token):
    return obj.get('status', {}).get('lastHandledReconcileAt') == token


def lost(obj, token):
    """A parent Flux apply can strip a kubectl-managed request before any reconcile handles it."""
    return not acknowledged(obj, token) and (obj['metadata'].get('annotations') or {}).get(REQUEST) != token


def conditions(obj):
    return {c['type']: c for c in obj.get('status', {}).get('conditions', [])}


def ready(obj):
    c = conditions(obj).get('Ready', {})
    return c.get('status') == 'True' and obj.get('status', {}).get('observedGeneration') == obj['metadata']['generation']


def kustomizations(state, cluster):
    return {i['metadata']['namespace'] + '/' + i['metadata']['name']: i for i in kget(state, cluster, 'get', 'kustomizations.kustomize.toolkit.fluxcd.io', '-A')['items']}


def wait_cluster(state, cluster, timeout, names=None, revision=None, skip=(), observe_also=()):
    """All named (default: every) Kustomizations Ready, current generation, at the pushed revision.

    observe_also samples other clusters' gating while waiting (bootstrap only:
    later waits run while a cluster is deliberately paused)."""
    revision = revision or current(state)['fluxRevision']
    deadline = time.monotonic() + timeout
    source_token = request(state, cluster, 'ocirepositories.source.toolkit.fluxcd.io', 'flux-system/fleet-gitops')
    tokens = {}
    reissued = {}
    last = {}
    while time.monotonic() < deadline:
        source = kget(state, cluster, '-n', 'flux-system', 'get', 'ocirepositories.source.toolkit.fluxcd.io', 'fleet-gitops')
        if lost(source, source_token):
            # Only a fresh request counts; the acknowledgement must still match it exactly.
            source_token = request(state, cluster, 'ocirepositories.source.toolkit.fluxcd.io', 'flux-system/fleet-gitops')
            reissued['flux-system/fleet-gitops'] = reissued.get('flux-system/fleet-gitops', 0) + 1
        ks = kustomizations(state, cluster)
        want = [n for n in names or ['flux-system/fleet-cluster'] + EXPECTED[cluster] + ([n for n in SITE_LAYERS if n in ks] if cluster == 'cell' else [])
                if n not in skip and not ks.get(n, {}).get('spec', {}).get('suspend')]
        for n in want:
            if n in ks and (n not in tokens or lost(ks[n], tokens[n])):
                if n in tokens:
                    reissued[n] = reissued.get(n, 0) + 1
                tokens[n] = request(state, cluster, 'kustomizations.kustomize.toolkit.fluxcd.io', n)
        last = {n: {'requestedAt': tokens.get(n), 'lastHandledReconcileAt': ks.get(n, {}).get('status', {}).get('lastHandledReconcileAt'), 'ready': ready(ks[n]) if n in ks else False, 'revision': ks.get(n, {}).get('status', {}).get('lastAppliedRevision'),
                    'reason': conditions(ks.get(n, {})).get('Ready', {}).get('reason')} for n in want}
        observe(state, cluster, revision, ks)
        for other in observe_also:
            if other != cluster:
                try:
                    observe(state, other, revision, kustomizations(state, other))
                except SystemExit:
                    pass  # an unreachable cluster is sampled on the next poll
        if ready(source) and source.get('status', {}).get('lastHandledReconcileAt') == source_token and source.get('status', {}).get('artifact', {}).get('revision') == revision and all(
                v['ready'] and v['revision'] == revision and v['requestedAt'] is not None and v['lastHandledReconcileAt'] == v['requestedAt'] for v in last.values()):
            path = pathlib.Path(state) / 'evidence' / 'flux-reconciles.json'
            records = json.loads(path.read_text()) if path.exists() else []
            records.append({'cluster': cluster, 'revision': revision, 'observedAt': now(), 'sourceRequestedAt': source_token, 'sourceLastHandledReconcileAt': source['status']['lastHandledReconcileAt'], 'kustomizations': last,
                            'lostRequestsReissued': reissued})
            write(path, records)
            return last
        time.sleep(3)
    die('%s did not converge on %s: %s' % (cluster, revision, json.dumps(last)), harness.EXIT_TIMEOUT)


def observe(state, cluster, revision, ks):
    """Record each distinct non-Ready reason seen while waiting, e.g. dependency gating."""
    path = pathlib.Path(state) / 'flux-wait-observations.json'
    seen = json.loads(path.read_text()) if path.exists() else []
    # Keyed by whether the revision was already applied, so a dependent seen
    # gated after applying (a dependency re-reconciling) never masks the
    # gated-before-apply observation, or the reverse.
    keys = {(o['cluster'], o['kustomization'], o['revision'], o['reason'], o['lastAppliedRevision'] == o['revision']) for o in seen}
    for n, o in ks.items():
        c = conditions(o).get('Ready', {})
        key = (cluster, n, revision, c.get('reason'), o.get('status', {}).get('lastAppliedRevision') == revision)
        if c.get('status') != 'True' and key not in keys:
            keys.add(key)
            seen.append({'cluster': cluster, 'kustomization': n, 'revision': revision, 'reason': c.get('reason'),
                         'message': c.get('message', '')[:160], 'lastAppliedRevision': o.get('status', {}).get('lastAppliedRevision'),
                         'inventoryEntries': len(inventory(o)), 'observedAt': now()})
    write(path, seen)


def wait_cmd(args):
    for cluster in args.cluster:
        wait_cluster(args.state, cluster, args.timeout, skip=args.skip, observe_also=args.observe)
        print('wait: %s Kustomizations Ready at %s' % (cluster, current(args.state)['fluxRevision']))
    return 0


def bootstrap(args):
    """The documented bootstrap boundary: Flux components and sync objects, once."""
    s = pathlib.Path(args.state).resolve()
    first = s / 'gitops' / 'r1'
    components = subprocess.run(['kustomize', 'build', str(first / 'environment/flux-components')], capture_output=True, text=True, check=True).stdout
    path = s / ('flux-bootstrap-%s.yaml' % args.cluster)
    path.write_text(components)
    sync = first / 'environment' / args.cluster / 'flux-system' / 'gotk-sync.yaml'
    # Components (with CRDs) first, then the sync objects once the CRDs serve.
    # The bootstrap applies under kustomize-controller's own field manager, so
    # when Flux reconciles the same objects it takes them over with no residual
    # second manager (and no second writer) left behind.
    kubectl(s, args.cluster, 'apply', '--server-side', '--field-manager=kustomize-controller', '-f', str(path), timeout=180)
    kubectl(s, args.cluster, 'wait', '--for=condition=Established', 'crd/kustomizations.kustomize.toolkit.fluxcd.io',
            'crd/ocirepositories.source.toolkit.fluxcd.io', 'crd/helmreleases.helm.toolkit.fluxcd.io', '--timeout=120s', timeout=140)
    kubectl(s, args.cluster, '-n', 'flux-system', 'wait', '--for=condition=Available', 'deployment', '--all', '--timeout=300s', timeout=320)
    kubectl(s, args.cluster, 'apply', '--server-side', '--field-manager=kustomize-controller', '-f', str(sync), timeout=60)
    record = {'cluster': args.cluster, 'appliedAt': now(), 'componentsSHA256': sha256(path), 'syncSHA256': sha256(sync), 'fieldManager': 'kustomize-controller',
              'objects': len([d for d in components.split('\n---\n') if d.strip()]) + 2}
    write(s / ('flux-bootstrap-%s.json' % args.cluster), record)
    print('bootstrap: %s Flux v%s controllers Available; sync objects applied once (%d objects)' % (args.cluster, FLUX_VERSION, record['objects']))
    return 0


def publish(args):
    a = argparse.Namespace(**vars(args))
    stage(a)
    push(a)
    for cluster in args.cluster:
        wait_cluster(args.state, cluster, args.timeout)
    print('publish: r%d reconciled on %s' % (args.revision, ', '.join(args.cluster) or 'no cluster (waited later)'))
    return 0


# ---------------------------------------------------------------- evidence

def inventory(obj):
    return sorted(e['id'] for e in obj.get('status', {}).get('inventory', {}).get('entries', []))


def snapshot(state, cluster):
    ks = kustomizations(state, cluster)
    oci = kget(state, cluster, 'get', 'ocirepositories.source.toolkit.fluxcd.io', '-A')['items']
    hr = kget(state, cluster, 'get', 'helmreleases.helm.toolkit.fluxcd.io', '-A')['items']
    hrepo = kget(state, cluster, 'get', 'helmrepositories.source.toolkit.fluxcd.io', '-A')['items']
    charts = kget(state, cluster, 'get', 'helmcharts.source.toolkit.fluxcd.io', '-A')['items']
    deploys = kget(state, cluster, '-n', 'flux-system', 'get', 'deployments')['items']

    def cond(o):
        return sorted([c['type'], c['status'], c.get('reason', '')] for c in o.get('status', {}).get('conditions', []))
    return {
        'cluster': cluster, 'observedAt': now(),
        'controllers': {d['metadata']['name']: {'image': d['spec']['template']['spec']['containers'][0]['image'], 'uid': d['metadata']['uid'],
                                                'availableReplicas': d['status'].get('availableReplicas', 0)} for d in deploys},
        'sources': {o['metadata']['namespace'] + '/' + o['metadata']['name']: {
            'url': o['spec']['url'], 'ref': o['spec'].get('ref'), 'artifactRevision': o.get('status', {}).get('artifact', {}).get('revision'),
            'artifactDigest': o.get('status', {}).get('artifact', {}).get('digest'), 'generation': o['metadata']['generation'],
            'observedGeneration': o.get('status', {}).get('observedGeneration'), 'conditions': cond(o)} for o in oci + hrepo},
        'helmCharts': {o['metadata']['namespace'] + '/' + o['metadata']['name']: {
            'chart': o['spec'].get('chart'), 'version': o['spec'].get('version'), 'artifactRevision': o.get('status', {}).get('artifact', {}).get('revision'),
            'artifactDigest': o.get('status', {}).get('artifact', {}).get('digest'), 'conditions': cond(o)} for o in charts},
        'kustomizations': {n: {'path': o['spec']['path'], 'prune': o['spec'].get('prune'), 'wait': o['spec'].get('wait'),
                               'dependsOn': o['spec'].get('dependsOn', []), 'serviceAccountName': o['spec'].get('serviceAccountName'),
                               'remote': bool(o['spec'].get('kubeConfig')), 'suspend': o['spec'].get('suspend', False),
                               'generation': o['metadata']['generation'], 'observedGeneration': o.get('status', {}).get('observedGeneration'),
                               'lastAppliedRevision': o.get('status', {}).get('lastAppliedRevision'),
                               'lastAttemptedRevision': o.get('status', {}).get('lastAttemptedRevision'),
                               'conditions': cond(o), 'inventory': inventory(o)} for n, o in ks.items()},
        'helmReleases': {o['metadata']['namespace'] + '/' + o['metadata']['name']: {
            'generation': o['metadata']['generation'], 'observedGeneration': o.get('status', {}).get('observedGeneration'),
            'conditions': cond(o), 'driftDetection': o['spec'].get('driftDetection'),
            'history': [{k: h.get(k) for k in ('name', 'namespace', 'version', 'chartName', 'chartVersion', 'appVersion', 'digest', 'status', 'configDigest')}
                        for h in o.get('status', {}).get('history', [])],
            'lastAttemptedRevision': o.get('status', {}).get('lastAttemptedRevision'),
            'lastAttemptedRevisionDigest': o.get('status', {}).get('lastAttemptedRevisionDigest')} for o in hr},
    }


def record(args):
    snap = {c: snapshot(args.state, c) for c in args.cluster}
    write(pathlib.Path(args.state) / 'evidence' / ('flux-%s.json' % args.name), snap)
    print('record: Flux snapshot %s for %s' % (args.name, ', '.join(args.cluster)))
    return 0


def labels(obj):
    return obj['metadata'].get('labels', {}) or {}


def flux_owned(obj):
    return 'kustomize.toolkit.fluxcd.io/name' in labels(obj)


def kro_marked(obj):
    l = labels(obj)
    return any(k.startswith('kro.run/') for k in l) or 'applyset.kubernetes.io/part-of' in l or l.get('app.kubernetes.io/managed-by') == 'kro'


def ownership(state, snaps):
    """One writer per site object: a named Flux layer for shared infrastructure,
    kro only for per-operation worker objects. Returns (record, problems)."""
    s = pathlib.Path(state)
    problems = []
    cell_ks = snaps['cell']['kustomizations']
    every = sorted({e for c in snaps.values() for k in c['kustomizations'].values() for e in k['inventory']})
    layers = {}
    for layer, expected in LAYER_INVENTORY.items():
        layers[layer] = cell_ks.get(layer, {}).get('inventory')
        if layers[layer] != expected:
            problems.append('%s inventory is not exactly %s: %s' % (layer, expected, layers[layer]))
    shared = []
    for (kind, ns, name), layer in sorted(SHARED.items(), key=str):
        r = kubectl(s, 'cell', *(['-n', ns] if ns else []), 'get', kind, name, '-o', 'json', check=False)
        if r.returncode:
            problems.append('shared %s %s/%s is missing' % (kind, ns, name))
            continue
        o = json.loads(r.stdout)
        rec = {'kind': kind, 'namespace': ns, 'name': name, 'uid': o['metadata']['uid'], 'fluxLayer': labels(o).get('kustomize.toolkit.fluxcd.io/name'),
               'kroMarked': kro_marked(o), 'ownerReferences': sorted(x.get('kind', '') for x in o['metadata'].get('ownerReferences', [])),
               'inLayerInventory': any(e.startswith('%s_%s_' % (ns or '', name)) for e in cell_ks.get('flux-system/' + layer, {}).get('inventory', []))}
        shared.append(rec)
        if rec['fluxLayer'] != layer or rec['kroMarked'] or rec['ownerReferences'] or not rec['inLayerInventory']:
            problems.append('shared object not solely owned by Flux %s: %s' % (layer, rec))
    kro_written = []
    for kind, ns in WORKER_KINDS + (('deployments', SITE), ('services', SITE), ('configmaps', SITE), ('persistentvolumeclaims', SITE)):
        for o in kget(s, 'cell', '-n', ns, 'get', kind)['items']:
            name = o['metadata']['name']
            worker = name.startswith('w-op-')
            if kind == 'jobs' and not worker:
                problems.append('Job outside the per-operation set: %s' % name)
            if not worker and not kro_marked(o):
                continue
            rec = {'kind': kind, 'namespace': ns, 'name': name, 'uid': o['metadata']['uid'], 'kroNode': labels(o).get('kro.run/node-id'),
                   'fluxOwned': flux_owned(o), 'inFluxInventory': any(e.startswith('%s_%s_' % (ns, name)) for e in every)}
            kro_written.append(rec)
            if (kind, ns) not in WORKER_KINDS or not worker or not rec['kroNode'] or rec['fluxOwned'] or rec['inFluxInventory']:
                problems.append('kro wrote outside the per-operation set, or a worker object is not solely kro-owned: %s' % rec)
    for forbidden in ('batch_Job', '_Pod', '_MachineAllocationHandoff', '_Secret'):
        hits = [e for e in every if e.endswith(forbidden)]
        if hits:
            problems.append('Flux inventory contains %s: %s' % (forbidden, hits[:4]))
    if [e for e in every if '_w-op-' in e]:
        problems.append('a per-operation worker object is in a Flux inventory')
    deps = {n: sorted(d['name'] for d in cell_ks.get(n, {}).get('dependsOn', [])) for n in ['flux-system/fleet-site-base'] + SITE_LAYERS + ['flux-system/fleet-site-graph']}
    if 'kro' in deps['flux-system/fleet-site-base'] + deps['flux-system/fleet-site-services']:
        problems.append('shared site infrastructure depends on kro: %s' % deps)
    if not {'fleet-site-graph', 'fleet-site-services'} <= set(deps['flux-system/fleet-site-dispatch']):
        problems.append('the dispatcher does not depend on the graph and the shared services: %s' % deps)
    record = {'observedAt': now(), 'layerInventories': layers, 'sharedObjects': shared, 'kroWrittenObjects': kro_written,
              'siteDependsOn': deps, 'problems': problems}
    return record, problems


def ownership_cmd(args):
    snaps = {c: snapshot(args.state, c) for c in CLUSTERS}
    record, problems = ownership(args.state, snaps)
    write(pathlib.Path(args.state) / 'evidence' / ('flux-ownership-%s.json' % args.name), record)
    if problems:
        die('ownership audit failed:\n  ' + '\n  '.join(problems))
    print('ownership: %d shared site objects each owned by one Flux layer; kro wrote only %d per-operation worker objects'
          % (len(record['sharedObjects']), len(record['kroWrittenObjects'])))
    return 0


def verify(args):
    """Source identity, readiness, ownership and inventory on every cluster."""
    s = pathlib.Path(args.state)
    rev = current(s)
    problems = []
    snaps = {}
    for cluster in CLUSTERS:
        snap = snapshot(s, cluster)
        snaps[cluster] = snap
        src = snap['sources'].get('flux-system/fleet-gitops', {})
        if src.get('artifactRevision') != rev['fluxRevision']:
            problems.append('%s source at %s, pushed %s' % (cluster, src.get('artifactRevision'), rev['fluxRevision']))
        for n in ['flux-system/fleet-cluster'] + EXPECTED[cluster] + (SITE_LAYERS if cluster == 'cell' else []):
            k = snap['kustomizations'].get(n)
            if not k or ['Ready', 'True', 'ReconciliationSucceeded'] not in k['conditions'] or k['generation'] != k['observedGeneration'] or k['lastAppliedRevision'] != rev['fluxRevision']:
                problems.append('%s %s not Ready at the pushed revision: %s' % (cluster, n, k and (k['conditions'], k['lastAppliedRevision'])))
        for n in HELMRELEASES.get(cluster, []):
            h = snap['helmReleases'].get(n)
            if not h or ['Ready', 'True', 'InstallSucceeded'] not in h['conditions'] and ['Ready', 'True', 'UpgradeSucceeded'] not in h['conditions'] or h['generation'] != h['observedGeneration']:
                problems.append('%s HelmRelease %s not Ready: %s' % (cluster, n, h and h['conditions']))
        if set(snap['controllers']) != {'source-controller', 'kustomize-controller', 'helm-controller', 'notification-controller'} or any(
                '@sha256:' not in c['image'] or c['availableReplicas'] != 1 for c in snap['controllers'].values()):
            problems.append('%s Flux controllers not the pinned set: %s' % (cluster, snap['controllers']))
    cell = snaps['cell']
    hub = snaps['hub']
    kro_src = cell['sources'].get('flux-system/kro-chart', {})
    if (kro_src.get('ref') or {}).get('digest') != KRO_CHART_DIGEST or not str(kro_src.get('artifactRevision', '')).endswith(KRO_CHART_DIGEST):
        problems.append('kro chart source not at pinned digest: %s' % kro_src)
    kro_hist = (cell['helmReleases'].get('flux-system/kro', {}).get('history') or [{}])[0]
    # helm-controller suffixes an OCI chart's version with its manifest digest.
    if kro_hist.get('chartVersion') != '0.9.4+' + KRO_CHART_DIGEST[7:19] or kro_hist.get('status') != 'deployed':
        problems.append('kro release history: %s' % kro_hist)
    karmada_chart = next((c for c in hub['helmCharts'].values() if c['chart'] == 'karmada'), {})
    if karmada_chart.get('artifactDigest') != 'sha256:' + KARMADA_CHART_SHA256:
        problems.append('Karmada chart artifact digest %s, expected sha256:%s' % (karmada_chart.get('artifactDigest'), KARMADA_CHART_SHA256))
    karmada_hist = (hub['helmReleases'].get('flux-system/karmada', {}).get('history') or [{}])[0]
    if karmada_hist.get('chartVersion') != 'v1.16.0' or karmada_hist.get('status') != 'deployed':
        problems.append('karmada release history: %s' % karmada_hist)
    if PLATFORM_LAYER_INVENTORY:
        platform = snaps['platform']
        ipam_src = platform['sources'].get('flux-system/ipam-kustomize', {})
        if (ipam_src.get('ref') or {}).get('digest') != IPAM_KUSTOMIZE_DIGEST or not str(ipam_src.get('artifactRevision', '')).endswith(IPAM_KUSTOMIZE_DIGEST):
            problems.append('IPAM manifests source not at pinned digest: %s' % ipam_src)
        for layer, expected in PLATFORM_LAYER_INVENTORY.items():
            got = platform['kustomizations'].get(layer, {}).get('inventory')
            if got != expected:
                problems.append('%s inventory is not exactly %s: %s' % (layer, expected, got))
        images = sorted({c['image'] for p in kget(s, 'platform', '-n', 'ipam-system', 'get', 'pods', '-l', 'app=ipam-apiserver')['items']
                         for c in p['spec']['containers'] + p['spec'].get('initContainers', [])})
        if images != [IPAM_IMAGE]:
            problems.append('IPAM Pods do not run only the pinned image %s: %s' % (IPAM_IMAGE, images))
    # Ownership: Flux layers own the shared site; kro alone owns per-operation workers.
    owners, owner_problems = ownership(s, snaps)
    problems += owner_problems
    if owners['kroWrittenObjects']:
        problems.append('kro wrote objects before dispatch: %s' % owners['kroWrittenObjects'])
    pvc = kget(s, 'cell', '-n', 'fleet-site', 'get', 'pvc', 'fleet-site-state')
    pvc_ann = pvc['metadata'].get('annotations', {})
    if pvc_ann.get('kustomize.toolkit.fluxcd.io/prune') != 'disabled' or pvc_ann.get('kustomize.toolkit.fluxcd.io/ssa') != 'IfNotPresent':
        problems.append('site volume claim is not retained by Flux policy: %s' % pvc_ann)
    protected = {}
    for cluster, kind, ns, name in (('cell', 'namespace', None, 'fleet-site'), ('cell', 'namespace', None, 'fleet-coordination'),
                                    ('cell', 'crd', None, 'machineallocationhandoffs.coordination.fleet.datumapis.com'),
                                    ('cell', 'resourcegraphdefinition', None, RGD), ('cell', DISPATCHER[0], SITE, DISPATCHER[1]),
                                    ('cell', 'storageclass', None, 'fleet-site-retain')):
        o = kget(s, cluster, *(['-n', ns] if ns else []), 'get', kind, name)
        value = o['metadata'].get('annotations', {}).get('kustomize.toolkit.fluxcd.io/prune')
        protected['%s/%s/%s' % (cluster, kind, name)] = value
        if value != 'disabled':
            problems.append('%s %s %s is prunable' % (cluster, kind, name))
    handoffs = kget(s, 'cell', '-n', 'fleet-coordination', 'get', 'machineallocationhandoffs')['items']
    if any(flux_owned(h) for h in handoffs):
        problems.append('a handoff carries Flux ownership labels')
    # Bootstrap dependency ordering, as Flux recorded it while every cluster was sampled.
    seen = json.loads((s / 'flux-wait-observations.json').read_text()) if (s / 'flux-wait-observations.json').exists() else []
    first = revisions(s)[0]['fluxRevision']
    events = [o for o in seen if o['reason'] == 'DependencyNotReady']
    # Gating before apply: the dependent had applied nothing of r1. Gating after
    # apply is a dependency re-reconciling at the same revision (for example on
    # a fresh request) and is recorded, not counted as either evidence or fault.
    gated = [o for o in events if o['revision'] == first and not o['lastAppliedRevision'] and not o['inventoryEntries']]
    regated = [o for o in events if o['revision'] == first and o['lastAppliedRevision'] == first]
    for o in events:
        if o['revision'] == first and o['lastAppliedRevision'] not in (None, first):
            problems.append('dependent applied another revision during bootstrap: %s' % o)
        elif o['revision'] == first and not o['lastAppliedRevision'] and o['inventoryEntries']:
            problems.append('dependent has an inventory before applying while its dependency is not Ready: %s' % o)
    for cluster in ('cell', 'hub'):
        if not [o for o in gated if o['cluster'] == cluster]:
            problems.append('no dependency gating before apply was observed on %s during bootstrap' % cluster)
    result = {'observedAt': now(), 'revision': rev, 'revisions': revisions(s), 'tools': json.loads((s / 'flux-tools' / 'tools.json').read_text()),
              'bootstrap': [json.loads((s / ('flux-bootstrap-%s.json' % c)).read_text()) for c in CLUSTERS],
              'pins': {'kroChartManifestDigest': KRO_CHART_DIGEST, 'kroChartLayerSHA256': KRO_CHART_LAYER_SHA256,
                       'karmadaChartSHA256': KARMADA_CHART_SHA256, 'registryImage': REGISTRY_IMAGE},
              'ownership': owners, 'pruneProtected': protected, 'siteVolumeAnnotations': {k: v for k, v in pvc_ann.items() if 'fluxcd' in k},
              'handoffsFluxOwned': False, 'bootstrapDependencyEvents': events, 'bootstrapGatedBeforeApply': gated,
              'bootstrapRegatedAfterApply': regated, 'problems': problems}
    write(s / 'evidence' / 'flux-verify.json', result)
    write(s / 'evidence' / 'flux-initial.json', snaps)
    if problems:
        die('flux verification failed:\n  ' + '\n  '.join(problems))
    print('flux-verify: 3 clusters Ready at %s; pinned charts and controllers; Flux layers own %d shared site objects; kro owns %d per-operation objects; volume, CRD, graph and instance prune-protected'
          % (rev['fluxRevision'], len(owners['sharedObjects']), len(owners['kroWrittenObjects'])))
    return 0


# ---------------------------------------------------------------- scenarios

def site_facts(state):
    """Effect and custody facts that no GitOps action may change."""
    s = pathlib.Path(state)
    jobs = sorted((j['metadata']['name'], j['metadata']['uid']) for j in kget(s, 'cell', '-n', 'fleet-site', 'get', 'jobs')['items'])
    pvc = kget(s, 'cell', '-n', 'fleet-site', 'get', 'pvc', 'fleet-site-state')
    pv = kget(s, 'cell', 'get', 'pv', pvc['spec']['volumeName'])
    # The provider project's custody and the site store's state for the
    # Device (its fences and journal).
    records, custody = harness.custody_digest(s)
    store = kubectl(s, 'cell', '-n', 'fleet-site', 'exec', 'deploy/fleet-site-runtime', '-c', 'libvirt', '--', 'cat', harness.STATE_PATH).stdout
    return {'jobs': jobs, 'pvcUID': pvc['metadata']['uid'], 'pvUID': pv['metadata']['uid'], 'pvPhase': pv['status'].get('phase'),
            'reclaimPolicy': pv['spec']['persistentVolumeReclaimPolicy'], 'siteStoreSHA256': hashlib.sha256(store.encode()).hexdigest(),
            'custodyRecords': records, 'custodyDigest': custody}


def same_facts(before, after, what):
    if before != after:
        die('%s changed effect or custody facts: before %s after %s' % (what, before, after))


def wait_for(fn, timeout, what, interval=2):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        value = fn()
        if value:
            return value
        time.sleep(interval)
    die(what + ' not observed before deadline', harness.EXIT_TIMEOUT)


def drift(args):
    """Out-of-band changes corrected by Flux, not by the harness."""
    s = pathlib.Path(args.state)
    before = site_facts(s)
    out = {'observedAt': now(), 'cases': []}
    # 1. helm-controller drift correction of the kro release.
    d = kget(s, 'cell', '-n', 'kro-system', 'get', 'deployment', 'kro')
    kubectl(s, 'cell', '-n', 'kro-system', 'scale', 'deployment/kro', '--replicas=0')
    request(s, 'cell', 'helmreleases.helm.toolkit.fluxcd.io', 'flux-system/kro')

    def kro_back():
        x = kget(s, 'cell', '-n', 'kro-system', 'get', 'deployment', 'kro')
        return x if x['spec']['replicas'] == 1 and x['status'].get('availableReplicas') == 1 else None
    restored = wait_for(kro_back, 300, 'helm-controller correction of kro replicas')
    hr = kget(s, 'cell', '-n', 'flux-system', 'get', 'helmreleases.helm.toolkit.fluxcd.io', 'kro')
    events = [e.get('message', '')[:200] for e in kget(s, 'cell', '-n', 'flux-system', 'get', 'events', '--field-selector', 'involvedObject.name=kro')['items']
              if 'drift' in e.get('message', '').lower()]
    out['cases'].append({'case': 'kro Deployment scaled to 0 out of band', 'correctedBy': 'helm-controller driftDetection=enabled',
                         'deploymentUID': d['metadata']['uid'], 'sameDeployment': restored['metadata']['uid'] == d['metadata']['uid'],
                         'releaseVersion': [h.get('version') for h in hr['status'].get('history', [])][:1], 'events': events[:3]})
    # 2. kustomize-controller recreates a deleted Karmada-API object over the remote kubeconfig.
    old = kget(s, 'hub', 'get', 'resourceinterpretercustomizations.config.karmada.io', 'fleet-machine-allocation-handoff', config='hub-admin.yaml')
    kubectl(s, 'hub', 'delete', 'resourceinterpretercustomizations.config.karmada.io', 'fleet-machine-allocation-handoff', config='hub-admin.yaml')
    request(s, 'hub', 'kustomizations.kustomize.toolkit.fluxcd.io', 'karmada-system/fleet-karmada-api')

    def interp_back():
        r = kubectl(s, 'hub', 'get', 'resourceinterpretercustomizations.config.karmada.io', 'fleet-machine-allocation-handoff', '-o', 'json', check=False, config='hub-admin.yaml')
        return json.loads(r.stdout) if r.returncode == 0 else None
    new = wait_for(interp_back, 300, 'Flux recreation of the Karmada interpreter')
    if new['metadata']['uid'] == old['metadata']['uid'] or new['spec'] != old['spec']:
        die('interpreter was not recreated from the source')
    out['cases'].append({'case': 'Karmada interpreter deleted on the Karmada API', 'correctedBy': 'kustomize-controller (remote kubeConfig)',
                         'oldUID': old['metadata']['uid'], 'newUID': new['metadata']['uid'], 'specFromSource': True})
    # 3. kustomize-controller reverts an in-place edit of a site policy object.
    kubectl(s, 'cell', 'patch', 'clusterrole', 'fleet-site-kro', '--type=json', '-p',
            '[{"op":"add","path":"/rules/-","value":{"apiGroups":[""],"resources":["secrets"],"verbs":["get"]}}]')
    request(s, 'cell', 'kustomizations.kustomize.toolkit.fluxcd.io', 'flux-system/fleet-site-base')

    def role_back():
        rules = kget(s, 'cell', 'get', 'clusterrole', 'fleet-site-kro').get('rules', [])
        return not any('secrets' in r.get('resources', []) for r in rules)
    wait_for(role_back, 300, 'Flux reversion of the widened kro grant')
    out['cases'].append({'case': 'fleet-site-kro ClusterRole widened to read Secrets', 'correctedBy': 'kustomize-controller server-side apply', 'secretReadRemoved': True})
    # 4. kustomize-controller recreates the deleted shared boot Service, with the
    # same fixed ClusterIP, as the scoped services identity. kro must not write it.
    svc = kget(s, 'cell', '-n', SITE, 'get', 'service', 'fleet-site-boot')
    kubectl(s, 'cell', '-n', SITE, 'delete', 'service', 'fleet-site-boot', '--wait=true')
    request(s, 'cell', 'kustomizations.kustomize.toolkit.fluxcd.io', 'flux-system/fleet-site-services')

    def svc_back():
        r = kubectl(s, 'cell', '-n', SITE, 'get', 'service', 'fleet-site-boot', '-o', 'json', check=False)
        o = json.loads(r.stdout) if r.returncode == 0 else None
        return o if o and o['metadata']['uid'] != svc['metadata']['uid'] else None
    new_svc = wait_for(svc_back, 300, 'Flux recreation of the boot Service')
    same = {k: new_svc['spec'].get(k) == svc['spec'].get(k) for k in ('clusterIP', 'clusterIPs', 'ports', 'selector', 'type')}
    if not all(same.values()) or new_svc['spec']['clusterIP'] != BOOT_SERVICE_IP or labels(new_svc).get('kustomize.toolkit.fluxcd.io/name') != 'fleet-site-services' or kro_marked(new_svc):
        die('boot Service was not recreated by Flux with the same fixed ClusterIP and spec: %s' % same)
    out['cases'].append({'case': 'shared boot Service deleted', 'correctedBy': 'kustomize-controller as fleet-site-services-reconciler', 'oldUID': svc['metadata']['uid'],
                         'newUID': new_svc['metadata']['uid'], 'clusterIP': new_svc['spec']['clusterIP'], 'specUnchanged': same, 'fluxLayer': 'fleet-site-services', 'kroMarked': False})
    # 5. kustomize-controller restores the shared observer Deployment scaled to 0.
    obs = kget(s, 'cell', '-n', SITE, 'get', 'deployment', 'fleet-site-observer')
    kubectl(s, 'cell', '-n', SITE, 'scale', 'deployment/fleet-site-observer', '--replicas=0')
    request(s, 'cell', 'kustomizations.kustomize.toolkit.fluxcd.io', 'flux-system/fleet-site-services')

    def obs_back():
        x = kget(s, 'cell', '-n', SITE, 'get', 'deployment', 'fleet-site-observer')
        return x if x['spec']['replicas'] == 1 and x['status'].get('availableReplicas') == 1 and x['metadata']['generation'] > obs['metadata']['generation'] else None
    restored_obs = wait_for(obs_back, 300, 'Flux restoration of the observer Deployment')
    if restored_obs['metadata']['uid'] != obs['metadata']['uid']:
        die('observer Deployment was replaced instead of restored')
    out['cases'].append({'case': 'shared observer Deployment scaled to 0', 'correctedBy': 'kustomize-controller as fleet-site-services-reconciler',
                         'deploymentUID': obs['metadata']['uid'], 'sameDeployment': True, 'generationBefore': obs['metadata']['generation'],
                         'generationAfter': restored_obs['metadata']['generation']})
    # The dispatcher must return to ACTIVE against the repaired references.
    wait_for(lambda: kget(s, 'cell', '-n', SITE, 'get', DISPATCHER[0], DISPATCHER[1]).get('status', {}).get('state') == 'ACTIVE', 300, 'dispatcher ACTIVE after repair')
    snaps = {c: snapshot(s, c) for c in CLUSTERS}
    owners, problems = ownership(s, snaps)
    if problems:
        die('ownership after drift repair:\n  ' + '\n  '.join(problems))
    out['ownershipAfterRepair'] = owners
    after = site_facts(s)
    same_facts(before, after, 'drift correction')
    out['siteFacts'] = after
    write(s / 'evidence' / 'flux-drift.json', out)
    print('flux-drift: helm-controller restored kro, kustomize-controller recreated the Karmada interpreter, reverted a widened grant, recreated the shared boot Service at its fixed ClusterIP and restored the observer; journal, Jobs, volume and custody unchanged')
    return 0


def suspend(state, cluster, ref, value):
    namespace, name = ref.split('/')
    kubectl(state, cluster, '-n', namespace, 'patch', 'kustomizations.kustomize.toolkit.fluxcd.io', name, '--field-manager=fleet-fault-injection',
            '--type=merge', '-p', json.dumps({'spec': {'suspend': value}}))


def shared_uids(state):
    """Identity of every Flux-owned shared site object, the PV and the dispatcher graph."""
    s = pathlib.Path(state)
    out = {}
    for kind, ns, name in sorted(SHARED, key=str):
        out['%s/%s/%s' % (kind, ns, name)] = kget(s, 'cell', *(['-n', ns] if ns else []), 'get', kind, name)['metadata']['uid']
    out['rgd'] = kget(s, 'cell', 'get', 'resourcegraphdefinition', RGD)['metadata']['uid']
    out['storageclass'] = kget(s, 'cell', 'get', 'storageclass', 'fleet-site-retain')['metadata']['uid']
    return out


def worker_objects(state):
    s = pathlib.Path(state)
    return sorted((kind, o['metadata']['name'], o['metadata']['uid']) for kind, ns in WORKER_KINDS
                  for o in kget(s, 'cell', '-n', ns, 'get', kind)['items'] if o['metadata']['name'].startswith('w-op-'))


def handoff_outcome(s, handoff):
    """The install handoff's outcome on the cell. Once the coordinator
    accepted the result it labels the handoff answered and the cell deletes
    its copy, so an answered handoff counts as Succeeded only when the
    provider project's operation for it succeeded."""
    obj = kget(s, 'cell', '-n', COORD, 'get', 'machineallocationhandoff', handoff)
    result = obj.get('status', {}).get('observation', {}).get('result')
    if result:
        return result.get('phase')
    if obj['metadata'].get('labels', {}).get('coordination.fleet.datumapis.com/answered') != 'true':
        return None
    operation = json.loads(obj['spec']['assignmentJSON'])['operation']
    kind = {'DeviceInstallation': 'deviceinstallation', 'DeviceWipe': 'devicewipe'}[operation['kind']]
    r = subprocess.run(['kubectl', '--kubeconfig', str(s / 'provider' / 'bootstrap-project.json'), '-n', 'fleet-provider', 'get', kind, operation['name'], '-o', 'json'], text=True, capture_output=True)
    if r.returncode != 0:
        return None
    current = json.loads(r.stdout)
    return current.get('status', {}).get('phase') if current['metadata']['uid'] == operation['uid'] else None


def graph_lifecycle(args):
    """Deleting the dispatcher instance removes only kro's per-operation objects.

    Flux then recreates the instance from the source and kro recreates the
    workers, which replay a verified no-op. Shared services, the journal
    volume, the journal, guests and custody are untouched throughout.
    """
    s = pathlib.Path(args.state)
    handoff = (s / 'install-handoff').read_text().strip()
    snaps = {c: snapshot(s, c) for c in CLUSTERS}
    owners, problems = ownership(s, snaps)
    if problems:
        die('ownership with workers present:\n  ' + '\n  '.join(problems))
    want = sorted(['jobs', 'serviceaccounts', 'roles', 'rolebindings'])
    if sorted(k for k, n, _ in worker_objects(s) if n == 'w-' + handoff) != want:
        die('expected one kro-written Job, ServiceAccount, Role and RoleBinding for the install handoff: %s' % worker_objects(s))
    before, uids, workers = site_facts(s), shared_uids(s), worker_objects(s)
    status = handoff_outcome(s, handoff)
    if status != 'Succeeded':
        die('install handoff is not terminal before the graph lifecycle: %s' % status)
    old = kget(s, 'cell', '-n', SITE, 'get', DISPATCHER[0], DISPATCHER[1])
    # Explicit ownership transfer for the fault: suspend the instance's Flux
    # layer so the harness is never a second concurrent writer, then delete the
    # instance out of band.
    suspend(s, 'cell', 'flux-system/fleet-site-dispatch', True)
    kubectl(s, 'cell', '-n', SITE, 'delete', DISPATCHER[0], DISPATCHER[1], '--wait=true', timeout=300)
    wait_for(lambda: not worker_objects(s), 300, 'kro removal of per-operation objects')
    during_uids, during = shared_uids(s), site_facts(s)
    if during_uids != uids:
        die('instance deletion changed shared site objects: %s -> %s' % (uids, during_uids))
    same_facts(dict(before, jobs=[]), during, 'dispatcher instance deletion')
    for d in SITE_DEPLOYMENTS:
        if kget(s, 'cell', '-n', SITE, 'get', 'deployment', d)['status'].get('availableReplicas') != 1:
            die(d + ' became unavailable when the dispatcher was deleted')
    # Resuming hands ownership back: Flux, not the harness, restores the instance.
    suspend(s, 'cell', 'flux-system/fleet-site-dispatch', False)
    wait_cluster(s, 'cell', 600)
    new = kget(s, 'cell', '-n', SITE, 'get', DISPATCHER[0], DISPATCHER[1])
    if new['metadata']['uid'] == old['metadata']['uid'] or labels(new).get('kustomize.toolkit.fluxcd.io/name') != 'fleet-site-dispatch':
        die('dispatcher was not recreated by Flux')

    def replayed():
        j = kget(s, 'cell', '-n', SITE, 'get', 'jobs')['items']
        done = [x for x in j if x['metadata']['name'] == 'w-' + handoff and any(c['type'] == 'Complete' and c['status'] == 'True' for c in x['status'].get('conditions', []))]
        return done[0] if done else None
    job = wait_for(replayed, 600, 'recreated worker Job completing its verified no-op replay', interval=3)
    pods = [p for p in kget(s, 'cell', '-n', SITE, 'get', 'pods', '-l', 'batch.kubernetes.io/controller-uid=' + job['metadata']['uid'])['items']]
    exits = [next((c['state']['terminated']['exitCode'] for c in p['status'].get('containerStatuses', []) if 'terminated' in c.get('state', {})), None) for p in pods]
    if exits != [0]:
        die('recreated worker Job did not finish with one verified no-op replay: %s' % exits)
    after_uids, after = shared_uids(s), site_facts(s)
    if after_uids != uids:
        die('instance recreation changed shared site objects')
    same_facts(dict(before, jobs=[]), dict(after, jobs=[]), 'dispatcher instance recreation')
    snaps = {c: snapshot(s, c) for c in CLUSTERS}
    owners_after, problems = ownership(s, snaps)
    if problems:
        die('ownership after the graph lifecycle:\n  ' + '\n  '.join(problems))
    status_after = handoff_outcome(s, handoff)
    if status_after != 'Succeeded':
        die('install handoff lost its terminal observation across the graph lifecycle: %s' % status_after)
    # kro deletes Jobs without a propagation policy, so batch/v1 orphans their
    # (already terminal) Pods. Recorded, not hidden; see site-execution.md.
    live_jobs = {j['metadata']['uid'] for j in kget(s, 'cell', '-n', SITE, 'get', 'jobs')['items']}
    orphans = [{'pod': p['metadata']['name'], 'phase': p['status'].get('phase')} for p in kget(s, 'cell', '-n', SITE, 'get', 'pods', '-l', 'fleet.datumapis.com/handoff=' + handoff)['items']
               if labels(p).get('batch.kubernetes.io/controller-uid') not in live_jobs]
    if any(o['phase'] not in ('Succeeded', 'Failed') for o in orphans):
        die('a non-terminal worker Pod outlived its Job: %s' % orphans)
    write(s / 'evidence' / 'flux-graph-lifecycle.json', {
        'observedAt': now(), 'handoff': handoff, 'deletedInstanceUID': old['metadata']['uid'], 'recreatedInstanceUID': new['metadata']['uid'],
        'recreatedBy': 'kustomize-controller (Kustomization fleet-site-dispatch)', 'workerObjectsBefore': workers, 'workerObjectsAfter': worker_objects(s),
        'workerObjectsRemovedByKro': True, 'sharedObjectUIDs': uids, 'sharedObjectsUnchanged': True, 'replayJobUID': job['metadata']['uid'], 'replayExitCodes': exits,
        'handoffResultPhase': [status, status_after], 'orphanedTerminalWorkerPods': orphans, 'ownershipWithWorkers': owners, 'ownershipAfter': owners_after, 'siteFactsBefore': before, 'siteFactsAfter': after})
    print('flux-graph-lifecycle: deleting the dispatcher removed only kro per-operation objects; shared services, volume, journal and custody unchanged; Flux recreated it and the replayed Job was a verified no-op')
    return 0


def restart(args):
    """Restart every Flux controller; reconcile must not re-execute or upgrade anything."""
    s = pathlib.Path(args.state)
    before = site_facts(s)
    snap_before = {c: snapshot(s, c) for c in CLUSTERS}

    def pod_uids(cluster):
        return sorted(p['metadata']['uid'] for p in kget(s, cluster, '-n', 'flux-system', 'get', 'pods')['items'] if not p['metadata'].get('deletionTimestamp'))
    pods_before = {c: pod_uids(c) for c in CLUSTERS}
    release_tokens = {}
    for cluster in CLUSTERS:
        kubectl(s, cluster, '-n', 'flux-system', 'delete', 'pod', '--all', '--wait=true', timeout=180)
    for cluster in CLUSTERS:
        kubectl(s, cluster, '-n', 'flux-system', 'wait', '--for=condition=Available', 'deployment', '--all', '--timeout=300s', timeout=320)
        for kind, ref in [('kustomizations.kustomize.toolkit.fluxcd.io', 'flux-system/fleet-cluster')] + [
                ('kustomizations.kustomize.toolkit.fluxcd.io', n) for n in EXPECTED[cluster]] + [('helmreleases.helm.toolkit.fluxcd.io', n) for n in HELMRELEASES.get(cluster, [])]:
            token = request(s, cluster, kind, ref)
            if kind == 'helmreleases.helm.toolkit.fluxcd.io':
                release_tokens[(cluster, ref)] = token
    for cluster in CLUSTERS:
        wait_cluster(s, cluster, 600)
    release_receipts = []
    for (cluster, ref), token in release_tokens.items():
        namespace, name = ref.split('/')
        pending = {'token': token, 'reissued': 0}

        def reconciled_release():
            obj = kget(s, cluster, '-n', namespace, 'get', 'helmreleases.helm.toolkit.fluxcd.io', name)
            if lost(obj, pending['token']):
                pending['token'] = request(s, cluster, 'helmreleases.helm.toolkit.fluxcd.io', ref)
                pending['reissued'] += 1
                return None
            return obj if ready(obj) and acknowledged(obj, pending['token']) else None
        obj = wait_for(reconciled_release, 600, 'post-restart HelmRelease reconcile')
        release_receipts.append({'cluster': cluster, 'release': ref, 'requestedAt': pending['token'], 'lastHandledReconcileAt': obj['status']['lastHandledReconcileAt'],
                                 'lostRequestsReissued': pending['reissued']})
    time.sleep(10)
    snap_after = {c: snapshot(s, c) for c in CLUSTERS}
    changes = []
    for c in CLUSTERS:
        for n, h in snap_before[c]['helmReleases'].items():
            if [x['version'] for x in h['history']] != [x['version'] for x in snap_after[c]['helmReleases'][n]['history']]:
                changes.append('%s %s Helm release revision changed' % (c, n))
        for n, k in snap_before[c]['kustomizations'].items():
            if k['inventory'] != snap_after[c]['kustomizations'][n]['inventory']:
                changes.append('%s %s inventory changed' % (c, n))
        if set(pods_before[c]) & set(pod_uids(c)) or len(pod_uids(c)) != len(pods_before[c]):
            changes.append('%s controller Pods were not all replaced' % c)
    if changes:
        die('controller restart changed managed state: ' + '; '.join(changes))
    after = site_facts(s)
    same_facts(before, after, 'Flux controller restart')
    write(s / 'evidence' / 'flux-restart.json', {'observedAt': now(), 'restartedControllers': {c: sorted(snap_after[c]['controllers']) for c in CLUSTERS},
                                                 'controllerPodsBefore': pods_before, 'controllerPodsAfter': {c: pod_uids(c) for c in CLUSTERS},
                                                 'helmReleaseVersions': {c: {n: [x['version'] for x in h['history']] for n, h in snap_after[c]['helmReleases'].items()} for c in CLUSTERS},
                                                 'inventoriesUnchanged': True, 'helmReconcileReceipts': release_receipts, 'siteFacts': after})
    print('flux-restart: every Flux controller on 3 clusters restarted and re-reconciled; no Helm upgrade, inventory change, new Job, journal or custody change')
    return 0


def fail_closed(args):
    """A bad dependency revision blocks dependents; nothing running is disturbed."""
    s = pathlib.Path(args.state)
    before = site_facts(s)
    kro_before = kget(s, 'cell', '-n', 'kro-system', 'get', 'deployment', 'kro')
    site_before = kget(s, 'cell', '-n', SITE, 'get', DISPATCHER[0], DISPATCHER[1])
    shared_before = shared_uids(s)
    services_before = {d: kget(s, 'cell', '-n', SITE, 'get', 'deployment', d)['metadata']['generation'] for d in SITE_DEPLOYMENTS}
    good = current(s)
    bad = argparse.Namespace(state=str(s), root=args.root, revision=good['revision'] + 1, site='dispatch', bad_kro_chart=True, drop_site_volume=False,
                             purpose='fail-closed: kro chart pinned to an absent digest', cluster=[], timeout=0)
    stage(bad)
    push(bad)
    rev = current(s)['fluxRevision']
    request(s, 'cell', 'ocirepositories.source.toolkit.fluxcd.io', 'flux-system/fleet-gitops')

    def blocked():
        ks = kustomizations(s, 'cell')
        kro, site = ks['flux-system/kro'], ks['flux-system/fleet-site-dispatch']
        oci = kget(s, 'cell', '-n', 'flux-system', 'get', 'ocirepositories.source.toolkit.fluxcd.io', 'kro-chart')
        kro_failed = conditions(kro).get('Ready', {}).get('status') == 'False' and kro['status'].get('lastAttemptedRevision') == rev
        dep = conditions(site).get('Ready', {}).get('reason') == 'DependencyNotReady'
        # Shared site infrastructure does not depend on kro: it applies the new revision.
        shared_applied = all(ready(ks[n]) and ks[n]['status'].get('lastAppliedRevision') == rev for n in ('flux-system/fleet-site-base', 'flux-system/fleet-site-services'))
        return (kro, site, oci) if kro_failed and dep and shared_applied and conditions(oci).get('Ready', {}).get('status') == 'False' else None
    for n in ('flux-system/fleet-site-base', 'flux-system/fleet-site-services', 'flux-system/fleet-site-graph', 'flux-system/fleet-site-dispatch'):
        request(s, 'cell', 'kustomizations.kustomize.toolkit.fluxcd.io', n)
    kro, site, oci = wait_for(blocked, 600, 'kro failure blocking only the dispatcher layers', interval=5)
    graph = kustomizations(s, 'cell')['flux-system/fleet-site-graph']
    site_obj = kget(s, 'cell', '-n', SITE, 'get', DISPATCHER[0], DISPATCHER[1])
    marker = site_obj['metadata'].get('annotations', {}).get('fleet.datumapis.com/source-marker')
    if marker != site_before['metadata'].get('annotations', {}).get('fleet.datumapis.com/source-marker') or site.get('status', {}).get('lastAppliedRevision') == rev:
        die('the dependent dispatcher revision applied while kro was not Ready')
    services_during = {d: kget(s, 'cell', '-n', SITE, 'get', 'deployment', d)['metadata']['generation'] for d in services_before}
    if shared_uids(s) != shared_before or services_during != services_before:
        die('the failed kro revision disturbed shared site services: %s -> %s' % (services_before, services_during))
    kro_now = kget(s, 'cell', '-n', 'kro-system', 'get', 'deployment', 'kro')
    if kro_now['metadata']['uid'] != kro_before['metadata']['uid'] or kro_now['status'].get('availableReplicas') != 1:
        die('the running kro controller was disturbed by the failed revision')
    during = site_facts(s)
    same_facts(before, during, 'failed dependency revision')
    blocked_record = {'revision': rev, 'kroKustomization': sorted([c['type'], c['status'], c.get('reason', '')] for c in kro['status']['conditions']),
                      'kroChartSource': sorted([c['type'], c['status'], c.get('reason', '')] for c in oci['status']['conditions']),
                      'graphReady': conditions(graph).get('Ready', {}).get('reason'), 'siteReady': conditions(site).get('Ready', {}).get('reason'),
                      'siteLastAppliedRevision': site['status'].get('lastAppliedRevision'), 'siteMarkerUnchanged': marker,
                      'kroDeploymentUnchanged': True, 'siteFactsUnchanged': True, 'sharedLayersAppliedBadRevision': True,
                      'sharedServiceGenerationsUnchanged': services_during, 'sharedObjectUIDsUnchanged': True}
    fixed = argparse.Namespace(state=str(s), root=args.root, revision=good['revision'] + 2, site='dispatch', bad_kro_chart=False, drop_site_volume=False,
                               purpose='fail-closed recovery: pinned kro chart restored', cluster=['cell'], timeout=600)
    publish(fixed)
    site_obj = kget(s, 'cell', '-n', SITE, 'get', DISPATCHER[0], DISPATCHER[1])
    if site_obj['metadata'].get('annotations', {}).get('fleet.datumapis.com/source-marker') != 'r%d' % fixed.revision:
        die('recovered revision did not reach the dispatcher')
    after = site_facts(s)
    same_facts(before, after, 'fail-closed recovery')
    write(s / 'evidence' / 'flux-fail-closed.json', {'observedAt': now(), 'blocked': blocked_record, 'recoveredRevision': current(s), 'siteFacts': after,
                                                     'missingDigest': MISSING_DIGEST})
    print('flux-fail-closed: absent kro chart digest left kro not Ready; graph and dispatcher reported DependencyNotReady and were not applied; shared site layers applied it with services untouched; running kro and site untouched; restored revision converged')
    return 0


def retention(args):
    """Removing the journal volume from the source must not delete or replace it."""
    s = pathlib.Path(args.state)
    before = site_facts(s)
    base = current(s)['revision']
    drop = argparse.Namespace(state=str(s), root=args.root, revision=base + 1, site='dispatch', bad_kro_chart=False, drop_site_volume=True,
                              purpose='retention: site volume claim removed from the source', cluster=['cell'], timeout=600)
    publish(drop)
    inv = kustomizations(s, 'cell')['flux-system/fleet-site-base']
    if any('PersistentVolumeClaim' in e for e in inventory(inv)):
        die('volume claim still in the Flux inventory after its removal from the source')
    pvc = kget(s, 'cell', '-n', 'fleet-site', 'get', 'pvc', 'fleet-site-state')
    if pvc['metadata'].get('deletionTimestamp'):
        die('Flux garbage-collected the journal volume claim')
    removed = site_facts(s)
    same_facts(before, removed, 'source removal of the volume claim')
    restore = argparse.Namespace(state=str(s), root=args.root, revision=base + 2, site='dispatch', bad_kro_chart=False, drop_site_volume=False,
                                 purpose='retention: site volume claim restored to the source (create-once policy)', cluster=['cell'], timeout=600)
    publish(restore)
    inv = kustomizations(s, 'cell')['flux-system/fleet-site-base']
    if not any(e == 'fleet-site_fleet-site-state__PersistentVolumeClaim' for e in inventory(inv)):
        die('restored claim not back in the Flux inventory')
    after = site_facts(s)
    same_facts(before, after, 'restoring the volume claim to the source')
    write(s / 'evidence' / 'flux-retention.json', {'observedAt': now(), 'removedAt': drop.revision, 'restoredAt': restore.revision,
                                                   'pvcUID': after['pvcUID'], 'pvUID': after['pvUID'], 'reclaimPolicy': after['reclaimPolicy'],
                                                   'inventoryWithoutClaim': True, 'claimNotDeleted': True, 'siteFacts': after})
    print('flux-retention: removing the volume claim from the source left it, its PV, journal and custody unchanged; restoring it re-adopted the same claim')
    return 0


def main():
    p = argparse.ArgumentParser()
    sub = p.add_subparsers(dest='cmd', required=True)

    def add(name, *extra):
        s = sub.add_parser(name)
        s.add_argument('state')
        s.add_argument('--root', default='.')
        for e in extra:
            e(s)
        return s

    def staging(s):
        s.add_argument('--revision', type=int, required=True)
        s.add_argument('--site', choices=('absent', 'off', 'dispatch'), default='absent')
        s.add_argument('--bad-kro-chart', action='store_true')
        s.add_argument('--drop-site-volume', action='store_true')
        s.add_argument('--purpose', default='')

    def clusters(s):
        s.add_argument('--cluster', action='append', default=[], choices=CLUSTERS)
        s.add_argument('--timeout', type=int, default=900)
    add('tools')
    add('stage', staging)
    add('push', staging)
    add('publish', staging, clusters)
    add('bootstrap', lambda s: s.add_argument('--cluster', required=True, choices=CLUSTERS))
    add('wait', clusters, lambda s: s.add_argument('--except', dest='skip', action='append', default=[]),
        lambda s: s.add_argument('--observe', action='append', default=[], choices=CLUSTERS))
    add('record', clusters, lambda s: s.add_argument('--name', required=True))
    for name in ('verify', 'drift', 'restart', 'fail-closed', 'retention', 'graph-lifecycle'):
        add(name)
    add('ownership', lambda s: s.add_argument('--name', required=True))
    a = p.parse_args()
    return {'tools': tools, 'stage': stage, 'push': push, 'publish': publish, 'bootstrap': bootstrap, 'wait': wait_cmd, 'record': record,
            'verify': verify, 'drift': drift, 'restart': restart, 'fail-closed': fail_closed, 'retention': retention,
            'graph-lifecycle': graph_lifecycle, 'ownership': ownership_cmd}[a.cmd](a)


if __name__ == '__main__':
    sys.exit(main())
