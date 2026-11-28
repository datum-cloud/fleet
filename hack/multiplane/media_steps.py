#!/usr/bin/env python3
"""Virtual media acceptance steps (hack/multiplane/run.sh --redfish --virtual-media).

Observations go through the selected cell's API, the provider project and
the central media service's own records. Lab faults are written only through
the lab BMC adapter's fault file and are recorded. Evidence keeps methods,
paths, outcome classes, fences and digests; never a media URL, a URL
capability or a report token.

  offer             the operator adds virtual media after network boot, with
                    the central media service and the lab's short phases, and
                    restarts the coordinator; staff record target A as
                    qualified for virtual media only (the lab stand-in for
                    commissioning)
  arm-corrupt-token one-shot: the lab BMC's next config medium carries another
                    report token than the one the media service served
  held-report       the first install booted from media and its OS posted a
                    report with the wrong token; stray reports without a
                    token or with another token were refused; nothing was
                    taken, so it ended OutcomeUnknown
  arm-add-download  one-shot: the lab BMC's next config medium lists a
                    download the operation never pinned
  held-download     the second install's report named that download, so it
                    was never verified and the install was held
  arm-lost-eject    one-shot: the BMC applies target A's next CD1 eject and
                    withholds its response past the worker's client timeout
  held-eject        the third install's report was taken, then the eject
                    response was lost: OutcomeUnknown, release never journaled
  arm-stray-source  one-shot: the lab BMC's next config medium makes the
                    installed OS post its report, with the right token, from
                    another address inside the site's ranges first
  stray-address     the fourth install recorded its IPAM-bound report address;
                    the media service refused and recorded the right token
                    from the stray address, then took the report from the
                    Machine's address
  release-proof     after the settling install: the BMC reads back both
                    devices empty and no override, the media service reports
                    no open window, transfer or reused token for the Device,
                    and denies every window
  stale-fence       the media service refuses to open a window for an
                    operation custody no longer names, and the site store
                    refuses a media window and step below the stored fence
  evidence          the shareable virtual media evidence record
"""
import base64
import datetime
import hashlib
import json
import os
import pathlib
import re
import ssl
import subprocess
import sys
import time
import urllib.error
import urllib.request

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
import harness  # noqa: E402  (site store layout)
import redfish_steps as rf  # noqa: E402

SYSTEM = 'System-1'
DEVICE_NAME = harness.DEVICE.split('/')[1]
QUALIFIED = 'fleet.datumapis.com/qualified-delivery-methods'
# Another address on the lab network, inside the site's report ranges and
# outside IPAM's pool and the static leases: another server's.
STRAY_SOURCE = '192.168.77.250'
# The site's report range, as the lab's site workers declare it.
REPORT_RANGE = '192.168.77.0/24'
STORE_ROOT = '/var/lib/fleet-local/site'
LOCAL = 'https://127.0.0.1:%d' % rf.MEDIA_PORT
# The lab's virtual media phases, shorter than the 15 and 30 minute
# defaults, as a site may set. A faster runtime, such as a CI runner's
# KVM guests, may shorten them further.
FETCH = os.environ.get('FLEET_MULTIPLANE_MEDIA_FETCH') or '5m0s'
REPORT = os.environ.get('FLEET_MULTIPLANE_MEDIA_REPORT') or '10m0s'
if not all(re.fullmatch(r'[1-9][0-9]*m[0-9]+s|[1-9][0-9]*s', w) for w in (FETCH, REPORT)):
    raise SystemExit('FLEET_MULTIPLANE_MEDIA_FETCH and FLEET_MULTIPLANE_MEDIA_REPORT must be durations such as 5m0s or 90s')


def now():
    return datetime.datetime.now(datetime.timezone.utc).isoformat()


def later(minutes):
    return (datetime.datetime.now(datetime.timezone.utc) + datetime.timedelta(minutes=minutes)).isoformat().replace('+00:00', 'Z')


def die(message):
    raise SystemExit(message)


