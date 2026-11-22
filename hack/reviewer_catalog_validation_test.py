import copy
import unittest

from test_netbox_catalog import fixture, plan


class IndependentCatalogValidation(unittest.TestCase):
    def test_collection_attestation_must_be_a_list(self):
        snapshot, mapping, existing = fixture()
        existing['collections'] = {'Manufacturer': False, 'DeviceType': False}
        with self.assertRaises(ValueError):
            plan(snapshot, mapping, existing)

    def test_source_version_must_not_be_boolean(self):
        snapshot, mapping, existing = fixture()
        snapshot['version'] = True
        with self.assertRaises(ValueError):
            plan(snapshot, mapping, existing)

    def test_existing_identity_requires_provenance(self):
        snapshot, mapping, existing = fixture()
        existing['items'] = plan(snapshot, mapping, existing)['items'][:1]
        del existing['items'][0]['spec']['provenance']
        with self.assertRaises(ValueError):
            plan(snapshot, mapping, existing)

    def test_existing_identity_requires_matching_source_evidence(self):
        snapshot, mapping, existing = fixture()
        existing['items'] = plan(snapshot, mapping, existing)['items'][:1]
        existing['items'][0]['spec']['provenance']['source']['id'] = '999'
        with self.assertRaises(ValueError):
            plan(snapshot, mapping, existing)

    def test_observation_timestamp_must_be_rfc3339(self):
        snapshot, mapping, existing = fixture()
        snapshot['observedAt'] = '20261121T000000+0000'
        with self.assertRaises(ValueError):
            plan(snapshot, mapping, existing)


if __name__ == '__main__':
    unittest.main()
