package xray

import (
	"ThroneCore/gen"
	"context"
	"errors"
	"github.com/xtls/xray-core/features/outbound"
	"github.com/xtls/xray-core/proxy"
	"net"
	"testing"
	"time"
)

func TestHysteriaNetworkResetInterruptsHandshakeAndRecreatesClient(t *testing.T) {
	blackhole, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer blackhole.Close()
	server := "127.0.0.1"
	port := uint32(blackhole.LocalAddr().(*net.UDPAddr).Port)
	password := "test"
	tlsJSON := `{"enabled":true,"server_name":"localhost","insecure":true}`
	h, err := newXrayHysteria2Outbound(context.Background(), &gen.XrayHysteria2Config{Server: &server, ServerPort: &port, Password: &password, TlsJson: &tlsJSON})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	dialer := hysteriaLifecycleDialer{address: blackhole.LocalAddr().String()}
	old, err := h.getClient(context.Background(), dialer)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		conn, err := old.TCP("example.com:443")
		if conn != nil {
			conn.Close()
		}
		done <- err
	}()
	blackhole.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := blackhole.ReadFrom(make([]byte, 2048)); err != nil {
		t.Fatal(err)
	}
	reset := make(chan bool, 1)
	go func() { reset <- h.resetClient() }()
	select {
	case ok := <-reset:
		if !ok {
			t.Fatal("live client not reset")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reset blocked on remote handshake")
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("blackholed handshake succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("stale handshake survived reset")
	}
	fresh, err := h.getClient(context.Background(), dialer)
	if err != nil || fresh == old {
		t.Fatalf("new request retained stale client: %v", err)
	}
	h.Close()
	if h.resetClient() {
		t.Fatal("reset reopened a closed outbound")
	}
	if _, err := h.getClient(context.Background(), dialer); !errors.Is(err, net.ErrClosed) {
		t.Fatal("closed outbound recreated a client")
	}
}

func TestNetworkResetTargetsOnlyTheNamedXrayMember(t *testing.T) {
	instance, err := CreateXrayInstance(`{"outbounds":[
 {"tag":"smart-proxy-0","protocol":"hysteria2","settings":{"server":"localhost","server_port":443,"password":"test","tls":{"enabled":true,"server_name":"localhost","insecure":true}}},
 {"tag":"smart-proxy-1","protocol":"hysteria2","settings":{"server":"localhost","server_port":443,"password":"test","tls":{"enabled":true,"server_name":"localhost","insecure":true}}}
 ]}`)
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	manager := instance.GetFeature(outbound.ManagerType()).(outbound.Manager)
	get := func(tag string) *xrayHysteria2Outbound {
		return manager.GetHandler(tag).(interface{ GetOutbound() proxy.Outbound }).GetOutbound().(*xrayHysteria2Outbound)
	}
	a, b := get("smart-proxy-0"), get("smart-proxy-1")
	first, err := a.getClient(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := b.getClient(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if ResetHysteria2Member(instance, "missing") || !ResetHysteria2Member(instance, "smart-proxy-1") {
		t.Fatal("incorrect member reset result")
	}
	unchanged, _ := a.getClient(context.Background(), nil)
	fresh, _ := b.getClient(context.Background(), nil)
	if unchanged != first || fresh == second {
		t.Fatal("targeted reset replaced the healthy peer or retained the failed client")
	}
}
