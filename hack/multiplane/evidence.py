#!/usr/bin/env python3
"""Capture private diagnostics and emit an explicit allowlist-only summary.

Private copies stay in <state>/provider-private. Only evidence/ is shareable,
and run.sh scans it against every retained credential before reporting.
"""
import datetime
import hashlib
import json
import os
import pathlib
import re
import subprocess
import sys
import tarfile

s = pathlib.Path(sys.argv[1])
prefix = (s / 'prefix').read_text().strip()
ev = s / 'evidence'
os.environ['DOCKER_CONTEXT'] = os.environ.get('FLEET_MULTIPLANE_DOCKER_CONTEXT', 'colima-fleet-acceptance-tcg')
ACCEL = os.environ.get('FLEET_MULTIPLANE_ACCEL', 'tcg')
RUNTIME = os.environ.get('FLEET_MULTIPLANE_RUNTIME', 'colima')
REDFISH = (s / 'site-provider').exists() and (s / 'site-provider').read_text().strip() == 'redfish'


def out(*args):
    return subprocess.check_output(args, text=True, timeout=60)


def fail(message):
    raise SystemExit(message + '; retain diagnostics and Unknown custody')


def kubectl(*args):
    return out('kubectl', '--kubeconfig', str(s / 'cell-admin.yaml'), '--request-timeout=30s', *args)


def kget(*args):
    return json.loads(kubectl(*args, '-o', 'json'))


def runtime(*command):
    return kubectl('-n', 'fleet-site', 'exec', 'deploy/fleet-site-runtime', '-c', 'libvirt', '--', *command)


def copy_out(source, destination):
    """Stream a runtime Pod directory into private state through the cell API."""
    destination.mkdir(mode=0o700, parents=True, exist_ok=True)
    p = subprocess.Popen(['kubectl', '--kubeconfig', str(s / 'cell-admin.yaml'), '-n', 'fleet-site', 'exec', 'deploy/fleet-site-runtime', '-c', 'libvirt',
                          '--', 'tar', '-C', source, '-cf', '-', '.'], stdout=subprocess.PIPE)
    with tarfile.open(fileobj=p.stdout, mode='r|') as archive:
        archive.extractall(destination, filter='data')
    if p.wait(timeout=120):
        fail('could not copy ' + source + ' from the runtime Pod')


memory_events = dict(line.split() for line in runtime('cat', '/sys/fs/cgroup/memory.events').splitlines())
node_events = {node: dict(line.split() for line in out('docker', 'exec', prefix + '-' + node + '-control-plane', 'cat', '/sys/fs/cgroup/memory.events').splitlines())
               for node in ('cell', 'hub', 'platform')}
node_memory_events = node_events['cell']
if int(memory_events.get('oom_kill', '0')) or any(int(e.get('oom_kill', '0')) for e in node_events.values()):
    fail('runtime Pod or kind node OOM kill recorded; inspect memory.events')
for config, namespace in (('cell-admin.yaml', 'fleet-site'), ('cell-admin.yaml', 'kro-system'), ('cell-admin.yaml', 'flux-system'), ('host.yaml', 'flux-system'),
                          ('host.yaml', 'karmada-system'), ('platform-admin.yaml', 'flux-system'), ('platform-admin.yaml', 'milo-provider'),
                          ('platform-admin.yaml', 'milo-consumer-a'), ('platform-admin.yaml', 'milo-consumer-b')):
    for pod in json.loads(out('kubectl', '--kubeconfig', str(s / config), '--request-timeout=30s', '-n', namespace, 'get', 'pods', '-o', 'json'))['items']:
        for c in pod['status'].get('containerStatuses', []):
            if any(state.get('reason') == 'OOMKilled' for state in (c.get('lastState', {}).get('terminated') or {}, c.get('state', {}).get('terminated') or {})):
                fail('OOMKilled container in ' + namespace + '/' + pod['metadata']['name'])
routing = json.loads((s / 'routing.json').read_text())
if len(routing) != 2 or any(x['namespace'] != 'default' or x['watch'] != 'Added exact UID' for x in routing) or routing[0]['namespaceUID'] == routing[1]['namespaceUID'] or routing[0]['projectUID'] == routing[1]['projectUID']:
    fail('missing independent native project create/watch evidence')

