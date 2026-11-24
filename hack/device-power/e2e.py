#!/usr/bin/env python3
"""End-to-end acceptance of staff power operations against emulated BMCs.

One disposable kind cluster in the task-owned Colima profile holds:

  * a Milo v0.32.5 project API (its own etcd) with the inventory, metal and
    DevicePowerOperation CRDs and the requester admission policy, reached
    through Milo's native project route with per-identity static tokens;
  * the site namespace with read-only BMC discovery, the power dispatcher,
    its admission policies and the BMC credential Secrets;
  * two lab BMCs: the unmodified DMTF Redfish Interface Emulator 1.2.2 with
    the Fleet lab adapter over TLS, each serving one System. Power is the
    adapter's simulated libvirt (fake_virsh.py); no guest boots.

Every operation is created by a staff identity through the Milo API, accepted
or refused by the deployed dispatcher, and run by a real worker Job against a
lab BMC. Each scenario records the result staff see and the number of
ComputerSystem.Reset requests in the BMC's own audit log.

Synthetic data only. Credentials, tokens and kubeconfigs stay in the private
state directory; only evidence/ is shareable. See docs/operations/device-power.md.
"""
import argparse
import base64
import datetime
import hashlib
import json
import os
import pathlib
import re
import secrets
import shutil
import socket
import subprocess
import sys
import time

ROOT = pathlib.Path(__file__).resolve().parents[2]
CONTEXT = 'colima-fleet-acceptance-tcg'
NODE = 'kindest/node:v1.35.0@sha256:452d707d4862f52530247495d180205e029056831160e22870e37e3f6c1ac31f'
PROJECT = 'provider'
INVENTORY_NS = 'inventory'
SITE_NS = 'fleet-power-site'
LAB_NS = 'fleet-lab-bmc'
MILO_NS = 'milo-provider'
MILO_NODE_PORT = 30441
STAFF = 'staff@example.com'
# Synthetic lab servers. Device A has no capacity; Device B backs capacity.
BMCS = {
    'a': {'ip': '10.96.0.91', 'device': 'device-a', 'uuid': '00000000-0000-4000-8000-0000000000a1'},
    'b': {'ip': '10.96.0.92', 'device': 'device-b', 'uuid': '00000000-0000-4000-8000-0000000000b2'},
}
OBSERVE_TIMEOUT = '45s'
REQUEST_TIMEOUT = '10s'

os.environ['DOCKER_CONTEXT'] = CONTEXT
os.environ['KIND_EXPERIMENTAL_PROVIDER'] = 'docker'


class Failed(Exception):
    pass


def now():
    return datetime.datetime.now(datetime.timezone.utc).strftime('%Y-%m-%dT%H:%M:%SZ')


def run(*args, input=None, check=True, timeout=600, quiet=False):
    r = subprocess.run([str(a) for a in args], input=input, text=True, capture_output=True, timeout=timeout, cwd=ROOT)
    if check and r.returncode:
        raise Failed('%s failed (%d): %s' % (' '.join(str(a) for a in args[:4]), r.returncode, (r.stderr or r.stdout).strip()[-800:]))
    return r


class Env:
    def __init__(self, state, prefix):
        self.state, self.prefix = state, prefix
        self.evidence = state / 'evidence'
        self.admin = state / 'kind-admin.yaml'
        self.image = prefix + '-fleet:e2e'
        self.bmc_image = prefix + '-bmc:e2e'
        self.records = []

    # The kind cluster, as cluster admin (fixtures and lab faults only).
    def kube(self, *args, input=None, check=True, timeout=120):
        return run('kubectl', '--kubeconfig', self.admin, '--request-timeout=30s', *args, input=input, check=check, timeout=timeout)

    # The Milo project, as one identity.
    def milo(self, who, *args, input=None, check=True):
        # Milo's OpenAPI document fails kubectl's client-side schema check;
        # the server still validates every write.
        if args and args[0] in ('apply', 'create', 'replace'):
            args = (args[0], '--validate=false') + args[1:]
        return run('kubectl', '--kubeconfig', self.state / 'milo' / (who + '-project.json'), '--request-timeout=30s', *args, input=input, check=check)

    def apply(self, obj, who=None, namespace=None):
        args = ['apply', '-f', '-'] + (['-n', namespace] if namespace else [])
        data = json.dumps(obj) if not isinstance(obj, str) else obj
        return self.milo(who, *args, input=data) if who else self.kube(*args, input=data)

    def write(self, name, value):
        path = self.evidence / name
        path.write_text(json.dumps(value, indent=2, sort_keys=True) + '\n')
        return path


def phase(name):
    def wrap(fn):
        fn.phase = name
        return fn
    return wrap


def wait(fn, seconds, what, interval=2):
    deadline = time.time() + seconds
    last = None
    while time.time() < deadline:
        try:
            last = fn()
        except Failed:
            last = None
        if last:
            return last
        time.sleep(interval)
    raise Failed('timed out waiting for ' + what)


def free_port():
    s = socket.socket()
    s.bind(('127.0.0.1', 0))
    port = s.getsockname()[1]
    s.close()
    return port


def kubeconfig(path, server, token, ca):
    path.write_text(json.dumps({'apiVersion': 'v1', 'kind': 'Config',
                                'clusters': [{'name': 'milo', 'cluster': {'server': server, 'certificate-authority-data': base64.b64encode(ca).decode()}}],
                                'users': [{'name': 'u', 'user': {'token': token}}],
                                'contexts': [{'name': 'c', 'context': {'cluster': 'milo', 'user': 'u'}}], 'current-context': 'c'}))
    path.chmod(0o600)


def pin_of(cert):
    der = run('sh', '-c', 'openssl x509 -in "$1" -pubkey -noout | openssl pkey -pubin -outform der | shasum -a 256', 'sh', cert).stdout.split()[0]
    return der


# --- phases ---------------------------------------------------------------

@phase('preflight')
def preflight(e):
    for tool in ('docker', 'kind', 'kubectl', 'kustomize', 'go', 'openssl', 'curl'):
        if not shutil.which(tool):
            raise Failed('missing tool ' + tool)
    run('docker', 'info', timeout=30)
    running = run('docker', 'ps', '--format', '{{.Names}}').stdout.split()
    busy = [n for n in running if n.startswith(('fleet-mp-', 'fleet-local-', 'fleet-dp-'))]
    if busy:
        raise Failed('another Fleet runtime is running in %s: %s' % (CONTEXT, ', '.join(busy)))
    print('preflight: tools present; %s reachable; no other Fleet runtime running' % CONTEXT)


