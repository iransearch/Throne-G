package vpndns

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dns "ThroneCore/internal/dnsengine"
	mdns "github.com/miekg/dns"
	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
)

func provider(tag, kind string, fn func(context.Context, *mdns.Msg) (*mdns.Msg, error)) *fake {
	return &fake{TransportAdapter: dns.NewTransportAdapter(kind, tag, nil), exchange: fn}
}

func TestAndroidCachePreflightAllProvidersIncludingFallback(t *testing.T) {
	for _, source := range []string{"cloudflare", "yandex", "fallback"} {
		t.Run(source, func(t *testing.T) {
			tr := fixture(t)
			var network atomic.Int32
			fn := func(_ context.Context, q *mdns.Msg) (*mdns.Msg, error) { network.Add(1); return reply(q, 0), nil }
			tr.children = []adapter.DNSTransport{provider("google", "https", fn), provider("cloudflare", "https", fn), provider("yandex", "https", fn), provider("udp", "udp", fn)}
			tr.fallback = provider("fallback", "tcp", fn)
			var cached adapter.DNSTransport
			for _, child := range tr.all() {
				if child.Tag() == source {
					cached = child
				}
			}
			q := question()
			tr.cache().CacheResponse(cached, q, reply(q, 2), adapter.DNSQueryOptions{})
			ips, err := tr.DNSLookup(context.Background(), "race.invalid", adapter.DNSQueryOptions{Strategy: C.DomainStrategyIPv4Only})
			if err != nil || len(ips) != 1 || ips[0].String() != "192.0.2.3" || network.Load() != 0 {
				t.Fatalf("preflight: %v %v requests=%d", ips, err, network.Load())
			}
		})
	}
}

func TestAndroidRawBypassesIPCacheAndFailuresReturnSERVFAIL(t *testing.T) {
	tr := fixture(t)
	var network atomic.Int32
	child := provider("google", "https", func(_ context.Context, q *mdns.Msg) (*mdns.Msg, error) { network.Add(1); return reply(q, 0), nil })
	tr.children = []adapter.DNSTransport{child}
	q := question()
	tr.cache().CacheResponse(child, q, reply(q, 2), adapter.DNSQueryOptions{})
	client := dns.NewClient(dns.ClientOptions{Context: context.Background(), Logger: log.NewNOPFactory().Logger()})
	client.Start()
	for i := 0; i < 2; i++ {
		r, err := client.Exchange(context.Background(), tr, q, adapter.DNSQueryOptions{}, nil)
		if err != nil || !r.Answer[0].(*mdns.A).A.Equal(net.IPv4(192, 0, 2, 1)) {
			t.Fatalf("raw cache bypass: %v %v", r, err)
		}
	}
	if network.Load() != 2 {
		t.Fatal("raw requests were cached by the outer client")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, err := tr.DNSExchange(ctx, q, adapter.DNSQueryOptions{})
	if err != nil || r.Rcode != mdns.RcodeServerFailure || !r.RecursionAvailable {
		t.Fatal("Android raw failure response changed")
	}
}

func TestAndroidCachePreflightRequiresCompleteFamiliesAndSkipsTTLZero(t *testing.T) {
	tr := fixture(t)
	var network atomic.Int32
	child := provider("google", "https", func(_ context.Context, q *mdns.Msg) (*mdns.Msg, error) {
		network.Add(1)
		return new(mdns.Msg).SetReply(q), nil
	})
	tr.children = []adapter.DNSTransport{child}
	q := question()
	r := new(mdns.Msg).SetReply(q)
	tr.cache().CacheResponse(child, q, r, adapter.DNSQueryOptions{})
	// A NODATA entry with no SOA/TTL cannot become an immortal negative record.
	if cached := tr.cached(child, q, []uint16{mdns.TypeA, mdns.TypeAAAA}, adapter.DNSQueryOptions{}); len(cached) == 2 {
		t.Fatal("partial family cache treated as complete")
	}
	r = reply(q, 0)
	r.Answer[0].Header().Ttl = 0
	tr.cache().ClearCache()
	tr.cache().CacheResponse(child, q, r, adapter.DNSQueryOptions{})
	if _, hit := tr.cache().CachedResponse(child, q, adapter.DNSQueryOptions{}); hit {
		t.Fatal("TTL=0 cached")
	}
}

func TestAndroidDualFamilyLookupUsesOneProviderRace(t *testing.T) {
	tr := fixture(t)
	var requests atomic.Int32
	started := make(chan struct{}, 2)
	tr.children = []adapter.DNSTransport{
		provider("google", "https", func(ctx context.Context, _ *mdns.Msg) (*mdns.Msg, error) { <-ctx.Done(); return nil, ctx.Err() }),
		provider("cloudflare", "https", func(_ context.Context, q *mdns.Msg) (*mdns.Msg, error) {
			requests.Add(1)
			started <- struct{}{}
			if q.Question[0].Qtype == mdns.TypeAAAA {
				return new(mdns.Msg).SetReply(q), nil
			}
			return reply(q, 1), nil
		}),
	}
	client := dns.NewClient(dns.ClientOptions{Context: context.Background(), Logger: log.NewNOPFactory().Logger()})
	client.Start()
	ctx := dns.ContextWithLookup(context.Background(), []uint16{mdns.TypeA, mdns.TypeAAAA})
	var workers sync.WaitGroup
	for _, qType := range []uint16{mdns.TypeA, mdns.TypeAAAA} {
		qType := qType
		workers.Add(1)
		go func() {
			defer workers.Done()
			q := typedQuery(question(), qType)
			r, err := client.Exchange(ctx, tr, q, adapter.DNSQueryOptions{}, nil)
			if err != nil || r.Rcode != mdns.RcodeSuccess {
				t.Errorf("lookup family: %v %v", r, err)
			}
		}()
	}
	workers.Wait()
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("family query not launched")
		}
	}
	if requests.Load() != 2 {
		t.Fatalf("families raced providers independently: requests=%d", requests.Load())
	}
}

