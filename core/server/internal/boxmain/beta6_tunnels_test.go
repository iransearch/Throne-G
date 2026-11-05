package boxmain

import (
	"fmt"
	"strings"
	"testing"
)

func TestBeta6NativeDNSTunnelRegistration(t *testing.T) {
	for _, mode := range []string{"dnstt", "vaydns"} {
		t.Run(mode, func(t *testing.T) {
			config := fmt.Sprintf(`{"outbounds":[{"type":"dnstt","tag":"proxy","mode":%q,"domain":"tunnel.example.org","public_key":%q,"resolvers":[{"type":"udp","server":"127.0.0.1","server_port":53}]}],"route":{"final":"proxy"}}`, mode, strings.Repeat("01", 32))
			if err := Check([]byte(config)); err != nil {
				t.Fatal(err)
			}
			if err := Check([]byte(strings.Replace(config, strings.Repeat("01", 32), "invalid-key", 1))); err == nil {
				t.Fatal("invalid tunnel public key accepted")
			}
		})
	}
	if err := Check([]byte(`{"outbounds":[{"type":"masterdns","tag":"proxy","domain":["tunnel.example.org"],"resolvers":["127.0.0.1:53"],"encryption_method":"chacha20","encryption_key":"fixture-key"}],"route":{"final":"proxy"}}`)); err != nil {
		t.Fatal(err)
	}
}
