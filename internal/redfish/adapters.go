package redfish

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"strings"

	"github.com/stmcginnis/gofish"
	"github.com/stmcginnis/gofish/schemas"
)

// Network adapter bounds. Together with maxInventoryMembers Chassis and
// maxCollectionPages per collection they bound the requests one inventory
// issues for adapters: at most maxNetworkAdapters adapter reads and
// maxAdapterPortsTotal port reads, whatever the BMC reports.
const (
	maxNetworkAdapters   = 16
	maxAdapterPorts      = 32
	maxAdapterPortsTotal = 64
	maxPortMACAddresses  = 16
)

// Chassis is one Redfish Chassis that reports network adapters.
type Chassis struct {
	ID              string
	NetworkAdapters []NetworkAdapter
}

// NetworkAdapter is one Chassis NetworkAdapter, the physical card or LOM.
type NetworkAdapter struct {
	ID           string
	Manufacturer string
	Model        string
	SerialNumber string
	PartNumber   string
	// FirmwareVersion is the first controller's FirmwarePackageVersion.
	FirmwareVersion string
	Ports           []AdapterPort
}

// AdapterPort is one physical port of a NetworkAdapter, read from its Ports
// collection or, on services without one, its NetworkPorts collection.
type AdapterPort struct {
	ID string
	// PhysicalPortNumber is the label of the port on the adapter: Port.PortId
	// or NetworkPort.PhysicalPortNumber.
	PhysicalPortNumber string
	// MACAddresses are the valid 48-bit addresses the port reports, in
	// lowercase colon form and reported order, without duplicates.
	MACAddresses []string
	LinkStatus   string
	SpeedMbps    int64
	ServiceLabel string
	// LLDP is the neighbor the port received over LLDP, or nil when the BMC
	// reports none.
	LLDP *LLDPNeighbor
}

// LLDPNeighbor is a port's LLDPReceive: what the link partner, normally a
// switch, announced about itself. IDs are kept as reported; their subtype
// says how to read them.
type LLDPNeighbor struct {
	ChassisID             string
	ChassisIDSubtype      string
	PortID                string
	PortIDSubtype         string
	SystemName            string
	ManagementAddressIPv4 string
	ManagementAddressIPv6 string
}

// absent reports whether err is a 404: the optional resource is not there.
func absent(err error) bool {
	var status Status
	return errors.As(err, &status) && status.Code == http.StatusNotFound
}

// getLenientJSON is getJSON for optional data: a property whose type is off
// schema is left at its zero value instead of failing the read.
func getLenientJSON(api *gofish.APIClient, path string, out any) error {
	if _, err := cleanPath(path); err != nil {
		return err
	}
	resp, err := api.Get(path)
	if err == nil {
		err = json.NewDecoder(resp.Body).Decode(out)
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) {
			err = nil
		}
	}
	schemas.DeferredCleanupHTTPResponse(resp)
	return classify(err)
}

// ownedCollection returns the clean path of a collection link that must sit
// under owner, or "" when there is no link.
func ownedCollection(owner, collection string) (string, error) {
	if collection == "" {
		return "", nil
	}
	p, err := cleanPath(collection)
	if err != nil || !strings.HasPrefix(p, owner+"/") {
		return "", fmt.Errorf("%w: collection outside its owner", ErrIdentity)
	}
	return p, nil
}

// adapterReader reads network adapters within one request budget.
type adapterReader struct {
	api      *gofish.APIClient
	adapters int
	ports    int
}

