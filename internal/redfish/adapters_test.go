package redfish

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

const (
	chassisPath = "/redfish/v1/Chassis/1"
	adapterPath = chassisPath + "/NetworkAdapters/NIC.Slot.1"
)

func ref(path string) map[string]string { return map[string]string{"@odata.id": path} }

func collection(paths ...string) map[string]any {
	refs := []any{}
	for _, p := range paths {
		refs = append(refs, ref(p))
	}
	return map[string]any{"Members": refs}
}

// withChassis adds a Chassis collection holding one Chassis, and docs, to the
// synthetic service.
func withChassis(b *inventoryBMC, docs map[string]any) {
	b.docs["/redfish/v1/"].(map[string]any)["Chassis"] = ref("/redfish/v1/Chassis")
	b.docs["/redfish/v1/Chassis"] = collection(chassisPath)
	b.docs[chassisPath] = map[string]any{"Id": "1", "NetworkAdapters": ref(chassisPath + "/NetworkAdapters")}
	for k, v := range docs {
		b.docs[k] = v
	}
}

func adapter(links map[string]any) map[string]any {
	doc := map[string]any{"Id": "NIC.Slot.1", "Manufacturer": "Example NIC Vendor", "Model": "Example 2x25G", "SerialNumber": "SYNTHETIC-NIC-0001",
		"PartNumber": "EX-0001", "Controllers": []any{map[string]any{"FirmwarePackageVersion": "1.2.3"}}}
	for k, v := range links {
		doc[k] = v
	}
	return doc
}

// port is a Port document. lldp, when non-nil, is its Ethernet.LLDPReceive.
func port(id, mac string, lldp map[string]any) map[string]any {
	ethernet := map[string]any{"AssociatedMACAddresses": []string{mac}}
	if lldp != nil {
		ethernet["LLDPReceive"] = lldp
	}
	return map[string]any{"Id": id, "PortId": id, "LinkStatus": "LinkUp", "CurrentSpeedGbps": 25, "Ethernet": ethernet,
		"Location": map[string]any{"PartLocation": map[string]any{"ServiceLabel": "NIC Slot 1 Port " + id}}}
}

func neighbor(switchPort string) map[string]any {
	return map[string]any{"ChassisId": "02:00:00:00:ff:01", "ChassisIdSubtype": "MacAddr", "PortId": switchPort, "PortIdSubtype": "IfName",
		"SystemName": "example-switch-01", "ManagementAddressIPv4": "192.0.2.250", "ManagementAddressIPv6": "2001:DB8::FA"}
}

func portsAdapter() map[string]any {
	ports := adapterPath + "/Ports"
	return map[string]any{
		chassisPath + "/NetworkAdapters": collection(adapterPath),
		adapterPath:                      adapter(map[string]any{"Ports": ref(ports)}),
		ports:                            collection(ports+"/1", ports+"/2"),
	}
}

