package redfishwork

import (
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

	"go.datum.net/fleet/internal/bootnet"
	"go.datum.net/fleet/internal/redfish"
	"go.datum.net/fleet/internal/site"
)

// The provisioning network is site infrastructure: its DHCP hands every
// enrolled NIC the same static script, which chains to a per-MAC URL. The MAC
// is routing, not authentication. Only an open admission for that MAC, at the
// Device's stored fence, receives a script, and the script carries the
// operation's capability.
const chainScript = "#!ipxe\nchain %s/pxe/${net0/mac:hexhyp}/boot.ipxe\n"

// BootServer serves network boots from the site store's admissions.
type BootServer struct {
	Store *site.Store
	// Origin is the boot server's URL as firmware reaches it.
	Origin string
	// Artifacts holds the installer kernel, initrd and their SHA256SUMS.
	Artifacts string
	// Image is the artifact manifest's identity.
	Image string
	// BootRoot holds the per-operation delivery evidence and the retained
	// DHCP reports the workers read.
	BootRoot string
	// Enrollment names each Device's provisioning attachment when the
	// site's DHCP is synchronized.
	Enrollment redfish.Enrollment
	Now        func() time.Time
}

func (b BootServer) now() time.Time {
	if b.Now != nil {
		return b.Now()
	}
	return time.Now()
}

// Key is an operation's key in capability URLs.
func Key(operationUID string) string {
	sum := sha256.Sum256([]byte(operationUID))
	return hex.EncodeToString(sum[:])[:16]
}

// open returns the open admission whose key and token match, at the
// Device's stored fence.
func (b BootServer) open(key, token string) (Boot, bool) {
	records, err := b.Store.Records(bootRecords)
	if err != nil {
		return Boot{}, false
	}
	for _, record := range records {
		var boot Boot
		if json.Unmarshal(record.Data, &boot) != nil || Key(boot.Operation) != key || len(boot.Token) != 64 || subtle.ConstantTimeCompare([]byte(token), []byte(boot.Token)) != 1 || boot.Phase == "" || boot.Closed || !b.now().Before(boot.Expires) {
			continue
		}
		if fence, _, err := b.Store.Peek(record.Device); err == nil && fence == boot.Fence {
			return boot, true
		}
	}
	return Boot{}, false
}

// Handler never serves a caller-selected path, and records deliveries
// without URLs, headers, receipts or capabilities.
func (b BootServer) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		deny := func() { http.Error(w, "boot denied", http.StatusForbidden) }
		parts := strings.Split(request.URL.Path, "/")
		if request.Method != http.MethodGet || request.URL.RawQuery != "" {
			deny()
			return
		}
		if request.URL.Path == "/pxe/boot.ipxe" {
			fmt.Fprintf(w, chainScript, b.Origin)
			return
		}
		if len(parts) != 4 {
			deny()
			return
		}
		var boot Boot
		var name string
		if parts[1] == "pxe" && parts[3] == "boot.ipxe" {
			mac, ok := bootnet.NormalizeMAC(strings.ReplaceAll(parts[2], "-", ":"))
			if !ok {
				deny()
				return
			}
			found, open, err := OpenAdmission(b.Store, mac, b.now())
			if err != nil || !open {
				deny()
				return
			}
			boot, name = found, "boot.ipxe"
		} else {
			found, open := b.open(parts[1], parts[2])
			if !open || (parts[3] != "kernel" && parts[3] != "installer.gz") {
				deny()
				return
			}
			boot, name = found, parts[3]
		}
		var data []byte
		var artifact *os.File
		var size int64
		if name == "boot.ipxe" {
			base := b.Origin + "/" + Key(boot.Operation) + "/" + boot.Token
			data = []byte(fmt.Sprintf("#!ipxe\nkernel %s/kernel console=ttyS0 rdinit=/init fleet.receipt=%s fleet.mode=%s\ninitrd %s/installer.gz\nboot\n", base, boot.Receipt, boot.Mode, base))
			size = int64(len(data))
		} else {
			// Hash the pinned artifact, then serve the same open file.
			var err error
			if artifact, err = os.Open(filepath.Join(b.Artifacts, name)); err != nil {
				deny()
				return
			}
			defer artifact.Close()
			manifest, manifestErr := os.ReadFile(filepath.Join(b.Artifacts, "SHA256SUMS"))
			sum := sha256.Sum256(manifest)
			hash := sha256.New()
			size, err = io.Copy(hash, artifact)
			if manifestErr != nil || err != nil || b.Image != "fleet-local-linux@sha256:"+hex.EncodeToString(sum[:]) || !strings.Contains(string(manifest), hex.EncodeToString(hash.Sum(nil))+"  "+name+"\n") {
				deny()
				return
			}
			if _, err = artifact.Seek(0, io.SeekStart); err != nil {
				deny()
				return
			}
		}
		dir := BootDirectory(b.BootRoot, boot.Operation)
		if err := appendEvidence(dir, "boot-delivery.log", fmt.Sprintf("%s admitted %s bytes=%d\n", b.now().UTC().Format(time.RFC3339), name, size)); err != nil {
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
			_ = appendEvidence(dir, "boot-delivery.log", fmt.Sprintf("%s write-complete %s bytes=%d\n", b.now().UTC().Format(time.RFC3339), name, written))
		}
	})
}

