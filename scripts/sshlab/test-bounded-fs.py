#!/usr/bin/env python3
"""Verify a bounded-fs lease on an existing owned sshlab instance; no full test run."""

import argparse
import hashlib
import json
from pathlib import Path
import selectors
import shlex
import subprocess
import sys
import tempfile
import uuid


def verify(state):
    state = state.resolve()
    if not state.is_relative_to(Path('/tmp').resolve()):
        raise ValueError('expected an owned sshlab state directory under /tmp')
    metadata = {}
    for line in (state / 'meta.env').read_text().splitlines():
        key, value = line.split('=', 1)
        words = shlex.split(value)
        if len(words) != 1:
            raise ValueError('invalid sshlab metadata')
        metadata[key] = words[0]
    environment = metadata['SSHLAB_ENV']
    name = metadata['SSHLAB_CONTAINER_NAME']
    alias = metadata['SSHLAB_HOST_ALIAS']
    config = Path(metadata['SSHLAB_SSH_CONFIG']).resolve()
    remote_repo = metadata['SSHLAB_REMOTE_REPO']
    if (environment not in ('e1', 'e2', 'e3') or not name.startswith('bl-')
            or alias != name or remote_repo != '/tmp/branchline-test-' + environment
            or config != state / 'ssh_config'):
        raise ValueError('metadata does not identify a disposable sshlab instance')
    port = metadata['SSHLAB_SSH_PORT']
    if not port.isdecimal() or not 1 <= int(port) <= 65535:
        raise ValueError('invalid published sshlab sshd port in metadata')
    inspected = subprocess.run(['docker', 'inspect', name], capture_output=True,
                               check=True, timeout=20)
    identity = json.loads(inspected.stdout)[0]
    if identity.get('Name', '') != '/' + name or identity['State']['Status'] != 'running':
        raise ValueError('owned instance is not running')
    base = ['/usr/bin/ssh', '-F', str(config), '-o', 'BatchMode=yes',
            '-o', 'ControlPath=none', alias]

    def ssh(command, data=None, expected=0):
        result = subprocess.run(base + [command], input=data, capture_output=True, timeout=25)
        if result.returncode != expected:
            raise ValueError(f'SSH exit {result.returncode}, expected {expected}: '
                             + result.stderr.decode(errors='replace'))
        return result

    script = Path(__file__).with_name('bounded-fs.sh').read_bytes()
    remote = remote_repo + '/.bounded-fs-' + uuid.uuid4().hex + '.sh'
    records = []
    ssh('umask 077; cat > ' + shlex.quote(remote), script)
    try:
        parent = ssh('readlink /proc/self/ns/mnt').stdout.decode().strip()
        for close_mode in ('release', 'eof'):
            with tempfile.TemporaryFile() as errors:
                lease = subprocess.Popen(base + ['sh ' + shlex.quote(remote) + ' serve'],
                                         stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=errors)
                try:
                    with selectors.DefaultSelector() as selector:
                        selector.register(lease.stdout, selectors.EVENT_READ)
                        if not selector.select(15):
                            raise ValueError('lease readiness timed out')
                    line = lease.stdout.readline().decode().strip()
                    fields = line.split()
                    if len(fields) != 5:
                        errors.seek(0)
                        raise ValueError('invalid lease manifest: ' + errors.read().decode())
                    kind, pid, namespace, root, capacity = fields
                    if (kind != 'branchline-bounded-fs-v1' or not pid.isdecimal()
                            or not namespace.isdecimal() or capacity != '16777216'
                            or not root.startswith('/tmp/branchline-test-bounded-fs.')
                            or parent == 'mnt:[' + namespace + ']'):
                        raise ValueError('lease invariant failed')
                    target = root + '/fs'
                    prefix = ('test "$(readlink /proc/' + pid + '/ns/mnt)" = '
                              + shlex.quote('mnt:[' + namespace + ']')
                              + '; nsenter --target ' + pid + ' --user --mount -- /bin/sh -c ')

                    def inside(command, expected=0):
                        return ssh('set -e; ' + prefix + shlex.quote(command), expected=expected)

                    mount = inside('set -eu; mountpoint -q ' + shlex.quote(target)
                                   + '; stat -f -c %T ' + shlex.quote(target)
                                   + '; df -Pk ' + shlex.quote(target)).stdout.decode()
                    if 'tmpfs' not in mount:
                        raise ValueError('not a tmpfs mount')
                    inside('printf private > ' + shlex.quote(target + '/sentinel'))
                    ssh('test ! -e ' + shlex.quote(target + '/sentinel'))
                    full = inside('LC_ALL=C dd if=/dev/zero of=' + shlex.quote(target + '/filler')
                                  + ' bs=1048576 count=17', expected=1)
                    if b'No space left on device' not in full.stderr:
                        raise ValueError('fill failed without actual ENOSPC: ' + full.stderr.decode())
                    usage = inside('df -Pk ' + shlex.quote(target)).stdout.decode()
                    if usage.splitlines()[-1].split()[3] != '0':
                        raise ValueError('bounded filesystem did not become full')
                    inside('rm -- ' + shlex.quote(target + '/filler') + '; printf restored > '
                           + shlex.quote(target + '/recovered') + '; test "$(cat '
                           + shlex.quote(target + '/recovered') + ')" = restored')
                    if close_mode == 'release':
                        lease.stdin.write(b'release\n')
                        lease.stdin.flush()
                    lease.stdin.close()
                    code = lease.wait(timeout=15)
                    errors.seek(0)
                    if code != 0:
                        raise ValueError(f'lease exit {code}: ' + errors.read().decode())
                    ssh('set -eu; test ! -e ' + shlex.quote(root) + '; test ! -e /proc/' + pid + '/ns/mnt')
                    records.append({'case': close_mode, 'manifest': line, 'parent_namespace': parent,
                                    'mount': mount, 'full_df': usage, 'dd_exit': full.returncode,
                                    'dd_stderr': full.stderr.decode(), 'recovery_exit': 0,
                                    'lease_exit': code, 'directory_and_process_absent': True})
                finally:
                    if lease.poll() is None:
                        lease.stdin.close()
                        lease.wait(timeout=15)
    finally:
        ssh('rm -- ' + shlex.quote(remote))
    return {'result': 'PASS', 'container': name, 'environment': environment,
            'script_sha256': hashlib.sha256(script).hexdigest(), 'records': records,
            'scope': 'namespace/ENOSPC fixture only; no UI or full end-to-end acceptance'}


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--state', type=Path, required=True, help='owned sshlab e1/e2/e3 state directory')
    arguments = parser.parse_args()
    try:
        print(json.dumps(verify(arguments.state), indent=2))
    except (ValueError, OSError, KeyError, subprocess.SubprocessError) as error:
        print(f'bounded-fs verification: {error}', file=sys.stderr)
        sys.exit(1)
