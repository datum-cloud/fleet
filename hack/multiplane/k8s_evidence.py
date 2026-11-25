#!/usr/bin/env python3
"""Kubernetes site observation, fault injection and sanitized execution records.

Every call goes through the selected cell's API with its bootstrap kubeconfig.
Records keep names, UIDs, fences, phases, exit codes and conditions only: never
assignment inputs, journal payloads, boot capabilities or tokens.
"""
import datetime
import hashlib
import json
import os
import pathlib
import subprocess
import sys

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
import harness  # noqa: E402  (site store layout)

NS, SITE, CREDENTIALS = 'fleet-coordination', 'fleet-site', 'fleet-site-credentials'
LIBVIRT = 'qemu:///system?socket=/run/fleet-libvirt/libvirt-sock'
REDFISH = os.environ.get('FLEET_SITE_PROVIDER') == 'redfish'
CELL_LABEL, ROLE_LABEL = 'coordination.fleet.datumapis.com/cell', 'coordination.fleet.datumapis.com/role'


def now():
    return datetime.datetime.now(datetime.timezone.utc).isoformat()


def kubectl(state, *args, input=None, check=True, config='cell-admin.yaml'):
    r = subprocess.run(['kubectl', '--kubeconfig', str(pathlib.Path(state) / config), '--request-timeout=20s', *args],
                       input=input, text=True, capture_output=True)
    if check and r.returncode:
        raise SystemExit('kubectl %s failed: %s' % (' '.join(args[:4]), r.stderr.strip()[:300]))
    return r


def get(state, *args, config='cell-admin.yaml'):
    return json.loads(kubectl(state, *args, '-o', 'json', config=config).stdout)


def job(state, name):
    j = get(state, '-n', SITE, 'get', 'job', 'w-' + name)
    st = j.get('status', {})
    return {'job': j['metadata']['name'], 'uid': j['metadata']['uid'], 'active': st.get('active', 0), 'failed': st.get('failed', 0),
            'succeeded': st.get('succeeded', 0), 'conditions': sorted([c['type'], c['status'], c.get('reason', '')] for c in st.get('conditions', [])),
            'backoffLimit': j['spec'].get('backoffLimit'), 'podReplacementPolicy': j['spec'].get('podReplacementPolicy'),
            'serviceAccount': j['spec']['template']['spec'].get('serviceAccountName'),
            'kroNodeID': j['metadata'].get('labels', {}).get('kro.run/node-id')}


def pods(state, name):
    out = []
    for p in get(state, '-n', SITE, 'get', 'pods', '-l', 'fleet.datumapis.com/handoff=' + name)['items']:
        exit_code = next((c['state']['terminated']['exitCode'] for c in p['status'].get('containerStatuses', []) if 'terminated' in c.get('state', {})), None)
        out.append({'pod': p['metadata']['name'], 'uid': p['metadata']['uid'], 'phase': p['status'].get('phase'), 'exitCode': exit_code,
                    'deleting': bool(p['metadata'].get('deletionTimestamp')), 'jobUID': p['metadata'].get('labels', {}).get('batch.kubernetes.io/controller-uid'),
                    'node': p['spec'].get('nodeName')})
    return sorted(out, key=lambda p: p['pod'])


def running_pod(state, name, phases=('Running',)):
    running = [p for p in pods(state, name) if p['phase'] in phases and not p['deleting']]
    return running[0]['uid'] if len(running) == 1 else ''


def runtime(state, *command):
    return kubectl(state, '-n', SITE, 'exec', 'deploy/fleet-site-runtime', '-c', 'libvirt', '--', *command).stdout


def evidence(state):
    return pathlib.Path(state) / 'evidence'


def interrupt_record(state):
    return json.loads((evidence(state) / 'interrupt.json').read_text())


def write(path, value):
    pathlib.Path(path).write_text(json.dumps(value, indent=2, sort_keys=True) + '\n')


def installs(path):
    return [o for o in json.loads(pathlib.Path(path).read_text()) if o['kind'] == 'DeviceInstallation']


