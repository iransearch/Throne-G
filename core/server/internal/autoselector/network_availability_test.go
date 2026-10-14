package autoselector

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter/outbound"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"
	"github.com/sagernet/sing/service/pause"
)

func waitAvailability(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("immediate recovery did not finish before the normal interval")
}

func availabilityFixture(t *testing.T) *AutoSelector {
	s := fixture()
	s.ctx, s.cancel = context.WithCancel(context.Background())
	t.Cleanup(s.cancel)
	s.close = make(chan struct{})
	s.tags = []string{"a", "b", "c"}
	s.activeSize = 3
	s.expected = 3
	s.concurrency = 2
	s.failTolerance = .2
	s.probeTimeout = time.Second
	s.interval = time.Hour
	s.benchInterval = time.Hour
	s.pause = service.FromContext[pause.Manager](pause.WithDefaultManager(s.ctx))
	s.evaluateLocked(time.Now())
	return s
}

type countedRecoveryConn struct {
	net.Conn
	once   sync.Once
	active *atomic.Int32
}

func (c *countedRecoveryConn) Close() error {
	c.once.Do(func() { c.active.Add(-1) })
	return c.Conn.Close()
}

type countedRecoveryOutbound struct {
	outbound.Adapter
	address      string
	active, peak *atomic.Int32
	dials        atomic.Int32
	broken       atomic.Bool
}

func (o *countedRecoveryOutbound) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, errors.New("unused in TCP recovery test")
}

func (o *countedRecoveryOutbound) DialContext(ctx context.Context, _ string, _ M.Socksaddr) (net.Conn, error) {
	o.dials.Add(1)
	if o.broken.Load() {
		return nil, errors.New("stale transport")
	}
	c, err := (&net.Dialer{}).DialContext(ctx, "tcp", o.address)
	if err != nil {
		return nil, err
	}
	current := o.active.Add(1)
	for old := o.peak.Load(); current > old; old = o.peak.Load() {
		if o.peak.CompareAndSwap(old, current) {
			break
		}
	}
	return &countedRecoveryConn{Conn: c, active: o.active}, nil
}

func TestWanReturnSweepsImmediatelyPublishesFirstSuccessAndCoalesces(t *testing.T) {
	s := availabilityFixture(t)
	fast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer fast.Close()
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
			w.WriteHeader(204)
		case <-r.Context().Done():
		}
	}))
	defer slow.Close()
	defer close(release)
	s.link = fast.URL
	var active, peak atomic.Int32
	for _, tag := range s.tags {
		address := slow.Listener.Addr().String()
		if tag == "a" {
			address = fast.Listener.Addr().String()
		}
		s.outbounds[tag] = &countedRecoveryOutbound{Adapter: outbound.NewAdapter("socks", tag, []string{N.NetworkTCP}, nil), address: address, active: &active, peak: &peak}
		for i := 0; i < 10; i++ {
			s.members[tag].put(rttFailed, time.Now().Add(-10*time.Second))
		}
	}
	s.evaluateLocked(time.Now())
	s.NetworkUnavailable()
	s.runRound(true)
	s.CheckOutbounds()
	for _, tag := range s.tags {
		if s.outbounds[tag].(*countedRecoveryOutbound).dials.Load() != 0 {
			t.Fatal("probed during WAN outage")
		}
	}
	if !s.Status().Suspended {
		t.Fatal("WAN outage did not suspend without OS events")
	}
	start := time.Now()
	s.NetworkReturned()
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("availability RPC waited for sweep")
	}
	waitAvailability(t, func() bool { return s.Status().MembersQualified == 1 })
	status := s.Status()
	if status.ProbesInFlight == 0 {
		t.Fatal("first success was withheld until dead peers finished")
	}
	for i := 0; i < 50; i++ {
		s.NetworkReturned()
		s.CheckOutbounds()
	}
	if s.outbounds["a"].(*countedRecoveryOutbound).dials.Load() != 1 {
		t.Fatal("duplicate signals launched overlapping sweeps")
	}
	if peak.Load() > 2 {
		t.Fatal("urgent sweep exceeded concurrency limit")
	}
	// A second outage cancels the two blocked peers and rejects their late results.
	s.NetworkUnavailable()
	waitAvailability(t, func() bool { s.access.Lock(); defer s.access.Unlock(); return !s.checkPending })
	status = s.Status()
	if !status.Suspended || status.ProbesInFlight != 0 {
		t.Fatal("cancelled sweep survived WAN outage")
	}
	if s.members["b"].probes != 10 || s.members["c"].probes != 10 {
		t.Fatal("cancelled peer became a server failure/success")
	}
	if active.Load() != 0 {
		t.Fatal("cancelled probes retained connections")
	}
}

