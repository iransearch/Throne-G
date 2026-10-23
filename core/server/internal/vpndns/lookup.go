package vpndns

import (
	"context"
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"time"

	dns "ThroneCore/internal/dnsengine"
	mdns "github.com/miekg/dns"
	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
)

var _ dns.ManagedTransport = (*Transport)(nil)

type ipResult struct {
	responses map[uint16]*mdns.Msg
	err       error
	tag       string
}
type sharedLookup struct {
	once   sync.Once
	result ipResult
}

func (t *Transport) cache() *dns.Client {
	t.ipOnce.Do(func() {
		ctx, logger := t.ctx, t.logger
		if ctx == nil {
			ctx = context.Background()
		}
		if logger == nil {
			logger = log.NewNOPFactory().Logger()
		}
		t.ipClient = dns.NewClient(dns.ClientOptions{Context: ctx, Logger: logger})
		t.ipClient.Start()
	})
	return t.ipClient
}
func (t *Transport) DNSClearCache() { t.cache().ClearCache() }

func lookupTypes(options adapter.DNSQueryOptions) []uint16 {
	strategy := options.LookupStrategy
	if strategy == C.DomainStrategyAsIS {
		strategy = options.Strategy
	}
	if strategy == C.DomainStrategyIPv4Only {
		return []uint16{mdns.TypeA}
	}
	if strategy == C.DomainStrategyIPv6Only {
		return []uint16{mdns.TypeAAAA}
	}
	return []uint16{mdns.TypeA, mdns.TypeAAAA}
}
func typedQuery(template *mdns.Msg, qType uint16) *mdns.Msg {
	q := template.Copy()
	q.Question[0].Qtype = qType
	return q
}
func hasAddresses(responses map[uint16]*mdns.Msg) bool {
	for _, response := range responses {
		if response.Rcode == mdns.RcodeSuccess && len(dns.MessageToAddresses(response)) > 0 {
			return true
		}
	}
	return false
}
func responseFor(result ipResult, query *mdns.Msg) *mdns.Msg {
	if r := result.responses[query.Question[0].Qtype]; r != nil {
		r = r.Copy()
		r.Id = query.Id
		if !hasAddresses(result.responses) {
			r.Rcode = negativeCode(result.responses)
		}
		return r
	}
	r := new(mdns.Msg).SetReply(query)
	if result.err != nil {
		r.Rcode = mdns.RcodeServerFailure
	}
	r.RecursionAvailable = true
	return r
}

func negativeCode(responses map[uint16]*mdns.Msg) int {
	// Android IPRecord aggregation uses the last requested family error.
	if r := responses[mdns.TypeAAAA]; r != nil {
		return r.Rcode
	}
	if r := responses[mdns.TypeA]; r != nil {
		return r.Rcode
	}
	return mdns.RcodeSuccess
}

func (t *Transport) DNSExchange(ctx context.Context, message *mdns.Msg, options adapter.DNSQueryOptions) (*mdns.Msg, error) {
	if session := dns.LookupContextFrom(ctx); session != nil && len(message.Question) == 1 {
		ttl := int64(-1)
		if options.RewriteTTL != nil {
			ttl = int64(*options.RewriteTTL)
		}
		key := fmt.Sprintf("%p/%s/%t/%t/%d/%d/%d/%s/%t/%d", t, strings.ToLower(message.Question[0].Name), options.DisableCache, options.DisableOptimisticCache, options.Strategy, options.LookupStrategy, options.Timeout, options.ClientSubnet.String(), options.RemoveClientSubnet, ttl)
		v, _ := session.Results.LoadOrStore(key, new(sharedLookup))
		shared := v.(*sharedLookup)
		shared.once.Do(func() { shared.result = t.lookupResponses(session.Context, message, session.Types, options) })
		return responseFor(shared.result, message), nil
	}
	// Android QueryRaw bypasses the IP cache, including on repeated A/AAAA queries.
	response, err := t.rawExchange(ctx, message)
	if err == nil {
		return response, nil
	}
	response = new(mdns.Msg).SetRcode(message, mdns.RcodeServerFailure)
	response.RecursionAvailable = true
	return response, nil
}

func (t *Transport) DNSLookup(ctx context.Context, domain string, options adapter.DNSQueryOptions) ([]netip.Addr, error) {
	types := lookupTypes(options)
	query := new(mdns.Msg).SetQuestion(mdns.Fqdn(domain), types[0])
	r := t.lookupResponses(ctx, query, types, options)
	if r.err != nil {
		return nil, dns.RcodeError(mdns.RcodeServerFailure)
	}
	var addresses []netip.Addr
	for _, qType := range types {
		if response := r.responses[qType]; response != nil && response.Rcode == mdns.RcodeSuccess {
			addresses = append(addresses, dns.MessageToAddresses(response)...)
		}
	}
	if len(addresses) == 0 {
		if code := negativeCode(r.responses); code != mdns.RcodeSuccess {
			return nil, dns.RcodeError(code)
		}
	}
	for i := range addresses {
		addresses[i] = addresses[i].Unmap()
	}
	strategy := options.LookupStrategy
	if strategy == C.DomainStrategyAsIS {
		strategy = options.Strategy
	}
	if strategy == C.DomainStrategyPreferIPv6 {
		var sorted []netip.Addr
		for _, ip := range addresses {
			if ip.Is6() {
				sorted = append(sorted, ip)
			}
		}
		for _, ip := range addresses {
			if ip.Is4() {
				sorted = append(sorted, ip)
			}
		}
		addresses = sorted
	}
	return addresses, nil
}

