package xray

import (
	"ThroneCore/gen"
	"context"
	"errors"
	"net"
	"testing"
	"time"

	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/transport/internet"
	"github.com/xtls/xray-core/transport/internet/stat"
)

type hysteriaLifecycleDialer struct {
	internet.Dialer
	address string
}

func (d hysteriaLifecycleDialer) Dial(ctx context.Context, _ xnet.Destination) (stat.Connection, error) {
	return (&net.Dialer{}).DialContext(ctx, "udp", d.address)
}

func TestHysteriaCloseInterruptsBlackholedHandshake(t *testing.T) {
	// Receive QUIC packets but never answer: cleanup must not wait for the
	// remote handshake timeout, even after the originating stream is cancelled.
	blackhole, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer blackhole.Close()
	server := "127.0.0.1"
	port := uint32(blackhole.LocalAddr().(*net.UDPAddr).Port)
	password := "test-password"
	tlsJSON := `{"enabled":true,"server_name":"localhost","insecure":true}`
	h, err := newXrayHysteria2Outbound(context.Background(), &gen.XrayHysteria2Config{
		Server: &server, ServerPort: &port, Password: &password, TlsJson: &tlsJSON,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	defer cancelRequest()
	client, err := h.getClient(requestCtx, hysteriaLifecycleDialer{address: blackhole.LocalAddr().String()})
	if err != nil {
		t.Fatal(err)
	}
	cancelRequest() // A single stream must not own the shared client's lifetime.
	dialDone := make(chan error, 1)
	go func() {
		conn, err := client.TCP("example.com:443")
		if conn != nil {
			_ = conn.Close()
		}
		dialDone <- err
	}()
	_ = blackhole.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := blackhole.ReadFrom(make([]byte, 2048)); err != nil {
		t.Fatalf("shared client did not start its handshake: %v", err)
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- h.Close() }()
	select {
	case <-closeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Close waited for the remote handshake timeout")
	}
	select {
	case err := <-dialDone:
		if err == nil {
			t.Fatal("blackholed handshake unexpectedly succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("handshake goroutine survived outbound Close")
	}
	if _, err := h.getClient(context.Background(), nil); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("closed outbound allowed client recreation: %v", err)
	}
}

func TestHysteriaLifetimeCancellationInterruptsRead(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn := newXrayHysteria2LifetimeConn(ctx, left)
	defer conn.Close()
	done := make(chan error, 1)
	go func() {
		_, err := conn.Read(make([]byte, 1))
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled read unexpectedly succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("outbound cancellation did not close its transport")
	}
}
