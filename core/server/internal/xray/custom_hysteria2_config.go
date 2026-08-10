package xray

import (
	"ThroneCore/gen"
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/sagernet/sing-box/option"
	xserial "github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/core"
)

const (
	xrayLegacyHysteriaProtocol = "hysteria"
	xrayHysteria2AliasProtocol = "hysteria2"
)

func isCustomXrayHysteria2Protocol(protocol string) bool {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case xrayHysteria2Protocol, xrayHysteria2AliasProtocol:
		return true
	default:
		return false
	}
}

type xrayHysteria2JSONObfs struct {
	Type     string `json:"type"`
	Password string `json:"password"`
}

type xrayHysteria2JSONSettings struct {
	Server      string                 `json:"server"`
	ServerPort  uint32                 `json:"server_port"`
	ServerPorts []string               `json:"server_ports"`
	HopInterval string                 `json:"hop_interval"`
	UpMbps      int32                  `json:"up_mbps"`
	DownMbps    int32                  `json:"down_mbps"`
	Password    string                 `json:"password"`
	Obfs        *xrayHysteria2JSONObfs `json:"obfs"`
	TLS         json.RawMessage        `json:"tls"`
}

// Older Xray Hysteria2 configs put authentication and TLS under streamSettings.
// New Xray releases reject tlsSettings.allowInsecure before the Hysteria transport
// is even built. Throne already owns a native Xray proxy.Outbound for Hysteria2,
// so the compatibility adapter below moves only that removed legacy form onto the
// custom outbound while preserving the original outbound tag used by balancers.
type xrayLegacyHysteriaClientSettings struct {
	Version int32  `json:"version"`
	Address string `json:"address"`
	Port    uint32 `json:"port"`
}

type xrayLegacyHysteriaTLSSettings struct {
	ServerName    string          `json:"serverName"`
	AllowInsecure *bool           `json:"allowInsecure"`
	ALPN          json.RawMessage `json:"alpn,omitempty"`
}

type xrayLegacyHysteriaTransportSettings struct {
	Version int32  `json:"version"`
	Auth    string `json:"auth"`
}

type xrayLegacyHysteriaStreamSettings struct {
	Network          string                              `json:"network"`
	Security         string                              `json:"security"`
	TLSSettings      xrayLegacyHysteriaTLSSettings       `json:"tlsSettings"`
	HysteriaSettings xrayLegacyHysteriaTransportSettings `json:"hysteriaSettings"`
}

func prepareXrayCustomOutbounds(config string) (string, map[string]*gen.XrayHysteria2Config, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal([]byte(config), &root); err != nil {
		return "", nil, err
	}
	rawOutbounds, ok := root["outbounds"]
	if !ok {
		return config, nil, nil
	}
	var outbounds []map[string]json.RawMessage
	if err := json.Unmarshal(rawOutbounds, &outbounds); err != nil {
		return "", nil, fmt.Errorf("xray custom outbound: parse outbounds: %w", err)
	}

	custom := make(map[string]*gen.XrayHysteria2Config)
	changed := false
	for index, outbound := range outbounds {
		var protocol string
		if err := json.Unmarshal(outbound["protocol"], &protocol); err != nil {
			continue
		}

		var settings *xrayHysteria2JSONSettings
		stripLegacyStreamSettings := false
		switch {
		case isCustomXrayHysteria2Protocol(protocol):
			var customSettings xrayHysteria2JSONSettings
			if err := json.Unmarshal(outbound["settings"], &customSettings); err != nil {
				return "", nil, fmt.Errorf("xray custom outbound %d: invalid Hysteria2 settings: %w", index, err)
			}
			settings = &customSettings
		case protocol == xrayLegacyHysteriaProtocol:
			legacySettings, matched, err := translateLegacyXrayHysteria2(outbound)
			if err != nil {
				return "", nil, fmt.Errorf("xray legacy Hysteria2 outbound %d: %w", index, err)
			}
			if !matched {
				continue
			}
			settings = legacySettings
			stripLegacyStreamSettings = true
		default:
			continue
		}

		changed = true
		var tag string
		if err := json.Unmarshal(outbound["tag"], &tag); err != nil || strings.TrimSpace(tag) == "" {
			return "", nil, fmt.Errorf("xray custom outbound %d: missing tag", index)
		}
		if _, exists := custom[tag]; exists {
			return "", nil, fmt.Errorf("xray custom outbound: duplicate tag %q", tag)
		}
		customConfig, err := settings.toProto()
		if err != nil {
			return "", nil, fmt.Errorf("xray custom outbound %q: %w", tag, err)
		}
		custom[tag] = customConfig

		// Let Xray's normal JSON compiler create the outbound slot and all routing /
		// observatory references by tag. The compiled proxy settings are replaced
		// with XrayHysteria2Config after Build(). Legacy Hysteria streamSettings must
		// not reach the compiler because its removed allowInsecure field is exactly
		// what caused the load-balanced config to fail.
		outbound["protocol"] = json.RawMessage(`"freedom"`)
		outbound["settings"] = json.RawMessage(`{}`)
		if stripLegacyStreamSettings {
			delete(outbound, "streamSettings")
		}
	}
	if !changed {
		return config, nil, nil
	}
	patchedOutbounds, err := json.Marshal(outbounds)
	if err != nil {
		return "", nil, err
	}
	root["outbounds"] = patchedOutbounds
	patchedConfig, err := json.Marshal(root)
	if err != nil {
		return "", nil, err
	}
	return string(patchedConfig), custom, nil
}