phases = (s / 'phases.log').read_text()
markers = ('Verified forbidden observer/project/hub writes', 'allocate: verified', 'contention: consumer-a Bound', 'delivery: Karmada delivered exactly one',
           'kro created the worker Job and per-operation RBAC', 'worker-rbac: per-operation ServiceAccount', 'interrupt-pod: worker Pod force-deleted',
           'interrupt-job: running worker Job deleted mid-install', 'resume: replacement worker completed the exact interrupted operation',
           'worker Job is Complete while hub/consumer disconnected; public Ready stayed False', 'install: verified', 'lifecycle: runtime, boot and observer Pods',
           'delivery-faults: duplicate, wrong-cell and stale-epoch', 'boot-denials: PASS', 'release: verified', 'release-status-a: consumer-a Machine Released', 'Installation Released, Ready False', 'reuse: verified', 'reuse-status: consumer-b Bound', 'release-b: verified', 'release-status-b: consumer-b Machine Released',
           'wrong-location: denied before allocation', 'Flux restored the binding', 'flux-verify: 3 clusters Ready', 'cell Flux applied the dispatch revision',
           'Flux restored consumer-a', 'Flux on every cluster Ready at the current revision', 'flux-drift: helm-controller restored kro',
           'recreated the shared boot Service at its fixed ClusterIP and restored the observer', 'flux-graph-lifecycle: deleting the dispatcher removed only kro per-operation objects',
           'kro RBAC and admission confine it to per-operation objects',
           'flux-restart: every Flux controller', 'flux-fail-closed: absent kro chart digest', 'shared site layers applied it with services untouched',
           'flux-retention: removing the volume claim', 'flux-final: platform, hub and cell Ready', 'one writer per site object') + tuple(m + ': management rejected' for m in (
               'wrong-cell', 'stale', 'duplicate', 'future', 'wrong-intent', 'wrong-digest', 'wrong-operation', 'incomplete', 'missing'))
if REDFISH:
    markers += ('bmc-upstream: DMTF Redfish-Interface-Emulator 1.2.2 archive verified', 'bmc-self-test: upstream emulator and lab adapter self-test passed',
                'redfish-enroll: synthetic BMC credential Secret created', 'redfish-baseline: both lab target disks present', 'redfish-discovery-faults: real emulator discovery',
                'redfish-lost-response: On took effect but its response was lost', 'redfish-bmc-interrupt: lab BMC Pod force-deleted mid-install',
                'install-b: verified', 'redfish-isolation: target B never started')
for marker in markers:
    if marker not in phases:
        fail('missing acceptance phase: ' + marker)

