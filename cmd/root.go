package cmd

import (
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "mxctl",
	Short: "Matrix sync daemon — receive messages and forward to processor scripts",
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func configDir() string {
	if d := os.Getenv("MXCTL_CONFIG_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "mxctl")
}

func configPath() string {
	return filepath.Join(configDir(), "config.toml")
}
