#!/usr/bin/env python3
"""Optional operator-run review aid: GET-only snapshots and offline candidates.

Manual inventory entry needs neither NetBox nor this tool. Nothing is applied,
scheduled, synchronized, or deleted; an operator reviews every proposed record.
"""

import argparse
import copy
import datetime
import hashlib
import json
import os
from pathlib import Path
import re
import sys
import time
import urllib.error
import urllib.parse
import urllib.request


COMPONENTS = {
    "interfaces": "NetworkPort", "power-ports": "PowerInlet",
    "power-outlets": "PowerOutlet", "console-ports": "ConsolePort",
    "console-server-ports": "ConsoleServerPort", "front-ports": "FrontPort",
    "rear-ports": "RearPort", "modules": "Module",
    "inventory-items": "InventoryItem", "device-bays": "DeviceBay",
}
SUPPORT = {"manufacturers", "device-types", "module-types", "device-roles", "rack-types"}
REQUIRED = {"dcim/" + name for name in (
    "sites", "locations", "racks", "devices", "device-types", "manufacturers",
    "cables", "power-feeds", "power-panels", *COMPONENTS,
)} | {"circuits/circuits", "circuits/circuit-terminations"}
FIELDS = {
    "id", "name", "display", "slug", "model", "manufacturer", "device_type",
    "module_type", "device", "site", "location", "rack", "region", "parent",
    "role", "type", "status", "serial", "asset_tag", "position", "face",
    "u_height", "is_full_depth", "rear_port", "rear_port_position", "positions",
    "power_port", "power_panel", "supply", "voltage", "amperage", "phase",
    "a_terminations", "b_terminations", "object_type", "object_id", "object",
    "termination_type", "termination_id", "termination", "circuit", "cid",
    "provider", "term_side", "label", "value", "last_updated", "created",
}


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=True)


def digest(value):
    return "sha256:" + hashlib.sha256(canonical(value).encode()).hexdigest()


def reference_id(value):
    if isinstance(value, dict):
        value = value.get("id")
    return str(value) if value is not None else ""


def value(record, key, default=""):
    item = record.get(key, default)
    return item.get("value", default) if isinstance(item, dict) else item


def sanitize(item):
    if isinstance(item, dict):
        return {key: sanitize(child) for key, child in item.items() if key in FIELDS}
    if isinstance(item, list):
        return [sanitize(child) for child in item]
    return item


def name_ok(name):
    return isinstance(name, str) and len(name) <= 253 and re.fullmatch(r"[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?", name)


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, response, code, message, headers, url):
        raise ValueError("NetBox redirects are refused; configure the canonical HTTPS origin")


class Reader:
    def __init__(self, url, token, auth_scheme="Bearer"):
        self.base = url.rstrip("/")
        parsed = urllib.parse.urlsplit(self.base)
        if parsed.scheme != "https" or not parsed.hostname or parsed.username or parsed.password or parsed.query or parsed.fragment or parsed.path:
            raise ValueError("NetBox URL must be a credential-free HTTPS origin")
        if auth_scheme not in ("Bearer", "Token"):
            raise ValueError("unsupported authorization scheme")
        self.authorization = auth_scheme + " " + token.strip()
        self.opener = urllib.request.build_opener(NoRedirect())

    def get(self, url):
        parsed = urllib.parse.urlsplit(url)
        base = urllib.parse.urlsplit(self.base)
        if parsed.scheme != base.scheme or parsed.netloc != base.netloc or not parsed.path.startswith("/api/") or parsed.fragment or parsed.username or parsed.password:
            raise ValueError("refusing off-origin or non-API pagination link")
        request = urllib.request.Request(url, headers={"Authorization": self.authorization, "Accept": "application/json"}, method="GET")
        for attempt in range(3):
            try:
                with self.opener.open(request, timeout=30) as response:
                    return json.load(response)
            except urllib.error.HTTPError as error:
                if error.code not in (429, 500, 502, 503, 504) or attempt == 2:
                    raise ValueError("NetBox GET failed with HTTP " + str(error.code)) from None
            except (urllib.error.URLError, TimeoutError):
                if attempt == 2:
                    raise ValueError("NetBox GET failed: network unavailable") from None
            time.sleep(2 ** attempt)

    def collection(self, path):
        url = self.base + "/api/" + path.strip("/") + "/?limit=200"
        seen_urls, records, count = set(), {}, None
        while url:
            if url in seen_urls:
                raise ValueError("pagination cycle")
            seen_urls.add(url)
            page = self.get(url)
            if not isinstance(page, dict) or not isinstance(page.get("results"), list) or not isinstance(page.get("count"), int):
                raise ValueError("invalid paginated collection")
            if count is not None and count != page["count"]:
                raise ValueError("source count changed during export; restart the snapshot")
            count = page["count"]
            if count < 0 or (not page["results"] and page.get("next")):
                raise ValueError("invalid count or empty nonterminal page")
            for record in page["results"]:
                identity = reference_id(record)
                if not identity or identity in records:
                    raise ValueError("missing or repeated source ID during pagination")
                records[identity] = sanitize(record)
            if len(records) > count:
                raise ValueError("snapshot exceeds advertised source count")
            next_url = page.get("next")
            url = urllib.parse.urljoin(url, next_url) if next_url else None
            if url and urllib.parse.urlsplit(url).path != "/api/" + path.strip("/") + "/":
                raise ValueError("pagination changed collection")
        if len(records) != count:
            raise ValueError("source count does not match snapshot")
        return sorted(records.values(), key=lambda record: reference_id(record))


