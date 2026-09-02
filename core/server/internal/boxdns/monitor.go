package boxdns

import (
	"fmt"
	"sync"

	tun "github.com/sagernet/sing-tun"
	"github.com/sagernet/sing/common/control"
	logger2 "github.com/sagernet/sing/common/logger"
)

// Process-wide and independent of the box lifecycle. It is constructed during
// init and started after the RPC health handshake; on Windows it also drives
// system DNS, elsewhere HandleSystemDNS is a no-op.
var DnsManagerInstance *DnsManager

// deferredStarter keeps the potentially slow OS monitor startup out of package
// initialization. Start is a barrier: every caller observes the same completed
// attempt and the start function is never retried, matching the old init-time
// behavior. StartAsync only schedules that same attempt.
type deferredStarter struct {
	startOnce sync.Once
	asyncOnce sync.Once
	start     func() error
	err       error
}

func newDeferredStarter(start func() error) *deferredStarter {
	return &deferredStarter{start: start}
}

func (s *deferredStarter) Start() error {
	if s == nil {
		return nil
	}
	s.startOnce.Do(func() {
		if s.start != nil {
			s.err = s.start()
		}
	})
	return s.err
}

func (s *deferredStarter) StartAsync() {
	if s == nil {
		return
	}
	s.asyncOnce.Do(func() {
		go func() {
			_ = s.Start()
		}()
	})
}

var networkMonitorStarter *deferredStarter

type DnsManager struct {
	Monitor tun.DefaultInterfaceMonitor
	lastIfc *control.Interface
}

func init() {
	logger := logger2.NOP()
	updMonitor, err := tun.NewNetworkUpdateMonitor(logger)
	if err != nil {
		fmt.Println("Could not create NetworkUpdateMonitor")
		return
	}
	monitor, err := tun.NewDefaultInterfaceMonitor(updMonitor, logger, tun.DefaultInterfaceMonitorOptions{
		InterfaceFinder: control.NewDefaultInterfaceFinder(),
	})
	if err != nil {
		fmt.Println("Could not create DefaultInterfaceMonitor")
		return
	}
	DnsManagerInstance = &DnsManager{Monitor: monitor}
	monitor.RegisterCallback(DnsManagerInstance.HandleSystemDNS)
	networkMonitorStarter = newDeferredStarter(func() error {
		if err := updMonitor.Start(); err != nil {
			fmt.Println("Could not start updMonitor")
			return err
		}
		if err := monitor.Start(); err != nil {
			fmt.Println("Could not start monitor")
			return err
		}
		return nil
	})
}

// Start waits until the single network-monitor startup attempt completes.
// Callers intentionally retain their existing behavior after a failed attempt;
// the error is available for diagnostics but must not become a new RPC failure.
func Start() error {
	return networkMonitorStarter.Start()
}

// StartAsync begins the same single startup attempt without delaying the RPC
// health handshake that triggered it.
func StartAsync() {
	networkMonitorStarter.StartAsync()
}

// nil when the monitor is unavailable; TUN and loopback are excluded, so the result is safe to bind egress to while throne-tun is up.
func DefaultInterface() *control.Interface {
	if DnsManagerInstance == nil || DnsManagerInstance.Monitor == nil {
		return nil
	}
	return DnsManagerInstance.Monitor.DefaultInterface()
}
