package hydownloader

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	coredownloader "github.com/official-elinas/hydrus-go/internal/core/downloader"
)

func TestMain(m *testing.M) {
	killOrphanDaemonFn = func(_ *Manager, _ context.Context) {}
	m.Run()
}

func TestManagerInitializesQueuesAndShutsDown(t *testing.T) {
	root := filepath.Join(t.TempDir(), "hydownloader-root")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer listener.Close()

	host, portString, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatalf("SplitHostPort() error = %v", err)
	}
	port, err := strconv.Atoi(portString)
	if err != nil {
		t.Fatalf("Atoi(port) error = %v", err)
	}

	var (
		mu                     sync.Mutex
		queuedURLBody          []map[string]any
		queuedSubscriptionBody []map[string]any
		resumeAutoimportCalled bool
		shutdownCalled         bool
		serverCloser           func()
	)

	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("HyDownloader-Access-Key") != "hydl-access-key" {
			t.Fatalf("HyDownloader-Access-Key = %q, want hydl-access-key", r.Header.Get("HyDownloader-Access-Key"))
		}

		switch r.URL.Path {
		case "/api_version":
			writeManagerJSON(t, w, map[string]any{"version": "test"})
		case "/get_status_info":
			writeManagerJSON(t, w, map[string]any{
				"subscriptions_due":                    2,
				"urls_queued":                          1,
				"subscriptions_paused":                 false,
				"urls_paused":                          false,
				"autoimport_jobs_paused":               true,
				"subscription_worker_status":           "checking subscription",
				"url_worker_status":                    "downloading URL",
				"autoimport_worker_status":             "paused",
				"subscription_worker_last_update_time": 123.0,
				"url_worker_last_update_time":          456.0,
				"autoimport_worker_last_update_time":   789.0,
			})
		case "/downloaders":
			writeManagerJSON(t, w, map[string]any{"gelbooru": "https://gelbooru.com/index.php?page=post&s=list&tags={keywords}"})
		case "/add_or_update_urls":
			decodeManagerJSON(t, r.Body, &queuedURLBody)
			writeManagerJSON(t, w, map[string]any{"status": true})
		case "/add_or_update_subscriptions":
			decodeManagerJSON(t, r.Body, &queuedSubscriptionBody)
			writeManagerJSON(t, w, map[string]any{"status": true})
		case "/shutdown":
			mu.Lock()
			shutdownCalled = true
			closer := serverCloser
			mu.Unlock()
			writeManagerJSON(t, w, map[string]any{"status": true})
			if closer != nil {
				go closer()
			}
		case "/resume_autoimports":
			mu.Lock()
			resumeAutoimportCalled = true
			mu.Unlock()
			writeManagerJSON(t, w, map[string]any{"status": true})
		default:
			t.Fatalf("unexpected hydownloader API path %q", r.URL.Path)
		}
	})}
	go server.Serve(listener)
	defer server.Close()

	originalExecCommandContext := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		switch name {
		case "fake-tools":
			if err := os.MkdirAll(root, 0o755); err != nil {
				t.Fatalf("MkdirAll(root) error = %v", err)
			}
			if err := os.WriteFile(filepath.Join(root, hydownloaderDatabaseFileName), []byte("db"), 0o644); err != nil {
				t.Fatalf("WriteFile(hydownloader.db) error = %v", err)
			}
			if err := os.WriteFile(filepath.Join(root, hydownloaderConfigFileName), []byte("{}\n"), 0o644); err != nil {
				t.Fatalf("WriteFile(config) error = %v", err)
			}
			if err := os.WriteFile(filepath.Join(root, hydownloaderImportJobsName), []byte("defAPIURL = \"old\"\ndefAPIKey = \"old\"\n"), 0o644); err != nil {
				t.Fatalf("WriteFile(import jobs) error = %v", err)
			}
			return exec.CommandContext(ctx, "sh", "-c", "true")
		case "fake-daemon":
			return exec.CommandContext(ctx, "sleep", "300")
		default:
			t.Fatalf("unexpected command %q args=%v", name, args)
			return exec.CommandContext(ctx, "sh", "-c", "false")
		}
	}
	defer func() { execCommandContext = originalExecCommandContext }()

	manager, err := New(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), coredownloader.Config{
		Enabled:    true,
		Root:       root,
		Host:       host,
		Port:       port,
		AccessKey:  "hydl-access-key",
		Autoimport: true,
		DaemonBin:  "fake-daemon",
		ToolsBin:   "fake-tools",
	}, "http://127.0.0.1:45869", "hydrus-go-access-key")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	status, err := manager.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if !status.Running || status.URLsQueued != 1 || status.SubscriptionsDue != 2 {
		t.Fatalf("status = %#v, want running with queued URL and due subscription counts", status)
	}
	if !status.AutoimportPaused || status.AutoimportWorkerStatus != "paused" {
		t.Fatalf("status = %#v, want paused autoimport worker details", status)
	}

	downloaders, err := manager.Downloaders(context.Background())
	if err != nil {
		t.Fatalf("Downloaders() error = %v", err)
	}
	if downloaders["gelbooru"] == "" {
		t.Fatalf("downloaders = %v, want gelbooru pattern", downloaders)
	}

	maxFiles := int64(25)
	if err := manager.QueueURL(context.Background(), coredownloader.URLRequest{URL: "https://example.com/post/1", MaxFiles: &maxFiles}); err != nil {
		t.Fatalf("QueueURL() error = %v", err)
	}
	if err := manager.QueueSubscription(context.Background(), coredownloader.SubscriptionRequest{Downloader: "gelbooru", Keywords: "blue_archive", CheckInterval: 3600}); err != nil {
		t.Fatalf("QueueSubscription() error = %v", err)
	}

	if got := queuedURLBody[0]["url"]; got != "https://example.com/post/1" {
		t.Fatalf("queuedURLBody[0][url] = %v, want https://example.com/post/1", got)
	}
	if got := int64(queuedSubscriptionBody[0]["check_interval"].(float64)); got != 3600 {
		t.Fatalf("queuedSubscriptionBody[0][check_interval] = %d, want 3600", got)
	}

	configBytes, err := os.ReadFile(filepath.Join(root, hydownloaderConfigFileName))
	if err != nil {
		t.Fatalf("ReadFile(config) error = %v", err)
	}
	var configPayload map[string]any
	if err := json.Unmarshal(configBytes, &configPayload); err != nil {
		t.Fatalf("Unmarshal(config) error = %v", err)
	}
	if configPayload["daemon.ssl"] != false {
		t.Fatalf("daemon.ssl = %v, want false", configPayload["daemon.ssl"])
	}
	if configPayload["daemon.access-key"] != "hydl-access-key" {
		t.Fatalf("daemon.access-key = %v, want hydl-access-key", configPayload["daemon.access-key"])
	}

	importJobsBytes, err := os.ReadFile(filepath.Join(root, hydownloaderImportJobsName))
	if err != nil {
		t.Fatalf("ReadFile(import jobs) error = %v", err)
	}
	importJobsText := string(importJobsBytes)
	if !strings.Contains(importJobsText, `defAPIURL = "http://127.0.0.1:45869"`) {
		t.Fatalf("import jobs text = %q, want hydrus API URL replacement", importJobsText)
	}
	if !strings.Contains(importJobsText, `defAPIKey = "hydrus-go-access-key"`) {
		t.Fatalf("import jobs text = %q, want hydrus API key replacement", importJobsText)
	}

	if err := manager.ActivateAutoimport(context.Background()); err != nil {
		t.Fatalf("ActivateAutoimport() error = %v", err)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := manager.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !resumeAutoimportCalled {
		t.Fatal("resumeAutoimportCalled = false, want hydownloader autoimport resume API request")
	}
	if !shutdownCalled {
		t.Fatal("shutdownCalled = false, want hydownloader shutdown API request")
	}
}

