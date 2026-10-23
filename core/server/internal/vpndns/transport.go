// Package vpndns implements the Android-style first-completed VPN DNS race.
// Raw forwarding bypasses IP caches; IP lookups cache each provider separately.
package vpndns

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"time"

	dns "ThroneCore/internal/dnsengine"
	mdns "github.com/miekg/dns"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/dns/transport"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json/badoption"
)

const Type = "vpn-dns-race"

type Options struct {
	Servers         []option.DNSServerOptions `json:"servers"`
	Fallback        *option.DNSServerOptions  `json:"fallback,omitempty"`
	Timeout         badoption.Duration        `json:"timeout,omitempty"`
	FallbackTimeout badoption.Duration        `json:"fallback_timeout,omitempty"`
}

func Register(registry *dns.TransportRegistry) { dns.RegisterTransport[Options](registry, Type, New) }

var _ adapter.DNSTransport = (*Transport)(nil)

type Transport struct {
	dns.TransportAdapter
	logger          log.ContextLogger
	children        []adapter.DNSTransport
	fallback        adapter.DNSTransport
	timeout         time.Duration
	fallbackTimeout time.Duration
	ctx             context.Context
	cancel          context.CancelFunc
	gate            sync.Mutex
	closed          bool
	workers         sync.WaitGroup
	ipOnce          sync.Once
	ipClient        *dns.Client
	diagnostics     raceDiagnostics
}

func New(ctx context.Context, logger log.ContextLogger, tag string, options Options) (adapter.DNSTransport, error) {
	if len(options.Servers) < 2 || len(options.Servers) > 4 {
		return nil, errors.New("vpn-dns-race requires 2 to 4 primary servers")
	}
	t := &Transport{TransportAdapter: dns.NewTransportAdapter(Type, tag, nil), logger: logger,
		timeout: time.Duration(options.Timeout), fallbackTimeout: time.Duration(options.FallbackTimeout)}
	t.ctx, t.cancel = context.WithCancel(ctx)
	if t.timeout == 0 {
		t.timeout = 8 * time.Second
	}
	if t.fallbackTimeout == 0 {
		t.fallbackTimeout = 4 * time.Second
	}
	if t.timeout <= 0 || t.fallbackTimeout <= 0 {
		t.cancel()
		return nil, errors.New("vpn-dns-race timeouts must be positive")
	}
	var detour string
	for i, o := range options.Servers {
		child, via, err := newChild(ctx, logger, fmt.Sprintf("%s-primary-%d", tag, i), o, false)
		if err != nil {
			t.Close()
			return nil, err
		}
		t.children = append(t.children, child)
		if i == 0 {
			detour = via
		}
		if via != detour {
			t.Close()
			return nil, errors.New("vpn-dns-race servers must share one VPN detour")
		}
	}
	if options.Fallback != nil {
		child, via, err := newChild(ctx, logger, tag+"-fallback", *options.Fallback, true)
		if err != nil {
			t.Close()
			return nil, err
		}
		t.fallback = child
		if via != detour {
			t.Close()
			return nil, errors.New("vpn-dns-race fallback must use the same VPN detour")
		}
	}
	t.initDiagnostics()
	return t, nil
}

func newChild(ctx context.Context, logger log.ContextLogger, tag string, o option.DNSServerOptions, fallback bool) (adapter.DNSTransport, string, error) {
	var remote option.RemoteDNSServerOptions
	var doh *option.RemoteHTTPSDNSServerOptions
	switch o.Type {
	case "https":
		var ok bool
		doh, ok = o.Options.(*option.RemoteHTTPSDNSServerOptions)
		if !ok || doh == nil || fallback {
			return nil, "", errors.New("invalid vpn-dns-race DoH options")
		}
		remote = doh.RemoteDNSServerOptions
		if doh.TLS == nil || !doh.TLS.Enabled || doh.TLS.Insecure || doh.TLS.ServerName == "" || doh.Path != "/dns-query" {
			return nil, "", errors.New("vpn-dns-race requires verified DoH TLS/SNI and /dns-query")
		}
	case "udp", "tcp":
		v, ok := o.Options.(*option.RemoteDNSServerOptions)
		if !ok || v == nil || fallback != (o.Type == "tcp") {
			return nil, "", errors.New("vpn-dns-race requires UDP primary or TCP fallback")
		}
		remote = *v
	default:
		return nil, "", fmt.Errorf("unsupported vpn-dns-race transport: %s", o.Type)
	}
	if _, err := netip.ParseAddr(remote.Server); err != nil || remote.Detour == "" || remote.DomainResolver != nil {
		return nil, "", errors.New("vpn-dns-race requires literal endpoints through a VPN detour")
	}
	var child adapter.DNSTransport
	var err error
	switch o.Type {
	case "https":
		child, err = transport.NewHTTPS(ctx, logger, tag, *doh)
	case "udp":
		child, err = newUDP(ctx, tag, remote)
	case "tcp":
		child, err = newTCP(ctx, tag, remote)
	}
	return child, remote.Detour, err
}

