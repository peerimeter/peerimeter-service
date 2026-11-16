package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/peerimeter/peerimeter-service/internal/api"
	"github.com/peerimeter/peerimeter-service/internal/configstore"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	key := os.Getenv("PEERIMETER_API_KEY")
	if strings.TrimSpace(key) == "" {
		logger.Error("PEERIMETER_API_KEY must be set")
		os.Exit(2)
	}
	path := os.Getenv("PEERIMETER_DATA_FILE")
	if path == "" {
		path = "/var/lib/peerimeter/config.json"
	}
	publicURL := os.Getenv("PEERIMETER_PUBLIC_API_URL")
	store, err := configstore.Open(path, 128)
	if err != nil {
		logger.Error("open configuration store", "error", err)
		os.Exit(1)
	}
	defer store.Close()

	server := &http.Server{
		Addr:              ":" + envOr("PORT", "8080"),
		Handler:           api.New(key, publicURL, store, logger),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	shutdownResult := make(chan error, 1)
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		shutdownResult <- server.Shutdown(shutdownCtx)
	}()

	logger.Info("starting peerimeter service", "addr", server.Addr, "data_file", path)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("serve HTTP", "error", err)
		os.Exit(1)
	}
	if err := <-shutdownResult; err != nil {
		logger.Error("shutdown HTTP server", "error", err)
		os.Exit(1)
	}
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