def assignment_name(assignment):
    """internal/coordination.AssignmentName: the only name an assignment's handoff may have."""
    return 'op-' + hashlib.sha256(('%s/%s/%d' % (assignment['device'], assignment['operation']['uid'], assignment['fence'])).encode()).hexdigest()[:40]


def observation(state, name):
    return (get(state, '-n', NS, 'get', 'machineallocationhandoff', name).get('status') or {}).get('observation')


DEVICE = 'fleet-provider/synthetic-device-central-01'
OTHER_DEVICE = 'fleet-provider/synthetic-device-central-02'


def credential_name(device):
    """The only name a Device's credential Secret may have at the site."""
    return 'bmc-' + hashlib.sha256(device.encode()).hexdigest()[:20]


def worker_rbac(state, name):
    s = pathlib.Path(state)
    cfg = json.loads(kubectl(state, 'config', 'view', '--raw', '--minify', '-o', 'json').stdout)
    cfg['users'] = [{'name': 'worker', 'user': {'token': (s / 'worker.token').read_text().strip()}}]
    cfg['contexts'][0]['context']['user'] = 'worker'
    path = s / 'worker-kubeconfig.json'
    path.write_text(json.dumps(cfg))
    path.chmod(0o600)
    own = kubectl(state, '-n', NS, 'get', 'machineallocationhandoff', name, '-o', 'name', check=False, config='worker-kubeconfig.json')
    if own.returncode:
        raise SystemExit('worker cannot read its own handoff: ' + own.stderr[:200])
    denied = []
    for args in (('-n', NS, 'get', 'machineallocationhandoff', 'op-other-operation'), ('-n', NS, 'get', 'machineallocationhandoffs'),
                 ('-n', NS, 'patch', 'machineallocationhandoff', name, '--subresource=status', '--type=merge', '-p', '{"status":{}}'),
                 ('-n', NS, 'patch', 'machineallocationhandoff', name, '--type=merge', '-p', '{"spec":{"cell":"other"}}'),
                 ('-n', NS, 'get', 'secrets'), ('-n', SITE, 'get', 'secrets'), ('-n', SITE, 'create', 'configmap', 'forbidden', '--from-literal=x=y'), ('-n', SITE, 'get', 'jobs'),
                 # Another Device's credential, and listing or watching them, are
                 # denied in both modes.
                 ('-n', CREDENTIALS, 'get', 'secret', credential_name(OTHER_DEVICE)), ('-n', CREDENTIALS, 'get', 'secrets'),
                 ('-n', CREDENTIALS, 'get', 'secrets', '--watch', '--request-timeout=5s'),
                 *((('-n', CREDENTIALS, 'get', 'secret', credential_name(DEVICE)),) if not REDFISH else ())):
        r = kubectl(state, *args, check=False, config='worker-kubeconfig.json')
        if r.returncode == 0 or 'forbidden' not in r.stderr.lower():
            raise SystemExit('worker credential allowed: ' + ' '.join(args[:4]) + ' ' + r.stderr[:200])
        denied.append(args[1] + ' ' + ' '.join(args[2:4]) + (' (status)' if '--subresource=status' in args else ''))
    record = {'observedAt': now(), 'serviceAccount': 'w-' + name, 'ownHandoffReadable': True, 'forbidden': denied}
    role = get(state, '-n', NS, 'get', 'role', 'w-' + name)
    record.update(roleRules=role['rules'], roleCreatedBy=role['metadata'].get('labels', {}).get('kro.run/node-id'))
    if REDFISH:
        # A Redfish worker may get exactly its Device's credential Secret, the
        # one its handoff names, and nothing else.
        spec = get(state, '-n', NS, 'get', 'machineallocationhandoff', name)['spec']
        if spec.get('credential') != credential_name(spec['device']):
            raise SystemExit('the handoff does not name its Device credential Secret: %s' % spec.get('credential'))
        mine = kubectl(state, '-n', CREDENTIALS, 'get', 'secret', spec['credential'], '-o', 'name', check=False, config='worker-kubeconfig.json')
        if mine.returncode:
            raise SystemExit("Redfish worker cannot get its own Device's credential: " + mine.stderr[:200])
        creds = get(state, '-n', CREDENTIALS, 'get', 'role', 'w-' + name)
        if creds['rules'] != [{'apiGroups': [''], 'resources': ['secrets'], 'resourceNames': [spec['credential']], 'verbs': ['get']}]:
            raise SystemExit('Redfish worker credential Role is not get on its one Secret: %s' % creds['rules'])
        record.update(ownCredentialReadable=True, credentialRoleRules=creds['rules'])
    write(s / 'k8s-worker-rbac.json', record)
    print('worker-rbac: per-operation ServiceAccount reads only its own handoff; list, other names, status, spec, Secrets and site writes forbidden'
          + ("; it gets only its own Device's BMC credential Secret, never another's, and lists none" if REDFISH else ''))


