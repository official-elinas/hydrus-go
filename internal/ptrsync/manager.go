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
	cfg               coreptrsync.Config
	readBundle        *hydrusdb.Bundle
	writeBundle       *hydrusdb.Bundle
	unavailableReason string
	runnerCtx         context.Context
	runnerCancel      context.CancelFunc
	runMu             sync.Mutex
	activeRun         *activeRun
	retryWakeupMu     sync.Mutex
	retryWakeupID     uint64
	retryWakeupCancel context.CancelFunc
}

type activeRun struct {
	cancel context.CancelFunc
	done   chan struct{}
	status coreptrsync.Status
}

type startedSync struct {
	client          *Client
	lease           hydrusdb.PTRSyncLease
	replaceMetadata bool
}

type downloadedPTRUpdateBatchItem struct {
	hashHex        string
	preparedImport hydrusdb.PreparedLocalImport
}

const (
	ptrSyncDownloadedUpdateBatchSize = 25
	ptrSyncBusyPause                 = 10 * time.Minute
	ptrSyncThrottleMaxDelay          = 2 * time.Minute
	ptrSyncMaxBusyRetryAttempts      = 10
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

	if recoveredStatus.Phase == coreptrsync.PhaseRetrying {
		manager.schedulePTRRetryWakeup(recoveredStatus.RetryAtMS)
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

	runCtx, cancel := context.WithCancel(m.runnerCtx)
	done := make(chan struct{})
	m.activeRun = &activeRun{cancel: cancel, done: done, status: status}

	go func(started startedSync, done chan struct{}) {
		defer close(done)
		defer cancel()

		status, err := m.runStartedSync(runCtx, started)
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
	}(started, done)

	return status, nil
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

	if !m.cfg.Enabled {
		return coreptrsync.PendingMappingsResult{}, coreptrsync.ErrSyncDisabled
	}

	if m.unavailableReason != "" || m.writeBundle == nil {
		return coreptrsync.PendingMappingsResult{}, coreptrsync.ErrCommitPendingUnavailable
	}

	return m.writeBundle.StagePTRPendingMappings(ctx, m.cfg, request)
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

	if !m.cfg.Enabled {
		return coreptrsync.CommitPendingResult{}, coreptrsync.ErrSyncDisabled
	}

	if m.unavailableReason != "" || m.readBundle == nil || m.writeBundle == nil {
		return coreptrsync.CommitPendingResult{}, coreptrsync.ErrCommitPendingUnavailable
	}

	groups, err := m.readBundle.ListPTRPendingMappingsForCommit(ctx, m.cfg, request.ServiceKey)
	if err != nil {
		return coreptrsync.CommitPendingResult{}, err
	}
	if len(groups) == 0 {
		return coreptrsync.CommitPendingResult{ServiceKey: repositoryServiceKeyHex(request.ServiceKey)}, nil
	}

	client, err := NewClient(m.cfg)
	if err != nil {
		return coreptrsync.CommitPendingResult{}, fmt.Errorf("construct PTR client: %w", err)
	}

	if err := client.CommitPendingMappings(ctx, groups); err != nil {
		return coreptrsync.CommitPendingResult{}, fmt.Errorf("commit PTR pending mappings: %w", err)
	}

	result, err := m.writeBundle.CommitPTRPendingMappingsSuccess(ctx, m.cfg, request.ServiceKey)
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

	if !m.cfg.Enabled {
		return coreptrsync.PendingInfo{}, coreptrsync.ErrSyncDisabled
	}

	if m.unavailableReason != "" || m.readBundle == nil {
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
	m.cancelPTRRetryWakeup()

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
	m.cancelPTRRetryWakeup()

	return m.runStartedSync(ctx, started)
}

// Status returns the daemon-visible PTR sync status for HTTP/UI polling.
func (m *Manager) Status(ctx context.Context) (coreptrsync.Status, error) {
	if m == nil {
		return coreptrsync.Status{Phase: coreptrsync.PhaseUnavailable}, nil
	}

	if !m.cfg.Enabled {
		return disabledStatus(m.cfg), nil
	}

	if m.unavailableReason != "" {
		status := disabledStatus(m.cfg)
		status.Enabled = m.cfg.Enabled
		status.Phase = coreptrsync.PhaseUnavailable
		status.UnavailableReason = m.unavailableReason
		return status, nil
	}

	if m.readBundle == nil {
		status := disabledStatus(m.cfg)
		status.Phase = coreptrsync.PhaseUnavailable
		status.UnavailableReason = "PTR sync requires a readable Hydrus DB bundle"
		return status, nil
	}

	startedAt := time.Now()
	status, err := m.readBundle.GetPTRSyncStatus(ctx, m.cfg)
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

	if !m.cfg.Enabled {
		return startedSync{}, disabledStatus(m.cfg), coreptrsync.ErrSyncDisabled
	}

	if m.unavailableReason != "" {
		status := disabledStatus(m.cfg)
		status.Enabled = m.cfg.Enabled
		status.Phase = coreptrsync.PhaseUnavailable
		status.UnavailableReason = m.unavailableReason
		return startedSync{}, status, fmt.Errorf("%w: %s", coreptrsync.ErrSyncUnavailable, m.unavailableReason)
	}

	if m.readBundle == nil || m.writeBundle == nil {
		status := disabledStatus(m.cfg)
		status.Enabled = m.cfg.Enabled
		status.Phase = coreptrsync.PhaseUnavailable
		status.UnavailableReason = "PTR sync requires readable and writable Hydrus DB bundles"
		return startedSync{}, status, fmt.Errorf("%w: %s", coreptrsync.ErrSyncUnavailable, status.UnavailableReason)
	}

	status, err := m.readBundle.GetPTRSyncStatus(ctx, m.cfg)
	if err != nil {
		return startedSync{}, coreptrsync.Status{}, fmt.Errorf("load PTR status before sync: %w", err)
	}

	if status.Phase == coreptrsync.PhaseRetrying && status.RetryAtMS > time.Now().UTC().UnixMilli() {
		return startedSync{}, status, nil
	}

	client, err := NewClient(m.cfg)
	if err != nil {
		status, statusErr := m.readBundle.GetPTRSyncStatus(ctx, m.cfg)
		if statusErr != nil {
			return startedSync{}, coreptrsync.Status{}, errors.Join(
				fmt.Errorf("construct PTR client: %w", err),
				fmt.Errorf("load PTR status: %w", statusErr),
			)
		}

		return startedSync{}, status, fmt.Errorf("construct PTR client: %w", err)
	}

	lease, err := m.writeBundle.BeginPTRSync(ctx, m.cfg)
	if err != nil {
		status, statusErr := m.readBundle.GetPTRSyncStatus(ctx, m.cfg)
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
		batch := make([]downloadedPTRUpdateBatchItem, 0, minInt(ptrSyncDownloadedUpdateBatchSize, len(pendingHashes)))
		flushBatch := func() error {
			if len(batch) == 0 {
				return nil
			}

			batchItems := make([]hydrusdb.PTRDownloadedUpdateBatchItem, 0, len(batch))
			for _, item := range batch {
				batchItems = append(batchItems, hydrusdb.PTRDownloadedUpdateBatchItem{
					HashHex:        item.hashHex,
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
					"stored_update_files_total",
					status.DownloadedUpdateCount,
				)
			}

			batch = batch[:0]
			return nil
		}

		for _, pendingHash := range pendingHashes {
			body, mimeEnum, ok, artifactErr := loadStoredPTRUpdateArtifact(m.writeBundle, pendingHash)
			if artifactErr != nil {
				err = fmt.Errorf("load stored PTR update artifact %x: %w", pendingHash, artifactErr)
				return status, err
			}

			if !ok {
				var fetchErr error
				body, mimeEnum, fetchErr = started.client.FetchUpdate(ctx, pendingHash)
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

					err = fmt.Errorf("fetch PTR update %x: %w", pendingHash, fetchErr)
					return status, err
				}
			}

			hashHex := hex.EncodeToString(pendingHash)
			if !ok {
				_, _, artifactErr = storePTRUpdateArtifact(
					m.writeBundle,
					hashHex,
					body,
				)
				if artifactErr != nil {
					err = fmt.Errorf("store PTR update artifact %x: %w", pendingHash, artifactErr)
					return status, err
				}
			}

			batch = append(batch, downloadedPTRUpdateBatchItem{
				hashHex: hashHex,
				preparedImport: hydrusdb.PreparedLocalImport{
					HashHex:             hashHex,
					Size:                int64(len(body)),
					Mime:                mimeEnum,
					ImportedAtMS:        time.Now().UTC().UnixMilli(),
					LocalFileServiceKey: repositoryUpdatesServiceKeyHex(),
				},
			})

			if len(batch) >= ptrSyncDownloadedUpdateBatchSize {
				if flushErr := flushBatch(); flushErr != nil {
					err = fmt.Errorf("flush PTR downloaded update batch: %w", flushErr)
					return status, err
				}
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

	for _, item := range processable {
		if item.Processed {
			continue
		}

		updateHash, err := hex.DecodeString(item.HashHex)
		if err != nil {
			return fmt.Errorf("decode PTR update hash %s: %w", item.HashHex, err)
		}

		body, _, ok, err := loadStoredPTRUpdateArtifact(m.writeBundle, updateHash)
		if err != nil {
			return fmt.Errorf("load PTR update artifact %s: %w", item.HashHex, err)
		}
		if !ok {
			return fmt.Errorf("PTR update artifact %s is not available in managed storage", item.HashHex)
		}

		switch item.ContentType {
		case hydrusdb.PTRContentTypeDefinitions:
			decoded, err := decodeDefinitionsUpdatePayload(body)
			if err != nil {
				return fmt.Errorf("decode PTR definitions update %s: %w", item.HashHex, err)
			}

			if err := m.writeBundle.ApplyPTRDefinitions(ctx, m.cfg, runToken, item.HashHex, decoded); err != nil {
				return fmt.Errorf("apply PTR definitions update %s: %w", item.HashHex, err)
			}
		case hydrusdb.PTRContentTypeMappings:
			decoded, err := decodeMappingsUpdatePayload(body)
			if err != nil {
				return fmt.Errorf("decode PTR mappings update %s: %w", item.HashHex, err)
			}

			if err := m.writeBundle.ApplyPTRMappings(ctx, m.cfg, runToken, item.HashHex, decoded); err != nil {
				return fmt.Errorf("apply PTR mappings update %s: %w", item.HashHex, err)
			}
		}
	}

	return nil
}

func (m *Manager) finishPTRSyncThrottled(
	ctx context.Context,
	started startedSync,
	status coreptrsync.Status,
	runErr error,
) (coreptrsync.Status, bool, error) {
	busyDelay, busy := ptrBusyRetryAfter(runErr)
	if !busy {
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

func (m *Manager) cancelPTRRetryWakeup() {
	if m == nil {
		return
	}

	m.retryWakeupMu.Lock()
	defer m.retryWakeupMu.Unlock()

	if m.retryWakeupCancel != nil {
		m.retryWakeupCancel()
		m.retryWakeupCancel = nil
	}
}

func (m *Manager) schedulePTRRetryWakeup(retryAtMS int64) {
	if m == nil || retryAtMS <= 0 {
		return
	}

	m.retryWakeupMu.Lock()
	if m.retryWakeupCancel != nil {
		m.retryWakeupCancel()
	}
	ctx, cancel := context.WithCancel(m.runnerCtx)
	m.retryWakeupID++
	wakeupID := m.retryWakeupID
	m.retryWakeupCancel = cancel
	m.retryWakeupMu.Unlock()

	go func(retryAtMS int64, wakeCtx context.Context, wakeupID uint64) {
		defer func() {
			m.retryWakeupMu.Lock()
			if m.retryWakeupID == wakeupID {
				m.retryWakeupCancel = nil
			}
			m.retryWakeupMu.Unlock()
		}()

		delay := time.Until(time.UnixMilli(retryAtMS).UTC())
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
				m.logger.Warn("PTR retry wakeup trigger failed", "error", err)
			}
			return
		}

		if m.logger != nil {
			m.logger.Info(
				"PTR retry wakeup triggered sync",
				"phase",
				status.Phase,
				"retry_at_ms",
				retryAtMS,
			)
		}
	}(retryAtMS, ctx, wakeupID)
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

func maxInt64(left int64, right int64) int64 {
	if left > right {
		return left
	}

	return right
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
	artifactPath, err := resolvePTRUpdateArtifactPath(bundle, hashHex)
	if err != nil {
		return nil, 0, false, err
	}

	body, mime, ok, err := readPTRUpdateArtifactFile(artifactPath, updateHash)
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

	if _, _, err := storePTRUpdateArtifact(bundle, hashHex, body); err != nil {
		return nil, 0, false, fmt.Errorf("migrate legacy PTR update artifact: %w", err)
	}

	if err := os.Remove(legacyPath); err != nil && !os.IsNotExist(err) {
		return nil, 0, false, fmt.Errorf("remove legacy PTR update artifact: %w", err)
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