// translateLegacyXrayHysteria2 recognizes only the legacy form that current
// Xray can no longer parse: Hysteria v2 + TLS + allowInsecure=true. Configs that
// do not use the removed setting remain native Xray Hysteria configs unchanged.
func translateLegacyXrayHysteria2(outbound map[string]json.RawMessage) (*xrayHysteria2JSONSettings, bool, error) {
	var client xrayLegacyHysteriaClientSettings
	if err := json.Unmarshal(outbound["settings"], &client); err != nil {
		return nil, false, fmt.Errorf("invalid client settings: %w", err)
	}
	if client.Version != 2 {
		return nil, false, nil
	}

	rawStream, ok := outbound["streamSettings"]
	if !ok || len(bytes.TrimSpace(rawStream)) == 0 {
		return nil, false, nil
	}
	var stream xrayLegacyHysteriaStreamSettings
	if err := json.Unmarshal(rawStream, &stream); err != nil {
		return nil, false, fmt.Errorf("invalid streamSettings: %w", err)
	}
	if stream.TLSSettings.AllowInsecure == nil || !*stream.TLSSettings.AllowInsecure {
		return nil, false, nil
	}

	if strings.ToLower(strings.TrimSpace(stream.Network)) != "hysteria" {
		return nil, true, fmt.Errorf("allowInsecure compatibility requires streamSettings.network=hysteria")
	}
	if strings.ToLower(strings.TrimSpace(stream.Security)) != "tls" {
		return nil, true, fmt.Errorf("Hysteria2 requires streamSettings.security=tls")
	}
	if stream.HysteriaSettings.Version != 2 {
		return nil, true, fmt.Errorf("hysteriaSettings.version must be 2")
	}
	if strings.TrimSpace(client.Address) == "" {
		return nil, true, fmt.Errorf("missing server address")
	}
	if client.Port == 0 || client.Port > 65535 {
		return nil, true, fmt.Errorf("invalid server port %d", client.Port)
	}

	tlsObject := map[string]json.RawMessage{
		"enabled":  json.RawMessage(`true`),
		"insecure": json.RawMessage(`true`),
	}
	if stream.TLSSettings.ServerName != "" {
		serverName, err := json.Marshal(stream.TLSSettings.ServerName)
		if err != nil {
			return nil, true, err
		}
		tlsObject["server_name"] = serverName
	}
	if alpn := bytes.TrimSpace(stream.TLSSettings.ALPN); len(alpn) > 0 && !bytes.Equal(alpn, []byte("null")) {
		tlsObject["alpn"] = append(json.RawMessage(nil), alpn...)
	}
	tlsJSON, err := json.Marshal(tlsObject)
	if err != nil {
		return nil, true, fmt.Errorf("encode TLS compatibility settings: %w", err)
	}

	return &xrayHysteria2JSONSettings{
		Server:     client.Address,
		ServerPort: client.Port,
		Password:   stream.HysteriaSettings.Auth,
		TLS:        tlsJSON,
	}, true, nil
}