def snapshot(reader, source):
    if not name_ok(source):
        raise ValueError("source must be a stable DNS-style instance name")
    started = datetime.datetime.now(datetime.timezone.utc).isoformat()
    objects, errors, excluded_endpoints = {}, {}, {}
    for app in ("dcim", "circuits"):
        try:
            discovery = reader.get(reader.base + "/api/" + app + "/")
            if not isinstance(discovery, dict):
                raise ValueError("invalid API discovery")
        except ValueError as error:
            errors[app + "/"] = str(error)
            continue
        for collection in sorted(discovery):
            if not re.fullmatch(r"[a-z0-9-]+", collection):
                raise ValueError("invalid API collection name")
            path = app + "/" + collection
            if path == "dcim/connected-device":
                excluded_endpoints[path] = "parameterized connectivity lookup, not an inventory collection"
                continue
            try:
                objects[path] = reader.collection(path)
            except ValueError as error:
                errors[path] = str(error)
    for path in REQUIRED - objects.keys() - errors.keys():
        errors[path] = "required collection not advertised by this NetBox version"
    return {"version": 1, "source": source, "observedAt": started, "completedAt": datetime.datetime.now(datetime.timezone.utc).isoformat(), "objects": objects, "errors": errors, "excludedEndpoints": excluded_endpoints}


def local_ref(name):
    return {"name": name}


