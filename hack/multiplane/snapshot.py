#!/usr/bin/env python3
"""Record why a failed run stopped, before its containers are stopped.

On a failed or timed-out run, run.sh calls this first and then the secret
scan, so the snapshot ships in evidence/ only if it holds no retained secret.
It copies an allow-list of fields, never a Secret, a spec or a whole status:

  * provider custody and every Device operation: phase, reason, fence;
  * each handoff on the cell and on the hub: its reported result;
  * Karmada propagation: ResourceBinding and Work conditions;
  * the recent error lines of the coordinator, the site workers and the
    observer.

Every copied string is masked and cut to a bounded length, and every number
is checked to be one; the secret scan only knows values the run retained, so
the masks must catch a credential the run never stored. Each section is
bounded and best effort: one that cannot be read records why, the whole
snapshot stops at its deadline, and it never fails the run's teardown.
"""
import datetime
import json
import os
import pathlib
import re
import subprocess
import sys
import time

DEADLINE = time.monotonic() + float(os.environ.get('FLEET_MULTIPLANE_SNAPSHOT_SECONDS', '60'))
REQUEST = 8
ERROR = re.compile(r'error|fail|incomplete|refus|denied|timeout|timed out|unknown|panic', re.I)
SECRET_KEY = r'[\w.-]*(?:password|passwd|pwd|passphrase|token|secret|api[_-]?key|credential|private[_-]?key|auth)[\w.-]*'
# Values shaped like credentials, masked in every copied string. A digest is
# masked too; its length is kept so two can still be told apart by kind.
MASKS = (
    (re.compile(r'(?i)(\b[a-z][a-z0-9+.-]*://)[^/\s:@]+:[^/\s@]+@'), r'\1<masked>@'),
    (re.compile(r'eyJ[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]*'), '<masked>'),
    (re.compile(r'(?i)\b(authorization\s*["\']?\s*[:=]\s*["\']?)(?:[A-Za-z]+\s+)?[^\s"\',}]+'), r'\1<masked>'),
    (re.compile(r'(?i)\b(bearer|basic|token)\s+[A-Za-z0-9+/=._~-]{6,}'), r'\1 <masked>'),
    (re.compile(r'(?i)(' + SECRET_KEY + r')(["\']?\s*[:=]\s*)(?:"[^"]*"|\'[^\']*\'|[^\s,}\]]+)'), r'\1\2<masked>'),
    (re.compile(r'\b(?:ghp|gho|ghs|ghu|ghr|github_pat|glpat|xox[abprs])[_-][A-Za-z0-9_-]{10,}'), '<masked>'),
    (re.compile(r'-----BEGIN [A-Z ]*PRIVATE KEY-----.*?(?:-----END [A-Z ]*PRIVATE KEY-----|$)', re.S), '<masked private key>'),
)
LONG = (re.compile(r'\b[A-Fa-f0-9]{32,}\b'), re.compile(r'[A-Za-z0-9+/_-]{40,}={0,2}'))
CONDITION = ('type', 'status', 'reason', 'message', 'lastTransitionTime')


def mask(text):
    for pattern, replacement in MASKS:
        text = pattern.sub(replacement, text)
    for pattern in LONG:
        text = pattern.sub(lambda m: '<masked len=%d>' % len(m.group(0)), text)
    return text


def text(value, limit=300):
    """A copied string: masked, then cut. None stays None."""
    return None if value is None else mask(str(value))[:limit]


def number(value):
    return value if isinstance(value, int) and not isinstance(value, bool) else None


def conditions(obj):
    return [{k: text(c[k]) for k in CONDITION if k in c} for c in ((obj.get('status') or {}).get('conditions') or [])[:20]]


def kubectl(state, config, *args, as_json=True):
    left = DEADLINE - time.monotonic()
    if left < 2:
        raise RuntimeError('snapshot deadline reached')
    path = state / config
    if not path.exists():
        raise RuntimeError('no kubeconfig ' + config)
    request = max(1, min(REQUEST, int(left) - 1))
    r = subprocess.run(['kubectl', '--kubeconfig', str(path), '--request-timeout=%ds' % request, *args] + (['-o', 'json'] if as_json else []),
                       text=True, capture_output=True, timeout=request + 2)
    if r.returncode:
        raise RuntimeError(text(r.stderr.strip()[-300:]) or 'kubectl exited %d' % r.returncode)
    return json.loads(r.stdout) if as_json else r.stdout


def section(fn):
    try:
        return fn()
    except Exception as e:  # noqa: BLE001  a snapshot records what it could not read
        return {'unavailable': text(e)}


