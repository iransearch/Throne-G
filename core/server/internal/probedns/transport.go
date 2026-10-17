// Package probedns supplies a composite transport for isolated server probes.
// Pinning dns-direct (including Xray's resolver) must still race all providers.
package probedns

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"

	mdns "github.com/miekg/dns"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/dns"
	"github.com/sagernet/sing-box/dns/transport"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
)

const Type = "https-race"
const timeout = 4 * time.Second

type Options struct {
	Servers []option.RemoteHTTPSDNSServerOptions `json:"servers"`
}

func Register(registry *dns.TransportRegistry) { dns.RegisterTransport[Options](registry, Type, New) }

type Transport struct {
	dns.TransportAdapter
	children []adapter.DNSTransport
}

func New(ctx context.Context, logger log.ContextLogger, tag string, options Options) (adapter.DNSTransport, error) {
	if len(options.Servers) < 2 || len(options.Servers) > 4 {
		return nil, errors.New("https-race requires 2 to 4 DoH servers")
	}
	t := &Transport{TransportAdapter: dns.NewTransportAdapter(Type, tag, nil)}
	for i, o := range options.Servers {
		// Literal endpoints and direct sockets avoid bootstrap DNS and proxy loops.
		if _, err := netip.ParseAddr(o.Server); err != nil || o.Detour != "" || o.DomainResolver != nil || o.TLS == nil || !o.TLS.Enabled || o.TLS.Insecure || o.TLS.ServerName == "" {
			t.Close()
			return nil, fmt.Errorf("https-race server %d requires a literal IP, direct routing and verified TLS/SNI", i)
		}
		child, err := transport.NewHTTPS(ctx, logger, fmt.Sprintf("%s-%d", tag, i), o)
		if err != nil {
			t.Close()
			return nil, fmt.Errorf("https-race server %d: %w", i, err)
		}
		t.children = append(t.children, child)
	}
	return t, nil
}
func (t *Transport) Start(stage adapter.StartStage) error {
	for _, child := range t.children {
		if err := child.Start(stage); err != nil {
			return err
		}
	}
	return nil
}
func (t *Transport) Close() error {
	var errs []error
	for _, child := range t.children {
		errs = append(errs, child.Close())
	}
	return errors.Join(errs...)
}
func (t *Transport) Reset() {
	for _, child := range t.children {
		child.Reset()
	}
}

func valid(query, response *mdns.Msg) bool {
	if response == nil || !response.Response || response.Truncated || response.Opcode != query.Opcode || len(response.Question) != len(query.Question) {
		return false
	}
	if response.Rcode != mdns.RcodeSuccess && response.Rcode != mdns.RcodeNameError {
		return false
	}
	for i, q := range query.Question {
		r := response.Question[i]
		if !strings.EqualFold(q.Name, r.Name) || q.Qclass != r.Qclass || q.Qtype != r.Qtype {
			return false
		}
	}
	return true // NODATA and NXDOMAIN are valid negative answers.
}
func (t *Transport) Exchange(ctx context.Context, message *mdns.Msg) (*mdns.Msg, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel() // No Wait/Join: slow losers cannot delay the caller.
	type result struct {
		response *mdns.Msg
		err      error
	}
	results := make(chan result, len(t.children))
	for _, child := range t.children {
		query := message.Copy()
		go func(child adapter.DNSTransport) {
			response, err := child.Exchange(ctx, query)
			if err == nil && !valid(query, response) {
				err = errors.New("invalid DoH response")
			}
			results <- result{response, err} // Buffered even after the winner returns.
		}(child)
	}
	var errs []error
	for range t.children {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case r := <-results:
			if r.err == nil {
				r.response.Id = message.Id
				return r.response, nil
			}
			errs = append(errs, r.err)
		}
	}
	return nil, fmt.Errorf("all probe DoH servers failed: %w", errors.Join(errs...))
}
func (t *Transport) ExchangeAsync(ctx context.Context, message *mdns.Msg, callback func(*mdns.Msg, error)) {
	go func() { response, err := t.Exchange(ctx, message); callback(response, err) }()
}
