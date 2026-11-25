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

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
import harness  # noqa: E402  (site store layout, provider custody)

s = pathlib.Path(sys.argv[1])
prefix = (s / 'prefix').read_text().strip()
ev = s / 'evidence'
os.environ['DOCKER_CONTEXT'] = 'colima-fleet-acceptance-tcg'
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

custody_rules = json.loads((ev / 'custody-crd-rules.json').read_text())
if not custody_rules.get('objectsRemoved') or len(custody_rules.get('checks', [])) != 8 or not all(c.get('refused') for c in custody_rules['checks']):
    fail('the provider project API did not refuse every forbidden custody write')

phases = (s / 'phases.log').read_text()
REFLECTION_FAULTS = ('wrong-cell', 'foreign-observer', 'duplicate', 'future', 'wrong-assignment', 'wrong-device', 'wrong-operation', 'stale-fence', 'missing')
markers = ('custody-crd-rules: the provider project API refused every forbidden custody and operation write', 'Verified forbidden observer/project/hub writes', 'allocate: verified', 'contention: consumer-a Bound', 'delivery: Karmada delivered exactly one',
           'kro created the worker Job and per-operation RBAC', 'worker-rbac: per-operation ServiceAccount', 'interrupt-pod: worker Pod force-deleted',
           'interrupt-job: running worker Job deleted mid-install', 'resume: replacement worker completed the exact interrupted operation',
           'worker Job is Complete while hub/consumer disconnected; public Ready stayed False', 'install: verified', 'lifecycle: runtime, boot and observer Pods',
           'delivery-faults: duplicate refused by the API', 'boot-denials: PASS', 'release: verified', 'release-status-a: consumer-a Machine Released', 'Installation Released, Ready False', 'reuse: verified', 'reuse-status: consumer-b Bound', 'release-b: verified', 'release-status-b: consumer-b Machine deleted',
           'import-inert: ', 'staff-access: staff read custody', 'contention-custody: custody holds the Device for the one MachineAllocation',
           'deadline-admit: the second installation was admitted', 'release-waits: the Machine is deleting', 'deadline-hold: the installation ended OutcomeUnknown at its deadline',
           'recovery-mismatch: refused', 'deadline-settled: the release wipe was admitted', 'rebuild-unverified: custody rebuilt Unverified',
           'probe-unallocatable: no eligible capacity while custody is Unverified', 'rebuild-verified: custody Verified',
           'wrong-location: denied before allocation', 'Flux restored the binding', 'flux-verify: 3 clusters Ready', 'cell Flux applied the dispatch revision',
           'Flux restored consumer-a', 'Flux on every cluster Ready at the current revision', 'flux-drift: helm-controller restored kro',
           'recreated the shared boot Service at its fixed ClusterIP and restored the observer', 'flux-graph-lifecycle: deleting the dispatcher removed only kro per-operation objects',
           'kro RBAC and admission confine it to per-operation objects',
           'flux-restart: every Flux controller', 'flux-fail-closed: absent kro chart digest', 'shared site layers applied it with services untouched',
           'flux-retention: removing the volume claim', 'flux-final: platform, hub and cell Ready', 'one writer per site object') + tuple(m + ': management rejected' for m in REFLECTION_FAULTS)
if REDFISH:
    markers += ('bmc-upstream: DMTF Redfish-Interface-Emulator 1.2.2 archive verified', 'bmc-self-test: upstream emulator and lab adapter self-test passed',
                'redfish-enroll: synthetic per-Device BMC credentials', 'redfish-baseline: both lab target disks present', 'redfish-discovery-faults: real emulator discovery',
                'redfish-lost-response: On took effect but its response was lost', 'redfish-bmc-interrupt: lab BMC Pod force-deleted mid-install',
                'a TLS key other than the pin reached no request', 'redfish-isolation: target B never started')
for marker in markers:
    if marker not in phases:
        fail('missing acceptance phase: ' + marker)

