#!/usr/bin/env python3
"""An interrupted run never reports or records passed.

Runs the real run.sh in a scratch copy of the repository, with the real
harness except for its preflight, which needs a container runtime, and a fake
`go` that keeps the first phase running. Then it interrupts the run mid-phase
and checks the RESULT line, evidence/result.json and the exit code. It also
checks the other exits that end with code 0: a preflight-only run, and a run
that exits 0 before it completed.
"""
import json
import os
import pathlib
import shutil
import signal
import subprocess
import sys
import tempfile
import time

root = pathlib.Path(sys.argv[1]).resolve()
failures = []


def check(name, condition, detail=''):
    if not condition:
        failures.append(name + (': ' + detail if detail else ''))


SHIM = '''import importlib.util, sys
spec = importlib.util.spec_from_file_location('harness', %r)
harness = importlib.util.module_from_spec(spec)
sys.modules['harness'] = harness
spec.loader.exec_module(harness)
# The preflight needs a container runtime; everything else is the real harness.
harness.preflight = lambda args: 0
sys.exit(harness.main())
'''


def scratch(tmp):
    """A repository copy holding run.sh, a harness shim, and fake tools."""
    repo = tmp / 'repo'
    (repo / 'hack/multiplane').mkdir(parents=True)
    shutil.copy(root / 'hack/multiplane/run.sh', repo / 'hack/multiplane/run.sh')
    (repo / 'hack/multiplane/harness.py').write_text(SHIM % str(root / 'hack/multiplane/harness.py'))
    (repo / 'hack/multiplane/stop.sh').write_text('#!/bin/sh\nexit 0\n')
    for name in ('go.mod', 'go.sum'):
        (repo / name).write_text('')
    bin_dir = tmp / 'bin'
    bin_dir.mkdir()
    # go keeps the build phase running; docker reports no containers.
    # The marker names this scratch copy, so the leftover check sees only
    # this run's phase command.
    for tool, body in (('go', 'exec python3 -c "import time; time.sleep(120)" %s' % tmp.name), ('docker', 'exit 0')):
        path = bin_dir / tool
        path.write_text('#!/bin/sh\n' + body + '\n')
        path.chmod(0o755)
    env = dict(os.environ, PATH=str(bin_dir) + os.pathsep + os.environ['PATH'], FLEET_MULTIPLANE_STATE_ROOT=str(tmp))
    return repo, env


def result_of(tmp):
    results = list(tmp.glob('fleet-multiplane.*/evidence/result.json'))
    return json.loads(results[0].read_text()) if len(results) == 1 else None


def default_signals():
    """Give the run default signal dispositions. A parent started under nohup
    or as a background job ignores SIGHUP or SIGINT, and bash cannot trap a
    signal that was ignored when it started, so the test would then exercise
    the parent's dispositions, not run.sh."""
    for sig in (signal.SIGINT, signal.SIGHUP, signal.SIGTERM):
        signal.signal(sig, signal.SIG_DFL)


def interrupted(sig):
    with tempfile.TemporaryDirectory() as t:
        tmp = pathlib.Path(t)
        repo, env = scratch(tmp)
        log = open(tmp / 'run.out', 'w')
        p = subprocess.Popen(['bash', str(repo / 'hack/multiplane/run.sh')], cwd=repo, env=env, text=True,
                             stdout=log, stderr=subprocess.STDOUT, start_new_session=True, preexec_fn=default_signals)
        deadline = time.monotonic() + 60
        while time.monotonic() < deadline and not list(tmp.glob('fleet-multiplane.*/logs/build.log')):
            time.sleep(0.2)
        check(sig.name + ': the build phase started', bool(list(tmp.glob('fleet-multiplane.*/logs/build.log'))))
        time.sleep(1)
        # Signal only the run itself, as an operator would. It must stop its
        # phase promptly and record the interruption.
        p.send_signal(sig)
        try:
            p.wait(timeout=60)
        except subprocess.TimeoutExpired:
            check(sig.name + ': the run stopped within 60 seconds of the signal', False)
        finally:
            time.sleep(1)
            leftover = subprocess.run(['pgrep', '-f', r'sleep\(120\) %s' % tmp.name], capture_output=True, text=True).stdout.split()
            try:
                os.killpg(p.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            log.close()
        check(sig.name + ': the phase command was stopped', not leftover, str(leftover))
        out = (tmp / 'run.out').read_text()
        lines = [l for l in out.splitlines() if l.startswith('RESULT ')]
        result = result_of(tmp)
        check(sig.name + ': nonzero exit', p.returncode != 0, str(p.returncode))
        check(sig.name + ': RESULT line is not passed', bool(lines) and not lines[-1].startswith('RESULT passed'), str(lines))
        check(sig.name + ': result.json is not passed', result is not None and result['status'] != 'passed', str(result and result['status']))
        check(sig.name + ': result.json names the interrupted phase', result is not None and result.get('failedPhase') == 'build',
              str(result and result.get('failedPhase')))
        check(sig.name + ': result.json exit code is nonzero', result is not None and result['exitCode'] != 0, str(result and result['exitCode']))


for sig in (signal.SIGTERM, signal.SIGINT, signal.SIGHUP):
    interrupted(sig)

# A preflight-only run that succeeds is not an acceptance pass.
with tempfile.TemporaryDirectory() as t:
    tmp = pathlib.Path(t)
    repo, env = scratch(tmp)
    r = subprocess.run(['bash', str(repo / 'hack/multiplane/run.sh'), '--preflight-only'], cwd=repo, env=env, text=True, capture_output=True, timeout=120)
    result = result_of(tmp)
    check('preflight-only: exit 0', r.returncode == 0, str(r.returncode) + r.stdout[-300:] + r.stderr[-300:])
    check('preflight-only: not recorded as passed', result is not None and result['status'] == 'preflight-passed', str(result and result['status']))

# An exit 0 without the completion marker is never passed.
with tempfile.TemporaryDirectory() as t:
    state = pathlib.Path(t)
    (state / 'evidence').mkdir()
    env = dict(os.environ, PATH=str(state) + os.pathsep + os.environ['PATH'])
    docker = state / 'docker'
    docker.write_text('#!/bin/sh\nexit 0\n')
    docker.chmod(0o755)
    r = subprocess.run([sys.executable, str(root / 'hack/multiplane/harness.py'), 'result', '--state', str(state), '--exit-code', '0'], env=env, text=True, capture_output=True)
    result = json.loads((state / 'evidence/result.json').read_text())
    check('incomplete exit 0: not passed', result['status'] != 'passed' and not r.stdout.startswith('RESULT passed'), result['status'])
    check('incomplete exit 0: nonzero', r.returncode != 0 and result['exitCode'] != 0, str(r.returncode))

if failures:
    print('FAIL interrupt tests:\n  ' + '\n  '.join(failures))
    sys.exit(1)
print('PASS interrupt tests')
