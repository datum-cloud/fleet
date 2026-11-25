package redfish

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func withStorage(b *bmcStub) {
	a := "/redfish/v1/Systems/A"
	b.docs[a].(map[string]any)["Storage"] = map[string]string{"@odata.id": a + "/Storage"}
	b.docs[a+"/Storage"] = map[string]any{"Members": []any{map[string]string{"@odata.id": a + "/Storage/1"}, map[string]string{"@odata.id": a + "/Storage/2"}}}
	b.docs[a+"/Storage/1"] = map[string]any{"Drives": []any{map[string]string{"@odata.id": a + "/Storage/1/Drives/0"}, map[string]string{"@odata.id": a + "/Storage/1/Drives/1"}},
		"Volumes": map[string]string{"@odata.id": a + "/Storage/1/Volumes"}}
	// A second Storage links drive 0 again; it is read once.
	b.docs[a+"/Storage/2"] = map[string]any{"Drives": []any{map[string]string{"@odata.id": a + "/Storage/1/Drives/0"}}}
	b.docs[a+"/Storage/1/Drives/0"] = map[string]any{"Id": "0", "SerialNumber": " EXAMPLE-SN-0001 ", "Model": "EXAMPLE-NVME-3840", "Revision": "EX1.3",
		"Protocol": "NVMe", "MediaType": "SSD", "CapacityBytes": 3840755982336, "EncryptionStatus": "Unencrypted"}
	b.docs[a+"/Storage/1/Drives/1"] = map[string]any{"Id": "1", "SerialNumber": "EXAMPLE-SN-0002", "Protocol": "SATA", "MediaType": "HDD", "CapacityBytes": 8001563222016}
	b.docs[a+"/Storage/1/Volumes"] = map[string]any{"Members": []any{map[string]string{"@odata.id": a + "/Storage/1/Volumes/1"}, map[string]string{"@odata.id": a + "/Storage/1/Volumes/2"}}}
	b.docs[a+"/Storage/1/Volumes/1"] = map[string]any{"RAIDType": "RAID1", "Links": map[string]any{"Drives": []any{map[string]string{"@odata.id": a + "/Storage/1/Drives/1"}}}}
	b.docs[a+"/Storage/1/Volumes/2"] = map[string]any{"RAIDType": "None", "Links": map[string]any{"Drives": []any{map[string]string{"@odata.id": a + "/Storage/1/Drives/0"}}}}
}

func TestInventoryReadsDrivesAndRAIDMembership(t *testing.T) {
	b := newStub()
	withStorage(b)
	inv, err := stubClient(t, b, 5*time.Second).Inventory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	a, other := inv.Systems[0], inv.Systems[1]
	if !a.StorageReported || len(a.Drives) != 2 {
		t.Fatalf("system A drives = %+v (reported %v)", a.Drives, a.StorageReported)
	}
	want := Drive{ID: "Storage/1/Drives/0", SerialNumber: "EXAMPLE-SN-0001", Model: "EXAMPLE-NVME-3840", Revision: "EX1.3", Protocol: "NVMe", MediaType: "SSD", CapacityBytes: 3840755982336, EncryptionStatus: "Unencrypted"}
	if a.Drives[0] != want {
		t.Fatalf("drive 0 = %+v", a.Drives[0])
	}
	if a.Drives[0].InVolume || !a.Drives[1].InVolume {
		t.Fatalf("only the RAID1 member is in a volume: %+v", a.Drives)
	}
	// A System that links no Storage is unknown, not empty.
	if other.StorageReported || other.Drives != nil {
		t.Fatalf("system B = %+v", other)
	}
}

func TestInventoryRefusesForeignOrPartialStorage(t *testing.T) {
	a := "/redfish/v1/Systems/A"
	for name, mutate := range map[string]func(b *bmcStub){
		"storage outside the system": func(b *bmcStub) {
			b.docs[a].(map[string]any)["Storage"] = map[string]string{"@odata.id": "/redfish/v1/Systems/B/Storage"}
		},
		"drive outside the system": func(b *bmcStub) {
			b.docs[a+"/Storage/1"].(map[string]any)["Drives"] = []any{map[string]string{"@odata.id": "/redfish/v1/Systems/B/Storage/1/Drives/0"}}
		},
		"unreadable drive":  func(b *bmcStub) { delete(b.docs, a+"/Storage/1/Drives/1") },
		"unreadable volume": func(b *bmcStub) { delete(b.docs, a+"/Storage/1/Volumes/1") },
	} {
		t.Run(name, func(t *testing.T) {
			b := newStub()
			withStorage(b)
			mutate(b)
			if _, err := stubClient(t, b, 5*time.Second).Inventory(context.Background()); err == nil {
				t.Fatal("a foreign or partial Storage read must fail the whole inventory")
			}
		})
	}
}

