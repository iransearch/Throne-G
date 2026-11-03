package dns

import (
	"testing"

	mdns "github.com/miekg/dns"
	"github.com/sagernet/sing-box/adapter"
)

func TestResponseTTLIncludesZeroRecords(t *testing.T) {
	for _, test := range []struct {
		name    string
		answer  []uint32
		ns      []uint32
		extra   []uint32
		wantTTL uint32
	}{
		{name: "empty"},
		{name: "positive minimum", answer: []uint32{60, 30}, ns: []uint32{90}, extra: []uint32{120}, wantTTL: 30},
		{name: "zero first", answer: []uint32{0, 60}},
		{name: "zero last", answer: []uint32{60, 0}},
		{name: "zero authority", answer: []uint32{60}, ns: []uint32{0}},
		{name: "zero additional", answer: []uint32{60}, extra: []uint32{0}},
		{name: "OPT zero is not a record TTL", answer: []uint32{60}, wantTTL: 60},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := new(mdns.Msg)
			for section, ttls := range [][]uint32{test.answer, test.ns, test.extra} {
				for _, ttl := range ttls {
					record := &mdns.A{Hdr: mdns.RR_Header{Name: "ttl.example.", Rrtype: mdns.TypeA, Class: mdns.ClassINET, Ttl: ttl}}
					switch section {
					case 0:
						response.Answer = append(response.Answer, record)
					case 1:
						response.Ns = append(response.Ns, record)
					case 2:
						response.Extra = append(response.Extra, record)
					}
				}
			}
			response.SetEdns0(1232, false)
			if got := applyResponseOptions(mdns.Question{Qtype: mdns.TypeA}, response, adapter.DNSQueryOptions{}); got != test.wantTTL {
				t.Fatalf("response TTL = %d, want %d", got, test.wantTTL)
			}
			for _, records := range [][]mdns.RR{response.Answer, response.Ns, response.Extra} {
				for _, record := range records {
					if record.Header().Rrtype != mdns.TypeOPT && record.Header().Ttl != test.wantTTL {
						t.Fatalf("normalized record TTL = %d, want %d", record.Header().Ttl, test.wantTTL)
					}
				}
			}
		})
	}
}

func TestNegativeSOAAndExplicitRewriteTTL(t *testing.T) {
	for _, test := range []struct {
		name    string
		soaTTL  uint32
		minimum uint32
		rewrite *uint32
		wantTTL uint32
	}{
		{name: "SOA minimum", soaTTL: 60, minimum: 30, wantTTL: 30},
		{name: "SOA record TTL", soaTTL: 30, minimum: 60, wantTTL: 30},
		{name: "SOA zero minimum", soaTTL: 60},
		{name: "SOA zero record", minimum: 60},
		{name: "explicit positive rewrite", rewrite: new(uint32(90)), wantTTL: 90},
		{name: "explicit zero rewrite", soaTTL: 60, minimum: 30, rewrite: new(uint32)},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := &mdns.Msg{Ns: []mdns.RR{&mdns.SOA{Hdr: mdns.RR_Header{Rrtype: mdns.TypeSOA, Ttl: test.soaTTL}, Minttl: test.minimum}}}
			if got := applyResponseOptions(mdns.Question{Qtype: mdns.TypeA}, response, adapter.DNSQueryOptions{RewriteTTL: test.rewrite}); got != test.wantTTL {
				t.Fatalf("negative/rewrite TTL = %d, want %d", got, test.wantTTL)
			}
		})
	}
}
