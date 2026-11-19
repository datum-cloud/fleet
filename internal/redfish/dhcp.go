package redfish

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
)

// withdrawalWindow bounds how long a worker waits, after the network boot
// closed, for every DHCP instance that served the reservation to report it
// withdrawn. Past it the outcome stays unknown and replay waits again.
const withdrawalWindow = 10 * time.Minute

const dhcpLog = "dhcp-applied.log"

// report is one retained applied report: when the boot service accepted it,
// which DHCP instance applied which exact feed.
type report struct {
	At       time.Time    `json:"at"`
	Instance string       `json:"instance"`
	Feed     bootnet.Feed `json:"feed"`
}

// reserved reports whether the operation's boot NIC should hold its
// provisioning address now: the same admission the boot handler serves,
// widened to the stages before power-on so DHCP is ready when firmware asks.
func (p *Provider) reserved(j journal, r *record, now time.Time) bool {
	return r.Target.Provisioning != (Provisioning{}) && !j.Closed && j.Image == p.image && r.Stage != "done" && !r.BootClosed && len(r.BootToken) == 64 && now.Before(r.BootExpires) &&
		r.Request.BindingID == j.Binding && r.Request.Epoch == j.Epoch && r.Request.FenceToken == j.Fence
}

// Reservations is the complete desired reservation set derived from the
// journal. It is empty when no network boot is admitted.
func (p *Provider) Reservations() (bootnet.Feed, error) {
	j, err := p.read()
	if err != nil {
		return bootnet.Feed{}, err
	}
	now := p.clock()
	rs := []bootnet.Reservation{}
	for _, r := range j.Operations {
		if p.reserved(j, r, now) {
			rs = append(rs, bootnet.Reservation{MAC: r.Target.MAC, Address: r.Target.Provisioning.Address, Gateway: r.Target.Provisioning.Gateway, Expires: r.BootExpires})
		}
	}
	return bootnet.NewFeed(rs)
}

// DHCPHandler serves the reservation feed and accepts applied reports from
// the DHCP owner's synchronizer, authenticated by a shared bearer token. It
// is never served on the firmware-facing boot listener.
func (p *Provider) DHCPHandler(token string) (http.Handler, error) {
	if len(token) < 32 || strings.ContainsAny(token, " \t\r\n") {
		return nil, errors.New("DHCP synchronizer token must be at least 32 characters without whitespace")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		got, ok := strings.CutPrefix(q.Header.Get("Authorization"), "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch {
		case q.Method == http.MethodGet && q.URL.Path == "/dhcp/v1/reservations" && q.URL.RawQuery == "":
			feed, err := p.Reservations()
			if err != nil {
				http.Error(w, "reservations unavailable", http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(feed)
		case q.Method == http.MethodPost && q.URL.Path == "/dhcp/v1/applied" && q.URL.RawQuery == "":
			var a bootnet.Applied
			d := json.NewDecoder(io.LimitReader(q.Body, 64<<10))
			d.DisallowUnknownFields()
			if d.Decode(&a) != nil || d.Decode(&struct{}{}) != io.EOF || a.Verify() != nil {
				http.Error(w, "invalid applied report", http.StatusBadRequest)
				return
			}
			if err := p.retainReport(a); err != nil {
				http.Error(w, "applied report not retained", http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}), nil
}

// ServeDHCP listens on :8081, separate from the boot listener.
func (p *Provider) ServeDHCP(token string) error {
	h, err := p.DHCPHandler(token)
	if err != nil {
		return err
	}
	s := &http.Server{Addr: ":8081", Handler: h, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second}
	return s.ListenAndServe()
}

// retainReport appends one report and syncs it before acknowledging.
func (p *Provider) retainReport(a bootnet.Applied) error {
	b, err := json.Marshal(report{At: p.clock(), Instance: a.Instance, Feed: a.Feed})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(p.bootRoot, 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(p.bootRoot, dhcpLog), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = syncDir(p.bootRoot)
	}
	return err
}

// reports reads every retained report in order. An unreadable line is
// corruption, never evidence in either direction.
func (p *Provider) reports() ([]report, error) {
	f, err := os.Open(filepath.Join(p.bootRoot, dhcpLog))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []report
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for s.Scan() {
		var r report
		if json.Unmarshal(s.Bytes(), &r) != nil || r.At.IsZero() || (bootnet.Applied{Instance: r.Instance, Feed: r.Feed}).Verify() != nil {
			return nil, errors.New("invalid retained DHCP report")
		}
		out = append(out, r)
	}
	return out, s.Err()
}

// awaitReservation waits, without any effect, until some DHCP instance reports
// serving the target's exact provisioning address for this operation.
func (p *Provider) awaitReservation(ctx context.Context, r *record) error {
	poll, _, _, _ := p.durations()
	for {
		reports, err := p.reports()
		if err != nil {
			return err
		}
		for _, a := range reports {
			if a.At.After(r.Result.CreatedAt) && a.Feed.Has(r.Target.MAC, r.Target.Provisioning.Address) {
				return nil
			}
		}
		if !p.clock().Before(r.BootExpires) {
			return errors.New("DHCP did not apply the boot reservation before boot admission expired")
		}
		if err := sleep(ctx, poll); err != nil {
			return err
		}
	}
}

// awaitWithdrawal requires that every DHCP instance which served the boot NIC
// during this operation now reports a feed without it. A silent instance
// keeps the outcome unknown: its reservation may still be served.
func (p *Provider) awaitWithdrawal(ctx context.Context, r *record) error {
	poll, _, _, _ := p.durations()
	deadline := p.clock().Add(withdrawalWindow)
	for {
		reports, err := p.reports()
		if err != nil {
			return err
		}
		served, latest := map[string]bool{}, map[string]bootnet.Feed{}
		for _, a := range reports {
			if !a.At.After(r.Result.CreatedAt) {
				continue
			}
			if a.Feed.Serves(r.Target.MAC) {
				served[a.Instance] = true
			}
			latest[a.Instance] = a.Feed
		}
		withdrawn := len(served) > 0
		for instance := range served {
			if latest[instance].Serves(r.Target.MAC) {
				withdrawn = false
			}
		}
		if withdrawn {
			return nil
		}
		if p.clock().After(deadline) {
			return errors.New("DHCP has not reported the boot reservation withdrawn")
		}
		if err := sleep(ctx, poll); err != nil {
			return err
		}
	}
}
