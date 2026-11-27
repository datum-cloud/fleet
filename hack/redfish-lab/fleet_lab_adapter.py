#!/usr/bin/env python3
"""Lab-only hardware adapter for the DMTF Redfish Interface Emulator.

The unmodified upstream emulator (DMTF Redfish-Interface-Emulator 1.2.2,
BSD-3-Clause) serves the service root, Systems, Chassis, Managers, ActionInfo
and its own routing and serialization. Upstream keeps ComputerSystem.Reset and
PowerState purely in memory, rejects Boot in a System PATCH and enforces no
authentication. This module, loaded into the same Flask process, adds only:

  * HTTP Basic authentication against a mounted synthetic credential;
  * ComputerSystem.Reset (On, ForceOff, ForceRestart) applied to an exact
    persistent libvirt domain per System, and PowerState read from libvirt;
  * Boot source override (Pxe, Hdd or Cd, Once) persisted like BMC NVRAM and
    consumed when the domain is started;
  * VirtualMedia on each System: two CD devices with InsertMedia, which
    fetches the image over HTTPS verifying the lab media service's CA, and
    EjectMedia, persisted like BMC state; inserted media are attached to the
    domain as CD-ROMs;
  * an append-only request audit log (no bodies, no credentials);
  * bounded, one-shot lab fault injection driven by a state file.

It is never part of Fleet's production code. It is the only component in
Redfish mode that talks to libvirt; provisioning workers have no libvirt
socket and reach the lab hardware only through Redfish.
"""
import base64
import datetime
import fcntl
import hashlib
import hmac
import ipaddress
import ssl
import json
import os
import re
import subprocess
import sys
import tempfile
import threading
import time
import urllib.parse
import urllib.request

UPSTREAM = os.environ.get('FLEET_BMC_UPSTREAM', '/opt/redfish-emulator')
CONFIG_DIR = os.environ.get('FLEET_BMC_CONFIG', '/etc/fleet-bmc')
STATE = os.environ.get('FLEET_BMC_STATE', '/var/lib/fleet-bmc')
CREDENTIALS = os.environ.get('FLEET_BMC_CREDENTIALS', '/etc/fleet-bmc-credentials')
LIBVIRT_URI = os.environ.get('FLEET_LIBVIRT_URI', 'qemu:///system?socket=/run/fleet-libvirt/libvirt-sock')
VIRSH = os.environ.get('FLEET_VIRSH', 'virsh')
PORT = int(os.environ.get('FLEET_BMC_PORT', '8443'))
# The lab BMC serves TLS with a key the harness generates per run, so Fleet
# workers reach it only over a connection pinned to that key.
TLS_DIR = os.environ.get('FLEET_BMC_TLS', '/etc/fleet-bmc-tls')
RESET_TYPES = ('On', 'ForceOff', 'ForceRestart')
BOOT_TARGETS = ('None', 'Pxe', 'Hdd', 'Cd')
# Virtual media: two CD devices per System, attached to the domain as IDE
# CD-ROMs in this order. Fetched media live on a volume the hypervisor also
# mounts at the same path.
MEDIA_DEVICES = (('CD1', 'hdc'), ('CD2', 'hdd'))
MEDIA_DIR = os.environ.get('FLEET_BMC_MEDIA', '/var/lib/fleet-lab-media')
MEDIA_CA = os.environ.get('FLEET_BMC_MEDIA_CA', '/etc/fleet-bmc-media-ca/ca.crt')
MEDIA_LIMIT = 1 << 30
BOOT_ENABLED = ('Disabled', 'Once')
NETWORK = 'fleet-lab-pxe'
# Lab DHCP mode. static: libvirt's dnsmasq hands each target a fixed lease.
# synced: the network has no DHCP of its own, and the site's Kea, fed by
# Fleet's reservation feed, is the only DHCP server on the bridge.
LAB_DHCP = os.environ.get('FLEET_LAB_DHCP', 'static')
# The accelerator the lab targets run under, kvm or tcg. The site runtime that
# hosts libvirt publishes its own beside the socket; the adapter defines and
# starts a target only while the two agree, with no software fallback.
ACCEL = os.environ.get('FLEET_LAB_ACCEL', '')
RUNTIME_ACCELERATOR = os.environ.get('FLEET_LAB_RUNTIME_ACCELERATOR', '/run/fleet-libvirt/accelerator')
LOCK = threading.RLock()


