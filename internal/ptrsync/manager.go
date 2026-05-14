// Package ptrsync manages daemon-owned Public Tag Repository sync state.
package ptrsync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	coreptrsync "github.com/official-elinas/hydrus-go/internal/core/ptrsync"
	"github.com/official-elinas/hydrus-go/internal/db/hydrusdb"
	"github.com/official-elinas/hydrus-go/internal/storage/clientfiles"
)

// Manager provides daemon-visible PTR sync status backed by the Hydrus bundle.
type Manager struct {
	logger            *slog.Logger
	stateMu           sync.RWMutex
	cfg               coreptrsync.Config
	readBundle        *hydrusdb.Bundle
	writeBundle       *hydrusdb.Bundle
	unavailableReason string
	runnerCtx         context.Context
	runnerCancel      context.CancelFunc
	runMu             sync.Mutex
	activeRun         *activeRun
	wakeupMu          sync.Mutex
	wakeupID          uint64
	wakeupCancel      context.CancelFunc
}

type activeRun struct {
	cancel                context.CancelFunc
	done                  chan struct{}
	status                coreptrsync.Status
	runToken              string
	networkFetchedBytes   int64
	networkFetchMS        int64
	networkBytesPerSecond int64
}

type startedSync struct {
	client          *Client
	lease           hydrusdb.PTRSyncLease
	replaceMetadata bool
}

type downloadedPTRUpdateBatchItem struct {
	hashHex        string
	body           []byte
	preparedImport hydrusdb.PreparedLocalImport
}

type fetchedPTRUpdateResult struct {
	pendingHash []byte
	body        []byte
	mimeEnum    int
	err         error
}

const (
	ptrSyncDownloadedUpdateBatchSize = 25
	// Keep update fetch concurrency low enough that the public PTR is less likely
	// to reset long-running runs while still overlapping request latency.
	ptrSyncDownloadParallelism  = 3
	ptrSyncOptInMarkerName      = "ptrsync"
	ptrSyncBusyPause            = 10 * time.Minute
	ptrSyncThrottleMaxDelay     = 2 * time.Minute
	ptrSyncMaxBusyRetryAttempts = 10
)

var ptrSyncInitialBusyRetryDelays = []time.Duration{
	2 * time.Second,
	3 * time.Second,
	4 * time.Second,
	5 * time.Second,
	5 * time.Second,
}

// NewManager constructs the daemon PTR status manager, ensures persisted local
// state exists, and performs startup recovery of stale PTR runtime state when
// PTR sync is explicitly enabled and a writable bundle is available.
func NewManager(
	ctx context.Context,
	logger *slog.Logger,
	cfg coreptrsync.Config,
	readBundle *hydrusdb.Bundle,
	writeBundle *hydrusdb.Bundle,
) (*Manager, error) {
	runnerCtx, runnerCancel := context.WithCancel(context.Background())

	manager := &Manager{
		logger:       logger,
		cfg:          cfg,
		readBundle:   readBundle,
		writeBundle:  writeBundle,
		runnerCtx:    runnerCtx,
		runnerCancel: runnerCancel,
	}

	optedInByMarker := false
	if !cfg.Enabled && readBundle != nil && writeBundle != nil {
		markerExists, markerErr := ptrSyncOptInMarkerExists(writeBundle)
		if markerErr != nil {
			return nil, fmt.Errorf("check PTR sync opt-in marker: %w", markerErr)
		}
		if markerExists {
			cfg.Enabled = true
			manager.cfg = cfg
			optedInByMarker = true
		}
	}

	if !cfg.Enabled {
		return manager, nil
	}

	if readBundle == nil {
		manager.unavailableReason = "PTR sync requires HYDRUS_GO_DB_DIR to be configured"
		return manager, nil
	}

	if writeBundle == nil {
		manager.unavailableReason = "PTR sync requires a writable Hydrus DB bundle"
		return manager, nil
	}

	recoveredStatus, err := writeBundle.RecoverPTRSyncFoundation(ctx, cfg)
	if err != nil {
		if errors.Is(err, hydrusdb.ErrPTRServiceNameCollision) {
			manager.unavailableReason = err.Error()
			if logger != nil {
				logger.Warn(
					"anonymous PTR sync unavailable",
					"reason",
					manager.unavailableReason,
				)
			}

			return manager, nil
		}

		return nil, fmt.Errorf("ensure PTR sync foundation: %w", err)
	}

	now := time.Now().UTC()
	if recoveredStatus.Phase == coreptrsync.PhaseRetrying {
		manager.schedulePTRRetryWakeup(recoveredStatus.RetryAtMS)
	} else {
		nextUpdateDue, err := readBundle.GetPTRNextUpdateDue(ctx, cfg)
		if err != nil {
			return nil, fmt.Errorf("load PTR next update due: %w", err)
		}
		if shouldSchedulePTRNextUpdateWakeup(nextUpdateDue, now) {
			manager.schedulePTRNextUpdateWakeup(nextUpdateDue)
		} else if optedInByMarker {
			go func() {
				_, _ = manager.Trigger(context.Background())
			}()
		} else {
			hasMappings, mappingsErr := readBundle.HasPTRCurrentMappings(ctx)
			if mappingsErr != nil {
				if logger != nil {
					logger.Warn("could not check PTR current mappings for auto-start", "error", mappingsErr)
				}
			} else if hasMappings {
				go func() {
					_, _ = manager.Trigger(context.Background())
				}()
			}
		}
	}

	if logger != nil {
		logger.Info(
			"prepared anonymous PTR sync foundation",
			"service_name",
			cfg.ServiceName,
			"host",
			cfg.Host,
			"port",
			cfg.Port,
		)
	}

	return manager, nil
}

