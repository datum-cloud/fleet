// fleet-dhcp-sync runs beside one Kea DHCPv4 instance. It applies a site's
// boot-reservation feed through Kea's control socket and reports each applied
// revision back to the site, which retains it as boot and revocation evidence.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"go.datum.net/fleet/internal/dhcpsync"
)

func main() {
	var feed, tokenPath, caPath, instance, socket, base string
	var interval, refresh time.Duration
	flag.StringVar(&feed, "feed", "", "site DHCP feed origin, https://host[:port]")
	flag.StringVar(&tokenPath, "token-file", "", "file holding the synchronizer bearer token")
	flag.StringVar(&caPath, "ca-file", "", "PEM CA bundle the feed's certificate must chain to; default the system roots")
	flag.StringVar(&instance, "instance", os.Getenv("POD_NAME"), "stable name of this Kea instance")
	flag.StringVar(&socket, "kea-socket", "/var/run/kea/kea4-ctrl-socket", "Kea DHCPv4 unix control socket; Kea 2.6 allows only /var/run/kea")
	flag.StringVar(&base, "base-config", "/etc/kea/kea-dhcp4.conf", "Kea configuration without Fleet reservations")
	flag.DurationVar(&interval, "interval", 5*time.Second, "feed poll interval")
	flag.DurationVar(&refresh, "refresh", dhcpsync.DefaultRefresh, "least time between two reports of an unchanged applied feed")
	flag.Parse()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger, feed, tokenPath, caPath, instance, socket, base, interval, refresh); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("dhcp sync stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger, feed, tokenPath, caPath, instance, socket, base string, interval, refresh time.Duration) error {
	u, err := url.Parse(feed)
	if err != nil || u.Scheme != "https" || u.Host == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.User != nil {
		return errors.New("feed must be an https origin")
	}
	token, err := os.ReadFile(tokenPath)
	if err != nil {
		return fmt.Errorf("token: %w", err)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	if caPath != "" {
		pem, err := os.ReadFile(caPath)
		if err != nil {
			return fmt.Errorf("CA bundle: %w", err)
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(pem) {
			return errors.New("CA bundle holds no certificate")
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	}
	s := &dhcpsync.Syncer{
		Feed: u.Scheme + "://" + u.Host, Token: strings.TrimSpace(string(token)), Instance: instance, Socket: socket, Base: base, Refresh: refresh,
		HTTP: &http.Client{Timeout: 15 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	return s.Run(ctx, interval, func(err error) { logger.Warn("sync cycle failed", "error", err) })
}
