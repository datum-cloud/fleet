#!/usr/bin/env python3
"""Evidence checks for the #174 failure scenarios in the multi-plane runs.

Each command reads the provider project with its bootstrap credential and the
cell with its admin kubeconfig, checks one outcome, and writes a sanitized
record under evidence/. None of them writes custody or an operation.
"""
import datetime
import json
import pathlib
import subprocess
import sys
import time

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
import harness  # noqa: E402  (site store layout)

NS, SITE = 'fleet-coordination', 'fleet-site'
PROVIDER_NS, DEVICE = 'fleet-provider', 'synthetic-device-central-01'
ASSIGNMENTS = 'coordination.fleet.datumapis.com/role=assignment'
REDFISH = harness.PROVIDER == 'redfish'
# The Installation UID the tenant-isolation phase's refused installation
# labels (internal/multiplane/scenarios.go).
TENANT_REQUEST = '00000000-0000-4000-8000-000000000174'


def die(message):
    raise SystemExit(message)


def kubectl(s, config, *args, check=True, input=None):
    r = subprocess.run(['kubectl', '--kubeconfig', str(s / config), '--request-timeout=20s', *args], text=True, capture_output=True, input=input)
    if check and r.returncode:
        die('kubectl %s failed: %s' % (' '.join(args[:4]), r.stderr.strip()[:300]))
    return r


def get(s, config, *args):
    return json.loads(kubectl(s, config, *args, '-o', 'json').stdout)


def provider(s, *args):
    return get(s, 'provider/bootstrap-project.json', '-n', PROVIDER_NS, *args)


def custody(s):
    return provider(s, 'get', 'devicecustody', DEVICE)


def items(s, kind):
    return provider(s, 'get', kind)['items']


def write(s, name, record):
    record = dict(record, observedAt=datetime.datetime.now(datetime.timezone.utc).isoformat())
    (s / 'evidence' / name).write_text(json.dumps(record, indent=2, sort_keys=True) + '\n')


def wait_for(fn, timeout, what, interval=3):
    end = time.monotonic() + timeout
    while time.monotonic() < end:
        value = fn()
        if value:
            return value
        time.sleep(interval)
    die('timed out waiting for ' + what)


def bmc_effects(s):
    """Mutating BMC requests in the lab audit, in Redfish mode."""
    if not REDFISH:
        return []
    import redfish_steps
    return redfish_steps.mutating(redfish_steps.audit(s))


def admitted_key(op):
    """An operation's site key at the fence custody admitted it, or None
    before the coordinator recorded the admission."""
    fence = (op.get('status', {}).get('admission') or {}).get('fence')
    return harness.operation_key(op['kind'], op['metadata']['name'], fence) if fence else None


def handoff_for(s, key):
    """The assignment handoff for the operation admission with this site key."""
    for h in get(s, 'cell-admin.yaml', '-n', NS, 'get', 'machineallocationhandoffs', '-l', ASSIGNMENTS)['items']:
        a = json.loads(h['spec']['assignmentJSON'])
        if harness.operation_key(a['operation']['kind'], a['operation']['name'], a['fence']) == key:
            return h['metadata']['name']
    return None


def same_admission(ref, key):
    """Whether custody's operation reference (kind, name, fence) names the
    admission with this site key."""
    return bool(ref) and harness.operation_key(ref.get('kind', ''), ref.get('name', ''), ref.get('fence', 0)) == key


