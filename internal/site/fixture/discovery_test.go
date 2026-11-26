package fixture

import "testing"

func TestSimulatedDrivesAreStableAndDistinct(t *testing.T) {
	target := Target{Disks: []string{"disk-0", "disk-1"}}
	a, again, b := Drives("default/server-a", target), Drives("default/server-a", target), Drives("default/server-b", target)
	if len(a.Drives) != 2 || a.Drives[0] != again.Drives[0] || a.Drives[0].SerialNumber == a.Drives[1].SerialNumber || a.Drives[0].SerialNumber == b.Drives[0].SerialNumber {
		t.Fatalf("serials must be stable per Device and disk and distinct otherwise: %+v %+v", a.Drives, b.Drives)
	}
	if d := a.Drives[0]; d.Model != SimulatedModel || d.Protocol != "NVMe" || d.MediaType != "SSD" || d.CapacityBytes != SimulatedDriveBytes {
		t.Fatalf("drive = %+v", d)
	}
	many := Drives("default/server-a", Target{Disks: []string{"disk-0", "disk-1", "disk-2", "disk-3", "disk-4", "disk-5", "disk-6", "disk-7", "disk-8", "disk-9", "disk-10"}})
	for i := 1; i < len(many.Drives); i++ {
		if many.Drives[i-1].SerialNumber >= many.Drives[i].SerialNumber {
			t.Fatalf("serials must sort in enrollment order: %s >= %s", many.Drives[i-1].SerialNumber, many.Drives[i].SerialNumber)
		}
	}
	if empty := Drives("default/server-c", Target{}); empty == nil || len(empty.Drives) != 0 {
		t.Fatal("a target with no disks reports a verified empty list")
	}
}
