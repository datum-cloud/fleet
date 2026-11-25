#!/usr/bin/env python3
"""The runtime lock admits one run at a time, in arrival order.

Drives the real `harness.py lock` against a scratch lock directory, with
sleeping processes standing in for runs: concurrent starts, arrival order,
release when the owner exits or is signalled, recovery from a killed holder
or waiter, the bounded wait, and a waiter stopped while queued. Then runs the
real run.sh in a scratch repository, checks it holds the lock during a phase,
and checks a TERM to the run releases it.
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
harness = root / 'hack/multiplane/harness.py'
failures = []


def check(name, condition, detail=''):
    if not condition:
        failures.append(name + (': ' + detail if detail else ''))


def owner():
    """A stand-in run: the lock follows this process's lifetime."""
    return subprocess.Popen([sys.executable, '-c', 'import time; time.sleep(600)'])


def waiter(tmp, own, name, timeout=60):
    state = tmp / name
    state.mkdir(exist_ok=True)
    env = dict(os.environ, FLEET_MULTIPLANE_LOCK_DIR=str(tmp / 'lock'))
    return subprocess.Popen([sys.executable, str(harness), 'lock', '--state', str(state), '--owner', str(own.pid), '--timeout', str(timeout),
                             '--interval', '0.2', '--status-interval', '1'], env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)


def finished(p, within):
    try:
        p.wait(within)
        return True
    except subprocess.TimeoutExpired:
        return False


def stop(*procs):
    for p in procs:
        if p.poll() is None:
            p.kill()
            p.wait()


def lock_record(tmp, name):
    return json.loads((tmp / name / 'evidence/lock.json').read_text())


def tickets(tmp):
    return list((tmp / 'lock/queue').glob('*.ticket'))


# Two runs that start together: exactly one holds the lock, the other queues
# and takes it when the first run exits.
with tempfile.TemporaryDirectory() as t:
    tmp = pathlib.Path(t)
    a, b = owner(), owner()
    wa, wb = waiter(tmp, a, 'a'), waiter(tmp, b, 'b')
    done = [w for w in (wa, wb) if finished(w, 10)]
    check('concurrent: exactly one run holds the lock', len(done) == 1, str(len(done)))
    if len(done) == 1:
        first, second, first_owner = (wa, wb, a) if done[0] is wa else (wb, wa, b)
        check('concurrent: the holder exits 0', first.returncode == 0, first.stderr.read())
        time.sleep(1.5)
        check('concurrent: the other still waits', second.poll() is None)
        first_owner.terminate()
        first_owner.wait()
        check('concurrent: the waiter acquires once the holder exits', finished(second, 10) and second.returncode == 0)
        err = second.stderr.read()
        check('concurrent: the waiter printed a status line naming the holder', 'queue position 1 of 1; held by pid %d' % first_owner.pid in err, err[-400:])
        check('concurrent: the wait is recorded', lock_record(tmp, 'b' if second is wb else 'a')['status'] == 'acquired')
    stop(a, b, wa, wb)

# Waiters are served in arrival order.
with tempfile.TemporaryDirectory() as t:
    tmp = pathlib.Path(t)
    owners = [owner() for _ in range(4)]
    w0 = waiter(tmp, owners[0], 'w0')
    check('order: the first run acquires', finished(w0, 10) and w0.returncode == 0)
    ws = []
    for i in (1, 2, 3):
        ws.append(waiter(tmp, owners[i], 'w%d' % i))
        time.sleep(0.5)
    check('order: three runs queue', len(tickets(tmp)) == 3, str(len(tickets(tmp))))
    order = []
    for i in (0, 1, 2):
        owners[i].terminate()
        owners[i].wait()
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline and not any(w.poll() is not None and w not in order for w in ws):
            time.sleep(0.1)
        order += [w for w in ws if w.poll() is not None and w not in order]
    check('order: served in arrival order', order == ws, str([ws.index(w) for w in order]))
    stop(*owners, w0, *ws)

