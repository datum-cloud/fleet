import copy
import io
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
import urllib.error

import netbox_inventory as inventory


def fixture():
    objects = {collection: [] for collection in inventory.REQUIRED}
    objects.update({
        "dcim/sites": [{"id": 1, "name": "Example metro grouping"}],
        "dcim/locations": [{"id": 1, "name": "Example room"}],
        "dcim/racks": [{"id": 1, "name": "Example rack", "u_height": 42}],
        "dcim/manufacturers": [{"id": 1, "name": "Example"}],
        "dcim/device-types": [{"id": 1, "model": "Example physical model", "manufacturer": {"id": 1, "name": "Example"}}],
        "dcim/devices": [
            {"id": 1, "name": "Example server", "serial": "provider-001", "asset_tag": "asset-001", "device_type": {"id": 1}, "rack": {"id": 1}, "position": 1},
            {"id": 2, "name": "Example PDU", "device_type": {"id": 1}, "rack": {"id": 1}},
            {"id": 3, "name": "Example panel", "device_type": {"id": 1}},
            {"id": 4, "name": "Example VM", "device_type": {"model": "netactuate VM"}},
        ],
        "dcim/interfaces": [{"id": 1, "name": "eth0", "device": {"id": 1}, "type": "1000base-t"}, {"id": 2, "name": "bond0", "device": {"id": 1}, "type": "lag"}],
        "dcim/power-ports": [{"id": 1, "name": "PSU1", "device": {"id": 1}}, {"id": 2, "name": "inlet", "device": {"id": 2}}],
        "dcim/power-outlets": [{"id": 1, "name": "outlet-1", "device": {"id": 2}, "power_port": {"id": 2}}],
        "dcim/front-ports": [{"id": 1, "name": "front-1", "device": {"id": 3}, "rear_port": {"id": 1}, "rear_port_position": 1}],
        "dcim/rear-ports": [{"id": 1, "name": "rear-1", "device": {"id": 3}, "positions": 2}],
        "dcim/power-panels": [{"id": 1, "name": "Panel A"}],
        "dcim/power-feeds": [{"id": 1, "name": "Feed A", "power_panel": {"id": 1}, "rack": {"id": 1}, "supply": "ac", "voltage": 208, "amperage": 30}],
        "dcim/cables": [{"id": 1, "label": "Power 1", "a_terminations": [{"object_type": "dcim.poweroutlet", "object_id": 1}], "b_terminations": [{"object_type": "dcim.powerport", "object_id": 1}]}],
        "circuits/circuits": [{"id": 1, "cid": "Example-service"}],
        "circuits/circuit-terminations": [{"id": 1, "circuit": {"id": 1}}],
    })
    source = {"version": 1, "source": "example-netbox", "observedAt": "2026-11-20T12:00:00Z", "objects": objects, "errors": {}}
    mapping = {
        "revision": "example-reviewed-1",
        "target": {"project": "example-provider", "namespace": "inventory", "writer": "Flux", "application": "provider-inventory"},
        "facilities": [{"source": "dcim/sites/1", "name": "example-premises", "displayName": "Example premises"}],
        "racks": {"1": {"facility": "example-premises"}},
        "devices": {
            "1": {"classification": "physical", "serialMeaning": "providerHostID", "provider": "example-provider", "placement": {"scope": "RackMembership"}},
            "2": {"classification": "physical", "serialMeaning": "unknown", "placement": {"scope": "RackMembership"}},
            "3": {"classification": "physical", "serialMeaning": "unknown", "placement": {"scope": "Facility", "facility": "example-premises"}},
        },
        "feeds": {"1": {"facility": "example-premises"}},
        "cables": {"1": {"medium": "Power"}},
        "circuits": {"1": {"system": "example-service-owner", "type": "transport", "id": "Example-service"}},
        "demarcations": {"1": {"componentSource": "dcim/rear-ports/1"}},
    }
    return source, mapping