def record_pod_interrupt(state, before, after):
    s = pathlib.Path(state)
    seen = json.loads((s / 'interrupt-observed.json').read_text())
    after_ops = installs(s / 'interrupt-after.json')
    jb = json.loads((s / 'interrupt-job-before-pod.json').read_text())
    ja = json.loads((s / 'interrupt-job-after-pod.json').read_text())
    if len(after_ops) != 1 or after_ops[0]['operation'] != seen['operation']['operation'] or after_ops[0]['done'] or after_ops[0]['phase']:
        raise SystemExit('interrupted operation is not the same unfinished operation without a result')
    if jb['uid'] != ja['uid'] or ja['failed'] < 1:
        raise SystemExit('Job controller did not account and replace the lost Pod in the same Job: %s' % ja)
    write(evidence(state) / 'interrupt.json', {
        'observedAt': now(), 'operationID': seen['operation']['operation'], 'fence': seen['operation']['fence'], 'domain': seen['operation']['domain'],
        'stageAtInterrupt': seen['operation']['stage'], 'domainRunningAtInterrupt': seen['domainRunning'],
        'podInterruption': {'method': 'worker Pod force-deleted with grace period 0', 'deletedPodUID': before, 'replacementPodUID': after,
                            'jobUID': jb['uid'], 'jobFailedPodsBefore': jb['failed'], 'jobFailedPodsAfter': ja['failed'], 'replacedBy': 'Job controller'},
        'localObservationAtInterrupt': None, 'stageAfterInterrupt': after_ops[0]['stage'], 'resultPhaseAfterInterrupt': after_ops[0]['phase']})


def record_job_interrupt(state, old, new, pod_before, pod_after):
    s = pathlib.Path(state)
    i = interrupt_record(state)
    before, after = installs(s / 'interrupt-job-before.json'), installs(s / 'interrupt-job-after.json')
    if len(before) != 1 or before[0]['done']:
        raise SystemExit('install finished before the Job could be deleted mid-operation')
    if len(after) != 1 or after[0]['operation'] != i['operationID'] or after[0]['fence'] != i['fence']:
        raise SystemExit('Job recreation created another install operation')
    i['jobRecreation'] = {'deletedJobUID': old, 'recreatedJobUID': new, 'recreatedBy': 'kro collection reconciliation',
                          'podBeforeUID': pod_before, 'podAfterUID': pod_after, 'stageAtDeletion': before[0]['stage'], 'stageAfterRecreation': after[0]['stage']}
    write(evidence(state) / 'interrupt.json', i)


def record_resume(state, name):
    s = pathlib.Path(state)
    i = interrupt_record(state)
    done = installs(s / 'resume-after.json')
    if len(done) != 1 or done[0]['operation'] != i['operationID'] or done[0]['fence'] != i['fence'] or not done[0]['done']:
        raise SystemExit('replacement workers did not complete the exact interrupted operation')
    # The observer published the stored result for exactly that assignment.
    result = (observation(state, name) or {}).get('result') or {}
    if result.get('phase') != 'Succeeded' or result.get('operation', {}).get('uid') != i['operationID'] or result.get('fence') != i['fence']:
        raise SystemExit('the observed result is not the interrupted operation Succeeded at its fence')
    j, ps = job(state, name), pods(state, name)
    if j['uid'] != i['jobRecreation']['recreatedJobUID'] or ['Complete', 'True', 'CompletionsReached'] not in j['conditions']:
        raise SystemExit('recreated Job did not complete: %s' % j)
    if [p['exitCode'] for p in ps if p['phase'] == 'Succeeded'] != [0]:
        raise SystemExit('expected exactly one succeeded worker Pod in the recreated Job: %s' % ps)
    i.update(resumedStage=done[0]['stage'], resumedPhase=result['phase'], installOperations=len(done), completedJob=j, completedJobPods=ps)
    write(evidence(state) / 'interrupt.json', i)