func TestManagerStartupError_DaemonCrashesDuringStartup(t *testing.T) {
	root := filepath.Join(t.TempDir(), "hydownloader-root")

	originalExecCommandContext := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		switch name {
		case "fake-tools":
			if err := os.MkdirAll(root, 0o755); err != nil {
				t.Fatalf("MkdirAll(root) error = %v", err)
			}
			if err := os.WriteFile(filepath.Join(root, hydownloaderDatabaseFileName), []byte("db"), 0o644); err != nil {
				t.Fatalf("WriteFile(hydownloader.db) error = %v", err)
			}
			if err := os.WriteFile(filepath.Join(root, hydownloaderConfigFileName), []byte("{}\n"), 0o644); err != nil {
				t.Fatalf("WriteFile(config) error = %v", err)
			}
			if err := os.WriteFile(filepath.Join(root, hydownloaderImportJobsName), []byte("defAPIURL = \"old\"\ndefAPIKey = \"old\"\n"), 0o644); err != nil {
				t.Fatalf("WriteFile(import jobs) error = %v", err)
			}
			return exec.CommandContext(ctx, "sh", "-c", "true")
		case "fake-daemon-crash":
			return exec.CommandContext(ctx, "sh", "-c", "exit 1")
		default:
			t.Fatalf("unexpected command %q", name)
			return exec.CommandContext(ctx, "sh", "-c", "false")
		}
	}
	defer func() { execCommandContext = originalExecCommandContext }()

	_, err := New(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), coredownloader.Config{
		Enabled:   true,
		Root:      root,
		Host:      "127.0.0.1",
		Port:      1,
		AccessKey: "hydl-access-key",
		DaemonBin: "fake-daemon-crash",
		ToolsBin:  "fake-tools",
	}, "http://127.0.0.1:45869", "hydrus-go-access-key")
	if err == nil {
		t.Fatal("New() error = nil, want startup error when daemon crashes immediately")
	}
}

