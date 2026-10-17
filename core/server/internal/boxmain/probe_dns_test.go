package boxmain

import (
	"os"
	"testing"

	"ThroneCore/internal/probedns"
	"github.com/sagernet/sing-box/option"
)

const probeProfile = `{"log":{"disabled":true},"dns":{"servers":[{"tag":"dns-direct","type":"https-race","servers":[
{"server":"8.8.8.8","path":"/dns-query","tls":{"enabled":true,"server_name":"dns.google"}},
{"server":"1.1.1.1","path":"/dns-query","tls":{"enabled":true,"server_name":"cloudflare-dns.com"}},
{"server":"77.88.8.8","path":"/dns-query","tls":{"enabled":true,"server_name":"common.dot.dns.yandex.net"}}
]}]},"outbounds":[{"type":"direct","tag":"direct"}],"route":{"auto_detect_interface":true,"default_domain_resolver":{"server":"dns-direct"}}}`

func TestProbeDNSProfile(t *testing.T) {
	raw := []byte(probeProfile)
	if path := os.Getenv("IRSPEEDY_PROBE_DNS_FIXTURE"); path != "" {
		var err error
		raw, err = os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := parseConfig(newBoxContext(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.DNS.Servers) != 1 || cfg.DNS.Servers[0].Tag != "dns-direct" || cfg.DNS.Servers[0].Type != probedns.Type {
		t.Fatal("probe DNS not composite")
	}
	o := cfg.DNS.Servers[0].Options.(*probedns.Options)
	ips := []string{"8.8.8.8", "1.1.1.1", "77.88.8.8"}
	names := []string{"dns.google", "cloudflare-dns.com", "common.dot.dns.yandex.net"}
	if len(o.Servers) != 3 {
		t.Fatal("provider count")
	}
	for i, s := range o.Servers {
		if s.Server != ips[i] || s.TLS.ServerName != names[i] || !s.TLS.Enabled || s.TLS.Insecure || s.Detour != "" || s.DomainResolver != nil || s.Path != "/dns-query" {
			t.Fatalf("provider %d policy changed", i)
		}
	}
	if !cfg.Route.AutoDetectInterface || cfg.Route.DefaultDomainResolver.Server != "dns-direct" {
		t.Fatal("probe physical resolver pin lost")
	}
	if err = Check(raw); err != nil {
		t.Fatal(err)
	}
	// Both production entry points register the custom type before strict parsing.
	var _ *option.Options = cfg
}
