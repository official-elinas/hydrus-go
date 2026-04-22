// Package ptrsync manages daemon-owned Public Tag Repository sync state.
package ptrsync

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
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

	if _, err := writeBundle.RecoverPTRSyncFoundation(ctx, cfg); err != nil {
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

// Shutdown stops any in-flight daemon-owned background PTR sync and waits for it
// to release its lease before returning.
func (m *Manager) Shutdown(ctx context.Context) error {
	if m == nil {
		return nil
	}

	if m.runnerCancel != nil {
		m.runnerCancel()
	}

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
			"downloaded_updates",
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
	finished := false
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
		if finished {
			if m.logger != nil {
				m.logger.Info(
					"PTR sync run completed successfully",
					"run_token",
					started.lease.RunToken,
					"metadata_slice",
					status.MetadataSlice,
					"downloaded_updates",
					status.DownloadedUpdateCount,
					"processed_definitions",
					status.ProcessedDefinitionCount,
					"processed_content",
					status.ProcessedContentCount,
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
				"downloaded_updates",
				failedStatus.DownloadedUpdateCount,
			)
		}
	}()

	remoteState, err := started.client.FetchRemoteState(ctx, started.lease.Status.MetadataSlice)
	if err != nil {
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
			"downloaded_updates",
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

		for index, pendingHash := range pendingHashes {
			body, mimeEnum, fetchErr := started.client.FetchUpdate(ctx, pendingHash)
			if fetchErr != nil {
				err = fmt.Errorf("fetch PTR update %x: %w", pendingHash, fetchErr)
				return status, err
			}

			hashHex := hex.EncodeToString(pendingHash)
			artifactPath, alreadyPresent, artifactErr := storePTRUpdateArtifact(
				m.writeBundle,
				hashHex,
				body,
			)
			if artifactErr != nil {
				err = fmt.Errorf("store PTR update artifact %x: %w", pendingHash, artifactErr)
				return status, err
			}

			result, importErr := m.writeBundle.RecordPreparedLocalImport(
				ctx,
				hydrusdb.PreparedLocalImport{
					HashHex:             hashHex,
					Size:                int64(len(body)),
					Mime:                mimeEnum,
					ImportedAtMS:        time.Now().UTC().UnixMilli(),
					LocalFileServiceKey: repositoryUpdatesServiceKeyHex(),
				},
			)
			if importErr != nil {
				if !alreadyPresent {
					if cleanupErr := os.Remove(artifactPath); cleanupErr != nil && !os.IsNotExist(cleanupErr) {
						importErr = errors.Join(
							importErr,
							fmt.Errorf("remove PTR update artifact after failed DB registration: %w", cleanupErr),
						)
					}
				}

				err = fmt.Errorf("register PTR update %x: %w", pendingHash, importErr)
				return status, err
			}

			if status, err = m.writeBundle.FinalizePTRDownloadedUpdate(
				ctx,
				m.cfg,
				started.lease.RunToken,
				hex.EncodeToString(pendingHash),
			); err != nil {
				err = fmt.Errorf("finalize PTR downloaded update %d: %w", result.FileID, err)
				return status, err
			}

			if m.logger != nil && (index == 0 || (index+1)%25 == 0 || index == len(pendingHashes)-1) {
				m.logger.Info(
					"PTR update download progress",
					"run_token",
					started.lease.RunToken,
					"completed",
					index+1,
					"total",
					len(pendingHashes),
					"latest_hash",
					hashHex,
					"downloaded_updates",
					status.DownloadedUpdateCount,
				)
			}
		}
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

	finished = true
	return status, nil
}

func repositoryUpdatesArtifactsRoot(bundle *hydrusdb.Bundle) string {
	if bundle == nil {
		return ""
	}

	return filepath.Join(filepath.Dir(bundle.MainDBPath()), "repository_updates")
}

func repositoryUpdatesServiceKeyHex() string {
	return hex.EncodeToString([]byte("repository updates"))
}

func resolvePTRUpdateArtifactPath(bundle *hydrusdb.Bundle, hashHex string) (string, error) {
	if bundle == nil {
		return "", fmt.Errorf("hydrus bundle is required")
	}

	normalizedHash, err := clientfiles.NormalizeSHA256Hex(hashHex)
	if err != nil {
		return "", err
	}

	return filepath.Join(
		repositoryUpdatesArtifactsRoot(bundle),
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
