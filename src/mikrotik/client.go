// Package mikrotik provides a client for the MikroTik REST API.
//
// API reference: https://help.mikrotik.com/docs/display/ROS/REST+API
// Available since RouterOS 7.1beta4.
//
// Authentication: HTTP Basic Auth over HTTPS.
// Create a dedicated API user with minimal permissions:
//
//	/user/add name=dns-proxy group=api password=<secret> address=<container_ip>
//
// Security notes:
//   - TLSSkipVerify=true is common on MikroTik (self-signed cert by default).
//     Acceptable on a trusted LAN segment. For production: install a proper cert.
//   - Credentials are in config.json - use a file with restricted permissions (600).
//   - The API user should be restricted by source IP (address= in /user/add).
package mikrotik

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"dns-geosite-proxy/config"
)

// reTimeout matches RouterOS REST API timeout strings.
// RouterOS 7 uses letter-suffixed format: "1w6d23h56m59s"
// Each component is optional; at least one must be present.
//
//	"1w6d23h56m59s"  - full
//	"6d23h56m59s"    - no weeks
//	"23h56m59s"      - no weeks/days
//	"56m59s"         - minutes+seconds only
var reTimeout = regexp.MustCompile(
	`^(?:(\d+)w)?(?:(\d+)d)?(?:(\d+)h)?(?:(\d+)m)?(?:(\d+)s)?$`,
)

// Client is a MikroTik REST API client.
type Client struct {
	cfg        *config.MikrotikConfig
	httpClient *http.Client
	baseURL    string // trimmed trailing slash
}

// NewClient creates a Client from MikrotikConfig.
// The HTTP client is configured once and reused for all requests (connection pooling).
func NewClient(cfg *config.MikrotikConfig) *Client {
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			//nolint:gosec // InsecureSkipVerify is intentional and user-controlled
			InsecureSkipVerify: cfg.TLSSkipVerify,
		},
		// Keep connections alive for async pushes from multiple goroutines
		MaxIdleConns:    10,
		IdleConnTimeout: 30 * time.Second,
	}

	return &Client{
		cfg:     cfg,
		baseURL: strings.TrimRight(cfg.Address, "/"),
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   10 * time.Second,
		},
	}
}

// HTTP helpers
// get sends GET /rest/{path} and decodes the JSON response into v.
// v must be a pointer (typically *[]SomeStruct or *SomeStruct).
func (c *Client) get(path string, v interface{}) error {
	req, err := c.newRequest(http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	return c.do(req, v)
}

// put sends PUT /rest/{path} with JSON body.
// Used for creating new address-list entries (MikroTik REST uses PUT for add).
func (c *Client) put(path string, body interface{}) error {
	return c.doWithBody(http.MethodPut, path, body)
}

// patch sends PATCH /rest/{path}/{id} with JSON body.
// Used for updating existing address-list entries (e.g. refreshing timeout).
func (c *Client) patch(path string, body interface{}) error {
	return c.doWithBody(http.MethodPatch, path, body)
}

func (c *Client) doWithBody(method, path string, body interface{}) error {
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal body: %w", err)
	}

	req, err := c.newRequest(method, path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	return c.do(req, nil)
}

// newRequest builds an authenticated HTTP request to the MikroTik REST API.
func (c *Client) newRequest(method, path string, body io.Reader) (*http.Request, error) {
	url := c.baseURL + "/rest/" + strings.TrimPrefix(path, "/")

	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, fmt.Errorf("build request %s %s: %w", method, url, err)
	}

	req.SetBasicAuth(c.cfg.Username, c.cfg.Password)
	req.Header.Set("Accept", "application/json")

	return req, nil
}

// do executes req and optionally decodes the JSON response into v.
// Returns an error for HTTP 4xx/5xx responses with the response body included.
func (c *Client) do(req *http.Request, v interface{}) error {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", req.Method, req.URL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s %s: HTTP %d: %s",
			req.Method, req.URL, resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}

	if v != nil {
		if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}

	return nil
}

// Address-list REST paths
// apiPath returns the REST path for IPv4 or IPv6 firewall address-list.
//
//	IPv4: ip/firewall/address-list
//	IPv6: ipv6/firewall/address-list
func apiPath(isIPv6 bool) string {
	if isIPv6 {
		return "ipv6/firewall/address-list"
	}
	return "ip/firewall/address-list"
}

// Address-list entry model
// AddressListEntry mirrors the MikroTik REST API JSON for a firewall address-list entry.
// Field names match the RouterOS attribute names exactly.
type AddressListEntry struct {
	// .id is the MikroTik internal object ID, e.g. "*1" or "*A2"
	ID string `json:".id"`

	List    string `json:"list"`
	Address string `json:"address"`
	Comment string `json:"comment,omitempty"`

	// Timeout is the remaining TTL in MikroTik format: "13d23:59:59"
	// Empty string means the entry is permanent (no expiry).
	Timeout string `json:"timeout,omitempty"`

	// Dynamic=true means the entry was created by a firewall rule, not manually.
	// We only manage non-dynamic entries.
	Dynamic  string `json:"dynamic,omitempty"`  // "true" / "false" (string in REST API)
	Disabled string `json:"disabled,omitempty"` // "true" / "false"
}

// Time helpers
// FormatTimeout converts time.Duration to MikroTik timeout string.
// Uses the same letter-suffixed format that RouterOS 7 REST API returns.
//
//	336h → "2w0d0h0m0s"
//	72h  → "0w3d0h0m0s"
//	90m  → "0w0d0h1m30s"
func FormatTimeout(d time.Duration) string {
	total := int(d.Seconds())
	weeks := total / (7 * 86400)
	total %= 7 * 86400
	days := total / 86400
	total %= 86400
	hours := total / 3600
	total %= 3600
	mins := total / 60
	secs := total % 60
	return fmt.Sprintf("%dw%dd%dh%dm%ds", weeks, days, hours, mins, secs)
}

// ParseTimeout parses a MikroTik timeout string into time.Duration.
// Returns 0 for empty strings (permanent entries) or unparseable values.
//
// RouterOS 7 REST API returns letter-suffixed format: "1w6d23h56m59s".
// Each component is optional. Parsing is done via reTimeout regexp.
func ParseTimeout(s string) time.Duration {
	if s == "" {
		return 0
	}

	m := reTimeout.FindStringSubmatch(s)
	// m[0]=full match, m[1]=weeks, m[2]=days, m[3]=hours, m[4]=mins, m[5]=secs
	if m == nil || m[0] == "" {
		return 0
	}

	parseInt := func(s string) time.Duration {
		if s == "" {
			return 0
		}
		v, _ := strconv.Atoi(s)
		return time.Duration(v)
	}

	return parseInt(m[1])*7*24*time.Hour +
		parseInt(m[2])*24*time.Hour +
		parseInt(m[3])*time.Hour +
		parseInt(m[4])*time.Minute +
		parseInt(m[5])*time.Second
}
