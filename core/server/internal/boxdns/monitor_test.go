package boxdns

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDeferredStarterAsyncAndConcurrentStartRunOnce(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseStart := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseStart)
	wantErr := errors.New("start failed")
	var calls atomic.Int32

	starter := newDeferredStarter(func() error {
		calls.Add(1)
		close(started)
		<-release
		return wantErr
	})

	asyncReturned := make(chan struct{})
	go func() {
		starter.StartAsync()
		close(asyncReturned)
	}()
	select {
	case <-asyncReturned:
	case <-time.After(time.Second):
		t.Fatal("StartAsync blocked on initialization")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("StartAsync did not begin initialization")
	}

	const waiterCount = 32
	results := make(chan error, waiterCount)
	var ready sync.WaitGroup
	ready.Add(waiterCount)
	for range waiterCount {
		go func() {
			ready.Done()
			results <- starter.Start()
		}()
	}
	ready.Wait()
	starter.StartAsync()
	releaseStart()

	for range waiterCount {
		select {
		case err := <-results:
			if !errors.Is(err, wantErr) {
				t.Fatalf("Start() error = %v, want %v", err, wantErr)
			}
		case <-time.After(time.Second):
			t.Fatal("concurrent Start call did not join completed initialization")
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("start function called %d times, want 1", got)
	}
	if err := starter.Start(); !errors.Is(err, wantErr) {
		t.Fatalf("later Start() error = %v, want %v", err, wantErr)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("later Start repeated start function: calls = %d", got)
	}
}

func TestDeferredStarterNilReceiverAndNilStartComplete(t *testing.T) {
	var missing *deferredStarter
	if err := missing.Start(); err != nil {
		t.Fatalf("nil Start() error = %v, want nil", err)
	}
	missing.StartAsync()

	starter := newDeferredStarter(nil)
	if err := starter.Start(); err != nil {
		t.Fatalf("Start() error = %v, want nil", err)
	}
	starter.StartAsync()
}
