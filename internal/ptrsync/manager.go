// Package ptrsync manages daemon-owned Public Tag Repository sync state.
package ptrsync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	coreptrsync "github.com/official-elinas/hydrus-go/internal/core/ptrsync"
	"github.com/official-elinas/hydrus-go/internal/db/hydrusdb"
)

// Manager provides daemon-visible PTR sync status backed by the Hydrus bundle.
type Manager struct {
	logger            *slog.Logger
	cfg               coreptrsync.Config
	readBundle        *hydrusdb.Bundle
	writeBundle       *hydrusdb.Bundle
	unavailableReason string
}

// NewManager constructs the daemon PTR status manager and ensures persisted
// local state exists when PTR sync is explicitly enabled and a writable bundle
// is available.
func NewManager(
	ctx context.Context,
	logger *slog.Logger,
	cfg coreptrsync.Config,
	readBundle *hydrusdb.Bundle,
	writeBundle *hydrusdb.Bundle,
) (*Manager, error) {
	manager := &Manager{
		logger:      logger,
		cfg:         cfg,
		readBundle:  readBundle,
		writeBundle: writeBundle,
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

	if _, err := writeBundle.EnsurePTRSyncFoundation(ctx, cfg); err != nil {
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

	return m.readBundle.GetPTRSyncStatus(ctx, m.cfg)
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
