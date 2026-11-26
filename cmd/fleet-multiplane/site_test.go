package main

import (
	"context"
	"path/filepath"
	"testing"

	"go.datum.net/fleet/internal/site"
	"go.datum.net/fleet/internal/site/powerwork"
	"k8s.io/client-go/rest"
)

// TestARedfishSiteRunsPowerOperations requires the Redfish site to register
// the power worker beside the install and wipe workers.
func TestARedfishSiteRunsPowerOperations(t *testing.T) {
	root := t.TempDir()
	t.Setenv("FLEET_SITE_PROVIDER", "redfish")
	t.Setenv("FLEET_LIBVIRT_URI", "")
	t.Setenv("FLEET_REDFISH_ENROLLMENT", `{"dhcp":"static","targets":[{"device":"fleet-provider/server-example-001","endpoint":"https://192.0.2.10:8443",`+
		`"systemID":"System-1","systemUUID":"7f3d0c2e-1a5b-4c6d-8e9f-0a1b2c3d4e0a","mac":"52:54:00:77:01:0a","diskBytes":2147483648,"console":"target-a.log"}]}`)
	t.Setenv("FLEET_REDFISH_CONSOLE", filepath.Join(root, "console"))
	t.Setenv("FLEET_BOOT_EVIDENCE", filepath.Join(root, "boot"))
	t.Setenv("FLEET_SITE_CREDENTIALS_NAMESPACE", "fleet-site-credentials")
	t.Setenv("FLEET_SITE_PROJECT", "provider-example")
	runner, err := siteRunner(context.Background(), &rest.Config{Host: "https://127.0.0.1:1"}, "cell-example", &site.Store{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := runner.Kinds["DevicePowerOperation"].(powerwork.Power); !ok {
		t.Fatalf("kinds = %v", runner.Kinds)
	}
	for _, kind := range []string{"DeviceInstallation", "DeviceWipe"} {
		if runner.Kinds[kind] == nil {
			t.Fatalf("%s is not registered", kind)
		}
	}
}
