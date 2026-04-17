// Package app wires together the bootstrap hydrus-go daemon.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/official-elinas/hydrus-go/internal/api/httpapi"
	"github.com/official-elinas/hydrus-go/internal/buildinfo"
	"github.com/official-elinas/hydrus-go/internal/config"
	"github.com/official-elinas/hydrus-go/internal/core/filemetadata"
	"github.com/official-elinas/hydrus-go/internal/core/services"
	"github.com/official-elinas/hydrus-go/internal/db/hydrusdb"
)

// App holds the bootstrap daemon runtime state.
type App struct {
	cfg    config.Config
	logger *slog.Logger
	access *httpapi.AccessControl
	server *http.Server
	bundle *hydrusdb.Bundle
}

// New constructs the bootstrap daemon application.
func New(cfg config.Config, logger *slog.Logger) (*App, error) {
	access, err := httpapi.NewAccessControl(
		cfg.AccessKey,
		cfg.AccessName,
		[]httpapi.Permission{httpapi.PermissionSearchAndFetchFiles},
	)
	if err != nil {
		return nil, fmt.Errorf("create access control: %w", err)
	}

	serviceProvider := services.Provider(services.DefaultProvider())
	var metadataStore filemetadata.Store
	var bundle *hydrusdb.Bundle

	if cfg.DBDir != "" {
		bundle, err = hydrusdb.Open(context.Background(), cfg.DBDir)
		if err != nil {
			return nil, fmt.Errorf("open hydrus DB bundle: %w", err)
		}

		serviceProvider = bundle
		metadataStore = bundle
	}

	handler := httpapi.NewHandler(
		logger,
		access,
		serviceProvider,
		metadataStore,
		cfg.EnableCORS,
	)

	server := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       time.Minute,
	}

	return &App{
		cfg:    cfg,
		logger: logger,
		access: access,
		server: server,
		bundle: bundle,
	}, nil
}

// Run starts the daemon and blocks until the context is canceled or the server
// exits with an error.
func (a *App) Run(ctx context.Context) error {
	defer a.closeResources()

	if generatedAccessKey, generated := a.access.GeneratedAccessKey(); generated {
		a.logger.Warn(
			"generated initial API access key",
			"access_key",
			generatedAccessKey,
		)
	} else {
		a.logger.Info(
			"using configured API access key",
			"access_name",
			a.cfg.AccessName,
		)
	}

	a.logger.Info(
		"starting hydrus-go daemon",
		"listen_addr",
		a.cfg.ListenAddr,
		"client_api_version",
		buildinfo.ClientAPIVersion,
		"hydrus_version",
		buildinfo.ReferenceHydrusVersion,
	)

	errCh := make(chan error, 1)

	go func() {
		err := a.server.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}

		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(
			context.Background(),
			a.cfg.ShutdownTimeout,
		)
		defer cancel()

		a.logger.Info("shutting down hydrus-go daemon")

		if err := a.server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown daemon: %w", err)
		}

		if err := <-errCh; err != nil {
			return fmt.Errorf("wait for server stop: %w", err)
		}

		return ctx.Err()
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("serve daemon: %w", err)
		}

		return nil
	}
}

func (a *App) closeResources() {
	if a.bundle == nil {
		return
	}

	if err := a.bundle.Close(); err != nil {
		a.logger.Error("close hydrus DB bundle", "error", err)
	}
}
