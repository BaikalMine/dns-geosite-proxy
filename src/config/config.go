// Package config provides configuration loading and validation.
// The JSON format is intentionally close to xray/v2ray dns+routing config
// so users familiar with those tools can adapt their existing configs.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// Config is the root configuration structure.
type Config struct {
	// Listen address for the DNS server (default: ":53")
	Listen string `json:"listen"`

	// GeositePath is the path to dlc.dat (geosite database)
	GeositePath string `json:"geosite_path"`

	// AsyncPush enables asynchronous MikroTik address-list updates.
	// When true, DNS responses are returned immediately without waiting
	// for the MikroTik REST API call to complete.
	// Recommended: true (avoids latency on slow MikroTik connections)
	AsyncPush bool `json:"async_push"`

	// LogLevel controls verbosity: debug, info, warn, error
	LogLevel string `json:"log_level"`

	DNS      DNSConfig      `json:"dns"`
	Mikrotik MikrotikConfig `json:"mikrotik"`
}

// DNSConfig groups upstream DNS server definitions.
type DNSConfig struct {
	Servers []DNSServer `json:"servers"`
}

// DNSServer defines an upstream resolver with optional domain matching rules.
// Rules are evaluated top-to-bottom; first match wins.
// A server with no domains and fallback=true acts as the default resolver.
type DNSServer struct {
	// Address of the upstream resolver.
	// Formats:
	//   "77.88.8.8"                      - plain UDP (port 53 assumed)
	//   "77.88.8.8:53"                   - plain UDP explicit port
	//   "tcp://77.88.8.8"                - force TCP
	//   "https://1.1.1.1/dns-query"      - DNS-over-HTTPS (DoH)
	Address string `json:"address"`

	// Domains is a list of matching rules for this upstream.
	// Rule syntax (same as xray/v2ray):
	//   "geosite:category-ru"   - lookup in dlc.dat (domain+subdomain match)
	//   "full:example.com"      - exact FQDN match
	//   "domain:example.com"    - domain + all subdomains
	//   "keyword:tracker"       - substring match
	//   "regexp:.*\\.ru$"       - regex match (use sparingly, slower)
	//   "example.com"           - shorthand for domain:example.com
	Domains []string `json:"domains,omitempty"`

	// Tag is the routing tag assigned when this server is matched.
	// Maps to mikrotik.address_lists keys and controls which address-list
	// resolved IPs are pushed to.
	// Well-known tags: "direct", "proxy", "block"
	Tag string `json:"tag"`

	// QueryStrategy restricts the record types to resolve.
	// "UseIPv4" - only A records
	// "UseIPv6" - only AAAA records
	// "UseIP"   - both A and AAAA
	QueryStrategy string `json:"query_strategy,omitempty"`

	// Fallback marks this server as the catch-all when no rule matches.
	// Only one server should have fallback=true (last one is used).
	Fallback bool `json:"fallback,omitempty"`

	// SkipFallback prevents falling through to the next server on NXDOMAIN.
	SkipFallback bool `json:"skip_fallback,omitempty"`
}

// MikrotikConfig holds MikroTik REST API connection and address-list settings.
type MikrotikConfig struct {
	// Address is the base URL of the MikroTik REST API.
	// Example: "https://192.168.88.1"
	Address string `json:"address"`

	// Username and Password for HTTP Basic Auth.
	// Create a dedicated API user with limited permissions:
	//   /user/add name=dns-proxy group=api password=... address=<container_ip>
	Username string `json:"username"`
	Password string `json:"password"`

	// TLSSkipVerify disables TLS certificate validation.
	// MikroTik uses self-signed certs by default - set true unless you
	// have installed a proper certificate.
	// Security note: MITM risk on untrusted networks; acceptable on LAN.
	TLSSkipVerify bool `json:"tls_skip_verify"`

	// AddressLists maps routing tags to MikroTik address-list configurations.
	// Tag "direct" maps to nil → no push to MikroTik.
	// Tag "block"  may push to a block list (optional).
	// Example key: "proxy" → list "vpn_routes"
	AddressLists map[string]*AddressListConfig `json:"address_lists"`

	// IPv4/IPv6 enable address family processing independently.
	IPv4 AddressFamilyConfig `json:"ipv4"`
	IPv6 AddressFamilyConfig `json:"ipv6"`
}

// AddressListConfig maps a routing tag to a MikroTik address-list with TTL policy.
type AddressListConfig struct {
	// List is the MikroTik address-list name (ip/firewall/address-list).
	// Example: "vpn_routes"
	List string `json:"list"`

	// TTL is how long the entry lives in the address-list.
	// MikroTik "timeout" field. Use Go duration strings: "336h" = 14 days.
	TTL Duration `json:"ttl"`

	// RefreshThreshold: if an existing entry's remaining TTL drops below
	// this value, the TTL is refreshed to TTL.
	// Example: TTL=336h + RefreshThreshold=72h means:
	//   renew only if less than 3 days remain of the 14-day lifetime.
	RefreshThreshold Duration `json:"refresh"`
}

// AddressFamilyConfig enables per-address-family processing.
type AddressFamilyConfig struct {
	Enabled bool `json:"enabled"`
}

// Duration wraps time.Duration to support JSON string encoding ("336h", "72h", etc.)
type Duration struct {
	time.Duration
}

// UnmarshalJSON parses a Go duration string from JSON (e.g. "336h", "72h0m0s").
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	dur, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	d.Duration = dur
	return nil
}

// MarshalJSON serializes Duration back to a Go duration string.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.Duration.String())
}

// Load reads, parses, and validates the configuration file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config %q: %w", path, err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config JSON: %w", err)
	}

	if err := cfg.applyDefaults(); err != nil {
		return nil, fmt.Errorf("config validation: %w", err)
	}

	return &cfg, nil
}

// applyDefaults fills in optional fields with sensible defaults and validates required ones.
func (c *Config) applyDefaults() error {
	if c.Listen == "" {
		c.Listen = ":53"
	}
	if c.GeositePath == "" {
		c.GeositePath = "/data/dlc.dat"
	}
	if c.LogLevel == "" {
		c.LogLevel = "info"
	}
	if c.Mikrotik.Address == "" {
		return fmt.Errorf("mikrotik.address is required")
	}
	if c.Mikrotik.Username == "" {
		return fmt.Errorf("mikrotik.username is required")
	}
	if len(c.DNS.Servers) == 0 {
		return fmt.Errorf("dns.servers must have at least one entry")
	}
	// Ensure at least one fallback server
	hasFallback := false
	for _, s := range c.DNS.Servers {
		if s.Fallback {
			hasFallback = true
			break
		}
	}
	if !hasFallback {
		return fmt.Errorf("dns.servers: at least one server must have fallback=true")
	}
	return nil
}
