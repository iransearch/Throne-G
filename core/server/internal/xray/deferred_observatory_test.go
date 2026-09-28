package xray

import (
	"ThroneCore/gen"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/observatory"
	"github.com/xtls/xray-core/app/observatory/burst"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/core"
	dnsfeature "github.com/xtls/xray-core/features/dns"
	"github.com/xtls/xray-core/features/extension"
	xinternet "github.com/xtls/xray-core/transport/internet"
	"google.golang.org/protobuf/proto"

	_ "github.com/xtls/xray-core/main/distro/all"
)

func TestDeferredObservatoryPreservesPoolOptions(t *testing.T) {
	original := &burst.Config{
		SubjectSelector: []string{"smart-proxy-", "ai-proxy-"},
		PingConfig: &burst.HealthPingConfig{
			Destination: "https://connectivitycheck.gstatic.com/generate_204",
			Interval:    int64(15 * time.Minute), SamplingCount: 2,
			Timeout: int64(5 * time.Second), HttpMethod: "HEAD",
		},
	}
	config := &core.Config{App: []*serial.TypedMessage{serial.ToTypedMessage(original)}}
	deferBurstObservatory(config)
	wrapped, err := config.App[0].GetInstance()
	if err != nil {
		t.Fatal(err)
	}
	var restored burst.Config
	if err = proto.Unmarshal(wrapped.(*gen.XrayDeferredObservatoryConfig).GetBurstConfig(), &restored); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(original, &restored) {
		t.Fatalf("Pool options changed: got %v, want %v", &restored, original)
	}
}

// A real Xray observer with two main and two AI members. The domain-named HTTP
// proxy endpoints can resolve only after readiness; neither OS DNS nor internet
// access can make an early probe succeed by accident.
func TestPoolFirstSamplesWaitForDNSReadiness(t *testing.T) {
	var requests atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "CONNECT required", http.StatusMethodNotAllowed)
			return
		}
		conn, stream, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		_, _ = stream.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		_ = stream.Flush()
		probe, err := http.ReadRequest(stream.Reader)
		if err != nil {
			return
		}
		_ = probe.Body.Close()
		requests.Add(1)
		_, _ = stream.WriteString("HTTP/1.1 204 No Content\r\nConnection: close\r\n\r\n")
		_ = stream.Flush()
	}))
	defer proxy.Close()
	port := proxy.Listener.Addr().(*net.TCPAddr).Port
	config := fmt.Sprintf(`{
      "log":{"loglevel":"none"},
      "outbounds":[
        {"tag":"smart-proxy-0","protocol":"http","settings":{"servers":[{"address":"main0.invalid","port":%d}]}},
        {"tag":"smart-proxy-1","protocol":"http","settings":{"servers":[{"address":"main1.invalid","port":%d}]}},
        {"tag":"ai-proxy-1","protocol":"http","settings":{"servers":[{"address":"ai1.invalid","port":%d}]}},
        {"tag":"ai-proxy-2","protocol":"http","settings":{"servers":[{"address":"ai2.invalid","port":%d}]}}
      ],
      "routing":{"balancers":[
        {"tag":"main","selector":["smart-proxy-"],"strategy":{"type":"leastLoad","settings":{"expected":5,"tolerance":0.2}}},
        {"tag":"ai","selector":["ai-proxy-"],"fallbackTag":"ai-proxy-1","strategy":{"type":"leastLoad","settings":{"expected":5,"tolerance":0.2}}}
      ]},
      "burstObservatory":{"subjectSelector":["smart-proxy-","ai-proxy-"],
        "pingConfig":{"destination":"http://probe.invalid/generate_204","interval":"15m","sampling":2,"timeout":"1s","httpMethod":"HEAD"}}
    }`, port, port, port, port)
	instance, err := CreateXrayInstanceWithDeferredObservatory(config)
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	dns := &readinessDNS{}
	instance.SetOutboundDNS(dns, xinternet.ParseDomainStrategy("ForceIP"))
	if err = instance.Start(); err != nil {
		t.Fatal(err)
	}
	observer := instance.GetFeature(extension.ObservatoryType()).(extension.BurstObservatory)
	observer.Check([]string{"smart-proxy-0", "ai-proxy-1"})
	// Model a slow box startup, longer than an early local DNS failure takes.
	time.Sleep(100 * time.Millisecond)
	if dns.calls.Load() != 0 || requests.Load() != 0 {
		t.Fatalf("probes escaped before readiness: DNS=%d HTTP=%d", dns.calls.Load(), requests.Load())
	}
	result, err := observer.GetObservation(context.Background())
	if err != nil || len(result.(*observatory.ObservationResult).Status) != 0 {
		t.Fatalf("startup recorded a premature member result: %v, %v", result, err)
	}
	dns.ready.Store(true)
	// Bootstrap downloads must still be able to use Xray before the observer is
	// released (e.g. while sing-box is loading remote rule sets).
	transport := &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		target, parseErr := xnet.ParseDestination("tcp:" + address)
		if parseErr != nil {
			return nil, parseErr
		}
		return core.Dial(ctx, instance, target)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	response, err := client.Head("http://bootstrap.invalid/rules")
	if err != nil {
		t.Fatalf("bootstrap traffic was blocked by deferred probes: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("bootstrap status = %d", response.StatusCode)
	}
	result, err = observer.GetObservation(context.Background())
	if err != nil || len(result.(*observatory.ObservationResult).Status) != 0 {
		t.Fatalf("bootstrap traffic released probes prematurely: %v, %v", result, err)
	}
	if err = StartDeferredObservatory(instance); err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		result, err = observer.GetObservation(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		statuses := result.(*observatory.ObservationResult).Status
		if len(statuses) == 4 {
			for _, status := range statuses {
				if !status.Alive || status.HealthPing.All < 1 || status.HealthPing.Fail != 0 {
					t.Fatalf("first sample for %s was poisoned: %v", status.OutboundTag, status)
				}
			}
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("first sweep incomplete: %v (DNS=%d HTTP=%d)", statuses, dns.calls.Load(), requests.Load())
		}
	}
}

