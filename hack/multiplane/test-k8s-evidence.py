#!/usr/bin/env python3
"""Offline tests for k8s_evidence.py against a fake kubectl.

Every helper command the live acceptance uses is executed once on the success
path and on representative failure paths, so call-signature and data-shape
mistakes fail here instead of mid-run. Never contacts a cluster.
"""
import hashlib
import json
import os
import pathlib
import subprocess
import sys
import tempfile

root = pathlib.Path(sys.argv[1])
helper = root / 'hack/multiplane/k8s_evidence.py'
failures = []


def check(name, ok, detail=''):
    if not ok:
        failures.append(name + (': ' + detail if detail else ''))


FAKE = r'''#!PYTHON
import json, os, sys
a = sys.argv[1:]
world = json.load(open(os.environ['FAKE_WORLD']))
config = a[a.index('--kubeconfig') + 1]
args = [x for x in a if not x.startswith('--request-timeout')]
args = args[args.index('--kubeconfig') + 2:]
def ns():
    return args[args.index('-n') + 1] if '-n' in args else ''
def emit(v): print(json.dumps(v)); sys.exit(0)
if config.endswith('worker-kubeconfig.json'):
    if args[-3:] == ['get', 'machineallocationhandoff', world['handoff']] or args[-5:-2] == ['get', 'machineallocationhandoff', world['handoff']]:
        print('machineallocationhandoff/' + world['handoff']); sys.exit(0)
    if args[:2] == ['auth', 'can-i'] and args[3] == 'machineallocationpayload/' + world['handoff'] + '-payload':
        print('yes'); sys.exit(0)
    print('Error from server (Forbidden): denied', file=sys.stderr); sys.exit(1)
if args[:3] == ['config', 'view', '--raw']:
    emit({'clusters': [{'cluster': {}}], 'contexts': [{'context': {}}], 'users': []})
if 'exec' in args:
    cmd = args[args.index('--') + 1:]
    if cmd[:1] == ['cat'] and cmd[1].endswith('/state.json'): print(world['store']); sys.exit(0)
    if cmd[:1] == ['test']: sys.exit(0 if cmd[-1].rsplit('/', 1)[1] in world.get('results', []) else 1)
    if cmd[:1] == ['cat'] and '/qemu/' in cmd[1]: print(world['qemu']); sys.exit(0)
    if cmd[:1] == ['virsh']: print(''); sys.exit(0)
    sys.exit(1)
if args[:1] == ['create']:
    obj = json.loads(sys.stdin.read())
    if obj['metadata']['name'] in [h['metadata']['name'] for h in world['handoffs']] + world.get('created', []):
        print('Error from server (AlreadyExists): exists', file=sys.stderr); sys.exit(1)
    world.setdefault('created', []).append(obj['metadata']['name'])
    json.dump(world, open(os.environ['FAKE_WORLD'], 'w')); sys.exit(0)
if 'delete' in args:
    name = args[args.index('delete') + 2]
    world['jobs'].pop('w-' + name, None); json.dump(world, open(os.environ['FAKE_WORLD'], 'w')); sys.exit(0)
verb, kind = args[args.index('get') + 1], None
kind = args[args.index('get') + 1]
rest = args[args.index('get') + 2:]
name = rest[0] if rest and not rest[0].startswith('-') else None
if kind == 'job':
    if name not in world['jobs']: print('NotFound', file=sys.stderr); sys.exit(1)
    emit(world['jobs'][name])
if kind == 'pods':
    sel = args[args.index('-l') + 1].split('=', 1)[1]
    emit({'items': [p for p in world['pods'] if p['metadata']['labels'].get('fleet.datumapis.com/handoff') == sel or p['metadata']['labels'].get('app.kubernetes.io/name') == sel]})
if kind == 'pvc': emit({'metadata': {'uid': 'pvc-uid'}, 'spec': {'volumeName': 'pv-1'}})
if kind == 'pv': emit({'metadata': {'uid': 'pv-uid'}, 'spec': {'persistentVolumeReclaimPolicy': 'Retain'}})
if kind == 'machine':
    emit(world['machines'][config.split('/')[-2]])
if kind == 'role': emit({'metadata': {'labels': {'kro.run/node-id': 'workerRoles'}}, 'rules': [{'resourceNames': [world['handoff']], 'verbs': ['get']}, {'resourceNames': [world['handoff'] + '-payload'], 'verbs': ['get']}]})
if kind == 'machineallocationhandoffs': emit({'items': world['handoffs']})
if kind == 'machineallocationhandoff': emit({'metadata': {'name': name}, 'spec': {}, 'status': world.get('statuses', {}).get(name)})
sys.exit(1)
'''.replace('#!PYTHON', '#!' + sys.executable)