def import_inert(s):
    """Importing Devices admitted, delivered and sent nothing: no operation,
    allocation, assignment, worker Job or journal entry exists, and custody
    has no active operation, result or hold."""
    found = {kind: [o['metadata']['name'] for o in items(s, kind)] for kind in ('deviceinstallations', 'devicewipes', 'devicerecoveries', 'machineallocations')}
    if any(found.values()):
        die('records exist before any allocation: %s' % found)
    records = items(s, 'devicecustodies')
    devices = sorted(d['metadata']['name'] for d in items(s, 'devices'))
    if sorted(c['metadata']['name'] for c in records) != devices:
        die('custody does not exist for exactly the imported Devices')
    for c in records:
        spec = c['spec']
        if spec.get('activeOperation') or spec.get('lastResult') or spec.get('recoveryHold') or spec['holder']['type'] != 'None':
            die('imported custody is not idle: %s' % c['metadata']['name'])
    if get(s, 'cell-admin.yaml', '-n', NS, 'get', 'machineallocationhandoffs', '-l', ASSIGNMENTS)['items']:
        die('an assignment was delivered before any allocation')
    if get(s, 'cell-admin.yaml', '-n', SITE, 'get', 'jobs')['items']:
        die('a worker Job exists before any allocation')
    if harness.journal(s):
        die('the site journal holds an operation before any allocation')
    effects = bmc_effects(s)
    if effects:
        die('the BMC received an effect before any allocation: %s' % effects)
    write(s, 'import-inert.json', {'devices': devices, 'custody': {c['metadata']['name']: {'state': c['spec']['state'], 'fence': c['spec']['fence']} for c in records},
                                   'operations': 0, 'allocations': 0, 'assignments': 0, 'workerJobs': 0, 'journalOperations': 0,
                                   'bmcEffects': 0 if REDFISH else None})
    print('import-inert: %d imported Devices; no operation, allocation, assignment, worker Job or journal entry%s'
          % (len(devices), '; the BMC audit holds no effect' if REDFISH else ''))


def staff_access(s):
    """The staff identity creates recoveries and reads inventory and custody.
    It cannot read Secrets, where a bootstrap payload would be sealed, or
    write any other operation, allocation or custody. It holds no consumer
    project or hub credential."""
    staff = 'provider/staff-project.json'
    probe = {'apiVersion': 'fleet.datumapis.com/v1alpha1', 'metadata': {'name': 'staff-probe', 'namespace': PROVIDER_NS}, 'spec': {}}
    denied = {
        'get Secrets': ('-n', PROVIDER_NS, 'get', 'secrets'),
        'get Secrets in default': ('-n', 'default', 'get', 'secrets'),
        'create a DeviceInstallation': ('create', '--dry-run=server', '-f', '-', json.dumps(dict(probe, kind='DeviceInstallation'))),
        'create a DeviceWipe': ('create', '--dry-run=server', '-f', '-', json.dumps(dict(probe, kind='DeviceWipe'))),
        'create a MachineAllocation': ('create', '--dry-run=server', '-f', '-', json.dumps(dict(probe, apiVersion='metal.datumapis.com/v1alpha1', kind='MachineAllocation'))),
        'update custody': ('-n', PROVIDER_NS, 'annotate', '--dry-run=server', 'devicecustody', DEVICE, 'probe=staff'),
        'delete custody': ('-n', PROVIDER_NS, 'delete', '--dry-run=server', 'devicecustody', DEVICE),
    }
    results = {}
    for name, args in denied.items():
        stdin = None
        if args[-2:-1] == ('-',):
            args, stdin = args[:-1], args[-1]
        r = kubectl(s, staff, *args, check=False, input=stdin)
        if r.returncode == 0 or 'forbidden' not in r.stderr.lower():
            die('staff was not forbidden to %s: %s' % (name, r.stderr.strip()[:200]))
        results[name] = 'Forbidden'
    for name, args in {'read custody': ('-n', PROVIDER_NS, 'get', 'devicecustody', DEVICE), 'list recoveries': ('-n', PROVIDER_NS, 'get', 'devicerecoveries')}.items():
        if kubectl(s, staff, *args, check=False).returncode:
            die('staff cannot ' + name)
        results[name] = 'Allowed'
    elsewhere = [str(p.relative_to(s)) for p in s.glob('*/staff-project.json') if p.parent.name != 'provider'] + [str(p.relative_to(s)) for p in s.glob('staff*.yaml')]
    if elsewhere:
        die('staff holds a credential outside the provider project: %s' % elsewhere)
    write(s, 'staff-access.json', {'checks': results, 'credentials': ['provider project'], 'bootstrapPayloads': 'refused at admission and at the site'})
    print('staff-access: staff read custody and list recoveries; Secrets, other operations, allocations and custody writes forbidden; no consumer or hub credential')


