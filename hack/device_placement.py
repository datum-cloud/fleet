"""Prepare guarded Device placement patches only. No API client or execution path."""

import argparse
import copy
import datetime
import math
import sys
from pathlib import Path

from netbox_inventory import digest, read_json, reference_id, write_private, private_output


def source_report(snapshot, device, model, rack_name=None, facility_name=None):
    report = {"revision": 1, "method": "NetBoxImport", "observedAt": snapshot["observedAt"], "snapshotDigest": digest(snapshot)}
    if facility_name:
        report["facilityRef"] = {"name": facility_name}
    if not rack_name:
        return report
    report["rackRef"] = {"name": rack_name}
    raw = device.get("position")
    if raw is not None:
        if isinstance(raw, bool):
            raise ValueError("invalid source geometry")
        try:
            number = float(raw)
        except (ValueError, TypeError):
            raise ValueError("invalid source geometry") from None
        if not math.isfinite(number) or number < 1 or number > 100:
            raise ValueError("invalid source geometry")
        report["position"] = number
    face = device.get("face")
    if isinstance(face, dict):
        face = face.get("value")
    if face is not None and face != "":
        if face not in ("front", "rear"):
            raise ValueError("invalid source face")
        report["face"] = face
    return report


def enrichment_plan(snapshot, target, allowlist, now=None):
    now = now or datetime.datetime.now(datetime.timezone.utc)
    for observed in (snapshot.get("observedAt"), target.get("observedAt")):
        if not isinstance(observed, str):
            raise ValueError("fresh source and target observation timestamps required")
        timestamp = datetime.datetime.fromisoformat(observed.replace("Z", "+00:00"))
        if timestamp.tzinfo is None or not 0 <= (now - timestamp).total_seconds() <= 900:
            raise ValueError("source and target reads must be no more than 15 minutes old")
    destination = target.get("target", {})
    if set(destination) != {"project", "namespace"} or not all(isinstance(value, str) and value for value in destination.values()):
        raise ValueError("explicit direct-API project and namespace required")
    if set(allowlist) != {"source", "target", "devices"} or allowlist["target"] != target.get("target") or allowlist["source"] != snapshot.get("source"):
        raise ValueError("exact target and device allowlist required")
    if not allowlist["devices"] or snapshot.get("errors"):
        raise ValueError("empty allowlist or incomplete source snapshot")
    datetime.datetime.fromisoformat(snapshot["observedAt"].replace("Z", "+00:00"))
    objects = snapshot["objects"]
    sources = {str(record["id"]): record for record in objects["dcim/devices"]}
    models = {str(record["id"]): record for record in objects["dcim/device-types"]}
    if len(sources) != len(objects["dcim/devices"]) or len(models) != len(objects["dcim/device-types"]):
        raise ValueError("duplicate source identity")
    records = {}
    for record in target["items"]:
        key = (record["kind"], record["metadata"].get("namespace"), record["metadata"]["name"])
        if key in records:
            raise ValueError("duplicate target identity")
        records[key] = record
    planned, seen, seen_sources = [], set(), set()
    for selection in allowlist["devices"]:
        if set(selection) != {"name", "namespace", "uid", "resourceVersion", "sourceID"}:
            raise ValueError("device allowlist has unexpected or missing fields")
        if selection["namespace"] != destination["namespace"]:
            raise ValueError("selection is outside the reviewed target namespace")
        key = ("Device", selection["namespace"], selection["name"])
        if key in seen or selection["sourceID"] in seen_sources:
            raise ValueError("duplicate allowlist target")
        seen.add(key)
        seen_sources.add(selection["sourceID"])
        device = records[key]
        metadata, spec = device["metadata"], device["spec"]
        if any(not selection[field] or metadata.get(field) != selection[field] for field in ("uid", "resourceVersion")) or metadata.get("deletionTimestamp"):
            raise ValueError("stale or replaced target")
        provenance = spec["provenance"]
        if datetime.datetime.fromisoformat(snapshot["observedAt"].replace("Z", "+00:00")) <= datetime.datetime.fromisoformat(provenance["recordedAt"].replace("Z", "+00:00")):
            raise ValueError("source snapshot does not advance the retained record revision")
        identity = {"system": snapshot["source"], "type": "dcim/devices", "id": selection["sourceID"]}
        if provenance["method"] != "NetBoxImport" or spec["identity"].get("sourceIdentity") != identity or any(provenance.get("source", {}).get(field) != value for field, value in identity.items()):
            raise ValueError("not a matching imported device")
        if gitops_owned(device):
            raise ValueError("legacy Flux records are excluded")
        claim = records[("DeviceClaim", selection["namespace"], spec["claimRef"]["name"])]
        placement = claim["spec"]["placement"]
        if claim["spec"].get("lifecycle") != "Installation" or placement.get("scope") != "RackMembership" or spec.get("reportedPlacement"):
            raise ValueError("only previously unreported rack-membership enrichment is reviewable")
        rack = records[("Rack", selection["namespace"], placement["rackRef"]["name"])]
        source = sources[selection["sourceID"]]
        rack_source = rack["spec"]["provenance"].get("source", {})
        if rack_source.get("system") != snapshot["source"] or rack_source.get("type") != "dcim/racks" or rack_source.get("id") != reference_id(source.get("rack")):
            raise ValueError("rack contradiction or unresolved rack source")
        for reference in (claim, rack):
            if gitops_owned(reference):
                raise ValueError("legacy Flux references are excluded")
            if not reference["metadata"].get("uid") or not reference["metadata"].get("resourceVersion") or reference["metadata"].get("deletionTimestamp"):
                raise ValueError("reference incarnation unresolved")
        model = models[reference_id(source["device_type"])]
        report = source_report(snapshot, source, model, rack["metadata"]["name"])
        if report.get("position") is None:
            raise ValueError("selected enrichment lacks a source position")
        height = rack["spec"].get("heightUnits")
        if not height or report["position"] > height or ("height" in report and report["position"] + report["height"] - 1 > height):
            raise ValueError("source position outside retained rack bounds")
        after = copy.deepcopy(device)
        after["spec"]["reportedPlacement"] = report
        after["spec"]["provenance"]["recordedAt"] = snapshot["observedAt"]
        after["spec"]["provenance"]["source"].update(observedAt=snapshot["observedAt"], snapshotDigest=digest(snapshot), mappingRevision="device-reported-placement-review-v2")
        preconditions = [{"op": "test", "path": "/metadata/uid", "value": selection["uid"]}, {"op": "test", "path": "/metadata/resourceVersion", "value": selection["resourceVersion"]}]
        patch = preconditions + [{"op": "add", "path": "/spec/reportedPlacement", "value": report}, {"op": "replace", "path": "/spec/provenance", "value": after["spec"]["provenance"]}]
        planned.append({"selection": selection, "before": device, "proposedAfter": after, "references": [claim, rack], "sourceDevice": source, "sourceModel": model,
                        "preconditions": preconditions, "patch": patch, "patchDigest": digest(patch),
                        "contentType": "application/json-patch+json", "serverDryRunQuery": "dryRun=All&fieldManager=fleet-placement-enrichment",
                        "disposition": "Prepared only: require reviewed deployed head, fresh dependency reads, server dry-run, approved canary and exact readback"})
    return {"version": 2, "executable": False, "applied": False, "preparedAt": now.isoformat(), "sourceDigest": digest(snapshot), "targetDigest": digest(target), "allowlistDigest": digest(allowlist), "target": target["target"], "devices": planned,
            "next": "Do not execute from this file alone. Confirm reviewed deployed schema head, refresh source/target and exact allowlist, retain authority and digests, server dry-run each patch, approve one canary, read back UID/spec before the remainder. Timeout is unknown: reconcile by GET; changed UID or competing revision is conflict, never retry automatically. No deletion or schema pruning rollback."}