// Trigger starts one daemon-owned background PTR sync pass when one is not
// already active and returns the daemon-visible status immediately.
func (m *Manager) Trigger(ctx context.Context) (coreptrsync.Status, error) {
	if m == nil {
		return coreptrsync.Status{Phase: coreptrsync.PhaseUnavailable}, coreptrsync.ErrSyncUnavailable
	}

	m.runMu.Lock()
	defer m.runMu.Unlock()

	if m.activeRun != nil {
		return m.activeRun.status, nil
	}

	cfg, _ := m.snapshotState()
	if !cfg.Enabled {
		enabledStatus, enableErr := m.enablePTRSyncFromManualTrigger(ctx)
		if enableErr != nil {
			return enabledStatus, enableErr
		}
	}

	started, status, err := m.beginSync(ctx)
	if err != nil {
		if errors.Is(err, coreptrsync.ErrSyncAlreadyRunning) {
			return status, nil
		}

		return status, err
	}

	if started.client == nil {
		return status, nil
	}
	m.cancelPTRWakeup()

	runCtx, cancel := context.WithCancel(m.runnerCtx)
	done := make(chan struct{})
	m.activeRun = &activeRun{cancel: cancel, done: done, status: status, runToken: started.lease.RunToken}

	go func(started startedSync, done chan struct{}) {
		defer close(done)
		defer cancel()

		status, err := m.runStartedSync(runCtx, started)

		nextUpdateDue := int64(0)
		scheduleNextWakeup := false
		continueImmediately := false
		if err == nil && status.Phase != coreptrsync.PhaseRetrying {
			cfg, _ := m.snapshotState()
			loadedNextUpdateDue, nextUpdateErr := m.readBundle.GetPTRNextUpdateDue(context.Background(), cfg)
			if nextUpdateErr != nil {
				err = fmt.Errorf("load PTR next update due after trigger run: %w", nextUpdateErr)
			} else {
				nextUpdateDue = loadedNextUpdateDue
				now := time.Now().UTC()
				continueImmediately = shouldContinuePTRSync(nextUpdateDue, started.lease.Status.MetadataSlice, status.MetadataSlice, now)
				scheduleNextWakeup = shouldSchedulePTRNextUpdateWakeup(nextUpdateDue, now)
			}
		}
		if err != nil {
			if m.logger != nil {
				m.logger.Warn(
					"PTR sync trigger run failed",
					"error",
					err,
					"phase",
					status.Phase,
					"metadata_slice",
					status.MetadataSlice,
				)
			}
		} else if m.logger != nil {
			m.logger.Info(
				"completed PTR sync trigger run",
				"metadata_slice",
				status.MetadataSlice,
			)
		}

		m.runMu.Lock()
		defer m.runMu.Unlock()

		if m.activeRun != nil && m.activeRun.done == done {
			m.activeRun = nil
		}

		if err == nil && status.Phase != coreptrsync.PhaseRetrying {
			if continueImmediately {
				go func() {
					_, _ = m.Trigger(context.Background())
				}()
			} else if scheduleNextWakeup {
				m.schedulePTRNextUpdateWakeup(nextUpdateDue)
			}
		}
	}(started, done)

	return status, nil
}

func (m *Manager) snapshotState() (coreptrsync.Config, string) {
	if m == nil {
		return coreptrsync.Config{}, ""
	}

	m.stateMu.RLock()
	defer m.stateMu.RUnlock()
	return m.cfg, m.unavailableReason
}

func (m *Manager) setEnabledFlag(enabled bool) {
	if m == nil {
		return
	}

	m.stateMu.Lock()
	m.cfg.Enabled = enabled
	m.stateMu.Unlock()
}

func (m *Manager) setUnavailableReason(reason string) {
	if m == nil {
		return
	}

	m.stateMu.Lock()
	m.unavailableReason = reason
	m.stateMu.Unlock()
}

func (m *Manager) clearUnavailableReason() {
	m.setUnavailableReason("")
}

func (m *Manager) updateActiveRunNetworkMetrics(runToken string, fetchedBytes int64, fetchMS int64) {
	if m == nil {
		return
	}

	m.runMu.Lock()
	defer m.runMu.Unlock()
	if m.activeRun == nil || m.activeRun.runToken != runToken {
		return
	}

	m.activeRun.networkFetchedBytes = fetchedBytes
	m.activeRun.networkFetchMS = fetchMS
	if fetchedBytes > 0 && fetchMS > 0 {
		m.activeRun.networkBytesPerSecond = (fetchedBytes * 1000) / fetchMS
	} else {
		m.activeRun.networkBytesPerSecond = 0
	}
}

func (m *Manager) enablePTRSyncFromManualTrigger(ctx context.Context) (coreptrsync.Status, error) {
	if m == nil {
		return coreptrsync.Status{Phase: coreptrsync.PhaseUnavailable}, coreptrsync.ErrSyncUnavailable
	}

	if m.readBundle == nil || m.writeBundle == nil {
		cfg, _ := m.snapshotState()
		status := disabledStatus(cfg)
		status.Phase = coreptrsync.PhaseUnavailable
		status.UnavailableReason = "PTR sync requires readable and writable Hydrus DB bundles"
		return status, fmt.Errorf("%w: %s", coreptrsync.ErrSyncUnavailable, status.UnavailableReason)
	}

	enabledCfg, _ := m.snapshotState()
	enabledCfg.Enabled = true

	recoveredStatus, err := m.writeBundle.RecoverPTRSyncFoundation(context.Background(), enabledCfg)
	if err != nil {
		if errors.Is(err, hydrusdb.ErrPTRServiceNameCollision) {
			m.setUnavailableReason(err.Error())
			status := disabledStatus(enabledCfg)
			status.Phase = coreptrsync.PhaseUnavailable
			_, unavailableReason := m.snapshotState()
			status.UnavailableReason = unavailableReason
			return status, fmt.Errorf("%w: %s", coreptrsync.ErrSyncUnavailable, unavailableReason)
		}

		return coreptrsync.Status{}, fmt.Errorf("enable PTR sync from manual trigger: %w", err)
	}

	markerPath, err := ptrSyncOptInMarkerPath(m.writeBundle)
	if err != nil {
		return coreptrsync.Status{}, err
	}
	if err := writePTRSyncOptInMarker(m.writeBundle); err != nil {
		return coreptrsync.Status{}, err
	}

	m.setEnabledFlag(true)
	m.clearUnavailableReason()
	if m.logger != nil {
		m.logger.Info("persisted PTR manual opt-in marker", "marker_path", markerPath)
	}

	return recoveredStatus, nil
}

