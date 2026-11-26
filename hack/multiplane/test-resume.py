#!/usr/bin/env python3
"""A resumed run reruns a failed run's suite from one phase, and is never a pass.

Drives the real `harness.py resume-plan`, phase skipping and result mapping
against synthetic retained state, then runs the real run.sh with --resume in
a scratch repository whose suite and runtime scripts are stand-ins.
"""
import json
import os
import pathlib
import shutil
import subprocess
import sys
import tempfile

root = pathlib.Path(sys.argv[1]).resolve()
harness = root / 'hack/multiplane/harness.py'
failures = []


def check(name, condition, detail=''):
    if not condition:
        failures.append(name + (': ' + detail if detail else ''))


def run(args, env=None, cwd=None):
    return subprocess.run([sys.executable, str(harness), *args], text=True, capture_output=True, env=env or dict(os.environ), cwd=cwd or root)


def retained(tmp, status='failed', failed='release', name='fleet-multiplane.Retain'):
    """A failed run's retained state, as run.sh leaves it."""
    s = tmp / name
    (s / 'evidence').mkdir(parents=True)
    (s / 'prefix').write_text('fleet-mp-0000beef\n')
    (s / 'site-provider').write_text('libvirt\n')
    (s / 'suite').write_text('acceptance\n')
    (s / 'fleet-multiplane-host').write_text('')
    (s / 'acceptance.json').write_text('{}')
    (s / 'evidence/phases.jsonl').write_text(json.dumps({'name': 'rbac', 'status': 'passed'}) + '\n' + json.dumps({'name': failed, 'status': 'failed'}) + '\n')
    (s / 'evidence/result.json').write_text(json.dumps({'status': status, 'failedPhase': failed}))
    return s


with tempfile.TemporaryDirectory() as t:
    tmp = pathlib.Path(t)
    env = dict(os.environ, FLEET_MULTIPLANE_STATE_ROOT=str(tmp))

    # Refusals: nothing retained, a passed run, a run without a result, a
    # failure before the suite started, and a phase the suite does not have.
    check('refuses an unknown target', run(['resume-plan', '--target', 'fleet-mp-ffffffff'], env).returncode == 64)
    for name, status, failed, phase in (('passed', 'passed', None, 'release'), ('resumed', 'resumed', None, 'release'),
                                        ('bootstrap', 'failed', 'topology', None), ('bogus', 'failed', 'release', 'no-such-phase')):
        s = retained(tmp, status, failed, 'fleet-multiplane.' + name)
        r = run(['resume-plan', '--target', str(s)] + (['--from', phase] if phase else []), env)
        check('refuses ' + name, r.returncode == 64 and not (s / 'resume.json').exists(), r.stderr)
        check('refusal leaves the record ' + name, (s / 'evidence/result.json').exists())
        shutil.rmtree(s)
    s = retained(tmp, name='fleet-multiplane.Live')
    (s / 'evidence/result.json').unlink()
    check('refuses a run that has not ended', run(['resume-plan', '--target', str(s)], env).returncode == 64)
    shutil.rmtree(s)

    # A failed run resumes at its failed phase by default, found by prefix.
    s = retained(tmp)
    r = run(['resume-plan', '--target', 'fleet-mp-0000beef'], env)
    check('plans a failed run', r.returncode == 0 and r.stdout.split() == [str(s), 'release'], r.stdout + r.stderr)
    record = json.loads((s / 'resume.json').read_text())
    check('records the attempt', record['attempt'] == 1 and record['from'] == 'release' and record['previousStatus'] == 'failed', str(record))
    check('keeps the earlier records', (s / 'evidence/result-attempt0.json').exists() and (s / 'evidence/phases-attempt0.jsonl').exists()
          and not (s / 'evidence/result.json').exists() and not (s / 'evidence/phases.jsonl').exists())
    check('records whether the binaries still match the source', 'binarySourceChanged' in record)

    # Phases before the resume point are recorded skipped and not run; the
    # resume point and those after it run.
    (s / 'skip-until').write_text('release\n')
    marker = tmp / 'ran'
    r = run(['phase', '--state', str(s), '--name', 'rbac', '--timeout', '30', '--', 'touch', str(marker)], env)
    check('skips an earlier phase', r.returncode == 0 and not marker.exists() and 'skipped' in r.stdout, r.stdout)
    r = run(['phase', '--state', str(s), '--name', 'release', '--timeout', '30', '--', 'touch', str(marker)], env)
    check('runs the resume point', r.returncode == 0 and marker.exists() and not (s / 'skip-until').exists())
    phases = [json.loads(l) for l in (s / 'evidence/phases.jsonl').read_text().splitlines()]
    check('records skipped and run phases', [(p['name'], p['status']) for p in phases] == [('rbac', 'skipped'), ('release', 'passed')], str(phases))

    # A completed resumed run is recorded resumed, exits 5, and is never passed.
    (s / 'completed').write_text('')
    r = run(['result', '--state', str(s), '--exit-code', '0'], env)
    result = json.loads((s / 'evidence/result.json').read_text())
    check('a completed resumed run is not passed', result['status'] == 'resumed' and result['exitCode'] == 5 and r.returncode == 5, str(result))
    check('RESULT line says resumed', r.stdout.startswith('RESULT resumed'), r.stdout)
    check('result carries the resume record', (result.get('resumed') or {}).get('from') == 'release')
    check('no summary pointer for a resumed run', result['summary'] is None)
    check('a resumed run does not claim custody was released', result['custody'].startswith('unverified'), result['custody'])
    r = run(['result', '--state', str(s), '--exit-code', '1'], env)
    result = json.loads((s / 'evidence/result.json').read_text())
    check('a failed resumed run is failed', result['status'] == 'failed' and result['resumed'] and result['failedPhase'] is None, str(result))

