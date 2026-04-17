// Package logging configures the daemon's structured logger.
package logging

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// New builds the bootstrap daemon logger for the requested level.
func New(level string) (*slog.Logger, error) {
	parsedLevel, err := parseLevel(level)
	if err != nil {
		return nil, err
	}

	handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: parsedLevel})

	return slog.New(handler), nil
}

func parseLevel(level string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("unsupported log level %q", level)
	}
}
