#!/usr/bin/env python3
"""Offline stand-in for virsh used only by test_adapter.py: records calls, keeps state in JSON."""
import json, os, re, sys
state_path = os.environ['FAKE_VIRSH_STATE']
s = json.load(open(state_path)) if os.path.exists(state_path) else {'domains': {}, 'networks': {}, 'calls': []}
args = sys.argv[3:]  # drop --connect URI
s['calls'].append(args[0])
def done(out='', code=0):
    json.dump(s, open(state_path, 'w'))
    print(out); sys.exit(code)
cmd = args[0]
if os.environ.get('FAKE_VIRSH_DOWN'):
    print('cannot connect', file=sys.stderr); done(code=1)
if cmd == 'net-info':
    n = s['networks'].get(args[1])
    done('Active:         %s' % ('yes' if n and n['active'] else 'no')) if n else done(code=1)
if cmd == 'net-define':
    s['networks'][re.search(r'<name>([^<]+)', sys.stdin.read()).group(1)] = {'active': False}; done()
if cmd == 'net-start':
    s['networks'][args[1]]['active'] = True; done()
if cmd == 'define':
    xml = sys.stdin.read(); name = re.search(r'<name>([^<]+)', xml).group(1)
    d = s['domains'].setdefault(name, {'running': False, 'starts': []})
    d['uuid'] = re.search(r'<uuid>([^<]+)', xml).group(1); d['boot'] = re.search(r"<boot dev='([^']+)'", xml).group(1); done()
if cmd == 'domuuid':
    d = s['domains'].get(args[1]); done(d['uuid']) if d else done(code=1)
if cmd == 'domstate':
    done('running' if s['domains'][args[1]]['running'] else 'shut off')
if cmd == 'start':
    d = s['domains'][args[1]]; d['running'] = True; d['starts'].append(d['boot']); done()
if cmd == 'destroy':
    s['domains'][args[1]]['running'] = False; done()
done(code=2)
