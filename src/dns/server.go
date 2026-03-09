// Package dns implements the DNS proxy server using miekg/dns.
//
// Each incoming query is processed as follows:
//  1. Classify the domain (tag + upstream) via the Classifier
//  2. If tag == "block" → return NXDOMAIN immediately (no upstream query)
//  3. Forward to the selected upstream (UDP; DoH via net/http)
//  4. Write DNS response to the client
//  5. Extract A/AAAA records from the response
//  6. Push IPs to MikroTik address-list (async goroutine if async_push=true)
//
// The server listens on both UDP and TCP on the configured address.
// ReloadGeosite() can be called from a SIGHUP handler - it rebuilds the
// Classifier atomically while queries continue to be served.
package dns

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"

	"dns-geosite-proxy/classifier"
	"dns-geosite-proxy/config"
	"dns-geosite-proxy/geosite"
	"dns-geosite-proxy/logger"
	"dns-geosite-proxy/mikrotik"
)

// Server is the DNS proxy. Holds both a UDP and a TCP dns.Server.
type Server struct {
	cfg      *config.Config
	db       *geosite.Database
	mtClient *mikrotik.Client

	// mu protects clf during ReloadGeosite() rebuilds.
	// Read-lock is taken on every DNS query; write-lock only on SIGHUP.
	mu  sync.RWMutex
	clf *classifier.Classifier

	udpSrv *dns.Server
	tcpSrv *dns.Server

	// Shared HTTP client for DoH upstreams (connection pool reuse)
	httpClient *http.Client
}