@phase('unit-tests')
def unit_tests(e):
    env = dict(os.environ)
    assets = run('go', 'run', 'sigs.k8s.io/controller-runtime/tools/setup-envtest@v0.25.2', 'use', '1.37.0', '-p', 'path', timeout=900).stdout.strip()
    env['KUBEBUILDER_ASSETS'] = assets
    r = subprocess.run(['go', 'test', '-race', '-count=1', './internal/devicepower', './internal/redfish', './internal/bmcdiscovery', './internal/api/inventory/...'],
                       cwd=ROOT, env=env, text=True, capture_output=True, timeout=1200)
    (e.state / 'logs' / 'unit-tests.log').write_text(r.stdout + r.stderr)
    if r.returncode:
        raise Failed('go test failed; see logs/unit-tests.log')
    print('unit-tests: devicepower, redfish, bmcdiscovery and inventory API tests passed, including envtest')


@phase('images')
def images(e):
    bin_dir = e.state / 'bin'
    bin_dir.mkdir(exist_ok=True)
    arch = run('docker', 'info', '--format', '{{.Architecture}}').stdout.strip()
    goarch = {'aarch64': 'arm64', 'x86_64': 'amd64'}.get(arch, arch)
    env = dict(os.environ, CGO_ENABLED='0', GOOS='linux', GOARCH=goarch)
    for cmd in ('fleet-device-power', 'fleet-bmc-discovery'):
        r = subprocess.run(['go', 'build', '-trimpath', '-o', str(bin_dir / cmd), './cmd/' + cmd], cwd=ROOT, env=env, text=True, capture_output=True, timeout=900)
        if r.returncode:
            raise Failed('build %s: %s' % (cmd, r.stderr[-500:]))
    (bin_dir / 'Containerfile').write_text('FROM scratch\nCOPY --chmod=0755 fleet-device-power fleet-bmc-discovery /\nUSER 65532:65532\n')
    run('docker', 'build', '-q', '-t', e.image, '-f', bin_dir / 'Containerfile', bin_dir, timeout=600)
    upstream = e.state / 'redfish-upstream'
    provenance = run('bash', 'hack/redfish-lab/upstream.sh', upstream, timeout=300).stdout
    (e.evidence / 'redfish-upstream.json').write_text(provenance)
    run('docker', 'build', '-q', '--build-context', 'upstream=%s' % (upstream / 'src' / 'Redfish-Interface-Emulator-1.2.2'), '-t', e.bmc_image,
        '-f', 'hack/redfish-lab/Containerfile', 'hack/redfish-lab', timeout=1800)
    run('docker', 'run', '--rm', '--network', 'none', '--entrypoint', 'python3', e.bmc_image, '/opt/fleet-lab/test_adapter.py', timeout=300)
    ids = {name: run('docker', 'image', 'inspect', name, '--format', '{{.Id}}').stdout.strip() for name in (e.image, e.bmc_image)}
    e.write('images.json', {'fleet': ids[e.image], 'labBMC': ids[e.bmc_image], 'binaries': {
        b: hashlib.sha256((bin_dir / b).read_bytes()).hexdigest() for b in ('fleet-device-power', 'fleet-bmc-discovery')}})
    print('images: fleet binaries built from the checkout; lab BMC image built from the verified DMTF 1.2.2 archive; adapter self-test passed')


@phase('cluster')
def cluster(e):
    port = free_port()
    (e.state / 'milo-port').write_text(str(port))
    config = e.state / 'kind.yaml'
    config.write_text('kind: Cluster\napiVersion: kind.x-k8s.io/v1alpha4\nnodes:\n  - role: control-plane\n    extraPortMappings:\n'
                      '      - {containerPort: %d, hostPort: %d, listenAddress: 127.0.0.1}\n' % (MILO_NODE_PORT, port))
    run('kind', 'create', 'cluster', '--name', e.prefix, '--config', config, '--kubeconfig', e.admin, '--image', NODE, '--wait', '180s', timeout=600)
    e.admin.chmod(0o600)
    for image in (e.image, e.bmc_image):
        run('kind', 'load', 'docker-image', '--name', e.prefix, image, timeout=600)
    print('cluster: kind node %s; images loaded' % NODE.split('@')[0])


