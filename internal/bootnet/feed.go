// Package bootnet is the boot-reservation contract between a site's boot
// service and the DHCP synchronizer. A reservation maps one boot NIC, as
// Device inventory records it, to the provisioning address IPAM assigned to
// one operation, and exists only while that operation's network boot is
// admitted. The DHCP base configuration supplies each subnet's prefix and
// gateway. The synchronizer reports what each DHCP instance applied; those
// reports are the evidence that a reservation was served and later withdrawn.
package bootnet

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"regexp"
	"sort"
	"time"
)

// Reservation is one boot NIC's provisioning address: one host address,
// without a prefix length.
type Reservation struct {
	MAC     string    `json:"mac"`
	Address string    `json:"address"`
	Expires time.Time `json:"expires"`
}

// Feed is the complete desired reservation set. Revision is derived from the
// reservations, so a report of an applied Feed is self-verifying.
type Feed struct {
	Revision     string        `json:"revision"`
	Reservations []Reservation `json:"reservations"`
}

// Applied reports that one DHCP instance now serves exactly Feed.
type Applied struct {
	Instance string `json:"instance"`
	Feed     Feed   `json:"feed"`
}

var instanceName = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,61}[a-z0-9])?$`)

// NormalizeMAC returns the canonical lower-case colon form of a 48-bit MAC.
func NormalizeMAC(v string) (string, bool) {
	hw, err := net.ParseMAC(v)
	if err != nil || len(hw) != 6 {
		return "", false
	}
	return hw.String(), true
}

// HostAddress returns the canonical form of one IP host address, refusing a
// prefix, a zone, and the unspecified address.
func HostAddress(v string) (string, error) {
	address, err := netip.ParseAddr(v)
	if err != nil || address.Zone() != "" || address.IsUnspecified() || address.Is4In6() {
		return "", errors.New("a provisioning address must be one IP host address")
	}
	return address.String(), nil
}

func canonical(r Reservation) (Reservation, error) {
	mac, ok := NormalizeMAC(r.MAC)
	if !ok || r.Expires.IsZero() {
		return Reservation{}, errors.New("invalid reservation")
	}
	address, err := HostAddress(r.Address)
	if err != nil {
		return Reservation{}, err
	}
	return Reservation{MAC: mac, Address: address, Expires: r.Expires.UTC().Truncate(time.Second)}, nil
}

func revision(rs []Reservation) string {
	b, _ := json.Marshal(rs)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// NewFeed canonicalizes, orders and checks reservations, and derives the
// revision. A MAC or an address may appear only once.
func NewFeed(rs []Reservation) (Feed, error) {
	out := make([]Reservation, 0, len(rs))
	for _, r := range rs {
		c, err := canonical(r)
		if err != nil {
			return Feed{}, err
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].MAC < out[j].MAC })
	addresses := map[string]bool{}
	for i, r := range out {
		if (i > 0 && out[i-1].MAC == r.MAC) || addresses[r.Address] {
			return Feed{}, errors.New("duplicate reservation")
		}
		addresses[r.Address] = true
	}
	return Feed{Revision: revision(out), Reservations: out}, nil
}

// Verify accepts only a feed already in canonical form whose revision matches.
func (f Feed) Verify() error {
	want, err := NewFeed(f.Reservations)
	if err != nil {
		return err
	}
	got, _ := json.Marshal(f)
	exact, _ := json.Marshal(want)
	if string(got) != string(exact) {
		return errors.New("feed is not canonical or its revision does not match")
	}
	return nil
}

// Verify checks the instance name and the applied feed.
func (a Applied) Verify() error {
	if !instanceName.MatchString(a.Instance) {
		return errors.New("invalid DHCP instance name")
	}
	return a.Feed.Verify()
}

// Has reports whether the feed serves mac at exactly address.
func (f Feed) Has(mac, address string) bool {
	for _, r := range f.Reservations {
		if r.MAC == mac && r.Address == address {
			return true
		}
	}
	return false
}

// Serves reports whether the feed serves mac at any address, or address to
// any MAC.
func (f Feed) Serves(mac, address string) bool {
	for _, r := range f.Reservations {
		if r.MAC == mac || r.Address == address {
			return true
		}
	}
	return false
}
