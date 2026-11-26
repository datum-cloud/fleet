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
)

// Target is one enrolled server: the Device it is, its BMC endpoint and its
// exact Redfish System. Console names the serial-console capture file for the
// target (a lab seam; see the operations guide). The boot NIC comes from
// Device inventory through the admission snapshot, and the boot address from
// IPAM through the operation's inputs. Enrollment is site configuration and
// is never inferred.
type Target struct {
	// Device is "<namespace>/<name>" in the provider project.
	Device     string `json:"device"`
	Endpoint   string `json:"endpoint"`
	SystemID   string `json:"systemID"`
	SystemUUID string `json:"systemUUID"`
	DiskBytes  int64  `json:"diskBytes"`
	Console    string `json:"console"`
}

// DHCP modes. Static: the site's DHCP is configured independently of Fleet,
// and an operation with a boot address is refused. Synced: Fleet publishes
// each operation's boot reservation, an operation without a boot address is
// refused, and a network boot requires evidence that DHCP applied the
// reservation and later withdrew it.
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

func ParseEnrollment(b []byte) (Enrollment, error) {
	var e Enrollment
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.DisallowUnknownFields()
	if err := d.Decode(&e); err != nil || len(e.Targets) == 0 || (e.DHCP != DHCPStatic && e.DHCP != DHCPSynced) {
		return Enrollment{}, errors.New("invalid Redfish enrollment")
	}
	seen := map[string]bool{}
	for i, t := range e.Targets {
		endpoint, err := ParseEndpoint(t.Endpoint)
		// A Redfish System ID is unique only within its BMC.
		system := "s" + endpoint + " " + t.SystemID
		if err != nil || !validDevice(t.Device) || t.SystemID == "" || len(t.SystemUUID) != 36 || t.DiskBytes <= 0 || t.Console == "" || filepath.Base(t.Console) != t.Console ||
			seen["a"+t.Device] || seen[system] || seen["u"+strings.ToLower(t.SystemUUID)] {
			return Enrollment{}, errors.New("invalid or duplicate Redfish enrollment target")
		}
		seen["a"+t.Device], seen[system], seen["u"+strings.ToLower(t.SystemUUID)] = true, true, true
		e.Targets[i].Endpoint = endpoint
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
