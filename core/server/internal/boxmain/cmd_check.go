package boxmain

import (
	"ThroneCore/internal/autoselector"
	"context"
	"github.com/sagernet/sing-box/include"

	"ThroneCore/internal/boxbox"
)

func Check(content []byte) error {
	ctx := context.Background()
	registry := include.OutboundRegistry()
	autoselector.RegisterAutoSelector(registry)
	ctx = boxbox.Context(ctx, include.InboundRegistry(), registry, include.EndpointRegistry(), include.DNSTransportRegistry(), include.ServiceRegistry(), include.CertificateProviderRegistry())
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
