package cleaning

import (
	"errors"
	"slices"
	"testing"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
)

func nvme(serial, firmware string) inventory.ObservedDrive {
	return inventory.ObservedDrive{ID: serial, SerialNumber: serial, Model: "EXAMPLE-NVME-3840", Firmware: firmware, Protocol: "NVMe", MediaType: "SSD", CapacityBytes: 3840755982336}
}

func storage(drives ...inventory.ObservedDrive) *inventory.ObservedStorage {
	return &inventory.ObservedStorage{Drives: drives}
}

func TestManifestNamesCoveredDrivesInOrder(t *testing.T) {
	manifest, err := Manifest(purgePolicy(), storage(nvme("SN-B", "EX1.3"), nvme("SN-A", "EX1.2")))
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest) != 2 || manifest[0].Name != "disk-0" || manifest[0].SerialNumber != "SN-A" || manifest[1].SerialNumber != "SN-B" || manifest[0].Rule != "nvme-qualified" {
		t.Fatalf("manifest = %+v", manifest)
	}
}

func TestManifestRefusesUncoveredServers(t *testing.T) {
	raid := nvme("SN-R", "EX1.3")
	raid.InVolume = true
	anonymous := nvme("", "EX1.3")
	cases := map[string]*inventory.ObservedStorage{
		"unknown storage":     nil,
		"no drives":           storage(),
		"unqualified drive":   storage(nvme("SN-A", "EX0.9")),
		"RAID member":         storage(raid),
		"no serial number":    storage(anonymous),
		"duplicate serial":    storage(nvme("SN-A", "EX1.3"), nvme("SN-A", "EX1.3")),
		"no rule for the bus": storage(inventory.ObservedDrive{ID: "x", SerialNumber: "SN-X", Protocol: "SAS", MediaType: "HDD", CapacityBytes: 1}),
	}
	for name, s := range cases {
		if _, err := Manifest(purgePolicy(), s); !errors.Is(err, ErrUncovered) {
			t.Errorf("%s: err = %v, want ErrUncovered", name, err)
		}
	}
}

func TestPlan(t *testing.T) {
	policy := purgePolicy()
	manifest, err := Manifest(policy, storage(nvme("SN-A", "EX1.3"), nvme("SN-B", "EX1.3")))
	if err != nil {
		t.Fatal(err)
	}
	states := func(plan []ops.PlannedDrive) []string {
		var out []string
		for _, d := range plan {
			out = append(out, d.Name+"="+d.SerialNumber+":"+string(d.State))
		}
		return out
	}
	cases := []struct {
		name     string
		current  *inventory.ObservedStorage
		disposed []string
		want     []string
	}{
		{"unchanged", storage(nvme("SN-A", "EX1.3"), nvme("SN-B", "EX1.3")), nil, []string{"disk-0=SN-A:", "disk-1=SN-B:"}},
		{"missing", storage(nvme("SN-A", "EX1.3")), nil, []string{"disk-0=SN-A:", "disk-1=SN-B:Missing"}},
		{"disposed", storage(nvme("SN-A", "EX1.3")), []string{"disk-1"}, []string{"disk-0=SN-A:"}},
		{"unqualified firmware flashed during the allocation", storage(nvme("SN-A", "EX9.9"), nvme("SN-B", "EX1.3")), nil, []string{"disk-0=SN-A:Uncovered", "disk-1=SN-B:"}},
		{"added drive", storage(nvme("SN-A", "EX1.3"), nvme("SN-B", "EX1.3"), nvme("SN-C", "EX1.2")), nil, []string{"disk-0=SN-A:", "disk-1=SN-B:", "disk-2=SN-C:"}},
		{"added drive after a disposal keeps names unique", storage(nvme("SN-A", "EX1.3"), nvme("SN-C", "EX1.2")), []string{"disk-1"}, []string{"disk-0=SN-A:", "disk-2=SN-C:"}},
		{"added anonymous drive", storage(nvme("SN-A", "EX1.3"), nvme("SN-B", "EX1.3"), nvme("", "EX1.3")), nil, []string{"disk-0=SN-A:", "disk-1=SN-B:", "disk-2=:Uncovered"}},
		{"a pinned serial reported twice", storage(nvme("SN-A", "EX1.3"), nvme("SN-B", "EX1.3"), withID(nvme("SN-A", "EX1.3"), "clone")), nil, []string{"disk-0=SN-A:Uncovered", "disk-1=SN-B:", "disk-2=SN-A:Uncovered"}},
		{"two new drives share a serial", storage(nvme("SN-A", "EX1.3"), nvme("SN-B", "EX1.3"), withID(nvme("SN-C", "EX1.3"), "c1"), withID(nvme("SN-C", "EX1.3"), "c2")), nil, []string{"disk-0=SN-A:", "disk-1=SN-B:", "disk-2=SN-C:Uncovered", "disk-3=SN-C:Uncovered"}},
		{"a pinned serial on another model", storage(withModel(nvme("SN-A", "EX1.3"), "OTHER-MODEL"), nvme("SN-B", "EX1.3")), nil, []string{"disk-0=SN-A:Uncovered", "disk-1=SN-B:"}},
		{"a pinned serial with another capacity", storage(withCapacity(nvme("SN-A", "EX1.3"), 1<<30), nvme("SN-B", "EX1.3")), nil, []string{"disk-0=SN-A:Uncovered", "disk-1=SN-B:"}},
	}
	for _, tc := range cases {
		plan, err := Plan(policy, manifest, tc.current, tc.disposed)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := states(plan); !slices.Equal(got, tc.want) {
			t.Errorf("%s: plan = %v, want %v", tc.name, got, tc.want)
		}
		if Cleanable(plan) != !slices.ContainsFunc(tc.want, func(s string) bool { return s[len(s)-1] != ':' }) {
			t.Errorf("%s: Cleanable disagrees with the states", tc.name)
		}
	}
	plan, _ := Plan(policy, manifest, storage(nvme("SN-A", "EX1.3"), nvme("SN-B", "EX1.3")), nil)
	if !slices.Equal(plan[0].Methods, []ops.CleaningMethod{ops.MethodCryptographicErase, ops.MethodBlockErase}) || plan[0].Verify.Mode != ops.VerifyCanary || plan[0].TimePerTiB == nil {
		t.Fatalf("a covered step carries its methods, check and time bound: %+v", plan[0])
	}
	if _, err := Plan(policy, manifest, nil, nil); !errors.Is(err, ErrStorageUnknown) {
		t.Fatalf("unknown storage = %v", err)
	}
}

