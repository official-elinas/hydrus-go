package hydownloader

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	neturl "net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	coredownloader "github.com/official-elinas/hydrus-go/internal/core/downloader"
)

var execCommandContext = exec.CommandContext

var killOrphanDaemonFn func(m *Manager, ctx context.Context) = (*Manager).killOrphanDaemon

var (
	livenessInterval   = 30 * time.Second
	restartBackoffBase = 2 * time.Second
	restartBackoffMax  = 2 * time.Minute
	restartBackoffMult = 2.0
)

const (
	hydownloaderConfigFileName   = "hydownloader-config.json"
	hydownloaderImportJobsName   = "hydownloader-import-jobs.py"
	hydownloaderDatabaseFileName = "hydownloader.db"
	hydownloaderInitTimeout      = 30 * time.Second
	hydownloaderShutdownWait     = 10 * time.Second
	hydownloaderStartupWait      = 20 * time.Second
)

// Manager supervises an external hydownloader daemon and exposes a stable
// daemon-owned queue/status surface to hydrus-go.
type Manager struct {
	logger          *slog.Logger
	cfg             coredownloader.Config
	hydrusAPIURL    string
	hydrusAccessKey string
	httpClient      *http.Client

	mu            sync.Mutex
	cmd           *exec.Cmd
	waitDone      chan error
	lastErr       string
	stopLiveness  chan struct{}
	livenessDone  chan struct{}
	livenessCtx   context.Context
	livenessCancel context.CancelFunc
}

// New prepares the hydownloader root, starts the daemon process, and waits for
// its API to become reachable.
func New(ctx context.Context, logger *slog.Logger, cfg coredownloader.Config, hydrusAPIURL string, hydrusAccessKey string) (*Manager, error) {
	if !cfg.Enabled {
		return nil, nil
	}

	if logger == nil {
		logger = slog.Default()
	}

	livenessCtx, livenessCancel := context.WithCancel(context.Background())
	manager := &Manager{
		logger:          logger,
		cfg:             cfg,
		hydrusAPIURL:    strings.TrimSpace(hydrusAPIURL),
		hydrusAccessKey: strings.TrimSpace(hydrusAccessKey),
		httpClient:      &http.Client{Timeout: 30 * time.Second},
		stopLiveness:    make(chan struct{}),
		livenessDone:    make(chan struct{}),
		livenessCtx:     livenessCtx,
		livenessCancel:  livenessCancel,
	}

	if err := manager.ensureInitialized(ctx); err != nil {
		return nil, err
	}
	killOrphanDaemonFn(manager, ctx)
	if err := manager.start(ctx); err != nil {
		return nil, err
	}

	go func() {
		defer close(manager.livenessDone)
		manager.livenessLoop()
	}()

	return manager, nil
}

// Shutdown stops the supervised hydownloader daemon.
func (m *Manager) Shutdown(ctx context.Context) error {
	if m == nil {
		return nil
	}

	select {
	case <-m.stopLiveness:
	default:
		close(m.stopLiveness)
	}
	m.livenessCancel()

	m.logger.Info("shutting down hydownloader daemon", "root", m.cfg.Root, "host", m.cfg.Host, "port", m.cfg.Port)

	select {
	case <-m.livenessDone:
	case <-ctx.Done():
		return ctx.Err()
	}

	m.refreshProcessState()

	m.mu.Lock()
	cmd := m.cmd
	waitDone := m.waitDone
	m.mu.Unlock()
	if cmd == nil || waitDone == nil {
		return nil
	}

	shutdownCtx := ctx
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		shutdownCtx, cancel = context.WithTimeout(ctx, hydownloaderShutdownWait)
		defer cancel()
	}

	_ = m.postJSON(shutdownCtx, "/shutdown", map[string]any{}, nil)

	addr := fmt.Sprintf("%s:%d", m.cfg.Host, m.cfg.Port)
	select {
	case err := <-waitDone:
		m.mu.Lock()
		m.cmd = nil
		m.waitDone = nil
		m.mu.Unlock()
		if err != nil {
			m.logger.Error("hydownloader shutdown wait returned error", "error", err)
			return fmt.Errorf("wait for hydownloader shutdown: %w", err)
		}
		m.logger.Info("hydownloader daemon stopped cleanly")
		m.waitForPortFree(addr, 5*time.Second)
		return nil
	case <-shutdownCtx.Done():
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		select {
		case err := <-waitDone:
			m.mu.Lock()
			m.cmd = nil
			m.waitDone = nil
			m.mu.Unlock()
			var exitErr *exec.ExitError
			if err != nil && !errors.As(err, &exitErr) {
				return fmt.Errorf("kill hydownloader process: %w", err)
			}
			m.waitForPortFree(addr, 5*time.Second)
			return nil
		case <-time.After(5 * time.Second):
			return fmt.Errorf("wait for hydownloader shutdown: %w", shutdownCtx.Err())
		}
	}
}

