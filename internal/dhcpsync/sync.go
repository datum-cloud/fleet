// Package dhcpsync applies a site's boot-reservation feed to a Kea DHCPv4
// server and reports each applied revision back to the site. It runs beside
// one Kea instance, owned with DHCP by the network owner; Fleet only decides
// which reservations exist. A report is sent only after Kea accepted the
// configuration that serves exactly the reported feed.
package dhcpsync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"time"

	"go.datum.net/fleet/internal/bootnet"
)

// Syncer is one synchronizer for one Kea instance.
type Syncer struct {
	Feed     string // the site's https DHCP feed origin
	Token    string
	Instance string
	Socket   string // Kea's unix control socket
	Base     string // Kea configuration without Fleet reservations
	HTTP     *http.Client

	applied, reported string
	pid               float64
}

// Cycle fetches the feed, applies it when it changed or Kea restarted, and
// reports a newly applied revision. Any failure leaves the last report as the
// site's evidence; nothing is reported that Kea did not accept.
func (s *Syncer) Cycle(ctx context.Context) error {
	feed, err := s.fetch(ctx)
	if err != nil {
		return err
	}
	status, err := s.command("status-get", nil)
	if err != nil {
		return err
	}
	pid, _ := status["pid"].(float64)
	if pid == 0 {
		return errors.New("kea status has no pid")
	}
	if feed.Revision != s.applied || pid != s.pid {
		cfg, err := s.config(feed)
		if err != nil {
			return err
		}
		if _, err := s.command("config-set", cfg); err != nil {
			return err
		}
		s.applied, s.pid = feed.Revision, pid
	}
	if s.reported == s.applied {
		return nil
	}
	if err := s.report(ctx, feed); err != nil {
		return err
	}
	s.reported = feed.Revision
	return nil
}

func (s *Syncer) request(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	q, err := http.NewRequestWithContext(ctx, method, s.Feed+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	q.Header.Set("Authorization", "Bearer "+s.Token)
	// The site serves its feed only to an instance it expects.
	q.Header.Set(bootnet.InstanceHeader, s.Instance)
	if body != nil {
		q.Header.Set("Content-Type", "application/json")
	}
	return s.HTTP.Do(q)
}

func (s *Syncer) fetch(ctx context.Context) (bootnet.Feed, error) {
	resp, err := s.request(ctx, http.MethodGet, "/dhcp/v1/reservations", nil)
	if err != nil {
		return bootnet.Feed{}, err
	}
	defer resp.Body.Close()
	var feed bootnet.Feed
	d := json.NewDecoder(io.LimitReader(resp.Body, 1<<20))
	d.DisallowUnknownFields()
	if resp.StatusCode != http.StatusOK || d.Decode(&feed) != nil || feed.Verify() != nil {
		return bootnet.Feed{}, fmt.Errorf("invalid reservation feed (HTTP %d)", resp.StatusCode)
	}
	return feed, nil
}

// report echoes the applied feed exactly as the site served it, issue time
// included: the site proves withdrawal from when each reported feed was
// issued.
func (s *Syncer) report(ctx context.Context, feed bootnet.Feed) error {
	b, _ := json.Marshal(bootnet.Applied{Instance: s.Instance, Feed: feed})
	resp, err := s.request(ctx, http.MethodPost, "/dhcp/v1/applied", b)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("applied report rejected (HTTP %d)", resp.StatusCode)
	}
	return nil
}

// config adds each reservation to the one base subnet that contains its
// address. The base subnet supplies the prefix, gateway and boot options. A
// reservation with no such subnet, or with two, is an error: it is never
// dropped silently.
func (s *Syncer) config(feed bootnet.Feed) (map[string]any, error) {
	b, err := os.ReadFile(s.Base)
	if err != nil {
		return nil, err
	}
	var cfg map[string]any
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("base Kea configuration: %w", err)
	}
	dhcp, _ := cfg["Dhcp4"].(map[string]any)
	subnets, _ := dhcp["subnet4"].([]any)
	if subnets == nil {
		return nil, errors.New("base Kea configuration has no Dhcp4 subnet4 list")
	}
	for _, r := range feed.Reservations {
		address := netip.MustParseAddr(r.Address)
		if !address.Is4() {
			return nil, fmt.Errorf("reservation %s is not IPv4", r.MAC)
		}
		var subnet map[string]any
		for _, v := range subnets {
			m, _ := v.(map[string]any)
			text, _ := m["subnet"].(string)
			sp, err := netip.ParsePrefix(text)
			if err != nil || !sp.Contains(address) {
				continue
			}
			if subnet != nil {
				return nil, fmt.Errorf("two base subnets contain reservation %s", r.MAC)
			}
			subnet = m
		}
		if subnet == nil {
			return nil, fmt.Errorf("no base subnet contains reservation %s", r.MAC)
		}
		hosts, _ := subnet["reservations"].([]any)
		subnet["reservations"] = append(hosts, map[string]any{"hw-address": r.MAC, "ip-address": address.String()})
	}
	return cfg, nil
}

// command sends one Kea control command and requires result 0.
func (s *Syncer) command(name string, arguments map[string]any) (map[string]any, error) {
	c, err := net.DialTimeout("unix", s.Socket, 5*time.Second)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(30 * time.Second))
	q := map[string]any{"command": name}
	if arguments != nil {
		q["arguments"] = arguments
	}
	if err := json.NewEncoder(c).Encode(q); err != nil {
		return nil, err
	}
	var resp struct {
		Result    int            `json:"result"`
		Text      string         `json:"text"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.NewDecoder(io.LimitReader(c, 4<<20)).Decode(&resp); err != nil {
		return nil, fmt.Errorf("kea %s: %w", name, err)
	}
	if resp.Result != 0 {
		return nil, fmt.Errorf("kea %s failed: %s", name, resp.Text)
	}
	return resp.Arguments, nil
}

// Run cycles until the context ends, reporting each failed cycle.
func (s *Syncer) Run(ctx context.Context, interval time.Duration, failed func(error)) error {
	for {
		if err := s.Cycle(ctx); err != nil {
			failed(err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}
