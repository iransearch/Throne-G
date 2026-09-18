//go:build with_quic

package boxbox

import (
	"ThroneCore/internal/netdiag"
	"context"
	"fmt"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/protocol/hysteria2"
)

// Embed the concrete outbound to preserve every existing method and interface.
// Overrides observe and then delegate exactly once; no retry, cancellation or reset is added.
type diagnosticHysteria2 struct {
	*hysteria2.Outbound
	boxID      uint64
	outboundID uint64
	tagID      string
}

func observeHysteriaRegistry(registry adapter.OutboundRegistry) {
	r, ok := registry.(*outbound.Registry)
	if !ok {
		return
	}
	outbound.Register[option.Hysteria2OutboundOptions](r, "hysteria2", func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.Hysteria2OutboundOptions) (adapter.Outbound, error) {
		original, err := hysteria2.NewOutbound(ctx, router, logger, tag, options)
		if err != nil {
			return original, err
		}
		h, ok := original.(*hysteria2.Outbound)
		if !ok {
			return original, nil
		}
		d := &diagnosticHysteria2{Outbound: h, boxID: netdiag.Box(ctx), outboundID: netdiag.ID(), tagID: netdiag.Hash(tag)}
		netdiag.Emit("hy2-created", d.boxID, d.fields())
		return d, nil
	})
}

func (d *diagnosticHysteria2) fields() string {
	return fmt.Sprintf("outbound=%d tag=%s", d.outboundID, d.tagID)
}
func (d *diagnosticHysteria2) InterfaceUpdated(ctx context.Context) {
	netdiag.Emit("hy2-reset", d.boxID, d.fields()+" reason="+netdiag.ResetSource()+" context="+netdiag.ContextState(ctx))
	d.Outbound.InterfaceUpdated(ctx)
}
func (d *diagnosticHysteria2) Close() error {
	netdiag.Emit("hy2-close", d.boxID, d.fields()+" reason=outbound-close")
	return d.Outbound.Close()
}