def job(name, uid, conditions, failed=0, succeeded=0):
    return {'metadata': {'name': name, 'uid': uid, 'labels': {'kro.run/node-id': 'workers'}},
            'spec': {'backoffLimit': 6, 'podReplacementPolicy': 'Failed', 'template': {'spec': {'serviceAccountName': name}}},
            'status': {'failed': failed, 'succeeded': succeeded, 'conditions': [{'type': t, 'status': 'True', 'reason': r} for t, r in conditions]}}


def pod(name, uid, handoff, job_uid, phase, exit_code=None, app=None):
    status = {'phase': phase, 'containerStatuses': [{'state': {'terminated': {'exitCode': exit_code}} if exit_code is not None else {'running': {}}}]}
    labels = {'batch.kubernetes.io/controller-uid': job_uid}
    labels['fleet.datumapis.com/handoff' if handoff else 'app.kubernetes.io/name'] = handoff or app
    return {'metadata': {'name': name, 'uid': uid, 'labels': labels}, 'spec': {'nodeName': 'cell'}, 'status': status}


with tempfile.TemporaryDirectory() as tmp:
    tmp = pathlib.Path(tmp)
    bindir = tmp / 'bin'; bindir.mkdir()
    (bindir / 'kubectl').write_text(FAKE); (bindir / 'kubectl').chmod(0o755)
    state = tmp / 'state'; (state / 'evidence').mkdir(parents=True); (state / 'consumer-a').mkdir(); (state / 'consumer-b').mkdir()
    (state / 'prefix').write_text('fleet-mp-1234abcd\n')
    h = 'op-' + 'a' * 40
    (state / 'install-handoff').write_text(h + '\n')
    device = 'fleet-provider/synthetic-device-central-01'
    # The latest delivered assignment: the release wipe at fence 2.
    wipe = {'device': device, 'operation': {'kind': 'DeviceWipe', 'name': 'alloc-0123456789abcdef-wipe-1'}, 'fence': 2,
            'admittedAt': '2026-01-01T00:00:00Z', 'deadline': '2026-01-01T01:00:00Z', 'snapshot': {}, 'inputDigest': 'sha256:' + '0' * 64, 'inputs': {}}
    wipe_name = 'op-' + hashlib.sha256(('%s/devicewipe.alloc-0123456789abcdef-wipe-1.2' % device).encode()).hexdigest()[:40]
    install = dict(wipe, operation={'kind': 'DeviceInstallation', 'name': 'alloc-0123456789abcdef-install-0123456789ab'}, fence=1)
    install_key = 'deviceinstallation.alloc-0123456789abcdef-install-0123456789ab.1'
    store = {'device': device, 'fence': 2, 'stopped': 2, 'journal': [{'operation': install_key, 'fence': 1, 'step': 'prepare-disk', 'state': 'Done', 'at': 'x'}]}
    world = {'handoff': h, 'store': json.dumps(store), 'qemu': 'x: starting up a\nx: starting up b\n',
             'jobs': {'w-' + h: job('w-' + h, 'job-2', [('Complete', 'CompletionsReached')], succeeded=1)},
             'pods': [pod('p2', 'pod-2', h, 'job-2', 'Succeeded', 0)] + [pod(d, d + '-uid', None, '', 'Running', app=d) for d in ('fleet-site-runtime', 'fleet-site-boot', 'fleet-site-observer')],
             'machines': {'consumer-a': {'status': {'phase': 'Ready', 'conditions': [{'type': 'Bound', 'status': 'True'}, {'type': 'Ready', 'status': 'True'}]}},
                          'consumer-b': {'status': {'phase': 'Bound', 'conditions': [{'type': 'Bound', 'status': 'True'}]}}},
             'statuses': {h: {'observation': {'cell': 'fleet-mp-1234abcd-cell', 'result': {'phase': 'Succeeded', 'operation': install['operation'], 'fence': 1}}}},
             'handoffs': [{'metadata': {'name': h}, 'spec': {'cell': 'fleet-mp-1234abcd-cell', 'device': device, 'assignmentJSON': json.dumps(install)}},
                          {'metadata': {'name': wipe_name}, 'spec': {'cell': 'fleet-mp-1234abcd-cell', 'device': device, 'assignmentJSON': json.dumps(wipe)}}]}
    wpath = tmp / 'world.json'
    env = dict(os.environ, PATH=str(bindir) + os.pathsep + os.environ['PATH'], FAKE_WORLD=str(wpath))

    def save():
        wpath.write_text(json.dumps(world))

    def run(*args):
        save()
        r = subprocess.run([sys.executable, str(helper), args[0], str(state), *args[1:]], text=True, capture_output=True, env=env)
        world.update(json.loads(wpath.read_text()))
        return r

    r = run('running-pod', h)
    check('running-pod ignores completed pods', r.returncode == 0 and r.stdout.strip() == '', r.stdout + r.stderr)
    r = run('job-pod', h, 'job-2')
    check('job-pod finds pod by job uid', r.stdout.strip() == 'pod-2', r.stdout + r.stderr)
    r = run('job', h)
    check('job record', r.returncode == 0 and json.loads(r.stdout)['uid'] == 'job-2', r.stderr)

    # Interruption records.
    ops = lambda stage: json.dumps([{'operation': install_key, 'fence': 1, 'kind': 'DeviceInstallation', 'stage': stage, 'stageState': 'Done',
                                     'done': stage == 'disk-verified', 'phase': 'Succeeded' if stage == 'disk-verified' else None, 'steps': [[stage, 'Done']]}])
    (state / 'interrupt-observed.json').write_text(json.dumps({'operation': {'operation': install_key, 'fence': 1, 'stage': 'installer-start', 'domain': 'd'}, 'domainRunning': True}))
    (state / 'interrupt-after.json').write_text(ops('installer-start'))
    (state / 'interrupt-job-before-pod.json').write_text(json.dumps({'uid': 'job-1', 'failed': 0}))
    (state / 'interrupt-job-after-pod.json').write_text(json.dumps({'uid': 'job-1', 'failed': 1}))
    r = run('record-pod-interrupt', 'pod-0', 'pod-1')
    check('record-pod-interrupt', r.returncode == 0, r.stderr)
    (state / 'interrupt-job-before.json').write_text(ops('installer-verified'))
    (state / 'interrupt-job-after.json').write_text(ops('disk-start'))
    r = run('record-job-interrupt', 'job-1', 'job-2', 'pod-1', 'pod-2')
    check('record-job-interrupt', r.returncode == 0, r.stderr)
    (state / 'interrupt-job-before.json').write_text(ops('disk-verified'))
    check('record-job-interrupt rejects finished install', run('record-job-interrupt', 'job-1', 'job-2', 'pod-1', 'pod-2').returncode != 0)
    (state / 'interrupt-job-before.json').write_text(ops('installer-verified'))
    run('record-job-interrupt', 'job-1', 'job-2', 'pod-1', 'pod-2')
    (state / 'resume-after.json').write_text(ops('disk-verified'))
    r = run('record-resume', h)
    check('record-resume', r.returncode == 0, r.stderr)
    world['statuses'][h]['observation']['result']['fence'] = 2
    check('record-resume rejects a result at another fence', run('record-resume', h).returncode != 0)
    world['statuses'][h]['observation']['result']['fence'] = 1

    # Lifecycle: snapshot, replace site Pods and the Job, record.
    r = run('lifecycle-snapshot', h)
    check('lifecycle-snapshot', r.returncode == 0, r.stderr)
    (state / 'lifecycle-before.json').write_text(r.stdout)
    world['pods'] = [pod('p3', 'pod-3', h, 'job-3', 'Succeeded', 0)] + [pod(d, d + '-new', None, '', 'Running', app=d) for d in ('fleet-site-runtime', 'fleet-site-boot', 'fleet-site-observer')]
    world['jobs']['w-' + h] = job('w-' + h, 'job-3', [('Complete', 'CompletionsReached')], succeeded=1)
    r = run('lifecycle-snapshot', h)
    (state / 'lifecycle-after.json').write_text(r.stdout)
    r = run('record-lifecycle', 'job-2', 'job-3')
    check('record-lifecycle', r.returncode == 0 and (state / 'evidence/lifecycle.json').exists(), r.stderr)
    world['store'] = json.dumps(dict(store, journal=store['journal'] + [{'operation': install_key, 'fence': 1, 'step': 'installer-start', 'state': 'Intended', 'at': 'y'}]))
    (state / 'lifecycle-after.json').write_text(run('lifecycle-snapshot', h).stdout)
    check('record-lifecycle rejects a site store change', run('record-lifecycle', 'job-2', 'job-3').returncode != 0)

    # Worker RBAC probes.
    (state / 'worker.token').write_text('w' * 40)
    r = run('worker-rbac', h)
    check('worker-rbac', r.returncode == 0 and (state / 'k8s-worker-rbac.json').exists(), r.stderr)

    # Delivery faults: create, verify failure by policy, prune.
    r = run('delivery-faults', 'create')
    check('delivery-faults create', r.returncode == 0 and len(world.get('created', [])) == 2, r.stderr)
    faults = json.loads((state / 'fault-handoffs.json').read_text())
    check('duplicate refused by the API', faults['duplicate'] == {'handoff': wipe_name, 'refused': 'AlreadyExists'}, str(faults['duplicate']))
    stale = faults['stale-fence']
    check('stale fault is the same operation below the fence under its own name', stale['operation'] == 'devicewipe.alloc-0123456789abcdef-wipe-1.1' and stale['fence'] == 1
          and stale['handoff'] == 'op-' + hashlib.sha256(('%s/devicewipe.alloc-0123456789abcdef-wipe-1.1' % device).encode()).hexdigest()[:40], str(stale))
    check('faults derive from the latest assignment', faults['source']['handoff'] == wipe_name and faults['source']['fence'] == 2)
    for fault in ('wrong-cell', 'stale-fence'):
        name = faults[fault]['handoff']
        check('fault name fits worker names: ' + fault, len('w-' + name) <= 63 and (state / ('fault-%s-job' % fault)).read_text().strip() == 'w-' + name, name)
        world['jobs']['w-' + name] = job('w-' + name, fault, [('Failed', 'PodFailurePolicy')], failed=1)
        world['pods'].append(pod('f-' + fault, 'f-' + fault, name, fault, 'Failed', 65))
    world['statuses'][stale['handoff']] = {'observation': {'cell': 'fleet-mp-1234abcd-cell', 'fence': {'seen': 2, 'stopped': 2}}}
    r = run('delivery-faults', 'verify')
    check('delivery-faults verify', r.returncode == 0, r.stderr)
    world['results'] = ['devicewipe.alloc-0123456789abcdef-wipe-1.1.json']
    check('delivery-faults rejects a stored stale result', run('delivery-faults', 'verify').returncode != 0)
    world['results'] = []
    world['statuses'][faults['wrong-cell']['handoff']] = {'observation': {'cell': 'fleet-mp-1234abcd-cell'}}
    check('delivery-faults rejects a wrong-cell observation', run('delivery-faults', 'verify').returncode != 0)
    del world['statuses'][faults['wrong-cell']['handoff']]
    run('delivery-faults', 'verify')
    r = run('delivery-faults', 'delete')
    check('delivery-faults delete', r.returncode == 0 and json.loads((state / 'evidence/delivery-faults.json').read_text()).get('prunedByKro'), r.stderr)
    world['pods'][-1]['status']['containerStatuses'][0]['state']['terminated']['exitCode'] = 75
    world['jobs']['w-' + name] = job('w-' + name, 'x', [('Failed', 'BackoffLimitExceeded')], failed=7)
    check('delivery-faults rejects non-policy failure', run('delivery-faults', 'verify').returncode != 0)

    secrets = ['F' * 32, 'w' * 40]
    leaked = [p.name for p in (state / 'evidence').rglob('*') if p.is_file() and any(s in p.read_text() for s in secrets)]
    check('evidence records contain no fence or token', not leaked, str(leaked))

if failures:
    print('FAIL k8s evidence tests:\n  ' + '\n  '.join(failures))
    sys.exit(1)
print('PASS k8s evidence tests')