def now():
    return datetime.datetime.now(datetime.timezone.utc).isoformat()


def atomic_write(path, value):
    fd, tmp = tempfile.mkstemp(dir=os.path.dirname(path), prefix='.tmp-')
    with os.fdopen(fd, 'w') as f:
        json.dump(value, f, indent=2, sort_keys=True)
        f.flush()
        os.fsync(f.fileno())
    os.replace(tmp, path)
    d = os.open(os.path.dirname(path), os.O_RDONLY)
    try:
        os.fsync(d)
    finally:
        os.close(d)


def read_json(path, default):
    try:
        with open(path) as f:
            return json.load(f)
    except FileNotFoundError:
        return default


class FaultFile:
    """One-shot lab faults, shared with the fault CLI through an flock."""

    def __init__(self, state):
        self.path = os.path.join(state, 'faults.json')
        self.lock = os.path.join(state, 'faults.lock')

    def update(self, fn):
        with open(self.lock, 'a') as l:
            fcntl.flock(l, fcntl.LOCK_EX)
            value = read_json(self.path, {})
            result = fn(value)
            atomic_write(self.path, value)
            return result

    def read(self):
        return read_json(self.path, {})


def virsh(*args, input=None):
    try:
        r = subprocess.run([VIRSH, '--connect', LIBVIRT_URI, *args], input=input, text=True, capture_output=True, timeout=180)
    except subprocess.TimeoutExpired:
        raise LibvirtError('virsh %s timed out' % args[0])
    if r.returncode:
        raise LibvirtError('virsh %s failed: %s' % (args[0], r.stderr.strip()[-300:]))
    return r.stdout.strip()


class LibvirtError(Exception):
    pass


def require_accelerator():
    if ACCEL not in ('kvm', 'tcg'):
        raise LibvirtError('FLEET_LAB_ACCEL must be kvm or tcg')
    try:
        published = open(RUNTIME_ACCELERATOR).read().strip()
    except OSError:
        published = ''
    if published != ACCEL:
        raise LibvirtError('the site runtime publishes accelerator %r, not %r' % (published, ACCEL))


def media_xml(media):
    return ''.join("<disk type='file' device='cdrom'><driver name='qemu' type='raw'/><source file='%s'/><target dev='%s' bus='ide'/><readonly/></disk>"
                   % (media[dev]['file'], target) for dev, target in MEDIA_DEVICES if media.get(dev, {}).get('Inserted'))


def domain_xml(t, boot, media=None):
    # Persistent lab target: fixed UUID, MAC, disk and console. Only the boot
    # device changes, from the Redfish boot override at power-on.
    return ("<domain type='" + ('kvm' if ACCEL == 'kvm' else 'qemu') + "'><name>{domain}</name><uuid>{uuid}</uuid><memory unit='MiB'>2048</memory><vcpu>2</vcpu>"
            "<cpu><topology sockets='1' cores='2' threads='1'/></cpu><features><acpi/><apic/></features>"
            "<os><type arch='x86_64' machine='pc'>hvm</type><boot dev='{boot}'/></os>"
            "<on_poweroff>destroy</on_poweroff><on_reboot>destroy</on_reboot><on_crash>destroy</on_crash>"
            "<devices><emulator>/usr/bin/qemu-system-x86_64</emulator>"
            "<disk type='file' device='disk'><driver name='qemu' type='raw' discard='unmap' detect_zeroes='unmap'/>"
            "<source file='{disk}'/><target dev='vda' bus='virtio'/></disk>"
            "<interface type='network'><source network='" + NETWORK + "'/><mac address='{mac}'/><model type='e1000'/></interface>"
            "<serial type='file'><source path='{console}' append='on'/><target port='0'/></serial>"
            + media_xml(media or {}).replace('{', '{{').replace('}', '}}') +
            "</devices></domain>").format(boot=boot, **t)


def network_xml(targets, boot_ip, dhcp=None):
    head = ("<network><name>" + NETWORK + "</name><forward mode='nat'/><bridge name='fleetpxe0' stp='off' delay='0'/><dns enable='no'/>"
            "<ip address='192.168.77.1' netmask='255.255.255.0'>")
    if (dhcp or LAB_DHCP) == 'synced':
        return head + "</ip></network>"
    if (dhcp or LAB_DHCP) != 'static':
        raise ValueError('FLEET_LAB_DHCP must be static or synced')
    hosts = ''.join("<host mac='%s' ip='%s'/>" % (t['mac'], t['ip']) for t in targets.values())
    ips = sorted(t['ip'] for t in targets.values())
    return head + ("<dhcp><range start='%s' end='%s'/>%s"
                   "<bootp file='http://%s:8080/pxe/boot.ipxe'/></dhcp></ip></network>") % (ips[0], ips[-1], hosts, boot_ip)


