#!/usr/bin/env python3
"""Shared local acceptance helpers: preflight, bounded phases, sanitized results.

Exit codes are part of the harness contract (see docs/operations/multiplane.md):
0 passed, 1 failed, 2 preflight failed, 3 blocked by implementation readiness,
4 bounded wait expired, 64 usage. Nothing here deletes resources.
"""
import argparse
import datetime
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
# Run parameters (docs/operations/multiplane.md#ci-mode). The defaults are the
# task-owned Colima profile: an arm64 runtime running the amd64 guest under
# TCG. CI mode uses a disposable GitHub-hosted runner's own Docker daemon.
RUNTIME = os.environ.get('FLEET_MULTIPLANE_RUNTIME', 'colima')
CONTEXT = os.environ.get('FLEET_MULTIPLANE_DOCKER_CONTEXT', 'colima-' + PROFILE)
ARCH = os.environ.get('FLEET_MULTIPLANE_ARCH', 'arm64')
ACCEL = os.environ.get('FLEET_MULTIPLANE_ACCEL', 'tcg')
DOCKER_ARCHITECTURES = {'arm64': ('aarch64', 'arm64'), 'amd64': ('x86_64', 'amd64')}
CI_ENDPOINT = 'unix:///var/run/docker.sock'
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

def preflight(args):
    checks = []

    def check(name, ok, detail='', kind='preflight'):
        checks.append({'name': name, 'ok': bool(ok), 'detail': detail, 'kind': kind})
        return ok

    for tool in ('go', 'python3', 'openssl', 'docker', 'kind', 'kubectl', 'kustomize', 'curl', 'git'):
        check('tool:' + tool, shutil.which(tool), 'required on PATH')
    check('python>=3.12', sys.version_info >= (3, 12), sys.version.split()[0] + ' (evidence extraction uses the tarfile data filter)')
    check('runtime-mode', RUNTIME in ('colima', 'ci'), 'FLEET_MULTIPLANE_RUNTIME=%s; colima or ci' % RUNTIME)
    if RUNTIME == 'ci':
        # The run adjusts host kernel settings and fills the Docker data root:
        # only a disposable runner, never a workstation's daemon.
        check('ci-runner', os.environ.get('GITHUB_ACTIONS') == 'true' and os.environ.get('RUNNER_ENVIRONMENT') == 'github-hosted',
              'CI mode runs only on a disposable GitHub-hosted runner (GITHUB_ACTIONS=true, RUNNER_ENVIRONMENT=github-hosted)')
    check('runtime-parameter:architecture', ARCH in DOCKER_ARCHITECTURES, 'FLEET_MULTIPLANE_ARCH=%s; arm64 or amd64' % ARCH)
    # KVM runs the amd64 guest natively, so it needs an amd64 runtime. The
    # Redfish lab adapter defines TCG domains only.
    accel_ok = ACCEL == 'tcg' or (ACCEL == 'kvm' and ARCH == 'amd64' and PROVIDER == 'libvirt')
    check('accelerator', accel_ok, 'FLEET_MULTIPLANE_ACCEL=%s; tcg, or kvm with an amd64 runtime and the libvirt provider' % ACCEL)

    code, out, _ = capture(['docker', 'context', 'inspect', CONTEXT, '--format', '{{.Endpoints.docker.Host}}'], 20)
    endpoint_ok = endpoint_matches(code, out)
    if not endpoint_ok and RUNTIME == 'colima' and args.bootstrap and shutil.which('colima'):
        # Only start an existing task-owned profile with its saved configuration.
        code, out, err = capture(['colima', 'list', '--json'], 30, cwd='/')
        known = any(json.loads(line).get('name') == PROFILE for line in out.splitlines() if line.strip().startswith('{')) if code == 0 else False
        if known:
            code, _, err = capture(['colima', 'start', '--profile', PROFILE], 600, cwd='/')
            check('bootstrap:colima-start', code == 0, 'started existing profile ' + PROFILE if code == 0 else err.strip()[-300:])
            code, out, _ = capture(['docker', 'context', 'inspect', CONTEXT, '--format', '{{.Endpoints.docker.Host}}'], 20)
            endpoint_ok = endpoint_matches(code, out)
    check('docker-context', endpoint_ok, CONTEXT + (' must point at the runner daemon ' + CI_ENDPOINT if RUNTIME == 'ci' else ' must point at the task-owned Colima profile socket; never fleet-e2e-x86'))

    info = {}
    if endpoint_ok:
        code, out, err = capture(['docker', 'info', '--format', '{{json .}}'], 30)
        if check('docker-reachable', code == 0, ('start the profile: colima start --profile %s (or run with --bootstrap)' % PROFILE if RUNTIME == 'colima' else 'the runner daemon is unreachable') if code else 'reachable'):
            info = json.loads(out)
    if info:
        check('runtime-architecture', info.get('Architecture') in DOCKER_ARCHITECTURES.get(ARCH, ()), '%s; FLEET_MULTIPLANE_ARCH=%s' % (info.get('Architecture'), ARCH))
        check('runtime-memory', int(info.get('MemTotal', 0)) >= 7_800_000_000, '%.2f GiB; 8 GiB required' % (int(info.get('MemTotal', 0)) / 2**30))
        check('runtime-cpus', int(info.get('NCPU', 0)) >= 4, '%s CPUs; 4 required' % info.get('NCPU'))
        code, out, _ = capture(['docker', 'ps', '--format', '{{.Names}}'], 30)
        active = sorted(n for n in out.split() if re.match(r'^fleet-(local|mp)-', n))
        check('no-concurrent-fleet-runtime', code == 0 and not active, ('running: ' + ', '.join(active[:6])) if active else 'none running; stopped diagnostics are left untouched')
        code, out, err = capture(['docker', 'run', '--rm', '--platform', 'linux/amd64', '--entrypoint', 'uname', DEBIAN, '-m'], 300)
        check('amd64-emulation', code == 0 and out.strip() == 'x86_64', 'installer image build needs amd64 container execution' + ('' if code == 0 else ': ' + err.strip()[-200:]))
        free = vm_free_bytes()
        check('runtime-disk', free is not None and free >= 12 * 2**30, ('%.1f GiB free in docker data root; 12 GiB required' % (free / 2**30)) if free is not None else 'unable to read docker data-root free space')
        limit = vm_inotify()
        opted = os.environ.get('FLEET_MULTIPLANE_INOTIFY') == '1024'
        check('inotify-instances', limit is not None and (limit >= 1024 or opted), 'current %s; requires >=1024 or FLEET_MULTIPLANE_INOTIFY=1024 (reversible task-VM adjustment)' % limit)
        if ACCEL == 'kvm':
            check('kvm-device', vm_exec('test -c /dev/kvm && test -r /dev/kvm && test -w /dev/kvm && echo present') == 'present',
                  'a readable and writable /dev/kvm on the Docker host; no automatic software fallback')

    root = pathlib.Path(args.root)
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

    pre = [c for c in checks if c['kind'] == 'preflight' and not c['ok']]
    ready = [c for c in checks if c['kind'] == 'readiness' and not c['ok']]
    code = EXIT_PREFLIGHT if pre else EXIT_BLOCKED if ready else EXIT_PASSED
    write_json(evidence_dir(args.state) / 'preflight.json', {'schemaVersion': 1, 'observedAt': now(), 'profile': PROFILE, 'status': STATUS_BY_EXIT[code], 'checks': checks,
                                                           'runtime': {'mode': RUNTIME, 'dockerContext': CONTEXT, 'architecture': ARCH, 'accelerator': ACCEL}})
    for c in checks:
        print('%-4s %-34s %s' % ('ok' if c['ok'] else 'FAIL', c['name'], c['detail']))
    return code