def contention(s):
    """Custody names the winner's MachineAllocation, and it is the only one."""
    allocations = [a for a in items(s, 'machineallocations') if a['spec']['deviceRef']['name'] == DEVICE]
    winner = get(s, 'consumer-a/management-project.json', '-n', 'default', 'get', 'machine', 'allocation')
    # The allocation labels the Machine incarnation it is for.
    if len(allocations) != 1 or allocations[0]['metadata'].get('labels', {}).get('fleet.datumapis.com/machine-uid') != winner['metadata']['uid']:
        die('expected one MachineAllocation, for consumer-a: %s' % [a['metadata']['name'] for a in allocations])
    holder = custody(s)['spec']['holder']
    if holder['type'] != 'Allocation' or (holder.get('allocationRef') or {}).get('name') != allocations[0]['metadata']['name']:
        die('custody does not name the winning allocation: %s' % holder)
    write(s, 'contention.json', {'allocations': 1, 'holder': holder['type'], 'holderNamesWinner': True})
    print('contention-custody: custody holds the Device for the one MachineAllocation, consumer-a\'s')


def holder_install(s):
    """The one installation for the allocation custody holds, ignoring the
    tenant-isolation phase's refused installation."""
    allocation = (custody(s)['spec']['holder'].get('allocationRef') or {}).get('name')
    ops = [o for o in items(s, 'deviceinstallations') if (o['spec']['authority'].get('allocationRef') or {}).get('name') == allocation
           and o['metadata'].get('labels', {}).get('fleet.datumapis.com/installation-uid') != TENANT_REQUEST]
    if not allocation or len(ops) != 1:
        die('expected one installation for the held allocation %s, found %d' % (allocation, len(ops)))
    return ops[0]


def wait_installer(s, step):
    """Record the held allocation's installation and its handoff, and wait
    until it journaled its installer step. It fails at once if the
    installation already ended, so a slow start is reported as itself."""
    name = holder_install(s)['metadata']['name']
    # The site keys the operation by kind, name and the fence it was admitted
    # at; a same-name installation created later is admitted at another.
    key = wait_for(lambda: admitted_key(provider(s, 'get', 'deviceinstallation', name)), 300, 'the installation to be admitted', interval=1)
    handoff = wait_for(lambda: handoff_for(s, key), 300, 'the installation to be delivered', interval=1)
    (s / 'deadline-install.json').write_text(json.dumps({'name': name, 'key': key, 'handoff': handoff}) + '\n')

    def journaled():
        phase = deadline_op(s)[0].get('status', {}).get('phase')
        if phase in ('Succeeded', 'Failed', 'Cancelled', 'OutcomeUnknown'):
            die('the installation ended %s before it journaled %s; its deadline left too little time' % (phase, step))
        for o in harness.journal(s) or []:
            if o['operation'] == key and any(n == step for n, _ in o['steps']):
                return o
        return None
    wait_for(journaled, 600, 'the second installation to journal %s' % step, interval=1)


def deadline_op(s):
    ref = json.loads((s / 'deadline-install.json').read_text())
    op = provider(s, 'get', 'deviceinstallation', ref['name'])
    if admitted_key(op) != ref['key']:
        die('the deadline installation was replaced')
    return op, ref['key']


def wipes_for(s, allocation):
    return [w for w in items(s, 'devicewipes') if (w['spec'].get('authority', {}).get('allocationRef') or {}).get('name') == allocation]


def release_waits(s):
    """The Machine is deleted mid-install. Release begins, and waits: custody
    holds the Device for release with the installation still active, and no
    wipe exists."""
    op, key = deadline_op(s)
    allocation = op['spec']['authority']['allocationRef']['name']
    spec = custody(s)['spec']
    active = spec.get('activeOperation') or {}
    if spec['holder']['type'] != 'Release' or not same_admission(active, key):
        die('release did not wait for the installation: holder %s, active %s' % (spec['holder']['type'], active.get('kind')))
    if wipes_for(s, allocation):
        die('a wipe was created while the installation was active')
    machine = kubectl(s, 'consumer-b/management-project.json', '-n', 'default', 'get', 'machine', 'allocation', '-o', 'json')
    m = json.loads(machine.stdout)
    if not m['metadata'].get('deletionTimestamp'):
        die('the Machine is not being deleted')
    write(s, 'release-waits.json', {'holder': 'Release', 'activeOperation': 'DeviceInstallation', 'wipes': 0, 'machineDeleting': True,
                                    'openGates': spec['holder'].get('openGates', [])})
    print('release-waits: the Machine is deleting; custody holds the Device for release with the installation active and no wipe')


