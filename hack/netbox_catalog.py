"""Offline, create-only catalog candidate review; never writes to a live API."""

import argparse
import copy
import datetime
import json
import re
import sys

from netbox_inventory import digest, name_ok


KINDS = {"Manufacturer": "dcim/manufacturers", "DeviceType": "dcim/device-types"}
LABEL = re.compile(r"[A-Za-z0-9]([ -~]*[!-~])?")


def fields(value, required, optional=()):
    if not isinstance(value, dict) or not set(required) <= value.keys() or value.keys() - set(required) - set(optional):
        raise ValueError("missing or unknown mapping/identity fields")


def text(value):
    if not isinstance(value, str) or not value or len(value) > 253 or value != value.strip():
        raise ValueError("invalid catalog text")
    return value


def label(value):
    if not LABEL.fullmatch(text(value)):
        raise ValueError("invalid ASCII catalog label")
    return value


def model_key(value):
    value = text(value)
    if not value.isascii():
        raise ValueError("non-ASCII source model requires adjudication")
    result = re.sub(r"[^a-z0-9]+", "-", value.lower()).strip("-")
    if not result:
        raise ValueError("empty normalized model")
    return result


def source_id(value):
    if type(value) is int and value > 0:
        return str(value)
    if isinstance(value, str) and re.fullmatch(r"[1-9][0-9]*", value):
        return value
    raise ValueError("invalid explicit NetBox source ID")


def timestamp(value):
    if not isinstance(value, str):
        raise ValueError("invalid observation time")
    parsed = datetime.datetime.fromisoformat(value.replace("Z", "+00:00"))
    if parsed.tzinfo is None:
        raise ValueError("timestamp requires timezone")
    return parsed


def identity(value):
    fields(value, ("system", "type", "id"))
    return tuple(text(value[key]) for key in ("system", "type", "id"))


def no_uid(value):
    if isinstance(value, dict):
        if "uid" in value:
            raise ValueError("UID fields are not allowed in specs or mappings")
        for child in value.values():
            no_uid(child)
    elif isinstance(value, list):
        for child in value:
            no_uid(child)