def provider(s, *args, check=True):
    """kubectl against the provider project as its bootstrap identity."""
    r = subprocess.run(['kubectl', '--kubeconfig', str(s / 'provider' / 'bootstrap-project.json'), '--request-timeout=30s', *args],
                       text=True, capture_output=True, timeout=60)
    if check and r.returncode:
        die('provider kubectl %s failed: %s' % (' '.join(args[:3]), r.stderr.strip()[-300:]))
    return r


def media_installs(s):
    items = json.loads(provider(s, '-n', 'fleet-provider', 'get', 'deviceinstallations', '-o', 'json').stdout)['items']
    return sorted((i for i in items if i['spec'].get('delivery', {}).get('method') == 'VirtualMedia' and i.get('status', {}).get('fence')),
                  key=lambda i: i['status']['fence'])


def key(uid):
    return hashlib.sha256(uid.encode()).hexdigest()[:16]


def central(s, method, path, body=None, identity='media-worker'):
    """A request to the central media service on loopback, verifying it, as
    the cell's site worker by default; identity None presents no client
    certificate."""
    context = ssl.create_default_context(cafile=str(s / 'redfish' / 'media-tls.crt'))
    if identity:
        context.load_cert_chain(str(s / 'redfish' / (identity + '.crt')), str(s / 'redfish' / (identity + '.key')))
    data = json.dumps(body).encode() if body is not None else None
    request = urllib.request.Request(LOCAL + path, data=data, method=method, headers={'Content-Type': 'application/json'})
    try:
        with urllib.request.urlopen(request, context=context, timeout=20) as r:
            return r.status, json.loads(r.read() or b'null')
    except urllib.error.HTTPError as e:
        return e.code, None


def window_record(s, uid):
    """The central service's own record of a window, without its medium."""
    w = json.loads((s / 'media-central' / 'windows' / (key(uid) + '.json')).read_text())
    w['configMediumKept'] = bool(w.pop('configMedium', None))
    return w


def site_window(s, uid):
    """The site's digests of a window: what the report had to carry."""
    return json.loads(rf.runtime(s, 'sh', '-c', 'cat %s/devices/*/media/%s.json' % (STORE_ROOT, uid)).stdout)


def read_console(s):
    """Target A's serial console: diagnostics only. Firmware writes bytes
    that are not UTF-8, so it is decoded leniently."""
    r = subprocess.run(['kubectl', '--kubeconfig', str(s / 'cell-admin.yaml'), '--request-timeout=30s', '-n', rf.SITE, 'exec', 'deploy/fleet-site-runtime', '-c', 'libvirt', '--',
                        'cat', '/var/lib/fleet-lab-console/target-a.log'], capture_output=True, timeout=180)
    return [l.strip() for l in r.stdout.decode(errors='replace').splitlines()] if r.returncode == 0 else []


def why(s, uid):
    """What the lab saw of an install's report, for a failure message: the
    installed OS's console outcomes (never a token), the window's report
    outcomes, and the recent output of the report relay and the central
    media service."""
    console = [l for l in read_console(s) if l.startswith('fleet:')][-6:]
    relay = rf.kubectl(s, '-n', rf.SITE, 'logs', 'deploy/fleet-site-runtime', '-c', 'report-relay', '--tail', '8', check=False).stdout.strip().splitlines()
    log = s / 'media-central.log'
    central = log.read_text(errors='replace').strip().splitlines()[-8:] if log.exists() else []
    return '; console %s; report outcomes %s; relay %s; media service %s' % (console, outcomes(s, uid), relay, central)


def detail(s, op):
    """How an install ended, for a failure message: its phase, reason and
    message, the BMC's recent virtual media requests, and what the lab saw
    of its report."""
    status = op.get('status', {})
    media = summarize([e for e in rf.audit(s) if '/VirtualMedia/' in e['path']])[-6:]
    return '; ended %s %s: %s; BMC media requests %s%s' % (status.get('phase'), status.get('reason'), (status.get('message') or '')[:300], media, why(s, op['metadata']['uid']))


def steps_of(s, uid):
    return dict((n, st) for o in rf.journal(s) if o['operation'] == uid for n, st in o['steps'])


