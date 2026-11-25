package redfish

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go.datum.net/fleet/internal/bootnet"
)

// Target is one enrolled server: the Device it is, its BMC endpoint, its
// exact Redfish System, its boot NIC and, when the site's DHCP is
// synchronized, the provisioning attachment the network owner assigned to
// it. Console names the serial-console capture file for the target (a lab
// seam; see the operations guide). Enrollment is site configuration and is
// never inferred.
type Target struct {
	// Device is "<namespace>/<name>" in the provider project.
	Device       string       `json:"device"`
	Endpoint     string       `json:"endpoint"`
	SystemID     string       `json:"systemID"`
	SystemUUID   string       `json:"systemUUID"`
	MAC          string       `json:"mac"`
	Provisioning Provisioning `json:"provisioning,omitzero"`
	DiskBytes    int64        `json:"diskBytes"`
	Console      string       `json:"console"`
}

// Provisioning is the asset's address on the provisioning network, in prefix
// form, and that network's gateway. Networking/IPAM allocates it; Fleet only
// publishes it while the asset's network boot is admitted.
type Provisioning struct {
	Address string `json:"address"`
	Gateway string `json:"gateway"`
}

// DHCP modes. Static: the site's DHCP is configured independently of Fleet.
// Synced: Fleet publishes boot reservations and a network boot requires
// evidence that DHCP applied the reservation and later withdrew it.
const (
	DHCPStatic = "static"
	DHCPSynced = "synced"
)

// Enrollment is the site's explicit Device to BMC mapping.
type Enrollment struct {
	DHCP    string   `json:"dhcp"`
	Targets []Target `json:"targets"`
}

// Target returns the enrolled server of a "<namespace>/<name>" Device.
func (e Enrollment) Target(device string) (Target, bool) {
	for _, t := range e.Targets {
		if t.Device == device {
			return t, true
		}
	}
	return Target{}, false
}

func normalizeMAC(v string) (string, bool) {
	return bootnet.NormalizeMAC(strings.ReplaceAll(v, "-", ":"))
}

func ParseEnrollment(b []byte) (Enrollment, error) {
	var e Enrollment
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.DisallowUnknownFields()
	if err := d.Decode(&e); err != nil || len(e.Targets) == 0 || (e.DHCP != DHCPStatic && e.DHCP != DHCPSynced) {
		return Enrollment{}, errors.New("invalid Redfish enrollment")
	}
	seen := map[string]bool{}
	for i, t := range e.Targets {
		mac, ok := normalizeMAC(t.MAC)
		endpoint, err := ParseEndpoint(t.Endpoint)
		// A Redfish System ID is unique only within its BMC.
		system := "s" + endpoint + " " + t.SystemID
		if !ok || err != nil || !validDevice(t.Device) || t.SystemID == "" || len(t.SystemUUID) != 36 || t.DiskBytes <= 0 || t.Console == "" || filepath.Base(t.Console) != t.Console ||
			seen["a"+t.Device] || seen[system] || seen["u"+strings.ToLower(t.SystemUUID)] || seen["m"+mac] {
			return Enrollment{}, errors.New("invalid or duplicate Redfish enrollment target")
		}
		seen["a"+t.Device], seen[system], seen["u"+strings.ToLower(t.SystemUUID)], seen["m"+mac] = true, true, true, true
		e.Targets[i].MAC, e.Targets[i].Endpoint = mac, endpoint
		if e.DHCP == DHCPStatic {
			if t.Provisioning != (Provisioning{}) {
				return Enrollment{}, errors.New("a static-DHCP enrollment carries no provisioning attachment")
			}
			continue
		}
		feed, err := bootnet.NewFeed([]bootnet.Reservation{{MAC: mac, Address: t.Provisioning.Address, Gateway: t.Provisioning.Gateway, Expires: time.Unix(1, 0)}})
		if err != nil {
			return Enrollment{}, errors.New("invalid provisioning attachment for enrolled target " + t.Device)
		}
		r := feed.Reservations[0]
		address := "p" + strings.SplitN(r.Address, "/", 2)[0]
		if seen[address] {
			return Enrollment{}, errors.New("duplicate provisioning address")
		}
		seen[address] = true
		e.Targets[i].Provisioning = Provisioning{Address: r.Address, Gateway: r.Gateway}
	}
	return e, nil
}

// ParseBootOrigin accepts only a literal http://IPv4:port origin.
func ParseBootOrigin(value string) (string, error) {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("a boot origin must be http://IPv4:port")
	}
	ip := net.ParseIP(u.Hostname())
	port, err := strconv.Atoi(u.Port())
	if ip == nil || ip.To4() == nil || err != nil || port < 1 || port > 65535 {
		return "", errors.New("a boot origin must be http://IPv4:port")
	}
	return "http://" + ip.To4().String() + ":" + strconv.Itoa(port), nil
}

// VerifyArtifacts derives the admitted image from the exact two-artifact manifest.
func VerifyArtifacts(dir string) (string, error) {
	b, err := os.ReadFile(dir + "/SHA256SUMS")
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	seen := map[string]bool{}
	for _, line := range lines {
		f := strings.Fields(line)
		if len(lines) != 2 || len(f) != 2 || (f[1] != "kernel" && f[1] != "installer.gz") || seen[f[1]] {
			return "", errors.New("invalid artifact manifest")
		}
		seen[f[1]] = true
		if sum, err := fileSHA256(dir + "/" + f[1]); err != nil || sum != f[0] {
			return "", errors.New("artifact digest mismatch")
		}
	}
	sum := sha256.Sum256(b)
	return "fleet-local-linux@sha256:" + hex.EncodeToString(sum[:]), nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func validDevice(device string) bool {
	namespace, name, ok := strings.Cut(device, "/")
	return ok && namespace != "" && name != "" && !strings.Contains(name, "/")
}