class PlanTests(unittest.TestCase):
    def setUp(self):
        self.source, self.mapping = fixture()

    def plan(self, existing=None, previous=None):
        if existing is not None:
            existing = dict(existing, target=self.mapping["target"], collections=["Facility", "Rack", "DeviceClaim", "Device", "Cable", "PowerFeed"])
        return inventory.Planner(self.source, self.mapping, existing, previous).run()

    def test_physical_shapes_and_no_hardware_authority(self):
        report, records = self.plan()
        self.assertTrue(report["coverageComplete"])
        self.assertEqual({}, report["conflicts"])
        self.assertEqual({"Facility", "Rack", "DeviceClaim", "Device", "Cable", "PowerFeed"}, {record["kind"] for record in records})
        self.assertEqual(sum(len(rows) for rows in self.source["objects"].values()), len(report["records"]))
        server = next(record for record in records if record["kind"] == "Device" and record["spec"].get("assetTag"))
        self.assertNotIn("serial", server["spec"]["identity"])
        self.assertEqual("provider-001", server["spec"]["providerHostRef"]["id"])
        self.assertEqual("asset-001", server["spec"]["assetTag"])
        for record in records:
            self.assertNotIn("status", record)
            self.assertNotIn("bmc", record["spec"])
            if record["kind"] == "DeviceClaim":
                self.assertNotIn("position", record["spec"]["placement"])
                self.assertNotIn("height", record["spec"]["placement"])
        states = {row["source"]: row["classification"] for row in report["records"]}
        self.assertEqual("excluded", states["dcim/devices/4"])
        self.assertEqual("excluded", states["dcim/interfaces/2"])
        self.assertEqual("imported", states["circuits/circuit-terminations/1"])

    def test_checked_in_synthetic_handoff_matches_planner(self):
        _, records = self.plan()
        fixture_path = Path(__file__).parent / "testdata/netbox/candidates.json"
        self.assertEqual(inventory.read_json(fixture_path)["items"], records)

    def test_existing_snapshot_wrong_project_and_missing_collections(self):
        with self.assertRaisesRegex(ValueError, "target"):
            inventory.Planner(self.source, self.mapping, {"items": [], "target": {"project": "other"}})
        with self.assertRaisesRegex(ValueError, "six"):
            inventory.Planner(self.source, self.mapping, {"items": [], "target": self.mapping["target"]})

    def test_repeated_and_interrupted_run(self):
        report, records = self.plan()
        self.assertEqual((report, records), self.plan())
        second, remaining = self.plan({"items": records})
        self.assertEqual({}, second["conflicts"])
        self.assertEqual([], remaining)
        partial, remaining = self.plan({"items": records[:2]})
        self.assertEqual({}, partial["conflicts"])
        self.assertEqual(len(records) - 2, len(remaining))

    def test_existing_identity_is_never_recreated(self):
        _, records = self.plan()
        server = copy.deepcopy(next(record for record in records if record["kind"] == "Device" and record["spec"].get("assetTag")))
        server["metadata"]["name"] = "original-asset"
        server["spec"]["identity"] = {"model": "Legacy", "serial": "provider-001"}
        server["spec"].pop("assetTag")
        server["spec"].pop("providerHostRef")
        report, candidates = self.plan({"items": [server]})
        self.assertTrue(report["conflicts"])
        self.assertFalse(any(record["kind"] == "Device" and record["spec"].get("providerHostRef") for record in candidates))
        self.assertEqual("original-asset", server["metadata"]["name"])

    def test_changed_source_and_different_writer_are_conflicts(self):
        _, records = self.plan()
        records[0]["spec"]["displayName"] = "Manual correction"
        report, _ = self.plan({"items": records})
        self.assertTrue(report["conflicts"])
        _, records = self.plan()
        records[0]["spec"]["provenance"]["source"]["system"] = "other-source"
        report, _ = self.plan({"items": records})
        self.assertTrue(report["conflicts"])

    def test_omission_preserves_records_and_does_not_prune(self):
        previous = copy.deepcopy(self.source)
        self.source["objects"]["dcim/devices"] = self.source["objects"]["dcim/devices"][1:]
        report, _ = self.plan(previous=previous)
        self.assertIn("dcim/devices/1", report["missingSincePrevious"])
        self.assertEqual([], report["deletions"])

    def test_unknown_collections_and_missing_collections_are_visible(self):
        self.source["objects"]["dcim/new-physical-kind"] = [{"id": 1}]
        del self.source["objects"]["dcim/console-ports"]
        report, _ = self.plan()
        self.assertFalse(report["coverageComplete"])
        self.assertEqual("unsupported", next(row for row in report["records"] if row["source"] == "dcim/new-physical-kind/1")["classification"])

    def test_duplicate_identity_and_target_mapping_rejected(self):
        self.source["objects"]["dcim/sites"].append({"id": 1})
        with self.assertRaises(ValueError):
            self.plan()
        self.source, self.mapping = fixture()
        self.mapping["facilities"].append(copy.deepcopy(self.mapping["facilities"][0]))
        with self.assertRaises(ValueError):
            self.plan()

    def test_site_can_map_to_several_premises(self):
        self.mapping["facilities"].append({"source": "dcim/sites/1", "name": "another-premises", "displayName": "Other premises"})
        _, records = self.plan()
        self.assertEqual(2, sum(record["kind"] == "Facility" for record in records))

    def test_no_mapping_means_no_guessed_assets(self):
        self.mapping = {}
        report, records = self.plan()
        self.assertEqual([], records)
        self.assertTrue(any(row["classification"] == "unresolved" for row in report["records"]))

    def test_duplicate_cable_termination_quarantines_both(self):
        cable = copy.deepcopy(self.source["objects"]["dcim/cables"][0])
        cable["id"] = 2
        self.source["objects"]["dcim/cables"].append(cable)
        self.mapping["cables"]["2"] = {"medium": "Power"}
        report, records = self.plan()
        self.assertFalse(any(record["kind"] == "Cable" for record in records))
        self.assertTrue(report["conflicts"])

    def test_breakout_requires_reviewed_lanes_and_every_termination(self):
        self.source["objects"]["dcim/interfaces"].append({"id": 3, "name": "eth1", "device": {"id": 1}, "type": "1000base-t"})
        self.source["objects"]["dcim/cables"] = [{"id": 2, "a_terminations": [{"object_type": "dcim.rearport", "object_id": 1}], "b_terminations": [{"object_type": "dcim.interface", "object_id": 1}, {"object_type": "dcim.interface", "object_id": 3}]}]
        planner = inventory.Planner(self.source, self.mapping)
        panel = planner.stable_name("device", "3")
        server = planner.stable_name("device", "1")
        self.mapping["cables"]["2"] = {"medium": "Copper"}
        report, records = self.plan()
        self.assertFalse(any(record["kind"] == "Cable" for record in records))
        lanes = [{"id": "lane-" + str(position), "a": {"deviceRef": {"name": panel}, "componentID": "rear-ports-1", "position": position}, "b": {"deviceRef": {"name": server}, "componentID": "interfaces-" + identity, "position": 1}} for position, identity in ((1, "1"), (2, "3"))]
        self.mapping["cables"]["2"]["lanes"] = lanes
        report, records = self.plan()
        self.assertEqual({}, report["conflicts"])
        self.assertEqual(1, sum(record["kind"] == "Cable" for record in records))
        self.mapping["cables"]["2"]["lanes"] = lanes[:1]
        report, records = self.plan()
        self.assertFalse(any(record["kind"] == "Cable" for record in records))

    def test_incompatible_medium_and_missing_internal_mapping(self):
        self.mapping["cables"]["1"]["medium"] = "Fiber"
        self.source["objects"]["dcim/front-ports"][0]["rear_port_position"] = 3
        report, _ = self.plan()
        self.assertGreaterEqual(len(report["conflicts"]), 2)

    def test_redaction_uses_allowlisted_fields(self):
        source = {"id": 1, "comments": "SECRET", "custom_fields": {"bmc_password": "SECRET"}, "config_context": {"token": "SECRET"}, "device": {"id": 2, "url": "https://user:SECRET@example.invalid"}}
        self.assertNotIn("SECRET", json.dumps(inventory.sanitize(source)))

    def test_output_is_private_and_never_overwritten(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "report.json"
            inventory.write_private(path, {"example": True})
            self.assertEqual(0o600, path.stat().st_mode & 0o777)
            with self.assertRaises(FileExistsError):
                inventory.write_private(path, {})

    def test_export_refuses_repository_output(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / ".git").write_text("gitdir: example")
            with self.assertRaisesRegex(ValueError, "outside Git"):
                inventory.private_output(root / "private-export")
            self.assertFalse((root / "private-export").exists())

    def test_handoff_requires_target_existing_and_partial_acknowledgement(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for name, data in (("source", self.source), ("mapping", self.mapping), ("existing", {"items": [], "target": self.mapping["target"], "collections": ["Facility", "Rack", "DeviceClaim", "Device", "Cable", "PowerFeed"]})):
                inventory.write_private(root / (name + ".json"), data)
            args = ["plan", "--snapshot", str(root / "source.json"), "--mapping", str(root / "mapping.json"), "--output", str(root / "blocked"), "--handoff"]
            with self.assertRaises(ValueError):
                inventory.main(args)
            self.assertFalse((root / "blocked/candidates.json").exists())
            args[args.index(str(root / "blocked"))] = str(root / "allowed")
            args.extend(["--existing", str(root / "existing.json"), "--allow-partial"])
            self.assertEqual(0, inventory.main(args))
            self.assertTrue((root / "allowed/candidates.json").exists())
            with self.assertRaises(FileExistsError):
                inventory.main(args)


class ReaderTests(unittest.TestCase):
    def reader(self):
        return inventory.Reader("https://example.invalid", "synthetic-token")

    def test_discovery_reports_lookup_exclusion_and_missing_collections(self):
        reader = self.reader()
        with patch.object(reader, "get", side_effect=[{"devices": "unused", "connected-device": "unused"}, {}]), patch.object(reader, "collection", return_value=[]) as collection:
            result = inventory.snapshot(reader, "example-netbox")
            collection.assert_called_once_with("dcim/devices")
            self.assertIn("dcim/connected-device", result["excludedEndpoints"])
            self.assertIn("dcim/sites", result["errors"])

    def test_discovery_permission_error_retains_partial_snapshot(self):
        reader = self.reader()
        with patch.object(reader, "get", side_effect=[{"devices": "unused"}, ValueError("NetBox GET failed with HTTP 403")]), patch.object(reader, "collection", return_value=[{"id": 1}]):
            result = inventory.snapshot(reader, "example-netbox")
            self.assertEqual([{"id": 1}], result["objects"]["dcim/devices"])
            self.assertIn("HTTP 403", result["errors"]["circuits/"])

    def test_pagination_and_duplicates(self):
        reader = self.reader()
        with patch.object(reader, "get", side_effect=[{"count": 2, "results": [{"id": 1}], "next": "?offset=1"}, {"count": 2, "results": [{"id": 2}], "next": None}]):
            self.assertEqual(2, len(reader.collection("dcim/devices")))
        with patch.object(reader, "get", side_effect=[{"count": 2, "results": [{"id": 1}], "next": "?offset=1"}, {"count": 2, "results": [{"id": 1}], "next": None}]):
            with self.assertRaisesRegex(ValueError, "repeated"):
                reader.collection("dcim/devices")

    def test_partial_pagination_and_cycles_fail_closed(self):
        reader = self.reader()
        for page in ({"count": 2, "results": [{"id": 1}], "next": None}, {"count": 1, "results": [], "next": "?limit=200"}, {"count": 1, "results": [], "next": "/api/users/"}):
            with patch.object(reader, "get", return_value=page), self.assertRaises(ValueError):
                reader.collection("dcim/devices")

    def test_cross_origin_and_http_and_credentials_refused(self):
        reader = self.reader()
        with patch.object(reader.opener, "open") as opener:
            for url in ("https://attacker.invalid/api/dcim/devices", "http://example.invalid/api/dcim/devices", "https://user:password@example.invalid/api/dcim/devices"):
                with self.assertRaises(ValueError):
                    reader.get(url)
            opener.assert_not_called()

    def test_retries_get_only_and_permission_errors_redacted(self):
        reader = self.reader()
        forbidden = urllib.error.HTTPError("https://example.invalid", 403, "SECRET", {}, None)
        with patch.object(reader.opener, "open", side_effect=forbidden) as opener:
            with self.assertRaisesRegex(ValueError, "HTTP 403") as caught:
                reader.get("https://example.invalid/api/dcim/")
            self.assertNotIn("SECRET", str(caught.exception))
            self.assertEqual("GET", opener.call_args.args[0].method)
            self.assertEqual(1, opener.call_count)
        unavailable = urllib.error.HTTPError("https://example.invalid", 503, "SECRET", {}, None)
        with patch.object(reader.opener, "open", side_effect=[unavailable, io.StringIO('{"ok":true}')]) as opener, patch.object(inventory.time, "sleep"):
            self.assertTrue(reader.get("https://example.invalid/api/dcim/")["ok"])
            self.assertEqual(2, opener.call_count)


if __name__ == "__main__":
    unittest.main()
