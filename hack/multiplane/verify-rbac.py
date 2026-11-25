#!/usr/bin/env python3
"""Negative writes and credential revocation against real API servers."""
import json,os,pathlib,subprocess,sys,time
s=pathlib.Path(sys.argv[1]);ns='fleet-coordination'
def call(config,*args,input=None):return subprocess.run(['kubectl','--kubeconfig',str(config),'--request-timeout=15s',*args],input=input,text=True,capture_output=True)
def forbidden(config,*args):
 r=call(config,*args)
 if r.returncode==0 or 'forbidden' not in r.stderr.lower():raise SystemExit('Expected Forbidden for '+config.name+' '+args[0]+': '+r.stderr[:200])
# The site observer is the only cell identity that writes handoff status.
token=call(s/'cell-admin.yaml','-n','fleet-site','create','token','fleet-site-observer','--duration=10m')
if token.returncode:raise SystemExit(token.stderr)
admin=json.loads(call(s/'cell-admin.yaml','config','view','--raw','--minify','-o','json').stdout)
admin['users']=[{'name':'observer','user':{'token':token.stdout.strip()}}];admin['contexts'][0]['context']['user']='observer'
local=s/'observer-kubeconfig.json';local.write_text(json.dumps(admin));local.chmod(0o600)
try:
 r=call(local,'-n',ns,'get','machineallocationhandoffs')
 if r.returncode:raise SystemExit('observer cannot list delivered intent: '+r.stderr[:200])
 forbidden(local,'-n',ns,'create','configmap','forbidden-write','--from-literal=x=y')
 forbidden(local,'-n',ns,'patch','machineallocationhandoff','forbidden-write','--type=merge','-p','{"spec":{"cell":"other"}}')
 forbidden(local,'-n',ns,'delete','machineallocationhandoff','forbidden-write')
 forbidden(local,'-n',ns,'get','secrets')
 forbidden(local,'-n','fleet-site','create','job','forbidden','--image=x')
 forbidden(s/'management-hub.json','-n',ns,'patch','machineallocationhandoff','forbidden-write','--subresource=status','--type=merge','-p','{"status":{}}')
 for name in ('consumer-a','consumer-b'):
  forbidden(s/name/'customer-project.json','-n','default','patch','machine','allocation','--subresource=status','--type=merge','-p','{"status":{"phase":"Ready"}}')
  forbidden(s/name/'customer-project.json','-n','default','get','secrets')
 # Revoke the actual observer RoleBinding and prove denied reads. The harness
 # never restores it: the cell's Flux recreates it from the source.
 old=json.loads(call(s/'cell-admin.yaml','-n',ns,'get','rolebinding','fleet-site-observer','-o','json').stdout)['metadata']['uid']
 r=call(s/'cell-admin.yaml','-n',ns,'delete','rolebinding','fleet-site-observer')
 if r.returncode:raise SystemExit(r.stderr)
 forbidden(local,'-n',ns,'get','machineallocationhandoffs')
 r=call(s/'cell-admin.yaml','-n','flux-system','annotate','--overwrite','kustomization','fleet-site-base','reconcile.fluxcd.io/requestedAt=%f'%time.time())
 if r.returncode:raise SystemExit(r.stderr)
 for _ in range(150):
  b=call(s/'cell-admin.yaml','-n',ns,'get','rolebinding','fleet-site-observer','-o','json')
  if b.returncode==0 and call(local,'-n',ns,'get','machineallocationhandoffs').returncode==0:break
  time.sleep(2)
 else:raise SystemExit('Flux did not restore the revoked observer RoleBinding')
 binding=json.loads(b.stdout)
 if binding['metadata']['uid']==old or binding['metadata'].get('labels',{}).get('kustomize.toolkit.fluxcd.io/name')!='fleet-site-base':
  raise SystemExit('observer RoleBinding was not recreated by Flux')
 (s/'evidence'/'flux-rbac-restore.json').write_text(json.dumps({'object':'fleet-coordination/RoleBinding/fleet-site-observer','revokedUID':old,'restoredUID':binding['metadata']['uid'],
  'restoredBy':'kustomize-controller (Kustomization fleet-site-base)','deniedWhileRevoked':True,'observerReadsRestored':True},indent=2)+'\n')
finally:
 local.unlink(missing_ok=True)
