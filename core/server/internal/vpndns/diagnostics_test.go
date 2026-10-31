package vpndns

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	dns "ThroneCore/internal/dnsengine"
)

func TestDiagnosticWinnerAndCacheCounters(t *testing.T) {
	for _, count := range []int{2, 4, 7} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			tr := fixture(t)
			tags := []string{}
			for i := 0; i < count; i++ {
				tr.children = append(tr.children, &fake{TransportAdapter: dns.NewTransportAdapter("https", fmt.Sprint(i), nil)})
				tags = append(tags, fmt.Sprint(i))
			}
			tr.fallback = &fake{TransportAdapter: dns.NewTransportAdapter("tcp", "fallback", nil)}
			for _, tag := range append(tags, "fallback") {
				tr.recordResult(tag, "raw-network", time.Now(), nil)
				tr.recordResult(tag, "ip-cache", time.Now(), nil)
			}
			tr.recordResult("", "ip-network", time.Now(), errors.New("failed private.example 203.0.113.8 secret"))
			d := &tr.diagnostics
			d.mu.Lock()
			defer d.mu.Unlock()
			want := [MaxPrimaryServers + 1]uint64{}
			for i := 0; i <= count; i++ {
				want[i] = 2
			}
			if d.count != uint64(2*(count+1)+1) || d.failures != 1 || d.cached != uint64(count+1) || d.wins != want {
				t.Fatalf("wrong complete winner counters: %s", d.counters())
			}
			if strings.Contains(d.counters(), "private") || strings.Contains(d.counters(), "203.0.113") {
				t.Fatal("private data in result counters")
			}
		})
	}
}

func TestDiagnosticConcurrentResultsAndClose(t *testing.T) {
	tr := fixture(t)
	tr.children = append(tr.children, &fake{TransportAdapter: dns.NewTransportAdapter("https", "0", nil)})
	var done sync.WaitGroup
	for range 16 {
		done.Go(func() {
			for range 100 {
				tr.recordResult("0", "ip-network", time.Now(), nil)
			}
		})
	}
	done.Wait()
	if tr.diagnostics.count != 1600 || tr.diagnostics.wins[0] != 1600 {
		t.Fatal("sampling lost cumulative results")
	}
	done.Go(tr.closeDiagnostics)
	done.Go(func() { tr.recordResult("", "raw-network", time.Now(), context.Canceled) })
	done.Wait()
	count := tr.diagnostics.count
	tr.closeDiagnostics()
	tr.recordResult("0", "ip-cache", time.Now(), nil)
	if tr.diagnostics.count != count {
		t.Fatal("diagnostics continued after close")
	}
}