// AddPendingMappings stages add-only pending PTR mappings for the daemon-owned
// public tag repository.
func (m *Manager) AddPendingMappings(
	ctx context.Context,
	request coreptrsync.PendingMappingsRequest,
) (coreptrsync.PendingMappingsResult, error) {
	if m == nil {
		return coreptrsync.PendingMappingsResult{}, coreptrsync.ErrCommitPendingUnavailable
	}

	cfg, unavailableReason := m.snapshotState()
	if !cfg.Enabled {
		return coreptrsync.PendingMappingsResult{}, coreptrsync.ErrSyncDisabled
	}

	if unavailableReason != "" || m.writeBundle == nil {
		return coreptrsync.PendingMappingsResult{}, coreptrsync.ErrCommitPendingUnavailable
	}

	return m.writeBundle.StagePTRPendingMappings(ctx, cfg, request)
}

// CommitPending uploads currently pending PTR add mappings and, on success,
// applies the local pending->current transition.
func (m *Manager) CommitPending(
	ctx context.Context,
	request coreptrsync.CommitPendingRequest,
) (coreptrsync.CommitPendingResult, error) {
	if m == nil {
		return coreptrsync.CommitPendingResult{}, coreptrsync.ErrCommitPendingUnavailable
	}

	cfg, unavailableReason := m.snapshotState()
	if !cfg.Enabled {
		return coreptrsync.CommitPendingResult{}, coreptrsync.ErrSyncDisabled
	}

	if unavailableReason != "" || m.readBundle == nil || m.writeBundle == nil {
		return coreptrsync.CommitPendingResult{}, coreptrsync.ErrCommitPendingUnavailable
	}

	groups, err := m.readBundle.ListPTRPendingMappingsForCommit(ctx, cfg, request.ServiceKey)
	if err != nil {
		return coreptrsync.CommitPendingResult{}, err
	}
	if len(groups) == 0 {
		return coreptrsync.CommitPendingResult{ServiceKey: repositoryServiceKeyHex(request.ServiceKey)}, nil
	}

	client, err := NewClient(cfg)
	if err != nil {
		return coreptrsync.CommitPendingResult{}, fmt.Errorf("construct PTR client: %w", err)
	}

	if err := client.CommitPendingMappings(ctx, groups); err != nil {
		return coreptrsync.CommitPendingResult{}, fmt.Errorf("commit PTR pending mappings: %w", err)
	}

	result, err := m.writeBundle.CommitPTRPendingMappingsSuccess(ctx, cfg, request.ServiceKey)
	if err != nil {
		return coreptrsync.CommitPendingResult{}, err
	}

	return result, nil
}

// PendingMappingCount returns the locally staged pending mapping count for the
// requested PTR service.
func (m *Manager) PendingMappingCount(
	ctx context.Context,
	request coreptrsync.PendingCountRequest,
) (coreptrsync.PendingInfo, error) {
	if m == nil {
		return coreptrsync.PendingInfo{}, coreptrsync.ErrCommitPendingUnavailable
	}

	cfg, unavailableReason := m.snapshotState()
	if !cfg.Enabled {
		return coreptrsync.PendingInfo{PendingCount: 0}, nil
	}

	if unavailableReason != "" || m.readBundle == nil {
		return coreptrsync.PendingInfo{}, coreptrsync.ErrCommitPendingUnavailable
	}

	return m.readBundle.CountPTRPendingMappings(ctx, request.ServiceKey)
}