// Status reports the current daemon-owned downloader status.
func (m *Manager) Status(ctx context.Context) (coredownloader.Status, error) {
	if m == nil {
		return coredownloader.Status{}, &coredownloader.NotConfiguredError{Message: "hydownloader integration is disabled"}
	}

	m.refreshProcessState()
	status := coredownloader.Status{
		Enabled:    true,
		Configured: true,
		Root:       m.cfg.Root,
		BaseURL:    m.baseURL(),
		Autoimport: m.cfg.Autoimport,
	}

	m.mu.Lock()
	status.LastError = m.lastErr
	status.Running = m.cmd != nil
	m.mu.Unlock()

	var raw hydownloaderStatusResponse
	if err := m.postJSONWithRetry(ctx, "/get_status_info", map[string]any{}, &raw); err != nil {
		m.logger.Warn("hydownloader status check failed", "error", err, "host", m.cfg.Host, "port", m.cfg.Port)
		status.LastError = err.Error()
		return status, nil
	}

	status.Running = true
	status.URLsQueued = raw.URLsQueued
	status.SubscriptionsDue = raw.SubscriptionsDue
	status.SubscriptionsPaused = raw.SubscriptionsPaused
	status.URLsPaused = raw.URLsPaused
	status.AutoimportPaused = raw.AutoimportJobsPaused
	status.SubscriptionWorkerStatus = raw.SubscriptionWorkerStatus
	status.URLWorkerStatus = raw.URLWorkerStatus
	status.AutoimportWorkerStatus = raw.AutoimportWorkerStatus
	status.SubscriptionWorkerUpdatedAt = raw.SubscriptionWorkerLastUpdateTime
	status.URLWorkerUpdatedAt = raw.URLWorkerLastUpdateTime
	status.AutoimportWorkerUpdatedAt = raw.AutoimportWorkerLastUpdateTime
	status.LastError = ""
	return status, nil
}

// QueueURL queues one hydownloader single URL job.
func (m *Manager) QueueURL(ctx context.Context, request coredownloader.URLRequest) error {
	if m == nil {
		return &coredownloader.NotConfiguredError{Message: "hydownloader integration is disabled"}
	}

	url := strings.TrimSpace(request.URL)
	if url == "" {
		return &coredownloader.RequestError{Message: "url is required"}
	}

	body := []map[string]any{{
		"url":                url,
		"priority":           request.Priority,
		"ignore_anchor":      request.IgnoreAnchor,
		"additional_data":    strings.TrimSpace(request.AdditionalData),
		"metadata_only":      request.MetadataOnly,
		"overwrite_existing": request.OverwriteExisting,
		"filter":             strings.TrimSpace(request.Filter),
		"paused":             request.Paused,
	}}
	if request.MaxFiles != nil {
		body[0]["max_files"] = *request.MaxFiles
	}
	if request.Autoimport != nil {
		body[0]["autoimport"] = *request.Autoimport
	} else {
		body[0]["autoimport"] = m.cfg.Autoimport
	}

	var response struct {
		Status bool   `json:"status"`
		Reason string `json:"reason"`
	}
	if err := m.postJSON(ctx, "/add_or_update_urls", body, &response); err != nil {
		return err
	}
	if !response.Status {
		if response.Reason != "" {
			return fmt.Errorf("hydownloader rejected queued URL: %s", response.Reason)
		}
		return fmt.Errorf("hydownloader rejected queued URL")
	}

	return nil
}