def plan(snapshot, mapping, existing, recorded_by, recorded_at):
    no_uid(mapping)
    fields(mapping, ("revision", "target", "manufacturers", "deviceTypes"))
    target = mapping["target"]
    fields(target, ("project", "namespace", "writer"))
    if target["writer"] != "DirectAPI" or not name_ok(target["project"]) or not name_ok(target["namespace"]):
        raise ValueError("catalog requires an explicit operator-managed DirectAPI target")
    revision = text(mapping["revision"])
    text(recorded_by)
    if not isinstance(snapshot, dict) or snapshot.get("version") != 1 or snapshot.get("errors"):
        raise ValueError("complete version-1 snapshot required")
    system = text(snapshot.get("source"))
    observed = snapshot.get("observedAt")
    if timestamp(recorded_at) < timestamp(observed):
        raise ValueError("recording predates observation")
    fields(existing, ("target", "collections", "items"))
    if existing["target"] != target or sorted(existing["collections"]) != sorted(KINDS) or not isinstance(existing["items"], list):
        raise ValueError("complete matching catalog snapshot required")
    objects = snapshot.get("objects")
    if not isinstance(objects, dict):
        raise ValueError("source collections required")
    sources = {}
    for collection in KINDS.values():
        if not isinstance(objects.get(collection), list):
            raise ValueError("missing catalog source collection")
        for row in objects[collection]:
            if not isinstance(row, dict):
                raise ValueError("malformed source row")
            key = (collection, source_id(row.get("id")))
            if key in sources:
                raise ValueError("duplicate source identity")
            sources[key] = row
    records = {}
    source_names = {}
    for record in existing["items"]:
        if not isinstance(record, dict) or record.get("kind") not in KINDS or record.get("apiVersion") != "fleet.datumapis.com/v1alpha1":
            raise ValueError("invalid existing catalog record")
        meta, spec = record.get("metadata", {}), record.get("spec", {})
        no_uid(spec)
        if meta.get("namespace") != target["namespace"] or not name_ok(meta.get("name")) or meta.get("deletionTimestamp"):
            raise ValueError("invalid or terminating existing target")
        key = (record["kind"], meta["name"])
        if key in records:
            raise ValueError("duplicate existing target")
        records[key] = copy.deepcopy(record)
        if "sourceIdentity" in spec:
            source = identity(spec["sourceIdentity"])
            if source in source_names:
                raise ValueError("ambiguous existing source identity")
            source_names[source] = key
    candidates = []
    mapped_sources = set()
    mapped_names = set()
    manufacturer_names = {}
    for kind, mapping_key in (("Manufacturer", "manufacturers"), ("DeviceType", "deviceTypes")):
        entries = mapping[mapping_key]
        if not isinstance(entries, list):
            raise ValueError("catalog mappings must be lists")
        for entry in entries:
            required = ("sourceID", "name", "canonicalName") if kind == "Manufacturer" else ("sourceID", "name", "manufacturerSourceID", "model", "displayName")
            fields(entry, required, ("aliases",) if kind == "Manufacturer" else ())
            identifier = source_id(entry["sourceID"])
            name = entry["name"]
            if not name_ok(name):
                raise ValueError("invalid stable resource name")
            source = (system, KINDS[kind], identifier)
            key = (kind, name)
            if source in mapped_sources or key in mapped_names:
                raise ValueError("duplicate mapping identity or target")
            mapped_sources.add(source)
            mapped_names.add(key)
            row = sources.get((KINDS[kind], identifier))
            if row is None:
                raise ValueError("mapped source ID missing")
            if source in source_names and source_names[source] != key:
                raise ValueError("source identity already has a different stable target name")
            external = dict(zip(("system", "type", "id"), source))
            spec = {"sourceIdentity": external, "provenance": {"method": "NetBoxImport", "recordedBy": recorded_by, "recordedAt": recorded_at, "source": {**external, "snapshotDigest": digest(snapshot), "mappingRevision": revision, "observedAt": observed}}}
            if kind == "Manufacturer":
                canonical = label(entry["canonicalName"])
                if canonical != row.get("name"):
                    raise ValueError("canonical name differs from source; adjudicate explicitly outside import")
                aliases = entry.get("aliases", [])
                if not isinstance(aliases, list) or len(aliases) > 32:
                    raise ValueError("invalid aliases")
                spec.update(displayName=canonical, canonicalName=canonical)
                if aliases:
                    spec["aliases"] = [label(alias) for alias in aliases]
                manufacturer_names[identifier] = name
            else:
                parent = source_id(entry["manufacturerSourceID"])
                parent_record = row.get("manufacturer")
                if not isinstance(parent_record, dict) or source_id(parent_record.get("id")) != parent or parent not in manufacturer_names:
                    raise ValueError("manufacturer source mapping missing or mismatched")
                if entry["model"] != model_key(row.get("model")):
                    raise ValueError("model key must match normalized source model")
                spec.update(displayName=text(entry["displayName"]), manufacturerRef={"name": manufacturer_names[parent]}, model=entry["model"])
            candidate = {"apiVersion": "fleet.datumapis.com/v1alpha1", "kind": kind, "metadata": {"name": name, "namespace": target["namespace"]}, "spec": spec}
            if key in records:
                old_spec = records[key]["spec"]
                if {field: value for field, value in old_spec.items() if field != "provenance"} != {field: value for field, value in spec.items() if field != "provenance"}:
                    raise ValueError("existing target differs; no update, merge, rebind or takeover permitted")
            else:
                records[key] = candidate
                candidates.append(candidate)
    labels, models = set(), set()
    for (kind, name), record in records.items():
        spec = record["spec"]
        if kind == "Manufacturer":
            aliases = spec.get("aliases", [])
            if not isinstance(aliases, list) or len(aliases) > 32:
                raise ValueError("invalid existing aliases")
            for value in [spec.get("canonicalName"), *aliases]:
                normalized = label(value).lower()
                if normalized in labels:
                    raise ValueError("ambiguous canonical name or alias collision")
                labels.add(normalized)
        else:
            fields(spec.get("manufacturerRef"), ("name",))
            parent = spec["manufacturerRef"]["name"]
            model = text(spec.get("model"))
            if not name_ok(parent) or ("Manufacturer", parent) not in records or not re.fullmatch(r"[a-z0-9]+(-[a-z0-9]+)*", model) or (parent, model) in models:
                raise ValueError("missing manufacturer or duplicate/invalid normalized model")
            models.add((parent, model))
    return {"target": target, "items": candidates, "report": "Create-only candidates; no device migration, synchronization, GitOps or live effects. Re-read target before individual creates; reject existing names."}


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("duplicate JSON key")
        result[key] = value
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("snapshot", "mapping", "existing", "recorded-by", "recorded-at"):
        parser.add_argument("--" + name, required=True)
    args = parser.parse_args()
    try:
        inputs = []
        for path in (args.snapshot, args.mapping, args.existing):
            with open(path, encoding="utf-8") as stream:
                inputs.append(json.load(stream, object_pairs_hook=unique_object))
        result = plan(*inputs, args.recorded_by, args.recorded_at)
    except (ValueError, TypeError, KeyError, AttributeError, OSError) as error:
        print(json.dumps({"items": [], "quarantined": True, "reason": str(error)}))
        return 1
    print(json.dumps(result, indent=2))
    return 0


if __name__ == "__main__":
    sys.exit(main())
