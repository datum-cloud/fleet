package redfishwork

import (
	"bufio"
	"context"
	"crypto/subtle"
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
// closed, for every DHCP instance that served the reservation to report it
// withdrawn. Past it the outcome stays unknown, and a resumed worker waits
// again.
const withdrawalWindow = 10 * time.Minute

const dhcpLog = "dhcp-applied.log"

// report is one retained applied report: when the boot server accepted it,
// and which DHCP instance applied which exact feed. A Retired record is an
// operator's statement that the instance is gone and serves nothing; a later
// report from the same instance counts again.
type report struct {
	At       time.Time    `json:"at"`
	Instance string       `json:"instance"`
	Feed     bootnet.Feed `json:"feed"`
	Retired  bool         `json:"retired,omitempty"`
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

// DHCPHandler serves the reservation feed and accepts applied reports from
// the DHCP owner's synchronizer, authenticated by a bearer token. It is
// never served on the firmware-facing boot listener.
func (b BootServer) DHCPHandler(token string) (http.Handler, error) {
	if len(token) < 32 || strings.ContainsAny(token, " \t\r\n") {
		return nil, errors.New("the DHCP synchronizer token must be at least 32 characters without whitespace")
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
			feed, err := b.Reservations()
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

// ServeDHCP listens on :8081, apart from the boot listener.
func (b BootServer) ServeDHCP(token string) error {
	handler, err := b.DHCPHandler(token)
	if err != nil {
		return err
	}
	server := &http.Server{Addr: ":8081", Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second}
	return server.ListenAndServe()
}

// retain appends one report and syncs it before acknowledging.
func (b BootServer) retain(applied bootnet.Applied) error {
	data, err := json.Marshal(report{At: b.now().UTC(), Instance: applied.Instance, Feed: applied.Feed})
	if err != nil {
		return err
	}
	return appendEvidence(b.BootRoot, dhcpLog, string(data)+"\n")
}

// RetireInstance records that a DHCP instance is gone. Withdrawal then stops
// waiting for it. Only an operator at the site runs it, after making sure the
// instance's server no longer answers.
func RetireInstance(root, instance string) error {
	empty, _ := bootnet.NewFeed(nil)
	if err := (bootnet.Applied{Instance: instance, Feed: empty}).Verify(); err != nil {
		return err
	}
	data, err := json.Marshal(report{At: time.Now().UTC(), Instance: instance, Feed: empty, Retired: true})
	if err != nil {
		return err
	}
	return appendEvidence(root, dhcpLog, string(data)+"\n")
}

// reports reads every retained report in order. An unreadable line is
// corruption, never evidence in either direction.
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
		var r report
		if json.Unmarshal(scanner.Bytes(), &r) != nil || r.At.IsZero() || (bootnet.Applied{Instance: r.Instance, Feed: r.Feed}).Verify() != nil {
			return nil, errors.New("invalid retained DHCP report")
		}
		out = append(out, r)
	}
	return out, scanner.Err()
}

// awaitReservation waits, without any effect, until some DHCP instance
// reports serving the boot NIC at the operation's exact address since this
// operation's boot admission was created.
func (c Config) awaitReservation(ctx context.Context, boot Boot) error {
	poll, _, _, _ := c.durations()
	for {
		retained, err := reports(c.BootRoot)
		if err != nil {
			return err
		}
		for _, r := range retained {
			if !r.Retired && r.At.After(boot.Created) && r.Feed.Has(boot.MAC, boot.Address) {
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

// awaitWithdrawal requires that some DHCP instance served the reservation
// during this operation, and that no instance's latest report, however old,
// serves any MAC or address this site ever published for the Device. An
// instance that went silent while serving one keeps the outcome unknown
// until an operator retires it, so a success also proves that every earlier
// reservation for the Device, such as a held operation's, is withdrawn.
func (c Config) awaitWithdrawal(ctx context.Context, store *site.Store, device string, boot Boot) error {
	poll, _, _, _ := c.durations()
	deadline := c.now().Add(withdrawalWindow)
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
			if r.Retired {
				delete(latest, r.Instance)
				continue
			}
			if r.At.After(boot.Created) && r.Feed.Has(boot.MAC, boot.Address) {
				served = true
			}
			latest[r.Instance] = r.Feed
		}
		withdrawn := served
		for _, feed := range latest {
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
