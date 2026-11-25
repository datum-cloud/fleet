package redfish

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"

	"github.com/stmcginnis/gofish"
	"github.com/stmcginnis/gofish/schemas"
)

// MaxInventoryDrives bounds a System's drives and its RAID volumes. Unlike the
// other bounds, storage is never truncated: a System that reports more fails
// the whole inventory, so a partial drive list can never look complete.
const MaxInventoryDrives = 32

// Inventory bounds. A BMC that reports more is read only up to these limits.
const (
	maxInventoryMembers    = 16
	maxInventoryInterfaces = 32
	maxInventoryStorage    = 8
	maxCollectionPages     = 8
)

// Inventory is a read-only observation of what a BMC reports about itself and
// the Systems it manages.
type Inventory struct {
	Managers []Manager
	Systems  []InventorySystem
}

// Manager is one Redfish Manager: the BMC itself.
type Manager struct {
	ID                string
	Model             string
	FirmwareVersion   string
	NetworkInterfaces []NetworkInterface
}

// InventorySystem is one ComputerSystem's identity and observed power state.
type InventorySystem struct {
	ID           string
	UUID         string
	Manufacturer string
	Model        string
	SerialNumber string
	SKU          string
	PowerState   string
	// NetworkInterfaces are the System's EthernetInterfaces.
	NetworkInterfaces []NetworkInterface
	// Drives are the System's Storage drives. StorageReported is false when
	// the System links no Storage collection.
	Drives          []Drive
	StorageReported bool
}

// Drive is one Redfish Drive under a System's Storage.
type Drive struct {
	ID               string
	SerialNumber     string
	Model            string
	Revision         string
	Protocol         string
	MediaType        string
	CapacityBytes    int64
	EncryptionStatus string
	// InVolume means a Volume with a RAID type links the drive.
	InVolume bool
}

// NetworkInterface is one Redfish EthernetInterface.
type NetworkInterface struct {
	ID         string
	MACAddress string
	LinkStatus string
	SpeedMbps  int64
}

// Inventory reads the service root, every Manager and every ComputerSystem
// with their EthernetInterfaces. It issues GETs only and never follows an
// action, so it cannot change power, boot or any setting. Documents are
// decoded leniently: only the named properties are read, and a vendor
// property with an off-schema type elsewhere in a document is ignored. Any
// failed read fails the whole observation; nothing partial is returned.
func (c *Client) Inventory(ctx context.Context) (Inventory, error) {
	api, err := c.connect(ctx)
	if err != nil {
		return Inventory{}, err
	}
	var root struct {
		Systems, Managers link
	}
	if err := getJSON(api, schemas.DefaultServiceRoot, &root); err != nil {
		return Inventory{}, err
	}
	var inv Inventory
	if root.Managers.ID != "" {
		paths, err := members(api, root.Managers.ID, maxInventoryMembers)
		if err != nil {
			return Inventory{}, err
		}
		for _, p := range paths {
			var doc struct {
				ID                 string `json:"Id"`
				Model              string
				FirmwareVersion    string
				EthernetInterfaces link
			}
			if err := getJSON(api, p, &doc); err != nil {
				return Inventory{}, err
			}
			nics, err := interfaces(api, p, doc.EthernetInterfaces.ID)
			if err != nil {
				return Inventory{}, err
			}
			inv.Managers = append(inv.Managers, Manager{ID: doc.ID, Model: doc.Model, FirmwareVersion: doc.FirmwareVersion, NetworkInterfaces: nics})
		}
	}
	if root.Systems.ID != "" {
		paths, err := members(api, root.Systems.ID, maxInventoryMembers)
		if err != nil {
			return Inventory{}, err
		}
		for _, p := range paths {
			var doc struct {
				ID                 string `json:"Id"`
				UUID               string
				Manufacturer       string
				Model              string
				SerialNumber       string
				SKU                string
				PowerState         string
				EthernetInterfaces link
				Storage            link
			}
			if err := getJSON(api, p, &doc); err != nil {
				return Inventory{}, err
			}
			nics, err := interfaces(api, p, doc.EthernetInterfaces.ID)
			if err != nil {
				return Inventory{}, err
			}
			drives, err := storageDrives(api, p, doc.Storage.ID)
			if err != nil {
				return Inventory{}, err
			}
			inv.Systems = append(inv.Systems, InventorySystem{ID: doc.ID, UUID: canonicalUUID(doc.UUID), Manufacturer: doc.Manufacturer,
				Model: doc.Model, SerialNumber: doc.SerialNumber, SKU: doc.SKU, PowerState: doc.PowerState, NetworkInterfaces: nics,
				Drives: drives, StorageReported: doc.Storage.ID != ""})
		}
	}
	return inv, nil
}

