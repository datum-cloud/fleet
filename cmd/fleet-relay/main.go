// SPDX-License-Identifier: Apache-2.0

package main

import (
	"flag"
	"log/slog"
	"net"
	"os"
	"strings"

	"go.datum.net/fleet/internal/relay"
)

func main() {
	addr := flag.String("listen", ":8443", "relay TCP listen address")
	tokenFile := flag.String("gateway-token-file", "/var/run/secrets/fleet/relay-token", "gateway registration token file")
	flag.Parse()
	token, err := os.ReadFile(*tokenFile)
	if err != nil {
		slog.Error("read gateway token", "error", err)
		os.Exit(1)
	}
	l, err := net.Listen("tcp", *addr)
	if err != nil {
		slog.Error("listen", "error", err)
		os.Exit(1)
	}
	slog.Info("relay listening", "address", l.Addr().String())
	if err := relay.NewServer(strings.TrimSpace(string(token))).Serve(l); err != nil {
		slog.Error("serve relay", "error", err)
		os.Exit(1)
	}
}
