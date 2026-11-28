#!/usr/bin/env python3
"""Offline tests for snapshot.py with a fake kubectl; no container runtime."""
import json
import os
import pathlib
import stat
import subprocess
import sys
import tempfile

root = pathlib.Path(sys.argv[1])
failures = []


def check(name, ok, detail=''):
    if not ok:
        failures.append(name + (': ' + detail if detail else ''))


RETAINED = 'r' * 24 + 'etained0secret'
JWT = 'eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJzeW50aGV0aWMifQ.c2lnbmF0dXJlc2lnbmF0dXJl'
# Credentials the run never retained, so only the masks can catch them, in
# every shape a log line, condition, reason or conflict might carry one.
UNRETAINED = {'bmc_password=hunter2xx': 'hunter2xx', 'access_token: abcdef123456': 'abcdef123456', 'secretKey=abcd1234': 'abcd1234',
              'https://admin:s3cretpw@10.0.0.1/redfish': 's3cretpw', 'ghp_abcdefghijklmnopqrstu1234': 'ghp_abcdefghijklmnopqrstu1234',
              'Authorization: Digest digestvalue123': 'digestvalue123', '"password":"two words here"': 'words here',
              'bare ' + JWT: JWT, 'pin ' + 'ab' * 32: 'ab' * 32,
              # Escaped JSON echoed in a quoted log field, an escaped quote in a
              # value, and keys separated from their value by a space.
              'body="{\\"Password\\":\\"escjson71\\"}"': 'escjson71', 'password="a\\"b escquote72"': 'escquote72',
              'PASSWORD spaced73': 'spaced73', 'sshpass -p flagged74 ssh': 'flagged74', 'pw=short75': 'short75',
              'Cookie: session=cookie76': 'cookie76', 'AKIAABCDEFGHIJKLMNOP': 'AKIAABCDEFGHIJKLMNOP'}
tmp = pathlib.Path(tempfile.mkdtemp())
bin_dir = tmp / 'bin'
bin_dir.mkdir()
fake = bin_dir / 'kubectl'
# The fake API answers with objects that carry secret-shaped values in places
# the snapshot must not copy (specs, Secret-like fields) and in places it
# copies (condition messages, log lines), so both the allow-list and the
# masking are exercised.
fake.write_text('''#!/usr/bin/env python3
import json, os, sys
args = sys.argv[1:]
retained, jwt = os.environ['RETAINED'], os.environ['JWT']
shapes = json.loads(os.environ['UNRETAINED'])
if os.environ.get('FAKE_DOWN') and 'management-hub.json' in ' '.join(args):
    print('Unable to connect to the server: dial tcp: connection refused', file=sys.stderr); sys.exit(1)
if 'logs' in args:
    print('[pod/w-1/worker] 2026-11-28T04:01:00Z pass incomplete: token=%s refused' % retained)
    print('[pod/w-1/worker] 2026-11-28T04:01:01Z routine progress line')
    print('[pod/w-1/worker] 2026-11-28T04:01:02Z error: Authorization: Bearer %s' % jwt)
    for shape in shapes:
        print('[pod/w-1/worker] 2026-11-28T04:01:03Z error: %s' % shape)
    sys.exit(0)
kind = args[args.index('get') + 1]
cond = [{'type': 'Ready', 'status': 'False', 'reason': 'Waiting', 'message': 'reflected status missing; password=%s; %s' % (retained, '; '.join(shapes)),
         'lastTransitionTime': '2026-11-28T04:00:00Z'}]
item = {'metadata': {'name': kind + '-1-api_key=nameleak99', 'namespace': 'fleet-coordination', 'generation': 2,
                     'labels': {'coordination.fleet.datumapis.com/role': 'assignment'}},
        'spec': {'assignmentJSON': retained, 'credential': jwt, 'state': 'Verified', 'holder': {'type': 'Allocation'}, 'fence': 4,
                 'conflicts': ['DeviceUIDMismatch', 'token=conflictleak9', {'nested': retained}],
                 'activeOperation': {'kind': 'DeviceInstallation', 'name': 'install-a', 'fence': 4, 'uid': retained}},
        'status': {'phase': 'Admitted', 'reason': 'Waiting password=reasonleak9', 'fence': '4; secret=fenceleak9', 'conditions': cond,
                   'observation': {'result': {'phase': 'Succeeded', 'fence': 4, 'receipt': retained}}}}
print(json.dumps({'items': [item]}))
''')
fake.chmod(fake.stat().st_mode | stat.S_IEXEC)


def state_dir():
    s = pathlib.Path(tempfile.mkdtemp(dir=tmp))
    (s / 'provider').mkdir()
    for name in ('provider/bootstrap-project.json', 'cell-admin.yaml', 'management-hub.json'):
        (s / name).write_text('{}\n')
    # A retained credential, as the run keeps it in private state.
    (s / 'provider' / 'bootstrap.token').write_text(RETAINED + '\n')
    (s / 'coordinator.log').write_text('2026-11-28T04:00:00Z coordinator: custody coordinator running\n'
                                       '2026-11-28T04:01:00Z coordinator: pass incomplete: reading the hub: Bearer %s\n' % JWT)
    return s