def offer(s):
    config = json.loads((s / 'acceptance.json').read_text())
    config['DeliveryMethods'] = [{'method': 'NetworkBoot'},
                                 {'method': 'VirtualMedia', 'addresses': 'DataNetwork', 'mediaService': config['MediaService'], 'fetchWindow': FETCH, 'reportWindow': REPORT}]
    (s / 'acceptance.json').write_text(json.dumps(config))
    provider(s, '-n', 'fleet-provider', 'annotate', '--overwrite', 'device', DEVICE_NAME, QUALIFIED + '=VirtualMedia')
    subprocess.run(['bash', 'hack/multiplane/coordinator.sh', 'restart', str(s)], check=True, capture_output=True, text=True)
    rf.write(s / 'evidence' / 'media-offer.json', {'observedAt': now(), 'deliveryMethods': config['DeliveryMethods'],
                                                    'qualified': {'device': harness.DEVICE, 'methods': ['VirtualMedia'], 'source': 'lab-set annotation; commissioning is not implemented'}})
    print('media-offer: the cell declares NetworkBoot then VirtualMedia (central media service, %s fetch and %s report phases); target A is qualified for VirtualMedia only; coordinator restarted' % (FETCH, REPORT))


def arm(s, *fault):
    r = rf.bmc(s, 'python3', '/opt/fleet-lab/fleet_lab_adapter.py', 'fault', *fault)
    (s / 'redfish' / ('media-fault-%s.json' % fault[0])).write_text(r.stdout)


def arm_corrupt_token(s):
    arm(s, 'corrupt-config', SYSTEM)
    print('media-arm-corrupt-token: target A\'s next config medium will carry another report token than the media service served')


def arm_add_download(s):
    arm(s, 'add-download', SYSTEM)
    print('media-arm-add-download: target A\'s next config medium will list a download the operation never pinned')


def arm_stray_source(s):
    arm(s, 'stray-source', SYSTEM, STRAY_SOURCE)
    print('media-arm-stray-source: target A\'s next config medium makes its installed OS post its report, with the right token, from %s first' % STRAY_SOURCE)


def stray_address(s):
    op = media_installs(s)[-1]
    uid = op['metadata']['uid']
    address = (op['spec']['delivery'].get('reportAddress') or {})
    w = window_record(s, uid)
    claim = json.loads(provider(s, '-n', 'fleet-provider', 'get', 'ipclaims.ipam.miloapis.com', address.get('addressClaim', {}).get('name', '-'), '-o', 'json').stdout)
    bound = (claim.get('status') or {}).get('address', '').split('/')[0]
    if not address.get('address') or bound != address['address'] or not address['address'].startswith('192.168.77.'):
        die('the install records no IPAM-bound report address: %s, claim holds %r' % (address, bound))
    refused = w.get('refused') or []
    if [(r['from'], r['tokenMatched']) for r in refused] != [(STRAY_SOURCE, True)]:
        die('the media service did not refuse, and record, the right token from %s: %s%s' % (STRAY_SOURCE, refused, why(s, uid)))
    if not w.get('report') or w['report'].get('from') != address['address']:
        die('the media service took no report from the Machine\'s address %s: %s%s' % (address['address'], w.get('report'), why(s, uid)))
    if 'rejected-address' not in outcomes(s, uid) or len(media_effects(rf.audit(s), 'add-stray-source')) != 1:
        die('the stray-source fault did not fire exactly once, or no report was refused for its address')
    console = [l for l in read_console(s) if l.startswith('fleet: stray report') or l.startswith('fleet: first-boot report from')]
    rf.write(s / 'media-stray-address.json', {'operation': op['metadata']['name'], 'reportAddress': address, 'claimBound': bound, 'refused': refused,
                                               'reportFrom': w['report']['from'], 'consoleDiagnostics': console})
    print('media-stray-address: %s reports only from its IPAM address %s (claim %s); the right token from %s, inside the site\'s ranges, was refused and recorded; '
          'the report from %s was taken' % (op['metadata']['name'], address['address'], address['addressClaim']['name'], STRAY_SOURCE, address['address']))


def arm_lost_eject(s):
    arm(s, 'drop-eject', SYSTEM, 'CD1', '45')
    print('media-arm-lost-eject: target A\'s next CD1 eject is applied and its response withheld for 45s, past the 30s client timeout')


