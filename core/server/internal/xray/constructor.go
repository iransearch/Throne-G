package xray

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

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

// addValidationTags gives Throne's internal Hysteria2 outbound a temporary tag
// only while CheckXrayConfig validates a standalone profile. The GUI validator
// intentionally sends just BuildXray() inside an outbounds array, before the
// normal chain builder assigns the real runtime tag. prepareXrayCustomOutbounds
// needs a tag so it can replace the compiled placeholder with the typed custom
// outbound; without this temporary value URL Test rejects an otherwise valid
// Hysteria2 profile before BuildTestConfig has a chance to tag it.
func addValidationTags(config string) string {
	var root map[string]json.RawMessage
	if err := json.Unmarshal([]byte(config), &root); err != nil {
		return config
	}
	rawOutbounds, ok := root["outbounds"]
	if !ok {
		return config
	}
	var outbounds []map[string]json.RawMessage
	if err := json.Unmarshal(rawOutbounds, &outbounds); err != nil {
		return config
	}

	changed := false
	for index, outbound := range outbounds {
		var protocol string
		if err := json.Unmarshal(outbound["protocol"], &protocol); err != nil || protocol != xrayHysteria2Protocol {
			continue
		}
		var tag string
		_ = json.Unmarshal(outbound["tag"], &tag)
		if strings.TrimSpace(tag) != "" {
			continue
		}
		tagJSON, err := json.Marshal(fmt.Sprintf("__throne-validation-hysteria2-%d", index))
		if err != nil {
			return config
		}
		outbound["tag"] = tagJSON
		changed = true
	}
	if !changed {
		return config
	}
	patchedOutbounds, err := json.Marshal(outbounds)
	if err != nil {
		return config
	}
	root["outbounds"] = patchedOutbounds
	patchedConfig, err := json.Marshal(root)
	if err != nil {
		return config
	}
	return string(patchedConfig)
}

// CheckXrayConfig validates an Xray JSON config without creating a running
// instance. It also validates Throne's internal Hysteria2 marker and patches
// the compiled config with the registered typed outbound message, but stops
// short of core.New so concurrent validation does not instantiate handlers.
func CheckXrayConfig(config string) error {
	_, err := buildXrayConfig(addValidationTags(config))
	return err
}