type link struct {
	ID string `json:"@odata.id"`
}

func getJSON(api *gofish.APIClient, path string, out any) error {
	if _, err := cleanPath(path); err != nil {
		return err
	}
	resp, err := api.Get(path)
	if err == nil {
		err = json.NewDecoder(resp.Body).Decode(out)
	}
	schemas.DeferredCleanupHTTPResponse(resp)
	return classify(err)
}

// members returns up to limit member paths of a collection, following
// Members@odata.nextLink pages. Every member must sit under the collection.
func members(api *gofish.APIClient, collection string, limit int) ([]string, error) {
	out, _, err := pagedMembers(api, collection, limit)
	return out, err
}

// pagedMembers is members that also reports whether the collection holds
// members it did not read: past limit, or on a page past maxCollectionPages.
func pagedMembers(api *gofish.APIClient, collection string, limit int) ([]string, bool, error) {
	base, err := cleanPath(collection)
	if err != nil {
		return nil, false, err
	}
	more := false
	var out []string
	next := base
	for page := 0; next != "" && page < maxCollectionPages && len(out) < limit; page++ {
		var doc struct {
			Members  []link
			NextLink string `json:"Members@odata.nextLink"`
		}
		path, query, _ := strings.Cut(next, "?")
		if _, err := cleanPath(path); err != nil || (path != base && !strings.HasPrefix(path, base+"/")) {
			return nil, false, errors.New("redfish: collection page outside the collection")
		}
		if query != "" {
			path += "?" + query
		}
		resp, err := api.Get(path)
		if err == nil {
			err = json.NewDecoder(resp.Body).Decode(&doc)
		}
		schemas.DeferredCleanupHTTPResponse(resp)
		if err != nil {
			return nil, false, classify(err)
		}
		for _, m := range doc.Members {
			p, err := cleanPath(m.ID)
			if err != nil || !strings.HasPrefix(p, base+"/") {
				return nil, false, errors.New("redfish: collection member outside the collection")
			}
			if len(out) < limit {
				out = append(out, p)
			} else {
				more = true
			}
		}
		next = doc.NextLink
	}
	return out, more || next != "", nil
}

// interfaces reads an EthernetInterfaces collection that must belong to owner.
func interfaces(api *gofish.APIClient, owner, collection string) ([]NetworkInterface, error) {
	if collection == "" {
		return nil, nil
	}
	if p, err := cleanPath(collection); err != nil || !strings.HasPrefix(p, owner+"/") {
		return nil, errors.New("redfish: EthernetInterfaces outside its owner")
	}
	paths, err := members(api, collection, maxInventoryInterfaces)
	if err != nil {
		return nil, err
	}
	var out []NetworkInterface
	for _, p := range paths {
		var doc struct {
			ID                   string `json:"Id"`
			MACAddress           string
			PermanentMACAddress  string
			LinkStatus           string
			SpeedMbps            *json.Number
			InterfaceEnabledFlag *bool `json:"InterfaceEnabled"`
		}
		if err := getJSON(api, p, &doc); err != nil {
			return nil, err
		}
		mac := canonicalMAC(doc.PermanentMACAddress)
		if mac == "" {
			mac = canonicalMAC(doc.MACAddress)
		}
		nic := NetworkInterface{ID: doc.ID, MACAddress: mac, LinkStatus: doc.LinkStatus}
		if doc.SpeedMbps != nil {
			if v, err := doc.SpeedMbps.Int64(); err == nil && v >= 0 {
				nic.SpeedMbps = v
			}
		}
		out = append(out, nic)
	}
	return out, nil
}