def media_effects(entries, fault=None):
    return [e for e in entries if '/VirtualMedia/' in e['path'] and e['method'] == 'POST' and (fault is None or e.get('fault') == fault)]


def summarize(entries):
    out = []
    for e in entries:
        item = {k: e[k] for k in ('at', 'method', 'path', 'status') if k in e}
        item.update({k: e[k] for k in ('media', 'effect', 'bytes', 'urlSHA256', 'resetType', 'fault', 'boot') if k in e})
        out.append(item)
    return out


def outcomes(s, uid):
    path = s / 'media-central' / 'transfers' / key(uid) / 'reports.log'
    return [w for w in (path.read_text().split() if path.exists() else []) if w.startswith('rejected') or w in ('accepted', 'duplicate', 'repeated')]


def held_report(s):
    ops = media_installs(s)
    if len(ops) != 1 or ops[0]['status'].get('phase') != 'OutcomeUnknown':
        die('expected one held virtual media installation, got %s' % [(o['metadata']['name'], o['status'].get('phase')) for o in ops])
    op = ops[0]
    uid = op['metadata']['uid']
    w = window_record(s, uid)
    if w.get('report') or w.get('duplicates') or w.get('rejected', 0) < 3:
        die('the media service took a report or refused fewer than three: %s%s' % ({k: w.get(k) for k in ('report', 'duplicates', 'rejected')}, why(s, uid)))
    stray = json.loads((s / 'media-stray-reports.json').read_text())
    if stray['answers'] != {'outside-ranges-ipv4': 403, 'outside-ranges-ipv6': 403} or not {'rejected-source', 'rejected-token'} <= set(outcomes(s, uid)):
        die('the stray reports were not all refused for their source, or the guest\'s report was not refused for its token: %s %s' % (stray['answers'], outcomes(s, uid)))
    steps = steps_of(s, uid)
    for step in ('media-window', 'media-insert-boot', 'media-insert-config', 'media-installer-power-on', 'media-installer-done', 'disk-power-on'):
        if steps.get(step) != 'Done':
            die('the held installation did not journal %s done: %s' % (step, steps))
    if 'media-report-verified' in steps or 'media-released' in steps:
        die('the held installation verified a report or recorded release: %s' % steps)
    if len(media_effects(rf.audit(s), 'corrupt-config-token')) != 1:
        die('the corrupt-token fault did not fire exactly once')
    # The proof is the media service's refusals above; the console is
    # diagnostics only and is recorded, never required.
    diagnostics = [l for l in read_console(s) if l.startswith('fleet: first-boot report')]
    # The worker's media identity is its own Secret: the Device's BMC
    # credential holds only BMC keys.
    credential = json.loads(rf.kubectl(s, '-n', rf.CREDENTIALS, 'get', 'secret', rf.credential_name(harness.DEVICE), '-o', 'json').stdout)
    if sorted(credential.get('data', {})) != ['password', 'tlsPublicKeySHA256', 'username']:
        die('the BMC credential Secret holds more than BMC credentials: %s' % sorted(credential.get('data', {})))
    rf.write(s / 'media-held-report.json', {'operation': op['metadata']['name'], 'fence': op['status']['fence'], 'reason': op['status'].get('reason'),
                                             'mediaService': {'report': None, 'rejected': w['rejected'], 'outcomes': outcomes(s, uid)},
                                             'strayReports': stray['answers'], 'steps': steps, 'consoleDiagnostics': diagnostics})
    print('media-held-report: %s booted from media; its OS posted a report with another token from its IPAM address (refused); stray reports from outside the site\'s report ranges were refused (%s); '
          'the media service took no report (%d refused); OutcomeUnknown (%s) at the report deadline'
          % (op['metadata']['name'], stray['answers'], w['rejected'], op['status'].get('reason')))


