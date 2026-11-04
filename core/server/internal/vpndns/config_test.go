package vpndns

import (
	"context"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	dns "ThroneCore/internal/dnsengine"
	mdns "github.com/miekg/dns"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/dns/transport"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/sagernet/sing/service"
)

type proxy struct {
	outbound.Adapter
	tcp atomic.Int32
	udp atomic.Int32
}

func (p *proxy) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	if network == "udp" {
		p.udp.Add(1)
	} else {
		p.tcp.Add(1)
	}
	return (&net.Dialer{}).DialContext(ctx, network, destination.String())
}
func (p *proxy) ListenPacket(ctx context.Context, _ M.Socksaddr) (net.PacketConn, error) {
	return (&net.ListenConfig{}).ListenPacket(ctx, "udp", "127.0.0.1:0")
}

type manager struct {
	adapter.OutboundManager
	proxy *proxy
}

func (m *manager) Outbound(tag string) (adapter.Outbound, bool) { return m.proxy, tag == "proxy" }
func (m *manager) Default() adapter.Outbound                    { return m.proxy }

func remote(address string) option.RemoteDNSServerOptions {
	host, port, _ := net.SplitHostPort(address)
	p, _ := strconv.Atoi(port)
	o := option.RemoteDNSServerOptions{}
	o.Server, o.ServerPort, o.Detour = host, uint16(p), "proxy"
	return o
}

// Exercise the actual strict schema, registered constructor, verified HTTPS,
// UDP, TCP fallback, and the production detour dialers against local endpoints.
func TestConfigurationAndRealVPNDetours(t *testing.T) {
	for _, count := range []int{4, 7} {
		for winner := 0; winner <= count; winner++ {
			t.Run(strconv.Itoa(count)+"/winner-"+strconv.Itoa(winner), func(t *testing.T) {
				opts := Options{}
				for i := 0; i < 3; i++ {
					i := i
					server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.Method != "POST" || r.URL.Path != "/dns-query" {
							t.Error("invalid DoH request")
						}
						raw, err := io.ReadAll(r.Body)
						if err != nil {
							return
						}
						q := new(mdns.Msg)
						if err = q.Unpack(raw); err != nil {
							t.Error(err)
							return
						}
						response := reply(q, i)
						if winner != i {
							response.Rcode = mdns.RcodeServerFailure
							response.Answer = nil
						}
						data, _ := response.Pack()
						w.Header().Set("Content-Type", "application/dns-message")
						w.Write(data)
					}))
					t.Cleanup(server.Close)
					o := option.RemoteHTTPSDNSServerOptions{Path: "/dns-query"}
					o.RemoteDNSServerOptions = remote(server.Listener.Addr().String())
					o.TLS = &option.OutboundTLSOptions{Enabled: true, ServerName: "example.com", Certificate: []string{string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))}}
					opts.Servers = append(opts.Servers, option.DNSServerOptions{Type: "https", Options: &o})
				}
				for i := 3; i < count; i++ {
					i := i
					packet, err := net.ListenPacket("udp", "127.0.0.1:0")
					if err != nil {
						t.Fatal(err)
					}
					udp := &mdns.Server{PacketConn: packet, Handler: mdns.HandlerFunc(func(w mdns.ResponseWriter, q *mdns.Msg) {
						r := reply(q, i)
						if winner != i {
							r.Rcode = mdns.RcodeServerFailure
							r.Answer = nil
						}
						w.WriteMsg(r)
					})}
					go udp.ActivateAndServe()
					t.Cleanup(func() { udp.Shutdown() })
					u := remote(packet.LocalAddr().String())
					opts.Servers = append(opts.Servers, option.DNSServerOptions{Type: "udp", Options: &u})
				}
				listener, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				var fallbackCalls atomic.Int32
				tcp := &mdns.Server{Listener: listener, Handler: mdns.HandlerFunc(func(w mdns.ResponseWriter, q *mdns.Msg) { fallbackCalls.Add(1); w.WriteMsg(reply(q, count)) })}
				go tcp.ActivateAndServe()
				t.Cleanup(func() { tcp.Shutdown() })
				f := remote(listener.Addr().String())
				opts.Fallback = &option.DNSServerOptions{Type: "tcp", Options: &f}

				p := &proxy{Adapter: outbound.NewAdapter("test-vpn", "proxy", []string{"tcp", "udp"}, nil)}
				ctx := service.ContextWith[adapter.OutboundManager](context.Background(), &manager{proxy: p})
				registry := dns.NewTransportRegistry()
				transport.RegisterHTTPS(registry)
				transport.RegisterUDP(registry)
				transport.RegisterTCP(registry)
				Register(registry)
				ctx = service.ContextWith[option.DNSTransportOptionsRegistry](ctx, registry)
				raw, err := json.MarshalContext(ctx, &option.DNSServerOptions{Type: Type, Tag: "dns-remote", Options: &opts})
				if err != nil {
					t.Fatal(err)
				}
				var parsed option.DNSServerOptions
				if err = json.UnmarshalContext(ctx, raw, &parsed); err != nil {
					t.Fatal(err)
				}
				tr, err := registry.CreateDNSTransport(ctx, log.NewNOPFactory().Logger(), parsed.Tag, parsed.Type, parsed.Options)
				if err != nil {
					t.Fatalf("construct: %v; wire=%s; options=%+v", err, raw, parsed.Options)
				}
				t.Cleanup(func() { tr.(*Transport).Close() })
				if err = tr.Start(adapter.StartStateStart, adapter.NewScope(context.Background(), log.NewNOPFactory().Logger())); err != nil {
					t.Fatal(err)
				}
				r, err := tr.Exchange(ctx, question())
				if err != nil || r == nil || !r.Answer[0].(*mdns.A).A.Equal(net.IPv4(192, 0, 2, byte(winner+1))) {
					t.Fatalf("winner %d: %v %v", winner, r, err)
				}
				if winner >= 3 && winner < count && p.udp.Load() == 0 || (winner < 3 || winner == count) && p.tcp.Load() == 0 {
					t.Fatal("winner bypassed VPN detour")
				}
				wantFallback := int32(0)
				if winner == count {
					wantFallback = 1
				}
				if fallbackCalls.Load() != wantFallback {
					t.Fatalf("fallback ran at the wrong time: got %d want %d", fallbackCalls.Load(), wantFallback)
				}
			})
		}
	}
}

