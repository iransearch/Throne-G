package main

import (
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
)

// A probe box has no tun to mark its sockets, so the running Tun's auto_redirect nftables rules would capture them.
func applyAutoRedirectMark(options *option.Options, mark uint32) {
	if mark == 0 {
		return
	}
	for _, inbound := range options.Inbounds {
		// sing-box refuses route.default_mark next to its own tun auto_redirect.
		if inbound.Type == C.TypeTun {
			return
		}
	}
	if options.Route == nil {
		options.Route = &option.RouteOptions{}
	}
	if options.Route.DefaultMark == 0 {
		options.Route.DefaultMark = option.FwMark(mark)
	}
}
