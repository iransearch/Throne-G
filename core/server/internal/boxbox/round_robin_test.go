package boxbox

import (
	"ThroneCore/internal/autoselector"
	"context"
	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/protocol/group"
	"testing"
)

func TestRoundRobinOnRPCBoxAndAIUnchanged(t *testing.T) {
	ctx := box.Context(context.Background(), include.InboundRegistry(), include.OutboundRegistry(), include.EndpointRegistry(), include.DNSTransportRegistry(), include.ServiceRegistry(), include.CertificateProviderRegistry())
	b, err := New(Options{Context: ctx, Options: option.Options{Outbounds: []option.Outbound{
		{Type: "socks", Tag: "member", Options: &option.SOCKSOutboundOptions{ServerOptions: option.ServerOptions{Server: "127.0.0.1", ServerPort: 31001}}},
		{Type: autoselector.TypeAutoSelectorRoundRobin, Tag: "proxy", Options: &option.AutoSelectorOutboundOptions{Outbounds: []string{"member"}, Balance: true, BalanceMode: autoselector.BalanceModeRoundRobin}},
		{Type: "auto-selector", Tag: "ai-proxy", Options: &option.AutoSelectorOutboundOptions{Outbounds: []string{"member"}, Balance: true, BalanceMode: "connection"}},
	}, Route: &option.RouteOptions{Final: "proxy"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	main, _ := b.outbound.Outbound("proxy")
	if _, ok := main.(*autoselector.AutoSelector); !ok {
		t.Fatalf("main constructor: %T", main)
	}
	if _, ok := main.(group.AutoSelectorGroup); !ok {
		t.Fatal("extension cannot be queried by existing RPC")
	}
	ai, _ := b.outbound.Outbound("ai-proxy")
	if _, ok := ai.(*group.AutoSelector); !ok {
		t.Fatalf("AI no longer uses upstream: %T", ai)
	}
}
