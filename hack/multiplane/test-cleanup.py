#!/usr/bin/env python3
"""Cleanup removes only what old, finished runs left behind.

Drives the real `harness.py cleanup` against synthetic run state, a fake
docker that serves containers from a file, and a stand-in teardown.sh, and
checks it never touches an active, locked, queued or recent run.
"""
import json
import os
import pathlib
import subprocess
import sys
import tempfile
import time

root = pathlib.Path(sys.argv[1]).resolve()
harness = root / 'hack/multiplane/harness.py'
failures = []
DAY = 86400


def check(name, condition, detail=''):
    if not condition:
        failures.append(name + (': ' + detail if detail else ''))


FAKE_DOCKER = r'''#!/usr/bin/env python3
import json, os, sys
a = sys.argv[1:]
db = os.environ['FAKE_CONTAINERS']
containers = json.load(open(db))
with open(os.environ['FAKE_TRACE'], 'a') as t:
    t.write(json.dumps(a) + '\n')
if a[:1] == ['ps']:
    print('\n'.join(c['name'] for c in containers))
elif a[:1] == ['inspect']:
    for c in containers:
        if c['name'] in a:
            print('/%s\t%s\t%s' % (c['name'], c['state'], c['finishedAt']))
elif a[:2] == ['rm', '-f']:
    json.dump([c for c in containers if c['name'] not in a], open(db, 'w'))
'''


def stamp(seconds_ago):
    return time.strftime('%Y-%m-%dT%H:%M:%S.000000000Z', time.gmtime(time.time() - seconds_ago))


