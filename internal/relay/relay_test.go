package relay

import (
	"bufio"
	"net"
	"strings"
	"testing"
)

func TestFirstConnectorConsumesRoute(t *testing.T) {
	s := NewServer("separate-gateway-token")
	g, relayGateway := net.Pipe()
	go s.handle(relayGateway)
	if err := Register(g, "route-a", "separate-gateway-token"); err != nil {
		t.Fatal(err)
	}
	c1, relayClient1 := net.Pipe()
	go s.handle(relayClient1)
	if err := Connect(c1, "route-a"); err != nil {
		t.Fatal(err)
	}
	if line, _ := bufio.NewReader(g).ReadString('\n'); line != "OK\n" {
		t.Fatalf("gateway got %q", line)
	}
	c2, relayClient2 := net.Pipe()
	go s.handle(relayClient2)
	if err := Connect(c2, "route-a"); err == nil || !strings.Contains(err.Error(), "consumed") {
		t.Fatalf("second connector: %v", err)
	}
}
