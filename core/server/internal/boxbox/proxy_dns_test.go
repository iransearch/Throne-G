package boxbox

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mdns "github.com/miekg/dns"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/inbound"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/protocol/mixed"
	sjson "github.com/sagernet/sing/common/json"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	xproxy "golang.org/x/net/proxy"
)

type proxyDNSDial struct {
	tag         string
	destination M.Socksaddr
}

type proxyDNSRoutes struct {
	mu      sync.Mutex
	closing bool
	active  sync.WaitGroup
}

type proxyDNSRouter struct {
	adapter.Router
	routes *proxyDNSRoutes
}

func (r *proxyDNSRouter) RouteConnectionEx(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	r.routes.mu.Lock()
	if r.routes.closing {
		r.routes.mu.Unlock()
		_ = conn.Close()
		return
	}
	r.routes.active.Add(1)
	r.routes.mu.Unlock()
	defer r.routes.active.Done()
	r.Router.RouteConnectionEx(ctx, conn, metadata, onClose)
}

func (r *proxyDNSRoutes) stop() {
	r.mu.Lock()
	r.closing = true
	r.mu.Unlock()
	r.active.Wait()
}

type proxyDNSOutbound struct {
	outbound.Adapter
	dnsPorts map[uint16]bool
	dials    chan<- proxyDNSDial
}

func (o *proxyDNSOutbound) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	if o.dnsPorts[destination.Port] {
		return (&net.Dialer{}).DialContext(ctx, network, destination.String())
	}
	select {
	case o.dials <- proxyDNSDial{o.Tag(), destination}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	local, remote := net.Pipe()
	go func() { defer remote.Close(); _, _ = io.Copy(remote, remote) }()
	return local, nil
}

func (o *proxyDNSOutbound) ListenPacket(ctx context.Context, _ M.Socksaddr) (net.PacketConn, error) {
	return (&net.ListenConfig{}).ListenPacket(ctx, "udp", "127.0.0.1:0")
}

