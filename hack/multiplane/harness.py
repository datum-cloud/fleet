#!/usr/bin/env python3
"""Shared local acceptance helpers: preflight, bounded phases, sanitized results.

Exit codes are part of the harness contract (see docs/operations/multiplane.md):
0 passed, 1 failed, 2 preflight failed, 3 blocked by implementation readiness,
4 bounded wait expired, 64 usage. Nothing here deletes resources.
"""
import argparse
import datetime
import fcntl
import hashlib
import json
import os
import pathlib
import re
import shutil
import signal
import subprocess
import sys
import time

PROFILE = os.environ.get('FLEET_MULTIPLANE_PROFILE', 'fleet-acceptance-tcg')
CONTEXT = 'colima-' + PROFILE
EXIT_PASSED, EXIT_FAILED, EXIT_PREFLIGHT, EXIT_BLOCKED, EXIT_TIMEOUT, EXIT_USAGE = 0, 1, 2, 3, 4, 64
STATUS_BY_EXIT = {EXIT_PASSED: 'passed', EXIT_FAILED: 'failed', EXIT_PREFLIGHT: 'preflight-failed',
                  EXIT_BLOCKED: 'blocked', EXIT_TIMEOUT: 'timeout', EXIT_USAGE: 'usage'}
# A run or phase ended by a signal exits 128 plus the signal number.
INTERRUPT_SIGNALS = (signal.SIGTERM, signal.SIGINT, signal.SIGHUP)
# run.sh writes these markers as its last act, so exit 0 without one is an
# interrupted run, never a pass.
COMPLETED, PREFLIGHT_COMPLETED = 'completed', 'preflight-completed'


class Interrupted(Exception):
    pass
DEBIAN = 'debian:bookworm-slim@sha256:734a72b3d6142a71993b4bc580ca01d8ca687f77a2c422d96a961d3376b179bc'
# Subcommands and errors the live harness depends on. A missing one blocks before any resource exists.
REQUIRED_SOURCE_MARKERS = tuple('"%s"' % c for c in ('seed', 'allocate', 'admit-install', 'install', 'release', 'release-b', 'reuse',
                                                     'coordinator', 'image', 'boot-service', 'boot-denials', 'site-worker', 'site-observer',
                                                     'admit-install-b', 'recovery-mismatch', 'probe-unallocatable', 'tenant-isolation')) + ('multiplane.ErrLocationNotServed',)
READINESS_PACKAGES = ('./internal/coordination', './internal/custody', './internal/site/...', './internal/multiplane', './cmd/fleet-multiplane')
# Redfish mode (run.sh --redfish) adds its executor and probe to the readiness gate.
PROVIDER = os.environ.get('FLEET_SITE_PROVIDER', 'libvirt')
if PROVIDER == 'redfish':
    REQUIRED_SOURCE_MARKERS += ('"redfish-probe"', 'redfish.NewPinnedClient')
    READINESS_PACKAGES += ('./internal/redfish',)
# The one Device the acceptance allocates, as the site store names it. In
# Redfish mode it is enrolled as lab target A (System-1), a persistent domain.
DEVICE = 'fleet-provider/synthetic-device-central-01'
LAB_DOMAIN = 'fleet-lab-target-a'
# The site store on the cell's retained volume (internal/site/store.go): one
# directory per Device, named by the first 16 bytes of the Device's SHA-256.
SITE_STORE = '/var/lib/fleet-local/site'


def device_dir(device=DEVICE):
    return SITE_STORE + '/devices/' + hashlib.sha256(device.encode()).hexdigest()[:32]


STATE_PATH = device_dir() + '/state.json'
RESULTS_DIR = device_dir() + '/results'
# The journaled step that ends each kind, in either mode (internal/site/vmwork
# and internal/site/redfishwork).
FINAL_STEPS = {'DeviceInstallation': ('disk-verified',), 'DeviceWipe': ('wipe', 'wipe-verified')}
# Provider-project custody records, read with the provider's bootstrap credential.
CUSTODY_KINDS = ('devicecustodies.fleet.datumapis.com', 'deviceinstallations.fleet.datumapis.com', 'devicewipes.fleet.datumapis.com',
                 'devicerecoveries.fleet.datumapis.com', 'machineallocations.metal.datumapis.com')


def now():
    return datetime.datetime.now(datetime.timezone.utc).isoformat()


def env():
    e = dict(os.environ)
    e['DOCKER_CONTEXT'] = CONTEXT
    return e


def capture(args, timeout=60, **kw):
    """Bounded command; returns (code, stdout, stderr). Missing tools return 127."""
    try:
        r = subprocess.run(args, text=True, capture_output=True, timeout=timeout, env=kw.pop('env', env()), **kw)
        return r.returncode, r.stdout, r.stderr
    except FileNotFoundError:
        return 127, '', args[0] + ' not found'
    except subprocess.TimeoutExpired:
        return 124, '', 'timed out after %ss' % timeout


def evidence_dir(state):
    d = pathlib.Path(state) / 'evidence'
    d.mkdir(mode=0o700, parents=True, exist_ok=True)
    return d


