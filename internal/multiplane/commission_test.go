package multiplane

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestLabFirmwareIsWhatTheLabServes keeps the Redfish run's plan baseline
// equal to the firmware inventory the lab adapter serves, so the
// commissioning's firmware check passes against the emulator.
func TestLabFirmwareIsWhatTheLabServes(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "hack", "redfish-lab", "firmware.json"))
	if err != nil {
		t.Fatal(err)
	}
	var served []struct{ Id, Version string }
	if err := json.Unmarshal(data, &served); err != nil {
		t.Fatal(err)
	}
	versions := map[string]string{}
	for _, item := range served {
		versions[item.Id] = item.Version
	}
	if len(LabFirmware) == 0 {
		t.Fatal("the Redfish run's plan has no firmware baseline")
	}
	for _, baseline := range LabFirmware {
		if versions[baseline.Component] != baseline.Version {
			t.Errorf("the plan requires %s %s, the lab serves %q", baseline.Component, baseline.Version, versions[baseline.Component])
		}
	}
}