def qemu_starts(state, operation):
    # Redfish mode: the persistent lab target A carries every operation.
    domain = harness.LAB_DOMAIN if REDFISH else harness.domain_name(operation)
    r = kubectl(state, '-n', SITE, 'exec', 'deploy/fleet-site-runtime', '-c', 'libvirt', '--', 'cat', '/var/log/libvirt/qemu/' + domain + '.log', check=False)
    return r.stdout.count(': starting up ') if r.returncode == 0 else None


def lifecycle_snapshot(state, name):
    s = pathlib.Path(state)
    op = interrupt_record(state)['operationID']
    pvc = get(state, '-n', SITE, 'get', 'pvc', 'fleet-site-state')
    pv = get(state, 'get', 'pv', pvc['spec']['volumeName'])
    machine = get(state, '-n', 'default', 'get', 'machine', 'allocation', config='consumer-a/management-project.json')
    conds = {c['type']: c['status'] for c in machine.get('status', {}).get('conditions', [])}
    site_pods = {d: [p['metadata']['uid'] for p in get(state, '-n', SITE, 'get', 'pods', '-l', 'app.kubernetes.io/name=' + d)['items']]
                 for d in ('fleet-site-runtime', 'fleet-site-boot', 'fleet-site-observer')}
    print(json.dumps({'observedAt': now(), 'siteStoreSHA256': hashlib.sha256(runtime(state, 'cat', harness.STATE_PATH).encode()).hexdigest(),
                      'qemuStarts': qemu_starts(state, op), 'domains': runtime(state, 'virsh', '--connect', LIBVIRT, 'list', '--name', *([] if REDFISH else ['--all'])).split(),
                      'pvcUID': pvc['metadata']['uid'], 'pvUID': pv['metadata']['uid'], 'pvReclaimPolicy': pv['spec']['persistentVolumeReclaimPolicy'],
                      'machineA': {'phase': machine.get('status', {}).get('phase'), 'bound': conds.get('Bound'), 'ready': conds.get('Ready')},
                      'sitePods': site_pods, 'job': job(state, name)}, sort_keys=True))


def record_lifecycle(state, old, new):
    s = pathlib.Path(state)
    a, b = json.loads((s / 'lifecycle-before.json').read_text()), json.loads((s / 'lifecycle-after.json').read_text())
    for key in ('siteStoreSHA256', 'qemuStarts', 'pvcUID', 'pvUID', 'pvReclaimPolicy', 'domains', 'machineA'):
        if a[key] != b[key]:
            raise SystemExit('%s changed across Pod/Job lifecycle: %s -> %s' % (key, a[key], b[key]))
    if a['machineA'] != {'phase': 'Ready', 'bound': 'True', 'ready': 'True'} or a['qemuStarts'] != 2 or a['domains'] or a['pvReclaimPolicy'] != 'Retain':
        raise SystemExit('unexpected lifecycle baseline: %s' % a)
    for d, uids in a['sitePods'].items():
        if set(uids) & set(b['sitePods'][d]) or len(b['sitePods'][d]) != 1:
            raise SystemExit(d + ' Pod was not replaced exactly once')
    name = (s / 'install-handoff').read_text().strip()
    replay = [p for p in pods(state, name) if p['jobUID'] == new]
    if [p['exitCode'] for p in replay] != [0]:
        raise SystemExit('recreated completed Job did not finish with one verified no-op replay: %s' % replay)
    write(evidence(state) / 'lifecycle.json', {'observedAt': now(), 'recreated': ['fleet-site-runtime Pod', 'fleet-site-boot Pod', 'fleet-site-observer Pod', 'completed install Job'],
                                                'completedJobUIDBefore': old, 'completedJobUIDAfter': new, 'replayPods': replay,
                                                'unchanged': ['site store state bytes', 'QEMU starts (2)', 'PVC/PV identity', 'Retain reclaim policy', 'no domains', 'consumer-a Machine Ready/Bound'],
                                                'siteStoreSHA256': a['siteStoreSHA256'], 'observerRepublished': True, 'before': a, 'after': b})


