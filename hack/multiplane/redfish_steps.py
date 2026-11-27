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
  ipam-watch          synced DHCP only, in the background for the whole run:
                      sample IPAM's claims and allocations in the provider
                      project, so dhcp-synced can show what IPAM held during
                      each boot after the claims are gone
  dhcp-synced         synced DHCP: boot addresses, reservations, Kea leases
                      and IPAM's allocations agree, and nothing remains held
"""
import base64
import datetime
import hashlib
import json
import os
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
IDENTITY, IDENTITY_SECRET = 'fleet-site-worker-identity', 'fleet-site-worker-media'
BMC_IP = '10.96.0.81'
BOOT_IP = '10.96.0.80'
# The workstation as lab Pods and guests reach it (the Colima host gateway),
# where the central media service listens on loopback.
MEDIA_HOST = '192.168.5.2'
# The report relay on the lab bridge, where guests post their reports.
REPORT_RELAY = '192.168.77.1'
MEDIA_PORT = 28443


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


def credential_name(device):
    """The only name a Device's credential Secret may have at the site."""
    return 'bmc-' + hashlib.sha256(device.encode()).hexdigest()[:20]


def synced(s):
    """Whether this run's lab DHCP is synchronized (run.sh --synced-dhcp)."""
    p = s / 'lab-dhcp'
    return p.exists() and p.read_text().strip() == 'synced'