def site_operations(raw):
    """The Device's journaled operations from the copied site store, matched to
    the provider project's operations by UID. Expected order: consumer A's
    install, A's release wipe, consumer B's install, held at its deadline,
    and B's release wipe, with strictly increasing fences. Each operation
    but the held one Succeeded at its fence; that one is returned apart."""
    store = json.loads((raw / 'site' / 'devices' / harness.device_dir().rsplit('/', 1)[1] / 'state.json').read_text())
    journaled = harness.sanitized_operations(store, redfish=REDFISH)
    provider = {i['metadata']['uid']: i for i in harness.custody(s)}
    held_uid = json.loads((s / 'deadline-install.json').read_text())['uid']
    ops, held = [], None
    if [o['fence'] for o in journaled] != sorted({o['fence'] for o in journaled}):
        fail('journaled fences are not strictly increasing: %s' % [o['fence'] for o in journaled])
    for o in journaled:
        p = provider.get(o['operation'])
        if o['operation'] == held_uid:
            st = (p or {}).get('status', {})
            if not p or p['kind'] != 'DeviceInstallation' or o['done'] or st.get('phase') != 'OutcomeUnknown' or st.get('reason') != 'DeadlineExceeded' or st.get('fence') != o['fence']:
                fail('the deadline installation is not held OutcomeUnknown at its fence: %s' % {k: st.get(k) for k in ('phase', 'reason', 'fence')})
            held = dict(o, name=p['metadata']['name'], allocation=p['spec']['authority']['allocationRef']['name'], phase=st['phase'])
            continue
        if not p or p['kind'] != o['kind'] or not o['done']:
            fail('journaled operation %s is not a finished provider %s' % (o['operation'], o['kind']))
        st = p.get('status', {})
        if st.get('phase') != 'Succeeded' or st.get('fence') != o['fence']:
            fail('%s %s is %s at fence %s, journaled at fence %s' % (p['kind'], p['metadata']['name'], st.get('phase'), st.get('fence'), o['fence']))
        ops.append(dict(o, name=p['metadata']['name'], allocation=p['spec']['authority']['allocationRef']['name'], phase=st['phase']))
    if held is None:
        fail('the site journal holds no deadline installation')
    return store, ops, provider, held


def check_sequence(ops, held, interrupt):
    """Consumer A's allocation holds the interrupted install; the other is B's,
    whose installation was held at its deadline."""
    a = next((o['allocation'] for o in ops if o['operation'] == interrupt['operationID']), None)
    b = next((o['allocation'] for o in ops if o['allocation'] != a), None)
    want = [('DeviceInstallation', a), ('DeviceWipe', a), ('DeviceWipe', b)]
    if not a or not b or held['allocation'] != b or [(o['kind'], o['allocation']) for o in ops] != want:
        fail('expected %s, got %s' % (want, [(o['kind'], o['allocation']) for o in ops]))
    if any(o['kind'] == 'DeviceWipe' and o['name'] != o['allocation'] + '-wipe-1' for o in ops):
        fail('a release wiped the Device other than with its first wipe step')
    stage = 'installer-power-on' if REDFISH else 'installer-start'
    if interrupt['stageAtInterrupt'] != stage or not interrupt['domainRunningAtInterrupt'] or interrupt['fence'] != ops[0]['fence']:
        fail('interrupt evidence does not identify the retained install operation mid-installer')
    return a, b