FAULTS = ('duplicate', 'wrong-cell', 'stale-fence')
# Faults delivered to the cell; the duplicate never exists.
JOB_FAULTS = ('wrong-cell', 'stale-fence')


def handoff(name, cell, spec, fault):
    return {'apiVersion': 'coordination.fleet.datumapis.com/v1alpha1', 'kind': 'MachineAllocationHandoff',
            'metadata': {'name': name, 'namespace': NS, 'labels': {CELL_LABEL: cell, ROLE_LABEL: 'assignment', 'fleet.datumapis.com/fault': fault}}, 'spec': spec}


def delivery_faults(state, action):
    """Each fault derives from the latest delivered assignment, whose fence is
    the Device's stored fence: a duplicate of it, the same assignment for
    another cell, and the same operation below that fence."""
    s = pathlib.Path(state)
    cell = (s / 'prefix').read_text().strip() + '-cell'
    private = s / 'fault-handoffs.json'
    if action == 'create':
        handoffs = get(state, '-n', NS, 'get', 'machineallocationhandoffs', '-l', '%s=%s,%s=assignment' % (CELL_LABEL, cell, ROLE_LABEL))['items']
        if not handoffs:
            raise SystemExit('no delivered assignment to derive faults from')
        source = max(handoffs, key=lambda h: json.loads(h['spec']['assignmentJSON'])['fence'])
        assignment = json.loads(source['spec']['assignmentJSON'])
        if source['metadata']['name'] != assignment_name(assignment) or assignment['fence'] < 2:
            raise SystemExit('the stale-fence fault needs a delivered assignment under its own name with a fence of at least 2')
        faults = {}
        # A Redfish worker's Role names the Device's credential, so each fault
        # carries the source's; without it kro renders no Job.
        credential = {k: v for k, v in source['spec'].items() if k == 'credential'}
        # The same assignment again: its name is fixed by its contents, so the API refuses it.
        dup = handoff(source['metadata']['name'], cell, source['spec'], 'duplicate')
        r = kubectl(state, 'create', '-f', '-', input=json.dumps(dup), check=False)
        if r.returncode == 0 or 'AlreadyExists' not in r.stderr:
            raise SystemExit('a duplicate assignment was not refused as AlreadyExists: ' + r.stderr[:200])
        faults['duplicate'] = {'handoff': source['metadata']['name'], 'refused': 'AlreadyExists'}
        # Selected by the cell label, but the handoff names another cell.
        wrong = dict(assignment, operation=dict(assignment['operation'], uid=assignment['operation']['uid'] + '-fault-wrong-cell'))
        faults['wrong-cell'] = {'handoff': assignment_name(wrong), 'spec': {'cell': 'remote-elsewhere-cell', 'device': assignment['device'],
                                                                          'assignmentJSON': json.dumps(wrong, separators=(',', ':')), **credential}}
        # Well-formed for this cell under its own name, but below the stored fence.
        stale = dict(assignment, fence=assignment['fence'] - 1)
        faults['stale-fence'] = {'handoff': assignment_name(stale), 'operation': assignment['operation']['uid'], 'fence': stale['fence'],
                                 'spec': {'cell': cell, 'device': assignment['device'], 'assignmentJSON': json.dumps(stale, separators=(',', ':')), **credential}}
        for fault in JOB_FAULTS:
            f = faults[fault]
            kubectl(state, 'create', '-f', '-', input=json.dumps(handoff(f['handoff'], cell, f.pop('spec'), fault)))
            (s / ('fault-%s-job' % fault)).write_text('w-' + f['handoff'] + '\n')
        faults['source'] = {'handoff': source['metadata']['name'], 'operation': assignment['operation'], 'fence': assignment['fence']}
        write(private, faults)
        return
    faults = json.loads(private.read_text())
    if action == 'verify':
        record = {'observedAt': now(), 'source': faults['source'], 'faults': {'duplicate': faults['duplicate']}}
        for fault in JOB_FAULTS:
            name = faults[fault]['handoff']
            j, ps = job(state, name), pods(state, name)
            if ['Failed', 'True', 'PodFailurePolicy'] not in j['conditions'] or j['succeeded'] or [p['exitCode'] for p in ps] != [65]:
                raise SystemExit('%s fault Job did not fail once by podFailurePolicy exit 65: %s %s' % (fault, j, ps))
            seen = observation(state, name)
            if fault == 'wrong-cell' and seen:
                raise SystemExit('the wrong-cell handoff gained an observation')
            if fault == 'stale-fence' and (seen or {}).get('result'):
                raise SystemExit('the stale-fence handoff gained a result')
            record['faults'][fault] = {'handoff': name, 'job': j, 'pods': ps, 'observedFence': (seen or {}).get('fence'), 'result': None}
        stored = kubectl(state, '-n', SITE, 'exec', 'deploy/fleet-site-runtime', '-c', 'libvirt', '--', 'test', '-e',
                         '%s/%s-%d.json' % (harness.RESULTS_DIR, faults['stale-fence']['operation'], faults['stale-fence']['fence']), check=False)
        if stored.returncode == 0:
            raise SystemExit('the site stored a result for the stale-fence assignment')
        machine = get(state, '-n', 'default', 'get', 'machine', 'allocation', config='consumer-b/management-project.json')
        conds = {c['type']: c['status'] for c in machine.get('status', {}).get('conditions', [])}
        if machine.get('status', {}).get('phase') != 'Bound' or conds.get('Bound') != 'True':
            raise SystemExit('failed fault Jobs changed consumer-b allocation: %s' % machine.get('status'))
        record.update(siteStoreUnchanged=True, staleResultStored=False, consumerBStillBound=True)
        write(evidence(state) / 'delivery-faults.json', record)
        return
    names = [faults[f]['handoff'] for f in JOB_FAULTS]
    for name in names:
        kubectl(state, '-n', NS, 'delete', 'machineallocationhandoff', name, '--wait=true')
    for _ in range(120):
        left = [n for n in names if kubectl(state, '-n', SITE, 'get', 'job', 'w-' + n, check=False).returncode == 0]
        if not left:
            break
        subprocess.run(['sleep', '1'])
    else:
        raise SystemExit('kro did not prune fault Jobs: %s' % left)
    record = json.loads((evidence(state) / 'delivery-faults.json').read_text())
    record['prunedByKro'] = True
    write(evidence(state) / 'delivery-faults.json', record)