@phase('milo')
def milo(e):
    d = e.state / 'milo'
    d.mkdir(mode=0o700, exist_ok=True)
    identities = {'bootstrap': 'system:masters', STAFF: 'staff', 'viewer@example.com': 'staff', 'fleet-device-power-dispatcher': 'fleet', 'fleet-bmc-discovery': 'fleet'}
    tokens = {who: secrets.token_hex(32) for who in identities}
    (d / 'tokens.csv').write_text(''.join('%s,%s,%s,%s\n' % (tokens[w], w, w, g) for w, g in identities.items()))
    (d / 'tokens.json').write_text(json.dumps(tokens))
    run('openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-keyout', d / 'tls.key', '-out', d / 'tls.crt', '-days', '2', '-subj', '/CN=fleet-e2e-milo',
        '-addext', 'subjectAltName=DNS:milo-apiserver.%s.svc,IP:127.0.0.1' % MILO_NS)
    for f in ('tokens.csv', 'tokens.json', 'tls.key', 'tls.crt'):
        (d / f).chmod(0o600)
    e.apply(run('kustomize', 'build', 'config/multiplane/milo/provider').stdout)
    secret = e.kube('-n', MILO_NS, 'create', 'secret', 'generic', 'milo-apiserver-private', '--from-file=tokens.csv=%s' % (d / 'tokens.csv'),
                    '--from-file=tls.crt=%s' % (d / 'tls.crt'), '--from-file=tls.key=%s' % (d / 'tls.key'), '--dry-run=client', '-o', 'json').stdout
    e.apply(secret)
    e.kube('-n', MILO_NS, 'rollout', 'status', 'deployment/milo-apiserver', '--timeout=600s', timeout=660)
    ca = (d / 'tls.crt').read_bytes()
    root = 'https://127.0.0.1:%s' % (e.state / 'milo-port').read_text()
    route = '/apis/resourcemanager.miloapis.com/v1alpha1/projects/%s/control-plane' % PROJECT
    kubeconfig(d / 'bootstrap-root.json', root, tokens['bootstrap'], ca)
    wait(lambda: run('kubectl', '--kubeconfig', d / 'bootstrap-root.json', 'get', 'crd', 'projects.resourcemanager.miloapis.com', check=False).returncode == 0, 300, 'Milo API')
    run('kubectl', '--kubeconfig', d / 'bootstrap-root.json', 'wait', '--for=condition=Established', 'crd/projects.resourcemanager.miloapis.com', '--timeout=90s')
    run('kubectl', '--kubeconfig', d / 'bootstrap-root.json', 'apply', '--validate=false', '-f', '-', input=json.dumps(
        {'apiVersion': 'resourcemanager.miloapis.com/v1alpha1', 'kind': 'Project', 'metadata': {'name': PROJECT}, 'spec': {'ownerRef': {'kind': 'Organization', 'name': 'e2e-fixture'}}}))
    names = {'bootstrap': 'bootstrap', STAFF: 'staff', 'viewer@example.com': 'viewer', 'fleet-device-power-dispatcher': 'dispatcher', 'fleet-bmc-discovery': 'discovery'}
    for who, short in names.items():
        kubeconfig(d / (short + '-project.json'), root + route, tokens[who], ca)
    for who in ('fleet-device-power-dispatcher', 'fleet-bmc-discovery'):
        kubeconfig(d / (names[who] + '-incluster.json'), 'https://milo-apiserver.%s.svc:6443%s' % (MILO_NS, route), tokens[who], ca)
    # The project's APIs: inventory, capacity and power operations.
    def project_apply(path):
        wait(lambda: e.milo('bootstrap', 'apply', '-f', '-', input=run('kustomize', 'build', path).stdout, check=False).returncode == 0, 120, 'apply ' + path)
    e.milo('bootstrap', 'apply', '-f', 'config/base/crd/metal.datumapis.com.yaml')
    project_apply('config/components/inventory/crds')
    project_apply('config/components/device-power/crds')
    e.milo('bootstrap', 'wait', '--for=condition=Established', 'crd', '--all', '--timeout=120s')
    project_apply('config/components/device-power/admission')
    e.apply({'apiVersion': 'v1', 'kind': 'Namespace', 'metadata': {'name': INVENTORY_NS}}, who='bootstrap')
    # Kubernetes RBAC stands in for the Milo IAM roles of the same names
    # (config/components/device-power/roles, inventory discoverer). This is
    # the local static-token seam, as in the multi-plane harness.
    rules = {
        'staff': [{'apiGroups': ['fleet.datumapis.com'], 'resources': ['devicepoweroperations'], 'verbs': ['create', 'get', 'list', 'watch']},
                  {'apiGroups': ['fleet.datumapis.com'], 'resources': ['devices'], 'verbs': ['get', 'list', 'watch']}],
        'viewer': [{'apiGroups': ['fleet.datumapis.com'], 'resources': ['devices', 'devicepoweroperations'], 'verbs': ['get', 'list', 'watch']}],
        'dispatcher': [{'apiGroups': ['fleet.datumapis.com'], 'resources': ['devicepoweroperations', 'devices'], 'verbs': ['get', 'list', 'watch']},
                       {'apiGroups': ['fleet.datumapis.com'], 'resources': ['devicepoweroperations/status'], 'verbs': ['update', 'patch']},
                       {'apiGroups': ['metal.datumapis.com'], 'resources': ['machinecapacities'], 'verbs': ['get', 'list', 'watch']}],
        'discovery': [{'apiGroups': ['fleet.datumapis.com'], 'resources': ['devices'], 'verbs': ['get', 'list', 'watch']},
                      {'apiGroups': ['fleet.datumapis.com'], 'resources': ['devices/status'], 'verbs': ['update', 'patch']}],
    }
    users = {'staff': STAFF, 'viewer': 'viewer@example.com', 'dispatcher': 'fleet-device-power-dispatcher', 'discovery': 'fleet-bmc-discovery'}
    for role, rule in rules.items():
        for config in ('bootstrap-root.json',):
            run('kubectl', '--kubeconfig', d / config, 'apply', '--validate=false', '-f', '-', input=json.dumps({'apiVersion': 'v1', 'kind': 'List', 'items': [
                {'apiVersion': 'rbac.authorization.k8s.io/v1', 'kind': 'ClusterRole', 'metadata': {'name': 'e2e-' + role}, 'rules': rule},
                {'apiVersion': 'rbac.authorization.k8s.io/v1', 'kind': 'ClusterRoleBinding', 'metadata': {'name': 'e2e-' + role},
                 'roleRef': {'apiGroup': 'rbac.authorization.k8s.io', 'kind': 'ClusterRole', 'name': 'e2e-' + role},
                 'subjects': [{'apiGroup': 'rbac.authorization.k8s.io', 'kind': 'User', 'name': users[role]}]}]}))
    wait(lambda: e.milo('staff', 'auth', 'can-i', 'create', 'devicepoweroperations.fleet.datumapis.com', '-n', INVENTORY_NS, check=False).stdout.strip() == 'yes', 60, 'staff RBAC')
    print('milo: Milo v0.32.5 project API ready; inventory, metal and DevicePowerOperation CRDs and the requester policy applied in project %s' % PROJECT)


def bmc_manifests(e, key, spec):
    d = e.state / 'lab' / key
    d.mkdir(parents=True, exist_ok=True)
    run('openssl', 'req', '-x509', '-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:prime256v1', '-nodes', '-keyout', d / 'tls.key', '-out', d / 'tls.crt',
        '-days', '2', '-subj', '/CN=fleet-lab-bmc', '-addext', 'subjectAltName=IP:' + spec['ip'])
    password = secrets.token_hex(24)
    (d / 'password').write_text(password)
    for f in ('tls.key', 'password'):
        (d / f).chmod(0o600)
    spec['pin'] = pin_of(d / 'tls.crt')
    spec['password'] = password
    targets = [{'system': 'System-1', 'name': 'fleet-lab-' + key, 'domain': 'fleet-lab-' + key, 'uuid': spec['uuid'], 'mac': '52:54:00:77:02:0' + ('a' if key == 'a' else 'b'),
                'ip': '192.168.78.1' + ('0' if key == 'a' else '1'), 'disk': '/var/lib/fleet-bmc/disk.raw', 'console': '/var/lib/fleet-bmc/console.log'}]
    populate = json.loads((ROOT / 'hack/redfish-lab/populate-config.json').read_text())
    populate['POPULATE']['Chassis'][0]['Links']['ComputerSystems'][0]['Count'] = 1
    name = 'lab-bmc-' + key
    return [
        {'apiVersion': 'v1', 'kind': 'ConfigMap', 'metadata': {'name': name, 'namespace': LAB_NS}, 'data': {
            'emulator-config.json': (ROOT / 'hack/redfish-lab/emulator-config.json').read_text(), 'populate-config.json': json.dumps(populate),
            'targets.json': json.dumps(targets)}},
        {'apiVersion': 'v1', 'kind': 'Secret', 'metadata': {'name': name, 'namespace': LAB_NS}, 'type': 'Opaque', 'data': {
            'username': base64.b64encode(b'lab-operator').decode(), 'password': base64.b64encode(password.encode()).decode(),
            'tls.crt': base64.b64encode((d / 'tls.crt').read_bytes()).decode(), 'tls.key': base64.b64encode((d / 'tls.key').read_bytes()).decode()}},
        {'apiVersion': 'v1', 'kind': 'Service', 'metadata': {'name': name, 'namespace': LAB_NS}, 'spec': {
            'clusterIP': spec['ip'], 'selector': {'app': name}, 'ports': [{'name': 'https', 'port': 443, 'targetPort': 8443}]}},
        {'apiVersion': 'apps/v1', 'kind': 'Deployment', 'metadata': {'name': name, 'namespace': LAB_NS}, 'spec': {
            'replicas': 1, 'strategy': {'type': 'Recreate'}, 'selector': {'matchLabels': {'app': name}}, 'template': {
                'metadata': {'labels': {'app': name}}, 'spec': {'automountServiceAccountToken': False, 'enableServiceLinks': False, 'containers': [{
                    'name': 'bmc', 'image': e.bmc_image, 'imagePullPolicy': 'Never', 'args': ['serve'],
                    'env': [{'name': 'FLEET_BMC_CONFIG', 'value': '/etc/fleet-bmc'}, {'name': 'FLEET_BMC_STATE', 'value': '/var/lib/fleet-bmc'},
                            {'name': 'FLEET_BMC_CREDENTIALS', 'value': '/etc/fleet-bmc-credentials'}, {'name': 'FLEET_BMC_PORT', 'value': '8443'},
                            {'name': 'FLEET_BMC_TLS_CERT', 'value': '/etc/fleet-bmc-credentials/tls.crt'}, {'name': 'FLEET_BMC_TLS_KEY', 'value': '/etc/fleet-bmc-credentials/tls.key'},
                            {'name': 'FLEET_VIRSH', 'value': '/opt/fleet-lab/fake_virsh.py'}, {'name': 'FAKE_VIRSH_STATE', 'value': '/var/lib/fleet-bmc/virsh.json'},
                            {'name': 'FLEET_BOOT_IP', 'value': '10.96.0.80'}],
                    'ports': [{'containerPort': 8443}],
                    'readinessProbe': {'tcpSocket': {'port': 8443}, 'periodSeconds': 2},
                    'volumeMounts': [{'name': 'config', 'mountPath': '/etc/fleet-bmc', 'readOnly': True},
                                     {'name': 'credentials', 'mountPath': '/etc/fleet-bmc-credentials', 'readOnly': True},
                                     {'name': 'state', 'mountPath': '/var/lib/fleet-bmc'}]}],
                    'volumes': [{'name': 'config', 'configMap': {'name': name}}, {'name': 'credentials', 'secret': {'secretName': name}},
                                {'name': 'state', 'emptyDir': {}}]}}}},
    ]