def deadline_hold(s):
    """The installation passes its deadline: it ends OutcomeUnknown with
    DeadlineExceeded, custody holds it, and its worker has stopped."""
    def ended():
        op, _ = deadline_op(s)
        return op if op.get('status', {}).get('phase') == 'OutcomeUnknown' else None
    op = wait_for(ended, 900, 'the installation to end OutcomeUnknown')
    key = admitted_key(op)
    status = op['status']
    if status.get('reason') != 'DeadlineExceeded':
        die('the installation ended OutcomeUnknown for %s, not its deadline' % status.get('reason'))
    hold = custody(s)['spec'].get('recoveryHold') or {}
    if not same_admission(hold, key) or hold.get('fence') != status.get('fence'):
        die('custody does not hold the installation: %s' % hold.get('kind'))
    # The handoff was recorded at delivery: its worker Pods must show the
    # worker stopped. A missing Pod is a failure, never a pass.
    handoff = json.loads((s / 'deadline-install.json').read_text())['handoff']

    def stopped():
        pods = get(s, 'cell-admin.yaml', '-n', SITE, 'get', 'pods', '-l', 'fleet.datumapis.com/handoff=' + handoff)['items']
        ends = [c['state']['terminated']['finishedAt'] for p in pods for c in p['status'].get('containerStatuses', []) if 'terminated' in c.get('state', {})]
        return ends if pods and len(ends) == len(pods) else None
    finished = wait_for(stopped, 300, 'the installation worker to stop')
    boots = [b for b in harness.journal(s) or [] if b['operation'] == key]
    record = {'phase': 'OutcomeUnknown', 'reason': 'DeadlineExceeded', 'fence': status['fence'], 'deadline': status.get('deadline'),
              'holdFence': hold['fence'], 'workerFinishedAt': max(finished), 'journaledSteps': [n for o in boots for n, _ in o['steps']]}
    (s / 'deadline-hold.json').write_text(json.dumps(record) + '\n')
    write(s, 'deadline-hold.json', record)
    print('deadline-hold: the installation ended OutcomeUnknown at its deadline; custody holds it at fence %d; its worker stopped' % status['fence'])


def deadline_settled(s):
    """The release's wipe settled the installation hold: admitted above the
    held fence after the worker stopped, Succeeded, the hold cleared, the
    Device free and the Machine gone."""
    op, _ = deadline_op(s)
    held = json.loads((s / 'deadline-hold.json').read_text())
    allocation = op['spec']['authority']['allocationRef']['name']
    wipes = [w for w in wipes_for(s, allocation) if w.get('status', {}).get('phase') == 'Succeeded']
    if len(wipes) != 1:
        die('expected one succeeded wipe for the released allocation')
    wipe = wipes[0]['status']
    spec = custody(s)['spec']
    if wipe['fence'] <= held['fence'] or spec.get('recoveryHold') or spec['holder']['type'] != 'None':
        die('the wipe did not settle the installation hold: wipe fence %s, held fence %s, hold %s' % (wipe['fence'], held['fence'], bool(spec.get('recoveryHold'))))
    if wipe['admittedAt'] < held['workerFinishedAt']:
        die('the wipe was admitted before the installation worker stopped')
    r = kubectl(s, 'consumer-b/management-project.json', '-n', 'default', 'get', 'machine', 'allocation', check=False)
    if r.returncode == 0 or 'NotFound' not in r.stderr:
        die('the deleted Machine remains')
    store, _ = harness.site_store(s)
    write(s, 'deadline-settled.json', {'heldFence': held['fence'], 'wipeFence': wipe['fence'], 'wipeAdmittedAt': wipe['admittedAt'],
                                       'workerFinishedAt': held['workerFinishedAt'], 'siteStopped': (store or {}).get('stopped'),
                                       'holdCleared': True, 'holder': 'None', 'machineDeleted': True})
    print('deadline-settled: the release wipe was admitted at fence %d after the worker stopped, succeeded and cleared the hold; the Machine is gone' % wipe['fence'])


