package localvm

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// defaultBootOrigin is the provisioning-bridge address used when the boot
// service shares the hypervisor network namespace (single-container mode).
const defaultBootOrigin = "http://192.168.77.1:8080"

func (p *Provider) origin() string {
	if p.bootOrigin == "" {
		return defaultBootOrigin
	}
	return p.bootOrigin
}

// prepareNetwork is the sole local network writer. DHCP stays on a bridge in the
// runtime namespace. NAT forwarding lets firmware reach a boot Service outside
// that namespace; the runtime restricts forwarded traffic to that origin.
func (p *Provider) prepareNetwork(ctx context.Context, r *record) error {
	names, err := p.virsh(ctx, "net-list", "--name")
	if err != nil {
		return err
	}
	for _, n := range strings.Fields(names) {
		if n == "fleet-pxe" {
			if _, err = p.virsh(ctx, "net-destroy", n); err != nil {
				return err
			}
		}
	}
	xml := fmt.Sprintf(`<network><name>fleet-pxe</name>%s<bridge name='fleetpxe0' stp='off' delay='0'/><dns enable='no'/><ip address='192.168.77.1' netmask='255.255.255.0'><dhcp><range start='192.168.77.2' end='192.168.77.2'/><host mac='52:54:00:77:00:02' ip='192.168.77.2'/><bootp file='%s/%s/%s/boot.ipxe'/></dhcp></ip></network>`, p.forward(), p.origin(), key(r.Request.ID)[:16], r.BootToken)
	path := p.stateRoot() + "/network-private.xml"
	if err = os.WriteFile(path, []byte(xml), 0600); err != nil {
		return err
	}
	_, err = p.virsh(ctx, "net-create", path)
	return err
}

// forward is empty when the boot service is on the bridge itself.
func (p *Provider) forward() string {
	if p.origin() == defaultBootOrigin {
		return ""
	}
	return "<forward mode='nat'/>"
}

// BootHandler never uses a file server or caller-selected filesystem path.
// Atomic journal replacement allows reads while the executor holds its flock.
func (p *Provider) BootHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		deny := func() { http.Error(w, "boot denied", http.StatusForbidden) }
		parts := strings.Split(q.URL.Path, "/")
		if q.Method != "GET" || q.URL.RawQuery != "" || len(parts) != 4 {
			deny()
			return
		}
		j, err := p.read()
		if err != nil || p.checkMode(j) != nil || j.BootMode != "pxe" || j.Image != p.image || j.Closed {
			deny()
			return
		}
		var r *record
		for _, candidate := range j.Operations {
			if key(candidate.Request.ID)[:16] == parts[1] {
				r = candidate
				break
			}
		}
		if r == nil || r.Request.Kind != "install" || r.Stage != "installer-started" || r.BootClosed || !time.Now().Before(r.BootExpires) || len(r.BootToken) != 64 || subtle.ConstantTimeCompare([]byte(parts[2]), []byte(r.BootToken)) != 1 || r.Request.BindingID != j.Binding || r.Request.Epoch != j.Epoch || r.Request.FenceToken != j.Fence || p.validate(r.Request) != nil {
			deny()
			return
		}
		name := parts[3]
		var data []byte
		var artifact *os.File
		var size int64
		switch name {
		case "boot.ipxe":
			base := p.origin() + "/" + parts[1] + "/" + r.BootToken
			data = []byte(fmt.Sprintf("#!ipxe\nkernel %s/kernel console=ttyS0 rdinit=/init fleet.receipt=%s\ninitrd %s/installer.gz\nboot\n", base, r.Receipt, base))
			size = int64(len(data))
		case "kernel", "installer.gz":
			// Stream: hash the pinned artifact, then serve the same open file.
			artifact, err = os.Open(artifacts + "/" + name)
			if err != nil {
				deny()
				return
			}
			defer artifact.Close()
			manifest, e := os.ReadFile(artifacts + "/SHA256SUMS")
			sum := sha256.Sum256(manifest)
			h := sha256.New()
			size, err = io.Copy(h, artifact)
			if e != nil || err != nil || p.image != "fleet-local-linux@sha256:"+hex.EncodeToString(sum[:]) || !strings.Contains(string(manifest), hex.EncodeToString(h.Sum(nil))+"  "+name+"\n") {
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
		// No URLs, headers, credentials or bootstrap script in evidence.
		if err := os.MkdirAll(p.bootDirectory(r), 0700); err != nil {
			deny()
			return
		}
		f, err := os.OpenFile(p.bootDirectory(r)+"/boot-delivery.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			deny()
			return
		}
		_, err = fmt.Fprintf(f, "%s admitted %s bytes=%d\n", time.Now().UTC().Format(time.RFC3339), name, size)
		if err == nil {
			err = f.Sync()
		}
		f.Close()
		if err != nil {
			deny()
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
		var n int64
		var writeErr error
		if artifact != nil {
			n, writeErr = io.Copy(w, artifact)
		} else {
			var written int
			written, writeErr = w.Write(data)
			n = int64(written)
		}
		if writeErr != nil || n != size {
			return
		}
		// A completed server write is not proof of client receipt or guest boot.
		f, err = os.OpenFile(p.bootDirectory(r)+"/boot-delivery.log", os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return
		}
		_, err = fmt.Fprintf(f, "%s write-complete %s bytes=%d\n", time.Now().UTC().Format(time.RFC3339), name, n)
		if err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err == nil && closeErr == nil {
			_ = p.syncDir(p.bootDirectory(r))
		}
	})
}

