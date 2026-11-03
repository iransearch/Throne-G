package vpndns

import (
	"context"
	"net"
	"sync/atomic"
	"testing"

	dns "ThroneCore/internal/dnsengine"
	mdns "github.com/miekg/dns"
	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
)

func zeroTTLReply(query *mdns.Msg, address byte) *mdns.Msg {
	response := new(mdns.Msg).SetReply(query)
	response.Answer = []mdns.RR{
		&mdns.CNAME{Hdr: mdns.RR_Header{Name: query.Question[0].Name, Rrtype: mdns.TypeCNAME, Class: mdns.ClassINET, Ttl: 0}, Target: "alias.example."},
		&mdns.A{Hdr: mdns.RR_Header{Name: "alias.example.", Rrtype: mdns.TypeA, Class: mdns.ClassINET, Ttl: 60}, A: net.IPv4(192, 0, 2, address)},
	}
	return response
}

func TestZeroTTLCNAMERefreshesProxyLookup(t *testing.T) {
	tr := fixture(t)
	var network atomic.Int32
	tr.children = []adapter.DNSTransport{provider("google", "https", func(_ context.Context, query *mdns.Msg) (*mdns.Msg, error) {
		return zeroTTLReply(query, byte(network.Add(1))), nil
	})}
	for _, want := range []string{"192.0.2.1", "192.0.2.2"} {
		addresses, err := tr.DNSLookup(context.Background(), "race.invalid", adapter.DNSQueryOptions{Strategy: C.DomainStrategyIPv4Only})
		if err != nil || len(addresses) != 1 || addresses[0].String() != want {
			t.Fatalf("lookup = %v, %v; want fresh address %s", addresses, err, want)
		}
	}
}

func TestZeroTTLCNAMEPreservedForTUN(t *testing.T) {
	tr := fixture(t)
	tr.children = []adapter.DNSTransport{provider("google", "https", func(_ context.Context, query *mdns.Msg) (*mdns.Msg, error) {
		return zeroTTLReply(query, 1), nil
	})}
	client := dns.NewClient(dns.ClientOptions{Context: context.Background(), Logger: log.NewNOPFactory().Logger()})
	client.Start()
	response, err := client.Exchange(context.Background(), tr, question(), adapter.DNSQueryOptions{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if response.Answer[0].Header().Ttl != 0 {
		t.Fatalf("TUN zero-TTL CNAME raised to %d", response.Answer[0].Header().Ttl)
	}
}
