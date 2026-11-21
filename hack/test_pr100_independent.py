import copy
import json
import tempfile
import unittest
from pathlib import Path

import netbox_inventory as inventory
from test_netbox_inventory import PlanTests


class IndependentReviewTests(unittest.TestCase):
    def test_new_site_handoff_does_not_prescribe_gitops(self):
        source, mapping, existing = PlanTests().operational_mapping()
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for name, data in (("source", source), ("mapping", mapping), ("existing", existing)):
                (root / (name + ".json")).write_text(json.dumps(data))
            inventory.main(["plan", "--snapshot", str(root / "source.json"), "--mapping", str(root / "mapping.json"), "--existing", str(root / "existing.json"), "--output", str(root / "output"), "--handoff", "--allow-partial"])
            candidates = json.loads((root / "output/candidates.json").read_text())
            self.assertEqual(["Site"], [record["kind"] for record in candidates["items"]])
            handoff = json.loads((root / "output/handoff.json").read_text())
            self.assertFalse(handoff["applied"])
            self.assertNotIn("Flux", handoff["application"])

    def test_invalid_site_code_is_quarantined(self):
        source, mapping, existing = PlanTests().operational_mapping()
        mapping["operationalSites"][0]["code"] = ""
        report, candidates = inventory.Planner(source, mapping, existing).run()
        self.assertFalse(any(record["kind"] == "Site" for record in candidates), report)

    def test_duplicate_aliases_do_not_merge_premises(self):
        source, mapping, existing = PlanTests().operational_mapping()
        facility = next(record for record in existing["items"] if record["kind"] == "Facility")
        identifiers = [{"provider": "example-provider", "type": "PremisesAlias", "value": "SHARED-ALIAS"}]
        facility["spec"]["identifiers"] = copy.deepcopy(identifiers)
        mapping["facilities"][0].update(identifiers=identifiers, identifiersVerified=True)
        other = copy.deepcopy(facility)
        other["metadata"].update(name="other-premises", uid="other-uid")
        existing["items"].append(other)
        before = copy.deepcopy(existing)
        report, candidates = inventory.Planner(source, mapping, existing).run()
        self.assertEqual({}, report["conflicts"])
        self.assertEqual(existing, before)
        self.assertEqual(["Site"], [record["kind"] for record in candidates])