@phase('site')
def site(e):
    e.apply({'apiVersion': 'v1', 'kind': 'Namespace', 'metadata': {'name': LAB_NS}})
    # The site namespace enforces the restricted Pod Security profile, which
    # every discovery, dispatcher and worker Pod must meet.
    e.apply({'apiVersion': 'v1', 'kind': 'Namespace', 'metadata': {'name': SITE_NS, 'labels': {
        'pod-security.kubernetes.io/enforce': 'restricted', 'pod-security.kubernetes.io/enforce-version': 'latest',
        'fleet.datumapis.com/device-power-site': 'true'}}})
    for key, spec in BMCS.items():
        e.apply({'apiVersion': 'v1', 'kind': 'List', 'items': bmc_manifests(e, key, spec)})
    for key in BMCS:
        # A lab BMC reads its TLS key at start; a resumed run regenerated it.
        e.kube('-n', LAB_NS, 'rollout', 'restart', 'deployment/lab-bmc-' + key)
        e.kube('-n', LAB_NS, 'rollout', 'status', 'deployment/lab-bmc-' + key, '--timeout=300s', timeout=330)
    lab = {k: {kk: v for kk, v in spec.items() if kk not in ('password',)} for k, spec in BMCS.items()}
    e.write('lab.json', lab)
    d = e.state / 'milo'
    for who, secret in (('dispatcher', 'fleet-device-power-milo-kubeconfig'), ('discovery', 'fleet-bmc-discovery-milo-kubeconfig')):
        e.apply(e.kube('-n', SITE_NS, 'create', 'secret', 'generic', secret, '--from-file=kubeconfig=%s' % (d / (who + '-incluster.json')), '--dry-run=client', '-o', 'json').stdout)
    # One credential Secret per Device, named bmc.<namespace>.<device>, with
    # the approved pin. The harness generated each lab key, which stands in
    # for the out-of-band verification.
    for key, spec in BMCS.items():
        e.apply({'apiVersion': 'v1', 'kind': 'Secret', 'metadata': {'name': 'bmc.%s.%s' % (INVENTORY_NS, spec['device']), 'namespace': SITE_NS,
                 'labels': {'fleet.datumapis.com/bmc-credential': 'true'},
                 'annotations': {'fleet.datumapis.com/device-project': PROJECT, 'fleet.datumapis.com/device-namespace': INVENTORY_NS, 'fleet.datumapis.com/device-name': spec['device']}},
                 'type': 'Opaque', 'stringData': {'username': 'lab-operator', 'password': spec['password'], 'tlsPublicKeySHA256': spec['pin']}})
    overlay = e.state / 'overlay'
    for name, base, config in (('discovery', 'config/base/bmc-discovery', {'project': PROJECT, 'miloKubeconfig': '/var/run/fleet/milo/kubeconfig',
                                'deviceNamespaces': [INVENTORY_NS], 'interval': '1h', 'retryBase': '10s', 'minContactInterval': '1m', 'requestTimeout': '10s',
                                'concurrency': 4, 'healthProbeAddress': ':8081'}),
                               ('dispatcher', 'config/base/device-power', {'project': PROJECT, 'miloKubeconfig': '/var/run/fleet/milo/kubeconfig',
                                'deviceNamespaces': [INVENTORY_NS], 'workerImage': e.image, 'discoveryMaxAge': '2h', 'observeTimeout': OBSERVE_TIMEOUT,
                                'requestTimeout': REQUEST_TIMEOUT, 'retention': '1h', 'healthProbeAddress': ':8081'})):
        o = overlay / name
        o.mkdir(parents=True, exist_ok=True)
        o = o.resolve()
        (o / 'config.yaml').write_text(json.dumps(config))
        rel = os.path.relpath(ROOT / base, o)
        patches = ''
        if name == 'dispatcher':
            (o / 'params.json').write_text(json.dumps([{'op': 'replace', 'path': '/data/workerImage', 'value': e.image}]))
            patches = 'patches:\n  - target: {kind: ConfigMap, name: fleet-device-power-worker-policy}\n    path: params.json\n'
        cm = 'fleet-device-power-config' if name == 'dispatcher' else 'fleet-bmc-discovery-config'
        (o / 'kustomization.yaml').write_text(
            'apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nnamespace: %s\nresources:\n  - %s\n'
            'images:\n  - {name: ghcr.io/datum-cloud/fleet, newName: %s, newTag: e2e}\n'
            'configMapGenerator:\n  - {name: %s, behavior: replace, files: [config.yaml]}\n%s' % (SITE_NS, rel, e.image.split(':')[0], cm, patches))
        rendered = run('kustomize', 'build', o).stdout
        (e.evidence / ('rendered-' + name + '.yaml')).write_text(rendered)
        e.apply(rendered)
    for deploy in ('fleet-bmc-discovery', 'fleet-device-power-dispatcher'):
        e.kube('-n', SITE_NS, 'patch', 'deployment', deploy, '--type=json', '-p', json.dumps([{'op': 'add', 'path': '/spec/template/spec/containers/0/imagePullPolicy', 'value': 'Never'}]))
    print('site: two TLS lab BMCs, credential Secrets with approved pins, BMC discovery and the dispatcher applied from config/base')


