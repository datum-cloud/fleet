package redfishwork

import (
	"bufio"
	"context"
	"crypto/subtle"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.datum.net/fleet/internal/bootnet"
	"go.datum.net/fleet/internal/site"
)

// withdrawalWindow bounds how long a worker waits, after the network boot
// closed, for every expected DHCP instance to report the reservation
// withdrawn. Past it the outcome stays unknown, and a resumed worker waits
// again.
const withdrawalWindow = 10 * time.Minute

const dhcpLog = "dhcp-applied.log"

// report is one retained applied report: when the boot server accepted it,
// and which DHCP instance applied which exact feed, as the feed server issued
// it.
type report struct {
	At       time.Time    `json:"at"`
	Instance string       `json:"instance"`
	Feed     bootnet.Feed `json:"feed"`
}

// retainedLine is how a retained line is read. A line carrying "retired" was
// written by the operator retirement command this site no longer has; it
// is ignored and has no effect: it is neither a report nor evidence, and an
// instance is retired only by leaving the enrollment's expected set.
type retainedLine struct {
	report
	Retired bool `json:"retired"`
}

// expected is the enrollment's expected DHCP instance set.
func expected(instances []string) map[string]bool {
	out := map[string]bool{}
	for _, instance := range instances {
		out[instance] = true
	}
	return out
}

// Reservations is the complete desired reservation set: each boot NIC whose
// admission carries a boot address and is not closed, unexpired and at the
// Device's stored fence holds that address. The MAC comes from the admission
// snapshot and the address from the operation's inputs. It is empty when no
// network boot is admitted.
func (b BootServer) Reservations() (bootnet.Feed, error) {
	records, err := b.Store.Records(bootRecords)
	if err != nil {
		return bootnet.Feed{}, err
	}
	reservations := []bootnet.Reservation{}
	for _, record := range records {
		var boot Boot
		if json.Unmarshal(record.Data, &boot) != nil || boot.Address == "" || boot.Closed || !b.now().Before(boot.Expires) {
			continue
		}
		if fence, _, err := b.Store.Peek(record.Device); err != nil || fence != boot.Fence {
			continue
		}
		reservations = append(reservations, bootnet.Reservation{MAC: boot.MAC, Address: boot.Address, Expires: boot.Expires})
	}
	return bootnet.NewFeed(reservations)
}

// Issue is the feed served now: the clock is read before the reservations,
// so every reservation stored before Issued is in the feed.
func (b BootServer) Issue() (bootnet.Feed, error) {
	issued := b.now().UTC().Truncate(time.Second)
	feed, err := b.Reservations()
	feed.Issued = issued
	return feed, err
}