// NewServer constructs a Server. Does not start listening yet.
func NewServer(cfg *config.Config, db *geosite.Database, mt *mikrotik.Client) *Server {
	s := &Server{
		cfg:      cfg,
		db:       db,
		mtClient: mt,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
	s.clf = classifier.New(&cfg.DNS, db)
	return s
}

// Start launches UDP and TCP listeners and blocks until one of them fails.
func (s *Server) Start() error {
	mux := dns.NewServeMux()
	mux.HandleFunc(".", s.handleDNS)

	s.udpSrv = &dns.Server{
		Addr:    s.cfg.Listen,
		Net:     "udp",
		Handler: mux,
	}
	s.tcpSrv = &dns.Server{
		Addr:         s.cfg.Listen,
		Net:          "tcp",
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 2)
	go func() { errCh <- s.udpSrv.ListenAndServe() }()
	go func() { errCh <- s.tcpSrv.ListenAndServe() }()

	return <-errCh
}

// Stop shuts down both listeners gracefully.
func (s *Server) Stop() {
	if s.udpSrv != nil {
		_ = s.udpSrv.Shutdown()
	}
	if s.tcpSrv != nil {
		_ = s.tcpSrv.Shutdown()
	}
}

// ReloadGeosite reloads dlc.dat and rebuilds the Classifier atomically.
// Called from main() on SIGHUP. Safe while queries are in-flight.
func (s *Server) ReloadGeosite(path string) error {
	if err := s.db.Reload(path); err != nil {
		return fmt.Errorf("reload db: %w", err)
	}

	newClf := classifier.New(&s.cfg.DNS, s.db)

	s.mu.Lock()
	s.clf = newClf
	s.mu.Unlock()

	return nil
}

// handleDNS is the miekg/dns handler - entry point for every DNS query.
func (s *Server) handleDNS(w dns.ResponseWriter, req *dns.Msg) {
	if len(req.Question) == 0 {
		dns.HandleFailed(w, req)
		return
	}

	q := req.Question[0]
	domain := q.Name // FQDN with trailing dot, e.g. "google.com."

	// Classify: determine tag + upstream for this domain
	s.mu.RLock()
	result := s.clf.Classify(domain)
	s.mu.RUnlock()

	// Log every query at INFO: domain, tag, matched rule, upstream
	logger.Info("[dns] query %-40s tag=%-8s match=%s:%s upstream=%s",
		strings.TrimSuffix(domain, "."),
		result.Tag,
		result.MatchType, result.MatchValue,
		result.Upstream,
	)

	// Block tag: return NXDOMAIN, no upstream query
	if result.Tag == "block" {
		resp := new(dns.Msg)
		resp.SetRcode(req, dns.RcodeNameError)
		resp.RecursionAvailable = true
		_ = w.WriteMsg(resp)
		return
	}

	// Forward to upstream DNS
	resp, err := s.forward(req, result.Upstream)
	if err != nil {
		logger.Warn("[dns] forward %s via %s: %v", strings.TrimSuffix(domain, "."), result.Upstream, err)
		dns.HandleFailed(w, req)
		return
	}

	// Return response to client first (low latency)
	_ = w.WriteMsg(resp)

	// Push resolved IPs to MikroTik (if tag is not "direct")
	if result.Tag != "direct" && result.Tag != "" {
		ips := extractIPs(resp, result.QueryStrategy)
		if len(ips) > 0 {
			// Comment format: tag:matchvalue:matchtype:actual-domain
			// Example: proxy:category-ru:geosite:mail.yandex.ru
			//          proxy:google.com:domain:maps.google.com
			//          proxy::fallback:somesite.com
			comment := fmt.Sprintf("%s:%s:%s:%s",
				result.Tag, result.MatchValue, result.MatchType,
				strings.TrimSuffix(domain, "."),
			)
			logger.Info("[dns] resolved  %-40s → %v",
				strings.TrimSuffix(domain, "."), ips,
			)
			if s.cfg.AsyncPush {
				go s.pushIPs(result.Tag, comment, ips)
			} else {
				s.pushIPs(result.Tag, comment, ips)
			}
		}
	}
}

// forward sends req to the upstream and returns the response.
// Supports plain DNS (UDP/TCP) and DNS-over-HTTPS (https:// prefix).
//
// Security note: upstream address comes from config (trusted).
// DoH upstream certificates are validated unless tls_skip_verify is set
// on the MikroTik client - the DNS server itself always verifies.
func (s *Server) forward(req *dns.Msg, upstream string) (*dns.Msg, error) {
	if upstream == "" {
		return nil, fmt.Errorf("no upstream configured")
	}

	// DoH: delegate to forwardDoH
	if strings.HasPrefix(upstream, "https://") {
		return s.forwardDoH(req, upstream)
	}

	// Plain DNS over UDP (with automatic TCP retry on truncation)
	addr := upstream
	if _, _, err := net.SplitHostPort(addr); err != nil {
		// No port specified - add default :53
		addr = net.JoinHostPort(addr, "53")
	}

	client := &dns.Client{
		Net:     "udp",
		Timeout: 5 * time.Second,
	}

	resp, _, err := client.Exchange(req, addr)
	if err != nil {
		return nil, fmt.Errorf("udp exchange with %s: %w", addr, err)
	}

	// TC bit set → response was truncated, retry over TCP
	if resp.Truncated {
		tcpClient := &dns.Client{
			Net:     "tcp",
			Timeout: 5 * time.Second,
		}
		resp, _, err = tcpClient.Exchange(req, addr)
		if err != nil {
			return nil, fmt.Errorf("tcp retry with %s: %w", addr, err)
		}
	}

	return resp, nil
}

// forwardDoH forwards a DNS query to a DoH upstream per RFC 8484.
// Uses POST with Content-Type: application/dns-message (wire format).
//
// Security note: TLS certificate of the DoH server is always validated
// by the shared httpClient (no skip_verify - that option is MikroTik-only).
func (s *Server) forwardDoH(req *dns.Msg, upstream string) (*dns.Msg, error) {
	// Pack DNS message to wire format
	packed, err := req.Pack()
	if err != nil {
		return nil, fmt.Errorf("doh pack: %w", err)
	}

	httpReq, err := http.NewRequest(http.MethodPost, upstream, bytes.NewReader(packed))
	if err != nil {
		return nil, fmt.Errorf("doh build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/dns-message")
	httpReq.Header.Set("Accept", "application/dns-message")

	httpResp, err := s.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("doh http post %s: %w", upstream, err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("doh upstream %s returned HTTP %d", upstream, httpResp.StatusCode)
	}

	// Read response body (RFC 8484: wire-format DNS message, max 65535 bytes)
	buf, err := io.ReadAll(io.LimitReader(httpResp.Body, 65535))
	if err != nil {
		return nil, fmt.Errorf("doh read body: %w", err)
	}

	resp := new(dns.Msg)
	if err := resp.Unpack(buf); err != nil {
		return nil, fmt.Errorf("doh unpack response: %w", err)
	}

	return resp, nil
}

// extractIPs collects A and/or AAAA records from the DNS response
// based on the QueryStrategy setting.
//
//	UseIPv4 → only A records
//	UseIPv6 → only AAAA records
//	UseIP   → both A and AAAA
//	""      → both (same as UseIP)
func extractIPs(resp *dns.Msg, strategy string) []net.IP {
	var ips []net.IP
	wantV4 := strategy != "UseIPv6"
	wantV6 := strategy != "UseIPv4"

	for _, rr := range resp.Answer {
		switch v := rr.(type) {
		case *dns.A:
			if wantV4 {
				ips = append(ips, v.A)
			}
		case *dns.AAAA:
			if wantV6 {
				ips = append(ips, v.AAAA)
			}
		}
	}
	return ips
}

// pushIPs adds resolved IPs to the appropriate MikroTik address-list.
// comment is stored in the address-list entry for identification.
// Called in a goroutine if async_push=true.
func (s *Server) pushIPs(tag, comment string, ips []net.IP) {
	listCfg, ok := s.cfg.Mikrotik.AddressLists[tag]
	if !ok || listCfg == nil {
		// Tag has no address-list configured → nothing to push
		return
	}

	for _, ip := range ips {
		isIPv6 := ip.To4() == nil

		if isIPv6 && !s.cfg.Mikrotik.IPv6.Enabled {
			continue
		}
		if !isIPv6 && !s.cfg.Mikrotik.IPv4.Enabled {
			continue
		}

		logger.Debug("[mikrotik] push  %s → list=%s comment=%q", ip, listCfg.List, comment)
		if err := s.mtClient.EnsureEntry(ip, listCfg, isIPv6, comment); err != nil {
			logger.Error("[mikrotik] %s → list=%s: %v", ip, listCfg.List, err)
		}
	}
}
