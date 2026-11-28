package redfish

import (
	"context"
	"errors"
	"strings"

	"github.com/stmcginnis/gofish"
	"github.com/stmcginnis/gofish/schemas"
)

// maxComponents bounds each hardware collection a commissioning reads.
const maxComponents = 64

// Hardware is what a System reports about its identity, components and
// health. It is read with GETs only.
type Hardware struct {
	SerialNumber string
	UUID         string
	// Healthy is false when the System or any present component reports
	// Warning or Critical health. Redfish allows a component to omit its
	// health, and an omitted health counts as healthy.
	Healthy    bool
	Unhealthy  []string
	Processors []Processor
	MemoryMiB  int64
	Disks      []Disk
	Interfaces []NetworkInterface
}

type Processor struct {
	ID           string
	Model        string
	Architecture string
	Cores        int64
}

type Disk struct {
	Name          string
	CapacityBytes int64
}

// Firmware is one FirmwareInventory member.
type Firmware struct {
	ID      string
	Name    string
	Version string
}

type status struct {
	State  string
	Health string
}

// skip reports whether a component is not present to judge. ok reports
// whether a present component reports neither Warning nor Critical.
func (s status) skip() bool { return s.State == "Absent" || s.State == "Disabled" }
func (s status) ok() bool   { return s.Health != "Warning" && s.Health != "Critical" }

// Hardware reads the verified System and its processors, memory, storage and
// network interfaces. Every member must sit under the System.
func (c *Client) Hardware(ctx context.Context, sys System) (Hardware, error) {
	api, err := c.connect(ctx)
	if err != nil {
		return Hardware{}, err
	}
	var doc struct {
		SerialNumber       string
		UUID               string
		Status             status
		Processors         link
		Memory             link
		Storage            link
		SimpleStorage      link
		EthernetInterfaces link
	}
	if err := getJSON(api, sys.Path, &doc); err != nil {
		return Hardware{}, err
	}
	hw := Hardware{SerialNumber: strings.TrimSpace(doc.SerialNumber), UUID: canonicalUUID(doc.UUID), Healthy: true}
	// Two Storage controllers may link the same drive; it counts once.
	seen := map[string]bool{}
	check := func(name string, s status) {
		if !s.skip() && !s.ok() {
			hw.Healthy = false
			if len(hw.Unhealthy) < maxComponents {
				hw.Unhealthy = append(hw.Unhealthy, name)
			}
		}
	}
	check("System", doc.Status)
	under := func(collection string) (string, error) {
		if collection == "" {
			return "", nil
		}
		p, err := cleanPath(collection)
		if err != nil || !strings.HasPrefix(p, sys.Path+"/") {
			return "", errors.New("redfish: hardware collection outside the System")
		}
		return p, nil
	}
	each := func(collection string, read func(path string) error) error {
		p, err := under(collection)
		if err != nil || p == "" {
			return err
		}
		paths, err := members(api, p, maxComponents)
		if err != nil {
			return err
		}
		for _, member := range paths {
			if err := read(member); err != nil {
				return err
			}
		}
		return nil
	}
	if err := each(doc.Processors.ID, func(p string) error {
		var cpu struct {
			ID                    string `json:"Id"`
			Model                 string
			ProcessorArchitecture string
			InstructionSet        string
			TotalCores            int64
			Status                status
		}
		if err := getJSON(api, p, &cpu); err != nil {
			return err
		}
		if cpu.Status.State == "Absent" {
			return nil
		}
		check("Processor "+cpu.ID, cpu.Status)
		hw.Processors = append(hw.Processors, Processor{ID: cpu.ID, Model: cpu.Model, Architecture: architecture(cpu.ProcessorArchitecture, cpu.InstructionSet), Cores: cpu.TotalCores})
		return nil
	}); err != nil {
		return Hardware{}, err
	}
	if err := each(doc.Memory.ID, func(p string) error {
		var dimm struct {
			ID          string `json:"Id"`
			CapacityMiB int64
			Status      status
		}
		if err := getJSON(api, p, &dimm); err != nil {
			return err
		}
		// An Absent or Disabled module is not memory the server can use.
		if dimm.Status.skip() {
			return nil
		}
		check("Memory "+dimm.ID, dimm.Status)
		hw.MemoryMiB += dimm.CapacityMiB
		return nil
	}); err != nil {
		return Hardware{}, err
	}
	if err := each(doc.Storage.ID, func(p string) error {
		var storage struct {
			Drives []link
		}
		if err := getJSON(api, p, &storage); err != nil {
			return err
		}
		for _, d := range storage.Drives {
			// Drives sit under the System or, on some BMCs, under a Chassis.
			dp, err := cleanPath(d.ID)
			if err != nil || !(strings.HasPrefix(dp, sys.Path+"/") || strings.HasPrefix(dp, "/redfish/v1/Chassis/")) {
				return errors.New("redfish: drive outside the System and its Chassis")
			}
			if seen[dp] {
				continue
			}
			seen[dp] = true
			var drive struct {
				Name          string
				CapacityBytes int64
				Status        status
			}
			if err := getJSON(api, dp, &drive); err != nil {
				return err
			}
			if drive.Status.State == "Absent" || drive.CapacityBytes <= 0 {
				continue
			}
			check("Disk "+drive.Name, drive.Status)
			if len(hw.Disks) < maxComponents {
				hw.Disks = append(hw.Disks, Disk{Name: drive.Name, CapacityBytes: drive.CapacityBytes})
			}
		}
		return nil
	}); err != nil {
		return Hardware{}, err
	}
	// A BMC may list the same drives under both Storage and SimpleStorage.
	// Storage is the current schema, so SimpleStorage is read only when
	// Storage yields no disk: some BMCs link a Storage collection that lists
	// none of the drives SimpleStorage reports.
	if len(hw.Disks) == 0 {
		if err := each(doc.SimpleStorage.ID, func(p string) error {
			var controller struct {
				ID      string `json:"Id"`
				Devices []struct {
					Name          string
					CapacityBytes int64
					Status        status
				}
			}
			if err := getJSON(api, p, &controller); err != nil {
				return err
			}
			for _, device := range controller.Devices {
				if device.Status.State == "Absent" || device.CapacityBytes <= 0 {
					continue
				}
				check("Disk "+device.Name, device.Status)
				if len(hw.Disks) < maxComponents {
					hw.Disks = append(hw.Disks, Disk{Name: device.Name, CapacityBytes: device.CapacityBytes})
				}
			}
			return nil
		}); err != nil {
			return Hardware{}, err
		}
	}
	p, err := under(doc.EthernetInterfaces.ID)
	if err != nil {
		return Hardware{}, err
	}
	nics, err := interfaces(api, sys.Path, p)
	if err != nil {
		return Hardware{}, err
	}
	hw.Interfaces = nics
	return hw, nil
}