func TestInventoryMarksVolumesAcrossStorageAndKeysDrivesByPath(t *testing.T) {
	b := newStub()
	withStorage(b)
	a := "/redfish/v1/Systems/A"
	// Storage/2 has its own drive "0". A RAID volume under Storage/1 links it.
	b.docs[a+"/Storage/2"] = map[string]any{"Drives": []any{map[string]string{"@odata.id": a + "/Storage/2/Drives/0"}}}
	b.docs[a+"/Storage/2/Drives/0"] = map[string]any{"Id": "0", "SerialNumber": "EXAMPLE-SN-0003", "CapacityBytes": 1}
	b.docs[a+"/Storage/1/Volumes/2"] = map[string]any{"RAIDType": "RAID0", "Links": map[string]any{"Drives": []any{map[string]string{"@odata.id": a + "/Storage/2/Drives/0"}}}}
	inv, err := stubClient(t, b, 5*time.Second).Inventory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	drives := inv.Systems[0].Drives
	if len(drives) != 3 || drives[2].ID != "Storage/2/Drives/0" || drives[0].ID == drives[2].ID {
		t.Fatalf("drive IDs must be unique paths: %+v", drives)
	}
	if !drives[2].InVolume {
		t.Fatal("a volume under one Storage that links a drive under a later Storage must mark it")
	}
}

func TestInventoryFailsRatherThanTruncatingStorage(t *testing.T) {
	a := "/redfish/v1/Systems/A"
	for name, mutate := range map[string]func(b *bmcStub){
		"more drives than the bound": func(b *bmcStub) {
			var drives []any
			for i := 0; i <= MaxInventoryDrives; i++ {
				p := fmt.Sprintf("%s/Storage/1/Drives/%d", a, i)
				drives = append(drives, map[string]string{"@odata.id": p})
				b.docs[p] = map[string]any{"SerialNumber": fmt.Sprintf("SN-%d", i)}
			}
			b.docs[a+"/Storage/1"].(map[string]any)["Drives"] = drives
		},
		"more volumes than the bound": func(b *bmcStub) {
			var volumes []any
			for i := 0; i <= MaxInventoryDrives; i++ {
				p := fmt.Sprintf("%s/Storage/1/Volumes/v%d", a, i)
				volumes = append(volumes, map[string]string{"@odata.id": p})
				b.docs[p] = map[string]any{"RAIDType": "None"}
			}
			b.docs[a+"/Storage/1/Volumes"] = map[string]any{"Members": volumes}
		},
		"more storage than the bound": func(b *bmcStub) {
			var storages []any
			for i := 0; i <= maxInventoryStorage; i++ {
				p := fmt.Sprintf("%s/Storage/s%d", a, i)
				storages = append(storages, map[string]string{"@odata.id": p})
				b.docs[p] = map[string]any{}
			}
			b.docs[a+"/Storage"] = map[string]any{"Members": storages}
		},
		"a page past the page bound": func(b *bmcStub) {
			b.docs[a+"/Storage"] = map[string]any{"Members": []any{map[string]string{"@odata.id": a + "/Storage/1"}}, "Members@odata.nextLink": a + "/Storage?page=1"}
			for i := 1; i <= maxCollectionPages; i++ {
				b.docs[fmt.Sprintf("%s/Storage?page=%d", a, i)] = map[string]any{"Members": []any{}, "Members@odata.nextLink": fmt.Sprintf("%s/Storage?page=%d", a, i+1)}
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			b := newStub()
			withStorage(b)
			mutate(b)
			if _, err := stubClient(t, b, 5*time.Second).Inventory(context.Background()); err == nil {
				t.Fatal("storage beyond a bound must fail the inventory, not truncate it")
			}
		})
	}
}
