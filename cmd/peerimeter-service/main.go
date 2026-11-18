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
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/peerimeter/peerimeter-service/internal/api"
	"github.com/peerimeter/peerimeter-service/internal/config"
	"github.com/peerimeter/peerimeter-service/internal/configstore"
	"github.com/peerimeter/peerimeter-service/internal/postgresstore"
)

func runServer() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("load configuration", "error", err)
		os.Exit(2)
	}
	var importSnapshot configstore.Snapshot
	if cfg.StorageBackend == "postgres" && cfg.ImportFile == "" {
		if fileStoreMayContainData(cfg.DataFile) {
			logger.Warn("file-backed data may be ignored by the PostgreSQL backend; set PEERIMETER_IMPORT_FILE to import it, or set PEERIMETER_STORAGE_BACKEND=file to keep using file storage")
		}
	}
	if cfg.StorageBackend == "postgres" && cfg.ImportFile != "" {
		if err := validateImportIndex(cfg.ImportFile, cfg.ImportSidecarOnly); err != nil {
			logger.Error("open file store for import", "error", err, "data_file", filepath.Base(cfg.ImportFile))
			os.Exit(1)
		}
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
		var db *postgresstore.Store
		var err error
		if cfg.AutoMigrate {
			db, err = postgresstore.Open(cfg.DatabaseURL)
		} else {
			logger.Info("automatic PostgreSQL migrations are disabled")
			db, err = postgresstore.OpenWithoutMigrations(cfg.DatabaseURL)
		}
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

func migrateFromEnvironment() error {
	if os.Getenv("PEERIMETER_STORAGE_BACKEND") != "postgres" {
		return errors.New("PEERIMETER_STORAGE_BACKEND must be postgres for the migrate command")
	}
	databaseURL := strings.TrimSpace(os.Getenv("PEERIMETER_DATABASE_URL"))
	if databaseURL == "" {
		return errors.New("PEERIMETER_DATABASE_URL must be set for the migrate command")
	}
	return postgresstore.Migrate(databaseURL)
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
		for _, segment := range strings.Split(filepath.ToSlash(indexPath), "/") {
			if segment == ".." {
				return errors.New("sidecar-only import path cannot contain '..' components")
			}
		}
	}
	canonicalIndex := filepath.Join(filepath.Dir(indexPath), "config.json")
	if sidecarOnly {
		_, statErr := os.Lstat(canonicalIndex)
		if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return errors.New("config.json cannot be accessed; resolve it before enabling sidecar-only import")
		}
		if statErr == nil {
			return errors.New("config.json exists; check PEERIMETER_IMPORT_FILE instead of enabling sidecar-only import")
		}
	}
	if sidecarOnly && fileStoreHasSidecars(indexPath) {
		return nil
	}
	return errors.New("import index file is missing; set PEERIMETER_IMPORT_SIDECAR_ONLY=true only when importing node or registration sidecars without an index file")
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