def custody_evidence(store, ops, provider, a, b):
    """Final provider custody: the Device returned Verified to no holder, both
    allocations Released, and the site's fence equal to custody's."""
    custody = next((i for i in provider.values() if i['kind'] == 'DeviceCustody' and i['metadata']['name'] == harness.DEVICE.split('/')[1]), None)
    allocations = {i['metadata']['name']: i for i in provider.values() if i['kind'] == 'MachineAllocation'}
    if not custody or a not in allocations or b not in allocations:
        fail('missing the Device custody or a consumer allocation in the provider project')
    spec = custody['spec']
    if spec.get('state') != 'Verified' or spec.get('holder', {}).get('type') != 'None' or spec.get('activeOperation') or spec.get('recoveryHold'):
        fail('final custody is not Verified with no holder, operation or hold: %s' % {k: spec.get(k) for k in ('state', 'holder', 'activeOperation', 'recoveryHold')})
    # Custody was rebuilt after the last operation, with a fence above every
    # fence the site stored.
    rebuilt = json.loads((ev / 'rebuild.json').read_text())
    if spec.get('fence') != rebuilt['verifiedFence'] or spec['fence'] <= ops[-1]['fence'] or store.get('fence') != ops[-1]['fence'] or store.get('stopped') != ops[-1]['fence']:
        fail('custody fence %s, site fence %s/%s and the last operation fence %s do not follow the rebuild' % (spec.get('fence'), store.get('fence'), store.get('stopped'), ops[-1]['fence']))
    recoveries = [i for i in provider.values() if i['kind'] == 'DeviceRecovery']
    if len(recoveries) != 1 or recoveries[0].get('status', {}).get('phase') != 'Failed' or recoveries[0]['status'].get('fence'):
        fail('expected only the refused mismatched recovery, never admitted')
    record = {'observedAt': datetime.datetime.now(datetime.timezone.utc).isoformat(), 'device': harness.DEVICE,
              'custody': {'state': spec['state'], 'holder': spec['holder'], 'fence': spec['fence'], 'activeOperation': None, 'recoveryHold': None,
                          'rejectedResults': spec.get('rejectedResults')},
              'siteFence': {'seen': store['fence'], 'stopped': store['stopped']}, 'allocations': {}}
    for consumer, name in (('consumerA', a), ('consumerB', b)):
        m = allocations[name]
        if m.get('status', {}).get('phase') != 'Released' or m['spec']['deviceRef']['name'] != harness.DEVICE.split('/')[1]:
            fail('%s allocation %s is not Released for the Device' % (consumer, name))
        record['allocations'][consumer] = {'name': name, 'phase': 'Released', 'machine': {k: m['spec']['machineRef'].get(k) for k in ('project', 'namespace', 'name')},
                                           'wipe': m['status'].get('wipe')}
    record['operations'] = [{k: o[k] for k in ('kind', 'name', 'operation', 'allocation', 'fence', 'phase')} for o in ops]
    (ev / 'custody.json').write_text(json.dumps(record, indent=2, sort_keys=True) + '\n')
    return record


def boot_record(raw, collection, name):
    p = raw / 'site' / 'devices' / harness.device_dir().rsplit('/', 1)[1] / collection / (name + '.json')
    return json.loads(p.read_text()) if p.exists() else {}


