package vpndns

import (
	"context"
	"errors"
	"net"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	dns "ThroneCore/internal/dnsengine"
	mdns "github.com/miekg/dns"
	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
)

type fake struct {
	dns.TransportAdapter
	exchange func(context.Context, *mdns.Msg) (*mdns.Msg, error)
}

func (f *fake) Start(adapter.StartStage) error { return nil }
func (f *fake) Close() error                   { return nil }
func (f *fake) Reset()                         {}
func (f *fake) Exchange(ctx context.Context, q *mdns.Msg) (*mdns.Msg, error) {
	return f.exchange(ctx, q)
}
func (f *fake) ExchangeAsync(ctx context.Context, q *mdns.Msg, cb func(*mdns.Msg, error)) {
	go func() { cb(f.Exchange(ctx, q)) }()
}
func question() *mdns.Msg { return new(mdns.Msg).SetQuestion("race.invalid.", mdns.TypeA) }
func reply(q *mdns.Msg, index int) *mdns.Msg {
	r := new(mdns.Msg).SetReply(q)
	r.Answer = []mdns.RR{&mdns.A{Hdr: mdns.RR_Header{Name: q.Question[0].Name, Rrtype: mdns.TypeA, Class: mdns.ClassINET, Ttl: 60}, A: net.IPv4(192, 0, 2, byte(index+1))}}
	return r
}
func fixture(t *testing.T) *Transport {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	tr := &Transport{TransportAdapter: dns.NewTransportAdapter(Type, "dns-remote", nil), ctx: ctx, cancel: cancel,
		timeout: time.Second, fallbackTimeout: time.Second, logger: log.NewNOPFactory().Logger()}
	t.Cleanup(func() { tr.Close() })
	return tr
}

// Reproduce delayed arbitration deterministically: all replies are already queued
// when the selector runs, with a non-Google reply preceding Google's valid reply.
func TestCompletionOrderWhenSeveralAnswersAreReady(t *testing.T) {
	for _, winner := range []int{1, 2, 3} {
		t.Run(strconv.Itoa(winner), func(t *testing.T) {
			tr, q := fixture(t), question()
			results := make(chan result, 4)
			first := reply(q, winner)
			results <- result{response: first, tag: "first-non-google"}
			results <- result{response: reply(q, 0), tag: "google"}
			r, err := tr.receive(context.Background(), q, results, 2)
			if err != nil || r != first {
				t.Fatalf("completion order lost: %v %v", r, err)
			}
		})
	}
}

func TestEachPrimaryWinsWithoutWaitingForCanceledLosers(t *testing.T) {
	for winner := 0; winner < 4; winner++ {
		for _, async := range []bool{false, true} {
			t.Run(strconv.Itoa(winner)+"/async-"+strconv.FormatBool(async), func(t *testing.T) {
				tr := fixture(t)
				started, canceled := make(chan struct{}, 4), make(chan struct{}, 3)
				finishWinner, releaseLosers := make(chan struct{}), make(chan struct{})
				t.Cleanup(func() { close(releaseLosers) })
				for i := 0; i < 4; i++ {
					i := i
					tr.children = append(tr.children, &fake{exchange: func(ctx context.Context, q *mdns.Msg) (*mdns.Msg, error) {
						started <- struct{}{}
						if i == winner {
							<-finishWinner
							return reply(q, i), nil
						}
						<-ctx.Done()
						canceled <- struct{}{}
						<-releaseLosers
						return nil, ctx.Err()
					}})
				}
				done := make(chan result, 1)
				q := question()
				if async {
					tr.ExchangeAsync(context.Background(), q, func(r *mdns.Msg, err error) { done <- result{response: r, err: err} })
				} else {
					go func() { r, err := tr.Exchange(context.Background(), q); done <- result{response: r, err: err} }()
				}
				for i := 0; i < 4; i++ {
					select {
					case <-started:
					case <-time.After(time.Second):
						close(finishWinner)
						t.Fatal("not all primaries launched")
					}
				}
				close(finishWinner)
				select {
				case r := <-done:
					if r.err != nil || !r.response.Answer[0].(*mdns.A).A.Equal(net.IPv4(192, 0, 2, byte(winner+1))) {
						t.Fatalf("wrong winner: %+v", r)
					}
				case <-time.After(time.Second):
					t.Fatal("winner waited for losers")
				}
				for i := 0; i < 3; i++ {
					select {
					case <-canceled:
					case <-time.After(time.Second):
						t.Fatal("loser not canceled")
					}
				}
			})
		}
	}
}

func TestResponseValidation(t *testing.T) {
	for _, kind := range []string{"servfail", "refused", "wrong-id", "wrong-question", "wrong-type", "wrong-class", "wrong-opcode", "query", "truncated", "nil", "nodata", "nxdomain", "case-insensitive"} {
		t.Run(kind, func(t *testing.T) {
			q := question()
			r := new(mdns.Msg).SetReply(q)
			switch kind {
			case "servfail":
				r.Rcode = mdns.RcodeServerFailure
			case "refused":
				r.Rcode = mdns.RcodeRefused
			case "wrong-id":
				r.Id++
			case "wrong-question":
				r.Question = append([]mdns.Question(nil), r.Question...)
				r.Question[0].Name = "wrong.invalid."
			case "wrong-type":
				r.Question = append([]mdns.Question(nil), r.Question...)
				r.Question[0].Qtype = mdns.TypeAAAA
			case "wrong-class":
				r.Question = append([]mdns.Question(nil), r.Question...)
				r.Question[0].Qclass = mdns.ClassCHAOS
			case "wrong-opcode":
				r.Opcode = mdns.OpcodeStatus
			case "query":
				r.Response = false
			case "truncated":
				r.Truncated = true
			case "nil":
				r = nil
			case "nxdomain":
				r.Rcode = mdns.RcodeNameError
			case "case-insensitive":
				r.Question = append([]mdns.Question(nil), r.Question...)
				r.Question[0].Name = "RACE.INVALID."
			}
			want := kind == "nodata" || kind == "nxdomain" || kind == "case-insensitive"
			if valid(q, r) != want {
				t.Fatalf("validation of %s", kind)
			}
		})
	}
}

