package vmwork

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go.datum.net/fleet/internal/site"
)

// defaultBootOrigin is the provisioning bridge's address, used when the boot
// server shares the hypervisor's network namespace.
const defaultBootOrigin = "http://192.168.77.1:8080"

func (c Config) origin() string {
	if c.BootOrigin == "" {
		return defaultBootOrigin
	}
	return c.BootOrigin
}

// prepareNetwork is the only writer of the provisioning network. DHCP stays
// on a bridge in the runtime namespace; NAT forwarding lets the firmware
// reach a boot server outside it.
func (k Installation) prepareNetwork(ctx context.Context, work *site.Work, boot Boot) error {
	if err := k.stopNetwork(ctx); err != nil {
		return err
	}
	forward := "<forward mode='nat'/>"
	if k.origin() == defaultBootOrigin {
		forward = ""
	}
	xml := fmt.Sprintf(`<network><name>fleet-pxe</name>%s<bridge name='fleetpxe0' stp='off' delay='0'/><dns enable='no'/><ip address='192.168.77.1' netmask='255.255.255.0'><dhcp><range start='192.168.77.2' end='192.168.77.2'/><host mac='%s' ip='192.168.77.2'/><bootp file='%s/%s/%s/boot.ipxe'/></dhcp></ip></network>`, forward, guestMAC, k.origin(), Key(work.Operation.UID), boot.Token)
	path := filepath.Join(k.directory(work.Device, work.Operation.UID), "network-private.xml")
	if err := os.WriteFile(path, []byte(xml), 0o600); err != nil {
		return err
	}
	_, err := k.virsh(ctx, "net-create", path)
	return err
}

func (c Config) stopNetwork(ctx context.Context) error {
	names, err := c.virsh(ctx, "net-list", "--name")
	if err != nil {
		return err
	}
	for _, name := range strings.Fields(names) {
		if name == "fleet-pxe" {
			_, err = c.virsh(ctx, "net-destroy", name)
			return err
		}
	}
	return nil
}

// verifyNetworkBoot requires the boot server's completed deliveries and the
// firmware's DHCP lease.
func (k Installation) verifyNetworkBoot(ctx context.Context, work *site.Work) error {
	data, err := os.ReadFile(filepath.Join(k.BootRoot, Key(work.Operation.UID), "boot-delivery.log"))
	if err != nil {
		return err
	}
	completed := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 4 || fields[1] != "write-complete" || !strings.HasPrefix(fields[3], "bytes=") {
			continue
		}
		if n, err := strconv.Atoi(strings.TrimPrefix(fields[3], "bytes=")); err == nil && n > 0 {
			completed[fields[2]] = true
		}
	}
	for _, name := range []string{"boot.ipxe", "kernel", "installer.gz"} {
		if !completed[name] {
			return fmt.Errorf("missing network delivery: %s", name)
		}
	}
	leases, err := k.virsh(ctx, "net-dhcp-leases", "fleet-pxe")
	if err != nil {
		return err
	}
	if !strings.Contains(leases, guestMAC) || !strings.Contains(leases, "192.168.77.2") {
		return fmt.Errorf("missing firmware DHCP lease")
	}
	return os.WriteFile(filepath.Join(k.directory(work.Device, work.Operation.UID), "dhcp-leases.txt"), []byte(leases), 0o600)
}

// admission returns the open admission under a boot key: open for the
// installer, unexpired, with the token, and at the Device's stored fence, so
// a superseded installation never boots the guest.
func admission(store *site.Store, key, token string, now time.Time) (Boot, bool) {
	records, err := store.Records(bootRecords)
	if err != nil {
		return Boot{}, false
	}
	for _, record := range records {
		var boot Boot
		if record.Name != key || json.Unmarshal(record.Data, &boot) != nil || !boot.Open || boot.Closed || !now.Before(boot.Expires) || len(boot.Token) != 64 || subtle.ConstantTimeCompare([]byte(token), []byte(boot.Token)) != 1 {
			continue
		}
		if fence, _, err := store.Current(record.Device); err == nil && fence == boot.Fence {
			return boot, true
		}
	}
	return Boot{}, false
}