// QueueSubscription queues one hydownloader subscription.
func (m *Manager) QueueSubscription(ctx context.Context, request coredownloader.SubscriptionRequest) error {
	if m == nil {
		return &coredownloader.NotConfiguredError{Message: "hydownloader integration is disabled"}
	}

	downloaderName := strings.TrimSpace(request.Downloader)
	keywords := strings.TrimSpace(request.Keywords)
	if downloaderName == "" {
		return &coredownloader.RequestError{Message: "downloader is required"}
	}
	if keywords == "" {
		return &coredownloader.RequestError{Message: "keywords are required"}
	}
	if request.CheckInterval <= 0 {
		return &coredownloader.RequestError{Message: "check_interval must be greater than zero"}
	}

	body := []map[string]any{{
		"downloader":      downloaderName,
		"keywords":        keywords,
		"additional_data": strings.TrimSpace(request.AdditionalData),
		"check_interval":  request.CheckInterval,
		"priority":        request.Priority,
		"paused":          request.Paused,
		"filter":          strings.TrimSpace(request.Filter),
		"worker_id":       strings.TrimSpace(request.WorkerID),
	}}
	if request.AbortAfter != nil {
		body[0]["abort_after"] = *request.AbortAfter
	}
	if request.MaxFilesInitial != nil {
		body[0]["max_files_initial"] = *request.MaxFilesInitial
	}
	if request.MaxFilesRegular != nil {
		body[0]["max_files_regular"] = *request.MaxFilesRegular
	}
	if request.Autoimport != nil {
		body[0]["autoimport"] = *request.Autoimport
	} else {
		body[0]["autoimport"] = m.cfg.Autoimport
	}

	var response struct {
		Status bool   `json:"status"`
		Reason string `json:"reason"`
	}
	if err := m.postJSON(ctx, "/add_or_update_subscriptions", body, &response); err != nil {
		return err
	}
	if !response.Status {
		if response.Reason != "" {
			return fmt.Errorf("hydownloader rejected subscription request: %s", response.Reason)
		}
		return fmt.Errorf("hydownloader rejected subscription request")
	}

	return nil
}

// Downloaders returns the supported hydownloader subscription downloader map.
func (m *Manager) Downloaders(ctx context.Context) (map[string]string, error) {
	if m == nil {
		return nil, &coredownloader.NotConfiguredError{Message: "hydownloader integration is disabled"}
	}

	result := map[string]string{}
	if err := m.postJSONWithRetry(ctx, "/downloaders", map[string]any{}, &result); err != nil {
		return nil, err
	}

	return result, nil
}

// ActivateAutoimport resumes hydownloader's autoimport worker after hydrusd is ready.
func (m *Manager) ActivateAutoimport(ctx context.Context) error {
	if m == nil || !m.cfg.Autoimport {
		return nil
	}

	m.logger.Info("activating hydownloader autoimport", "host", m.cfg.Host, "port", m.cfg.Port)

	var response struct {
		Status bool `json:"status"`
	}
	if err := m.postJSON(ctx, "/resume_autoimports", map[string]any{}, &response); err != nil {
		m.logger.Error("hydownloader autoimport activation failed", "error", err)
		return err
	}
	if !response.Status {
		m.logger.Error("hydownloader rejected autoimport resume request")
		return fmt.Errorf("hydownloader rejected autoimport resume request")
	}

	m.logger.Info("hydownloader autoimport activated")
	return nil
}