// architecture maps Redfish processor fields to a Kubernetes architecture.
func architecture(processor, instructionSet string) string {
	switch {
	case instructionSet == "x86-64" || (processor == "x86" && instructionSet == ""):
		return "amd64"
	case instructionSet == "ARM-A64":
		return "arm64"
	case instructionSet != "":
		return instructionSet
	}
	return processor
}

// Firmware reads the UpdateService's FirmwareInventory. A service without
// one reports no components.
func (c *Client) Firmware(ctx context.Context) ([]Firmware, error) {
	api, err := c.connect(ctx)
	if err != nil {
		return nil, err
	}
	return firmware(api)
}

func firmware(api *gofish.APIClient) ([]Firmware, error) {
	var root struct{ UpdateService link }
	if err := getJSON(api, schemas.DefaultServiceRoot, &root); err != nil {
		return nil, err
	}
	if root.UpdateService.ID == "" {
		return nil, nil
	}
	var service struct{ FirmwareInventory link }
	if err := getJSON(api, root.UpdateService.ID, &service); err != nil {
		return nil, err
	}
	if service.FirmwareInventory.ID == "" {
		return nil, nil
	}
	paths, err := members(api, service.FirmwareInventory.ID, maxComponents)
	if err != nil {
		return nil, err
	}
	var out []Firmware
	for _, p := range paths {
		var doc struct {
			ID      string `json:"Id"`
			Name    string
			Version string
		}
		if err := getJSON(api, p, &doc); err != nil {
			return nil, err
		}
		out = append(out, Firmware{ID: doc.ID, Name: doc.Name, Version: doc.Version})
	}
	return out, nil
}
