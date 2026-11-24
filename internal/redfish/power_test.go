package redfish

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	powerUUID = "00000000-0000-4000-8000-0000000000a1"
	otherUUID = "00000000-0000-4000-8000-0000000000b2"
)

// powerBMC serves one System, or two, and counts Reset POSTs.
func powerBMC(t *testing.T, mutate func(docs map[string]any)) (*httptest.Server, *inventoryBMC, *atomic.Int32, func(http.HandlerFunc)) {
	t.Helper()
	b := &inventoryBMC{docs: map[string]any{
		"/redfish/v1/":        map[string]any{"Systems": map[string]string{"@odata.id": "/redfish/v1/Systems"}},
		"/redfish/v1/Systems": map[string]any{"Members": []any{map[string]string{"@odata.id": "/redfish/v1/Systems/1"}}},
		"/redfish/v1/Systems/1": map[string]any{"@odata.id": "/redfish/v1/Systems/1", "Id": "1", "UUID": strings.ToUpper(powerUUID), "PowerState": "On",
			"LastResetTime": "2026-01-02T03:04:05Z",
			"Actions": map[string]any{"#ComputerSystem.Reset": map[string]any{"target": "/redfish/v1/Systems/1/Actions/ComputerSystem.Reset",
				"@Redfish.ActionInfo": "/redfish/v1/Systems/1/ResetActionInfo"}}},
		"/redfish/v1/Systems/1/ResetActionInfo": map[string]any{"Parameters": []any{map[string]any{"Name": "ResetType", "Required": "true", "DataType": "String",
			"AllowableValues": []string{"On", "ForceOff", "ForceRestart"}}}},
	}}
	if mutate != nil {
		mutate(b.docs)
	}
	var resets atomic.Int32
	var onReset atomic.Pointer[http.HandlerFunc]
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/redfish/v1/Systems/1/Actions/ComputerSystem.Reset" {
			resets.Add(1)
			if h := onReset.Load(); h != nil {
				(*h)(w, r)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		b.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	return server, b, &resets, func(h http.HandlerFunc) { onReset.Store(&h) }
}

func pinnedClient(t *testing.T, server *httptest.Server, timeout time.Duration) *Client {
	t.Helper()
	c, err := NewPinnedClient(server.URL, "operator", "synthetic-secret", PublicKeySHA256(server.Certificate()), timeout)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestFindPowerSystemByUUID(t *testing.T) {
	server, bmc, resets, _ := powerBMC(t, nil)
	c := pinnedClient(t, server, 5*time.Second)
	sys, err := c.FindPowerSystem(context.Background(), powerUUID)
	if err != nil {
		t.Fatal(err)
	}
	if sys.ID != "1" || sys.ResetTarget != "/redfish/v1/Systems/1/Actions/ComputerSystem.Reset" || !slices.Equal(sys.ResetTypes, []string{"On", "ForceOff", "ForceRestart"}) {
		t.Fatalf("system = %+v", sys)
	}
	p, err := c.ObservePower(context.Background(), sys)
	if err != nil || p.State != "On" || !p.LastResetTime.Equal(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Fatalf("power = %+v %v", p, err)
	}
	bmc.mu.Lock()
	defer bmc.mu.Unlock()
	for _, r := range bmc.requests {
		if !strings.HasPrefix(r, "GET ") {
			t.Fatalf("finding and observing issued %s", r)
		}
	}
	if resets.Load() != 0 {
		t.Fatal("a reset was sent")
	}
}

func TestFindPowerSystemRefusesAnyOtherServer(t *testing.T) {
	for name, mutate := range map[string]func(map[string]any){
		"replaced server": func(d map[string]any) { d["/redfish/v1/Systems/1"].(map[string]any)["UUID"] = otherUUID },
		"duplicate UUID": func(d map[string]any) {
			d["/redfish/v1/Systems"] = map[string]any{"Members": []any{map[string]string{"@odata.id": "/redfish/v1/Systems/1"}, map[string]string{"@odata.id": "/redfish/v1/Systems/2"}}}
			two := map[string]any{}
			for k, v := range d["/redfish/v1/Systems/1"].(map[string]any) {
				two[k] = v
			}
			two["@odata.id"], two["Id"] = "/redfish/v1/Systems/2", "2"
			d["/redfish/v1/Systems/2"] = two
		},
	} {
		t.Run(name, func(t *testing.T) {
			server, _, resets, _ := powerBMC(t, mutate)
			if _, err := pinnedClient(t, server, 5*time.Second).FindPowerSystem(context.Background(), powerUUID); !errors.Is(err, ErrIdentity) {
				t.Fatalf("err = %v", err)
			}
			if resets.Load() != 0 {
				t.Fatal("a reset was sent")
			}
		})
	}
}

func TestFindPowerSystemRefusesAResetTargetOffTheSystem(t *testing.T) {
	server, _, _, _ := powerBMC(t, func(d map[string]any) {
		d["/redfish/v1/Systems/1"].(map[string]any)["Actions"] = map[string]any{"#ComputerSystem.Reset": map[string]any{
			"target": "/redfish/v1/Systems/2/Actions/ComputerSystem.Reset", "ResetType@Redfish.AllowableValues": []string{"On"}}}
	})
	if _, err := pinnedClient(t, server, 5*time.Second).FindPowerSystem(context.Background(), powerUUID); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v", err)
	}
}

func TestObservePowerRefusesAChangedIdentity(t *testing.T) {
	server, bmc, _, _ := powerBMC(t, nil)
	c := pinnedClient(t, server, 5*time.Second)
	sys, err := c.FindPowerSystem(context.Background(), powerUUID)
	if err != nil {
		t.Fatal(err)
	}
	bmc.mu.Lock()
	bmc.docs["/redfish/v1/Systems/1"].(map[string]any)["UUID"] = otherUUID
	bmc.mu.Unlock()
	if _, err := c.ObservePower(context.Background(), sys); !errors.Is(err, ErrIdentity) {
		t.Fatalf("err = %v", err)
	}
}

// A reset whose response is lost is sent exactly once; the caller observes.
func TestResetPowerIsSentOnceWhenTheResponseIsLost(t *testing.T) {
	server, _, resets, onReset := powerBMC(t, nil)
	c := pinnedClient(t, server, 300*time.Millisecond)
	sys, err := c.FindPowerSystem(context.Background(), powerUUID)
	if err != nil {
		t.Fatal(err)
	}
	onReset(func(w http.ResponseWriter, r *http.Request) { time.Sleep(time.Second) })
	err = c.ResetPower(context.Background(), sys, "ForceRestart")
	if err == nil || errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v", err)
	}
	if n := resets.Load(); n != 1 {
		t.Fatalf("%d resets reached the BMC", n)
	}
	if err := c.ResetPower(context.Background(), sys, "PowerCycle"); !errors.Is(err, ErrUnsupported) || resets.Load() != 1 {
		t.Fatalf("an unoffered reset was sent: %v", err)
	}
}

