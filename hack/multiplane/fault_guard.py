#!/usr/bin/env python3
"""Record and verify that a Flux suspension survived a fault interval."""
import datetime
import json
import pathlib
import subprocess
import sys


def validate(obj, deployment, pods, baseline=None):
    metadata = obj['metadata']
    receipt = {'uid': metadata['uid'], 'generation': metadata['generation'],
               'suspended': obj.get('spec', {}).get('suspend') is True,
               'replicas': deployment.get('spec', {}).get('replicas'),
               'activePods': len(pods['items'])}
    if not receipt['suspended'] or receipt['replicas'] != 0 or receipt['activePods'] != 0:
        raise ValueError('fault isolation lost: suspension, zero replicas and no Pods required')
    if baseline is not None and receipt != baseline:
        raise ValueError('fault isolation changed during suspension')
    return receipt


def root_receipt(root, revision):
    """The cluster's root sync applied the given source revision while the fault held."""
    status = root.get('status', {})
    ready = any(c.get('type') == 'Ready' and c.get('status') == 'True' for c in status.get('conditions', []))
    if status.get('lastAppliedRevision') != revision or not ready or status.get('observedGeneration') != root['metadata']['generation']:
        raise ValueError('root sync did not apply %s during the fault' % revision)
    return {'revision': revision, 'lastHandledReconcileAt': status.get('lastHandledReconcileAt')}


def main():
    mode, state, config, kind, name, namespace, deployment, evidence = sys.argv[1:9]
    # Optional: the source revision the root sync must have applied during the fault.
    revision = sys.argv[9] if len(sys.argv) > 9 else None
    if mode not in ('record', 'check'):
        raise ValueError('expected record or check')
    state = pathlib.Path(state)

    def get(*args):
        return json.loads(subprocess.check_output(
            ['kubectl', '--kubeconfig', str(state / config), '--request-timeout=20s',
             *args, '-o', 'json'], text=True))

    obj = get('-n', 'flux-system', 'get', kind, name)
    dep = get('-n', namespace, 'get', 'deployment', deployment)
    labels = dep['spec']['selector']['matchLabels']
    pods = get('-n', namespace, 'get', 'pods', '-l', ','.join(k + '=' + v for k, v in sorted(labels.items())))
    path = state / 'evidence' / (evidence + '.json')
    previous = json.loads(path.read_text()) if mode == 'check' else None
    receipt = validate(obj, dep, pods, previous['before'] if previous else None)
    record = previous or {'kind': kind, 'name': name, 'fieldManager': 'fleet-fault-injection', 'before': receipt}
    record['checkedAt'] = datetime.datetime.now(datetime.timezone.utc).isoformat()
    if mode == 'check':
        record['after'] = receipt
        record['unchanged'] = True
        if revision:
            root = get('-n', 'flux-system', 'get', 'kustomizations.kustomize.toolkit.fluxcd.io', 'fleet-cluster')
            record['rootReconciledDuringFault'] = root_receipt(root, revision)
    path.write_text(json.dumps(record, indent=2) + '\n')


if __name__ == '__main__':
    main()
