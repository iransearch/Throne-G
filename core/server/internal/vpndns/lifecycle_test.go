package vpndns

import (
	"context"
	"errors"
	"testing"

	dns "ThroneCore/internal/dnsengine"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
)

type lifecycleChild struct {
	fake
	fail   bool
	closed int
}

func (c *lifecycleChild) Start(stage adapter.StartStage, scope *adapter.Scope) error {
	if stage == adapter.StartStateInitialize {
		scope.Add(func() error { c.closed++; return nil })
	}
	if stage == adapter.StartStateStart && c.fail {
		return errors.New("fixture start failure")
	}
	return nil
}

func TestScopeCleansEveryDNSChildAfterPartialStartup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	logger := log.NewNOPFactory().Logger()
	a := &lifecycleChild{fake: fake{TransportAdapter: dns.NewTransportAdapter("fake", "a", nil)}}
	b := &lifecycleChild{fake: fake{TransportAdapter: dns.NewTransportAdapter("fake", "b", nil)}, fail: true}
	tr := &Transport{ctx: ctx, cancel: cancel, children: []adapter.DNSTransport{a, b}, scope: adapter.NewScope(ctx, logger)}
	scope := adapter.NewScope(ctx, logger)
	defer scope.Close()
	if err := scope.Start("race", tr, adapter.StartStateInitialize); err != nil {
		t.Fatal(err)
	}
	if err := scope.Start("race", tr, adapter.StartStateStart); err == nil {
		t.Fatal("partial startup failure was lost")
	}
	if err := scope.Close(); err != nil {
		t.Fatal(err)
	}
	_ = tr.Close()
	if a.closed != 1 || b.closed != 1 || !tr.closed || ctx.Err() == nil {
		t.Fatalf("incomplete or repeated cleanup: a=%d b=%d closed=%v context=%v", a.closed, b.closed, tr.closed, ctx.Err())
	}
}
