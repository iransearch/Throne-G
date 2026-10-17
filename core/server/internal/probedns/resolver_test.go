package probedns

import (
	"context"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"ThroneCore/internal/xraydns"
	mdns "github.com/miekg/dns"
	"github.com/sagernet/sing-box/adapter"
	D "github.com/sagernet/sing-box/dns"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/service"
	xdns "github.com/xtls/xray-core/features/dns"
)

type manager struct {
	adapter.DNSTransportManager
	transport adapter.DNSTransport
}

func (m *manager) Transport(tag string) (adapter.DNSTransport, bool) {
	return m.transport, tag == "dns-direct"
}
func (m *manager) Default() adapter.DNSTransport { return m.transport }

// Exercise the actual pinned resolver paths. Merely adding three DNS rules would
// not satisfy either path, because both explicitly pin the dns-direct transport.
func TestNativeAndXrayPinnedResolver(t *testing.T) {
	for winner := 0; winner < 3; winner++ {
		t.Run(strconv.Itoa(winner), func(t *testing.T) {
			opts := Options{}
			for i := 0; i < 3; i++ {
				i := i
				s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if i != winner {
						w.WriteHeader(503)
						return
					}
					raw, _ := io.ReadAll(r.Body)
					q := new(mdns.Msg)
					if e := q.Unpack(raw); e != nil {
						t.Error(e)
						return
					}
					response := reply(q)
					hdr := mdns.RR_Header{Name: q.Question[0].Name, Rrtype: q.Question[0].Qtype, Class: mdns.ClassINET, Ttl: 60}
					if q.Question[0].Qtype == mdns.TypeAAAA {
						response.Answer = []mdns.RR{&mdns.AAAA{Hdr: hdr, AAAA: net.ParseIP("2001:db8::7")}}
					} else {
						response.Answer = []mdns.RR{&mdns.A{Hdr: hdr, A: net.ParseIP("203.0.113.7")}}
					}
					data, _ := response.Pack()
					w.Header().Set("Content-Type", "application/dns-message")
					w.Write(data)
				}))
				t.Cleanup(s.Close)
				host, port, _ := net.SplitHostPort(s.Listener.Addr().String())
				p, _ := strconv.Atoi(port)
				o := option.RemoteHTTPSDNSServerOptions{Path: "/dns-query"}
				o.Server = host
				o.ServerPort = uint16(p)
				o.TLS = &option.OutboundTLSOptions{Enabled: true, ServerName: "example.com", Certificate: []string{string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.Certificate().Raw}))}}
				opts.Servers = append(opts.Servers, o)
			}
			ctx := service.ContextWithDefaultRegistry(context.Background())
			factory := log.NewNOPFactory()
			tr, e := New(ctx, factory.Logger(), "dns-direct", opts)
			if e != nil {
				t.Fatal(e)
			}
			defer tr.Close()
			ctx = service.ContextWith[adapter.DNSTransportManager](ctx, &manager{transport: tr})
			router, e := D.NewRouter(ctx, factory, option.DNSOptions{})
			if e != nil {
				t.Fatal(e)
			}
			defer router.Close()
			ctx = service.ContextWith[adapter.DNSRouter](ctx, router)
			native, e := adapter.DNSQueryOptionsFrom(ctx, &option.DomainResolveOptions{Server: "dns-direct"})
			if e != nil {
				t.Fatal(e)
			}
			addresses, e := router.Lookup(ctx, "probe.invalid", native)
			if e != nil || len(addresses) == 0 {
				t.Fatalf("native pin failed: %v %v", addresses, e)
			}
			resolver := xraydns.New(func() context.Context { return ctx })
			for _, family := range []xdns.IPOption{{IPv4Enable: true}, {IPv6Enable: true}, {IPv4Enable: true, IPv6Enable: true}} {
				addresses, _, e := resolver.LookupIP("another-probe.invalid", family)
				if e != nil || len(addresses) == 0 {
					t.Fatalf("Xray pin failed: %v %v", addresses, e)
				}
			}
		})
	}
}