def rebuild_unverified(s):
    """Custody was deleted. The coordinator rebuilds it Unverified and keeps
    it there while no site reports its fence."""
    def rebuilt():
        r = kubectl(s, 'provider/bootstrap-project.json', '-n', PROVIDER_NS, 'get', 'devicecustody', DEVICE, '-o', 'json', check=False)
        return json.loads(r.stdout) if r.returncode == 0 else None
    c = wait_for(rebuilt, 300, 'custody to be rebuilt')
    old = json.loads((s / 'rebuild-before.json').read_text())
    if c['metadata']['uid'] == old['uid']:
        die('custody was not deleted')
    if c['spec']['state'] != 'Unverified':
        die('rebuilt custody is %s before any site report' % c['spec']['state'])
    store, _ = harness.site_store(s)
    record = {'oldFence': old['fence'], 'rebuiltState': 'Unverified', 'rebuiltHolder': c['spec']['holder']['type'], 'siteFence': (store or {}).get('fence'),
              'siteStopped': (store or {}).get('stopped')}
    (s / 'rebuild-unverified.json').write_text(json.dumps(record) + '\n')
    print('rebuild-unverified: custody rebuilt Unverified with holder %s while the site cannot report' % record['rebuiltHolder'])


def rebuild_before(s):
    c = custody(s)
    if c['spec']['state'] != 'Verified' or c['spec']['holder']['type'] != 'None':
        die('the rebuild starts from free, Verified custody')
    (s / 'rebuild-before.json').write_text(json.dumps({'uid': c['metadata']['uid'], 'fence': c['spec']['fence']}) + '\n')


def rebuild_verified(s):
    """After the site reports again, custody is Verified with a fence above
    every fence the site stored."""
    before = json.loads((s / 'rebuild-unverified.json').read_text())
    c = wait_for(lambda: (lambda c: c if c['spec']['state'] == 'Verified' else None)(custody(s)), 600, 'rebuilt custody to be Verified')
    site_fence = max(before['siteFence'] or 0, before['siteStopped'] or 0)
    if c['spec']['fence'] <= site_fence or c['spec']['fence'] <= before['oldFence']:
        die('rebuilt custody fence %d is not above the site fence %d and the old fence %d' % (c['spec']['fence'], site_fence, before['oldFence']))
    probe = get(s, 'consumer-a/management-project.json', '-n', 'default', 'get', 'machine', 'allocation')['metadata']['uid']
    allocations = items(s, 'machineallocations')
    # An allocation labels the Machine incarnation it is for.
    probe_allocated = any(a['metadata'].get('labels', {}).get('fleet.datumapis.com/machine-uid') == probe for a in allocations)
    if probe_allocated or any(a.get('status', {}).get('phase') != 'Released' for a in allocations):
        die('an allocation was recorded during the rebuild')
    write(s, 'rebuild.json', dict(before, verifiedFence=c['spec']['fence'], verifiedHolder=c['spec']['holder']['type'], probeAllocated=probe_allocated))
    print('rebuild-verified: custody Verified at fence %d, above the site fence %d; nothing was allocated while Unverified' % (c['spec']['fence'], site_fence))


if __name__ == '__main__':
    command, state = sys.argv[1], pathlib.Path(sys.argv[2])
    {'import-inert': import_inert, 'staff-access': staff_access, 'contention': contention, 'release-waits': release_waits,
     'deadline-hold': deadline_hold, 'deadline-settled': deadline_settled, 'rebuild-before': rebuild_before,
     'rebuild-unverified': rebuild_unverified, 'rebuild-verified': rebuild_verified,
     'wait-installer': lambda s: wait_installer(s, sys.argv[3])}[command](state)
