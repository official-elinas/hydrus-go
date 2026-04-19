// Package config loads daemon configuration from environment variables.
package config

import (
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	defaultListenAddr       = "127.0.0.1:45869"
	defaultAccessName       = "hydrus-go"
	defaultLogLevel         = "info"
	defaultShutdownTimeout  = 10 * time.Second
	defaultBootstrapTimeout = 2 * time.Minute
)

// Config holds the bootstrap hydrus-go daemon configuration.
type Config struct {
	ListenAddr                 string
	DBDir                      string
	EnableFreshClientBootstrap bool
	BootstrapTimeout           time.Duration
	AccessKey                  string
	AccessName                 string
	LogLevel                   string
	ShutdownTimeout            time.Duration
	AllowNonLocalConnections   bool
	EnableCORS                 bool
}

// LoadFromEnv loads and validates the bootstrap daemon configuration.
func LoadFromEnv() (Config, error) {
	cfg, err := LoadFromEnvUnvalidated()
	if err != nil {
		return Config{}, err
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// LoadFromEnvUnvalidated loads daemon configuration from environment variables
// without final validation, so higher-precedence runtime overrides can be
// applied before checks run.
func LoadFromEnvUnvalidated() (Config, error) {
	cfg := Config{
		ListenAddr:                 getEnv("HYDRUS_GO_LISTEN_ADDR", defaultListenAddr),
		DBDir:                      strings.TrimSpace(os.Getenv("HYDRUS_GO_DB_DIR")),
		EnableFreshClientBootstrap: false,
		BootstrapTimeout:           defaultBootstrapTimeout,
		AccessKey:                  strings.TrimSpace(os.Getenv("HYDRUS_GO_ACCESS_KEY")),
		AccessName:                 getEnv("HYDRUS_GO_ACCESS_NAME", defaultAccessName),
		LogLevel:                   getEnv("HYDRUS_GO_LOG_LEVEL", defaultLogLevel),
		ShutdownTimeout:            defaultShutdownTimeout,
		AllowNonLocalConnections:   false,
		EnableCORS:                 false,
	}

	if cfg.DBDir != "" {
		cfg.DBDir = filepath.Clean(cfg.DBDir)
	}

	enableFreshClientBootstrap, err := getFreshBootstrapEnabled()
	if err != nil {
		return Config{}, err
	}
	cfg.EnableFreshClientBootstrap = enableFreshClientBootstrap

	allowNonLocal, err := getEnvBool(
		"HYDRUS_GO_ALLOW_NON_LOCAL_CONNECTIONS",
		false,
	)
	if err != nil {
		return Config{}, err
	}
	cfg.AllowNonLocalConnections = allowNonLocal

	enableCORS, err := getEnvBool("HYDRUS_GO_ENABLE_CORS", false)
	if err != nil {
		return Config{}, err
	}
	cfg.EnableCORS = enableCORS

	shutdownTimeout, err := getEnvDuration(
		"HYDRUS_GO_SHUTDOWN_TIMEOUT",
		defaultShutdownTimeout,
	)
	if err != nil {
		return Config{}, err
	}
	cfg.ShutdownTimeout = shutdownTimeout

	bootstrapTimeout, err := getEnvDuration(
		"HYDRUS_GO_BOOTSTRAP_TIMEOUT",
		defaultBootstrapTimeout,
	)
	if err != nil {
		return Config{}, err
	}
	cfg.BootstrapTimeout = bootstrapTimeout

	normalizedAccessKey, err := normalizeOptionalAccessKey(cfg.AccessKey)
	if err != nil {
		return Config{}, err
	}
	cfg.AccessKey = normalizedAccessKey

	return cfg, nil
}

// Validate applies the final daemon configuration checks.
func (c Config) Validate() error {
	return c.validate()
}

func (c Config) validate() error {
	if strings.TrimSpace(c.ListenAddr) == "" {
		return fmt.Errorf("listen address must not be empty")
	}

	if c.ShutdownTimeout <= 0 {
		return fmt.Errorf("shutdown timeout must be greater than zero")
	}

	if c.EnableFreshClientBootstrap {
		if strings.TrimSpace(c.DBDir) == "" {
			return fmt.Errorf("HYDRUS_GO_DB_DIR is required when fresh-client bootstrap is enabled")
		}

		if c.BootstrapTimeout <= 0 {
			return fmt.Errorf("bootstrap timeout must be greater than zero")
		}
	}

	if c.DBDir != "" {
		info, err := os.Stat(c.DBDir)
		if err != nil {
			if os.IsNotExist(err) && c.EnableFreshClientBootstrap {
				info = nil
			} else {
				return fmt.Errorf("stat HYDRUS_GO_DB_DIR: %w", err)
			}
		}

		if info == nil {
			// fresh bootstrap is allowed to create the directory on first start
		} else if !info.IsDir() {
			return fmt.Errorf("HYDRUS_GO_DB_DIR must be a directory")
		}
	}

	host, _, err := net.SplitHostPort(c.ListenAddr)
	if err != nil {
		return fmt.Errorf("parse listen address %q: %w", c.ListenAddr, err)
	}

	if c.AllowNonLocalConnections {
		return nil
	}

	if isLocalOnlyHost(host) {
		return nil
	}

	return fmt.Errorf(
		"listen address %q is not local-only; set HYDRUS_GO_ALLOW_NON_LOCAL_CONNECTIONS=true to permit it",
		c.ListenAddr,
	)
}

func getEnv(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}

	return value
}

func getFreshBootstrapEnabled() (bool, error) {
	if raw := strings.TrimSpace(os.Getenv("HYDRUS_GO_ENABLE_FRESH_CLIENT_BOOTSTRAP")); raw != "" {
		return parseEnvBool("HYDRUS_GO_ENABLE_FRESH_CLIENT_BOOTSTRAP", raw)
	}

	if raw := strings.TrimSpace(os.Getenv("HYDRUS_GO_ENABLE_PYTHON_FRESH_CLIENT_BOOTSTRAP")); raw != "" {
		return parseEnvBool("HYDRUS_GO_ENABLE_PYTHON_FRESH_CLIENT_BOOTSTRAP", raw)
	}

	return false, nil
}

func getEnvBool(key string, fallback bool) (bool, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}

	return parseEnvBool(key, raw)
}

func parseEnvBool(key string, raw string) (bool, error) {
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("parse %s: %w", key, err)
	}

	return value, nil
}

func getEnvDuration(key string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}

	value, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", key, err)
	}

	return value, nil
}

func isLocalOnlyHost(host string) bool {
	host = strings.TrimSpace(strings.ToLower(host))

	switch host {
	case "127.0.0.1", "localhost", "::1":
		return true
	default:
		return false
	}
}

func normalizeOptionalAccessKey(accessKey string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(accessKey))
	if normalized == "" {
		return "", nil
	}

	decoded, err := hex.DecodeString(normalized)
	if err != nil {
		return "", fmt.Errorf("decode HYDRUS_GO_ACCESS_KEY: %w", err)
	}

	if len(decoded) != 32 {
		return "", fmt.Errorf(
			"HYDRUS_GO_ACCESS_KEY must be 32 bytes (64 hex characters), got %d bytes",
			len(decoded),
		)
	}

	return normalized, nil
}