def libvirt_provider():
    raw = s / 'provider-private'
    raw.mkdir(mode=0o700, exist_ok=True)
    copy_out('/var/lib/fleet-local', raw)
    copy_out('/var/lib/fleet-boot', raw / 'boot-evidence')
    copy_out('/var/log/libvirt/qemu', raw / 'libvirt-qemu')
    store, ops, provider, held = site_operations(raw)
    interrupt = json.loads((ev / 'interrupt.json').read_text())
    a, b = check_sequence(ops, held, interrupt)
    installs = [o for o in ops if o['kind'] == 'DeviceInstallation']
    wipes = [o for o in ops if o['kind'] == 'DeviceWipe']

    receipts, starts = [], {}
    for o in installs:
        uid = o['operation']
        directory = raw / 'vm' / harness.key(harness.DEVICE) / harness.key(uid)
        boot = boot_record(raw, 'vmboot', harness.key(uid))
        if not boot.get('closed') or boot.get('open') or boot.get('fence') != o['fence']:
            fail('boot admission not closed at the install fence')
        log = raw / 'boot-evidence' / harness.key(uid) / 'boot-delivery.log'
        written = log.read_text() if log.exists() else ''
        for name in ('boot.ipxe', 'kernel', 'installer.gz'):
            if not any('write-complete ' + name + ' bytes=' in line for line in written.splitlines()):
                fail('missing completed broker write for ' + name)
        for filename, marker in (('installer.log', 'FLEET-INSTALLED:'), ('disk.log', 'FLEET-DISK-BOOT:')):
            p = directory / filename
            lines = [line.strip() for line in p.read_text(errors='replace').splitlines()] if p.exists() else []
            if lines.count(marker + boot['receipt']) != 1 or sum(1 for l in lines if l.startswith(marker)) != 1:
                fail('expected exactly one exact guest receipt in ' + filename)
            receipts.append({'operation': uid, 'fence': o['fence'], 'path': str(p.relative_to(raw)), 'marker': marker.rstrip(':'), 'sha256': hashlib.sha256(p.read_bytes()).hexdigest()})
        if sum(1 for l in (directory / 'installer.log').read_text(errors='replace').splitlines() if re.match(r'^\+ sfdisk /dev/vda', l.strip())) != 1:
            fail('installer partitioned the disk other than exactly once')
        installer, disk = (directory / 'installer.xml').read_text(), (directory / 'disk.xml').read_text()
        if "boot dev='network'" not in installer or '<kernel>' in installer or '<initrd>' in installer:
            fail('installer domain is not firmware network boot only')
        if '<interface' in disk or '<kernel>' in disk or '<initrd>' in disk or "boot dev='hd'" not in disk:
            fail('disk-only boot has a NIC or direct kernel')
        if '52:54:00:77:00:02' not in (directory / 'dhcp-leases.txt').read_text():
            fail('missing DHCP lease for the installer NIC')
        # libvirt appends one 'starting up' block per QEMU start. Independent of the
        # journal, this proves one firmware-network installer start and one disk start.
        qemu = (raw / 'libvirt-qemu' / (harness.domain_name(uid) + '.log')).read_text(errors='replace')
        blocks = [b for b in re.split(r'^\S+ \S+: starting up ', qemu, flags=re.M)[1:]]
        network = [b for b in blocks if '-netdev' in b and '52:54:00:77:00:02' in b]
        disk_only = [b for b in blocks if '-netdev' not in b and 'virtio-disk0' in b]
        starts[uid] = {'qemuStarts': len(blocks), 'networkInstallerStarts': len(network), 'diskOnlyStarts': len(disk_only)}
        if len(blocks) != 2 or len(network) != 1 or len(disk_only) != 1 or blocks.index(network[0]) != 0:
            fail('expected exactly one network installer start followed by one disk-only start, got %s' % starts[uid])
    # The release wipes deleted every installation disk of the Device.
    if list((raw / 'vm').rglob('disk.raw')):
        fail('a wiped Device still has an installation disk')
    domains = runtime('virsh', '--connect', 'qemu:///system?socket=/run/fleet-libvirt/libvirt-sock', 'list', '--all', '--name').strip()
    if domains:
        fail('provider domain remains after the wipes')
    denials = (s / 'boot-denials.txt').read_text().strip() if (s / 'boot-denials.txt').exists() else ''
    if not denials.startswith('PASS'):
        fail('missing live closed-boot HTTP denial evidence')
    custody = custody_evidence(store, ops, provider, a, b)
    return ops, installs, wipes, custody, interrupt, receipts, starts, denials


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
    store, ops, provider, held = site_operations(raw)
    interrupt = json.loads((ev / 'interrupt.json').read_text())
    a_alloc, b_alloc = check_sequence(ops, held, interrupt)
    installs = [o for o in ops if o['kind'] == 'DeviceInstallation']
    wipes = [o for o in ops if o['kind'] == 'DeviceWipe']
    enrollment = json.loads(next(l for l in pathlib.Path('config/site/redfish/graph/kustomization.yaml').read_text().splitlines() if l.strip().startswith("value: '{")).split("'")[1])
    a = next(t for t in enrollment['targets'] if t['device'] == harness.DEVICE)
    if a['systemID'] != 'System-1' or a['console'] != 'target-a.log':
        fail('the allocated Device is not enrolled as lab target A')
    console = [l.strip() for l in (raw / 'lab-console' / 'target-a.log').read_text(errors='replace').splitlines()]
    expected, receipts = [], []
    for o in ops:
        boot = boot_record(raw, 'boot', o['operation'])
        if not boot.get('closed') or boot.get('phase') or boot.get('fence') != o['fence'] or boot.get('mac') != a['mac'].lower():
            fail('an operation did not keep a closed boot admission for target A at its fence')
        want = ['FLEET-INSTALLED:' + boot['receipt'], 'FLEET-DISK-BOOT:' + boot['receipt']] if o['kind'] == 'DeviceInstallation' else ['FLEET-WIPED:%s:%d' % (boot['receipt'], a['diskBytes'])]
        for line in want:
            if console.count(line) != 1:
                fail('expected exactly one exact console receipt %s for %s' % (line.split(':')[0], o['kind']))
            expected.append(console.index(line))
        receipts.append({'operation': o['operation'], 'kind': o['kind'], 'fence': o['fence'],
                         'receipts': [line.split(':')[0] for line in want], 'receiptSHA256': hashlib.sha256(boot['receipt'].encode()).hexdigest()})
        log = raw / 'boot-evidence' / harness.key(o['operation']) / 'boot-delivery.log'
        written = log.read_text() if log.exists() else ''
        for name in ('boot.ipxe', 'kernel', 'installer.gz'):
            if 'write-complete ' + name + ' bytes=' not in written:
                fail('missing completed boot delivery of %s for %s' % (name, o['kind']))
    if expected != sorted(expected) or sum(1 for l in console if l.startswith('FLEET-')) != len(expected):
        fail('console receipts are out of order or include another operation')
    if sum(1 for l in console if l.startswith('+ sfdisk /dev/vda')) != 1:
        fail('the installer partitioned the target disk other than once for the one finished install')
    qemu = (raw / 'libvirt-qemu' / 'fleet-lab-target-a.log').read_text(errors='replace')
    blocks = re.split(r'^\S+ \S+: starting up ', qemu, flags=re.M)[1:]
    def network(block):
        return any(a['mac'] in seg and ('"bootindex":1' in seg or 'bootindex=1' in seg) for seg in re.split(r'\s-device\s', block))
    order = ['network' if network(b) else 'disk' for b in blocks]
    starts = {'targetA': order, 'targetB': (raw / 'libvirt-qemu' / 'fleet-lab-target-b.log').read_text(errors='replace').count(': starting up ')
              if (raw / 'libvirt-qemu' / 'fleet-lab-target-b.log').exists() else 0}
    # A install (network, disk), A wipe, B's held install (its network boot
    # found no boot server), B wipe.
    if order != ['network', 'disk', 'network', 'network', 'network'] or starts['targetB']:
        fail('expected target A boots network, disk, network, network, network and no target B start; got %s' % starts)
    audit = [json.loads(l) for l in (ev / 'redfish-audit.jsonl').read_text().splitlines()]
    effects = [e for e in audit if e['method'] not in ('GET', 'HEAD')]
    ons = [e for e in effects if e.get('resetType') == 'On']
    if len(ons) != 5 or any(not e.get('effect', '').startswith('started') for e in ons) or any('/System-1' not in e['path'] for e in effects):
        fail('expected exactly five effective On requests, all to System-1, and no other target effect')
    if sum(1 for e in ons if e.get('fault') == 'drop-response-after-effect') != 1:
        fail('the lost-response fault did not fire exactly once')
    # Each boot was requested once: one journaled power-on step per phase.
    powered = sum(1 for o in ops + [held] for n, st in o['steps'] if n.endswith('-power-on') and st == 'Done')
    if powered != len(ons):
        fail('journaled %d power-on steps for %d effective On requests' % (powered, len(ons)))
    if runtime('virsh', '--connect', 'qemu:///system?socket=/run/fleet-libvirt/libvirt-sock', 'list', '--name').strip():
        fail('a lab target is still running')
    admission = json.loads((ev / 'kro-rbac.json').read_text())['admission']
    for case in ('Redfish worker mounting the libvirt socket', 'Redfish worker mounting the lab disks', 'Redfish worker with a libvirt URI', 'worker selecting the libvirt provider',
                 'Redfish worker mounting a credential Secret'):
        if admission.get(case, {}).get('deniedBy') != 'ValidatingAdmissionPolicy':
            fail('kro admission did not deny: ' + case)
    for case in ('Redfish worker Job', 'worker credential Role'):
        if not admission.get(case, {}).get('admitted'):
            fail('kro admission did not admit: ' + case)
    for name in ('redfish-upstream.json', 'redfish-enrollment.json', 'redfish-discovery-faults.json', 'redfish-lost-response.json', 'redfish-bmc-interrupt.json', 'redfish-isolation.json'):
        if not (ev / name).exists():
            fail('missing Redfish evidence ' + name)
    denials = (s / 'boot-denials.txt').read_text().strip() if (s / 'boot-denials.txt').exists() else ''
    if not denials.startswith('PASS'):
        fail('missing live closed-boot HTTP denial evidence')
    starts['redfishEffects'] = [{k: e.get(k) for k in ('method', 'path', 'status', 'resetType', 'boot', 'effect', 'fault')} for e in effects]
    custody = custody_evidence(store, ops, provider, a_alloc, b_alloc)
    return ops, installs, wipes, custody, interrupt, receipts, starts, denials


