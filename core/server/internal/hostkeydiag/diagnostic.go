// Package hostkeydiag observes existing Hostkey requests. It never performs I/O,
// resolves another name, changes a deadline, or chooses an outbound.
package hostkeydiag

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"ThroneCore/internal/netdiag"
	mdns "github.com/miekg/dns"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	M "github.com/sagernet/sing/common/metadata"
)

type observerKey struct{}
type traceKey struct{}
type queryKey struct{}
type dnsWorkKey struct{}

// Bounded per Box, with no static request/address cache or background worker.
type observer struct {
	box      uint64
	admitted atomic.Uint64
	emit     func(string, uint64, string)
}

type trace struct {
	observer *observer
	id       uint64
	request  uint32
	started  time.Time
	enabled  atomic.Bool
	ended    sync.Once
	events   atomic.Uint64
}

func WithObserver(ctx context.Context) context.Context {
	return context.WithValue(ctx, observerKey{}, &observer{box: netdiag.Box(ctx), emit: netdiag.Emit})
}

func Matches(domain string) bool {
	domain = strings.TrimSuffix(strings.ToLower(domain), ".")
	if len(domain) > 253 || strings.ContainsAny(domain, "/:@%\\ \t\r\n") {
		return false
	}
	return domain == "hostkey.com" || strings.HasSuffix(domain, ".hostkey.com")
}

func newTrace(ctx context.Context) *trace {
	o, _ := ctx.Value(observerKey{}).(*observer)
	if o == nil {
		return nil
	}
	id, _ := log.IDFromContext(ctx)
	return &trace{observer: o, id: netdiag.ID(), request: id.ID, started: time.Now()}
}

func from(ctx context.Context) *trace { t, _ := ctx.Value(traceKey{}).(*trace); return t }

func (t *trace) enable(domain string) {
	if t == nil || !Matches(domain) || t.enabled.Load() {
		return
	}
	if t.observer.admitted.Add(1) > 128 {
		return
	}
	if t.enabled.CompareAndSwap(false, true) {
		t.emit("hostkey-begin", "")
	}
}

func (t *trace) emit(event, fields string) {
	if t == nil || !t.enabled.Load() {
		return
	}
	n := t.events.Add(1)
	if n > 64 && event != "hostkey-end" {
		return
	}
	if n == 64 && event != "hostkey-end" {
		event, fields = "hostkey-limit", ""
	}
	t.observer.emit(event, t.observer.box, fmt.Sprintf("target=hostkey trace=%d request=%d %s", t.id, t.request, fields))
}

func ErrorKind(err error) string {
	if err == nil {
		return "none"
	}
	if errors.Is(err, io.EOF) {
		return "eof"
	}
	s := strings.ToLower(err.Error())
	switch {
	case strings.Contains(s, "reject"), strings.Contains(s, "not permitted"):
		return "rejected"
	case strings.Contains(s, "reset"):
		return "reset"
	case strings.Contains(s, "refused"):
		return "refused"
	case strings.Contains(s, "closed pipe"), strings.Contains(s, "broken pipe"):
		return "pipe"
	case strings.Contains(s, "no usable outbound"):
		return "no-member"
	case strings.Contains(s, "lookup"), strings.Contains(s, "dns"):
		return "dns"
	case strings.Contains(s, "tls"), strings.Contains(s, "certificate"):
		return "tls"
	default:
		return netdiag.ErrorKind(err)
	}
}

func failure(err error) string {
	var code syscall.Errno
	errors.As(err, &code)
	return fmt.Sprintf("error=%s errno=%d", ErrorKind(err), uint64(code))
}

func family(addr netip.Addr) int {
	if addr.Unmap().Is4() {
		return 4
	}
	if addr.Is6() {
		return 6
	}
	return 0
}

func (t *trace) addresses(event string, query uint64, addresses []netip.Addr) {
	for i, addr := range addresses {
		if i == 8 {
			break
		}
		if !addr.IsValid() {
			continue
		}
		// Only literal answers/destinations of the allowlisted Hostkey request.
		t.emit(event, fmt.Sprintf("query=%d index=%d peer=%s family=%d public=%t", query, i, addr.WithZone(""), family(addr),
			addr.IsGlobalUnicast() && !addr.IsPrivate() && !addr.IsLoopback() && !addr.IsLinkLocalUnicast()))
	}
}