func (p *Provider) ServeBoot() error {
	// The write bound matches the boot admission window: through the site's
	// NAT path an emulated firmware takes several minutes for the installer.
	s := &http.Server{Addr: ":8080", Handler: p.BootHandler(), ReadHeaderTimeout: 5 * time.Second, WriteTimeout: bootWindow, IdleTimeout: 5 * time.Second}
	return s.ListenAndServe()
}

func (p *Provider) verifyNetworkBoot(ctx context.Context, r *record) error {
	b, err := os.ReadFile(p.bootDirectory(r) + "/boot-delivery.log")
	if err != nil {
		return err
	}
	completed := map[string]bool{}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 4 || fields[1] != "write-complete" || !strings.HasPrefix(fields[3], "bytes=") {
			continue
		}
		n, err := strconv.Atoi(strings.TrimPrefix(fields[3], "bytes="))
		if err == nil && n > 0 {
			completed[fields[2]] = true
		}
	}
	for _, name := range []string{"boot.ipxe", "kernel", "installer.gz"} {
		if !completed[name] {
			return fmt.Errorf("missing network delivery: %s", name)
		}
	}
	leases, err := p.virsh(ctx, "net-dhcp-leases", "fleet-pxe")
	if err != nil {
		return err
	}
	if !strings.Contains(leases, "52:54:00:77:00:02") || !strings.Contains(leases, "192.168.77.2") {
		return fmt.Errorf("missing firmware DHCP lease")
	}
	return os.WriteFile(p.directory(r)+"/dhcp-leases.txt", []byte(leases), 0600)
}

func (p *Provider) stopNetwork(ctx context.Context) error {
	names, err := p.virsh(ctx, "net-list", "--name")
	if err != nil {
		return err
	}
	for _, n := range strings.Fields(names) {
		if n == "fleet-pxe" {
			_, err = p.virsh(ctx, "net-destroy", n)
			return err
		}
	}
	return nil
}

// CheckClosedBoot probes a live boot listener without printing capability URLs.
// origin is the listener under test, e.g. the boot Service seen by the runtime.
func (p *Provider) CheckClosedBoot(origin string) (string, error) {
	origin, err := parseBootOrigin(origin)
	if err != nil {
		return "", err
	}
	j, err := p.read()
	if err != nil {
		return "", err
	}
	client := &http.Client{Timeout: 10 * time.Second}
	count := 0
	for _, r := range j.Operations {
		if r.Request.Kind != "install" {
			continue
		}
		if !r.BootClosed {
			return "", fmt.Errorf("boot admission not closed")
		}
		base := origin + "/" + key(r.Request.ID)[:16] + "/" + r.BootToken
		for _, url := range []string{base + "/boot.ipxe", base + "/kernel", base + "/installer.gz", base + "/disk.raw", origin + "/kernel", origin + "/unknown/unknown/boot.ipxe"} {
			resp, e := client.Get(url)
			if e != nil {
				return "", fmt.Errorf("boot denial probe unavailable")
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden {
				return "", fmt.Errorf("closed/unknown boot access allowed")
			}
			count++
		}
	}
	if count == 0 {
		return "", fmt.Errorf("no retained admission to probe")
	}
	return fmt.Sprintf("PASS %d live HTTP denials: closed predecessor scripts/artifacts, unsupported artifact, unguarded artifact, unknown identity", count), nil
}
