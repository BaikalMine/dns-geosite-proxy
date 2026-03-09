// addresslist.go - EnsureEntry logic for MikroTik firewall address-list.
//
// Decision tree for each (ip, list, ttl) tuple:
//
//	┌─ Entry exists in address-list?
//	│
//	├─ NO  → PUT  (add new entry with full TTL)
//	│
//	└─ YES → remaining TTL < refresh?
//	         ├─ YES → PATCH (update timeout to full TTL)
//	         └─ NO  → skip  (entry is fresh enough)
//
// This avoids unnecessary API calls on every DNS resolution of
// frequently-visited domains while still keeping entries alive.
package mikrotik

import (
	"fmt"
	"net"
	"net/url"
	"time"

	"dns-geosite-proxy/config"
	"dns-geosite-proxy/logger"
)

// EnsureEntry adds ip to the MikroTik address-list or refreshes its TTL.
// isIPv6=true → ipv6/firewall/address-list; false → ip/firewall/address-list.
// comment is stored in the address-list entry, format: tag:matchvalue:matchtype:domain
//
// Thread-safe: each call uses independent HTTP requests; no shared mutable state.
func (c *Client) EnsureEntry(ip net.IP, listCfg *config.AddressListConfig, isIPv6 bool, comment string) error {
	ipStr := ip.String()
	path := apiPath(isIPv6)

	existing, err := c.findEntry(path, listCfg.List, ipStr)
	if err != nil {
		return fmt.Errorf("findEntry %s in list=%s: %w", ipStr, listCfg.List, err)
	}

	if existing == nil {
		// New entry - add with full TTL and comment
		if err := c.addEntry(path, listCfg.List, ipStr, listCfg.TTL.Duration, comment); err != nil {
			return err
		}
		logger.Info("[mikrotik] added   %s → list=%s", ipStr, listCfg.List)
		return nil
	}

	// Existing entry - check if TTL refresh is needed
	remaining := ParseTimeout(existing.Timeout)
	logger.Debug("[mikrotik] ttl    %s list=%s raw=%q remaining=%v threshold=%v",
		ipStr, listCfg.List, existing.Timeout, remaining.Round(time.Second), listCfg.RefreshThreshold.Duration)

	if remaining < listCfg.RefreshThreshold.Duration {
		// Less than threshold remaining → renew TTL and update comment
		// (comment may differ if the same IP was resolved for a different domain)
		if err := c.updateEntry(path, existing.ID, listCfg.TTL.Duration, comment); err != nil {
			return err
		}
		logger.Info("[mikrotik] updated %s → list=%s (was %.1fd left)",
			ipStr, listCfg.List, remaining.Hours()/24)
		return nil
	}

	// Entry is fresh enough → nothing to do
	logger.Debug("[mikrotik] skip   %s list=%s (fresh, %.1fd left)",
		ipStr, listCfg.List, remaining.Hours()/24)
	return nil
}

// findEntry queries address-list for a specific list+address combination.
// Returns nil if no matching entry is found.
//
// MikroTik REST supports query filtering:
//
//	GET /rest/ip/firewall/address-list?list=vpn_routes&address=1.2.3.4
func (c *Client) findEntry(path, list, address string) (*AddressListEntry, error) {
	queryPath := fmt.Sprintf("%s?list=%s&address=%s",
		path,
		url.QueryEscape(list),
		url.QueryEscape(address),
	)

	var entries []AddressListEntry
	if err := c.get(queryPath, &entries); err != nil {
		return nil, err
	}

	if len(entries) == 0 {
		return nil, nil
	}

	// list+address should be unique; take first match
	return &entries[0], nil
}

// addEntry creates a new address-list entry.
//
// PUT /rest/ip/firewall/address-list
//
//	body: { "list": "vpn_routes", "address": "1.2.3.4",
//	        "timeout": "14d00:00:00", "comment": "proxy:category-ru:geosite:mail.yandex.ru" }
//
// Note: MikroTik REST API uses PUT for creating address-list entries.
// Omitting "timeout" creates a permanent (non-expiring) entry.
func (c *Client) addEntry(path, list, address string, ttl time.Duration, comment string) error {
	body := map[string]interface{}{
		"list":    list,
		"address": address,
		"comment": comment,
	}

	if ttl > 0 {
		body["timeout"] = FormatTimeout(ttl)
	}

	return c.put(path, body)
}

// updateEntry refreshes both the TTL and comment of an existing entry.
// Comment is updated to reflect the most recent domain resolution that triggered the refresh.
//
// PATCH /rest/ip/firewall/address-list/*1
//
//	body: { "timeout": "14d00:00:00", "comment": "proxy:category-ru:geosite:mail.yandex.ru" }
func (c *Client) updateEntry(path, id string, ttl time.Duration, comment string) error {
	if ttl == 0 {
		// Cannot convert a timed entry to permanent via PATCH alone.
		// Would require delete+recreate. Skip for now - log at call site if needed.
		return nil
	}

	return c.patch(path+"/"+id, map[string]string{
		"timeout": FormatTimeout(ttl),
		"comment": comment,
	})
}
