package redfishwork

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"
)

// maxProxyHeader is the longest PROXY protocol version 1 header.
const maxProxyHeader = 107

// ProxyListener accepts connections that a trusted relay or load balancer
// opens on behalf of installed servers, each starting with a PROXY protocol
// version 1 header that names the server's own address. A connection's
// remote address is the one the header names, so the media service checks a
// report's source even when the path to it does not keep source addresses.
// It drops any connection from outside Trusted, and any whose header is
// missing or malformed, before TLS starts.
type ProxyListener struct {
	net.Listener
	// Trusted are the relays' own addresses. Only they may name a source.
	Trusted []netip.Prefix
	// OnDrop, when set, is told why a connection was dropped.
	OnDrop func(error)
}

// Accept returns the next connection with a valid header from a trusted
// peer, dropping the others.
func (l ProxyListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		source, err := l.header(conn)
		if err == nil {
			return proxied{Conn: conn, source: source}, nil
		}
		_ = conn.Close()
		if l.OnDrop != nil {
			l.OnDrop(err)
		}
	}
}

// header checks the peer and reads exactly one header line, so the TLS
// handshake that follows reads nothing of it.
func (l ProxyListener) header(conn net.Conn) (net.Addr, error) {
	peer, err := netip.ParseAddrPort(conn.RemoteAddr().String())
	if err != nil || !slices.ContainsFunc(l.Trusted, func(prefix netip.Prefix) bool { return prefix.Contains(peer.Addr().Unmap()) }) {
		return nil, errors.New("a PROXY connection came from outside the trusted relays")
	}
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return nil, err
	}
	line := make([]byte, 0, maxProxyHeader)
	one := make([]byte, 1)
	for len(line) < maxProxyHeader && !strings.HasSuffix(string(line), "\r\n") {
		if _, err := conn.Read(one); err != nil {
			return nil, errors.New("a PROXY connection sent no complete header")
		}
		line = append(line, one[0])
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		return nil, err
	}
	return ParseProxyHeader(string(line))
}

// ParseProxyHeader reads a PROXY protocol version 1 header for TCP over IPv4
// or IPv6 and returns its source address and port.
func ParseProxyHeader(line string) (net.Addr, error) {
	fields := strings.Split(strings.TrimSuffix(line, "\r\n"), " ")
	if !strings.HasSuffix(line, "\r\n") || len(fields) != 6 || fields[0] != "PROXY" || (fields[1] != "TCP4" && fields[1] != "TCP6") {
		return nil, errors.New("a PROXY header is not TCP4 or TCP6 version 1")
	}
	source, sourceErr := netip.ParseAddr(fields[2])
	destination, destinationErr := netip.ParseAddr(fields[3])
	port, portErr := strconv.ParseUint(fields[4], 10, 16)
	_, destinationPortErr := strconv.ParseUint(fields[5], 10, 16)
	if sourceErr != nil || destinationErr != nil || portErr != nil || destinationPortErr != nil || source.Zone() != "" ||
		source.Is4() != (fields[1] == "TCP4") || destination.Is4() != (fields[1] == "TCP4") {
		return nil, errors.New("a PROXY header names no valid addresses and ports")
	}
	return net.TCPAddrFromAddrPort(netip.AddrPortFrom(source, uint16(port))), nil
}

// ProxyHeader is the PROXY protocol version 1 header a relay sends for a
// connection from source to destination.
func ProxyHeader(source, destination netip.AddrPort) string {
	family := "TCP6"
	if source.Addr().Unmap().Is4() && destination.Addr().Unmap().Is4() {
		family = "TCP4"
		source = netip.AddrPortFrom(source.Addr().Unmap(), source.Port())
		destination = netip.AddrPortFrom(destination.Addr().Unmap(), destination.Port())
	}
	return fmt.Sprintf("PROXY %s %s %s %d %d\r\n", family, source.Addr(), destination.Addr(), source.Port(), destination.Port())
}

// proxied is a relayed connection whose remote address is the one its
// header named.
type proxied struct {
	net.Conn
	source net.Addr
}

func (p proxied) RemoteAddr() net.Addr { return p.source }
