package probedns

import (
	"context"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	mdns "github.com/miekg/dns"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/dns"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
)

type fake struct {
	dns.TransportAdapter
	exchange func(context.Context, *mdns.Msg) (*mdns.Msg, error)
}

func (f *fake) Start(adapter.StartStage) error { return nil }
func (f *fake) Close() error                   { return nil }
func (f *fake) Reset()                         {}
func (f *fake) Exchange(ctx context.Context, m *mdns.Msg) (*mdns.Msg, error) {
	return f.exchange(ctx, m)
}
func (f *fake) ExchangeAsync(ctx context.Context, m *mdns.Msg, cb func(*mdns.Msg, error)) {
	cb(f.Exchange(ctx, m))
}
func question() *mdns.Msg         { m := new(mdns.Msg); m.SetQuestion("probe.invalid.", mdns.TypeA); return m }
func reply(m *mdns.Msg) *mdns.Msg { return new(mdns.Msg).SetReply(m) }

func TestEachWinnerNeverWaitsForLosers(t *testing.T) {
	for winner := 0; winner < 3; winner++ {
		for _, async := range []bool{false, true} {
			release := make(chan struct{})
			canceled := make(chan struct{}, 2)
			tr := &Transport{}
			for i := 0; i < 3; i++ {
				i := i
				tr.children = append(tr.children, &fake{exchange: func(ctx context.Context, m *mdns.Msg) (*mdns.Msg, error) {
					if i == winner {
						time.Sleep(10 * time.Millisecond)
						return reply(m), nil
					}
					<-ctx.Done()
					canceled <- struct{}{}
					<-release
					return nil, ctx.Err()
				}})
			}
			done := make(chan error, 1)
			m := question()
			if async {
				tr.ExchangeAsync(context.Background(), m, func(r *mdns.Msg, e error) { done <- e })
			} else {
				go func() { _, e := tr.Exchange(context.Background(), m); done <- e }()
			}
			select {
			case e := <-done:
				if e != nil {
					t.Fatal(e)
				}
			case <-time.After(time.Second):
				close(release)
				t.Fatal("winner waited for loser")
			}
			for i := 0; i < 2; i++ {
				select {
				case <-canceled:
				case <-time.After(time.Second):
					t.Fatal("loser not canceled")
				}
			}
			close(release)
		}
	}
}
func TestInvalidAnswersDoNotWin(t *testing.T) {
	for _, kind := range []string{"servfail", "refused", "wrong-question", "query", "truncated", "nil", "error"} {
		t.Run(kind, func(t *testing.T) {
			tr := &Transport{children: []adapter.DNSTransport{
				&fake{exchange: func(_ context.Context, m *mdns.Msg) (*mdns.Msg, error) {
					r := reply(m)
					switch kind {
					case "servfail":
						r.Rcode = mdns.RcodeServerFailure
					case "refused":
						r.Rcode = mdns.RcodeRefused
					case "wrong-question":
						r.Question[0].Name = "wrong.invalid."
					case "query":
						r.Response = false
					case "truncated":
						r.Truncated = true
					case "nil":
						return nil, nil
					case "error":
						return nil, errors.New("reset")
					}
					return r, nil
				}},
				&fake{exchange: func(_ context.Context, m *mdns.Msg) (*mdns.Msg, error) {
					time.Sleep(5 * time.Millisecond)
					r := reply(m)
					r.Rcode = mdns.RcodeNameError
					return r, nil
				}},
			}}
			r, e := tr.Exchange(context.Background(), question())
			if e != nil || r.Rcode != mdns.RcodeNameError {
				t.Fatalf("%v %v", r, e)
			}
		})
	}
}
func TestAllFailuresAndParentCancel(t *testing.T) {
	tr := &Transport{children: []adapter.DNSTransport{&fake{exchange: func(context.Context, *mdns.Msg) (*mdns.Msg, error) { return nil, errors.New("closed") }}}}
	if _, err := tr.Exchange(context.Background(), question()); err == nil {
		t.Fatal("all failures became success")
	}
	tr.children[0] = &fake{exchange: func(ctx context.Context, _ *mdns.Msg) (*mdns.Msg, error) { <-ctx.Done(); return nil, ctx.Err() }}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := tr.Exchange(ctx, question()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

// Production HTTPS constructors, TLS verification, POST wire packets and cancellation.
// A reset Google endpoint must not hide a working Cloudflare or Yandex endpoint.
func TestRealHTTPSRace(t *testing.T) {
	for winner := 0; winner < 3; winner++ {
		t.Run(strconv.Itoa(winner), func(t *testing.T) {
			var requests, cancellations atomic.Int32
			servers := make([]*httptest.Server, 0, 3)
			opts := Options{}
			for i := 0; i < 3; i++ {
				i := i
				server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					if r.Method != "POST" || r.URL.Path != "/dns-query" || r.Header.Get("Content-Type") != "application/dns-message" {
						t.Error("invalid DoH request")
						w.WriteHeader(400)
						return
					}
					raw, e := io.ReadAll(r.Body)
					if e != nil {
						return
					}
					m := new(mdns.Msg)
					if e = m.Unpack(raw); e != nil {
						t.Error(e)
						return
					}
					if i != winner {
						<-r.Context().Done()
						cancellations.Add(1)
						return
					}
					// Ensure losers really entered HTTPS before winning.
					until := time.Now().Add(time.Second)
					for requests.Load() < 3 && time.Now().Before(until) {
						time.Sleep(time.Millisecond)
					}
					response := reply(m)
					response.Answer = []mdns.RR{&mdns.A{Hdr: mdns.RR_Header{Name: m.Question[0].Name, Rrtype: mdns.TypeA, Class: mdns.ClassINET, Ttl: 60}, A: net.ParseIP("203.0.113.7")}}
					data, _ := response.Pack()
					w.Header().Set("Content-Type", "application/dns-message")
					w.Write(data)
				}))
				servers = append(servers, server)
				host, port, _ := net.SplitHostPort(server.Listener.Addr().String())
				p, _ := strconv.Atoi(port)
				cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
				o := option.RemoteHTTPSDNSServerOptions{Path: "/dns-query"}
				o.Server = host
				o.ServerPort = uint16(p)
				o.TLS = &option.OutboundTLSOptions{Enabled: true, ServerName: "example.com", Certificate: []string{string(cert)}}
				opts.Servers = append(opts.Servers, o)
			}
			tr, e := New(context.Background(), log.NewNOPFactory().Logger(), "dns-direct", opts)
			if e != nil {
				t.Fatal(e)
			}
			defer func() {
				tr.Close()
				for _, s := range servers {
					s.Close()
				}
			}()
			if e = tr.Start(adapter.StartStateStart); e != nil {
				t.Fatal(e)
			}
			r, e := tr.Exchange(context.Background(), question())
			if e != nil || len(r.Answer) != 1 {
				t.Fatalf("answer=%v err=%v", r, e)
			}
			until := time.Now().Add(time.Second)
			for cancellations.Load() < 2 && time.Now().Before(until) {
				time.Sleep(time.Millisecond)
			}
			if requests.Load() != 3 || cancellations.Load() != 2 {
				t.Fatalf("requests=%d canceled=%d", requests.Load(), cancellations.Load())
			}
			// Neither an invalid certificate nor all HTTPS failures can trigger OS fallback.
			opts.Servers[0].TLS.ServerName = "wrong.invalid"
			opts.Servers = opts.Servers[:2]
			opts.Servers[1].TLS.ServerName = "wrong.invalid"
			broken, e := New(context.Background(), log.NewNOPFactory().Logger(), "dns-direct", opts)
			if e != nil {
				t.Fatal(e)
			}
			defer broken.Close()
			if _, e = broken.Exchange(context.Background(), question()); e == nil {
				t.Fatal("unverified TLS accepted")
			}
		})
	}
}
