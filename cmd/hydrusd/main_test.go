package main

import (
	"io"
	"strings"
	"testing"
)

func TestLoadRuntimeConfig(t *testing.T) {
	t.Run("defaults to local env config", func(t *testing.T) {
		clearDaemonEnv(t)

		cfg, err := loadRuntimeConfig(nil, io.Discard)
		if err != nil {
			t.Fatalf("loadRuntimeConfig() error = %v", err)
		}

		if cfg.ListenAddr != "127.0.0.1:45869" {
			t.Fatalf("cfg.ListenAddr = %q, want 127.0.0.1:45869", cfg.ListenAddr)
		}

		if cfg.AllowNonLocalConnections {
			t.Fatal("cfg.AllowNonLocalConnections = true, want false")
		}
	})

	t.Run("listen flag overrides env and permits non-local bind", func(t *testing.T) {
		clearDaemonEnv(t)

		cfg, err := loadRuntimeConfig([]string{"--listen", "0.0.0.0:5555"}, io.Discard)
		if err != nil {
			t.Fatalf("loadRuntimeConfig() error = %v", err)
		}

		if cfg.ListenAddr != "0.0.0.0:5555" {
			t.Fatalf("cfg.ListenAddr = %q, want 0.0.0.0:5555", cfg.ListenAddr)
		}

		if !cfg.AllowNonLocalConnections {
			t.Fatal("cfg.AllowNonLocalConnections = false, want true")
		}
	})

	t.Run("listen flag overrides invalid env listen address", func(t *testing.T) {
		clearDaemonEnv(t)
		t.Setenv("HYDRUS_GO_LISTEN_ADDR", "0.0.0.0:45869")
		t.Setenv("HYDRUS_GO_ALLOW_NON_LOCAL_CONNECTIONS", "false")

		cfg, err := loadRuntimeConfig([]string{"--listen", "127.0.0.1:5555"}, io.Discard)
		if err != nil {
			t.Fatalf("loadRuntimeConfig() error = %v", err)
		}

		if cfg.ListenAddr != "127.0.0.1:5555" {
			t.Fatalf("cfg.ListenAddr = %q, want 127.0.0.1:5555", cfg.ListenAddr)
		}
	})

	t.Run("rejects invalid listen override", func(t *testing.T) {
		clearDaemonEnv(t)

		_, err := loadRuntimeConfig([]string{"--listen", "not-a-listen-addr"}, io.Discard)
		if err == nil {
			t.Fatal("loadRuntimeConfig() error = nil, want error")
		}

		if !strings.Contains(err.Error(), "parse listen address") {
			t.Fatalf("loadRuntimeConfig() error = %v, want parse listen address error", err)
		}
	})

	t.Run("rejects unexpected positional arguments", func(t *testing.T) {
		clearDaemonEnv(t)

		_, err := loadRuntimeConfig([]string{"extra"}, io.Discard)
		if err == nil {
			t.Fatal("loadRuntimeConfig() error = nil, want error")
		}

		if !strings.Contains(err.Error(), "unexpected arguments") {
			t.Fatalf("loadRuntimeConfig() error = %v, want unexpected arguments error", err)
		}
	})
}

func clearDaemonEnv(t *testing.T) {
	t.Helper()

	t.Setenv("HYDRUS_GO_LISTEN_ADDR", "")
	t.Setenv("HYDRUS_GO_DB_DIR", "")
	t.Setenv("HYDRUS_GO_ACCESS_KEY", "")
	t.Setenv("HYDRUS_GO_ACCESS_NAME", "")
	t.Setenv("HYDRUS_GO_LOG_LEVEL", "")
	t.Setenv("HYDRUS_GO_SHUTDOWN_TIMEOUT", "")
	t.Setenv("HYDRUS_GO_ALLOW_NON_LOCAL_CONNECTIONS", "")
	t.Setenv("HYDRUS_GO_ENABLE_CORS", "")
}
