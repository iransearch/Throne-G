package xray

import "testing"

func TestCheckXrayConfigAcceptsUntaggedThroneHysteria2(t *testing.T) {
	config := `{
  "outbounds": [
    {
      "protocol": "throne-hysteria2",
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
}`

	if err := CheckXrayConfig(config); err != nil {
		t.Fatalf("untagged standalone Hysteria2 validation failed: %v", err)
	}
}
