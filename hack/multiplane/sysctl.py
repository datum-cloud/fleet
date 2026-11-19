#!/usr/bin/env python3
"""Reversible, explicitly opted-in inotify adjustment in the task Colima VM (or the disposable CI runner)."""
import json,os,pathlib,subprocess,sys
sys.path.insert(0,os.path.dirname(os.path.abspath(__file__)))
import harness
s=pathlib.Path(sys.argv[1]);action=sys.argv[2];prefix=(s/'prefix').read_text().strip()
os.environ['DOCKER_CONTEXT']=harness.CONTEXT
def run(*args):return subprocess.check_output(args,text=True).strip()
endpoint=run('docker','context','inspect',harness.CONTEXT,'--format','{{.Endpoints.docker.Host}}')
if not harness.endpoint_matches(0,endpoint):raise SystemExit('unexpected Docker runtime endpoint')
node=prefix+'-hub-control-plane';current=int(run('docker','exec',node,'cat','/proc/sys/fs/inotify/max_user_instances'))
if action=='set':
 if (s/'sysctl.json').exists():raise SystemExit('sysctl evidence already exists; do not overwrite original value')
 if current<1024 and os.environ.get('FLEET_MULTIPLANE_INOTIFY')!='1024':raise SystemExit('Requires fs.inotify.max_user_instances>=1024. Opt into reversible task-VM adjustment with FLEET_MULTIPLANE_INOTIFY=1024')
 record={'context':harness.CONTEXT,'setting':'fs.inotify.max_user_instances','original':current,'effective':max(current,1024)}
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