// DHCPHandler serves the reservation feed and accepts applied reports from
// the DHCP owner's synchronizer, authenticated by a bearer token. It is
// never served on the firmware-facing boot listener. It serves a feed only
// to, and retains a report only from, an instance in the enrollment's
// expected set, so an instance outside it never gets a reservation to
// serve; a feed request names its instance in bootnet.InstanceHeader.
func (b BootServer) DHCPHandler(token string) (http.Handler, error) {
	if len(token) < 32 || strings.ContainsAny(token, " \t\r\n") {
		return nil, errors.New("the DHCP synchronizer token must be at least 32 characters without whitespace")
	}
	if len(b.DHCPInstances) == 0 {
		return nil, errors.New("the DHCP feed needs the enrollment's expected DHCP instances")
	}
	allowed := expected(b.DHCPInstances)
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		got, ok := strings.CutPrefix(request.Header.Get("Authorization"), "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/dhcp/v1/reservations" && request.URL.RawQuery == "":
			if !allowed[request.Header.Get(bootnet.InstanceHeader)] {
				http.Error(w, "not an expected DHCP instance", http.StatusForbidden)
				return
			}
			feed, err := b.Issue()
			if err != nil {
				http.Error(w, "reservations unavailable", http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(feed)
		case request.Method == http.MethodPost && request.URL.Path == "/dhcp/v1/applied" && request.URL.RawQuery == "":
			var applied bootnet.Applied
			decoder := json.NewDecoder(io.LimitReader(request.Body, 64<<10))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&applied) != nil || decoder.Decode(&struct{}{}) != io.EOF || applied.Verify() != nil {
				http.Error(w, "invalid applied report", http.StatusBadRequest)
				return
			}
			if !allowed[applied.Instance] {
				http.Error(w, "not an expected DHCP instance", http.StatusForbidden)
				return
			}
			if err := b.retain(applied); err != nil {
				http.Error(w, "applied report not retained", http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}), nil
}

// ServeDHCP listens with TLS on :8081, apart from the boot listener. The
// synchronizer accepts the feed only over TLS.
func (b BootServer) ServeDHCP(token, certFile, keyFile string) error {
	if certFile == "" || keyFile == "" {
		return errors.New("the DHCP feed needs a TLS certificate and key")
	}
	handler, err := b.DHCPHandler(token)
	if err != nil {
		return err
	}
	server := &http.Server{Addr: ":8081", Handler: handler, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12}, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second}
	return server.ListenAndServeTLS(certFile, keyFile)
}

// retain appends one report and syncs it before acknowledging.
func (b BootServer) retain(applied bootnet.Applied) error {
	data, err := json.Marshal(report{At: b.now().UTC(), Instance: applied.Instance, Feed: applied.Feed})
	if err != nil {
		return err
	}
	return appendEvidence(b.BootRoot, dhcpLog, string(data)+"\n")
}

// reports reads every retained report in order. An unreadable line is
// corruption, never evidence in either direction. A report retained before
// feeds carried an issue time reads as issued at the zero time, so it can
// never prove a withdrawal. A retired line is skipped (see retainedLine).
func reports(root string) ([]report, error) {
	file, err := os.Open(filepath.Join(root, dhcpLog))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var out []report
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for scanner.Scan() {
		var line retainedLine
		if json.Unmarshal(scanner.Bytes(), &line) != nil || line.At.IsZero() || (bootnet.Applied{Instance: line.Instance, Feed: line.Feed}).Verify() != nil {
			return nil, errors.New("invalid retained DHCP report")
		}
		if !line.Retired {
			out = append(out, line.report)
		}
	}
	return out, scanner.Err()
}

// awaitReservation waits, without any effect, until some expected DHCP
// instance reports serving the boot NIC at the operation's exact address
// since this operation's boot admission was created.
func (c Config) awaitReservation(ctx context.Context, boot Boot) error {
	poll, _, _, _ := c.durations()
	allowed := expected(c.DHCPInstances)
	for {
		retained, err := reports(c.BootRoot)
		if err != nil {
			return err
		}
		for _, r := range retained {
			if allowed[r.Instance] && r.At.After(boot.Created) && r.Feed.Has(boot.MAC, boot.Address) {
				return nil
			}
		}
		if !c.now().Before(boot.Expires) {
			return errors.New("DHCP did not apply the boot reservation before the boot admission expired")
		}
		if err := sleep(ctx, poll); err != nil {
			return err
		}
	}
}

// awaitWithdrawal requires that some expected DHCP instance reported
// serving the reservation during this operation, and that every expected
// instance's latest report is of a feed issued after the admission was
// published (Boot.Published) that serves no MAC or address this site ever
// published for the Device. Reports from instances outside the enrollment's
// expected set never count.
//
// While the admission is open, every feed issued after it was published
// carries the reservation. So a report of a feed issued after that without
// it was issued after the reservation left the feed, and every later feed
// lacks it too. An instance that fetched a feed with the reservation and
// stopped before reporting it still has a latest report that was either
// issued before the admission was published or serves the reservation, so
// it blocks; so does an expected instance that never reported. Blocking
// keeps the outcome unknown past withdrawalWindow; it never passes. The
// feed server and the worker read one site clock: both mount the site's
// state volume. A success also proves that every earlier reservation for
// the Device, such as a held operation's, is withdrawn. The only way past an
// instance that is gone is to remove it from the expected set.
func (c Config) awaitWithdrawal(ctx context.Context, store *site.Store, device string, boot Boot) error {
	poll, _, _, _ := c.durations()
	deadline := c.now().Add(withdrawalWindow)
	allowed := expected(c.DHCPInstances)
	for {
		published, err := publishedFor(store, device)
		if err != nil {
			return err
		}
		retained, err := reports(c.BootRoot)
		if err != nil {
			return err
		}
		served, latest := false, map[string]bootnet.Feed{}
		for _, r := range retained {
			if !allowed[r.Instance] {
				continue
			}
			if r.At.After(boot.Created) && r.Feed.Has(boot.MAC, boot.Address) {
				served = true
			}
			latest[r.Instance] = r.Feed
		}
		withdrawn := served && !boot.Published.IsZero() && len(allowed) > 0
		for instance := range allowed {
			feed, reported := latest[instance]
			if !reported || !feed.Issued.After(boot.Published) {
				withdrawn = false
				continue
			}
			for _, p := range published {
				if feed.Serves(p.MAC, p.Address) {
					withdrawn = false
				}
			}
		}
		if withdrawn {
			return nil
		}
		if c.now().After(deadline) {
			return errors.New("DHCP has not reported the boot reservation withdrawn")
		}
		if err := sleep(ctx, poll); err != nil {
			return err
		}
	}
}

// publishedFor returns every boot admission of the Device that carried a
// boot address, whatever its fence or state.
func publishedFor(store *site.Store, device string) ([]Boot, error) {
	records, err := store.Records(bootRecords)
	if err != nil {
		return nil, err
	}
	var out []Boot
	for _, record := range records {
		var boot Boot
		if record.Device != device {
			continue
		}
		if err := json.Unmarshal(record.Data, &boot); err != nil {
			return nil, errors.New("an unreadable boot admission")
		}
		if boot.Address != "" {
			out = append(out, boot)
		}
	}
	return out, nil
}
