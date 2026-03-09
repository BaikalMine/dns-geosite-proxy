// Package classifier matches a DNS domain name against the configured
// dns.servers[] rules and returns the routing tag + upstream to use.
//
// Rule evaluation order:
//  1. Iterate servers top-to-bottom
//  2. For each server, check all domain rules in order
//  3. First match wins → return that server's tag + upstream
//  4. If no server matches → use the server marked as fallback=true
//
// Rule prefix syntax (same as xray/v2ray):
//
//	geosite:category-ru  - dlc.dat lookup (Domain+subdomain match inside category)
//	full:example.com     - exact FQDN match
//	domain:example.com   - domain + all subdomains
//	keyword:tracker      - substring match anywhere in the domain
//	regexp:.*\.ru$       - Go regexp match (pre-compiled in NewClassifier)
//	example.com          - shorthand for domain:example.com
package classifier

import (
	"regexp"
	"strings"

	"dns-geosite-proxy/config"
	"dns-geosite-proxy/geosite"
)

// Result is the output of Classify() for a single domain query.
type Result struct {
	// Tag is the routing decision: "proxy", "direct", "block", etc.
	Tag string

	// Upstream is the DNS server address to forward the query to.
	// Empty string means "no upstream" (e.g. for "block" tag → return NXDOMAIN).
	Upstream string

	// QueryStrategy restricts resolved record types: UseIPv4, UseIPv6, UseIP.
	QueryStrategy string

	// SkipFallback, when true, means we should not try the next server on failure.
	SkipFallback bool

	// MatchType is the type of the rule that matched this domain.
	// Values: "geosite", "full", "domain", "keyword", "regexp", "plain", "fallback".
	MatchType string

	// MatchValue is the value part of the matched rule.
	// For geosite: "category-ru". For domain/full: "example.com". For fallback: "".
	MatchValue string
}

// compiledRule is a pre-parsed form of a single domain rule string.
type compiledRule struct {
	ruleType string // "geosite", "full", "domain", "keyword", "regexp", "plain"
	value    string // lowercased value after prefix stripping
	re       *regexp.Regexp
}

// compiledServer is a server entry with pre-parsed rules.
type compiledServer struct {
	cfg   config.DNSServer
	rules []compiledRule
}

// Classifier is the domain → routing decision engine.
// Thread-safe for concurrent reads; immutable after construction.
type Classifier struct {
	servers  []compiledServer
	fallback *compiledServer // server with fallback=true
	db       *geosite.Database
}

// New builds a Classifier from config and geosite database.
// Pre-compiles all rule prefixes and regex patterns for fast query-time matching.
func New(cfg *config.DNSConfig, db *geosite.Database) *Classifier {
	c := &Classifier{db: db}

	for _, srv := range cfg.Servers {
		cs := compiledServer{cfg: srv}
		for _, rule := range srv.Domains {
			cs.rules = append(cs.rules, compileRule(rule))
		}

		if srv.Fallback {
			// Allocate a copy on the heap - 'copy' is a Go built-in, avoid shadowing it.
			// The address of a local variable escapes to heap when taken (&fb),
			// so this is safe and well-defined in Go.
			fb := cs
			c.fallback = &fb
		} else {
			c.servers = append(c.servers, cs)
		}
	}

	return c
}

// Classify returns the routing result for the given domain (FQDN or plain).
func (c *Classifier) Classify(domain string) Result {
	// Normalize: lowercase + strip trailing dot (miekg/dns FQDN format)
	domain = strings.ToLower(strings.TrimSuffix(domain, "."))

	// Try all non-fallback servers in order
	for i := range c.servers {
		srv := &c.servers[i]
		if rule, ok := c.matchServer(domain, srv); ok {
			return Result{
				Tag:           srv.cfg.Tag,
				Upstream:      srv.cfg.Address,
				QueryStrategy: srv.cfg.QueryStrategy,
				SkipFallback:  srv.cfg.SkipFallback,
				MatchType:     rule.ruleType,
				MatchValue:    rule.value,
			}
		}
	}

	// No match → use fallback (catch-all server)
	if c.fallback != nil {
		return Result{
			Tag:           c.fallback.cfg.Tag,
			Upstream:      c.fallback.cfg.Address,
			QueryStrategy: c.fallback.cfg.QueryStrategy,
			SkipFallback:  false,
			MatchType:     "fallback",
			MatchValue:    "",
		}
	}

	// Config validation should prevent this, but handle defensively
	return Result{Tag: "direct", MatchType: "fallback"}
}

// matchServer returns the first matching rule and true, or zero value and false.
// An empty rules list never matches (use fallback=true for a catch-all).
func (c *Classifier) matchServer(domain string, srv *compiledServer) (compiledRule, bool) {
	for i := range srv.rules {
		if c.matchRule(domain, &srv.rules[i]) {
			return srv.rules[i], true
		}
	}
	return compiledRule{}, false
}

// matchRule evaluates domain against a single compiled rule.
func (c *Classifier) matchRule(domain string, r *compiledRule) bool {
	switch r.ruleType {
	case "geosite":
		// Delegate to geosite database (thread-safe RLock inside)
		return c.db.MatchDomain(domain, r.value)

	case "full":
		return domain == r.value

	case "domain", "plain":
		// "domain:example.com" matches "example.com" and "*.example.com"
		return domain == r.value || strings.HasSuffix(domain, "."+r.value)

	case "keyword":
		return strings.Contains(domain, r.value)

	case "regexp":
		if r.re != nil {
			return r.re.MatchString(domain)
		}
		return false
	}
	return false
}

// compileRule parses a rule string into a compiledRule for fast matching.
// Regex patterns are compiled here once rather than on every DNS query.
func compileRule(rule string) compiledRule {
	lower := strings.ToLower(rule)

	switch {
	case strings.HasPrefix(lower, "geosite:"):
		return compiledRule{
			ruleType: "geosite",
			value:    strings.TrimPrefix(lower, "geosite:"),
		}

	case strings.HasPrefix(lower, "full:"):
		return compiledRule{
			ruleType: "full",
			value:    strings.TrimPrefix(lower, "full:"),
		}

	case strings.HasPrefix(lower, "domain:"):
		return compiledRule{
			ruleType: "domain",
			value:    strings.TrimPrefix(lower, "domain:"),
		}

	case strings.HasPrefix(lower, "keyword:"):
		return compiledRule{
			ruleType: "keyword",
			value:    strings.TrimPrefix(lower, "keyword:"),
		}

	case strings.HasPrefix(lower, "regexp:"):
		pattern := strings.TrimPrefix(lower, "regexp:")
		re, _ := regexp.Compile(pattern) // nil on compile error → matchRule skips it
		return compiledRule{
			ruleType: "regexp",
			value:    pattern,
			re:       re,
		}

	default:
		// No prefix → treat as domain:xxx (most common case in configs)
		return compiledRule{
			ruleType: "plain",
			value:    lower,
		}
	}
}
