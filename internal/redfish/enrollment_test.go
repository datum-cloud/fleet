package redfish

import (
	"strings"
	"testing"
)

const (
	uuidA = "7f3d0c2e-1a5b-4c6d-8e9f-0a1b2c3d4e0a"
	uuidB = "7f3d0c2e-1a5b-4c6d-8e9f-0a1b2c3d4e0b"
)

func enrollment(targets string) string {
	return `{"dhcp":"static","targets":[` + targets + `]}`
}

const targetA = `{"device":"fleet-provider/server-example-001","endpoint":"https://192.0.2.10:8443","systemID":"System-1","systemUUID":"` + uuidA + `","diskBytes":2147483648,"console":"target-a.log"}`

// TestEnrollmentNamesEachDeviceOnce accepts an explicit Device to BMC
// mapping and refuses a target without a Device, a duplicate, or another
// incomplete target.
func TestEnrollmentNamesEachDeviceOnce(t *testing.T) {
	parsed, err := ParseEnrollment([]byte(enrollment(targetA)))
	if err != nil {
		t.Fatal(err)
	}
	if target, ok := parsed.Target("fleet-provider/server-example-001"); !ok || target.SystemUUID != uuidA {
		t.Fatalf("target = %+v, %v", target, ok)
	}
	if _, ok := parsed.Target("fleet-provider/server-example-002"); ok {
		t.Fatal("an unenrolled Device has a target")
	}
	for name, body := range map[string]string{
		"no device":        strings.Replace(targetA, `"device":"fleet-provider/server-example-001",`, "", 1),
		"device name only": strings.Replace(targetA, "fleet-provider/server-example-001", "server-example-001", 1),
		"duplicate device": targetA + "," + strings.NewReplacer(uuidA, uuidB, "System-1", "System-2").Replace(targetA),
		"duplicate system": targetA + "," + strings.NewReplacer("server-example-001", "server-example-002").Replace(targetA),
		"enrolled MAC":     strings.Replace(targetA, `"console"`, `"mac":"52:54:00:77:01:0a","console"`, 1),
		"no console":       strings.Replace(targetA, `"target-a.log"`, `""`, 1),
		"no disk":          strings.Replace(targetA, `2147483648`, `0`, 1),
		"unknown field":    strings.Replace(targetA, `"console"`, `"extra":1,"console"`, 1),
	} {
		if _, err := ParseEnrollment([]byte(enrollment(body))); err == nil {
			t.Errorf("%s: enrollment accepted", name)
		}
	}
}

// TestASyncedSiteNamesItsDHCPInstances requires a synced enrollment to name a
// non-empty set of unique, valid DHCP instances, and a static one to name
// none.
func TestASyncedSiteNamesItsDHCPInstances(t *testing.T) {
	with := func(dhcp, instances string) string {
		return `{"dhcp":"` + dhcp + `"` + instances + `,"targets":[` + targetA + `]}`
	}
	parsed, err := ParseEnrollment([]byte(with("synced", `,"dhcpInstances":["fleet-site-dhcp-0","fleet-site-dhcp-1"]`)))
	if err != nil || len(parsed.DHCPInstances) != 2 || parsed.DHCPInstances[1] != "fleet-site-dhcp-1" {
		t.Fatalf("synced enrollment = %+v, %v", parsed, err)
	}
	for name, body := range map[string]string{
		"synced without instances":  with("synced", ""),
		"synced with an empty set":  with("synced", `,"dhcpInstances":[]`),
		"static with instances":     with("static", `,"dhcpInstances":["fleet-site-dhcp-0"]`),
		"duplicate instance":        with("synced", `,"dhcpInstances":["fleet-site-dhcp-0","fleet-site-dhcp-0"]`),
		"invalid instance":          with("synced", `,"dhcpInstances":["Fleet_DHCP"]`),
		"empty instance name":       with("synced", `,"dhcpInstances":[""]`),
		"instance name with prefix": with("synced", `,"dhcpInstances":["-kea"]`),
	} {
		if _, err := ParseEnrollment([]byte(body)); err == nil {
			t.Errorf("%s: enrollment accepted", name)
		}
	}
}