with tempfile.TemporaryDirectory() as t:
    tmp = pathlib.Path(t)
    runs, bin_dir, repo = tmp / 'runs', tmp / 'bin', tmp / 'repo'
    for d in (runs, bin_dir, repo / 'hack/multiplane'):
        d.mkdir(parents=True)
    trace = tmp / 'trace'
    trace.write_text('')
    (bin_dir / 'docker').write_text(FAKE_DOCKER.replace('#!/usr/bin/env python3', '#!' + sys.executable))
    (bin_dir / 'docker').chmod(0o755)
    (repo / 'hack/multiplane/teardown.sh').write_text('#!/bin/sh\necho "teardown $1 $2" >> %s\n' % trace)
    containers = []

    def run_state(name, prefix, age, result=True, status='failed', running=False, stopped=None):
        s = runs / ('fleet-multiplane.' + name)
        (s / 'evidence').mkdir(parents=True)
        (s / 'prefix').write_text(prefix + '\n')
        if result:
            r = s / 'evidence/result.json'
            r.write_text(json.dumps({'status': status}))
            os.utime(r, (time.time() - age, time.time() - age))
        if stopped is not None or running:
            for suffix in ('cell-control-plane', 'registry'):
                containers.append({'name': prefix + '-' + suffix, 'state': 'running' if running else 'exited',
                                   'finishedAt': '0001-01-01T00:00:00Z' if running else stamp(stopped)})
        return s

    passed = run_state('Passd1', 'fleet-mp-00000001', 2 * DAY, status='passed')
    failed = run_state('Faild1', 'fleet-mp-00000002', 2 * DAY, stopped=2 * DAY)
    recent = run_state('Recnt1', 'fleet-mp-00000003', 3600, stopped=3600)
    noresult = run_state('NoRes1', 'fleet-mp-00000004', 2 * DAY, result=False, stopped=2 * DAY)
    running = run_state('Runng1', 'fleet-mp-00000005', 2 * DAY, running=True)
    holder = run_state('Holdr1', 'fleet-mp-00000006', 2 * DAY, stopped=2 * DAY)
    queued = run_state('Queue1', 'fleet-mp-00000007', 2 * DAY, stopped=2 * DAY)
    live = run_state('LiveR1', 'fleet-mp-00000008', 2 * DAY, stopped=2 * DAY)
    for prefix, state, since in (('fleet-mp-0000000a', 'exited', 2 * DAY), ('fleet-mp-0000000b', 'exited', 3600), ('fleet-mp-0000000c', 'running', None)):
        containers.append({'name': prefix + '-cell-control-plane', 'state': state, 'finishedAt': stamp(since) if since else '0001-01-01T00:00:00Z'})
    # Containers that are not this harness's are never listed or touched.
    containers.append({'name': 'fleet-local-0123456789ab', 'state': 'exited', 'finishedAt': stamp(30 * DAY)})
    db = tmp / 'containers.json'
    db.write_text(json.dumps(containers))
    lock = tmp / 'lock'
    (lock / 'queue').mkdir(parents=True)
    (lock / 'holder.json').write_text(json.dumps({'state': str(holder)}))
    (lock / 'queue' / '00000000000000000001-1.ticket').write_text(json.dumps({'state': str(queued)}))
    keeper = subprocess.Popen([sys.executable, '-c', 'import time; time.sleep(600)', str(live)])
    env = dict(os.environ, PATH=str(bin_dir) + os.pathsep + os.environ['PATH'], FLEET_MULTIPLANE_STATE_ROOT=str(runs), FLEET_MULTIPLANE_STATE_ROOTS=str(runs),
               FLEET_MULTIPLANE_LOCK_DIR=str(lock), FAKE_CONTAINERS=str(db), FAKE_TRACE=str(trace))
    try:
        mine = runs / 'fleet-multiplane.Mine01'
        (mine / 'evidence').mkdir(parents=True)

        def cleanup(*extra):
            return subprocess.run([sys.executable, str(harness), 'cleanup', '--state', str(mine), '--root', str(repo), '--max-age-hours', '24', *extra],
                                  env=env, text=True, capture_output=True, timeout=120)
        r = cleanup('--dry-run')
        check('dry run succeeds', r.returncode == 0, r.stderr)
        check('dry run removes nothing', all(p.exists() for p in (passed, failed)) and 'teardown' not in trace.read_text()
              and not any('rm' in json.loads(l)[:1] for l in trace.read_text().splitlines()))
        dry = json.loads((mine / 'evidence/cleanup.json').read_text())
        check('dry run reports what it would remove', sorted(x['prefix'] for x in dry['removed']) == ['fleet-mp-00000001', 'fleet-mp-00000002', 'fleet-mp-0000000a'],
              str(dry['removed']))

        r = cleanup()
        check('cleanup succeeds', r.returncode == 0, r.stderr)
        check('an old passed run is removed', not passed.exists())
        check('an old failed run is torn down then removed', not failed.exists() and ('teardown %s --discard-unknown' % failed) in trace.read_text(), trace.read_text())
        for name, s in (('recent', recent), ('without a result', noresult), ('with a running container', running), ('holding the lock', holder),
                        ('queued', queued), ('named by a live process', live), ('doing the cleanup', mine)):
            check('a run %s is kept' % name, s.exists() and str(s) not in trace.read_text().replace('fleet-multiplane.Faild1', ''))
        left = {c['name'] for c in json.loads(db.read_text())}
        check('old orphaned stopped containers are removed', 'fleet-mp-0000000a-cell-control-plane' not in left)
        check('recent and running orphans are kept', {'fleet-mp-0000000b-cell-control-plane', 'fleet-mp-0000000c-cell-control-plane'} <= left)
        check('other containers are never touched', 'fleet-local-0123456789ab' in left and 'fleet-local' not in trace.read_text())
        touched = [json.loads(l) for l in trace.read_text().splitlines() if l.startswith('[')]
        check('only exact owned prefixes are removed', all(n.startswith('fleet-mp-0000000a') for a in touched if a[:2] == ['rm', '-f'] for n in a[3:]), str(touched))
    finally:
        keeper.kill()
        keeper.wait()

if failures:
    print('FAIL cleanup tests:\n  ' + '\n  '.join(failures))
    sys.exit(1)
print('PASS cleanup tests')
