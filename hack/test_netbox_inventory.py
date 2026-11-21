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
    def test_invalid_identifier_shapes_are_quarantined(self):
        valid = {"provider": "example", "type": "PremisesCode", "value": "EX1"}
        invalid = [None, {}, "invalid", 1, False, [None], ["invalid"], [[]],
                   [dict(valid, uid="forbidden")], [dict(valid, other="unknown")],
                   [dict(valid, provider=[])], [dict(valid, type={})],
                   [dict(valid, value=None)], [{"provider": "example"}],
                   [valid, valid], [dict(valid, value=str(index)) for index in range(65)]]
        for identifiers in invalid:
            with self.subTest(identifiers=identifiers):
                source, mapping = fixture()
                mapping["facilities"][0].update(identifiersVerified=True, identifiers=identifiers)
                report, candidates = inventory.Planner(source, mapping).run()
                self.assertIn("Facility/example-premises", report["conflicts"])
                self.assertFalse(any(record["kind"] == "Facility" for record in candidates))

    def test_identifier_cardinality_boundary(self):
        for count in (0, 64):
            with self.subTest(count=count):
                source, mapping = fixture()
                identifiers = [{"provider": "example", "type": "PremisesCode", "value": str(index)} for index in range(count)]
                mapping["facilities"][0].update(identifiersVerified=True, identifiers=identifiers)
                report, candidates = inventory.Planner(source, mapping).run()
                self.assertNotIn("Facility/example-premises", report["conflicts"])
                facility = next(record for record in candidates if record["kind"] == "Facility")
                self.assertEqual(facility["spec"]["identifiers"], identifiers)

    def operational_mapping(self):
        source, mapping = fixture()
        _, records = inventory.Planner(source, mapping).run()
        for record in records:
            record["metadata"]["uid"] = "uid-" + record["metadata"]["name"]
        facility = next(record for record in records if record["kind"] == "Facility")
        existing = {"target": mapping["target"], "collections": ["Site", "Facility", "Rack", "DeviceClaim", "Device", "Cable", "PowerFeed"], "items": records}
        mapping["operationalSites"] = [{"name": "site-stable", "displayName": "Example operational identity", "code": "EX3", "source": "dcim/sites/1", "verified": True, "facilityRefs": [{"name": facility["metadata"]["name"]}]}]
        return source, mapping, existing

    def test_site_mapping_is_explicit_and_observational(self):
        source, mapping, existing = self.operational_mapping()
        before = copy.deepcopy(existing)
        report, records = inventory.Planner(source, mapping, existing).run()
        self.assertEqual({}, report["conflicts"])
        self.assertEqual(["Site"], [record["kind"] for record in records])
        self.assertEqual("EX3", records[0]["spec"]["code"])
        self.assertEqual([{"name": "example-premises"}], records[0]["spec"]["facilityRefs"])
        self.assertEqual(before, existing)
        self.assertEqual([], report["deletions"])
        self.assertFalse(report["applied"])
        self.assertEqual("Unknown", report["readiness"])
        mapping.pop("operationalSites")
        _, records = inventory.Planner(source, mapping, existing).run()
        self.assertFalse(any(record["kind"] == "Site" for record in records))

    def test_site_mapping_requires_verified_existing_names(self):
        for invalid in ("unverified", "uid", "missing", "incomplete", "namespace", "deleting", "empty-name", "extra-namespace"):
            with self.subTest(invalid=invalid):
                source, mapping, existing = self.operational_mapping()
                site = mapping["operationalSites"][0]
                facility = next(record for record in existing["items"] if record["kind"] == "Facility")
                if invalid == "unverified":
                    site["verified"] = False
                elif invalid == "uid":
                    site["facilityRefs"][0]["uid"] = "other-uid"
                elif invalid == "empty-name":
                    site["facilityRefs"][0]["name"] = ""
                elif invalid == "extra-namespace":
                    site["facilityRefs"][0]["namespace"] = "other"
                elif invalid == "missing":
                    existing["items"] = []
                elif invalid == "incomplete":
                    existing["collections"].remove("Site")
                elif invalid == "namespace":
                    facility["metadata"]["namespace"] = "other"
                else:
                    facility["metadata"]["deletionTimestamp"] = source["observedAt"]
                if invalid in ("uid", "empty-name", "extra-namespace"):
                    report, records = inventory.Planner(source, mapping, existing).run()
                    self.assertTrue(report["conflicts"])
                    self.assertFalse(any(record["kind"] == "Site" for record in records))
                else:
                    with self.assertRaises(ValueError):
                        inventory.Planner(source, mapping, existing).run()

    def test_malformed_site_fields_are_quarantined(self):
        invalid_fields = [("code", value) for value in ("", None, 1, [], "x" * 65)]
        invalid_fields += [("displayName", value) for value in ("", None, 1, "x" * 254)]
        invalid_fields += [("name", value) for value in ("", "premises..one", "premises.-one", "premises-.one", "x" * 254)]
        invalid_fields += [("facilityRefs", value) for value in (None, [], "bad", [None], [{"name": "premises..one"}], [{"name": "example-premises"}] * 2, [{"name": "example-premises"}] * 65)]
        invalid_fields += [("uid", "not-a-spec-field")]
        for field, value in invalid_fields:
            with self.subTest(field=field, value=value):
                source, mapping, existing = self.operational_mapping()
                mapping["operationalSites"][0][field] = value
                report, records = inventory.Planner(source, mapping, existing).run()
                self.assertTrue(report["conflicts"])
                self.assertFalse(any(record["kind"] == "Site" for record in records))

    def test_operator_handoff_supports_site_and_physical_candidates(self):
        for sites in (True, False):
            with self.subTest(sites=sites), tempfile.TemporaryDirectory() as directory:
                source, mapping, existing = self.operational_mapping()
                mapping["target"]["writer"] = "Operator"
                if not sites:
                    mapping.pop("operationalSites")
                    existing["items"] = []
                root = Path(directory)
                for name, data in (("source", source), ("mapping", mapping), ("existing", existing)):
                    inventory.write_private(root / (name + ".json"), data)
                inventory.main(["plan", "--snapshot", str(root / "source.json"), "--mapping", str(root / "mapping.json"), "--existing", str(root / "existing.json"), "--output", str(root / "output"), "--handoff", "--allow-partial"])
                handoff = json.loads((root / "output/handoff.json").read_text())
                self.assertEqual("Operator", handoff["target"]["writer"])
                self.assertIn("direct API", handoff["application"])
                self.assertNotIn("Flux", handoff["application"])
                self.assertFalse(handoff["applied"])

    def test_valid_mapping_cannot_clear_same_source_quarantine(self):
        source, mapping, existing = self.operational_mapping()
        malformed = dict(mapping["operationalSites"][0], code="", name="malformed-site")
        mapping["operationalSites"].insert(0, malformed)
        report, records = inventory.Planner(source, mapping, existing).run()
        self.assertTrue(report["conflicts"])
        self.assertFalse(any(record["kind"] == "Site" for record in records))

    def test_site_mapping_does_not_pin_server_uids(self):
        source, mapping, existing = self.operational_mapping()
        facility = next(record for record in existing["items"] if record["kind"] == "Facility")
        facility["metadata"]["uid"] = "replacement-server-uid"
        report, records = inventory.Planner(source, mapping, existing).run()
        self.assertEqual({}, report["conflicts"])
        self.assertEqual([{"name": "example-premises"}], records[0]["spec"]["facilityRefs"])
        facility["metadata"].pop("uid")
        report_without_uid, records_without_uid = inventory.Planner(source, mapping, existing).run()
        self.assertEqual({}, report_without_uid["conflicts"])
        self.assertEqual(records, records_without_uid)

    def test_provider_identifier_collisions_are_qualified_not_inferred(self):
        source, mapping = fixture()
        identifiers = [{"provider": provider, "type": "PremisesCode", "value": "EX1"} for provider in ("example-a", "example-b")]
        mapping["facilities"][0].update(identifiers=identifiers, identifiersVerified=True)
        report, records = inventory.Planner(source, mapping).run()
        self.assertEqual({}, report["conflicts"])
        self.assertEqual(identifiers, next(record for record in records if record["kind"] == "Facility")["spec"]["identifiers"])
        self.assertFalse(any(record["kind"] == "Site" for record in records))
        mapping["facilities"][0]["identifiersVerified"] = False
        with self.assertRaises(ValueError):
            inventory.Planner(source, mapping).run()

    def test_provider_metro_is_not_a_premises_identifier(self):
        source, mapping = fixture()
        mapping["facilities"][0].update(identifiers=[{"provider": "example", "type": "Metro", "value": "EX"}], identifiersVerified=True)
        report, _ = inventory.Planner(source, mapping).run()
        self.assertIn("Facility/example-premises", report["conflicts"])

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

    def test_rear_capacity_cannot_default_to_singleton_or_allow_handoff(self):
        for capacity in (None, 0, -1, True, False, 1.5, "2", 257, "missing"):
            with self.subTest(capacity=capacity):
                self.source, self.mapping = fixture()
                rear = self.source["objects"]["dcim/rear-ports"][0]
                rear["positions"] = capacity
                if capacity == "missing":
                    del rear["positions"]
                self.source["objects"]["dcim/cables"].append({"id": 2, "a_terminations": [{"object_type": "dcim.rearport", "object_id": 1}], "b_terminations": [{"object_type": "dcim.interface", "object_id": 1}]})
                self.mapping["cables"]["2"] = {"medium": "Copper"}
                report, records = self.plan()
                entry = next(row for row in report["records"] if row["source"] == "dcim/rear-ports/1")
                self.assertEqual("unresolved", entry["classification"])
                self.assertTrue(report["conflicts"])
                self.assertFalse(any(component["id"] == "rear-ports-1" for record in records for component in record["spec"].get("components", [])))
                with tempfile.TemporaryDirectory() as directory:
                    root = Path(directory)
                    existing = {"items": [], "target": self.mapping["target"], "collections": ["Facility", "Rack", "DeviceClaim", "Device", "Cable", "PowerFeed"]}
                    for name, data in (("source", self.source), ("mapping", self.mapping), ("existing", existing)):
                        inventory.write_private(root / (name + ".json"), data)
                    with self.assertRaisesRegex(ValueError, "reconciliation conflicts"):
                        inventory.main(["plan", "--snapshot", str(root / "source.json"), "--mapping", str(root / "mapping.json"), "--existing", str(root / "existing.json"), "--output", str(root / "blocked"), "--handoff", "--allow-partial"])
                    self.assertFalse((root / "blocked/candidates.json").exists())

    def test_rear_capacity_preserves_supported_geometry(self):
        for capacity in (1, 2, 256):
            with self.subTest(capacity=capacity):
                self.source["objects"]["dcim/rear-ports"][0]["positions"] = capacity
                report, records = self.plan()
                self.assertEqual({}, report["conflicts"])
                rear = next(component for record in records for component in record["spec"].get("components", []) if component["id"] == "rear-ports-1")
                self.assertEqual(capacity, rear["positions"])

    def test_front_port_requires_explicit_valid_rear_position(self):
        for position in (None, 0, -1, True, False, 1.0, "1", 257, "missing"):
            with self.subTest(position=position):
                front = self.source["objects"]["dcim/front-ports"][0]
                front["rear_port_position"] = position
                if position == "missing":
                    del front["rear_port_position"]
                report, records = self.plan()
                entry = next(row for row in report["records"] if row["source"] == "dcim/front-ports/1")
                self.assertEqual("unresolved", entry["classification"])
                self.assertTrue(report["conflicts"])
                self.assertFalse(any(component["id"] == "front-ports-1" for record in records for component in record["spec"].get("components", [])))

    def test_front_port_preserves_explicit_multi_position_mapping(self):
        for position in (1, 2):
            with self.subTest(position=position):
                self.source["objects"]["dcim/front-ports"][0]["rear_port_position"] = position
                report, records = self.plan()
                self.assertEqual({}, report["conflicts"])
                mappings = [mapping for record in records for mapping in record["spec"].get("internalMappings", []) if mapping["from"]["componentID"] == "front-ports-1"]
                self.assertEqual(1, len(mappings))
                self.assertEqual(position, mappings[0]["to"]["position"])

    def test_partial_handoff_cannot_bypass_unknown_front_port_position(self):
        del self.source["objects"]["dcim/front-ports"][0]["rear_port_position"]
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            existing = {"items": [], "target": self.mapping["target"], "collections": ["Facility", "Rack", "DeviceClaim", "Device", "Cable", "PowerFeed"]}
            for name, data in (("source", self.source), ("mapping", self.mapping), ("existing", existing)):
                inventory.write_private(root / (name + ".json"), data)
            with self.assertRaisesRegex(ValueError, "reconciliation conflicts"):
                inventory.main(["plan", "--snapshot", str(root / "source.json"), "--mapping", str(root / "mapping.json"), "--existing", str(root / "existing.json"), "--output", str(root / "blocked"), "--handoff", "--allow-partial"])
            self.assertFalse((root / "blocked/candidates.json").exists())

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