type hydownloaderStatusResponse struct {
	SubscriptionsDue                 int64   `json:"subscriptions_due"`
	URLsQueued                       int64   `json:"urls_queued"`
	SubscriptionsPaused              bool    `json:"subscriptions_paused"`
	URLsPaused                       bool    `json:"urls_paused"`
	AutoimportJobsPaused             bool    `json:"autoimport_jobs_paused"`
	SubscriptionWorkerStatus         string  `json:"subscription_worker_status"`
	URLWorkerStatus                  string  `json:"url_worker_status"`
	AutoimportWorkerStatus           string  `json:"autoimport_worker_status"`
	SubscriptionWorkerLastUpdateTime float64 `json:"subscription_worker_last_update_time"`
	URLWorkerLastUpdateTime          float64 `json:"url_worker_last_update_time"`
	AutoimportWorkerLastUpdateTime   float64 `json:"autoimport_worker_last_update_time"`
}

func (m *Manager) ensureInitialized(ctx context.Context) error {
	root := filepath.Clean(strings.TrimSpace(m.cfg.Root))
	if root == "" || root == "." {
		return fmt.Errorf("hydownloader root is required")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("create hydownloader root: %w", err)
	}

	if strings.TrimSpace(m.cfg.AccessKey) == "" {
		if existing := readExistingHydownloaderAccessKey(filepath.Join(root, hydownloaderConfigFileName)); existing != "" {
			m.cfg.AccessKey = existing
		} else {
			generated, err := randomURLSafeKey()
			if err != nil {
				return err
			}
			m.cfg.AccessKey = generated
		}
	}

	if _, err := os.Stat(filepath.Join(root, hydownloaderDatabaseFileName)); os.IsNotExist(err) {
		initCtx, cancel := context.WithTimeout(ctx, hydownloaderInitTimeout)
		defer cancel()
		cmd := execCommandContext(initCtx, m.cfg.ToolsBin, "init-db", "--path", root)
		output, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("initialize hydownloader root: %w: %s", err, strings.TrimSpace(string(output)))
		}
	} else if err != nil {
		return fmt.Errorf("stat hydownloader database: %w", err)
	}

	if err := m.patchHydownloaderConfig(root); err != nil {
		return err
	}
	if err := m.patchImportJobs(root); err != nil {
		return err
	}

	return nil
}

func (m *Manager) patchHydownloaderConfig(root string) error {
	path := filepath.Join(root, hydownloaderConfigFileName)
	payload := map[string]any{}
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read hydownloader config: %w", err)
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &payload); err != nil {
			return fmt.Errorf("decode hydownloader config: %w", err)
		}
	}

	payload["daemon.host"] = m.cfg.Host
	payload["daemon.port"] = m.cfg.Port
	payload["daemon.ssl"] = false
	payload["daemon.access-key"] = m.cfg.AccessKey
	payload["daemon.fill-import-queue"] = true

	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("encode hydownloader config: %w", err)
	}
	encoded = append(encoded, '\n')
	if err := os.WriteFile(path, encoded, 0o644); err != nil {
		return fmt.Errorf("write hydownloader config: %w", err)
	}

	return nil
}

func (m *Manager) patchImportJobs(root string) error {
	path := filepath.Join(root, hydownloaderImportJobsName)
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read hydownloader import jobs: %w", err)
	}

	updated, urlReplaced := replacePythonAssignment(string(raw), "defAPIURL", m.hydrusAPIURL)
	if !urlReplaced {
		m.logger.Warn("defAPIURL assignment not found in hydownloader import jobs; prepending line — file format may have changed", "path", path)
	}
	updated, keyReplaced := replacePythonAssignment(updated, "defAPIKey", m.hydrusAccessKey)
	if !keyReplaced {
		m.logger.Warn("defAPIKey assignment not found in hydownloader import jobs; prepending line — file format may have changed", "path", path)
	}

	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		return fmt.Errorf("write hydownloader import jobs: %w", err)
	}

	return nil
}

