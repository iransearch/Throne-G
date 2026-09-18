//go:build !with_quic

package boxbox

import "github.com/sagernet/sing-box/adapter"

func observeHysteriaRegistry(adapter.OutboundRegistry) {}
