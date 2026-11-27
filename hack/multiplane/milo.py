#!/usr/bin/env python3
"""Enroll credentials for, then seed, the Flux-managed Milo v0.32.5 project APIs.

Flux on the platform cluster owns each Milo API's Deployment, etcd StatefulSet,
volume and Service (config/multiplane/milo). This script does only what a
source revision must never hold:

- credential enrollment: per-API static tokens and a self-signed serving key,
  written as the milo-apiserver-private Secret (local credential seam), and
  the provider API's front-proxy CA and client key for its aggregation layer;
- tenant fixture bootstrap through each API: the Project, static-token RBAC
  grants and the metal CRDs inside the project, and in the provider project
  the inventory and Device custody CRDs. The provider project's Fleet
  components each get their own token: the Metal provider (management), the
  custody coordinator, the release controller and fixture discovery. A staff
  token creates recoveries.
- with IPAM (synchronized lab DHCP or the virtual media suite), IPAM's credentials
  (its Milo token, serving key and database password), the APIService that
  aggregates IPAM into the provider API, and the boot address class and pool
  inside the provider project.
  Production uses Milo's own project provisioning, service catalog and IAM;
  this is not that machinery.
"""
import argparse, base64, json, os, pathlib, secrets, subprocess, time
p=argparse.ArgumentParser();p.add_argument('state');p.add_argument('prefix');a=p.parse_args()
root=pathlib.Path(a.state).resolve();root.mkdir(mode=0o700,parents=True,exist_ok=True)
if not a.prefix.startswith('fleet-mp-'):raise SystemExit('task-owned fleet-mp- prefix required')
os.environ['DOCKER_CONTEXT']=os.environ.get('FLEET_MULTIPLANE_DOCKER_CONTEXT','colima-fleet-acceptance-tcg')
platform=root/'platform-admin.yaml'
with_ipam=os.environ.get('FLEET_LAB_DHCP')=='synced' or os.environ.get('FLEET_LAB_IPAM')=='1'
ports=json.loads((root/'platform-ports.json').read_text())
def run(*args,**kw):return subprocess.run(args,check=True,text=True,**kw)
def kubectl(config,*args,**kw):return run('kubectl','--kubeconfig',str(config),'--request-timeout=15s',*args,**kw)
def apply(config,obj):kubectl(config,'apply','-f','-',input=json.dumps(obj),stdout=subprocess.DEVNULL)
def kube(path,server,token,ca):
 path.write_text(json.dumps({'apiVersion':'v1','kind':'Config','clusters':[{'name':'local','cluster':{'server':server,'certificate-authority-data':base64.b64encode(ca).decode()}}],'users':[{'name':'role','user':{'token':token}}],'contexts':[{'name':'role','context':{'cluster':'local','user':'role'}}],'current-context':'role'}));path.chmod(0o600)
def enroll(ns,*create):
 obj=kubectl(platform,'-n',ns,'create',*create,'--dry-run=client','-o','json',capture_output=True).stdout
 kubectl(platform,'apply','--server-side','--field-manager=fleet-credential-enrollment','-f','-',input=obj,stdout=subprocess.DEVNULL)
