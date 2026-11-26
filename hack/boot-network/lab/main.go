// Command lab drives the boot network lab: it serves a site's reservation
// feed from a site store over TLS, and opens and closes boot admissions in
// that store as a Redfish worker would. The lab script runs it beside real Kea
// and the real synchronizer. It is a lab seam, never deployed.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"go.datum.net/fleet/internal/bootnet"
	"go.datum.net/fleet/internal/redfish"
	"go.datum.net/fleet/internal/site"
	"go.datum.net/fleet/internal/site/redfishwork"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "lab:", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 {
		return errors.New("usage: lab serve|open|close [flags]")
	}
	fs := flag.NewFlagSet(os.Args[1], flag.ContinueOnError)
	storeDir := fs.String("store", "", "site store directory")
	bootDir := fs.String("boot", "", "boot evidence directory")
	tokenFile := fs.String("token-file", "", "synchronizer token file (serve)")
	cert := fs.String("cert", "", "TLS certificate (serve)")
	key := fs.String("key", "", "TLS key (serve)")
	device := fs.String("device", "fleet-provider/server-example-001", "Device namespace/name")
	operation := fs.String("operation", "", "operation UID (open, close)")
	fence := fs.Int64("fence", 1, "operation fence (open)")
	mac := fs.String("mac", "", "boot NIC MAC (open)")
	address := fs.String("address", "", "boot address (open)")
	if err := fs.Parse(os.Args[2:]); err != nil {
		return err
	}
	store, err := site.OpenStore(*storeDir, nil)
	if err != nil {
		return err
	}
	switch os.Args[1] {
	case "serve":
		token, err := os.ReadFile(*tokenFile)
		if err != nil {
			return err
		}
		// The same listener the site's dhcp-feed role runs, on :8081, serving
		// the expected DHCP instances of the same synced enrollment.
		enrollment, err := redfish.ParseEnrollment([]byte(os.Getenv("FLEET_REDFISH_ENROLLMENT")))
		if err != nil {
			return err
		}
		if enrollment.DHCP != redfish.DHCPSynced {
			return errors.New("the lab feed needs a synced enrollment")
		}
		return redfishwork.BootServer{Store: store, BootRoot: *bootDir, DHCPInstances: enrollment.DHCPInstances}.ServeDHCP(strings.TrimSpace(string(token)), *cert, *key)
	case "open":
		normalized, ok := bootnet.NormalizeMAC(*mac)
		if !ok || *operation == "" {
			return errors.New("open needs an operation and a MAC")
		}
		if err := store.Raise(*device, *fence); err != nil {
			return err
		}
		now := time.Now().UTC()
		boot := redfishwork.Boot{Operation: *operation, Fence: *fence, Mode: "install", MAC: normalized, Address: *address, Receipt: "lab", Token: strings.Repeat("0", 64), Created: now, Expires: now.Add(20 * time.Minute)}
		return store.PutRecord(*device, *fence, "boot", *operation, boot)
	case "close":
		var boot redfishwork.Boot
		found, err := store.GetRecord(*device, "boot", *operation, &boot)
		if err != nil || !found {
			return fmt.Errorf("no boot admission %s: %v", *operation, err)
		}
		boot.Closed = true
		return store.PutRecord(*device, boot.Fence, "boot", *operation, boot)
	}
	return errors.New("unknown command")
}
