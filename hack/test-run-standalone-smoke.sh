#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
exec python3 - "$root" <<'PY'
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import sys
import tempfile
import time


mock = r'''#!PYTHON
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import time

tool = Path(sys.argv[0]).name
scenario = os.environ['SCENARIO']
directory = Path(os.environ['CASE_DIRECTORY'])
arguments = sys.argv[1:]
stage = tool
if tool == 'kind':
    stage = arguments[0]
record = {'stage': stage, 'arguments': arguments,
          'cluster': os.environ['KIND_CLUSTER'], 'context': os.environ['KUBECTL_CONTEXT'],
          'kubeconfig': os.environ['KUBECONFIG'], 'temporary': os.environ['TMPDIR'],
          'mode': Path(os.environ['KUBECONFIG']).stat().st_mode & 0o777,
          'directory_mode': Path(os.environ['TMPDIR']).stat().st_mode & 0o777,
          'provider': os.environ['KIND_EXPERIMENTAL_PROVIDER'],
          'alias': os.environ['ALLOW_KIND_CONTEXT_ALIAS']}
with (directory / 'trace').open('a') as trace:
    trace.write(json.dumps(record) + '\n')
print('SYNTHETIC-SECRET-MUST-NOT-LEAK', file=sys.stderr, flush=True)
if tool == 'docker':
    if scenario == 'docker_fail':
        print('SYNTHETIC-SECRET-MUST-NOT-LEAK')
        sys.exit(71)
    print('windows' if scenario == 'wrong_os' else 'linux')
elif stage == 'get':
    if scenario == 'list_fail':
        sys.exit(73)
    print('unrelated-cluster')
    if scenario == 'collision':
        print(os.environ['KIND_CLUSTER'])
elif stage == 'create':
    assert '--retain' in arguments
    assert arguments[arguments.index('--kubeconfig') + 1] == os.environ['KUBECONFIG']
    assert arguments[arguments.index('--name') + 1] == os.environ['KIND_CLUSTER']
    assert '@sha256:' in arguments[arguments.index('--image') + 1]
    assert Path(arguments[arguments.index('--config') + 1]).read_text().endswith('- role: control-plane\n')
    (directory / 'owned-cluster').touch()
    if scenario == 'create_fail':
        print('SYNTHETIC-SECRET-MUST-NOT-LEAK')
        sys.exit(74)
elif stage == 'delete':
    assert arguments[arguments.index('--name') + 1] == os.environ['KIND_CLUSTER']
    assert arguments[arguments.index('--kubeconfig') + 1] == os.environ['KUBECONFIG']
    if scenario in ('delete_fail', 'smoke_delete_fail'):
        sys.exit(43)
    if scenario != 'delete_timeout':
        (directory / 'owned-cluster').unlink()
elif tool == 'standalone-smoke.sh':
    assert Path.cwd() == (directory / 'repo').resolve()
    print('SYNTHETIC-SECRET-MUST-NOT-LEAK', flush=True)
    if scenario in ('smoke_fail', 'smoke_delete_fail'):
        sys.exit(42)
else:
    sys.exit(99)

if scenario in (stage + '_timeout', stage + '_signal'):
    signal.signal(signal.SIGTERM, signal.SIG_IGN)
    child = subprocess.Popen([sys.executable, '-c',
        'import signal,time; signal.signal(signal.SIGTERM, signal.SIG_IGN); time.sleep(60)'])
    (directory / 'descendant').write_text(str(child.pid))
    while True:
        time.sleep(1)
'''.replace('#!PYTHON', '#!' + sys.executable, 1)


def check(condition, message):
    if not condition:
        raise AssertionError(message)


