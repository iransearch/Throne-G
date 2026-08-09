package main

import (
	"context"
	"time"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	xnet "github.com/xtls/xray-core/common/net"
	xdns "github.com/xtls/xray-core/features/dns"
)

const xrayTestDNSLookupTimeout = 5 * time.Second

// singBoxXrayDNS lets throwaway Xray test instances resolve their outbound
// server domains through the throwaway sing-box DNS router. Live profiles use
// the loopback throne-dns bridge instead; tests do not expose that DNS inbound,
// so falling back to the OS resolver made Xray-backed URL/IP/speed tests fail
// even when the same profile connected successfully at runtime.
type singBoxXrayDNS struct {
	ctx    context.Context
	router adapter.DNSRouter
}

func newSingBoxXrayDNS(ctx context.Context, router adapter.DNSRouter) xdns.Client {
	if ctx == nil {
		ctx = context.Background()
	}
	return &singBoxXrayDNS{ctx: ctx, router: router}
}

func (*singBoxXrayDNS) Type() interface{} { return xdns.ClientType() }
func (*singBoxXrayDNS) Start() error      { return nil }
func (*singBoxXrayDNS) Close() error      { return nil }

func (r *singBoxXrayDNS) LookupIP(domain string, option xdns.IPOption) ([]xnet.IP, uint32, error) {
	strategy := C.DomainStrategyAsIS
	switch {
	case option.IPv4Enable && !option.IPv6Enable:
		strategy = C.DomainStrategyIPv4Only
	case !option.IPv4Enable && option.IPv6Enable:
		strategy = C.DomainStrategyIPv6Only
	}

	ctx, cancel := context.WithTimeout(r.ctx, xrayTestDNSLookupTimeout)
	defer cancel()
	addresses, err := r.router.Lookup(ctx, domain, adapter.DNSQueryOptions{
		Strategy: strategy,
	})
	if err != nil {
		return nil, 0, err
	}
	if len(addresses) == 0 {
		return nil, 0, xdns.ErrEmptyResponse
	}

	ips := make([]xnet.IP, 0, len(addresses))
	for _, address := range addresses {
		ips = append(ips, append(xnet.IP(nil), address.AsSlice()...))
	}
	return ips, uint32(C.DefaultDNSTTL), nil
}
