package redfish

import (
	"context"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

// Discovery reads offered reset types inline or from the System's own
// ActionInfo, with GETs only. A reset target off the System records none.
func TestInventoryRecordsResetTypes(t *testing.T) {
	bmc := newInventoryBMC()
	bmc.docs["/redfish/v1/Systems"] = map[string]any{"Members": []any{
		map[string]string{"@odata.id": "/redfish/v1/Systems/1"},
		map[string]string{"@odata.id": "/redfish/v1/Systems/2"},
		map[string]string{"@odata.id": "/redfish/v1/Systems/3"},
	}}
	bmc.docs["/redfish/v1/Systems/1"].(map[string]any)["Actions"] = map[string]any{"#ComputerSystem.Reset": map[string]any{
		"target": "/redfish/v1/Systems/1/Actions/ComputerSystem.Reset", "@Redfish.ActionInfo": "/redfish/v1/Systems/1/ResetActionInfo"}}
	bmc.docs["/redfish/v1/Systems/1/ResetActionInfo"] = map[string]any{"Parameters": []any{map[string]any{
		"Name": "ResetType", "Required": "true", "DataType": "String", "AllowableValues": []string{"On", "ForceOff", "ForceRestart"}}}}
	bmc.docs["/redfish/v1/Systems/2"] = map[string]any{"Id": "2", "UUID": "00000000-0000-4000-8000-0000000000b2", "Actions": map[string]any{"#ComputerSystem.Reset": map[string]any{
		"target": "/redfish/v1/Systems/2/Actions/ComputerSystem.Reset", "ResetType@Redfish.AllowableValues": []string{"On", "PowerCycle"}}}}
	bmc.docs["/redfish/v1/Systems/3"] = map[string]any{"Id": "3", "UUID": "00000000-0000-4000-8000-0000000000c3", "Actions": map[string]any{"#ComputerSystem.Reset": map[string]any{
		"target": "/redfish/v1/Systems/1/Actions/ComputerSystem.Reset", "ResetType@Redfish.AllowableValues": []string{"On"}}}}
	server := httptest.NewTLSServer(bmc)
	t.Cleanup(server.Close)
	client, err := NewPinnedClient(server.URL, "operator", "synthetic-secret", PublicKeySHA256(server.Certificate()), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	inv, err := client.Inventory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Systems) != 3 || !slices.Equal(inv.Systems[0].ResetTypes, []string{"On", "ForceOff", "ForceRestart"}) ||
		!slices.Equal(inv.Systems[1].ResetTypes, []string{"On", "PowerCycle"}) || inv.Systems[2].ResetTypes != nil {
		t.Fatalf("systems = %+v", inv.Systems)
	}
	requests, _ := bmc.seen()
	for _, request := range requests {
		if !strings.HasPrefix(request, "GET ") || strings.Contains(request, "/Actions/") {
			t.Fatalf("discovery issued %s", request)
		}
	}
}

// An ActionInfo that cannot be read records no reset types and keeps the
// rest of the inventory.
func TestInventoryKeepsTheSystemWhenActionInfoFails(t *testing.T) {
	for name, info := range map[string]any{"missing": nil, "malformed": "not an ActionInfo"} {
		t.Run(name, func(t *testing.T) {
			bmc := newInventoryBMC()
			bmc.docs["/redfish/v1/Systems/1"].(map[string]any)["Actions"] = map[string]any{"#ComputerSystem.Reset": map[string]any{
				"target": "/redfish/v1/Systems/1/Actions/ComputerSystem.Reset", "@Redfish.ActionInfo": "/redfish/v1/Systems/1/ResetActionInfo"}}
			if info != nil {
				bmc.docs["/redfish/v1/Systems/1/ResetActionInfo"] = info
			}
			server := httptest.NewTLSServer(bmc)
			t.Cleanup(server.Close)
			client, err := NewPinnedClient(server.URL, "operator", "synthetic-secret", PublicKeySHA256(server.Certificate()), 5*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			inv, err := client.Inventory(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(inv.Systems) != 1 || inv.Systems[0].ResetTypes != nil || inv.Systems[0].PowerState != "Off" || len(inv.Systems[0].NetworkInterfaces) != 1 {
				t.Fatalf("systems = %+v", inv.Systems)
			}
		})
	}
}