with tempfile.TemporaryDirectory(prefix='test-fleet-smoke-') as temporary:
    suite = Path(temporary)
    names = []

    # Each mock is a fresh Python process, which can take over a second to
    # start on a loaded machine. Cleanup that is expected to finish gets ten
    # seconds; the two cases that test the cleanup deadline itself get five,
    # enough for the hanging mock to start and record itself.
    def case(scenario, expected, missing=None, timeout=None, send_signal=None, arguments=(), cleanup_timeout='10'):
        directory = suite / (scenario + '-' + str(len(list(suite.iterdir()))))
        directory.mkdir()
        repository = directory / 'repo'
        (repository / 'hack').mkdir(parents=True)
        wrapper = repository / 'hack/run-standalone-smoke.sh'
        shutil.copy2(Path(sys.argv[1]) / 'hack/run-standalone-smoke.sh', wrapper)
        inner = repository / 'hack/standalone-smoke.sh'
        inner.write_text(mock)
        inner.chmod(0o755)
        binaries = directory / 'bin'
        binaries.mkdir()
        for tool in ('docker', 'kind', 'kubectl', 'kustomize', 'git'):
            if tool != missing:
                target = binaries / tool
                target.write_text(mock)
                target.chmod(0o755)
        for tool in ('python3', 'dirname'):
            if tool != missing:
                (binaries / tool).symlink_to(shutil.which(tool))
        home = directory / 'home'
        (home / '.kube').mkdir(parents=True)
        credentials = [home / '.kube/config', directory / 'first-config', directory / 'second-config']
        for credential in credentials:
            credential.write_text('user-config-and-current-context-must-not-change\n')
        scratch = directory / 'scratch'
        scratch.mkdir()
        environment = dict(os.environ, PATH=str(binaries), HOME=str(home), TMPDIR=str(scratch),
                           KUBECONFIG=str(credentials[1]) + ':' + str(credentials[2]),
                           KIND_CLUSTER='unrelated-cluster', KUBECTL_CONTEXT='production',
                           ALLOW_KIND_CONTEXT_ALIAS='true', KIND_EXPERIMENTAL_PROVIDER='podman',
                           SCENARIO=scenario, CASE_DIRECTORY=str(directory),
                           FLEET_SMOKE_TIMEOUT_SECONDS=timeout or '10',
                           FLEET_SMOKE_CLEANUP_TIMEOUT_SECONDS=cleanup_timeout)
        started = time.monotonic()
        process = subprocess.Popen([str(wrapper), *arguments], env=environment,
                                   stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
        try:
            if send_signal:
                deadline = time.monotonic() + 10
                while not (directory / 'descendant').exists():
                    check(process.poll() is None, scenario + ' exited before signal')
                    check(time.monotonic() < deadline, scenario + ' did not start')
                    time.sleep(0.02)
                process.send_signal(send_signal)
            output = process.communicate(timeout=30)[0].decode()
        finally:
            if process.poll() is None:
                process.kill()
                process.communicate()
        check(process.returncode == expected, scenario + ': unexpected status ' + str(process.returncode) + '\n' + output)
        check(time.monotonic() - started < 30, scenario + ': unbounded execution')
        check('SYNTHETIC-SECRET' not in output, scenario + ': raw diagnostics leaked')
        for credential in credentials:
            check(credential.read_text() == 'user-config-and-current-context-must-not-change\n', scenario + ': changed user config')
        check(not list(scratch.iterdir()), scenario + ': leaked private temporary files')
        trace = directory / 'trace'
        records = [json.loads(line) for line in trace.read_text().splitlines()] if trace.exists() else []
        stages = [record['stage'] for record in records]
        for record in records:
            check(record['cluster'].startswith('fleet-smoke-') and record['cluster'] != 'unrelated-cluster', 'unsafe name')
            check(record['context'] == 'kind-' + record['cluster'], 'unsafe context')
            check(record['provider'] == 'docker' and record['alias'] == 'false', 'unsafe inherited settings')
            check(record['mode'] == 0o600 and record['directory_mode'] == 0o700, 'public credentials')
            check(Path(record['kubeconfig']).parent == Path(record['temporary']), 'nonprivate kubeconfig')
            check(Path(record['temporary']).parent == scratch, 'nonprivate scratch')
            check(not Path(record['temporary']).exists(), 'temporary directory survived')
        check(stages.count('delete') == (1 if 'create' in stages else 0), scenario + ': wrong cleanup authority')
        check(len({record['cluster'] for record in records}) <= 1, scenario + ': changed cluster identity')
        if 'create' in stages:
            names.append(records[0]['cluster'])
            if scenario not in ('delete_fail', 'smoke_delete_fail', 'delete_timeout'):
                check(not (directory / 'owned-cluster').exists(), scenario + ': cluster leaked')
            else:
                check('cleanup unverified' in output, scenario + ': cleanup uncertainty concealed')
        if scenario in ('collision', 'create_fail', 'create_timeout', 'create_signal'):
            check('standalone-smoke.sh' not in stages, scenario + ': ran smoke without fresh cluster')
        if scenario == 'success':
            check(stages == ['docker', 'get', 'create', 'standalone-smoke.sh', 'delete'], 'incorrect success lifecycle')
        descendant = directory / 'descendant'
        if descendant.exists():
            process_id = int(descendant.read_text())
            state = subprocess.run(['ps', '-o', 'stat=', '-p', str(process_id)],
                                   capture_output=True, text=True, check=False).stdout.strip()
            check(not state or state.startswith('Z'), scenario + ': descendant survived process-group cleanup')
        print('PASS ' + scenario + (': missing ' + missing if missing else ''))

    case('success', 0)
    case('success', 0)
    for tool in ('python3', 'docker', 'kind', 'kubectl', 'kustomize', 'git'):
        case('missing', 1, missing=tool)
    case('arguments', 1, arguments=('existing-cluster',))
    for value in ('0', '-1', 'oops', '3601'):
        case('invalid_timeout', 1, timeout=value)
    case('docker_fail', 71)
    case('wrong_os', 1)
    case('list_fail', 73)
    case('collision', 1)
    case('create_fail', 74)
    case('smoke_fail', 42)
    case('delete_fail', 1)
    case('smoke_delete_fail', 42)
    case('docker_timeout', 124, timeout='1')
    case('get_timeout', 124, timeout='1')
    case('create_timeout', 124, timeout='1')
    case('standalone-smoke.sh_timeout', 124, timeout='1')
    case('delete_timeout', 1, cleanup_timeout='5')
    case('get_signal', 143, send_signal=signal.SIGTERM)
    case('create_signal', 143, send_signal=signal.SIGTERM)
    case('standalone-smoke.sh_signal', 130, send_signal=signal.SIGINT)
    case('standalone-smoke.sh_signal', 129, send_signal=signal.SIGHUP)
    case('delete_signal', 1, send_signal=signal.SIGTERM, cleanup_timeout='5')
    check(len(names) == len(set(names)), 'cluster names reused across invocations')
    print('All disposable standalone smoke tests passed; no real tools or clusters used.')
PY
