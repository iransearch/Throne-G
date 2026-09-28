package xray

import (
	"ThroneCore/gen"
	"context"
	"errors"
	"sync"

	"github.com/xtls/xray-core/app/observatory/burst"
	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/extension"
	"google.golang.org/protobuf/proto"
)

// Wrap the existing observer rather than implementing another scheduler. This
// preserves selectors, sampling, interval, statistics and leastLoad policy.
func deferBurstObservatory(config *core.Config) {
	burstType := serial.GetMessageType(&burst.Config{})
	for i, app := range config.App {
		if app.Type == burstType {
			config.App[i] = serial.ToTypedMessage(&gen.XrayDeferredObservatoryConfig{
				BurstConfig: app.Value,
			})
		}
	}
}

type deferredObservatory struct {
	extension.BurstObservatory
	mu      sync.Mutex
	started bool
	ready   bool
	closed  bool
}

var _ extension.BurstObservatory = (*deferredObservatory)(nil)

// Called by core.Instance.Start. No probes or wait goroutines are started here.
func (o *deferredObservatory) Start() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return errors.New("deferred observatory is closed")
	}
	o.started = true
	return nil
}

func (o *deferredObservatory) startProbes() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed || !o.started {
		return errors.New("deferred observatory is not running")
	}
	if o.ready {
		return nil
	}
	if err := o.BurstObservatory.Start(); err != nil {
		return err
	}
	o.ready = true
	return nil
}

// A forced recheck must not bypass startup readiness either. The normal first
// sweep includes every selected member when startProbes releases the scheduler.
func (o *deferredObservatory) Check(tags []string) {
	o.mu.Lock()
	ready := o.ready && !o.closed
	o.mu.Unlock()
	if ready {
		o.BurstObservatory.Check(tags)
	}
}

func (o *deferredObservatory) Close() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return nil
	}
	o.closed = true
	return o.BurstObservatory.Close()
}

// No-op for configurations without a deferred burst observer.
func StartDeferredObservatory(instance *core.Instance) error {
	if observer, ok := instance.GetFeature(extension.ObservatoryType()).(*deferredObservatory); ok {
		return observer.startProbes()
	}
	return nil
}

func init() {
	common.Must(common.RegisterConfig((*gen.XrayDeferredObservatoryConfig)(nil), func(ctx context.Context, config interface{}) (interface{}, error) {
		var original burst.Config
		if err := proto.Unmarshal(config.(*gen.XrayDeferredObservatoryConfig).GetBurstConfig(), &original); err != nil {
			return nil, err
		}
		observer, err := burst.New(ctx, &original)
		if err != nil {
			return nil, err
		}
		return &deferredObservatory{BurstObservatory: observer}, nil
	}))
}