def main():
    cmd, state, *rest = sys.argv[1:]
    if cmd == 'running-pod':
        print(running_pod(state, rest[0]))
    elif cmd == 'job-pod':
        # Any Pod (any phase) created by one exact Job UID.
        print(next((p['uid'] for p in pods(state, rest[0]) if p['jobUID'] == rest[1]), ''))
    elif cmd == 'live-pod':
        # A scheduled or running replacement; used to interrupt as early as possible.
        print(running_pod(state, rest[0], ('Pending', 'Running')))
    elif cmd == 'job':
        print(json.dumps(job(state, rest[0]), sort_keys=True))
    elif cmd == 'worker-rbac':
        worker_rbac(state, rest[0])
    elif cmd == 'record-pod-interrupt':
        record_pod_interrupt(state, *rest)
    elif cmd == 'record-job-interrupt':
        record_job_interrupt(state, *rest)
    elif cmd == 'record-resume':
        record_resume(state, rest[0])
    elif cmd == 'lifecycle-snapshot':
        lifecycle_snapshot(state, rest[0])
    elif cmd == 'record-lifecycle':
        record_lifecycle(state, *rest)
    elif cmd == 'delivery-faults':
        delivery_faults(state, rest[0])
    else:
        raise SystemExit('unknown command ' + cmd)


if __name__ == '__main__':
    main()