raw = s / 'provider-private'
ops, installs, wipes, custody, interrupt, receipts, starts, denials = (redfish_provider if REDFISH else libvirt_provider)()

# Kubernetes execution: what actually ran, as names, UIDs, phases and conditions.
kro = kget('-n', 'kro-system', 'get', 'deployment')['items']
kro_pods = kget('-n', 'kro-system', 'get', 'pods')['items']
rgd = kget('get', 'resourcegraphdefinition', 'fleet-handoff-dispatcher')
instance = kget('-n', 'fleet-site', 'get', 'handoffdispatcher', 'dispatcher')
if rgd['status'].get('state') != 'Active' or instance['status'].get('state') != 'ACTIVE':
    fail('kro RGD or HandoffDispatcher instance not active')
jobs = kget('-n', 'fleet-site', 'get', 'jobs', '-l', 'app.kubernetes.io/name=fleet-site-worker')['items']
handoffs = {h['metadata']['name']: json.loads(h['spec']['assignmentJSON']) for h in kget('-n', 'fleet-coordination', 'get', 'machineallocationhandoffs', '-l', 'coordination.fleet.datumapis.com/role=assignment')['items']}
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
    operation = handoffs[handoff]['operation']
    worker_jobs.append({'job': job['metadata']['name'], 'uid': job['metadata']['uid'], 'operationID': operation['uid'], 'kind': operation['kind'], 'name': operation['name'],
                        'fence': handoffs[handoff]['fence'], 'conditions': conditions, 'succeeded': job['status'].get('succeeded', 0), 'failed': job['status'].get('failed', 0),
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
# One kro-created worker Job per admitted operation, at that operation's fence.
if sorted((j['operationID'], j['fence']) for j in worker_jobs) != sorted((o['operation'], o['fence']) for o in ops) or any(j['createdBy'] != 'workers' for j in worker_jobs):
    fail('expected one kro-created worker Job per operation at its fence, got %s' % [(j['kind'], j['name'], j['fence'], j['createdBy']) for j in worker_jobs])
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
    'authority': 'Job Complete, Flux Ready and kro Ready are execution facts only; the custody coordinator accepted only Karmada-reflected results the site stored at each fence',
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
# The #174 failure scenarios, each written by hack/multiplane/scenarios.py.
FAILURE_SCENARIOS = ('import-inert', 'staff-access', 'contention', 'release-waits', 'deadline-hold', 'deadline-settled', 'rebuild')
failure_scenarios = {}
for name in FAILURE_SCENARIOS:
    if not (ev / (name + '.json')).exists():
        fail('missing failure scenario evidence ' + name)
    failure_scenarios[name] = json.loads((ev / (name + '.json')).read_text())

summary = {
    'schemaVersion': 2, 'acceptance': 'passed',
    'observedAt': datetime.datetime.now(datetime.timezone.utc).isoformat(), 'prefix': prefix,
    'qualification': {'nativeMiloProjectAPIs': True, 'actualKarmadaDeliveryAndReflection': True, 'realLocalPXE': True,
                      'kubernetesJobExecution': True, 'actualKroController': True, 'fluxReconciledServices': True,
                      'kvm': False, 'uefi': False, 'productionAuthority': False, 'productionEquivalence': False},
    'versions': {'milo': 'v0.32.5', 'karmada': 'v1.16.0', 'kro': 'v0.9.4', 'flux': 'v2.9.6', 'kindNode': 'v1.35.0', 'architecture': arch, 'guestArchitecture': 'amd64', 'accelerator': 'tcg', 'firmware': 'SeaBIOS/iPXE (legacy BIOS)'},
    'scenarios': {
        'exclusiveContention': True,
        'partitionPublicReadyStayedFalse': True,
        'interruptedWorkerResumedExactOperation': interrupt,
        'kubernetesExecution': 'kubernetes-execution.json',
        'gitops': {'sourceRevisions': [{k: r[k] for k in ('revision', 'digest', 'purpose', 'gitHead', 'contentSumsSHA256', 'files')} for r in flux_revisions],
                   'evidence': list(flux_files), 'siteImage': write_env['siteImage']},
        'installerStarts': starts,
        'reflectionRejections': list(REFLECTION_FAULTS),
        'failureScenarios': failure_scenarios,
        'wrongLocationDeniedBeforeAllocation': json.loads((ev / 'wrong-location.json').read_text()),
        'custodyCRDRules': custody_rules,
        'releasedProjection': {'consumerA': {'machine': 'Released, Bound False, Ready False, no currentInstallationRef, no Fleet finalizer', 'installation': 'Released, Ready False'},
                               'consumerB': {'machine': 'Released, Bound False, Ready False, no currentInstallationRef, no Fleet finalizer'}},
        'wipeAndReuse': {'fences': [o['fence'] for o in ops], 'wipes': [o['name'] for o in wipes], 'domainsAfterWipe': [], 'disksAfterWipe': 0},
        'custody': 'custody.json',
        'closedBootDenials': denials,
    },
    'operations': [{'operation': o['operation'], 'kind': o['kind'], 'name': o['name'], 'allocation': o['allocation'], 'fence': o['fence'], 'phase': o['phase'],
                    'inputDigest': next((h['inputDigest'] for h in handoffs.values() if h['operation']['uid'] == o['operation']), None)} for o in ops],
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
if REDFISH:
    summary['qualification'].update(redfishExecutor=True, dmtfRedfishInterfaceEmulator='1.2.2 (unmodified) + lab-only libvirt adapter', bootedWipeEnvironment=True,
                                    physicalHardware=False, physicalBMC=False, vendorRedfishQualified=False, bmcTLS=True, mediaSanitization=False)
    summary['scenarios']['wipeAndReuse'] = {'fences': [o['fence'] for o in ops], 'installs': len(installs), 'wipes': [o['name'] for o in wipes], 'reinstallOnReusedTarget': True,
                                            'wipe': 'PXE-booted wipe environment zeroed and read back the whole 2 GiB virtual disk; the harness hashed the disk file as all zeroes. Logical test wipe, not media sanitization.'}
    summary['scenarios']['redfish'] = {'evidence': ['redfish-upstream.json', 'redfish-enrollment.json', 'redfish-discovery-faults.json', 'redfish-lost-response.json',
                                                    'redfish-bmc-interrupt.json', 'redfish-isolation.json', 'redfish-audit.jsonl'],
                                       'bootOrderTargetA': starts['targetA'], 'targetBStarts': starts['targetB'], 'effects': starts['redfishEffects']}
    summary['seams'] = [x for x in summary['seams'] if 'root libvirtd' not in x] + [
        'DMTF Redfish Interface Emulator with a lab-only adapter in place of a physical BMC; libvirt/QEMU TCG VMs in place of servers',
        'Basic authentication over TLS pinned to a lab key generated per run, with synthetic per-Device credential Secrets enrolled by the harness (no rotation or vault)',
        'static lab provisioning network (libvirt dnsmasq) with MAC-chained iPXE; MAC is routing, not authentication',
        'installation receipts read from a read-only serial-console capture (stand-in for SOL or an installer callback)',
        'trusted graph authors: worker Jobs list the site BMC credential Secrets; only the runtime and the lab BMC hold the libvirt socket',
        'one allocatable lab target; the site store fences each Device separately']
(ev / 'summary.json').write_text(json.dumps(summary, indent=2) + '\n')
for name in ('source-SHA256SUMS', 'sysctl.json'):
    if (s / name).exists():
        (ev / name).write_text((s / name).read_text())
paths = [s / 'site-bin' / 'fleet-multiplane', s / 'fleet-multiplane-host']
(ev / 'binary-SHA256SUMS').write_text(''.join(hashlib.sha256(p.read_bytes()).hexdigest() + '  ' + p.name + '\n' for p in paths))
(ev / 'image-id').write_text(out('docker', 'image', 'inspect', prefix + '-site-image', '--format', '{{.Id}}').strip() + '\nregistry digest ' + write_env['siteImageDigest'] + '\n' + (
    'bmc image ' + out('docker', 'image', 'inspect', prefix + '-bmc-image', '--format', '{{.Id}}').strip() + '\nbmc registry digest ' + write_env['bmcImageDigest'] + '\n' if REDFISH else ''))
print('evidence: sanitized summary ' + str(ev / 'summary.json'))
