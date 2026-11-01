package vpndns

import (
	"context"
	"net"
	"net/netip"
	"strconv"
	"sync/atomic"
	"testing"

	mdns "github.com/miekg/dns"
	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
)

// Diagnose the existing proxy Lookup path separately from raw TUN DNS.
// This is an observation of current policy, not a change of family preference.
func TestProxyLookupFirstPositiveFamilyAndRefreshCache(t *testing.T) {
	for _, first := range []uint16{mdns.TypeA, mdns.TypeAAAA} {
		for _, strategy := range []C.DomainStrategy{C.DomainStrategyAsIS, C.DomainStrategyPreferIPv4} {
			t.Run(mdns.TypeToString[first]+"/"+strconv.Itoa(int(strategy)), func(t *testing.T) {
				tr := fixture(t)
				var queries atomic.Int32
				ready := make(chan struct{})
				tr.children = []adapter.DNSTransport{provider("first", "https", func(ctx context.Context, q *mdns.Msg) (*mdns.Msg, error) {
					if queries.Add(1) == 2 {
						close(ready)
					}
					select {
					case <-ready:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
					if q.Question[0].Qtype != first {
						<-ctx.Done()
						return nil, ctx.Err()
					}
					r := new(mdns.Msg).SetReply(q)
					if first == mdns.TypeA {
						r.Answer = []mdns.RR{&mdns.A{Hdr: mdns.RR_Header{Name: q.Question[0].Name, Rrtype: first, Class: mdns.ClassINET, Ttl: 60}, A: net.IPv4(104, 26, 12, 47)}}
					} else {
						r.Answer = []mdns.RR{&mdns.AAAA{Hdr: mdns.RR_Header{Name: q.Question[0].Name, Rrtype: first, Class: mdns.ClassINET, Ttl: 60}, AAAA: net.ParseIP("2606:4700:20::681a:c2f")}}
					}
					return r, nil
				})}
				want := netip.MustParseAddr("104.26.12.47")
				if first == mdns.TypeAAAA {
					want = netip.MustParseAddr("2606:4700:20::681a:c2f")
				}
				for refresh := 0; refresh < 2; refresh++ {
					ips, err := tr.DNSLookup(context.Background(), "invapi.hostkey.com", adapter.DNSQueryOptions{Strategy: strategy})
					if err != nil || len(ips) != 1 || ips[0] != want {
						t.Fatalf("lookup %d: %v %v", refresh, ips, err)
					}
				}
				if queries.Load() != 2 {
					t.Fatalf("partial-family cached reply issued new DNS requests: %d", queries.Load())
				}
			})
		}
	}
}