def held_download(s):
    ops = media_installs(s)
    if [o['status'].get('phase') for o in ops] != ['OutcomeUnknown', 'OutcomeUnknown']:
        die('expected two held virtual media installations, got %s' % [(o['metadata']['name'], o['status'].get('phase')) for o in ops])
    op = ops[1]
    uid = op['metadata']['uid']
    steps = steps_of(s, uid)
    if steps.get('media-installer-done') != 'Done' or 'media-report-verified' in steps or 'media-released' in steps:
        die('the second installation did not install from media, or verified its report or recorded release: %s%s' % (steps, detail(s, op)))
    if len(media_effects(rf.audit(s), 'add-config-download')) != 1:
        die('the add-download fault did not fire exactly once')
    w = window_record(s, uid)
    named = [d['name'] for d in (w.get('report') or {}).get('downloads') or []]
    if not w.get('report') or named != ['extension'] or op['spec']['image'].get('downloads'):
        die('the report did not name the unpinned download, or the operation pinned one: %s' % w.get('report'))
    rf.write(s / 'media-held-download.json', {'operation': op['metadata']['name'], 'fence': op['status']['fence'], 'reason': op['status'].get('reason'),
                                               'recordedDownloads': [], 'reportedDownloads': w['report']['downloads'], 'steps': steps})
    print('media-held-download: %s pinned no download; its first-boot report named %s, so it was never verified: OutcomeUnknown (%s)'
          % (op['metadata']['name'], named, op['status'].get('reason')))


def held_eject(s):
    ops = media_installs(s)
    if [o['status'].get('phase') for o in ops] != ['OutcomeUnknown'] * 3:
        die('expected three held virtual media installations, got %s' % [(o['metadata']['name'], o['status'].get('phase')) for o in ops])
    op = ops[2]
    uid = op['metadata']['uid']
    steps = steps_of(s, uid)
    for step in ('media-window', 'media-insert-boot', 'media-insert-config', 'media-installer-done', 'media-report-verified', 'media-close'):
        if steps.get(step) != 'Done':
            die('the third installation did not journal %s done: %s%s' % (step, steps, detail(s, op)))
    if steps.get('media-eject-cd1') != 'Intended' or 'media-released' in steps:
        die('the lost eject is not journaled intended-only, or release was recorded: %s' % steps)
    # The BMC audits the eject only once it answers, after the 45 seconds it
    # withholds the answer, so after the worker gave up on it.
    for _ in range(60):
        faulted = media_effects(rf.audit(s), 'drop-response-after-effect')
        if faulted:
            break
        time.sleep(2)
    if len(faulted) != 1 or faulted[0].get('media') != 'CD1':
        die('the lost eject fault did not fire exactly once on CD1')
    w, want = window_record(s, uid), site_window(s, uid)
    if not w.get('report') or w['report']['image'] != want['image'] or w['report']['bootstrap'] != want['bootstrap'] or w.get('duplicates'):
        die('the third installation\'s report was not taken with its own digests')
    rf.write(s / 'media-held-eject.json', {'operation': op['metadata']['name'], 'fence': op['status']['fence'], 'reason': op['status'].get('reason'),
                                            'steps': steps, 'fault': summarize(faulted), 'report': w['report'], 'reportOutcomes': outcomes(s, uid)})
    print('media-held-eject: %s took its first-boot report (image %s..., bootstrap %s...), then lost the CD1 eject response: OutcomeUnknown (%s), media-released never journaled'
          % (op['metadata']['name'], w['report']['image'][:19], w['report']['bootstrap'][:19], op['status'].get('reason')))


def redfish_get(s, path):
    """GET through the lab BMC's own Redfish API from inside its Pod, with
    the mounted synthetic credential. Only the fields the proof needs leave
    the Pod; never a media URL."""
    script = r'''
import base64, json, ssl, sys, urllib.request
user = open('/etc/fleet-bmc-credentials/username').read().strip()
password = open('/etc/fleet-bmc-credentials/password').read().strip()
context = ssl.create_default_context(); context.check_hostname = False; context.verify_mode = ssl.CERT_NONE
request = urllib.request.Request('https://127.0.0.1:8443' + sys.argv[1], headers={'Authorization': 'Basic ' + base64.b64encode((user + ':' + password).encode()).decode()})
doc = json.load(urllib.request.urlopen(request, context=context, timeout=20))
keep = {k: doc.get(k) for k in ('Id', 'Inserted', 'PowerState', 'Boot', 'Members') if k in doc}
if 'Image' in doc:
    keep['ImageEmpty'] = doc['Image'] in (None, '')
if 'Boot' in keep:
    keep['Boot'] = {k: v for k, v in keep['Boot'].items() if '@' not in k}
print(json.dumps(keep))
'''
    return json.loads(rf.bmc(s, 'python3', '-c', script, path).stdout)