@phase('inventory')
def inventory(e):
    for key, spec in BMCS.items():
        e.apply({'apiVersion': 'fleet.datumapis.com/v1alpha1', 'kind': 'Device', 'metadata': {'name': spec['device'], 'namespace': INVENTORY_NS},
                 'spec': {'claimRef': {'name': 'claim-' + key}, 'identity': {'model': 'libvirt/QEMU lab target', 'systemUUID': spec['uuid']},
                          'bmc': {'endpoint': 'https://%s:443' % spec['ip']}}}, who='bootstrap')
    # Device B backs customer capacity.
    e.apply({'apiVersion': 'metal.datumapis.com/v1alpha1', 'kind': 'MachineCapacity', 'metadata': {'name': 'capacity-b', 'namespace': INVENTORY_NS},
             'spec': {'deviceRef': {'name': BMCS['b']['device']}, 'allocationPolicy': 'Disabled', 'cleaningPolicyRef': {'name': ''}, 'reclaimPolicy': 'Retain'}}, who='bootstrap')
    for deploy in ('fleet-bmc-discovery', 'fleet-device-power-dispatcher'):
        e.kube('-n', SITE_NS, 'rollout', 'status', 'deployment/' + deploy, '--timeout=300s', timeout=330)

    def discovered():
        out = {}
        for spec in BMCS.values():
            d = json.loads(e.milo('bootstrap', 'get', 'device', spec['device'], '-n', INVENTORY_NS, '-o', 'json').stdout)
            conds = {c['type']: c['status'] for c in d.get('status', {}).get('conditions', [])}
            systems = (d.get('status', {}).get('discovery') or {}).get('inventory', {}).get('systems', [])
            if not (conds.get('Discovered') == conds.get('IdentityMatched') == conds.get('PublicKeyUnique') == 'True') or not systems:
                return None
            out[spec['device']] = {'conditions': conds, 'systems': [{k: s.get(k) for k in ('id', 'uuid', 'powerState', 'resetTypes')} for s in systems],
                                   'publicKeySHA256': d['status']['discovery']['tls']['publicKeySHA256']}
        return out
    seen = wait(discovered, 240, 'discovery of both Devices')
    for key, spec in BMCS.items():
        if seen[spec['device']]['publicKeySHA256'] != spec['pin'] or seen[spec['device']]['systems'][0]['resetTypes'] != ['On', 'ForceOff', 'ForceRestart']:
            raise Failed('discovery recorded unexpected TLS key or reset types: %s' % seen)
    posts = {k: [x for x in audit(e, k) if x['method'] != 'GET'] for k in BMCS}
    if any(posts.values()):
        raise Failed('discovery issued a non-GET request: %s' % posts)
    e.write('discovery.json', {'observedAt': now(), 'devices': seen, 'nonGetRequests': 0})
    print('inventory: discovery matched both Devices over pinned TLS and recorded resetTypes [On, ForceOff, ForceRestart] with GETs only')


def lab_pod(e, key):
    pods = json.loads(e.kube('-n', LAB_NS, 'get', 'pod', '-l', 'app=lab-bmc-' + key, '-o', 'json').stdout)['items']
    live = [p['metadata']['name'] for p in pods if not p['metadata'].get('deletionTimestamp') and p['status'].get('phase') == 'Running']
    if len(live) != 1:
        raise Failed('expected one running lab BMC %s, found %d' % (key, len(live)))
    return live[0]


def audit(e, key):
    pod = lab_pod(e, key)
    text = e.kube('-n', LAB_NS, 'exec', pod, '--', 'cat', '/var/lib/fleet-bmc/audit.log', check=False).stdout
    return [json.loads(l) for l in text.splitlines() if l.strip()]


def resets(e, key):
    return [x for x in audit(e, key) if x['method'] == 'POST' and x['path'].endswith('/Actions/ComputerSystem.Reset')]


def fault(e, key, *args):
    pod = lab_pod(e, key)
    return e.kube('-n', LAB_NS, 'exec', pod, '--', 'python3', '/opt/fleet-lab/fleet_lab_adapter.py', 'fault', *args).stdout.strip()


def power_state(e, key):
    pod = lab_pod(e, key)
    r = e.kube('-n', LAB_NS, 'exec', pod, '--', 'cat', '/var/lib/fleet-bmc/virsh.json', check=False)
    if r.returncode:
        return 'Off'  # no lab domain defined yet: never powered
    state = json.loads(r.stdout)
    return 'On' if state['domains'].get('fleet-lab-' + key, {}).get('running') else 'Off'


SEQ = [0]
# Distinguishes operations of a resumed run from earlier ones.
RUN_ID = secrets.token_hex(2)


def operation(e, device, action, uuid=None, requester=STAFF, who='staff'):
    SEQ[0] += 1
    name = 'op-%s-%02d-%s' % (RUN_ID, SEQ[0], action.lower())
    key = next(k for k, s in BMCS.items() if s['device'] == device)
    obj = {'apiVersion': 'fleet.datumapis.com/v1alpha1', 'kind': 'DevicePowerOperation', 'metadata': {'name': name, 'namespace': INVENTORY_NS},
           'spec': {'deviceRef': {'name': device}, 'action': action, 'expectedSystemUUID': uuid or BMCS[key]['uuid'], 'requester': requester,
                    'reason': 'synthetic end-to-end scenario'}}
    r = e.milo(who, 'create', '-f', '-', input=json.dumps(obj), check=False)
    return name, r


def get_op(e, name):
    return json.loads(e.milo('staff', 'get', 'devicepoweroperation', name, '-n', INVENTORY_NS, '-o', 'json').stdout)


def finished(e, name, seconds=240):
    def done():
        op = get_op(e, name)
        return op if op.get('status', {}).get('result') else None
    return wait(done, seconds, 'operation %s to finish' % name)


def jobs_for(e, uid):
    out = json.loads(e.kube('-n', SITE_NS, 'get', 'jobs', '-l', 'fleet.datumapis.com/power-operation-uid=' + uid, '-o', 'json').stdout)
    return out['items']


def summary(op):
    s = op.get('status', {})
    reason = next((c['reason'] for c in s.get('conditions', []) if c['type'] == 'Finished'), None)
    message = next((c['message'] for c in s.get('conditions', []) if c['type'] == 'Finished'), None)
    accepted = next((c['status'] for c in s.get('conditions', []) if c['type'] == 'Accepted'), None)
    return {'operation': op['metadata']['name'], 'action': op['spec']['action'], 'device': op['spec']['deviceRef']['name'], 'requester': op['spec']['requester'],
            'accepted': accepted, 'result': s.get('result'), 'reason': reason, 'message': message, 'requestSentAt': s.get('requestSentAt'),
            'power': s.get('power'), 'finishedAt': s.get('finishedAt')}