type DNSObservation struct {
	trace   *trace
	query   uint64
	started time.Time
}

func StartDNS(ctx context.Context, domain string, queryType uint16, strategy int) (context.Context, *DNSObservation) {
	if !Matches(domain) {
		// A DNS stream can contain several names; never inherit another query's scope.
		if ctx.Value(queryKey{}) != nil {
			ctx = context.WithValue(ctx, queryKey{}, uint64(0))
		}
		return ctx, nil
	}
	t := from(ctx)
	if t == nil {
		t = newTrace(ctx)
	}
	if t == nil {
		return ctx, nil
	}
	t.enable(domain)
	if !t.enabled.Load() {
		return ctx, nil
	}
	q := &DNSObservation{t, netdiag.ID(), time.Now()}
	ctx = context.WithValue(ctx, traceKey{}, t)
	ctx = context.WithValue(ctx, queryKey{}, q.query)
	ctx = context.WithValue(ctx, dnsWorkKey{}, true)
	t.emit("hostkey-dns-begin", fmt.Sprintf("query=%d queryType=%d strategy=%d", q.query, queryType, strategy))
	return ctx, q
}

func (q *DNSObservation) Finish(addresses []netip.Addr, rcode int, err error) {
	if q == nil {
		return
	}
	a, aaaa := 0, 0
	for _, addr := range addresses {
		if family(addr) == 4 {
			a++
		}
		if family(addr) == 6 {
			aaaa++
		}
	}
	q.trace.emit("hostkey-dns-end", fmt.Sprintf("query=%d a=%d aaaa=%d rcode=%d elapsedMs=%d %s", q.query, a, aaaa, rcode, time.Since(q.started).Milliseconds(), failure(err)))
	q.trace.addresses("hostkey-dns-address", q.query, addresses)
}

func DNSWinner(ctx context.Context, index int, cached bool, source string, err error) {
	t := from(ctx)
	if t == nil {
		return
	}
	query, _ := ctx.Value(queryKey{}).(uint64)
	if query == 0 {
		return
	}
	if source != "raw-network" && source != "ip-network" && source != "ip-cache" {
		return
	}
	t.emit("hostkey-dns-winner", fmt.Sprintf("query=%d index=%d cacheHit=%t source=%s %s", query, index, cached, source, failure(err)))
}

func routeKind(tag string) string {
	switch tag {
	case "proxy":
		return "main"
	case "ai-proxy":
		return "ai"
	case "direct", "split-direct":
		return "direct"
	case "block":
		return "block"
	default:
		return "other"
	}
}

// Called at the existing AutoSelector dial, never for the DNS providers' detours.
func PoolAttempt(ctx context.Context, destination M.Socksaddr, pool, member string, attempt int, err error, elapsed time.Duration) {
	if ctx.Value(dnsWorkKey{}) == true {
		return
	}
	t := from(ctx)
	if t == nil {
		return
	}
	metadata := adapter.ContextFrom(ctx)
	if metadata == nil || (!Matches(metadata.Domain) && !Matches(metadata.Destination.Fqdn)) {
		return
	}
	t.enable(metadata.Domain)
	t.enable(metadata.Destination.Fqdn)
	t.emit("hostkey-pool-dial", fmt.Sprintf("route=%s memberTag=%s attempt=%d family=%d elapsedMs=%d %s", routeKind(pool), netdiag.Hash(member), attempt, family(destination.Addr), elapsed.Milliseconds(), failure(err)))
	t.addresses("hostkey-dial-address", 0, []netip.Addr{destination.Addr})
}

func StartDNSMessage(ctx context.Context, message *mdns.Msg, strategy int) (context.Context, *DNSObservation) {
	if message == nil || len(message.Question) != 1 {
		return ctx, nil
	}
	return StartDNS(ctx, message.Question[0].Name, message.Question[0].Qtype, strategy)
}
func (q *DNSObservation) FinishMessage(response *mdns.Msg, err error) {
	if q == nil {
		return
	}
	rcode := -1
	if response != nil {
		rcode = response.Rcode
	}
	q.Finish(adapter.DNSResponseAddresses(response), rcode, err)
}
