package xray

import (
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/outbound"
	"github.com/xtls/xray-core/proxy"
)

// ResetHysteria2Member keeps listeners, DNS, routing and other members intact.
func ResetHysteria2Member(instance *core.Instance, tag string) bool {
	if instance == nil || tag == "" {
		return false
	}
	manager, ok := instance.GetFeature(outbound.ManagerType()).(outbound.Manager)
	if !ok {
		return false
	}
	handler, ok := manager.GetHandler(tag).(interface{ GetOutbound() proxy.Outbound })
	if !ok {
		return false
	}
	h, ok := handler.GetOutbound().(*xrayHysteria2Outbound)
	return ok && h.resetClient()
}

func (h *xrayHysteria2Outbound) resetClient() bool {
	h.clientMu.Lock()
	if h.closed || h.client == nil {
		h.clientMu.Unlock()
		return false
	}
	client, cancel := h.client, h.clientCancel
	h.client, h.clientCancel = nil, nil
	h.clientMu.Unlock()
	// Cancel first so Close does not wait for a blackholed QUIC handshake.
	if cancel != nil {
		cancel()
	}
	_ = client.Close()
	return true
}
