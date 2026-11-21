import copy
import datetime
import unittest

import device_placement as placement
import netbox_inventory as inventory
from test_netbox_inventory import fixture


def inputs():
    snapshot, mapping = fixture()
    _, candidates = inventory.Planner(snapshot, mapping).run()
    for index, record in enumerate(candidates):
        record["metadata"].update(uid="synthetic-" + str(index), resourceVersion="7")
    device = next(record for record in candidates if record["kind"] == "Device" and record["spec"]["identity"]["sourceIdentity"]["id"] == "1")
    snapshot = copy.deepcopy(snapshot)
    snapshot["observedAt"] = "2026-11-22T00:00:00Z"
    snapshot["objects"]["dcim/devices"][0]["face"] = {"value": "rear"}
    snapshot["objects"]["dcim/device-types"][0].update(u_height=2, is_full_depth=True)
    selection = {field: device["metadata"][field] for field in ("name", "namespace", "uid", "resourceVersion")}
    selection["sourceID"] = "1"
    destination = {field: mapping["target"][field] for field in ("project", "namespace")}
    return snapshot, {"target": destination, "observedAt": snapshot["observedAt"], "items": candidates}, {"source": snapshot["source"], "target": destination, "devices": [selection]}


def plan(snapshot, target, allowlist):
    return placement.enrichment_plan(snapshot, target, allowlist, datetime.datetime(2026, 11, 22, 0, 1, tzinfo=datetime.timezone.utc))


class PlacementTests(unittest.TestCase):
    def test_same_rack_plan_is_prepared_only_and_preserves_inputs(self):
        snapshot, target, allowlist = inputs()
        before = copy.deepcopy(target)
        result = plan(snapshot, target, allowlist)
        self.assertEqual(target, before)
        self.assertFalse(result["executable"])
        self.assertFalse(result["applied"])
        item = result["devices"][0]
        self.assertEqual([test["path"] for test in item["preconditions"]], ["/metadata/uid", "/metadata/resourceVersion"])
        original, proposed = item["before"]["spec"], item["proposedAfter"]["spec"]
        for field in set(original) - {"provenance"}:
            self.assertEqual(original[field], proposed[field])
        for field in ("method", "recordedBy"):
            self.assertEqual(original["provenance"][field], proposed["provenance"][field])
        self.assertEqual(proposed["reportedPlacement"]["face"], "rear")
        self.assertNotIn("height", proposed["reportedPlacement"])
        self.assertNotIn("fullDepth", proposed["reportedPlacement"])
        self.assertEqual(item["patch"][:2], item["preconditions"])
        self.assertEqual(item["patchDigest"], inventory.digest(item["patch"]))
        self.assertEqual([entry["path"] for entry in item["patch"]][2:], ["/spec/reportedPlacement", "/spec/provenance"])

    def test_rejects_conflicts_and_nonallowlisted_changes(self):
        for case in ("uid", "resourceVersion", "extra", "rack", "flux", "legacy", "positioned", "duplicate", "source", "unknown", "null", "bounds", "stale", "namespace"):
            with self.subTest(case=case):
                snapshot, target, allowlist = inputs()
                device = next(record for record in target["items"] if record["metadata"]["name"] == allowlist["devices"][0]["name"])
                claim = next(record for record in target["items"] if record["kind"] == "DeviceClaim" and record["metadata"]["name"] == device["spec"]["claimRef"]["name"])
                if case in ("uid", "resourceVersion"):
                    device["metadata"][case] = "changed"
                elif case == "extra": allowlist["devices"][0]["actor"] = "admin"
                elif case == "rack": snapshot["objects"]["dcim/devices"][0]["rack"] = {"id": "other"}
                elif case == "flux": device["metadata"]["managedFields"] = [{"manager": "kustomize-controller"}]
                elif case == "legacy": claim["spec"].pop("lifecycle")
                elif case == "positioned": claim["spec"]["placement"]["scope"] = "RackUnits"
                elif case == "duplicate": allowlist["devices"].append(allowlist["devices"][0])
                elif case == "source": device["spec"]["provenance"]["source"]["id"] = "other"
                elif case == "unknown": device["metadata"].pop("uid")
                elif case == "null": snapshot["objects"]["dcim/devices"][0]["position"] = None
                elif case == "bounds": snapshot["objects"]["dcim/devices"][0]["position"] = 43
                elif case == "stale": snapshot["observedAt"] = device["spec"]["provenance"]["recordedAt"]
                elif case == "namespace": allowlist["devices"][0]["namespace"] = "other"
                with self.assertRaises(ValueError): plan(snapshot, target, allowlist)

    def test_partial_and_nonunit_geometry_is_not_invented(self):
        snapshot, _, _ = inputs()
        report = placement.source_report(snapshot, {"position": None, "face": None}, {"u_height": 0}, "rack")
        self.assertEqual(set(report), {"revision", "method", "observedAt", "snapshotDigest", "rackRef"})
        report = placement.source_report(snapshot, {"position": "12.5"}, {"u_height": "0.5"}, "rack")
        self.assertEqual(report["position"], 12.5)
        self.assertNotIn("height", report)
        for raw in (True, -1, "nan", "inf", "invalid"):
            with self.assertRaises(ValueError): placement.source_report(snapshot, {"position": raw}, {}, "rack")

    def test_create_report_requires_explicit_per_device_selection(self):
        snapshot, mapping = fixture()
        _, old = inventory.Planner(snapshot, mapping).run()
        self.assertFalse(any("reportedPlacement" in record["spec"] for record in old))
        mapping["devices"]["1"]["includeReportedPlacement"] = True
        _, candidates = inventory.Planner(snapshot, mapping).run()
        reports = [record for record in candidates if "reportedPlacement" in record["spec"]]
        self.assertEqual(len(reports), 1)
        self.assertEqual(reports[0]["spec"]["reportedPlacement"]["snapshotDigest"], reports[0]["spec"]["provenance"]["source"]["snapshotDigest"])
        self.assertEqual(reports[0]["spec"]["reportedPlacement"]["position"], 1)

    def test_fresh_reads_and_exact_source_required(self):
        for field in ("source", "target", "missing", "future", "source-system"):
            snapshot, target, allowlist = inputs()
            if field == "source": snapshot["observedAt"] = "2026-11-21T00:00:00Z"
            if field == "target": target["observedAt"] = "2026-11-21T00:00:00Z"
            if field == "missing": target.pop("observedAt")
            if field == "future": target["observedAt"] = "2026-11-23T00:00:00Z"
            if field == "source-system": allowlist["source"] = "other"
            with self.subTest(field=field), self.assertRaises(ValueError): plan(snapshot, target, allowlist)

    def test_readback_reconciliation_never_authorizes_retry(self):
        item = plan(*inputs())["devices"][0]
        self.assertEqual(placement.reconcile(item, None), "Unknown")
        self.assertEqual(placement.reconcile(item, item["before"]), "Unchanged; no automatic retry")
        after = copy.deepcopy(item["proposedAfter"])
        self.assertEqual(placement.reconcile(item, after), "Conflict")
        after["metadata"]["resourceVersion"] = "8"
        self.assertEqual(placement.reconcile(item, after), "Applied")
        after["metadata"]["uid"] = "replaced"
        self.assertEqual(placement.reconcile(item, after), "Conflict")


if __name__ == "__main__":
    unittest.main()