func TestStorageOfFindsTheDevicesSystem(t *testing.T) {
	mine, other := storage(nvme("SN-MINE", "EX1.3")), storage(nvme("SN-OTHER", "EX1.3"))
	device := func(uuid string, systems ...inventory.ObservedSystem) *inventory.Device {
		d := &inventory.Device{}
		d.Spec.Identity.SystemUUID = uuid
		d.Status.Discovery = &inventory.DeviceDiscovery{Inventory: &inventory.ObservedInventory{Systems: systems}}
		return d
	}
	const uuid = "00000000-0000-4000-8000-000000000001"
	cases := []struct {
		name   string
		device *inventory.Device
		want   *inventory.ObservedStorage
	}{
		{"matched by UUID", device(uuid, inventory.ObservedSystem{ID: "2", UUID: "00000000-0000-4000-8000-000000000002", Storage: other}, inventory.ObservedSystem{ID: "1", UUID: uuid, Storage: mine}), mine},
		{"UUID case differs", device(uuid, inventory.ObservedSystem{ID: "1", UUID: "00000000-0000-4000-8000-000000000001", Storage: mine}), mine},
		{"only System, no UUID recorded", device("", inventory.ObservedSystem{ID: "1", Storage: mine}), mine},
		{"no System with the UUID", device(uuid, inventory.ObservedSystem{ID: "2", UUID: "00000000-0000-4000-8000-000000000002", Storage: other}), nil},
		{"two Systems and no UUID", device("", inventory.ObservedSystem{ID: "1", Storage: mine}, inventory.ObservedSystem{ID: "2", Storage: other}), nil},
		{"two Systems claim the UUID", device(uuid, inventory.ObservedSystem{ID: "1", UUID: uuid, Storage: mine}, inventory.ObservedSystem{ID: "2", UUID: uuid, Storage: other}), nil},
		{"no discovery", &inventory.Device{}, nil},
	}
	for _, tc := range cases {
		if got := StorageOf(tc.device); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func withID(d inventory.ObservedDrive, id string) inventory.ObservedDrive { d.ID = id; return d }
func withModel(d inventory.ObservedDrive, model string) inventory.ObservedDrive {
	d.Model = model
	return d
}
func withCapacity(d inventory.ObservedDrive, bytes int64) inventory.ObservedDrive {
	d.CapacityBytes = bytes
	return d
}