type readinessDNS struct {
	ready atomic.Bool
	calls atomic.Int32
}

func (*readinessDNS) Type() interface{} { return dnsfeature.ClientType() }
func (*readinessDNS) Start() error      { return nil }
func (*readinessDNS) Close() error      { return nil }
func (d *readinessDNS) LookupIP(string, dnsfeature.IPOption) ([]xnet.IP, uint32, error) {
	d.calls.Add(1)
	if !d.ready.Load() {
		return nil, 0, errors.New("DNS and egress are not ready")
	}
	return []xnet.IP{net.ParseIP("127.0.0.1").To4()}, 0, nil
}

type recordingObserver struct {
	starts atomic.Int32
	closes atomic.Int32
	checks atomic.Int32
}

func (*recordingObserver) Type() interface{} { return extension.ObservatoryType() }
func (o *recordingObserver) Start() error    { o.starts.Add(1); return nil }
func (o *recordingObserver) Close() error    { o.closes.Add(1); return nil }
func (o *recordingObserver) Check([]string)  { o.checks.Add(1) }
func (*recordingObserver) GetObservation(context.Context) (proto.Message, error) {
	return &observatory.ObservationResult{}, nil
}

func TestDeferredObservatoryLifecycle(t *testing.T) {
	t.Run("failed box startup never starts probes", func(t *testing.T) {
		underlying := &recordingObserver{}
		o := &deferredObservatory{BurstObservatory: underlying}
		if err := o.Start(); err != nil {
			t.Fatal(err)
		}
		if err := o.Close(); err != nil {
			t.Fatal(err)
		}
		if err := o.startProbes(); err == nil {
			t.Fatal("closed observer restarted")
		}
		o.Check([]string{"ai-proxy-1"})
		if underlying.starts.Load() != 0 || underlying.checks.Load() != 0 {
			t.Fatal("aborted startup launched probes")
		}
	})
	t.Run("ready is idempotent and close races safely", func(t *testing.T) {
		underlying := &recordingObserver{}
		o := &deferredObservatory{BurstObservatory: underlying}
		if err := o.Start(); err != nil {
			t.Fatal(err)
		}
		if err := o.startProbes(); err != nil {
			t.Fatal(err)
		}
		o.Check([]string{"smart-proxy-0"})
		var workers sync.WaitGroup
		for i := 0; i < 32; i++ {
			workers.Add(1)
			go func() { defer workers.Done(); _ = o.startProbes(); _ = o.Close() }()
		}
		workers.Wait()
		if underlying.starts.Load() != 1 || underlying.closes.Load() != 1 || underlying.checks.Load() != 1 {
			t.Fatalf("unexpected lifecycle counts: start=%d close=%d check=%d", underlying.starts.Load(), underlying.closes.Load(), underlying.checks.Load())
		}
	})
}

func TestOrdinaryXrayHasNoDeferredObserver(t *testing.T) {
	instance, err := CreateXrayInstance(`{"outbounds":[{"protocol":"freedom"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	if err = StartDeferredObservatory(instance); err != nil {
		t.Fatal(err)
	}
}
