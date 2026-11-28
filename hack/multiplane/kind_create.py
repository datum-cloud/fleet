#!/usr/bin/env python3
"""Create one kind cluster whose host ports are chosen at creation time.

kind resolves every host port (its API server port and each extraPortMapping)
before it starts the node container, so another process can bind a port in
between and Docker refuses the start with "port is already allocated". That
failure happens when the node container starts, before anything runs in the
cluster, so this deletes the cluster it was creating and tries again with
fresh ports, a bounded number of times. Any other failure is final.

The template names each host port as @port:NAME@. Every attempt replaces
them with fresh free ports, writes the kind config, and records the ports
the created cluster bound as {NAME: port} in --ports.
"""
import argparse
import json
import pathlib
import re
import socket
import subprocess
import sys

TAKEN = 'port is already allocated'
PORT = re.compile(r'@port:([a-z0-9-]+)@')


def free_port():
    s = socket.socket()
    s.bind(('127.0.0.1', 0))
    port = s.getsockname()[1]
    s.close()
    return port


def kind(*args):
    """Run kind, echoing its output, and return (exit code, output)."""
    p = subprocess.Popen(['kind', *args], stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
    lines = []
    for line in p.stdout:
        sys.stdout.write(line)
        lines.append(line)
    return p.wait(), ''.join(lines)


def main():
    a = argparse.ArgumentParser()
    a.add_argument('--name', required=True)
    a.add_argument('--template', required=True)
    a.add_argument('--config', required=True)
    a.add_argument('--ports', required=True)
    a.add_argument('--kubeconfig', required=True)
    a.add_argument('--image', required=True)
    a.add_argument('--attempts', type=int, default=3)
    args = a.parse_args()
    template = pathlib.Path(args.template).read_text()
    names = sorted(set(PORT.findall(template)))
    for attempt in range(1, args.attempts + 1):
        ports = {}
        while len(ports) < len(names):
            port = free_port()
            if port not in ports.values():
                ports[names[len(ports)]] = port
        pathlib.Path(args.config).write_text(PORT.sub(lambda m: str(ports[m.group(1)]), template))
        code, out = kind('create', 'cluster', '--name', args.name, '--config', args.config, '--kubeconfig', args.kubeconfig,
                         '--image', args.image, '--wait', '180s')
        if code == 0:
            pathlib.Path(args.ports).write_text(json.dumps(ports, sort_keys=True) + '\n')
            return 0
        if TAKEN not in out:
            return code
        # Only this run's own cluster, which failed before any node ran.
        kind('delete', 'cluster', '--name', args.name, '--kubeconfig', args.kubeconfig)
        print('kind-create: a host port was taken before %s bound it (attempt %d of %d)' % (args.name, attempt, args.attempts), file=sys.stderr)
    print('kind-create: %s could not bind fresh host ports in %d attempts' % (args.name, args.attempts), file=sys.stderr)
    return 1


if __name__ == '__main__':
    sys.exit(main())