def release_proof(s):
    ops = media_installs(s)
    if [o['status'].get('phase') for o in ops] != ['OutcomeUnknown'] * 3 + ['Succeeded']:
        die('expected three held and then the settling virtual media installation, got %s' % [(o['metadata']['name'], o['status'].get('phase')) for o in ops])
    settling = ops[3]
    custody = json.loads(provider(s, '-n', 'fleet-provider', 'get', 'devicecustody', DEVICE_NAME, '-o', 'json').stdout)['spec']
    if custody.get('recoveryHold') or custody.get('activeOperation'):
        die('custody still holds a hold or an operation: %s' % {k: custody.get(k) for k in ('recoveryHold', 'activeOperation')})
    # Source one: the BMC, through Redfish.
    base = '/redfish/v1/Systems/%s' % SYSTEM
    system = redfish_get(s, base)
    devices = [redfish_get(s, m['@odata.id']) for m in redfish_get(s, base + '/VirtualMedia')['Members']]
    if len(devices) != 2 or any(d['Inserted'] or not d['ImageEmpty'] for d in devices):
        die('the BMC still reports media: %s' % devices)
    if system['Boot'].get('BootSourceOverrideEnabled') != 'Disabled':
        die('the BMC reports a boot override: %s' % system['Boot'])
    # Source two: the central media service, live.
    code, status = central(s, 'GET', '/v1/devices/' + harness.DEVICE)
    if code != 200 or status != {'windows': 4, 'openWindows': 0, 'openTransfers': 0, 'duplicates': 0}:
        die('the media service reports %s %s for the Device' % (code, status))
    # The live media service denies every window, from the BMC's own Pod.
    denials = []
    for op in ops:
        w = window_record(s, op['metadata']['uid'])
        url = 'https://%s:%d/media/%s/boot/%s/boot.iso' % (rf.MEDIA_HOST, rf.MEDIA_PORT, key(op['metadata']['uid']), w['bootDigest'])
        probe = ("import ssl,sys,urllib.request,urllib.error\nc=ssl.create_default_context(cafile='/etc/fleet-bmc-media-ca/ca.crt')\n"
                 "try:\n urllib.request.urlopen(sys.argv[1],context=c,timeout=20); print(200)\nexcept urllib.error.HTTPError as e: print(e.code)\n")
        answer = rf.bmc(s, 'python3', '-c', probe, url).stdout.strip()
        if answer != '403' or not w.get('closed') or w['configMediumKept']:
            die('the media service served or kept a window: HTTP %s, closed %s' % (answer, w.get('closed')))
        denials.append({'operation': op['metadata']['name'], 'status': int(answer)})
    # No site store record keeps a rendered config medium.
    kept = rf.runtime(s, 'sh', '-c', 'ls %s/devices/*/mediacfg 2>/dev/null | grep -c json || true' % STORE_ROOT).stdout.strip()
    if kept not in ('', '0'):
        die('the site store still keeps %s config media' % kept)
    steps = steps_of(s, settling['metadata']['uid'])
    for step in ('media-window', 'media-insert-boot', 'media-insert-config', 'media-installer-done', 'media-report-verified', 'media-close', 'media-eject-cd1', 'media-eject-cd2', 'media-released'):
        if steps.get(step) != 'Done':
            die('the settling installation did not journal %s done: %s' % (step, steps))
    rf.write(s / 'media-release.json', {'observedAt': now(), 'settling': settling['metadata']['name'], 'fence': settling['status']['fence'],
                                         'bmc': {'system': system, 'devices': devices}, 'mediaService': {'device': status, 'liveDenials': denials},
                                         'journal': steps, 'evidence': settling['status'].get('evidence')})
    print('media-release-proof: BMC reads back CD1 and CD2 empty and override Disabled; the central media service reports 4 windows, none open, no transfer in progress, no reused token, '
          'and denies all four (HTTP 403); no config medium kept; custody holds no hold')