def write_json(path, value):
    path = pathlib.Path(path)
    tmp = path.with_suffix(path.suffix + '.tmp')
    tmp.write_text(json.dumps(value, indent=2, sort_keys=True) + '\n')
    tmp.chmod(0o600)
    tmp.replace(path)


# ---------------------------------------------------------------- preflight

def running_fleet_containers():
    code, out, _ = capture(['docker', 'ps', '--format', '{{.Names}}'], 30)
    return code, sorted(n for n in out.split() if re.match(r'^fleet-(local|mp)-', n))


def live_run(prefix):
    """The state directory of the live run that owns a container prefix, or
    None. A run is live while any process names its state directory; a run
    that exited and left containers running has none. A run is between
    processes only briefly, so it is looked for three times."""
    roots = {pathlib.Path(os.environ.get('FLEET_MULTIPLANE_STATE_ROOT', '/tmp')), pathlib.Path('/tmp')}
    for state in (d for r in roots for d in r.glob('fleet-multiplane.*')):
        try:
            if (state / 'prefix').read_text().strip() != prefix:
                continue
        except OSError:
            continue
        for attempt in range(3):
            code, out, _ = capture(['pgrep', '-f', '--', re.escape(str(state))], 10, env=dict(os.environ))
            pids = out.split() if code == 0 else []
            # A coordinator outlives a run killed outright; it alone is not a live run.
            code, out, _ = capture(['ps', '-o', 'command=', '-p', ','.join(pids)], 10, env=dict(os.environ)) if pids else (1, '', '')
            if any(' coordinator --config ' not in c for c in out.splitlines() if c.strip()):
                return str(state)
            time.sleep(1)
        return None
    return None


def contention_checks(check, wait=0):
    """Checks another run on the profile can change. run.sh repeats them while
    it holds the runtime lock, so no two runs can both pass them. With wait,
    containers of a live run that does not take the lock (an older checkout)
    are waited for, up to wait seconds; anything else fails at once."""
    deadline, last, expired = time.monotonic() + wait, 0.0, False
    while True:
        code, active = running_fleet_containers()
        prefixes = sorted({m.group(0) for m in (re.match(r'^fleet-mp-[a-f0-9]{8}', n) for n in active) if m})
        owners = {p: live_run(p) for p in prefixes} if wait and code == 0 else {}
        waitable = active and len(prefixes) and all(owners.values()) and all(n.startswith(tuple(prefixes)) for n in active)
        if not waitable:
            break
        if time.monotonic() >= deadline:
            expired = bool(wait)
            break
        if time.monotonic() - last >= 60:
            print('lock %s: held; waiting for a run without the lock to finish: %s' % (PROFILE, ', '.join('%s (%s)' % kv for kv in owners.items())),
                  file=sys.stderr, flush=True)
            last = time.monotonic()
        time.sleep(15)
    check('no-concurrent-fleet-runtime', code == 0 and not active, ('running: ' + ', '.join(active[:6])) if active else 'none running; stopped diagnostics are left untouched')
    free = vm_free_bytes()
    check('runtime-disk', free is not None and free >= 12 * 2**30, ('%.1f GiB free in docker data root; 12 GiB required' % (free / 2**30)) if free is not None else 'unable to read docker data-root free space')
    return expired


def readiness_digest(root):
    """Digest of every source the readiness gate tests and the run builds."""
    root = pathlib.Path(root)
    paths = [p for name in ('internal', 'cmd') for p in (root / name).rglob('*') if p.is_file() and '__pycache__' not in p.parts]
    paths += [root / n for n in ('go.mod', 'go.sum') if (root / n).is_file()]
    h = hashlib.sha256()
    for p in sorted(paths):
        h.update(str(p.relative_to(root)).encode() + b'\0' + hashlib.sha256(p.read_bytes()).digest())
    return h.hexdigest()


def readiness_checks(check, args):
    """The implementation gate. It records the digest of the sources it
    gated, so a run that queued can tell whether they changed while it waited."""
    root = pathlib.Path(args.root)
    digest = readiness_digest(root)
    main = (root / 'cmd/fleet-multiplane/main.go')
    source = main.read_text() if main.exists() else ''
    missing = [c for c in REQUIRED_SOURCE_MARKERS if c not in source]
    check('implementation:subcommands', not missing, ('missing: ' + ', '.join(missing)) if missing else 'all harness subcommands and denial errors present', 'readiness')
    if args.skip_tests:
        check('implementation:tests', True, 'skipped by caller', 'readiness')
    else:
        code, out, err = capture(['go', 'test', '-race', '-count=1', *READINESS_PACKAGES], 900, cwd=str(root), env=dict(os.environ))
        failing = sorted(set(re.findall(r'--- FAIL: (\S+)', out + err)))
        check('implementation:tests', code == 0, 'passed' if code == 0 else 'failing: ' + (', '.join(failing[:8]) or (out + err).strip()[-300:]), 'readiness')
    (pathlib.Path(args.state) / 'readiness-SHA256').write_text(digest + '\n')


