package bootnet

import (
	"testing"
	"time"
)

func TestFeedIsCanonicalAndSelfVerifying(t *testing.T) {
	exp := time.Date(2030, 1, 2, 3, 4, 5, 999, time.FixedZone("x", 3600))
	a := Reservation{MAC: "52-54-00-77-01-0B", Address: "192.0.2.11/24", Gateway: "192.0.2.1", Expires: exp}
	b := Reservation{MAC: "52:54:00:77:01:0a", Address: "192.0.2.10/24", Gateway: "192.0.2.1", Expires: exp}
	f1, err := NewFeed([]Reservation{a, b})
	if err != nil {
		t.Fatal(err)
	}
	f2, _ := NewFeed([]Reservation{b, a})
	if f1.Revision != f2.Revision || f1.Reservations[0].MAC != "52:54:00:77:01:0a" || !f1.Reservations[0].Expires.Equal(exp.Truncate(time.Second)) {
		t.Fatalf("feed not canonical: %+v", f1)
	}
	if err := f1.Verify(); err != nil {
		t.Fatal(err)
	}
	if !f1.Has("52:54:00:77:01:0a", "192.0.2.10/24") || f1.Has("52:54:00:77:01:0a", "192.0.2.11/24") || !f1.Serves("52:54:00:77:01:0b") || f1.Serves("52:54:00:77:01:0c") {
		t.Fatal("membership is not exact")
	}
	empty, _ := NewFeed(nil)
	for name, f := range map[string]Feed{
		"other revision":   {Revision: empty.Revision, Reservations: f1.Reservations},
		"unordered":        {Revision: f1.Revision, Reservations: []Reservation{f1.Reservations[1], f1.Reservations[0]}},
		"null reservation": {Revision: empty.Revision},
	} {
		if f.Verify() == nil {
			t.Errorf("%s: verified", name)
		}
	}
	for name, rs := range map[string][]Reservation{
		"duplicate MAC":     {b, {MAC: b.MAC, Address: "192.0.2.12/24", Gateway: "192.0.2.1", Expires: exp}},
		"duplicate address": {b, {MAC: "52:54:00:77:01:0c", Address: "192.0.2.10/25", Gateway: "192.0.2.1", Expires: exp}},
		"no expiry":         {{MAC: b.MAC, Address: b.Address, Gateway: b.Gateway}},
		"bare address":      {{MAC: b.MAC, Address: "192.0.2.10", Gateway: b.Gateway, Expires: exp}},
		"gateway is host":   {{MAC: b.MAC, Address: b.Address, Gateway: "192.0.2.10", Expires: exp}},
		"host route":        {{MAC: b.MAC, Address: "192.0.2.10/32", Gateway: "192.0.2.1", Expires: exp}},
		"long MAC":          {{MAC: "02:00:5e:10:00:00:00:01", Address: b.Address, Gateway: b.Gateway, Expires: exp}},
	} {
		if _, err := NewFeed(rs); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := NewFeed([]Reservation{{MAC: b.MAC, Address: "2001:db8::10/64", Gateway: "2001:db8::1", Expires: exp}}); err != nil {
		t.Fatalf("IPv6 attachment: %v", err)
	}
	if (Applied{Instance: "kea-0", Feed: empty}).Verify() != nil || (Applied{Instance: "-kea", Feed: empty}).Verify() == nil {
		t.Fatal("instance names not checked")
	}
}