def stale_fence(s):
    refused = []
    for op in media_installs(s):
        w = window_record(s, op['metadata']['uid'])
        body = {'device': harness.DEVICE, 'operation': op['metadata']['uid'], 'fence': op['status']['fence'], 'bootDigest': w['bootDigest'],
                'capabilitySHA256': '0' * 64, 'tokenSHA256': '0' * 64, 'configMedium': base64.b64encode(b'stale').decode(),
                'fetchExpires': later(10), 'reportExpires': later(20), 'reportFrom': [REPORT_RANGE],
                'reportAddress': op['spec']['delivery']['reportAddress']['address']}
        # A well-formed open, so the cell worker's answer is custody's.
        # Without a client certificate, and with another site's, the open is
        # refused before the service looks at custody; as the cell's worker,
        # custody refuses it.
        answers = {client: central(s, 'PUT', '/v1/windows/' + key(op['metadata']['uid']), body, identity)[0]
                   for client, identity in (('unauthenticated', None), ('anotherSite', 'media-worker-other'), ('cellWorker', 'media-worker'))}
        if answers != {'unauthenticated': 401, 'anotherSite': 403, 'cellWorker': 409}:
            die('the media service answered %s to window opens it must refuse' % answers)
        refused.append({'operation': op['metadata']['name'], 'fence': op['status']['fence'], 'answers': answers})
    r = rf.runtime(s, 'fleet-multiplane', 'media-stale', '--device', harness.DEVICE, check=False)
    if r.returncode != 0 or not r.stdout.startswith('PASS'):
        die('the site store stale fence probe failed: %s %s' % (r.stdout.strip()[-300:], r.stderr.strip()[-300:]))
    rf.write(s / 'media-stale.json', {'mediaService': refused, 'siteStore': r.stdout.strip()})
    print('media-stale-fence: for all %d ended operations the media service refused a window open without a client certificate (401), as another site (403), and as the cell\'s worker at the old fence (409); site store: %s'
          % (len(refused), r.stdout.strip()))


