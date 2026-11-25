#!/usr/bin/env python3
"""Offline checks for the commission phase: the harness classifies a
commissioning's journal entries as their own kind, and each lab identity the
phase uses holds exactly the permissions the phase needs."""
import ast
import pathlib
import re
import sys

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

print('PASS commission tests' if not failures else 'FAIL commission tests')
sys.exit(1 if failures else 0)