def libvirt_provider():
    raw = s / 'provider-private'
    raw.mkdir(mode=0o700, exist_ok=True)
    copy_out('/var/lib/fleet-local', raw)
    copy_out('/var/lib/fleet-boot', raw / 'boot-evidence')
    copy_out('/var/log/libvirt/qemu', raw / 'libvirt-qemu')
    j = json.loads((raw / 'journal.json').read_text())
    ops = list(j['operations'].values())
    installs = [o for o in ops if o['request']['kind'] == 'install']
    cleanups = [o for o in ops if o['request']['kind'] == 'cleanup']
    if j.get('bootMode') != 'pxe' or len(installs) != 1 or len(cleanups) != 2 or any(o['result']['phase'] != 'Succeeded' or o.get('stage') != 'done' for o in ops):
        fail('expected PXE mode, exactly one install and two verified cleanups')
    if not j.get('closed'):
        fail('allocation epoch is not closed after final cleanup')
    epochs = sorted({o['request']['epoch'] for o in ops})
    if epochs != [1, 2]:
        fail('expected allocation epochs 1 and 2, got %s' % epochs)

    interrupt = json.loads((ev / 'interrupt.json').read_text())
    if interrupt['operationID'] != installs[0]['request']['id'] or interrupt['stageAtInterrupt'] != 'installer-started' or not interrupt['domainRunningAtInterrupt']:
        fail('interrupt evidence does not identify the retained install operation mid-installer')

    logs = list(raw.rglob('boot-delivery.log'))
    written = '\n'.join(p.read_text() for p in logs)
    for name in ('boot.ipxe', 'kernel', 'installer.gz'):
        if not any('write-complete ' + name + ' bytes=' in line for line in written.splitlines()):
            fail('missing completed broker write for ' + name)

    receipts, starts = [], {}
    for o in installs:
        op_id = o['request']['id']
        directory = raw / hashlib.sha256(op_id.encode()).hexdigest()
        for filename, marker in (('installer.log', 'FLEET-INSTALLED:'), ('disk.log', 'FLEET-DISK-BOOT:')):
            p = directory / filename
            lines = [line.strip() for line in p.read_text(errors='replace').splitlines()] if p.exists() else []
            if lines.count(marker + o['receipt']) != 1 or sum(1 for l in lines if l.startswith(marker)) != 1:
                fail('expected exactly one exact guest receipt in ' + filename)
            receipts.append({'path': str(p.relative_to(raw)), 'marker': marker.rstrip(':'), 'sha256': hashlib.sha256(p.read_bytes()).hexdigest()})
        if sum(1 for l in (directory / 'installer.log').read_text(errors='replace').splitlines() if re.match(r'^\+ sfdisk /dev/vda', l.strip())) != 1:
            fail('installer partitioned the disk other than exactly once')
        installer, disk = (directory / 'installer.xml').read_text(), (directory / 'disk.xml').read_text()
        if "boot dev='network'" not in installer or '<kernel>' in installer or '<initrd>' in installer:
            fail('installer domain is not firmware network boot only')
        if '<interface' in disk or '<kernel>' in disk or '<initrd>' in disk or "boot dev='hd'" not in disk:
            fail('disk-only boot has a NIC or direct kernel')
        if '52:54:00:77:00:02' not in (directory / 'dhcp-leases.txt').read_text():
            fail('missing DHCP lease for the installer NIC')
        if not o.get('bootClosed'):
            fail('boot admission not closed')
        if (directory / 'disk.raw').exists():
            fail('cleaned allocation still has a guest disk')
        # libvirt appends one 'starting up' block per QEMU start. Independent of the
        # journal, this proves one firmware-network installer start and one disk start.
        qemu = (raw / 'libvirt-qemu' / ('fleet-local-' + hashlib.sha256(op_id.encode()).hexdigest()[:24] + '.log')).read_text(errors='replace')
        blocks = [b for b in re.split(r'^\S+ \S+: starting up ', qemu, flags=re.M)[1:]]
        network = [b for b in blocks if '-netdev' in b and '52:54:00:77:00:02' in b]
        disk_only = [b for b in blocks if '-netdev' not in b and 'virtio-disk0' in b]
        starts[op_id] = {'qemuStarts': len(blocks), 'networkInstallerStarts': len(network), 'diskOnlyStarts': len(disk_only)}
        if len(blocks) != 2 or len(network) != 1 or len(disk_only) != 1 or blocks.index(network[0]) != 0:
            fail('expected exactly one network installer start followed by one disk-only start, got %s' % starts[op_id])

    domains = runtime('virsh', '--connect', 'qemu:///system?socket=/run/fleet-libvirt/libvirt-sock', 'list', '--all', '--name').strip()
    if domains:
        fail('provider domain remains after cleanup')
    denials = (s / 'boot-denials.txt').read_text().strip() if (s / 'boot-denials.txt').exists() else ''
    if not denials.startswith('PASS'):
        fail('missing live closed-boot HTTP denial evidence')
    return ops, installs, cleanups, epochs, interrupt, receipts, starts, denials


