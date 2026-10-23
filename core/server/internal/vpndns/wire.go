package vpndns

import (
	"context"
	"errors"

	dns "ThroneCore/internal/dnsengine"
	mdns "github.com/miekg/dns"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/dialer"
	"github.com/sagernet/sing-box/dns/transport"
	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// One context-bound connection per wire query: no TCP reuse probes and no
// implicit UDP-to-TCP retry outside Android's explicit fallback stage.
type singleWire struct {
	dns.TransportAdapter
	dialer  N.Dialer
	server  M.Socksaddr
	network string
}

func newTCP(ctx context.Context, tag string, options option.RemoteDNSServerOptions) (adapter.DNSTransport, error) {
	d, err := dns.NewRemoteDialer(ctx, options)
	if err != nil {
		return nil, err
	}
	server := options.DNSServerAddressOptions.Build()
	if server.Port == 0 {
		server.Port = 53
	}
	return &singleWire{TransportAdapter: dns.NewTransportAdapterWithRemoteOptions("tcp", tag, options), dialer: d, server: server, network: "tcp"}, nil
}

func newUDP(ctx context.Context, tag string, options option.RemoteDNSServerOptions) (adapter.DNSTransport, error) {
	v, err := newTCP(ctx, tag, options)
	if err != nil {
		return nil, err
	}
	t := v.(*singleWire)
	t.TransportAdapter = dns.NewTransportAdapterWithRemoteOptions("udp", tag, options)
	t.network = "udp"
	return t, nil
}
func (t *singleWire) Start(stage adapter.StartStage) error {
	if stage == adapter.StartStateStart {
		return dialer.InitializeDetour(t.dialer)
	}
	return nil
}
func (t *singleWire) Close() error { return nil }
func (t *singleWire) Reset()       {}
func (t *singleWire) Exchange(ctx context.Context, message *mdns.Msg) (*mdns.Msg, error) {
	conn, err := t.dialer.DialContext(ctx, t.network, t.server)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
	}
	if t.network == "udp" {
		data, err := message.Pack()
		if err != nil {
			return nil, err
		}
		if _, err = conn.Write(data); err != nil {
			return nil, err
		}
		data = make([]byte, 65535)
		n, err := conn.Read(data)
		if err != nil {
			return nil, err
		}
		response := new(mdns.Msg)
		if err = response.Unpack(data[:n]); err != nil {
			return nil, err
		}
		return response, nil // The race rejects truncated/invalid answers without TCP retry.
	}
	if err = transport.WriteMessage(conn, message.Id, message); err != nil {
		return nil, err
	}
	response, err := transport.ReadMessage(conn)
	if err == nil && response.Id != message.Id {
		return nil, errors.New("mismatched fallback DNS response ID")
	}
	return response, err
}
func (t *singleWire) ExchangeAsync(ctx context.Context, message *mdns.Msg, callback func(*mdns.Msg, error)) {
	go func() { response, err := t.Exchange(ctx, message); callback(response, err) }()
}
