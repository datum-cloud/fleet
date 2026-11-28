package multiplane

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"time"

	"go.datum.net/fleet/internal/site/redfishwork"
)

// ReportRelay forwards installed servers' report connections from the lab
// network to the central media service's PROXY listener, naming each
// server's own address in a PROXY header. The lab's path from the guests to
// the workstation rewrites their addresses; through the relay the media
// service still sees each report's source. It listens once the lab bridge
// has its address, retrying until then.
func ReportRelay(ctx context.Context, listen, upstream string) error {
	var listener net.Listener
	for {
		var err error
		if listener, err = (&net.ListenConfig{}).Listen(ctx, "tcp", listen); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(time.Second):
		}
	}
	go func() { <-ctx.Done(); _ = listener.Close() }()
	fmt.Println("report-relay: listening")
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go relay(conn, upstream)
	}
}

func relay(conn net.Conn, upstream string) {
	defer conn.Close()
	source, sourceErr := netip.ParseAddrPort(conn.RemoteAddr().String())
	destination, destinationErr := netip.ParseAddrPort(conn.LocalAddr().String())
	if sourceErr != nil || destinationErr != nil {
		return
	}
	out, err := net.DialTimeout("tcp", upstream, 10*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "report-relay: upstream unreachable: %v\n", err)
		return
	}
	defer out.Close()
	if _, err := io.WriteString(out, redfishwork.ProxyHeader(source, destination)); err != nil {
		return
	}
	fmt.Printf("report-relay: relaying a connection from %s\n", source.Addr())
	done := make(chan struct{}, 2)
	pipe := func(to, from net.Conn) {
		_, _ = io.Copy(to, from)
		if tcp, ok := to.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
		done <- struct{}{}
	}
	go pipe(out, conn)
	go pipe(conn, out)
	<-done
	<-done
}