def operations(state):
    out = {}
    for kind in ('devicecustody', 'deviceinstallation', 'devicewipe', 'devicerecovery'):
        items = kubectl(state, 'provider/bootstrap-project.json', '-n', 'fleet-provider', 'get', kind)['items']
        out[kind] = []
        for o in items[:50]:
            st = o.get('status') or {}
            rec = {'name': text(o['metadata']['name']), 'generation': number(o['metadata'].get('generation')),
                   'phase': text(st.get('phase'), 40), 'reason': text(st.get('reason'), 80), 'fence': number(st.get('fence')),
                   'deadline': text(st.get('deadline'), 40), 'conditions': conditions(o)}
            if kind == 'devicecustody':
                spec = o.get('spec') or {}
                active = spec.get('activeOperation') or {}
                hold = spec.get('recoveryHold') or {}
                rec.update(state=text(spec.get('state'), 40), holder=text((spec.get('holder') or {}).get('type'), 40), fence=number(spec.get('fence')),
                           activeOperation={'kind': text(active.get('kind'), 60), 'name': text(active.get('name')), 'fence': number(active.get('fence')),
                                            'deadline': text(active.get('deadline'), 40)} if active else None,
                           recoveryHold={'kind': text(hold.get('kind'), 60), 'name': text(hold.get('name')), 'fence': number(hold.get('fence'))} if hold else None,
                           conflicts=[text(c, 80) for c in (spec.get('conflicts') or [])[:20] if isinstance(c, str)])
            out[kind].append(rec)
    return out


def handoffs(state, config):
    items = kubectl(state, config, '-n', 'fleet-coordination', 'get', 'machineallocationhandoffs')['items']
    out = []
    for h in items[:50]:
        obs = (h.get('status') or {}).get('observation') or {}
        result = obs.get('result') or {}
        out.append({'name': text(h['metadata']['name']), 'role': text((h['metadata'].get('labels') or {}).get('coordination.fleet.datumapis.com/role'), 40),
                    'generation': number(h['metadata'].get('generation')), 'resultPhase': text(result.get('phase'), 40), 'resultFence': number(result.get('fence')),
                    'observedAt': text(obs.get('observedAt'), 40), 'conditions': conditions(h)})
    return out


def propagation(state):
    out = {}
    for kind, args in (('resourcebindings', ('-n', 'fleet-coordination')), ('works', ('-A',))):
        items = kubectl(state, 'management-hub.json', 'get', kind, *args)['items']
        out[kind] = [{'namespace': text(o['metadata'].get('namespace')), 'name': text(o['metadata']['name']), 'generation': number(o['metadata'].get('generation')),
                      'conditions': conditions(o)} for o in items[:100]]
    return out


def error_lines(log, limit=40):
    return [text(l.rstrip(), 400) for l in log.splitlines() if ERROR.search(l)][-limit:]


def coordinator(state):
    log = state / 'coordinator.log'
    if not log.exists():
        raise RuntimeError('no coordinator log')
    return error_lines(log.read_text(errors='replace'))


def site_logs(state, selector):
    return error_lines(kubectl(state, 'cell-admin.yaml', '-n', 'fleet-site', 'logs', '-l', selector, '--all-containers', '--prefix', '--tail=400',
                               '--max-log-requests=20', as_json=False))


def main():
    state = pathlib.Path(sys.argv[1])
    failed = sys.argv[2] if len(sys.argv) > 2 else None
    record = {'observedAt': datetime.datetime.now(datetime.timezone.utc).isoformat(), 'failedPhase': text(failed, 80)}
    # The coordinator's errors come first: they are local and cannot hang.
    for key, fn in (('coordinatorErrors', lambda: coordinator(state)),
                    ('provider', lambda: operations(state)),
                    ('cellHandoffs', lambda: handoffs(state, 'cell-admin.yaml')),
                    ('hubHandoffs', lambda: handoffs(state, 'management-hub.json')),
                    ('karmada', lambda: propagation(state)),
                    ('workerErrors', lambda: site_logs(state, 'app.kubernetes.io/name=fleet-site-worker')),
                    ('observerErrors', lambda: site_logs(state, 'app.kubernetes.io/name=fleet-site-observer'))):
        record[key] = section(fn)
    ev = state / 'evidence'
    ev.mkdir(mode=0o700, exist_ok=True)
    (ev / 'failure-snapshot.json').write_text(json.dumps(record, indent=2, sort_keys=True) + '\n')
    print('snapshot: failure state recorded in evidence/failure-snapshot.json')
    return 0


if __name__ == '__main__':
    sys.exit(main())
