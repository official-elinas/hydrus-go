// Package app wires together the bootstrap hydrus-go daemon.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/official-elinas/hydrus-go/internal/api/httpapi"
	"github.com/official-elinas/hydrus-go/internal/bootstrap"
	"github.com/official-elinas/hydrus-go/internal/buildinfo"
	"github.com/official-elinas/hydrus-go/internal/config"
	"github.com/official-elinas/hydrus-go/internal/core/clientapi"
	coredownloader "github.com/official-elinas/hydrus-go/internal/core/downloader"
	"github.com/official-elinas/hydrus-go/internal/core/fileassets"
	"github.com/official-elinas/hydrus-go/internal/core/fileimport"
	"github.com/official-elinas/hydrus-go/internal/core/filemetadata"
	"github.com/official-elinas/hydrus-go/internal/core/filetrash"
	"github.com/official-elinas/hydrus-go/internal/core/librarybrowse"
	coreptrsync "github.com/official-elinas/hydrus-go/internal/core/ptrsync"
	"github.com/official-elinas/hydrus-go/internal/core/services"
	"github.com/official-elinas/hydrus-go/internal/db/hydrusdb"
	hydownloadermanager "github.com/official-elinas/hydrus-go/internal/downloader/hydownloader"
	"github.com/official-elinas/hydrus-go/internal/importing"
	ptrsyncmanager "github.com/official-elinas/hydrus-go/internal/ptrsync"
)

var (
	openReadBundle          = hydrusdb.Open
	openWriteBundle         = hydrusdb.OpenWritable
	newDefaultImporter      = importing.NewDefaultImporter
	ensureFreshClientBundle = bootstrap.EnsureFreshClientBundle
	waitForDaemonReadyFn    = waitForDaemonReady
)

type downloaderController interface {
	ActivateAutoimport(context.Context) error
	Shutdown(context.Context) error
}

// App holds the bootstrap daemon runtime state.
type App struct {
	cfg               config.Config
	logger            *slog.Logger
	access            *httpapi.AccessControl
	server            *http.Server
	readBundle        *hydrusdb.Bundle
	metadataReadBundle *hydrusdb.Bundle
	ptrReadBundle     *hydrusdb.Bundle
	writeBundle       *hydrusdb.Bundle
	ptrManager        *ptrsyncmanager.Manager
	downloaderManager downloaderController
}