func (t *Transport) all() []adapter.DNSTransport {
	children := append([]adapter.DNSTransport(nil), t.children...)
	if t.fallback != nil {
		children = append(children, t.fallback)
	}
	return children
}
func (t *Transport) Start(stage adapter.StartStage) error {
	for _, child := range t.all() {
		if err := child.Start(stage); err != nil {
			return err
		}
	}
	return nil
}
func (t *Transport) Close() error {
	defer t.closeDiagnostics()
	t.gate.Lock()
	t.closed = true
	if t.cancel != nil {
		t.cancel()
	}
	t.gate.Unlock()
	var errs []error
	for _, child := range t.all() {
		errs = append(errs, child.Close())
	}
	t.workers.Wait()
	return errors.Join(errs...)
}
func (t *Transport) Reset() {
	for _, child := range t.all() {
		child.Reset()
	}
}

func valid(query, response *mdns.Msg) bool {
	if response == nil || !response.Response || response.Truncated || response.Id != query.Id || response.Opcode != query.Opcode || len(response.Question) != len(query.Question) {
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
	return true // NOERROR (including NODATA) and NXDOMAIN are both valid.
}

type result struct {
	response *mdns.Msg
	err      error
	tag      string
}

// Results are queued in completion order, never scanned in provider order.
// A buffer for every worker also lets canceled/late losers exit after return.
func (t *Transport) raceStage(parent context.Context, message *mdns.Msg, children []adapter.DNSTransport, timeout time.Duration) (*mdns.Msg, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	results := make(chan result, len(children))
	t.gate.Lock()
	if t.closed || ctx.Err() != nil {
		t.gate.Unlock()
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return nil, errors.New("vpn-dns-race is closed")
	}
	t.workers.Add(len(children))
	t.gate.Unlock()
	for _, child := range children {
		query := message.Copy()
		go func(child adapter.DNSTransport) {
			defer t.workers.Done()
			response, err := exchangeProvider(ctx, child, query)
			if err == nil && !valid(message, response) {
				err = errors.New("invalid VPN DNS response")
			}
			results <- result{response, err, child.Tag()}
		}(child)
	}
	return t.receive(ctx, message, results, len(children))
}

// sing-box's DoH transport uses wire ID zero; validate that wire ID before
// restoring the caller ID, just as Android's per-client ID translation does.
func exchangeProvider(ctx context.Context, child adapter.DNSTransport, query *mdns.Msg) (*mdns.Msg, error) {
	wire := query.Copy()
	if child.Type() == "https" {
		wire.Id = 0
	}
	response, err := child.Exchange(ctx, wire)
	if err == nil && !valid(wire, response) {
		return nil, errors.New("invalid VPN DNS response")
	}
	if err == nil {
		response.Id = query.Id
	}
	return response, err
}

func (t *Transport) receive(ctx context.Context, message *mdns.Msg, results <-chan result, count int) (*mdns.Msg, error) {
	started := time.Now()
	var errs []error
	for i := 0; i < count; i++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case r := <-results:
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if r.err == nil {
				r.response.Id = message.Id
				t.recordResult(r.tag, "raw-network", started, nil)
				if t.logger != nil {
					t.logger.DebugContext(ctx, "VPN DNS winner=", r.tag, " elapsed_ms=", time.Since(started).Milliseconds())
				}
				return r.response, nil
			}
			errs = append(errs, r.err)
		}
	}
	return nil, fmt.Errorf("all VPN DNS servers failed: %w", errors.Join(errs...))
}

func (t *Transport) rawExchange(ctx context.Context, message *mdns.Msg) (response *mdns.Msg, err error) {
	started := time.Now()
	defer func() {
		if err != nil {
			t.recordResult("", "raw-network", started, err)
		}
	}()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if t.ctx != nil {
		stop := context.AfterFunc(t.ctx, cancel)
		defer stop()
	}
	response, err = t.raceStage(ctx, message, t.children, t.timeout)
	if err == nil {
		return response, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if t.fallback == nil {
		return nil, err
	}
	return t.raceStage(ctx, message, []adapter.DNSTransport{t.fallback}, t.fallbackTimeout)
}
func (t *Transport) Exchange(ctx context.Context, message *mdns.Msg) (*mdns.Msg, error) {
	return t.DNSExchange(ctx, message, adapter.DNSQueryOptions{})
}
func (t *Transport) ExchangeAsync(ctx context.Context, message *mdns.Msg, callback func(*mdns.Msg, error)) {
	go func() { response, err := t.Exchange(ctx, message); callback(response, err) }()
}