func TestStaleMemberIsRepairedBeforeDeadPeersFinish(t *testing.T) {
	s := availabilityFixture(t)
	fast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer fast.Close()
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
			w.WriteHeader(204)
		case <-r.Context().Done():
		}
	}))
	defer slow.Close()
	defer close(release)
	s.link = fast.URL
	var active, peak, resets atomic.Int32
	for _, tag := range s.tags {
		address := slow.Listener.Addr().String()
		if tag == "a" {
			address = fast.Listener.Addr().String()
		}
		o := &countedRecoveryOutbound{Adapter: outbound.NewAdapter("socks", tag, []string{N.NetworkTCP}, nil), address: address, active: &active, peak: &peak}
		o.broken.Store(tag == "a")
		s.outbounds[tag] = o
	}
	s.SetMemberRecovery(func(tag string) bool {
		if tag != "a" {
			return false
		}
		resets.Add(1)
		s.outbounds[tag].(*countedRecoveryOutbound).broken.Store(false)
		return true
	})
	s.NetworkUnavailable()
	s.NetworkReturned()
	waitAvailability(t, func() bool { return s.Status().MembersQualified == 1 })
	if resets.Load() != 1 || s.Status().ProbesInFlight == 0 {
		t.Fatal("stale client repair waited for all failed peers")
	}
	if peak.Load() > 2 {
		t.Fatal("inline repair exceeded pool concurrency")
	}
	s.NetworkUnavailable()
	waitAvailability(t, func() bool { s.access.Lock(); defer s.access.Unlock(); return !s.checkPending })
}

func TestRecoveryPreservesEarlierServerFailuresAndCleansOnlyOnce(t *testing.T) {
	s := availabilityFixture(t)
	h := s.members["a"]
	old := time.Now().Add(-2 * time.Minute)
	h.put(rttFailed, old)
	for i := 0; i < 6; i++ {
		h.put(rttFailed, time.Now())
	}
	s.NetworkUnavailable()
	// Supply the same production return state without launching HTTP workers.
	s.access.Lock()
	s.externalNetworkOffline = false
	s.suspended = false
	s.recoveryReturnedAt = time.Now()
	s.recoveryUntil = time.Now().Add(time.Minute)
	epoch := s.probeEpoch
	s.access.Unlock()
	result := probeResult{tag: "a", value: 10 * time.Millisecond, at: time.Now(), epoch: epoch}
	if !s.recordRecoverySuccess(result) {
		t.Fatal("fresh external proof was not published")
	}
	stats := h.stats(time.Now())
	if stats.Fail != 1 || stats.All != 2 {
		t.Fatalf("erased earlier server failure or retained outage failures: %+v", stats)
	}
	h.put(rttFailed, time.Now())
	result.at = time.Now()
	if s.recordRecoverySuccess(result) || h.stats(time.Now()).Fail != 2 {
		t.Fatal("later success repeatedly erased new server failures")
	}
	// A delayed round belonging to the old network cannot mutate this ranking.
	s.NetworkUnavailable()
	before := h.probes
	s.commitRound([]probeResult{result}, time.Now())
	if h.probes != before || !s.Status().Suspended {
		t.Fatal("late success overwrote another outage")
	}
}

func TestImmediateSweepPreemptsScheduledIntervalJitter(t *testing.T) {
	s := availabilityFixture(t)
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer web.Close()
	s.link = web.URL
	var active, peak atomic.Int32
	for _, tag := range s.tags {
		s.outbounds[tag] = &countedRecoveryOutbound{Adapter: outbound.NewAdapter("socks", tag, []string{N.NetworkTCP}, nil), address: web.Listener.Addr().String(), active: &active, peak: &peak}
	}
	normalDone := make(chan struct{})
	go func() { s.runRound(false); close(normalDone) }()
	waitAvailability(t, func() bool {
		if s.roundAccess.TryLock() {
			s.roundAccess.Unlock()
			return false
		}
		return true
	})
	s.NetworkReturned()
	waitAvailability(t, func() bool {
		select {
		case <-normalDone:
			return true
		default:
			return false
		}
	})
	waitAvailability(t, func() bool { return s.Status().MembersAlive == 3 })
}
