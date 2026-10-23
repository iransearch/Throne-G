package vpndns

import (
	"ThroneCore/internal/netdiag"
	"fmt"
	"sync"
	"time"
)

// No domain, answer, endpoint, user tag or raw error enters these diagnostics.
// Keep complete counters, but emit only the first 32 results and every 128th.
type raceDiagnostics struct {
	once     sync.Once
	close    sync.Once
	mu       sync.Mutex
	id       uint64
	closed   bool
	count    uint64
	failures uint64
	wins     [5]uint64
	cached   uint64
}

func (t *Transport) diagnosticBox() uint64 {
	if t.ctx == nil {
		return 0
	}
	return netdiag.Box(t.ctx)
}

func (t *Transport) initDiagnostics() {
	t.diagnostics.once.Do(func() {
		t.diagnostics.mu.Lock()
		defer t.diagnostics.mu.Unlock()
		if t.diagnostics.closed {
			return
		}
		t.diagnostics.id = netdiag.ID()
		netdiag.Emit("dns-race-created", t.diagnosticBox(), fmt.Sprintf("dns=%d count=%d timeoutMs=%d fallbackTimeoutMs=%d current=%t",
			t.diagnostics.id, len(t.children), t.timeout.Milliseconds(), t.fallbackTimeout.Milliseconds(), t.fallback != nil))
	})
}

func (t *Transport) recordResult(tag, source string, started time.Time, err error) {
	t.initDiagnostics()
	d := &t.diagnostics
	index := -1
	if tag != "" {
		for i, child := range t.children {
			if child.Tag() == tag {
				index = i
				break
			}
		}
		if t.fallback != nil && t.fallback.Tag() == tag {
			index = 4
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return
	}
	d.count++
	if err != nil {
		d.failures++
	} else if index >= 0 && index < len(d.wins) {
		d.wins[index]++
		if source == "ip-cache" {
			d.cached++
		}
	}
	if d.count <= 32 || d.count%128 == 0 {
		netdiag.Emit("dns-race-result", t.diagnosticBox(), d.counters()+fmt.Sprintf(" index=%d source=%s elapsedMs=%d error=%s",
			index, source, time.Since(started).Milliseconds(), netdiag.ErrorKind(err)))
	}
}

// Caller holds mu so the sampled result and cumulative totals describe one point.
func (d *raceDiagnostics) counters() string {
	return fmt.Sprintf("dns=%d count=%d wins0=%d wins1=%d wins2=%d wins3=%d wins4=%d cached=%d failures=%d",
		d.id, d.count, d.wins[0], d.wins[1], d.wins[2], d.wins[3], d.wins[4], d.cached, d.failures)
}

func (t *Transport) closeDiagnostics() {
	t.diagnostics.close.Do(func() {
		// Close can also run during an unsuccessful constructor.
		t.diagnostics.mu.Lock()
		defer t.diagnostics.mu.Unlock()
		t.diagnostics.closed = true
		if t.diagnostics.id != 0 {
			netdiag.Emit("dns-race-summary", t.diagnosticBox(), t.diagnostics.counters())
		}
	})
}
