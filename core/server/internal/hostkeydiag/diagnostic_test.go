package hostkeydiag

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"ThroneCore/internal/netdiag"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

func capture() (context.Context, func() string) {
	var mu sync.Mutex
	var lines []string
	ctx := WithObserver(netdiag.WithBox(context.Background()))
	o := ctx.Value(observerKey{}).(*observer)
	o.emit = func(event string, box uint64, fields string) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, fmt.Sprintf("event=%s box=%d %s", event, box, fields))
	}
	ctx = log.ContextWithID(ctx, log.ID{ID: 42})
	return ctx, func() string { mu.Lock(); defer mu.Unlock(); return strings.Join(lines, "\n") }
}

func TestAllowlistAndQueryIsolation(t *testing.T) {
	for _, name := range []string{"hostkey.com", "INVAPI.HOSTKEY.COM.", "api.hostkey.com"} {
		if !Matches(name) {
			t.Fatal(name)
		}
	}
	for _, name := range []string{"evilhostkey.com", "hostkey.com.evil", "https://invapi.hostkey.com/secret", "user@hostkey.com", "hostkey.com\npassword=x"} {
		if Matches(name) {
			t.Fatal(name)
		}
	}
	ctx, out := capture()
	original := ctx
	ctx, q := StartDNS(ctx, "invapi.hostkey.com", 1, 0)
	DNSWinner(ctx, 6, true, "ip-cache", nil)
	q.Finish([]netip.Addr{netip.MustParseAddr("10.10.34.36"), netip.MustParseAddr("2606:4700:20::1")}, 0, nil)
	other, q2 := StartDNS(ctx, "private.customer.example", 1, 0)
	DNSWinner(other, 4, false, "raw-network", errors.New("PASSWORD secret"))
	if q2 != nil || strings.Contains(out(), "index=4") || strings.Contains(out(), "secret") || strings.Contains(out(), "customer") {
		t.Fatal(out())
	}
	if !strings.Contains(out(), "request=42") || !strings.Contains(out(), "public=false") || !strings.Contains(out(), "a=1 aaaa=1") || !strings.Contains(out(), "cacheHit=true") {
		t.Fatal(out())
	}
	if _, ok := original.Deadline(); ok || original.Err() != nil {
		t.Fatal("context changed")
	}
}

type fakeRouter struct {
	adapter.Router
	seen     net.Conn
	calls    int
	err      error
	track    adapter.ConnectionTracker
	metadata adapter.InboundContext
}

func (r *fakeRouter) AppendTracker(t adapter.ConnectionTracker) { r.track = t }
func (r *fakeRouter) RouteConnectionEx(ctx context.Context, c net.Conn, m adapter.InboundContext, close N.CloseHandlerFunc) {
	r.calls++
	r.seen = c
	if r.metadata.Domain != "" {
		m = r.metadata
	}
	if r.track.RoutedConnection(ctx, c, m, nil, nil) != c {
		panic("stream wrapped")
	}
	close(r.err)
}

func TestRouterForwardsOriginalStreamErrorAndCallback(t *testing.T) {
	for _, domain := range []string{"invapi.hostkey.com", "private.customer.example", ""} {
		ctx, out := capture()
		originalErr := errors.New("connection reset by peer PASSWORD secret")
		f := &fakeRouter{err: originalErr}
		r := ObserveRouter(f)
		a, b := net.Pipe()
		defer a.Close()
		defer b.Close()
		calls := 0
		m := adapter.InboundContext{InboundType: "mixed", Network: "tcp", Destination: M.Socksaddr{Fqdn: domain, Port: 443}}
		r.RouteConnectionEx(ctx, a, m, func(err error) {
			calls++
			if err != originalErr {
				t.Fatal("original error changed")
			}
		})
		if calls != 1 || f.calls != 1 || f.seen != a || strings.Contains(out(), "PASSWORD") {
			t.Fatal(out())
		}
		if (domain == "invapi.hostkey.com") != strings.Contains(out(), "hostkey-end") {
			t.Fatal(out())
		}
	}
}

func TestTunDiscoveryAndPoolCorrelationWithoutDNSDetours(t *testing.T) {
	ctx, out := capture()
	m := adapter.InboundContext{InboundType: "tun", Network: "tcp", Domain: "invapi.hostkey.com", Destination: M.ParseSocksaddr("104.26.12.47:443")}
	f := &fakeRouter{metadata: m}
	r := ObserveRouter(f)
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	r.RouteConnectionEx(ctx, a, adapter.InboundContext{InboundType: "tun", Destination: m.Destination}, nil)
	if !strings.Contains(out(), "inboundKind=tun") || !strings.Contains(out(), "peer=104.26.12.47") || !strings.Contains(out(), "hostkey-end") {
		t.Fatal(out())
	}
	ctx, tr := routeContext(ctx, m)
	ctx = adapter.WithContext(ctx, &m)
	PoolAttempt(ctx, m.Destination, "proxy", "smart-proxy-1", 1, nil, time.Millisecond)
	dnsCtx, q := StartDNS(ctx, "invapi.hostkey.com", 0, 0)
	PoolAttempt(dnsCtx, M.ParseSocksaddr("8.8.8.8:443"), "proxy", "smart-proxy-0", 0, nil, time.Millisecond)
	q.Finish(nil, 0, nil)
	tr.finish(nil)
	if strings.Count(out(), "hostkey-pool-dial") != 1 || strings.Contains(out(), "8.8.8.8") || !strings.Contains(out(), "memberTag="+netdiag.Hash("smart-proxy-1")) {
		t.Fatal(out())
	}
}

func TestObserverBoundedAndConcurrent(t *testing.T) {
	ctx, out := capture()
	ctx, q := StartDNS(ctx, "invapi.hostkey.com", 1, 0)
	var wg sync.WaitGroup
	for range 200 {
		wg.Add(1)
		go func() { defer wg.Done(); DNSWinner(ctx, 3, false, "ip-network", nil) }()
	}
	wg.Wait()
	q.trace.finish(nil)
	if strings.Count(out(), "\n") > 65 || !strings.Contains(out(), "hostkey-end") {
		t.Fatal("unbounded trace")
	}
	for range 200 {
		_, q := StartDNS(context.WithValue(ctx, traceKey{}, (*trace)(nil)), "hostkey.com", 1, 0)
		if q != nil {
			q.Finish(nil, 0, nil)
		}
	}
	if q.trace.observer.admitted.Load() < 128 {
		t.Fatal("admission not exercised")
	}
}

func TestTerminalRecordAtLimitIsNotReplaced(t *testing.T) {
	ctx, out := capture()
	_, q := StartDNS(ctx, "hostkey.com", 1, 0)
	for q.trace.events.Load() < 63 {
		q.trace.emit("hostkey-route", "")
	}
	q.trace.finish(nil)
	if !strings.Contains(out(), "event=hostkey-end ") {
		t.Fatal("terminal record replaced at event limit")
	}
}