class Planner:
    def __init__(self, source, mapping, existing=None, previous=None):
        self.snapshot = source
        self.mapping = mapping
        self.source = source.get("source", "")
        if source.get("version") != 1 or not name_ok(self.source):
            raise ValueError("unsupported snapshot or invalid source identity")
        datetime.datetime.fromisoformat(source["observedAt"].replace("Z", "+00:00"))
        self.target = mapping.get("target", {})
        self.namespace = self.target.get("namespace", "")
        self.revision = mapping.get("revision", "")
        self.records, self.report, self.candidates = {}, {}, {}
        self.existing = existing
        if existing is not None and existing.get("target") != self.target:
            raise ValueError("Fleet snapshot target must exactly match the reviewed provider project, namespace and application")
        if existing is not None and set(existing.get("collections", [])) != {"Facility", "Rack", "DeviceClaim", "Device", "Cable", "PowerFeed"}:
            raise ValueError("Fleet snapshot must attest all six inventory collections, including empty ones")
        self.previous = previous
        self.digest = digest(source)
        self.device_names, self.rack_names, self.feed_names = {}, {}, {}
        for collection, records in source.get("objects", {}).items():
            for record in records:
                identity = reference_id(record)
                key = collection + "/" + identity
                if not identity or key in self.records:
                    raise ValueError("duplicate or absent source identity")
                self.records[key] = record
                self.report[key] = {"source": key, "classification": "unsupported", "reason": "source object class has no physical adapter", "targets": []}

    def rows(self, collection):
        return [(key, record) for key, record in sorted(self.records.items()) if key.rsplit("/", 1)[0] == collection]

    def mark(self, key, classification, reason, targets=()):
        self.report[key] = {"source": key, "classification": classification, "reason": reason, "targets": list(targets)}

    def stable_name(self, kind, identity):
        return "nb-" + hashlib.sha256((self.source + "/" + kind + "/" + identity).encode()).hexdigest()[:24]

    def external(self, key):
        collection, identity = key.rsplit("/", 1)
        return {"system": self.source, "type": collection, "id": identity}

    def add(self, key, kind, name, spec):
        if not name_ok(name):
            raise ValueError("invalid mapped Fleet name")
        provenance = {"method": "NetBoxImport", "recordedBy": "netbox-inventory-offline", "recordedAt": self.snapshot["observedAt"], "source": dict(self.external(key), snapshotDigest=self.digest, mappingRevision=self.revision or "unreviewed", observedAt=self.snapshot["observedAt"])}
        record = {"apiVersion": "fleet.datumapis.com/v1alpha1", "kind": kind, "metadata": {"name": name, "namespace": self.namespace}, "spec": dict(spec, provenance=provenance)}
        target = kind + "/" + name
        if target in self.candidates:
            raise ValueError("multiple source records mapped to the same target")
        self.candidates[target] = record
        self.mark(key, "imported", "observational candidate; not applied or verified", [*self.report[key]["targets"], target])
        return target

    def facilities(self):
        for key, record in self.rows("dcim/sites") + self.rows("dcim/locations"):
            self.mark(key, "unresolved", "provider/metro sites and locations require an explicit premises mapping")
        for facility in self.mapping.get("facilities", []):
            key = facility["source"]
            if key not in self.records or key.rsplit("/", 1)[0] not in ("dcim/sites", "dcim/locations"):
                raise ValueError("facility source must name a present site or location")
            spec = {"displayName": facility["displayName"]}
            if facility.get("locationRef"):
                spec["locationRef"] = local_ref(facility["locationRef"])
            self.add(key, "Facility", facility["name"], spec)
        for key, record in self.rows("dcim/racks"):
            selected = self.mapping.get("racks", {}).get(reference_id(record))
            if not selected or "Facility/" + selected.get("facility", "") not in self.candidates:
                self.mark(key, "unresolved", "rack needs a mapped physical facility")
                continue
            name = self.stable_name("rack", reference_id(record))
            spec = {"displayName": record.get("name", name), "facilityRef": local_ref(selected["facility"])}
            if isinstance(record.get("u_height"), int) and 1 <= record["u_height"] <= 100:
                spec["heightUnits"] = record["u_height"]
            self.add(key, "Rack", name, spec)
            self.rack_names[reference_id(record)] = name

    def devices(self):
        for key, record in self.rows("dcim/devices"):
            identity = reference_id(record)
            selected = self.mapping.get("devices", {}).get(identity, {})
            role = record.get("role") or {}
            dtype = record.get("device_type") or {}
            model = self.records.get("dcim/device-types/" + reference_id(dtype), dtype)
            manufacturer = model.get("manufacturer") or {}
            hints = canonical([role, manufacturer, model.get("model", "")]).lower()
            if "netactuate" in hints or "hosted-vm" in hints:
                self.mark(key, "excluded", "NetActuate/hosted VM represented in DCIM; not a physical asset")
                continue
            if selected.get("classification") == "excluded" and selected.get("reason"):
                self.mark(key, "excluded", selected["reason"])
                continue
            if selected.get("classification") != "physical" or not selected.get("serialMeaning") in ("chassis", "providerHostID", "unknown"):
                self.mark(key, "unresolved", "review physical classification and serial meaning for this device")
                continue
            placement = selected.get("placement", {})
            rack_id = reference_id(record.get("rack"))
            if placement.get("scope") == "RackMembership" and rack_id in self.rack_names:
                placement = {"scope": "RackMembership", "rackRef": local_ref(self.rack_names[rack_id])}
            elif placement.get("scope") == "Facility" and "Facility/" + placement.get("facility", "") in self.candidates:
                placement = {"scope": "Facility", "facilityRef": local_ref(placement["facility"])}
            else:
                self.mark(key, "unresolved", "import accepts reviewed facility or known-rack/unknown-position placement only")
                continue
            if not model.get("model"):
                self.mark(key, "unresolved", "device model is absent")
                continue
            name = self.stable_name("device", identity)
            claim_name = self.stable_name("installation", identity)
            asset_identity = {"model": model["model"], "sourceIdentity": self.external(key)}
            if manufacturer.get("name"):
                asset_identity["manufacturer"] = manufacturer["name"]
            serial = record.get("serial")
            if serial and ("servers.com" in hints or "serversdotcom" in hints) and selected["serialMeaning"] == "chassis":
                self.mark(key, "unresolved", "servers.com loader serial is a provider host ID, not a chassis serial")
                continue
            if serial and selected["serialMeaning"] == "chassis":
                asset_identity["serial"] = serial
            spec = {"displayName": record.get("name") or name, "claimRef": local_ref(claim_name), "identity": asset_identity, "topologyAuthority": "Cable"}
            shape = {}
            try:
                height = float(model.get("u_height", ""))
                if 0 <= height <= 100:
                    shape["heightUnits"] = height
            except (ValueError, TypeError):
                pass
            if isinstance(model.get("is_full_depth"), bool):
                shape["fullDepth"] = model["is_full_depth"]
            if shape:
                spec["modelShape"] = shape
            if serial and selected["serialMeaning"] == "providerHostID":
                if not selected.get("provider"):
                    self.mark(key, "unresolved", "provider host ID requires its provider authority")
                    continue
                spec["providerHostRef"] = {"system": selected["provider"], "type": "host", "id": serial}
            if record.get("asset_tag"):
                spec["assetTag"] = record["asset_tag"]
            self.add(key, "DeviceClaim", claim_name, {"displayName": record.get("name") or name, "lifecycle": "Installation", "expectedIdentity": {name: item for name, item in asset_identity.items() if name != "sourceIdentity"}, "placement": placement})
            self.add(key, "Device", name, spec)
            self.device_names[identity] = name

    def components(self):
        for collection, kind in COMPONENTS.items():
            for key, record in self.rows("dcim/" + collection):
                device_id = reference_id(record.get("device"))
                if device_id not in self.device_names:
                    parent = self.report.get("dcim/devices/" + device_id, {})
                    state = "excluded" if parent.get("classification") == "excluded" else "unresolved"
                    self.mark(key, state, "parent device is excluded or unresolved")
                    continue
                if collection == "interfaces" and value(record, "type") in ("virtual", "lag", "bridge"):
                    self.mark(key, "excluded", "logical interface belongs to networking, not physical inventory")
                    continue
                target = "Device/" + self.device_names[device_id]
                component = {"id": collection + "-" + reference_id(record), "name": record.get("name") or record.get("display") or reference_id(record), "type": kind, "sourceRef": self.external(key)}
                connector = value(record, "type")
                if connector:
                    component["connector"] = connector
                positions = record.get("positions")
                if isinstance(positions, int) and 1 <= positions <= 256:
                    component["positions"] = positions
                for source_field, target_field in (("serial", "serial"), ("asset_tag", "assetTag")):
                    if record.get(source_field):
                        component[target_field] = record[source_field]
                self.candidates[target]["spec"].setdefault("components", []).append(component)
                self.mark(key, "imported", "typed component on the parent asset", [target])
        for collection, parent_field, parent_collection in (("front-ports", "rear_port", "rear-ports"), ("power-outlets", "power_port", "power-ports")):
            for key, record in self.rows("dcim/" + collection):
                if self.report[key]["classification"] != "imported":
                    continue
                parent_id = reference_id(record.get(parent_field))
                if not parent_id:
                    self.report[key]["reason"] += "; internal dependency Unknown"
                    continue
                target = self.report[key]["targets"][0]
                parent_key = "dcim/" + parent_collection + "/" + parent_id
                if self.report.get(parent_key, {}).get("targets") != [target]:
                    self.mark(key, "unresolved", "internal dependency has a missing or different parent device", [target])
                    continue
                parent_position = 1
                if collection == "front-ports":
                    parent_position = record.get("rear_port_position")
                    if type(parent_position) is not int or not 1 <= parent_position <= 256:
                        self.mark(key, "unresolved", "front port requires an explicit valid rear-port position; never infer position one", [target])
                        continue
                mapping = {"from": {"componentID": collection + "-" + reference_id(record), "position": 1}, "to": {"componentID": parent_collection + "-" + parent_id, "position": parent_position}}
                self.candidates[target]["spec"].setdefault("internalMappings", []).append(mapping)

    def feeds(self):
        for key, record in self.rows("dcim/power-panels"):
            self.mark(key, "unresolved", "panel is external electrical dependency evidence, not a fabricated asset")
        for key, record in self.rows("dcim/power-feeds"):
            selected = self.mapping.get("feeds", {}).get(reference_id(record), {})
            if "Facility/" + selected.get("facility", "") not in self.candidates:
                self.mark(key, "unresolved", "feed requires an explicit facility mapping")
                continue
            name = self.stable_name("feed", reference_id(record))
            spec = {"displayName": record.get("name", name), "facilityRef": local_ref(selected["facility"]), "terminations": [{"id": "output", "name": "output", "type": "PowerOutlet"}]}
            rack = self.rack_names.get(reference_id(record.get("rack")))
            if rack:
                spec["rackRef"] = local_ref(rack)
            supply = {"type": str(value(record, "supply", "Unknown")).upper()}
            if supply["type"] not in ("AC", "DC"):
                supply["type"] = "Unknown"
            for field, target_field in (("voltage", "voltageV"), ("amperage", "currentA")):
                if isinstance(record.get(field), int) and 0 < record[field] <= 100000:
                    supply[target_field] = record[field]
            if value(record, "phase") in ("single-phase", "three-phase"):
                supply["phase"] = value(record, "phase")
            spec["supply"] = supply
            panel_key = "dcim/power-panels/" + reference_id(record.get("power_panel"))
            if panel_key in self.records:
                spec["upstreamSources"] = [self.external(panel_key)]
                self.mark(panel_key, "imported", "external electrical panel reference only; upstream independence Unknown", sorted(set(self.report[panel_key]["targets"] + ["PowerFeed/" + name])))
            self.add(key, "PowerFeed", name, spec)
            self.feed_names[reference_id(record)] = name

    def endpoint(self, termination):
        kind = termination.get("object_type") or termination.get("termination_type")
        identity = reference_id(termination.get("object_id") or termination.get("termination_id") or termination.get("object") or termination.get("termination"))
        if kind == "dcim.powerfeed" and identity in self.feed_names:
            return {"powerFeedRef": local_ref(self.feed_names[identity]), "componentID": "output", "position": 1}
        collection = next((name for name in COMPONENTS if "dcim." + name.replace("-", "").rstrip("s") == kind), None)
        if kind == "dcim.inventoryitem":
            collection = "inventory-items"
        key = "dcim/" + str(collection) + "/" + identity
        if kind == "circuits.circuittermination":
            selected = self.mapping.get("demarcations", {}).get(identity)
            if not selected:
                return None
            key = selected.get("componentSource", "")
        state = self.report.get(key, {})
        if state.get("classification") != "imported" or not state.get("targets"):
            return None
        target = state["targets"][0]
        if not target.startswith("Device/"):
            return None
        source_class, source_id = key.rsplit("/", 1)
        return {"deviceRef": local_ref(target.split("/", 1)[1]), "componentID": source_class.split("/")[1] + "-" + source_id, "position": 1}

    def circuits(self):
        for key, record in self.rows("circuits/circuits"):
            self.mark(key, "unresolved", "circuit requires an external service-owner reference; no Fleet Circuit kind")
        for key, record in self.rows("circuits/circuit-terminations"):
            circuit_key = "circuits/circuits/" + reference_id(record.get("circuit"))
            owner = self.mapping.get("circuits", {}).get(reference_id(record.get("circuit")))
            endpoint = self.endpoint({"object_type": "circuits.circuittermination", "object_id": reference_id(record)})
            if not owner or not all(owner.get(field) for field in ("system", "type", "id")) or not endpoint or circuit_key not in self.records:
                self.mark(key, "unresolved", "local demarcation and external service owner must both be mapped")
                continue
            target = "Device/" + endpoint["deviceRef"]["name"]
            self.candidates[target]["spec"].setdefault("serviceAssociations", []).append({"serviceRef": {field: owner[field] for field in ("system", "type", "id")}, "componentID": endpoint["componentID"]})
            self.mark(key, "imported", "local physical demarcation association only", [target])
            self.mark(circuit_key, "imported", "external service-owner reference only; no service or IPAM migration", sorted(set(self.report[circuit_key]["targets"] + [target])))

    def cables(self):
        for key, record in self.rows("dcim/cables"):
            ends = [record.get("a_terminations", []), record.get("b_terminations", [])]
            selected = self.mapping.get("cables", {}).get(reference_id(record), {})
            lanes = selected.get("lanes")
            available = [[self.endpoint(termination) for termination in end] for end in ends]
            if not all(available) or any(endpoint is None for side in available for endpoint in side):
                self.mark(key, "unresolved", "cable endpoint is missing, excluded, or has no reviewed demarcation")
                continue
            if lanes:
                lanes = copy.deepcopy(lanes)
                represented = [set(), set()]
                valid = isinstance(lanes, list) and len(lanes) <= 64
                for lane in lanes if valid else []:
                    if not isinstance(lane, dict) or not name_ok(lane.get("id")):
                        valid = False
                        break
                    for index, side in enumerate(("a", "b")):
                        endpoint = lane.get(side)
                        if not isinstance(endpoint, dict) or not isinstance(endpoint.get("position"), int) or endpoint["position"] < 1:
                            valid = False
                            break
                        base_endpoint = dict(endpoint, position=1)
                        if base_endpoint not in available[index]:
                            valid = False
                            break
                        represented[index].add(canonical(base_endpoint))
                if not valid or any(represented[index] != {canonical(endpoint) for endpoint in available[index]} for index in (0, 1)):
                    self.mark(key, "unresolved", "reviewed lanes must account for every source termination on its original side")
                    continue
            else:
                if any(len(end) != 1 for end in ends):
                    self.mark(key, "unsupported", "multi-termination cable needs reviewed explicit lane mappings; no all-to-all inference")
                    continue
                endpoints = [end[0] for end in available]
                multiposition = False
                for endpoint in endpoints:
                    if endpoint.get("deviceRef"):
                        device = self.candidates["Device/" + endpoint["deviceRef"]["name"]]
                        component = next(component for component in device["spec"]["components"] if component["id"] == endpoint["componentID"])
                        multiposition |= component.get("positions", 1) > 1
                if multiposition:
                    self.mark(key, "unresolved", "multi-position termination needs explicit lanes; never infer position one for a trunk")
                    continue
                lanes = [{"id": "lane-1", "a": endpoints[0], "b": endpoints[1]}]
            medium = selected.get("medium")
            if medium not in ("Copper", "Fiber", "Power", "Console"):
                self.mark(key, "unresolved", "cable medium requires review; source connector codes are not qualification")
                continue
            name = self.stable_name("cable", reference_id(record))
            self.add(key, "Cable", name, {"displayName": record.get("label") or name, "medium": medium, "lanes": lanes})

    def reconcile(self):
        current = {}
        for record in (self.existing or {}).get("items", []):
            if record.get("apiVersion") != "fleet.datumapis.com/v1alpha1" or record.get("metadata", {}).get("namespace") != self.namespace:
                raise ValueError("Fleet snapshot must contain only this namespace's physical inventory")
            key = record["kind"] + "/" + record["metadata"]["name"]
            if key in current:
                raise ValueError("duplicate Fleet target identity")
            current[key] = record
        conflicts, unchanged = {}, set()
        for key, candidate in self.candidates.items():
            if key in current:
                old_spec, new_spec = copy.deepcopy(current[key]["spec"]), copy.deepcopy(candidate["spec"])
                old_provenance, new_provenance = old_spec.pop("provenance"), new_spec.pop("provenance")
                source_fields = ("system", "type", "id")
                same_source = all(old_provenance.get("source", {}).get(field) == new_provenance["source"][field] for field in source_fields)
                if same_source and old_spec == new_spec:
                    unchanged.add(key)
                else:
                    conflicts[key] = "existing target differs; no automatic update, claim rebinding, or ownership takeover"
            if candidate["kind"] == "Device":
                spec = candidate["spec"]
                for other_key, other in {**current, **self.candidates}.items():
                    if other_key == key or other.get("kind") != "Device":
                        continue
                    other_spec = other["spec"]
                    pairs = [(spec.get("assetTag"), other_spec.get("assetTag")), (spec.get("providerHostRef"), other_spec.get("providerHostRef")), (spec.get("providerHostRef", {}).get("id"), other_spec["identity"].get("serial")), (spec["identity"].get("serial"), other_spec["identity"].get("serial")), (spec["identity"].get("sourceIdentity"), other_spec["identity"].get("sourceIdentity"))]
                    if any(left and left == right for left, right in pairs):
                        conflicts[key] = "possible existing asset identity; preserve Device/claim/capacity and adjudicate privately"
        all_records = {**current, **self.candidates}
        for key, reasons in validate_graph(all_records).items():
            if key in self.candidates:
                conflicts[key] = "; ".join(reasons)
        changed = True
        while changed:
            changed = False
            for entry in self.report.values():
                if entry["classification"] == "unresolved":
                    for target in entry["targets"]:
                        if target not in conflicts:
                            conflicts[target] = "one or more source relationships are unresolved"
                            changed = True
                if any(target in conflicts for target in entry["targets"]):
                    for target in entry["targets"]:
                        if target not in conflicts:
                            conflicts[target] = "same source record has a quarantined dependency"
                            changed = True
            for key, record in self.candidates.items():
                if key not in conflicts and any(target in conflicts for target in dependencies(record)):
                    conflicts[key] = "referenced candidate is quarantined"
                    changed = True
        for entry in self.report.values():
            failed = [target for target in entry["targets"] if target in conflicts]
            if failed:
                entry.update(classification="unresolved", reason=conflicts[failed[0]])
            elif entry["targets"] and all(target in unchanged for target in entry["targets"]):
                entry["reason"] = "already present with same source identity and physical fields; no write"
        self.candidates = {key: record for key, record in self.candidates.items() if key not in conflicts and key not in unchanged}
        return conflicts

    def run(self):
        self.facilities()
        self.devices()
        self.components()
        self.feeds()
        self.circuits()
        self.cables()
        for key in self.records:
            collection = key.rsplit("/", 1)[0]
            if collection.startswith("dcim/") and collection.split("/")[1] in SUPPORT:
                self.mark(key, "excluded", "model/catalog metadata consumed where referenced; no independent physical asset")
            elif collection.endswith("-templates"):
                self.mark(key, "excluded", "model template is not installed hardware; actual instances imported separately")
            elif collection in ("dcim/regions", "dcim/site-groups", "dcim/rack-roles", "dcim/virtual-chassis", "circuits/providers", "circuits/provider-accounts", "circuits/provider-networks", "circuits/circuit-types"):
                self.mark(key, "excluded", "grouping or service-owner metadata; not physical premises or installed assets")
        conflicts = self.reconcile()
        omitted = sorted(set((self.previous or {}).get("objects", {})) - set(self.snapshot.get("objects", {})))
        prior_keys = {collection + "/" + reference_id(record) for collection, rows in (self.previous or {}).get("objects", {}).items() for record in rows}
        if self.previous and self.previous.get("source") != self.source:
            raise ValueError("previous snapshot belongs to another source")
        errors = dict(self.snapshot.get("errors", {}))
        for collection in REQUIRED - self.snapshot.get("objects", {}).keys():
            errors.setdefault(collection, "required collection absent; coverage unknown")
        retained = []
        for record in (self.existing or {}).get("items", []):
            source = record.get("spec", {}).get("provenance", {}).get("source", {})
            if source.get("system") == self.source and source.get("type", "") + "/" + source.get("id", "") not in self.records:
                retained.append(record["kind"] + "/" + record["metadata"]["name"])
        return {"version": 1, "source": self.source, "snapshotDigest": self.digest, "observedAt": self.snapshot["observedAt"], "target": self.target, "mappingRevision": self.revision, "coverageComplete": not errors, "collectionErrors": errors, "excludedEndpoints": self.snapshot.get("excludedEndpoints", {}), "omittedCollections": omitted, "missingSincePrevious": sorted(prior_keys - self.records.keys()), "retainedMissingSource": retained, "deletions": [], "conflicts": conflicts, "records": list(self.report.values()), "candidateCount": len(self.candidates), "readiness": "Unknown", "applied": False}, list(self.candidates.values())


