package xray

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"sync"
	"syscall"
	"testing"
	"time"

	hyobfs "github.com/apernet/hysteria/extras/v2/obfs"
	quic "github.com/apernet/quic-go"
)

// No network traffic: record initial datagrams and wait for transport shutdown.
type geckoTestConn struct {
	done  chan struct{}
	wrote chan struct{}
	once  sync.Once
}

func newGeckoTestConn() *geckoTestConn {
	return &geckoTestConn{done: make(chan struct{}), wrote: make(chan struct{}, 64)}
}
func (c *geckoTestConn) ReadFrom([]byte) (int, net.Addr, error) {
	<-c.done
	return 0, nil, net.ErrClosed
}
func (c *geckoTestConn) WriteTo(p []byte, _ net.Addr) (int, error) {
	select {
	case c.wrote <- struct{}{}:
	default:
	}
	return len(p), nil
}
func (c *geckoTestConn) Close() error { c.once.Do(func() { close(c.done) }); return nil }
func (*geckoTestConn) LocalAddr() net.Addr {
	return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 19001}
}
func (*geckoTestConn) SetDeadline(time.Time) error      { return nil }
func (*geckoTestConn) SetReadDeadline(time.Time) error  { return nil }
func (*geckoTestConn) SetWriteDeadline(time.Time) error { return nil }

func TestXrayGeckoQUICHandshakeStarts(t *testing.T) {
	for _, fixed := range []bool{false, true} {
		t.Run(map[bool]string{false: "upstream-reproduces-failure", true: "xray-wrapper-sends-handshake"}[fixed], func(t *testing.T) {
			conn := newGeckoTestConn()
			options := hyobfs.GeckoOptions{Password: []byte("test-only-password")}
			var packet net.PacketConn
			var err error
			if fixed {
				packet, err = wrapXrayGecko(conn, options)
			} else {
				packet, err = hyobfs.WrapPacketConnGecko(conn, options)
			}
			if err != nil {
				t.Fatal(err)
			}
			defer packet.Close()
			if _, exposed := packet.(interface {
				SyscallConn() (syscall.RawConn, error)
			}); exposed == fixed {
				t.Fatal("incorrect socket capability exposure")
			}
			transport := &quic.Transport{Conn: packet}
			// Close the fake socket before Transport.Close unblocks its reader.
			defer func() { packet.Close(); transport.Close() }()
			ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
			defer cancel()
			_, err = transport.Dial(ctx, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 19002}, &tls.Config{ServerName: "test.invalid", NextProtos: []string{"h3"}}, nil)
			if !fixed {
				if !errors.Is(err, errors.ErrUnsupported) || len(conn.wrote) != 0 {
					t.Fatalf("expected pre-handshake unsupported failure, got %v", err)
				}
			} else {
				if !errors.Is(err, context.DeadlineExceeded) || len(conn.wrote) == 0 {
					t.Fatalf("expected handshake then deadline without server, got %v", err)
				}
			}
		})
	}
}