func appendEvidence(dir, name, line string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
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
	parent, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer parent.Close()
	return parent.Sync()
}

// Serve listens on :8080. The write bound covers emulated firmware fetching
// the initrd through the site's NAT path.
func (b BootServer) Serve(window time.Duration) error {
	server := &http.Server{Addr: ":8080", Handler: b.Handler(), ReadHeaderTimeout: 5 * time.Second, WriteTimeout: window, IdleTimeout: 5 * time.Second}
	return server.ListenAndServe()
}

// CheckClosedBoot probes a live boot server after every admission closed:
// each admission's per-MAC script and capability URLs, other enrolled MACs,
// an unsupported and an unguarded artifact, and an unknown identity must
// all be denied. It prints no capability URL.
func (b BootServer) CheckClosedBoot(origin string, extraMACs []string) (string, error) {
	records, err := b.Store.Records(bootRecords)
	if err != nil {
		return "", err
	}
	client := &http.Client{Timeout: 10 * time.Second}
	count := 0
	probe := func(url string) error {
		response, err := client.Get(url)
		if err != nil {
			return fmt.Errorf("the boot denial probe is unavailable")
		}
		response.Body.Close()
		if response.StatusCode != http.StatusForbidden {
			return fmt.Errorf("a closed or unknown boot was served")
		}
		count++
		return nil
	}
	for _, record := range records {
		var boot Boot
		if err := json.Unmarshal(record.Data, &boot); err != nil {
			return "", err
		}
		if !boot.Closed {
			return "", fmt.Errorf("a boot admission is not closed")
		}
		base := origin + "/" + Key(boot.Operation) + "/" + boot.Token
		for _, url := range []string{origin + "/pxe/" + strings.ReplaceAll(boot.MAC, ":", "-") + "/boot.ipxe", base + "/kernel", base + "/installer.gz", base + "/disk.raw", base + "/boot.ipxe"} {
			if err := probe(url); err != nil {
				return "", err
			}
		}
	}
	for _, mac := range extraMACs {
		if err := probe(origin + "/pxe/" + strings.ReplaceAll(mac, ":", "-") + "/boot.ipxe"); err != nil {
			return "", err
		}
	}
	for _, url := range []string{origin + "/kernel", origin + "/unknown/unknown/kernel", origin + "/pxe/not-a-mac/boot.ipxe"} {
		if err := probe(url); err != nil {
			return "", err
		}
	}
	if len(records) == 0 {
		return "", fmt.Errorf("no boot admission to probe")
	}
	return fmt.Sprintf("PASS %d live HTTP denials: closed per-MAC scripts and capabilities of %d operations, other enrolled MACs, unsupported and unguarded artifacts, unknown identity", count, len(records)), nil
}
