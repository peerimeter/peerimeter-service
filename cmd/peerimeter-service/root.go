package main

import (
	"log/slog"
	"os"

	"github.com/spf13/cobra"
)

func main() {
	if err := newRootCommand().Execute(); err != nil {
		os.Exit(1)
	}
}

func newRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:          "peerimeter-service",
		Short:        "Peerimeter Service",
		SilenceUsage: true,
	}
	root.AddCommand(&cobra.Command{
		Use:   "serve",
		Short: "Run the Peerimeter HTTP service",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runServer()
		},
	})
	root.AddCommand(&cobra.Command{
		Use:   "migrate",
		Short: "Apply pending PostgreSQL migrations",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if err := migrateFromEnvironment(); err != nil {
				return err
			}
			slog.New(slog.NewJSONHandler(os.Stdout, nil)).Info("PostgreSQL migrations are up to date")
			return nil
		},
	})
	return root
}
