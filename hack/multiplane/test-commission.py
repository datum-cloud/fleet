#!/usr/bin/env python3
"""Offline checks for the commission phase: the harness classifies a
commissioning's journal entries as their own kind, each lab identity the
phase uses holds exactly the permissions the phase needs, and the install's
delivery checks ignore only the completed commissioning's handoff and Job."""
import ast
import json
import os
import pathlib
import re
import subprocess
import sys
import tempfile

root = pathlib.Path(sys.argv[1])
sys.path.insert(0, str(root / 'hack/multiplane'))
import harness  # noqa: E402

failures = []


def check(name, ok, detail=''):
    print(('PASS ' if ok else 'FAIL ') + name + (': ' + detail if detail and not ok else ''))
    if not ok:
        failures.append(name)


# A Redfish and a simulated commissioning are their own kind, finished at their
# last burn-in step, and never counted as an install.
store = {'journal': [
    {'operation': 'c1', 'fence': 1, 'step': 'burnin-power-off', 'state': 'Done'},
    {'operation': 'c1', 'fence': 1, 'step': 'burnin-arm', 'state': 'Done'},
    {'operation': 'c1', 'fence': 1, 'step': 'burnin-power-on', 'state': 'Done'},
    {'operation': 'c1', 'fence': 1, 'step': 'burnin-verified', 'state': 'Done'},
    {'operation': 'i1', 'fence': 2, 'step': 'installer-power-on', 'state': 'Done'},
    {'operation': 'i1', 'fence': 2, 'step': 'disk-verified', 'state': 'Done'},
    {'operation': 's1', 'fence': 3, 'step': 'simulated-burnin', 'state': 'Done'},
]}
ops = {o['operation']: o for o in harness.sanitized_operations(store, redfish=True)}
check('a Redfish commissioning is its own kind', ops['c1']['kind'] == 'DeviceCommissioning' and ops['c1']['done'], str(ops['c1']))
check('a simulated commissioning is its own kind', ops['s1']['kind'] == 'DeviceCommissioning' and ops['s1']['done'], str(ops['s1']))
check('an install stays an install', ops['i1']['kind'] == 'DeviceInstallation')
check('custody evidence reads commissionings', 'devicecommissionings.fleet.datumapis.com' in harness.CUSTODY_KINDS)

# Each identity the phase acts as holds the permissions for what it does.
milo = (root / 'hack/multiplane/milo.py').read_text()
block = milo[milo.index("  components={"):milo.index("  for who,component in components.items():")]
components = ast.literal_eval(block.split('=', 1)[1].strip())


def allows(who, group, resource, verb):
    return any(group in r['apiGroups'] and resource in r['resources'] and verb in r['verbs'] for r in components.get(who, []))


need = {
    'plan-author': [('fleet.datumapis.com', 'devicecommissioningplans', v) for v in ('create', 'get')],
    'plan-approver': [('fleet.datumapis.com', 'devicecommissioningplanapprovals', 'create')],
    'operator': [('fleet.datumapis.com', 'devices', 'get'), ('fleet.datumapis.com', 'devicecommissionings', 'list'), ('fleet.datumapis.com', 'devicecommissionings', 'create')],
    'capacity-operator': [('fleet.datumapis.com', 'devices', 'get'), ('metal.datumapis.com', 'machinecapacities', 'get'), ('metal.datumapis.com', 'machinecapacities', 'update')],
}
for who, rules in need.items():
    for group, resource, verb in rules:
        check('%s may %s %s' % (who, verb, resource), allows(who, group, resource, verb))
check('the commissioning operator cannot change capacity', not allows('operator', 'metal.datumapis.com', 'machinecapacities', 'update'))
check('the plan author cannot approve', not allows('plan-author', 'fleet.datumapis.com', 'devicecommissioningplanapprovals', 'create'))
check('only the projector writes capacity status', [w for w in components if allows(w, 'metal.datumapis.com', 'machinecapacities/status', 'update')] == ['projector'])

# The phase reads and enables capacity only as the capacity operator.
code = (root / 'internal/multiplane/commission.go').read_text()
capacity_calls = re.findall(r'(\w+)\.(Get|Update)\(ctx, (?:client\.ObjectKeyFromObject\()?capacity', code)
check('capacity is read and enabled as the capacity operator', capacity_calls and all(c[0] == 'capacityOperator' for c in capacity_calls), str(capacity_calls))

# Dispatch is on for the commissioning, so its handoff and Job outlive the
# commission phase. The install's checks ignore exactly that handoff and Job,
# and still fail when the install's handoff or Job appears early. The real
# step and fault scripts run against a fake kubectl.
FAKE = r'''#!PYTHON
import json, os, sys
world = json.load(open(os.environ['FAKE_WORLD']))
args = [a for a in sys.argv[1:] if not a.startswith('--request-timeout')]
config = args[args.index('--kubeconfig') + 1]
args = args[args.index('--kubeconfig') + 2:]
handoffs = world['hub'] if config.endswith('hub-admin.yaml') else world['cell']
if 'wait' in args:
    sys.exit(0 if any(a.endswith('=%d' % world['delivered']) for a in args) else 1)
kind = args[args.index('get') + 1] if 'get' in args else None
output = args[args.index('-o') + 1] if '-o' in args else ''
if kind == 'machineallocationhandoffs' and output == 'json':
    print(json.dumps({'items': handoffs}))
elif kind == 'machineallocationhandoffs' and output == 'name':
    print('\n'.join('machineallocationhandoff/' + h['metadata']['name'] for h in handoffs))
elif kind == 'machineallocationhandoff' and output.startswith('jsonpath'):
    print('')
elif kind == 'machineallocationhandoff' and output == 'json':
    name = args[args.index('get') + 2]
    print(json.dumps(next(h for h in handoffs if h['metadata']['name'] == name)))
elif kind == 'jobs' and output == 'json':
    print(json.dumps({'items': [{'metadata': {'name': j}} for j in world['jobs']]}))
elif kind == 'jobs' and output == 'name':
    print('\n'.join('job.batch/' + j for j in world['jobs']))
else:
    sys.exit(1)
'''.replace('#!PYTHON', '#!' + sys.executable)