// Exercise the registered constructor rather than a separate provider-count shim.
func TestPrimaryServerCountBounds(t *testing.T) {
	for _, count := range []int{0, 1, 2, 3, 4, 5, 6, 7, 8} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			options := Options{}
			for range count {
				endpoint := remote("127.0.0.1:53")
				options.Servers = append(options.Servers, option.DNSServerOptions{Type: "udp", Options: &endpoint})
			}
			p := &proxy{Adapter: outbound.NewAdapter("test-vpn", "proxy", []string{"tcp", "udp"}, nil)}
			ctx := service.ContextWith[adapter.OutboundManager](context.Background(), &manager{proxy: p})
			transport, err := New(ctx, log.NewNOPFactory().Logger(), "bounds", options)
			if count < 2 || count > 7 {
				if err == nil || !strings.Contains(err.Error(), "requires 2 to 7 primary servers") {
					t.Fatalf("unexpected bounds result: %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				transport.(*Transport).Close()
			}
		})
	}
}

func TestSevenProviderOptionsKeepTLSDetourAndTimeoutValidation(t *testing.T) {
	for _, kind := range []string{"valid", "insecure", "missing-sni", "wrong-detour", "invalid-timeout"} {
		t.Run(kind, func(t *testing.T) {
			options := Options{}
			for range 6 {
				endpoint := remote("127.0.0.1:53")
				options.Servers = append(options.Servers, option.DNSServerOptions{Type: "udp", Options: &endpoint})
			}
			doh := option.RemoteHTTPSDNSServerOptions{Path: "/dns-query"}
			doh.RemoteDNSServerOptions = remote("127.0.0.1:443")
			doh.TLS = &option.OutboundTLSOptions{Enabled: true, ServerName: "dns.example"}
			switch kind {
			case "insecure":
				doh.TLS.Insecure = true
			case "missing-sni":
				doh.TLS.ServerName = ""
			case "wrong-detour":
				doh.Detour = "direct"
			case "invalid-timeout":
				options.Timeout = -1
			}
			options.Servers = append(options.Servers, option.DNSServerOptions{Type: "https", Options: &doh})
			p := &proxy{Adapter: outbound.NewAdapter("test-vpn", "proxy", []string{"tcp", "udp"}, nil)}
			ctx := service.ContextWith[adapter.OutboundManager](context.Background(), &manager{proxy: p})
			transport, err := New(ctx, log.NewNOPFactory().Logger(), "validation", options)
			if kind == "valid" {
				if err != nil {
					t.Fatal(err)
				}
				transport.(*Transport).Close()
				return
			}
			if err == nil {
				transport.(*Transport).Close()
				t.Fatal("seven-provider configuration bypassed validation")
			}
		})
	}
}