with tempfile.TemporaryDirectory() as t:
    tmp = pathlib.Path(t)
    env = dict(os.environ, FLEET_MULTIPLANE_STATE_ROOT=str(tmp))
    # A resume that stopped before its suite (preflight, the lock, or the
    # topology restart) leaves the run resumable at the same phase, as a
    # second attempt whose records sit beside the first's.
    for status, failed in (('preflight-failed', None), ('timeout', 'runtime-lock'), ('failed', 'resume-topology')):
        s = retained(tmp, name='fleet-multiplane.Again' + str(len(status)))
        (s / 'readiness-SHA256').write_text('original\n')
        check('first attempt planned', run(['resume-plan', '--target', str(s)], env).returncode == 0)
        (s / 'readiness-SHA256').write_text('rewritten by the attempt\n')
        (s / 'evidence/result.json').write_text(json.dumps({'status': status, 'failedPhase': failed, 'resumed': json.loads((s / 'resume.json').read_text())}))
        r = run(['resume-plan', '--target', str(s)], env)
        check('a resume stopped by %s stays resumable' % status, r.returncode == 0 and r.stdout.split()[1:] == ['release'], r.stdout + r.stderr)
        record = json.loads((s / 'resume.json').read_text())
        check('second attempt recorded: ' + status, record['attempt'] == 2 and (s / 'evidence/result-attempt1.json').exists() and (s / 'evidence/result-attempt0.json').exists())
        check('the original source digest is kept: ' + status, (s / 'readiness-original-SHA256').read_text().strip() == 'original')

    # A resumed run's own retained containers do not block its contention check.
    bin_dir = tmp / 'bin'
    bin_dir.mkdir()
    (bin_dir / 'docker').write_text('#!/bin/sh\n[ "$1" = ps ] && echo fleet-mp-0000beef-cell-control-plane\nexit 0\n')
    (bin_dir / 'colima').write_text('#!/bin/sh\necho "/dev/vda1 100 1 99999999 1%% /var/lib/docker"\n')
    for tool in ('docker', 'colima'):
        (bin_dir / tool).chmod(0o755)
    s = tmp / 'contention'
    s.mkdir()
    cenv = dict(env, PATH=str(bin_dir) + os.pathsep + os.environ['PATH'], FLEET_MULTIPLANE_STATE_ROOTS=str(tmp))
    r = run(['preflight', '--checks', 'contention', '--state', str(s), '--allow-prefix', 'fleet-mp-0000beef'], cenv)
    check('own retained containers are allowed', r.returncode == 0, r.stdout)
    r = run(['preflight', '--checks', 'contention', '--state', str(s), '--allow-prefix', 'fleet-mp-0000aaaa'], cenv)
    check('only its own', r.returncode == 2, r.stdout)