// The fixture comes from the actual C# policy, not a hand-copied resolve rule.
// Run real mixed HTTP/SOCKS inbounds, DNS routing/race, AI selection and Shield.
func TestProxyDestinationDNSAndShield(t *testing.T) {
	dir := os.Getenv("IRSPEEDY_DNS_FIXTURES")
	if dir == "" {
		t.Skip("generate Windows ParallelDnsChecks fixtures first")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "proxy-behavior.json"))
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err = json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	var primaryQueries, directQueries atomic.Int32
	var perDomain sync.Map
	countRace := func(domain string) int32 {
		if value, ok := perDomain.Load(mdns.Fqdn(domain)); ok {
			return value.(*atomic.Int32).Load()
		}
		return 0
	}
	dnsPorts := make(map[uint16]bool)
	newDNS := func(primary bool, provider int) map[string]any {
		packet, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		_, port, _ := net.SplitHostPort(packet.LocalAddr().String())
		value, _ := strconv.Atoi(port)
		dnsPorts[uint16(value)] = true
		server := &mdns.Server{PacketConn: packet, Handler: mdns.HandlerFunc(func(w mdns.ResponseWriter, q *mdns.Msg) {
			if primary {
				primaryQueries.Add(1)
				counter, _ := perDomain.LoadOrStore(q.Question[0].Name, new(atomic.Int32))
				counter.(*atomic.Int32).Add(1)
			} else {
				directQueries.Add(1)
			}
			r := new(mdns.Msg).SetReply(q)
			name := q.Question[0].Name
			switch {
			case strings.HasPrefix(name, "nxdomain."):
				r.Rcode = mdns.RcodeNameError
			case strings.HasPrefix(name, "nodata."):
			case primary && provider != 2:
				r.Rcode = mdns.RcodeServerFailure
			case q.Question[0].Qtype == mdns.TypeA:
				last := byte(9)
				if !primary {
					last = 77
				}
				if name == "ip-blocked.example." {
					last = 90
				}
				if name == "set-ip-blocked.example." {
					last = 91
				}
				r.Answer = []mdns.RR{&mdns.A{Hdr: mdns.RR_Header{Name: name, Rrtype: mdns.TypeA, Class: mdns.ClassINET, Ttl: 60}, A: net.IPv4(192, 0, 2, last)}}
			}
			_ = w.WriteMsg(r)
		})}
		go func() { _ = server.ActivateAndServe() }()
		t.Cleanup(func() { _ = server.Shutdown() })
		options := map[string]any{"type": "udp", "server": "127.0.0.1", "server_port": value}
		if primary {
			options["detour"] = "proxy"
		}
		return options
	}
	var primaries []any
	for i := range 4 {
		primaries = append(primaries, newDNS(true, i))
	}
	dnsConfig := config["dns"].(map[string]any)
	// The fixture's sockets all stay on loopback; no OS route monitor is needed.
	config["route"].(map[string]any)["auto_detect_interface"] = false
	dnsConfig["servers"] = []any{
		map[string]any{"type": "vpn-dns-race", "tag": "dns-remote", "servers": primaries, "timeout": "500ms", "fallback_timeout": "100ms"},
		newDNS(false, 0),
	}
	dnsConfig["servers"].([]any)[1].(map[string]any)["tag"] = "dns-direct"
	portReservation, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	proxyAddress := portReservation.Addr().String()
	_, portString, _ := net.SplitHostPort(proxyAddress)
	port, _ := strconv.Atoi(portString)
	_ = portReservation.Close()
	config["inbounds"] = []any{map[string]any{"type": "mixed", "tag": "mixed-in", "listen": "127.0.0.1", "listen_port": port}}
	config["outbounds"] = []any{
		map[string]any{"type": "dns-capture", "tag": "proxy"}, map[string]any{"type": "dns-capture", "tag": "ai-proxy"},
		map[string]any{"type": "dns-capture", "tag": "split-direct"}, map[string]any{"type": "block", "tag": "block"},
		map[string]any{"type": "direct", "tag": "direct"},
	}
	config["log"] = map[string]any{"disabled": true}
	dials := make(chan proxyDNSDial, 32)
	registry := include.OutboundRegistry()
	outbound.Register[option.DirectOutboundOptions](registry, "dns-capture", func(_ context.Context, _ adapter.Router, _ log.ContextLogger, tag string, _ option.DirectOutboundOptions) (adapter.Outbound, error) {
		return &proxyDNSOutbound{Adapter: outbound.NewAdapter("dns-capture", tag, []string{"tcp", "udp"}, nil), dnsPorts: dnsPorts, dials: dials}, nil
	})
	inbounds := include.InboundRegistry()
	routes := new(proxyDNSRoutes)
	inbound.Register[option.HTTPMixedInboundOptions](inbounds, "mixed", func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.HTTPMixedInboundOptions) (adapter.Inbound, error) {
		return mixed.NewInbound(ctx, &proxyDNSRouter{Router: router, routes: routes}, logger, tag, options)
	})
	ctx := Context(context.Background(), inbounds, registry, include.EndpointRegistry(), include.DNSTransportRegistry(), include.ServiceRegistry(), include.CertificateProviderRegistry())
	raw, _ = json.Marshal(config)
	var options option.Options
	if err = sjson.UnmarshalContext(ctx, raw, &options); err != nil {
		t.Fatal(err)
	}
	box, err := New(Options{Context: ctx, Options: options})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = box.inbound.Close()
		routes.stop()
		_ = box.Close()
	})
	// Exercise real protocol/routing/DNS managers without starting privileged OS
	// interface monitors. Production Box.Start is unchanged by this test harness.
	if err = box.logFactory.Start(); err != nil {
		t.Fatal(err)
	}
	for _, stage := range adapter.ListStartStages {
		if err = adapter.Start(box.ctx, box.logger, stage, box.outbound, box.dnsTransport, box.dnsRouter,
			box.connection, box.router, box.endpoint, box.certificateProvider, box.inbound, box.service); err != nil {
			t.Fatal(err)
		}
	}
	socks, err := xproxy.SOCKS5("tcp", proxyAddress, nil, &net.Dialer{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	dial := func(protocol, domain string) (net.Conn, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if protocol == "socks" {
			return socks.(xproxy.ContextDialer).DialContext(ctx, "tcp", net.JoinHostPort(domain, "443"))
		}
		conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", proxyAddress)
		if err != nil {
			return nil, err
		}
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		_, err = fmt.Fprintf(conn, "CONNECT %s:443 HTTP/1.1\r\nHost: %s:443\r\n\r\n", domain, domain)
		if err == nil {
			var response *http.Response
			response, err = http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: "CONNECT"})
			if err == nil && response.StatusCode != 200 {
				err = fmt.Errorf("CONNECT status %d", response.StatusCode)
			}
		}
		if err != nil {
			_ = conn.Close()
			return nil, err
		}
		return conn, nil
	}
	for _, protocol := range []string{"socks", "http"} {
		for _, test := range []struct{ domain, tag, address string }{
			{"allowed.example", "proxy", "192.0.2.9"}, {"chat.ai.example", "ai-proxy", "192.0.2.9"},
			{"bootstrap.example.org", "proxy", "192.0.2.77"}, {"direct.example", "split-direct", ""},
		} {
			t.Run(protocol+"/"+test.domain, func(t *testing.T) {
				before := countRace(test.domain)
				conn, err := dial(protocol, test.domain)
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				select {
				case actual := <-dials:
					if actual.tag != test.tag {
						t.Fatalf("wrong route: %+v", actual)
					}
					if test.address != "" && (!actual.destination.IsIP() || actual.destination.Addr.String() != test.address) {
						t.Fatalf("domain escaped to remote resolver: %+v", actual)
					}
					if test.address == "" && actual.destination.Fqdn != test.domain {
						t.Fatalf("direct policy modified: %+v", actual)
					}
				case <-time.After(time.Second):
					t.Fatal("no VPN dial")
				}
				if (test.domain == "direct.example" || test.domain == "bootstrap.example.org") && countRace(test.domain) != before {
					t.Fatal("direct/bootstrap entered VPN DNS race")
				}
				_ = conn.SetDeadline(time.Now().Add(time.Second))
				payload := "original TLS/SNI payload remains opaque"
				if _, err = io.WriteString(conn, payload); err != nil {
					t.Fatal(err)
				}
				echo := make([]byte, len(payload))
				if _, err = io.ReadFull(conn, echo); err != nil || string(echo) != payload {
					t.Fatalf("payload changed: %q %v", echo, err)
				}
			})
		}
		for _, domain := range []string{"blocked.example", "shield-set.example", "dns-blocked.example", "ip-blocked.example", "set-ip-blocked.example", "nxdomain.example", "nodata.example"} {
			t.Run(protocol+"/"+domain, func(t *testing.T) {
				before := countRace(domain)
				conn, err := dial(protocol, domain)
				if err == nil {
					// These inbounds can acknowledge CONNECT before sniff/routing.
					// Actual data reaching the fake endpoint is the success criterion.
					_ = conn.SetDeadline(time.Now().Add(time.Second))
					_, err = io.WriteString(conn, "blocked payload")
					if err == nil {
						var one [1]byte
						_, err = conn.Read(one[:])
					}
					_ = conn.Close()
					if err == nil {
						t.Fatal("Shield/failed DNS allowed application data")
					}
				}
				select {
				case actual := <-dials:
					t.Fatalf("blocked target reached outbound: %+v", actual)
				default:
				}
				if (domain == "blocked.example" || domain == "shield-set.example" || domain == "dns-blocked.example") && countRace(domain) != before {
					t.Fatal("blocked domain unnecessarily queried upstream DNS")
				}
			})
		}
	}
	if primaryQueries.Load() == 0 || directQueries.Load() == 0 {
		t.Fatal("DNS routing was not exercised")
	}
}
