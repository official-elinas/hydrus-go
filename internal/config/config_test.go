package config

import (
	"strings"
	"testing"
	"time"

	coredownloader "github.com/official-elinas/hydrus-go/internal/core/downloader"
	coreptrsync "github.com/official-elinas/hydrus-go/internal/core/ptrsync"
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

	if cfg.PTR.Enabled {
		t.Fatal("PTR.Enabled = true, want false")
	}

	if cfg.PTR.Host != coreptrsync.DefaultHost {
		t.Fatalf("PTR.Host = %q, want %q", cfg.PTR.Host, coreptrsync.DefaultHost)
	}

	if cfg.PTR.Port != coreptrsync.DefaultPort {
		t.Fatalf("PTR.Port = %d, want %d", cfg.PTR.Port, coreptrsync.DefaultPort)
	}

	if cfg.PTR.AccessKey != coreptrsync.DefaultSharedAccessKey {
		t.Fatalf("PTR.AccessKey = %q, want %q", cfg.PTR.AccessKey, coreptrsync.DefaultSharedAccessKey)
	}

	if cfg.PTR.ServiceName != coreptrsync.DefaultServiceName {
		t.Fatalf("PTR.ServiceName = %q, want %q", cfg.PTR.ServiceName, coreptrsync.DefaultServiceName)
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
	t.Setenv("HYDRUS_GO_ENABLE_PTR_SYNC", "true")
	t.Setenv("HYDRUS_GO_PTR_HOST", "example.ptr")
	t.Setenv("HYDRUS_GO_PTR_PORT", "60000")
	t.Setenv("HYDRUS_GO_PTR_ACCESS_KEY", strings.Repeat("d", 64))
	t.Setenv("HYDRUS_GO_PTR_SERVICE_NAME", "my public tag repository")

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

	if !cfg.PTR.Enabled {
		t.Fatal("PTR.Enabled = false, want true")
	}

	if cfg.PTR.Host != "example.ptr" {
		t.Fatalf("PTR.Host = %q, want %q", cfg.PTR.Host, "example.ptr")
	}

	if cfg.PTR.Port != 60000 {
		t.Fatalf("PTR.Port = %d, want %d", cfg.PTR.Port, 60000)
	}

	if cfg.PTR.AccessKey != strings.Repeat("d", 64) {
		t.Fatalf("PTR.AccessKey = %q, want %q", cfg.PTR.AccessKey, strings.Repeat("d", 64))
	}

	if cfg.PTR.ServiceName != "my public tag repository" {
		t.Fatalf("PTR.ServiceName = %q, want %q", cfg.PTR.ServiceName, "my public tag repository")
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

func TestLoadFromEnv_RejectsPTRSyncWithoutDBDir(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("HYDRUS_GO_ENABLE_PTR_SYNC", "true")

	_, err := LoadFromEnv()
	if err == nil {
		t.Fatal("LoadFromEnv() error = nil, want error")
	}

	if !strings.Contains(err.Error(), "HYDRUS_GO_DB_DIR is required when PTR sync is enabled") {
		t.Fatalf("LoadFromEnv() error = %v, want PTR DB dir error", err)
	}
}

func TestLoadFromEnv_RejectsInvalidPTRAccessKey(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("HYDRUS_GO_PTR_ACCESS_KEY", "abcdef")

	_, err := LoadFromEnv()
	if err == nil {
		t.Fatal("LoadFromEnv() error = nil, want error")
	}
}

func TestConfigValidate_RejectsInvalidPTRFields(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(cfg *Config)
		wantErr string
	}{
		{
			name: "empty ptr host",
			mutate: func(cfg *Config) {
				cfg.PTR.Host = "   "
			},
			wantErr: "HYDRUS_GO_PTR_HOST must not be empty",
		},
		{
			name: "ptr port out of range",
			mutate: func(cfg *Config) {
				cfg.PTR.Port = 70000
			},
			wantErr: "HYDRUS_GO_PTR_PORT must be between 1 and 65535",
		},
		{
			name: "empty ptr service name",
			mutate: func(cfg *Config) {
				cfg.PTR.ServiceName = "\t"
			},
			wantErr: "HYDRUS_GO_PTR_SERVICE_NAME must not be empty",
		},
		{
			name: "invalid downloader callback url",
			mutate: func(cfg *Config) {
				cfg.Downloader.Enabled = true
				cfg.DBDir = t.TempDir()
				cfg.Downloader.Root = t.TempDir()
				cfg.Downloader.PublicAPIURL = "not-a-url"
			},
			wantErr: "HYDRUS_GO_PUBLIC_API_URL must be a full http or https URL when set",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{
				ListenAddr:               defaultListenAddr,
				Downloader:               coredownloader.DefaultConfig(),
				PTR:                      coreptrsync.DefaultConfig(),
				AccessName:               defaultAccessName,
				ShutdownTimeout:          time.Second,
				AllowNonLocalConnections: false,
				EnableCORS:               false,
			}

			tt.mutate(&cfg)

			err := cfg.Validate()
			if err == nil {
				t.Fatal("Validate() error = nil, want error")
			}

			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() error = %v, want substring %q", err, tt.wantErr)
			}
		})
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
	t.Setenv("HYDRUS_GO_ENABLE_HYDOWNLOADER", "")
	t.Setenv("HYDRUS_GO_HYDOWNLOADER_ROOT", "")
	t.Setenv("HYDRUS_GO_HYDOWNLOADER_HOST", "")
	t.Setenv("HYDRUS_GO_HYDOWNLOADER_PORT", "")
	t.Setenv("HYDRUS_GO_HYDOWNLOADER_ACCESS_KEY", "")
	t.Setenv("HYDRUS_GO_HYDOWNLOADER_AUTOIMPORT", "")
	t.Setenv("HYDRUS_GO_HYDOWNLOADER_DAEMON_BIN", "")
	t.Setenv("HYDRUS_GO_HYDOWNLOADER_TOOLS_BIN", "")
	t.Setenv("HYDRUS_GO_PUBLIC_API_URL", "")
	t.Setenv("HYDRUS_GO_ENABLE_PTR_SYNC", "")
	t.Setenv("HYDRUS_GO_PTR_HOST", "")
	t.Setenv("HYDRUS_GO_PTR_PORT", "")
	t.Setenv("HYDRUS_GO_PTR_ACCESS_KEY", "")
	t.Setenv("HYDRUS_GO_PTR_SERVICE_NAME", "")
}
