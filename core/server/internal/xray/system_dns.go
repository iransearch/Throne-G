package xray

import (
	"context"
	stdnet "net"
	"time"

	xnet "github.com/xtls/xray-core/common/net"
	xdns "github.com/xtls/xray-core/features/dns"
)

const fallbackOutboundDNSLookupTimeout = 5 * time.Second

// systemDNSClient is a last-resort resolver for standalone Xray instances used
// by Throne's test path. Live profiles replace it with Throne's sing-box-backed
// resolver before Start(), but opaque Xray full-config URL tests otherwise had
// no outbound-domain resolver after Hysteria2 legacy streamSettings were adapted
// to the custom throne-hysteria2 outbound.
type systemDNSClient struct{}

func (*systemDNSClient) Type() interface{} { return xdns.ClientType() }
func (*systemDNSClient) Start() error      { return nil }
func (*systemDNSClient) Close() error      { return nil }

func (*systemDNSClient) LookupIP(domain string, option xdns.IPOption) ([]xnet.IP, uint32, error) {
	network := "ip"
	switch {
	case option.IPv4Enable && !option.IPv6Enable:
		network = "ip4"
	case !option.IPv4Enable && option.IPv6Enable:
		network = "ip6"
	}

	ctx, cancel := context.WithTimeout(context.Background(), fallbackOutboundDNSLookupTimeout)
	defer cancel()
	addresses, err := stdnet.DefaultResolver.LookupIP(ctx, network, domain)
	if err != nil {
		return nil, 0, err
	}
	if len(addresses) == 0 {
		return nil, 0, xdns.ErrEmptyResponse
	}

	ips := make([]xnet.IP, 0, len(addresses))
	for _, address := range addresses {
		ips = append(ips, append(xnet.IP(nil), address...))
	}
	return ips, xdns.DefaultTTL, nil
}