def dependencies(record):
    spec = record["spec"]
    references = []
    for field, kind in (("claimRef", "DeviceClaim"), ("facilityRef", "Facility"), ("rackRef", "Rack")):
        if spec.get(field):
            references.append(kind + "/" + spec[field]["name"])
        if spec.get("placement", {}).get(field):
            references.append(kind + "/" + spec["placement"][field]["name"])
    for lane in spec.get("lanes", []):
        for endpoint in (lane["a"], lane["b"]):
            for field, kind in (("deviceRef", "Device"), ("powerFeedRef", "PowerFeed")):
                if endpoint.get(field):
                    references.append(kind + "/" + endpoint[field]["name"])
    return references


def validate_graph(records):
    errors, occupied = {}, {}
    def fail(key, reason):
        errors.setdefault(key, []).append(reason)
    for key, record in records.items():
        for target in dependencies(record):
            if target not in records:
                fail(key, "missing local reference " + target)
        spec = record["spec"]
        components = {component["id"]: component for component in spec.get("components", [])}
        mapped_positions = set()
        for mapping in spec.get("internalMappings", []):
            for end in (mapping["from"], mapping["to"]):
                component = components.get(end["componentID"])
                if not component or not 1 <= end["position"] <= component.get("positions", 1):
                    fail(key, "invalid internal component mapping")
            source_type = components.get(mapping["from"]["componentID"], {}).get("type")
            target_type = components.get(mapping["to"]["componentID"], {}).get("type")
            if (source_type, target_type) not in (("FrontPort", "RearPort"), ("PowerOutlet", "PowerInlet")):
                fail(key, "incompatible internal component types")
            if source_type == "FrontPort":
                position = canonical(mapping["to"])
                if position in mapped_positions:
                    fail(key, "pass-through rear position mapped more than once")
                mapped_positions.add(position)
        for association in spec.get("serviceAssociations", []):
            if association["componentID"] not in components:
                fail(key, "service demarcation names a missing component")
        for lane in spec.get("lanes", []):
            kinds = []
            for endpoint in (lane["a"], lane["b"]):
                owner = ("Device/" + endpoint["deviceRef"]["name"]) if endpoint.get("deviceRef") else ("PowerFeed/" + endpoint["powerFeedRef"]["name"])
                target = records.get(owner, {}).get("spec", {})
                components = target.get("components", []) + target.get("terminations", [])
                component = next((component for component in components if component["id"] == endpoint["componentID"]), None)
                if not component or not 1 <= endpoint["position"] <= component.get("positions", 1):
                    fail(key, "unresolved endpoint component or position")
                    continue
                kinds.append(component["type"])
                connector = component.get("connector", "").lower()
                if spec.get("medium") == "Fiber" and ("base-t" in connector or "rj45" in connector):
                    fail(key, "fiber cable on a copper termination")
                identity = owner + "/" + endpoint["componentID"] + "/" + str(endpoint["position"])
                if identity in occupied:
                    fail(key, "termination already used")
                    fail(occupied[identity], "termination already used")
                occupied[identity] = key
            allowed = {"Power": {"PowerInlet", "PowerOutlet"}, "Console": {"ConsolePort", "ConsoleServerPort", "FrontPort", "RearPort"}, "Copper": {"NetworkPort", "FrontPort", "RearPort"}, "Fiber": {"NetworkPort", "FrontPort", "RearPort"}}
            if spec.get("medium") not in allowed or not set(kinds) <= allowed[spec["medium"]]:
                fail(key, "incompatible or unknown cable medium and component types")
            if spec.get("medium") == "Power" and sorted(kinds) != ["PowerInlet", "PowerOutlet"]:
                fail(key, "power cable must connect an outlet to an inlet")
            if spec.get("medium") == "Console" and all(kind in ("ConsolePort", "ConsoleServerPort") for kind in kinds) and sorted(kinds) != ["ConsolePort", "ConsoleServerPort"]:
                fail(key, "unsupported console direction; adapter mapping required")
    return errors


