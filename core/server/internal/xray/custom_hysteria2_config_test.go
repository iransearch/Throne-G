package xray

import (
	"ThroneCore/gen"
	"encoding/json"
	"strings"
	"testing"

	hy2 "github.com/sagernet/sing-quic/hysteria2"
)

func TestPrepareXrayCustomOutboundsPreservesGeckoPacketSizes(t *testing.T) {
	config := `{
  "outbounds": [{
    "protocol": "hysteria2",
    "settings": {
      "server": "example.com",
      "server_port": 443,
      "password": "auth-password",
      "obfs": {
        "type": "gecko",
        "password": "obfs-password",
        "min_packet_size": 640,
        "max_packet_size": 1400
      },
      "tls": {"enabled": true, "server_name": "example.com"}
    },
    "tag": "gecko-out"
  }]
}`

	_, custom, err := prepareXrayCustomOutbounds(config)
	if err != nil {
		t.Fatal(err)
	}
	got := custom["gecko-out"]
	if got == nil {
		t.Fatal("missing Gecko custom outbound")
	}
	if got.GetMinPacketSize() != 640 || got.GetMaxPacketSize() != 1400 {
		t.Fatalf("protobuf Gecko packet sizes = %d..%d, want 640..1400", got.GetMinPacketSize(), got.GetMaxPacketSize())
	}

	clientOptions := new(hy2.ClientOptions)
	if err := applyXrayHysteria2Obfs(clientOptions, got); err != nil {
		t.Fatal(err)
	}
	if clientOptions.GeckoPassword != "obfs-password" {
		t.Fatalf("sing-quic Gecko password = %q, want obfs-password", clientOptions.GeckoPassword)
	}
	if clientOptions.GeckoMinPacketSize != 640 || clientOptions.GeckoMaxPacketSize != 1400 {
		t.Fatalf("sing-quic Gecko packet sizes = %d..%d, want 640..1400", clientOptions.GeckoMinPacketSize, clientOptions.GeckoMaxPacketSize)
	}
}

func TestXrayHysteria2GeckoPacketSizeValidation(t *testing.T) {
	tests := []struct {
		name            string
		obfsType        string
		minPacketSize   *int32
		maxPacketSize   *int32
		wantErrContains string
	}{
		{name: "omitted defaults", obfsType: "gecko"},
		{name: "explicit defaults", obfsType: "gecko", minPacketSize: int32Ptr(0), maxPacketSize: int32Ptr(0)},
		{name: "custom range", obfsType: "gecko", minPacketSize: int32Ptr(640), maxPacketSize: int32Ptr(1400)},
		{name: "wire limit", obfsType: "gecko", maxPacketSize: int32Ptr(2048)},
		{name: "negative minimum", obfsType: "gecko", minPacketSize: int32Ptr(-1), wantErrContains: "cannot be negative"},
		{name: "negative maximum", obfsType: "gecko", maxPacketSize: int32Ptr(-1), wantErrContains: "cannot be negative"},
		{name: "minimum above default maximum", obfsType: "gecko", minPacketSize: int32Ptr(1201), wantErrContains: "invalid Gecko packet size range"},
		{name: "maximum below default minimum", obfsType: "gecko", maxPacketSize: int32Ptr(511), wantErrContains: "invalid Gecko packet size range"},
		{name: "maximum above wire limit", obfsType: "gecko", maxPacketSize: int32Ptr(2049), wantErrContains: "cannot exceed 2048"},
		{name: "salamander nonzero", obfsType: "salamander", minPacketSize: int32Ptr(640), wantErrContains: "only supported with Gecko"},
		{name: "salamander explicit zero", obfsType: "salamander", minPacketSize: int32Ptr(0), wantErrContains: "only supported with Gecko"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := &gen.XrayHysteria2Config{
				ObfsType:      stringPtr(test.obfsType),
				MinPacketSize: test.minPacketSize,
				MaxPacketSize: test.maxPacketSize,
			}
			err := validateXrayHysteria2GeckoPacketSizes(config)
			if test.wantErrContains == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErrContains) {
				t.Fatalf("error = %v, want substring %q", err, test.wantErrContains)
			}
		})
	}
}

func TestPrepareXrayCustomOutboundsRejectsPacketSizesForNonGeckoObfs(t *testing.T) {
	config := `{
  "outbounds": [{
    "protocol": "hysteria2",
    "settings": {
      "server": "example.com",
      "server_port": 443,
      "obfs": {
        "type": "salamander",
        "password": "obfs-password",
        "min_packet_size": 0
      },
      "tls": {"enabled": true, "server_name": "example.com"}
    },
    "tag": "salamander-out"
  }]
}`

	_, _, err := prepareXrayCustomOutbounds(config)
	if err == nil || !strings.Contains(err.Error(), "only supported with Gecko") {
		t.Fatalf("error = %v, want Gecko-only packet-size error", err)
	}
}