def enroll_dhcp(s, d):
    """The synchronizer's token and the feed's TLS key, generated per run and
    never committed: Secrets the site DHCP layer mounts."""
    (d / 'dhcp-sync.token').write_text(secrets.token_hex(32) + '\n')
    subprocess.run(['openssl', 'req', '-x509', '-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:prime256v1', '-nodes', '-keyout', str(d / 'dhcp-feed.key'),
                    '-out', str(d / 'dhcp-feed.crt'), '-days', '2', '-subj', '/CN=fleet-site-dhcp-feed',
                    '-addext', 'subjectAltName=DNS:fleet-site-dhcp-feed.fleet-site.svc'], check=True, capture_output=True)
    for p in d.iterdir():
        p.chmod(0o600)
    kubectl(s, '-n', SITE, 'create', 'secret', 'generic', 'fleet-site-dhcp-sync', '--from-file=token=%s' % (d / 'dhcp-sync.token'))
    # The certificate is self-signed, so it is also the CA the synchronizer
    # trusts.
    kubectl(s, '-n', SITE, 'create', 'secret', 'generic', 'fleet-site-dhcp-feed-tls', '--from-file=tls.crt=%s' % (d / 'dhcp-feed.crt'),
            '--from-file=tls.key=%s' % (d / 'dhcp-feed.key'), '--from-file=ca.crt=%s' % (d / 'dhcp-feed.crt'))


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
    # The central media service's TLS key, also per run. It runs on the
    # workstation, which lab Pods and guests reach at the runtime's host
    # gateway. The lab BMC, the workers and the installed OS verify it
    # against this certificate.
    subprocess.run(['openssl', 'req', '-x509', '-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:prime256v1', '-nodes', '-keyout', str(d / 'media-tls.key'),
                    '-out', str(d / 'media-tls.crt'), '-days', '2', '-subj', '/CN=fleet-lab-media', '-addext', 'subjectAltName=IP:%s,IP:%s,IP:127.0.0.1,IP:::1' % (MEDIA_HOST, REPORT_RELAY)],
                   check=True, capture_output=True)
    # Site workers authenticate to the central media service with client
    # certificates from a CA generated per run: the cell's workers, and one
    # other identity the service must refuse.
    cell = (s / 'prefix').read_text().strip() + '-cell'
    subprocess.run(['openssl', 'req', '-x509', '-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:prime256v1', '-nodes', '-keyout', str(d / 'media-worker-ca.key'),
                    '-out', str(d / 'media-worker-ca.crt'), '-days', '2', '-subj', '/CN=fleet-lab-media-workers', '-addext', 'basicConstraints=critical,CA:TRUE',
                    '-addext', 'keyUsage=critical,keyCertSign'], check=True, capture_output=True)
    for name, identity in (('media-worker', 'fleet-site-worker:' + cell), ('media-worker-other', 'fleet-site-worker:another-cell')):
        subprocess.run(['openssl', 'req', '-new', '-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:prime256v1', '-nodes', '-keyout', str(d / (name + '.key')),
                        '-out', str(d / (name + '.csr')), '-subj', '/CN=' + identity], check=True, capture_output=True)
        ext = d / (name + '.ext')
        ext.write_text('extendedKeyUsage=clientAuth\nkeyUsage=critical,digitalSignature\n')
        subprocess.run(['openssl', 'x509', '-req', '-in', str(d / (name + '.csr')), '-CA', str(d / 'media-worker-ca.crt'), '-CAkey', str(d / 'media-worker-ca.key'),
                        '-CAcreateserial', '-days', '2', '-extfile', str(ext), '-out', str(d / (name + '.crt'))], check=True, capture_output=True)
    env_path = s / 'gitops-environment.json'
    env = json.loads(env_path.read_text())
    env['mediaCA'] = base64.b64encode((d / 'media-tls.crt').read_bytes()).decode()
    env_path.write_text(json.dumps(env, indent=2) + '\n')
    spki = subprocess.run(['openssl', 'x509', '-in', str(d / 'bmc-tls.crt'), '-pubkey', '-noout'], check=True, capture_output=True).stdout
    der = subprocess.run(['openssl', 'pkey', '-pubin', '-outform', 'der'], input=spki, check=True, capture_output=True).stdout
    pin = hashlib.sha256(der).hexdigest()
    (d / 'bmc-pin').write_text(pin + '\n')
    for p in d.iterdir():
        p.chmod(0o600)
    kubectl(s, '-n', SITE, 'create', 'secret', 'generic', SECRET, '--from-file=username=%s' % (d / 'bmc-username'), '--from-file=password=%s' % (d / 'bmc-password.token'))
    kubectl(s, '-n', SITE, 'create', 'secret', 'tls', TLS_SECRET, '--cert=%s' % (d / 'bmc-tls.crt'), '--key=%s' % (d / 'bmc-tls.key'))
    kubectl(s, '-n', SITE, 'create', 'secret', 'generic', 'fleet-site-media-ca', '--from-file=ca.crt=%s' % (d / 'media-tls.crt'))
    if synced(s):
        enroll_dhcp(s, d)
    # The site worker's own media service identity, one Secret per site
    # worker identity, apart from every BMC credential; and another site's,
    # which no worker here may read.
    if kubectl(s, 'get', 'namespace', IDENTITY, check=False).returncode != 0:
        kubectl(s, 'create', 'namespace', IDENTITY)
    for name, files in ((IDENTITY_SECRET, 'media-worker'), (IDENTITY_SECRET + '-another-cell', 'media-worker-other')):
        kubectl(s, '-n', IDENTITY, 'create', 'secret', 'tls', name, '--cert=%s' % (d / (files + '.crt')), '--key=%s' % (d / (files + '.key')))
    # One credential Secret per Device, by the BMC discovery contract.
    if kubectl(s, 'get', 'namespace', CREDENTIALS, check=False).returncode != 0:
        kubectl(s, 'create', 'namespace', CREDENTIALS)
    # The enrollment this run's workers get: the synced graph's, with its
    # expected DHCP instance, under --synced-dhcp.
    graph = 'config/site/redfish/synced/graph/kustomization.yaml' if synced(s) else 'config/site/redfish/graph/kustomization.yaml'
    enrollment = json.loads(next(l for l in pathlib.Path(graph).read_text().splitlines() if l.strip().startswith("value: '{")).split("'")[1])
    # The boot NIC is a Device inventory fact; the lab's VM definitions
    # record it.
    lab_macs = {t['system']: t['mac'] for t in json.loads(pathlib.Path('hack/redfish-lab/targets.json').read_text())}
    fixture = {}
    for target in enrollment['targets']:
        namespace, name = target['device'].split('/')
        # The Secret's name derives from the Device (site.CredentialSecretName),
        # so each worker can be granted get on exactly its Device's.
        secret = {'apiVersion': 'v1', 'kind': 'Secret', 'metadata': {'name': credential_name(target['device']), 'namespace': CREDENTIALS, 'labels': {'fleet.datumapis.com/bmc-credential': 'true'},
                  'annotations': {'fleet.datumapis.com/device-project': 'provider', 'fleet.datumapis.com/device-namespace': namespace, 'fleet.datumapis.com/device-name': name}},
                  'stringData': {'username': (d / 'bmc-username').read_text().strip(), 'password': (d / 'bmc-password.token').read_text().strip(), 'tlsPublicKeySHA256': pin}}
        kubectl(s, 'apply', '-f', '-', input=json.dumps(secret))
        # Only target A is a Fleet Device. Target B is enrolled at the site to
        # prove isolation and is never inventory: the lab serves both targets
        # from one BMC with one TLS key, and custody refuses a key that
        # another active Device also records.
        if target['device'] == 'fleet-provider/synthetic-device-central-01':
            fixture[target['device']] = {'bmcEndpoint': target['endpoint'], 'systemUUID': target['systemUUID'], 'bootMACAddress': lab_macs[target['systemID']], 'bmcPublicKeySHA256': pin, 'disks': ['disk-0']}
    write(d / 'fixture-enrollment.json', fixture)
    write(s / 'evidence' / 'redfish-enrollment.json', {
        'observedAt': now(), 'credentialSecrets': [CREDENTIALS + '/' + credential_name(t['device']) for t in enrollment['targets']], 'username': 'fleet-lab-operator',
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
        pin = (d / 'bmc-pin').read_text().strip()

        def probe(name, creds, system, uuid, want, pin=pin):
            r = subprocess.run([str(s / 'fleet-multiplane-host'), 'redfish-probe', '--endpoint', 'https://127.0.0.1:%d' % port, '--credentials', str(creds),
                                '--system', system, '--uuid', uuid, '--pin', pin], text=True, capture_output=True, timeout=120)
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
        # Another key's digest: the TLS handshake fails before any request,
        # so the BMC audit gains nothing.
        other = pin[:-1] + ('0' if pin[-1] != '0' else '1')
        seen = len(audit(s))
        probe('a TLS key other than the pin', good, TARGET_A['system'], TARGET_A['uuid'], 13, pin=other)
        if len(audit(s)) != seen:
            die('a probe with the wrong pin reached the BMC')
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
    print('redfish-discovery-faults: real emulator discovery selected each enrolled System exactly; wrong credential, swapped or unknown identity and an unadvertised ResetType were refused with GETs only; a TLS key other than the pin reached no request')


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
                                                          'guestStateAfter': runtime(s, 'virsh', '--connect', LIBVIRT, 'domstate', TARGET_A['domain']).stdout.strip(),
                                                          'note': 'the BMC adapter re-reads NVRAM and libvirt state; the worker tolerates a bounded BMC outage while observing'})
    print('redfish-bmc-interrupt: lab BMC Pod force-deleted mid-install and replaced; the guest stayed active and the operation stayed unfinished')


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
    # The flux group runs no release, so target A keeps its installed disk and
    # its disk boot; only the groups that wipe it can require it zeroed and off.
    released = harness.GROUP != 'flux'
    record['targetA']['checkedWiped'] = released
    if released and a['diskSHA256'] != a['zeroDiskSHA256']:
        die('target A disk is not all zeroes after the final verified cleanup')
    if released and record['runningDomains']:
        die('a lab target is still running: %s' % record['runningDomains'])
    if not released and TARGET_B['domain'] in record['runningDomains']:
        die('target B is running: %s' % record['runningDomains'])
    write(s / 'evidence' / 'redfish-isolation.json', record)
    (s / 'evidence' / 'redfish-audit.jsonl').write_text(''.join(json.dumps(e, sort_keys=True) + '\n' for e in entries))
    print('redfish-isolation: target B never started, never received a Redfish effect and its disk is unchanged; ' +
          ('target A disk independently hashes as all zeroes after the final cleanup' if released else 'target A stays installed: this group runs no release'))


