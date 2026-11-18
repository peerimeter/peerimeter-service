package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/peerimeter/peerimeter-service/internal/api"
	"github.com/peerimeter/peerimeter-service/internal/config"
	"github.com/peerimeter/peerimeter-service/internal/configstore"
	"github.com/peerimeter/peerimeter-service/internal/postgresstore"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("load configuration", "error", err)
		os.Exit(2)
	}
	var importSnapshot configstore.Snapshot
	if cfg.StorageBackend == "postgres" && cfg.ImportFile != "" {
		legacy, err := configstore.Open(cfg.ImportFile, 128)
		if err != nil {
			logger.Error("open file store for import", "error", err)
			os.Exit(1)
		}
		importSnapshot = legacy.Snapshot()
		legacy.Close()
		if importSnapshot.Empty() {
			logger.Error("import file store into PostgreSQL", "error", "source contains no persisted network, node, or registration data")
			os.Exit(1)
		}
	}

	var store interface {
		GetNetwork(string) (json.RawMessage, error)
		GetNetworkMetadata(string) (json.RawMessage, error)
		GetNode(string, string) (json.RawMessage, error)
		ListRegisteredNodes(string) (map[string]json.RawMessage, error)
		ListNetworks() (map[string]json.RawMessage, error)
		CreateNetwork(context.Context, string, json.RawMessage) error
		RegisterNode(context.Context, string, string, json.RawMessage) (json.RawMessage, error)
		PutNetwork(context.Context, string, json.RawMessage) error
		PutNode(context.Context, string, string, json.RawMessage) error
	}
	var closeStore func()
	if cfg.StorageBackend == "postgres" {
		db, err := postgresstore.Open(cfg.DatabaseURL)
		if err != nil {
			logger.Error("open PostgreSQL store", "error", err)
			os.Exit(1)
		}
		if cfg.ImportFile != "" {
			err = db.ImportSnapshot(context.Background(), importSnapshot)
			if err != nil {
				logger.Error("import file store into PostgreSQL", "error", err)
				_ = db.Close()
				os.Exit(1)
			}
			logger.Info("imported file store into empty PostgreSQL database", "data_file", cfg.ImportFile)
		}
		store = db
		closeStore = func() { _ = db.Close() }
	} else {
		fileStore, err := configstore.Open(cfg.DataFile, 128)
		if err != nil {
			logger.Error("open configuration store", "error", err)
			os.Exit(1)
		}
		store = fileStore
		closeStore = fileStore.Close
	}
	defer closeStore()

	server := &http.Server{
		Addr:              net.JoinHostPort(cfg.ListenAddr, cfg.Port),
		Handler:           api.New(cfg.APIKey, cfg.PublicAPIURL, store, logger, cfg.RequireHTTPS, strings.Split(cfg.CORSAllowedOrigins, ",")...),
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

	logger.Info("starting peerimeter service", "addr", server.Addr, "data_file", cfg.DataFile)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("serve HTTP", "error", err)
		os.Exit(1)
	}
	if err := <-shutdownResult; err != nil {
		logger.Error("shutdown HTTP server", "error", err)
		os.Exit(1)
	}
}
