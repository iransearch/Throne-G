package hostkeydiag

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"time"

	"ThroneCore/internal/netdiag"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-tun"
	N "github.com/sagernet/sing/common/network"
)

type router struct{ adapter.Router }
type tracker struct{}

var _ adapter.Router = (*router)(nil)
var _ adapter.ConnectionTracker = tracker{}

func ObserveRouter(original adapter.Router) adapter.Router {
	original.AppendTracker(tracker{})
	return &router{original}
}

func routeContext(ctx context.Context, m adapter.InboundContext) (context.Context, *trace) {
	// Hijacked system DNS has its own per-query observations, not a shared stream trace.
	if m.Destination.Port == 53 {
		return ctx, nil
	}
	switch m.InboundType {
	case "mixed", "http", "socks", "tun":
	default:
		return ctx, nil
	}
	t := newTrace(ctx)
	if t == nil {
		return ctx, nil
	}
	t.enable(m.Destination.Fqdn)
	t.enable(m.Domain)
	return context.WithValue(ctx, traceKey{}, t), t
}

func (t *trace) finish(err error) {
	if t == nil {
		return
	}
	t.ended.Do(func() {
		t.emit("hostkey-end", fmt.Sprintf("elapsedMs=%d %s", time.Since(t.started).Milliseconds(), failure(err)))
	})
}

// Only a context value and an observational callback are added. Original
// streams, packets, errors, cancellation and callback delivery stay intact.
func (r *router) RouteConnectionEx(ctx context.Context, c net.Conn, m adapter.InboundContext, close N.CloseHandlerFunc) {
	ctx, t := routeContext(ctx, m)
	if t == nil {
		r.Router.RouteConnectionEx(ctx, c, m, close)
		return
	}
	r.Router.RouteConnectionEx(ctx, c, m, func(err error) {
		t.finish(err)
		if close != nil {
			close(err)
		}
	})
}
func (r *router) RoutePacketConnectionEx(ctx context.Context, c N.PacketConn, m adapter.InboundContext, close N.CloseHandlerFunc) {
	ctx, t := routeContext(ctx, m)
	if t == nil {
		r.Router.RoutePacketConnectionEx(ctx, c, m, close)
		return
	}
	r.Router.RoutePacketConnectionEx(ctx, c, m, func(err error) {
		t.finish(err)
		if close != nil {
			close(err)
		}
	})
}
func (r *router) RouteConnection(ctx context.Context, c net.Conn, m adapter.InboundContext) error {
	ctx, t := routeContext(ctx, m)
	err := r.Router.RouteConnection(ctx, c, m)
	t.finish(err)
	return err
}
func (r *router) RoutePacketConnection(ctx context.Context, c N.PacketConn, m adapter.InboundContext) error {
	ctx, t := routeContext(ctx, m)
	err := r.Router.RoutePacketConnection(ctx, c, m)
	t.finish(err)
	return err
}

func routed(ctx context.Context, m adapter.InboundContext, out adapter.Outbound) {
	t := from(ctx)
	if t == nil {
		t = newTrace(ctx)
	}
	if t == nil {
		return
	}
	t.enable(m.Destination.Fqdn)
	t.enable(m.Domain)
	if !t.enabled.Load() {
		return
	}
	tag := ""
	if out != nil {
		tag = out.Tag()
	}
	inbound := "other"
	switch m.InboundType {
	case "mixed", "socks", "http", "tun":
		inbound = m.InboundType
	}
	network := "other"
	switch m.Network {
	case "tcp", "udp", "icmp":
		network = m.Network
	}
	t.emit("hostkey-route", fmt.Sprintf("route=%s tag=%s inboundKind=%s network=%s family=%d count=%d", routeKind(tag), netdiag.Hash(tag), inbound, network, family(m.Destination.Addr), len(m.DestinationAddresses)))
	t.addresses("hostkey-route-address", 0, m.DestinationAddresses)
	if m.Destination.IsIP() {
		t.addresses("hostkey-route-address", 0, []netip.Addr{m.Destination.Addr})
	}
}

func (tracker) RoutedConnection(ctx context.Context, c net.Conn, m adapter.InboundContext, _ adapter.Rule, out adapter.Outbound) net.Conn {
	routed(ctx, m, out)
	return c
}
func (tracker) RoutedPacketConnection(ctx context.Context, c N.PacketConn, m adapter.InboundContext, _ adapter.Rule, out adapter.Outbound) N.PacketConn {
	routed(ctx, m, out)
	return c
}
func (tracker) RoutedFlow(ctx context.Context, m adapter.InboundContext, _ adapter.Rule, out adapter.Outbound) tun.FlowTracker {
	routed(ctx, m, out)
	return nil
}
