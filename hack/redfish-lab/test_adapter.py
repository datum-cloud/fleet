#!/usr/bin/env python3
"""Self-test of the unmodified upstream emulator plus the lab adapter.

Runs inside the built BMC image (it needs the pinned upstream emulator and its
Python dependencies) with fake_virsh.py in place of libvirt. The harness runs
it before creating any cluster; see docs/operations/redfish-lab.md.
"""
import base64
import json
import os
import pathlib
import sys
import tempfile
import unittest

HERE = pathlib.Path(__file__).resolve().parent
WORK = pathlib.Path(tempfile.mkdtemp())
(WORK / 'creds').mkdir()
(WORK / 'creds' / 'username').write_text('lab-operator\n')
(WORK / 'creds' / 'password').write_text('synthetic-test-password\n')
os.environ.update(FLEET_VIRSH=str(HERE / 'fake_virsh.py'), FAKE_VIRSH_STATE=str(WORK / 'virsh.json'), FLEET_BMC_STATE=str(WORK / 'state'))
sys.path.insert(0, str(HERE))
import fleet_lab_adapter as adapter  # noqa: E402

AUTH = {'Authorization': 'Basic ' + base64.b64encode(b'lab-operator:synthetic-test-password').decode()}
BAD = {'Authorization': 'Basic ' + base64.b64encode(b'lab-operator:wrong').decode()}


def virsh_state():
    return json.loads((WORK / 'virsh.json').read_text())


class AdapterTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.app = adapter.create_app(adapter.load_targets(), str(WORK / 'state'), str(WORK / 'creds'), '10.96.0.80').test_client()

    def audit(self):
        return [json.loads(l) for l in (WORK / 'state' / 'audit.log').read_text().splitlines()]

    def test_1_service_root_is_upstream_and_unauthenticated(self):
        r = self.app.get('/redfish/v1/')
        self.assertEqual(r.status_code, 200)
        self.assertEqual(r.json['Systems'], {'@odata.id': '/redfish/v1/Systems'})
        self.assertEqual(self.app.delete('/redfish/v1/reset/').status_code, 404)

    def test_2_authentication_is_enforced(self):
        self.assertEqual(self.app.get('/redfish/v1/Systems').status_code, 401)
        self.assertEqual(self.app.get('/redfish/v1/Systems', headers=BAD).status_code, 401)
        members = self.app.get('/redfish/v1/Systems', headers=AUTH).json['Members']
        self.assertEqual(sorted(m['@odata.id'] for m in members), ['/redfish/v1/Systems/System-1', '/redfish/v1/Systems/System-2'])

    def test_3_system_identity_and_capabilities(self):
        s = self.app.get('/redfish/v1/Systems/System-1', headers=AUTH).json
        self.assertEqual(s['UUID'], '7f3d0c2e-1a5b-4c6d-8e9f-0a1b2c3d4e0a')
        self.assertEqual(s['SerialNumber'], 'serial-synthetic-device-central-01')
        self.assertNotIn('Storage', s)
        self.assertEqual(self.app.get('/redfish/v1/Systems/System-1/Storage', headers=AUTH).status_code, 404)
        disks = self.app.get(s['SimpleStorage']['@odata.id'] + '/VirtIO-0', headers=AUTH).json['Devices']
        self.assertEqual([d['CapacityBytes'] for d in disks], [2147483648])
        self.assertEqual(self.app.get('/redfish/v1/', headers=AUTH).json['UpdateService'], {'@odata.id': '/redfish/v1/UpdateService'})
        inventory = self.app.get('/redfish/v1/UpdateService/FirmwareInventory', headers=AUTH).json['Members']
        self.assertEqual(self.app.get(inventory[0]['@odata.id'], headers=AUTH).json['Version'], 'fleet-lab-1.0')
        self.assertEqual(s['PowerState'], 'Off')
        self.assertEqual(s['Boot']['BootSourceOverrideTarget@Redfish.AllowableValues'], ['None', 'Pxe', 'Hdd'])
        info = self.app.get(s['Actions']['#ComputerSystem.Reset']['@Redfish.ActionInfo'], headers=AUTH).json
        self.assertEqual(info['Parameters'][0]['AllowableValues'], ['On', 'ForceOff', 'ForceRestart'])

    def test_4_boot_once_is_consumed_by_power_on(self):
        r = self.app.patch('/redfish/v1/Systems/System-1', headers=AUTH, json={'Boot': {'BootSourceOverrideTarget': 'Pxe', 'BootSourceOverrideEnabled': 'Once'}})
        self.assertEqual(r.status_code, 200)
        self.assertEqual(self.app.patch('/redfish/v1/Systems/System-1', headers=AUTH, json={'Boot': {'BootSourceOverrideTarget': 'Cd'}}).status_code, 400)
        r = self.app.post('/redfish/v1/Systems/System-1/Actions/ComputerSystem.Reset', headers=AUTH, json={'ResetType': 'On'})
        self.assertEqual(r.status_code, 204)
        d = virsh_state()['domains']
        self.assertEqual(d['fleet-lab-target-a']['starts'], ['network'])
        self.assertFalse(d['fleet-lab-target-b']['running'] if 'fleet-lab-target-b' in d else False)
        s = self.app.get('/redfish/v1/Systems/System-1', headers=AUTH).json
        self.assertEqual((s['PowerState'], s['Boot']['BootSourceOverrideEnabled']), ('On', 'Disabled'))
        # On while on is a no-op, never a second start.
        self.app.post('/redfish/v1/Systems/System-1/Actions/ComputerSystem.Reset', headers=AUTH, json={'ResetType': 'On'})
        self.assertEqual(virsh_state()['domains']['fleet-lab-target-a']['starts'], ['network'])
        self.app.post('/redfish/v1/Systems/System-1/Actions/ComputerSystem.Reset', headers=AUTH, json={'ResetType': 'ForceOff'})
        self.assertEqual(self.app.get('/redfish/v1/Systems/System-1', headers=AUTH).json['PowerState'], 'Off')
        self.assertEqual(self.app.post('/redfish/v1/Systems/System-1/Actions/ComputerSystem.Reset', headers=AUTH, json={'ResetType': 'Nmi'}).status_code, 400)

    def test_5_faults_are_bounded_and_audited(self):
        adapter.fault_cli(['reject-auth', '1'])
        self.assertEqual(self.app.get('/redfish/v1/Systems', headers=AUTH).status_code, 401)
        self.assertEqual(self.app.get('/redfish/v1/Systems', headers=AUTH).status_code, 200)
        adapter.fault_cli(['hide-reset', 'On'])
        info = self.app.get('/redfish/v1/Systems/System-1/ResetActionInfo', headers=AUTH).json
        self.assertNotIn('On', info['Parameters'][0]['AllowableValues'])
        self.assertEqual(self.app.post('/redfish/v1/Systems/System-1/Actions/ComputerSystem.Reset', headers=AUTH, json={'ResetType': 'On'}).status_code, 400)
        adapter.fault_cli(['clear'])
        # Skip one effective On, then withhold the response of the next one.
        adapter.fault_cli(['drop-response', 'System-1', 'On', '1', '1'])
        self.app.post('/redfish/v1/Systems/System-1/Actions/ComputerSystem.Reset', headers=AUTH, json={'ResetType': 'On'})
        self.assertNotIn('fault', self.audit()[-1])
        self.app.post('/redfish/v1/Systems/System-1/Actions/ComputerSystem.Reset', headers=AUTH, json={'ResetType': 'ForceOff'})
        self.app.post('/redfish/v1/Systems/System-1/Actions/ComputerSystem.Reset', headers=AUTH, json={'ResetType': 'On'})
        entries = self.audit()
        self.assertEqual(entries[-1]['fault'], 'drop-response-after-effect')
        self.assertEqual(entries[-1]['effect'], 'started:hd')
        # Each request records the client address that attributes it to a worker Pod.
        self.assertEqual(self.app.post('/redfish/v1/Systems/System-1/Actions/ComputerSystem.Reset', headers=AUTH, json={'ResetType': 'ForceOff'},
                                       environ_base={'REMOTE_ADDR': '10.244.0.7'}).status_code // 100, 2)
        self.assertEqual(self.audit()[-1]['client'], '10.244.0.7')
        self.assertEqual(adapter.FaultFile(str(WORK / 'state')).read().get('dropResponse'), [])
        text = (WORK / 'state' / 'audit.log').read_text()
        self.assertNotIn('synthetic-test-password', text)
        self.assertNotIn('Authorization', text)

    def test_6_lost_libvirt_definitions_are_redefined_with_the_same_identity(self):
        s = virsh_state()
        s['domains'].pop('fleet-lab-target-a')
        s['networks'].clear()
        (WORK / 'virsh.json').write_text(json.dumps(s))
        self.assertEqual(self.app.get('/redfish/v1/Systems/System-1', headers=AUTH).status_code, 200)
        self.assertEqual(virsh_state()['domains']['fleet-lab-target-a']['uuid'], '7f3d0c2e-1a5b-4c6d-8e9f-0a1b2c3d4e0a')
        os.environ['FAKE_VIRSH_DOWN'] = '1'
        try:
            self.assertEqual(self.app.get('/redfish/v1/Systems/System-1', headers=AUTH).status_code, 503)
        finally:
            del os.environ['FAKE_VIRSH_DOWN']


if __name__ == '__main__':
    unittest.main(verbosity=2)
