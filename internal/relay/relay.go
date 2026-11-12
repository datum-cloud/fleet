// SPDX-License-Identifier: Apache-2.0

// Package relay pairs one outbound location connection with one client. After
// pairing it copies opaque bytes and cannot inspect the inner TLS/Redfish data.
package relay

import (
	"bufio"
	"crypto/subtle"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"
)

type Server struct {
	GatewayToken string
	mu           sync.Mutex
	routes       map[string]net.Conn
}

func NewServer(token string) *Server {
	return &Server{GatewayToken: token, routes: map[string]net.Conn{}}
}

func (s *Server) Serve(l net.Listener) error {
	for {
		c, err := l.Accept()
		if err != nil {
			return err
		}
		go s.handle(c)
	}
}

func (s *Server) handle(c net.Conn) {
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	br := bufio.NewReader(io.LimitReader(c, 4096))
	line, err := br.ReadString('\n')
	if err != nil {
		_ = c.Close()
		return
	}
	parts := strings.Fields(line)
	switch {
	case len(parts) == 3 && parts[0] == "GATEWAY":
		if subtle.ConstantTimeCompare([]byte(parts[2]), []byte(s.GatewayToken)) != 1 {
			reject(c, "gateway authentication failed")
			return
		}
		s.register(parts[1], c)
	case len(parts) == 2 && parts[0] == "CLIENT":
		s.connect(parts[1], c)
	default:
		reject(c, "invalid relay preface")
	}
}

func (s *Server) register(route string, c net.Conn) {
	s.mu.Lock()
	if old := s.routes[route]; old != nil {
		s.mu.Unlock()
		reject(c, "route already registered")
		return
	}
	s.routes[route] = c
	s.mu.Unlock()
	_ = c.SetDeadline(time.Time{})
	if _, err := io.WriteString(c, "WAIT\n"); err != nil {
		s.remove(route, c)
		_ = c.Close()
	}
}

func (s *Server) connect(route string, client net.Conn) {
	s.mu.Lock()
	gateway := s.routes[route]
	delete(s.routes, route) // the first connector consumes the route
	s.mu.Unlock()
	if gateway == nil {
		reject(client, "route unavailable or already consumed")
		return
	}
	_ = client.SetDeadline(time.Time{})
	if _, err := io.WriteString(client, "OK\n"); err != nil {
		_ = client.Close()
		_ = gateway.Close()
		return
	}
	if _, err := io.WriteString(gateway, "OK\n"); err != nil {
		_ = client.Close()
		_ = gateway.Close()
		return
	}
	proxy(client, gateway)
}

func (s *Server) remove(route string, c net.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.routes[route] == c {
		delete(s.routes, route)
	}
}

func reject(c net.Conn, reason string) { _, _ = fmt.Fprintf(c, "ERROR %s\n", reason); _ = c.Close() }

func proxy(a, b net.Conn) {
	var once sync.Once
	closeBoth := func() { _ = a.Close(); _ = b.Close() }
	go func() { _, _ = io.Copy(a, b); once.Do(closeBoth) }()
	go func() { _, _ = io.Copy(b, a); once.Do(closeBoth) }()
}

func Register(conn net.Conn, route, token string) error {
	if _, err := fmt.Fprintf(conn, "GATEWAY %s %s\n", route, token); err != nil {
		return err
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return err
	}
	if line != "WAIT\n" {
		return fmt.Errorf("relay rejected gateway: %s", strings.TrimSpace(line))
	}
	return nil
}

func Connect(conn net.Conn, route string) error {
	if _, err := fmt.Fprintf(conn, "CLIENT %s\n", route); err != nil {
		return err
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return err
	}
	if line != "OK\n" {
		return fmt.Errorf("relay refused route: %s", strings.TrimSpace(line))
	}
	return nil
}