def redfish_provider():
    """Redfish mode: the persistent lab target A carries every operation.
    Independent of the journal, its serial console proves one exact receipt
    per boot, libvirt's QEMU log proves the boot order and count, the BMC
    audit proves one On per boot, and target B never ran."""
    raw.mkdir(mode=0o700, exist_ok=True)
    copy_out('/var/lib/fleet-local', raw)
    copy_out('/var/lib/fleet-boot', raw / 'boot-evidence')
    copy_out('/var/log/libvirt/qemu', raw / 'libvirt-qemu')
    copy_out('/var/lib/fleet-lab-console', raw / 'lab-console')
    j = json.loads((raw / 'journal.json').read_text())
    ops = sorted(j['operations'].values(), key=lambda o: o['result']['createdAt'])
    installs = [o for o in ops if o['request']['kind'] == 'install']
    cleanups = [o for o in ops if o['request']['kind'] == 'cleanup']
    if j.get('provider') != 'redfish' or len(installs) != 2 or len(cleanups) != 2 or any(o['result']['phase'] != 'Succeeded' or o.get('stage') != 'done' for o in ops):
        fail('expected the Redfish provider, two installs and two verified cleanups')
    if [o['request']['kind'] for o in ops] != ['install', 'cleanup', 'install', 'cleanup'] or not j.get('closed'):
        fail('expected install, cleanup, reinstall, cleanup with the final epoch closed')
    epochs = sorted({o['request']['epoch'] for o in ops})
    if epochs != [1, 2]:
        fail('expected allocation epochs 1 and 2, got %s' % epochs)
    enrolled = json.loads((raw / 'enrollment.json').read_text())
    a = next(t for t in enrolled['targets'] if t['systemID'] == 'System-1')
    for o in ops:
        if o['target'] != a or o['system']['path'] != '/redfish/v1/Systems/System-1' or o['system']['uuid'] != a['systemUUID'] or not o.get('bootClosed'):
            fail('an operation did not retain the exact enrolled target A System with closed boot admission')
    interrupt = json.loads((ev / 'interrupt.json').read_text())
    if interrupt['operationID'] != installs[0]['request']['id'] or interrupt['stageAtInterrupt'] != 'installer-started' or not interrupt['domainRunningAtInterrupt']:
        fail('interrupt evidence does not identify the retained install operation mid-installer')
    console = [l.strip() for l in (raw / 'lab-console' / 'target-a.log').read_text(errors='replace').splitlines()]
    expected, receipts = [], []
    for o in ops:
        want = ['FLEET-INSTALLED:' + o['receipt'], 'FLEET-DISK-BOOT:' + o['receipt']] if o['request']['kind'] == 'install' else ['FLEET-WIPED:%s:%d' % (o['receipt'], a['diskBytes'])]
        for line in want:
            if console.count(line) != 1:
                fail('expected exactly one exact console receipt %s for %s' % (line.split(':')[0], o['request']['kind']))
            expected.append(console.index(line))
        receipts.append({'operation': o['request']['id'], 'kind': o['request']['kind'], 'epoch': o['request']['epoch'],
                         'receipts': [line.split(':')[0] for line in want], 'receiptSHA256': hashlib.sha256(o['receipt'].encode()).hexdigest()})
        log = raw / 'boot-evidence' / hashlib.sha256(o['request']['id'].encode()).hexdigest() / 'boot-delivery.log'
        written = log.read_text() if log.exists() else ''
        for name in ('boot.ipxe', 'kernel', 'installer.gz'):
            if 'write-complete ' + name + ' bytes=' not in written:
                fail('missing completed boot delivery of %s for %s' % (name, o['request']['kind']))
    if expected != sorted(expected) or sum(1 for l in console if l.startswith('FLEET-')) != len(expected):
        fail('console receipts are out of order or include another operation')
    if sum(1 for l in console if l.startswith('+ sfdisk /dev/vda')) != 2:
        fail('the installer partitioned the target disk other than once per install')
    qemu = (raw / 'libvirt-qemu' / 'fleet-lab-target-a.log').read_text(errors='replace')
    blocks = re.split(r'^\S+ \S+: starting up ', qemu, flags=re.M)[1:]
    def network(block):
        return any(a['mac'] in seg and ('"bootindex":1' in seg or 'bootindex=1' in seg) for seg in re.split(r'\s-device\s', block))
    order = ['network' if network(b) else 'disk' for b in blocks]
    starts = {'targetA': order, 'targetB': (raw / 'libvirt-qemu' / 'fleet-lab-target-b.log').read_text(errors='replace').count(': starting up ')
              if (raw / 'libvirt-qemu' / 'fleet-lab-target-b.log').exists() else 0}
    if order != ['network', 'disk', 'network', 'network', 'disk', 'network'] or starts['targetB']:
        fail('expected target A boots network, disk, network, network, disk, network and no target B start; got %s' % starts)
    audit = [json.loads(l) for l in (ev / 'redfish-audit.jsonl').read_text().splitlines()]
    effects = [e for e in audit if e['method'] not in ('GET', 'HEAD')]
    ons = [e for e in effects if e.get('resetType') == 'On']
    if len(ons) != 6 or any(not e.get('effect', '').startswith('started') for e in ons) or any('/System-1' not in e['path'] for e in effects):
        fail('expected exactly six effective On requests, all to System-1, and no other target effect')
    if sum(1 for e in ons if e.get('fault') == 'drop-response-after-effect') != 1:
        fail('the lost-response fault did not fire exactly once')
    if runtime('virsh', '--connect', 'qemu:///system?socket=/run/fleet-libvirt/libvirt-sock', 'list', '--name').strip():
        fail('a lab target is still running')
    admission = json.loads((ev / 'kro-rbac.json').read_text())['admission']
    for case in ('Redfish worker mounting the libvirt socket', 'Redfish worker mounting the lab disks', 'Redfish worker with a libvirt URI', 'worker selecting the libvirt provider'):
        if admission.get(case, {}).get('deniedBy') != 'ValidatingAdmissionPolicy':
            fail('kro admission did not deny: ' + case)
    if not admission.get('Redfish worker Job with the BMC credential', {}).get('admitted'):
        fail('kro admission did not admit the Redfish worker shape')
    for name in ('redfish-upstream.json', 'redfish-enrollment.json', 'redfish-discovery-faults.json', 'redfish-lost-response.json', 'redfish-bmc-interrupt.json', 'redfish-isolation.json'):
        if not (ev / name).exists():
            fail('missing Redfish evidence ' + name)
    denials = (s / 'boot-denials.txt').read_text().strip() if (s / 'boot-denials.txt').exists() else ''
    if not denials.startswith('PASS'):
        fail('missing live closed-boot HTTP denial evidence')
    starts['redfishEffects'] = [{k: e.get(k) for k in ('method', 'path', 'status', 'resetType', 'boot', 'effect', 'fault')} for e in effects]
    return ops, installs, cleanups, epochs, interrupt, receipts, starts, denials


