#!/usr/bin/env python3
"""Explicit test-only hub status fault injection; no simulated federation relay."""
import copy,json,pathlib,subprocess,sys
s=pathlib.Path(sys.argv[1]);hub=s/'hub-admin.yaml';host=s/'host.yaml'
def k(config,*args,input=None):return subprocess.run(['kubectl','--kubeconfig',str(config),'--request-timeout=20s',*args],input=input,text=True,capture_output=True,check=True).stdout
items=json.loads(k(hub,'-n','fleet-coordination','get','machineallocationhandoffs','-o','json'))['items']
if len(items)!=1:raise SystemExit('run reflection faults after install and before cleanup')
obj=items[0];original=obj.get('status');name=obj['metadata']['name']
if len((original or {}).get('reflected',[]))!=1:raise SystemExit('no real reflected result to test')
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
try:
 # Each mode changes exactly one attribute of the real reflected result.
 for mode in ('wrong-cell','stale','duplicate','future','wrong-intent','wrong-digest','wrong-operation','incomplete','missing'):
  status=copy.deepcopy(original);o=status['reflected'][0]['observation']
  if mode=='wrong-cell':status['reflected'][0]['clusterName']='wrong-cell'
  elif mode=='stale':o['observedAt']='2000-01-01T00:00:00Z'
  elif mode=='duplicate':status['reflected'].append(copy.deepcopy(status['reflected'][0]))
  elif mode=='future':o['observedAt']='2999-01-01T00:00:00Z'
  elif mode=='wrong-intent':o['intentDigest']='0'*64
  elif mode=='wrong-digest':o['result']['inputDigest']='0'*64
  elif mode=='wrong-operation':o['result']['id']=o['result']['id']+'-other'
  elif mode=='incomplete':o['result']['phase']='Unknown'
  else:status={'reflected':[]}
  guard('check')
  patch(status)
  result=subprocess.run([str(s/'fleet-multiplane-host'),'install','--once','--config',str(s/'acceptance.json')],text=True,capture_output=True)
  if result.returncode==0:raise SystemExit(mode+' incorrectly accepted')
  if 'unknown' not in result.stderr.lower():raise SystemExit(mode+' did not reach result validation: '+result.stderr[:200])
  print(mode+': management rejected reflected evidence')
finally:
 guard('check')
 patch(original)
 k(host,'-n','karmada-system','scale','deployment/karmada-controller-manager','--replicas=1')
 k(host,'-n','flux-system','patch','helmrelease/karmada','--field-manager=fleet-fault-injection','--type=merge','-p','{"spec":{"suspend":false}}')
 k(host,'-n','karmada-system','wait','--for=condition=Available','deployment/karmada-controller-manager','--timeout=180s')