def record_preflight(args, checks, code):
    path = evidence_dir(args.state) / 'preflight.json'
    earlier = json.loads(path.read_text())['checks'] if args.checks == 'contention' and path.exists() else []
    earlier = [c for c in earlier if c['name'] not in {n['name'] for n in checks}]
    write_json(path, {'schemaVersion': 1, 'observedAt': now(), 'profile': PROFILE, 'status': STATUS_BY_EXIT[code], 'checks': earlier + checks})
    for c in checks:
        print('%-4s %-34s %s' % ('ok' if c['ok'] else 'FAIL', c['name'], c['detail']))
    return code


def preflight(args):
    checks = []

    def check(name, ok, detail='', kind='preflight'):
        checks.append({'name': name, 'ok': bool(ok), 'detail': detail, 'kind': kind})
        return ok

    if args.checks == 'contention':
        # The wait for a run without the lock shares the lock's bound.
        lock_record = evidence_dir(args.state) / 'lock.json'
        waited = json.loads(lock_record.read_text()).get('waitedSeconds', 0) if lock_record.exists() else 0
        expired = contention_checks(check, max(0, args.wait - int(waited)))
        # Sources edited while the run queued are gated again, under the lock.
        gated = pathlib.Path(args.state) / 'readiness-SHA256'
        if gated.exists() and gated.read_text().strip() != readiness_digest(args.root):
            check('readiness-regated', True, 'sources changed while queued; readiness gated again', 'readiness')
            readiness_checks(check, args)
        pre = [c for c in checks if c['kind'] == 'preflight' and not c['ok']]
        ready = [c for c in checks if c['kind'] == 'readiness' and not c['ok']]
        return record_preflight(args, checks, EXIT_TIMEOUT if expired else EXIT_PREFLIGHT if pre else EXIT_BLOCKED if ready else EXIT_PASSED)

    for tool in ('go', 'python3', 'openssl', 'docker', 'kind', 'kubectl', 'kustomize', 'curl', 'git'):
        check('tool:' + tool, shutil.which(tool), 'required on PATH')
    check('python>=3.12', sys.version_info >= (3, 12), sys.version.split()[0] + ' (evidence extraction uses the tarfile data filter)')

    code, out, _ = capture(['docker', 'context', 'inspect', CONTEXT, '--format', '{{.Endpoints.docker.Host}}'], 20)
    endpoint_ok = code == 0 and out.strip().endswith('/.colima/%s/docker.sock' % PROFILE)
    if not endpoint_ok and args.bootstrap and shutil.which('colima'):
        # Only start an existing task-owned profile with its saved configuration.
        code, out, err = capture(['colima', 'list', '--json'], 30, cwd='/')
        known = any(json.loads(line).get('name') == PROFILE for line in out.splitlines() if line.strip().startswith('{')) if code == 0 else False
        if known:
            code, _, err = capture(['colima', 'start', '--profile', PROFILE], 600, cwd='/')
            check('bootstrap:colima-start', code == 0, 'started existing profile ' + PROFILE if code == 0 else err.strip()[-300:])
            code, out, _ = capture(['docker', 'context', 'inspect', CONTEXT, '--format', '{{.Endpoints.docker.Host}}'], 20)
            endpoint_ok = code == 0 and out.strip().endswith('/.colima/%s/docker.sock' % PROFILE)
    check('docker-context', endpoint_ok, CONTEXT + ' must point at the task-owned Colima profile socket; never fleet-e2e-x86')

    info = {}
    if endpoint_ok:
        code, out, err = capture(['docker', 'info', '--format', '{{json .}}'], 30)
        if check('docker-reachable', code == 0, 'start the profile: colima start --profile %s (or run with --bootstrap)' % PROFILE if code else 'reachable'):
            info = json.loads(out)
    if info:
        check('runtime-architecture', info.get('Architecture') in ('aarch64', 'arm64'), '%s (the Milo image pin is native arm64)' % info.get('Architecture'))
        check('runtime-memory', int(info.get('MemTotal', 0)) >= 7_800_000_000, '%.2f GiB; 8 GiB required' % (int(info.get('MemTotal', 0)) / 2**30))
        check('runtime-cpus', int(info.get('NCPU', 0)) >= 4, '%s CPUs; 4 required' % info.get('NCPU'))
        if args.checks != 'host':
            contention_checks(check)
        code, out, err = capture(['docker', 'run', '--rm', '--platform', 'linux/amd64', '--entrypoint', 'uname', DEBIAN, '-m'], 300)
        check('amd64-emulation', code == 0 and out.strip() == 'x86_64', 'installer image build needs amd64 container execution' + ('' if code == 0 else ': ' + err.strip()[-200:]))
        limit = vm_inotify()
        opted = os.environ.get('FLEET_MULTIPLANE_INOTIFY') == '1024'
        check('inotify-instances', limit is not None and (limit >= 1024 or opted), 'current %s; requires >=1024 or FLEET_MULTIPLANE_INOTIFY=1024 (reversible task-VM adjustment)' % limit)

    readiness_checks(check, args)
    pre = [c for c in checks if c['kind'] == 'preflight' and not c['ok']]
    ready = [c for c in checks if c['kind'] == 'readiness' and not c['ok']]
    code = EXIT_PREFLIGHT if pre else EXIT_BLOCKED if ready else EXIT_PASSED
    return record_preflight(args, checks, code)


