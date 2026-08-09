package xray

import (
	"encoding/json"
	"strings"
	"testing"
)

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