func TestManagerStartupError_StartupTimeout(t *testing.T) {
	root := filepath.Join(t.TempDir(), "hydownloader-root")

	originalExecCommandContext := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		switch name {
		case "fake-tools":
			if err := os.MkdirAll(root, 0o755); err != nil {
				t.Fatalf("MkdirAll(root) error = %v", err)
			}
			if err := os.WriteFile(filepath.Join(root, hydownloaderDatabaseFileName), []byte("db"), 0o644); err != nil {
				t.Fatalf("WriteFile(hydownloader.db) error = %v", err)
			}
			if err := os.WriteFile(filepath.Join(root, hydownloaderConfigFileName), []byte("{}\n"), 0o644); err != nil {
				t.Fatalf("WriteFile(config) error = %v", err)
			}
			if err := os.WriteFile(filepath.Join(root, hydownloaderImportJobsName), []byte("defAPIURL = \"old\"\ndefAPIKey = \"old\"\n"), 0o644); err != nil {
				t.Fatalf("WriteFile(import jobs) error = %v", err)
			}
			return exec.CommandContext(ctx, "sh", "-c", "true")
		case "fake-daemon-hang":
			return exec.CommandContext(ctx, "sleep", "300")
		default:
			t.Fatalf("unexpected command %q", name)
			return exec.CommandContext(ctx, "sh", "-c", "false")
		}
	}
	defer func() { execCommandContext = originalExecCommandContext }()

	startCtx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	_, err := New(startCtx, slog.New(slog.NewTextHandler(io.Discard, nil)), coredownloader.Config{
		Enabled:   true,
		Root:      root,
		Host:      "127.0.0.1",
		Port:      1,
		AccessKey: "hydl-access-key",
		DaemonBin: "fake-daemon-hang",
		ToolsBin:  "fake-tools",
	}, "http://127.0.0.1:45869", "hydrus-go-access-key")
	if err == nil {
		t.Fatal("New() error = nil, want timeout error when daemon never becomes reachable")
	}
}