func (m *Manager) start(ctx context.Context) error {
	m.refreshProcessState()
	m.mu.Lock()
	alreadyRunning := m.cmd != nil
	m.mu.Unlock()
	if alreadyRunning {
		return nil
	}

	m.logger.Info("starting hydownloader daemon", "root", m.cfg.Root, "host", m.cfg.Host, "port", m.cfg.Port)

	args := []string{"start", "--path", m.cfg.Root}
	if !m.cfg.Autoimport {
		args = append(args, "--no-autoimporter")
	} else {
		args = append(args, "--paused-autoimporter")
	}
	cmd := execCommandContext(context.Background(), m.cfg.DaemonBin, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
	cmd.Stdout = io.Discard

	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		m.logger.Warn("could not capture hydownloader stderr pipe; stderr will be discarded", "error", err)
		cmd.Stderr = io.Discard
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start hydownloader daemon: %w", err)
	}

	if stderrPipe != nil {
		go m.drainStderr(stderrPipe)
	}

	waitDone := make(chan error, 1)
	go func() {
		waitDone <- cmd.Wait()
	}()

	m.mu.Lock()
	m.cmd = cmd
	m.waitDone = waitDone
	m.lastErr = ""
	m.mu.Unlock()

	startupCtx, cancel := context.WithTimeout(ctx, hydownloaderStartupWait)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := m.postJSON(startupCtx, "/api_version", map[string]any{}, nil); err == nil {
			m.logger.Info("hydownloader daemon is ready", "host", m.cfg.Host, "port", m.cfg.Port)
			return nil
		}

		select {
		case err := <-waitDone:
			m.mu.Lock()
			m.cmd = nil
			m.waitDone = nil
			m.lastErr = fmt.Sprintf("hydownloader exited during startup: %v", err)
			m.mu.Unlock()
			m.logger.Error("hydownloader exited during startup", "error", err, "root", m.cfg.Root)
			return fmt.Errorf("hydownloader exited during startup: %w", err)
		case <-startupCtx.Done():
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			select {
			case <-waitDone:
			case <-time.After(5 * time.Second):
			}
			m.mu.Lock()
			m.cmd = nil
			m.waitDone = nil
			m.lastErr = startupCtx.Err().Error()
			m.mu.Unlock()
			m.logger.Error("timed out waiting for hydownloader API", "timeout", hydownloaderStartupWait, "root", m.cfg.Root)
			return fmt.Errorf("wait for hydownloader API: %w", startupCtx.Err())
		case <-ticker.C:
		}
	}
}

// livenessLoop polls /api_version every livenessInterval and relaunches the
// daemon on crash with capped exponential backoff. It exits when stopLiveness
// is closed.
func (m *Manager) livenessLoop() {
	backoff := restartBackoffBase
	ticker := time.NewTicker(livenessInterval)
	defer ticker.Stop()

	for {
		select {
		case <-m.stopLiveness:
			return
		case <-ticker.C:
		}

		m.refreshProcessState()

		m.mu.Lock()
		running := m.cmd != nil
		m.mu.Unlock()

		if !running {
			m.logger.Warn("hydownloader daemon has exited; scheduling restart",
				"backoff", backoff, "root", m.cfg.Root)

		select {
		case <-m.stopLiveness:
			return
		case <-time.After(backoff):
		}

		select {
		case <-m.stopLiveness:
			return
		default:
		}

		backoff = time.Duration(float64(backoff) * restartBackoffMult)
		if backoff > restartBackoffMax {
			backoff = restartBackoffMax
		}

		m.logger.Info("restarting hydownloader daemon", "root", m.cfg.Root, "host", m.cfg.Host, "port", m.cfg.Port)
		if err := m.start(m.livenessCtx); err != nil {
				m.logger.Error("hydownloader restart failed", "error", err, "root", m.cfg.Root)
				m.mu.Lock()
				m.lastErr = err.Error()
				m.mu.Unlock()
			} else {
				m.logger.Info("hydownloader daemon restarted successfully", "root", m.cfg.Root)
				backoff = restartBackoffBase
			}
			continue
		}

		probeCtx, cancel := context.WithTimeout(m.livenessCtx, 5*time.Second)
		apiErr := m.postJSON(probeCtx, "/api_version", map[string]any{}, nil)
		cancel()

		if apiErr != nil {
			m.logger.Warn("hydownloader liveness check failed; daemon may be unresponsive",
				"error", apiErr, "host", m.cfg.Host, "port", m.cfg.Port)
		} else {
			backoff = restartBackoffBase
		}
	}
}