# A holder killed outright cannot keep the profile: the kernel drops its flock.
# A waiter killed outright leaves a ticket nobody holds; the next waiter
# removes it instead of waiting behind it.
with tempfile.TemporaryDirectory() as t:
    tmp = pathlib.Path(t)
    a, b, c = owner(), owner(), owner()
    wa = waiter(tmp, a, 'a')
    finished(wa, 10)
    holder = json.loads((tmp / 'lock/holder.json').read_text())
    check('stale: the holder records its owner', holder['owner'] == a.pid, str(holder))
    wb = waiter(tmp, b, 'b')
    time.sleep(1)
    wb.kill()
    wb.wait()
    check('stale: the killed waiter left its ticket', len(tickets(tmp)) == 1)
    wc = waiter(tmp, c, 'c')
    time.sleep(1)
    # Kill the detached holder itself, not its owner: the lock must still free.
    out = subprocess.run(['pgrep', '-f', 'harness.py lock --state %s' % (tmp / 'a')], capture_output=True, text=True).stdout.split()
    check('stale: one detached holder', len(out) == 1, str(out))
    for pid in out:
        os.kill(int(pid), signal.SIGKILL)
    check('stale: the next waiter acquires after the holder is killed', finished(wc, 10) and wc.returncode == 0)
    check('stale: the dead ticket was removed', not tickets(tmp), str(tickets(tmp)))
    stop(a, b, c, wa, wb, wc)

# The holder ignores signals while its owner lives, so a run stopping its own
# process tree keeps the lock through its cleanup.
with tempfile.TemporaryDirectory() as t:
    tmp = pathlib.Path(t)
    a, b = owner(), owner()
    wa = waiter(tmp, a, 'a')
    finished(wa, 10)
    for pid in subprocess.run(['pgrep', '-f', 'harness.py lock --state %s' % (tmp / 'a')], capture_output=True, text=True).stdout.split():
        os.kill(int(pid), signal.SIGTERM)
    # The bounded wait expires while the owner lives, and records why.
    wb = waiter(tmp, b, 'b', timeout=2)
    check('signal: TERM to the holder does not release the lock', finished(wb, 15) and wb.returncode == 4, str(wb.returncode))
    record = lock_record(tmp, 'b')
    check('timeout: recorded', record['status'] == 'timeout' and record['waitedSeconds'] >= 2, str(record))
    check('timeout: ticket removed', not tickets(tmp))
    # A waiter stopped while queued exits with the signal and leaves no ticket.
    wb = waiter(tmp, b, 'b')
    time.sleep(1)
    wb.send_signal(signal.SIGTERM)
    check('queued TERM: exits 143', finished(wb, 10) and wb.returncode == 143, str(wb.returncode))
    check('queued TERM: recorded', lock_record(tmp, 'b')['status'] == 'interrupted')
    check('queued TERM: ticket removed', not tickets(tmp))
    stop(a, b, wa, wb)