func TestManagerStatus_APIUnreachable(t *testing.T) {
	root := filepath.Join(t.TempDir(), "hydownloader-root")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}

	host, portString, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatalf("SplitHostPort() error = %v", err)
	}
	port, err := strconv.Atoi(portString)
	if err != nil {
		t.Fatalf("Atoi(port) error = %v", err)
	}

	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeManagerJSON(t, w, map[string]any{"version": "test"})
	})}
	go server.Serve(listener)

	originalExecCommandContext := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		switch name {
		case "fake-tools":
			if err := os.MkdirAll(root, 0o755); err != nil {
				t.Fatalf("MkdirAll(root) error = %v", err)
			}
			if err := os.WriteFile(filepath.Join(root, hydownloaderDatabaseFileName), []byte("db"), 0o644); err != nil {
				t.Fatalf("WriteFile(hydownloader.db) error = %v", err)
			}
			if err := os.WriteFile(filepath.Join(root, hydownloaderConfigFileName), []byte("{}\n"), 0o644); err != nil {
				t.Fatalf("WriteFile(config) error = %v", err)
			}
			if err := os.WriteFile(filepath.Join(root, hydownloaderImportJobsName), []byte("defAPIURL = \"old\"\ndefAPIKey = \"old\"\n"), 0o644); err != nil {
				t.Fatalf("WriteFile(import jobs) error = %v", err)
			}
			return exec.CommandContext(ctx, "sh", "-c", "true")
		case "fake-daemon":
			return exec.CommandContext(ctx, "sleep", "300")
		default:
			t.Fatalf("unexpected command %q", name)
			return exec.CommandContext(ctx, "sh", "-c", "false")
		}
	}
	defer func() { execCommandContext = originalExecCommandContext }()

	manager, err := New(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), coredownloader.Config{
		Enabled:   true,
		Root:      root,
		Host:      host,
		Port:      port,
		AccessKey: "hydl-access-key",
		DaemonBin: "fake-daemon",
		ToolsBin:  "fake-tools",
	}, "http://127.0.0.1:45869", "hydrus-go-access-key")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer manager.Shutdown(context.Background()) //nolint:errcheck

	server.Close()
	listener.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	status, err := manager.Status(ctx)
	if err != nil {
		t.Fatalf("Status() error = %v, want nil (errors surfaced via status.LastError)", err)
	}
	if status.LastError == "" {
		t.Fatal("status.LastError = empty, want error message when API is unreachable")
	}
}

func TestManagerAutoRestart_AfterCrash(t *testing.T) {
	root := filepath.Join(t.TempDir(), "hydownloader-root")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer listener.Close()

	host, portString, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatalf("SplitHostPort() error = %v", err)
	}
	port, err := strconv.Atoi(portString)
	if err != nil {
		t.Fatalf("Atoi(port) error = %v", err)
	}

	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeManagerJSON(t, w, map[string]any{"version": "test"})
	})}
	go server.Serve(listener)
	defer server.Close()

	var (
		startMu    sync.Mutex
		startCount int
		readyCh    = make(chan struct{}, 10)
	)

	originalExecCommandContext := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		switch name {
		case "fake-tools":
			if err := os.MkdirAll(root, 0o755); err != nil {
				t.Fatalf("MkdirAll(root) error = %v", err)
			}
			if err := os.WriteFile(filepath.Join(root, hydownloaderDatabaseFileName), []byte("db"), 0o644); err != nil {
				t.Fatalf("WriteFile(hydownloader.db) error = %v", err)
			}
			if err := os.WriteFile(filepath.Join(root, hydownloaderConfigFileName), []byte("{}\n"), 0o644); err != nil {
				t.Fatalf("WriteFile(config) error = %v", err)
			}
			if err := os.WriteFile(filepath.Join(root, hydownloaderImportJobsName), []byte("defAPIURL = \"old\"\ndefAPIKey = \"old\"\n"), 0o644); err != nil {
				t.Fatalf("WriteFile(import jobs) error = %v", err)
			}
			return exec.CommandContext(ctx, "sh", "-c", "true")
		case "fake-daemon":
			startMu.Lock()
			startCount++
			current := startCount
			startMu.Unlock()
			if current == 1 {
				// First start: exits immediately after a brief delay so the startup
				// poll succeeds (the real HTTP server is up), then the process dies.
				readyCh <- struct{}{}
				return exec.CommandContext(ctx, "sh", "-c", "sleep 0.1")
			}
			readyCh <- struct{}{}
			return exec.CommandContext(ctx, "sleep", "300")
		default:
			t.Fatalf("unexpected command %q", name)
			return exec.CommandContext(ctx, "sh", "-c", "false")
		}
	}
	defer func() { execCommandContext = originalExecCommandContext }()

	origInterval := livenessInterval
	origBase := restartBackoffBase
	livenessInterval = 100 * time.Millisecond
	restartBackoffBase = 50 * time.Millisecond
	defer func() {
		livenessInterval = origInterval
		restartBackoffBase = origBase
	}()

	manager, err := New(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), coredownloader.Config{
		Enabled:   true,
		Root:      root,
		Host:      host,
		Port:      port,
		AccessKey: "hydl-access-key",
		DaemonBin: "fake-daemon",
		ToolsBin:  "fake-tools",
	}, "http://127.0.0.1:45869", "hydrus-go-access-key")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer manager.Shutdown(context.Background()) //nolint:errcheck

	deadline := time.After(5 * time.Second)
	restarted := false
	for !restarted {
		select {
		case <-readyCh:
			startMu.Lock()
			count := startCount
			startMu.Unlock()
			if count >= 2 {
				restarted = true
			}
		case <-deadline:
			t.Fatal("daemon was not restarted within 5 seconds after crash")
		}
	}
}