// New constructs the bootstrap daemon application.
func New(startupCtx context.Context, cfg config.Config, logger *slog.Logger) (*App, error) {
	if startupCtx == nil {
		startupCtx = context.Background()
	}

	cfg.PTR = normalizedPTRConfig(cfg.PTR)

	serviceProvider := services.Provider(services.DefaultProvider())
	var metadataStore filemetadata.Store
	var browseStore librarybrowse.Store
	var assetStore fileassets.Store
	var clientAPIStore clientapi.Store
	var downloaderStore coredownloader.Store
	var importStore fileimport.Store
	var trashStore filetrash.Store
	var ptrStore coreptrsync.Store
	var readBundle *hydrusdb.Bundle
	var metadataReadBundle *hydrusdb.Bundle
	var ptrReadBundle *hydrusdb.Bundle
	var writeBundle *hydrusdb.Bundle
	var err error

	if cfg.DBDir != "" {
		logger.Info(
			"preparing hydrus DB bundle",
			"db_dir",
			cfg.DBDir,
			"fresh_bootstrap",
			cfg.EnableFreshClientBootstrap,
		)

		bootstrapResult, err := ensureFreshClientBundle(startupCtx, bootstrap.Options{
			DBDir:   cfg.DBDir,
			Enabled: cfg.EnableFreshClientBootstrap,
			Timeout: cfg.BootstrapTimeout,
		})
		if err != nil {
			return nil, fmt.Errorf("prepare hydrus DB bundle: %w", err)
		}

		if bootstrapResult.Bootstrapped {
			logger.Info(
				"bootstrapped fresh hydrus client bundle",
				"db_dir",
				cfg.DBDir,
			)
		}

		logger.Info("opening hydrus DB read bundle", "db_dir", cfg.DBDir)
		readBundle, err = openReadBundle(startupCtx, cfg.DBDir)
		if err != nil {
			return nil, fmt.Errorf("open hydrus DB bundle: %w", err)
		}

		logger.Info("opening PTR hydrus DB read bundle", "db_dir", cfg.DBDir)
		ptrReadBundle, err = openReadBundle(startupCtx, cfg.DBDir)
		if err != nil {
			_ = readBundle.Close()
			return nil, fmt.Errorf("open PTR read hydrus DB bundle: %w", err)
		}

		logger.Info("opening hydrus DB metadata read bundle", "db_dir", cfg.DBDir)
		metadataReadBundle, err = openReadBundle(startupCtx, cfg.DBDir)
		if err != nil {
			_ = readBundle.Close()
			_ = ptrReadBundle.Close()
			return nil, fmt.Errorf("open metadata read hydrus DB bundle: %w", err)
		}

		logger.Info("opening hydrus DB write bundle", "db_dir", cfg.DBDir)
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
				clientAPIStore = writeBundle
				importStore = importer
				trashStore = writeBundle
			}
		}

		serviceProvider = readBundle
		metadataStore = newMetadataStoreRouter(metadataReadBundle, writeBundle)
		browseStore = readBundle
		assetStore = readBundle
	}

	logger.Info("preparing PTR sync manager", "enabled", cfg.PTR.Enabled)
	ptrManager, err := ptrsyncmanager.NewManager(
		startupCtx,
		logger,
		cfg.PTR,
		ptrReadBundle,
		writeBundle,
	)
	if err != nil {
		if readBundle != nil {
			_ = readBundle.Close()
		}
		if metadataReadBundle != nil {
			_ = metadataReadBundle.Close()
		}
		if ptrReadBundle != nil {
			_ = ptrReadBundle.Close()
		}

		if writeBundle != nil {
			_ = writeBundle.Close()
		}

		return nil, fmt.Errorf("prepare PTR sync manager: %w", err)
	}
	ptrStore = ptrManager

	permissions := []httpapi.Permission{
		httpapi.PermissionImportAndEditURLs,
		httpapi.PermissionSearchAndFetchFiles,
		httpapi.PermissionManageDatabase,
		httpapi.PermissionEditFileTags,
		httpapi.PermissionEditFileNotes,
		httpapi.PermissionEditFileTimes,
		httpapi.PermissionCommitPending,
	}
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
		if metadataReadBundle != nil {
			_ = metadataReadBundle.Close()
		}
		if ptrReadBundle != nil {
			_ = ptrReadBundle.Close()
		}

		if writeBundle != nil {
			_ = writeBundle.Close()
		}

		return nil, fmt.Errorf("create access control: %w", err)
	}

	hydrusAPIURL := strings.TrimSpace(cfg.Downloader.PublicAPIURL)
	if hydrusAPIURL == "" {
		hydrusAPIURL = "http://" + cfg.ListenAddr
	}
	if cfg.Downloader.Enabled {
		logger.Info(
			"preparing hydownloader manager",
			"root",
			cfg.Downloader.Root,
			"host",
			cfg.Downloader.Host,
			"port",
			cfg.Downloader.Port,
			"autoimport",
			cfg.Downloader.Autoimport,
		)
	}
	hydownloaderManager, err := hydownloadermanager.New(
		startupCtx,
		logger,
		cfg.Downloader,
		hydrusAPIURL,
		access.AccessKey(),
	)
	if err != nil {
		if readBundle != nil {
			_ = readBundle.Close()
		}
		if metadataReadBundle != nil {
			_ = metadataReadBundle.Close()
		}
		if writeBundle != nil {
			_ = writeBundle.Close()
		}
		if ptrManager != nil {
			_ = ptrManager.Shutdown(context.Background())
		}
		return nil, fmt.Errorf("prepare hydownloader manager: %w", err)
	}
	var downloaderManager downloaderController
	if hydownloaderManager != nil {
		downloaderManager = hydownloaderManager
		downloaderStore = hydownloaderManager
	}

	handler := httpapi.NewHandler(
		logger,
		access,
		serviceProvider,
		metadataStore,
		browseStore,
		assetStore,
		clientAPIStore,
		downloaderStore,
		importStore,
		trashStore,
		ptrStore,
		cfg.EnableCORS,
	)

	server := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       5 * time.Minute,
		WriteTimeout:      5 * time.Minute,
		IdleTimeout:       time.Minute,
	}

	return &App{
		cfg:               cfg,
		logger:            logger,
		access:            access,
		server:            server,
		readBundle:        readBundle,
		metadataReadBundle: metadataReadBundle,
		ptrReadBundle:     ptrReadBundle,
		writeBundle:       writeBundle,
		ptrManager:        ptrManager,
		downloaderManager: downloaderManager,
	}, nil
}