// CheckCallbackURLReachability performs a best-effort TCP dial to the public
// API URL and logs a warning if it is unreachable.
func (m *Manager) CheckCallbackURLReachability() {
	rawURL := strings.TrimSpace(m.hydrusAPIURL)
	if rawURL == "" {
		return
	}

	parsed, err := neturl.Parse(rawURL)
	if err != nil {
		m.logger.Warn("hydownloader callback URL is unparseable; autoimport callbacks may fail",
			"url", rawURL, "error", err)
		return
	}

	host := parsed.Hostname()
	port := parsed.Port()
	if port == "" {
		switch parsed.Scheme {
		case "https":
			port = "443"
		default:
			port = "80"
		}
	}

	addr := net.JoinHostPort(host, port)
	conn, dialErr := net.DialTimeout("tcp", addr, 3*time.Second)
	if dialErr != nil {
		m.logger.Warn(
			"hydownloader callback URL is unreachable; autoimport callbacks will fail until resolved",
			"url", rawURL,
			"addr", addr,
			"error", dialErr,
		)
		return
	}
	conn.Close()
	m.logger.Info("hydownloader callback URL is reachable", "url", rawURL, "addr", addr)
}

// drainStderr reads from the hydownloader process stderr pipe and forwards
// each non-empty line to the structured logger.
func (m *Manager) drainStderr(r io.Reader) {
	buf := make([]byte, 4096)
	remainder := ""
	for {
		n, err := r.Read(buf)
		if n > 0 {
			chunk := remainder + string(buf[:n])
			lines := strings.Split(chunk, "\n")
			remainder = lines[len(lines)-1]
			for _, line := range lines[:len(lines)-1] {
				trimmed := strings.TrimSpace(line)
				if trimmed != "" {
					m.logger.Info("hydownloader stderr", "line", trimmed)
				}
			}
		}
		if err != nil {
			if trimmed := strings.TrimSpace(remainder); trimmed != "" {
				m.logger.Info("hydownloader stderr", "line", trimmed)
			}
			return
		}
	}
}

func (m *Manager) refreshProcessState() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.waitDone == nil {
		return
	}

	select {
	case err := <-m.waitDone:
		m.cmd = nil
		m.waitDone = nil
		if err != nil {
			m.lastErr = err.Error()
			m.logger.Error("hydownloader process exited unexpectedly", "error", err)
		}
	default:
	}
}

var retryBackoffs = []time.Duration{time.Second, 2 * time.Second, 3 * time.Second}

func (m *Manager) postJSONWithRetry(ctx context.Context, path string, body any, target any) error {
	var lastErr error
	for attempt, delay := range retryBackoffs {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
		}
		if lastErr = m.postJSON(ctx, path, body, target); lastErr == nil {
			return nil
		}
	}
	return lastErr
}