def snapshot(s, **env):
    return subprocess.run([sys.executable, str(root / 'hack/multiplane/snapshot.py'), str(s), 'install'], text=True, capture_output=True,
                          env=dict(os.environ, PATH=str(bin_dir) + os.pathsep + os.environ['PATH'], RETAINED=RETAINED, JWT=JWT,
                                   UNRETAINED=json.dumps(list(UNRETAINED)), **env))


s = state_dir()
r = snapshot(s)
path = s / 'evidence' / 'failure-snapshot.json'
text = path.read_text() if path.exists() else ''
snap = json.loads(text) if text else {}
check('keeps diagnosis readable', any('pass incomplete: reading the hub' in l for l in snap.get('coordinatorErrors') or [])
      and any('reflected status missing' in c.get('message', '') for c in ((snap.get('cellHandoffs') or [{}])[0].get('conditions') or [])), text[:400])
check('records a snapshot', r.returncode == 0 and snap.get('failedPhase') == 'install', r.stdout + r.stderr)
custody = (snap.get('provider') or {}).get('devicecustody') or [{}]
check('records custody and the operation phase', custody[0].get('phase') == 'Admitted' and custody[0].get('holder') == 'Allocation'
      and custody[0].get('activeOperation') == {'kind': 'DeviceInstallation', 'name': 'install-a', 'fence': 4, 'deadline': None}, str(custody))
check('records the reported result on cell and hub', (snap.get('cellHandoffs') or [{}])[0].get('resultPhase') == 'Succeeded'
      and (snap.get('hubHandoffs') or [{}])[0].get('resultPhase') == 'Succeeded', str(snap.get('hubHandoffs')))
check('records Karmada propagation conditions', [c['type'] for c in (snap.get('karmada') or {}).get('works', [{}])[0].get('conditions', [])] == ['Ready'],
      str(snap.get('karmada')))
check('records error lines only', len(snap.get('workerErrors') or []) == 2 + len(UNRETAINED) and len(snap.get('coordinatorErrors') or []) == 1,
      str(snap.get('workerErrors')))
check('copies no spec field or receipt', 'assignmentJSON' not in text and 'credential' not in text and 'receipt' not in text and '"uid"' not in text)
check('masks credential-shaped values', RETAINED not in text and JWT not in text and text.count('<masked>') >= 4, text[:400])
leaked = [shape for shape, value in UNRETAINED.items() if value in text]
check('masks credentials the run never retained, in every shape', not leaked, str(leaked))
check('masks names, reasons and conflicts too', not any(v in text for v in ('nameleak99', 'reasonleak9', 'conflictleak9', 'fenceleak9'))
      and custody[0].get('conflicts', [None])[0] == 'DeviceUIDMismatch' and len(custody[0].get('conflicts')) == 2 and custody[0].get('fence') == 4
      and (snap.get('provider') or {}).get('deviceinstallation', [{}])[0].get('fence') is None, str(custody))

# A section that cannot be read says why, and the rest is still recorded.
s2 = state_dir()
r2 = snapshot(s2, FAKE_DOWN='1')
snap2 = json.loads((s2 / 'evidence' / 'failure-snapshot.json').read_text())
check('records an unreadable section without failing', r2.returncode == 0 and 'unavailable' in snap2['hubHandoffs']
      and 'connection refused' in snap2['karmada']['unavailable'] and snap2['cellHandoffs'][0]['resultPhase'] == 'Succeeded', json.dumps(snap2)[:400])

# The secret scan is the last guard: a retained secret with no recognisable
# shape reaches the snapshot the script writes, and the scan fails and
# redacts it.
s3 = state_dir()
with open(s3 / 'coordinator.log', 'a') as f:
    f.write('2026-11-28T04:02:00Z coordinator: pass incomplete: hub refused %s\n' % RETAINED)
check('an unshaped retained value reaches the snapshot', snapshot(s3).returncode == 0 and RETAINED in (s3 / 'evidence' / 'failure-snapshot.json').read_text())
scan = subprocess.run([sys.executable, str(root / 'hack/multiplane/harness.py'), 'scan', '--state', str(s3)], text=True, capture_output=True)
check('a retained secret in a snapshot fails the scan', scan.returncode != 0 and 'failure-snapshot.json' in scan.stderr, scan.stdout + scan.stderr)
check('the scan redacts that snapshot', RETAINED not in (s3 / 'evidence' / 'failure-snapshot.json').read_text())
clean = subprocess.run([sys.executable, str(root / 'hack/multiplane/harness.py'), 'scan', '--state', str(s)], text=True, capture_output=True)
check('the masked snapshot passes the scan', clean.returncode == 0, clean.stdout + clean.stderr)

run_sh = (root / 'hack/multiplane/run.sh').read_text()
finish = run_sh.split('finish() {')[1].split('\n}\n')[0]
check('a failed run records the snapshot before stopping and scanning', finish.index('python3 hack/multiplane/snapshot.py') < finish.index('bash hack/multiplane/stop.sh') < finish.index('scan --state')
      and 'snapshot_seconds=8' in finish)

if failures:
    print('FAIL snapshot tests:\n  ' + '\n  '.join(failures))
    sys.exit(1)
print('PASS snapshot tests')