// storageDrives reads every Drive of every Storage in a collection that must
// belong to owner, then marks drives that a RAID Volume links. Volumes are read
// after every drive, because a Volume can link a drive under another Storage.
// A drive linked from two Storage resources is read once. Its ID is its path
// under the System, which is unique; a document Id is not. Any storage beyond
// the bounds fails the read rather than being dropped.
func storageDrives(api *gofish.APIClient, owner, collection string) ([]Drive, error) {
	if collection == "" {
		return nil, nil
	}
	if p, err := cleanPath(collection); err != nil || !strings.HasPrefix(p, owner+"/") {
		return nil, errors.New("redfish: Storage outside its owner")
	}
	storages, err := exactMembers(api, collection, maxInventoryStorage)
	if err != nil {
		return nil, err
	}
	out := []Drive{}
	index := map[string]int{}
	var volumeCollections []string
	for _, storage := range storages {
		var doc struct {
			Drives  []link
			Volumes link
		}
		if err := getJSON(api, storage, &doc); err != nil {
			return nil, err
		}
		for _, d := range doc.Drives {
			p, err := cleanPath(d.ID)
			if err != nil || !strings.HasPrefix(p, owner+"/") {
				return nil, errors.New("redfish: Drive outside its System")
			}
			if _, seen := index[p]; seen {
				continue
			}
			if len(out) == MaxInventoryDrives {
				return nil, errors.New("redfish: the System reports more drives than Fleet reads")
			}
			var drive struct {
				SerialNumber     string
				Model            string
				Revision         string
				Protocol         string
				MediaType        string
				CapacityBytes    *json.Number
				EncryptionStatus string
			}
			if err := getJSON(api, p, &drive); err != nil {
				return nil, err
			}
			found := Drive{ID: strings.TrimPrefix(p, owner+"/"), SerialNumber: strings.TrimSpace(drive.SerialNumber), Model: strings.TrimSpace(drive.Model), Revision: strings.TrimSpace(drive.Revision),
				Protocol: drive.Protocol, MediaType: drive.MediaType, EncryptionStatus: drive.EncryptionStatus}
			if drive.CapacityBytes != nil {
				if v, err := drive.CapacityBytes.Int64(); err == nil && v > 0 {
					found.CapacityBytes = v
				}
			}
			index[p] = len(out)
			out = append(out, found)
		}
		if doc.Volumes.ID != "" {
			if p, err := cleanPath(doc.Volumes.ID); err != nil || !strings.HasPrefix(p, storage+"/") {
				return nil, errors.New("redfish: Volumes outside its Storage")
			}
			volumeCollections = append(volumeCollections, doc.Volumes.ID)
		}
	}
	for _, collection := range volumeCollections {
		volumes, err := exactMembers(api, collection, MaxInventoryDrives)
		if err != nil {
			return nil, err
		}
		for _, v := range volumes {
			var volume struct {
				RAIDType string
				Links    struct{ Drives []link }
			}
			if err := getJSON(api, v, &volume); err != nil {
				return nil, err
			}
			if volume.RAIDType == "" || volume.RAIDType == "None" {
				continue
			}
			for _, d := range volume.Links.Drives {
				p, err := cleanPath(d.ID)
				if err != nil {
					return nil, errors.New("redfish: a Volume links an invalid Drive")
				}
				if i, ok := index[p]; ok {
					out[i].InVolume = true
				}
			}
		}
	}
	return out, nil
}

// exactMembers is members that fails, instead of truncating, when the
// collection holds members it did not read.
func exactMembers(api *gofish.APIClient, collection string, limit int) ([]string, error) {
	paths, more, err := pagedMembers(api, collection, limit)
	if err != nil {
		return nil, err
	}
	if more {
		return nil, errors.New("redfish: a storage collection holds more members than Fleet reads")
	}
	return paths, nil
}

// canonicalMAC returns a 48-bit MAC in lowercase colon form, or "" when the
// value is not one (including the all-zero placeholder some BMCs report).
func canonicalMAC(s string) string {
	hw, err := net.ParseMAC(strings.TrimSpace(s))
	if err != nil || len(hw) != 6 || hw.String() == "00:00:00:00:00:00" {
		return ""
	}
	return hw.String()
}

// canonicalUUID returns the SMBIOS UUID in lowercase canonical form, or "".
func canonicalUUID(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) != 36 {
		return ""
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return ""
			}
		default:
			if !strings.ContainsRune("0123456789abcdef", r) {
				return ""
			}
		}
	}
	return s
}