# The inotify record: a resumed run sets the limit again only after stop.sh
# restored it, and an unrestored record keeps the original value.
FAKE_DOCKER = '''#!/bin/sh
case "$1" in
 context) echo unix:///Users/x/.colima/fleet-acceptance-tcg/docker.sock ;;
 exec) case "$3" in cat) cat "$FAKE_VALUE" ;; sysctl) echo "${5#*=}" > "$FAKE_VALUE" ;; esac ;;
esac
'''
with tempfile.TemporaryDirectory() as t:
    tmp = pathlib.Path(t)
    (tmp / 'bin').mkdir()
    (tmp / 'bin/docker').write_text(FAKE_DOCKER)
    (tmp / 'bin/docker').chmod(0o755)
    value = tmp / 'value'
    env = dict(os.environ, PATH=str(tmp / 'bin') + os.pathsep + os.environ['PATH'], FAKE_VALUE=str(value), FLEET_MULTIPLANE_INOTIFY='1024')
    sysctl = [sys.executable, str(root / 'hack/multiplane/sysctl.py')]
    for restored in (True, False):
        s = tmp / ('state-%s' % restored)
        s.mkdir()
        (s / 'prefix').write_text('fleet-mp-0000beef\n')
        (s / 'sysctl.json').write_text(json.dumps({'original': 128, 'effective': 1024, 'restored': True} if restored else {'original': 128, 'effective': 1024}, indent=2))
        value.write_text('128\n' if restored else '1024\n')
        r = subprocess.run(sysctl + [str(s), 'resume'], env=env, capture_output=True, text=True)
        record = json.loads((s / 'sysctl.json').read_text())
        if restored:
            check('restored: set again with a fresh record', r.returncode == 0 and record['original'] == 128 and value.read_text().strip() == '1024'
                  and (s / 'sysctl-before-resume-0.json').exists(), r.stdout + r.stderr)
        else:
            check('unrestored: the earlier record and its original value stay', r.returncode == 0 and record == {'original': 128, 'effective': 1024}
                  and not list(s.glob('sysctl-before-resume-*')), r.stdout + r.stderr)

# Teardown after a resumed run is labeled as no acceptance or release evidence.
with tempfile.TemporaryDirectory() as t:
    tmp = pathlib.Path(t)
    (tmp / 'bin').mkdir()
    for tool in ('docker', 'kind'):
        (tmp / 'bin' / tool).write_text('#!/bin/sh\n[ "$1" = inspect ] && exit 1\nexit 0\n')
        (tmp / 'bin' / tool).chmod(0o755)
    env = dict(os.environ, PATH=str(tmp / 'bin') + os.pathsep + os.environ['PATH'])
    for resumed in (True, False):
        s = tmp / ('state-%s' % resumed)
        (s / 'evidence').mkdir(parents=True)
        (s / 'prefix').write_text('fleet-mp-0000beef\n')
        (s / 'evidence/summary.json').write_text(json.dumps({'acceptance': 'evidence-verified', 'resumed': resumed}))
        if resumed:
            (s / 'resume.json').write_text('{}')
        r = subprocess.run(['bash', str(root / 'hack/multiplane/teardown.sh'), str(s)], env=env, capture_output=True, text=True)
        mode = json.loads((s / 'evidence/teardown.json').read_text())['mode'] if (s / 'evidence/teardown.json').exists() else None
        check('teardown label, resumed=%s' % resumed, r.returncode == 0 and (('not acceptance' in mode) if resumed else mode == 'after passed acceptance'),
              '%s %s %s' % (r.returncode, mode, r.stderr[-300:]))