// Shutdown stops any in-flight daemon-owned background PTR sync and waits for it
// to release its lease before returning.
func (m *Manager) Shutdown(ctx context.Context) error {
	if m == nil {
		return nil
	}

	if m.runnerCancel != nil {
		m.runnerCancel()
	}
	m.cancelPTRWakeup()

	m.runMu.Lock()
	active := m.activeRun
	m.runMu.Unlock()

	if active == nil {
		return nil
	}

	select {
	case <-active.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// SyncOnce runs one real remote PTR snapshot fetch and, once it has acquired a
// runtime lease, persists either the successful remote state or a daemon-visible
// failure status. The returned status is the terminal persisted daemon-visible
// status for this attempt, so callers may receive err != nil while the returned
// status is already back to idle with LastError populated. Failures before
// lease acquisition return the current persisted status without recording a new
// failure state.
func (m *Manager) SyncOnce(ctx context.Context) (status coreptrsync.Status, err error) {
	started, status, err := m.beginSync(ctx)
	if err != nil {
		return status, err
	}

	if started.client == nil {
		return status, nil
	}
	m.cancelPTRWakeup()

	return m.runStartedSync(ctx, started)
}

// Status returns the daemon-visible PTR sync status for HTTP/UI polling.
func (m *Manager) Status(ctx context.Context) (coreptrsync.Status, error) {
	if m == nil {
		return coreptrsync.Status{Phase: coreptrsync.PhaseUnavailable}, nil
	}

	cfg, unavailableReason := m.snapshotState()
	if !cfg.Enabled {
		return disabledStatus(cfg), nil
	}

	if unavailableReason != "" {
		status := disabledStatus(cfg)
		status.Enabled = cfg.Enabled
		status.Phase = coreptrsync.PhaseUnavailable
		status.UnavailableReason = unavailableReason
		return status, nil
	}

	if m.readBundle == nil {
		status := disabledStatus(m.cfg)
		status.Phase = coreptrsync.PhaseUnavailable
		status.UnavailableReason = "PTR sync requires a readable Hydrus DB bundle"
		return status, nil
	}

	startedAt := time.Now()
	status, err := m.readBundle.GetPTRSyncStatus(ctx, cfg)
	if err != nil {
		if m.logger != nil {
			m.logger.Warn(
				"PTR status lookup failed",
				"error",
				err,
				"duration_ms",
				time.Since(startedAt).Milliseconds(),
			)
		}

		return coreptrsync.Status{}, err
	}

	m.runMu.Lock()
	active := m.activeRun
	if active != nil && status.IsRunning {
		status.CurrentRunNetworkFetchedBytes = active.networkFetchedBytes
		status.CurrentRunNetworkFetchMS = active.networkFetchMS
		status.CurrentRunNetworkBytesPerSecond = active.networkBytesPerSecond
	}
	m.runMu.Unlock()

	if m.logger != nil && (status.IsRunning || status.LastError != "") {
		m.logger.Info(
			"loaded PTR status",
			"phase",
			status.Phase,
			"running",
			status.IsRunning,
			"metadata_slice",
			status.MetadataSlice,
			"stored_update_files_total",
			status.DownloadedUpdateCount,
			"processed_definitions",
			status.ProcessedDefinitionCount,
			"processed_content",
			status.ProcessedContentCount,
			"last_error",
			status.LastError,
			"duration_ms",
			time.Since(startedAt).Milliseconds(),
		)
	}

	return status, nil
}

func disabledStatus(cfg coreptrsync.Config) coreptrsync.Status {
	status := coreptrsync.Status{
		Enabled:     cfg.Enabled,
		Configured:  false,
		ServiceName: cfg.ServiceName,
		Host:        cfg.Host,
		Port:        cfg.Port,
		AccountMode: coreptrsync.AccountModeSharedReadOnly,
		Phase:       coreptrsync.PhaseDisabled,
		IsRunning:   false,
	}

	if cfg.Enabled {
		status.Phase = coreptrsync.PhaseIdle
	}

	return status
}

func (m *Manager) beginSync(ctx context.Context) (startedSync, coreptrsync.Status, error) {
	if m == nil {
		return startedSync{}, coreptrsync.Status{Phase: coreptrsync.PhaseUnavailable}, coreptrsync.ErrSyncUnavailable
	}

	cfg, unavailableReason := m.snapshotState()
	if !cfg.Enabled {
		return startedSync{}, disabledStatus(cfg), coreptrsync.ErrSyncDisabled
	}

	if unavailableReason != "" {
		status := disabledStatus(cfg)
		status.Enabled = cfg.Enabled
		status.Phase = coreptrsync.PhaseUnavailable
		status.UnavailableReason = unavailableReason
		return startedSync{}, status, fmt.Errorf("%w: %s", coreptrsync.ErrSyncUnavailable, unavailableReason)
	}

	if m.readBundle == nil || m.writeBundle == nil {
		status := disabledStatus(cfg)
		status.Enabled = cfg.Enabled
		status.Phase = coreptrsync.PhaseUnavailable
		status.UnavailableReason = "PTR sync requires readable and writable Hydrus DB bundles"
		return startedSync{}, status, fmt.Errorf("%w: %s", coreptrsync.ErrSyncUnavailable, status.UnavailableReason)
	}

	status, err := m.readBundle.GetPTRSyncStatus(ctx, cfg)
	if err != nil {
		return startedSync{}, coreptrsync.Status{}, fmt.Errorf("load PTR status before sync: %w", err)
	}

	if status.Phase == coreptrsync.PhaseRetrying && status.RetryAtMS > time.Now().UTC().UnixMilli() {
		return startedSync{}, status, nil
	}

	client, err := NewClient(cfg)
	if err != nil {
		status, statusErr := m.readBundle.GetPTRSyncStatus(ctx, cfg)
		if statusErr != nil {
			return startedSync{}, coreptrsync.Status{}, errors.Join(
				fmt.Errorf("construct PTR client: %w", err),
				fmt.Errorf("load PTR status: %w", statusErr),
			)
		}

		return startedSync{}, status, fmt.Errorf("construct PTR client: %w", err)
	}

	lease, err := m.writeBundle.BeginPTRSync(ctx, cfg)
	if err != nil {
		status, statusErr := m.readBundle.GetPTRSyncStatus(ctx, cfg)
		if statusErr != nil {
			return startedSync{}, coreptrsync.Status{}, errors.Join(
				err,
				fmt.Errorf("load PTR status after begin failure: %w", statusErr),
			)
		}

		return startedSync{}, status, err
	}

	return startedSync{
		client:          client,
		lease:           lease,
		replaceMetadata: lease.Status.MetadataSlice == 0,
	}, lease.Status, nil
}

func (m *Manager) runStartedSync(ctx context.Context, started startedSync) (status coreptrsync.Status, err error) {
	terminalStatePersisted := false
	if m.logger != nil {
		m.logger.Info(
			"starting PTR sync run",
			"metadata_slice",
			started.lease.Status.MetadataSlice,
			"replace_metadata",
			started.replaceMetadata,
			"run_token",
			started.lease.RunToken,
		)
	}

	defer func() {
		if terminalStatePersisted {
			if m.logger != nil {
				if status.Phase == coreptrsync.PhaseRetrying {
					m.logger.Info(
						"PTR sync run deferred after remote busy response",
						"run_token",
						started.lease.RunToken,
						"retry_at_ms",
						status.RetryAtMS,
						"retry_attempt",
						status.RetryAttempt,
					)
				} else {
					m.logger.Info(
						"PTR sync run completed successfully",
						"run_token",
						started.lease.RunToken,
						"metadata_slice",
						status.MetadataSlice,
						"stored_update_files_total",
						status.DownloadedUpdateCount,
						"processed_definitions",
						status.ProcessedDefinitionCount,
						"processed_content",
						status.ProcessedContentCount,
					)
				}
			}

			return
		}

		if errors.Is(err, context.Canceled) {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			cancelledStatus, cancelErr := m.writeBundle.CancelPTRSync(
				cleanupCtx,
				m.cfg,
				started.lease.RunToken,
			)
			if cancelErr != nil {
				err = errors.Join(err, fmt.Errorf("clear PTR sync lease after cancellation: %w", cancelErr))
				return
			}

			status = cancelledStatus
			err = nil
			if m.logger != nil {
				m.logger.Info(
					"PTR sync run cancelled",
					"run_token",
					started.lease.RunToken,
					"metadata_slice",
					cancelledStatus.MetadataSlice,
					"stored_update_files_total",
					cancelledStatus.DownloadedUpdateCount,
				)
			}

			return
		}

		failureReason := "PTR sync failed"
		if err != nil {
			failureReason = err.Error()
		}

		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		failedStatus, finishErr := m.writeBundle.FinishPTRSyncFailure(
			cleanupCtx,
			m.cfg,
			started.lease.RunToken,
			failureReason,
		)
		if finishErr != nil {
			err = errors.Join(err, fmt.Errorf("persist PTR sync failure: %w", finishErr))
			return
		}

		status = failedStatus
		if m.logger != nil {
			m.logger.Warn(
				"PTR sync run failed",
				"run_token",
				started.lease.RunToken,
				"error",
				err,
				"failure_reason",
				failureReason,
				"metadata_slice",
				failedStatus.MetadataSlice,
				"stored_update_files_total",
				failedStatus.DownloadedUpdateCount,
			)
		}
	}()

	remoteState, err := started.client.FetchRemoteState(ctx, started.lease.Status.MetadataSlice)
	if err != nil {
		if busyStatus, handled, handleErr := m.finishPTRSyncThrottled(ctx, started, status, err); handled {
			status = busyStatus
			err = handleErr
			terminalStatePersisted = handleErr == nil
			return status, err
		}

		err = fmt.Errorf("fetch PTR remote state: %w", err)
		return status, err
	}

	status, err = m.writeBundle.PersistPTRSyncMetadata(
		ctx,
		m.cfg,
		started.lease.RunToken,
		remoteState,
		started.replaceMetadata,
	)
	if err != nil {
		err = fmt.Errorf("persist PTR sync metadata: %w", err)
		return status, err
	}

	if m.logger != nil {
		m.logger.Info(
			"persisted PTR remote metadata",
			"run_token",
			started.lease.RunToken,
			"metadata_slice",
			status.MetadataSlice,
			"stored_update_files_total",
			status.DownloadedUpdateCount,
		)
	}

	if status.IsRunning {
		pendingHashes, listErr := m.writeBundle.ListPTRPendingUpdateHashes(ctx, m.cfg, started.lease.RunToken)
		if listErr != nil {
			err = fmt.Errorf("list PTR pending update hashes: %w", listErr)
			return status, err
		}

		if m.logger != nil {
			m.logger.Info(
				"loaded PTR pending update hashes",
				"run_token",
				started.lease.RunToken,
				"pending_count",
				len(pendingHashes),
			)
		}

		currentRunDownloads := 0
		currentRunDownloadedBytes := int64(0)
		currentRunDownloadMS := int64(0)
		currentRunNetworkFetchMS := int64(0)
		var currentRunDownloadStartedAt time.Time
		batch := make([]downloadedPTRUpdateBatchItem, 0, minInt(ptrSyncDownloadedUpdateBatchSize, len(pendingHashes)))
		flushBatch := func() error {
			if len(batch) == 0 {
				return nil
			}

			batchItems := make([]hydrusdb.PTRDownloadedUpdateBatchItem, 0, len(batch))
			for _, item := range batch {
				batchItems = append(batchItems, hydrusdb.PTRDownloadedUpdateBatchItem{
					HashHex:        item.hashHex,
					Body:           item.body,
					PreparedImport: item.preparedImport,
				})
			}

			updatedStatus, batchErr := m.writeBundle.FinalizePTRDownloadedUpdatesBatch(
				ctx,
				m.cfg,
				started.lease.RunToken,
				batchItems,
			)
			if batchErr != nil {
				return batchErr
			}

			status = updatedStatus
			if currentRunDownloadedBytes > 0 || currentRunDownloadMS > 0 {
				status, batchErr = m.writeBundle.UpdatePTRSyncDownloadMetrics(
					ctx,
					m.cfg,
					started.lease.RunToken,
					currentRunDownloadedBytes,
					currentRunDownloadMS,
				)
				if batchErr != nil {
					return batchErr
				}
			}

			if batchErr := m.applyDownloadedPTRUpdates(ctx, started.lease.RunToken); batchErr != nil {
				return batchErr
			}

			currentRunDownloads += len(batch)
			if m.logger != nil {
				latestHash := batch[len(batch)-1].hashHex
				m.logger.Info(
					"PTR update download progress",
					"run_token",
					started.lease.RunToken,
					"current_run_downloaded",
					currentRunDownloads,
					"current_run_pending_total",
					len(pendingHashes),
					"latest_hash",
					latestHash,
					"current_run_downloaded_bytes",
					status.CurrentRunDownloadedBytes,
					"current_run_download_ms",
					status.CurrentRunDownloadMS,
					"current_run_bytes_per_second",
					status.CurrentRunBytesPerSecond,
					"stored_update_files_total",
					status.DownloadedUpdateCount,
					"stored_update_bytes_total",
					status.DownloadedUpdateBytes,
				)
			}

			batch = batch[:0]
			return nil
		}

		for startIndex := 0; startIndex < len(pendingHashes); startIndex += ptrSyncDownloadParallelism {
			endIndex := startIndex + ptrSyncDownloadParallelism
			if endIndex > len(pendingHashes) {
				endIndex = len(pendingHashes)
			}

			missingHashes := make([][]byte, 0, endIndex-startIndex)
			for _, pendingHash := range pendingHashes[startIndex:endIndex] {
				body, mimeEnum, ok, artifactErr := loadStoredPTRUpdateArtifact(m.writeBundle, pendingHash)
				if artifactErr != nil {
					err = fmt.Errorf("load stored PTR update artifact %x: %w", pendingHash, artifactErr)
					return status, err
				}

				if ok {
					hashHex := hex.EncodeToString(pendingHash)
					batch = append(batch, downloadedPTRUpdateBatchItem{
						hashHex: hashHex,
						body:    body,
						preparedImport: hydrusdb.PreparedLocalImport{
							HashHex:             hashHex,
							Size:                int64(len(body)),
							Mime:                mimeEnum,
							ImportedAtMS:        time.Now().UTC().UnixMilli(),
							LocalFileServiceKey: repositoryUpdatesServiceKeyHex(),
						},
					})
				} else {
					missingHashes = append(missingHashes, append([]byte(nil), pendingHash...))
				}
			}

			if len(missingHashes) > 0 && currentRunDownloadStartedAt.IsZero() {
				currentRunDownloadStartedAt = time.Now().UTC()
			}

			fetchStartedAt := time.Now().UTC()
			fetched, fetchErr := fetchPTRUpdatesInParallel(ctx, started.client, missingHashes)
			currentRunNetworkFetchMS += time.Since(fetchStartedAt).Milliseconds()
			if len(fetched) > 0 {
				for _, result := range fetched {
					hashHex := hex.EncodeToString(result.pendingHash)
					currentRunDownloadedBytes += int64(len(result.body))
					batch = append(batch, downloadedPTRUpdateBatchItem{
						hashHex: hashHex,
						body:    result.body,
						preparedImport: hydrusdb.PreparedLocalImport{
							HashHex:             hashHex,
							Size:                int64(len(result.body)),
							Mime:                result.mimeEnum,
							ImportedAtMS:        time.Now().UTC().UnixMilli(),
							LocalFileServiceKey: repositoryUpdatesServiceKeyHex(),
						},
					})
				}
				currentRunDownloadMS = time.Since(currentRunDownloadStartedAt).Milliseconds()
				if currentRunDownloadMS <= 0 {
					currentRunDownloadMS = 1
				}
			}
			m.updateActiveRunNetworkMetrics(started.lease.RunToken, currentRunDownloadedBytes, currentRunNetworkFetchMS)

			if len(batch) >= ptrSyncDownloadedUpdateBatchSize {
				if flushErr := flushBatch(); flushErr != nil {
					err = fmt.Errorf("flush PTR downloaded update batch: %w", flushErr)
					return status, err
				}
			}

			if fetchErr != nil {
				if flushErr := flushBatch(); flushErr != nil {
					err = fmt.Errorf("flush PTR downloaded update batch: %w", flushErr)
					return status, err
				}

				if busyStatus, handled, handleErr := m.finishPTRSyncThrottled(ctx, started, status, fetchErr); handled {
					status = busyStatus
					err = handleErr
					terminalStatePersisted = handleErr == nil
					return status, err
				}

				err = fmt.Errorf("fetch PTR update batch starting at %d: %w", startIndex, fetchErr)
				return status, err
			}
		}

		if flushErr := flushBatch(); flushErr != nil {
			err = fmt.Errorf("flush PTR downloaded update batch: %w", flushErr)
			return status, err
		}
	}

	if applyErr := m.applyDownloadedPTRUpdates(ctx, started.lease.RunToken); applyErr != nil {
		err = fmt.Errorf("apply PTR downloaded updates: %w", applyErr)
		return status, err
	}

	status, err = m.writeBundle.GetPTRSyncStatus(ctx, m.cfg)
	if err != nil {
		err = fmt.Errorf("reload PTR status after apply: %w", err)
		return status, err
	}

	status, err = m.writeBundle.CompletePTRSyncSuccess(
		ctx,
		m.cfg,
		started.lease.RunToken,
	)
	if err != nil {
		err = fmt.Errorf("complete PTR sync success: %w", err)
		return status, err
	}

	terminalStatePersisted = true
	return status, nil
}

func (m *Manager) applyDownloadedPTRUpdates(ctx context.Context, runToken string) error {
	processable, err := m.writeBundle.ListPTRProcessableUpdates(ctx, m.cfg, runToken)
	if err != nil {
		return err
	}

	decodedItems := make([]hydrusdb.PTRApplyUpdateBatchItem, 0, len(processable))
	for _, item := range processable {
		if item.Processed {
			continue
		}
		if len(item.Body) == 0 {
			return fmt.Errorf("PTR update artifact %s is not available in sqlite storage", item.HashHex)
		}

		switch item.ContentType {
		case hydrusdb.PTRContentTypeDefinitions:
			decoded, err := decodeDefinitionsUpdatePayload(item.Body)
			if err != nil {
				return fmt.Errorf("decode PTR definitions update %s: %w", item.HashHex, err)
			}
			decodedItems = append(decodedItems, hydrusdb.PTRApplyUpdateBatchItem{
				HashHex:     item.HashHex,
				ContentType: item.ContentType,
				Definitions: decoded,
			})
		case hydrusdb.PTRContentTypeMappings:
			decoded, err := decodeMappingsUpdatePayload(item.Body)
			if err != nil {
				return fmt.Errorf("decode PTR mappings update %s: %w", item.HashHex, err)
			}
			decodedItems = append(decodedItems, hydrusdb.PTRApplyUpdateBatchItem{
				HashHex:     item.HashHex,
				ContentType: item.ContentType,
				Mappings:    decoded,
			})
		}
	}

	return m.writeBundle.ApplyPTRProcessableUpdatesBatch(ctx, m.cfg, runToken, decodedItems)
}

func (m *Manager) finishPTRSyncThrottled(
	ctx context.Context,
	started startedSync,
	status coreptrsync.Status,
	runErr error,
) (coreptrsync.Status, bool, error) {
	busyDelay, busy := ptrBusyRetryAfter(runErr)
	transientTransport := ptrTransientTransport(runErr)
	if !busy && !transientTransport {
		return coreptrsync.Status{}, false, nil
	}

	retryAttempt := maxInt64(started.lease.Status.RetryAttempt, status.RetryAttempt) + 1
	if retryAttempt > ptrSyncMaxBusyRetryAttempts {
		failureReason := fmt.Sprintf(
			"PTR server issue: remote stayed busy after %d retries; please try again later",
			retryAttempt-1,
		)
		failedStatus, err := m.writeBundle.FinishPTRSyncFailure(
			ctx,
			m.cfg,
			started.lease.RunToken,
			failureReason,
		)
		if err != nil {
			return coreptrsync.Status{}, true, fmt.Errorf("persist PTR server issue state: %w", err)
		}

		return failedStatus, true, fmt.Errorf(failureReason)
	}

	delay := ptrSyncBusyPause
	if busyDelay > delay {
		delay = busyDelay
	}
	retryAtMS := time.Now().UTC().Add(delay).UnixMilli()

	retryingStatus, err := m.writeBundle.SetPTRSyncThrottled(
		ctx,
		m.cfg,
		started.lease.RunToken,
		retryAtMS,
		retryAttempt,
	)
	if err != nil {
		return coreptrsync.Status{}, true, fmt.Errorf("persist PTR sync retry state: %w", err)
	}
	m.schedulePTRRetryWakeup(retryingStatus.RetryAtMS)

	return retryingStatus, true, nil
}

func (m *Manager) cancelPTRWakeup() {
	if m == nil {
		return
	}

	m.wakeupMu.Lock()
	defer m.wakeupMu.Unlock()

	if m.wakeupCancel != nil {
		m.wakeupCancel()
		m.wakeupCancel = nil
	}
}

func (m *Manager) schedulePTRRetryWakeup(retryAtMS int64) {
	m.schedulePTRWakeup(retryAtMS, "retry")
}

func (m *Manager) schedulePTRNextUpdateWakeup(nextUpdateDue int64) {
	if nextUpdateDue <= 0 {
		return
	}

	m.schedulePTRWakeup(time.Unix(nextUpdateDue, 0).UTC().UnixMilli(), "next_update_due")
}

func (m *Manager) schedulePTRWakeup(wakeupAtMS int64, reason string) {
	if m == nil || wakeupAtMS <= 0 {
		return
	}

	m.wakeupMu.Lock()
	if m.wakeupCancel != nil {
		m.wakeupCancel()
	}
	ctx, cancel := context.WithCancel(m.runnerCtx)
	m.wakeupID++
	wakeupID := m.wakeupID
	m.wakeupCancel = cancel
	m.wakeupMu.Unlock()

	go func(wakeupAtMS int64, wakeCtx context.Context, wakeupID uint64, reason string) {
		defer func() {
			m.wakeupMu.Lock()
			if m.wakeupID == wakeupID {
				m.wakeupCancel = nil
			}
			m.wakeupMu.Unlock()
		}()

		delay := time.Until(time.UnixMilli(wakeupAtMS).UTC())
		if delay > 0 {
			timer := time.NewTimer(delay)
			defer timer.Stop()

			select {
			case <-wakeCtx.Done():
				return
			case <-timer.C:
			}
		} else {
			select {
			case <-wakeCtx.Done():
				return
			default:
			}
		}

		status, err := m.Trigger(context.Background())
		if err != nil {
			if m.logger != nil {
				m.logger.Warn("PTR scheduled wakeup trigger failed", "reason", reason, "error", err)
			}
			return
		}

		if m.logger != nil {
			m.logger.Info(
				"PTR scheduled wakeup triggered sync",
				"reason",
				reason,
				"phase",
				status.Phase,
				"wakeup_at_ms",
				wakeupAtMS,
			)
		}
	}(wakeupAtMS, ctx, wakeupID, reason)
}

func shouldContinuePTRSync(nextUpdateDue int64, previousSlice int64, currentSlice int64, now time.Time) bool {
	if nextUpdateDue <= 0 {
		return false
	}

	if time.Unix(nextUpdateDue, 0).UTC().After(now) {
		return false
	}

	return currentSlice > previousSlice
}

func shouldSchedulePTRNextUpdateWakeup(nextUpdateDue int64, now time.Time) bool {
	if nextUpdateDue <= 0 {
		return false
	}

	return time.Unix(nextUpdateDue, 0).UTC().After(now)
}

func ptrSyncThrottleDelay(retryAttempt int64) time.Duration {
	if retryAttempt < 1 {
		retryAttempt = 1
	}

	if retryAttempt <= int64(len(ptrSyncInitialBusyRetryDelays)) {
		return ptrSyncInitialBusyRetryDelays[retryAttempt-1]
	}

	delay := ptrSyncInitialBusyRetryDelays[len(ptrSyncInitialBusyRetryDelays)-1]
	for step := int64(len(ptrSyncInitialBusyRetryDelays)); step < retryAttempt && delay < ptrSyncThrottleMaxDelay; step++ {
		delay *= 2
		if delay >= ptrSyncThrottleMaxDelay {
			return ptrSyncThrottleMaxDelay
		}
	}

	return delay
}

func minInt(left int, right int) int {
	if left < right {
		return left
	}

	return right
}

func fetchPTRUpdatesInParallel(ctx context.Context, client *Client, pendingHashes [][]byte) ([]fetchedPTRUpdateResult, error) {
	if client == nil {
		return nil, fmt.Errorf("PTR client is required")
	}

	if len(pendingHashes) == 0 {
		return nil, nil
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	resultsCh := make(chan fetchedPTRUpdateResult, len(pendingHashes))
	var wg sync.WaitGroup
	for _, pendingHash := range pendingHashes {
		hashCopy := append([]byte(nil), pendingHash...)
		wg.Add(1)
		go func(hash []byte) {
			defer wg.Done()
			body, mimeEnum, err := client.FetchUpdate(ctx, hash)
			resultsCh <- fetchedPTRUpdateResult{
				pendingHash: hash,
				body:        body,
				mimeEnum:    mimeEnum,
				err:         err,
			}
		}(hashCopy)
	}

	go func() {
		wg.Wait()
		close(resultsCh)
	}()

	results := make([]fetchedPTRUpdateResult, 0, len(pendingHashes))
	var firstErr error
	for result := range resultsCh {
		if result.err != nil {
			if firstErr == nil {
				firstErr = result.err
				cancel()
			}
			continue
		}

		results = append(results, result)
	}

	return results, firstErr
}

func maxInt64(left int64, right int64) int64 {
	if left > right {
		return left
	}

	return right
}

func ptrSyncOptInMarkerPath(bundle *hydrusdb.Bundle) (string, error) {
	if bundle == nil {
		return "", fmt.Errorf("hydrus bundle is required")
	}

	mainDBPath := strings.TrimSpace(bundle.MainDBPath())
	if mainDBPath == "" {
		return "", fmt.Errorf("hydrus bundle main DB path is required")
	}

	return filepath.Join(filepath.Dir(mainDBPath), ptrSyncOptInMarkerName), nil
}

func ptrSyncOptInMarkerExists(bundle *hydrusdb.Bundle) (bool, error) {
	markerPath, err := ptrSyncOptInMarkerPath(bundle)
	if err != nil {
		return false, err
	}

	info, err := os.Stat(markerPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}

		return false, fmt.Errorf("stat PTR sync opt-in marker: %w", err)
	}
	if info.IsDir() {
		return false, fmt.Errorf("PTR sync opt-in marker path %q is a directory", markerPath)
	}

	return true, nil
}

func writePTRSyncOptInMarker(bundle *hydrusdb.Bundle) error {
	markerPath, err := ptrSyncOptInMarkerPath(bundle)
	if err != nil {
		return err
	}

	if err := os.WriteFile(markerPath, []byte("true\n"), 0o644); err != nil {
		return fmt.Errorf("write PTR sync opt-in marker: %w", err)
	}

	return nil
}

func repositoryUpdatesServiceKeyHex() string {
	return hex.EncodeToString([]byte("repository updates"))
}

func repositoryServiceKeyHex(serviceKey string) string {
	normalized := strings.ToLower(strings.TrimSpace(serviceKey))
	if normalized == "" {
		return coreptrsync.DaemonServiceKeyHex()
	}

	return normalized
}

func resolvePTRUpdateArtifactPath(bundle *hydrusdb.Bundle, hashHex string) (string, error) {
	if bundle == nil {
		return "", fmt.Errorf("hydrus bundle is required")
	}

	normalizedHash, err := clientfiles.NormalizeSHA256Hex(hashHex)
	if err != nil {
		return "", err
	}

	layout, err := bundle.ManagedLayout(context.Background())
	if err != nil {
		return "", fmt.Errorf("load managed layout for PTR update artifact: %w", err)
	}

	artifactPath, err := layout.ResolveFilePath(normalizedHash, "")
	if err != nil {
		return "", fmt.Errorf("resolve managed PTR update artifact path: %w", err)
	}

	return artifactPath, nil
}

func resolveLegacyPTRUpdateArtifactPath(bundle *hydrusdb.Bundle, hashHex string) (string, error) {
	if bundle == nil {
		return "", fmt.Errorf("hydrus bundle is required")
	}

	normalizedHash, err := clientfiles.NormalizeSHA256Hex(hashHex)
	if err != nil {
		return "", err
	}

	return filepath.Join(
		filepath.Dir(bundle.MainDBPath()),
		"repository_updates",
		normalizedHash[:2],
		normalizedHash,
	), nil
}

func storePTRUpdateArtifact(
	bundle *hydrusdb.Bundle,
	hashHex string,
	body []byte,
) (string, bool, error) {
	if len(body) == 0 {
		return "", false, fmt.Errorf("PTR update artifact body is required")
	}

	artifactPath, err := resolvePTRUpdateArtifactPath(bundle, hashHex)
	if err != nil {
		return "", false, err
	}

	parentDir := filepath.Dir(artifactPath)
	if err := os.MkdirAll(parentDir, 0o755); err != nil {
		return "", false, fmt.Errorf("create PTR update artifact directory: %w", err)
	}

	tempFile, err := os.CreateTemp(parentDir, hashHex+".*.tmp")
	if err != nil {
		return "", false, fmt.Errorf("create PTR update artifact temp file: %w", err)
	}
	tempPath := tempFile.Name()
	defer os.Remove(tempPath)

	if _, err := tempFile.Write(body); err != nil {
		_ = tempFile.Close()
		return "", false, fmt.Errorf("write PTR update artifact temp file: %w", err)
	}

	if err := tempFile.Close(); err != nil {
		return "", false, fmt.Errorf("close PTR update artifact temp file: %w", err)
	}

	if err := os.Link(tempPath, artifactPath); err != nil {
		if !os.IsExist(err) {
			return "", false, fmt.Errorf("link PTR update artifact into place: %w", err)
		}

		existingBytes, readErr := os.ReadFile(artifactPath)
		if readErr != nil {
			return "", false, fmt.Errorf("read existing PTR update artifact: %w", readErr)
		}

		if !bytes.Equal(existingBytes, body) {
			return "", false, fmt.Errorf(
				"PTR update artifact conflict at %q",
				artifactPath,
			)
		}

		return artifactPath, true, nil
	}

	return artifactPath, false, nil
}

func loadStoredPTRUpdateArtifact(bundle *hydrusdb.Bundle, updateHash []byte) ([]byte, int, bool, error) {
	if bundle == nil {
		return nil, 0, false, fmt.Errorf("hydrus bundle is required")
	}

	if len(updateHash) == 0 {
		return nil, 0, false, fmt.Errorf("PTR update hash is required")
	}

	hashHex := hex.EncodeToString(updateHash)
	body, mime, ok, err := bundle.LoadPTRStoredUpdateBody(context.Background(), hashHex)
	if err != nil {
		return nil, 0, false, err
	}
	if ok {
		return body, mime, true, nil
	}

	artifactPath, err := resolvePTRUpdateArtifactPath(bundle, hashHex)
	if err != nil {
		return nil, 0, false, err
	}

	body, mime, ok, err = readPTRUpdateArtifactFile(artifactPath, updateHash)
	if err != nil {
		return nil, 0, false, err
	}
	if ok {
		return body, mime, true, nil
	}

	legacyPath, err := resolveLegacyPTRUpdateArtifactPath(bundle, hashHex)
	if err != nil {
		return nil, 0, false, err
	}

	body, mime, ok, err = readPTRUpdateArtifactFile(legacyPath, updateHash)
	if err != nil {
		return nil, 0, false, err
	}
	if !ok {
		return nil, 0, false, nil
	}

	return body, mime, true, nil
}

func readPTRUpdateArtifactFile(path string, updateHash []byte) ([]byte, int, bool, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, false, nil
		}

		return nil, 0, false, fmt.Errorf("read artifact file: %w", err)
	}

	sum := sha256.Sum256(body)
	if !equalBytes(sum[:], updateHash) {
		return nil, 0, false, fmt.Errorf("artifact hash %x did not match expected %x", sum[:], updateHash)
	}

	mime, err := classifyUpdatePayload(body)
	if err != nil {
		return nil, 0, false, fmt.Errorf("classify stored update payload: %w", err)
	}

	return body, mime, true, nil
}
