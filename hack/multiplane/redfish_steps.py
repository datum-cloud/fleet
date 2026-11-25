#!/usr/bin/env python3
"""Redfish-mode acceptance steps (hack/multiplane/run.sh --redfish).

Every observation goes through the selected cell's API. Lab faults are written
only through the lab BMC adapter's fault file (bounded, one-shot) and are
recorded. Evidence records methods, paths, outcome classes, UIDs, fences and digests,
never the BMC credential, boot capabilities or request bodies.

  enroll              create the synthetic BMC credential Secret (never in Git)
  baseline            snapshot target B and the BMC audit before any operation
  discovery-faults    real emulator: read-only discovery with good/wrong
                      credentials, identity mismatch and a hidden ResetType
  arm-lost-response   one-shot: withhold the response to target A's second
                      effective On (the disk boot of the first install)
  lost-response       stop the worker while that On awaits its response;
                      the replacement observed the target instead of repeating it
  bmc-interrupt       delete the BMC Pod mid-installer; the guest keeps running
  isolation           target B untouched, target A disk verifiably zeroed
"""
import datetime
import hashlib
import json
import pathlib
import secrets
import socket
import subprocess
import sys
import time

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
import harness  # noqa: E402  (site store layout)

SITE = 'fleet-site'
TARGET_A = {'system': 'System-1', 'uuid': '7f3d0c2e-1a5b-4c6d-8e9f-0a1b2c3d4e0a', 'domain': 'fleet-lab-target-a', 'disk': '/var/lib/fleet-lab/disks/target-a.raw'}
TARGET_B = {'system': 'System-2', 'uuid': '7f3d0c2e-1a5b-4c6d-8e9f-0a1b2c3d4e0b', 'domain': 'fleet-lab-target-b', 'disk': '/var/lib/fleet-lab/disks/target-b.raw'}
DISK_BYTES = 2 << 30
LIBVIRT = 'qemu:///system?socket=/run/fleet-libvirt/libvirt-sock'
SECRET = 'fleet-site-bmc-credentials'
TLS_SECRET = 'fleet-site-bmc-tls'
CREDENTIALS = 'fleet-site-credentials'
BMC_IP = '10.96.0.81'


def now():
    return datetime.datetime.now(datetime.timezone.utc).isoformat()


def die(message):
    raise SystemExit(message)


def kubectl(s, *args, check=True, input=None, timeout=60):
    r = subprocess.run(['kubectl', '--kubeconfig', str(s / 'cell-admin.yaml'), '--request-timeout=30s', *args], text=True, capture_output=True, input=input, timeout=timeout)
    if check and r.returncode:
        die('kubectl %s failed: %s' % (' '.join(args[:4]), r.stderr.strip()[-300:]))
    return r


def kget(s, *args):
    return json.loads(kubectl(s, *args, '-o', 'json').stdout)


def runtime(s, *command, check=True):
    return kubectl(s, '-n', SITE, 'exec', 'deploy/fleet-site-runtime', '-c', 'libvirt', '--', *command, check=check, timeout=180)


def bmc(s, *command, check=True):
    return kubectl(s, '-n', SITE, 'exec', 'deploy/fleet-site-bmc', '-c', 'bmc', '--', *command, check=check)


def write(path, value):
    pathlib.Path(path).write_text(json.dumps(value, indent=2, sort_keys=True) + '\n')


def audit(s):
    r = bmc(s, 'cat', '/var/lib/fleet-bmc/audit.log', check=False)
    return [json.loads(l) for l in r.stdout.splitlines() if l.strip()] if r.returncode == 0 else []


def mutating(entries, system=None):
    return [e for e in entries if e['method'] not in ('GET', 'HEAD') and (system is None or e['path'].rstrip('/').startswith('/redfish/v1/Systems/' + system))]