func normalizedPTRConfig(cfg coreptrsync.Config) coreptrsync.Config {
	defaults := coreptrsync.DefaultConfig()
	if strings.TrimSpace(cfg.Host) == "" {
		cfg.Host = defaults.Host
	}

	if cfg.Port == 0 {
		cfg.Port = defaults.Port
	}

	if strings.TrimSpace(cfg.AccessKey) == "" {
		cfg.AccessKey = defaults.AccessKey
	}

	if strings.TrimSpace(cfg.ServiceName) == "" {
		cfg.ServiceName = defaults.ServiceName
	}

	return cfg
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

	if err := a.activateDownloaderAutoimportAfterReady(); err != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), a.cfg.ShutdownTimeout)
		if a.cfg.ShutdownTimeout <= 0 {
			shutdownCtx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
		}
		defer cancel()
		_ = a.server.Shutdown(shutdownCtx)
		if waitErr := <-errCh; waitErr != nil {
			return errors.Join(err, fmt.Errorf("wait for server stop: %w", waitErr))
		}
		return err
	}

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

		if a.ptrManager != nil {
			if err := a.ptrManager.Shutdown(shutdownCtx); err != nil {
				return fmt.Errorf("shutdown PTR sync manager: %w", err)
			}
		}

		if a.downloaderManager != nil {
			if err := a.downloaderManager.Shutdown(shutdownCtx); err != nil {
				return fmt.Errorf("shutdown hydownloader manager: %w", err)
			}
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
	if a.ptrManager != nil {
		shutdownTimeout := a.cfg.ShutdownTimeout
		if shutdownTimeout <= 0 {
			shutdownTimeout = 30 * time.Second
		}

		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		if err := a.ptrManager.Shutdown(shutdownCtx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
			a.logger.Error("stop PTR sync manager", "error", err)
		}
		cancel()
	}

	if a.downloaderManager != nil {
		shutdownTimeout := a.cfg.ShutdownTimeout
		if shutdownTimeout <= 0 {
			shutdownTimeout = 30 * time.Second
		}

		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		if err := a.downloaderManager.Shutdown(shutdownCtx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
			a.logger.Error("stop hydownloader manager", "error", err)
		}
		cancel()
	}

	if a.readBundle != nil {
		if err := a.readBundle.Close(); err != nil {
			a.logger.Error("close read hydrus DB bundle", "error", err)
		}
	}

	if a.metadataReadBundle != nil {
		if err := a.metadataReadBundle.Close(); err != nil {
			a.logger.Error("close metadata read hydrus DB bundle", "error", err)
		}
	}

	if a.ptrReadBundle != nil {
		if err := a.ptrReadBundle.Close(); err != nil {
			a.logger.Error("close ptr read hydrus DB bundle", "error", err)
		}
	}

	if a.writeBundle != nil {
		if err := a.writeBundle.Close(); err != nil {
			a.logger.Error("close write hydrus DB bundle", "error", err)
		}
	}
}

func (a *App) activateDownloaderAutoimportAfterReady() error {
	if a.downloaderManager == nil {
		return nil
	}

	readyTimeout := a.cfg.ShutdownTimeout
	if readyTimeout <= 0 {
		readyTimeout = 30 * time.Second
	}

	readyCtx, cancel := context.WithTimeout(context.Background(), readyTimeout)
	defer cancel()

	if err := waitForDaemonReadyFn(readyCtx, a.cfg.ListenAddr); err != nil {
		return fmt.Errorf("wait for hydrus-go readiness before hydownloader autoimport: %w", err)
	}
	if err := a.downloaderManager.ActivateAutoimport(readyCtx); err != nil {
		return fmt.Errorf("resume hydownloader autoimport: %w", err)
	}

	return nil
}

func waitForDaemonReady(ctx context.Context, listenAddr string) error {
	baseURL := "http://" + strings.TrimSpace(listenAddr)
	client := &http.Client{Timeout: 2 * time.Second}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/healthz", nil)
		if err == nil {
			resp, err := client.Do(req)
			if err == nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					return nil
				}
			}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
