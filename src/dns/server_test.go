package dns

import (
	"errors"
	"testing"

	"github.com/miekg/dns"

	"dns-geosite-proxy/classifier"
)

func TestQueryTypeBlocked(t *testing.T) {
	tests := []struct {
		name     string
		qtype    uint16
		strategy string
		want     bool
	}{
		{name: "aaaa blocked by ipv4", qtype: dns.TypeAAAA, strategy: "UseIPv4", want: true},
		{name: "a allowed by ipv4", qtype: dns.TypeA, strategy: "UseIPv4", want: false},
		{name: "a blocked by ipv6", qtype: dns.TypeA, strategy: "UseIPv6", want: true},
		{name: "aaaa allowed by ipv6", qtype: dns.TypeAAAA, strategy: "UseIPv6", want: false},
		{name: "any allowed by default", qtype: dns.TypeANY, strategy: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := queryTypeBlocked(tt.qtype, tt.strategy); got != tt.want {
				t.Fatalf("queryTypeBlocked() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFilterResponse(t *testing.T) {
	resp := new(dns.Msg)
	resp.Answer = []dns.RR{
		&dns.A{Hdr: dns.RR_Header{Name: "example.com.", Rrtype: dns.TypeA}, A: []byte{1, 2, 3, 4}},
		&dns.AAAA{Hdr: dns.RR_Header{Name: "example.com.", Rrtype: dns.TypeAAAA}, AAAA: []byte{0x20, 0x01}},
		&dns.CNAME{Hdr: dns.RR_Header{Name: "www.example.com.", Rrtype: dns.TypeCNAME}, Target: "example.com."},
	}

	filterResponse(resp, "UseIPv4")

	if len(resp.Answer) != 2 {
		t.Fatalf("len(resp.Answer) = %d, want 2", len(resp.Answer))
	}
	if _, ok := resp.Answer[0].(*dns.A); !ok {
		t.Fatalf("first answer = %T, want *dns.A", resp.Answer[0])
	}
	if _, ok := resp.Answer[1].(*dns.CNAME); !ok {
		t.Fatalf("second answer = %T, want *dns.CNAME", resp.Answer[1])
	}
}

func TestShouldTryFallback(t *testing.T) {
	result := classifier.Result{MatchType: "geosite"}

	if !shouldTryFallback(result, true, nil, errors.New("upstream failed")) {
		t.Fatal("expected fallback on upstream error")
	}

	resp := new(dns.Msg)
	resp.Rcode = dns.RcodeNameError
	if !shouldTryFallback(result, true, resp, nil) {
		t.Fatal("expected fallback on NXDOMAIN")
	}

	result.SkipFallback = true
	if shouldTryFallback(result, true, resp, nil) {
		t.Fatal("did not expect fallback when SkipFallback is true")
	}
}