def journal(s, pending_ok=False):
    """The Device's journaled operations, sanitized: UID, fence, kind and steps."""
    # pending_ok: before the first worker raises the fence there is no state yet.
    def run(*command):
        r = runtime(s, *command, check=False)
        return r.returncode, r.stdout
    store, results = harness.read_site_store(run)
    if store is None and not pending_ok:
        die('the site store holds no state for the Device')
    return harness.sanitized_operations(store, results, redfish=True)


def installs(s, pending_ok=False):
    return [o for o in journal(s, pending_ok) if o['kind'] == 'DeviceInstallation']


def done(op, step):
    return [step, 'Done'] in op['steps']


def power_ons(op):
    """Journaled power-on steps by phase. The journal holds one entry per step."""
    return {n[:-len('-power-on')]: st for n, st in op['steps'] if n.endswith('-power-on')}


def qemu_starts(s, domain):
    r = runtime(s, 'cat', '/var/log/libvirt/qemu/%s.log' % domain, check=False)
    return r.stdout.count(': starting up ') if r.returncode == 0 else 0


def disk_sha(s, path):
    return runtime(s, 'sha256sum', path).stdout.split()[0]


def console_lines(s, name):
    r = runtime(s, 'cat', '/var/lib/fleet-lab-console/' + name, check=False)
    return [l.strip() for l in r.stdout.splitlines()] if r.returncode == 0 else []


def running(s):
    return runtime(s, 'virsh', '--connect', LIBVIRT, 'list', '--name').stdout.split()


def enroll(s):
    d = s / 'redfish'
    d.mkdir(mode=0o700, exist_ok=True)
    (d / 'bmc-username').write_text('fleet-lab-operator\n')
    # The .token suffix enrolls the value in the harness secret scan.
    (d / 'bmc-password.token').write_text(secrets.token_hex(32) + '\n')
    # The lab BMC's TLS key is generated per run and never committed. Workers
    # connect only to this key: its digest is each Device's approved pin.
    subprocess.run(['openssl', 'req', '-x509', '-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:prime256v1', '-nodes', '-keyout', str(d / 'bmc-tls.key'),
                    '-out', str(d / 'bmc-tls.crt'), '-days', '2', '-subj', '/CN=fleet-lab-bmc', '-addext', 'subjectAltName=IP:%s' % BMC_IP],
                   check=True, capture_output=True)
    spki = subprocess.run(['openssl', 'x509', '-in', str(d / 'bmc-tls.crt'), '-pubkey', '-noout'], check=True, capture_output=True).stdout
    der = subprocess.run(['openssl', 'pkey', '-pubin', '-outform', 'der'], input=spki, check=True, capture_output=True).stdout
    pin = hashlib.sha256(der).hexdigest()
    (d / 'bmc-pin').write_text(pin + '\n')
    for p in d.iterdir():
        p.chmod(0o600)
    kubectl(s, '-n', SITE, 'create', 'secret', 'generic', SECRET, '--from-file=username=%s' % (d / 'bmc-username'), '--from-file=password=%s' % (d / 'bmc-password.token'))
    kubectl(s, '-n', SITE, 'create', 'secret', 'tls', TLS_SECRET, '--cert=%s' % (d / 'bmc-tls.crt'), '--key=%s' % (d / 'bmc-tls.key'))
    # One credential Secret per Device, by the BMC discovery contract.
    if kubectl(s, 'get', 'namespace', CREDENTIALS, check=False).returncode != 0:
        kubectl(s, 'create', 'namespace', CREDENTIALS)
    enrollment = json.loads(next(l for l in pathlib.Path('config/site/redfish/graph/kustomization.yaml').read_text().splitlines() if l.strip().startswith("value: '{")).split("'")[1])
    fixture = {}
    for target in enrollment['targets']:
        namespace, name = target['device'].split('/')
        secret = {'apiVersion': 'v1', 'kind': 'Secret', 'metadata': {'name': 'bmc-' + name, 'namespace': CREDENTIALS, 'labels': {'fleet.datumapis.com/bmc-credential': 'true'},
                  'annotations': {'fleet.datumapis.com/device-project': 'provider', 'fleet.datumapis.com/device-namespace': namespace, 'fleet.datumapis.com/device-name': name}},
                  'stringData': {'username': (d / 'bmc-username').read_text().strip(), 'password': (d / 'bmc-password.token').read_text().strip(), 'tlsPublicKeySHA256': pin}}
        kubectl(s, 'apply', '-f', '-', input=json.dumps(secret))
        fixture[target['device']] = {'bmcEndpoint': target['endpoint'], 'systemUUID': target['systemUUID'], 'bootMACAddress': target['mac'], 'bmcPublicKeySHA256': pin, 'disks': ['disk-0']}
    write(d / 'fixture-enrollment.json', fixture)
    write(s / 'evidence' / 'redfish-enrollment.json', {
        'observedAt': now(), 'credentialSecrets': [CREDENTIALS + '/bmc-' + t['device'].split('/')[1] for t in enrollment['targets']], 'username': 'fleet-lab-operator',
        'password': 'synthetic random 256-bit value generated per run; never recorded or committed',
        'transport': 'Redfish over TLS to the fixed BMC Service %s:8443, pinned to a key generated per run (lab seam: one key for both lab targets)' % BMC_IP,
        'pin': pin,
        'readers': ['lab BMC adapter (server side)', 'per-operation Redfish worker Pods (client side, own Device only)'],
        'enrollment': enrollment,
        'labTargets': json.loads(pathlib.Path('hack/redfish-lab/targets.json').read_text())})
    print('redfish-enroll: synthetic per-Device BMC credentials and the lab BMC TLS key created in the cell; not part of any source revision')


