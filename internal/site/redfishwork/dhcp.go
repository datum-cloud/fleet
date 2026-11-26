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
	"sync"
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

// contactInterval is the least time between two retained refused contacts
// from one instance name, so a polling replica outside the expected set
// grows the log slowly.
const contactInterval = 30 * time.Second

// contactQuiet is how long after an admission was published the withdrawal
// gate waits before it can pass: one contactInterval plus a synchronizer
// poll (five seconds by default; at most fifteen is assumed), so an instance
// still polling after the admission was published has a retained contact
// by then.
const contactQuiet = contactInterval + 15*time.Second

// issuesKept is how many served feeds the feed remembers per instance.
const issuesKept = 32

// issue is one feed the feed server served: a report must echo one.
type issue struct {
	issued   time.Time
	revision string
}

// report is one retained applied report: when the boot server accepted it,
// and which DHCP instance applied which exact feed, as the feed server issued
// it. A Rejected record is instead an authenticated contact the feed refused
// because its instance was missing or outside the expected set: it has no
// feed, and its instance may be empty or not a valid name. It is never
// evidence that anything was served or withdrawn; it only shows that an
// instance the proof does not watch was still running at At.
type report struct {
	At       time.Time    `json:"at"`
	Instance string       `json:"instance"`
	Feed     bootnet.Feed `json:"feed"`
	Rejected bool         `json:"rejected,omitempty"`
}

// contact is how a refused contact is retained.
type contact struct {
	At       time.Time `json:"at"`
	Instance string    `json:"instance"`
	Rejected bool      `json:"rejected"`
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
//
// A report is retained only if it echoes a feed this handler served to the
// same instance, by issue time and revision, among the last issuesKept it
// served it; any other is refused with 409 and not retained. So a report's
// issue time is one the site's clock gave, never one a token holder chose.
// The handler remembers issues in memory only: after a restart, reports are
// refused until the instance fetches again, which its synchronizer does on
// its next cycle before it reports again.
func (b BootServer) DHCPHandler(token string) (http.Handler, error) {
	if len(token) < 32 || strings.ContainsAny(token, " \t\r\n") {
		return nil, errors.New("the DHCP synchronizer token must be at least 32 characters without whitespace")
	}
	if len(b.DHCPInstances) == 0 {
		return nil, errors.New("the DHCP feed needs the enrollment's expected DHCP instances")
	}
	allowed := expected(b.DHCPInstances)
	var mu sync.Mutex
	recorded := map[string]time.Time{}
	var issuedMu sync.Mutex
	issues := map[string][]issue{}
	// refuse retains, at most once per instance name per contactInterval,
	// that an authenticated instance outside the expected set is still
	// running, then answers 403 whether or not that was retained.
	refuse := func(w http.ResponseWriter, instance string) {
		if len(instance) > 253 {
			instance = instance[:253]
		}
		mu.Lock()
		now := b.now().UTC()
		if last, ok := recorded[instance]; !ok || now.Sub(last) >= contactInterval {
			if len(recorded) > 1024 {
				for name, at := range recorded {
					if now.Sub(at) >= contactInterval {
						delete(recorded, name)
					}
				}
			}
			if b.retainContact(contact{At: now, Instance: instance, Rejected: true}) == nil {
				recorded[instance] = now
			}
		}
		mu.Unlock()
		http.Error(w, "not an expected DHCP instance", http.StatusForbidden)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		got, ok := strings.CutPrefix(request.Header.Get("Authorization"), "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/dhcp/v1/reservations" && request.URL.RawQuery == "":
			if instance := request.Header.Get(bootnet.InstanceHeader); !allowed[instance] {
				refuse(w, instance)
				return
			}
			feed, err := b.Issue()
			if err != nil {
				http.Error(w, "reservations unavailable", http.StatusServiceUnavailable)
				return
			}
			instance := request.Header.Get(bootnet.InstanceHeader)
			issuedMu.Lock()
			kept := append(issues[instance], issue{issued: feed.Issued, revision: feed.Revision})
			if len(kept) > issuesKept {
				kept = kept[len(kept)-issuesKept:]
			}
			issues[instance] = kept
			issuedMu.Unlock()
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
				refuse(w, applied.Instance)
				return
			}
			served := false
			issuedMu.Lock()
			for _, i := range issues[applied.Instance] {
				if i.issued.Equal(applied.Feed.Issued) && i.revision == applied.Feed.Revision {
					served = true
				}
			}
			issuedMu.Unlock()
			if !served {
				http.Error(w, "the report echoes no feed served to this instance", http.StatusConflict)
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

// retainContact appends one refused contact and syncs it.
func (b BootServer) retainContact(c contact) error {
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return appendEvidence(b.BootRoot, dhcpLog, string(data)+"\n")
}

// reports reads every retained report in order. An unreadable line is
// corruption, never evidence in either direction. A report retained before
// feeds carried an issue time reads as issued at the zero time, so it can
// never prove a withdrawal. A retired line is skipped (see retainedLine). A
// rejected contact carries no feed to verify.
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
		if json.Unmarshal(scanner.Bytes(), &line) != nil || line.At.IsZero() || (line.Rejected && line.Retired) ||
			(!line.Rejected && (bootnet.Applied{Instance: line.Instance, Feed: line.Feed}).Verify() != nil) {
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
			if !r.Rejected && allowed[r.Instance] && r.At.After(boot.Created) && r.Feed.Has(boot.MAC, boot.Address) {
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
//
// Removing a name does not stop its replica. A removed replica whose
// synchronizer still polls is refused by the feed, which retains that
// contact, so the gate also blocks while any refused contact was retained
// after the admission was published, and passes no earlier than
// contactQuiet after it, by which time a still-polling replica has been
// retained despite the rate limit. A replica whose synchronizer stopped
// while its Kea keeps serving is not seen.
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
		served, contacted, latest := false, false, map[string]bootnet.Feed{}
		for _, r := range retained {
			if r.Rejected {
				if !r.At.Before(boot.Published) {
					contacted = true
				}
				continue
			}
			if !allowed[r.Instance] {
				continue
			}
			if r.At.After(boot.Created) && r.Feed.Has(boot.MAC, boot.Address) {
				served = true
			}
			latest[r.Instance] = r.Feed
		}
		withdrawn := served && !contacted && !boot.Published.IsZero() && len(allowed) > 0 && !c.now().Before(boot.Published.Add(c.quiet()))
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

// quiet is contactQuiet unless a test shortens it.
func (c Config) quiet() time.Duration {
	if c.ContactQuiet > 0 {
		return c.ContactQuiet
	}
	return contactQuiet
}