class Lab:
    """Exact System -> persistent libvirt domain mapping and BMC NVRAM."""

    def __init__(self, targets, state, boot_ip):
        self.targets, self.state, self.boot_ip = targets, state, boot_ip
        self.nvram_path = os.path.join(state, 'nvram.json')
        self.nvram = read_json(self.nvram_path, {})
        self.media_path = os.path.join(state, 'media.json')
        self.media = read_json(self.media_path, {})
        for sid in targets:
            self.nvram.setdefault(sid, {'BootSourceOverrideEnabled': 'Disabled', 'BootSourceOverrideTarget': 'None'})
            for dev, _ in MEDIA_DEVICES:
                self.media.setdefault(sid, {}).setdefault(dev, {'Inserted': False, 'Image': None, 'file': None})
        atomic_write(self.nvram_path, self.nvram)
        atomic_write(self.media_path, self.media)

    def ensure(self, sid):
        """Define lost libvirt objects (the runtime Pod's libvirt config is not durable)."""
        t = self.targets[sid]
        require_accelerator()
        try:
            info = virsh('net-info', NETWORK)
            if not re.search(r'^Active:\s+yes', info, re.M):
                virsh('net-start', NETWORK)
        except LibvirtError:
            virsh('net-define', '/dev/stdin', input=network_xml(self.targets, self.boot_ip))
            virsh('net-start', NETWORK)
        try:
            uuid = virsh('domuuid', t['domain'])
        except LibvirtError:
            virsh('define', '/dev/stdin', input=domain_xml(t, 'hd'))
            uuid = virsh('domuuid', t['domain'])
        if uuid != t['uuid']:
            raise LibvirtError('lab domain identity changed; refusing to control it')

    def power(self, sid):
        state = virsh('domstate', self.targets[sid]['domain'])
        if state in ('running', 'paused', 'idle', 'blocked'):
            return 'On'
        if state == 'in shutdown':
            return 'PoweringOff'
        return 'Off'

    def save(self):
        atomic_write(self.nvram_path, self.nvram)
        atomic_write(self.media_path, self.media)

    def power_on(self, sid):
        t, boot = self.targets[sid], 'hd'
        override = self.nvram[sid]
        if override['BootSourceOverrideEnabled'] == 'Once' and override['BootSourceOverrideTarget'] in ('Pxe', 'Hdd', 'Cd'):
            boot = {'Pxe': 'network', 'Hdd': 'hd', 'Cd': 'cdrom'}[override['BootSourceOverrideTarget']]
            # Once is consumed by this boot. Persisted before start, like NVRAM.
            self.nvram[sid] = {'BootSourceOverrideEnabled': 'Disabled', 'BootSourceOverrideTarget': 'None'}
            self.save()
        require_accelerator()
        virsh('define', '/dev/stdin', input=domain_xml(t, boot, self.media[sid]))
        virsh('start', t['domain'])
        return boot


def is_config(path):
    """Whether a fetched medium is a config medium."""
    with open(path, 'rb') as f:
        return f.read(15) == b'FLEET-CONFIG-V1'


def corrupt_token(path):
    """Replace a config medium's report token with another value of the
    same shape, in place. Returns False for any other medium."""
    with open(path, 'r+b') as f:
        data = f.read()
        m = re.search(rb'\ntoken=([0-9a-f]{64})\n', data)
        if not data.startswith(b'FLEET-CONFIG-V1') or not m:
            return False
        other = hashlib.sha256(m.group(1)).hexdigest().encode()
        f.seek(0)
        f.write(data[:m.start(1)] + other + data[m.end(1):])
    return True


def add_stray_source(path, address):
    """Add a stray source to a config medium, in place, within its padded
    size: the installed OS then posts its report, with the right token, from
    that address first. Returns False for any other medium."""
    if not re.fullmatch(r'[0-9]{1,3}(\.[0-9]{1,3}){3}', address):
        return False
    with open(path, 'r+b') as f:
        data = f.read()
        if not data.startswith(b'FLEET-CONFIG-V1') or b'\nend\n' not in data:
            return False
        text = data.rstrip(b'\x00').replace(b'\nend\n', b'\nstraysource=' + address.encode() + b'\nend\n')
        if len(text) > len(data):
            return False
        f.seek(0)
        f.write(text.ljust(len(data), b'\x00'))
    return True