func merge(maps ...map[string]any) map[string]any {
	out := map[string]any{}
	for _, m := range maps {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

func syntheticAdapter(ports ...AdapterPort) []Chassis {
	return []Chassis{{ID: "1", NetworkAdapters: []NetworkAdapter{{ID: "NIC.Slot.1", Manufacturer: "Example NIC Vendor", Model: "Example 2x25G",
		SerialNumber: "SYNTHETIC-NIC-0001", PartNumber: "EX-0001", FirmwareVersion: "1.2.3", Ports: ports}}}}
}

func syntheticPort(id, mac string, lldp *LLDPNeighbor) AdapterPort {
	return AdapterPort{ID: id, PhysicalPortNumber: id, MACAddresses: []string{mac}, LinkStatus: "LinkUp", SpeedMbps: 25000,
		ServiceLabel: "NIC Slot 1 Port " + id, LLDP: lldp}
}

func syntheticNeighbor(switchPort string) *LLDPNeighbor {
	return &LLDPNeighbor{ChassisID: "02:00:00:00:ff:01", ChassisIDSubtype: "MacAddr", PortID: switchPort, PortIDSubtype: "IfName",
		SystemName: "example-switch-01", ManagementAddressIPv4: "192.0.2.250", ManagementAddressIPv6: "2001:db8::fa"}
}

func TestInventoryReadsNetworkAdapterPorts(t *testing.T) {
	ports := adapterPath + "/Ports"
	networkPorts := adapterPath + "/NetworkPorts"
	for name, tc := range map[string]struct {
		docs    map[string]any
		want    []Chassis
		wantErr bool
		// adapterErr is the AdapterErr wanted; the rest of the inventory
		// is still read.
		adapterErr error
	}{
		"ports with LLDP": {
			docs: merge(portsAdapter(), map[string]any{
				ports + "/1": port("1", "02-00-00-00-01-01", neighbor("Ethernet1/1")),
				ports + "/2": port("2", "02:00:00:00:01:02", neighbor("Ethernet1/2")),
			}),
			want: syntheticAdapter(syntheticPort("1", "02:00:00:00:01:01", syntheticNeighbor("Ethernet1/1")),
				syntheticPort("2", "02:00:00:00:01:02", syntheticNeighbor("Ethernet1/2"))),
		},
		"ports without LLDP": {
			docs: merge(portsAdapter(), map[string]any{
				ports + "/1": port("1", "02:00:00:00:01:01", nil),
				// An empty neighbor, as some services report, is no neighbor.
				ports + "/2": port("2", "02:00:00:00:01:02", map[string]any{"ChassisIdSubtype": "NotTransmitted", "ManagementAddressIPv4": "0.0.0.0"}),
			}),
			want: syntheticAdapter(syntheticPort("1", "02:00:00:00:01:01", nil), syntheticPort("2", "02:00:00:00:01:02", nil)),
		},
		"network ports only": {
			docs: map[string]any{
				chassisPath + "/NetworkAdapters": collection(adapterPath),
				adapterPath:                      adapter(map[string]any{"NetworkPorts": ref(networkPorts)}),
				networkPorts:                     collection(networkPorts + "/1"),
				networkPorts + "/1": map[string]any{"Id": "1", "PhysicalPortNumber": "1", "LinkStatus": "Up", "CurrentLinkSpeedMbps": 10000,
					// A Fibre Channel WWN is not a MAC and is dropped.
					"AssociatedNetworkAddresses": []string{"02:00:00:00:01:01", "20:00:00:00:00:00:00:01", "02:00:00:00:01:01"}},
			},
			want: syntheticAdapter(AdapterPort{ID: "1", PhysicalPortNumber: "1", MACAddresses: []string{"02:00:00:00:01:01"}, LinkStatus: "Up", SpeedMbps: 10000}),
		},
		"ports collection 404 falls back to network ports": {
			docs: map[string]any{
				chassisPath + "/NetworkAdapters": collection(adapterPath),
				adapterPath:                      adapter(map[string]any{"Ports": ref(ports), "NetworkPorts": ref(networkPorts)}),
				networkPorts:                     collection(networkPorts + "/1"),
				networkPorts + "/1":              map[string]any{"Id": "1", "PhysicalPortNumber": "1", "AssociatedNetworkAddresses": []string{"02:00:00:00:01:01"}},
			},
			want: syntheticAdapter(AdapterPort{ID: "1", PhysicalPortNumber: "1", MACAddresses: []string{"02:00:00:00:01:01"}}),
		},
		"no network adapters collection": {
			docs: map[string]any{chassisPath: map[string]any{"Id": "1"}},
		},
		"network adapters collection 404": {
			docs: map[string]any{},
		},
		"adapter without ports": {
			docs: map[string]any{chassisPath + "/NetworkAdapters": collection(adapterPath), adapterPath: adapter(nil)},
			want: syntheticAdapter(),
		},
		"one port 404": {
			docs: merge(portsAdapter(), map[string]any{ports + "/2": port("2", "02:00:00:00:01:02", neighbor("Ethernet1/2"))}),
			want: syntheticAdapter(syntheticPort("2", "02:00:00:00:01:02", syntheticNeighbor("Ethernet1/2"))),
		},
		"off-schema property": {
			docs: merge(portsAdapter(), map[string]any{
				ports + "/1": merge(port("1", "02:00:00:00:01:01", nil), map[string]any{"CurrentSpeedGbps": "fast"}),
				ports + "/2": port("2", "02:00:00:00:01:02", nil),
			}),
			want: syntheticAdapter(AdapterPort{ID: "1", PhysicalPortNumber: "1", MACAddresses: []string{"02:00:00:00:01:01"}, LinkStatus: "LinkUp",
				ServiceLabel: "NIC Slot 1 Port 1"}, syntheticPort("2", "02:00:00:00:01:02", nil)),
		},
		"empty ports collection falls back to network ports": {
			docs: map[string]any{
				chassisPath + "/NetworkAdapters": collection(adapterPath),
				adapterPath:                      adapter(map[string]any{"Ports": ref(ports), "NetworkPorts": ref(networkPorts)}),
				ports:                            collection(),
				networkPorts:                     collection(networkPorts + "/1"),
				networkPorts + "/1":              map[string]any{"Id": "1", "PhysicalPortNumber": "1", "AssociatedNetworkAddresses": []string{"02:00:00:00:01:01"}},
			},
			want: syntheticAdapter(AdapterPort{ID: "1", PhysicalPortNumber: "1", MACAddresses: []string{"02:00:00:00:01:01"}}),
		},
		"empty ports collection without network ports": {
			docs: map[string]any{
				chassisPath + "/NetworkAdapters": collection(adapterPath),
				adapterPath:                      adapter(map[string]any{"Ports": ref(ports)}),
				ports:                            collection(),
			},
			want: syntheticAdapter(),
		},
		"port server error fails only the adapter read": {
			docs:       merge(portsAdapter(), map[string]any{ports + "/1": http.StatusInternalServerError, ports + "/2": port("2", "02:00:00:00:01:02", nil)}),
			adapterErr: Status{http.StatusInternalServerError},
		},
		"adapters collection server error fails only the adapter read": {
			docs:       map[string]any{chassisPath + "/NetworkAdapters": http.StatusServiceUnavailable},
			adapterErr: Status{http.StatusServiceUnavailable},
		},
		"chassis server error fails only the adapter read": {
			docs:       map[string]any{chassisPath: http.StatusInternalServerError},
			adapterErr: Status{http.StatusInternalServerError},
		},
		"port member outside its collection": {
			docs:    merge(portsAdapter(), map[string]any{ports: collection(ports+"/1", chassisPath+"/Elsewhere/1"), ports + "/1": port("1", "02:00:00:00:01:01", nil)}),
			wantErr: true,
		},
		"ports outside the adapter": {
			docs: map[string]any{
				chassisPath + "/NetworkAdapters": collection(adapterPath),
				adapterPath:                      adapter(map[string]any{"Ports": ref("/redfish/v1/Chassis/2/NetworkAdapters/NIC.Slot.1/Ports")}),
			},
			wantErr: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			bmc, server, pin := tlsBMC(t)
			withChassis(bmc, tc.docs)
			c, err := NewPinnedClient(server.URL, "operator", "synthetic-secret", pin, 5*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			inv, err := c.Inventory(context.Background())
			if tc.wantErr {
				if !errors.Is(err, ErrIdentity) {
					t.Fatalf("err = %v, chassis %+v; want ErrIdentity", err, inv.Chassis)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if inv.AdapterErr != tc.adapterErr {
				t.Fatalf("adapter error = %v, want %v", inv.AdapterErr, tc.adapterErr)
			}
			if !reflect.DeepEqual(inv.Chassis, tc.want) {
				t.Fatalf("chassis = %+v\nwant %+v", inv.Chassis, tc.want)
			}
			// Managers and Systems are read as before.
			if len(inv.Managers) != 1 || len(inv.Systems) != 1 {
				t.Fatalf("managers %d, systems %d", len(inv.Managers), len(inv.Systems))
			}
			requests, _ := bmc.seen()
			for _, r := range requests {
				if !strings.HasPrefix(r, "GET ") {
					t.Fatalf("inventory issued %s; discovery is GET only", r)
				}
			}
		})
	}
}

func TestInventoryWithoutChassisIssuesNoAdapterRequest(t *testing.T) {
	bmc, server, pin := tlsBMC(t)
	c, _ := NewPinnedClient(server.URL, "operator", "synthetic-secret", pin, 5*time.Second)
	inv, err := c.Inventory(context.Background())
	if err != nil || inv.Chassis != nil {
		t.Fatalf("chassis = %+v, %v", inv.Chassis, err)
	}
	requests, _ := bmc.seen()
	for _, r := range requests {
		if strings.Contains(r, "/Chassis") {
			t.Fatalf("requested %s without a Chassis link", r)
		}
	}
}

// TestInventoryBoundsAdapterRequests reports more adapters and ports than the
// limits and checks that the reads stop at them.
func TestInventoryBoundsAdapterRequests(t *testing.T) {
	bmc, server, pin := tlsBMC(t)
	const chassisCount, adaptersPerChassis, portsPerAdapter = 3, 12, 40
	docs := map[string]any{}
	var chassis []string
	for c := range chassisCount {
		cp := fmt.Sprintf("/redfish/v1/Chassis/%d", c)
		chassis = append(chassis, cp)
		docs[cp] = map[string]any{"Id": fmt.Sprint(c), "NetworkAdapters": ref(cp + "/NetworkAdapters")}
		var adapters []string
		for a := range adaptersPerChassis {
			ap := fmt.Sprintf("%s/NetworkAdapters/%d", cp, a)
			adapters = append(adapters, ap)
			docs[ap] = map[string]any{"Id": fmt.Sprint(a), "Ports": ref(ap + "/Ports")}
			var ports []string
			for p := range portsPerAdapter {
				pp := fmt.Sprintf("%s/Ports/%d", ap, p)
				ports = append(ports, pp)
				docs[pp] = port(fmt.Sprint(p), fmt.Sprintf("02:00:00:%02x:%02x:%02x", c, a, p), nil)
			}
			docs[ap+"/Ports"] = collection(ports...)
		}
		docs[cp+"/NetworkAdapters"] = collection(adapters...)
	}
	withChassis(bmc, docs)
	bmc.docs["/redfish/v1/Chassis"] = collection(chassis...)
	c, _ := NewPinnedClient(server.URL, "operator", "synthetic-secret", pin, 5*time.Second)
	inv, err := c.Inventory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var adapters, ports int
	for _, ch := range inv.Chassis {
		adapters += len(ch.NetworkAdapters)
		for _, a := range ch.NetworkAdapters {
			if len(a.Ports) > maxAdapterPorts {
				t.Fatalf("adapter %s has %d ports", a.ID, len(a.Ports))
			}
			ports += len(a.Ports)
		}
	}
	if adapters != maxNetworkAdapters || ports != maxAdapterPortsTotal {
		t.Fatalf("read %d adapters and %d ports, want %d and %d", adapters, ports, maxNetworkAdapters, maxAdapterPortsTotal)
	}
	requests, _ := bmc.seen()
	var adapterReads, portReads int
	for _, r := range requests {
		switch {
		case strings.Contains(r, "/Ports/"):
			portReads++
		case strings.HasSuffix(r, "/Ports"), strings.HasSuffix(r, "/NetworkAdapters"):
		case strings.Contains(r, "/NetworkAdapters/"):
			adapterReads++
		}
	}
	if adapterReads != maxNetworkAdapters || portReads != maxAdapterPortsTotal {
		t.Fatalf("issued %d adapter and %d port reads, want %d and %d", adapterReads, portReads, maxNetworkAdapters, maxAdapterPortsTotal)
	}
}

func TestInventoryAdapterReadStopsOnRejection(t *testing.T) {
	bmc, server, pin := tlsBMC(t)
	withChassis(bmc, merge(portsAdapter(), map[string]any{adapterPath + "/Ports/1": http.StatusUnauthorized}))
	c, _ := NewPinnedClient(server.URL, "operator", "synthetic-secret", pin, 5*time.Second)
	if _, err := c.Inventory(context.Background()); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
}

func TestInventoryInterruptedDuringAdapterReadIsUnfinished(t *testing.T) {
	bmc, server, pin := tlsBMC(t)
	withChassis(bmc, portsAdapter())
	ctx, cancel := context.WithCancel(context.Background())
	// The Ports collection is read after every Manager and System, so the
	// cancellation lands in the adapter read.
	bmc.onGet = func(path string) {
		if path == adapterPath+"/Ports" {
			cancel()
		}
	}
	c, _ := NewPinnedClient(server.URL, "operator", "synthetic-secret", pin, 5*time.Second)
	if inv, err := c.Inventory(ctx); err == nil {
		t.Fatalf("interrupted read succeeded: adapter error %v", inv.AdapterErr)
	}
}