func TestManagerQueueURL_RejectionWithReason(t *testing.T) {
	root := filepath.Join(t.TempDir(), "hydownloader-root")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer listener.Close()

	host, portString, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatalf("SplitHostPort() error = %v", err)
	}
	port, err := strconv.Atoi(portString)
	if err != nil {
		t.Fatalf("Atoi(port) error = %v", err)
	}

	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api_version":
			writeManagerJSON(t, w, map[string]any{"version": "test"})
		case "/add_or_update_urls":
			writeManagerJSON(t, w, map[string]any{"status": false, "reason": "duplicate URL"})
		default:
			writeManagerJSON(t, w, map[string]any{"status": true})
		}
	})}
	go server.Serve(listener)
	defer server.Close()

	originalExecCommandContext := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		switch name {
		case "fake-tools":
			if err := os.MkdirAll(root, 0o755); err != nil {
				t.Fatalf("MkdirAll(root) error = %v", err)
			}
			if err := os.WriteFile(filepath.Join(root, hydownloaderDatabaseFileName), []byte("db"), 0o644); err != nil {
				t.Fatalf("WriteFile(hydownloader.db) error = %v", err)
			}
			if err := os.WriteFile(filepath.Join(root, hydownloaderConfigFileName), []byte("{}\n"), 0o644); err != nil {
				t.Fatalf("WriteFile(config) error = %v", err)
			}
			if err := os.WriteFile(filepath.Join(root, hydownloaderImportJobsName), []byte("defAPIURL = \"old\"\ndefAPIKey = \"old\"\n"), 0o644); err != nil {
				t.Fatalf("WriteFile(import jobs) error = %v", err)
			}
			return exec.CommandContext(ctx, "sh", "-c", "true")
		case "fake-daemon":
			return exec.CommandContext(ctx, "sleep", "300")
		default:
			t.Fatalf("unexpected command %q", name)
			return exec.CommandContext(ctx, "sh", "-c", "false")
		}
	}
	defer func() { execCommandContext = originalExecCommandContext }()

	manager, err := New(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), coredownloader.Config{
		Enabled:   true,
		Root:      root,
		Host:      host,
		Port:      port,
		AccessKey: "hydl-access-key",
		DaemonBin: "fake-daemon",
		ToolsBin:  "fake-tools",
	}, "http://127.0.0.1:45869", "hydrus-go-access-key")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer manager.Shutdown(context.Background()) //nolint:errcheck

	err = manager.QueueURL(context.Background(), coredownloader.URLRequest{URL: "https://example.com/post/1"})
	if err == nil {
		t.Fatal("QueueURL() error = nil, want rejection error")
	}
	if !strings.Contains(err.Error(), "duplicate URL") {
		t.Fatalf("QueueURL() error = %q, want reason 'duplicate URL' in message", err.Error())
	}
}

func TestManagerPatchImportJobs_MissingAssignment(t *testing.T) {
	var logBuf strings.Builder
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	m := &Manager{
		logger:          logger,
		hydrusAPIURL:    "http://127.0.0.1:45869",
		hydrusAccessKey: "test-key",
	}

	root := t.TempDir()
	path := filepath.Join(root, hydownloaderImportJobsName)
	if err := os.WriteFile(path, []byte("# no assignments here\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := m.patchImportJobs(root); err != nil {
		t.Fatalf("patchImportJobs() error = %v", err)
	}

	if !strings.Contains(logBuf.String(), "defAPIURL") {
		t.Errorf("expected warning about missing defAPIURL assignment, got logs: %s", logBuf.String())
	}
	if !strings.Contains(logBuf.String(), "defAPIKey") {
		t.Errorf("expected warning about missing defAPIKey assignment, got logs: %s", logBuf.String())
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !strings.Contains(string(content), `defAPIURL = "http://127.0.0.1:45869"`) {
		t.Errorf("patchImportJobs did not prepend defAPIURL, content: %s", content)
	}
}

func decodeManagerJSON(t *testing.T, reader io.Reader, target any) {
	t.Helper()
	if err := json.NewDecoder(reader).Decode(target); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
}

func writeManagerJSON(t *testing.T, w http.ResponseWriter, payload any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
}