def wait(fn,seconds,what):
 for _ in range(seconds//2):
  if fn():return
  time.sleep(2)
 raise SystemExit(what)
def now():return time.strftime('%Y-%m-%dT%H:%M:%SZ',time.gmtime())
roles=('provider','consumer-a','consumer-b')
LOCATION={'apiGroup':'networking.datumapis.com','kind':'Location','name':'local'}
def ipam(d,config):
 """Aggregate IPAM into the provider API, as the platform registers it, and
 create the boot address class and pool inside the provider project."""
 apply(d/'bootstrap.json',{'apiVersion':'apiregistration.k8s.io/v1','kind':'APIService','metadata':{'name':'v1alpha1.ipam.miloapis.com'},
       'spec':{'group':'ipam.miloapis.com','version':'v1alpha1','service':{'namespace':'ipam-system','name':'ipam-apiserver','port':443},
               'caBundle':base64.b64encode((root/'ipam'/'tls.crt').read_bytes()).decode(),'groupPriorityMinimum':1000,'versionPriority':15}})
 # Milo's controller-manager marks a remote APIService Available; the lab
 # runs none, so bootstrap does, once Flux reported IPAM's apiserver Ready.
 kubectl(d/'bootstrap.json','patch','apiservice','v1alpha1.ipam.miloapis.com','--subresource=status','--type=merge','-p',json.dumps({'status':{'conditions':[
         {'type':'Available','status':'True','reason':'Remote','message':'IPAM apiserver Ready in the platform cluster','lastTransitionTime':now()}]}}),stdout=subprocess.DEVNULL)
 wait(lambda:subprocess.run(['kubectl','--kubeconfig',str(config),'--request-timeout=10s','get','ipclasses.ipam.miloapis.com'],capture_output=True).returncode==0,300,'IPAM is not served through the provider project')
 # Kea's base subnet is 192.168.77.0/24 and the bridge is .1: the pool
 # withholds .0-.99 and .200-.255, so IPAM hands out only .100-.199.
 def write(obj):
  # IPAM's first writes reach its fresh database through the provider API's
  # aggregation and may take longer than a request's usual bound; retry
  # them, and say why the last was refused.
  last=''
  for _ in range(12):
   r=subprocess.run(['kubectl','--kubeconfig',str(config),'--request-timeout=60s','apply','-f','-'],input=json.dumps(obj),capture_output=True,text=True)
   if r.returncode==0:return
   last=(r.stderr or r.stdout).strip()[-600:]
   print('ipam: %s %s not applied yet: %s'%(obj['kind'],obj['metadata']['name'],last),flush=True)
   time.sleep(10)
  raise SystemExit('IPAM refused %s %s: %s'%(obj['kind'],obj['metadata']['name'],last))
 write({'apiVersion':'ipam.miloapis.com/v1alpha1','kind':'IPClass','metadata':{'name':'lab-provisioning'},
               'spec':{'ipFamily':'IPv4','addressScope':'Private','allowedPrefixLengths':{'min':32,'max':32},'defaultPrefixLength':32,'reclaimPolicy':'Delete'}})
 write({'apiVersion':'ipam.miloapis.com/v1alpha1','kind':'IPPool','metadata':{'name':'lab-provisioning'},
               'spec':{'cidr':'192.168.77.0/24','ipFamily':'IPv4','classNames':['lab-provisioning'],'scope':{'location':LOCATION},
                       'allocation':{'minPrefixLength':32,'maxPrefixLength':32,'strategy':'FirstFit'},'reservations':{'leading':100,'trailing':56,'unitPrefixLength':32}}})
 get=lambda *a:json.loads(kubectl(config,'get',*a,'-o','json',capture_output=True).stdout)
 wait(lambda:get('ippool','lab-provisioning').get('status',{}).get('phase')=='Ready',120,'the lab IPAM pool did not become Ready')
 pod=next(x for x in json.loads(kubectl(platform,'-n','ipam-system','get','pods','-l','app=ipam-apiserver','-o','json',capture_output=True).stdout)['items'] if not x['metadata'].get('deletionTimestamp'))
 db=json.loads(kubectl(platform,'-n','ipam-system','get','statefulset','ipam-postgres','-o','json',capture_output=True).stdout)
 api=json.loads(kubectl(d/'bootstrap.json','get','apiservice','v1alpha1.ipam.miloapis.com','-o','json',capture_output=True).stdout)
 pool=get('ippool','lab-provisioning')
 (root/'evidence'/'ipam.json').write_text(json.dumps({'observedAt':now(),'service':'milo-os/ipam v0.6.0','image':pod['spec']['containers'][0]['image'],
  'imageID':pod['status']['containerStatuses'][0]['imageID'],'storage':db['spec']['template']['spec']['containers'][0]['image'],
  'quota':'off (ENABLE_QUOTA=false): the lab Milo serves no quota API','authn':'TokenReview and SubjectAccessReview delegated to the provider Milo API',
  'apiService':{'name':api['metadata']['name'],'service':api['spec']['service'],'conditions':[{k:c.get(k) for k in ('type','status','reason')} for c in api['status'].get('conditions',[])]},
  'class':get('ipclass','lab-provisioning')['spec'],'pool':{'spec':pool['spec'],'status':{k:pool['status'].get(k) for k in ('phase','allocatedCIDR','capacity')}}},indent=2,sort_keys=True)+'\n')
 print('ipam: milo-os/ipam v0.6.0 aggregated by the provider Milo API; class lab-provisioning, pool 192.168.77.0/24 allocating .100-.199',flush=True)
# 1. Credential enrollment into the Flux-created namespaces.
for role in roles:
 d=root/role;ns='milo-'+role
 if not d.exists():
  d.mkdir(mode=0o700)
  # IPAM delegates TokenReview and SubjectAccessReview to the provider API
  # as system:masters, as the platform's IPAM client identity does.
  who=('bootstrap','management','customer')+(('coordinator','release','discovery','staff','ipam') if role=='provider' else ())
  tokens={r:secrets.token_hex(32) for r in who}
  (d/'tokens.csv').write_text('\n'.join(f'{v},{k},{k},'+('system:masters' if k in ('bootstrap','ipam') else 'fleet-local') for k,v in tokens.items())+'\n')
  run('openssl','req','-x509','-newkey','rsa:2048','-nodes','-keyout',str(d/'tls.key'),'-out',str(d/'tls.crt'),'-days','2','-subj','/CN=fleet-local','-addext',f'subjectAltName=DNS:milo-apiserver.{ns}.svc,IP:127.0.0.1',stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
  if role=='provider':
   # The aggregation layer's front proxy: a CA the aggregated APIs trust for
   # X-Remote-* identity headers, and the client key Milo proxies with.
   run('openssl','req','-x509','-newkey','rsa:2048','-nodes','-keyout',str(d/'front-proxy-ca.key'),'-out',str(d/'front-proxy-ca.crt'),'-days','2','-subj','/CN=fleet-front-proxy-ca',
       '-addext','basicConstraints=critical,CA:TRUE','-addext','keyUsage=critical,keyCertSign,cRLSign',stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
   run('openssl','req','-new','-newkey','rsa:2048','-nodes','-keyout',str(d/'front-proxy-client.key'),'-out',str(d/'front-proxy-client.csr'),'-subj','/CN=fleet-front-proxy-client',stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
   (d/'front-proxy-client.ext').write_text('extendedKeyUsage=clientAuth\n')
   run('openssl','x509','-req','-in',str(d/'front-proxy-client.csr'),'-CA',str(d/'front-proxy-ca.crt'),'-CAkey',str(d/'front-proxy-ca.key'),'-set_serial','1','-days','2',
       '-extfile',str(d/'front-proxy-client.ext'),'-out',str(d/'front-proxy-client.crt'),stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
  for f in d.iterdir():f.chmod(0o600)
 wait(lambda:subprocess.run(['kubectl','--kubeconfig',str(platform),'get','namespace',ns],capture_output=True).returncode==0,300,f'Flux did not create namespace {ns}')
 files=('tokens.csv','tls.crt','tls.key')+(('front-proxy-ca.crt','front-proxy-client.crt','front-proxy-client.key') if role=='provider' else ())
 enroll(ns,'secret','generic','milo-apiserver-private',*(f'--from-file={f}={d/f}' for f in files))
 print(role+': credential Secret enrolled (not in the source)',flush=True)
# IPAM's credentials, in the namespace its Flux layer creates: the database
# password and the DSN IPAM reads, its serving key (whose certificate is the
# APIService caBundle), its kubeconfig for the provider Milo API, and Milo's
# front-proxy CA in the control-plane-ca ConfigMap IPAM's manifests mount.
if with_ipam:
 i=root/'ipam';pd=root/'provider'
 if not i.exists():
  i.mkdir(mode=0o700)
  # The .token suffix enrolls the password in the harness secret scan.
  (i/'postgres-password.token').write_text(secrets.token_hex(32))
  run('openssl','req','-x509','-newkey','rsa:2048','-nodes','-keyout',str(i/'tls.key'),'-out',str(i/'tls.crt'),'-days','2','-subj','/CN=ipam-apiserver.ipam-system.svc',
      '-addext','subjectAltName=DNS:ipam-apiserver.ipam-system.svc',stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
  token=next(l.split(',')[0] for l in (pd/'tokens.csv').read_text().splitlines() if l.split(',')[1]=='ipam')
  kube(i/'kubeconfig','https://milo-apiserver.milo-provider.svc:6443',token,(pd/'tls.crt').read_bytes())
  (i/'dsn').write_text('postgres://ipam:%s@ipam-postgres-0.ipam-postgres.ipam-system.svc:5432/ipam?sslmode=disable'%(i/'postgres-password.token').read_text())
  for f in i.iterdir():f.chmod(0o600)
 wait(lambda:subprocess.run(['kubectl','--kubeconfig',str(platform),'get','namespace','ipam-system'],capture_output=True).returncode==0,300,'Flux did not create namespace ipam-system')
 enroll('ipam-system','secret','generic','postgres-credentials',f'--from-file=password={i/"postgres-password.token"}',f'--from-file=dsn={i/"dsn"}')
 enroll('ipam-system','secret','generic','ipam-apiserver-tls',f'--from-file=tls.crt={i/"tls.crt"}',f'--from-file=tls.key={i/"tls.key"}')
 enroll('ipam-system','secret','generic','ipam-milo-credentials',f'--from-file=kubeconfig={i/"kubeconfig"}')
 enroll('ipam-system','configmap','control-plane-ca',f'--from-file=ca.crt={pd/"front-proxy-ca.crt"}')
 print('ipam: credentials enrolled (not in the source)',flush=True)
# 2. Flux must report each Milo Kustomization Ready (Deployment and etcd
# healthy), and with IPAM, IPAM's: PostgreSQL, then the apiserver, which is
# Ready only once its delegated authentication has read the provider API.
layers=['milo-'+r for r in roles]+(['ipam-system','ipam-apiserver'] if with_ipam else [])
for layer in layers:
 kubectl(platform,'-n','flux-system','annotate','--overwrite','kustomization',layer,'reconcile.fluxcd.io/requestedAt='+str(time.time()),stdout=subprocess.DEVNULL)
for layer in layers:
 kubectl(platform,'-n','flux-system','wait','--for=condition=Ready','kustomization/'+layer,'--timeout=600s',stdout=subprocess.DEVNULL)
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
  # Synchronized lab DHCP claims boot addresses from IPAM, which asks this
  # API to authorize each request. Each requester claims and releases its
  # own boot addresses, and reads allocations to confirm a release; the
  # release controller reads custody to keep a held operation's claim.
  claims={'apiGroups':['ipam.miloapis.com'],'resources':['ipclaims'],'verbs':['get','list','create','delete']}
  allocations={'apiGroups':['ipam.miloapis.com'],'resources':['ipallocations'],'verbs':['get','list']}
  if with_ipam:rules+=[claims,allocations]
  components={
   'coordinator':[{'apiGroups':['fleet.datumapis.com'],'resources':['devices'],'verbs':['get','list','watch']},{'apiGroups':['fleet.datumapis.com'],'resources':['devicecustodies'],'verbs':['get','list','watch','create','update','delete']},
                  {'apiGroups':['fleet.datumapis.com'],'resources':['deviceinstallations','devicewipes','devicerecoveries'],'verbs':['get','list','watch','delete']},
                  {'apiGroups':['fleet.datumapis.com'],'resources':['deviceinstallations/status','devicewipes/status','devicerecoveries/status'],'verbs':['get','update']},
                  {'apiGroups':['metal.datumapis.com'],'resources':['machineallocations'],'verbs':['get','list','watch']}]+([{'apiGroups':['ipam.miloapis.com'],'resources':['ipclaims'],'verbs':['get']}] if with_ipam else []),
   'release':[{'apiGroups':['fleet.datumapis.com'],'resources':['devicewipes'],'verbs':['get','create']}]+([claims,allocations,{'apiGroups':['fleet.datumapis.com'],'resources':['devicecustodies'],'verbs':['get']}] if with_ipam else []),
   'discovery':[{'apiGroups':['fleet.datumapis.com'],'resources':['devices'],'verbs':['get','list']},{'apiGroups':['fleet.datumapis.com'],'resources':['devices/status'],'verbs':['get','update']}],
   # Staff create recoveries and read inventory and custody, nothing else.
   # The coordinator treats every unconfigured creator as staff.
   'staff':[{'apiGroups':['fleet.datumapis.com'],'resources':['devices','devicecustodies'],'verbs':['get','list','watch']},{'apiGroups':['fleet.datumapis.com'],'resources':['devicerecoveries'],'verbs':['get','list','watch','create']}]}
  for who,component in components.items():
   apply(d/'bootstrap.json',{'apiVersion':'rbac.authorization.k8s.io/v1','kind':'ClusterRole','metadata':{'name':'fleet-'+who},'rules':component})
   apply(d/'bootstrap.json',{'apiVersion':'rbac.authorization.k8s.io/v1','kind':'ClusterRoleBinding','metadata':{'name':'fleet-'+who},'roleRef':{'apiGroup':'rbac.authorization.k8s.io','kind':'ClusterRole','name':'fleet-'+who},'subjects':[{'kind':'User','name':who}]})
  if with_ipam:ipam(d,config)
 if role!='provider':rules += [{'apiGroups':['metal.datumapis.com'],'resources':['machines'],'verbs':['update']},{'apiGroups':['metal.datumapis.com'],'resources':['machines/status','installations/status'],'verbs':['update','patch']}]
 apply(d/'bootstrap.json',{'apiVersion':'rbac.authorization.k8s.io/v1','kind':'ClusterRole','metadata':{'name':'fleet-management'},'rules':rules})
 apply(d/'bootstrap.json',{'apiVersion':'rbac.authorization.k8s.io/v1','kind':'ClusterRoleBinding','metadata':{'name':'fleet-management'},'roleRef':{'apiGroup':'rbac.authorization.k8s.io','kind':'ClusterRole','name':'fleet-management'},'subjects':[{'kind':'User','name':'management'}]})
 if role!='provider':
  apply(d/'bootstrap.json',{'apiVersion':'v1','kind':'Namespace','metadata':{'name':'default'}})
  apply(d/'bootstrap.json',{'apiVersion':'rbac.authorization.k8s.io/v1','kind':'Role','metadata':{'name':'customer','namespace':'default'},'rules':[{'apiGroups':['metal.datumapis.com'],'resources':['machines','installations'],'verbs':['get','list','watch','create','delete']}]})
  apply(d/'bootstrap.json',{'apiVersion':'rbac.authorization.k8s.io/v1','kind':'RoleBinding','metadata':{'name':'customer','namespace':'default'},'roleRef':{'apiGroup':'rbac.authorization.k8s.io','kind':'Role','name':'customer'},'subjects':[{'kind':'User','name':'customer'}]})
 print(role+': Flux-managed Milo project API ready (static-token RBAC test seam)',flush=True)
