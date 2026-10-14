package autoselector

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter/outbound"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"
	"github.com/sagernet/sing/service/pause"
)

type recoveryOutbound struct {
	outbound.Adapter
	address string
	broken  atomic.Bool
	dials   atomic.Int32
}

func (o *recoveryOutbound) DialContext(ctx context.Context, _ string, _ M.Socksaddr) (net.Conn, error) {
	o.dials.Add(1)
	if o.broken.Load() {
		return nil, errors.New("stale external session")
	}
	return (&net.Dialer{}).DialContext(ctx, "tcp", o.address)
}
func (o *recoveryOutbound) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, errors.New("unused")
}

func TestBridgeSuccessDoesNotEraseExternalFailure(t *testing.T) {
	s := fixture()
	s.outbounds["a"] = &testOutbound{Adapter: outbound.NewAdapter("socks", "a", []string{N.NetworkTCP, N.NetworkUDP}, nil)}
	h := s.members["a"]
	now := time.Now()
	h.put(rttFailed, now)
	h.penalize(now, dialBackoff, errors.New("external failure"))
	s.recentDialFails["a"] = now
	s.noteDialResult(s.outbounds["a"], N.NetworkTCP, nil)
	if h.dialFail != 1 || !h.inCooldown(time.Now()) || h.lastDialErr == "" || !h.lastOK.IsZero() {
		t.Fatal("local SOCKS acceptance rewarded a failed external path")
	}
	if _, ok := s.recentDialFails["a"]; !ok {
		t.Fatal("outage evidence erased")
	}
	delete(s.outbounds, "b")
	delete(s.outbounds, "c")
	if s.pick(N.NetworkTCP, nil) != nil {
		t.Fatal("tested-dead SOCKS bridge remains a fallback")
	}
	if s.pick(N.NetworkUDP, nil) != nil {
		t.Fatal("tested-dead bridge accepts UDP associations")
	}
	h.put(10*time.Millisecond, time.Now())
	h.clearCooldown()
	if s.pick(N.NetworkTCP, nil) == nil {
		t.Fatal("fresh external proof cannot restore member")
	}
}

func TestNetworkReturnRepairsOnlyFailedMembersAndRequiresProof(t *testing.T) {
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer web.Close()
	s := fixture()
	s.tags = []string{"a", "b", "c"}
	s.link = web.URL
	s.probeTimeout = time.Second
	s.concurrency = 2
	s.activeSize = 3
	s.expected = 3
	s.failTolerance = .2
	s.interval = time.Minute
	s.benchInterval = time.Minute
	s.pause = service.FromContext[pause.Manager](pause.WithDefaultManager(s.ctx))
	for _, tag := range s.tags {
		o := &recoveryOutbound{Adapter: outbound.NewAdapter("socks", tag, []string{N.NetworkTCP}, nil), address: web.Listener.Addr().String()}
		o.broken.Store(tag != "a")
		s.outbounds[tag] = o
	}
	counts := map[string]int{}
	s.SetMemberRecovery(func(tag string) bool {
		counts[tag]++
		if tag == "b" {
			s.outbounds[tag].(*recoveryOutbound).broken.Store(false)
		}
		return true
	})
	s.evaluateLocked(time.Now())
	// No network-return event means ordinary server failures cannot reset it.
	s.runRound(true)
	if len(counts) != 0 {
		t.Fatal("normal failure triggered a reset")
	}
	s.InterfaceUpdated(context.Background())
	// Stale-session failures recorded after return must not quarantine a repaired member.
	for i := 0; i < 6; i++ {
		s.members["b"].put(rttFailed, time.Now())
	}
	s.runRound(true)
	if counts["a"] != 0 || counts["b"] != 1 || counts["c"] != 1 {
		t.Fatalf("reset healthy/wrong members: %v", counts)
	}
	stats := s.members["b"].stats(time.Now())
	if !stats.Alive() || stats.Fail != 0 {
		t.Fatalf("repair lost proof or retained stale-session failures: %+v", stats)
	}
	qualified := false
	for _, tag := range s.qualified {
		qualified = qualified || tag == "b"
	}
	if !qualified {
		t.Fatal("externally proven repaired member remains excluded by old failures")
	}
	if s.members["c"].stats(time.Now()).Alive() {
		t.Fatal("reset without external proof declared a member healthy")
	}
	s.runRound(true)
	if counts["c"] != 1 {
		t.Fatal("unreachable server caused repeated reset")
	}
	s.pause.NetworkPause()
	before := s.outbounds["a"].(*recoveryOutbound).dials.Load()
	s.runRound(true)
	if s.outbounds["a"].(*recoveryOutbound).dials.Load() != before {
		t.Fatal("health probing continued during network pause")
	}
	s.pause.NetworkWake()
	s.InterfaceUpdated(context.Background())
	s.recoverFailedResults([]probeResult{{tag: "c", value: rttFailed, err: syscall.ENETDOWN, epoch: s.probeEpoch}})
	if counts["c"] != 1 {
		t.Fatal("local network error reset a remote member")
	}
	s.recoveryUntil = time.Now().Add(-time.Second)
	s.recoverFailedResults([]probeResult{{tag: "c", value: rttFailed, err: errors.New("remote"), epoch: s.probeEpoch}})
	if counts["c"] != 1 {
		t.Fatal("expired recovery budget reset a remote member")
	}
}