def evidence(s):
    ops = media_installs(s)
    entries = rf.audit(s)
    console = read_console(s)
    reports = []
    for op in ops:
        uid = op['metadata']['uid']
        w, want = window_record(s, uid), site_window(s, uid)
        path = s / 'media-central' / 'transfers' / key(uid) / 'media-transfer.log'
        transfers = path.read_text().splitlines() if path.exists() else []
        reports.append({'operation': op['metadata']['name'], 'fence': op['status']['fence'], 'phase': op['status']['phase'], 'reason': op['status'].get('reason'),
                        'reportTokenSHA256': w['tokenSHA256'], 'expected': {'image': want['image'], 'bootstrap': want['bootstrap']}, 'report': w.get('report'),
                        'reportOutcomes': outcomes(s, uid), 'transfers': [' '.join(t.split()[1:2] + t.split()[3:]) for t in transfers]})
    if [r['report'] is not None for r in reports] != [False, True, True, True]:
        die('expected no report for the corrupted install and one for each of the others')
    if any(r['report'] and (r['report']['image'] != r['expected']['image'] or r['report']['bootstrap'] != r['expected']['bootstrap']) for r in reports) or \
            [bool((r['report'] or {}).get('downloads')) for r in reports] != [False, True, False, False]:
        die('a taken report does not carry its operation\'s digests')
    if any(l.startswith('FLEET-REPORT') or 'token=' in l for l in console):
        die('the console carries a report or a token')
    qemu = rf.runtime(s, 'cat', '/var/log/libvirt/qemu/fleet-lab-target-a.log').stdout
    blocks = re.split(r'^\S+ \S+: starting up ', qemu, flags=re.M)[1:]

    def first(block):
        # The device QEMU boots first: the one with boot index 1.
        segment = next((seg for seg in re.split(r'\s-device\s', block) if '"bootindex":1' in seg or 'bootindex=1' in seg), '')
        return 'cdrom' if 'ide-cd' in segment else 'disk' if 'virtio-blk' in segment else 'network' if segment else 'unknown'
    starts = [first(b) for b in blocks]
    mutations = summarize([e for e in entries if e['method'] != 'GET' and '/Systems/%s' % SYSTEM in e['path']])
    record = {'observedAt': now(), 'device': harness.DEVICE, 'operations': reports, 'qemuStarts': starts, 'bmcEffects': mutations,
              'firstBootConsole': [l for l in console if l.startswith('fleet:')],
              'offer': json.loads((s / 'evidence' / 'media-offer.json').read_text()), 'heldReport': json.loads((s / 'media-held-report.json').read_text()),
              'heldDownload': json.loads((s / 'media-held-download.json').read_text()), 'heldEject': json.loads((s / 'media-held-eject.json').read_text()),
              'strayAddress': json.loads((s / 'media-stray-address.json').read_text()), 'release': json.loads((s / 'media-release.json').read_text()),
              'staleFence': json.loads((s / 'media-stale.json').read_text())}
    # Target B, served by the same BMC, was never touched.
    base = json.loads((s / 'redfish-baseline.json').read_text())
    b = rf.TARGET_B
    record['targetB'] = {'diskSHA256Unchanged': rf.disk_sha(s, b['disk']) == base['targetB']['diskSHA256'], 'qemuStarts': rf.qemu_starts(s, b['domain']),
                         'mutatingRedfishRequests': len(rf.mutating(entries, b['system']))}
    if not record['targetB']['diskSHA256Unchanged'] or record['targetB']['qemuStarts'] or record['targetB']['mutatingRedfishRequests']:
        die('target B was touched: %s' % record['targetB'])
    if starts != ['cdrom', 'disk'] * 4:
        die('expected target A to boot from media then disk, four times; got %s' % starts)
    rf.write(s / 'evidence' / 'virtual-media.json', record)
    # Every check above passed. Only result.json, which run.sh writes last,
    # records whether the run passed.
    rf.write(s / 'evidence' / 'summary.json', {
        'schemaVersion': 3, 'acceptance': 'evidence-verified', 'suite': 'virtual-media', 'observedAt': now(), 'prefix': (s / 'prefix').read_text().strip(),
        'qualification': {'nativeMiloProjectAPIs': True, 'actualKarmadaDeliveryAndReflection': True, 'kubernetesJobExecution': True, 'actualKroController': True,
                          'fluxReconciledServices': True, 'redfishVirtualMedia': 'DMTF Redfish Interface Emulator 1.2.2 + lab-only adapter',
                          'centralMediaService': 'workstation process on loopback', 'firstBootReport': True,
                          'physicalHardware': False, 'physicalBMC': False, 'vendorRedfishQualified': False, 'uefi': False, 'productionAuthority': False},
        'scenarios': {'virtualMedia': 'virtual-media.json', 'installs': [{k: r[k] for k in ('operation', 'fence', 'phase', 'reason')} for r in reports]}})
    for name in ('source-SHA256SUMS',):
        if (s / name).exists():
            (s / 'evidence' / name).write_text((s / name).read_text())
    paths = [s / 'site-bin' / 'fleet-multiplane', s / 'fleet-multiplane-host']
    (s / 'evidence' / 'binary-SHA256SUMS').write_text(''.join(hashlib.sha256(p.read_bytes()).hexdigest() + '  ' + p.name + '\n' for p in paths))
    print('media-evidence: %d virtual media installs; reports taken %s; target A booted %s; console carries no token; %d BMC effects recorded'
          % (len(reports), [r['report'] is not None for r in reports], ', '.join(starts), len(mutations)))


if __name__ == '__main__':
    step, state = sys.argv[1], pathlib.Path(sys.argv[2])
    {'offer': offer, 'arm-corrupt-token': arm_corrupt_token, 'held-report': held_report, 'arm-add-download': arm_add_download, 'held-download': held_download,
     'arm-lost-eject': arm_lost_eject, 'held-eject': held_eject, 'arm-stray-source': arm_stray_source, 'stray-address': stray_address,
     'release-proof': release_proof, 'stale-fence': stale_fence, 'evidence': evidence}[step](state)
