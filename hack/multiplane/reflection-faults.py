#!/usr/bin/env python3
"""Explicit test-only hub status fault injection; no simulated federation relay.

The install's result is already accepted, so the coordinator answered its
handoff and the cell deleted its copy. The completed commissioning's handoff
is ignored; every other assignment counts. With Karmada stopped, the harness
reopens that handoff (removes its answered label) and reflects one forged
result at a time, each wrong in exactly one attribute. The custody
coordinator must ignore every one: custody stays unchanged and the handoff
stays unanswered. The label and the real status are restored afterwards.
"""
import copy,datetime,json,pathlib,subprocess,sys,time
sys.path.insert(0,str(pathlib.Path(__file__).resolve().parent))
import harness  # noqa: E402  (provider custody digest)
import k8s_evidence  # noqa: E402  (the commissioning's recorded handoff)
s=pathlib.Path(sys.argv[1]);hub=s/'hub-admin.yaml';host=s/'host.yaml'
ANSWERED='coordination.fleet.datumapis.com/answered'
def k(config,*args,input=None):return subprocess.run(['kubectl','--kubeconfig',str(config),'--request-timeout=20s',*args],input=input,text=True,capture_output=True,check=True).stdout
def handoff():return json.loads(k(hub,'-n','fleet-coordination','get','machineallocationhandoff',name,'-o','json'))
items=json.loads(k(hub,'-n','fleet-coordination','get','machineallocationhandoffs','-l','coordination.fleet.datumapis.com/role=assignment','-o','json'))['items']
done=k8s_evidence.commissioned(s);installs=k8s_evidence.install_assignments(items,done)
if len(installs)!=1:raise SystemExit('run reflection faults after install and before the release wipe')
obj=next(h for h in items if h['metadata']['name']==installs[0]);original=obj.get('status');name=obj['metadata']['name']
if obj['metadata'].get('labels',{}).get(ANSWERED)!='true':raise SystemExit('the install result is not answered yet')
if len((original or {}).get('reflected',[]))!=1:raise SystemExit('no real reflected observation to test')
assignment=json.loads(obj['spec']['assignmentJSON'])
# Explicit ownership transfer: suspend the Flux HelmRelease before stopping the
# controller, so Flux and the harness are never concurrent writers.
k(host,'-n','flux-system','patch','helmrelease/karmada','--field-manager=fleet-fault-injection','--type=merge','-p','{"spec":{"suspend":true}}')
k(host,'-n','karmada-system','scale','deployment/karmada-controller-manager','--replicas=0')
k(host,'-n','karmada-system','wait','--for=delete','pod','-l','app=karmada-controller-manager','--timeout=90s')
def guard(mode):
 subprocess.run([sys.executable,str(pathlib.Path(__file__).with_name('fault_guard.py')),mode,str(s),'host.yaml','helmrelease','karmada','karmada-system','karmada-controller-manager','flux-reflection-suspension'],check=True)
guard('record')
def patch(status):
 p=s/'status-fault.json';p.write_text(json.dumps({'status':status}));p.chmod(0o600)
 k(hub,'-n','fleet-coordination','patch','machineallocationhandoff',name,'--subresource=status','--type=merge','--patch-file',str(p))
before=harness.custody_digest(s)
try:
 k(hub,'-n','fleet-coordination','label','machineallocationhandoff',name,ANSWERED+'-')
 # A result the coordinator would otherwise accept for exactly this assignment.
 base=copy.deepcopy(original);o=base['reflected'][0]['observation']
 o['observedAt']=datetime.datetime.now(datetime.timezone.utc).strftime('%Y-%m-%dT%H:%M:%SZ')
 o['result']={'site':o['cell'],'device':assignment['device'].split('/',1)[1],'operation':assignment['operation'],'fence':assignment['fence'],
  'phase':'Failed','reason':'FaultInjection','message':'forged by the acceptance harness'}
 for mode in ('wrong-cell','foreign-observer','duplicate','future','wrong-assignment','wrong-device','wrong-operation','stale-fence','missing'):
  status=copy.deepcopy(base);o=status['reflected'][0]['observation'];result=o['result']
  if mode=='wrong-cell':status['reflected'][0]['clusterName']='wrong-cell'
  elif mode=='foreign-observer':o['cell']='wrong-cell'
  elif mode=='duplicate':status['reflected'].append(copy.deepcopy(status['reflected'][0]))
  elif mode=='future':o['observedAt']='2999-01-01T00:00:00Z'
  elif mode=='wrong-assignment':o['assignmentDigest']='0'*64
  elif mode=='wrong-device':result['device']=result['device']+'-other'
  elif mode=='wrong-operation':result['operation']=dict(result['operation'],uid=result['operation']['uid']+'-other')
  elif mode=='stale-fence':result['fence']=result['fence']-1
  else:status={'reflected':[]}
  guard('check')
  patch(status)
  # The coordinator passes every second; give it several passes.
  time.sleep(8)
  if handoff()['metadata'].get('labels',{}).get(ANSWERED)=='true':raise SystemExit(mode+' was answered by the coordinator')
  if harness.custody_digest(s)!=before:raise SystemExit(mode+' changed custody')
  print(mode+': management rejected reflected evidence')
finally:
 guard('check')
 patch(original)
 k(hub,'-n','fleet-coordination','label','--overwrite','machineallocationhandoff',name,ANSWERED+'=true')
 k(host,'-n','karmada-system','scale','deployment/karmada-controller-manager','--replicas=1')
 k(host,'-n','flux-system','patch','helmrelease/karmada','--field-manager=fleet-fault-injection','--type=merge','-p','{"spec":{"suspend":false}}')
 k(host,'-n','karmada-system','wait','--for=condition=Available','deployment/karmada-controller-manager','--timeout=180s')
