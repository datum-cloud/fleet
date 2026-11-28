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
objects = (('devicerecovery', 'rules-recovery'), ('devicecustody', 'server-example-001'), ('devicecommissioning', 'rules-commissioning'),
           ('devicecommissioningplanapproval', 'rules-plan-0123456789abcdef'), ('devicecommissioningplan', 'rules-plan'))
names = ('devicecustodies', 'deviceinstallations', 'devicewipes', 'devicerecoveries', 'devicecommissionings', 'devicecommissioningplans',
         'devicecommissioningplanapprovals')
DIGEST = 'sha256:' + '0' * 64


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


def copy(obj, name=None, **spec):
    """A deep copy of obj, renamed and with spec fields replaced."""
    out = json.loads(json.dumps(obj))
    if name:
        out['metadata']['name'] = name
    out['spec'].update(spec)
    return out


def status(kind, name, phase):
    return kubectl('-n', namespace, 'patch', kind, name, '--subresource=status', '--type=merge',
                   '-p', json.dumps({'status': {'phase': phase}}), check=False)


checks = []
removed = False
try:
    for name in names:
        kubectl('wait', '--for=condition=Established', 'crd/%s.fleet.datumapis.com' % name, '--timeout=90s')
    envelope = {'creator': {'username': 'staff-example', 'uid': 'staff-example-uid'},
                'deviceRef': {'name': 'server-example-001'},
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

    # Commissioning plans, their approvals and commissionings. The objects are
    # synthetic; the coordinator never reads this namespace, so nothing
    # qualifies the plan or admits the commissioning.
    content = {'inspection': {'cpu': {'architecture': 'amd64', 'cores': 2}, 'memory': {'capacity': '2Gi'},
                              'disks': {'count': 1, 'minCapacity': '1Gi'}, 'interfaces': {'count': 1}},
               'burnIn': {'image': {'name': 'burn-in-example', 'digest': DIGEST}, 'duration': '10m'}}
    plan = {'apiVersion': 'fleet.datumapis.com/v1alpha1', 'kind': 'DeviceCommissioningPlan',
            'metadata': {'name': 'rules-plan', 'namespace': namespace},
            'spec': {'creator': envelope['creator'], 'deviceSelector': {'matchLabels': {'example.com/model': 'example'}},
                     'content': content, 'evidenceLifetime': '24h'}}
    apply(plan)
    refused('the spec is immutable after create', '-n', namespace, 'patch', 'devicecommissioningplan', 'rules-plan',
            '--type=merge', '-p', json.dumps({'spec': {'evidenceLifetime': '48h'}}))
    refused('a plan must select Devices', 'create', '-f', '-', stdin=json.dumps(copy(plan, 'rules-plan-unselective', deviceSelector={})))
    refused('evidence stays current from 1 hour to 365 days', 'create', '-f', '-', stdin=json.dumps(copy(plan, 'rules-plan-lifetime', evidenceLifetime='30m')))
    refused('burn-in runs from 1 minute to 24 hours', 'create', '-f', '-',
            stdin=json.dumps(copy(plan, 'rules-plan-burn-in', content=dict(content, burnIn=dict(content['burnIn'], duration='25h')))))
    refused("a plan's name leaves room for its approval's suffix", 'create', '-f', '-', stdin=json.dumps(copy(plan, 'p' * 237)))
    approval = {'apiVersion': 'fleet.datumapis.com/v1alpha1', 'kind': 'DeviceCommissioningPlanApproval',
                'metadata': {'name': 'rules-plan-0123456789abcdef', 'namespace': namespace},
                'spec': {'creator': {'username': 'approver-example'}, 'planRef': {'name': 'rules-plan', 'generation': 1, 'digest': DIGEST}}}
    apply(approval)
    refused('the spec is immutable after create', '-n', namespace, 'patch', 'devicecommissioningplanapproval', 'rules-plan-0123456789abcdef',
            '--type=merge', '-p', json.dumps({'spec': {'planRef': {'generation': 2}}}))
    refused("an approval's name is its plan's name and a suffix", 'create', '-f', '-', stdin=json.dumps(copy(approval, 'rules-plan-short')))
    commissioning = {'apiVersion': 'fleet.datumapis.com/v1alpha1', 'kind': 'DeviceCommissioning',
                     'metadata': {'name': 'rules-commissioning', 'namespace': namespace},
                     'spec': dict(envelope, plan={'name': 'rules-plan', 'generation': 1, 'digest': DIGEST,
                                                  'approval': 'rules-plan-0123456789abcdef', 'content': content})}
    apply(commissioning)
    refused('an operation spec is immutable after create', '-n', namespace, 'patch', 'devicecommissioning', 'rules-commissioning',
            '--type=merge', '-p', json.dumps({'spec': {'authority': {'serviceRequest': {'reason': 'changed'}}}}))
    refused('burn-in runs from 1 minute to 24 hours', 'create', '-f', '-',
            stdin=json.dumps(copy(commissioning, 'rules-commissioning-short', plan=dict(commissioning['spec']['plan'], content=dict(content, burnIn=dict(content['burnIn'], duration='30s'))))))
    for phase in ('Pending', 'Admitted', 'Dispatched'):
        if status('devicecommissioning', 'rules-commissioning', phase).returncode != 0:
            raise SystemExit('custody-crd-rules: a forward commissioning phase change was refused')
    refused('operation phases move forward only', '-n', namespace, 'patch', 'devicecommissioning', 'rules-commissioning', '--subresource=status',
            '--type=merge', '-p', json.dumps({'status': {'phase': 'Pending'}}))
    if status('devicecommissioning', 'rules-commissioning', 'Failed').returncode != 0:
        raise SystemExit('custody-crd-rules: ending a commissioning Failed was refused')
    refused('an ended phase is final', '-n', namespace, 'patch', 'devicecommissioning', 'rules-commissioning', '--subresource=status',
            '--type=merge', '-p', json.dumps({'status': {'phase': 'Succeeded'}}))
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
