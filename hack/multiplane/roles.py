#!/usr/bin/env python3
"""Credential enrollment for Flux-created identities; installs nothing.

Flux owns every ServiceAccount, Role, CRD, namespace, interpreter, member
Cluster and PropagationPolicy referenced here (config/multiplane). This script
only mints the short-lived tokens a source must never hold, writes private
mode-0600 kubeconfigs, and enrolls the cell's push-mode member Secret on the
Karmada API. No cell credential can access hub/project/private APIs.
"""
import base64,json,os,pathlib,subprocess,sys,time
state=pathlib.Path(sys.argv[1]).resolve();prefix=(state/'prefix').read_text().strip()
os.environ['DOCKER_CONTEXT']='colima-fleet-acceptance-tcg'
env=json.loads((state/'gitops-environment.json').read_text())
def k(config,*args,input=None):return subprocess.run(['kubectl','--kubeconfig',str(config),'--request-timeout=30s',*args],input=input,text=True,capture_output=True,check=True).stdout
def exists(config,*args):return subprocess.run(['kubectl','--kubeconfig',str(config),'--request-timeout=15s','get',*args],capture_output=True).returncode==0
def wait(config,*args):
 for _ in range(150):
  if exists(config,*args):return
  time.sleep(2)
 raise SystemExit('Flux did not create '+' '.join(args))
def credentials(config,ns,name,dest,internal=None,server=None):
 wait(config,'-n',ns,'serviceaccount',name)
 token=k(config,'-n',ns,'create','token',name,'--duration=24h').strip()
 obj=json.loads(k(config,'config','view','--raw','--minify','-o','json'))
 obj['users']=[{'name':name,'user':{'token':token}}];obj['contexts'][0]['context']['user']=name
 if server or internal:obj['clusters'][0]['cluster']['server']=server or internal
 dest.write_text(json.dumps(obj));dest.chmod(0o600)
 return token,base64.b64decode(obj['clusters'][0]['cluster']['certificate-authority-data'])
host=state/'host.yaml';hub=state/'hub-admin.yaml';cell=state/'cell-admin.yaml';ns='fleet-coordination'
# Karmada's admin kubeconfig (chart-generated Secret) rewritten to the host port.
wait(host,'-n','karmada-system','secret','karmada-kubeconfig')
raw=base64.b64decode(json.loads(k(host,'-n','karmada-system','get','secret','karmada-kubeconfig','-o','json'))['data']['kubeconfig'])
hub.write_bytes(raw);hub.chmod(0o600)
entry=k(hub,'config','view','-o','jsonpath={.clusters[0].name}')
k(hub,'config','set-cluster',entry,'--server=https://127.0.0.1:%d'%env['karmadaPort'])
# CA verification is retained; the chart includes localhost in its serving SANs.
credentials(hub,ns,'fleet-management',state/'management-hub.json')
# Cell execution credentials are Kubernetes ServiceAccounts inside the cell
# site; no cell credential is exported to the host.
token,ca=credentials(cell,ns,'fleet-federation',state/'federation-cell.json',env['cellEndpoint'])
wait(hub,'namespace','karmada-cluster')
secret={'apiVersion':'v1','kind':'Secret','metadata':{'name':env['cell'],'namespace':'karmada-cluster'},'data':{'token':base64.b64encode(token.encode()).decode(),'caBundle':base64.b64encode(ca).decode()}}
k(hub,'apply','--server-side','--field-manager=fleet-credential-enrollment','-f','-',input=json.dumps(secret))
k(host,'-n','karmada-system','annotate','--overwrite','kustomization','fleet-karmada-members','reconcile.fluxcd.io/requestedAt=%f'%time.time())
wait(hub,'cluster/'+env['cell'])
k(hub,'wait','--for=condition=Ready','cluster/'+env['cell'],'--timeout=300s')
k(host,'-n','karmada-system','wait','--for=condition=Ready','kustomization/fleet-karmada-members','--timeout=300s')
print('Karmada member enrolled; separate management and cell credentials minted for Flux-created identities')
