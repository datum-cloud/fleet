package bmcdiscovery

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"go.datum.net/fleet/internal/redfish"
)

func TestObservedStorageKeepsUnknownApartFromEmpty(t *testing.T) {
	many := make([]redfish.Drive, 32)
	for i := range many {
		many[i] = redfish.Drive{ID: "d", SerialNumber: strings.Repeat("s", 300), Revision: strings.Repeat("r", 100), CapacityBytes: -1, InVolume: i == 0}
	}
	inv := redfish.Inventory{Systems: []redfish.InventorySystem{
		{ID: "unknown"},
		{ID: "empty", StorageReported: true},
		{ID: "full", StorageReported: true, Drives: many},
		{ID: "blank-id", StorageReported: true, Drives: []redfish.Drive{{ID: "Storage/1/Drives/0"}, {ID: "   "}}},
		{ID: "too-many", StorageReported: true, Drives: make([]redfish.Drive, 33)},
	}}
	out := observedInventory(inv, metav1.Now(), "https://bmc.example")
	if out.Systems[0].Storage != nil {
		t.Fatal("a System that links no Storage must stay unknown")
	}
	if out.Systems[1].Storage == nil || len(out.Systems[1].Storage.Drives) != 0 {
		t.Fatal("a reported Storage with no drives is a verified empty list")
	}
	drives := out.Systems[2].Storage.Drives
	if out.Systems[3].Storage != nil || out.Systems[4].Storage != nil {
		t.Fatal("a drive that cannot be recorded, or too many drives, leaves storage unknown rather than looking complete")
	}
	if len(drives) != 32 || len(drives[0].SerialNumber) != 253 || len(drives[0].Firmware) != 63 || drives[0].CapacityBytes != 0 || !drives[0].InVolume {
		t.Fatalf("drives are bounded and clipped: %d, %+v", len(drives), drives[0])
	}
}