func (t *Transport) cached(child adapter.DNSTransport, query *mdns.Msg, types []uint16, options adapter.DNSQueryOptions) map[uint16]*mdns.Msg {
	responses := make(map[uint16]*mdns.Msg)
	for _, qType := range types {
		q := typedQuery(query, qType)
		if r, hit := t.cache().CachedResponse(child, q, options); hit && valid(q, r) {
			responses[qType] = r
		}
	}
	return responses
}

func (t *Transport) lookupResponses(parent context.Context, query *mdns.Msg, types []uint16, options adapter.DNSQueryOptions) ipResult {
	t.gate.Lock()
	closed := t.closed
	t.gate.Unlock()
	if closed {
		return ipResult{err: context.Canceled}
	}
	query = t.cache().PrepareQuery(query, options)
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	if options.Timeout > 0 {
		var limit context.CancelFunc
		ctx, limit = context.WithTimeout(ctx, options.Timeout)
		defer limit()
	}
	if t.ctx != nil {
		stop := context.AfterFunc(t.ctx, cancel)
		defer stop()
	}
	if ctx.Err() != nil {
		return ipResult{err: ctx.Err()}
	}
	// Exactly Android's cache-only pass: every DoH and TCP resolver, including
	// fallback, before any primary network work; require all requested families.
	for _, child := range t.all() {
		if child.Type() != "https" && child.Type() != "tcp" {
			continue
		}
		responses := t.cached(child, query, types, options)
		if len(responses) == len(types) {
			if t.logger != nil {
				t.logger.DebugContext(ctx, "VPN DNS IP cache winner=", child.Tag())
			}
			return ipResult{responses: responses, tag: child.Tag()}
		}
	}
	if r := t.ipStage(ctx, query, types, options, t.children, t.timeout); r.err == nil {
		return r
	}
	if ctx.Err() != nil {
		return ipResult{err: ctx.Err()}
	}
	if t.fallback != nil {
		return t.ipStage(ctx, query, types, options, []adapter.DNSTransport{t.fallback}, t.fallbackTimeout)
	}
	return ipResult{err: dns.RcodeError(mdns.RcodeServerFailure)}
}

func (t *Transport) ipStage(parent context.Context, query *mdns.Msg, types []uint16, options adapter.DNSQueryOptions, children []adapter.DNSTransport, timeout time.Duration) ipResult {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	results := make(chan ipResult, len(children))
	t.gate.Lock()
	if t.closed || ctx.Err() != nil {
		t.gate.Unlock()
		return ipResult{err: context.Canceled}
	}
	t.workers.Add(len(children))
	t.gate.Unlock()
	for _, child := range children {
		go func(child adapter.DNSTransport) {
			defer t.workers.Done()
			results <- t.queryIP(ctx, child, query, types, options)
		}(child)
	}
	for range children {
		select {
		case <-ctx.Done():
			return ipResult{err: ctx.Err()}
		case r := <-results:
			if r.err == nil {
				if t.logger != nil {
					t.logger.DebugContext(ctx, "VPN DNS IP winner=", r.tag)
				}
				return r
			}
		}
	}
	return ipResult{err: dns.RcodeError(mdns.RcodeServerFailure)}
}

func (t *Transport) queryIP(ctx context.Context, child adapter.DNSTransport, query *mdns.Msg, types []uint16, options adapter.DNSQueryOptions) ipResult {
	responses := t.cached(child, query, types, options)
	if hasAddresses(responses) || len(responses) == len(types) {
		return ipResult{responses: responses, tag: child.Tag()}
	}
	results := make(chan result, len(types))
	t.gate.Lock()
	if t.closed {
		t.gate.Unlock()
		return ipResult{err: context.Canceled}
	}
	t.workers.Add(len(types))
	t.gate.Unlock()
	for _, qType := range types {
		q := typedQuery(query, qType)
		go func(q *mdns.Msg) {
			defer t.workers.Done()
			r, err := exchangeProvider(ctx, child, q)
			if err == nil && !valid(q, r) {
				err = fmt.Errorf("invalid IP DNS response")
			}
			if err == nil {
				t.cache().CacheResponse(child, q, r, options)
			}
			results <- result{response: r, err: err, tag: child.Tag()}
		}(q)
	}
	for range types {
		select {
		case <-ctx.Done():
			return ipResult{err: ctx.Err()}
		case r := <-results:
			if r.err == nil {
				responses[r.response.Question[0].Qtype] = r.response
				if hasAddresses(responses) || len(responses) == len(types) {
					return ipResult{responses: responses, tag: child.Tag()}
				}
			}
		}
	}
	// Android IP clients wait for their stage deadline when a requested family
	// has no cache record, even if the underlying HTTP request already failed.
	<-ctx.Done()
	return ipResult{err: ctx.Err()}
}
