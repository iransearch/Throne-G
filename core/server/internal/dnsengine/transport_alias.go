package dns

import (
	upstream "github.com/sagernet/sing-box/dns"
)

// Share the pinned transport registry/manager and native transports. Only the
// client/router need local hooks for Android's provider-owned IP cache policy.
type TransportRegistry = upstream.TransportRegistry
type TransportManager = upstream.TransportManager
type TransportAdapter = upstream.TransportAdapter

var (
	NewTransportRegistry                 = upstream.NewTransportRegistry
	NewTransportManager                  = upstream.NewTransportManager
	NewTransportAdapter                  = upstream.NewTransportAdapter
	NewTransportAdapterWithRemoteOptions = upstream.NewTransportAdapterWithRemoteOptions
	NewTransportAdapterWithLocalOptions  = upstream.NewTransportAdapterWithLocalOptions
	NewRemoteDialer                      = upstream.NewRemoteDialer
)

func RegisterTransport[Options any](registry *TransportRegistry, transportType string, constructor upstream.TransportConstructorFunc[Options]) {
	upstream.RegisterTransport(registry, transportType, constructor)
}