def vm_exec(command, timeout=30):
    if not shutil.which('colima'):
        return None
    code, out, _ = capture(['colima', 'ssh', '--profile', PROFILE, '--', 'sh', '-c', command], timeout, cwd='/')
    return out.strip() if code == 0 else None


def vm_free_bytes():
    out = vm_exec('df -Pk /var/lib/docker | tail -1')
    try:
        return int(out.split()[3]) * 1024
    except (AttributeError, IndexError, ValueError):
        return None


def vm_inotify():
    out = vm_exec('cat /proc/sys/fs/inotify/max_user_instances')
    try:
        return int(out)
    except (TypeError, ValueError):
        return None


# ---------------------------------------------------------------- runtime lock

# One run at a time holds the profile. The lock is a kernel flock on a file
# named for the profile, so it is atomic and the kernel releases it when its
# holder dies: a dead run can never leave the profile locked. Waiters queue in
# arrival order with one ticket each, also flocked, so a dead waiter's ticket
# is recognized and removed. Tests point FLEET_MULTIPLANE_LOCK_DIR elsewhere.
LOCK_DIR = pathlib.Path(os.environ.get('FLEET_MULTIPLANE_LOCK_DIR', '/tmp/fleet-multiplane-lock.' + PROFILE))
LOCK_TIMEOUT = int(os.environ.get('FLEET_MULTIPLANE_LOCK_TIMEOUT', str(4 * 3600)))


def try_flock(fd):
    try:
        fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        return True
    except BlockingIOError:
        return False


def process_started(pid):
    """The process start time, so a reused pid is not mistaken for its owner."""
    code, out, _ = capture(['ps', '-o', 'lstart=', '-p', str(pid)], 10, env=dict(os.environ))
    return out.strip() if code == 0 and out.strip() else None


def owner_alive(pid, started):
    """Whether the owning run still runs: the same process, and not a zombie
    its parent has yet to reap."""
    code, out, _ = capture(['ps', '-o', 'stat=', '-o', 'lstart=', '-p', str(pid)], 10, env=dict(os.environ))
    fields = out.split(None, 1) if code == 0 else []
    return len(fields) == 2 and not fields[0].startswith('Z') and fields[1].strip() == started


def lock_dir():
    """The lock directory, created private. One another user created, or a
    symlink, is refused: it could block or redirect the lock."""
    LOCK_DIR.mkdir(mode=0o700, parents=True, exist_ok=True)
    st = os.lstat(LOCK_DIR)
    if LOCK_DIR.is_symlink() or st.st_uid != os.getuid() or st.st_mode & 0o077:
        raise SystemExit('lock: %s is not a private directory this user owns' % LOCK_DIR)
    (LOCK_DIR / 'queue').mkdir(mode=0o700, exist_ok=True)


def read_holder():
    try:
        return json.loads((LOCK_DIR / 'holder.json').read_text())
    except (OSError, ValueError):
        return None


def live_tickets(mine):
    """Live tickets in arrival order. A ticket nobody holds is a dead waiter's: remove it."""
    live = []
    for t in sorted((LOCK_DIR / 'queue').glob('*.ticket')):
        if t.name == mine.name:
            live.append(t)
            continue
        try:
            fd = os.open(t, os.O_RDONLY)
        except FileNotFoundError:
            continue
        try:
            if try_flock(fd):
                t.unlink(missing_ok=True)
            else:
                live.append(t)
        finally:
            os.close(fd)
    return live