def read_json(path):
    with open(path) as stream:
        return json.load(stream)


def write_private(path, content):
    descriptor = os.open(path, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
    with os.fdopen(descriptor, "w") as stream:
        json.dump(content, stream, sort_keys=True, indent=2)
        stream.write("\n")


def private_output(path):
    resolved = path.resolve()
    if any((parent / ".git").exists() for parent in (resolved, *resolved.parents)):
        raise ValueError("private snapshots and handoffs must be written outside Git checkouts")
    path.mkdir(mode=0o700, parents=False, exist_ok=False)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    subparsers = parser.add_subparsers(dest="command", required=True)
    export = subparsers.add_parser("snapshot", help="explicitly read a private NetBox snapshot; optional")
    export.add_argument("--url", default=os.environ.get("NETBOX_URL", "https://netbox.datum-infra.net"))
    export.add_argument("--source", required=True)
    export.add_argument("--auth-scheme", choices=("Bearer", "Token"), default="Bearer")
    export.add_argument("--output", required=True)
    plan = subparsers.add_parser("plan", help="review a local snapshot offline; never apply records")
    plan.add_argument("--snapshot", required=True)
    plan.add_argument("--mapping")
    plan.add_argument("--existing")
    plan.add_argument("--previous")
    plan.add_argument("--output", required=True)
    plan.add_argument("--handoff", action="store_true", help="also write optional review candidates; never apply, commit, or push")
    plan.add_argument("--allow-partial", action="store_true")
    args = parser.parse_args(argv)
    output = Path(args.output)
    private_output(output)
    if args.command == "snapshot":
        token = os.environ.get("NETBOX_TOKEN")
        if not token:
            token_path = Path(os.environ.get("NETBOX_TOKEN_FILE", "~/.config/netbox/token")).expanduser()
            if not token_path.is_file():
                raise ValueError("no authorized NetBox read credential in the documented environment/file paths")
            token = token_path.read_text().strip()
        result = snapshot(Reader(args.url, token, args.auth_scheme), args.source)
        write_private(output / "snapshot.json", result)
        print("Private snapshot saved; collection errors:", len(result["errors"]))
        return 2 if result["errors"] else 0
    mapping = read_json(args.mapping) if args.mapping else {}
    planner = Planner(read_json(args.snapshot), mapping, read_json(args.existing) if args.existing else None, read_json(args.previous) if args.previous else None)
    report, candidates = planner.run()
    write_private(output / "report.json", report)
    if args.handoff:
        target = mapping.get("target", {})
        if not args.existing or not mapping.get("revision") or not name_ok(target.get("project")) or not name_ok(target.get("namespace")) or target.get("writer") != "Flux" or target.get("application") != "provider-inventory":
            raise ValueError("handoff requires an explicit provider project/namespace, reviewed mapping revision, Flux/provider-inventory target, and current Fleet snapshot")
        if report["collectionErrors"] or report["conflicts"]:
            raise ValueError("handoff blocked by incomplete source coverage or reconciliation conflicts; inspect the private report")
        if not args.allow_partial and any(row["classification"] in ("unresolved", "unsupported") for row in report["records"]):
            raise ValueError("handoff has unresolved or unsupported records; review the report and explicitly choose --allow-partial for a partial cohort")
        write_private(output / "candidates.json", {"apiVersion": "v1", "kind": "List", "items": candidates})
        write_private(output / "handoff.json", {"target": target, "snapshotDigest": report["snapshotDigest"], "mappingDigest": digest(mapping), "existingDigest": digest(planner.existing), "candidateDigest": digest(candidates), "application": "review and add individual new records to the private Flux provider-inventory source; never replace or prune that source", "applied": False})
    print("Private report saved; observational candidates:", len(candidates), "(nothing applied)")
    counts = {state: sum(row["classification"] == state for row in report["records"]) for state in ("imported", "unresolved", "unsupported", "excluded")}
    print("Record classifications:", canonical(counts), "; collection errors:", len(report["collectionErrors"]), "; readiness: Unknown")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (ValueError, OSError, KeyError, TypeError) as error:
        print("Inventory import stopped:", str(error) if isinstance(error, ValueError) else type(error).__name__, file=sys.stderr)
        sys.exit(2)
