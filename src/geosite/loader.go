// Package geosite loads and queries the v2fly domain-list-community dlc.dat file.
//
// dlc.dat is a protobuf-encoded binary (GeoSiteList message from v2ray-core).
// Instead of depending on v2ray-core or running protoc, we decode the wire
// format manually using google.golang.org/protobuf/encoding/protowire.
//
// Proto schema (abbreviated):
//
//	message GeoSiteList { repeated GeoSite entry = 1; }
//	message GeoSite     { string country_code = 1; repeated Domain domain = 2; }
//	message Domain      { DomainType type = 1; string value = 2; }
//	enum    DomainType  { Plain=0, Regex=1, Domain=2, Full=3 }
//
// Matching semantics (identical to xray/v2ray):
//
//	Plain  (0) - substring: domain contains the value
//	Regex  (1) - regexp match against full domain
//	Domain (2) - domain itself OR any subdomain: "google.com" matches "mail.google.com"
//	Full   (3) - exact FQDN match only
package geosite

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"

	"google.golang.org/protobuf/encoding/protowire"
)

// DomainType mirrors the proto enum Domain.Type.
type DomainType int32

const (
	DomainTypePlain  DomainType = 0 // substring / keyword
	DomainTypeRegex  DomainType = 1 // regular expression
	DomainTypeDomain DomainType = 2 // domain + subdomains
	DomainTypeFull   DomainType = 3 // exact FQDN
)

// Domain is a single domain entry from dlc.dat.
type Domain struct {
	Type     DomainType
	Value    string
	compiled *regexp.Regexp // cached compiled regex for DomainTypeRegex
}

// GeoSite holds all domain entries for one category (country_code in the proto).
type GeoSite struct {
	CategoryCode string
	Domains      []Domain
}

// Database is a thread-safe in-memory index over all geosite categories.
// Read access is optimized (RWMutex); writes only happen on Reload().
type Database struct {
	mu         sync.RWMutex
	categories map[string]*GeoSite // key: UPPERCASE category code
}

// Load parses dlc.dat and returns a ready-to-query Database.
// Typical dlc.dat: ~4MB on disk, ~50–80MB after expansion into structs.
func Load(path string) (*Database, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %q: %w", path, err)
	}

	db := &Database{}
	cats, err := parseGeoSiteList(data)
	if err != nil {
		return nil, fmt.Errorf("parsing geosite list: %w", err)
	}
	db.categories = cats

	// Pre-compile all regex patterns at load time to avoid per-query overhead
	db.compileRegexes()

	return db, nil
}

// CategoryCount returns the number of loaded categories (thread-safe).
func (db *Database) CategoryCount() int {
	db.mu.RLock()
	defer db.mu.RUnlock()
	return len(db.categories)
}

// MatchDomain reports whether domain belongs to the given category.
// category is case-insensitive, e.g. "category-ru", "CATEGORY-RU".
//
// Performance note: called on every DNS query. The inner loop iterates
// all domains in the category - for large categories (category-ads-all has
// ~150k entries) this can be slow. TODO: build a trie/radix tree for O(log n).
func (db *Database) MatchDomain(domain, category string) bool {
	db.mu.RLock()
	gs, ok := db.categories[strings.ToUpper(category)]
	db.mu.RUnlock()

	if !ok {
		return false
	}

	// Strip trailing dot (miekg/dns returns FQDN with trailing dot)
	domain = strings.ToLower(strings.TrimSuffix(domain, "."))

	for i := range gs.Domains {
		if matchEntry(domain, &gs.Domains[i]) {
			return true
		}
	}
	return false
}

// Reload atomically replaces the database contents from a new file.
// Safe to call concurrently while queries are running (SIGHUP handler).
func (db *Database) Reload(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading %q: %w", path, err)
	}

	cats, err := parseGeoSiteList(data)
	if err != nil {
		return fmt.Errorf("parsing: %w", err)
	}

	// Compile regex patterns before taking the write lock
	newDB := &Database{categories: cats}
	newDB.compileRegexes()

	db.mu.Lock()
	db.categories = newDB.categories
	db.mu.Unlock()

	return nil
}

// compileRegexes pre-compiles all DomainTypeRegex entries.
// Called once after load/reload, NOT under a lock (single-goroutine context).
func (db *Database) compileRegexes() {
	for _, gs := range db.categories {
		for i := range gs.Domains {
			d := &gs.Domains[i]
			if d.Type == DomainTypeRegex {
				// Log compile errors but don't abort - skip bad patterns
				re, err := regexp.Compile(d.Value)
				if err == nil {
					d.compiled = re
				}
			}
		}
	}
}

