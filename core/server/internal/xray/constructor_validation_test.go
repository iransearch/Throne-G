package xray

import (
	"fmt"
	"testing"
)

func TestCheckXrayConfigAcceptsUntaggedHysteria2Aliases(t *testing.T) {
	for _, protocol := range []string{xrayHysteria2AliasProtocol, xrayHysteria2Protocol} {
		t.Run(protocol, func(t *testing.T) {
			config := fmt.Sprintf(`{
  "outbounds": [
    {
      "protocol": %q,
      "settings": {
        "server": "hy2192.hy2any.info",
        "server_port": 8080,
        "password": "test-password",
        "obfs": {
          "type": "gecko",
          "password": "test-obfs"
        },
        "tls": {
          "enabled": true,
          "server_name": "www.google.com",
          "insecure": true
        }
      }
    }
  ]
}`, protocol)

			if err := CheckXrayConfig(config); err != nil {
				t.Fatalf("untagged standalone Hysteria2 validation failed for protocol %q: %v", protocol, err)
			}
		})
	}
}
