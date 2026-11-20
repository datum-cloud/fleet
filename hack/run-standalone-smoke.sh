#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
if ! command -v python3 >/dev/null 2>&1; then
  echo 'standalone smoke requires python3' >&2
  exit 1
fi
exec python3 - "$root" "$@" <<'PY'
import os
from pathlib import Path
import shutil
import signal
import subprocess
import sys
import tempfile
import time
import uuid


class Failure(Exception):
    def __init__(self, message, status=1):
        super().__init__(message)
        self.status = status


interrupted = 0


def interrupt(signum, frame):
    global interrupted
    interrupted = signum


def seconds(name, default):
    value = os.environ.get(name, str(default))
    if not value.isascii() or not value.isdigit() or not 1 <= int(value) <= 3600:
        raise Failure(name + ' must be an integer from 1 to 3600')
    return int(value)


def run(label, command, environment, deadline, capture=False, cleanup=False, on_start=None):
    print('standalone smoke: ' + label, flush=True)
    if interrupted and not cleanup:
        raise Failure('interrupted', 128 + interrupted)
    if time.monotonic() >= deadline:
        raise Failure(label + ' timed out', 124)
    with tempfile.TemporaryFile(dir=environment['TMPDIR']) as output:
        process = subprocess.Popen(command, env=environment, stdin=subprocess.DEVNULL,
                                   stdout=output, stderr=subprocess.DEVNULL,
                                   start_new_session=True)
        try:
            if on_start:
                on_start()
            while process.poll() is None:
                if interrupted and not cleanup:
                    raise Failure('interrupted', 128 + interrupted)
                if time.monotonic() >= deadline:
                    raise Failure(label + ' timed out', 124)
                time.sleep(0.1)
            if interrupted and not cleanup:
                raise Failure('interrupted', 128 + interrupted)
            if process.returncode != 0:
                raise Failure(label + ' failed (exit ' + str(process.returncode) + ')',
                              process.returncode if process.returncode > 0 else 1)
            if capture:
                output.seek(0)
                return output.read().decode('utf-8')
        finally:
            try:
                os.killpg(process.pid, signal.SIGTERM)
            except ProcessLookupError:
                pass
            try:
                process.wait(timeout=2)
            except subprocess.TimeoutExpired:
                pass
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            process.wait(timeout=2)


def main():
    if len(sys.argv) != 2:
        raise Failure('usage: hack/run-standalone-smoke.sh (no arguments)')
    timeout = seconds('FLEET_SMOKE_TIMEOUT_SECONDS', 1200)
    cleanup_timeout = seconds('FLEET_SMOKE_CLEANUP_TIMEOUT_SECONDS', 120)
    for tool in ('docker', 'kind', 'kubectl', 'kustomize', 'git'):
        if shutil.which(tool) is None:
            raise Failure('missing required tool: ' + tool)
    os.chdir(sys.argv[1])
    os.umask(0o077)
    cluster = 'fleet-smoke-' + uuid.uuid4().hex
    created = False

    def mark_created():
        nonlocal created
        created = True

    status = 0
    deadline = time.monotonic() + timeout
    with tempfile.TemporaryDirectory(prefix='fleet-smoke-') as directory:
        kubeconfig = str(Path(directory) / 'kubeconfig')
        Path(kubeconfig).touch(mode=0o600)
        config = str(Path(directory) / 'kind.yaml')
        Path(config).write_text('kind: Cluster\napiVersion: kind.x-k8s.io/v1alpha4\n'
                                'nodes:\n- role: control-plane\n')
        environment = dict(os.environ, KUBECONFIG=kubeconfig, TMPDIR=directory,
                           KIND_CLUSTER=cluster, KUBECTL_CONTEXT='kind-' + cluster,
                           ALLOW_KIND_CONTEXT_ALIAS='false', KIND_EXPERIMENTAL_PROVIDER='docker')
        try:
            operating_system = run('Docker preflight', ['docker', 'info', '--format', '{{.OSType}}'],
                                   environment, min(deadline, time.monotonic() + 30), capture=True)
            if operating_system.strip() != 'linux':
                raise Failure('Docker must provide Linux containers')
            clusters = run('kind preflight', ['kind', 'get', 'clusters'], environment,
                           min(deadline, time.monotonic() + 30), capture=True)
            if cluster in clusters.splitlines():
                raise Failure('generated cluster name already exists; refusing to adopt or delete it')
            print('standalone smoke: disposable cluster ' + cluster, flush=True)
            run('create cluster', ['kind', 'create', 'cluster', '--name', cluster,
                                  '--kubeconfig', kubeconfig, '--config', config, '--retain',
                                  '--image', 'kindest/node:v1.35.0@sha256:452d707d4862f52530247495d180205e029056831160e22870e37e3f6c1ac31f',
                                  '--wait', '120s'], environment,
                min(deadline, time.monotonic() + 240), on_start=mark_created)
            run('synthetic lifecycle', ['./hack/standalone-smoke.sh'], environment, deadline)
        except Failure as error:
            print('standalone smoke: ' + str(error), file=sys.stderr)
            status = error.status
        except (OSError, ValueError, subprocess.SubprocessError):
            print('standalone smoke: command execution failed; raw diagnostics withheld', file=sys.stderr)
            status = 1
        finally:
            if created:
                try:
                    run('delete cluster', ['kind', 'delete', 'cluster', '--name', cluster,
                                           '--kubeconfig', kubeconfig], environment,
                        time.monotonic() + cleanup_timeout, cleanup=True)
                except (Failure, OSError, ValueError, subprocess.SubprocessError):
                    print('standalone smoke: cleanup unverified for ' + cluster +
                          '; inspect this exact cluster before manual cleanup', file=sys.stderr)
                    status = status or 1
    return status or (128 + interrupted if interrupted else 0)


for signum in (signal.SIGINT, signal.SIGTERM, signal.SIGHUP):
    signal.signal(signum, interrupt)
try:
    sys.exit(main())
except (Failure, OSError, ValueError) as error:
    print('standalone smoke: ' + (str(error) if isinstance(error, Failure)
                                 else 'local setup failed; raw diagnostics withheld'), file=sys.stderr)
    sys.exit(error.status if isinstance(error, Failure) else 1)
PY