def gitops_owned(record):
    metadata = record["metadata"]
    return any(any(marker in key.lower() for marker in ("fluxcd.io", "argocd")) for key in {**metadata.get("labels", {}), **metadata.get("annotations", {})}) or any(any(marker in entry.get("manager", "").lower() for marker in ("flux", "kustomize-controller", "helm-controller", "argocd")) for entry in metadata.get("managedFields", []))


def reconcile(item, current):
    if not isinstance(current, dict) or "metadata" not in current or "spec" not in current:
        return "Unknown"
    if current["metadata"].get("uid") != item["selection"]["uid"] or current["metadata"].get("deletionTimestamp"):
        return "Conflict"
    if current["spec"] == item["proposedAfter"]["spec"] and current["metadata"].get("resourceVersion") and current["metadata"]["resourceVersion"] != item["selection"]["resourceVersion"]:
        return "Applied"
    if current["spec"] == item["before"]["spec"] and current["metadata"].get("resourceVersion") == item["selection"]["resourceVersion"]:
        return "Unchanged; no automatic retry"
    return "Conflict"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("snapshot", "target", "allowlist", "output"):
        parser.add_argument("--" + name, required=True)
    args = parser.parse_args()
    result = enrichment_plan(read_json(args.snapshot), read_json(args.target), read_json(args.allowlist))
    output = Path(args.output)
    private_output(output)
    write_private(output / "placement-review.json", result)
    print("Prepared offline review saved; no writes dispatched:", len(result["devices"]))


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError, KeyError, TypeError) as error:
        print("Placement review stopped:", str(error) if isinstance(error, ValueError) else type(error).__name__, file=sys.stderr)
        sys.exit(2)