# What the lab IPAM pool can allocate: 192.168.77.0/24 less 100 leading and
# 56 trailing reserved addresses (milo.py).
LAB_IPAM_FIRST, LAB_IPAM_LAST = '192.168.77.100', '192.168.77.199'
LAB_POOL = {'total': '256', 'allocated': '156', 'available': '100'}
IPAM_KINDS = 'ipclaims.ipam.miloapis.com,ipallocations.ipam.miloapis.com'


def ipam_sample(s):
    """One read, through the provider project, of IPAM's boot address claims
    and the allocations it holds for them."""
    try:
        r = subprocess.run(['kubectl', '--kubeconfig', str(s / 'provider' / 'bootstrap-project.json'), '--request-timeout=10s', '-n', 'fleet-provider',
                            'get', IPAM_KINDS, '-o', 'json'], text=True, capture_output=True, timeout=30)
    except subprocess.TimeoutExpired:
        return {'error': 'timed out'}
    if r.returncode:
        return {'error': r.stderr.strip()[-200:]}
    items = json.loads(r.stdout)['items']
    status = lambda i: i.get('status') or {}
    return {'claims': sorted(({'name': i['metadata']['name'], 'phase': status(i).get('phase'), 'address': status(i).get('address'),
                               'allocation': (status(i).get('boundAllocationRef') or {}).get('name')} for i in items if i['kind'] == 'IPClaim'), key=lambda c: c['name']),
            'allocations': sorted(({'name': i['metadata']['name'], 'claim': ((i.get('spec') or {}).get('claimRef') or {}).get('name'), 'cidr': status(i).get('allocatedCIDR'),
                                    'phase': status(i).get('phase')} for i in items if i['kind'] == 'IPAllocation'), key=lambda a: a['name'])}


