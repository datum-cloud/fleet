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
CONTEXT = 'colima-' + PROFILE
EXIT_PASSED, EXIT_FAILED, EXIT_PREFLIGHT, EXIT_BLOCKED, EXIT_TIMEOUT, EXIT_USAGE = 0, 1, 2, 3, 4, 64
STATUS_BY_EXIT = {EXIT_PASSED: 'passed', EXIT_FAILED: 'failed', EXIT_PREFLIGHT: 'preflight-failed',
                  EXIT_BLOCKED: 'blocked', EXIT_TIMEOUT: 'timeout', EXIT_USAGE: 'usage'}
DEBIAN = 'debian:bookworm-slim@sha256:734a72b3d6142a71993b4bc580ca01d8ca687f77a2c422d96a961d3376b179bc'
# Subcommands and errors the live harness depends on. A missing one blocks before any resource exists.
REQUIRED_SOURCE_MARKERS = tuple('"%s"' % c for c in ('seed', 'allocate', 'admit-install', 'install', 'release', 'release-b', 'reuse',
                                                     'asset', 'image', 'boot-service', 'boot-denials', 'site-worker', 'site-observer')) + ('multiplane.ErrLocationNotServed',)
READINESS_PACKAGES = ('./internal/coordination', './internal/multiplane', './internal/localvm', './cmd/fleet-multiplane')
# Redfish mode (run.sh --redfish) adds its executor and probe to the readiness gate.
PROVIDER = os.environ.get('FLEET_SITE_PROVIDER', 'libvirt')
if PROVIDER == 'redfish':
    REQUIRED_SOURCE_MARKERS += ('"redfish-probe"', '"install-b"', 'redfish.Open()')
    READINESS_PACKAGES += ('./internal/redfish',)
# Lab targets behind the Redfish emulator: System Id -> persistent libvirt domain.
LAB_DOMAINS = {'System-1': 'fleet-lab-target-a', 'System-2': 'fleet-lab-target-b'}


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
    write_json(evidence_dir(args.state) / 'preflight.json', {'schemaVersion': 1, 'observedAt': now(), 'profile': PROFILE, 'status': STATUS_BY_EXIT[code], 'checks': checks})
    for c in checks:
        print('%-4s %-34s %s' % ('ok' if c['ok'] else 'FAIL', c['name'], c['detail']))
    return code


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
    with open(log, 'ab') as out, open(state / 'phases.log', 'ab') as markers:
        p = subprocess.Popen(args.command, stdout=subprocess.PIPE, stderr=out, start_new_session=True)
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
    record['durationSeconds'] = round(time.monotonic() - started, 1)
    record['finishedAt'] = now()
    if timed_out:
        record.update(status='timeout', exitCode=EXIT_TIMEOUT)
    else:
        record.update(status='passed' if code == 0 else 'blocked' if code == EXIT_BLOCKED else 'failed', exitCode=code)
    with open(evidence_dir(state) / 'phases.jsonl', 'a') as f:
        f.write(json.dumps(record, sort_keys=True) + '\n')
    if record['status'] != 'passed':
        print('<== %s %s (exit %s); private log: %s' % (args.name, record['status'], record['exitCode'], log), flush=True)
        tail = log.read_bytes()[-1500:].decode(errors='replace')
        print('\n'.join('    ' + l for l in tail.splitlines()[-15:]), flush=True)
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


def journal(state):
    code, out, err = runtime_exec(state, 'cat', '/var/lib/fleet-local/journal.json')
    if code != 0:
        return None
    return json.loads(out)


def domains(state):
    # Running domains only; Redfish lab targets stay defined while powered off.
    code, out, _ = runtime_exec(state, 'virsh', '--connect', LIBVIRT_URI, 'list', '--name')
    return out.split() if code == 0 else None


def domain_name(operation_id):
    return 'fleet-local-' + hashlib.sha256(operation_id.encode()).hexdigest()[:24]


def sanitized_operations(j):
    """Only non-secret fields: no fence, boot capability or receipt."""
    ops = []
    for r in (j or {}).get('operations', {}).values():
        ops.append({'id': r['request']['id'], 'kind': r['request']['kind'], 'epoch': r['request']['epoch'],
                    'stage': r.get('stage'), 'phase': r.get('result', {}).get('phase'), 'bootClosed': r.get('bootClosed', False),
                    'domain': LAB_DOMAINS.get(r.get('target', {}).get('systemID'), '?') if (j or {}).get('provider') == 'redfish' else domain_name(r['request']['id'])})
    return ops


def wait_stage(args):
    """Wait until exactly one install is in args.stage with its domain running."""
    deadline = time.monotonic() + args.timeout
    last = None
    while time.monotonic() < deadline:
        ops = sanitized_operations(journal(args.state))
        installs = [o for o in ops if o['kind'] == 'install']
        running = domains(args.state) or []
        last = {'installs': installs, 'runningDomains': len(running)}
        if len(installs) > 1:
            print(json.dumps({'error': 'more than one install operation', **last}))
            return EXIT_FAILED
        if installs and installs[0]['stage'] in ('installer-verified', 'disk-armed', 'disk-requested', 'disk-started', 'disk-verified', 'done'):
            print(json.dumps({'error': 'installer completed before the interrupt window', **last}))
            return EXIT_FAILED
        if installs and installs[0]['stage'] == args.stage and installs[0]['domain'] in running:
            print(json.dumps({'observedAt': now(), 'operation': installs[0], 'domainRunning': True}))
            return 0
        time.sleep(args.interval)
    print(json.dumps({'error': 'stage not observed before deadline', **(last or {})}))
    return EXIT_TIMEOUT


def journal_cmd(args):
    print(json.dumps(sanitized_operations(journal(args.state)), sort_keys=True))
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
        if p.name == 'journal.json':
            try:
                j = json.loads(text)
            except ValueError:
                continue
            if j.get('fence'):
                found.add(j['fence'])
            for r in j.get('operations', {}).values():
                for value in (r.get('bootToken'), r.get('request', {}).get('fenceToken')):
                    if value:
                        found.add(value)
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
    failed = next((p for p in phases if p['status'] != 'passed'), None)
    status = STATUS_BY_EXIT.get(args.exit_code, 'failed')
    summary = ev / 'summary.json'
    value = {'schemaVersion': 1, 'observedAt': now(), 'status': status, 'exitCode': args.exit_code, 'prefix': prefix,
             'failedPhase': failed['name'] if failed else None, 'phases': phases, 'retainedContainers': retained,
             'stateDirectory': str(state), 'evidenceDirectory': str(ev),
             'summary': 'summary.json' if summary.exists() and status == 'passed' else None,
             'custody': 'released and verified' if status == 'passed' else 'Unknown: inspect private state before any cleanup',
             'productionAuthority': False}
    write_json(ev / 'result.json', value)
    print('RESULT %s exit=%s evidence=%s' % (status, args.exit_code, ev / 'result.json'))
    return 0


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
    return {'preflight': preflight, 'phase': phase, 'wait-stage': wait_stage, 'journal': journal_cmd,
            'scan': scan, 'result': result}[a.cmd](a)


if __name__ == '__main__':
    sys.exit(main())
