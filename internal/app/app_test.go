package app

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/official-elinas/hydrus-go/internal/config"
)

func TestRun_ShutsDownWhenContextIsCanceled(t *testing.T) {
	cfg := config.Config{
		ListenAddr:               "127.0.0.1:0",
		AccessName:               "test-client",
		LogLevel:                 "error",
		ShutdownTimeout:          time.Second,
		AllowNonLocalConnections: false,
		EnableCORS:               false,
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	application, err := New(cfg, logger)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- application.Run(ctx)
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for App.Run to stop")
	}
}

func TestNew_OpensConfiguredDBBundle(t *testing.T) {
	dbDir := t.TempDir()
	createEmptySQLiteDB(t, filepath.Join(dbDir, "client.db"))
	createEmptySQLiteDB(t, filepath.Join(dbDir, "client.master.db"))
	createEmptySQLiteDB(t, filepath.Join(dbDir, "client.caches.db"))
	createEmptySQLiteDB(t, filepath.Join(dbDir, "client.mappings.db"))

	cfg := config.Config{
		ListenAddr:               "127.0.0.1:0",
		DBDir:                    dbDir,
		AccessName:               "test-client",
		LogLevel:                 "error",
		ShutdownTimeout:          time.Second,
		AllowNonLocalConnections: false,
		EnableCORS:               false,
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	application, err := New(cfg, logger)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer application.closeResources()

	if application.bundle == nil {
		t.Fatal("application.bundle = nil, want opened bundle")
	}
}

func createEmptySQLiteDB(t *testing.T, path string) {
	t.Helper()

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open(%q) error = %v", path, err)
	}
	defer db.Close()

	if _, err := db.Exec(`PRAGMA user_version = 0;`); err != nil {
		t.Fatalf("Exec(PRAGMA user_version) error = %v", err)
	}
}
