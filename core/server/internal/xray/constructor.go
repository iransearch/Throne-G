package xray

import (
	"bytes"

	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/infra/conf/serial"
	xinternet "github.com/xtls/xray-core/transport/internet"
)

func buildXrayConfig(config string) (*core.Config, error) {
	patchedJSON, customOutbounds, err := prepareXrayCustomOutbounds(config)
	if err != nil {
		return nil, err
	}

	r := bytes.NewReader([]byte(patchedJSON))
	conf, err := serial.DecodeJSONConfig(r)
	if err != nil {
		return nil, err
	}

	built, err := conf.Build()
	if err != nil {
		return nil, err
	}
	if err := patchXrayCustomOutbounds(built, customOutbounds); err != nil {
		return nil, err
	}
	return built, nil
}

func hasThroneHysteria2Outbound(config string) bool {
	_, customOutbounds, err := prepareXrayCustomOutbounds(config)
	return err == nil && len(customOutbounds) > 0
}

func CreateXrayInstance(config string) (*core.Instance, error) {
	hasCustomHysteria2 := hasThroneHysteria2Outbound(config)
	built, err := buildXrayConfig(config)
	if err != nil {
		return nil, err
	}

	server, err := core.New(built)
	if err != nil {
		return nil, err
	}

	// Generated/live profiles replace this fallback with Throne's sing-box-backed
	// resolver before Start(). Standalone Xray full configs used by URL/IP/speed
	// tests do not have that preparation step; without a resolver the custom
	// Hysteria2 outbound cannot bootstrap a domain-named server. Restrict the
	// fallback to configs that actually contain Throne Hysteria2 so unrelated
	// Xray protocols keep their existing behavior.
	if hasCustomHysteria2 {
		server.SetOutboundDNS(&systemDNSClient{}, xinternet.ParseDomainStrategy("UseIPv4"))
	}

	return server, nil
}

// CheckXrayConfig validates an Xray JSON config without creating a running
// instance. It also validates Throne's internal Hysteria2 marker and patches
// the compiled config with the registered typed outbound message, but stops
// short of core.New so concurrent validation does not instantiate handlers.
func CheckXrayConfig(config string) error {
	_, err := buildXrayConfig(config)
	return err
}