raw = s / 'provider-private'
ops, installs, cleanups, epochs, interrupt, receipts, starts, denials = (redfish_provider if REDFISH else libvirt_provider)()

# Kubernetes execution: what actually ran, as names, UIDs, phases and conditions.
kro = kget('-n', 'kro-system', 'get', 'deployment')['items']
kro_pods = kget('-n', 'kro-system', 'get', 'pods')['items']
rgd = kget('get', 'resourcegraphdefinition', 'fleet-handoff-dispatcher')
instance = kget('-n', 'fleet-site', 'get', 'handoffdispatcher', 'dispatcher')
if rgd['status'].get('state') != 'Active' or instance['status'].get('state') != 'ACTIVE':
    fail('kro RGD or HandoffDispatcher instance not active')
jobs = kget('-n', 'fleet-site', 'get', 'jobs', '-l', 'app.kubernetes.io/name=fleet-site-worker')['items']
handoffs = {h['metadata']['name']: json.loads(h['spec']['requestJSON']) for h in kget('-n', 'fleet-coordination', 'get', 'machineallocationhandoffs')['items']}
worker_jobs = []
for job in jobs:
    handoff = job['metadata']['labels']['fleet.datumapis.com/handoff']
    conditions = sorted([c['type'], c['status'], c.get('reason', '')] for c in job['status'].get('conditions', []))
    if ['Complete', 'True', 'CompletionsReached'] not in conditions:
        fail('worker Job not Complete: ' + job['metadata']['name'])
    job_pods = kget('-n', 'fleet-site', 'get', 'pods', '-l', 'batch.kubernetes.io/controller-uid=' + job['metadata']['uid'])['items']
    exits = [next((c['state']['terminated']['exitCode'] for c in p['status'].get('containerStatuses', []) if 'terminated' in c.get('state', {})), None) for p in job_pods]
    if exits.count(0) != 1:
        fail('worker Job without exactly one successful Pod: ' + job['metadata']['name'])
    worker_jobs.append({'job': job['metadata']['name'], 'uid': job['metadata']['uid'], 'operationID': handoffs[handoff]['id'], 'kind': handoffs[handoff]['kind'],
                        'epoch': handoffs[handoff]['epoch'], 'conditions': conditions, 'succeeded': job['status'].get('succeeded', 0), 'failed': job['status'].get('failed', 0),
                        'pods': [{'uid': p['metadata']['uid'], 'phase': p['status'].get('phase'), 'exitCode': e} for p, e in zip(job_pods, exits)],
                        'serviceAccount': job['spec']['template']['spec']['serviceAccountName'], 'podReplacementPolicy': job['spec'].get('podReplacementPolicy'),
                        'podFailurePolicy': job['spec'].get('podFailurePolicy'), 'backoffLimit': job['spec'].get('backoffLimit'),
                        'activeDeadlineSeconds': job['spec'].get('activeDeadlineSeconds'), 'createdBy': job['metadata']['labels'].get('kro.run/node-id')})