# Under the lock, a run waits for the containers of a live run that does not
# take the lock, and fails at once on containers no live run owns.
with tempfile.TemporaryDirectory() as t:
    tmp = pathlib.Path(t)
    bin_dir = tmp / 'bin'
    bin_dir.mkdir()
    running = tmp / 'running'
    running.write_text('fleet-mp-1234abcd-vm\nfleet-mp-1234abcd-cell-control-plane\n')
    (bin_dir / 'docker').write_text('#!/bin/sh\n[ "$1" = ps ] && cat %s\nexit 0\n' % running)
    (bin_dir / 'colima').write_text('#!/bin/sh\necho "/dev/vda1 100 1 99999999 1%% /var/lib/docker"\n')
    for tool in ('docker', 'colima'):
        (bin_dir / tool).chmod(0o755)
    runs = tmp / 'runs'
    other = runs / 'fleet-multiplane.Other1'
    other.mkdir(parents=True)
    (other / 'prefix').write_text('fleet-mp-1234abcd\n')
    mine = tmp / 'mine'
    mine.mkdir()
    env = dict(os.environ, PATH=str(bin_dir) + os.pathsep + os.environ['PATH'], FLEET_MULTIPLANE_STATE_ROOT=str(runs))

    def contention(wait):
        return subprocess.Popen([sys.executable, str(harness), 'preflight', '--checks', 'contention', '--wait', str(wait), '--state', str(mine)],
                                env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    started = time.monotonic()
    c = contention(60)
    check('foreign: containers no live run owns fail at once', finished(c, 20) and c.returncode == 2, str(c.returncode))
    check('foreign: and quickly', time.monotonic() - started < 20)
    live = subprocess.Popen([sys.executable, '-c', 'import time; time.sleep(600)', str(other)])
    c = contention(60)
    time.sleep(5)
    check('foreign: a live run without the lock is waited for', c.poll() is None)
    live.kill()
    live.wait()
    running.write_text('')
    check('foreign: the check passes once that run is done', finished(c, 40) and c.returncode == 0, str(c.returncode))
    pre = json.loads((mine / 'evidence/preflight.json').read_text())
    check('foreign: recorded', [x['ok'] for x in pre['checks'] if x['name'] == 'no-concurrent-fleet-runtime'] == [True], str(pre))
    stop(live, c)

# The real run.sh holds the lock through its phases and releases it when TERM
# stops the run.
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
    (repo / 'hack/multiplane/stop.sh').write_text('#!/bin/sh\nexit 0\n')
    for name in ('go.mod', 'go.sum'):
        (repo / name).write_text('')
    bin_dir = tmp / 'bin'
    bin_dir.mkdir()
    for tool, body in (('go', 'exec python3 -c "import time; time.sleep(120)" %s' % tmp.name), ('docker', 'exit 0')):
        (bin_dir / tool).write_text('#!/bin/sh\n' + body + '\n')
        (bin_dir / tool).chmod(0o755)
    runs = tmp / 'runs'
    runs.mkdir()
    env = dict(os.environ, PATH=str(bin_dir) + os.pathsep + os.environ['PATH'], FLEET_MULTIPLANE_STATE_ROOT=str(runs),
               FLEET_MULTIPLANE_LOCK_DIR=str(tmp / 'lock'))
    p = subprocess.Popen(['bash', str(repo / 'hack/multiplane/run.sh')], cwd=repo, env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                         start_new_session=True)
    deadline = time.monotonic() + 60
    while time.monotonic() < deadline and not list(runs.glob('fleet-multiplane.*/logs/build.log')):
        time.sleep(0.2)
    check('run.sh: the build phase started', bool(list(runs.glob('fleet-multiplane.*/logs/build.log'))))
    b = owner()
    probe = waiter(tmp, b, 'probe', timeout=1)
    check('run.sh: holds the lock during a phase', finished(probe, 10) and probe.returncode == 4, str(probe.returncode))
    p.send_signal(signal.SIGTERM)
    check('run.sh: stops on TERM', finished(p, 60) and p.returncode == 143, str(p.returncode))
    probe = waiter(tmp, b, 'probe', timeout=10)
    check('run.sh: TERM released the lock', finished(probe, 15) and probe.returncode == 0, str(probe.returncode))
    result = json.loads(next(runs.glob('fleet-multiplane.*/evidence/result.json')).read_text())
    check('run.sh: result records the lock', (result.get('runtimeLock') or {}).get('status') == 'acquired', str(result.get('runtimeLock')))
    try:
        os.killpg(p.pid, signal.SIGKILL)
    except ProcessLookupError:
        pass
    stop(b, probe)

if failures:
    print('FAIL lock tests:\n  ' + '\n  '.join(failures))
    sys.exit(1)
print('PASS lock tests')