# The real run.sh: --resume reuses the retained state, skips the topology
# build, restarts the retained topology, and runs the suite from the phase.
SHIM = '''import importlib.util, sys
spec = importlib.util.spec_from_file_location('harness', %r)
harness = importlib.util.module_from_spec(spec)
sys.modules['harness'] = harness
spec.loader.exec_module(harness)
harness.preflight = lambda args: 0
sys.exit(harness.main())
'''
with tempfile.TemporaryDirectory() as t:
    tmp = pathlib.Path(t)
    repo = tmp / 'repo'
    (repo / 'hack/multiplane').mkdir(parents=True)
    shutil.copy(root / 'hack/multiplane/run.sh', repo / 'hack/multiplane/run.sh')
    (repo / 'hack/multiplane/harness.py').write_text(SHIM % str(harness))
    calls = tmp / 'calls'
    for name, body in (('stop.sh', 'exit 0'), ('teardown.sh', 'echo teardown >> %s' % calls),
                       ('steps.sh', 'echo "steps $1" >> %s' % calls),
                       # A stand-in suite: the real phase helper, with the real acceptance phase names.
                       ('acceptance.sh', 'state=$1\nph() { python3 hack/multiplane/harness.py phase --state "$state" --name "$1" --timeout 30 -- sh -c "echo $1 >> %s"; }\n'
                                         'ph rbac\nph allocate\nph release\nph evidence' % calls)):
        (repo / 'hack/multiplane' / name).write_text('#!/bin/sh\nset -e\n' + body + '\n')
    bin_dir = tmp / 'bin'
    bin_dir.mkdir()
    (bin_dir / 'docker').write_text('#!/bin/sh\nexit 0\n')
    (bin_dir / 'docker').chmod(0o755)
    runs = tmp / 'runs'
    runs.mkdir()
    s = retained(runs)
    env = dict(os.environ, PATH=str(bin_dir) + os.pathsep + os.environ['PATH'], FLEET_MULTIPLANE_STATE_ROOT=str(runs), FLEET_MULTIPLANE_LOCK_DIR=str(tmp / 'lock'))
    r = subprocess.run(['bash', str(repo / 'hack/multiplane/run.sh'), '--resume', str(s), '--from', 'allocate'], cwd=repo, env=env, text=True, capture_output=True, timeout=120)
    result = json.loads((s / 'evidence/result.json').read_text())
    check('run.sh --resume exits 5', r.returncode == 5, str(r.returncode) + r.stdout[-600:] + r.stderr[-600:])
    check('run.sh --resume records resumed', result['status'] == 'resumed', result['status'])
    check('run.sh --resume restarted the topology and ran from the phase',
          calls.read_text().split('\n')[:4] == ['steps resume-topology', 'allocate', 'release', 'evidence'], calls.read_text())
    check('run.sh --resume used no new state', [p.name for p in runs.iterdir()] == [s.name])
    check('run.sh --resume never says passed', 'NOT an acceptance pass' in r.stdout and ' passed.' not in r.stdout, r.stdout[-400:])
    names = [(p['name'], p['status']) for p in map(json.loads, (s / 'evidence/phases.jsonl').read_text().splitlines())]
    check('run.sh --resume records the skipped phase', ('rbac', 'skipped') in names and ('build', 'passed') not in names and ('topology', 'passed') not in names, str(names))
    for args in (['--resume', str(s), '--redfish'], ['--resume', str(s), '--suite', 'acceptance'], ['--from', 'release'], ['--resume', str(s), '--preflight-only']):
        r = subprocess.run(['bash', str(repo / 'hack/multiplane/run.sh'), *args], cwd=repo, env=env, text=True, capture_output=True, timeout=60)
        check('run.sh usage: ' + ' '.join(a for a in args if a != str(s)), r.returncode == 64, str(r.returncode))
    # With --keep, a completed resumed run still exits 5.
    k = retained(runs, name='fleet-multiplane.Keep01')
    r = subprocess.run(['bash', str(repo / 'hack/multiplane/run.sh'), '--resume', str(k), '--from', 'release', '--keep'], cwd=repo, env=env, text=True, capture_output=True, timeout=120)
    check('run.sh --resume --keep exits 5', r.returncode == 5 and json.loads((k / 'evidence/result.json').read_text())['status'] == 'resumed', str(r.returncode))
    # A phase the suite script never reaches fails the run instead of skipping everything.
    u = retained(runs, name='fleet-multiplane.Unrch1')
    r = subprocess.run(['bash', str(repo / 'hack/multiplane/run.sh'), '--resume', str(u), '--from', 'heal'], cwd=repo, env=env, text=True, capture_output=True, timeout=120)
    check('run.sh --resume to an unreached phase exits 64', r.returncode == 64 and json.loads((u / 'evidence/result.json').read_text())['status'] != 'resumed', str(r.returncode))
    r = subprocess.run(['bash', str(repo / 'hack/multiplane/run.sh'), '--resume', ''], cwd=repo, env=env, text=True, capture_output=True, timeout=60)
    check('run.sh --resume with no target is usage', r.returncode == 64 and [p.name for p in runs.iterdir() if p.name.startswith('fleet-multiplane.')] == sorted(
        [s.name, k.name, u.name], key=lambda n: [p.name for p in runs.iterdir()].index(n)), str(r.returncode))
    # A resumed run cannot be resumed into a pass.
    r = subprocess.run(['bash', str(repo / 'hack/multiplane/run.sh'), '--resume', str(s)], cwd=repo, env=env, text=True, capture_output=True, timeout=60)
    check('a resumed run that completed cannot be resumed again', r.returncode == 64, str(r.returncode))

if failures:
    print('FAIL resume tests:\n  ' + '\n  '.join(failures))
    sys.exit(1)
print('PASS resume tests')