def handoff(name, kind):
    return {'metadata': {'name': name, 'labels': {}}, 'spec': {'assignmentJSON': json.dumps({'operation': {'kind': kind, 'uid': name}, 'fence': 1})}, 'status': {}}


commissioning, install, wipe = handoff('op-commission', 'DeviceCommissioning'), handoff('op-install', 'DeviceInstallation'), handoff('op-wipe', 'DeviceWipe')
work = pathlib.Path(tempfile.mkdtemp())
(work / 'bin').mkdir()
(work / 'bin' / 'kubectl').write_text(FAKE)
(work / 'bin' / 'kubectl').chmod(0o755)
env = dict(os.environ, PATH=str(work / 'bin') + os.pathsep + os.environ['PATH'], FAKE_WORLD=str(work / 'world.json'))


def run(world, command, recorded=True):
    state = pathlib.Path(tempfile.mkdtemp(dir=work))
    (state / 'prefix').write_text('fleet-mp-0123abcd\n')
    if recorded:
        (state / 'commission-handoffs').write_text('op-commission\n')
    (work / 'world.json').write_text(json.dumps(dict({'hub': [], 'cell': [], 'jobs': [], 'delivered': 0}, **world)))
    result = subprocess.run([c.replace('STATE', str(state)) for c in command], cwd=root, env=env, text=True, capture_output=True, timeout=60)
    return result, state


def step(name, world, recorded=True):
    return run(world, ['bash', 'hack/multiplane/steps.sh', name, 'STATE'], recorded)


r, s = run({'cell': [commissioning]}, [sys.executable, 'hack/multiplane/k8s_evidence.py', 'record-commissioning', 'STATE'], recorded=False)
check('settle records the commissioning handoff', r.returncode == 0 and (s / 'commission-handoffs').read_text() == 'op-commission\n', r.stderr)
r, _ = run({'cell': [commissioning, install]}, [sys.executable, 'hack/multiplane/k8s_evidence.py', 'record-commissioning', 'STATE'], recorded=False)
check('settle fails when the install handoff appears before allocation', r.returncode and 'exactly one assignment' in r.stderr, r.stderr)
r, _ = run({'cell': [install]}, [sys.executable, 'hack/multiplane/k8s_evidence.py', 'install-assignments', 'STATE'], recorded=False)
check('install checks refuse to run without the recorded commissioning', r.returncode and 'no commissioning handoff was recorded' in r.stderr, r.stderr)

world = {'cell': [commissioning, install], 'jobs': ['w-op-commission'], 'delivered': 2}
r, s = step('await-delivery', world)
check('await-delivery ignores the completed commissioning', r.returncode == 0 and (s / 'install-handoff').read_text() == 'op-install\n', r.stderr)
r, _ = step('await-delivery', dict(world, cell=[commissioning, install, wipe], delivered=3))
check('await-delivery fails on a second install-side assignment', r.returncode and 'more than one delivered assignment' in r.stderr, r.stderr)
r, _ = step('await-delivery', dict(world, jobs=['w-op-commission', 'w-op-install']))
check('await-delivery fails when the install Job exists before dispatch', r.returncode and 'worker Job exists before dispatch' in r.stderr, r.stderr)
r, _ = step('await-delivery', dict(world, delivered=1))
check('await-delivery counts the commissioning in kro\'s total', r.returncode != 0, r.stderr)
r, _ = step('commission-dispatch', {'cell': [install]})
check('commission-dispatch fails when an assignment already exists', r.returncode and 'an assignment exists before commissioning' in r.stderr, r.stderr)

# reflection-faults reads the hub: past its count check it next requires the
# install's result to be answered, which the fake never says.
faults = [sys.executable, 'hack/multiplane/reflection-faults.py', 'STATE']
r, _ = run({'hub': [commissioning, install]}, faults)
check('reflection faults ignore the completed commissioning', 'the install result is not answered yet' in r.stderr, r.stderr)
for name, hub in (('an extra assignment', [commissioning, install, wipe]), ('no install', [commissioning])):
    r, _ = run({'hub': hub}, faults)
    check('reflection faults refuse ' + name, r.returncode and 'run reflection faults after install and before the release wipe' in r.stderr, r.stderr)

# The install checks that cannot run offline use the same filters.
steps = (root / 'hack/multiplane/steps.sh').read_text()
for name, call in (('tenant-isolation', 'install_assignments'), ('partition', 'install_jobs')):
    body = steps[steps.index(' %s)\n' % name):]
    check('%s uses the install filter' % name, call in body[:body.index(';;')])

print('PASS commission tests' if not failures else 'FAIL commission tests')
sys.exit(1 if failures else 0)