// Discovery reads offered reset types inline or from the ActionInfo, with
// GETs only.
func TestInventoryRecordsResetTypes(t *testing.T) {
	server, bmc, resets, _ := powerBMC(t, func(d map[string]any) {
		d["/redfish/v1/Systems"] = map[string]any{"Members": []any{map[string]string{"@odata.id": "/redfish/v1/Systems/1"}, map[string]string{"@odata.id": "/redfish/v1/Systems/2"}}}
		d["/redfish/v1/Systems/2"] = map[string]any{"Id": "2", "UUID": otherUUID, "Actions": map[string]any{"#ComputerSystem.Reset": map[string]any{
			"target": "/redfish/v1/Systems/2/Actions/ComputerSystem.Reset", "ResetType@Redfish.AllowableValues": []string{"On", "PowerCycle"}}}}
	})
	inv, err := pinnedClient(t, server, 5*time.Second).Inventory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Systems) != 2 || !slices.Equal(inv.Systems[0].ResetTypes, []string{"On", "ForceOff", "ForceRestart"}) || !slices.Equal(inv.Systems[1].ResetTypes, []string{"On", "PowerCycle"}) {
		t.Fatalf("systems = %+v", inv.Systems)
	}
	bmc.mu.Lock()
	defer bmc.mu.Unlock()
	for _, r := range bmc.requests {
		if !strings.HasPrefix(r, "GET ") {
			t.Fatalf("inventory issued %s", r)
		}
	}
	if resets.Load() != 0 {
		t.Fatal("inventory sent a reset")
	}
}