func TestAndroidIPFailureWaitsForPrimaryDeadlineBeforeFallback(t *testing.T) {
	tr := fixture(t)
	tr.timeout = 30 * time.Millisecond
	tr.children = []adapter.DNSTransport{provider("google", "https", func(_ context.Context, q *mdns.Msg) (*mdns.Msg, error) {
		r := new(mdns.Msg).SetReply(q)
		r.Rcode = mdns.RcodeServerFailure
		return r, nil
	})}
	tr.fallback = provider("fallback", "tcp", func(_ context.Context, q *mdns.Msg) (*mdns.Msg, error) { return reply(q, 1), nil })
	started := time.Now()
	ips, err := tr.DNSLookup(context.Background(), "race.invalid", adapter.DNSQueryOptions{Strategy: C.DomainStrategyIPv4Only})
	if err != nil || len(ips) != 1 || time.Since(started) < tr.timeout {
		t.Fatalf("IP stage deadline skipped: %v %v", ips, err)
	}
}

func TestSeventhUDPProviderWinsIPLookupAndUsesItsOwnCache(t *testing.T) {
	tr := fixture(t)
	var queries, live atomic.Int32
	for i := 0; i < 7; i++ {
		i := i
		kind := "udp"
		if i < 3 {
			kind = "https"
		}
		tr.children = append(tr.children, provider(string(rune('a'+i)), kind, func(ctx context.Context, q *mdns.Msg) (*mdns.Msg, error) {
			live.Add(1)
			defer live.Add(-1)
			if i == 6 {
				queries.Add(1)
				return reply(q, i), nil
			}
			<-ctx.Done()
			return nil, ctx.Err()
		}))
	}
	for i := 0; i < 3; i++ {
		if i == 2 {
			tr.DNSClearCache()
		}
		ips, err := tr.DNSLookup(context.Background(), "race.invalid", adapter.DNSQueryOptions{Strategy: C.DomainStrategyIPv4Only})
		if err != nil || len(ips) != 1 || ips[0].String() != "192.0.2.7" {
			t.Fatalf("seventh IP provider: %v %v", ips, err)
		}
		want := int32(1)
		if i == 2 {
			want = 2
		}
		if queries.Load() != want {
			t.Fatalf("provider-owned cache: calls=%d want=%d", queries.Load(), want)
		}
	}
	tr.Close()
	if live.Load() != 0 {
		t.Fatal("IP provider workers remained after close")
	}
	if tr.diagnostics.wins[6] != 3 || tr.diagnostics.cached != 1 || tr.diagnostics.wins[7] != 0 {
		t.Fatal("seventh primary was misreported as fallback or lost its cache hit")
	}
}
