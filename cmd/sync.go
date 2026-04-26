package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"mxctl/internal/matrix"
)

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Start syncing and forwarding messages to plugins",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := matrix.LoadConfig(configPath())
		if err != nil {
			return fmt.Errorf("load config (run 'mxctl login' first): %w", err)
		}

		client, err := matrix.New(cfg)
		if err != nil {
			return err
		}

		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()

		fmt.Fprintf(os.Stderr, "mxctl sync started (user: %s)\n", cfg.UserID)
		return client.Sync(ctx)
	},
}

func init() {
	rootCmd.AddCommand(syncCmd)
}
