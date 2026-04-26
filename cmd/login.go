package cmd

import (
	"fmt"
	"os"
	"syscall"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"mxctl/internal/matrix"
)

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Authenticate with the Matrix homeserver",
	RunE: func(cmd *cobra.Command, args []string) error {
		homeserver, _ := cmd.Flags().GetString("homeserver")
		userID, _ := cmd.Flags().GetString("user")

		if homeserver == "" {
			homeserver = "https://matrix.cloud-surf.com"
		}
		if userID == "" {
			userID = "@eugene:matrix.cloud-surf.com"
		}

		fmt.Printf("Homeserver: %s\n", homeserver)
		fmt.Printf("User: %s\n", userID)
		fmt.Print("Password: ")

		pw, err := term.ReadPassword(int(syscall.Stdin))
		fmt.Println()
		if err != nil {
			return fmt.Errorf("read password: %w", err)
		}

		fmt.Println("Logging in...")
		authCfg, err := matrix.Login(homeserver, userID, string(pw))
		if err != nil {
			return err
		}

		if err := os.MkdirAll(configDir(), 0700); err != nil {
			return err
		}

		// Preserve existing config (plugins, aliases, etc); only update auth fields.
		cfg, _ := matrix.LoadConfig(configPath())
		if cfg == nil {
			cfg = authCfg
		} else {
			cfg.Homeserver = authCfg.Homeserver
			cfg.UserID = authCfg.UserID
			cfg.AccessToken = authCfg.AccessToken
			cfg.DeviceID = authCfg.DeviceID
		}

		if err := matrix.SaveConfig(configPath(), cfg); err != nil {
			return err
		}

		fmt.Printf("Logged in as %s (device: %s)\n", cfg.UserID, cfg.DeviceID)
		fmt.Printf("Config saved to %s\n", configPath())
		return nil
	},
}

func init() {
	loginCmd.Flags().StringP("homeserver", "s", "", "homeserver URL (default: https://matrix.cloud-surf.com)")
	loginCmd.Flags().StringP("user", "u", "", "Matrix user ID (default: @eugene:matrix.cloud-surf.com)")
	rootCmd.AddCommand(loginCmd)
}