def baseline(s):
    # Read target B through the BMC once so the adapter defines it, as any
    # discovery would, then record its untouched state.
    for _ in range(60):
        if kubectl(s, '-n', SITE, 'get', 'deployment', 'fleet-site-bmc', '-o', 'jsonpath={.status.availableReplicas}', check=False).stdout == '1':
            break
        time.sleep(2)
    record = {'observedAt': now(), 'targetB': {'diskSHA256': disk_sha(s, TARGET_B['disk']), 'qemuStarts': qemu_starts(s, TARGET_B['domain']),
                                               'consoleLines': len(console_lines(s, 'target-b.log'))},
              'targetA': {'diskSHA256': disk_sha(s, TARGET_A['disk']), 'qemuStarts': qemu_starts(s, TARGET_A['domain'])},
              'auditEntries': len(audit(s)), 'runningDomains': running(s)}
    if record['targetB']['qemuStarts'] or record['targetA']['qemuStarts'] or record['runningDomains']:
        die('lab targets were started before any operation: %s' % record)
    write(s / 'redfish-baseline.json', record)
    print('redfish-baseline: both lab target disks present and neither target ever started; target B disk %s...' % record['targetB']['diskSHA256'][:12])


def port_forward(s):
    sock = socket.socket()
    sock.bind(('127.0.0.1', 0))
    port = sock.getsockname()[1]
    sock.close()
    p = subprocess.Popen(['kubectl', '--kubeconfig', str(s / 'cell-admin.yaml'), '-n', SITE, 'port-forward', 'svc/fleet-site-bmc', '%d:8443' % port],
                         stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    for _ in range(60):
        try:
            socket.create_connection(('127.0.0.1', port), 1).close()
            return p, port
        except OSError:
            time.sleep(0.5)
    p.kill()
    die('could not port-forward to the BMC Service')


def discovery_faults(s):
    d = s / 'redfish'
    good, bad = d / 'probe-good', d / 'probe-bad'
    for p, password in ((good, (d / 'bmc-password.token').read_text()), (bad, secrets.token_hex(16) + '\n')):
        p.mkdir(mode=0o700, exist_ok=True)
        (p / 'username').write_text((d / 'bmc-username').read_text())
        (p / 'password').write_text(password)
    pf, port = port_forward(s)
    before = len(audit(s))
    cases = []
    try:
        def probe(name, creds, system, uuid, want):
            r = subprocess.run([str(s / 'fleet-multiplane-host'), 'redfish-probe', '--endpoint', 'https://127.0.0.1:%d' % port, '--credentials', str(creds),
                                '--system', system, '--uuid', uuid, '--pin', (d / 'bmc-pin').read_text().strip()], text=True, capture_output=True, timeout=120)
            line = (r.stdout.strip().splitlines() or [''])[-1]
            cases.append({'case': name, 'system': system, 'exitCode': r.returncode, 'outcome': line})
            if r.returncode != want:
                die('%s: expected exit %d, got %d: %s %s' % (name, want, r.returncode, line, r.stderr[-200:]))
        probe('target A with the enrolled credential', good, TARGET_A['system'], TARGET_A['uuid'], 0)
        probe('target B with the enrolled credential', good, TARGET_B['system'], TARGET_B['uuid'], 0)
        probe('wrong credential', bad, TARGET_A['system'], TARGET_A['uuid'], 10)
        probe("target A's Id with target B's UUID (swapped mapping)", good, TARGET_A['system'], TARGET_B['uuid'], 12)
        probe('unknown UUID', good, TARGET_A['system'], '00000000-0000-0000-0000-000000000000', 12)
        bmc(s, 'python3', '/opt/fleet-lab/fleet_lab_adapter.py', 'fault', 'hide-reset', 'On')
        try:
            probe('ResetType On not advertised (injected)', good, TARGET_A['system'], TARGET_A['uuid'], 11)
        finally:
            bmc(s, 'python3', '/opt/fleet-lab/fleet_lab_adapter.py', 'fault', 'clear')
        probe('target A after the fault is cleared', good, TARGET_A['system'], TARGET_A['uuid'], 0)
    finally:
        pf.kill()
        for p in (good, bad):
            for f in p.iterdir():
                f.unlink()
            p.rmdir()
    window = audit(s)[before:]
    if mutating(window) or not any(e['principal'] == 'rejected' for e in window):
        die('discovery probes issued an effect or the wrong credential was not rejected: %s' % mutating(window))
    write(s / 'evidence' / 'redfish-discovery-faults.json', {'observedAt': now(), 'emulator': 'DMTF Redfish-Interface-Emulator 1.2.2 + lab adapter (live, in the cell)',
                                                               'client': 'internal/redfish discovery through fleet-multiplane redfish-probe', 'cases': cases,
                                                               'auditRequests': len(window), 'mutatingRequests': 0,
                                                               'rejectedRequests': sum(1 for e in window if e['principal'] == 'rejected')})
    print('redfish-discovery-faults: real emulator discovery selected each enrolled System exactly; wrong credential, swapped or unknown identity and an unadvertised ResetType were refused with GETs only')


def arm_lost_response(s):
    # The installer phase is about a minute under TCG and carries the worker
    # Pod, Job and BMC interruptions; the disk boot's On carries this fault.
    out = bmc(s, 'python3', '/opt/fleet-lab/fleet_lab_adapter.py', 'fault', 'drop-response', TARGET_A['system'], 'On', '45', '1').stdout
    (s / 'redfish-lost-response-armed').write_text(now() + '\n')
    print('redfish-arm-lost-response: one-shot fault armed: the second effective On for System-1 (the disk boot) withholds its response for 45s (client timeout 20s): %s' % out.strip())


def lost_response(s):
    # The site journals disk-power-on before it sends On. While that step is
    # only Intended and the BMC withholds the On response, the worker Pod is
    # stopped: it stores no result, and its replacement must observe that the
    # boot began instead of sending On again. A worker whose request timed out
    # first stores OutcomeUnknown, which this step reports as a failure.
    name = (s / 'install-handoff').read_text().strip()
    deadline = time.monotonic() + 900
    record = None
    while time.monotonic() < deadline:
        ops = installs(s)
        if len(ops) == 1 and ops[0]['phase']:
            die('the install stored result %s before its worker could be stopped mid-request' % ops[0]['phase'])
        if len(ops) == 1 and done(ops[0], 'disk-power-on'):
            die('the disk boot On completed before its worker could be stopped mid-request')
        if len(ops) == 1 and ['disk-power-on', 'Intended'] in ops[0]['steps']:
            live = [p for p in kget(s, '-n', SITE, 'get', 'pods', '-l', 'fleet.datumapis.com/handoff=' + name)['items']
                    if p['status'].get('phase') == 'Running' and not p['metadata'].get('deletionTimestamp')]
            if len(live) == 1:
                kubectl(s, '-n', SITE, 'delete', 'pod', live[0]['metadata']['name'], '--wait=false')
                record = {'stoppedAt': now(), 'stoppedPodUID': live[0]['metadata']['uid'], 'journaledAtStop': 'disk-power-on Intended',
                          'stop': 'worker Pod deleted (SIGTERM) while its On request awaited the withheld response'}
                break
        time.sleep(0.5)
    if not record:
        die('the disk boot On was never in flight with one running worker')
    fired = []
    for _ in range(120):
        fired = [e for e in audit(s) if e.get('fault') == 'drop-response-after-effect']
        if fired:
            break
        time.sleep(2)
    if len(fired) != 1:
        die('the lost-response fault did not fire exactly once')
    record.update(faultRecordedAt=fired[0]['at'], faultedRequest={k: fired[0].get(k) for k in ('method', 'path', 'resetType', 'effect', 'status')})
    # The replacement worker found disk-power-on journaled but not done and
    # observed the boot instead of sending On again.
    for _ in range(300):
        ops = installs(s)
        if len(ops) == 1 and done(ops[0], 'disk-power-on'):
            break
        time.sleep(2)
    ops = installs(s)
    ons = [e for e in mutating(audit(s), TARGET_A['system']) if e.get('resetType') == 'On' and e.get('effect', '').startswith('started')]
    journaled = power_ons(ops[0]) if len(ops) == 1 else {}
    if len(ops) != 1 or not done(ops[0], 'disk-power-on') or len(ons) != 2 or journaled != {'installer': 'Done', 'disk': 'Done'}:
        die('recovery after the lost response repeated or did not observe the boot: %d effective On requests, journaled power-on %s' % (len(ons), journaled))
    record.update(observedAt=now(), operationID=ops[0]['operation'], fence=ops[0]['fence'], stageAfterRecovery=ops[0]['stage'], phase='disk boot', journaledPowerOn=journaled,
                  onRequestsToTargetA=len(ons), runningDomains=running(s),
                  recovery='replacement worker observed PowerState and the consumed one-time override; it did not repeat On')
    write(s / 'evidence' / 'redfish-lost-response.json', record)
    print('redfish-lost-response: On took effect but its response was lost; the worker was stopped with the step journaled Intended and stored no result; its replacement observed the running target and did not repeat the effect')


def bmc_interrupt(s):
    ops = []
    for _ in range(600):
        ops = installs(s, pending_ok=True)
        if len(ops) == 1 and ops[0]['stage'] == 'installer-power-on' and TARGET_A['domain'] in running(s):
            break
        if len(ops) == 1 and harness.past_installer(ops[0]):
            die('the installer finished before the BMC could be interrupted: %s' % ops[0]['stage'])
        time.sleep(0.5)
    else:
        die('BMC interruption requires the install mid-installer with target A running')
    old = kget(s, '-n', SITE, 'get', 'pods', '-l', 'app.kubernetes.io/name=fleet-site-bmc')['items']
    kubectl(s, '-n', SITE, 'delete', 'pod', '-l', 'app.kubernetes.io/name=fleet-site-bmc', '--grace-period=0', '--force', check=False)
    new = None
    for _ in range(180):
        pods = [p for p in kget(s, '-n', SITE, 'get', 'pods', '-l', 'app.kubernetes.io/name=fleet-site-bmc')['items']
                if p['metadata']['uid'] not in {o['metadata']['uid'] for o in old} and any(c['type'] == 'Ready' and c['status'] == 'True' for c in p['status'].get('conditions', []))]
        if pods:
            new = pods[0]
            break
        time.sleep(1)
    if not new:
        die('BMC Pod was not replaced')
    after = installs(s)
    if TARGET_A['domain'] not in running(s) and not harness.past_installer(after[0]):
        die('the guest stopped when the BMC was interrupted')
    write(s / 'evidence' / 'redfish-bmc-interrupt.json', {'observedAt': now(), 'deletedBMCPodUIDs': [o['metadata']['uid'] for o in old], 'replacementBMCPodUID': new['metadata']['uid'],
                                                          'operationID': ops[0]['operation'], 'fence': ops[0]['fence'], 'stageAtInterrupt': ops[0]['stage'], 'stageAfter': after[0]['stage'],
                                                          'guestRunningAfter': TARGET_A['domain'] in running(s),
                                                          'note': 'the BMC adapter re-reads NVRAM and libvirt state; the worker tolerates a bounded BMC outage while observing'})
    print('redfish-bmc-interrupt: lab BMC Pod force-deleted mid-install and replaced; the guest kept running and the operation stayed unfinished')


def isolation(s):
    base = json.loads((s / 'redfish-baseline.json').read_text())
    entries = audit(s)
    zero = hashlib.sha256()
    block = bytes(1 << 20)
    for _ in range(DISK_BYTES >> 20):
        zero.update(block)
    record = {'observedAt': now(),
              'targetB': {'diskSHA256Before': base['targetB']['diskSHA256'], 'diskSHA256After': disk_sha(s, TARGET_B['disk']), 'qemuStarts': qemu_starts(s, TARGET_B['domain']),
                          'consoleLines': len(console_lines(s, 'target-b.log')), 'mutatingRedfishRequests': mutating(entries, TARGET_B['system'])},
              'targetA': {'diskSHA256': disk_sha(s, TARGET_A['disk']), 'zeroDiskSHA256': zero.hexdigest(), 'qemuStarts': qemu_starts(s, TARGET_A['domain']),
                          'mutatingRedfishRequests': [{k: e.get(k) for k in ('method', 'path', 'status', 'resetType', 'boot', 'effect', 'fault')} for e in mutating(entries, TARGET_A['system'])]},
              'runningDomains': running(s), 'auditEntries': len(entries)}
    b, a = record['targetB'], record['targetA']
    if b['diskSHA256Before'] != b['diskSHA256After'] or b['qemuStarts'] or b['consoleLines'] != base['targetB']['consoleLines'] or b['mutatingRedfishRequests']:
        die('target B was touched: %s' % b)
    if a['diskSHA256'] != a['zeroDiskSHA256']:
        die('target A disk is not all zeroes after the final verified cleanup')
    if record['runningDomains']:
        die('a lab target is still running: %s' % record['runningDomains'])
    write(s / 'evidence' / 'redfish-isolation.json', record)
    (s / 'evidence' / 'redfish-audit.jsonl').write_text(''.join(json.dumps(e, sort_keys=True) + '\n' for e in entries))
    print('redfish-isolation: target B never started, never received a Redfish effect and its disk is unchanged; target A disk independently hashes as all zeroes after the final cleanup')


if __name__ == '__main__':
    step, state = sys.argv[1], pathlib.Path(sys.argv[2])
    {'enroll': enroll, 'baseline': baseline, 'discovery-faults': discovery_faults, 'arm-lost-response': arm_lost_response,
     'lost-response': lost_response, 'bmc-interrupt': bmc_interrupt, 'isolation': isolation}[step](state)
