package autoselector

import (
	"context"
	"errors"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/common/interrupt"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"net"
	"sync"
	"testing"
	"time"
)

type trackedConn struct {
	net.Conn
	closed bool
}

func (c *trackedConn) Close() error { c.closed = true; return nil }

type testOutbound struct {
	outbound.Adapter
	fail        bool
	attempts    int
	connections []*trackedConn
}

func (o *testOutbound) DialContext(context.Context, string, M.Socksaddr) (net.Conn, error) {
	o.attempts++
	if o.fail {
		return nil, errors.New("remote dial failure")
	}
	c := &trackedConn{}
	o.connections = append(o.connections, c)
	return c, nil
}
func (o *testOutbound) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, errors.New("unused")
}
func fixture() *AutoSelector {
	s := &AutoSelector{ctx: context.Background(), balance: true, balanceMode: BalanceModeRoundRobin,
		qualified: []string{"a", "b", "c"}, outbounds: make(map[string]adapter.Outbound), members: make(map[string]*memberHealth),
		recentDialFails: make(map[string]time.Time), interruptGroup: interrupt.NewGroup(), logger: log.NewNOPFactory().NewLogger("test"),
		kick: make(chan struct{}, 1), dialRetries: 2, started: true}
	for _, tag := range s.qualified {
		s.outbounds[tag] = &testOutbound{Adapter: outbound.NewAdapter("test", tag, []string{N.NetworkTCP, N.NetworkUDP}, nil)}
		s.members[tag] = newMemberHealth(tag, 10, time.Hour)
	}
	s.selectedTCP.Store(s.outbounds["a"])
	s.selectedUDP.Store(s.outbounds["a"])
	return s
}
func TestRoundRobinOrderAndNetworkIndependence(t *testing.T) {
	s := fixture()
	for _, want := range []string{"a", "b", "c", "a", "b", "c"} {
		if got := s.pick(N.NetworkTCP, nil).Tag(); got != want {
			t.Fatalf("want %s got %s", want, got)
		}
	}
	if got := s.pick(N.NetworkUDP, nil).Tag(); got != "a" {
		t.Fatalf("UDP cursor advanced by TCP: %s", got)
	}
}
func TestRoundRobinSkipsCooldownTriedAndUnsupported(t *testing.T) {
	s := fixture()
	s.members["a"].cooldownUntil = time.Now().Add(time.Minute)
	if got := s.pick(N.NetworkTCP, map[string]bool{"b": true}).Tag(); got != "c" {
		t.Fatal(got)
	}
	s.outbounds["b"] = &testOutbound{Adapter: outbound.NewAdapter("test", "b", []string{N.NetworkTCP}, nil)}
	if got := s.pick(N.NetworkUDP, nil).Tag(); got != "c" {
		t.Fatalf("unsupported UDP member: %s", got)
	}
}
func TestRoundRobinMembershipChangesAndRecovery(t *testing.T) {
	s := fixture()
	if s.pick(N.NetworkTCP, nil).Tag() != "a" {
		t.Fatal("first pick")
	}
	s.qualified = []string{"c", "a", "b"}
	if s.pick(N.NetworkTCP, nil).Tag() != "b" {
		t.Fatal("ranking change reset cursor")
	}
	s.qualified = []string{"c"}
	if s.pick(N.NetworkTCP, nil).Tag() != "c" {
		t.Fatal("removed last member")
	}
	s.qualified = []string{"a", "b", "c"}
	if s.pick(N.NetworkTCP, nil).Tag() != "a" {
		t.Fatal("recovered members did not rejoin")
	}
}
func TestRoundRobinPinAndDisabledBalance(t *testing.T) {
	s := fixture()
	s.pinnedTag = "b"
	s.selectedTCP.Store(s.outbounds["b"])
	for i := 0; i < 3; i++ {
		if s.pick(N.NetworkTCP, nil).Tag() != "b" {
			t.Fatal("pin ignored")
		}
	}
	s.pinnedTag = ""
	s.balance = false
	if s.pick(N.NetworkTCP, nil).Tag() != "b" {
		t.Fatal("disabled balance rotates")
	}
}
func TestRoundRobinConcurrentDistribution(t *testing.T) {
	s := fixture()
	counts := make(map[string]int)
	var lock sync.Mutex
	var workers sync.WaitGroup
	for i := 0; i < 300; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			tag := s.pick(N.NetworkTCP, nil).Tag()
			lock.Lock()
			counts[tag]++
			lock.Unlock()
		}()
	}
	workers.Wait()
	for _, tag := range []string{"a", "b", "c"} {
		if counts[tag] != 100 {
			t.Fatalf("distribution: %v", counts)
		}
	}
}
func TestRoundRobinDialsKeepExistingFlows(t *testing.T) {
	s := fixture()
	for i := 0; i < 6; i++ {
		conn, err := s.DialContext(context.Background(), N.NetworkTCP, M.Socksaddr{})
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
	}
	for _, o := range s.outbounds {
		f := o.(*testOutbound)
		if f.attempts != 2 {
			t.Fatalf("%s received %d dials", f.Tag(), f.attempts)
		}
		for _, conn := range f.connections {
			if conn.closed {
				t.Fatal("balancing closed existing flow")
			}
		}
	}
}
func TestRoundRobinRetryPenalizesFailedMember(t *testing.T) {
	s := fixture()
	s.outbounds["a"].(*testOutbound).fail = true
	conn, err := s.DialContext(context.Background(), N.NetworkTCP, M.Socksaddr{})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if s.members["a"].dialFail != 1 || !s.members["a"].inCooldown(time.Now()) {
		t.Fatal("failed member not penalized")
	}
	if s.outbounds["b"].(*testOutbound).attempts != 1 {
		t.Fatal("next healthy member not retried")
	}
	if s.pick(N.NetworkTCP, nil).Tag() != "c" || s.pick(N.NetworkTCP, nil).Tag() != "b" {
		t.Fatal("failed member kept receiving dials")
	}
}

func TestExplicitTypeDefaultsAndRejectsOtherModes(t *testing.T) {
	logger := log.NewNOPFactory().NewLogger("test")
	out, err := NewAutoSelector(context.Background(), nil, logger, "proxy", option.AutoSelectorOutboundOptions{Outbounds: []string{"a"}, Balance: true})
	if err != nil {
		t.Fatal(err)
	}
	selector := out.(*AutoSelector)
	defer selector.Close()
	if selector.Type() != TypeAutoSelectorRoundRobin || selector.balanceMode != BalanceModeRoundRobin {
		t.Fatal("explicit type silently used a different algorithm")
	}
	_, err = NewAutoSelector(context.Background(), nil, logger, "proxy", option.AutoSelectorOutboundOptions{Outbounds: []string{"a"}, BalanceMode: "rotate"})
	if err == nil {
		t.Fatal("contradictory mode accepted")
	}
}