def add_download(path):
    """Add a download line the operation never pinned to a config medium,
    in place, within its padded size. Returns False for any other medium."""
    with open(path, 'r+b') as f:
        data = f.read()
        if not data.startswith(b'FLEET-CONFIG-V1') or b'\nend\n' not in data:
            return False
        line = b'download=extension registry.example/extension@sha256:' + hashlib.sha256(b'unpinned').hexdigest().encode() + b'\n'
        text = data.rstrip(b'\x00').replace(b'\nend\n', b'\n' + line + b'end\n')
        if len(text) > len(data):
            return False
        f.seek(0)
        f.write(text.ljust(len(data), b'\x00'))
    return True


def fetch(url, path):
    """Fetch a medium the way a BMC does: HTTPS only, to a literal IPv4
    endpoint, verifying the lab media service's certificate. Returns the
    byte count."""
    u = urllib.parse.urlsplit(url)
    if u.scheme != 'https' or u.username or u.password or u.query or u.fragment:
        raise ValueError('media must be fetched over HTTPS without credentials or a query')
    ipaddress.IPv4Address(u.hostname)
    context = ssl.create_default_context(cafile=MEDIA_CA)
    tmp = path + '.part'
    with urllib.request.urlopen(url, context=context, timeout=25) as r, open(tmp, 'wb') as f:
        n = 0
        while True:
            chunk = r.read(1 << 20)
            if not chunk:
                break
            n += len(chunk)
            if n > MEDIA_LIMIT:
                raise ValueError('medium exceeds the size bound')
            f.write(chunk)
        f.flush()
        os.fsync(f.fileno())
    os.replace(tmp, path)
    return n


def error(status, message):
    return {'error': {'code': 'Base.1.0.GeneralError', 'message': message}}, status


def challenge():
    body, status = error(401, 'authentication required')
    return body, status, {'WWW-Authenticate': 'Basic realm="fleet-lab-bmc"'}