def endpoint_matches(code, out):
    """Whether the context reaches exactly the selected runtime's daemon."""
    if code != 0:
        return False
    if RUNTIME == 'ci':
        return out.strip() == CI_ENDPOINT
    return RUNTIME == 'colima' and out.strip().endswith('/.colima/%s/docker.sock' % PROFILE)


def vm_exec(command, timeout=30):
    """A read-only command on the Docker host: the Colima VM, or the runner itself in CI mode."""
    if RUNTIME == 'ci':
        code, out, _ = capture(['sh', '-c', command], timeout, cwd='/')
        return out.strip() if code == 0 else None
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
        # A copied rendered config medium carries its URL capability.
        if p.suffix == '.json' and p.parent.name == 'mediacfg':
            try:
                found.add(json.loads(text).get('capability') or '')
            except (ValueError, AttributeError):
                pass
        for m in re.finditer(r'-----BEGIN [A-Z ]*PRIVATE KEY-----[^-]+', text):
            found.add(m.group(0)[40:100])
    return {s for s in found if s and len(s) >= 16}


def scan(args):
    values = secrets(args.state) | set(args.extra or [])
    leaked = []
    ev = evidence_dir(args.state)
    for p in ev.rglob('*'):
        if p.is_file():
            data = p.read_bytes()
            if any(v.encode() in data for v in values):
                leaked.append(p)
    if leaked:
        # Redact the matching file itself, wherever it is under evidence/.
        for path in leaked:
            path.write_text('{"redacted":"secret scan matched; see private state"}\n')
        print('secret scan matched and redacted: ' + ', '.join(sorted(str(p.relative_to(ev)) for p in leaked)), file=sys.stderr)
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
    summary = ev / 'summary.json'
    value = {'schemaVersion': 1, 'observedAt': now(), 'status': status, 'exitCode': code, 'prefix': prefix,
             'failedPhase': failed['name'] if failed else None, 'phases': phases, 'retainedContainers': retained,
             'stateDirectory': str(state), 'evidenceDirectory': str(ev),
             'summary': 'summary.json' if summary.exists() and status == 'passed' else None,
             'custody': 'released and verified' if status == 'passed' else 'Unknown: inspect private state before any cleanup',
             'productionAuthority': False}
    write_json(ev / 'result.json', value)
    print('RESULT %s exit=%s evidence=%s' % (status, code, ev / 'result.json'))
    # run.sh exits with this code, so only a completed run exits 0.
    return code