func TestFallbackRunsOnlyAfterPrimaryFailureOrTimeout(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		t.Run(strconv.FormatBool(timeout), func(t *testing.T) {
			tr := fixture(t)
			tr.timeout = 20 * time.Millisecond
			var primaries, fallbacks atomic.Int32
			for i := 0; i < 4; i++ {
				tr.children = append(tr.children, &fake{exchange: func(ctx context.Context, q *mdns.Msg) (*mdns.Msg, error) {
					primaries.Add(1)
					if timeout {
						<-ctx.Done()
						return nil, ctx.Err()
					}
					r := new(mdns.Msg).SetReply(q)
					r.Rcode = mdns.RcodeServerFailure
					return r, nil
				}})
			}
			tr.fallback = &fake{exchange: func(_ context.Context, q *mdns.Msg) (*mdns.Msg, error) {
				fallbacks.Add(1)
				if primaries.Load() != 4 {
					t.Error("fallback started before primaries")
				}
				return reply(q, 4), nil
			}}
			r, err := tr.Exchange(context.Background(), question())
			if err != nil || r == nil || fallbacks.Load() != 1 {
				t.Fatalf("fallback failed: %v %v", r, err)
			}
		})
	}
}

func TestValidNegativeAnswerDoesNotUseFallback(t *testing.T) {
	for _, code := range []int{mdns.RcodeSuccess, mdns.RcodeNameError} {
		tr := fixture(t)
		tr.children = []adapter.DNSTransport{&fake{exchange: func(_ context.Context, q *mdns.Msg) (*mdns.Msg, error) {
			r := new(mdns.Msg).SetReply(q)
			r.Rcode = code
			return r, nil
		}}}
		tr.fallback = &fake{exchange: func(context.Context, *mdns.Msg) (*mdns.Msg, error) {
			t.Error("valid negative answer used fallback")
			return nil, errors.New("unused")
		}}
		r, err := tr.Exchange(context.Background(), question())
		if err != nil || r.Rcode != code {
			t.Fatalf("negative response: %v %v", r, err)
		}
	}
}

func TestCancelCloseAndAllFailures(t *testing.T) {
	tr := fixture(t)
	var fallbacks atomic.Int32
	tr.children = []adapter.DNSTransport{&fake{exchange: func(ctx context.Context, _ *mdns.Msg) (*mdns.Msg, error) { <-ctx.Done(); return nil, ctx.Err() }}}
	tr.fallback = &fake{exchange: func(context.Context, *mdns.Msg) (*mdns.Msg, error) {
		fallbacks.Add(1)
		return nil, errors.New("failed")
	}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if r, err := tr.Exchange(ctx, question()); err != nil || r.Rcode != mdns.RcodeServerFailure || fallbacks.Load() != 0 {
		t.Fatalf("parent cancellation: %v", err)
	}
	tr.Close()
	if r, err := tr.Exchange(context.Background(), question()); err != nil || r.Rcode != mdns.RcodeServerFailure || fallbacks.Load() != 0 {
		t.Fatal("closed transport queried fallback")
	}
	failed := fixture(t)
	failed.children = []adapter.DNSTransport{&fake{exchange: func(context.Context, *mdns.Msg) (*mdns.Msg, error) { return nil, errors.New("failed") }}}
	failed.fallback = failed.children[0]
	if r, err := failed.Exchange(context.Background(), question()); err != nil || r.Rcode != mdns.RcodeServerFailure {
		t.Fatal("all failures became success")
	}
}

// Use the real pinned DNS client to verify TTL caching before any new race, a
// non-Google cached winner, cache bypass, and ClearCache after network changes.
func TestNativeTTLCacheBeforeNetwork(t *testing.T) {
	tr := fixture(t)
	var queries atomic.Int32
	tr.children = []adapter.DNSTransport{&fake{exchange: func(_ context.Context, q *mdns.Msg) (*mdns.Msg, error) { queries.Add(1); return reply(q, 2), nil }}}
	client := dns.NewClient(dns.ClientOptions{Context: context.Background(), Logger: log.NewNOPFactory().Logger()})
	client.Start()
	for i := 0; i < 2; i++ {
		r, err := client.Lookup(context.Background(), tr, "race.invalid", adapter.DNSQueryOptions{Strategy: C.DomainStrategyIPv4Only}, nil)
		if err != nil || len(r) != 1 || r[0].String() != "192.0.2.3" {
			t.Fatalf("cached winner: %v %v", r, err)
		}
	}
	if queries.Load() != 1 {
		t.Fatal("cache hit started another network race")
	}
	_, err := client.Lookup(context.Background(), tr, "race.invalid", adapter.DNSQueryOptions{Strategy: C.DomainStrategyIPv4Only, DisableCache: true}, nil)
	if err != nil || queries.Load() != 2 {
		t.Fatal("disable_cache ignored")
	}
	client.ClearCache()
	_, err = client.Lookup(context.Background(), tr, "race.invalid", adapter.DNSQueryOptions{Strategy: C.DomainStrategyIPv4Only}, nil)
	if err != nil || queries.Load() != 3 {
		t.Fatal("cache clear ignored")
	}
}
