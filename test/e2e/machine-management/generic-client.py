#!/usr/bin/env python3
"""Exercise Fleet through DMTF's unmodified Python Redfish client."""

import json
import os

import redfish


with open(os.environ["FLEET_CREDENTIAL_FILE"], encoding="utf-8") as source:
    credential = json.load(source)

client = redfish.redfish_client(
    base_url=credential["endpoint"].removesuffix("/redfish/v1/"),
    username=credential["username"],
    password=credential["password"],
    cafile=os.environ.get("FLEET_CA_BUNDLE", credential["caBundle"]),
    default_prefix="/redfish/v1",
)
client.login(auth="session")
root = client.get("/redfish/v1/")
systems = client.get("/redfish/v1/Systems")
system = client.get("/redfish/v1/Systems/allocated")
assert root.status == 200, root.status
assert systems.status == 200 and systems.dict["Members@odata.count"] == 1
assert system.status == 200 and system.dict["Id"] == "allocated"
assert "Managers" not in root.dict and "AccountService" not in root.dict
client.logout()
print("generic DMTF Redfish client: PASS")
