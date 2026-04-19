package main

import (
	"io"
	"strings"
	"testing"
	"time"
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

	t.Run("bootstrap flags override env", func(t *testing.T) {
		clearDaemonEnv(t)
		t.Setenv("HYDRUS_GO_DB_DIR", t.TempDir()+"/fresh-bundle")
		t.Setenv("HYDRUS_GO_ENABLE_FRESH_CLIENT_BOOTSTRAP", "false")
		t.Setenv("HYDRUS_GO_BOOTSTRAP_TIMEOUT", "30s")

		cfg, err := loadRuntimeConfig(
			[]string{
				"--bootstrap-fresh-client",
				"--bootstrap-timeout", "90s",
			},
			io.Discard,
		)
		if err != nil {
			t.Fatalf("loadRuntimeConfig() error = %v", err)
		}

		if !cfg.EnableFreshClientBootstrap {
			t.Fatal("cfg.EnableFreshClientBootstrap = false, want true")
		}

		if cfg.BootstrapTimeout != 90*time.Second {
			t.Fatalf("cfg.BootstrapTimeout = %v, want %v", cfg.BootstrapTimeout, 90*time.Second)
		}
	})

	t.Run("bootstrap flag can disable env enabled bootstrap", func(t *testing.T) {
		clearDaemonEnv(t)
		t.Setenv("HYDRUS_GO_ENABLE_FRESH_CLIENT_BOOTSTRAP", "true")

		cfg, err := loadRuntimeConfig([]string{"--bootstrap-fresh-client=false"}, io.Discard)
		if err != nil {
			t.Fatalf("loadRuntimeConfig() error = %v", err)
		}

		if cfg.EnableFreshClientBootstrap {
			t.Fatal("cfg.EnableFreshClientBootstrap = true, want false")
		}
	})

	t.Run("rejects invalid bootstrap timeout when bootstrap enabled", func(t *testing.T) {
		clearDaemonEnv(t)
		t.Setenv("HYDRUS_GO_DB_DIR", t.TempDir()+"/fresh-bundle")

		_, err := loadRuntimeConfig(
			[]string{
				"--bootstrap-fresh-client",
				"--bootstrap-timeout", "0s",
			},
			io.Discard,
		)
		if err == nil {
			t.Fatal("loadRuntimeConfig() error = nil, want error")
		}

		if !strings.Contains(err.Error(), "bootstrap timeout") {
			t.Fatalf("loadRuntimeConfig() error = %v, want bootstrap timeout error", err)
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

	t.Run("rejects removed python bootstrap flags", func(t *testing.T) {
		clearDaemonEnv(t)

		_, err := loadRuntimeConfig([]string{"--bootstrap-python", "/usr/bin/python3"}, io.Discard)
		if err == nil {
			t.Fatal("loadRuntimeConfig() error = nil, want error")
		}

		if !strings.Contains(err.Error(), "flag provided but not defined") {
			t.Fatalf("loadRuntimeConfig() error = %v, want unknown flag error", err)
		}
	})

	t.Run("accepts legacy bootstrap enable env alias", func(t *testing.T) {
		clearDaemonEnv(t)
		t.Setenv("HYDRUS_GO_DB_DIR", t.TempDir()+"/fresh-bundle")
		t.Setenv("HYDRUS_GO_ENABLE_PYTHON_FRESH_CLIENT_BOOTSTRAP", "true")

		cfg, err := loadRuntimeConfig(nil, io.Discard)
		if err != nil {
			t.Fatalf("loadRuntimeConfig() error = %v", err)
		}

		if !cfg.EnableFreshClientBootstrap {
			t.Fatal("cfg.EnableFreshClientBootstrap = false, want true")
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
	t.Setenv("HYDRUS_GO_ENABLE_FRESH_CLIENT_BOOTSTRAP", "")
	t.Setenv("HYDRUS_GO_ENABLE_PYTHON_FRESH_CLIENT_BOOTSTRAP", "")
	t.Setenv("HYDRUS_GO_BOOTSTRAP_PYTHON", "")
	t.Setenv("HYDRUS_GO_BOOTSTRAP_HYDRUS_ROOT", "")
	t.Setenv("HYDRUS_GO_BOOTSTRAP_TIMEOUT", "")
	t.Setenv("HYDRUS_GO_ACCESS_KEY", "")
	t.Setenv("HYDRUS_GO_ACCESS_NAME", "")
	t.Setenv("HYDRUS_GO_LOG_LEVEL", "")
	t.Setenv("HYDRUS_GO_SHUTDOWN_TIMEOUT", "")
	t.Setenv("HYDRUS_GO_ALLOW_NON_LOCAL_CONNECTIONS", "")
	t.Setenv("HYDRUS_GO_ENABLE_CORS", "")
}