def ipam_watch(s):
    """Sample IPAM every two seconds until the acceptance script that started
    this exits. Consecutive equal samples collapse into one state that held
    from its first to its last sample."""
    parent, path, states = os.getppid(), s / 'redfish' / 'ipam-observations.json', []
    while os.getppid() == parent:
        at_ = now()
        try:
            sample = ipam_sample(s)
        except Exception as e:  # recorded, never fatal: a gap is visible as an error sample
            sample = {'error': repr(e)[-200:]}
        if states and states[-1]['sample'] == sample:
            states[-1]['to'] = at_
        else:
            states.append({'from': at_, 'to': at_, 'sample': sample})
        try:
            part = path.with_suffix('.part')
            part.write_text(json.dumps(states))
            part.replace(path)
        except OSError:
            pass  # the next write carries every state; dhcp-synced checks freshness
        time.sleep(2)


def ipam_held(sample, claim, address):
    """The allocation name if the sample shows the claim bound to address and
    an allocation that names the claim back and holds exactly address/32."""
    c = next((c for c in sample.get('claims', []) if c['name'] == claim), None)
    if not c or c['phase'] != 'Bound' or c['address'] != address or not c['allocation']:
        return None
    a = next((a for a in sample['allocations'] if a['name'] == c['allocation']), None)
    return a['name'] if a and a['claim'] == claim and a['cidr'] == address + '/32' else None


def ipam_record(states, claim, address, start, end):
    """IPAM's record of the claim bound to address, and of the allocation that
    claim names, held over start..end (epoch seconds). Either a sample inside
    the window shows it, or, when IPAM could not be read inside the window,
    samples just before and just after it show the same allocation with only
    failed reads between: a claim is immutable and its allocation goes only
    with it. None without one."""
    def record(allocation, first, last):
        return {'claim': claim, 'address': address, 'allocation': allocation, 'allocationClaimRef': claim, 'allocatedCIDR': address + '/32',
                'observedFrom': first['from'], 'observedTo': last['to']}
    before = None
    for state in states:
        sample, began, ended = state['sample'], at(state['from']).timestamp(), at(state['to']).timestamp()
        held = ipam_held(sample, claim, address) if 'claims' in sample else None
        if ended < start:
            before = (held, state) if held else (None if 'claims' in sample else before)
            continue
        if began > end:
            if held and before and before[0] == held:
                return record(held, before[1], state)
            return None
        if held:
            return record(held, state, state)
        if 'claims' in sample:
            # IPAM answered inside the window without the allocation.
            before = None
    return None


def lease_owners(granted, windows):
    """For each lease Kea granted (address, MAC, grant time), the operations
    whose boot window contains it at that address. windows maps an operation
    name to (address, start, end)."""
    return [(lease, [n for n, (address, start, end) in windows.items() if address == lease[0] and start <= lease[2] <= end]) for lease in granted]


def at(text):
    """A Go RFC 3339 time as an aware datetime, to compare report and boot times."""
    text = text.replace('Z', '+00:00')
    head, _, rest = text.partition('.')
    if rest:
        digits = rest[:len(rest) - len(rest.lstrip('0123456789'))]
        zone = rest[len(digits):]
        text = head + '.' + (digits + '000000')[:6] + zone
    return datetime.datetime.fromisoformat(text)


