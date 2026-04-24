package geosite

import "testing"

func TestMatchDomainUsesIndexes(t *testing.T) {
	db := &Database{
		categories: map[string]*GeoSite{
			"TEST": {
				CategoryCode: "TEST",
				Domains: []Domain{
					{Type: DomainTypeFull, Value: "exact.example"},
					{Type: DomainTypeDomain, Value: "suffix.example"},
					{Type: DomainTypePlain, Value: "keyword"},
					{Type: DomainTypeRegex, Value: `^re-[0-9]+\.example$`},
				},
			},
		},
	}
	db.buildIndexes()

	matches := []string{
		"exact.example",
		"www.suffix.example",
		"has-keyword.example",
		"re-42.example",
	}
	for _, domain := range matches {
		if !db.MatchDomain(domain, "test") {
			t.Fatalf("MatchDomain(%q) = false, want true", domain)
		}
	}

	nonMatches := []string{
		"www.exact.example",
		"suffix.example.evil",
		"re-abc.example",
	}
	for _, domain := range nonMatches {
		if db.MatchDomain(domain, "test") {
			t.Fatalf("MatchDomain(%q) = true, want false", domain)
		}
	}
}
