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
	"github.com/official-elinas/hydrus-go/internal/bootstrap"
	"github.com/official-elinas/hydrus-go/internal/buildinfo"
	"github.com/official-elinas/hydrus-go/internal/config"
	"github.com/official-elinas/hydrus-go/internal/core/fileassets"
	"github.com/official-elinas/hydrus-go/internal/core/fileimport"
	"github.com/official-elinas/hydrus-go/internal/core/filemetadata"
	"github.com/official-elinas/hydrus-go/internal/core/filetrash"
	"github.com/official-elinas/hydrus-go/internal/core/librarybrowse"
	"github.com/official-elinas/hydrus-go/internal/core/services"
	"github.com/official-elinas/hydrus-go/internal/db/hydrusdb"
	"github.com/official-elinas/hydrus-go/internal/importing"
)

var (
	openReadBundle          = hydrusdb.Open
	openWriteBundle         = hydrusdb.OpenWritable
	newDefaultImporter      = importing.NewDefaultImporter
	ensureFreshClientBundle = bootstrap.EnsureFreshClientBundle
)

// App holds the bootstrap daemon runtime state.
type App struct {
	cfg         config.Config
	logger      *slog.Logger
	access      *httpapi.AccessControl
	server      *http.Server
	readBundle  *hydrusdb.Bundle
	writeBundle *hydrusdb.Bundle
}

// New constructs the bootstrap daemon application.
func New(startupCtx context.Context, cfg config.Config, logger *slog.Logger) (*App, error) {
	if startupCtx == nil {
		startupCtx = context.Background()
	}

	serviceProvider := services.Provider(services.DefaultProvider())
	var metadataStore filemetadata.Store
	var browseStore librarybrowse.Store
	var assetStore fileassets.Store
	var importStore fileimport.Store
	var trashStore filetrash.Store
	var readBundle *hydrusdb.Bundle
	var writeBundle *hydrusdb.Bundle
	var err error

	if cfg.DBDir != "" {
		bootstrapResult, err := ensureFreshClientBundle(startupCtx, bootstrap.Options{
			DBDir:         cfg.DBDir,
			Enabled:       cfg.EnableFreshClientBootstrap,
			PythonCommand: cfg.BootstrapPythonCommand,
			HydrusRoot:    cfg.BootstrapHydrusRoot,
			Timeout:       cfg.BootstrapTimeout,
		})
		if err != nil {
			return nil, fmt.Errorf("prepare hydrus DB bundle: %w", err)
		}

		if bootstrapResult.Bootstrapped {
			logger.Info(
				"bootstrapped fresh hydrus client bundle via Python",
				"db_dir",
				cfg.DBDir,
				"hydrus_root",
				bootstrapResult.HydrusRoot,
			)
		}

		readBundle, err = openReadBundle(startupCtx, cfg.DBDir)
		if err != nil {
			return nil, fmt.Errorf("open hydrus DB bundle: %w", err)
		}

		writeBundle, err = openWriteBundle(startupCtx, cfg.DBDir)
		if err != nil {
			logger.Warn(
				"write bundle unavailable; continuing in read-only daemon mode",
				"db_dir",
				cfg.DBDir,
				"error",
				err,
			)
		} else {
			importer, err := newDefaultImporter(writeBundle, cfg.DBDir)
			if err != nil {
				logger.Warn(
					"local importer unavailable; continuing in read-only daemon mode",
					"db_dir",
					cfg.DBDir,
					"error",
					err,
				)
				if closeErr := writeBundle.Close(); closeErr != nil {
					logger.Error("close unusable write hydrus DB bundle", "error", closeErr)
				}
				writeBundle = nil
			} else {
				importStore = importer
				trashStore = writeBundle
			}
		}

		serviceProvider = readBundle
		metadataStore = readBundle
		browseStore = readBundle
		assetStore = readBundle
	}

	permissions := []httpapi.Permission{httpapi.PermissionSearchAndFetchFiles}
	if importStore != nil || trashStore != nil {
		permissions = append(permissions, httpapi.PermissionImportAndDeleteFiles)
	}

	access, err := httpapi.NewAccessControl(
		cfg.AccessKey,
		cfg.AccessName,
		permissions,
	)
	if err != nil {
		if readBundle != nil {
			_ = readBundle.Close()
		}

		if writeBundle != nil {
			_ = writeBundle.Close()
		}

		return nil, fmt.Errorf("create access control: %w", err)
	}

	handler := httpapi.NewHandler(
		logger,
		access,
		serviceProvider,
		metadataStore,
		browseStore,
		assetStore,
		importStore,
		trashStore,
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
		cfg:         cfg,
		logger:      logger,
		access:      access,
		server:      server,
		readBundle:  readBundle,
		writeBundle: writeBundle,
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
	if a.readBundle != nil {
		if err := a.readBundle.Close(); err != nil {
			a.logger.Error("close read hydrus DB bundle", "error", err)
		}
	}

	if a.writeBundle != nil {
		if err := a.writeBundle.Close(); err != nil {
			a.logger.Error("close write hydrus DB bundle", "error", err)
		}
	}
}
