package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadFromEnv_Defaults(t *testing.T) {
	clearConfigEnv(t)

	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("LoadFromEnv() error = %v", err)
	}

	if cfg.ListenAddr != defaultListenAddr {
		t.Fatalf("ListenAddr = %q, want %q", cfg.ListenAddr, defaultListenAddr)
	}

	if cfg.DBDir != "" {
		t.Fatalf("DBDir = %q, want empty", cfg.DBDir)
	}

	if cfg.EnableFreshClientBootstrap {
		t.Fatal("EnableFreshClientBootstrap = true, want false")
	}

	if cfg.BootstrapTimeout != defaultBootstrapTimeout {
		t.Fatalf(
			"BootstrapTimeout = %v, want %v",
			cfg.BootstrapTimeout,
			defaultBootstrapTimeout,
		)
	}

	if cfg.AccessName != defaultAccessName {
		t.Fatalf("AccessName = %q, want %q", cfg.AccessName, defaultAccessName)
	}

	if cfg.LogLevel != defaultLogLevel {
		t.Fatalf("LogLevel = %q, want %q", cfg.LogLevel, defaultLogLevel)
	}

	if cfg.ShutdownTimeout != defaultShutdownTimeout {
		t.Fatalf(
			"ShutdownTimeout = %v, want %v",
			cfg.ShutdownTimeout,
			defaultShutdownTimeout,
		)
	}

	if cfg.AllowNonLocalConnections {
		t.Fatal("AllowNonLocalConnections = true, want false")
	}

	if cfg.EnableCORS {
		t.Fatal("EnableCORS = true, want false")
	}
}

func TestLoadFromEnv_RejectsNonLocalListenAddressByDefault(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("HYDRUS_GO_LISTEN_ADDR", "0.0.0.0:45869")
	t.Setenv("HYDRUS_GO_ALLOW_NON_LOCAL_CONNECTIONS", "false")

	_, err := LoadFromEnv()
	if err == nil {
		t.Fatal("LoadFromEnv() error = nil, want error")
	}
}

func TestLoadFromEnv_AllowsConfiguredOverrides(t *testing.T) {
	clearConfigEnv(t)
	dbDir := t.TempDir()

	t.Setenv("HYDRUS_GO_LISTEN_ADDR", "0.0.0.0:9999")
	t.Setenv("HYDRUS_GO_DB_DIR", dbDir)
	t.Setenv("HYDRUS_GO_ENABLE_FRESH_CLIENT_BOOTSTRAP", "true")
	t.Setenv("HYDRUS_GO_BOOTSTRAP_TIMEOUT", "45s")
	t.Setenv("HYDRUS_GO_ACCESS_KEY", strings.Repeat("c", 64))
	t.Setenv("HYDRUS_GO_ACCESS_NAME", "integration-test")
	t.Setenv("HYDRUS_GO_LOG_LEVEL", "debug")
	t.Setenv("HYDRUS_GO_SHUTDOWN_TIMEOUT", "30s")
	t.Setenv("HYDRUS_GO_ALLOW_NON_LOCAL_CONNECTIONS", "true")
	t.Setenv("HYDRUS_GO_ENABLE_CORS", "true")

	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("LoadFromEnv() error = %v", err)
	}

	if cfg.ListenAddr != "0.0.0.0:9999" {
		t.Fatalf("ListenAddr = %q, want %q", cfg.ListenAddr, "0.0.0.0:9999")
	}

	if cfg.DBDir != dbDir {
		t.Fatalf("DBDir = %q, want %q", cfg.DBDir, dbDir)
	}

	if !cfg.EnableFreshClientBootstrap {
		t.Fatal("EnableFreshClientBootstrap = false, want true")
	}

	if cfg.BootstrapTimeout != 45*time.Second {
		t.Fatalf("BootstrapTimeout = %v, want %v", cfg.BootstrapTimeout, 45*time.Second)
	}

	if cfg.AccessKey != strings.Repeat("c", 64) {
		t.Fatalf(
			"AccessKey = %q, want %q",
			cfg.AccessKey,
			strings.Repeat("c", 64),
		)
	}

	if cfg.AccessName != "integration-test" {
		t.Fatalf("AccessName = %q, want %q", cfg.AccessName, "integration-test")
	}

	if cfg.LogLevel != "debug" {
		t.Fatalf("LogLevel = %q, want %q", cfg.LogLevel, "debug")
	}

	if cfg.ShutdownTimeout != 30*time.Second {
		t.Fatalf(
			"ShutdownTimeout = %v, want %v",
			cfg.ShutdownTimeout,
			30*time.Second,
		)
	}

	if !cfg.AllowNonLocalConnections {
		t.Fatal("AllowNonLocalConnections = false, want true")
	}

	if !cfg.EnableCORS {
		t.Fatal("EnableCORS = false, want true")
	}
}

func TestLoadFromEnv_RejectsInvalidAccessKey(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("HYDRUS_GO_ACCESS_KEY", "abcdef")

	_, err := LoadFromEnv()
	if err == nil {
		t.Fatal("LoadFromEnv() error = nil, want error")
	}
}

func TestLoadFromEnv_RejectsMissingDBDir(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("HYDRUS_GO_DB_DIR", t.TempDir()+"/missing")

	_, err := LoadFromEnv()
	if err == nil {
		t.Fatal("LoadFromEnv() error = nil, want error")
	}
}

func TestLoadFromEnv_AllowsMissingDBDirWhenBootstrapEnabled(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("HYDRUS_GO_DB_DIR", t.TempDir()+"/missing")
	t.Setenv("HYDRUS_GO_ENABLE_FRESH_CLIENT_BOOTSTRAP", "true")

	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("LoadFromEnv() error = %v", err)
	}

	if !cfg.EnableFreshClientBootstrap {
		t.Fatal("EnableFreshClientBootstrap = false, want true")
	}
}

func TestLoadFromEnv_AcceptsLegacyBootstrapEnableAlias(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("HYDRUS_GO_DB_DIR", t.TempDir()+"/missing")
	t.Setenv("HYDRUS_GO_ENABLE_PYTHON_FRESH_CLIENT_BOOTSTRAP", "true")

	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("LoadFromEnv() error = %v", err)
	}

	if !cfg.EnableFreshClientBootstrap {
		t.Fatal("EnableFreshClientBootstrap = false, want true")
	}
}

func TestLoadFromEnv_IgnoresRemovedPythonBootstrapInterpreterAndRootEnvVars(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("HYDRUS_GO_BOOTSTRAP_PYTHON", "/usr/bin/python3")
	t.Setenv("HYDRUS_GO_BOOTSTRAP_HYDRUS_ROOT", "/fake/hydrus/root")

	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("LoadFromEnv() error = %v", err)
	}

	if cfg.EnableFreshClientBootstrap {
		t.Fatal("EnableFreshClientBootstrap = true, want false when only removed interpreter/root env vars are set")
	}
}

func TestLoadFromEnv_RejectsBootstrapWithoutDBDir(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("HYDRUS_GO_ENABLE_FRESH_CLIENT_BOOTSTRAP", "true")

	_, err := LoadFromEnv()
	if err == nil {
		t.Fatal("LoadFromEnv() error = nil, want error")
	}

	if !strings.Contains(err.Error(), "HYDRUS_GO_DB_DIR is required") {
		t.Fatalf("LoadFromEnv() error = %v, want missing DB dir error", err)
	}
}

func clearConfigEnv(t *testing.T) {
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
