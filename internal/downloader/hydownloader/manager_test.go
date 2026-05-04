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
			mu.Unlock()
			writeManagerJSON(t, w, map[string]any{"status": true})
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
