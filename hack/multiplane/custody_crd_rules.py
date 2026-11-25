#!/usr/bin/env python3
"""Prove the provider project's Milo API enforces the Device custody CRD rules.

Milo v0.32.5 ignores admission policies stored in a project, so the custody
contract rests on the CRD schema and CEL rules the project API server itself
evaluates. The provider project serves the device-operations CRDs for the
whole run. This phase tries each forbidden change against synthetic objects
in the fleet-rules namespace, which the coordinator never reads, requires
the API server to refuse it, and removes the objects. It sends nothing to
any BMC.
"""
import json, pathlib, subprocess, sys

state = pathlib.Path(sys.argv[1]).resolve()
config = state / 'provider' / 'bootstrap-project.json'
# The namespace the fixture bootstrap creates for this check. The phase
# creates no namespace, because a project API may not run a namespace
# controller to finish deleting one.
namespace = 'fleet-rules'
objects = (('devicerecovery', 'rules-recovery'), ('devicecustody', 'server-example-001'))
names = ('devicecustodies', 'deviceinstallations', 'devicewipes', 'devicerecoveries')


def kubectl(*args, stdin=None, check=True):
    result = subprocess.run(['kubectl', '--kubeconfig', str(config), '--request-timeout=20s', *args],
                            input=stdin, text=True, capture_output=True)
    if check and result.returncode != 0:
        raise SystemExit('custody-crd-rules: kubectl %s failed' % args[0])
    return result


def apply(obj):
    kubectl('apply', '-f', '-', stdin=json.dumps(obj))


def refused(what, *args, stdin=None):
    """The API server must refuse the write as Invalid, naming the rule."""
    result = kubectl(*args, stdin=stdin, check=False)
    text = result.stderr
    ok = result.returncode != 0 and 'invalid' in text.lower() and what in text
    checks.append({'rule': what, 'refused': ok})
    if not ok:
        raise SystemExit('custody-crd-rules: the project API accepted a write that breaks: ' + what)


def status(kind, name, phase):
    return kubectl('-n', namespace, 'patch', kind, name, '--subresource=status', '--type=merge',
                   '-p', json.dumps({'status': {'phase': phase}}), check=False)


checks = []
removed = False
try:
    for name in names:
        kubectl('wait', '--for=condition=Established', 'crd/%s.fleet.datumapis.com' % name, '--timeout=90s')
    envelope = {'creator': {'username': 'staff-example', 'uid': 'staff-example-uid'},
                'deviceRef': {'name': 'server-example-001', 'uid': '00000000-0000-4000-8000-000000000001'},
                'authority': {'serviceRequest': {'reason': 'synthetic rule check'}}}
    recovery = {'apiVersion': 'fleet.datumapis.com/v1alpha1', 'kind': 'DeviceRecovery',
                'metadata': {'name': 'rules-recovery', 'namespace': namespace},
                'spec': dict(envelope, decision='Settle', hold={'kind': 'DeviceWipe', 'name': 'alloc-example-wipe-1', 'fence': 3},
                             evidence={'observedAt': '2026-01-02T03:04:05Z', 'summary': 'synthetic'})}
    apply(recovery)
    refused('an operation spec is immutable after create', '-n', namespace, 'patch', 'devicerecovery', 'rules-recovery',
            '--type=merge', '-p', json.dumps({'spec': {'decision': 'Retry'}}))
    refused('an operation spec is immutable after create', '-n', namespace, 'patch', 'devicerecovery', 'rules-recovery',
            '--type=merge', '-p', json.dumps({'spec': {'creator': {'username': 'someone-else'}}}))
    for phase in ('Pending', 'Admitted', 'Verifying'):
        if status('devicerecovery', 'rules-recovery', phase).returncode != 0:
            raise SystemExit('custody-crd-rules: a forward phase change was refused')
    refused('operation phases move forward only', '-n', namespace, 'patch', 'devicerecovery', 'rules-recovery', '--subresource=status',
            '--type=merge', '-p', json.dumps({'status': {'phase': 'Admitted'}}))
    if status('devicerecovery', 'rules-recovery', 'OutcomeUnknown').returncode != 0:
        raise SystemExit('custody-crd-rules: ending OutcomeUnknown was refused')
    refused('an ended phase is final', '-n', namespace, 'patch', 'devicerecovery', 'rules-recovery', '--subresource=status',
            '--type=merge', '-p', json.dumps({'status': {'phase': 'Succeeded'}}))
    both = json.loads(json.dumps(recovery))
    both['metadata']['name'] = 'rules-both'
    both['metadata']['namespace'] = namespace
    both['spec']['authority'] = {'serviceRequest': {'reason': 'x'}, 'allocationRef': {'name': 'alloc-example'}}
    refused('set exactly one of allocationRef and serviceRequest', 'create', '-f', '-', stdin=json.dumps(both))
    custody = {'apiVersion': 'fleet.datumapis.com/v1alpha1', 'kind': 'DeviceCustody',
               'metadata': {'name': 'server-example-001', 'namespace': namespace},
               'spec': {'deviceRef': envelope['deviceRef'], 'state': 'Verified', 'holder': {'type': 'Retired'}, 'fence': 5}}
    apply(custody)
    refused('the fence never decreases', '-n', namespace, 'patch', 'devicecustody', 'server-example-001',
            '--type=merge', '-p', json.dumps({'spec': {'fence': 4}}))
    refused('Retired is terminal', '-n', namespace, 'patch', 'devicecustody', 'server-example-001',
            '--type=merge', '-p', json.dumps({'spec': {'holder': {'type': 'None'}}}))
    mismatch = json.loads(json.dumps(custody))
    mismatch['metadata']['name'] = 'server-example-002'
    mismatch['metadata']['namespace'] = namespace
    refused('custody has the same name as its Device', 'create', '-f', '-', stdin=json.dumps(mismatch))
finally:
    # The rule-check custody carries no finalizer: nothing reconciles this
    # namespace, so it is deleted at once.
    removed = all(kubectl('-n', namespace, 'delete', kind, name, '--ignore-not-found', '--wait=true', '--timeout=120s', check=False).returncode == 0
                  for kind, name in objects)

if not removed:
    raise SystemExit('custody-crd-rules: the rule-check objects could not be removed')
evidence = state / 'evidence'
evidence.mkdir(mode=0o700, exist_ok=True)
path = evidence / 'custody-crd-rules.json'
path.write_text(json.dumps({'checks': checks, 'objectsRemoved': removed}, indent=1) + '\n')
path.chmod(0o600)
print('custody-crd-rules: the provider project API refused every forbidden custody and operation write (%d rules); objects removed' % len(checks), flush=True)
