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
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	coredownloader "github.com/official-elinas/hydrus-go/internal/core/downloader"
)

var execCommandContext = exec.CommandContext

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

	mu       sync.Mutex
	cmd      *exec.Cmd
	waitDone chan error
	lastErr  string
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

	manager := &Manager{
		logger:          logger,
		cfg:             cfg,
		hydrusAPIURL:    strings.TrimSpace(hydrusAPIURL),
		hydrusAccessKey: strings.TrimSpace(hydrusAccessKey),
		httpClient:      &http.Client{Timeout: 30 * time.Second},
	}

	if err := manager.ensureInitialized(ctx); err != nil {
		return nil, err
	}
	if err := manager.start(ctx); err != nil {
		return nil, err
	}

	return manager, nil
}

// Shutdown stops the supervised hydownloader daemon.
func (m *Manager) Shutdown(ctx context.Context) error {
	if m == nil {
		return nil
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

	select {
	case err := <-waitDone:
		m.mu.Lock()
		m.cmd = nil
		m.waitDone = nil
		m.mu.Unlock()
		if err != nil {
			return fmt.Errorf("wait for hydownloader shutdown: %w", err)
		}
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
	if err := m.postJSON(ctx, "/get_status_info", map[string]any{}, &raw); err != nil {
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
		Status bool `json:"status"`
	}
	if err := m.postJSON(ctx, "/add_or_update_urls", body, &response); err != nil {
		return err
	}
	if !response.Status {
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
		Status bool `json:"status"`
	}
	if err := m.postJSON(ctx, "/add_or_update_subscriptions", body, &response); err != nil {
		return err
	}
	if !response.Status {
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
	if err := m.postJSON(ctx, "/downloaders", map[string]any{}, &result); err != nil {
		return nil, err
	}

	return result, nil
}

// ActivateAutoimport resumes hydownloader's autoimport worker after hydrusd is ready.
func (m *Manager) ActivateAutoimport(ctx context.Context) error {
	if m == nil || !m.cfg.Autoimport {
		return nil
	}

	var response struct {
		Status bool `json:"status"`
	}
	if err := m.postJSON(ctx, "/resume_autoimports", map[string]any{}, &response); err != nil {
		return err
	}
	if !response.Status {
		return fmt.Errorf("hydownloader rejected autoimport resume request")
	}

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
		generated, err := randomURLSafeKey()
		if err != nil {
			return err
		}
		m.cfg.AccessKey = generated
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

	updated := replacePythonAssignment(string(raw), "defAPIURL", m.hydrusAPIURL)
	updated = replacePythonAssignment(updated, "defAPIKey", m.hydrusAccessKey)
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

	args := []string{"start", "--path", m.cfg.Root}
	if !m.cfg.Autoimport {
		args = append(args, "--no-autoimporter")
	} else {
		args = append(args, "--paused-autoimporter")
	}
	cmd := execCommandContext(context.Background(), m.cfg.DaemonBin, args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start hydownloader daemon: %w", err)
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
			return nil
		}

		select {
		case err := <-waitDone:
			m.mu.Lock()
			m.cmd = nil
			m.waitDone = nil
			m.lastErr = fmt.Sprintf("hydownloader exited during startup: %v", err)
			m.mu.Unlock()
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
			return fmt.Errorf("wait for hydownloader API: %w", startupCtx.Err())
		case <-ticker.C:
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
		}
	default:
	}
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

func replacePythonAssignment(source string, name string, value string) string {
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

	return strings.Join(lines, "\n")
}

func randomURLSafeKey() (string, error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate hydownloader access key: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(buffer), nil
}