func (m *Manager) postJSON(ctx context.Context, path string, body any, target any) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode hydownloader request body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.baseURL()+path, bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("build hydownloader request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("HyDownloader-Access-Key", m.cfg.AccessKey)

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("perform hydownloader request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		return fmt.Errorf("hydownloader request returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(payload)))
	}
	if target == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(target); err != nil {
		return fmt.Errorf("decode hydownloader response: %w", err)
	}

	return nil
}

func (m *Manager) baseURL() string {
	return fmt.Sprintf("http://%s:%d", m.cfg.Host, m.cfg.Port)
}

func replacePythonAssignment(source string, name string, value string) (string, bool) {
	lines := strings.Split(source, "\n")
	replacement := fmt.Sprintf("%s = %q", name, value)
	replaced := false
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, name+" =") {
			lines[index] = replacement
			replaced = true
		}
	}
	if !replaced {
		lines = append([]string{replacement}, lines...)
	}

	return strings.Join(lines, "\n"), replaced
}

func (m *Manager) killOrphanDaemon(ctx context.Context) {
	addr := fmt.Sprintf("%s:%d", m.cfg.Host, m.cfg.Port)
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return
	}
	conn.Close()

	m.logger.Warn("found orphan hydownloader process on port; attempting graceful shutdown", "addr", addr)
	shutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_ = m.postJSON(shutCtx, "/shutdown", map[string]any{}, nil)

	if m.waitForPortFree(addr, 30*time.Second) {
		m.logger.Info("orphan hydownloader stopped", "addr", addr)
		return
	}

	m.logger.Warn("orphan hydownloader did not stop gracefully; force-killing by port", "addr", addr)
	if pid, err := findPIDByPort(m.cfg.Port); err == nil && pid > 0 {
		if kerr := syscall.Kill(pid, syscall.SIGKILL); kerr != nil {
			m.logger.Warn("force-kill orphan hydownloader failed", "pid", pid, "error", kerr)
		} else {
			m.logger.Info("force-killed orphan hydownloader", "pid", pid)
			m.waitForPortFree(addr, 10*time.Second)
		}
	} else {
		m.logger.Warn("could not find orphan hydownloader PID for force-kill", "port", m.cfg.Port)
	}
}

func (m *Manager) waitForPortFree(addr string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	consecutive := 0
	for time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)
		c, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err != nil {
			consecutive++
			if consecutive >= 2 {
				return true
			}
			continue
		}
		c.Close()
		consecutive = 0
	}
	return false
}

func readExistingHydownloaderAccessKey(configPath string) string {
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return ""
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ""
	}
	key, _ := payload["daemon.access-key"].(string)
	return strings.TrimSpace(key)
}

func findPIDByPort(port int) (int, error) {
	target := fmt.Sprintf("%04X", port)
	for _, path := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(raw), "\n")[1:] {
			fields := strings.Fields(line)
			if len(fields) < 10 {
				continue
			}
			localAddr := fields[1]
			parts := strings.SplitN(localAddr, ":", 2)
			if len(parts) != 2 || !strings.EqualFold(parts[1], target) {
				continue
			}
			if fields[3] != "0A" {
				continue
			}
			inode := fields[9]
			pid, err := findPIDByInode(inode)
			if err == nil && pid > 0 {
				return pid, nil
			}
		}
	}
	return 0, fmt.Errorf("no process found listening on port %d", port)
}

func findPIDByInode(inode string) (int, error) {
	target := "socket:[" + inode + "]"
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid := 0
		if _, err := fmt.Sscanf(e.Name(), "%d", &pid); err != nil || pid <= 0 {
			continue
		}
		fdDir := fmt.Sprintf("/proc/%d/fd", pid)
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			continue
		}
		for _, fd := range fds {
			link, err := os.Readlink(fmt.Sprintf("%s/%s", fdDir, fd.Name()))
			if err == nil && link == target {
				return pid, nil
			}
		}
	}
	return 0, fmt.Errorf("inode %s not found in /proc", inode)
}

func randomURLSafeKey() (string, error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate hydownloader access key: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(buffer), nil
}
