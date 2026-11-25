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

// Inventory bounds. A BMC that reports more is read only up to these limits.
const (
	maxInventoryMembers    = 16
	maxInventoryInterfaces = 32
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
	// ResetTypes are the ComputerSystem.Reset types the System advertises.
	// Discovery reads them and never invokes the action.
	ResetTypes []string
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
				resetAction
			}
			if err := getJSON(api, p, &doc); err != nil {
				return Inventory{}, err
			}
			nics, err := interfaces(api, p, doc.EthernetInterfaces.ID)
			if err != nil {
				return Inventory{}, err
			}
			system := InventorySystem{ID: doc.ID, UUID: canonicalUUID(doc.UUID), Manufacturer: doc.Manufacturer,
				Model: doc.Model, SerialNumber: doc.SerialNumber, SKU: doc.SKU, PowerState: doc.PowerState, NetworkInterfaces: nics}
			if doc.Actions.Reset.Target != "" {
				// Reset types are optional. A reset target or ActionInfo off
				// the System, or an ActionInfo that cannot be read, records
				// none and keeps the rest of the inventory. Only a rejected
				// credential, pin or identity fails the read.
				if _, types, err := resetTypes(api, p, doc.resetAction); err == nil {
					system.ResetTypes = types
				} else if errors.Is(err, ErrUnauthorized) || errors.Is(err, ErrPinMismatch) || errors.Is(err, ErrIdentity) {
					return Inventory{}, err
				}
			}
			inv.Systems = append(inv.Systems, system)
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
	base, err := cleanPath(collection)
	if err != nil {
		return nil, err
	}
	var out []string
	next := base
	for page := 0; next != "" && page < maxCollectionPages && len(out) < limit; page++ {
		var doc struct {
			Members  []link
			NextLink string `json:"Members@odata.nextLink"`
		}
		path, query, _ := strings.Cut(next, "?")
		if _, err := cleanPath(path); err != nil || (path != base && !strings.HasPrefix(path, base+"/")) {
			return nil, errors.New("redfish: collection page outside the collection")
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
			return nil, classify(err)
		}
		for _, m := range doc.Members {
			p, err := cleanPath(m.ID)
			if err != nil || !strings.HasPrefix(p, base+"/") {
				return nil, errors.New("redfish: collection member outside the collection")
			}
			if len(out) < limit {
				out = append(out, p)
			}
		}
		next = doc.NextLink
	}
	return out, nil
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