print('Verified forbidden observer/project/hub writes and observer RoleBinding revocation; Flux restored the binding')
# kro's narrowed authority: cluster-wide reads, writes only of per-operation
# kinds in their namespaces, and admission policies that bind those writes to
# the worker shape. Checked as kro's own identity through impersonation;
# admission is exercised with server-side dry runs, so nothing is persisted.
kro='system:serviceaccount:kro-system:kro';site='fleet-site';h='machineallocationhandoffs.coordination.fleet.datumapis.com'
def can(*args):
 r=call(s/'cell-admin.yaml','auth','can-i',*args,'--as',kro)
 if r.stdout.strip() not in ('yes','no'):raise SystemExit('auth can-i failed: '+r.stderr[:200])
 return r.stdout.strip()=='yes'
allowed=[('create','jobs','-n',site),('create','serviceaccounts','-n',site),('create','roles','-n',ns),('create','rolebindings','-n',ns),
 ('list','deployments','-A'),('list','services','-A'),('watch',h,'-A'),('get','persistentvolumeclaims','-n',site)]
redfish=os.environ.get('FLEET_SITE_PROVIDER')=='redfish';creds_ns='fleet-site-credentials'
if redfish:
 # Redfish mode: kro creates each worker's Role to get its one BMC credential
 # Secret, so it holds get there and nothing more; it cannot list or watch.
 allowed+=[('create','roles','-n',creds_ns),('create','rolebindings','-n',creds_ns),('get','secrets','-n',creds_ns)]
denied=[('create','deployments','-n',site),('patch','deployments','-n',site),('create','services','-n',site),('delete','services','-n',site),
 ('delete','persistentvolumeclaims','-n',site),('patch','configmaps','-n',site),('create','jobs','-n','default'),('create','jobs','-n','kube-system'),
 ('create','roles','-n',site),('create','rolebindings','-n','kube-system'),('create','clusterroles'),('get','secrets','-n',site),
 ('patch',h,'-n',ns),('update',h,'--subresource=status','-n',ns),('delete','validatingadmissionpolicies')]+(
 [('list','secrets','-n',creds_ns),('create','secrets','-n',creds_ns),('watch','secrets','-n',creds_ns)] if redfish else [])
bad=[' '.join(a) for a in allowed if not can(*a)]+['denied '+' '.join(a) for a in denied if can(*a)]
if bad:raise SystemExit('kro RBAC is not the narrowed grant: '+'; '.join(bad))
# Grants the kro chart keeps because kro needs them: it creates the CRD for each
# graph's kind, and elects a leader. Recorded as remaining authority, not hidden.
retained=[('create','customresourcedefinitions'),('delete','customresourcedefinitions'),('update','leases','-n','kro-system')]
retained={' '.join(a):can(*a) for a in retained}
image=json.loads((s/'gitops-environment.json').read_text())['siteImage']
def job(**change):
 c={'name':'worker','image':image,'command':['fleet-multiplane','site-worker'],'securityContext':{'runAsUser':0,'allowPrivilegeEscalation':False,'readOnlyRootFilesystem':True,'capabilities':{'drop':['ALL']}}}
 if redfish:c['env']=[{'name':'FLEET_SITE_PROVIDER','value':'redfish'}]
 spec={'restartPolicy':'Never','serviceAccountName':'w-op-admission-probe','containers':[c],'volumes':[{'name':'state','persistentVolumeClaim':{'claimName':'fleet-site-state'}},{'name':'tmp','emptyDir':{}}]}
 for k,v in change.items():
  (c if k in ('image','securityContext','env','volumeMounts') else spec)[k]=v
 return {'apiVersion':'batch/v1','kind':'Job','metadata':{'name':'w-op-admission-probe','namespace':site,'labels':{'fleet.datumapis.com/handoff':'op-admission-probe'}},'spec':{'template':{'spec':spec}}}
role=lambda rules,namespace=ns:{'apiVersion':'rbac.authorization.k8s.io/v1','kind':'Role','metadata':{'name':'w-op-admission-probe','namespace':namespace},'rules':rules}
own=[{'apiGroups':['coordination.fleet.datumapis.com'],'resources':['machineallocationhandoffs'],'resourceNames':['op-admission-probe'],'verbs':['get']}]
# RBAC resolves a binding's Role before admission runs, so the probe binds an
# existing Role that kro fully holds; a dry run cannot create w-<handoff> first.
binding=lambda name,sa,ref,namespace=ns:{'apiVersion':'rbac.authorization.k8s.io/v1','kind':'RoleBinding','metadata':{'name':name,'namespace':namespace},
 'subjects':[{'kind':'ServiceAccount','name':sa,'namespace':site}],'roleRef':{'apiGroup':'rbac.authorization.k8s.io','kind':'Role','name':ref}}