// BootHandler serves an open admission's boot script and pinned artifacts.
// It never serves a caller-chosen path, and it records deliveries without
// URLs, headers or credentials.
func (c Config) BootHandler(store *site.Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		deny := func() { http.Error(w, "boot denied", http.StatusForbidden) }
		parts := strings.Split(request.URL.Path, "/")
		if request.Method != http.MethodGet || request.URL.RawQuery != "" || len(parts) != 4 {
			deny()
			return
		}
		boot, ok := admission(store, parts[1], parts[2], c.now())
		if !ok {
			deny()
			return
		}
		name := parts[3]
		var data []byte
		var artifact *os.File
		var size int64
		switch name {
		case "boot.ipxe":
			base := c.origin() + "/" + parts[1] + "/" + boot.Token
			data = []byte(fmt.Sprintf("#!ipxe\nkernel %s/kernel console=ttyS0 rdinit=/init fleet.receipt=%s\ninitrd %s/installer.gz\nboot\n", base, boot.Receipt, base))
			size = int64(len(data))
		case "kernel", "installer.gz":
			// Hash the pinned artifact, then serve the same open file.
			var err error
			if artifact, err = os.Open(filepath.Join(c.Artifacts, name)); err != nil {
				deny()
				return
			}
			defer artifact.Close()
			manifest, manifestErr := os.ReadFile(filepath.Join(c.Artifacts, "SHA256SUMS"))
			sum := sha256.Sum256(manifest)
			hash := sha256.New()
			size, err = io.Copy(hash, artifact)
			if manifestErr != nil || err != nil || c.Image != "fleet-local-linux@sha256:"+hex.EncodeToString(sum[:]) || !strings.Contains(string(manifest), hex.EncodeToString(hash.Sum(nil))+"  "+name+"\n") {
				deny()
				return
			}
			if _, err = artifact.Seek(0, io.SeekStart); err != nil {
				deny()
				return
			}
		default:
			deny()
			return
		}
		dir := filepath.Join(c.BootRoot, parts[1])
		if err := appendEvidence(dir, fmt.Sprintf("%s admitted %s bytes=%d\n", c.now().Format(time.RFC3339), name, size)); err != nil {
			deny()
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
		var written int64
		var err error
		if artifact != nil {
			written, err = io.Copy(w, artifact)
		} else {
			var n int
			n, err = w.Write(data)
			written = int64(n)
		}
		// A completed write is not proof that the client received it.
		if err == nil && written == size {
			_ = appendEvidence(dir, fmt.Sprintf("%s write-complete %s bytes=%d\n", c.now().Format(time.RFC3339), name, written))
		}
	})
}

func appendEvidence(dir, line string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(dir, "boot-delivery.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = file.WriteString(line)
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return syncDir(dir)
}

// ServeBoot serves the boot handler. The write bound matches the boot
// window: emulated firmware takes minutes to fetch the installer.
func (c Config) ServeBoot(store *site.Store) error {
	server := &http.Server{Addr: ":8080", Handler: c.BootHandler(store), ReadHeaderTimeout: 5 * time.Second, WriteTimeout: c.window(), IdleTimeout: 5 * time.Second}
	return server.ListenAndServe()
}

// CheckClosedBoot probes a live boot server with every installation's
// closed admission, and with unknown names, and requires each to be denied.
// It prints no capability URL.
func (c Config) CheckClosedBoot(store *site.Store, origin string) (string, error) {
	records, err := store.Records(bootRecords)
	if err != nil {
		return "", err
	}
	client := &http.Client{Timeout: 10 * time.Second}
	count := 0
	for _, record := range records {
		var boot Boot
		if err := json.Unmarshal(record.Data, &boot); err != nil {
			return "", err
		}
		if !boot.Closed {
			return "", fmt.Errorf("a boot admission is not closed")
		}
		base := origin + "/" + record.Name + "/" + boot.Token
		for _, url := range []string{base + "/boot.ipxe", base + "/kernel", base + "/installer.gz", base + "/disk.raw", origin + "/kernel", origin + "/unknown/unknown/boot.ipxe"} {
			response, err := client.Get(url)
			if err != nil {
				return "", fmt.Errorf("the boot denial probe is unavailable")
			}
			response.Body.Close()
			if response.StatusCode != http.StatusForbidden {
				return "", fmt.Errorf("a closed or unknown boot was served")
			}
			count++
		}
	}
	if count == 0 {
		return "", fmt.Errorf("no boot admission to probe")
	}
	return fmt.Sprintf("PASS %d live HTTP denials: closed admissions' scripts and artifacts, an unsupported artifact, an unguarded artifact, an unknown identity", count), nil
}