def verify(args):
    """Whether a recorded result counts as a pass: status passed, every phase
    passed, and the run reached teardown. A killed, timed-out or interrupted
    run never does, whatever its exit code."""
    path = evidence_dir(args.state) / 'result.json'
    try:
        value = json.loads(path.read_text())
    except (OSError, ValueError):
        print('VERIFY failed: no readable result.json')
        return EXIT_FAILED
    phases = value.get('phases') or []
    problems = []
    if value.get('status') != 'passed' or value.get('exitCode') != EXIT_PASSED:
        problems.append('status %s exit %s' % (value.get('status'), value.get('exitCode')))
    problems += ['phase %s %s' % (p.get('name'), p.get('status')) for p in phases if p.get('status') != 'passed']
    if not any(p.get('name') == 'teardown' and p.get('status') == 'passed' for p in phases):
        problems.append('teardown did not pass')
    if value.get('retainedContainers'):
        problems.append('retained containers: %d' % len(value['retainedContainers']))
    print('VERIFY %s phases=%d%s' % ('failed' if problems else 'passed', len(phases), (': ' + '; '.join(problems[:8])) if problems else ''))
    return EXIT_FAILED if problems else EXIT_PASSED


def main():
    p = argparse.ArgumentParser()
    sub = p.add_subparsers(dest='cmd', required=True)
    s = sub.add_parser('preflight')
    s.add_argument('--state', required=True)
    s.add_argument('--root', default='.')
    s.add_argument('--bootstrap', action='store_true')
    s.add_argument('--skip-tests', action='store_true')
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
    s = sub.add_parser('verify')
    s.add_argument('--state', required=True)
    a = p.parse_args()
    if a.cmd == 'phase':
        if a.command[:1] == ['--']:
            a.command = a.command[1:]
        if not a.command or not re.match(r'^[a-z0-9-]+$', a.name):
            return EXIT_USAGE
    return {'preflight': preflight, 'phase': phase, 'wait-stage': wait_stage, 'journal': journal_cmd, 'custody-digest': custody_cmd,
            'scan': scan, 'result': result, 'verify': verify}[a.cmd](a)


if __name__ == '__main__':
    sys.exit(main())
