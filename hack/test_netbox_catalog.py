import copy
import json
from pathlib import Path
import subprocess
import tempfile
import unittest

import netbox_catalog as catalog


def fixture():
    snapshot = {"version": 1, "source": "example-netbox", "observedAt": "2026-11-21T00:00:00Z", "errors": {}, "objects": {
        "dcim/manufacturers": [{"id": 1, "name": "Example Vendor"}],
        "dcim/device-types": [{"id": 2, "model": "Example 1U", "manufacturer": {"id": 1}}],
    }}
    mapping = {"revision": "reviewed-1", "target": {"project": "example-provider", "namespace": "inventory", "writer": "DirectAPI"},
               "manufacturers": [{"sourceID": "1", "name": "vendor-stable", "canonicalName": "Example Vendor", "aliases": ["Old Vendor"]}],
               "deviceTypes": [{"sourceID": "2", "name": "type-stable", "manufacturerSourceID": "1", "model": "example-1u", "displayName": "Example 1U"}]}
    existing = {"target": copy.deepcopy(mapping["target"]), "collections": ["Manufacturer", "DeviceType"], "items": []}
    return snapshot, mapping, existing


def plan(snapshot, mapping, existing):
    return catalog.plan(snapshot, mapping, existing, "synthetic-operator", "2026-11-21T01:00:00Z")