def scenario(e, title, name, key, before_resets, want_result, want_reason, want_resets, extra=None, want_jobs=None):
    op = get_op(e, name)
    # The lab audits a request when its response is written, which a
    # withheld response delays by up to 25 seconds. Wait out that window
    # before counting, so a late second request would be counted too.
    if op.get('status', {}).get('requestSentAt'):
        time.sleep(30)
    after = resets(e, key)
    sent = after[len(before_resets):]
    record = summary(op)
    record.update({'scenario': title, 'resetRequestsAtBMC': len(sent), 'resetRequests': [{k: x.get(k) for k in ('resetType', 'status', 'effect', 'fault')} for x in sent],
                   'workerJobs': len(jobs_for(e, op['metadata']['uid']))})
    if extra:
        record.update(extra)
    e.records.append(record)
    ok = record['result'] == want_result and record['reason'] == want_reason and len(sent) == want_resets and (want_jobs is None or record['workerJobs'] == want_jobs)
    status = 'passed' if ok else 'FAILED'
    print('  %-58s %-9s %-26s resets=%d jobs=%d  %s' % (title, record['result'], record['reason'], len(sent), record['workerJobs'], status))
    if not ok:
        raise Failed('%s: got %s/%s with %d resets, want %s/%s with %d' % (title, record['result'], record['reason'], len(sent), want_result, want_reason, want_resets))
    return op


@phase('admission')
def admission(e):
    results = {}
    # Device B backs capacity, so a probe that is admitted is still refused
    # by the dispatcher and reaches no BMC.
    _, r = operation(e, 'device-b', 'On', requester='someone-else@example.com')
    # Known gap: Milo v0.32.5 does not evaluate ValidatingAdmissionPolicies
    # stored in a project, so the requester policy is applied but not
    # enforced. Recorded as observed; see docs/operations/device-power.md.
    results['forgedRequester'] = 'denied' if r.returncode and 'spec.requester must be' in r.stderr else r.stderr.strip() or 'admitted'
    _, r = operation(e, 'device-b', 'On', requester='viewer@example.com', who='viewer')
    results['withoutOperatorRole'] = 'forbidden' if r.returncode and 'forbidden' in r.stderr.lower() else r.stderr.strip() or 'admitted'
    name, r = operation(e, 'device-b', 'On', uuid='00000000-0000-4000-8000-0000000000ff')
    if r.returncode:
        raise Failed('staff could not create an operation: ' + r.stderr)
    op = finished(e, name)
    op['spec']['action'] = 'ForceOff'
    r = e.milo('bootstrap', 'replace', '-f', '-', input=json.dumps(op), check=False)
    results['specChange'] = 'rejected' if r.returncode and 'spec is immutable' in r.stderr else r.stderr.strip() or 'accepted'
    status = {'apiVersion': op['apiVersion'], 'kind': op['kind'], 'metadata': {'name': name, 'namespace': INVENTORY_NS, 'resourceVersion': op['metadata']['resourceVersion']},
              'spec': get_op(e, name)['spec'], 'status': dict(op['status'], result='Succeeded')}
    r = e.milo('staff', 'replace', '--subresource=status', '-f', '-', input=json.dumps(status), check=False)
    results['staffStatusWrite'] = 'forbidden' if r.returncode and 'forbidden' in r.stderr.lower() else r.stderr.strip() or 'allowed'
    r = e.milo('staff', 'delete', 'devicepoweroperation', name, '-n', INVENTORY_NS, check=False)
    results['staffDelete'] = 'forbidden' if r.returncode and 'forbidden' in r.stderr.lower() else r.stderr.strip() or 'allowed'
    results['requesterPolicyStoredInProject'] = e.milo('bootstrap', 'get', 'validatingadmissionpolicy', 'fleet-device-power-requester', check=False).returncode == 0
    e.write('admission.json', results)
    want = {'forgedRequester': 'admitted', 'requesterPolicyStoredInProject': True, 'withoutOperatorRole': 'forbidden', 'specChange': 'rejected', 'staffStatusWrite': 'forbidden', 'staffDelete': 'forbidden'}
    if results != want:
        raise Failed('admission: %s' % results)
    print('admission: KNOWN GAP forged requester admitted (Milo v0.32.5 does not enforce project-stored admission policies); viewer forbidden; spec change rejected; staff status write and delete forbidden')


