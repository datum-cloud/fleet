#!/usr/bin/env python3
"""Offline tests for kind_create.py with a fake kind; no container runtime."""
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


tmp = pathlib.Path(tempfile.mkdtemp())
bin_dir = tmp / 'bin'
bin_dir.mkdir()
# The fake kind records each call and the config it was given, then fails
# create the way FAKE_KIND lists: taken (a bound port), other, or ok.
fake = bin_dir / 'kind'
fake.write_text('''#!/usr/bin/env python3
import json, os, pathlib, sys
log = pathlib.Path(os.environ['FAKE_KIND_LOG'])
calls = json.loads(log.read_text()) if log.exists() else []
args = sys.argv[1:]
entry = {'args': args}
if args[:2] == ['create', 'cluster']:
    entry['config'] = pathlib.Path(args[args.index('--config') + 1]).read_text()
    outcome = os.environ['FAKE_KIND'].split(',')[sum(1 for c in calls if c['args'][:2] == ['create', 'cluster'])]
calls.append(entry)
log.write_text(json.dumps(calls))
if args[:2] == ['create', 'cluster']:
    if outcome == 'taken':
        print('ERROR: failed to create cluster: docker run error: Bind for 127.0.0.1:37783 failed: port is already allocated')
        sys.exit(1)
    if outcome == 'other':
        print('ERROR: failed to create cluster: node image pull failed')
        sys.exit(1)
''')
fake.chmod(fake.stat().st_mode | stat.S_IEXEC)
template = tmp / 'kind.yaml.template'
template.write_text('ports:\n  - {hostPort: @port:a@}\n  - {hostPort: @port:b@}\n  - {again: @port:a@}\n')


def run(outcomes):
    log = tmp / ('log-%s.json' % outcomes.replace(',', '-'))
    ports = tmp / ('ports-%s.json' % outcomes.replace(',', '-'))
    args = [sys.executable, str(root / 'hack/multiplane/kind_create.py'), '--name', 'fleet-mp-0000abcd-hub', '--template', str(template),
            '--config', str(tmp / 'kind.yaml'), '--ports', str(ports), '--kubeconfig', str(tmp / 'kubeconfig'), '--image', 'kindest/node:test']
    r = subprocess.run(args, text=True, capture_output=True,
                       env=dict(os.environ, PATH=str(bin_dir) + os.pathsep + os.environ['PATH'], FAKE_KIND=outcomes, FAKE_KIND_LOG=str(log)))
    calls = json.loads(log.read_text())
    return r, calls, (json.loads(ports.read_text()) if ports.exists() else None)


r, calls, ports = run('ok')
check('creates on the first attempt', r.returncode == 0 and [c['args'][:2] for c in calls] == [['create', 'cluster']], r.stdout + r.stderr)
check('records the ports the config bound', ports and set(ports) == {'a', 'b'} and ports['a'] != ports['b']
      and calls[0]['config'] == 'ports:\n  - {hostPort: %d}\n  - {hostPort: %d}\n  - {again: %d}\n' % (ports['a'], ports['b'], ports['a']), str(ports))

r, calls, ports = run('taken,ok')
creates = [c for c in calls if c['args'][:2] == ['create', 'cluster']]
check('retries a taken port with fresh ports', r.returncode == 0 and len(creates) == 2 and creates[0]['config'] != creates[1]['config'], r.stdout + r.stderr)
check('deletes only its own failed cluster before retrying',
      [c['args'][:4] for c in calls] == [['create', 'cluster', '--name', 'fleet-mp-0000abcd-hub'], ['delete', 'cluster', '--name', 'fleet-mp-0000abcd-hub'],
                                         ['create', 'cluster', '--name', 'fleet-mp-0000abcd-hub']], str([c['args'] for c in calls]))
check('records the ports of the attempt that bound', ports and creates[1]['config'].startswith('ports:\n  - {hostPort: %d}\n  - {hostPort: %d}' % (ports['a'], ports['b'])))

r, calls, ports = run('other,ok')
check('fails at once on any other error', r.returncode != 0 and len(calls) == 1 and ports is None, r.stdout + r.stderr)

r, calls, ports = run('taken,taken,taken,ok')
check('gives up after a bounded number of attempts', r.returncode != 0 and sum(1 for c in calls if c['args'][:2] == ['create', 'cluster']) == 3 and ports is None,
      r.stdout + r.stderr)

topology = (root / 'hack/multiplane/topology.sh').read_text()
check('every kind cluster is created through the port retry', 'kind create cluster' not in topology and topology.count('kind_create "$prefix-') == 3)
check('Docker assigns the registry host port', '-p "127.0.0.1::5000"' in topology and 'docker port "$prefix-registry" 5000/tcp' in topology
      and 'free_port' not in topology)

if failures:
    print('FAIL kind create tests:\n  ' + '\n  '.join(failures))
    sys.exit(1)
print('PASS kind create tests')