class CatalogTests(unittest.TestCase):
    def test_punctuated_human_names_are_not_resource_names(self):
        snapshot, mapping, existing = fixture()
        snapshot["objects"]["dcim/manufacturers"][0]["name"] = "Example Vendor, Inc."
        mapping["manufacturers"][0]["canonicalName"] = "Example Vendor, Inc."
        self.assertEqual("vendor-stable", plan(snapshot, mapping, existing)["items"][0]["metadata"]["name"])

    def test_checked_in_candidates_are_reproducible(self):
        root = Path(__file__).parent / "testdata" / "catalog"
        inputs = [json.loads((root / (name + ".json")).read_text()) for name in ("snapshot", "mapping", "existing")]
        self.assertEqual(json.loads((root / "candidates.json").read_text()), plan(*inputs))

    def test_create_only_and_repeat_noop(self):
        snapshot, mapping, existing = fixture()
        retained = copy.deepcopy((snapshot, mapping, existing))
        result = plan(snapshot, mapping, existing)
        self.assertEqual(["Manufacturer", "DeviceType"], [item["kind"] for item in result["items"]])
        self.assertEqual({"name": "vendor-stable"}, result["items"][1]["spec"]["manufacturerRef"])
        self.assertEqual("2", result["items"][1]["spec"]["sourceIdentity"]["id"])
        self.assertEqual(retained, (snapshot, mapping, existing))
        existing["items"] = result["items"]
        self.assertEqual([], plan(snapshot, mapping, existing)["items"])
        mapping["deviceTypes"][0]["name"] = "new-target"
        with self.assertRaises(ValueError):
            plan(snapshot, mapping, existing)

    def test_interrupted_import_retains_existing_manufacturer(self):
        snapshot, mapping, existing = fixture()
        existing["items"] = plan(snapshot, mapping, existing)["items"][:1]
        self.assertEqual(["DeviceType"], [item["kind"] for item in plan(snapshot, mapping, existing)["items"]])

    def test_aliases_and_normalization_collisions_quarantine_whole_cohort(self):
        for aliases in (["Old Vendor", "old vendor"], ["EXAMPLE VENDOR"], [" trailing "], [""], [None], {}, "alias"):
            snapshot, mapping, existing = fixture()
            mapping["manufacturers"][0]["aliases"] = aliases
            with self.subTest(aliases=aliases), self.assertRaises(ValueError):
                plan(snapshot, mapping, existing)
        snapshot, mapping, existing = fixture()
        snapshot["objects"]["dcim/manufacturers"].append({"id": 3, "name": "Other Vendor"})
        mapping["manufacturers"].append({"sourceID": "3", "name": "other-vendor", "canonicalName": "Other Vendor", "aliases": ["old vendor"]})
        with self.assertRaises(ValueError):
            plan(snapshot, mapping, existing)
        snapshot, mapping, existing = fixture()
        snapshot["objects"]["dcim/device-types"].append({"id": 3, "model": "Example--1U", "manufacturer": {"id": 1}})
        mapping["deviceTypes"].append(dict(mapping["deviceTypes"][0], sourceID="3", name="other-type"))
        with self.assertRaises(ValueError):
            plan(snapshot, mapping, existing)

    def test_malformed_mappings_and_unknown_uid_fields(self):
        for key, value in (("uid", "forbidden"), ("namespace", "other"), ("sourceID", True), ("sourceID", "01"), ("name", "Upper"), ("model", "Wrong Model"), ("manufacturerSourceID", "9")):
            snapshot, mapping, existing = fixture()
            mapping["deviceTypes"][0][key] = value
            with self.subTest(key=key), self.assertRaises(ValueError):
                plan(snapshot, mapping, existing)
        for value in (None, [], "wrong", {"uid": "forbidden"}):
            snapshot, mapping, existing = fixture()
            mapping["deviceTypes"] = [value]
            with self.subTest(value=value), self.assertRaises(ValueError):
                plan(snapshot, mapping, existing)

    def test_existing_collisions_and_mutations_are_not_updates(self):
        for mutation in ("label", "source", "uid", "duplicate", "namespace", "terminating", "unknown-reference"):
            snapshot, mapping, existing = fixture()
            existing["items"] = plan(snapshot, mapping, existing)["items"]
            record = existing["items"][1]
            if mutation == "label":
                record["spec"]["displayName"] = "Operator changed"
            elif mutation == "source":
                record["spec"]["sourceIdentity"]["id"] = "9"
            elif mutation == "uid":
                record["spec"]["manufacturerRef"]["uid"] = "not-allowed"
            elif mutation == "duplicate":
                duplicate = copy.deepcopy(record)
                duplicate["metadata"]["name"] = "duplicate-source"
                existing["items"].append(duplicate)
            elif mutation == "namespace":
                record["metadata"]["namespace"] = "other"
            elif mutation == "terminating":
                record["metadata"]["deletionTimestamp"] = "2026-11-21T00:00:00Z"
            else:
                record["spec"]["manufacturerRef"]["extra"] = "invalid"
            with self.subTest(mutation=mutation), self.assertRaises(ValueError):
                plan(snapshot, mapping, existing)

    def test_incomplete_source_and_gitops_are_refused(self):
        for mutation in ("errors", "missing", "duplicate", "gitops", "stale", "incomplete"):
            snapshot, mapping, existing = fixture()
            if mutation == "errors":
                snapshot["errors"] = {"dcim/manufacturers": "denied"}
            elif mutation == "missing":
                del snapshot["objects"]["dcim/device-types"]
            elif mutation == "duplicate":
                snapshot["objects"]["dcim/manufacturers"] *= 2
            elif mutation == "gitops":
                mapping["target"]["writer"] = "Flux"
            elif mutation == "stale":
                snapshot["observedAt"] = "2026-11-22T00:00:00Z"
            else:
                existing["collections"] = ["DeviceType"]
            with self.subTest(mutation=mutation), self.assertRaises(ValueError):
                plan(snapshot, mapping, existing)

    def test_cli_quarantines_duplicate_json_keys_without_candidates(self):
        with tempfile.TemporaryDirectory() as directory:
            files = []
            for index, value in enumerate(fixture()):
                path = Path(directory) / f"{index}.json"
                path.write_text(json.dumps(value))
                files.append(str(path))
            Path(files[1]).write_text('{"uid":"one","uid":"two"}')
            result = subprocess.run(["python3", str(Path(catalog.__file__)), "--snapshot", files[0], "--mapping", files[1], "--existing", files[2], "--recorded-by", "synthetic", "--recorded-at", "2026-11-21T01:00:00Z"], capture_output=True, text=True)
            self.assertEqual(1, result.returncode)
            self.assertEqual([], json.loads(result.stdout)["items"])


if __name__ == "__main__":
    unittest.main()
