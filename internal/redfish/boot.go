package redfish

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// The provisioning network is site infrastructure: its DHCP hands every
// enrolled NIC the same static script, which chains to a per-MAC URL. The MAC
// is routing information, not authentication. Only a retained operation whose
// enrolled target has that MAC and whose boot admission is open receives a
// script, and that script carries the operation's scoped capability.
const chainScript = "#!ipxe\nchain %s/pxe/${net0/mac:hexhyp}/boot.ipxe\n"

func (p *Provider) admissionOpen(j journal, r *record) bool {
	if j.Closed || j.Image != p.image || r.BootClosed || len(r.BootToken) != 64 || !p.clock().Before(r.BootExpires) {
		return false
	}
	if r.Request.BindingID != j.Binding || r.Request.Epoch != j.Epoch || r.Request.FenceToken != j.Fence {
		return false
	}
	for _, ph := range phases[r.Request.Kind] {
		if ph.mode != "" && (r.Stage == ph.name+"-requested" || r.Stage == ph.name+"-started") {
			return true
		}
	}
	return false
}

func mode(r *record) string {
	if r.Request.Kind == "cleanup" {
		return "wipe"
	}
	return "install"
}

// BootHandler never serves a caller-selected path. It reads the atomically
// replaced journal on every request and writes only delivery evidence.
func (p *Provider) BootHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		deny := func() { http.Error(w, "boot denied", http.StatusForbidden) }
		parts := strings.Split(q.URL.Path, "/")
		if q.Method != http.MethodGet || q.URL.RawQuery != "" {
			deny()
			return
		}
		if q.URL.Path == "/pxe/boot.ipxe" {
			fmt.Fprintf(w, chainScript, p.origin)
			return
		}
		j, err := p.read()
		if err != nil || len(parts) != 4 {
			deny()
			return
		}
		var r *record
		var name string
		if parts[1] == "pxe" && parts[3] == "boot.ipxe" {
			mac, ok := normalizeMAC(parts[2])
			for _, c := range j.Operations {
				if ok && c.Target.MAC == mac && p.admissionOpen(j, c) {
					r, name = c, "boot.ipxe"
				}
			}
		} else {
			for _, c := range j.Operations {
				if key(c.Request.ID)[:16] == parts[1] && len(c.BootToken) == 64 && subtle.ConstantTimeCompare([]byte(parts[2]), []byte(c.BootToken)) == 1 && p.admissionOpen(j, c) {
					r, name = c, parts[3]
				}
			}
		}
		if r == nil || (name != "boot.ipxe" && name != "kernel" && name != "installer.gz") || (name == "boot.ipxe" && parts[1] != "pxe") {
			deny()
			return
		}
		var data []byte
		var artifact *os.File
		var size int64
		if name == "boot.ipxe" {
			base := p.origin + "/" + key(r.Request.ID)[:16] + "/" + r.BootToken
			data = []byte(fmt.Sprintf("#!ipxe\nkernel %s/kernel console=ttyS0 rdinit=/init fleet.receipt=%s fleet.mode=%s\ninitrd %s/installer.gz\nboot\n", base, r.Receipt, mode(r), base))
			size = int64(len(data))
		} else {
			// Hash the pinned artifact, then serve the same open file.
			if artifact, err = os.Open(p.artifacts + "/" + name); err != nil {
				deny()
				return
			}
			defer artifact.Close()
			manifest, e := os.ReadFile(p.artifacts + "/SHA256SUMS")
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
		}
		if err := p.evidence(r, "admitted", name, size); err != nil {
			deny()
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
		var n int64
		var werr error
		if artifact != nil {
			n, werr = io.Copy(w, artifact)
		} else {
			var written int
			written, werr = w.Write(data)
			n = int64(written)
		}
		// A completed server write is not proof of client receipt or boot.
		if werr == nil && n == size {
			_ = p.evidence(r, "write-complete", name, n)
		}
	})
}

// evidence appends one line without URLs, headers, receipts or capabilities.
func (p *Provider) evidence(r *record, event, name string, size int64) error {
	dir := p.bootDirectory(r)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, "boot-delivery.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(f, "%s %s %s bytes=%d\n", p.clock().Format(time.RFC3339), event, name, size)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = syncDir(dir)
	}
	return err
}

// ServeBoot listens on :8080. The write bound covers an emulated firmware
// fetching the initrd through the site's NAT path.
func (p *Provider) ServeBoot() error {
	s := &http.Server{Addr: ":8080", Handler: p.BootHandler(), ReadHeaderTimeout: 5 * time.Second, WriteTimeout: bootWindow, IdleTimeout: 5 * time.Second}
	return s.ListenAndServe()
}

// CheckClosedBoot probes a live boot listener after every admission closed:
// each retained operation's MAC script and capability URLs, an unsupported
// artifact, an unguarded artifact and an unknown identity must be denied.
func (p *Provider) CheckClosedBoot(origin string, extraMACs []string) (string, error) {
	origin, err := ParseBootOrigin(origin)
	if err != nil {
		return "", err
	}
	j, err := p.read()
	if err != nil {
		return "", err
	}
	client := &http.Client{Timeout: 10 * time.Second}
	count := 0
	probe := func(u string) error {
		resp, err := client.Get(u)
		if err != nil {
			return fmt.Errorf("boot denial probe unavailable")
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			return fmt.Errorf("closed or unknown boot access allowed")
		}
		count++
		return nil
	}
	for _, r := range j.Operations {
		if !r.BootClosed {
			return "", fmt.Errorf("boot admission not closed")
		}
		base := origin + "/" + key(r.Request.ID)[:16] + "/" + r.BootToken
		macPath := strings.ReplaceAll(r.Target.MAC, ":", "-")
		for _, u := range []string{origin + "/pxe/" + macPath + "/boot.ipxe", base + "/kernel", base + "/installer.gz", base + "/disk.raw", base + "/boot.ipxe"} {
			if err := probe(u); err != nil {
				return "", err
			}
		}
	}
	for _, mac := range extraMACs {
		if err := probe(origin + "/pxe/" + strings.ReplaceAll(mac, ":", "-") + "/boot.ipxe"); err != nil {
			return "", err
		}
	}
	for _, u := range []string{origin + "/kernel", origin + "/unknown/unknown/kernel", origin + "/pxe/not-a-mac/boot.ipxe"} {
		if err := probe(u); err != nil {
			return "", err
		}
	}
	if len(j.Operations) == 0 {
		return "", fmt.Errorf("no retained admission to probe")
	}
	return fmt.Sprintf("PASS %d live HTTP denials: closed per-MAC scripts and capabilities of %d operations, other enrolled MACs, unsupported and unguarded artifacts, unknown identity", count, len(j.Operations)), nil
}
