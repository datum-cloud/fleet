#!/usr/bin/env python3
"""Enroll credentials for, then seed, the Flux-managed Milo v0.32.5 project APIs.

Flux on the platform cluster owns each Milo API's Deployment, etcd StatefulSet,
volume and Service (config/multiplane/milo). This script does only what a
source revision must never hold:

- credential enrollment: per-API static tokens and a self-signed serving key,
  written as the milo-apiserver-private Secret (local credential seam);
- tenant fixture bootstrap through each API: the Project, static-token RBAC
  grants and the metal CRDs inside the project, and in the provider project
  the inventory and Device custody CRDs. The provider project's Fleet
  components each get their own token: the Metal provider (management), the
  custody coordinator, the release controller and fixture discovery.
  Production uses Milo's own project provisioning, service catalog and IAM;
  this is not that machinery.
"""
import argparse, base64, json, os, pathlib, secrets, subprocess, time
p=argparse.ArgumentParser();p.add_argument('state');p.add_argument('prefix');a=p.parse_args()
root=pathlib.Path(a.state).resolve();root.mkdir(mode=0o700,parents=True,exist_ok=True)
if not a.prefix.startswith('fleet-mp-'):raise SystemExit('task-owned fleet-mp- prefix required')
os.environ['DOCKER_CONTEXT']='colima-fleet-acceptance-tcg'
platform=root/'platform-admin.yaml'
ports=json.loads((root/'platform-ports.json').read_text())
def run(*args,**kw):return subprocess.run(args,check=True,text=True,**kw)
def kubectl(config,*args,**kw):return run('kubectl','--kubeconfig',str(config),'--request-timeout=15s',*args,**kw)
def apply(config,obj):kubectl(config,'apply','-f','-',input=json.dumps(obj),stdout=subprocess.DEVNULL)
def kube(path,server,token,ca):
 path.write_text(json.dumps({'apiVersion':'v1','kind':'Config','clusters':[{'name':'local','cluster':{'server':server,'certificate-authority-data':base64.b64encode(ca).decode()}}],'users':[{'name':'role','user':{'token':token}}],'contexts':[{'name':'role','context':{'cluster':'local','user':'role'}}],'current-context':'role'}));path.chmod(0o600)
