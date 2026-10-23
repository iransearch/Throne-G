package boxmain

import (
	"ThroneCore/internal/autoselector"
	"ThroneCore/internal/probedns"
	"ThroneCore/internal/vpndns"
	"context"
	"github.com/sagernet/sing-box/include"

	"ThroneCore/internal/boxbox"
)

func Check(content []byte) error {
	ctx := context.Background()
	registry := include.OutboundRegistry()
	autoselector.RegisterAutoSelector(registry)
	dnsRegistry := include.DNSTransportRegistry()
	probedns.Register(dnsRegistry)
	vpndns.Register(dnsRegistry)
	ctx = boxbox.Context(ctx, include.InboundRegistry(), registry, include.EndpointRegistry(), dnsRegistry, include.ServiceRegistry(), include.CertificateProviderRegistry())
	options, err := parseConfig(ctx, content)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	instance, err := boxbox.New(boxbox.Options{
		Context: ctx,
		Options: *options,
	})
	if err == nil {
		instance.Close()
	}
	cancel()
	return err
}
