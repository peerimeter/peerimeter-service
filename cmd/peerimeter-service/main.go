package main

import (
	"context"
	"errors"
	"fmt"
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

const defaultFileStoreQueueSize = 128

func runServer() error {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	store, closeStore, err := openStore(cfg, logger)
	if err != nil {
		return err
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

	startupAttrs := []any{"addr", server.Addr, "storage_backend", cfg.StorageBackend}
	if cfg.StorageBackend == "file" {
		startupAttrs = append(startupAttrs, "data_file", cfg.DataFile)
	}
	logger.Info("starting peerimeter service", startupAttrs...)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve HTTP: %w", err)
	}
	if err := <-shutdownResult; err != nil {
		return fmt.Errorf("shutdown HTTP server: %w", err)
	}
	return nil
}

func openStore(cfg config.Config, logger *slog.Logger) (api.ConfigStore, func(), error) {
	if cfg.StorageBackend == "postgres" {
		var db *postgresstore.Store
		var err error
		tlsOptions := postgresstore.TLSOptions{
			CACertPEM:           cfg.DatabaseCACert,
			AllowSSLModeRequire: cfg.DatabaseAllowSSLModeRequire,
		}
		if cfg.AutoMigrate {
			db, err = postgresstore.Open(cfg.DatabaseURL, tlsOptions)
		} else {
			logger.Info("automatic PostgreSQL migrations are disabled")
			db, err = postgresstore.OpenWithoutMigrations(cfg.DatabaseURL, tlsOptions)
		}
		if err != nil {
			return nil, nil, fmt.Errorf("open PostgreSQL store: %w", err)
		}
		return db, func() { _ = db.Close() }, nil
	}
	if cfg.StorageBackend == "file" {
		fileStore, err := configstore.Open(cfg.DataFile, defaultFileStoreQueueSize)
		if err != nil {
			return nil, nil, fmt.Errorf("open configuration store: %w", err)
		}
		return fileStore, fileStore.Close, nil
	}
	return nil, nil, errors.New("unsupported storage backend")
}

func migrateFromEnvironment() error {
	cfg, err := config.LoadMigrate()
	if err != nil {
		return err
	}
	return postgresstore.Migrate(cfg.DatabaseURL, postgresstore.TLSOptions{
		CACertPEM:           cfg.DatabaseCACert,
		AllowSSLModeRequire: cfg.AllowSSLModeRequire,
	})
}