@phase('power')
def power(e):
    print('power: scenario                                                 result    reason                     resets jobs')
    a, b = 'a', 'b'
    if power_state(e, a) != 'Off':
        raise Failed('device A did not start Off')

    # 1. On, with the dispatcher restarted while the operation is new.
    before = resets(e, a)
    name, _ = operation(e, 'device-a', 'On')
    e.kube('-n', SITE_NS, 'delete', 'pod', '-l', 'app.kubernetes.io/name=fleet-device-power-dispatcher', '--wait=false')
    finished(e, name)
    scenario(e, 'On (dispatcher restarted after the request)', name, a, before, 'Succeeded', 'RequestedStateObserved', 1, want_jobs=1)

    # 2. ForceRestart, proven by the BMC's LastResetTime.
    before = resets(e, a)
    name, _ = operation(e, 'device-a', 'ForceRestart')
    finished(e, name)
    scenario(e, 'ForceRestart', name, a, before, 'Succeeded', 'ResetObserved', 1)

    # 3. ForceOff.
    before = resets(e, a)
    name, _ = operation(e, 'device-a', 'ForceOff')
    finished(e, name)
    scenario(e, 'ForceOff', name, a, before, 'Succeeded', 'RequestedStateObserved', 1)
    if power_state(e, a) != 'Off':
        raise Failed('device A is not Off after ForceOff')

    # 4. Refusals at acceptance: nothing reaches the BMC and no worker starts.
    before_a, before_b = resets(e, a), resets(e, b)
    name, _ = operation(e, 'device-a', 'PowerCycle')
    finished(e, name)
    scenario(e, 'PowerCycle, not offered by the BMC', name, a, before_a, 'Failed', 'ResetTypeNotOffered', 0, want_jobs=0)
    name, _ = operation(e, 'device-a', 'On', uuid=BMCS['b']['uuid'])
    finished(e, name)
    scenario(e, 'On with the wrong expected system UUID', name, a, before_a, 'Failed', 'SystemUUIDMismatch', 0, want_jobs=0)
    name, _ = operation(e, 'device-b', 'ForceRestart')
    finished(e, name)
    scenario(e, 'ForceRestart of a capacity-backed Device', name, b, before_b, 'Failed', 'CapacityBacked', 0, want_jobs=0)

    # 5. A replaced server behind the same BMC: the worker's identity check
    #    refuses before any command.
    fault(e, a, 'swap-identity', 'System-1', '00000000-0000-4000-8000-0000000000cc')
    before = resets(e, a)
    name, _ = operation(e, 'device-a', 'On')
    finished(e, name)
    scenario(e, 'On after the BMC reports another server', name, a, before, 'Failed', 'SystemUUIDMismatch', 0, want_jobs=1)
    fault(e, a, 'clear')

    # 6. Lost response, effect applied: the requested state proves it.
    fault(e, a, 'drop-response', 'System-1', 'On', '25')
    before = resets(e, a)
    name, _ = operation(e, 'device-a', 'On')
    finished(e, name)
    scenario(e, 'On, response lost after the effect', name, a, before, 'Succeeded', 'RequestedStateObserved', 1)

    # 7. Lost BMC response with no effect: Unknown, never resent. A cloned
    #    worker for the same operation starts mid-observation and stops at the
    #    claim without contacting the BMC.
    fault(e, a, 'drop-request', 'System-1', 'ForceOff', '25')
    before = resets(e, a)
    name, _ = operation(e, 'device-a', 'ForceOff')
    uid = get_op(e, name)['metadata']['uid']
    wait(lambda: record_stage(e, uid) in ('Sending', 'Sent'), 60, 'the worker to send')
    clone = clone_worker(e, uid)
    op = finished(e, name)
    logs = e.kube('-n', SITE_NS, 'logs', 'job/' + clone, check=False).stdout
    scenario(e, 'ForceOff, request lost (no effect); cloned worker', name, a, before, 'Unknown', 'OutcomeNotObserved', 1,
             extra={'clonedWorker': {'job': clone, 'exitedWithoutClaim': 'already claimed' in logs}})
    if 'already claimed' not in logs:
        raise Failed('the cloned worker did not stop at the claim: ' + logs[-400:])

    # 8. Two requests at once for one Device, and a worker lost after its
    #    command: the second request is refused; the first ends Unknown with
    #    one command and no retry.
    fault(e, a, 'drop-request', 'System-1', 'ForceOff', '25')
    before = resets(e, a)
    first, _ = operation(e, 'device-a', 'ForceOff')
    uid = get_op(e, first)['metadata']['uid']
    wait(lambda: record_stage(e, uid) in ('Sending', 'Sent'), 60, 'the first worker to send')
    second, _ = operation(e, 'device-a', 'On')
    finished(e, second)
    scenario(e, 'On while another operation on the Device runs', second, a, resets(e, a), 'Failed', 'OperationInProgress', 0, want_jobs=0)
    e.kube('-n', SITE_NS, 'delete', 'pod', '-l', 'fleet.datumapis.com/power-operation-uid=' + uid, '--grace-period=0', '--force')
    finished(e, first)
    scenario(e, 'ForceOff, worker deleted after its command', first, a, before, 'Unknown', 'WorkerLost', 1, want_jobs=1)
    pods = json.loads(e.kube('-n', SITE_NS, 'get', 'pods', '-l', 'fleet.datumapis.com/power-operation-uid=' + uid, '-o', 'json').stdout)['items']
    if pods:
        raise Failed('a replacement worker Pod was started: %s' % [p['metadata']['name'] for p in pods])
    fault(e, a, 'clear')

    # 9. Duplicate dispatch: a second dispatcher Deployment runs against the
    #    same project and site. One worker, one command.
    deploy = json.loads(e.kube('-n', SITE_NS, 'get', 'deployment', 'fleet-device-power-dispatcher', '-o', 'json').stdout)
    for k in ('resourceVersion', 'uid', 'creationTimestamp', 'generation', 'managedFields', 'annotations'):
        deploy['metadata'].pop(k, None)
    deploy.pop('status', None)
    deploy['metadata']['name'] = 'fleet-device-power-dispatcher-duplicate'
    labels = {'app.kubernetes.io/name': 'fleet-device-power-dispatcher-duplicate'}
    deploy['metadata']['labels'] = labels
    deploy['spec']['selector'] = {'matchLabels': labels}
    deploy['spec']['template']['metadata']['labels'] = labels
    e.apply(deploy)
    e.kube('-n', SITE_NS, 'rollout', 'status', 'deployment/fleet-device-power-dispatcher-duplicate', '--timeout=180s', timeout=200)
    # Device A is On after scenario 8's lost ForceOff had no effect.
    state = power_state(e, a)
    action = 'ForceRestart' if state == 'On' else 'On'
    before = resets(e, a)
    name, _ = operation(e, 'device-a', action)
    finished(e, name)
    scenario(e, '%s with two dispatchers running' % action, name, a, before, 'Succeeded', 'ResetObserved' if action == 'ForceRestart' else 'RequestedStateObserved', 1, want_jobs=1)
    e.kube('-n', SITE_NS, 'delete', 'deployment', 'fleet-device-power-dispatcher-duplicate')

    # 10. A restart on a BMC that does not report a reset time cannot be
    #     proven: Unknown, one command.
    fault(e, a, 'hide-reset-time')
    before = resets(e, a)
    name, _ = operation(e, 'device-a', 'ForceRestart')
    finished(e, name)
    scenario(e, 'ForceRestart on a BMC without LastResetTime', name, a, before, 'Unknown', 'OutcomeNotObserved', 1)
    fault(e, a, 'clear')

    if resets(e, b):
        raise Failed('the capacity-backed Device received a command')
    e.write('scenarios.json', {'observedAt': now(), 'observeTimeout': OBSERVE_TIMEOUT, 'requestTimeout': REQUEST_TIMEOUT, 'scenarios': e.records})
    print('power: all scenarios matched; the capacity-backed Device received no command')


def record_stage(e, uid):
    r = e.kube('-n', SITE_NS, 'get', 'configmap', 'pwr-' + uid, '-o', 'jsonpath={.data.progress}', check=False)
    try:
        return json.loads(r.stdout).get('stage')
    except ValueError:
        return None


def clone_worker(e, uid):
    """As cluster admin (outside the dispatcher's policy), start a second Job
    with the first worker's exact Pod template."""
    job = wait(lambda: (jobs_for(e, uid) or [None])[0], 60, 'the worker Job')
    template = job['spec']['template']
    template['metadata'].pop('labels', None)
    template['metadata']['labels'] = {'fleet.datumapis.com/e2e-clone': uid}
    clone = {'apiVersion': 'batch/v1', 'kind': 'Job', 'metadata': {'name': 'clone-' + uid[:8], 'namespace': SITE_NS},
             'spec': {'backoffLimit': 0, 'template': template}}
    e.apply(clone)
    e.kube('-n', SITE_NS, 'wait', '--for=condition=Complete', 'job/clone-' + uid[:8], '--timeout=120s', timeout=150)
    return 'clone-' + uid[:8]