def create_app(targets, state, credentials, boot_ip):
    os.makedirs(state, exist_ok=True)
    os.chdir(CONFIG_DIR)
    sys.path.insert(0, UPSTREAM)
    import logging
    import g  # upstream module: Flask app and flask_restful API
    import emulator  # noqa: F401  upstream: loads resources and runs infragen populate on import
    from api_emulator.redfish import ComputerSystem_api as cs
    from api_emulator.redfish import ResetActionInfo_api as rai
    from flask import g as request_context, request
    logging.getLogger().setLevel(logging.WARNING)
    lab = Lab(targets, state, boot_ip)
    faults = FaultFile(state)
    audit_path = os.path.join(state, 'audit.log')

    def publish(sid, power=None):
        m = cs.members[sid]
        m['UUID'] = targets[sid]['uuid']
        m['Name'] = targets[sid]['name']
        m['Manufacturer'] = 'DMTF Redfish Interface Emulator (Fleet lab adapter)'
        m['Model'] = 'libvirt/QEMU lab target'
        m['VirtualMedia'] = {'@odata.id': '/redfish/v1/Systems/%s/VirtualMedia' % sid}
        m['Boot'] = dict(lab.nvram[sid], BootSourceOverrideMode='Legacy', **{
            'BootSourceOverrideTarget@Redfish.AllowableValues': list(BOOT_TARGETS),
            'BootSourceOverrideEnabled@Redfish.AllowableValues': list(BOOT_ENABLED)})
        if power:
            m['PowerState'] = power
        hidden = set(faults.read().get('hideResetTypes', []))
        rai.members[sid]['Parameters'][0]['AllowableValues'] = [v for v in RESET_TYPES if v not in hidden]

    for sid in targets:
        if sid not in cs.members or sid not in rai.members:
            raise SystemExit('populate did not create lab System ' + sid)
        publish(sid, 'Off')

    def authorized():
        header = request.headers.get('Authorization', '')
        if not header.startswith('Basic '):
            return False
        try:
            user, _, password = base64.b64decode(header[6:], validate=True).decode().partition(':')
        except ValueError:
            return False
        with open(os.path.join(credentials, 'username')) as f:
            want_user = f.read().strip()
        with open(os.path.join(credentials, 'password')) as f:
            want_password = f.read().strip()
        return hmac.compare_digest(user.encode(), want_user.encode()) & hmac.compare_digest(password.encode(), want_password.encode())

    def take(kind, match=None):
        def fn(value):
            if kind == 'rejectAuth':
                if value.get('rejectAuth', 0) > 0:
                    value['rejectAuth'] -= 1
                    return True
                return False
            for i, f in enumerate(value.get(kind, [])):
                if match(f):
                    if f.get('skip', 0) > 0:
                        f['skip'] -= 1  # let this matching request through unfaulted
                        return None
                    return value[kind].pop(i)
            return None
        return faults.update(fn)

    @g.app.before_request
    def guard():
        request_context.principal, request_context.detail = 'anonymous', {}
        path = request.path.rstrip('/') or '/'
        if path in ('/redfish', '/redfish/v1') and request.method == 'GET':
            return None  # the service root is unauthenticated (Redfish specification)
        if not path.startswith('/redfish/v1/') or path == '/redfish/v1/reset':
            return error(404, 'not served by this lab BMC')
        if not authorized():
            request_context.principal = 'rejected'
            return challenge()
        if take('rejectAuth'):
            request_context.principal, request_context.detail = 'rejected', {'fault': 'reject-auth'}
            return challenge()
        request_context.principal = 'authenticated'
        v = re.fullmatch(r'/redfish/v1/Systems/([^/]+)/VirtualMedia(?:/([^/]+)(/Actions/VirtualMedia\.(InsertMedia|EjectMedia))?)?', path)
        if v and v.group(1) in targets:
            return virtual_media(v.group(1), v.group(2), v.group(4))
        m = re.fullmatch(r'/redfish/v1/Systems/([^/]+)(/Actions/ComputerSystem\.Reset|/ResetActionInfo)?', path)
        if not m:
            return None
        sid, sub = m.group(1), m.group(2)
        if sid not in targets:
            return None
        try:
            with LOCK:
                lab.ensure(sid)
                publish(sid, lab.power(sid))
        except LibvirtError as e:
            return error(503, 'lab hardware unavailable: %s' % e)
        if sub is None and request.method == 'PATCH':
            body = request.get_json(force=True, silent=True)
            if not isinstance(body, dict) or 'Boot' not in body:
                return None  # upstream handles its own patchable properties
            return patch_boot(sid, body)
        if sub == '/Actions/ComputerSystem.Reset' and request.method == 'POST':
            return reset(sid)
        return None

    def patch_boot(sid, body):
        boot = body['Boot']
        if set(body) != {'Boot'} or not isinstance(boot, dict) or not set(boot) <= {'BootSourceOverrideTarget', 'BootSourceOverrideEnabled', 'BootSourceOverrideMode'}:
            return error(400, 'only Boot source override properties are patchable with Boot')
        current = dict(lab.nvram[sid])
        current.update({k: v for k, v in boot.items() if k != 'BootSourceOverrideMode'})
        if boot.get('BootSourceOverrideMode', 'Legacy') != 'Legacy' or current['BootSourceOverrideTarget'] not in BOOT_TARGETS or current['BootSourceOverrideEnabled'] not in BOOT_ENABLED:
            return error(400, 'unsupported boot override value')
        if current['BootSourceOverrideTarget'] == 'None':
            current['BootSourceOverrideEnabled'] = 'Disabled'
        with LOCK:
            lab.nvram[sid] = current
            lab.save()
            publish(sid)
        request_context.detail = {'boot': current}
        return cs.members[sid], 200

    def media_resource(sid, dev):
        state = lab.media[sid][dev]
        base = '/redfish/v1/Systems/%s/VirtualMedia/%s' % (sid, dev)
        return {'@odata.id': base, '@odata.type': '#VirtualMedia.v1_6_0.VirtualMedia', 'Id': dev, 'Name': 'Virtual ' + dev,
                'MediaTypes': ['CD', 'DVD'], 'Inserted': state['Inserted'], 'Image': state['Image'], 'WriteProtected': True,
                'TransferProtocolType': 'HTTPS' if state['Inserted'] else None, 'ConnectedVia': 'URI' if state['Inserted'] else 'NotConnected',
                'Actions': {'#VirtualMedia.InsertMedia': {'target': base + '/Actions/VirtualMedia.InsertMedia'},
                            '#VirtualMedia.EjectMedia': {'target': base + '/Actions/VirtualMedia.EjectMedia'}}}

    def virtual_media(sid, dev, action):
        devices = [d for d, _ in MEDIA_DEVICES]
        if dev is None:
            if request.method != 'GET':
                return error(405, 'method not allowed')
            base = '/redfish/v1/Systems/%s/VirtualMedia' % sid
            return {'@odata.id': base, '@odata.type': '#VirtualMediaCollection.VirtualMediaCollection', 'Name': 'Virtual media',
                    'Members': [{'@odata.id': base + '/' + d} for d in devices], 'Members@odata.count': len(devices)}, 200
        if dev not in devices:
            return error(404, 'no such virtual media device')
        if action is None:
            if request.method != 'GET':
                return error(405, 'method not allowed')
            return media_resource(sid, dev), 200
        if request.method != 'POST':
            return error(405, 'method not allowed')
        body = request.get_json(force=True, silent=True)
        if not isinstance(body, dict):
            return error(400, 'a JSON object is required')
        state = lab.media[sid][dev]
        if action == 'InsertMedia':
            if not set(body) <= {'Image', 'Inserted', 'WriteProtected', 'TransferProtocolType', 'TransferMethod'} or not isinstance(body.get('Image'), str) or body.get('Inserted', True) is not True:
                return error(400, 'unsupported InsertMedia parameters')
            if body.get('TransferProtocolType', 'HTTPS') != 'HTTPS':
                return error(400, 'only HTTPS media are accepted')
            if state['Inserted']:
                return error(400, 'a medium is already inserted')
            url = body['Image']
            path = os.path.join(MEDIA_DIR, '%s-%s.img' % (targets[sid]['domain'], dev))
            request_context.detail = {'media': dev, 'urlSHA256': hashlib.sha256(url.encode()).hexdigest()}
            try:
                n = fetch(url, path)
            except Exception as e:  # noqa: BLE001  the class of failure only, never the URL
                request_context.detail.update({'effect': 'fetch-failed', 'error': type(e).__name__})
                return error(400, 'the medium could not be fetched')
            if is_config(path) and take('corruptConfig', lambda f: f.get('system') == sid) and corrupt_token(path):
                # Lab fault: the medium the BMC holds carries another
                # report token than the one the media service served.
                request_context.detail['fault'] = 'corrupt-config-token'
            elif is_config(path) and take('addDownload', lambda f: f.get('system') == sid) and add_download(path):
                # Lab fault: the medium the BMC holds lists a download the
                # operation never pinned.
                request_context.detail['fault'] = 'add-config-download'
            elif is_config(path) and (stray := take('straySource', lambda f: f.get('system') == sid)) and add_stray_source(path, stray['address']):
                # Lab fault: another server in the site's ranges posts the
                # report with the right token before the Machine does.
                request_context.detail['fault'] = 'add-stray-source'
            with LOCK:
                lab.media[sid][dev] = {'Inserted': True, 'Image': url, 'file': path}
                lab.save()
            request_context.detail.update({'effect': 'inserted', 'bytes': n})
            return '', 204
        if set(body) - {'Inserted'}:
            return error(400, 'unsupported EjectMedia parameters')
        drop = take('dropEject', lambda f: f.get('system') == sid and f.get('device') == dev) if state['Inserted'] else None
        effect = 'none'
        if state['Inserted']:
            with LOCK:
                if state.get('file'):
                    try:
                        os.remove(state['file'])
                    except FileNotFoundError:
                        pass
                lab.media[sid][dev] = {'Inserted': False, 'Image': None, 'file': None}
                lab.save()
            effect = 'ejected'
        request_context.detail = {'media': dev, 'effect': effect}
        if drop:
            # Lab fault: the eject is applied, the response is withheld past the client timeout.
            request_context.detail['fault'] = 'drop-response-after-effect'
            time.sleep(min(int(drop.get('seconds', 45)), 120))
        return '', 204

    def reset(sid):
        body = request.get_json(force=True, silent=True)
        kind = body.get('ResetType') if isinstance(body, dict) and set(body) == {'ResetType'} else None
        allowed = [v for v in RESET_TYPES if v not in set(faults.read().get('hideResetTypes', []))]
        if kind not in allowed:
            return error(400, 'unsupported ResetType')
        domain = targets[sid]['domain']
        try:
            with LOCK:
                power = lab.power(sid)
                effect = 'none'
                if kind in ('ForceOff', 'ForceRestart') and power != 'Off':
                    virsh('destroy', domain)
                    effect = 'destroyed'
                if kind in ('On', 'ForceRestart') and (kind == 'ForceRestart' or power == 'Off'):
                    effect = 'started:' + lab.power_on(sid)
                publish(sid, lab.power(sid))
        except LibvirtError as e:
            return error(503, 'lab hardware unavailable: %s' % e)
        request_context.detail = {'resetType': kind, 'effect': effect}
        drop = take('dropResponse', lambda f: f.get('system') == sid and f.get('resetType') == kind) if effect.startswith('started') else None
        if drop:
            # Lab fault: the effect is applied, the response is withheld past the client timeout.
            request_context.detail['fault'] = 'drop-response-after-effect'
            time.sleep(min(int(drop.get('seconds', 45)), 120))
        return '', 204

    @g.app.after_request
    def audit(response):
        entry = {'at': now(), 'method': request.method, 'path': request.path, 'status': response.status_code,
                 'principal': getattr(request_context, 'principal', 'anonymous')}
        entry.update(getattr(request_context, 'detail', {}))
        with LOCK, open(audit_path, 'a') as f:
            f.write(json.dumps(entry, sort_keys=True) + '\n')
            f.flush()
            os.fsync(f.fileno())
        return response

    return g.app