def wait(fn,seconds,what):
 for _ in range(seconds//2):
  if fn():return
  time.sleep(2)
 raise SystemExit(what)
roles=('provider','consumer-a','consumer-b')
# 1. Credential enrollment into the Flux-created namespaces.
for role in roles:
 d=root/role;ns='milo-'+role
 if not d.exists():
  d.mkdir(mode=0o700)
  who=('bootstrap','management','customer')+(('coordinator','release','discovery') if role=='provider' else ())
  tokens={r:secrets.token_hex(32) for r in who}
  (d/'tokens.csv').write_text('\n'.join(f'{v},{k},{k},'+('system:masters' if k=='bootstrap' else 'fleet-local') for k,v in tokens.items())+'\n')
  run('openssl','req','-x509','-newkey','rsa:2048','-nodes','-keyout',str(d/'tls.key'),'-out',str(d/'tls.crt'),'-days','2','-subj','/CN=fleet-local','-addext',f'subjectAltName=DNS:milo-apiserver.{ns}.svc,IP:127.0.0.1',stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
  for f in ('tokens.csv','tls.key','tls.crt'):(d/f).chmod(0o600)
 wait(lambda:subprocess.run(['kubectl','--kubeconfig',str(platform),'get','namespace',ns],capture_output=True).returncode==0,300,f'Flux did not create namespace {ns}')
 secret=kubectl(platform,'-n',ns,'create','secret','generic','milo-apiserver-private',f'--from-file=tokens.csv={d/"tokens.csv"}',f'--from-file=tls.crt={d/"tls.crt"}',f'--from-file=tls.key={d/"tls.key"}','--dry-run=client','-o','json',capture_output=True).stdout
 kubectl(platform,'apply','--server-side','--field-manager=fleet-credential-enrollment','-f','-',input=secret,stdout=subprocess.DEVNULL)
 print(role+': credential Secret enrolled (not in the source)',flush=True)
# 2. Flux must report each Milo Kustomization Ready (Deployment and etcd healthy).
for role in roles:
 kubectl(platform,'-n','flux-system','annotate','--overwrite','kustomization','milo-'+role,'reconcile.fluxcd.io/requestedAt='+str(time.time()),stdout=subprocess.DEVNULL)
for role in roles:
 kubectl(platform,'-n','flux-system','wait','--for=condition=Ready','kustomization/milo-'+role,'--timeout=600s',stdout=subprocess.DEVNULL)
# 3. Tenant fixture bootstrap through each project API.
for role in roles:
 d=root/role
 tokens={line.split(',')[1]:line.split(',')[0] for line in (d/'tokens.csv').read_text().splitlines()}
 server=f'https://127.0.0.1:{ports[role]}';ca=(d/'tls.crt').read_bytes()
 kube(d/'bootstrap.json',server,tokens['bootstrap'],ca)
 for attempt in range(90):
  result=subprocess.run(['kubectl','--kubeconfig',str(d/'bootstrap.json'),'--request-timeout=2s','get','--raw','/readyz'],capture_output=True)
  if result.returncode==0:
   discovery=subprocess.run(['kubectl','--kubeconfig',str(d/'bootstrap.json'),'get','crd','projects.resourcemanager.miloapis.com'],capture_output=True)
   if discovery.returncode==0:break
  time.sleep(2)
 else:raise SystemExit(f'{role} Milo API did not become ready. Inspect namespace milo-{role} on the platform cluster; private state: {d}')
 kubectl(d/'bootstrap.json','wait','--for=condition=Established','crd/projects.resourcemanager.miloapis.com','--timeout=90s',stdout=subprocess.DEVNULL)
 apply(d/'bootstrap.json',{'apiVersion':'resourcemanager.miloapis.com/v1alpha1','kind':'Project','metadata':{'name':role},'spec':{'ownerRef':{'kind':'Organization','name':'local-fixture'}}})
 route=server+'/apis/resourcemanager.miloapis.com/v1alpha1/projects/'+role+'/control-plane'
 for who in tokens:kube(d/(who+'-project.json'),route,tokens[who],ca)
 kube(d/'management-root.json',server,tokens['management'],ca)
 apply(d/'bootstrap.json',{'apiVersion':'rbac.authorization.k8s.io/v1','kind':'ClusterRole','metadata':{'name':'fleet-project-reader'},'rules':[{'apiGroups':['resourcemanager.miloapis.com'],'resources':['projects'],'verbs':['get']}]})
 apply(d/'bootstrap.json',{'apiVersion':'rbac.authorization.k8s.io/v1','kind':'ClusterRoleBinding','metadata':{'name':'fleet-project-reader'},'roleRef':{'apiGroup':'rbac.authorization.k8s.io','kind':'ClusterRole','name':'fleet-project-reader'},'subjects':[{'kind':'User','name':'management'}]})
 config=d/'bootstrap-project.json'
 kubectl(config,'apply','-f','config/base/crd/metal.datumapis.com.yaml',stdout=subprocess.DEVNULL)
 kubectl(config,'wait','--for=condition=Established','crd','--all','--timeout=90s',stdout=subprocess.DEVNULL)
 ns='fleet-provider' if role=='provider' else 'default'
 apply(config,{'apiVersion':'v1','kind':'Namespace','metadata':{'name':ns}})
 rules=[{'apiGroups':[''],'resources':['namespaces'],'verbs':['get']},{'apiGroups':['metal.datumapis.com'],'resources':['machineclasses','machineprofiles','machinecapacities','machines','installations'],'verbs':['get','list','watch']}]
 if role=='provider':
  for crds in ('config/components/inventory/crds/fleet.datumapis.com.yaml','config/components/device-operations/crds/fleet.datumapis.com.yaml'):
   kubectl(config,'apply','-f',crds,stdout=subprocess.DEVNULL)
  kubectl(config,'wait','--for=condition=Established','crd','--all','--timeout=90s',stdout=subprocess.DEVNULL)
  # The CRD rule check writes its synthetic objects here, away from the
  # coordinator's namespace. A project API runs no namespace controller, so
  # the namespace is never deleted.
  apply(config,{'apiVersion':'v1','kind':'Namespace','metadata':{'name':'fleet-rules'}})
  # The Metal provider records allocations and creates installations; it
  # reads custody and Devices only to skip taken capacity.
  rules+=[{'apiGroups':['metal.datumapis.com'],'resources':['machineallocations'],'verbs':['get','list','watch','create','delete']},{'apiGroups':['metal.datumapis.com'],'resources':['machineallocations/status'],'verbs':['get','update']},
          {'apiGroups':['fleet.datumapis.com'],'resources':['deviceinstallations'],'verbs':['get','list','watch','create']},{'apiGroups':['fleet.datumapis.com'],'resources':['devices','devicecustodies'],'verbs':['get','list','watch']}]
  components={
   'coordinator':[{'apiGroups':['fleet.datumapis.com'],'resources':['devices'],'verbs':['get','list','watch']},{'apiGroups':['fleet.datumapis.com'],'resources':['devicecustodies'],'verbs':['get','list','watch','create','update','delete']},
                  {'apiGroups':['fleet.datumapis.com'],'resources':['deviceinstallations','devicewipes','devicepoweroperations','devicerecoveries'],'verbs':['get','list','watch','delete']},
                  {'apiGroups':['fleet.datumapis.com'],'resources':['deviceinstallations/status','devicewipes/status','devicepoweroperations/status','devicerecoveries/status'],'verbs':['get','update']},
                  {'apiGroups':['metal.datumapis.com'],'resources':['machineallocations'],'verbs':['get','list','watch']}],
   'release':[{'apiGroups':['fleet.datumapis.com'],'resources':['devicewipes'],'verbs':['get','create']}],
   'discovery':[{'apiGroups':['fleet.datumapis.com'],'resources':['devices'],'verbs':['get','list']},{'apiGroups':['fleet.datumapis.com'],'resources':['devices/status'],'verbs':['get','update']}]}
  for who,component in components.items():
   apply(d/'bootstrap.json',{'apiVersion':'rbac.authorization.k8s.io/v1','kind':'ClusterRole','metadata':{'name':'fleet-'+who},'rules':component})
   apply(d/'bootstrap.json',{'apiVersion':'rbac.authorization.k8s.io/v1','kind':'ClusterRoleBinding','metadata':{'name':'fleet-'+who},'roleRef':{'apiGroup':'rbac.authorization.k8s.io','kind':'ClusterRole','name':'fleet-'+who},'subjects':[{'kind':'User','name':who}]})
 if role!='provider':rules += [{'apiGroups':['metal.datumapis.com'],'resources':['machines'],'verbs':['update']},{'apiGroups':['metal.datumapis.com'],'resources':['machines/status','installations/status'],'verbs':['update','patch']}]
 apply(d/'bootstrap.json',{'apiVersion':'rbac.authorization.k8s.io/v1','kind':'ClusterRole','metadata':{'name':'fleet-management'},'rules':rules})
 apply(d/'bootstrap.json',{'apiVersion':'rbac.authorization.k8s.io/v1','kind':'ClusterRoleBinding','metadata':{'name':'fleet-management'},'roleRef':{'apiGroup':'rbac.authorization.k8s.io','kind':'ClusterRole','name':'fleet-management'},'subjects':[{'kind':'User','name':'management'}]})
 if role!='provider':
  apply(d/'bootstrap.json',{'apiVersion':'v1','kind':'Namespace','metadata':{'name':'default'}})
  apply(d/'bootstrap.json',{'apiVersion':'rbac.authorization.k8s.io/v1','kind':'Role','metadata':{'name':'customer','namespace':'default'},'rules':[{'apiGroups':['metal.datumapis.com'],'resources':['machines','installations'],'verbs':['get','list','watch','create','delete']}]})
  apply(d/'bootstrap.json',{'apiVersion':'rbac.authorization.k8s.io/v1','kind':'RoleBinding','metadata':{'name':'customer','namespace':'default'},'roleRef':{'apiGroup':'rbac.authorization.k8s.io','kind':'Role','name':'customer'},'subjects':[{'kind':'User','name':'customer'}]})
 print(role+': Flux-managed Milo project API ready (static-token RBAC test seam)',flush=True)
