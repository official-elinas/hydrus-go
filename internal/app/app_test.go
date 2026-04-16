package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
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
