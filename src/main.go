// dns-geosite-proxy - DNS proxy with geosite-based routing and MikroTik address-list push.
//
// Flow:
//  1. Receive DNS query
//  2. Classify domain via geosite rules → assign tag + upstream
//  3. Forward to upstream DNS, return response to client
//  4. If tag != "direct": push resolved IPs to MikroTik address-list via REST API
//
// Signal handling:
//
//	SIGHUP  → reload dlc.dat in-place (no restart needed after weekly update)
//	SIGTERM → graceful shutdown
//	SIGINT  → graceful shutdown
package main

import (
	"flag"
	"os"
	"os/signal"
	"syscall"

	"dns-geosite-proxy/config"
	dnsserver "dns-geosite-proxy/dns"
	"dns-geosite-proxy/geosite"
	"dns-geosite-proxy/logger"
	"dns-geosite-proxy/mikrotik"
)

// Build-time variables injected via -ldflags by Makefile.
// Defaults are used when building without make (e.g. go run ./...).
var (
	version   = "dev"
	commit    = "none"
	buildDate = "unknown"
)

func main() {
	// Command-line flags
	configPath := flag.String(
		"config",
		"/etc/dns-proxy/config.json",
		"path to JSON configuration file",
	)
	flag.Parse()

	// Load configuration
	cfg, err := config.Load(*configPath)
	if err != nil {
		// logger not yet initialized - use Fatal directly
		logger.Fatal("config: %v", err)
	}

	// Initialize logger first so all subsequent messages respect log_level
	logger.Init(cfg.LogLevel)
	logger.Info("dns-geosite-proxy %s (commit: %s, built: %s)", version, commit, buildDate)

	// Load geosite database (dlc.dat)
	db, err := geosite.Load(cfg.GeositePath)
	if err != nil {
		logger.Fatal("geosite: %v", err)
	}
	logger.Info("geosite: loaded %d categories from %s", db.CategoryCount(), cfg.GeositePath)

	// Initialize MikroTik REST API client
	mt := mikrotik.NewClient(&cfg.Mikrotik)

	// Build and start DNS server
	srv := dnsserver.NewServer(cfg, db, mt)

	// Start in background; any fatal error will be logged and process exits
	go func() {
		if err := srv.Start(); err != nil {
			logger.Fatal("dns server: %v", err)
		}
	}()

	logger.Info("listening on %s (async_push=%v)", cfg.Listen, cfg.AsyncPush)

	// Signal handling
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)

	for sig := range sigCh {
		switch sig {
		case syscall.SIGHUP:
			// Reload geosite without restart.
			// Triggered by update-dlc.sh after successful dlc.dat download.
			logger.Info("SIGHUP: reloading geosite...")
			if err := srv.ReloadGeosite(cfg.GeositePath); err != nil {
				logger.Error("geosite reload failed: %v", err)
			} else {
				logger.Info("geosite reloaded: %d categories", db.CategoryCount())
			}

		default:
			logger.Info("signal %v: shutting down gracefully...", sig)
			srv.Stop()
			os.Exit(0)
		}
	}
}
