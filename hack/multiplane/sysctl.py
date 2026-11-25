#!/usr/bin/env python3
"""Reversible, explicitly opted-in inotify adjustment in the task Colima VM."""
import json,os,pathlib,subprocess,sys
s=pathlib.Path(sys.argv[1]);action=sys.argv[2];prefix=(s/'prefix').read_text().strip()
os.environ['DOCKER_CONTEXT']='colima-fleet-acceptance-tcg'
def run(*args):return subprocess.check_output(args,text=True).strip()
endpoint=run('docker','context','inspect','colima-fleet-acceptance-tcg','--format','{{.Endpoints.docker.Host}}')
if not endpoint.endswith('/.colima/fleet-acceptance-tcg/docker.sock'):raise SystemExit('unexpected Colima profile')
node=prefix+'-hub-control-plane';current=int(run('docker','exec',node,'cat','/proc/sys/fs/inotify/max_user_instances'))
if action=='resume':
 # A resumed run: when the limit was restored (or never set), set it again
 # with a fresh record, keeping the earlier one. Unrestored, as after
 # --leave-running, the earlier record still holds the original value.
 if (s/'sysctl.json').exists() and not json.loads((s/'sysctl.json').read_text()).get('restored'):
  print(json.dumps({'kept':'earlier record, not restored'}));raise SystemExit(0)
 if (s/'sysctl.json').exists():(s/'sysctl.json').rename(s/('sysctl-before-resume-%d.json'%len(list(s.glob('sysctl-before-resume-*.json')))))
 action='set'
if action=='set':
 if (s/'sysctl.json').exists():raise SystemExit('sysctl evidence already exists; do not overwrite original value')
 if current<1024 and os.environ.get('FLEET_MULTIPLANE_INOTIFY')!='1024':raise SystemExit('Requires fs.inotify.max_user_instances>=1024. Opt into reversible task-VM adjustment with FLEET_MULTIPLANE_INOTIFY=1024')
 record={'context':'colima-fleet-acceptance-tcg','setting':'fs.inotify.max_user_instances','original':current,'effective':max(current,1024)}
 (s/'sysctl.json').write_text(json.dumps(record,indent=2))
 if current<1024:run('docker','exec',node,'sysctl','-w','fs.inotify.max_user_instances=1024')
 record['observedEffective']=int(run('docker','exec',node,'cat','/proc/sys/fs/inotify/max_user_instances'))
 (s/'sysctl.json').write_text(json.dumps(record,indent=2))
 if record['observedEffective']!=record['effective']:raise SystemExit('inotify adjustment readback mismatch; inspect sysctl.json')
elif action=='restore':
 record=json.loads((s/'sysctl.json').read_text())
 record.pop('restored',None);record.pop('restoreSkipped',None)
 if current==record['effective'] and current!=record['original']:
  run('docker','exec',node,'sysctl','-w','fs.inotify.max_user_instances='+str(record['original']));record['restored']=True
 elif current!=record['effective']:record['restoreSkipped']='value changed by another owner'
 else:record['restored']=True
 record['observedAfterRestore']=int(run('docker','exec',node,'cat','/proc/sys/fs/inotify/max_user_instances'))
 (s/'sysctl.json').write_text(json.dumps(record,indent=2))
 if record.get('restored') and record['observedAfterRestore']!=record['original']:raise SystemExit('inotify restoration readback mismatch; inspect sysctl.json')
else:raise SystemExit('expected set or restore')
print(json.dumps(record))