func (s xrayHysteria2JSONSettings) toProto() (*gen.XrayHysteria2Config, error) {
	s.Server = strings.TrimSpace(s.Server)
	if s.Server == "" {
		return nil, fmt.Errorf("missing server")
	}
	if s.ServerPort == 0 {
		s.ServerPort = 443
	}
	if s.ServerPort > 65535 {
		return nil, fmt.Errorf("invalid server port %d", s.ServerPort)
	}
	if s.UpMbps < 0 || s.DownMbps < 0 {
		return nil, fmt.Errorf("bandwidth cannot be negative")
	}
	if s.HopInterval != "" {
		if _, err := time.ParseDuration(s.HopInterval); err != nil {
			return nil, fmt.Errorf("invalid hop interval %q: %w", s.HopInterval, err)
		}
	}

	obfsType, obfsPassword := "", ""
	if s.Obfs != nil {
		obfsType = strings.ToLower(strings.TrimSpace(s.Obfs.Type))
		obfsPassword = s.Obfs.Password
		if obfsType == "" {
			obfsType = "salamander"
		}
		if obfsType != "salamander" && obfsType != "gecko" {
			return nil, fmt.Errorf("unsupported obfs type %q", obfsType)
		}
		if obfsPassword == "" {
			return nil, fmt.Errorf("missing obfs password")
		}
	}

	if len(bytes.TrimSpace(s.TLS)) == 0 || bytes.Equal(bytes.TrimSpace(s.TLS), []byte("null")) || bytes.Equal(bytes.TrimSpace(s.TLS), []byte("{}")) {
		return nil, fmt.Errorf("TLS is required")
	}
	var tlsOptions option.OutboundTLSOptions
	if err := json.Unmarshal(s.TLS, &tlsOptions); err != nil {
		return nil, fmt.Errorf("invalid TLS options: %w", err)
	}
	if !tlsOptions.Enabled {
		return nil, fmt.Errorf("TLS is required")
	}
	tlsJSON, err := json.Marshal(tlsOptions)
	if err != nil {
		return nil, fmt.Errorf("encode TLS options: %w", err)
	}

	config := &gen.XrayHysteria2Config{
		Server:       &s.Server,
		ServerPort:   &s.ServerPort,
		ServerPorts:  append([]string(nil), s.ServerPorts...),
		HopInterval:  stringPtr(s.HopInterval),
		UpMbps:       int32Ptr(s.UpMbps),
		DownMbps:     int32Ptr(s.DownMbps),
		Password:     stringPtr(s.Password),
		ObfsType:     stringPtr(obfsType),
		ObfsPassword: stringPtr(obfsPassword),
		TlsJson:      stringPtr(string(tlsJSON)),
	}
	return config, validateXrayHysteria2Proto(config)
}

func validateXrayHysteria2Proto(config *gen.XrayHysteria2Config) error {
	if config == nil {
		return fmt.Errorf("nil Hysteria2 config")
	}
	if strings.TrimSpace(config.GetServer()) == "" {
		return fmt.Errorf("missing server")
	}
	if config.GetServerPort() == 0 || config.GetServerPort() > 65535 {
		return fmt.Errorf("invalid server port %d", config.GetServerPort())
	}
	if config.GetUpMbps() < 0 || config.GetDownMbps() < 0 {
		return fmt.Errorf("bandwidth cannot be negative")
	}
	if value := strings.TrimSpace(config.GetHopInterval()); value != "" {
		if _, err := time.ParseDuration(value); err != nil {
			return fmt.Errorf("invalid hop interval %q: %w", value, err)
		}
	}
	if config.GetObfsPassword() != "" {
		switch strings.ToLower(config.GetObfsType()) {
		case "", "salamander", "gecko":
		default:
			return fmt.Errorf("unsupported obfs type %q", config.GetObfsType())
		}
	}
	if strings.TrimSpace(config.GetTlsJson()) == "" {
		return fmt.Errorf("TLS is required")
	}
	return nil
}

func patchXrayCustomOutbounds(config *core.Config, custom map[string]*gen.XrayHysteria2Config) error {
	if len(custom) == 0 {
		return nil
	}
	remaining := make(map[string]*gen.XrayHysteria2Config, len(custom))
	for tag, value := range custom {
		remaining[tag] = value
	}
	for _, outbound := range config.Outbound {
		customConfig, ok := remaining[outbound.GetTag()]
		if !ok {
			continue
		}
		outbound.ProxySettings = xserial.ToTypedMessage(customConfig)
		delete(remaining, outbound.GetTag())
	}
	if len(remaining) != 0 {
		for tag := range remaining {
			return fmt.Errorf("xray custom outbound %q disappeared while compiling config", tag)
		}
	}
	return nil
}

func stringPtr(value string) *string { return &value }
func int32Ptr(value int32) *int32    { return &value }