def dhcp_synced(s):
    """Synchronized lab DHCP: every installation and wipe carried a boot address
    from its own IPAM claim, the site published its reservation for the boot
    NIC only while its boot was open, Kea answered the firmware with that
    address, IPAM held it for that operation's claim meanwhile, and the lab
    network had no DHCP of its own."""
    if not synced(s):
        die('dhcp-synced runs only with --synced-dhcp')
    bootstrap = s / 'provider' / 'bootstrap-project.json'
    def provider(*args):
        r = subprocess.run(['kubectl', '--kubeconfig', str(bootstrap), '--request-timeout=30s', '-n', 'fleet-provider', *args, '-o', 'json'], text=True, capture_output=True, timeout=60)
        if r.returncode:
            die('provider read failed: %s' % r.stderr.strip()[-300:])
        return json.loads(r.stdout)['items']
    mac = json.loads(pathlib.Path('hack/redfish-lab/targets.json').read_text())[0]['mac'].lower()
    first, last = [tuple(int(x) for x in a.split('.')) for a in (LAB_IPAM_FIRST, LAB_IPAM_LAST)]
    ops = []
    for kind in ('deviceinstallations', 'devicewipes'):
        for o in provider('get', kind):
            network = o['spec'].get('bootNetwork')
            if not network:
                die('%s %s carries no boot address' % (o['kind'], o['metadata']['name']))
            claim = network['addressClaim']
            if claim.get('apiGroup') != 'ipam.miloapis.com' or claim.get('kind') != 'IPClaim':
                die('%s %s names a %s/%s boot address claim, not an IPAM IPClaim' % (o['kind'], o['metadata']['name'], claim.get('apiGroup'), claim.get('kind')))
            if claim['name'] != o['metadata']['name'] + '-boot':
                die('%s %s names a claim other than its own' % (o['kind'], o['metadata']['name']))
            if not first <= tuple(int(x) for x in network['address'].split('.')) <= last:
                die('%s %s boot address %s is outside the lab IPAM range' % (o['kind'], o['metadata']['name'], network['address']))
            ops.append({'kind': o['kind'], 'name': o['metadata']['name'], 'uid': o['metadata']['uid'], 'phase': o.get('status', {}).get('phase'),
                        'address': network['address'], 'claim': network['addressClaim']['name'],
                        'snapshotMAC': (o.get('status', {}).get('snapshot') or {}).get('bootMACAddress', '').lower()})
    kinds = [o['kind'] for o in ops]
    if kinds.count('DeviceInstallation') < 2 or kinds.count('DeviceWipe') < 2:
        die('expected at least two installations and two wipes, got %s' % kinds)
    # Claim names derive from operation names, which differ per operation.
    if len({o['claim'] for o in ops}) != len(ops):
        die('two operations shared one boot address claim')
    leftover = provider('get', 'ipclaims')
    if leftover:
        die('boot address claims remain after every release: %s' % [c['metadata']['name'] for c in leftover])
    held = provider('get', 'ipallocations')
    if held:
        die('IPAM still holds allocations after every release: %s' % [(a['metadata']['name'], (a.get('spec', {}).get('claimRef') or {}).get('name')) for a in held])
    # Only the pool's own reservations hold addresses now: no release leaked.
    r = subprocess.run(['kubectl', '--kubeconfig', str(bootstrap), '--request-timeout=30s', 'get', 'ippool', 'lab-provisioning', '-o', 'json'], text=True, capture_output=True, timeout=60)
    if r.returncode:
        die('IPAM pool read failed: %s' % r.stderr.strip()[-300:])
    capacity = (json.loads(r.stdout).get('status') or {}).get('capacity') or {}
    if capacity != LAB_POOL:
        die('the lab IPAM pool reports %s, want %s: only its reservations should hold addresses' % (capacity, LAB_POOL))
    states = json.loads((s / 'redfish' / 'ipam-observations.json').read_text()) if (s / 'redfish' / 'ipam-observations.json').exists() else []
    if not any('claims' in st['sample'] for st in states):
        die('no IPAM sample was recorded during the run')
    # The sampler ran to the end: its last state is recent.
    if time.time() - at(states[-1]['to']).timestamp() > 60:
        die('the IPAM sampler stopped at %s; its last sample was %s' % (states[-1]['to'], states[-1]['sample']))

    def run(*command, container='libvirt'):
        return kubectl(s, '-n', SITE, 'exec', 'deploy/fleet-site-runtime', '-c', container, '--', *command, check=False, timeout=120)
    device = harness.device_dir()
    applied = [json.loads(l) for l in run('cat', '/var/lib/fleet-boot/dhcp-applied.log').stdout.splitlines() if l.strip()]
    if not applied or {r['instance'] for r in applied} != {'lab-kea'}:
        die('the site retained no applied report, or one from an instance other than lab-kea')
    if not all(r['feed'].get('issued', '0001-01-01T00:00:00Z') != '0001-01-01T00:00:00Z' for r in applied):
        die('a retained report has no feed issue time')
    def serves(report, address):
        return any(r['mac'] == mac and r['address'] == address for r in report['feed']['reservations'])
    leases = run('cat', '/var/lib/kea/dhcp4.leases', container='kea').stdout.splitlines()
    header = leases[0].split(',') if leases else []
    rows = [dict(zip(header, l.split(','))) for l in leases[1:] if l.strip()]
    if not rows or not {'address', 'hwaddr', 'expire', 'valid_lifetime'} <= set(header):
        die('Kea recorded no leases')
    answered = {(r.get('address'), r.get('hwaddr', '').lower()) for r in rows}
    # When Kea granted each lease: its expiry less its lifetime, in seconds.
    # A row with no lifetime records a released or deleted lease. Its grant
    # time is unknown, so it is only counted among the answers; the row that
    # granted the lease carries the grant time.
    granted = [(r['address'], r['hwaddr'].lower(), int(r['expire']) - int(r['valid_lifetime'])) for r in rows if int(r['valid_lifetime'])]
    windows = {}
    # An operation asked for a network boot once its power-on was journaled,
    # whether or not the boot server then served it.
    network_boots = {o['operation'] for o in journal(s) if any(n in ('installer-power-on', 'wipe-power-on') for n, _ in o['steps'])}
    record = {'observedAt': now(), 'bootMAC': mac, 'operations': []}
    for o in ops:
        boot = run('cat', '%s/boot/%s.json' % (device, o['uid']))
        entry = {k: o[k] for k in ('kind', 'name', 'phase', 'address')}
        if boot.returncode:
            # Admitted but never run at the site, or refused before any
            # effect: no reservation was ever published for it.
            if o['phase'] not in ('Failed', 'Cancelled'):
                die('%s %s ended %s with no boot admission at the site' % (o['kind'], o['name'], o['phase']))
            entry['published'] = False
            record['operations'].append(entry)
            continue
        b = json.loads(boot.stdout)
        if b.get('mac') != mac or b.get('address') != o['address'] or o['snapshotMAC'] != mac:
            die('%s %s published %s at %s, want the inventory boot MAC %s at %s' % (o['kind'], o['name'], b.get('mac'), b.get('address'), mac, o['address']))
        opened = at(b['created'])
        served = [r for r in applied if at(r['at']) > opened and serves(r, o['address'])]
        if not served:
            die('%s %s: Kea never reported serving its reservation after its boot opened' % (o['kind'], o['name']))
        withdrawn = [r for r in applied if at(r['at']) > at(served[0]['at']) and not serves(r, o['address'])]
        if not withdrawn:
            die('%s %s: Kea never reported its reservation withdrawn' % (o['kind'], o['name']))
        delivered = run('sh', '-c', 'cat /var/lib/fleet-boot/%s/boot-delivery.log 2>/dev/null; true' % harness.key(o['uid'])).stdout
        # Kea leased this operation's address to the boot NIC inside this
        # operation's own window: after its boot opened and before Kea
        # withdrew its reservation, with five seconds for clock rounding.
        start, end = opened.timestamp() - 5, at(withdrawn[0]['at']).timestamp() + 5
        leased = [g for a, m, g in granted if a == o['address'] and m == mac and start <= g <= end]
        # IPAM allocated the address to this operation's own claim while its
        # boot was open, as sampled by ipam-watch.
        ipam = ipam_record(states, o['claim'], o['address'], start, end)
        if not ipam:
            die('%s %s: IPAM recorded no allocation of %s to claim %s during its boot' % (o['kind'], o['name'], o['address'], o['claim']))
        windows[o['name']] = (o['address'], start, end)
        entry.update(published=True, servedAt=served[0]['at'], withdrawnAt=withdrawn[0]['at'], networkBoot=o['uid'] in network_boots,
                     bootScriptDelivered='admitted boot.ipxe' in delivered, keaLeasesInWindow=len(leased), ipam=ipam)
        if entry['networkBoot'] and not leased:
            die('%s %s powered on for a network boot, but Kea leased %s no address %s during its boot' % (o['kind'], o['name'], mac, o['address']))
        if entry['bootScriptDelivered'] and not entry['networkBoot']:
            die('%s %s delivered a boot script with no journaled network boot' % (o['kind'], o['name']))
        record['operations'].append(entry)
    if applied[-1]['feed']['reservations']:
        die('Kea still serves a reservation after the last operation ended')
    # Every lease Kea granted falls in the boot window of an operation whose
    # claim IPAM held that address for, so Kea served only IPAM's allocations.
    for (address, hwaddr, grant), owners in lease_owners(granted, windows):
        if not owners:
            die('Kea leased %s to %s at %d outside every operation boot window with an IPAM allocation of it' % (address, hwaddr, grant))
    booted = [e for e in record['operations'] if e.get('networkBoot')]
    if sum(e['kind'] == 'DeviceWipe' for e in booted) < 2 or sum(e['kind'] == 'DeviceInstallation' for e in booted) < 2:
        die('expected Kea-leased network boots of two installations and two wipes, got %s' % [(e['kind'], e['name']) for e in booted])
    static = {t['ip'] for t in json.loads(pathlib.Path('hack/redfish-lab/targets.json').read_text())}
    if any(a in static for a, _ in answered) or any(not first <= tuple(int(x) for x in a.split('.')) <= last for a, _ in answered if a):
        die('Kea leased an address outside the lab IPAM range: %s' % sorted(answered))
    network = runtime(s, 'virsh', '--connect', harness.LIBVIRT_URI, 'net-dumpxml', 'fleet-lab-pxe').stdout
    dnsmasq = run('sh', '-c', 'for p in /proc/[0-9]*/comm; do cat "$p" 2>/dev/null; done | grep -c dnsmasq; true').stdout.strip()
    if '<dhcp' in network or dnsmasq not in ('', '0'):
        die('the lab network still runs its own DHCP')
    record.update(appliedReports=len(applied), keaLeases=len(rows), keaLeasesAttributed=len(granted), labNetworkDHCP=False, dnsmasqProcesses=0,
                  claimsRemaining=0, allocationsRemaining=0, distinctClaims=len(ops), poolCapacityAfter=capacity,
                  ipamSamples={'states': len(states), 'errors': sum('error' in st['sample'] for st in states), 'from': states[0]['from'], 'to': states[-1]['to']})
    write(s / 'evidence' / 'redfish-dhcp.json', record)
    print('redfish-dhcp-synced: %d operations each carried their own IPAM boot address; Kea served each reservation only after its boot opened and reported it withdrawn; '
          'firmware leased each operation\'s address from Kea during its own boot (%d network boots, two installations and two wipes); IPAM allocated every leased address '
          'to that operation\'s own claim during its boot; the lab network had no DHCP; no claim or allocation remains'
          % (len(ops), len(booted)))


def audit_mark(s):
    """Record the BMC audit's length for a later audit-unchanged."""
    (s / 'redfish' / 'audit-mark').write_text('%d\n' % len(audit(s)))


def audit_unchanged(s):
    """No request after the mark carried an effect."""
    mark = int((s / 'redfish' / 'audit-mark').read_text())
    effects = mutating(audit(s)[mark:])
    if effects:
        die('the BMC received an effect: %s' % [{k: e.get(k) for k in ('method', 'path', 'resetType')} for e in effects])


if __name__ == '__main__':
    step, state = sys.argv[1], pathlib.Path(sys.argv[2])
    {'enroll': enroll, 'baseline': baseline, 'discovery-faults': discovery_faults, 'arm-lost-response': arm_lost_response,
     'lost-response': lost_response, 'bmc-interrupt': bmc_interrupt, 'isolation': isolation,
     'audit-mark': audit_mark, 'audit-unchanged': audit_unchanged, 'ipam-watch': ipam_watch, 'dhcp-synced': dhcp_synced}[step](state)