func TestPrepareXrayCustomOutboundsTranslatesLegacyHysteria2AllowInsecure(t *testing.T) {
	config := `{
  "outbounds": [
    {
      "protocol": "hysteria",
      "settings": {"version": 2, "address": "hy2192.hy2any.info", "port": 8080},
      "streamSettings": {
        "network": "hysteria",
        "security": "tls",
        "tlsSettings": {"serverName": "www.google.com", "allowInsecure": true},
        "hysteriaSettings": {"version": 2, "auth": "auth-333"},
        "sockopt": {"domainStrategy": "UseIPv4"}
      },
      "tag": "smart-3-proxy-333"
    },
    {
      "protocol": "hysteria",
      "settings": {"version": 2, "address": "hy2it159.hy2any.info", "port": 900},
      "streamSettings": {
        "network": "hysteria",
        "security": "tls",
        "tlsSettings": {"serverName": "www.google.com", "allowInsecure": true},
        "hysteriaSettings": {"version": 2, "auth": "auth-334"}
      },
      "tag": "smart-3-proxy-334"
    }
  ],
  "routing": {
    "balancers": [{"tag": "smart-balancer-3", "selector": ["smart-3-proxy-"]}]
  }
}`

	patched, custom, err := prepareXrayCustomOutbounds(config)
	if err != nil {
		t.Fatal(err)
	}
	if len(custom) != 2 {
		t.Fatalf("expected 2 custom outbounds, got %d", len(custom))
	}

	checks := map[string]struct {
		server string
		port   uint32
		auth   string
	}{
		"smart-3-proxy-333": {"hy2192.hy2any.info", 8080, "auth-333"},
		"smart-3-proxy-334": {"hy2it159.hy2any.info", 900, "auth-334"},
	}
	for tag, expected := range checks {
		got := custom[tag]
		if got == nil {
			t.Fatalf("missing translated outbound %q", tag)
		}
		if got.GetServer() != expected.server || got.GetServerPort() != expected.port || got.GetPassword() != expected.auth {
			t.Fatalf("translated %s mismatch: server=%q port=%d auth=%q", tag, got.GetServer(), got.GetServerPort(), got.GetPassword())
		}
		var tls struct {
			Enabled    bool   `json:"enabled"`
			Insecure   bool   `json:"insecure"`
			ServerName string `json:"server_name"`
		}
		if err := json.Unmarshal([]byte(got.GetTlsJson()), &tls); err != nil {
			t.Fatalf("decode translated TLS for %s: %v", tag, err)
		}
		if !tls.Enabled || !tls.Insecure || tls.ServerName != "www.google.com" {
			t.Fatalf("translated TLS for %s mismatch: %+v", tag, tls)
		}
	}

	if strings.Contains(patched, "allowInsecure") {
		t.Fatal("legacy allowInsecure leaked into Xray parser input")
	}
	if !strings.Contains(patched, "smart-3-proxy-") || !strings.Contains(patched, "smart-balancer-3") {
		t.Fatal("balancer tags were not preserved")
	}

	var root map[string]json.RawMessage
	if err := json.Unmarshal([]byte(patched), &root); err != nil {
		t.Fatal(err)
	}
	var outbounds []map[string]json.RawMessage
	if err := json.Unmarshal(root["outbounds"], &outbounds); err != nil {
		t.Fatal(err)
	}
	for _, outbound := range outbounds {
		var protocol, tag string
		_ = json.Unmarshal(outbound["protocol"], &protocol)
		_ = json.Unmarshal(outbound["tag"], &tag)
		if protocol != "freedom" {
			t.Fatalf("placeholder %s protocol = %q, want freedom", tag, protocol)
		}
		if _, exists := outbound["streamSettings"]; exists {
			t.Fatalf("placeholder %s still has streamSettings", tag)
		}
	}

	if _, err := buildXrayConfig(config); err != nil {
		t.Fatalf("translated config still fails Xray build: %v", err)
	}
}

func TestPrepareXrayCustomOutboundsLeavesSupportedNativeHysteriaUntouched(t *testing.T) {
	config := `{"outbounds":[{"protocol":"hysteria","settings":{"version":2,"address":"example.com","port":443},"streamSettings":{"network":"hysteria","security":"tls","tlsSettings":{"serverName":"example.com"},"hysteriaSettings":{"version":2,"auth":"secret"}},"tag":"native-hy2"}]}`
	patched, custom, err := prepareXrayCustomOutbounds(config)
	if err != nil {
		t.Fatal(err)
	}
	if patched != config {
		t.Fatal("native Hysteria2 without removed allowInsecure should not be rewritten")
	}
	if len(custom) != 0 {
		t.Fatalf("expected no custom outbounds, got %d", len(custom))
	}
}
