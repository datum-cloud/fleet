package bootnet

import (
	"encoding/json"
	"testing"
	"time"
)

func TestFeedIsCanonicalAndSelfVerifying(t *testing.T) {
	exp := time.Date(2030, 1, 2, 3, 4, 5, 999, time.FixedZone("x", 3600))
	a := Reservation{MAC: "52-54-00-77-01-0B", Address: "192.0.2.11", Expires: exp}
	b := Reservation{MAC: "52:54:00:77:01:0a", Address: "192.0.2.10", Expires: exp}
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
	if !f1.Has("52:54:00:77:01:0a", "192.0.2.10") || f1.Has("52:54:00:77:01:0a", "192.0.2.11") || !f1.Serves("52:54:00:77:01:0b", "") || !f1.Serves("", "192.0.2.11") || f1.Serves("52:54:00:77:01:0c", "192.0.2.12") {
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
		"duplicate MAC":     {b, {MAC: b.MAC, Address: "192.0.2.12", Expires: exp}},
		"duplicate address": {b, {MAC: "52:54:00:77:01:0c", Address: "192.0.2.10", Expires: exp}},
		"no expiry":         {{MAC: b.MAC, Address: b.Address}},
		"prefix form":       {{MAC: b.MAC, Address: "192.0.2.10/24", Expires: exp}},
		"unspecified":       {{MAC: b.MAC, Address: "0.0.0.0", Expires: exp}},
		"mapped":            {{MAC: b.MAC, Address: "::ffff:192.0.2.10", Expires: exp}},
		"long MAC":          {{MAC: "02:00:5e:10:00:00:00:01", Address: b.Address, Expires: exp}},
	} {
		if _, err := NewFeed(rs); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := NewFeed([]Reservation{{MAC: b.MAC, Address: "2001:db8::10", Expires: exp}}); err != nil {
		t.Fatalf("IPv6 address: %v", err)
	}
	if (Applied{Instance: "kea-0", Feed: empty}).Verify() != nil || (Applied{Instance: "-kea", Feed: empty}).Verify() == nil {
		t.Fatal("instance names not checked")
	}
}

// TestIssuedIsOutsideTheRevision: the same reservations issued at different
// times are one revision, both verify, and a feed retained before feeds
// carried an issue time still verifies with the zero time. Issued must be
// UTC to the second.
func TestIssuedIsOutsideTheRevision(t *testing.T) {
	r := Reservation{MAC: "52:54:00:77:01:0a", Address: "192.0.2.10", Expires: time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)}
	a, _ := NewFeed([]Reservation{r})
	b := a
	a.Issued = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	b.Issued = a.Issued.Add(time.Hour)
	if a.Revision != b.Revision || a.Verify() != nil || b.Verify() != nil {
		t.Fatalf("issue time changed the revision or failed verification: %+v %+v", a, b)
	}
	encoded, _ := json.Marshal(a)
	var decoded Feed
	if json.Unmarshal(encoded, &decoded) != nil || decoded.Verify() != nil || !decoded.Issued.Equal(a.Issued) {
		t.Fatalf("issue time did not round-trip: %s", encoded)
	}
	again, _ := json.Marshal(decoded)
	if string(again) != string(encoded) {
		t.Fatalf("encoding is not deterministic: %s != %s", again, encoded)
	}
	var old Feed
	if err := json.Unmarshal([]byte(`{"revision":"`+a.Revision+`","reservations":[{"mac":"52:54:00:77:01:0a","address":"192.0.2.10","expires":"2030-01-02T03:04:05Z"}]}`), &old); err != nil || old.Verify() != nil || !old.Issued.IsZero() {
		t.Fatalf("a feed without an issue time: %v, %v", err, old.Verify())
	}
	for name, issued := range map[string]time.Time{
		"sub-second": a.Issued.Add(time.Millisecond),
		"not UTC":    a.Issued.In(time.FixedZone("x", 3600)),
	} {
		f := a
		f.Issued = issued
		if f.Verify() == nil {
			t.Errorf("%s issue time verified", name)
		}
	}
}
