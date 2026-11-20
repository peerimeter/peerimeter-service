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
	"path/filepath"
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
	importSnapshot, err := loadImportSnapshot(cfg, logger)
	if err != nil {
		return err
	}
	store, closeStore, err := openStore(cfg, importSnapshot, logger)
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

func loadImportSnapshot(cfg config.Config, logger *slog.Logger) (configstore.Snapshot, error) {
	var importSnapshot configstore.Snapshot
	if cfg.StorageBackend == "postgres" && cfg.ImportFile == "" {
		if fileStoreMayContainData(cfg.DataFile) {
			logger.Warn("file-backed data may be ignored by the PostgreSQL backend; set PEERIMETER_IMPORT_FILE to import it, or set PEERIMETER_STORAGE_BACKEND=file to keep using file storage")
		}
	}
	if cfg.StorageBackend == "postgres" && cfg.ImportFile != "" {
		if err := validateImportIndex(cfg.ImportFile, cfg.ImportSidecarOnly); err != nil {
			return importSnapshot, fmt.Errorf("open file store for import %s: %w", filepath.Base(cfg.ImportFile), err)
		}
		legacy, err := configstore.Open(cfg.ImportFile, defaultFileStoreQueueSize)
		if err != nil {
			return importSnapshot, fmt.Errorf("open file store for import: %w", err)
		}
		importSnapshot = legacy.Snapshot()
		legacy.Close()
		if importSnapshot.Empty() {
			return importSnapshot, errors.New("import file store into PostgreSQL: source contains no persisted network, node, or registration data")
		}
	}
	return importSnapshot, nil
}

func openStore(cfg config.Config, importSnapshot configstore.Snapshot, logger *slog.Logger) (api.ConfigStore, func(), error) {
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
		if cfg.ImportFile != "" {
			err = db.ImportSnapshot(context.Background(), importSnapshot)
			if err != nil {
				_ = db.Close()
				return nil, nil, fmt.Errorf("import file store into PostgreSQL: %w", err)
			}
			logger.Info("imported file store into empty PostgreSQL database", "data_file", cfg.ImportFile)
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

func fileStoreMayContainData(indexPath string) bool {
	if info, err := os.Stat(indexPath); err == nil && info.Mode().IsRegular() && info.Size() > 0 {
		return true
	}
	return fileStoreHasSidecars(indexPath)
}

func validateImportIndex(indexPath string, sidecarOnly bool) error {
	info, err := os.Stat(indexPath)
	if err == nil {
		if info.Mode().IsRegular() && !sidecarOnly {
			return nil
		}
		if info.Mode().IsRegular() {
			return errors.New("sidecar-only import mode requires a missing index file")
		}
		return errors.New("import index path must be a regular file")
	}
	if !errors.Is(err, os.ErrNotExist) {
		return errors.New("import index file cannot be accessed")
	}
	if sidecarOnly {
		if err := validateSidecarOnlyImport(indexPath); err != nil {
			return err
		}
		if fileStoreHasSidecars(indexPath) {
			return nil
		}
	}
	return errors.New("import index file is missing; set PEERIMETER_IMPORT_SIDECAR_ONLY=true only when importing node or registration sidecars without an index file")
}

func validateSidecarOnlyImport(indexPath string) error {
	for _, segment := range strings.Split(filepath.ToSlash(indexPath), "/") {
		if segment == ".." {
			return errors.New("sidecar-only import path cannot contain '..' components")
		}
	}
	canonicalIndex := filepath.Join(filepath.Dir(indexPath), "config.json")
	_, err := os.Lstat(canonicalIndex)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("config.json cannot be accessed; resolve it before enabling sidecar-only import")
	}
	if err == nil {
		return errors.New("config.json exists; check PEERIMETER_IMPORT_FILE instead of enabling sidecar-only import")
	}
	return nil
}

func fileStoreHasSidecars(indexPath string) bool {
	for _, path := range []string{
		filepath.Join(filepath.Dir(indexPath), "nodes"),
		filepath.Join(filepath.Dir(indexPath), "registrations"),
		indexPath + ".registrations",
	} {
		entries, err := os.ReadDir(path)
		if err == nil && len(entries) > 0 {
			return true
		}
	}
	return false
}