sa=lambda name:{'apiVersion':'v1','kind':'ServiceAccount','metadata':{'name':name,'namespace':site}}
cases={'admitted':{'worker Job':job(),'worker Role':role(own),'worker ServiceAccount':sa('w-op-admission-probe')},
 'denied':{'Job as the observer':job(serviceAccountName='fleet-site-observer'),'privileged Job':job(securityContext={'privileged':True,'allowPrivilegeEscalation':True}),
  'Job from another image':job(image='registry.k8s.io/pause:3.10'),'Job with a hostPath volume':job(volumes=[{'name':'host','hostPath':{'path':'/'}}]),
  'Job on the host network':job(hostNetwork=True),'Role listing every handoff':role([dict(own[0],resourceNames=[],verbs=['get','list'])]),
  'RoleBinding to another Role, for the observer':binding('w-op-admission-probe','fleet-site-observer','fleet-site-kro-worker-rbac'),'ServiceAccount outside the worker set':sa('fleet-site-probe')}}
if redfish:
 # Redfish workers have no hypervisor path: the libvirt socket, the lab disks
 # and a libvirt URI are denied by policy. They mount no credential: each gets
 # its Device's credential Secret, by name, through a per-operation Role.
 named=[{'apiGroups':[''],'resources':['secrets'],'resourceNames':['bmc-'+'0'*20],'verbs':['get']}]
 listing=[{'apiGroups':[''],'resources':['secrets'],'verbs':['list']}]
 cases['admitted'].update({'Redfish worker Job':job(),'worker credential Role':role(named,creds_ns)})
 cases['denied'].update({'Redfish worker mounting the libvirt socket':job(volumeMounts=[{'name':'state','mountPath':'/run/fleet-libvirt','subPath':'libvirt-socket'}]),
  'Redfish worker mounting the lab disks':job(volumeMounts=[{'name':'state','mountPath':'/var/lib/fleet-lab/disks','subPath':'lab-disks'}]),
  'Redfish worker with a libvirt URI':job(env=[{'name':'FLEET_SITE_PROVIDER','value':'redfish'},{'name':'FLEET_LIBVIRT_URI','value':'qemu:///system?socket=/run/fleet-libvirt/libvirt-sock'}]),
  'worker selecting the libvirt provider':job(env=[{'name':'FLEET_SITE_PROVIDER','value':'libvirt'}]),
  'Redfish worker mounting a credential Secret':job(volumes=[{'name':'state','persistentVolumeClaim':{'claimName':'fleet-site-state'}},{'name':'credentials','secret':{'secretName':'bmc-synthetic-device-central-01'}}]),
  # Shapes within kro's own grant, so the policy, not RBAC escalation, denies them.
  'credential Role with a second rule':role(named+own,creds_ns),
  'credential Role listing Secrets':role(listing,creds_ns),
  'credential Role getting every Secret':role([dict(named[0],resourceNames=[])],creds_ns),
  'credential Role getting two Secrets':role([dict(named[0],resourceNames=['bmc-'+'0'*20,'bmc-'+'1'*20])],creds_ns),
  'credential Role naming a Secret outside the rule':role([dict(named[0],resourceNames=['fleet-site-bmc-credentials'])],creds_ns),
  'credential RoleBinding to another Role':binding('w-op-admission-probe','w-op-admission-probe','fleet-site-kro-worker-credentials',creds_ns)})
record={'identity':kro,'allowed':[' '.join(a) for a in allowed],'denied':[' '.join(a) for a in denied],'chartGrantsRetained':retained,'admission':{}}
for outcome,objs in cases.items():
 for name,obj in objs.items():
  r=call(s/'cell-admin.yaml','create','--dry-run=server','--as',kro,'-f','-',input=json.dumps(obj))
  ok=r.returncode==0
  by_policy='ValidatingAdmissionPolicy' in r.stderr and 'fleet-site-kro-worker-' in r.stderr
  if (outcome=='admitted')!=ok or (not ok and not by_policy and 'forbidden' not in r.stderr.lower()):
   raise SystemExit('kro admission %s for %s: %s'%('denied' if not ok else 'admitted',name,r.stderr[:300]))
  record['admission'][name]={'expected':outcome,'admitted':ok,'deniedBy':None if ok else ('ValidatingAdmissionPolicy' if by_policy else 'RBAC')}
if any(v['deniedBy']!='ValidatingAdmissionPolicy' for v in record['admission'].values() if not v['admitted']):
 raise SystemExit('expected the admission policies, not RBAC, to deny in-namespace shape violations: %s'%record['admission'])
(s/'evidence'/'kro-rbac.json').write_text(json.dumps(record,indent=2,sort_keys=True)+'\n')
print('Verified kro RBAC and admission confine it to per-operation objects: %d allowed, %d denied verbs; %d shapes admitted, %d denied by policy'%(
 len(allowed),len(denied),len(cases['admitted']),len(cases['denied'])))
