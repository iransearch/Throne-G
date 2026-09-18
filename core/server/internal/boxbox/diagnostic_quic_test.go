//go:build with_quic

package boxbox

import (
	"ThroneCore/internal/netdiag"
	"context"
	"testing"

	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
)

var _ adapter.InterfaceUpdateListener = (*diagnosticHysteria2)(nil)

// Exercise the upstream context used by the real RPC path, without starting
// monitors or making a connection. This catches hooks installed only in Context.
func TestHysteriaDiagnosticsOnRPCBox(t *testing.T) {
	ctx := box.Context(context.Background(), include.InboundRegistry(), include.OutboundRegistry(), include.EndpointRegistry(), include.DNSTransportRegistry(), include.ServiceRegistry(), include.CertificateProviderRegistry())
	b, err := New(Options{Context: ctx, Options: option.Options{
		Outbounds: []option.Outbound{{Type: "hysteria2", Tag: "proxy", Options: &option.Hysteria2OutboundOptions{
			ServerOptions:               option.ServerOptions{Server: "127.0.0.1", ServerPort: 443},
			OutboundTLSOptionsContainer: option.OutboundTLSOptionsContainer{TLS: &option.OutboundTLSOptions{Enabled: true}},
		}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	o, ok := b.outbound.Outbound("proxy")
	if !ok {
		t.Fatal("missing outbound")
	}
	d, ok := o.(*diagnosticHysteria2)
	if !ok {
		t.Fatalf("RPC box bypassed diagnostic wrapper: %T", o)
	}
	if d.boxID == 0 || d.boxID != netdiag.Box(b.Context()) || d.tagID != netdiag.Hash("proxy") {
		t.Fatal("outbound cannot be correlated with its box and probe")
	}
	d.InterfaceUpdated(ctx)
}
