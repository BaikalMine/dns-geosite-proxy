// Package logger provides a simple leveled logger built on top of the standard log package.
//
// Levels (ascending severity): DEBUG < INFO < WARN < ERROR
// Init() must be called once from main() before any other package uses the logger.
// All functions are safe for concurrent use (standard log.Logger is mutex-protected).
package logger

import (
	"log"
	"strings"
)

// Level represents log verbosity.
type Level int

const (
	DEBUG Level = iota
	INFO
	WARN
	ERROR
)

// current is the active log level; messages below it are silently dropped.
var current = INFO

// Init sets the global log level from a string ("debug", "info", "warn", "error").
// Unknown strings default to INFO. Call once from main() after loading config.
func Init(levelStr string) {
	switch strings.ToLower(levelStr) {
	case "debug":
		current = DEBUG
	case "warn", "warning":
		current = WARN
	case "error":
		current = ERROR
	default:
		current = INFO
	}
	// Standard log flags: date + time prefix (no file/line - too noisy for a proxy)
	log.SetFlags(log.LstdFlags)
}

// Debug logs at DEBUG level (config, classification decisions, TTL checks).
func Debug(format string, args ...interface{}) {
	if current <= DEBUG {
		log.Printf("[DEBUG] "+format, args...)
	}
}

// Info logs at INFO level (query, resolved, entry added/updated).
func Info(format string, args ...interface{}) {
	if current <= INFO {
		log.Printf("[INFO]  "+format, args...)
	}
}

// Warn logs at WARN level (upstream errors, partial failures).
func Warn(format string, args ...interface{}) {
	if current <= WARN {
		log.Printf("[WARN]  "+format, args...)
	}
}

// Error logs at ERROR level (API errors, unrecoverable per-request failures).
func Error(format string, args ...interface{}) {
	if current <= ERROR {
		log.Printf("[ERROR] "+format, args...)
	}
}

// Fatal logs at ERROR level and calls os.Exit(1).
// Use only in main() for startup failures.
func Fatal(format string, args ...interface{}) {
	log.Fatalf("[FATAL] "+format, args...)
}