if REDFISH:
    # Every Redfish worker that ran: Redfish provider, no hypervisor path.
    for job in jobs:
        pod = job['spec']['template']['spec']
        c = pod['containers'][0]
        env = {e['name']: e.get('value') for e in c.get('env', [])}
        mounts = [m.get('subPath', '') + ':' + m['mountPath'] for m in c.get('volumeMounts', [])]
        if env.get('FLEET_SITE_PROVIDER') != 'redfish' or 'FLEET_LIBVIRT_URI' in env or any('libvirt' in m or 'lab-disks' in m for m in mounts) or any('hostPath' in v for v in pod.get('volumes', [])):
            fail('a Redfish worker Job had a hypervisor path: ' + job['metadata']['name'])
expected_jobs = ['cleanup', 'cleanup', 'install', 'install'] if REDFISH else ['cleanup', 'cleanup', 'install']
if sorted(j['kind'] for j in worker_jobs) != expected_jobs or any(j['createdBy'] != 'workers' for j in worker_jobs):
    fail('expected kro-created worker Jobs for exactly one install and two cleanups, got %s' % [(j['kind'], j['createdBy']) for j in worker_jobs])
pvc = kget('-n', 'fleet-site', 'get', 'pvc', 'fleet-site-state')
pv = kget('get', 'pv', pvc['spec']['volumeName'])
deployments = {d['metadata']['name']: {'uid': d['metadata']['uid'], 'availableReplicas': d['status'].get('availableReplicas', 0),
                                       'fluxLayer': d['metadata'].get('labels', {}).get('kustomize.toolkit.fluxcd.io/name'),
                                       'kroLabels': sorted(k for k in d['metadata'].get('labels', {}) if k.startswith('kro.run/') or k.startswith('applyset.kubernetes.io/'))}
               for d in kget('-n', 'fleet-site', 'get', 'deployments')['items']}
execution = {
    'observedAt': datetime.datetime.now(datetime.timezone.utc).isoformat(),
    'kro': {'version': 'v0.9.4', 'chart': 'oci://registry.k8s.io/kro/charts/kro@sha256:4e8de4d34b7e2c3cede958e140e26cd6d34a377062e63caf355a636d2bf4a67c',
            'chartArchiveSHA256': 'ceeb630764eb79ca4220dfdcc2907b819a27c30f68cb60d09c1ae3fa2c58b5cd', 'installedBy': 'Flux HelmRelease flux-system/kro',
            'image': kro[0]['spec']['template']['spec']['containers'][0]['image'], 'imageID': kro_pods[0]['status']['containerStatuses'][0]['imageID'], 'rbacMode': 'aggregation',
            'helmReleaseOwner': kro[0]['metadata'].get('labels', {}).get('helm.toolkit.fluxcd.io/name')},
    'resourceGraphDefinition': {'name': 'fleet-handoff-dispatcher', 'state': rgd['status']['state'], 'conditions': sorted([c['type'], c['status']] for c in rgd['status'].get('conditions', []))},
    'handoffDispatcher': {k: v for k, v in instance['status'].items() if k != 'conditions'},
    'siteDeployments': deployments,
    'ownership': 'Flux Kustomization fleet-site-services owns the shared Deployments and boot Service; kro owns only per-operation worker Jobs, ServiceAccounts, Roles and RoleBindings',
    'siteVolume': {'pvc': pvc['metadata']['name'], 'pvcUID': pvc['metadata']['uid'], 'pv': pv['metadata']['name'], 'pvUID': pv['metadata']['uid'],
                   'reclaimPolicy': pv['spec']['persistentVolumeReclaimPolicy'], 'storageClass': pvc['spec']['storageClassName'], 'managedByKro': False},
    'workerJobs': worker_jobs,
    'dispatch': json.loads((s / 'k8s-dispatch-job.json').read_text()),
    'workerRBAC': json.loads((s / 'k8s-worker-rbac.json').read_text()),
    'interruptions': json.loads((ev / 'interrupt.json').read_text()),
    'lifecycle': json.loads((ev / 'lifecycle.json').read_text()),
    'deliveryFaults': json.loads((ev / 'delivery-faults.json').read_text()),
    'authority': 'Job Complete, Flux Ready and kro Ready are execution facts only; management validated Karmada-reflected, journal-derived observations before any projection',
}
if execution['kro']['helmReleaseOwner'] != 'kro':
    fail('kro controller is not owned by the Flux HelmRelease')
