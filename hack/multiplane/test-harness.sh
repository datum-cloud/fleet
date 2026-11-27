#!/bin/sh
# Offline tests for the multi-plane acceptance harness. Uses fake docker, colima
# and go executables; never contacts a container runtime or cluster.
set -eu
root=$(CDPATH='' cd -- "$(dirname "$0")/../.." && pwd)
for f in "$root"/hack/multiplane/*.sh; do bash -n "$f"; done
python3 -m py_compile "$root"/hack/multiplane/*.py
python3 "$root"/hack/multiplane/test-k8s-evidence.py "$root"
python3 "$root"/hack/multiplane/test-redfish.py "$root"
python3 "$root"/hack/multiplane/test-interrupt.py "$root"
python3 "$root"/hack/multiplane/test-kind-create.py "$root"
python3 -m py_compile "$root"/hack/redfish-lab/*.py
exec python3 - "$root" <<'PY'
import json, os, pathlib, re, stat, subprocess, sys, tempfile, time

root = pathlib.Path(sys.argv[1])
harness = root / 'hack/multiplane/harness.py'
failures = []

def check(name, condition, detail=''):
    if not condition:
        failures.append(name + (': ' + detail if detail else ''))

FAKE = r'''#!/usr/bin/env python3
import json, os, sys
tool = os.path.basename(sys.argv[0]); a = sys.argv[1:]
scenario = os.environ.get('FAKE', '')
with open(os.environ['FAKE_TRACE'], 'a') as t: t.write(json.dumps([tool] + a) + '\n')
if tool == 'docker':
    with open(os.environ['FAKE_TRACE'] + '.context', 'a') as t: t.write(os.environ.get('DOCKER_CONTEXT', '') + '\n')
    if a[:2] == ['context', 'inspect']:
        if scenario == 'no-context': sys.exit(1)
        print(os.environ.get('FAKE_ENDPOINT', 'unix:///Users/x/.colima/fleet-acceptance-tcg/docker.sock')); sys.exit(0)
    if a[:1] == ['info']:
        print(json.dumps({'Architecture': os.environ.get('FAKE_ARCH', 'aarch64'), 'MemTotal': 8308948992, 'NCPU': 4})); sys.exit(0)
    if a[:1] == ['ps']:
        if '-a' in a: print('fleet-mp-0000aaaa-vm\texited'); sys.exit(0)
        print('fleet-mp-1234abcd-vm' if scenario == 'busy' else 'unrelated'); sys.exit(0)
    if a[:1] == ['run']:
        print('x86_64'); sys.exit(0)
if tool == 'kubectl' and 'exec' in a and 'deploy/fleet-site-runtime' in a:
    if a[-1].endswith('/state.json'):
        stage = os.environ.get('FAKE_STAGE', 'installer-start')
        steps = {'prepared': ['prepare-disk'], 'installer-start': ['prepare-disk', 'installer-network', 'installer-start'],
                 'disk-start': ['prepare-disk', 'installer-network', 'installer-start', 'installer-verified', 'disk-network-off', 'disk-start']}[stage]
        print(json.dumps({'device': 'fleet-provider/synthetic-device-central-01', 'fence': 3, 'stopped': 3,
              'journal': [{'operation': 'install-op-1', 'fence': 3, 'step': s, 'state': 'Done', 'at': '2026-01-01T00:00:00Z', 'payload': {'note': 'R' * 64}} for s in steps]})); sys.exit(0)
    if 'virsh' in a:
        import hashlib
        print('fleet-local-' + hashlib.sha256(b'install-op-1').hexdigest()[:16]); sys.exit(0)
# CI mode runs the same read-only host probes on the runner itself (sh -c).
if tool in ('colima', 'sh'):
    if 'kvm' in a[-1]:
        if os.environ.get('FAKE_KVM') == 'absent': sys.exit(1)
        print('present')
    elif 'df' in a[-1]: print('/dev/vdb1 41000000 20000000 %d 50%% /var/lib/docker' % (int(os.environ.get('FAKE_FREE_KB', '20000000'))))
    elif 'inotify' in a[-1]: print(os.environ.get('FAKE_INOTIFY', '1024'))
    sys.exit(0)
sys.exit(0)
'''

with tempfile.TemporaryDirectory() as tmp:
    tmp = pathlib.Path(tmp)
    bin_dir = tmp / 'bin'; bin_dir.mkdir()
    for tool in ('docker', 'colima', 'go', 'kind', 'kubectl', 'helm', 'openssl'):
        p = bin_dir / tool; p.write_text(FAKE.replace('#!/usr/bin/env python3', '#!' + sys.executable)); p.chmod(0o755)
    trace = tmp / 'trace'
    base = dict(os.environ, PATH=str(bin_dir) + os.pathsep + os.environ['PATH'], FAKE_TRACE=str(trace))
    for name in ('FLEET_MULTIPLANE_INOTIFY', 'FLEET_MULTIPLANE_RUNTIME', 'FLEET_MULTIPLANE_DOCKER_CONTEXT', 'FLEET_MULTIPLANE_ARCH',
                 'FLEET_MULTIPLANE_ACCEL', 'FLEET_SITE_PROVIDER', 'GITHUB_ACTIONS', 'RUNNER_ENVIRONMENT', 'DOCKER_CONTEXT'):
        base.pop(name, None)
    # CI-mode host probes go through a fake sh, on PATH only for those runs.
    ci_bin = tmp / 'ci-bin'; ci_bin.mkdir()
    (ci_bin / 'sh').write_text(FAKE.replace('#!/usr/bin/env python3', '#!' + sys.executable)); (ci_bin / 'sh').chmod(0o755)
    ci = {'PATH': str(ci_bin) + os.pathsep + base['PATH'], 'FLEET_MULTIPLANE_RUNTIME': 'ci', 'FLEET_MULTIPLANE_DOCKER_CONTEXT': 'default',
          'FLEET_MULTIPLANE_ARCH': 'amd64', 'FLEET_MULTIPLANE_ACCEL': 'kvm', 'GITHUB_ACTIONS': 'true', 'RUNNER_ENVIRONMENT': 'github-hosted',
          'FAKE_ENDPOINT': 'unix:///var/run/docker.sock', 'FAKE_ARCH': 'x86_64'}

    def run(args, env=None, **kw):
        return subprocess.run([sys.executable, str(harness), *args], text=True, capture_output=True, env=dict(base, **(env or {})), **kw)

    def state():
        d = pathlib.Path(tempfile.mkdtemp(dir=tmp)); d.chmod(0o700); return d

    good_root = tmp / 'good'; (good_root / 'cmd/fleet-multiplane').mkdir(parents=True)
    markers = ['seed', 'allocate', 'admit-install', 'install', 'release', 'release-b', 'reuse', 'coordinator', 'image', 'boot-service', 'boot-denials', 'site-worker', 'site-observer',
               'admit-install-b', 'recovery-mismatch', 'probe-unallocatable', 'tenant-isolation']
    (good_root / 'cmd/fleet-multiplane/main.go').write_text(' '.join('"%s"' % m for m in markers) + ' multiplane.ErrLocationNotServed "redfish-probe" redfish.NewPinnedClient')
    bad_root = tmp / 'bad'; (bad_root / 'cmd/fleet-multiplane').mkdir(parents=True)
    (bad_root / 'cmd/fleet-multiplane/main.go').write_text(' '.join('"%s"' % m for m in markers))

    # Preflight: pass, prerequisite failures (exit 2) and readiness block (exit 3).
    for name, env, rootdir, expected in (
            ('pass', {}, good_root, 0),
            ('no-context', {'FAKE': 'no-context'}, good_root, 2),
            ('concurrent-runtime', {'FAKE': 'busy'}, good_root, 2),
            ('low-disk', {'FAKE_FREE_KB': '1000'}, good_root, 2),
            ('inotify-without-opt-in', {'FAKE_INOTIFY': '128'}, good_root, 2),
            ('missing-location-denial', {}, bad_root, 3)):
        s = state()
        r = run(['preflight', '--state', str(s), '--root', str(rootdir), '--skip-tests'], env)
        check('preflight ' + name, r.returncode == expected, 'exit %s: %s' % (r.returncode, r.stdout[-400:]))
        pre = json.loads((s / 'evidence/preflight.json').read_text())
        check('preflight record ' + name, pre['status'] == {0: 'passed', 2: 'preflight-failed', 3: 'blocked'}[expected])
    s = state()
    r = run(['preflight', '--state', str(s), '--root', str(good_root), '--skip-tests'], {'FAKE_INOTIFY': '128', 'FLEET_MULTIPLANE_INOTIFY': '1024'})
    check('preflight inotify opt-in', r.returncode == 0, r.stdout[-300:])
    calls = [json.loads(l) for l in trace.read_text().splitlines()]
    check('preflight never mutates runtime', not any(c[0] == 'docker' and c[1] in ('rm', 'stop', 'kill', 'network', 'volume', 'system') for c in calls))
    check('preflight never starts colima without --bootstrap', not any(c[0] == 'colima' and 'start' in c for c in calls))
    check('default runtime recorded', pre['runtime'] == {'mode': 'colima', 'dockerContext': 'colima-fleet-acceptance-tcg', 'architecture': 'arm64', 'accelerator': 'tcg'}, str(pre.get('runtime')))

    # Run parameters: CI mode replaces only the Colima-specific probes; every
    # combination it cannot honor fails closed before any resource exists.
    for name, env, expected in (
            ('ci kvm pass', dict(ci), 0),
            ('ci tcg pass', dict(ci, FLEET_MULTIPLANE_ACCEL='tcg'), 0),
            ('ci redfish tcg pass', dict(ci, FLEET_MULTIPLANE_ACCEL='tcg', FLEET_SITE_PROVIDER='redfish'), 0),
            ('ci outside GitHub Actions', dict(ci, GITHUB_ACTIONS=''), 2),
            ('ci on a self-hosted runner', dict(ci, RUNNER_ENVIRONMENT='self-hosted'), 2),
            ('ci against a Colima socket', dict(ci, FAKE_ENDPOINT='unix:///Users/x/.colima/fleet-acceptance-tcg/docker.sock'), 2),
            ('colima against the runner socket', {'FAKE_ENDPOINT': 'unix:///var/run/docker.sock'}, 2),
            ('ci architecture mismatch', dict(ci, FAKE_ARCH='aarch64'), 2),
            ('ci unknown architecture', dict(ci, FLEET_MULTIPLANE_ARCH='riscv64', FLEET_MULTIPLANE_ACCEL='tcg'), 2),
            ('ci without /dev/kvm', dict(ci, FAKE_KVM='absent'), 2),
            ('ci low disk', dict(ci, FAKE_FREE_KB='1000'), 2),
            ('ci concurrent runtime', dict(ci, FAKE='busy'), 2),
            ('ci redfish kvm pass', dict(ci, FLEET_SITE_PROVIDER='redfish'), 0),
            ('redfish kvm without /dev/kvm', dict(ci, FLEET_SITE_PROVIDER='redfish', FAKE_KVM='absent'), 2),
            ('kvm on an arm64 runtime', {'FLEET_MULTIPLANE_ACCEL': 'kvm'}, 2),
            ('unknown accelerator', {'FLEET_MULTIPLANE_ACCEL': 'auto'}, 2),
            ('unknown runtime', {'FLEET_MULTIPLANE_RUNTIME': 'lima'}, 2)):
        s = state(); trace.write_text(''); pathlib.Path(str(trace) + '.context').write_text('')
        r = run(['preflight', '--state', str(s), '--root', str(good_root), '--skip-tests'], env)
        check('preflight ' + name, r.returncode == expected, 'exit %s: %s' % (r.returncode, r.stdout[-600:]))
        if name == 'ci kvm pass':
            pre = json.loads((s / 'evidence/preflight.json').read_text())
            check('ci runtime recorded', pre['runtime'] == {'mode': 'ci', 'dockerContext': 'default', 'architecture': 'amd64', 'accelerator': 'kvm'}, str(pre['runtime']))
            check('ci checks kvm and keeps the safety checks', {'kvm-device', 'no-concurrent-fleet-runtime', 'runtime-disk', 'runtime-memory', 'runtime-cpus', 'inotify-instances', 'ci-runner', 'amd64-emulation'}
                  <= {c['name'] for c in pre['checks'] if c['ok']}, str(pre['checks']))
            calls = [json.loads(l) for l in trace.read_text().splitlines()]
            check('ci mode never touches colima', not any(c[0] == 'colima' for c in calls), str(calls))
            check('ci mode uses only the selected context', set(pathlib.Path(str(trace) + '.context').read_text().split()) == {'default'})

    # run.sh exports the defaults to every step, and passes overrides through.
    for name, env, want in (
            ('run.sh defaults', {}, {'mode': 'colima', 'dockerContext': 'colima-fleet-acceptance-tcg', 'architecture': 'arm64', 'accelerator': 'tcg'}),
            ('run.sh ci parameters', ci, {'mode': 'ci', 'dockerContext': 'default', 'architecture': 'amd64', 'accelerator': 'kvm'})):
        runs = pathlib.Path(tempfile.mkdtemp(dir=tmp))
        r = subprocess.run(['bash', str(root / 'hack/multiplane/run.sh'), '--preflight-only', '--skip-readiness-tests'], text=True, capture_output=True,
                           env=dict(base, FLEET_MULTIPLANE_STATE_ROOT=str(runs), **env))
        states = list(runs.iterdir())
        pre = json.loads((states[0] / 'evidence/preflight.json').read_text()) if len(states) == 1 else {}
        check(name, r.returncode == 0 and pre.get('runtime') == want, 'exit %s %s: %s' % (r.returncode, pre.get('runtime'), (r.stdout + r.stderr)[-600:]))

    # Bounded phases: pass, fail, blocked passthrough, timeout kills the process group.
    s = state()
    check('phase pass', run(['phase', '--state', str(s), '--name', 'ok', '--timeout', '30', '--', 'sh', '-c', 'echo marker-ok']).returncode == 0)
    check('phase fail', run(['phase', '--state', str(s), '--name', 'bad', '--timeout', '30', '--', 'sh', '-c', 'echo secret-in-stderr >&2; exit 7']).returncode == 1)
    check('phase blocked', run(['phase', '--state', str(s), '--name', 'blocked', '--timeout', '30', '--', 'sh', '-c', 'exit 3']).returncode == 3)
    started = time.monotonic()
    pidfile = s / 'child.pid'
    r = run(['phase', '--state', str(s), '--name', 'slow', '--timeout', '2', '--', 'sh', '-c', 'sleep 300 & echo $! > "$0"; wait', str(pidfile)])
    check('phase timeout code', r.returncode == 4, str(r.returncode))
    check('phase timeout bounded', time.monotonic() - started < 30)
    time.sleep(0.5)
    child = int(pidfile.read_text())
    try:
        os.kill(child, 0); alive = True
    except ProcessLookupError:
        alive = False
    check('phase timeout kills process group', not alive)
    records = [json.loads(l) for l in (s / 'evidence/phases.jsonl').read_text().splitlines()]
    check('phase records', [(x['name'], x['status']) for x in records] == [('ok', 'passed'), ('bad', 'failed'), ('blocked', 'blocked'), ('slow', 'timeout')], str(records))
    check('stdout markers retained', 'marker-ok' in (s / 'phases.log').read_text())
    check('stderr kept private', 'secret-in-stderr' not in (s / 'phases.log').read_text() and 'secret-in-stderr' not in (s / 'evidence/phases.jsonl').read_text())
    check('phase name validated', run(['phase', '--state', str(s), '--name', '../x', '--timeout', '1', '--', 'true']).returncode == 64)

    # Partial output and EOF must not bypass the phase deadline.
    for name, script in (
        ('partial', 'import sys,time; sys.stdout.write("partial"); sys.stdout.flush(); time.sleep(60)'),
        ('closed', 'import os,time; os.close(1); time.sleep(60)'),
    ):
        started = time.monotonic()
        r = run(['phase', '--state', str(state()), '--name', name, '--timeout', '1', '--', sys.executable, '-c', script], timeout=10)
        check(name + ' timeout', r.returncode == 4, r.stdout + r.stderr)
        check(name + ' bounded', time.monotonic() - started < 8)

    # Provider observation reads the site store's journal, never step payloads.
    obs = state()
    r = run(['wait-stage', '--state', str(obs), '--stage', 'installer-start', '--timeout', '5'])
    check('wait-stage observes running installer', r.returncode == 0, r.stdout)
    seen = json.loads(r.stdout) if r.returncode == 0 else {}
    check('wait-stage names the operation and its fence', seen.get('operation', {}).get('operation') == 'install-op-1' and seen['operation'].get('fence') == 3
          and seen['operation'].get('kind') == 'DeviceInstallation', r.stdout)
    check('wait-stage sanitized', 'R' * 64 not in r.stdout, r.stdout)
    r = run(['wait-stage', '--state', str(obs), '--stage', 'installer-start', '--timeout', '5'], {'FAKE_STAGE': 'disk-start'})
    check('wait-stage rejects missed window', r.returncode == 1, r.stdout)
    r = run(['wait-stage', '--state', str(obs), '--stage', 'installer-start', '--timeout', '2'], {'FAKE_STAGE': 'prepared'})
    check('wait-stage bounded', r.returncode == 4, r.stdout)
    out = run(['journal', '--state', str(obs)]).stdout
    check('journal sanitized', 'R' * 64 not in out and 'install-op-1' in out, out)
    calls = [json.loads(l) for l in trace.read_text().splitlines()]
    check('provider observation only through the cell API', not any(c[0] == 'docker' and 'exec' in c for c in calls)
          and any(c[0] == 'kubectl' and 'exec' in c and 'deploy/fleet-site-runtime' in c for c in calls))

    # Secret scan: every retained credential form is detected and redacted.
    s = state(); (s / 'evidence').mkdir()
    (s / 'provider').mkdir(); (s / 'provider/tokens.csv').write_text('a' * 64 + ',bootstrap,bootstrap,system:masters\n')
    (s / 'hub.json').write_text(json.dumps({'users': [{'user': {'token': 'k' * 40}}]}))
    boot = s / 'provider-private/site/devices/0123/vmboot'; boot.mkdir(parents=True)
    (boot / 'k.json').write_text(json.dumps({'operation': 'x', 'fence': 1, 'receipt': 'r' * 64, 'token': 'b' * 64}))
    (s / 'host.yaml').write_text('users:\n- user:\n    client-key-data: ' + 'K' * 48 + '\n')
    (s / 'evidence/clean.json').write_text('{"ok":true}')
    check('scan clean', run(['scan', '--state', str(s)]).returncode == 0)
    (s / 'worker.token').write_text('w' * 40 + '\n')
    for secret in ('a' * 64, 'k' * 40, 'b' * 64, 'K' * 48, 'w' * 40):
        (s / 'evidence/leak.json').write_text('{"v":"%s"}' % secret)
        check('scan detects ' + secret[0], run(['scan', '--state', str(s)]).returncode == 1)
        check('scan redacts ' + secret[0], secret not in (s / 'evidence/leak.json').read_text())
    (s / 'evidence/leak.json').unlink()
    (s / 'evidence/nested').mkdir()
    (s / 'evidence/nested/deep.json').write_text('{"v":"%s"}' % ('w' * 40))
    check('scan detects a nested leak', run(['scan', '--state', str(s)]).returncode == 1)
    check('scan redacts the nested file itself', 'w' * 40 not in (s / 'evidence/nested/deep.json').read_text())
    check('scan is clean after redaction', run(['scan', '--state', str(s)]).returncode == 0)

    # Result: status mapping and retained owned containers. Exit 0 is passed
    # only with the completion marker run.sh writes last.
    s = state(); (s / 'prefix').write_text('fleet-mp-0000aaaa\n')
    (s / 'completed').write_text('')
    for code, status in ((0, 'passed'), (1, 'failed'), (2, 'preflight-failed'), (3, 'blocked'), (4, 'timeout'), (143, 'interrupted'), (130, 'interrupted')):
        run(['result', '--state', str(s), '--exit-code', str(code)])
        res = json.loads((s / 'evidence/result.json').read_text())
        check('result ' + status, res['status'] == status and res['productionAuthority'] is False)
    check('result lists retained containers', res['retainedContainers'] == [{'name': 'fleet-mp-0000aaaa-vm', 'state': 'exited'}], str(res['retainedContainers']))
    check('result keeps unknown custody', 'Unknown' in res['custody'])

    # Verify: a pass needs status passed, every phase passed, teardown passed
    # and no retained container, whatever the exit code said.
    def verified(result):
        s = state(); (s / 'evidence').mkdir(); (s / 'evidence/result.json').write_text(json.dumps(result))
        return run(['verify', '--state', str(s)]).returncode
    good = {'status': 'passed', 'exitCode': 0, 'retainedContainers': [], 'phases': [{'name': 'build', 'status': 'passed'}, {'name': 'teardown', 'status': 'passed'}]}
    check('verify passes a completed run', verified(good) == 0)
    for name, change in (('interrupted', {'status': 'interrupted', 'exitCode': 1}), ('timeout', {'status': 'timeout', 'exitCode': 4}),
                         ('a failed phase', {'phases': [{'name': 'build', 'status': 'timeout'}, {'name': 'teardown', 'status': 'passed'}]}),
                         ('no teardown', {'phases': [{'name': 'build', 'status': 'passed'}, {'name': 'stop', 'status': 'passed'}]}),
                         ('retained containers', {'retainedContainers': [{'name': 'fleet-mp-0000aaaa-vm', 'state': 'exited'}]})):
        check('verify refuses ' + name, verified(dict(good, **change)) == 1)
    s = state()
    check('verify refuses a missing result', run(['verify', '--state', str(s)]).returncode == 1)

    # The installer hold: the watcher suspends a guest whose kernel printed and
    # reports a guest that printed its receipt first as missed, never held.
    steps_text = (root / 'hack/multiplane/steps.sh').read_text()
    watcher = steps_text.split("<<'SH'\n", 1)[1].split('\nSH\n', 1)[0]
    for name, console, want in (('held', 'Linux version 6.1\n', 'held '), ('missed', 'Linux version 6.1\nFLEET-INSTALLED:' + 'r' * 64 + '\n', 'missed ')):
        h = pathlib.Path(tempfile.mkdtemp(dir=tmp))
        (h / 'vm/dev/0123456789abcdef').mkdir(parents=True); (h / 'vm/dev/0123456789abcdef/installer.log').write_text(console)
        (h / 'bin').mkdir()
        (h / 'bin/virsh').write_text('#!/bin/sh\nfor a; do last=$a; done\ncase "$3" in list) echo fleet-local-0123456789abcdef ;; suspend) echo "$4" > "%s/suspended" ;; domstate) [ -e "%s/suspended" ] && echo paused || echo running ;; esac\n' % (h, h))
        (h / 'bin/virsh').chmod(0o755)
        script = watcher.replace('/var/lib/fleet-local/vm', str(h / 'vm')).replace('/tmp/fleet-installer-hold', str(h / 'hold'))
        r = subprocess.run(['sh', '-c', script], text=True, capture_output=True, env=dict(os.environ, PATH=str(h / 'bin') + os.pathsep + os.environ['PATH']), timeout=30)
        result = (h / 'hold').read_text() if (h / 'hold').exists() else ''
        check('installer hold ' + name, r.returncode == 0 and result.startswith(want + 'fleet-local-0123456789abcdef'), repr(result) + r.stderr[-300:])

    # Teardown refuses without passed acceptance, before touching the runtime.
    s = state(); (s / 'prefix').write_text('fleet-mp-0000aaaa\n'); (s / 'evidence').mkdir()
    trace.write_text('')
    r = subprocess.run(['bash', str(root / 'hack/multiplane/teardown.sh'), str(s)], text=True, capture_output=True, env=base)
    check('teardown refuses unknown', r.returncode == 1 and 'refusing' in r.stderr, r.stderr)
    (s / 'evidence/summary.json').write_text('{"acceptance":"failed"}')
    r = subprocess.run(['bash', str(root / 'hack/multiplane/teardown.sh'), str(s)], text=True, capture_output=True, env=base)
    check('teardown refuses failed summary', r.returncode == 1)
    (s / 'evidence/summary.json').write_text('{"acceptance":"passed"}')
    r = subprocess.run(['bash', str(root / 'hack/multiplane/teardown.sh'), str(s)], text=True, capture_output=True, env=base)
    check('teardown refuses a summary that claims the run passed', r.returncode == 1)
    check('teardown refusal is effect-free', trace.read_text() == '')
    (s / 'prefix').write_text('other-prefix\n')
    r = subprocess.run(['bash', str(root / 'hack/multiplane/teardown.sh'), str(s), '--discard-unknown'], text=True, capture_output=True, env=base)
    check('teardown rejects foreign prefix', r.returncode == 1 and trace.read_text() == '')
    r = subprocess.run(['bash', str(root / 'hack/multiplane/run.sh'), '--bogus'], text=True, capture_output=True, env=base)
    check('run usage', r.returncode == 64)

    # Static guards for runtime-only step code the offline fakes cannot execute.
    for f in sorted((root / 'hack/multiplane').glob('*.*')):
        if f.suffix in ('.sh', '.py') and not f.name.startswith('test-'):
            text = f.read_text()
            check('docker context is the run parameter: ' + f.name, 'DOCKER_CONTEXT=colima' not in text and "['DOCKER_CONTEXT']='colima" not in text
                  and "['DOCKER_CONTEXT'] = 'colima" not in text and "'context','inspect','colima" not in text)
    run_text = (root / 'hack/multiplane/run.sh').read_text()
    check('site binary built for the selected architecture', 'GOARCH="$FLEET_MULTIPLANE_ARCH"' in run_text and 'GOARCH=arm64' not in run_text)
    check('site accelerator is the run parameter', 'FLEET_LOCAL_VM_ACCEL, value: tcg' not in (root / 'config/site/services/services.yaml').read_text()
          and '{name: FLEET_LOCAL_VM_ACCEL, value: "${schema.spec.accelerator}"}' in (root / 'config/site/graph/rgd.yaml').read_text()
          and 'accelerator: "${FLEET_LOCAL_VM_ACCEL}"' in (root / 'config/site/instance/dispatcher.yaml').read_text())
    runtime_text = (root / 'hack/local-vm/site-runtime').read_text()
    bmc_text = (root / 'config/site/redfish/services/bmc.yaml').read_text()
    adapter_text = (root / 'hack/redfish-lab/fleet_lab_adapter.py').read_text()
    check('lab BMC runs targets only under the runtime accelerator', '{name: FLEET_LAB_ACCEL, value: "${FLEET_LOCAL_VM_ACCEL}"}' in bmc_text
          and adapter_text.count('require_accelerator()') == 3 and "('kvm' if ACCEL == 'kvm' else 'qemu')" in adapter_text)
    redfish_suite = (root / 'hack/multiplane/acceptance-redfish.sh').read_text().splitlines()
    check('Redfish interruptions land on a held installer', redfish_suite.index('step hold-installer 120') < redfish_suite.index('step dispatch 900')
          < redfish_suite.index('step interrupt-job 600') < redfish_suite.index('step release-installer 120') < redfish_suite.index('rf lost-response 1200'))
    check('site runtime requires the KVM device and publishes its accelerator', 'kvm) [ -c /dev/kvm ]' in runtime_text and ' *) echo' in runtime_text
          and '"$sock/accelerator"' in runtime_text)
    check('interrupts land on a held installer in local VM mode', 'step hold-installer' in (root / 'hack/multiplane/acceptance.sh').read_text()
          and 'step release-installer' in (root / 'hack/multiplane/acceptance.sh').read_text() and "installerHold" in (root / 'hack/multiplane/evidence.py').read_text())
    steps = (root / 'hack/multiplane/steps.sh').read_text()
    evidence = (root / 'hack/multiplane/evidence.py').read_text()
    # The partition marker is counted from real output only: the shell's
    # trace of its printf shows the format, and a kernel message may prefix it.
    import re as _re
    partitioned = _re.compile(_re.search(r"PARTITIONED = re.compile\(r'([^']+)'\)", evidence).group(1))
    receipt = 'ab' * 32
    traced = subprocess.run(['sh', '-xc', "printf 'FLEET-PARTITIONED:%s\\n' \"$1\"", 'sh', receipt], text=True, capture_output=True)
    log = traced.stderr + '[    1.5] vda: vda1\n' + traced.stdout.replace('FLEET-', '[    1.6] virtio_blk: ok FLEET-', 1)
    check('init prints the partition marker after sfdisk', "sfdisk /dev/vda\n# One line" in (root / 'hack/local-vm/init').read_text()
          and "printf 'FLEET-PARTITIONED:%s\\n' \"$receipt\"" in (root / 'hack/local-vm/init').read_text())
    check('partition marker ignores its own trace', 'FLEET-PARTITIONED:' in traced.stderr and partitioned.findall(log) == [receipt], log)
    check('partition marker counts a second partition', partitioned.findall(log + traced.stdout) == [receipt, receipt])
    for name, text in (('steps', steps), ('evidence', evidence), ('harness', harness.read_text())):
        check('no docker process launch in site execution: ' + name, '-vm' not in text and 'cell.pid' not in text)
    sequence = (root / 'hack/multiplane/acceptance.sh').read_text()
    order = ['custody-crd-rules', 'import-inert', 'staff-access', 'allocate', 'verify-contention', 'tenant-isolation', 'admit-install', 'await-delivery', 'partition', 'hold-installer', 'dispatch', 'interrupt-pod', 'interrupt-job', 'release-installer', 'resume',
             'worker-rbac', 'partition-readiness', 'heal', 'install', 'install-repeat', 'lifecycle', 'graph-lifecycle', 'drift', 'restart', 'fail-closed', 'retention',
             'reflection-faults', 'release', 'release-status-a',
             'reuse', 'reuse-status', 'delivery-faults', 'deadline-admit', 'machine-delete', 'deadline-hold', 'release-b', 'deadline-settled', 'release-status-b',
             'rebuild', 'wrong-location', 'boot-denials', 'flux-final', 'evidence']
    positions = [sequence.find(' ' + name + ' ') for name in order]
    check('acceptance phase order', all(p >= 0 for p in positions) and positions == sorted(positions), str(list(zip(order, positions))))
    for name in order:
        if name in ('custody-crd-rules', 'allocate', 'admit-install', 'install', 'install-repeat', 'release', 'reuse', 'release-b', 'reflection-faults', 'evidence',
                    'graph-lifecycle', 'drift', 'restart', 'fail-closed', 'retention'):
            continue
        check('step implemented: ' + name, ' %s)' % name in steps or '|%s)' % name in steps or ' %s|' % name in steps)

    # The custody CRD rule phase proves the provider project API enforces the
    # CEL rules in its own namespace, then deletes every object it created, in
    # both modes. The CRDs belong to the provider project; it installs none.
    rules = (root / 'hack/multiplane/custody_crd_rules.py').read_text()
    redfish_sequence = (root / 'hack/multiplane/acceptance-redfish.sh').read_text()
    check('custody CRD rules run in both modes', 'ph custody-crd-rules ' in sequence and 'ph custody-crd-rules ' in redfish_sequence)
    check('custody CRD rules install nothing and remove what they create', 'crds' not in rules
          and "'delete', kind, name" in rules and "'Namespace'" not in rules and "namespace = 'fleet-rules'" in rules)
    check('custody CRD rules evidence is checked', "custody-crd-rules.json" in evidence and 'custody-crd-rules: the provider project API refused' in evidence)

    # The kro graph and the worker exit-code contract must agree. kro owns only
    # per-operation workers: the shared services and the durable site volume are
    # Flux-owned and only referenced by the graph.
    rgd = (root / 'config/site/graph/rgd.yaml').read_text()
    services = (root / 'config/site/services/services.yaml').read_text()
    main_go = (root / 'cmd/fleet-multiplane/main.go').read_text()
    check('Job replacement waits for terminal Pods', 'podReplacementPolicy: Failed' in rgd)
    check('rejected authority fails the Job', 'values: [65]' in rgd and 'exitRejected = 65' in main_go)
    templates = re.findall(r'\n      template:\n        apiVersion: \S+\n        kind: (\w+)', rgd)
    check('graph templates only per-operation kinds', sorted(templates) == ['Job', 'Role', 'RoleBinding', 'ServiceAccount'], str(templates))
    check('every graph template is per handoff', rgd.count('forEach:\n        - handoff: ${handoffs}') == 4 and rgd.count('${schema.spec.dispatch}') == 4)
    refs = re.findall(r'\n      externalRef:\n        apiVersion: \S+\n        kind: (\w+)', rgd)
    check('graph references the shared site and the volume', sorted(refs) == ['Deployment', 'Deployment', 'Deployment', 'MachineAllocationHandoff', 'PersistentVolumeClaim', 'Service'], str(refs))
    check('workers run the runtime image', 'image: ${hypervisor.spec.template.spec.containers[0].image}' in rgd and 'image: "${FLEET_SITE_IMAGE}"' in services)
    check('workers get per-operation resourceNames RBAC', 'resourceNames: ["${handoff.metadata.name}"]' in rgd)
    # kro v0.9.4 rejects these resource IDs (pkg/graph/validation.go, kroReservedKeyWords and CEL reserved symbols).
    reserved = {'apiVersion', 'context', 'dependency', 'dependencies', 'each', 'externalRef', 'externalReference', 'externalRefs', 'externalReferences',
                'graph', 'instance', 'item', 'items', 'kind', 'kro', 'metadata', 'namespace', 'object', 'resource', 'resourcegraphdefinition',
                'resourceGraphDefinition', 'resources', 'root', 'runtime', 'schema', 'self', 'serviceAccountName', 'spec', 'status', 'this', 'variables',
                'vars', 'version', 'true', 'false', 'null', 'in', 'as', 'break', 'const', 'continue', 'else', 'for', 'function', 'if', 'import', 'let',
                'loop', 'package', 'return', 'var', 'void', 'while'}
    ids = re.findall(r'^    - id: (\S+)$', rgd, re.M)
    check('graph resource IDs valid for kro', len(ids) == 10 and not set(ids) & reserved and all(re.fullmatch(r'[a-z][a-zA-Z0-9]*', i) for i in ids), str(ids))
    check('nothing in the graph is privileged', 'privileged: true' not in rgd)
    check('only the runtime is privileged', services.count('privileged: true') == 1)
    check('services own no volume', 'kind: PersistentVolumeClaim' not in services and 'kind: StorageClass' not in services)
    check('boot Service at a fixed ClusterIP', 'clusterIP: "${FLEET_BOOT_SERVICE_IP}"' in services)
    base = (root / 'config/site/base/site.yaml').read_text()
    check('site volume is retained', 'reclaimPolicy: Retain' in base)
    kro_rbac = (root / 'config/site/base/kro.yaml').read_text()
    cluster_role = kro_rbac.split('kind: ClusterRole\n')[1].split('\n---')[0]
    check('kro cluster-wide grant is read-only for site kinds', cluster_role.count('verbs: [get, list, watch]') == 5
          and cluster_role.count('create') == 1 and 'handoffdispatchers' in cluster_role.split('create')[0], cluster_role)
    roles = {r.split('\n')[0]: r.split('\n---')[0] for r in kro_rbac.split('kind: Role\nmetadata: ')[1:]}
    write_roles = {'{name: fleet-site-kro-workers, namespace: fleet-site}': ('jobs', 'serviceaccounts'),
                   '{name: fleet-site-kro-worker-rbac, namespace: fleet-coordination}': ('roles, rolebindings',)}
    check('kro writes only worker kinds in their namespaces', sorted(roles) == sorted(write_roles) and all(
        all(k in roles[n] for k in kinds) and not any(x in roles[n] for x in ('deployments', 'services', 'persistentvolumeclaims', 'configmaps', 'secrets'))
        for n, kinds in write_roles.items()), str(sorted(roles)))
    check('kro writes are shape-checked by admission', kro_rbac.count('kind: ValidatingAdmissionPolicy\n') == 2 and kro_rbac.count('kind: ValidatingAdmissionPolicyBinding\n') == 2
          and kro_rbac.count("request.userInfo.username == 'system:serviceaccount:kro-system:kro'") == 2 and 'parameterNotFoundAction: Deny' in kro_rbac)
    kro = (root / 'config/multiplane/kro/kro.yaml').read_text()
    check('unused chart ConfigMap grant removed, failing closed', 'postRenderers:' in kro and 'op: test' in kro and 'resources: [configmaps]' in kro and 'op: remove' in kro)
    check('kro version pinned consistently', 'tag: v0.9.4@sha256:' in kro and "'version': 'v0.9.4'" in evidence and 'digest: sha256:4e8de4d3' in kro)

    # Imperative service installation is gone: only Flux installs services.
    run_sh = (root / 'hack/multiplane/run.sh').read_text()
    for name in ('run.sh', 'steps.sh', 'site.sh', 'topology.sh', 'milo.py', 'roles.py', 'verify-rbac.py'):
        text = (root / 'hack/multiplane' / name).read_text()
        check('no helm install in ' + name, 'helm install' not in text and 'helm upgrade' not in text)
        check('no manifest apply of config in ' + name, not any(x in text for x in ("apply -f config/", "'apply','-f','config/multiplane", "'apply','-f','config/site", 'kind load')))
    check('no docker-run Milo API', 'docker' not in (root / 'hack/multiplane/milo.py').read_text().replace("os.environ['DOCKER_CONTEXT']", ''))
    for f in ('flux-tools', 'topology', 'flux-source', 'flux-bootstrap-$c', 'flux-platform', 'flux-milo', 'federation-enrollment', 'flux-site', 'flux-verify'):
        check('bootstrap phase present: ' + f, 'ph %s ' % f in run_sh or 'ph "%s" ' % f in run_sh)
    check('dispatch is a source revision', "--site dispatch" in steps and 'patch handoffdispatcher' not in steps)
    steps_py = (root / 'hack/multiplane/media_steps.py').read_text()
    # The stale-fence probe's opens must be well-formed, so only the fence
    # decides the cell worker's answer: they send every field a window open
    # takes.
    server = (root / 'internal/site/redfishwork/mediaserver.go').read_text()
    fields = re.findall(r'json:"([a-zA-Z0-9]+)', server[server.index('type OpenRequest struct'):server.index('}', server.index('type OpenRequest struct'))])
    stale = steps_py[steps_py.index('def stale_fence'):steps_py.index('\ndef ', steps_py.index('def stale_fence') + 1)]
    missing = [f for f in fields if "'%s'" % f not in stale]
    check('the stale-fence probe sends every window open field', fields and not missing, str(missing))
    arms = re.findall(r'^\s+arm\(([^,)]*)', steps_py, re.M)
    check('every media lab fault is armed with the run state', arms and all(a == 's' for a in arms), str(arms))
    graph = (root / 'config/site/redfish/graph/kustomization.yaml').read_text()
    report_from = re.search(r'FLEET_MEDIA_REPORT_FROM, value: ([0-9./]+)', graph)
    central = (root / 'hack/multiplane/media-central.sh').read_text()
    clients = re.search(r'--clients "?(\S+?)"? ', central)
    check('the central media service admits the site\'s report range as clients',
          report_from is not None and clients is not None and report_from.group(1) in clients.group(1).split(','))
    media = (root / 'hack/multiplane/media_steps.py').read_text()
    observer = (root / 'config/site/services/services.yaml').read_text()
    check('the delivery declaration is site enrollment, changed by a source revision',
          "'--delivery-methods'" in media and 'coordinator.sh' not in media and 'DeliveryMethods' not in run_sh
          and 'name: fleet-site-delivery' in observer and 'binaryData:\n  methods: ${FLEET_SITE_DELIVERY_METHODS}' in observer
          and '--delivery-methods, /etc/fleet-site-delivery/methods' in observer)
    check('fault injection suspends Flux first', 'suspend milo-consumer-a true' in steps and '"suspend":true' in (root / 'hack/multiplane/reflection-faults.py').read_text())
    check('RBAC revocation repaired by Flux, not the harness', "'apply'" not in (root / 'hack/multiplane/verify-rbac.py').read_text())
    for path in ('config/site/base/site.yaml', 'config/site/graph/rgd.yaml', 'config/site/instance/dispatcher.yaml', 'config/multiplane/coordination/crd.yaml',
                 'config/multiplane/karmada/karmada.yaml', 'config/multiplane/karmada-members/members.yaml'):
        check('prune-protected: ' + path, 'kustomize.toolkit.fluxcd.io/prune: disabled' in (root / path).read_text())
    check('site volume is create-once', base.count('kustomize.toolkit.fluxcd.io/ssa: IfNotPresent') == 2)
    check('graph has no Flux substitution', 'postBuild' not in (root / 'config/multiplane/clusters/cell/cell.yaml').read_text().split('name: fleet-site-graph')[1])
    cell_layers = (root / 'config/multiplane/clusters/cell/cell.yaml').read_text()
    site_layers = (root / 'config/multiplane/clusters/cell-site/site.yaml').read_text()
    base_layer = cell_layers.split('name: fleet-site-base')[1].split('\n---')[0]
    services_layer = site_layers.split('name: fleet-site-services')[1].split('\n---')[0]
    dispatch_layer = site_layers.split('name: fleet-site-dispatch')[1]
    check('shared site infrastructure does not depend on kro', 'name: kro' not in base_layer and 'name: kro' not in services_layer and '{name: fleet-site-base' in services_layer)
    check('dispatcher waits for graph and shared services', '{name: fleet-site-graph' in dispatch_layer and '{name: fleet-site-services' in dispatch_layer)
    check('site layers use scoped identities', 'serviceAccountName: fleet-site-services-reconciler' in services_layer and 'serviceAccountName: fleet-site-dispatch-reconciler' in dispatch_layer)
    r = subprocess.run([sys.executable, str(root / 'hack/multiplane/test-flux.py'), str(root)], text=True, capture_output=True)
    check('flux source and scenario tests', r.returncode == 0, (r.stdout + r.stderr)[-1500:])

if failures:
    print('FAIL multi-plane harness tests:\n  ' + '\n  '.join(failures))
    sys.exit(1)
print('PASS multi-plane harness tests')
PY
