package dns

import (
	"context"
	"net/netip"
	"sync"

	mdns "github.com/miekg/dns"
	"github.com/sagernet/sing-box/adapter"
)

// ManagedTransport owns per-provider IP caches. The client must not put a
// winner-only cache in front of it or turn raw requests into cached IP lookups.
type ManagedTransport interface {
	adapter.DNSTransport
	DNSExchange(context.Context, *mdns.Msg, adapter.DNSQueryOptions) (*mdns.Msg, error)
	DNSLookup(context.Context, string, adapter.DNSQueryOptions) ([]netip.Addr, error)
	DNSClearCache()
}

type lookupContextKey struct{}

// LookupContext keeps A/AAAA in one logical provider race, including modern
// rule-based lookups which otherwise issue the families as separate exchanges.
type LookupContext struct {
	Context context.Context
	Types   []uint16
	Results sync.Map
}

func LookupContextFrom(ctx context.Context) *LookupContext {
	v, _ := ctx.Value(lookupContextKey{}).(*LookupContext)
	return v
}

func ContextWithLookup(ctx context.Context, types []uint16) context.Context {
	return context.WithValue(ctx, lookupContextKey{}, &LookupContext{Context: ctx, Types: types})
}

func (c *Client) PrepareQuery(message *mdns.Msg, options adapter.DNSQueryOptions) *mdns.Msg {
	return c.prepareExchangeMessage(message, options)
}

// CachedResponse is a read-only TTL check. It never launches a query, waits on
// an in-flight request, or refreshes optimistic/stale entries.
func (c *Client) CachedResponse(transport adapter.DNSTransport, message *mdns.Msg, options adapter.DNSQueryOptions) (*mdns.Msg, bool) {
	if c.disableCache || options.DisableCache || len(message.Question) != 1 {
		return nil, false
	}
	response, _, stale := c.loadResponse(c.newCacheKey(transport, message.Question[0], message, options))
	if response == nil || stale {
		return nil, false
	}
	response.Id = message.Id
	return response, true
}

// CacheResponse records a provider's completed IP query without putting a
// composite-winner entry in front of other providers. TTL=0 is never stored.
func (c *Client) CacheResponse(transport adapter.DNSTransport, query, response *mdns.Msg, options adapter.DNSQueryOptions) {
	if c.disableCache || options.DisableCache || len(query.Question) != 1 || response == nil || (response.Rcode != mdns.RcodeSuccess && response.Rcode != mdns.RcodeNameError) {
		return
	}
	response = response.Copy()
	ttl := applyResponseOptions(query.Question[0], response, options)
	key, ok := c.finishCacheKey(transport, c.newCacheKey(transport, query.Question[0], query, options))
	if ok {
		c.storeCache(key, response, ttl)
	}
}