if pv['spec']['persistentVolumeReclaimPolicy'] != 'Retain' or sorted(deployments) != sorted(['fleet-site-boot', 'fleet-site-observer', 'fleet-site-runtime'] + (['fleet-site-bmc'] if REDFISH else [])) or any(
        d['availableReplicas'] != 1 or d['fluxLayer'] != 'fleet-site-services' or d['kroLabels'] for d in deployments.values()):
    fail('site volume or Flux-owned shared Deployments not as expected')
(ev / 'kubernetes-execution.json').write_text(json.dumps(execution, indent=2, sort_keys=True) + '\n')
arch = out('docker', 'info', '--format', '{{.Architecture}}').strip()
flux_files = ('flux-verify.json', 'flux-initial.json', 'flux-rbac-restore.json', 'flux-drift.json', 'flux-restart.json', 'flux-fail-closed.json', 'flux-retention.json', 'flux-final.json',
              'flux-graph-lifecycle.json', 'flux-ownership-final.json', 'kro-rbac.json')
for name in flux_files:
    if not (ev / name).exists():
        fail('missing Flux evidence ' + name)
flux_revisions = json.loads((s / 'flux-revisions.json').read_text())
final = json.loads((ev / 'flux-final.json').read_text())
for cluster, snap in final.items():
    if snap['sources']['flux-system/fleet-gitops']['artifactRevision'] != flux_revisions[-1]['fluxRevision']:
        fail('%s did not end on the final source revision' % cluster)
    if any(k['lastAppliedRevision'] != flux_revisions[-1]['fluxRevision'] or k['suspend'] for n, k in snap['kustomizations'].items()):
        fail('%s has a Kustomization not at the final revision' % cluster)
write_env = json.loads((s / 'gitops-environment.json').read_text())

summary = {
    'schemaVersion': 2, 'acceptance': 'passed',
    'observedAt': datetime.datetime.now(datetime.timezone.utc).isoformat(), 'prefix': prefix,
    'qualification': {'nativeMiloProjectAPIs': True, 'actualKarmadaDeliveryAndReflection': True, 'realLocalPXE': True,
                      'kubernetesJobExecution': True, 'actualKroController': True, 'fluxReconciledServices': True,
                      'kvm': ACCEL == 'kvm', 'uefi': False, 'productionAuthority': False, 'productionEquivalence': False},
    'versions': {'milo': 'v0.32.5', 'karmada': 'v1.16.0', 'kro': 'v0.9.4', 'flux': 'v2.9.6', 'kindNode': 'v1.35.0', 'architecture': arch, 'guestArchitecture': 'amd64', 'accelerator': ACCEL, 'firmware': 'SeaBIOS/iPXE (legacy BIOS)'},
    'scenarios': {
        'exclusiveContention': True,
        'partitionPublicReadyStayedFalse': True,
        'interruptedWorkerResumedExactOperation': interrupt,
        'kubernetesExecution': 'kubernetes-execution.json',
        'gitops': {'sourceRevisions': [{k: r[k] for k in ('revision', 'digest', 'purpose', 'gitHead', 'contentSumsSHA256', 'files')} for r in flux_revisions],
                   'evidence': list(flux_files), 'siteImage': write_env['siteImage']},
        'installerStarts': starts,
        'reflectionRejections': ['wrong-cell', 'stale', 'duplicate', 'future', 'wrong-intent', 'wrong-digest', 'wrong-operation', 'incomplete', 'missing'],
        'wrongLocationDeniedBeforeAllocation': json.loads((ev / 'wrong-location.json').read_text()),
        'releasedProjection': {'consumerA': {'machine': 'Released, Bound False, Ready False, no currentInstallationRef, no Fleet finalizer', 'installation': 'Released, Ready False'},
                               'consumerB': {'machine': 'Released, Bound False, Ready False, no currentInstallationRef, no Fleet finalizer'}},
        'cleanupAndReuse': {'epochs': epochs, 'cleanups': len(cleanups), 'domainsAfterCleanup': [], 'disksAfterCleanup': 0},
        'closedBootDenials': denials,
    },
    'operations': [{'id': o['request']['id'], 'kind': o['request']['kind'], 'epoch': o['request']['epoch'], 'phase': o['result']['phase'], 'result': o['result']['result'], 'inputDigest': o['result']['inputDigest']} for o in ops],
    'projectRouting': routing, 'brokerWriteCompletion': True, 'guestEvidence': receipts,
    'runtimePodMemoryEvents': {key: int(value) for key, value in memory_events.items()},
    'cellNodeMemoryEvents': {key: int(value) for key, value in node_memory_events.items()},
    'nodeMemoryEvents': {node: {key: int(value) for key, value in e.items()} for node, e in node_events.items()},
    'seams': ['static-token root RBAC', 'independent local Milo roots on one platform kind cluster (Flux-managed workloads, bootstrap-enrolled credentials, fixture project bootstrap)',
              'plain-HTTP unsigned local OCI registry as the Flux source', 'bootstrap-minted 24h ServiceAccount tokens and member Secret', 'synthetic source-use admission and publication',
              'synthetic quota and settlement', 'synthetic hardware enrollment mapping', 'networkless workload',
              'privileged disposable VM runtime Pod on a single kind node', 'kro reads delivered handoffs (it must, to enumerate them)',
              'trusted graph and services authors: worker Jobs reach root libvirtd through its socket',
              'reflection faults injected with bootstrap credentials while the Karmada controller is stopped'],
    'privateArtifacts': 'state directory outside evidence/ contains authority; do not publish',
}
if RUNTIME == 'ci':
    # A KVM guest on a GitHub-hosted runner is nested virtualization in a cloud
    # VM: a different qualification target from the recorded Colima TCG runs,
    # and still not physical hardware.
    summary['qualification'].update(runtime='GitHub-hosted runner (CI mode)', physicalHardware=False)