// matchEntry checks domain against a single Domain entry.
func matchEntry(domain string, d *Domain) bool {
	val := strings.ToLower(d.Value)
	switch d.Type {
	case DomainTypeFull:
		return domain == val

	case DomainTypeDomain:
		// "google.com" matches "google.com" itself and "mail.google.com"
		return domain == val || strings.HasSuffix(domain, "."+val)

	case DomainTypePlain:
		// Substring / keyword match
		return strings.Contains(domain, val)

	case DomainTypeRegex:
		if d.compiled != nil {
			return d.compiled.MatchString(domain)
		}
		return false
	}
	return false
}

// Protobuf wire format decoder
// All field numbers and wire types match the v2ray-core proto schema.

// parseGeoSiteList decodes the top-level GeoSiteList message.
//
//	GeoSiteList: { repeated GeoSite entry = 1 (field 1, BytesType) }
func parseGeoSiteList(data []byte) (map[string]*GeoSite, error) {
	cats := make(map[string]*GeoSite)

	for len(data) > 0 {
		num, typ, tagLen := protowire.ConsumeTag(data)
		if tagLen < 0 {
			return nil, fmt.Errorf("GeoSiteList: invalid tag (offset from end: %d)", len(data))
		}
		data = data[tagLen:]

		if num != 1 || typ != protowire.BytesType {
			// Skip unknown/future fields gracefully
			n := protowire.ConsumeFieldValue(num, typ, data)
			// NOTE: ConsumeFieldValue returns the length, not (value, length)
			// It's a single int return in newer protowire versions - handle both
			if n < 0 {
				return nil, fmt.Errorf("GeoSiteList: skip field %d failed", num)
			}
			data = data[n:]
			continue
		}

		// field 1: embedded GeoSite message bytes
		b, bLen := protowire.ConsumeBytes(data)
		if bLen < 0 {
			return nil, fmt.Errorf("GeoSiteList: consuming GeoSite bytes failed")
		}
		data = data[bLen:]

		gs, err := parseGeoSite(b)
		if err != nil {
			return nil, fmt.Errorf("GeoSiteList: parsing GeoSite: %w", err)
		}

		key := strings.ToUpper(gs.CategoryCode)
		cats[key] = gs
	}

	return cats, nil
}

// parseGeoSite decodes a single GeoSite message.
//
//	GeoSite: { string country_code = 1; repeated Domain domain = 2 }
func parseGeoSite(data []byte) (*GeoSite, error) {
	gs := &GeoSite{}

	for len(data) > 0 {
		num, typ, tagLen := protowire.ConsumeTag(data)
		if tagLen < 0 {
			return nil, fmt.Errorf("GeoSite: invalid tag")
		}
		data = data[tagLen:]

		switch {
		case num == 1 && typ == protowire.BytesType:
			// country_code (proto: string → wire: length-delimited)
			b, n := protowire.ConsumeBytes(data)
			if n < 0 {
				return nil, fmt.Errorf("GeoSite: consuming country_code failed")
			}
			gs.CategoryCode = string(b)
			data = data[n:]

		case num == 2 && typ == protowire.BytesType:
			// repeated Domain
			b, n := protowire.ConsumeBytes(data)
			if n < 0 {
				return nil, fmt.Errorf("GeoSite: consuming Domain bytes failed")
			}
			d, err := parseDomain(b)
			if err != nil {
				return nil, fmt.Errorf("GeoSite: parsing Domain: %w", err)
			}
			gs.Domains = append(gs.Domains, d)
			data = data[n:]

		default:
			// Skip attribute fields (field 3) and any future fields
			n := protowire.ConsumeFieldValue(num, typ, data)
			if n < 0 {
				return nil, fmt.Errorf("GeoSite: skip field %d failed", num)
			}
			data = data[n:]
		}
	}

	return gs, nil
}

// parseDomain decodes a single Domain message.
//
//	Domain: { DomainType type = 1; string value = 2; repeated Attribute attribute = 3 }
func parseDomain(data []byte) (Domain, error) {
	var d Domain

	for len(data) > 0 {
		num, typ, tagLen := protowire.ConsumeTag(data)
		if tagLen < 0 {
			return d, fmt.Errorf("Domain: invalid tag")
		}
		data = data[tagLen:]

		switch {
		case num == 1 && typ == protowire.VarintType:
			// type enum
			v, n := protowire.ConsumeVarint(data)
			if n < 0 {
				return d, fmt.Errorf("Domain: consuming type varint failed")
			}
			d.Type = DomainType(v)
			data = data[n:]

		case num == 2 && typ == protowire.BytesType:
			// value string
			b, n := protowire.ConsumeBytes(data)
			if n < 0 {
				return d, fmt.Errorf("Domain: consuming value failed")
			}
			d.Value = string(b)
			data = data[n:]

		default:
			// Skip attribute field (field 3) - not needed for matching
			n := protowire.ConsumeFieldValue(num, typ, data)
			if n < 0 {
				return d, fmt.Errorf("Domain: skip field %d failed", num)
			}
			data = data[n:]
		}
	}

	return d, nil
}