def lock_status(position, waiters, waited, timeout):
    holder = read_holder() or {}
    phase = None
    if holder.get('state'):
        try:
            phase = (pathlib.Path(holder['state']) / 'current-phase').read_text().strip()
        except OSError:
            pass
    since = ''
    if holder.get('acquiredEpoch'):
        since = ', held %dm' % ((time.time() - holder['acquiredEpoch']) // 60)
    held = 'held by pid %s (%s, %s%s%s)' % (holder.get('owner', '?'), holder.get('provider', '?'), holder.get('state', '?'),
                                            ', phase ' + phase if phase else '', since) if holder else 'free'
    return 'lock %s: queue position %d of %d; %s; waited %dm of %dm' % (PROFILE, position, waiters, held, waited // 60, timeout // 60)


def lock(args):
    """Queue for the profile, take the lock, and hand it to a detached holder
    that keeps it until the owning run exits. Exits 0 once held, 4 when the
    bounded wait expires, 1 if the owning run is gone, and 128 plus the
    signal number if stopped while queued."""
    owner_started = process_started(args.owner)
    if owner_started is None:
        print('lock: owner pid %s is not running' % args.owner, file=sys.stderr)
        return EXIT_USAGE
    lock_dir()
    # Under with-lock.sh the wrapper already holds the lock for this run.
    wrapper = os.environ.get('FLEET_MULTIPLANE_LOCK_OWNER')
    holder = read_holder() or {}
    if wrapper and str(holder.get('owner')) == wrapper and owner_alive(int(wrapper), holder.get('ownerStarted')):
        write_json(evidence_dir(args.state) / 'lock.json', {'schemaVersion': 1, 'profile': PROFILE, 'status': 'acquired', 'heldBy': 'with-lock.sh',
                                                            'positionAtQueue': 1, 'waitedSeconds': 0, 'finishedAt': now()})
        print('lock %s: held by with-lock.sh (pid %s)' % (PROFILE, wrapper), file=sys.stderr, flush=True)
        return 0
    ev = evidence_dir(args.state)
    queued = time.time()
    received = []

    def on_signal(signum, _frame):
        # Only the first signal interrupts; later ones arrive during cleanup.
        received.append(signum)
        if len(received) == 1:
            raise Interrupted()
    for sig in INTERRUPT_SIGNALS:
        signal.signal(sig, on_signal)
    record = {'schemaVersion': 1, 'profile': PROFILE, 'queuedAt': now(), 'timeoutSeconds': args.timeout}
    status, line, last_print, ticket, ticket_fd, lock_fd = None, None, 0.0, None, None, None
    tmp = LOCK_DIR / 'queue' / ('.%d.tmp' % os.getpid())
    try:
        # The ticket is flocked before it is visible, so no waiter can see it unheld.
        ticket_fd = os.open(tmp, os.O_RDWR | os.O_CREAT | os.O_TRUNC, 0o600)
        fcntl.flock(ticket_fd, fcntl.LOCK_EX)
        os.write(ticket_fd, json.dumps({'owner': args.owner, 'state': str(args.state)}).encode())
        ticket = LOCK_DIR / 'queue' / ('%020d-%d.ticket' % (time.time_ns(), os.getpid()))
        os.rename(tmp, ticket)
        lock_fd = os.open(LOCK_DIR / 'lock', os.O_RDWR | os.O_CREAT, 0o600)
        while True:
            waited = time.time() - queued
            live = live_tickets(ticket)
            position = live.index(ticket) + 1 if ticket in live else 1
            record.setdefault('positionAtQueue', position)
            if position == 1 and try_flock(lock_fd):
                status = 'acquired'
                break
            if waited >= args.timeout:
                status = 'timeout'
                break
            # A run killed outright leaves this waiter behind: give up its place.
            if not owner_alive(args.owner, owner_started):
                status = 'owner-gone'
                break
            line_now = lock_status(position, len(live), int(waited), args.timeout)
            if line_now != line or time.time() - last_print >= args.status_interval:
                print(line_now, file=sys.stderr, flush=True)
                line, last_print = line_now, time.time()
            time.sleep(args.interval)
    except Interrupted:
        status = 'interrupted'
    for sig in INTERRUPT_SIGNALS:
        signal.signal(sig, signal.SIG_IGN)
    tmp.unlink(missing_ok=True)
    if ticket:
        ticket.unlink(missing_ok=True)
    if ticket_fd is not None:
        os.close(ticket_fd)
    record.update(status=status, waitedSeconds=round(time.time() - queued, 1), finishedAt=now())
    if status != 'acquired':
        if lock_fd is not None:
            os.close(lock_fd)
        write_json(ev / 'lock.json', record)
        print('lock %s: %s after %dm; no resources were created' % (
            PROFILE, {'timeout': 'wait expired', 'owner-gone': 'owning run gone'}.get(status, 'stopped while queued'), record['waitedSeconds'] // 60),
            file=sys.stderr, flush=True)
        return {'timeout': EXIT_TIMEOUT, 'owner-gone': EXIT_FAILED}.get(status) or 128 + received[0]
    record['acquiredAt'] = record['finishedAt']
    holder = {'owner': args.owner, 'ownerStarted': owner_started, 'state': str(args.state), 'acquiredEpoch': time.time(),
              'acquiredAt': record['acquiredAt'], 'provider': PROVIDER}
    write_json(LOCK_DIR / 'holder.json', holder)
    write_json(ev / 'lock.json', record)
    if os.fork() == 0:
        # The holder: detached from the run's session and output, deaf to the
        # signals the run forwards, it keeps the inherited flock until the
        # owning run has exited and finished its own cleanup.
        os.setsid()
        null = os.open(os.devnull, os.O_RDWR)
        for fd in (0, 1, 2):
            os.dup2(null, fd)
        while owner_alive(args.owner, owner_started):
            time.sleep(1)
        if (read_holder() or {}).get('owner') == args.owner:
            (LOCK_DIR / 'holder.json').unlink(missing_ok=True)
        os._exit(0)
    os.close(lock_fd)
    print('lock %s: acquired after %dm' % (PROFILE, record['waitedSeconds'] // 60), file=sys.stderr, flush=True)
    return 0


# ---------------------------------------------------------------- phases

def phase(args):
    """Run one bounded phase, keep its full output private, record a sanitized entry."""
    state = pathlib.Path(args.state)
    logs = state / 'logs'
    logs.mkdir(mode=0o700, exist_ok=True)
    log = logs / (args.name + '.log')
    started = time.monotonic()
    record = {'name': args.name, 'startedAt': now(), 'timeoutSeconds': args.timeout}
    print('==> %s (bound %ss)' % (args.name, args.timeout), flush=True)
    # A signal stops the phase's command and records the phase interrupted.
    received = []

    def on_signal(signum, _frame):
        received.append(signum)
        raise Interrupted()
    for sig in INTERRUPT_SIGNALS:
        signal.signal(sig, on_signal)
    (state / 'current-phase').write_text(args.name + '\n')
    with open(log, 'ab') as out, open(state / 'phases.log', 'ab') as markers:
        p = subprocess.Popen(args.command, stdout=subprocess.PIPE, stderr=out, start_new_session=True)
        try:
            deadline = started + args.timeout
            timed_out = False
            import selectors
            sel = selectors.DefaultSelector()
            os.set_blocking(p.stdout.fileno(), False)
            sel.register(p.stdout, selectors.EVENT_READ)
            eof = False
            while True:
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    timed_out = True
                    break
                if eof:
                    try:
                        p.wait(timeout=remaining)
                    except subprocess.TimeoutExpired:
                        timed_out = True
                    break
                if sel.select(timeout=min(remaining, 1.0)):
                    line = os.read(p.stdout.fileno(), 65536)
                    if not line:
                        sel.unregister(p.stdout)
                        eof = True
                        continue
                    out.write(line)
                    markers.write(line)
                    out.flush()
                    markers.flush()
                    if args.echo:
                        sys.stdout.write(''.join('    ' + l for l in line.decode(errors='replace').splitlines(True)))
                        sys.stdout.flush()
            sel.close()
            p.stdout.close()
            if timed_out:
                terminate(p)
            code = p.wait(timeout=5)
        except Interrupted:
            for sig in INTERRUPT_SIGNALS:
                signal.signal(sig, signal.SIG_IGN)
            terminate(p)
            p.stdout.close()
            code = 128 + received[0]
    record['durationSeconds'] = round(time.monotonic() - started, 1)
    record['finishedAt'] = now()
    if received:
        record.update(status='interrupted', exitCode=code)
    elif timed_out:
        record.update(status='timeout', exitCode=EXIT_TIMEOUT)
    else:
        record.update(status='passed' if code == 0 else 'blocked' if code == EXIT_BLOCKED else 'failed', exitCode=code)
    with open(evidence_dir(state) / 'phases.jsonl', 'a') as f:
        f.write(json.dumps(record, sort_keys=True) + '\n')
    (state / 'current-phase').unlink(missing_ok=True)
    if record['status'] != 'passed':
        print('<== %s %s (exit %s); private log: %s' % (args.name, record['status'], record['exitCode'], log), flush=True)
        tail = log.read_bytes()[-1500:].decode(errors='replace')
        print('\n'.join('    ' + l for l in tail.splitlines()[-15:]), flush=True)
    if received:
        return code
    return EXIT_TIMEOUT if timed_out else (0 if code == 0 else EXIT_BLOCKED if code == EXIT_BLOCKED else EXIT_FAILED)


def terminate(p):
    for sig, wait in ((signal.SIGTERM, 15), (signal.SIGKILL, 5)):
        try:
            os.killpg(p.pid, sig)
        except ProcessLookupError:
            return
        try:
            p.wait(wait)
            # The group may still contain children after its leader exits.
            try:
                os.killpg(p.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            return
        except subprocess.TimeoutExpired:
            continue


# ---------------------------------------------------------------- provider observation

LIBVIRT_URI = 'qemu:///system?socket=/run/fleet-libvirt/libvirt-sock'


def runtime_exec(state, *command, timeout=20):
    """Run a read-only command in the site VM runtime Pod through the cell API."""
    kubeconfig = str(pathlib.Path(state) / 'cell-admin.yaml')
    return capture(['kubectl', '--kubeconfig', kubeconfig, '--request-timeout=15s', '-n', 'fleet-site', 'exec',
                    'deploy/fleet-site-runtime', '-c', 'libvirt', '--', *command], timeout)


def read_site_store(run):
    """The Device's site store state and its stored results, or (None, []).
    run(*command) runs a read-only command in the runtime Pod and returns
    (exit code, stdout)."""
    code, out = run('cat', STATE_PATH)
    if code != 0:
        return None, []
    store = json.loads(out)
    code, out = run('sh', '-c', 'for f in %s/*.json; do [ -f "$f" ] && cat "$f" && echo; done; true' % RESULTS_DIR)
    return store, [json.loads(l) for l in out.splitlines() if l.strip()] if code == 0 else []


def site_store(state):
    return read_site_store(lambda *command: runtime_exec(state, *command)[:2])


def journal(state):
    store, results = site_store(state)
    return None if store is None else sanitized_operations(store, results)


def domains(state):
    # Running domains only; Redfish lab targets stay defined while powered off.
    code, out, _ = runtime_exec(state, 'virsh', '--connect', LIBVIRT_URI, 'list', '--name')
    return out.split() if code == 0 else None


def key(operation_uid):
    """An operation's 16-character key in domain, disk and boot evidence names."""
    return hashlib.sha256(operation_uid.encode()).hexdigest()[:16]


def domain_name(operation_uid):
    return 'fleet-local-' + key(operation_uid)


def operation_kind(steps):
    return 'DeviceWipe' if any(s == 'wipe' or s.startswith('wipe-') for s in steps) else 'DeviceInstallation'


def sanitized_operations(store, results=(), redfish=None):
    """Each journaled operation in admission order: its UID, fence, kind (from
    its step names), steps with their states, and its stored result phase.
    Step payloads are never read out."""
    redfish = PROVIDER == 'redfish' if redfish is None else redfish
    ops = {}
    for e in (store or {}).get('journal') or []:
        o = ops.setdefault(e['operation'], {'operation': e['operation'], 'fence': e['fence'], 'steps': []})
        o['steps'].append([e['step'], e['state']])
    phases = {(r['result']['operation']['uid'], r['result']['fence']): r['result'].get('phase') for r in results}
    out = []
    for o in ops.values():
        kind = operation_kind([n for n, _ in o['steps']])
        o.update(kind=kind, stage=o['steps'][-1][0], stageState=o['steps'][-1][1],
                 done=any(n in FINAL_STEPS[kind] and st == 'Done' for n, st in o['steps']),
                 phase=phases.get((o['operation'], o['fence'])), domain=LAB_DOMAIN if redfish else domain_name(o['operation']))
        out.append(o)
    return out


def past_installer(op):
    """Whether an install verified its installer phase or began its disk phase."""
    return any(n == 'installer-verified' or n.startswith('disk-') for n, _ in op['steps'])


def custody(state):
    """The provider project's custody, operations and allocations."""
    config = pathlib.Path(state) / 'provider' / 'bootstrap-project.json'
    code, out, err = capture(['kubectl', '--kubeconfig', str(config), '--request-timeout=15s', '-n', 'fleet-provider', 'get', ','.join(CUSTODY_KINDS), '-o', 'json'], 30)
    if code != 0:
        raise SystemExit('provider custody unreadable: ' + err.strip()[-200:])
    return json.loads(out)['items']


def custody_digest(state):
    """Count and digest of the custody facts: spec, phase and fence of each record."""
    rows = sorted((i['kind'], i['metadata']['name'], i['metadata']['uid'], json.dumps(i.get('spec'), sort_keys=True),
                   i.get('status', {}).get('phase'), i.get('status', {}).get('fence')) for i in custody(state))
    return len(rows), hashlib.sha256(json.dumps(rows).encode()).hexdigest()


def wait_stage(args):
    """Wait until exactly one install is in args.stage with its domain running."""
    deadline = time.monotonic() + args.timeout
    last = None
    while time.monotonic() < deadline:
        installs = [o for o in journal(args.state) or [] if o['kind'] == 'DeviceInstallation']
        running = domains(args.state) or []
        last = {'installs': installs, 'runningDomains': len(running)}
        if len(installs) > 1:
            print(json.dumps({'error': 'more than one install operation', **last}))
            return EXIT_FAILED
        if installs and past_installer(installs[0]):
            print(json.dumps({'error': 'installer completed before the interrupt window', **last}))
            return EXIT_FAILED
        if installs and installs[0]['stage'] == args.stage and installs[0]['domain'] in running:
            print(json.dumps({'observedAt': now(), 'operation': installs[0], 'domainRunning': True}))
            return 0
        time.sleep(args.interval)
    print(json.dumps({'error': 'stage not observed before deadline', **(last or {})}))
    return EXIT_TIMEOUT


def journal_cmd(args):
    print(json.dumps(journal(args.state) or [], sort_keys=True))
    return 0


def custody_cmd(args):
    print('%d %s' % custody_digest(args.state))
    return 0


# ---------------------------------------------------------------- secrets and result

TOKEN = re.compile(r'''["']?\b(?:token|client-key-data)["']?\s*:\s*["']?([A-Za-z0-9+/=._-]{16,})''')


def secrets(state):
    state = pathlib.Path(state)
    found = set()
    for p in state.rglob('*'):
        if not p.is_file() or 'evidence' in p.relative_to(state).parts or p.stat().st_size > 4 << 20:
            continue
        try:
            text = p.read_text(errors='ignore')
        except OSError:
            continue
        if p.suffix == '.token':
            found.add(text.strip())
        if p.name == 'tokens.csv':
            found.update(line.split(',')[0] for line in text.splitlines() if line.count(',') >= 2)
        for m in TOKEN.finditer(text):
            found.add(m.group(1))
        # Copied site store boot admissions carry the one-time boot capability.
        if p.suffix == '.json' and p.parent.name in ('vmboot', 'boot'):
            try:
                found.add(json.loads(text).get('token') or '')
            except (ValueError, AttributeError):
                pass
        for m in re.finditer(r'-----BEGIN [A-Z ]*PRIVATE KEY-----[^-]+', text):
            found.add(m.group(0)[40:100])
    return {s for s in found if s and len(s) >= 16}


def scan(args):
    values = secrets(args.state) | set(args.extra or [])
    leaked = []
    for p in evidence_dir(args.state).rglob('*'):
        if p.is_file():
            data = p.read_bytes()
            if any(v.encode() in data for v in values):
                leaked.append(p.name)
    if leaked:
        for name in leaked:
            (evidence_dir(args.state) / name).write_text('{"redacted":"secret scan matched; see private state"}\n')
        print('secret scan matched and redacted: ' + ', '.join(sorted(leaked)), file=sys.stderr)
        return EXIT_FAILED
    print('secret scan: %d evidence files clean against %d retained secrets' % (sum(1 for p in evidence_dir(args.state).rglob('*') if p.is_file()), len(values)))
    return 0


def result(args):
    state = pathlib.Path(args.state)
    ev = evidence_dir(state)
    phases = []
    if (ev / 'phases.jsonl').exists():
        phases = [json.loads(l) for l in (ev / 'phases.jsonl').read_text().splitlines() if l.strip()]
    prefix = (state / 'prefix').read_text().strip() if (state / 'prefix').exists() else None
    retained = []
    if prefix:
        code, out, _ = capture(['docker', 'ps', '-a', '--filter', 'name=^' + prefix + '-', '--format', '{{.Names}}\t{{.State}}'], 30)
        retained = [dict(zip(('name', 'state'), l.split('\t'))) for l in out.splitlines() if code == 0 and l.strip()]
    code = args.exit_code
    status = STATUS_BY_EXIT.get(code, 'interrupted' if code in [128 + s for s in INTERRUPT_SIGNALS] else 'failed')
    # Exit 0 is a pass only if run.sh reached its last line.
    if code == EXIT_PASSED:
        if (state / COMPLETED).exists():
            status = 'passed'
        elif (state / PREFLIGHT_COMPLETED).exists():
            status = 'preflight-passed'
        else:
            status, code = 'interrupted', EXIT_FAILED
    failed = next((p for p in phases if p['status'] != 'passed'), None)
    running = state / 'current-phase'
    if failed is None and status != 'passed' and running.exists():
        failed = {'name': running.read_text().strip()}
    lock_record = json.loads((ev / 'lock.json').read_text()) if (ev / 'lock.json').exists() else None
    if failed is None and lock_record and lock_record['status'] != 'acquired':
        failed = {'name': 'runtime-lock'}
    summary = ev / 'summary.json'
    value = {'schemaVersion': 1, 'observedAt': now(), 'status': status, 'exitCode': code, 'prefix': prefix,
             'failedPhase': failed['name'] if failed else None, 'phases': phases,
             'runtimeLock': {k: lock_record.get(k) for k in ('status', 'positionAtQueue', 'waitedSeconds')} if lock_record else None, 'retainedContainers': retained,
             'stateDirectory': str(state), 'evidenceDirectory': str(ev),
             'summary': 'summary.json' if summary.exists() and status == 'passed' else None,
             'custody': 'released and verified' if status == 'passed' else 'Unknown: inspect private state before any cleanup',
             'productionAuthority': False}
    write_json(ev / 'result.json', value)
    print('RESULT %s exit=%s evidence=%s' % (status, code, ev / 'result.json'))
    # run.sh exits with this code, so only a completed run exits 0.
    return code


def main():
    p = argparse.ArgumentParser()
    sub = p.add_subparsers(dest='cmd', required=True)
    s = sub.add_parser('preflight')
    s.add_argument('--state', required=True)
    s.add_argument('--root', default='.')
    s.add_argument('--bootstrap', action='store_true')
    s.add_argument('--skip-tests', action='store_true')
    s.add_argument('--checks', choices=('all', 'host', 'contention'), default='all')
    s.add_argument('--wait', type=int, default=0, help='contention: seconds to wait for a live run without the lock')
    s = sub.add_parser('lock')
    s.add_argument('--state', required=True)
    s.add_argument('--owner', type=int, required=True)
    s.add_argument('--timeout', type=int, default=LOCK_TIMEOUT)
    s.add_argument('--interval', type=float, default=2.0)
    s.add_argument('--status-interval', type=float, default=60.0)
    s = sub.add_parser('phase')
    s.add_argument('--state', required=True)
    s.add_argument('--name', required=True)
    s.add_argument('--timeout', type=int, required=True)
    s.add_argument('--echo', action='store_true')
    s.add_argument('command', nargs=argparse.REMAINDER)
    s = sub.add_parser('wait-stage')
    s.add_argument('--state', required=True)
    s.add_argument('--stage', required=True)
    s.add_argument('--timeout', type=int, default=900)
    s.add_argument('--interval', type=float, default=1.0)
    s = sub.add_parser('journal')
    s.add_argument('--state', required=True)
    s = sub.add_parser('custody-digest')
    s.add_argument('--state', required=True)
    s = sub.add_parser('scan')
    s.add_argument('--state', required=True)
    s.add_argument('--extra', nargs='*')
    s = sub.add_parser('result')
    s.add_argument('--state', required=True)
    s.add_argument('--exit-code', type=int, required=True)
    a = p.parse_args()
    if a.cmd == 'phase':
        if a.command[:1] == ['--']:
            a.command = a.command[1:]
        if not a.command or not re.match(r'^[a-z0-9-]+$', a.name):
            return EXIT_USAGE
    return {'preflight': preflight, 'lock': lock, 'phase': phase, 'wait-stage': wait_stage, 'journal': journal_cmd, 'custody-digest': custody_cmd,
            'scan': scan, 'result': result}[a.cmd](a)


if __name__ == '__main__':
    sys.exit(main())
