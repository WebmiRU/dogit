// Package logger provides a small slog-based logger shared by all processes.
package logger

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

func New(level, environment string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{
		Level:     lvl,
		AddSource: lvl == slog.LevelDebug,
	}

	// Logs always go to stderr. stdout is a protocol stream for the git
	// entry points: a single stray log line there corrupts the packfile
	// negotiation and the client fails with a confusing protocol error.
	var h slog.Handler
	if environment == "development" {
		h = slog.NewTextHandler(os.Stderr, opts)
	} else {
		h = slog.NewJSONHandler(os.Stderr, opts)
	}

	l := slog.New(h)
	slog.SetDefault(l)
	return l
}

// SetLevel rebuilds the logger with a different level and installs it as the
// default.
//
// The git entry points use it: their stderr is the client's "remote:" stream, so
// anything below a warning would be printed to whoever pushed, unless the operator
// asked for it explicitly.
func SetLevel(existing *slog.Logger, level string) *slog.Logger {
	environment := "development"
	if existing != nil {
		// The handler style follows the environment the original logger was built
		// for, so switching level does not change text into JSON.
		if os.Getenv("DOGIT_ENV") != "" {
			environment = os.Getenv("DOGIT_ENV")
		}
	}
	return New(level, environment)
}

// Discard returns a logger that writes nowhere. Useful in tests.
func Discard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}