// chassisAdapters reads the Chassis collection and, for each Chassis, its
// NetworkAdapters and their ports. A missing collection or a 404 on a
// Chassis, adapter or port leaves that part out; any other failure fails the
// adapter read. Chassis without adapters are omitted.
func chassisAdapters(api *gofish.APIClient, collection string) ([]Chassis, error) {
	if collection == "" {
		return nil, nil
	}
	paths, err := members(api, collection, maxInventoryMembers)
	if absent(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r := &adapterReader{api: api, adapters: maxNetworkAdapters, ports: maxAdapterPortsTotal}
	var out []Chassis
	for _, p := range paths {
		if r.adapters == 0 {
			break
		}
		var doc struct {
			ID              string `json:"Id"`
			NetworkAdapters link
		}
		if err := getLenientJSON(api, p, &doc); absent(err) {
			continue
		} else if err != nil {
			return nil, err
		}
		adapters, err := r.networkAdapters(p, doc.NetworkAdapters.ID)
		if err != nil {
			return nil, err
		}
		if len(adapters) > 0 {
			out = append(out, Chassis{ID: doc.ID, NetworkAdapters: adapters})
		}
	}
	return out, nil
}

func (r *adapterReader) networkAdapters(owner, collection string) ([]NetworkAdapter, error) {
	collection, err := ownedCollection(owner, collection)
	if err != nil || collection == "" {
		return nil, err
	}
	paths, err := members(r.api, collection, r.adapters)
	if absent(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []NetworkAdapter
	for _, p := range paths {
		if r.adapters == 0 {
			break
		}
		r.adapters--
		var doc struct {
			ID           string `json:"Id"`
			Manufacturer string
			Model        string
			SerialNumber string
			PartNumber   string
			Controllers  []struct {
				FirmwarePackageVersion string
			}
			Ports        link
			NetworkPorts link
		}
		if err := getLenientJSON(r.api, p, &doc); absent(err) {
			continue
		} else if err != nil {
			return nil, err
		}
		a := NetworkAdapter{ID: doc.ID, Manufacturer: doc.Manufacturer, Model: doc.Model, SerialNumber: doc.SerialNumber, PartNumber: doc.PartNumber}
		for _, c := range doc.Controllers {
			if v := strings.TrimSpace(c.FirmwarePackageVersion); v != "" {
				a.FirmwareVersion = v
				break
			}
		}
		// Ports replaces the deprecated NetworkPorts; read the older
		// collection only when the newer one yields no port.
		ports, err := r.adapterPorts(p, doc.Ports.ID)
		if err == nil && len(ports) == 0 {
			ports, err = r.adapterPorts(p, doc.NetworkPorts.ID)
		}
		if err != nil {
			return nil, err
		}
		a.Ports = ports
		out = append(out, a)
	}
	return out, nil
}

// adapterPorts reads a Ports or NetworkPorts collection. It returns no port
// when the adapter has no such link, the collection is 404 or empty, or every
// member is 404.
func (r *adapterReader) adapterPorts(owner, collection string) (ports []AdapterPort, err error) {
	collection, err = ownedCollection(owner, collection)
	if err != nil || collection == "" {
		return nil, err
	}
	paths, err := members(r.api, collection, min(maxAdapterPorts, r.ports))
	if absent(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	for _, p := range paths {
		if r.ports == 0 {
			break
		}
		r.ports--
		var doc portDocument
		if err := getLenientJSON(r.api, p, &doc); absent(err) {
			continue
		} else if err != nil {
			return nil, err
		}
		ports = append(ports, doc.port())
	}
	return ports, nil
}

// portDocument holds the properties read from a Port and from the older
// NetworkPort; each schema sets only its own.
type portDocument struct {
	ID string `json:"Id"`
	// Port
	PortID string `json:"PortId"`
	// Speeds are raw so that an off-schema value, such as a string, reads
	// as no speed: a json.Number target fails the whole decode instead.
	CurrentSpeedGbps json.RawMessage
	Ethernet         struct {
		AssociatedMACAddresses []string
		LLDPReceive            struct {
			ChassisID             string `json:"ChassisId"`
			ChassisIDSubtype      string `json:"ChassisIdSubtype"`
			PortID                string `json:"PortId"`
			PortIDSubtype         string `json:"PortIdSubtype"`
			SystemName            string
			ManagementAddressIPv4 string
			ManagementAddressIPv6 string
		}
	}
	Location struct {
		PartLocation struct {
			ServiceLabel string
		}
	}
	// NetworkPort
	PhysicalPortNumber         string
	AssociatedNetworkAddresses []string
	CurrentLinkSpeedMbps       json.RawMessage
	// Both
	LinkStatus string
}

func (d portDocument) port() AdapterPort {
	p := AdapterPort{ID: d.ID, PhysicalPortNumber: strings.TrimSpace(d.PortID), LinkStatus: d.LinkStatus, ServiceLabel: strings.TrimSpace(d.Location.PartLocation.ServiceLabel)}
	if p.PhysicalPortNumber == "" {
		p.PhysicalPortNumber = strings.TrimSpace(d.PhysicalPortNumber)
	}
	for _, s := range append(append([]string(nil), d.Ethernet.AssociatedMACAddresses...), d.AssociatedNetworkAddresses...) {
		// AssociatedNetworkAddresses may also carry Fibre Channel WWNs,
		// which are not 48-bit MACs and are dropped here.
		if mac := canonicalMAC(s); mac != "" && !contains(p.MACAddresses, mac) && len(p.MACAddresses) < maxPortMACAddresses {
			p.MACAddresses = append(p.MACAddresses, mac)
		}
	}
	if v, ok := speed(d.CurrentSpeedGbps); ok {
		p.SpeedMbps = int64(math.Round(v * 1000))
	} else if v, ok := speed(d.CurrentLinkSpeedMbps); ok {
		p.SpeedMbps = int64(math.Round(v))
	}
	l := d.Ethernet.LLDPReceive
	n := LLDPNeighbor{ChassisID: strings.TrimSpace(l.ChassisID), ChassisIDSubtype: strings.TrimSpace(l.ChassisIDSubtype), PortID: strings.TrimSpace(l.PortID),
		PortIDSubtype: strings.TrimSpace(l.PortIDSubtype), SystemName: strings.TrimSpace(l.SystemName),
		ManagementAddressIPv4: managementAddress(l.ManagementAddressIPv4, true), ManagementAddressIPv6: managementAddress(l.ManagementAddressIPv6, false)}
	// A subtype or address without any neighbor identity is not a neighbor;
	// some services report empty LLDPReceive objects or "NotTransmitted".
	if n.ChassisID != "" || n.PortID != "" || n.SystemName != "" {
		p.LLDP = &n
	}
	return p
}

// speed decodes a non-negative JSON number below 2^31.
func speed(raw json.RawMessage) (float64, bool) {
	var v float64
	if len(raw) == 0 || json.Unmarshal(raw, &v) != nil || v < 0 || v > math.MaxInt32 {
		return 0, false
	}
	return v, true
}

// managementAddress returns a specified address of the wanted family in
// canonical form, or "".
func managementAddress(s string, v4 bool) string {
	ip := net.ParseIP(strings.TrimSpace(s))
	if ip == nil || ip.IsUnspecified() || (ip.To4() != nil) != v4 {
		return ""
	}
	return ip.String()
}
