#!/usr/bin/env python3
import datetime
import http.server
import json
import pathlib

LOG = pathlib.Path('/evidence/pxe-callbacks.jsonl')
BOOT_DIR = pathlib.Path('/var/lib/libvirt/dnsmasq/tftp')

class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        entry = {'time': datetime.datetime.now(datetime.timezone.utc).isoformat(),
                 'path': self.path, 'client': self.client_address[0]}
        with LOG.open('a', encoding='utf-8') as out:
            out.write(json.dumps(entry) + '\n')
            out.flush()
        if self.path == '/boot.ipxe':
            body = (b'#!ipxe\n'
                    b'kernel http://192.168.124.1:8080/vmlinuz console=ttyS0 ip=dhcp\n'
                    b'initrd http://192.168.124.1:8080/initramfs.gz\n'
                    b'boot\n')
            self.send_response(200)
            self.send_header('Content-Type', 'text/plain')
            self.send_header('Content-Length', str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        if self.path in ('/vmlinuz', '/initramfs.gz'):
            body = (BOOT_DIR / self.path.removeprefix('/')).read_bytes()
            self.send_response(200)
            self.send_header('Content-Type', 'application/octet-stream')
            self.send_header('Content-Length', str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        if not self.path.startswith('/pxe/'):
            self.send_error(404)
            return
        self.send_response(204)
        self.end_headers()
    def log_message(self, *_):
        pass

http.server.ThreadingHTTPServer(('192.168.124.1', 8080), Handler).serve_forever()