if REDFISH:
    summary['qualification'].update(redfishExecutor=True, dmtfRedfishInterfaceEmulator='1.2.2 (unmodified) + lab-only libvirt adapter', bootedCleanupEnvironment=True,
                                    physicalHardware=False, physicalBMC=False, vendorRedfishQualified=False, bmcTLS=False, mediaSanitization=False)
    summary['scenarios']['cleanupAndReuse'] = {'epochs': epochs, 'installs': len(installs), 'cleanups': len(cleanups), 'reinstallOnReusedTarget': True,
                                               'cleanup': 'PXE-booted wipe environment zeroed and read back the whole 2 GiB virtual disk; the harness hashed the disk file as all zeroes. Logical test cleanup, not media sanitization.'}
    summary['scenarios']['redfish'] = {'evidence': ['redfish-upstream.json', 'redfish-enrollment.json', 'redfish-discovery-faults.json', 'redfish-lost-response.json',
                                                    'redfish-bmc-interrupt.json', 'redfish-isolation.json', 'redfish-audit.jsonl'],
                                       'bootOrderTargetA': starts['targetA'], 'targetBStarts': starts['targetB'], 'effects': starts['redfishEffects']}
    summary['seams'] = [x for x in summary['seams'] if 'root libvirtd' not in x] + [
        'DMTF Redfish Interface Emulator with a lab-only adapter in place of a physical BMC; libvirt/QEMU TCG VMs in place of servers',
        'plain-HTTP Basic authentication with a synthetic per-run credential Secret enrolled by the harness (no TLS, rotation or vault)',
        'static lab provisioning network (libvirt dnsmasq) with MAC-chained iPXE; MAC is routing, not authentication',
        'installation receipts read from a read-only serial-console capture (stand-in for SOL or an installer callback)',
        'trusted graph authors: worker Jobs hold the synthetic BMC credential; only the runtime and the lab BMC hold the libvirt socket',
        'one site journal fences every lab target (one allocatable target)']
(ev / 'summary.json').write_text(json.dumps(summary, indent=2) + '\n')
for name in ('source-SHA256SUMS', 'sysctl.json'):
    if (s / name).exists():
        (ev / name).write_text((s / name).read_text())
paths = [s / 'site-bin' / 'fleet-multiplane', s / 'fleet-multiplane-host']
(ev / 'binary-SHA256SUMS').write_text(''.join(hashlib.sha256(p.read_bytes()).hexdigest() + '  ' + p.name + '\n' for p in paths))
(ev / 'image-id').write_text(out('docker', 'image', 'inspect', prefix + '-site-image', '--format', '{{.Id}}').strip() + '\nregistry digest ' + write_env['siteImageDigest'] + '\n' + (
    'bmc image ' + out('docker', 'image', 'inspect', prefix + '-bmc-image', '--format', '{{.Id}}').strip() + '\nbmc registry digest ' + write_env['bmcImageDigest'] + '\n' if REDFISH else ''))
print('evidence: sanitized summary ' + str(ev / 'summary.json'))