@phase('security')
def security(e):
    """Facts about who can do what, as each identity."""
    can = lambda who, *a: e.kube('auth', 'can-i', *a, '-n', SITE_NS, '--as', who, check=False).stdout.strip()
    dispatcher = 'system:serviceaccount:%s:fleet-device-power-dispatcher' % SITE_NS
    facts = {
        'dispatcherCanGetSecrets': can(dispatcher, 'get', 'secrets'),
        'dispatcherCanCreatePods': can(dispatcher, 'create', 'pods'),
        'dispatcherCanCreateJobs': can(dispatcher, 'create', 'jobs'),
        'dispatcherCanDeleteJobs': can(dispatcher, 'delete', 'jobs'),
    }
    jobs = json.loads(e.kube('-n', SITE_NS, 'get', 'jobs', '-l', 'app.kubernetes.io/part-of=fleet-device-power', '-o', 'json').stdout)['items']
    shapes = []
    for job in jobs:
        spec = job['spec']['template']['spec']
        sa = spec['serviceAccountName']
        shapes.append({'job': job['metadata']['name'], 'image': spec['containers'][0]['image'], 'secretVolumes': [v['secret']['secretName'] for v in spec['volumes'] if 'secret' in v],
                       'backoffLimit': job['spec']['backoffLimit'], 'workerCanGetItsCredential': can('system:serviceaccount:%s:%s' % (SITE_NS, sa), 'get', 'secret/' + spec['volumes'][0]['secret']['secretName']),
                       'workerCanListSecrets': can('system:serviceaccount:%s:%s' % (SITE_NS, sa), 'list', 'secrets'),
                       'workerCanUpdateItsRecord': can('system:serviceaccount:%s:%s' % (SITE_NS, sa), 'update', 'configmap/' + job['metadata']['name'])})
    facts['workers'] = shapes
    # The worker holds no Milo credential: its Pod mounts only the Device's
    # credential Secret and its own ServiceAccount token.
    dispatcher_rbac = e.milo('dispatcher', 'auth', 'can-i', '--list', '-n', INVENTORY_NS, check=False).stdout
    facts['dispatcherMiloCanCreateOperations'] = e.milo('dispatcher', 'auth', 'can-i', 'create', 'devicepoweroperations.fleet.datumapis.com', '-n', INVENTORY_NS, check=False).stdout.strip()
    facts['dispatcherMiloCanPatchDevices'] = e.milo('dispatcher', 'auth', 'can-i', 'patch', 'devices.fleet.datumapis.com', '-n', INVENTORY_NS, check=False).stdout.strip()
    e.write('security.json', facts)
    bad = [k for k, v in facts.items() if isinstance(v, str) and v == 'yes' and k != 'dispatcherCanCreateJobs']
    bad += ['%s:%s' % (w['job'], k) for w in shapes for k in ('workerCanGetItsCredential', 'workerCanListSecrets') if w[k] == 'yes']
    bad += ['%s:record' % w['job'] for w in shapes if w['workerCanUpdateItsRecord'] != 'yes']
    if facts['dispatcherCanCreateJobs'] != 'yes' or bad or len({w['image'] for w in shapes}) != 1:
        raise Failed('security facts: %s' % bad)
    del dispatcher_rbac
    print('security: the dispatcher cannot read Secrets or create Pods; each worker reads no Secret through the API and updates only its own record')


@phase('evidence-scan')
def evidence_scan(e):
    tokens = json.loads((e.state / 'milo' / 'tokens.json').read_text()).values()
    needles = list(tokens) + [s['password'] for s in BMCS.values()] + [(e.state / 'milo' / 'tls.key').read_text().splitlines()[1]]
    for key in BMCS:
        needles.append((e.state / 'lab' / key / 'tls.key').read_text().splitlines()[1])
    for path in e.evidence.iterdir():
        text = path.read_text()
        for n in needles:
            if n and n in text:
                raise Failed('secret material in evidence/%s' % path.name)
    print('evidence-scan: %d evidence files contain no token, password or private key' % len(list(e.evidence.iterdir())))


PHASES = [preflight, unit_tests, images, cluster, milo, site, inventory, admission, power, security, evidence_scan]


def teardown(e, keep):
    if keep:
        print('teardown: --keep; cluster %s left running (delete with: kind delete cluster --name %s)' % (e.prefix, e.prefix))
        return
    run('kind', 'delete', 'cluster', '--name', e.prefix, check=False, timeout=300)
    for image in (e.image, e.bmc_image):
        run('docker', 'image', 'rm', image, check=False)
    for p in ('milo', 'lab', 'kind-admin.yaml', 'overlay'):
        path = e.state / p
        if path.is_dir():
            shutil.rmtree(path, ignore_errors=True)
        elif path.exists():
            path.unlink()
    print('teardown: cluster and image tags removed; tokens, passwords, keys and kubeconfigs deleted')


def main():
    p = argparse.ArgumentParser()
    p.add_argument('--keep', action='store_true', help='leave the cluster running after the run')
    p.add_argument('--skip-unit-tests', action='store_true')
    p.add_argument('--resume', metavar='STATE', help='diagnosis only: continue a kept run from --from-phase')
    p.add_argument('--from-phase', default='preflight')
    a = p.parse_args()
    if a.resume:
        state = pathlib.Path(a.resume).resolve()
        prefix = (state / 'prefix').read_text().strip()
    else:
        state = pathlib.Path(run('mktemp', '-d', '/tmp/fleet-device-power.XXXXXX').stdout.strip()).resolve()
        prefix = 'fleet-dp-' + secrets.token_hex(4)
    state.chmod(0o700)
    (state / 'evidence').mkdir(exist_ok=True)
    (state / 'logs').mkdir(exist_ok=True)
    (state / 'prefix').write_text(prefix)
    e = Env(state, prefix)
    names = [fn.phase for fn in PHASES]
    if a.from_phase not in names:
        raise SystemExit('unknown phase ' + a.from_phase)
    if a.resume:
        # Rebuild in-memory lab facts the site phase records.
        for key, spec in BMCS.items():
            d = state / 'lab' / key
            if (d / 'tls.crt').exists():
                spec['pin'], spec['password'] = pin_of(d / 'tls.crt'), (d / 'password').read_text()
    print('state %s prefix %s' % (state, prefix), flush=True)
    phases, status, failed = [], 'passed', None
    try:
        for fn in PHASES[names.index(a.from_phase):]:
            if fn is unit_tests and a.skip_unit_tests:
                continue
            start = time.time()
            try:
                fn(e)
            except (Failed, subprocess.TimeoutExpired) as err:
                phases.append({'phase': fn.phase, 'status': 'failed', 'seconds': round(time.time() - start)})
                status, failed = 'failed', fn.phase
                print('%s: FAILED: %s' % (fn.phase, err), file=sys.stderr)
                break
            phases.append({'phase': fn.phase, 'status': 'passed', 'seconds': round(time.time() - start)})
            sys.stdout.flush()
    finally:
        if status == 'failed' and e.admin.exists():
            for cmd in (['-n', SITE_NS, 'get', 'all,configmaps,leases', '-o', 'wide'], ['-n', SITE_NS, 'logs', 'deployment/fleet-device-power-dispatcher', '--tail=200']):
                r = e.kube(*cmd, check=False)
                (state / 'logs' / ('failure-' + cmd[2] + '.log')).write_text(r.stdout + r.stderr)
        e.write('result.json', {'status': status, 'failedPhase': failed, 'phases': phases, 'finishedAt': now(), 'prefix': prefix,
                                'physicalHardware': False, 'vendorBMC': False, 'bmcTLS': True, 'powerPlane': 'lab adapter fake_virsh (simulated)'})
        teardown(e, a.keep or status == 'failed')
    print('result: %s; evidence in %s' % (status, e.evidence))
    sys.exit(0 if status == 'passed' else 1)


if __name__ == '__main__':
    main()
