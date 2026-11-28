package redfishwork

import (
	"io"
	"net"
	"net/netip"
	"testing"
	"time"
)

func TestParseProxyHeader(t *testing.T) {
	for line, want := range map[string]string{
		"PROXY TCP4 192.0.2.70 192.0.2.1 40000 28443\r\n":     "192.0.2.70:40000",
		"PROXY TCP6 2001:db8::70 2001:db8::1 40000 28443\r\n": "[2001:db8::70]:40000",
	} {
		if got, err := ParseProxyHeader(line); err != nil || got.String() != want {
			t.Errorf("%q = %v, %v", line, got, err)
		}
	}
	for _, line := range []string{
		"PROXY TCP4 192.0.2.70 192.0.2.1 40000 28443\n",
		"PROXY UNKNOWN\r\n",
		"PROXY TCP4 2001:db8::70 192.0.2.1 40000 28443\r\n",
		"PROXY TCP4 192.0.2.70 192.0.2.1 70000 28443\r\n",
		"PROXY TCP4 192.0.2.70 192.0.2.1 40000\r\n",
		"GET / HTTP/1.1\r\n",
	} {
		if _, err := ParseProxyHeader(line); err == nil {
			t.Errorf("%q was accepted", line)
		}
	}
	header := ProxyHeader(netip.MustParseAddrPort("192.0.2.70:40000"), netip.MustParseAddrPort("192.0.2.1:28443"))
	if got, err := ParseProxyHeader(header); err != nil || got.String() != "192.0.2.70:40000" {
		t.Fatalf("a relay's header %q = %v, %v", header, got, err)
	}
}

// TestTheProxyListenerTrustsOnlyItsRelays takes the source a trusted relay
// names, leaves the rest of the stream to TLS, and drops a connection from
// any other peer or without a valid header.
func TestTheProxyListenerTrustsOnlyItsRelays(t *testing.T) {
	listen := func(trusted string) (*ProxyListener, chan error) {
		inner, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { inner.Close() })
		dropped := make(chan error, 4)
		return NewProxyListener(inner, []netip.Prefix{netip.MustParsePrefix(trusted)}, func(err error) { dropped <- err }), dropped
	}
	send := func(listener *ProxyListener, data string) {
		conn, err := net.Dial("tcp", listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.Close() })
		if _, err := io.WriteString(conn, data); err != nil {
			t.Fatal(err)
		}
	}

	relayed, _ := listen("127.0.0.0/8")
	// A trusted peer that sends nothing does not hold up the next one.
	send(relayed, "")
	send(relayed, "PROXY TCP4 192.0.2.70 192.0.2.1 40000 28443\r\nhello")
	conn, err := relayed.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	rest := make([]byte, 5)
	if _, err := io.ReadFull(conn, rest); err != nil || conn.RemoteAddr().String() != "192.0.2.70:40000" || string(rest) != "hello" {
		t.Fatalf("relayed connection from %v read %q, %v", conn.RemoteAddr(), rest, err)
	}

	for name, c := range map[string]struct{ trusted, data string }{
		"an untrusted peer": {"198.51.100.0/24", "PROXY TCP4 192.0.2.70 192.0.2.1 40000 28443\r\n"},
		"no header":         {"127.0.0.0/8", "GET / HTTP/1.1\r\n"},
	} {
		listener, dropped := listen(c.trusted)
		send(listener, c.data)
		select {
		case <-dropped:
		case <-time.After(10 * time.Second):
			t.Fatalf("%s was not dropped", name)
		}
	}
}