def load_targets():
    with open(os.path.join(CONFIG_DIR, 'targets.json')) as f:
        return {t['system']: t for t in json.load(f)}


def fault_cli(args):
    faults = FaultFile(STATE)
    if args[:1] == ['drop-response'] and len(args) in (4, 5):
        skip = int(args[4]) if len(args) == 5 else 0
        faults.update(lambda v: v.setdefault('dropResponse', []).append({'system': args[1], 'resetType': args[2], 'seconds': int(args[3]), 'skip': skip}))
    elif args[:1] == ['drop-eject'] and len(args) == 4:
        faults.update(lambda v: v.setdefault('dropEject', []).append({'system': args[1], 'device': args[2], 'seconds': int(args[3])}))
    elif args[:1] == ['stray-source'] and len(args) == 3:
        faults.update(lambda v: v.setdefault('straySource', []).append({'system': args[1], 'address': args[2]}))
    elif args[:1] == ['add-download'] and len(args) == 2:
        faults.update(lambda v: v.setdefault('addDownload', []).append({'system': args[1]}))
    elif args[:1] == ['corrupt-config'] and len(args) == 2:
        faults.update(lambda v: v.setdefault('corruptConfig', []).append({'system': args[1]}))
    elif args[:1] == ['reject-auth'] and len(args) == 2:
        faults.update(lambda v: v.__setitem__('rejectAuth', int(args[1])))
    elif args[:1] == ['hide-reset'] and len(args) == 2:
        faults.update(lambda v: v.setdefault('hideResetTypes', []).append(args[1]))
    elif args == ['clear']:
        faults.update(lambda v: v.clear())
    elif args == ['show']:
        pass
    else:
        raise SystemExit('usage: fault drop-response SYSTEM RESETTYPE SECONDS [SKIP] | drop-eject SYSTEM DEVICE SECONDS | corrupt-config SYSTEM | add-download SYSTEM | reject-auth COUNT | hide-reset TYPE | clear | show')
    print(json.dumps(faults.read(), sort_keys=True))


def main():
    if sys.argv[1:2] == ['serve']:
        boot_ip = os.environ.get('FLEET_BOOT_IP', '')
        if not re.fullmatch(r'\d+\.\d+\.\d+\.\d+', boot_ip):
            raise SystemExit('FLEET_BOOT_IP must be the boot Service IPv4 address')
        if ACCEL not in ('kvm', 'tcg'):
            raise SystemExit('FLEET_LAB_ACCEL must be kvm or tcg')
        tls = (os.path.join(TLS_DIR, 'tls.crt'), os.path.join(TLS_DIR, 'tls.key'))
        create_app(load_targets(), STATE, CREDENTIALS, boot_ip).run(host='0.0.0.0', port=PORT, threaded=True, ssl_context=tls)
    elif sys.argv[1:2] == ['fault']:
        fault_cli(sys.argv[2:])
    else:
        raise SystemExit('usage: fleet_lab_adapter.py serve | fault ...')


if __name__ == '__main__':
    main()
